package orch

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// An orchestrator card wired to an editor card directs the agents opened inside that editor
// (docs/API-studio.md §7.8): orchestrator → editor, next to editor → orchestrator (the Ask).

const (
	edCard   = "n_editor0001"
	leadCard = "n_orch000001"
)

// panelSession adds a console of the Code workbench: no tile, opened in card (or none), placed
// at the root or in a part.
func (h *fakeHub) panelSession(p project.Project, wid, handle, agentID, card string, root bool, part string) session.Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	kind := agent.KindAgent
	if agentID == "shell" {
		kind = agent.KindShell
	}
	in := &session.Info{Spec: session.Spec{ID: auth.RandomID("s_"), Handle: handle, Agent: agentID, AgentKind: kind, MachineID: "local", Panel: true,
		Card: card, Root: root, PartID: part, ProjectID: p.ID, WorkspaceID: wid, CreatedAt: t0}, Status: session.StatusRunning}
	in.AgentState, in.Foreground = agentstate.Ready, agentstate.FgAgent
	if kind == agent.KindShell {
		in.AgentState = agentstate.Shell
	}
	h.sessions[in.ID] = in
	h.order = append(h.order, in.ID)
	return *in
}

func TestCardDirectsTheAgentsInsideAWiredEditor(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	putTile(t, h, wid, leadCard, KindOrchestrator, `{"title":"Lead"}`)
	putTile(t, h, wid, edCard, KindEditor, `{}`)
	ash := h.panelSession(p, wid, "Ash", "claude", edCard, false, "")
	tom := h.panelSession(p, wid, "Tom", "shell", edCard, false, "") // the user's own terminal in the editor
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)      // on the board, not wired

	// before the wire the card does not reach the editor's agent
	if r, _ := s.Command(CommandRequest{Text: "@Ash run the tests", WorkspaceID: wid, CardID: leadCard, Planner: "off"}); len(h.deliveriesTo(ash.ID)) != 0 {
		t.Fatalf("directed before the wire: %s", texts(r))
	}
	// both directions between the two cards, as two edges
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000001", leadCard, edCard), wireOp("e_wire000002", edCard, leadCard)}); err != nil {
		t.Fatal(err)
	}
	sc, err := s.newScope(wid, leadCard)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.directed) != 1 || !sc.directed[ash.ID] {
		t.Fatalf("directed %v (ash %s, tom %s, bea %s)", sc.directed, ash.ID, tom.ID, bea.ID)
	}
	if got := DirectedSessions(p, mustBoard(t, h, wid), h.Sessions(), leadCard); len(got) != 1 || !got[ash.ID] {
		t.Fatalf("DirectedSessions %v", got)
	}

	if r, err := s.Command(CommandRequest{Text: "@Ash run the tests", WorkspaceID: wid, CardID: leadCard, Planner: "off"}); err != nil || len(h.deliveriesTo(ash.ID)) != 1 {
		t.Fatalf("order to the editor's agent: %v %s", err, texts(r))
	}
	if r, _ := s.Command(CommandRequest{Text: "@Bea run the tests", WorkspaceID: wid, CardID: leadCard, Planner: "off"}); len(h.deliveriesTo(bea.ID)) != 0 {
		t.Fatalf("a console that is not wired got it: %s", texts(r))
	}
	if r, _ := s.Command(CommandRequest{Text: "@Tom run the tests", WorkspaceID: wid, CardID: leadCard, Planner: "off"}); len(h.deliveriesTo(tom.ID)) != 0 {
		t.Fatalf("the editor's plain terminal got an order: %s", texts(r))
	}
	if r, _ := s.Command(CommandRequest{Text: "@everyone check the build", WorkspaceID: wid, CardID: leadCard, Planner: "off"}); len(h.deliveriesTo(ash.ID)) != 2 || len(h.deliveriesTo(bea.ID)) != 0 {
		t.Fatalf("everyone of the card: %s", texts(r))
	}

	// a loop of the card reaches it too
	if got := s.loopTargets(LoopDef{WorkspaceID: wid, Targets: LoopTargets{Kind: "orchestrator", IDs: []string{leadCard}}}); len(got) != 1 || got[0].ID != ash.ID {
		t.Fatalf("loop targets %+v", got)
	}

	// and the lead's task board counts it in the team
	putTile(t, h, wid, "n_kanban0001", "kanban", `{"title":"Tasks","columns":[]}`)
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000003", leadCard, "n_kanban0001")}); err != nil {
		t.Fatal(err)
	}
	b := mustBoard(t, h, wid)
	if tiles := taskTilesOf(b, s.editorAgentsOf(wid, b)); len(tiles) != 1 || !tiles[0].team[ash.ID] || tiles[0].team[tom.ID] {
		t.Fatalf("task team %+v", tiles)
	}

	// the card goes, and its wires with it; the agent keeps running
	if err := h.ApplyOps(wid, []board.Op{{Op: "del", ID: edCard}}); err != nil {
		t.Fatal(err)
	}
	if b := mustBoard(t, h, wid); len(b.Edges) != 1 || b.Edges[0].Target != "n_kanban0001" {
		t.Fatalf("edges left %+v", b.Edges)
	}
	if _, ok := h.SessionInfo(ash.ID); !ok {
		t.Fatal("the agent went with the card")
	}
}

