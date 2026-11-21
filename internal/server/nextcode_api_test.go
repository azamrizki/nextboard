package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/azamrizki/nextboard/internal/agent"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeNextCodeCLI plays Claude Code headless (stream-json in and out, permission requests on
// the same pipe) for NextCode consoles (docs/API-agents.md §30): "run tests" asks to run a
// command; anything else is answered "Done: <text>".
const fakeNextCodeCLI = `#!/bin/sh
sid=-; prev=
for a in "$@"; do
  case "$prev" in --session-id|--resume) sid=$a;; esac
  prev=$a
done
printf '%s\n' "$*" >> "$FAKE_DIR/args"
printf '%s\n' "$CLAUDE_CONFIG_DIR" >> "$FAKE_DIR/config"
field() { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p"; }
inited=0
while IFS= read -r line; do
  case "$line" in
  *'"subtype":"initialize"'*)
    printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{"commands":[{"name":"review"}]}}}\n' "$(field "$line" request_id)";;
  *'"type":"user"'*)
    if [ $inited = 0 ]; then inited=1; printf '{"type":"system","subtype":"init","session_id":"%s","model":"fake-model","permissionMode":"default"}\n' "$sid"; fi
    text=$(field "$line" text)
    case "$text" in
    "run tests")
      printf '{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"npm test"}}]},"parent_tool_use_id":null}\n'
      printf '{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"npm test"},"tool_use_id":"toolu_1"}}\n'
      IFS= read -r ans
      case "$ans" in
      *'"behavior":"allow"'*)
        printf '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"3 passed"}]},"parent_tool_use_id":null}\n'
        printf '{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"Tests pass."}]},"parent_tool_use_id":null}\n';;
      *)
        printf '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"denied","is_error":true}]},"parent_tool_use_id":null}\n';;
      esac;;
    *)
      printf '{"type":"assistant","message":{"id":"m%s","content":[{"type":"text","text":"Done: %s"}]},"parent_tool_use_id":null}\n' "$$" "$text";;
    esac
    printf '{"type":"result","subtype":"success","is_error":false,"result":"","session_id":"%s","total_cost_usd":0.02}\n' "$sid";;
  esac
done
`

// readFrame reads chat frames until ok holds.
func readFrame(t *testing.T, ws *websocket.Conn, what string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		var f map[string]any
		if err := ws.ReadJSON(&f); err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if ok(f) {
			return f
		}
	}
}

func itemOf(f map[string]any) map[string]any {
	it, _ := f["item"].(map[string]any)
	return it
}

