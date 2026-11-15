package project

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Part is one place of a project's code: a folder on a machine, named for its role ("Backend",
// "Frontend"), so one project can span the machines its pieces run on (ROADMAP #49). All parts
// are equal (v0.9.0-beta.2): any one may be renamed, moved or removed while another is left, and
// the order is only the display order. The project keeps its machineId and path in step with the
// first part, only so that a client that knows nothing of parts still sees the project as before.
// A part's name is also its folder's name at the project root (CleanPartName).
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
	// MainPart is the id the first part of a project had before v0.9.0-beta.2 (made before parts,
	// or with them). It is only in old data, and nothing treats it specially: new projects and
	// their parts all get "pt_…" ids, and old ids are never rewritten.
	MainPart = "main"
	// MaxParts is how many parts one project may have.
	MaxParts = 8
)

var (
	ErrPartNotFound  = errors.New("part not found")
	ErrPartOverlap   = errors.New("that folder is inside another part of this project, or holds one")
	ErrTooManyParts  = fmt.Errorf("a project has at most %d parts", MaxParts)
	ErrPartName      = errors.New("another part of this project has that name")
	ErrBadPartAccess = errors.New(`partAccess must be "" (read only on other machines) or "full"`)
	ErrBadBlocked    = fmt.Errorf("blocked takes at most %d commands of at most %d characters, one line each", maxBlocked, maxBlockedLen)
	// ErrLastPart: a project keeps at least one part (409 last_part). Every part is equal: any one
	// may be removed or split off while another is left.
	ErrLastPart = errors.New("a project keeps at least one part")
	// ErrBadPartName: a part's name is its folder's name at the project root, so it must be safe
	// as one path segment (400 bad_part_name). See CleanPartName.
	ErrBadPartName = fmt.Errorf(`a part's name is its folder name at the project root: 1 to %d characters, no "/" or "\", not "." or "..", not starting with ".", not AGENTS.md or CLAUDE.md`, MaxName)
)

// InProjectError: the folder is already another project, or a part of one.
type InProjectError struct{ ProjectID, Name string }

func (e *InProjectError) Error() string {
	return fmt.Sprintf("that folder is already in project %q", e.Name)
}

// PartByID returns the part with exactly this id. "" is no part and never matches.
func (p Project) PartByID(id string) (Part, bool) {
	if id == "" {
		return Part{}, false
	}
	for _, pt := range p.Parts {
		if pt.ID == id {
			return pt, true
		}
	}
	return Part{}, false
}

// PartNamed returns the part s names: its id, else its name in any case (the lead's steps, the
// typed grammar, saved layouts and task cards name parts so). Surrounding spaces are ignored;
// "" matches none. An id wins over another part's equal name.
func (p Project) PartNamed(s string) (Part, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Part{}, false
	}
	if pt, ok := p.PartByID(s); ok {
		return pt, true
	}
	for _, pt := range p.Parts {
		if strings.EqualFold(pt.Name, s) {
			return pt, true
		}
	}
	return Part{}, false
}

// DefaultPart is the compat default for a client or a stored record that names no part: the
// first part in display order (old clients' project.machineId/path, a project ref without a
// part, a monitor or session from before part ids were stamped). Nothing else about the first
// part is special; call it only where "no part named" must still mean one. The zero Part when
// the project has none, which a loaded project never is.
func (p Project) DefaultPart() Part {
	if len(p.Parts) == 0 {
		return Part{}
	}
	return p.Parts[0]
}

// partReserved are the names the hub writes at the top of a project's root folder.
var partReserved = []string{"AGENTS.md", "CLAUDE.md"}

// CleanPartName checks a part's name and returns it trimmed. It is CleanName's rule (1 to
// MaxName runes, no control characters) plus, since the name is the part's folder name at the
// project root (<data>/roots/<project>/<name>), safe as one path segment: no "/", "\" or NUL,
// not "." or "..", not starting with "." (the root's own .nextboard), and not AGENTS.md or
// CLAUDE.md in any case (the hub writes those there). Every failure is ErrBadPartName. Being
// unique in the project (any case, ErrPartName) is checked by the caller.
func CleanPartName(name string) (string, error) {
	name, err := CleanName(name)
	if err != nil || strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, ".") {
		return "", ErrBadPartName
	}
	for _, r := range partReserved {
		if strings.EqualFold(name, r) {
			return "", ErrBadPartName
		}
	}
	return name, nil
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

// normParts gives a project from before parts its one part (id MainPart, named after its folder),
// and keeps machineId and path in step with the first part for old clients.
func (p *Project) normParts() {
	if len(p.Parts) == 0 {
		p.Parts = []Part{{ID: MainPart, Name: partNameFrom(p.Path), MachineID: p.MachineID, Path: p.Path}}
	}
	p.MachineID, p.Path = p.Parts[0].MachineID, p.Parts[0].Path
}

// partNameFrom is the default name of a part in dir: its folder's name, made safe as a path
// segment (sanitizePartName); "Part" for "/".
func partNameFrom(dir string) string {
	base := path.Base(dir)
	if base == "/" || base == "." || base == "" {
		return "Part"
	}
	return sanitizePartName(base)
}

