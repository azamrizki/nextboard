package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// unexpanded is an argument that is only a variable's name: argv is written one
// argument per line. Hook commands rightly mention variables for later.
var unexpanded = regexp.MustCompile(`(?m)^\$NB_[A-Z0-9_]+$`)

// spyCLI stands in for an agent's CLI: it writes down how it was started.
func spyCLI(t *testing.T, h *testHub, dir, name, base string) string {
	t.Helper()
	bin := filepath.Join(dir, name)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + bin + ".argv\nenv > " + bin + ".env\nexec sleep 120\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": name, "name": name, "command": bin, "base": base}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	return bin
}

func waitSpy(t *testing.T, bin string) (argv, env string) {
	t.Helper()
	waitTrue(t, filepath.Base(bin)+" started", 20*time.Second, func() bool {
		a, _ := os.ReadFile(bin + ".argv")
		e, _ := os.ReadFile(bin + ".env")
		argv, env = string(a), string(e)
		return strings.Contains(env, "NB_TOKEN=")
	})
	_ = os.Remove(bin + ".argv")
	_ = os.Remove(bin + ".env")
	return argv, env
}

// TestMCPServersReachEveryCLI: set once in NextBoard, every CLI gets it at launch,
// with its secret only in the process environment.
func TestMCPServersReachEveryCLI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	claude, codex := spyCLI(t, h, dir, "spyclaude", "claude"), spyCLI(t, h, dir, "spycodex", "codex")
	_, wid := h.homeWorkspace()
	const secret = "tok-very-secret"
	st, sv := h.do("POST", "/api/mcp/servers", map[string]any{"id": "docs", "transport": "http", "url": "https://mcp.example.com/mcp", "enabled": true,
		"headers": []any{map[string]any{"name": "Authorization", "value": "Bearer " + secret}}}, nil)
	if st != 201 || strings.Contains(fmt.Sprint(sv), secret) {
		t.Fatalf("create %d %v", st, sv)
	}
	if st, b := h.do("POST", "/api/mcp/servers", map[string]any{"id": "docs", "transport": "http", "url": "https://x.example.com"}, nil); st != 409 || b["code"] != "mcp_exists" {
		t.Fatalf("twice %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/mcp/servers", map[string]any{"id": "nextboard", "transport": "http", "url": "https://x.example.com"}, nil); st != 400 || b["code"] != "bad_mcp" {
		t.Fatalf("reserved %d %v", st, b)
	}
	if _, l := h.do("GET", "/api/mcp/servers", nil, nil); strings.Contains(fmt.Sprint(l), secret) {
		t.Fatalf("list shows the value: %v", l)
	}
	for id, bin := range map[string]string{"spyclaude": claude, "spycodex": codex} {
		if st, in := h.do("POST", "/api/sessions", map[string]any{"agent": id, "workspaceId": wid, "cwd": dir}, nil); st != 201 {
			t.Fatalf("%s %d %v", id, st, in)
		}
		argv, env := waitSpy(t, bin)
		if strings.Contains(argv, secret) {
			t.Fatalf("%s: the value is on its command line:\n%s", id, argv)
		}
		// What the CLI receives, after the wrapper: no variable left unexpanded.
		if unexpanded.MatchString(argv) {
			t.Fatalf("%s: a variable reached the CLI as text:\n%s", id, argv)
		}
		if !strings.Contains(env, "Bearer "+secret) {
			t.Fatalf("%s: the value did not arrive", id)
		}
		if !strings.Contains(argv, "mcp.example.com") {
			t.Fatalf("%s: the server is not in what the CLI was given:\n%s", id, argv)
		}
	}
	// Only the sealed registry keeps it.
	entries, err := os.ReadDir(h.srv.st.Path("."))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if b, _ := os.ReadFile(h.srv.st.Path(e.Name())); strings.Contains(string(b), secret) {
			t.Fatalf("the value is in the clear in %s", e.Name())
		}
	}
	// Taken away, the next launch does not have it.
	if st, b := h.do("DELETE", "/api/mcp/servers/docs", nil, nil); st != 200 {
		t.Fatalf("delete %d %v", st, b)
	}
	if st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "spycodex", "workspaceId": wid, "cwd": dir}, nil); st != 201 {
		t.Fatalf("%d %v", st, in)
	}
	if argv, env := waitSpy(t, codex); strings.Contains(argv+env, "mcp.example.com") {
		t.Fatal("still there after deleting")
	}
}

