package orch

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/board"
)

// Board control (docs/API-orchestration.md §3.6): what lets the planner act on
// everything on a board, not only on consoles. Its input names every tile by id
// with its content; these steps change them through the same validated board ops
// as a client, so every open client sees the result at once.

// KindEditor is the Code workbench card.
const KindEditor = "editor"

const (
	maxTileContent = 700 // characters of a tile's data in the planner's input
	maxPlanSteps   = 40
)

// tileDataHidden is data that means nothing to a planner, or that is big.
var tileDataHidden = map[string]bool{"points": true, "strokes": true, "snapshot": true, "markup": true}

// tileDataProtected ties a tile to something real: a console, a preview, a file
// on the hub. Changing it by hand would orphan that.
var tileDataProtected = map[string]bool{"sessionId": true, "previewId": true, "assetId": true, "appId": true, "points": true, "strokes": true}

// tileBrief is one tile in the planner's input. Content too long to show whole is
// marked, because array fields can only be replaced as a whole.
func tileBrief(n *board.Node) map[string]any {
	t := map[string]any{"id": n.ID, "kind": n.Kind, "x": int(n.X), "y": int(n.Y), "w": int(n.W), "h": int(n.H)}
	if l := tileLabel(n); l != "" {
		t["label"] = l
	}
	if n.Locked {
		t["locked"] = true
	}
	var d map[string]any
	if json.Unmarshal(n.Data, &d) != nil {
		return t
	}
	for k := range d {
		if tileDataHidden[k] {
			delete(d, k)
		}
	}
	if len(d) == 0 {
		return t
	}
	if b, err := json.Marshal(d); err == nil {
		if len(b) <= maxTileContent {
			t["data"] = d
		} else {
			t["contentCut"] = clipText(string(b), maxTileContent)
		}
	}
	return t
}

// boardEdges lists a board's connections for the planner's input.
func boardEdges(b board.Board) []map[string]any {
	out := []map[string]any{}
	for _, e := range b.Edges {
		out = append(out, map[string]any{"source": e.Source, "target": e.Target, "kind": e.Kind})
	}
	return out
}

// eachNode visits the current board first, so that a name means what the user sees.
func (sc *scope) eachNode(fn func(n *board.Node, wid string) bool) {
	for _, n := range sc.b.Nodes {
		if fn(n, sc.ws.ID) {
			return
		}
	}
	for wid, b := range sc.others {
		for _, n := range b.Nodes {
			if fn(n, wid) {
				return
			}
		}
	}
}

// findTile resolves what a planner calls a tile: an id, "@handle", a group name, a
// kind when there is only one of it ("editor"), or a title. problem says why not.
func (sc *scope) findTile(ref string) (*board.Node, string, string) {
	want := strings.ToLower(strings.TrimSpace(ref))
	if want == "" {
		return nil, "", "Planner step named no tile; step dropped."
	}
	type hit struct {
		n   *board.Node
		wid string
	}
	first := func(match func(n *board.Node) bool) []hit {
		var out []hit
		sc.eachNode(func(n *board.Node, wid string) bool {
			if match(n) {
				out = append(out, hit{n, wid})
			}
			return false
		})
		return out
	}
	handle := strings.TrimPrefix(want, "@")
	rules := []func(n *board.Node) bool{
		func(n *board.Node) bool { return strings.ToLower(n.ID) == want },
		func(n *board.Node) bool {
			sid, ok := sc.sidOf[n.ID]
			return ok && strings.ToLower(sc.handle(sid)) == handle
		},
		func(n *board.Node) bool { return n.Kind == KindGroup && strings.ToLower(GroupOf(n).Name) == handle },
		func(n *board.Node) bool {
			return n.Kind == want || (n.Kind == KindEditor && (want == "code editor" || want == "code"))
		},
		func(n *board.Node) bool { return strings.ToLower(tileLabel(n)) == want },
		func(n *board.Node) bool { return strings.Contains(strings.ToLower(tileLabel(n)), want) },
	}
	for _, r := range rules {
		hits := first(r)
		if len(hits) == 0 {
			continue
		}
		// a board in view wins over the same name elsewhere
		onThis := hits[:0:0]
		for _, h := range hits {
			if h.wid == sc.ws.ID {
				onThis = append(onThis, h)
			}
		}
		if len(onThis) > 0 {
			hits = onThis
		}
		// a copy of the editor card only points to the first one, which is the live one
		if len(hits) > 1 && hits[0].n.Kind == KindEditor {
			hits = hits[:1]
		}
		if len(hits) > 1 {
			return nil, "", fmt.Sprintf("Planner named %q, which fits %d tiles; step dropped.", ref, len(hits))
		}
		return hits[0].n, hits[0].wid, ""
	}
	return nil, "", fmt.Sprintf("Planner named %q, which is not on any board; step dropped.", ref)
}

