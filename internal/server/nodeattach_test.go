package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/nodedap"
	"github.com/azamrizki/nextboard/internal/studio"
)

// The test binary stands in for `nextboard dap-node`, which is all the hub runs.
func init() {
	if os.Getenv("NEXTBOARD_TEST_DAP_NODE") == "1" {
		_ = nodedap.Serve(os.Stdin, os.Stdout)
		os.Exit(0)
	}
}

func useTestAdapter(t *testing.T) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old := studio.NodeAdapterArgv
	studio.NodeAdapterArgv = func() []string { return []string{"env", "NEXTBOARD_TEST_DAP_NODE=1", self} }
	t.Cleanup(func() { studio.NodeAdapterArgv = old })
}

// nodeProgram runs a program with no inspector open. --inspect-port=0 makes the signal choose a
// free port: the machine this runs on may be shared, and 9229 taken.
func nodeProgram(t *testing.T, dir string) (*exec.Cmd, string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	file := filepath.Join(dir, "server.js")
	src := "let n = 0;\nsetInterval(() => {\n  n = n + 1;\n}, 50);\n"
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", "--inspect-port=127.0.0.1:0", file)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd, file
}

// dapSession plays the editor over the hub's WebSocket.
type dapSession struct {
	t   *testing.T
	ws  *websocket.Conn
	seq int
	in  chan map[string]any
}

func openDAP(t *testing.T, h *testHub, path string) *dapSession {
	t.Helper()
	hdr := http.Header{"Origin": {h.ts.URL}}
	u, _ := url.Parse(h.ts.URL)
	for _, c := range h.c.Jar.Cookies(u) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	ws, _, err := h.dialWS(path, hdr)
	if err != nil {
		t.Fatal(err)
	}
	s := &dapSession{t: t, ws: ws, in: make(chan map[string]any, 200)}
	go func() {
		defer close(s.in)
		for {
			_, b, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				s.in <- m
			}
		}
	}()
	t.Cleanup(func() { ws.Close() })
	return s
}

func (s *dapSession) send(cmd string, args any) int {
	s.t.Helper()
	s.seq++
	b, _ := json.Marshal(map[string]any{"seq": s.seq, "type": "request", "command": cmd, "arguments": args})
	if err := s.ws.WriteMessage(websocket.TextMessage, b); err != nil {
		s.t.Fatal(err)
	}
	return s.seq
}

func (s *dapSession) until(pred func(map[string]any) bool, what string) map[string]any {
	s.t.Helper()
	deadline := time.After(25 * time.Second)
	for {
		select {
		case m, ok := <-s.in:
			if !ok {
				s.t.Fatalf("session ended waiting for %s", what)
			}
			if pred(m) {
				return m
			}
		case <-deadline:
			s.t.Fatalf("no %s", what)
		}
	}
}

func (s *dapSession) req(cmd string, args any) map[string]any {
	s.t.Helper()
	seq := s.send(cmd, args)
	r := s.until(func(m map[string]any) bool { return m["type"] == "response" && int(m["request_seq"].(float64)) == seq }, cmd)
	if r["success"] != true {
		s.t.Fatalf("%s failed: %v", cmd, r["message"])
	}
	return r
}

