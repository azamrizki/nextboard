package gui

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// browserArgsEnv names the file the fake browser logs its command lines to.
const browserArgsEnv = "GUI_TEST_BROWSER_ARGS"

// fakeBrowserForwards makes the test binary behave like Chrome: the first run
// on a profile opens a window, later runs hand over their URL and exit. It only
// runs inside a session (see TestMain), so it can never start the tests again.
func fakeBrowserForwards() bool {
	p := os.Getenv(browserArgsEnv)
	if p == "" {
		return false
	}
	// A profile holds one running instance: a later run on the same one hands over and exits.
	profile := profileArg(os.Args[1:])
	before, err := os.ReadFile(p)
	first := os.IsNotExist(err) || !bytes.Contains(before, []byte(profile))
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(6)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(append([]string{"bus=" + os.Getenv("DBUS_SESSION_BUS_ADDRESS")}, os.Args[1:]...), "\x1f") + "\n"); err != nil {
		os.Exit(7)
	}
	return !first
}

func profileArg(args []string) string {
	for i, a := range args {
		if strings.HasPrefix(a, "--user-data-dir=") {
			return a
		}
		if a == "--profile" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "no profile"
}

// fakeBrowser installs a desktop entry for the test binary under a program
// name the hub takes for a browser. It returns the log of its command lines.
func fakeBrowser(t *testing.T, id, program, categories string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), program)
	if err := os.Symlink(self, bin); err != nil {
		t.Fatal(err)
	}
	writeDesktop(t, os.Getenv("XDG_DATA_HOME"), id+".desktop",
		"[Desktop Entry]\nType=Application\nName="+id+"\nExec="+bin+" %U\nCategories="+categories+"\n")
	log := filepath.Join(t.TempDir(), "args")
	t.Setenv(browserArgsEnv, log)
	t.Setenv(helperEnv, "Fake Browser")
	return log
}

func browserRuns(t *testing.T, log string) [][]string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	var out [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		out = append(out, strings.Split(l, "\x1f"))
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func errCode(err error) string {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.Code
	}
	return ""
}

func TestWebURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com":             "https://github.com",
		"  http://192.168.1.5:3000/a  ":  "http://192.168.1.5:3000/a",
		"HTTPS://Example.com/a b?q=1#h":  "https://Example.com/a%20b?q=1#h",
		"https://x.test/a%20b?q=%25%U&z": "https://x.test/a%20b?q=%25%U&z",
	} {
		if got, err := webURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "github.com", "--user-data-dir=/etc", "-x", "javascript:alert(1)", "file:///etc/passwd",
		"chrome://settings", "about:blank", "data:text/html,x", "http://", "https:///a", "https://a\nb",
		"https://x.test/" + strings.Repeat("a", 9000),
	} {
		if got, err := webURL(in); errCode(err) != "bad_url" {
			t.Errorf("%q: accepted as %q (%v)", in, got, err)
		}
	}
}

// TestBrowserChoice: the best supported browser, never a web app that runs the
// same program, and nothing without a browser.
func TestBrowserChoice(t *testing.T) {
	m, _ := newTestManager(t, map[string]string{"notes": ""})
	if e := m.browser(); e != nil {
		t.Fatalf("no browser installed, got %s", e.ID)
	}
	_, err := m.OpenURL(OpenURLRequest{URL: "https://github.com"})
	if errCode(err) != "browser_not_found" || err.(*Error).Status != http.StatusNotFound {
		t.Fatalf("no browser: %v", err)
	}
	fakeBrowser(t, "chrome-webapp", "google-chrome", "Network")
	fakeBrowser(t, "a-editor", "code", "WebBrowser")
	if e := m.browser(); e != nil {
		t.Fatalf("not browsers we support, got %s", e.ID)
	}
	fakeBrowser(t, "a-firefox", "firefox", "Network;WebBrowser")
	if e := m.browser(); e == nil || e.ID != "a-firefox" {
		t.Fatalf("firefox is supported, got %+v", e)
	}
	fakeBrowser(t, "a-brave", "brave-browser", "Network;WebBrowser")
	if e := m.browser(); e == nil || e.ID != "a-brave" {
		t.Fatalf("a Chromium goes before firefox, got %+v", e)
	}
	fakeBrowser(t, "z-chrome", "google-chrome", "Network;WebBrowser;")
	if e := m.browser(); e == nil || e.ID != "z-chrome" {
		t.Fatalf("chrome ranks first, got %+v", e)
	}
}

