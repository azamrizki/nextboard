package orch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// Root and part placement of what orchestration starts (v0.9.0-beta.2, docs/API-orchestration.md
// §3.10, §5, §9, §13). acme-shop has three parts: its own folder and Web on This PC, Api on a
// server.

func shopParts(t *testing.T, h *fakeHub) (project.Project, string, project.Part, project.Part) {
	t.Helper()
	return namedShop(t, h, "acme")
}

// namedShop is <name>-shop with Web (/work/<name>-web) and Api (/srv/<name>-api on m_devbox).
func namedShop(t *testing.T, h *fakeHub, name string) (project.Project, string, project.Part, project.Part) {
	t.Helper()
	p, wid := h.project(name + "-shop")
	p, web, err := h.projects.AddPart(p.ID, "Web", "local", "/work/"+name+"-web")
	if err != nil {
		t.Fatal(err)
	}
	p, api, err := h.projects.AddPart(p.ID, "Api", "m_devbox", "/srv/"+name+"-api")
	if err != nil {
		t.Fatal(err)
	}
	return p, wid, web, api
}

// created is where the consoles the last call started were asked to work.
func (h *fakeHub) created(t *testing.T) []session.CreateRequest {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.createReq
	h.createReq = nil
	return out
}

func isRoot(cr session.CreateRequest) bool {
	return cr.Root && cr.PartID == "" && cr.MachineID == "local" && cr.Cwd == ""
}

func TestSpawnPlacement(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid, web, api := shopParts(t, h)

	// the request, a slot, both, and a slot of its own over the request
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 2, Agent: "claude", Root: true}); err != nil {
		t.Fatal(err)
	}
	if got := h.created(t); len(got) != 2 || !isRoot(got[0]) || !isRoot(got[1]) {
		t.Fatalf("root %+v", got)
	}
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Root: true, Slots: []SpawnSlot{{Agent: "claude", Part: "api"}, {Agent: "shell"}}}); err != nil {
		t.Fatal(err)
	}
	got := h.created(t)
	if got[0].PartID != api.ID || got[0].MachineID != "m_devbox" || got[0].Cwd != "/srv/acme-api" || got[0].Root || !isRoot(got[1]) {
		t.Fatalf("slot over request %+v", got)
	}
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 1, Root: true, Part: "Web"}); !errors.Is(err, project.ErrBadPlace) {
		t.Fatalf("root and a part: %v", err)
	}
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Slots: []SpawnSlot{{Agent: "claude"}, {Agent: "claude", Part: "Gateway"}}}); !errors.Is(err, ErrPartNotFound) {
		t.Fatalf("unknown part: %v", err)
	}
	if got := h.created(t); len(got) != 0 {
		t.Fatalf("a bad placement started %+v", got)
	}

	// a client that places nothing: the compat rule, the folder of the first part on the machine
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Slots: []SpawnSlot{{Agent: "claude"}, {Agent: "claude", MachineID: "m_devbox"}}}); err != nil {
		t.Fatal(err)
	}
	got = h.created(t)
	if got[0].PartID != p.Parts[0].ID || got[0].Cwd != "/work/acme-shop" || got[0].Root ||
		got[1].PartID != api.ID || got[1].Cwd != "/srv/acme-api" || got[1].MachineID != "m_devbox" {
		t.Fatalf("compat %+v", got)
	}

	// a new group in Web; adding to it later stays in Web, over the lead's fallback
	res, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 2, Agent: "claude", Part: "web", Group: &SpawnGroup{Name: "Front"}})
	if err != nil {
		t.Fatal(err)
	}
	h.created(t)
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 1, Agent: "codex", Group: &SpawnGroup{ID: res.GroupNodeID}, Fallback: project.Place{Root: true}}); err != nil {
		t.Fatal(err)
	}
	if got := h.created(t); len(got) != 1 || got[0].PartID != web.ID || got[0].Cwd != "/work/acme-web" {
		t.Fatalf("group.add %+v", got)
	}
	// a group whose members differ gives nothing: the fallback applies
	b, _ := h.Board(wid)
	var members []string
	for _, n := range b.Nodes {
		if n.Kind == board.KindTerminal {
			members = append(members, n.ID)
		}
	}
	// the first console is at the root, the third in Api
	mixed := groupNode("n_grpmixed01", "Mixed", "general", members[0], members[2])
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: mixed}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 1, Agent: "claude", Group: &SpawnGroup{ID: mixed.ID}, Fallback: project.Place{Root: true}}); err != nil {
		t.Fatal(err)
	}
	if got := h.created(t); len(got) != 1 || !isRoot(got[0]) {
		t.Fatalf("mixed group %+v", got)
	}
}

