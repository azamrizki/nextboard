package orch

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/session"
)

// teamFixture is a running mission of two consoles, @Web and @Api, each working on its task,
// and @Ops outside it.
type teamFixture struct {
	h             *fakeHub
	s             *Service
	now           *time.Time
	ms            MissionRun
	web, api, ops session.Info
}

func newTeamFixture(t *testing.T) *teamFixture {
	t.Helper()
	h, s := newFakeHub(t)
	now := t0
	s.Now = func() time.Time { return now }
	s.Threads.Now = s.Now
	p, wid := h.project("shop")
	f := &teamFixture{h: h, s: s, now: &now}
	f.web = h.console(p, wid, "Web", "claude", agentstate.Working)
	f.api = h.console(p, wid, "Api", "codex", agentstate.Working)
	f.ops = h.console(p, wid, "Ops", "claude", agentstate.Ready)
	approved := t0
	f.ms = MissionRun{ID: "ms_team1", ProjectID: p.ID, ThreadID: p.ID, WorkspaceID: wid, Lead: "Aruna", Goal: "Orders page with its API",
		DoneWhen: "the page lists orders from GET /api/orders", Doc: "GET /api/orders answers a list.", DocRev: 0, State: MissionRunning,
		MaxRounds: 30, Deadline: t0.Add(4 * time.Hour), CreatedAt: t0, UpdatedAt: t0, ApprovedAt: &approved,
		Tasks: []MissionTask{
			{ID: "t1", Handle: "Web", SessionID: f.web.ID, Title: "Build the orders page", Text: "Build /orders.", State: TaskWorking, UpdatedAt: t0},
			{ID: "t2", Handle: "Api", SessionID: f.api.ID, Title: "Build GET /api/orders", Text: "Serve the orders.", State: TaskWorking, UpdatedAt: t0},
		}}
	s.Missions.put(f.ms, t0)
	return f
}

func (f *teamFixture) at(d time.Duration) time.Time {
	*f.now = t0.Add(d)
	return *f.now
}

func (f *teamFixture) mission() MissionRun {
	ms, _ := f.s.Missions.Get(f.ms.ID)
	return ms
}

func (f *teamFixture) teamMsgs() []ThreadMessage { return f.s.TeamMessages(f.mission(), 100) }

func (f *teamFixture) wakes() []string {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if w := f.s.mwake[f.ms.ID]; w != nil {
		return append([]string{}, w.reasons...)
	}
	return nil
}

var qidRe = regexp.MustCompile(`q_[A-Za-z0-9_-]+`)

