package orch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/session"
)

// Missions (§22): a scripted lead proposes, the owner approves once, the hub sends the tasks
// in dependency order and wakes the lead on what happens.

func missionJob(m map[string]any, steps ...any) jobs.Job {
	if steps == nil {
		steps = []any{}
	}
	return doneJob("", map[string]any{"reply": "", "steps": steps, "mission": m})
}

// proposeLogin has the lead propose a two-task mission: Ash builds the API, Bea the page after it.
func proposeLogin(t *testing.T, s *Service, pid, wid string) MissionRun {
	t.Helper()
	if _, err := s.Command(CommandRequest{Text: "bikin fitur login, frontend dan api kerja bareng", WorkspaceID: wid}); err != nil {
		t.Fatal(err)
	}
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Aku susun misinya.", "steps": []any{}, "mission": map[string]any{
		"goal": "Login feature", "summary": "Ash builds the API, then Bea the page", "doneWhen": "a user can log in from the page",
		"doc": "POST /api/login {user, pass} → {token}",
		"tasks": []any{
			map[string]any{"id": "t1", "handle": "Ash", "title": "Login API", "text": "Build POST /api/login as the brief says"},
			map[string]any{"id": "t2", "handle": "Bea", "title": "Login page", "text": "Build the login page against the API", "dependsOn": []any{"t1"}},
		}}}))
	ms, ok := s.Missions.Active(pid)
	if !ok {
		t.Fatalf("no mission; thread %q", threadTexts(s, pid, "note"))
	}
	return ms
}

func approve(t *testing.T, s *Service, ms MissionRun) MissionRun {
	t.Helper()
	if _, err := s.Decide(ms.ConfirmationID, "approve", "", "admin"); err != nil {
		t.Fatal(err)
	}
	return mustMission(t, s, ms.ID)
}

func mustMission(t *testing.T, s *Service, id string) MissionRun {
	t.Helper()
	ms, ok := s.Missions.Get(id)
	if !ok {
		t.Fatalf("mission %s gone", id)
	}
	return ms
}

func taskState(ms MissionRun, id string) string {
	t, _ := ms.Task(id)
	return t.State
}

// finishTurn types the console's last delivery and ends its turn with reply.
func finishTurn(t *testing.T, h *fakeHub, s *Service, sid, reply string) {
	t.Helper()
	d := h.deliveriesTo(sid)
	if len(d) == 0 {
		t.Fatal("nothing was sent to the console")
	}
	h.setDelivery(s, d[len(d)-1].ID, delivery.Confirmed)
	h.mu.Lock()
	h.replies[sid] = &session.Reply{Text: reply, MessageID: "m_" + d[len(d)-1].ID, Chars: len(reply), At: t0.Add(time.Minute)}
	h.mu.Unlock()
	end := t0.Add(time.Minute)
	in, _ := h.SessionInfo(sid)
	s.OnAgentEvent(in, agentstate.Event{Kind: agentstate.EvFinished, Turn: agentstate.LastTurn{StartedAt: t0, EndedAt: &end, DurationMs: 60000}})
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func plannerRuns(h *fakeHub) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.runs {
		if r.Purpose == "planner" {
			n++
		}
	}
	return n
}

// wakeRound makes the mission's pending wake due, checks that it started a lead turn, and
// answers it with plan.
func wakeRound(t *testing.T, h *fakeHub, s *Service, ms MissionRun, at time.Time, plan jobs.Job) jobs.RunnerRequest {
	t.Helper()
	before := plannerRuns(h)
	s.Tick(at)
	if plannerRuns(h) != before+1 {
		t.Fatalf("no lead turn at %s; mission %+v", at.Sub(t0), mustMission(t, s, ms.ID))
	}
	run := h.lastRun(t, "planner")
	s.plannerDone(ms.ThreadID, plan)
	return run
}

