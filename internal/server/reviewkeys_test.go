package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/azamrizki/nextboard/internal/project"
)

// TestMigrateReviewKeys: the first part's review data moves from the bare project id to its part
// key once; a second run, a project with nothing kept, and one whose key already has data change
// nothing.
func TestMigrateReviewKeys(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(dir, rel, "state.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(dir, rel))
		return err == nil
	}
	parts := func(ids ...string) []project.Part {
		out := make([]project.Part, len(ids))
		for i, id := range ids {
			out[i] = project.Part{ID: id, Name: id, MachineID: "local", Path: "/home/me/" + id}
		}
		return out
	}
	shop := project.Project{ID: "p_shop", Parts: parts("main", "pt_web")}
	blog := project.Project{ID: "p_blog", Parts: parts("pt_blog")}
	both := project.Project{ID: "p_both", Parts: parts("pt_a", "pt_b")}
	none := project.Project{ID: "p_none"}
	write("p_shop")
	write("p_shop~pt_web")
	write("p_blog/mirror/srv")
	write("p_both")
	write("p_both~pt_a")
	list := []project.Project{shop, blog, both, none}

	if n := migrateReviewKeys(dir, list); n != 2 {
		t.Fatalf("moved %d, want 2", n)
	}
	for _, rel := range []string{"p_shop~main/state.json", "p_shop~pt_web/state.json", "p_blog~pt_blog/mirror/srv/state.json", "p_both", "p_both~pt_a"} {
		if !exists(rel) {
			t.Fatalf("%s is missing", rel)
		}
	}
	for _, rel := range []string{"p_shop", "p_blog"} {
		if exists(rel) {
			t.Fatalf("%s is still there", rel)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "p_shop~main", "state.json")); string(b) != "p_shop" {
		t.Fatalf("moved content %q", b)
	}
	if n := migrateReviewKeys(dir, list); n != 0 {
		t.Fatalf("second run moved %d", n)
	}
	if n := migrateReviewKeys(filepath.Join(dir, "missing"), list); n != 0 {
		t.Fatalf("no review folder moved %d", n)
	}
}
