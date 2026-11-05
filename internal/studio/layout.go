package studio

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/azamrizki/nextboard/internal/board"
)

// Landing a diagram (docs/API-studio.md §6) turns nb.diagram/v1 into loose
// board nodes in one ops batch: a layered layout (longest-path layers,
// barycenter ordering), `shape` nodes with text, `line` nodes with an arrow
// plus a `text` node per edge label, dashed group zones behind when there
// are 2 or more non-overlapping groups, and a title. Every node carries
// data.diagramId and data.diagramKey. This hub-side landing serves the Android
// client, the command bar and tests; the web client has the same layout in
// web/src/studio/diagrams/layout.ts, so a diagram looks the same either way.
//
// What goes on the board must stay readable: a frame is sized from its wrapped
// text, an arrow bends around tiles that are not its own, and a label sits
// beside its arrow where nothing else is.
//
// Look chooses how arrows are drawn: curves, or wires in straight stretches
// (wires.go), in one ink or in colour.

// Look is how the arrows of a landing look. No ArrowColor: they follow the shapes,
// as before there was a choice.
type Look struct {
	Arrows     string // "curved" (default) | "wiring"
	ArrowColor string // "" | "mono" | "color"
}

// Palette (web/src/board/palette.ts INK_COLORS).
const inkColor = "#e6eaf2"

var groupColors = []string{"#6e95ff", "#b49cff", "#4fd6e8", "#2fdc8a", "#f2b27a", "#ff5c77", "#8a93a6", "#6e95ff"}

// wireColors are colourful arrows, one per source, so that neighbours differ
// (web/src/board/palette.ts DIAGRAM_WIRE_COLORS).
var wireColors = []string{"#6e95ff", "#f2b27a", "#2fdc8a", "#ff5c77", "#4fd6e8", "#b49cff"}

const (
	gapMain        = 100.0 // between layers
	gapCross       = 64.0  // between nodes of a layer
	zonePad        = 24.0
	zoneHead       = 18.0
	titleH         = 68.0
	titleBoxH      = 56.0 // .font-hand makes "l" 35 px, at line-height 1.3 + padding
	nodeMinW       = 130.0
	nodeMinH       = 64.0
	lineH          = 31.25 // .font-hand makes "m" 25 px, at line-height 1.25
	rectPad        = 16.0  // .shape-label inset 12 + room for a rough outline
	rectTextMax    = 300.0
	ellipseTextMax = 250.0
	labelH         = 32.0 // .font-hand makes "s" 17.5 px, at line-height 1.3 + padding
	labelScale     = 0.72 // a label's 17.5 px against a node's 25 px
	clearance      = 12.0 // what an arrow keeps from a tile that is not its own
	arrowGap       = 6.0  // between an arrow's tip and the outline it points at
	bendStep       = 36.0
	bendTries      = 60
	pairBend       = 30.0
	curveSteps     = 24
	maxTracks      = 14 // the room a gap between layers makes for wires; more find their way around
)

var labelDistances = []float64{8, 30, 56, 84}

var (
	wireLabelDistances = []float64{6, 14, 24, 40, 64, 96, 140, 200} // the far ones only where nothing nearer is free
	wireLabelSpots     = []float64{0.5, 0.35, 0.65, 0.2, 0.8, 0.1, 0.9}
)

// wireLabelNear: a free spot this close ends the search; a further one has to beat every other stretch.
const wireLabelNear = 24.0

// bendAlong moves the top of a bend along the arrow, for one that has to clear
// something near one of its ends.
var bendAlong = []float64{0, -0.3, 0.3}

var diamondTextMax = []float64{140, 190, 240, 300, 360}

type box struct{ x, y, w, h float64 }

type pt struct{ x, y float64 }

func (b box) cx() float64 { return b.x + b.w/2 }
func (b box) cy() float64 { return b.y + b.h/2 }

func (b box) overlaps(o box) bool {
	return b.x < o.x+o.w && o.x < b.x+b.w && b.y < o.y+o.h && o.y < b.y+b.h
}

// touches reports whether a line through pts touches b grown by margin. Every
// segment, so a long curve can't step over it.
func (b box) touches(pts []pt, margin float64) bool {
	x0, y0, x1, y1 := b.x-margin, b.y-margin, b.x+b.w+margin, b.y+b.h+margin
	for i := 1; i < len(pts); i++ {
		a := pts[i-1]
		dx, dy := pts[i].x-a.x, pts[i].y-a.y
		t0, t1 := 0.0, 1.0
		inside := true
		for _, c := range [][2]float64{{-dx, a.x - x0}, {dx, x1 - a.x}, {-dy, a.y - y0}, {dy, y1 - a.y}} {
			p, q := c[0], c[1]
			switch {
			case p == 0:
				if q < 0 {
					inside = false
				}
			case p < 0:
				t0 = math.Max(t0, q/p)
			default:
				t1 = math.Min(t1, q/p)
			}
		}
		if inside && t0 <= t1 {
			return true
		}
	}
	return false
}

