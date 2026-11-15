package instance

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/settings"
	"github.com/azamrizki/nextboard/internal/store"
)

// fakeServiceManager makes a scratch HOME and puts a systemctl and a loginctl first on PATH that
// only note their calls: nothing here reaches this machine's service manager. $FAKE_SHOW and
// $FAKE_RESTART are the exit codes of show-environment and restart.
func fakeServiceManager(t *testing.T) (home, calls string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	home = tempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("NEXTBOARD_HOME", "")
	t.Setenv("NEXTBOARD_INSTANCE", "")
	tools := t.TempDir()
	calls = filepath.Join(tools, "calls.log")
	script := `#!/bin/sh
echo "$(basename "$0") $*" >> "` + calls + `"
case "$*" in
*show-environment*) exit ${FAKE_SHOW:-0} ;;
*restart*) exit ${FAKE_RESTART:-0} ;;
esac
`
	for _, n := range []string{"systemctl", "loginctl"} {
		if err := os.WriteFile(filepath.Join(tools, n), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools+":/usr/bin:/bin")
	t.Setenv("FAKE_SHOW", "0")
	t.Setenv("FAKE_RESTART", "0")
	return home, calls
}

func readCalls(t *testing.T, calls string) string {
	t.Helper()
	raw, err := os.ReadFile(calls)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(raw)
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

var allFree = func(int) bool { return true }

// shift moves every block by 30000 ports (block 1 is 42412), and free says all can be bound:
// the test never binds or depends on a real port.
const testShift = 30000

func TestCreateNamedInstance(t *testing.T) {
	home, calls := fakeServiceManager(t)
	def := Instance{Name: Default, DataDir: filepath.Join(home, ".nextboard"), Listen: "127.0.0.1:42312", PreviewRange: [2]int{42320, 42339}}
	c, err := Create(CreateOptions{
		Name: "work", Bin: "/opt/nb/nextboard", Shell: "/bin/zsh", Siblings: []Instance{def},
		URLs: func(listen string, _ []string) []string {
			return []string{"http://" + listen, "http://100.64.0.7:42412"}
		},
		HealthWait: 100 * time.Millisecond, shift: testShift, free: allFree,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".nextboard-work")
	if c.Instance != "work" || c.DataDir != dir || c.ConfigFile != filepath.Join(dir, "config.json") || c.Listen != "127.0.0.1:42412" || c.PreviewRange != [2]int{42420, 42439} {
		t.Fatalf("created %+v", c)
	}
	if !c.Started || c.Healthy || c.Command != "" || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "nextboard logs --instance work") {
		t.Fatalf("start %+v", c)
	}
	// the folder and its files: 0700 and 0600
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("folder %v", m)
	}
	for _, f := range []string{"config.json", "settings.json", "presets.json", "auth.json"} {
		if m := mode(t, filepath.Join(dir, f)); m != 0o600 {
			t.Errorf("%s %v", f, m)
		}
	}
	var cfg map[string]any
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["instance"] != "work" || cfg["listen"] != "127.0.0.1:42412" || cfg["hubId"] != c.HubID || !ValidHubID(c.HubID) || len(c.HubID) != 10 {
		t.Fatalf("config %v", cfg)
	}
	if id, err := ReadIdentity(dir); err != nil || !id.Recorded || id.Name != "work" || id.HubID != c.HubID {
		t.Fatalf("identity %+v %v", id, err)
	}
	// the hub reads the preview range from its own settings
	st, _ := store.Open(dir)
	sets, err := settings.Open(st)
	if err != nil {
		t.Fatal(err)
	}
	if r := sets.Get().Previews; r.PortRange != [2]int{42420, 42439} || !r.Auto {
		t.Fatalf("previews %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "presets.json")); string(b) != "[]\n" {
		t.Fatalf("presets %q", b)
	}

	// its own unit, started by the service manager, with linger
	unitFile := filepath.Join(home, ".config", "systemd", "user", "nextboard-work.service")
	unit, err := os.ReadFile(unitFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Description=NextBoard hub "work" (visual board for AI coding agents)`, "ExecStart=/opt/nb/nextboard serve --instance work --data-dir " + dir + "\n",
		"Environment=SHELL=/bin/zsh\n", "Environment=HOME=" + home + "\n", "WantedBy=default.target"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if c.Service == nil || *c.Service != (Service{Kind: "systemd", Name: "nextboard-work.service", UnitPath: unitFile, Bin: "/opt/nb/nextboard"}) {
		t.Fatalf("service %+v", c.Service)
	}
	u, _ := user.Current()
	want := "systemctl --user show-environment\nsystemctl --user daemon-reload\nsystemctl --user enable nextboard-work\nsystemctl --user restart nextboard-work\nloginctl enable-linger " + u.Username + "\n"
	if got := readCalls(t, calls); got != want {
		t.Fatalf("calls:\n%s", got)
	}

	// the setup links: one per address, one code, single use, 30 minutes
	if len(c.SetupURLs) != 2 || c.SetupURLs[0] != "http://127.0.0.1:42412/setup?code="+c.SetupCode || c.SetupURLs[1] != "http://100.64.0.7:42412/setup?code="+c.SetupCode {
		t.Fatalf("setup urls %v", c.SetupURLs)
	}
	if d := time.Until(c.ExpiresAt); d < 29*time.Minute || d > 31*time.Minute {
		t.Fatalf("expires in %v", d)
	}
	as, err := auth.Open(st)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := as.CompleteSetup(c.SetupCode, "rahasia123"); err != nil {
		t.Fatalf("the code: %v", err)
	}
	if _, err := as.CompleteSetup(c.SetupCode, "rahasia456"); err == nil {
		t.Fatal("the code worked twice")
	}
	// what POST /api/instances answers: the links, never the code on its own
	body, _ := json.Marshal(c)
	if strings.Contains(string(body), `"setupCode"`) || !strings.Contains(string(body), `"setupUrls"`) || !strings.Contains(string(body), `"started":true`) {
		t.Fatalf("json %s", body)
	}

	// and List finds it, with its service
	ins, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 1 || ins[0].Name != "work" || ins[0].Service == nil || ins[0].Service.Bin != "/opt/nb/nextboard" || ins[0].PreviewRange != [2]int{42420, 42439} {
		t.Fatalf("list %+v", ins)
	}
	// a second one with that name is refused, now that it is there
	if _, err := Create(CreateOptions{Name: "work", Bin: "/opt/nb/nextboard", shift: testShift, free: allFree}); !errors.Is(err, ErrExists) {
		t.Fatalf("again: %v", err)
	}
}

func TestCreateRefuses(t *testing.T) {
	home, calls := fakeServiceManager(t)
	work := Instance{Name: "work", DataDir: filepath.Join(home, "srv", "work"), Listen: "127.0.0.1:42412", StatusListen: "0.0.0.0:42499", PreviewRange: [2]int{42420, 42439}}
	opts := func(name string) CreateOptions {
		return CreateOptions{Name: name, Bin: "/opt/nb/nextboard", Siblings: []Instance{work}, HealthWait: time.Millisecond, shift: testShift, free: allFree}
	}
	gone := func(name string) {
		t.Helper()
		if _, err := os.Lstat(filepath.Join(home, ".nextboard-"+name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left a folder: %v", name, err)
		}
	}

	for _, bad := range []string{"", "Work", "1x", "a b", "../x", strings.Repeat("a", 25)} {
		if _, err := Create(opts(bad)); !errors.Is(err, ErrBadName) {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	// a sibling of that name elsewhere, a folder that holds something, a unit already there
	if _, err := Create(opts("work")); !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), work.DataDir) {
		t.Errorf("sibling: %v", err)
	}
	writeFile(t, filepath.Join(home, ".nextboard-used", "notes.txt"), "mine")
	if _, err := Create(opts("used")); !errors.Is(err, ErrExists) {
		t.Errorf("folder: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".nextboard-used", "notes.txt")); string(b) != "mine" {
		t.Error("a refused create touched the folder")
	}
	writeUnit(t, home, "nextboard-unit.service", "/opt/nb/nextboard serve --instance unit --data-dir /elsewhere")
	o := opts("unit")
	o.Siblings = []Instance{}
	if _, err := Create(o); !errors.Is(err, ErrExists) {
		t.Errorf("unit: %v", err)
	}
	gone("unit")

	// ports: a sibling's, a busy one, a bad address; nothing is left behind
	for _, l := range []string{"127.0.0.1:42412", "0.0.0.0:42425", "127.0.0.1:42499"} {
		o := opts("side")
		o.Listen = l
		if _, err := Create(o); !errors.Is(err, ErrPortInUse) || !strings.Contains(err.Error(), "work") {
			t.Errorf("listen %s: %v", l, err)
		}
	}
	o = opts("side")
	o.Listen = "127.0.0.1:43000"
	o.free = func(p int) bool { return p != 43000 }
	if _, err := Create(o); !errors.Is(err, ErrPortInUse) || !strings.Contains(err.Error(), "taken") {
		t.Errorf("busy: %v", err)
	}
	for _, l := range []string{"43000", "127.0.0.1:x", "127.0.0.1:70000"} {
		o := opts("side")
		o.Listen = l
		if _, err := Create(o); !errors.Is(err, ErrBadListen) {
			t.Errorf("bad listen %s: %v", l, err)
		}
	}
	o = opts("side")
	o.StatusListen = "0.0.0.0:42499"
	if _, err := Create(o); !errors.Is(err, ErrPortInUse) {
		t.Errorf("status listen of a sibling: %v", err)
	}
	o.StatusListen = "0.0.0.0:42522" // one of its own previews
	if _, err := Create(o); !errors.Is(err, ErrPortInUse) {
		t.Errorf("status listen on its previews: %v", err)
	}
	o = opts("side")
	o.free = func(int) bool { return false }
	if _, err := Create(o); !errors.Is(err, ErrNoFreeBlock) {
		t.Errorf("no block: %v", err)
	}
	gone("side")
	if got := readCalls(t, calls); got != "" {
		t.Fatalf("a refused create called the service manager:\n%s", got)
	}

	// an empty folder is no instance yet: it is used; and a picked block skips work's previews
	if err := os.MkdirAll(filepath.Join(home, ".nextboard-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := Create(opts("empty"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:42512" || c.PreviewRange != [2]int{42520, 42539} || mode(t, c.DataDir) != 0o700 {
		t.Fatalf("empty folder %+v", c)
	}
}

// Without a service manager (a container, a machine without systemd), the folder and the setup
// code are made and the result says how to start the hub.
func TestCreateWithoutServiceManager(t *testing.T) {
	home, calls := fakeServiceManager(t)
	t.Setenv("FAKE_SHOW", "1")
	c, err := Create(CreateOptions{Name: "side", Bin: "/opt/nb/nextboard", Listen: "127.0.0.1:42612", Siblings: []Instance{}, shift: testShift, free: allFree})
	if err != nil {
		t.Fatal(err)
	}
	if c.Started || c.Service != nil || c.Command != "nextboard serve --instance side" || c.SetupCode == "" || c.Listen != "127.0.0.1:42612" || c.PreviewRange != [2]int{42420, 42439} {
		t.Fatalf("created %+v", c)
	}
	if c.URLs[0] != "http://127.0.0.1:42612" || c.SetupURLs[0] != "http://127.0.0.1:42612/setup?code="+c.SetupCode {
		t.Fatalf("urls %v %v", c.URLs, c.SetupURLs)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "nextboard-side.service")); err == nil {
		t.Fatal("a unit with no service manager to run it")
	}
	if got := readCalls(t, calls); got != "systemctl --user show-environment\n" {
		t.Fatalf("calls %q", got)
	}
	// the code is in the folder, for the hub that starts on it
	st, _ := store.Open(c.DataDir)
	as, _ := auth.Open(st)
	if !as.NeedsSetup() {
		t.Fatal("set up already")
	}
	if _, err := as.CompleteSetup(c.SetupCode, "rahasia123"); err != nil {
		t.Fatal(err)
	}

	// a folder of the CLI's choosing goes into the command
	srv := filepath.Join(tempDir(t), "srv nb")
	c, err = Create(CreateOptions{Name: "other", DataDir: srv, Bin: "/opt/nb/nextboard", Siblings: []Instance{}, shift: testShift, free: allFree})
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != srv || c.Command != "nextboard serve --instance other --data-dir '"+srv+"'" {
		t.Fatalf("custom folder %+v", c)
	}
}

// What `serve --instance N` does on its first start: the folder only, as the hub it then is.
func TestCreateFolderOnly(t *testing.T) {
	home, calls := fakeServiceManager(t)
	dir := filepath.Join(home, ".nextboard-work")
	// the unit of work runs the folder that is not there yet: its own first start
	writeUnit(t, home, "nextboard-work.service", "/opt/nb/nextboard serve --instance work --data-dir "+dir)
	c, err := Create(CreateOptions{Name: "work", DataDir: dir, Listen: "127.0.0.1:42777", FolderOnly: true, shift: testShift, free: allFree})
	if err != nil {
		t.Fatal(err)
	}
	if c.Started || c.Service != nil || c.SetupCode != "" || c.Command != "" || c.Listen != "127.0.0.1:42777" || c.PreviewRange != [2]int{42420, 42439} {
		t.Fatalf("created %+v", c)
	}
	if _, err := os.Stat(filepath.Join(dir, "auth.json")); err == nil {
		t.Error("a setup code: the hub makes its own")
	}
	if got := readCalls(t, calls); got != "" {
		t.Fatalf("calls %q", got)
	}
	if id, _ := ReadIdentity(dir); id.Name != "work" || !id.Recorded {
		t.Fatalf("identity %+v", id)
	}
	// another folder under a name a unit already has is refused
	if _, err := Create(CreateOptions{Name: "work", DataDir: filepath.Join(home, "elsewhere"), FolderOnly: true, shift: testShift, free: allFree}); !errors.Is(err, ErrExists) {
		t.Fatalf("elsewhere: %v", err)
	}
}

// A service that does not start leaves the instance there and says so.
func TestCreateServiceFails(t *testing.T) {
	home, _ := fakeServiceManager(t)
	t.Setenv("FAKE_RESTART", "1")
	c, err := Create(CreateOptions{Name: "work", Bin: "/opt/nb/nextboard", Siblings: []Instance{}, shift: testShift, free: allFree})
	if err == nil || c == nil || c.Started || c.Service == nil || !strings.Contains(err.Error(), "nextboard-work.service did not start") {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".nextboard-work", "config.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Service.UnitPath); err != nil {
		t.Fatal(err)
	}
}

// The default instance's unit and plist are word for word what `install` always wrote.
func TestRenderUnitDefaultUnchanged(t *testing.T) {
	spec := UnitSpec{Name: Default, Bin: "/home/alice/.local/bin/nextboard", DataDir: "/home/alice/.nextboard", Shell: "/bin/bash", Home: "/home/alice"}
	unit, err := RenderUnit("linux", spec)
	if err != nil {
		t.Fatal(err)
	}
	wantUnit := `[Unit]
Description=NextBoard hub (visual board for AI coding agents)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/home/alice/.local/bin/nextboard serve --data-dir /home/alice/.nextboard
Restart=on-failure
RestartSec=3
Environment=SHELL=/bin/bash
Environment=HOME=/home/alice
# Keep remote tmux sessions alive; local agents are relaunched with resumeArgs.
KillMode=mixed
TimeoutStopSec=15

[Install]
WantedBy=default.target
`
	if string(unit) != wantUnit {
		t.Errorf("unit:\n%s", unit)
	}
	plist, err := RenderUnit("darwin", spec)
	if err != nil {
		t.Fatal(err)
	}
	wantPlist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>id.nextboard</string>
  <key>ProgramArguments</key>
  <array>
    <string>/home/alice/.local/bin/nextboard</string>
    <string>serve</string>
    <string>--data-dir</string>
    <string>/home/alice/.nextboard</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SHELL</key><string>/bin/bash</string>
    <key>HOME</key><string>/home/alice</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/home/alice/.nextboard/nextboard.log</string>
  <key>StandardErrorPath</key><string>/home/alice/.nextboard/nextboard.log</string>
</dict>
</plist>
`
	if string(plist) != wantPlist {
		t.Errorf("plist:\n%s", plist)
	}
	if _, err := RenderUnit("linux", UnitSpec{Name: Default, Bin: "nextboard", DataDir: "/x"}); err == nil {
		t.Error("a relative binary")
	}
	if _, err := RenderUnit("linux", UnitSpec{Name: "work", Bin: "/x/nextboard", DataDir: "/x\nExecStartPre=/bin/evil"}); err == nil {
		t.Error("a line break in a path")
	}
}

// A named instance's files read back as that instance: its name, its folder, its binary, also
// with a space in a path.
func TestRenderUnitNamedReadsBack(t *testing.T) {
	home := tempDir(t)
	spec := UnitSpec{Name: "work", Bin: "/opt/next board/nextboard", DataDir: filepath.Join(home, "data & more"), Shell: "/bin/zsh", Home: home}
	unit, err := RenderUnit("linux", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), `ExecStart="/opt/next board/nextboard" serve --instance work --data-dir "`+spec.DataDir+`"`) {
		t.Fatalf("unit:\n%s", unit)
	}
	plist, err := RenderUnit("darwin", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plist), "<string>id.nextboard.work</string>") || !strings.Contains(string(plist), "data &amp; more") {
		t.Fatalf("plist:\n%s", plist)
	}
	writeFile(t, filepath.Join(home, ".config", "systemd", "user", "nextboard-work.service"), string(unit))
	writeFile(t, filepath.Join(home, "Library", "LaunchAgents", "id.nextboard.work.plist"), string(plist))
	for _, goos := range []string{"linux", "darwin"} {
		us := listUnits(home, goos)
		if len(us) != 1 || us[0].name != "work" || us[0].dataDir != spec.DataDir || us[0].svc.Bin != spec.Bin {
			t.Errorf("%s reads back %+v", goos, us)
		}
	}
}

func TestServicesOn(t *testing.T) {
	home := tempDir(t)
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "bin", "nextboard")
	writeFile(t, bin, "#!/bin/sh\n")
	link := filepath.Join(home, "nextboard-link")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, home, "nextboard.service", bin+" serve --data-dir "+filepath.Join(home, ".nextboard"))
	writeUnit(t, home, "nextboard-work.service", link+" serve --instance work")
	writeUnit(t, home, "nextboard-old.service", "/opt/old/nextboard serve --instance old")
	if runtime.GOOS != "linux" {
		t.Skip("systemd units")
	}
	ins, err := ServicesOn(bin)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, in := range ins {
		names = append(names, in.Name)
	}
	if strings.Join(names, ",") != "default,work" {
		t.Fatalf("on %s: %v", bin, names)
	}
	svc, dir, err := FindService("work")
	if err != nil || svc == nil || svc.Name != "nextboard-work.service" || dir != filepath.Join(home, ".nextboard-work") {
		t.Fatalf("work: %+v %s %v", svc, dir, err)
	}
	if svc, _, _ := FindService("nothing"); svc != nil {
		t.Fatalf("nothing: %+v", svc)
	}
	if SameBinary(bin, "/opt/old/nextboard") || !SameBinary(link, bin) || SameBinary("", "") {
		t.Fatal("SameBinary")
	}
}

