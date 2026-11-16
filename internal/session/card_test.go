package session

import (
	"errors"
	"testing"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/machine"
)

// TestPanelCard: an agent opened inside an editor card keeps that card (docs/API-studio.md §7.8)
// across a hub restart; a console with a tile has none, and a card that is not a node id is
// refused before anything starts.
func TestPanelCard(t *testing.T) {
	m := testManager(t)
	if _, err := m.Create(CreateRequest{Preset: "shell", Panel: true, Card: "not a node"}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("bad card: %v", err)
	}
	in, err := m.Create(CreateRequest{Preset: "shell", Panel: true, Card: "n_editor0001", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Delete(in.ID) })
	if in.Card != "n_editor0001" || !in.Panel {
		t.Fatalf("panel %+v", in.Spec)
	}
	tile, err := m.Create(CreateRequest{Preset: "shell", Card: "n_editor0001", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Delete(tile.ID) })
	if tile.Card != "" {
		t.Fatalf("a console with a tile kept a card: %+v", tile.Spec)
	}

	// what sessions.json holds comes back after a restart (Load starts nothing)
	sp := Spec{ID: "s_panel00001", Name: "a", Handle: "Ash", MachineID: machine.LocalID, Agent: "claude", Preset: "claude", AgentKind: agent.KindAgent,
		Command: "claude", Cols: 80, Rows: 24, Cwd: t.TempDir(), Panel: true, Card: "n_editor0002", Asleep: true}
	again := testManager(t)
	if err := again.st.Save(sessionsFile, []stored{{Spec: sp}}); err != nil {
		t.Fatal(err)
	}
	if err := again.Load(); err != nil {
		t.Fatal(err)
	}
	s, ok := again.Get(sp.ID)
	if !ok {
		t.Fatal("not loaded")
	}
	if got := again.SessionInfo(s); got.Card != "n_editor0002" || !got.Panel {
		t.Fatalf("after a restart %+v", got.Spec)
	}
	// and the hub writes it back as it read it
	var list []stored
	again.saveNow()
	if _, err := again.st.Load(sessionsFile, &list); err != nil || len(list) != 1 || list[0].Card != "n_editor0002" {
		t.Fatalf("saved %v %+v", err, list)
	}
}
