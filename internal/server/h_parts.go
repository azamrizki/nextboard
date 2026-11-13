package server

import (
	"context"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
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
	case errors.Is(err, project.ErrMainPart):
		writeErrCode(w, http.StatusConflict, "main_part", err.Error())
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
	if _, err := project.CleanName(req.Name); err != nil {
		projectErr(w, err)
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

// handlePartDelete takes a part out of a project. Its folder is not touched; terminals in it
// must be closed first, since they would work in a folder the project no longer has.
func (s *Server) handlePartDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	pid, partID := r.PathValue("id"), r.PathValue("part")
	if pr, ok := s.projects.Get(pid); ok && len(pr.Parts) > 0 && pr.Parts[0].ID == partID {
		partErr(w, project.ErrMainPart)
		return
	}
	n := 0
	for _, pl := range s.sessions.AllPlacements() {
		if pl.ProjectID == pid && pl.PartID == partID && partID != "" {
			n++
		}
	}
	if n > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "close the terminals in that part first", "code": "part_busy", "sessions": n})
		return
	}
	before, _ := s.projects.Get(pid)
	pr, _, err := s.projects.DeletePart(pid, partID)
	if err != nil {
		partErr(w, err)
		return
	}
	s.review.Forget(reviewKey(before, partID))
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusOK, s.projectView(pr))
}

// placeInPart fills in the machine and folder of a new session from its part, or finds the part
// from them. An empty folder on a machine of a part is that part's folder.
func placeInPart(proj project.Project, cr *session.CreateRequest) error {
	if cr.PartID != "" {
		pt, ok := proj.Part(cr.PartID)
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
		cr.MachineID = proj.MachineID
	}
	if strings.TrimSpace(cr.Cwd) == "" {
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
func (s *Server) projectRef(ref, part string) (project.Project, string, error) {
	id, inRef, cut := strings.Cut(ref, "~")
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
	pt, ok := p.Part(part)
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

// partReadOnly is session.Manager.PartReadOnly.
func (s *Server) partReadOnly(projectID, partID string) bool {
	pr, ok := s.projects.Get(projectID)
	if !ok {
		return false
	}
	pt, ok := pr.Part(partID)
	return ok && pt.ReadOnly
}

// reviewKey is what review knows a part by (review is per part, from its own folder): the project
// id for the first part, as before parts, and "<project>~<part>" for another.
func reviewKey(pr project.Project, partID string) string {
	if partID == "" || len(pr.Parts) == 0 || pr.Parts[0].ID == partID {
		return pr.ID
	}
	return pr.ID + "~" + partID
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
// part's own.
func (s *Server) blockedIn(projectID, partID string) []string {
	var out []string
	if s.orch != nil {
		out = s.orch.BlockedCommands(projectID)
	}
	if pr, ok := s.projects.Get(projectID); ok {
		if pt, ok := pr.Part(partID); ok {
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
	for _, pr := range s.projects.List() {
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
