package gui

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the "app": inside a session it opens one real
// X11 window and waits, so no GUI program is needed on the test machine. The
// window title comes from helperEnv, which must survive strippedEnv.
const helperEnv = "GUI_TEST_WINDOW"

func TestMain(m *testing.M) {
	// Never run the tests from inside a session: each run starts more
	// sessions, which would multiply without end.
	if os.Getenv(markerEnv) != "" {
		if fakeBrowserForwards() {
			return
		}
		runTestWindow(os.Getenv(helperEnv))
		return
	}
	os.Exit(m.Run())
}

func runTestWindow(title string) {
	if title == "" {
		os.Exit(2)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(os.Getenv("DISPLAY"), ":"))
	if err != nil {
		os.Exit(3)
	}
	// Xwayland takes no cookie.
	var cookie []byte
	if p := os.Getenv("XAUTHORITY"); p != "" {
		auth, err := os.ReadFile(p)
		if err != nil || len(auth) < 16 {
			os.Exit(4)
		}
		cookie = auth[len(auth)-16:]
	}
	x, err := dialX(n, cookie)
	if err != nil {
		os.Exit(5)
	}
	win := x.ridBase | 1
	create := make([]byte, 32)
	create[0] = 1 // CreateWindow
	le.PutUint16(create[2:], 8)
	le.PutUint32(create[4:], win)
	le.PutUint32(create[8:], x.root)
	le.PutUint16(create[16:], 300)
	le.PutUint16(create[18:], 200)
	le.PutUint16(create[22:], 1) // InputOutput
	if _, err := x.c.Write(create); err != nil {
		os.Exit(6)
	}
	nameAtom, _ := x.atom("_NET_WM_NAME")
	utf8Atom, _ := x.atom("UTF8_STRING")
	prop := make([]byte, 24+len(title)+pad4(len(title)))
	prop[0] = 18 // ChangeProperty
	le.PutUint16(prop[2:], uint16(len(prop)/4))
	le.PutUint32(prop[4:], win)
	le.PutUint32(prop[8:], nameAtom)
	le.PutUint32(prop[12:], utf8Atom)
	prop[16] = 8
	le.PutUint32(prop[20:], uint32(len(title)))
	copy(prop[24:], title)
	if _, err := x.c.Write(prop); err != nil {
		os.Exit(7)
	}
	mapReq := make([]byte, 8)
	mapReq[0] = 8 // MapWindow
	le.PutUint16(mapReq[2:], 2)
	le.PutUint32(mapReq[4:], win)
	if _, err := x.c.Write(mapReq); err != nil {
		os.Exit(8)
	}
	if _, err := x.clients(); err != nil { // flushes: replies follow requests
		os.Exit(9)
	}
	// "close:<title>" closes the window by itself, as a person would.
	if strings.HasPrefix(title, "close:") {
		time.Sleep(1500 * time.Millisecond)
		os.Exit(0)
	}
	// "animate:<title>" keeps changing its picture, as a moving app does.
	if strings.HasPrefix(title, "animate:") {
		for color := uint32(0); ; color += 0x101010 {
			attrs := make([]byte, 16)
			attrs[0] = 2 // ChangeWindowAttributes: the background
			le.PutUint16(attrs[2:], 4)
			le.PutUint32(attrs[4:], win)
			le.PutUint32(attrs[8:], 2)
			le.PutUint32(attrs[12:], color&0xffffff)
			clear := make([]byte, 16)
			clear[0] = 61 // ClearArea: everything
			le.PutUint16(clear[2:], 4)
			le.PutUint32(clear[4:], win)
			if _, err := x.c.Write(append(attrs, clear...)); err != nil {
				os.Exit(0)
			}
			time.Sleep(16 * time.Millisecond)
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
}

type evLog struct {
	mu   sync.Mutex
	list []map[string]any
}

func (l *evLog) add(v any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list = append(l.list, v.(map[string]any))
}

func (l *evLog) removed(id string) map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.list {
		if e["type"] == "guiSessionRemoved" && e["id"] == id {
			return e
		}
	}
	return nil
}

// newTestManager returns a manager with fast timings and one desktop entry
// for each given title, or skips without the display packages.
func newTestManager(t *testing.T, apps map[string]string) (*Manager, *evLog) {
	t.Helper()
	dir := filepath.Join(shortTemp(t), "gui")
	ev := &evLog{}
	m := New(Config{Dir: dir, Publish: ev.add})
	m.forceX11 = true
	if !m.Status().Available {
		t.Skip("tigervnc-standalone-server / openbox belum terpasang")
	}
	m.pollEvery = 100 * time.Millisecond
	m.closeGrace = 300 * time.Millisecond
	m.windowTimeout = 15 * time.Second
	m.exitedWait = time.Second

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	for id, exec := range apps {
		if exec == "" {
			exec = self
		}
		writeDesktop(t, data, id+".desktop", "[Desktop Entry]\nType=Application\nName="+id+"\nExec="+exec+"\n")
	}
	t.Cleanup(func() {
		for _, s := range m.List() {
			_ = m.Stop(s.ID)
		}
		deadline := time.Now().Add(10 * time.Second)
		for len(m.List()) > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		m.Close()
	})
	return m, ev
}

// shortTemp keeps socket paths under the 108 byte limit: t.TempDir embeds
// the (long) test name.
func shortTemp(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "nbgui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func waitSession(t *testing.T, m *Manager, ev *evLog, what string, ok func(Session) bool) Session {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.List() {
			if ok(s) {
				return s
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	t.Fatalf("timeout: %s (sessions: %+v, events: %+v)", what, m.List(), ev.list)
	return Session{}
}

func waitGone(t *testing.T, m *Manager, ev *evLog, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if e := ev.removed(id); e != nil {
			return e
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s not removed (sessions: %+v)", id, m.List())
	return nil
}

func TestSessionLifecycle(t *testing.T) {
	t.Setenv(helperEnv, "Hello window")
	m, ev := newTestManager(t, map[string]string{"app": ""})

	s, err := m.Launch(LaunchRequest{AppID: "app", Width: 640, Height: 480})
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateStarting {
		t.Errorf("state = %q", s.State)
	}
	got := waitSession(t, m, ev, "first window", func(x Session) bool { return x.State == StateRunning && x.Title == "Hello window" })

	// The socket answers RFB and is private to the hub's user.
	c, err := m.Dial(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	banner := make([]byte, 12)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(banner); err != nil || !strings.HasPrefix(string(banner), "RFB ") {
		t.Errorf("banner %q, %v", banner, err)
	}
	c.Close()
	sess := m.sessions[got.ID]
	fi, err := os.Stat(sess.sock())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode: %v %v", fi, err)
	}

	// One session per app: a second launch reports the running one.
	_, err = m.Launch(LaunchRequest{AppID: "app"})
	ge, ok := err.(*Error)
	if !ok || ge.Code != "already_running" || ge.Session == nil || ge.Session.ID != got.ID {
		t.Fatalf("second launch: %v", err)
	}

	if err := m.Stop(got.ID); err != nil {
		t.Fatal(err)
	}
	e := waitGone(t, m, ev, got.ID)
	if e["reason"] != ReasonStopped {
		t.Errorf("reason = %v", e["reason"])
	}
	if left := markedProcs(got.ID, 0); len(left) != 0 {
		t.Errorf("processes left: %v", left)
	}
	if _, err := os.Stat(sess.dir); !os.IsNotExist(err) {
		t.Errorf("session folder left: %v", err)
	}
	// The profile outlives the session.
	if _, err := os.Stat(filepath.Join(m.cfg.Dir, "profiles", "app")); err != nil {
		t.Errorf("profile gone: %v", err)
	}
	if err := m.Stop(got.ID); err == nil {
		t.Error("stopping twice must report not found")
	}
}

func TestLastWindowClosedEndsSession(t *testing.T) {
	t.Setenv(helperEnv, "close:soon")
	m, ev := newTestManager(t, map[string]string{"app": ""})
	s, err := m.Launch(LaunchRequest{AppID: "app"})
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, m, ev, "first window", func(x Session) bool { return x.State == StateRunning })
	e := waitGone(t, m, ev, s.ID)
	if e["reason"] != ReasonClosed {
		t.Errorf("reason = %v", e["reason"])
	}
}

func TestNoWindowFails(t *testing.T) {
	m, ev := newTestManager(t, map[string]string{
		"crash":  `/bin/sh -c "echo boom >&2; exit 7"`,
		"silent": `/bin/sh -c "exit 0"`,
	})
	s, err := m.Launch(LaunchRequest{AppID: "crash"})
	if err != nil {
		t.Fatal(err)
	}
	e := waitGone(t, m, ev, s.ID)
	if e["reason"] != ReasonFailed || !strings.Contains(fmt.Sprint(e["message"]), "boom") {
		t.Errorf("crash: %v", e)
	}
	s, err = m.Launch(LaunchRequest{AppID: "silent"})
	if err != nil {
		t.Fatal(err)
	}
	e = waitGone(t, m, ev, s.ID)
	if e["reason"] != ReasonFailed || !strings.Contains(fmt.Sprint(e["message"]), "layar lain") {
		t.Errorf("silent: %v", e)
	}
}

func TestDisplayDeathEndsSession(t *testing.T) {
	t.Setenv(helperEnv, "w")
	m, ev := newTestManager(t, map[string]string{"app": ""})
	s, err := m.Launch(LaunchRequest{AppID: "app"})
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, m, ev, "first window", func(x Session) bool { return x.State == StateRunning })
	if err := syscall.Kill(m.sessions[s.ID].xpid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	e := waitGone(t, m, ev, s.ID)
	if e["reason"] != ReasonFailed {
		t.Errorf("reason = %v", e["reason"])
	}
}

func TestAdoptAfterRestart(t *testing.T) {
	t.Setenv(helperEnv, "Survivor")
	m, ev := newTestManager(t, map[string]string{"app": ""})
	s, err := m.Launch(LaunchRequest{AppID: "app"})
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, m, ev, "first window", func(x Session) bool { return x.State == StateRunning })
	m.Close() // what a hub shutdown does: the app keeps running

	if !processAlive(m.sessions[s.ID].xpid) {
		t.Fatal("display died with the hub")
	}
	ev2 := &evLog{}
	m2 := New(Config{Dir: m.cfg.Dir, Publish: ev2.add})
	m2.forceX11 = true
	m2.pollEvery = 100 * time.Millisecond
	m2.closeGrace = 300 * time.Millisecond
	m2.Start()
	defer m2.Close()
	got := waitSession(t, m2, ev2, "adoption", func(x Session) bool { return x.ID == s.ID && x.Title == "Survivor" })
	if got.State != StateRunning {
		t.Errorf("adopted state = %q", got.State)
	}
	c, err := m2.Dial(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := m2.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitGone(t, m2, ev2, s.ID)
	if left := markedProcs(s.ID, 0); len(left) != 0 {
		t.Errorf("processes left: %v", left)
	}
	_ = ev
	m.mu.Lock()
	delete(m.sessions, s.ID) // already stopped through m2
	m.mu.Unlock()
}

func TestAdoptClearsDeadSessions(t *testing.T) {
	dir := filepath.Join(shortTemp(t), "gui")
	stale := filepath.Join(dir, "sessions", "g_stale")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	// A pid that exists, but is not this session's display.
	st := fmt.Sprintf(`{"session":{"id":"g_stale","appId":"a"},"display":99,"xpid":%d}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(stale, "state.json"), []byte(st), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(Config{Dir: dir})
	m.Start()
	defer m.Close()
	if l := m.List(); len(l) != 0 {
		t.Errorf("stale session adopted: %+v", l)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale folder left: %v", err)
	}
}

func TestLaunchErrors(t *testing.T) {
	m, _ := newTestManager(t, map[string]string{"app": "/bin/true %F"})
	_, err := m.Launch(LaunchRequest{AppID: "nope"})
	if ge, ok := err.(*Error); !ok || ge.Code != "app_not_found" {
		t.Errorf("unknown app: %v", err)
	}
	// A folder reaches the command line only as an existing absolute path.
	writeDesktop(t, os.Getenv("XDG_DATA_HOME"), "folderapp.desktop", "[Desktop Entry]\nType=Application\nName=F\nExec=/bin/true %F\nMimeType=inode/directory;\n")
	t.Chdir(t.TempDir())
	if err := os.Mkdir("--flag", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"--flag", "/no/such/folder"} {
		_, err = m.Launch(LaunchRequest{AppID: "folderapp", Path: p})
		if ge, ok := err.(*Error); !ok || ge.Code != "folder_not_found" {
			t.Errorf("path %q: %v", p, err)
		}
	}
}

func TestStatusMissing(t *testing.T) {
	m := New(Config{Dir: t.TempDir(), Path: func() string { return t.TempDir() }})
	st := m.Status()
	if st.Available || len(st.Missing) != 2 || !strings.Contains(st.Hint, "apt install") {
		t.Errorf("status: %+v", st)
	}
	_, err := m.Launch(LaunchRequest{AppID: "x"})
	if ge, ok := err.(*Error); !ok || ge.Code != "gui_unavailable" {
		t.Errorf("launch: %v", err)
	}
}
