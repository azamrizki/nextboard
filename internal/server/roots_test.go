package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/session"
)

// openForCleanup makes the note folders of every root writable again, so the test's data folder
// can go (they are 0555).
func (r *rootDirs) openForCleanup() {
	if r == nil {
		return
	}
	_ = filepath.WalkDir(r.base, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
}

// rootProject makes a project of three parts on This PC (Web; Docs, read only; Api on a server
// that is never reached) and returns its id, workspace and part ids by name.
func rootProject(t *testing.T, h *testHub) (pid, wid string, web, docs string, ids map[string]string) {
	t.Helper()
	web, docs = t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(web, "index.html"), []byte("<p>acme-shop</p>\n"), 0o644)
	os.WriteFile(filepath.Join(web, "CLAUDE.md"), []byte("web rules\n"), 0o644)
	os.WriteFile(filepath.Join(docs, "guide.md"), []byte("# guide\n"), 0o644)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": web, "name": "Acme Shop", "partName": "Web"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid, wid = p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	if st, b := h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Docs", "machineId": "local", "path": docs}, nil); st != 201 {
		t.Fatalf("part %d %v", st, b)
	}
	if _, _, err := h.srv.projects.AddPart(pid, "Api", "m_far", "/srv/api"); err != nil {
		t.Fatal(err)
	}
	ids = map[string]string{}
	pr, _ := h.srv.projects.Get(pid)
	for _, pt := range pr.Parts {
		ids[pt.Name] = pt.ID
	}
	if ids["Web"] == "" || ids["Docs"] == "" || ids["Api"] == "" {
		t.Fatalf("parts %v", pr.Parts)
	}
	if st, b := h.do("PATCH", "/api/projects/"+pid+"/parts/"+ids["Docs"], map[string]any{"readOnly": true}, nil); st != 200 {
		t.Fatalf("read only %d %v", st, b)
	}
	return pid, wid, web, docs, ids
}

