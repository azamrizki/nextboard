package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/webui"
)

type testHub struct {
	t   *testing.T
	srv *Server
	ts  *httptest.Server
	c   *http.Client
}

// hubDataDir is a hub's data folder for a test. t.TempDir fails the test when something writes
// while it is being removed, and a hub has background writers that can finish just after it
// stops, which is harmless and shows only on a busy machine. A folder still being written after
// 5 seconds is a failure all the same.
func hubDataDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nb-hub-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var err error
		for range 50 {
			if err = os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Errorf("data folder still being written after the hub stopped: %v", err)
	})
	return dir
}

func newTestHub(t *testing.T) *testHub {
	t.Helper()
	srv, err := New(Options{DataDir: hubDataDir(t), Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	// claude may be installed on the test machine: no test reaches the
	// planner (the owner's plan) unless it turns it on explicitly.
	if _, err := srv.settings.Patch([]byte(`{"commands":{"planner":"off"}}`)); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	srv.sessions.Start() // tickers: busy, screen rules, state timers
	srv.startOrchestration()
	t.Cleanup(func() {
		srv.stopOrchestration()
		srv.jobs.Close()
		srv.sessions.Shutdown()
		// before t.TempDir goes: a snapshot still running would write the folder back
		srv.review.Close()
		srv.boards.Close()
		ts.Close()
	})
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: ts.Client().Transport, Jar: jar}
	return &testHub{t: t, srv: srv, ts: ts, c: c}
}

func (h *testHub) do(method, path string, body any, hdr map[string]string) (int, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.ts.URL+path, rd)
	if method != http.MethodGet {
		req.Header.Set("X-NextBoard", "1")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := h.c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{"_raw": string(raw)}
	}
	return resp.StatusCode, out
}

func (h *testHub) setup() {
	h.t.Helper()
	code, _, err := h.srv.auth.NewSetupCode()
	if err != nil {
		h.t.Fatal(err)
	}
	if st, _ := h.do("POST", "/api/setup", map[string]any{"code": code, "password": "rahasia123", "hubName": "devbox"}, nil); st != 200 {
		h.t.Fatalf("setup status %d", st)
	}
}

