package preview

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const testSite = "portal.example.net"

// siteEnv is a preview env whose tailnet is one upstream: every Tailscale IP dials it.
type siteEnv struct {
	*env
	addr   string
	mu     sync.Mutex
	names  map[string][]netip.Addr
	dialed []netip.Addr
	seen   []*http.Request
}

func addrs(list ...string) []netip.Addr {
	out := make([]netip.Addr, len(list))
	for i, s := range list {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

func newSiteEnv(t *testing.T, addr string) *siteEnv {
	t.Helper()
	s := &siteEnv{env: newEnv(t), addr: addr, names: map[string][]netip.Addr{testSite: addrs("100.64.10.20")}}
	s.m.cfg.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if a, ok := s.names[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
	s.m.cfg.DialAddr = func(ctx context.Context, a netip.AddrPort) (net.Conn, error) {
		s.mu.Lock()
		s.dialed = append(s.dialed, a.Addr())
		s.mu.Unlock()
		var d net.Dialer
		return d.DialContext(ctx, "tcp", s.addr)
	}
	s.m.cfg.LocalAddr = func(netip.Addr) bool { return false }
	return s
}

func (s *siteEnv) open(t *testing.T, url string) Preview {
	t.Helper()
	p, lp, _, err := s.m.Create(CreateRequest{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	s.launch(p, lp)
	return p
}

func codeOf(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func TestIsTailnet(t *testing.T) {
	for addr, want := range map[string]bool{
		"100.64.0.1":           true,
		"100.64.10.20":        true,
		"100.127.255.254":      true,
		"::ffff:100.64.10.20": true,
		"fd7a:115c:a1e0::1":    true,
		"100.63.255.255":       false,
		"100.128.0.1":          false,
		"100.100.100.100":      false,
		"100.115.92.1":         false,
		"fd7a:115c:a1e0::53":   false,
		"127.0.0.1":            false,
		"::1":                  false,
		"10.0.0.1":             false,
		"192.168.1.1":          false,
		"169.254.169.254":      false,
		"8.8.8.8":              false,
		"fd7a:115c:a1e1::1":    false,
	} {
		if got := IsTailnet(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
}

func TestSiteCreateRules(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	s := newSiteEnv(t, up.Listener.Addr().String())
	s.names["inside.example.net"] = addrs("127.0.0.1")
	s.names["mixed.example.net"] = addrs("10.0.0.9", "127.0.0.1", "100.64.10.21")

	for url, code := range map[string]string{
		"ftp://" + testSite + "/":                          "bad_url",
		"javascript:alert(1)":                              "bad_url",
		"https://user:pw@" + testSite:                      "bad_url",
		"https://":                                         "bad_url",
		"https://" + testSite + ":70000/":                  "bad_url",
		"https://inside.example.net/":                      "private_address",
		"https://nowhere.example.net/":                     "no_such_host",
		"http://127.0.0.1:" + strconv.Itoa(s.upPort) + "/": "private_address",
		"http://localhost/":                                "no_such_host",
		"http://10.0.0.1/":                                 "private_address",
		"http://169.254.169.254/latest/":                   "private_address",
		"http://100.100.100.100/":                          "private_address",
	} {
		if _, _, _, err := s.m.Create(CreateRequest{URL: url}); codeOf(err) != code {
			t.Errorf("%s: %v (%s)", url, err, codeOf(err))
		}
	}
	if n := len(s.dialed); n != 0 {
		t.Fatalf("refused addresses were dialed: %v", s.dialed)
	}
	if n := len(s.m.List()); n != 0 {
		t.Fatalf("a refused site opened a listener: %d", n)
	}

	// Of a name with local addresses too, only the Tailscale IP is ever dialed.
	p, _, _, err := s.m.Create(CreateRequest{URL: "https://mixed.example.net/x?y=1#z"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.dialed) != 1 || s.dialed[0].String() != "100.64.10.21" {
		t.Fatalf("dialed %v", s.dialed)
	}
	if p.TargetHost != "mixed.example.net" || p.TargetPort != 443 || p.TargetScheme != "https" || p.Path != "/x?y=1#z" || p.State != StateLive {
		t.Fatalf("preview %+v", p)
	}

	// A literal Tailscale IP, upper case names and the same site again.
	if lit, _, _, err := s.m.Create(CreateRequest{URL: "http://100.64.10.20:8443/"}); err != nil || lit.TargetHost != "100.64.10.20" || lit.TargetPort != 8443 {
		t.Fatalf("literal %+v %v", lit, err)
	}
	a, _, created, err := s.m.Create(CreateRequest{URL: "https://" + strings.ToUpper(testSite) + "/one"})
	if err != nil || !created || a.TargetHost != testSite {
		t.Fatalf("first %+v %v %v", a, created, err)
	}
	b, _, created, err := s.m.Create(CreateRequest{URL: "https://" + testSite + "/two"})
	if err != nil || created || b.ID != a.ID {
		t.Fatalf("the same site must share its preview: %+v %v %v", b, created, err)
	}
	if c, _, created, err := s.m.Create(CreateRequest{URL: "https://" + testSite + ":8443/"}); err != nil || !created || c.ID == a.ID {
		t.Fatalf("another port is another preview: %+v %v %v", c, created, err)
	}

	// Nothing answers.
	for _, old := range s.m.List() {
		if err := s.m.Delete(old.ID); err != nil {
			t.Fatal(err)
		}
	}
	s.m.cfg.DialAddr = func(context.Context, netip.AddrPort) (net.Conn, error) { return nil, errors.New("no route") }
	if _, _, _, err := s.m.Create(CreateRequest{URL: "https://" + testSite + ":9000/"}); codeOf(err) != "site_unreachable" {
		t.Fatalf("unreachable: %v", err)
	}

	// This PC's own Tailscale IP may carry other sites, but not the hub itself.
	s.m.cfg.DialAddr = func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", up.Listener.Addr().String())
	}
	s.m.cfg.LocalAddr = func(a netip.Addr) bool { return a.String() == "100.64.10.20" }
	if _, _, _, err := s.m.Create(CreateRequest{URL: "http://100.64.10.20:12312/"}); codeOf(err) != "bad_port" {
		t.Fatalf("hub port: %v", err)
	}
	lo, _ := s.m.PortRange()
	if _, _, _, err := s.m.Create(CreateRequest{URL: "http://100.64.10.20:" + strconv.Itoa(lo) + "/"}); codeOf(err) != "bad_port" {
		t.Fatalf("preview port: %v", err)
	}
	if _, _, _, err := s.m.Create(CreateRequest{URL: "http://100.64.10.20:9000/"}); err != nil {
		t.Fatalf("another port of this PC: %v", err)
	}
}

func TestSiteProxy(t *testing.T) {
	var s *siteEnv
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Clone(context.Background()))
		s.mu.Unlock()
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Content-Security-Policy", "default-src 'self' https://"+testSite+" https://"+testSite+":5177; upgrade-insecure-requests; frame-ancestors 'none'")
		h.Set("Link", "<https://"+testSite+"/a.css>; rel=preload")
		h.Set("X-Inertia-Location", "https://"+testSite+"/next")
		h.Add("Set-Cookie", "portal-session=abc; expires=Tue, 03 Nov 2026 16:04:54 GMT; Max-Age=7200; path=/; domain=.example.net; secure; httponly; samesite=lax")
		h.Add("Set-Cookie", "embed=1; Path=/; Secure; SameSite=None")
		h.Add("Set-Cookie", "nb_session=evil; Path=/")
		h.Add("Set-Cookie", "nb_c_00000000_other=evil; Path=/")
		io.WriteString(w, `<html><head><title>x</title></head><body>`+
			`<a id=https href="https://`+testSite+`/a">a</a><a id=explicit href="https://`+testSite+`:443/b">b</a>`+
			`<a id=http href="http://`+testSite+`/c">c</a><a id=rel href="//`+testSite+`/d">d</a>`+
			`<script>var z={"url":"https:\/\/`+testSite+`","ws":"wss://`+testSite+`/app"};</script>`+
			`<script type=module src="https://`+testSite+`:5177/@vite/client"></script>`+
			`<a id=longer href="https://`+testSite+`.evil.example/e">e</a><a id=sub href="https://x.`+testSite+`-b/f">f</a>`+
			`<a id=encoded href="/r?to=https%3A%2F%2F`+testSite+`%2Fg">g</a><span>`+testSite+`</span></body></html>`)
	}
	mux.HandleFunc("/", page)
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream") // a text body would be rewritten
		fmt.Fprintf(w, "host=%s\norigin=%s\nreferer=%s\ncookie=%s\nreal=%s\n", r.Host, r.Header.Get("Origin"), r.Header.Get("Referer"), r.Header.Get("Cookie"), r.Header.Get("X-Real-Ip"))
	})
	mux.HandleFunc("/to-self", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+testSite+"/after?x=1", http.StatusFound)
	})
	mux.HandleFunc("/to-other", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+testSite+".evil.example/after", http.StatusFound)
	})
	mux.HandleFunc("/to-loopback", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:443/after", http.StatusFound)
	})
	mux.HandleFunc("/gz.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		fmt.Fprintf(zw, `{"url":"https:\/\/%s\/x"}`, testSite)
		zw.Close()
	})
	mux.HandleFunc("/big.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("Content-Length", strconv.Itoa(bigLen))
		w.Header().Set("Accept-Ranges", "bytes")
		io.WriteString(w, bigBody)
	})
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "https://"+testSite+"/stays")
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: https://"+testSite+"/stays\n\n")
	})
	up := httptest.NewServer(mux)
	defer up.Close()
	s = newSiteEnv(t, up.Listener.Addr().String())

	// Without the launch cookie nothing passes.
	p, lp, _, err := s.m.Create(CreateRequest{URL: "https://" + testSite + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if p.State != StateLive {
		t.Fatalf("state %s", p.State)
	}
	if p.TargetHost != testSite {
		t.Fatalf("%+v", p)
	}
	// The site believes it is on https, while the upstream of this test speaks plain HTTP.
	s.m.mu.Lock()
	s.m.byID[p.ID].TargetScheme = "http"
	s.m.mu.Unlock()
	if resp, _ := s.get(s.base(p) + "/"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no cookie: %d", resp.StatusCode)
	}
	s.launch(p, lp)
	base := s.base(p)
	mine := "127.0.0.1:" + strconv.Itoa(p.ListenPort)

	// ---- the page ----
	resp, body := s.get(base + "/")
	if resp.Header.Get("X-Frame-Options") != "" {
		t.Fatal("frame blocker stayed")
	}
	for _, want := range []string{
		`<head><script src="/__nb/bridge.js"></script><title>`,
		`id=https href="` + base + `/a"`,
		`id=explicit href="` + base + `/b"`,
		`id=http href="` + base + `/c"`,
		`id=rel href="//` + mine + `/d"`,
		`"url":"http:\/\/` + mine + `"`,
		`"ws":"ws://` + mine + `/app"`,
		// another port of the host is another server, which the browser loads by itself
		`src="https://` + testSite + `:5177/@vite/client"`,
		// other hosts, encoded URLs and the bare name stay
		`https://` + testSite + `.evil.example/e`,
		`https://x.` + testSite + `-b/f`,
		`to=https%3A%2F%2F` + testSite + `%2Fg`,
		`<span>` + testSite + `</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s\nin %s", want, body)
		}
	}
	csp := strings.Join(resp.Header.Values("Content-Security-Policy"), " | ")
	if !strings.Contains(csp, "default-src 'self' "+base+" https://"+testSite+":5177") || strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "upgrade-insecure-requests") {
		t.Errorf("csp %s", csp)
	}
	if got := resp.Header.Get("Link"); got != "<"+base+"/a.css>; rel=preload" {
		t.Errorf("link %s", got)
	}
	if got := resp.Header.Get("X-Inertia-Location"); got != base+"/next" {
		t.Errorf("inertia %s", got)
	}

	// ---- cookies: a jar of its own, usable on plain HTTP ----
	cookies := resp.Header.Values("Set-Cookie")
	prefix := s.m.byID[p.ID].site.cookiePrefix
	if !strings.HasPrefix(prefix, "nb_c_") {
		t.Fatalf("prefix %s", prefix)
	}
	var session, embed string
	for _, c := range cookies {
		switch {
		case strings.HasPrefix(c, prefix+"portal-session="):
			session = c
		case strings.HasPrefix(c, prefix+"embed="):
			embed = c
		case strings.Contains(c, "evil"):
			t.Errorf("a NextBoard cookie name passed: %s", c)
		}
	}
	low := strings.ToLower(session)
	if session == "" || strings.Contains(low, "domain=") || strings.Contains(low, "secure") || !strings.Contains(low, "httponly") || !strings.Contains(low, "samesite=lax") {
		t.Errorf("session cookie %q", session)
	}
	if strings.Contains(strings.ToLower(embed), "secure") || !strings.Contains(embed, "SameSite=Lax") {
		t.Errorf("embed cookie %q", embed)
	}
	req := func(path string, hdr ...string) (*http.Response, string) {
		return s.get(base+path, hdr...)
	}
	_, echo := req("/echo", "Origin", base, "Referer", base+"/page?x=1", "X-Real-Ip", "203.0.113.9",
		"Cookie", "nb_session=hub; nb_session_http=hub; nb_c_11111111_x=otherSite; plain=1")
	want := "host=" + testSite + "\norigin=http://" + testSite + "\nreferer=http://" + testSite + "/page?x=1\ncookie=portal-session=abc; embed=1\nreal=\n"
	if echo != want {
		t.Errorf("echo\n%s\nwant\n%s", echo, want)
	}
	_, js := s.get(base + "/__nb/bridge.js")
	if !strings.Contains(js, `var COOKIES = "`+prefix+`";`) || strings.Contains(js, "__NB_COOKIE_PREFIX__") {
		t.Errorf("bridge without the prefix")
	}

	// A dev server on localhost never gets a site's cookies, and has no prefix in its bridge.
	lpv, llp := s.create(s.upPort)
	s.launch(lpv, llp)
	_, lecho := s.get(s.base(lpv)+"/json", "Cookie", prefix+"portal-session=abc; app=1")
	if strings.Contains(lecho, "portal-session") || !strings.Contains(lecho, "app=1") {
		t.Errorf("localhost got %s", lecho)
	}
	if _, ljs := s.get(s.base(lpv) + "/__nb/bridge.js"); !strings.Contains(ljs, `var COOKIES = "";`) {
		t.Errorf("localhost bridge")
	}

	// ---- redirects ----
	for path, want := range map[string]string{
		"/to-self":     base + "/after?x=1",
		"/to-other":    "https://" + testSite + ".evil.example/after",
		"/to-loopback": "http://localhost:443/after",
	} {
		if r, _ := req(path); r.Header.Get("Location") != want {
			t.Errorf("%s → %s", path, r.Header.Get("Location"))
		}
	}

	// ---- bodies ----
	if r, b := req("/gz.json"); b != `{"url":"http:\/\/`+mine+`\/x"}` || r.Header.Get("Content-Encoding") != "" {
		t.Errorf("gzip %q %v", b, r.Header)
	}
	r, b := req("/big.js")
	if want := strings.ReplaceAll(strings.ReplaceAll(bigBody, "https://"+testSite+"/", base+"/"), "https:\\/\\/"+testSite+"\\/", "http:\\/\\/"+mine+"\\/"); b != want {
		t.Errorf("big body differs, %d bytes, want %d", len(b), len(want))
	}
	if r.Header.Get("Accept-Ranges") != "" {
		t.Errorf("ranges of a rewritten body: %v", r.Header)
	}
	if _, b := req("/logo.png"); !strings.Contains(b, testSite) {
		t.Errorf("an image was rewritten: %s", b)
	}
	if _, b := req("/events"); !strings.Contains(b, testSite) {
		t.Errorf("an event stream was held back: %s", b)
	}
}

const bigLen = 400_000

// bigBody has URLs at every offset around the size of the reads.
var bigBody = func() string {
	var b strings.Builder
	for b.Len() < bigLen-200 {
		b.WriteString(strings.Repeat("x", b.Len()%97))
		b.WriteString("https://" + testSite + "/a;")
		b.WriteString("https:\\/\\/" + testSite + "\\/b;")
	}
	for b.Len() < bigLen {
		b.WriteByte('y')
	}
	return b.String()
}()

func TestSiteRewriteAcrossChunks(t *testing.T) {
	st := newSite("https", testSite, 443)
	in := []byte("<a href=\"https://" + testSite + "/a\">https://" + testSite + ".evil/</a> //" + testSite + " https://" + testSite)
	whole, _ := st.rewrite(in, "http://hub:12320", true)
	if want := "<a href=\"http://hub:12320/a\">https://" + testSite + ".evil/</a> //hub:12320 http://hub:12320"; string(whole) != want {
		t.Fatalf("whole %s", whole)
	}
	for size := 1; size < len(in)+2; size++ {
		var out, held []byte
		for i := 0; i < len(in); i += size {
			end := min(i+size, len(in))
			var o []byte
			o, held = st.rewrite(append(held, in[i:end]...), "http://hub:12320", false)
			out = append(out, o...)
		}
		o, _ := st.rewrite(held, "http://hub:12320", true)
		out = append(out, o...)
		if !bytes.Equal(out, whole) {
			t.Fatalf("size %d: %s", size, out)
		}
	}
	// Other ports of the site, and a site on another port.
	if got := st.rewriteString("https://"+testSite+":5177/x", "http://hub:1"); got != "https://"+testSite+":5177/x" {
		t.Fatalf("other port: %s", got)
	}
	other := newSite("http", testSite, 8080)
	if got := other.rewriteString("http://"+testSite+":8080/x http://"+testSite+"/y", "https://hub:1"); got != "https://hub:1/x http://"+testSite+"/y" {
		t.Fatalf("own port: %s", got)
	}
	v6 := newSite("http", "fd7a:115c:a1e0::1", 80)
	if got := v6.rewriteString("http://[fd7a:115c:a1e0::1]/x", "http://hub:1"); got != "http://hub:1/x" || v6.authority != "[fd7a:115c:a1e0::1]" {
		t.Fatalf("v6: %s %s", got, v6.authority)
	}
}

// A name that points elsewhere later must not take the proxy with it.
func TestSiteNameChangesLater(t *testing.T) {
	var hits int
	var mu sync.Mutex
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Connection", "close")
		io.WriteString(w, "site")
	}))
	defer up.Close()
	s := newSiteEnv(t, up.Listener.Addr().String())
	p := s.open(t, "http://"+testSite+"/")
	if _, b := s.get(s.base(p) + "/"); b != "site" {
		t.Fatalf("first %q", b)
	}
	for _, now := range []string{"127.0.0.1", "169.254.169.254", "10.1.2.3", "::1"} {
		s.mu.Lock()
		s.names[testSite] = addrs(now)
		s.dialed = nil
		s.mu.Unlock()
		s.m.byID[p.ID].tr.CloseIdleConnections()
		resp, body := s.get(s.base(p) + "/")
		s.mu.Lock()
		dialed := append([]netip.Addr(nil), s.dialed...)
		s.mu.Unlock()
		if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "Cannot reach") || len(dialed) != 0 {
			t.Fatalf("%s: %d %q dialed %v", now, resp.StatusCode, body, dialed)
		}
		if got, _ := s.m.Get(p.ID); got.State != StateError {
			t.Fatalf("%s: state %s", now, got.State)
		}
	}
	// Back on the tailnet, the preview recovers.
	s.mu.Lock()
	s.names[testSite] = addrs("100.64.10.20")
	s.mu.Unlock()
	if _, b := s.get(s.base(p) + "/"); b != "site" {
		t.Fatalf("back %q", b)
	}
	if got, _ := s.m.Get(p.ID); got.State != StateLive {
		t.Fatalf("state %s", got.State)
	}
}

func TestSiteCertificate(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "tls host=%s", r.Host)
	}))
	defer up.Close()
	s := newSiteEnv(t, up.Listener.Addr().String())
	// A name needs a certificate that holds.
	p := s.open(t, "https://"+testSite+"/")
	resp, body := s.get(s.base(p) + "/")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "certificate is not trusted") {
		t.Fatalf("name: %d %q", resp.StatusCode, body)
	}
	// A bare Tailscale IP has none to check.
	q := s.open(t, "https://100.64.10.20:8443/")
	if _, body := s.get(s.base(q) + "/"); body != "tls host=100.64.10.20:8443" {
		t.Fatalf("ip: %q", body)
	}
}

// Behind the hub's HTTPS proxy a preview keeps what the site says about its cookies, and its own
// URLs become https.
func TestSiteBehindHTTPSProxy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "app-session=abc; Path=/; Domain=.example.net; Secure; HttpOnly; SameSite=None")
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<a href="http://`+testSite+`/a">a</a><script>new WebSocket("ws://`+testSite+`/ws")</script>`)
	}))
	defer up.Close()
	s := newSiteEnv(t, up.Listener.Addr().String())
	p, lp, _, err := s.m.Create(CreateRequest{URL: "http://" + testSite + "/"})
	if err != nil {
		t.Fatal(err)
	}
	s.client.Jar = nil
	resp, _ := s.get(s.base(p)+lp, "X-Forwarded-Proto", "https")
	var launch string
	for _, c := range resp.Cookies() {
		if c.Name == CookieName(p.ListenPort, "https") {
			launch = c.Name + "=" + c.Value
		}
	}
	if launch == "" {
		t.Fatalf("launch cookies %v", resp.Header)
	}
	resp, body := s.get(s.base(p)+"/", "X-Forwarded-Proto", "https", "Cookie", launch)
	mine := "127.0.0.1:" + strconv.Itoa(p.ListenPort)
	if !strings.Contains(body, `href="https://`+mine+`/a"`) || !strings.Contains(body, `"wss://`+mine+`/ws"`) {
		t.Errorf("body %s", body)
	}
	got := strings.Join(resp.Header.Values("Set-Cookie"), " | ")
	if !strings.Contains(got, "_app-session=abc; Path=/; Secure; HttpOnly; SameSite=None") || strings.Contains(got, "Domain") {
		t.Errorf("cookie %s", got)
	}
}

