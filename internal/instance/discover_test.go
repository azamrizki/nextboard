package instance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/config"
)

// tempDir is a scratch folder with no link in its path, as List reports folders.
func tempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func named(ins []Instance) map[string][]Instance {
	m := map[string][]Instance{}
	for _, in := range ins {
		m[in.Name] = append(m[in.Name], in)
	}
	return m
}

func hasProblem(in Instance, part string) bool {
	return slices.ContainsFunc(in.Problems, func(p string) bool { return strings.Contains(p, part) })
}

// A scratch HOME with what `install` leaves: units, folders, one folder outside HOME, a
// unit that is not a hub, a copy of a folder, and two instances on one port.
func TestListSystemd(t *testing.T) {
	home, srv := tempDir(t), tempDir(t)
	def := filepath.Join(home, ".nextboard")
	writeUnit(t, home, "nextboard.service", "/home/alice/.local/bin/nextboard serve --data-dir "+def)
	writeFile(t, filepath.Join(def, config.File), `{"instance":"default","hubId":"aaaaaaaaaa","hubName":"devbox"}`)

	work := filepath.Join(srv, "work")
	writeUnit(t, home, "nextboard-work.service", `"/opt/next board/nextboard" serve --instance work --data-dir=`+work)
	writeFile(t, filepath.Join(work, config.File), `{"instance":"work","hubId":"bbbbbbbbbb","listen":"127.0.0.1:12412","statusListen":"0.0.0.0:12499"}`)
	writeFile(t, filepath.Join(work, "settings.json"), `{"previews":{"auto":true,"portRange":[12420,12439]}}`)

	// a legacy folder on the same hub port as work: both are told
	writeFile(t, filepath.Join(home, ".nextboard-personal", config.File), `{"listen":"127.0.0.1:12412"}`)
	// a copy of work's folder: two instances named work
	writeFile(t, filepath.Join(home, ".nextboard-copy", config.File), `{"instance":"work","listen":"127.0.0.1:12612"}`)
	writeFile(t, filepath.Join(home, ".nextboard-copy", "settings.json"), `{"previews":{"portRange":[12620,12639]}}`)
	// not instances: no config.json, a file, a unit that runs something else, a bad unit name
	os.MkdirAll(filepath.Join(home, ".nextboard-empty"), 0o700)
	writeFile(t, filepath.Join(home, ".nextboard-file"), "x")
	writeUnit(t, home, "nextboard-tunnel.service", "/usr/bin/ssh -N -L 12312:127.0.0.1:12312 devbox")
	writeUnit(t, home, "nextboard-Bad.service", "/x/nextboard serve --data-dir "+filepath.Join(srv, "bad"))
	// a unit whose folder does not exist (yet)
	writeUnit(t, home, "nextboard-later.service", "/x/nextboard serve --instance later --data-dir "+filepath.Join(srv, "later"))

	ins, err := list(home, "linux")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, in := range ins {
		names = append(names, in.Name)
	}
	if strings.Join(names, ",") != "default,later,personal,work,work" {
		t.Fatalf("names %v\n%+v", names, ins)
	}
	m := named(ins)

	d := m["default"][0]
	if d.DataDir != def || d.HubID != "aaaaaaaaaa" || d.ConfigFile != filepath.Join(def, config.File) || !d.Configured ||
		d.Listen != config.DefaultListen || d.PreviewRange != [2]int{12320, 12339} || d.Running {
		t.Errorf("default %+v", d)
	}
	if d.Service == nil || d.Service.Kind != "systemd" || d.Service.Name != "nextboard.service" || d.Service.Bin != "/home/alice/.local/bin/nextboard" ||
		d.Service.UnitPath != filepath.Join(home, ".config", "systemd", "user", "nextboard.service") {
		t.Errorf("default service %+v", d.Service)
	}

	var w, cp Instance
	for _, in := range m["work"] {
		if in.DataDir == work {
			w = in
		} else {
			cp = in
		}
	}
	if w.Service == nil || w.Service.Name != "nextboard-work.service" || w.Service.Bin != "/opt/next board/nextboard" ||
		w.Listen != "127.0.0.1:12412" || w.StatusListen != "0.0.0.0:12499" || w.PreviewRange != [2]int{12420, 12439} {
		t.Errorf("work %+v %+v", w, w.Service)
	}
	if cp.DataDir != filepath.Join(home, ".nextboard-copy") || cp.Service != nil {
		t.Errorf("copy %+v", cp)
	}
	if !hasProblem(w, "also named work") || !hasProblem(cp, "also named work") {
		t.Errorf("duplicate name not told: %v / %v", w.Problems, cp.Problems)
	}
	p := m["personal"][0]
	if p.HubID != "" || p.Service != nil || p.PreviewRange != [2]int{12320, 12339} {
		t.Errorf("personal %+v", p)
	}
	// personal shares 12412 with work, and the default's previews with the default instance
	if !hasProblem(p, "instance work ("+work+") also uses port 12412") || !hasProblem(w, "instance personal") {
		t.Errorf("port overlap not told: %v / %v", p.Problems, w.Problems)
	}
	if !hasProblem(p, "instance default") || !hasProblem(d, "instance personal") {
		t.Errorf("preview overlap not told: %v", p.Problems)
	}
	if hasProblem(cp, "instance default") || hasProblem(cp, "instance personal") {
		t.Errorf("copy has its own ports: %v", cp.Problems)
	}
	l := m["later"][0]
	if l.Configured || l.DataDir != filepath.Join(srv, "later") || l.Service == nil || l.HubID != "" {
		t.Errorf("later %+v", l)
	}
	// List reads; it writes nothing
	if _, err := os.Stat(filepath.Join(home, ".nextboard-personal", "hub.lock")); err == nil {
		t.Error("List made a lock file")
	}
	if raw, _ := os.ReadFile(filepath.Join(home, ".nextboard-personal", config.File)); string(raw) != `{"listen":"127.0.0.1:12412"}` {
		t.Errorf("List wrote config.json: %s", raw)
	}

	// a running hub is seen
	lk, err := AcquireLock(filepath.Join(home, ".nextboard-personal"))
	if err != nil {
		t.Fatal(err)
	}
	ins, _ = list(home, "linux")
	p = named(ins)["personal"][0]
	lk.Release()
	if !p.Running || p.PID != os.Getpid() {
		t.Errorf("running not seen: %+v", p)
	}
}

