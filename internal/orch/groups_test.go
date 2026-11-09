package orch

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/session"
)

func groupNode(id, name, role string, members ...string) *board.Node {
	raw, _ := json.Marshal(GroupData{Name: name, Role: role, Members: members})
	return &board.Node{ID: id, Kind: KindGroup, W: 100, H: 100, Data: raw}
}

func codeOf(err error) (string, int) {
	var ve *board.ValidationError
	if errors.As(err, &ve) {
		return ve.Code, ve.Status
	}
	return "", 0
}

// TestGroupWireForwardValidation covers §1 and §19.10.
func TestGroupWireForwardValidation(t *testing.T) {
	h, _ := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	na, nb := h.node(wid, ash.ID).ID, h.node(wid, bea.ID).ID
	apply := func(ops ...board.Op) error { return h.ApplyOps(wid, ops) }
	put := func(n *board.Node) board.Op { return board.Op{Op: "put", Node: n} }
	edge := func(id, kind, src, dst string, data string) board.Op {
		return board.Op{Op: "putEdge", Edge: &board.Edge{ID: id, Kind: kind, Source: src, Target: dst, Data: json.RawMessage(data)}}
	}
	if err := apply(put(groupNode("n_grp0000001", "Backend", "backend", na))); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		ops    []board.Op
		code   string
		status int
	}{
		{"name = handle", []board.Op{put(groupNode("n_grp0000002", "bea", "general", nb))}, "name_conflict", http.StatusConflict},
		{"duplicate name", []board.Op{put(groupNode("n_grp0000002", "BACKEND", "general", nb))}, "name_conflict", http.StatusConflict},
		{"member twice", []board.Op{put(groupNode("n_grp0000002", "Front", "frontend", na))}, "member_conflict", 0},
		{"member not terminal", []board.Op{put(groupNode("n_grp0000002", "Front", "frontend", "n_grp0000001"))}, "member_conflict", 0},
		{"bad role", []board.Op{put(groupNode("n_grp0000002", "Front", "pilots", nb))}, "bad_group", 0},
		{"too long", []board.Op{put(groupNode("n_grp0000002", strings.Repeat("x", 25), "general", nb))}, "bad_group", 0},
		{"wire from terminal", []board.Op{edge("e_wire000001", EdgeWire, na, nb, `{}`)}, "bad_edge", 0},
		{"forward bad mode", []board.Op{edge("e_fwd0000001", EdgeForward, na, nb, `{"mode":"copy"}`)}, "bad_edge", 0},
		{"forward to own group", []board.Op{edge("e_fwd0000001", EdgeForward, na, "n_grp0000001", `{}`)}, "forward_cycle", 0},
	}
	for _, c := range cases {
		code, st := codeOf(apply(c.ops...))
		if code != c.code || st != c.status {
			t.Errorf("%s: got %s/%d, want %s/%d", c.name, code, st, c.code, c.status)
		}
	}
	tooMany := []string{}
	for i := 0; i < 13; i++ {
		tooMany = append(tooMany, nb)
	}
	if code, _ := codeOf(apply(put(groupNode("n_grp0000003", "Big", "general", tooMany...)))); code != "too_many" {
		t.Fatalf("13 members: %s", code)
	}
	// Valid orchestrator + wire, then a duplicate wire.
	raw, _ := json.Marshal(OrchestratorData{Title: "Orchestrator"})
	if err := apply(put(&board.Node{ID: "n_orch000001", Kind: KindOrchestrator, W: 400, H: 500, Data: raw}),
		edge("e_wire000001", EdgeWire, "n_orch000001", "n_grp0000001", `{}`)); err != nil {
		t.Fatal(err)
	}
	if code, _ := codeOf(apply(edge("e_wire000002", EdgeWire, "n_orch000001", "n_grp0000001", `{}`))); code != "bad_edge" {
		t.Fatalf("duplicate wire %s", code)
	}
	long, _ := json.Marshal(OrchestratorData{Title: strings.Repeat("t", 41)})
	if code, _ := codeOf(apply(put(&board.Node{ID: "n_orch000002", Kind: KindOrchestrator, W: 1, H: 1, Data: long}))); code != "bad_orchestrator" {
		t.Fatalf("title %s", code)
	}
	// Forward edges: A → B fine, B → A is a cycle.
	cn := h.node(wid, h.console(p, wid, "Cass", "claude", agentstate.Ready).ID).ID
	if err := apply(edge("e_fwd0000001", EdgeForward, nb, cn, `{"mode":"digest","trigger":"turn"}`)); err != nil {
		t.Fatal(err)
	}
	if code, _ := codeOf(apply(edge("e_fwd0000002", EdgeForward, cn, na, `{}`), edge("e_fwd0000003", EdgeForward, na, nb, `{}`))); code != "forward_cycle" {
		t.Fatalf("cycle %s", code)
	}
	// Deleting a terminal removes it from its group's members.
	if err := h.boards.RemoveTerminal(wid, ash.ID); err != nil {
		t.Fatal(err)
	}
	b, _ := h.Board(wid)
	for _, n := range b.Nodes {
		if n.ID == "n_grp0000001" && len(GroupOf(n).Members) != 0 {
			t.Fatalf("member kept: %s", n.Data)
		}
	}
}

