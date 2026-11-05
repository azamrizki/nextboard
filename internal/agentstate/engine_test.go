package agentstate

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// fixture builds an engine of a running, wrapped agent that sent agent-start.
func running(cfg Config, live string) (*Engine, *time.Time) {
	now := t0
	e := New(cfg, now)
	e.NewRun("fresh", live, now)
	e.SetProc(ProcRunning, now)
	e.AgentStart(now)
	return e, &now
}

func (e *Engine) mustBe(t *testing.T, state, src string) {
	t.Helper()
	s := e.Snapshot()
	if s.AgentState != state || (src != "" && s.StateSource != src) {
		t.Fatalf("state %s/%s, want %s/%s (activity %q)", s.AgentState, s.StateSource, state, src, s.Activity)
	}
}

func adv(now *time.Time, d time.Duration) time.Time {
	*now = now.Add(d)
	return *now
}

func TestProcessPriority(t *testing.T) {
	e := New(Config{}, t0)
	e.mustBe(t, Starting, SrcProcess)
	e.SetProc(ProcRunning, t0)
	e.AgentStart(t0)
	e.Hook(Hook{Event: "SessionStart"}, t0)
	e.mustBe(t, Ready, SrcHooks)
	for _, c := range []struct{ proc, want string }{
		{ProcReconnecting, Reconnecting}, {ProcExited, Exited}, {ProcError, Error}, {ProcStarting, Starting}, {ProcRunning, Ready},
	} {
		e.SetProc(c.proc, t0)
		e.mustBe(t, c.want, "")
	}
	// kind shell is always shell (and ignores agent markers).
	sh := New(Config{Shell: true}, t0)
	sh.SetProc(ProcRunning, t0)
	sh.AgentStart(t0)
	sh.Hook(Hook{Event: "UserPromptSubmit"}, t0)
	sh.mustBe(t, Shell, SrcProcess)
	if sh.Snapshot().Foreground != FgShell {
		t.Fatal("shell foreground")
	}
}

