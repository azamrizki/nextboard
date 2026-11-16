package orch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/delivery"
)

// A mission next to everything else of its thread (§22): the owner's own open question, the
// planner turn, the end of the mission, tasks the lead cancels.

func TestMissionLeavesTheOwnersQuestionAlone(t *testing.T) {
	h, s, pid, wid, _, bea := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	s.Command(CommandRequest{Text: "kill Bea", WorkspaceID: wid, Planner: "off"})
	kill, ok := s.Confirms.Pending(pid, t0)
	if !ok || kill.Kind != ConfDestructive || kill.MissionID != "" {
		t.Fatalf("the owner's question %+v", kill)
	}
	// no wake, no question of the mission's, while the owner's is open
	s.MissionEvent(ms.ID, "go")
	runs := plannerRuns(h)
	s.Tick(t0.Add(6 * time.Second))
	if plannerRuns(h) != runs {
		t.Fatal("the lead woke over the owner's open question")
	}
	s.askToGoOn(mustMission(t, s, ms.ID), WaitLimit, "Limit reached: continue?")
	if cf, _ := s.Confirms.Pending(pid, t0); cf.ID != kill.ID || mustMission(t, s, ms.ID).State != MissionRunning {
		t.Fatalf("the limit question set the owner's aside: %+v", cf)
	}
	// a wake's step that needs an OK does not set aside a question that came up while the
	// lead thought (the owner's commands wait behind the wake; one from elsewhere does not)
	s.Confirms.Decide(kill.ID, "cancel", "", "admin", t0)
	s.Tick(t0.Add(12 * time.Second))
	if s.Planning(pid) == "" {
		t.Fatal("no wake")
	}
	kill = *s.addConfirm(&execCtx{commandID: "cmd_kill", threadID: pid, projectID: pid, workspaceID: wid},
		&Confirmation{Kind: ConfDestructive, Prompt: "Kill @Bea?", Steps: []Step{{Op: "kill", Targets: []string{bea}}},
			Options: []Option{{ID: "confirm", Label: "Kill"}, {ID: "cancel", Label: "Cancel"}}})
	s.plannerDone(pid, missionJob(map[string]any{"tasks": []any{
		map[string]any{"id": "t3", "handle": "Ash", "text": "Drop table sessions_old"}}}))
	if cf, _ := s.Confirms.Pending(pid, t0); cf.ID != kill.ID {
		t.Fatalf("the wake set the owner's question aside: %+v", cf)
	}
	if ms = mustMission(t, s, ms.ID); ms.State != MissionRunning || len(ms.Tasks) != 2 {
		t.Fatalf("mission %+v", ms)
	}
	// the owner's "yes" answers the owner's question
	s.Command(CommandRequest{Text: "yes", WorkspaceID: wid})
	if !strings.Contains(strings.Join(h.calls, ","), "kill:"+bea) {
		t.Fatalf("calls %v", h.calls)
	}
	if ms = mustMission(t, s, ms.ID); ms.State != MissionRunning || ms.ConfirmationID != "" {
		t.Fatalf("the yes reached the mission: %+v", ms)
	}
	// and the lead hears what waited
	run := wakeRound(t, h, s, ms, t0.Add(18*time.Second), missionJob(nil))
	if !strings.Contains(run.Prompt, "another question open") {
		t.Fatal("the lead was not told its step waited")
	}
}

func TestMissionCancelledTaskStopsItsOrder(t *testing.T) {
	h, s, pid, wid, ash, bea := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	old := ms.Tasks[0].OrderID
	s.MissionEvent(ms.ID, "go")
	wakeRound(t, h, s, ms, t0.Add(6*time.Second), missionJob(map[string]any{"tasks": []any{map[string]any{"id": "t1", "state": "cancelled"}}}))
	ms = mustMission(t, s, ms.ID)
	t1, _ := ms.Task("t1")
	if d := h.deliveriesTo(ash); t1.State != TaskCancelled || t1.OrderID != "" || d[0].State != delivery.Cancelled {
		t.Fatalf("t1 %+v, delivery %s", t1, d[0].State)
	}
	// what its order still says moves nothing
	o, _ := s.Orders.Get(old)
	o.Targets[0].State = TWorking
	s.missionOrderChanged(o)
	// and t2 does not take a cancelled t1 for done: it waits, and the lead hears why
	if ms = mustMission(t, s, ms.ID); taskState(ms, "t1") != TaskCancelled || taskState(ms, "t2") != TaskWaiting || len(h.deliveriesTo(bea)) != 0 {
		t.Fatalf("after: %+v", ms.Tasks)
	}
	run := wakeRound(t, h, s, ms, t0.Add(12*time.Second), missionJob(nil))
	if !strings.Contains(run.Prompt, "t2 waits for t1, which was cancelled") {
		t.Fatal("the lead was not told t2 waits on a cancelled task")
	}
}

