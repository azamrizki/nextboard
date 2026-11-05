package server

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestPublicStatusOverHTTP(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "SECRET-PROJECT"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid := p["id"].(string)
	st, m := h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "INTERNAL-NAME", "type": "http", "url": "http://127.0.0.1:9/CANARY-PATH?t=CANARY-TOKEN",
		"headers": []any{map[string]any{"name": "Authorization", "value": "Bearer CANARY-HEADER"}}, "public": true, "publicName": "Website"}, nil)
	if st != 201 || m["publicName"] != "Website" {
		t.Fatalf("monitor %d %v", st, m)
	}
	if st, b := h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "x", "type": "http", "url": "http://a.example/", "public": true}, nil); st != 400 {
		t.Fatalf("public without a public name: %d %v", st, b)
	}
	st, c := h.do("PUT", "/api/projects/"+pid+"/uptime/public", map[string]any{"enabled": true, "title": "Our status"}, nil)
	if st != 200 || c["token"] == "" {
		t.Fatalf("enable %d %v", st, c)
	}
	token := c["token"].(string)
	_, list := h.do("GET", "/api/projects/"+pid+"/uptime", nil, nil)
	if list["settings"].(map[string]any)["publicPage"].(map[string]any)["token"] != token {
		t.Fatalf("settings %v", list["settings"])
	}

	// A stranger: no cookie, no special header.
	get := func(path string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", h.ts.URL+path, nil)
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, page := get("/status/" + token)
	if resp.StatusCode != 200 || !strings.Contains(page, "Our status") || !strings.Contains(page, "Website") {
		t.Fatalf("%d %s", resp.StatusCode, page)
	}
	whole := page + strings.Join(resp.Header.Values("Set-Cookie"), "")
	for _, secret := range []string{"SECRET-PROJECT", "INTERNAL-NAME", "CANARY", "127.0.0.1", "nextboard", "NextBoard", "devbox", "<script", "/api/"} {
		if strings.Contains(whole, secret) {
			t.Errorf("%q is out in the open:\n%s", secret, whole)
		}
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Error("a cookie for a stranger")
	}
	for k, want := range map[string]string{"Cache-Control": "no-store", "X-Robots-Tag": "noindex", "Content-Security-Policy": "default-src 'none'", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
		if !strings.Contains(resp.Header.Get(k), want) {
			t.Errorf("%s: %q", k, resp.Header.Get(k))
		}
	}

	// Everything else looks the same, and nothing of the app answers under this path.
	notFound, nf := get("/status/nope")
	for _, path := range []string{"/status/", "/status/" + token + "x", "/status/" + token + "/", "/status/" + token + "/../x"} {
		r, b := get(path)
		if r.StatusCode == 200 || (r.StatusCode == 404 && b != nf) {
			t.Errorf("%s: %d %s", path, r.StatusCode, b)
		}
	}
	if notFound.StatusCode != 404 || strings.Contains(nf, "<html") {
		t.Fatalf("not found: %d %s", notFound.StatusCode, nf)
	}
	req, _ := http.NewRequest("POST", h.ts.URL+"/status/"+token, strings.NewReader("x"))
	if r, err := http.DefaultTransport.RoundTrip(req); err != nil || r.StatusCode != 404 {
		t.Fatalf("POST: %v %v", r, err)
	}

	// The settings need a login, also to read the address.
	req, _ = http.NewRequest("GET", h.ts.URL+"/api/projects/"+pid+"/uptime", nil)
	if r, err := http.DefaultTransport.RoundTrip(req); err != nil || r.StatusCode != 401 {
		t.Fatalf("settings without a login: %v %v", r, err)
	}
	req, _ = http.NewRequest("POST", h.ts.URL+"/api/projects/"+pid+"/uptime/public/rotate", nil)
	req.Header.Set("X-NextBoard", "1")
	if r, err := http.DefaultTransport.RoundTrip(req); err != nil || r.StatusCode != 401 {
		t.Fatalf("rotate without a login: %v %v", r, err)
	}

	// Rotate and turn off: both at once.
	st, c = h.do("POST", "/api/projects/"+pid+"/uptime/public/rotate", map[string]any{}, nil)
	if st != 200 || c["token"] == token {
		t.Fatalf("rotate %d %v", st, c)
	}
	if r, _ := get("/status/" + token); r.StatusCode != 404 {
		t.Fatalf("the old address: %d", r.StatusCode)
	}
	fresh := c["token"].(string)
	if r, _ := get("/status/" + fresh); r.StatusCode != 200 {
		t.Fatalf("the new address: %d", r.StatusCode)
	}
	if st, _ := h.do("PUT", "/api/projects/"+pid+"/uptime/public", map[string]any{"enabled": false}, nil); st != 200 {
		t.Fatal(st)
	}
	if r, b := get("/status/" + fresh); r.StatusCode != 404 || b != nf {
		t.Fatalf("turned off: %d %s", r.StatusCode, b)
	}
}

// The second address can be put on the internet: it knows one route.
func TestStatusOnlyAddress(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "w"}, nil)
	if st != 201 {
		t.Fatal(st)
	}
	pid := p["id"].(string)
	if st, b := h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "x", "type": "push", "public": true, "publicName": "Job"}, nil); st != 201 {
		t.Fatalf("%d %v", st, b)
	}
	_, c := h.do("PUT", "/api/projects/"+pid+"/uptime/public", map[string]any{"enabled": true}, nil)
	token := c["token"].(string)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	h.srv.statusListen = addr
	stop := h.srv.serveStatusOnly()
	defer stop()

	get := func(method, path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://"+addr+path, nil)
		req.Header.Set("X-NextBoard", "1")
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if st, b := get("GET", "/status/"+token); st != 200 || !strings.Contains(b, "Job") {
		t.Fatalf("%d %s", st, b)
	}
	// Not the app, not the API, not the setup, not the push route, not the assets.
	for _, path := range []string{"/", "/index.html", "/login", "/setup", "/api/setup/status", "/api/projects", "/api/health", "/api/push/x", "/assets/x.js", "/nb/hook", "/api/events"} {
		st, b := get("GET", path)
		if st != 404 || strings.Contains(b, "<html") || strings.Contains(b, "needsSetup") {
			t.Errorf("%s: %d %s", path, st, b)
		}
	}
	if st, _ := get("POST", "/api/setup"); st != 404 {
		t.Errorf("POST: %d", st)
	}
}
