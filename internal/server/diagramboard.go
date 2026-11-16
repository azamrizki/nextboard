package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/studio"
)

// Diagrams on the board without the Diagrams panel (docs/API-studio.md §6): the lead's diagram
// step (API-orchestration §3.6) and an agent's board_diagram tool. Both keep a record like the
// studio's, so the panel lists it and exports it, and land it on a board by themselves.

var (
	errDiagramNotReady = errors.New("diagram not finished yet")
	errLandWorkspace   = errors.New("workspace not found in the project of this diagram")
)

// diagramGap is the room left between a diagram and the tiles around it.
const diagramGap = 80

// maxDiagramArg caps board_diagram's arguments: 40 nodes and 80 edges never come near it.
const maxDiagramArg = 256 << 10

// Hidden diagram runs that the lead and agents start run on the owner's account and wait in the
// runner queue with the lead's own turns, so they are bounded: one per console at a time, and per
// project two at a time and ten an hour, the lead's and the agents' together. The panel's runs,
// which the owner starts, are not counted.
const (
	maxDiagramRunsAtOnce  = 2
	maxDiagramRunsPerHour = 10
)

// diagramRuns counts the hidden diagram runs of the lead and the agents (studioHub.drawing).
type diagramRuns struct {
	mu      sync.Mutex
	console map[string]bool        // a console's run is drawing
	running map[string]int         // by project
	started map[string][]time.Time // by project: the starts of the last hour
}

// diagramLimitError is a run the limits refuse; it says which and when to ask again.
type diagramLimitError struct{ msg string }

func (e *diagramLimitError) Error() string { return e.msg }

// take reserves a run for project pid and console sid ("" for the lead). release gives it back:
// ran when the run ended, !ran when it could not start (then it does not count for the hour).
func (l *diagramRuns) take(pid, sid string, now time.Time) (release func(ran bool), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.console == nil {
		l.console, l.running, l.started = map[string]bool{}, map[string]int{}, map[string][]time.Time{}
	}
	var recent []time.Time
	for _, at := range l.started[pid] {
		if now.Sub(at) < time.Hour {
			recent = append(recent, at)
		}
	}
	l.started[pid] = recent
	switch {
	case sid != "" && l.console[sid]:
		return nil, &diagramLimitError{"you have a diagram being drawn already, which goes on the board by itself when it is ready: ask for the next one after that"}
	case l.running[pid] >= maxDiagramRunsAtOnce:
		return nil, &diagramLimitError{fmt.Sprintf("%d diagrams of this project are being drawn already, the most at once: ask again when one of them is on the board", maxDiagramRunsAtOnce)}
	case len(recent) >= maxDiagramRunsPerHour:
		wait := int(math.Ceil(recent[0].Add(time.Hour).Sub(now).Minutes()))
		return nil, &diagramLimitError{fmt.Sprintf("%d diagrams of this project were drawn in the last hour, the most there may be: the next one can start in %d minutes", maxDiagramRunsPerHour, max(wait, 1))}
	}
	if sid != "" {
		l.console[sid] = true
	}
	l.running[pid]++
	l.started[pid] = append(recent, now)
	var once sync.Once
	return func(ran bool) {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if sid != "" {
				delete(l.console, sid)
			}
			if l.running[pid]--; l.running[pid] <= 0 {
				delete(l.running, pid)
			}
			if i := slices.IndexFunc(l.started[pid], func(at time.Time) bool { return at.Equal(now) }); !ran && i >= 0 {
				l.started[pid] = slices.Delete(l.started[pid], i, i+1)
			}
		})
	}, nil
}

// diagramSpotOK checks a spot asked for before a run starts: it must be on the board.
func diagramSpotOK(x, y *float64) error {
	if x != nil && y != nil && (math.Abs(*x) > board.MaxCoord || math.Abs(*y) > board.MaxCoord) {
		return fmt.Errorf("x and y must be within ±%d, the board's edge", board.MaxCoord)
	}
	return nil
}

