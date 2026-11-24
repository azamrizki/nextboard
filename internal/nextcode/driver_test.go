package nextcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("NEXTCODE_FAKE") {
	case "claude":
		fakeClaude()
		os.Exit(0)
	case "codex":
		fakeCodex()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// rig is a driver on the fake CLI with a recording listener.
type rig struct {
	t      *testing.T
	d      *Driver
	sub    *Sub
	frames []Frame
	args   string // file the fake CLI writes its arguments to, one start per line
	log    string // file a fake CLI writes the lines it reads to (the Codex fake)
	starts []bool // resume flag of each start

	mu    sync.Mutex
	calls []string
}

func (r *rig) call(s string) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *rig) called() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.calls...)
}

// calledUntil waits for the listener's calls, joined with "|", to satisfy ok and returns them.
// The listener runs after the status frame it goes with (Driver.later), so a test that read it
// at once after turnEnded raced it on a busy machine.
func (r *rig) calledUntil(ok func(calls string) bool) string {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls := strings.Join(r.called(), "|")
		if ok(calls) || time.Now().After(deadline) {
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// endsWith reports whether the listener's last call is the end of a turn without an error.
func endsWith(calls, last string) bool { return strings.HasSuffix(calls, "|"+last) || calls == last }

// rigOpts chooses the rig's engine: its fake CLI (NEXTCODE_FAKE=<engine>), the agent's own
// launch the engine's argv starts from, the permission mode, more environment for the fake.
type rigOpts struct {
	engine string
	base   func(resume bool) []string
	mode   string
	env    []string
}

func newRig(t *testing.T, dir string, idle time.Duration) *rig {
	t.Helper()
	return newRigWith(t, dir, idle, rigOpts{})
}

func newRigWith(t *testing.T, dir string, idle time.Duration, o rigOpts) *rig {
	t.Helper()
	if o.engine == "" {
		o.engine = EngineClaude
	}
	if o.base == nil {
		o.base = func(resume bool) []string {
			if resume {
				return []string{os.Args[0], "--resume", "conv-1"}
			}
			return []string{os.Args[0], "--session-id", "conv-1"}
		}
	}
	r := &rig{t: t, args: filepath.Join(dir, "args"), log: filepath.Join(dir, "log")}
	on := Listener{
		TurnStart:    func() { r.call("start") },
		Tool:         func(n, s string) { r.call("tool " + n + " " + s) },
		NeedsYou:     func(k, s string) { r.call("needs " + k + " " + s) },
		Answered:     func() { r.call("answered") },
		TurnEnd:      func(e string) { r.call("end " + e) },
		Conversation: func(id string) { r.call("conv " + id) },
		Exit:         func(e string) { r.call("exit " + e) },
	}
	start := func(resume bool, line func([]string) []string) (*Proc, error) {
		r.mu.Lock()
		r.starts = append(r.starts, resume)
		r.mu.Unlock()
		argv := line(o.base(resume))
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append(append(os.Environ(), "NEXTCODE_FAKE="+o.engine, "NEXTCODE_FAKE_ARGS="+r.args, "NEXTCODE_FAKE_LOG="+r.log), o.env...)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		pr, pw := io.Pipe()
		cmd.Stdout = pw
		stderr := &lockedBuf{}
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			_ = pw.Close()
			close(done)
		}()
		return &Proc{Stdin: stdin, Stdout: pr, Done: done, Stderr: stderr.String,
			Stop: func() { _ = stdin.Close(); _ = cmd.Process.Kill() }}, nil
	}
	d, err := New(Config{Engine: o.engine, HistoryPath: filepath.Join(dir, "chat.jsonl"), Start: start, IdleExit: idle,
		PermissionMode: o.mode, On: on})
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	_, r.sub = d.Subscribe()
	t.Cleanup(d.Close)
	return r
}

// until reads frames until ok says so.
func (r *rig) until(what string, ok func(Frame) bool) Frame {
	r.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f, open := <-r.sub.C:
			if !open {
				r.t.Fatalf("subscription closed waiting for %s", what)
			}
			r.frames = append(r.frames, f)
			if ok(f) {
				return f
			}
		case <-deadline:
			r.t.Fatalf("timed out waiting for %s; frames %s", what, frameKinds(r.frames))
		}
	}
}

func frameKinds(fs []Frame) string {
	var s []string
	for _, f := range fs {
		k := f.T
		if f.Item != nil {
			k += ":" + f.Item.Kind
		}
		s = append(s, k)
	}
	return strings.Join(s, " ")
}

