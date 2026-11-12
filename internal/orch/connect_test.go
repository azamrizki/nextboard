package orch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/preview"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

func forwardOp(id, src, dst string) board.Op {
	return board.Op{Op: "putEdge", Edge: &board.Edge{ID: id, Kind: EdgeForward, Source: src, Target: dst, Data: json.RawMessage(`{"mode":"digest","trigger":"turn"}`)}}
}

func wireOp(id, src, dst string) board.Op {
	return board.Op{Op: "putEdge", Edge: &board.Edge{ID: id, Kind: EdgeWire, Source: src, Target: dst, Data: json.RawMessage(`{}`)}}
}

func placeTile(t *testing.T, h *fakeHub, wid, id, kind string, data any) {
	t.Helper()
	raw, _ := json.Marshal(data)
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: &board.Node{ID: id, Kind: kind, X: 900, Y: 900, W: 300, H: 200, Data: raw}}}); err != nil {
		t.Fatal(err)
	}
}

func threadHas(s *Service, p project.Project, want string) bool {
	for _, m := range s.Threads.Last(p.ID, 50) {
		if strings.Contains(m.Text, want) {
			return true
		}
	}
	return false
}

// TestEdgeHoldsResultsForItsMinute: what ends inside an edge's minute is sent when
// the minute is up, in one digest; nothing is lost and the rate stays one a minute.
func TestEdgeHoldsResultsForItsMinute(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	b := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	if err := h.ApplyOps(wid, []board.Op{forwardOp("e_fwd0000011", h.node(wid, a.ID).ID, h.node(wid, b.ID).ID)}); err != nil {
		t.Fatal(err)
	}
	reply := func(text string, at time.Time) {
		h.mu.Lock()
		h.replies[a.ID] = &session.Reply{Text: text, At: at}
		h.mu.Unlock()
		finish(t, h, s, a.ID, at)
	}
	reply("first result", t0)
	if len(h.deliveriesTo(b.ID)) != 1 {
		t.Fatal("first turn not forwarded")
	}
	reply("second result", t0.Add(10*time.Second))
	reply("third result", t0.Add(20*time.Second))
	if len(h.deliveriesTo(b.ID)) != 1 {
		t.Fatal("sent inside the minute")
	}
	if !threadHas(s, p, "waits 50 s") {
		t.Fatal("no note that it waits")
	}
	s.Tick(t0.Add(59 * time.Second))
	if len(h.deliveriesTo(b.ID)) != 1 {
		t.Fatal("sent before the minute was up")
	}
	s.Tick(t0.Add(60 * time.Second))
	ds := h.deliveriesTo(b.ID)
	if len(ds) != 2 || !strings.Contains(ds[1].Text, "second result") || !strings.Contains(ds[1].Text, "third result") || ds[1].ForwardDepth != 1 {
		t.Fatalf("held results %+v", ds)
	}
	s.Tick(t0.Add(61 * time.Second))
	if len(h.deliveriesTo(b.ID)) != 2 {
		t.Fatal("sent twice")
	}
	// the send started a new minute
	reply("fourth result", t0.Add(70*time.Second))
	if len(h.deliveriesTo(b.ID)) != 2 {
		t.Fatal("the held send did not count as the edge's send")
	}
	s.Tick(t0.Add(120 * time.Second))
	if ds = h.deliveriesTo(b.ID); len(ds) != 3 || !strings.Contains(ds[2].Text, "fourth result") {
		t.Fatalf("after the held send %+v", ds)
	}
}

// TestEdgesHaveTheirOwnMinute: a console forwarding to two places reaches both.
func TestEdgesHaveTheirOwnMinute(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	b := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	c := h.console(p, wid, "Cass", "claude", agentstate.Ready)
	na := h.node(wid, a.ID).ID
	if err := h.ApplyOps(wid, []board.Op{forwardOp("e_fwd0000021", na, h.node(wid, b.ID).ID), forwardOp("e_fwd0000022", na, h.node(wid, c.ID).ID)}); err != nil {
		t.Fatal(err)
	}
	h.replies[a.ID] = &session.Reply{Text: "result", At: t0}
	finish(t, h, s, a.ID, t0)
	if len(h.deliveriesTo(b.ID)) != 1 || len(h.deliveriesTo(c.ID)) != 1 {
		t.Fatalf("to Bea %d, to Cass %d", len(h.deliveriesTo(b.ID)), len(h.deliveriesTo(c.ID)))
	}
}

