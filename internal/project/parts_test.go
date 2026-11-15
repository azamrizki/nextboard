package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPartsOfAProjectFromBefore: a project saved before parts gets its one part on open (id
// "main", named after its folder), which is saved; a client that knows nothing of parts still
// reads machineId and path, and opening again changes nothing.
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
	if pt := p.Parts[0]; pt.ID != MainPart || pt.Name != "shop" || pt.MachineID != "m_srv" || pt.Path != "/srv/shop" {
		t.Fatalf("its part %+v", pt)
	}
	if d := p.DefaultPart(); d.ID != MainPart || p.MachineID != "m_srv" || p.Path != "/srv/shop" {
		t.Fatalf("default part %+v, project %s:%s", d, p.MachineID, p.Path)
	}
	saved, err := os.ReadFile(filepath.Join(dir, File))
	if err != nil || !strings.Contains(string(saved), `"parts"`) {
		t.Fatalf("not saved: %s %v", saved, err)
	}
	if again, _ := openStore(t, dir).Get("p_old"); again.Parts[0].Name != "shop" || again.Parts[0].ID != MainPart {
		t.Fatalf("reopened %+v", again.Parts)
	}
	if now, _ := os.ReadFile(filepath.Join(dir, File)); string(now) != string(saved) {
		t.Fatalf("a second open changed projects.json:\n%s\n%s", saved, now)
	}
}

