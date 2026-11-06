package vault

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func entry(id string, projects ...string) AgentEntry {
	return AgentEntry{ID: id, Name: "Stripe test", Type: "login", Projects: projects, Values: []string{"username", "password", "STRIPE_KEY"}}
}

func TestAgentCatalog(t *testing.T) {
	s, st := ready(t)
	if _, err := s.SetAgentEntry(entry("abcdefgh", "p_one")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an entry needs its item: %v", err)
	}
	if _, err := s.Put("abcdefgh", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("ijklmnop", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	e, err := s.SetAgentEntry(entry("abcdefgh", "p_two", "p_one", "p_two"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(e.Projects, ",") != "p_one,p_two" || e.UpdatedAt.IsZero() {
		t.Fatalf("entry %+v", e)
	}
	if !s.HasAgentEntries("p_one") || s.HasAgentEntries("p_three") || s.HasAgentEntries("") {
		t.Fatal("HasAgentEntries")
	}
	// The whole vault view never carries it: the catalog has its own route.
	if v, _ := s.Get(); len(v.Items) != 2 {
		t.Fatalf("view %+v", v)
	}

	// It survives a restart, and a damaged entry counts as a damaged file.
	again := New(st)
	if got := again.AgentEntriesFor("p_two"); len(got) != 1 || got[0].Name != "Stripe test" {
		t.Fatalf("after restart %+v", got)
	}

	// Removing it: no projects.
	if _, err := s.SetAgentEntry(AgentEntry{ID: "abcdefgh"}); err != nil {
		t.Fatal(err)
	}
	if s.HasAgentEntries("p_one") {
		t.Fatal("still there")
	}
	if _, err := s.SetAgentEntry(AgentEntry{ID: "abcdefgh"}); err != nil {
		t.Fatalf("removing twice is fine: %v", err)
	}

	// Deleting the item takes its entry along.
	if _, err := s.SetAgentEntry(entry("ijklmnop", "p_one")); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Get()
	var rev int64
	for _, it := range v.Items {
		if it.ID == "ijklmnop" {
			rev = it.Rev
		}
	}
	if err := s.Delete("ijklmnop", rev); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.AgentEntries(); len(all) != 0 {
		t.Fatalf("entry outlived its item: %+v", all)
	}
	if New(st).HasAgentEntries("p_one") {
		t.Fatal("entry outlived its item on disk")
	}

	// A reset forgets the catalog with the vault.
	if _, err := s.SetAgentEntry(entry("abcdefgh", "p_one")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if s.HasAgentEntries("p_one") {
		t.Fatal("reset kept the catalog")
	}
}

func TestAgentEntryIsChecked(t *testing.T) {
	s, _ := ready(t)
	if _, err := s.Put("abcdefgh", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*AgentEntry){
		"empty name":     func(e *AgentEntry) { e.Name = "" },
		"control char":   func(e *AgentEntry) { e.Name = "Stripe\ntest" },
		"padded":         func(e *AgentEntry) { e.Name = " Stripe" },
		"long name":      func(e *AgentEntry) { e.Name = strings.Repeat("x", 201) },
		"type":           func(e *AgentEntry) { e.Type = "secret" },
		"project id":     func(e *AgentEntry) { e.Projects = []string{"../etc"} },
		"no values":      func(e *AgentEntry) { e.Values = nil },
		"same value":     func(e *AgentEntry) { e.Values = []string{"password", "Password"} },
		"value newline":  func(e *AgentEntry) { e.Values = []string{"pass\nword"} },
		"too many projs": func(e *AgentEntry) { e.Projects = make([]string, 65) },
	}
	for name, change := range bad {
		e := entry("abcdefgh", "p_one")
		change(&e)
		if name == "too many projs" {
			for i := range e.Projects {
				e.Projects[i] = fmt.Sprintf("p%d", i)
			}
		}
		if _, err := s.SetAgentEntry(e); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s.HasAgentEntries("p_one") {
		t.Fatal("a refused entry was kept")
	}
}

func TestAgentCatalogNeedsAVault(t *testing.T) {
	s, _ := open(t)
	if _, err := s.SetAgentEntry(entry("abcdefgh", "p_one")); !errors.Is(err, ErrAbsent) {
		t.Fatalf("no vault: %v", err)
	}
}