func TestMissionRunsToTheEnd(t *testing.T) {
	h, s, pid, wid, ash, bea := leadHub(t)
	ms := proposeLogin(t, s, pid, wid)
	cf, ok := s.Confirms.Pending(pid, t0)
	if ms.State != MissionAwaiting || !ok || cf.Kind != ConfMission || cf.MissionID != ms.ID || len(cf.Plan) != 2 || cf.Plan[1].Handle != "Bea" {
		t.Fatalf("proposal %+v / %+v", ms, cf)
	}
	if ms.Doc == "" || ms.DocRev != 1 || ms.Lead != "Aruna" || len(ms.Tasks) != 2 || ms.Tasks[0].SessionID != ash {
		t.Fatalf("mission %+v", ms)
	}
	if len(h.delReqs) != 0 {
		t.Fatal("sent before the owner approved")
	}
	if got := threadTexts(s, pid, "mission"); len(got) != 1 || !strings.Contains(got[0], "Mission proposed") {
		t.Fatalf("mission messages %q", got)
	}

	// approval in words, like a proposal
	s.Command(CommandRequest{Text: "ya lanjut", WorkspaceID: wid})
	ms = mustMission(t, s, ms.ID)
	if ms.State != MissionRunning || ms.ApprovedAt == nil || !ms.Deadline.Equal(t0.Add(4*time.Hour)) || ms.MaxRounds != 30 {
		t.Fatalf("approved %+v", ms)
	}
	d := h.deliveriesTo(ash)
	if len(d) != 1 || len(h.deliveriesTo(bea)) != 0 {
		t.Fatalf("t1 only goes first: %d to Ash, %d to Bea", len(d), len(h.deliveriesTo(bea)))
	}
	for _, want := range []string{"Build POST /api/login", "task t1", "Goal: Login feature", "mission_read", "team_ask", "team_reply", "team_update", "mission_doc", "mission_report", "@Bea"} {
		if !strings.Contains(d[0].Text, want) {
			t.Fatalf("order text lacks %q:\n%s", want, d[0].Text)
		}
	}
	o, _ := s.Orders.Get(d[0].OrderID)
	if o.MissionID != ms.ID || ms.Tasks[0].OrderID != o.ID || taskState(ms, "t1") != TaskSent {
		t.Fatalf("order %+v task %+v", o, ms.Tasks[0])
	}
	plan := false
	for _, m := range s.Threads.Last(pid, 30) {
		plan = plan || (m.Kind == "plan" && m.MissionID == ms.ID)
	}
	if !plan {
		t.Fatal("the order's plan message has no missionId")
	}

	// t1 done: t2 goes out at once, and the reply wakes the lead
	finishTurn(t, h, s, ash, "API ready on :8080.")
	if ms = mustMission(t, s, ms.ID); taskState(ms, "t1") != TaskDone || taskState(ms, "t2") != TaskSent || len(h.deliveriesTo(bea)) != 1 {
		t.Fatalf("after t1: %+v", ms.Tasks)
	}
	waitUntil(t, "t1's result", func() bool { x, _ := mustMission(t, s, ms.ID).Task("t1"); return x.Result == "API ready on :8080." })
	run := wakeRound(t, h, s, ms, t0.Add(6*time.Second), missionJob(map[string]any{"tasks": []any{
		map[string]any{"id": "t3", "handle": "Ash", "title": "API tests", "text": "Write tests for the login API", "dependsOn": []any{"t2"}}}}))
	for _, want := range []string{`"wokenBy"`, "finished t1", `"roundsLeft":30`, `"doneWhen"`, "API ready on :8080."} {
		if !strings.Contains(run.Prompt, want) {
			t.Fatalf("lead input lacks %s", want)
		}
	}
	if ms = mustMission(t, s, ms.ID); ms.Rounds != 1 || taskState(ms, "t3") != TaskWaiting {
		t.Fatalf("after the wake: rounds %d, %+v", ms.Rounds, ms.Tasks)
	}

	// t2 done: t3 goes to Ash; t3 done: the lead sums up
	finishTurn(t, h, s, bea, "Page done.")
	waitUntil(t, "t3 sent", func() bool { return taskState(mustMission(t, s, ms.ID), "t3") == TaskSent })
	finishTurn(t, h, s, ash, "Tests pass.")
	waitUntil(t, "t3's result", func() bool { x, _ := mustMission(t, s, ms.ID).Task("t3"); return x.Result != "" })
	run = wakeRound(t, h, s, ms, t0.Add(12*time.Second), missionJob(map[string]any{"done": map[string]any{"summary": "Login works: API (Ash), page (Bea), tests pass."}}))
	if !strings.Contains(run.Prompt, "every task has ended") {
		t.Fatal("the lead did not hear that every task ended")
	}
	ms = mustMission(t, s, ms.ID)
	if ms.State != MissionDone || ms.EndedAt == nil || !strings.Contains(ms.Result, "Login works") {
		t.Fatalf("done %+v", ms)
	}
	msgs := threadTexts(s, pid, "mission")
	if last := msgs[len(msgs)-1]; !strings.HasPrefix(last, "Mission done: Login feature") || !strings.Contains(last, "tests pass") {
		t.Fatalf("final message %q", last)
	}
	if _, ok := s.MissionOf(ash); ok {
		t.Fatal("a mission that is done still has members")
	}
}

