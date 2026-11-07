package preview

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
)

const publicSite = "public.example.com"

func TestIsPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":              true,
		"172.64.132.132":       true,
		"2606:4700::6810:84e5": true,
		"::ffff:8.8.8.8":       true,
		"127.0.0.1":            false,
		"::1":                  false,
		"0.0.0.0":              false,
		"10.0.0.1":             false,
		"172.16.0.1":           false,
		"192.168.1.1":          false,
		"169.254.169.254":      false, // a cloud's metadata service
		"fe80::1":              false,
		"fc00::1":              false,
		"100.64.0.1":           false, // CGNAT, and the tailnet has its own rule
		"100.64.10.20":         false,
		"192.0.2.1":            false,
		"198.18.0.1":           false,
		"224.0.0.1":            false,
		"240.0.0.1":            false,
		"255.255.255.255":      false,
		"::ffff:127.0.0.1":     false,
		"::ffff:10.0.0.1":      false,
		"64:ff9b::a00:1":       false, // NAT64 around 10.0.0.1
		"2002:a00:1::":         false, // 6to4 around 10.0.0.1
		"2001:db8::1":          false,
	} {
		if got := IsPublic(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
}

// publicEnv has one public site whose address leads to the test's upstream.
func publicEnv(t *testing.T, h http.Handler) *siteEnv {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	s := newSiteEnv(t, up.Listener.Addr().String())
	s.names[publicSite] = addrs("8.8.8.8")
	return s
}

// A Laravel page as the example of the feature: its URLs are absolute, and its cookies must
// come back under their own names, or the login answers 419.
func TestPublicSiteThroughProxy(t *testing.T) {
	var cookie, origin string
	s := publicEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, origin = r.Header.Get("Cookie"), r.Header.Get("Origin")
		if r.Host != publicSite {
			t.Errorf("host %q", r.Host)
		}
		http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "abc", Path: "/", Secure: true})
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Frame-Options", "DENY")
		io.WriteString(w, `<html><head><link href="http://`+publicSite+`/build/app.css"></head></html>`)
	}))
	p := s.open(t, "http://"+publicSite+"/auth/login")
	if p.TargetHost != publicSite || p.Path != "/auth/login" {
		t.Fatalf("preview %+v", p)
	}
	resp, body := s.get(s.base(p) + "/auth/login")
	if resp.StatusCode != 200 || !strings.Contains(body, "/__nb/bridge.js") {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if strings.Contains(body, publicSite) || !strings.Contains(body, "127.0.0.1:"+strconv.Itoa(p.ListenPort)+"/build/app.css") {
		t.Fatalf("own URL not rewritten: %q", body)
	}
	if resp.Header.Get("X-Frame-Options") != "" {
		t.Fatal("frame blocking stayed")
	}
	sc := resp.Header.Get("Set-Cookie")
	if !strings.HasPrefix(sc, siteCookiePrefix) || !strings.Contains(sc, "_XSRF-TOKEN=abc") || strings.Contains(strings.ToLower(sc), "secure") {
		t.Fatalf("cookie %q", sc)
	}
	// its own page posts: the site sees its own origin and its cookie by its real name
	req, _ := http.NewRequest("POST", s.base(p)+"/auth/login", strings.NewReader("x=1"))
	req.Header.Set("Origin", s.base(p))
	req.Header.Set("Cookie", "nb_session=secret; nb_pv_x=secret; nb_c_other_session=secret") // the jar adds its own
	r, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 || cookie != "XSRF-TOKEN=abc" || origin != "http://"+publicSite {
		t.Fatalf("%d cookie=%q origin=%q", r.StatusCode, cookie, origin)
	}
}

