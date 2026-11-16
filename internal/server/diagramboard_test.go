package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/orch"
)

// boardNode is a board node as the API gives it.
type boardNode struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	X, Y, W, H float64        `json:"-"`
	Data       map[string]any `json:"data"`
}

func (h *testHub) boardNodes(wid string) []boardNode {
	h.t.Helper()
	_, b := h.do("GET", "/api/workspaces/"+wid+"/board", nil, nil)
	var out []boardNode
	for _, x := range b["nodes"].([]any) {
		m := x.(map[string]any)
		n := boardNode{ID: m["id"].(string), Kind: m["kind"].(string), X: m["x"].(float64), Y: m["y"].(float64), W: m["w"].(float64), H: m["h"].(float64)}
		n.Data, _ = m["data"].(map[string]any)
		out = append(out, n)
	}
	return out
}

// diagramBox is the bounding box of a diagram's tiles on a board, and how many there are.
func diagramBox(nodes []boardNode, did string) (x0, y0, x1, y1 float64, n int) {
	for _, nd := range nodes {
		if nd.Data["diagramId"] != did {
			continue
		}
		if n == 0 || nd.X < x0 {
			x0 = nd.X
		}
		if n == 0 || nd.Y < y0 {
			y0 = nd.Y
		}
		if n == 0 || nd.X+nd.W > x1 {
			x1 = nd.X + nd.W
		}
		if n == 0 || nd.Y+nd.H > y1 {
			y1 = nd.Y + nd.H
		}
		n++
	}
	return
}

// diagramList is GET /api/projects/{id}/diagrams, newest first.
func (h *testHub) diagramList(pid string) []map[string]any {
	h.t.Helper()
	_, l := h.do("GET", "/api/projects/"+pid+"/diagrams", nil, nil)
	var out []map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(l["_raw"])), &out); err != nil {
		h.t.Fatalf("list %v: %v", l, err)
	}
	return out
}

