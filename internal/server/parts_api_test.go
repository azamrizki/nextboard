package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/project"
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

// TestCreateWithParts: a project made in several parts at once; a wrong folder makes nothing and
// says which one it is (ROADMAP #49).
func TestCreateWithParts(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	api, web, taken := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(api, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, b := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": taken, "name": "Taken"}, nil); st != 201 {
		t.Fatalf("other project %d %v", st, b)
	}
	count := len(h.srv.projects.List())
	extra := func(parts ...map[string]any) map[string]any {
		return map[string]any{"machineId": "local", "path": api, "partName": "Api", "parts": parts}
	}
	part := func(name, machineID, dir string) map[string]any {
		return map[string]any{"name": name, "machineId": machineID, "path": dir}
	}
	for _, c := range []struct {
		why  string
		body map[string]any
		st   int
		code string
		at   float64
	}{
		{"a folder that is not there", extra(part("Web", "local", filepath.Join(web, "nope"))), 400, "", 1},
		{"a machine that is not there", extra(part("Web", "m_nope", web)), 404, "", 1},
		{"a name twice", extra(part("api", "local", web)), 409, "name_conflict", 1},
		{"a folder inside another", extra(part("Sub", "local", filepath.Join(api, "sub"))), 409, "part_overlap", 1},
		{"a folder of another project", extra(part("Web", "local", taken)), 409, "path_in_project", 1},
		{"its own folder in another project", map[string]any{"machineId": "local", "path": taken, "parts": []any{part("Web", "local", web)}}, 409, "path_in_project", 0},
		{"its own folder not there", map[string]any{"machineId": "local", "path": filepath.Join(api, "nope"), "parts": []any{part("Web", "local", web)}}, 400, "", 0},
	} {
		st, b := h.do("POST", "/api/projects", c.body, nil)
		if st != c.st || (c.code != "" && b["code"] != c.code) || b["part"] != c.at || b["error"] == nil {
			t.Fatalf("%s: %d %v", c.why, st, b)
		}
	}
	many := []map[string]any{}
	for i := range project.MaxParts {
		many = append(many, part(fmt.Sprintf("P%d", i), "local", t.TempDir()))
	}
	if st, b := h.do("POST", "/api/projects", extra(many...), nil); st != 409 || b["code"] != "too_many_parts" {
		t.Fatalf("too many %d %v", st, b)
	}
	if n := len(h.srv.projects.List()); n != count {
		t.Fatalf("a refused project was made: %d projects, %d before", n, count)
	}

	// made: its own folder is the first part, named; the others follow in order
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": api, "name": "Shop", "partName": "Api", "parts": []any{part("Web", "", web+"/./")}}, nil)
	parts, _ := p["parts"].([]any)
	if st != 201 || p["name"] != "Shop" || p["path"] != api || len(parts) != 2 {
		t.Fatalf("made %d %v", st, p)
	}
	first, second := parts[0].(map[string]any), parts[1].(map[string]any)
	if first["id"] != "main" || first["name"] != "Api" || second["name"] != "Web" || second["path"] != web || second["machineId"] != "local" {
		t.Fatalf("parts %v", parts)
	}
	// the first part's name alone is enough, and an unnamed one is Main
	solo := t.TempDir()
	if st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": solo, "partName": "Backend"}, nil); st != 201 || p["parts"].([]any)[0].(map[string]any)["name"] != "Backend" {
		t.Fatalf("named first part %d %v", st, p)
	}
	// an unnamed part takes its folder's name
	one, two := t.TempDir(), t.TempDir()
	if st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": one, "parts": []any{part("", "local", two)}}, nil); st != 201 ||
		p["parts"].([]any)[0].(map[string]any)["name"] != "Main" || p["parts"].([]any)[1].(map[string]any)["name"] != filepath.Base(two) {
		t.Fatalf("unnamed parts %d %v", st, p)
	}
	// one part whose folder is already a project opens that project, as a plain add does
	if st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": taken, "partName": "Backend"}, nil); st != 200 || p["name"] != "Taken" {
		t.Fatalf("one part in a project's folder %d %v", st, p)
	}
	if _, created, err := h.srv.projects.CreateWithParts("", []project.NewPart{{MachineID: "local", Path: t.TempDir()}}); err != nil || !created {
		t.Fatal(created, err)
	}
	// a plain add of a folder already in a project still opens that project
	if st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": web}, nil); st != 200 || p["name"] != "Shop" {
		t.Fatalf("a part's folder opens its project %d %v", st, p)
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

