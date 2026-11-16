package orch

import (
	"errors"
	"strings"
	"testing"
)

// An agent asked to start a page on a topic can: memory_save makes the page it names, and finds a
// page by its title whatever the case.
func TestAgentMakesAPage(t *testing.T) {
	h, s := newFakeHub(t)
	p, _ := h.project("shop")
	page, made, err := s.SaveMemoryFactTo(p.ID, "commands", "GET /health answers 200 when the API is up", "API", "API", "The API: endpoints, auth, how to run it. Read before changing an endpoint.")
	if err != nil || !made || page != "api" {
		t.Fatalf("made %q %v %v", page, made, err)
	}
	sub, err := s.Memory.Sub(p.ID, "api")
	if err != nil || sub.Title != "API" || !strings.Contains(sub.Description, "endpoints") || !strings.Contains(sub.Content, "## Commands\n- GET /health") {
		t.Fatalf("page %+v %v", sub, err)
	}
	// again, by name or title: the same page, not a new one
	if page, made, err = s.SaveMemoryFactTo(p.ID, "gotchas", "tokens expire after an hour", "api", "", ""); err != nil || made || page != "api" {
		t.Fatalf("again %q %v %v", page, made, err)
	}
	if !strings.Contains(s.memoryIndex(p.ID), "- api: API. The API: endpoints") {
		t.Fatalf("index %s", s.memoryIndex(p.ID))
	}
	// critical notes and the memory of all projects stay the owner's; a name with nothing in it is no page
	for _, bad := range []string{"critical", "global/api", "!!!"} {
		if _, _, err := s.SaveMemoryFactTo(p.ID, "context", "x y z", bad, "", ""); err == nil {
			t.Fatalf("%q was allowed", bad)
		}
	}
}

// memory_move takes facts out of the main memory and into a page, each under its heading, and
// leaves no copy behind; a fact it cannot find is said, not guessed.
func TestAgentMovesFactsToAPage(t *testing.T) {
	h, s := newFakeHub(t)
	p, _ := h.project("shop")
	main := "## Architecture\n- [api] Laravel 11 serves the API under /api\n- the web is Vue 3\n\n## Commands\n- [api] php artisan test runs the API tests\n"
	if _, err := s.WriteMemory(p.ID, "project", main, ""); err != nil {
		t.Fatal(err)
	}
	res, err := s.MoveMemoryFacts(p.ID, "", "api", "API", "The API part: what holds for it only.",
		[]string{"[api] Laravel 11 serves the API under /api", "php artisan test runs", "nothing like this"})
	if err != nil || len(res.Moved) != 2 || len(res.Missing) != 1 || !res.Made || res.To != "api" || res.From != MainDoc {
		t.Fatalf("move %+v %v", res, err)
	}
	after, _ := s.Memory.Read("project", p.ID)
	if strings.Contains(after.Content, "[api]") || !strings.Contains(after.Content, "the web is Vue 3") || strings.Contains(after.Content, "## Commands") {
		t.Fatalf("main after %q", after.Content)
	}
	sub, _ := s.Memory.Sub(p.ID, "api")
	if !strings.Contains(sub.Content, "## Architecture\n- [api] Laravel 11") || !strings.Contains(sub.Content, "## Commands\n- [api] php artisan test") {
		t.Fatalf("page %q", sub.Content)
	}
	// nothing of it there: an error, and nothing changes
	if _, err := s.MoveMemoryFacts(p.ID, "", "api", "", "", []string{"not in the memory at all"}); !errors.Is(err, ErrFactNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// and back: from the page into the main memory
	if res, err := s.MoveMemoryFacts(p.ID, "api", "", "", "", []string{"php artisan test runs"}); err != nil || len(res.Moved) != 1 {
		t.Fatalf("back %+v %v", res, err)
	}
	if after, _ = s.Memory.Read("project", p.ID); !strings.Contains(after.Content, "php artisan test") {
		t.Fatalf("main %q", after.Content)
	}
	if _, err := s.MoveMemoryFacts(p.ID, "critical", "api", "", "", []string{"Never"}); err == nil {
		t.Fatal("critical notes moved")
	}
}
