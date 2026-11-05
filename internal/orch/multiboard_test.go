package orch

import (
	"encoding/json"
	"strings"
	"testing"

	"nextboard/internal/agentstate"
	"nextboard/internal/auth"
	"nextboard/internal/board"
	"nextboard/internal/project"
)

func twoBoards(t *testing.T) (*fakeHub, *Service, project.Project, string, string) {
	t.Helper()
	h, s := newFakeHub(t)
	p, main := h.project("app")
	w, err := h.CreateWorkspace(p.ID, "Second")
	if err != nil {
		t.Fatal(err)
	}
	p, _ = h.projects.Get(p.ID)
	return h, s, p, main, w.ID
}

func threadTexts(s *Service, tid, kind string) []string {
	var out []string
	for _, m := range s.Threads.Last(tid, 100) {
		if m.Kind == kind {
			out = append(out, m.Text)
		}
	}
	return out
}

// TestPlannerAnswersOnce: a planner that repeats its answer as a "reply" step
// shows it once; a different reply step still goes out.
func TestPlannerAnswersOnce(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.console(p, wid, "Bea", "claude", agentstate.Ready)
	s.Command(CommandRequest{Text: "what is going on over there", WorkspaceID: wid})
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "All quiet.", "steps": []any{
		map[string]any{"op": "reply", "text": " All quiet. "},
		map[string]any{"op": "reply", "text": "Nothing to do."},
		map[string]any{"op": "reply", "text": "Nothing to do."},
	}}))
	got := threadTexts(s, p.ID, "reply")
	if strings.Join(got, "|") != "All quiet.|Nothing to do." {
		t.Fatalf("replies %q", got)
	}
}