func TestHooksTransitions(t *testing.T) {
	e, now := running(Config{HasRules: true}, LivePending)
	e.mustBe(t, Starting, SrcWrapper) // T1
	if e.Snapshot().LiveStatus != LivePending || e.Snapshot().Foreground != FgAgent {
		t.Fatalf("%+v", e.Snapshot())
	}
	e.Hook(Hook{Event: "SessionStart"}, adv(now, time.Second)) // T2
	e.mustBe(t, Ready, SrcHooks)
	if e.Snapshot().LiveStatus != LiveHooks {
		t.Fatal("liveStatus not hooks after first hook")
	}
	e.Hook(Hook{Event: "UserPromptSubmit"}, adv(now, time.Second)) // T3
	e.mustBe(t, Working, SrcHooks)
	if e.Snapshot().Activity != "Thinking" || e.Snapshot().LastTurn == nil || e.Snapshot().LastTurn.EndedAt != nil {
		t.Fatalf("%+v", e.Snapshot())
	}
	e.Hook(Hook{Event: "PreToolUse", ToolName: "Bash", ToolSummary: "npm test\nmore"}, adv(now, time.Second)) // T4
	if a := e.Snapshot().Activity; a != "Bash: npm test …" {
		t.Fatalf("activity %q", a)
	}
	e.Hook(Hook{Event: "Notification", NotificationType: "permission_prompt", Message: "Claude needs your permission to use Bash"}, adv(now, time.Second)) // T7
	e.mustBe(t, NeedsYou, SrcHooks)
	s := e.Snapshot()
	// The tool of the preceding PreToolUse names the permission.
	if s.Activity != "Permission: Bash(npm test …)" || s.NeedsYouSince == nil || !s.NeedsYouSince.Equal(*now) {
		t.Fatalf("%+v", s)
	}
	// Input, timers and a dialog on screen do not change a hook state.
	e.Screen(Screen{NeedsYou: "Do you want to proceed?"}, adv(now, 5*time.Second))
	e.Input([]byte("\r"), *now)
	e.Tick(adv(now, 70*time.Second))
	e.mustBe(t, NeedsYou, SrcHooks)
	e.Hook(Hook{Event: "PostToolUse", ToolName: "Bash", ToolSummary: "npm test"}, adv(now, time.Second)) // T6
	e.mustBe(t, Working, SrcHooks)
	if e.Snapshot().NeedsYouSince != nil || e.Snapshot().Activity != "Bash: npm test" {
		t.Fatalf("%+v", e.Snapshot())
	}
	e.Hook(Hook{Event: "PreToolUse", ToolName: "AskUserQuestion"}, adv(now, time.Second)) // T5
	if e.mustBe(t, NeedsYou, ""); e.Snapshot().Activity != "Question" {
		t.Fatal(e.Snapshot().Activity)
	}
	e.Hook(Hook{Event: "PreToolUse", ToolName: "ExitPlanMode"}, *now)
	if e.Snapshot().Activity != "Plan approval" {
		t.Fatal(e.Snapshot().Activity)
	}
	e.Hook(Hook{Event: "SubagentStop"}, *now) // T10
	if e.mustBe(t, NeedsYou, ""); e.Snapshot().Activity != "Subagent finished" {
		t.Fatal("T10")
	}
	e.TakeEvents()
	e.Hook(Hook{Event: "Stop"}, adv(now, 2*time.Second)) // T9
	e.mustBe(t, Ready, SrcHooks)
	ev := e.TakeEvents()
	if len(ev) != 1 || ev[0].Kind != EvFinished || ev[0].Turn.DurationMs != 81000 {
		t.Fatalf("events %+v", ev)
	}
	if lt := e.Snapshot().LastTurn; lt == nil || lt.EndedAt == nil || lt.DurationMs != 81000 {
		t.Fatalf("lastTurn %+v", lt)
	}
	e.Hook(Hook{Event: "SessionEnd"}, *now) // T11
	e.mustBe(t, Ready, SrcHooks)
	rc := 0
	e.AgentExit(rc, adv(now, time.Second)) // T12
	e.mustBe(t, Shell, SrcWrapper)
	if s := e.Snapshot(); s.Foreground != FgShell || s.AgentExitCode == nil || *s.AgentExitCode != 0 {
		t.Fatalf("%+v", s)
	}
	e.AgentStart(adv(now, time.Second)) // T1 again from shell
	e.mustBe(t, Starting, SrcWrapper)
	if e.Snapshot().LiveStatus != LivePending || e.Snapshot().AgentExitCode != nil {
		t.Fatalf("%+v", e.Snapshot())
	}
}

func TestIdleNotificationOnlyClosesWorking(t *testing.T) { // T8 (lead decision: idle is not needsYou)
	e, now := running(Config{}, LivePending)
	e.Hook(Hook{Event: "SessionStart"}, *now)
	e.Hook(Hook{Event: "Notification", NotificationType: "idle_prompt", Message: "Claude is waiting for your input"}, *now)
	e.mustBe(t, Ready, SrcHooks)
	e.Hook(Hook{Event: "UserPromptSubmit"}, adv(now, time.Second))
	e.Hook(Hook{Event: "Notification", Message: "Claude is waiting for your input"}, adv(now, time.Second))
	e.mustBe(t, Ready, SrcHooks)
	// Message heuristic for permission without a type.
	e.Hook(Hook{Event: "Notification", Message: "Claude needs your permission to use Edit"}, *now)
	e.mustBe(t, NeedsYou, SrcHooks)
	e.Hook(Hook{Event: "Notification", NotificationType: "idle_prompt"}, *now)
	e.mustBe(t, NeedsYou, SrcHooks) // idle never leaves needsYou
}

