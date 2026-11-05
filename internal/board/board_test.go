package board

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/store"
)

const wid = "w_testboard1"

type sent struct {
	rev      int64
	clientID string
	ops      []Op
}

type fixture struct {
	t     *testing.T
	st    *store.Store
	m     *Manager
	mu    sync.Mutex
	live  map[string]string // session → workspace
	sent  []sent
	clock time.Time
}

func newFixture(t *testing.T, dir string) *fixture {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, st: st, live: map[string]string{}, clock: time.Date(2026, 10, 28, 14, 5, 0, 0, time.UTC)}
	f.m = f.manager()
	return f
}

func (f *fixture) manager() *Manager {
	m := NewManager(f.st)
	m.SaveDelay, m.MaxDelay = time.Hour, time.Hour // tests flush explicitly
	m.now = func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.clock
	}
	m.SessionWorkspace = func(sid string) (string, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w, ok := f.live[sid]
		return w, ok
	}
	m.Names = func(string) (string, string) { return "proj", "Main" }
	m.OnOps = func(_ string, rev int64, clientID string, ops []Op) {
		f.mu.Lock()
		f.sent = append(f.sent, sent{rev, clientID, ops})
		f.mu.Unlock()
	}
	f.t.Cleanup(m.Close)
	return m
}

func (f *fixture) apply(ops ...Op) (int64, error) { return f.m.Apply(wid, "c_test", ops) }

func (f *fixture) mustApply(ops ...Op) int64 {
	f.t.Helper()
	rev, err := f.apply(ops...)
	if err != nil {
		f.t.Fatalf("apply: %v", err)
	}
	return rev
}

func (f *fixture) board() Board {
	f.t.Helper()
	b, err := f.m.Get(wid)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *fixture) lastSent() sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[len(f.sent)-1]
}

func note(id string, x float64, data string) Op {
	return Op{Op: "put", Node: &Node{ID: id, Kind: "note", X: x, Y: 0, W: 200, H: 100, Data: json.RawMessage(data)}}
}

func ids(b Board) string {
	var s []string
	for _, n := range b.Nodes {
		s = append(s, n.ID)
	}
	return strings.Join(s, ",")
}