func TestNextCodeConsoleEndToEnd(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeNextCodeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DIR", dir)
	// the path set for Claude Code is NextCode's CLI
	if st, r := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": bin}}}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, r)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode", "cwd": dir}, nil)
	if st != 201 || in["agent"] != "nextcode" {
		t.Fatalf("create %d %v", st, in)
	}
	id := in["id"].(string)
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" && in["status"] == "running" })

	// keys and a switch to a terminal agent are refused
	if st, _ := h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "x"}, nil); st != 409 {
		t.Fatalf("input %d", st)
	}
	if st, r := h.do("POST", "/api/sessions/"+id+"/restart", map[string]any{"agent": "claude"}, nil); st != 409 || r["code"] != "nextcode_switch" {
		t.Fatalf("switch %d %v", st, r)
	}
	if st, r := h.do("POST", "/api/sessions/"+id+"/handoff", map[string]any{"agent": "claude", "inPlace": true}, nil); st != 409 || r["code"] != "nextcode_switch" {
		t.Fatalf("handoff in place %d %v", st, r)
	}
	// a terminal console has no chat
	shellSt, shell := h.do("POST", "/api/sessions", map[string]any{"agent": "shell", "cwd": dir}, nil)
	if shellSt != 201 {
		t.Fatalf("shell %d %v", shellSt, shell)
	}
	if st, r := h.do("GET", "/api/sessions/"+shell["id"].(string)+"/nextcode", nil, nil); st != 409 || r["code"] != "not_nextcode" {
		t.Fatalf("shell chat %d %v", st, r)
	}

	hdr := http.Header{"Origin": {h.ts.URL}}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	ws, _, err := h.dialWS("/api/sessions/"+id+"/nextcode/ws", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	snap := readFrame(t, ws, "snapshot", func(f map[string]any) bool { return f["t"] == "snapshot" })
	if s, _ := snap["status"].(map[string]any); s["process"] != "stopped" || s["engine"] != "claude" {
		t.Fatalf("snapshot %v", snap)
	}

	if st, r := h.do("POST", "/api/sessions/"+id+"/nextcode/messages", map[string]any{"text": "hello"}, nil); st != 202 {
		t.Fatalf("message %d %v", st, r)
	}
	readFrame(t, ws, "the reply", func(f map[string]any) bool {
		it := itemOf(f)
		return it != nil && it["kind"] == "text" && it["text"] == "Done: hello"
	})
	h.waitInfo(id, "ready after the turn", func(in map[string]any) bool { return in["agentState"] == "ready" })

	// the model switches; a name that is none is refused
	if st, r := h.do("PUT", "/api/sessions/"+id+"/nextcode/model", map[string]any{"model": "sonnet", "effort": "high"}, nil); st != 200 {
		t.Fatalf("model %d %v", st, r)
	}
	if _, info := h.do("GET", "/api/sessions/"+id, nil, nil); info["model"] != "sonnet" || info["effort"] != "high" {
		t.Fatalf("model kept %v %v", info["model"], info["effort"])
	}
	if st, r := h.do("PUT", "/api/sessions/"+id+"/nextcode/model", map[string]any{"model": "not a model!"}, nil); st != 400 || r["code"] != "bad_model" {
		t.Fatalf("bad model %d %v", st, r)
	}

	// a permission request: the console needs the owner, the answer goes back to the CLI
	h.do("POST", "/api/sessions/"+id+"/nextcode/messages", map[string]any{"text": "run tests"}, nil)
	req := readFrame(t, ws, "the request", func(f map[string]any) bool { return f["t"] == "request" })["request"].(map[string]any)
	if req["tool"] != "Bash" || req["kind"] != "permission" {
		t.Fatalf("request %v", req)
	}
	in = h.waitInfo(id, "needs you", func(in map[string]any) bool { return in["agentState"] == "needsYou" })
	if act, _ := in["activity"].(string); !strings.Contains(act, "Bash(npm test)") {
		t.Fatalf("activity %q", act)
	}
	if st, r := h.do("POST", "/api/sessions/"+id+"/nextcode/requests/bogus", map[string]any{"decision": "allow"}, nil); st != 404 || r["code"] != "request_gone" {
		t.Fatalf("bogus answer %d %v", st, r)
	}
	if st, r := h.do("POST", "/api/sessions/"+id+"/nextcode/requests/"+req["id"].(string), map[string]any{"decision": "maybe"}, nil); st != 400 {
		t.Fatalf("bad decision %d %v", st, r)
	}
	if st, r := h.do("POST", "/api/sessions/"+id+"/nextcode/requests/"+req["id"].(string), map[string]any{"decision": "allow"}, nil); st != 200 {
		t.Fatalf("answer %d %v", st, r)
	}
	readFrame(t, ws, "the tool's result", func(f map[string]any) bool {
		it := itemOf(f)
		tool, _ := it["tool"].(map[string]any)
		return tool != nil && tool["status"] == "done" && tool["output"] == "3 passed"
	})
	h.waitInfo(id, "ready after the tests", func(in map[string]any) bool { return in["agentState"] == "ready" })

	// an order goes in as a message
	if st, d := h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "from an order"}, nil); st != 202 {
		t.Fatalf("delivery %d %v", st, d)
	}
	readFrame(t, ws, "the order's reply", func(f map[string]any) bool {
		it := itemOf(f)
		return it != nil && it["text"] == "Done: from an order"
	})

	// the chat over REST, and the CLI's launch
	_, chat := h.do("GET", "/api/sessions/"+id+"/nextcode", nil, nil)
	items, _ := chat["items"].([]any)
	if len(items) < 7 {
		t.Fatalf("chat %v", chat)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "args"))
	args := string(b)
	for _, want := range []string{"-p --input-format stream-json --output-format stream-json --verbose --include-partial-messages --permission-prompt-tool stdio", "--session-id "} {
		if !strings.Contains(args, want) {
			t.Fatalf("no %q in the CLI's launch %q", want, args)
		}
	}
	_, info := h.do("GET", "/api/sessions/"+id, nil, nil)
	if aid, _ := info["agentSessionId"].(string); len(aid) != 36 || !strings.Contains(args, "--session-id "+aid) {
		t.Fatalf("agentSessionId %v, launch %q", info["agentSessionId"], args)
	}

	// deleting the console ends its chat and removes its file
	if st, _ := h.do("DELETE", "/api/sessions/"+id, nil, nil); st != 200 && st != 204 {
		t.Fatalf("delete %d", st)
	}
	if _, err := os.Stat(h.srv.st.Path("nextcode", id+".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("chat file after delete: %v", err)
	}
}

