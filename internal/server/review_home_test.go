package server

import (
	"os"
	"path/filepath"
	"testing"
)

// The default Home project is the user's whole home folder, secrets included: review
// must never copy that into its shadow repository. A folder holding it counts the same.
func TestReviewLeavesTheHomeFolderAlone(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	pid, _ := h.homeWorkspace()

	unsupported := func(id string) {
		t.Helper()
		if err := h.srv.review.Snapshot(id); err == nil {
			t.Fatalf("%s: snapshot harus ditolak", id)
		}
		var sum struct {
			Supported bool `json:"supported"`
		}
		if st := h.getJSON("/api/projects/"+id+"/review", &sum); st != 200 || sum.Supported {
			t.Fatalf("%s: review: %d %+v", id, st, sum)
		}
		if _, err := os.Stat(h.srv.st.Path("review", id)); !os.IsNotExist(err) {
			t.Fatalf("%s: folder review terbentuk: %v", id, err)
		}
	}
	unsupported(pid)

	st, above := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": filepath.Dir(homeDir()), "name": "above"}, nil)
	if st != 201 {
		t.Fatalf("project di atas home: %d %v", st, above)
	}
	unsupported(above["id"].(string))

	// a project of its own, even inside home, does take part
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, own := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "own"}, nil)
	if st != 201 {
		t.Fatalf("project: %d %v", st, own)
	}
	if err := h.srv.review.Snapshot(own["id"].(string)); err != nil {
		t.Fatalf("project biasa: %v", err)
	}
}
