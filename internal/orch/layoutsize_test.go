package orch

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/azamrizki/nextboard/internal/board"
)

// landsClean checks a sized layout the way a board would show it: group frames around their
// members, and nothing lying on anything else.
func landsClean(t *testing.T, name string, l Layout) {
	t.Helper()
	type item struct {
		what string
		r    board.Rect
	}
	var items []item
	for _, g := range l.Groups {
		items = append(items, item{"group " + g.Key, laneFrame(lanesOf(l.Lanes, g.Key))})
	}
	for _, ln := range l.Lanes {
		if ln.Group == "" {
			items = append(items, item{"lane " + ln.Key, rectOf(ln.X, ln.Y, ln.W, ln.H)})
		}
	}
	for _, n := range l.Nodes {
		items = append(items, item{n.Kind + " " + n.Key, rectOf(n.X, n.Y, n.W, n.H)})
	}
	for i, a := range items {
		for _, b := range items[i+1:] {
			if rectsMeet(a.r, b.r) {
				t.Errorf("%s: %s %+v lies on %s %+v", name, a.what, a.r, b.what, b.r)
			}
		}
	}
	// and inside a group, no member lies on another
	for i, a := range l.Lanes {
		for _, b := range l.Lanes[i+1:] {
			if rectsMeet(rectOf(a.X, a.Y, a.W, a.H), rectOf(b.X, b.Y, b.W, b.H)) {
				t.Errorf("%s: lane %s lies on lane %s", name, a.Key, b.Key)
			}
		}
	}
}

// Every built-in, every group on its own at every size the limits allow, and all groups at once.
func TestEveryBuiltinAtEverySize(t *testing.T) {
	for _, l := range BuiltinLayouts() {
		landsClean(t, l.ID, l)
		for _, g := range l.Groups {
			others := len(l.Lanes) - len(lanesOf(l.Lanes, g.Key))
			for n := 0; n+others <= maxSpawn && n <= maxMembers; n++ {
				if n+others == 0 {
					continue
				}
				got, err := l.Sized(map[string]int{g.Key: n})
				if err != nil {
					t.Fatalf("%s %s=%d: %v", l.ID, g.Key, n, err)
				}
				name := fmt.Sprintf("%s %s=%d", l.ID, g.Key, n)
				landsClean(t, name, got)
				if have := len(lanesOf(got.Lanes, g.Key)); have != n {
					t.Errorf("%s: %d lanes", name, have)
				}
				if len(got.Lanes) != n+others {
					t.Errorf("%s: %d lanes in all", name, len(got.Lanes))
				}
				if len(got.Nodes) != len(l.Nodes) {
					t.Errorf("%s: %d cards of %d", name, len(got.Nodes), len(l.Nodes))
				}
				keys := map[string]bool{}
				for _, ln := range got.Lanes {
					if keys[ln.Key] {
						t.Errorf("%s: key %s twice", name, ln.Key)
					}
					keys[ln.Key] = true
				}
				c := got
				if err := validateLayout(&c); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}
		}
		for size := 1; size*len(l.Groups) <= maxSpawn && size <= maxMembers && len(l.Groups) > 0; size++ {
			sizes := map[string]int{}
			for _, g := range l.Groups {
				sizes[g.Key] = size
			}
			got, err := l.Sized(sizes)
			if err != nil {
				t.Fatalf("%s all=%d: %v", l.ID, size, err)
			}
			landsClean(t, fmt.Sprintf("%s all=%d", l.ID, size), got)
		}
	}
}

func builtin(t *testing.T, id string) Layout {
	t.Helper()
	i := slices.IndexFunc(BuiltinLayouts(), func(l Layout) bool { return l.ID == id })
	if i < 0 {
		t.Fatalf("no %s", id)
	}
	return BuiltinLayouts()[i]
}