func (s *dapSession) event(name string) map[string]any {
	s.t.Helper()
	return s.until(func(m map[string]any) bool { return m["event"] == name }, name)
}

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// attachFlow is the whole path of the panel: pick a process, open its inspector, debug, leave.
func attachFlow(t *testing.T, h *testHub, pid string, proc *exec.Cmd, file string) {
	t.Helper()
	st, list := h.do("GET", "/api/projects/"+pid+"/dap/node/processes", nil, nil)
	if st != 200 {
		t.Fatalf("processes: %d %v", st, list)
	}
	var found bool
	for _, x := range list["items"].([]any) {
		m := x.(map[string]any)
		if int(m["pid"].(float64)) == proc.Process.Pid {
			found = true
			if !strings.Contains(m["command"].(string), "server.js") || m["cwd"] != filepath.Dir(file) {
				t.Fatalf("row: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("process %d not listed: %v", proc.Process.Pid, list)
	}
	st, in := h.do("POST", "/api/projects/"+pid+"/dap/node/inspect", map[string]any{"pid": proc.Process.Pid}, nil)
	if st != 200 || in["opened"] != true {
		t.Fatalf("inspect: %d %v", st, in)
	}
	port := int(in["port"].(float64))
	s := openDAP(t, h, "/api/projects/"+pid+"/dap/node/ws?inspect="+strconv.Itoa(port))
	s.req("initialize", map[string]any{"adapterID": "node"})
	s.req("attach", map[string]any{"closeInspector": true})
	s.event("initialized")
	s.req("setBreakpoints", map[string]any{"source": map[string]any{"path": file}, "breakpoints": []map[string]any{{"line": 3}}})
	s.req("configurationDone", nil)
	if b := s.event("stopped")["body"].(map[string]any); b["reason"] != "breakpoint" {
		t.Fatalf("stopped: %v", b)
	}
	trace := s.req("stackTrace", map[string]any{"threadId": 1})["body"].(map[string]any)["stackFrames"].([]any)[0].(map[string]any)
	if trace["source"].(map[string]any)["path"] != file {
		t.Fatalf("frame: %v", trace)
	}
	s.req("disconnect", map[string]any{})
	s.ws.Close()
	waitTrue(t, "the inspector to close", 10*time.Second, func() bool { return !portOpen(port) })
	if syscall.Kill(proc.Process.Pid, 0) != nil {
		t.Fatal("the session killed the process")
	}
	// No socket is left behind.
	waitTrue(t, "the socket to go", 15*time.Second, func() bool {
		left, _ := filepath.Glob(filepath.Join(os.TempDir(), "nb-dap-*", "i"))
		return len(left) == 0
	})
}

func TestNodeAttachOnThisPC(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	useTestAdapter(t)
	h := newTestHub(t)
	h.setup()
	pid, dir := filesProject(t, h)
	proc, file := nodeProgram(t, dir)
	waitInspectable(t, proc)
	attachFlow(t, h, pid, proc, file)
}

// The goal of the whole project: the debugger on This PC, the program on the server.
func TestNodeAttachOnAServer(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	useTestAdapter(t)
	port, key := startSSHServer(t)
	h := newTestHub(t)
	h.setup()
	mid := h.connectMachine(port, key)
	dir := t.TempDir()
	st, pr := h.do("POST", "/api/projects", map[string]any{"name": "srv", "machineId": mid, "path": dir}, nil)
	if st != 201 {
		t.Fatalf("project: %d %v", st, pr)
	}
	pid := pr["id"].(string)
	st, res := h.do("GET", "/api/projects/"+pid+"/dap", nil, nil)
	if st != 200 {
		t.Fatalf("dap: %d %v", st, res)
	}
	for _, x := range res["items"].([]any) {
		if m := x.(map[string]any); m["id"] == "node" && (m["available"] != false || m["attach"] != true) {
			t.Fatalf("on a server node can attach but not launch: %v", m)
		}
	}
	proc, file := nodeProgram(t, dir)
	waitInspectable(t, proc)
	attachFlow(t, h, pid, proc, file)
}

// node installs its SIGUSR1 handler while it starts; a signal before that would kill it. The
// real picker lists running programs, so only a test can be this early.
func waitInspectable(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	time.Sleep(700 * time.Millisecond)
	if syscall.Kill(cmd.Process.Pid, 0) != nil {
		t.Fatal("the program died")
	}
}

// SIGUSR1 kills a program that does not handle it. Whatever the request says, it only ever goes
// to node.
func TestNodeInspectNeverSignalsOtherPrograms(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	pid, _ := filesProject(t, h)
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	})
	st, res := h.do("POST", "/api/projects/"+pid+"/dap/node/inspect", map[string]any{"pid": sleeper.Process.Pid}, nil)
	if st != 409 || res["code"] != "not_node" {
		t.Fatalf("sleep: %d %v", st, res)
	}
	time.Sleep(200 * time.Millisecond)
	if syscall.Kill(sleeper.Process.Pid, 0) != nil {
		t.Fatal("the program was signalled")
	}
	var ws syscall.WaitStatus
	if got, _ := syscall.Wait4(sleeper.Process.Pid, &ws, syscall.WNOHANG, nil); got != 0 {
		t.Fatalf("the program ended: %v", ws)
	}
	for name, tc := range map[string]struct {
		body map[string]any
		st   int
		code string
	}{
		"gone":    {map[string]any{"pid": 1 << 30}, 404, "no_process"},
		"init":    {map[string]any{"pid": 1}, 404, "no_process"},
		"both":    {map[string]any{"pid": 5, "port": 5}, 400, "bad_request"},
		"neither": {map[string]any{}, 400, "bad_request"},
	} {
		t.Run(name, func(t *testing.T) {
			if st, res := h.do("POST", "/api/projects/"+pid+"/dap/node/inspect", tc.body, nil); st != tc.st || res["code"] != tc.code {
				t.Fatalf("%d %v", st, res)
			}
		})
	}
}

func TestNodeInspectRefusesOtherPorts(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	pid, _ := filesProject(t, h)
	browser := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Browser":"Chrome/150"}`)
	}))
	defer browser.Close()
	port := browser.Listener.Addr().(*net.TCPAddr).Port
	if st, res := h.do("POST", "/api/projects/"+pid+"/dap/node/inspect", map[string]any{"port": port}, nil); st != 409 || res["code"] != "not_inspector" {
		t.Fatalf("browser port: %d %v", st, res)
	}
	if st, res := h.do("POST", "/api/projects/"+pid+"/dap/node/inspect", map[string]any{"port": 70000}, nil); st != 409 || res["code"] != "not_inspector" {
		t.Fatalf("bad port: %d %v", st, res)
	}
	if _, resp, err := h.dialWS("/api/projects/"+pid+"/dap/node/ws?inspect=nope", authHeader(h)); err == nil || resp == nil || resp.StatusCode != 400 {
		t.Fatalf("bad inspect: %v", err)
	}
}

func authHeader(h *testHub) http.Header {
	hdr := http.Header{"Origin": {h.ts.URL}}
	u, _ := url.Parse(h.ts.URL)
	for _, c := range h.c.Jar.Cookies(u) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	return hdr
}
