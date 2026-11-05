package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nextboard/internal/machine"
	"nextboard/internal/store"
)

// fakeClaude imitates `claude -p` (stream-json or json); FAKE_MODE picks
// the behaviour. The prompt arrives on stdin.
const fakeClaude = `#!/bin/sh
prompt=$(cat)
[ -n "$FAKE_PIDFILE" ] && echo $$ > "$FAKE_PIDFILE"
[ -n "$FAKE_COUNT" ] && echo x >> "$FAKE_COUNT"
case "$FAKE_MODE" in
stream)
  echo '{"type":"system","subtype":"init","session_id":"sess-1"}'
  sleep 0.1
  echo '{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"}]}}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Hello "}]}}'
  sleep 0.1
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"world"}]}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":"Hello world","session_id":"sess-1"}'
  ;;
echo)
  printf '{"type":"result","subtype":"success","is_error":false,"result":"got:%s","session_id":"s2"}\n' "$prompt" ;;
partial)
  echo '{"type":"system","subtype":"init","session_id":"sess-p"}'
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"# Draft"}]}}'
  sleep 30 ;;
idle)
  echo '{"type":"system","subtype":"init","session_id":"sess-i"}'
  sleep 30 ;;
wait)
  while [ ! -e "$FAKE_GATE" ]; do sleep 0.05; done
  echo '{"type":"result","subtype":"success","is_error":false,"result":"released"}' ;;
login)
  echo '{"type":"result","subtype":"success","is_error":true,"result":"Invalid API key · Please run /login"}'
  exit 1 ;;
crash)
  echo 'boom: something broke' >&2
  exit 3 ;;
badjson)
  echo '{"type":"result","subtype":"success","is_error":false,"result":"not json at all"}' ;;
fenced)
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"` + "```json\\n{\\\"a\\\": 1}\\n```" + `"}' ;;
esac
`

// fakeCodex imitates `codex exec --json`; with --output-schema it answers
// with the schema path it got and copies the schema to $FAKE_OUT.
const fakeCodex = `#!/bin/sh
schema=
while [ $# -gt 0 ]; do case "$1" in --output-schema) schema="$2"; shift;; esac; shift; done
prompt=$(cat)
echo '{"type":"thread.started","thread_id":"th-1"}'
echo '{"type":"turn.started"}'
echo '{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"thinking"}}'
if [ -n "$schema" ]; then
  cp "$schema" "$FAKE_OUT"
  printf '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"{\\"dir\\":\\"%s\\"}"}}\n' "$(dirname "$schema")"
else
  printf '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"codex:%s"}}\n' "$prompt"
fi
echo '{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":1}}'
`

type events struct {
	mu   sync.Mutex
	list []map[string]any
}

func (e *events) add(v any) {
	e.mu.Lock()
	e.list = append(e.list, v.(map[string]any))
	e.mu.Unlock()
}

func (e *events) of(typ, jobID string) []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []map[string]any
	for _, ev := range e.list {
		if ev["type"] != typ {
			continue
		}
		if j, ok := ev["job"].(Job); ok && j.ID != jobID {
			continue
		}
		if id, ok := ev["jobId"].(string); ok && id != jobID {
			continue
		}
		out = append(out, ev)
	}
	return out
}

type fixture struct {
	t      *testing.T
	m      *Manager
	st     *store.Store
	ev     *events
	claude string
	codex  string
	dir    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	local := machine.NewLocal(machine.Config{})
	hosts := func(id string) (machine.Host, bool) {
		switch id {
		case machine.LocalID, "m_a", "m_b":
			return local, true
		}
		return nil, false
	}
	f := &fixture{t: t, st: st, ev: &events{}, dir: dir,
		claude: write("fake-claude", fakeClaude), codex: write("fake-codex", fakeCodex)}
	f.m = NewManager(st, hosts)
	f.m.Publish = f.ev.add
	t.Cleanup(f.m.Close)
	return f
}

