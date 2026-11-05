package preview

// A site (docs/API-studio.md §4.9): a preview can also stand for an address on the tailnet or on
// the public internet, so that it loads in the web tile instead of a real browser. Every
// connection resolves the name again and only ever dials such an address, so a name that changes
// later cannot lead the proxy to This PC or its network. A site is not the user's own dev server,
// so it also gets its own cookie jar: cookies are scoped by host, not port, and would otherwise
// reach every other tile on this hostname.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// siteCookiePrefix starts the name of every cookie of a site in the browser.
const siteCookiePrefix = "nb_c_"

var (
	tailnet4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnet6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	// Inside those ranges, but no node of the tailnet: Tailscale's own resolver, and the range
	// Tailscale leaves to ChromeOS VMs.
	tailnetService4 = netip.MustParseAddr("100.100.100.100")
	tailnetService6 = netip.MustParseAddr("fd7a:115c:a1e0::53")
	chromeOSVMs     = netip.MustParsePrefix("100.115.92.0/23")
	// Global unicast by the book, but no place on the internet. NAT64 and 6to4 can wrap any
	// IPv4 address, private ones too.
	notPublic = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"),
	}
)

// IsPublic reports whether a is an address on the public internet: not This PC, not its local
// network, not a cloud's metadata service.
func IsPublic(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// IsTailnet reports whether a is the Tailscale IP of a node.
func IsTailnet(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	if a == tailnetService4 || a == tailnetService6 || chromeOSVMs.Contains(a) {
		return false
	}
	return tailnet4.Contains(a) || tailnet6.Contains(a)
}

var (
	errPrivate = errors.New("neither public nor a Tailscale address")
	errNoHost  = errors.New("no such host")
	errOwnPort = errors.New("port of this hub")
)

// parseSiteURL splits the address of a site: host in lower case and without brackets, the port
// spelled out, and what goes after the host.
func parseSiteURL(raw string) (scheme, host string, port int, path string, err *Error) {
	bad := newErr(http.StatusBadRequest, "bad_url", "alamat harus http atau https dengan host")
	if len(raw) > 2048 {
		return "", "", 0, "", bad
	}
	u, perr := url.Parse(raw)
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" {
		return "", "", 0, "", bad
	}
	port = defaultPort(u.Scheme)
	if p := u.Port(); p != "" {
		if port, perr = strconv.Atoi(p); perr != nil || port < 1 || port > 65535 {
			return "", "", 0, "", bad
		}
	}
	path = u.RequestURI()
	if u.Fragment != "" {
		path += "#" + u.EscapedFragment()
	}
	return u.Scheme, strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), port, path, nil
}

func defaultPort(scheme string) int {
	if scheme == "https" {
		return 443
	}
	return 80
}

// siteAddrs resolves host down to the addresses a preview may dial.
func (m *Manager) siteAddrs(ctx context.Context, host string, port int) ([]netip.Addr, error) {
	var found []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		found = []netip.Addr{a}
	} else if found, err = m.cfg.Resolve(ctx, host); err != nil {
		return nil, errNoHost
	}
	var out []netip.Addr
	own := false
	for _, a := range found {
		switch {
		case !IsTailnet(a) && !IsPublic(a):
		case m.cfg.LocalAddr(a) && (m.InRange(port) || (m.cfg.LocalReserved != nil && m.cfg.LocalReserved(port))):
			own = true
		default:
			out = append(out, a.Unmap())
		}
	}
	if len(out) == 0 && own {
		return nil, errOwnPort
	}
	if len(out) == 0 {
		return nil, errPrivate
	}
	return out, nil
}

