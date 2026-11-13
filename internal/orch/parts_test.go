package orch

import (
	"strings"
	"testing"
)

// TestLeadKnowsParts: the lead sees the parts of a project in several and the part of each
// console, starts consoles in the part it names, and so does a typed order (ROADMAP #49).
func TestLeadKnowsParts(t *testing.T) {
	h, s := newFakeHub(t)
	h.set.Lead.TalkFirst = true
	p, wid := h.project("shop")
	p, web, err := h.projects.AddPart(p.ID, "Frontend", "local", "/work/shop-web")
	if err != nil {
		t.Fatal(err)
	}
	if p, _, err = h.projects.AddPart(p.ID, "Ops", "local", "/work/shop-ops"); err != nil {
		t.Fatal(err)
	}
	c := h.console(p, wid, "Ayu", "claude", "idle")
	h.mu.Lock()
	h.sessions[c.ID].PartID = web.ID
	h.mu.Unlock()

	say(t, s, wid, "fix the checkout across the backend and the frontend please")
	pr := h.lastRun(t, "planner").Prompt
	for _, want := range []string{`"parts":[{"machine":"This PC","name":"Main","path":"/work/shop"}`, `"name":"Frontend"`, `"handle":"Ayu"`, `"part":"Frontend"`, `A spawn step for one part sets "part"`} {
		if !strings.Contains(pr, want) {
			t.Fatalf("no %s in the planner prompt", want)
		}
	}
	// a spawn step names a part, in any case: the console works in that folder
	h.createReq = nil
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "", "steps": []any{map[string]any{"op": "spawn", "count": 1, "agent": "claude", "part": "frontend"}}}))
	if len(h.createReq) != 1 || h.createReq[0].PartID != web.ID || h.createReq[0].Cwd != "/work/shop-web" {
		t.Fatalf("spawn in a part %+v", h.createReq)
	}
	// a part the project does not have starts nothing and says so
	h.createReq = nil
	say(t, s, wid, "start one more for the gateway please")
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "", "steps": []any{map[string]any{"op": "spawn", "agent": "claude", "part": "Gateway"}}}))
	if len(h.createReq) != 0 || !strings.Contains(texts(CommandResponse{Messages: s.Threads.Last(p.ID, 4)}), "no such part") {
		t.Fatalf("unknown part %+v %s", h.createReq, texts(CommandResponse{Messages: s.Threads.Last(p.ID, 4)}))
	}

	// typed: "spawn 2 claude in Frontend"
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "", "steps": []any{}}))
	h.createReq = nil
	cmd(t, s, wid, "spawn 2 claude in frontend")
	if len(h.createReq) != 2 || h.createReq[0].PartID != web.ID || h.createReq[1].Cwd != "/work/shop-web" {
		t.Fatalf("typed %+v", h.createReq)
	}
}

func TestParseSpawnInPart(t *testing.T) {
	ctx := ParseContext{Agents: []string{"claude", "codex", "shell"}, Parts: []string{"Backend", "Web App"}}
	for text, want := range map[string]Intent{
		"spawn 2 codex in backend":       {Kind: IntentSpawn, Count: 2, Agent: "codex", Part: "Backend"},
		"spawn a claude on Web App part": {Kind: IntentSpawn, Count: 1, Agent: "claude", Part: "Web App"},
		"spawn claude":                   {Kind: IntentSpawn, Count: 1, Agent: "claude"},
		"spawn 3 reviewers in backend":   {Kind: IntentSpawn, Count: 3, Role: RoleOf("reviewers"), Part: "Backend"},
	} {
		got := Parse(text, ctx)
		if got.Kind != want.Kind || got.Count != want.Count || got.Agent != want.Agent || got.Part != want.Part || got.Role != want.Role {
			t.Errorf("%q: %+v", text, got)
		}
	}
	if got := Parse("spawn 2 codex in frontend", ctx); got.Kind == IntentSpawn {
		t.Errorf("an unknown part is not a spawn: %+v", got)
	}
}
