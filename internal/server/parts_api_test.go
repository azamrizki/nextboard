package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectParts: a project spans folders as parts; a terminal opens in a part, and a part
// marked read-only keeps agents out (ROADMAP #49).
func TestProjectParts(t *testing.T) {
	h := newTestHub(t)
	pid, wid, api := orchSetup(t, h)
	web, docs := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(web, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, other := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": docs, "name": "Docs"}, nil)
	if st != 201 {
		t.Fatalf("other project %d %v", st, other)
	}

	// the project's own folder is the first part
	st, pr := h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Frontend", "machineId": "local", "path": web + "/./"}, nil)
	parts, _ := pr["parts"].([]any)
	if st != 201 || len(parts) != 2 || pr["path"] != api {
		t.Fatalf("add %d %v", st, pr)
	}
	first, front := parts[0].(map[string]any), parts[1].(map[string]any)
	if first["id"] != "main" || first["path"] != api || front["path"] != web || front["name"] != "Frontend" {
		t.Fatalf("parts %v", parts)
	}
	fid := front["id"].(string)

	for _, c := range []struct {
		body       map[string]any
		want       int
		code       string
		otherOwner bool
	}{
		{map[string]any{"name": "Gone", "path": filepath.Join(web, "missing")}, 400, "", false},
		{map[string]any{"name": "Src", "path": filepath.Join(web, "src")}, 409, "part_overlap", false},
		{map[string]any{"name": "frontend", "path": t.TempDir()}, 409, "name_conflict", false},
		{map[string]any{"name": "Docs", "path": docs}, 409, "path_in_project", true},
		{map[string]any{"name": "Far", "machineId": "m_nope", "path": "/srv"}, 404, "", false},
		{map[string]any{"path": t.TempDir()}, 400, "", false},
	} {
		st, b := h.do("POST", "/api/projects/"+pid+"/parts", c.body, nil)
		if st != c.want || (c.code != "" && b["code"] != c.code) || (c.otherOwner && b["projectId"] != other["id"]) {
			t.Errorf("%v: %d %v", c.body, st, b)
		}
	}

	// a terminal in a part opens in its folder; one opened inside the folder is found in it
	st, s1 := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": wid, "partId": fid}, nil)
	if st != 201 || s1["cwd"] != web || s1["partId"] != fid {
		t.Fatalf("shell in part %d cwd=%v part=%v", st, s1["cwd"], s1["partId"])
	}
	st, s2 := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": wid, "cwd": filepath.Join(web, "src")}, nil)
	if st != 201 || s2["partId"] != fid {
		t.Fatalf("shell inside part %d part=%v", st, s2["partId"])
	}
	st, s3 := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": wid}, nil)
	if st != 201 || s3["cwd"] != api || s3["partId"] != "main" {
		t.Fatalf("shell in the project %d cwd=%v part=%v", st, s3["cwd"], s3["partId"])
	}
	if st, b := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": wid, "partId": "pt_nope"}, nil); st != 404 || b["code"] != "part_not_found" {
		t.Fatalf("unknown part %d %v", st, b)
	}

	// read-only: agents stay out, the owner's shell does not
	if st, b := h.do("PATCH", "/api/projects/"+pid+"/parts/"+fid, map[string]any{"readOnly": true}, nil); st != 200 || b["parts"].([]any)[1].(map[string]any)["readOnly"] != true {
		t.Fatalf("read-only %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "workspaceId": wid, "partId": fid}, nil); st != 422 || b["code"] != "part_read_only" {
		t.Fatalf("agent in a read-only part %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions/"+s1["id"].(string)+"/restart", map[string]any{"agent": "fake-claude"}, nil); st != 422 || b["code"] != "part_read_only" {
		t.Fatalf("shell turned agent in a read-only part %d %v", st, b)
	}
	h.spawnAgent(wid) // the first part is not read-only

	// a part with terminals stays until they are closed; its folder stays on disk
	if st, b := h.do("DELETE", "/api/projects/"+pid+"/parts/"+fid, nil, nil); st != 409 || b["code"] != "part_busy" || b["sessions"] != float64(2) {
		t.Fatalf("busy %d %v", st, b)
	}
	for _, s := range []map[string]any{s1, s2} {
		if st, b := h.do("DELETE", "/api/sessions/"+s["id"].(string), nil, nil); st != 200 {
			t.Fatalf("close %d %v", st, b)
		}
	}
	if st, b := h.do("DELETE", "/api/projects/"+pid+"/parts/main", nil, nil); st != 409 || b["code"] != "main_part" {
		t.Fatalf("first part %d %v", st, b)
	}
	if st, b := h.do("DELETE", "/api/projects/"+pid+"/parts/"+fid, nil, nil); st != 200 || len(b["parts"].([]any)) != 1 {
		t.Fatalf("delete %d %v", st, b)
	}
	if _, err := os.Stat(web); err != nil {
		t.Fatalf("the folder went: %v", err)
	}
}

// TestPartWorkbench: the Code workbench of a part sees that part's files and git, by the project
// ref "<project>~<part>" or ?part=, and saves inside it (ROADMAP #49).
func TestPartWorkbench(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	api, web := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(api, "server.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(web, "index.html"), []byte("<p>hi</p>\n"), 0o644)
	git(t, web, "init", "-q")
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": api, "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid := p["id"].(string)
	st, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Web", "machineId": "local", "path": web}, nil)
	if st != 201 {
		t.Fatalf("part %d %v", st, p)
	}
	part := p["parts"].([]any)[1].(map[string]any)["id"].(string)

	names := func(url string) string {
		t.Helper()
		st, l := h.do("GET", url, nil, nil)
		if st != 200 {
			t.Fatalf("%s: %d %v", url, st, l)
		}
		out := ""
		for _, it := range l["items"].([]any) {
			out += it.(string) + " "
		}
		return out
	}
	if got := names("/api/projects/" + pid + "/files"); got != "server.go " {
		t.Fatalf("first part %q", got)
	}
	for _, url := range []string{"/api/projects/" + pid + "~" + part + "/files", "/api/projects/" + pid + "/files?part=" + part} {
		if got := names(url); got != "index.html " {
			t.Fatalf("%s: %q", url, got)
		}
	}
	if st, b := h.do("GET", "/api/projects/"+pid+"~pt_nope/files", nil, nil); st != 404 || b["code"] != "part_not_found" {
		t.Fatalf("unknown part %d %v", st, b)
	}
	// git: the part's own repository, not the first part's (which has none)
	if _, s := h.do("GET", "/api/projects/"+pid+"/git/status", nil, nil); s["isRepo"] != false {
		t.Fatalf("first part git %v", s)
	}
	if st, s := h.do("GET", "/api/projects/"+pid+"~"+part+"/git/status", nil, nil); st != 200 || s["isRepo"] != true {
		t.Fatalf("part git %d %v", st, s)
	}
	// saving with the project (any part's folder on that machine) or the part ref
	for _, ref := range []string{pid, pid + "~" + part} {
		if st, b := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": filepath.Join(web, "a.txt"), "content": "x", "projectId": ref}, nil); st != 200 {
			t.Fatalf("write in the part with %s: %d %v", ref, st, b)
		}
	}
	if st, b := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": filepath.Join(t.TempDir(), "b.txt"), "content": "x", "projectId": pid + "~" + part}, nil); st != 403 || b["code"] != "outside_project" {
		t.Fatalf("write outside %d %v", st, b)
	}
}