// charW is the width of one character of the hand font at 25 px, measured and
// on the generous side.
func charW(c rune) float64 {
	switch {
	case c > 0x2e7f: // wide scripts
		return 25
	case strings.ContainsRune(" il.,:;'!", c):
		return 6.5
	case strings.ContainsRune("mwMW@%&#", c):
		return 16
	case c >= 'A' && c <= 'Z':
		return 13.5
	}
	return 11
}

func textW(s string) float64 {
	w := 0.0
	for _, c := range s {
		w += charW(c)
	}
	return w
}

// wrapText gives the lines of text in limit px: words stay whole unless one
// alone is too long, as in the labels' CSS.
func wrapText(text string, limit float64) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		cur, curW := "", 0.0
		for _, word := range strings.Split(para, " ") {
			w, space := textW(word), 0.0
			if cur != "" {
				space = charW(' ')
			}
			if curW+space+w <= limit {
				if cur != "" {
					cur += " "
				}
				cur += word
				curW += space + w
				continue
			}
			if cur != "" {
				lines = append(lines, cur)
			}
			cur, curW = "", 0
			for _, c := range word {
				if curW+charW(c) > limit {
					lines = append(lines, cur)
					cur, curW = "", 0
				}
				cur += string(c)
				curW += charW(c)
			}
		}
		lines = append(lines, cur)
	}
	return lines
}

func nodeLines(n DNode, limit float64) []string {
	lines := wrapText(n.Label, limit)
	if n.Note != "" {
		lines = append(lines, wrapText(n.Note, limit)...)
	}
	return lines
}

// nodeSize is the frame for a node's text: the text sits in a rectangle inside
// the outline (.shape-label).
func nodeSize(n DNode) (float64, float64) {
	fit := func(lines []string) (tw, th float64) {
		for _, l := range lines {
			tw = math.Max(tw, textW(l))
		}
		return tw, float64(len(lines)) * lineH
	}
	var w, h float64
	switch n.Shape {
	case "diamond":
		// half of each side is usable; wider rather than tall, up to a point
		var lines []string
		for _, limit := range diamondTextMax {
			lines = nodeLines(n, limit)
			if len(lines) <= 3 {
				break
			}
		}
		tw, th := fit(lines)
		w, h = math.Max(170, 2*tw+16), math.Max(100, 2*th+16)
	case "ellipse":
		tw, th := fit(nodeLines(n, ellipseTextMax))
		w, h = math.Max(nodeMinW, (tw+8)/0.7), math.Max(nodeMinH, (th+8)/0.7)
	default:
		tw, th := fit(nodeLines(n, rectTextMax))
		w, h = math.Max(nodeMinW, tw+2*rectPad), math.Max(nodeMinH, th+2*rectPad)
	}
	return math.Round(w), math.Round(h)
}

func labelW(text string) float64 {
	return math.Max(40, math.Ceil(textW(text)*labelScale)+16)
}

// layers assigns each node a layer (longest path from sources; edges that
// close a cycle are ignored) and orders the layers by barycenter.
func layers(d *Diagram) [][]int {
	idx := map[string]int{}
	for i, n := range d.Nodes {
		idx[n.ID] = i
	}
	n := len(d.Nodes)
	out := make([][]int, n)
	in := make([][]int, n)
	// drop back edges found by DFS (cycle breaking)
	state := make([]int, n)
	succ := make([][]int, n)
	for _, e := range d.Edges {
		a, b := idx[e.From], idx[e.To]
		if a != b {
			succ[a] = append(succ[a], b)
		}
	}
	var dfs func(v int)
	dfs = func(v int) {
		state[v] = 1
		for _, w := range succ[v] {
			switch state[w] {
			case 0:
				out[v], in[w] = append(out[v], w), append(in[w], v)
				dfs(w)
			case 2:
				out[v], in[w] = append(out[v], w), append(in[w], v)
			}
		}
		state[v] = 2
	}
	for v := 0; v < n; v++ {
		if state[v] == 0 {
			dfs(v)
		}
	}
	layer := make([]int, n)
	var depth func(v int) int
	memo := map[int]int{}
	depth = func(v int) int {
		if x, ok := memo[v]; ok {
			return x
		}
		best := 0
		for _, p := range in[v] {
			best = max(best, depth(p)+1)
		}
		memo[v] = best
		return best
	}
	maxL := 0
	for v := 0; v < n; v++ {
		layer[v] = depth(v)
		maxL = max(maxL, layer[v])
	}
	ls := make([][]int, maxL+1)
	for v := 0; v < n; v++ {
		ls[layer[v]] = append(ls[layer[v]], v)
	}
	// barycenter sweeps (down, up, down)
	pos := make([]float64, n)
	setPos := func() {
		for _, l := range ls {
			for i, v := range l {
				pos[v] = float64(i)
			}
		}
	}
	setPos()
	sweep := func(l []int, nb func(int) []int) {
		bc := map[int]float64{}
		for _, v := range l {
			ns := nb(v)
			if len(ns) == 0 {
				bc[v] = pos[v]
				continue
			}
			s := 0.0
			for _, w := range ns {
				s += pos[w]
			}
			bc[v] = s / float64(len(ns))
		}
		sort.SliceStable(l, func(i, j int) bool { return bc[l[i]] < bc[l[j]] })
		for i, v := range l {
			pos[v] = float64(i)
		}
	}
	for it := 0; it < 3; it++ {
		for k := 1; k < len(ls); k++ {
			sweep(ls[k], func(v int) []int { return in[v] })
		}
		for k := len(ls) - 2; k >= 0; k-- {
			sweep(ls[k], func(v int) []int { return out[v] })
		}
	}
	return ls
}