// TestEdgeQueueSurvivesRestart: a hub restart inside the minute loses nothing.
func TestEdgeQueueSurvivesRestart(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	b := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	if err := h.ApplyOps(wid, []board.Op{forwardOp("e_fwd0000031", h.node(wid, a.ID).ID, h.node(wid, b.ID).ID)}); err != nil {
		t.Fatal(err)
	}
	h.replies[a.ID] = &session.Reply{Text: "first", At: t0}
	finish(t, h, s, a.ID, t0)
	h.replies[a.ID] = &session.Reply{Text: "held over a restart", At: t0.Add(5 * time.Second)}
	finish(t, h, s, a.ID, t0.Add(5*time.Second))
	s2 := New(h, h.st)
	s2.Now = func() time.Time { return t0.Add(time.Minute) }
	s2.Tick(t0.Add(time.Minute))
	if ds := h.deliveriesTo(b.ID); len(ds) != 2 || !strings.Contains(ds[1].Text, "held over a restart") {
		t.Fatalf("after restart %+v", ds)
	}
}

// TestRemovedEdgeSendsNothing: removing the line during the wait is a no.
func TestRemovedEdgeSendsNothing(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	b := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	if err := h.ApplyOps(wid, []board.Op{forwardOp("e_fwd0000041", h.node(wid, a.ID).ID, h.node(wid, b.ID).ID)}); err != nil {
		t.Fatal(err)
	}
	h.replies[a.ID] = &session.Reply{Text: "first", At: t0}
	finish(t, h, s, a.ID, t0)
	h.replies[a.ID] = &session.Reply{Text: "second", At: t0.Add(5 * time.Second)}
	finish(t, h, s, a.ID, t0.Add(5*time.Second))
	if err := h.ApplyOps(wid, []board.Op{{Op: "delEdge", ID: "e_fwd0000041"}}); err != nil {
		t.Fatal(err)
	}
	s.Tick(t0.Add(time.Minute))
	if len(h.deliveriesTo(b.ID)) != 1 {
		t.Fatal("sent over a removed line")
	}
	if !threadHas(s, p, "its line was removed") {
		t.Fatal("no note")
	}
}

// TestContextTurnIsNotForwarded: an agent's answer to memory or a tile is no result:
// it does not reach the next agent and does not use the edge's minute.
func TestContextTurnIsNotForwarded(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	b := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	if err := h.ApplyOps(wid, []board.Op{forwardOp("e_fwd0000051", h.node(wid, a.ID).ID, h.node(wid, b.ID).ID)}); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return t0 }
	if err := writeMemory(s, p.ID); err != nil {
		t.Fatal(err)
	}
	res, err := s.PushMemory(p.ID, wid, "", []string{a.ID}, []string{"project"})
	if err != nil || res.Delivered != 1 {
		t.Fatalf("push %+v %v", res, err)
	}
	h.setDelivery(s, res.DeliveryIDs[0], delivery.Sent)
	h.replies[a.ID] = &session.Reply{Text: "Noted.", At: t0.Add(time.Second)}
	finish(t, h, s, a.ID, t0.Add(time.Second))
	if len(h.deliveriesTo(b.ID)) != 0 {
		t.Fatal("the answer to a memory push was forwarded")
	}
	h.replies[a.ID] = &session.Reply{Text: "real result", At: t0.Add(5 * time.Second)}
	finish(t, h, s, a.ID, t0.Add(5*time.Second))
	if ds := h.deliveriesTo(b.ID); len(ds) != 1 || !strings.Contains(ds[0].Text, "real result") {
		t.Fatalf("real result %+v", ds)
	}
}