// TestPartsForAgents: an agent reaches the other parts of its project through the hub's tools. Those
// on its own machine it may change; those on another machine it only reads unless the project gives
// full access; a read-only part stays read only (ROADMAP #49).
func TestPartsForAgents(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	bin := spyCodex(t, h)
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

	sa := newSpyAgents(t, h, bin, wid)
	start, tools, call := sa.start, sa.tools, sa.call
	// a part on another machine (never reached here: refusals come before the machine is)
	if _, _, err := h.srv.projects.AddPart(pid, "Edge", "m_far", "/srv/edge"); err != nil {
		t.Fatal(err)
	}
	// an agent in the first part, on This PC: the other parts through the hub; those on This PC it
	// may change too, the one on another machine it only reads
	sid, tok, _, env := start(map[string]any{})
	for _, want := range []string{"This project spans several parts", "(yours, where you work)", "- Web: This PC, " + web + " (you may change it too)",
		"- Ops: This PC, " + ops + " (read only for you)", "- Edge: m_far, /srv/edge (read only for you)", "write_file, edit_file and bash where you may change it"} {
		if !strings.Contains(env, want) {
			t.Fatalf("prompt without %q:\n%s", want, env)
		}
	}
	ts := tools(sid, tok)
	if ts["write_file"] == nil || ts["bash"] == nil || ts["read_file"] == nil {
		t.Fatalf("write tools for the parts on This PC: %v", ts)
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
	if out := call(sid, tok, "read_file", map[string]any{"path": "index.html"}); !strings.Contains(out, "use your own tools") {
		t.Fatalf("its own part on This PC: %s", out)
	}
	// the same machine: changes and commands without full access
	if out := call(sid, tok, "write_file", map[string]any{"part": "Web", "path": "new.txt", "content": "hi"}); strings.Contains(out, "isError:true") {
		t.Fatalf("write in Web: %s", out)
	}
	if b, err := os.ReadFile(filepath.Join(web, "new.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("written %q %v", b, err)
	}
	if out := call(sid, tok, "bash", map[string]any{"part": "Web", "command": "pwd"}); !strings.Contains(out, web) {
		t.Fatalf("bash in Web: %s", out)
	}
	// another machine, and a read-only part: read only
	if out := call(sid, tok, "write_file", map[string]any{"part": "Edge", "path": "x.txt", "content": "x"}); !strings.Contains(out, "Edge is read only for you") {
		t.Fatalf("write on another machine: %s", out)
	}
	if out := call(sid, tok, "bash", map[string]any{"part": "Edge", "command": "pwd"}); !strings.Contains(out, "Edge is read only for you") {
		t.Fatalf("a command on another machine: %s", out)
	}
	if out := call(sid, tok, "write_file", map[string]any{"part": "Ops", "path": "x.txt", "content": "x"}); !strings.Contains(out, "Ops is read only for you") {
		t.Fatalf("write in the read-only part: %s", out)
	}

	// full access: the part on another machine takes changes too, the read-only one still not
	h.do("PATCH", "/api/projects/"+pid, map[string]any{"partAccess": "full"}, nil)
	fid, ftok, _, fenv := start(map[string]any{})
	if !strings.Contains(fenv, "- Edge: m_far, /srv/edge (you may change it too)") || !strings.Contains(fenv, "- Ops: This PC, "+ops+" (read only for you)") {
		t.Fatalf("prompt with full access:\n%s", fenv)
	}
	if out := call(fid, ftok, "write_file", map[string]any{"part": "Edge", "path": "x.txt", "content": "x"}); strings.Contains(out, "read only") {
		t.Fatalf("write on another machine with full access: %s", out)
	}
	if out := call(fid, ftok, "write_file", map[string]any{"part": "Ops", "path": "x.txt", "content": "x"}); !strings.Contains(out, "Ops is read only for you") {
		t.Fatalf("write in the read-only part with full access: %s", out)
	}
	h.do("PATCH", "/api/projects/"+pid, map[string]any{"partAccess": ""}, nil)
	// a part's own blocked commands hold in that part, not in the others
	webID := ""
	for _, x := range p["parts"].([]any) {
		if x.(map[string]any)["name"] == "Web" {
			webID = x.(map[string]any)["id"].(string)
		}
	}
	if st, b := h.do("PATCH", "/api/projects/"+pid+"/parts/"+webID, map[string]any{"blocked": []string{"two\nlines"}}, nil); st != 400 || b["code"] != "bad_blocked" {
		t.Fatalf("bad blocked %d %v", st, b)
	}
	if st, b := h.do("PATCH", "/api/projects/"+pid+"/parts/"+webID, map[string]any{"blocked": []string{" touch ", "touch", ""}}, nil); st != 200 || len(b["parts"].([]any)[1].(map[string]any)["blocked"].([]any)) != 1 {
		t.Fatalf("blocked %d %v", st, b)
	}
	if out := call(sid, tok, "bash", map[string]any{"part": "Web", "command": "touch made.txt"}); !strings.Contains(out, "the owner blocked this command") {
		t.Fatalf("a blocked command in Web: %s", out)
	}
	if _, err := os.Stat(filepath.Join(web, "made.txt")); err == nil {
		t.Fatal("the blocked command ran")
	}
	if got := h.srv.blockedIn(pid, webID); !slices.Contains(got, "touch") {
		t.Fatalf("launch list of the part %v", got)
	}
	if got := h.srv.blockedIn(pid, ""); slices.Contains(got, "touch") {
		t.Fatalf("the first part takes the other part's list %v", got)
	}

	// an agent whose own part is read only: no local tools; there the hub's read tools only
	rid, rtok, argv, env := start(map[string]any{"partId": opsID})
	if !strings.Contains(argv, "shell_tool") || !strings.Contains(argv, "read-only") {
		t.Fatalf("local tools stay on:\n%s", argv)
	}
	if !strings.Contains(env, "Your own part is read only") || !strings.Contains(env, "on this computer, which is read only for you") {
		t.Fatalf("prompt:\n%s", env)
	}
	if ts := tools(rid, rtok); ts["grep"] == nil || ts["write_file"] == nil {
		t.Fatalf("read tools, and write tools for the other parts on This PC: %v", ts)
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

// TestReviewPerPart: each part has its own review and local history, reached by its project ref;
// changes in one part's folder show in that part's review only (ROADMAP #49).
func TestReviewPerPart(t *testing.T) {
	h := newP7Hub(t)
	api, web := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(api, "a.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(web, "b.txt"), []byte("bee\n"), 0o644)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": api, "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid := p["id"].(string)
	_, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Web", "machineId": "local", "path": web}, nil)
	part := p["parts"].([]any)[1].(map[string]any)["id"].(string)
	key := pid + "~" + part
	if got := h.srv.reviewKeyOf(pid, part); got != key {
		t.Fatalf("key %q", got)
	}
	if got := h.srv.reviewKeyOf(pid, ""); got != pid {
		t.Fatalf("first part key %q", got)
	}
	if got := h.srv.reviewKeyAt(pid, "local", filepath.Join(web, "b.txt")); got != key {
		t.Fatalf("key of a file in the part %q", got)
	}
	if err := h.srv.review.Snapshot(key); err != nil {
		t.Fatal(err)
	}
	if err := h.srv.review.Snapshot(pid); err != nil {
		t.Fatal(err)
	}
	rv := h.srv.review
	rv.SessionChanged(key, "s_web", "@Bayu", "claude", true, true)
	time.Sleep(300 * time.Millisecond)
	os.WriteFile(filepath.Join(web, "b.txt"), []byte("bee!\n"), 0o644)
	rv.SessionChanged(key, "s_web", "", "", false, false)

	files := func(ref string) int {
		var sum struct {
			Supported bool `json:"supported"`
			Agents    []struct {
				Files []struct {
					Path string `json:"path"`
				} `json:"files"`
			} `json:"agents"`
		}
		h.getJSON("/api/projects/"+ref+"/review", &sum)
		n := 0
		for _, a := range sum.Agents {
			n += len(a.Files)
		}
		return n
	}
	waitTrue(t, "the part's change", 10*time.Second, func() bool { return files(key) == 1 })
	if n := files(pid); n != 0 {
		t.Fatalf("the first part's review shows the other part's change: %d", n)
	}
	if st, b := h.do("GET", "/api/projects/"+pid+"~pt_nope/review", nil, nil); st != 404 || b["code"] != "part_not_found" {
		t.Fatalf("unknown part %d %v", st, b)
	}
}

// TestMonitorPart: a monitor names the part of the project it watches; the part must be the
// project's, and nothing of it reaches the public status page.
func TestMonitorPart(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid := p["id"].(string)
	_, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "SECRET-PART", "machineId": "local", "path": t.TempDir()}, nil)
	part := p["parts"].([]any)[1].(map[string]any)["id"].(string)
	if st, b := h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "api", "type": "http", "url": "http://a.example/", "partId": "pt_nope"}, nil); st != 404 || b["code"] != "part_not_found" {
		t.Fatalf("unknown part %d %v", st, b)
	}
	st, m := h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "api", "type": "http", "url": "http://127.0.0.1:9/", "partId": part, "public": true, "publicName": "Website", "active": false}, nil)
	if st != 201 || m["partId"] != part {
		t.Fatalf("monitor %d %v", st, m)
	}
	if st, b := h.do("PATCH", "/api/uptime/"+m["id"].(string), map[string]any{"partId": "pt_gone"}, nil); st != 404 || b["code"] != "part_not_found" {
		t.Fatalf("update to an unknown part %d %v", st, b)
	}
	_, c := h.do("PUT", "/api/projects/"+pid+"/uptime/public", map[string]any{"enabled": true, "title": "Status"}, nil)
	req, _ := http.NewRequest("GET", h.ts.URL+"/status/"+c["token"].(string), nil)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var page bytes.Buffer
	page.ReadFrom(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(page.String(), "Website") {
		t.Fatalf("%d %s", resp.StatusCode, page.String())
	}
	for _, secret := range []string{"SECRET-PART", part, "partId"} {
		if strings.Contains(page.String(), secret) {
			t.Errorf("%q is on the public page", secret)
		}
	}
}