// border is where the ray from a node's centre towards `to` leaves its outline,
// plus gap.
func border(b box, shape string, to pt, gap float64) pt {
	cx, cy := b.cx(), b.cy()
	dx, dy := to.x-cx, to.y-cy
	dist := math.Hypot(dx, dy)
	if dist == 0 {
		return pt{cx, cy}
	}
	rx, ry := dx/(b.w/2), dy/(b.h/2)
	var s float64
	switch shape {
	case "diamond":
		s = 1 / (math.Abs(rx) + math.Abs(ry))
	case "ellipse":
		s = 1 / math.Hypot(rx, ry)
	default:
		s = 1 / math.Max(math.Abs(rx), math.Abs(ry))
	}
	return pt{cx + dx*s + dx/dist*gap, cy + dy*s + dy/dist*gap}
}

// curveAt is the curve a line tile draws: from p0 to p2, through mid halfway.
func curveAt(p0, mid, p2 pt, t float64) pt {
	u := 1 - t
	cx, cy := 2*mid.x-(p0.x+p2.x)/2, 2*mid.y-(p0.y+p2.y)/2
	return pt{u*u*p0.x + 2*u*t*cx + t*t*p2.x, u*u*p0.y + 2*u*t*cy + t*t*p2.y}
}

func curveSamples(p0, mid, p2 pt) []pt {
	out := make([]pt, 0, curveSteps+1)
	for i := 0; i <= curveSteps; i++ {
		out = append(out, curveAt(p0, mid, p2, float64(i)/curveSteps))
	}
	return out
}

// runsAlong reports a curve whose middle runs along a curve already drawn.
func runsAlong(pts []pt, drawn [][]pt) bool {
	closeBy, total := 0, 0
	for i := 5; i <= curveSteps-5; i += 2 {
		total++
	search:
		for _, d := range drawn {
			for _, q := range d {
				if math.Hypot(q.x-pts[i].x, q.y-pts[i].y) < 9 {
					closeBy++
					break search
				}
			}
		}
	}
	return float64(closeBy)/float64(total) > 0.35
}

// crossesOwn reports a curve that runs through a tile it starts or ends at. Both
// ends lie just outside its outline.
func crossesOwn(from, mid, to pt, b box, shape string) bool {
	hw, hh := b.w/2, b.h/2
	for i := 1; i < 2*curveSteps; i++ {
		p := curveAt(from, mid, to, float64(i)/(2*curveSteps))
		dx, dy := math.Abs(p.x-(b.x+hw))/hw, math.Abs(p.y-(b.y+hh))/hh
		var inside bool
		switch shape {
		case "diamond":
			inside = dx+dy < 1
		case "ellipse":
			inside = dx*dx+dy*dy < 1
		default:
			inside = dx < 1 && dy < 1
		}
		if inside {
			return true
		}
	}
	return false
}

type route struct {
	from, mid, to pt
	pts           []pt
	clean         bool // touches nothing it should not
}

// routeEdge bends an edge as little as it takes to pass no tile that is not its
// own; reach 0 tries only firstBend.
func routeEdge(a box, aShape string, b box, bShape string, firstBend float64, others []box, drawn [][]pt, reach float64) route {
	dx, dy := b.cx()-a.cx(), b.cy()-a.cy()
	dist := math.Hypot(dx, dy)
	if dist == 0 {
		dist = 1
	}
	nx, ny := -dy/dist, dx/dist
	mx, my := (a.cx()+b.cx())/2, (a.cy()+b.cy())/2
	type try struct{ o, along float64 }
	tries := []try{{firstBend, 0}}
	for k := 1; k <= bendTries && float64(k)*bendStep <= reach; k++ {
		o := float64(k) * bendStep
		for _, along := range bendAlong {
			tries = append(tries, try{firstBend + o, along}, try{firstBend - o, along})
		}
	}
	var best route
	bestScore := math.Inf(1)
	for _, tr := range tries {
		o := tr.o
		apex := pt{mx + nx*o + dx*tr.along, my + ny*o + dy*tr.along}
		from, to := border(a, aShape, apex, 2), border(b, bShape, apex, arrowGap)
		mid := apex
		if o == 0 {
			mid = pt{(from.x + to.x) / 2, (from.y + to.y) / 2}
		}
		pts := curveSamples(from, mid, to)
		hits := 0
		for _, other := range others {
			if other.touches(pts, clearance) {
				hits++
			}
		}
		// worse than passing another tile: the arrow seems to come out on the wrong side
		if crossesOwn(from, mid, to, a, aShape) || crossesOwn(from, mid, to, b, bShape) {
			hits += 3
		}
		score := float64(hits)*1000 + math.Abs(o)/100
		if runsAlong(pts, drawn) {
			score += 500
		}
		if score < bestScore {
			bestScore = score
			best = route{from, mid, to, pts, hits == 0 && score < 500}
		}
		if hits == 0 && score < 500 {
			break
		}
	}
	return best
}

