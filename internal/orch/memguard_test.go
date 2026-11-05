package orch

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestBlockedLists(t *testing.T) {
	h, s := newFakeHub(t)
	p, _ := h.project("app")
	other, _ := h.project("acme")

	// The first start seeds what the first critical notes forbid, for all projects.
	if got := s.BlockedList(""); !slices.Contains(got, "git reset --hard") || !slices.Contains(got, "*db:reset*") {
		t.Fatalf("seed %v", got)
	}
	if got := s.BlockedList(p.ID); len(got) != 0 {
		t.Fatalf("project %v", got)
	}

	list, err := s.SetBlocked(p.ID, []string{"  npm  run deploy ", "", "npm run deploy", "git reset --hard"})
	if err != nil || strings.Join(list, "|") != "npm run deploy|git reset --hard" {
		t.Fatalf("%v %v", list, err)
	}
	both := s.BlockedCommands(p.ID)
	if !slices.Contains(both, "npm run deploy") || !slices.Contains(both, "git push --force") {
		t.Fatalf("both %v", both)
	}
	if n := 0; true {
		for _, x := range both {
			if x == "git reset --hard" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("twice: %v", both)
		}
	}
	if slices.Contains(s.BlockedCommands(other.ID), "npm run deploy") {
		t.Fatal("reached another project")
	}
	if !slices.Contains(s.AllBlocked(), "npm run deploy") {
		t.Fatal("the shared list lost it")
	}

	for _, bad := range [][]string{{"*"}, {"* *"}, {"a"}, {strings.Repeat("x", maxBlockedChar+1)}, make64()} {
		if _, err := s.SetBlocked(p.ID, bad); !errors.Is(err, ErrBadBlocked) {
			t.Fatalf("%.30v: %v", bad, err)
		}
	}
	if !slices.Contains(s.BlockedList(p.ID), "npm run deploy") {
		t.Fatal("a refused list changed the old one")
	}

	// Emptied stays empty, also after a restart: the seed is once.
	if _, err := s.SetBlocked("", nil); err != nil {
		t.Fatal(err)
	}
	s2 := afterRestart(h, s)
	if got := s2.BlockedList(""); len(got) != 0 {
		t.Fatalf("seeded again: %v", got)
	}
	if got := s2.BlockedList(p.ID); !slices.Contains(got, "npm run deploy") {
		t.Fatalf("after restart %v", got)
	}

	if s2.BlockedAnswer(p.ID, "git status") != "" {
		t.Fatal("blocked a harmless command")
	}
	if got := s2.BlockedAnswer(p.ID, "cd web && npm run deploy -- --prod"); !strings.Contains(got, "`npm run deploy`") || !strings.Contains(got, "Not run") {
		t.Fatalf("answer %q", got)
	}
}

func make64() []string {
	out := make([]string, maxBlocked+1)
	for i := range out {
		out[i] = "cmd" + itoa(i)
	}
	return out
}
