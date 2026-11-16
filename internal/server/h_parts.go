package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/uptime"
)

// Parts of a project (docs/API-board.md §1, ROADMAP #49): the folders on other machines that one
// project spans, each with a name for its role.

func partErr(w http.ResponseWriter, err error) {
	var in *project.InProjectError
	switch {
	case errors.As(err, &in):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "path_in_project", "projectId": in.ProjectID})
	case errors.Is(err, project.ErrPartNotFound):
		writeErrCode(w, http.StatusNotFound, "part_not_found", err.Error())
	case errors.Is(err, project.ErrPartOverlap):
		writeErrCode(w, http.StatusConflict, "part_overlap", err.Error())
	case errors.Is(err, project.ErrPartName):
		writeErrCode(w, http.StatusConflict, "name_conflict", err.Error())
	case errors.Is(err, project.ErrTooManyParts):
		writeErrCode(w, http.StatusConflict, "too_many_parts", err.Error())
	case errors.Is(err, project.ErrLastPart):
		writeErrCode(w, http.StatusConflict, "last_part", err.Error())
	case errors.Is(err, project.ErrBadPartName):
		writeErrCode(w, http.StatusBadRequest, "bad_part_name", err.Error())
	case errors.Is(err, project.ErrBadPlace):
		writeErrCode(w, http.StatusBadRequest, "bad_placement", err.Error())
	case errors.Is(err, project.ErrBadBlocked):
		writeErrCode(w, http.StatusBadRequest, "bad_blocked", err.Error())
	default:
		projectErr(w, err)
	}
}

// statFolder resolves path to a folder on machineID, as adding a project does.
func (s *Server) statFolder(w http.ResponseWriter, r *http.Request, machineID, dir string) (string, bool) {
	h, ok := s.machines.Host(machineID)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return "", false
	}
	if strings.TrimSpace(dir) == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return "", false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	st, err := h.Stat(ctx, strings.TrimSpace(dir))
	if err != nil {
		fsErr(w, err)
		return "", false
	}
	if !st.Exists || !st.Dir {
		writeErr(w, http.StatusBadRequest, "path is not a folder: "+st.Path)
		return "", false
	}
	return st.Path, true
}

// createWithParts makes a project from its parts (POST /api/projects with "partName" or "parts"):
// every folder is checked first, and nothing is made when one is wrong. An error about one folder
// says which with "part", its index in the list. One part whose folder is already in a project
// opens that project, as a plain add does.
func (s *Server) createWithParts(w http.ResponseWriter, r *http.Request, name string, parts []project.NewPart) {
	if len(parts) > project.MaxParts {
		atPart(w, project.MaxParts, func(w http.ResponseWriter) { partErr(w, project.ErrTooManyParts) })
		return
	}
	if name != "" {
		if _, err := project.CleanName(name); err != nil {
			projectErr(w, err)
			return
		}
	}
	for i := range parts {
		if pn := strings.TrimSpace(parts[i].Name); pn != "" {
			if _, err := project.CleanPartName(pn); err != nil {
				atPart(w, i, func(w http.ResponseWriter) { partErr(w, err) })
				return
			}
		}
		ok := false
		atPart(w, i, func(w http.ResponseWriter) { parts[i].Path, ok = s.statFolder(w, r, parts[i].MachineID, parts[i].Path) })
		if !ok {
			return
		}
	}
	pr, created, err := s.projects.CreateWithParts(name, parts)
	var at *project.AtPart
	if errors.As(err, &at) {
		atPart(w, at.Index, func(w http.ResponseWriter) { partErr(w, at.Err) })
		return
	}
	if err != nil {
		partErr(w, err)
		return
	}
	if !created {
		writeJSON(w, http.StatusOK, s.projectView(pr))
		return
	}
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusCreated, s.projectView(pr))
}

// atPart runs answer and, when it answered with an error, adds "part": i to that error's body.
func atPart(w http.ResponseWriter, i int, answer func(http.ResponseWriter)) {
	rec := &errAnswer{header: http.Header{}}
	answer(rec)
	if rec.status == 0 && rec.body.Len() == 0 {
		return
	}
	body := map[string]any{}
	if json.Unmarshal(rec.body.Bytes(), &body) != nil {
		body = map[string]any{"error": strings.TrimSpace(rec.body.String())}
	}
	body["part"] = i
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	writeJSON(w, rec.status, body)
}

// errAnswer keeps an answer for atPart to add to.
type errAnswer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (a *errAnswer) Header() http.Header         { return a.header }
func (a *errAnswer) Write(b []byte) (int, error) { return a.body.Write(b) }
func (a *errAnswer) WriteHeader(status int)      { a.status = status }