func TestMissionLimits(t *testing.T) {
	t.Run("rounds", func(t *testing.T) {
		h, s, pid, wid, _, _ := leadHub(t)
		h.set.Lead.MissionRounds = 1
		ms := approve(t, s, proposeLogin(t, s, pid, wid))
		s.MissionEvent(ms.ID, "first")
		wakeRound(t, h, s, ms, t0.Add(6*time.Second), missionJob(nil))
		s.MissionEvent(ms.ID, "second")
		before := plannerRuns(h)
		s.Tick(t0.Add(12 * time.Second))
		ms = mustMission(t, s, ms.ID)
		cf, ok := s.Confirms.Pending(pid, t0)
		if plannerRuns(h) != before || ms.State != MissionWaiting || ms.WaitKind != WaitLimit || !ok || cf.Kind != ConfMission || cf.Prompt != "Limit reached (1 rounds / 4 h): continue?" || ms.ConfirmationID != cf.ID {
			t.Fatalf("limit %+v / %+v", ms, cf)
		}
		s.Command(CommandRequest{Text: "lanjut", WorkspaceID: wid})
		if ms = mustMission(t, s, ms.ID); ms.State != MissionRunning || ms.MaxRounds != 2 || ms.WaitKind != "" {
			t.Fatalf("after continue %+v", ms)
		}
		// what woke it meanwhile is still there
		wakeRound(t, h, s, ms, t0.Add(18*time.Second), missionJob(nil))
	})
	t.Run("hours", func(t *testing.T) {
		_, s, pid, wid, _, _ := leadHub(t)
		ms := approve(t, s, proposeLogin(t, s, pid, wid))
		s.Now = func() time.Time { return t0.Add(4*time.Hour + time.Second) }
		s.Tick(s.Now())
		if ms = mustMission(t, s, ms.ID); ms.State != MissionWaiting || !strings.HasPrefix(ms.Waiting, "Limit reached (30 rounds / 4 h)") {
			t.Fatalf("deadline %+v", ms)
		}
		s.Now = func() time.Time { return t0.Add(4*time.Hour + time.Minute) }
		ms = approve(t, s, ms)
		if ms.State != MissionRunning || !ms.Deadline.Equal(t0.Add(8*time.Hour+time.Minute)) {
			t.Fatalf("after continue %+v", ms)
		}
	})
	t.Run("no progress", func(t *testing.T) {
		h, s, pid, wid, _, _ := leadHub(t)
		ms := approve(t, s, proposeLogin(t, s, pid, wid))
		at := t0
		for i := 0; i < 3; i++ {
			s.MissionEvent(ms.ID, "nothing new "+itoa(i))
			at = at.Add(6 * time.Second)
			wakeRound(t, h, s, ms, at, missionJob(nil))
		}
		s.MissionEvent(ms.ID, "nothing new again")
		before := plannerRuns(h)
		s.Tick(at.Add(6 * time.Second))
		if ms = mustMission(t, s, ms.ID); plannerRuns(h) != before || ms.State != MissionWaiting || ms.WaitKind != WaitNoProgress || !strings.Contains(ms.Waiting, "No progress") {
			t.Fatalf("stuck %+v", ms)
		}
		if _, err := s.Decide(ms.ConfirmationID, "cancel", "", "admin"); err != nil {
			t.Fatal(err)
		}
		if ms = mustMission(t, s, ms.ID); ms.State != MissionStopped || !strings.Contains(ms.Result, "Still at work: t1 (@Ash)") {
			t.Fatalf("stopped %+v", ms)
		}
	})
}