type labelSpot struct {
	b     box
	align string
}

// placeLabel finds a spot beside the curve where the label hides nothing and
// nothing hides it.
func placeLabel(r route, w float64, boxes []box, lines [][]pt, own int) labelSpot {
	var best labelSpot
	bestScore := math.Inf(1)
	spots := []float64{0.5, 0.4, 0.6, 0.3, 0.7, 0.2, 0.8}
	// close to its arrow first, further out only when there is no room
	for _, far := range labelDistances {
		for s, t := range spots {
			p := curveAt(r.from, r.mid, r.to, t)
			before, after := curveAt(r.from, r.mid, r.to, t-0.02), curveAt(r.from, r.mid, r.to, t+0.02)
			tx, ty := after.x-before.x, after.y-before.y
			right := labelSpot{box{p.x + far, p.y - labelH/2, w, labelH}, "left"}
			left := labelSpot{box{p.x - far - w, p.y - labelH/2, w, labelH}, "right"}
			above := labelSpot{box{p.x - w/2, p.y - labelH - far + 6, w, labelH}, "center"}
			below := labelSpot{box{p.x - w/2, p.y + far - 6, w, labelH}, "center"}
			var sides []labelSpot
			switch {
			case math.Abs(tx) < math.Abs(ty)*0.6:
				sides = []labelSpot{right, left, above, below}
			case math.Abs(ty) < math.Abs(tx)*0.6:
				sides = []labelSpot{above, below, right, left}
			case tx*ty > 0: // falling to the right: free above it on the right, below it on the left
				right.b.y, left.b.y = p.y-labelH, p.y
				sides = []labelSpot{right, left, above, below}
			default:
				right.b.y, left.b.y = p.y, p.y-labelH
				sides = []labelSpot{right, left, above, below}
			}
			for _, c := range sides {
				score := float64(s) + far/10
				for _, b := range boxes {
					if c.b.overlaps(box{b.x - 4, b.y - 4, b.w + 8, b.h + 8}) {
						score += 1000
					}
				}
				for i, l := range lines {
					if c.b.touches(l, 4) {
						if i == own {
							score += 300
						} else {
							score += 100
						}
					}
				}
				if score < bestScore {
					bestScore, best = score, c
				}
				if score < 100 {
					return c
				}
			}
		}
	}
	return best
}

// placeWireLabel finds a spot beside a wire: on a stretch of its own if it has one,
// where the label hides nothing.
func placeWireLabel(route []pt, w float64, boxes []box, lines [][]pt, own int) labelSpot {
	type stretch struct {
		a, b pt
		rank float64
	}
	stretches := make([]stretch, 0, len(route))
	for i := 1; i < len(route); i++ {
		a, b := route[i-1], route[i]
		length := math.Abs(b.x-a.x) + math.Abs(b.y-a.y)
		mid := box{x: (a.x + b.x) / 2, y: (a.y + b.y) / 2}
		rank := -math.Min(length, 400) / 20
		for k, l := range lines {
			if k != own && mid.touches(l, 1) {
				rank += 30
				break
			}
		}
		stretches = append(stretches, stretch{a, b, rank})
	}
	sort.SliceStable(stretches, func(i, j int) bool { return stretches[i].rank < stretches[j].rank })
	var best labelSpot
	bestScore, settled := math.Inf(1), false
	for _, st := range stretches {
		upright := st.a.x == st.b.x
		for _, far := range wireLabelDistances {
			for _, t := range wireLabelSpots {
				x, y := st.a.x+(st.b.x-st.a.x)*t, st.a.y+(st.b.y-st.a.y)*t
				spots := []labelSpot{{box{x - w/2, y - labelH - far + 6, w, labelH}, "center"}, {box{x - w/2, y + far - 6, w, labelH}, "center"}}
				if upright {
					spots = []labelSpot{{box{x + far, y - labelH/2, w, labelH}, "left"}, {box{x - far - w, y - labelH/2, w, labelH}, "right"}}
				}
				for _, c := range spots {
					hits := 0.0
					for _, b := range boxes {
						if c.b.overlaps(box{b.x - 4, b.y - 4, b.w + 8, b.h + 8}) {
							hits += 1000
						}
					}
					for k, l := range lines {
						if c.b.touches(l, 3) {
							if k == own {
								hits += 300
							} else {
								hits += 100
							}
						}
					}
					if score := hits + st.rank + far + math.Abs(t-0.5)*4; score < bestScore {
						bestScore, best, settled = score, c, hits == 0 && far <= wireLabelNear
					}
				}
			}
			if settled {
				return best
			}
		}
	}
	return best
}

