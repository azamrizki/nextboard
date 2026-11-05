package studio

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func dn(id, label, note, shape string) DNode {
	return DNode{ID: id, Label: label, Note: note, Shape: shape}
}
func de(from, to, label string) DEdge { return DEdge{From: from, To: to, Label: label} }

// demoDiagram is the kind of architecture a runner writes: long labels, notes,
// a decision, and arrows back to the top across everything between.
func demoDiagram() *Diagram {
	return &Diagram{Title: "Demo Dashboard Kit: system architecture", Direction: "TB", Nodes: []DNode{
		dn("spa", "Vue 3 SPA", "Inertia v3, TypeScript, Pinia stores, vue-i18n, Vueform", "round"),
		dn("mw", "HandleInertiaRequests, EnforceWebConfig,", "SetLocale, security headers", "box"),
		dn("check", "Request allowed?", "Web Config rules, auth, 2FA and can: gates", "diamond"),
		dn("app", "App controllers", "Dashboard, Settings, Notifications, Changelog, Api", "box"),
		dn("mod", "Auth, Operator, Role, Permission, Module, Log,", "Media, Backup, Workflow, Scheduler", "box"),
		dn("svc", "Domain services", "Actions, policies, DTOs, Spatie packages", "box"),
		dn("db", "PostgreSQL", "Users, roles, audit log, settings", "ellipse"),
	}, Edges: []DEdge{
		de("spa", "mw", "Inertia/HTTP requests"), de("mw", "check", "checks"), de("check", "app", "yes, app route"),
		de("check", "mod", "yes, module route"), de("check", "spa", "no, redirect or error"), de("app", "svc", ""),
		de("mod", "svc", ""), de("app", "spa", "updates UI"), de("svc", "db", "Eloquent"),
	}}
}

// curvePoints is a line as the tile draws it, finer than the layout looks at it.
func curvePoints(e edgeOut) []pt {
	var out []pt
	for i := 0; i <= 96; i++ {
		out = append(out, curveAt(e.from, e.mid, e.to, float64(i)/96))
	}
	return out
}

// problems counts what makes a diagram hard to read.
func problems(d *Diagram, l laid) (crossings, labelOnFrame, labelOnLabel int) {
	for k, e := range l.edges {
		pts := curvePoints(e)
		for i, n := range l.nodes {
			if d.Nodes[i].ID == d.Edges[k].From || d.Nodes[i].ID == d.Edges[k].To {
				continue
			}
			if n.touches(pts, 0) {
				crossings++
			}
		}
		if e.labelBox == nil {
			continue
		}
		for _, n := range l.nodes {
			if e.labelBox.b.overlaps(n) {
				labelOnFrame++
			}
		}
		for _, o := range l.edges[k+1:] {
			if o.labelBox != nil && e.labelBox.b.overlaps(o.labelBox.b) {
				labelOnLabel++
			}
		}
	}
	return
}

// An arrow ends at the outline of its tile, on the side it comes from: it does not run
// through the tile first.
func ownCrossings(d *Diagram, l laid) int {
	n := 0
	for k, e := range l.edges {
		if d.Edges[k].From == d.Edges[k].To {
			continue
		}
		for i, nd := range d.Nodes {
			if (nd.ID == d.Edges[k].From || nd.ID == d.Edges[k].To) && crossesOwn(e.from, e.mid, e.to, l.nodes[i], nd.Shape) {
				n++
			}
		}
	}
	return n
}