func TestListUnitAndConfigDisagree(t *testing.T) {
	home := tempDir(t)
	dir := filepath.Join(home, ".nextboard-work")
	writeUnit(t, home, "nextboard-work.service", "/x/nextboard serve --data-dir "+dir)
	writeUnit(t, home, "nextboard-side.service", "/x/nextboard serve --data-dir "+dir)
	writeFile(t, filepath.Join(dir, config.File), `{"instance":"personal","listen":"127.0.0.1:12512"}`)
	ins, _ := list(home, "linux")
	if len(ins) != 1 {
		t.Fatalf("%+v", ins)
	}
	in := ins[0]
	if in.Name != "personal" || !hasProblem(in, "config.json says instance personal") || !hasProblem(in, "several services run this folder") {
		t.Errorf("%+v", in)
	}
	// a folder without a recorded name takes its unit's
	writeFile(t, filepath.Join(dir, config.File), `{"listen":"127.0.0.1:12512"}`)
	os.Remove(filepath.Join(home, ".config", "systemd", "user", "nextboard-side.service"))
	ins, _ = list(home, "linux")
	if len(ins) != 1 || ins[0].Name != "work" || len(ins[0].Problems) != 0 {
		t.Errorf("%+v", ins)
	}
}

func TestListLaunchd(t *testing.T) {
	home, srv := tempDir(t), tempDir(t)
	plist := func(label string, args []string, env string) string {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + label + `</string>
  <key>ProgramArguments</key>
  <array>
`)
		for _, a := range args {
			b.WriteString("    <string>" + a + "</string>\n")
		}
		b.WriteString(`  </array>
  <key>EnvironmentVariables</key>
  <dict>` + env + `</dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
`)
		return b.String()
	}
	agents := filepath.Join(home, "Library", "LaunchAgents")
	def := filepath.Join(home, ".nextboard")
	writeFile(t, filepath.Join(agents, "id.nextboard.plist"), plist("id.nextboard", []string{"/Users/alice/.local/bin/nextboard", "serve", "--data-dir", def}, `<key>HOME</key><string>`+home+`</string>`))
	work := filepath.Join(srv, "work &amp; more")
	writeFile(t, filepath.Join(agents, "id.nextboard.work.plist"), plist("id.nextboard.work", []string{"/Users/alice/.local/bin/nextboard", "serve", "--instance", "work", "--data-dir", work}, ""))
	// no --data-dir: $NEXTBOARD_HOME from the plist's environment
	envDir := filepath.Join(srv, "env")
	writeFile(t, filepath.Join(agents, "id.nextboard.side.plist"), plist("id.nextboard.side", []string{"/x/nextboard", "serve"}, `<key>NEXTBOARD_HOME</key><string>`+envDir+`</string>`))
	writeFile(t, filepath.Join(agents, "id.nextboard.other.plist"), plist("id.nextboard.other", []string{"/x/nextboard", "update"}, ""))
	writeFile(t, filepath.Join(agents, "id.nextboard.broken.plist"), "<plist><dict><key>ProgramArguments</key><array><string>")
	writeFile(t, filepath.Join(def, config.File), `{}`)

	ins, err := list(home, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	m := named(ins)
	if len(ins) != 3 || len(m["default"]) != 1 || len(m["work"]) != 1 || len(m["side"]) != 1 {
		t.Fatalf("%+v", ins)
	}
	if s := m["default"][0].Service; s == nil || s.Kind != "launchd" || s.Name != "id.nextboard" || s.Bin != "/Users/alice/.local/bin/nextboard" || m["default"][0].DataDir != def {
		t.Errorf("default %+v %+v", m["default"][0], s)
	}
	if w := m["work"][0]; w.DataDir != filepath.Join(srv, "work & more") || w.Service.Name != "id.nextboard.work" ||
		w.Service.UnitPath != filepath.Join(agents, "id.nextboard.work.plist") {
		t.Errorf("work %+v %+v", w, w.Service)
	}
	if s := m["side"][0]; s.DataDir != envDir {
		t.Errorf("side %+v", s)
	}
	// Linux does not read LaunchAgents, nor macOS systemd units
	if ins, _ := list(home, "linux"); len(ins) != 1 || ins[0].Service != nil {
		t.Errorf("linux saw %+v", ins)
	}
}

func TestListFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if ins, err := List(); err != nil || len(ins) != 0 {
		t.Fatalf("%+v %v", ins, err)
	}
	writeFile(t, filepath.Join(home, ".nextboard", config.File), `{}`)
	if ins, err := List(); err != nil || len(ins) != 1 || ins[0].Name != "default" {
		t.Fatalf("%+v %v", ins, err)
	}
}

func TestSplitWords(t *testing.T) {
	got := splitWords(`/bin/nb serve  --data-dir "/a b/c" 'x y'z --listen=1`)
	want := []string{"/bin/nb", "serve", "--data-dir", "/a b/c", "x yz", "--listen=1"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	args, _ := parseUnit([]byte("ExecStart=-/bin/nb serve --dev --data-dir /d\n"))
	if dir, ok := servedDir("/home/alice", args, nil); !ok || dir != "/d" || args[0] != "/bin/nb" {
		t.Fatalf("%q %s %v", args, dir, ok)
	}
}
