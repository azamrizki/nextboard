package preview

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"nextboard/internal/machine"
)

// freeRange finds n consecutive free loopback ports for preview listeners
// (never the owner's 12320–12339).
func freeRange(t *testing.T, n int) (int, int) {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		base := 30000 + rand.IntN(25000)
		ok := true
		for p := base; p < base+n; p++ {
			l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
			if err != nil {
				ok = false
				break
			}
			l.Close()
		}
		if ok {
			return base, base + n - 1
		}
	}
	t.Fatal("no free port range")
	return 0, 0
}

type env struct {
	t       *testing.T
	m       *Manager
	up      *httptest.Server
	upPort  int
	client  *http.Client
	jar     http.CookieJar
	mu      sync.Mutex
	events  []map[string]any
	seenReq []*http.Request
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t}
	wsUp := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "http://"+r.Host }}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.seenReq = append(e.seenReq, r.Clone(context.Background()))
		e.mu.Unlock()
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Strict-Transport-Security", "max-age=63072000")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src https://cdn.example; frame-ancestors 'none'")
		h.Add("Set-Cookie", "app=1; Domain=localhost; Path=/; HttpOnly")
		h.Add("Set-Cookie", "nb_session=evil; Path=/")
		h.Add("Set-Cookie", "nb_session_http=evil; Path=/")
		h.Set("Clear-Site-Data", `"cookies"`)
		io.WriteString(w, "<!doctype html><html><HEAD lang=en><title>App</title></head><body>hi</body></html>")
	})
	mux.HandleFunc("/nohead", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<p>fragment</p>")
	})
	mux.HandleFunc("/gz", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			http.Error(w, "want gzip", 400)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		io.WriteString(zw, "<html><head><meta charset=utf-8></head><body>"+strings.Repeat("x", 100000)+"</body></html>")
		zw.Close()
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"host": r.Host, "origin": r.Header.Get("Origin"), "cookie": r.Header.Get("Cookie"),
			"referer": r.Header.Get("Referer"), "xff": r.Header.Get("X-Forwarded-For")})
	})
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:"+strconv.Itoa(e.upPort)+"/json?x=1", http.StatusFound)
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, n)
	})
	mux.HandleFunc("/hmr", func(w http.ResponseWriter, r *http.Request) {
		c, err := wsUp.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			c.WriteMessage(mt, append([]byte("echo:"), msg...))
		}
	})
	e.up = httptest.NewServer(mux)
	t.Cleanup(e.up.Close)
	e.upPort = e.up.Listener.Addr().(*net.TCPAddr).Port
	lo, hi := freeRange(t, 4)
	local := machine.NewLocal(machine.Config{})
	e.m = New(Config{
		BindHost: "127.0.0.1", PortMin: lo, PortMax: hi, LocalID: machine.LocalID,
		LocalReserved: func(p int) bool { return p == 12312 },
		Dial: func(ctx context.Context, mid string, port int) (net.Conn, error) {
			if mid != machine.LocalID {
				return nil, machine.ErrNotConnected
			}
			return machine.DialLoopback(ctx, local, port)
		},
		HubOrigins: func(scheme, host string) []string {
			h, _, _ := net.SplitHostPort(host)
			return []string{scheme + "://" + h + ":12312"}
		},
		Publish: func(v any) {
			e.mu.Lock()
			defer e.mu.Unlock()
			if m, ok := v.(map[string]any); ok {
				e.events = append(e.events, m)
			}
		},
	})
	t.Cleanup(e.m.Close)
	e.jar, _ = cookiejar.New(nil)
	e.client = &http.Client{Jar: e.jar, Timeout: 20 * time.Second,
		Transport:     &http.Transport{DisableCompression: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return e
}

func (e *env) base(p Preview) string { return "http://127.0.0.1:" + strconv.Itoa(p.ListenPort) }

func (e *env) get(u string, hdr ...string) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (e *env) create(port int) (Preview, string) {
	e.t.Helper()
	p, lp, _, err := e.m.Create(CreateRequest{MachineID: machine.LocalID, Port: port})
	if err != nil {
		e.t.Fatal(err)
	}
	return p, lp
}

func (e *env) launch(p Preview, lp string) {
	e.t.Helper()
	resp, _ := e.get(e.base(p) + lp)
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("launch %d", resp.StatusCode)
	}
}