// landDiagram puts a finished diagram on the board wid in one ops batch: its top left at at;
// else beside the tile of the console sid (diagramSpot); else right of everything on the board.
// style "" is the record's. It returns the board's rev, the new nodes' ids, and the node of the
// diagram's first box.
func (s *Server) landDiagram(d studio.DiagramRecord, wid string, at *[2]float64, sid, style string, look studio.Look) (int64, []string, string, error) {
	if d.Diagram == nil {
		return 0, nil, "", errDiagramNotReady
	}
	ws, pr, ok := s.projects.Workspace(wid)
	if !ok || pr.ID != d.ProjectID {
		return 0, nil, "", errLandWorkspace
	}
	if style != "ink" && style != "color" {
		style = d.Style
	}
	ops := studio.Land(d.Diagram, style, d.ID, 0, 0, look)
	w, h := 0.0, 0.0
	for _, op := range ops {
		w, h = math.Max(w, op.Node.X+op.Node.W), math.Max(h, op.Node.Y+op.Node.H)
	}
	x, y := 0.0, 0.0
	if at != nil {
		// moved in where it would cross the board's edge
		x = math.Max(math.Min(at[0], board.MaxCoord-w), -board.MaxCoord)
		y = math.Max(math.Min(at[1], board.MaxCoord-h), -board.MaxCoord)
	} else if b, err := s.boards.Get(ws.ID); err == nil {
		near := ""
		if in, ok := s.sessions.Info(sid); ok && sid != "" {
			near = diagramAnchor(b, in)
		}
		x, y = diagramSpot(b, near, w, h)
	}
	dx, dy := math.Round(x), math.Round(y)
	first := ""
	ids := make([]string, 0, len(ops))
	for _, op := range ops {
		op.Node.X += dx
		op.Node.Y += dy
		ids = append(ids, op.Node.ID)
		var key struct {
			DiagramKey string `json:"diagramKey"`
		}
		if first == "" && json.Unmarshal(op.Node.Data, &key) == nil && key.DiagramKey == "node:"+d.Diagram.Nodes[0].ID {
			first = op.Node.ID
		}
	}
	rev, err := s.boards.Apply(ws.ID, "", ops)
	if err != nil {
		return 0, nil, "", err
	}
	return rev, ids, first, nil
}

// diagramSpot is the top left for a w×h diagram on b: beside the tile near, on the first side
// (right, below, left, above) where it covers no other tile; else right of everything.
func diagramSpot(b board.Board, near string, w, h float64) (float64, float64) {
	free := func(x, y float64) bool {
		if math.Abs(x) > board.MaxCoord || math.Abs(y) > board.MaxCoord || x+w > board.MaxCoord || y+h > board.MaxCoord {
			return false
		}
		for _, n := range b.Nodes {
			if x < n.X+n.W+diagramGap/2 && n.X < x+w+diagramGap/2 && y < n.Y+n.H+diagramGap/2 && n.Y < y+h+diagramGap/2 {
				return false
			}
		}
		return true
	}
	if i := slices.IndexFunc(b.Nodes, func(n *board.Node) bool { return n.ID == near }); near != "" && i >= 0 {
		a := b.Nodes[i]
		for _, p := range [][2]float64{{a.X + a.W + diagramGap, a.Y}, {a.X, a.Y + a.H + diagramGap}, {a.X - diagramGap - w, a.Y}, {a.X, a.Y - diagramGap - h}} {
			if free(p[0], p[1]) {
				return p[0], p[1]
			}
		}
	}
	x, y := 0.0, 0.0
	for i, n := range b.Nodes {
		if i == 0 || n.X+n.W+diagramGap > x {
			x = n.X + n.W + diagramGap
		}
		if i == 0 || n.Y < y {
			y = n.Y
		}
	}
	return math.Max(math.Min(x, board.MaxCoord-w), -board.MaxCoord), y
}

// diagramAnchor is the tile a console's diagram goes beside: its own tile, or for an agent in the
// Code workbench the editor card it was opened in ("" when the board has neither).
func diagramAnchor(b board.Board, in session.Info) string {
	if !in.Panel {
		for _, n := range b.Nodes {
			if n.SessionID() == in.ID {
				return n.ID
			}
		}
		return ""
	}
	var cards []*board.Node
	for _, n := range b.Nodes {
		if n.Kind != orch.KindEditor {
			continue
		}
		if n.ID == in.Card {
			return n.ID
		}
		cards = append(cards, n)
	}
	// opened before cards were recorded: the card of its placement (data.part, none at the root)
	for _, n := range cards {
		var d struct {
			Part string `json:"part"`
		}
		if json.Unmarshal(n.Data, &d) == nil && d.Part == in.PartID {
			return n.ID
		}
	}
	if len(cards) > 0 {
		return cards[0].ID
	}
	return ""
}

