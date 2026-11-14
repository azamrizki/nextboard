// Package review keeps what agents change, so that it can be looked at, taken or undone
// (docs/API-studio.md §7.10), and a local history of every file (§7.11). A shadow repository in the
// hub's data folder holds snapshots of each work tree. It does not touch the project's own
// repository, works for every CLI and for folders without git, and sees edits that no hook reports,
// such as a sed from a shell tool. A project on a server takes part through a copy on This PC
// (mirror.go).
package review

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits (docs/API-studio.md §7.10).
const (
	// MaxFile: bigger files are no part of a snapshot.
	MaxFile = 10 << 20
	// MaxPaths bounds the changes taken in one snapshot.
	MaxPaths      = 20000
	tickEvery     = 3 * time.Second
	soonAfter     = 400 * time.Millisecond
	snapshotLimit = 2 * time.Minute
	readLimit     = 15 * time.Second
	// HistoryCommits and HistoryAge bound the local history of one root.
	HistoryCommits = 2000
	HistoryAge     = 30 * 24 * time.Hour
	pruneEvery     = 24 * time.Hour
	historyRef     = "refs/nb/history"
	keepRefs       = "refs/nb/keep/"
	emptyBlob      = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
	zeroHash       = "0000000000000000000000000000000000000000"
)

// Folders no snapshot looks into, on top of what the work tree's .gitignore says.
var defaultExcludes = []string{"node_modules/", "bower_components/", ".venv/", "venv/", "__pycache__/", ".next/", ".cache/", ".gradle/", ".terraform/"}

// Error is a failure with an HTTP status and a stable code.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

var (
	errUnsupported = &Error{http.StatusConflict, "review_unsupported", "this project takes no part in review"}
	errNoChange    = &Error{http.StatusNotFound, "no_change", "this agent has no pending change there"}
	errNoVersion   = &Error{http.StatusNotFound, "no_version", "that version is not in the local history"}
	errBadPath     = &Error{http.StatusBadRequest, "bad_path", "the path is not inside the project"}
)

// Root is one work tree of a project: its own folder or a repository inside it.
type Root struct {
	Rel  string // relative to the project folder; "" = the folder itself
	Path string // absolute, on This PC
	// For a project on a server: Path is in this copy, and Origin is the folder on the server,
	// the name hooks and the editor know a file by.
	Mirror *Mirror
	Origin string
}

// known is the name other parts of the hub use for a file of this root.
func (r Root) known(inRoot string) string {
	if r.Mirror != nil {
		return path.Join(r.Origin, inRoot)
	}
	return filepath.Join(r.Path, inRoot)
}

// Config wires the manager to the hub.
type Config struct {
	Dir string
	// Roots lists the work trees of a project; nothing when it takes no part.
	Roots   func(projectID string) []Root
	Publish func(v any)
	Now     func() time.Time
}

// Change is one file an agent changed, against the state it found.
type Change struct {
	Path string `json:"path"` // relative to the project folder
	Add  int    `json:"add"`
	Del  int    `json:"del"`
	// Status is "added", "modified" or "deleted".
	Status string `json:"status"`
	Binary bool   `json:"binary,omitempty"`
	// Unsure: several agents were at work and no hook named this file.
	Unsure bool `json:"unsure,omitempty"`
	// Also names the other agents with a pending change to this file.
	Also  []string  `json:"also,omitempty"`
	Since time.Time `json:"since"`

	Root     string `json:"root"`
	Base     string `json:"base"` // blob; "" = the file did not exist
	BaseMode string `json:"baseMode"`
}

// Agent is one console and what it changed.
type Agent struct {
	SessionID string             `json:"sessionId"`
	Name      string             `json:"name"`
	Kind      string             `json:"kind"`
	Files     map[string]*Change `json:"files"`
}

// Summary is GET …/review.
type Summary struct {
	Supported bool `json:"supported"`
	// Problem says why the last look at the server failed: what is listed may be behind.
	Problem string       `json:"problem,omitempty"`
	Agents  []AgentFiles `json:"agents"`
}