func (s *Server) handlePartCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name      string `json:"name"`
		MachineID string `json:"machineId"`
		Path      string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.MachineID == "" {
		req.MachineID = machine.LocalID
	}
	if _, ok := s.projects.Get(r.PathValue("id")); !ok {
		writeErr(w, http.StatusNotFound, project.ErrNotFound.Error())
		return
	}
	if _, err := project.CleanPartName(req.Name); err != nil {
		partErr(w, err)
		return
	}
	dir, ok := s.statFolder(w, r, req.MachineID, req.Path)
	if !ok {
		return
	}
	pr, _, err := s.projects.AddPart(r.PathValue("id"), req.Name, req.MachineID, dir)
	if err != nil {
		partErr(w, err)
		return
	}
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusCreated, s.projectView(pr))
}

func (s *Server) handlePartUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name     *string   `json:"name"`
		ReadOnly *bool     `json:"readOnly"`
		Blocked  *[]string `json:"blocked"`
		Index    *int      `json:"index"`
	}
	if !decode(w, r, &req) {
		return
	}
	pr, err := s.projects.PatchPart(r.PathValue("id"), r.PathValue("part"), project.PartPatch{Name: req.Name, ReadOnly: req.ReadOnly, Blocked: req.Blocked, Index: req.Index})
	if err != nil {
		partErr(w, err)
		return
	}
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusOK, s.projectView(pr))
}

// handlePartDelete takes a part out of a project: any part, the first one too, while another is
// left (409 last_part). Its folder is not touched; terminals in it must be closed first, since
// they would work in a folder the project no longer has. A root console does not hold a part.
func (s *Server) handlePartDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	pid, partID := r.PathValue("id"), r.PathValue("part")
	if lastPart(w, s.projects, pid, partID) || partBusy(w, s.sessions.AllPlacements(), pid, partID) {
		return
	}
	before, _ := s.projects.Get(pid)
	pr, _, err := s.projects.DeletePart(pid, partID)
	if err != nil {
		partErr(w, err)
		return
	}
	s.review.Forget(reviewKey(before, partID))
	s.unlabelMonitors(pid, partID)
	if s.orch != nil {
		s.orch.PartRemoved(before, partID) // its memory stays, as an ordinary page
	}
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusOK, s.projectView(pr))
}

// lastPart answers 409 last_part for a project's one part, before part_busy: closing its
// terminals would not help.
func lastPart(w http.ResponseWriter, projects *project.Store, pid, partID string) bool {
	if pr, ok := projects.Get(pid); ok && len(pr.Parts) == 1 && pr.Parts[0].ID == partID {
		partErr(w, project.ErrLastPart)
		return true
	}
	return false
}

// partBusy answers 409 part_busy when consoles still work in the part: those placed in it,
// counting the ones from before part ids, which the start-up migration stamped. A root console
// (partId "", root) is not in the part.
func partBusy(w http.ResponseWriter, all []session.Placement, pid, partID string) bool {
	n := 0
	for _, pl := range all {
		if pl.ProjectID == pid && pl.PartID == partID && partID != "" {
			n++
		}
	}
	if n > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "close the terminals in that part first", "code": "part_busy", "sessions": n})
	}
	return n > 0
}

// unlabelMonitors takes a part the project no longer has off its uptime monitors: they then
// watch the project as a whole (the part is a label only).
func (s *Server) unlabelMonitors(pid, partID string) {
	if s.uptime == nil || partID == "" {
		return
	}
	for _, m := range s.uptime.List(pid) {
		if m.PartID != partID {
			continue
		}
		if _, err := s.uptime.Update(m.ID, func(m *uptime.Monitor) error { m.PartID = ""; return nil }); err != nil {
			log.Printf("uptime: monitor %s keeps the removed part %s: %v", m.ID, partID, err)
		}
	}
}

// placeInPart fills in the machine and folder of a new session from its part, or finds the part
// from them. An empty folder on a machine of a part is that part's folder.
func placeInPart(proj project.Project, cr *session.CreateRequest) error {
	if cr.PartID != "" {
		pt, ok := proj.PartByID(cr.PartID)
		if !ok {
			return project.ErrPartNotFound
		}
		if cr.MachineID == "" {
			cr.MachineID = pt.MachineID
		}
		if cr.MachineID != pt.MachineID {
			return errPartMachine
		}
		if strings.TrimSpace(cr.Cwd) == "" {
			cr.Cwd = pt.Path
		}
		return nil
	}
	if cr.MachineID == "" {
		// compat: a client that names neither a part nor a machine gets the first part's machine
		cr.MachineID = proj.DefaultPart().MachineID
	}
	if strings.TrimSpace(cr.Cwd) == "" {
		// the first part on that machine in display order
		for _, pt := range proj.Parts {
			if pt.MachineID == cr.MachineID {
				cr.Cwd, cr.PartID = pt.Path, pt.ID
				return nil
			}
		}
		return nil
	}
	if c := strings.TrimSpace(cr.Cwd); strings.HasPrefix(c, "/") {
		if pt, ok := proj.PartAt(cr.MachineID, path.Clean(c)); ok {
			cr.PartID = pt.ID
		}
	}
	return nil
}