// NextCode is independent of NextBoard's Claude Code (owner, 2026-11-20): its own home profile
// and login, never Claude Code's profiles, and none of the hub's memory or tools at launch, also
// in a project. A Claude Code console in the same project keeps all of it.
func TestNextCodeIsIndependent(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeNextCodeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DIR", dir)
	h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": bin}}}}, nil)

	var profiles []map[string]any
	h.getJSON("/api/profiles", &profiles)
	home := map[string]map[string]any{}
	for _, p := range profiles {
		if p["home"] == true {
			home[p["agent"].(string)] = p
		}
	}
	nc, cc := home["nextcode"], home["claude"]
	if nc == nil || cc == nil || nc["dir"] == cc["dir"] || !strings.HasSuffix(nc["dir"].(string), filepath.Join("agents", "nextcode")) ||
		nc["name"] != "NextCode" || cc["name"] != "NextBoard" {
		t.Fatalf("home profiles %v", home)
	}

	proj := filepath.Join(dir, "shop")
	_ = os.MkdirAll(proj, 0o755)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": proj, "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	wid := p["workspaces"].([]any)[0].(map[string]any)["id"].(string)

	// Claude Code's login is not NextCode's
	if st, r := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode", "workspaceId": wid, "cwd": proj, "profileId": cc["id"]}, nil); st != 400 || r["code"] != "profile_mismatch" {
		t.Fatalf("nextcode with Claude Code's profile: %d %v", st, r)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode", "workspaceId": wid, "cwd": proj}, nil)
	if st != 201 || in["profileId"] != nc["id"] {
		t.Fatalf("nextcode console %d %v", st, in)
	}
	id := in["id"].(string)
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" })
	h.do("POST", "/api/sessions/"+id+"/nextcode/messages", map[string]any{"text": "hello"}, nil)
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, chat := h.do("GET", "/api/sessions/"+id+"/nextcode", nil, nil)
		if items, _ := chat["items"].([]any); len(items) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no reply: %v", chat)
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "args"))
	args := string(b)
	// nothing of the hub's memory, tools or MCP, and nothing of the CLI's own setup (Clean NextCode):
	// NextCode's own skills and memory folders only, and its own prompt instead of Claude Code's
	for _, not := range []string{"--mcp-config", "autoMemoryDirectory", "mcp__nextboard", "/memory/" + "p_", h.srv.st.Path("memory")} {
		if strings.Contains(args, not) {
			t.Fatalf("NextCode's launch has %q: %s", not, args)
		}
	}
	for _, want := range []string{"--setting-sources  --strict-mcp-config --disable-slash-commands --system-prompt-file ", "--tools " + agent.NextCodeTools,
		h.srv.st.Path("nextcode", "skills"), h.srv.st.Path("nextcode", "memory")} {
		if !strings.Contains(args, want) {
			t.Fatalf("NextCode's launch lacks %q: %s", want, args)
		}
	}
	b, _ = os.ReadFile(filepath.Join(dir, "config"))
	if got := strings.TrimSpace(string(b)); got != nc["dir"] {
		t.Fatalf("NextCode's CLAUDE_CONFIG_DIR %q, want its own %q", got, nc["dir"])
	}

	// its profile signs in in Claude Code's own terminal, with NextCode's folder
	st, sign := h.do("POST", "/api/profiles/"+nc["id"].(string)+"/signin", nil, nil)
	if st != 201 || sign["agent"] != "claude" || sign["profileId"] != nc["id"] {
		t.Fatalf("sign-in %d %v", st, sign)
	}
	h.do("DELETE", "/api/sessions/"+sign["id"].(string), nil, nil)

	// a Claude Code console in the same project is as it was: its own login
	st, cl := h.do("POST", "/api/sessions", map[string]any{"agent": "claude", "workspaceId": wid, "cwd": proj}, nil)
	if st != 201 || cl["profileId"] != cc["id"] {
		t.Fatalf("claude console %d %v", st, cl)
	}
	h.do("DELETE", "/api/sessions/"+cl["id"].(string), nil, nil)
	h.do("DELETE", "/api/sessions/"+id, nil, nil)
}

