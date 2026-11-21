package session

// NextCode consoles (docs/API-agents.md §30). A NextCode agent's console has no terminal: its
// CLI runs headless under a nextcode.Driver, made when the console launches, which starts the
// CLI on the first message and ends it when idle. The console's agent state follows the driver
// through the same transitions Claude Code's hooks drive for a terminal console. Terminal
// consoles never come here.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/nextcode"
)

// Errors of NextCode consoles.
var (
	ErrNotNextCode    = errors.New("this console is not a NextCode console")
	ErrNextCodeInput  = errors.New("a NextCode console takes messages, not keys")
	ErrNextCodeSwitch = errors.New("a terminal console and a NextCode console cannot become one another; open a new console instead")
	ErrNextCodeThisPC = errors.New("NextCode runs on This PC for now")
	// ErrNextCodeNeedsProvider: NextCode's own engine has no login; it runs on an API provider.
	ErrNextCodeNeedsProvider = errors.New("NextCode's own engine runs on an API provider: pick one, or add one in Settings › API providers")
)

// Chat is the console's NextCode driver, nil for a terminal console (or before it launched).
func (s *Session) Chat() *nextcode.Driver {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nc
}

// IsNextCode reports whether the console's agent is a NextCode agent.
func (s *Session) IsNextCode() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ag.Engine != ""
}

func (m *Manager) chatHistoryPath(id string) string { return m.st.Path("nextcode", id+".jsonl") }

// nextcodeState is where NextCode's own agent keeps its conversations (NEXTCODE_STATE), one
// <conversation id>.json each and its own files beside it (<id>.*.json).
func (m *Manager) nextcodeState() string { return m.st.Path("nextcode", "api") }

// removeConversation removes the conversation of a console of NextCode's own engine: its file in
// nextcodeState and the agent's files beside it, nothing else.
func (m *Manager) removeConversation(id string) {
	if !validSessionID(id) {
		return
	}
	dir := m.nextcodeState()
	_ = os.Remove(filepath.Join(dir, id+".json"))
	more, _ := filepath.Glob(filepath.Join(dir, id+".*.json"))
	for _, f := range more {
		_ = os.Remove(f)
	}
}

// nextcodeProvider is the API provider a console of NextCode's own engine runs on when none was
// picked: settings.nextcode.provider, while it is there and can run it. A spent budget says so.
func (m *Manager) nextcodeProvider(a agent.Agent) (string, error) {
	id := m.Settings().NextCode.Provider
	if id == "" {
		return "", ErrNextCodeNeedsProvider
	}
	switch err := m.checkProvider(id, a); {
	case errors.Is(err, ErrProviderBudget):
		return "", err
	case err != nil:
		return "", ErrNextCodeNeedsProvider
	}
	return id, nil
}

// chatIdle: an idle NextCode CLI ends after nextcode.idleExitMin.
func (m *Manager) chatIdle() time.Duration {
	return time.Duration(max(m.Settings().NextCode.IdleExitMin, 1)) * time.Minute
}

