package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/gorilla/websocket"
)

// The acceptance criteria of docs/SPEC.md §12 that a test can hold on to. The
// table of which criterion is proved where is in docs/PROJECT.md §13.

var (
	routeRe   = regexp.MustCompile(`\b(pub|authed|admin)\("((?:GET|POST|PUT|PATCH|DELETE|HEAD) /[^"]+)"`)
	rawMuxRe  = regexp.MustCompile(`\bm\.Handle(?:Func)?\("([^"]+)"`)
	wildcards = regexp.MustCompile(`\{[^}]+\}`)
)

// declaredRoutes reads the route table from the source, so a new route is
// covered without anyone remembering to add it here.
func declaredRoutes(t *testing.T) (routes map[string][]string, raw []string) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	routes = map[string][]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range routeRe.FindAllSubmatch(b, -1) {
			routes[string(m[1])] = append(routes[string(m[1])], string(m[2]))
		}
		for _, m := range rawMuxRe.FindAllSubmatch(b, -1) {
			raw = append(raw, string(m[1]))
		}
	}
	if len(routes["authed"]) < 200 || len(routes["admin"]) < 5 {
		t.Fatalf("the route table was not found: %d authed, %d admin", len(routes["authed"]), len(routes["admin"]))
	}
	return routes, raw
}

// anonymous sends a request with no cookie and no token, past the CSRF header.
func anonymous(h *testHub, pattern string, hdr map[string]string) int {
	h.t.Helper()
	method, path, _ := strings.Cut(pattern, " ")
	req, err := http.NewRequest(method, h.ts.URL+wildcards.ReplaceAllString(path, "x"), nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("X-NextBoard", "1")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// SPEC F1.7: a request without auth gets 401, on every route there is.
func TestEveryRouteRefusesAnonymous(t *testing.T) {
	routes, _ := declaredRoutes(t)
	h := newTestHub(t)
	check := func(state string) {
		for _, group := range []string{"authed", "admin"} {
			for _, p := range routes[group] {
				if st := anonymous(h, p, nil); st != http.StatusUnauthorized {
					t.Errorf("%s: %s answered %d without auth", state, p, st)
				}
			}
		}
	}
	check("before setup")
	h.setup()
	check("after setup")
}

// A paired device can use the hub but not administer it.
func TestAdminRoutesRefuseADevice(t *testing.T) {
	routes, _ := declaredRoutes(t)
	h := newTestHub(t)
	h.setup()
	token, _ := pairTestDevice(t, h)
	for _, p := range routes["admin"] {
		if st := anonymous(h, p, map[string]string{"Authorization": "Bearer " + token}); st != http.StatusForbidden {
			t.Errorf("%s answered %d to a device", p, st)
		}
	}
}

// What works without auth is a short list. A new entry here is a decision: look
// at what it gives away before adding it.
func TestPublicRoutesAreOnlyThese(t *testing.T) {
	routes, raw := declaredRoutes(t)
	want := []string{
		"GET /api/health", "GET /api/push/{token}", "GET /api/setup/status",
		"POST /api/auth/challenge", "POST /api/auth/device", "POST /api/auth/login",
		"POST /api/pair/complete", "POST /api/setup",
	}
	got := slices.Sorted(slices.Values(routes["pub"]))
	if !slices.Equal(got, want) {
		t.Errorf("public routes changed:\n got %q\nwant %q", got, want)
	}
	if want := []string{"/api/"}; !slices.Equal(raw, want) {
		t.Errorf("handlers registered around the auth helpers: %q, want %q", raw, want)
	}
}

func TestAPINeedsItsHeaderForAnythingButReading(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, _ := h.do("POST", "/api/auth/logout", nil, map[string]string{"X-NextBoard": ""})
	if st != http.StatusForbidden {
		t.Errorf("a POST without X-NextBoard answered %d", st)
	}
	if st, _ := h.do("GET", "/api/auth/me", nil, nil); st != 200 {
		t.Errorf("that POST logged out: %d", st)
	}
}

// pairTestDevice pairs a device through the auth store and logs it in.
func pairTestDevice(t *testing.T, h *testHub) (token, id string) {
	t.Helper()
	code, _, err := h.srv.auth.NewPairCode()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d, err := h.srv.auth.CompletePairing(code, "Phone", "android", base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := h.srv.auth.Challenge(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err = h.srv.auth.DeviceLogin(d.ID, nonce, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(nonce))))
	if err != nil {
		t.Fatal(err)
	}
	return token, d.ID
}

// termConn is a terminal socket that gathers everything it reads.
type termConn struct {
	t   *testing.T
	ws  *websocket.Conn
	out bytes.Buffer
	// syncs makes each sync's marker its own.
	syncs int
}

func (h *testHub) cookieHeader() http.Header {
	u, err := url.Parse(h.ts.URL)
	if err != nil {
		h.t.Fatal(err)
	}
	hdr := http.Header{"Origin": {h.ts.URL}}
	for _, c := range h.c.Jar.Cookies(u) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	return hdr
}

func (h *testHub) openTerm(id string, hdr http.Header) *termConn {
	h.t.Helper()
	ws, _, err := h.dialWS("/api/sessions/"+id+"/ws", hdr)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = ws.Close() })
	return &termConn{t: h.t, ws: ws}
}

