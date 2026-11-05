package orch

import (
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/session"
)

// Mission Map (docs/API-orchestration.md §7): who directs whom, groups,
// and every console's state with its current order.

// Director is the command bar or an orchestrator card.
type Director struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"` // bar | orchestrator
	Title     string     `json:"title"`
	LastOrder *LastOrder `json:"lastOrder,omitempty"`
	Targets   []string   `json:"targets"`
}

// LastOrder is the latest order text of a director.
type LastOrder struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// MissionGroup is a group with member session ids.
type MissionGroup struct {
	NodeID   string   `json:"nodeId"`
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Color    string   `json:"color"`
	Members  []string `json:"members"`
	LastTask string   `json:"lastTask,omitempty"`
}

// CurrentOrder is the order a console works on.
type CurrentOrder struct {
	OrderID string `json:"orderId"`
	Text    string `json:"text"`
}

// MissionConsole is one console.
type MissionConsole struct {
	SessionID    string        `json:"sessionId"`
	NodeID       string        `json:"nodeId"`
	WorkspaceID  string        `json:"workspaceId"`
	Handle       string        `json:"handle"`
	Agent        string        `json:"agent"`
	Role         string        `json:"role,omitempty"`
	GroupNodeID  string        `json:"groupNodeId,omitempty"`
	AgentState   string        `json:"agentState"`
	Activity     string        `json:"activity"`
	StateSince   time.Time     `json:"stateSince"`
	Paused       bool          `json:"paused"`
	Subagents    int           `json:"subagents"`
	CurrentOrder *CurrentOrder `json:"currentOrder,omitempty"`
	DirectedBy   []string      `json:"directedBy"`
}

// MissionCounts are the filter counts.
type MissionCounts struct {
	Working  int `json:"working"`
	NeedsYou int `json:"needsYou"`
	Idle     int `json:"idle"`
	Done     int `json:"done"`
}

// Mission is GET /api/workspaces/{id}/mission.
type Mission struct {
	WorkspaceID string           `json:"workspaceId"`
	Directors   []Director       `json:"directors"`
	Groups      []MissionGroup   `json:"groups"`
	Consoles    []MissionConsole `json:"consoles"`
	Counts      MissionCounts    `json:"counts"`
}

