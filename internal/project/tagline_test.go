package project

import (
	"errors"
	"strings"
	"testing"
)

// A tagline set by hand is one clean line, kept through a reload, and "" goes back to the cover's.
func TestTaglinePatch(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	p := addProject(t, s, "/a", "a")
	got, err := s.Patch(p.ID, Patch{Tagline: strp("  Game   top-ups\tand vouchers ")})
	if err != nil || got.Tagline != "Game top-ups and vouchers" {
		t.Fatalf("%q %v", got.Tagline, err)
	}
	if got := openStore(t, dir); got.List()[0].Tagline != "Game top-ups and vouchers" {
		t.Fatalf("not kept: %q", got.List()[0].Tagline)
	}
	for _, bad := range []string{strings.Repeat("x", MaxTagline+1), "a\x00b"} {
		if _, err := s.Patch(p.ID, Patch{Tagline: &bad}); !errors.Is(err, ErrBadTagline) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if got, err := s.Patch(p.ID, Patch{Tagline: strp("")}); err != nil || got.Tagline != "" {
		t.Fatalf("clear: %q %v", got.Tagline, err)
	}
	// a rename leaves it alone
	s.Patch(p.ID, Patch{Tagline: strp("Kept")})
	if got, _ := s.Patch(p.ID, Patch{Name: strp("b")}); got.Tagline != "Kept" {
		t.Fatalf("rename dropped it: %q", got.Tagline)
	}
}