// Ports of one host are one site to a browser, so a page in another tile can call this listener
// with its cookies: a site takes no such call, and a dev server none from a site.
func TestTilesCannotCallEachOther(t *testing.T) {
	var hits int
	s := publicEnv(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	p := s.open(t, "http://"+publicSite+"/")
	dev, lp := s.create(s.upPort)
	s.launch(dev, lp)

	call := func(to, from string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", to+"/x", nil)
		if from != "" {
			req.Header.Set("Origin", from)
		}
		r, err := s.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	other := "http://127.0.0.1:12312" // say the hub's page, or any other tile
	if st := call(s.base(p), other); st != http.StatusForbidden {
		t.Fatalf("a site took a call from another port: %d", st)
	}
	if st := call(s.base(p), s.base(dev)); st != http.StatusForbidden {
		t.Fatalf("a site took a call from a dev server's tile: %d", st)
	}
	if st := call(s.base(dev), s.base(p)); st != http.StatusForbidden {
		t.Fatalf("a dev server took a call from a site: %d", st)
	}
	n := hits
	if st := call(s.base(p), s.base(p)); st != http.StatusOK || hits != n+1 {
		t.Fatalf("its own page: %d", st)
	}
	if st := call(s.base(p), "https://sso.example.org"); st != http.StatusOK {
		t.Fatalf("another host: %d", st) // SameSite decides there
	}
	if st := call(s.base(dev), other); st == http.StatusForbidden {
		t.Fatal("a dev server's own neighbours must keep working")
	}

	// a service worker would outlive the site on this port
	resp, _ := s.get(s.base(p)+"/sw.js", "Service-Worker", "script")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("service worker: %d", resp.StatusCode)
	}
}

func TestPublicSiteProbe(t *testing.T) {
	s := publicEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "bare.example.com":
			http.Redirect(w, r, "http://www.bare.example.com/start", http.StatusMovedPermanently)
		case r.Host == "www.bare.example.com":
			w.WriteHeader(200)
		case r.URL.Path == "/moved":
			http.Redirect(w, r, "/here", http.StatusFound)
		case r.URL.Path == "/sso":
			http.Redirect(w, r, "https://login.example.org/", http.StatusFound)
		case r.URL.Path == "/inside":
			http.Redirect(w, r, "http://10.0.0.5/", http.StatusFound)
		case r.URL.Path == "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case r.URL.Path == "/refused":
			// the site's own refusal, or a deploy: the page says so, through the hub
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/deploying":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("<h1>Be right back.</h1>"))
		case r.URL.Path == "/wall":
			w.Header().Set("Server", "cloudflare")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<title>Just a moment...</title><script src=\"/cdn-cgi/challenge-platform/h/b/orchestrate/chl_page/v1\"></script>"))
		case r.URL.Path == "/captcha":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<script src="https://ct.captcha-delivery.com/c.js"></script>`))
		case r.URL.Path == "/challenge":
			w.Header().Set("Cf-Mitigated", "challenge")
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/ua":
			if r.UserAgent() != "Test Browser" {
				t.Errorf("user agent %q", r.UserAgent())
			}
		}
	}))
	s.names["bare.example.com"] = addrs("8.8.8.8")
	s.names["www.bare.example.com"] = addrs("8.8.4.4")

	// where the page really is
	p, _, _, err := s.m.Create(CreateRequest{URL: "http://bare.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if p.TargetHost != "www.bare.example.com" || p.Path != "/start" {
		t.Fatalf("name with www: %+v", p)
	}
	if p, _, _, err = s.m.Create(CreateRequest{URL: "http://" + publicSite + "/moved"}); err != nil || p.Path != "/here" {
		t.Fatalf("same host: %+v %v", p, err)
	}
	if _, _, _, err = s.m.Create(CreateRequest{URL: "http://" + publicSite + "/ua", UserAgent: "Test Browser"}); err != nil {
		t.Fatal(err)
	}
	// what the proxy would only break
	for _, path := range []string{"/sso", "/inside", "/loop", "/wall", "/captcha", "/challenge"} {
		if _, _, _, err := s.m.Create(CreateRequest{URL: "http://" + publicSite + path}); codeOf(err) != "needs_browser" {
			t.Errorf("%s: %v", path, err)
		}
	}
	if _, _, _, err := s.m.Create(CreateRequest{URL: "http://" + publicSite + "/wall"}); err == nil || !strings.Contains(err.Error(), "bot check (403)") {
		t.Errorf("wall says why: %v", err)
	}
	// a refusal or a deploy of the site itself is shown through the hub
	for _, path := range []string{"/refused", "/deploying"} {
		if _, _, _, err := s.m.Create(CreateRequest{URL: "http://" + publicSite + path}); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
	// a redirect never reaches what a typed address could not
	s.names["www."+publicSite] = addrs("10.0.0.5")
	s.mu.Lock()
	s.dialed = nil
	s.mu.Unlock()
	up := publicEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://www."+publicSite+"/", http.StatusFound)
	}))
	up.names["www."+publicSite] = addrs("10.0.0.5")
	if _, _, _, err := up.m.Create(CreateRequest{URL: "http://" + publicSite + "/"}); codeOf(err) != "site_unreachable" {
		t.Fatalf("redirect to a local name: %v", err)
	}
	for _, a := range up.dialed {
		if !IsPublic(a) {
			t.Fatalf("dialed %s", a)
		}
	}

	// the tailnet is the user's own: no first look there
	if _, _, _, err := s.m.Create(CreateRequest{URL: "http://" + testSite + "/refused"}); err != nil {
		t.Fatalf("tailnet: %v", err)
	}
}

// A public address has no pass on its certificate, unlike a Tailscale IP.
func TestPublicCertificateIsChecked(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	s := newSiteEnv(t, up.Listener.Addr().String())
	s.names[publicSite] = addrs("8.8.8.8")
	for _, url := range []string{"https://" + publicSite + "/", "https://8.8.8.8/"} {
		if _, _, _, err := s.m.Create(CreateRequest{URL: url}); codeOf(err) != "needs_browser" {
			t.Errorf("%s: %v", url, err)
		}
	}
}

// A site comes back on the same port, so that what it keeps in the browser stays.
func TestSiteKeepsItsPort(t *testing.T) {
	s := publicEnv(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	a, _, _, err := s.m.Create(CreateRequest{URL: "http://" + publicSite + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.m.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	b, _, _, err := s.m.Create(CreateRequest{URL: "http://" + publicSite + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ListenPort != b.ListenPort {
		t.Fatalf("port %d, then %d", a.ListenPort, b.ListenPort)
	}
}