// launchChat is launch for a NextCode console. Caller holds launchMu.
func (s *Session) launchChat(spec Spec, a agent.Agent, mode string) {
	if spec.MachineID != machine.LocalID {
		s.setStatus(StatusError, -1, ErrNextCodeThisPC.Error())
		return
	}
	if _, ok := s.m.machines.Host(spec.MachineID); !ok {
		s.setStatus(StatusError, -1, "machine not found")
		return
	}
	s.mu.Lock()
	d, handOver := s.nc, s.handOver
	s.handOver = false
	s.mu.Unlock()
	if d != nil && (handOver || d.Engine() != a.Engine) {
		// another account (SwitchChatAccount) or engine, which cannot continue the conversation:
		// the chat stays, and its next message starts a new one there with the chat so far
		d.HandOver(a.Engine, func(carry bool) string { return s.m.handOverNote(spec, a, carry) })
		s.mu.Lock()
		s.spec.AgentSessionID = ""
		s.mu.Unlock()
	} else if d != nil && mode == agent.ModeFresh && d.Started() {
		// a new conversation asked for: the chat of the one before is kept beside it
		d.Close()
		path := s.m.chatHistoryPath(spec.ID)
		_ = os.Rename(path, strings.TrimSuffix(path, ".jsonl")+fmt.Sprintf(".%d.jsonl", time.Now().Unix()))
		d = nil
		s.mu.Lock()
		s.nc, s.spec.AgentSessionID = nil, ""
		s.mu.Unlock()
	}
	if d != nil {
		d.Reset() // a restart or hand-over: the next message starts a CLI with the launch as it is now
	} else {
		// a new console starts in the mode of Settings › NextCode; one with a chat keeps its own
		nd, err := nextcode.New(nextcode.Config{Engine: a.Engine, HistoryPath: s.m.chatHistoryPath(spec.ID),
			Start: s.chatStart, IdleExit: s.m.chatIdle(), PermissionMode: s.m.Settings().NextCode.PermissionMode, On: s.chatListener()})
		if err != nil {
			s.setStatus(StatusError, -1, "the chat could not be opened: "+err.Error())
			return
		}
		s.mu.Lock()
		if s.deleted {
			s.mu.Unlock()
			nd.Close()
			return
		}
		s.nc = nd
		s.mu.Unlock()
	}
	now := time.Now()
	s.mu.Lock()
	if s.ag.ID != a.ID || s.ag.Kind != a.Kind {
		s.setAgentLocked(a)
	} else {
		s.ag = a
	}
	s.startedAt = now
	s.spec.Asleep, s.spec.AsleepAt = false, nil
	s.spec.Paused, s.spec.PausedAt = false, nil
	s.eng.NewRun(agent.ModeFresh, agentstate.LiveHooks, now)
	s.mu.Unlock()
	s.setStatus(StatusRunning, 0, "")
	s.chatHook(agentstate.Hook{Event: "SessionStart"})
	s.m.saveSoon()
}

// chatStart starts the console's CLI headless (nextcode.Config.Start).
func (s *Session) chatStart(resume bool, line func([]string) []string) (*nextcode.Proc, error) {
	s.mu.Lock()
	sp, a := s.spec, s.ag
	s.mu.Unlock()
	mode := agent.ModeFresh
	if resume && validSessionID(sp.AgentSessionID) {
		mode = agent.ModeResume
	}
	if a.ProviderOnly() && sp.ProviderID == "" {
		return nil, ErrNextCodeNeedsProvider
	}
	o, st, err := s.m.launchOptions(s, a, sp, mode)
	if err != nil {
		return nil, err
	}
	if p := st.Agents.Paths[sp.MachineID][a.Base]; o.Command == "" && p != "" {
		o.Command = p // the path set for Claude Code is NextCode's CLI too
	}
	if a.Engine == nextcode.EngineCodex {
		// NextCode's effort, fitted to what the Codex model takes (One NextCode)
		home := o.ProfileDir
		if o.Provider != nil && o.Provider.ConfigDir != "" {
			home = o.Provider.ConfigDir
		}
		o.Effort = agent.NextCodeEffort(a, o.Model, o.Effort, home)
	}
	var extra []string
	if a.ProviderOnly() {
		// NextCode's own agent is the hub's own program (agents.paths can point it elsewhere)
		if o.Command == "" {
			if s.m.SelfPath == "" {
				return nil, errors.New("the hub does not know its own program, which runs NextCode's own engine")
			}
			o.Command = s.m.SelfPath
		}
		dir := s.m.nextcodeState()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		extra = append(extra, "NEXTCODE_STATE="+dir)
	}
	hp, err := agent.Headless(a, o)
	if err != nil {
		return nil, err
	}
	if hp.Mode == agent.ModeFresh && hp.AgentSessionID != "" {
		s.mu.Lock()
		s.spec.AgentSessionID = hp.AgentSessionID
		s.mu.Unlock()
		s.m.saveSoon()
	}
	h, ok := s.m.machines.Host(sp.MachineID)
	if !ok {
		return nil, errors.New("machine not found")
	}
	env := append(append(append([]string{}, hp.Env...), extra...), "NEXTBOARD_SESSION_ID="+sp.ID)
	if sp.MachineID == machine.LocalID && s.m.CommitHooks != "" && !st.Agents.CoAuthor {
		env = append(env, agent.CommitHookEnv(s.m.CommitHooks)...)
	}
	if sp.MachineID == machine.LocalID {
		env = append(env, s.m.LocalEnv...)
	}
	p, err := machine.StartPipe(h, machine.ExecSpec{Argv: line(hp.Argv), Cwd: sp.Cwd, Env: env})
	if err != nil {
		return nil, err
	}
	return &nextcode.Proc{Stdin: p.Stdin, Stdout: p.Stdout, Done: p.Done(), Stop: p.Stop, Stderr: p.StderrTail}, nil
}

