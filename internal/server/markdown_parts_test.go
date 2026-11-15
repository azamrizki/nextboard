package server

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/azamrizki/nextboard/internal/transcript"
)

// TestMarkdownVaultPerPart: the Markdown studio's vault, graph and backlinks read the part a ref
// names ("<project>~<part>" or ?part=), each with its own Claude memory folder; a bare project id
// is the first part in display order, for old clients (v0.9.0-beta.2, audit #53).
func TestMarkdownVaultPerPart(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	lh, _ := h.srv.machines.Host("local")
	cfg, err := h.srv.claudeCfg(t.Context(), lh, "") // the hub's default Claude profile
	if err != nil {
		t.Fatal(err)
	}
	atlas, harbor := t.TempDir(), t.TempDir()
	write := func(dir, rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(atlas, "README.md", "# Atlas\n[[plan]]")
	write(atlas, "plan.md", "# Plan")
	write(harbor, "guide.md", "# Harbor guide\n[[faq]]")
	write(harbor, "docs/faq.md", "# FAQ")
	write(harbor, "lonely.md", "alone")
	memOf := func(dir string) string {
		return filepath.Join(cfg, "projects", transcript.ClaudeSlug(filepath.Clean(dir)), "memory")
	}
	write(memOf(atlas), "MEMORY.md", "# Atlas memory")
	write(memOf(harbor), "harbor-notes.md", "# Harbor memory")

	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": atlas, "name": "Atlas"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid := p["id"].(string)
	st, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Harbor", "machineId": "local", "path": harbor}, nil)
	if st != 201 {
		t.Fatalf("part %d %v", st, p)
	}
	part := p["parts"].([]any)[1].(map[string]any)["id"].(string)

	vault := func(url string) (rels, mem []string, memDir string) {
		t.Helper()
		st, v := h.do("GET", url, nil, nil)
		if st != 200 {
			t.Fatalf("%s: %d %v", url, st, v)
		}
		for _, f := range v["files"].([]any) {
			rels = append(rels, f.(map[string]any)["rel"].(string))
		}
		if am, ok := v["agentMemory"].(map[string]any); ok {
			memDir, _ = am["dir"].(string)
			fs, _ := am["files"].([]any)
			for _, f := range fs {
				mem = append(mem, f.(map[string]any)["rel"].(string))
			}
		}
		slices.Sort(rels)
		return rels, mem, memDir
	}
	check := func(url string, want []string, wantMem, dir string) {
		t.Helper()
		rels, mem, memDir := vault(url)
		if !slices.Equal(rels, want) || !slices.Equal(mem, []string{wantMem}) || memDir != memOf(dir) {
			t.Fatalf("%s: files %v, memory %v in %q", url, rels, mem, memDir)
		}
	}
	atlasNotes, harborNotes := []string{"README.md", "plan.md"}, []string{"docs/faq.md", "guide.md", "lonely.md"}
	base := "/api/projects/" + pid
	check(base+"/markdown", atlasNotes, "MEMORY.md", atlas) // a bare id: the first part
	check(base+"~"+part+"/markdown", harborNotes, "harbor-notes.md", harbor)
	check(base+"/markdown?part="+part, harborNotes, "harbor-notes.md", harbor)

	// graph and backlinks follow the same part
	graph := func(url string) (nodes, edges int) {
		t.Helper()
		st, g := h.do("GET", url, nil, nil)
		if st != 200 {
			t.Fatalf("%s: %d %v", url, st, g)
		}
		return len(g["nodes"].([]any)), len(g["edges"].([]any))
	}
	if n, e := graph(base + "/markdown/graph"); n != 2 || e != 1 {
		t.Fatalf("first part graph %d nodes %d edges", n, e)
	}
	if n, e := graph(base + "~" + part + "/markdown/graph"); n != 3 || e != 1 {
		t.Fatalf("part graph %d nodes %d edges", n, e)
	}
	st, bl := h.do("GET", base+"~"+part+"/markdown/backlinks?path=docs/faq.md", nil, nil)
	if st != 200 || len(bl["backlinks"].([]any)) != 1 || bl["backlinks"].([]any)[0].(map[string]any)["from"] != "guide.md" {
		t.Fatalf("part backlinks %d %v", st, bl)
	}
	if st, b := h.do("GET", base+"/markdown/backlinks?path=docs/faq.md", nil, nil); st != 404 || b["code"] != "not_in_vault" {
		t.Fatalf("a note of another part in the first part's vault %d %v", st, b)
	}
	if st, b := h.do("GET", base+"~pt_nope/markdown", nil, nil); st != 404 || b["code"] != "part_not_found" {
		t.Fatalf("unknown part %d %v", st, b)
	}

	// all parts are equal: move Harbor first and the bare id follows it; Atlas by its own ref
	if st, b := h.do("PATCH", base+"/parts/"+part, map[string]any{"index": 0}, nil); st != 200 {
		t.Fatalf("reorder %d %v", st, b)
	}
	first := p["parts"].([]any)[0].(map[string]any)["id"].(string)
	check(base+"/markdown", harborNotes, "harbor-notes.md", harbor)
	check(base+"~"+first+"/markdown", atlasNotes, "MEMORY.md", atlas)
}
