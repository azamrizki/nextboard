package orch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nextboard/internal/agentstate"
)

// One project has one memory: the same folder for the panel, every CLI and the
// planner.
func TestOneProjectOneMemory(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	dir := h.st.Path("memory", p.ID)
	file := filepath.Join(dir, "MEMORY.md")

	// A memory from before there was a folder moves in.
	if err := os.WriteFile(h.st.Path("memory", p.ID+".md"), []byte("## Context\n- old fact\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := s.ReadMemory(p.ID)
	if err != nil || !strings.Contains(v.Project.Content, "old fact") {
		t.Fatalf("view %+v %v", v, err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("did not move in: %v", err)
	}

	// The panel and the memory_save tool write the same file.
	if _, err := s.WriteMemory(p.ID, "project", v.Project.Content+"- from the panel\n", v.Project.Mtime); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMemoryFact(p.ID, "Commands", "the build command is make zebra", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	for _, want := range []string{"old fact", "from the panel", "## Commands", "make zebra"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("lost %q:\n%s", want, b)
		}
	}
	if err := s.SaveMemoryFact(p.ID, "", "   ", ""); err == nil {
		t.Fatal("an empty fact was saved")
	}

	// What Claude Code writes there directly reaches everyone else.
	note := "---\nname: deploy-day\ntype: project\n---\n\nDeploys happen on Friday.\n"
	if err := os.WriteFile(filepath.Join(dir, "deploy-day.md"), []byte(note), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "not-a-note.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.ReadMemory(p.ID); len(v.Notes) != 1 || v.Notes[0].Name != "deploy-day.md" || strings.Contains(v.Notes[0].Content, "name:") {
		t.Fatalf("notes %+v", v.Notes)
	}
	if _, err := s.WriteMemory("", "global", "## Conventions\n- tabs everywhere\n", ""); err != nil {
		t.Fatal(err)
	}

	// A CLI without an auto memory gets all of it at launch, framed as background.
	prompt := s.MemoryPrompt(p.ID, false)
	for _, want := range []string{"make zebra", "Deploys happen on Friday", "tabs everywhere", "memory_save", "not a task"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	// Claude Code reads the folder itself: only what lives outside it.
	prompt = s.MemoryPrompt(p.ID, true)
	if strings.Contains(prompt, "make zebra") || !strings.Contains(prompt, "tabs everywhere") {
		t.Fatalf("native prompt:\n%s", prompt)
	}

	// The planner plans with it.
	c := &execCtx{threadID: p.ID, workspaceID: wid}
	sc, err := s.newScope(wid, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s.plannerSnapshot(c, sc, "x"))
	if !strings.Contains(string(raw), "make zebra") {
		t.Fatalf("planner input:\n%s", raw)
	}

	// Closing and opening orchestrator cards does not touch it.
	if v, _ := s.ReadMemory(p.ID); !strings.Contains(v.Project.Content, "make zebra") {
		t.Fatal("memory lost")
	}

	// Another project has its own.
	q, _ := h.project("other")
	if v, _ := s.ReadMemory(q.ID); v.Project.Content != "" || len(v.Notes) != 0 {
		t.Fatalf("memory leaked between projects: %+v", v)
	}
}

// What a login's Claude Code learned before moves in, once, without losing
// anything on either side.
func TestAdoptClaudeMemory(t *testing.T) {
	h, s := newFakeHub(t)
	p, _ := h.project("app")
	src := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "MEMORY.md"), []byte("- [Deploy](deploy.md): friday\n- shared line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "deploy.md"), []byte("Friday.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Memory.Dir(p.ID)
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- shared line\n- mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Memory.Adopt(src, p.ID); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if string(b) != "- shared line\n- mine\n- [Deploy](deploy.md): friday\n" {
		t.Fatalf("merged:\n%s", b)
	}
	if c, _ := os.ReadFile(filepath.Join(dir, "deploy.md")); string(c) != "Friday.\n" {
		t.Fatalf("note %q", c)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("the source must be renamed, so this happens once")
	}
	if err := s.Memory.Adopt(src, p.ID); err != nil {
		t.Fatalf("second adopt: %v", err)
	}
}

// A CLI that got the memory at launch is not typed a briefing as well.
func TestAutoPushOnlyForCLIsWithoutContext(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	if _, err := s.WriteMemory(p.ID, "project", "## Context\n- fact\n", ""); err != nil {
		t.Fatal(err)
	}
	h.set.Memory.AutoPush = true
	claude := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	s.autoPush(claude)
	if len(h.delReqs) != 0 {
		t.Fatalf("typed a briefing: %+v", h.delReqs)
	}
}
