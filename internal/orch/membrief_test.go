package orch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/session"
)

const (
	memTile  = "n_mem0000001"
	orchTile = "n_orc0000001"
	edTile   = "n_edt0000001"
)

func memoryTileOp(t *testing.T, data map[string]any) board.Op {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return board.Op{Op: "put", Node: &board.Node{ID: memTile, Kind: KindMemory, W: 320, H: 300, Data: raw}}
}

func lastText(t *testing.T, h *fakeHub, sid string) string {
	t.Helper()
	ds := h.deliveriesTo(sid)
	if len(ds) == 0 {
		t.Fatal("nothing was typed")
	}
	return ds[len(ds)-1].Text
}

// The whole point: an agent is only ever sent what it does not have, and nothing new
// costs no turn.
func TestMemoryOnlyWhatIsNew(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	if _, err := s.WriteMemory(p.ID, "project", "## Commands\n- make test runs everything\n\n## Gotchas\n- the hub needs Go 1.26\n", ""); err != nil {
		t.Fatal(err)
	}
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	push := func(force bool) PushResult {
		t.Helper()
		r, err := s.SendMemory(p.ID, MemorySend{WorkspaceID: wid, Targets: []string{ash.ID}, Blocks: []MemoryBlock{{ProjectID: p.ID, Name: MainDoc}}, Force: force})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	if r := push(false); r.Delivered != 1 || r.UpToDate != 0 || r.Chars == 0 {
		t.Fatalf("first send %+v", r)
	}
	text := lastText(t, h, ash.ID)
	if !strings.HasPrefix(text, MemoryPushHead) || !strings.Contains(text, "make test") || !strings.Contains(text, "## Gotchas") {
		t.Fatalf("first text:\n%s", text)
	}

	// It is still in the queue: asking again must not type it twice.
	if r := push(false); r.Delivered+r.Busy != 0 || r.UpToDate != 1 {
		t.Fatalf("second send while queued %+v", r)
	}
	h.setDelivery(s, h.deliveriesTo(ash.ID)[0].ID, delivery.Sent)
	if r := push(false); r.Delivered+r.Busy != 0 || r.UpToDate != 1 || r.Chars != 0 {
		t.Fatalf("nothing new %+v", r)
	}
	if n := len(h.deliveriesTo(ash.ID)); n != 1 {
		t.Fatalf("typed %d times", n)
	}

	// One new fact: that fact, under its heading, and nothing else.
	if _, err := s.AcceptMemory(p.ID, "project", []Fact{{Kind: "gotchas", Text: "tests need a scratch HOME"}}); err != nil {
		t.Fatal(err)
	}
	if r := push(false); r.Delivered != 1 {
		t.Fatalf("new fact %+v", r)
	}
	text = lastText(t, h, ash.ID)
	if !strings.Contains(text, "## Gotchas\n- tests need a scratch HOME") || strings.Contains(text, "make test") || strings.Contains(text, "Critical rules") {
		t.Fatalf("delta:\n%s", text)
	}
	h.setDelivery(s, h.deliveriesTo(ash.ID)[1].ID, delivery.Sent)

	// A fact taken out is said once, without repeating the rest.
	if _, err := s.WriteMemory(p.ID, "project", "## Commands\n- make test runs everything\n", ""); err != nil {
		t.Fatal(err)
	}
	if r := push(false); r.Delivered != 1 {
		t.Fatalf("gone %+v", r)
	}
	if text = lastText(t, h, ash.ID); !strings.Contains(text, "2 facts you were given earlier") || strings.Contains(text, "make test") {
		t.Fatalf("gone text:\n%s", text)
	}
	h.setDelivery(s, h.deliveriesTo(ash.ID)[2].ID, delivery.Sent)
	if r := push(false); r.UpToDate != 1 {
		t.Fatalf("after gone %+v", r)
	}

	// The owner can always insist, after a /clear for instance.
	if r := push(true); r.Delivered != 1 {
		t.Fatalf("force %+v", r)
	}
	if text = lastText(t, h, ash.ID); !strings.Contains(text, "make test") || !strings.Contains(text, "Critical rules") {
		t.Fatalf("force text:\n%s", text)
	}
}

// What never reached the agent does not count as had.
func TestMemoryLostDeliveryIsSentAgain(t *testing.T) {
	for _, state := range []string{delivery.Failed, delivery.Expired, delivery.Cancelled} {
		h, s := newFakeHub(t)
		p, wid := h.project("app")
		if _, err := s.WriteMemory(p.ID, "project", "- the first fact\n", ""); err != nil {
			t.Fatal(err)
		}
		ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
		req := MemorySend{Targets: []string{ash.ID}, Blocks: []MemoryBlock{{ProjectID: p.ID, Name: MainDoc}}}
		if r, err := s.SendMemory(p.ID, req); err != nil || r.Delivered != 1 {
			t.Fatalf("%v %+v", err, r)
		}
		h.setDelivery(s, h.deliveriesTo(ash.ID)[0].ID, state)
		if r, err := s.SendMemory(p.ID, req); err != nil || r.Delivered != 1 {
			t.Fatalf("%s: %v %+v", state, err, r)
		}
		if !strings.Contains(lastText(t, h, ash.ID), "the first fact") {
			t.Fatalf("%s: lost", state)
		}
	}
}

// A hub restart keeps the note: consoles and their context survive it.
func TestMemorySeenSurvivesRestart(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	if _, err := s.WriteMemory(p.ID, "project", "- the first fact\n", ""); err != nil {
		t.Fatal(err)
	}
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	req := MemorySend{Targets: []string{ash.ID}, Blocks: []MemoryBlock{{ProjectID: p.ID, Name: MainDoc}}}
	if _, err := s.SendMemory(p.ID, req); err != nil {
		t.Fatal(err)
	}
	h.setDelivery(s, h.deliveriesTo(ash.ID)[0].ID, delivery.Sent)
	if _, err := s.SendMemory(p.ID, req); err != nil { // settles it
		t.Fatal(err)
	}
	s.seen = newMemorySeen(h.st)
	if r, err := s.SendMemory(p.ID, req); err != nil || r.UpToDate != 1 {
		t.Fatalf("after restart: %v %+v", err, r)
	}
	// A console that is gone leaves nothing behind.
	s.SessionRemoved(ash.ID)
	if s.seen.sessions[ash.ID] != nil {
		t.Fatal("kept the note of a deleted console")
	}
}

// An agent that exited lost its conversation, so it has nothing any more.
func TestMemoryExitedAgentStartsOver(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	if _, err := s.WriteMemory(p.ID, "project", "- the first fact\n", ""); err != nil {
		t.Fatal(err)
	}
	ash := h.console(p, wid, "Ash", "gemini", agentstate.Ready)
	req := MemorySend{Targets: []string{ash.ID}, Blocks: []MemoryBlock{{ProjectID: p.ID, Name: MainDoc}}}
	if _, err := s.SendMemory(p.ID, req); err != nil {
		t.Fatal(err)
	}
	h.setDelivery(s, h.deliveriesTo(ash.ID)[0].ID, delivery.Sent)
	h.setState(ash.ID, agentstate.Exited)
	in, _ := h.SessionInfo(ash.ID)
	s.SessionChanged(in)
	h.setState(ash.ID, agentstate.Ready)
	if r, err := s.SendMemory(p.ID, req); err != nil || r.Delivered != 1 {
		t.Fatalf("%v %+v", err, r)
	}
}

// A block that does not fit, or was asked to stay out, is one line saying where it is.
// An agent without the hub's tools could not follow that, so it gets what fits.
func TestMemoryBudgetAndLookup(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	if _, err := s.WriteMemory(p.ID, "project", "- the project fact\n", ""); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("- "+strings.Repeat("a long fact ", 9)+"\n", 60)
	for _, d := range []struct{ slug, title, content string }{
		{"big", "Big page", long},
		{"small", "Small page", "- a small fact\n"},
		{"later", "Later page", "- read me later\n"},
	} {
		if _, err := s.WriteDoc("", d.slug, d.title, "When it matters", d.content, ""); err != nil {
			t.Fatal(err)
		}
	}
	blocks := []MemoryBlock{{ProjectID: p.ID, Name: MainDoc}, {Name: "big"}, {Name: "small"}, {Name: "later", Lookup: true}}
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)

	if r, err := s.SendMemory(p.ID, MemorySend{Targets: []string{ash.ID}, Blocks: blocks}); err != nil || r.Delivered != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	text := lastText(t, h, ash.ID)
	for _, want := range []string{"the project fact", "a small fact", "- global/big: Big page. When it matters", "- global/later: Later page", "memory_read"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "a long fact") || strings.Contains(text, "read me later") {
		t.Fatalf("sent what should stay out:\n%s", text)
	}
	if n := len([]rune(text)); n > memoryPushChars+2000 {
		t.Fatalf("%d characters", n)
	}

	// It knows where they are: the pointers are not typed again until they change.
	h.setDelivery(s, h.deliveriesTo(ash.ID)[0].ID, delivery.Sent)
	if r, err := s.SendMemory(p.ID, MemorySend{Targets: []string{ash.ID}, Blocks: blocks}); err != nil || r.UpToDate != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := s.WriteDoc("", "later", "Later page", "When it matters", "- read me later\n- and this too\n", ""); err != nil {
		t.Fatal(err)
	}
	if r, err := s.SendMemory(p.ID, MemorySend{Targets: []string{ash.ID}, Blocks: blocks}); err != nil || r.Delivered != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	if text = lastText(t, h, ash.ID); !strings.Contains(text, "- global/later") || strings.Contains(text, "global/big") {
		t.Fatalf("after a change:\n%s", text)
	}

	// Asked for whole, what it was only told about goes in after all.
	h.setDelivery(s, h.deliveriesTo(ash.ID)[1].ID, delivery.Sent)
	if r, err := s.SendMemory(p.ID, MemorySend{Targets: []string{ash.ID}, Blocks: []MemoryBlock{{Name: "later"}}}); err != nil || r.Delivered != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	if text = lastText(t, h, ash.ID); !strings.Contains(text, "read me later") || !strings.Contains(text, "and this too") || strings.Contains(text, "memory_read") {
		t.Fatalf("whole:\n%s", text)
	}
	h.setDelivery(s, h.deliveriesTo(ash.ID)[2].ID, delivery.Sent)
	if r, err := s.SendMemory(p.ID, MemorySend{Targets: []string{ash.ID}, Blocks: []MemoryBlock{{Name: "later"}}}); err != nil || r.UpToDate != 1 {
		t.Fatalf("%v %+v", err, r)
	}

	// No tools: what fits, and how much does not.
	gem := h.console(p, wid, "Gem", "gemini", agentstate.Ready)
	if r, err := s.SendMemory(p.ID, MemorySend{Targets: []string{gem.ID}, Blocks: blocks}); err != nil || r.Delivered != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	text = lastText(t, h, gem.ID)
	if !strings.Contains(text, "a long fact") || !strings.Contains(text, "more facts not included)") || strings.Contains(text, "memory_read") || strings.Contains(text, "memory_search") {
		t.Fatalf("no tools:\n%s", text)
	}
	if n := len([]rune(text)); n > memoryPushChars+2000 {
		t.Fatalf("%d characters", n)
	}
}

