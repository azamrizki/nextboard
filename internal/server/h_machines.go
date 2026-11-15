package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/session"
)

func (s *Server) machineInfo(w http.ResponseWriter, id string) {
	in, ok := s.machines.Info(id)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// handleMachines lists the active machines (This PC first), or with ?archived=1 the archived ones.
func (s *Server) handleMachines(w http.ResponseWriter, r *http.Request, p *principal) {
	if r.URL.Query().Get("archived") == "1" {
		writeJSON(w, http.StatusOK, s.machines.ListArchived())
		return
	}
	writeJSON(w, http.StatusOK, s.machines.List())
}

func (s *Server) handleSSHConfig(w http.ResponseWriter, r *http.Request, p *principal) {
	hosts, err := machine.ListSSHConfigHosts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot read ~/.ssh/config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (s *Server) handleMachineAdd(w http.ResponseWriter, r *http.Request, p *principal) {
	var req machine.AddRequest
	if !decode(w, r, &req) {
		return
	}
	m, err := s.machines.Add(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// First connect is synchronous so the response already says whether a
	// host key confirmation is needed. Errors are reported in the machine.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	_ = m.Connect(ctx)
	m.EnsureLoop()
	in, _ := s.machines.Info(m.ID())
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleMachineHostKey(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Accept      bool   `json:"accept"`
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &req) {
		return
	}
	m, ok := s.machines.SSH(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	if !req.Accept {
		writeErr(w, http.StatusBadRequest, "accept must be true")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := m.AcceptHostKey(ctx, req.Fingerprint); errors.Is(err, machine.ErrFingerprintMismatch) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.machineInfo(w, m.ID())
}

// handleMachineCredentials answers what a connection asked for (machine.AuthNeed).
// The answer never comes back in any response.
func (s *Server) handleMachineCredentials(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Kind     string `json:"kind"`
		Value    string `json:"value"`
		Remember bool   `json:"remember"`
	}
	if !decode(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	m, ok := s.machines.SSH(id)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	if req.Value == "" {
		writeErr(w, http.StatusBadRequest, "fill it in first")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := m.Answer(ctx, req.Kind, req.Value, req.Remember); errors.Is(err, machine.ErrAuthNotNeeded) {
		writeErrCode(w, http.StatusConflict, "auth_not_needed", err.Error())
		return
	}
	// A refused answer or a server gone away is the machine's state, like any connect.
	s.machineInfo(w, id)
}

func (s *Server) handleMachineConnect(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	if s.machines.IsArchived(id) {
		machineErr(w, machine.ErrArchived)
		return
	}
	if m, ok := s.machines.SSH(id); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		_ = m.Connect(ctx)
		m.EnsureLoop()
	}
	s.machineInfo(w, id)
}

func (s *Server) handleMachineProbe(w http.ResponseWriter, r *http.Request, p *principal) {
	if s.machines.IsArchived(r.PathValue("id")) {
		machineErr(w, machine.ErrArchived)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	in, err := s.machines.Probe(ctx, r.PathValue("id"))
	switch {
	case errors.Is(err, machine.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, in)
	}
}

func (s *Server) handleMachineUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name        *string `json:"name"`
		PersistMode *string `json:"persistMode"`
	}
	if !decode(w, r, &req) {
		return
	}
	in, err := s.machines.Update(r.PathValue("id"), req.Name, req.PersistMode)
	switch {
	case errors.Is(err, machine.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, in)
	}
}

func (s *Server) handleMachineLs(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	l, err := h.ListDir(ctx, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// orphans returns the tmux sessions on a server that have no node on the board: own are this
// hub's (nb-<hubId>-…); legacy carry no hub mark (nb-s_…, from before hubIds), so they may be
// another hub's live consoles, and the dialog leaves them unselected. A sibling hub's names
// (nb-<its hubId>-…) are not listed at all: a shared server holds other hubs' consoles.
func (s *Server) orphans(ctx context.Context, id string) (own, legacy []string, err error) {
	own, legacy = []string{}, []string{}
	m, ok := s.machines.SSH(id)
	if !ok {
		return own, legacy, nil
	}
	names, err := m.TmuxSessions(ctx)
	if err != nil {
		return nil, nil, err
	}
	own, legacy = classifyOrphans(names, s.sessions.TmuxNames(id), s.hubID())
	return own, legacy, nil
}

// classifyOrphans sorts a server's tmux sessions that no node owns into this hub's and those
// without a hub mark; everything else (a sibling hub's, someone's own nb-…) is left out.
func classifyOrphans(names []string, owned map[string]bool, hubID string) (own, legacy []string) {
	own, legacy = []string{}, []string{}
	mine := ""
	if hubID != "" {
		mine = session.TmuxPrefix(hubID)
	}
	for _, n := range names {
		switch {
		case owned[n]:
		case mine != "" && strings.HasPrefix(n, mine):
			own = append(own, n)
		case strings.HasPrefix(n, session.LegacyTmuxPrefix):
			legacy = append(legacy, n)
		}
	}
	return own, legacy
}

func (s *Server) handleMachineOrphans(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	if _, ok := s.machines.Host(id); !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	names, legacy, err := s.orphans(ctx, id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": names, "legacy": legacy})
}

func (s *Server) handleMachineCleanup(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Names []string `json:"names"`
	}
	if !decode(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	m, ok := s.machines.SSH(id)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// Only kill names that are still orphans right now.
	own, legacy, err := s.orphans(ctx, id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	isOrphan := map[string]bool{}
	for _, n := range append(own, legacy...) {
		isOrphan[n] = true
	}
	killed := []string{}
	for _, n := range req.Names {
		if isOrphan[n] && m.KillTmux(ctx, n) == nil {
			killed = append(killed, n)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"killed": killed})
}