// TestMergeAndSplit: a project kept apart becomes a part of another with its boards, terminals,
// monitors and memory; its critical notes join only when asked; a part becomes a project again.
func TestMergeAndSplit(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	api, web := t.TempDir(), t.TempDir()
	st, a := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": api, "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, a)
	}
	st, b := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": web, "name": "Shop Web"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, b)
	}
	aid, bid := a["id"].(string), b["id"].(string)
	bws := b["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	_, sh := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": bws}, nil)
	_, mon := h.do("POST", "/api/projects/"+bid+"/uptime", map[string]any{"name": "web", "type": "http", "url": "http://127.0.0.1:9/", "public": true, "publicName": "Website", "active": false}, nil)
	h.do("PUT", "/api/projects/"+bid+"/uptime/public", map[string]any{"enabled": true, "title": "Status"}, nil)
	h.do("PUT", "/api/projects/"+bid+"/memory", map[string]any{"scope": "project", "content": "- the web build runs on node 22\n"}, nil)
	h.do("PUT", "/api/memory/docs/design?projectId="+bid, map[string]any{"title": "Design", "content": "- buttons are square\n"}, nil)
	h.do("PUT", "/api/memory/docs/critical?projectId="+bid, map[string]any{"content": "- never deploy on friday\n"}, nil)
	h.srv.orch.Threads.Add(orch.ThreadMessage{ThreadID: bid, Role: "user", Kind: "order", Text: "build the web shop", At: time.Now().UTC().Add(-time.Hour)})

	if st, v := h.do("GET", "/api/projects/"+aid+"/merge?from="+aid, nil, nil); st != 200 || v["code"] != "merge_self" {
		t.Fatalf("preview of itself %d %v", st, v)
	}
	st, v := h.do("GET", "/api/projects/"+aid+"/merge?from="+bid, nil, nil)
	if st != 200 || v["problem"] != nil || len(v["parts"].([]any)) != 1 || v["parts"].([]any)[0].(map[string]any)["name"] != "Shop Web" ||
		v["sessions"] != float64(1) || v["monitors"] != float64(1) || v["publicPage"] != true || v["criticalChars"].(float64) == 0 || v["criticalFits"] != true || v["subMemories"] != float64(1) {
		t.Fatalf("preview %d %v", st, v)
	}
	if st, e := h.do("POST", "/api/projects/"+aid+"/merge", map[string]any{"from": "p_nope"}, nil); st != 404 {
		t.Fatalf("unknown %d %v", st, e)
	}
	st, m := h.do("POST", "/api/projects/"+aid+"/merge", map[string]any{"from": bid, "critical": "append"}, nil)
	if st != 200 || len(m["parts"].([]any)) != 2 || len(m["workspaces"].([]any)) != 2 || fmt.Sprint(m["aliases"]) != "["+bid+"]" {
		t.Fatalf("merge %d %v", st, m)
	}
	part := m["parts"].([]any)[1].(map[string]any)
	if part["path"] != web || part["name"] != "Shop Web" {
		t.Fatalf("part %v", part)
	}
	pid := part["id"].(string)
	if _, list := h.do("GET", "/api/projects", nil, nil); strings.Contains(fmt.Sprint(list), bid+" ") {
		t.Fatalf("the merged project is still listed")
	}
	// the terminal moved, in its part, without a restart
	if _, s := h.do("GET", "/api/sessions/"+sh["id"].(string), nil, nil); s["projectId"] != aid || s["partId"] != pid || s["workspaceId"] != bws {
		t.Fatalf("session %v", s)
	}
	// the monitor moved, off the public page, in the part
	_, ups := h.do("GET", "/api/projects/"+aid+"/uptime", nil, nil)
	mons := ups["items"].([]any)
	if len(mons) != 1 || mons[0].(map[string]any)["id"] != mon["id"] || mons[0].(map[string]any)["public"] == true || mons[0].(map[string]any)["partId"] != pid {
		t.Fatalf("monitors %v", ups)
	}
	// the memory: a sub-memory of its own, its pages, and the critical notes as asked
	mem := h.srv.orch.Memory
	if sub, err := mem.Sub(aid, "shop-web"); err != nil || !strings.Contains(sub.Content, "node 22") {
		t.Fatalf("memory %v %v", sub, err)
	}
	if sub, err := mem.Sub(aid, "shop-web-design"); err != nil || !strings.Contains(sub.Content, "square") {
		t.Fatalf("its page %v %v", sub, err)
	}
	if c, _ := mem.Sub(aid, "critical"); !strings.Contains(c.Content, "## From Shop Web") || !strings.Contains(c.Content, "never deploy on friday") {
		t.Fatalf("critical %q", c.Content)
	}
	last := h.srv.orch.Threads.Last(aid, 5)
	if len(last) < 2 || !strings.Contains(last[len(last)-1].Text, "Shop Web is now part of this project") || !strings.Contains(fmt.Sprint(last), "build the web shop") {
		t.Fatalf("thread %+v", last)
	}
	if len(h.srv.orch.Threads.Last(bid, 5)) != 0 {
		t.Fatal("the merged project's thread is still there")
	}

	// split it again: not while a terminal works in it
	if st, e := h.do("POST", "/api/projects/"+aid+"/parts/"+pid+"/split", nil, nil); st != 409 || e["code"] != "part_busy" {
		t.Fatalf("busy %d %v", st, e)
	}
	h.do("DELETE", "/api/sessions/"+sh["id"].(string), nil, nil)
	if st, e := h.do("POST", "/api/projects/"+aid+"/parts/main/split", nil, nil); st != 409 || e["code"] != "main_part" {
		t.Fatalf("first part %d %v", st, e)
	}
	st, sp := h.do("POST", "/api/projects/"+aid+"/parts/"+pid+"/split", nil, nil)
	if st != 201 {
		t.Fatalf("split %d %v", st, sp)
	}
	np, from := sp["project"].(map[string]any), sp["from"].(map[string]any)
	if np["path"] != web || np["name"] != "Shop Web" || len(from["parts"].([]any)) != 1 || len(np["workspaces"].([]any)) != 1 {
		t.Fatalf("split %v", sp)
	}
}