// A panel agent opened before cards were recorded, or whose card is gone, belongs to the card of
// its placement, else to a Root card.
func TestEditorAgentsByPlacement(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid, web, api := shopParts(t, h)
	root, webCard := "n_edroot0001", "n_edweb00001"
	placeTile(t, h, wid, root, KindEditor, map[string]any{})
	placeTile(t, h, wid, webCard, KindEditor, map[string]any{"part": web.ID})
	oldWeb := h.panelSession(p, wid, "Ada", "claude", "", false, web.ID)
	oldRoot := h.panelSession(p, wid, "Bo", "claude", "", true, "")
	oldAPI := h.panelSession(p, wid, "Cy", "claude", "", false, api.ID) // no card of Api: the Root card shows it
	goneWeb := h.panelSession(p, wid, "Di", "claude", "n_gone000001", false, web.ID)
	inRoot := h.panelSession(p, wid, "Ed", "claude", root, false, web.ID) // opened in the Root card, working in Web
	h.panelSession(p, wid, "Fay", "shell", "", false, web.ID)
	second, err := h.CreateWorkspace(p.ID, "Second")
	if err != nil {
		t.Fatal(err)
	}
	other := h.panelSession(p, second.ID, "Gil", "claude", "", false, web.ID) // another board of the project

	p, _ = h.projects.Get(p.ID)
	sc, err := s.newScope(wid, "")
	if err != nil {
		t.Fatal(err)
	}
	got := func(card string) []string {
		l := sc.nodeSessions(card)
		slices.Sort(l)
		return l
	}
	want := func(ins ...session.Info) []string {
		var l []string
		for _, in := range ins {
			l = append(l, in.ID)
		}
		slices.Sort(l)
		return l
	}
	if g, w := got(webCard), want(oldWeb, goneWeb); !slices.Equal(g, w) {
		t.Fatalf("web card %v, want %v", g, w)
	}
	if g, w := got(root), want(oldRoot, oldAPI, inRoot); !slices.Equal(g, w) {
		t.Fatalf("root card %v, want %v", g, w)
	}
	for _, sid := range append(got(root), got(webCard)...) {
		if sid == other.ID {
			t.Fatal("an agent of another board")
		}
	}

	// one part: every card holds the editor's agents
	h2, s2 := newFakeHub(t)
	p2, wid2 := h2.project("solo")
	placeTile(t, h2, wid2, edCard, KindEditor, map[string]any{})
	a := h2.panelSession(p2, wid2, "Ada", "claude", "", false, p2.Parts[0].ID)
	sc2, _ := s2.newScope(wid2, "")
	if l := sc2.nodeSessions(edCard); len(l) != 1 || l[0] != a.ID {
		t.Fatalf("single part %v", l)
	}
}

func TestWireOrchestratorToEditorValidation(t *testing.T) {
	h, _ := newFakeHub(t)
	_, wid := h.project("app")
	putTile(t, h, wid, leadCard, KindOrchestrator, `{"title":"Lead"}`)
	putTile(t, h, wid, edCard, KindEditor, `{}`)
	putTile(t, h, wid, "n_kanban0001", "kanban", `{"title":"Tasks"}`)
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000001", leadCard, edCard)}); err != nil {
		t.Fatalf("orchestrator → editor: %v", err)
	}
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000002", edCard, leadCard)}); err != nil {
		t.Fatalf("editor → orchestrator next to it: %v", err)
	}
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000003", leadCard, edCard)}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("a second orchestrator → editor: %v", err)
	}
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000004", "n_kanban0001", edCard)}); err == nil {
		t.Fatal("kanban → editor was accepted")
	}
	if got := edgeKindFor(&board.Node{Kind: KindOrchestrator}, &board.Node{Kind: KindEditor}); got != EdgeWire {
		t.Fatalf("edgeKindFor orchestrator → editor %q", got)
	}
}