func TestMissionAskOwner(t *testing.T) {
	h, s, pid, wid, _, _ := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	s.MissionEvent(ms.ID, "@Bea is blocked on t2")
	wakeRound(t, h, s, ms, t0.Add(6*time.Second), missionJob(map[string]any{"askOwner": map[string]any{"question": "Token di cookie atau header?"}}))
	ms = mustMission(t, s, ms.ID)
	if ms.State != MissionWaiting || ms.WaitKind != WaitQuestion || ms.Waiting != "Token di cookie atau header?" || ms.ConfirmationID != "" {
		t.Fatalf("ask %+v", ms)
	}
	if msgs := threadTexts(s, pid, "mission"); msgs[len(msgs)-1] != "Token di cookie atau header?" {
		t.Fatalf("question not in the thread: %q", msgs)
	}
	// whatever the owner writes is the answer, for the lead
	before := plannerRuns(h)
	s.Command(CommandRequest{Text: "cookie", WorkspaceID: wid})
	if plannerRuns(h) != before+1 || !strings.Contains(h.lastRun(t, "planner").Prompt, "Token di cookie atau header?") {
		t.Fatal("the answer did not reach the lead with the mission")
	}
	s.plannerDone(pid, missionJob(map[string]any{"doc": "POST /api/login {user, pass} → token in an httpOnly cookie"}))
	ms = mustMission(t, s, ms.ID)
	// the owner's turn while the mission waited for them is no round of the lead's
	if ms.State != MissionRunning || ms.DocRev != 2 || !strings.Contains(ms.Doc, "cookie") || ms.Rounds != 1 {
		t.Fatalf("after the answer %+v", ms)
	}
}

func TestMissionPauseResumeStop(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	first := h.deliveriesTo(ash)[0]
	s.Command(CommandRequest{Text: "jeda misi", WorkspaceID: wid})
	ms = mustMission(t, s, ms.ID)
	if d, _ := h.Delivery(first.ID); ms.State != MissionPaused || d.State != delivery.Cancelled || taskState(ms, "t1") != TaskWaiting || ms.Tasks[0].OrderID != "" {
		t.Fatalf("paused %+v, delivery %s", ms, d.State)
	}
	s.Command(CommandRequest{Text: "lanjutkan misi", WorkspaceID: wid})
	ms = mustMission(t, s, ms.ID)
	if ms.State != MissionRunning || len(h.deliveriesTo(ash)) != 2 || taskState(ms, "t1") != TaskSent {
		t.Fatalf("resumed %+v", ms)
	}
	second := h.deliveriesTo(ash)[1]
	s.Command(CommandRequest{Text: "stop mission", WorkspaceID: wid})
	ms = mustMission(t, s, ms.ID)
	if d, _ := h.Delivery(second.ID); ms.State != MissionStopped || d.State != delivery.Cancelled || taskState(ms, "t2") != TaskCancelled ||
		!strings.HasPrefix(ms.Result, "Stopped by the owner.") {
		t.Fatalf("stopped %+v", ms)
	}
	if _, err := s.PauseMission(ms.ID); !errors.Is(err, ErrMissionState) {
		t.Fatalf("pause after stop: %v", err)
	}
	for _, w := range []string{"pause mission", "Jeda misi.", "resume the mission", "lanjutkan misinya", "hentikan misi", "stop mission!"} {
		if missionControl(w) == "" {
			t.Errorf("%q is no mission control", w)
		}
	}
	for _, w := range []string{"pause", "stop", "lanjutkan", "mission control is down"} {
		if missionControl(w) != "" {
			t.Errorf("%q taken for a mission control", w)
		}
	}
}

func TestMissionPausedByHubRestart(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	s2 := afterRestart(h, s)
	t.Cleanup(s2.Close)
	s2.Start()
	ms = mustMission(t, s2, ms.ID)
	if ms.State != MissionPaused || taskState(ms, "t1") != TaskWaiting || ms.Tasks[0].OrderID != "" {
		t.Fatalf("after restart %+v", ms)
	}
	if msgs := threadTexts(s2, pid, "mission"); msgs[len(msgs)-1] != "Paused by a hub restart. Resume?" {
		t.Fatalf("thread %q", msgs)
	}
	if _, err := s2.ResumeMission(ms.ID); err != nil {
		t.Fatal(err)
	}
	if ms = mustMission(t, s2, ms.ID); ms.State != MissionRunning || len(h.deliveriesTo(ash)) != 2 || taskState(ms, "t1") != TaskSent {
		t.Fatalf("resumed %+v, %d deliveries", ms, len(h.deliveriesTo(ash)))
	}
}

