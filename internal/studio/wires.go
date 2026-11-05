package studio

import (
	"math"
	"sort"
	"strconv"
)

// Wiring-diagram arrows (docs/API-studio.md §6): every arrow runs in straight
// horizontal and vertical stretches on a grid, around the tiles that are not
// its own. A search per arrow finds the way with few bends and few crossings.
// Arrows from one source share their way out and split at a dot, as wires do; a
// crossing without a dot is no connection. The web client has the same rules in
// web/src/studio/diagrams/wires.ts.

const (
	wireStep        = 16.0
	wireClear       = 16.0 // what a wire keeps from a tile that is not its own
	wireMargin      = 10   // grid lines around the diagram, for a wire with no way between the tiles
	wireOutline     = 5.0  // a shape's outline lies this far inside its box (drawShape's pad)
	wireTipGap      = 2.0  // between an arrow's tip and the outline it points at
	wirePortMargin  = 12.0
	wireMaxSpacing  = 3 // in grid steps
	wirePasses      = 2
	costBend        = 48.0
	costCrossing    = 64.0
	costShared      = 4.0   // running along a wire of the same source
	costAlong       = 240.0 // running along a wire of another source: they could not be told apart
	costOnWire      = 320.0 // a corner on another wire looks like a connection
	costUnderLabel  = 400.0
	costThroughTile = 1_000_000.0 // only when there is no other way at all
	costWrongWay    = 100_000.0
)

var (
	wireDX = [4]int{1, 0, -1, 0}
	wireDY = [4]int{0, 1, 0, -1}
)

type wireNode struct {
	b     box
	shape string
}

type wireEdge struct {
	from, to int
	dashed   bool
}

type wire struct {
	route []pt // first point at the source's outline, last point the arrow's tip
	dots  []pt // where this wire and another from the same source part ways
}

type wirePort struct {
	node  int
	dir   int     // the way out of the tile on this side
	coord float64 // across the side
	sort  float64
	count int
}

func wireFlat(dir int) bool { return dir%2 == 0 }

func wireSnap(v float64) float64 { return math.Floor(v/wireStep+0.5) * wireStep }

// outlineAt is how far along dir the outline of a shape lies at this spot of the
// side, from the box's centre.
func outlineAt(n wireNode, dir int, coord float64) float64 {
	half, across, centre := n.b.h/2-wireOutline, n.b.w/2-wireOutline, n.b.cx()
	if wireFlat(dir) {
		half, across, centre = n.b.w/2-wireOutline, n.b.h/2-wireOutline, n.b.cy()
	}
	t := math.Min(1, math.Abs(coord-centre)/math.Max(1, across))
	switch n.shape {
	case "ellipse":
		return half * math.Sqrt(1-t*t)
	case "diamond":
		return half * (1 - t)
	}
	return half
}

// usableSide is the width of a side a wire can meet; an ellipse or a diamond falls
// away towards its ends.
func usableSide(n wireNode, dir int) float64 {
	length := n.b.w
	if wireFlat(dir) {
		length = n.b.h
	}
	switch n.shape {
	case "ellipse":
		return length * 0.6
	case "diamond":
		return length * 0.5
	}
	return math.Max(0, length-2*wirePortMargin)
}

// wireSides is which way a wire leaves its source and arrives at its target.
func wireSides(a, b box, lr, self bool) (int, int) {
	if self {
		return 0, 2
	}
	below, above := b.y >= a.y+a.h, b.y+b.h <= a.y
	right, left := b.x >= a.x+a.w, b.x+b.w <= a.x
	if lr {
		switch {
		case right:
			return 0, 0
		case left:
			return 2, 2
		case below:
			return 1, 1
		}
		return 3, 3
	}
	switch {
	case below:
		return 1, 1
	case above:
		return 3, 3
	case right:
		return 0, 0
	}
	return 2, 2
}