func TestCommandBarReachesEveryBoard(t *testing.T) {
	h, s, p, main, second := twoBoards(t)
	ash := h.console(p, main, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, second, "Bea", "codex", agentstate.Ready)
	other, otherWs := h.project("other")
	h.console(other, otherWs, "Cid", "claude", agentstate.Ready)
	send := func(text string) CommandResponse {
		t.Helper()
		res, err := s.Command(CommandRequest{Text: text, WorkspaceID: main, Planner: "off"})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	targetsOf := func() map[string]bool {
		out := map[string]bool{}
		for _, d := range h.dels {
			out[d.SessionID] = true
		}
		return out
	}

	send("status")
	if r := threadTexts(s, p.ID, "reply"); !strings.Contains(r[len(r)-1], "2 agent(s)") || !strings.Contains(r[len(r)-1], "Main: 1, Second: 1.") {
		t.Fatalf("status %q", r)
	}

	send("tell everyone: run the tests")
	got := targetsOf()
	if !got[ash.ID] || !got[bea.ID] || len(got) != 2 {
		t.Fatalf("everyone reached %v, other project must stay out", got)
	}

	// A group on another board, named from this one.
	if _, err := s.CreateGroup(second, []string{bea.ID}, "Backend", "backend"); err != nil {
		t.Fatal(err)
	}
	n := len(h.delReqs)
	send("@Backend hello")
	if len(h.delReqs) != n+1 {
		t.Fatalf("group on another board got %d deliveries", len(h.delReqs)-n)
	}

	// Going to a console on another board carries the board to open.
	res := send("go to Bea")
	if len(res.UIActions) != 1 || res.UIActions[0].WorkspaceID != second || res.UIActions[0].Target != h.node(second, bea.ID).ID {
		t.Fatalf("focus %+v", res.UIActions)
	}

	// The prompt says a kill spans boards.
	if res := send("kill all"); res.Confirmation == nil || !strings.Contains(res.Confirmation.Prompt, "(on 2 boards)") {
		t.Fatalf("kill all %+v", res.Confirmation)
	}
	send("no")

	// A group is one board: mixed members are refused, not silently dropped.
	send("group Ash and Cid")
	if e := threadTexts(s, p.ID, "error"); len(e) == 0 || !strings.Contains(e[len(e)-1], ErrMixedBoards.Error()) {
		t.Fatalf("mixed group: %q", e)
	}

	// Dissolving acts on the board the group is on.
	send("ungroup Backend")
	b, _ := h.boards.Get(second)
	for _, n := range b.Nodes {
		if n.Kind == KindGroup {
			t.Fatal("group still on its board")
		}
	}

	// A loop stays on one board and says so in its estimate.
	res = send("every 15 minutes run the tests")
	if res.Confirmation == nil || !strings.Contains(res.Confirmation.Prompt, "× 1 console(s)") {
		t.Fatalf("loop %+v", res.Confirmation)
	}
}

// TestCardStaysOnItsBoard: an orchestrator card directs its own board only.
func TestCardStaysOnItsBoard(t *testing.T) {
	h, s, p, main, second := twoBoards(t)
	ash := h.console(p, main, "Ash", "claude", agentstate.Ready)
	h.console(p, second, "Bea", "claude", agentstate.Ready)
	cardID := auth.RandomID("n_")
	card := &board.Node{ID: cardID, Kind: KindOrchestrator, W: 420, H: 520, Data: json.RawMessage(`{"title":"Lead"}`)}
	wire := &board.Edge{ID: auth.RandomID("e_"), Kind: EdgeWire, Source: cardID, Target: h.node(main, ash.ID).ID, Data: json.RawMessage(`{}`)}
	if err := h.ApplyOps(main, []board.Op{{Op: "put", Node: card}, {Op: "putEdge", Edge: wire}}); err != nil {
		t.Fatal(err)
	}
	sc, err := s.newScope(main, cardID)
	if err != nil {
		t.Fatal(err)
	}
	if l := sc.agentsInScope(); len(l) != 1 || l[0].ID != ash.ID {
		t.Fatalf("card scope %+v", l)
	}
	if r := sc.resolve([]string{"Bea"}); len(r.notWired) != 1 {
		t.Fatalf("Bea from the card: %+v", r)
	}
	// Wiring from the main bar refuses consoles of another board.
	if _, err := s.Command(CommandRequest{Text: "connect Bea to the orchestrator", WorkspaceID: main, Planner: "off"}); err != nil {
		t.Fatal(err)
	}
	if n := threadTexts(s, p.ID, "note"); len(n) == 0 || !strings.Contains(n[len(n)-1], "its own board") {
		t.Fatalf("wire notes %q", n)
	}
	b, _ := h.boards.Get(main)
	if len(b.Edges) != 1 {
		t.Fatalf("edges %+v", b.Edges)
	}
}

func TestPlannerSeesAndActsOnEveryBoard(t *testing.T) {
	h, s, p, main, second := twoBoards(t)
	h.console(p, main, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, second, "Bea", "claude", agentstate.Ready)
	note := &board.Node{ID: auth.RandomID("n_"), Kind: "note", W: 200, H: 100, Data: json.RawMessage(`{"text":"ship on friday","color":"yellow"}`)}
	if err := h.ApplyOps(second, []board.Op{{Op: "put", Node: note}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGroup(second, []string{bea.ID}, "Backend", "backend"); err != nil {
		t.Fatal(err)
	}
	c := &execCtx{threadID: p.ID, workspaceID: main}
	sc, err := s.newScope(main, "")
	if err != nil {
		t.Fatal(err)
	}
	in := s.plannerSnapshot(c, sc, "x")
	raw, _ := json.Marshal(in)
	for _, want := range []string{`"name":"Second"`, `"current":true`, `"label":"ship on friday"`, `"board":"Second"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("snapshot lacks %s: %s", want, raw)
		}
	}

	steps, notes := s.translatePlan(sc, planOutput{Steps: []plannerStep{
		{Op: "send", Targets: []string{"Backend"}, Text: "hello"},
		{Op: "node.add", Kind: "note", Text: "from the bar", Workspace: "Second"},
		{Op: "spawn", Count: 1, Workspace: "Nope"},
		{Op: "group.add", Group: "Backend", Count: 1},
	}})
	if len(steps) != 3 || len(notes) != 1 {
		t.Fatalf("steps %+v notes %q", steps, notes)
	}
	if steps[0].Targets[0] != bea.ID || steps[1].WorkspaceID != second || steps[2].WorkspaceID != second {
		t.Fatalf("translated %+v", steps)
	}
	s.execute(c, steps[1:2])
	b, _ := h.boards.Get(second)
	found := false
	for _, n := range b.Nodes {
		found = found || (n.Kind == "note" && strings.Contains(string(n.Data), "from the bar"))
	}
	if !found {
		t.Fatal("note did not land on the named board")
	}
}

func TestSyncPeers(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	b := h.console(p, wid, "Bea", "claude", agentstate.Working)
	c := h.console(p, wid, "Cid", "claude", agentstate.Ready)
	d := h.console(p, wid, "Dee", "claude", agentstate.Ready)
	if _, err := s.CreateGroup(wid, []string{a.ID, b.ID, c.ID}, "Team", ""); err != nil {
		t.Fatal(err)
	}
	if got := s.SyncPeers(a.ID); len(got) != 0 {
		t.Fatalf("not linked yet: %v", got)
	}
	bd, _ := h.boards.Get(wid)
	gid := ""
	for _, n := range bd.Nodes {
		if n.Kind == KindGroup {
			gid = n.ID
		}
	}
	link := func(on bool) {
		raw, _ := json.Marshal(map[string]any{"syncInput": on})
		if err := h.ApplyOps(wid, []board.Op{{Op: "patch", ID: gid, Set: map[string]json.RawMessage{"data": raw}}}); err != nil {
			t.Fatal(err)
		}
	}
	link(true)
	if got := s.SyncPeers(a.ID); len(got) != 2 || got[0] != b.ID || got[1] != c.ID {
		t.Fatalf("peers of Ash: %v", got)
	}
	if got := s.SyncPeers(d.ID); len(got) != 0 {
		t.Fatalf("outside the group: %v", got)
	}
	h.mu.Lock()
	h.sessions[b.ID].Paused = true
	h.mu.Unlock()
	if got := s.SyncPeers(a.ID); len(got) != 1 || got[0] != c.ID {
		t.Fatalf("a paused console takes no input: %v", got)
	}
	link(false)
	if got := s.SyncPeers(a.ID); len(got) != 0 {
		t.Fatalf("unlinked: %v", got)
	}
}
