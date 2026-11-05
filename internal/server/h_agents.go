package server

import (
	"errors"
	"net/http"
	"os"

	"nextboard/internal/agent"
	"nextboard/internal/machine"
	"nextboard/internal/transcript"
)

func agentErr(w http.ResponseWriter, err error) {
	var be *agent.BadAgentError
	switch {
	case errors.As(err, &be):
		writeErrCode(w, http.StatusBadRequest, "bad_agent", err.Error())
	case errors.Is(err, agent.ErrExists):
		writeErrCode(w, http.StatusConflict, "agent_exists", err.Error())
	case errors.Is(err, agent.ErrBuiltin):
		writeErrCode(w, http.StatusForbidden, "agent_builtin", err.Error())
	case errors.Is(err, agent.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "agent_not_found", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// agentOff reports an agent the owner turned off. A shell is no agent: it stays.
func (s *Server) agentOff(a agent.Agent) bool {
	return a.Kind == agent.KindAgent && s.settings.Get().Agents.Disabled[a.ID]
}

// available reports whether an agent is there to be offered on machine h.
func (s *Server) available(h machine.Host, a agent.Agent) bool {
	return !s.agentOff(a) && s.installed(h, a)
}

// installed reports whether an agent can run on machine h: shells always,
// otherwise its detect command (or the settings path override) resolves.
func (s *Server) installed(h machine.Host, a agent.Agent) bool {
	if a.Kind == agent.KindShell || a.Command == "$SHELL" {
		return true
	}
	if h.ID() != machine.LocalID {
		// Agents run on This PC; a server takes those that can work on it (§21).
		if !s.settings.Get().Agents.RemoteTools || !a.WorksOnServers() {
			return false
		}
		h = s.machines.Local()
	}
	override := s.settings.Get().Agents.Paths[h.ID()][a.ID]
	if h.ID() == machine.LocalID {
		if override != "" {
			return s.machines.Local().Has(override)
		}
		return a.Detect != "" && s.machines.Local().Has(a.Detect)
	}
	probe := h.Info().Probe
	if probe == nil {
		return false
	}
	if override != "" {
		return probe.Commands[override] != ""
	}
	return a.Detect != "" && probe.Commands[a.Detect] != ""
}

// machineAgents returns the catalog with what is installed on one machine and what
// is turned off. A turned off agent never is the default.
func (s *Server) machineAgents(h machine.Host) ([]agent.Agent, string) {
	all := s.agents.All()
	for i := range all {
		v := s.installed(h, all[i])
		all[i].Available = &v
		all[i].Disabled = s.agentOff(all[i])
	}
	def := agent.DefaultFor(all, s.settings.Get().Agents.Defaults[h.ID()], func(a agent.Agent) bool { return *a.Available && !a.Disabled })
	return all, def
}

// offered leaves out what is turned off: what a client without its own filter gets.
func offered(list []agent.Agent) []agent.Agent {
	out := make([]agent.Agent, 0, len(list))
	for _, a := range list {
		if !a.Disabled {
			out = append(out, a)
		}
	}
	return out
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.agents.All())
}

// handlePresets is the phase 1–4 endpoint; Agent is a superset of Preset.
func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.agents.All())
}

func (s *Server) handleMachineAgents(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	list, def := s.machineAgents(h)
	// ?all=1: also the turned off ones, for the page that turns them on again.
	if r.URL.Query().Get("all") != "1" {
		list = offered(list)
	}
	writeJSON(w, http.StatusOK, map[string]any{"defaultAgent": def, "agents": list})
}

// handleAgentModels lists what a CLI says it can run. Agents run on This PC
// whichever machine they work on, so that is where the list comes from.
func (s *Server) handleAgentModels(w http.ResponseWriter, r *http.Request, p *principal) {
	a, ok := s.agents.Get(r.PathValue("agent"))
	if !ok {
		writeErr(w, http.StatusNotFound, "agen tidak dikenal")
		return
	}
	id := r.URL.Query().Get("profileId")
	if id == "" {
		id = s.settings.Get().Agents.Profiles[machine.LocalID][a.ID]
	}
	dir := ""
	if pf, ok := s.profiles.Get(id); ok && pf.MachineID == machine.LocalID {
		dir = pf.Dir
	}
	home, _ := os.UserHomeDir()
	switch a.CapBase() {
	case "claude":
		if dir == "" {
			dir = os.Getenv("CLAUDE_CONFIG_DIR")
		}
		dir = transcript.ClaudeConfigDir(home, dir)
	case "codex":
		if dir == "" {
			dir = os.Getenv("CODEX_HOME")
		}
		dir = transcript.CodexHome(home, dir)
	}
	writeJSON(w, http.StatusOK, agent.ListModels(r.Context(), a, s.settings.Get().Agents.Paths[machine.LocalID][a.ID], dir))
}

func (s *Server) handleMachinePresets(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	list, _ := s.machineAgents(h)
	writeJSON(w, http.StatusOK, offered(list))
}

func (s *Server) agentsChanged() {
	s.events.Publish(map[string]any{"type": "agents"})
}

func (s *Server) handleAgentCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req agent.Request
	if !decode(w, r, &req) {
		return
	}
	a, err := s.agents.Create(req)
	if err != nil {
		agentErr(w, err)
		return
	}
	s.agentsChanged()
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleAgentUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req agent.Request
	if !decode(w, r, &req) {
		return
	}
	a, err := s.agents.Update(r.PathValue("id"), req)
	if err != nil {
		agentErr(w, err)
		return
	}
	s.agentsChanged()
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleAgentDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.agents.Delete(r.PathValue("id")); err != nil {
		agentErr(w, err)
		return
	}
	s.agentsChanged()
	writeJSON(w, http.StatusOK, okBody)
}
