package settings

import (
	"testing"

	"nextboard/internal/store"
)

func TestModelFor(t *testing.T) {
	a := Agents{
		RoleAgents: map[string]string{"testers": "codex", "general": ""},
		Models:     map[string]ModelChoice{"codex": {Model: "gpt-6-sol", Effort: "low"}, "claude": {Model: "sonnet"}},
		RoleModels: map[string]ModelChoice{"testers": {Effort: "high"}, "general": {Model: "nope"}},
	}
	if c := a.ModelFor("", "codex"); c.Model != "gpt-6-sol" || c.Effort != "low" {
		t.Fatalf("agent default %+v", c)
	}
	if c := a.ModelFor("testers", "codex"); c.Model != "gpt-6-sol" || c.Effort != "high" {
		t.Fatalf("role over agent, field by field %+v", c)
	}
	// the user put another agent in the slot: the role speaks of its own agent only
	if c := a.ModelFor("testers", "claude"); c.Model != "sonnet" || c.Effort != "" {
		t.Fatalf("another agent %+v", c)
	}
	if c := a.ModelFor("general", "claude"); c.Model != "sonnet" {
		t.Fatalf("a role with no agent %+v", c)
	}
}

func TestModelSettingsPatch(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Patch([]byte(`{"agents":{"models":{"codex":{"model":"gpt-6.1-sol","effort":"high"}},"roleModels":{"testers":{"model":"gpt-6-astra"}}}}`))
	if err != nil || got.Agents.Models["codex"].Model != "gpt-6.1-sol" || got.Agents.RoleModels["testers"].Model != "gpt-6-astra" {
		t.Fatalf("%+v %v", got.Agents.Models, err)
	}
	got, err = s.Patch([]byte(`{"agents":{"models":{"codex":{"effort":null}},"roleModels":{"testers":null}}}`))
	if err != nil || got.Agents.Models["codex"].Effort != "" || got.Agents.Models["codex"].Model != "gpt-6.1-sol" || len(got.Agents.RoleModels) != 0 {
		t.Fatalf("%+v %+v %v", got.Agents.Models, got.Agents.RoleModels, err)
	}
	for _, bad := range []string{
		`{"agents":{"models":{"codex":{"model":"a b"}}}}`,
		`{"agents":{"models":{"codex":{"model":"$(x)"}}}}`,
		`{"agents":{"roleModels":{"testers":{"effort":"Very High"}}}}`,
	} {
		if _, err := s.Patch([]byte(bad)); err == nil {
			t.Fatalf("%s passes", bad)
		}
	}
}
