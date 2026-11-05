package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/store"
)

// doRaw sends a raw body and returns status, body and headers.
func (h *testHub) doRaw(method, path, contentType string, body []byte) (int, []byte, http.Header) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.ts.URL+path, bytes.NewReader(body))
	if method != http.MethodGet {
		req.Header.Set("X-NextBoard", "1")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := h.c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header
}

// getJSON decodes any JSON response (arrays included) into out.
func (h *testHub) getJSON(path string, out any) int {
	h.t.Helper()
	st, raw, _ := h.doRaw("GET", path, "", nil)
	if err := json.Unmarshal(raw, out); err != nil && st == 200 {
		h.t.Fatalf("GET %s: %v %s", path, err, raw)
	}
	return st
}

// events opens /api/events with the admin cookie and returns the
// connection plus the snapshot frame.
func (h *testHub) events() (*websocket.Conn, map[string]any) {
	h.t.Helper()
	hdr := http.Header{"Origin": {h.ts.URL}}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	ws, _, err := h.dialWS("/api/events", hdr)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { ws.Close() })
	var snap map[string]any
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := ws.ReadJSON(&snap); err != nil || snap["type"] != "snapshot" {
		h.t.Fatalf("snapshot %v %v", snap, err)
	}
	return ws, snap
}

// waitEvent reads events until match returns true.
func waitEvent(t *testing.T, ws *websocket.Conn, match func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_ = ws.SetReadDeadline(deadline)
		var ev map[string]any
		if err := ws.ReadJSON(&ev); err != nil {
			t.Fatalf("waiting for event: %v", err)
		}
		if match(ev) {
			return ev
		}
	}
}

func isBoardOps(wid, clientID string) func(map[string]any) bool {
	return func(ev map[string]any) bool {
		return ev["type"] == "boardOps" && ev["workspaceId"] == wid && ev["clientId"] == clientID
	}
}

// homeWorkspace returns the Home project and its Main workspace id.
func (h *testHub) homeWorkspace() (string, string) {
	h.t.Helper()
	var list []map[string]any
	h.getJSON("/api/projects", &list)
	for _, p := range list {
		if p["name"] == "Home" {
			return p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
		}
	}
	h.t.Fatalf("no Home project: %v", list)
	return "", ""
}

func noteOp(id, text string) map[string]any {
	return map[string]any{"op": "put", "node": map[string]any{"id": id, "kind": "note", "x": 0, "y": 0, "w": 200, "h": 120, "data": map[string]any{"text": text, "color": "yellow"}}}
}

func (h *testHub) ops(wid string, ops ...map[string]any) (int, map[string]any) {
	return h.do("POST", "/api/workspaces/"+wid+"/board/ops", map[string]any{"clientId": "c_tab1", "baseRev": 0, "ops": ops}, nil)
}