// A question goes to the teammate as a delivery of source "team", not an order, and is in the
// thread; the answer goes back to the asker and closes it.
func TestTeamAskAndReply(t *testing.T) {
	f := newTeamFixture(t)
	out, err := f.s.TeamAsk(f.web.ID, "@api", "Which fields does GET /api/orders answer?")
	if err != nil || !strings.Contains(out, "Asked @Api as q_") || !strings.Contains(out, "end your turn") {
		t.Fatalf("ask %q %v", out, err)
	}
	qid := qidRe.FindString(out)
	ds := f.h.deliveriesTo(f.api.ID)
	if len(ds) != 1 || ds[0].Source != "team" || ds[0].OrderID != "" ||
		!strings.Contains(ds[0].Text, "@Web asks you (mission \"Orders page with its API\", question "+qid+"): Which fields") || !strings.Contains(ds[0].Text, "team_reply") {
		t.Fatalf("delivery %+v", ds)
	}
	if n := len(f.s.Orders.List(nil)); n != 0 {
		t.Fatalf("a team message is no order: %d", n)
	}
	q, ok := f.mission().Question(qid)
	if !ok || !q.Open() || q.From != "Web" || q.To != "Api" || q.ToID != f.api.ID {
		t.Fatalf("question %+v", q)
	}
	msgs := f.teamMsgs()
	if len(msgs) != 1 || msgs[0].Kind != "team" || msgs[0].Role != "agent" || msgs[0].Handle != "Web" || msgs[0].MissionID != f.ms.ID ||
		*msgs[0].Team != (TeamRef{From: "Web", To: "Api", Kind: "ask", QuestionID: qid}) {
		t.Fatalf("thread %+v", msgs)
	}

	// only the one asked answers, once
	if _, err := f.s.TeamReply(f.web.ID, qid, "id, total"); err == nil || !strings.Contains(err.Error(), "asked of @Api") {
		t.Fatalf("the asker answering %v", err)
	}
	if _, err := f.s.TeamReply(f.api.ID, "q_nope", "x"); err == nil {
		t.Fatal("an unknown question")
	}
	out, err = f.s.TeamReply(f.api.ID, qid, "id, total, createdAt")
	if err != nil || !strings.Contains(out, "@Web gets it") {
		t.Fatalf("reply %q %v", out, err)
	}
	ds = f.h.deliveriesTo(f.web.ID)
	if len(ds) != 1 || ds[0].Source != "team" || ds[0].Text != "@Api answers your question "+qid+": id, total, createdAt" {
		t.Fatalf("answer delivery %+v", ds)
	}
	if q, _ := f.mission().Question(qid); q.Open() || q.Answer != "id, total, createdAt" {
		t.Fatalf("closed %+v", q)
	}
	if _, err := f.s.TeamReply(f.api.ID, qid, "again"); err == nil || !strings.Contains(err.Error(), "answered already") {
		t.Fatalf("twice %v", err)
	}
	if msgs := f.teamMsgs(); len(msgs) != 2 || *msgs[1].Team != (TeamRef{From: "Api", To: "Web", Kind: "reply", QuestionID: qid}) {
		t.Fatalf("thread %+v", msgs)
	}
	if len(f.wakes()) != 0 {
		t.Fatalf("talk between teammates wakes nobody: %v", f.wakes())
	}
	// the turn the message brings is talk, not a result for forward lines
	f.s.mu.Lock()
	n := len(f.s.turnContext[f.web.ID])
	f.s.mu.Unlock()
	if n != 1 {
		t.Fatalf("turn context %d", n)
	}
}

// A question to the lead is recorded and wakes it; nothing is typed anywhere.
func TestTeamAskLead(t *testing.T) {
	f := newTeamFixture(t)
	for _, to := range []string{"lead", "Aruna"} {
		out, err := f.s.TeamAsk(f.api.ID, to, "Paginate the list?")
		if err != nil || !strings.Contains(out, "Asked the lead as q_") {
			t.Fatalf("ask %s: %q %v", to, out, err)
		}
	}
	if w := f.wakes(); len(w) != 2 || !strings.HasPrefix(w[0], "@Api asks you (q_") || !strings.Contains(w[0], "Paginate the list?") {
		t.Fatalf("wakes %v", w)
	}
	if len(f.h.delOrder) != 0 {
		t.Fatalf("deliveries %v", f.h.delOrder)
	}
	if msgs := f.teamMsgs(); len(msgs) != 2 || msgs[0].Team.To != "lead" || msgs[0].Team.Kind != "ask" {
		t.Fatalf("thread %+v", msgs)
	}
	if _, err := f.s.TeamReply(f.web.ID, f.mission().Questions[0].ID, "yes"); err == nil || !strings.Contains(err.Error(), "asked of the lead") {
		t.Fatalf("a teammate answering for the lead %v", err)
	}
}

