package kit

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSkillsAndMemory(t *testing.T) {
	k := New(t.TempDir())
	if err := k.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := k.PutSkill("Bad Name", "x", "y"); !errors.Is(err, ErrBadName) {
		t.Fatalf("bad name %v", err)
	}
	if _, err := k.PutSkill("release", "", "y"); err == nil {
		t.Fatal("a skill needs a description")
	}
	sk, err := k.PutSkill("release", "  How to check\n a release  ", "1. Run the tests.")
	if err != nil || sk.Description != "How to check a release" || sk.Body != "1. Run the tests." || !strings.HasSuffix(sk.Path, "/skills/release/SKILL.md") {
		t.Fatalf("put %+v %v", sk, err)
	}
	b, _ := os.ReadFile(sk.Path)
	if !strings.HasPrefix(string(b), "---\nname: release\ndescription: How to check a release\n---\n\n1. Run the tests.") {
		t.Fatalf("file %q", b)
	}
	if _, err := k.PutSkill("deploy", "How to deploy", "Ask first."); err != nil {
		t.Fatal(err)
	}
	if list := k.Skills(); len(list) != 2 || list[0].Name != "deploy" || list[0].Body != "" {
		t.Fatalf("list %+v", list)
	}
	if err := k.DeleteSkill("deploy"); err != nil || len(k.Skills()) != 1 {
		t.Fatalf("delete %v", err)
	}
	if err := k.DeleteSkill("deploy"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete again %v", err)
	}

	if _, err := k.MemoryPath("../x"); !errors.Is(err, ErrBadScope) {
		t.Fatal("scope")
	}
	if err := k.PutMemory("p_abcd1234", "- tests: make test"); err != nil {
		t.Fatal(err)
	}
	if err := k.PutMemory("global", "- answers in Indonesian"); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.Memory("p_abcd1234"); got != "- tests: make test\n" {
		t.Fatalf("memory %q", got)
	}
	if list := k.Memories(); len(list) != 2 || list[0].Scope != "global" {
		t.Fatalf("memories %+v", list)
	}
	if err := k.PutMemory("p_abcd1234", strings.Repeat("x", MaxMemoryBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large %v", err)
	}

	p := k.Prompt("p_abcd1234")
	for _, want := range []string{"# Skills", "- release: How to check a release (" + sk.Path + ")", "## Global", "- answers in Indonesian",
		"## This project", "- tests: make test", "add a short line to the project's file"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, p)
		}
	}
	if p := New(t.TempDir()).Prompt(""); strings.Contains(p, "# Skills") || strings.Contains(p, "This project") || !strings.Contains(p, "(empty)") {
		t.Fatalf("an empty kit's prompt:\n%s", p)
	}
	// an empty memory removes its file
	if err := k.PutMemory("global", "  "); err != nil || len(k.Memories()) != 1 {
		t.Fatalf("clear %v %+v", err, k.Memories())
	}
}