func TestMissionRiskyStepHeldThenGoesOn(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	finishTurn(t, h, s, ash, "API ready.")
	waitUntil(t, "t1's result", func() bool { x, _ := mustMission(t, s, ms.ID).Task("t1"); return x.Result != "" })
	wakeRound(t, h, s, ms, t0.Add(6*time.Second), missionJob(map[string]any{"tasks": []any{
		map[string]any{"id": "t3", "handle": "Ash", "title": "Clean up", "text": "Drop table sessions_old, it is unused now"}}}))
	ms = mustMission(t, s, ms.ID)
	cf, ok := s.Confirms.Pending(pid, t0)
	if ms.State != MissionWaiting || ms.WaitKind != WaitHeld || !ok || ms.ConfirmationID != cf.ID || cf.MissionID != ms.ID || len(ms.Tasks) != 2 {
		t.Fatalf("held %+v / %+v", ms, cf)
	}
	if _, err := s.Decide(cf.ID, "confirm", "", "admin"); err != nil {
		t.Fatal(err)
	}
	ms = mustMission(t, s, ms.ID)
	d := h.deliveriesTo(ash)
	if ms.State != MissionRunning || ms.WaitKind != "" || taskState(ms, "t3") != TaskSent || !strings.Contains(d[len(d)-1].Text, "Drop table sessions_old") {
		t.Fatalf("after OK %+v", ms)
	}
}

func TestMissionTeamChangeWaitsForOwner(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	p, _ := h.projects.Get(pid)
	cy := h.console(p, wid, "Cy", "claude", agentstate.Ready)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	finishTurn(t, h, s, ash, "API ready.")
	waitUntil(t, "t1's result", func() bool { x, _ := mustMission(t, s, ms.ID).Task("t1"); return x.Result != "" })
	wakeRound(t, h, s, ms, t0.Add(6*time.Second), missionJob(map[string]any{"tasks": []any{
		map[string]any{"id": "t3", "handle": "Cy", "title": "Docs", "text": "Document the login API"}}}))
	ms = mustMission(t, s, ms.ID)
	cf, _ := s.Confirms.Pending(pid, t0)
	if ms.State != MissionWaiting || cf.Kind != ConfMission || len(cf.Steps) != 1 || len(h.deliveriesTo(cy.ID)) != 0 {
		t.Fatalf("team change %+v / %+v", ms, cf)
	}
	if _, err := s.Decide(cf.ID, "approve", "", "admin"); err != nil {
		t.Fatal(err)
	}
	ms = mustMission(t, s, ms.ID)
	if ms.State != MissionRunning || taskState(ms, "t3") != TaskSent || !ms.Member(cy.ID) || len(h.deliveriesTo(cy.ID)) != 1 {
		t.Fatalf("after OK %+v", ms)
	}
}

func TestMissionLeadAnswersAndNudges(t *testing.T) {
	h, s, pid, wid, _, bea := leadHub(t)
	ms := approve(t, s, proposeLogin(t, s, pid, wid))
	s.UpdateMission(ms.ID, func(m *MissionRun) bool {
		m.Questions = append(m.Questions, MissionQuestion{ID: "q_port", From: "Bea", FromID: bea, To: "lead", Text: "Which port?", At: t0})
		return true
	})
	// nobody answered for 10 min: the lead hears of it
	run := wakeRound(t, h, s, ms, t0.Add(11*time.Minute), missionJob(map[string]any{"answers": []any{map[string]any{"question": "q_port", "answer": "8080"}}}))
	if !strings.Contains(run.Prompt, "no answer after 10 min") || !strings.Contains(run.Prompt, `"openQuestions"`) {
		t.Fatal("the lead was not told of the open question")
	}
	ms = mustMission(t, s, ms.ID)
	q, _ := ms.Question("q_port")
	if q.Open() || q.Answer != "8080" || !q.Nudged {
		t.Fatalf("question %+v", q)
	}
	var reply ThreadMessage
	for _, m := range s.TeamMessages(ms, 10) {
		if m.Team != nil && m.Team.Kind == "reply" && m.Team.QuestionID == "q_port" {
			reply = m
		}
	}
	if reply.Text != "8080" || reply.Team.From != "Aruna" || reply.Team.To != "Bea" {
		t.Fatalf("reply %+v", reply)
	}
	d := h.deliveriesTo(bea)
	if len(d) != 1 || d[0].Source != "team" || !strings.Contains(d[0].Text, "answers your question q_port: 8080") {
		t.Fatalf("delivery %+v", d)
	}
}

