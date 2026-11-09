package orch

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/azamrizki/nextboard/internal/board"
)

// Choosing how many consoles a group of a layout has, before it is deployed
// (docs/API-orchestration.md §13). The hub does the geometry, for the preview and the deploy
// alike, so that what the owner sees is what lands.

// sizedBlock is what moves as one when a group changes size: a group with its frame, or a tile.
type sizedBlock struct {
	group   string // group key, or "" for a loose lane or a card
	lane    int    // index in lanes, or -1
	node    int    // index in nodes, or -1
	old     board.Rect
	now     board.Rect
	changed bool
	removed bool
	dx, dy  float64
}

// Sized returns the layout with these groups at these sizes. A group of 0 goes away with its
// wires, as long as the layout keeps a console. Without a change it returns the layout as it is.
func (l Layout) Sized(sizes map[string]int) (Layout, error) {
	current := map[string]int{}
	for _, ln := range l.Lanes {
		current[ln.Group]++
	}
	known := map[string]bool{}
	for _, g := range l.Groups {
		known[g.Key] = true
	}
	changed, total := false, len(l.Lanes)
	for key, n := range sizes {
		if !known[key] {
			return Layout{}, fmt.Errorf("%w: no group %q", ErrBadLayout, key)
		}
		if n < 0 {
			return Layout{}, fmt.Errorf("%w: a group cannot have %d consoles", ErrBadLayout, n)
		}
		if n > maxMembers {
			return Layout{}, ErrTooMany
		}
		if n != current[key] {
			changed = true
		}
		total += n - current[key]
	}
	if !changed {
		return l, nil
	}
	if total > maxSpawn {
		return Layout{}, ErrTooMany
	}
	if total == 0 {
		return Layout{}, fmt.Errorf("%w: a layout keeps at least one console", ErrBadLayout)
	}

	// The blocks as they are now, before anything moves.
	var blocks []*sizedBlock
	groupBlock := map[string]*sizedBlock{}
	for _, g := range l.Groups {
		b := &sizedBlock{group: g.Key, lane: -1, node: -1, old: laneFrame(lanesOf(l.Lanes, g.Key))}
		blocks = append(blocks, b)
		groupBlock[g.Key] = b
	}
	for i, ln := range l.Lanes {
		if !known[ln.Group] {
			blocks = append(blocks, &sizedBlock{lane: i, node: -1, old: rectOf(ln.X, ln.Y, ln.W, ln.H)})
		}
	}
	for i, n := range l.Nodes {
		blocks = append(blocks, &sizedBlock{lane: -1, node: i, old: rectOf(n.X, n.Y, n.W, n.H)})
	}

	// Each resized group, laid out again from its top left corner.
	lanes := map[string][]Lane{}
	taken := map[string]bool{}
	for _, ln := range l.Lanes {
		taken[ln.Key] = true
	}
	for _, g := range l.Groups {
		cur := lanesOf(l.Lanes, g.Key)
		lanes[g.Key] = cur
		n, asked := sizes[g.Key]
		b := groupBlock[g.Key]
		b.now = b.old
		if !asked || n == len(cur) {
			continue
		}
		b.changed = true
		if n == 0 {
			lanes[g.Key] = nil
			b.removed = true
			b.now.W, b.now.H = 0, 0
			continue
		}
		next := regrid(cur, g.Key, n, b.old, taken)
		lanes[g.Key] = next
		b.now = laneFrame(next)
		b.now.X, b.now.Y = b.old.X, b.old.Y
	}
	for _, b := range blocks {
		if b.group == "" {
			b.now = b.old
		}
	}
	reflow(blocks)

	// Everything in its new place.
	out := l
	out.Lanes, out.Nodes, out.Groups, out.Wires = []Lane{}, make([]LayoutNode, len(l.Nodes)), []LayoutGroup{}, []LayoutWire{}
	present := map[string]bool{}
	for _, g := range l.Groups {
		b := groupBlock[g.Key]
		if b.removed {
			continue
		}
		out.Groups = append(out.Groups, g)
		present[g.Key] = true
		base := laneFrame(lanes[g.Key])
		for _, ln := range lanes[g.Key] {
			ln.X += b.now.X - base.X
			ln.Y += b.now.Y - base.Y
			out.Lanes = append(out.Lanes, ln)
			present[ln.Key] = true
		}
	}
	for _, b := range blocks {
		switch {
		case b.lane >= 0:
			ln := l.Lanes[b.lane]
			ln.X, ln.Y = ln.X+b.now.X-b.old.X, ln.Y+b.now.Y-b.old.Y
			out.Lanes = append(out.Lanes, ln)
			present[ln.Key] = true
		case b.node >= 0:
			n := l.Nodes[b.node]
			n.X, n.Y = n.X+b.now.X-b.old.X, n.Y+b.now.Y-b.old.Y
			out.Nodes[b.node] = n
			present[n.Key] = true
		}
	}
	for _, w := range l.Wires {
		if present[w.From] && present[w.To] {
			out.Wires = append(out.Wires, w)
		}
	}
	fill(&out)
	return out, nil
}

func lanesOf(all []Lane, group string) []Lane {
	var out []Lane
	for _, ln := range all {
		if ln.Group == group {
			out = append(out, ln)
		}
	}
	return out
}

