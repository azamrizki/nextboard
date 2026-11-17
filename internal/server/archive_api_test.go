package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/project"
)

func listIDs(t *testing.T, h *testHub, path string) []string {
	t.Helper()
	var list []map[string]any
	if st := h.getJSON(path, &list); st != 200 {
		t.Fatalf("GET %s: %d", path, st)
	}
	var out []string
	for _, x := range list {
		out = append(out, x["id"].(string))
	}
	return out
}

func has(list []string, id string) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// TestProjectArchiveRestoreDelete: archiving keeps everything and stops what runs; nothing of
// the project starts while it is archived, also after a hub restart; restoring brings it back;
// deleting for good removes the hub's data of it and never its folder.
func TestProjectArchiveRestoreDelete(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	h := newTestHub(t)
	pid, wid, dir := orchSetup(t, h)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// another project, which nothing here may touch
	st, other := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "other"}, nil)
	if st != 201 {
		t.Fatalf("other %d %v", st, other)
	}
	oid := other["id"].(string)

	agent := h.spawnAgent(wid)["id"].(string)
	h.waitInfo(agent, "agent ready", state("ready"))
	st, sh := h.do("POST", "/api/sessions", map[string]any{"agent": "shell", "workspaceId": wid}, nil)
	if st != 201 {
		t.Fatalf("shell %d %v", st, sh)
	}
	shell := sh["id"].(string)
	h.waitInfo(shell, "shell running", func(in map[string]any) bool { return in["status"] == "running" })

	// what the project has: a board, a closed board in the archive, a monitor, a loop, memory,
	// a thread, a pasted file, a review shadow
	if st, _ := h.ops(wid, noteOp("n_note00001", "keep me")); st != 200 {
		t.Fatalf("note %d", st)
	}
	st, ws2 := h.do("POST", "/api/projects/"+pid+"/workspaces", map[string]any{"name": "Old"}, nil)
	if st != 201 {
		t.Fatalf("workspace %d", st)
	}
	h.ops(ws2["id"].(string), noteOp("n_note00002", "closed"))
	if st, _ := h.do("DELETE", "/api/workspaces/"+ws2["id"].(string), nil, nil); st != 200 {
		t.Fatalf("close workspace %d", st)
	}
	st, mon := h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "cron", "type": "push", "intervalSec": 60, "active": true}, nil)
	if st != 201 {
		t.Fatalf("monitor %d %v", st, mon)
	}
	token := mon["pushToken"].(string)
	loop, err := h.srv.orch.Loops.Create(orch.LoopDef{ProjectID: pid, WorkspaceID: wid, Name: "Guard", Enabled: true, Text: "Run the tests.",
		Trigger: orch.LoopTrigger{Kind: "interval", EverySec: 60}, Targets: orch.LoopTargets{Kind: "allAgents"}}, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{pid, oid} {
		if _, err := h.srv.orch.Memory.Write("project", p, "# Memory\n\nfacts\n", ""); err != nil {
			t.Fatal(err)
		}
	}
	h.srv.orch.Threads.Add(orch.ThreadMessage{ThreadID: pid, At: time.Now(), Role: "hub", Kind: "note", Text: "hello"})
	if st, _, _ := h.doRaw("POST", "/api/projects/"+pid+"/attach?name=notes.txt", "text/plain", []byte("pasted")); st != 200 {
		t.Fatalf("attach %d", st)
	}
	if err := h.srv.review.Snapshot(h.srv.reviewKeyOf(pid, "")); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !exists(h.srv.st.Path("review", h.srv.reviewKeyOf(pid, ""))) || !exists(h.srv.st.Path("attachments", "project-"+pid)) || len(h.srv.boards.ListArchive()) != 1 {
		t.Fatal("setup incomplete")
	}

	ev, _ := h.events()
	st, ap := h.do("POST", "/api/projects/"+pid+"/archive", nil, nil)
	if st != 200 || ap["archivedAt"] == nil {
		t.Fatalf("archive %d %v", st, ap)
	}
	waitEvent(t, ev, func(e map[string]any) bool {
		p, _ := e["project"].(map[string]any)
		return e["type"] == "project" && p["id"] == pid && p["archivedAt"] != nil
	})
	if ids := listIDs(t, h, "/api/projects"); has(ids, pid) || !has(ids, oid) {
		t.Fatalf("active list %v", ids)
	}
	if ids := listIDs(t, h, "/api/projects?archived=1"); len(ids) != 1 || ids[0] != pid {
		t.Fatalf("archived list %v", ids)
	}
	h.waitInfo(agent, "agent asleep", asleep)
	h.waitInfo(shell, "shell ended", func(in map[string]any) bool { return in["status"] == "exited" })
	waitTrue(t, "fake agent gone", 5*time.Second, func() bool { return !fakeRunning(dir) })
	// nothing wakes it while archived
	if st, b := h.do("POST", "/api/sessions/"+agent+"/wake", nil, nil); st != 409 || b["code"] != "project_archived" {
		t.Fatalf("wake %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/projects/"+pid+"/wake", nil, nil); st != 200 || b["woken"] != 0.0 {
		t.Fatalf("project wake %d %v", st, b)
	}
	h.do("POST", "/api/sessions/"+agent+"/input", map[string]any{"data": "hello"}, nil)
	time.Sleep(300 * time.Millisecond)
	if _, in := h.do("GET", "/api/sessions/"+agent, nil, nil); !asleep(in) || fakeRunning(dir) {
		t.Fatalf("typing woke it: %v", in)
	}
	if st, b := h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": "status", "clientId": "c_test0001"}, nil); st != 409 || b["code"] != "project_archived" {
		t.Fatalf("command bar %d %v", st, b)
	}
	if h.srv.uptime.Push(token, false, "", -1) {
		t.Fatal("push taken while archived")
	}
	for _, p := range (hubAdapter{h.srv}).Projects() {
		if p.ID == pid {
			t.Fatal("the lead and MCP tools still list it")
		}
	}
	// everything stays
	if b, err := h.srv.boards.Get(wid); err != nil || len(b.Nodes) < 2 {
		t.Fatalf("board %v %+v", err, b)
	}
	if _, ok := h.srv.orch.Loops.Get(loop.ID); !ok || !exists(filepath.Join(dir, "main.go")) {
		t.Fatal("archive lost something")
	}

	// the hub restarts: nothing of it starts
	h.srv.sessions.Shutdown()
	h.srv.boards.Close()
	h.srv.dataLock.Release() // the first hub is gone
	srv2, err := New(Options{DataDir: h.srv.st.Dir, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv2.review.Close)
	defer srv2.boards.Close()
	defer srv2.sessions.Shutdown()
	srv2.sessions.Start()
	waitTrue(t, "asleep after restart", 5*time.Second, func() bool { in, _ := srv2.sessions.Info(agent); return in.Status == "asleep" })
	waitTrue(t, "exited after restart", 5*time.Second, func() bool { in, _ := srv2.sessions.Info(shell); return in.Status == "exited" })
	time.Sleep(500 * time.Millisecond)
	if fakeRunning(dir) {
		t.Fatal("an archived project's agent started with the hub")
	}
	if _, err := srv2.sessions.Wake(agent); err == nil {
		t.Fatal("woke after restart")
	}
	if pr, ok := srv2.projects.Get(pid); !ok || !pr.Archived() {
		t.Fatal("archive lost on restart")
	}
}

// TestProjectRestoreAndPermanentDelete runs on one hub: restore, the old clients' DELETE, and the
// permanent delete with all it removes.
func TestProjectRestoreAndPermanentDelete(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	h := newTestHub(t)
	pid, wid, dir := orchSetup(t, h)
	st, other := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "other"}, nil)
	if st != 201 {
		t.Fatal(st)
	}
	oid := other["id"].(string)
	agent := h.spawnAgent(wid)["id"].(string)
	h.waitInfo(agent, "agent ready", state("ready"))
	st, mon := h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "cron", "type": "push", "intervalSec": 60, "active": true}, nil)
	if st != 201 {
		t.Fatalf("monitor %d %v", st, mon)
	}
	h.do("PUT", "/api/projects/"+pid+"/uptime/public", map[string]any{"enabled": true, "title": "Status"}, nil)
	h.ops(wid, map[string]any{"op": "put", "node": map[string]any{"id": "n_orchcard01", "kind": "orchestrator", "x": 0, "y": 0, "w": 300, "h": 200, "data": map[string]any{"title": "Lead"}}})
	h.srv.orch.Threads.Add(orch.ThreadMessage{ThreadID: "n_orchcard01", At: time.Now(), Role: "hub", Kind: "note", Text: "card"})
	h.srv.orch.Threads.Add(orch.ThreadMessage{ThreadID: pid, At: time.Now(), Role: "hub", Kind: "note", Text: "lead"})
	for _, p := range []string{pid, oid} {
		if _, err := h.srv.orch.Memory.Write("project", p, "# Memory\n", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := h.srv.orch.Memory.WriteSub(p, "deploy", "Deploy", "", "steps\n", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := h.srv.orch.Loops.Create(orch.LoopDef{ProjectID: p, Name: "Guard", Enabled: true, Text: "Run the tests.",
			Trigger: orch.LoopTrigger{Kind: "interval", EverySec: 60}, Targets: orch.LoopTargets{Kind: "allAgents"}}, time.Now(), false); err != nil {
			t.Fatal(err)
		}
	}
	h.doRaw("POST", "/api/projects/"+pid+"/attach?name=notes.txt", "text/plain", []byte("pasted"))
	reviewDir := h.srv.st.Path("review", h.srv.reviewKeyOf(pid, ""))
	if err := h.srv.review.Snapshot(h.srv.reviewKeyOf(pid, "")); err != nil {
		t.Fatal(err)
	}
	// a closed board of each project in the archive
	for _, p := range []string{pid, oid} {
		_, ws := h.do("POST", "/api/projects/"+p+"/workspaces", map[string]any{"name": "Old"}, nil)
		h.ops(ws["id"].(string), noteOp("n_closed0001", "closed"))
		if st, _ := h.do("DELETE", "/api/workspaces/"+ws["id"].(string), nil, nil); st != 200 {
			t.Fatal(st)
		}
	}
	if n := len(h.srv.boards.ListArchive()); n != 2 {
		t.Fatalf("archive before %d", n)
	}

	if st, b := h.do("POST", "/api/projects/"+pid+"/restore", nil, nil); st != 409 || b["code"] != "not_archived" {
		t.Fatalf("restore active %d %v", st, b)
	}
	// an old client's delete archives
	st, ap := h.do("DELETE", "/api/projects/"+pid, nil, nil)
	if st != 200 || ap["archivedAt"] == nil || ap["id"] != pid {
		t.Fatalf("delete without permanent %d %v", st, ap)
	}
	h.waitInfo(agent, "asleep", asleep)
	st, rp := h.do("POST", "/api/projects/"+pid+"/restore", nil, nil)
	if st != 200 || rp["archivedAt"] != nil {
		t.Fatalf("restore %d %v", st, rp)
	}
	if !has(listIDs(t, h, "/api/projects"), pid) {
		t.Fatal("not back in the list")
	}
	if !h.srv.uptime.Push(mon["pushToken"].(string), false, "", -1) {
		t.Fatal("push refused after restore")
	}
	// it wakes as asleep agents do
	if st, _ := h.do("POST", "/api/sessions/"+agent+"/wake", nil, nil); st != 200 {
		t.Fatalf("wake after restore %d", st)
	}
	h.waitInfo(agent, "awake", state("ready"))

	scroll := h.srv.st.Path("scrollback", agent+".log")
	h.srv.sessions.Info(agent)
	ev, _ := h.events()
	if st, b := h.do("DELETE", "/api/projects/"+pid+"?permanent=1", nil, nil); st != 200 || b["ok"] != true {
		t.Fatalf("permanent delete %d %v", st, b)
	}
	waitEvent(t, ev, func(e map[string]any) bool { return e["type"] == "projectRemoved" && e["id"] == pid })
	if st, _ := h.do("DELETE", "/api/projects/"+pid+"?permanent=1", nil, nil); st != 404 {
		t.Fatalf("delete again %d", st)
	}
	if _, ok := h.srv.sessions.Info(agent); ok {
		t.Fatal("console kept")
	}
	waitTrue(t, "agent process gone", 5*time.Second, func() bool { return !fakeRunning(dir) })
	gone := map[string]string{
		"board":       h.srv.st.Path("boards", wid+".json"),
		"snapshots":   h.srv.st.Path("snapshots", wid),
		"scrollback":  scroll,
		"memory":      h.srv.st.Path("memory", pid),
		"subs":        h.srv.st.Path("memory", "subs", pid),
		"thread":      h.srv.st.Path("threads", pid+".jsonl"),
		"card thread": h.srv.st.Path("threads", "n_orchcard01.jsonl"),
		"review":      reviewDir,
		"attachments": h.srv.st.Path("attachments", "project-"+pid),
		"monitor":     h.srv.st.Path("uptime", mon["id"].(string)),
	}
	for what, p := range gone {
		if exists(p) {
			t.Errorf("%s kept: %s", what, p)
		}
	}
	if a := h.srv.boards.ListArchive(); len(a) != 1 || a[0].ProjectName != "other" {
		t.Errorf("archived boards: %+v", a)
	}
	if len(h.srv.orch.Loops.List(pid)) != 0 || len(h.srv.orch.Loops.List(oid)) != 1 {
		t.Error("loops")
	}
	if h.srv.uptime.Settings(pid).Public.Enabled || len(h.srv.uptime.List(pid)) != 0 {
		t.Error("public page or monitors kept")
	}
	if !exists(h.srv.st.Path("memory", oid)) || !exists(h.srv.st.Path("memory", "subs", oid)) {
		t.Error("another project's memory went")
	}
	if !exists(dir) || !exists(filepath.Join(dir, "fakeorch")) {
		t.Fatal("the project folder was touched")
	}
	if ids := listIDs(t, h, "/api/projects?archived=1"); len(ids) != 0 {
		t.Fatalf("archived %v", ids)
	}
}

func TestProjectsBulk(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	var ids []string
	for _, n := range []string{"alpha", "beta", "gamma"} {
		st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": n}, nil)
		if st != 201 {
			t.Fatal(st)
		}
		ids = append(ids, p["id"].(string))
	}
	for _, body := range []map[string]any{{"ids": ids, "action": "explode"}, {"ids": []string{}, "action": "archive"}, {"ids": make([]string, 201), "action": "archive"}} {
		if b, ok := body["ids"].([]string); ok && len(b) == 201 {
			for i := range b {
				b[i] = fmt.Sprintf("p_x%07d", i)
			}
		}
		if st, _ := h.do("POST", "/api/projects/bulk", body, nil); st != 400 {
			t.Fatalf("bad bulk %v: %d", body["action"], st)
		}
	}
	ev, _ := h.events()
	st, b := h.do("POST", "/api/projects/bulk", map[string]any{"ids": []string{ids[0], ids[1], "p_missing000", ids[0]}, "action": "archive"}, nil)
	res, _ := b["results"].([]any)
	if st != 200 || len(res) != 3 {
		t.Fatalf("bulk archive %d %v", st, b)
	}
	ok := func(i int) bool { return res[i].(map[string]any)["ok"] == true }
	if !ok(0) || !ok(1) || ok(2) || res[2].(map[string]any)["error"] == "" || res[2].(map[string]any)["code"] != "project_not_found" {
		t.Fatalf("results %v", res)
	}
	waitEvent(t, ev, func(e map[string]any) bool { return e["type"] == "projects" })
	if got := listIDs(t, h, "/api/projects?archived=1"); len(got) != 2 {
		t.Fatalf("archived %v", got)
	}
	_, b = h.do("POST", "/api/projects/bulk", map[string]any{"ids": []string{ids[0], ids[2]}, "action": "restore"}, nil)
	res = b["results"].([]any)
	if !ok(0) || ok(1) || res[1].(map[string]any)["code"] != "not_archived" {
		t.Fatalf("restore %v", res)
	}
	_, b = h.do("POST", "/api/projects/bulk", map[string]any{"ids": []string{ids[1], ids[2]}, "action": "delete"}, nil)
	res = b["results"].([]any)
	if !ok(0) || !ok(1) {
		t.Fatalf("delete %v", res)
	}
	if got := listIDs(t, h, "/api/projects"); !has(got, ids[0]) || has(got, ids[1]) || has(got, ids[2]) {
		t.Fatalf("left %v", got)
	}
	if got := listIDs(t, h, "/api/projects?archived=1"); len(got) != 0 {
		t.Fatalf("archived left %v", got)
	}
}

func TestProjectCoverIcon(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "shop"}, nil)
	if st != 201 {
		t.Fatal(st)
	}
	pid := p["id"].(string)
	st, raw, _ := h.doRaw("POST", "/api/assets", "image/png", pngBytes(t, 64, 40))
	var asset map[string]any
	_ = json.Unmarshal(raw, &asset)
	if st != 201 {
		t.Fatalf("upload %d %s", st, raw)
	}
	aid := asset["id"].(string)
	st, got := h.do("PATCH", "/api/projects/"+pid, map[string]any{"cover": map[string]any{"assetId": aid}, "icon": map[string]any{"assetId": aid}}, nil)
	cover, _ := got["cover"].(map[string]any)
	if st != 200 || cover["assetId"] != aid || cover["source"] != "upload" || cover["at"] == nil || got["icon"] == nil {
		t.Fatalf("set %d %v", st, got)
	}
	for _, bad := range []any{map[string]any{"assetId": "a_missing0000"}, map[string]any{"assetId": "../secret.key"}, map[string]any{}, "a_text", 12} {
		if st, b := h.do("PATCH", "/api/projects/"+pid, map[string]any{"icon": bad, "name": "renamed"}, nil); st != 400 || b["code"] != "bad_asset" {
			t.Fatalf("bad asset %v: %d %v", bad, st, b)
		}
	}
	// a file in the asset folder that is no image
	os.WriteFile(h.srv.assetPath("a_notimage01"), []byte("plain text"), 0o600)
	if st, _ := h.do("PATCH", "/api/projects/"+pid, map[string]any{"cover": map[string]any{"assetId": "a_notimage01"}}, nil); st != 400 {
		t.Fatalf("text asset %d", st)
	}
	st, got = h.do("PATCH", "/api/projects/"+pid, map[string]any{"cover": nil}, nil)
	if st != 200 || got["cover"] != nil || got["icon"] == nil || got["name"] != "shop" {
		t.Fatalf("remove %d %v", st, got)
	}
}

