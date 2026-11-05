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

func (h *testHub) boardOps(wid string, ops ...map[string]any) (int, map[string]any) {
	h.t.Helper()
	return h.do("POST", "/api/workspaces/"+wid+"/board/ops", map[string]any{"clientId": "c_tab1", "baseRev": 0, "ops": ops}, nil)
}

func (h *testHub) terminalNode(wid, sid string) string {
	h.t.Helper()
	var b struct {
		Nodes []struct {
			ID   string         `json:"id"`
			Kind string         `json:"kind"`
			Data map[string]any `json:"data"`
		} `json:"nodes"`
	}
	h.getJSON("/api/workspaces/"+wid+"/board", &b)
	for _, n := range b.Nodes {
		if n.Kind == "terminal" && n.Data["sessionId"] == sid {
			return n.ID
		}
	}
	h.t.Fatalf("no terminal node for %s in %+v", sid, b.Nodes)
	return ""
}

func (h *testHub) otherProject(name string) string {
	h.t.Helper()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "name": name, "path": h.t.TempDir()}, nil)
	if st != 201 {
		h.t.Fatalf("project %d %v", st, p)
	}
	return p["id"].(string)
}

const testMemTile = "n_mem0000001"

func memTileOp(data map[string]any) map[string]any {
	return map[string]any{"op": "put", "node": map[string]any{"id": testMemTile, "kind": "memory", "x": 0, "y": 0, "w": 320, "h": 300, "data": data}}
}

func wireEdge(id, from, to string) map[string]any {
	return map[string]any{"op": "putEdge", "edge": map[string]any{"id": id, "kind": "wire", "source": from, "target": to, "data": map[string]any{}}}
}

