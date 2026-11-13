package orch

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// Spawning consoles with groups and wires in one board change
// (docs/API-orchestration.md §5), and layout deploys (§13).

var (
	ErrTooMany         = errors.New("too many consoles (at most 12 per call, 64 per workspace)")
	ErrGroupNotFound   = errors.New("group not found")
	ErrOrderNotFound   = errors.New("order not found")
	ErrNothingToResend = errors.New("there is nothing to resend")
	ErrMembersMissing  = errors.New("group members are not on this board")
	ErrPartNotFound    = errors.New("the project has no such part")
)

const (
	maxSpawn         = 12
	maxPerWorkspace  = 64
	termW, termH     = board.TermW, board.TermH
	termGap          = 40.0
	framePad, header = 24.0, 32.0
)

// SpawnSlot is one console of POST /api/workspaces/{id}/spawn.
type SpawnSlot struct {
	// Cwd is used when the slot is not on the project's machine (handoff).
	Cwd string `json:"cwd,omitempty"`
	// Part is the part of the project it works in, by name or id (ROADMAP #49): its machine and folder.
	Part      string `json:"part,omitempty"`
	Agent     string `json:"agent,omitempty"`
	MachineID string `json:"machineId,omitempty"`
	ProfileID string `json:"profileId,omitempty"`
	// ProviderID runs it on an API provider instead of a login (docs/API-agents.md §10.2).
	ProviderID string `json:"providerId,omitempty"`
	Yolo       *bool  `json:"yolo,omitempty"`
	// Model and Effort: nil = the default of the role or agent in settings, "" = the CLI's own choice.
	Model  *string `json:"model,omitempty"`
	Effort *string `json:"effort,omitempty"`
}

// SpawnGroup: a new group (name/role/color) or an existing one (id).
type SpawnGroup struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Role  string `json:"role,omitempty"`
	Color string `json:"color,omitempty"`
}

// Point is a board position.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// SpawnRequest is the body of POST /api/workspaces/{id}/spawn.
type SpawnRequest struct {
	WorkspaceID string      `json:"-"`
	Slots       []SpawnSlot `json:"slots"`
	Count       int         `json:"count,omitempty"`
	Agent       string      `json:"agent,omitempty"`
	Role        string      `json:"role,omitempty"`
	MachineID   string      `json:"machineId,omitempty"`
	// Part is the part of the project every slot without its own works in, by name or id.
	Part           string      `json:"part,omitempty"`
	Group          *SpawnGroup `json:"group,omitempty"`
	OrchestratorID string      `json:"orchestratorId,omitempty"`
	At             *Point      `json:"at,omitempty"`
	// Near places the consoles right of this node (handoff).
	Near string `json:"-"`
	// ByLead: the lead starts these consoles, so a slot without an account takes the API provider
	// Account names, else the one settings lead.providers gives its agent (docs/API-agents.md §10.2).
	ByLead  bool   `json:"-"`
	Account string `json:"-"`
}

// SpawnResult is the 201 body.
type SpawnResult struct {
	Sessions    []session.Info `json:"sessions"`
	GroupNodeID string         `json:"groupNodeId,omitempty"`
}

// pickAgent resolves a slot's agent: slot → role agent → machine default.
// Unknown agents, and ones turned off, fall back to the default; deleted custom ones to shell.
func (s *Service) pickAgent(want, role, machineID string) string {
	all := s.hub.Agents()
	if want != "" {
		if a, ok := agentByID(all, want); ok && !s.hub.Settings().Agents.Disabled[a.ID] {
			return a.ID
		}
	}
	if role != "" {
		if r := s.hub.Settings().Agents.RoleAgents[role]; r != "" {
			if a, ok := agentByID(all, r); ok && (a.Kind == agent.KindShell || s.hub.AgentOnMachine(machineID, a.ID)) {
				return a.ID
			}
		}
	}
	if d := s.hub.DefaultAgent(machineID); d != "" {
		return d
	}
	return "shell"
}

// slotModel fills in what a slot left to the settings.
func (s *Service) slotModel(role, ag string, model, effort *string) (*string, *string) {
	c := s.hub.Settings().Agents.ModelFor(role, ag)
	if model == nil {
		model = &c.Model
	}
	if effort == nil {
		effort = &c.Effort
	}
	return model, effort
}

