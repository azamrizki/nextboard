package board

import (
	"encoding/json"
	"errors"
	"log"
	"math"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/store"
)

var (
	// ErrNotFound: the workspace board was dropped (workspace deleted).
	ErrNotFound = errors.New("board not found")
	// ErrReadOnly: the board was saved by a newer hub (schemaVersion > 2).
	ErrReadOnly = errors.New("the board was saved by a newer hub version; it cannot be changed")
)

// Manager owns every loaded board. The hub is authoritative: each board
// has its own mutex, and every change bumps rev and is reported through
// OnOps in rev order.
type Manager struct {
	st *store.Store

	// SessionWorkspace reports the workspace of a live session. Terminal
	// nodes are only valid for sessions of the board's own workspace.
	SessionWorkspace func(sessionID string) (workspaceID string, ok bool)
	// Names returns display names for archive records.
	Names func(workspaceID string) (project, workspace string)
	// OnOps is called under the board lock after every change; clientID is
	// "" for hub-originated ops.
	OnOps func(workspaceID string, rev int64, clientID string, ops []Op)
	// Validate checks hub-understood kinds (group, orchestrator, memory,
	// wire/forward edges) on the board that a batch would produce;
	// touched holds the ids of nodes and edges the batch changed. Called
	// under the board lock: it must not call back into the manager.
	Validate func(workspaceID string, nodes []*Node, edges []*Edge, touched map[string]bool) error

	// SaveDelay/MaxDelay: debounce after the last change, capped.
	SaveDelay time.Duration
	MaxDelay  time.Duration
	now       func() time.Time

	mu      sync.Mutex
	docs    map[string]*doc
	dropped map[string]bool
	closed  bool

	metaMu   sync.Mutex
	archMeta map[string]ArchiveMeta
	snapMeta map[string]SnapshotMeta // key: wid + "/" + id
}

type doc struct {
	m   *Manager
	wid string

	saveMu    sync.Mutex // serialises disk writes; taken before mu
	diskNodes int        // node count of the version on disk (guarded by saveMu)

	mu         sync.Mutex
	rev        int64
	nodes      []*Node
	edges      []*Edge
	readOnly   bool
	dropped    bool
	dirty      bool
	dirtySince time.Time
	timer      *time.Timer
}

// NewManager creates the board manager rooted at the data directory.
func NewManager(st *store.Store) *Manager {
	for _, d := range []string{"boards", "snapshots", "archive"} {
		_ = os.MkdirAll(st.Path(d), 0o700)
	}
	return &Manager{
		st: st, SaveDelay: 2 * time.Second, MaxDelay: 7 * time.Second, now: time.Now,
		docs: map[string]*doc{}, dropped: map[string]bool{},
		archMeta: map[string]ArchiveMeta{}, snapMeta: map[string]SnapshotMeta{},
	}
}

func (m *Manager) boardPath(wid string) string { return m.st.Path("boards", wid+".json") }