func dataMap(t *testing.T, n *Node) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(n.Data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOpsApplyAndValidate(t *testing.T) {
	f := newFixture(t, t.TempDir())
	if rev := f.mustApply(note("n_aaaaaa1", 0, `{"text":"a","color":"yellow"}`), note("n_bbbbbb2", 10, `{}`), note("n_cccccc3", 20, ``)); rev != 1 {
		t.Fatalf("rev %d", rev)
	}
	if got := ids(f.board()); got != "n_aaaaaa1,n_bbbbbb2,n_cccccc3" {
		t.Fatalf("order %s", got)
	}
	if s := f.lastSent(); s.rev != 1 || s.clientID != "c_test" || len(s.ops) != 3 {
		t.Fatalf("broadcast %+v", s)
	}

	// put of an existing node replaces it in place (z kept); patch merges data.
	f.mustApply(note("n_aaaaaa1", 5, `{"text":"A"}`),
		Op{Op: "patch", ID: "n_aaaaaa1", Set: map[string]json.RawMessage{"x": json.RawMessage("42"), "locked": json.RawMessage("true"), "data": json.RawMessage(`{"color":"pink","text":null}`)}})
	b := f.board()
	a := b.Nodes[0]
	if a.ID != "n_aaaaaa1" || a.X != 42 || !a.Locked {
		t.Fatalf("patched %+v", a)
	}
	if d := dataMap(t, a); d["color"] != "pink" || d["text"] != nil || len(d) != 1 {
		t.Fatalf("shallow merge %v", d)
	}

	// z order + edges; deleting a node removes its edges.
	rev := f.mustApply(Op{Op: "z", ID: "n_aaaaaa1", To: "front"}, Op{Op: "z", ID: "n_cccccc3", To: "back"},
		Op{Op: "putEdge", Edge: &Edge{ID: "e_edge0001", Source: "n_aaaaaa1", Target: "n_bbbbbb2"}},
		Op{Op: "putEdge", Edge: &Edge{ID: "e_edge0002", Source: "n_cccccc3", Target: "n_bbbbbb2", Kind: "arrow"}})
	if rev != 3 {
		t.Fatalf("rev %d", rev)
	}
	b = f.board()
	if ids(b) != "n_cccccc3,n_bbbbbb2,n_aaaaaa1" || len(b.Edges) != 2 || b.Edges[0].Kind != "wire" {
		t.Fatalf("z/edges %s %+v", ids(b), b.Edges)
	}
	f.mustApply(Op{Op: "del", ID: "n_aaaaaa1"})
	b = f.board()
	if ids(b) != "n_cccccc3,n_bbbbbb2" || len(b.Edges) != 1 || b.Edges[0].ID != "e_edge0002" {
		t.Fatalf("after del %s %+v", ids(b), b.Edges)
	}
	if s := f.lastSent(); len(s.ops) != 2 || s.ops[1].Op != "delEdge" || s.ops[1].ID != "e_edge0001" {
		t.Fatalf("del broadcast %+v", s.ops)
	}

	// Ops on vanished nodes are no-ops: no rev bump, no broadcast.
	before := len(f.sent)
	if rev := f.mustApply(Op{Op: "patch", ID: "n_gone0000", Set: map[string]json.RawMessage{"x": json.RawMessage("1")}}, Op{Op: "del", ID: "n_gone0000"}); rev != 4 || len(f.sent) != before {
		t.Fatalf("noop rev %d", rev)
	}

	// Validation errors reject the whole batch atomically.
	bad := []struct {
		name string
		op   Op
	}{
		{"bad id", note("N_UPPER", 0, `{}`)},
		{"short id", note("n_ab", 0, `{}`)},
		{"w zero", Op{Op: "put", Node: &Node{ID: "n_dddddd4", Kind: "note", W: 0, H: 10}}},
		{"too far", Op{Op: "put", Node: &Node{ID: "n_dddddd4", Kind: "note", X: 20001, W: 10, H: 10}}},
		{"data array", note("n_dddddd4", 0, `[1]`)},
		{"data big", note("n_dddddd4", 0, `{"t":"`+strings.Repeat("x", MaxDataBytes)+`"}`)},
		{"bad kind", Op{Op: "put", Node: &Node{ID: "n_dddddd4", Kind: "Bad Kind", W: 10, H: 10}}},
		{"edge to missing", Op{Op: "putEdge", Edge: &Edge{ID: "e_edge0003", Source: "n_bbbbbb2", Target: "n_missing0"}}},
		{"patch unknown field", Op{Op: "patch", ID: "n_bbbbbb2", Set: map[string]json.RawMessage{"kind": json.RawMessage(`"x"`)}}},
		{"patch nan-ish", Op{Op: "patch", ID: "n_bbbbbb2", Set: map[string]json.RawMessage{"w": json.RawMessage(`"wide"`)}}},
		{"z bad", Op{Op: "z", ID: "n_bbbbbb2", To: "top"}},
		{"unknown op", Op{Op: "move", ID: "n_bbbbbb2"}},
		{"terminal for missing session", Op{Op: "put", Node: &Node{ID: "n_term0001", Kind: KindTerminal, W: 10, H: 10, Data: json.RawMessage(`{"sessionId":"s_nope00000"}`)}}},
	}
	for _, c := range bad {
		_, err := f.apply(note("n_eeeeee5", 0, `{}`), c.op)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s: expected validation error, got %v", c.name, err)
		}
	}
	if strings.Contains(ids(f.board()), "n_eeeeee5") {
		t.Fatal("batch was not atomic")
	}
	if _, err := f.apply(make([]Op, MaxOps+1)...); err == nil {
		t.Fatal("too many ops accepted")
	}
	// Board total limit (8 MB).
	var big []Op
	for i := 0; i < 40; i++ {
		big = append(big, note(randomID("n_"), 0, `{"t":"`+strings.Repeat("x", 250<<10)+`"}`))
	}
	if _, err := f.apply(big...); err == nil || !strings.Contains(err.Error(), "terlalu besar") {
		t.Fatalf("board size limit: %v", err)
	}
}