// Spawn creates consoles (+ a group and wires) in one board op batch.
func (s *Service) Spawn(req SpawnRequest) (SpawnResult, error) {
	ws, proj, ok := s.hub.Workspace(req.WorkspaceID)
	if !ok {
		return SpawnResult{}, ErrWorkspaceNotFound
	}
	slots := req.Slots
	if len(slots) == 0 {
		n := max(req.Count, 1)
		if n > maxSpawn {
			return SpawnResult{}, ErrTooMany
		}
		for i := 0; i < n; i++ {
			slots = append(slots, SpawnSlot{Agent: req.Agent, MachineID: req.MachineID})
		}
	}
	if len(slots) > maxSpawn {
		return SpawnResult{}, ErrTooMany
	}
	inWs := 0
	for _, in := range s.hub.Sessions() {
		if in.WorkspaceID == ws.ID {
			inWs++
		}
	}
	if inWs+len(slots) > maxPerWorkspace {
		return SpawnResult{}, ErrTooMany
	}
	b, err := s.hub.Board(ws.ID)
	if err != nil {
		return SpawnResult{}, err
	}
	var group *board.Node
	role := req.Role
	if req.Group != nil && req.Group.ID != "" {
		for _, n := range b.Nodes {
			if n.ID == req.Group.ID && n.Kind == KindGroup {
				group = n
			}
		}
		if group == nil {
			return SpawnResult{}, ErrGroupNotFound
		}
		if role == "" {
			role = GroupOf(group).Role
		}
	} else if req.Group != nil && req.Group.Role != "" && role == "" {
		role = req.Group.Role
	}
	if req.OrchestratorID != "" && !slices.ContainsFunc(b.Nodes, func(n *board.Node) bool { return n.ID == req.OrchestratorID && n.Kind == KindOrchestrator }) {
		return SpawnResult{}, ErrBadCard
	}
	// Positions: a grid at At, below an existing group, right of Near, or
	// right of the board's content.
	cols := 2
	switch {
	case len(slots) > 9:
		cols = 4
	case len(slots) > 4:
		cols = 3
	case len(slots) == 1:
		cols = 1
	}
	x0, y0 := rightOf(b.Nodes)
	if req.Group != nil {
		x0, y0 = x0+framePad, y0+framePad+header
	}
	switch {
	case req.At != nil:
		x0, y0 = req.At.X, req.At.Y
	case group != nil:
		x0, y0 = group.X+framePad, group.Y+group.H+termGap
	case req.Near != "":
		for _, n := range b.Nodes {
			if n.ID == req.Near {
				x0, y0 = n.X+n.W+termGap, n.Y
			}
		}
	}
	var created []session.Info
	rollback := func() {
		for _, in := range created {
			_ = s.hub.DeleteSession(in.ID)
		}
	}
	var ops []board.Op
	var termNodes []*board.Node
	for i, sl := range slots {
		mid := sl.MachineID
		named := sl.Part
		if named == "" {
			named = req.Part
		}
		var part project.Part
		if named != "" {
			pt, ok := partNamed(proj, named)
			if !ok {
				rollback()
				return SpawnResult{}, fmt.Errorf("%w: %q", ErrPartNotFound, named)
			}
			part, mid = pt, pt.MachineID
		}
		if mid == "" {
			mid = proj.MachineID
		}
		ag := s.pickAgent(sl.Agent, role, mid)
		if req.ByLead && sl.ProfileID == "" && sl.ProviderID == "" {
			pid, err := s.providerFor(ag, req.Account)
			if err != nil {
				rollback()
				return SpawnResult{}, err
			}
			sl.ProviderID = pid
		}
		cr := session.CreateRequest{Agent: ag, MachineID: mid, WorkspaceID: ws.ID, ProjectID: proj.ID, Yolo: sl.Yolo}
		cr.Model, cr.Effort = s.slotModel(role, ag, sl.Model, sl.Effort)
		if sl.ProviderID != "" && sl.Model == nil {
			cr.Model = nil // the provider's own model, not one the settings name for a login
		}
		switch {
		case part.ID != "":
			cr.Cwd, cr.PartID = part.Path, part.ID
		case mid == proj.MachineID:
			cr.Cwd = proj.Path
		case sl.Cwd != "":
			cr.Cwd = sl.Cwd
		default:
			// another machine of the project: the folder of its part there
			for _, pt := range proj.Parts {
				if pt.MachineID == mid {
					cr.Cwd, cr.PartID = pt.Path, pt.ID
					break
				}
			}
		}
		if sl.ProfileID != "" {
			p := sl.ProfileID
			cr.ProfileID = &p
		}
		if sl.ProviderID != "" {
			p := sl.ProviderID
			cr.ProviderID = &p
		}
		in, err := s.hub.CreateSession(cr)
		if err != nil {
			rollback()
			return SpawnResult{}, err
		}
		created = append(created, in)
		data, _ := json.Marshal(map[string]string{"sessionId": in.ID})
		n := &board.Node{ID: auth.RandomID("n_"), Kind: board.KindTerminal,
			X: clampCoord(x0 + float64(i%cols)*(termW+termGap)), Y: clampCoord(y0 + float64(i/cols)*(termH+termGap)), W: termW, H: termH, Data: data}
		termNodes = append(termNodes, n)
		ops = append(ops, board.Op{Op: "put", Node: n})
	}
	res := SpawnResult{Sessions: created}
	switch {
	case group != nil:
		g := GroupOf(group)
		members := append(slices.Clone(g.Members), ids(termNodes)...)
		frame := GroupFrame(append(membersOf(b, g.Members), termNodes...))
		patch, _ := json.Marshal(map[string]any{"members": members})
		ops = append(ops, board.Op{Op: "patch", ID: group.ID, Set: map[string]json.RawMessage{
			"x": num(frame.X), "y": num(frame.Y), "w": num(frame.W), "h": num(frame.H), "data": patch}})
		res.GroupNodeID = group.ID
	case req.Group != nil:
		name := strings.TrimSpace(req.Group.Name)
		if name == "" {
			name = defaultGroupName(b)
		}
		gr := role
		if gr == "" {
			gr = "general"
		}
		color := req.Group.Color
		if color == "" {
			color = RoleColor(gr)
		}
		frame := GroupFrame(termNodes)
		data, _ := json.Marshal(GroupData{Name: name, Role: gr, Color: color, Members: ids(termNodes)})
		gn := &board.Node{ID: auth.RandomID("n_"), Kind: KindGroup, X: frame.X, Y: frame.Y, W: frame.W, H: frame.H, Data: data}
		// The frame goes below the consoles in z order.
		ops = append([]board.Op{{Op: "put", Node: gn}}, ops...)
		res.GroupNodeID = gn.ID
	}
	if req.OrchestratorID != "" {
		targets := ids(termNodes)
		if res.GroupNodeID != "" {
			targets = []string{res.GroupNodeID}
		}
		for _, t := range targets {
			if !hasEdge(b, EdgeWire, req.OrchestratorID, t) {
				ops = append(ops, board.Op{Op: "putEdge", Edge: &board.Edge{ID: auth.RandomID("e_"), Kind: EdgeWire, Source: req.OrchestratorID, Target: t, Data: json.RawMessage(`{}`)}})
			}
		}
	}
	if err := s.hub.ApplyOps(ws.ID, ops); err != nil {
		rollback()
		return SpawnResult{}, err
	}
	return res, nil
}