// TestOldPartNames: data from before equal parts. A one-part project's {main, "Main"} is named
// after its folder, a project in several keeps "Main", names that are not safe as a folder name
// are made safe and reported once, ids never change, and a second open changes nothing. An owner
// who names the part "Main" again keeps that name.
func TestOldPartNames(t *testing.T) {
	dir := t.TempDir()
	old := `[
{"id":"p_solo","name":"Solo","machineId":"local","path":"/home/me/acme-shop","parts":[{"id":"main","name":"Main","machineId":"local","path":"/home/me/acme-shop"}],"workspaces":[{"id":"w_1","name":"Main"}]},
{"id":"p_many","name":"Many","machineId":"local","path":"/srv/api","parts":[
 {"id":"main","name":"Main","machineId":"local","path":"/srv/api"},
 {"id":"pt_a","name":"web/admin","machineId":"local","path":"/srv/web"},
 {"id":"pt_b","name":"..","machineId":"devbox","path":"/srv/b"},
 {"id":"pt_c","name":"agents.md","machineId":"devbox","path":"/srv/c"},
 {"id":"pt_d","name":".hidden","machineId":"devbox","path":"/srv/d"},
 {"id":"pt_e","name":"web-admin","machineId":"devbox","path":"/srv/e"}],"workspaces":[{"id":"w_2","name":"Main"}]},
{"id":"p_root","name":"Root","machineId":"local","path":"/","parts":[{"id":"main","name":"Main","machineId":"local","path":"/"}],"workspaces":[{"id":"w_3","name":"Main"}]}
]`
	if err := os.WriteFile(filepath.Join(dir, File), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir)
	names := func(id string) string {
		p, _ := s.Get(id)
		var out []string
		for _, pt := range p.Parts {
			out = append(out, pt.ID+"="+pt.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names("p_solo"); got != "main=acme-shop" {
		t.Fatalf("one part %s", got)
	}
	if got := names("p_root"); got != "main=Part" {
		t.Fatalf("the root folder %s", got)
	}
	// "web/admin" becomes "web-admin", which another part has: the renamed one takes a number
	if got := names("p_many"); got != "main=Main,pt_a=web-admin 2,pt_b=Part,pt_c=agents.md part,pt_d=hidden,pt_e=web-admin" {
		t.Fatalf("several parts %s", got)
	}
	for _, p := range s.List() {
		for _, pt := range p.Parts {
			if _, err := CleanPartName(pt.Name); err != nil {
				t.Errorf("%s: %q is still unsafe", p.ID, pt.Name)
			}
		}
	}
	renamed := s.TakePartRenames()
	unsafe := map[string]bool{}
	for _, r := range renamed {
		unsafe[r.ProjectID+"/"+r.PartID] = r.Unsafe
	}
	if len(renamed) != 6 || unsafe["p_solo/main"] || unsafe["p_root/main"] || !unsafe["p_many/pt_a"] || !unsafe["p_many/pt_b"] || !unsafe["p_many/pt_c"] || !unsafe["p_many/pt_d"] {
		t.Fatalf("renames %+v", renamed)
	}
	if again := s.TakePartRenames(); len(again) != 0 {
		t.Fatalf("reported twice %+v", again)
	}

	// a second open: nothing to do, nothing saved
	saved, _ := os.ReadFile(filepath.Join(dir, File))
	s2 := openStore(t, dir)
	if r := s2.TakePartRenames(); len(r) != 0 {
		t.Fatalf("second open renamed %+v", r)
	}
	if now, _ := os.ReadFile(filepath.Join(dir, File)); string(now) != string(saved) {
		t.Fatal("a second open changed projects.json")
	}
	// the owner names it Main again: it stays Main
	main := "Main"
	if _, err := s2.PatchPart("p_solo", MainPart, PartPatch{Name: &main}); err != nil {
		t.Fatal(err)
	}
	s3 := openStore(t, dir)
	if p, _ := s3.Get("p_solo"); p.Parts[0].Name != "Main" || len(s3.TakePartRenames()) != 0 {
		t.Fatalf("renamed again %+v", p.Parts)
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
		{"frontend", "m_api", "/opt/x", ErrPartName},                          // names differ only in case
		{"Docs", "local", "/srv/shop-api/docs", ErrPartOverlap},               // inside a part
		{"Root", "local", "/srv", ErrPartOverlap},                             // holds a part
		{"Same", "m_web", "/home/me/shop-web", ErrPartOverlap},                // the same folder
		{"", "m_api", "/opt/x", ErrBadPartName},                               // no name
		{"Way too long a name for a part", "m_api", "/opt/x", ErrBadPartName}, // over MaxName
		{"a/b", "m_api", "/opt/x", ErrBadPartName},                            // a part's name is a folder name
		{"..", "m_api", "/opt/x", ErrBadPartName},
		{".x", "m_api", "/opt/x", ErrBadPartName},
		{"agents.md", "m_api", "/opt/x", ErrBadPartName},
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

	// rename, read-only, and move one to the front: display order only; machineId and path
	// follow the first part for old clients
	apiID := p.Parts[0].ID
	if !strings.HasPrefix(apiID, "pt_") || p.Parts[0].Name != "shop-api" {
		t.Fatalf("a new project's part %+v", p.Parts[0])
	}
	name, ro, front := "Web", true, 0
	p, err = s.PatchPart(p.ID, web.ID, PartPatch{Name: &name, ReadOnly: &ro, Index: &front})
	if err != nil || p.Parts[0].ID != web.ID || p.Parts[0].Name != "Web" || !p.Parts[0].ReadOnly || p.MachineID != "m_web" || p.Path != "/home/me/shop-web" {
		t.Fatalf("patch %v %+v %s:%s", err, p.Parts, p.MachineID, p.Path)
	}
	if p.Parts[1].ID != apiID || p.Parts[2].Name != "Gateway" {
		t.Fatalf("order %+v", p.Parts)
	}
	dup, bad := "gateway", "x/y"
	if _, err := s.PatchPart(p.ID, web.ID, PartPatch{Name: &dup}); !errors.Is(err, ErrPartName) {
		t.Fatalf("rename to a taken name %v", err)
	}
	if _, err := s.PatchPart(p.ID, web.ID, PartPatch{Name: &bad}); !errors.Is(err, ErrBadPartName) {
		t.Fatalf("rename to an unsafe name %v", err)
	}

	// any part goes, the first one too: machineId and path follow the new first part
	if p, _, err = s.DeletePart(p.ID, web.ID); err != nil || len(p.Parts) != 2 || p.Parts[0].ID != apiID || p.MachineID != "local" || p.Path != "/srv/shop-api" {
		t.Fatalf("delete the first %v %+v %s:%s", err, p.Parts, p.MachineID, p.Path)
	}
	if _, _, err := s.DeletePart(p.ID, "pt_none"); !errors.Is(err, ErrPartNotFound) {
		t.Fatalf("delete unknown %v", err)
	}
	if p, _, err = s.DeletePart(p.ID, apiID); err != nil || len(p.Parts) != 1 || p.Parts[0].Name != "Gateway" || p.MachineID != "m_api" {
		t.Fatalf("delete %v %+v", err, p.Parts)
	}
	if _, _, err := s.DeletePart(p.ID, p.Parts[0].ID); !errors.Is(err, ErrLastPart) {
		t.Fatalf("delete the last %v", err)
	}

	// saved: a new store reads the same parts
	again, _ := openStore(t, dir).Get(p.ID)
	if len(again.Parts) != 1 || again.Parts[0].Name != "Gateway" || again.MachineID != "m_api" || again.Path != "/srv/shop-api" {
		t.Fatalf("reloaded %+v", again)
	}
}

// TestCreateWithPartsNames: every part of a new project gets a "pt_" id; an unnamed one takes its
// folder's name, made safe and unique, and never the name another part is given.
func TestCreateWithPartsNames(t *testing.T) {
	s := openStore(t, t.TempDir())
	p, created, err := s.CreateWithParts("", []NewPart{
		{MachineID: "local", Path: "/srv/a/app"},
		{MachineID: "devbox", Path: "/srv/b/app"},
		{Name: "app", MachineID: "local", Path: "/srv/c"},
		{MachineID: "local", Path: "/srv/.cache"},
	})
	if err != nil || !created || p.Name != "app" {
		t.Fatalf("made %v %v %+v", err, created, p)
	}
	var got []string
	for _, pt := range p.Parts {
		if !strings.HasPrefix(pt.ID, "pt_") {
			t.Errorf("id %q", pt.ID)
		}
		got = append(got, pt.Name)
	}
	if strings.Join(got, ",") != "app 2,app 3,app,cache" {
		t.Fatalf("names %v", got)
	}
	if _, _, err := s.CreateWithParts("", []NewPart{{Name: "x/y", MachineID: "local", Path: "/srv/d"}}); !errors.Is(err, ErrBadPartName) {
		t.Fatalf("unsafe name %v", err)
	}
	var at *AtPart
	if _, _, err := s.CreateWithParts("", []NewPart{{MachineID: "local", Path: "/srv/e"}, {Name: "Web", MachineID: "local", Path: "/srv/f"}, {Name: "web", MachineID: "local", Path: "/srv/g"}}); !errors.As(err, &at) || at.Index != 2 || !errors.Is(err, ErrPartName) {
		t.Fatalf("a name twice %v", err)
	}
	solo, _, _ := s.Create("local", "/home/me/blog", "")
	if len(solo.Parts) != 1 || solo.Parts[0].Name != "blog" || !strings.HasPrefix(solo.Parts[0].ID, "pt_") {
		t.Fatalf("one part %+v", solo.Parts)
	}
}

// TestSplitAnyPart: the first part splits off too, keeping its id and name; the last one stays.
func TestSplitAnyPart(t *testing.T) {
	s := openStore(t, t.TempDir())
	p := addProject(t, s, "/srv/shop-api", "Shop")
	ro := true
	p, _, _ = s.AddPart(p.ID, "Web", "devbox", "/srv/shop-web")
	first := p.Parts[0]
	if _, err := s.PatchPart(p.ID, first.ID, PartPatch{ReadOnly: &ro}); err != nil {
		t.Fatal(err)
	}
	np, from, err := s.Split(p.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(np.Parts) != 1 || np.Parts[0].ID != first.ID || np.Parts[0].Name != "shop-api" || !np.Parts[0].ReadOnly || np.Name != "shop-api" || np.Path != "/srv/shop-api" {
		t.Fatalf("split off %+v", np)
	}
	if len(from.Parts) != 1 || from.Parts[0].Name != "Web" || from.MachineID != "devbox" || from.Path != "/srv/shop-web" {
		t.Fatalf("left %+v", from)
	}
	if _, _, err := s.Split(from.ID, from.Parts[0].ID); !errors.Is(err, ErrLastPart) {
		t.Fatalf("split the last %v", err)
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

func TestMergeNames(t *testing.T) {
	s := openStore(t, t.TempDir())
	a := addProject(t, s, "/srv/shop", "Shop")
	b := addProject(t, s, "/srv/web", "Web")
	bFirst := b.Parts[0].ID
	if _, _, err := s.AddPart(b.ID, "Docs", "local", "/srv/web-docs"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddPart(a.ID, "Web · web", "m_x", "/srv/taken"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlanMerge(a.ID, a.ID); !errors.Is(err, ErrMergeSelf) {
		t.Fatalf("self %v", err)
	}
	got, gone, plan, err := s.Merge(a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, pt := range got.Parts {
		names = append(names, pt.Name)
	}
	// a project in several parts: "<project> · <part>", and a taken name gets a number
	if want := "shop,Web · web,Web · web 2,Web · Docs"; strings.Join(names, ",") != want {
		t.Fatalf("names %s", strings.Join(names, ","))
	}
	if got.Workspaces[1].Name != "Web · Main" || got.Workspaces[1].ProjectID != a.ID {
		t.Fatalf("workspace %+v", got.Workspaces[1])
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != b.ID || gone.ID != b.ID {
		t.Fatalf("aliases %v", got.Aliases)
	}
	if plan.PartIDs[bFirst] != got.Parts[2].ID || plan.PartIDs[""] != got.Parts[2].ID {
		t.Fatalf("part ids %v", plan.PartIDs)
	}
	if _, ok := s.Get(b.ID); ok {
		t.Fatal("the merged project is still there")
	}
	// overlap: a folder inside one of the project's parts
	c := addProject(t, s, "/srv/shop/sub", "Sub")
	if _, err := s.PlanMerge(a.ID, c.ID); !errors.Is(err, ErrPartOverlap) {
		t.Fatalf("overlap %v", err)
	}
	// a project's name may hold "/", a part's may not
	d := addProject(t, s, "/srv/ops", "ops/infra")
	got, _, _, err = s.Merge(a.ID, d.ID)
	if err != nil || got.Parts[len(got.Parts)-1].Name != "ops-infra" {
		t.Fatalf("merged name %v %+v", err, got.Parts)
	}
}