func readText(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestProjectRoot: a console placed at the root of a project of three parts works on This PC in
// the hub's root folder, which holds a link for the writable part on This PC, notes for the
// read-only one and the one on a server, and AGENTS.md/CLAUDE.md listing all three. The folder
// follows the project (a rename, access, a removed part) and goes with it; the parts' own files
// never do.
func TestProjectRoot(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	pid, wid, web, docs, ids := rootProject(t, h)

	// wrong placements
	for _, body := range []map[string]any{
		{"root": true, "partId": ids["Web"]},
		{"root": true, "machineId": "m_far"},
		{"root": true, "cwd": web},
	} {
		body["preset"], body["workspaceId"] = "shell", wid
		if st, b := h.do("POST", "/api/sessions", body, nil); st != 400 || b["code"] != "bad_placement" {
			t.Fatalf("%v: %d %v", body, st, b)
		}
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": wid, "root": true}, nil)
	dir := h.srv.roots.Dir(pid)
	if st != 201 || in["root"] != true || in["machineId"] != "local" || in["cwd"] != dir || in["partId"] != nil && in["partId"] != "" {
		t.Fatalf("root console %d %v", st, in)
	}
	sid := in["id"].(string)

	// the entries
	if fi, err := os.Lstat(filepath.Join(dir, "Web")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Web is no link: %v %v", fi, err)
	}
	if got, _ := os.Readlink(filepath.Join(dir, "Web")); got != web {
		t.Fatalf("Web → %s", got)
	}
	if got := readText(t, filepath.Join(dir, "Web", "index.html")); !strings.Contains(got, "acme-shop") {
		t.Fatalf("through the link: %q", got)
	}
	for _, n := range []string{"Docs", "Api"} {
		fi, err := os.Lstat(filepath.Join(dir, n))
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o555 {
			t.Fatalf("%s: %v %v", n, fi, err)
		}
		if fi, err := os.Stat(filepath.Join(dir, n, rootNoteFile)); err != nil || fi.Mode().Perm() != 0o444 {
			t.Fatalf("note of %s: %v %v", n, fi, err)
		}
		if err := os.WriteFile(filepath.Join(dir, n, "x.txt"), []byte("x"), 0o644); err == nil {
			t.Fatalf("a write in the note of %s worked", n)
		}
	}
	if note := readText(t, filepath.Join(dir, "Api", rootNoteFile)); !strings.Contains(note, `server "m_far"`) || !strings.Contains(note, "/srv/api") || !strings.Contains(note, `part: "Api"`) {
		t.Fatalf("note of Api:\n%s", note)
	}
	if _, err := os.Stat(filepath.Join(dir, "Docs", "guide.md")); err == nil {
		t.Fatal("the read-only part was copied")
	}
	agents, claude := readText(t, filepath.Join(dir, "AGENTS.md")), readText(t, filepath.Join(dir, "CLAUDE.md"))
	if agents != claude {
		t.Fatalf("AGENTS.md and CLAUDE.md differ")
	}
	for _, want := range []string{
		"# Acme Shop — project root (made by NextBoard)",
		"| Web | This PC | " + web + " | ./Web (link) | change it with your own tools, in ./Web |",
		"| Docs | This PC | " + docs + ` | ./Docs (note) | read only: read_file, grep, glob with part: "Docs" |`,
		`| Api | server "m_far" | /srv/api | ./Api (note) | read only: read_file, grep, glob with part: "Api" |`,
		"./Web/CLAUDE.md",
		"Git: each part is its own repository",
	} {
		if !strings.Contains(agents, want) {
			t.Fatalf("AGENTS.md without %q:\n%s", want, agents)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "AGENTS.md")); fi.Mode().Perm() != 0o444 {
		t.Fatalf("AGENTS.md mode %v", fi.Mode())
	}
	var m rootManifestFile
	if err := json.Unmarshal([]byte(readText(t, filepath.Join(dir, rootManifestDir, rootManifest))), &m); err != nil || len(m.Entries) != 3 || m.ProjectID != pid {
		t.Fatalf("manifest %v %+v", err, m)
	}

	// something an agent made at the root stays
	os.WriteFile(filepath.Join(dir, "plan.md"), []byte("plan\n"), 0o644)

	// full access: Api may change, through the hub's tools
	h.do("PATCH", "/api/projects/"+pid, map[string]any{"partAccess": "full"}, nil)
	waitTrue(t, "Api writable in AGENTS.md", 5*time.Second, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
		return strings.Contains(string(b), `change: write_file, edit_file, bash with part: "Api"`)
	})
	// a rename moves the entry; the folder behind it stays as it was
	if st, b := h.do("PATCH", "/api/projects/"+pid+"/parts/"+ids["Web"], map[string]any{"name": "Front"}, nil); st != 200 {
		t.Fatalf("rename %d %v", st, b)
	}
	waitTrue(t, "the entry renamed", 5*time.Second, func() bool {
		_, gone := os.Lstat(filepath.Join(dir, "Web"))
		fi, err := os.Lstat(filepath.Join(dir, "Front"))
		return gone != nil && err == nil && fi.Mode()&os.ModeSymlink != 0
	})
	if !strings.Contains(readText(t, filepath.Join(dir, "AGENTS.md")), "| Front | This PC | "+web+" | ./Front (link)") {
		t.Fatalf("AGENTS.md after the rename:\n%s", readText(t, filepath.Join(dir, "AGENTS.md")))
	}
	// a part made writable becomes a link; made read only again, a note, and the link goes
	h.do("PATCH", "/api/projects/"+pid+"/parts/"+ids["Docs"], map[string]any{"readOnly": false}, nil)
	waitTrue(t, "Docs a link", 5*time.Second, func() bool {
		fi, err := os.Lstat(filepath.Join(dir, "Docs"))
		return err == nil && fi.Mode()&os.ModeSymlink != 0
	})
	// a part removed: its link goes, its folder's files stay (a root console holds no part)
	if st, b := h.do("DELETE", "/api/projects/"+pid+"/parts/"+ids["Web"], nil, nil); st != 200 {
		t.Fatalf("delete part %d %v", st, b)
	}
	waitTrue(t, "the link gone", 5*time.Second, func() bool {
		_, err := os.Lstat(filepath.Join(dir, "Front"))
		return err != nil
	})
	if got := readText(t, filepath.Join(web, "index.html")); !strings.Contains(got, "acme-shop") {
		t.Fatalf("the part's file: %q", got)
	}
	if readText(t, filepath.Join(dir, "plan.md")) != "plan\n" {
		t.Fatal("the agent's file went")
	}

	// a restart keeps the placement and the folder
	st, in = h.do("POST", "/api/sessions/"+sid+"/restart", map[string]any{}, nil)
	if st != 200 || in["root"] != true || in["cwd"] != dir {
		t.Fatalf("restart %d %v", st, in)
	}
	// a part added while nothing told the root: the next launch lists it
	other := t.TempDir()
	if _, _, err := h.srv.projects.AddPart(pid, "Jobs", "local", other); err != nil {
		t.Fatal(err)
	}
	h.do("POST", "/api/sessions/"+sid+"/restart", map[string]any{}, nil)
	waitTrue(t, "Jobs at the root after a launch", 10*time.Second, func() bool {
		fi, err := os.Lstat(filepath.Join(dir, "Jobs"))
		return err == nil && fi.Mode()&os.ModeSymlink != 0
	})

	// deleted for good: the root goes, the parts' folders stay
	h.do("DELETE", "/api/sessions/"+sid, nil, nil)
	if st, b := h.do("DELETE", "/api/projects/"+pid+"?permanent=1", nil, nil); st != 200 {
		t.Fatalf("delete %d %v", st, b)
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Fatal("the root folder stays")
	}
	for _, f := range []string{filepath.Join(web, "index.html"), filepath.Join(docs, "guide.md")} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("a part's file went with the root: %v", err)
		}
	}
}

