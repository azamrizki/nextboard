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
)

// mcpCall is one JSON-RPC call to the hub's MCP server of a console, as a CLI makes it.
func mcpCall(t *testing.T, url, token, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		t.Fatalf("%s: HTTP %d %v", method, resp.StatusCode, out)
	}
	return out
}

func toolNames(res map[string]any) []string {
	var names []string
	for _, x := range res["result"].(map[string]any)["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	return names
}

// NextCode's multi-agent (docs/API-agents.md §30): its console's CLI calls the agent tool of the
// hub's MCP server with two tasks; each runs as a sub-agent (here the fake CLI) side by side,
// shows in the console's chat under a card of its own, and its report comes back. A sub-agent's
// own token gets no agent tool.
func TestNextCodeSubAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	t.Setenv("FAKE_DIR", dir)
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeNextCodeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": bin}}}}, nil)
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode", "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("console %d %v", st, in)
	}
	id := in["id"].(string)
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" })
	sess, _ := h.srv.sessions.Get(id)
	url := strings.TrimSuffix(h.srv.hookURL, "/nb/hook") + "/nb/mcp/" + id

	if names := toolNames(mcpCall(t, url, sess.HookToken(), "tools/list", map[string]any{})); len(names) != 1 || names[0] != "agent" {
		t.Fatalf("a NextCode console's hub tools %v", names)
	}
	res := mcpCall(t, url, sess.HookToken(), "tools/call", map[string]any{"name": "agent", "arguments": map[string]any{"tasks": []any{
		map[string]any{"description": "Look at the API", "prompt": "list the routes"},
		map[string]any{"description": "Look at the web", "prompt": "list the pages"},
	}}})
	r := res["result"].(map[string]any)
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if r["isError"] == true || !strings.Contains(text, "## Task 1: Look at the API") || !strings.Contains(text, "## Task 2: Look at the web") ||
		!strings.Contains(text, "list the routes") || !strings.Contains(text, "list the pages") {
		t.Fatalf("the reports:\n%s", text)
	}
	// each sub-agent is a card in the console's chat, its work under it
	_, chat := h.do("GET", "/api/sessions/"+id+"/nextcode", nil, nil)
	var cards []map[string]any
	children := map[string]int{}
	for _, x := range chat["items"].([]any) {
		it := x.(map[string]any)
		if tool, _ := it["tool"].(map[string]any); tool != nil && tool["name"] == "Agent" {
			cards = append(cards, it)
		}
		if p, _ := it["parent"].(string); p != "" {
			children[p]++
		}
	}
	if len(cards) != 2 {
		t.Fatalf("cards %v", chat["items"])
	}
	for _, c := range cards {
		tool := c["tool"].(map[string]any)
		if tool["status"] != "done" || !strings.HasPrefix(fmt.Sprint(tool["output"]), "Done: ") || children[c["id"].(string)] < 2 {
			t.Fatalf("card %v (children %d)", c, children[c["id"].(string)])
		}
	}
	// two CLIs started for them, each a conversation of its own; the console's own is untouched
	b, _ := os.ReadFile(filepath.Join(dir, "args"))
	if n := strings.Count(string(b), "--session-id"); n != 2 {
		t.Fatalf("CLI starts %d: %s", n, b)
	}
	if _, info := h.do("GET", "/api/sessions/"+id, nil, nil); info["agentSessionId"] != nil && info["agentSessionId"] != "" {
		t.Fatalf("the console's conversation changed: %v", info["agentSessionId"])
	}

	// a sub-agent's token: the console's hub tools, but no agent of its own
	h.srv.subTokens.Store("sa_test", id)
	if names := toolNames(mcpCall(t, url, "sa_test", "tools/list", map[string]any{})); len(names) != 0 {
		t.Fatalf("a sub-agent's hub tools %v", names)
	}
	res = mcpCall(t, url, "sa_test", "tools/call", map[string]any{"name": "agent", "arguments": map[string]any{"tasks": []any{map[string]any{"prompt": "x"}}}})
	if res["result"].(map[string]any)["isError"] != true {
		t.Fatalf("a sub-agent started one: %v", res)
	}
	h.srv.subTokens.Delete("sa_test")
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer sa_test")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 401 {
		t.Fatalf("a token that is gone %v %v", resp, err)
	}
	h.do("DELETE", "/api/sessions/"+id, nil, nil)
}
