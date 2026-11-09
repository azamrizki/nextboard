package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPasskeyAddressRules(t *testing.T) {
	for _, tc := range []struct {
		origin, rp, want string
		ok               bool
	}{
		{"https://github.com", "github.com", "github.com", true},
		{"https://github.com", "", "github.com", true},
		{"https://www.github.com", "github.com", "github.com", true},
		{"https://a.b.example.co.id", "example.co.id", "example.co.id", true},
		{"http://localhost:3000", "localhost", "localhost", true},
		{"http://127.0.0.1:3000", "", "127.0.0.1", true},
		{"https://evil.com", "github.com", "", false},
		{"https://github.com.evil.com", "github.com", "", false},
		{"https://notgithub.com", "github.com", "", false},
		{"http://github.com", "github.com", "", false},
		{"http://192.168.1.5", "", "", false},
		{"https://a.co.id", "co.id", "", false},
		{"https://foo.github.io", "github.io", "", false},
		{"https://foo.com", "com", "", false},
		{"https://1.2.3.4", "2.3.4", "", false},
		{"ftp://github.com", "github.com", "", false},
		{"https://github.com/path", "github.com", "", false},
		{"", "", "", false},
	} {
		got, ok := checkPasskeyAddress(tc.origin, tc.rp)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%q %q = %q %v", tc.origin, tc.rp, got, ok)
		}
	}
}

// pagePost plays the extension: it goes to the loopback listener with the token it was given.
func pagePost(t *testing.T, h *testHub, token, kind, origin, options string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"kind": kind, "origin": origin, "options": json.RawMessage(options)})
	req, err := http.NewRequest("POST", h.srv.passkeyHub()+"/nb/passkey", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

const createOptions = `{"rp":{"id":"example.com","name":"Example"},"user":{"id":"dQ","name":"alice"},"challenge":"Y2g","pubKeyCredParams":[{"alg":-7,"type":"public-key"}]}`

func TestPasskeyRelay(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	h.srv.passkeyWaitOverride = 10 * time.Second
	// off: no token works
	if st, _ := pagePost(t, h, "", "create", "https://example.com", createOptions); st != 401 {
		t.Fatalf("without a token: %d", st)
	}
	st, res := h.do("PUT", "/api/vault/passkeys", map[string]any{"enabled": true}, nil)
	if st != 200 {
		t.Fatalf("enable: %d %v", st, res)
	}
	dir, _ := res["path"].(string)
	conf, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ Hub, Token string }
	if err := json.Unmarshal(conf, &c); err != nil || c.Token == "" || !strings.HasPrefix(c.Hub, "http://127.0.0.1:") {
		t.Fatalf("config: %s %v", conf, err)
	}
	for _, f := range []string{"manifest.json", "main.js", "bridge.js", "background.js"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("extension lacks %s", f)
		}
	}
	if st, _ := pagePost(t, h, "wrong", "create", "https://example.com", createOptions); st != 401 {
		t.Fatalf("wrong token: %d", st)
	}

	// what the browser must refuse goes back to the browser, and nobody is asked
	for name, tc := range map[string][]string{
		"other site":  {"create", "https://evil.example.net", createOptions},
		"plain http":  {"create", "http://example.com", createOptions},
		"other algos": {"create", "https://example.com", `{"rp":{"id":"example.com"},"pubKeyCredParams":[{"alg":-257}]}`},
		"bad kind":    {"sign", "https://example.com", createOptions},
	} {
		st, out := pagePost(t, h, c.Token, tc[0], tc[1], tc[2])
		if st != 200 || out["fallback"] != true {
			t.Fatalf("%s: %d %v", name, st, out)
		}
		_, list := h.do("GET", "/api/vault/passkeys", nil, nil)
		if len(list["requests"].([]any)) != 0 {
			t.Fatalf("%s reached the owner: %v", name, list)
		}
	}

	answer := func(want map[string]any) map[string]any {
		t.Helper()
		done := make(chan map[string]any, 1)
		go func() {
			_, out := pagePost(t, h, c.Token, "create", "https://example.com", createOptions)
			done <- out
		}()
		var id string
		waitTrue(t, "the request", 5*time.Second, func() bool {
			_, list := h.do("GET", "/api/vault/passkeys", nil, nil)
			items := list["requests"].([]any)
			if len(items) == 1 {
				m := items[0].(map[string]any)
				if m["origin"] != "https://example.com" || m["rpId"] != "example.com" || m["kind"] != "create" {
					t.Errorf("request: %v", m)
				}
				id = m["id"].(string)
			}
			return id != ""
		})
		if st, res := h.do("POST", "/api/vault/passkeys/requests/"+id, want, nil); st != 200 {
			t.Fatalf("answer: %d %v", st, res)
		}
		return <-done
	}
	if out := answer(map[string]any{"result": map[string]any{"id": "abc"}}); out["result"].(map[string]any)["id"] != "abc" {
		t.Fatalf("result: %v", out)
	}
	if out := answer(map[string]any{"refuse": true}); out["error"].(map[string]any)["name"] != "NotAllowedError" {
		t.Fatalf("refuse: %v", out)
	}
	if out := answer(map[string]any{"fallback": true}); out["fallback"] != true {
		t.Fatalf("fallback: %v", out)
	}
	if st, res := h.do("POST", "/api/vault/passkeys/requests/pk_gone", map[string]any{"refuse": true}, nil); st != 404 || res["code"] != "request_gone" {
		t.Fatalf("gone: %d %v", st, res)
	}

	// nobody at the vault: the page gets its browser's own, and the request is gone
	h.srv.passkeyWaitOverride = 200 * time.Millisecond
	if st, out := pagePost(t, h, c.Token, "create", "https://example.com", createOptions); st != 200 || out["fallback"] != true {
		t.Fatalf("timeout: %d %v", st, out)
	}
	_, list := h.do("GET", "/api/vault/passkeys", nil, nil)
	if len(list["requests"].([]any)) != 0 {
		t.Fatalf("stayed: %v", list)
	}

	// the extension says it runs
	req, _ := http.NewRequest("GET", h.srv.passkeyHub()+"/nb/passkey/hello", nil)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != 200 {
		t.Fatalf("hello: %v %v", r, err)
	}
	if _, list := h.do("GET", "/api/vault/passkeys", nil, nil); list["active"] != true {
		t.Fatalf("active: %v", list)
	}

	// Off: the old token is dead, whoever still holds a copy of the extension.
	h.srv.passkeyWaitOverride = 10 * time.Second
	if st, _ := h.do("PUT", "/api/vault/passkeys", map[string]any{"enabled": false}, nil); st != 200 {
		t.Fatal("off")
	}
	if st, _ := pagePost(t, h, c.Token, "create", "https://example.com", createOptions); st != 401 {
		t.Fatalf("after off: %d", st)
	}
	if _, list := h.do("GET", "/api/vault/passkeys", nil, nil); list["enabled"] != false || list["active"] != false {
		t.Fatalf("list: %v", list)
	}
	// Nothing the page sent was kept.
	saved, _ := os.ReadFile(h.srv.st.Path("gui", "passkey.json"))
	if strings.Contains(string(saved), "alice") {
		t.Fatalf("kept: %s", saved)
	}
}

