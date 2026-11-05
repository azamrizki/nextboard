// Package gui runs desktop apps of This PC on private virtual displays, so a
// board tile can show and control them (docs/API-gui.md).
//
// Every session is its own display and app: a headless compositor where the GPU
// allows it (wayland.go), else Xtigervnc with openbox. The app never appears on
// the physical screen. The VNC server listens only on a Unix socket the hub
// bridges to the tile's WebSocket. Sessions run detached from the hub and are
// adopted again after a hub restart.
package gui

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Session states.
const (
	StateStarting = "starting" // no window yet
	StateRunning  = "running"
	StateStopping = "stopping"
)

// Reasons of a guiSessionRemoved event.
const (
	ReasonClosed  = "closed"  // the last window was closed in the app
	ReasonStopped = "stopped" // stopped through the API
	ReasonFailed  = "failed"  // no window ever appeared, or the display died
)

const (
	maxSessions = 8
	logLimit    = 16 << 20
	defaultW    = 1280
	defaultH    = 800

	// markerEnv tags every process of a session. Apps fork and detach (the
	// VS Code launcher exits at once), so neither the pid of the launcher
	// nor its process group finds them again.
	markerEnv = "NEXTBOARD_GUI_SESSION"

	installHint = "sudo apt install tigervnc-standalone-server openbox (atau sway wayvnc wf-recorder xwayland, dengan GPU)"
)

var errBadExec = errors.New("exec tidak valid")

// App is a launchable desktop entry.
type App struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Comment       string   `json:"comment,omitempty"`
	Categories    []string `json:"categories,omitempty"`
	AcceptsFolder bool     `json:"acceptsFolder"`
}