func TestGroupsBulkDelete(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	_, top := h.do("POST", "/api/project-groups", map[string]any{"name": "Clients"}, nil)
	_, sub := h.do("POST", "/api/project-groups", map[string]any{"name": "Globex", "parentId": top["id"]}, nil)
	_, other := h.do("POST", "/api/project-groups", map[string]any{"name": "Initech"}, nil)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "portal"}, nil)
	if st != 201 {
		t.Fatal(st)
	}
	h.do("PATCH", "/api/projects/"+p["id"].(string), map[string]any{"groupId": sub["id"]}, nil)
	if st, _ := h.do("POST", "/api/project-groups/bulk", map[string]any{"ids": []string{sub["id"].(string)}, "action": "archive"}, nil); st != 400 {
		t.Fatalf("groups have no archive: %d", st)
	}
	st, b := h.do("POST", "/api/project-groups/bulk", map[string]any{"ids": []string{sub["id"].(string), "g_missing000", other["id"].(string)}, "action": "delete"}, nil)
	res, _ := b["results"].([]any)
	if st != 200 || len(res) != 3 || res[0].(map[string]any)["ok"] != true || res[1].(map[string]any)["code"] != "group_not_found" || res[2].(map[string]any)["ok"] != true {
		t.Fatalf("bulk %d %v", st, b)
	}
	if pr, _ := h.srv.projects.Get(p["id"].(string)); pr.GroupID != top["id"] {
		t.Fatalf("project not moved up: %q", pr.GroupID)
	}
	if gs := h.srv.projects.Groups(); len(gs) != 1 || gs[0].ID != top["id"] {
		t.Fatalf("groups %+v", gs)
	}
}