// TestContextTurnTiming: which turn counts as the answer to context.
func TestContextTurnTiming(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	x := h.console(p, wid, "Xan", "claude", agentstate.Ready)
	y := h.console(p, wid, "Yul", "claude", agentstate.Ready)
	nx := h.node(wid, x.ID).ID
	placeTile(t, h, wid, "n_note000001", "note", map[string]any{"text": "context"})
	if err := h.ApplyOps(wid, []board.Op{forwardOp("e_fwd0000052", nx, h.node(wid, y.ID).ID)}); err != nil {
		t.Fatal(err)
	}
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	turn := func(m int) int {
		h.mu.Lock()
		h.replies[x.ID] = &session.Reply{Text: "reply", At: at(m)}
		h.mu.Unlock()
		before := len(h.deliveriesTo(y.ID))
		finish(t, h, s, x.ID, at(m))
		return len(h.deliveriesTo(y.ID)) - before
	}
	send := func() string {
		res, err := s.PushTile(wid, "n_note000001", []string{nx})
		if err != nil || len(res.DeliveryIDs) != 1 {
			t.Fatalf("%+v %v", res, err)
		}
		return res.DeliveryIDs[0]
	}
	s.Now = func() time.Time { return at(10) }
	// Not typed yet: the turn that just ended is work.
	id := send()
	if turn(1) != 1 {
		t.Fatal("a turn before the context was typed did not forward")
	}
	// Typed later than the turn ended: the same.
	s.Now = func() time.Time { return at(10) }
	h.setDelivery(s, id, delivery.Sent)
	if turn(2) != 1 {
		t.Fatal("an older turn counted as the answer to context")
	}
	// Its answer is held back, also when the engine's confirmation comes after it...
	if turn(11) != 0 {
		t.Fatal("the answer to context was forwarded")
	}
	h.setDelivery(s, id, delivery.Confirmed)
	// ...and the next turn is work again.
	if turn(13) != 1 {
		t.Fatal("work after a late confirmation was held back")
	}
	// Never reached the agent: nothing to hold back.
	s.Now = func() time.Time { return at(20) }
	id = send()
	h.setDelivery(s, id, delivery.Failed)
	if turn(22) != 1 {
		t.Fatal("a failed delivery held a turn back")
	}
	// Its turn was never seen: much later work is not its answer.
	s.Now = func() time.Time { return at(30) }
	id = send()
	h.setDelivery(s, id, delivery.Sent)
	if turn(40) != 1 {
		t.Fatal("a turn long after the context counted as its answer")
	}
}