// TestOpenURL: the first page starts the browser on its profile, the next ones
// are handed to it, and a closed browser starts again.
func TestOpenURL(t *testing.T) {
	m, ev := newTestManager(t, map[string]string{"notes": ""})
	log := fakeBrowser(t, "fake-chrome", "google-chrome", "Network;WebBrowser;")

	const first = "https://github.com/a?q=%25#x"
	opened, err := m.OpenURL(OpenURLRequest{URL: first, Width: 900, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if opened.NewTab || opened.URL != first || opened.Session.AppID != "fake-chrome" {
		t.Fatalf("first %+v", opened)
	}
	id := opened.Session.ID
	waitSession(t, m, ev, "window", func(s Session) bool { return s.ID == id && s.State == StateRunning })
	runs := browserRuns(t, log)
	if len(runs) != 1 || runs[0][len(runs[0])-1] != first {
		t.Fatalf("first run %q", runs)
	}
	profile := "--user-data-dir=" + filepath.Join(m.cfg.Dir, "profiles", "fake-chrome", "data")
	if !slices.Contains(runs[0], profile) {
		t.Fatalf("not on its profile: %q", runs[0])
	}

	const second = "https://example.com/"
	opened, err = m.OpenURL(OpenURLRequest{URL: second})
	if err != nil {
		t.Fatal(err)
	}
	if !opened.NewTab || opened.Session.ID != id {
		t.Fatalf("second %+v", opened)
	}
	waitFor(t, "second run", func() bool { return len(browserRuns(t, log)) == 2 })
	runs = browserRuns(t, log)
	if runs[1][len(runs[1])-1] != second || !slices.Contains(runs[1], profile) {
		t.Fatalf("second run %q", runs[1])
	}
	if n := len(m.List()); n != 1 {
		t.Fatalf("%d sessions after a new tab", n)
	}
	// The tab's helper process does not count as a new window or end the session.
	if s := m.List()[0]; s.ID != id || s.State != StateRunning {
		t.Fatalf("session %+v", s)
	}

	for _, bad := range []string{"--no-sandbox", "file:///etc/passwd"} {
		if _, err := m.OpenURL(OpenURLRequest{URL: bad}); errCode(err) != "bad_url" {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if n := len(browserRuns(t, log)); n != 2 {
		t.Fatalf("a refused address ran the browser (%d runs)", n)
	}

	if err := m.Stop(id); err != nil {
		t.Fatal(err)
	}
	waitGone(t, m, ev, id)
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	opened, err = m.OpenURL(OpenURLRequest{URL: second})
	if err != nil {
		t.Fatal(err)
	}
	if opened.NewTab || opened.Session.ID == id {
		t.Fatalf("after closing %+v", opened)
	}
	waitSession(t, m, ev, "window again", func(s Session) bool { return s.ID == opened.Session.ID && s.State == StateRunning })
}

// TestOpenURLOtherAppsUntouched: an app that is no browser never takes a page.
func TestOpenURLOtherAppsUntouched(t *testing.T) {
	m, ev := newTestManager(t, map[string]string{"notes": ""})
	t.Setenv(helperEnv, "Notes")
	pub, err := m.Launch(LaunchRequest{AppID: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, m, ev, "window", func(s Session) bool { return s.ID == pub.ID && s.State == StateRunning })
	if _, err := m.OpenURL(OpenURLRequest{URL: "https://github.com"}); errCode(err) != "browser_not_found" {
		t.Fatalf("%v", err)
	}
}

// Two tiles, two browsers: each on a profile of its own, or the second would only add a window
// to the display of the first. Tabs still go to the first. A tile that comes back gets its own
// profile again, so its logins are there.
func TestOpenURLHere(t *testing.T) {
	m, ev := newTestManager(t, map[string]string{"notes": ""})
	log := fakeBrowser(t, "fake-chrome", "google-chrome", "Network;WebBrowser;")

	first, err := m.OpenURL(OpenURLRequest{URL: "https://a.example/", Here: true})
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, m, ev, "first window", func(s Session) bool { return s.ID == first.Session.ID && s.State == StateRunning })
	second, err := m.OpenURL(OpenURLRequest{URL: "https://b.example/", Here: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.Session.ID == first.Session.ID || second.Session.Instance != 2 || second.Session.Name != "fake-chrome 2" {
		t.Fatalf("second %+v", second.Session)
	}
	waitSession(t, m, ev, "second window", func(s Session) bool { return s.ID == second.Session.ID && s.State == StateRunning })
	runs := browserRuns(t, log)
	if len(runs) != 2 || profileArg(runs[0]) == profileArg(runs[1]) {
		t.Fatalf("runs %q", runs)
	}
	if want := "--user-data-dir=" + filepath.Join(m.cfg.Dir, "profiles", "fake-chrome~2", "data"); profileArg(runs[1]) != want {
		t.Fatalf("profile %q, want %q", profileArg(runs[1]), want)
	}

	// no tile asked for its own: a tab of the first
	tab, err := m.OpenURL(OpenURLRequest{URL: "https://c.example/"})
	if err != nil {
		t.Fatal(err)
	}
	if !tab.NewTab || tab.Session.ID != first.Session.ID {
		t.Fatalf("tab %+v", tab)
	}

	// the second tile closes and comes back: the same profile
	if err := m.Stop(second.Session.ID); err != nil {
		t.Fatal(err)
	}
	waitGone(t, m, ev, second.Session.ID)
	back, err := m.OpenURL(OpenURLRequest{URL: "https://b.example/", Here: true, Instance: 2})
	if err != nil {
		t.Fatal(err)
	}
	if back.Session.Instance != 2 {
		t.Fatalf("back %+v", back.Session)
	}
	// its number is taken: the next free one, never the same profile twice
	third, err := m.OpenURL(OpenURLRequest{URL: "https://d.example/", Here: true, Instance: 2})
	if err != nil {
		t.Fatal(err)
	}
	if third.Session.Instance != 3 {
		t.Fatalf("third %+v", third.Session)
	}
	if _, err := m.OpenURL(OpenURLRequest{URL: "https://e.example/", Here: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenURL(OpenURLRequest{URL: "https://f.example/", Here: true}); errCode(err) != "too_many_sessions" {
		t.Fatalf("fifth: %v", err)
	}
	// a desktop app tile of the same app still finds the one session, not a new one
	if _, err := m.Launch(LaunchRequest{AppID: "fake-chrome"}); errCode(err) != "already_running" {
		t.Fatalf("launch: %v", err)
	}
}

// Firefox hands a page over on a session bus, so every run of a session has to find the same one.
func TestFirefoxOpensTabsOverItsBus(t *testing.T) {
	m, ev := newTestManager(t, map[string]string{"notes": ""})
	if _, ok := lookIn(m.pathEnv(), "dbus-daemon"); !ok {
		t.Skip("no dbus-daemon")
	}
	log := fakeBrowser(t, "fake-firefox", "firefox", "Network;WebBrowser;")
	opened, err := m.OpenURL(OpenURLRequest{URL: "https://a.example/"})
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, m, ev, "window", func(s Session) bool { return s.ID == opened.Session.ID && s.State == StateRunning })
	if _, err := m.OpenURL(OpenURLRequest{URL: "https://b.example/"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second run", func() bool { return len(browserRuns(t, log)) == 2 })
	runs := browserRuns(t, log)
	profile := filepath.Join(m.cfg.Dir, "profiles", "fake-firefox", "data")
	if !slices.Contains(runs[0], "--new-instance") || slices.Contains(runs[0], "--new-tab") || !slices.Contains(runs[0], profile) {
		t.Fatalf("first run %q", runs[0])
	}
	if slices.Contains(runs[1], "--new-instance") || !slices.Contains(runs[1], "--new-tab") || !slices.Contains(runs[1], profile) {
		t.Fatalf("second run %q", runs[1])
	}
	bus := func(run []string) string { return run[0] }
	sess := filepath.Join(m.cfg.Dir, "sessions", opened.Session.ID, "bus")
	if bus(runs[0]) != "bus=unix:path="+sess || bus(runs[1]) != bus(runs[0]) {
		t.Fatalf("buses %q %q", bus(runs[0]), bus(runs[1]))
	}
	if _, err := os.Stat(sess); err != nil {
		t.Fatalf("no bus: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(profile, "user.js"))
	if err != nil || !strings.Contains(string(b), "checkDefaultBrowser") {
		t.Fatalf("prefs: %q %v", b, err)
	}
}

// A flatpak's flags go after its id, and its profile in its own folder.
func TestFlatpakBrowserCommand(t *testing.T) {
	m, ev := newTestManager(t, map[string]string{"notes": ""})
	home := t.TempDir()
	t.Setenv("HOME", home)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "flatpak")
	if err := os.Symlink(self, bin); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "runs")
	t.Setenv(browserArgsEnv, log)
	writeDesktop(t, os.Getenv("XDG_DATA_HOME"), "org.mozilla.firefox.desktop",
		"[Desktop Entry]\nType=Application\nName=Firefox\nExec="+bin+" run --branch=stable --command=firefox --file-forwarding org.mozilla.firefox @@u %u @@\nCategories=Network;WebBrowser;\n")
	t.Setenv(helperEnv, "Fake Browser")
	opened, err := m.OpenURL(OpenURLRequest{URL: "https://a.example/"})
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, m, ev, "window", func(s Session) bool { return s.ID == opened.Session.ID && s.State == StateRunning })
	runs := browserRuns(t, log)
	want := []string{"run", "--branch=stable", "--command=firefox", "org.mozilla.firefox", "--new-instance", "--profile", filepath.Join(home, ".var", "app", "org.mozilla.firefox", "nextboard", "data"), "https://a.example/"}
	if len(runs) != 1 || !slices.Equal(runs[0][1:len(want)+1], want) {
		t.Fatalf("got %q\nwant %q", runs, want)
	}
}
