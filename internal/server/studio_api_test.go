package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/usage"
)

// Phase 7 part B over HTTP: documents and diagrams with a fake runner CLI,
// Code/Markdown studio endpoints, skills (local; SSH is covered in
// internal/machine/studio_ssh_test.go), usage with the fake TUI.

const fakeStudioCLI = `#!/bin/sh
prompt=$(cat)
case "$prompt" in
*nb.diagram/v1*)
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"x","structured_output":{"schema":"nb.diagram/v1","title":"Flow","direction":"TB","nodes":[{"id":"web","label":"Web","note":"","shape":"round","group":""},{"id":"api","label":"API","note":"Go","shape":"box","group":""},{"id":"ok","label":"Valid?","note":"","shape":"diamond","group":""}],"edges":[{"from":"web","to":"api","label":"HTTPS","style":"solid"},{"from":"api","to":"ok","label":"","style":"dashed"}],"groups":[]}}' ;;
*)
  printf '%s\n' '{"type":"system","subtype":"init","session_id":"s"}'
  printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"# Project overview\n\nHello."}]}}'
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"# Project overview\n\nHello.","session_id":"s"}' ;;
esac
`

func (h *testHub) waitStudioJob(id string) map[string]any {
	h.t.Helper()
	for i := 0; i < 400; i++ {
		_, j := h.do("GET", "/api/jobs/"+id, nil, nil)
		if j["state"] != "queued" && j["state"] != "running" {
			time.Sleep(60 * time.Millisecond) // OnFinish after the state change
			return j
		}
		time.Sleep(30 * time.Millisecond)
	}
	h.t.Fatalf("job %s did not finish", id)
	return nil
}

func (h *testHub) studioProject(dir string) (string, string) {
	h.t.Helper()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "studio"}, nil)
	if st != 201 && st != 200 {
		h.t.Fatalf("project %d %v", st, p)
	}
	ws := p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	return p["id"].(string), ws
}

func TestDocumentsAndDiagramsAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	fake := filepath.Join(t.TempDir(), "fake-claude")
	if err := os.WriteFile(fake, []byte(fakeStudioCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": fake}}}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, b)
	}
	pid, wid := h.studioProject(dir)

	st, r := h.do("POST", "/api/projects/"+pid+"/documents", map[string]any{"type": "overview", "effort": "quick", "agent": "claude"}, nil)
	if st != 202 || r["versionId"] == nil {
		t.Fatalf("doc %d %v", st, r)
	}
	doc := r["document"].(map[string]any)
	if j := h.waitStudioJob(r["job"].(map[string]any)["id"].(string)); j["state"] != "done" {
		t.Fatalf("job %v", j)
	}
	st, v := h.do("GET", "/api/documents/"+doc["id"].(string)+"/versions/"+r["versionId"].(string), nil, nil)
	if st != 200 || !strings.HasPrefix(v["markdown"].(string), "# Project overview") || v["version"].(map[string]any)["state"] != "complete" {
		t.Fatalf("version %d %v", st, v)
	}
	if st, list := h.do("GET", "/api/projects/"+pid+"/documents", nil, nil); st != 200 || !strings.Contains(fmt.Sprint(list), doc["id"].(string)) {
		t.Fatalf("list %d %v", st, list)
	}
	if st, s := h.do("GET", "/api/documents/stats?machineId=local&agent=claude", nil, nil); st != 200 || s["quick"].(map[string]any)["runs"] != 1.0 {
		t.Fatalf("stats %d %v", st, s)
	}
	if st, b := h.do("POST", "/api/projects/"+pid+"/documents", map[string]any{"type": "overview", "effort": "x"}, nil); st != 400 || b["code"] != "bad_effort" {
		t.Fatalf("bad effort %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/projects/"+pid+"/documents", map[string]any{"type": "overview", "agent": "gemini"}, nil); st != 400 || b["code"] != "runner_unsupported" {
		t.Fatalf("gemini %d %v", st, b)
	}
	if st, _ := h.do("DELETE", "/api/documents/"+doc["id"].(string), nil, nil); st != 200 {
		t.Fatal("delete doc")
	}
	if st, b := h.do("GET", "/api/documents/"+doc["id"].(string), nil, nil); st != 404 || b["code"] != "document_not_found" {
		t.Fatalf("deleted %d %v", st, b)
	}

	st, r = h.do("POST", "/api/projects/"+pid+"/diagrams", map[string]any{"subject": "requestLifecycle", "effort": "quick"}, nil)
	if st != 202 {
		t.Fatalf("diagram %d %v", st, r)
	}
	if j := h.waitStudioJob(r["job"].(map[string]any)["id"].(string)); j["state"] != "done" {
		t.Fatalf("diagram job %v", j)
	}
	did := r["diagramId"].(string)
	st, d := h.do("GET", "/api/diagrams/"+did, nil, nil)
	if st != 200 || d["state"] != "done" || d["diagram"].(map[string]any)["title"] != "Flow" || d["style"] != "ink" {
		t.Fatalf("get %d %v", st, d)
	}
	st, land := h.do("POST", "/api/diagrams/"+did+"/land", map[string]any{"workspaceId": wid, "x": 0, "y": 0}, nil)
	if st != 200 || len(land["nodeIds"].([]any)) != 3+2+1+1 { // shapes, lines, edge label, title
		t.Fatalf("land %d %v", st, land)
	}
	_, b := h.do("GET", "/api/workspaces/"+wid+"/board", nil, nil)
	n := 0
	for _, x := range b["nodes"].([]any) {
		if x.(map[string]any)["data"].(map[string]any)["diagramId"] == did {
			n++
		}
	}
	if n != 7 {
		t.Fatalf("board has %d diagram nodes", n)
	}
	// wires: the arrow holds its route, in one ink, whatever the shapes
	st, wired := h.do("POST", "/api/diagrams/"+did+"/land", map[string]any{"workspaceId": wid, "x": 0, "y": 900, "style": "color", "arrows": "wiring", "arrowColor": "mono"}, nil)
	if st != 200 {
		t.Fatalf("land wired %d %v", st, wired)
	}
	_, b = h.do("GET", "/api/workspaces/"+wid+"/board", nil, nil)
	routes := 0
	for _, x := range b["nodes"].([]any) {
		data := x.(map[string]any)["data"].(map[string]any)
		if x.(map[string]any)["kind"] != "line" || data["route"] == nil {
			continue
		}
		routes++
		if data["stroke"] != "#e6eaf2" || data["roughness"] != 0.0 {
			t.Fatalf("wire %v", data)
		}
	}
	if routes != 2 {
		t.Fatalf("%d wires on the board", routes)
	}
	if st, b := h.do("POST", "/api/diagrams/"+did+"/land", map[string]any{"workspaceId": "w_nope000000"}, nil); st != 404 {
		t.Fatalf("bad workspace %d %v", st, b)
	}
	if st, l := h.do("GET", "/api/projects/"+pid+"/diagrams", nil, nil); st != 200 || !strings.Contains(fmt.Sprint(l), did) {
		t.Fatalf("diagram list %d %v", st, l)
	}
}

func TestCodeAndMarkdownStudioAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	write("README.md", "# Home\n[[guide]]")
	write("docs/guide.md", "# Guide\n[home](../README.md)")
	write("docs/lonely.md", "alone")
	write("node_modules/a/b.md", "# vendored")
	_ = os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	pid, _ := h.studioProject(dir)
	q := "?path=" + dir
	st, tr := h.do("GET", "/api/machines/local/fs/tree"+q, nil, nil)
	if st != 200 || len(tr["entries"].([]any)) != 2 || !strings.Contains(fmt.Sprint(tr["pruned"]), "node_modules") {
		t.Fatalf("tree %d %v", st, tr)
	}
	if st, b := h.do("GET", "/api/machines/local/fs/tree?path="+h.srv.st.Dir, nil, nil); st != 403 {
		t.Fatalf("hub data dir listed %d %v", st, b)
	}
	if st, f := h.do("GET", "/api/machines/local/fs/find?root="+dir+"&q=guide", nil, nil); st != 200 || len(f["items"].([]any)) != 1 {
		t.Fatalf("find %d %v", st, f)
	}
	if st, b := h.do("POST", "/api/machines/local/fs/mkdir", map[string]any{"path": filepath.Join(dir, "docs")}, nil); st != 409 || b["code"] != "exists" {
		t.Fatalf("mkdir exists %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/machines/local/fs/mkdir", map[string]any{"path": filepath.Join(dir, "new")}, nil); st != 200 || b["dir"] != true {
		t.Fatalf("mkdir %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/machines/local/fs/rename", map[string]any{"from": filepath.Join(dir, "docs/lonely.md"), "to": filepath.Join(dir, "README.md")}, nil); st != 409 || b["code"] != "exists" {
		t.Fatalf("rename over %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/machines/local/fs/trash", map[string]any{"path": "/etc/hostname", "projectId": pid}, nil); st != 403 || b["code"] != "outside_project" {
		t.Fatalf("trash outside %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/machines/local/fs/trash", map[string]any{"path": filepath.Join(dir, "new"), "projectId": pid}, nil); st != 200 || !strings.Contains(b["trashedTo"].(string), "/.git/nextboard-trash/") {
		t.Fatalf("trash %d %v", st, b)
	}
	st, sb := h.do("POST", "/api/machines/local/fs/stat-batch", map[string]any{"paths": []string{filepath.Join(dir, "README.md"), filepath.Join(dir, "gone")}}, nil)
	if st != 200 || !strings.Contains(sb["_raw"].(string), `"exists":true`) {
		t.Fatalf("stat-batch %d %v", st, sb)
	}
	// Save through a symlink that leaves the project needs allowOutside.
	outside := filepath.Join(t.TempDir(), "x.txt")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	_ = os.Symlink(outside, filepath.Join(dir, "link.txt"))
	body := map[string]any{"path": filepath.Join(dir, "link.txt"), "content": "y", "projectId": pid}
	if st, b := h.do("PUT", "/api/machines/local/fs/write", body, nil); st != 403 || b["code"] != "outside_project" {
		t.Fatalf("outside write %d %v", st, b)
	}
	body["allowOutside"] = true
	if st, b := h.do("PUT", "/api/machines/local/fs/write", body, nil); st != 200 {
		t.Fatalf("allowOutside %d %v", st, b)
	}
	// stale ifMtime → 409 mtime_mismatch
	if st, b := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": filepath.Join(dir, "README.md"), "content": "z",
		"ifMtime": "2001-01-01T00:00:00Z", "projectId": pid}, nil); st != 409 || b["code"] != "mtime_mismatch" {
		t.Fatalf("stale %d %v", st, b)
	}
	// Markdown vault, graph, backlinks.
	st, v := h.do("GET", "/api/projects/"+pid+"/markdown", nil, nil)
	if st != 200 || len(v["files"].([]any)) != 3 || len(v["links"].([]any)) != 2 || v["agentMemory"] == nil {
		t.Fatalf("vault %d %v", st, v)
	}
	st, g := h.do("GET", "/api/projects/"+pid+"/markdown/graph", nil, nil)
	if st != 200 || len(g["edges"].([]any)) != 2 || len(g["nodes"].([]any)) != 3 {
		t.Fatalf("graph %d %v", st, g)
	}
	st, bl := h.do("GET", "/api/projects/"+pid+"/markdown/backlinks?path=docs/guide.md", nil, nil)
	if st != 200 || len(bl["backlinks"].([]any)) != 1 || len(bl["outgoing"].([]any)) != 1 {
		t.Fatalf("backlinks %d %v", st, bl)
	}
	// Watch: register, see the change event, delete.
	_, evs := h.eventsCh("")
	h.srv.studio.watcher.Interval = 100 * time.Millisecond
	h.srv.studio.watcher.Start()
	defer h.srv.studio.watcher.Close()
	st, wb := h.do("POST", "/api/machines/local/fs/watch", map[string]any{"paths": []string{filepath.Join(dir, "docs/guide.md")}}, nil)
	if st != 200 || wb["watchId"] == nil {
		t.Fatalf("watch %d %v", st, wb)
	}
	time.Sleep(150 * time.Millisecond)
	_ = os.WriteFile(filepath.Join(dir, "docs/guide.md"), []byte("# Guide v2 longer"), 0o644)
	if ev := waitEvt(t, evs, "fsChanged"); ev["watchId"] != wb["watchId"] {
		t.Fatalf("event %v", ev)
	}
	if st, _ := h.do("DELETE", "/api/fs/watch/"+wb["watchId"].(string), nil, nil); st != 200 {
		t.Fatal("watch delete")
	}
}

func TestSkillsAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	cfg := t.TempDir()
	_ = os.MkdirAll(filepath.Join(cfg, "skills", "plugin-free"), 0o755)
	_ = os.WriteFile(filepath.Join(cfg, "skills", "plugin-free", "SKILL.md"), []byte("---\nname: plugin-free\ndescription: d\n---\nb\n"), 0o644)
	plug := filepath.Join(cfg, "plugins", "cache", "m", "tools", "1")
	_ = os.MkdirAll(filepath.Join(plug, "skills", "ro"), 0o755)
	_ = os.WriteFile(filepath.Join(plug, "skills", "ro", "SKILL.md"), []byte("---\nname: ro\ndescription: r\n---\nx\n"), 0o644)
	inst, _ := json.Marshal(map[string]any{"plugins": map[string]any{"tools@m": []any{map[string]any{"installPath": plug}}}})
	_ = os.WriteFile(filepath.Join(cfg, "plugins", "installed_plugins.json"), inst, 0o644)
	st, pf := h.do("POST", "/api/profiles", map[string]any{"machineId": "local", "agent": "claude", "name": "Test", "dir": cfg}, nil)
	if st != 201 {
		t.Fatalf("profile %d %v", st, pf)
	}
	prof := pf["id"].(string)
	repo := t.TempDir()
	pid, _ := h.studioProject(repo)
	base := "/api/machines/local/skills"
	st, l := h.do("GET", base+"?profileId="+prof+"&projectId="+pid, nil, nil)
	if st != 200 || len(l["items"].([]any)) != 2 || l["cfg"] != cfg {
		t.Fatalf("list %d %v", st, l)
	}
	_, evs := h.eventsCh("")
	st, c := h.do("POST", base, map[string]any{"scope": "personal", "name": "my-skill", "description": "Mine", "body": "Do it", "profileId": prof}, nil)
	if st != 201 || c["id"] != "personal:my-skill" || c["invoke"] != "/my-skill" {
		t.Fatalf("create %d %v", st, c)
	}
	if ev := waitEvt(t, evs, "skills"); ev["machineId"] != "local" {
		t.Fatalf("event %v", ev)
	}
	path := filepath.Join(cfg, "skills", "my-skill", "SKILL.md")
	st, it := h.do("GET", base+"/item?profileId="+prof+"&path="+path, nil, nil)
	if st != 200 || !strings.Contains(it["content"].(string), "Do it") {
		t.Fatalf("item %d %v", st, it)
	}
	if st, b := h.do("PUT", base+"/item", map[string]any{"path": path, "content": "---\nname: my-skill\ndescription: v2\n---\nv2\n",
		"ifMtime": "2001-01-01T00:00:00Z", "profileId": prof}, nil); st != 409 || b["code"] != "mtime_mismatch" {
		t.Fatalf("stale %d %v", st, b)
	}
	if st, b := h.do("PUT", base+"/item", map[string]any{"path": path, "content": "---\nname: my-skill\ndescription: v2\n---\nv2\n", "profileId": prof}, nil); st != 200 || b["description"] != "v2" {
		t.Fatalf("write %d %v", st, b)
	}
	if st, b := h.do("PUT", base+"/item", map[string]any{"path": filepath.Join(plug, "skills", "ro", "SKILL.md"), "content": "x", "profileId": prof}, nil); st != 403 || b["code"] != "read_only" {
		t.Fatalf("plugin %d %v", st, b)
	}
	if st, b := h.do("POST", base+"/duplicate", map[string]any{"path": path, "toScope": "project", "profileId": prof, "projectId": pid}, nil); st != 201 || b["scope"] != "project" {
		t.Fatalf("duplicate %d %v", st, b)
	}
	if st, b := h.do("POST", base+"/lint", map[string]any{"content": "---\nname: Bad\n---\n", "path": "/x/skills/Bad/SKILL.md"}, nil); st != 200 || len(b["lint"].([]any)) < 3 {
		t.Fatalf("lint %d %v", st, b)
	}
	st, del := h.do("DELETE", base+"/item", map[string]any{"path": path, "profileId": prof}, nil)
	if st != 200 || del["backup"] == "" {
		t.Fatalf("delete %d %v", st, del)
	}
	if _, err := os.Stat(filepath.Join(del["backup"].(string), "skills", "my-skill", "SKILL.md")); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if !strings.HasPrefix(del["backup"].(string), h.srv.st.Path("backups", "skills", "local")) {
		t.Fatalf("backup outside hub data: %v", del["backup"])
	}
	if st, s := h.do("GET", "/api/skills/starters", nil, nil); st != 200 || len(s["_raw"].(string)) < 1000 || !strings.Contains(s["_raw"].(string), "commit-message") {
		t.Fatalf("starters %d", st)
	}
	// Run a skill: only into an agent console (shell → 409 not_agent).
	in, err := h.srv.sessions.Create(sessionCreate("shell"))
	if err != nil {
		t.Fatal(err)
	}
	if st, b := h.do("POST", "/api/sessions/"+in.ID+"/skill", map[string]any{"name": "commit-message", "args": "api"}, nil); st != 409 || b["code"] != "not_agent" {
		t.Fatalf("skill into shell %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions/"+in.ID+"/skill", map[string]any{"name": "bad name; rm"}, nil); st != 400 {
		t.Fatalf("bad name %d %v", st, b)
	}
}

func TestUsageAPI(t *testing.T) {
	home := t.TempDir() // token history reads only this "home"
	t.Setenv("HOME", home)
	root, err := os.MkdirTemp("", "nbu-api")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	old := usage.Root
	usage.Root = root
	defer func() { usage.Root = old }()
	h := newTestHub(t)
	h.setup()
	script, err := os.ReadFile("../usage/testdata/fake-tui.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-claude")
	_ = os.WriteFile(fake, script, 0o755)
	_ = os.WriteFile(fake+".env", []byte("FAKE_KIND=claude\nFAKE_TRUST=1\nFAKE_LOG="+filepath.Join(dir, "log")+"\n"), 0o644)
	h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": fake}}}}, nil)
	st, j := h.do("POST", "/api/usage/check", map[string]any{"machineId": "local", "agent": "claude", "maxAgeSec": 0, "trigger": "user"}, nil)
	if st != 202 || j["kind"] != "usage" {
		t.Fatalf("check %d %v", st, j)
	}
	if jj := h.waitStudioJob(j["id"].(string)); jj["state"] != "done" || jj["result"].(map[string]any)["state"] != "ok" {
		t.Fatalf("job %v", jj)
	}
	st, rs := h.do("GET", "/api/usage?machineId=local&agent=claude", nil, nil)
	items := rs["items"].([]any)
	if st != 200 || len(items) != 1 || len(items[0].(map[string]any)["windows"].([]any)) != 2 {
		t.Fatalf("reports %d %v", st, rs)
	}
	if st, r := h.do("POST", "/api/usage/check", map[string]any{"agent": "claude", "maxAgeSec": 180}, nil); st != 200 || r["state"] != "ok" {
		t.Fatalf("cached %d %v", st, r)
	}
	if st, r := h.do("POST", "/api/usage/check", map[string]any{"agent": "opencode"}, nil); st != 200 || r["state"] != "unsupported" {
		t.Fatalf("unsupported %d %v", st, r)
	}
	h.do("PATCH", "/api/settings", map[string]any{"usage": map[string]any{"background": false}}, nil)
	if st, b := h.do("POST", "/api/usage/check", map[string]any{"agent": "claude", "maxAgeSec": 0}, nil); st != 409 || b["code"] != "background_disabled" {
		t.Fatalf("background off %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/usage/check", map[string]any{"machineId": "m_nope", "agent": "claude"}, nil); st != 404 {
		t.Fatalf("unknown machine %d %v", st, b)
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("usage folder left %v", ents)
	}
	// Token history from fixture transcripts in the temp home.
	pdir := filepath.Join(home, ".claude", "projects", "-x")
	_ = os.MkdirAll(pdir, 0o700)
	ts := time.Now().UTC().Format(time.RFC3339)
	_ = os.WriteFile(filepath.Join(pdir, "a.jsonl"), []byte(`{"type":"assistant","timestamp":"`+ts+`","message":{"id":"m1","model":"x","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`+"\n"), 0o600)
	st, tk := h.do("GET", "/api/usage/tokens?days=7&machineId=local", nil, nil)
	if st != 200 || tk["top"] != "claude" || len(tk["series"].([]any)) != 1 || tk["partial"] != false {
		t.Fatalf("tokens %d %v", st, tk)
	}
	if _, err := os.Stat(h.srv.st.Path("usage", "tokens", "local.json")); err != nil {
		t.Fatal("index not persisted")
	}
	// settings validation of the new keys
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"usage": map[string]any{"chime": map[string]any{"play": "never"}}}, nil); st != 400 || b["code"] != "bad_setting" {
		t.Fatalf("chime play %d %v", st, b)
	}
}