// TestPushTile: what each tile sends, to whom, and what it refuses.
func TestPushTile(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	sh := h.console(p, wid, "Kit", "shell", agentstate.Shell)
	na, nsh := h.node(wid, a.ID).ID, h.node(wid, sh.ID).ID
	h.docText = "# The spec"
	cases := []struct {
		kind string
		data any
		want []string
	}{
		{"note", map[string]any{"text": "use port 8080"}, []string{"a note", "use port 8080", "not a task"}},
		{"checklist", map[string]any{"title": "Ship", "items": []any{map[string]any{"text": "tests", "done": true}, map[string]any{"text": "docs"}}}, []string{`"Ship"`, "- [x] tests", "- [ ] docs"}},
		{"kanban", map[string]any{"title": "Work", "columns": []any{map[string]any{"title": "To do", "cards": []any{map[string]any{"title": "Fix login", "priority": "high", "notes": "see logs"}}}, map[string]any{"title": "Empty", "cards": []any{}}}}, []string{"## To do", "- Fix login (high priority)", "  see logs"}},
		{"calendar", map[string]any{"title": "Plan", "events": []any{map[string]any{"title": "Later", "date": "2026-12-01"}, map[string]any{"title": "Sooner", "date": "2026-11-05", "time": "09:00"}}}, []string{"- 2026-11-05 09:00: Sooner\n- 2026-12-01: Later"}},
		{"link", map[string]any{"title": "Docs", "url": "https://example.com/a"}, []string{"Docs\nhttps://example.com/a"}},
		{"file", map[string]any{"path": "/srv/app/main.go", "machineId": "m_x", "snapshot": map[string]any{"content": "package main"}}, []string{"File: /srv/app/main.go (on machine m_x)", "package main"}},
		{"image", map[string]any{"src": map[string]any{"asset": "as_1"}, "alt": "login page"}, []string{"Image file: /data/assets/as_1", "Description: login page"}},
		{"document", map[string]any{"documentId": "d_1"}, []string{`"Spec"`, "# The spec"}},
		{"branches", map[string]any{"projectId": p.ID}, []string{"- main (current)"}},
		{"web", map[string]any{"url": "http://localhost:3000/"}, []string{"the page http://localhost:3000/", "not a task"}},
	}
	for i, c := range cases {
		id := "n_tile0000" + string(rune('a'+i))
		placeTile(t, h, wid, id, c.kind, c.data)
		res, err := s.PushTile(wid, id, []string{na, nsh})
		if err != nil || res.Delivered != 1 || res.NotAgent != 1 || res.Empty {
			t.Fatalf("%s: %+v %v", c.kind, res, err)
		}
		ds := h.deliveriesTo(a.ID)
		last := ds[len(ds)-1]
		if last.Source != SourceTile {
			t.Fatalf("%s: source %q", c.kind, last.Source)
		}
		for _, w := range c.want {
			if !strings.Contains(last.Text, w) {
				t.Fatalf("%s: %q not in %q", c.kind, w, last.Text)
			}
		}
	}
	if len(h.deliveriesTo(sh.ID)) != 0 {
		t.Fatal("typed into a shell")
	}
	// Nothing to send is not an error, and nothing is typed.
	placeTile(t, h, wid, "n_blank00001", "note", map[string]any{"text": "  "})
	before := len(h.deliveriesTo(a.ID))
	if res, err := s.PushTile(wid, "n_blank00001", []string{na}); err != nil || !res.Empty {
		t.Fatalf("blank %+v %v", res, err)
	}
	if len(h.deliveriesTo(a.ID)) != before {
		t.Fatal("a blank tile was sent")
	}
	// Without targets it goes to what it is wired to.
	placeTile(t, h, wid, "n_wired00001", "note", map[string]any{"text": "wired one"})
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000001", "n_wired00001", na)}); err != nil {
		t.Fatal(err)
	}
	if res, err := s.PushTile(wid, "n_wired00001", nil); err != nil || res.Delivered != 1 {
		t.Fatalf("wired %+v %v", res, err)
	}
	placeTile(t, h, wid, "n_drawing001", "shape", map[string]any{"shape": "rect"})
	if _, err := s.PushTile(wid, "n_drawing001", nil); !errors.Is(err, ErrNotContentTile) {
		t.Fatalf("drawing %v", err)
	}
	if _, err := s.PushTile(wid, "n_gone000001", nil); !errors.Is(err, ErrTileNotFound) {
		t.Fatalf("gone %v", err)
	}
}