func TestCodexNotifyAndScreen(t *testing.T) {
	e, now := running(Config{Notify: true, HasRules: true}, LivePending)
	// Before any notify: the screen may bring it to ready (T17 from starting).
	e.Output(*now)
	e.Screen(Screen{Ready: true}, adv(now, time.Second))
	e.mustBe(t, Starting, "") // output not quiet for 1.5 s yet
	e.Screen(Screen{Ready: true}, adv(now, time.Second))
	e.mustBe(t, Ready, SrcScreen)
	e.Input([]byte("hi"), *now)
	e.mustBe(t, Ready, "")
	e.Input([]byte("\r"), adv(now, time.Second)) // T14
	e.mustBe(t, Working, SrcActivity)
	e.Notify("agent-turn-complete", adv(now, 3*time.Second)) // T13
	e.mustBe(t, Ready, SrcNotify)
	if e.Snapshot().LiveStatus != LiveNotify {
		t.Fatal("liveStatus")
	}
	e.Input([]byte("do it\r"), adv(now, time.Second))
	e.Output(*now)
	// needsYou from screen needs 300 ms of stability (T15).
	e.Screen(Screen{NeedsYou: "Would you like to run the following command?"}, adv(now, 100*time.Millisecond))
	e.mustBe(t, Working, "")
	e.Screen(Screen{NeedsYou: "Would you like to run the following command?"}, adv(now, 300*time.Millisecond))
	e.mustBe(t, NeedsYou, SrcScreen)
	if e.Snapshot().Activity != "Would you like to run the following command?" {
		t.Fatal(e.Snapshot().Activity)
	}
	e.Screen(Screen{Working: true}, adv(now, time.Second)) // T16
	e.mustBe(t, Working, SrcScreen)
	e.Screen(Screen{Ready: true}, adv(now, 5*time.Second)) // screen-set working may end at the prompt
	e.mustBe(t, Ready, SrcScreen)
	// Once notify works, the screen no longer ends a turn started by input.
	e.Input([]byte("go\r"), adv(now, time.Second))
	e.Screen(Screen{Ready: true}, adv(now, 5*time.Second))
	e.mustBe(t, Working, SrcActivity)
	e.Notify("agent-turn-complete", *now)
	e.mustBe(t, Ready, SrcNotify)
}

func TestScreenRulesMode(t *testing.T) {
	e, now := running(Config{HasRules: true}, LiveScreen)
	e.Output(*now)
	e.Screen(Screen{Ready: true}, adv(now, 2*time.Second)) // T17
	e.mustBe(t, Ready, SrcScreen)
	e.Screen(Screen{Working: true}, adv(now, time.Second))
	e.Screen(Screen{Working: true}, adv(now, 400*time.Millisecond)) // screen working (stable)
	e.mustBe(t, Working, SrcScreen)
	e.Screen(Screen{Ready: true}, adv(now, 2*time.Second))
	e.mustBe(t, Ready, SrcScreen)
	e.Screen(Screen{NeedsYou: "Allow execution?"}, adv(now, time.Second))
	e.Screen(Screen{NeedsYou: "Allow execution?"}, adv(now, 400*time.Millisecond)) // T15 from ready
	e.mustBe(t, NeedsYou, SrcScreen)
	ev := e.TakeEvents()
	if len(ev) == 0 || ev[len(ev)-1].Kind != EvNeedsYou || ev[len(ev)-1].Activity != "Allow execution?" {
		t.Fatalf("events %+v", ev)
	}
	e.Screen(Screen{}, adv(now, time.Second)) // T16
	e.mustBe(t, Working, SrcScreen)
	// Startup dialogs (trust prompt) are needsYou too.
	f, now2 := running(Config{HasRules: true}, LiveScreen)
	f.Screen(Screen{NeedsYou: "Do you trust the files in this folder?"}, *now2)
	f.Screen(Screen{NeedsYou: "Do you trust the files in this folder?"}, adv(now2, 300*time.Millisecond))
	f.mustBe(t, NeedsYou, SrcScreen)
}

func TestActivityMode(t *testing.T) { // T18 without screen rules
	e, now := running(Config{}, LiveScreen)
	e.Output(adv(now, 500*time.Millisecond))
	e.Tick(adv(now, 500*time.Millisecond))
	e.mustBe(t, Working, SrcActivity)
	e.Tick(adv(now, 2*time.Second))
	e.mustBe(t, Working, "")
	e.Tick(adv(now, 1500*time.Millisecond))
	e.mustBe(t, Ready, SrcActivity)
	e.Input([]byte("\r"), *now) // T14 still applies
	e.mustBe(t, Working, SrcActivity)
}