// TestSpawnOneBroadcast: §19.11 — 4 consoles with a new group and an
// orchestrator arrive in one boardOps batch.
func TestSpawnOneBroadcast(t *testing.T) {
	h, s := newFakeHub(t)
	_, wid := h.project("app")
	raw, _ := json.Marshal(OrchestratorData{Title: "Orchestrator"})
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: &board.Node{ID: "n_orch000001", Kind: KindOrchestrator, W: 400, H: 500, Data: raw}}}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var batches [][]board.Op
	h.boards.OnOps = func(_ string, _ int64, _ string, ops []board.Op) {
		mu.Lock()
		batches = append(batches, ops)
		mu.Unlock()
	}
	h.set.Agents.RoleAgents["reviewers"] = "codex"
	h.onMach["local/codex"] = true
	res, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 4, Group: &SpawnGroup{Role: "reviewers"}, OrchestratorID: "n_orch000001"})
	if err != nil || len(res.Sessions) != 4 || res.GroupNodeID == "" {
		t.Fatalf("%v %+v", err, res)
	}
	if len(batches) != 1 {
		t.Fatalf("%d board batches", len(batches))
	}
	kinds := map[string]int{}
	for _, op := range batches[0] {
		switch {
		case op.Node != nil:
			kinds[op.Node.Kind]++
		case op.Edge != nil:
			kinds["edge:"+op.Edge.Kind]++
		}
	}
	if kinds["terminal"] != 4 || kinds["group"] != 1 || kinds["edge:wire"] != 1 {
		t.Fatalf("batch %v", kinds)
	}
	for _, in := range res.Sessions {
		if in.Agent != "codex" || in.Cwd != "/work/app" {
			t.Fatalf("role agent / cwd: %+v", in.Spec)
		}
	}
	b, _ := h.Board(wid)
	var g *board.Node
	for _, n := range b.Nodes {
		if n.ID == res.GroupNodeID {
			g = n
		}
	}
	gd := GroupOf(g)
	if gd.Name != "Group 1" || gd.Role != "reviewers" || gd.Color != "rose" || len(gd.Members) != 4 {
		t.Fatalf("group %+v", gd)
	}
	// The frame wraps the members: bbox + padding 24 + header 32.
	var minX, minY = 1e9, 1e9
	for _, n := range b.Nodes {
		if n.Kind == board.KindTerminal {
			minX, minY = min(minX, n.X), min(minY, n.Y)
		}
	}
	if g.X != minX-24 || g.Y != minY-56 {
		t.Fatalf("frame %v,%v vs %v,%v", g.X, g.Y, minX, minY)
	}
	// group.add appends and recomputes the frame (one more batch).
	res2, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 1, Group: &SpawnGroup{ID: res.GroupNodeID}})
	if err != nil || res2.GroupNodeID != res.GroupNodeID {
		t.Fatal(err)
	}
	b, _ = h.Board(wid)
	for _, n := range b.Nodes {
		if n.ID == res.GroupNodeID && len(GroupOf(n).Members) != 5 {
			t.Fatalf("members %v", GroupOf(n).Members)
		}
	}
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 13}); !errors.Is(err, ErrTooMany) {
		t.Fatal(err)
	}
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 1, Group: &SpawnGroup{ID: "n_nothing0001"}}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatal(err)
	}
	// A name conflicting with a handle rolls the created consoles back.
	n := len(h.Sessions())
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 2, Group: &SpawnGroup{Name: "N1"}}); err == nil {
		t.Fatal("name conflict accepted")
	}
	if len(h.Sessions()) != n {
		t.Fatal("no rollback")
	}
}