func TestHealth(t *testing.T) {
	for listen, want := range map[string]string{
		"127.0.0.1:12412":  "http://127.0.0.1:12412/api/health",
		"0.0.0.0:12412":    "http://127.0.0.1:12412/api/health",
		":12412":           "http://127.0.0.1:12412/api/health",
		"[::]:12412":       "http://127.0.0.1:12412/api/health",
		"localhost:12412":  "http://127.0.0.1:12412/api/health",
		"100.64.0.7:12412": "http://100.64.0.7:12412/api/health",
		"[fd7a::1]:12412":  "http://[fd7a::1]:12412/api/health",
	} {
		if got := HealthURL(listen); got != want {
			t.Errorf("%s: %s", listen, got)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"ok":true,"version":"0.9.0","commit":"3f2a1c9"}`))
	}))
	defer ts.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	h, err := CheckHealth("0.0.0.0:"+port, time.Second)
	if err != nil || h.Version != "0.9.0" || h.Commit != "3f2a1c9" {
		t.Fatalf("%+v %v", h, err)
	}
	if !WaitHealthy("127.0.0.1:"+port, time.Second) {
		t.Fatal("not healthy")
	}
	ts.Close()
	p, _ := strconv.Atoi(port)
	start := time.Now()
	if WaitHealthy("127.0.0.1:"+strconv.Itoa(p), 400*time.Millisecond) || time.Since(start) > 5*time.Second {
		t.Fatal("healthy without a hub")
	}
}
