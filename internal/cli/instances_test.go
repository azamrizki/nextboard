package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/instance"
)

// nb runs the built binary as the box's user.
func (b *box) nb(args ...string) (string, error) {
	b.t.Helper()
	cmd := exec.Command(nextboardBinary(b.t), args...)
	cmd.Env = b.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (b *box) mustNB(args ...string) string {
	b.t.Helper()
	out, err := b.nb(args...)
	if err != nil {
		b.t.Fatalf("nextboard %s: %v\n%s\ncalls:\n%s", strings.Join(args, " "), err, out, b.callLog())
	}
	return out
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// design §9.2: the default instance and `work` side by side, each with its own folder, unit,
// port and setup link, both healthy at once; listed, logged and removed one at a time.
func TestTwoInstancesSideBySide(t *testing.T) {
	b := newBox(t)
	writeExec(t, filepath.Join(b.bin, "journalctl"), `echo "journalctl $*" >> "`+b.calls+`"`+"\n")
	bin := nextboardBinary(t)
	units := filepath.Join(b.home, ".config", "systemd", "user")

	out := b.mustNB("install")
	waitHealthyHub(t, b.listen)
	m := setupCodeRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no setup link:\n%s", out)
	}
	defaultCode := m[1]
	defaultUnit, _ := os.ReadFile(filepath.Join(units, "nextboard.service"))
	if !strings.Contains(string(defaultUnit), "ExecStart="+bin+" serve --data-dir "+filepath.Join(b.home, ".nextboard")+"\n") {
		t.Fatalf("default unit:\n%s", defaultUnit)
	}

	workListen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	out = b.mustNB("install", "--instance", "work", "--listen", workListen)
	work := filepath.Join(b.home, ".nextboard-work")
	for _, want := range []string{"New instance work: data in " + work + ", hub on " + workListen, "Service installed: " + filepath.Join(units, "nextboard-work.service"),
		"NextBoard instance work is running at http://" + workListen, "Open this setup link"} {
		if !strings.Contains(out, want) {
			t.Errorf("install --instance said no %q:\n%s", want, out)
		}
	}
	m = setupCodeRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no setup link:\n%s", out)
	}
	workCode := m[1]
	// both hubs answer at the same time
	waitHealthyHub(t, workListen)
	waitHealthyHub(t, b.listen)

	var cfg struct {
		Instance, HubID, Listen string
	}
	readJSON(t, filepath.Join(work, "config.json"), &cfg)
	if cfg.Instance != "work" || !instance.ValidHubID(cfg.HubID) || cfg.Listen != workListen {
		t.Fatalf("work config %+v", cfg)
	}
	var sets struct {
		Previews struct{ PortRange [2]int }
	}
	readJSON(t, filepath.Join(work, "settings.json"), &sets)
	if r := sets.Previews.PortRange; r[0] < 12420 || r[0]%100 != 20 || r[1] != r[0]+19 {
		t.Fatalf("work previews %v", r)
	}
	if fi, _ := os.Stat(work); fi.Mode().Perm() != 0o700 {
		t.Errorf("work folder %v", fi.Mode())
	}
	unit, err := os.ReadFile(filepath.Join(units, "nextboard-work.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "ExecStart="+bin+" serve --instance work --data-dir "+work+"\n") {
		t.Fatalf("work unit:\n%s", unit)
	}
	if again, _ := os.ReadFile(filepath.Join(units, "nextboard.service")); string(again) != string(defaultUnit) {
		t.Fatalf("the default unit changed:\n%s", again)
	}
	calls := b.callLog()
	for _, want := range []string{"systemctl --user enable nextboard-work\n", "systemctl --user restart nextboard-work\n"} {
		if !strings.Contains(calls, want) {
			t.Errorf("never called %q:\n%s", want, calls)
		}
	}
	if strings.Count(calls, "systemctl --user restart nextboard\n") != 1 {
		t.Errorf("the default restarted for work's install:\n%s", calls)
	}

	// each setup link opens its own hub only
	def, wc := newClient(t, b.listen), newClient(t, workListen)
	if st, _ := def.do("POST", "/api/setup", map[string]any{"code": workCode, "password": "rahasia123", "hubName": "devbox"}); st == 200 {
		t.Fatal("work's code opened the default hub")
	}
	if st, r := def.do("POST", "/api/setup", map[string]any{"code": defaultCode, "password": "rahasia123", "hubName": "devbox"}); st != 200 {
		t.Fatalf("default setup %d %v", st, r)
	}
	if st, _ := wc.do("POST", "/api/setup", map[string]any{"code": defaultCode, "password": "pribadi123", "hubName": "devbox · work"}); st == 200 {
		t.Fatal("the default's code opened work")
	}
	if st, r := wc.do("POST", "/api/setup", map[string]any{"code": workCode, "password": "pribadi123", "hubName": "devbox · work"}); st != 200 {
		t.Fatalf("work setup %d %v", st, r)
	}

	// listed: instances --json, status --all, status
	var list struct {
		Items []struct {
			Name, State, Version, Listen, DataDir string
			Running                               bool
			Service                               *instance.Service
			URLs                                  []string
		}
	}
	if err := json.Unmarshal([]byte(b.mustNB("instances", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.Items[0].Name != "default" || list.Items[1].Name != "work" {
		t.Fatalf("instances %+v", list.Items)
	}
	for _, it := range list.Items {
		if it.State != "active" || !it.Running || it.Version != "0.1.0-dev" || it.Service == nil || len(it.URLs) == 0 {
			t.Errorf("instance %+v", it)
		}
	}
	if it := list.Items[1]; it.Listen != workListen || it.DataDir != work || it.Service.Name != "nextboard-work.service" || it.Service.Bin != bin {
		t.Errorf("work %+v %+v", it, it.Service)
	}
	table := b.mustNB("instances")
	if !strings.Contains(table, "NAME") || !strings.Contains(table, "nextboard-work.service") || strings.Contains(table, "problem") {
		t.Errorf("instances:\n%s", table)
	}
	all := b.mustNB("status", "--all")
	for _, want := range []string{"Instance:    default", "Instance:    work", "Service:     active (nextboard-work.service)", "Hub:         devbox · work", "Data:        " + work} {
		if !strings.Contains(all, want) {
			t.Errorf("status --all lacks %q:\n%s", want, all)
		}
	}
	if out := b.mustNB("status"); !strings.Contains(out, "Instance:    default") || !strings.Contains(out, "Other instances: work (active, "+workListen+")") {
		t.Errorf("status:\n%s", out)
	}
	if out := b.mustNB("status", "--instance", "work"); !strings.Contains(out, "Listen:      "+workListen) || !strings.Contains(out, "Health:      OK") {
		t.Errorf("status --instance work:\n%s", out)
	}
	b.mustNB("logs", "--instance", "work")
	if !strings.Contains(b.callLog(), "journalctl --user -u nextboard-work -n 200 --no-pager\n") {
		t.Errorf("logs:\n%s", b.callLog())
	}
	if out := b.mustNB("version", "--instance", "work"); !strings.Contains(out, "Instance work ("+workListen+") runs 0.1.0-dev") {
		t.Errorf("version:\n%s", out)
	}
	// a name with no instance is not made by asking about it
	if out, err := b.nb("setup-link", "--instance", "typo"); err == nil || !strings.Contains(out, "no instance typo") {
		t.Errorf("setup-link of nothing: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(b.home, ".nextboard-typo")); err == nil {
		t.Error("asking about typo made its folder")
	}
	// a second install of that name, or one on work's port, is refused
	if out, err := b.nb("install", "--instance", "side", "--listen", workListen); err == nil || !strings.Contains(out, "instance work's") {
		t.Errorf("install on work's port: %v %s", err, out)
	}

	// removed one at a time: the service only, then the data, never the default's
	out = b.mustNB("uninstall", "--instance", "work")
	if !strings.Contains(b.callLog(), "systemctl --user disable --now nextboard-work\n") || !strings.Contains(out, "Data kept in "+work) {
		t.Fatalf("uninstall: %s\n%s", out, b.callLog())
	}
	if _, err := os.Stat(filepath.Join(units, "nextboard-work.service")); err == nil {
		t.Fatal("work's unit is still there")
	}
	if strings.Contains(b.callLog(), "disable --now nextboard\n") {
		t.Fatal("the default service was stopped")
	}
	lock, err := instance.AcquireLock(work) // a hub run by hand on work's folder
	if err != nil {
		t.Fatal(err)
	}
	if out, err := b.nb("uninstall", "--instance", "work", "--purge"); err == nil || !strings.Contains(out, "still runs on "+work) {
		t.Fatalf("purge while running: %v %s", err, out)
	}
	if _, err := os.Stat(work); err != nil {
		t.Fatal("purged while its hub runs")
	}
	lock.Release()
	if out := b.mustNB("uninstall", "--instance", "work", "--purge"); !strings.Contains(out, "Data deleted: "+work) {
		t.Fatalf("purge: %s", out)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("work's folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.home, ".nextboard", "config.json")); err != nil {
		t.Fatal("the default's data went too")
	}
	if st, _ := def.do("GET", "/api/auth/me", nil); st != 200 {
		t.Fatalf("the default hub after work went: %d", st)
	}
}

// `serve --instance` on a folder that is not there makes the instance first, with a block of
// its own, and `setup-link --instance` then opens it.
func TestServeInstanceFirstStart(t *testing.T) {
	b := newBox(t)
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	logFile := filepath.Join(t.TempDir(), "hub.log")
	f, err := os.Create(logFile)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(nextboardBinary(t), "serve", "--instance", "personal", "--listen", listen)
	cmd.Env, cmd.Stdout, cmd.Stderr = b.env(), f, f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &hubProc{cmd: cmd, log: logFile}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		f.Close()
	})
	waitHealthyHub(t, listen)
	dir := filepath.Join(b.home, ".nextboard-personal")
	var cfg struct{ Instance, HubID, Listen string }
	readJSON(t, filepath.Join(dir, "config.json"), &cfg)
	if cfg.Instance != "personal" || !instance.ValidHubID(cfg.HubID) || cfg.Listen != listen {
		t.Fatalf("config %+v", cfg)
	}
	var sets struct{ Previews struct{ PortRange [2]int } }
	readJSON(t, filepath.Join(dir, "settings.json"), &sets)
	if r := sets.Previews.PortRange; r[0] < 12420 || r[0]%100 != 20 {
		t.Fatalf("previews %v", r)
	}
	if _, err := os.Stat(filepath.Join(b.home, ".config", "systemd", "user", "nextboard-personal.service")); err == nil {
		t.Fatal("serve wrote a unit")
	}
	raw, _ := os.ReadFile(logFile)
	if !strings.Contains(string(raw), "new instance personal: data in "+dir) || !strings.Contains(string(raw), "nextboard setup-link --instance personal") {
		t.Errorf("log:\n%s", raw)
	}
	out := b.mustNB("setup-link", "--instance", "personal")
	m := setupCodeRe.FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "http://"+listen+"/setup?code=") {
		t.Fatalf("setup-link:\n%s", out)
	}
	if st, r := newClient(t, listen).do("POST", "/api/setup", map[string]any{"code": m[1], "password": "pribadi123", "hubName": "devbox · personal"}); st != 200 {
		t.Fatalf("setup %d %v", st, r)
	}
	// it is the hub on the folder: a second one is refused
	if out, err := b.nb("serve", "--instance", "personal", "--listen", fmt.Sprintf("127.0.0.1:%d", freePort(t))); err == nil || !strings.Contains(out, "already running") {
		t.Errorf("second hub: %v %s", err, out)
	}
	p.stop(t)
}

// install.sh --instance=work installs work's service, and restarts the default instance, whose
// service runs the binary it replaced and whose hub still runs the build before; a service of
// another binary stays as it is.
func TestInstallScriptInstance(t *testing.T) {
	b := newBox(t)
	b.publish("9.9.9")
	installed := filepath.Join(b.home, ".local", "bin", "nextboard")
	units := filepath.Join(b.home, ".config", "systemd", "user")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, exec := range map[string]string{
		"nextboard.service":       installed + " serve --data-dir " + filepath.Join(b.home, ".nextboard"),
		"nextboard-other.service": "/opt/other/nextboard serve --instance other",
	} {
		if err := os.WriteFile(filepath.Join(units, file), []byte("[Service]\nExecStart="+exec+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// the default's hub, an older build
	ln, err := net.Listen("tcp", b.listen)
	if err != nil {
		t.Fatal(err)
	}
	old := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"version":"0.8.1","commit":"1a2b3c4"}`))
	})}
	go old.Serve(ln)
	defer old.Close()
	// work's folder, from before instances had names: its port is its own
	workListen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	work := filepath.Join(b.home, ".nextboard-work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "config.json"), []byte(`{"listen":"`+workListen+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := b.runInstaller("--instance=work")
	if err != nil {
		t.Fatalf("install.sh: %v\n%s\ncalls:\n%s", err, out, b.callLog())
	}
	unit, err := os.ReadFile(filepath.Join(units, "nextboard-work.service"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(unit), "ExecStart="+installed+" serve --instance work --data-dir "+work+"\n") {
		t.Fatalf("unit:\n%s", unit)
	}
	waitHealthyHub(t, workListen)
	if !strings.Contains(out, "Restarting the other instances on "+installed+", so they run 0.1.0-dev too: default ("+b.listen+", was 0.8.1)") {
		t.Errorf("install.sh said:\n%s", out)
	}
	calls := b.callLog()
	for _, want := range []string{"systemctl --user restart nextboard-work\n", "systemctl --user restart nextboard\n"} {
		if !strings.Contains(calls, want) {
			t.Errorf("never called %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "nextboard-other") {
		t.Errorf("another binary's service was touched:\n%s", calls)
	}
	if m := setupCodeRe.FindStringSubmatch(out); m == nil {
		t.Errorf("no setup link:\n%s", out)
	} else if st, r := newClient(t, workListen).do("POST", "/api/setup", map[string]any{"code": m[1], "password": "pribadi123", "hubName": "devbox · work"}); st != 200 {
		t.Errorf("work setup %d %v", st, r)
	}
	var cfg struct{ Instance string }
	readJSON(t, filepath.Join(work, "config.json"), &cfg)
	if cfg.Instance != "work" {
		t.Errorf("work's folder records %q", cfg.Instance)
	}
}