// A console with no live mission is told so by every tool; whom one may ask is checked; text
// and open questions are limited.
func TestTeamToolsRefused(t *testing.T) {
	f := newTeamFixture(t)
	rev := 0
	for name, call := range map[string]func(sid string) (string, error){
		"read":   f.s.MissionReadText,
		"ask":    func(sid string) (string, error) { return f.s.TeamAsk(sid, "Web", "hi?") },
		"reply":  func(sid string) (string, error) { return f.s.TeamReply(sid, "q_x", "hi") },
		"update": func(sid string) (string, error) { return f.s.TeamUpdate(sid, "hi", "") },
		"doc":    func(sid string) (string, error) { return f.s.MissionDocEdit(sid, "hi", "replace", &rev) },
		"report": func(sid string) (string, error) { return f.s.MissionReport(sid, "done", "hi", "") },
	} {
		if _, err := call(f.ops.ID); !errors.Is(err, ErrNotInMission) || err.Error() != "You are not in a mission." {
			t.Errorf("%s from outside: %v", name, err)
		}
	}
	if _, err := f.s.TeamAsk(f.web.ID, "Ops", "hi?"); err == nil || !strings.Contains(err.Error(), "@Api, lead") {
		t.Fatalf("outside the team %v", err)
	}
	if _, err := f.s.TeamAsk(f.web.ID, "web", "hi?"); err == nil || !strings.Contains(err.Error(), "that is you") {
		t.Fatalf("itself %v", err)
	}
	if _, err := f.s.TeamAsk(f.web.ID, "Api", strings.Repeat("x", teamTextMax+1)); err == nil || !strings.Contains(err.Error(), "at most 4000") {
		t.Fatalf("too long %v", err)
	}
	if _, err := f.s.TeamUpdate(f.web.ID, "  ", ""); err == nil {
		t.Fatal("empty note")
	}
	for i := range MaxOpenQuestions {
		if _, err := f.s.TeamAsk(f.web.ID, "lead", "question "+string(rune('a'+i))); err != nil {
			t.Fatalf("ask %d: %v", i, err)
		}
	}
	if _, err := f.s.TeamAsk(f.web.ID, "Api", "one more?"); err == nil || !strings.Contains(err.Error(), "20 open questions") {
		t.Fatalf("21st question %v", err)
	}
	// a mission waiting for approval has no team yet; an ended one none any more
	f.s.UpdateMission(f.ms.ID, func(m *MissionRun) bool { m.State = MissionAwaiting; return true })
	if _, err := f.s.MissionReadText(f.web.ID); !errors.Is(err, ErrNotInMission) {
		t.Fatalf("awaiting %v", err)
	}
	f.s.UpdateMission(f.ms.ID, func(m *MissionRun) bool { m.State = MissionDone; return true })
	if _, err := f.s.MissionReadText(f.web.ID); !errors.Is(err, ErrNotInMission) {
		t.Fatalf("done %v", err)
	}
}

// A console gets at most one team delivery every 10 s, and none while the last waits to be
// typed; what comes meanwhile goes as one.
func TestTeamPacing(t *testing.T) {
	f := newTeamFixture(t)
	if _, err := f.s.TeamUpdate(f.web.ID, "The page calls /api/orders?page=1.", "Api"); err != nil {
		t.Fatal(err)
	}
	f.s.TeamUpdate(f.web.ID, "And sorts by createdAt.", "Api")
	ds := f.h.deliveriesTo(f.api.ID)
	if len(ds) != 1 || !strings.HasPrefix(ds[0].Text, "@Web tells you (mission \"Orders page with its API\"): The page calls") {
		t.Fatalf("first %+v", ds)
	}
	// typed at 5 s, still within the 10 s
	f.at(5 * time.Second)
	f.h.setDelivery(f.s, ds[0].ID, delivery.Sent)
	f.s.teamTick(*f.now)
	if n := len(f.h.deliveriesTo(f.api.ID)); n != 1 {
		t.Fatalf("within 10 s: %d", n)
	}
	f.s.TeamUpdate(f.web.ID, "Dates in UTC.", "Api")
	f.s.teamTick(f.at(10 * time.Second))
	ds = f.h.deliveriesTo(f.api.ID)
	if len(ds) != 2 || ds[1].Text != "@Web tells you (mission \"Orders page with its API\"): And sorts by createdAt.\n\n@Web tells you (mission \"Orders page with its API\"): Dates in UTC." {
		t.Fatalf("held ones as one: %+v", ds)
	}
	// not typed yet: the next waits for it even past 10 s
	f.s.TeamUpdate(f.web.ID, "One more thing.", "Api")
	f.s.teamTick(f.at(30 * time.Second))
	if n := len(f.h.deliveriesTo(f.api.ID)); n != 2 {
		t.Fatalf("while one waits: %d", n)
	}
	f.h.setDelivery(f.s, ds[1].ID, delivery.Confirmed)
	f.s.teamTick(f.at(31 * time.Second))
	if ds = f.h.deliveriesTo(f.api.ID); len(ds) != 3 || !strings.HasSuffix(ds[2].Text, "One more thing.") {
		t.Fatalf("after it was typed: %+v", ds)
	}
	// another console has its own pace
	f.s.TeamUpdate(f.api.ID, "Endpoint is up.", "Web")
	if n := len(f.h.deliveriesTo(f.web.ID)); n != 1 {
		t.Fatalf("other console %d", n)
	}
	// a note to nobody, or to the lead, is only recorded
	before := len(f.h.delOrder)
	if out, err := f.s.TeamUpdate(f.web.ID, "Pushed the page.", ""); err != nil || !strings.Contains(out, "lead reads it") {
		t.Fatalf("note %q %v", out, err)
	}
	f.s.TeamUpdate(f.web.ID, "FYI", "lead")
	if len(f.h.delOrder) != before || len(f.wakes()) != 0 {
		t.Fatalf("recorded only: %d deliveries, wakes %v", len(f.h.delOrder)-before, f.wakes())
	}
	if msgs := f.teamMsgs(); msgs[len(msgs)-2].Team.To != "" || msgs[len(msgs)-1].Team.To != "lead" || msgs[len(msgs)-1].Team.Kind != "update" {
		t.Fatalf("thread %+v", msgs[len(msgs)-2:])
	}
}