func TestCreateMissionChecksIt(t *testing.T) {
	_, s, pid, _, _, _ := leadHub(t)
	task := func(id, handle string, deps ...string) MissionTaskSpec {
		return MissionTaskSpec{ID: id, Handle: handle, Text: "do " + id, DependsOn: deps}
	}
	for name, sp := range map[string]MissionSpec{
		"no goal":        {Tasks: []MissionTaskSpec{task("t1", "Ash")}},
		"no tasks":       {Goal: "x"},
		"unknown":        {Goal: "x", Tasks: []MissionTaskSpec{task("t1", "Zed")}},
		"circle":         {Goal: "x", Tasks: []MissionTaskSpec{task("t1", "Ash", "t2"), task("t2", "Bea", "t1")}},
		"missing dep":    {Goal: "x", Tasks: []MissionTaskSpec{task("t1", "Ash", "t9")}},
		"bad id":         {Goal: "x", Tasks: []MissionTaskSpec{task("first", "Ash")}},
		"too long title": {Goal: "x", Tasks: []MissionTaskSpec{{ID: "t1", Handle: "Ash", Text: "y", Title: strings.Repeat("t", 201)}}},
	} {
		if _, err := s.CreateMission(MissionRequest{MissionSpec: sp, ProjectID: pid}); !errors.Is(err, ErrBadMission) {
			t.Errorf("%s: %v", name, err)
		}
	}
	ms, err := s.CreateMission(MissionRequest{ProjectID: pid, MissionSpec: MissionSpec{Goal: "Login", Tasks: []MissionTaskSpec{task("", "@Ash"), task("", "Bea", "t1")}}})
	if err != nil || ms.State != MissionAwaiting || ms.ThreadID != pid || ms.Tasks[1].ID != "t2" || ms.ConfirmationID == "" {
		t.Fatalf("%v %+v", err, ms)
	}
	approve(t, s, ms)
	if _, err := s.CreateMission(MissionRequest{ProjectID: pid, MissionSpec: MissionSpec{Goal: "Other", Tasks: []MissionTaskSpec{task("t1", "Ash")}}}); !errors.Is(err, ErrMissionActive) {
		t.Fatalf("second mission in the thread: %v", err)
	}
	if _, err := s.CreateMission(MissionRequest{ProjectID: "p_nothere00"}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
}

func TestMissionApprovalExpires(t *testing.T) {
	h, s, pid, wid, _, _ := leadHub(t)
	ms := proposeLogin(t, s, pid, wid)
	s.Tick(t0.Add(proposalTTL + time.Second))
	if ms = mustMission(t, s, ms.ID); ms.State != MissionStopped || len(h.delReqs) != 0 {
		t.Fatalf("expired %+v", ms)
	}
	if _, ok := s.Missions.Active(pid); ok {
		t.Fatal("an expired proposal still blocks the thread")
	}
	// a thread has one open question: another one sets the plan aside, it does not hang
	ms = proposeLogin(t, s, pid, wid)
	s.Command(CommandRequest{Text: "kill Ash", WorkspaceID: wid, Planner: "off"})
	if cf, _ := s.Confirms.Pending(pid, t0); cf.Kind != ConfDestructive {
		t.Fatalf("pending %+v", cf)
	}
	if ms = mustMission(t, s, ms.ID); ms.State != MissionStopped || !strings.Contains(ms.Result, "set aside") {
		t.Fatalf("set aside %+v", ms)
	}
}

func TestLeadLanguage(t *testing.T) {
	for _, c := range []struct{ lang, want, not string }{
		{"", "in the language the owner writes in", "whatever language"},
		{"id", "always in Bahasa Indonesia, whatever language the owner writes in", "in the language the owner writes in"},
		{"en", "always in English, whatever language the owner writes in", "in the language the owner writes in"},
	} {
		h, s, _, _, _, _ := leadHub(t)
		h.set.Lead.Language = c.lang
		sc, err := s.newScope(h.projects.List()[0].Workspaces[0].ID, "")
		if err != nil {
			t.Fatal(err)
		}
		p := leadPrompt(s.leadOf(sc))
		if !strings.Contains(p, c.want) || strings.Contains(p, c.not) || !strings.Contains(p, "Write instructions for agents") {
			t.Errorf("%q:\n%s", c.lang, p)
		}
		danger := map[string]string{"": "in the owner's language", "id": "in Bahasa Indonesia.", "en": "in English."}[c.lang]
		if !strings.Contains(p, "one short sentence "+danger) {
			t.Errorf("%q: danger line", c.lang)
		}
	}
}
