package board

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"nextboard/internal/store"
)

const keepSnapshots = 24

// Archive reasons.
const (
	ReasonClosed     = "closed"
	ReasonCleared    = "cleared"
	ReasonUnreadable = "unreadable"
)

var (
	ErrSnapshotNotFound = errors.New("snapshot tidak ditemukan")
	ErrArchiveNotFound  = errors.New("arsip tidak ditemukan")
)

// Snapshot ids: YYYYMMDDHH for hourly snapshots, YYYYMMDDHHMMSS for the
// copy saved right before a restore.
var snapIDRe = regexp.MustCompile(`^[0-9]{10}([0-9]{4})?$`)

// SnapshotMeta is one entry of GET /api/workspaces/{id}/snapshots.
type SnapshotMeta struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	NodeCount int       `json:"nodeCount"`
}

type snapshotFile struct {
	SnapshotMeta
	Board json.RawMessage `json:"board"`
}

// ArchiveMeta is one entry of GET /api/archive.
type ArchiveMeta struct {
	ID            string    `json:"id"`
	ProjectName   string    `json:"projectName"`
	WorkspaceName string    `json:"workspaceName"`
	ArchivedAt    time.Time `json:"archivedAt"`
	NodeCount     int       `json:"nodeCount"`
	Reason        string    `json:"reason"`
}

type archiveFile struct {
	ArchiveMeta
	WorkspaceID string `json:"workspaceId"`
	Board       *Board `json:"board,omitempty"`
	// Raw keeps the bytes of an unreadable board file.
	Raw []byte `json:"raw,omitempty"`
}

func (m *Manager) snapDir(wid string) string { return m.st.Path("snapshots", wid) }

func (m *Manager) snapPath(wid, id string) string {
	return m.st.Path("snapshots", wid, id+".json")
}

// hourlySnapshot keeps the version being replaced by the first save of
// each hour, so every snapshot is the board as it was when that hour's
// editing began. Caller holds the board's saveMu.
func (m *Manager) hourlySnapshot(wid string, old []byte, nodes int) {
	id := m.now().UTC().Format("2006010215")
	if _, err := os.Stat(m.snapPath(wid, id)); err == nil {
		return
	}
	if err := m.writeSnapshot(wid, id, old, nodes); err != nil {
		log.Printf("snapshot board %s gagal: %v", wid, err)
	}
}

func (m *Manager) writeSnapshot(wid, id string, board []byte, nodes int) error {
	if err := os.MkdirAll(m.snapDir(wid), 0o700); err != nil {
		return err
	}
	f := snapshotFile{SnapshotMeta: SnapshotMeta{ID: id, At: m.now().UTC(), NodeCount: nodes}, Board: board}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := store.WriteFileAtomic(m.snapPath(wid, id), b); err != nil {
		return err
	}
	m.metaMu.Lock()
	m.snapMeta[wid+"/"+id] = f.SnapshotMeta
	m.metaMu.Unlock()
	m.pruneSnapshots(wid)
	return nil
}

func (m *Manager) snapshotIDs(wid string) []string {
	ents, err := os.ReadDir(m.snapDir(wid))
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && snapIDRe.MatchString(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids) // both id forms sort chronologically
	return ids
}

func (m *Manager) pruneSnapshots(wid string) {
	ids := m.snapshotIDs(wid)
	for len(ids) > keepSnapshots {
		_ = os.Remove(m.snapPath(wid, ids[0]))
		m.metaMu.Lock()
		delete(m.snapMeta, wid+"/"+ids[0])
		m.metaMu.Unlock()
		ids = ids[1:]
	}
}

// Snapshots lists a board's snapshots, newest first.
func (m *Manager) Snapshots(wid string) ([]SnapshotMeta, error) {
	if !IDRe.MatchString(wid) {
		return nil, ErrNotFound
	}
	out := []SnapshotMeta{}
	for _, id := range m.snapshotIDs(wid) {
		key := wid + "/" + id
		m.metaMu.Lock()
		meta, ok := m.snapMeta[key]
		m.metaMu.Unlock()
		if !ok {
			var f snapshotFile
			if _, err := m.st.Load("snapshots/"+wid+"/"+id+".json", &f); err != nil {
				continue
			}
			meta = f.SnapshotMeta
			meta.ID = id
			m.metaMu.Lock()
			m.snapMeta[key] = meta
			m.metaMu.Unlock()
		}
		out = append(out, meta)
	}
	slices.Reverse(out)
	return out, nil
}