// planGeom turns coordinates of the plan into a step's geometry.
func planGeom(ps plannerStep) *TileGeom {
	if ps.X == nil && ps.Y == nil && ps.W == nil && ps.H == nil {
		return nil
	}
	return &TileGeom{X: ps.X, Y: ps.Y, W: ps.W, H: ps.H}
}

// planData is the data a step sets: "data" without protected keys, plus the
// shorthand fields the planner may use instead.
func planData(ps plannerStep, kind string) map[string]any {
	d := map[string]any{}
	for k, v := range ps.Data {
		if !tileDataProtected[k] {
			d[k] = v
		}
	}
	if ps.Text != "" {
		d["text"] = ps.Text
	}
	if ps.Color != "" {
		d["color"] = ps.Color
	}
	if ps.Title != "" {
		if kind == KindGroup || kind == "app" {
			d["name"] = ps.Title
		} else {
			d["title"] = ps.Title
		}
	}
	if ps.URL != "" {
		d["url"] = ps.URL
	}
	ensureItemIDs(d)
	return d
}

// ensureItemIDs gives list entries the ids clients key them by, however deep:
// checklist items, kanban columns and cards, calendar events.
func ensureItemIDs(v any) {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range []string{"items", "columns", "cards", "events"} {
			list, ok := x[k].([]any)
			if !ok {
				continue
			}
			for _, e := range list {
				if m, ok := e.(map[string]any); ok {
					if id, _ := m["id"].(string); id == "" {
						m["id"] = auth.RandomID("i_")
					}
					ensureItemIDs(m)
				}
			}
		}
	}
}

// translateBoardStep fills the step of a board control op, or says why it cannot.
func (s *Service) translateBoardStep(sc *scope, ps plannerStep, st Step) (Step, string) {
	switch ps.Op {
	case "node.update":
		n, wid, problem := sc.findTile(ps.Tile)
		if problem != "" {
			return st, problem
		}
		st.Tile, st.WorkspaceID, st.Geom = n.ID, wid, planGeom(ps)
		if n.Kind == board.KindTerminal {
			if len(ps.Data) > 0 || ps.Text != "" || ps.Title != "" {
				return st, "A console's content cannot be edited; use send, or console.rename for its name. Step dropped."
			}
		} else {
			st.Data = planData(ps, n.Kind)
		}
		if st.Geom == nil && len(st.Data) == 0 {
			return st, "Planner step changed nothing; step dropped."
		}
	case "node.delete":
		refs := ps.Tiles
		if ps.Tile != "" {
			refs = append([]string{ps.Tile}, refs...)
		}
		var labels []string
		for _, r := range refs {
			n, wid, problem := sc.findTile(r)
			if problem != "" {
				return st, problem
			}
			if n.Kind == board.KindTerminal {
				return st, "A console goes away with kill, not with a tile delete. Step dropped."
			}
			if st.WorkspaceID != "" && st.WorkspaceID != wid {
				return st, "One delete step reaches one board; step dropped."
			}
			st.WorkspaceID = wid
			st.Tiles = append(st.Tiles, n.ID)
			label := tileLabel(n)
			if label == "" {
				label = n.Kind
			}
			labels = append(labels, label)
		}
		st.Text = strings.Join(labels, ", ")
	case "connect", "disconnect":
		src, srcWs, problem := sc.findTile(ps.Source)
		if problem != "" {
			return st, problem
		}
		var targets []*board.Node
		for _, r := range ps.Targets {
			n, wid, problem := sc.findTile(r)
			if problem != "" {
				return st, problem
			}
			if wid != srcWs {
				return st, "Tiles on different boards cannot be connected; step dropped."
			}
			targets = append(targets, n)
		}
		if len(targets) == 0 {
			return st, "Planner connect step has no targets; step dropped."
		}
		// "connect Bea to the editor": the wire starts at the card, whichever way it was said
		if !isWireSource(src) && len(targets) == 1 && isWireSource(targets[0]) {
			src, targets = targets[0], []*board.Node{src}
		}
		// "connect the kanban to Nara": the lead keeps the board, not the other way round
		if isTaskTile(src) && len(targets) == 1 && targets[0].Kind == KindOrchestrator {
			src, targets = targets[0], []*board.Node{src}
		}
		st.WorkspaceID, st.Source = srcWs, src.ID
		for _, t := range targets {
			if ps.Op == "connect" && !isWireTarget(t, src) {
				return st, fmt.Sprintf("A %s cannot be connected to a %s; step dropped.", src.Kind, t.Kind)
			}
			st.Tiles = append(st.Tiles, t.ID)
		}
	case "ui":
		a := UIAction{Action: ps.Action, Panel: ps.Panel}
		if ps.Tile != "" {
			n, wid, problem := sc.findTile(ps.Tile)
			if problem != "" {
				return st, problem
			}
			a.Target, a.WorkspaceID = n.ID, wid
		}
		st.UIAction = &a
	case "editor.open":
		p := strings.TrimSpace(ps.Path)
		if p == "" {
			return st, "Planner editor.open has no path; step dropped."
		}
		if !strings.HasPrefix(p, "/") {
			p = path.Join(sc.proj.Path, p)
		}
		st.UIAction = &UIAction{Action: "openFile", Path: p, Line: ps.Line}
		st.Op = "ui"
	case "console.rename":
		r := sc.resolve(ps.Targets)
		if !r.ok() || len(r.targets) != 1 {
			return st, "Planner console.rename needs one known console; step dropped."
		}
		st.Targets, st.Name = r.targets, ps.Name
	case "group.dissolve":
		g := sc.groupByName(ps.Group)
		if g == nil {
			return st, "Planner named an unknown group; step dropped."
		}
		st.GroupID, st.WorkspaceID = g.ID, sc.wsOf[g.ID]
	case "workspace.create":
		st.Name = ps.Name
	case "workspace.rename", "workspace.go":
		st.WorkspaceID, st.Name = sc.ws.ID, ps.Name
		if ps.Workspace != "" {
			w, found := findWorkspace(sc.proj.Workspaces, ps.Workspace)
			if !found {
				return st, "Planner named an unknown board; step dropped."
			}
			st.WorkspaceID = w
		}
	}
	return st, ""
}