// TestCommandPlacement: what the lead and a typed order start goes where the command says, else
// to the UI's place, else to the root; a single-part project has no root of its own.
func TestCommandPlacement(t *testing.T) {
	h, s := newFakeHub(t)
	_, wid, web, api := shopParts(t, h)

	cmd(t, s, wid, "spawn 2 claude")
	if got := h.created(t); len(got) != 2 || !isRoot(got[0]) || !isRoot(got[1]) {
		t.Fatalf("no part named: root %+v", got)
	}
	for text, want := range map[string]string{
		"buat 2 claude di api": api.ID, "spawn a codex in Web part": web.ID, "start claude on web": web.ID,
		"spawn claude at the root": "", "buat claude di semua part": "", "jalankan 2 codex di root": "",
		"make a group of 2 claude in api called Servers": api.ID, "buat tim 2 codex di web bernama Depan": web.ID,
	} {
		cmd(t, s, wid, text)
		got := h.created(t)
		if len(got) == 0 {
			t.Fatalf("%q started nothing", text)
		}
		for _, cr := range got {
			if (want == "" && !isRoot(cr)) || (want != "" && (cr.PartID != want || cr.Root)) {
				t.Fatalf("%q: %+v", text, cr)
			}
		}
	}

	// the UI's place: the focused tile's part
	r, err := s.Command(CommandRequest{Text: "spawn claude", WorkspaceID: wid, ClientID: "c_test", Planner: "off", Place: project.Place{Part: "Api"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.created(t); len(got) != 1 || got[0].PartID != api.ID {
		t.Fatalf("command place %+v %s", got, texts(r))
	}
	// what the order says wins over it
	s.Command(CommandRequest{Text: "spawn claude at the root", WorkspaceID: wid, ClientID: "c_test", Planner: "off", Place: project.Place{Part: "Api"}})
	if got := h.created(t); len(got) != 1 || !isRoot(got[0]) {
		t.Fatalf("typed root over the place %+v", got)
	}
	// a part the project lacks: left out, with a note
	r, err = s.Command(CommandRequest{Text: "spawn claude", WorkspaceID: wid, ClientID: "c_test", Planner: "off", Place: project.Place{Part: "Gateway"}})
	if err != nil || !strings.Contains(texts(r), `no part "Gateway"`) {
		t.Fatalf("unknown place %v %s", err, texts(r))
	}
	if got := h.created(t); len(got) != 1 || !isRoot(got[0]) {
		t.Fatalf("unknown place %+v", got)
	}
	if _, err := s.Command(CommandRequest{Text: "spawn claude", WorkspaceID: wid, ClientID: "c_test", Place: project.Place{Root: true, Part: "Api"}}); !errors.Is(err, project.ErrBadPlace) {
		t.Fatalf("root and a part: %v", err)
	}

	// the lead's steps: part, root, neither (the command's place, else the root), both refused
	sc, _ := s.newScope(wid, "")
	steps, notes := s.translatePlan(sc, planOutput{Steps: []plannerStep{{Op: "spawn", Count: 1, Agent: "claude", Part: "api"},
		{Op: "spawn", Count: 1, Agent: "claude", Root: true}, {Op: "spawn", Count: 1, Agent: "claude"}, {Op: "spawn", Count: 1, Agent: "claude", Root: true, Part: "Web"}}})
	if len(steps) != 3 || len(notes) != 1 || !strings.Contains(notes[0], "root or part") {
		t.Fatalf("steps %+v notes %v", steps, notes)
	}
	s.execute(&execCtx{threadID: sc.proj.ID, projectID: sc.proj.ID, workspaceID: wid, place: project.Place{Part: "web"}}, steps)
	got := h.created(t)
	if len(got) != 3 || got[0].PartID != api.ID || !isRoot(got[1]) || got[2].PartID != web.ID {
		t.Fatalf("lead steps %+v", got)
	}

	// one part: the root is that part, as before
	p1, wid1 := h.project("solo-app")
	cmd(t, s, wid1, "spawn claude at the root")
	cmd(t, s, wid1, "spawn claude")
	for _, cr := range h.created(t) {
		if cr.Root || cr.PartID != p1.Parts[0].ID || cr.Cwd != "/work/solo-app" {
			t.Fatalf("single part %+v", cr)
		}
	}
}

func TestParseRootPlacement(t *testing.T) {
	ctx := ParseContext{Agents: []string{"claude", "codex", "shell"}, Parts: []string{"Admin", "Web App"}}
	for text, want := range map[string]Intent{
		"spawn 2 claude at the root":                  {Kind: IntentSpawn, Count: 2, Agent: "claude", Root: true},
		"spawn a codex in all parts":                  {Kind: IntentSpawn, Count: 1, Agent: "codex", Root: true},
		"start claude on the project root":            {Kind: IntentSpawn, Count: 1, Agent: "claude", Root: true},
		"buat 2 claude di root":                       {Kind: IntentSpawn, Count: 2, Agent: "claude", Root: true},
		"buat 2 claude di semua part":                 {Kind: IntentSpawn, Count: 2, Agent: "claude", Root: true},
		"jalankan codex di semua bagian":              {Kind: IntentSpawn, Count: 1, Agent: "codex", Root: true},
		"buat 2 claude di admin":                      {Kind: IntentSpawn, Count: 2, Agent: "claude", Part: "Admin"},
		"jalankan satu claude pada web app":           {Kind: IntentSpawn, Count: 1, Agent: "claude", Part: "Web App"},
		"make a group of 3 codex in admin called Ops": {Kind: IntentGroupCreate, Count: 3, Agent: "codex", Part: "Admin", Name: "Ops"},
		"make a group of 2 at the root":               {Kind: IntentGroupCreate, Count: 2, Root: true},
		// where it goes does not make work a console
		"buat dokumentasi di root":  {},
		"jalankan backend di admin": {},
	} {
		got := Parse(text, ctx)
		if got.Kind != want.Kind || got.Count != want.Count || got.Agent != want.Agent || got.Part != want.Part || got.Root != want.Root || got.Name != want.Name {
			t.Errorf("%q: %+v", text, got)
		}
	}
	// a part called root is the part
	ctx.Parts = append(ctx.Parts, "Root")
	if got := Parse("spawn claude in root", ctx); got.Part != "Root" || got.Root {
		t.Errorf("a part named root: %+v", got)
	}
}

func TestDeployPlacement(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid, web, api := shopParts(t, h)

	// a lane on another machine goes to the folder of its part there, not to ~
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: "builtin:duo", LaneOverrides: map[string]LaneOverride{"a2": {MachineID: "m_devbox"}}}); err != nil {
		t.Fatal(err)
	}
	got := h.created(t)
	if got[0].PartID != p.Parts[0].ID || got[1].PartID != api.ID || got[1].Cwd != "/srv/acme-api" {
		t.Fatalf("lane machine %+v", got)
	}
	// places per group and per lane, and a lane override
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: "builtin:front-back", Places: map[string]project.Place{"g1": {Part: "web"}, "g2": {Part: "Api"}, "b2": {Root: true}},
		LaneOverrides: map[string]LaneOverride{"f2": {Root: true}}}); err != nil {
		t.Fatal(err)
	}
	got = h.created(t)
	if len(got) != 4 || got[0].PartID != web.ID || !isRoot(got[1]) || got[2].PartID != api.ID || !isRoot(got[3]) {
		t.Fatalf("places %+v", got)
	}
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: "builtin:duo", Places: map[string]project.Place{"g1": {Root: true, Part: "Web"}}}); !errors.Is(err, project.ErrBadPlace) {
		t.Fatalf("bad place %v", err)
	}

	// a layout saved from this board keeps each console's place, by part name
	b, _ := h.Board(wid)
	sess := map[string]session.Info{}
	for _, in := range h.Sessions() {
		sess[in.ID] = in
	}
	l := LayoutFromBoard(b, nil, sess, p)
	parts := map[string]int{}
	for _, ln := range l.Lanes {
		switch {
		case ln.Root:
			parts["(root)"]++
		default:
			parts[ln.Part]++
		}
	}
	if parts["Web"] != 1 || parts["Api"] != 2 || parts["(root)"] != 2 || parts[p.Parts[0].Name] != 1 {
		t.Fatalf("saved places %v", parts)
	}
	// a template with lanes in Web and Api, and an editor card in Web
	tpl := Layout{Name: "Split", Groups: []LayoutGroup{{Key: "g1", Name: "Front", Role: "frontend"}, {Key: "g2", Name: "Back", Role: "backend"}},
		Lanes: []Lane{{Key: "f1", Group: "g1", Part: "web", W: 720, H: 440}, {Key: "f2", Group: "g1", Part: "Web", X: 760, W: 720, H: 440},
			{Key: "b1", Group: "g2", Part: "api", Y: 600, W: 720, H: 440}, {Key: "r1", Root: true, Y: 1200, W: 720, H: 440}},
		Nodes: []LayoutNode{{Key: "e1", Kind: KindEditor, X: 2000, W: 1240, H: 780, Data: map[string]any{"part": "Web"}},
			{Key: "w1", Kind: "web", X: 2000, Y: 900, W: 1024, H: 700, Data: map[string]any{"url": "http://localhost:5173/", "part": "api"}}}}
	saved, err := s.Layouts.Add(tpl, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: saved.ID}); err != nil {
		t.Fatal(err)
	}
	got = h.created(t)
	if got[0].PartID != web.ID || got[1].PartID != web.ID || got[2].PartID != api.ID || !isRoot(got[3]) {
		t.Fatalf("saved names %+v", got)
	}
	editors := func() []*board.Node {
		b, _ := h.Board(wid)
		var out []*board.Node
		for _, n := range b.Nodes {
			if n.Kind == KindEditor {
				out = append(out, n)
			}
		}
		return out
	}
	if e := editors(); len(e) != 1 || editorPlace(p, e[0]) != web.ID {
		t.Fatalf("editor card %+v", e)
	}
	// the web tile previews Api's dev server, on its machine, and is saved with it again
	var webTile *board.Node
	for _, n := range mustBoard(t, h, wid).Nodes {
		if n.Kind == "web" {
			webTile = n
		}
	}
	if d := string(webTile.Data); !strings.Contains(d, `"machineId":"m_devbox"`) || !strings.Contains(d, `"partId":"`+api.ID+`"`) || strings.Contains(d, `"part":`) {
		t.Fatalf("web tile %s", d)
	}
	again := LayoutFromBoard(mustBoard(t, h, wid), []string{webTile.ID}, nil, p)
	if len(again.Nodes) != 1 || again.Nodes[0].Data["part"] != "Api" {
		t.Fatalf("saved web tile %+v", again.Nodes)
	}
	// the same placement again: the card moves, no second one
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: saved.ID}); err != nil {
		t.Fatal(err)
	}
	h.created(t)
	if e := editors(); len(e) != 1 {
		t.Fatalf("doubled editor %d", len(e))
	}
	// placed at the root instead: a card of its own
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: saved.ID, Places: map[string]project.Place{"e1": {Root: true}}}); err != nil {
		t.Fatal(err)
	}
	h.created(t)
	if e := editors(); len(e) != 2 || editorPlace(p, e[1]) != "" || strings.Contains(string(e[1].Data), "part") {
		t.Fatalf("root editor %+v", e)
	}

	// another project with other parts: the deploy asks, a group at a time
	q, wq, _, _ := namedShop(t, h, "beta")
	if q, _, err = h.projects.AddPart(q.ID, "Docs", "local", "/work/docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.projects.PatchPart(q.ID, q.Parts[1].ID, project.PartPatch{Name: ptr("Frontend")}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Deploy(wq, DeployRequest{LayoutID: saved.ID})
	var pu *PlaceUnmatchedError
	if !errors.As(err, &pu) || !errors.Is(err, ErrPlaceUnmatched) {
		t.Fatalf("unmatched %v", err)
	}
	if raw, _ := json.Marshal(pu.Unmatched); string(raw) != `[{"key":"g1","part":"web"},{"key":"e1","part":"Web"}]` {
		t.Fatalf("unmatched list %s", raw)
	}
	if got := h.created(t); len(got) != 0 {
		t.Fatalf("started before asking %+v", got)
	}
	front := q.Parts[1].ID
	if _, err := s.Deploy(wq, DeployRequest{LayoutID: saved.ID, Places: map[string]project.Place{"g1": {Part: "frontend"}, "e1": {Part: "Docs"}}}); err != nil {
		t.Fatal(err)
	}
	if got := h.created(t); got[0].PartID != front || got[1].PartID != front {
		t.Fatalf("matched %+v", got)
	}

	// one part: what the layout saved does not apply
	p1, w1 := h.project("solo-app")
	if _, err := s.Deploy(w1, DeployRequest{LayoutID: saved.ID}); err != nil {
		t.Fatal(err)
	}
	for _, cr := range h.created(t) {
		if cr.Root || cr.PartID != p1.Parts[0].ID {
			t.Fatalf("single part %+v", cr)
		}
	}
	if l := LayoutFromBoard(mustBoard(t, h, w1), nil, sessions(h), p1); strings.Contains(mustJSON(l), `"part"`) || strings.Contains(mustJSON(l), `"root"`) {
		t.Fatalf("single part saves no place %s", mustJSON(l))
	}
}