func TestProxyAuthAndRewrite(t *testing.T) {
	e := newEnv(t)
	p, lp := e.create(e.upPort)
	if p.ListenPort < e.m.cfg.PortMin || p.State == "" || !strings.HasPrefix(lp, "/__nb/launch?t=") {
		t.Fatalf("create %+v %s", p, lp)
	}
	// No cookie → 401 page; the hub API is not served here.
	if resp, body := e.get(e.base(p) + "/"); resp.StatusCode != 401 || !strings.Contains(body, "Open this preview from NextBoard") {
		t.Fatalf("no cookie %d %s", resp.StatusCode, body)
	}
	resp, _ := e.get(e.base(p) + lp)
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/" {
		t.Fatalf("launch %d %v", resp.StatusCode, resp.Header)
	}
	sc := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"nb_pv_http_" + strconv.Itoa(p.ListenPort) + "=", "HttpOnly", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(sc, want) {
			t.Fatalf("cookie %q lacks %s", sc, want)
		}
	}
	if strings.Contains(sc, "Secure") {
		t.Fatalf("plain HTTP cookie must not be Secure: %q", sc)
	}
	// The token is single use.
	jar2, _ := cookiejar.New(nil)
	c2 := *e.client
	c2.Jar = jar2
	if r2, err := c2.Get(e.base(p) + lp); err != nil || r2.StatusCode != 401 {
		t.Fatalf("token reuse %v %v", r2.StatusCode, err)
	}
	// Proxied HTML: bridge after <head…>, frame blockers gone.
	u, _ := url.Parse(e.base(p))
	e.jar.SetCookies(u, []*http.Cookie{{Name: "nb_session", Value: "hub-secret"}, {Name: "nb_session_http", Value: "hub-secret"}, {Name: "nb_pv_1", Value: "other"}, {Name: "mine", Value: "keep"}})
	resp, body := e.get(e.base(p) + "/")
	if resp.StatusCode != 200 || !strings.Contains(body, `<HEAD lang=en>`+bridgeTag+`<title>`) {
		t.Fatalf("html %d %s", resp.StatusCode, body)
	}
	h := resp.Header
	if h.Get("X-Frame-Options") != "" || h.Get("Strict-Transport-Security") != "" || h.Get("Clear-Site-Data") != "" {
		t.Fatalf("headers %v", h)
	}
	csp := h.Values("Content-Security-Policy")
	if len(csp) != 2 || csp[0] != "default-src 'self'; script-src 'self' https://cdn.example" ||
		csp[1] != "frame-ancestors 'self' http://127.0.0.1:12312" {
		t.Fatalf("csp %q", csp)
	}
	cookies := h.Values("Set-Cookie")
	if len(cookies) != 1 || cookies[0] != "app=1; Path=/; HttpOnly" {
		t.Fatalf("set-cookie %q", cookies)
	}
	e.mu.Lock()
	last := e.seenReq[len(e.seenReq)-1]
	e.mu.Unlock()
	if last.Host != "localhost:"+strconv.Itoa(e.upPort) || strings.Contains(last.Header.Get("Cookie"), "nb_") ||
		last.Header.Get("Cookie") != "mine=keep" {
		t.Fatalf("upstream saw host %q cookie %q", last.Host, last.Header.Get("Cookie"))
	}
	// Host/Origin/Referer rewritten, no forwarding headers.
	_, js := e.get(e.base(p)+"/json", "Origin", e.base(p), "Referer", e.base(p)+"/page")
	var echo map[string]string
	json.Unmarshal([]byte(js), &echo)
	lh := "localhost:" + strconv.Itoa(e.upPort)
	if echo["host"] != lh || echo["origin"] != "http://"+lh || echo["referer"] != "http://"+lh+"/page" || echo["xff"] != "" {
		t.Fatalf("echo %v", echo)
	}
	// JSON is not touched.
	if strings.Contains(js, "bridge") {
		t.Fatal("bridge in json")
	}
	// Loopback redirects come back to the preview origin; others stay.
	if resp, _ := e.get(e.base(p) + "/redir"); resp.Header.Get("Location") != e.base(p)+"/json?x=1" {
		t.Fatalf("location %q", resp.Header.Get("Location"))
	}
	if resp, _ := e.get(e.base(p) + "/away"); resp.Header.Get("Location") != "https://example.com/elsewhere" {
		t.Fatalf("external location %q", resp.Header.Get("Location"))
	}
	// Gzip HTML is unpacked and injected; no head → at the start.
	resp, body = e.get(e.base(p)+"/gz", "Accept-Encoding", "gzip")
	if resp.Header.Get("Content-Encoding") != "" || !strings.Contains(body, "<head>"+bridgeTag+"<meta") || len(body) < 100000 {
		t.Fatalf("gz %v %d", resp.Header, len(body))
	}
	if _, body := e.get(e.base(p) + "/nohead"); body != bridgeTag+"<p>fragment</p>" {
		t.Fatalf("nohead %q", body)
	}
	// Bridge script carries the hub origins.
	resp, body = e.get(e.base(p) + "/__nb/bridge.js")
	if resp.StatusCode != 200 || !strings.Contains(body, `var HUB = ["http://127.0.0.1:12312"]`) || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/javascript") {
		t.Fatalf("bridge %d %s", resp.StatusCode, body[:min(len(body), 200)])
	}
	// Request body passes through.
	req, _ := http.NewRequest("POST", e.base(p)+"/upload", bytes.NewReader(make([]byte, 3<<20)))
	r3, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b3, _ := io.ReadAll(r3.Body)
	r3.Body.Close()
	if string(b3) != strconv.Itoa(3<<20) {
		t.Fatalf("upload %s", b3)
	}
	if st, _ := e.m.Get(p.ID); st.State != StateLive {
		t.Fatalf("state %+v", st)
	}
}

