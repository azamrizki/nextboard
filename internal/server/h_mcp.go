package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/azamrizki/nextboard/internal/mcpreg"
)

// routesMCP serves the registry. A server can name any command, so changing or
// trying one is for the admin only.
func (s *Server) routesMCP(authed, admin func(string, handler)) {
	authed("GET /api/mcp/servers", s.handleMCPList)
	admin("POST /api/mcp/servers", s.handleMCPCreate)
	admin("PUT /api/mcp/servers/{id}", s.handleMCPUpdate)
	admin("DELETE /api/mcp/servers/{id}", s.handleMCPDelete)
	admin("POST /api/mcp/servers/{id}/test", s.handleMCPTest)
}

func (s *Server) mcpErr(w http.ResponseWriter, err error) {
	var bad *mcpreg.BadError
	switch {
	case errors.As(err, &bad):
		writeErrCode(w, http.StatusBadRequest, "bad_mcp", bad.Msg)
	case errors.Is(err, mcpreg.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "mcp_not_found", err.Error())
	case errors.Is(err, mcpreg.ErrExists):
		writeErrCode(w, http.StatusConflict, "mcp_exists", err.Error())
	case errors.Is(err, mcpreg.ErrTooMany):
		writeErrCode(w, http.StatusConflict, "too_many_mcp", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleMCPList(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.mcp.List()})
}

func (s *Server) handleMCPCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req mcpreg.Server
	if !decode(w, r, &req) {
		return
	}
	sv, err := s.mcp.Create(req)
	if err != nil {
		s.mcpErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sv)
}

func (s *Server) handleMCPUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req mcpreg.Server
	if !decode(w, r, &req) {
		return
	}
	sv, err := s.mcp.Update(r.PathValue("id"), req)
	if err != nil {
		s.mcpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sv)
}

func (s *Server) handleMCPDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.mcp.Delete(r.PathValue("id")); err != nil {
		s.mcpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleMCPTest(w http.ResponseWriter, r *http.Request, p *principal) {
	res, err := s.mcp.Probe(context.WithoutCancel(r.Context()), r.PathValue("id"))
	if err != nil {
		s.mcpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
