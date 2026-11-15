package session

import "testing"

// TestFillParts: only sessions in a project with no part and not at the root get one, and the
// stamp is saved.
func TestFillParts(t *testing.T) {
	m := testManager(t)
	for _, sp := range []Spec{
		{ID: "s_old", ProjectID: "p_shop", MachineID: "local", Cwd: "/home/me/shop"},
		{ID: "s_part", ProjectID: "p_shop", PartID: "pt_web", MachineID: "local", Cwd: "/home/me/web"},
		{ID: "s_root", ProjectID: "p_shop", Root: true, MachineID: "local", Cwd: "/data/roots/p_shop"},
		{ID: "s_none", MachineID: "local", Cwd: "/tmp"},
		{ID: "s_home", ProjectID: "p_shop", MachineID: "local", Cwd: "/home/me"},
	} {
		m.sessions[sp.ID] = &Session{m: m, spec: sp}
		m.order = append(m.order, sp.ID)
	}
	n := m.FillParts(func(sp Spec) string {
		if sp.Cwd == "/home/me/shop" {
			return "main"
		}
		return ""
	})
	if n != 1 {
		t.Fatalf("filled %d", n)
	}
	got := map[string]Placement{}
	for _, pl := range m.AllPlacements() {
		got[pl.ID] = pl
	}
	if got["s_old"].PartID != "main" || got["s_part"].PartID != "pt_web" || got["s_root"].PartID != "" || !got["s_root"].Root || got["s_home"].PartID != "" {
		t.Fatalf("placements %+v", got)
	}
	var saved []stored
	if _, err := m.st.Load(sessionsFile, &saved); err != nil || len(saved) != 5 || saved[0].PartID != "main" {
		t.Fatalf("saved %+v %v", saved, err)
	}
}
