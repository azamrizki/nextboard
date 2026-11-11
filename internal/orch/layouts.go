package orch

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/store"
)

// Layout templates (docs/API-orchestration.md §13). Coordinates are
// relative to the deploy point.

const layoutsFile = "layouts.json"

var (
	ErrLayoutNotFound = errors.New("layout not found")
	ErrLayoutBuiltin  = errors.New("a built-in layout cannot be changed or deleted")
	ErrBadLayout      = errors.New("invalid layout")
)

// Lane is a console slot.
type Lane struct {
	Key   string  `json:"key"`
	Agent string  `json:"agent,omitempty"`
	Role  string  `json:"role,omitempty"`
	Group string  `json:"group,omitempty"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
}

// LayoutGroup is a group of lanes.
type LayoutGroup struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Role  string `json:"role"`
	Color string `json:"color"`
}

// LayoutNode is another tile.
type LayoutNode struct {
	Key  string         `json:"key"`
	Kind string         `json:"kind"`
	X    float64        `json:"x"`
	Y    float64        `json:"y"`
	W    float64        `json:"w"`
	H    float64        `json:"h"`
	Data map[string]any `json:"data"`
}

// LayoutWire connects keys (wire or forward edge).
type LayoutWire struct {
	From string         `json:"from"`
	To   string         `json:"to"`
	Kind string         `json:"kind"`
	Data map[string]any `json:"data,omitempty"`
}

// Layout is a template.
type Layout struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Description says in one line what a built-in is for. Search finds it too.
	Description string        `json:"description,omitempty"`
	Builtin     bool          `json:"builtin"`
	CreatedAt   *time.Time    `json:"createdAt,omitempty"`
	Lanes       []Lane        `json:"lanes"`
	Groups      []LayoutGroup `json:"groups"`
	Nodes       []LayoutNode  `json:"nodes"`
	Wires       []LayoutWire  `json:"wires"`
}

var layoutNodeKinds = map[string]bool{"orchestrator": true, "kanban": true, "note": true, "text": true, "checklist": true, "memory": true, "web": true, KindEditor: true}

const (
	laneW, laneH, laneGap = 720, 440, 40
	groupPad              = 24 + 32
)

// gridLanes places n lanes in rows of cols starting at (x0, y0).
func gridLanes(prefix string, n, cols int, x0, y0 float64, agent, role, group string) []Lane {
	var out []Lane
	for i := 0; i < n; i++ {
		out = append(out, Lane{Key: fmt.Sprintf("%s%d", prefix, i+1), Agent: agent, Role: role, Group: group,
			X: x0 + float64(i%cols)*(laneW+laneGap), Y: y0 + float64(i/cols)*(laneH+laneGap), W: laneW, H: laneH})
	}
	return out
}

func kanbanData(title string, cols ...string) map[string]any {
	var c []any
	for i, t := range cols {
		c = append(c, map[string]any{"id": fmt.Sprintf("c%d", i+1), "title": t, "cards": []any{}})
	}
	return map[string]any{"title": title, "columns": c}
}

func checklistData(title string, items ...string) map[string]any {
	c := []any{}
	for i, t := range items {
		c = append(c, map[string]any{"id": fmt.Sprintf("i%d", i+1), "text": t, "done": false})
	}
	return map[string]any{"title": title, "items": c}
}

// BuiltinLayouts are the NextBoard built-in templates.
func BuiltinLayouts() []Layout {
	g := func(key, name, role string) LayoutGroup {
		return LayoutGroup{Key: key, Name: name, Role: role, Color: RoleColor(role)}
	}
	y := float64(groupPad)
	def4 := []string{"Backlog", "To do", "In progress", "Done"}
	col := float64(laneW + laneGap)
	// beside one, two, three and four lanes; below a group of one row and of two
	x1, x2, x3, x4 := col+120, 2*col+120, 3*col+120, 4*col+240
	row2 := y + laneH + laneGap + groupPad + 40
	row3 := row2 + laneH + laneGap + groupPad + 40
	orchAt := func(x, yy float64) LayoutNode {
		return LayoutNode{Key: "o1", Kind: "orchestrator", X: x, Y: yy, W: 420, H: 520, Data: map[string]any{"title": "Orchestrator"}}
	}
	wired := func(groups ...string) []LayoutWire {
		var w []LayoutWire
		for _, to := range groups {
			w = append(w, LayoutWire{From: "o1", To: to, Kind: "wire"})
		}
		return w
	}
	const editorW, editorH = 1240, 780
	ls := []Layout{
		{ID: "builtin:duo", Name: "Duo", Lanes: gridLanes("a", 2, 2, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Duo", "general")}},
		{ID: "builtin:trio", Name: "Trio", Lanes: gridLanes("a", 3, 3, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Trio", "general")}},
		{ID: "builtin:quartet", Name: "Quartet", Lanes: gridLanes("a", 4, 2, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Quartet", "general")}},
		{ID: "builtin:two-quartets", Name: "Two quartets",
			Lanes:  append(gridLanes("a", 4, 2, 24, y, "", "", "g1"), gridLanes("b", 4, 2, 24+2*(laneW+laneGap)+120, y, "", "", "g2")...),
			Groups: []LayoutGroup{g("g1", "Team A", "general"), g("g2", "Team B", "general")}},
		{ID: "builtin:build-review", Name: "Build & review",
			Lanes: append(gridLanes("b", 2, 2, 24, y, "", "general", "g1"), Lane{Key: "r1", Role: "reviewers", Group: "g2",
				X: 24, Y: y + laneH + laneGap + groupPad + 40, W: laneW, H: laneH}),
			Groups: []LayoutGroup{g("g1", "Builders", "general"), g("g2", "Review", "reviewers")},
			Nodes: []LayoutNode{
				{Key: "o1", Kind: "orchestrator", X: 2*(laneW+laneGap) + 120, Y: y, W: 420, H: 520, Data: map[string]any{"title": "Orchestrator"}},
				{Key: "k1", Kind: "kanban", X: 2*(laneW+laneGap) + 120, Y: y + 560, W: 900, H: 420, Data: kanbanData("Tasks", def4...)},
			},
			Wires: []LayoutWire{{From: "o1", To: "g1", Kind: "wire"}, {From: "o1", To: "g2", Kind: "wire"}}},
		{ID: "builtin:front-back", Name: "Front & back",
			Lanes:  append(gridLanes("f", 2, 2, 24, y, "", "frontend", "g1"), gridLanes("b", 2, 2, 24, y+laneH+laneGap+groupPad+40, "", "backend", "g2")...),
			Groups: []LayoutGroup{g("g1", "Frontend", "frontend"), g("g2", "Backend", "backend")},
			Nodes:  []LayoutNode{{Key: "o1", Kind: "orchestrator", X: 2*(laneW+laneGap) + 120, Y: y, W: 420, H: 520, Data: map[string]any{"title": "Orchestrator"}}},
			Wires:  []LayoutWire{{From: "o1", To: "g1", Kind: "wire"}, {From: "o1", To: "g2", Kind: "wire"}}},
		{ID: "builtin:review-trio", Name: "Review trio", Lanes: gridLanes("r", 3, 3, 24, y, "", "reviewers", "g1"), Groups: []LayoutGroup{g("g1", "Reviewers", "reviewers")}},
		{ID: "builtin:triage", Name: "Triage",
			Lanes: append(gridLanes("t", 2, 2, 24, y, "", "testers", "g1"), Lane{Key: "r1", Role: "reviewers", Group: "g2",
				X: 24, Y: y + laneH + laneGap + groupPad + 40, W: laneW, H: laneH}),
			Groups: []LayoutGroup{g("g1", "Testers", "testers"), g("g2", "Review", "reviewers")},
			Nodes:  []LayoutNode{{Key: "k1", Kind: "kanban", X: 2*(laneW+laneGap) + 120, Y: y, W: 900, H: 460, Data: kanbanData("Triage", "New", "Confirmed", "Fixing", "Verified")}}},
		{ID: "builtin:preview-lab", Name: "Preview lab", Lanes: gridLanes("a", 3, 3, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Lab", "frontend")},
			Nodes: []LayoutNode{{Key: "w1", Kind: "web", X: 24, Y: y + laneH + laneGap + 40, W: 1280, H: 800, Data: map[string]any{"url": "http://localhost:5173/"}}}},
		{ID: "builtin:docs-crew", Name: "Docs crew", Lanes: gridLanes("d", 2, 2, 24, y, "", "docs", "g1"), Groups: []LayoutGroup{g("g1", "Docs", "docs")},
			Nodes: []LayoutNode{{Key: "c1", Kind: "checklist", X: 2*(laneW+laneGap) + 120, Y: y, W: 420, H: 440, Data: map[string]any{"title": "Docs to write", "items": []any{}}}}},
		{ID: "builtin:solo", Name: "Solo", Lanes: gridLanes("a", 1, 1, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Solo", "general")},
			Nodes: []LayoutNode{{Key: "c1", Kind: "checklist", X: x1, Y: y, W: 420, H: 440, Data: checklistData("To do")}}},
		{ID: "builtin:sextet", Name: "Sextet", Lanes: gridLanes("a", 6, 3, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Sextet", "general")}},
		{ID: "builtin:three-trios", Name: "Three trios",
			Lanes:  slices.Concat(gridLanes("a", 3, 3, 24, y, "", "", "g1"), gridLanes("b", 3, 3, 24, row2, "", "", "g2"), gridLanes("c", 3, 3, 24, row3, "", "", "g3")),
			Groups: []LayoutGroup{g("g1", "Team A", "general"), g("g2", "Team B", "general"), g("g3", "Team C", "general")},
			Nodes:  []LayoutNode{orchAt(x3, y)},
			Wires:  wired("g1", "g2", "g3")},
		{ID: "builtin:lead-crew", Name: "Lead & crew", Lanes: gridLanes("a", 4, 2, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Crew", "general")},
			Nodes: []LayoutNode{orchAt(x2, y), {Key: "k1", Kind: "kanban", X: x2, Y: y + 560, W: 900, H: 420, Data: kanbanData("Tasks", def4...)}},
			Wires: wired("g1")},
		{ID: "builtin:full-stack", Name: "Full stack",
			Lanes: slices.Concat(gridLanes("f", 2, 2, 24, y, "", "frontend", "g1"), gridLanes("b", 2, 2, 24+x2, y, "", "backend", "g2"),
				gridLanes("t", 2, 2, 24, row2, "", "testers", "g3"), gridLanes("r", 2, 2, 24+x2, row2, "", "reviewers", "g4")),
			Groups: []LayoutGroup{g("g1", "Frontend", "frontend"), g("g2", "Backend", "backend"), g("g3", "Testers", "testers"), g("g4", "Review", "reviewers")},
			Nodes:  []LayoutNode{orchAt(x4, y), {Key: "k1", Kind: "kanban", X: x4, Y: y + 560, W: 900, H: 420, Data: kanbanData("Tasks", def4...)}},
			Wires:  wired("g1", "g2", "g3", "g4")},
		{ID: "builtin:code-pair", Name: "Code & pair", Lanes: gridLanes("a", 2, 1, 24+editorW+120, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Pair", "general")},
			Nodes: []LayoutNode{{Key: "e1", Kind: KindEditor, X: 24, Y: y, W: editorW, H: editorH, Data: map[string]any{}}}},
		{ID: "builtin:web-builder", Name: "Web builder",
			Lanes:  slices.Concat(gridLanes("f", 1, 1, 24, y, "", "frontend", "g1"), gridLanes("b", 1, 1, 24+x1, y, "", "backend", "g2")),
			Groups: []LayoutGroup{g("g1", "Frontend", "frontend"), g("g2", "Backend", "backend")},
			Nodes: []LayoutNode{{Key: "e1", Kind: KindEditor, X: 24, Y: row2, W: editorW, H: editorH, Data: map[string]any{}},
				{Key: "w1", Kind: "web", X: 24 + editorW + 120, Y: row2, W: 960, H: editorH, Data: map[string]any{"url": "http://localhost:5173/"}}}},
		{ID: "builtin:bug-hunt", Name: "Bug hunt",
			Lanes:  append(gridLanes("t", 2, 2, 24, y, "", "testers", "g1"), gridLanes("x", 2, 2, 24, row2, "", "general", "g2")...),
			Groups: []LayoutGroup{g("g1", "Testers", "testers"), g("g2", "Fixers", "general")},
			Nodes:  []LayoutNode{{Key: "k1", Kind: "kanban", X: x2, Y: y, W: 900, H: 460, Data: kanbanData("Bugs", "Reported", "Reproduced", "Fixing", "Verified")}, orchAt(x2, y+500)},
			Wires:  wired("g1", "g2")},
		{ID: "builtin:security-audit", Name: "Security audit",
			Lanes:  append(gridLanes("s", 2, 2, 24, y, "", "security", "g1"), gridLanes("r", 1, 1, 24, row2, "", "reviewers", "g2")...),
			Groups: []LayoutGroup{g("g1", "Security", "security"), g("g2", "Review", "reviewers")},
			Nodes: []LayoutNode{{Key: "c1", Kind: "checklist", X: x2, Y: y, W: 420, H: 440,
				Data: checklistData("Audit", "Secrets in code and history", "Authentication and sessions", "Input validation", "Dependencies", "File and network permissions")}}},
		{ID: "builtin:design-studio", Name: "Design studio", Lanes: gridLanes("d", 2, 2, 24, y, "", "design", "g1"), Groups: []LayoutGroup{g("g1", "Design", "design")},
			Nodes: []LayoutNode{{Key: "w1", Kind: "web", X: 24, Y: row2, W: 1280, H: 800, Data: map[string]any{"url": "http://localhost:5173/"}},
				{Key: "n1", Kind: "note", X: x2, Y: y, W: 420, H: 300, Data: map[string]any{"text": "Design notes", "color": "yellow"}}}},
		{ID: "builtin:release-crew", Name: "Release crew",
			Lanes:  slices.Concat(gridLanes("b", 2, 2, 24, y, "", "general", "g1"), gridLanes("t", 1, 1, 24, row2, "", "testers", "g2"), gridLanes("d", 1, 1, 24+x1, row2, "", "docs", "g3")),
			Groups: []LayoutGroup{g("g1", "Builders", "general"), g("g2", "Testers", "testers"), g("g3", "Docs", "docs")},
			Nodes:  []LayoutNode{orchAt(x2+x1-col, y), {Key: "k1", Kind: "kanban", X: x2 + x1 - col, Y: y + 560, W: 1000, H: 420, Data: kanbanData("Release", "To do", "Building", "Testing", "Ready", "Shipped")}},
			Wires:  wired("g1", "g2", "g3")},
		{ID: "builtin:research-desk", Name: "Research desk", Lanes: gridLanes("a", 2, 2, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Research", "general")},
			Nodes: []LayoutNode{{Key: "m1", Kind: KindMemory, X: x2, Y: y, W: 320, H: 300, Data: map[string]any{"blocks": []any{}}},
				{Key: "n1", Kind: "note", X: x2, Y: y + 340, W: 420, H: 300, Data: map[string]any{"text": "Findings", "color": "yellow"}}}},
	}
	ls = append(ls, moreLayouts(g, y, col, x1, x2, x3, row2, def4, orchAt, wired, editorW, editorH)...)
	for i := range ls {
		ls[i].Builtin = true
		if d, ok := layoutDescriptions[ls[i].ID]; ok {
			ls[i].Description = d
		}
		fill(&ls[i])
	}
	return ls
}

// layoutDescriptions say what each built-in is for, in the library and in its search.
var layoutDescriptions = map[string]string{
	"builtin:duo":               "Two agents side by side.",
	"builtin:trio":              "Three agents in a row.",
	"builtin:quartet":           "Four agents in two rows.",
	"builtin:two-quartets":      "Two separate teams of four.",
	"builtin:build-review":      "Builders and a reviewer, led by an orchestrator, with a task board.",
	"builtin:front-back":        "A frontend team and a backend team under one orchestrator.",
	"builtin:review-trio":       "Three reviewers, for a second, third and fourth opinion.",
	"builtin:triage":            "Testers and a reviewer sort bugs on a board.",
	"builtin:preview-lab":       "Three agents above a live preview of a dev server.",
	"builtin:docs-crew":         "Two writers with a list of docs to write.",
	"builtin:solo":              "One agent and a to-do list.",
	"builtin:sextet":            "Six agents in two rows.",
	"builtin:three-trios":       "Three teams of three under one orchestrator.",
	"builtin:lead-crew":         "A crew of four, an orchestrator and a task board.",
	"builtin:full-stack":        "Frontend, backend, testers and reviewers, with an orchestrator and a board.",
	"builtin:code-pair":         "The code editor and two agents beside it.",
	"builtin:web-builder":       "Frontend and backend above the editor and a live preview.",
	"builtin:bug-hunt":          "Testers find bugs, fixers fix them, a board keeps track.",
	"builtin:security-audit":    "Security agents and a reviewer, with an audit checklist.",
	"builtin:design-studio":     "Designers with a live preview and notes.",
	"builtin:release-crew":      "Builders, a tester and a writer, with a release board.",
	"builtin:research-desk":     "Two researchers, shared memory and a note for findings.",
	"builtin:two-duos":          "Two pairs working apart.",
	"builtin:octet":             "Eight agents in two rows.",
	"builtin:writer-checker":    "One writes, one checks.",
	"builtin:relay":             "Builder to tester to reviewer: each turn's result goes on to the next by itself, and uses their turns.",
	"builtin:editor-agent":      "The code editor and one agent.",
	"builtin:code-crew":         "The code editor, a crew of four and an orchestrator.",
	"builtin:laravel-crew":      "Backend, Blade and tests, with the code editor and a board for a Laravel app.",
	"builtin:api-crew":          "Backend, a tester and a writer, with a board per endpoint.",
	"builtin:app-team":          "Frontend, backend and a tester above a live preview.",
	"builtin:ui-lab":            "Frontend and a designer above a live preview.",
	"builtin:refactor-squad":    "Three to change the code, one to review, with the steps on a list.",
	"builtin:migration-desk":    "Backend and a tester, with a checklist for moving data safely.",
	"builtin:incident-room":     "Responders and a fixer, with a timeline and a runbook.",
	"builtin:review-board":      "Reviewers and a security agent, with a review board.",
	"builtin:planning-desk":     "One agent, an orchestrator, a board and a list, for planning work.",
	"builtin:content-studio":    "Writers and a designer, with a preview and notes.",
	"builtin:onboarding":        "Two agents learn a codebase and write down what they find.",
	"builtin:quality-gate":      "Testers, a reviewer and a security agent, with a checklist before release.",
	"builtin:performance-lab":   "Backend and a tester chase slow spots, with a board and benchmark notes.",
	"builtin:hackathon":         "Two teams of three, an orchestrator and a board.",
	"builtin:dependency-update": "One agent updates, one tester checks, with the steps on a list.",
	"builtin:data-crew":         "Two on the data, a reviewer, shared memory and notes.",
}

// moreLayouts are the later built-ins, as they do not fit on one screen with the first ones.
func moreLayouts(g func(key, name, role string) LayoutGroup, y, col, x1, x2, x3, row2 float64, def4 []string, orchAt func(x, yy float64) LayoutNode, wired func(groups ...string) []LayoutWire, editorW, editorH float64) []Layout {
	web := func(key string, x, yy float64) LayoutNode {
		return LayoutNode{Key: key, Kind: "web", X: x, Y: yy, W: 1280, H: 800, Data: map[string]any{"url": "http://localhost:5173/"}}
	}
	note := func(key, text string, x, yy float64) LayoutNode {
		return LayoutNode{Key: key, Kind: "note", X: x, Y: yy, W: 420, H: 300, Data: map[string]any{"text": text, "color": "yellow"}}
	}
	list := func(key, title string, x, yy float64, items ...string) LayoutNode {
		return LayoutNode{Key: key, Kind: "checklist", X: x, Y: yy, W: 420, H: 440, Data: checklistData(title, items...)}
	}
	board := func(key, title string, x, yy float64, cols ...string) LayoutNode {
		return LayoutNode{Key: key, Kind: "kanban", X: x, Y: yy, W: 900, H: 420, Data: kanbanData(title, cols...)}
	}
	editor := func(x, yy float64) LayoutNode {
		return LayoutNode{Key: "e1", Kind: KindEditor, X: x, Y: yy, W: editorW, H: editorH, Data: map[string]any{}}
	}
	return []Layout{
		{ID: "builtin:two-duos", Name: "Two duos",
			Lanes:  slices.Concat(gridLanes("a", 2, 2, 24, y, "", "", "g1"), gridLanes("b", 2, 2, 24+x2, y, "", "", "g2")),
			Groups: []LayoutGroup{g("g1", "Pair A", "general"), g("g2", "Pair B", "general")}},
		{ID: "builtin:octet", Name: "Octet", Lanes: gridLanes("a", 8, 4, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Octet", "general")}},
		{ID: "builtin:writer-checker", Name: "Writer & checker",
			Lanes:  slices.Concat(gridLanes("w", 1, 1, 24, y, "", "general", "g1"), gridLanes("c", 1, 1, 24+x1, y, "", "reviewers", "g2")),
			Groups: []LayoutGroup{g("g1", "Writer", "general"), g("g2", "Checker", "reviewers")}},
		{ID: "builtin:relay", Name: "Relay",
			Lanes:  slices.Concat(gridLanes("b", 1, 1, 24, y, "", "general", "g1"), gridLanes("t", 1, 1, 24+x1, y, "", "testers", "g2"), gridLanes("r", 1, 1, 24+2*x1, y, "", "reviewers", "g3")),
			Groups: []LayoutGroup{g("g1", "Builder", "general"), g("g2", "Tester", "testers"), g("g3", "Reviewer", "reviewers")},
			Wires:  []LayoutWire{{From: "g1", To: "g2", Kind: "forward"}, {From: "g2", To: "g3", Kind: "forward"}}},
		{ID: "builtin:editor-agent", Name: "Editor & agent", Lanes: gridLanes("a", 1, 1, 24+editorW+120, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Agent", "general")},
			Nodes: []LayoutNode{editor(24, y)}},
		{ID: "builtin:code-crew", Name: "Code & crew", Lanes: gridLanes("a", 4, 2, 24+editorW+120, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Crew", "general")},
			Nodes: []LayoutNode{editor(24, y), orchAt(24+editorW+120+x2, y)},
			Wires: wired("g1")},
		{ID: "builtin:laravel-crew", Name: "Laravel crew",
			Lanes:  slices.Concat(gridLanes("b", 2, 2, 24, y, "", "backend", "g1"), gridLanes("f", 1, 1, 24+x2, y, "", "frontend", "g2"), gridLanes("t", 1, 1, 24+x2+x1, y, "", "testers", "g3")),
			Groups: []LayoutGroup{g("g1", "Backend", "backend"), g("g2", "Blade", "frontend"), g("g3", "Tests", "testers")},
			Nodes:  []LayoutNode{editor(24, row2), board("k1", "Tasks", 24+editorW+120, row2, def4...)}},
		{ID: "builtin:api-crew", Name: "API crew",
			Lanes:  slices.Concat(gridLanes("b", 2, 2, 24, y, "", "backend", "g1"), gridLanes("t", 1, 1, 24, row2, "", "testers", "g2"), gridLanes("d", 1, 1, 24+x1, row2, "", "docs", "g3")),
			Groups: []LayoutGroup{g("g1", "Backend", "backend"), g("g2", "Testers", "testers"), g("g3", "Docs", "docs")},
			Nodes:  []LayoutNode{orchAt(x2+x1-col, y), {Key: "k1", Kind: "kanban", X: x2 + x1 - col, Y: y + 560, W: 900, H: 420, Data: kanbanData("Endpoints", "Design", "Build", "Test", "Documented")}},
			Wires:  wired("g1", "g2", "g3")},
		{ID: "builtin:app-team", Name: "App team",
			Lanes:  slices.Concat(gridLanes("f", 2, 2, 24, y, "", "frontend", "g1"), gridLanes("b", 1, 1, 24+x2, y, "", "backend", "g2"), gridLanes("t", 1, 1, 24+x2+x1, y, "", "testers", "g3")),
			Groups: []LayoutGroup{g("g1", "Frontend", "frontend"), g("g2", "Backend", "backend"), g("g3", "Testers", "testers")},
			Nodes:  []LayoutNode{web("w1", 24, row2)}},
		{ID: "builtin:ui-lab", Name: "UI lab",
			Lanes:  slices.Concat(gridLanes("f", 2, 2, 24, y, "", "frontend", "g1"), gridLanes("d", 1, 1, 24+x2, y, "", "design", "g2")),
			Groups: []LayoutGroup{g("g1", "Frontend", "frontend"), g("g2", "Design", "design")},
			Nodes:  []LayoutNode{web("w1", 24, row2)}},
		{ID: "builtin:refactor-squad", Name: "Refactor squad",
			Lanes:  slices.Concat(gridLanes("a", 3, 3, 24, y, "", "general", "g1"), gridLanes("r", 1, 1, 24, row2, "", "reviewers", "g2")),
			Groups: []LayoutGroup{g("g1", "Refactor", "general"), g("g2", "Review", "reviewers")},
			Nodes:  []LayoutNode{orchAt(x3, y), list("c1", "Refactor", x3, y+560, "Tests pass before starting", "Small steps, one change each", "Tests pass after each step", "Nothing else changed by accident")},
			Wires:  wired("g1", "g2")},
		{ID: "builtin:migration-desk", Name: "Migration desk",
			Lanes:  slices.Concat(gridLanes("b", 2, 2, 24, y, "", "backend", "g1"), gridLanes("t", 1, 1, 24, row2, "", "testers", "g2")),
			Groups: []LayoutGroup{g("g1", "Backend", "backend"), g("g2", "Testers", "testers")},
			Nodes:  []LayoutNode{list("c1", "Migration", x2, y, "Backup first", "Try it on a copy", "Rollback ready", "Run it", "Check the data")}},
		{ID: "builtin:incident-room", Name: "Incident room",
			Lanes:  slices.Concat(gridLanes("a", 2, 2, 24, y, "", "general", "g1"), gridLanes("x", 1, 1, 24, row2, "", "backend", "g2")),
			Groups: []LayoutGroup{g("g1", "Responders", "general"), g("g2", "Fix", "backend")},
			Nodes:  []LayoutNode{note("n1", "Timeline", x2, y), list("c1", "Runbook", x2, y+340, "Confirm the impact", "Find the last change", "Stop the damage", "Fix it", "Write down what happened")}},
		{ID: "builtin:review-board", Name: "Review board",
			Lanes:  slices.Concat(gridLanes("r", 2, 2, 24, y, "", "reviewers", "g1"), gridLanes("s", 1, 1, 24, row2, "", "security", "g2")),
			Groups: []LayoutGroup{g("g1", "Reviewers", "reviewers"), g("g2", "Security", "security")},
			Nodes:  []LayoutNode{board("k1", "Reviews", x2, y, "To review", "In review", "Changes asked", "Approved")}},
		{ID: "builtin:planning-desk", Name: "Planning desk", Lanes: gridLanes("a", 1, 1, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Planner", "general")},
			Nodes: []LayoutNode{orchAt(x1, y), board("k1", "Plan", x1+460, y, "Ideas", "Next", "Doing", "Done"), list("c1", "Decisions", x1+460, y+460)},
			Wires: wired("g1")},
		{ID: "builtin:content-studio", Name: "Content studio",
			Lanes:  slices.Concat(gridLanes("d", 2, 2, 24, y, "", "docs", "g1"), gridLanes("v", 1, 1, 24+x2, y, "", "design", "g2")),
			Groups: []LayoutGroup{g("g1", "Writers", "docs"), g("g2", "Design", "design")},
			Nodes:  []LayoutNode{web("w1", 24, row2), note("n1", "Outline", 24+1280+120, row2)}},
		{ID: "builtin:onboarding", Name: "Onboarding", Lanes: gridLanes("a", 2, 2, 24, y, "", "", "g1"), Groups: []LayoutGroup{g("g1", "Explorers", "general")},
			Nodes: []LayoutNode{{Key: "m1", Kind: KindMemory, X: x2, Y: y, W: 320, H: 300, Data: map[string]any{"blocks": []any{}}},
				list("c1", "Learn this project", x2, y+340, "Run it", "Follow the main flow", "Find the tests", "Note the conventions")}},
		{ID: "builtin:quality-gate", Name: "Quality gate",
			Lanes:  slices.Concat(gridLanes("t", 2, 2, 24, y, "", "testers", "g1"), gridLanes("r", 1, 1, 24, row2, "", "reviewers", "g2"), gridLanes("s", 1, 1, 24+x1, row2, "", "security", "g3")),
			Groups: []LayoutGroup{g("g1", "Testers", "testers"), g("g2", "Review", "reviewers"), g("g3", "Security", "security")},
			Nodes:  []LayoutNode{orchAt(x2+x1-col, y), list("c1", "Before release", x2+x1-col, y+560, "Tests pass", "Code reviewed", "No secrets in the diff", "Docs updated")},
			Wires:  wired("g1", "g2", "g3")},
		{ID: "builtin:performance-lab", Name: "Performance lab",
			Lanes:  slices.Concat(gridLanes("b", 2, 2, 24, y, "", "backend", "g1"), gridLanes("t", 1, 1, 24, row2, "", "testers", "g2")),
			Groups: []LayoutGroup{g("g1", "Backend", "backend"), g("g2", "Benchmarks", "testers")},
			Nodes:  []LayoutNode{board("k1", "Slow spots", x2, y, "Suspected", "Measured", "Fixing", "Faster"), note("n1", "Benchmarks", x2, y+460)}},
		{ID: "builtin:hackathon", Name: "Hackathon",
			Lanes:  slices.Concat(gridLanes("a", 3, 3, 24, y, "", "", "g1"), gridLanes("b", 3, 3, 24, row2, "", "", "g2")),
			Groups: []LayoutGroup{g("g1", "Team A", "general"), g("g2", "Team B", "general")},
			Nodes:  []LayoutNode{orchAt(x3, y), board("k1", "Tasks", x3, y+560, def4...)},
			Wires:  wired("g1", "g2")},
		{ID: "builtin:dependency-update", Name: "Dependency update",
			Lanes:  slices.Concat(gridLanes("a", 1, 1, 24, y, "", "general", "g1"), gridLanes("t", 1, 1, 24+x1, y, "", "testers", "g2")),
			Groups: []LayoutGroup{g("g1", "Update", "general"), g("g2", "Test", "testers")},
			Nodes:  []LayoutNode{list("c1", "Update", 24+2*x1, y, "Read the changelogs", "One update at a time", "Tests pass after each", "Lock file committed")}},
		{ID: "builtin:data-crew", Name: "Data crew",
			Lanes:  slices.Concat(gridLanes("b", 2, 2, 24, y, "", "backend", "g1"), gridLanes("r", 1, 1, 24, row2, "", "reviewers", "g2")),
			Groups: []LayoutGroup{g("g1", "Data", "backend"), g("g2", "Review", "reviewers")},
			Nodes: []LayoutNode{{Key: "m1", Kind: KindMemory, X: x2, Y: y, W: 320, H: 300, Data: map[string]any{"blocks": []any{}}},
				note("n1", "Findings", x2, y+340)}},
	}
}

func fill(l *Layout) {
	if l.Lanes == nil {
		l.Lanes = []Lane{}
	}
	if l.Groups == nil {
		l.Groups = []LayoutGroup{}
	}
	if l.Nodes == nil {
		l.Nodes = []LayoutNode{}
	}
	if l.Wires == nil {
		l.Wires = []LayoutWire{}
	}
}

// Layouts stores user layouts (layouts.json); built-ins live in the binary.
type Layouts struct {
	st   *store.Store
	mu   sync.Mutex
	list []Layout
}

func newLayouts(st *store.Store) *Layouts {
	l := &Layouts{st: st}
	_, _ = st.Load(layoutsFile, &l.list)
	return l
}

// All returns built-ins first, then saved layouts.
func (l *Layouts) All() []Layout {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := BuiltinLayouts()
	for _, x := range l.list {
		fill(&x)
		out = append(out, x)
	}
	return out
}

// Get finds a layout by id.
func (l *Layouts) Get(id string) (Layout, bool) {
	for _, x := range l.All() {
		if x.ID == id {
			return x, true
		}
	}
	return Layout{}, false
}

// Add stores a new layout ("Set N" when unnamed).
func (l *Layouts) Add(x Layout, now time.Time) (Layout, error) {
	if err := validateLayout(&x); err != nil {
		return Layout{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if x.Name == "" {
		taken := map[string]bool{}
		for _, y := range l.list {
			taken[y.Name] = true
		}
		for n := len(l.list) + 1; ; n++ {
			if x.Name = fmt.Sprintf("Set %d", n); !taken[x.Name] {
				break
			}
		}
	}
	t := now.UTC()
	x.ID, x.Builtin, x.CreatedAt = auth.RandomID("ly_"), false, &t
	l.list = append(l.list, x)
	return x, l.st.Save(layoutsFile, l.list)
}

// Rename / Delete saved layouts.
func (l *Layouts) Rename(id, name string) (Layout, error) {
	if strings.HasPrefix(id, "builtin:") {
		return Layout{}, ErrLayoutBuiltin
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 40 {
		return Layout{}, fmt.Errorf("%w: name 1–40 characters", ErrBadLayout)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.list {
		if l.list[i].ID == id {
			l.list[i].Name = name
			return l.list[i], l.st.Save(layoutsFile, l.list)
		}
	}
	return Layout{}, ErrLayoutNotFound
}

func (l *Layouts) Delete(id string) error {
	if strings.HasPrefix(id, "builtin:") {
		return ErrLayoutBuiltin
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.list {
		if l.list[i].ID == id {
			l.list = slices.Delete(l.list, i, i+1)
			return l.st.Save(layoutsFile, l.list)
		}
	}
	return ErrLayoutNotFound
}

func validateLayout(x *Layout) error {
	x.Name = strings.TrimSpace(x.Name)
	if len([]rune(x.Name)) > 40 {
		return fmt.Errorf("%w: name at most 40 characters", ErrBadLayout)
	}
	if len(x.Lanes) > 12 || len(x.Nodes) > 40 || len(x.Groups) > 8 {
		return fmt.Errorf("%w: at most 12 lanes, 8 groups, 40 nodes", ErrBadLayout)
	}
	keys := map[string]bool{}
	for _, k := range x.Groups {
		keys[k.Key] = true
	}
	for _, ln := range x.Lanes {
		keys[ln.Key] = true
	}
	for _, n := range x.Nodes {
		if !layoutNodeKinds[n.Kind] {
			return fmt.Errorf("%w: kind %q", ErrBadLayout, n.Kind)
		}
		keys[n.Key] = true
	}
	for _, w := range x.Wires {
		if !keys[w.From] || !keys[w.To] || (w.Kind != "wire" && w.Kind != "forward") {
			return fmt.Errorf("%w: wire %s→%s", ErrBadLayout, w.From, w.To)
		}
	}
	fill(x)
	return nil
}

// localhostURL keeps only http(s) URLs to localhost / 127.0.0.1.
func localhostURL(u string) string {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") {
		return ""
	}
	if h := p.Hostname(); h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return p.String()
	}
	return ""
}

// LayoutFromBoard saves a layout from board nodes (nodeIDs, or all): kind,
// geometry, titles/text/colors, lane agents, groups, orchestrators,
// wires/forwards, kanban column titles and localhost web tiles. Drawings,
// ink, images, files, card contents and external URLs are dropped.
func LayoutFromBoard(b board.Board, nodeIDs []string, sessions map[string]session.Info) Layout {
	sel := map[string]bool{}
	for _, id := range nodeIDs {
		sel[id] = true
	}
	pick := func(n *board.Node) bool { return len(sel) == 0 || sel[n.ID] }
	var x Layout
	keyOf := map[string]string{}
	minX, minY := 1e9, 1e9
	for _, n := range b.Nodes {
		if pick(n) {
			minX, minY = min(minX, n.X), min(minY, n.Y)
		}
	}
	groupOfTerm := map[string]string{}
	gi, li, ni := 0, 0, 0
	for _, n := range b.Nodes {
		if !pick(n) || n.Kind != KindGroup {
			continue
		}
		gi++
		key := fmt.Sprintf("g%d", gi)
		keyOf[n.ID] = key
		g := GroupOf(n)
		x.Groups = append(x.Groups, LayoutGroup{Key: key, Name: g.Name, Role: g.Role, Color: g.Color})
		for _, m := range g.Members {
			groupOfTerm[m] = key
		}
	}
	for _, n := range b.Nodes {
		if !pick(n) {
			continue
		}
		switch n.Kind {
		case board.KindTerminal:
			li++
			key := fmt.Sprintf("l%d", li)
			keyOf[n.ID] = key
			ln := Lane{Key: key, Group: groupOfTerm[n.ID], X: n.X - minX, Y: n.Y - minY, W: n.W, H: n.H}
			if in, ok := sessions[n.SessionID()]; ok {
				ln.Agent = in.Agent
			}
			x.Lanes = append(x.Lanes, ln)
		case KindOrchestrator, KindMemory, KindEditor, "kanban", "note", "text", "checklist", "web", "calendar", "uptime":
			data := map[string]any{}
			_ = json.Unmarshal(n.Data, &data)
			keep := map[string]any{}
			kind := n.Kind
			switch n.Kind {
			case "kanban":
				keep["title"] = data["title"]
				var cols []any
				if cs, ok := data["columns"].([]any); ok {
					for _, c := range cs {
						if cm, ok := c.(map[string]any); ok {
							cols = append(cols, map[string]any{"id": cm["id"], "title": cm["title"], "color": cm["color"], "cards": []any{}})
						}
					}
				}
				keep["columns"] = cols
			case "checklist", "calendar":
				keep["title"] = data["title"]
				keep["items"] = []any{}
				if n.Kind == "calendar" {
					kind = "note" // calendar content is dropped; keep a titled note
					keep = map[string]any{"text": data["title"]}
				}
			case "web":
				u, _ := data["url"].(string)
				if u = localhostURL(u); u == "" {
					continue
				}
				keep["url"] = u
			case "memory":
				for _, k := range []string{"scope", "blocks", "manual"} {
					if v, ok := data[k]; ok {
						keep[k] = v
					}
				}
			case KindEditor: // what it had open is not part of a layout
			default:
				for _, k := range []string{"title", "text", "color", "font", "size", "align"} {
					if v, ok := data[k]; ok {
						keep[k] = v
					}
				}
			}
			ni++
			key := fmt.Sprintf("n%d", ni)
			keyOf[n.ID] = key
			x.Nodes = append(x.Nodes, LayoutNode{Key: key, Kind: kind, X: n.X - minX, Y: n.Y - minY, W: n.W, H: n.H, Data: keep})
		}
	}
	for _, e := range b.Edges {
		if e.Kind != EdgeWire && e.Kind != EdgeForward {
			continue
		}
		f, t := keyOf[e.Source], keyOf[e.Target]
		if f == "" || t == "" {
			continue
		}
		w := LayoutWire{From: f, To: t, Kind: e.Kind}
		if e.Kind == EdgeForward {
			fd := ForwardOf(e)
			w.Data = map[string]any{"mode": fd.Mode, "trigger": fd.Trigger}
		}
		x.Wires = append(x.Wires, w)
	}
	fill(&x)
	return x
}

// LaneOverride is a per-lane deploy override.
type LaneOverride struct {
	Agent     string `json:"agent,omitempty"`
	MachineID string `json:"machineId,omitempty"`
	ProfileID string `json:"profileId,omitempty"`
	// ProviderID runs the lane on an API provider instead of a login.
	ProviderID string  `json:"providerId,omitempty"`
	Yolo       *bool   `json:"yolo,omitempty"`
	Model      *string `json:"model,omitempty"`
	Effort     *string `json:"effort,omitempty"`
}

// DeployRequest is POST /api/workspaces/{id}/deploy.
type DeployRequest struct {
	LayoutID      string                  `json:"layoutId"`
	At            *board.Rect             `json:"at"`
	Fill          *struct{ Agent string } `json:"fill"`
	LaneOverrides map[string]LaneOverride `json:"laneOverrides"`
	// GroupSizes sets how many consoles a group has, by group key (Layout.Sized).
	GroupSizes map[string]int `json:"groupSizes"`
}

// DeployResult is the 201 body.
type DeployResult struct {
	Sessions []session.Info `json:"sessions"`
	NodeIDs  []string       `json:"nodeIds"`
}

var _ = project.MaxName
