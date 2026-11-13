package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/settings"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// fakeConsole is a terminal whose state the test controls; every byte
// the engine writes is recorded.
type fakeConsole struct {
	mu     sync.Mutex
	v      session.DeliveryView
	writes []string
	fgOK   bool
	fgErr  error
	woken  int
}

func (f *fakeConsole) View() session.DeliveryView {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.v
	v.Lines = append([]string(nil), f.v.Lines...)
	return v
}

func (f *fakeConsole) AgentForeground(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fgOK, f.fgErr
}

func (f *fakeConsole) Write(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, string(p))
	return nil
}

// Wake is an asleep console starting: it becomes ready when the test says so.
func (f *fakeConsole) Wake() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.v.Status != session.StatusAsleep {
		return false
	}
	f.woken++
	f.v.Status, f.v.AgentState, f.v.Running = session.StatusStarting, agentstate.Starting, false
	return true
}

func (f *fakeConsole) set(fn func(v *session.DeliveryView)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.v)
}

func (f *fakeConsole) written() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.writes, "")
}

var claudeBox = []string{"● previous reply", "", "──────────────────────", "❯ ", "──────────────────────", "  ⏸ manual mode on"}

// readyClaude is a Claude console at its input prompt.
func readyClaude() *fakeConsole {
	return &fakeConsole{fgOK: true, v: session.DeliveryView{
		Handle: "Ash", ProjectID: "p_test", AgentKind: "agent", Status: session.StatusRunning, Running: true,
		AgentState: agentstate.Ready, StateSince: t0.Add(-time.Minute), Foreground: agentstate.FgAgent,
		ScreenRules: "claude", Lines: append([]string(nil), claudeBox...), CursorY: 3, BracketedPaste: true,
		LastOutput: t0.Add(-time.Minute),
	}}
}

type harness struct {
	e      *Engine
	cons   map[string]*fakeConsole
	events []Delivery
	mu     sync.Mutex
	st     settings.Delivery
}

func newHarness() *harness {
	h := &harness{cons: map[string]*fakeConsole{}, st: settings.Defaults().Delivery}
	h.e = New()
	h.e.Now = func() time.Time { return t0 }
	h.e.Source = func(id string) (Console, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		c, ok := h.cons[id]
		return c, ok
	}
	h.e.Settings = func() settings.Delivery { return h.st }
	h.e.OnChange = func(d Delivery) {
		h.mu.Lock()
		h.events = append(h.events, d)
		h.mu.Unlock()
	}
	return h
}

func (h *harness) add(id string, c *fakeConsole) *fakeConsole {
	h.mu.Lock()
	h.cons[id] = c
	h.mu.Unlock()
	return c
}

func (h *harness) get(t *testing.T, id string) Delivery {
	t.Helper()
	d, ok := h.e.Get(id)
	if !ok {
		t.Fatalf("delivery %s gone", id)
	}
	return d
}

// run steps the engine from t0+from to t0+to every 100 ms.
func (h *harness) run(from, to time.Duration) {
	for d := from; d <= to; d += 100 * time.Millisecond {
		h.e.Step(t0.Add(d))
	}
}

func TestSanitize(t *testing.T) {
	in := "a\x1b[31mred\x1b[0m\r\nline2\rline3\ttab\x07bell\x7fdel\u009bcsi"
	if got := Sanitize(in); got != "a[31mred[0m\nline2\nline3\ttabbelldelcsi" {
		t.Fatalf("%q", got)
	}
}