// A pause takes back a team delivery not typed yet and holds what comes; resume sends it all;
// an ended mission drops what is held.
func TestTeamDeliveriesFollowPause(t *testing.T) {
	f := newTeamFixture(t)
	f.s.TeamUpdate(f.web.ID, "first", "Api")
	first := f.h.deliveriesTo(f.api.ID)[0]
	f.s.UpdateMission(f.ms.ID, func(m *MissionRun) bool { m.State = MissionPaused; return true })
	f.s.teamTick(f.at(time.Second))
	if d, _ := f.h.Delivery(first.ID); d.State != delivery.Cancelled {
		t.Fatalf("not taken back: %s", d.State)
	}
	f.s.TeamUpdate(f.web.ID, "second", "Api")
	f.s.teamTick(f.at(20 * time.Second))
	if n := len(f.h.deliveriesTo(f.api.ID)); n != 1 {
		t.Fatalf("sent while paused: %d", n)
	}
	f.s.UpdateMission(f.ms.ID, func(m *MissionRun) bool { m.State = MissionRunning; return true })
	f.s.teamTick(f.at(21 * time.Second))
	ds := f.h.deliveriesTo(f.api.ID)
	if len(ds) != 2 || !strings.HasSuffix(ds[1].Text, ": first\n\n@Web tells you (mission \"Orders page with its API\"): second") {
		t.Fatalf("after resume %+v", ds)
	}
	// the owner cancels one himself: it stays cancelled
	f.h.CancelDelivery(ds[1].ID)
	f.s.TeamUpdate(f.web.ID, "third", "Api")
	f.s.UpdateMission(f.ms.ID, func(m *MissionRun) bool { m.State = MissionStopped; return true })
	f.s.teamTick(f.at(40 * time.Second))
	if n := len(f.h.deliveriesTo(f.api.ID)); n != 2 {
		t.Fatalf("after stop %d", n)
	}
	f.s.team.mu.Lock()
	held, pending := len(f.s.team.held), len(f.s.team.pending)
	f.s.team.mu.Unlock()
	if held != 0 || pending != 0 {
		t.Fatalf("left over: %d held, %d pending", held, pending)
	}
}