// routeWires finds the way of every edge. labels are places a wire keeps off.
func routeWires(nodes []wireNode, edges []wireEdge, labels []box, lr bool) []wire {
	if len(edges) == 0 {
		return nil
	}

	// ---- one way out per source and line style, one way in per arrow ----
	groupIDs := map[string]int{}
	groupOf := make([]int, len(edges))
	for k, e := range edges {
		key := strconv.Itoa(e.from) + ":" + strconv.FormatBool(e.dashed)
		id, ok := groupIDs[key]
		if !ok {
			id = len(groupIDs)
			groupIDs[key] = id
		}
		groupOf[k] = id
	}
	centreAcross := func(node, dir int) float64 {
		if wireFlat(dir) {
			return nodes[node].b.cy()
		}
		return nodes[node].b.cx()
	}
	outOf := map[[2]int]*wirePort{}
	var ports []*wirePort
	outPort := make([]*wirePort, len(edges))
	inPort := make([]*wirePort, len(edges))
	for k, e := range edges {
		out, into := wireSides(nodes[e.from].b, nodes[e.to].b, lr, e.from == e.to)
		key := [2]int{groupOf[k], out}
		p := outOf[key]
		if p == nil {
			p = &wirePort{node: e.from, dir: out}
			outOf[key] = p
			ports = append(ports, p)
		}
		p.sort += centreAcross(e.to, out)
		p.count++
		outPort[k] = p
		q := &wirePort{node: e.to, dir: (into + 2) % 4, sort: centreAcross(e.from, into), count: 1}
		ports = append(ports, q)
		inPort[k] = q
	}
	bySide := map[[2]int][]*wirePort{}
	for _, p := range ports {
		key := [2]int{p.node, p.dir}
		bySide[key] = append(bySide[key], p)
	}
	for _, side := range bySide {
		sort.SliceStable(side, func(i, j int) bool {
			return side[i].sort/float64(side[i].count) < side[j].sort/float64(side[j].count)
		})
		n := nodes[side[0].node]
		dir := side[0].dir
		room := usableSide(n, dir)
		centre := centreAcross(side[0].node, dir)
		spacing := 0.0
		if len(side) > 1 {
			spacing = math.Max(1, math.Min(wireMaxSpacing, math.Floor(room/float64(len(side)-1)/wireStep))) * wireStep
		}
		for i, p := range side {
			offset := (float64(i) - float64(len(side)-1)/2) * spacing
			p.coord = wireSnap(centre + math.Max(-room/2, math.Min(room/2, offset)))
		}
	}
	// nothing else on either side and the tiles face each other: a straight wire, not a small jog
	onSide := func(p *wirePort) int { return len(bySide[[2]int{p.node, p.dir}]) }
	for k, e := range edges {
		a, b := outPort[k], inPort[k]
		if e.from == e.to || a.count != 1 || onSide(a) != 1 || onSide(b) != 1 {
			continue
		}
		lo := math.Max(centreAcross(a.node, a.dir)-usableSide(nodes[a.node], a.dir)/2, centreAcross(b.node, b.dir)-usableSide(nodes[b.node], b.dir)/2)
		hi := math.Min(centreAcross(a.node, a.dir)+usableSide(nodes[a.node], a.dir)/2, centreAcross(b.node, b.dir)+usableSide(nodes[b.node], b.dir)/2)
		if hi-lo < wireStep {
			continue
		}
		fits := func(v float64) bool { return v >= lo && v <= hi }
		coord := math.Ceil(lo/wireStep) * wireStep
		if fits(a.coord) {
			coord = a.coord
		} else if fits(b.coord) {
			coord = b.coord
		}
		a.coord, b.coord = coord, coord
	}

	// ---- the grid ----
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, n := range nodes {
		minX, minY = math.Min(minX, n.b.x), math.Min(minY, n.b.y)
		maxX, maxY = math.Max(maxX, n.b.x+n.b.w), math.Max(maxY, n.b.y+n.b.h)
	}
	x0 := int(math.Floor(minX/wireStep)) - wireMargin
	y0 := int(math.Floor(minY/wireStep)) - wireMargin
	w := int(math.Ceil(maxX/wireStep)) - x0 + wireMargin + 1
	h := int(math.Ceil(maxY/wireStep)) - y0 + wireMargin + 1
	cell := func(ix, iy int) int { return iy*w + ix }
	hard := make([]bool, w*h)
	soft := make([]bool, w*h)
	mark := func(into []bool, b box, margin float64) {
		fromX := max(0, int(math.Ceil((b.x-margin)/wireStep))-x0)
		toX := min(w-1, int(math.Floor((b.x+b.w+margin)/wireStep))-x0)
		fromY := max(0, int(math.Ceil((b.y-margin)/wireStep))-y0)
		toY := min(h-1, int(math.Floor((b.y+b.h+margin)/wireStep))-y0)
		for iy := fromY; iy <= toY; iy++ {
			for ix := fromX; ix <= toX; ix++ {
				into[cell(ix, iy)] = true
			}
		}
	}
	// a wire may run exactly wireClear from a tile: the first line free of it is where wires start
	for _, n := range nodes {
		mark(hard, n.b, wireClear-1)
	}
	for _, b := range labels {
		mark(soft, b, 4)
	}

	// portCell is the grid crossing just outside a port.
	portCell := func(p *wirePort) int {
		b := nodes[p.node].b
		var ix, iy int
		switch p.dir {
		case 0:
			ix, iy = int(math.Ceil((b.x+b.w+wireClear)/wireStep))-x0, int(wireSnap(p.coord)/wireStep)-y0
		case 2:
			ix, iy = int(math.Floor((b.x-wireClear)/wireStep))-x0, int(wireSnap(p.coord)/wireStep)-y0
		case 1:
			ix, iy = int(wireSnap(p.coord)/wireStep)-x0, int(math.Ceil((b.y+b.h+wireClear)/wireStep))-y0
		default:
			ix, iy = int(wireSnap(p.coord)/wireStep)-x0, int(math.Floor((b.y-wireClear)/wireStep))-y0
		}
		return cell(max(0, min(w-1, ix)), max(0, min(h-1, iy)))
	}
	starts := make([]int, len(edges))
	goals := make([]int, len(edges))
	for k := range edges {
		starts[k], goals[k] = portCell(outPort[k]), portCell(inPort[k])
	}

	// ---- what already runs where ----
	hOcc := make([]int32, w*h)
	vOcc := make([]int32, w*h)
	cellOcc := make([]int32, w*h)
	cornerOcc := make([]int32, w*h)
	occupy := func(arr []int32, i, gid int) {
		if arr[i] == 0 || arr[i] == int32(gid+1) {
			arr[i] = int32(gid + 1)
		} else {
			arr[i] = -1
		}
	}
	paths := make([][]int, len(edges))
	fill := func(skip int) {
		clear(hOcc)
		clear(vOcc)
		clear(cellOcc)
		clear(cornerOcc)
		for k, path := range paths {
			if path == nil || k == skip {
				continue
			}
			gid := groupOf[k]
			prevStep := 0
			for i, c := range path {
				occupy(cellOcc, c, gid)
				if i+1 == len(path) {
					break
				}
				d := path[i+1] - c
				if d == 1 || d == -1 {
					occupy(hOcc, min(c, path[i+1]), gid)
				} else {
					occupy(vOcc, min(c, path[i+1]), gid)
				}
				if prevStep != 0 && prevStep != d {
					occupy(cornerOcc, c, gid)
				}
				prevStep = d
			}
		}
	}

	// ---- the search ----
	states := w * h * 4
	done := states // the state after the last step, so that arriving the right way can count
	dist := make([]float64, states+1)
	prev := make([]int32, states+1)
	var heapF []float64
	var heapS []int32
	heapPush := func(f float64, s int) {
		i := len(heapF)
		heapF, heapS = append(heapF, f), append(heapS, int32(s))
		for i > 0 {
			up := (i - 1) >> 1
			if heapF[up] <= f {
				break
			}
			heapF[i], heapS[i] = heapF[up], heapS[up]
			i = up
		}
		heapF[i], heapS[i] = f, int32(s)
	}
	heapPop := func() int {
		top := heapS[0]
		last := len(heapF) - 1
		f, s := heapF[last], heapS[last]
		heapF, heapS = heapF[:last], heapS[:last]
		if last > 0 {
			i := 0
			for {
				child := 2*i + 1
				if child >= last {
					break
				}
				if child+1 < last && heapF[child+1] < heapF[child] {
					child++
				}
				if heapF[child] >= f {
					break
				}
				heapF[i], heapS[i] = heapF[child], heapS[child]
				i = child
			}
			heapF[i], heapS[i] = f, s
		}
		return int(top)
	}
	abs := func(v int) int {
		if v < 0 {
			return -v
		}
		return v
	}
	search := func(start, startDir, goal, intoDir, gid int) []int {
		for i := range dist {
			dist[i] = math.Inf(1)
		}
		prev[done] = -1
		heapF, heapS = heapF[:0], heapS[:0]
		gx, gy := goal%w, goal/w
		own := int32(gid + 1)
		first := start*4 + startDir
		dist[first] = 0
		prev[first] = -1
		heapPush(0, first)
		for len(heapF) > 0 {
			s := heapPop()
			if s == done {
				break
			}
			g := dist[s]
			dir := s % 4
			c := s / 4
			if c == goal {
				extra := costBend
				if dir == intoDir {
					extra = 0
				} else if (dir+2)%4 == intoDir {
					extra = costWrongWay
				}
				if g+extra < dist[done] {
					dist[done] = g + extra
					prev[done] = int32(s)
					heapPush(g+extra, done)
				}
			}
			ix, iy := c%w, c/w
			for nd := 0; nd < 4; nd++ {
				if nd == (dir+2)%4 {
					continue
				}
				nx, ny := ix+wireDX[nd], iy+wireDY[nd]
				if nx < 0 || ny < 0 || nx >= w || ny >= h {
					continue
				}
				nc := cell(nx, ny)
				cost := wireStep
				if nd != dir {
					cost += costBend
					if cellOcc[c] != 0 && cellOcc[c] != own {
						cost += costOnWire
					}
				}
				on := vOcc[min(c, nc)]
				if wireFlat(nd) {
					on = hOcc[min(c, nc)]
				}
				switch {
				case on == own:
					cost = costShared
					if nd != dir {
						cost += costBend
					}
				case on != 0:
					cost += costAlong
				case cellOcc[nc] != 0 && cellOcc[nc] != own:
					cost += costCrossing
				}
				if cornerOcc[nc] != 0 && cornerOcc[nc] != own {
					cost += costOnWire
				}
				if hard[nc] {
					cost += costThroughTile
				}
				if soft[nc] {
					cost += costUnderLabel
				}
				ns := nc*4 + nd
				if g2 := g + cost; g2 < dist[ns] {
					dist[ns] = g2
					prev[ns] = int32(s)
					heapPush(g2+float64(abs(nx-gx)+abs(ny-gy))*wireStep, ns)
				}
			}
		}
		var out []int
		for s := prev[done]; s != -1; s = prev[s] {
			c := int(s) / 4
			if len(out) == 0 || out[len(out)-1] != c {
				out = append(out, c)
			}
		}
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return out
	}

	// short wires first: the long ones go around them; then each again, knowing all the others
	span := func(k int) int {
		return abs(starts[k]%w-goals[k]%w) + abs(starts[k]/w-goals[k]/w)
	}
	order := make([]int, len(edges))
	for k := range order {
		order[k] = k
	}
	sort.SliceStable(order, func(i, j int) bool { return span(order[i]) < span(order[j]) })
	for pass := 0; pass < wirePasses; pass++ {
		for _, k := range order {
			fill(k)
			paths[k] = search(starts[k], outPort[k].dir, goals[k], (inPort[k].dir+2)%4, groupOf[k])
		}
	}

	// ---- back to board coordinates ----
	point := func(c int) pt { return pt{float64(c%w+x0) * wireStep, float64(c/w+y0) * wireStep} }
	portPoint := func(p *wirePort, inset float64) pt {
		b := nodes[p.node].b
		depth := outlineAt(nodes[p.node], p.dir, p.coord) + inset
		if wireFlat(p.dir) {
			return pt{b.cx() + float64(wireDX[p.dir])*depth, p.coord}
		}
		return pt{p.coord, b.cy() + float64(wireDY[p.dir])*depth}
	}
	wires := make([]wire, len(edges))
	for k, path := range paths {
		route := []pt{portPoint(outPort[k], -1)}
		for _, c := range path {
			route = append(route, point(c))
		}
		wires[k].route = cleanRoute(append(route, portPoint(inPort[k], wireTipGap)))
	}

	// a dot where three ways meet, on the first wire of the group that passes it
	stepBit := func(d int) int {
		switch d {
		case 1:
			return 1
		case w:
			return 2
		case -1:
			return 4
		}
		return 8
	}
	for gid := range groupIDs {
		id := groupIDs[gid]
		bits := map[int]int{}
		var seen []int
		for k, path := range paths {
			if groupOf[k] != id {
				continue
			}
			for i, c := range path {
				if _, ok := bits[c]; !ok {
					seen = append(seen, c)
				}
				if i == 0 {
					bits[c] |= 1 << ((outPort[k].dir + 2) % 4)
				}
				if i+1 < len(path) {
					bits[c] |= stepBit(path[i+1] - c)
				}
				if i > 0 {
					bits[c] |= stepBit(path[i-1] - c)
				}
			}
		}
		for _, c := range seen {
			if bits[c]&1+bits[c]>>1&1+bits[c]>>2&1+bits[c]>>3&1 < 3 {
				continue
			}
		first:
			for k, path := range paths {
				if groupOf[k] != id {
					continue
				}
				for _, on := range path {
					if on == c {
						wires[k].dots = append(wires[k].dots, point(c))
						break first
					}
				}
			}
		}
	}
	return wires
}

// cleanRoute drops repeated points and the points in the middle of a straight stretch.
func cleanRoute(pts []pt) []pt {
	out := make([]pt, 0, len(pts))
	for _, p := range pts {
		if n := len(out); n > 0 && out[n-1] == p {
			continue
		}
		if n := len(out); n > 1 && ((out[n-2].x == out[n-1].x && out[n-1].x == p.x) || (out[n-2].y == out[n-1].y && out[n-1].y == p.y)) {
			out = out[:n-1]
		}
		out = append(out, p)
	}
	return out
}