// TestDeliveryReadyBracketedPaste: §19.3 — multi-line text in bracketed
// paste, ESC removed, Return after submitDelayMs, confirmed on working.
func TestDeliveryReadyBracketedPaste(t *testing.T) {
	h := newHarness()
	c := h.add("s_a", readyClaude())
	d, err := h.e.Submit("s_a", Request{Text: "Run the tests\n\x1b[2Jand fix failures", Source: "order"})
	if err != nil || d.State != Queued || d.Handle != "Ash" || d.ProjectID != "p_test" {
		t.Fatalf("%v %+v", err, d)
	}
	h.e.Step(t0)
	if got := c.written(); got != "\x1b[200~Run the tests\n[2Jand fix failures\x1b[201~" {
		t.Fatalf("paste %q", got)
	}
	if h.get(t, d.ID).State != Typing {
		t.Fatal("not typing")
	}
	h.e.Step(t0.Add(100 * time.Millisecond)) // before submitDelayMs
	if strings.HasSuffix(c.written(), "\r") {
		t.Fatal("Return before the submit delay")
	}
	h.e.Step(t0.Add(250 * time.Millisecond))
	if !strings.HasSuffix(c.written(), "\x1b[201~\r") || h.get(t, d.ID).State != Sent {
		t.Fatalf("no Return: %q %+v", c.written(), h.get(t, d.ID))
	}
	c.set(func(v *session.DeliveryView) { v.AgentState, v.StateSince = agentstate.Working, t0.Add(time.Second) })
	h.e.Step(t0.Add(time.Second))
	if got := h.get(t, d.ID); got.State != Confirmed || got.ConfirmedAt == nil || h.e.ActiveCount("s_a") != 0 {
		t.Fatalf("confirm %+v", got)
	}
	// Without mode 2004 the text becomes one line.
	c2 := h.add("s_b", readyClaude())
	c2.set(func(v *session.DeliveryView) { v.BracketedPaste = false })
	d2, _ := h.e.Submit("s_b", Request{Text: "one\ntwo"})
	h.e.Step(t0)
	if c2.written() != "one two" {
		t.Fatalf("plain %q", c2.written())
	}
	// Not confirmed within 8 s: stays sent and leaves the queue.
	h.run(300*time.Millisecond, 9*time.Second)
	if got := h.get(t, d2.ID); got.State != Sent || got.ConfirmedAt != nil || h.e.ActiveCount("s_b") != 0 {
		t.Fatalf("unconfirmed %+v", got)
	}
}

// TestDeliveryNeverTypesIntoShell: §19.1.
func TestDeliveryNeverTypesIntoShell(t *testing.T) {
	h := newHarness()
	sh := h.add("s_sh", readyClaude())
	sh.set(func(v *session.DeliveryView) {
		v.AgentKind, v.AgentState, v.Foreground = "shell", agentstate.Shell, agentstate.FgShell
	})
	if _, err := h.e.Submit("s_sh", Request{Text: "ls"}); !errors.Is(err, ErrNotAgent) {
		t.Fatalf("shell console: %v", err)
	}
	ex := h.add("s_ex", readyClaude())
	ex.set(func(v *session.DeliveryView) { v.AgentState, v.Foreground = agentstate.Shell, agentstate.FgShell })
	if _, err := h.e.Submit("s_ex", Request{Text: "ls"}); !errors.Is(err, ErrNotAgent) {
		t.Fatalf("exited agent: %v", err)
	}
	man := h.add("s_man", readyClaude())
	man.set(func(v *session.DeliveryView) { v.ManualAgent = true })
	if _, err := h.e.Submit("s_man", Request{Text: "x"}); !errors.Is(err, ErrNotAgent) {
		t.Fatalf("manual agent: %v", err)
	}
	// The agent exits (/exit) while the delivery waits behind a turn.
	c := h.add("s_a", readyClaude())
	c.set(func(v *session.DeliveryView) { v.AgentState = agentstate.Working })
	d, _ := h.e.Submit("s_a", Request{Text: "next task"})
	h.run(0, time.Second)
	if got := h.get(t, d.ID); got.State != Waiting || got.WaitReason != WaitWorking {
		t.Fatalf("%+v", got)
	}
	c.set(func(v *session.DeliveryView) {
		v.AgentState, v.Foreground = agentstate.Shell, agentstate.FgShell
		v.Lines, v.CursorY = []string{"alice@devbox:~/app$ "}, 0
	})
	h.e.Step(t0.Add(2 * time.Second))
	if got := h.get(t, d.ID); got.State != Failed || got.Code != "agent_exited" {
		t.Fatalf("%+v", got)
	}
	// The process check disagrees with the markers → never typed.
	c3 := h.add("s_c", readyClaude())
	c3.fgOK = false
	d3, _ := h.e.Submit("s_c", Request{Text: "x"})
	h.e.Step(t0)
	if got := h.get(t, d3.ID); got.State != Failed || got.Code != "agent_exited" {
		t.Fatalf("process check %+v", got)
	}
	for _, f := range []*fakeConsole{c, c3} {
		if f.written() != "" {
			t.Fatalf("bytes written: %q", f.written())
		}
	}
}