// mission_doc appends, or replaces at the rev read; a stale rev is refused.
func TestMissionDoc(t *testing.T) {
	f := newTeamFixture(t)
	out, err := f.s.MissionDocEdit(f.api.ID, "Errors answer {\"error\": \"…\"}.", "append", nil)
	if err != nil || !strings.Contains(out, "rev 1") {
		t.Fatalf("append %q %v", out, err)
	}
	if ms := f.mission(); ms.DocRev != 1 || ms.Doc != "GET /api/orders answers a list.\n\nErrors answer {\"error\": \"…\"}." {
		t.Fatalf("doc %d %q", ms.DocRev, ms.Doc)
	}
	stale := 0
	if _, err := f.s.MissionDocEdit(f.web.ID, "mine", "replace", &stale); err == nil || !strings.Contains(err.Error(), "changed since rev 0 (it is at rev 1)") {
		t.Fatalf("stale rev %v", err)
	}
	if _, err := f.s.MissionDocEdit(f.web.ID, "mine", "replace", nil); err == nil || !strings.Contains(err.Error(), "needs rev") {
		t.Fatalf("no rev %v", err)
	}
	cur := 1
	if _, err := f.s.MissionDocEdit(f.web.ID, "# Contract\nGET /api/orders?page=", "replace", &cur); err != nil {
		t.Fatal(err)
	}
	if ms := f.mission(); ms.DocRev != 2 || ms.Doc != "# Contract\nGET /api/orders?page=" {
		t.Fatalf("replaced %d %q", ms.DocRev, ms.Doc)
	}
	if _, err := f.s.MissionDocEdit(f.web.ID, strings.Repeat("x", maxMissionDoc), "append", nil); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("too large %v", err)
	}
	if _, err := f.s.MissionDocEdit(f.web.ID, "x", "rewrite", nil); err == nil {
		t.Fatal("bad mode")
	}
	msgs := f.teamMsgs()
	if len(msgs) != 2 || msgs[0].Team.Kind != "doc" || !strings.HasPrefix(msgs[0].Text, "updated the brief (rev 1): Errors answer") || msgs[1].Text != "updated the brief (rev 2)" {
		t.Fatalf("thread %+v", msgs)
	}
	if len(f.wakes()) != 0 || len(f.h.delOrder) != 0 {
		t.Fatalf("the brief wakes and types nothing: %v %v", f.wakes(), f.h.delOrder)
	}
}

// mission_report done marks the agent's task done with its summary and wakes the lead;
// blocked and needs wake it; progress is only recorded.
func TestMissionReport(t *testing.T) {
	f := newTeamFixture(t)
	if out, err := f.s.MissionReport(f.api.ID, "progress", "Model and migration done.", ""); err != nil || out != "Noted." || len(f.wakes()) != 0 {
		t.Fatalf("progress %q %v %v", out, err, f.wakes())
	}
	if _, err := f.s.MissionReport(f.api.ID, "finished", "x", ""); err == nil {
		t.Fatal("bad status")
	}
	if _, err := f.s.MissionReport(f.api.ID, "done", " ", ""); err == nil {
		t.Fatal("no summary")
	}
	out, err := f.s.MissionReport(f.api.ID, "done", "GET /api/orders in routes/api.php; 4 tests pass.", "")
	if err != nil || !strings.Contains(out, "Reported t2 done") {
		t.Fatalf("done %q %v", out, err)
	}
	task, _ := f.mission().Task("t2")
	if task.State != TaskDone || task.Result != "GET /api/orders in routes/api.php; 4 tests pass." || task.ResultBy != "report" {
		t.Fatalf("task %+v", task)
	}
	if w := f.wakes(); len(w) != 1 || !strings.Contains(w[0], "t2 done") {
		t.Fatalf("wakes %v", w)
	}
	out, err = f.s.MissionReport(f.web.ID, "blocked", "The API answers 500.", "a fix of GET /api/orders")
	if err != nil || !strings.Contains(out, "blocked") {
		t.Fatalf("blocked %q %v", out, err)
	}
	if w := f.wakes(); len(w) != 2 || w[1] != "@Web is blocked on t1: The API answers 500. Needs: a fix of GET /api/orders" {
		t.Fatalf("wakes %v", w)
	}
	f.s.MissionReport(f.web.ID, "progress", "Styling.", "the brand colours")
	if w := f.wakes(); len(w) != 3 || !strings.Contains(w[2], "needs (t1): the brand colours") {
		t.Fatalf("needs wakes %v", w)
	}
	msgs := f.teamMsgs()
	if len(msgs) != 4 || msgs[1].Team.Kind != "report" || msgs[1].Team.To != "lead" || !strings.HasPrefix(msgs[1].Text, "t2 done: GET /api/orders") ||
		!strings.HasSuffix(msgs[2].Text, "\nNeeds: a fix of GET /api/orders") {
		t.Fatalf("thread %+v", msgs)
	}
}