// chatListener turns the driver's events into the agent state of the console.
func (s *Session) chatListener() nextcode.Listener {
	return nextcode.Listener{
		TurnStart: func() { s.chatHook(agentstate.Hook{Event: "UserPromptSubmit"}) },
		Tool: func(name, summary string) {
			switch name {
			case "AskUserQuestion", "ExitPlanMode": // their request says so (NeedsYou)
				return
			}
			s.chatHook(agentstate.Hook{Event: "PreToolUse", ToolName: name, ToolSummary: summary})
		},
		NeedsYou: func(kind, text string) {
			switch kind {
			case nextcode.ReqQuestion:
				s.chatHook(agentstate.Hook{Event: "PreToolUse", ToolName: "AskUserQuestion"})
			case nextcode.ReqPlan:
				s.chatHook(agentstate.Hook{Event: "PreToolUse", ToolName: "ExitPlanMode"})
			default:
				s.chatHook(agentstate.Hook{Event: "Notification", NotificationType: "permission_prompt", Message: text})
			}
		},
		Answered: func() { s.chatHook(agentstate.Hook{Event: "PostToolUse"}) },
		TurnEnd:  func(string) { s.chatHook(agentstate.Hook{Event: "Stop"}) },
		Exit:     func(string) { s.chatHook(agentstate.Hook{Event: "Stop"}) },
		Conversation: func(id string) {
			if !validSessionID(id) {
				return
			}
			s.mu.Lock()
			changed := s.spec.AgentSessionID != id
			s.spec.AgentSessionID = id
			s.mu.Unlock()
			if changed {
				s.m.saveSoon()
			}
		},
	}
}

// chatHook applies a state transition to the console and publishes it.
func (s *Session) chatHook(h agentstate.Hook) {
	s.mu.Lock()
	if s.deleted {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	s.lastOutput = now
	s.eng.Hook(h, now)
	notify, ev := s.afterLocked(now, false)
	s.mu.Unlock()
	s.finish(notify, ev)
}

// SendChat sends a message to a NextCode console (the owner's, or an order's).
func (s *Session) SendChat(text string) error {
	d := s.Chat()
	if d == nil {
		if s.IsNextCode() {
			return errors.New("the console is not running")
		}
		return ErrNotNextCode
	}
	// NextCode's own commands (/effort, /model, /mode) it carries out itself, on every engine
	if handled, err := s.chatCommand(d, text); handled {
		return err
	}
	return d.Send(text)
}

// SetChatModel changes a NextCode console's model and effort (nil = unchanged, "" = the CLI's
// own): kept in its Spec for every CLI after this one; the model switches the running CLI at
// once, the effort from its next start (the CLI ends when it is idle for that).
func (s *Session) SetChatModel(model, effort *string) error {
	d := s.Chat()
	if d == nil {
		if s.IsNextCode() {
			return errors.New("the console is not running")
		}
		return ErrNotNextCode
	}
	s.mu.Lock()
	a := s.ag
	m, e, err := chooseModel(a, model, effort, s.spec.Model, s.spec.Effort)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	modelChanged, effortChanged := m != s.spec.Model, e != s.spec.Effort
	s.spec.Model, s.spec.Effort = m, e
	s.mu.Unlock()
	if !modelChanged && !effortChanged {
		return nil
	}
	s.m.saveSoon()
	s.m.changed(s)
	if effortChanged {
		d.ResetWhenIdle()
		return nil
	}
	return d.SetModel(m)
}

// closeChat ends the console's driver (delete, hub shutdown).
func (s *Session) closeChat() {
	s.mu.Lock()
	d := s.nc
	s.mu.Unlock()
	if d != nil {
		d.Close()
	}
}