// AgentFiles is an Agent as the API shows it.
type AgentFiles struct {
	SessionID string   `json:"sessionId"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Working   bool     `json:"working"`
	Files     []Change `json:"files"`
}

// FileView is GET …/review/file.
type FileView struct {
	Path          string `json:"path"`
	Base          string `json:"base"`
	BaseExists    bool   `json:"baseExists"`
	Current       string `json:"current"`
	CurrentExists bool   `json:"currentExists"`
	Binary        bool   `json:"binary"`
	TooBig        bool   `json:"tooBig"`
}

// Version is one state of a file in the local history.
type Version struct {
	Commit string    `json:"commit"`
	At     time.Time `json:"at"`
	Who    string    `json:"who"`
	Exists bool      `json:"exists"`
}

// Manager owns the review state of all projects.
type Manager struct {
	cfg Config

	mu       sync.Mutex
	projects map[string]*project
	closed   bool
	wg       sync.WaitGroup
}

// New returns a manager; state on disk is read when a project is first used.
func New(cfg Config) *Manager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Manager{cfg: cfg, projects: map[string]*project{}}
}

// Close waits for running snapshots.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	all := make([]*project, 0, len(m.projects))
	for _, p := range m.projects {
		all = append(all, p)
	}
	m.mu.Unlock()
	for _, p := range all {
		p.stop()
	}
	m.wg.Wait()
}

type project struct {
	m  *Manager
	id string

	// snap allows one snapshot at a time.
	snap sync.Mutex

	mu      sync.Mutex
	state   stateFile
	want    map[string]bool // consoles at work, as last told
	active  map[string]*activeAgent
	hints   map[string]map[string]bool // absolute path → sessions a hook named it for
	ignored map[string]bool            // absolute paths the user or the review just wrote
	overlap map[string]bool
	ticking bool
	problem string // why the last look at a server failed
	soon    *time.Timer
	stopped chan struct{}
	pruned  map[string]time.Time
	// quiet counts the snapshots in a row that found nothing new.
	quiet int
}

// After quietAfter snapshots in a row with nothing new (an agent thinking, or waiting for its
// owner, which counts as at work), the tick takes one only every quietEvery ticks: each is four
// git processes, or a listing of the whole tree on a server. A hook naming a file ends the wait.
const (
	quietAfter = 5
	quietEvery = 5
)

type activeAgent struct {
	name, kind string
	// hooks: this CLI reports the files it edits, so what no hook named is rarely its own.
	hooks bool
}

type stateFile struct {
	Agents map[string]*Agent `json:"agents"`
}

func (m *Manager) project(id string) *project {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.projects[id]; ok {
		return p
	}
	p := &project{m: m, id: id, want: map[string]bool{}, active: map[string]*activeAgent{}, hints: map[string]map[string]bool{}, ignored: map[string]bool{}, overlap: map[string]bool{}, pruned: map[string]time.Time{}, stopped: make(chan struct{})}
	p.state.Agents = map[string]*Agent{}
	if b, err := os.ReadFile(p.statePath()); err == nil {
		_ = json.Unmarshal(b, &p.state)
		if p.state.Agents == nil {
			p.state.Agents = map[string]*Agent{}
		}
	}
	m.projects[id] = p
	return p
}

func (p *project) dir() string       { return filepath.Join(p.m.cfg.Dir, p.id) }
func (p *project) statePath() string { return filepath.Join(p.dir(), "state.json") }

func (p *project) stop() {
	select {
	case <-p.stopped:
	default:
		close(p.stopped)
	}
}

// saveLocked writes the pending changes; they outlive a hub restart.
func (p *project) saveLocked() {
	b, err := json.Marshal(p.state)
	if err != nil {
		return
	}
	if os.MkdirAll(p.dir(), 0o700) != nil {
		return
	}
	tmp := p.statePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p.statePath())
	}
}

func (p *project) publish() {
	if p.m.cfg.Publish != nil {
		p.m.cfg.Publish(map[string]any{"type": "review", "projectId": p.id})
	}
}

// ---- the shadow repository ------------------------------------------------------------

type shadow struct {
	gitDir string
	root   string
}

func (p *project) shadowOf(root string) shadow {
	sum := sha256.Sum256([]byte(root))
	return shadow{gitDir: filepath.Join(p.dir(), hex.EncodeToString(sum[:6])+".git"), root: root}
}

func (sh shadow) git(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.autocrlf=false", "-c", "core.safecrlf=false",
		"-c", "core.quotepath=off", "-c", "gc.auto=0", "-c", "advice.addEmbeddedRepo=false", "-c", "core.bigFileThreshold=4m"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir = sh.root
	env := make([]string, 0, len(os.Environ())+8)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "GIT_DIR="+sh.gitDir, "GIT_WORK_TREE="+sh.root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C",
		"GIT_AUTHOR_NAME=NextBoard", "GIT_AUTHOR_EMAIL=nextboard@localhost", "GIT_COMMITTER_NAME=NextBoard", "GIT_COMMITTER_EMAIL=nextboard@localhost")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func (sh shadow) ready(ctx context.Context, nested []string) error {
	if _, err := os.Stat(filepath.Join(sh.gitDir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(sh.gitDir), 0o700); err != nil {
			return err
		}
		if _, err := sh.git(ctx, nil, "init", "-q"); err != nil {
			return err
		}
	}
	excl := append([]string{}, defaultExcludes...)
	for _, n := range nested {
		excl = append(excl, "/"+n+"/")
	}
	info := filepath.Join(sh.gitDir, "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(info, "exclude"), []byte(strings.Join(excl, "\n")+"\n"), 0o600)
}

// rawChange is one line of diff-tree --raw.
type rawChange struct {
	path             string
	oldMode, newMode string
	oldBlob, newBlob string
}

// take stores the work tree as it is now. It returns the commit, what changed since the last
// snapshot, and false when this is the first one.
func (sh shadow) take(ctx context.Context, who string, nested []string) (string, []rawChange, bool, error) {
	if err := sh.ready(ctx, nested); err != nil {
		return "", nil, false, err
	}
	out, err := sh.git(ctx, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=all")
	if err != nil {
		return "", nil, false, err
	}
	var paths bytes.Buffer
	count := 0
	for _, e := range strings.Split(string(out), "\x00") {
		if len(e) < 4 || count >= MaxPaths {
			continue
		}
		rel := e[3:]
		if strings.HasSuffix(rel, "/") {
			continue // a repository inside this one
		}
		if st, err := os.Lstat(filepath.Join(sh.root, rel)); err == nil && st.Size() > MaxFile {
			continue
		}
		paths.WriteString(rel)
		paths.WriteByte(0)
		count++
	}
	if count > 0 {
		// a file that vanishes meanwhile must not cost the rest
		_, _ = sh.git(ctx, paths.Bytes(), "add", "-A", "--ignore-errors", "--pathspec-from-file=-", "--pathspec-file-nul")
	}
	t, err := sh.git(ctx, nil, "write-tree")
	if err != nil {
		return "", nil, false, err
	}
	tree := strings.TrimSpace(string(t))
	prev, _ := sh.git(ctx, nil, "rev-parse", "-q", "--verify", historyRef)
	last := strings.TrimSpace(string(prev))
	if last == "" {
		c, err := sh.git(ctx, nil, "commit-tree", tree, "-m", "Earlier")
		if err != nil {
			return "", nil, false, err
		}
		commit := strings.TrimSpace(string(c))
		_, err = sh.git(ctx, nil, "update-ref", historyRef, commit)
		return commit, nil, false, err
	}
	oldTree, err := sh.git(ctx, nil, "rev-parse", last+"^{tree}")
	if err != nil {
		return "", nil, false, err
	}
	if strings.TrimSpace(string(oldTree)) == tree {
		return last, nil, true, nil
	}
	raw, err := sh.git(ctx, nil, "diff-tree", "-r", "--raw", "--no-renames", "--no-abbrev", "-z", last, tree)
	if err != nil {
		return "", nil, false, err
	}
	changes := parseRaw(string(raw))
	c, err := sh.git(ctx, nil, "commit-tree", tree, "-p", last, "-m", who)
	if err != nil {
		return "", nil, false, err
	}
	commit := strings.TrimSpace(string(c))
	if _, err := sh.git(ctx, nil, "update-ref", historyRef, commit); err != nil {
		return "", nil, false, err
	}
	return commit, changes, true, nil
}

func parseRaw(out string) []rawChange {
	var list []rawChange
	t := strings.Split(out, "\x00")
	for i := 0; i+1 < len(t); i += 2 {
		f := strings.Fields(strings.TrimPrefix(t[i], ":"))
		if len(f) < 5 {
			continue
		}
		// a repository inside this one has no content to review
		if f[0] == "160000" || f[1] == "160000" {
			continue
		}
		list = append(list, rawChange{path: t[i+1], oldMode: f[0], newMode: f[1], oldBlob: f[2], newBlob: f[3]})
	}
	return list
}

func (sh shadow) numstat(ctx context.Context, oldBlob, newBlob string) (add, del int, binary bool) {
	a, b := oldBlob, newBlob
	if a == "" || a == zeroHash {
		a = emptyBlob
	}
	if b == "" || b == zeroHash {
		b = emptyBlob
	}
	// the empty blob is not stored until something asks for it
	_, _ = sh.git(ctx, []byte{}, "hash-object", "-w", "--stdin")
	out, err := sh.git(ctx, nil, "diff", "--numstat", a, b)
	if err != nil {
		return 0, 0, false
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return 0, 0, false
	}
	if f[0] == "-" {
		return 0, 0, true
	}
	add, _ = strconv.Atoi(f[0])
	del, _ = strconv.Atoi(f[1])
	return add, del, false
}

func (sh shadow) keep(ctx context.Context, blob string) {
	if blob != "" {
		_, _ = sh.git(ctx, nil, "update-ref", keepRefs+blob, blob)
	}
}

func (sh shadow) unkeep(ctx context.Context, blob string) {
	if blob != "" {
		_, _ = sh.git(ctx, nil, "update-ref", "-d", keepRefs+blob)
	}
}

// prune drops what is older than the history keeps. Blobs of pending changes have refs of
// their own, so they stay.
func (sh shadow) prune(ctx context.Context, now time.Time) {
	cut := ""
	if out, err := sh.git(ctx, nil, "rev-list", "-n", "1", "--skip="+strconv.Itoa(HistoryCommits-1), historyRef); err == nil {
		cut = strings.TrimSpace(string(out))
	}
	if out, err := sh.git(ctx, nil, "rev-list", "--since="+strconv.FormatInt(now.Add(-HistoryAge).Unix(), 10), "--reverse", historyRef); err == nil {
		if f := strings.Fields(string(out)); len(f) > 0 {
			count := len(f)
			if cut == "" || count < HistoryCommits {
				if all, err := sh.git(ctx, nil, "rev-list", "--count", historyRef); err == nil && strings.TrimSpace(string(all)) != strconv.Itoa(count) {
					cut = f[0]
				}
			}
		}
	}
	// One thread and a bounded window: the hub shares the machine with what it runs.
	gc := []string{"-c", "pack.threads=1", "-c", "pack.windowMemory=64m", "gc", "--quiet"}
	if cut == "" {
		// nothing too old, but every snapshot wrote its files as loose objects, one compressed
		// file each: packed, they take a fraction of that. Unreferenced ones go after git's own
		// two weeks, so nothing a snapshot is writing right now.
		_, _ = sh.git(ctx, nil, gc...)
		return
	}
	if err := os.WriteFile(filepath.Join(sh.gitDir, "shallow"), []byte(cut+"\n"), 0o600); err != nil {
		return
	}
	_, _ = sh.git(ctx, nil, "reflog", "expire", "--expire=now", "--all")
	_, _ = sh.git(ctx, nil, append(gc, "--prune=now")...)
}

// prunedMark is a file in the shadow whose time says when it was last pruned. It is on disk:
// kept in memory only, a hub restarted more often than once a day never reached a prune.
const prunedMark = "nb-pruned"

// ---- following the agents ---------------------------------------------------------------

// SessionChanged says whether a console's agent is at work now.
func (m *Manager) SessionChanged(projectID, sessionID, name, kind string, working, hooks bool) {
	if projectID == "" {
		return
	}
	m.mu.Lock()
	closed := m.closed
	if !closed {
		m.wg.Add(1)
	}
	m.mu.Unlock()
	if closed {
		return
	}
	defer m.wg.Done()
	p := m.project(projectID)
	p.mu.Lock()
	if p.want[sessionID] == working {
		p.mu.Unlock()
		return
	}
	if working {
		p.want[sessionID] = true
	} else {
		delete(p.want, sessionID)
	}
	p.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		// what is there before the turn is not its own; what is there after it is
		_ = p.snapshot()
		p.mu.Lock()
		// by now the turn may be over, or may have begun again
		if p.want[sessionID] {
			p.active[sessionID] = &activeAgent{name: name, kind: kind, hooks: hooks}
			p.quiet = 0 // a turn begins: look often again
			start := !p.ticking
			p.ticking = true
			p.mu.Unlock()
			if start {
				m.wg.Add(1)
				go p.tick()
			}
			return
		}
		delete(p.active, sessionID)
		p.mu.Unlock()
	}()
}

func isRemote(roots []Root) bool {
	for _, r := range roots {
		if r.Mirror != nil {
			return true
		}
	}
	return false
}

// Prepare starts the copy of a server's project before anyone needs it. What an agent changes
// before the first copy is complete has nothing to be compared with.
func (m *Manager) Prepare(projectID string) {
	if isRemote(m.cfg.Roots(projectID)) {
		m.Soon(projectID)
	}
}

// Soon takes a snapshot shortly, once for a burst of saves.
func (m *Manager) Soon(projectID string) {
	roots := m.cfg.Roots(projectID)
	if len(roots) == 0 {
		return
	}
	after := soonAfter
	if isRemote(roots) {
		after = remoteSoonAfter
	}
	p := m.project(projectID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.soon != nil {
		return
	}
	p.soon = time.AfterFunc(after, func() {
		p.mu.Lock()
		p.soon = nil
		p.mu.Unlock()
		_ = p.snapshot()
	})
}

// Hint says that a hook named this file for that console.
func (m *Manager) Hint(projectID, sessionID, absPath string) {
	if projectID == "" || !filepath.IsAbs(absPath) {
		return
	}
	p := m.project(projectID)
	p.mu.Lock()
	defer p.mu.Unlock()
	absPath = filepath.Clean(absPath)
	if p.hints[absPath] == nil {
		p.hints[absPath] = map[string]bool{}
	}
	p.hints[absPath][sessionID] = true
}

// UserWrote says that the owner saved this file, from the editor: no agent's doing.
func (m *Manager) UserWrote(absPath string) {
	absPath = filepath.Clean(absPath)
	m.mu.Lock()
	all := make([]*project, 0, len(m.projects))
	for _, p := range m.projects {
		all = append(all, p)
	}
	m.mu.Unlock()
	for _, p := range all {
		p.mu.Lock()
		if len(p.active) > 0 {
			p.ignored[absPath] = true
		}
		p.mu.Unlock()
	}
}

func (p *project) tick() {
	defer p.m.wg.Done()
	every := tickEvery
	if isRemote(p.m.cfg.Roots(p.id)) {
		every = remoteTickEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	skipped := 0
	for {
		select {
		case <-p.stopped:
			return
		case <-t.C:
		}
		p.mu.Lock()
		done := len(p.active) == 0
		if done {
			p.ticking = false
		}
		wait := p.quiet >= quietAfter && len(p.hints) == 0
		p.mu.Unlock()
		if done {
			return
		}
		if wait && skipped < quietEvery-1 {
			skipped++
			continue
		}
		skipped = 0
		_ = p.snapshot()
	}
}

// Snapshot stores the project's work trees now.
func (m *Manager) Snapshot(projectID string) error {
	if len(m.cfg.Roots(projectID)) == 0 {
		return errUnsupported
	}
	return m.project(projectID).snapshot()
}

func (p *project) nestedIn(roots []Root, r Root) []string {
	var out []string
	for _, o := range roots {
		if o.Rel == r.Rel {
			continue
		}
		if r.Rel == "" {
			out = append(out, o.Rel)
		} else if rest, ok := strings.CutPrefix(o.Rel, r.Rel+"/"); ok {
			out = append(out, rest)
		}
	}
	return out
}

func (p *project) snapshot() error {
	p.snap.Lock()
	defer p.snap.Unlock()
	roots := p.m.cfg.Roots(p.id)
	if len(roots) == 0 {
		return errUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), snapshotLimit)
	defer cancel()

	p.mu.Lock()
	who := make([]string, 0, len(p.active))
	for _, a := range p.active {
		who = append(who, a.name)
	}
	p.mu.Unlock()
	sort.Strings(who)
	label := "You"
	if len(who) > 0 {
		label = strings.Join(who, ", ")
	}

	changed, seen := false, false
	var firstErr error
	pulled := map[*Mirror]error{}
	for _, r := range roots {
		if r.Mirror != nil {
			err, done := pulled[r.Mirror]
			if !done {
				err = r.Mirror.Pull(ctx)
				pulled[r.Mirror] = err
			}
			if err != nil {
				// No snapshot of a copy that may be behind: what it lacks would look deleted.
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		sh := p.shadowOf(r.Path)
		_, list, had, err := sh.take(ctx, label, p.nestedIn(roots, r))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		seen = seen || len(list) > 0
		if had && p.attribute(ctx, sh, r, list) {
			changed = true
		}
		p.maybePrune(ctx, sh)
	}
	p.mu.Lock()
	if seen || firstErr != nil {
		p.quiet = 0
	} else {
		p.quiet++
	}
	p.hints = map[string]map[string]bool{}
	p.ignored = map[string]bool{}
	problem := ""
	if isRemote(roots) && firstErr != nil {
		problem = firstErr.Error()
	}
	told := problem != p.problem
	p.problem = problem
	if changed {
		p.saveLocked()
	}
	p.mu.Unlock()
	if changed || told {
		p.publish()
	}
	return firstErr
}

// maybePrune prunes the shadow once a day: at its first snapshot after the mark is a day old,
// or after a first snapshot with no mark at all, which packs what it wrote.
func (p *project) maybePrune(ctx context.Context, sh shadow) {
	now := p.m.cfg.Now()
	mark := filepath.Join(sh.gitDir, prunedMark)
	p.mu.Lock()
	last, seen := p.pruned[sh.root]
	if !seen {
		last = time.Time{}
		if st, err := os.Stat(mark); err == nil {
			last = st.ModTime()
		}
		p.pruned[sh.root] = last
	}
	due := now.Sub(last) >= pruneEvery
	if due {
		p.pruned[sh.root] = now
	}
	p.mu.Unlock()
	if !due {
		return
	}
	sh.prune(ctx, now)
	if f, err := os.Create(mark); err == nil {
		f.Close()
	}
	_ = os.Chtimes(mark, now, now)
}

func relPath(r Root, inRoot string) string {
	if r.Rel == "" {
		return inRoot
	}
	return r.Rel + "/" + inRoot
}

// attribute gives what changed to the agents at work. A hook that named the file decides. If none
// did, agents without such hooks come first, since a CLI with hooks would have named it.
func (p *project) attribute(ctx context.Context, sh shadow, r Root, list []rawChange) bool {
	changed := false
	for _, c := range list {
		abs := r.known(c.path)
		rel := relPath(r, c.path)

		p.mu.Lock()
		// the file came back to where a review started: nothing left to look at
		back := map[string]bool{}
		for id, a := range p.state.Agents {
			if ch := a.Files[rel]; ch != nil && sameContent(ch.Base, c.newBlob) {
				sh.unkeep(ctx, ch.Base)
				delete(a.Files, rel)
				back[id] = true
				changed = true
			}
		}
		owners := map[string]bool{}
		unsure := false
		if !p.ignored[abs] {
			for id := range p.hints[abs] {
				if _, ok := p.active[id]; ok {
					owners[id] = true
				}
			}
			if len(owners) == 0 {
				var plain, hooked []string
				for id, a := range p.active {
					if a.hooks {
						hooked = append(hooked, id)
					} else {
						plain = append(plain, id)
					}
				}
				pick := plain
				if len(pick) == 0 {
					pick = hooked
				}
				for _, id := range pick {
					owners[id] = true
				}
				unsure = len(pick) > 1
			}
		}
		type todo struct {
			id     string
			change *Change
		}
		var fresh []todo
		for id := range owners {
			if back[id] {
				continue
			}
			a := p.state.Agents[id]
			if a == nil {
				info := p.active[id]
				a = &Agent{SessionID: id, Name: info.name, Kind: info.kind, Files: map[string]*Change{}}
				p.state.Agents[id] = a
			}
			if a.Files[rel] != nil {
				continue
			}
			base := c.oldBlob
			if base == zeroHash {
				base = ""
			}
			ch := &Change{Path: rel, Root: r.Path, Base: base, BaseMode: c.oldMode, Unsure: unsure, Since: p.m.cfg.Now().UTC()}
			a.Files[rel] = ch
			fresh = append(fresh, todo{id, ch})
			changed = true
		}
		// a pending change whose current state moved needs new numbers
		var stale []*Change
		for _, a := range p.state.Agents {
			if ch := a.Files[rel]; ch != nil {
				stale = append(stale, ch)
			}
		}
		p.mu.Unlock()

		for _, f := range fresh {
			sh.keep(ctx, f.change.Base)
		}
		for _, ch := range stale {
			base := ch.Base
			add, del, bin := sh.numstat(ctx, base, c.newBlob)
			p.mu.Lock()
			ch.Add, ch.Del, ch.Binary = add, del, bin
			switch {
			case base == "":
				ch.Status = "added"
			case c.newBlob == zeroHash:
				ch.Status = "deleted"
			default:
				ch.Status = "modified"
			}
			changed = true
			p.mu.Unlock()
		}
	}
	if changed {
		p.noticeOverlaps()
	}
	return changed
}

func sameContent(base, blob string) bool {
	if base == "" {
		return blob == zeroHash
	}
	return base == blob
}

// noticeOverlaps says once when two agents hold changes to the same file.
func (p *project) noticeOverlaps() {
	p.mu.Lock()
	owners := map[string][]string{}
	for _, a := range p.state.Agents {
		for rel, ch := range a.Files {
			if !ch.Unsure {
				owners[rel] = append(owners[rel], a.Name)
			}
		}
	}
	type notice struct {
		path  string
		names []string
	}
	var news []notice
	for rel, names := range owners {
		if len(names) < 2 {
			delete(p.overlap, rel)
			continue
		}
		if !p.overlap[rel] {
			p.overlap[rel] = true
			sort.Strings(names)
			news = append(news, notice{rel, names})
		}
	}
	p.mu.Unlock()
	if p.m.cfg.Publish == nil {
		return
	}
	for _, n := range news {
		p.m.cfg.Publish(map[string]any{"type": "reviewOverlap", "projectId": p.id, "path": n.path, "agents": n.names})
	}
}

// ---- looking at it, taking it, undoing it ---------------------------------------------------

// List says what each agent has changed.
func (m *Manager) List(projectID string) Summary {
	if len(m.cfg.Roots(projectID)) == 0 {
		return Summary{Supported: false, Agents: []AgentFiles{}}
	}
	p := m.project(projectID)
	p.mu.Lock()
	defer p.mu.Unlock()
	byPath := map[string][]string{}
	for _, a := range p.state.Agents {
		for rel := range a.Files {
			byPath[rel] = append(byPath[rel], a.Name)
		}
	}
	out := Summary{Supported: true, Problem: p.problem, Agents: []AgentFiles{}}
	for id, a := range p.state.Agents {
		if len(a.Files) == 0 {
			continue
		}
		_, working := p.active[id]
		af := AgentFiles{SessionID: id, Name: a.Name, Kind: a.Kind, Working: working, Files: make([]Change, 0, len(a.Files))}
		for rel, ch := range a.Files {
			c := *ch
			for _, n := range byPath[rel] {
				if n != a.Name {
					c.Also = append(c.Also, n)
				}
			}
			sort.Strings(c.Also)
			c.Root, c.Base, c.BaseMode = "", "", ""
			af.Files = append(af.Files, c)
		}
		sort.Slice(af.Files, func(i, j int) bool { return af.Files[i].Path < af.Files[j].Path })
		out.Agents = append(out.Agents, af)
	}
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].Name < out.Agents[j].Name })
	return out
}

func (p *project) change(sessionID, rel string) (*Agent, *Change, error) {
	a := p.state.Agents[sessionID]
	if a == nil || a.Files[rel] == nil {
		return nil, nil, errNoChange
	}
	return a, a.Files[rel], nil
}

func looksBinary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8<<10)], 0) >= 0
}

// File gives both sides of a pending change.
func (m *Manager) File(projectID, sessionID, rel string) (*FileView, error) {
	p := m.project(projectID)
	p.mu.Lock()
	_, ch, err := p.change(sessionID, rel)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	c := *ch
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), readLimit)
	defer cancel()
	v := &FileView{Path: rel}
	if r, ok := p.rootOf(c); ok && r.Mirror != nil {
		_ = r.Mirror.Pull(ctx) // a look at the last known state is better than no look
	}
	sh := p.shadowOf(c.Root)
	if c.Base != "" {
		b, err := sh.git(ctx, nil, "cat-file", "blob", c.Base)
		if err != nil {
			return nil, err
		}
		v.BaseExists = true
		if looksBinary(b) {
			v.Binary = true
		} else {
			v.Base = string(b)
		}
	}
	abs, err := insideRoot(c.Root, p.relInRoot(c, rel))
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		v.CurrentExists = true
		if st.Size() > MaxFile {
			v.TooBig = true
			return v, nil
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		if looksBinary(b) {
			v.Binary = true
		} else if !v.Binary {
			v.Current = string(b)
		}
	}
	if v.Binary {
		v.Base, v.Current = "", ""
	}
	return v, nil
}

// rootOf finds the root a change belongs to.
func (p *project) rootOf(c Change) (Root, bool) {
	for _, r := range p.m.cfg.Roots(p.id) {
		if r.Path == c.Root {
			return r, true
		}
	}
	return Root{}, false
}

// relInRoot turns a project-relative path into one inside its root.
func (p *project) relInRoot(c Change, rel string) string {
	for _, r := range p.m.cfg.Roots(p.id) {
		if r.Path == c.Root && r.Rel != "" {
			return strings.TrimPrefix(rel, r.Rel+"/")
		}
	}
	return rel
}

// insideRoot refuses a path that leaves the root, also through a link in a parent folder.
func insideRoot(root, rel string) (string, error) {
	abs := filepath.Join(root, rel)
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", errBadPath
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return abs, nil
		}
		return "", err
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if dir != real && !strings.HasPrefix(dir, real+string(filepath.Separator)) {
		return "", errBadPath
	}
	return abs, nil
}

// Accept takes a change: it leaves the file as it is and forgets the change. With base, only part
// of it was taken, and base is the file with that part in it.
func (m *Manager) Accept(projectID, sessionID, rel string, base *string) error {
	p := m.project(projectID)
	ctx, cancel := context.WithTimeout(context.Background(), readLimit)
	defer cancel()
	p.mu.Lock()
	a := p.state.Agents[sessionID]
	if a == nil {
		p.mu.Unlock()
		return errNoChange
	}
	var list []*Change
	if rel == "" {
		for _, ch := range a.Files {
			list = append(list, ch)
		}
	} else if ch := a.Files[rel]; ch != nil {
		list = append(list, ch)
	} else {
		p.mu.Unlock()
		return errNoChange
	}
	p.mu.Unlock()

	if base != nil && rel != "" {
		return p.movedBase(ctx, sessionID, list[0], *base)
	}
	p.mu.Lock()
	for _, ch := range list {
		p.shadowOf(ch.Root).unkeep(ctx, ch.Base)
		delete(a.Files, ch.Path)
	}
	if len(a.Files) == 0 {
		delete(p.state.Agents, sessionID)
	}
	p.saveLocked()
	p.mu.Unlock()
	p.noticeOverlaps()
	p.publish()
	return nil
}

// movedBase takes part of a change: the review goes on from the new text.
func (p *project) movedBase(ctx context.Context, sessionID string, ch *Change, base string) error {
	sh := p.shadowOf(ch.Root)
	out, err := sh.git(ctx, []byte(base), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	blob := strings.TrimSpace(string(out))
	abs, err := insideRoot(ch.Root, p.relInRoot(*ch, ch.Path))
	if err != nil {
		return err
	}
	cur, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	if string(cur) == base {
		return p.m.Accept(p.id, sessionID, ch.Path, nil)
	}
	curOut, err := sh.git(ctx, cur, "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	add, del, bin := sh.numstat(ctx, blob, strings.TrimSpace(string(curOut)))
	sh.keep(ctx, blob)
	p.mu.Lock()
	old := ch.Base
	ch.Base, ch.Add, ch.Del, ch.Binary = blob, add, del, bin
	if ch.Status == "added" {
		ch.Status = "modified"
	}
	p.saveLocked()
	p.mu.Unlock()
	sh.unkeep(ctx, old)
	p.publish()
	return nil
}

// Reject undoes a change: the file goes back to what the agent found. With text, only part of it
// was undone, and text is the file without that part.
func (m *Manager) Reject(projectID, sessionID, rel string, text *string) error {
	p := m.project(projectID)
	ctx, cancel := context.WithTimeout(context.Background(), readLimit)
	defer cancel()
	p.mu.Lock()
	a := p.state.Agents[sessionID]
	if a == nil {
		p.mu.Unlock()
		return errNoChange
	}
	var list []*Change
	if rel == "" {
		for _, ch := range a.Files {
			list = append(list, ch)
		}
	} else if ch := a.Files[rel]; ch != nil {
		list = append(list, ch)
	} else {
		p.mu.Unlock()
		return errNoChange
	}
	p.mu.Unlock()

	// nothing is lost: what the file holds now goes into the local history first
	if err := p.snapshot(); err != nil {
		return err
	}
	for _, ch := range list {
		inRoot := p.relInRoot(*ch, ch.Path)
		abs, err := insideRoot(ch.Root, inRoot)
		if err != nil {
			return err
		}
		root, _ := p.rootOf(*ch)
		p.mu.Lock()
		p.ignored[abs] = true
		if root.Mirror != nil {
			p.ignored[root.known(inRoot)] = true
		}
		p.mu.Unlock()
		if text != nil && rel != "" {
			if root.Mirror != nil {
				err = root.Mirror.WriteFile(ctx, path.Join(root.Rel, inRoot), []byte(*text), "")
			} else {
				err = writeFile(abs, []byte(*text), "")
			}
			if err != nil {
				return err
			}
			continue
		}
		if err := p.restore(ctx, *ch, abs, root, inRoot); err != nil {
			return err
		}
		p.mu.Lock()
		p.shadowOf(ch.Root).unkeep(ctx, ch.Base)
		if cur := p.state.Agents[sessionID]; cur != nil {
			delete(cur.Files, ch.Path)
			if len(cur.Files) == 0 {
				delete(p.state.Agents, sessionID)
			}
		}
		p.mu.Unlock()
	}
	p.mu.Lock()
	p.saveLocked()
	p.mu.Unlock()
	// the undo itself is no agent's change, and any other agent's view of the file moved with it
	if err := p.snapshot(); err != nil {
		return err
	}
	p.noticeOverlaps()
	p.publish()
	return nil
}

func (p *project) restore(ctx context.Context, ch Change, abs string, root Root, inRoot string) error {
	if ch.Base == "" {
		if root.Mirror != nil {
			return root.Mirror.RemoveFile(ctx, path.Join(root.Rel, inRoot))
		}
		err := os.Remove(abs)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := p.shadowOf(ch.Root).git(ctx, nil, "cat-file", "blob", ch.Base)
	if err != nil {
		return err
	}
	if root.Mirror != nil {
		return root.Mirror.WriteFile(ctx, path.Join(root.Rel, inRoot), b, ch.BaseMode)
	}
	if ch.BaseMode == "120000" {
		_ = os.Remove(abs)
		return os.Symlink(string(b), abs)
	}
	return writeFile(abs, b, ch.BaseMode)
}

func writeFile(abs string, b []byte, mode string) error {
	perm := os.FileMode(0o644)
	if st, err := os.Stat(abs); err == nil {
		perm = st.Mode().Perm()
	}
	switch mode {
	case "100755":
		perm = 0o755
	case "100644":
		perm &^= 0o111
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".nb-review-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), abs)
}

// Forget drops everything kept for a project.
// Rename moves what review keeps of one project (its snapshots, local history and server copies)
// to another name, as when a project becomes a part of another (ROADMAP #49). Nothing there is
// overwritten: when the new name has something already, the old one is forgotten.
func (m *Manager) Rename(from, to string) {
	if from == to {
		return
	}
	m.mu.Lock()
	for _, id := range []string{from, to} {
		if p := m.projects[id]; p != nil {
			p.stop() // its tick would run on, for nobody, until the hub stops
		}
		delete(m.projects, id)
	}
	m.mu.Unlock()
	src, dst := filepath.Join(m.cfg.Dir, from), filepath.Join(m.cfg.Dir, to)
	if _, err := os.Stat(dst); err == nil {
		_ = os.RemoveAll(src)
		return
	}
	_ = os.Rename(src, dst)
}

func (m *Manager) Forget(projectID string) {
	m.mu.Lock()
	if p := m.projects[projectID]; p != nil {
		p.stop()
	}
	delete(m.projects, projectID)
	m.mu.Unlock()
	_ = os.RemoveAll(filepath.Join(m.cfg.Dir, projectID))
}

// ---- local history ----------------------------------------------------------------------

func (p *project) rootFor(rel string) (Root, string, error) {
	var best *Root
	roots := p.m.cfg.Roots(p.id)
	for i, r := range roots {
		if r.Rel == "" || rel == r.Rel || strings.HasPrefix(rel, r.Rel+"/") {
			if best == nil || len(r.Rel) > len(best.Rel) {
				best = &roots[i]
			}
		}
	}
	if best == nil {
		return Root{}, "", errUnsupported
	}
	in := rel
	if best.Rel != "" {
		in = strings.TrimPrefix(strings.TrimPrefix(rel, best.Rel), "/")
	}
	if in == "" || strings.HasPrefix(in, "../") || in == ".." || strings.Contains(in, "/../") || filepath.IsAbs(in) {
		return Root{}, "", errBadPath
	}
	return *best, in, nil
}

// History lists the states of a file, newest first. A snapshot is taken first, so that what the
// file holds now is the first entry.
func (m *Manager) History(projectID, rel string, limit int) ([]Version, error) {
	if len(m.cfg.Roots(projectID)) == 0 {
		return nil, errUnsupported
	}
	p := m.project(projectID)
	if err := p.snapshot(); err != nil {
		return nil, err
	}
	r, in, err := p.rootFor(rel)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	ctx, cancel := context.WithTimeout(context.Background(), readLimit)
	defer cancel()
	sh := p.shadowOf(r.Path)
	out, err := sh.git(ctx, nil, "log", "-n", strconv.Itoa(limit), "--format=%H%x00%ct%x00%s%x00", "--name-status", "--no-renames", "-z", historyRef, "--", in)
	if err != nil {
		return []Version{}, nil
	}
	return parseHistory(string(out)), nil
}

func parseHistory(out string) []Version {
	items := []Version{}
	// with -z and --name-status: "hash\0time\0who\0\0M\0path\0", then the next
	t := strings.Split(out, "\x00")
	for i := 0; i < len(t); {
		h := strings.TrimSpace(t[i])
		if len(h) != 40 && len(h) != 64 {
			i++
			continue
		}
		if i+2 >= len(t) {
			break
		}
		sec, _ := strconv.ParseInt(t[i+1], 10, 64)
		v := Version{Commit: h, At: time.Unix(sec, 0).UTC(), Who: t[i+2], Exists: true}
		for j := i + 3; j < len(t) && j <= i+5; j++ {
			if s := strings.TrimSpace(t[j]); s == "D" {
				v.Exists = false
				break
			}
		}
		items = append(items, v)
		i += 3
	}
	return items
}

// Version gives a file as it was in a snapshot.
func (m *Manager) Version(projectID, rel, commit string) (string, bool, error) {
	if len(m.cfg.Roots(projectID)) == 0 {
		return "", false, errUnsupported
	}
	p := m.project(projectID)
	r, in, err := p.rootFor(rel)
	if err != nil {
		return "", false, err
	}
	if len(commit) != 40 && len(commit) != 64 || strings.Trim(commit, "0123456789abcdef") != "" {
		return "", false, errNoVersion
	}
	ctx, cancel := context.WithTimeout(context.Background(), readLimit)
	defer cancel()
	b, err := p.shadowOf(r.Path).git(ctx, nil, "cat-file", "blob", commit+":"+in)
	if err != nil {
		return "", false, errNoVersion
	}
	return string(b), looksBinary(b), nil
}