func fakeDiagramCLI(t *testing.T, h *testHub) {
	t.Helper()
	fake := filepath.Join(t.TempDir(), "fake-claude")
	if err := os.WriteFile(fake, []byte(fakeStudioCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": fake}}}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, b)
	}
}

// TestAgentDrawsADiagram: board_diagram checks what the agent wrote and says what is wrong in
// words; a good one is kept as a diagram of the project (listed, by the console's handle) and
// lands beside the console's tile, the next one where that side is taken. Without a diagram the
// hub draws it in a hidden run and lands it by itself.
func TestAgentDrawsADiagram(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	bin := spyCodex(t, h)
	fakeDiagramCLI(t, h)
	pid, wid := h.studioProject(t.TempDir())
	sa := newSpyAgents(t, h, bin, wid)
	sid, tok, argv, _ := sa.start(map[string]any{})
	if ts := sa.tools(sid, tok); ts["board_diagram"] == nil {
		t.Fatalf("tools %v", ts)
	}
	if strings.Contains(argv, "board_diagram") {
		t.Fatalf("approved without asking: %s", argv)
	}
	var tile boardNode
	for _, n := range h.boardNodes(wid) {
		if n.Data["sessionId"] == sid {
			tile = n
		}
	}
	isErr := func(out string) bool { return strings.Contains(out, "isError:true") }

	if out := sa.call(sid, tok, "board_diagram", map[string]any{}); !isErr(out) || !strings.Contains(out, "Give diagram") {
		t.Fatalf("nothing %s", out)
	}
	bad := map[string]any{"title": "Checkout", "nodes": []any{map[string]any{"id": "web", "label": "Web"}}, "edges": []any{map[string]any{"from": "web", "to": "db"}}}
	if out := sa.call(sid, tok, "board_diagram", map[string]any{"diagram": bad}); !isErr(out) ||
		!strings.Contains(out, "invalid diagram: edge web→db points at a node that does not exist. Fix it and call board_diagram again.") {
		t.Fatalf("bad %s", out)
	}
	good := map[string]any{"title": "Checkout", "direction": "LR",
		"nodes": []any{map[string]any{"id": "web", "label": "Web app", "shape": "round"}, map[string]any{"id": "api", "label": "API", "note": "Go"},
			map[string]any{"id": "ok", "label": "Paid?", "shape": "diamond"}},
		"edges": []any{map[string]any{"from": "web", "to": "api", "label": "POST /pay"}, map[string]any{"from": "api", "to": "ok"}}}
	out := sa.call(sid, tok, "board_diagram", map[string]any{"diagram": good})
	if isErr(out) || !strings.Contains(out, `Drew "Checkout" on the board`) || !strings.Contains(out, "next to your tile: 3 boxes and 2 arrows, 7 tiles in all") ||
		!strings.Contains(out, "Diagrams list as dg_") || strings.Contains(out, "Note:") {
		t.Fatalf("good %s", out)
	}
	in, _ := h.srv.sessions.Info(sid)
	recs := h.diagramList(pid)
	if len(recs) != 1 || recs[0]["title"] != "Checkout" || recs[0]["state"] != "done" || recs[0]["subject"] != "custom" || recs[0]["agent"] != in.Handle {
		t.Fatalf("list %v (handle %s)", recs, in.Handle)
	}
	did := recs[0]["id"].(string)
	if _, rec := h.do("GET", "/api/diagrams/"+did, nil, nil); rec["ask"] != "Checkout" || rec["style"] != "ink" || rec["diagram"] == nil {
		t.Fatalf("record %v", rec)
	}
	x0, y0, _, y1, n := diagramBox(h.boardNodes(wid), did)
	if n != 3+2+1+1 || x0 != tile.X+tile.W+diagramGap || y0 != tile.Y { // boxes, arrows, a label, the title
		t.Fatalf("%d tiles at %v,%v; the console is at %+v", n, x0, y0, tile)
	}

	// right of the tile is taken now: below it; a diagram sent as a string of JSON works too
	raw, _ := json.Marshal(map[string]any{"title": "Refunds", "nodes": []any{map[string]any{"id": "a", "label": "Ask"}, map[string]any{"id": "b", "label": "Pay back"}},
		"edges": []any{map[string]any{"from": "a", "to": "b", "style": "dashed"}}})
	if out := sa.call(sid, tok, "board_diagram", map[string]any{"diagram": string(raw), "style": "color"}); isErr(out) {
		t.Fatalf("string %s", out)
	}
	if recs = h.diagramList(pid); len(recs) != 2 || recs[0]["title"] != "Refunds" {
		t.Fatalf("list %v", recs)
	}
	bx0, by0, _, _, _ := diagramBox(h.boardNodes(wid), recs[0]["id"].(string))
	if bx0 != tile.X || by0 != tile.Y+tile.H+diagramGap {
		t.Fatalf("second at %v,%v (first ends at y %v); the console is at %+v", bx0, by0, y1, tile)
	}

	// at a spot it names
	if out := sa.call(sid, tok, "board_diagram", map[string]any{"diagram": good, "x": -3000, "y": 4000}); isErr(out) || !strings.Contains(out, "at -3000, 4000") {
		t.Fatalf("at %s", out)
	}
	recs = h.diagramList(pid)
	if cx, cy, _, _, _ := diagramBox(h.boardNodes(wid), recs[0]["id"].(string)); cx != -3000 || cy != 4000 {
		t.Fatalf("at %v,%v", cx, cy)
	}

	// without a diagram: a hidden run draws it, and it lands by itself
	if out := sa.call(sid, tok, "board_diagram", map[string]any{"ask": "how a payment travels"}); isErr(out) || !strings.Contains(out, "Drawing it in a hidden run") {
		t.Fatalf("ask %s", out)
	}
	var hidden string
	waitTrue(t, "the hidden run's diagram on the board", 20*time.Second, func() bool {
		if recs = h.diagramList(pid); len(recs) != 4 || recs[0]["state"] != "done" {
			return false
		}
		hidden = recs[0]["id"].(string)
		_, _, _, _, n := diagramBox(h.boardNodes(wid), hidden)
		return n > 0
	})
	if recs[0]["title"] != "Flow" || recs[0]["agent"] != "claude" {
		t.Fatalf("hidden %v", recs[0])
	}
	hx0, hy0, hx1, hy1, _ := diagramBox(h.boardNodes(wid), hidden)
	if hx0 < tile.X+tile.W && tile.X < hx1 && hy0 < tile.Y+tile.H && tile.Y < hy1 {
		t.Fatalf("over the console: %v,%v–%v,%v; the console is at %+v", hx0, hy0, hx1, hy1, tile)
	}
}

// TestLeadDiagramLands: the hub's side of the lead's diagram step. The studio's run reads the
// project, and once it is done the diagram is a record of the project and lands right of
// everything on the board, and the lead hears its title, board and first box.
func TestLeadDiagramLands(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	fakeDiagramCLI(t, h)
	pid, wid := h.studioProject(t.TempDir())
	if _, err := h.srv.boards.Apply(wid, "", []board.Op{{Op: "put", Node: &board.Node{ID: "n_note000001", Kind: "note", X: 100, Y: 50, W: 300, H: 200, Data: json.RawMessage(`{"text":"hi"}`)}}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan orch.DiagramLanded, 1)
	did, err := hubAdapter{h.srv}.GenerateDiagram(pid, wid, orch.DiagramOrder{Ask: "the checkout", Done: func(r orch.DiagramLanded) { done <- r }})
	if err != nil || !strings.HasPrefix(did, "dg_") {
		t.Fatalf("%q %v", did, err)
	}
	var r orch.DiagramLanded
	select {
	case r = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the diagram did not land")
	}
	nodes := h.boardNodes(wid)
	x0, y0, _, _, n := diagramBox(nodes, did)
	if r.Err != "" || r.DiagramID != did || r.Title != "Flow" || r.WorkspaceID != wid || r.Board == "" || n != 7 || x0 != 100+300+diagramGap || y0 != 50 {
		t.Fatalf("landed %+v: %d tiles at %v,%v", r, n, x0, y0)
	}
	for _, nd := range nodes {
		if nd.ID == r.FirstNode && nd.Data["diagramKey"] != "node:web" {
			t.Fatalf("first node %+v", nd)
		}
	}
	if _, rec := h.do("GET", "/api/diagrams/"+did, nil, nil); rec["subject"] != "custom" || rec["ask"] != "the checkout" || rec["state"] != "done" {
		t.Fatalf("record %v", rec)
	}
	// what cannot start says so at once
	if _, err := (hubAdapter{h.srv}).GenerateDiagram(pid, wid, orch.DiagramOrder{Ask: "x", Part: "nowhere"}); err == nil {
		t.Fatal("a part the project lacks")
	}
	if _, err := (hubAdapter{h.srv}).GenerateDiagram(pid, wid, orch.DiagramOrder{Subject: "weather"}); err == nil || !strings.Contains(err.Error(), "unknown diagram subject") {
		t.Fatalf("subject %v", err)
	}
}

// fakeLeadCLI is Claude Code for the lead and the studio: the lead plans one diagram step, the
// studio's run answers with the diagram of fakeStudioCLI.
const fakeLeadCLI = `#!/bin/sh
prompt=$(cat)
case "$prompt" in
*nb.diagram/v1*)
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"x","structured_output":{"schema":"nb.diagram/v1","title":"Flow","direction":"TB","nodes":[{"id":"web","label":"Web","note":"","shape":"round","group":""},{"id":"api","label":"API","note":"Go","shape":"box","group":""}],"edges":[{"from":"web","to":"api","label":"HTTPS","style":"solid"}],"groups":[]}}' ;;
*)
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"x","structured_output":{"reply":"","steps":[{"op":"diagram","text":"the checkout","x":3000,"y":200}]}}' ;;
esac
`

// TestLeadDiagramThroughThePlanner: the owner asks the lead (talk first, as by default) for a
// diagram; it is drawn and landed with no question to the owner, and the thread says where.
func TestLeadDiagramThroughThePlanner(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	fake := filepath.Join(t.TempDir(), "fake-claude")
	if err := os.WriteFile(fake, []byte(fakeLeadCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"commands": map[string]any{"planner": "auto"},
		"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": fake}}}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, b)
	}
	pid, wid := h.studioProject(t.TempDir())
	st, r := h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": "draw me how the checkout works", "workspaceId": wid, "clientId": "c_diagram01"}, nil)
	if st != 202 || r["confirmation"] != nil {
		t.Fatalf("command %d %v", st, r)
	}
	tid := r["threadId"].(string)
	var texts string
	waitTrue(t, "the thread says where the diagram is", 30*time.Second, func() bool {
		_, m := h.do("GET", "/api/threads/"+tid+"/messages", nil, nil)
		texts = fmt.Sprint(m)
		return strings.Contains(texts, `The diagram "Flow" is on`) || strings.Contains(texts, "could not be drawn")
	})
	if !strings.Contains(texts, "Drawing a diagram of the checkout.") || !strings.Contains(texts, `The diagram "Flow" is on`) || strings.Contains(texts, "Needs your OK") {
		t.Fatalf("thread %s", texts)
	}
	recs := h.diagramList(pid)
	if len(recs) != 1 {
		t.Fatalf("list %v", recs)
	}
	if x0, y0, _, _, n := diagramBox(h.boardNodes(wid), recs[0]["id"].(string)); n != 5 || x0 != 3000 || y0 != 200 { // 2 boxes, an arrow, its label, the title
		t.Fatalf("%d tiles at %v,%v", n, x0, y0)
	}
}

