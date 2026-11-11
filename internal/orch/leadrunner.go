package orch

import (
	"fmt"
	"strings"

	"github.com/azamrizki/nextboard/internal/jobs"
)

// ProviderInfo is an API provider as the lead sees it (docs/API-agents.md §10.2): never its key.
type ProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Agents are the agent ids whose consoles can run on it.
	Agents []string `json:"agents"`
}

// leadRun is what the lead thinks with (settings lead.runner).
type leadRun struct {
	agent, command string
	// provider goes into RunnerRequest.Provider: "" = runner.provider, jobs.ProviderLogin, ap_….
	provider string
	// label names it in what the owner reads.
	label string
}

func cliLabel(agentID string) string {
	if agentID == "codex" {
		return "Codex"
	}
	return "Claude Code"
}

// leadRunner resolves settings lead.runner. An API provider runs on the CLI that speaks its API.
func (s *Service) leadRunner() (leadRun, error) {
	r := s.hub.Settings().Lead.Runner
	switch r {
	case "":
		return leadRun{agent: "claude", command: s.cliCommand("claude"), label: "Claude Code"}, nil
	case "claude", "codex":
		return leadRun{agent: r, command: s.cliCommand(r), provider: jobs.ProviderLogin, label: cliLabel(r)}, nil
	}
	ag, name, err := s.hub.ProviderRunner(r)
	if err != nil {
		return leadRun{}, err
	}
	return leadRun{agent: ag, command: s.cliCommand(ag), provider: r, label: "API · " + name}, nil
}

// leadRunOrDefault names the lead's runner for a message, also when it can no longer be resolved.
func (s *Service) leadRunOrDefault() leadRun {
	if lr, err := s.leadRunner(); err == nil {
		return lr
	}
	return leadRun{agent: "claude", label: "The lead's runner"}
}

// leadRequest fills in the CLI and account of a run the lead makes for itself.
func (s *Service) leadRequest(req jobs.RunnerRequest) (jobs.RunnerRequest, error) {
	lr, err := s.leadRunner()
	if err != nil {
		return req, err
	}
	req.Agent, req.Command, req.Provider = lr.agent, lr.command, lr.provider
	return req, nil
}

// cliCommand is the path override of an agent CLI on This PC, if any.
func (s *Service) cliCommand(agentID string) string {
	return s.hub.Settings().Agents.Paths["local"][agentID]
}

// providerFor is the API provider a console the lead starts runs on: the one an order named
// ("login" or "subscription" = none), else settings lead.providers for that agent.
func (s *Service) providerFor(agentID, named string) (string, error) {
	switch n := strings.ToLower(strings.TrimSpace(named)); n {
	case "":
	case "login", "subscription", "default", "none":
		return "", nil
	default:
		for _, p := range s.hub.Providers() {
			if p.ID == n || strings.EqualFold(p.Name, strings.TrimSpace(named)) || strings.EqualFold("api · "+p.Name, n) {
				return p.ID, nil
			}
		}
		return "", fmt.Errorf("there is no API provider %q", named)
	}
	return s.hub.Settings().Lead.Providers[agentID], nil
}
