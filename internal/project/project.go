// Package project persists projects (a folder on a machine) and their
// workspaces (board tabs) in projects.json.
package project

import (
	"crypto/rand"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/store"
)

const (
	File = "projects.json"
	// MaxName is the display name limit of projects and workspaces.
	MaxName = 24
)

var (
	ErrNotFound          = errors.New("project not found")
	ErrWorkspaceNotFound = errors.New("workspace not found")
	ErrLastWorkspace     = errors.New("the last workspace of a project cannot be deleted")
	ErrBadName           = fmt.Errorf("a name is required, at most %d characters", MaxName)
	ErrBadSandbox        = errors.New(`sandbox must be "", "workspace" or "offline"`)
)

// Workspace is one board tab inside a project.
type Workspace struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// Project is a folder (usually a git repo) on a machine.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// MachineID and Path are those of the first part.
	MachineID string `json:"machineId"`
	Path      string `json:"path"`
	// Parts are the folders the project spans, the first one being MachineID and Path (#49).
	Parts        []Part      `json:"parts"`
	CreatedAt    time.Time   `json:"createdAt"`
	LastOpenedAt time.Time   `json:"lastOpenedAt"`
	Workspaces   []Workspace `json:"workspaces"` // tab order
	GroupID      string      `json:"groupId,omitempty"`
	Tags         []string    `json:"tags,omitempty"`
	// Sandbox limits what the commands of its agents may do (docs/API-agents.md §27):
	// "" = what each CLI does by itself, SandboxWorkspace = write only in the project
	// folder (and temp and package caches), SandboxOffline = that and no network.
	Sandbox string `json:"sandbox,omitempty"`
	// PartAccess is what its agents may do in the parts on other machines than their own (ROADMAP
	// #49): "" = only read, PartAccessFull = everything. The other parts on their own machine they
	// may always change. A read-only part stays read-only either way.
	PartAccess string `json:"partAccess,omitempty"`
	// Aliases are the ids of projects merged into this one (ROADMAP #49): an old link still finds it.
	Aliases []string `json:"aliases,omitempty"`
}

// Sandbox levels of a project.
const (
	SandboxWorkspace = "workspace"
	SandboxOffline   = "offline"
)

// Counts are live session counts used in the API views.
type Counts struct{ Total, Running int }

// WorkspaceView / View are the API forms with session counts.
type WorkspaceView struct {
	Workspace
	SessionCount int `json:"sessionCount"`
}

type View struct {
	Project
	Workspaces   []WorkspaceView `json:"workspaces"`
	SessionCount int             `json:"sessionCount"`
	RunningCount int             `json:"runningCount"`
}

// WorkspaceView returns the API view of w.
func (w Workspace) View(byWorkspace map[string]Counts) WorkspaceView {
	return WorkspaceView{Workspace: w, SessionCount: byWorkspace[w.ID].Total}
}

// View builds the API view; counts are per project and per workspace id.
func (p Project) View(byProject, byWorkspace map[string]Counts) View {
	v := View{Project: p, Workspaces: make([]WorkspaceView, 0, len(p.Workspaces))}
	for _, w := range p.Workspaces {
		v.Workspaces = append(v.Workspaces, w.View(byWorkspace))
	}
	c := byProject[p.ID]
	v.SessionCount, v.RunningCount = c.Total, c.Running
	return v
}

func (p Project) clone() Project {
	p.Workspaces = slices.Clone(p.Workspaces)
	p.Tags = slices.Clone(p.Tags)
	p.Parts = slices.Clone(p.Parts)
	p.Aliases = slices.Clone(p.Aliases)
	for i := range p.Parts {
		p.Parts[i].Blocked = slices.Clone(p.Parts[i].Blocked)
	}
	return p
}

// Store owns projects.json. Every change is saved immediately.
type Store struct {
	st      *store.Store
	existed bool

	mu     sync.Mutex
	list   []*Project
	groups []*Group
}

// Open loads projects.json.
func Open(st *store.Store) (*Store, error) {
	var list []*Project
	found, err := st.Load(File, &list)
	if err != nil {
		return nil, err
	}
	s := &Store{st: st, existed: found}
	for _, p := range list {
		if p == nil || p.ID == "" {
			continue
		}
		for i := range p.Workspaces {
			p.Workspaces[i].ProjectID = p.ID
		}
		p.normParts()
		s.list = append(s.list, p)
	}
	if err := s.openGroups(); err != nil {
		return nil, err
	}
	return s, nil
}

// Existed reports whether projects.json was present at Open (migration).
func (s *Store) Existed() bool { return s.existed }

func (s *Store) saveLocked() error {
	out := make([]*Project, len(s.list))
	copy(out, s.list)
	return s.st.Save(File, out)
}

// CleanName trims and validates a display name.
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxName || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", ErrBadName
	}
	return name, nil
}

// truncName shortens a default name to MaxName runes.
func truncName(name string) string {
	r := []rune(strings.TrimSpace(name))
	if len(r) > MaxName {
		r = r[:MaxName]
	}
	if len(r) == 0 {
		return "Project"
	}
	return string(r)
}

// List returns all projects, most recently opened first.
func (s *Store) List() []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Project, 0, len(s.list))
	for _, p := range s.list {
		out = append(out, p.clone())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastOpenedAt.After(out[j].LastOpenedAt) })
	return out
}

func (s *Store) findLocked(id string) (int, *Project) {
	for i, p := range s.list {
		if p.ID == id {
			return i, p
		}
	}
	return -1, nil
}

