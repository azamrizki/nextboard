package project

import (
	"errors"
	"strings"
	"testing"
)

func twoParts() Project {
	return Project{ID: "p_shop", Parts: []Part{
		{ID: "main", Name: "Main", MachineID: "local", Path: "/home/me/shop-api"},
		{ID: "pt_web", Name: "Web", MachineID: "m_srv", Path: "/srv/shop-web"},
	}}
}

// TestPartLookups: PartByID is exact and "" is no part; PartNamed takes an id or a name in any
// case, an id winning over another part's equal name; DefaultPart is the first in display order.
func TestPartLookups(t *testing.T) {
	p := twoParts()
	if pt, ok := p.PartByID("pt_web"); !ok || pt.Name != "Web" {
		t.Fatalf("by id %+v %v", pt, ok)
	}
	for _, id := range []string{"", "Web", "PT_WEB", "nope"} {
		if _, ok := p.PartByID(id); ok {
			t.Fatalf("PartByID(%q) matched", id)
		}
	}
	for in, want := range map[string]string{"pt_web": "pt_web", " web ": "pt_web", "MAIN": "main", "main": "main"} {
		if pt, ok := p.PartNamed(in); !ok || pt.ID != want {
			t.Fatalf("PartNamed(%q) = %+v %v, want %s", in, pt, ok, want)
		}
	}
	for _, in := range []string{"", "  ", "Api"} {
		if _, ok := p.PartNamed(in); ok {
			t.Fatalf("PartNamed(%q) matched", in)
		}
	}
	// a part named like another part's id: the id wins
	p.Parts[0].Name = "pt_web"
	if pt, _ := p.PartNamed("pt_web"); pt.ID != "pt_web" {
		t.Fatalf("id should win, got %+v", pt)
	}
	if d := p.DefaultPart(); d.ID != "main" {
		t.Fatalf("default %+v", d)
	}
	if d := (Project{}).DefaultPart(); d.ID != "" {
		t.Fatalf("default of none %+v", d)
	}
}

// TestCleanPartName: a part's name is safe as one folder name at the project root.
func TestCleanPartName(t *testing.T) {
	for in, want := range map[string]string{"Web": "Web", "  Api  ": "Api", "Admin · Docs": "Admin · Docs", "v1.2": "v1.2", "api-v2_x": "api-v2_x", "Main": "Main"} {
		if got, err := CleanPartName(in); err != nil || got != want {
			t.Fatalf("CleanPartName(%q) = %q %v", in, got, err)
		}
	}
	bad := []string{"", "   ", "a/b", `a\b`, ".", "..", ".x", ".nextboard", "agents.md", "CLAUDE.MD", "AGENTS.md", "a\x00b", "a\nb", strings.Repeat("x", MaxName+1)}
	for _, in := range bad {
		if got, err := CleanPartName(in); !errors.Is(err, ErrBadPartName) {
			t.Fatalf("CleanPartName(%q) = %q %v, want ErrBadPartName", in, got, err)
		}
	}
}

// TestResolvePlace: root or one part, never both; a single-part project's root is its part.
func TestResolvePlace(t *testing.T) {
	p := twoParts()
	if root, pt, ok, err := p.ResolvePlace(Place{Root: true}); err != nil || !ok || !root || pt.ID != "" {
		t.Fatalf("root %v %+v %v %v", root, pt, ok, err)
	}
	for _, in := range []string{"pt_web", "web", " WEB "} {
		if root, pt, ok, err := p.ResolvePlace(Place{Part: in}); err != nil || !ok || root || pt.ID != "pt_web" {
			t.Fatalf("part %q: %v %+v %v %v", in, root, pt, ok, err)
		}
	}
	if _, _, _, err := p.ResolvePlace(Place{Root: true, Part: "web"}); !errors.Is(err, ErrBadPlace) {
		t.Fatalf("both: %v", err)
	}
	if _, _, _, err := p.ResolvePlace(Place{Part: "api"}); !errors.Is(err, ErrPartNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if _, _, ok, err := p.ResolvePlace(Place{Part: "  "}); ok || err != nil || !(Place{Part: "  "}).IsZero() {
		t.Fatalf("zero: %v %v", ok, err)
	}
	one := Project{ID: "p_blog", Parts: p.Parts[1:]}
	if root, pt, ok, err := one.ResolvePlace(Place{Root: true}); err != nil || !ok || root || pt.ID != "pt_web" {
		t.Fatalf("single-part root %v %+v %v %v", root, pt, ok, err)
	}
	if _, _, _, err := (Project{}).ResolvePlace(Place{Root: true}); !errors.Is(err, ErrPartNotFound) {
		t.Fatalf("no part: %v", err)
	}
}

// TestPartKey: every part, the first included, is "<project>~<part>"; "" resolves to the default
// part only through Project.KeyOf.
func TestPartKey(t *testing.T) {
	p := twoParts()
	if k := PartKey(p.ID, "main"); k != "p_shop~main" {
		t.Fatal(k)
	}
	if k := PartKey(p.ID, ""); k != "p_shop" {
		t.Fatal(k)
	}
	if k := p.KeyOf(""); k != "p_shop~main" {
		t.Fatal(k)
	}
	if k := p.KeyOf("pt_web"); k != "p_shop~pt_web" {
		t.Fatal(k)
	}
	// reordering moves no key
	p.Parts[0], p.Parts[1] = p.Parts[1], p.Parts[0]
	if k := p.KeyOf("main"); k != "p_shop~main" {
		t.Fatal(k)
	}
	for in, want := range map[string][2]string{"p_shop~pt_web": {"p_shop", "pt_web"}, "p_shop": {"p_shop", ""}, "": {"", ""}} {
		if a, b := SplitPartKey(in); a != want[0] || b != want[1] {
			t.Fatalf("SplitPartKey(%q) = %q %q", in, a, b)
		}
	}
}