// generateDiagram starts the Diagrams studio's run for a project, read at pl as the panel reads
// (the root: every part, its runner picked on This PC), for the console sid ("" for the lead),
// within the limits of diagramRuns, and calls then with the record when the run ends. No subject:
// custom with an ask, else the architecture; a subject with an ask keeps both in the prompt.
func (s *Server) generateDiagram(pid, sid string, pl project.Place, subject, ask, style string, then func(studio.DiagramRecord)) (studio.DiagramRecord, error) {
	pr, ok := s.projects.Get(pid)
	if !ok {
		return studio.DiagramRecord{}, project.ErrNotFound
	}
	root, pt, _, err := pr.ResolvePlace(pl)
	if err != nil {
		return studio.DiagramRecord{}, err
	}
	if !root && pt.ID != "" {
		pr.MachineID, pr.Path = pt.MachineID, pt.Path
	}
	seen, atRoot, _, names := readPlace(pr, root)
	t, err := s.runnerFor(seen, "", "")
	if err != nil {
		return studio.DiagramRecord{}, err
	}
	if ask = strings.TrimSpace(ask); subject == "" {
		subject = "custom"
		if ask == "" {
			subject = "architecture"
		}
	}
	release, err := s.studio.drawing.take(pr.ID, sid, time.Now())
	if err != nil {
		return studio.DiagramRecord{}, err
	}
	rec, _, err := s.studio.diagrams.Generate(pr.ID, studio.DiagramRequest{Subject: subject, Ask: ask, Effort: "balanced", Style: style,
		Agent: t.agent, MachineID: pr.MachineID, Cwd: pr.Path, Command: t.command, Env: t.env, Root: atRoot, Parts: names,
		Then: func(rec studio.DiagramRecord) {
			release(true)
			if then != nil {
				then(rec)
			}
		}})
	if err != nil {
		release(false)
		return studio.DiagramRecord{}, err
	}
	// the panel lists it as drawing from now on, wherever it was asked
	s.events.Publish(map[string]any{"type": "diagram", "projectId": pr.ID, "diagramId": rec.ID, "state": rec.State})
	return rec, nil
}

// runFailure says why a diagram run left nothing to land.
func runFailure(rec studio.DiagramRecord) string {
	switch {
	case rec.State == "cancelled":
		return "the run was stopped"
	case rec.Error != "":
		return rec.Error
	}
	return "the run failed"
}

var _ orch.DiagramMaker = hubAdapter{}

// GenerateDiagram is the lead's diagram step: read where the step says (the root, every part,
// unless it names a part), landed on the board wid at x/y or a free spot once it is drawn.
func (h hubAdapter) GenerateDiagram(pid, wid string, req orch.DiagramOrder) (string, error) {
	s := h.s
	ws, pr, ok := s.projects.Workspace(wid)
	if !ok || pr.ID != pid {
		return "", errLandWorkspace
	}
	if err := diagramSpotOK(req.X, req.Y); err != nil {
		return "", err
	}
	var at *[2]float64
	if req.X != nil && req.Y != nil {
		at = &[2]float64{*req.X, *req.Y}
	}
	pl := project.Place{Part: req.Part, Root: req.Root}
	if pl.IsZero() {
		pl.Root = true
	}
	rec, err := s.generateDiagram(pid, "", pl, req.Subject, req.Ask, req.Style, func(rec studio.DiagramRecord) {
		out := orch.DiagramLanded{DiagramID: rec.ID, Title: rec.Title, WorkspaceID: ws.ID, Board: ws.Name}
		if now, _, ok := s.projects.Workspace(ws.ID); ok {
			out.Board = now.Name
		}
		if rec.State != "done" {
			out.Err = runFailure(rec)
		} else if _, _, first, err := s.landDiagram(rec, ws.ID, at, "", "", studio.Look{}); err != nil {
			out.Err = "it is drawn, but could not go on the board: " + err.Error()
		} else {
			out.FirstNode = first
		}
		if req.Done != nil {
			req.Done(out)
		}
	})
	return rec.ID, err
}