// RestoreSnapshot replaces the board with a snapshot. The current version
// is saved as a snapshot first; terminal nodes of sessions that are gone
// are dropped, and live sessions missing from the snapshot keep their
// current node. Clients get the change as hub ops (clientId "").
func (m *Manager) RestoreSnapshot(wid, sid string) (Board, error) {
	if !snapIDRe.MatchString(sid) {
		return Board{}, ErrSnapshotNotFound
	}
	if !IDRe.MatchString(wid) {
		return Board{}, ErrNotFound
	}
	var f snapshotFile
	if found, err := m.st.Load("snapshots/"+wid+"/"+sid+".json", &f); !found || err != nil {
		return Board{}, ErrSnapshotNotFound
	}
	var snap Board
	if json.Unmarshal(f.Board, &snap) != nil {
		return Board{}, ErrSnapshotNotFound
	}
	d, err := m.doc(wid, true)
	if err != nil {
		return Board{}, err
	}
	d.saveMu.Lock()
	d.mu.Lock()
	if d.dropped || d.readOnly {
		d.mu.Unlock()
		d.saveMu.Unlock()
		if d.readOnly {
			return Board{}, ErrReadOnly
		}
		return Board{}, ErrNotFound
	}
	cur := d.boardLocked()
	if b, err := json.Marshal(cur); err == nil {
		// Two restores within a second must not share one safety copy.
		backup := m.now().UTC().Format("20060102150405")
		for t := m.now().UTC(); ; {
			if _, err := os.Stat(m.snapPath(wid, backup)); err != nil {
				break
			}
			t = t.Add(time.Second)
			backup = t.Format("20060102150405")
		}
		if err := m.writeSnapshot(wid, backup, b, len(cur.Nodes)); err != nil {
			log.Printf("snapshot sebelum restore gagal: %v", err)
		}
	}
	next := &doc{m: m, wid: wid}
	next.clean(snap.Nodes, snap.Edges)
	for _, n := range d.nodes {
		if n.Kind != KindTerminal || slices.ContainsFunc(next.nodes, func(x *Node) bool { return x.sid == n.sid }) {
			continue
		}
		if slices.ContainsFunc(next.nodes, func(x *Node) bool { return x.ID == n.ID }) {
			cp := *n
			cp.ID = randomID("n_")
			n = &cp
		}
		next.nodes = append(next.nodes, n)
	}
	var ops []Op
	for _, e := range d.edges {
		ops = append(ops, Op{Op: "delEdge", ID: e.ID})
	}
	for _, n := range d.nodes {
		ops = append(ops, Op{Op: "del", ID: n.ID})
	}
	for _, n := range next.nodes {
		ops = append(ops, Op{Op: "put", Node: n})
	}
	for _, e := range next.edges {
		ops = append(ops, Op{Op: "putEdge", Edge: e})
	}
	d.commitLocked(&work{nodes: next.nodes, edges: next.edges}, "", ops)
	out := d.boardLocked()
	d.mu.Unlock()
	d.saveMu.Unlock()
	d.save(false)
	return out, nil
}

// Archive stores the current board under archive/ (never pruned). Boards
// without any non-terminal node are not archived (nothing to restore).
func (m *Manager) Archive(wid, projectName, workspaceName, reason string) (string, error) {
	d, err := m.doc(wid, false)
	if err != nil || d == nil {
		return "", err
	}
	d.mu.Lock()
	b := d.boardLocked()
	d.mu.Unlock()
	if contentCount(b.Nodes) == 0 {
		return "", nil
	}
	return m.writeArchive(b, projectName, workspaceName, reason)
}

