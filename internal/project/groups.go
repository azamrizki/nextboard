package project

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Groups are a tree of names over the projects, and tags are words on a project
// (docs/API-board.md §2). They only arrange the list: a project's id, folder and
// boards stay when it moves, and removing a group removes no project.

const (
	// GroupsFile sits next to projects.json.
	GroupsFile = "project-groups.json"
	// MaxGroupDepth is the number of levels, the top one included.
	MaxGroupDepth = 5
	// MaxTags is the limit per project. One tag follows MaxName.
	MaxTags = 12
)

var (
	ErrGroupNotFound  = errors.New("group tidak ditemukan")
	ErrGroupNameTaken = errors.New("nama group sudah dipakai di tingkat ini")
	ErrGroupTooDeep   = fmt.Errorf("group maksimal %d tingkat", MaxGroupDepth)
	ErrGroupCycle     = errors.New("group tidak bisa dipindah ke dalam dirinya sendiri")
	ErrBadTag         = fmt.Errorf("tag maksimal %d karakter tanpa koma, paling banyak %d per project", MaxName, MaxTags)
)

// Group is one level of the tree. An empty ParentID is the top level.
type Group struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ParentID  string    `json:"parentId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Patch is what PATCH /api/projects/{id} can change. Nil leaves a field alone.
type Patch struct {
	Name *string
	// Opened marks the project opened now.
	Opened bool
	// GroupID "" takes the project out of its group.
	GroupID *string
	Tags    *[]string
}

// openGroups loads the tree. A hub from before groups has no file yet, and that
// is the one time names are read for groups.
func (s *Store) openGroups() error {
	var list []*Group
	found, err := s.st.Load(GroupsFile, &list)
	if err != nil {
		return err
	}
	if !found {
		return s.groupsFromPrefixes()
	}
	for _, g := range list {
		if g != nil && g.ID != "" {
			s.groups = append(s.groups, g)
		}
	}
	s.repairGroups()
	return nil
}

// groupsFromPrefixes moves a prefix into a group: "Acme - Portal" becomes Portal
// in group Acme. Projects were named that way because there were no groups. Only
// " - " counts, so "demo-backoffice" stays one name.
func (s *Store) groupsFromPrefixes() error {
	byName := map[string]*Group{}
	now := time.Now().UTC()
	for _, p := range s.list {
		prefix, rest, ok := strings.Cut(p.Name, " - ")
		prefix, rest = strings.TrimSpace(prefix), strings.TrimSpace(rest)
		if !ok || prefix == "" || rest == "" {
			continue
		}
		key := strings.ToLower(prefix)
		g := byName[key]
		if g == nil {
			g = &Group{ID: randomID("g_"), Name: prefix, CreatedAt: now}
			byName[key] = g
			s.groups = append(s.groups, g)
		}
		p.GroupID, p.Name = g.ID, rest
	}
	// Groups first: stopped between the two files, the names are still as they were.
	if err := s.saveGroupsLocked(); err != nil {
		return err
	}
	if len(byName) == 0 {
		return nil
	}
	return s.saveLocked()
}

// repairGroups makes a file that was edited or half written safe to walk: what
// lost its parent goes to the top, and a project of a missing group has none.
func (s *Store) repairGroups() {
	byID := make(map[string]*Group, len(s.groups))
	for _, g := range s.groups {
		byID[g.ID] = g
	}
	for _, g := range s.groups {
		seen := map[string]bool{g.ID: true}
		for at := g; at.ParentID != ""; at = byID[at.ParentID] {
			if byID[at.ParentID] == nil || seen[at.ParentID] {
				at.ParentID = ""
				break
			}
			seen[at.ParentID] = true
		}
	}
	for _, p := range s.list {
		if p.GroupID != "" && byID[p.GroupID] == nil {
			p.GroupID = ""
		}
	}
}

func (s *Store) saveGroupsLocked() error {
	out := make([]*Group, len(s.groups))
	copy(out, s.groups)
	return s.st.Save(GroupsFile, out)
}

// CleanTags trims the tags, drops empty ones and repeats (the first spelling
// stays), and keeps the order.
func CleanTags(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > MaxName || strings.Contains(t, ",") || strings.IndexFunc(t, unicode.IsControl) >= 0 {
			return nil, ErrBadTag
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return nil, ErrBadTag
	}
	return out, nil
}

// Patch changes a project in one save, and nothing when any part is refused.
func (s *Store) Patch(id string, in Patch) (Project, error) {
	var name string
	var tags []string
	var err error
	if in.Name != nil {
		if name, err = CleanName(*in.Name); err != nil {
			return Project{}, err
		}
	}
	if in.Tags != nil {
		if tags, err = CleanTags(*in.Tags); err != nil {
			return Project{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(id)
	if p == nil {
		return Project{}, ErrNotFound
	}
	if in.GroupID != nil && *in.GroupID != "" && s.groupLocked(*in.GroupID) == nil {
		return Project{}, ErrGroupNotFound
	}
	if in.Name != nil {
		p.Name = name
	}
	if in.Opened {
		p.LastOpenedAt = time.Now().UTC()
	}
	if in.GroupID != nil {
		p.GroupID = *in.GroupID
	}
	if in.Tags != nil {
		p.Tags = tags
	}
	return p.clone(), s.saveLocked()
}

// Groups returns the whole tree, by name.
func (s *Store) Groups() []Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Group, 0, len(s.groups))
	for _, g := range s.groups {
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (s *Store) groupLocked(id string) *Group {
	for _, g := range s.groups {
		if g.ID == id {
			return g
		}
	}
	return nil
}

// groupDepthLocked counts the levels down to id; the top level ("") has none.
func (s *Store) groupDepthLocked(id string) int {
	n := 0
	for g := s.groupLocked(id); g != nil; g = s.groupLocked(g.ParentID) {
		n++
	}
	return n
}

// groupHeightLocked counts the levels from id down, id included.
func (s *Store) groupHeightLocked(id string) int {
	n := 1
	for _, g := range s.groups {
		if g.ParentID == id {
			n = max(n, s.groupHeightLocked(g.ID)+1)
		}
	}
	return n
}

// groupInsideLocked reports whether id is root or anywhere below it.
func (s *Store) groupInsideLocked(id, root string) bool {
	for g := s.groupLocked(id); g != nil; g = s.groupLocked(g.ParentID) {
		if g.ID == root {
			return true
		}
	}
	return false
}

func (s *Store) groupNameTakenLocked(parentID, name, except string) bool {
	for _, g := range s.groups {
		if g.ParentID == parentID && g.ID != except && strings.EqualFold(g.Name, name) {
			return true
		}
	}
	return false
}

// CreateGroup adds a group at the top level or inside parentID.
func (s *Store) CreateGroup(name, parentID string) (Group, error) {
	name, err := CleanName(name)
	if err != nil {
		return Group{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if parentID != "" && s.groupLocked(parentID) == nil {
		return Group{}, ErrGroupNotFound
	}
	if s.groupDepthLocked(parentID)+1 > MaxGroupDepth {
		return Group{}, ErrGroupTooDeep
	}
	if s.groupNameTakenLocked(parentID, name, "") {
		return Group{}, ErrGroupNameTaken
	}
	g := &Group{ID: randomID("g_"), Name: name, ParentID: parentID, CreatedAt: time.Now().UTC()}
	s.groups = append(s.groups, g)
	return *g, s.saveGroupsLocked()
}

// UpdateGroup renames a group and/or moves it with everything inside.
func (s *Store) UpdateGroup(id string, name, parentID *string) (Group, error) {
	var clean string
	var err error
	if name != nil {
		if clean, err = CleanName(*name); err != nil {
			return Group{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groupLocked(id)
	if g == nil {
		return Group{}, ErrGroupNotFound
	}
	nextName, nextParent := g.Name, g.ParentID
	if name != nil {
		nextName = clean
	}
	if parentID != nil {
		nextParent = *parentID
	}
	if nextParent != g.ParentID {
		if nextParent != "" && s.groupLocked(nextParent) == nil {
			return Group{}, ErrGroupNotFound
		}
		if s.groupInsideLocked(nextParent, id) {
			return Group{}, ErrGroupCycle
		}
		if s.groupDepthLocked(nextParent)+s.groupHeightLocked(id) > MaxGroupDepth {
			return Group{}, ErrGroupTooDeep
		}
	}
	if (nextParent != g.ParentID || !strings.EqualFold(nextName, g.Name)) && s.groupNameTakenLocked(nextParent, nextName, id) {
		return Group{}, ErrGroupNameTaken
	}
	g.Name, g.ParentID = nextName, nextParent
	return *g, s.saveGroupsLocked()
}

// DeleteGroup removes a group. What was inside moves up one level, and the ids
// of the projects that moved are returned. Two groups may end up with the same
// name there: everything goes by id, and refusing would leave no way to remove it.
func (s *Store) DeleteGroup(id string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groupLocked(id)
	if g == nil {
		return nil, ErrGroupNotFound
	}
	moved := []string{}
	for _, p := range s.list {
		if p.GroupID == id {
			p.GroupID = g.ParentID
			moved = append(moved, p.ID)
		}
	}
	for _, c := range s.groups {
		if c.ParentID == id {
			c.ParentID = g.ParentID
		}
	}
	s.groups = slices.DeleteFunc(s.groups, func(x *Group) bool { return x.ID == id })
	// The projects first: stopped between the two files, a missing group is
	// repaired at the next start, and these already are where they should be.
	if err := s.saveLocked(); err != nil {
		return moved, err
	}
	return moved, s.saveGroupsLocked()
}