// A Return starts a turn even when the agent prints nothing for a while: the quiet
// before it says nothing about the turn. It ended at the next tick.
func TestActivityModeSubmitInTheQuiet(t *testing.T) {
	e, now := running(Config{}, LiveScreen)
	e.Tick(adv(now, 10*time.Second))
	e.mustBe(t, Ready, SrcActivity)
	e.Input([]byte("go\r"), *now)
	e.mustBe(t, Working, SrcActivity)
	e.Tick(adv(now, 250*time.Millisecond))
	e.mustBe(t, Working, SrcActivity)
	e.Tick(adv(now, 3*time.Second))
	e.mustBe(t, Ready, SrcActivity)
}

func TestGenericScreenSilentThinking(t *testing.T) {
	e, now := running(Config{}, LiveScreen)
	e.GenericScreen(Screen{Ready: true}, adv(now, 10*time.Second))
	e.Tick(*now)
	e.mustBe(t, Ready, SrcActivity)
	e.Input([]byte("\r"), *now)
	e.Output(adv(now, 100*time.Millisecond)) // the echo

	// No output and no input prompt: it may be thinking.
	e.GenericScreen(Screen{}, adv(now, 5*time.Second))
	e.Tick(*now)
	e.mustBe(t, Working, SrcActivity)
	// But not for ever: a CLI whose prompt we do not know must still end.
	e.Tick(adv(now, UnknownIdleAfter))
	e.mustBe(t, Ready, SrcActivity)
}

func TestGenericScreenPromptEndsTheTurnAtOnce(t *testing.T) {
	e, now := running(Config{}, LiveScreen)
	e.Output(adv(now, time.Second))
	e.Tick(*now)
	e.mustBe(t, Working, SrcActivity)
	e.GenericScreen(Screen{Ready: true}, adv(now, IdleAfter))
	e.Tick(*now)
	e.mustBe(t, Ready, SrcActivity)
}

func TestGenericScreenWorkingHintOutlastsTheQuiet(t *testing.T) {
	e, now := running(Config{}, LiveScreen)
	e.GenericScreen(Screen{Working: true}, adv(now, time.Minute))
	e.Tick(*now)
	e.mustBe(t, Working, SrcActivity)
	e.Tick(adv(now, 5*time.Minute))
	e.mustBe(t, Working, SrcActivity)
	e.GenericScreen(Screen{Ready: true}, adv(now, time.Second))
	e.Tick(*now)
	e.mustBe(t, Ready, SrcActivity)
}

func TestGenericScreenDialog(t *testing.T) {
	e, now := running(Config{}, LiveScreen)
	e.Output(adv(now, time.Second))
	e.Tick(*now)
	e.mustBe(t, Working, SrcActivity)

	// Text still arriving: prose in a reply, not a dialog.
	q := Screen{NeedsYou: "Do you want me to go on?"}
	e.GenericScreen(q, adv(now, StableFor))
	e.Output(adv(now, time.Second))
	e.GenericScreen(q, *now)
	e.mustBe(t, Working, SrcActivity)

	// It stands still.
	e.GenericScreen(Screen{NeedsYou: "Apply? (y/n)"}, adv(now, ReadyQuiet))
	e.GenericScreen(Screen{NeedsYou: "Apply? (y/n)"}, adv(now, StableFor))
	e.mustBe(t, NeedsYou, SrcScreen)
	if ev := e.TakeEvents(); len(ev) == 0 || ev[len(ev)-1].Kind != EvNeedsYou {
		t.Fatalf("no needsYou event: %+v", ev)
	}
	// Waiting is not idle: the timer must not call it ready.
	e.Tick(adv(now, time.Hour))
	e.mustBe(t, NeedsYou, SrcScreen)

	// Answered.
	e.GenericScreen(Screen{}, adv(now, time.Second))
	e.mustBe(t, Working, SrcScreen)
	e.GenericScreen(Screen{Ready: true}, adv(now, time.Second))
	e.Tick(*now)
	e.mustBe(t, Ready, SrcActivity)
}