func TestMachinesArchive(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.ssh/config of the machine running the test
	h := newTestHub(t)
	h.setup()
	add := func(name string, port int) string {
		t.Helper()
		st, m := h.do("POST", "/api/machines", map[string]any{"name": name, "host": "127.0.0.1", "port": port, "user": "alice", "authMethod": "agent"}, nil)
		if st != 201 {
			t.Fatalf("add %d %v", st, m)
		}
		return m["id"].(string)
	}
	used, free := add("staging", 1), add("old-box", 3)
	pr, _, err := h.srv.projects.Create(used, "/srv/shop", "shop-api")
	if err != nil {
		t.Fatal(err)
	}
	h.srv.projects.Archive(pr.ID) // an archived project still holds its machine

	for _, path := range []string{"/api/machines/local/archive"} {
		if st, _ := h.do("POST", path, nil, nil); st != 400 {
			t.Fatalf("%s %d", path, st)
		}
	}
	if st, _ := h.do("DELETE", "/api/machines/local?permanent=1", nil, nil); st != 400 {
		t.Fatalf("delete local %d", st)
	}
	for _, try := range []func() (int, map[string]any){
		func() (int, map[string]any) { return h.do("POST", "/api/machines/"+used+"/archive", nil, nil) },
		func() (int, map[string]any) { return h.do("DELETE", "/api/machines/"+used, nil, nil) },
		func() (int, map[string]any) { return h.do("DELETE", "/api/machines/"+used+"?permanent=1", nil, nil) },
	} {
		st, b := try()
		ps, _ := b["projects"].([]any)
		if st != 409 || b["error"] != "in_use" || len(ps) != 1 || ps[0].(map[string]any)["id"] != pr.ID || ps[0].(map[string]any)["name"] != "shop-api" {
			t.Fatalf("in use %d %v", st, b)
		}
	}
	st, m := h.do("POST", "/api/machines/"+free+"/archive", nil, nil)
	if st != 200 || m["archivedAt"] == nil {
		t.Fatalf("archive %d %v", st, m)
	}
	if ids := listIDs(t, h, "/api/machines"); has(ids, free) || !has(ids, "local") || !has(ids, used) {
		t.Fatalf("active %v", ids)
	}
	if ids := listIDs(t, h, "/api/machines?archived=1"); len(ids) != 1 || ids[0] != free {
		t.Fatalf("archived %v", ids)
	}
	if st, b := h.do("POST", "/api/machines/"+free+"/connect", nil, nil); st != 409 || b["code"] != "archived" {
		t.Fatalf("connect archived %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/machines/"+used+"/restore", nil, nil); st != 409 || b["code"] != "not_archived" {
		t.Fatalf("restore active %d %v", st, b)
	}
	st, m = h.do("POST", "/api/machines/"+free+"/restore", nil, nil)
	if st != 200 || m["archivedAt"] != nil {
		t.Fatalf("restore %d %v", st, m)
	}

	ev, _ := h.events()
	st, b := h.do("POST", "/api/machines/bulk", map[string]any{"ids": []string{used, free, "local"}, "action": "archive"}, nil)
	res, _ := b["results"].([]any)
	if st != 200 || len(res) != 3 || res[0].(map[string]any)["code"] != "in_use" || res[1].(map[string]any)["ok"] != true || res[2].(map[string]any)["ok"] == true {
		t.Fatalf("bulk %d %v", st, b)
	}
	waitEvent(t, ev, func(e map[string]any) bool { return e["type"] == "machines" })
	if st, b := h.do("DELETE", "/api/machines/"+free+"?permanent=1", nil, nil); st != 200 || b["ok"] != true {
		t.Fatalf("delete %d %v", st, b)
	}
	waitEvent(t, ev, func(e map[string]any) bool { return e["type"] == "machineRemoved" && e["id"] == free })
	if _, ok := h.srv.machines.SSH(free); ok {
		t.Fatal("machine kept")
	}
	// once nothing uses it, it can go
	if err := h.srv.deleteProjectForever(pr.ID); err != nil {
		t.Fatal(err)
	}
	_, b = h.do("POST", "/api/machines/bulk", map[string]any{"ids": []string{used}, "action": "delete"}, nil)
	if res := b["results"].([]any); res[0].(map[string]any)["ok"] != true {
		t.Fatalf("delete freed %v", b)
	}
	if _, ok := h.srv.projects.Get(pr.ID); ok {
		t.Fatal(project.ErrNotFound)
	}
}

// TestProjectArt: Create with AI through a fake codex (the settings path), never the real one.
func TestProjectArt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	h := newTestHub(t)
	h.setup()
	logDir, bin := t.TempDir(), filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(bin, []byte(fakeArtCodex), 0o755); err != nil {
		t.Fatal(err)
	}
	pngPath := filepath.Join(t.TempDir(), "made.png")
	os.WriteFile(pngPath, pngBytes(t, 300, 200), 0o644)
	t.Setenv("FAKE_LOG", logDir)
	t.Setenv("FAKE_PNG", pngPath)
	t.Setenv("FAKE_MODE", "")
	if _, err := h.srv.settings.Patch([]byte(`{"agents":{"paths":{"local":{"codex":"` + bin + `"}}}}`)); err != nil {
		t.Fatal(err)
	}
	h.srv.art.timeout = 3 * time.Second
	folder := t.TempDir()
	os.WriteFile(filepath.Join(folder, "README.md"), []byte("# Lumen Garden\n\nWaters balcony plants.\n"), 0o644)
	os.WriteFile(filepath.Join(folder, ".env"), []byte("TOKEN=hidden-value\n"), 0o600)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": folder, "name": "lumen"}, nil)
	if st != 201 {
		t.Fatal(st)
	}
	pid := p["id"].(string)

	if st, c := h.do("GET", "/api/art/capability", nil, nil); st != 200 || c["available"] != true || c["tool"] != "codex" {
		t.Fatalf("capability %d %v", st, c)
	}
	if st, b := h.do("POST", "/api/projects/"+pid+"/art", map[string]any{"kind": "poster"}, nil); st != 400 || b["code"] != "bad_kind" {
		t.Fatalf("bad kind %d %v", st, b)
	}
	if st, _ := h.do("POST", "/api/projects/p_missing000/art", map[string]any{"kind": "cover"}, nil); st != 404 {
		t.Fatalf("missing project %d", st)
	}
	st, j := h.do("POST", "/api/projects/"+pid+"/art", map[string]any{"kind": "cover"}, nil)
	if st != 202 || j["purpose"] != "projectArt" || j["projectId"] != pid {
		t.Fatalf("art %d %v", st, j)
	}
	done := h.waitJob(j["id"].(string))
	res, _ := done["result"].(map[string]any)
	if done["state"] != "done" || res["kind"] != "cover" || res["assetId"] == nil {
		t.Fatalf("job %v", done)
	}
	got, _ := h.srv.projects.Get(pid)
	if got.Cover == nil || got.Cover.Source != "ai" || got.Cover.AssetID != res["assetId"] || !h.srv.isImageAsset(got.Cover.AssetID) {
		t.Fatalf("cover %+v", got.Cover)
	}
	// a background: the clients set the name over it, with the tool's tagline
	if !got.Cover.Title || got.Cover.Tagline != "Plants watered on time" || res["title"] != true || res["tagline"] != "Plants watered on time" {
		t.Fatalf("cover title %+v, result %v", got.Cover, res)
	}
	prompt, _ := os.ReadFile(filepath.Join(logDir, "prompt"))
	if !strings.Contains(string(prompt), "Waters balcony plants") || strings.Contains(string(prompt), "hidden-value") {
		t.Fatalf("prompt %s", prompt)
	}
	argv, _ := os.ReadFile(filepath.Join(logDir, "argv"))
	if !strings.Contains(string(argv), "--sandbox\nread-only\n") {
		t.Fatalf("argv %s", argv)
	}

	// one at a time per project and kind; cancel ends it
	t.Setenv("FAKE_MODE", "hang")
	h.srv.art.timeout = 20 * time.Second
	_, j1 := h.do("POST", "/api/projects/"+pid+"/art", map[string]any{"kind": "icon"}, nil)
	if st, b := h.do("POST", "/api/projects/"+pid+"/art", map[string]any{"kind": "icon"}, nil); st != 409 || b["code"] != "art_running" {
		t.Fatalf("twice %d %v", st, b)
	}
	if st, _ := h.do("POST", "/api/jobs/"+j1["id"].(string)+"/cancel", nil, nil); st != 200 {
		t.Fatalf("cancel %d", st)
	}
	if done := h.waitJob(j1["id"].(string)); done["state"] != "cancelled" {
		t.Fatalf("cancelled %v", done)
	}
	t.Setenv("FAKE_MODE", "nothing")
	_, j2 := h.do("POST", "/api/projects/"+pid+"/art", map[string]any{"kind": "icon"}, nil)
	if done := h.waitJob(j2["id"].(string)); done["state"] != "failed" || done["code"] != "no_image" {
		t.Fatalf("nothing %v", done)
	}
	t.Setenv("FAKE_MODE", "hang")
	h.srv.art.timeout = 1500 * time.Millisecond
	_, j3 := h.do("POST", "/api/projects/"+pid+"/art", map[string]any{"kind": "icon"}, nil)
	if done := h.waitJob(j3["id"].(string)); done["state"] != "failed" || done["code"] != "timeout" {
		t.Fatalf("hang %v", done)
	}
	if got, _ := h.srv.projects.Get(pid); got.Icon != nil {
		t.Fatal("an icon from a failed job")
	}

	// no image tool: the option is not offered, and the job is refused
	t.Setenv("FAKE_IMAGEGEN", "false")
	h.srv.art.reset()
	if _, c := h.do("GET", "/api/art/capability", nil, nil); c["available"] != false || !strings.Contains(c["reason"].(string), "image_generation") {
		t.Fatalf("feature off %v", c)
	}
	if st, b := h.do("POST", "/api/projects/"+pid+"/art", map[string]any{"kind": "cover"}, nil); st != http.StatusPreconditionFailed || b["code"] != "no_image_tool" {
		t.Fatalf("412 %d %v", st, b)
	}
	if _, err := h.srv.settings.Patch([]byte(`{"agents":{"disabled":{"codex":true}}}`)); err != nil {
		t.Fatal(err)
	}
	h.srv.art.reset()
	if _, c := h.do("GET", "/api/art/capability", nil, nil); c["available"] != false || !strings.Contains(c["reason"].(string), "turned off") {
		t.Fatalf("turned off %v", c)
	}
	if _, err := h.srv.settings.Patch([]byte(`{"agents":{"disabled":{"codex":false},"paths":{"local":{"codex":"` + filepath.Join(home, "no-codex") + `"}}}}`)); err != nil {
		t.Fatal(err)
	}
	h.srv.art.reset()
	if _, c := h.do("GET", "/api/art/capability", nil, nil); c["available"] != false || !strings.Contains(c["reason"].(string), "not installed") {
		t.Fatalf("missing %v", c)
	}
}