// A frame is as big as its text, whatever the shape: lines at the width the shape
// gives them (.shape-label) fit its height.
func TestDiagramFramesHoldTheirText(t *testing.T) {
	long := "WWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWW"
	note := "A_note_with_one_long_word_that_cannot_wrap_anywhere_at_all_because_no_space_at_all"
	for _, shape := range []string{"box", "round", "ellipse", "diamond"} {
		for _, c := range []DNode{
			{Label: "OK"},
			{Label: "Request allowed?", Note: "Web Config rules, auth, 2FA and can: gates"},
			{Label: "Auth, Operator, Role, Permission, Module, Log,", Note: "Media, Backup, Workflow, Scheduler and everything else"},
			{Label: long, Note: note},
			{Label: "数据库服务器和所有的缓存层都在这里运行", Note: "这个说明足够长所以它一定会换行显示在框里面"},
		} {
			c.Shape = shape
			w, h := nodeSize(c)
			innerW, innerH := w-24, h-24
			switch shape {
			case "diamond":
				innerW, innerH = w/2, h/2
			case "ellipse":
				innerW, innerH = w*0.7, h*0.7
			}
			lines := nodeLines(c, innerW)
			if need := float64(len(lines)) * lineH; need > innerH {
				t.Errorf("%s %q: %d lines need %.0f, the %.0f×%.0f frame gives %.0f", shape, c.Label, len(lines), need, w, h, innerH)
			}
			for _, l := range lines {
				if textW(l) > innerW {
					t.Errorf("%s: line %q is %.0f wide, the frame gives %.0f", shape, l, textW(l), innerW)
				}
			}
		}
	}
}

func TestWrapText(t *testing.T) {
	for _, c := range []struct {
		text  string
		limit float64
		want  []string
	}{
		{"one two", 1000, []string{"one two"}},
		{"one two", 40, []string{"one", "two"}},
		{"first\nsecond", 1000, []string{"first", "second"}},
		{"aaaaaa", 33, []string{"aaa", "aaa"}},
		{"", 100, []string{""}},
	} {
		if got := wrapText(c.text, c.limit); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("wrap(%q, %.0f) = %q", c.text, c.limit, got)
		}
	}
}

// Arrows go around what is not theirs, and labels sit where they can be read.
func TestDiagramStaysReadable(t *testing.T) {
	d := demoDiagram()
	l := layoutDiagram(d, "ink", Look{})
	if c, f, o := problems(d, l); c != 0 || f != 0 || o != 0 {
		t.Fatalf("%d arrows over a frame, %d labels on a frame, %d labels on a label", c, f, o)
	}
	if n := ownCrossings(d, l); n != 0 {
		t.Fatalf("%d arrows run through their own tile", n)
	}
	for k, e := range l.edges {
		if e.labelBox == nil {
			continue
		}
		for _, o := range l.edges {
			if e.labelBox.b.touches(curvePoints(o), 0) {
				t.Errorf("arrow %s runs over the label of %s", o.key, l.edges[k].key)
			}
		}
	}
	// the arrow back from the decision does not cut through the box between
	mw := l.nodes[1]
	for k, e := range d.Edges {
		if e.From == "check" && e.To == "spa" && mw.touches(curvePoints(l.edges[k]), clearance/2) {
			t.Fatal("the way back runs through the box between")
		}
	}
}

// Two arrows between the same tiles lie side by side, each with its own line.
func TestDiagramParallelArrows(t *testing.T) {
	d := &Diagram{Direction: "TB", Nodes: []DNode{dn("a", "Draft", "", "box"), dn("b", "Review", "", "box")},
		Edges: []DEdge{de("a", "b", "submit"), de("b", "a", "changes"), de("a", "b", "autosave")}}
	l := layoutDiagram(d, "ink", Look{})
	for i, e := range l.edges {
		for _, o := range l.edges[i+1:] {
			if math.Hypot(e.mid.x-o.mid.x, e.mid.y-o.mid.y) < 20 {
				t.Fatalf("%s and %s run together: %+v %+v", e.key, o.key, e.mid, o.mid)
			}
		}
	}
	if _, f, o := problems(d, l); f != 0 || o != 0 {
		t.Fatalf("%d labels on a frame, %d labels on a label", f, o)
	}
}