func TestTerminalNodeRules(t *testing.T) {
	f := newFixture(t, t.TempDir())
	f.live["s_session01"] = wid
	f.live["s_otherws01"] = "w_otherws0001"
	n, err := f.m.AddTerminal(wid, "s_session01", &Rect{X: 100, Y: 50, W: 600, H: 400})
	if err != nil || n.X != 100 || n.SessionID() != "s_session01" {
		t.Fatalf("add %+v %v", n, err)
	}
	if s := f.lastSent(); s.clientID != "" || s.ops[0].Op != "put" {
		t.Fatalf("hub op %+v", s)
	}
	// Idempotent per session.
	if again, _ := f.m.AddTerminal(wid, "s_session01", nil); again.ID != n.ID || len(f.board().Nodes) != 1 {
		t.Fatal("second AddTerminal duplicated the node")
	}
	// Auto placement: right of the last terminal.
	f.live["s_session02"] = wid
	n2, _ := f.m.AddTerminal(wid, "s_session02", nil)
	if n2.X != 100+600+placeGap || n2.Y != 50 || n2.W != TermW {
		t.Fatalf("auto place %+v", n2)
	}
	// Client del of a terminal node is ignored; moving it is fine.
	rev := f.board().Rev
	if got := f.mustApply(Op{Op: "del", ID: n.ID}); got != rev {
		t.Fatal("terminal del changed the board")
	}
	f.mustApply(Op{Op: "patch", ID: n.ID, Set: map[string]json.RawMessage{"x": json.RawMessage("7"), "data": json.RawMessage(`{"collapsed":true}`)}})
	for _, op := range []Op{
		{Op: "patch", ID: n.ID, Set: map[string]json.RawMessage{"data": json.RawMessage(`{"sessionId":"s_session02"}`)}},
		note(n.ID, 0, `{}`), // replace terminal with another kind
		{Op: "put", Node: &Node{ID: "n_dupterm01", Kind: KindTerminal, W: 10, H: 10, Data: json.RawMessage(`{"sessionId":"s_session01"}`)}},
		{Op: "put", Node: &Node{ID: "n_otherws01", Kind: KindTerminal, W: 10, H: 10, Data: json.RawMessage(`{"sessionId":"s_otherws01"}`)}},
	} {
		if _, err := f.apply(op); err == nil {
			t.Fatalf("op accepted: %+v", op)
		}
	}
	// RemoveTerminal deletes node + edges.
	f.mustApply(Op{Op: "putEdge", Edge: &Edge{ID: "e_termedge1", Source: n.ID, Target: n2.ID}})
	if err := f.m.RemoveTerminal(wid, "s_session01"); err != nil {
		t.Fatal(err)
	}
	b := f.board()
	if len(b.Nodes) != 1 || len(b.Edges) != 0 || f.lastSent().clientID != "" {
		t.Fatalf("after remove %+v", b)
	}
}

