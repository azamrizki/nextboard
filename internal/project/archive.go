package project

import (
	"errors"
	"sort"
	"time"
)

// Archive and project images (docs/API-board.md §1). An archived project keeps everything it
// has, its folders included; it is only out of the active list until it is restored. Images
// are assets of the hub's asset store, named here by id.

var (
	ErrNotArchived  = errors.New("the project is not archived")
	ErrBadImageKind = errors.New(`the image kind must be "cover" or "icon"`)
)

// Image kinds and sources.
const (
	ImageCover  = "cover"
	ImageIcon   = "icon"
	ImageUpload = "upload"
	ImageAI     = "ai"
)

// Image is a project's cover or icon: an asset, where it came from, and when it was set.
type Image struct {
	AssetID string    `json:"assetId"`
	Source  string    `json:"source"`
	At      time.Time `json:"at"`
}

// ImageChange in a Patch sets an image (Image) or removes it (Image nil).
type ImageChange struct{ Image *Image }

func cloneImage(im *Image) *Image {
	if im == nil {
		return nil
	}
	c := *im
	return &c
}

// Archived reports whether the project is archived.
func (p Project) Archived() bool { return p.ArchivedAt != nil }

// List returns the active projects, most recently opened first.
func (s *Store) List() []Project {
	return s.filter(func(p *Project) bool { return p.ArchivedAt == nil })
}

// ListAll returns every project, archived ones too, most recently opened first.
func (s *Store) ListAll() []Project { return s.filter(func(*Project) bool { return true }) }

// Archived returns the archived projects, most recently archived first.
func (s *Store) Archived() []Project {
	out := s.filter(func(p *Project) bool { return p.ArchivedAt != nil })
	sort.SliceStable(out, func(i, j int) bool { return out[i].ArchivedAt.After(*out[j].ArchivedAt) })
	return out
}

func (s *Store) filter(keep func(*Project) bool) []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Project, 0, len(s.list))
	for _, p := range s.list {
		if keep(p) {
			out = append(out, p.clone())
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastOpenedAt.After(out[j].LastOpenedAt) })
	return out
}

// IsArchived reports whether id is an archived project (false for one that does not exist).
func (s *Store) IsArchived(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(id)
	return p != nil && p.ArchivedAt != nil
}

// Archive marks a project archived. An archived one stays as it is.
func (s *Store) Archive(id string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(id)
	if p == nil {
		return Project{}, ErrNotFound
	}
	if p.ArchivedAt != nil {
		return p.clone(), nil
	}
	now := time.Now().UTC()
	p.ArchivedAt = &now
	if err := s.saveLocked(); err != nil {
		p.ArchivedAt = nil
		return Project{}, err
	}
	return p.clone(), nil
}

// Restore brings an archived project back to the active list (ErrNotArchived when it was not).
func (s *Store) Restore(id string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(id)
	if p == nil {
		return Project{}, ErrNotFound
	}
	if p.ArchivedAt == nil {
		return p.clone(), ErrNotArchived
	}
	was := p.ArchivedAt
	p.ArchivedAt = nil
	if err := s.saveLocked(); err != nil {
		p.ArchivedAt = was
		return Project{}, err
	}
	return p.clone(), nil
}

// SetImage sets (im) or removes (nil) a project's cover or icon.
func (s *Store) SetImage(id, kind string, im *Image) (Project, error) {
	c := &ImageChange{Image: im}
	switch kind {
	case ImageCover:
		return s.Patch(id, Patch{Cover: c})
	case ImageIcon:
		return s.Patch(id, Patch{Icon: c})
	}
	return Project{}, ErrBadImageKind
}
