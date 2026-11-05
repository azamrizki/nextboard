package orch

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"nextboard/internal/board"
)

// Hub-understood board kinds (docs/API-orchestration.md §1): group,
// orchestrator and memory nodes; wire and forward edges.

const (
	KindGroup        = "group"
	KindOrchestrator = "orchestrator"
	KindMemory       = "memory"
	EdgeWire         = "wire"
	EdgeForward      = "forward"
	EdgePreview      = "preview"
	EdgeLog          = "log"
	maxMembers       = 12
)

// Colors of groups and the default per role.
var groupColors = map[string]bool{"amber": true, "sage": true, "sky": true, "violet": true, "rose": true, "teal": true}

var roleColor = map[string]string{"general": "sky", "frontend": "violet", "backend": "teal", "testers": "amber",
	"reviewers": "rose", "design": "violet", "docs": "sage", "security": "rose"}

// RoleColor is the default color of a role.
func RoleColor(role string) string {
	if c := roleColor[role]; c != "" {
		return c
	}
	return "sky"
}

// GroupData is data of a group node.
type GroupData struct {
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	Color   string   `json:"color"`
	Members []string `json:"members"`
	// SyncInput copies what is typed in one member to the other members (§5).
	SyncInput bool `json:"syncInput,omitempty"`
}

// OrchestratorData is data of an orchestrator node.
type OrchestratorData struct {
	Title       string `json:"title"`
	Personality string `json:"personality,omitempty"`
}

// ForwardData is data of a forward edge.
type ForwardData struct {
	Mode    string `json:"mode"`
	Trigger string `json:"trigger"`
}

// GroupOf decodes a group node (defaults filled in).
func GroupOf(n *board.Node) GroupData {
	var g GroupData
	_ = json.Unmarshal(n.Data, &g)
	if g.Role == "" {
		g.Role = "general"
	}
	if g.Color == "" {
		g.Color = RoleColor(g.Role)
	}
	if g.Members == nil {
		g.Members = []string{}
	}
	return g
}

// ForwardOf decodes a forward edge (defaults: digest, turn).
func ForwardOf(e *board.Edge) ForwardData {
	var f ForwardData
	_ = json.Unmarshal(e.Data, &f)
	if f.Mode == "" {
		f.Mode = "digest"
	}
	if f.Trigger == "" {
		f.Trigger = "turn"
	}
	return f
}

// TitleOf returns an orchestrator's title.
func TitleOf(n *board.Node) string {
	var o OrchestratorData
	_ = json.Unmarshal(n.Data, &o)
	if strings.TrimSpace(o.Title) == "" {
		return "Orchestrator"
	}
	return o.Title
}

// BoardValidator returns the board.Manager.Validate hook. handleTaken
// reports whether a name is a console handle anywhere in the hub.
func BoardValidator(handleTaken func(name string) bool) func(string, []*board.Node, []*board.Edge, map[string]bool) error {
	return func(_ string, nodes []*board.Node, edges []*board.Edge, touched map[string]bool) error {
		return ValidateBoard(nodes, edges, touched, handleTaken)
	}
}

