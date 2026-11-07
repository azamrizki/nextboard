// Package profiles keeps CLI login profiles (docs/API-agents.md §10,
// owner decision #4): a profile is a config folder handed to the agent
// through its profileEnv (CLAUDE_CONFIG_DIR, CODEX_HOME). On This PC the
// hub may create the folder; on SSH machines a profile only references a
// folder that already exists — the hub never creates folders remotely.
package profiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/store"
)

// File lists the profiles.
const File = "profiles.json"

// Profile is the API view (Exists is filled by the caller via fs/stat).
type Profile struct {
	ID           string    `json:"id"`
	MachineID    string    `json:"machineId"`
	Agent        string    `json:"agent"`
	Name         string    `json:"name"`
	Dir          string    `json:"dir"`
	CreatedByHub bool      `json:"createdByHub"`
	CreatedAt    time.Time `json:"createdAt"`
	Exists       bool      `json:"exists"`
	// Home marks the agent's own state folder inside the NextBoard data
	// folder (<data>/agents/<agent>): login, memory and settings live there.
	Home bool `json:"home,omitempty"`
}

var (
	ErrNotFound = errors.New("profile not found")
	ErrBadName  = errors.New("a profile name is required (≤ 40 characters)")
	ErrBadDir   = errors.New("the profile dir must be an absolute path")
	ErrIsHome   = errors.New("the NextBoard state folder cannot be deleted; turn it off as the default instead")
)

// Store persists profiles.json.
type Store struct {
	st *store.Store

	mu   sync.Mutex
	list []Profile
}

// Open loads profiles.json.
func Open(st *store.Store) (*Store, error) {
	s := &Store{st: st}
	if _, err := st.Load(File, &s.list); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	if s.list == nil {
		s.list = []Profile{}
	}
	return s.st.Save(File, s.list)
}

// List returns profiles, optionally of one machine.
func (s *Store) List(machineID string) []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Profile{}
	for _, p := range s.list {
		if machineID == "" || p.MachineID == machineID {
			out = append(out, p)
		}
	}
	return out
}

// Get looks a profile up.
func (s *Store) Get(id string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.list {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// HubDir is the folder of a hub-created local profile.
func (s *Store) HubDir(id string) string { return s.st.Path("profiles", id) }

// CleanName validates a display name.
func CleanName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len([]rune(n)) > 40 {
		return "", ErrBadName
	}
	return n, nil
}

// Add records a profile. createLocal makes <data>/profiles/<id>/ (0700)
// and uses it as dir; otherwise dir must be absolute (the caller checked
// it exists on the machine).
func (s *Store) Add(machineID, agent, name, dir string, createLocal bool) (Profile, error) {
	name, err := CleanName(name)
	if err != nil {
		return Profile{}, err
	}
	p := Profile{ID: auth.RandomID("pf_"), MachineID: machineID, Agent: agent, Name: name, CreatedAt: time.Now().UTC()}
	if createLocal {
		p.Dir, p.CreatedByHub = s.HubDir(p.ID), true
		if err := os.MkdirAll(p.Dir, 0o700); err != nil {
			return Profile{}, err
		}
		_ = os.Chmod(p.Dir, 0o700)
	} else {
		if !strings.HasPrefix(dir, "/") && !strings.HasPrefix(dir, "~") {
			return Profile{}, ErrBadDir
		}
		p.Dir = dir
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, p)
	return p, s.saveLocked()
}

// EnsureHome returns the agent's state folder on This PC, creating folder and
// profile when missing. created is true the first time.
func (s *Store) EnsureHome(machineID, agent string) (p Profile, created bool, err error) {
	dir := s.st.Path("agents", agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Profile{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.list {
		if x.Home && x.Agent == agent && x.MachineID == machineID {
			return x, false, nil
		}
	}
	p = Profile{ID: auth.RandomID("pf_"), MachineID: machineID, Agent: agent, Name: "NextBoard", Dir: dir,
		CreatedByHub: true, CreatedAt: time.Now().UTC(), Home: true}
	s.list = append(s.list, p)
	return p, true, s.saveLocked()
}

// TrustFolder records in a Claude Code config folder that folder, and all
// below it, is trusted, so the CLI shows no trust dialog for it. Only for
// folders the hub owns.
func TrustFolder(configDir, folder string) error {
	return editClaudeConfig(configDir, func(doc map[string]any) bool {
		projects, _ := doc["projects"].(map[string]any)
		if projects == nil {
			projects = map[string]any{}
			doc["projects"] = projects
		}
		entry, _ := projects[folder].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
			projects[folder] = entry
		}
		if entry["hasTrustDialogAccepted"] == true {
			return false
		}
		entry["hasTrustDialogAccepted"] = true
		return true
	})
}

// NoLSPOffer stops Claude Code offering its language server plugins. The offer
// is a dialog that takes the next keys, and NextBoard types orders into its
// consoles: their first words would answer it, a digit installing the plugin.
// Only for folders the hub owns.
func NoLSPOffer(configDir string) error {
	return editClaudeConfig(configDir, func(doc map[string]any) bool {
		if doc["lspRecommendationDisabled"] == true {
			return false
		}
		doc["lspRecommendationDisabled"] = true
		return true
	})
}

// editClaudeConfig changes a config file, when change says it did. A file that
// is not valid JSON is left alone.
func editClaudeConfig(configDir string, change func(doc map[string]any) bool) error {
	file := filepath.Join(configDir, ".claude.json")
	doc := map[string]any{}
	b, err := os.ReadFile(file)
	switch {
	case err == nil:
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber() // keep the CLI's large numbers as they are
		if dec.Decode(&doc) != nil {
			return nil
		}
	case !os.IsNotExist(err):
		return err
	}
	if !change(doc) {
		return nil
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := file + ".nb-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// Rename changes the display name.
func (s *Store) Rename(id, name string) (Profile, error) {
	name, err := CleanName(name)
	if err != nil {
		return Profile{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		if s.list[i].ID == id {
			s.list[i].Name = name
			return s.list[i], s.saveLocked()
		}
	}
	return Profile{}, ErrNotFound
}

// Delete removes the reference. purge also deletes the folder, but only
// for a hub-created local profile inside <data>/profiles/.
func (s *Store) Delete(id string, purge bool) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.list {
		if p.ID != id {
			continue
		}
		if p.Home {
			return Profile{}, ErrIsHome
		}
		s.list = append(s.list[:i], s.list[i+1:]...)
		if err := s.saveLocked(); err != nil {
			return Profile{}, err
		}
		root := s.st.Path("profiles")
		if purge && p.CreatedByHub && filepath.Dir(filepath.Clean(p.Dir)) == root {
			_ = os.RemoveAll(p.Dir)
		}
		return p, nil
	}
	return Profile{}, ErrNotFound
}