func (h *testHub) dialWS(path string, hdr http.Header) (*websocket.Conn, *http.Response, error) {
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	u := "ws" + strings.TrimPrefix(h.ts.URL, "http") + path
	if hdr == nil {
		hdr = http.Header{}
	}
	return d.Dial(u, hdr)
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func TestSetupLoginAndCSRF(t *testing.T) {
	h := newTestHub(t)
	if st, body := h.do("GET", "/api/setup/status", nil, nil); st != 200 || body["needsSetup"] != true {
		t.Fatalf("status %d %v", st, body)
	}
	code, _, _ := h.srv.auth.NewSetupCode()
	// CSRF header required.
	if st, _ := h.do("POST", "/api/setup", map[string]any{"code": code, "password": "rahasia123", "hubName": "devbox"}, map[string]string{"X-NextBoard": ""}); st != 403 {
		t.Fatalf("no csrf header: %d", st)
	}
	if st, _ := h.do("POST", "/api/setup", map[string]any{"code": "AAAA-AAAA", "password": "rahasia123", "hubName": "devbox"}, nil); st != 403 {
		t.Fatalf("bad code: %d", st)
	}
	if st, _ := h.do("POST", "/api/setup", map[string]any{"code": code, "password": "short", "hubName": "devbox"}, nil); st != 400 {
		t.Fatalf("weak pw: %d", st)
	}
	if st, _ := h.do("POST", "/api/setup", map[string]any{"code": strings.ToLower(strings.ReplaceAll(code, "-", "")), "password": "rahasia123", "hubName": "devbox"}, nil); st != 200 {
		t.Fatalf("setup: %d", st)
	}
	// Setup link cannot be reused.
	if st, _ := h.do("POST", "/api/setup", map[string]any{"code": code, "password": "rahasia123", "hubName": "x"}, nil); st != 409 {
		t.Fatalf("reuse: %d", st)
	}
	if st, body := h.do("GET", "/api/auth/me", nil, nil); st != 200 || body["kind"] != "admin" {
		t.Fatalf("me %d %v", st, body)
	}
	if _, body := h.do("GET", "/api/health", nil, nil); body["name"] != "devbox" {
		t.Fatalf("health %v", body)
	}
	h.do("POST", "/api/auth/logout", nil, nil)
	if st, _ := h.do("GET", "/api/sessions", nil, nil); st != 401 {
		t.Fatalf("after logout: %d", st)
	}
	if st, _ := h.do("POST", "/api/auth/login", map[string]any{"password": "rahasia123"}, nil); st != 200 {
		t.Fatalf("login: %d", st)
	}
	if st, _ := h.do("GET", "/api/sessions", nil, nil); st != 200 {
		t.Fatalf("sessions after login: %d", st)
	}
	// 5 failures per IP → 429.
	for i := 0; i < 5; i++ {
		if st, _ := h.do("POST", "/api/auth/login", map[string]any{"password": "salah"}, nil); st != 401 {
			t.Fatalf("wrong pw #%d: %d", i, st)
		}
	}
	if st, _ := h.do("POST", "/api/auth/login", map[string]any{"password": "rahasia123"}, nil); st != 429 {
		t.Fatalf("rate limit: %d", st)
	}
	// SPA fallback.
	// Only with a bundle: `go test` works before `make web`.
	if _, err := fs.Stat(webui.FS(), "index.html"); err == nil {
		if st, body := h.do("GET", "/devices", nil, nil); st != 200 || !strings.Contains(body["_raw"].(string), "<html") {
			t.Fatalf("spa %d", st)
		}
	}
}

func TestLocalTerminalOverWS(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, sess := h.do("POST", "/api/sessions", map[string]any{"machineId": "local", "preset": "shell", "cols": 100, "rows": 30}, nil)
	if st != 201 {
		t.Fatalf("create %d %v", st, sess)
	}
	id := sess["id"].(string)
	if sess["machineName"] != "devbox" || sess["color"] == "" {
		t.Fatalf("enrich: %v", sess)
	}
	// Cross-origin WS with cookie is refused.
	hdr := http.Header{"Origin": {"https://evil.example"}}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	if _, resp, err := h.dialWS("/api/sessions/"+id+"/ws", hdr); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("cross-origin ws should be 403, got %v", err)
	}
	hdr.Set("Origin", h.ts.URL)
	ws, _, err := h.dialWS("/api/sessions/"+id+"/ws", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	typ, _, err := ws.ReadMessage()
	if err != nil || typ != websocket.BinaryMessage {
		t.Fatalf("first frame should be binary snapshot: %v %v", typ, err)
	}
	_ = ws.WriteJSON(map[string]any{"t": "r", "c": 90, "r": 25})
	_ = ws.WriteJSON(map[string]any{"t": "i", "d": "echo nb-$((40+2))-ok; stty size\r"})
	var out bytes.Buffer
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !(strings.Contains(out.String(), "nb-42-ok") && strings.Contains(out.String(), "25 90")) {
		_ = ws.SetReadDeadline(deadline)
		typ, b, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if typ == websocket.BinaryMessage {
			out.Write(b)
		}
	}
	if !strings.Contains(out.String(), "nb-42-ok") || !strings.Contains(out.String(), "25 90") {
		t.Fatalf("terminal output missing; got %q", out.String())
	}
	// Exit keeps the node with status exited.
	_ = ws.WriteJSON(map[string]any{"t": "i", "d": "exit 3\r"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, in := h.do("GET", "/api/sessions/"+id, nil, nil); in["status"] == "exited" {
			if in["exitCode"].(float64) != 3 {
				t.Fatalf("exit code %v", in["exitCode"])
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, in := h.do("GET", "/api/sessions/"+id, nil, nil); in["status"] != "exited" {
		t.Fatalf("status %v", in["status"])
	}
	// Restart brings it back; snapshot still has the old output.
	if st, _ := h.do("POST", "/api/sessions/"+id+"/restart", map[string]any{"resume": false}, nil); st != 200 {
		t.Fatalf("restart %d", st)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, in := h.do("GET", "/api/sessions/"+id, nil, nil); in["status"] == "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	snap, _ := h.srv.sessions.Get(id)
	b, sub := snap.Subscribe()
	snap.Unsubscribe(sub)
	if !bytes.Contains(b, []byte("proses selesai (kode 3)")) {
		t.Fatalf("scrollback missing exit marker")
	}
	if st, _ := h.do("DELETE", "/api/sessions/"+id, nil, nil); st != 200 {
		t.Fatalf("delete %d", st)
	}
	if st, _ := h.do("GET", "/api/sessions/"+id, nil, nil); st != 404 {
		t.Fatalf("after delete %d", st)
	}
}

func TestUnknownCommandKeepsNode(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	if err := h.srv.st.Save("presets.json", []map[string]any{{"id": "nope", "name": "Nope", "command": "definitely-not-a-real-cli-xyz"}}); err != nil {
		t.Fatal(err)
	}
	h.srv.sessions.ResetPresetCache()
	st, sess := h.do("POST", "/api/sessions", map[string]any{"preset": "nope"}, nil)
	if st != 201 {
		t.Fatalf("create %d %v", st, sess)
	}
	id := sess["id"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, in := h.do("GET", "/api/sessions/"+id, nil, nil); in["status"] == "error" {
			s, _ := h.srv.sessions.Get(id)
			b, sub := s.Subscribe()
			s.Unsubscribe(sub)
			if !bytes.Contains(b, []byte("perintah tidak ditemukan")) {
				t.Fatalf("no red message: %q", b)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("status never became error")
}

func TestPairingDeviceAuthAndRevoke(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, ps := h.do("POST", "/api/pair/start", nil, nil)
	if st != 200 {
		t.Fatalf("pair start %d", st)
	}
	code := ps["code"].(string)
	if !strings.HasPrefix(ps["uri"].(string), "nextboard://pair?h=devbox&u=") {
		t.Fatalf("uri %v", ps)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	anon := &testHub{t: t, srv: h.srv, ts: h.ts, c: &http.Client{Transport: h.ts.Client().Transport}}
	st, pc := anon.do("POST", "/api/pair/complete", map[string]any{
		"code": code, "deviceName": "Pixel 8", "platform": "android", "publicKey": base64.StdEncoding.EncodeToString(pub),
	}, nil)
	if st != 200 {
		t.Fatalf("pair complete %d %v", st, pc)
	}
	devID := pc["deviceId"].(string)
	// Code is single-use.
	if st, _ := anon.do("POST", "/api/pair/complete", map[string]any{
		"code": code, "deviceName": "x", "platform": "android", "publicKey": base64.StdEncoding.EncodeToString(pub),
	}, nil); st != 403 {
		t.Fatalf("pair code reuse %d", st)
	}
	_, ch := anon.do("POST", "/api/auth/challenge", map[string]any{"deviceId": devID}, nil)
	nonce := ch["nonce"].(string)
	// Wrong signature is rejected.
	badSig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("other")))
	if st, _ := anon.do("POST", "/api/auth/device", map[string]any{"deviceId": devID, "nonce": nonce, "sig": badSig}, nil); st != 401 {
		t.Fatalf("bad sig %d", st)
	}
	_, ch = anon.do("POST", "/api/auth/challenge", map[string]any{"deviceId": devID}, nil)
	nonce = ch["nonce"].(string)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(nonce)))
	st, tok := anon.do("POST", "/api/auth/device", map[string]any{"deviceId": devID, "nonce": nonce, "sig": sig}, nil)
	if st != 200 {
		t.Fatalf("device auth %d %v", st, tok)
	}
	access := tok["accessToken"].(string)
	bearer := map[string]string{"Authorization": "Bearer " + access}
	if st, me := anon.do("GET", "/api/auth/me", nil, bearer); st != 200 || me["kind"] != "device" {
		t.Fatalf("bearer me %d %v", st, me)
	}
	// Devices cannot manage devices.
	if st, _ := anon.do("POST", "/api/pair/start", nil, bearer); st != 403 {
		t.Fatalf("device pair start %d", st)
	}
	// A token in the URL would land in the reverse proxy's access log.
	if _, resp, err := h.dialWS("/api/events?access_token="+access, nil); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("token in URL must be refused: %v", err)
	}
	ws, _, err := h.dialWS("/api/events", http.Header{"Authorization": {"Bearer " + access}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_, snap, err := ws.ReadMessage()
	if err != nil || !strings.Contains(string(snap), `"type":"snapshot"`) {
		t.Fatalf("snapshot %v %s", err, snap)
	}
	if st, _ := h.do("DELETE", "/api/devices/"+devID, nil, nil); st != 200 {
		t.Fatalf("revoke %d", st)
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := ws.ReadMessage()
		if err == nil {
			continue
		}
		if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != 4401 {
			t.Fatalf("expected close 4401, got %v", err)
		}
		break
	}
	if st, _ := anon.do("GET", "/api/sessions", nil, bearer); st != 401 {
		t.Fatalf("revoked token still works: %d", st)
	}
	if st, _ := anon.do("POST", "/api/auth/challenge", map[string]any{"deviceId": devID}, nil); st != 404 {
		t.Fatalf("revoked challenge %d", st)
	}
}

func TestHubRestartResumesSessions(t *testing.T) {
	dir := hubDataDir(t)
	srv1, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv1.review.Close) // a snapshot still running would write the folder back
	if err := srv1.st.Save("presets.json", []map[string]any{{
		"id": "echoer", "name": "Echoer", "command": "sh",
		"args": []string{"-c", "echo RUN:$0; exec sleep 60"}, "resumeArgs": []string{"resumed"},
	}}); err != nil {
		t.Fatal(err)
	}
	srv1.sessions.ResetPresetCache()
	in, err := srv1.sessions.Create(sessionCreate("echoer"))
	if err != nil {
		t.Fatal(err)
	}
	waitScrollback(t, srv1, in.ID, "RUN:sh")
	srv1.sessions.Shutdown()
	srv1.boards.Close()

	srv2, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv2.review.Close) // a snapshot still running would write the folder back
	defer srv2.sessions.Shutdown()
	defer srv2.boards.Close()
	srv2.sessions.Start()
	b := waitScrollback(t, srv2, in.ID, "RUN:resumed")
	if !bytes.Contains(b, []byte("RUN:sh")) || !bytes.Contains(b, []byte("hub dimulai ulang")) {
		t.Fatalf("old scrollback not restored: %q", b)
	}
}

func sessionCreate(preset string) session.CreateRequest {
	return session.CreateRequest{MachineID: "local", Preset: preset}
}

func waitScrollback(t *testing.T, srv *Server, id, want string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := srv.sessions.Get(id); ok {
			b, sub := s.Subscribe()
			s.Unsubscribe(sub)
			if bytes.Contains(b, []byte(want)) {
				return b
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %q", want)
	return nil
}