// "connect the orchestrator to the editor" and "connect the editor to the orchestrator" are two
// links; a disconnect takes the one said first.
func TestPlannerConnectsOrchestratorAndEditorBothWays(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	putTile(t, h, wid, edCard, KindEditor, `{}`)
	putTile(t, h, wid, leadCard, KindOrchestrator, `{"title":"Lead"}`)
	notes := plan(t, s, p, wid, "", map[string]any{"op": "connect", "source": "Lead", "targets": []string{"editor"}})
	b := mustBoard(t, h, wid)
	if len(b.Edges) != 1 || b.Edges[0].Source != leadCard || b.Edges[0].Target != edCard || b.Edges[0].Kind != EdgeWire {
		t.Fatalf("orchestrator → editor: %+v", b.Edges)
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "No agent is open in that editor yet") }) {
		t.Fatalf("notes %q", notes)
	}
	plan(t, s, p, wid, "", map[string]any{"op": "connect", "source": "editor", "targets": []string{"Lead"}})
	if b = mustBoard(t, h, wid); len(b.Edges) != 2 || !hasEdge(b, EdgeWire, edCard, leadCard) || !hasEdge(b, EdgeWire, leadCard, edCard) {
		t.Fatalf("both ways: %+v", b.Edges)
	}
	plan(t, s, p, wid, "", map[string]any{"op": "disconnect", "source": "Lead", "targets": []string{"editor"}})
	if b = mustBoard(t, h, wid); len(b.Edges) != 1 || !hasEdge(b, EdgeWire, edCard, leadCard) {
		t.Fatalf("after the disconnect: %+v", b.Edges)
	}
	// with only the other direction left, the disconnect takes that one
	plan(t, s, p, wid, "", map[string]any{"op": "disconnect", "source": "Lead", "targets": []string{"editor"}})
	if b = mustBoard(t, h, wid); len(b.Edges) != 0 {
		t.Fatalf("still connected: %+v", b.Edges)
	}
}

// A memory tile wired to an orchestrator reaches what it directs, the agents inside a wired
// editor too; a memory tile wired to the editor itself reaches what the editor asks.
func TestMemoryReachesTheAgentsOfADirectedEditor(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	asked := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	inside := h.panelSession(p, wid, "Ash", "claude", edCard, false, "")
	if err := h.ApplyOps(wid, []board.Op{
		memoryTileOp(t, map[string]any{"blocks": []MemoryBlock{{Name: "design"}}}),
		{Op: "put", Node: &board.Node{ID: "n_mem0000002", Kind: KindMemory, W: 300, H: 200, Data: json.RawMessage(`{}`)}},
		{Op: "put", Node: &board.Node{ID: leadCard, Kind: KindOrchestrator, W: 300, H: 200, Data: json.RawMessage(`{"title":"Lead"}`)}},
		{Op: "put", Node: &board.Node{ID: edCard, Kind: KindEditor, W: 300, H: 200, Data: json.RawMessage(`{}`)}},
		wireOp("e_wire000001", memTile, leadCard),
		wireOp("e_wire000002", leadCard, edCard),
		wireOp("e_wire000003", "n_mem0000002", edCard),
		wireOp("e_wire000004", edCard, h.node(wid, asked.ID).ID),
	}); err != nil {
		t.Fatal(err)
	}
	b := mustBoard(t, h, wid)
	agents := s.editorAgentsOf(wid, b)
	if _, got := memoryReach(b, wireTargets(b, memTile), agents); !slices.Equal(got, []string{inside.ID}) {
		t.Fatalf("through the orchestrator %v, want %s", got, inside.ID)
	}
	if _, got := memoryReach(b, wireTargets(b, "n_mem0000002"), agents); !slices.Equal(got, []string{asked.ID}) {
		t.Fatalf("wired to the editor %v, want %s", got, asked.ID)
	}
	// what a console's memory gets from the tiles connected to it
	if got := s.attachedBlocks(inside.ID, wid); len(got) != 1 || got[0].Name != "design" {
		t.Fatalf("what the tiles add to the editor's agent: %+v", got)
	}
}

// A login profile's sign-in console is a panel agent with no card: it is that account's own CLI,
// so neither a wired editor's card nor the command bar ever types into it, by @everyone or by
// its handle.
func TestSignInConsoleTakesNoOrders(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	putTile(t, h, wid, leadCard, KindOrchestrator, `{"title":"Lead"}`)
	putTile(t, h, wid, edCard, KindEditor, `{}`)
	ash := h.panelSession(p, wid, "Ash", "claude", edCard, false, "")
	sign := h.panelSession(p, wid, "Signer", "claude", "", false, "")
	h.mu.Lock()
	h.sessions[sign.ID].SignIn = true
	h.mu.Unlock()
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000001", leadCard, edCard)}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []CommandRequest{
		{Text: "@everyone run the tests", WorkspaceID: wid, CardID: leadCard, Planner: "off"},
		{Text: "@everyone run the tests", WorkspaceID: wid, Planner: "off"},
		{Text: "@Signer run the tests", WorkspaceID: wid, Planner: "off"},
	} {
		if _, err := s.Command(req); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(h.deliveriesTo(sign.ID)); n != 0 {
		t.Fatalf("the sign-in console got %d orders", n)
	}
	if len(h.deliveriesTo(ash.ID)) == 0 {
		t.Fatal("the editor's agent got no order")
	}
}