// A line-based CLI leaves the question on the screen. Return answers it, and the
// same text is not a new question until something else has been on the screen.
func TestGenericScreenAnsweredQuestionStaysDrawn(t *testing.T) {
	e, now := running(Config{}, LiveScreen)
	q := Screen{NeedsYou: "Do you want me to go on?"}
	e.GenericScreen(q, adv(now, 5*time.Second))
	e.GenericScreen(q, adv(now, StableFor))
	e.mustBe(t, NeedsYou, SrcScreen)

	e.Input([]byte("y\r"), adv(now, time.Second))
	e.mustBe(t, Working, SrcActivity)
	e.GenericScreen(q, adv(now, 3*time.Second))
	e.GenericScreen(q, adv(now, 3*time.Second))
	e.mustBe(t, Working, SrcActivity)

	e.GenericScreen(Screen{}, adv(now, time.Second))
	e.GenericScreen(q, adv(now, 3*time.Second))
	e.GenericScreen(q, adv(now, StableFor))
	e.mustBe(t, NeedsYou, SrcScreen)
}

// Screen rules and hooks stay in charge where they exist.
func TestGenericScreenIgnoredElsewhere(t *testing.T) {
	e, now := running(Config{HasRules: true}, LiveScreen)
	e.Screen(Screen{Ready: true}, adv(now, 5*time.Second))
	before := e.Snapshot().AgentState
	e.GenericScreen(Screen{NeedsYou: "Apply? (y/n)"}, adv(now, time.Minute))
	e.GenericScreen(Screen{NeedsYou: "Apply? (y/n)"}, adv(now, time.Minute))
	if got := e.Snapshot().AgentState; got != before {
		t.Errorf("rules mode: %s, then %s", before, got)
	}
}

func TestStartingTimeout(t *testing.T) { // T19
	e, now := running(Config{HasRules: true}, LivePending)
	e.Tick(adv(now, 59*time.Second))
	e.mustBe(t, Starting, "")
	e.Tick(adv(now, 2*time.Second))
	e.mustBe(t, Ready, SrcActivity)
}

func TestShellModeAndManualAgent(t *testing.T) {
	e := New(Config{HasRules: true}, t0)
	e.NewRun("shell", LivePending, t0)
	e.SetProc(ProcRunning, t0)
	e.mustBe(t, Shell, SrcProcess)
	e.Input([]byte("\r"), t0)
	e.Screen(Screen{NeedsYou: "x"}, t0.Add(time.Second))
	e.mustBe(t, Shell, "")
	e.SetForeground(FgAgent, true, t0.Add(2*time.Second))
	if s := e.Snapshot(); !s.ManualAgent || s.Foreground != FgAgent || s.AgentState != Starting {
		t.Fatalf("%+v", s)
	}
	e.SetForeground(FgShell, false, t0.Add(3*time.Second))
	e.mustBe(t, Shell, "")
}

func TestReattachUsesScreen(t *testing.T) {
	e, now := running(Config{HasRules: true}, LivePending)
	e.Hook(Hook{Event: "SessionStart"}, *now)
	e.Reattach(FgAgent, adv(now, time.Second))
	e.mustBe(t, Starting, SrcScreen)
	if e.HooksActive() || e.Snapshot().LiveStatus != LivePending {
		t.Fatal("hooks still active after reattach")
	}
	e.Screen(Screen{Ready: true}, adv(now, 2*time.Second))
	e.mustBe(t, Ready, SrcScreen)
	e.Reattach(FgShell, *now)
	e.mustBe(t, Shell, "")
}

func TestActivityClipped(t *testing.T) {
	e, now := running(Config{}, LivePending)
	long := ""
	for i := 0; i < 50; i++ {
		long += "abcd "
	}
	e.Hook(Hook{Event: "Notification", NotificationType: "permission_prompt", Message: long}, *now)
	if n := len([]rune(e.Snapshot().Activity)); n != ActivityMaxLen {
		t.Fatalf("activity %d runes", n)
	}
	e.Hook(Hook{Event: "PreToolUse", ToolName: "Edit", ToolSummary: long}, *now)
	if n := len([]rune(e.Snapshot().Activity)); n > len("Edit: ")+80 {
		t.Fatalf("tool activity %d runes", n)
	}
}

