package orch

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// drawHub is the fake hub that can draw: it keeps what the lead asked for and finishes the run
// when the test says.
type drawHub struct {
	*fakeHub
	dmu  sync.Mutex
	asks []drawAsk
	err  error
}

type drawAsk struct {
	pid, wid string
	req      DiagramOrder
}

func (h *drawHub) GenerateDiagram(pid, wid string, req DiagramOrder) (string, error) {
	h.dmu.Lock()
	defer h.dmu.Unlock()
	if h.err != nil {
		return "", h.err
	}
	h.asks = append(h.asks, drawAsk{pid, wid, req})
	return fmt.Sprintf("dg_test%05d", len(h.asks)), nil
}

func newDrawHub(t *testing.T) (*drawHub, *Service) {
	h, s := newFakeHub(t)
	d := &drawHub{fakeHub: h}
	s.hub = d
	return d, s
}

// planAs runs an order through the planner for a client, with this plan as the answer.
func planAs(t *testing.T, s *Service, wid, tid, client string, steps ...map[string]any) {
	t.Helper()
	if _, err := s.Command(CommandRequest{Text: "draw this for me please", WorkspaceID: wid, ClientID: client}); err != nil {
		t.Fatal(err)
	}
	list := make([]any, len(steps))
	for i, x := range steps {
		list[i] = x
	}
	s.plannerDone(tid, doneJob("", map[string]any{"reply": "", "steps": list}))
}

// TestLeadDrawsADiagram: the lead's diagram step asks the hub for the studio's run at once,
// without asking the owner, on the board it names; when the run is done the thread says where
// the diagram is and the client that asked focuses its first box.
func TestLeadDrawsADiagram(t *testing.T) {
	h, s := newDrawHub(t)
	h.set.Lead.TalkFirst = true // the lead's own rules: a diagram is not something to agree on
	p, wid := h.project("shop")
	ws2, err := s.hub.CreateWorkspace(p.ID, "Design")
	if err != nil {
		t.Fatal(err)
	}
	x, y := 400.0, -120.0
	planAs(t, s, wid, p.ID, "c_client01", map[string]any{"op": "diagram", "text": "how an order travels from the cart to the database",
		"workspace": "design", "part": "api", "style": "color", "x": x, "y": y})
	if len(h.asks) != 1 {
		t.Fatalf("asks %+v, notes %v, errors %v", h.asks, threadTexts(s, p.ID, "note"), threadTexts(s, p.ID, "error"))
	}
	a := h.asks[0]
	if a.pid != p.ID || a.wid != ws2.ID || a.req.Ask != "how an order travels from the cart to the database" || a.req.Subject != "" ||
		a.req.Style != "color" || a.req.Part != "api" || a.req.Root || *a.req.X != x || *a.req.Y != y || a.req.Done == nil {
		t.Fatalf("asked %+v", a)
	}
	if cf, ok := s.Confirms.Pending(p.ID, s.Now()); ok {
		t.Fatalf("asked the owner: %+v", cf)
	}
	notes := strings.Join(threadTexts(s, p.ID, "note"), "\n")
	if !strings.Contains(notes, "Drawing a diagram of how an order travels from the cart to the database. It goes on Design when it is ready.") {
		t.Fatalf("notes %s", notes)
	}

	a.req.Done(DiagramLanded{DiagramID: "dg_test00001", Title: "Order flow", WorkspaceID: ws2.ID, Board: "Design", FirstNode: "n_box0000001"})
	if notes := threadTexts(s, p.ID, "note"); notes[len(notes)-1] != `The diagram "Order flow" is on Design.` {
		t.Fatalf("notes %v", notes)
	}
	var focus []UIAction
	h.fakeHub.mu.Lock()
	for _, ev := range h.events {
		if ev["type"] == "command" && ev["clientId"] == "c_client01" && ev["state"] == "done" {
			if u, _ := ev["uiActions"].([]UIAction); len(u) > 0 {
				focus = u
			}
		}
	}
	h.fakeHub.mu.Unlock()
	if len(focus) != 1 || focus[0] != (UIAction{Action: "focus", Target: "n_box0000001", WorkspaceID: ws2.ID}) {
		t.Fatalf("ui %+v", focus)
	}

	// a subject alone, at the root (every part) on the current board; the run fails
	planAs(t, s, wid, p.ID, "c_client01", map[string]any{"op": "diagram", "subject": "userFlow"})
	if len(h.asks) != 2 || h.asks[1].wid != wid || h.asks[1].req.Subject != "userFlow" || h.asks[1].req.Part != "" || h.asks[1].req.X != nil {
		t.Fatalf("asks %+v", h.asks)
	}
	if notes := threadTexts(s, p.ID, "note"); !strings.Contains(notes[len(notes)-1], "Drawing a diagram of the user flow. It goes on ") {
		t.Fatalf("notes %v", notes)
	}
	h.asks[1].req.Done(DiagramLanded{Err: "the CLI is not signed in"})
	if errs := threadTexts(s, p.ID, "error"); len(errs) == 0 || errs[len(errs)-1] != "The diagram could not be drawn: the CLI is not signed in" {
		t.Fatalf("errors %v", errs)
	}

	// what the hub refuses at once is the step's error
	h.err = fmt.Errorf("part not found")
	planAs(t, s, wid, p.ID, "", map[string]any{"op": "diagram", "text": "x", "part": "nowhere"})
	if errs := threadTexts(s, p.ID, "error"); errs[len(errs)-1] != "diagram: part not found" {
		t.Fatalf("errors %v", errs)
	}
}

// TestDiagramStepTranslation: an unknown board, no text or subject, or a style the studio does not
// have drops the step with a note.
func TestDiagramStepTranslation(t *testing.T) {
	if !json.Valid(PlanSchema) || !strings.Contains(string(PlanSchema), `"reply","diagram"]`) || !strings.Contains(string(PlanSchema), `"style":{"type":"string","enum":["ink","color"]}`) {
		t.Fatal("the planner's schema has no diagram step")
	}
	h, s := newDrawHub(t)
	p, wid := h.project("shop")
	for _, c := range []struct {
		step map[string]any
		note string
	}{
		{map[string]any{"op": "diagram", "text": "the api", "workspace": "Nowhere"}, "Planner named an unknown board; step dropped."},
		{map[string]any{"op": "diagram"}, "Planner step dropped: invalid step: diagram: text or subject is required"},
		{map[string]any{"op": "diagram", "text": "the api", "style": "neon"}, `Planner step dropped: invalid step: diagram: style "neon"`},
		{map[string]any{"op": "diagram", "text": "the api", "x": 25000, "y": 0}, "Planner step dropped: invalid step: diagram: x and y within ±20000"},
	} {
		planAs(t, s, wid, p.ID, "", c.step)
		if notes := threadTexts(s, p.ID, "note"); notes[len(notes)-1] != c.note {
			t.Fatalf("%v: notes %v", c.step, notes)
		}
	}
	if len(h.asks) != 0 {
		t.Fatalf("asked %+v", h.asks)
	}
	// a hub without the studio says so
	s.hub = h.fakeHub
	planAs(t, s, wid, p.ID, "", map[string]any{"op": "diagram", "text": "the api"})
	if errs := threadTexts(s, p.ID, "error"); len(errs) == 0 || errs[len(errs)-1] != "diagram: this hub cannot draw diagrams" {
		t.Fatalf("errors %v", errs)
	}
}
