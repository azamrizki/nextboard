package orch

import (
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/store"
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

// TestThreadsMerge: a merged project's thread joins the other's in time order, with its replies.
func TestThreadsMerge(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	th := newThreads(st)
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	th.Add(ThreadMessage{ThreadID: "p_aaaaaaaaaa", Role: "user", Kind: "order", Text: "a1", At: at})
	th.Add(ThreadMessage{ThreadID: "p_aaaaaaaaaa", Role: "user", Kind: "order", Text: "a2", At: at.Add(2 * time.Minute)})
	b := th.Add(ThreadMessage{ThreadID: "p_bbbbbbbbbb", Role: "agent", Kind: "report", Text: "b1", At: at.Add(time.Minute), Report: &Report{State: TFinished, Chars: 15}})
	th.SaveOutput(ReplyOutput{MessageID: b.ID, ThreadID: "p_bbbbbbbbbb", Text: "the whole reply"})
	if n := th.Merge("p_aaaaaaaaaa", "p_bbbbbbbbbb"); n != 1 {
		t.Fatalf("moved %d", n)
	}
	var got []string
	for _, m := range th.Last("p_aaaaaaaaaa", 10) {
		got = append(got, m.Text+"@"+m.ThreadID)
	}
	if strings.Join(got, " ") != "a1@p_aaaaaaaaaa b1@p_aaaaaaaaaa a2@p_aaaaaaaaaa" {
		t.Fatalf("order %v", got)
	}
	if o, err := th.Output("p_aaaaaaaaaa", b.ID); err != nil || o.Text != "the whole reply" {
		t.Fatalf("reply %+v %v", o, err)
	}
	if len(th.Last("p_bbbbbbbbbb", 10)) != 0 {
		t.Fatal("the merged thread is not empty")
	}
	// read back from disk too
	again := newThreads(st)
	if len(again.Last("p_aaaaaaaaaa", 10)) != 3 || len(again.Last("p_bbbbbbbbbb", 10)) != 0 {
		t.Fatalf("on disk %v %v", again.Last("p_aaaaaaaaaa", 10), again.Last("p_bbbbbbbbbb", 10))
	}
}