// TestDeliveryWaitsForNeedsYou: §19.2 — no byte while needsYou, sent
// after the permission was answered.
func TestDeliveryWaitsForNeedsYou(t *testing.T) {
	h := newHarness()
	c := h.add("s_a", readyClaude())
	perm, _ := os.ReadFile("../agent/testdata/dialogs/claude-permission.txt")
	_, body, _ := strings.Cut(string(perm), "\n")
	c.set(func(v *session.DeliveryView) {
		v.AgentState = agentstate.NeedsYou
		v.Lines, v.CursorY = strings.Split(body, "\n"), 17
	})
	d, _ := h.e.Submit("s_a", Request{Text: "after the permission"})
	h.run(0, 60*time.Second)
	if c.written() != "" {
		t.Fatalf("typed into the permission prompt: %q", c.written())
	}
	if got := h.get(t, d.ID); got.WaitReason != WaitNeedsYou {
		t.Fatalf("%+v", got)
	}
	c.set(func(v *session.DeliveryView) {
		v.AgentState = agentstate.Ready
		v.Lines, v.CursorY = append([]string(nil), claudeBox...), 3
	})
	h.run(61*time.Second, 62*time.Second)
	if !strings.HasSuffix(c.written(), "after the permission\x1b[201~\r") {
		t.Fatalf("not sent after the answer: %q", c.written())
	}
	// needsYouTimeoutSec runs out → expired.
	h.st.NeedsYouTimeoutSec = 30
	c2 := h.add("s_b", readyClaude())
	c2.set(func(v *session.DeliveryView) { v.AgentState = agentstate.NeedsYou })
	d2, _ := h.e.Submit("s_b", Request{Text: "x"})
	h.run(0, 31*time.Second)
	if got := h.get(t, d2.ID); got.State != Expired || got.Code != "needs_you_timeout" || c2.written() != "" {
		t.Fatalf("%+v %q", got, c2.written())
	}
}

// TestDeliveryRefusesDialogs feeds every dialog fixture (Codex update
// dialog with "Update now" as default, trust/login/API-key/model pickers,
// shell prompts, …) as the screen of a console whose state claims ready:
// nothing is ever written and the delivery fails with prompt_unrecognized.
func TestDeliveryRefusesDialogs(t *testing.T) {
	files, _ := filepath.Glob("../agent/testdata/dialogs/*.txt")
	if len(files) < 20 {
		t.Fatalf("fixtures: %d", len(files))
	}
	h := newHarness()
	h.st.PromptTimeoutSec = 120
	ids := map[string]string{}
	for i, f := range files {
		b, _ := os.ReadFile(f)
		head, body, _ := strings.Cut(string(b), "\n")
		var cx, cy int
		var rules string
		fmt.Sscanf(head, "cursor=%d,%d", &cx, &cy)
		if j := strings.Index(head, "rules="); j >= 0 {
			rules = strings.TrimSpace(head[j+6:])
		}
		sid := fmt.Sprintf("s_%d", i)
		c := h.add(sid, readyClaude())
		c.set(func(v *session.DeliveryView) {
			v.ScreenRules, v.Lines, v.CursorY = rules, strings.Split(body, "\n"), cy
		})
		d, err := h.e.Submit(sid, Request{Text: "1"}) // "1" would pick option 1
		if err != nil {
			t.Fatal(err)
		}
		ids[sid] = d.ID
	}
	h.run(0, 60*time.Second)
	for sid, id := range ids {
		if got := h.get(t, id); got.State != Waiting || got.WaitReason != WaitDialog {
			t.Fatalf("%s: %+v", sid, got)
		}
	}
	h.run(60*time.Second+100*time.Millisecond, 121*time.Second)
	for i, f := range files {
		sid := fmt.Sprintf("s_%d", i)
		got := h.get(t, ids[sid])
		if got.State != Failed || got.Code != "prompt_unrecognized" || !strings.Contains(got.Error, "not typed") {
			t.Fatalf("%s: %+v", filepath.Base(f), got)
		}
		if w := h.cons[sid].written(); w != "" {
			t.Fatalf("%s: bytes written %q", filepath.Base(f), w)
		}
	}
}

