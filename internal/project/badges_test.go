package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/store"
)

func TestBadgeValidation(t *testing.T) {
	s := openStore(t, t.TempDir())
	b, err := s.CreateBadge("  PROD ", "red", "shield")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "PROD" || b.Color != "red" || b.Icon != "shield" || !strings.HasPrefix(b.ID, "b_") || b.CreatedAt.IsZero() {
		t.Fatalf("%+v", b)
	}
	for _, c := range []struct {
		name, color, icon string
		want              error
	}{
		{"", "red", "", ErrBadBadgeName},
		{"   ", "red", "", ErrBadBadgeName},
		{strings.Repeat("x", MaxBadgeName+1), "red", "", ErrBadBadgeName},
		{"a,b", "red", "", ErrBadBadgeName},
		{"a\tb", "red", "", ErrBadBadgeName},
		{"ok", "", "", ErrBadBadgeColor},
		{"ok", "Red", "", ErrBadBadgeColor},
		{"ok", "#ff0000", "", ErrBadBadgeColor},
		{"ok", "red", "1shield", ErrBadBadgeIcon},
		{"ok", "red", "shield-check", ErrBadBadgeIcon},
		{"ok", "red", strings.Repeat("a", MaxBadgeIcon+1), ErrBadBadgeIcon},
		{"prod", "blue", "", ErrBadgeNameTaken},
	} {
		if _, err := s.CreateBadge(c.name, c.color, c.icon); !errors.Is(err, c.want) {
			t.Errorf("%q %q %q: %v, want %v", c.name, c.color, c.icon, err, c.want)
		}
	}
	// 12 runes, not bytes; no icon is fine
	if _, err := s.CreateBadge("ÄÖÜäöüÄÖÜäöü", "gray", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBadge("Icon", "teal", "Ab"+strings.Repeat("1", MaxBadgeIcon-2)); err != nil {
		t.Fatal(err)
	}
	for len(s.Badges()) < MaxBadges {
		if _, err := s.CreateBadge(randomID("x")[:8], "blue", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateBadge("one more", "blue", ""); !errors.Is(err, ErrTooManyBadges) {
		t.Fatalf("over the limit: %v", err)
	}
}

func TestUpdateBadge(t *testing.T) {
	s := openStore(t, t.TempDir())
	a, _ := s.CreateBadge("PROD", "red", "shield")
	b, _ := s.CreateBadge("Dev", "green", "")
	if _, err := s.UpdateBadge(b.ID, strp("prod"), nil, nil); !errors.Is(err, ErrBadgeNameTaken) {
		t.Fatalf("taken: %v", err)
	}
	// its own name in another case is no conflict
	got, err := s.UpdateBadge(a.ID, strp("Prod"), nil, strp(""))
	if err != nil || got.Name != "Prod" || got.Color != "red" || got.Icon != "" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.UpdateBadge(a.ID, nil, strp("black"), nil); !errors.Is(err, ErrBadBadgeColor) {
		t.Fatalf("color: %v", err)
	}
	if _, err := s.UpdateBadge(a.ID, nil, nil, strp("a b")); !errors.Is(err, ErrBadBadgeIcon) {
		t.Fatalf("icon: %v", err)
	}
	if _, err := s.UpdateBadge("b_none", nil, strp("red"), nil); !errors.Is(err, ErrBadgeNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// sorted by name regardless of case
	list := s.Badges()
	if len(list) != 2 || list[0].Name != "Dev" || list[1].Name != "Prod" {
		t.Fatalf("%+v", list)
	}
}

func TestBadgeOnProjects(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if _, err := os.Stat(filepath.Join(dir, BadgesFile)); !os.IsNotExist(err) {
		t.Fatalf("no file until a badge is made: %v", err)
	}
	if len(s.Badges()) != 0 {
		t.Fatal(s.Badges())
	}
	b, _ := s.CreateBadge("PROD", "red", "shield")
	keep, _ := s.CreateBadge("Dev", "green", "")
	p1 := addProject(t, s, "/a", "a")
	p2 := addProject(t, s, "/b", "b")
	p3 := addProject(t, s, "/c", "c")
	if _, err := s.Patch(p1.ID, Patch{BadgeID: strp("b_none")}); !errors.Is(err, ErrBadgeNotFound) {
		t.Fatalf("unknown badge: %v", err)
	}
	for _, id := range []string{p1.ID, p2.ID} {
		if got, err := s.Patch(id, Patch{BadgeID: &b.ID}); err != nil || got.BadgeID != b.ID {
			t.Fatalf("%+v %v", got, err)
		}
	}
	s.Patch(p3.ID, Patch{BadgeID: &keep.ID})
	// "" takes it off, nil leaves it
	if got, _ := s.Patch(p2.ID, Patch{BadgeID: strp("")}); got.BadgeID != "" {
		t.Fatalf("%+v", got)
	}
	if got, _ := s.Patch(p1.ID, Patch{Name: strp("a2")}); got.BadgeID != b.ID {
		t.Fatalf("%+v", got)
	}
	s.Patch(p2.ID, Patch{BadgeID: &b.ID})

	cleared, err := s.DeleteBadge(b.ID)
	if err != nil || len(cleared) != 2 {
		t.Fatalf("%v %v", cleared, err)
	}
	if _, err := s.DeleteBadge(b.ID); !errors.Is(err, ErrBadgeNotFound) {
		t.Fatalf("twice: %v", err)
	}
	s = openStore(t, dir)
	for id, want := range map[string]string{p1.ID: "", p2.ID: "", p3.ID: keep.ID} {
		if got, _ := s.Get(id); got.BadgeID != want {
			t.Errorf("%s: %q, want %q", id, got.BadgeID, want)
		}
	}
	if list := s.Badges(); len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("%+v", list)
	}
}

func TestRepairBadges(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(BadgesFile, []*Badge{{ID: "b_ok", Name: "OK", Color: "green"}, nil, {Name: "no id"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(File, []*Project{
		{ID: "p_a", Name: "a", Path: "/a", BadgeID: "b_gone"},
		{ID: "p_b", Name: "b", Path: "/b", BadgeID: "b_ok"},
	}); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir)
	if got, _ := s.Get("p_a"); got.BadgeID != "" {
		t.Fatalf("%+v", got)
	}
	if got, _ := s.Get("p_b"); got.BadgeID != "b_ok" {
		t.Fatalf("%+v", got)
	}
	if list := s.Badges(); len(list) != 1 {
		t.Fatalf("%+v", list)
	}
}

func TestMergeKeepsOneBadge(t *testing.T) {
	s := openStore(t, t.TempDir())
	red, _ := s.CreateBadge("PROD", "red", "")
	green, _ := s.CreateBadge("Dev", "green", "")
	a := addProject(t, s, "/a", "a")
	b := addProject(t, s, "/b", "b")
	c := addProject(t, s, "/c", "c")
	s.Patch(a.ID, Patch{BadgeID: &red.ID})
	s.Patch(b.ID, Patch{BadgeID: &green.ID})
	s.Patch(c.ID, Patch{BadgeID: &green.ID})
	got, _, _, err := s.Merge(a.ID, b.ID)
	if err != nil || got.BadgeID != red.ID {
		t.Fatalf("the target's stays: %+v %v", got, err)
	}
	d := addProject(t, s, "/d", "d")
	if got, _, _, err = s.Merge(d.ID, c.ID); err != nil || got.BadgeID != green.ID {
		t.Fatalf("one without takes the merged one's: %+v %v", got, err)
	}
}

func strp(s string) *string { return &s }
