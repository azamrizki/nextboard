package settings

import (
	"testing"

	"github.com/azamrizki/nextboard/internal/store"
)

// ui.openProjectsIn (docs/API-agents.md §11): where opening a project goes. "" asks, the default;
// "board" and "code" go there. Anything else is refused, null goes back to asking, and a document
// saved before the key existed asks too.
func TestOpenProjectsIn(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Get().UI.OpenProjectsIn; v != "" {
		t.Fatalf("default %q, want \"\" (ask)", v)
	}
	for _, v := range []string{"code", "board", ""} {
		got, err := s.Patch([]byte(`{"ui":{"openProjectsIn":"` + v + `"}}`))
		if err != nil || got.UI.OpenProjectsIn != v {
			t.Fatalf("%q: %v %+v", v, err, got.UI)
		}
	}
	if _, err := s.Patch([]byte(`{"ui":{"openProjectsIn":"code"}}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"ui":{"openProjectsIn":"Code"}}`, `{"ui":{"openProjectsIn":"editor"}}`, `{"ui":{"openProjectsIn":" board"}}`,
		`{"ui":{"openProjectsIn":1}}`, `{"ui":{"openProjectsIn":true}}`, `{"ui":{"other":"x"}}`, `{"ui":"code"}`} {
		if _, err := s.Patch([]byte(bad)); code(err) != "bad_setting" {
			t.Errorf("%s: %v", bad, err)
		}
	}
	// a refused patch leaves the choice as it was, on disk too
	if v := s.Get().UI.OpenProjectsIn; v != "code" {
		t.Fatalf("after refused patches %q", v)
	}
	if again, err := Open(st); err != nil || again.Get().UI.OpenProjectsIn != "code" {
		t.Fatalf("reloaded %v %+v", err, again.Get().UI)
	}
	// merged like the others: another key leaves it, null resets it to asking
	got, err := s.Patch([]byte(`{"lead":{"missionHours":6}}`))
	if err != nil || got.UI.OpenProjectsIn != "code" {
		t.Fatalf("other key: %v %+v", err, got.UI)
	}
	got, err = s.Patch([]byte(`{"ui":{"openProjectsIn":null}}`))
	if err != nil || got.UI.OpenProjectsIn != "" {
		t.Fatalf("null: %v %+v", err, got.UI)
	}
	if _, err := s.Patch([]byte(`{"ui":{"openProjectsIn":"board"}}`)); err != nil {
		t.Fatal(err)
	}
	got, err = s.Patch([]byte(`{"ui":null}`))
	if err != nil || got.UI.OpenProjectsIn != "" {
		t.Fatalf("ui null: %v %+v", err, got.UI)
	}
	// a document from before the key: ask
	if err := st.Save(File, map[string]any{"schemaVersion": 1, "lead": map[string]any{"name": "Sekar", "talkFirst": true}}); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(st); err != nil || s.Get().UI.OpenProjectsIn != "" || s.Get().Lead.Name != "Sekar" {
		t.Fatalf("old document: %v %+v", err, s.Get().UI)
	}
	// a hand-edited value the hub does not know: the document is not taken, as for any other bad key
	if err := st.Save(File, map[string]any{"schemaVersion": 1, "ui": map[string]any{"openProjectsIn": "tiles"}}); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(st); err != nil || s.Get().UI.OpenProjectsIn != "" {
		t.Fatalf("bad stored value: %v %+v", err, s.Get().UI)
	}
}
