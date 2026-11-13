package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestPartsOfAProjectFromBefore: a project saved before parts gets its one part on open, and a
// client that knows nothing of parts still reads machineId and path.
func TestPartsOfAProjectFromBefore(t *testing.T) {
	dir := t.TempDir()
	old := `[{"id":"p_old","name":"Shop","machineId":"m_srv","path":"/srv/shop","workspaces":[{"id":"w_1","name":"Main"}]}]`
	if err := os.WriteFile(filepath.Join(dir, File), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir)
	p, ok := s.Get("p_old")
	if !ok || len(p.Parts) != 1 {
		t.Fatalf("parts %+v", p.Parts)
	}
	if pt := p.Parts[0]; pt.ID != MainPart || pt.Name != "Main" || pt.MachineID != "m_srv" || pt.Path != "/srv/shop" {
		t.Fatalf("main part %+v", pt)
	}
	if first, ok := p.Part(""); !ok || first.ID != MainPart {
		t.Fatalf("first part %+v", first)
	}
}

func TestParts(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	p := addProject(t, s, "/srv/shop-api", "Shop")
	other := addProject(t, s, "/home/me/blog", "Blog")

	// a part on another machine
	p, web, err := s.AddPart(p.ID, "Frontend", "m_web", "/home/me/shop-web")
	if err != nil || len(p.Parts) != 2 || web.MachineID != "m_web" || p.MachineID != "local" {
		t.Fatalf("add %v %+v", err, p.Parts)
	}
	if got, ok := p.PartAt("m_web", "/home/me/shop-web/src/app"); !ok || got.ID != web.ID {
		t.Fatalf("part at %+v %v", got, ok)
	}
	if _, ok := p.PartAt("m_web", "/home/me/shop-webby"); ok {
		t.Fatal("a sibling folder with a longer name is not inside the part")
	}

	for _, c := range []struct {
		name, machine, dir string
		want               error
	}{
		{"frontend", "m_api", "/opt/x", ErrPartName},                      // names differ only in case
		{"Docs", "local", "/srv/shop-api/docs", ErrPartOverlap},           // inside a part
		{"Root", "local", "/srv", ErrPartOverlap},                         // holds a part
		{"Same", "m_web", "/home/me/shop-web", ErrPartOverlap},            // the same folder
		{"", "m_api", "/opt/x", ErrBadName},                               // no name
		{"Way too long a name for a part", "m_api", "/opt/x", ErrBadName}, // over MaxName
	} {
		if _, _, err := s.AddPart(p.ID, c.name, c.machine, c.dir); !errors.Is(err, c.want) {
			t.Errorf("%q %s:%s: %v, want %v", c.name, c.machine, c.dir, err, c.want)
		}
	}
	// another project's folder: named, so the owner knows where it is
	var in *InProjectError
	if _, _, err := s.AddPart(p.ID, "Blog", "local", "/home/me/blog"); !errors.As(err, &in) || in.ProjectID != other.ID || in.Name != "Blog" {
		t.Fatalf("other project %v", err)
	}
	// the same folder on another machine is another folder
	if _, _, err := s.AddPart(p.ID, "Gateway", "m_api", "/srv/shop-api"); err != nil {
		t.Fatal(err)
	}
	// adding a part's folder as a project finds the project it is in
	if got, created, err := s.Create("m_web", "/home/me/shop-web", ""); err != nil || created || got.ID != p.ID {
		t.Fatalf("create of a part %v %v %s", err, created, got.ID)
	}

	// rename, read-only, and move one to the front: it becomes the project's own folder
	name, ro, front := "Web", true, 0
	p, err = s.PatchPart(p.ID, web.ID, PartPatch{Name: &name, ReadOnly: &ro, Index: &front})
	if err != nil || p.Parts[0].ID != web.ID || p.Parts[0].Name != "Web" || !p.Parts[0].ReadOnly || p.MachineID != "m_web" || p.Path != "/home/me/shop-web" {
		t.Fatalf("patch %v %+v %s:%s", err, p.Parts, p.MachineID, p.Path)
	}
	if p.Parts[1].ID != MainPart || p.Parts[2].Name != "Gateway" {
		t.Fatalf("order %+v", p.Parts)
	}
	dup := "gateway"
	if _, err := s.PatchPart(p.ID, web.ID, PartPatch{Name: &dup}); !errors.Is(err, ErrPartName) {
		t.Fatalf("rename to a taken name %v", err)
	}

	// the first part stays; another goes, and its folder is free again
	if _, _, err := s.DeletePart(p.ID, web.ID); !errors.Is(err, ErrMainPart) {
		t.Fatalf("delete first %v", err)
	}
	if p, _, err = s.DeletePart(p.ID, MainPart); err != nil || len(p.Parts) != 2 {
		t.Fatalf("delete %v %+v", err, p.Parts)
	}
	if _, _, err := s.DeletePart(p.ID, "pt_none"); !errors.Is(err, ErrPartNotFound) {
		t.Fatalf("delete unknown %v", err)
	}

	// saved: a new store reads the same parts
	again, _ := openStore(t, dir).Get(p.ID)
	if len(again.Parts) != 2 || again.Parts[0].ID != web.ID || !again.Parts[0].ReadOnly || again.MachineID != "m_web" {
		t.Fatalf("reloaded %+v", again)
	}
}

func TestPartsLimit(t *testing.T) {
	s := openStore(t, t.TempDir())
	p := addProject(t, s, "/srv/p0", "Many")
	for i := 1; i < MaxParts; i++ {
		if _, _, err := s.AddPart(p.ID, "Part "+string(rune('A'+i)), "local", "/srv/p"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.AddPart(p.ID, "One more", "local", "/srv/more"); !errors.Is(err, ErrTooManyParts) {
		t.Fatalf("limit %v", err)
	}
}
