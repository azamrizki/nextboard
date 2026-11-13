package project

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
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
	// Blocked are commands that need the owner's approval there, on top of the hub's and the
	// project's lists (ROADMAP #38).
	Blocked []string `json:"blocked,omitempty"`
}

// PartAccessFull lets agents change the parts on other machines than their own, not only read
// them. The other parts on their own machine they may always change.
const PartAccessFull = "full"

const (
	maxBlocked    = 50
	maxBlockedLen = 200
)

// cleanBlocked trims a part's blocked commands and drops empty and repeated ones.
func cleanBlocked(in []string) ([]string, error) {
	var out []string
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if len(c) > maxBlockedLen || strings.ContainsAny(c, "\r\n") {
			return nil, ErrBadBlocked
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) > maxBlocked {
		return nil, ErrBadBlocked
	}
	return out, nil
}

const (
	// MainPart is the id of the first part of a project made before parts, or with one.
	MainPart = "main"
	// MaxParts is how many parts one project may have.
	MaxParts = 8
)

var (
	ErrPartNotFound  = errors.New("part not found")
	ErrPartOverlap   = errors.New("that folder is inside another part of this project, or holds one")
	ErrMainPart      = errors.New("the first part is the project's own folder: move another part first to remove it")
	ErrTooManyParts  = fmt.Errorf("a project has at most %d parts", MaxParts)
	ErrPartName      = errors.New("another part of this project has that name")
	ErrBadPartAccess = errors.New(`partAccess must be "" (read only on other machines) or "full"`)
	ErrBadBlocked    = fmt.Errorf("blocked takes at most %d commands of at most %d characters, one line each", maxBlocked, maxBlockedLen)
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

// NewPart is one folder of a project being made: dir already an absolute, cleaned path that was
// verified to be a folder on MachineID.
type NewPart struct {
	Name, MachineID, Path string
}

// AtPart is an error about one folder of a project being made; Index 0 is its own folder.
type AtPart struct {
	Index int
	Err   error
}

func (e *AtPart) Error() string { return e.Err.Error() }
func (e *AtPart) Unwrap() error { return e.Err }

// CreateWithParts makes a project from its parts, parts[0] being its own folder: unnamed, the
// first is "Main" and the others take their folder's name. Nothing is made when one part cannot
// be, and the error is an *AtPart. A folder already in a project: with one part, that project is
// returned (created=false), as Create does; with more, it is an *InProjectError.
func (s *Store) CreateWithParts(name string, parts []NewPart) (Project, bool, error) {
	if len(parts) == 0 {
		return Project{}, false, ErrPartNotFound
	}
	if len(parts) > MaxParts {
		return Project{}, false, &AtPart{Index: MaxParts, Err: ErrTooManyParts}
	}
	if name != "" {
		var err error
		if name, err = CleanName(name); err != nil {
			return Project{}, false, err
		}
	} else {
		name = truncName(path.Base(parts[0].Path))
		if parts[0].Path == "/" {
			name = "/"
		}
	}
	list := make([]Part, len(parts))
	for i, np := range parts {
		pn := strings.TrimSpace(np.Name)
		switch {
		case pn != "":
		case i == 0:
			pn = "Main"
		default:
			pn = truncName(path.Base(np.Path))
		}
		pn, err := CleanName(pn)
		if err != nil {
			return Project{}, false, &AtPart{Index: i, Err: err}
		}
		id := MainPart
		if i > 0 {
			id = randomID("pt_")
		}
		for _, prev := range list[:i] {
			if strings.EqualFold(prev.Name, pn) {
				return Project{}, false, &AtPart{Index: i, Err: ErrPartName}
			}
			if prev.MachineID == np.MachineID && overlaps(prev.Path, np.Path) {
				return Project{}, false, &AtPart{Index: i, Err: ErrPartOverlap}
			}
		}
		list[i] = Part{ID: id, Name: pn, MachineID: np.MachineID, Path: np.Path}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, pt := range list {
		if x := s.holderLocked(pt.MachineID, pt.Path, ""); x != nil {
			if len(list) == 1 {
				return x.clone(), false, nil
			}
			return Project{}, false, &AtPart{Index: i, Err: &InProjectError{ProjectID: x.ID, Name: x.Name}}
		}
	}
	now := time.Now().UTC()
	np := &Project{ID: randomID("p_"), Name: name, CreatedAt: now, LastOpenedAt: now, Parts: list}
	np.normParts()
	np.Workspaces = []Workspace{{ID: randomID("w_"), ProjectID: np.ID, Name: "Main", CreatedAt: now}}
	s.list = append(s.list, np)
	if err := s.saveLocked(); err != nil {
		s.list = s.list[:len(s.list)-1]
		return Project{}, false, err
	}
	return np.clone(), true, nil
}

// PartPatch changes a part; nil fields stay.
type PartPatch struct {
	Name     *string
	ReadOnly *bool
	Blocked  *[]string
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
	if in.Blocked != nil {
		list, err := cleanBlocked(*in.Blocked)
		if err != nil {
			return Project{}, err
		}
		pt.Blocked = list
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
