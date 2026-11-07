package board

import (
	"encoding/json"
	"slices"
)

// work is a copy-on-write view of a board used to apply one op batch: the
// slices are copied, nodes/edges are shared and never mutated.
type work struct {
	wid   string
	nodes []*Node
	edges []*Edge
	// sessionOK reports whether a session may own a terminal node here.
	sessionOK func(sessionID string) bool
}

func (w *work) findNode(id string) int {
	return slices.IndexFunc(w.nodes, func(n *Node) bool { return n.ID == id })
}

func (w *work) findEdge(id string) int {
	return slices.IndexFunc(w.edges, func(e *Edge) bool { return e.ID == id })
}

func (w *work) terminalFor(sid string) int {
	return slices.IndexFunc(w.nodes, func(n *Node) bool { return n.sid == sid })
}

func (w *work) size() int {
	t := 0
	for _, n := range w.nodes {
		t += n.size + 1
	}
	for _, e := range w.edges {
		t += e.size + 1
	}
	return t
}

// removeNode deletes node i and every edge touching it, returning the
// delEdge ops for the removed edges.
func (w *work) removeNode(i int) []Op {
	id := w.nodes[i].ID
	w.nodes = slices.Delete(w.nodes, i, i+1)
	var out []Op
	w.edges = slices.DeleteFunc(w.edges, func(e *Edge) bool {
		if e.Source == id || e.Target == id {
			out = append(out, Op{Op: "delEdge", ID: e.ID})
			return true
		}
		return false
	})
	return out
}

// apply runs one client op. It returns the normalized ops to broadcast
// (none when the op was a no-op, e.g. it targets a node that is already
// gone, or deletes a terminal node).
func (w *work) apply(op Op) ([]Op, error) {
	switch op.Op {
	case "put":
		n, err := prepare(op.Node)
		if err != nil {
			return nil, err
		}
		i := w.findNode(n.ID)
		if n.Kind == KindTerminal {
			if w.sessionOK == nil || !w.sessionOK(n.sid) {
				return nil, invalid("session %q is not in this workspace", n.sid)
			}
			if j := w.terminalFor(n.sid); j >= 0 && j != i {
				return nil, invalid("session %q already has a terminal node", n.sid)
			}
		}
		if i >= 0 {
			old := w.nodes[i]
			if old.Kind == KindTerminal && (n.Kind != KindTerminal || n.sid != old.sid) {
				return nil, invalid("a terminal node cannot be replaced with put")
			}
			w.nodes[i] = n // keeps its z position
		} else {
			w.nodes = append(w.nodes, n)
		}
		return []Op{{Op: "put", Node: n}}, nil

	case "patch":
		if !IDRe.MatchString(op.ID) {
			return nil, invalid("invalid node id: %q", op.ID)
		}
		i := w.findNode(op.ID)
		if i < 0 {
			return nil, nil
		}
		old := w.nodes[i]
		n, set, err := patchNode(old, op.Set)
		if err != nil {
			return nil, err
		}
		if old.Kind == KindTerminal && n.sid != old.sid {
			return nil, invalid("the data.sessionId of a terminal node cannot be changed")
		}
		w.nodes[i] = n
		return []Op{{Op: "patch", ID: op.ID, Set: set}}, nil

	case "del":
		if !IDRe.MatchString(op.ID) {
			return nil, invalid("invalid node id: %q", op.ID)
		}
		i := w.findNode(op.ID)
		if i < 0 || w.nodes[i].Kind == KindTerminal {
			return nil, nil // terminal nodes go away with DELETE /api/sessions
		}
		return append([]Op{{Op: "del", ID: op.ID}}, w.removeNode(i)...), nil

	case "z":
		if !IDRe.MatchString(op.ID) {
			return nil, invalid("invalid node id: %q", op.ID)
		}
		if op.To != "front" && op.To != "back" {
			return nil, invalid(`z.to must be "front" or "back"`)
		}
		i := w.findNode(op.ID)
		if i < 0 {
			return nil, nil
		}
		n := w.nodes[i]
		w.nodes = slices.Delete(w.nodes, i, i+1)
		if op.To == "front" {
			w.nodes = append(w.nodes, n)
		} else {
			w.nodes = slices.Insert(w.nodes, 0, n)
		}
		return []Op{{Op: "z", ID: op.ID, To: op.To}}, nil

	case "putEdge":
		e, err := prepareEdge(op.Edge)
		if err != nil {
			return nil, err
		}
		if w.findNode(e.Source) < 0 || w.findNode(e.Target) < 0 {
			return nil, invalid("edge %q points to a node that does not exist", e.ID)
		}
		if i := w.findEdge(e.ID); i >= 0 {
			w.edges[i] = e
		} else {
			w.edges = append(w.edges, e)
		}
		return []Op{{Op: "putEdge", Edge: e}}, nil

	case "delEdge":
		if !IDRe.MatchString(op.ID) {
			return nil, invalid("invalid edge id: %q", op.ID)
		}
		i := w.findEdge(op.ID)
		if i < 0 {
			return nil, nil
		}
		w.edges = slices.Delete(w.edges, i, i+1)
		return []Op{{Op: "delEdge", ID: op.ID}}, nil
	}
	return nil, invalid("unknown op: %q", op.Op)
}

// contentCount counts non-terminal nodes (what an archive preserves).
func contentCount(nodes []*Node) int {
	n := 0
	for _, x := range nodes {
		if x.Kind != KindTerminal {
			n++
		}
	}
	return n
}

// KindGroup nodes list terminal node ids in data.members (phase 6).
const KindGroup = "group"

// dropMember removes a terminal node id from the members of every group
// (docs/API-orchestration.md §1: a deleted terminal leaves its group).
func (w *work) dropMember(id string) []Op {
	var out []Op
	for i, n := range w.nodes {
		if n.Kind != KindGroup {
			continue
		}
		var d struct {
			Members []string `json:"members"`
		}
		if json.Unmarshal(n.Data, &d) != nil || !slices.Contains(d.Members, id) {
			continue
		}
		members := slices.DeleteFunc(slices.Clone(d.Members), func(x string) bool { return x == id })
		raw, _ := json.Marshal(members)
		set := map[string]json.RawMessage{"data": json.RawMessage(`{"members":` + string(raw) + `}`)}
		nn, norm, err := patchNode(n, set)
		if err != nil {
			continue
		}
		w.nodes[i] = nn
		out = append(out, Op{Op: "patch", ID: n.ID, Set: norm})
	}
	return out
}