// settleForWires lets wires run straighter between tiles that face each other: each
// layer moves towards what it connects to, keeping its order.
func settleForWires(d *Diagram, ls [][]int, size []box, lr bool, crossGap []float64, idx map[string]int, layerOf []int) {
	half := func(v int) float64 {
		if lr {
			return size[v].h / 2
		}
		return size[v].w / 2
	}
	centre := func(v int) float64 {
		if lr {
			return size[v].y + half(v)
		}
		return size[v].x + half(v)
	}
	setCentre := func(v int, c float64) {
		if lr {
			size[v].y = c - half(v)
		} else {
			size[v].x = c - half(v)
		}
	}
	near := make([][]int, len(d.Nodes))
	for _, e := range d.Edges {
		a, b := idx[e.From], idx[e.To]
		if layerOf[a] == layerOf[b] {
			continue
		}
		near[a], near[b] = append(near[a], b), append(near[b], a)
	}
	place := func(k int, want []float64) {
		l := ls[k]
		push := append([]float64{}, want...)
		for i := 1; i < len(l); i++ {
			push[i] = math.Max(push[i], push[i-1]+half(l[i-1])+crossGap[k]+half(l[i]))
		}
		pull := append([]float64{}, want...)
		for i := len(l) - 2; i >= 0; i-- {
			pull[i] = math.Min(pull[i], pull[i+1]-half(l[i+1])-crossGap[k]-half(l[i]))
		}
		for i, v := range l {
			setCentre(v, (push[i]+pull[i])/2)
		}
	}
	for it := 0; it < 8; it++ {
		down := it%2 == 0
		for j := range ls {
			k := j
			if !down {
				k = len(ls) - 1 - j
			}
			want := make([]float64, len(ls[k]))
			for i, v := range ls[k] {
				sum, n := 0.0, 0
				for _, o := range near[v] {
					if (down && layerOf[o] < k) || (!down && layerOf[o] > k) {
						sum += centre(o)
						n++
					}
				}
				if n > 0 {
					want[i] = sum / float64(n)
				} else {
					want[i] = centre(v)
				}
			}
			place(k, want)
		}
	}
	// the middle of every tile on a grid line, so that a wire can meet it there
	for k, l := range ls {
		low := math.Inf(-1)
		for i, v := range l {
			c := math.Max(wireSnap(centre(v)), low)
			setCentre(v, c)
			if i+1 < len(l) {
				low = math.Ceil((c+half(v)+crossGap[k]+half(l[i+1]))/wireStep) * wireStep
			}
		}
		for _, v := range l {
			if lr {
				size[v].x = wireSnap(size[v].cx()) - size[v].w/2
			} else {
				size[v].y = wireSnap(size[v].cy()) - size[v].h/2
			}
		}
	}
}

type zoneOut struct {
	id, label, color string
	b, labelBox      box
}

type edgeOut struct {
	key, label, color string
	from, mid, to     pt
	route, dots       []pt // a wire: its stretches and where it splits
	dashed            bool
	labelBox          *labelSpot
}

// laid is everything of a diagram in its place, top left at (0, 0).
type laid struct {
	nodes []box
	zones []zoneOut
	edges []edgeOut
	title *box
}

