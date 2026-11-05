package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreviewsAndGitKeys(t *testing.T) {
	s := open(t, t.TempDir())
	got := s.Get()
	if !got.Previews.Auto || got.Previews.PortRange != [2]int{12320, 12339} || !got.Git.CommitWithRunner {
		t.Fatalf("defaults %+v %+v", got.Previews, got.Git)
	}
	for _, bad := range []string{`{"previews":{"portRange":[80,90]}}`, `{"previews":{"portRange":[13000,12000]}}`,
		`{"previews":{"portRange":[20000,30000]}}`, `{"previews":{"nope":1}}`, `{"git":{"x":1}}`} {
		if _, err := s.Patch([]byte(bad)); code(err) != "bad_setting" {
			t.Errorf("%s: %v", bad, err)
		}
	}
	v, err := s.Patch([]byte(`{"previews":{"auto":false,"portRange":[40000,40019]},"git":{"commitWithRunner":false}}`))
	if err != nil || v.Previews.Auto || v.Previews.PortRange[0] != 40000 || v.Git.CommitWithRunner {
		t.Fatalf("patch %+v %v", v, err)
	}
	if v, _ := s.Patch([]byte(`{"previews":null}`)); !v.Previews.Auto || v.Previews.PortRange[0] != 12320 {
		t.Fatalf("reset %+v", v.Previews)
	}
}

// TestOldGitKey: a settings file saved with suggestWithRunner, whatever its value, opens with
// the button reading the changes, and later patches still work.
func TestOldGitKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, File), []byte(`{"git":{"suggestWithRunner":false},"previews":{"auto":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, dir)
	if got := s.Get(); !got.Git.CommitWithRunner || got.Previews.Auto {
		t.Fatalf("opened %+v %+v", got.Git, got.Previews)
	}
	if v, err := s.Patch([]byte(`{"previews":{"auto":true}}`)); err != nil || !v.Git.CommitWithRunner {
		t.Fatalf("patch %+v %v", v.Git, err)
	}
}
