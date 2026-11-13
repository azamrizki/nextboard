package project

import (
	"errors"
	"fmt"
	"strings"
)

// Part is one place of a project's code: a folder on a machine, named for its role ("Backend",
// "Frontend"), so one project can span the machines its pieces run on (ROADMAP #49). The first
// part is the project's own machineId and path; the project keeps both fields in step with it,
// so a client that knows nothing of parts still sees the project as before.
type Part struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MachineID string `json:"machineId"`
	Path      string `json:"path"`
	// ReadOnly: agents get only the read tools there, whichever part they work from.
	ReadOnly bool `json:"readOnly,omitempty"`
}

const (
	// MainPart is the id of the first part of a project made before parts, or with one.
	MainPart = "main"
	// MaxParts is how many parts one project may have.
	MaxParts = 8
)

var (
	ErrPartNotFound = errors.New("part not found")
	ErrPartOverlap  = errors.New("that folder is inside another part of this project, or holds one")
	ErrMainPart     = errors.New("the first part is the project's own folder: move another part first to remove it")
	ErrTooManyParts = fmt.Errorf("a project has at most %d parts", MaxParts)
	ErrPartName     = errors.New("another part of this project has that name")
)

// InProjectError: the folder is already another project, or a part of one.
type InProjectError struct{ ProjectID, Name string }

func (e *InProjectError) Error() string {
	return fmt.Sprintf("that folder is already in project %q", e.Name)
}

// Part returns a part of the project; "" is the first part.
func (p Project) Part(id string) (Part, bool) {
	if len(p.Parts) == 0 {
		return Part{}, false
	}
	if id == "" {
		return p.Parts[0], true
	}
	for _, pt := range p.Parts {
		if pt.ID == id {
			return pt, true
		}
	}
	return Part{}, false
}

// PartAt returns the part that holds dir on machineID, if any: the deepest one.
func (p Project) PartAt(machineID, dir string) (Part, bool) {
	var best Part
	found := false
	for _, pt := range p.Parts {
		if pt.MachineID == machineID && (pt.Path == dir || isUnder(dir, pt.Path)) && (!found || len(pt.Path) > len(best.Path)) {
			best, found = pt, true
		}
	}
	return best, found
}

// normParts gives a project from before parts its one part, and keeps machineId and path in
// step with the first part.
func (p *Project) normParts() {
	if len(p.Parts) == 0 {
		p.Parts = []Part{{ID: MainPart, Name: "Main", MachineID: p.MachineID, Path: p.Path}}
	}
	p.MachineID, p.Path = p.Parts[0].MachineID, p.Parts[0].Path
}

// isUnder reports whether dir is strictly inside parent (both absolute and clean).
func isUnder(dir, parent string) bool {
	if parent == "/" {
		return dir != "/"
	}
	return strings.HasPrefix(dir, parent+"/")
}

func overlaps(a, b string) bool { return a == b || isUnder(a, b) || isUnder(b, a) }

// holderLocked is the project (other than skip) that has machineID:dir as one of its parts.
func (s *Store) holderLocked(machineID, dir, skip string) *Project {
	for _, x := range s.list {
		if x.ID == skip {
			continue
		}
		for _, pt := range x.Parts {
			if pt.MachineID == machineID && pt.Path == dir {
				return x
			}
		}
	}
	return nil
}

// AddPart adds a folder to a project as a new part, at the end. dir must already be an absolute,
// cleaned path that was verified to be a folder on machineID.
func (s *Store) AddPart(projectID, name, machineID, dir string) (Project, Part, error) {
	name, err := CleanName(name)
	if err != nil {
		return Project{}, Part{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(projectID)
	if p == nil {
		return Project{}, Part{}, ErrNotFound
	}
	if len(p.Parts) >= MaxParts {
		return Project{}, Part{}, ErrTooManyParts
	}
	for _, pt := range p.Parts {
		if strings.EqualFold(pt.Name, name) {
			return Project{}, Part{}, ErrPartName
		}
		if pt.MachineID == machineID && overlaps(pt.Path, dir) {
			return Project{}, Part{}, ErrPartOverlap
		}
	}
	if x := s.holderLocked(machineID, dir, p.ID); x != nil {
		return Project{}, Part{}, &InProjectError{ProjectID: x.ID, Name: x.Name}
	}
	pt := Part{ID: randomID("pt_"), Name: name, MachineID: machineID, Path: dir}
	p.Parts = append(p.Parts, pt)
	if err := s.saveLocked(); err != nil {
		p.Parts = p.Parts[:len(p.Parts)-1]
		return Project{}, Part{}, err
	}
	return p.clone(), pt, nil
}

// PartPatch changes a part; nil fields stay.
type PartPatch struct {
	Name     *string
	ReadOnly *bool
	// Index moves the part; moving one to 0 makes it the project's own folder.
	Index *int
}

// PatchPart renames, marks or moves a part.
func (s *Store) PatchPart(projectID, partID string, in PartPatch) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(projectID)
	if p == nil {
		return Project{}, ErrNotFound
	}
	i := -1
	for k, pt := range p.Parts {
		if pt.ID == partID {
			i = k
		}
	}
	if i < 0 {
		return Project{}, ErrPartNotFound
	}
	next := p.clone()
	pt := next.Parts[i]
	if in.Name != nil {
		name, err := CleanName(*in.Name)
		if err != nil {
			return Project{}, err
		}
		for k, o := range next.Parts {
			if k != i && strings.EqualFold(o.Name, name) {
				return Project{}, ErrPartName
			}
		}
		pt.Name = name
	}
	if in.ReadOnly != nil {
		pt.ReadOnly = *in.ReadOnly
	}
	next.Parts[i] = pt
	if in.Index != nil {
		to := min(max(*in.Index, 0), len(next.Parts)-1)
		moved := append(next.Parts[:i:i], next.Parts[i+1:]...)
		next.Parts = append(moved[:to:to], append([]Part{pt}, moved[to:]...)...)
	}
	next.normParts()
	old := *p
	*p = next
	if err := s.saveLocked(); err != nil {
		*p = old
		return Project{}, err
	}
	return p.clone(), nil
}

// DeletePart removes a part from a project; its folder is not touched. The first part stays.
func (s *Store) DeletePart(projectID, partID string) (Project, Part, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findLocked(projectID)
	if p == nil {
		return Project{}, Part{}, ErrNotFound
	}
	for i, pt := range p.Parts {
		if pt.ID != partID {
			continue
		}
		if i == 0 {
			return Project{}, Part{}, ErrMainPart
		}
		old := p.Parts
		p.Parts = append(p.Parts[:i:i], p.Parts[i+1:]...)
		if err := s.saveLocked(); err != nil {
			p.Parts = old
			return Project{}, Part{}, err
		}
		return p.clone(), pt, nil
	}
	return Project{}, Part{}, ErrPartNotFound
}