func layoutDiagram(d *Diagram, style string, look Look) laid {
	wired := look.Arrows == "wiring"
	ls := layers(d)
	n := len(d.Nodes)
	size := make([]box, n)
	idx := map[string]int{}
	for i, nd := range d.Nodes {
		w, h := nodeSize(nd)
		if wired { // even, so that the middle of a tile can lie exactly on a grid line
			w, h = w+math.Mod(w, 2), h+math.Mod(h, 2)
		}
		size[i] = box{w: w, h: h}
		idx[nd.ID] = i
	}
	layerOf := make([]int, n)
	for k, l := range ls {
		for _, v := range l {
			layerOf[v] = k
		}
	}
	lr := d.Direction == "LR"
	crossGap := make([]float64, len(ls))
	for k := range crossGap {
		crossGap[k] = gapCross
	}
	// main axis = layers; cross axis = order inside a layer
	extent := func(l []int, k int) (main, cross float64) {
		for i, v := range l {
			mw, cw := size[v].h, size[v].w
			if lr {
				mw, cw = size[v].w, size[v].h
			}
			main = math.Max(main, mw)
			cross += cw
			if i > 0 {
				cross += crossGap[k]
			}
		}
		return
	}
	// left to right, a label runs along its arrow: the gap has to hold it
	gaps := make([]float64, len(ls))
	for k := range gaps {
		gaps[k] = gapMain
	}
	if lr {
		for _, e := range d.Edges {
			a, b := layerOf[idx[e.From]], layerOf[idx[e.To]]
			if e.Label != "" && (a-b == 1 || b-a == 1) {
				gaps[min(a, b)] = math.Max(gaps[min(a, b)], labelW(e.Label)+40)
			}
		}
	}
	// wires lie side by side: the gaps have to hold them
	if wired {
		tracks := make([]map[string]bool, len(ls))
		passing := make([]int, len(ls))
		labelled := make([]bool, len(ls))
		for k := range tracks {
			tracks[k] = map[string]bool{}
		}
		for _, e := range d.Edges {
			a, b := layerOf[idx[e.From]], layerOf[idx[e.To]]
			for k := min(a, b); k < max(a, b); k++ {
				tracks[k][e.From+":"+e.Style] = true // wires of one source share a track
				labelled[k] = labelled[k] || e.Label != ""
				if k > min(a, b) {
					passing[k]++
				}
			}
		}
		for k, l := range ls {
			extra := 0.0
			if labelled[k] && !lr {
				extra = labelH
			}
			gaps[k] = math.Max(gaps[k], wireStep*float64(min(len(tracks[k]), maxTracks)+3)+extra)
			crossGap[k] = math.Max(gapCross, wireStep*(math.Ceil(float64(passing[k])/float64(len(l)+1))+3))
		}
	}
	widest := 0.0
	for k, l := range ls {
		_, c := extent(l, k)
		widest = math.Max(widest, c)
	}
	m := 0.0
	for k, l := range ls {
		mainSz, cross := extent(l, k)
		c := (widest - cross) / 2
		for _, v := range l {
			b := &size[v]
			if lr {
				b.x = m + (mainSz-b.w)/2
				b.y = c
				c += b.h + crossGap[k]
			} else {
				b.x = c
				b.y = m + (mainSz-b.h)/2
				c += b.w + crossGap[k]
			}
			b.x, b.y = math.Round(b.x), math.Round(b.y)
		}
		m += mainSz + gaps[k]
	}
	if wired {
		settleForWires(d, ls, size, lr, crossGap, idx, layerOf)
	}
	gcolor := map[string]string{}
	for i, g := range d.Groups {
		gcolor[g.ID] = groupColors[i%len(groupColors)]
	}
	stroke := func(group string) string {
		if style == "color" && gcolor[group] != "" {
			return gcolor[group]
		}
		return inkColor
	}
	// colourful arrows: wires from one source alike, so that one can be followed across the diagram
	sourceColor := map[string]string{}
	for _, l := range ls {
		for _, v := range l {
			id := d.Nodes[v].ID
			for _, e := range d.Edges {
				if e.From == id {
					sourceColor[id] = wireColors[len(sourceColor)%len(wireColors)]
					break
				}
			}
		}
	}
	arrowColor := func(from string) string {
		group := d.Nodes[idx[from]].Group
		switch look.ArrowColor {
		case "mono":
			return inkColor
		case "color":
			if style == "color" && gcolor[group] != "" {
				return gcolor[group]
			}
			return sourceColor[from]
		}
		return stroke(group)
	}
	// group zones (behind), only when ≥ 2 groups and they do not overlap
	var zones []zoneOut
	if len(d.Groups) >= 2 {
		boxes := map[string]box{}
		for _, g := range d.Groups {
			first := true
			var z box
			for i, nd := range d.Nodes {
				if nd.Group != g.ID {
					continue
				}
				b := size[i]
				if first {
					z, first = b, false
					continue
				}
				x2, y2 := math.Max(z.x+z.w, b.x+b.w), math.Max(z.y+z.h, b.y+b.h)
				z.x, z.y = math.Min(z.x, b.x), math.Min(z.y, b.y)
				z.w, z.h = x2-z.x, y2-z.y
			}
			if !first {
				boxes[g.ID] = box{z.x - zonePad, z.y - zonePad - zoneHead, z.w + 2*zonePad, z.h + 2*zonePad + zoneHead}
			}
		}
		clash := map[string]bool{}
		for a, za := range boxes {
			for b, zb := range boxes {
				if a < b && za.overlaps(zb) {
					clash[a], clash[b] = true, true
				}
			}
			for i, nd := range d.Nodes {
				if nd.Group != a && za.overlaps(size[i]) {
					clash[a] = true
				}
			}
		}
		for _, g := range d.Groups {
			z, ok := boxes[g.ID]
			if !ok || clash[g.ID] {
				continue
			}
			zones = append(zones, zoneOut{g.ID, g.Label, stroke(g.ID), z, box{z.x + 10, z.y + 4, z.w - 20, labelH}})
		}
	}
	// First the arrows that can run as they are, with their labels; the others then
	// bend around those labels as well as around the tiles.
	reach := 2*math.Max(widest, m) + 2*bendStep
	seen := map[string]int{}
	bends := make([]float64, len(d.Edges))
	for k, e := range d.Edges {
		// more than one arrow between two tiles: side by side. The way back bends the other way by itself.
		pair := e.From + ">" + e.To
		nth := seen[pair]
		seen[pair]++
		both := nth > 0
		for _, o := range d.Edges {
			both = both || (o.From == e.To && o.To == e.From)
		}
		if both {
			bends[k] = pairBend * float64(nth+1)
		}
	}
	drawn := make([][]pt, 0, len(d.Edges))
	own := make([]int, len(d.Edges))
	taken := append([]box{}, size...)
	for _, z := range zones {
		taken = append(taken, z.labelBox)
	}
	routes := make([]route, len(d.Edges))
	spots := make([]*labelSpot, len(d.Edges))
	routeOf := func(k int, far float64) route {
		e := d.Edges[k]
		ai, bi := idx[e.From], idx[e.To]
		a, b := size[ai], size[bi]
		if e.From == e.To { // self loop: a small hook on the right
			r := route{from: pt{a.x + a.w, a.cy() - 10}, mid: pt{a.x + a.w + 40, a.cy()}, to: pt{a.x + a.w, a.cy() + 10}, clean: true}
			r.pts = curveSamples(r.from, r.mid, r.to)
			return r
		}
		others := make([]box, 0, len(taken))
		for i, t := range taken {
			if i != ai && i != bi {
				others = append(others, t)
			}
		}
		return routeEdge(a, d.Nodes[ai].Shape, b, d.Nodes[bi].Shape, bends[k], others, drawn, far)
	}
	label := func(k int) {
		if e := d.Edges[k]; e.Label != "" {
			spot := placeLabel(routes[k], labelW(e.Label), taken, drawn, own[k])
			spots[k] = &spot
			taken = append(taken, spot.b)
		}
	}
	var wires []wire
	if wired {
		wireNodes := make([]wireNode, len(d.Nodes))
		for i, nd := range d.Nodes {
			wireNodes[i] = wireNode{size[i], nd.Shape}
		}
		wireEdges := make([]wireEdge, len(d.Edges))
		labelled := false
		for k, e := range d.Edges {
			wireEdges[k] = wireEdge{idx[e.From], idx[e.To], e.Style == "dashed"}
			labelled = labelled || e.Label != ""
		}
		var zoneLabels []box
		for _, z := range zones {
			zoneLabels = append(zoneLabels, z.labelBox)
		}
		labelAll := func() {
			lines := make([][]pt, len(wires))
			for k := range wires {
				lines[k] = wires[k].route
			}
			free := append([]box{}, taken...)
			for k, e := range d.Edges {
				if e.Label != "" {
					spot := placeWireLabel(lines[k], labelW(e.Label), free, lines, k)
					spots[k] = &spot
					free = append(free, spot.b)
				}
			}
		}
		// Labels go where the wires leave room; the wires then keep off the labels, and the
		// labels settle beside where their wires ended up.
		wires = routeWires(wireNodes, wireEdges, zoneLabels, lr)
		if labelled {
			labelAll()
			keepOff := append([]box{}, zoneLabels...)
			for _, sp := range spots {
				if sp != nil {
					keepOff = append(keepOff, sp.b)
				}
			}
			wires = routeWires(wireNodes, wireEdges, keepOff, lr)
			labelAll()
		}
		for k, wr := range wires {
			a, b := wr.route[0], wr.route[len(wr.route)-1]
			routes[k] = route{from: a, mid: pt{(a.x + b.x) / 2, (a.y + b.y) / 2}, to: b, pts: wr.route, clean: true}
		}
	} else {
		straight := make([]bool, len(d.Edges))
		for k := range d.Edges {
			if r := routeOf(k, 0); r.clean {
				routes[k], straight[k], own[k] = r, true, len(drawn)
				drawn = append(drawn, r.pts)
			}
		}
		for k := range d.Edges {
			if straight[k] {
				label(k)
			}
		}
		for k := range d.Edges {
			if !straight[k] {
				routes[k], own[k] = routeOf(k, reach), len(drawn)
				drawn = append(drawn, routes[k].pts)
			}
		}
		for k := range d.Edges {
			if !straight[k] {
				label(k)
			}
		}
	}
	edges := make([]edgeOut, len(d.Edges))
	for k, e := range d.Edges {
		r := routes[k]
		edges[k] = edgeOut{key: "edge:" + e.From + ">" + e.To + ":" + strconv.Itoa(k), label: e.Label, color: arrowColor(e.From),
			from: r.from, mid: r.mid, to: r.to, dashed: e.Style == "dashed", labelBox: spots[k]}
		if wired {
			edges[k].route, edges[k].dots = r.pts, wires[k].dots
		}
	}
	// everything from (0, 0), the title above it
	minX, minY := math.Inf(1), math.Inf(1)
	grow := func(x, y float64) { minX, minY = math.Min(minX, x), math.Min(minY, y) }
	for _, b := range size {
		grow(b.x, b.y)
	}
	for _, z := range zones {
		grow(z.b.x, z.b.y)
	}
	for k := range edges {
		if s := edges[k].labelBox; s != nil {
			grow(s.b.x, s.b.y)
		}
		for _, p := range routes[k].pts {
			grow(p.x, p.y)
		}
	}
	if math.IsInf(minX, 1) {
		minX, minY = 0, 0
	}
	sx, sy := math.Ceil(-minX), math.Ceil(-minY)
	if d.Title != "" {
		sy += titleH
	}
	for i := range size {
		size[i].x += sx
		size[i].y += sy
	}
	for i := range zones {
		zones[i].b.x += sx
		zones[i].b.y += sy
		zones[i].labelBox.x += sx
		zones[i].labelBox.y += sy
	}
	for i := range edges {
		e := &edges[i]
		e.from, e.mid, e.to = pt{e.from.x + sx, e.from.y + sy}, pt{e.mid.x + sx, e.mid.y + sy}, pt{e.to.x + sx, e.to.y + sy}
		for j := range e.route {
			e.route[j] = pt{e.route[j].x + sx, e.route[j].y + sy}
		}
		for j := range e.dots {
			e.dots[j] = pt{e.dots[j].x + sx, e.dots[j].y + sy}
		}
		if e.labelBox != nil {
			e.labelBox.b.x += sx
			e.labelBox.b.y += sy
		}
	}
	out := laid{nodes: size, zones: zones, edges: edges}
	if d.Title != "" {
		out.title = &box{0, 0, math.Max(200, math.Ceil(textW(d.Title)*1.4)+24), titleBoxH}
	}
	return out
}

