// Package board holds the per-workspace board documents (board v2): nodes
// and edges changed through ops, with debounced persistence, a .prev copy,
// hourly snapshots and a never-pruned archive (docs/API-board.md §2).
package board

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
)

const (
	SchemaVersion = 2
	MaxOps        = 500
	MaxDataBytes  = 256 << 10
	MaxBoardBytes = 8 << 20
	MaxCoord      = 20000
	MaxSide       = 20000

	// Default size of a terminal node placed by the hub.
	TermW = 720
	TermH = 440
	// gap between auto-placed nodes.
	placeGap = 40
)

// IDRe validates node, edge and other client-visible ids.
var IDRe = regexp.MustCompile(`^[a-z]_[a-z0-9]{6,24}$`)

var kindRe = regexp.MustCompile(`^[a-z][a-zA-Z0-9_-]{0,31}$`)

// KindTerminal nodes are created and removed by the hub only.
const KindTerminal = "terminal"

// Node is one tile on the board. Nodes are immutable once stored: every
// change replaces the pointer, so readers may marshal outside the lock.
type Node struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	X        float64         `json:"x"`
	Y        float64         `json:"y"`
	W        float64         `json:"w"`
	H        float64         `json:"h"`
	Rotation float64         `json:"rotation,omitempty"`
	Locked   bool            `json:"locked,omitempty"`
	Data     json.RawMessage `json:"data"`

	size int    // encoded size in bytes
	sid  string // data.sessionId of terminal nodes
}

// SessionID returns data.sessionId of a terminal node ("" otherwise).
func (n *Node) SessionID() string { return n.sid }

// Edge connects two nodes.
type Edge struct {
	ID     string          `json:"id"`
	Source string          `json:"source"`
	Target string          `json:"target"`
	Kind   string          `json:"kind"`
	Data   json.RawMessage `json:"data"`

	size int
}

// Board is the API and on-disk form of one workspace board. Array order of
// nodes is z order (last = top).
type Board struct {
	WorkspaceID   string  `json:"workspaceId"`
	SchemaVersion int     `json:"schemaVersion"`
	Rev           int64   `json:"rev"`
	Nodes         []*Node `json:"nodes"`
	Edges         []*Edge `json:"edges"`
}

// Rect is a node position/size (POST /api/sessions "node").
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Op is one board change. Ops in a request apply in order, atomically.
type Op struct {
	Op   string                     `json:"op"`
	Node *Node                      `json:"node,omitempty"`
	ID   string                     `json:"id,omitempty"`
	Set  map[string]json.RawMessage `json:"set,omitempty"`
	To   string                     `json:"to,omitempty"`
	Edge *Edge                      `json:"edge,omitempty"`
}

// ValidationError is a rejected op batch (HTTP 400, or Status with a
// stable Code for hub-validated kinds of phase 6: name_conflict 409,
// member_conflict / bad_edge / forward_cycle 400).
type ValidationError struct {
	Msg    string
	Code   string
	Status int
}

func (e *ValidationError) Error() string { return e.Msg }

// Invalid builds a coded validation error (status 0 = 400).
func Invalid(status int, code, format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...), Code: code, Status: status}
}

func invalid(format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...)}
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Validate checks a rectangle against the board limits.
func (r Rect) Validate() error {
	for _, f := range []float64{r.X, r.Y, r.W, r.H} {
		if !finite(f) {
			return invalid("angka harus finite")
		}
	}
	if math.Abs(r.X) > MaxCoord || math.Abs(r.Y) > MaxCoord {
		return invalid("x/y di luar batas ±%d", MaxCoord)
	}
	if r.W < 1 || r.W > MaxSide || r.H < 1 || r.H > MaxSide {
		return invalid("w/h harus 1…%d", MaxSide)
	}
	return nil
}

// normObject validates that raw is a JSON object (null/empty → {}) of at
// most MaxDataBytes, and returns it compacted.
func normObject(raw json.RawMessage, what string) (json.RawMessage, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return json.RawMessage("{}"), nil
	}
	if t[0] != '{' {
		return nil, invalid("%s harus objek JSON", what)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, t); err != nil {
		return nil, invalid("%s bukan JSON valid", what)
	}
	if buf.Len() > MaxDataBytes {
		return nil, invalid("%s lebih dari %d KB", what, MaxDataBytes>>10)
	}
	return json.RawMessage(buf.Bytes()), nil
}

