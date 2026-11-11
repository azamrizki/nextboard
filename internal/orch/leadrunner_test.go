package orch

import (
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/jobs"
)

// say sends an order to the lead with the planner on.
func say(t *testing.T, s *Service, wid, text string) CommandResponse {
	t.Helper()
	r, err := s.Command(CommandRequest{Text: text, WorkspaceID: wid, ClientID: "c_test"})
	if err != nil {
		t.Fatalf("%q: %v", text, err)
	}
	return r
}

// TestLeadRunner: settings lead.runner picks the CLI and account of the lead's own runs.
func TestLeadRunner(t *testing.T) {
	h, s := newFakeHub(t)
	h.set.Lead.TalkFirst = true // every sentence goes to the lead
	p, wid := h.project("app")
	h.onMach["local/codex"] = true
	h.provs = []ProviderInfo{{ID: "ap_anth1", Name: "Relay", Agents: []string{"claude", "opencode"}}, {ID: "ap_resp1", Name: "Resp", Agents: []string{"codex"}}}
	h.provRun = map[string]string{"ap_anth1": "claude", "ap_resp1": "codex"}
	plan := func() jobs.RunnerRequest {
		t.Helper()
		if r := say(t, s, wid, "ship the release notes please, all of them"); r.HandledBy != "pending" {
			t.Fatalf("not planned: %s %s", r.HandledBy, texts(r))
		}
		run := h.lastRun(t, "planner")
		s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "ok", "steps": []any{}}))
		return run
	}
	for _, c := range []struct{ runner, agent, provider, label string }{
		{"", "claude", "", "Claude Code"},
		{"claude", "claude", jobs.ProviderLogin, "Claude Code"},
		{"codex", "codex", jobs.ProviderLogin, "Codex"},
		{"ap_anth1", "claude", "ap_anth1", "API · Relay"},
		{"ap_resp1", "codex", "ap_resp1", "API · Resp"},
	} {
		h.set.Lead.Runner = c.runner
		if st := s.Planner(); !st.Available || st.Runner != c.label {
			t.Fatalf("%q status %+v", c.runner, st)
		}
		if run := plan(); run.Agent != c.agent || run.Provider != c.provider || string(run.Schema) != string(PlanSchema) {
			t.Fatalf("%q runner %+v", c.runner, run)
		}
	}
	// the lead's model and effort go with its runs
	h.set.Lead.Model, h.set.Lead.Effort = "cx/fast", "low"
	if run := plan(); run.Model != "cx/fast" || run.Effort != "low" {
		t.Fatalf("model %q effort %q", run.Model, run.Effort)
	}
	h.set.Lead.Model, h.set.Lead.Effort = "", ""
	// the lead's CLI must be on This PC
	h.set.Lead.Runner = "codex"
	delete(h.onMach, "local/codex")
	h.planner = false
	if st := s.Planner(); st.Available || st.Reason != "codex_missing" {
		t.Fatalf("missing codex %+v", st)
	}
	h.planner = true
	// a provider that is gone: the parser answers and says why
	h.set.Lead.Runner = "ap_gone1"
	if st := s.Planner(); st.Available || !strings.Contains(st.Reason, "provider not found") {
		t.Fatalf("gone %+v", st)
	}
	// a sign-in failure names the runner, and choosing another runner tries again at once
	h.set.Lead.Runner = "ap_anth1"
	say(t, s, wid, "ship the release notes please, all of them")
	s.plannerDone(p.ID, failedJob("cli_not_logged_in"))
	if last := s.Threads.Last(p.ID, 3); !strings.Contains(texts(CommandResponse{Messages: last}), "API · Relay refused the key") {
		t.Fatalf("login note %+v", last)
	}
	if st := s.Planner(); st.Available || st.Reason != "not_logged_in" {
		t.Fatalf("after login failure %+v", st)
	}
	h.set.Lead.Runner = "claude"
	if st := s.Planner(); !st.Available {
		t.Fatalf("another runner %+v", st)
	}
	// the summary of a digest goes the same way
	h.set.Lead.Runner = "ap_resp1"
	s.summarise(&execCtx{threadID: p.ID, projectID: p.ID}, "a report")
	if run := h.lastRun(t, "thread-summary"); run.Agent != "codex" || run.Provider != "ap_resp1" {
		t.Fatalf("summary %+v", run)
	}
}