// Land lays out d and returns the ops (z order: zones, edges, nodes,
// labels, title) and the ids of the created nodes. (ox, oy) is the top
// left of the result on the board.
func Land(d *Diagram, style, diagramID string, ox, oy float64, look Look) []board.Op {
	l := layoutDiagram(d, style, look)
	gcolor := map[string]string{}
	for i, g := range d.Groups {
		gcolor[g.ID] = groupColors[i%len(groupColors)]
	}
	stroke := func(group string) string {
		if style == "color" && gcolor[group] != "" {
			return gcolor[group]
		}
		return inkColor
	}
	meta := func(key string, extra map[string]any) json.RawMessage {
		extra["diagramId"] = diagramID
		extra["diagramKey"] = key
		b, _ := json.Marshal(extra)
		return b
	}
	var ops []board.Op
	put := func(kind string, b box, data json.RawMessage) {
		ops = append(ops, board.Op{Op: "put", Node: &board.Node{ID: newNodeID(), Kind: kind, X: math.Round(ox + b.x), Y: math.Round(oy + b.y),
			W: math.Max(1, math.Round(b.w)), H: math.Max(1, math.Round(b.h)), Data: data}})
	}
	for _, z := range l.zones {
		put("shape", z.b, meta("group:"+z.id, map[string]any{"shape": "rect", "stroke": z.color, "strokeWidth": 1,
			"strokeStyle": "dashed", "roughness": 1, "fill": "transparent", "fillStyle": "hachure", "edges": "round",
			"opacity": 70, "text": "", "seed": seed()}))
		put("text", z.labelBox, meta("groupLabel:"+z.id, map[string]any{"text": z.label,
			"font": "hand", "size": "s", "align": "left", "color": z.color}))
	}
	r := func(v float64) float64 { return math.Round(v*10) / 10 }
	for _, e := range l.edges {
		minX, minY := math.Min(e.from.x, math.Min(e.to.x, e.mid.x)), math.Min(e.from.y, math.Min(e.to.y, e.mid.y))
		maxX, maxY := math.Max(e.from.x, math.Max(e.to.x, e.mid.x)), math.Max(e.from.y, math.Max(e.to.y, e.mid.y))
		for _, p := range e.route {
			minX, minY = math.Min(minX, p.x), math.Min(minY, p.y)
			maxX, maxY = math.Max(maxX, p.x), math.Max(maxY, p.y)
		}
		rel := func(ps ...pt) [][2]float64 {
			out := make([][2]float64, len(ps))
			for i, p := range ps {
				out[i] = [2]float64{r(p.x - minX), r(p.y - minY)}
			}
			return out
		}
		ss := "solid"
		if e.dashed {
			ss = "dashed"
		}
		data := map[string]any{"points": rel(e.from, e.mid, e.to), "arrowStart": false, "arrowEnd": true, "stroke": e.color,
			"strokeWidth": 2, "strokeStyle": ss, "roughness": 1, "opacity": 100, "seed": seed()}
		if e.route != nil {
			// a client that does not know routes draws the straight line through points
			data["route"], data["dots"], data["roughness"] = rel(e.route...), rel(e.dots...), 0
		}
		put("line", box{math.Round(minX), math.Round(minY), maxX - minX, maxY - minY}, meta(e.key, data))
	}
	for i, nd := range d.Nodes {
		shape, edges := "rect", "round"
		switch nd.Shape {
		case "box":
			edges = "sharp"
		case "ellipse":
			shape = "ellipse"
		case "diamond":
			shape = "diamond"
		}
		text := nd.Label
		if nd.Note != "" {
			text += "\n" + nd.Note
		}
		put("shape", l.nodes[i], meta("node:"+nd.ID, map[string]any{"shape": shape, "stroke": stroke(nd.Group), "strokeWidth": 2,
			"strokeStyle": "solid", "roughness": 1, "fill": "transparent", "fillStyle": "hachure", "backed": true, "edges": edges,
			"opacity": 100, "text": text, "font": "hand", "size": "m", "align": "center", "seed": seed()}))
	}
	for k, e := range l.edges {
		if e.labelBox != nil {
			color := inkColor
			if look.ArrowColor == "color" {
				color = e.color
			}
			put("text", e.labelBox.b, meta("edgeLabel:"+strconv.Itoa(k), map[string]any{"text": e.label, "font": "hand", "size": "s", "align": e.labelBox.align, "color": color}))
		}
	}
	if l.title != nil {
		put("text", *l.title, meta("title", map[string]any{"text": d.Title, "font": "hand", "size": "l", "align": "left", "color": inkColor}))
	}
	return ops
}

// newNodeID is a board node id (n_ + 10 chars).
func newNodeID() string {
	const alpha = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return "n_" + string(b)
}

func seed() int {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return int(binary.LittleEndian.Uint32(b[:]) % 2147483647)
}
