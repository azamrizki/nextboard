package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// agents.resumeTellChanges (docs/API-agents.md §29): on by default, also in a document saved
// before it existed, and turned off by a patch.
func TestResumeTellChanges(t *testing.T) {
	if !Defaults().Agents.ResumeTellChanges {
		t.Fatal("off by default")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, File), []byte(`{"schemaVersion":1,"agents":{"lazyStart":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, dir)
	if got := s.Get().Agents; !got.ResumeTellChanges || got.LazyStart {
		t.Fatalf("older document %+v", got)
	}
	got, err := s.Patch([]byte(`{"agents":{"resumeTellChanges":false}}`))
	if err != nil || got.Agents.ResumeTellChanges || got.Agents.LazyStart {
		t.Fatalf("patch %v %+v", err, got.Agents)
	}
	if open(t, dir).Get().Agents.ResumeTellChanges {
		t.Fatal("not saved")
	}
}
