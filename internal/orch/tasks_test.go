package orch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nextboard/internal/agentstate"
	"nextboard/internal/board"
	"nextboard/internal/delivery"
	"nextboard/internal/project"
	"nextboard/internal/session"
)

// taskBoardSetup: a lead wired to a console and to a kanban.
func taskBoardSetup(t *testing.T, h *fakeHub, autoRun bool, handles ...string) (project.Project, string, string, []session.Info) {
	t.Helper()
	p, wid := h.project("app")
	var cons []session.Info
	ops := []board.Op{
		{Op: "put", Node: &board.Node{ID: "n_lead0001", Kind: KindOrchestrator, W: 340, H: 400, Data: json.RawMessage(`{"title":"Nara"}`)}},
		{Op: "put", Node: &board.Node{ID: "n_task0001", Kind: "kanban", W: 900, H: 420, Data: json.RawMessage(`{"title":"Sprint","autoRun":` + map[bool]string{true: "true", false: "false"}[autoRun] + `,"columns":[
			{"id":"c1","title":"Backlog","cards":[]},{"id":"c2","title":"To do","cards":[]},{"id":"c3","title":"In progress","cards":[]},{"id":"c4","title":"Done","cards":[]}]}`)}},
		{Op: "putEdge", Edge: &board.Edge{ID: "e_task0001", Kind: EdgeWire, Source: "n_lead0001", Target: "n_task0001"}},
	}
	for i, hd := range handles {
		in := h.console(p, wid, hd, "claude", agentstate.Ready)
		cons = append(cons, in)
		ops = append(ops, board.Op{Op: "putEdge", Edge: &board.Edge{ID: "e_team000" + itoa(i), Kind: EdgeWire, Source: "n_lead0001", Target: h.node(wid, in.ID).ID}})
	}
	if err := h.ApplyOps(wid, ops); err != nil {
		t.Fatal(err)
	}
	return p, wid, "n_task0001", cons
}

type kanbanView struct {
	Columns []struct {
		Title string `json:"title"`
		Cards []struct {
			ID, Title, Notes, Assignee, State, OrderID, SessionID, Error string
			Done                                                         bool
		} `json:"cards"`
	} `json:"columns"`
}

