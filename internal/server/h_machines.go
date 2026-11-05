package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"nextboard/internal/machine"
)

func (s *Server) machineInfo(w http.ResponseWriter, id string) {
	in, ok := s.machines.Info(id)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleMachines(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.machines.List())
}

func (s *Server) handleSSHConfig(w http.ResponseWriter, r *http.Request, p *principal) {
	hosts, err := machine.ListSSHConfigHosts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "gagal membaca ~/.ssh/config: "+err.Error())
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
		writeErr(w, http.StatusBadRequest, "accept harus true")
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
		writeErr(w, http.StatusBadRequest, "isi dulu")
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
	if m, ok := s.machines.SSH(id); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		_ = m.Connect(ctx)
		m.EnsureLoop()
	}
	s.machineInfo(w, id)
}

func (s *Server) handleMachineProbe(w http.ResponseWriter, r *http.Request, p *principal) {
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

func (s *Server) handleMachineDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	if id == machine.LocalID {
		writeErr(w, http.StatusBadRequest, machine.ErrLocal.Error())
		return
	}
	if n := s.sessions.CountByMachine(id); n > 0 {
		writeErr(w, http.StatusConflict, "machine masih punya sesi; tutup semua terminalnya dulu")
		return
	}
	if err := s.machines.Remove(id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, okBody)
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

// orphans returns nb-* tmux sessions with no node on the board.
func (s *Server) orphans(ctx context.Context, id string) ([]string, error) {
	m, ok := s.machines.SSH(id)
	if !ok {
		return []string{}, nil
	}
	names, err := m.TmuxSessions(ctx)
	if err != nil {
		return nil, err
	}
	owned := s.sessions.TmuxNames(id)
	out := []string{}
	for _, n := range names {
		if !owned[n] {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *Server) handleMachineOrphans(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	if _, ok := s.machines.Host(id); !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	names, err := s.orphans(ctx, id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": names})
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
	current, err := s.orphans(ctx, id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	isOrphan := map[string]bool{}
	for _, n := range current {
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