func (c *termConn) send(text string) {
	c.t.Helper()
	if err := c.ws.WriteJSON(map[string]any{"t": "i", "d": text}); err != nil {
		c.t.Fatal(err)
	}
}

// waitFor reads until the output holds want, and says so.
func (c *termConn) waitFor(want string, d time.Duration) bool {
	c.t.Helper()
	deadline := time.Now().Add(d)
	for !bytes.Contains(c.out.Bytes(), []byte(want)) {
		if time.Now().After(deadline) {
			return false
		}
		_ = c.ws.SetReadDeadline(deadline)
		typ, b, err := c.ws.ReadMessage()
		if err != nil {
			return false
		}
		if typ == websocket.BinaryMessage {
			c.out.Write(b)
		}
	}
	return true
}

// mustSee fails the test when the output never holds want.
func (c *termConn) mustSee(want string) {
	c.t.Helper()
	if !c.waitFor(want, 20*time.Second) {
		c.t.Fatalf("terminal never showed %q; got %q", want, c.out.String())
	}
}

// sync waits for the shell to answer and forgets what came before: what a program
// printed last can arrive after the program is done.
func (c *termConn) sync() {
	c.t.Helper()
	c.syncs++
	c.send(fmt.Sprintf("echo nb-sync-$((%d+1))\r", c.syncs*10))
	c.mustSee(fmt.Sprintf("nb-sync-%d", c.syncs*10+1))
	c.out.Reset()
}

// closed reports whether the server ended the socket within d, and how.
func (c *termConn) closed(d time.Duration) (bool, error) {
	deadline := time.Now().Add(d)
	_ = c.ws.SetReadDeadline(deadline)
	for {
		_, _, err := c.ws.ReadMessage()
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				return false, err
			}
			return true, err
		}
	}
}

func (h *testHub) newShell() string {
	h.t.Helper()
	st, sess := h.do("POST", "/api/sessions", map[string]any{"machineId": "local", "preset": "shell", "cols": 100, "rows": 30}, nil)
	if st != 201 {
		h.t.Fatalf("create %d %v", st, sess)
	}
	return sess["id"].(string)
}

