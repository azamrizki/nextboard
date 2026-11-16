package orch

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/azamrizki/nextboard/internal/board"
)

// The lead's diagram step (docs/API-orchestration.md §3.6): the Diagrams studio's run, started by
// the hub for the project and landed on a board by itself when it is done. Only the hub knows the
// studio, so the step reaches it through DiagramMaker, which the hub's adapter implements.

// DiagramAsk is what a diagram step draws, besides its text (Step.Text) and its place.
type DiagramAsk struct {
	// Subject: one of the studio's subjects; "" is custom when the step has a text.
	Subject string `json:"subject,omitempty"`
	Style   string `json:"style,omitempty"` // ink | color; "" = ink
}

// DiagramOrder is a diagram the lead asks the hub for.
type DiagramOrder struct {
	Ask, Subject, Style string
	// Part: the part to read, by name or id; with neither it and Root, the root (every part).
	Part string
	Root bool
	// X, Y: the diagram's top left on the board; nil = a free spot.
	X, Y *float64
	// Done is called once, when the diagram is on the board or could not be drawn.
	Done func(DiagramLanded)
}

// DiagramLanded is how a diagram run ended.
type DiagramLanded struct {
	DiagramID, Title string
	WorkspaceID      string
	Board            string // the board's name
	FirstNode        string // the tile of the diagram's first box
	Err              string // why it is not on the board; "" when it is
}

// DiagramMaker starts a diagram for a project and lands it on the board wid when it is done; the
// diagram's id, or an error when it cannot start (a part the project lacks, no runner).
type DiagramMaker interface {
	GenerateDiagram(projectID, workspaceID string, req DiagramOrder) (string, error)
}

const maxDiagramAsk = 2000

// diagramSubjectWords name the studio's subjects in a sentence.
var diagramSubjectWords = map[string]string{"architecture": "the architecture", "userFlow": "the user flow", "dataFlow": "the data flow",
	"components": "the components", "requestLifecycle": "the request lifecycle", "stateFlow": "the state flow"}

// translateDiagramStep: the board by name (the current one without, or under an orchestrator
// card), the rest as the plan says; the hub checks the subject and the part.
func translateDiagramStep(sc *scope, ps plannerStep, st Step) (Step, string) {
	st.Text, st.Part, st.Root = strings.TrimSpace(ps.Text), ps.Part, ps.Root
	st.Diagram = &DiagramAsk{Subject: ps.Subject, Style: ps.Style}
	if ps.Workspace != "" && sc.card == nil {
		w, found := findWorkspace(sc.proj.Workspaces, ps.Workspace)
		if !found {
			return st, "Planner named an unknown board; step dropped."
		}
		st.WorkspaceID = w
	}
	if ps.X != nil && ps.Y != nil {
		st.Geom = &TileGeom{X: ps.X, Y: ps.Y}
	}
	return st, ""
}

func validateDiagramStep(s Step) error {
	if s.Diagram == nil || (strings.TrimSpace(s.Text) == "" && s.Diagram.Subject == "") {
		return badStep("diagram: text or subject is required")
	}
	if len([]rune(s.Text)) > maxDiagramAsk {
		return badStep("diagram: text at most %d characters", maxDiagramAsk)
	}
	if st := s.Diagram.Style; st != "" && st != "ink" && st != "color" {
		return badStep("diagram: style %q", st)
	}
	// a spot off the board would only fail once the run is done, with nobody waiting for it
	if g := s.Geom; g != nil && ((g.X != nil && math.Abs(*g.X) > board.MaxCoord) || (g.Y != nil && math.Abs(*g.Y) > board.MaxCoord)) {
		return badStep("diagram: x and y within ±%d", board.MaxCoord)
	}
	return nil
}

// diagramStep starts the run; the diagram lands later, when it is done (diagramLanded). It
// only reads the project and adds tiles, so it never waits for the owner.
func (s *Service) diagramStep(c *execCtx, st Step) error {
	dm, ok := s.hub.(DiagramMaker)
	if !ok {
		return errors.New("this hub cannot draw diagrams")
	}
	wid := firstNonEmpty(st.WorkspaceID, c.workspaceID)
	ws, _, found := s.hub.Workspace(wid)
	if !found {
		return ErrWorkspaceNotFound
	}
	req := DiagramOrder{Ask: st.Text, Subject: st.Diagram.Subject, Style: st.Diagram.Style, Part: st.Part, Root: st.Root}
	if g := st.Geom; g != nil && g.X != nil && g.Y != nil {
		req.X, req.Y = g.X, g.Y
	}
	tid, cmd, client := c.threadID, c.commandID, c.clientID
	req.Done = func(r DiagramLanded) { s.diagramLanded(tid, cmd, client, r) }
	if _, err := dm.GenerateDiagram(c.projectID, wid, req); err != nil {
		return err
	}
	what := clipText(st.Text, 80)
	if what == "" {
		what = firstNonEmpty(diagramSubjectWords[st.Diagram.Subject], st.Diagram.Subject)
	}
	s.note(c, fmt.Sprintf("Drawing a diagram of %s. It goes on %s when it is ready.", what, ws.Name))
	return nil
}

// diagramLanded tells the thread where the diagram is, or why it is not there.
func (s *Service) diagramLanded(threadID, commandID, clientID string, r DiagramLanded) {
	if r.Err != "" {
		s.Threads.Add(ThreadMessage{ThreadID: threadID, Role: "hub", Kind: "error", Text: "The diagram could not be drawn: " + r.Err})
		return
	}
	s.Threads.Add(ThreadMessage{ThreadID: threadID, Role: "hub", Kind: "note", Text: fmt.Sprintf("The diagram \"%s\" is on %s.", r.Title, r.Board)})
	// a result that comes later goes out like a planner's (§3.8): the client that sent the
	// command shows it
	s.hub.Publish(map[string]any{"type": "command", "commandId": commandID, "clientId": clientID, "state": "done",
		"uiActions": []UIAction{{Action: "focus", Target: r.FirstNode, WorkspaceID: r.WorkspaceID}}})
}