// TestMissionMap computes directors, groups, consoles and counts.
func TestMissionMap(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Working)
	bea := h.console(p, wid, "Bea", "claude", agentstate.NeedsYou)
	cas := h.console(p, wid, "Cass", "codex", agentstate.Ready)
	h.mu.Lock()
	h.sessions[cas.ID].Paused = true
	h.sessions[ash.ID].Subagents = 2
	h.mu.Unlock()
	h.console(p, wid, "Dov", "shell", "")
	raw, _ := json.Marshal(OrchestratorData{Title: "Planning"})
	ops := []board.Op{
		{Op: "put", Node: groupNode("n_grp0000001", "Backend", "backend", h.node(wid, ash.ID).ID, h.node(wid, bea.ID).ID)},
		{Op: "put", Node: &board.Node{ID: "n_orch000001", Kind: KindOrchestrator, W: 400, H: 500, Data: raw}},
		{Op: "putEdge", Edge: &board.Edge{ID: "e_wire000001", Kind: EdgeWire, Source: "n_orch000001", Target: "n_grp0000001"}},
	}
	if err := h.ApplyOps(wid, ops); err != nil {
		t.Fatal(err)
	}
	r := cmd(t, s, wid, "@Ash add tests")
	m, err := s.Mission(wid, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Directors) != 2 || m.Directors[0].ID != "bar" || m.Directors[0].LastOrder == nil || m.Directors[0].LastOrder.Text != "@Ash add tests" ||
		m.Directors[1].Title != "Planning" || m.Directors[1].Targets[0] != "n_grp0000001" {
		t.Fatalf("directors %+v", m.Directors)
	}
	if len(m.Groups) != 1 || len(m.Groups[0].Members) != 2 || m.Groups[0].Color != "teal" || m.Groups[0].LastTask != "add tests" {
		t.Fatalf("groups %+v", m.Groups)
	}
	byH := map[string]MissionConsole{}
	for _, c := range m.Consoles {
		byH[c.Handle] = c
	}
	if a := byH["Ash"]; a.Role != "backend" || a.Subagents != 2 || a.CurrentOrder == nil || a.CurrentOrder.OrderID != r.OrderID ||
		len(a.DirectedBy) != 2 || a.DirectedBy[1] != "n_orch000001" {
		t.Fatalf("ash %+v", a)
	}
	if c := byH["Cass"]; !c.Paused || len(c.DirectedBy) != 1 || c.DirectedBy[0] != "bar" {
		t.Fatalf("cass %+v", c)
	}
	if m.Counts != (MissionCounts{Working: 1, NeedsYou: 1, Idle: 2, Done: 0}) {
		t.Fatalf("counts %+v", m.Counts)
	}
	// Project scope includes other workspaces.
	w2, _, _ := h.projects.AddWorkspace(p.ID, "Two")
	h.console(p, w2.ID, "Eli", "claude", agentstate.Exited)
	m, _ = s.Mission(wid, true)
	if len(m.Consoles) != 5 || m.Counts.Done != 1 {
		t.Fatalf("project scope %d %+v", len(m.Consoles), m.Counts)
	}
}

// TestBuiltinLayoutsLandCleanly: every built-in deploys whole on an empty board,
// and nothing on it covers anything else, group frames included.
func TestBuiltinLayoutsLandCleanly(t *testing.T) {
	h, s := newFakeHub(t)
	ids, names := map[string]bool{}, map[string]bool{}
	for _, l := range BuiltinLayouts() {
		if ids[l.ID] || names[l.Name] {
			t.Fatalf("%s: id or name used twice", l.ID)
		}
		ids[l.ID], names[l.Name] = true, true

		_, wid := h.project(strings.TrimPrefix(l.ID, "builtin:"))
		res, err := s.Deploy(wid, DeployRequest{LayoutID: l.ID})
		if err != nil || len(res.Sessions) != len(l.Lanes) {
			t.Fatalf("%s: %v, %d sessions of %d lanes", l.ID, err, len(res.Sessions), len(l.Lanes))
		}
		b, _ := h.Board(wid)
		want := len(l.Lanes) + len(l.Groups) + len(l.Nodes)
		if len(b.Nodes) != want || len(b.Edges) != len(l.Wires) {
			t.Fatalf("%s: %d nodes of %d, %d edges of %d", l.ID, len(b.Nodes), want, len(b.Edges), len(l.Wires))
		}
		// a group frame may hold its members, nothing else may touch anything
		inGroup := map[string]string{}
		for _, n := range b.Nodes {
			if n.Kind == KindGroup {
				for _, m := range GroupOf(n).Members {
					inGroup[m] = n.ID
				}
			}
		}
		for i, a := range b.Nodes {
			for _, c := range b.Nodes[i+1:] {
				if inGroup[a.ID] == c.ID || inGroup[c.ID] == a.ID {
					continue
				}
				if a.X < c.X+c.W && c.X < a.X+a.W && a.Y < c.Y+c.H && c.Y < a.Y+a.H {
					t.Errorf("%s: %s (%v,%v %vx%v) covers %s (%v,%v %vx%v)", l.ID, a.Kind, a.X, a.Y, a.W, a.H, c.Kind, c.X, c.Y, c.W, c.H)
				}
			}
		}
	}
}