// Settings › NextCode: its own section, checked.
func TestNextCodeSettings(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	_, st := h.do("GET", "/api/settings", nil, nil)
	if nc, _ := st["nextcode"].(map[string]any); nc["permissionMode"] != "default" || nc["idleExitMin"] != float64(30) {
		t.Fatalf("defaults %v", st["nextcode"])
	}
	if code, r := h.do("PATCH", "/api/settings", map[string]any{"nextcode": map[string]any{"permissionMode": "acceptEdits", "idleExitMin": 5}}, nil); code != 200 {
		t.Fatalf("patch %d %v", code, r)
	}
	for _, bad := range []map[string]any{{"permissionMode": "yolo"}, {"idleExitMin": 0}, {"idleExitMin": 2000}} {
		if code, r := h.do("PATCH", "/api/settings", map[string]any{"nextcode": bad}, nil); code != 400 {
			t.Fatalf("bad %v: %d %v", bad, code, r)
		}
	}
}

// NextCode where the hub's tools do the work (a server, or a read-only part on This PC, which is
// reached the same way): the CLI on This PC with the server's tools only, never NextBoard's
// memory, missions, board or credentials.
func TestNextCodeOnServerTools(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeNextCodeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DIR", dir)
	h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": bin}}}}, nil)
	api, ops := filepath.Join(dir, "api"), filepath.Join(dir, "ops")
	for _, d := range []string{api, ops} {
		_ = os.MkdirAll(d, 0o755)
	}
	_ = os.WriteFile(filepath.Join(ops, "deploy.sh"), []byte("echo deploy\n"), 0o644)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": api, "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	_, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Ops", "machineId": "local", "path": ops}, nil)
	opsID := p["parts"].([]any)[1].(map[string]any)["id"].(string)
	h.do("PATCH", "/api/projects/"+pid+"/parts/"+opsID, map[string]any{"readOnly": true}, nil)

	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode", "workspaceId": wid, "partId": opsID}, nil)
	if st != 201 || in["remoteMachineId"] != "local" || in["remoteCwd"] != ops {
		t.Fatalf("console %d %v", st, in)
	}
	id := in["id"].(string)
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" })
	h.do("POST", "/api/sessions/"+id+"/nextcode/messages", map[string]any{"text": "hello"}, nil)
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, chat := h.do("GET", "/api/sessions/"+id+"/nextcode", nil, nil)
		if items, _ := chat["items"].([]any); len(items) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no reply: %v", chat)
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "args"))
	args := string(b)
	for _, want := range []string{"--tools Read,Write,Edit,TodoWrite,AskUserQuestion,ExitPlanMode", "--strict-mcp-config", "/nb/mcp/" + id, "read only for you"} {
		if !strings.Contains(args, want) && !(want == "read only for you" && strings.Contains(args, "--system-prompt-file")) {
			t.Fatalf("no %q in the launch %s", want, args)
		}
	}
	if strings.Contains(args, "autoMemoryDirectory") || strings.Contains(args, "mcp__nextboard__memory") {
		t.Fatalf("memory in the launch %s", args)
	}

	// its MCP endpoint: the server's tools, read only, and nothing of NextBoard's memory
	sess, _ := h.srv.sessions.Get(id)
	rpc := func(method string, params any) map[string]any {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", strings.TrimSuffix(h.srv.hookURL, "/nb/hook")+"/nb/mcp/"+id, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+sess.HookToken())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	names := map[string]bool{}
	for _, x := range rpc("tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
		names[x.(map[string]any)["name"].(string)] = true
	}
	if !names["read_file"] || !names["grep"] || names["memory_search"] || names["board_diagram"] || names["mission_read"] {
		t.Fatalf("tools %v", names)
	}
	// its own part stays read only (the write tools are there for the parts it may change)
	rpc("tools/call", map[string]any{"name": "write_file", "arguments": map[string]any{"path": "new.txt", "content": "x"}})
	if _, err := os.Stat(filepath.Join(ops, "new.txt")); err == nil {
		t.Fatal("wrote into its read-only part")
	}
	if out := fmt.Sprint(rpc("tools/call", map[string]any{"name": "memory_search", "arguments": map[string]any{"query": "x"}})); !strings.Contains(out, "does not have the tool") {
		t.Fatalf("memory_search %s", out)
	}
	if out := fmt.Sprint(rpc("tools/call", map[string]any{"name": "read_file", "arguments": map[string]any{"path": "deploy.sh"}})); !strings.Contains(out, "echo deploy") {
		t.Fatalf("read_file %s", out)
	}
	h.do("DELETE", "/api/sessions/"+id, nil, nil)
}

