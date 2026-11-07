package preview

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ServeHTTP is the handler of a preview listener. The hub API is never
// served here; /__nb/* is reserved (launch, bridge.js, log).
func (p *preview) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/__nb/launch" {
		p.launch(w, r)
		return
	}
	if p.fromAnotherTile(r) {
		page(w, http.StatusForbidden, "Refused", "Another tile's page may not call this one.")
		return
	}
	if !p.authed(w, r) {
		page(w, http.StatusUnauthorized, "Open this preview from NextBoard",
			"This address is a NextBoard preview. Open it from its web tile (or “Open in new tab”) on the board.")
		return
	}
	p.touch()
	switch {
	case r.URL.Path == "/__nb/bridge.js":
		p.serveBridge(w, r)
		return
	case r.URL.Path == "/__nb/log":
		p.serveLog(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/__nb/"):
		http.NotFound(w, r)
		return
	}
	// A service worker would outlive the site on this port, and answer for whatever comes next.
	if p.site != nil && r.Header.Get("Service-Worker") != "" {
		http.NotFound(w, r)
		return
	}
	if r.ContentLength > maxRequestBody {
		page(w, http.StatusRequestEntityTooLarge, "Request too large", "Preview requests are limited to 100 MB.")
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	}
	// The response rewriter needs the preview origin's host; the
	// outgoing request carries it in its context.
	ctx := context.WithValue(r.Context(), inHostKey{}, r.Host)
	r = r.WithContext(context.WithValue(ctx, inSchemeKey{}, SchemeOf(r)))
	p.proxy().ServeHTTP(w, r)
}

// fromAnotherTile reports a call from a page on another port of this host. Browsers treat the
// ports of one host as one site, so such a call carries this listener's cookies and SameSite does
// not stop it. A site takes none, and a dev server none from a site, which may be anyone's page.
func (p *preview) fromAnotherTile(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == r.Host || u.Hostname() != stripPort(r.Host) {
		return false
	}
	if p.site != nil {
		return true
	}
	port, _ := strconv.Atoi(u.Port())
	return p.m.siteListener(port)
}

func stripPort(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

// launch trades a single-use token for the listener's cookie.
func (p *preview) launch(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("t")
	p.m.mu.Lock()
	t, ok := p.tokens[tok]
	if ok {
		delete(p.tokens, tok)
	}
	valid := ok && tok != "" && p.m.now().Before(t.exp)
	var val string
	if valid {
		val = randToken()
		p.cookies[val] = p.m.now().Add(CookieTTL)
		p.LastUsedAt = p.m.now().UTC()
	}
	p.m.mu.Unlock()
	if !valid {
		page(w, http.StatusUnauthorized, "Open this preview from NextBoard", "This preview link was already used or has expired. Open the preview again from its tile.")
		return
	}
	to := r.URL.Query().Get("to")
	if c, err := cleanTarget(to); err != nil || c == "" {
		to = "/"
	} else {
		to = c
	}
	p.setCookie(w, r, val)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, to, http.StatusFound)
}

func (p *preview) setCookie(w http.ResponseWriter, r *http.Request, val string) {
	scheme := SchemeOf(r)
	http.SetCookie(w, &http.Cookie{Name: CookieName(p.ListenPort, scheme), Value: val, Path: "/", MaxAge: int(CookieTTL / time.Second),
		HttpOnly: true, Secure: scheme == "https", SameSite: http.SameSiteLaxMode})
}

// authed checks the cookie (sliding 12 h; re-sent at most hourly).
func (p *preview) authed(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie(CookieName(p.ListenPort, SchemeOf(r)))
	if err != nil || c.Value == "" {
		return false
	}
	now := p.m.now()
	p.m.mu.Lock()
	exp, ok := p.cookies[c.Value]
	if ok && now.After(exp) {
		delete(p.cookies, c.Value)
		ok = false
	}
	renew := false
	if ok {
		renew = exp.Sub(now) < CookieTTL-time.Hour
		p.cookies[c.Value] = now.Add(CookieTTL)
	}
	p.m.mu.Unlock()
	if renew {
		p.setCookie(w, r, c.Value)
	}
	return ok
}