func TestProxyWebSocketAndWaiting(t *testing.T) {
	e := newEnv(t)
	p, lp := e.create(e.upPort)
	e.launch(p, lp)
	u, _ := url.Parse(e.base(p))
	hdr := http.Header{}
	for _, c := range e.jar.Cookies(u) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	hdr.Set("Origin", e.base(p))
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	c, resp, err := d.Dial("ws://127.0.0.1:"+strconv.Itoa(p.ListenPort)+"/hmr", hdr)
	if err != nil {
		t.Fatalf("ws dial %v %v", err, resp)
	}
	c.WriteMessage(websocket.TextMessage, []byte("update"))
	_, msg, err := c.ReadMessage()
	if err != nil || string(msg) != "echo:update" {
		t.Fatalf("ws %q %v", msg, err)
	}
	c.Close()
	// Without the cookie the upgrade is refused.
	if _, resp, err := d.Dial("ws://127.0.0.1:"+strconv.Itoa(p.ListenPort)+"/hmr", http.Header{"Origin": {e.base(p)}}); err == nil || resp.StatusCode != 401 {
		t.Fatalf("ws without cookie %v", err)
	}
	// A dev server that is not running yet → 502 waiting page.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l.Addr().(*net.TCPAddr).Port
	l.Close()
	p2, lp2 := e.create(dead)
	e.launch(p2, lp2)
	resp2, body := e.get(e.base(p2) + "/")
	if resp2.StatusCode != 502 || !strings.Contains(body, "Waiting for the dev server on :"+strconv.Itoa(dead)) {
		t.Fatalf("waiting %d %s", resp2.StatusCode, body)
	}
	if st, _ := e.m.Get(p2.ID); st.State != StateWaiting {
		t.Fatalf("state %+v", st)
	}
}

func TestCreateRulesLogsAndClose(t *testing.T) {
	e := newEnv(t)
	for _, port := range []int{0, 70000, 12312, e.m.cfg.PortMin} {
		if _, _, _, err := e.m.Create(CreateRequest{MachineID: machine.LocalID, Port: port}); err == nil || err.(*Error).Code != "bad_port" {
			t.Fatalf("port %d: %v", port, err)
		}
	}
	if _, _, _, err := e.m.Create(CreateRequest{MachineID: machine.LocalID, Port: e.upPort, Path: "//evil.example/"}); err == nil {
		t.Fatal("scheme-relative path accepted")
	}
	p, lp := e.create(e.upPort)
	// Same (machine, port) → same preview, new token.
	p2, lp2, created, err := e.m.Create(CreateRequest{MachineID: machine.LocalID, Port: e.upPort})
	if err != nil || created || p2.ID != p.ID || lp2 == lp {
		t.Fatalf("again %+v %v %v", p2, created, err)
	}
	// launch "to" never leaves the origin.
	resp, _ := e.get(e.base(p) + strings.Replace(lp2, "to=%2F", "to=%2F%2Fevil.example%2F", 1))
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/" {
		t.Fatalf("open redirect %v", resp.Header.Get("Location"))
	}
	// Bridge beacons fill the log ring and publish previewLog.
	batch := `{"entries":[{"type":"console","level":"error","args":["boom"]},{"type":"net","method":"get","url":"/api/x","status":500,"ms":12},{"type":"bogus"}]}`
	req, _ := http.NewRequest("POST", e.base(p)+"/__nb/log", strings.NewReader(batch))
	r, err := e.client.Do(req)
	if err != nil || r.StatusCode != 204 {
		t.Fatalf("log post %v %v", r.StatusCode, err)
	}
	items, seq, err := e.m.Logs(p.ID, 0)
	if err != nil || len(items) != 2 || seq != 2 || items[0].Args[0] != "boom" || items[1].Method != "GET" || items[1].Status != 500 {
		t.Fatalf("logs %+v %d %v", items, seq, err)
	}
	if items, _, _ := e.m.Logs(p.ID, 1); len(items) != 1 {
		t.Fatalf("since %+v", items)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		e.mu.Lock()
		found := false
		for _, ev := range e.events {
			if ev["type"] == "previewLog" && ev["previewId"] == p.ID {
				found = true
			}
		}
		e.mu.Unlock()
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no previewLog event")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Beacons are rate limited (≤ 10/s).
	limited := false
	for i := 0; i < 15; i++ {
		req, _ := http.NewRequest("POST", e.base(p)+"/__nb/log", strings.NewReader(`[]`))
		r, _ := e.client.Do(req)
		if r != nil && r.StatusCode == 429 {
			limited = true
		}
	}
	if !limited {
		t.Fatal("beacon not limited")
	}
	// Delete closes the listener.
	if err := e.m.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(p.ListenPort), time.Second); err == nil {
		c.Close()
		t.Fatal("listener still open")
	}
	if err := e.m.Delete(p.ID); err != ErrNotFound {
		t.Fatalf("delete twice %v", err)
	}
}