func TestErrorEvent(t *testing.T) {
	e, _ := running(Config{}, LivePending)
	e.TakeEvents()
	e.SetProc(ProcError, t0)
	ev := e.TakeEvents()
	if len(ev) != 1 || ev[0].Kind != EvError {
		t.Fatalf("%+v", ev)
	}
	e.AgentExit(2, t0)
	if ev := e.TakeEvents(); len(ev) != 1 || ev[0].Kind != EvAgentExit || ev[0].Rc != 2 {
		t.Fatalf("%+v", ev)
	}
}

// Verified on Claude Code 2.1.312: the permission Notification comes ~6 s
// after the dialog, and denying with Esc fires no hook.
func TestHooksScreenFallback(t *testing.T) {
	e, now := running(Config{HasRules: true}, LivePending)
	e.Hook(Hook{Event: "SessionStart"}, *now)
	e.Hook(Hook{Event: "UserPromptSubmit"}, adv(now, time.Second))
	e.Hook(Hook{Event: "PreToolUse", ToolName: "Write", ToolSummary: "/work/app/nb.txt"}, adv(now, time.Second))
	e.Output(*now)
	// Ready prompt alone never ends a hook-driven turn.
	e.Screen(Screen{Ready: true}, adv(now, 3*time.Second))
	e.mustBe(t, Working, SrcHooks)
	e.Screen(Screen{NeedsYou: "Do you want to create nb.txt?"}, adv(now, 100*time.Millisecond))
	e.Screen(Screen{NeedsYou: "Do you want to create nb.txt?"}, adv(now, 300*time.Millisecond))
	e.mustBe(t, NeedsYou, SrcScreen)
	e.Hook(Hook{Event: "Notification", NotificationType: "permission_prompt", Message: "Claude needs your permission"}, adv(now, 5*time.Second))
	e.mustBe(t, NeedsYou, SrcHooks)
	if a := e.Snapshot().Activity; a != "Permission: Write(nb.txt)" {
		t.Fatalf("activity %q", a)
	}
	// Esc: the dialog disappears, the empty prompt is back, no hook comes.
	e.Output(adv(now, time.Second))
	e.Screen(Screen{Ready: true}, adv(now, time.Second))
	e.mustBe(t, NeedsYou, "") // not quiet yet
	e.TakeEvents()
	e.Screen(Screen{Ready: true}, adv(now, time.Second))
	e.mustBe(t, Ready, SrcScreen)
	if ev := e.TakeEvents(); len(ev) != 1 || ev[0].Kind != EvFinished {
		t.Fatalf("events %+v", ev)
	}
	// Hooks stay in charge afterwards.
	e.Hook(Hook{Event: "UserPromptSubmit"}, adv(now, time.Second))
	e.mustBe(t, Working, SrcHooks)
}

// Verified on Codex 0.163.0: the TUI may still show its working line for a
// moment after notify; that must not reopen the turn.
func TestCodexStaleWorkingAfterNotify(t *testing.T) {
	e, now := running(Config{Notify: true, HasRules: true}, LivePending)
	e.Screen(Screen{Ready: true}, adv(now, 2*time.Second))
	e.Input([]byte("\r"), adv(now, time.Second))
	e.Screen(Screen{Working: true}, adv(now, time.Second))
	e.Notify("agent-turn-complete", adv(now, 2*time.Second))
	e.mustBe(t, Ready, SrcNotify)
	e.Screen(Screen{Working: true}, adv(now, 100*time.Millisecond)) // stale frame
	e.Screen(Screen{Ready: true}, adv(now, 150*time.Millisecond))
	e.mustBe(t, Ready, SrcNotify)
	// A screen-set working still ends at the prompt (no notify follows).
	e.Screen(Screen{Working: true}, adv(now, time.Second))
	e.Screen(Screen{Working: true}, adv(now, 400*time.Millisecond))
	e.mustBe(t, Working, SrcScreen)
	e.Screen(Screen{Ready: true}, adv(now, 2*time.Second))
	e.mustBe(t, Ready, SrcScreen)
}