// Session is the public state of a running app.
type Session struct {
	ID        string    `json:"id"`
	AppID     string    `json:"appId"`
	Name      string    `json:"name"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Display   string    `json:"display"` // DisplayWayland or DisplayX11
	Audio     bool      `json:"audio"`   // has its own sink
	StartedAt time.Time `json:"startedAt"`
}

// Status says whether this hub can run apps at all.
type Status struct {
	Available bool     `json:"available"`
	Missing   []string `json:"missing"`
	Hint      string   `json:"hint,omitempty"`
	Display   string   `json:"display,omitempty"` // what new sessions get
	GPU       string   `json:"gpu,omitempty"`     // render node in use
	Video     bool     `json:"video"`             // a hardware encoder works
	Codecs    []string `json:"videoCodecs"`       // what it can encode, best first
	Audio     bool     `json:"audio"`             // sessions get their own sink
}

// Error carries the HTTP mapping of a failure.
type Error struct {
	Status  int
	Code    string
	Msg     string
	Session *Session // the running session of an "already_running" conflict
}

func (e *Error) Error() string { return e.Msg }

func newErr(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Config wires the manager to the hub.
type Config struct {
	Dir     string        // <data>/gui
	Publish func(any)     // event sink
	Path    func() string // PATH for apps; nil = the hub's own
}

// LaunchRequest starts an app.
type LaunchRequest struct {
	AppID  string `json:"appId"`
	Path   string `json:"path"` // folder to open, for apps that accept one
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Manager owns the sessions.
type Manager struct {
	cfg Config

	pollEvery     time.Duration
	closeGrace    time.Duration // no windows for this long = the app was closed
	windowTimeout time.Duration // no first window for this long = failed
	exitedWait    time.Duration // same, once the launcher itself has exited

	mu       sync.Mutex
	sessions map[string]*session
	encoders int
	closed   bool
	forceX11 bool // tests: the X11 display whatever is installed
	wg       sync.WaitGroup

	// Result of encoderArgs.
	probeOnce sync.Once
	encOK     bool
	encAV1    bool
	encArgs   []string
	encNode   string
}

type session struct {
	pub     Session // guarded by Manager.mu
	dir     string
	display int
	xpid    int
	cookie  []byte
	pageURL string // web page the app opens with (OpenURL)

	hadWindow bool // the app reached its first window (saved in state.json)
	stopping  bool // guarded by Manager.mu

	launcher atomic.Pointer[launcherExit]
	rfbPid   atomic.Int64 // wayvnc, on a Wayland display
	rfbBytes atomic.Int64
	quit     chan struct{} // stops the monitor
	quitOnce sync.Once
	gone     chan struct{} // closed once the teardown finished
}

type launcherExit struct {
	at  time.Time
	err error
}

func (s *session) stopMonitor() { s.quitOnce.Do(func() { close(s.quit) }) }
func (s *session) sock() string { return filepath.Join(s.dir, "rfb") }

// savedState is state.json, what adoption needs after a hub restart.
type savedState struct {
	Session   Session `json:"session"`
	Display   int     `json:"display"`
	XPid      int     `json:"xpid"`
	RFBPid    int     `json:"rfbPid,omitempty"`
	HadWindow bool    `json:"hadWindow"`
}

// New creates the manager; call Start to adopt surviving sessions.
func New(cfg Config) *Manager {
	return &Manager{
		cfg:           cfg,
		pollEvery:     1500 * time.Millisecond,
		closeGrace:    5 * time.Second,
		windowTimeout: 60 * time.Second,
		exitedWait:    20 * time.Second,
		sessions:      map[string]*session{},
	}
}

func (m *Manager) publish(v map[string]any) {
	if m.cfg.Publish != nil {
		m.cfg.Publish(v)
	}
}

func (m *Manager) publishSession(pub Session) {
	m.publish(map[string]any{"type": "guiSession", "session": pub})
}

func (m *Manager) pathEnv() string {
	if m.cfg.Path != nil {
		if p := m.cfg.Path(); p != "" {
			return p
		}
	}
	return os.Getenv("PATH")
}

func (m *Manager) xvncBinary() string {
	for _, b := range []string{"Xtigervnc", "Xvnc"} {
		if p, ok := lookIn(m.pathEnv(), b); ok {
			return p
		}
	}
	return ""
}

// Status reports the missing system packages.
func (m *Manager) Status() Status {
	st := Status{Missing: []string{}, Codecs: []string{}}
	if runtime.GOOS != "linux" {
		st.Hint = "Aplikasi GUI di board baru tersedia di Linux."
		return st
	}
	// Either kind of display will do.
	if !m.waylandAvailable() {
		if m.xvncBinary() == "" {
			st.Missing = append(st.Missing, "tigervnc-standalone-server")
		}
		if _, ok := lookIn(m.pathEnv(), "openbox"); !ok {
			st.Missing = append(st.Missing, "openbox")
		}
	}
	st.Available = len(st.Missing) == 0
	if !st.Available {
		st.Hint = installHint
		return st
	}
	st.Display = m.displayKind()
	st.GPU = renderNode()
	st.Video = m.videoAvailable()
	if st.Video {
		st.Codecs = m.videoCodecs()
	}
	st.Audio = m.audioAvailable()
	return st
}

// Apps lists the launchable desktop entries by name.
func (m *Manager) Apps() []App {
	path := m.pathEnv()
	out := []App{}
	for _, e := range scanEntries(dataDirs()) {
		if e.launchable(path) {
			out = append(out, App{ID: e.ID, Name: e.Name, Comment: e.Comment, Categories: e.Categories, AcceptsFolder: e.acceptsFolder()})
		}
	}
	slices.SortFunc(out, func(a, b App) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return out
}

func (m *Manager) lookup(id string) (*entry, bool) {
	e, ok := scanEntries(dataDirs())[id]
	if !ok || !e.launchable(m.pathEnv()) {
		return nil, false
	}
	return e, true
}

// IconPath returns the icon file of an app ("" when it has none we can serve).
func (m *Manager) IconPath(id string) string {
	e, ok := m.lookup(id)
	if !ok {
		return ""
	}
	return resolveIcon(e.Icon, dataDirs())
}

// List returns the live sessions, oldest first.
func (m *Manager) List() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.pub)
	}
	slices.SortFunc(out, func(a, b Session) int {
		return cmp.Or(a.StartedAt.Compare(b.StartedAt), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// Dial connects to a session's VNC server.
func (m *Manager) Dial(id string) (net.Conn, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	var sock string
	if ok && !s.stopping {
		sock = s.sock()
	}
	m.mu.Unlock()
	if sock == "" {
		return nil, notFound()
	}
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return nil, newErr(http.StatusBadGateway, "gui_unreachable", "display aplikasi tidak bisa dihubungi: %v", err)
	}
	return c, nil
}

func notFound() *Error {
	return newErr(http.StatusNotFound, "session_not_found", "sesi aplikasi tidak ditemukan")
}

func randID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "g_" + hex.EncodeToString(b), nil
}

// reserve claims the single session of an app. Each app has one profile, and
// profile-locked apps would open their window on the display that already
// holds the profile, so a second session could only ever stay empty.
func (m *Manager) reserve(e *entry, display string) (*session, error) {
	id, err := randID()
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, newErr(http.StatusServiceUnavailable, "gui_closed", "hub sedang berhenti")
		}
		var gone chan struct{}
		for _, s := range m.sessions {
			if s.pub.AppID != e.ID {
				continue
			}
			if !s.stopping {
				pub := s.pub
				m.mu.Unlock()
				return nil, &Error{Status: http.StatusConflict, Code: "already_running", Msg: e.Name + " sudah terbuka", Session: &pub}
			}
			gone = s.gone
		}
		if gone != nil {
			// The previous session still holds the profile: wait it out.
			m.mu.Unlock()
			select {
			case <-gone:
			case <-time.After(15 * time.Second):
			}
			if attempt > 0 {
				return nil, newErr(http.StatusConflict, "still_stopping", "%s masih berhenti, coba lagi sebentar", e.Name)
			}
			continue
		}
		if len(m.sessions) >= maxSessions {
			m.mu.Unlock()
			return nil, newErr(http.StatusConflict, "too_many_sessions", "maksimal %d aplikasi terbuka bersamaan", maxSessions)
		}
		s := &session{
			pub:  Session{ID: id, AppID: e.ID, Name: e.Name, Title: e.Name, State: StateStarting, Display: display, StartedAt: time.Now().UTC()},
			dir:  filepath.Join(m.sessionsRoot(), id),
			quit: make(chan struct{}),
			gone: make(chan struct{}),
		}
		m.sessions[id] = s
		m.mu.Unlock()
		return s, nil
	}
}

func (m *Manager) sessionsRoot() string { return filepath.Join(m.cfg.Dir, "sessions") }

// Launch starts an app. The display is up when it returns; the window
// arrives later, announced by a guiSession event.
func (m *Manager) Launch(req LaunchRequest) (Session, error) {
	return m.launch(req, "")
}

// checkAvailable says why this hub can't run apps, if so.
func (m *Manager) checkAvailable() error {
	st := m.Status()
	if st.Available {
		return nil
	}
	msg := st.Hint
	if len(st.Missing) > 0 {
		msg = "paket belum terpasang: " + strings.Join(st.Missing, ", ") + ". Pasang dengan: " + st.Hint
	}
	return newErr(http.StatusServiceUnavailable, "gui_unavailable", "%s", msg)
}

func (m *Manager) launch(req LaunchRequest, pageURL string) (Session, error) {
	if err := m.checkAvailable(); err != nil {
		return Session{}, err
	}
	e, ok := m.lookup(req.AppID)
	if !ok {
		return Session{}, newErr(http.StatusNotFound, "app_not_found", "aplikasi tidak ditemukan di This PC")
	}
	target := ""
	if req.Path != "" && e.acceptsFolder() {
		target = req.Path
		if rest, ok := strings.CutPrefix(target, "~"); ok && (rest == "" || rest[0] == '/') {
			home, _ := os.UserHomeDir()
			target = home + rest
		}
		// Only an absolute path: it lands on the app's command line.
		fi, err := os.Stat(target)
		if err != nil || !fi.IsDir() || !filepath.IsAbs(target) {
			return Session{}, newErr(http.StatusBadRequest, "folder_not_found", "folder tidak ditemukan: %s", req.Path)
		}
	}
	s, err := m.reserve(e, m.displayKind())
	if err != nil {
		return Session{}, err
	}
	s.pageURL = pageURL
	x, err := m.bootstrap(s, e, target, clamp(req.Width, 320, 3840, defaultW), clamp(req.Height, 200, 2160, defaultH))
	if err != nil {
		m.terminate(s)
		m.mu.Lock()
		delete(m.sessions, s.pub.ID)
		m.mu.Unlock()
		close(s.gone)
		return Session{}, newErr(http.StatusInternalServerError, "gui_start_failed", "gagal membuka %s: %v", e.Name, err)
	}
	m.mu.Lock()
	closed := m.closed
	pub := s.pub
	if !closed {
		m.wg.Add(1)
	}
	m.mu.Unlock()
	if closed {
		x.close()
		m.end(s, ReasonStopped, "")
		return Session{}, newErr(http.StatusServiceUnavailable, "gui_closed", "hub sedang berhenti")
	}
	go m.monitor(s, x)
	m.publishSession(pub)
	return pub, nil
}

func clamp(v, lo, hi, def int) int {
	if v <= 0 {
		return def
	}
	return max(lo, min(hi, v))
}

// displayWatch tells the monitor which windows a display has. An error means the
// display is gone.
type displayWatch interface {
	windows() (count int, title string, err error)
	close()
}

// bootstrap brings up display, window manager and app, in that order.
func (m *Manager) bootstrap(s *session, e *entry, target string, w, h int) (displayWatch, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	// sun_path is 108 bytes on Linux.
	if len(s.sock()) > 100 {
		return nil, fmt.Errorf("path data hub terlalu panjang untuk socket: %s", s.sock())
	}
	if s.pub.Display == DisplayWayland {
		return m.bootstrapWayland(s, e, target, w, h)
	}
	s.cookie = make([]byte, 16)
	if _, err := rand.Read(s.cookie); err != nil {
		return nil, err
	}
	xauth := filepath.Join(s.dir, "xauth")
	if err := os.WriteFile(xauth, xauthority(s.cookie), 0o600); err != nil {
		return nil, err
	}
	if err := m.startDisplay(s, e.Name, xauth, w, h); err != nil {
		return nil, err
	}
	if err := m.saveState(s); err != nil {
		return nil, err
	}
	x, err := dialX(s.display, s.cookie)
	if err != nil {
		return nil, fmt.Errorf("display tidak bisa dihubungi: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			x.close()
		}
	}()
	if err := m.startWM(s, xauth); err != nil {
		return nil, err
	}
	// Windows that open before the window manager stay small and unmanaged.
	deadline := time.Now().Add(5 * time.Second)
	for {
		up, err := x.hasWM()
		if err != nil {
			return nil, fmt.Errorf("display berhenti: %w", err)
		}
		if up {
			break
		}
		if time.Now().After(deadline) {
			return nil, errors.New("openbox tidak siap: " + lastLine(filepath.Join(s.dir, "wm.log")))
		}
		time.Sleep(50 * time.Millisecond)
	}
	m.startAudio(s)
	if err := m.startApp(s, e, target, xauth); err != nil {
		return nil, err
	}
	ok = true
	return x, nil
}

// xauthority is one FamilyWild cookie: valid for whichever display number the
// server picks, so the file can exist before the number is known.
func xauthority(cookie []byte) []byte {
	const name = "MIT-MAGIC-COOKIE-1"
	var b bytes.Buffer
	b.Write([]byte{0xff, 0xff, 0, 0, 0, 0, 0, byte(len(name))})
	b.WriteString(name)
	b.Write([]byte{0, byte(len(cookie))})
	b.Write(cookie)
	return b.Bytes()
}

func (m *Manager) startDisplay(s *session, name, xauth string, w, h int) error {
	r, wr, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	argv := []string{
		m.xvncBinary(),
		"-displayfd", "3",
		"-auth", xauth,
		// The socket is the only way in, so the RFB layer needs no password.
		"-rfbunixpath", s.sock(), "-rfbunixmode", "0600", "-rfbport", "-1",
		"-SecurityTypes", "None",
		"-nolisten", "tcp",
		// Several tiles or browsers may watch one app.
		"-AlwaysShared", "-DisconnectClients=0",
		// Only an explicit copy reaches the browser, not every text selection.
		"-SendPrimary=0",
		"-noreset",
		"-desktop", name,
		"-geometry", fmt.Sprintf("%dx%d", w, h), "-depth", "24", "-dpi", "96",
	}
	// DRI3: GL clients hand the display GPU buffers instead of pixels from the CPU.
	if node := renderNode(); node != "" {
		argv = append(argv, "-rendernode", node)
	}
	cmd, err := m.spawn(s, argv, s.dir, m.baseEnv(), "display.log", []*os.File{wr})
	wr.Close()
	if err != nil {
		return err
	}
	s.xpid = cmd.Process.Pid
	_ = r.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		return fmt.Errorf("Xtigervnc tidak siap: %s", lastLine(filepath.Join(s.dir, "display.log")))
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return fmt.Errorf("nomor display tidak valid: %q", line)
	}
	s.display = n
	return nil
}

// renderNode returns the GPU this hub may render on, "" when there is none or
// NEXTBOARD_GUI_GPU=off. Access comes from the video groups or the seat's ACL,
// so only opening the device tells.
func renderNode() string {
	if os.Getenv("NEXTBOARD_GUI_GPU") == "off" {
		return ""
	}
	nodes, _ := filepath.Glob("/dev/dri/renderD*")
	for _, n := range nodes {
		if f, err := os.OpenFile(n, os.O_RDWR, 0); err == nil {
			f.Close()
			return n
		}
	}
	return ""
}

// openboxConfig maximizes normal windows without decoration, so the app fills
// the tile and follows its size. Dialogs stay floating and keep a title bar.
const openboxConfig = `<?xml version="1.0" encoding="UTF-8"?>
<openbox_config xmlns="http://openbox.org/3.4/rc">
  <focus><focusNew>yes</focusNew><followMouse>no</followMouse></focus>
  <placement><policy>Smart</policy><center>yes</center></placement>
  <desktops><number>1</number></desktops>
  <keyboard>
    <keybind key="A-grave"><action name="NextWindow"><finalactions><action name="Focus"/><action name="Raise"/></finalactions></action></keybind>
  </keyboard>
  <mouse>
    <context name="Client">
      <mousebind button="Left" action="Press"><action name="Focus"/><action name="Raise"/></mousebind>
      <mousebind button="Middle" action="Press"><action name="Focus"/><action name="Raise"/></mousebind>
      <mousebind button="Right" action="Press"><action name="Focus"/><action name="Raise"/></mousebind>
    </context>
    <context name="Titlebar">
      <mousebind button="Left" action="Press"><action name="Focus"/><action name="Raise"/></mousebind>
      <mousebind button="Left" action="Drag"><action name="Move"/></mousebind>
    </context>
    <context name="Close">
      <mousebind button="Left" action="Click"><action name="Close"/></mousebind>
    </context>
  </mouse>
  <applications>
    <application class="*" type="normal">
      <decor>no</decor>
      <maximized>yes</maximized>
      <focus>yes</focus>
    </application>
  </applications>
</openbox_config>
`

func (m *Manager) startWM(s *session, xauth string) error {
	rc := filepath.Join(s.dir, "openbox.xml")
	if err := os.WriteFile(rc, []byte(openboxConfig), 0o600); err != nil {
		return err
	}
	bin, _ := lookIn(m.pathEnv(), "openbox")
	wmDir := filepath.Join(s.dir, "wm")
	// Openbox exits without a theme and only looks in the data directories,
	// which a service environment may not point at the system's.
	dataDirs := "/usr/local/share:/usr/share"
	if v := os.Getenv("XDG_DATA_DIRS"); v != "" {
		dataDirs = v + ":" + dataDirs
	}
	env := append(m.baseEnv(),
		fmt.Sprintf("DISPLAY=:%d", s.display), "XAUTHORITY="+xauth,
		"XDG_CONFIG_HOME="+wmDir, "XDG_CACHE_HOME="+wmDir, "XDG_STATE_HOME="+wmDir, "XDG_DATA_DIRS="+dataDirs,
	)
	_, err := m.spawn(s, []string{bin, "--sm-disable", "--config-file", rc}, s.dir, env, "wm.log", nil)
	return err
}

func (m *Manager) startApp(s *session, e *entry, target, xauth string) error {
	argv, env, cwd, err := m.appCommand(s, e, target, xauth, s.pageURL)
	if err != nil {
		return err
	}
	cmd, err := m.spawnNoWait(s, argv, cwd, env, "app.log", nil)
	if err != nil {
		return err
	}
	go func() {
		err := cmd.Wait()
		s.launcher.Store(&launcherExit{at: time.Now(), err: err})
	}()
	return nil
}

// appCommand builds the app's command line on the session's display and
// profile. pageURL goes last, after the Exec field codes have been resolved, so
// nothing in it is read as one.
func (m *Manager) appCommand(s *session, e *entry, target, xauth, pageURL string) (argv, env []string, cwd string, err error) {
	raw, err := parseExec(e.Exec)
	if err != nil {
		return nil, nil, "", err
	}
	argv = expandExec(raw, e, target)
	i := binaryIndex(argv)
	bin, ok := lookIn(m.pathEnv(), argv[i])
	if !ok {
		return nil, nil, "", fmt.Errorf("program tidak ditemukan: %s", argv[i])
	}
	argv[i] = bin
	profile := filepath.Join(m.cfg.Dir, "profiles", e.ID)
	if err := os.MkdirAll(filepath.Join(profile, "data"), 0o700); err != nil {
		return nil, nil, "", err
	}
	flags, profEnv := profileFor(bin, profile)
	if s.pub.Display == DisplayWayland && (familyOf(bin) == familyChromium || familyOf(bin) == familyVSCode) {
		// On X11 only by default, and on Wayland far lighter than under Xwayland.
		flags = append(flags, "--ozone-platform=wayland")
	}
	for _, kv := range profEnv {
		_, dir, _ := strings.Cut(kv, "=")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, nil, "", err
		}
	}
	argv = slices.Concat(argv[:i+1], flags, argv[i+1:])
	if pageURL != "" {
		argv = append(argv, pageURL)
	}
	env = m.baseEnv()
	if s.pub.Display == DisplayWayland {
		env = append(env, "WAYLAND_DISPLAY="+s.waylandSocket(), "XDG_SESSION_TYPE=wayland",
			"GDK_BACKEND=wayland,x11", "QT_QPA_PLATFORM=wayland;xcb", "SDL_VIDEODRIVER=wayland,x11",
			"MOZ_ENABLE_WAYLAND=1", "ELECTRON_OZONE_PLATFORM_HINT=auto", "_JAVA_AWT_WM_NONREPARENTING=1")
		if s.display >= 0 {
			env = append(env, fmt.Sprintf("DISPLAY=:%d", s.display))
		}
	} else {
		env = append(env, fmt.Sprintf("DISPLAY=:%d", s.display), "XAUTHORITY="+xauth, "GDK_BACKEND=x11", "QT_QPA_PLATFORM=xcb")
	}
	env = append(env, profEnv...)
	if s.pub.Audio {
		env = append(env, "PULSE_SINK="+sinkName(s.pub.ID), "PIPEWIRE_NODE="+sinkName(s.pub.ID))
	}
	// A private bus keeps D-Bus activated apps (one instance per bus) from
	// sending their window to an instance on the physical display.
	if p, ok := lookIn(m.pathEnv(), "dbus-run-session"); ok {
		argv = append([]string{p, "--"}, argv...)
	}
	cwd = e.Path
	if target != "" {
		cwd = target
	}
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		cwd = s.dir
	}
	return argv, env, cwd, nil
}

// strippedEnv reports whether an inherited variable must not reach an app:
// markers of the enclosing session would send the VS Code launcher to the
// remote server, or an app window to the physical display.
func strippedEnv(kv string) bool {
	k, _, _ := strings.Cut(kv, "=")
	switch k {
	case "CLAUDECODE", "TERM", "COLORTERM", "PATH", "DISPLAY", "XAUTHORITY", "WAYLAND_DISPLAY", "WAYLAND_SOCKET",
		"DBUS_SESSION_BUS_ADDRESS", "SESSION_MANAGER", "XDG_CURRENT_DESKTOP", "XDG_SEAT", "XDG_VTNR", "DESKTOP_SESSION",
		"DESKTOP_STARTUP_ID", "XDG_ACTIVATION_TOKEN", "GDK_BACKEND", "QT_QPA_PLATFORM", "PULSE_SINK", "PIPEWIRE_NODE",
		"INVOCATION_ID", "JOURNAL_STREAM", "NOTIFY_SOCKET", "SYSTEMD_EXEC_PID":
		return true
	}
	for _, p := range []string{"CLAUDE_CODE_", "NB_", "NEXTBOARD_", "VSCODE_", "ELECTRON_", "TERM_PROGRAM", "SSH_", "XDG_SESSION_", "GIO_LAUNCHED_"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

func (m *Manager) baseEnv() []string {
	env := []string{"PATH=" + m.pathEnv()}
	for _, kv := range os.Environ() {
		if !strippedEnv(kv) {
			env = append(env, kv)
		}
	}
	return env
}

// scoped moves a process out of the hub's systemd unit, whose KillMode would
// end it together with the hub. It stays the same process (systemd-run execs).
func scoped(argv []string, pathEnv string) []string {
	if os.Getenv("INVOCATION_ID") == "" {
		return argv
	}
	run, ok := lookIn(pathEnv, "systemd-run")
	if !ok {
		return argv
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "systemd", "private")); err != nil {
		return argv
	}
	return append([]string{run, "--user", "--scope", "--quiet", "--collect"}, argv...)
}

// spawnNoWait starts a detached process that logs to a file in the session
// folder; pipes would break when the hub restarts.
func (m *Manager) spawnNoWait(s *session, argv []string, dir string, env []string, logName string, extra []*os.File) (*exec.Cmd, error) {
	argv = scoped(argv, m.pathEnv())
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(env, markerEnv+"="+s.pub.ID)
	lf, err := os.OpenFile(filepath.Join(s.dir, logName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer lf.Close()
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.ExtraFiles = extra
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// spawn is spawnNoWait for processes whose exit nobody awaits.
func (m *Manager) spawn(s *session, argv []string, dir string, env []string, logName string, extra []*os.File) (*exec.Cmd, error) {
	cmd, err := m.spawnNoWait(s, argv, dir, env, logName, extra)
	if err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd, nil
}

func (m *Manager) saveState(s *session) error {
	m.mu.Lock()
	st := savedState{Session: s.pub, Display: s.display, XPid: s.xpid, RFBPid: int(s.rfbPid.Load()), HadWindow: s.hadWindow}
	m.mu.Unlock()
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "state.json"))
}

// monitor watches the display until the session ends or the hub stops.
func (m *Manager) monitor(s *session, x displayWatch) {
	defer m.wg.Done()
	defer x.close()
	t := time.NewTicker(m.pollEvery)
	defer t.Stop()
	started := time.Now()
	var emptySince time.Time
	for tick := 0; ; tick++ {
		select {
		case <-s.quit:
			return
		case <-t.C:
		}
		count, title, err := x.windows()
		if err != nil {
			s.stopMonitor()
			m.end(s, ReasonFailed, "Display aplikasi berhenti.")
			return
		}
		if tick%40 == 39 {
			trimLogs(s.dir)
		}
		m.keepRFB(s)
		now := time.Now()
		if count == 0 {
			if s.hadWindow {
				if emptySince.IsZero() {
					emptySince = now
				} else if now.Sub(emptySince) >= m.closeGrace {
					m.end(s, ReasonClosed, "")
					return
				}
				continue
			}
			if msg := m.noWindowReason(s, now.Sub(started), now); msg != "" {
				m.end(s, ReasonFailed, msg)
				return
			}
			continue
		}
		emptySince = time.Time{}
		title = strings.TrimSpace(title)
		if len(title) > 200 {
			title = strings.ToValidUTF8(title[:200], "")
		}
		first := !s.hadWindow
		m.mu.Lock()
		if s.stopping {
			m.mu.Unlock()
			return
		}
		changed := first || (title != "" && title != s.pub.Title)
		s.hadWindow = true
		s.pub.State = StateRunning
		if title != "" {
			s.pub.Title = title
		}
		pub := s.pub
		m.mu.Unlock()
		if first {
			_ = m.saveState(s)
		}
		if changed {
			m.publishSession(pub)
		}
	}
}

// noWindowReason decides whether a session without any window has failed.
func (m *Manager) noWindowReason(s *session, waited time.Duration, now time.Time) string {
	if ex := s.launcher.Load(); ex != nil {
		if ex.err != nil && now.Sub(ex.at) >= 2*time.Second {
			detail := lastLine(filepath.Join(s.dir, "app.log"))
			if detail == "" {
				detail = ex.err.Error()
			}
			return "Aplikasi berhenti sebelum membuka jendela: " + detail
		}
		if ex.err == nil && now.Sub(ex.at) >= m.exitedWait {
			return "Aplikasi keluar tanpa membuka jendela di sini. Mungkin aplikasi itu sudah berjalan di layar lain."
		}
	}
	if waited >= m.windowTimeout {
		return "Aplikasi tidak membuka jendela."
	}
	return ""
}

func trimLogs(dir string) {
	for _, n := range []string{"app.log", "display.log", "wm.log", "rfb.log", "audio.log"} {
		p := filepath.Join(dir, n)
		if fi, err := os.Stat(p); err == nil && fi.Size() > logLimit {
			_ = os.Truncate(p, 0) // writers use O_APPEND
		}
	}
}

func lastLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > 8<<10 {
		b = b[len(b)-8<<10:]
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 300 {
		line = strings.ToValidUTF8(line[:300], "")
	}
	return line
}

// Stop ends a session; the guiSessionRemoved event follows when it is gone.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return notFound()
	}
	m.end(s, ReasonStopped, "")
	return nil
}

// end starts the teardown once; the session stays listed until it finished,
// because a new launch must not race it for the profile.
func (m *Manager) end(s *session, reason, msg string) {
	m.mu.Lock()
	if s.stopping {
		m.mu.Unlock()
		return
	}
	s.stopping = true
	s.pub.State = StateStopping
	pub := s.pub
	m.mu.Unlock()
	s.stopMonitor()
	m.publishSession(pub)
	go func() {
		m.terminate(s)
		m.mu.Lock()
		delete(m.sessions, pub.ID)
		m.mu.Unlock()
		close(s.gone)
		ev := map[string]any{"type": "guiSessionRemoved", "id": pub.ID, "appId": pub.AppID, "reason": reason}
		if msg != "" {
			ev["message"] = msg
		}
		m.publish(ev)
	}()
}

// terminate asks the app to quit, then takes the display away, then forces
// whatever is left. The profile stays; the session folder goes.
func (m *Manager) terminate(s *session) {
	id := s.pub.ID
	// The app first, watched by pid: without its parent it can't be found as
	// part of the session anymore, and it needs the display until it has saved.
	targets := quitTargets(id, s.xpid)
	for _, pid := range targets {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	waitUntil(4*time.Second, func() bool { return !slices.ContainsFunc(targets, processAlive) })
	signalMarked(id, syscall.SIGTERM, s.xpid)
	waitUntil(time.Second, func() bool { return len(markedProcs(id, s.xpid)) == 0 })
	if s.xpid > 0 {
		_ = syscall.Kill(s.xpid, syscall.SIGTERM)
		waitUntil(2*time.Second, func() bool { return !processAlive(s.xpid) })
	}
	signalMarked(id, syscall.SIGKILL, 0)
	_ = os.RemoveAll(s.runDir())
	_ = os.RemoveAll(s.dir)
}

func waitUntil(d time.Duration, done func() bool) {
	deadline := time.Now().Add(d)
	for !done() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	// A zombie still answers kill(0) until its parent reaps it.
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	if i := bytes.LastIndexByte(b, ')'); i >= 0 && i+2 < len(b) {
		return b[i+2] != 'Z'
	}
	return true
}

// sessionProc is one live process of a session.
type sessionProc struct {
	pid    int
	parent int
	comm   string
	marked bool // shows the tag itself
}

// sessionProcs finds a session's live processes: those showing the tag in their
// environment, and everything below them. The tag alone misses Chrome, which
// overwrites the memory /proc/<pid>/environ reads to set its process titles.
// Other users' processes can't be read and don't match.
func sessionProcs(id string) []sessionProc {
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	mark := []byte(markerEnv + "=" + id + "\x00")
	self := os.Getpid()
	all := map[int]*sessionProc{}
	kids := map[int][]int{}
	var roots []int
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid == self {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", d.Name(), "stat"))
		if err != nil {
			continue
		}
		open, end := bytes.IndexByte(stat, '('), bytes.LastIndexByte(stat, ')')
		if open < 0 || end < 0 || end+2 >= len(stat) {
			continue
		}
		fields := bytes.Fields(stat[end+2:])
		if len(fields) < 2 || string(fields[0]) == "Z" {
			continue
		}
		parent, _ := strconv.Atoi(string(fields[1]))
		p := &sessionProc{pid: pid, parent: parent, comm: string(stat[open+1 : end])}
		if b, err := os.ReadFile(filepath.Join("/proc", d.Name(), "environ")); err == nil && bytes.Contains(b, mark) {
			p.marked = true
			roots = append(roots, pid)
		}
		all[pid] = p
		kids[parent] = append(kids[parent], pid)
	}
	seen := map[int]bool{}
	var out []sessionProc
	var walk func(int)
	walk = func(pid int) {
		if seen[pid] || all[pid] == nil {
			return
		}
		seen[pid] = true
		out = append(out, *all[pid])
		for _, k := range kids[pid] {
			walk(k)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

// markedProcs lists the live processes of a session, except pid skip.
func markedProcs(id string, skip int) []int {
	var out []int
	for _, p := range sessionProcs(id) {
		if p.pid != skip {
			out = append(out, p.pid)
		}
	}
	return out
}

// quitTargets picks who gets asked to quit first: not Chrome's renderers one by
// one, which would look like a crash to their browser, only the top of each
// program. The session bus stays for now: when it goes, its wrapper goes, and
// the app quitting at that moment can't reach the bus it may still be using.
func quitTargets(id string, skip int) []int {
	procs := sessionProcs(id)
	marked := map[int]bool{}
	for _, p := range procs {
		if p.marked {
			marked[p.pid] = true
		}
	}
	var out []int
	for _, p := range procs {
		if p.pid == skip || p.comm == "dbus-run-sessio" || p.comm == "dbus-daemon" {
			continue
		}
		if p.marked || marked[p.parent] {
			out = append(out, p.pid)
		}
	}
	return out
}

func signalMarked(id string, sig syscall.Signal, skip int) {
	for _, pid := range markedProcs(id, skip) {
		_ = syscall.Kill(pid, sig)
	}
}

// Start adopts the sessions that survived a hub restart and clears the rest.
func (m *Manager) Start() {
	root := m.sessionsRoot()
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		s, x := m.adopt(dir)
		if s == nil {
			signalMarked(d.Name(), syscall.SIGKILL, 0)
			_ = os.RemoveAll(runDirFor(dir, d.Name()))
			_ = os.RemoveAll(dir)
			continue
		}
		m.mu.Lock()
		m.sessions[s.pub.ID] = s
		m.wg.Add(1)
		m.mu.Unlock()
		go m.monitor(s, x)
	}
}

func (m *Manager) adopt(dir string) (*session, displayWatch) {
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, nil
	}
	var st savedState
	if json.Unmarshal(b, &st) != nil || st.Session.ID != filepath.Base(dir) {
		return nil, nil
	}
	s := &session{
		pub:       st.Session,
		dir:       dir,
		display:   st.Display,
		xpid:      st.XPid,
		hadWindow: st.HadWindow,
		quit:      make(chan struct{}),
		gone:      make(chan struct{}),
	}
	// The pid may have been reused: it must be this session's server.
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", st.XPid))
	if err != nil || !bytes.Contains(cmdline, []byte(dir)) || !processAlive(st.XPid) {
		return nil, nil
	}
	if s.pub.Display == DisplayWayland {
		ipc, err := dialSway(s.swaySock())
		if err != nil {
			return nil, nil
		}
		s.rfbPid.Store(int64(st.RFBPid))
		return s, ipc
	}
	auth, err := os.ReadFile(filepath.Join(dir, "xauth"))
	if err != nil || len(auth) < 16 {
		return nil, nil
	}
	s.cookie = auth[len(auth)-16:]
	x, err := dialX(st.Display, s.cookie)
	if err != nil {
		return nil, nil
	}
	return s, x
}

// Close stops watching but leaves the apps running for the next hub.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for _, s := range m.sessions {
		s.stopMonitor()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
