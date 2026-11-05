package server

import (
	"errors"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"nextboard/internal/machine"
	"nextboard/internal/usage"
)

// ---- Usage (§10) ----

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request, p *principal) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, map[string]any{"items": s.studio.usage.Reports(q.Get("machineId"), q.Get("agent"), q.Get("profileId"), q.Has("profileId"))})
}

func (s *Server) handleUsageCheck(w http.ResponseWriter, r *http.Request, p *principal) {
	var req usage.CheckRequest
	if !decode(w, r, &req) {
		return
	}
	rep, j, err := s.studio.usage.Check(req)
	switch {
	case errors.Is(err, machine.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "machine_not_found", err.Error())
	case errors.Is(err, usage.ErrOffline):
		writeErrCode(w, http.StatusServiceUnavailable, "machine_offline", err.Error())
	case errors.Is(err, usage.ErrBackground):
		writeErrCode(w, http.StatusConflict, "background_disabled", err.Error())
	case errors.Is(err, usage.ErrDisabled):
		writeErrCode(w, http.StatusConflict, "usage_disabled", err.Error())
	case errors.Is(err, errProfileMismatch):
		writeErrCode(w, http.StatusBadRequest, "profile_mismatch", err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	case rep != nil:
		writeJSON(w, http.StatusOK, rep)
	default:
		writeJSON(w, http.StatusAccepted, j)
	}
}

func (s *Server) handleUsageTokens(w http.ResponseWriter, r *http.Request, p *principal) {
	q := r.URL.Query()
	days, _ := strconv.Atoi(q.Get("days"))
	var ids []string
	if id := q.Get("machineId"); id != "" {
		if _, ok := s.machines.Host(id); !ok {
			writeErrCode(w, http.StatusNotFound, "machine_not_found", machine.ErrNotFound.Error())
			return
		}
		ids = []string{id}
	}
	c, err := s.studio.usage.Tokens(r.Context(), days, ids, q.Get("agent"))
	switch {
	case errors.Is(err, usage.ErrOffline):
		writeErrCode(w, http.StatusServiceUnavailable, "machine_offline", err.Error())
	case err != nil:
		fsErr(w, err)
	default:
		writeJSON(w, http.StatusOK, c)
	}
}

// ---- small helpers ----

var skillInvokeRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}(:[A-Za-z0-9_.-]{1,64})?$`)

// oneLineArgs keeps skill arguments on one line (a newline would submit).
func oneLineArgs(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 2000 {
		s = string(r[:2000])
	}
	return s
}

func pathBase(p string) string { return path.Base(path.Clean(p)) }
func pathDir(p string) string  { return path.Dir(path.Clean(p)) }
func trimExt(n string) string  { return strings.TrimSuffix(n, path.Ext(n)) }
