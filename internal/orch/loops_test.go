package orch

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/session"
)

func TestCron(t *testing.T) {
	c, err := ParseCron("*/15 9-18 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	mon := time.Date(2026, 10, 26, 8, 59, 0, 0, time.UTC) // Monday
	if got := c.Next(mon); !got.Equal(time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)) {
		t.Fatal(got)
	}
	fri := time.Date(2026, 10, 30, 18, 50, 0, 0, time.UTC)
	if got := c.Next(fri); !got.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekend skip %v", got)
	}
	if got := c.Next(time.Date(2026, 10, 26, 9, 7, 30, 0, time.UTC)); got.Minute() != 15 {
		t.Fatal(got)
	}
	// Both day fields restricted: either matches.
	d, _ := ParseCron("0 0 1 * sun")
	if got := d.Next(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("dom|dow %v", got)
	}
	for _, bad := range []string{"* * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "5-1 * * * *", "x * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if r := c.RunsPerHour(mon); r < 1.18 || r > 1.2 { // 4/h × 10 h × 5 d / 168 h
		t.Fatalf("runs/h %v", r)
	}
}

func TestLoopValidation(t *testing.T) {
	ok := LoopDef{Text: "run tests", Trigger: LoopTrigger{Kind: "interval", EverySec: 900}, Targets: LoopTargets{Kind: "allAgents"}}
	if err := ValidateLoop(&ok, false); err != nil || ok.CreatedBy != "user" {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(l *LoopDef){
		"interval < 60":  func(l *LoopDef) { l.Trigger.EverySec = 30 },
		"interval > 24h": func(l *LoopDef) { l.Trigger.EverySec = 90000 },
		"bad cron":       func(l *LoopDef) { l.Trigger = LoopTrigger{Kind: "cron", Expr: "every day"} },
		"short pattern":  func(l *LoopDef) { l.Trigger = LoopTrigger{Kind: "output", Pattern: "x"} },
		"bad regex":      func(l *LoopDef) { l.Trigger = LoopTrigger{Kind: "output", Pattern: "(ab", Regex: true} },
		"maxRuns":        func(l *LoopDef) { l.Stop.MaxRuns = 501 },
		"duration":       func(l *LoopDef) { l.Stop.MaxDurationSec = 100 },
		"no text":        func(l *LoopDef) { l.Text = " " },
		"targets":        func(l *LoopDef) { l.Targets = LoopTargets{Kind: "handles"} },
		"goal agent":     func(l *LoopDef) { l.Stop.Goal = &LoopGoal{Kind: "agentJudged"} },
	} {
		l := ok
		l.Stop = LoopStop{}
		mut(&l)
		if err := ValidateLoop(&l, false); !errors.Is(err, ErrBadLoop) {
			t.Errorf("%s accepted", name)
		}
	}
	cmdGoal := ok
	cmdGoal.Stop.Goal = &LoopGoal{Kind: "command", Command: "npm test"}
	if err := ValidateLoop(&cmdGoal, true); !errors.Is(err, ErrBadLoop) {
		t.Fatal("planner may not create a goal command")
	}
	cmdGoal.CreatedBy = "order"
	if err := ValidateLoop(&cmdGoal, false); !errors.Is(err, ErrBadLoop) {
		t.Fatal("orders may not create a goal command")
	}
	cmdGoal.CreatedBy = "user"
	if err := ValidateLoop(&cmdGoal, false); err != nil || cmdGoal.Stop.Goal.TimeoutSec != 300 {
		t.Fatal(err)
	}
}

func loopHist(s *Service, id string) []LoopHistoryEntry { return s.Loops.History(id, 50) }

// TestLoopIntervalRunsWithoutClients: a run delivers to free agents,
// skips needsYou (loopHeld) and paused consoles, retries once when it
// reaches nobody, stops at maxRuns (loopEnded), and survives a restart
// without repeating missed runs.
func TestLoopIntervalRunsWithoutClients(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.NeedsYou)
	cas := h.console(p, wid, "Cass", "codex", agentstate.Ready)
	h.mu.Lock()
	h.sessions[cas.ID].Paused = true
	h.mu.Unlock()
	h.console(p, wid, "Dov", "shell", "")
	l, err := s.Loops.Create(LoopDef{ProjectID: p.ID, WorkspaceID: wid, Name: "Test guard", Enabled: true, Text: "Run the tests.",
		Trigger: LoopTrigger{Kind: "interval", EverySec: 60}, Targets: LoopTargets{Kind: "allAgents"}, Stop: LoopStop{MaxRuns: 2}}, t0, false)
	if err != nil || l.State.Status != "running" || !l.State.NextRunAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("%v %+v", err, l.State)
	}
	s.Loops.Tick(t0.Add(59 * time.Second))
	if len(h.delReqs) != 0 {
		t.Fatal("ran early")
	}
	s.Loops.Tick(t0.Add(60 * time.Second))
	ds := h.deliveriesTo(ash.ID)
	if len(ds) != 1 || len(h.delReqs) != 1 || h.delReqs[0].Source != "loop" || h.delReqs[0].DeadlineSec != 60 {
		t.Fatalf("deliveries %+v", h.delReqs)
	}
	if len(h.attn) != 1 || h.attn[0].Kind != "loopHeld" || h.attn[0].SessionID != bea.ID {
		t.Fatalf("loopHeld %+v", h.attn)
	}
	h.setDelivery(s, ds[0].ID, delivery.Confirmed)
	s.Loops.Tick(t0.Add(61 * time.Second))
	hist := loopHist(s, l.ID)
	if len(hist) != 1 || hist[0].Kind != "run" {
		t.Fatalf("history %+v", hist)
	}
	res := map[string]string{}
	for _, r := range hist[0].Targets {
		res[r.SessionID] = r.Result
	}
	if res[ash.ID] != "delivered" || res[bea.ID] != "needsYou" || res[cas.ID] != "paused" || len(res) != 3 {
		t.Fatalf("results %v", res)
	}
	if d, _ := s.Loops.Get(l.ID); d.State.Runs != 1 {
		t.Fatalf("runs %d", d.State.Runs)
	}
	// Nobody free at the next run → skip + one retry 60 s later.
	h.setState(ash.ID, agentstate.NeedsYou)
	s.Loops.Tick(t0.Add(121 * time.Second))
	hist = loopHist(s, l.ID)
	if last := hist[len(hist)-1]; last.Kind != "skip" {
		t.Fatalf("skip %+v", last)
	}
	h.setState(ash.ID, agentstate.Ready)
	s.Loops.Tick(t0.Add(181 * time.Second)) // retry (and the interval is due too)
	ds = h.deliveriesTo(ash.ID)
	h.setDelivery(s, ds[len(ds)-1].ID, delivery.Sent)
	s.Loops.Tick(t0.Add(182 * time.Second))
	d, _ := s.Loops.Get(l.ID)
	if d.State.Status != "done" || d.State.StoppedReason != "maxRuns" || d.State.Runs != 2 {
		t.Fatalf("maxRuns %+v", d.State)
	}
	if last := h.attn[len(h.attn)-1]; last.Kind != "loopEnded" {
		t.Fatalf("loopEnded %+v", h.attn)
	}
	// Restart: a running loop gets nextRunAt from now, missed runs are not
	// repeated; the definition and history persist.
	l2, _ := s.Loops.Create(LoopDef{ProjectID: p.ID, WorkspaceID: wid, Name: "Ping", Enabled: true, Text: "status?",
		Trigger: LoopTrigger{Kind: "interval", EverySec: 600}, Targets: LoopTargets{Kind: "handles", IDs: []string{ash.ID}}}, t0, false)
	s2 := New(h, h.st)
	later := t0.Add(5 * time.Hour)
	s2.Now = func() time.Time { return later }
	s2.Loops.Loc = time.UTC
	s2.Loops.Restart(later)
	got, ok := s2.Loops.Get(l2.ID)
	if !ok || got.State.Status != "running" || !got.State.NextRunAt.Equal(later.Add(10*time.Minute)) {
		t.Fatalf("restart %+v", got.State)
	}
	n := len(h.delReqs)
	s2.Loops.Tick(later)
	if len(h.delReqs) != n {
		t.Fatal("missed run repeated after restart")
	}
	if len(loopHist(s2, l.ID)) < 3 {
		t.Fatal("history lost")
	}
	// Stop-all and too many loops.
	if s2.Loops.StopAll(later) != 1 {
		t.Fatal("stop all")
	}
	for i := len(s2.Loops.List("")); i < maxLoops; i++ {
		if _, err := s2.Loops.Create(LoopDef{ProjectID: p.ID, WorkspaceID: wid, Text: "x", Trigger: LoopTrigger{Kind: "agentIdle"}, Targets: LoopTargets{Kind: "allAgents"}}, later, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s2.Loops.Create(LoopDef{ProjectID: p.ID, WorkspaceID: wid, Text: "x", Trigger: LoopTrigger{Kind: "agentIdle"}, Targets: LoopTargets{Kind: "allAgents"}}, later, false); !errors.Is(err, ErrTooManyLoops) {
		t.Fatal(err)
	}
}

// TestLoopEventTriggers: agentIdle (≤ 1 per 120 s per console, skipped
// when busy again) and output patterns (smart case; sent when free;
// stop.outputPattern stops the loop).
func TestLoopEventTriggers(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	idle, _ := s.Loops.Create(LoopDef{ProjectID: p.ID, WorkspaceID: wid, Enabled: true, Text: "Continue.",
		Trigger: LoopTrigger{Kind: "agentIdle"}, Targets: LoopTargets{Kind: "handles", IDs: []string{ash.ID}}}, t0, false)
	s.Loops.TurnFinished(ash.ID, t0)
	s.Loops.TurnFinished(bea.ID, t0) // not a target
	s.Loops.Tick(t0.Add(time.Second))
	if len(h.deliveriesTo(ash.ID)) != 1 || len(h.deliveriesTo(bea.ID)) != 0 {
		t.Fatalf("idle run %v", h.delReqs)
	}
	h.setDelivery(s, h.deliveriesTo(ash.ID)[0].ID, delivery.Sent)
	s.Loops.Tick(t0.Add(2 * time.Second))
	s.Loops.TurnFinished(ash.ID, t0.Add(60*time.Second)) // within 120 s
	s.Loops.Tick(t0.Add(61 * time.Second))
	if len(h.deliveriesTo(ash.ID)) != 1 {
		t.Fatal("agentIdle fired twice within 120 s")
	}
	s.Loops.TurnFinished(ash.ID, t0.Add(200*time.Second))
	h.setState(ash.ID, agentstate.Working) // busy again: the turn passes
	s.Loops.Tick(t0.Add(201 * time.Second))
	if len(h.deliveriesTo(ash.ID)) != 1 {
		t.Fatal("fired while busy")
	}
	s.Loops.Stop(idle.ID, "user", t0)
	h.setState(ash.ID, agentstate.Ready)
	// Output trigger with smart case.
	out, _ := s.Loops.Create(LoopDef{ProjectID: p.ID, WorkspaceID: wid, Enabled: true, Text: "Fix the failure.",
		Trigger: LoopTrigger{Kind: "output", Pattern: "FAIL"}, Targets: LoopTargets{Kind: "handles", IDs: []string{bea.ID}},
		Stop: LoopStop{OutputPattern: "all tests passed"}}, t0, false)
	h.setState(bea.ID, agentstate.Working)
	s.OnOutput(bea.ID, []byte("\x1b[31m3 tests fail\x1b[0m"))
	s.Loops.Tick(t0.Add(time.Second))
	s.OnOutput(bea.ID, []byte("--- FAIL: TestX"))
	s.Loops.Tick(t0.Add(2 * time.Second))
	if len(h.deliveriesTo(bea.ID)) != 0 {
		t.Fatal("sent while working / matched lower case")
	}
	h.setState(bea.ID, agentstate.Ready)
	s.Loops.Tick(t0.Add(3 * time.Second))
	if ds := h.deliveriesTo(bea.ID); len(ds) != 1 || ds[0].Text != "Fix the failure." {
		t.Fatalf("output run %+v", ds)
	}
	s.OnOutput(bea.ID, []byte("ok  All Tests Passed"))
	if d, _ := s.Loops.Get(out.ID); d.State.Status != "done" || d.State.StoppedReason != "output" {
		t.Fatalf("output stop %+v", d.State)
	}
}

// TestLoopGoals: agentJudged before a scheduled run (met → stop "goal";
// inconclusive → run anyway; not twice within 90 s) and command goals.
func TestLoopGoals(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.replies[ash.ID] = &session.Reply{Text: "3 tests still fail"}
	l, _ := s.Loops.Create(LoopDef{ProjectID: p.ID, WorkspaceID: wid, Enabled: true, Text: "Fix failing tests.",
		Trigger: LoopTrigger{Kind: "interval", EverySec: 60}, Targets: LoopTargets{Kind: "allAgents"},
		Stop: LoopStop{Goal: &LoopGoal{Kind: "agentJudged", Text: "All tests pass", Agent: "claude"}}}, t0, false)
	s.Loops.Tick(t0.Add(60 * time.Second))
	run := h.lastRun(t, "loop-goal")
	if string(run.Schema) != string(GoalSchema) || !contains(run.Prompt, "All tests pass") || !contains(run.Prompt, "3 tests still fail") {
		t.Fatalf("goal job %+v", run)
	}
	if len(h.delReqs) != 0 {
		t.Fatal("ran before the goal check")
	}
	run.OnFinish(failedJob("cli_not_logged_in")) // inconclusive → run anyway
	if len(h.delReqs) != 1 {
		t.Fatal("inconclusive goal blocked the run")
	}
	h.setDelivery(s, h.deliveriesTo(ash.ID)[0].ID, delivery.Sent)
	s.Loops.Tick(t0.Add(61 * time.Second))
	// Turn finished 20 s later: goal not checked again within 90 s.
	nRuns := len(h.runs)
	s.Loops.TurnFinished(ash.ID, t0.Add(80*time.Second))
	if len(h.runs) != nRuns {
		t.Fatal("goal checked twice within 90 s")
	}
	s.Loops.Tick(t0.Add(160 * time.Second)) // next run due, 100 s after the check
	run = h.lastRun(t, "loop-goal")
	if len(h.runs) != nRuns+1 {
		t.Fatal("no goal check before the run")
	}
	met, _ := json.Marshal(map[string]any{"met": true, "reason": "green"})
	run.OnFinish(doneJob("", json.RawMessage(met)))
	if d, _ := s.Loops.Get(l.ID); d.State.Status != "done" || d.State.StoppedReason != "goal" {
		t.Fatalf("goal stop %+v", d.State)
	}
	hist := loopHist(s, l.ID)
	if hist[len(hist)-2].Kind != "goalCheck" || hist[len(hist)-2].Goal == nil || !hist[len(hist)-2].Goal.Met {
		t.Fatalf("history %+v", hist)
	}
	// Command goal (exit 0 = met), run on the project machine.
	h.execCode = 0
	l2, err := s.Loops.Create(LoopDef{ProjectID: p.ID, WorkspaceID: wid, Enabled: true, Text: "Keep fixing.",
		Trigger: LoopTrigger{Kind: "interval", EverySec: 60}, Targets: LoopTargets{Kind: "allAgents"},
		Stop: LoopStop{Goal: &LoopGoal{Kind: "command", Command: "npm test", TimeoutSec: 30}}}, t0, false)
	if err != nil {
		t.Fatal(err)
	}
	s.Loops.Tick(t0.Add(60 * time.Second))
	for i := 0; i < 100; i++ {
		if d, _ := s.Loops.Get(l2.ID); d.State.Status == "done" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d, _ := s.Loops.Get(l2.ID); d.State.StoppedReason != "goal" {
		t.Fatalf("command goal %+v", d.State)
	}
	// maxDurationSec stops with reason "time".
	l3, _ := s.Loops.Create(LoopDef{ProjectID: p.ID, WorkspaceID: wid, Enabled: true, Text: "x", Trigger: LoopTrigger{Kind: "agentIdle"},
		Targets: LoopTargets{Kind: "allAgents"}, Stop: LoopStop{MaxDurationSec: 900}}, t0, false)
	s.Loops.Tick(t0.Add(901 * time.Second))
	if d, _ := s.Loops.Get(l3.ID); d.State.StoppedReason != "time" {
		t.Fatalf("time %+v", d.State)
	}
	_ = jobs.StateDone
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (stringIndex(s, sub) >= 0) }

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
