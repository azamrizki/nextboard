package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/config"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/gorilla/websocket"
)

// These tests run the real binary, the way a user does (docs/SPEC.md §12). They
// never reach this machine's service manager or its real home folder: HOME is a
// scratch folder, and systemctl and loginctl are fakes first on PATH.

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if builtBin != "" {
		_ = os.RemoveAll(filepath.Dir(builtBin))
	}
	os.Exit(code)
}

func nextboardBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	if runtime.GOOS != "linux" {
		t.Skip("uses systemd's layout")
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nb-bin-")
		if err != nil {
			buildErr = err
			return
		}
		builtBin = filepath.Join(dir, "nextboard")
		out, err := exec.Command("go", "build", "-o", builtBin, "../../cmd/nextboard").CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return builtBin
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// box is a scratch user: a home folder, and a PATH with fakes in front.
type box struct {
	t        *testing.T
	home     string
	bin      string
	calls    string // what the fakes were called with
	pidFile  string
	release  string
	listen   string
	stdoutLn string
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newBox(t *testing.T) *box {
	t.Helper()
	b := &box{t: t, home: t.TempDir(), bin: t.TempDir(), release: t.TempDir()}
	b.calls = filepath.Join(b.bin, "calls.log")
	b.pidFile = filepath.Join(b.bin, "hub.pid")
	b.listen = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	// `restart` runs the unit's own ExecStart, so a broken unit fails the test.
	// timeout: a hub the test failed to stop cannot outlive it for long.
	writeExec(t, filepath.Join(b.bin, "systemctl"), `echo "systemctl $*" >> "`+b.calls+`"
case "$*" in
*restart*)
  unit="$HOME/.config/systemd/user/nextboard.service"
  start=$(sed -n 's/^ExecStart=//p' "$unit")
  nohup timeout 120 $start >"`+b.bin+`/hub.log" 2>&1 &
  echo $! > "`+b.pidFile+`"
  ;;
esac
`)
	writeExec(t, filepath.Join(b.bin, "loginctl"), `echo "loginctl $*" >> "`+b.calls+`"`+"\n")
	// curl answers from a folder standing in for the GitHub release.
	writeExec(t, filepath.Join(b.bin, "curl"), `out=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
echo "curl $url" >> "`+b.calls+`"
case "$url" in
*/releases/latest) echo '{"tag_name": "v9.9.9"}' ;;
*) cp "`+b.release+`/$(basename "$url")" "$out" ;;
esac
`)
	t.Cleanup(b.stopHub)
	// A listen address of its own, whatever else runs on this machine.
	st, err := store.Open(filepath.Join(b.home, ".nextboard"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(st)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = b.listen
	if err := cfg.Save(st); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *box) env() []string {
	return []string{"HOME=" + b.home, "PATH=" + b.bin + ":/usr/bin:/bin", "SHELL=/bin/sh", "USER=" + os.Getenv("USER"), "LANG=C.UTF-8", "TMPDIR=" + os.TempDir()}
}

func (b *box) stopHub() {
	raw, err := os.ReadFile(b.pidFile)
	if err != nil {
		return
	}
	var pid int
	if _, err := fmt.Sscan(string(raw), &pid); err == nil && pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		for i := 0; i < 100 && syscall.Kill(pid, 0) == nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (b *box) callLog() string {
	raw, _ := os.ReadFile(b.calls)
	return string(raw)
}

// publish puts a release for this OS into the folder curl answers from.
func (b *box) publish(version string) string {
	b.t.Helper()
	name := fmt.Sprintf("nextboard_%s_%s_%s.tar.gz", version, runtime.GOOS, map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH])
	stage := b.t.TempDir()
	raw, err := os.ReadFile(nextboardBinary(b.t))
	if err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "nextboard"), raw, 0o755); err != nil {
		b.t.Fatal(err)
	}
	if out, err := exec.Command("tar", "-czf", filepath.Join(b.release, name), "-C", stage, "nextboard").CombinedOutput(); err != nil {
		b.t.Fatalf("tar: %v %s", err, out)
	}
	sum, err := os.ReadFile(filepath.Join(b.release, name))
	if err != nil {
		b.t.Fatal(err)
	}
	digest := sha256.Sum256(sum)
	line := hex.EncodeToString(digest[:]) + "  " + name + "\n"
	if err := os.WriteFile(filepath.Join(b.release, "checksums.txt"), []byte(line), 0o644); err != nil {
		b.t.Fatal(err)
	}
	return name
}

func (b *box) runInstaller(args ...string) (string, error) {
	cmd := exec.Command("/bin/sh", append([]string{"../../install.sh"}, args...)...)
	cmd.Env = b.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

var setupCodeRe = regexp.MustCompile(`/setup\?code=([A-Z0-9-]+)`)

// client talks to a hub the way the web UI does.
type client struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newClient(t *testing.T, listen string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: "http://" + listen, c: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("X-NextBoard", "1")
	resp, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
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

func waitHealthyHub(t *testing.T, listen string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + listen + "/api/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no hub answers on %s", listen)
}

// SPEC F1.1 and the part of F1.3 a test can see: install.sh fetches the release,
// checks it, installs the binary and the service, asks for linger, and prints a
// setup link that works, once.
func TestInstallScriptFromReleaseToSetup(t *testing.T) {
	b := newBox(t)
	b.publish("9.9.9")
	out, err := b.runInstaller()
	if err != nil {
		t.Fatalf("install.sh: %v\n%s\ncalls:\n%s", err, out, b.callLog())
	}
	installed := filepath.Join(b.home, ".local", "bin", "nextboard")
	if fi, err := os.Stat(installed); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("binary: %v %v", fi, err)
	}
	if !strings.Contains(out, "Checksum OK") {
		t.Errorf("no checksum line:\n%s", out)
	}
	unit, err := os.ReadFile(filepath.Join(b.home, ".config", "systemd", "user", "nextboard.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ExecStart=" + installed + " serve --data-dir " + filepath.Join(b.home, ".nextboard"), "Environment=HOME=" + b.home, "WantedBy=default.target", "Restart=on-failure"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	calls := b.callLog()
	for _, want := range []string{"/releases/latest", "systemctl --user daemon-reload", "systemctl --user enable nextboard", "systemctl --user restart nextboard", "loginctl enable-linger"} {
		if !strings.Contains(calls, want) {
			t.Errorf("never called %q:\n%s", want, calls)
		}
	}
	waitHealthyHub(t, b.listen)

	m := setupCodeRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no setup link printed:\n%s", out)
	}
	c := newClient(t, b.listen)
	if st, r := c.do("POST", "/api/setup", map[string]any{"code": m[1], "password": "rahasia123", "hubName": "devbox"}); st != 200 {
		t.Fatalf("the printed link does not work: %d %v", st, r)
	}
	if st, _ := c.do("GET", "/api/auth/me", nil); st != 200 {
		t.Errorf("not logged in after setup: %d", st)
	}
	again := newClient(t, b.listen)
	if st, _ := again.do("POST", "/api/setup", map[string]any{"code": m[1], "password": "another pass", "hubName": "x"}); st == 200 {
		t.Error("the link worked twice")
	}
	if st, _ := again.do("GET", "/api/auth/me", nil); st != 401 {
		t.Errorf("anonymous after setup: %d", st)
	}

	// And it comes off again.
	b.stopHub()
	cmd := exec.Command(installed, "uninstall")
	cmd.Env = b.env()
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uninstall: %v %s", err, raw)
	}
	if _, err := os.Stat(filepath.Join(b.home, ".config", "systemd", "user", "nextboard.service")); err == nil {
		t.Error("the unit is still there")
	}
	if !strings.Contains(b.callLog(), "systemctl --user disable --now nextboard") {
		t.Errorf("service not stopped:\n%s", b.callLog())
	}
	if _, err := os.Stat(filepath.Join(b.home, ".nextboard")); err != nil {
		t.Error("uninstall without --purge removed the data")
	}
}

func TestInstallScriptRefusesABadChecksum(t *testing.T) {
	b := newBox(t)
	name := b.publish("9.9.9")
	bad := strings.Repeat("0", 64) + "  " + name + "\n"
	if err := os.WriteFile(filepath.Join(b.release, "checksums.txt"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := b.runInstaller()
	if err == nil {
		t.Fatalf("installed a release with the wrong checksum:\n%s", out)
	}
	if !strings.Contains(out, "checksum") {
		t.Errorf("does not say why:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(b.home, ".local", "bin", "nextboard")); err == nil {
		t.Error("the binary was installed anyway")
	}
	if strings.Contains(b.callLog(), "systemctl") {
		t.Errorf("the service was touched:\n%s", b.callLog())
	}
}

func TestInstallScriptWithAPinnedVersionAndNoService(t *testing.T) {
	b := newBox(t)
	b.publish("1.2.3")
	out, err := b.runInstaller("--version=v1.2.3", "--no-service")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(b.callLog(), "/releases/latest") || strings.Contains(b.callLog(), "systemctl") {
		t.Errorf("calls:\n%s", b.callLog())
	}
	if !strings.Contains(out, "PATH") {
		t.Errorf("no word about PATH:\n%s", out)
	}
}

// hubProc is one run of `nextboard serve`.
type hubProc struct {
	cmd *exec.Cmd
	log string
}

func startHub(t *testing.T, b *box, data string) *hubProc {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "hub.log")
	f, err := os.Create(logFile)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(nextboardBinary(t), "serve", "--listen", b.listen, "--data-dir", data)
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
	waitHealthyHub(t, b.listen)
	return p
}

// stop sends what systemctl sends and waits for a clean exit.
func (p *hubProc) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			raw, _ := os.ReadFile(p.log)
			t.Fatalf("exit after SIGTERM: %v\n%s", err, raw)
		}
	case <-time.After(30 * time.Second):
		_ = p.cmd.Process.Kill()
		t.Fatal("the hub did not stop within 30 s of SIGTERM")
	}
}

func (c *client) snapshot(id string) (string, *websocket.Conn) {
	c.t.Helper()
	u := "ws" + strings.TrimPrefix(c.base, "http") + "/api/sessions/" + id + "/ws"
	hdr := http.Header{"Origin": {c.base}}
	cu, err := http.NewRequest("GET", c.base, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	for _, ck := range c.c.Jar.Cookies(cu.URL) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	ws, _, err := websocket.DefaultDialer.Dial(u, hdr)
	if err != nil {
		c.t.Fatal(err)
	}
	_, first, err := ws.ReadMessage()
	if err != nil {
		c.t.Fatal(err)
	}
	return string(first), ws
}

func termNode(t *testing.T, c *client, wid, sid string) map[string]any {
	t.Helper()
	st, board := c.do("GET", "/api/workspaces/"+wid+"/board", nil)
	if st != 200 {
		t.Fatalf("board %d %v", st, board)
	}
	nodes, _ := board["nodes"].([]any)
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		data, _ := node["data"].(map[string]any)
		if data["sessionId"] == sid {
			return node
		}
	}
	t.Fatalf("no tile for %s in %v", sid, board)
	return nil
}

// SPEC F1.6, with the real process and the real signal: the login, the tile's
// place and the terminal's past all come back, around a new shell.
func TestHubProcessComesBackFromSIGTERM(t *testing.T) {
	b := newBox(t)
	data, work := t.TempDir(), t.TempDir()
	hub := startHub(t, b, data)
	// Without a terminal, as under systemd, the code stays out of the log: it opens the hub.
	raw, _ := os.ReadFile(hub.log)
	if setupCodeRe.Match(raw) {
		t.Errorf("a setup code in the log:\n%s", raw)
	}
	link := exec.Command(nextboardBinary(t), "setup-link", "--data-dir", data)
	link.Env = b.env()
	out, err := link.CombinedOutput()
	if err != nil {
		t.Fatalf("setup-link: %v %s", err, out)
	}
	m := setupCodeRe.FindSubmatch(out)
	if m == nil {
		t.Fatalf("no link printed:\n%s", out)
	}
	c := newClient(t, b.listen)
	if st, r := c.do("POST", "/api/setup", map[string]any{"code": string(m[1]), "password": "rahasia123", "hubName": "devbox"}); st != 200 {
		t.Fatalf("setup %d %v", st, r)
	}
	st, pr := c.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": work, "name": "app"})
	if st != 201 {
		t.Fatalf("project %d %v", st, pr)
	}
	workspaces, _ := pr["workspaces"].([]any)
	if len(workspaces) == 0 {
		t.Fatalf("project %v", pr)
	}
	wid := workspaces[0].(map[string]any)["id"].(string)
	st, sess := c.do("POST", "/api/sessions", map[string]any{"machineId": "local", "preset": "shell", "cols": 100, "rows": 30, "workspaceId": wid, "cwd": work})
	if st != 201 {
		t.Fatalf("console %d %v", st, sess)
	}
	id := sess["id"].(string)
	pidBefore := sess["pid"]

	node := termNode(t, c, wid, id)
	node["x"], node["y"] = 1234.0, 567.0
	if st, r := c.do("POST", "/api/workspaces/"+wid+"/board/ops", map[string]any{"clientId": "c_tab1", "baseRev": 0, "ops": []any{map[string]any{"op": "put", "node": node}}}); st != 200 {
		t.Fatalf("move %d %v", st, r)
	}

	_, ws := c.snapshot(id)
	if err := ws.WriteJSON(map[string]any{"t": "i", "d": "echo nb-before-$((40+2))\r"}); err != nil {
		t.Fatal(err)
	}
	var seen bytes.Buffer
	_ = ws.SetReadDeadline(time.Now().Add(20 * time.Second))
	for !strings.Contains(seen.String(), "nb-before-42") {
		_, frame, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("%v, saw %q", err, seen.String())
		}
		seen.Write(frame)
	}
	ws.Close()

	hub.stop(t)
	if resp, err := http.Get(c.base + "/api/health"); err == nil {
		resp.Body.Close()
		t.Fatal("something still answers after the hub stopped")
	}
	startHub(t, b, data)

	if st, _ := c.do("GET", "/api/auth/me", nil); st != 200 {
		t.Errorf("the login did not come back: %d", st)
	}
	waitForStatus := func() map[string]any {
		deadline := time.Now().Add(30 * time.Second)
		for {
			st, in := c.do("GET", "/api/sessions/"+id, nil)
			if st == 200 && in["status"] == "running" {
				return in
			}
			if time.Now().After(deadline) {
				t.Fatalf("the console did not come back: %d %v", st, in)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	in := waitForStatus()
	if pidBefore != nil && in["pid"] == pidBefore {
		t.Errorf("same pid %v: a shell cannot have survived its hub", in["pid"])
	}
	moved := termNode(t, c, wid, id)
	if moved["x"] != 1234.0 || moved["y"] != 567.0 {
		t.Errorf("the tile moved: %v, %v", moved["x"], moved["y"])
	}
	snap, ws2 := c.snapshot(id)
	defer ws2.Close()
	if !strings.Contains(snap, "nb-before-42") {
		t.Errorf("the terminal's past is gone: %q", snap)
	}
	if err := ws2.WriteJSON(map[string]any{"t": "i", "d": "echo nb-after-$((1+1))\r"}); err != nil {
		t.Fatal(err)
	}
	seen.Reset()
	_ = ws2.SetReadDeadline(time.Now().Add(20 * time.Second))
	for !strings.Contains(seen.String(), "nb-after-2") {
		_, frame, err := ws2.ReadMessage()
		if err != nil {
			t.Fatalf("the new shell does not answer: %v, saw %q", err, seen.String())
		}
		seen.Write(frame)
	}
}
