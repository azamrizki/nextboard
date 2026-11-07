package server

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"time"

	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/profiles"
	"github.com/azamrizki/nextboard/internal/skills"
)

// ---- Skills manager (§9, owner decision #3) ----

const skillsTimeout = 60 * time.Second

// skillLoc resolves the machine, its Claude config folder (profileId →
// the default Claude profile of the machine → ~/.claude) and the project
// repo (projectId, must be on the same machine).
func (s *Server) skillLoc(ctx context.Context, w http.ResponseWriter, machineID, projectID, profileID string) (machine.Host, skills.Loc, bool) {
	h, ok := s.machines.Host(machineID)
	if !ok {
		writeErrCode(w, http.StatusNotFound, "machine_not_found", machine.ErrNotFound.Error())
		return nil, skills.Loc{}, false
	}
	cfg, err := s.claudeCfg(ctx, h, profileID)
	if errors.Is(err, profiles.ErrNotFound) {
		writeErrCode(w, http.StatusNotFound, "profile_not_found", err.Error())
		return nil, skills.Loc{}, false
	}
	if err != nil {
		fsErr(w, err)
		return nil, skills.Loc{}, false
	}
	loc := skills.Loc{MachineID: h.ID(), MachineName: h.Info().Name, Remote: h.ID() != machine.LocalID, Cfg: cfg}
	if projectID != "" {
		pr, ok := s.projects.Get(projectID)
		if !ok || pr.MachineID != h.ID() {
			writeErrCode(w, http.StatusNotFound, "project_not_found", "project not found on this machine")
			return nil, skills.Loc{}, false
		}
		loc.Repo = pr.Path
		if st, err := h.Stat(ctx, pr.Path); err == nil {
			loc.Repo = st.Path
		}
	}
	return h, loc, true
}

func skillErr(w http.ResponseWriter, err error) {
	var ce *skills.ConfirmError
	switch {
	case errors.As(err, &ce):
		writeConfirm(w, ce)
	case errors.Is(err, skills.ErrReadOnly):
		writeErrCode(w, http.StatusForbidden, "read_only", err.Error())
	case errors.Is(err, skills.ErrNotSkill):
		writeErrCode(w, http.StatusBadRequest, "not_skill", err.Error())
	case errors.Is(err, skills.ErrBadName):
		writeErrCode(w, http.StatusBadRequest, "bad_name", err.Error())
	case errors.Is(err, skills.ErrBadScope):
		writeErrCode(w, http.StatusBadRequest, "bad_scope", err.Error())
	case errors.Is(err, skills.ErrExists):
		writeErrCode(w, http.StatusConflict, "exists", err.Error())
	case errors.Is(err, skills.ErrTooLarge), errors.Is(err, skills.ErrContent):
		writeErrCode(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, skills.ErrNoSkillFile):
		writeErrCode(w, http.StatusBadRequest, "no_skill_file", err.Error())
	case errors.Is(err, machine.ErrMtimeMismatch):
		writeErrCode(w, http.StatusConflict, "mtime_mismatch", err.Error())
	case errors.Is(err, machine.ErrNoSFTP):
		writeErrCode(w, http.StatusNotImplemented, "no_sftp", err.Error())
	case errors.Is(err, fs.ErrNotExist):
		writeErrCode(w, http.StatusNotFound, "not_found", "skill not found")
	default:
		fsErr(w, err)
	}
}

func (s *Server) skillsChanged(machineID string) {
	s.events.Publish(map[string]any{"type": "skills", "machineId": machineID})
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, cancel := context.WithTimeout(r.Context(), skillsTimeout)
	defer cancel()
	q := r.URL.Query()
	h, loc, ok := s.skillLoc(ctx, w, r.PathValue("id"), q.Get("projectId"), q.Get("profileId"))
	if !ok {
		return
	}
	items, truncated, err := s.studio.skills.List(ctx, h, loc)
	if err != nil {
		skillErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated, "cfg": loc.Cfg})
}

func (s *Server) handleSkillItem(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, cancel := context.WithTimeout(r.Context(), skillsTimeout)
	defer cancel()
	q := r.URL.Query()
	h, loc, ok := s.skillLoc(ctx, w, r.PathValue("id"), q.Get("projectId"), q.Get("profileId"))
	if !ok {
		return
	}
	sk, content, err := s.studio.skills.Item(ctx, h, loc, q.Get("path"))
	if err != nil {
		skillErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill": sk, "content": content})
}

func (s *Server) handleSkillLint(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Content string `json:"content"`
		Path    string `json:"path"`
		Kind    string `json:"kind"`
	}
	if !decode(w, r, &req) {
		return
	}
	kind, name := req.Kind, ""
	if req.Path != "" {
		if base := pathBase(req.Path); base == "SKILL.md" {
			kind, name = skills.KindSkill, pathBase(pathDir(req.Path))
		} else {
			kind, name = skills.KindCommand, trimExt(base)
		}
	}
	if kind == "" {
		kind = skills.KindSkill
	}
	writeJSON(w, http.StatusOK, map[string]any{"lint": skills.Lint(req.Content, kind, name)})
}

