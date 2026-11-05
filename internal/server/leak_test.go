package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"nextboard/internal/connlog"
)

// tcpGate is a network between the hub and a server that can be cut.
type tcpGate struct {
	t      *testing.T
	port   int
	target int
	mu     sync.Mutex
	ln     net.Listener
	conns  []net.Conn
}

func newTCPGate(t *testing.T, target int) *tcpGate {
	t.Helper()
	g := &tcpGate{t: t, target: target}
	g.up()
	t.Cleanup(g.down)
	return g
}

func (g *tcpGate) up() {
	g.t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", g.port))
	if err != nil {
		g.t.Fatal(err)
	}
	g.port = ln.Addr().(*net.TCPAddr).Port
	g.mu.Lock()
	g.ln = ln
	g.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", g.target))
			if err != nil {
				c.Close()
				continue
			}
			g.mu.Lock()
			g.conns = append(g.conns, c, u)
			g.mu.Unlock()
			go func() { _, _ = io.Copy(u, c); u.Close() }()
			go func() { _, _ = io.Copy(c, u); c.Close() }()
		}
	}()
}

// down cuts every connection and refuses new ones.
func (g *tcpGate) down() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ln != nil {
		g.ln.Close()
		g.ln = nil
	}
	for _, c := range g.conns {
		c.Close()
	}
	g.conns = nil
}