func TestIdleReap(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	var mu sync.Mutex
	e.m.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	ref := map[string]bool{}
	e.m.cfg.Referenced = func(id string) bool { return ref[id] }
	a, _ := e.create(e.upPort)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	b, _ := e.create(l.Addr().(*net.TCPAddr).Port)
	ref[b.ID] = true
	mu.Lock()
	now = now.Add(IdleClose + time.Minute)
	mu.Unlock()
	e.m.Reap()
	if _, ok := e.m.Get(a.ID); ok {
		t.Fatal("idle preview kept")
	}
	if _, ok := e.m.Get(b.ID); !ok {
		t.Fatal("referenced preview closed")
	}
}

func TestPortProbe(t *testing.T) {
	e := newEnv(t)
	local := machine.NewLocal(machine.Config{})
	raw, _ := net.Listen("tcp", "127.0.0.1:0") // TCP, not HTTP
	defer raw.Close()
	go func() {
		for {
			c, err := raw.Accept()
			if err != nil {
				return
			}
			go func() { time.Sleep(3 * time.Second); c.Close() }()
		}
	}()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	rawPort := raw.Addr().(*net.TCPAddr).Port
	ports, err := ParsePorts(fmt.Sprintf("%d,%d,%d,%d", closed, e.upPort, rawPort, e.upPort))
	if err != nil || len(ports) != 3 {
		t.Fatal(ports, err)
	}
	start := time.Now()
	res := ProbePorts(context.Background(), func(ctx context.Context, port int) (net.Conn, error) {
		return machine.DialLoopback(ctx, local, port)
	}, ports)
	got := map[int]PortProbe{}
	for _, r := range res {
		got[r.Port] = r
	}
	if got[closed].Open || !got[e.upPort].Open || !got[e.upPort].HTTP || !got[rawPort].Open || got[rawPort].HTTP {
		t.Fatalf("probe %+v", res)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("probe slow %s", time.Since(start))
	}
	if _, err := ParsePorts("1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21"); err == nil {
		t.Fatal("21 ports accepted")
	}
	if _, err := ParsePorts("0"); err == nil {
		t.Fatal("port 0 accepted")
	}
}