func TestMissionControlDoesNotWaitInTheQueue(t *testing.T) {
	_, s, pid, wid, _, _ := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	s.Command(CommandRequest{Text: "@Aruna menurutmu desainnya gimana?", WorkspaceID: wid})
	if s.Planning(pid) == "" {
		t.Fatal("no planner turn holds the thread")
	}
	r, err := s.Command(CommandRequest{Text: "hentikan misi", WorkspaceID: wid})
	if err != nil || r.HandledBy != "parser" || mustMission(t, s, ms.ID).State != MissionStopped {
		t.Fatalf("%v %+v, mission %s", err, r, mustMission(t, s, ms.ID).State)
	}
}

func TestMissionTurnClaims(t *testing.T) {
	h, s, pid, wid, _, _ := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	sc, _ := s.newScope(wid, "")
	owner := &execCtx{commandID: "cmd_owner", threadID: pid, projectID: pid, workspaceID: wid}
	if ok, _ := s.startPlanner(owner, sc, "status?"); !ok {
		t.Fatal("no turn")
	}
	// another caller's drain leaves the owner's turn alone
	s.drain(pid, &execCtx{commandID: "cmd_other"})
	if s.Planning(pid) != "cmd_owner" {
		t.Fatalf("drain took the owner's turn: %q", s.Planning(pid))
	}
	// a command that reaches the planner while it is busy goes first in line
	s.mu.Lock()
	s.thread[pid].queue = append(s.thread[pid].queue, queuedCmd{ctx: &execCtx{commandID: "cmd_later", threadID: pid}, text: "later"})
	s.mu.Unlock()
	second := &execCtx{commandID: "cmd_second", threadID: pid, projectID: pid, workspaceID: wid}
	s.startPlanner(second, sc, "and this?")
	s.mu.Lock()
	q := s.thread[pid].queue
	s.mu.Unlock()
	if len(q) != 2 || q[0].ctx != second || !q[0].plan {
		t.Fatalf("queue %+v", q)
	}
	// a wake does not start while commands wait
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "ok", "steps": []any{}}))
	s.mu.Lock()
	s.thread[pid].busy = false
	s.thread[pid].queue = []queuedCmd{{ctx: &execCtx{commandID: "cmd_q", threadID: pid}, text: "x"}}
	s.mu.Unlock()
	s.MissionEvent(ms.ID, "go")
	runs := plannerRuns(h)
	s.missionRound(mustMission(t, s, ms.ID), t0.Add(6*time.Second))
	if plannerRuns(h) != runs {
		t.Fatal("a wake started while a command waited")
	}
	s.mu.Lock()
	s.thread[pid].queue = nil
	s.mu.Unlock()

	// a wake whose start fails keeps the turn until it lets it go itself
	h.mu.Lock()
	h.runErr = errors.New("runner down")
	h.mu.Unlock()
	s.missionRound(mustMission(t, s, ms.ID), t0.Add(12*time.Second))
	if s.Planning(pid) != "" {
		t.Fatalf("the failed wake still holds the thread: %q", s.Planning(pid))
	}
	// resume lets go of a wake left busy
	s.PauseMission(ms.ID)
	s.mu.Lock()
	s.mwake[ms.ID].busy = true
	s.mu.Unlock()
	s.ResumeMission(ms.ID)
	s.mu.Lock()
	busy := s.mwake[ms.ID].busy
	s.mu.Unlock()
	if busy {
		t.Fatal("resume left the wake busy")
	}
}

