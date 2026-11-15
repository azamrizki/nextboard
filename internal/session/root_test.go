package session

import (
	"errors"
	"strings"
	"testing"
)

// TestRootKeptByMove: a merge moves a root console to the other project and leaves it at the
// root, in its folder; a console in a part goes to the part its old one maps to.
func TestRootKeptByMove(t *testing.T) {
	m := testManager(t)
	for _, sp := range []Spec{
		{ID: "s_root", ProjectID: "p_from", Root: true, MachineID: "local", Cwd: "/data/roots/p_from"},
		{ID: "s_part", ProjectID: "p_from", PartID: "pt_web", MachineID: "local", Cwd: "/home/me/web"},
	} {
		m.sessions[sp.ID] = &Session{m: m, spec: sp}
		m.order = append(m.order, sp.ID)
	}
	if n := m.MoveProject("p_from", "p_into", func(id string) string { return "pt_new" }); n != 2 {
		t.Fatalf("moved %d", n)
	}
	root, _ := m.Get("s_root")
	part, _ := m.Get("s_part")
	if sp := root.Spec(); sp.ProjectID != "p_into" || !sp.Root || sp.PartID != "" || sp.Cwd != "/data/roots/p_from" {
		t.Fatalf("root console %+v", sp)
	}
	if sp := part.Spec(); sp.ProjectID != "p_into" || sp.PartID != "pt_new" || sp.Root {
		t.Fatalf("part console %+v", sp)
	}
}

// TestRootLaunchSyncs: every launch of a root console makes its folder current first; when that
// fails, nothing starts.
func TestRootLaunchSyncs(t *testing.T) {
	s := startingShell(t)
	s.spec.ProjectID, s.spec.Root = "p_shop", true
	var asked []string
	s.m.RootDir = func(pid string) (string, error) {
		asked = append(asked, pid)
		return s.spec.Cwd, nil
	}
	start(s)
	waitFor(t, s) // started
	if len(asked) != 1 || asked[0] != "p_shop" {
		t.Fatalf("RootDir %v", asked)
	}

	f := startingShell(t)
	f.spec.ProjectID, f.spec.Root = "p_shop", true
	f.m.RootDir = func(string) (string, error) { return "", errors.New("disk full") }
	start(f)
	f.mu.Lock()
	status, msg, running := f.status, f.errMsg, f.cur != nil
	f.mu.Unlock()
	if status != StatusError || !strings.Contains(msg, "root_unavailable") || running {
		t.Fatalf("status %s %q running %v", status, msg, running)
	}
}

// TestRootCreate: Create keeps the root the server placed, and refuses a root anywhere but This
// PC or with a part.
func TestRootCreate(t *testing.T) {
	m := testManager(t)
	m.RootDir = func(string) (string, error) { return t.TempDir(), nil }
	for _, req := range []CreateRequest{
		{Preset: "shell", Root: true, PartID: "pt_web", ProjectID: "p_shop"},
		{Preset: "shell", Root: true},
	} {
		if _, err := m.Create(req); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	in, err := m.Create(CreateRequest{Preset: "shell", Root: true, ProjectID: "p_shop", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Delete(in.ID) })
	if !in.Root || in.MachineID != "local" || in.PartID != "" {
		t.Fatalf("info %+v", in.Spec)
	}
}