// TestDeliveryDialogBeforeReturn: a dialog that appears between the paste
// and the Return (e.g. an update prompt) gets no Return.
func TestDeliveryDialogBeforeReturn(t *testing.T) {
	h := newHarness()
	c := h.add("s_a", readyClaude())
	d, _ := h.e.Submit("s_a", Request{Text: "hello"})
	h.e.Step(t0)
	c.set(func(v *session.DeliveryView) {
		v.Lines, v.CursorY = []string{" Update available: 2.1.312 → 2.2.0", "", " ❯ 1. Update now", "   2. Skip"}, 2
	})
	h.e.Step(t0.Add(300 * time.Millisecond))
	if got := h.get(t, d.ID); got.State != Failed || got.Code != "submit_aborted" {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(c.written(), "\r") {
		t.Fatalf("Return sent into a dialog: %q", c.written())
	}
	// Human input after the paste also blocks the Return.
	c2 := h.add("s_b", readyClaude())
	d2, _ := h.e.Submit("s_b", Request{Text: "hello"})
	h.e.Step(t0)
	c2.set(func(v *session.DeliveryView) { v.LastHuman = t0.Add(50 * time.Millisecond) })
	h.e.Step(t0.Add(300 * time.Millisecond))
	if got := h.get(t, d2.ID); got.Code != "submit_aborted" || strings.Contains(c2.written(), "\r") {
		t.Fatalf("%+v %q", got, c2.written())
	}
}

// TestDeliveryHumanTyping: §19.4 — client input within 2 s delays the
// delivery; sustained typing for > 10 s gives up.
func TestDeliveryHumanTyping(t *testing.T) {
	h := newHarness()
	c := h.add("s_a", readyClaude())
	c.set(func(v *session.DeliveryView) { v.LastHuman = t0.Add(-time.Second) })
	d, _ := h.e.Submit("s_a", Request{Text: "x"})
	h.e.Step(t0)
	if got := h.get(t, d.ID); got.WaitReason != WaitHumanTyping || c.written() != "" {
		t.Fatalf("%+v", got)
	}
	h.run(100*time.Millisecond, 1500*time.Millisecond)
	if c.written() == "" {
		t.Fatal("not typed after the guard")
	}
	c2 := h.add("s_b", readyClaude())
	d2, _ := h.e.Submit("s_b", Request{Text: "x"})
	for ms := 0; ms <= 10500; ms += 100 {
		now := t0.Add(time.Duration(ms) * time.Millisecond)
		c2.set(func(v *session.DeliveryView) { v.LastHuman = now })
		h.e.Step(now)
	}
	if got := h.get(t, d2.ID); got.State != Expired || got.Code != "human_typing" || c2.written() != "" {
		t.Fatalf("%+v", got)
	}
}

// TestDeliveryQueueAndWhileWorking: FIFO, one active per session, queue
// limit, whileWorking "type", the quiet-screen rule.
func TestDeliveryQueueAndWhileWorking(t *testing.T) {
	h := newHarness()
	c := h.add("s_a", readyClaude())
	d1, _ := h.e.Submit("s_a", Request{Text: "first"})
	d2, _ := h.e.Submit("s_a", Request{Text: "second"})
	h.e.Step(t0)
	if !strings.Contains(c.written(), "first") || strings.Contains(c.written(), "second") {
		t.Fatalf("%q", c.written())
	}
	if h.get(t, d2.ID).State != Queued {
		t.Fatal("second must stay queued")
	}
	h.e.Step(t0.Add(300 * time.Millisecond)) // Return for the first
	c.set(func(v *session.DeliveryView) {
		v.AgentState, v.StateSince = agentstate.Working, t0.Add(400*time.Millisecond)
	})
	h.run(400*time.Millisecond, 5*time.Second)
	if h.get(t, d1.ID).State != Confirmed || strings.Contains(c.written(), "second") || h.get(t, d2.ID).WaitReason != WaitWorking {
		t.Fatalf("%+v %+v", h.get(t, d1.ID), h.get(t, d2.ID))
	}
	c.set(func(v *session.DeliveryView) { v.AgentState, v.LastOutput = agentstate.Ready, t0.Add(6*time.Second) })
	h.e.Step(t0.Add(6100 * time.Millisecond)) // output 100 ms ago: not quiet
	if strings.Contains(c.written(), "second") {
		t.Fatal("typed into a changing screen")
	}
	h.e.Step(t0.Add(6400 * time.Millisecond))
	if !strings.Contains(c.written(), "second") {
		t.Fatalf("second not typed %q", c.written())
	}
	// whileWorking "type": typed during the turn (Claude keeps its box).
	h.st.WhileWorking = "type"
	c2 := h.add("s_b", readyClaude())
	c2.set(func(v *session.DeliveryView) { v.AgentState = agentstate.Working })
	h.e.Submit("s_b", Request{Text: "while working"})
	h.run(0, 300*time.Millisecond)
	if !strings.HasSuffix(c2.written(), "\r") {
		t.Fatalf("type mode %q", c2.written())
	}
	h.st.WhileWorking = "queue"
	// Queue limit.
	c3 := h.add("s_c", readyClaude())
	c3.set(func(v *session.DeliveryView) { v.AgentState = agentstate.Working })
	for i := 0; i < MaxQueue; i++ {
		if _, err := h.e.Submit("s_c", Request{Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.e.Submit("s_c", Request{Text: "x"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("queue full: %v", err)
	}
	if h.e.ActiveCount("s_c") != MaxQueue {
		t.Fatal("count")
	}
	if _, err := h.e.Submit("s_c", Request{Text: strings.Repeat("x", MaxText+1)}); !errors.Is(err, ErrTextTooLong) {
		t.Fatal(err)
	}
	if _, err := h.e.Submit("s_c", Request{Text: " \x1b\x07 "}); !errors.Is(err, ErrEmptyText) {
		t.Fatal(err)
	}
}

// TestDeliveryPausedStartingReconnectingDeadline covers the remaining
// rows of the §2 table and cancel.
// TestDeliveryWakesAsleep: text for an asleep agent wakes it and is typed once it is ready
// (ROADMAP #50).
func TestDeliveryWakesAsleep(t *testing.T) {
	h := newHarness()
	c := h.add("s_a", readyClaude())
	c.set(func(v *session.DeliveryView) {
		v.Status, v.AgentState, v.Running = session.StatusAsleep, agentstate.Asleep, false
	})
	d, err := h.e.Submit("s_a", Request{Text: "look at the logs"})
	if err != nil {
		t.Fatal(err)
	}
	h.run(0, 2*time.Second)
	if got := h.get(t, d.ID); got.State != Waiting || got.WaitReason != WaitStarting || c.written() != "" || c.woken != 1 {
		t.Fatalf("woken %d %+v", c.woken, got)
	}
	c.set(func(v *session.DeliveryView) {
		v.Status, v.AgentState, v.Running = session.StatusRunning, agentstate.Ready, true
	})
	h.run(2100*time.Millisecond, 4*time.Second)
	if !strings.Contains(c.written(), "look at the logs") || !strings.HasSuffix(c.written(), "\r") || c.woken != 1 {
		t.Fatalf("woken %d, written %q", c.woken, c.written())
	}
}

func TestDeliveryPausedStartingReconnectingDeadline(t *testing.T) {
	h := newHarness()
	c := h.add("s_a", readyClaude())
	c.set(func(v *session.DeliveryView) { v.Paused = true })
	d, _ := h.e.Submit("s_a", Request{Text: "x"})
	h.run(0, 10*time.Second)
	if got := h.get(t, d.ID); got.WaitReason != WaitPaused || c.written() != "" {
		t.Fatalf("%+v", got)
	}
	c.set(func(v *session.DeliveryView) { v.Paused = false })
	h.run(10100*time.Millisecond, 10500*time.Millisecond)
	if !strings.HasSuffix(c.written(), "\r") {
		t.Fatal("not sent after resume")
	}
	h.st.StartingTimeoutSec = 60
	c2 := h.add("s_b", readyClaude())
	c2.set(func(v *session.DeliveryView) { v.AgentState = agentstate.Starting })
	d2, _ := h.e.Submit("s_b", Request{Text: "x"})
	h.run(0, 61*time.Second)
	if got := h.get(t, d2.ID); got.State != Expired || got.Code != "starting_timeout" {
		t.Fatalf("%+v", got)
	}
	c3 := h.add("s_c", readyClaude())
	c3.set(func(v *session.DeliveryView) {
		v.Status, v.AgentState = session.StatusReconnecting, agentstate.Reconnecting
	})
	d3, _ := h.e.Submit("s_c", Request{Text: "x", DeadlineSec: 20})
	h.run(0, 10*time.Second)
	if got := h.get(t, d3.ID); got.WaitReason != WaitReconnecting {
		t.Fatalf("%+v", got)
	}
	h.run(10*time.Second, 21*time.Second)
	if got := h.get(t, d3.ID); got.State != Expired || got.Code != "deadline" {
		t.Fatalf("%+v", got)
	}
	// Cancel: queued → cancelled; typed → already_sent.
	c4 := h.add("s_d", readyClaude())
	c4.set(func(v *session.DeliveryView) { v.AgentState = agentstate.Working })
	d4, _ := h.e.Submit("s_d", Request{Text: "x"})
	if got, err := h.e.Cancel(d4.ID); err != nil || got.State != Cancelled {
		t.Fatalf("%v %+v", err, got)
	}
	h.add("s_e", readyClaude())
	d5, _ := h.e.Submit("s_e", Request{Text: "x"})
	h.e.Step(t0)
	if _, err := h.e.Cancel(d5.ID); !errors.Is(err, ErrAlreadySent) {
		t.Fatalf("cancel typed: %v", err)
	}
	// Session deleted.
	c6 := h.add("s_f", readyClaude())
	c6.set(func(v *session.DeliveryView) { v.AgentState = agentstate.Working })
	d6, _ := h.e.Submit("s_f", Request{Text: "x"})
	h.e.SessionRemoved("s_f")
	if got := h.get(t, d6.ID); got.State != Failed || got.Code != "session_gone" {
		t.Fatalf("%+v", got)
	}
	// Dictation guard.
	c7 := h.add("s_g", readyClaude())
	c7.set(func(v *session.DeliveryView) { v.AgentState = agentstate.NeedsYou })
	if _, err := h.e.Submit("s_g", Request{Text: "x", Guard: "dictation"}); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("dictation: %v", err)
	}
	c7.set(func(v *session.DeliveryView) { v.AgentState = agentstate.Ready })
	d7, err := h.e.Submit("s_g", Request{Text: "dictated", Guard: "dictation"})
	if err != nil || d7.Submit {
		t.Fatalf("%v %+v", err, d7)
	}
	h.e.Step(t0)
	if got := h.get(t, d7.ID); got.State != Sent || strings.Contains(c7.written(), "\r") {
		t.Fatalf("dictation %+v %q", got, c7.written())
	}
	if len(h.e.List("s_g")) != 1 || len(h.e.ListAll("p_test", true)) == 0 {
		t.Fatal("list")
	}
}