func (f *fixture) req(purpose, mode string, env ...string) RunnerRequest {
	return RunnerRequest{Purpose: purpose, Agent: "claude", Command: f.claude, Cwd: f.dir, Prompt: "hi",
		Env: append([]string{"FAKE_MODE=" + mode}, env...)}
}

func (f *fixture) submit(r RunnerRequest) Job {
	f.t.Helper()
	j, err := f.m.SubmitRunner(r)
	if err != nil {
		f.t.Fatal(err)
	}
	return j
}

// wait polls until the job matches cond.
func (f *fixture) wait(id string, cond func(Job) bool) Job {
	f.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := f.m.Get(id); ok && cond(j) {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	j, _ := f.m.Get(id)
	f.t.Fatalf("timeout; job %+v", j)
	return Job{}
}

func finished(j Job) bool { return !j.Active() }

func result(t *testing.T, j Job) runnerResult {
	t.Helper()
	var r runnerResult
	if err := json.Unmarshal(j.Result, &r); err != nil {
		t.Fatalf("result %s: %v", j.Result, err)
	}
	return r
}

func TestRunnerLifecycleAndStream(t *testing.T) {
	f := newFixture(t)
	r := f.req("document", "stream")
	r.Stream = true
	j := f.submit(r)
	if j.Kind != KindRunner || j.MachineID != machine.LocalID || !strings.HasPrefix(j.ID, "jb_") {
		t.Fatalf("job %+v", j)
	}
	j = f.wait(j.ID, finished)
	if j.State != StateDone || j.Code != "" || j.StartedAt == nil || j.FinishedAt == nil || j.ElapsedMs <= 0 {
		t.Fatalf("done %+v", j)
	}
	res := result(t, j)
	if res.Text != "Hello world" || res.AgentSessionID != "sess-1" {
		t.Fatalf("result %+v", res)
	}
	// State and phases in order: queued → running(launching … writing …) → done.
	var states, phases []string
	for _, ev := range f.ev.of("job", j.ID) {
		jj := ev["job"].(Job)
		if len(states) == 0 || states[len(states)-1] != jj.State {
			states = append(states, jj.State)
		}
		phases = append(phases, jj.Progress.Phase)
	}
	if strings.Join(states, ",") != "queued,running,done" {
		t.Fatalf("states %v", states)
	}
	if phases[1] != "launching" || phases[len(phases)-1] != "done" {
		t.Fatalf("phases %v", phases)
	}
	out := f.ev.of("jobOutput", j.ID)
	if len(out) != 2 || out[0]["seq"] != 1 || out[1]["seq"] != 2 || out[0]["text"] != "Hello " || out[1]["text"] != "world" {
		t.Fatalf("jobOutput %v", out)
	}
	if l := f.m.List("", false, 0); len(l) != 1 || l[0].ID != j.ID || l[0].Result != nil {
		t.Fatalf("list %+v", l)
	}
	if l := f.m.List("", true, 0); len(l) != 0 {
		t.Fatalf("active list %+v", l)
	}
}

func TestRunnerPromptOnStdinAndJSONOutput(t *testing.T) {
	f := newFixture(t)
	j := f.wait(f.submit(f.req("commit-subject", "echo")).ID, finished)
	if j.State != StateDone || result(t, j).Text != "got:hi" {
		t.Fatalf("%+v %s", j, j.Result)
	}
	if got := f.ev.of("jobOutput", j.ID); len(got) != 0 {
		t.Fatalf("json mode streamed output: %v", got)
	}
}

// Every purpose the orchestration submits must be known here: an unknown one
// fails only when a person presses the button.
func TestMemoryInitIsAReadOnlyPurpose(t *testing.T) {
	if p, ok := purposes["memory-init"]; !ok || !p.readOnly {
		t.Fatalf("memory-init: %+v %v", p, ok)
	}
}

func TestRunnerCodex(t *testing.T) {
	f := newFixture(t)
	r := RunnerRequest{Purpose: "memory-scan", Agent: "codex", Command: f.codex, Cwd: f.dir, Prompt: "scan"}
	j := f.wait(f.submit(r).ID, finished)
	res := result(t, j)
	if j.State != StateDone || res.Text != "codex:scan" || res.AgentSessionID != "th-1" {
		t.Fatalf("%+v %+v", j, res)
	}
	if out := f.ev.of("jobOutput", j.ID); len(out) != 1 || out[0]["text"] != "codex:scan" {
		t.Fatalf("output %v", out)
	}

	// Schema → private /tmp/nb-run.XXXXXX folder, always removed.
	copyTo := filepath.Join(f.dir, "schema-copy.json")
	r.Schema = json.RawMessage(`{ "type": "object" }`)
	r.Env = []string{"FAKE_OUT=" + copyTo}
	j = f.wait(f.submit(r).ID, finished)
	res = result(t, j)
	var got struct{ Dir string }
	if j.State != StateDone || json.Unmarshal(res.Structured, &got) != nil || !strings.HasPrefix(got.Dir, "/tmp/nb-run.") {
		t.Fatalf("schema run %+v %s", j, j.Result)
	}
	if b, _ := os.ReadFile(copyTo); string(b) != `{"type":"object"}` {
		t.Fatalf("schema file %q", b)
	}
	if _, err := os.Stat(got.Dir); !os.IsNotExist(err) {
		t.Fatalf("temp folder left behind: %s (%v)", got.Dir, err)
	}
}

func TestRunnerCancelKeepsPartialDocument(t *testing.T) {
	f := newFixture(t)
	pidFile := filepath.Join(f.dir, "pid")
	r := f.req("document", "partial", "FAKE_PIDFILE="+pidFile)
	r.Stream = true
	j := f.submit(r)
	f.wait(j.ID, func(j Job) bool { return j.Progress.Phase == "writing" })
	b, _ := os.ReadFile(pidFile)
	j, err := f.m.Cancel(j.ID)
	if err != nil || j.State != StateCancelled {
		t.Fatalf("cancel %+v %v", j, err)
	}
	if res := result(t, j); res.Text != "# Draft" || !res.Partial {
		t.Fatalf("partial %+v", res)
	}
	pid := strings.TrimSpace(string(b))
	if _, err := os.Stat("/proc/" + pid); err == nil {
		if st, _ := os.ReadFile("/proc/" + pid + "/stat"); !strings.Contains(string(st), ") Z") {
			t.Fatalf("runner process %s still alive", pid)
		}
	}
	// Non-document purposes keep no partial result.
	r = f.req("diagram", "partial")
	r.Stream = true
	j = f.submit(r)
	f.wait(j.ID, func(j Job) bool { return j.Progress.Phase == "writing" })
	if j, _ = f.m.Cancel(j.ID); j.State != StateCancelled || j.Result != nil {
		t.Fatalf("diagram cancel %+v", j)
	}
	if _, err := f.m.Cancel("jb_nope"); err != ErrNotFound {
		t.Fatalf("unknown %v", err)
	}
}

func TestRunnerTimeouts(t *testing.T) {
	f := newFixture(t)
	r := f.req("commit-subject", "idle")
	r.Timeout, r.IdleTimeout = 30*time.Second, 700*time.Millisecond
	idle := f.submit(r)
	r = f.req("commit-subject", "idle")
	r.Timeout, r.IdleTimeout = 700*time.Millisecond, 30*time.Second
	total := f.submit(r)
	if j := f.wait(idle.ID, finished); j.State != StateFailed || j.Code != "idle_timeout" {
		t.Fatalf("idle %+v", j)
	}
	if j := f.wait(total.ID, finished); j.State != StateFailed || j.Code != "timeout" {
		t.Fatalf("timeout %+v", j)
	}
}

func TestRunnerFailures(t *testing.T) {
	f := newFixture(t)
	r := f.req("planner", "echo")
	r.Command = "/nonexistent/claude-nb-test"
	if j := f.wait(f.submit(r).ID, finished); j.Code != "cli_missing" || !strings.Contains(j.Error, "tidak ditemukan") {
		t.Fatalf("missing %+v", j)
	}
	if j := f.wait(f.submit(f.req("planner", "login")).ID, finished); j.Code != "cli_not_logged_in" {
		t.Fatalf("login %+v", j)
	}
	if j := f.wait(f.submit(f.req("planner", "crash")).ID, finished); j.Code != "runner_failed" || !strings.Contains(j.Error, "boom") || !strings.Contains(j.Error, "kode keluar 3") {
		t.Fatalf("crash %+v", j)
	}

	// Schema + output that is not JSON: retried once, then bad_output.
	count := filepath.Join(f.dir, "count")
	r = f.req("diagram", "badjson", "FAKE_COUNT="+count)
	r.Schema = json.RawMessage(`{"type":"object"}`)
	if j := f.wait(f.submit(r).ID, finished); j.Code != "bad_output" {
		t.Fatalf("bad output %+v", j)
	}
	if b, _ := os.ReadFile(count); strings.Count(string(b), "x") != 2 {
		t.Fatalf("attempts %q", b)
	}
	r = f.req("diagram", "fenced")
	r.Schema = json.RawMessage(`{"type":"object"}`)
	j := f.wait(f.submit(r).ID, finished)
	if res := result(t, j); j.State != StateDone || string(res.Structured) != `{"a":1}` {
		t.Fatalf("fenced %+v %s", j, j.Result)
	}

	for _, bad := range []RunnerRequest{
		{Purpose: "document", Agent: "gemini", Prompt: "x"},
		{Purpose: "document", Agent: "my-custom", Prompt: "x"},
	} {
		if _, err := f.m.SubmitRunner(bad); err != ErrRunnerUnsupported {
			t.Fatalf("%+v → %v", bad, err)
		}
	}
	if _, err := f.m.SubmitRunner(RunnerRequest{Purpose: "shell", Agent: "claude", Prompt: "x"}); err != ErrBadPurpose {
		t.Fatalf("purpose %v", err)
	}
	if _, err := f.m.SubmitRunner(RunnerRequest{Purpose: "document", Agent: "claude", Prompt: " "}); err == nil {
		t.Fatal("empty prompt accepted")
	}
	if _, err := f.m.SubmitRunner(RunnerRequest{Purpose: "document", Agent: "claude", Prompt: "x", Schema: json.RawMessage("{")}); err == nil {
		t.Fatal("bad schema accepted")
	}
	r = f.req("document", "echo")
	r.MachineID = "m_gone"
	if j := f.wait(f.submit(r).ID, finished); j.Code != "machine_not_found" {
		t.Fatalf("machine %+v", j)
	}
}

func TestRunnerPlannerCwd(t *testing.T) {
	f := newFixture(t)
	r := f.req("planner", "echo")
	r.MachineID, r.Cwd = "m_a", "/somewhere/else"
	j := f.wait(f.submit(r).ID, finished)
	if j.MachineID != machine.LocalID || j.State != StateDone {
		t.Fatalf("planner %+v", j)
	}
	fi, err := os.Stat(f.st.Path("runner", "planner"))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("planner dir %v %v", fi, err)
	}
}