// TestRootSingleAndCompat: in a project of one part the root is that part, so it never gets a
// root folder; a console that names no placement is placed as before.
func TestRootSingleAndCompat(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "devbox"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	part := p["parts"].([]any)[0].(map[string]any)["id"].(string)
	st, in := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": wid, "root": true}, nil)
	if st != 201 || in["root"] == true || in["partId"] != part || in["cwd"] != dir {
		t.Fatalf("root of one part %d %v", st, in)
	}
	if _, err := os.Lstat(h.srv.roots.Dir(pid)); err == nil {
		t.Fatal("a project of one part got a root folder")
	}
	st, in = h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": wid}, nil)
	if st != 201 || in["partId"] != part || in["cwd"] != dir || in["root"] == true {
		t.Fatalf("no placement %d %v", st, in)
	}
	// an agent outside the part's folder (no part) gets no file tools, as before parts
	sa := newSpyAgents(t, h, spyCodex(t, h), wid)
	sid, tok, _, env := sa.start(map[string]any{"cwd": t.TempDir()})
	if ts := sa.tools(sid, tok); ts["read_file"] != nil || ts["memory_read"] == nil {
		t.Fatalf("tools %v", ts)
	}
	if strings.Contains(env, "spans several parts") || strings.Contains(env, "project root") {
		t.Fatalf("prompt:\n%s", env)
	}
}

// TestRootFileAttribution: what a root console's own tools name through a part's link goes to
// that part's review with its real path; a path in no part is dropped. Its turns are watched in
// the parts it changes with its own tools.
func TestRootFileAttribution(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	pid, wid, web, docs, ids := rootProject(t, h)
	dir, err := h.srv.roots.Sync(pid)
	if err != nil {
		t.Fatal(err)
	}
	sp := session.Spec{ID: "s_x", ProjectID: pid, WorkspaceID: wid, MachineID: "local", Cwd: dir, Root: true}
	for _, c := range []struct{ path, key, real string }{
		{filepath.Join(dir, "Web", "src", "app.ts"), pid + "~" + ids["Web"], filepath.Join(web, "src", "app.ts")},
		{"Web/index.html", pid + "~" + ids["Web"], filepath.Join(web, "index.html")},
		{filepath.Join(web, "index.html"), pid + "~" + ids["Web"], filepath.Join(web, "index.html")},
		{filepath.Join(docs, "guide.md"), pid + "~" + ids["Docs"], filepath.Join(docs, "guide.md")},
	} {
		key, real, ok := h.srv.fileToolReview(sp, c.path)
		if !ok || key != c.key || real != c.real {
			t.Fatalf("%s → %v %s %s", c.path, ok, key, real)
		}
	}
	for _, p := range []string{filepath.Join(dir, "plan.md"), filepath.Join(dir, "Api", "x.php"), "/etc/hosts"} {
		if key, _, ok := h.srv.fileToolReview(sp, p); ok {
			t.Fatalf("%s went to %s", p, key)
		}
	}
	got := h.srv.sessionReviews(sp)
	var watched []string
	for _, k := range got {
		if k.changes {
			watched = append(watched, k.key)
		}
	}
	if len(got) != 3 || !slices.Equal(watched, []string{pid + "~" + ids["Web"]}) {
		t.Fatalf("reviews %+v", got)
	}
}
