package nodedap

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// running starts a node program the adapter knows nothing about, and returns its inspector's port.
func running(t *testing.T, source string, flags ...string) (*exec.Cmd, string, int) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "server.js")
	if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", append(append([]string{"--inspect=127.0.0.1:0"}, flags...), file)...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	re := regexp.MustCompile(`ws://127\.0\.0\.1:(\d+)/`)
	port := make(chan int, 1)
	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			if m := re.FindStringSubmatch(s.Text()); m != nil {
				n, _ := strconv.Atoi(m[1])
				port <- n
			}
		}
	}()
	select {
	case p := <-port:
		return cmd, file, p
	case <-time.After(10 * time.Second):
		t.Fatal("node did not print its inspector")
		return nil, "", 0
	}
}

func (c *client) attach(args map[string]any, breaks map[string][]map[string]any) {
	c.t.Helper()
	c.req("initialize", map[string]any{"adapterID": "node"})
	c.req("attach", args)
	c.wait("initialized")
	for path, list := range breaks {
		c.req("setBreakpoints", map[string]any{"source": map[string]any{"path": path}, "breakpoints": list})
	}
	c.req("configurationDone", nil)
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

const ticking = `let count = 0;
console.log('printed before anyone attached');
setInterval(() => {
  count = count + 1;
  console.log('tick', count);
}, 50);
`

func TestAttachStopsAtABreakpointAndLeavesTheProcessRunning(t *testing.T) {
	cmd, file, port := running(t, ticking)
	c := start(t)
	c.attach(map[string]any{"port": port, "cwd": filepath.Dir(file)}, map[string][]map[string]any{file: {{"line": 5}}})

	if st := c.wait("stopped"); st["reason"] != "breakpoint" {
		t.Fatalf("stopped: %v", st)
	}
	frame := c.topFrame()
	if int(frame["line"].(float64)) != 5 {
		t.Fatalf("frame: %v", frame)
	}
	if v := c.req("evaluate", map[string]any{"expression": "count", "frameId": frame["id"]}); v["result"] == "" {
		t.Fatalf("evaluate: %v", v)
	}
	// Leaving while paused: the program has to go on, not wait for a debugger that left.
	c.req("disconnect", map[string]any{})
	time.Sleep(300 * time.Millisecond)
	if !alive(cmd.Process.Pid) {
		t.Fatal("stopping the session killed the process")
	}
	// A second session finds the program moving, and without the old breakpoint.
	c2 := start(t)
	c2.attach(map[string]any{"port": port}, nil)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(c2.outputText(), "tick") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	out := c2.outputText()
	if !strings.Contains(out, "tick") {
		t.Fatalf("the process does not run on, or console output does not arrive: %q", out)
	}
	if strings.Contains(out, "printed before anyone attached") {
		t.Fatalf("old output replayed: %q", out)
	}
	select {
	case e := <-c2.events:
		if e["event"] == "stopped" {
			t.Fatalf("the old breakpoint stayed: %v", e)
		}
	default:
	}
	c2.req("disconnect", map[string]any{})
}

func TestAttachTerminateDoesNotKill(t *testing.T) {
	cmd, _, port := running(t, ticking)
	c := start(t)
	c.attach(map[string]any{"port": port}, nil)
	c.req("terminate", map[string]any{})
	c.wait("terminated")
	time.Sleep(200 * time.Millisecond)
	if !alive(cmd.Process.Pid) {
		t.Fatal("terminate killed a process the adapter did not start")
	}
}

func TestAttachSeesTheProcessEnd(t *testing.T) {
	cmd, _, port := running(t, ticking)
	c := start(t)
	c.attach(map[string]any{"port": port}, nil)
	_ = cmd.Process.Signal(syscall.SIGTERM)
	c.wait("terminated")
}

// An inspector opened with a signal for this session closes with it. Left open, every login on
// the machine could run code in the process.
func TestAttachClosesTheInspectorItOpened(t *testing.T) {
	cmd, _, port := running(t, ticking)
	c := start(t)
	c.attach(map[string]any{"port": port, "closeInspector": true}, nil)
	c.req("disconnect", map[string]any{})
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("the inspector stayed open")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("closing the inspector killed the process")
	}
}

// The session cut off, as when the browser goes away: same promise.
func TestAttachClosesTheInspectorWhenTheInputEnds(t *testing.T) {
	cmd, _, port := running(t, ticking)
	c := start(t)
	c.attach(map[string]any{"port": port, "closeInspector": true}, nil)
	c.in.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("the inspector stayed open")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("the process died with the session")
	}
}

func TestAttachOverAUnixSocket(t *testing.T) {
	_, file, port := running(t, ticking)
	dir := t.TempDir()
	sock := filepath.Join(dir, "i")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				conn.Close()
				continue
			}
			go func() { _, _ = io.Copy(up, conn); up.Close() }()
			go func() { _, _ = io.Copy(conn, up); conn.Close() }()
		}
	}()
	t.Setenv(InspectSocketEnv, sock)
	c := start(t)
	// the socket wins over anything a launch.json says
	c.attach(map[string]any{"port": 1}, map[string][]map[string]any{file: {{"line": 5}}})
	if st := c.wait("stopped"); st["reason"] != "breakpoint" {
		t.Fatalf("stopped: %v", st)
	}
	c.req("disconnect", map[string]any{})
}

func TestAttachProblems(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	// A browser's debugging port answers the same request. Not ours to attach to.
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Browser":"Chrome/150","Protocol-Version":"1.3"}`)
	})}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()
	chromePort := l.Addr().(*net.TCPAddr).Port

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()

	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"no port":     {map[string]any{}, "needs the inspector's port"},
		"bad port":    {map[string]any{"port": 70000}, "needs the inspector's port"},
		"other host":  {map[string]any{"port": 9229, "address": "192.0.2.1"}, "loopback"},
		"named host":  {map[string]any{"port": 9229, "address": "example.com"}, "loopback"},
		"not node":    {map[string]any{"port": chromePort}, "not a Node.js inspector"},
		"nobody home": {map[string]any{"port": closedPort}, "no inspector answers"},
	} {
		t.Run(name, func(t *testing.T) {
			c := start(t)
			c.req("initialize", map[string]any{"adapterID": "node"})
			r := c.try("attach", tc.args)
			if r["success"] == true || !strings.Contains(fmt.Sprint(r["message"]), tc.want) {
				t.Fatalf("got %v", r)
			}
		})
	}
}