func TestPersistenceAndReload(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, dir)
	f.live["s_session01"] = wid
	f.m.AddTerminal(wid, "s_session01", nil)
	f.mustApply(note("n_aaaaaa1", 0, `{"text":"keep"}`))
	f.m.Flush()
	if _, err := os.Stat(f.st.Path("boards", wid+".json")); err != nil {
		t.Fatal(err)
	}
	f.mustApply(note("n_bbbbbb2", 0, `{}`))
	f.m.Flush()
	if _, err := os.Stat(f.st.Path("boards", wid+".json.prev")); err != nil {
		t.Fatal(".prev missing")
	}

	// A fresh manager reads the same board; the session is gone now, so its
	// terminal node is dropped on load.
	delete(f.live, "s_session01")
	m2 := f.manager()
	b, err := m2.Get(wid)
	if err != nil || b.Rev != 3 || ids(b) != "n_aaaaaa1,n_bbbbbb2" || b.SchemaVersion != 2 {
		t.Fatalf("reload rev=%d %s %v", b.Rev, ids(b), err)
	}

	// Debounced save: within MaxDelay even while changes keep coming.
	m2.Flush() // load dropped the stale terminal node → dirty with the fake clock
	m2.SaveDelay, m2.MaxDelay, m2.now = 40*time.Millisecond, 150*time.Millisecond, time.Now
	path := f.st.Path("boards", wid+".json")
	start := time.Now()
	for time.Since(start) < 400*time.Millisecond {
		if _, err := m2.Apply(wid, "", []Op{note("n_cccccc3", float64(time.Since(start).Milliseconds()), `{}`)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
		if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "n_cccccc3") {
			if time.Since(start) > 300*time.Millisecond {
				t.Fatalf("save waited too long")
			}
			return
		}
	}
	t.Fatal("debounced save never happened")
}