// mission_read shows the mission as the agent needs it: goal, its tasks, the team, the brief,
// the questions to it and the latest talk that involves it.
func TestMissionReadText(t *testing.T) {
	f := newTeamFixture(t)
	out, _ := f.s.TeamAsk(f.web.ID, "Api", "Which fields?")
	qid := qidRe.FindString(out)
	f.s.TeamAsk(f.web.ID, "lead", "Paginate?")
	f.s.TeamUpdate(f.web.ID, "a note only for the lead", "lead")
	f.at(3 * time.Minute)
	text, err := f.s.MissionReadText(f.api.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Mission: Orders page with its API\n", "State: running\n", "Done when: the page lists orders", "Lead: Aruna",
		"You: @Api\n", "Your tasks:\n- t2 [working] Build GET /api/orders\n  Serve the orders.\n",
		"- @Web: working; t1 Build the orders page [working]\n", "- @Api (you): working; t2 Build GET /api/orders [working]\n",
		"Shared brief (rev 0; mission_doc changes it):\nGET /api/orders answers a list.\n",
		"Questions to you, answer each with team_reply:\n- " + qid + " from @Web: Which fields?\n",
		"- 3 min ago, @Web → @Api (ask " + qid + "): Which fields?\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Paginate?") || strings.Contains(text, "only for the lead") || strings.Contains(text, "Build /orders.") {
		t.Errorf("what does not involve @Api:\n%s", text)
	}
	mine, _ := f.s.MissionReadText(f.web.ID)
	if !strings.Contains(mine, "Your questions still waiting for an answer:\n- "+qid+" to @Api, 3 min ago: Which fields?") || !strings.Contains(mine, "to the lead") {
		t.Errorf("the asker's view:\n%s", mine)
	}
}

// A team delivery still queued when the mission ends is cancelled at once, and nothing held
// goes out after it: Stop leaves nothing to type.
func TestTeamStopCancelsQueued(t *testing.T) {
	f := newTeamFixture(t)
	f.s.TeamUpdate(f.web.ID, "first", "Api")
	f.s.TeamUpdate(f.web.ID, "second, held", "Api")
	queued := f.h.deliveriesTo(f.api.ID)
	if len(queued) != 1 || queued[0].State != delivery.Queued {
		t.Fatalf("queued %+v", queued)
	}
	if _, err := f.s.StopMission(f.ms.ID); err != nil {
		t.Fatal(err)
	}
	if d, _ := f.h.Delivery(queued[0].ID); d.State != delivery.Cancelled {
		t.Fatalf("still to be typed after Stop: %s", d.State)
	}
	f.s.teamTick(f.at(time.Minute))
	if ds := f.h.deliveriesTo(f.api.ID); len(ds) != 1 {
		t.Fatalf("sent after Stop: %+v", ds)
	}

	// an end the tick finds (not through the owner's controls) does the same
	g := newTeamFixture(t)
	g.s.TeamUpdate(g.web.ID, "first", "Api")
	id := g.h.deliveriesTo(g.api.ID)[0].ID
	g.s.UpdateMission(g.ms.ID, func(m *MissionRun) bool { m.State = MissionDone; return true })
	g.s.teamTick(g.at(time.Second))
	if d, _ := g.h.Delivery(id); d.State != delivery.Cancelled {
		t.Fatalf("ended: %s", d.State)
	}
	g.s.team.mu.Lock()
	held, pending := len(g.s.team.held), len(g.s.team.pending)
	g.s.team.mu.Unlock()
	if held != 0 || pending != 0 {
		t.Fatalf("left over: %d held, %d pending", held, pending)
	}
}