func TestRunnerQueueLimits(t *testing.T) {
	f := newFixture(t)
	gate := filepath.Join(f.dir, "gate")
	var ids []string
	// 3 jobs on This PC: only 2 run. Then 3 more on two other machines:
	// the hub limit (4) holds back the last one.
	for _, mid := range []string{"local", "local", "local", "m_a", "m_a", "m_b"} {
		r := f.req("commit-subject", "wait", "FAKE_GATE="+gate)
		r.MachineID = mid
		ids = append(ids, f.submit(r).ID)
	}
	time.Sleep(300 * time.Millisecond)
	state := func() string {
		var s []string
		for _, id := range ids {
			j, _ := f.m.Get(id)
			s = append(s, j.State[:1])
		}
		return strings.Join(s, "")
	}
	if got := state(); got != "rrqrrq" {
		t.Fatalf("states %s (want rrqrrq)", got)
	}
	if l := f.m.List("", true, 0); len(l) != 6 {
		t.Fatalf("active %d", len(l))
	}
	// Cancelling a queued job frees nothing and never starts it.
	if j, _ := f.m.Cancel(ids[2]); j.State != StateCancelled || j.StartedAt != nil {
		t.Fatalf("queued cancel %+v", j)
	}
	os.WriteFile(gate, nil, 0o600)
	for i, id := range ids {
		j := f.wait(id, finished)
		if i != 2 && j.State != StateDone {
			t.Fatalf("job %d %+v", i, j)
		}
	}
}