func TestMissionWaitsOnItsNewestQuestion(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	finishTurn(t, h, s, ash, "API ready.")
	waitUntil(t, "t1's result", func() bool { x, _ := mustMission(t, s, ms.ID).Task("t1"); return x.Result != "" })
	wakeRound(t, h, s, ms, t0.Add(6*time.Second), missionJob(map[string]any{"tasks": []any{
		map[string]any{"id": "t3", "handle": "Ash", "text": "Drop table sessions_old"}}}))
	first, _ := s.Confirms.Pending(pid, t0)
	// the owner talks it over; the lead's new risky plan takes its place
	s.Command(CommandRequest{Text: "@Aruna sekalian hapus folder build juga", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "", "steps": []any{sendStep("Ash", "rm -rf build")}}))
	second, _ := s.Confirms.Pending(pid, t0)
	ms = mustMission(t, s, ms.ID)
	if second.ID == first.ID || ms.State != MissionWaiting || ms.ConfirmationID != second.ID || ms.WaitKind != WaitHeld {
		t.Fatalf("mission waits on %q, open %q", ms.ConfirmationID, second.ID)
	}
}

func TestMissionDoneDropsItsSends(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	before := len(h.deliveriesTo(ash))
	s.MissionEvent(ms.ID, "go")
	wakeRound(t, h, s, ms, t0.Add(6*time.Second), missionJob(map[string]any{"done": map[string]any{"summary": "All done."}}, sendStep("Ash", "One more thing")))
	if ms = mustMission(t, s, ms.ID); ms.State != MissionDone || len(h.deliveriesTo(ash)) != before {
		t.Fatalf("after done: %s, %d deliveries", ms.State, len(h.deliveriesTo(ash)))
	}
}

func TestMissionHeldStepsNeedTheirBoard(t *testing.T) {
	h, s, pid, wid, _, _ := leadHub(t)
	p, _ := h.projects.Get(pid)
	cy := h.console(p, wid, "Cy", "claude", agentstate.Ready)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	s.MissionEvent(ms.ID, "go")
	wakeRound(t, h, s, ms, t0.Add(6*time.Second), missionJob(nil, sendStep("Cy", "Write the docs")))
	cf, _ := s.Confirms.Pending(pid, t0)
	s.UpdateMission(ms.ID, func(m *MissionRun) bool { m.CardID = "n_gone00000001"; return true })
	if _, err := s.Decide(cf.ID, "approve", "", "admin"); err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(threadTexts(s, pid, "note"), " | ")
	if len(h.deliveriesTo(cy.ID)) != 0 || !strings.Contains(notes, "Not done: the mission's board or orchestrator card is gone") {
		t.Fatalf("ran without the policy: %d deliveries, notes %q", len(h.deliveriesTo(cy.ID)), notes)
	}
}

func TestMissionEndsOnce(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	s.endMission(ms.ID, MissionStopped, "first")
	s.endMission(ms.ID, MissionFailed, "second")
	ms = mustMission(t, s, ms.ID)
	n := 0
	for _, m := range threadTexts(s, pid, "mission") {
		if strings.HasPrefix(m, "Mission stopped") || strings.HasPrefix(m, "Mission failed") {
			n++
		}
	}
	if ms.State != MissionStopped || ms.Result != "first" || n != 1 || h.deliveriesTo(ash)[0].State != delivery.Cancelled {
		t.Fatalf("%s %q, %d end messages", ms.State, ms.Result, n)
	}
	if _, err := s.PauseMission(ms.ID); !errors.Is(err, ErrMissionState) {
		t.Fatalf("pause after the end: %v", err)
	}
	sc, _ := s.newScope(wid, "")
	s.holdForOwner(&execCtx{threadID: pid}, sc, ms.ID, []Step{{Op: "pause", Targets: []string{ash}}})
	if ms = mustMission(t, s, ms.ID); ms.State != MissionStopped || ms.ConfirmationID != "" {
		t.Fatalf("an ended mission came back: %+v", ms)
	}
}
