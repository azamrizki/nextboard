package project

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Merge and split (ROADMAP #49): a project that was kept apart becomes a part of another, with its
// boards; a part becomes a project of its own again. Folders on disk are never touched.

var ErrMergeSelf = errors.New("a project cannot be merged into itself")

// MergePlan is what merging one project into another does to the project list.
type MergePlan struct {
	// Parts: the other project's parts as they will be named in this one.
	Parts []Part `json:"parts"`
	// Workspaces: its boards with the names they will have.
	Workspaces []Workspace `json:"workspaces"`
	// PartIDs maps each of its part ids to the new one; "" stands for a session or monitor with none.
	PartIDs map[string]string `json:"-"`
}

// planMergeLocked names and checks what merging from into into would add.
func (s *Store) planMergeLocked(into, from *Project) (MergePlan, error) {
	if into.ID == from.ID {
		return MergePlan{}, ErrMergeSelf
	}
	if len(into.Parts)+len(from.Parts) > MaxParts {
		return MergePlan{}, ErrTooManyParts
	}
	plan := MergePlan{PartIDs: map[string]string{}}
	taken := func(name string) bool {
		for _, pt := range into.Parts {
			if strings.EqualFold(pt.Name, name) {
				return true
			}
		}
		for _, pt := range plan.Parts {
			if strings.EqualFold(pt.Name, name) {
				return true
			}
		}
		return false
	}
	for i, pt := range from.Parts {
		for _, have := range into.Parts {
			if have.MachineID == pt.MachineID && overlaps(have.Path, pt.Path) {
				return MergePlan{}, fmt.Errorf("%w: %s", ErrPartOverlap, pt.Path)
			}
		}
		name := from.Name
		if len(from.Parts) > 1 {
			name = from.Name + " · " + pt.Name
		}
		name = uniqueName(truncName(name), taken)
		np := pt
		np.ID, np.Name = randomID("pt_"), name
		np.Blocked = slices.Clone(pt.Blocked)
		plan.Parts = append(plan.Parts, np)
		plan.PartIDs[pt.ID] = np.ID
		if i == 0 {
			plan.PartIDs[""] = np.ID
		}
	}
	wsTaken := func(name string) bool {
		for _, w := range into.Workspaces {
			if strings.EqualFold(w.Name, name) {
				return true
			}
		}
		for _, w := range plan.Workspaces {
			if strings.EqualFold(w.Name, name) {
				return true
			}
		}
		return false
	}
	for _, w := range from.Workspaces {
		w.ProjectID = into.ID
		if wsTaken(w.Name) {
			w.Name = uniqueName(truncName(from.Name+" · "+w.Name), wsTaken)
		}
		plan.Workspaces = append(plan.Workspaces, w)
	}
	return plan, nil
}

// uniqueName adds " 2", " 3"… until the name is free.
func uniqueName(name string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}
	for n := 2; ; n++ {
		suffix := fmt.Sprintf(" %d", n)
		r := []rune(name)
		if len(r)+len(suffix) > MaxName {
			r = r[:MaxName-len(suffix)]
		}
		if c := strings.TrimSpace(string(r)) + suffix; !taken(c) {
			return c
		}
	}
}

// PlanMerge says what merging from into into would do, changing nothing.
func (s *Store) PlanMerge(into, from string) (MergePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, a := s.findLocked(into)
	_, b := s.findLocked(from)
	if a == nil || b == nil {
		return MergePlan{}, ErrNotFound
	}
	return s.planMergeLocked(a, b)
}

// Merge makes the project from a part of into: its folders become parts, its boards workspaces, its
// tags into's too, and from is gone from the list. Its id stays known as an alias of into.
func (s *Store) Merge(into, from string) (Project, Project, MergePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, a := s.findLocked(into)
	j, b := s.findLocked(from)
	if a == nil || b == nil {
		return Project{}, Project{}, MergePlan{}, ErrNotFound
	}
	plan, err := s.planMergeLocked(a, b)
	if err != nil {
		return Project{}, Project{}, MergePlan{}, err
	}
	gone := b.clone()
	old := a.clone()
	a.Parts = append(a.Parts, plan.Parts...)
	a.Workspaces = append(a.Workspaces, plan.Workspaces...)
	for _, t := range b.Tags {
		if len(a.Tags) < MaxTags && !slices.ContainsFunc(a.Tags, func(x string) bool { return strings.EqualFold(x, t) }) {
			a.Tags = append(a.Tags, t)
		}
	}
	a.Aliases = append(a.Aliases, b.ID)
	a.Aliases = append(a.Aliases, b.Aliases...)
	if b.LastOpenedAt.After(a.LastOpenedAt) {
		a.LastOpenedAt = b.LastOpenedAt
	}
	s.list = append(s.list[:j:j], s.list[j+1:]...)
	if err := s.saveLocked(); err != nil {
		*a = old
		s.list = slices.Insert(s.list, j, b)
		return Project{}, Project{}, MergePlan{}, err
	}
	return a.clone(), gone, plan, nil
}

// Split makes a part a project of its own, with one empty board, in the same group and with the
// same tags. The first part stays.
func (s *Store) Split(projectID, partID string) (Project, Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(projectID)
	if p == nil {
		return Project{}, Project{}, ErrNotFound
	}
	i := slices.IndexFunc(p.Parts, func(pt Part) bool { return pt.ID == partID })
	switch {
	case i < 0:
		return Project{}, Project{}, ErrPartNotFound
	case i == 0:
		return Project{}, Project{}, ErrMainPart
	}
	pt := p.Parts[i]
	now := time.Now().UTC()
	np := &Project{ID: randomID("p_"), Name: pt.Name, CreatedAt: now, LastOpenedAt: now, GroupID: p.GroupID, Tags: slices.Clone(p.Tags),
		Parts: []Part{{ID: MainPart, Name: "Main", MachineID: pt.MachineID, Path: pt.Path, ReadOnly: pt.ReadOnly, Blocked: slices.Clone(pt.Blocked)}}}
	np.normParts()
	np.Workspaces = []Workspace{{ID: randomID("w_"), ProjectID: np.ID, Name: "Main", CreatedAt: now}}
	old := p.Parts
	p.Parts = append(p.Parts[:i:i], p.Parts[i+1:]...)
	s.list = append(s.list, np)
	if err := s.saveLocked(); err != nil {
		p.Parts = old
		s.list = s.list[:len(s.list)-1]
		return Project{}, Project{}, err
	}
	return np.clone(), p.clone(), nil
}