// TestLeadSpawnsOnProvider: an order can name an API provider for new consoles, and settings
// lead.providers gives each agent a default; consoles started elsewhere keep their login.
func TestLeadSpawnsOnProvider(t *testing.T) {
	h, s := newFakeHub(t)
	h.set.Lead.TalkFirst = true // every sentence goes to the lead
	p, wid := h.project("app")
	h.onMach["local/codex"] = true
	h.provs = []ProviderInfo{{ID: "ap_anth1", Name: "Relay", Agents: []string{"claude"}}}
	spawn := func(step map[string]any) string {
		t.Helper()
		h.createReq = nil
		say(t, s, wid, "start two more agents for the release notes")
		s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "", "steps": []any{step}}))
		if len(h.createReq) == 0 {
			t.Fatalf("nothing started for %v: %s", step, texts(CommandResponse{Messages: s.Threads.Last(p.ID, 3)}))
		}
		got := ""
		for _, r := range h.createReq {
			pid := ""
			if r.ProviderID != nil {
				pid = *r.ProviderID
			}
			if r.ProviderID != nil && r.Model != nil {
				t.Fatalf("a provider console got the login's model %q", *r.Model)
			}
			got += r.Agent + ":" + pid + " "
		}
		return strings.TrimSpace(got)
	}
	if got := spawn(map[string]any{"op": "spawn", "count": 2, "agent": "claude", "account": "Relay"}); got != "claude:ap_anth1 claude:ap_anth1" {
		t.Fatalf("named %s", got)
	}
	if got := spawn(map[string]any{"op": "spawn", "count": 1, "agent": "claude", "account": "ap_anth1"}); got != "claude:ap_anth1" {
		t.Fatalf("by id %s", got)
	}
	if got := spawn(map[string]any{"op": "spawn", "count": 1, "agent": "claude"}); got != "claude:" {
		t.Fatalf("no default %s", got)
	}
	h.set.Lead.Providers = map[string]string{"claude": "ap_anth1"}
	if got := spawn(map[string]any{"op": "spawn", "count": 1, "agent": "claude"}); got != "claude:ap_anth1" {
		t.Fatalf("default %s", got)
	}
	if got := spawn(map[string]any{"op": "spawn", "count": 1, "agent": "codex"}); got != "codex:" {
		t.Fatalf("other agent %s", got)
	}
	if got := spawn(map[string]any{"op": "spawn", "count": 1, "agent": "claude", "account": "login"}); got != "claude:" {
		t.Fatalf("login %s", got)
	}
	// an unknown provider starts nothing and says so
	h.createReq = nil
	say(t, s, wid, "start two more agents for the release notes")
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "", "steps": []any{map[string]any{"op": "spawn", "agent": "claude", "account": "Nope"}}}))
	if len(h.createReq) != 0 || !strings.Contains(texts(CommandResponse{Messages: s.Threads.Last(p.ID, 4)}), `no API provider "Nope"`) {
		t.Fatalf("unknown %v %s", h.createReq, texts(CommandResponse{Messages: s.Threads.Last(p.ID, 4)}))
	}
	// the planner sees the providers, never more than id, name and agents
	say(t, s, wid, "start two more agents for the release notes")
	if pr := h.lastRun(t, "planner").Prompt; !strings.Contains(pr, `"providers":[{"id":"ap_anth1","name":"Relay","agents":["claude"]}]`) {
		t.Fatalf("prompt has no providers")
	}
	// a typed order ("spawn a claude") takes the default too
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "", "steps": []any{}}))
	h.createReq = nil
	cmd(t, s, wid, "spawn 1 claude")
	if len(h.createReq) != 1 || h.createReq[0].ProviderID == nil || *h.createReq[0].ProviderID != "ap_anth1" {
		t.Fatalf("typed %+v", h.createReq)
	}
	// the spawn panel (not the lead) keeps the login
	h.createReq = nil
	if _, err := s.Spawn(SpawnRequest{WorkspaceID: wid, Count: 1, Agent: "claude"}); err != nil || len(h.createReq) != 1 || h.createReq[0].ProviderID != nil {
		t.Fatalf("panel %v %+v", err, h.createReq)
	}
}