// BuildMission computes the map of one or more workspace boards (pure:
// boards, sessions and orders are inputs).
func BuildMission(wid string, boards []board.Board, sessions []session.Info, orders []Order, lastBar *LastOrder, lastCard map[string]*LastOrder) Mission {
	m := Mission{WorkspaceID: wid, Directors: []Director{}, Groups: []MissionGroup{}, Consoles: []MissionConsole{}}
	byID := map[string]session.Info{}
	for _, in := range sessions {
		byID[in.ID] = in
	}
	current := map[string]CurrentOrder{}
	lastTask := map[string]string{}
	for i := len(orders) - 1; i >= 0; i-- { // oldest first: newer overwrite
		o := orders[i]
		for _, t := range o.Targets {
			if !TargetFinal(t.State) {
				current[t.SessionID] = CurrentOrder{OrderID: o.ID, Text: clipText(o.Text, 120)}
			}
			lastTask[t.SessionID] = clipText(o.Text, 120)
		}
	}
	bar := Director{ID: "bar", Kind: "bar", Title: "Command bar", LastOrder: lastBar, Targets: []string{}}
	for _, b := range boards {
		sc := &scope{b: b, sidOf: map[string]string{}, nodeOf: map[string]*board.Node{}}
		directed := map[string][]string{}
		for _, n := range b.Nodes {
			switch n.Kind {
			case board.KindTerminal:
				sc.sidOf[n.ID] = n.SessionID()
				sc.nodeOf[n.SessionID()] = n
			case KindGroup:
				sc.groups = append(sc.groups, n)
			}
		}
		for _, n := range b.Nodes {
			if n.Kind != KindOrchestrator {
				continue
			}
			d := Director{ID: n.ID, Kind: "orchestrator", Title: TitleOf(n), LastOrder: lastCard[n.ID], Targets: []string{}}
			for _, e := range b.Edges {
				if e.Kind == EdgeWire && e.Source == n.ID {
					d.Targets = append(d.Targets, e.Target)
				}
			}
			for _, sid := range sc.wiredSessions(n.ID) {
				directed[sid] = append(directed[sid], n.ID)
			}
			m.Directors = append(m.Directors, d)
		}
		groupOf := map[string]*board.Node{}
		for _, g := range sc.groups {
			gd := GroupOf(g)
			mg := MissionGroup{NodeID: g.ID, Name: gd.Name, Role: gd.Role, Color: gd.Color, Members: []string{}}
			for _, sid := range sc.nodeSessions(g.ID) {
				mg.Members = append(mg.Members, sid)
				groupOf[sid] = g
				if t := lastTask[sid]; t != "" && mg.LastTask == "" {
					mg.LastTask = t
				}
			}
			m.Groups = append(m.Groups, mg)
			bar.Targets = append(bar.Targets, g.ID)
		}
		for _, n := range b.Nodes {
			if n.Kind != board.KindTerminal {
				continue
			}
			in, ok := byID[n.SessionID()]
			if !ok {
				continue
			}
			c := MissionConsole{SessionID: in.ID, NodeID: n.ID, WorkspaceID: in.WorkspaceID, Handle: in.Handle, Agent: in.Agent,
				AgentState: in.AgentState, Activity: in.Activity, StateSince: in.StateSince, Paused: in.Paused, Subagents: in.Subagents,
				DirectedBy: []string{"bar"}}
			if g := groupOf[in.ID]; g != nil {
				c.GroupNodeID, c.Role = g.ID, GroupOf(g).Role
			} else {
				bar.Targets = append(bar.Targets, n.ID)
			}
			c.DirectedBy = append(c.DirectedBy, directed[in.ID]...)
			if co, ok := current[in.ID]; ok {
				cp := co
				c.CurrentOrder = &cp
			}
			switch {
			case in.Paused:
				m.Counts.Idle++
			case in.AgentState == agentstate.Working || in.AgentState == agentstate.Starting || in.AgentState == agentstate.Reconnecting:
				m.Counts.Working++
			case in.AgentState == agentstate.NeedsYou || in.AgentState == agentstate.Error:
				m.Counts.NeedsYou++
			case in.AgentState == agentstate.Exited:
				m.Counts.Done++
			default:
				m.Counts.Idle++
			}
			m.Consoles = append(m.Consoles, c)
		}
	}
	m.Directors = append([]Director{bar}, m.Directors...)
	return m
}

// Mission builds the map of a workspace (or its whole project).
func (s *Service) Mission(wid string, projectScope bool) (Mission, error) {
	ws, proj, ok := s.hub.Workspace(wid)
	if !ok {
		return Mission{}, ErrWorkspaceNotFound
	}
	wids := []string{ws.ID}
	if projectScope {
		wids = nil
		for _, w := range proj.Workspaces {
			wids = append(wids, w.ID)
		}
	}
	var boards []board.Board
	for _, id := range wids {
		b, err := s.hub.Board(id)
		if err != nil {
			continue
		}
		boards = append(boards, b)
	}
	orders := s.Orders.List(func(o Order) bool { return o.ProjectID == proj.ID })
	lastOf := func(tid string) *LastOrder {
		msgs := s.Threads.Last(tid, 50)
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" && msgs[i].Kind == "order" {
				return &LastOrder{Text: clipText(msgs[i].Text, 120), At: msgs[i].At}
			}
		}
		return nil
	}
	cards := map[string]*LastOrder{}
	for _, b := range boards {
		for _, n := range b.Nodes {
			if n.Kind == KindOrchestrator {
				cards[n.ID] = lastOf(n.ID)
			}
		}
	}
	return BuildMission(ws.ID, boards, s.hub.Sessions(), orders, lastOf(proj.ID), cards), nil
}
