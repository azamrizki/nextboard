package orch

import (
	"encoding/json"
	"strings"
	"testing"

	"nextboard/internal/agentstate"
	"nextboard/internal/board"
	"nextboard/internal/project"
)

// putTile places a tile through the same ops a client uses.
func putTile(t *testing.T, h *fakeHub, wid, id, kind, data string) {
	t.Helper()
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: &board.Node{ID: id, Kind: kind, X: 10, Y: 20, W: 300, H: 200, Data: json.RawMessage(data)}}}); err != nil {
		t.Fatal(err)
	}
}

// plan runs an order through the planner with this plan as its answer.
func plan(t *testing.T, s *Service, p project.Project, wid string, reply string, steps ...map[string]any) []string {
	t.Helper()
	s.Command(CommandRequest{Text: "do this on the board for me please", WorkspaceID: wid})
	list := make([]any, len(steps))
	for i, x := range steps {
		list[i] = x
	}
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": reply, "steps": list}))
	return threadTexts(s, p.ID, "note")
}

func nodeByID(t *testing.T, h *fakeHub, wid, id string) *board.Node {
	t.Helper()
	b, err := h.Board(wid)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range b.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

func dataOf(t *testing.T, n *board.Node) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(n.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPlannerConnectsConsoleWithEditorWhicheverWaySaid(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	putTile(t, h, wid, "n_editor0001", KindEditor, `{}`)
	plan(t, s, p, wid, "", map[string]any{"op": "connect", "source": "@Bea", "targets": []string{"editor"}})
	b, _ := h.Board(wid)
	var termID string
	for _, n := range b.Nodes {
		if n.SessionID() == bea.ID {
			termID = n.ID
		}
	}
	if len(b.Edges) != 1 || b.Edges[0].Source != "n_editor0001" || b.Edges[0].Target != termID || b.Edges[0].Kind != EdgeWire {
		t.Fatalf("edges %+v", b.Edges)
	}
	plan(t, s, p, wid, "", map[string]any{"op": "disconnect", "source": "editor", "targets": []string{"Bea"}})
	if b, _ = h.Board(wid); len(b.Edges) != 0 {
		t.Fatalf("still connected: %+v", b.Edges)
	}
	// a stray copy of the card does not make "editor" ambiguous
	putTile(t, h, wid, "n_editor0002", KindEditor, `{}`)
	plan(t, s, p, wid, "", map[string]any{"op": "connect", "source": "editor", "targets": []string{"Bea"}})
	if b, _ = h.Board(wid); len(b.Edges) != 1 || b.Edges[0].Source != "n_editor0001" {
		t.Fatalf("edges with a copy of the card: %+v", b.Edges)
	}
}

func TestPlannerConnectsEditorToOrchestratorAndConsolesForward(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.console(p, wid, "Bea", "claude", agentstate.Ready)
	putTile(t, h, wid, "n_editor0001", KindEditor, `{}`)
	putTile(t, h, wid, "n_orch000001", KindOrchestrator, `{"title":"Lead"}`)
	putTile(t, h, wid, "n_note000001", "note", `{"text":"x"}`)
	plan(t, s, p, wid, "",
		map[string]any{"op": "connect", "source": "editor", "targets": []string{"Lead"}},
		map[string]any{"op": "connect", "source": "@Ash", "targets": []string{"@Bea"}},
		map[string]any{"op": "connect", "source": "Lead", "targets": []string{"n_note000001"}})
	b, _ := h.Board(wid)
	kinds := map[string]int{}
	for _, e := range b.Edges {
		kinds[e.Kind]++
	}
	if kinds[EdgeWire] != 1 || kinds[EdgeForward] != 1 {
		t.Fatalf("edges %+v", b.Edges)
	}
	if notes := strings.Join(threadTexts(s, p.ID, "note"), "|"); !strings.Contains(notes, "cannot be connected") {
		t.Fatalf("a wire to a note should be refused: %s", notes)
	}
}

func TestPlannerEditsAnyTile(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	con := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	putTile(t, h, wid, "n_note000001", "note", `{"text":"old","color":"yellow"}`)
	putTile(t, h, wid, "n_list000001", "checklist", `{"title":"Todo","items":[{"id":"i_1","text":"a","done":false}]}`)
	plan(t, s, p, wid, "",
		map[string]any{"op": "node.update", "tile": "n_note000001", "text": "new", "color": "blue", "x": 500.0, "w": 400.0},
		map[string]any{"op": "node.update", "tile": "Todo", "data": map[string]any{"items": []any{map[string]any{"id": "i_1", "text": "a", "done": true}, map[string]any{"text": "b"}}, "sessionId": "s_evil"}},
		map[string]any{"op": "node.update", "tile": "@Ash", "x": 900.0},
		map[string]any{"op": "node.update", "tile": "@Ash", "text": "nope"},
		map[string]any{"op": "node.update", "tile": "no such tile", "x": 1.0})
	n := nodeByID(t, h, wid, "n_note000001")
	d := dataOf(t, n)
	if d["text"] != "new" || d["color"] != "blue" || n.X != 500 || n.W != 400 || n.Y != 20 {
		t.Fatalf("note %+v %v", n, d)
	}
	l := dataOf(t, nodeByID(t, h, wid, "n_list000001"))
	items := l["items"].([]any)
	if _, evil := l["sessionId"]; evil || len(items) != 2 || items[0].(map[string]any)["done"] != true || items[1].(map[string]any)["id"] == "" {
		t.Fatalf("checklist %v", l)
	}
	b, _ := h.Board(wid)
	for _, x := range b.Nodes {
		if x.SessionID() == con.ID && (x.X != 900 || x.SessionID() != con.ID) {
			t.Fatalf("console tile %+v", x)
		}
	}
	notes := strings.Join(threadTexts(s, p.ID, "note"), "|")
	if !strings.Contains(notes, "console's content cannot be edited") || !strings.Contains(notes, "not on any board") {
		t.Fatalf("notes %s", notes)
	}
}

func TestPlannerAddsTilesWithContent(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	plan(t, s, p, wid, "",
		map[string]any{"op": "node.add", "kind": "checklist", "title": "Release", "x": 40.0, "y": 60.0, "data": map[string]any{"items": []any{map[string]any{"text": "tag"}}}},
		map[string]any{"op": "node.add", "kind": "file", "path": "docs/a.md"},
		map[string]any{"op": "node.add", "kind": "editor"},
		map[string]any{"op": "node.add", "kind": "editor"})
	b, _ := h.Board(wid)
	byKind := map[string]*board.Node{}
	editors := 0
	for _, n := range b.Nodes {
		byKind[n.Kind] = n
		if n.Kind == KindEditor {
			editors++
		}
	}
	c := byKind["checklist"]
	if c == nil || c.X != 40 || c.Y != 60 {
		t.Fatalf("checklist %+v", c)
	}
	cd := dataOf(t, c)
	if cd["title"] != "Release" || cd["items"].([]any)[0].(map[string]any)["id"] == "" {
		t.Fatalf("checklist data %v", cd)
	}
	fd := dataOf(t, byKind["file"])
	if fd["view"] != "markdown" || !strings.HasSuffix(fd["path"].(string), "/docs/a.md") || !strings.HasPrefix(fd["path"].(string), "/") {
		t.Fatalf("file data %v", fd)
	}
	if editors != 1 {
		t.Fatalf("%d editor cards", editors)
	}
}

func TestPlannerDeleteAsksFirstAndKeepsConsoles(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	h.console(p, wid, "Ash", "claude", agentstate.Ready)
	putTile(t, h, wid, "n_note000001", "note", `{"text":"bye"}`)
	plan(t, s, p, wid, "", map[string]any{"op": "node.delete", "tiles": []string{"@Ash"}})
	if notes := strings.Join(threadTexts(s, p.ID, "note"), "|"); !strings.Contains(notes, "kill") {
		t.Fatalf("notes %s", notes)
	}
	plan(t, s, p, wid, "", map[string]any{"op": "node.delete", "tile": "n_note000001"})
	if nodeByID(t, h, wid, "n_note000001") == nil {
		t.Fatal("deleted without asking")
	}
	conf, ok := s.Confirms.Pending(p.ID, s.Now())
	if !ok || !strings.Contains(conf.Prompt, "bye") {
		t.Fatalf("confirmation %+v %v", conf, ok)
	}
	if _, err := s.Decide(conf.ID, "confirm", "", "admin"); err != nil {
		t.Fatal(err)
	}
	if nodeByID(t, h, wid, "n_note000001") != nil {
		t.Fatal("not deleted after confirming")
	}
}

func TestPlannerInputShowsTileIDsContentAndEdges(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	h.console(p, wid, "Ash", "claude", agentstate.Ready)
	putTile(t, h, wid, "n_note000001", "note", `{"text":"remember me"}`)
	putTile(t, h, wid, "n_ink0000001", "ink", `{"points":[1,2,3]}`)
	big := strings.Repeat("x", 2000)
	putTile(t, h, wid, "n_note000002", "note", `{"text":"`+big+`"}`)
	sc, err := s.newScope(wid, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(sc.boardsSnapshot())
	got := string(b)
	for _, want := range []string{`"id":"n_note000001"`, `remember me`, `"edges"`, `"contentCut"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %s in %s", want, got[:min(len(got), 400)])
		}
	}
	if strings.Contains(got, `"points"`) {
		t.Fatal("ink points in the input")
	}
	if strings.Contains(got, big) {
		t.Fatal("a huge tile went in whole")
	}
}

func TestPlannerUISteps(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	putTile(t, h, wid, "n_note000001", "note", `{"text":"hello there"}`)
	sc, err := s.newScope(wid, "")
	if err != nil {
		t.Fatal(err)
	}
	steps, notes := s.translatePlan(sc, planOutput{Steps: []plannerStep{
		{Op: "ui", Action: "fit"},
		{Op: "ui", Action: "focus", Tile: "hello there"},
		{Op: "ui", Action: "openPanel", Panel: "git"},
		{Op: "ui", Action: "commitPush"},
		{Op: "editor.open", Path: "src/a.go", Line: 12},
		{Op: "workspace.rename", Name: "Renamed"},
	}})
	if len(notes) != 1 || !strings.Contains(notes[0], "dropped") {
		t.Fatalf("notes %v", notes)
	}
	if len(steps) != 5 || steps[1].UIAction.Target != "n_note000001" {
		t.Fatalf("steps %+v", steps)
	}
	if a := steps[3].UIAction; a.Action != "openFile" || a.Line != 12 || !strings.HasSuffix(a.Path, "/src/a.go") {
		t.Fatalf("open %+v", a)
	}
	if steps[4].WorkspaceID != wid || steps[4].Name != "Renamed" {
		t.Fatalf("rename %+v", steps[4])
	}
	_ = p
}

func TestPlannerRenamesConsoleAndDissolvesGroup(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	plan(t, s, p, wid, "", map[string]any{"op": "console.rename", "targets": []string{"@Ash"}, "name": "Zed"})
	if in, _ := h.SessionInfo(ash.ID); in.Handle != "Zed" {
		t.Fatalf("session %+v", in)
	}
}