func clampCoord(v float64) float64 { return min(max(v, -board.MaxCoord), board.MaxCoord-termW) }

func num(f float64) json.RawMessage { b, _ := json.Marshal(f); return b }

func ids(ns []*board.Node) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.ID)
	}
	return out
}

func membersOf(b board.Board, members []string) []*board.Node {
	var out []*board.Node
	for _, n := range b.Nodes {
		if slices.Contains(members, n.ID) {
			out = append(out, n)
		}
	}
	return out
}

func defaultGroupName(b board.Board) string {
	taken := map[string]bool{}
	for _, n := range b.Nodes {
		if n.Kind == KindGroup {
			taken[strings.ToLower(GroupOf(n).Name)] = true
		}
	}
	for i := 1; ; i++ {
		if name := fmt.Sprintf("Group %d", i); !taken[strings.ToLower(name)] {
			return name
		}
	}
}

// CreateGroup puts a group frame around existing consoles.
func (s *Service) CreateGroup(wid string, members []string, name, role string) (string, error) {
	b, err := s.hub.Board(wid)
	if err != nil {
		return "", err
	}
	var nodes []*board.Node
	for _, sid := range members {
		for _, n := range b.Nodes {
			if n.Kind == board.KindTerminal && n.SessionID() == sid {
				nodes = append(nodes, n)
			}
		}
	}
	if len(nodes) == 0 {
		return "", ErrMembersMissing
	}
	if name = strings.TrimSpace(name); name == "" {
		name = defaultGroupName(b)
	}
	if role == "" {
		role = "general"
	}
	frame := GroupFrame(nodes)
	data, _ := json.Marshal(GroupData{Name: name, Role: role, Color: RoleColor(role), Members: ids(nodes)})
	gn := &board.Node{ID: auth.RandomID("n_"), Kind: KindGroup, X: frame.X, Y: frame.Y, W: frame.W, H: frame.H, Data: data}
	ops := []board.Op{{Op: "put", Node: gn}, {Op: "z", ID: gn.ID, To: "back"}}
	if err := s.hub.ApplyOps(wid, ops); err != nil {
		return "", err
	}
	return name, nil
}