// Nothing lands left of or above the requested spot; a bend may not push it there.
func TestDiagramStartsAtTheGivenSpot(t *testing.T) {
	ops := Land(demoDiagram(), "ink", "dg_x", 500, 700, Look{})
	title := false
	for _, op := range ops {
		n := op.Node
		if k := string(n.Data); strings.Contains(k, `"diagramKey":"node:`) != strings.Contains(k, `"backed":true`) {
			t.Fatalf("only nodes are backed: %s", k)
		}
		if n.X < 500 || n.Y < 700 {
			t.Fatalf("%s at (%.0f, %.0f)", n.Kind, n.X, n.Y)
		}
		var data map[string]any
		_ = json.Unmarshal(n.Data, &data)
		if data["diagramKey"] == "title" {
			title = n.X == 500 && n.Y == 700 && n.H >= titleBoxH
		}
		if n.Y < 700+titleH && data["diagramKey"] != "title" {
			t.Fatalf("%v above the top of the diagram under the title: y %.0f", data["diagramKey"], n.Y)
		}
	}
	if !title {
		t.Fatal("no title at the top left")
	}
}

func randomDiagram(r *rand.Rand, nodes, edges int) *Diagram {
	d := &Diagram{Direction: "TB"}
	if r.Intn(3) == 0 {
		d.Direction = "LR"
	}
	shapes := []string{"box", "round", "ellipse", "diamond"}
	for i := range nodes {
		note := ""
		if r.Intn(2) == 0 {
			note = "A note about what this part does in a few words"
		}
		d.Nodes = append(d.Nodes, dn(fmt.Sprintf("n%d", i), fmt.Sprintf("Component %d", i), note, shapes[r.Intn(4)]))
	}
	// mostly forward, some back, as an architecture is
	for len(d.Edges) < edges {
		a, b := r.Intn(nodes), r.Intn(nodes)
		if a == b {
			continue
		}
		if a > b && r.Intn(4) != 0 {
			a, b = b, a
		}
		label := ""
		if r.Intn(2) == 0 {
			label = fmt.Sprintf("step %d", len(d.Edges))
		}
		d.Edges = append(d.Edges, de(d.Nodes[a].ID, d.Nodes[b].ID, label))
	}
	return d
}

// Diagrams of the size runners are asked for (§6: up to 16 components), with far more
// long and backward arrows than an architecture has: frames are never written on. One
// curve can't always clear a dense row; such an arrow runs behind the frame, which is
// opaque (backed), but it stays the exception.
func TestDiagramsOfUsualSize(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	arrows, crossings, own := 0, 0, 0
	for range 60 {
		d := randomDiagram(r, 6+r.Intn(11), 8+r.Intn(16))
		l := layoutDiagram(d, "ink", Look{})
		c, f, o := problems(d, l)
		arrows += len(d.Edges)
		crossings += c
		if f != 0 {
			t.Fatalf("%d labels on a frame in %+v", f, d)
		}
		if o != 0 {
			t.Fatalf("%d labels on a label in %+v", o, d)
		}
		own += ownCrossings(d, l)
	}
	t.Logf("%d of %d arrows pass over a frame, %d through their own", crossings, arrows, own)
	if own != 0 {
		t.Fatalf("%d arrows run through their own tile", own)
	}
	if float64(crossings)/float64(arrows) > 0.06 {
		t.Fatalf("%d of %d arrows pass over a frame", crossings, arrows)
	}
}

// The limit of §6 lands without keeping anyone waiting.
func TestDiagramLargestIsQuick(t *testing.T) {
	d := randomDiagram(rand.New(rand.NewSource(3)), 40, 80)
	start := time.Now()
	if ops := Land(d, "ink", "dg_x", 0, 0, Look{}); len(ops) < 120 {
		t.Fatalf("%d ops", len(ops))
	}
	if took := time.Since(start); took > 2*time.Second*raceSlowdown {
		t.Fatalf("took %v", took)
	}
}

var wiring = Look{Arrows: "wiring", ArrowColor: "mono"}