func TestPersistenceAndRestart(t *testing.T) {
	f := newFixture(t)
	done := f.wait(f.submit(f.req("commit-subject", "echo")).ID, finished)
	gate := filepath.Join(f.dir, "gate")
	running := f.submit(f.req("commit-subject", "wait", "FAKE_GATE="+gate))
	f.wait(running.ID, func(j Job) bool { return j.State == StateRunning })
	f.m.Close() // hub shutdown with a job in flight
	if j, _ := f.m.Get(running.ID); j.State != StateFailed || j.Code != "hub_restart" {
		t.Fatalf("after close %+v", j)
	}
	if _, err := f.m.SubmitRunner(f.req("commit-subject", "echo")); err != ErrClosed {
		t.Fatalf("submit after close %v", err)
	}

	// A crash leaves "running" in jobs.json: reported as failed on load.
	var saved []Job
	if _, err := f.st.Load(jobsFile, &saved); err != nil || len(saved) != 2 {
		t.Fatalf("saved %d %v", len(saved), err)
	}
	saved[1].State, saved[1].Code, saved[1].FinishedAt = StateRunning, "", nil
	f.st.Save(jobsFile, saved)
	m2 := NewManager(f.st, func(string) (machine.Host, bool) { return nil, false })
	defer m2.Close()
	if j, ok := m2.Get(running.ID); !ok || j.State != StateFailed || j.Code != "hub_restart" || j.FinishedAt == nil {
		t.Fatalf("reloaded %+v", j)
	}
	if j, ok := m2.Get(done.ID); !ok || j.State != StateDone || result(t, j).Text != "got:hi" {
		t.Fatalf("reloaded done %+v", j)
	}
}

func TestProgressEventsThrottled(t *testing.T) {
	f := newFixture(t)
	gate := filepath.Join(f.dir, "gate")
	j := f.submit(f.req("commit-subject", "wait", "FAKE_GATE="+gate))
	f.wait(j.ID, func(j Job) bool { return j.State == StateRunning })
	time.Sleep(50 * time.Millisecond)
	f.m.mu.Lock()
	rec := f.m.jobs[j.ID]
	f.m.mu.Unlock()
	before := len(f.ev.of("job", j.ID))
	for i := 0; i < 50; i++ {
		f.m.setProgress(rec, "thinking", "step "+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if n := len(f.ev.of("job", j.ID)) - before; n > 1 {
		t.Fatalf("%d events for 50 rapid progress updates", n)
	}
	time.Sleep(700 * time.Millisecond)
	evs := f.ev.of("job", j.ID)
	if n := len(evs) - before; n < 1 || n > 2 {
		t.Fatalf("coalesced events %d", n)
	}
	if last := evs[len(evs)-1]["job"].(Job); last.Progress.Text != "step xb" {
		t.Fatalf("last progress %+v", last.Progress)
	}
	os.WriteFile(gate, nil, 0o600)
	f.wait(j.ID, finished)
}