func TestDetector(t *testing.T) {
	var mu sync.Mutex
	var found []Found
	open := map[int]bool{5173: true}
	probes := 0
	d := &Detector{
		Session: func(sid string) (string, string, bool) { return "local", "run1", sid == "s_1" },
		Probe: func(ctx context.Context, mid string, port int) bool {
			mu.Lock()
			defer mu.Unlock()
			probes++
			return open[port]
		},
		Allowed: func(mid string, port int) bool { return port != 12312 },
		OnFound: func(f Found) bool { mu.Lock(); found = append(found, f); mu.Unlock(); return true },
		Tries:   3, Every: 10 * time.Millisecond,
	}
	// ANSI colours around the URL, split across chunks.
	d.Feed("s_1", []byte("  \x1b[32m➜\x1b[39m  \x1b[1mLocal\x1b[22m:   \x1b[36mhttp://localhost:\x1b[1m51"))
	d.Feed("s_1", []byte("73\x1b[22m/\x1b[39m\r\n"))
	// Printed again: no second tile.
	d.Feed("s_1", []byte("  ➜  Local:   http://localhost:5173/\r\n"))
	// 0.0.0.0 with a path; closed port → nothing.
	d.Feed("s_1", []byte("Listening on http://0.0.0.0:8080/app.\n"))
	// Hub port and unknown sessions are ignored.
	d.Feed("s_1", []byte("https://127.0.0.1:12312/\n"))
	d.Feed("s_2", []byte("http://localhost:5173/\n"))
	d.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(found) != 1 || found[0].Port != 5173 || found[0].URL != "http://localhost:5173/" || found[0].MachineID != "local" || found[0].Scheme != "http" {
		t.Fatalf("found %+v", found)
	}
	if probes != 1+3 {
		t.Fatalf("probes %d", probes)
	}
	// Disabled by settings.
	d.Enabled = func() bool { return false }
	d.Forget("s_1")
	mu.Unlock()
	d.Feed("s_1", []byte("http://localhost:5173/\n"))
	d.Wait()
	mu.Lock()
	if len(found) != 1 {
		t.Fatal("detector ran while disabled")
	}
}

// A backslash ends the path (a console echo of `printf ".../\n"`), and a
// URL whose preview could not be opened may be retried on its next print.
func TestDetectorBackslashAndRetry(t *testing.T) {
	var mu sync.Mutex
	var urls []string
	accept := false
	d := &Detector{
		Session: func(sid string) (string, string, bool) { return "local", "run1", true },
		Probe:   func(ctx context.Context, mid string, port int) bool { return true },
		OnFound: func(f Found) bool {
			mu.Lock()
			defer mu.Unlock()
			urls = append(urls, f.URL)
			return accept
		},
		Tries: 1, Every: time.Millisecond,
	}
	d.Feed("s_1", []byte(`printf "http://localhost:5173/\n"`+"\n"))
	d.Wait()
	mu.Lock()
	accept = true
	mu.Unlock()
	d.Feed("s_1", []byte("  ➜  Local:   http://localhost:5173/\r\n"))
	d.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(urls) != 2 || urls[0] != "http://localhost:5173/" || urls[1] != "http://localhost:5173/" {
		t.Fatalf("urls %q", urls)
	}
}

func TestSchemeOf(t *testing.T) {
	for _, c := range []struct {
		remote, proto, want string
	}{
		{"127.0.0.1:5000", "", "http"},
		{"127.0.0.1:5000", "https", "https"},
		{"[::1]:5000", "HTTPS", "https"},
		{"127.0.0.1:5000", "https, http", "https"},
		{"127.0.0.1:5000", "http", "http"},
		{"10.0.0.9:5000", "https", "http"}, // only a proxy on this host is believed
		{"garbage", "https", "http"},
	} {
		r := httptest.NewRequest("GET", "http://x/", nil)
		r.RemoteAddr = c.remote
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if got := SchemeOf(r); got != c.want {
			t.Errorf("SchemeOf(%s, %q) = %s, want %s", c.remote, c.proto, got, c.want)
		}
	}
}

// Behind the HTTPS reverse proxy a preview's cookie is Secure and its
// redirects and hub origins use https.
func TestPreviewBehindHTTPSProxy(t *testing.T) {
	e := newEnv(t)
	p, lp, _, err := e.m.Create(CreateRequest{MachineID: machine.LocalID, Port: e.upPort})
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := e.get(e.base(p)+lp, "X-Forwarded-Proto", "https")
	if sc := resp.Header.Get("Set-Cookie"); resp.StatusCode != 302 || !strings.Contains(sc, "Secure") ||
		!strings.HasPrefix(sc, "nb_pv_"+strconv.Itoa(p.ListenPort)+"=") {
		t.Fatalf("launch %d cookie %q", resp.StatusCode, sc)
	}
	// The jar will not send a Secure cookie over http; pass it by hand.
	cookie := resp.Cookies()[0]
	resp, _ = e.get(e.base(p)+"/redir", "X-Forwarded-Proto", "https", "Cookie", cookie.Name+"="+cookie.Value)
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "https://127.0.0.1:"+strconv.Itoa(p.ListenPort)+"/") {
		t.Fatalf("redirect %q", loc)
	}
	resp, body := e.get(e.base(p)+"/__nb/bridge.js", "X-Forwarded-Proto", "https", "Cookie", cookie.Name+"="+cookie.Value)
	if resp.StatusCode != 200 || !strings.Contains(body, `var HUB = ["https://127.0.0.1:12312"]`) {
		t.Fatalf("bridge %d %.120s", resp.StatusCode, body)
	}
}