// TestDiagramRunCounts: one run per console at a time, two per project at once and ten an hour;
// a run that never started does not count for the hour.
func TestDiagramRunCounts(t *testing.T) {
	var l diagramRuns
	t0 := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	relA, err := l.take("p_shop0001", "s_cons000a", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.take("p_shop0001", "s_cons000a", t0); err == nil || !strings.Contains(err.Error(), "you have a diagram being drawn already") {
		t.Fatalf("same console %v", err)
	}
	relB, _ := l.take("p_shop0001", "s_cons000b", t0)
	if _, err := l.take("p_shop0001", "", t0); err == nil || !strings.Contains(err.Error(), "2 diagrams of this project are being drawn already") {
		t.Fatalf("at once %v", err)
	}
	relOther, err := l.take("p_blog0001", "s_cons000c", t0)
	if err != nil {
		t.Fatalf("another project %v", err)
	}
	relOther(true)
	relA(true)
	relB(false) // never started: not counted
	relB(true)  // once only
	for i := 1; i <= 9; i++ {
		rel, err := l.take("p_shop0001", "", t0.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		rel(true)
	}
	if _, err := l.take("p_shop0001", "s_cons000a", t0.Add(30*time.Minute)); err == nil ||
		err.Error() != "10 diagrams of this project were drawn in the last hour, the most there may be: the next one can start in 30 minutes" {
		t.Fatalf("an hour %v", err)
	}
	if rel, err := l.take("p_shop0001", "", t0.Add(61*time.Minute)); err != nil {
		t.Fatalf("an hour later %v", err)
	} else {
		rel(true)
	}
}

// TestDiagramRunLimits: the hub refuses hidden runs over the limits in words, the agents' and the
// lead's together, and refuses a spot off the board or an oversized diagram before anything
// starts; a run that fails tells the project's thread.
func TestDiagramRunLimits(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	bin := spyCodex(t, h)
	dir := t.TempDir()
	hold := filepath.Join(dir, "hold")
	fake := filepath.Join(dir, "fake-claude")
	script := strings.Replace(fakeStudioCLI, "prompt=$(cat)\n", "prompt=$(cat)\nwhile [ -e "+hold+" ]; do sleep 0.05; done\ncase \"$prompt\" in *explode*) echo boom >&2; exit 3 ;; esac\n", 1)
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": fake}}}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, b)
	}
	_ = os.WriteFile(hold, nil, 0o600)
	t.Cleanup(func() { os.Remove(hold) }) // before the hub stops
	pid, wid := h.studioProject(t.TempDir())
	sa := newSpyAgents(t, h, bin, wid)
	a, aTok, _, _ := sa.start(map[string]any{})
	b, bTok, _, _ := sa.start(map[string]any{})
	c, cTok, _, _ := sa.start(map[string]any{})
	isErr := func(out string) bool { return strings.Contains(out, "isError:true") }

	if out := sa.call(a, aTok, "board_diagram", map[string]any{"ask": "the checkout", "x": 25000, "y": 0}); !isErr(out) || !strings.Contains(out, "x and y must be within ±20000") {
		t.Fatalf("off the board %s", out)
	}
	if out := sa.call(a, aTok, "board_diagram", map[string]any{"diagram": map[string]any{"title": strings.Repeat("x", 300<<10)}}); !isErr(out) || !strings.Contains(out, "larger than 256 KB") {
		t.Fatalf("oversized %.300s", out)
	}
	if n := len(h.diagramList(pid)); n != 0 {
		t.Fatalf("%d runs started", n)
	}
	if out := sa.call(a, aTok, "board_diagram", map[string]any{"ask": "the checkout"}); isErr(out) {
		t.Fatalf("first %s", out)
	}
	if out := sa.call(a, aTok, "board_diagram", map[string]any{"ask": "the refunds"}); !isErr(out) ||
		!strings.Contains(out, "Not started: you have a diagram being drawn already") || !strings.Contains(out, "pass it as diagram") {
		t.Fatalf("same console %s", out)
	}
	if out := sa.call(b, bTok, "board_diagram", map[string]any{"subject": "dataFlow", "ask": "explode"}); isErr(out) {
		t.Fatalf("second %s", out)
	}
	if out := sa.call(c, cTok, "board_diagram", map[string]any{"ask": "the refunds"}); !isErr(out) || !strings.Contains(out, "Not started: 2 diagrams of this project are being drawn already") {
		t.Fatalf("third %s", out)
	}
	if _, err := (hubAdapter{h.srv}).GenerateDiagram(pid, wid, orch.DiagramOrder{Ask: "the refunds"}); err == nil || !strings.Contains(err.Error(), "2 diagrams of this project are being drawn already") {
		t.Fatalf("the lead %v", err)
	}
	// writing it yourself needs no run
	if out := sa.call(c, cTok, "board_diagram", map[string]any{"diagram": map[string]any{"title": "Mine", "nodes": []any{map[string]any{"id": "a", "label": "A"}}}}); isErr(out) {
		t.Fatalf("written %s", out)
	}

	os.Remove(hold)
	_, bInfo := h.do("GET", "/api/sessions/"+b, nil, nil)
	var thread string
	waitTrue(t, "both runs end", 20*time.Second, func() bool {
		done := 0
		for _, r := range h.diagramList(pid) {
			if r["state"] == "done" || r["state"] == "failed" {
				done++
			}
		}
		_, m := h.do("GET", "/api/threads/"+pid+"/messages", nil, nil)
		thread = fmt.Sprint(m)
		return done == 3 && strings.Contains(thread, "could not be drawn")
	})
	if !strings.Contains(thread, fmt.Sprintf("The diagram @%s asked for could not be drawn:", bInfo["handle"])) {
		t.Fatalf("thread %s", thread)
	}
	var subj map[string]any
	for _, r := range h.diagramList(pid) {
		if r["state"] == "failed" {
			subj = r
		}
	}
	if subj["subject"] != "dataFlow" {
		t.Fatalf("failed run %v", subj)
	}
	// the slots are free again
	if out := sa.call(c, cTok, "board_diagram", map[string]any{"ask": "the refunds"}); isErr(out) {
		t.Fatalf("after %s", out)
	}
}