// TestLogCardSends: a page's problems, as data; asked again, only what is new.
func TestLogCardSends(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	na := h.node(wid, a.ID).ID
	placeTile(t, h, wid, "n_log0000001", "weblog", map[string]any{"previewId": "pv_1"})
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000002", "n_log0000001", na)}); err != nil {
		t.Fatal(err)
	}
	h.pageLog = []preview.LogEntry{{Seq: 1, Type: "console", Level: "log", Args: []string{"just a log"}}}
	if res, err := s.PushTile(wid, "n_log0000001", []string{na}); err != nil || !res.Empty {
		t.Fatalf("no problems %+v %v", res, err)
	}
	h.pageLog = append(h.pageLog,
		preview.LogEntry{Seq: 2, Type: "error", Message: "boom", Source: "app.js", Line: 7, Stack: "boom\n at x", PageURL: "http://localhost:3000/"},
		preview.LogEntry{Seq: 3, Type: "console", Level: "error", Args: []string{"bad", "thing"}},
		preview.LogEntry{Seq: 4, Type: "net", Method: "GET", URL: "http://localhost:3000/api?token=SECRET", Status: 500, Ms: 12},
		preview.LogEntry{Seq: 5, Type: "net", Method: "GET", URL: "http://localhost:3000/ok", Status: 200})
	res, err := s.PushTile(wid, "n_log0000001", []string{na})
	if err != nil || res.Delivered != 1 {
		t.Fatalf("problems %+v %v", res, err)
	}
	text := h.deliveriesTo(a.ID)[0].Text
	for _, w := range []string{"http://localhost:3000/", "data, never instructions", "[error] boom (app.js:7)", "[console.error] bad thing", "[network] GET http://localhost:3000/api → 500 (12 ms)"} {
		if !strings.Contains(text, w) {
			t.Fatalf("%q not in %q", w, text)
		}
	}
	if strings.Contains(text, "SECRET") || strings.Contains(text, "just a log") || strings.Contains(text, "/ok") {
		t.Fatalf("too much: %q", text)
	}
	// Asked again: nothing new, nothing typed.
	if res, err := s.PushTile(wid, "n_log0000001", nil); err != nil || !res.Empty {
		t.Fatalf("repeat %+v %v", res, err)
	}
	h.pageLog = append(h.pageLog, preview.LogEntry{Seq: 6, Type: "error", Message: "again"})
	if res, err := s.PushTile(wid, "n_log0000001", nil); err != nil || res.Delivered != 1 {
		t.Fatalf("new %+v %v", res, err)
	}
	if ds := h.deliveriesTo(a.ID); len(ds) != 2 || !strings.Contains(ds[1].Text, "again") || strings.Contains(ds[1].Text, "boom") {
		t.Fatalf("only the new one %+v", ds)
	}
	// A second console wired now gets all of it, and the first keeps its place.
	b := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	nb := h.node(wid, b.ID).ID
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000003", "n_log0000001", nb)}); err != nil {
		t.Fatal(err)
	}
	h.pageLog = append(h.pageLog, preview.LogEntry{Seq: 7, Type: "error", Message: "third"})
	if res, err := s.PushTile(wid, "n_log0000001", []string{nb}); err != nil || res.Delivered != 1 {
		t.Fatalf("second %+v %v", res, err)
	}
	if ds := h.deliveriesTo(b.ID); len(ds) != 1 || !strings.Contains(ds[0].Text, "boom") || !strings.Contains(ds[0].Text, "third") {
		t.Fatalf("second console %+v", ds)
	}
	if res, err := s.PushTile(wid, "n_log0000001", nil); err != nil || res.Delivered != 2 {
		t.Fatalf("first keeps its place %+v %v", res, err)
	}
	if ds := h.deliveriesTo(a.ID); !strings.Contains(ds[len(ds)-1].Text, "third") {
		t.Fatalf("first console %+v", ds)
	}
}