// A tile: the project's memory is always on, blocks go on top, and it reaches consoles
// directly, in a group, and through orchestrator and editor cards.
func TestMemoryTileReach(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	other, _ := h.project("acme")
	if _, err := s.WriteMemory(p.ID, "project", "- the project fact\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDoc(other.ID, "design", "Design system", "", "- buttons are square\n", ""); err != nil {
		t.Fatal(err)
	}
	direct := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	member := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	led := h.console(p, wid, "Cas", "claude", agentstate.Ready)
	asked := h.console(p, wid, "Dov", "claude", agentstate.Ready)
	alone := h.console(p, wid, "Eli", "claude", agentstate.Ready)

	if err := h.ApplyOps(wid, []board.Op{memoryTileOp(t, map[string]any{"blocks": []MemoryBlock{{ProjectID: other.ID, Name: "design"}}})}); err != nil {
		t.Fatal(err)
	}
	// Connected to nothing it sends to nobody, and never falls back to everyone.
	r, err := s.PushMemory(p.ID, wid, memTile, nil, nil)
	if err != nil || r.Delivered+r.Busy+r.UpToDate != 0 {
		t.Fatalf("connected to nothing: %v %+v", err, r)
	}
	if len(h.deliveriesTo(alone.ID)) != 0 {
		t.Fatal("typed to a console that is not connected")
	}

	if err := h.ApplyOps(wid, []board.Op{
		{Op: "put", Node: groupNode("n_grp0000001", "Team", "general", h.node(wid, member.ID).ID)},
		{Op: "put", Node: &board.Node{ID: orchTile, Kind: KindOrchestrator, W: 300, H: 200, Data: json.RawMessage(`{"title":"Lead"}`)}},
		{Op: "put", Node: &board.Node{ID: edTile, Kind: KindEditor, W: 300, H: 200, Data: json.RawMessage(`{}`)}},
		wireOp("e_wire000001", memTile, h.node(wid, direct.ID).ID),
		wireOp("e_wire000002", memTile, "n_grp0000001"),
		wireOp("e_wire000003", memTile, orchTile),
		wireOp("e_wire000004", orchTile, h.node(wid, led.ID).ID),
		wireOp("e_wire000005", memTile, edTile),
		wireOp("e_wire000006", edTile, h.node(wid, asked.ID).ID),
	}); err != nil {
		t.Fatal(err)
	}
	r, err = s.PushMemory(p.ID, wid, memTile, nil, nil)
	if err != nil || r.Delivered != 4 {
		t.Fatalf("%v %+v", err, r)
	}
	for _, sid := range []string{direct.ID, member.ID, led.ID, asked.ID} {
		text := lastText(t, h, sid)
		for _, want := range []string{"the project fact", "buttons are square", "another project, acme: Design system"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s misses %q:\n%s", sid, want, text)
			}
		}
	}
	if len(h.deliveriesTo(alone.ID)) != 0 {
		t.Fatal("typed to a console that is not connected")
	}

	// A new wire reaches only what is behind it.
	tr, err := s.PushTile(wid, memTile, []string{h.node(wid, alone.ID).ID})
	if err != nil || tr.Delivered != 1 || len(h.deliveriesTo(direct.ID)) != 1 {
		t.Fatalf("new wire: %v %+v", err, tr)
	}
}

