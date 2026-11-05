package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/store"
)

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func addProject(t *testing.T, s *Store, dir, name string) Project {
	t.Helper()
	p, _, err := s.Create("local", dir, name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGroupTree(t *testing.T) {
	s := openStore(t, t.TempDir())
	a, err := s.CreateGroup("Clients", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateGroup("Globex", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGroup("globex", a.ID); !errors.Is(err, ErrGroupNameTaken) {
		t.Fatalf("same name on one level: %v", err)
	}
	// the same name on another level is another group
	if _, err := s.CreateGroup("Globex", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGroup("x", "g_none"); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("unknown parent: %v", err)
	}
	if _, err := s.CreateGroup(strings.Repeat("x", MaxName+1), ""); !errors.Is(err, ErrBadName) {
		t.Fatalf("long name: %v", err)
	}

	// into itself or below itself
	self, child := a.ID, b.ID
	if _, err := s.UpdateGroup(a.ID, nil, &self); !errors.Is(err, ErrGroupCycle) {
		t.Fatalf("into itself: %v", err)
	}
	if _, err := s.UpdateGroup(a.ID, nil, &child); !errors.Is(err, ErrGroupCycle) {
		t.Fatalf("below itself: %v", err)
	}
	// only a case change of its own name is no conflict
	lower := "clients"
	if g, err := s.UpdateGroup(a.ID, &lower, nil); err != nil || g.Name != "clients" {
		t.Fatalf("%+v %v", g, err)
	}
	// to the top, where a group of that name already is
	top := ""
	if _, err := s.UpdateGroup(b.ID, nil, &top); !errors.Is(err, ErrGroupNameTaken) {
		t.Fatalf("name taken at the target: %v", err)
	}
}

func TestGroupDepth(t *testing.T) {
	s := openStore(t, t.TempDir())
	parent := ""
	var levels []Group
	for range MaxGroupDepth {
		g, err := s.CreateGroup("level", parent)
		if err != nil {
			t.Fatal(err)
		}
		levels = append(levels, g)
		parent = g.ID
	}
	if _, err := s.CreateGroup("too deep", parent); !errors.Is(err, ErrGroupTooDeep) {
		t.Fatalf("level %d: %v", MaxGroupDepth+1, err)
	}
	// a move counts what it brings along
	other, err := s.CreateGroup("other", "")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateGroup("inner", other.ID)
	if err != nil {
		t.Fatal(err)
	}
	under := levels[MaxGroupDepth-2].ID
	if _, err := s.UpdateGroup(other.ID, nil, &under); !errors.Is(err, ErrGroupTooDeep) {
		t.Fatalf("moved subtree: %v", err)
	}
	if _, err := s.UpdateGroup(inner.ID, nil, &under); err != nil {
		t.Fatalf("a single group fits: %v", err)
	}
}

func TestDeleteGroupMovesUp(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	top, _ := s.CreateGroup("Top", "")
	mid, _ := s.CreateGroup("Mid", top.ID)
	low, _ := s.CreateGroup("Low", mid.ID)
	p := addProject(t, s, "/a", "a")
	q := addProject(t, s, "/b", "b")
	r := addProject(t, s, "/c", "c")
	for pid, gid := range map[string]string{p.ID: mid.ID, q.ID: top.ID, r.ID: low.ID} {
		if _, err := s.Patch(pid, Patch{GroupID: &gid}); err != nil {
			t.Fatal(err)
		}
	}
	moved, err := s.DeleteGroup(mid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0] != p.ID {
		t.Fatalf("moved %v", moved)
	}
	moved, err = s.DeleteGroup(top.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 2 {
		t.Fatalf("moved %v", moved)
	}
	// saved: all of it after a restart
	s = openStore(t, dir)
	got, _ := s.Get(p.ID)
	if got.GroupID != "" || got.Name != "a" {
		t.Fatalf("%+v", got)
	}
	if got, _ := s.Get(r.ID); got.GroupID != low.ID {
		t.Fatalf("a project of a kept group moved: %+v", got)
	}
	groups := s.Groups()
	if len(groups) != 1 || groups[0].ID != low.ID || groups[0].ParentID != "" {
		t.Fatalf("%+v", groups)
	}
	if _, err := s.DeleteGroup("g_none"); !errors.Is(err, ErrGroupNotFound) {
		t.Fatal(err)
	}
}

func TestPatchRefusesAsOne(t *testing.T) {
	s := openStore(t, t.TempDir())
	p := addProject(t, s, "/a", "a")
	name, none := "renamed", "g_none"
	if _, err := s.Patch(p.ID, Patch{Name: &name, GroupID: &none}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatal(err)
	}
	bad := []string{"a,b"}
	if _, err := s.Patch(p.ID, Patch{Name: &name, Tags: &bad}); !errors.Is(err, ErrBadTag) {
		t.Fatal(err)
	}
	if got, _ := s.Get(p.ID); got.Name != "a" || got.GroupID != "" || got.Tags != nil {
		t.Fatalf("a refused patch changed it: %+v", got)
	}
	if _, err := s.Patch("p_none", Patch{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestCleanTags(t *testing.T) {
	got, err := CleanTags([]string{" backend ", "", "Backend", "web", "  "})
	if err != nil || strings.Join(got, "|") != "backend|web" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range [][]string{
		{strings.Repeat("x", MaxName+1)},
		{"a,b"},
		{"a\nb"},
	} {
		if _, err := CleanTags(bad); !errors.Is(err, ErrBadTag) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	many := make([]string, MaxTags+1)
	for i := range many {
		many[i] = strings.Repeat("t", i+1)
	}
	if _, err := CleanTags(many); !errors.Is(err, ErrBadTag) {
		t.Fatalf("too many: %v", err)
	}
	if got, err := CleanTags(nil); err != nil || got != nil {
		t.Fatalf("%q %v", got, err)
	}
}

// A hub from before groups: its prefixes become groups once, and only then.
func TestGroupsFromPrefixes(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := []*Project{
		{ID: "p_a", Name: "Acme - Portal", MachineID: "local", Path: "/a"},
		{ID: "p_b", Name: "Globex - Shop Dev", MachineID: "m", Path: "/b"},
		{ID: "p_c", Name: "globex - Shop Local", MachineID: "local", Path: "/c"},
		{ID: "p_d", Name: "demo-backoffice", MachineID: "local", Path: "/d"},
		{ID: "p_e", Name: "One - Two - Three", MachineID: "local", Path: "/e"},
		{ID: "p_f", Name: "- leading", MachineID: "local", Path: "/f"},
	}
	if err := st.Save(File, old); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir)
	groups := s.Groups()
	if len(groups) != 3 {
		t.Fatalf("%+v", groups)
	}
	idOf := map[string]string{}
	for _, g := range groups {
		idOf[g.Name] = g.ID
		if g.ParentID != "" {
			t.Fatalf("%+v", g)
		}
	}
	for id, want := range map[string]struct{ name, group string }{
		"p_a": {"Portal", "Acme"},
		"p_b": {"Shop Dev", "Globex"},
		"p_c": {"Shop Local", "Globex"},
		"p_d": {"demo-backoffice", ""},
		"p_e": {"Two - Three", "One"},
		"p_f": {"- leading", ""},
	} {
		got, ok := s.Get(id)
		if !ok || got.Name != want.name || got.GroupID != idOf[want.group] {
			t.Errorf("%s: %+v, want %+v", id, got, want)
		}
	}

	// Once. A project named like that later on keeps its name, also after a restart.
	s = openStore(t, dir)
	p := addProject(t, s, "/g", "New - Name")
	s = openStore(t, dir)
	if got, _ := s.Get(p.ID); got.Name != "New - Name" || got.GroupID != "" {
		t.Fatalf("%+v", got)
	}
	if len(s.Groups()) != 3 {
		t.Fatalf("%+v", s.Groups())
	}
}

// A new hub writes its empty file at once, so its later names are not prefixes.
func TestNewHubHasNoPrefixes(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	p := addProject(t, s, "/a", "A - B")
	if _, err := os.Stat(filepath.Join(dir, GroupsFile)); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, dir)
	if got, _ := s.Get(p.ID); got.Name != "A - B" {
		t.Fatalf("%+v", got)
	}
}

func TestRepairGroups(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(GroupsFile, []*Group{
		{ID: "g_lost", Name: "lost", ParentID: "g_gone"},
		{ID: "g_x", Name: "x", ParentID: "g_y"},
		{ID: "g_y", Name: "y", ParentID: "g_x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(File, []*Project{{ID: "p_a", Name: "a", Path: "/a", GroupID: "g_gone"}}); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir)
	if got, _ := s.Get("p_a"); got.GroupID != "" {
		t.Fatalf("%+v", got)
	}
	for _, g := range s.Groups() {
		// walking up must end, wherever it starts
		if s.groupDepthLocked(g.ID) > len(s.groups) {
			t.Fatalf("%+v", g)
		}
	}
	if g := s.groupLocked("g_lost"); g.ParentID != "" {
		t.Fatalf("%+v", g)
	}
}