// settled waits for goroutines to end and returns their number and the heap.
func settled() (int, uint64) {
	var n int
	for i := 0; i < 40; i++ {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		m := runtime.NumGoroutine()
		if i > 3 && m >= n {
			n = m
			break
		}
		n = m
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return n, ms.HeapAlloc
}

// TestNoLeakFromClientsAndServerDrops: browsers that come and go, tool calls and
// a server that disappears and returns leave no goroutines or memory behind.
func TestNoLeakFromClientsAndServerDrops(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	srv := newSSHServer(t)
	gate := newTCPGate(t, srv.port)
	mid := h.connectMachine(gate.port, srv.key)
	_, wid := h.homeWorkspace()
	dir := t.TempDir()
	st, in := h.do("POST", "/api/sessions", map[string]any{"machineId": "local", "preset": "shell", "workspaceId": wid, "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("session %d %v", st, in)
	}
	id := in["id"].(string)
	hdr := http.Header{"Origin": {h.ts.URL}}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	host, ok := h.srv.machines.Host(mid)
	if !ok {
		t.Fatal("no host")
	}
	tool := func(name string, args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return runRemoteTool(ctx, host, dir, name, raw)
	}
	round := func() {
		t.Helper()
		// Browser tabs opening and closing, cleanly and not.
		for i := 0; i < 40; i++ {
			ws, _, err := h.dialWS("/api/sessions/"+id+"/ws", hdr)
			if err != nil {
				t.Fatal(err)
			}
			ev, _, err := h.dialWS("/api/events", hdr)
			if err != nil {
				t.Fatal(err)
			}
			_ = ws.WriteMessage(1, []byte(`{"t":"i","d":"\r"}`))
			if i%2 == 0 {
				_ = ws.UnderlyingConn().Close() // a tab that just dies
				_ = ev.UnderlyingConn().Close()
			} else {
				_ = ws.Close()
				_ = ev.Close()
			}
		}
		// Tool calls on the server.
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(strings.Repeat("x\n", 2000)), 0o644); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 15; i++ {
			if _, err := tool("read_file", map[string]any{"path": "f.txt"}); err != nil {
				t.Fatal(err)
			}
			if _, err := tool("bash", map[string]any{"command": "echo hi"}); err != nil {
				t.Fatal(err)
			}
		}
		// The network to the server drops and returns.
		gate.down()
		if _, err := tool("bash", map[string]any{"command": "echo hi"}); err == nil {
			t.Fatal("a tool call must fail while the server is away")
		}
		gate.up()
		waitTrue(t, "reconnected", 60*time.Second, func() bool {
			_, err := tool("bash", map[string]any{"command": "echo hi"})
			return err == nil
		})
	}
	round() // caches and connections settle
	g0, heap0 := settled()
	for i := 0; i < 6; i++ {
		round()
	}
	g1, heap1 := settled()
	t.Logf("goroutine %d -> %d, heap %.1f -> %.1f MB", g0, g1, float64(heap0)/1e6, float64(heap1)/1e6)
	if g1 > g0+2 {
		buf := make([]byte, 1<<18)
		t.Fatalf("goroutines grew from %d to %d:\n%s", g0, g1, buf[:runtime.Stack(buf, true)])
	}
	if heap1 > heap0+4<<20 {
		t.Fatalf("heap grew from %d to %d", heap0, heap1)
	}
}

// TestRemoteBashLeavesNoProcess: a command that times out is gone from the
// server, children included.
func TestRemoteBashLeavesNoProcess(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	srv := newSSHServer(t)
	mid := h.connectMachine(srv.port, srv.key)
	host, _ := h.srv.machines.Host(mid)
	marker := fmt.Sprintf("31%d", time.Now().UnixNano()%100000)
	raw, _ := json.Marshal(map[string]any{"command": "sleep " + marker + " & sleep " + marker, "timeout_ms": 1500})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	text, err := runRemoteTool(ctx, host, t.TempDir(), "bash", raw)
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("expected a timeout: %q %v", text, err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("the timeout took %s", d)
	}
	count := func() int {
		out, _ := exec.Command("pgrep", "-fc", "sleep "+marker).CombinedOutput()
		var n int
		fmt.Sscan(string(out), &n)
		return n
	}
	waitTrue(t, "processes gone", 8*time.Second, func() bool { return count() == 0 })
}

// TestConnectionLogThroughHub: tool calls and the hub's own life end up in the
// log and the API, and nothing of what went over the link does.
func TestConnectionLogThroughHub(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	srv := newSSHServer(t)
	gate := newTCPGate(t, srv.port)
	mid := h.connectMachine(gate.port, srv.key)
	dir := t.TempDir()
	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, rpcErr := h.srv.remoteRPC(ctx, remoteTarget{machineID: mid, cwd: dir}, rpcRequest{Method: "tools/call", Params: params})
		if rpcErr != nil {
			t.Fatalf("%s: %v", tool, rpcErr)
		}
		return res.(map[string]any)
	}
	const secret = "TOP-SECRET-VALUE"
	// A command that fails on its own is not a link problem.
	if r := call("bash", map[string]any{"command": "echo " + secret + "; exit 3"}); r["isError"] != true {
		t.Fatalf("expected a failing command: %v", r)
	}
	// Right after the cut the hub may not know yet; each way to fail must count.
	gate.down()
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"bash", map[string]any{"command": "echo " + secret}},
		{"read_file", map[string]any{"path": "/etc/hostname"}},
	} {
		if r := call(c.tool, c.args); r["isError"] != true {
			t.Fatalf("expected %s to fail while away: %v", c.tool, r)
		}
	}
	gate.up()
	waitTrue(t, "reconnected", 60*time.Second, func() bool { return call("bash", map[string]any{"command": "true"})["isError"] != true })

	var view struct {
		Items   []connlog.Event `json:"items"`
		Summary connlog.Summary `json:"summary"`
	}
	if st := h.getJSON("/api/logs/connections?machineId="+mid, &view); st != 200 {
		t.Fatalf("api %d", st)
	}
	failed := map[string]bool{}
	for _, e := range view.Items {
		if e.Kind == connlog.ToolFailed {
			failed[e.Tool] = true
			if e.Reason == "" || e.MachineID != mid {
				t.Fatalf("tool event %+v", e)
			}
		}
	}
	if !failed["bash"] || !failed["read_file"] {
		t.Fatalf("tool failures missing: %+v", view.Items)
	}
	if view.Summary.Disconnects != 1 || view.Summary.Connects < 2 || view.Summary.MaxDowntimeMs <= 0 {
		t.Fatalf("summary %+v", view.Summary)
	}
	raw, err := os.ReadFile(h.srv.st.Path("logs", "connections.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "echo") {
		t.Fatalf("what went over the link must stay out of the log:\n%s", raw)
	}

	// A run that never stopped shows at the next start; a clean stop does not.
	h.srv.noteHubStart()
	h.srv.noteHubStop()
	h.srv.noteHubStart()
	h.srv.noteHubStop()
	unclean := func() int {
		n := 0
		for _, e := range h.srv.connLog.Read(connlog.Query{}) {
			if e.Kind == connlog.HubUnclean {
				n++
			}
		}
		return n
	}
	if unclean() != 0 {
		t.Fatal("a clean stop counted as a crash")
	}
	h.srv.noteHubStart()
	close(h.srv.stopBeat) // the process dies here: no stop, the marker stays
	h.srv.stopBeat = nil
	h.srv.noteHubStart()
	h.srv.noteHubStop()
	if unclean() != 1 {
		t.Fatal("a crash went unnoticed")
	}
}