func (m *Manager) writeArchive(b Board, projectName, workspaceName, reason string) (string, error) {
	f := archiveFile{
		ArchiveMeta: ArchiveMeta{
			ID: randomID("r_"), ProjectName: projectName, WorkspaceName: workspaceName,
			ArchivedAt: m.now().UTC(), NodeCount: len(b.Nodes), Reason: reason,
		},
		WorkspaceID: b.WorkspaceID, Board: &b,
	}
	return f.ID, m.saveArchive(&f)
}

// archiveRaw keeps an unreadable board file.
func (m *Manager) archiveRaw(wid string, raw []byte) {
	pn, wn := m.names(wid)
	f := archiveFile{
		ArchiveMeta: ArchiveMeta{ID: randomID("r_"), ProjectName: pn, WorkspaceName: wn, ArchivedAt: m.now().UTC(), Reason: ReasonUnreadable},
		WorkspaceID: wid, Raw: raw,
	}
	if err := m.saveArchive(&f); err != nil {
		log.Printf("arsip board %s gagal: %v", wid, err)
	}
}

func (m *Manager) saveArchive(f *archiveFile) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := store.WriteFileAtomic(m.st.Path("archive", f.ID+".json"), b); err != nil {
		return err
	}
	m.metaMu.Lock()
	m.archMeta[f.ID] = f.ArchiveMeta
	m.metaMu.Unlock()
	return nil
}

// ListArchive returns archived boards, newest first.
func (m *Manager) ListArchive() []ArchiveMeta {
	out := []ArchiveMeta{}
	ents, _ := os.ReadDir(m.st.Path("archive"))
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !IDRe.MatchString(id) {
			continue
		}
		m.metaMu.Lock()
		meta, ok := m.archMeta[id]
		m.metaMu.Unlock()
		if !ok {
			f, err := m.loadArchive(id)
			if err != nil {
				continue
			}
			meta = f.ArchiveMeta
			m.metaMu.Lock()
			m.archMeta[id] = meta
			m.metaMu.Unlock()
		}
		out = append(out, meta)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ArchivedAt.After(out[j].ArchivedAt) })
	return out
}

func (m *Manager) loadArchive(id string) (*archiveFile, error) {
	if !IDRe.MatchString(id) {
		return nil, ErrArchiveNotFound
	}
	var f archiveFile
	found, err := m.st.Load("archive/"+id+".json", &f)
	if err != nil || !found {
		return nil, ErrArchiveNotFound
	}
	f.ID = id
	return &f, nil
}

// ArchiveMetaOf returns one archive entry.
func (m *Manager) ArchiveMetaOf(id string) (ArchiveMeta, error) {
	f, err := m.loadArchive(id)
	if err != nil {
		return ArchiveMeta{}, err
	}
	return f.ArchiveMeta, nil
}

// RestoreArchive writes an archived board into a new (empty) workspace,
// without terminal nodes. The archive entry is kept.
func (m *Manager) RestoreArchive(id, newWid string) (Board, error) {
	f, err := m.loadArchive(id)
	if err != nil {
		return Board{}, err
	}
	if !IDRe.MatchString(newWid) {
		return Board{}, ErrNotFound
	}
	nd := &doc{m: m, wid: newWid}
	if f.Board != nil {
		var nodes []*Node
		for _, n := range f.Board.Nodes {
			if n != nil && n.Kind != KindTerminal {
				nodes = append(nodes, n)
			}
		}
		nd.clean(nodes, f.Board.Edges)
	}
	d, err := m.doc(newWid, true)
	if err != nil {
		return Board{}, err
	}
	d.mu.Lock()
	if d.dropped || len(d.nodes) > 0 || len(d.edges) > 0 {
		d.mu.Unlock()
		return Board{}, errors.New("workspace tujuan sudah punya isi")
	}
	ops := make([]Op, 0, len(nd.nodes)+len(nd.edges))
	for _, n := range nd.nodes {
		ops = append(ops, Op{Op: "put", Node: n})
	}
	for _, e := range nd.edges {
		ops = append(ops, Op{Op: "putEdge", Edge: e})
	}
	if len(ops) > 0 {
		d.commitLocked(&work{nodes: nd.nodes, edges: nd.edges}, "", ops)
	}
	b := d.boardLocked()
	d.mu.Unlock()
	d.save(false)
	return b, nil
}