// sanitizePartName makes a name CleanPartName accepts: "/" and "\" become "-", control
// characters go, leading dots go ("." and ".." become "Part"), AGENTS.md and CLAUDE.md get
// " part" added, and it is cut to MaxName. Being unique is the caller's (uniqueName).
func sanitizePartName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return '-'
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(name), "."))
	if name == "" {
		name = "Part"
	}
	for _, r := range partReserved {
		if strings.EqualFold(name, r) {
			name += " part"
		}
	}
	if n, err := CleanPartName(truncName(name)); err == nil {
		return n
	}
	return "Part"
}

// PartRename is a part whose name the store changed when it opened data from before
// v0.9.0-beta.2: a name that is not safe as its folder's name at the project root (Unsafe: the
// hub tells the owner), or the "Main" of a project in one part, which takes its folder's name.
type PartRename struct {
	ProjectID, ProjectName, PartID, From, To string
	Unsafe                                   bool
}

// migratePartNames makes every part's name path-safe and unique in the project and, with single,
// renames the {main, "Main"} part of a project in one part after its folder (a project in several
// keeps "Main": the owner may have chosen it). Ids are never rewritten. It is idempotent.
func (p *Project) migratePartNames(single bool) []PartRename {
	var out []PartRename
	for i := range p.Parts {
		pt := &p.Parts[i]
		from, unsafe := pt.Name, false
		if _, err := CleanPartName(pt.Name); err != nil {
			pt.Name, unsafe = sanitizePartName(pt.Name), true
		} else if single && len(p.Parts) == 1 && pt.ID == MainPart && pt.Name == "Main" {
			pt.Name = partNameFrom(pt.Path)
		}
		taken := func(name string) bool {
			for k, o := range p.Parts {
				if k != i && strings.EqualFold(o.Name, name) {
					return true
				}
			}
			return false
		}
		if taken(pt.Name) {
			pt.Name, unsafe = uniqueName(pt.Name, taken), unsafe || pt.Name == from
		}
		if pt.Name != from {
			out = append(out, PartRename{ProjectID: p.ID, ProjectName: p.Name, PartID: pt.ID, From: from, To: pt.Name, Unsafe: unsafe})
		}
	}
	return out
}

// TakePartRenames returns, once, the parts Open renamed (PartRename), for the hub to tell the
// owner and log.
func (s *Store) TakePartRenames() []PartRename {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.renamed
	s.renamed = nil
	return out
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
	name, err := CleanPartName(name)
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

// AtPart is an error about one folder of a project being made; Index is its place in the list.
type AtPart struct {
	Index int
	Err   error
}

func (e *AtPart) Error() string { return e.Err.Error() }
func (e *AtPart) Unwrap() error { return e.Err }

// CreateWithParts makes a project from its parts, in display order (the project's machineId and
// path follow the first, for old clients). Every part gets a "pt_" id; an unnamed one takes its
// folder's name (partNameFrom), with a number when another part has it. Nothing is made when one
// part cannot be, and the error is an *AtPart. A folder already in a project: with one part, that
// project is returned (created=false), as Create does; with more, it is an *InProjectError.
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
		if pn != "" {
			var err error
			if pn, err = CleanPartName(pn); err != nil {
				return Project{}, false, &AtPart{Index: i, Err: err}
			}
			for _, prev := range list[:i] {
				if strings.EqualFold(prev.Name, pn) {
					return Project{}, false, &AtPart{Index: i, Err: ErrPartName}
				}
			}
		} else {
			// a default name never clashes: not with the parts before, nor with a name given later
			pn = uniqueName(partNameFrom(np.Path), func(n string) bool {
				for k, o := range parts {
					if (k < i && strings.EqualFold(list[k].Name, n)) || (k > i && strings.EqualFold(strings.TrimSpace(o.Name), n)) {
						return true
					}
				}
				return false
			})
		}
		for _, prev := range list[:i] {
			if prev.MachineID == np.MachineID && overlaps(prev.Path, np.Path) {
				return Project{}, false, &AtPart{Index: i, Err: ErrPartOverlap}
			}
		}
		list[i] = Part{ID: randomID("pt_"), Name: pn, MachineID: np.MachineID, Path: np.Path}
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
	// Index moves the part. It is display order only: no part's id, review or git moves with
	// it (the project's machineId and path follow index 0, for old clients).
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
		name, err := CleanPartName(*in.Name)
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

// DeletePart removes a part from a project; its folder is not touched. Any part may go while
// another is left (else ErrLastPart); when the first goes, the project's machineId and path
// follow the new first one.
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
		if len(p.Parts) == 1 {
			return Project{}, Part{}, ErrLastPart
		}
		old := *p
		p.Parts = append(p.Parts[:i:i], p.Parts[i+1:]...)
		p.normParts()
		if err := s.saveLocked(); err != nil {
			*p = old
			return Project{}, Part{}, err
		}
		return p.clone(), pt, nil
	}
	return Project{}, Part{}, ErrPartNotFound
}