// doc returns the loaded board, loading it from disk on first use. With
// create=false a board that has never been saved yields (nil, nil).
func (m *Manager) doc(wid string, create bool) (*doc, error) {
	if !IDRe.MatchString(wid) {
		return nil, ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dropped[wid] {
		return nil, ErrNotFound
	}
	if d := m.docs[wid]; d != nil {
		return d, nil
	}
	d, exists := m.load(wid)
	if !exists && !create {
		return nil, nil
	}
	m.docs[wid] = d
	return d, nil
}

// load reads boards/<wid>.json, falling back to .prev. Unreadable files are
// archived (reason "unreadable") rather than lost. Terminal nodes whose
// session is gone (or lives in another workspace) are dropped.
func (m *Manager) load(wid string) (*doc, bool) {
	d := &doc{m: m, wid: wid}
	p := m.boardPath(wid)
	main, errMain := os.ReadFile(p)
	var b Board
	ok := errMain == nil && json.Unmarshal(main, &b) == nil
	recovered := !ok
	if !ok {
		prev, errPrev := os.ReadFile(p + ".prev")
		if errMain != nil && errPrev != nil {
			return d, false
		}
		if errMain == nil {
			log.Printf("board %s is unreadable; archived", wid)
			m.archiveRaw(wid, main)
		}
		b = Board{}
		if errPrev == nil {
			if ok = json.Unmarshal(prev, &b) == nil; !ok {
				b = Board{}
				m.archiveRaw(wid, prev)
			}
		}
	}
	d.rev = b.Rev
	d.readOnly = b.SchemaVersion > SchemaVersion
	d.diskNodes = len(b.Nodes)
	changed := d.clean(b.Nodes, b.Edges) || recovered
	if changed && !d.readOnly {
		d.markDirtyLocked()
	}
	return d, true
}

// clean validates nodes/edges loaded from disk or a snapshot, dropping
// invalid ones, duplicate ids and stale terminal nodes. It reports whether
// anything was dropped.
func (d *doc) clean(nodes []*Node, edges []*Edge) bool {
	changed := false
	seen := map[string]bool{}
	sids := map[string]bool{}
	d.nodes = d.nodes[:0:0]
	for _, raw := range nodes {
		n, err := prepare(raw)
		if err != nil || seen[n.ID] || (n.Kind == KindTerminal && (sids[n.sid] || !d.m.sessionOK(d.wid, n.sid))) {
			changed = true
			continue
		}
		seen[n.ID] = true
		if n.sid != "" {
			sids[n.sid] = true
		}
		d.nodes = append(d.nodes, n)
	}
	d.edges = d.edges[:0:0]
	eseen := map[string]bool{}
	for _, raw := range edges {
		e, err := prepareEdge(raw)
		if err != nil || eseen[e.ID] || !seen[e.Source] || !seen[e.Target] {
			changed = true
			continue
		}
		eseen[e.ID] = true
		d.edges = append(d.edges, e)
	}
	return changed
}

func (m *Manager) sessionOK(wid, sid string) bool {
	if m.SessionWorkspace == nil {
		return false
	}
	w, ok := m.SessionWorkspace(sid)
	return ok && w == wid
}

func (d *doc) boardLocked() Board {
	return Board{
		WorkspaceID: d.wid, SchemaVersion: SchemaVersion, Rev: d.rev,
		Nodes: slices.Clone(d.nodes), Edges: slices.Clone(d.edges),
	}
}

func (d *doc) workLocked() *work {
	return &work{
		wid: d.wid, nodes: slices.Clone(d.nodes), edges: slices.Clone(d.edges),
		sessionOK: func(sid string) bool { return d.m.sessionOK(d.wid, sid) },
	}
}

// commitLocked installs a batch result, bumps rev and broadcasts.
func (d *doc) commitLocked(w *work, clientID string, ops []Op) {
	d.nodes, d.edges = w.nodes, w.edges
	d.rev++
	if d.m.OnOps != nil {
		d.m.OnOps(d.wid, d.rev, clientID, ops)
	}
	d.markDirtyLocked()
}

// Get returns a copy of the board (an empty board if never saved).
func (m *Manager) Get(wid string) (Board, error) {
	d, err := m.doc(wid, true)
	if err != nil {
		return Board{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dropped {
		return Board{}, ErrNotFound
	}
	b := d.boardLocked()
	if b.Nodes == nil {
		b.Nodes = []*Node{}
	}
	if b.Edges == nil {
		b.Edges = []*Edge{}
	}
	return b, nil
}

// Apply runs a client op batch atomically and returns the new rev. Ops on
// nodes that no longer exist are skipped; invalid ops reject the whole
// batch with a *ValidationError.
func (m *Manager) Apply(wid, clientID string, ops []Op) (int64, error) {
	if len(ops) > MaxOps {
		return 0, invalid("too many ops (at most %d)", MaxOps)
	}
	d, err := m.doc(wid, true)
	if err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dropped {
		return 0, ErrNotFound
	}
	if d.readOnly {
		return 0, ErrReadOnly
	}
	w := d.workLocked()
	var applied []Op
	for i, op := range ops {
		out, err := w.apply(op)
		if err != nil {
			var ve *ValidationError
			if errors.As(err, &ve) {
				return 0, invalid("op #%d (%s): %s", i, op.Op, ve.Msg)
			}
			return 0, err
		}
		applied = append(applied, out...)
	}
	if len(applied) == 0 {
		return d.rev, nil
	}
	if m.Validate != nil {
		touched := map[string]bool{}
		for _, op := range applied {
			switch {
			case op.Node != nil:
				touched[op.Node.ID] = true
			case op.Edge != nil:
				touched[op.Edge.ID] = true
			case op.ID != "":
				touched[op.ID] = true
			}
		}
		if err := m.Validate(wid, w.nodes, w.edges, touched); err != nil {
			return 0, err
		}
	}
	if w.size() > MaxBoardBytes {
		return 0, invalid("the board is too large (at most %d MB)", MaxBoardBytes>>20)
	}
	// A board emptied in one go goes to the archive first.
	if before := contentCount(d.nodes); before >= 3 && contentCount(w.nodes) == 0 {
		pn, wn := m.names(wid)
		if _, err := m.writeArchive(d.boardLocked(), pn, wn, ReasonCleared); err != nil {
			log.Printf("archive of board %s failed: %v", wid, err)
		}
	}
	d.commitLocked(w, clientID, applied)
	return d.rev, nil
}

func (m *Manager) names(wid string) (string, string) {
	if m.Names == nil {
		return "", ""
	}
	return m.Names(wid)
}

// AddTerminal places the terminal node of a session (at r, or right of the
// last node) and broadcasts it. It is idempotent per session.
func (m *Manager) AddTerminal(wid, sessionID string, r *Rect) (*Node, error) {
	if r != nil {
		if err := r.Validate(); err != nil {
			return nil, err
		}
	}
	d, err := m.doc(wid, true)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dropped {
		return nil, ErrNotFound
	}
	if d.readOnly {
		return nil, ErrReadOnly
	}
	w := d.workLocked()
	if i := w.terminalFor(sessionID); i >= 0 {
		return w.nodes[i], nil
	}
	rect := autoPlace(d.nodes)
	if r != nil {
		rect = *r
	}
	data, _ := json.Marshal(map[string]string{"sessionId": sessionID})
	n, err := prepare(&Node{ID: randomID("n_"), Kind: KindTerminal, X: rect.X, Y: rect.Y, W: rect.W, H: rect.H, Data: data})
	if err != nil {
		return nil, err
	}
	w.nodes = append(w.nodes, n)
	d.commitLocked(w, "", []Op{{Op: "put", Node: n}})
	return n, nil
}

// autoPlace puts a new terminal right of the last terminal (or last node),
// wrapping to a new row when it would leave the board.
func autoPlace(nodes []*Node) Rect {
	r := Rect{W: TermW, H: TermH}
	if len(nodes) == 0 {
		return r
	}
	last := nodes[len(nodes)-1]
	for i := len(nodes) - 1; i >= 0; i-- {
		if nodes[i].Kind == KindTerminal {
			last = nodes[i]
			break
		}
	}
	r.X, r.Y = last.X+last.W+placeGap, last.Y
	if r.X+r.W > MaxCoord {
		bottom := 0.0
		for _, n := range nodes {
			bottom = math.Max(bottom, n.Y+n.H)
		}
		r.X, r.Y = 0, bottom+placeGap
	}
	r.X = math.Min(r.X, MaxCoord)
	r.Y = math.Min(r.Y, MaxCoord)
	return r
}

// RemoveTerminal deletes the terminal node(s) of a session and broadcasts.
func (m *Manager) RemoveTerminal(wid, sessionID string) error {
	d, err := m.doc(wid, false)
	if err != nil || d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dropped || d.readOnly {
		return nil
	}
	w := d.workLocked()
	var ops []Op
	for {
		i := w.terminalFor(sessionID)
		if i < 0 {
			break
		}
		id := w.nodes[i].ID
		ops = append(ops, Op{Op: "del", ID: id})
		ops = append(ops, w.removeNode(i)...)
		ops = append(ops, w.dropMember(id)...)
	}
	if len(ops) > 0 {
		d.commitLocked(w, "", ops)
	}
	return nil
}

// HasTerminal reports whether the board shows a node for the session.
func (m *Manager) HasTerminal(wid, sessionID string) bool {
	d, err := m.doc(wid, false)
	if err != nil || d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.ContainsFunc(d.nodes, func(n *Node) bool { return n.sid == sessionID })
}

// Drop forgets a board for good (workspace deleted): pending saves are
// cancelled and its files and snapshots removed. Archive first if needed.
func (m *Manager) Drop(wid string) {
	if !IDRe.MatchString(wid) {
		return
	}
	m.mu.Lock()
	m.dropped[wid] = true
	d := m.docs[wid]
	delete(m.docs, wid)
	m.mu.Unlock()
	if d != nil {
		d.saveMu.Lock()
		defer d.saveMu.Unlock()
		d.mu.Lock()
		d.dropped = true
		if d.timer != nil {
			d.timer.Stop()
		}
		d.mu.Unlock()
	}
	p := m.boardPath(wid)
	_ = os.Remove(p)
	_ = os.Remove(p + ".prev")
	_ = os.RemoveAll(m.st.Path("snapshots", wid))
	m.metaMu.Lock()
	for k := range m.snapMeta {
		if len(k) > len(wid) && k[:len(wid)+1] == wid+"/" {
			delete(m.snapMeta, k)
		}
	}
	m.metaMu.Unlock()
}

// Flush writes every dirty board now.
func (m *Manager) Flush() {
	m.mu.Lock()
	docs := make([]*doc, 0, len(m.docs))
	for _, d := range m.docs {
		docs = append(docs, d)
	}
	m.mu.Unlock()
	for _, d := range docs {
		d.save(true)
	}
}

// Close flushes everything and stops background saves.
func (m *Manager) Close() {
	m.Flush()
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// markDirtyLocked schedules a save SaveDelay after the last change, but
// never later than MaxDelay after the first unsaved change.
func (d *doc) markDirtyLocked() {
	now := d.m.now()
	if !d.dirty {
		d.dirty = true
		d.dirtySince = now
	}
	delay := d.m.SaveDelay
	if left := d.m.MaxDelay - now.Sub(d.dirtySince); left < delay {
		delay = max(left, 0)
	}
	if d.timer == nil {
		d.timer = time.AfterFunc(delay, func() { d.save(false) })
	} else {
		d.timer.Reset(delay)
	}
}

// save persists the board if dirty. force also runs after Close.
func (d *doc) save(force bool) {
	if !force && d.m.isClosed() {
		return
	}
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	if !d.dirty || d.dropped || d.readOnly {
		d.mu.Unlock()
		return
	}
	b := d.boardLocked()
	d.dirty = false
	if d.timer != nil {
		d.timer.Stop()
	}
	d.mu.Unlock()
	data, err := json.Marshal(b)
	if err == nil {
		err = d.m.writeBoard(d, data, len(b.Nodes))
	}
	if err != nil {
		log.Printf("saving board %s failed: %v", d.wid, err)
		d.mu.Lock()
		if !d.dropped {
			d.markDirtyLocked()
		}
		d.mu.Unlock()
	}
}

// writeBoard rotates the current file to .prev (taking the hourly snapshot
// from it first) and writes the new version. Caller holds d.saveMu.
func (m *Manager) writeBoard(d *doc, data []byte, nodes int) error {
	p := m.boardPath(d.wid)
	if old, err := os.ReadFile(p); err == nil && json.Valid(old) {
		m.hourlySnapshot(d.wid, old, d.diskNodes)
		if err := store.WriteFileAtomic(p+".prev", old); err != nil {
			return err
		}
	}
	if err := store.WriteFileAtomic(p, data); err != nil {
		return err
	}
	d.diskNodes = nodes
	return nil
}