// spyAgents starts consoles of a fake Codex that records its launch, and calls the hub's tools
// as that console.
type spyAgents struct {
	t        *testing.T
	h        *testHub
	bin, wid string
}

func newSpyAgents(t *testing.T, h *testHub, bin, wid string) *spyAgents {
	return &spyAgents{t: t, h: h, bin: bin, wid: wid}
}

// start opens a console with body's fields and returns its id, token, argv and environment.
func (a *spyAgents) start(body map[string]any) (string, string, string, string) {
	t := a.t
	t.Helper()
	body["agent"], body["workspaceId"] = "spycodex", a.wid
	st, in := a.h.do("POST", "/api/sessions", body, nil)
	if st != 201 {
		t.Fatalf("console %d %v", st, in)
	}
	var argv, env string
	waitTrue(t, "started", 20*time.Second, func() bool {
		b, _ := os.ReadFile(a.bin + ".argv")
		e, _ := os.ReadFile(a.bin + ".env")
		argv, env = string(b), string(e)
		return strings.Contains(env, "NB_TOKEN=")
	})
	os.Remove(a.bin + ".argv")
	os.Remove(a.bin + ".env")
	tok := ""
	for _, l := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(l, "NB_TOKEN="); ok {
			tok = v
		}
	}
	return in["id"].(string), tok, argv, env
}