// prepare validates a node from a client (or disk) and fills derived
// fields. It returns a fresh copy.
func prepare(in *Node) (*Node, error) {
	if in == nil {
		return nil, invalid("node kosong")
	}
	n := *in
	if !IDRe.MatchString(n.ID) {
		return nil, invalid("id node tidak valid: %q", n.ID)
	}
	if !kindRe.MatchString(n.Kind) {
		return nil, invalid("kind node tidak valid: %q", n.Kind)
	}
	if err := (Rect{n.X, n.Y, n.W, n.H}).Validate(); err != nil {
		return nil, err
	}
	if !finite(n.Rotation) {
		return nil, invalid("rotation harus finite")
	}
	data, err := normObject(n.Data, "data")
	if err != nil {
		return nil, err
	}
	n.Data = data
	n.sid = ""
	if n.Kind == KindTerminal {
		var d struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(data, &d)
		if d.SessionID == "" {
			return nil, invalid("node terminal wajib punya data.sessionId")
		}
		n.sid = d.SessionID
	}
	b, _ := json.Marshal(&n)
	n.size = len(b)
	return &n, nil
}

func prepareEdge(in *Edge) (*Edge, error) {
	if in == nil {
		return nil, invalid("edge kosong")
	}
	e := *in
	if !IDRe.MatchString(e.ID) {
		return nil, invalid("id edge tidak valid: %q", e.ID)
	}
	if !IDRe.MatchString(e.Source) || !IDRe.MatchString(e.Target) {
		return nil, invalid("source/target edge tidak valid")
	}
	if e.Kind == "" {
		e.Kind = "wire"
	}
	if !kindRe.MatchString(e.Kind) {
		return nil, invalid("kind edge tidak valid: %q", e.Kind)
	}
	data, err := normObject(e.Data, "data edge")
	if err != nil {
		return nil, err
	}
	e.Data = data
	b, _ := json.Marshal(&e)
	e.size = len(b)
	return &e, nil
}

// patchNode applies a patch "set" to a copy of old: top-level fields are
// replaced, "data" is merged key by key (a null value removes the key).
func patchNode(old *Node, set map[string]json.RawMessage) (*Node, map[string]json.RawMessage, error) {
	if len(set) == 0 {
		return nil, nil, invalid("patch tanpa set")
	}
	n := *old
	norm := make(map[string]json.RawMessage, len(set))
	for k, raw := range set {
		switch k {
		case "x", "y", "w", "h", "rotation":
			var f float64
			if json.Unmarshal(raw, &f) != nil {
				return nil, nil, invalid("set.%s harus angka", k)
			}
			switch k {
			case "x":
				n.X = f
			case "y":
				n.Y = f
			case "w":
				n.W = f
			case "h":
				n.H = f
			case "rotation":
				n.Rotation = f
			}
			norm[k], _ = json.Marshal(f)
		case "locked":
			var b bool
			if json.Unmarshal(raw, &b) != nil {
				return nil, nil, invalid("set.locked harus boolean")
			}
			n.Locked = b
			norm[k], _ = json.Marshal(b)
		case "data":
			patch, err := normObject(raw, "set.data")
			if err != nil {
				return nil, nil, err
			}
			merged, err := mergeData(old.Data, patch)
			if err != nil {
				return nil, nil, err
			}
			n.Data = merged
			norm[k] = patch
		default:
			return nil, nil, invalid("field %q tidak bisa di-patch", k)
		}
	}
	out, err := prepare(&n)
	if err != nil {
		return nil, nil, err
	}
	return out, norm, nil
}

func mergeData(base, patch json.RawMessage) (json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(base, &m); err != nil || m == nil {
		m = map[string]json.RawMessage{}
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, invalid("set.data bukan objek JSON")
	}
	for k, v := range p {
		if string(bytes.TrimSpace(v)) == "null" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, invalid("set.data tidak valid")
	}
	return b, nil
}

// randomID returns prefix + 10 chars of the id alphabet.
func randomID(prefix string) string {
	const alpha = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return prefix + string(b)
}
