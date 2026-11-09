package server

import (
	"context"
	"errors"
	"net/http"
	"path"

	"github.com/azamrizki/nextboard/internal/studio"
)

// Laravel projects in the Code card (docs/API-studio.md §7.14).

func (s *Server) routesLaravel(authed func(string, handler)) {
	authed("GET /api/projects/{id}/laravel", s.handleLaravel)
	authed("GET /api/projects/{id}/laravel/log", s.handleLaravelLog)
	authed("POST /api/projects/{id}/laravel/routes", s.handleLaravelRoutes)
}

func (s *Server) handleLaravel(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	idx, err := studio.ScanLaravel(r.Context(), h, pr.Path)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, idx)
}

// laravelRoot finds the app within the project.
func (s *Server) laravelRoot(w http.ResponseWriter, r *http.Request, ctx context.Context) (string, *studio.LaravelIndex, bool) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return "", nil, false
	}
	idx, err := studio.ScanLaravel(ctx, h, pr.Path)
	if err != nil {
		fsErr(w, err)
		return "", nil, false
	}
	if !idx.Detected {
		writeErrCode(w, http.StatusConflict, "not_laravel", "this project is not a Laravel application")
		return "", nil, false
	}
	return path.Join(pr.Path, idx.Root), idx, true
}

func (s *Server) handleLaravelLog(w http.ResponseWriter, r *http.Request, p *principal) {
	root, _, ok := s.laravelRoot(w, r, r.Context())
	if !ok {
		return
	}
	_, h, _ := s.projectHost(w, r)
	log, err := studio.ReadLaravelLog(r.Context(), h, root)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, log)
}

// handleLaravelRoutes runs artisan, which boots the application: POST, because it is an action,
// and nothing in the web calls it without a click.
func (s *Server) handleLaravelRoutes(w http.ResponseWriter, r *http.Request, p *principal) {
	root, _, ok := s.laravelRoot(w, r, r.Context())
	if !ok {
		return
	}
	_, h, _ := s.projectHost(w, r)
	items, err := studio.ArtisanRoutes(r.Context(), h, root)
	if err != nil {
		if errors.Is(err, studio.ErrNoPHP) {
			writeErrCode(w, http.StatusNotImplemented, "no_php", err.Error())
			return
		}
		writeErrCode(w, http.StatusBadGateway, "artisan_failed", err.Error())
		return
	}
	if items == nil {
		items = []studio.LaravelFullRoute{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
