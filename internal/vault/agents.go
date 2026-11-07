package vault

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The agent catalog (docs/API-vault.md, "Agent memakai credential"): what agents may know about
// the items the owner marked for them. Names only, in plain text, written by the browser that
// marked them. The values stay in the item's blob, and the browser reads the blob again before it
// gives a value out, so an entry the browser did not write opens nothing.

const (
	// MaxAgentEntries is more items than anyone marks for agents by hand.
	MaxAgentEntries = 500
	maxAgentName    = 200
	maxAgentValues  = 32
	maxAgentValue   = 100
	maxAgentProjs   = 64
)

var (
	projectIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	agentTypes  = map[string]bool{"login": true, "card": true, "identity": true, "note": true, "ssh": true}
)

// AgentEntry is one item the owner lets agents of some projects ask for.
type AgentEntry struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Projects []string `json:"projects"`
	// Values are the names of what a command can be given, like "password" or a custom field's name.
	Values    []string  `json:"values"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// For reports whether agents of the project may ask for this item.
func (e AgentEntry) For(projectID string) bool { return slices.Contains(e.Projects, projectID) }

func plainText(s string, max int) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > max || strings.TrimSpace(s) != s {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

func (e AgentEntry) validate() error {
	if !idRe.MatchString(e.ID) {
		return fmt.Errorf("%w: id", ErrInvalid)
	}
	if !plainText(e.Name, maxAgentName) {
		return fmt.Errorf("%w: name", ErrInvalid)
	}
	if !agentTypes[e.Type] {
		return fmt.Errorf("%w: type %q", ErrInvalid, e.Type)
	}
	if len(e.Projects) == 0 || len(e.Projects) > maxAgentProjs {
		return fmt.Errorf("%w: project", ErrInvalid)
	}
	for _, p := range e.Projects {
		if !projectIDRe.MatchString(p) {
			return fmt.Errorf("%w: project %q", ErrInvalid, p)
		}
	}
	if len(e.Values) == 0 || len(e.Values) > maxAgentValues {
		return fmt.Errorf("%w: values", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, v := range e.Values {
		k := strings.ToLower(v)
		if !plainText(v, maxAgentValue) || seen[k] {
			return fmt.Errorf("%w: value name %q", ErrInvalid, v)
		}
		seen[k] = true
	}
	return nil
}

func sortedEntries(m map[string]AgentEntry, keep func(AgentEntry) bool) []AgentEntry {
	out := []AgentEntry{}
	for _, e := range m {
		if keep == nil || keep(e) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// AgentEntries returns the whole catalog, by name.
func (s *Service) AgentEntries() ([]AgentEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return nil, err
	}
	return sortedEntries(s.d.Agents, nil), nil
}

// AgentEntriesFor returns what agents of one project may ask for, by name.
func (s *Service) AgentEntriesFor(projectID string) []AgentEntry {
	if projectID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unreadable() != nil {
		return nil
	}
	return sortedEntries(s.d.Agents, func(e AgentEntry) bool { return e.For(projectID) })
}

// HasAgentEntries reports whether agents of the project may ask for anything at all: only then do
// their consoles get the credential tools, which cost tokens in every turn.
func (s *Service) HasAgentEntries(projectID string) bool {
	return len(s.AgentEntriesFor(projectID)) > 0
}

// SetAgentEntry adds, changes or, with no projects, removes one item's entry. The item must exist.
func (s *Service) SetAgentEntry(e AgentEntry) (*AgentEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return nil, err
	}
	if s.d.Header == nil {
		return nil, ErrAbsent
	}
	if !idRe.MatchString(e.ID) {
		return nil, fmt.Errorf("%w: id", ErrInvalid)
	}
	next := s.d
	next.Agents = maps.Clone(s.d.Agents)
	if next.Agents == nil {
		next.Agents = map[string]AgentEntry{}
	}
	if len(e.Projects) == 0 {
		if _, ok := next.Agents[e.ID]; !ok {
			return nil, nil
		}
		delete(next.Agents, e.ID)
		return nil, s.save(next)
	}
	if _, ok := s.d.Items[e.ID]; !ok {
		return nil, ErrNotFound
	}
	e.Projects = slices.Compact(slices.Sorted(slices.Values(e.Projects)))
	if err := e.validate(); err != nil {
		return nil, err
	}
	if _, ok := next.Agents[e.ID]; !ok && len(next.Agents) >= MaxAgentEntries {
		return nil, ErrFull
	}
	e.UpdatedAt = now()
	next.Agents[e.ID] = e
	if err := s.save(next); err != nil {
		return nil, err
	}
	return &e, nil
}