func (a *spyAgents) rpc(sid, tok, method string, params any) map[string]any {
	t := a.t
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", strings.TrimSuffix(a.h.srv.hookURL, "/nb/hook")+"/nb/mcp/"+sid, bytes.NewReader(body))
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

func (a *spyAgents) tools(sid, tok string) map[string]map[string]any {
	a.t.Helper()
	out := map[string]map[string]any{}
	for _, x := range a.rpc(sid, tok, "tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
		m := x.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

func (a *spyAgents) call(sid, tok, name string, args map[string]any) string {
	a.t.Helper()
	return fmt.Sprint(a.rpc(sid, tok, "tools/call", map[string]any{"name": name, "arguments": args}))
}

// spyCodex registers the fake Codex and returns its path.
func spyCodex(t *testing.T, h *testHub) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "spycodex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+bin+".argv\nenv > "+bin+".env\nexec sleep 120\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "spycodex", "name": "Spy", "command": bin, "base": "codex"}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	return bin
}

// TestPartsOnAServer: a project with a part on This PC and parts on a real (throwaway) sshd. An
// agent on This PC reads the server's part, and changes it with full access; one on the server
// changes the server's other part and reads This PC's; the workbench and a terminal open in the
// server's part, and the read-only part there stays read only.
func TestPartsOnAServer(t *testing.T) {
	port, key := startSSHServer(t)
	h := newTestHub(t)
	h.setup()
	mid := h.connectMachine(port, key)
	bin := spyCodex(t, h)
	local, api, jobs, prod := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(local, "index.html"), []byte("<p>shop</p>\n"), 0o644)
	os.WriteFile(filepath.Join(api, "routes.php"), []byte("<?php // routes\n"), 0o644)
	os.WriteFile(filepath.Join(prod, ".env"), []byte("APP_ENV=production\n"), 0o644)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": local, "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	if st, b := h.do("PATCH", "/api/projects/"+pid+"/parts/main", map[string]any{"name": "Web"}, nil); st != 200 {
		t.Fatalf("rename %d %v", st, b)
	}
	st, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Api", "machineId": mid, "path": api}, nil)
	if st != 201 {
		t.Fatalf("server part %d %v", st, p)
	}
	h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Jobs", "machineId": mid, "path": jobs}, nil)
	_, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Prod", "machineId": mid, "path": prod}, nil)
	apiID := p["parts"].([]any)[1].(map[string]any)["id"].(string)
	prodID := p["parts"].([]any)[3].(map[string]any)["id"].(string)
	h.do("PATCH", "/api/projects/"+pid+"/parts/"+prodID, map[string]any{"readOnly": true}, nil)

	// the workbench and a terminal in the server's part
	st, l := h.do("GET", "/api/projects/"+pid+"~"+apiID+"/files", nil, nil)
	if st != 200 || fmt.Sprint(l["items"]) != "[routes.php]" {
		t.Fatalf("files over SSH %d %v", st, l)
	}
	st, sh := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": wid, "partId": apiID}, nil)
	if st != 201 || sh["machineId"] != mid || sh["cwd"] != api || sh["partId"] != apiID {
		t.Fatalf("terminal in the server's part %d %v", st, sh)
	}
	h.do("DELETE", "/api/sessions/"+sh["id"].(string), nil, nil)

	sa := newSpyAgents(t, h, bin, wid)
	// an agent on This PC: the server's parts through the hub, over SSH, read only at first
	sid, tok, _, env := sa.start(map[string]any{})
	if !strings.Contains(env, "- Api: srv, "+api+" (read only for you)") {
		t.Fatalf("prompt names the server's part:\n%s", env)
	}
	if out := sa.call(sid, tok, "read_file", map[string]any{"part": "api", "path": "routes.php"}); !strings.Contains(out, "// routes") {
		t.Fatalf("read on the server: %s", out)
	}
	if out := sa.call(sid, tok, "write_file", map[string]any{"part": "Api", "path": "new.php", "content": "<?php\n"}); !strings.Contains(out, "Api is read only for you") {
		t.Fatalf("write on another machine without full access: %s", out)
	}
	if _, err := os.Stat(filepath.Join(api, "new.php")); err == nil {
		t.Fatal("written without full access")
	}
	// full access: it changes the server's part too
	h.do("PATCH", "/api/projects/"+pid, map[string]any{"partAccess": "full"}, nil)
	sid, tok, _, _ = sa.start(map[string]any{})
	if out := sa.call(sid, tok, "write_file", map[string]any{"part": "Api", "path": "new.php", "content": "<?php\n"}); strings.Contains(out, "isError:true") {
		t.Fatalf("write on the server: %s", out)
	}
	if b, err := os.ReadFile(filepath.Join(api, "new.php")); err != nil || string(b) != "<?php\n" {
		t.Fatalf("written over SSH %q %v", b, err)
	}
	if out := sa.call(sid, tok, "bash", map[string]any{"part": "Api", "command": "pwd"}); !strings.Contains(out, api) {
		t.Fatalf("bash on the server: %s", out)
	}
	if out := sa.call(sid, tok, "write_file", map[string]any{"part": "Prod", "path": ".env", "content": "APP_ENV=oops\n"}); !strings.Contains(out, "Prod is read only for you") {
		t.Fatalf("write in the read-only part: %s", out)
	}
	h.do("PATCH", "/api/projects/"+pid, map[string]any{"partAccess": ""}, nil)

	// an agent on the server's part: its own part over SSH, This PC's part through the hub
	rid, rtok, argv, _ := sa.start(map[string]any{"partId": apiID})
	if !strings.Contains(argv, "shell_tool") {
		t.Fatalf("a server's agent keeps local tools:\n%s", argv)
	}
	if out := sa.call(rid, rtok, "read_file", map[string]any{"path": "routes.php"}); !strings.Contains(out, "// routes") {
		t.Fatalf("its own part: %s", out)
	}
	if out := sa.call(rid, rtok, "read_file", map[string]any{"part": "Web", "path": "index.html"}); !strings.Contains(out, "<p>shop</p>") {
		t.Fatalf("This PC's part from the server's agent: %s", out)
	}
	if out := sa.call(rid, rtok, "write_file", map[string]any{"part": "Web", "path": "x.html", "content": "x"}); !strings.Contains(out, "Web is read only for you") {
		t.Fatalf("write in This PC's part from the server's agent: %s", out)
	}
	// the server's other part, on its own machine: changes and commands without full access
	if out := sa.call(rid, rtok, "write_file", map[string]any{"part": "Jobs", "path": "job.php", "content": "<?php // job\n"}); strings.Contains(out, "isError:true") {
		t.Fatalf("write in Jobs: %s", out)
	}
	if b, err := os.ReadFile(filepath.Join(jobs, "job.php")); err != nil || string(b) != "<?php // job\n" {
		t.Fatalf("written over SSH %q %v", b, err)
	}
	if out := sa.call(rid, rtok, "bash", map[string]any{"part": "Jobs", "command": "pwd"}); !strings.Contains(out, jobs) {
		t.Fatalf("bash in Jobs: %s", out)
	}
	if out := sa.call(rid, rtok, "write_file", map[string]any{"part": "Prod", "path": ".env", "content": "APP_ENV=oops\n"}); !strings.Contains(out, "Prod is read only for you") {
		t.Fatalf("write in the read-only part from the server's agent: %s", out)
	}

	// an agent in the read-only part on the server only reads there
	oid, otok, _, _ := sa.start(map[string]any{"partId": prodID})
	if out := sa.call(oid, otok, "read_file", map[string]any{"path": ".env"}); !strings.Contains(out, "production") {
		t.Fatalf("read in its read-only part: %s", out)
	}
	if out := sa.call(oid, otok, "bash", map[string]any{"command": "rm .env"}); !strings.Contains(out, "Your part is read only") {
		t.Fatalf("a command in its read-only part: %s", out)
	}
	if _, err := os.Stat(filepath.Join(prod, ".env")); err != nil {
		t.Fatalf("the file went: %v", err)
	}
}