// TestLayoutsAndDeploy: built-ins are valid; deploy creates consoles,
// groups, tiles and wires in one batch with lane overrides (machine).
func TestLayoutsAndDeploy(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	for _, l := range BuiltinLayouts() {
		c := l
		if err := validateLayout(&c); err != nil {
			t.Fatalf("%s: %v", l.ID, err)
		}
	}
	if len(BuiltinLayouts()) != 44 {
		t.Fatalf("%d built-ins", len(BuiltinLayouts()))
	}
	var batches int
	h.boards.OnOps = func(string, int64, string, []board.Op) { batches++ }
	res, err := s.Deploy(wid, DeployRequest{LayoutID: "builtin:build-review",
		LaneOverrides: map[string]LaneOverride{"b1": {Agent: "codex", MachineID: "m_devbox"}}})
	if err != nil || len(res.Sessions) != 3 || batches != 1 {
		t.Fatalf("%v %d sessions %d batches", err, len(res.Sessions), batches)
	}
	if h.calls[0] != "create:codex@m_devbox" || h.calls[1] != "create:claude@local" {
		t.Fatalf("lane overrides %v", h.calls)
	}
	if res.Sessions[0].Cwd != "" || res.Sessions[1].Cwd != "/work/app" {
		t.Fatal("cwd only on the project machine")
	}
	b, _ := h.Board(wid)
	kinds := map[string]int{}
	for _, n := range b.Nodes {
		kinds[n.Kind]++
	}
	wires := 0
	for _, e := range b.Edges {
		if e.Kind == EdgeWire {
			wires++
		}
	}
	if kinds["terminal"] != 3 || kinds["group"] != 2 || kinds["orchestrator"] != 1 || kinds["kanban"] != 1 || wires != 2 {
		t.Fatalf("board %v wires %d", kinds, wires)
	}
	// Deploy twice: group names stay unique.
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: "builtin:build-review"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: "nope"}); !errors.Is(err, ErrLayoutNotFound) {
		t.Fatal(err)
	}
	// Save from board: drops ink, images, cards and external URLs.
	ink, _ := json.Marshal(map[string]any{"points": [][]float64{{0, 0, 1}}})
	ext, _ := json.Marshal(map[string]any{"url": "https://example.com/"})
	loc, _ := json.Marshal(map[string]any{"url": "http://localhost:5173/app"})
	kb, _ := json.Marshal(map[string]any{"title": "Tasks", "columns": []any{map[string]any{"id": "c1", "title": "Todo", "cards": []any{map[string]any{"id": "k", "title": "secret"}}}}})
	h.ApplyOps(wid, []board.Op{
		{Op: "put", Node: &board.Node{ID: "n_ink0000001", Kind: "ink", W: 10, H: 10, Data: ink}},
		{Op: "put", Node: &board.Node{ID: "n_web0000001", Kind: "web", W: 10, H: 10, Data: ext}},
		{Op: "put", Node: &board.Node{ID: "n_web0000002", Kind: "web", W: 10, H: 10, Data: loc}},
		{Op: "put", Node: &board.Node{ID: "n_kan0000001", Kind: "kanban", W: 10, H: 10, Data: kb}},
	})
	b, _ = h.Board(wid)
	sess := map[string]session.Info{}
	for _, in := range h.Sessions() {
		sess[in.ID] = in
	}
	l := LayoutFromBoard(b, nil, sess)
	saved, err := s.Layouts.Add(l, time.Now())
	if err != nil || saved.Name != "Set 1" || len(saved.Lanes) != 6 || len(saved.Groups) != 4 {
		t.Fatalf("%v %+v", err, saved)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "example.com") || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), `"ink"`) ||
		!strings.Contains(string(raw), "localhost:5173") || !strings.Contains(string(raw), `"Todo"`) || !strings.Contains(string(raw), `"agent":"codex"`) {
		t.Fatalf("saved layout %s", raw)
	}
	if err := s.Layouts.Delete("builtin:duo"); !errors.Is(err, ErrLayoutBuiltin) {
		t.Fatal(err)
	}
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: saved.ID}); err != nil {
		t.Fatalf("redeploy saved: %v", err)
	}
	_ = p
}