// wireTrouble counts what a wiring layout must not do at all, and the labels that a wire
// runs under, which can only be kept rare.
func wireTrouble(t *testing.T, d *Diagram, l laid) (underLabel int) {
	t.Helper()
	for k, e := range l.edges {
		if len(e.route) < 2 {
			t.Fatalf("%s has no route", e.key)
		}
		if e.route[0] != e.from || e.route[len(e.route)-1] != e.to {
			t.Fatalf("%s: the ends of its route are not its ends", e.key)
		}
		for i := 1; i < len(e.route); i++ {
			if e.route[i].x != e.route[i-1].x && e.route[i].y != e.route[i-1].y {
				t.Fatalf("%s runs at a slant: %+v", e.key, e.route)
			}
		}
		for i, n := range l.nodes {
			if d.Nodes[i].ID != d.Edges[k].From && d.Nodes[i].ID != d.Edges[k].To && n.touches(e.route, 0) {
				t.Fatalf("%s runs through %s", e.key, d.Nodes[i].ID)
			}
		}
		if e.labelBox == nil {
			continue
		}
		for _, n := range l.nodes {
			if e.labelBox.b.overlaps(n) {
				t.Fatalf("the label of %s lies on a frame", e.key)
			}
		}
		for _, o := range l.edges[k+1:] {
			if o.labelBox != nil && e.labelBox.b.overlaps(o.labelBox.b) {
				t.Fatalf("the labels of %s and %s overlap", e.key, o.key)
			}
		}
		for _, o := range l.edges {
			if e.labelBox.b.touches(o.route, 0) {
				underLabel++
				break
			}
		}
	}
	return underLabel
}

// labelsFarFromWire counts the labels further from their own wire than a label can be read as its label.
func labelsFarFromWire(l laid) int {
	far := 0
	for _, e := range l.edges {
		if e.labelBox == nil {
			continue
		}
		b, best := e.labelBox.b, math.Inf(1)
		for i := 1; i < len(e.route); i++ {
			p, q := e.route[i-1], e.route[i]
			gx := math.Max(0, math.Max(math.Min(p.x, q.x)-(b.x+b.w), b.x-math.Max(p.x, q.x)))
			gy := math.Max(0, math.Max(math.Min(p.y, q.y)-(b.y+b.h), b.y-math.Max(p.y, q.y)))
			best = math.Min(best, math.Hypot(gx, gy))
		}
		if best > 64 {
			far++
		}
	}
	return far
}

// Wires are straight stretches around what is not theirs, whatever the diagram. Labels stay
// off frames and each other, and a wire under a label or a label far from its wire stays the
// exception.
func TestWiresStayTidy(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	labels, under, far := 0, 0, 0
	for range 60 {
		d := randomDiagram(r, 6+r.Intn(11), 8+r.Intn(16))
		l := layoutDiagram(d, "ink", wiring)
		under += wireTrouble(t, d, l)
		far += labelsFarFromWire(l)
		for _, e := range d.Edges {
			if e.Label != "" {
				labels++
			}
		}
	}
	t.Logf("of %d labels, %d have a wire under them and %d are far from their own", labels, under, far)
	if float64(under)/float64(labels) > 0.03 {
		t.Fatalf("%d of %d labels have a wire under them", under, labels)
	}
	if float64(far)/float64(labels) > 0.03 {
		t.Fatalf("%d of %d labels are far from their wire", far, labels)
	}
	if under := wireTrouble(t, demoDiagram(), layoutDiagram(demoDiagram(), "ink", wiring)); under != 0 {
		t.Fatalf("%d labels of the usual architecture have a wire under them", under)
	}
}

// A straight run of tiles gets straight wires, not small jogs.
func TestWiresBetweenFacingTilesAreStraight(t *testing.T) {
	for _, dir := range []string{"TB", "LR"} {
		d := &Diagram{Direction: dir, Nodes: []DNode{dn("a", "One", "", "box"), dn("b", "Two", "with a note that makes it wider", "round"), dn("c", "Three", "", "ellipse"), dn("d", "Four", "", "diamond")},
			Edges: []DEdge{de("a", "b", ""), de("b", "c", ""), de("c", "d", "")}}
		for _, e := range layoutDiagram(d, "ink", wiring).edges {
			if len(e.route) != 2 {
				t.Fatalf("%s: %s bends: %+v", dir, e.key, e.route)
			}
		}
	}
}