func isWireSource(n *board.Node) bool {
	return n.Kind == KindOrchestrator || n.Kind == KindMemory || n.Kind == KindEditor || contentTiles[n.Kind]
}

// isWireTarget says what a connection from src may end at. Between consoles and
// groups a connection is a forward; the editor also talks to orchestrator cards, and a
// memory tile reaches what those two direct.
func isWireTarget(t, src *board.Node) bool {
	switch t.Kind {
	case board.KindTerminal, KindGroup:
		return true
	case KindOrchestrator:
		return src.Kind == KindEditor || src.Kind == KindMemory
	case KindEditor:
		return src.Kind == KindMemory
	case "kanban", "checklist":
		return src.Kind == KindOrchestrator
	}
	return false
}

// edgeKindFor is the connection src → dst can be, "" when there is none. The board has
// the same rule in connectIntent (web/src/board/groups.ts).
func edgeKindFor(src, dst *board.Node) string {
	console := func(n *board.Node) bool { return n.Kind == board.KindTerminal || n.Kind == KindGroup }
	switch {
	case src.Kind == "web" && dst.Kind == "weblog":
		return EdgeLog
	case src.Kind == board.KindTerminal && dst.Kind == "web":
		return EdgePreview
	case isWireSource(src) && isWireTarget(dst, src):
		return EdgeWire
	case console(src) && console(dst) && src.ID != dst.ID:
		return EdgeForward
	}
	return ""
}

// noAgentIn says why a console or group cannot take part in a connection: only an
// agent has turns to forward and takes text. "" when it can. A group is fine with one
// agent among its members, and an empty one may get members later.
func (s *Service) noAgentIn(b board.Board, n *board.Node) string {
	switch n.Kind {
	case board.KindTerminal:
		if in, ok := s.hub.SessionInfo(n.SessionID()); ok && !takesText(in) {
			return "@" + in.Handle + " has no agent running"
		}
	case KindGroup:
		sids := nodeSessionIDs(b, n.ID)
		for _, sid := range sids {
			if in, ok := s.hub.SessionInfo(sid); !ok || takesText(in) {
				return ""
			}
		}
		if len(sids) > 0 {
			return GroupOf(n).Name + " has no agent running"
		}
	}
	return ""
}