// TestProjectArtFindsCodexOnPath: the hub's agent catalog finds codex on the login PATH.
func TestProjectArtFindsCodexOnPath(t *testing.T) {
	home, binDir, logDir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(fakeArtCodex), 0o755); err != nil {
		t.Fatal(err)
	}
	pngPath := filepath.Join(t.TempDir(), "made.png")
	os.WriteFile(pngPath, pngBytes(t, 200, 200), 0o644)
	t.Setenv("FAKE_LOG", logDir)
	t.Setenv("FAKE_PNG", pngPath)
	t.Setenv("FAKE_MODE", "")
	h := newTestHub(t)
	// the fake must be the codex the hub finds, or the real one would run
	if first := strings.SplitN(h.srv.machines.Local().LoginPath(), string(os.PathListSeparator), 2)[0]; first != binDir {
		t.Fatalf("the login PATH does not start with the fake's folder: %q", first)
	}
	h.setup()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "lumen"}, nil)
	if st != 201 {
		t.Fatal(st)
	}
	if _, c := h.do("GET", "/api/art/capability", nil, nil); c["available"] != true {
		t.Fatalf("capability %v", c)
	}
	_, j := h.do("POST", "/api/projects/"+p["id"].(string)+"/art", map[string]any{"kind": "icon"}, nil)
	if done := h.waitJob(j["id"].(string)); done["state"] != "done" {
		t.Fatalf("job %v", done)
	}
	if got, _ := h.srv.projects.Get(p["id"].(string)); got.Icon == nil || got.Icon.Source != "ai" {
		t.Fatalf("icon %+v", got.Icon)
	}
}

// fakeArtCodex: see internal/art's fake; FAKE_MODE "" makes an image and names it, "nothing"
// makes none, "hang" never ends.
const fakeArtCodex = `#!/bin/sh
case "$1" in
features) printf 'image_generation  stable  %s\n' "${FAKE_IMAGEGEN:-true}"; exit 0;;
exec) ;;
*) exit 2;;
esac
if [ "$2" = "--help" ]; then echo "--ignore-user-config --color"; exit 0; fi
printf '%s\n' "$@" > "$FAKE_LOG/argv"
cat > "$FAKE_LOG/prompt"
case "$FAKE_MODE" in
hang) sleep 60; exit 0;;
nothing) echo "no image"; exit 0;;
esac
home="${CODEX_HOME:-$HOME/.codex}"
mkdir -p "$home/generated_images/01a2b3c4-1111-7222-8333-444455556666"
f="$home/generated_images/01a2b3c4-1111-7222-8333-444455556666/exec-$$.png"
cp "$FAKE_PNG" "$f"
out=""
prev=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
[ -n "$out" ] && printf '%s\nPlants watered on time\n' "$f" > "$out"
echo "$f"
`