func readBoardKanban(t *testing.T, h *fakeHub, wid, id string) kanbanView {
	t.Helper()
	b, err := h.Board(wid)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range b.Nodes {
		if n.ID == id {
			var v kanbanView
			if err := json.Unmarshal(n.Data, &v); err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("no tile %s", id)
	return kanbanView{}
}

func columnTitles(v kanbanView) map[string][]string {
	out := map[string][]string{}
	for _, c := range v.Columns {
		for _, k := range c.Cards {
			out[c.Title] = append(out[c.Title], k.Title)
		}
	}
	return out
}

// An order to the team becomes a card per console, and the card follows the order.
func TestTaskBoardFollowsOrders(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid, tile, cons := taskBoardSetup(t, h, false, "Ash", "Bea")
	c := &execCtx{threadID: p.ID, projectID: p.ID, workspaceID: wid}

	o := s.sendOrder(c, []string{cons[0].ID, cons[1].ID}, "@Ash @Bea Fix the login page\nThe redirect loops on Safari.", "order", 0)
	v := readBoardKanban(t, h, wid, tile)
	got := columnTitles(v)
	if len(got["To do"])+len(got["In progress"]) != 2 {
		t.Fatalf("a card per console: %+v", got)
	}
	for _, col := range v.Columns {
		for _, k := range col.Cards {
			if k.Title != "Fix the login page" || !strings.Contains(k.Notes, "Safari") || k.OrderID != o.ID {
				t.Fatalf("card %+v", k)
			}
		}
	}

	// sent: in progress
	for _, tg := range o.Targets {
		h.setDelivery(s, tg.DeliveryID, delivery.Confirmed)
	}
	v = readBoardKanban(t, h, wid, tile)
	if got = columnTitles(v); len(got["In progress"]) != 2 || len(got["To do"]) != 0 {
		t.Fatalf("not in progress: %+v", got)
	}

	// A card the user moved by hand stays while the state is the same.
	o2 := readBoardKanban(t, h, wid, tile)
	if o2.Columns[2].Cards[0].State != taskWorking {
		t.Fatalf("state %+v", o2.Columns[2].Cards[0])
	}

	// finished: done, checked, with who did it
	h.setState(cons[0].ID, agentstate.Working)
	now := s.Now()
	s.turnFinished(cons[0], agentstate.LastTurn{EndedAt: &now}, now.Add(time.Second))
	v = readBoardKanban(t, h, wid, tile)
	if got = columnTitles(v); len(got["Done"]) != 1 || len(got["In progress"]) != 1 {
		t.Fatalf("one finished: %+v", got)
	}
	var doneCard = v.Columns[3].Cards[0]
	if !doneCard.Done || doneCard.Assignee != "Ash" || doneCard.State != taskFinished {
		t.Fatalf("done card %+v", doneCard)
	}

	// a failed delivery stays where it is and says why
	x, _ := s.Orders.Get(o.ID)
	for _, tg := range x.Targets {
		if tg.SessionID == cons[1].ID {
			h.setDelivery(s, tg.DeliveryID, delivery.Failed)
		}
	}
	v = readBoardKanban(t, h, wid, tile)
	if got = columnTitles(v); len(got["In progress"]) != 1 || v.Columns[2].Cards[0].State != taskFailed {
		t.Fatalf("failed: %+v", v.Columns[2].Cards)
	}

	// Forwards between agents are no work the owner handed out.
	before := len(allTasksOf(t, h, wid, tile))
	s.sendOrder(c, []string{cons[0].ID}, "results from @Bea", "forward", 1)
	if n := len(allTasksOf(t, h, wid, tile)); n != before {
		t.Fatalf("a forward made a card: %d → %d", before, n)
	}
}

func allTasksOf(t *testing.T, h *fakeHub, wid, id string) []string {
	var out []string
	for _, c := range readBoardKanban(t, h, wid, id).Columns {
		for _, k := range c.Cards {
			out = append(out, k.Title)
		}
	}
	return out
}

// A console no lead of this board directs leaves the board alone.
func TestTaskBoardOnlyForItsTeam(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid, tile, _ := taskBoardSetup(t, h, false, "Ash")
	other := h.console(p, wid, "Kit", "claude", agentstate.Ready)
	s.sendOrder(&execCtx{threadID: p.ID, projectID: p.ID, workspaceID: wid}, []string{other.ID}, "not this team", "order", 0)
	if n := len(allTasksOf(t, h, wid, tile)); n != 0 {
		t.Fatalf("%d cards", n)
	}
}

// With autoRun on, what waits in To do goes to a free agent, one at a time per agent.
func TestTaskBoardHandsOutTasks(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid, tile, cons := taskBoardSetup(t, h, true, "Ash")
	c := &execCtx{threadID: p.ID, projectID: p.ID, workspaceID: wid}
	for _, title := range []string{"First", "Second"} {
		if err := s.applyTaskEdit(c, Step{Op: "task.add", Tile: tile, Task: &TaskEdit{Title: title, Notes: title + " in detail"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.applyTaskEdit(c, Step{Op: "task.add", Tile: tile, Task: &TaskEdit{Title: "Later", Column: "Backlog"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyTaskEdit(c, Step{Op: "task.add", Tile: tile, Task: &TaskEdit{Title: "For nobody here", Assignee: "@Zed"}}); err != nil {
		t.Fatal(err)
	}
	s.tasksTick(s.Now().Add(time.Minute))
	ds := h.deliveriesTo(cons[0].ID)
	if len(ds) != 1 || !strings.Contains(ds[0].Text, "First") || !strings.Contains(ds[0].Text, "in detail") {
		t.Fatalf("handed out: %+v", ds)
	}
	v := readBoardKanban(t, h, wid, tile)
	if got := columnTitles(v); strings.Join(got["To do"], ",") != "First,Second,For nobody here" {
		t.Fatalf("waits in its place until it is sent: %+v", got)
	}
	// busy: nothing more goes out
	s.tasksTick(s.Now().Add(2 * time.Minute))
	if n := len(h.deliveriesTo(cons[0].ID)); n != 1 {
		t.Fatalf("%d deliveries while busy", n)
	}
	// It turned off: nothing is handed out, whoever is free.
	if err := h.ApplyOps(wid, []board.Op{{Op: "patch", ID: tile, Set: map[string]json.RawMessage{"data": json.RawMessage(`{"autoRun":null}`)}}}); err != nil {
		t.Fatal(err)
	}
	x, _ := s.Orders.Get(ds[0].OrderID)
	h.setDelivery(s, x.Targets[0].DeliveryID, delivery.Confirmed)
	now := s.Now()
	s.turnFinished(cons[0], agentstate.LastTurn{EndedAt: &now}, now.Add(time.Second))
	h.setState(cons[0].ID, agentstate.Ready)
	s.tasksTick(s.Now().Add(3 * time.Minute))
	if n := len(h.deliveriesTo(cons[0].ID)); n != 1 {
		t.Fatalf("%d deliveries with autoRun off", n)
	}
	if got := columnTitles(readBoardKanban(t, h, wid, tile)); len(got["Done"]) != 1 {
		t.Fatalf("the card did not finish: %+v", got)
	}
}

// What the planner changes on a board.
func TestPlannerTaskSteps(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid, tile, cons := taskBoardSetup(t, h, false, "Ash")
	c := &execCtx{threadID: p.ID, projectID: p.ID, workspaceID: wid}
	if err := s.applyTaskEdit(c, Step{Op: "task.add", Tile: tile, Task: &TaskEdit{Title: "Write docs", Notes: "All of them", Priority: "high", Assignee: "@Ash"}}); err != nil {
		t.Fatal(err)
	}
	v := readBoardKanban(t, h, wid, tile)
	if got := columnTitles(v); len(got["To do"]) != 1 {
		t.Fatalf("%+v", got)
	}
	if k := v.Columns[1].Cards[0]; k.Assignee != "Ash" {
		t.Fatalf("%+v", k)
	}
	// by title, into a column named in any case
	if err := s.applyTaskEdit(c, Step{Op: "task.update", Tile: tile, Task: &TaskEdit{Card: "write DOCS", Column: "in progress"}}); err != nil {
		t.Fatal(err)
	}
	if got := columnTitles(readBoardKanban(t, h, wid, tile)); len(got["In progress"]) != 1 {
		t.Fatalf("%+v", got)
	}
	// handing out a card moves it instead of adding one
	o := s.sendOrder(c, []string{cons[0].ID}, "Write the docs", sourceTask, 0)
	s.linkTask(wid, tile, "Write docs", o)
	if n := len(allTasksOf(t, h, wid, tile)); n != 1 {
		t.Fatalf("%d cards", n)
	}
	linked := false
	for _, col := range readBoardKanban(t, h, wid, tile).Columns {
		for _, k := range col.Cards {
			linked = linked || (k.OrderID == o.ID && k.Assignee == "Ash")
		}
	}
	if !linked {
		t.Fatalf("not linked: %+v", readBoardKanban(t, h, wid, tile))
	}
	if err := s.applyTaskEdit(c, Step{Op: "task.delete", Tile: tile, Task: &TaskEdit{Card: "Write docs"}}); err != nil {
		t.Fatal(err)
	}
	if n := len(allTasksOf(t, h, wid, tile)); n != 0 {
		t.Fatalf("%d cards", n)
	}
	// and what it reads
	b, _ := h.Board(wid)
	in := tasksForPlanner(b, "Main")
	if len(in) != 1 || in[0]["linked"] != true || in[0]["title"] != "Sprint" {
		t.Fatalf("%+v", in)
	}
}

// Columns are found by name, then by place.
func TestTaskColumns(t *testing.T) {
	cols := func(names ...string) []map[string]any {
		var out []map[string]any
		for _, n := range names {
			out = append(out, map[string]any{"title": n})
		}
		return out
	}
	for _, c := range []struct {
		names                     []string
		todo, doing, finish, done int
	}{
		{[]string{"Backlog", "To do", "In progress", "Done"}, 1, 2, 3, 3},
		{[]string{"To do", "In progress", "Review", "Done"}, 0, 1, 2, 3},
		{[]string{"Antrian", "Dikerjakan", "Selesai"}, 0, 1, 2, 2},
		{[]string{"A", "B", "C"}, 0, 1, 2, 2},
		{[]string{"Only"}, 0, 0, 0, 0},
	} {
		td, dg, fi, dn := taskColumns(cols(c.names...))
		if td != c.todo || dg != c.doing || fi != c.finish || dn != c.done {
			t.Fatalf("%v: %d %d %d %d", c.names, td, dg, fi, dn)
		}
	}
}

// A kanban or checklist takes a wire from a lead only; the wire the planner says backwards is turned around.
func TestLeadWiresToATaskBoard(t *testing.T) {
	src := &board.Node{Kind: KindOrchestrator}
	if !isWireTarget(&board.Node{Kind: "kanban"}, src) || !isWireTarget(&board.Node{Kind: "checklist"}, src) {
		t.Fatal("a lead cannot reach a task board")
	}
	if isWireTarget(&board.Node{Kind: "kanban"}, &board.Node{Kind: KindMemory}) || isWireTarget(&board.Node{Kind: "note"}, src) {
		t.Fatal("too much is allowed")
	}
	if edgeKindFor(src, &board.Node{Kind: "kanban"}) != EdgeWire {
		t.Fatal("not a wire")
	}
}

// The whole way from what the planner answers to the board: its own words, its own names.
func TestPlannerRunsATaskBoard(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	putTile(t, h, wid, "n_lead0001", KindOrchestrator, `{"title":"Nara"}`)
	putTile(t, h, wid, "n_task0001", "kanban", `{"title":"Sprint","columns":[{"id":"c1","title":"To do","cards":[]},{"id":"c2","title":"Doing","cards":[]},{"id":"c3","title":"Done","cards":[]}]}`)

	// "connect the board to Nara": the wire starts at the lead, whichever way it was said
	plan(t, s, p, wid, "", map[string]any{"op": "connect", "source": "Sprint", "targets": []string{"Nara"}})
	plan(t, s, p, wid, "", map[string]any{"op": "connect", "source": "Nara", "targets": []string{"@Ash"}})
	b, _ := h.Board(wid)
	linked := false
	for _, e := range b.Edges {
		linked = linked || (e.Source == "n_lead0001" && e.Target == "n_task0001" && e.Kind == EdgeWire)
	}
	if !linked {
		t.Fatalf("edges %+v", b.Edges)
	}

	plan(t, s, p, wid, "", map[string]any{"op": "task.add", "tile": "Sprint", "title": "Write the tests", "text": "All of them", "priority": "high", "assignee": "@Ash"})
	plan(t, s, p, wid, "", map[string]any{"op": "task.add", "tile": "Sprint", "title": "Ship it", "column": "Doing"})
	got := columnTitles(readBoardKanban(t, h, wid, "n_task0001"))
	if strings.Join(got["To do"], ",") != "Write the tests" || strings.Join(got["Doing"], ",") != "Ship it" {
		t.Fatalf("%+v", got)
	}
	plan(t, s, p, wid, "", map[string]any{"op": "task.update", "tile": "Sprint", "card": "Ship it", "column": "Done", "done": true})
	if got = columnTitles(readBoardKanban(t, h, wid, "n_task0001")); strings.Join(got["Done"], ",") != "Ship it" {
		t.Fatalf("%+v", got)
	}
	// a tile that holds no tasks is refused, and nothing happens to it
	putTile(t, h, wid, "n_note0001", "note", `{"text":"hello"}`)
	plan(t, s, p, wid, "", map[string]any{"op": "task.add", "tile": "n_note0001", "title": "Nope"})
	if n := len(allTasksOf(t, h, wid, "n_task0001")); n != 2 {
		t.Fatalf("%d cards", n)
	}

	// handing out a card: that card moves, no new one appears, also after the owner agrees
	sc, err := s.newScope(wid, "")
	if err != nil {
		t.Fatal(err)
	}
	steps, notes := s.translatePlan(sc, planOutput{Steps: []plannerStep{{Op: "send", Targets: []string{"@Ash"}, Text: "Write the tests now", Tile: "Sprint", Card: "Write the tests"}}})
	if len(steps) != 1 || steps[0].Task == nil || steps[0].Task.Card != "Write the tests" || steps[0].Tile != "n_task0001" || len(notes) != 0 {
		t.Fatalf("%+v %v", steps, notes)
	}
	raw, _ := json.Marshal(steps[0])
	var again Step
	if err := json.Unmarshal(raw, &again); err != nil || again.Task == nil {
		t.Fatalf("a step waiting for agreement loses its card: %s %v", raw, err)
	}
	if err := s.runStep(&execCtx{threadID: p.ID, projectID: p.ID, workspaceID: wid}, again); err != nil {
		t.Fatal(err)
	}
	if n := len(allTasksOf(t, h, wid, "n_task0001")); n != 2 {
		t.Fatalf("a new card appeared: %d", n)
	}
	found := false
	for _, col := range readBoardKanban(t, h, wid, "n_task0001").Columns {
		for _, k := range col.Cards {
			found = found || (k.Title == "Write the tests" && k.SessionID == ash.ID && k.OrderID != "")
		}
	}
	if !found {
		t.Fatalf("card not linked: %+v", readBoardKanban(t, h, wid, "n_task0001"))
	}
	if ds := h.deliveriesTo(ash.ID); len(ds) != 1 || ds[0].Text != "Write the tests now" {
		t.Fatalf("%+v", ds)
	}

	// the planner reads what is there
	b, _ = h.Board(wid)
	in := tasksForPlanner(b, "Main")
	if len(in) != 1 || in[0]["linked"] != true {
		t.Fatalf("%+v", in)
	}
}