// A tile kept by hand types nothing when wired, and still sends when asked.
func TestMemoryManualTile(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	if _, err := s.WriteMemory(p.ID, "project", "- the project fact\n", ""); err != nil {
		t.Fatal(err)
	}
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	na := h.node(wid, ash.ID).ID
	if err := h.ApplyOps(wid, []board.Op{memoryTileOp(t, map[string]any{"manual": true}), wireOp("e_wire000001", memTile, na)}); err != nil {
		t.Fatal(err)
	}
	if r, err := s.PushTile(wid, memTile, []string{na}); err != nil || r.Delivered != 0 || len(h.deliveriesTo(ash.ID)) != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	if r, err := s.PushTile(wid, memTile, nil); err != nil || r.Delivered != 1 {
		t.Fatalf("%v %+v", err, r)
	}
}

// A tile saved with a scope still means what it meant.
func TestMemoryTileFromBeforeBlocks(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"scope": "global"})
	got := tileBlocks(&board.Node{Data: raw})
	if len(got) != 1 || got[0].Name != MainDoc || got[0].ProjectID != "" {
		t.Fatalf("%+v", got)
	}
	raw, _ = json.Marshal(map[string]any{"scope": "project"})
	if got := tileBlocks(&board.Node{Data: raw}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// At launch the blocks go in the system prompt, which uses no turn, and the note of that
// means nothing is typed afterwards.
func TestMemoryLaunchHoldsTheStack(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	other, _ := h.project("acme")
	if _, err := s.WriteMemory(p.ID, "project", "- the project fact\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteMemory("", "global", "- the global fact\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDoc(other.ID, "design", "Design system", "Colours and spacing", "- buttons are square\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDoc(other.ID, "deploy", "Deploy", "", "- deploys on Friday\n", ""); err != nil {
		t.Fatal(err)
	}
	ash := h.console(p, wid, "Ash", "codex", agentstate.Ready)
	if err := h.ApplyOps(wid, []board.Op{
		memoryTileOp(t, map[string]any{"blocks": []MemoryBlock{{ProjectID: other.ID, Name: "design"}, {ProjectID: other.ID, Name: "deploy", Lookup: true}, {Name: MainDoc}}}),
		wireOp("e_wire000001", memTile, h.node(wid, ash.ID).ID),
	}); err != nil {
		t.Fatal(err)
	}

	text := s.LaunchMemory(ash.ID, wid, p.ID, false)
	for _, want := range []string{"the project fact", "the global fact", "buttons are square", "- " + other.ID + "/deploy: Deploy, from project acme"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "deploys on Friday") {
		t.Fatalf("a lookup block went in whole:\n%s", text)
	}
	if strings.Count(text, "the global fact") != 1 {
		t.Fatalf("global memory twice:\n%s", text)
	}

	if r, err := s.PushMemory(p.ID, wid, memTile, nil, nil); err != nil || r.UpToDate != 1 || r.Delivered != 0 {
		t.Fatalf("after launch: %v %+v", err, r)
	}
	if len(h.deliveriesTo(ash.ID)) != 0 {
		t.Fatal("typed what the launch gave")
	}

	// A console with no tile gets what it always got.
	bea := h.console(p, wid, "Bea", "codex", agentstate.Ready)
	if got := s.LaunchMemory(bea.ID, wid, p.ID, false); strings.Contains(got, "buttons are square") || got != s.MemoryPrompt(p.ID, false) {
		t.Fatalf("no tile:\n%s", got)
	}

	// Claude Code holds the project's memory itself; it counts as had all the same.
	cas := h.console(p, wid, "Cas", "claude", agentstate.Ready)
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000002", memTile, h.node(wid, cas.ID).ID)}); err != nil {
		t.Fatal(err)
	}
	if got := s.LaunchMemory(cas.ID, wid, p.ID, true); strings.Contains(got, "the project fact") || !strings.Contains(got, "buttons are square") {
		t.Fatalf("native:\n%s", got)
	}
	if r, err := s.SendMemory(p.ID, MemorySend{Targets: []string{cas.ID}, Blocks: []MemoryBlock{{ProjectID: p.ID, Name: MainDoc}}}); err != nil || r.UpToDate != 1 {
		t.Fatalf("native push: %v %+v", err, r)
	}
}

// A launch's budget holds, however much is attached.
func TestMemoryLaunchBudget(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	if _, err := s.WriteDoc("", "big", "Big page", "", strings.Repeat("- "+strings.Repeat("x", 300)+"\n", 60), ""); err != nil {
		t.Fatal(err)
	}
	ash := h.console(p, wid, "Ash", "codex", agentstate.Ready)
	if err := h.ApplyOps(wid, []board.Op{
		memoryTileOp(t, map[string]any{"blocks": []MemoryBlock{{Name: "big"}}}),
		wireOp("e_wire000001", memTile, h.node(wid, ash.ID).ID),
	}); err != nil {
		t.Fatal(err)
	}
	if text := s.LaunchMemory(ash.ID, wid, p.ID, false); !strings.Contains(text, "- global/big: Big page") || strings.Contains(text, strings.Repeat("x", 300)) {
		t.Fatalf("too big went in whole: %d", len(text))
	}
}

// Tools reach another project's memory only through a tile connected to this console,
// and only the blocks on it.
func TestMemoryToolsReachOnlyWhatIsAttached(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	other, _ := h.project("acme")
	if _, err := s.WriteDoc(other.ID, "design", "Design system", "", "## Buttons\n- buttons are square zebra\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDoc(other.ID, "secret", "Other page", "", "- a private walrus\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteMemory(other.ID, "project", "- main walrus fact\n", ""); err != nil {
		t.Fatal(err)
	}
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	if err := h.ApplyOps(wid, []board.Op{
		memoryTileOp(t, map[string]any{"blocks": []MemoryBlock{{ProjectID: other.ID, Name: "design"}}}),
		wireOp("e_wire000001", memTile, h.node(wid, ash.ID).ID),
	}); err != nil {
		t.Fatal(err)
	}
	name := other.ID + "/design"

	got, err := s.MemoryReadFor(ash.ID, wid, p.ID, name, "")
	if err != nil || !strings.Contains(got, "zebra") {
		t.Fatalf("attached: %v %s", err, got)
	}
	if got, err = s.MemoryReadFor(ash.ID, wid, p.ID, name, "buttons"); err != nil || !strings.Contains(got, "zebra") {
		t.Fatalf("topic: %v %s", err, got)
	}
	if got, err = s.MemoryReadFor(ash.ID, wid, p.ID, "", ""); err != nil || !strings.Contains(got, name) {
		t.Fatalf("overview: %v %s", err, got)
	}
	for _, refused := range []string{other.ID + "/secret", other.ID + "/" + MainDoc, other.ID + "/" + CriticalSlug} {
		if got, err := s.MemoryReadFor(ash.ID, wid, p.ID, refused, ""); err == nil {
			t.Fatalf("read %s: %s", refused, got)
		}
	}
	if _, _, ok := attachedDocName(other.ID+"/"+CriticalSlug, []MemoryBlock{{ProjectID: other.ID, Name: CriticalSlug}}); ok {
		t.Fatal("another project's rules are not a block")
	}
	// Not connected: not readable.
	if got, err := s.MemoryReadFor(bea.ID, wid, p.ID, name, ""); err == nil {
		t.Fatalf("not connected: %s", got)
	}

	if got := s.MemorySearchFor(ash.ID, wid, p.ID, "zebra", 0); !strings.Contains(got, "buttons are square") || !strings.Contains(got, name) {
		t.Fatalf("search: %s", got)
	}
	if got := s.MemorySearchFor(ash.ID, wid, p.ID, "walrus", 0); strings.Contains(got, "walrus fact") || strings.Contains(got, "private walrus") {
		t.Fatalf("search found what is not attached: %s", got)
	}
	if got := s.MemorySearchFor(bea.ID, wid, p.ID, "zebra", 0); strings.Contains(got, "buttons are square") {
		t.Fatalf("search, not connected: %s", got)
	}
}

// A fact a console saved itself does not come back to it, and does reach the others.
func TestMemoryOwnFactIsNotSentBack(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "codex", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "codex", agentstate.Ready)
	s.LaunchMemory(ash.ID, wid, p.ID, false)
	s.LaunchMemory(bea.ID, wid, p.ID, false)
	if err := s.SaveMemoryFact(p.ID, "Commands", "the build command is make zebra", ""); err != nil {
		t.Fatal(err)
	}
	s.MemorySaved(ash.ID, p.ID, "", "the build command is make zebra")
	r, err := s.SendMemory(p.ID, MemorySend{Targets: []string{ash.ID, bea.ID}, Blocks: []MemoryBlock{{ProjectID: p.ID, Name: MainDoc}}})
	if err != nil || r.UpToDate != 1 || r.Delivered != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	if len(h.deliveriesTo(ash.ID)) != 0 || !strings.Contains(lastText(t, h, bea.ID), "make zebra") {
		t.Fatalf("ash %d", len(h.deliveriesTo(ash.ID)))
	}
}

// The planner has no tools and runs on every command: it gets the blocks of a tile
// connected to its orchestrator, within a small budget.
func TestMemoryPlannerGetsTheBlocks(t *testing.T) {
	h, s := newFakeHub(t)
	_, wid := h.project("app")
	other, _ := h.project("acme")
	if _, err := s.WriteDoc(other.ID, "design", "Design system", "", "- buttons are square\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDoc("", "big", "Big page", "", strings.Repeat("- "+strings.Repeat("y", 100)+"\n", 40), ""); err != nil {
		t.Fatal(err)
	}
	second := "n_orc0000002"
	if err := h.ApplyOps(wid, []board.Op{
		memoryTileOp(t, map[string]any{"blocks": []MemoryBlock{{ProjectID: other.ID, Name: "design"}, {Name: "big"}}}),
		{Op: "put", Node: &board.Node{ID: orchTile, Kind: KindOrchestrator, W: 300, H: 200, Data: json.RawMessage(`{"title":"Lead"}`)}},
		{Op: "put", Node: &board.Node{ID: second, Kind: KindOrchestrator, W: 300, H: 200, Data: json.RawMessage(`{"title":"Other"}`)}},
	}); err != nil {
		t.Fatal(err)
	}
	blocks := func(card string) string {
		t.Helper()
		sc, err := s.newScope(wid, card)
		if err != nil {
			t.Fatal(err)
		}
		return s.plannerBlocks(sc)
	}
	if got := blocks(""); got != "" {
		t.Fatalf("not connected: %s", got)
	}
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000001", memTile, orchTile)}); err != nil {
		t.Fatal(err)
	}
	got := blocks(orchTile)
	if !strings.Contains(got, "buttons are square") || !strings.Contains(got, "not included") || strings.Contains(got, "yyyy") {
		t.Fatalf("connected:\n%s", got)
	}
	if blocks(second) != "" {
		t.Fatal("another orchestrator got them")
	}
	if !strings.Contains(blocks(""), "buttons are square") {
		t.Fatal("the main bar is the same lead")
	}
}