// Wires from one source part ways at a dot that lies on them; other wires have none.
func TestWiresSplitAtADot(t *testing.T) {
	d := &Diagram{Direction: "TB", Nodes: []DNode{dn("a", "Source", "", "box"), dn("b", "Left", "", "box"), dn("c", "Right", "", "box"), dn("x", "Other", "", "box")},
		Edges: []DEdge{de("a", "b", "one"), de("a", "c", "two"), de("x", "b", "three")}}
	l := layoutDiagram(d, "ink", wiring)
	var dots []pt
	for _, e := range l.edges {
		dots = append(dots, e.dots...)
	}
	if len(dots) != 1 {
		t.Fatalf("dots: %+v", dots)
	}
	spot := box{x: dots[0].x, y: dots[0].y}
	if !spot.touches(l.edges[0].route, 0.5) || !spot.touches(l.edges[1].route, 0.5) || spot.touches(l.edges[2].route, 0.5) {
		t.Fatalf("the dot %+v is not where the two wires of a part ways: %+v %+v", dots[0], l.edges[0].route, l.edges[1].route)
	}
}

// Dashed and solid wires do not share a way, or one would look like the other.
func TestWiresOfOtherStylesDoNotShare(t *testing.T) {
	d := &Diagram{Direction: "TB", Nodes: []DNode{dn("a", "Source", "", "box"), dn("b", "Left", "", "box"), dn("c", "Right", "", "box")},
		Edges: []DEdge{{From: "a", To: "b", Style: "solid"}, {From: "a", To: "c", Style: "dashed"}}}
	l := layoutDiagram(d, "ink", wiring)
	if l.edges[0].route[0] == l.edges[1].route[0] {
		t.Fatalf("both leave at %+v", l.edges[0].route[0])
	}
	if len(l.edges[0].dots)+len(l.edges[1].dots) != 0 {
		t.Fatal("a dot between wires of another style")
	}
}

// A wire touches the outline of the shape it leaves and stops just before the one it reaches.
func TestWiresMeetTheOutline(t *testing.T) {
	for _, shape := range []string{"box", "round", "ellipse", "diamond"} {
		d := &Diagram{Direction: "TB", Nodes: []DNode{dn("a", "From", "", shape), dn("b", "One", "", shape), dn("c", "Two", "", shape)},
			Edges: []DEdge{de("a", "b", ""), de("a", "c", ""), de("b", "c", ""), de("c", "c", "")}}
		l := layoutDiagram(d, "ink", wiring)
		for k, e := range l.edges {
			inside := func(p pt, n box) bool {
				dx, dy := math.Abs(p.x-n.cx())/(n.w/2-wireOutline), math.Abs(p.y-n.cy())/(n.h/2-wireOutline)
				switch shape {
				case "ellipse":
					return dx*dx+dy*dy < 1
				case "diamond":
					return dx+dy < 1
				}
				return dx < 1 && dy < 1
			}
			var from, to box
			for i, nd := range d.Nodes {
				if nd.ID == d.Edges[k].From {
					from = l.nodes[i]
				}
				if nd.ID == d.Edges[k].To {
					to = l.nodes[i]
				}
			}
			if !inside(e.route[0], from) {
				t.Errorf("%s %s starts off its source: %+v, %+v", shape, e.key, e.route[0], from)
			}
			if inside(e.to, to) {
				t.Errorf("%s %s ends inside its target: %+v, %+v", shape, e.key, e.to, to)
			}
			if d := e.to; math.Abs(d.x-to.cx()) > to.w/2+4 || math.Abs(d.y-to.cy()) > to.h/2+4 {
				t.Errorf("%s %s ends far from its target: %+v, %+v", shape, e.key, e.to, to)
			}
		}
	}
}