// TestConnectRefusesPlainShells: a planner cannot connect what would do nothing, and
// says why; a content tile sends itself once when connected.
func TestConnectRefusesPlainShells(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	sh := h.console(p, wid, "Kit", "shell", agentstate.Shell)
	na, nsh := h.node(wid, a.ID).ID, h.node(wid, sh.ID).ID
	placeTile(t, h, wid, "n_note000001", "note", map[string]any{"text": "the context"})
	placeTile(t, h, wid, "n_orch000001", KindOrchestrator, map[string]any{"title": "Boss"})
	gd, _ := json.Marshal(GroupData{Name: "Shells", Members: []string{nsh}})
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: &board.Node{ID: "n_group00001", Kind: KindGroup, W: 400, H: 300, Data: gd}}}); err != nil {
		t.Fatal(err)
	}
	c := &execCtx{threadID: p.ID, projectID: p.ID, workspaceID: wid}
	edges := func() []*board.Edge {
		b, _ := h.Board(wid)
		return b.Edges
	}
	for _, st := range []Step{
		{Op: "connect", Source: "n_orch000001", Tiles: []string{nsh}},
		{Op: "connect", Source: "n_orch000001", Tiles: []string{"n_group00001"}},
		{Op: "connect", Source: na, Tiles: []string{nsh}},
		{Op: "connect", Source: nsh, Tiles: []string{na}},
		{Op: "connect", Source: "n_note000001", Tiles: []string{nsh}},
	} {
		if err := s.execConnect(c, st, wid); err != nil {
			t.Fatal(err)
		}
		if len(edges()) != 0 {
			t.Fatalf("%+v connected: %+v", st, edges()[0])
		}
	}
	if !threadHas(s, p, "@Kit has no agent running") || !threadHas(s, p, "Shells has no agent running") {
		t.Fatal("no reason given")
	}
	if err := s.execConnect(c, Step{Op: "connect", Source: "n_note000001", Tiles: []string{na}}, wid); err != nil {
		t.Fatal(err)
	}
	if len(edges()) != 1 || edges()[0].Kind != EdgeWire {
		t.Fatalf("edges %+v", edges())
	}
	if ds := h.deliveriesTo(a.ID); len(ds) != 1 || !strings.Contains(ds[0].Text, "the context") {
		t.Fatalf("note not sent once %+v", ds)
	}
	// Connecting again changes and sends nothing.
	if err := s.execConnect(c, Step{Op: "connect", Source: "n_note000001", Tiles: []string{na}}, wid); err != nil {
		t.Fatal(err)
	}
	if len(h.deliveriesTo(a.ID)) != 1 {
		t.Fatal("sent twice")
	}
}

// TestOrderToShellSaysSo: no "Sent to" for what was never typed, and the reason in
// the language of the UI.
func TestOrderToShellSaysSo(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	sh := h.console(p, wid, "Kit", "shell", agentstate.Shell)
	c := &execCtx{threadID: p.ID, projectID: p.ID, workspaceID: wid}
	s.sendOrder(c, []string{sh.ID}, "do it", "order", 0)
	if threadHas(s, p, "Sent to") {
		t.Fatal(`says "Sent to"`)
	}
	if !threadHas(s, p, "@Kit: no agent is running in this console") {
		t.Fatalf("%+v", s.Threads.Last(p.ID, 10))
	}
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	s.sendOrder(c, []string{sh.ID, a.ID}, "do it", "order", 0)
	if !threadHas(s, p, "Sent to @Ash.") {
		t.Fatalf("%+v", s.Threads.Last(p.ID, 10))
	}
}

// TestContentWiresAndLines: what the hub takes as a wire, a preview and a log line.
func TestContentWiresAndLines(t *testing.T) {
	h, _ := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	na := h.node(wid, a.ID).ID
	for id, kind := range map[string]string{"n_note000001": "note", "n_note000002": "note", "n_web0000001": "web", "n_log0000001": "weblog", "n_app0000001": "app", "n_ink0000001": "ink"} {
		placeTile(t, h, wid, id, kind, map[string]any{})
	}
	put := func(id, kind, src, dst string) string {
		code, _ := codeOf(h.ApplyOps(wid, []board.Op{{Op: "putEdge", Edge: &board.Edge{ID: id, Kind: kind, Source: src, Target: dst, Data: json.RawMessage(`{}`)}}}))
		return code
	}
	for _, c := range []struct{ id, kind, src, dst, code string }{
		{"e_0000000001", EdgeWire, "n_note000001", na, ""},
		{"e_0000000002", EdgeWire, "n_web0000001", na, ""},
		{"e_0000000003", EdgeWire, "n_log0000001", na, ""},
		{"e_0000000004", EdgePreview, na, "n_web0000001", ""},
		{"e_0000000005", EdgeLog, "n_web0000001", "n_log0000001", ""},
		{"e_0000000006", EdgeWire, "n_note000001", "n_note000002", "bad_edge"},
		{"e_0000000007", EdgeWire, "n_app0000001", na, "bad_edge"},
		{"e_0000000008", EdgeWire, "n_ink0000001", na, "bad_edge"},
		{"e_0000000009", EdgePreview, "n_note000001", "n_web0000001", "bad_edge"},
		{"e_0000000010", EdgeLog, "n_note000001", "n_log0000001", "bad_edge"},
	} {
		if code := put(c.id, c.kind, c.src, c.dst); code != c.code {
			t.Fatalf("%s %s→%s: %q, want %q", c.kind, c.src, c.dst, code, c.code)
		}
	}
}