// execBoardStep runs a board control step.
func (s *Service) execBoardStep(c *execCtx, st Step) error {
	wid := firstNonEmpty(st.WorkspaceID, c.workspaceID)
	switch st.Op {
	case "node.update":
		set := map[string]json.RawMessage{}
		if g := st.Geom; g != nil {
			for k, v := range map[string]*float64{"x": g.X, "y": g.Y, "w": g.W, "h": g.H} {
				if v != nil {
					b, _ := json.Marshal(*v)
					set[k] = b
				}
			}
		}
		if len(st.Data) > 0 {
			b, err := json.Marshal(st.Data)
			if err != nil {
				return err
			}
			set["data"] = b
		}
		if err := s.hub.ApplyOps(wid, []board.Op{{Op: "patch", ID: st.Tile, Set: set}}); err != nil {
			return err
		}
		s.note(c, "Updated a tile.")
	case "node.delete":
		ops := make([]board.Op, 0, len(st.Tiles))
		for _, id := range st.Tiles {
			ops = append(ops, board.Op{Op: "del", ID: id})
		}
		if err := s.hub.ApplyOps(wid, ops); err != nil {
			return err
		}
		s.note(c, fmt.Sprintf("Deleted %d tile(s): %s.", len(st.Tiles), st.Text))
	case "connect", "disconnect":
		return s.execConnect(c, st, wid)
	case "console.rename":
		if err := s.hub.RenameSession(st.Targets[0], st.Name); err != nil {
			return err
		}
		s.note(c, "Renamed the console to "+st.Name+".")
	}
	return nil
}

func (s *Service) execConnect(c *execCtx, st Step, wid string) error {
	b, err := s.hub.Board(wid)
	if err != nil {
		return err
	}
	byID := map[string]*board.Node{}
	for _, n := range b.Nodes {
		byID[n.ID] = n
	}
	src := byID[st.Source]
	if src == nil {
		return fmt.Errorf("tile %s sudah tidak ada", st.Source)
	}
	var ops []board.Op
	var wired []string
	for _, t := range st.Tiles {
		if st.Op == "disconnect" {
			for _, e := range b.Edges {
				// a disconnect removes the link in whichever direction it was made
				if (e.Source == st.Source && e.Target == t) || (e.Source == t && e.Target == st.Source) {
					ops = append(ops, board.Op{Op: "delEdge", ID: e.ID})
				}
			}
			continue
		}
		dst := byID[t]
		if dst == nil {
			continue
		}
		kind := edgeKindFor(src, dst)
		if kind == "" || hasEdge(b, kind, st.Source, t) {
			continue
		}
		if kind == EdgeWire || kind == EdgeForward {
			problem := s.noAgentIn(b, dst)
			if problem == "" && kind == EdgeForward {
				problem = s.noAgentIn(b, src)
			}
			if problem != "" {
				s.note(c, "Not connected: "+problem+". Start one there first.")
				continue
			}
		}
		e := &board.Edge{ID: auth.RandomID("e_"), Kind: kind, Source: st.Source, Target: t}
		if kind == EdgeForward {
			e.Data = json.RawMessage(`{"mode":"digest","trigger":"turn"}`)
		}
		ops = append(ops, board.Op{Op: "putEdge", Edge: e})
		if kind == EdgeWire && (contentTiles[src.Kind] || src.Kind == KindMemory) {
			wired = append(wired, t)
		}
	}
	if len(ops) == 0 {
		s.note(c, "Nothing changed.")
		return nil
	}
	if err := s.hub.ApplyOps(wid, ops); err != nil {
		return err
	}
	if st.Op == "disconnect" {
		s.note(c, "Disconnected.")
		return nil
	}
	s.note(c, fmt.Sprintf("Connected %d tile(s).", len(ops)))
	if len(wired) > 0 {
		res, err := s.PushTile(wid, src.ID, wired)
		switch {
		case err != nil:
			s.note(c, "Its content was not sent: "+err.Error())
		case res.Empty:
			s.note(c, "It has nothing to send yet.")
		default:
			s.note(c, fmt.Sprintf("Its content went to %d console(s).", res.Delivered+res.Busy))
		}
	}
	return nil
}

// addNodeDefaults is the size and starting data of the kinds only the planner
// adds this way; the board's own insert menus use the client's defaults.
func addNodeDefaults(st Step, projMachine string, data map[string]any) (w, h float64) {
	switch st.Kind {
	case "text":
		data["text"] = firstString(data["text"], "Text")
		return 280, 80
	case "shape":
		data["shape"] = firstString(data["shape"], "rect")
		return 240, 160
	case "branches":
		return 320, 420
	case KindEditor:
		return 1240, 780
	case KindMemory:
		return 320, 300
	case "file":
		data["machineId"] = projMachine
		p, _ := data["path"].(string)
		view := "file"
		if ext := strings.ToLower(path.Ext(p)); ext == ".md" || ext == ".markdown" {
			view = "markdown"
		}
		data["view"] = firstString(data["view"], view)
		return 320, 132
	}
	return 0, 0
}

func firstString(v any, fallback string) string {
	if s, _ := v.(string); s != "" {
		return s
	}
	return fallback
}

// tileIDOf is the id of a console's tile ("" when it has none).
func (sc *scope) tileIDOf(sid string) string {
	if n := sc.nodeOf[sid]; n != nil {
		return n.ID
	}
	return ""
}