// dialSite connects to the first allowed address of host that answers.
func (m *Manager) dialSite(ctx context.Context, host string, port int) (net.Conn, error) {
	addrs, err := m.siteAddrs(ctx, host, port)
	if err != nil {
		return nil, err
	}
	var last error
	for _, a := range addrs {
		c, err := m.cfg.DialAddr(ctx, netip.AddrPortFrom(a, uint16(port)))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

func dialAddr(ctx context.Context, a netip.AddrPort) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", a.String())
}

func resolveHost(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func isLocalAddr(a netip.Addr) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, ia := range addrs {
		if n, ok := ia.(*net.IPNet); ok {
			if own, ok := netip.AddrFromSlice(n.IP); ok && own.Unmap() == a.Unmap() {
				return true
			}
		}
	}
	return false
}

// site rewrites what a site says about itself, so that it stays on the preview origin.
type site struct {
	// authority is the Host of the site: [v6] in brackets, the port only if it is not the usual one.
	authority    string
	cookiePrefix string
	host         []byte
	urls         *regexp.Regexp
	// keep is more than the longest match, so that none is lost between two chunks of a body.
	keep int
}

func newSite(scheme, hostname string, port int) *site {
	h := hostname
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	authority, portRe := h, `(?::`+strconv.Itoa(port)+`)?`
	if port != defaultPort(scheme) {
		authority, portRe = h+":"+strconv.Itoa(port), `:`+strconv.Itoa(port)
	}
	sum := sha256.Sum256([]byte(hostname + ":" + strconv.Itoa(port)))
	return &site{
		authority:    authority,
		cookiePrefix: siteCookiePrefix + hex.EncodeToString(sum[:4]) + "_",
		host:         []byte(h),
		// The same host on another port stays as it is: that is another server, and the browser
		// loads it by itself (Vite beside Laravel).
		urls: regexp.MustCompile(`((?i:https?|wss?):)?(\\?/\\?/)` + regexp.QuoteMeta(h) + portRe + `([^A-Za-z0-9.:\-]|$)`),
		keep: len(`https:\/\/`) + len(h) + len(":65535") + 1,
	}
}

// rewrite replaces the site's own URLs in b with origin. Unless final, the end of b stays
// behind for the next call, since a URL can continue there.
func (s *site) rewrite(b []byte, origin string, final bool) (out, rest []byte) {
	cut := len(b)
	if !final {
		cut -= s.keep
		if cut <= 0 {
			return nil, b
		}
	}
	last := 0
	if bytes.Contains(b, s.host) {
		pScheme, pHost, _ := strings.Cut(origin, "://")
		for _, m := range s.urls.FindAllSubmatchIndex(b, -1) {
			if m[0] >= cut {
				break
			}
			out = append(out, b[last:m[0]]...)
			if m[2] >= 0 {
				out = append(append(out, schemeFor(string(b[m[2]:m[3]]), pScheme)...), ':')
			}
			out = append(append(out, b[m[4]:m[5]]...), pHost...)
			out = append(out, b[m[6]:m[7]]...)
			last = m[1]
		}
	}
	if last < cut {
		out = append(out, b[last:cut]...)
		last = cut
	}
	return out, append([]byte(nil), b[last:]...)
}

// schemeFor is the scheme on the preview origin for a URL that had the scheme "http:" or "ws:"…
func schemeFor(had, preview string) string {
	if strings.HasPrefix(strings.ToLower(had), "ws") {
		if preview == "https" {
			return "wss"
		}
		return "ws"
	}
	return preview
}

func (s *site) rewriteString(v, origin string) string {
	out, _ := s.rewrite([]byte(v), origin, true)
	return string(out)
}

// rewriteHeaders covers Location, Link, the CSP and whatever else names the site.
func (s *site) rewriteHeaders(h http.Header, origin string) {
	for k, vals := range h {
		if k == "Set-Cookie" {
			continue
		}
		for i, v := range vals {
			vals[i] = s.rewriteString(v, origin)
		}
	}
}

// requestCookies sends the site only its own cookies, under the names it gave them.
func (s *site) requestCookies(h http.Header) {
	var keep []string
	for _, v := range h.Values("Cookie") {
		for _, part := range strings.Split(v, ";") {
			if own, ok := strings.CutPrefix(strings.TrimSpace(part), s.cookiePrefix); ok {
				keep = append(keep, own)
			}
		}
	}
	h.Del("Cookie")
	if len(keep) > 0 {
		h.Set("Cookie", strings.Join(keep, "; "))
	}
}

// rewritable reports whether a body of this type can name the site. An event stream stays out:
// holding back its end would delay every event.
func rewritable(ct string) bool {
	ct, _, _ = strings.Cut(strings.ToLower(ct), ";")
	ct = strings.TrimSpace(ct)
	if ct == "text/event-stream" {
		return false
	}
	return strings.HasPrefix(ct, "text/") || strings.Contains(ct, "javascript") || strings.Contains(ct, "json") || strings.Contains(ct, "xml")
}

// rewriteBody streams the body with the site's URLs replaced, un-gzipping when needed; other
// encodings are left alone (the proxy only ever asks for gzip).
func (s *site) rewriteBody(resp *http.Response, origin string) {
	body := resp.Body
	var src io.Reader = body
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(body)
		if err != nil {
			return
		}
		src = zr
		resp.Header.Del("Content-Encoding")
	default:
		return
	}
	resp.Header.Del("Content-Length")
	resp.Header.Del("Content-MD5")
	resp.Header.Del("Accept-Ranges")
	resp.ContentLength = -1
	resp.Body = &siteBody{src: src, closer: body, s: s, origin: origin}
}