// TestLayoutKeepsTheEditor: a saved layout has the editor and its wires, and deploying
// it on a board that has an editor wires that one instead of adding a second.
func TestLayoutKeepsTheEditor(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	na := h.node(wid, a.ID).ID
	placeTile(t, h, wid, "n_edit000001", KindEditor, map[string]any{"open": []any{"secret.go"}})
	if err := h.ApplyOps(wid, []board.Op{wireOp("e_wire000005", "n_edit000001", na)}); err != nil {
		t.Fatal(err)
	}
	b, _ := h.Board(wid)
	l := LayoutFromBoard(b, nil, map[string]session.Info{a.ID: a})
	l.Name = "With editor"
	if len(l.Wires) != 1 || l.Wires[0].Kind != EdgeWire {
		t.Fatalf("wires %+v", l.Wires)
	}
	for _, n := range l.Nodes {
		if n.Kind == KindEditor && len(n.Data) != 0 {
			t.Fatalf("editor keeps %+v", n.Data)
		}
	}
	saved, err := s.Layouts.Add(l, t0)
	if err != nil {
		t.Fatal(err)
	}
	// On the same board: no second editor, one more wire from the one it has.
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: saved.ID}); err != nil {
		t.Fatal(err)
	}
	b, _ = h.Board(wid)
	editors, wires := 0, 0
	for _, n := range b.Nodes {
		if n.Kind == KindEditor {
			editors++
		}
	}
	for _, e := range b.Edges {
		if e.Kind == EdgeWire && e.Source == "n_edit000001" {
			wires++
		}
	}
	if editors != 1 || wires != 2 {
		t.Fatalf("editors %d, wires %d", editors, wires)
	}
	// The one it has moves into the layout's place, open files and all: left where it was, the
	// layout looked as if it had none.
	var ed *board.Node
	for _, n := range b.Nodes {
		if n.Kind == KindEditor {
			ed = n
		}
	}
	if ed.X < 400 || !strings.Contains(string(ed.Data), "secret.go") {
		t.Fatalf("editor not moved into the layout, or its files lost: %+v %s", ed, ed.Data)
	}
	// A locked one stays where the owner put it.
	locked := *ed
	locked.X, locked.Y, locked.Locked = 10, 10, true
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: &locked}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: saved.ID}); err != nil {
		t.Fatal(err)
	}
	b, _ = h.Board(wid)
	for _, n := range b.Nodes {
		if n.Kind == KindEditor && (n.X != 10 || n.Y != 10) {
			t.Fatalf("a locked editor moved to %v,%v", n.X, n.Y)
		}
	}
	// On an empty board it brings the editor along.
	ws2, err := h.CreateWorkspace(p.ID, "Two")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deploy(ws2.ID, DeployRequest{LayoutID: saved.ID}); err != nil {
		t.Fatal(err)
	}
	b, _ = h.Board(ws2.ID)
	editors, wires = 0, 0
	for _, n := range b.Nodes {
		if n.Kind == KindEditor {
			editors++
		}
	}
	for _, e := range b.Edges {
		if e.Kind == EdgeWire {
			wires++
		}
	}
	if editors != 1 || wires != 1 {
		t.Fatalf("new board: editors %d, wires %d", editors, wires)
	}
}

func writeMemory(s *Service, pid string) error {
	_, err := s.Memory.Write("project", pid, "- facts", "")
	return err
}