var errPartMachine = errors.New("that machine is not the machine of the part")

// projectRef resolves a project id, or "<project>~<part>" for one of its parts, and an optional
// part named apart (?part=): the project seen through that part (its machine and folder), and the
// part's id ("" when none was named). Project-scoped routes take either form (docs/API-board.md §1).
// A ref without a part is the project as old clients see it: machineId and path of its
// DefaultPart (compat; the web names a part on every workbench call).
func (s *Server) projectRef(ref, part string) (project.Project, string, error) {
	id, inRef, cut := strings.Cut(ref, project.PartKeySep)
	if cut {
		part = inRef
	}
	p, ok := s.projects.Get(id)
	if !ok {
		return p, "", project.ErrNotFound
	}
	if part == "" {
		return p, "", nil
	}
	pt, ok := p.PartByID(part)
	if !ok {
		return p, "", project.ErrPartNotFound
	}
	p.MachineID, p.Path = pt.MachineID, pt.Path
	return p, pt.ID, nil
}

// partFolder is the folder of the project's part on machineID that holds p, else the first part
// on that machine; false when the project has no part there.
func partFolder(pr project.Project, machineID, p string) (string, bool) {
	if strings.HasPrefix(p, "/") {
		if pt, ok := pr.PartAt(machineID, path.Clean(p)); ok {
			return pt.Path, true
		}
	}
	for _, pt := range pr.Parts {
		if pt.MachineID == machineID {
			return pt.Path, true
		}
	}
	return "", false
}

// partReadOnly is session.Manager.PartReadOnly. "" is no part (a root console, or one outside
// every part's folder): never read-only.
func (s *Server) partReadOnly(projectID, partID string) bool {
	pr, ok := s.projects.Get(projectID)
	if !ok {
		return false
	}
	pt, ok := pr.PartByID(partID)
	return ok && pt.ReadOnly
}

// reviewKey is what review knows a part by (review is per part, from its own folder): its part
// key "<project>~<part>" (project.PartKey), the same for every part, so moving or removing the
// first one moves no history. "" is the project's DefaultPart (a ref or a record that names no
// part, as before part ids were stamped).
func reviewKey(pr project.Project, partID string) string {
	return pr.KeyOf(partID)
}

// reviewKeyOf is reviewKey for a session's project and part.
func (s *Server) reviewKeyOf(projectID, partID string) string {
	pr, ok := s.projects.Get(projectID)
	if !ok {
		return projectID
	}
	return reviewKey(pr, partID)
}

// reviewKeyAt is the review of the part a file is in: the one a project ref names, else the part
// of the project on that machine whose folder holds it.
func (s *Server) reviewKeyAt(ref, machineID, p string) string {
	pr, part, err := s.projectRef(ref, "")
	if err != nil {
		return ref
	}
	if part == "" && strings.HasPrefix(p, "/") {
		if pt, ok := pr.PartAt(machineID, path.Clean(p)); ok {
			part = pt.ID
		}
	}
	return reviewKey(pr, part)
}

// blockedIn is what needs the owner's approval in a part: the hub's list, the project's, and the
// part's own. partID "" (no part) gets the hub's and the project's lists only.
func (s *Server) blockedIn(projectID, partID string) []string {
	var out []string
	if s.orch != nil {
		out = s.orch.BlockedCommands(projectID)
	}
	if pr, ok := s.projects.Get(projectID); ok {
		if pt, ok := pr.PartByID(partID); ok {
			for _, c := range pt.Blocked {
				if !slices.Contains(out, c) {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// allBlocked is every list, for a CLI that keeps its rules in a config folder all projects share.
func (s *Server) allBlocked() []string {
	var out []string
	if s.orch != nil {
		out = s.orch.AllBlocked()
	}
	for _, pr := range s.projects.ListAll() {
		for _, pt := range pr.Parts {
			for _, c := range pt.Blocked {
				if !slices.Contains(out, c) {
					out = append(out, c)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}