func TestPasskeyRoutesNeedLogin(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	for _, c := range []struct{ m, p string }{{"GET", "/api/vault/passkeys"}, {"PUT", "/api/vault/passkeys"}, {"POST", "/api/vault/passkeys/requests/x"}} {
		req, _ := http.NewRequest(c.m, h.ts.URL+c.p, strings.NewReader("{}"))
		req.Header.Set("X-NextBoard", "1")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Fatal(fmt.Sprintf("%s %s = %d", c.m, c.p, res.StatusCode))
		}
	}
}

// A person takes a while, longer than the loopback listener's read limit of 10 seconds. The page
// must still be there when they answer.
func TestPasskeyWaitsForAPerson(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	h.srv.passkeyWaitOverride = 30 * time.Second
	_, res := h.do("PUT", "/api/vault/passkeys", map[string]any{"enabled": true}, nil)
	conf, err := os.ReadFile(filepath.Join(res["path"].(string), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ Token string }
	if err := json.Unmarshal(conf, &c); err != nil {
		t.Fatal(err)
	}
	done := make(chan map[string]any, 1)
	go func() {
		_, out := pagePost(t, h, c.Token, "create", "https://example.com", createOptions)
		done <- out
	}()
	var id string
	waitTrue(t, "the request", 5*time.Second, func() bool {
		_, list := h.do("GET", "/api/vault/passkeys", nil, nil)
		if items := list["requests"].([]any); len(items) == 1 {
			id = items[0].(map[string]any)["id"].(string)
		}
		return id != ""
	})
	time.Sleep(11 * time.Second)
	if st, res := h.do("POST", "/api/vault/passkeys/requests/"+id, map[string]any{"result": map[string]any{"id": "late"}}, nil); st != 200 {
		t.Fatalf("answer after a while: %d %v", st, res)
	}
	if out := <-done; out["result"].(map[string]any)["id"] != "late" {
		t.Fatalf("page got %v", out)
	}
}

// A page cannot bury the owner in dialogs.
func TestPasskeyPageCannotPileUpRequests(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	h.srv.passkeyWaitOverride = 10 * time.Second
	_, res := h.do("PUT", "/api/vault/passkeys", map[string]any{"enabled": true}, nil)
	conf, _ := os.ReadFile(filepath.Join(res["path"].(string), "config.json"))
	var c struct{ Token string }
	_ = json.Unmarshal(conf, &c)
	first := make(chan map[string]any, 1)
	go func() {
		_, out := pagePost(t, h, c.Token, "create", "https://example.com", createOptions)
		first <- out
	}()
	var id string
	waitTrue(t, "the request", 5*time.Second, func() bool {
		_, list := h.do("GET", "/api/vault/passkeys", nil, nil)
		if items := list["requests"].([]any); len(items) == 1 {
			id = items[0].(map[string]any)["id"].(string)
		}
		return id != ""
	})
	if _, out := pagePost(t, h, c.Token, "create", "https://example.com", createOptions); out["fallback"] != true {
		t.Fatalf("second from the same site: %v", out)
	}
	_, list := h.do("GET", "/api/vault/passkeys", nil, nil)
	if n := len(list["requests"].([]any)); n != 1 {
		t.Fatalf("%d waiting", n)
	}
	h.do("POST", "/api/vault/passkeys/requests/"+id, map[string]any{"refuse": true}, nil)
	<-first
}