func TestSizedShapes(t *testing.T) {
	cols := func(l Layout, group string) int {
		seen := map[float64]bool{}
		for _, ln := range lanesOf(l.Lanes, group) {
			seen[ln.X] = true
		}
		return len(seen)
	}
	duo := builtin(t, "builtin:duo")
	for n, want := range map[int]int{1: 1, 2: 2, 3: 3, 4: 2, 5: 3, 6: 3, 7: 4, 8: 4} {
		got, err := duo.Sized(map[string]int{"g1": n})
		if err != nil {
			t.Fatal(err)
		}
		if c := cols(got, "g1"); c != want {
			t.Errorf("%d lanes in %d columns, want %d", n, c, want)
		}
	}
	// a stack stays a stack, beside the editor
	pair := builtin(t, "builtin:code-pair")
	got, err := pair.Sized(map[string]int{"g1": 4})
	if err != nil {
		t.Fatal(err)
	}
	if c := cols(got, "g1"); c != 1 {
		t.Errorf("stack in %d columns", c)
	}
}

// What is known of a lane stays with it, and new lanes take after the last.
func TestSizedKeepsWhatLanesAre(t *testing.T) {
	br := builtin(t, "builtin:build-review")
	got, err := br.Sized(map[string]int{"g1": 3, "g2": 2})
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, ln := range got.Lanes {
		keys = append(keys, ln.Key)
		want := "general"
		if ln.Group == "g2" {
			want = "reviewers"
		}
		if ln.Role != want {
			t.Errorf("%s has role %q", ln.Key, ln.Role)
		}
	}
	if !slices.Equal(keys, []string{"b1", "b2", "b3", "r1", "r2"}) {
		t.Errorf("keys %v", keys)
	}
	// wires go to the group, so they hold whatever its size
	if len(got.Wires) != 2 {
		t.Errorf("wires %v", got.Wires)
	}
	// the template itself is untouched
	if again := builtin(t, "builtin:build-review"); len(again.Lanes) != 3 {
		t.Errorf("the template changed: %d lanes", len(again.Lanes))
	}
}