func (r *rig) turnEnded() {
	r.t.Helper()
	r.until("the turn's end", func(f Frame) bool { return f.T == FrameStatus && !f.Status.Working && f.Status.Process == ProcRunning })
}

func (r *rig) item(kind string) *Item {
	r.t.Helper()
	return r.until(kind+" item", func(f Frame) bool { return f.T == FrameItem && f.Item.Kind == kind }).Item
}

func TestStreamedTurnAndHistory(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir, 0)
	if err := r.d.Send("hello"); err != nil {
		t.Fatal(err)
	}
	if u := r.item(KindUser); u.Text != "hello" {
		t.Fatalf("user item %+v", u)
	}
	blk := r.until("block", func(f Frame) bool { return f.T == FrameBlock })
	if blk.Kind != KindText || blk.ID != "msg_1/0" {
		t.Fatalf("block %+v", blk)
	}
	var streamed string
	r.until("deltas", func(f Frame) bool {
		if f.T == FrameDelta {
			streamed += f.Text
		}
		return streamed == "Hi there"
	})
	txt := r.item(KindText)
	if txt.Text != "Hi there" || txt.Stream != "msg_1/0" {
		t.Fatalf("text item %+v", txt)
	}
	r.turnEnded()
	st := r.d.Status()
	if st.Model != "fake-model" || st.Conversation != "conv-1" || st.CostUSD != 0.01 || st.Usage.Input != 10 ||
		len(st.Commands) != 2 || st.Commands[0] != "review" || !strings.Contains(string(st.RateLimit), "allowed") {
		t.Fatalf("status %+v", st)
	}
	if got := r.calledUntil(func(c string) bool { return c == "start|conv conv-1|end " }); got != "start|conv conv-1|end " {
		t.Fatalf("listener %q", got)
	}
	// the chat comes back after a restart, and the next CLI resumes the conversation
	r.d.Close()
	d2, err := New(Config{HistoryPath: filepath.Join(dir, "chat.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	items, more := d2.Items("", 0)
	if more || len(items) != 2 || items[0].Kind != KindUser || items[1].Text != "Hi there" || !d2.Started() ||
		d2.Status().Conversation != "conv-1" {
		t.Fatalf("history %+v more %v started %v", items, more, d2.Started())
	}
}

func TestPermissionAllowAlwaysAndDeny(t *testing.T) {
	r := newRig(t, t.TempDir(), 0)
	_ = r.d.Send("write")
	req := r.until("request", func(f Frame) bool { return f.T == FrameRequest }).Request
	if req.Kind != ReqPermission || req.Tool != "Write" || req.ToolUseID != "toolu_w" || !req.CanRemember ||
		!strings.Contains(string(req.Input), "/tmp/x.txt") {
		t.Fatalf("request %+v", req)
	}
	if p := r.d.Pending(); len(p) != 1 {
		t.Fatalf("pending %+v", p)
	}
	if err := r.d.Answer(req.ID, Answer{Decision: AllowAlways}); err != nil {
		t.Fatal(err)
	}
	r.until("resolved", func(f Frame) bool { return f.T == FrameResolved && f.ID == req.ID })
	done := r.until("tool done", func(f Frame) bool { return f.T == FrameItem && f.Item.Tool != nil && f.Item.Tool.Status == ToolDone }).Item
	if done.Tool.Output != "File created (remember)" {
		t.Fatalf("tool %+v", done.Tool)
	}
	r.turnEnded()
	if err := r.d.Answer(req.ID, Answer{Decision: Allow}); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("second answer: %v", err)
	}

	_ = r.d.Send("write")
	req = r.until("request", func(f Frame) bool { return f.T == FrameRequest }).Request
	_ = r.d.Answer(req.ID, Answer{Decision: Deny, Message: "not now"})
	denied := r.until("tool denied", func(f Frame) bool {
		return f.T == FrameItem && f.Item.Tool != nil && f.Item.Tool.Status == ToolDenied
	}).Item
	if denied.Tool.Output != "not now" {
		t.Fatalf("denied tool %+v", denied.Tool)
	}
	r.turnEnded()
	calls := strings.Join(r.called(), "|")
	if !strings.Contains(calls, "tool Write /tmp/x.txt|needs permission Write(/tmp/x.txt)|answered") {
		t.Fatalf("listener %q", calls)
	}
}

func TestQuestion(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir, 0)
	_ = r.d.Send("ask")
	req := r.until("question", func(f Frame) bool { return f.T == FrameRequest }).Request
	if req.Kind != ReqQuestion {
		t.Fatalf("request %+v", req)
	}
	_ = r.d.Answer(req.ID, Answer{Decision: Allow, Answers: map[string][]string{"Colour?": {"Blue"}}})
	// the answer is kept in the chat: what was asked, what was chosen, in words too
	ans := r.item(KindAnswer)
	if a := ans.Answer; a == nil || a.ToolUseID != "toolu_q" || a.Skipped || len(a.Answers["Colour?"]) != 1 || a.Answers["Colour?"][0] != "Blue" ||
		!strings.Contains(string(a.Input), `"Red"`) || ans.Text != "Colour: Blue" || ans.Parent != "" {
		t.Fatalf("answer item %+v %+v", ans, ans.Answer)
	}
	txt := r.item(KindText)
	if txt.Text != `You chose {"Colour?":"Blue"}` {
		t.Fatalf("reply %q", txt.Text)
	}
	r.turnEnded()
	if calls := strings.Join(r.called(), "|"); !strings.Contains(calls, "needs question Question") {
		t.Fatalf("listener %q", calls)
	}
	// a question skipped is kept as skipped, without the note meant for the agent
	_ = r.d.Send("ask")
	req = r.until("question", func(f Frame) bool { return f.T == FrameRequest }).Request
	_ = r.d.Answer(req.ID, Answer{Decision: Deny, Message: "The owner chose not to answer."})
	skipped := r.item(KindAnswer)
	if !skipped.Answer.Skipped || skipped.Answer.Message != "" || len(skipped.Answer.Answers) != 0 || skipped.Text != "Skipped: the owner answered nothing." {
		t.Fatalf("skipped %+v %+v", skipped, skipped.Answer)
	}
	r.turnEnded()
	// both come back after a restart
	r.d.Close()
	d2, err := New(Config{HistoryPath: filepath.Join(dir, "chat.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	items, _ := d2.Items("", 0)
	var kept []string
	for _, it := range items {
		if it.Kind == KindAnswer && it.Answer != nil {
			kept = append(kept, it.Text)
		}
	}
	if strings.Join(kept, "|") != "Colour: Blue|Skipped: the owner answered nothing." {
		t.Fatalf("kept answers %q", kept)
	}
}

func TestPlanDecision(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir, 0)
	plan := func() *Request {
		t.Helper()
		_ = r.d.Send("plan")
		req := r.until("plan", func(f Frame) bool { return f.T == FrameRequest }).Request
		if req.Kind != ReqPlan || req.ToolUseID != "toolu_p" {
			t.Fatalf("request %+v", req)
		}
		return req
	}
	reply := func(want string) {
		t.Helper()
		if txt := r.item(KindText); txt.Text != want {
			t.Fatalf("reply %q, want %q", txt.Text, want)
		}
		r.turnEnded()
	}
	// sent back with a note: the agent is told it, the chat keeps it
	req := plan()
	_ = r.d.Answer(req.ID, Answer{Decision: Deny, Message: "Use the Redis we have."})
	kept := r.item(KindAnswer)
	if a := kept.Answer; a == nil || a.Kind != ReqPlan || a.Approved || a.Message != "Use the Redis we have." || a.ToolUseID != "toolu_p" || len(a.Input) != 0 ||
		kept.Text != "Keep planning: Use the Redis we have." {
		t.Fatalf("decision %+v %+v", kept, kept.Answer)
	}
	reply("kept planning: Use the Redis we have.")
	// without a note the agent is told to keep planning; the chat has no note, nor for the
	// words a client sends when there is none
	for _, msg := range []string{"", planKeep} {
		req = plan()
		_ = r.d.Answer(req.ID, Answer{Decision: Deny, Message: msg})
		if bare := r.item(KindAnswer); bare.Answer.Message != "" || bare.Text != "Keep planning (no note)." {
			t.Fatalf("no note %q: %+v %+v", msg, bare, bare.Answer)
		}
		reply("kept planning: " + planKeep)
	}
	// an unknown mode is refused, and the plan still waits
	req = plan()
	if err := r.d.Answer(req.ID, Answer{Decision: Allow, Mode: "yolo"}); !errors.Is(err, ErrBadMode) {
		t.Fatalf("bad mode: %v", err)
	}
	// approved into Accept edits: the approval carries the mode, and the console works in it
	if err := r.d.Answer(req.ID, Answer{Decision: Allow, Mode: "acceptEdits"}); err != nil {
		t.Fatal(err)
	}
	ok := r.item(KindAnswer)
	if a := ok.Answer; !a.Approved || a.Mode != "acceptEdits" || a.Message != "" || ok.Text != "Approved the plan, with Accept edits: it edits files without asking." {
		t.Fatalf("approved %+v %+v", ok, ok.Answer)
	}
	reply(`approved [{"destination":"session","mode":"acceptEdits","type":"setMode"}]`)
	if m := r.d.Status().PermissionMode; m != "acceptEdits" {
		t.Fatalf("mode %q", m)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(r.log)
		if strings.Contains(string(b), "set_permission_mode acceptEdits") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no mode change sent: %q", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// approved as it is
	req = plan()
	_ = r.d.Answer(req.ID, Answer{Decision: Allow})
	if a := r.item(KindAnswer); !a.Answer.Approved || a.Answer.Mode != "" || a.Text != "Approved the plan." {
		t.Fatalf("approved %+v %+v", a, a.Answer)
	}
	reply("approved null")
	// all of it comes back after a restart
	r.d.Close()
	d2, err := New(Config{HistoryPath: filepath.Join(dir, "chat.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	items, _ := d2.Items("", 0)
	var texts []string
	for _, it := range items {
		if it.Kind == KindAnswer && it.Answer != nil && it.Answer.Kind == ReqPlan {
			texts = append(texts, it.Text)
		}
	}
	if got := strings.Join(texts, "|"); got != "Keep planning: Use the Redis we have.|Keep planning (no note).|Keep planning (no note).|Approved the plan, with Accept edits: it edits files without asking.|Approved the plan." {
		t.Fatalf("kept decisions %q", got)
	}
}

func TestAnswerText(t *testing.T) {
	in := json.RawMessage(`{"questions":[{"question":"Where?","header":"Store","options":[{"label":"Redis"}]},{"question":"Which limits?","options":[{"label":"Per IP"}]}]}`)
	a := &Answered{Input: in, Answers: map[string][]string{"Which limits?": {"Per IP", "Per account"}, "Where?": {"Redis"}, "Gone?": {"x"}}, Message: "and log it"}
	if got := answerText(a); got != "Store: Redis\nWhich limits?: Per IP, Per account\nGone?: x\nand log it" {
		t.Fatalf("text %q", got)
	}
}

func TestInterrupt(t *testing.T) {
	r := newRig(t, t.TempDir(), 0)
	if err := r.d.Interrupt(); !errors.Is(err, ErrNotWorking) {
		t.Fatalf("idle interrupt: %v", err)
	}
	_ = r.d.Send("long")
	r.until("a delta", func(f Frame) bool { return f.T == FrameDelta })
	if err := r.d.Interrupt(); err != nil {
		t.Fatal(err)
	}
	n := r.item(KindNotice)
	if n.Text != "Stopped." {
		t.Fatalf("notice %+v", n)
	}
	r.turnEnded()
	for _, f := range r.frames {
		if f.T == FrameItem && f.Item.Kind == KindError {
			t.Fatalf("a stopped turn is not an error: %+v", f.Item)
		}
	}
	if calls := r.calledUntil(func(c string) bool { return endsWith(c, "end ") }); !endsWith(calls, "end ") {
		t.Fatalf("listener %q", calls)
	}
}

func TestFailedTurnAndCrash(t *testing.T) {
	r := newRig(t, t.TempDir(), 0)
	_ = r.d.Send("fail")
	if e := r.item(KindError); e.Text != "error max turns" {
		t.Fatalf("error %+v", e)
	}
	r.turnEnded()

	// the reply and the result say the same: one error, not two
	_ = r.d.Send("nologin")
	if e := r.item(KindError); e.Text != "Not logged in · Please run /login" {
		t.Fatalf("not logged in %+v", e)
	}
	r.turnEnded()
	n := 0
	items, _ := r.d.Items("", 0)
	for _, it := range items {
		if strings.Contains(it.Text, "Not logged in") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d items say it: %+v", n, items)
	}

	_ = r.d.Send("crash")
	e := r.item(KindError)
	if !strings.Contains(e.Text, "boom: fatal") {
		t.Fatalf("crash %+v", e)
	}
	st := r.until("stopped", func(f Frame) bool { return f.T == FrameStatus && f.Status.Process == ProcStopped }).Status
	if st.Working || !strings.Contains(st.Error, "boom") {
		t.Fatalf("status %+v", st)
	}
	if calls := strings.Join(r.called(), "|"); !strings.Contains(calls, "exit The agent's CLI ended: boom: fatal") {
		t.Fatalf("listener %q", calls)
	}
	// the next message starts a CLI on the conversation again
	_ = r.d.Send("hello")
	r.item(KindText)
	r.turnEnded()
	r.mu.Lock()
	starts := append([]bool{}, r.starts...)
	r.mu.Unlock()
	if len(starts) != 2 || starts[0] || !starts[1] {
		t.Fatalf("starts (resume?) %v", starts)
	}
	if r.d.Status().Error != "" {
		t.Fatal("the error stays after a new start")
	}
}

func TestIdleExitAndResume(t *testing.T) {
	r := newRig(t, t.TempDir(), 150*time.Millisecond)
	_ = r.d.Send("hello")
	r.turnEnded()
	r.until("idle exit", func(f Frame) bool { return f.T == FrameStatus && f.Status.Process == ProcStopped })
	for _, f := range r.frames {
		if f.T == FrameItem && f.Item.Kind == KindError {
			t.Fatalf("an idle exit is not an error: %+v", f.Item)
		}
	}
	_ = r.d.Send("again")
	if txt := r.item(KindText); txt.Text != "echo again" {
		t.Fatalf("reply %+v", txt)
	}
	b, _ := os.ReadFile(r.args)
	starts := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(starts) != 2 || !strings.Contains(starts[0], "--session-id conv-1") || !strings.Contains(starts[1], "--resume conv-1") ||
		!strings.Contains(starts[1], "--permission-prompt-tool stdio") || !strings.Contains(starts[1], "--input-format stream-json") {
		t.Fatalf("CLI starts %q", starts)
	}
	if calls := r.called(); strings.Contains(strings.Join(calls, "|"), "exit") {
		t.Fatalf("listener %q", calls)
	}
}

func TestSubAgentAndUnknownRequest(t *testing.T) {
	r := newRig(t, t.TempDir(), 0)
	_ = r.d.Send("sub")
	p := r.item(KindPrompt)
	if p.Parent != "toolu_t" || p.Text != "do the thing" {
		t.Fatalf("prompt %+v", p)
	}
	b := r.until("sub-agent tool", func(f Frame) bool {
		return f.T == FrameItem && f.Item.ID == "toolu_b" && f.Item.Tool.Status == ToolDone
	}).Item
	if b.Parent != "toolu_t" || b.Tool.Output != "a\nb" {
		t.Fatalf("sub tool %+v", b)
	}
	r.turnEnded()
	calls := strings.Join(r.called(), "|")
	if !strings.Contains(calls, "tool Task look around") || strings.Contains(calls, "tool Bash") {
		t.Fatalf("listener %q (a sub-agent's tools are not the console's activity)", calls)
	}

	_ = r.d.Send("hook")
	if txt := r.item(KindText); txt.Text != "hook answered error" {
		t.Fatalf("hook reply %+v", txt)
	}
}

func TestModesAndLimits(t *testing.T) {
	r := newRig(t, t.TempDir(), 0)
	if err := r.d.Send("  \n"); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty: %v", err)
	}
	if err := r.d.Send(strings.Repeat("x", MaxMessage+1)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("long: %v", err)
	}
	if err := r.d.SetPermissionMode("yolo"); !errors.Is(err, ErrBadMode) {
		t.Fatalf("bad mode: %v", err)
	}
	if err := r.d.SetPermissionMode("acceptEdits"); err != nil {
		t.Fatal(err)
	}
	_ = r.d.Send("hello")
	r.turnEnded()
	b, _ := os.ReadFile(r.args)
	if !strings.Contains(string(b), "--permission-mode acceptEdits") || r.d.Status().PermissionMode != "acceptEdits" {
		t.Fatalf("args %q status %+v", b, r.d.Status())
	}
	// the clip keeps both ends of a long output and a long input stays valid JSON
	out := clipOutput(strings.Repeat("a", maxOutput) + "END")
	if !strings.HasSuffix(out, "END") || len(out) > maxOutput+10 {
		t.Fatalf("clip %d", len(out))
	}
	in, clipped := clipInput(json.RawMessage(`{"content":"` + strings.Repeat("b", maxInput) + `"}`))
	if !clipped || !json.Valid(in) || len(in) > maxInput {
		t.Fatalf("clip input %d %v", len(in), clipped)
	}
}