// NextCode on Codex's engine: its own console, and a NextCode console that changes engine starts
// a new conversation on the other engine (the old chat stays beside it).
func TestNextCodeCodexEngine(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode-codex", "cwd": dir}, nil)
	if st != 201 || in["agent"] != "nextcode-codex" {
		t.Fatalf("codex console %d %v", st, in)
	}
	cid := in["id"].(string)
	h.waitInfo(cid, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" })
	if _, chat := h.do("GET", "/api/sessions/"+cid+"/nextcode", nil, nil); chat["status"].(map[string]any)["engine"] != "codex" {
		t.Fatalf("engine %v", chat["status"])
	}
	// a server is the Claude engine's only
	var list []map[string]any
	h.getJSON("/api/profiles", &list)
	for _, p := range list {
		if p["agent"] == "nextcode-codex" && (p["home"] != true || p["name"] != "NextCode") {
			t.Fatalf("codex engine home %v", p)
		}
	}

	st, in = h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode", "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("claude console %d %v", st, in)
	}
	id := in["id"].(string)
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" })
	if st, r := h.do("POST", "/api/sessions/"+id+"/restart", map[string]any{"agent": "nextcode-codex"}, nil); st != 200 {
		t.Fatalf("engine switch %d %v", st, r)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, chat := h.do("GET", "/api/sessions/"+id+"/nextcode", nil, nil)
		if s, _ := chat["status"].(map[string]any); s != nil && s["engine"] == "codex" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still on the old engine: %v", chat["status"])
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st, r := h.do("POST", "/api/sessions/"+id+"/restart", map[string]any{"agent": "claude"}, nil); st != 409 || r["code"] != "nextcode_switch" {
		t.Fatalf("to a terminal %d %v", st, r)
	}
	h.do("DELETE", "/api/sessions/"+id, nil, nil)
	h.do("DELETE", "/api/sessions/"+cid, nil, nil)
}