// Arrow colour is a choice of its own: one ink, or one colour per source, whatever the shapes.
func TestArrowColours(t *testing.T) {
	d := &Diagram{Direction: "TB", Groups: []DGroup{{ID: "g", Label: "Group"}}, Nodes: []DNode{{ID: "a", Label: "A", Shape: "box", Group: "g"}, dn("b", "B", "", "box"), dn("c", "C", "", "box"), dn("e", "E", "", "box")},
		Edges: []DEdge{de("a", "b", ""), de("a", "c", ""), de("b", "c", ""), de("c", "e", "")}}
	colors := func(style string, look Look) []string {
		var out []string
		for _, e := range layoutDiagram(d, style, look).edges {
			out = append(out, e.color)
		}
		return out
	}
	for _, arrows := range []string{"curved", "wiring"} {
		for _, style := range []string{"ink", "color"} {
			for _, c := range colors(style, Look{Arrows: arrows, ArrowColor: "mono"}) {
				if c != inkColor {
					t.Fatalf("%s %s: black and white has %s", arrows, style, c)
				}
			}
		}
		c := colors("ink", Look{Arrows: arrows, ArrowColor: "color"})
		if c[0] != c[1] || c[0] == c[2] || c[2] == c[3] || c[0] == inkColor {
			t.Fatalf("%s: one colour per source: %v", arrows, c)
		}
		if c := colors("color", Look{Arrows: arrows, ArrowColor: "color"}); c[0] != groupColors[0] || c[2] == inkColor {
			t.Fatalf("%s: coloured shapes: an arrow takes the colour of its group: %v", arrows, c)
		}
	}
	// no choice made: as the shapes
	if c := colors("ink", Look{}); c[0] != inkColor {
		t.Fatalf("no choice, ink: %v", c)
	}
	if c := colors("color", Look{}); c[0] != groupColors[0] || c[2] != inkColor {
		t.Fatalf("no choice, colour: %v", c)
	}
}

// On the board a wire holds its route; `points` stays the line a client without routes draws.
func TestLandedWire(t *testing.T) {
	d := demoDiagram()
	lines := func(look Look) []map[string]any {
		var out []map[string]any
		for _, op := range Land(d, "ink", "dg_x", 500, 700, look) {
			if op.Node.Kind != "line" {
				continue
			}
			var data map[string]any
			if err := json.Unmarshal(op.Node.Data, &data); err != nil {
				t.Fatal(err)
			}
			data["x"], data["y"], data["w"], data["h"] = op.Node.X, op.Node.Y, op.Node.W, op.Node.H
			out = append(out, data)
		}
		return out
	}
	for _, data := range lines(Look{}) {
		if _, ok := data["route"]; ok || data["roughness"] != 1.0 {
			t.Fatalf("a curve with a route: %v", data)
		}
	}
	for _, data := range lines(wiring) {
		route, ok := data["route"].([]any)
		if !ok || len(route) < 2 || data["roughness"] != 0.0 || len(data["points"].([]any)) != 3 {
			t.Fatalf("a wire without its route: %v", data)
		}
		for _, p := range route {
			xy := p.([]any)
			if x, y := xy[0].(float64), xy[1].(float64); x < 0 || y < 0 || x > data["w"].(float64)+1 || y > data["h"].(float64)+1 {
				t.Fatalf("a route point outside its node: %v in %vx%v", xy, data["w"], data["h"])
			}
		}
		if !reflect.DeepEqual(data["points"].([]any)[0], route[0]) || !reflect.DeepEqual(data["points"].([]any)[2], route[len(route)-1]) {
			t.Fatalf("points and route end apart: %v", data)
		}
	}
}

// The limit of §6, in wires, lands without keeping anyone waiting.
func TestWiresLargestIsQuick(t *testing.T) {
	d := randomDiagram(rand.New(rand.NewSource(3)), 40, 80)
	start := time.Now()
	l := layoutDiagram(d, "ink", wiring)
	took := time.Since(start)
	wireTrouble(t, d, l)
	t.Logf("took %v", took)
	if took > 3*time.Second*raceSlowdown {
		t.Fatalf("took %v", took)
	}
}