func TestParseListening(t *testing.T) {
	ss := `LISTEN 0 4096 127.0.0.1:12412 0.0.0.0:* users:(("nextboard-test",pid=4192063,fd=7))
LISTEN 0 511 0.0.0.0:443 0.0.0.0:*
LISTEN 0 4096 *:12312 *:* users:(("nextboard-dev",pid=4173670,fd=7))
LISTEN 0 511 [::1]:5173 [::]:* users:(("node",pid=99,fd=20))
LISTEN 0 511 [::]:443 [::]:*
LISTEN 0 80 127.0.0.1:3306 0.0.0.0:* users:(("mysqld",pid=7,fd=1),("mysqld",pid=8,fd=1))
LISTEN 0 80 0.0.0.0:3000 0.0.0.0:*
LISTEN 0 80 [::]:3000 [::]:* users:(("node",pid=5,fd=3))
garbage line`
	got := ParseListening(ss)
	want := []Listening{{Port: 443, Address: "0.0.0.0"}, {Port: 3000, Address: "::", Process: "node", PID: 5}, {Port: 3306, Address: "127.0.0.1", Process: "mysqld", PID: 7}, {Port: 5173, Address: "::1", Process: "node", PID: 99}, {Port: 12312, Address: "*", Process: "nextboard-dev", PID: 4173670}, {Port: 12412, Address: "127.0.0.1", Process: "nextboard-test", PID: 4192063}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ss\n%+v\nwant\n%+v", got, want)
	}
	named := ParseListening("LISTEN 0 511 127.0.0.1:5173 0.0.0.0:* users:((\"MainThread\",pid=99,fd=20))\n" + psMark + "\n   99 /usr/bin/node --max-old-space-size=4096 /app/node_modules/.bin/vite --port 5173\n  100 /usr/sbin/nginx -g daemon on;\n")
	if len(named) != 1 || named[0].Process != "node vite" {
		t.Fatalf("named %+v", named)
	}
	lsof := `COMMAND  PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
node    1234 dev  20u IPv4 0x1234 0t0 TCP *:8080 (LISTEN)
Python  2222 dev  3u  IPv6 0x5678 0t0 TCP [::1]:9000 (LISTEN)`
	got = ParseListening(lsof)
	if len(got) != 2 || got[0].Port != 8080 || got[0].Process != "node" || got[0].PID != 1234 || got[1].Address != "::1" {
		t.Fatalf("lsof %+v", got)
	}
}
