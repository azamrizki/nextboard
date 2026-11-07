package orch

import (
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/store"
)

// Blocked commands (docs/API-orchestration.md §12). The critical notes are instructions a
// model chooses to follow; these are the commands for which the CLI itself, or the hub for
// its own bash tool, wants the owner's approval. Only the owner changes the list: no tool
// of an agent reaches it. A launch reads it, so a change counts from the next launch.

const (
	blockedFile    = "memory-blocked.json"
	globalBlocked  = "global"
	maxBlocked     = 64
	maxBlockedChar = 200
)

// ErrBadBlocked is a list the hub will not take.
var ErrBadBlocked = errors.New("invalid command list")

// defaultBlocked is what the first critical notes forbid in so many words. It is seeded
// once, for all projects; the owner can change or empty it.
var defaultBlocked = []string{
	"git push --force",
	"git push -f",
	"git reset --hard",
	"git filter-branch",
	"rm -rf ~",
	"rm -rf $HOME",
	"*migrate:fresh*",
	"*db:reset*",
	"*prisma migrate reset*",
	"*drop database*",
	"*DROP DATABASE*",
}

type blockedLists struct {
	mu    sync.Mutex
	st    *store.Store
	lists map[string][]string // project id, or globalBlocked
}

func newBlockedLists(st *store.Store) *blockedLists {
	l := &blockedLists{st: st, lists: map[string][]string{}}
	found, err := st.Load(blockedFile, &l.lists)
	if !found && err == nil {
		l.lists = map[string][]string{globalBlocked: slices.Clone(defaultBlocked)}
		_ = st.Save(blockedFile, l.lists)
	}
	if l.lists == nil {
		l.lists = map[string][]string{}
	}
	return l
}

func blockedOwner(pid string) string {
	if pid == "" {
		return globalBlocked
	}
	return pid
}

// BlockedList is one owner's list: pid "" for all projects.
func (s *Service) BlockedList(pid string) []string {
	s.blocked.mu.Lock()
	defer s.blocked.mu.Unlock()
	return append([]string{}, s.blocked.lists[blockedOwner(pid)]...)
}

// SetBlocked replaces one owner's list, cleaned.
func (s *Service) SetBlocked(pid string, list []string) ([]string, error) {
	clean := []string{}
	for _, raw := range list {
		p := strings.Join(strings.Fields(raw), " ")
		if p == "" || slices.Contains(clean, p) {
			continue
		}
		// With nothing to hold on to it would stop every command.
		if len([]rune(p)) > maxBlockedChar || len(strings.ReplaceAll(strings.ReplaceAll(p, "*", ""), " ", "")) < 2 {
			return nil, ErrBadBlocked
		}
		clean = append(clean, p)
	}
	if len(clean) > maxBlocked {
		return nil, ErrBadBlocked
	}
	s.blocked.mu.Lock()
	s.blocked.lists[blockedOwner(pid)] = clean
	err := s.blocked.st.Save(blockedFile, s.blocked.lists)
	s.blocked.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.hub.Publish(map[string]any{"type": "memory", "projectId": pid, "scope": "blocked"})
	return clean, nil
}

// BlockedCommands is what binds a console of a project: all projects' list, then its own.
func (s *Service) BlockedCommands(pid string) []string {
	s.blocked.mu.Lock()
	defer s.blocked.mu.Unlock()
	out := slices.Clone(s.blocked.lists[globalBlocked])
	if pid != "" {
		for _, p := range s.blocked.lists[pid] {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// AllBlocked is every list together, for a CLI whose rules live in a folder all its consoles
// share. A command blocked on one project then asks on the others too.
func (s *Service) AllBlocked() []string {
	s.blocked.mu.Lock()
	defer s.blocked.mu.Unlock()
	var out []string
	for _, list := range s.blocked.lists {
		for _, p := range list {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	slices.Sort(out)
	return out
}

// BlockedAnswer is what the hub's bash tool says instead of running a command, "" to run it.
func (s *Service) BlockedAnswer(pid, command string) string {
	p := agent.CommandBlocked(s.BlockedCommands(pid), command)
	if p == "" {
		return ""
	}
	return "Not run: the owner blocked this command (`" + p + "`). Do not look for another way to do the same thing. Stop, say what you wanted to run and why, and ask the owner: they can run it themselves or lift the block."
}
