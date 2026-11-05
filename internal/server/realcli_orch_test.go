package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nextboard/internal/jobs"
)

// TestRealOrchestration verifies phase 6 against the installed Claude Code
// (docs/API-orchestration.md "Hasil verifikasi"). It spends the owner's
// plan (2 tiny prompts: one planner run, one delivered order), so it never
// runs in a normal `go test`:
//
//	NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_ORCH=1 \
//	NEXTBOARD_REAL_CLI_CWD=/already/trusted/dir NEXTBOARD_REAL_CLI_DIR=/scratch \
//	go test -run TestRealOrchestration -v ./internal/server/
//
// The auto-updater is disabled (DISABLE_AUTOUPDATER=1); nothing runs with
// yolo; a fresh untrusted folder is only shown (trust dialog captured and
// cancelled with Esc, never accepted).
func TestRealOrchestration(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_CLI_TEST") != "1" || os.Getenv("NEXTBOARD_REAL_ORCH") != "1" {
		t.Skip("set NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_ORCH=1 (memakai plan CLI pemilik)")
	}
	trusted := os.Getenv("NEXTBOARD_REAL_CLI_CWD")
	scratch := os.Getenv("NEXTBOARD_REAL_CLI_DIR")
	if trusted == "" || scratch == "" {
		t.Fatal("NEXTBOARD_REAL_CLI_CWD (trusted folder) and NEXTBOARD_REAL_CLI_DIR (scratch) are required")
	}
	t.Setenv("DISABLE_AUTOUPDATER", "1")
	for _, k := range []string{"CLAUDE_EFFORT", "CLAUDE_PID", "CLAUDE_AGENT_SDK_VERSION"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	home, _ := os.UserHomeDir()
	sum := func(p string) string {
		b, _ := os.ReadFile(p)
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:8])
	}
	cfgFiles := []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "config.toml")}
	before := map[string]string{}
	for _, f := range cfgFiles {
		before[f] = sum(f)
	}
	t.Cleanup(func() {
		for _, f := range cfgFiles {
			if a := sum(f); a != before[f] {
				t.Errorf("%s changed during the run", f)
			} else {
				t.Logf("unchanged: %s (%s)", f, a)
			}
		}
	})
	h := newTestHub(t)
	h.setup()
	if _, err := h.srv.settings.Patch([]byte(`{"agents":{"hookChannel":"tty"},"commands":{"planner":"auto"}}`)); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "real-claude", "name": "Real Claude", "command": "claude",
		"args": []string{"--model", "haiku", "--permission-mode", "manual"}, "base": "claude"}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": trusted, "name": "realorch"}, nil)
	if st != 201 && st != 200 {
		t.Fatalf("project %d %v", st, p)
	}
	pid := p["id"].(string)
	wid := p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	dump := filepath.Join(scratch, "screens")
	os.MkdirAll(dump, 0o755)
	shot := func(id, name string) string {
		sess, _ := h.srv.sessions.Get(id)
		sn := sess.Screen()
		txt := sn.Text()
		os.WriteFile(filepath.Join(dump, name+".txt"), []byte(fmt.Sprintf("cursor=%d,%d alt=%v\n%s\n", sn.CursorX, sn.CursorY, sn.Alt, txt)), 0o644)
		return txt
	}
	info := func(id string) map[string]any { _, in := h.do("GET", "/api/sessions/"+id, nil, nil); return in }
	wait := func(what string, d time.Duration, f func() bool) bool {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if f() {
				return true
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Logf("TIMEOUT %s", what)
		return false
	}

	// A. Trust dialog at 60 columns in a fresh, untrusted folder: no
	// prompt is spent; the dialog is cancelled with Esc.
	untrusted := filepath.Join(scratch, "untrusted-"+time.Now().Format("150405"))
	os.MkdirAll(untrusted, 0o755)
	st, tr := h.do("POST", "/api/sessions", map[string]any{"agent": "real-claude", "workspaceId": wid, "cwd": untrusted, "cols": 60, "rows": 24}, nil)
	if st != 201 {
		t.Fatalf("trust console %d %v", st, tr)
	}
	tid := tr["id"].(string)
	if wait("trust needsYou", 40*time.Second, func() bool { return info(tid)["agentState"] == "needsYou" }) {
		time.Sleep(800 * time.Millisecond)
		in := info(tid)
		t.Logf("TRUST state=%v src=%v activity=%q", in["agentState"], in["stateSource"], in["activity"])
		shot(tid, "claude-2.1.312-needsYou-trust-60cols")
		_, d := h.do("POST", "/api/sessions/"+tid+"/deliveries", map[string]any{"text": "1"}, nil)
		time.Sleep(3 * time.Second)
		_, l := h.do("GET", "/api/sessions/"+tid+"/deliveries", nil, nil)
		t.Logf("TRUST delivery %v → %v", d["state"], l["items"])
		h.do("DELETE", "/api/deliveries/"+d["id"].(string), nil, nil)
		txt := shot(tid, "trust-after-delivery")
		t.Logf("TRUST screen unchanged by delivery: %v", strings.Contains(txt, "No, exit"))
	} else {
		shot(tid, "trust-timeout")
	}
	sess, _ := h.srv.sessions.Get(tid)
	_ = sess.Input([]byte("\x1b")) // human Esc: cancel (no trust written)
	wait("trust console leaves the agent", 15*time.Second, func() bool { return info(tid)["agentState"] == "shell" })
	t.Logf("after Esc: state=%v exit=%v", info(tid)["agentState"], info(tid)["agentExitCode"])
	h.do("DELETE", "/api/sessions/"+tid, nil, nil)

	// B. A real Claude console in the trusted folder.
	st, rc := h.do("POST", "/api/sessions", map[string]any{"agent": "real-claude", "workspaceId": wid, "cols": 110, "rows": 34}, nil)
	if st != 201 {
		t.Fatalf("real console %d %v", st, rc)
	}
	id := rc["id"].(string)
	handle := rc["handle"].(string)
	if !wait("ready", 60*time.Second, func() bool { return info(id)["agentState"] == "ready" }) {
		shot(id, "real-not-ready")
		t.Fatal("real Claude never became ready")
	}
	time.Sleep(1500 * time.Millisecond)
	shot(id, "real-ready")

	// C. Planner (claude -p --json-schema, no tools) → send to the real
	// console through the delivery engine.
	if pl := h.srv.orch.Planner(); !pl.Available {
		t.Fatalf("planner unavailable: %+v", pl)
	}
	start := time.Now()
	st, r := h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": "ask " + handle + " to reply with the single word OK", "workspaceId": wid, "clientId": "c_real"}, nil)
	t.Logf("COMMAND %d handledBy=%v", st, r["handledBy"])
	var job jobs.Job
	wait("planner job", 90*time.Second, func() bool {
		for _, j := range h.srv.jobs.List(pid, false, 10) {
			if j.Purpose == "planner" && !j.Active() {
				job, _ = h.srv.jobs.Get(j.ID)
				return true
			}
		}
		return false
	})
	t.Logf("PLANNER state=%s code=%q error=%q elapsedMs=%d wall=%v", job.State, job.Code, job.Error, job.ElapsedMs, time.Since(start).Round(time.Millisecond))
	t.Logf("PLANNER result=%s", job.Result)
	var res struct {
		Structured json.RawMessage `json:"structured"`
	}
	_ = json.Unmarshal(job.Result, &res)
	t.Logf("PLANNER structured_output present=%v", len(res.Structured) > 0)
	var oid string
	wait("order", 30*time.Second, func() bool {
		for _, o := range h.srv.orch.Orders.List(nil) {
			if o.ThreadID == pid {
				oid = o.ID
				return true
			}
		}
		return false
	})
	_, dls := h.do("GET", "/api/sessions/"+id+"/deliveries", nil, nil)
	t.Logf("DELIVERIES %v", dls["items"])
	wait("order finished", 120*time.Second, func() bool {
		o, _ := h.srv.orch.Orders.Get(oid)
		return len(o.Targets) > 0 && o.Targets[0].State == "finished"
	})
	o, _ := h.srv.orch.Orders.Get(oid)
	t.Logf("ORDER %+v", o)
	_, dls = h.do("GET", "/api/sessions/"+id+"/deliveries", nil, nil)
	t.Logf("DELIVERIES final %v", dls["items"])
	time.Sleep(2 * time.Second)
	shot(id, "real-after-order")
	_, msgs := h.do("GET", "/api/threads/"+pid+"/messages?limit=50", nil, nil)
	for _, m := range msgs["items"].([]any) {
		mm := m.(map[string]any)
		t.Logf("THREAD %v/%v: %q", mm["role"], mm["kind"], mm["text"])
	}
	// Exit the agent by hand (user input path), then the shell.
	_ = h.srv.sessionsInput(id, "/exit")
	time.Sleep(400 * time.Millisecond)
	_ = h.srv.sessionsInput(id, "\r")
	wait("shell", 20*time.Second, func() bool { return info(id)["agentState"] == "shell" })
	_ = h.srv.sessionsInput(id, "exit\r")
	wait("exited", 10*time.Second, func() bool { return info(id)["agentState"] == "exited" })
}

// sessionsInput types like a user (test helper).
func (s *Server) sessionsInput(id, text string) error {
	sess, ok := s.sessions.Get(id)
	if !ok {
		return fmt.Errorf("no session")
	}
	return sess.Input([]byte(text))
}