func ptr[T any](v T) *T { return &v }

func mustBoard(t *testing.T, h *fakeHub, wid string) board.Board {
	t.Helper()
	b, err := h.Board(wid)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sessions(h *fakeHub) map[string]session.Info {
	out := map[string]session.Info{}
	for _, in := range h.Sessions() {
		out[in.ID] = in
	}
	return out
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// TestHandoffKeepsPlacement: the next console works where the source works, a second part on the
// same machine included; the summary runs in the source's folder.
func TestHandoffKeepsPlacement(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid, web, api := shopParts(t, h)
	h.onMach["local/codex"] = true
	start := func(src session.Info, req HandoffRequest) session.CreateRequest {
		t.Helper()
		h.transcr[src.ID] = "user: fix the cart\n\nassistant: halfway"
		req.Agent = "codex"
		ho, err := s.StartHandoff(src.ID, req)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "summary", func() bool { v, _ := s.HandoffView(ho.ID); return v.State == "summarizing" })
		h.lastRun(t, "handoff-summary").OnFinish(doneJob("## Goal\nthe cart", nil))
		got := h.created(t)
		if len(got) != 1 {
			t.Fatalf("handoff started %+v", got)
		}
		return got[0]
	}
	inWeb := h.console(p, wid, "Ayu", "claude", agentstate.Working)
	h.mu.Lock()
	h.sessions[inWeb.ID].PartID, h.sessions[inWeb.ID].Cwd = web.ID, "/work/acme-web"
	h.mu.Unlock()
	h.created(t)
	if cr := start(inWeb, HandoffRequest{}); cr.PartID != web.ID || cr.Cwd != "/work/acme-web" || cr.Root {
		t.Fatalf("second part on the same machine %+v", cr)
	}
	if run := h.lastRun(t, "handoff-summary"); run.Cwd != "/work/acme-web" || run.MachineID != "local" {
		t.Fatalf("summary where the source works %+v", run)
	}
	atRoot := h.console(p, wid, "Bima", "claude", agentstate.Working)
	h.mu.Lock()
	h.sessions[atRoot.ID].Root = true
	h.mu.Unlock()
	if cr := start(atRoot, HandoffRequest{}); !isRoot(cr) {
		t.Fatalf("root %+v", cr)
	}
	other := h.console(p, wid, "Citra", "claude", agentstate.Working)
	if cr := start(other, HandoffRequest{Part: "api"}); cr.PartID != api.ID || cr.MachineID != "m_devbox" {
		t.Fatalf("asked part %+v", cr)
	}
	if _, err := s.StartHandoff(other.ID, HandoffRequest{Agent: "codex", Root: true, Part: "Web"}); !errors.Is(err, project.ErrBadPlace) {
		t.Fatalf("both %v", err)
	}
}

// TestTasksGoByPart: a card about a part goes to a console in it, else to one at the root; the
// card keeps the part's id, so a rename does not lose it.
func TestTasksGoByPart(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid, web, _ := shopParts(t, h)
	rootC := h.console(p, wid, "Root1", "claude", agentstate.Ready)
	webC := h.console(p, wid, "Web1", "claude", agentstate.Ready)
	h.mu.Lock()
	h.sessions[rootC.ID].Root = true
	h.sessions[webC.ID].PartID = web.ID
	h.mu.Unlock()
	orch := &board.Node{ID: "n_orch000001", Kind: KindOrchestrator, W: 400, H: 500, Data: json.RawMessage(`{"title":"Lead"}`)}
	kb, _ := json.Marshal(kanbanData("Tasks", "To do", "In progress", "Done"))
	kan := &board.Node{ID: "n_kan0000001", Kind: "kanban", W: 900, H: 420, Data: kb}
	ops := []board.Op{{Op: "put", Node: orch}, {Op: "put", Node: kan},
		{Op: "putEdge", Edge: &board.Edge{ID: "e_w00000001", Kind: EdgeWire, Source: orch.ID, Target: kan.ID, Data: json.RawMessage(`{}`)}}}
	for i, sid := range []string{rootC.ID, webC.ID} {
		ops = append(ops, board.Op{Op: "putEdge", Edge: &board.Edge{ID: "e_w0000001" + string(rune('a'+i)), Kind: EdgeWire, Source: orch.ID, Target: h.node(wid, sid).ID, Data: json.RawMessage(`{}`)}})
	}
	if err := h.ApplyOps(wid, ops); err != nil {
		t.Fatal(err)
	}
	c := &execCtx{threadID: p.ID, projectID: p.ID, workspaceID: wid}
	for _, title := range []string{"style the cart", "fix the api"} {
		part := "web"
		if title == "fix the api" {
			part = "Api"
		}
		if err := s.applyTaskEdit(c, Step{Op: "task.add", Tile: kan.ID, Task: &TaskEdit{Title: title, Part: part}}); err != nil {
			t.Fatal(err)
		}
	}
	b := mustBoard(t, h, wid)
	if d := string(b.Nodes[len(b.Nodes)-1].Data); !strings.Contains(d, `"part":"Web","partId":"`+web.ID+`"`) {
		t.Fatalf("card keeps the id %s", d)
	}
	// Web's card to Web1, the Api card (no console there) to the root console
	var tile *taskTile
	for _, x := range taskTilesOf(mustBoard(t, h, wid), nil) {
		tile = x
	}
	s.runTasks(wid, tile)
	to := map[string]string{}
	for _, o := range s.Orders.List(func(Order) bool { return true }) {
		to[o.Text] = o.Targets[0].Handle
	}
	if to["style the cart"] != "Web1" || to["fix the api"] != "Root1" {
		t.Fatalf("handed out %v", to)
	}
}

// TestEditorByPlacement: editor.open names the part whose card opens the file, node.add editor
// keeps one card per placement, and "the editor" is the Root card.
func TestEditorByPlacement(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid, web, api := shopParts(t, h)
	sc, _ := s.newScope(wid, "")
	for _, c := range []struct {
		part, path, wantPart, wantPath string
	}{
		{"", "web/src/cart.ts", web.ID, "/work/acme-web/src/cart.ts"},
		{"", "./Api/main.go", api.ID, "/srv/acme-api/main.go"},
		{"api", "cmd/x.go", api.ID, "/srv/acme-api/cmd/x.go"},
		{"", "/work/acme-web/index.html", web.ID, "/work/acme-web/index.html"},
		{"", "/web/index.html", web.ID, "/work/acme-web/index.html"},
		{"", "README.md", p.Parts[0].ID, "/work/acme-shop/README.md"},
		{"", "/usr/lib/x.d.ts", "", "/usr/lib/x.d.ts"},
	} {
		st, problem := s.translateBoardStep(sc, plannerStep{Op: "editor.open", Part: c.part, Path: c.path, Line: 3}, Step{Op: "editor.open"})
		if problem != "" || st.UIAction == nil || st.UIAction.Part != c.wantPart || st.UIAction.Path != c.wantPath || st.UIAction.Line != 3 {
			t.Fatalf("%s %s: %+v %s", c.part, c.path, st.UIAction, problem)
		}
	}
	if _, problem := s.translateBoardStep(sc, plannerStep{Op: "editor.open", Part: "Gateway", Path: "x"}, Step{}); !strings.Contains(problem, "no part") {
		t.Fatalf("unknown part %q", problem)
	}

	// one card per placement
	c := &execCtx{threadID: p.ID, projectID: p.ID, workspaceID: wid}
	add := func(st Step) {
		t.Helper()
		st.Op, st.Kind = "node.add", KindEditor
		if err := s.addNode(c, st); err != nil {
			t.Fatal(err)
		}
	}
	add(Step{Part: "web"})
	add(Step{Data: map[string]any{"part": "Web"}})
	add(Step{})
	add(Step{Root: true})
	b := mustBoard(t, h, wid)
	var cards []*board.Node
	for _, n := range b.Nodes {
		if n.Kind == KindEditor {
			cards = append(cards, n)
		}
	}
	if len(cards) != 2 || editorPlace(p, cards[0]) != web.ID || editorPlace(p, cards[1]) != "" {
		t.Fatalf("cards %+v", cards)
	}
	// a copy of the Web card points to the first one
	cp := &board.Node{ID: "n_edcopy0001", Kind: KindEditor, X: 5000, W: 100, H: 100, Data: cards[0].Data}
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: cp}}); err != nil {
		t.Fatal(err)
	}
	sc, _ = s.newScope(wid, "")
	for want, id := range map[string]string{"editor": cards[1].ID, "code": cards[1].ID, "editor web": cards[0].ID, "Web editor": cards[0].ID,
		"code editor at the root": cards[1].ID} {
		n, _, problem := sc.findTile(want)
		if problem != "" || n.ID != id {
			t.Fatalf("%q: %v %s", want, n, problem)
		}
	}
	// the planner sees which tree each card shows
	snap := mustJSON(s.plannerSnapshot(&execCtx{threadID: p.ID}, sc, "hi").Boards)
	if !strings.Contains(snap, `"part":"Web"`) || !strings.Contains(snap, `"root":true`) || strings.Contains(snap, web.ID) {
		t.Fatalf("snapshot %s", snap)
	}

	// one part: the one card, as before
	p1, w1 := h.project("solo-app")
	c1 := &execCtx{threadID: p1.ID, projectID: p1.ID, workspaceID: w1}
	for _, st := range []Step{{}, {Part: p1.Parts[0].Name}, {Root: true}} {
		st.Op, st.Kind = "node.add", KindEditor
		if err := s.addNode(c1, st); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, x := range mustBoard(t, h, w1).Nodes {
		if x.Kind == KindEditor {
			n++
		}
	}
	sc1, _ := s.newScope(w1, "")
	st, _ := s.translateBoardStep(sc1, plannerStep{Op: "editor.open", Path: "src/a.go"}, Step{})
	if n != 1 || st.UIAction.Path != "/work/solo-app/src/a.go" {
		t.Fatalf("single part: %d cards, %+v", n, st.UIAction)
	}
}