// Over HTTP, into a real console: a tile's stack is typed once, then only what is new.
func TestMemoryTileIntoAConsole(t *testing.T) {
	h := newTestHub(t)
	pid, wid, _ := orchSetup(t, h)
	dir := os.Getenv("FAKE_DIR")
	id := h.spawnAgent(wid)["id"].(string)
	h.waitInfo(id, "ready", state("ready"))
	other := h.otherProject("Acme")
	if st, b := h.do("PUT", "/api/projects/"+pid+"/memory", map[string]any{"scope": "project", "content": "- the project fact\n"}, nil); st != 200 {
		t.Fatalf("memory %d %v", st, b)
	}
	if st, b := h.do("PUT", "/api/memory/docs/design?projectId="+other, map[string]any{"title": "Design system", "content": "- buttons are square\n"}, nil); st != 200 {
		t.Fatalf("doc %d %v", st, b)
	}
	node := h.terminalNode(wid, id)
	if st, b := h.boardOps(wid, memTileOp(map[string]any{"blocks": []any{map[string]any{"projectId": other, "name": "design"}}}), wireEdge("e_wire000001", testMemTile, node)); st != 200 {
		t.Fatalf("tile %d %v", st, b)
	}

	plan := func() map[string]any {
		t.Helper()
		st, p := h.do("POST", "/api/projects/"+pid+"/memory/plan", map[string]any{"memoryNodeId": testMemTile}, nil)
		if st != 200 {
			t.Fatalf("plan %d %v", st, p)
		}
		return p
	}
	targetChars := func(p map[string]any) float64 {
		t.Helper()
		list := p["targets"].([]any)
		if len(list) != 1 {
			t.Fatalf("targets %v", list)
		}
		return list[0].(map[string]any)["chars"].(float64)
	}
	p := plan()
	docs := p["docs"].([]any)
	if len(docs) != 2 || docs[0].(map[string]any)["locked"] != true || docs[1].(map[string]any)["owner"] != "Acme" {
		t.Fatalf("docs %v", docs)
	}
	if targetChars(p) == 0 || strings.Contains(received(dir), "buttons are square") {
		t.Fatalf("plan %v", p)
	}

	// The wire was just made: the board sends through the tile push.
	st, r := h.do("POST", "/api/workspaces/"+wid+"/tiles/"+testMemTile+"/push", map[string]any{"targets": []any{node}}, nil)
	if st != 200 || r["delivered"].(float64) != 1 {
		t.Fatalf("tile push %d %v", st, r)
	}
	waitTrue(t, "the stack arrives", 10*time.Second, func() bool {
		got := received(dir)
		return strings.Contains(got, "the project fact") && strings.Contains(got, "buttons are square") && strings.Contains(got, "another project, Acme")
	})
	h.waitInfo(id, "ready again", state("ready"))

	// Nothing new: no turn.
	waitTrue(t, "the plan knows", 10*time.Second, func() bool { return targetChars(plan()) == 0 })
	before := received(dir)
	if st, r := h.do("POST", "/api/projects/"+pid+"/memory/push", map[string]any{"memoryNodeId": testMemTile}, nil); st != 200 || r["upToDate"].(float64) != 1 || r["delivered"].(float64) != 0 {
		t.Fatalf("nothing new %d %v", st, r)
	}
	time.Sleep(300 * time.Millisecond)
	if received(dir) != before {
		t.Fatalf("typed with nothing new:\n%s", received(dir))
	}

	// One new fact: only that.
	if st, b := h.do("POST", "/api/projects/"+pid+"/memory/accept", map[string]any{"scope": "project", "facts": []any{map[string]any{"kind": "gotchas", "text": "tests need a scratch HOME"}}}, nil); st != 200 {
		t.Fatalf("accept %d %v", st, b)
	}
	if st, r := h.do("POST", "/api/projects/"+pid+"/memory/push", map[string]any{"memoryNodeId": testMemTile}, nil); st != 200 || r["delivered"].(float64) != 1 {
		t.Fatalf("new fact %d %v", st, r)
	}
	waitTrue(t, "the new fact arrives", 10*time.Second, func() bool { return strings.Contains(received(dir), "scratch HOME") })
	if got := received(dir); strings.Count(got, "the project fact") != 1 || strings.Count(got, "buttons are square") != 1 {
		t.Fatalf("repeated what it had:\n%s", got)
	}
	h.waitInfo(id, "ready again", state("ready"))

	// Send-to-agent with a chosen memory and a chosen agent, insisting.
	if st, r := h.do("POST", "/api/projects/"+pid+"/memory/push", map[string]any{"targets": []any{id}, "blocks": []any{map[string]any{"projectId": other, "name": "design"}}, "force": true}, nil); st != 200 || r["delivered"].(float64) != 1 {
		t.Fatalf("force %d %v", st, r)
	}
	waitTrue(t, "sent again", 10*time.Second, func() bool { return strings.Count(received(dir), "buttons are square") == 2 })
	if got := received(dir); strings.Count(got, "the project fact") != 1 {
		t.Fatalf("sent a memory that was not picked:\n%s", got)
	}

	// Wires: the tile also goes to an orchestrator card and the editor. A note does not.
	if st, b := h.boardOps(wid,
		map[string]any{"op": "put", "node": map[string]any{"id": "n_orc0000001", "kind": "orchestrator", "x": 0, "y": 0, "w": 300, "h": 200, "data": map[string]any{"title": "Lead"}}},
		map[string]any{"op": "put", "node": map[string]any{"id": "n_edt0000001", "kind": "editor", "x": 0, "y": 0, "w": 900, "h": 600, "data": map[string]any{}}},
		wireEdge("e_wire000002", testMemTile, "n_orc0000001"),
		wireEdge("e_wire000003", testMemTile, "n_edt0000001"),
	); st != 200 {
		t.Fatalf("wires %d %v", st, b)
	}
	if st, b := h.boardOps(wid,
		map[string]any{"op": "put", "node": map[string]any{"id": "n_note000001", "kind": "note", "x": 0, "y": 0, "w": 200, "h": 200, "data": map[string]any{"text": "x"}}},
		wireEdge("e_wire000004", "n_note000001", "n_edt0000001"),
	); st != 400 || b["code"] != "bad_edge" {
		t.Fatalf("a note to the editor %d %v", st, b)
	}
	if st, b := h.boardOps(wid, memTileOp(map[string]any{"blocks": []any{map[string]any{"name": "critical"}}})); st != 400 || b["code"] != "bad_memory" {
		t.Fatalf("critical notes as a block %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/projects/"+pid+"/memory/plan", map[string]any{"memoryNodeId": "n_none0000001"}, nil); st != 404 {
		t.Fatalf("no tile %d %v", st, b)
	}
}

// A console launched with a tile connected: its CLI gets the stack without a turn, reads
// through its own token what the tile added and nothing more, and the note of all that
// means nothing is typed.
func TestMemoryTileAtLaunch(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	bin := filepath.Join(dir, "spycodex")
	script := "#!/bin/sh\nenv > " + bin + ".env\nexec sleep 120\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "spycodex", "name": "spycodex", "command": bin, "base": "codex"}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	pid, wid := h.homeWorkspace()
	other := h.otherProject("Acme")
	for name, content := range map[string]string{"design": "- buttons are square\n", "deploy": "- deploys on Friday\n"} {
		if st, b := h.do("PUT", "/api/memory/docs/"+name+"?projectId="+other, map[string]any{"title": name, "content": content}, nil); st != 200 {
			t.Fatalf("doc %d %v", st, b)
		}
	}
	if st, b := h.do("PUT", "/api/memory/docs/secret?projectId="+other, map[string]any{"title": "Secret", "content": "- not for you\n"}, nil); st != 200 {
		t.Fatalf("doc %d %v", st, b)
	}
	env := func() string {
		t.Helper()
		var out string
		waitTrue(t, "the CLI started", 20*time.Second, func() bool {
			b, _ := os.ReadFile(bin + ".env")
			out = string(b)
			return strings.Contains(out, "NB_TOKEN=")
		})
		_ = os.Remove(bin + ".env")
		return out
	}
	envOf := func(all, k string) string {
		for _, l := range strings.Split(all, "\n") {
			if v, ok := strings.CutPrefix(l, k+"="); ok {
				return v
			}
		}
		return ""
	}

	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "spycodex", "workspaceId": wid, "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("console %d %v", st, in)
	}
	id := in["id"].(string)
	if cfg := envOf(env(), "NB_CODEX_CFG"); strings.Contains(cfg, "buttons are square") {
		t.Fatalf("no tile yet:\n%s", cfg)
	}
	if st, b := h.boardOps(wid,
		memTileOp(map[string]any{"blocks": []any{map[string]any{"projectId": other, "name": "design"}, map[string]any{"projectId": other, "name": "deploy", "lookup": true}}}),
		wireEdge("e_wire000001", testMemTile, h.terminalNode(wid, id)),
	); st != 200 {
		t.Fatalf("tile %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions/"+id+"/restart", nil, nil); st != 200 {
		t.Fatalf("restart %d %v", st, b)
	}
	all := env()
	cfg := envOf(all, "NB_CODEX_CFG")
	for _, want := range []string{"buttons are square", other + "/deploy"} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("no %q in:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "deploys on Friday") || strings.Contains(cfg, "not for you") {
		t.Fatalf("too much at launch:\n%s", cfg)
	}

	// Nothing is left to type.
	st, plan := h.do("POST", "/api/projects/"+pid+"/memory/plan", map[string]any{"memoryNodeId": testMemTile}, nil)
	if st != 200 || plan["targets"].([]any)[0].(map[string]any)["chars"].(float64) != 0 {
		t.Fatalf("plan %d %v", st, plan)
	}

	tok := envOf(all, "NB_TOKEN")
	url := strings.TrimSuffix(h.srv.hookURL, "/nb/hook") + "/nb/mcp/" + id
	call := func(tool string, args map[string]any) string {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return fmt.Sprint(out)
	}
	if got := call("memory_read", map[string]any{"name": other + "/deploy"}); !strings.Contains(got, "deploys on Friday") {
		t.Fatalf("attached: %s", got)
	}
	if got := call("memory_read", map[string]any{"name": other + "/secret"}); strings.Contains(got, "not for you") {
		t.Fatalf("not attached: %s", got)
	}
	if got := call("memory_search", map[string]any{"query": "Friday"}); !strings.Contains(got, "deploys on Friday") {
		t.Fatalf("search: %s", got)
	}
	if got := call("memory_search", map[string]any{"query": "not for you"}); strings.Contains(got, "not for you\\n") || strings.Contains(got, "- not for you") {
		t.Fatalf("search found what is not attached: %s", got)
	}

	// What it saved itself does not come back.
	if got := call("memory_save", map[string]any{"fact": "the build command is make zebra", "section": "Commands"}); strings.Contains(got, "isError:true") {
		t.Fatalf("save: %s", got)
	}
	st, plan = h.do("POST", "/api/projects/"+pid+"/memory/plan", map[string]any{"memoryNodeId": testMemTile}, nil)
	if st != 200 || plan["targets"].([]any)[0].(map[string]any)["chars"].(float64) != 0 {
		t.Fatalf("its own fact would be sent back: %d %v", st, plan)
	}
}

// A CLI that takes nothing at launch starts each launch with nothing, so what it was
// sent before counts for nothing after a restart.
func TestMemoryRestartWithoutLaunchContext(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	bin := filepath.Join(dir, "plaincli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 120\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "plaincli", "name": "plaincli", "command": bin}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	pid, wid := h.homeWorkspace()
	if st, b := h.do("PUT", "/api/projects/"+pid+"/memory", map[string]any{"scope": "project", "content": "- the project fact\n"}, nil); st != 200 {
		t.Fatalf("memory %d %v", st, b)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "plaincli", "workspaceId": wid, "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("console %d %v", st, in)
	}
	id := in["id"].(string)
	body := map[string]any{"targets": []any{id}, "blocks": []any{map[string]any{"projectId": pid, "name": "main"}}}
	chars := func() float64 {
		t.Helper()
		st, p := h.do("POST", "/api/projects/"+pid+"/memory/plan", body, nil)
		if st != 200 || len(p["targets"].([]any)) != 1 {
			t.Fatalf("plan %d %v", st, p)
		}
		return p["targets"].([]any)[0].(map[string]any)["chars"].(float64)
	}
	if chars() == 0 {
		t.Fatal("it has nothing yet")
	}
	if st, r := h.do("POST", "/api/projects/"+pid+"/memory/push", body, nil); st != 200 || r["delivered"].(float64)+r["busy"].(float64) != 1 {
		t.Fatalf("push %d %v", st, r)
	}
	if chars() != 0 {
		t.Fatal("what was sent still counts as missing")
	}
	if st, b := h.do("POST", "/api/sessions/"+id+"/restart", nil, nil); st != 200 {
		t.Fatalf("restart %d %v", st, b)
	}
	waitTrue(t, "the restart forgets", 10*time.Second, func() bool { return chars() != 0 })
}