func TestProjectCloseAll(t *testing.T) {
	guiApp(t, "testapp")
	second := "[Desktop Entry]\nType=Application\nName=Shared App\nExec=/bin/sleep 60\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_DATA_HOME"), "applications", "sharedapp.desktop"), []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newTestHub(t)
	h.setup()
	withApps := h.srv.gui.Status().Available
	if !withApps {
		t.Log("no display packages: the desktop app part is skipped")
	}
	running := func() map[string]bool {
		out := map[string]bool{}
		for _, g := range h.srv.gui.List() {
			out[g.ID] = true
		}
		return out
	}
	defer func() {
		for id := range running() {
			_ = h.srv.gui.Stop(id)
		}
		deadline := time.Now().Add(15 * time.Second)
		for len(h.srv.gui.List()) > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}()

	pid, main := h.homeWorkspace()
	_, w2 := h.do("POST", "/api/projects/"+pid+"/workspaces", map[string]any{"name": "Second"}, nil)
	wid2 := w2["id"].(string)
	var sids []string
	for _, wid := range []string{main, wid2} {
		_, sess := h.do("POST", "/api/sessions", map[string]any{"workspaceId": wid, "preset": "shell"}, nil)
		sids = append(sids, sess["id"].(string))
	}
	h.ops(main, noteOp("n_closenote1", "keep"))

	// Another project: its loop and its board must survive.
	_, other := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "Other"}, nil)
	otherID := other["id"].(string)
	otherWS := other["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	h.ops(otherWS, noteOp("n_othernote1", "stays"))
	_, otherSess := h.do("POST", "/api/sessions", map[string]any{"workspaceId": otherWS, "preset": "shell"}, nil)
	loop := func(project string, target any) string {
		st, l := h.do("POST", "/api/projects/"+project+"/loops", map[string]any{"name": "Ping", "enabled": true, "text": "ping",
			"trigger": map[string]any{"kind": "interval", "everySec": 600}, "targets": map[string]any{"kind": "handles", "ids": []any{target}}}, nil)
		if st != 201 {
			t.Fatalf("loop %d %v", st, l)
		}
		return l["id"].(string)
	}
	ownLoop, otherLoop := loop(pid, sids[0]), loop(otherID, otherSess["id"])
	loopStatus := func(project, id string) string {
		for _, l := range h.list("/api/projects/" + project + "/loops") {
			if m := l.(map[string]any); m["id"] == id {
				return m["state"].(map[string]any)["status"].(string)
			}
		}
		return ""
	}

	// One app shown only here, one also on the other project's board.
	only, shared := "", ""
	tiles, wantApps := 1.0, 0.0
	if withApps {
		launch := func(app string) string {
			st, g := h.do("POST", "/api/gui/sessions", map[string]any{"appId": app, "width": 640, "height": 480}, nil)
			if st != 201 {
				t.Fatalf("launch %s: %d %v", app, st, g)
			}
			return g["id"].(string)
		}
		tile := func(wid, id, app, sid string) {
			h.ops(wid, map[string]any{"op": "put", "node": map[string]any{"id": id, "kind": "app", "x": 0, "y": 0, "w": 400, "h": 300,
				"data": map[string]any{"appId": app, "name": app, "sessionId": sid}}})
		}
		only, shared = launch("testapp"), launch("sharedapp")
		tile(main, "n_closeapp01", "testapp", only)
		tile(main, "n_closeapp02", "sharedapp", shared)
		tile(otherWS, "n_closeapp03", "sharedapp", shared)
		tiles, wantApps = 3, 1
	}

	st, pre := h.do("GET", "/api/projects/"+pid+"/close-all", nil, nil)
	if st != 200 || pre["boards"] != 2.0 || pre["tiles"] != tiles || pre["sessions"] != 2.0 || pre["apps"] != wantApps || pre["loops"] != 1.0 {
		t.Fatalf("preview %d %v", st, pre)
	}

	st, res := h.do("POST", "/api/projects/"+pid+"/close-all", nil, nil)
	if st != 200 || res["closed"] != 2.0 || res["archived"] != 1.0 || res["sessions"] != 2.0 || res["apps"] != wantApps || res["loops"] != 1.0 {
		t.Fatalf("close all %d %v", st, res)
	}

	// One new empty board remains, the old ones are gone.
	fresh := res["workspace"].(map[string]any)
	newID := fresh["id"].(string)
	if fresh["name"] != "Main" || newID == main || newID == wid2 {
		t.Fatalf("new workspace %v", fresh)
	}
	var list []map[string]any
	h.getJSON("/api/projects", &list)
	for _, p := range list {
		if p["id"] == pid {
			if ws := p["workspaces"].([]any); len(ws) != 1 || ws[0].(map[string]any)["id"] != newID {
				t.Fatalf("workspaces %v", ws)
			}
		}
	}
	for _, wid := range []string{main, wid2} {
		if st, _ := h.do("GET", "/api/workspaces/"+wid+"/board", nil, nil); st != 404 {
			t.Fatalf("board %s still there: %d", wid, st)
		}
	}
	if _, b := h.do("GET", "/api/workspaces/"+newID+"/board", nil, nil); len(b["nodes"].([]any)) != 0 {
		t.Fatalf("new board not empty: %v", b)
	}
	for _, id := range sids {
		if st, _ := h.do("GET", "/api/sessions/"+id, nil, nil); st != 404 {
			t.Fatalf("session %s survived: %d", id, st)
		}
	}
	var arch []map[string]any
	h.getJSON("/api/archive", &arch)
	if len(arch) != 1 || arch[0]["reason"] != "closed" || arch[0]["workspaceName"] != "Main" {
		t.Fatalf("archive %v", arch)
	}
	if got := loopStatus(pid, ownLoop); got != "stopped" {
		t.Fatalf("own loop %q", got)
	}
	if got := loopStatus(otherID, otherLoop); got != "running" {
		t.Fatalf("other project's loop %q", got)
	}
	if _, b := h.do("GET", "/api/workspaces/"+otherWS+"/board", nil, nil); len(b["nodes"].([]any)) == 0 {
		t.Fatalf("other project's board emptied: %v", b)
	}

	if withApps {
		deadline := time.Now().Add(15 * time.Second)
		for running()[only] && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if running()[only] {
			t.Fatal("app shown only on the closed boards still runs")
		}
		if !running()[shared] {
			t.Fatal("app still shown on another project's board was closed")
		}
		// Now only that project shows it: removing the project closes it.
		if st, _ := h.do("DELETE", "/api/projects/"+otherID, nil, nil); st != 200 {
			t.Fatalf("delete project %d", st)
		}
		deadline = time.Now().Add(15 * time.Second)
		for running()[shared] && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if running()[shared] {
			t.Fatal("app of a removed project still runs")
		}
	}

	// Closing again only replaces the empty board; nothing reaches the archive.
	if st, again := h.do("POST", "/api/projects/"+pid+"/close-all", nil, nil); st != 200 || again["closed"] != 1.0 || again["archived"] != 0.0 {
		t.Fatalf("second close %d %v", st, again)
	}
	for _, method := range []string{"GET", "POST"} {
		if st, _ := h.do(method, "/api/projects/p_missing000/close-all", nil, nil); st != 404 {
			t.Fatalf("%s missing project %d", method, st)
		}
	}
}