// TestHookedPermissionStaleScreen: with hooks live, a permission dialog
// that a hook already resolved but that is still drawn (and repainted,
// matching different lines) must not raise needsYou again nor emit a
// second needsYou event (§6.2 rule 3; web e2e regression).
func TestHookedPermissionStaleScreen(t *testing.T) {
	e, now := running(Config{HasRules: true}, LivePending)
	e.Hook(Hook{Event: "SessionStart"}, *now)
	e.Hook(Hook{Event: "UserPromptSubmit"}, adv(now, time.Second))
	e.Hook(Hook{Event: "PreToolUse", ToolName: "Bash", ToolSummary: "npm test"}, adv(now, 100*time.Millisecond))
	e.TakeEvents()
	dlg := Screen{NeedsYou: "Do you want to proceed?"}
	e.Screen(dlg, adv(now, 100*time.Millisecond))
	e.Screen(dlg, adv(now, 300*time.Millisecond)) // T15h
	e.mustBe(t, NeedsYou, SrcScreen)
	e.Hook(Hook{Event: "Notification", NotificationType: "permission_prompt", Message: "Claude needs your permission"}, adv(now, 5*time.Second))
	e.mustBe(t, NeedsYou, SrcHooks)
	if ev := e.TakeEvents(); len(ev) != 1 || ev[0].Kind != EvNeedsYou {
		t.Fatalf("first needsYou events %+v", ev)
	}
	e.Hook(Hook{Event: "PostToolUse", ToolName: "Bash", ToolSummary: "npm test"}, adv(now, 2*time.Second))
	e.mustBe(t, Working, SrcHooks)
	// The answered dialog is still on screen, repainted twice.
	for i, l := range []string{"Do you want to proceed?", "❯ 1. Yes", "Enter to confirm · Esc to cancel"} {
		e.Screen(Screen{NeedsYou: l}, adv(now, 400*time.Millisecond))
		e.Screen(Screen{NeedsYou: l}, adv(now, 400*time.Millisecond))
		if s := e.Snapshot(); s.AgentState != Working {
			t.Fatalf("stale screen #%d re-raised: %+v", i, s)
		}
	}
	if ev := e.TakeEvents(); len(ev) != 0 {
		t.Fatalf("duplicate events %+v", ev)
	}
	// Once the screen was clear, a new dialog raises needsYou again.
	e.Screen(Screen{Working: true}, adv(now, 400*time.Millisecond))
	e.Hook(Hook{Event: "PreToolUse", ToolName: "Write", ToolSummary: "/a/b.txt"}, adv(now, time.Second))
	e.Screen(dlg, adv(now, 100*time.Millisecond))
	e.Screen(dlg, adv(now, 300*time.Millisecond))
	e.mustBe(t, NeedsYou, SrcScreen)
}

// TestTrustDialogIsNeedsYou: the folder-trust dialog (Enter selects "No,
// exit") is needsYou with activity "Trust folder?" — before any hook and
// also when SessionStart already fired.
func TestTrustDialogIsNeedsYou(t *testing.T) {
	trust := Screen{NeedsYou: "Trust folder?", Startup: true}
	e, now := running(Config{HasRules: true}, LivePending)
	e.Screen(trust, *now)
	e.Screen(trust, adv(now, 300*time.Millisecond))
	e.mustBe(t, NeedsYou, SrcScreen)
	if s := e.Snapshot(); s.Activity != "Trust folder?" {
		t.Fatalf("activity %q", s.Activity)
	}
	f, now2 := running(Config{HasRules: true}, LivePending)
	f.Hook(Hook{Event: "SessionStart"}, *now2)
	f.mustBe(t, Ready, SrcHooks)
	f.Screen(Screen{NeedsYou: "Do you want to proceed?"}, adv(now2, 100*time.Millisecond))
	f.Screen(Screen{NeedsYou: "Do you want to proceed?"}, adv(now2, 400*time.Millisecond))
	f.mustBe(t, Ready, SrcHooks) // a non-startup line outside a turn is ignored with hooks
	f.Screen(trust, adv(now2, 100*time.Millisecond))
	f.Screen(trust, adv(now2, 400*time.Millisecond))
	f.mustBe(t, NeedsYou, SrcScreen)
}