// siteBody is a body with the site's URLs replaced.
type siteBody struct {
	src    io.Reader
	closer io.Closer
	s      *site
	origin string
	chunk  []byte
	held   []byte
	out    []byte
	eof    bool
}

func (b *siteBody) Read(p []byte) (int, error) {
	for len(b.out) == 0 {
		if b.eof {
			return 0, io.EOF
		}
		if b.chunk == nil {
			b.chunk = make([]byte, 32<<10)
		}
		n, err := b.src.Read(b.chunk)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		b.eof = err != nil
		b.out, b.held = b.s.rewrite(append(b.held, b.chunk[:n]...), b.origin, b.eof)
	}
	n := copy(p, b.out)
	b.out = b.out[n:]
	return n, nil
}

func (b *siteBody) Close() error { return b.closer.Close() }

// siteAddr is where a site is asked for a page.
type siteAddr struct {
	scheme, host string
	port         int
	path         string
}

func (a siteAddr) url() string {
	h := a.host
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	if a.port != defaultPort(a.scheme) {
		h += ":" + strconv.Itoa(a.port)
	}
	path, _, _ := strings.Cut(a.path, "#")
	return a.scheme + "://" + h + path
}

// sameSiteName: a name and its www are one site, which moved from one to the other.
func sameSiteName(a, b string) bool {
	return strings.TrimPrefix(a, "www.") == strings.TrimPrefix(b, "www.")
}

// needsBrowser is the answer for a site the proxy would only break.
func needsBrowser(host, why string) *Error {
	return newErr(http.StatusBadRequest, "needs_browser", "%s butuh browser sungguhan: %s", host, why)
}

// probeSite asks a public site for the page once, before a listener opens for it. It finds out
// where the page really is, since a redirect to https or to www would otherwise lead the frame out
// of the proxy. It also spares a tile the pages that cannot work: a redirect to another host keeps
// its login there, and a site that refuses the hub has nothing to show but its refusal. Every hop
// goes through dialSite, so a redirect cannot reach anything a typed address could not.
func (m *Manager) probeSite(ua string, at siteAddr) (siteAddr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			h, p, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			port, err := strconv.Atoi(p)
			if err != nil {
				return nil, err
			}
			c, err := m.dialSite(ctx, h, port)
			if err != nil {
				return nil, &dialError{err}
			}
			return c, nil
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for range maxSiteRedirects {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, at.url(), nil)
		if err != nil {
			return at, newErr(http.StatusBadRequest, "bad_url", "alamat tidak valid")
		}
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*")
		if ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		resp, err := cl.Do(req)
		if err != nil {
			var de *dialError
			if errors.As(err, &de) {
				return at, newErr(http.StatusBadGateway, "site_unreachable", "%s tidak terjangkau: %v", at.host, de.err)
			}
			return at, needsBrowser(at.host, "tidak menjawab lewat hub")
		}
		resp.Body.Close()
		switch {
		case resp.Header.Get("Cf-Mitigated") != "", resp.StatusCode == http.StatusForbidden,
			resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusServiceUnavailable:
			return at, needsBrowser(at.host, "menolak permintaan dari hub")
		case resp.StatusCode < 300 || resp.StatusCode >= 400 || resp.Header.Get("Location") == "":
			return at, nil
		}
		next, err := req.URL.Parse(resp.Header.Get("Location"))
		if err != nil {
			return at, needsBrowser(at.host, "redirect tidak valid")
		}
		scheme, host, port, path, perr := parseSiteURL(next.String())
		if perr != nil {
			return at, needsBrowser(at.host, "redirect tidak valid")
		}
		if !sameSiteName(at.host, host) {
			return at, needsBrowser(at.host, "pindah ke "+host)
		}
		at = siteAddr{scheme, host, port, path}
	}
	return at, needsBrowser(at.host, "terlalu banyak redirect")
}