// ---- the agents' tool ----

// diagramArgSchema is nb.diagram/v1 as a tool argument: what an agent must give, the rest optional.
var diagramArgSchema = json.RawMessage(`{"type":"object","description":"nb.diagram/v1","required":["title","nodes"],"properties":{` +
	`"title":{"type":"string","maxLength":80},"direction":{"type":"string","enum":["TB","LR"]},` +
	`"nodes":{"type":"array","minItems":1,"maxItems":40,"items":{"type":"object","required":["id","label"],"properties":{` +
	`"id":{"type":"string","pattern":"^[a-z0-9_-]{1,32}$"},"label":{"type":"string","maxLength":48},"note":{"type":"string","maxLength":80},` +
	`"shape":{"type":"string","enum":["box","round","ellipse","diamond"]},"group":{"type":"string"}}}},` +
	`"edges":{"type":"array","maxItems":80,"items":{"type":"object","required":["from","to"],"properties":{` +
	`"from":{"type":"string"},"to":{"type":"string"},"label":{"type":"string","maxLength":48},"style":{"type":"string","enum":["solid","dashed"]}}}},` +
	`"groups":{"type":"array","maxItems":8,"items":{"type":"object","required":["id","label"],"properties":{` +
	`"id":{"type":"string","pattern":"^[a-z0-9_-]{1,32}$"},"label":{"type":"string","maxLength":48}}}}}}`)

// boardToolList: an agent draws on its board (docs/API-agents.md §21). Not approved without
// asking: it changes the board.
var boardToolList = []map[string]any{
	{"name": "board_diagram", "description": "Draw a diagram on the board, next to your tile, when the owner or the lead asks for a diagram of what you know: how a part works, a flow, the architecture. " +
		"Write it yourself in diagram (nb.diagram/v1): title (at most 80 characters); direction TB (top to bottom) or LR (left to right); " +
		"nodes, at most 40, each {id matching ^[a-z0-9_-]{1,32}$, label at most 48 characters, note (optional) at most 80, shape box|round|ellipse|diamond, group (optional) a group's id}; " +
		"edges, at most 80, each {from and to: node ids, label (optional) at most 48, style solid|dashed}; groups, at most 8, each {id, label}. " +
		"Use diamond for a decision and label every edge leaving it with its answer; keep labels short and put detail in note. Example: " +
		`{"title":"Login","direction":"LR","nodes":[{"id":"web","label":"Web app","shape":"round"},{"id":"api","label":"API","note":"Go","shape":"box"}],"edges":[{"from":"web","to":"api","label":"POST /login"}]}. ` +
		"The hub checks it, lands it as shapes anyone can edit, and keeps it in the project's Diagrams list (PNG and Excalidraw export). " +
		"If you would rather not write it, leave diagram out and give ask instead: the hub then reads the project in a hidden run and draws it by itself, which takes a minute or two.",
		"inputSchema": toolSchema([]string{}, map[string]any{
			"diagram": diagramArgSchema,
			"ask":     map[string]any{"type": "string", "description": "Without diagram: what the hub should draw, e.g. \"how an order travels from the cart to the database\". At most 2000 characters."},
			"subject": map[string]any{"type": "string", "enum": []string{"architecture", "userFlow", "dataFlow", "components", "requestLifecycle", "stateFlow", "custom"},
				"description": "Without diagram: a ready subject instead of ask."},
			"style": map[string]any{"type": "string", "enum": []string{"ink", "color"}, "description": "ink (default): one ink; color: a color per group."},
			"x":     map[string]any{"type": "number", "description": "The diagram's left on the board; leave x and y out to put it next to your tile."},
			"y":     map[string]any{"type": "number", "description": "The diagram's top on the board."}})},
}

