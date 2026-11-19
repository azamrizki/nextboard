package server

import "net/http"

// Project badges (docs/API-board.md §1). Like the groups, every client holds the
// whole list, so each change publishes all of it.

func (s *Server) emitProjectBadges() {
	s.events.Publish(map[string]any{"type": "projectBadges", "badges": s.projects.Badges()})
}

func (s *Server) handleProjectBadges(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.projects.Badges())
}

func (s *Server) handleProjectBadgeCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
		Icon  string `json:"icon"`
	}
	if !decode(w, r, &req) {
		return
	}
	b, err := s.projects.CreateBadge(req.Name, req.Color, req.Icon)
	if err != nil {
		projectErr(w, err)
		return
	}
	s.emitProjectBadges()
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) handleProjectBadgeUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
		Icon  *string `json:"icon"`
	}
	if !decode(w, r, &req) {
		return
	}
	b, err := s.projects.UpdateBadge(r.PathValue("id"), req.Name, req.Color, req.Icon)
	if err != nil {
		projectErr(w, err)
		return
	}
	s.emitProjectBadges()
	writeJSON(w, http.StatusOK, b)
}

// handleProjectBadgeDelete removes the badge and takes it off its projects, which
// get a project event each.
func (s *Server) handleProjectBadgeDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	cleared, err := s.projects.DeleteBadge(r.PathValue("id"))
	if err != nil {
		projectErr(w, err)
		return
	}
	s.emitProjectBadges()
	for _, id := range cleared {
		s.emitProject(id)
	}
	writeJSON(w, http.StatusOK, okBody)
}