// ValidateBoard checks the touched hub kinds of a board (§1).
func ValidateBoard(nodes []*board.Node, edges []*board.Edge, touched map[string]bool, handleTaken func(string) bool) error {
	byID := make(map[string]*board.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	groupTouched := false
	for _, n := range nodes {
		if !touched[n.ID] {
			continue
		}
		switch n.Kind {
		case KindGroup:
			groupTouched = true
			var g GroupData
			if err := json.Unmarshal(n.Data, &g); err != nil {
				return board.Invalid(0, "bad_group", "data grup tidak valid")
			}
			name := strings.TrimSpace(g.Name)
			if name == "" || len([]rune(name)) > 24 {
				return board.Invalid(0, "bad_group", "nama grup 1–24 karakter")
			}
			if g.Role != "" && !slices.Contains(Roles, g.Role) {
				return board.Invalid(0, "bad_group", "role grup tidak dikenal: %q", g.Role)
			}
			if g.Color != "" && !groupColors[g.Color] {
				return board.Invalid(0, "bad_group", "warna grup tidak dikenal: %q", g.Color)
			}
			if len(g.Members) > maxMembers {
				return board.Invalid(0, "too_many", "grup maksimal %d anggota", maxMembers)
			}
			for _, m := range g.Members {
				if t := byID[m]; t == nil || t.Kind != board.KindTerminal {
					return board.Invalid(0, "member_conflict", "anggota %q bukan node terminal di board ini", m)
				}
			}
			for _, o := range nodes {
				if o.ID != n.ID && o.Kind == KindGroup && strings.EqualFold(strings.TrimSpace(GroupOf(o).Name), name) {
					return board.Invalid(http.StatusConflict, "name_conflict", "nama grup %q sudah dipakai di workspace ini", name)
				}
			}
			if handleTaken != nil && handleTaken(name) {
				return board.Invalid(http.StatusConflict, "name_conflict", "nama grup %q sama dengan handle console", name)
			}
		case KindOrchestrator:
			var o OrchestratorData
			if json.Unmarshal(n.Data, &o) != nil || len([]rune(o.Title)) > 40 {
				return board.Invalid(0, "bad_orchestrator", "judul orchestrator maks 40 karakter")
			}
		case KindMemory:
			var m struct {
				Scope string `json:"scope"`
			}
			if json.Unmarshal(n.Data, &m) != nil || (m.Scope != "" && m.Scope != "project" && m.Scope != "global") {
				return board.Invalid(0, "bad_memory", `scope memory harus "project" atau "global"`)
			}
		}
	}
	if groupTouched {
		owner := map[string]string{}
		for _, n := range nodes {
			if n.Kind != KindGroup {
				continue
			}
			for _, m := range GroupOf(n).Members {
				if o, dup := owner[m]; dup && o != n.ID {
					return board.Invalid(0, "member_conflict", "terminal %q sudah anggota grup lain", m)
				} else if dup {
					return board.Invalid(0, "member_conflict", "anggota ganda %q", m)
				}
				owner[m] = n.ID
			}
		}
	}
	fwdTouched := false
	for _, e := range edges {
		if !touched[e.ID] {
			continue
		}
		src, dst := byID[e.Source], byID[e.Target]
		if src == nil || dst == nil {
			continue
		}
		switch e.Kind {
		case EdgeWire:
			if !isWireSource(src) || !isWireTarget(dst, src) {
				return board.Invalid(0, "bad_edge", "wire hanya dari orchestrator/memory/editor/tile berisi ke terminal/grup (orchestrator juga ke kanban/checklist; editor juga ke orchestrator)")
			}
			for _, o := range edges {
				if o.ID != e.ID && o.Kind == EdgeWire && o.Source == e.Source && o.Target == e.Target {
					return board.Invalid(0, "bad_edge", "wire ganda")
				}
			}
		case EdgeForward:
			if (src.Kind != board.KindTerminal && src.Kind != KindGroup) || (dst.Kind != board.KindTerminal && dst.Kind != KindGroup) || e.Source == e.Target {
				return board.Invalid(0, "bad_edge", "forward hanya antar terminal/grup")
			}
			var f ForwardData
			_ = json.Unmarshal(e.Data, &f)
			if (f.Mode != "" && f.Mode != "digest" && f.Mode != "split") || (f.Trigger != "" && f.Trigger != "turn" && f.Trigger != "order") {
				return board.Invalid(0, "bad_edge", "forward: mode digest|split, trigger turn|order")
			}
			fwdTouched = true
		case EdgePreview, EdgeLog:
			if edgeKindFor(src, dst) != e.Kind {
				return board.Invalid(0, "bad_edge", "preview hanya dari terminal ke web, log hanya dari web ke kartu log")
			}
		}
	}
	if fwdTouched && ForwardCycle(nodes, edges) {
		return board.Invalid(0, "forward_cycle", "edge forward membentuk siklus")
	}
	return nil
}

// ForwardCycle reports a cycle among forward edges. A group and its
// members are one vertex (forwarding from a member to its own group, or
// back, is a cycle too).
func ForwardCycle(nodes []*board.Node, edges []*board.Edge) bool {
	rep := map[string]string{}
	for _, n := range nodes {
		if n.Kind == KindGroup {
			for _, m := range GroupOf(n).Members {
				rep[m] = n.ID
			}
		}
	}
	v := func(id string) string {
		if r, ok := rep[id]; ok {
			return r
		}
		return id
	}
	adj := map[string][]string{}
	for _, e := range edges {
		if e.Kind == EdgeForward {
			a, b := v(e.Source), v(e.Target)
			if a == b {
				return true
			}
			adj[a] = append(adj[a], b)
		}
	}
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var visit func(string) bool
	visit = func(x string) bool {
		color[x] = grey
		for _, y := range adj[x] {
			switch color[y] {
			case grey:
				return true
			case white:
				if visit(y) {
					return true
				}
			}
		}
		color[x] = black
		return false
	}
	for x := range adj {
		if color[x] == white && visit(x) {
			return true
		}
	}
	return false
}

// GroupFrame is the frame of a group around its members (§1: bounding box
// + padding 24 + header 32).
func GroupFrame(members []*board.Node) board.Rect {
	if len(members) == 0 {
		return board.Rect{W: 400, H: 200}
	}
	minX, minY := members[0].X, members[0].Y
	maxX, maxY := members[0].X+members[0].W, members[0].Y+members[0].H
	for _, m := range members[1:] {
		minX, minY = min(minX, m.X), min(minY, m.Y)
		maxX, maxY = max(maxX, m.X+m.W), max(maxY, m.Y+m.H)
	}
	return board.Rect{X: minX - 24, Y: minY - 24 - 32, W: maxX - minX + 48, H: maxY - minY + 48 + 32}
}
