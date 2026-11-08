package settings

import (
	"testing"

	"github.com/azamrizki/nextboard/internal/store"
)

// The first default lead name was never chosen by anyone: a hub that saved it takes the new
// one. A name the owner gave stays.
func TestLeadNameMovesFromTheOldDefault(t *testing.T) {
	for _, c := range []struct{ saved, want string }{{"Nara", DefaultLeadName}, {"Sekar", "Sekar"}} {
		st, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		doc := Defaults()
		doc.Lead.Name = c.saved
		if err := st.Save(File, doc); err != nil {
			t.Fatal(err)
		}
		s, err := Open(st)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Get().Lead.Name; got != c.want {
			t.Errorf("saved %q: %q, want %q", c.saved, got, c.want)
		}
	}
	if Defaults().Lead.Name != "Aruna" || !Defaults().Agents.RemoteTools {
		t.Fatalf("defaults %+v", Defaults().Lead)
	}
}
