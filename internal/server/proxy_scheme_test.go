package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The hub speaks plain HTTP. Directly the cookie is not Secure (a browser
// would drop it); behind the HTTPS reverse proxy it is, and pairing only
// hands out the public HTTPS origin, with no certificate pin.
func TestAdvertisedURLs(t *testing.T) {
	mixed := []string{"https://board.example.com", "http://127.0.0.1:12312"}
	if got := AdvertisedURLs(mixed); len(got) != 1 || got[0] != mixed[0] {
		t.Fatalf("mixed %v", got)
	}
	plain := []string{"http://127.0.0.1:12312"}
	if got := AdvertisedURLs(plain); len(got) != 1 || got[0] != plain[0] {
		t.Fatalf("plain %v", got)
	}
}

// A browser refuses to let an http:// page overwrite a Secure cookie of the
// same name, and cookies ignore ports. A host once used over HTTPS must still
// be able to log in over HTTP, so each scheme has its own cookie.
func TestSessionCookiePerScheme(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	do := func(method, path, body string, https bool, ck *http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, h.ts.URL+path, strings.NewReader(body))
		req.Header.Set("X-NextBoard", "1")
		if https {
			req.Header.Set("X-Forwarded-Proto", "https")
		}
		if ck != nil {
			req.AddCookie(ck)
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	login := func(https bool) *http.Cookie {
		t.Helper()
		cs := do("POST", "/api/auth/login", `{"password":"rahasia123"}`, https, nil).Cookies()
		if len(cs) != 1 {
			t.Fatalf("cookies %v", cs)
		}
		return cs[0]
	}
	plain, secure := login(false), login(true)
	if plain.Name != "nb_session_http" || plain.Secure || secure.Name != "nb_session" || !secure.Secure {
		t.Fatalf("plain %+v secure %+v", plain, secure)
	}
	for _, c := range []struct {
		ck    *http.Cookie
		https bool
		want  int
	}{{plain, false, 200}, {plain, true, 401}, {secure, true, 200}, {secure, false, 401}} {
		if r := do("GET", "/api/auth/me", "", c.https, c.ck); r.StatusCode != c.want {
			t.Fatalf("%s https=%v: %d, want %d", c.ck.Name, c.https, r.StatusCode, c.want)
		}
	}
	// Logout expires the cookie of the scheme in use.
	cs := do("POST", "/api/auth/logout", "", false, plain).Cookies()
	if len(cs) != 1 || cs[0].Name != "nb_session_http" || cs[0].MaxAge >= 0 {
		t.Fatalf("logout cookies %+v", cs)
	}
}

func TestHTTPDirectAndBehindReverseProxy(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	var session *http.Cookie // from the proxied login
	// send issues a request as nginx would forward it when proxied is set.
	send := func(method, path, body string, proxied bool) (*http.Response, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, h.ts.URL+path, strings.NewReader(body))
		if method != http.MethodGet {
			req.Header.Set("X-NextBoard", "1")
		}
		c := h.c
		if proxied {
			req.Host = "board.example.com"
			req.Header.Set("X-Forwarded-Proto", "https")
			// The jar skips a request whose Host differs from its URL and would
			// keep a Secure cookie off plain HTTP: forward the session by hand.
			if session != nil {
				req.AddCookie(session)
			}
			c = &http.Client{}
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}
	login := `{"password":"rahasia123"}`
	if r, _ := send("POST", "/api/auth/login", login, false); r.StatusCode != 200 || strings.Contains(r.Header.Get("Set-Cookie"), "Secure") {
		t.Fatalf("direct login %d cookie %q", r.StatusCode, r.Header.Get("Set-Cookie"))
	}
	r, _ := send("POST", "/api/auth/login", login, true)
	if !strings.Contains(r.Header.Get("Set-Cookie"), "Secure") ||
		!strings.Contains(r.Header.Get("Set-Cookie"), "SameSite=Strict") {
		t.Fatalf("proxied cookie %q", r.Header.Get("Set-Cookie"))
	}
	session = r.Cookies()[0]

	r, ps := send("POST", "/api/pair/start", "", true)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("pair start %d %v", r.StatusCode, ps)
	}
	uri, _ := ps["uri"].(string)
	urls, _ := ps["urls"].([]any)
	if len(urls) == 0 || urls[0] != "https://board.example.com" {
		t.Fatalf("urls %v", urls)
	}
	for _, u := range urls {
		if strings.HasPrefix(u.(string), "http://") {
			t.Fatalf("cleartext address handed out behind HTTPS: %v", urls)
		}
	}
	if strings.Contains(uri, "fp=") || !strings.Contains(uri, "u=https%3A%2F%2Fboard.example.com") {
		t.Fatalf("uri %q", uri)
	}
	if _, ok := ps["fingerprint"]; ok {
		t.Fatalf("fingerprint in pair response: %v", ps)
	}

	// The SPA frames previews on the same scheme as the page.
	r, _ = send("GET", "/", "", true)
	if csp := r.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-src 'self' https://board.example.com:") {
		t.Fatalf("csp %q", csp)
	}

	// Preview origins allowed to frame: the proxy origin on the default port.
	if o := h.srv.hubOrigins("https", "board.example.com:12320"); o[0] != "https://board.example.com" {
		t.Fatalf("hub origins %v", o)
	}
}