func writeScript(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// SPEC F1.5: the browser goes away, the terminal goes on, and what it printed
// in the meantime is there when the browser comes back.
func TestTerminalGoesOnWhileTheBrowserIsGone(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	id := h.newShell()
	before := h.sessionPID(id)
	first := h.openTerm(id, h.cookieHeader())
	first.send("(sleep 1; echo nb-away-$((40+2))) & echo nb-start-$((1+1))\r")
	first.mustSee("nb-start-2")
	_ = first.ws.Close()

	time.Sleep(1500 * time.Millisecond)
	if _, in := h.do("GET", "/api/sessions/"+id, nil, nil); in["status"] != "running" {
		t.Fatalf("status after the browser left: %v", in["status"])
	}
	if after := h.sessionPID(id); after != before {
		t.Errorf("the shell was replaced: pid %v, then %v", before, after)
	}
	again := h.openTerm(id, h.cookieHeader())
	// Both come in the first frame: nothing new is printed after the reconnect.
	typ, snap, err := again.ws.ReadMessage()
	if err != nil || typ != websocket.BinaryMessage {
		t.Fatalf("snapshot: %v %v", typ, err)
	}
	for _, want := range []string{"nb-start-2", "nb-away-42"} {
		if !bytes.Contains(snap, []byte(want)) {
			t.Errorf("snapshot lacks %q: %q", want, snap)
		}
	}
}

func (h *testHub) sessionPID(id string) any {
	h.t.Helper()
	_, in := h.do("GET", "/api/sessions/"+id, nil, nil)
	return in["pid"]
}

// SPEC F1.4: vim, htop and Ctrl-C need a real terminal: raw mode, the alternate
// screen, signals from the keyboard and from a resize.
func TestTerminalIsARealOne(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	term := h.openTerm(h.newShell(), h.cookieHeader())

	t.Run("raw mode passes a byte", func(t *testing.T) {
		term.t = t
		script := writeScript(t, "raw.sh", `stty raw -echo
printf '\033[?1049hnb-raw-ready\r\n'
c=$(dd bs=1 count=1 2>/dev/null | od -An -tx1 | tr -d ' ')
stty sane
printf '\033[?1049lnb-got-%s\r\n' "$c"
`)
		term.send("sh " + script + "\r")
		term.mustSee("\x1b[?1049hnb-raw-ready")
		term.send("\x03") // in raw mode a byte like any other, no signal
		term.mustSee("\x1b[?1049lnb-got-03")
	})

	t.Run("Ctrl-C interrupts", func(t *testing.T) {
		term.t = t
		script := writeScript(t, "int.sh", `trap 'echo nb-sigint-seen; exit 0' INT
echo nb-int-ready
while :; do sleep 0.1; done
`)
		term.send("sh " + script + "\r")
		term.mustSee("nb-int-ready\r\n")
		term.send("\x03")
		term.mustSee("nb-sigint-seen")
	})

	t.Run("resizing signals the program", func(t *testing.T) {
		term.t = t
		script := writeScript(t, "winch.sh", `trap 'echo "nb-winch-$(stty size)"; exit 0' WINCH
echo nb-winch-ready
while :; do sleep 0.1; done
`)
		term.send("sh " + script + "\r")
		term.mustSee("nb-winch-ready\r\n")
		if err := term.ws.WriteJSON(map[string]any{"t": "r", "c": 91, "r": 26}); err != nil {
			t.Fatal(err)
		}
		term.mustSee("nb-winch-26 91")
	})

	// Their settings stay out of the real home folder.
	home := t.TempDir()

	t.Run("vi", func(t *testing.T) {
		term.t = t
		if _, err := exec.LookPath("vi"); err != nil {
			t.Skip("no vi")
		}
		file := filepath.Join(home, "from-vi.txt")
		term.send("HOME=" + home + " vi " + file + "\r")
		time.Sleep(time.Second)
		term.send("ihello-from-vi\x1b:wq\r")
		deadline := time.Now().Add(15 * time.Second)
		for {
			if b, err := os.ReadFile(file); err == nil && strings.Contains(string(b), "hello-from-vi") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("vi wrote nothing; screen %q", term.out.String())
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("htop", func(t *testing.T) {
		term.t = t
		if _, err := exec.LookPath("htop"); err != nil {
			t.Skip("no htop")
		}
		term.sync() // vi's last output must not be taken for htop's
		term.send("HOME=" + home + " TERM=xterm htop\r")
		term.mustSee("\x1b[?1049h")
		// A key pressed while it still starts can be lost, so press until it leaves.
		stop, stopped := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(stopped)
			for {
				select {
				case <-stop:
					return
				case <-time.After(500 * time.Millisecond):
					_ = term.ws.WriteJSON(map[string]any{"t": "i", "d": "q"})
				}
			}
		}()
		left := term.waitFor("\x1b[?1049l", 20*time.Second)
		close(stop)
		<-stopped
		if !left {
			t.Fatalf("htop did not quit; screen %q", term.out.String())
		}
		// Enter first: the last q may have landed on the prompt.
		term.send("\recho nb-after-htop-$((3+4))\r")
		term.mustSee("nb-after-htop-7")
	})
}

// SPEC F3.1 and F3.2: a paired device is listed, and revoking it ends what it has
// open at once, its terminal too.
func TestRevokeCutsADeviceOffAtOnce(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	token, id := pairTestDevice(t, h)

	st, list := h.do("GET", "/api/devices", nil, nil)
	if st != 200 || !strings.Contains(asJSON(t, list), id) || !strings.Contains(asJSON(t, list), "Phone") {
		t.Fatalf("devices %d %v", st, list)
	}
	bearer := http.Header{"Authorization": {"Bearer " + token}}
	term := h.openTerm(h.newShell(), bearer)
	term.send("echo nb-device-$((1+2))\r")
	term.mustSee("nb-device-3")

	if st, _ := h.do("DELETE", "/api/devices/"+id, nil, nil); st != 200 {
		t.Fatalf("revoke %d", st)
	}
	gone, err := term.closed(5 * time.Second)
	if !gone {
		t.Fatal("the revoked device's terminal stayed open")
	}
	if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != wsCloseRevok {
		t.Errorf("closed with %v, want %d", err, wsCloseRevok)
	}
	if _, resp, err := h.dialWS("/api/sessions/x/ws", bearer); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Errorf("the token still opens sockets: %v", err)
	}
	st, list = h.do("GET", "/api/devices", nil, nil)
	if st != 200 || strings.Contains(asJSON(t, list), id) {
		t.Errorf("devices after revoke %d %v", st, list)
	}
}

// An open socket must not outlive the login that opened it: a stolen session
// would keep its shell after the owner logged out or reset the password.
func TestLoginsEndingCloseOpenSockets(t *testing.T) {
	login := func(t *testing.T, h *testHub) *testHub {
		t.Helper()
		jar, _ := cookiejar.New(nil)
		other := &testHub{t: t, srv: h.srv, ts: h.ts, c: &http.Client{Transport: h.ts.Client().Transport, Jar: jar}}
		if st, b := other.do("POST", "/api/auth/login", map[string]any{"password": "rahasia123"}, nil); st != 200 {
			t.Fatalf("login %d %v", st, b)
		}
		return other
	}
	t.Run("logout ends that login only", func(t *testing.T) {
		h := newTestHub(t)
		h.setup()
		other := login(t, h)
		id := h.newShell()
		term := h.openTerm(id, h.cookieHeader())
		events, _, err := h.dialWS("/api/events", h.cookieHeader())
		if err != nil {
			t.Fatal(err)
		}
		defer events.Close()
		keeps := other.openTerm(id, other.cookieHeader())

		if st, _ := h.do("POST", "/api/auth/logout", nil, nil); st != 200 {
			t.Fatalf("logout %d", st)
		}
		gone, err := term.closed(5 * time.Second)
		if !gone {
			t.Fatalf("the terminal stayed open after logout: %v", err)
		}
		if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != wsCloseRevok {
			t.Errorf("closed with %v: the web client only goes to login on %d", err, wsCloseRevok)
		}
		if gone, err := (&termConn{t: t, ws: events}).closed(5 * time.Second); !gone {
			t.Errorf("the event socket stayed open after logout: %v", err)
		}
		keeps.send("echo nb-other-$((2+2))\r")
		keeps.mustSee("nb-other-4")
	})
	t.Run("password reset ends them all", func(t *testing.T) {
		h := newTestHub(t)
		h.setup()
		other := login(t, h)
		id := h.newShell()
		first, second := h.openTerm(id, h.cookieHeader()), other.openTerm(id, other.cookieHeader())
		// Over the file, the way `nextboard reset-password` does it from its own process.
		st, err := store.Open(h.srv.st.Dir)
		if err != nil {
			t.Fatal(err)
		}
		cli, err := auth.Open(st)
		if err != nil {
			t.Fatal(err)
		}
		if err := cli.SetPassword("a new password"); err != nil {
			t.Fatal(err)
		}
		for _, term := range []*termConn{first, second} {
			if gone, err := term.closed(2*loginCheckEvery + time.Second); !gone {
				t.Errorf("a terminal stayed open after a password reset: %v", err)
			}
		}
	})
	t.Run("the watcher stops with the last socket", func(t *testing.T) {
		h := newTestHub(t)
		h.setup()
		term := h.openTerm(h.newShell(), h.cookieHeader())
		_ = term.ws.Close()
		waitUntil(t, 3*loginCheckEvery, func() bool {
			h.srv.conns.mu.Lock()
			defer h.srv.conns.mu.Unlock()
			return !h.srv.conns.watching
		})
	})
}

func waitUntil(t *testing.T, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SPEC F2.5 and F2.6, as they stand since agents moved to This PC: a console on a
// server loses its connection, says so, and is back when the server is.
func TestServerConsoleComesBackAfterADrop(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	srv := newSSHServer(t)
	gate := newTCPGate(t, srv.port)
	mid := h.connectMachine(gate.port, srv.key)
	st, sess := h.do("POST", "/api/sessions", map[string]any{"machineId": mid, "preset": "shell", "cols": 100, "rows": 30, "cwd": t.TempDir()}, nil)
	if st != 201 {
		t.Fatalf("console %d %v", st, sess)
	}
	id := sess["id"].(string)
	term := h.openTerm(id, h.cookieHeader())
	term.send("echo nb-before-$((40+2))\r")
	term.mustSee("nb-before-42")

	statusOf := func() any {
		_, in := h.do("GET", "/api/sessions/"+id, nil, nil)
		return in["status"]
	}
	gate.down()
	waitTrue(t, "the console to notice", 30*time.Second, func() bool { return statusOf() != "running" })
	gate.up()
	waitTrue(t, "the console to return", 90*time.Second, func() bool { return statusOf() == "running" })

	again := h.openTerm(id, h.cookieHeader())
	again.mustSee("nb-before-42")
	again.send("echo nb-after-$((1+1))\r")
	again.mustSee("nb-after-2")
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