func TestUnreadableBoardIsArchived(t *testing.T) {
	f := newFixture(t, t.TempDir())
	f.mustApply(note("n_aaaaaa1", 0, `{}`))
	f.m.Flush()
	f.mustApply(note("n_bbbbbb2", 0, `{}`))
	f.m.Flush()
	if err := os.WriteFile(f.st.Path("boards", wid+".json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	m2 := f.manager()
	b, _ := m2.Get(wid)
	if ids(b) != "n_aaaaaa1" { // fell back to .prev
		t.Fatalf("fallback %s", ids(b))
	}
	list := m2.ListArchive()
	if len(list) != 1 || list[0].Reason != ReasonUnreadable {
		t.Fatalf("archive %+v", list)
	}
}

func TestSnapshotsAndRestore(t *testing.T) {
	f := newFixture(t, t.TempDir())
	f.live["s_session01"] = wid
	f.mustApply(note("n_aaaaaa1", 0, `{"v":1}`))
	f.m.Flush() // first save: nothing older to snapshot
	if l, _ := f.m.Snapshots(wid); len(l) != 0 {
		t.Fatalf("unexpected snapshot %+v", l)
	}
	f.mustApply(note("n_bbbbbb2", 0, `{}`))
	f.m.Flush() // first save of 14h: snapshot = board before it (1 node)
	f.mustApply(note("n_cccccc3", 0, `{}`))
	f.m.Flush() // same hour: no new snapshot
	f.clock = f.clock.Add(time.Hour)
	f.m.AddTerminal(wid, "s_session01", nil)
	f.m.Flush() // 15h snapshot: 3 nodes

	l, _ := f.m.Snapshots(wid)
	if len(l) != 2 || l[0].ID != "2026102815" || l[0].NodeCount != 3 || l[1].ID != "2026102814" || l[1].NodeCount != 1 {
		t.Fatalf("snapshots %+v", l)
	}
	b, err := f.m.RestoreSnapshot(wid, "2026102814")
	if err != nil {
		t.Fatal(err)
	}
	// The live session's terminal node survives the restore.
	if len(b.Nodes) != 2 || b.Nodes[0].ID != "n_aaaaaa1" || b.Nodes[1].Kind != KindTerminal {
		t.Fatalf("restored %s", ids(b))
	}
	if s := f.lastSent(); s.clientID != "" || s.rev != b.Rev {
		t.Fatalf("restore broadcast %+v", s)
	}
	l, _ = f.m.Snapshots(wid)
	if len(l) != 3 || len(l[0].ID) != 14 || l[0].NodeCount != 4 {
		t.Fatalf("pre-restore snapshot %+v", l)
	}
	if _, err := f.m.RestoreSnapshot(wid, "../../x"); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("bad sid %v", err)
	}
	// Keep the newest 24.
	for i := 0; i < 30; i++ {
		f.clock = f.clock.Add(time.Hour)
		f.mustApply(note("n_dddddd4", float64(i), `{}`))
		f.m.Flush()
	}
	if l, _ := f.m.Snapshots(wid); len(l) != keepSnapshots {
		t.Fatalf("kept %d snapshots", len(l))
	}
}

func TestArchiveAndRestore(t *testing.T) {
	f := newFixture(t, t.TempDir())
	f.live["s_session01"] = wid
	f.m.AddTerminal(wid, "s_session01", nil)
	term := f.board().Nodes[0].ID
	f.mustApply(note("n_aaaaaa1", 0, `{}`), note("n_bbbbbb2", 0, `{}`), note("n_cccccc3", 0, `{}`),
		Op{Op: "putEdge", Edge: &Edge{ID: "e_edge0001", Source: "n_aaaaaa1", Target: "n_bbbbbb2"}},
		Op{Op: "putEdge", Edge: &Edge{ID: "e_edge0002", Source: term, Target: "n_bbbbbb2"}})

	// Clearing all content in one batch archives first ("cleared").
	f.mustApply(Op{Op: "del", ID: "n_aaaaaa1"}, Op{Op: "del", ID: "n_bbbbbb2"}, Op{Op: "del", ID: "n_cccccc3"})
	list := f.m.ListArchive()
	if len(list) != 1 || list[0].Reason != ReasonCleared || list[0].NodeCount != 4 {
		t.Fatalf("cleared %+v", list)
	}
	f.mustApply(note("n_eeeeee5", 0, `{}`))
	id, err := f.m.Archive(wid, "proj", "Main", ReasonClosed)
	if err != nil || id == "" {
		t.Fatal(err)
	}
	f.m.Drop(wid)
	if _, err := f.m.Get(wid); !errors.Is(err, ErrNotFound) {
		t.Fatal("dropped board still served")
	}
	if _, err := os.Stat(f.st.Path("boards", wid+".json")); err == nil {
		t.Fatal("board file not removed")
	}

	// Restore the "cleared" archive into a new workspace: no terminal nodes,
	// edges to them dropped.
	b, err := f.m.RestoreArchive(list[0].ID, "w_restored01")
	if err != nil {
		t.Fatal(err)
	}
	if ids(b) != "n_aaaaaa1,n_bbbbbb2,n_cccccc3" || len(b.Edges) != 1 || b.Edges[0].ID != "e_edge0001" {
		t.Fatalf("restored %s %+v", ids(b), b.Edges)
	}
	if len(f.m.ListArchive()) != 2 {
		t.Fatal("archive must never be pruned")
	}
	if _, err := f.m.RestoreArchive("r_../../etc", "w_restored02"); !errors.Is(err, ErrArchiveNotFound) {
		t.Fatalf("bad archive id %v", err)
	}
}

// TestRestoreKeepsEverySafetyCopy: restores within one second do not share a
// safety copy, so each state stays reachable.
func TestRestoreKeepsEverySafetyCopy(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st)
	t.Cleanup(m.Close)
	frozen := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return frozen }
	const wid = "w_restoretst"
	put := func(id string) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"text": id})
		if _, err := m.Apply(wid, "", []Op{{Op: "put", Node: &Node{ID: id, Kind: "note", W: 100, H: 100, Data: data}}}); err != nil {
			t.Fatal(err)
		}
	}
	put("n_aaaaaaaaaa")
	if err := m.writeSnapshot(wid, "2026110300", []byte(`{"workspaceId":"`+wid+`","nodes":[],"edges":[]}`), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RestoreSnapshot(wid, "2026110300"); err != nil { // backs up the note
		t.Fatal(err)
	}
	if _, err := m.RestoreSnapshot(wid, "2026110300"); err != nil { // backs up the empty board
		t.Fatal(err)
	}
	list, err := m.Snapshots(wid)
	if err != nil {
		t.Fatal(err)
	}
	copies, withNote := 0, 0
	for _, x := range list {
		if len(x.ID) == 14 {
			copies++
			if x.NodeCount == 1 {
				withNote++
			}
		}
	}
	if copies != 2 {
		t.Fatalf("safety copies %+v", list)
	}
	if withNote != 1 {
		t.Fatalf("the copy holding the note is gone: %+v", list)
	}
}
