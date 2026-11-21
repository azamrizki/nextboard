package session

// One NextCode (docs/API-agents.md §30): NextCode is one agent, and what runs it is its business.
// The owner picks a model; the model belongs to an account, and the account says which of
// NextCode's agents (engines) runs the console:
//   - a NextCode login profile (pf_…): its own agent, "nextcode" (Claude Code) or "nextcode-codex";
//   - an API provider (ap_…): NextCode's own engine, "nextcode-api".
// A new console that names nothing starts on Settings › NextCode's account; a chat that picks a
// model of another account moves there, and its next message takes the chat so far along.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/settings"
)

// ErrNextCodeAccount: not an account NextCode can run on.
var ErrNextCodeAccount = errors.New("NextCode cannot run on that account: it takes NextCode's own logins and API providers")

// nextcodeAPIAgent is NextCode's own engine, which runs it on an API provider.
const nextcodeAPIAgent = "nextcode-api"

// nextcodeAccount is the agent an account runs NextCode with, and its login or provider.
func (m *Manager) nextcodeAccount(id string) (a agent.Agent, profileID, providerID string, err error) {
	switch {
	case strings.HasPrefix(id, "pf_"):
		if m.Profiles == nil {
			return a, "", "", ErrProfileNotFound
		}
		p, ok := m.Profiles.Get(id)
		if !ok {
			return a, "", "", ErrProfileNotFound
		}
		a, ok = m.Agents.Get(p.Agent)
		if !ok || a.Engine == "" || a.Source != agent.SourceBuiltin || p.MachineID != machine.LocalID {
			return a, "", "", ErrNextCodeAccount
		}
		return a, id, "", nil
	case strings.HasPrefix(id, "ap_"):
		a, ok := m.Agents.Get(nextcodeAPIAgent)
		if !ok {
			return a, "", "", ErrNextCodeAccount
		}
		if err := m.checkProvider(id, a); err != nil {
			return a, "", "", err
		}
		return a, "", id, nil
	}
	return a, "", "", ErrNextCodeAccount
}

// accountOf is the account a console's spec runs on: its provider, else its login.
func accountOf(sp Spec) string {
	if sp.ProviderID != "" {
		return sp.ProviderID
	}
	return sp.ProfileID
}

// isNextCodeBuiltin: one of NextCode's own agents, not a custom agent built on one.
func isNextCodeBuiltin(a agent.Agent) bool { return a.Engine != "" && a.Source == agent.SourceBuiltin }

// nextcodeDefault puts Settings › NextCode's account in a request that names no login, provider,
// model or effort for NextCode (a spawn of "NextCode"): the agent becomes that account's, with
// the account's model and effort from the setting. An account that cannot run now (removed, a
// spent budget, its engine turned off) leaves the request as it was: the agent asked for, on its
// default login.
func (m *Manager) nextcodeDefault(a agent.Agent, req *CreateRequest, st settings.Settings) agent.Agent {
	nc := st.NextCode
	if !isNextCodeBuiltin(a) || nc.Account == "" || req.ProfileID != nil || req.ProviderID != nil || req.Model != nil || req.Effort != nil {
		return a
	}
	na, profileID, providerID, err := m.nextcodeAccount(nc.Account)
	if err != nil || st.Agents.Disabled[na.ID] {
		return a
	}
	if profileID != "" {
		req.ProfileID = &profileID
	} else {
		req.ProviderID = &providerID
	}
	model, effort := nc.Model, nc.Effort
	if !agent.ValidEffortFor(na, effort) {
		effort = ""
	}
	req.Model, req.Effort = &model, &effort
	if req.Name == a.Name {
		req.Name = na.Name
	}
	return na
}

// SwitchChatAccount moves a NextCode console to another account with a model and effort of
// that account (nil = the account's own): another login or provider, often another engine. The
// chat stays; its next message starts a new conversation there with the chat so far. On the
// account it already runs on, it is SetChatModel.
func (m *Manager) SwitchChatAccount(id, account string, model, effort *string) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	if !s.IsNextCode() {
		return Info{}, ErrNotNextCode
	}
	sp := s.Spec()
	cur := m.agentFor(sp)
	if !isNextCodeBuiltin(cur) {
		return Info{}, ErrNextCodeAccount
	}
	na, profileID, providerID, err := m.nextcodeAccount(account)
	if err != nil {
		return Info{}, err
	}
	own := accountOf(sp)
	if own == "" {
		own = m.defaultProfile(sp.MachineID, cur) // a console on its agent's default login
	}
	if na.ID == cur.ID && account == own {
		if err := s.SetChatModel(model, effort); err != nil {
			return Info{}, err
		}
		return m.SessionInfo(s), nil
	}
	if m.Settings().Agents.Disabled[na.ID] {
		return Info{}, ErrAgentDisabled
	}
	empty := ""
	if model == nil {
		model = &empty // the account's own model, not the one of the account it leaves
	}
	if effort == nil {
		effort = &empty
	}
	req := RestartRequest{Agent: na.ID, Model: model, Effort: effort}
	if profileID != "" {
		req.ProfileID, req.ProviderID = &profileID, &empty
	} else {
		req.ProviderID = &providerID
	}
	s.mu.Lock()
	s.handOver = true
	s.mu.Unlock()
	info, err := m.Restart(id, req)
	if err != nil {
		s.mu.Lock()
		s.handOver = false
		s.mu.Unlock()
	}
	return info, err
}

// handOverNote is what the chat says where it moved to another account: "Now on Codex ·
// gpt-5.5: a new conversation there, with this chat so far."
func (m *Manager) handOverNote(sp Spec, a agent.Agent, carry bool) string {
	where := map[string]string{"claude": "Claude", "codex": "Codex", agent.EngineAPI: "an API provider"}[a.Engine]
	if where == "" {
		where = a.Name
	}
	if sp.ProviderID != "" && m.Providers != nil {
		if n := m.Providers.Name(sp.ProviderID); n != "" {
			where = n
		}
	} else if sp.ProfileID != "" && m.Profiles != nil {
		if p, ok := m.Profiles.Get(sp.ProfileID); ok && !p.Home {
			where = fmt.Sprintf("%s (%s)", where, p.Name)
		}
	}
	model := sp.Model
	if model == "" {
		model = "its default model"
	}
	if !carry {
		return fmt.Sprintf("Now on %s · %s.", where, model)
	}
	return fmt.Sprintf("Now on %s · %s: a new conversation there, and your next message brings this chat so far.", where, model)
}