func TestProjectsAndWorkspaces(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	var list []map[string]any
	h.getJSON("/api/projects", &list)
	if len(list) != 1 || list[0]["name"] != "Home" || list[0]["machineId"] != "local" {
		t.Fatalf("migration should create Home: %v", list)
	}
	repo := filepath.Join(t.TempDir(), "my-repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(repo, "file.txt"), []byte("x"), 0o644)

	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": repo}, nil)
	if st != 201 || p["name"] != "my-repo" || p["path"] != repo || p["sessionCount"].(float64) != 0 {
		t.Fatalf("create %d %v", st, p)
	}
	pid := p["id"].(string)
	wss := p["workspaces"].([]any)
	if len(wss) != 1 || wss[0].(map[string]any)["name"] != "Main" {
		t.Fatalf("workspaces %v", wss)
	}
	main := wss[0].(map[string]any)["id"].(string)
	// Same folder (any spelling) → the existing project.
	if st, again := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": repo + "/./"}, nil); st != 200 || again["id"] != pid {
		t.Fatalf("duplicate %d %v", st, again)
	}
	for _, c := range []struct {
		body map[string]any
		want int
	}{
		{map[string]any{"path": filepath.Join(repo, "file.txt")}, 400},
		{map[string]any{"path": filepath.Join(repo, "missing")}, 400},
		{map[string]any{"path": "relative/dir"}, 400},
		{map[string]any{"path": repo, "name": strings.Repeat("x", 25)}, 400},
		{map[string]any{"machineId": "m_nope", "path": repo}, 404},
	} {
		if st, body := h.do("POST", "/api/projects", c.body, nil); st != c.want {
			t.Fatalf("%v: %d %v", c.body, st, body)
		}
	}

	if st, up := h.do("PATCH", "/api/projects/"+pid, map[string]any{"name": "Renamed", "opened": true}, nil); st != 200 || up["name"] != "Renamed" {
		t.Fatalf("patch %d %v", st, up)
	}
	if st, _ := h.do("PATCH", "/api/projects/"+pid, map[string]any{"name": strings.Repeat("y", 25)}, nil); st != 400 {
		t.Fatalf("long name %d", st)
	}
	h.getJSON("/api/projects", &list)
	if list[0]["id"] != pid {
		t.Fatalf("opened project should sort first: %v", list[0]["name"])
	}

	st, w2 := h.do("POST", "/api/projects/"+pid+"/workspaces", nil, nil)
	if st != 201 || w2["name"] != "Workspace 2" || w2["projectId"] != pid {
		t.Fatalf("workspace %d %v", st, w2)
	}
	_, qa := h.do("POST", "/api/projects/"+pid+"/workspaces", map[string]any{"name": "QA"}, nil)
	qaID := qa["id"].(string)
	if st, _ := h.do("PATCH", "/api/workspaces/"+qaID, map[string]any{"name": "Backend", "index": 0}, nil); st != 200 {
		t.Fatalf("reorder %d", st)
	}
	var pr []map[string]any
	h.getJSON("/api/projects", &pr)
	var names []string
	for _, w := range pr[0]["workspaces"].([]any) {
		names = append(names, w.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "Backend,Main,Workspace 2" {
		t.Fatalf("tab order %v", names)
	}
	if st, _ := h.do("PATCH", "/api/workspaces/w_missing000", map[string]any{"name": "x"}, nil); st != 404 {
		t.Fatalf("missing ws %d", st)
	}
	for _, id := range []string{w2["id"].(string), qaID} {
		if st, _ := h.do("DELETE", "/api/workspaces/"+id, nil, nil); st != 200 {
			t.Fatalf("delete ws %d", st)
		}
	}
	if st, _ := h.do("DELETE", "/api/workspaces/"+main, nil, nil); st != 409 {
		t.Fatalf("last workspace delete should be 409, got %d", st)
	}

	// Git info (read-only).
	if _, g := h.do("GET", "/api/projects/"+pid+"/git", nil, nil); g["isRepo"] != false {
		t.Fatalf("git non-repo %v", g)
	}
	if _, err := exec.LookPath("git"); err == nil {
		if out, err := exec.Command("git", "-C", repo, "init", "-q", "-b", "trunk").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		_, g := h.do("GET", "/api/projects/"+pid+"/git", nil, nil)
		if g["isRepo"] != true || g["branch"] != "trunk" || g["changed"].(float64) != 1 || g["error"] != "" {
			t.Fatalf("git %v", g)
		}
		if _, err := os.Stat(filepath.Join(repo, ".git", "index")); err == nil {
			t.Fatal("git status wrote the index")
		}
	}

	if st, _ := h.do("DELETE", "/api/projects/"+pid, nil, nil); st != 200 {
		t.Fatalf("delete project %d", st)
	}
	if st, _ := h.do("PATCH", "/api/projects/"+pid, map[string]any{"opened": true}, nil); st != 404 {
		t.Fatalf("deleted project %d", st)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Fatal("project delete must not touch files")
	}
}

func TestBoardOpsSnapshotsAndEvents(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	_, wid := h.homeWorkspace()
	ws, snap := h.events()
	if _, ok := snap["projects"].([]any); !ok {
		t.Fatalf("snapshot without projects: %v", snap)
	}
	if st, _ := h.do("GET", "/api/board", nil, nil); st != 404 {
		t.Fatalf("old board endpoint still there: %d", st)
	}

	st, res := h.ops(wid, noteOp("n_note00001", "hello"))
	if st != 200 || res["rev"].(float64) != 1 {
		t.Fatalf("ops %d %v", st, res)
	}
	ev := waitEvent(t, ws, isBoardOps(wid, "c_tab1"))
	ops := ev["ops"].([]any)
	if ev["rev"].(float64) != 1 || len(ops) != 1 || ops[0].(map[string]any)["node"].(map[string]any)["id"] != "n_note00001" {
		t.Fatalf("broadcast %v", ev)
	}
	st, res = h.ops(wid, map[string]any{"op": "patch", "id": "n_note00001", "set": map[string]any{"x": 50, "data": map[string]any{"color": "pink"}}})
	if st != 200 || res["rev"].(float64) != 2 {
		t.Fatalf("patch %d %v", st, res)
	}
	var b struct {
		Rev           int64 `json:"rev"`
		SchemaVersion int   `json:"schemaVersion"`
		Nodes         []struct {
			ID   string         `json:"id"`
			Kind string         `json:"kind"`
			X    float64        `json:"x"`
			Data map[string]any `json:"data"`
		} `json:"nodes"`
	}
	h.getJSON("/api/workspaces/"+wid+"/board", &b)
	if b.Rev != 2 || b.SchemaVersion != 2 || len(b.Nodes) != 1 || b.Nodes[0].X != 50 || b.Nodes[0].Data["color"] != "pink" || b.Nodes[0].Data["text"] != "hello" {
		t.Fatalf("board %+v", b)
	}

	// Validation and limits.
	if st, body := h.ops(wid, noteOp("n_note00002", "ok"), map[string]any{"op": "put", "node": map[string]any{"id": "bad", "kind": "note", "w": 1, "h": 1}}); st != 400 || !strings.Contains(body["error"].(string), "op #1") {
		t.Fatalf("bad op %d %v", st, body)
	}
	many := make([]map[string]any, 501)
	for i := range many {
		many[i] = map[string]any{"op": "del", "id": "n_note00001"}
	}
	if st, _ := h.ops(wid, many...); st != 400 {
		t.Fatalf("501 ops %d", st)
	}
	if st, _ := h.ops(wid, noteOp("n_note00003", strings.Repeat("x", 2<<20))); st != 413 {
		t.Fatalf("oversized ops body %d", st)
	}
	if st, _ := h.do("POST", "/api/workspaces/w_nowhere00/board/ops", map[string]any{"ops": []any{}}, nil); st != 404 {
		t.Fatalf("unknown workspace %d", st)
	}
	if st, _ := h.do("POST", "/api/workspaces/"+wid+"/board/ops", map[string]any{"clientId": "bad id!", "ops": []any{}}, nil); st != 400 {
		t.Fatalf("bad clientId %d", st)
	}

	// Snapshot of the state before the first save of this hour, then restore.
	h.srv.boards.Flush()
	h.ops(wid, noteOp("n_note00004", "later"))
	h.srv.boards.Flush()
	var snaps []map[string]any
	h.getJSON("/api/workspaces/"+wid+"/snapshots", &snaps)
	if len(snaps) != 1 || snaps[0]["nodeCount"].(float64) != 1 {
		t.Fatalf("snapshots %v", snaps)
	}
	st, rb := h.do("POST", "/api/workspaces/"+wid+"/snapshots/"+snaps[0]["id"].(string)+"/restore", nil, nil)
	if st != 200 || len(rb["nodes"].([]any)) != 1 {
		t.Fatalf("restore %d %v", st, rb)
	}
	ev = waitEvent(t, ws, isBoardOps(wid, ""))
	if ev["rev"].(float64) != rb["rev"].(float64) {
		t.Fatalf("restore broadcast %v", ev)
	}
	if st, _ := h.do("POST", "/api/workspaces/"+wid+"/snapshots/2001010100/restore", nil, nil); st != 404 {
		t.Fatalf("missing snapshot %d", st)
	}
}

func TestSessionTerminalNodes(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	pid, mainWS := h.homeWorkspace()
	_, agents := h.do("POST", "/api/projects/"+pid+"/workspaces", map[string]any{"name": "Agents"}, nil)
	wid := agents["id"].(string)
	ws, _ := h.events()

	st, sess := h.do("POST", "/api/sessions", map[string]any{"workspaceId": wid, "preset": "shell", "node": map[string]any{"x": 100, "y": 200, "w": 600, "h": 380}}, nil)
	if st != 201 || sess["workspaceId"] != wid || sess["projectId"] != pid || sess["cwd"] != homeDir() {
		t.Fatalf("create %d %v", st, sess)
	}
	sid := sess["id"].(string)
	ev := waitEvent(t, ws, isBoardOps(wid, ""))
	node := ev["ops"].([]any)[0].(map[string]any)["node"].(map[string]any)
	if node["kind"] != "terminal" || node["data"].(map[string]any)["sessionId"] != sid || node["x"].(float64) != 100 || node["w"].(float64) != 600 {
		t.Fatalf("terminal node %v", node)
	}
	waitEvent(t, ws, func(ev map[string]any) bool {
		p, _ := ev["project"].(map[string]any)
		return ev["type"] == "project" && p["id"] == pid && p["sessionCount"].(float64) == 1
	})
	nodeID := node["id"].(string)

	// Clients cannot fake or delete terminal nodes.
	if st, _ := h.ops(wid, map[string]any{"op": "put", "node": map[string]any{"id": "n_faketerm1", "kind": "terminal", "w": 10, "h": 10, "data": map[string]any{"sessionId": "s_doesnotexist"}}}); st != 400 {
		t.Fatalf("fake terminal %d", st)
	}
	_, before := h.do("GET", "/api/workspaces/"+wid+"/board", nil, nil)
	if _, res := h.ops(wid, map[string]any{"op": "del", "id": nodeID}); res["rev"] != before["rev"] {
		t.Fatalf("terminal del changed rev %v → %v", before["rev"], res["rev"])
	}
	// Bad requests.
	if st, _ := h.do("POST", "/api/sessions", map[string]any{"workspaceId": "w_missing000", "preset": "shell"}, nil); st != 404 {
		t.Fatalf("unknown workspace %d", st)
	}
	if st, _ := h.do("POST", "/api/sessions", map[string]any{"workspaceId": wid, "preset": "shell", "node": map[string]any{"w": 0, "h": 10}}, nil); st != 400 {
		t.Fatalf("bad node %d", st)
	}
	// Old clients without workspaceId land in the default workspace.
	_, legacy := h.do("POST", "/api/sessions", map[string]any{"machineId": "local", "preset": "shell"}, nil)
	if legacy["workspaceId"] != mainWS {
		t.Fatalf("default workspace %v", legacy["workspaceId"])
	}

	// DELETE session removes its node and broadcasts.
	if st, _ := h.do("DELETE", "/api/sessions/"+sid, nil, nil); st != 200 {
		t.Fatalf("delete %d", st)
	}
	ev = waitEvent(t, ws, isBoardOps(wid, ""))
	if op := ev["ops"].([]any)[0].(map[string]any); op["op"] != "del" || op["id"] != nodeID {
		t.Fatalf("del broadcast %v", ev)
	}
	_, b := h.do("GET", "/api/workspaces/"+wid+"/board", nil, nil)
	if len(b["nodes"].([]any)) != 0 {
		t.Fatalf("node left behind %v", b)
	}

	// Deleting a workspace ends its sessions and archives its board.
	_, s2 := h.do("POST", "/api/sessions", map[string]any{"workspaceId": wid, "preset": "shell"}, nil)
	h.ops(wid, noteOp("n_keepnote1", "todo"))
	if st, _ := h.do("DELETE", "/api/workspaces/"+wid, nil, nil); st != 200 {
		t.Fatalf("delete workspace %d", st)
	}
	if st, _ := h.do("GET", "/api/sessions/"+s2["id"].(string), nil, nil); st != 404 {
		t.Fatalf("session survived workspace delete: %d", st)
	}
	if st, _ := h.do("GET", "/api/workspaces/"+wid+"/board", nil, nil); st != 404 {
		t.Fatalf("board of deleted workspace %d", st)
	}
	var arch []map[string]any
	h.getJSON("/api/archive", &arch)
	if len(arch) != 1 || arch[0]["reason"] != "closed" || arch[0]["workspaceName"] != "Agents" || arch[0]["projectName"] != "Home" {
		t.Fatalf("archive %v", arch)
	}
	st, restored := h.do("POST", "/api/archive/"+arch[0]["id"].(string)+"/restore", map[string]any{"projectId": pid}, nil)
	if st != 201 || restored["name"] != "Agents" {
		t.Fatalf("archive restore %d %v", st, restored)
	}
	_, rb := h.do("GET", "/api/workspaces/"+restored["id"].(string)+"/board", nil, nil)
	nodes := rb["nodes"].([]any)
	if len(nodes) != 1 || nodes[0].(map[string]any)["id"] != "n_keepnote1" {
		t.Fatalf("restored board %v", rb)
	}
	if st, _ := h.do("POST", "/api/archive/r_missing000/restore", map[string]any{"projectId": pid}, nil); st != 404 {
		t.Fatalf("missing archive %d", st)
	}
}

func TestMigrationFromLegacyBoard(t *testing.T) {
	dir := hubDataDir(t)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	spec := func(id string, at time.Time) map[string]any {
		return map[string]any{"id": id, "name": "Old " + id, "preset": "shell", "machineId": "local", "command": "sh",
			"args": []string{"-c", "sleep 30"}, "cwd": dir, "cols": 80, "rows": 24, "createdAt": at}
	}
	st.Save("sessions.json", []any{spec("s_legacy0001", now), spec("s_legacy0002", now.Add(time.Second))})
	st.Save("board.json", map[string]any{
		"nodes":    map[string]any{"s_legacy0001": map[string]any{"x": 10, "y": 20, "w": 700, "h": 400}, "s_vanished01": map[string]any{"x": 1, "y": 1, "w": 1, "h": 1}},
		"viewport": map[string]any{"x": 0, "y": 0, "zoom": 1},
	})

	check := func(srv *Server, wantNodes int) (string, string) {
		t.Helper()
		ps := srv.projects.List()
		if len(ps) != 1 || ps[0].Name != "Home" || ps[0].MachineID != "local" || len(ps[0].Workspaces) != 1 || ps[0].Workspaces[0].Name != "Main" {
			t.Fatalf("projects %+v", ps)
		}
		wid := ps[0].Workspaces[0].ID
		for _, in := range srv.sessions.List() {
			if in.WorkspaceID != wid || in.ProjectID != ps[0].ID {
				t.Fatalf("session not moved: %+v", in.Spec)
			}
		}
		b, err := srv.boards.Get(wid)
		if err != nil || len(b.Nodes) != wantNodes {
			t.Fatalf("board %+v %v", b, err)
		}
		n0, n1 := b.Nodes[0], b.Nodes[1]
		if n0.Kind != "terminal" || n0.SessionID() != "s_legacy0001" || n0.X != 10 || n0.Y != 20 || n0.W != 700 || n0.H != 400 {
			t.Fatalf("migrated node %+v", n0)
		}
		if n1.SessionID() != "s_legacy0002" || n1.X != 10+700+40 {
			t.Fatalf("auto-placed node %+v", n1)
		}
		if _, err := os.Stat(filepath.Join(dir, "board.json")); err == nil {
			t.Fatal("board.json not renamed")
		}
		if _, err := os.Stat(filepath.Join(dir, "board.json.migrated")); err != nil {
			t.Fatal("board.json.migrated missing")
		}
		return ps[0].ID, wid
	}

	srv1, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv1.review.Close) // a snapshot still running would write the folder back
	pid1, wid1 := check(srv1, 2)
	note := &board.Node{ID: "n_migrnote1", Kind: "note", Y: 600, W: 100, H: 100, Data: json.RawMessage(`{"text":"x"}`)}
	rev, err := srv1.boards.Apply(wid1, "c_x", []board.Op{{Op: "put", Node: note}})
	if err != nil {
		t.Fatal(err)
	}
	srv1.shutdown()

	// Restart: idempotent, nothing duplicated, board reloaded from disk.
	srv2, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv2.review.Close) // a snapshot still running would write the folder back
	defer srv2.shutdown()
	pid2, wid2 := check(srv2, 3)
	if pid2 != pid1 || wid2 != wid1 {
		t.Fatal("restart created a new Home")
	}
	b2, _ := srv2.boards.Get(wid2)
	if b2.Rev != rev || b2.Nodes[2].ID != "n_migrnote1" {
		t.Fatalf("reloaded board rev=%d nodes=%d", b2.Rev, len(b2.Nodes))
	}
}