func rectOf(x, y, w, h float64) board.Rect { return board.Rect{X: x, Y: y, W: w, H: h} }

// laneFrame is the frame a group has around these lanes on the board.
func laneFrame(lanes []Lane) board.Rect {
	nodes := make([]*board.Node, 0, len(lanes))
	for _, ln := range lanes {
		nodes = append(nodes, &board.Node{X: ln.X, Y: ln.Y, W: ln.W, H: ln.H})
	}
	return GroupFrame(nodes)
}

// regrid lays a group out again. The first lanes stay themselves, keeping what was chosen for
// them. New ones take after the last. A stack stays a stack, and anything else goes by the shapes
// the built-in teams have: a row up to 3, then two rows.
func regrid(cur []Lane, group string, n int, frame board.Rect, taken map[string]bool) []Lane {
	w, h := float64(laneW), float64(laneH)
	base := Lane{Group: group, Key: group + "l"}
	if len(cur) > 0 {
		base = cur[len(cur)-1]
		w, h = cur[0].W, cur[0].H
	}
	columns, rows := map[float64]bool{}, map[float64]bool{}
	for _, ln := range cur {
		columns[ln.X] = true
		rows[ln.Y] = true
	}
	cols := columnsFor(n)
	if len(columns) == 1 && len(rows) > 1 {
		cols = 1
	}
	x0, y0 := frame.X+24, frame.Y+24+32
	prefix := strings.TrimRight(base.Key, "0123456789")
	out := make([]Lane, 0, n)
	for i := 0; i < n; i++ {
		ln := base
		if i < len(cur) {
			ln = cur[i]
		} else {
			key := prefix + strconv.Itoa(i+1)
			for j := i + 2; taken[key]; j++ {
				key = prefix + strconv.Itoa(j)
			}
			taken[key] = true
			ln.Key = key
		}
		ln.W, ln.H = w, h
		ln.X = x0 + float64(i%cols)*(w+laneGap)
		ln.Y = y0 + float64(i/cols)*(h+laneGap)
		out = append(out, ln)
	}
	return out
}

func columnsFor(n int) int {
	switch {
	case n <= 3:
		return max(n, 1)
	case n == 4:
		return 2
	case n <= 6:
		return 3
	}
	return 4
}

func rangesMeet(a0, a1, b0, b1 float64) bool { return a0 < b1 && b0 < a1 }

func rectsMeet(a, b board.Rect) bool {
	return rangesMeet(a.X, a.X+a.W, b.X, b.X+b.W) && rangesMeet(a.Y, a.Y+a.H, b.Y, b.Y+b.H)
}

// reflow moves what was beside or below a changed group by what the group gained or lost.
// What a smaller group frees up closes only if nothing lands on anything by that, since blocks
// in other rows may hold the place. Anything that still lands on something is pushed clear.
func reflow(blocks []*sizedBlock) {
	apply := func(growOnly bool) bool {
		for _, b := range blocks {
			b.dx, b.dy = 0, 0
		}
		for _, a := range blocks {
			if !a.changed {
				continue
			}
			dw, dh := a.now.W-a.old.W, a.now.H-a.old.H
			if a.removed {
				dw, dh = -a.old.W-laneGap, -a.old.H-laneGap
			}
			if growOnly {
				dw, dh = max(dw, 0), max(dh, 0)
			}
			// the place the group takes now counts as well as the place it had
			right, bottom := max(a.old.X+a.old.W, a.now.X+a.now.W), max(a.old.Y+a.old.H, a.now.Y+a.now.H)
			for _, b := range blocks {
				if b == a || b.removed {
					continue
				}
				if b.old.X >= a.old.X+a.old.W && rangesMeet(b.old.Y, b.old.Y+b.old.H, a.old.Y, bottom) {
					b.dx += dw
				}
				if b.old.Y >= a.old.Y+a.old.H && rangesMeet(b.old.X, b.old.X+b.old.W, a.old.X, right) {
					b.dy += dh
				}
			}
		}
		for _, b := range blocks {
			if b.removed {
				continue
			}
			b.now.X, b.now.Y = b.old.X+b.dx, b.old.Y+b.dy
		}
		return !anyNewOverlap(blocks)
	}
	if apply(false) {
		return
	}
	if apply(true) {
		return
	}
	pushClear(blocks)
}

func anyNewOverlap(blocks []*sizedBlock) bool {
	for i, a := range blocks {
		for _, b := range blocks[i+1:] {
			if !a.removed && !b.removed && rectsMeet(a.now, b.now) && !rectsMeet(a.old, b.old) {
				return true
			}
		}
	}
	return false
}

// pushClear moves whatever lands on something to its right, until nothing does. Tiles the owner
// had overlapping on purpose stay that way.
func pushClear(blocks []*sizedBlock) {
	for pass := 0; pass < len(blocks)*len(blocks)+1; pass++ {
		moved := false
		for i, a := range blocks {
			for _, b := range blocks[i+1:] {
				if a.removed || b.removed || !rectsMeet(a.now, b.now) || rectsMeet(a.old, b.old) {
					continue
				}
				first, second := a, b
				if b.now.X < a.now.X {
					first, second = b, a
				}
				second.now.X = first.now.X + first.now.W + laneGap
				moved = true
			}
		}
		if !moved {
			return
		}
	}
}