func TestSizedDropsAGroupAndItsWires(t *testing.T) {
	fs := builtin(t, "builtin:full-stack")
	got, err := fs.Sized(map[string]int{"g3": 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 3 || len(got.Lanes) != 6 {
		t.Fatalf("%d groups, %d lanes", len(got.Groups), len(got.Lanes))
	}
	for _, w := range got.Wires {
		if w.To == "g3" || w.From == "g3" {
			t.Errorf("wire left: %+v", w)
		}
	}
	// forward chain: the link to a dropped group goes, the rest holds
	relay := builtin(t, "builtin:relay")
	got, err = relay.Sized(map[string]int{"g2": 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Wires) != 0 {
		t.Errorf("wires %+v", got.Wires)
	}
}

func TestSizedNoChangeIsTheSameLayout(t *testing.T) {
	duo := builtin(t, "builtin:duo")
	for _, sizes := range []map[string]int{nil, {}, {"g1": 2}} {
		got, err := duo.Sized(sizes)
		if err != nil || len(got.Lanes) != 2 || got.Lanes[1].X != duo.Lanes[1].X {
			t.Fatalf("%v: %+v %v", sizes, got.Lanes, err)
		}
	}
}

func TestSizedRefuses(t *testing.T) {
	duo := builtin(t, "builtin:duo")
	two := builtin(t, "builtin:two-quartets")
	for name, tc := range map[string]struct {
		l     Layout
		sizes map[string]int
		want  error
	}{
		"unknown group":   {duo, map[string]int{"nope": 1}, ErrBadLayout},
		"negative":        {duo, map[string]int{"g1": -1}, ErrBadLayout},
		"over a group":    {duo, map[string]int{"g1": maxMembers + 1}, ErrTooMany},
		"over a deploy":   {two, map[string]int{"g1": 10, "g2": 10}, ErrTooMany},
		"nothing at all":  {duo, map[string]int{"g1": 0}, ErrBadLayout},
		"all groups gone": {two, map[string]int{"g1": 0, "g2": 0}, ErrBadLayout},
	} {
		if _, err := tc.l.Sized(tc.sizes); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A saved layout: lanes of any shape, loose lanes, and a note the owner put on a group on purpose.
func TestSizedSavedLayout(t *testing.T) {
	l := Layout{ID: "ly_x", Name: "Mine",
		Groups: []LayoutGroup{{Key: "g1", Name: "Team", Role: "general", Color: "sky"}},
		Lanes: []Lane{
			{Key: "l1", Group: "g1", X: 100, Y: 100, W: 500, H: 300},
			{Key: "l2", Group: "g1", X: 640, Y: 100, W: 500, H: 300},
			{Key: "l3", X: 1300, Y: 100, W: 500, H: 300},
		},
		Nodes: []LayoutNode{
			{Key: "n1", Kind: "note", X: 120, Y: 120, W: 200, H: 100},
			{Key: "n2", Kind: "note", X: 1300, Y: 500, W: 200, H: 100},
		},
		Wires: []LayoutWire{{From: "l1", To: "l3", Kind: "forward"}, {From: "l2", To: "l3", Kind: "forward"}},
	}
	got, err := l.Sized(map[string]int{"g1": 3})
	if err != nil {
		t.Fatal(err)
	}
	var loose Lane
	var added Lane
	for _, ln := range got.Lanes {
		switch ln.Key {
		case "l3":
			loose = ln
		case "l4":
			added = ln
		}
	}
	if added.W != 500 || added.H != 300 {
		t.Errorf("new lane %+v, keys %v", added, got.Lanes)
	}
	if loose.X < added.X+added.W {
		t.Errorf("the loose lane %+v lies on the group's new lane %+v", loose, added)
	}
	if got.Nodes[0].X != 120 {
		t.Errorf("the note on the group moved: %+v", got.Nodes[0])
	}
	// one lane back: its wire goes, the other stays
	got, err = l.Sized(map[string]int{"g1": 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Wires) != 1 || got.Wires[0].From != "l1" {
		t.Errorf("wires %+v", got.Wires)
	}
}

// Through the deploy itself: the sizes reach the board, group frames included.
func TestDeployWithGroupSizes(t *testing.T) {
	h, s := newFakeHub(t)
	_, wid := h.project("sized")
	res, err := s.Deploy(wid, DeployRequest{LayoutID: "builtin:full-stack", GroupSizes: map[string]int{"g1": 3, "g3": 0},
		LaneOverrides: map[string]LaneOverride{"f3": {Agent: "shell"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 7 {
		t.Fatalf("%d sessions", len(res.Sessions))
	}
	b, _ := h.Board(wid)
	groups := 0
	inGroup := map[string]string{}
	for _, n := range b.Nodes {
		if n.Kind == KindGroup {
			groups++
			g := GroupOf(n)
			for _, m := range g.Members {
				inGroup[m] = n.ID
			}
			if g.Name == "Frontend" && len(g.Members) != 3 {
				t.Errorf("Frontend has %d members", len(g.Members))
			}
			if g.Name == "Testers" {
				t.Error("a group of 0 was made")
			}
		}
	}
	if groups != 3 {
		t.Fatalf("%d groups", groups)
	}
	for i, a := range b.Nodes {
		for _, c := range b.Nodes[i+1:] {
			if inGroup[a.ID] == c.ID || inGroup[c.ID] == a.ID {
				continue
			}
			if a.X < c.X+c.W && c.X < a.X+a.W && a.Y < c.Y+c.H && c.Y < a.Y+a.H {
				t.Errorf("%s covers %s", a.Kind, c.Kind)
			}
		}
	}
	// an override reaches a lane that only exists at this size
	agents := []string{}
	for _, in := range res.Sessions {
		agents = append(agents, in.Agent)
	}
	if !slices.Contains(agents, "shell") {
		t.Errorf("agents %v", agents)
	}
	if _, err := s.Deploy(wid, DeployRequest{LayoutID: "builtin:duo", GroupSizes: map[string]int{"g9": 1}}); !errors.Is(err, ErrBadLayout) {
		t.Errorf("unknown group: %v", err)
	}
}