// While the mission waits for the owner at a limit (or for no progress), the team's talk waits
// too, and goes out when the owner lets the mission go on. A question to the owner holds
// nothing.
func TestTeamHeldAtLimit(t *testing.T) {
	f := newTeamFixture(t)
	f.s.TeamUpdate(f.web.ID, "first", "Api")
	first := f.h.deliveriesTo(f.api.ID)[0]
	f.s.limitReached(f.mission())
	if ms := f.mission(); ms.State != MissionWaiting || !teamHolds(ms) {
		t.Fatalf("limit %s %q", ms.State, ms.Waiting)
	}
	f.s.teamTick(f.at(time.Second))
	if d, _ := f.h.Delivery(first.ID); d.State != delivery.Cancelled {
		t.Fatalf("not taken back at the limit: %s", d.State)
	}
	if out, err := f.s.TeamUpdate(f.web.ID, "second", "Api"); err != nil || !strings.Contains(out, "gets it") {
		t.Fatalf("while held %q %v", out, err)
	}
	f.s.teamTick(f.at(30 * time.Second))
	if n := len(f.h.deliveriesTo(f.api.ID)); n != 1 {
		t.Fatalf("sent at the limit: %d", n)
	}
	if _, err := f.s.ResumeMission(f.ms.ID); err != nil {
		t.Fatal(err)
	}
	f.s.teamTick(f.at(31 * time.Second))
	ds := f.h.deliveriesTo(f.api.ID)
	if len(ds) != 2 || !strings.HasSuffix(ds[1].Text, ": first\n\n@Web tells you (mission \"Orders page with its API\"): second") {
		t.Fatalf("after going on %+v", ds)
	}

	g := newTeamFixture(t)
	g.s.UpdateMission(g.ms.ID, func(m *MissionRun) bool {
		m.State, m.Waiting = MissionWaiting, "Which colour for the buttons?"
		return true
	})
	g.s.TeamUpdate(g.web.ID, "first", "Api")
	if n := len(g.h.deliveriesTo(g.api.ID)); n != 1 {
		t.Fatalf("a question to the owner holds the team: %d", n)
	}
}

// Two agents may send each other 20 messages an hour, a mission's agents 60 in all; past
// that the tool says so, nothing is sent or recorded, and the lead hears it once.
func TestTeamBudget(t *testing.T) {
	f := newTeamFixture(t)
	p, _ := f.h.projects.Get(f.ms.ProjectID)
	db := f.h.console(p, f.ms.WorkspaceID, "Db", "claude", agentstate.Working)
	f.s.UpdateMission(f.ms.ID, func(m *MissionRun) bool {
		m.Tasks = append(m.Tasks, MissionTask{ID: "t3", Handle: "Db", SessionID: db.ID, Title: "Schema", State: TaskWorking, UpdatedAt: t0})
		return true
	})
	send := func(from session.Info, to string, i int) error {
		_, err := f.s.TeamUpdate(from.ID, fmt.Sprintf("note %d", i), to)
		return err
	}
	for i := range teamBudgetPair {
		// both ways count for the pair
		from, to := f.web, "Api"
		if i%2 == 1 {
			from, to = f.api, "Web"
		}
		if err := send(from, to, i); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	before := len(f.teamMsgs())
	err := send(f.web, "Api", 99)
	if err == nil || !strings.Contains(err.Error(), "you and @Api have sent each other 20 messages in the last hour") {
		t.Fatalf("pair budget %v", err)
	}
	if _, err := f.s.TeamAsk(f.api.ID, "Web", "one more?"); err == nil || !strings.Contains(err.Error(), "@Web have sent each other 20") {
		t.Fatalf("pair budget for a question %v", err)
	}
	if len(f.teamMsgs()) != before || len(f.mission().Questions) != 0 {
		t.Fatal("a message over the budget was recorded")
	}
	if w := f.wakes(); len(w) != 1 || w[0] != "@Web and @Api have used up their messages to each other (20 in the last hour)" {
		t.Fatalf("wakes %v", w)
	}
	// the others still talk, until the mission's 60
	for i := range teamBudgetPair {
		if err := send(f.web, "Db", i); err != nil {
			t.Fatalf("web→db %d: %v", i, err)
		}
		if err := send(db, "Api", i); err != nil {
			t.Fatalf("db→api %d: %v", i, err)
		}
	}
	if err := send(db, "Api", 99); err == nil || !strings.Contains(err.Error(), "60 messages between agents in the last hour") {
		t.Fatalf("mission budget %v", err)
	}
	if len(f.wakes()) != 1 {
		t.Fatalf("the lead hears it once an hour: %v", f.wakes())
	}
	// the lead, and the thread alone, stay open
	if _, err := f.s.TeamAsk(f.web.ID, "lead", "Shall we go on?"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TeamUpdate(f.web.ID, "only for the record", ""); err != nil {
		t.Fatal(err)
	}
	// an hour on, the budget is back
	f.at(61 * time.Minute)
	if err := send(f.web, "Api", 100); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
}
