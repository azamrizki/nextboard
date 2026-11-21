package server

// NextCode's skills and memory (docs/API-agents.md §30, Clean NextCode): Settings › NextCode
// keeps them; every NextCode console is told of them when its CLI starts.

import (
	"errors"
	"net/http"

	"github.com/azamrizki/nextboard/internal/nextcode/kit"
)

func (s *Server) nextcodeKitRoutes(authed func(string, handler)) {
	authed("GET /api/nextcode/skills", s.handleNCSkills)
	authed("GET /api/nextcode/skills/{name}", s.handleNCSkill)
	authed("PUT /api/nextcode/skills/{name}", s.handleNCSkillPut)
	authed("DELETE /api/nextcode/skills/{name}", s.handleNCSkillDelete)
	authed("GET /api/nextcode/memory", s.handleNCMemories)
	authed("GET /api/nextcode/memory/{scope}", s.handleNCMemory)
	authed("PUT /api/nextcode/memory/{scope}", s.handleNCMemoryPut)
}

func kitErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, kit.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "skill_not_found", err.Error())
	case errors.Is(err, kit.ErrTooLarge):
		writeErrCode(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, kit.ErrTooMany):
		writeErrCode(w, http.StatusConflict, "too_many_skills", err.Error())
	default:
		writeErrCode(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

func (s *Server) handleNCSkills(w http.ResponseWriter, r *http.Request, p *principal) {
	list := s.ncKit.Skills()
	if list == nil {
		list = []kit.Skill{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "dir": s.ncKit.SkillsDir()})
}

func (s *Server) handleNCSkill(w http.ResponseWriter, r *http.Request, p *principal) {
	sk, err := s.ncKit.Skill(r.PathValue("name"))
	if err != nil {
		kitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sk)
}

func (s *Server) handleNCSkillPut(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Description string `json:"description"`
		Body        string `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	sk, err := s.ncKit.PutSkill(r.PathValue("name"), req.Description, req.Body)
	if err != nil {
		kitErr(w, err)
		return
	}
	s.events.Publish(map[string]any{"type": "nextcodeSkills"})
	writeJSON(w, http.StatusOK, sk)
}

func (s *Server) handleNCSkillDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.ncKit.DeleteSkill(r.PathValue("name")); err != nil {
		kitErr(w, err)
		return
	}
	s.events.Publish(map[string]any{"type": "nextcodeSkills"})
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleNCMemories(w http.ResponseWriter, r *http.Request, p *principal) {
	list := s.ncKit.Memories()
	if list == nil {
		list = []kit.MemoryScope{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "dir": s.ncKit.MemoryDir()})
}

func (s *Server) handleNCMemory(w http.ResponseWriter, r *http.Request, p *principal) {
	scope := r.PathValue("scope")
	text, err := s.ncKit.Memory(scope)
	if err != nil {
		kitErr(w, err)
		return
	}
	path, _ := s.ncKit.MemoryPath(scope)
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "text": text, "path": path})
}

func (s *Server) handleNCMemoryPut(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	scope := r.PathValue("scope")
	if err := s.ncKit.PutMemory(scope, req.Text); err != nil {
		kitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "text": req.Text})
}