// Get returns one project.
func (s *Store) Get(id string) (Project, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(id)
	if p == nil {
		return Project{}, false
	}
	return p.clone(), true
}

// Create adds a project with one "Main" workspace. path must already be an
// absolute, cleaned path that was verified to be a folder. A folder that is already a project,
// or a part of one, returns that project (created=false).
func (s *Store) Create(machineID, dir, name string) (p Project, created bool, err error) {
	if name != "" {
		if name, err = CleanName(name); err != nil {
			return Project{}, false, err
		}
	} else {
		name = truncName(path.Base(dir))
		if dir == "/" {
			name = "/"
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// the folder may be another part of a project already
	if x := s.holderLocked(machineID, dir, ""); x != nil {
		return x.clone(), false, nil
	}
	now := time.Now().UTC()
	np := &Project{
		ID: randomID("p_"), Name: name, CreatedAt: now, LastOpenedAt: now,
		Parts: []Part{{ID: MainPart, Name: "Main", MachineID: machineID, Path: dir}},
	}
	np.normParts()
	np.Workspaces = []Workspace{{ID: randomID("w_"), ProjectID: np.ID, Name: "Main", CreatedAt: now}}
	s.list = append(s.list, np)
	if err := s.saveLocked(); err != nil {
		return Project{}, false, err
	}
	return np.clone(), true, nil
}

// Update renames a project and/or marks it opened now.
func (s *Store) Update(id string, name *string, opened bool) (Project, error) {
	return s.Patch(id, Patch{Name: name, Opened: opened})
}

// Delete removes a project and returns it (for archiving its boards).
func (s *Store) Delete(id string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, p := s.findLocked(id)
	if p == nil {
		return Project{}, ErrNotFound
	}
	s.list = slices.Delete(s.list, i, i+1)
	return p.clone(), s.saveLocked()
}

// Workspace finds a workspace and its project.
func (s *Store) Workspace(wid string) (Workspace, Project, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.list {
		for _, w := range p.Workspaces {
			if w.ID == wid {
				return w, p.clone(), true
			}
		}
	}
	return Workspace{}, Project{}, false
}

// Default is the first workspace of the most recently opened project.
func (s *Store) Default() (Project, Workspace, bool) {
	list := s.List()
	for _, p := range list {
		if len(p.Workspaces) > 0 {
			return p, p.Workspaces[0], true
		}
	}
	return Project{}, Workspace{}, false
}

// AddWorkspace appends a tab ("Workspace N" when name is empty).
func (s *Store) AddWorkspace(pid, name string) (Workspace, Project, error) {
	if name != "" {
		var err error
		if name, err = CleanName(name); err != nil {
			return Workspace{}, Project{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(pid)
	if p == nil {
		return Workspace{}, Project{}, ErrNotFound
	}
	if name == "" {
		taken := map[string]bool{}
		for _, w := range p.Workspaces {
			taken[w.Name] = true
		}
		for n := len(p.Workspaces) + 1; ; n++ {
			if name = fmt.Sprintf("Workspace %d", n); !taken[name] {
				break
			}
		}
	}
	w := Workspace{ID: randomID("w_"), ProjectID: p.ID, Name: name, CreatedAt: time.Now().UTC()}
	p.Workspaces = append(p.Workspaces, w)
	return w, p.clone(), s.saveLocked()
}

func (s *Store) findWorkspaceLocked(wid string) (*Project, int) {
	for _, p := range s.list {
		for i, w := range p.Workspaces {
			if w.ID == wid {
				return p, i
			}
		}
	}
	return nil, -1
}

// UpdateWorkspace renames and/or moves a tab (index is clamped).
func (s *Store) UpdateWorkspace(wid string, name *string, index *int) (Workspace, Project, error) {
	var clean string
	if name != nil {
		var err error
		if clean, err = CleanName(*name); err != nil {
			return Workspace{}, Project{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, i := s.findWorkspaceLocked(wid)
	if p == nil {
		return Workspace{}, Project{}, ErrWorkspaceNotFound
	}
	if name != nil {
		p.Workspaces[i].Name = clean
	}
	w := p.Workspaces[i]
	if index != nil {
		to := min(max(*index, 0), len(p.Workspaces)-1)
		p.Workspaces = slices.Delete(p.Workspaces, i, i+1)
		p.Workspaces = slices.Insert(p.Workspaces, to, w)
	}
	return w, p.clone(), s.saveLocked()
}

// DeleteWorkspace removes a tab; the last tab of a project cannot go.
func (s *Store) DeleteWorkspace(wid string) (Workspace, Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, i := s.findWorkspaceLocked(wid)
	if p == nil {
		return Workspace{}, Project{}, ErrWorkspaceNotFound
	}
	if len(p.Workspaces) <= 1 {
		return Workspace{}, Project{}, ErrLastWorkspace
	}
	w := p.Workspaces[i]
	p.Workspaces = slices.Delete(p.Workspaces, i, i+1)
	return w, p.clone(), s.saveLocked()
}

// CloseAllWorkspaces replaces every tab of a project with one new empty "Main".
// It returns the new tab and the ones it replaced.
func (s *Store) CloseAllWorkspaces(pid string) (Workspace, []Workspace, Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(pid)
	if p == nil {
		return Workspace{}, nil, Project{}, ErrNotFound
	}
	closed := p.Workspaces
	w := Workspace{ID: randomID("w_"), ProjectID: p.ID, Name: "Main", CreatedAt: time.Now().UTC()}
	p.Workspaces = []Workspace{w}
	return w, closed, p.clone(), s.saveLocked()
}

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