// Deploy creates a layout's consoles, groups, tiles and wires in one board
// change (§13). Lane agent: override → fill → lane → role agent → machine
// default; machine: override → project machine.
func (s *Service) Deploy(wid string, req DeployRequest) (DeployResult, error) {
	ws, proj, ok := s.hub.Workspace(wid)
	if !ok {
		return DeployResult{}, ErrWorkspaceNotFound
	}
	l, ok := s.Layouts.Get(req.LayoutID)
	if !ok {
		return DeployResult{}, ErrLayoutNotFound
	}
	l, err := l.Sized(req.GroupSizes)
	if err != nil {
		return DeployResult{}, err
	}
	if len(l.Lanes) > maxSpawn {
		return DeployResult{}, ErrTooMany
	}
	inWs := 0
	for _, in := range s.hub.Sessions() {
		if in.WorkspaceID == ws.ID {
			inWs++
		}
	}
	if inWs+len(l.Lanes) > maxPerWorkspace {
		return DeployResult{}, ErrTooMany
	}
	b, err := s.hub.Board(wid)
	if err != nil {
		return DeployResult{}, err
	}
	ox, oy := rightOf(b.Nodes)
	if req.At != nil {
		ox, oy = req.At.X, req.At.Y
	}
	keyNode := map[string]string{}
	var created []session.Info
	rollback := func() {
		for _, in := range created {
			_ = s.hub.DeleteSession(in.ID)
		}
	}
	var ops []board.Op
	laneNodes := map[string][]*board.Node{} // group key → terminals
	groupRole := map[string]string{}
	for _, g := range l.Groups {
		groupRole[g.Key] = g.Role
	}
	for _, ln := range l.Lanes {
		ov := req.LaneOverrides[ln.Key]
		mid := ov.MachineID
		if mid == "" {
			mid = proj.MachineID
		}
		want := ov.Agent
		if want == "" && req.Fill != nil {
			want = req.Fill.Agent
		}
		if want == "" {
			want = ln.Agent
		}
		role := ln.Role
		if role == "" {
			role = groupRole[ln.Group]
		}
		ag := s.pickAgent(want, role, mid)
		if want != "" && ag != want && ov.Agent == "" && ln.Agent == want {
			// A saved lane whose custom agent was deleted becomes a shell.
			if _, known := agentByID(s.hub.Agents(), want); !known && !isBuiltinAgent(want) {
				ag = "shell"
			}
		}
		cr := session.CreateRequest{Agent: ag, MachineID: mid, WorkspaceID: ws.ID, ProjectID: proj.ID, Yolo: ov.Yolo}
		cr.Model, cr.Effort = s.slotModel(role, ag, ov.Model, ov.Effort)
		if mid == proj.MachineID {
			cr.Cwd = proj.Path
		}
		if ov.ProfileID != "" {
			p := ov.ProfileID
			cr.ProfileID = &p
		}
		if ov.ProviderID != "" {
			p := ov.ProviderID
			cr.ProviderID = &p
		}
		in, err := s.hub.CreateSession(cr)
		if err != nil {
			rollback()
			return DeployResult{}, err
		}
		created = append(created, in)
		data, _ := json.Marshal(map[string]string{"sessionId": in.ID})
		n := &board.Node{ID: auth.RandomID("n_"), Kind: board.KindTerminal, X: clampCoord(ox + ln.X), Y: clampCoord(oy + ln.Y),
			W: max(ln.W, 200), H: max(ln.H, 150), Data: data}
		keyNode[ln.Key] = n.ID
		laneNodes[ln.Group] = append(laneNodes[ln.Group], n)
		ops = append(ops, board.Op{Op: "put", Node: n})
	}
	var groupOps []board.Op
	taken := map[string]bool{}
	for _, n := range b.Nodes {
		if n.Kind == KindGroup {
			taken[strings.ToLower(GroupOf(n).Name)] = true
		}
	}
	for _, g := range l.Groups {
		members := laneNodes[g.Key]
		frame := GroupFrame(members)
		name := g.Name
		for i := 2; taken[strings.ToLower(name)] || s.handleTaken(name); i++ {
			name = fmt.Sprintf("%s %d", g.Name, i)
		}
		taken[strings.ToLower(name)] = true
		role := g.Role
		if role == "" {
			role = "general"
		}
		color := g.Color
		if color == "" {
			color = RoleColor(role)
		}
		data, _ := json.Marshal(GroupData{Name: name, Role: role, Color: color, Members: ids(members)})
		gn := &board.Node{ID: auth.RandomID("n_"), Kind: KindGroup, X: frame.X, Y: frame.Y, W: frame.W, H: frame.H, Data: data}
		keyNode[g.Key] = gn.ID
		groupOps = append(groupOps, board.Op{Op: "put", Node: gn})
	}
	ops = append(groupOps, ops...)
	for _, n := range l.Nodes {
		if n.Kind == KindEditor {
			// A board has one editor card: the layout's wires go to the one it has, and it moves
			// into the layout's place, as the preview drew it (left where it was, it looked as if the
			// layout had none). Its open files go with it; one the owner locked stays put.
			if i := slices.IndexFunc(b.Nodes, func(o *board.Node) bool { return o.Kind == KindEditor }); i >= 0 {
				old := b.Nodes[i]
				keyNode[n.Key] = old.ID
				if !old.Locked {
					ops = append(ops, board.Op{Op: "put", Node: &board.Node{ID: old.ID, Kind: old.Kind, X: clampCoord(ox + n.X), Y: clampCoord(oy + n.Y),
						W: max(n.W, 40), H: max(n.H, 40), Rotation: old.Rotation, Data: old.Data}})
				}
				continue
			}
		}
		raw, _ := json.Marshal(n.Data)
		if n.Data == nil {
			raw = []byte("{}")
		}
		node := &board.Node{ID: auth.RandomID("n_"), Kind: n.Kind, X: clampCoord(ox + n.X), Y: clampCoord(oy + n.Y), W: max(n.W, 40), H: max(n.H, 40), Data: raw}
		keyNode[n.Key] = node.ID
		ops = append(ops, board.Op{Op: "put", Node: node})
	}
	for _, w := range l.Wires {
		f, t := keyNode[w.From], keyNode[w.To]
		if f == "" || t == "" {
			continue
		}
		raw := json.RawMessage(`{}`)
		if w.Data != nil {
			raw, _ = json.Marshal(w.Data)
		}
		ops = append(ops, board.Op{Op: "putEdge", Edge: &board.Edge{ID: auth.RandomID("e_"), Kind: w.Kind, Source: f, Target: t, Data: raw}})
	}
	if err := s.hub.ApplyOps(wid, ops); err != nil {
		rollback()
		return DeployResult{}, err
	}
	res := DeployResult{Sessions: created, NodeIDs: []string{}}
	for _, op := range ops {
		if op.Node == nil {
			continue
		}
		res.NodeIDs = append(res.NodeIDs, op.Node.ID)
		// its consoles started before the wires existed, so their launch has no blocks
		if op.Node.Kind == KindMemory && len(tileBlocks(op.Node)) > 0 && !tileIsManual(op.Node) {
			_, _ = s.PushTile(wid, op.Node.ID, nil)
		}
	}
	return res, nil
}

func isBuiltinAgent(id string) bool {
	for _, a := range append(agent.Builtins(), agent.Catalog()...) {
		if a.ID == id {
			return true
		}
	}
	return false
}

// handleTaken reports whether a name is a console handle.
func (s *Service) handleTaken(name string) bool {
	n := Norm(name)
	for _, in := range s.hub.Sessions() {
		if Norm(in.Handle) == n {
			return true
		}
	}
	return false
}

// partNamed finds a part of the project by id or name, any case.
func partNamed(proj project.Project, name string) (project.Part, bool) {
	name = strings.TrimSpace(name)
	for _, pt := range proj.Parts {
		if pt.ID == name || strings.EqualFold(pt.Name, name) {
			return pt, true
		}
	}
	return project.Part{}, false
}

// partNames are the names of a project's parts, for the typed grammar; none for one part.
func partNames(proj project.Project) []string {
	if len(proj.Parts) < 2 {
		return nil
	}
	out := make([]string, 0, len(proj.Parts))
	for _, pt := range proj.Parts {
		out = append(out, pt.Name)
	}
	return out
}
