package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProjectParts: a project spans folders as parts; a terminal opens in a part, and an agent
// in a part marked read-only only reads (ROADMAP #49).
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
	// an agent there works through the hub's tools, which only read; one that cannot stays out
	st, ag := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "workspaceId": wid, "partId": fid}, nil)
	if st != 201 || ag["remoteMachineId"] != "local" || ag["remoteCwd"] != web || ag["partId"] != fid {
		t.Fatalf("agent in a read-only part %d %v", st, ag)
	}
	if st, b := h.do("POST", "/api/agents", map[string]any{"id": "plain-cli", "name": "Plain", "command": "/bin/cat"}, nil); st != 201 {
		t.Fatalf("plain agent %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions", map[string]any{"agent": "plain-cli", "workspaceId": wid, "partId": fid}, nil); st != 422 || b["code"] != "part_read_only" {
		t.Fatalf("an agent with no hub tools in a read-only part %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions/"+s1["id"].(string)+"/restart", map[string]any{"agent": "fake-claude"}, nil); st != 200 || b["remoteMachineId"] != "local" || b["remoteCwd"] != web {
		t.Fatalf("shell turned agent in a read-only part %d %v", st, b)
	}
	if st, b := h.do("DELETE", "/api/sessions/"+ag["id"].(string), nil, nil); st != 200 {
		t.Fatalf("close %d %v", st, b)
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

// TestPartsForAgents: an agent reaches the other parts of its project through the hub's tools,
// read only unless the project gives full access; a read-only part stays read only (ROADMAP #49).
func TestPartsForAgents(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	bin := filepath.Join(t.TempDir(), "spycodex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+bin+".argv\nenv > "+bin+".env\nexec sleep 120\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "spycodex", "name": "Spy", "command": bin, "base": "codex"}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	api, web, ops := t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(web, "index.html"), []byte("<p>shop</p>\n"), 0o644)
	os.WriteFile(filepath.Join(ops, "deploy.sh"), []byte("echo deploy\n"), 0o644)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": api, "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Web", "machineId": "local", "path": web}, nil)
	_, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Ops", "machineId": "local", "path": ops}, nil)
	opsID := p["parts"].([]any)[2].(map[string]any)["id"].(string)
	h.do("PATCH", "/api/projects/"+pid+"/parts/"+opsID, map[string]any{"readOnly": true}, nil)
	if st, b := h.do("PATCH", "/api/projects/"+pid, map[string]any{"partAccess": "all"}, nil); st != 400 || b["code"] != "bad_part_access" {
		t.Fatalf("bad access %d %v", st, b)
	}

	start := func(body map[string]any) (string, string, string, string) {
		t.Helper()
		body["agent"], body["workspaceId"] = "spycodex", wid
		st, in := h.do("POST", "/api/sessions", body, nil)
		if st != 201 {
			t.Fatalf("console %d %v", st, in)
		}
		var argv, env string
		waitTrue(t, "started", 20*time.Second, func() bool {
			a, _ := os.ReadFile(bin + ".argv")
			e, _ := os.ReadFile(bin + ".env")
			argv, env = string(a), string(e)
			return strings.Contains(env, "NB_TOKEN=")
		})
		os.Remove(bin + ".argv")
		os.Remove(bin + ".env")
		tok := ""
		for _, l := range strings.Split(env, "\n") {
			if v, ok := strings.CutPrefix(l, "NB_TOKEN="); ok {
				tok = v
			}
		}
		return in["id"].(string), tok, argv, env
	}
	url := strings.TrimSuffix(h.srv.hookURL, "/nb/hook") + "/nb/mcp/"
	rpc := func(sid, tok, method string, params any) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", url+sid, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	tools := func(sid, tok string) map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		for _, x := range rpc(sid, tok, "tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
			m := x.(map[string]any)
			out[m["name"].(string)] = m
		}
		return out
	}
	call := func(sid, tok, name string, args map[string]any) string {
		t.Helper()
		return fmt.Sprint(rpc(sid, tok, "tools/call", map[string]any{"name": name, "arguments": args}))
	}

	// an agent in the first part, on This PC: the other parts through the hub, read only
	sid, tok, _, env := start(map[string]any{})
	if !strings.Contains(env, "This project spans several parts") || !strings.Contains(env, "- Web: This PC, "+web+" (read only for you)") || !strings.Contains(env, "(yours, where you work)") {
		t.Fatalf("prompt:\n%s", env)
	}
	ts := tools(sid, tok)
	if ts["write_file"] != nil || ts["read_file"] == nil {
		t.Fatalf("read tools only: %v", ts)
	}
	if props := ts["read_file"]["inputSchema"].(map[string]any)["properties"].(map[string]any); props["part"] == nil {
		t.Fatalf("no part argument: %v", props)
	}
	if out := call(sid, tok, "read_file", map[string]any{"part": "web", "path": "index.html"}); !strings.Contains(out, "<p>shop</p>") {
		t.Fatalf("read in Web: %s", out)
	}
	if out := call(sid, tok, "read_file", map[string]any{"part": "Docs", "path": "x"}); !strings.Contains(out, `no part \"Docs\"`) && !strings.Contains(out, "no part") {
		t.Fatalf("unknown part: %s", out)
	}
	if out := call(sid, tok, "write_file", map[string]any{"part": "Web", "path": "x.txt", "content": "x"}); !strings.Contains(out, "Web is read only for you") {
		t.Fatalf("write in a read-only part: %s", out)
	}
	if out := call(sid, tok, "read_file", map[string]any{"path": "index.html"}); !strings.Contains(out, "use your own tools") {
		t.Fatalf("its own part on This PC: %s", out)
	}

	// full access: the other parts take changes and commands too, except the read-only one
	h.do("PATCH", "/api/projects/"+pid, map[string]any{"partAccess": "full"}, nil)
	if ts := tools(sid, tok); ts["write_file"] == nil || ts["bash"] == nil {
		t.Fatalf("write tools with full access: %v", ts)
	}
	if out := call(sid, tok, "write_file", map[string]any{"part": "Web", "path": "new.txt", "content": "hi"}); strings.Contains(out, "isError:true") {
		t.Fatalf("write in Web: %s", out)
	}
	if b, err := os.ReadFile(filepath.Join(web, "new.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("written %q %v", b, err)
	}
	if out := call(sid, tok, "bash", map[string]any{"part": "Web", "command": "pwd"}); !strings.Contains(out, web) {
		t.Fatalf("bash in Web: %s", out)
	}
	if out := call(sid, tok, "write_file", map[string]any{"part": "Ops", "path": "x.txt", "content": "x"}); !strings.Contains(out, "Ops is read only for you") {
		t.Fatalf("write in the read-only part: %s", out)
	}
	h.do("PATCH", "/api/projects/"+pid, map[string]any{"partAccess": ""}, nil)

	// an agent whose own part is read only: no local tools, the hub's read tools only
	rid, rtok, argv, env := start(map[string]any{"partId": opsID})
	if !strings.Contains(argv, "shell_tool") || !strings.Contains(argv, "read-only") {
		t.Fatalf("local tools stay on:\n%s", argv)
	}
	if !strings.Contains(env, "Your own part is read only") || !strings.Contains(env, "on this computer, which is read only for you") {
		t.Fatalf("prompt:\n%s", env)
	}
	if ts := tools(rid, rtok); ts["write_file"] != nil || ts["bash"] != nil || ts["grep"] == nil {
		t.Fatalf("read tools only: %v", ts)
	}
	if out := call(rid, rtok, "read_file", map[string]any{"path": "deploy.sh"}); !strings.Contains(out, "echo deploy") {
		t.Fatalf("read its own part: %s", out)
	}
	if out := call(rid, rtok, "write_file", map[string]any{"path": "deploy.sh", "content": "rm -rf /"}); !strings.Contains(out, "Your part is read only") {
		t.Fatalf("write in its own read-only part: %s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(ops, "deploy.sh")); string(b) != "echo deploy\n" {
		t.Fatalf("changed: %q", b)
	}
}