type skillWriteBody struct {
	Path      string       `json:"path"`
	Content   string       `json:"content"`
	IfMtime   *string      `json:"ifMtime"`
	ProjectID string       `json:"projectId"`
	ProfileID string       `json:"profileId"`
	Confirm   *confirmBody `json:"confirm"`
}

func (c *confirmBody) list() []string {
	if c == nil {
		return nil
	}
	return c.Paths
}

func (s *Server) handleSkillWrite(w http.ResponseWriter, r *http.Request, p *principal) {
	var req skillWriteBody
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), skillsTimeout)
	defer cancel()
	h, loc, ok := s.skillLoc(ctx, w, r.PathValue("id"), req.ProjectID, req.ProfileID)
	if !ok {
		return
	}
	var ifm *time.Time
	if req.IfMtime != nil && *req.IfMtime != "" {
		t, err := time.Parse(time.RFC3339Nano, *req.IfMtime)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "ifMtime must be RFC 3339")
			return
		}
		ifm = &t
	}
	sk, err := s.studio.skills.Write(ctx, h, loc, req.Path, req.Content, ifm, req.Confirm.list())
	if err != nil {
		skillErr(w, err)
		return
	}
	s.skillsChanged(h.ID())
	writeJSON(w, http.StatusOK, sk)
}

func (s *Server) handleSkillCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		skills.CreateRequest
		ProjectID string       `json:"projectId"`
		ProfileID string       `json:"profileId"`
		Confirm   *confirmBody `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), skillsTimeout)
	defer cancel()
	h, loc, ok := s.skillLoc(ctx, w, r.PathValue("id"), req.ProjectID, req.ProfileID)
	if !ok {
		return
	}
	sk, err := s.studio.skills.Create(ctx, h, loc, req.CreateRequest, req.Confirm.list())
	if err != nil {
		skillErr(w, err)
		return
	}
	s.skillsChanged(h.ID())
	writeJSON(w, http.StatusCreated, sk)
}

func (s *Server) handleSkillImport(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		FromMachineID string       `json:"fromMachineId"`
		FromPath      string       `json:"fromPath"`
		Scope         string       `json:"scope"`
		ProjectID     string       `json:"projectId"`
		ProfileID     string       `json:"profileId"`
		Confirm       *confirmBody `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*skillsTimeout)
	defer cancel()
	h, loc, ok := s.skillLoc(ctx, w, r.PathValue("id"), req.ProjectID, req.ProfileID)
	if !ok {
		return
	}
	if req.FromMachineID == "" {
		req.FromMachineID = h.ID()
	}
	src, ok := s.machines.Host(req.FromMachineID)
	if !ok {
		writeErrCode(w, http.StatusNotFound, "machine_not_found", machine.ErrNotFound.Error())
		return
	}
	if req.FromPath == "" || s.hubPrivate(w, src, req.FromPath) {
		if req.FromPath == "" {
			writeErr(w, http.StatusBadRequest, "fromPath is required")
		}
		return
	}
	sk, err := s.studio.skills.Import(ctx, src, req.FromPath, h, loc, req.Scope, req.Confirm.list())
	if err != nil {
		skillErr(w, err)
		return
	}
	s.skillsChanged(h.ID())
	writeJSON(w, http.StatusCreated, sk)
}

func (s *Server) handleSkillDuplicate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Path      string       `json:"path"`
		ToScope   string       `json:"toScope"`
		ProjectID string       `json:"projectId"`
		ProfileID string       `json:"profileId"`
		Confirm   *confirmBody `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*skillsTimeout)
	defer cancel()
	h, loc, ok := s.skillLoc(ctx, w, r.PathValue("id"), req.ProjectID, req.ProfileID)
	if !ok {
		return
	}
	sk, err := s.studio.skills.Duplicate(ctx, h, loc, req.Path, req.ToScope, req.Confirm.list())
	if err != nil {
		skillErr(w, err)
		return
	}
	s.skillsChanged(h.ID())
	writeJSON(w, http.StatusCreated, sk)
}

func (s *Server) handleSkillDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Path      string       `json:"path"`
		ProjectID string       `json:"projectId"`
		ProfileID string       `json:"profileId"`
		Confirm   *confirmBody `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), skillsTimeout)
	defer cancel()
	h, loc, ok := s.skillLoc(ctx, w, r.PathValue("id"), req.ProjectID, req.ProfileID)
	if !ok {
		return
	}
	backup, err := s.studio.skills.Delete(ctx, h, loc, req.Path, req.Confirm.list())
	if err != nil {
		skillErr(w, err)
		return
	}
	s.skillsChanged(h.ID())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "backup": backup})
}

func (s *Server) handleSkillStarters(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, skills.Starters())
}

// handleSessionSkill types "/name args" into an agent console through the
// delivery engine (guards apply; never into a shell).
func (s *Server) handleSessionSkill(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name string `json:"name"`
		Args string `json:"args"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !skillInvokeRe.MatchString(req.Name) {
		writeErrCode(w, http.StatusBadRequest, "bad_name", skills.ErrBadName.Error())
		return
	}
	text := "/" + req.Name
	if a := oneLineArgs(req.Args); a != "" {
		text += " " + a
	}
	d, err := s.deliver.Submit(r.PathValue("id"), delivery.Request{Text: text, Source: "skill"})
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}
