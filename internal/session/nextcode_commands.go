package session

// NextCode's own commands (docs/API-agents.md §30, One NextCode): /effort, /model and /mode are
// NextCode's settings of the chat, the same on every engine, so it carries them out itself
// instead of handing them to the CLI (Claude Code had its own /effort, which also wrote its
// profile's settings and left the chat's chip behind; Codex had none). Any other "/" goes to the
// CLI as before.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/nextcode"
)

// NextCodeCommands are NextCode's own commands, for the composer's list.
var NextCodeCommands = []string{"model", "effort", "mode"}

// modeNames are the permission modes by the names the owner may type.
var modeNames = map[string]string{
	"ask": "default", "default": "default", "always-ask": "default",
	"accept": "acceptEdits", "accept-edits": "acceptEdits", "acceptedits": "acceptEdits", "edits": "acceptEdits",
	"plan": "plan", "auto": "auto",
	"bypass": "bypassPermissions", "bypasspermissions": "bypassPermissions", "yolo": "bypassPermissions",
}

// modeLabel is a mode as the chat names it.
var modeLabel = map[string]string{"default": "Ask", "acceptEdits": "Accept edits", "plan": "Plan", "auto": "Auto", "bypassPermissions": "Bypass"}

// chatCommand carries out one of NextCode's own commands; handled false for anything else.
func (s *Session) chatCommand(d *nextcode.Driver, text string) (handled bool, err error) {
	line := strings.TrimSpace(text)
	if !strings.HasPrefix(line, "/") || strings.Contains(line, "\n") {
		return false, nil
	}
	name, arg, _ := strings.Cut(line[1:], " ")
	name, arg = strings.ToLower(name), strings.TrimSpace(arg)
	switch name {
	case "effort":
		return true, d.Note(line, s.effortCommand(arg))
	case "model":
		return true, d.Note(line, s.modelCommand(arg))
	case "mode":
		return true, d.Note(line, modeCommand(d, arg))
	}
	return false, nil
}

func (s *Session) effortCommand(arg string) string {
	choices := strings.Join(agent.NextCodeEfforts(), ", ")
	if arg == "" {
		s.mu.Lock()
		cur := s.spec.Effort
		s.mu.Unlock()
		if cur == "" {
			cur = "the model's own"
		}
		return fmt.Sprintf("Effort: %s. /effort %s, or default.", cur, strings.ReplaceAll(choices, ", ", " | "))
	}
	e := strings.ToLower(arg)
	if e == "default" || e == "auto" {
		e = ""
	}
	if err := s.SetChatModel(nil, &e); err != nil {
		if errors.Is(err, ErrBadRequest) {
			return fmt.Sprintf("“%s” is not one of NextCode's efforts: %s, or default.", arg, choices)
		}
		return "The effort was not changed: " + err.Error()
	}
	if e == "" {
		return "Effort: the model's own, from the next message."
	}
	return fmt.Sprintf("Effort: %s, from the next message, on every model of this chat.", e)
}

func (s *Session) modelCommand(arg string) string {
	s.mu.Lock()
	sp, a := s.spec, s.ag
	s.mu.Unlock()
	where := s.m.accountLabel(sp, a)
	if arg == "" {
		cur := sp.Model
		if cur == "" {
			cur = "its default model"
		}
		return fmt.Sprintf("Model: %s, on %s. /model <name> picks another of %s; the model menu lists every account's.", cur, where, where)
	}
	m := arg
	if strings.EqualFold(m, "default") {
		m = ""
	}
	if err := s.SetChatModel(&m, nil); err != nil {
		if errors.Is(err, ErrBadRequest) {
			return fmt.Sprintf("“%s” is not a model name.", arg)
		}
		return "The model was not changed: " + err.Error()
	}
	if m == "" {
		return fmt.Sprintf("Model: %s's default.", where)
	}
	return fmt.Sprintf("Model: %s, on %s.", m, where)
}

func modeCommand(d *nextcode.Driver, arg string) string {
	if arg == "" {
		return fmt.Sprintf("Mode: %s. /mode ask | accept-edits | plan | auto | bypass.", modeLabel[d.Status().PermissionMode])
	}
	mode, ok := modeNames[strings.ToLower(arg)]
	if !ok {
		return fmt.Sprintf("“%s” is not a mode: ask, accept-edits, plan, auto or bypass.", arg)
	}
	if err := d.SetPermissionMode(mode); err != nil {
		return "The mode was not changed: " + err.Error()
	}
	return fmt.Sprintf("Mode: %s.", modeLabel[mode])
}