// A plan sends nothing, and says what a send would cost per console.
func TestMemoryPlan(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	other, _ := h.project("acme")
	if _, err := s.WriteMemory(p.ID, "project", "- the project fact\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDoc(other.ID, "design", "Design system", "", "- buttons are square\n", ""); err != nil {
		t.Fatal(err)
	}
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	s.LaunchMemory(bea.ID, wid, p.ID, true)
	shell := h.console(p, wid, "Sh", "shell", "")
	if err := h.ApplyOps(wid, []board.Op{
		memoryTileOp(t, map[string]any{"blocks": []MemoryBlock{{ProjectID: other.ID, Name: "design"}, {Name: "gone"}, {ProjectID: "p_gone", Name: MainDoc}}}),
		wireOp("e_wire000001", memTile, h.node(wid, ash.ID).ID),
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanMemory(p.ID, MemorySend{WorkspaceID: wid, MemoryNodeID: memTile})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Docs) != 4 || !plan.Docs[0].Locked || plan.Docs[1].Owner != "acme" || plan.Docs[1].Chars == 0 || !plan.Docs[2].Missing || !plan.Docs[3].Missing {
		t.Fatalf("docs %+v", plan.Docs)
	}
	if len(plan.Targets) != 1 || plan.Targets[0].SessionID != ash.ID || plan.Targets[0].Chars == 0 || !plan.Targets[0].CanSend {
		t.Fatalf("targets %+v", plan.Targets)
	}
	if len(h.deliveriesTo(ash.ID)) != 0 {
		t.Fatal("a plan typed something")
	}

	// Without a tile or targets: every agent of the project, and only agents.
	plan, err = s.PlanMemory(p.ID, MemorySend{Blocks: []MemoryBlock{{ProjectID: p.ID, Name: MainDoc}}})
	if err != nil || len(plan.Targets) != 2 {
		t.Fatalf("%v %+v", err, plan.Targets)
	}
	for _, x := range plan.Targets {
		if x.SessionID == shell.ID {
			t.Fatal("a shell is not an agent")
		}
		if x.SessionID == bea.ID && x.Chars != 0 {
			t.Fatalf("it has this already: %+v", x)
		}
	}
	if _, err := s.PlanMemory(p.ID, MemorySend{WorkspaceID: wid, MemoryNodeID: "n_none0000001"}); !errors.Is(err, ErrTileNotFound) {
		t.Fatalf("unknown tile: %v", err)
	}
}

func TestMemoryTileValidation(t *testing.T) {
	mem := func(data string) *board.Node {
		return &board.Node{ID: memTile, Kind: KindMemory, W: 320, H: 300, Data: json.RawMessage(data)}
	}
	check := func(n *board.Node) error {
		return ValidateBoard([]*board.Node{n}, nil, map[string]bool{n.ID: true}, nil)
	}
	if err := check(mem(`{"blocks":[{"projectId":"p_a","name":"design","lookup":true},{"name":"main"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := check(mem(`{"blocks":[{"name":"critical"}]}`)); err == nil {
		t.Fatal("critical notes as a block")
	}
	if err := check(mem(`{"blocks":[{"name":""}]}`)); err == nil {
		t.Fatal("a block with no name")
	}
	many, _ := json.Marshal(map[string]any{"blocks": make([]MemoryBlock, maxMemoryBlocks+1)})
	if err := check(mem(string(many))); err == nil {
		t.Fatal("too many blocks")
	}

	// What a memory tile may be wired to: more than a note may.
	note := &board.Node{ID: "n_note000001", Kind: "note"}
	orc := &board.Node{ID: orchTile, Kind: KindOrchestrator}
	ed := &board.Node{ID: edTile, Kind: KindEditor}
	mt := mem(`{}`)
	for _, c := range []struct {
		src, dst *board.Node
		want     string
	}{{mt, orc, EdgeWire}, {mt, ed, EdgeWire}, {note, orc, ""}, {note, ed, ""}, {orc, ed, ""}, {ed, orc, EdgeWire}} {
		if got := edgeKindFor(c.src, c.dst); got != c.want {
			t.Fatalf("%s → %s: %q", c.src.Kind, c.dst.Kind, got)
		}
	}
}

// A saved layout keeps its stack. Its consoles start before the wires exist, so the
// blocks are typed once they do, unless the tile is kept by hand.
func TestMemoryTileInALayout(t *testing.T) {
	for _, manual := range []bool{false, true} {
		h, s := newFakeHub(t)
		p, wid := h.project("app")
		if _, err := s.WriteDoc("", "writing", "Writing style", "", "- short sentences\n", ""); err != nil {
			t.Fatal(err)
		}
		ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
		if err := h.ApplyOps(wid, []board.Op{
			memoryTileOp(t, map[string]any{"manual": manual, "blocks": []MemoryBlock{{Name: "writing", Lookup: true}}}),
			wireOp("e_wire000001", memTile, h.node(wid, ash.ID).ID),
		}); err != nil {
			t.Fatal(err)
		}
		b, _ := h.Board(wid)
		l := LayoutFromBoard(b, nil, map[string]session.Info{ash.ID: ash})
		l.Name = "With a stack"
		var kept []MemoryBlock
		for _, n := range l.Nodes {
			if n.Kind == KindMemory {
				raw, _ := json.Marshal(n.Data)
				kept = tileBlocks(&board.Node{Data: raw})
			}
		}
		if len(kept) != 1 || kept[0].Name != "writing" || !kept[0].Lookup {
			t.Fatalf("the layout lost the stack: %+v", l.Nodes)
		}
		saved, err := s.Layouts.Add(l, t0)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Deploy(wid, DeployRequest{LayoutID: saved.ID})
		if err != nil || len(res.Sessions) != 1 {
			t.Fatalf("%v %+v", err, res)
		}
		got := h.deliveriesTo(res.Sessions[0].ID)
		if manual {
			if len(got) != 0 {
				t.Fatalf("a tile kept by hand typed %q", got[0].Text)
			}
			continue
		}
		if len(got) != 1 || !strings.Contains(got[0].Text, "global/writing") {
			t.Fatalf("deliveries %+v", got)
		}
	}
}
