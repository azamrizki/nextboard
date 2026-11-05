package orch

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGit struct {
	mu     sync.Mutex
	calls  []string
	report func(string, bool)
}

func (g *fakeGit) GitStep(pid, repo, action, message string, report func(string, bool)) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	call := pid + ":" + action + ":" + message
	if repo != "" {
		call = repo + "@" + call
	}
	g.calls = append(g.calls, call)
	g.report = report
	if action == "push" {
		return "Pushing…", nil
	}
	return "Committed abc1234 “" + message + "”.", nil
}

func (g *fakeGit) list() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

// TestGitSteps: without a git service the phase 6 reply stays; typed git
// commands run at once; a planner git step needs a "risky" confirmation;
// async outcomes land in the thread.
func TestGitSteps(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	if r := cmd(t, s, wid, "push"); !strings.Contains(texts(r), "Git actions are not available yet.") {
		t.Fatalf("no git: %s", texts(r))
	}
	g := &fakeGit{}
	s.Git = g
	r := cmd(t, s, wid, "commit with the message Fix login")
	if r.Confirmation != nil || !strings.Contains(texts(r), "Committed abc1234 “Fix login”.") {
		t.Fatalf("typed commit %+v %s", r.Confirmation, texts(r))
	}
	if c := g.list(); len(c) != 1 || c[0] != p.ID+":commit:Fix login" {
		t.Fatalf("calls %v", c)
	}
	r = cmd(t, s, wid, "push")
	if r.Confirmation != nil || !strings.Contains(texts(r), "Pushing…") {
		t.Fatalf("typed push %s", texts(r))
	}
	g.report("Push failed: rejected", true)
	last := s.Threads.Last(p.ID, 1)
	if len(last) != 1 || last[0].Kind != "error" || last[0].Text != "Push failed: rejected" || last[0].Role != "hub" {
		t.Fatalf("report %+v", last)
	}
	// Planner-made git step → risky confirmation, nothing run yet.
	h.planner = true
	s.Command(CommandRequest{Text: "ship everything we did today", WorkspaceID: wid})
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "", "steps": []any{map[string]any{"op": "git", "action": "commitPush"}}}))
	cf, ok := s.Confirms.Pending(p.ID, t0)
	if !ok || cf.Kind != ConfRisky || len(g.list()) != 2 {
		t.Fatalf("planned git %+v %v", cf, g.list())
	}
	if _, err := s.Decide(cf.ID, "confirm", "", "admin"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(g.list()) != 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c := g.list(); len(c) != 3 || c[2] != p.ID+":commitPush:" {
		t.Fatalf("after confirm %v", c)
	}
	// Naming a repository, typed or planned.
	h.planner = false
	for text, want := range map[string]string{
		"repo app-a push":                                 "app-a@" + p.ID + ":push:",
		"repo group/app-b: commit and push":               "group/app-b@" + p.ID + ":commitPush:",
		`repo "my app" commit with the message Fix login`: "my app@" + p.ID + ":commit:Fix login",
		"repo . pull":                                     ".@" + p.ID + ":pull:",
	} {
		n := len(g.list())
		cmd(t, s, wid, text)
		if c := g.list(); len(c) != n+1 || c[n] != want {
			t.Fatalf("%q: calls %v", text, c)
		}
	}
	// "repo" before anything that is not a git command is no git step.
	n := len(g.list())
	cmd(t, s, wid, "repo app-a looks fine to me")
	if c := g.list(); len(c) != n {
		t.Fatalf("not a git command ran: %v", c)
	}
	h.planner = true
	s.Command(CommandRequest{Text: "ship app-a", WorkspaceID: wid})
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "", "steps": []any{map[string]any{"op": "git", "action": "push", "repo": "app-a"}}}))
	cf, ok = s.Confirms.Pending(p.ID, t0)
	if !ok || cf.Kind != ConfRisky {
		t.Fatalf("planned repo git %+v", cf)
	}
	n = len(g.list())
	if _, err := s.Decide(cf.ID, "confirm", "", "admin"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for len(g.list()) != n+1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c := g.list(); len(c) != n+1 || c[n] != "app-a@"+p.ID+":push:" {
		t.Fatalf("planned repo %v", c)
	}
}