func TestMCPTestEndpoint(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, _ := h.do("POST", "/api/mcp/servers", map[string]any{"id": "cmd", "transport": "stdio", "command": "sh", "enabled": true,
		"args": []any{"-c", `while read -r l; do case "$l" in *'"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{}}';; *'"tools/list"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"look_up"}]}}';; esac; done`}}, nil)
	if st != 201 {
		t.Fatalf("create %d", st)
	}
	st, r := h.do("POST", "/api/mcp/servers/cmd/test", nil, nil)
	if st != 200 || r["ok"] != true || fmt.Sprint(r["tools"]) != "[look_up]" {
		t.Fatalf("test %d %v", st, r)
	}
	if st, b := h.do("POST", "/api/mcp/servers/nope/test", nil, nil); st != 404 || b["code"] != "mcp_not_found" {
		t.Fatalf("missing %d %v", st, b)
	}
}

// TestSwitchKeepsTheTile: the same console takes the next agent; going back
// continues the conversation it left, under its own id.
func TestSwitchKeepsTheTile(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	claude, codex := spyCLI(t, h, dir, "spyclaude", "claude"), spyCLI(t, h, dir, "spycodex", "codex")
	_, wid := h.homeWorkspace()
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "spyclaude", "workspaceId": wid, "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("%d %v", st, in)
	}
	id := in["id"].(string)
	handle := in["handle"].(string)
	waitSpy(t, claude)
	_, started := h.do("GET", "/api/sessions/"+id, nil, nil)
	firstConv, _ := started["agentSessionId"].(string)
	if firstConv == "" {
		t.Fatalf("no conversation id: %v", started)
	}
	// As if it had a conversation: the hook names the transcript of its first turn.
	h.srv.sessions.TestSetTranscript(id, filepath.Join(dir, "conv.jsonl"))

	switchTo := func(body map[string]any) string {
		t.Helper()
		body["inPlace"] = true
		st, ho := h.do("POST", "/api/sessions/"+id+"/handoff", body, nil)
		if st != 202 {
			t.Fatalf("switch %d %v", st, ho)
		}
		return ho["id"].(string)
	}
	view := func() map[string]any {
		t.Helper()
		_, v := h.do("GET", "/api/sessions/"+id, nil, nil)
		return v
	}

	switchTo(map[string]any{"agent": "spycodex", "noBrief": true})
	waitSpy(t, codex)
	if v := view(); v["agent"] != "spycodex" || v["handle"] != handle || v["name"] == "spyclaude" {
		t.Fatalf("after the switch %v", v)
	}
	var past []struct {
		Agent          string `json:"agent"`
		AgentSessionID string `json:"agentSessionId"`
	}
	raw, _ := json.Marshal(view()["pastRuns"])
	if err := json.Unmarshal(raw, &past); err != nil || len(past) != 1 || past[0].Agent != "spyclaude" || past[0].AgentSessionID != firstConv {
		t.Fatalf("what it left: %s", raw)
	}

	// Going back: the same conversation, not a new one.
	switchTo(map[string]any{"agent": "spyclaude", "noBrief": true})
	argv, _ := waitSpy(t, claude)
	if !strings.Contains(argv, firstConv) || !strings.Contains(argv, "--resume") {
		t.Fatalf("must resume %s:\n%s", firstConv, argv)
	}
	v := view()
	if v["agentSessionId"] != firstConv || v["agent"] != "spyclaude" {
		t.Fatalf("back %v", v)
	}
	raw, _ = json.Marshal(v["pastRuns"])
	if strings.Contains(string(raw), "spyclaude") {
		t.Fatalf("it is no longer left behind: %s", raw)
	}

	// Going back but starting over: a new conversation.
	h.srv.sessions.TestSetTranscript(id, filepath.Join(dir, "conv.jsonl"))
	switchTo(map[string]any{"agent": "spycodex", "noBrief": true})
	waitSpy(t, codex)
	switchTo(map[string]any{"agent": "spyclaude", "noBrief": true, "fresh": true})
	argv, _ = waitSpy(t, claude)
	if strings.Contains(argv, firstConv) {
		t.Fatalf("asked to start over:\n%s", argv)
	}
}