// page writes a small self-contained HTML page (no scripts).
func page(w http.ResponseWriter, status int, title, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>%s</title>`+
		`<style>body{font:14px system-ui,sans-serif;margin:0;display:grid;place-items:center;min-height:100vh;background:#0f1115;color:#c9ced6}`+
		`main{max-width:28rem;padding:24px;text-align:center}h1{font-size:16px;font-weight:600;color:#e6e9ee}</style></head>`+
		`<body><main><h1>%s</h1><p>%s</p></main></body></html>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(body))
}

// transport dials the target port of the preview's machine, or the site
// on the tailnet. https upstreams may be self-signed on loopback, and at a
// bare Tailscale IP, which no certificate names and WireGuard authenticates
// anyway; a site's name must have a certificate that holds.
func (m *Manager) transport(p *preview) *http.Transport {
	machineID, host, port := p.MachineID, p.TargetHost, p.TargetPort
	addr, addrErr := netip.ParseAddr(host)
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, DialTimeout)
			defer cancel()
			var c net.Conn
			var err error
			switch {
			case host != "":
				c, err = m.dialSite(ctx, host, port)
			case m.cfg.Dial == nil:
				return nil, errors.New("dial is not available")
			default:
				c, err = m.cfg.Dial(ctx, machineID, port)
			}
			if err != nil {
				return nil, &dialError{err}
			}
			return c, nil
		},
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: host == "" || (addrErr == nil && IsTailnet(addr))}, //nolint:gosec // see above
		ResponseHeaderTimeout: HeaderTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
	}
}

type dialError struct{ err error }

func (e *dialError) Error() string { return e.err.Error() }
func (e *dialError) Unwrap() error { return e.err }

// proxy builds the reverse proxy for one request (cheap; the transport
// is shared).
func (p *preview) proxy() *httputil.ReverseProxy {
	p.m.mu.Lock()
	scheme, port := p.TargetScheme, p.TargetPort
	p.m.mu.Unlock()
	target := "localhost:" + strconv.Itoa(port)
	if p.site != nil {
		target = p.site.authority
	}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme, out.URL.Host = scheme, target
			out.Host = target
			if o := out.Header.Get("Origin"); o != "" && o != "null" {
				out.Header.Set("Origin", scheme+"://"+target)
			}
			if ref := out.Header.Get("Referer"); ref != "" {
				if u, err := url.Parse(ref); err == nil && u.Host == pr.In.Host {
					u.Scheme, u.Host = scheme, target
					out.Header.Set("Referer", u.String())
				} else {
					out.Header.Del("Referer")
				}
			}
			if p.site != nil {
				p.site.requestCookies(out.Header)
				out.Header.Del("X-Real-Ip")
			} else {
				stripHubCookies(out.Header)
			}
			if strings.Contains(strings.ToLower(pr.In.Header.Get("Accept-Encoding")), "gzip") {
				out.Header.Set("Accept-Encoding", "gzip, identity")
			} else {
				out.Header.Set("Accept-Encoding", "identity")
			}
			out.Header.Del("X-Forwarded-For")
			out.Header.Del("Forwarded")
		},
		Transport:     p.tr,
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			p.m.setState(p.ID, StateLive, "")
			rewriteResponse(resp, port, resp.Request, p.m.frameAncestors(p.ListenPort, schemeOfOut(resp.Request), hostOf(resp.Request)), p.site)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var de *dialError
			if p.site != nil {
				p.siteError(w, err)
				return
			}
			if errors.As(err, &de) {
				st, msg := stateFor(de.err)
				p.m.setState(p.ID, st, msg)
				if st == StateError {
					page(w, http.StatusBadGateway, "Preview unavailable", msg)
					return
				}
				page(w, http.StatusBadGateway, fmt.Sprintf("Waiting for the dev server on :%d…", port),
					"Nothing answers on this port yet. Start the dev server; the tile retries on its own.")
				return
			}
			if errors.Is(err, context.Canceled) {
				return
			}
			page(w, http.StatusBadGateway, "Preview error", err.Error())
		},
		ErrorLog: nil,
	}
}

// siteError shows why a site did not answer.
func (p *preview) siteError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	var cert *tls.CertificateVerificationError
	if errors.As(err, &cert) {
		page(w, http.StatusBadGateway, "This site's certificate is not trusted",
			p.TargetHost+" answered with a certificate this hub does not trust. Open it by its Tailscale IP, or in a real browser from the tile's menu.")
		return
	}
	var de *dialError
	if errors.As(err, &de) {
		msg := p.TargetHost + " cannot be reached: " + de.err.Error()
		p.m.setState(p.ID, StateError, msg)
		page(w, http.StatusBadGateway, "Cannot reach "+p.TargetHost, msg)
		return
	}
	page(w, http.StatusBadGateway, "Preview error", err.Error())
}

// hostOf is the preview origin host (hostname:listenPort) of the
// browser's request; resp.Request is the outgoing request, so the host
// travels in its context.
func schemeOfOut(r *http.Request) string {
	if s, ok := r.Context().Value(inSchemeKey{}).(string); ok {
		return s
	}
	return "http"
}

func hostOf(r *http.Request) string {
	if r == nil {
		return ""
	}
	if h, ok := r.Context().Value(inHostKey{}).(string); ok {
		return h
	}
	return ""
}

type inHostKey struct{}

type inSchemeKey struct{}

// stripHubCookies removes NextBoard's own cookies (hub session and every
// preview cookie) before a request reaches the dev server.
func stripHubCookies(h http.Header) {
	vals := h.Values("Cookie")
	if len(vals) == 0 {
		return
	}
	var keep []string
	for _, v := range vals {
		for _, part := range strings.Split(v, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
			if name == "" || isHubCookie(name) {
				continue
			}
			keep = append(keep, strings.TrimSpace(part))
		}
	}
	h.Del("Cookie")
	if len(keep) > 0 {
		h.Set("Cookie", strings.Join(keep, "; "))
	}
}

func isHubCookie(name string) bool {
	return name == "nb_session" || name == "nb_session_http" || strings.HasPrefix(name, "nb_pv") || strings.HasPrefix(name, siteCookiePrefix)
}

var loopbackLocation = regexp.MustCompile(`(?i)^(https?)://(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(:(\d+))?(/.*)?$`)

// rewriteResponse applies §4.4: frame-blocking headers out, CSP fixed for
// the bridge, loopback redirects back to the preview origin, cookie
// domains dropped, NextBoard cookies protected, HTML gets the bridge. A
// site has its own URLs replaced too (§4.9).
func rewriteResponse(resp *http.Response, port int, out *http.Request, ancestors string, st *site) {
	h := resp.Header
	host := hostOf(out)
	scheme := schemeOfOut(out)
	h.Del("X-Frame-Options")
	h.Del("Strict-Transport-Security")
	h.Del("Clear-Site-Data") // would wipe the hub's cookies on this host
	h.Del("Alt-Svc")
	for _, k := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only"} {
		vals := h.Values(k)
		if len(vals) == 0 {
			continue
		}
		h.Del(k)
		for _, v := range vals {
			if nv := fixCSP(v, scheme != "https"); nv != "" {
				h.Add(k, nv)
			}
		}
	}
	if st != nil && host != "" {
		st.rewriteHeaders(h, scheme+"://"+host)
	}
	if ancestors != "" && resp.StatusCode != http.StatusSwitchingProtocols {
		h.Add("Content-Security-Policy", "frame-ancestors "+ancestors)
	}
	if loc := h.Get("Location"); st == nil && loc != "" && host != "" {
		if m := loopbackLocation.FindStringSubmatch(loc); m != nil {
			lp := m[4]
			if lp == "" {
				lp = map[string]string{"http": "80", "https": "443"}[strings.ToLower(m[1])]
			}
			if lp == strconv.Itoa(port) {
				h.Set("Location", scheme+"://"+host+m[5])
			}
		}
	}
	if cs := h.Values("Set-Cookie"); len(cs) > 0 {
		h.Del("Set-Cookie")
		for _, c := range cs {
			if nc := fixSetCookie(c, scheme != "https"); nc != "" {
				if st != nil {
					nc = st.cookiePrefix + nc
				}
				h.Add("Set-Cookie", nc)
			}
		}
	}
	if st != nil && host != "" && out.Method != http.MethodHead && rewritable(h.Get("Content-Type")) &&
		resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusNotModified && resp.StatusCode != http.StatusSwitchingProtocols {
		st.rewriteBody(resp, scheme+"://"+host)
	}
	if resp.StatusCode == http.StatusOK && isHTML(h.Get("Content-Type")) && out.Method != http.MethodHead {
		injectBridge(resp)
	}
}

func isHTML(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	return strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/xhtml+xml")
}

// fixCSP drops frame-ancestors and makes sure the bridge (same-origin
// script and beacon) may run. On plain HTTP it also drops the upgrade to
// https, which the preview origin does not have.
func fixCSP(v string, plain bool) string {
	var out []string
	for _, d := range strings.Split(v, ";") {
		f := strings.Fields(d)
		if len(f) == 0 {
			continue
		}
		name := strings.ToLower(f[0])
		switch name {
		case "frame-ancestors":
			continue
		case "upgrade-insecure-requests":
			if plain {
				continue
			}
		case "script-src", "script-src-elem", "default-src", "connect-src":
			hasSelf := false
			var srcs []string
			for _, s := range f[1:] {
				ls := strings.ToLower(s)
				if ls == "'none'" {
					continue
				}
				if ls == "'self'" {
					hasSelf = true
				}
				srcs = append(srcs, s)
			}
			if !hasSelf {
				srcs = append([]string{"'self'"}, srcs...)
			}
			f = append([]string{f[0]}, srcs...)
		}
		out = append(out, strings.Join(f, " "))
	}
	return strings.Join(out, "; ")
}

// fixSetCookie drops the Domain attribute (the cookie stays on the preview
// host) and refuses cookies named like NextBoard's own. A preview on plain
// HTTP drops Secure as well, or the browser would refuse the cookie; then
// SameSite=None, which needs Secure, becomes Lax.
func fixSetCookie(c string, plain bool) string {
	parts := strings.Split(c, ";")
	name, _, _ := strings.Cut(strings.TrimSpace(parts[0]), "=")
	if isHubCookie(strings.TrimSpace(name)) {
		return ""
	}
	keep := []string{strings.TrimSpace(parts[0])}
	for _, a := range parts[1:] {
		k, v, _ := strings.Cut(strings.TrimSpace(a), "=")
		k = strings.TrimSpace(k)
		if strings.EqualFold(k, "domain") || (plain && strings.EqualFold(k, "secure")) {
			continue
		}
		if plain && strings.EqualFold(k, "samesite") && strings.EqualFold(strings.TrimSpace(v), "none") {
			a = "SameSite=Lax"
		}
		keep = append(keep, strings.TrimSpace(a))
	}
	return strings.Join(keep, "; ")
}

const bridgeTag = `<script src="/__nb/bridge.js"></script>`

// injectBridge inserts the bridge after <head…> within the first 64 KiB
// (or at the start), un-gzipping when needed; other encodings are left
// alone.
func injectBridge(resp *http.Response) {
	body := resp.Body
	switch enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(body)
		if err != nil {
			return
		}
		body = struct {
			io.Reader
			io.Closer
		}{zr, resp.Body}
		resp.Header.Del("Content-Encoding")
	default:
		return
	}
	resp.Header.Del("Content-Length")
	resp.Header.Del("Content-MD5")
	resp.ContentLength = -1
	resp.Body = &injector{src: body, br: bufio.NewReaderSize(body, 64<<10)}
}