// boardDiagramTool is board_diagram for the console of t.
func (s *Server) boardDiagramTool(t remoteTarget, raw json.RawMessage) (string, bool) {
	in, ok := s.sessions.Info(t.sessionID)
	if t.sessionID == "" || t.projectID == "" || !ok || in.WorkspaceID == "" {
		return "Diagrams are only for a console on a project's board.", true
	}
	if len(raw) > maxDiagramArg {
		return fmt.Sprintf("The arguments are larger than %d KB. A diagram has at most 40 nodes and 80 edges with short labels: draw less, or split it in two.", maxDiagramArg>>10), true
	}
	var a struct {
		Diagram json.RawMessage `json:"diagram"`
		Ask     string          `json:"ask"`
		Subject string          `json:"subject"`
		Style   string          `json:"style"`
		X       *float64        `json:"x"`
		Y       *float64        `json:"y"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return fmt.Sprintf("invalid arguments: %v", err), true
		}
	}
	if err := diagramSpotOK(a.X, a.Y); err != nil {
		return err.Error() + ". Leave them out to put it next to your tile.", true
	}
	var at *[2]float64
	if a.X != nil && a.Y != nil {
		at = &[2]float64{*a.X, *a.Y}
	}
	where := "next to your tile"
	if at != nil {
		where = fmt.Sprintf("at %.0f, %.0f", *a.X, *a.Y)
	}
	boardName := ""
	if ws, _, ok := s.projects.Workspace(in.WorkspaceID); ok {
		boardName = ws.Name
	}
	d := bytes.TrimSpace(a.Diagram)
	if len(d) == 0 || string(d) == "null" {
		if strings.TrimSpace(a.Ask) == "" && a.Subject == "" {
			return "Give diagram (the nb.diagram/v1 object you wrote), or ask (what the hub should draw for you).", true
		}
		// read where the console works: its part, else the root (every part)
		pl := project.Place{Root: true}
		if in.PartID != "" {
			pl = project.Place{Part: in.PartID}
		}
		sid, wid, pid, handle := in.ID, in.WorkspaceID, t.projectID, in.Handle
		_, err := s.generateDiagram(pid, sid, pl, a.Subject, a.Ask, a.Style, func(rec studio.DiagramRecord) {
			// the agent was told it lands by itself: when it does not, the owner hears why
			problem := ""
			if rec.State != "done" {
				problem = fmt.Sprintf("The diagram @%s asked for could not be drawn: %s.", handle, runFailure(rec))
			} else if _, _, _, err := s.landDiagram(rec, wid, at, sid, "", studio.Look{}); err != nil {
				problem = fmt.Sprintf("The diagram \"%s\" @%s asked for is drawn and in the Diagrams list, but could not go on the board: %v.", rec.Title, handle, err)
			}
			if problem != "" && s.orch != nil {
				s.orch.Threads.Add(orch.ThreadMessage{ThreadID: pid, Role: "hub", Kind: "error", SessionID: sid, Handle: handle, Text: problem})
			}
		})
		var limit *diagramLimitError
		if errors.As(err, &limit) {
			return "Not started: " + limit.msg + ". Or write the diagram yourself and pass it as diagram, which needs no run.", true
		}
		if err != nil {
			return "Could not start the diagram: " + err.Error(), true
		}
		return fmt.Sprintf("Drawing it in a hidden run, which takes a minute or two. It goes on the board %s %s by itself when it is ready, "+
			"and into the project's Diagrams list. You do not need to wait for it.", boardName, where), false
	}
	// a CLI may send the object as a string of JSON
	var text string
	if json.Unmarshal(d, &text) == nil {
		if len(text) > maxDiagramArg {
			return fmt.Sprintf("The diagram is larger than %d KB.", maxDiagramArg>>10), true
		}
		d = []byte(text)
	}
	rec, err := s.studio.diagrams.Drawn(t.projectID, d, a.Style, in.Handle)
	if err != nil {
		return err.Error() + ". Fix it and call board_diagram again.", true
	}
	_, ids, _, err := s.landDiagram(rec, in.WorkspaceID, at, in.ID, "", studio.Look{})
	if err != nil {
		return fmt.Sprintf("The diagram is kept in the project's Diagrams list (%s), but could not go on the board: %v.", rec.ID, err), true
	}
	out := fmt.Sprintf("Drew \"%s\" on the board %s %s: %d boxes and %d arrows, %d tiles in all. It is in the project's Diagrams list as %s.",
		rec.Title, boardName, where, len(rec.Diagram.Nodes), len(rec.Diagram.Edges), len(ids), rec.ID)
	if len(rec.Warnings) > 0 {
		out += " Note: " + strings.Join(rec.Warnings, "; ") + "."
	}
	return out, false
}