// injector streams the body with the bridge tag inserted once.
type injector struct {
	src  io.ReadCloser
	br   *bufio.Reader
	head []byte // pending prefix (with the tag) still to be returned
	done bool
}

var headRe = regexp.MustCompile(`(?is)<head(\s[^>]*)?>`)

func (in *injector) Read(b []byte) (int, error) {
	if !in.done {
		in.done = true
		buf := make([]byte, 64<<10)
		n, err := io.ReadFull(in.br, buf)
		buf = buf[:n]
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if loc := headRe.FindIndex(buf); loc != nil {
			in.head = append(append(append([]byte{}, buf[:loc[1]]...), bridgeTag...), buf[loc[1]:]...)
		} else {
			in.head = append([]byte(bridgeTag), buf...)
		}
	}
	if len(in.head) > 0 {
		n := copy(b, in.head)
		in.head = in.head[n:]
		return n, nil
	}
	return in.br.Read(b)
}

func (in *injector) Close() error { return in.src.Close() }

// frameAncestors is the CSP frame-ancestors source list of a preview: the
// preview itself and the hub origins.
func (m *Manager) frameAncestors(listenPort int, scheme, host string) string {
	if host == "" {
		return ""
	}
	srcs := []string{"'self'"}
	if m.cfg.HubOrigins != nil {
		for _, o := range m.cfg.HubOrigins(scheme, host) {
			if !strings.ContainsAny(o, " ;,'\"") {
				srcs = append(srcs, o)
			}
		}
	}
	return strings.Join(srcs, " ")
}
