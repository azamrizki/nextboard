package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptModel is a model on loopback that answers Claude Code (Messages), Codex (Responses) and
// OpenCode (Chat Completions). When the latest user text holds the marker of the step it was
// given, it asks for that tool call; after a tool result it says "done"; else "ok". It keeps
// every request and the tool results it got.
type scriptModel struct {
	*httptest.Server
	mu      sync.Mutex
	bodies  []string
	results []string
	step    *scriptStep
}

type scriptStep struct {
	marker string
	tool   string         // the tool's name as this CLI calls it
	input  map[string]any // its arguments
	asked  bool
}

func (m *scriptModel) set(st *scriptStep) {
	m.mu.Lock()
	m.step = st
	m.mu.Unlock()
}

func (m *scriptModel) all() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.bodies, "\n")
}

// firstWith is the first request whose text holds s and offers the tool (a main turn, not a
// title or summary on the side).
func (m *scriptModel) firstWith(s, tool string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.bodies {
		if strings.Contains(b, s) && strings.Contains(b, `"name":"`+tool+`"`) {
			return b
		}
	}
	return ""
}

func (m *scriptModel) toolResults() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.results, "\n")
}

func newScriptModel(t *testing.T) *scriptModel {
	m := &scriptModel{}
	event := func(w io.Writer, name string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
	}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		lastText, result, tools := scriptTurn(r.URL.Path, body)
		m.mu.Lock()
		m.bodies = append(m.bodies, string(raw))
		// a CLI may send the last turn's tool result with the next prompt, in one user message
		st := m.step
		fresh := st != nil && !st.asked && strings.Contains(lastText, st.marker)
		if result != "" && !fresh {
			m.results = append(m.results, result)
		}
		var call *scriptStep
		if fresh && tools[st.tool] {
			st.asked, call = true, st
			result = ""
		}
		m.mu.Unlock()
		text := "ok"
		if result != "" {
			text = "done"
		}
		args, _ := json.Marshal(map[string]any{})
		if call != nil {
			args, _ = json.Marshal(call.input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/messages"):
			usage := map[string]int{"input_tokens": 1, "output_tokens": 1}
			event(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "m", "type": "message", "role": "assistant", "model": "x", "content": []any{}, "usage": usage}})
			stop := "end_turn"
			if call != nil {
				stop = "tool_use"
				event(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_01", "name": call.tool, "input": map[string]any{}}})
				event(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": string(args)}})
			} else {
				event(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
				event(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": text}})
			}
			event(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
			event(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": stop}, "usage": usage})
			event(w, "message_stop", map[string]string{"type": "message_stop"})
		case strings.HasSuffix(p, "/responses"):
			event(w, "response.created", map[string]any{"type": "response.created", "response": map[string]string{"id": "r"}})
			if call != nil {
				event(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0,
					"item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": call.tool, "arguments": string(args)}})
			} else {
				event(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant", "id": "i", "content": []any{map[string]string{"type": "output_text", "text": text}}}})
			}
			event(w, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "r", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
		case strings.HasSuffix(p, "/chat/completions"):
			chunk := func(delta map[string]any, stop any) {
				b, _ := json.Marshal(map[string]any{"id": "c", "object": "chat.completion.chunk", "model": "x", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": stop}}})
				fmt.Fprintf(w, "data: %s\n\n", b)
			}
			if call != nil {
				chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
					"function": map[string]any{"name": call.tool, "arguments": string(args)}}}}, nil)
				chunk(map[string]any{}, "tool_calls")
			} else {
				chunk(map[string]any{"role": "assistant", "content": text}, nil)
				chunk(map[string]any{}, "stop")
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[],"models":[],"object":"list"}`)
		}
	}))
	t.Cleanup(m.Close)
	return m
}

// scriptTurn reads a request: the latest user text, the tool result it ends with ("" = none),
// and the tools it offers.
func scriptTurn(path string, body map[string]any) (lastText, result string, tools map[string]bool) {
	tools = map[string]bool{}
	for _, x := range asList(body["tools"]) {
		t, _ := x.(map[string]any)
		if n, _ := t["name"].(string); n != "" {
			tools[n] = true
		}
		if f, _ := t["function"].(map[string]any); f != nil {
			if n, _ := f["name"].(string); n != "" {
				tools[n] = true
			}
		}
	}
	text := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	switch {
	case strings.HasSuffix(path, "/messages"):
		msgs := asList(body["messages"])
		for i := len(msgs) - 1; i >= 0; i-- {
			m, _ := msgs[i].(map[string]any)
			if m["role"] != "user" {
				continue
			}
			for _, c := range asList(m["content"]) {
				if cm, _ := c.(map[string]any); cm["type"] == "tool_result" && i == len(msgs)-1 {
					result = text(cm["content"])
				}
			}
			return text(m["content"]), result, tools
		}
	case strings.HasSuffix(path, "/responses"):
		in := asList(body["input"])
		if n := len(in); n > 0 {
			if last, _ := in[n-1].(map[string]any); last["type"] == "function_call_output" || last["type"] == "custom_tool_call_output" {
				result = text(last["output"])
			}
		}
		for i := len(in) - 1; i >= 0; i-- {
			if m, _ := in[i].(map[string]any); m["role"] == "user" {
				return text(m["content"]), result, tools
			}
		}
	case strings.HasSuffix(path, "/chat/completions"):
		msgs := asList(body["messages"])
		if n := len(msgs); n > 0 {
			if last, _ := msgs[n-1].(map[string]any); last["role"] == "tool" {
				result = text(last["content"])
			}
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			if m, _ := msgs[i].(map[string]any); m["role"] == "user" {
				return text(m["content"]), result, tools
			}
		}
	}
	return "", result, tools
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// TestRealRootConsole runs the installed Claude Code, Codex and OpenCode as consoles at the root
// of a project of two parts on This PC (Web, linked; Docs, read only, a note) through the hub,
// against a scripted model on loopback, from an empty home: no account, nothing of the owner's.
// Each one is asked to write through the root's link to Web (allowed by --add-dir /
// external_directory), then into Docs' real folder (not given to it: it must ask or be refused).
// No dialog is answered: a question is the result.
//
//	NEXTBOARD_REAL_ROOT_TEST=1 go test ./internal/server -run TestRealRootConsole -v
func TestRealRootConsole(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_ROOT_TEST") != "1" {
		t.Skip("set NEXTBOARD_REAL_ROOT_TEST=1 (installed CLIs, a model on loopback, no account)")
	}
	home := t.TempDir()
	t.Setenv("HOME", home) // nothing of the owner's logins or settings is read
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		for _, pre := range []string{"CLAUDE", "ANTHROPIC", "CODEX", "OPENAI", "OPENCODE", "XDG_"} {
			if strings.HasPrefix(k, pre) {
				t.Setenv(k, "")
				os.Unsetenv(k)
			}
		}
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "1")
	t.Setenv("DISABLE_AUTOUPDATER", "1")

	type step struct {
		name, tool string
		input      func(root, web, docs string) map[string]any
		file       func(web, docs string) string
		allowed    bool // expected: written without a question
	}
	codexSteps := []step{
		{"write through the link", "exec_command", func(root, web, docs string) map[string]any {
			return map[string]any{"cmd": "printf 'hi\\n' > Web/from-codex.txt"}
		}, func(web, docs string) string { return filepath.Join(web, "from-codex.txt") }, true},
		{"write in the read-only part's folder", "exec_command", func(root, web, docs string) map[string]any {
			return map[string]any{"cmd": "printf 'hi\\n' > " + filepath.Join(docs, "from-codex.txt")}
		}, func(web, docs string) string { return filepath.Join(docs, "from-codex.txt") }, false},
	}
	for _, c := range []struct {
		agent, kind, api string
		steps            []step
		sandbox          bool // the project's sandbox (§27) on
	}{
		{"codex", "openai", "responses", codexSteps, true},
		{"claude", "anthropic", "", []step{
			{"write through the link", "Write", func(root, web, docs string) map[string]any {
				return map[string]any{"file_path": filepath.Join(root, "Web", "from-claude.txt"), "content": "hi\n"}
			}, func(web, docs string) string { return filepath.Join(web, "from-claude.txt") }, true},
			{"write in the read-only part's folder", "Write", func(root, web, docs string) map[string]any {
				return map[string]any{"file_path": filepath.Join(docs, "from-claude.txt"), "content": "hi\n"}
			}, func(web, docs string) string { return filepath.Join(docs, "from-claude.txt") }, false},
		}, false},
		{"codex", "openai", "responses", codexSteps, false},
		{"opencode", "openai", "chat", []step{
			{"write through the link", "write", func(root, web, docs string) map[string]any {
				return map[string]any{"filePath": filepath.Join(root, "Web", "from-opencode.txt"), "content": "hi\n"}
			}, func(web, docs string) string { return filepath.Join(web, "from-opencode.txt") }, true},
			{"write by the linked part's real path", "write", func(root, web, docs string) map[string]any {
				return map[string]any{"filePath": filepath.Join(web, "real-opencode.txt"), "content": "hi\n"}
			}, func(web, docs string) string { return filepath.Join(web, "real-opencode.txt") }, true},
			{"write in the read-only part's folder", "write", func(root, web, docs string) map[string]any {
				return map[string]any{"filePath": filepath.Join(docs, "from-opencode.txt"), "content": "hi\n"}
			}, func(web, docs string) string { return filepath.Join(docs, "from-opencode.txt") }, false},
		}, false},
	} {
		name := c.agent
		if c.sandbox {
			name += "-sandbox"
		}
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath(c.agent); err != nil {
				t.Skip("not installed")
			}
			model := newScriptModel(t)
			h := newTestHub(t)
			h.setup()
			if c.agent == "claude" {
				// Claude Code 2.1.325 forces its default permission mode, where every edit asks, when
				// CLAUDE_CODE_SUBPROCESS_ENV_SCRUB is set, which a provider console gets when bwrap
				// is installed. Without it the console runs in acceptEdits, where only a write outside
				// its folders asks: the case this test is about.
				shim := filepath.Join(t.TempDir(), "claude-noscrub")
				real, _ := exec.LookPath("claude")
				os.WriteFile(shim, []byte("#!/bin/sh\nunset CLAUDE_CODE_SUBPROCESS_ENV_SCRUB\nexec "+real+" \"$@\"\n"), 0o755)
				if st, b := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": shim}}}}, nil); st != 200 {
					t.Fatalf("settings %d %v", st, b)
				}
			}
			// outside /tmp: Codex's sandbox lets every command write in /tmp
			web, docs := outsideTmp(t), outsideTmp(t)
			os.WriteFile(filepath.Join(web, "CLAUDE.md"), []byte("WEB-PART-RULES\n"), 0o644)
			os.WriteFile(filepath.Join(web, "AGENTS.md"), []byte("WEB-PART-RULES\n"), 0o644)
			st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": web, "name": "acme-shop", "partName": "Web"}, nil)
			if st != 201 {
				t.Fatalf("project %d %v", st, p)
			}
			pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
			_, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Docs", "machineId": "local", "path": docs}, nil)
			h.do("PATCH", "/api/projects/"+pid+"/parts/"+p["parts"].([]any)[1].(map[string]any)["id"].(string), map[string]any{"readOnly": true}, nil)
			if c.sandbox {
				h.do("PATCH", "/api/projects/"+pid, map[string]any{"sandbox": "workspace"}, nil)
			}
			body := map[string]any{"name": "Loopback", "kind": c.kind, "baseUrl": model.URL + "/v1", "model": "fake-model", "apiKey": "not-a-real-key"}
			if c.api != "" {
				body["api"] = c.api
			}
			st, pv := h.do("POST", "/api/providers", body, nil)
			if st != 201 {
				t.Fatalf("provider %d %v", st, pv)
			}
			st, in := h.do("POST", "/api/sessions", map[string]any{"agent": c.agent, "workspaceId": wid, "providerId": pv["id"], "root": true, "yolo": false}, nil)
			if st != 201 || in["root"] != true {
				t.Fatalf("console %d %v", st, in)
			}
			id, root := in["id"].(string), in["cwd"].(string)
			sess, _ := h.srv.sessions.Get(id)
			screenText := func() string { return strings.Join(sess.Screen().Lines, "\n") }
			info := func() map[string]any { _, x := h.do("GET", "/api/sessions/"+id, nil, nil); return x }
			t.Cleanup(func() { h.do("POST", "/api/sessions/"+id+"/kill", nil, nil) })
			ready := func(what string, limit time.Duration) bool {
				deadline := time.Now().Add(limit)
				for time.Now().Before(deadline) {
					x := info()
					if x["agentState"] == "ready" && x["busy"] == false {
						return true
					}
					if x["agentState"] == "needsYou" {
						t.Logf("%s: a question:\n%s", what, screenText())
						return false
					}
					time.Sleep(250 * time.Millisecond)
				}
				t.Logf("%s: not at its prompt; %v\n%s", what, info()["agentState"], screenText())
				return false
			}
			if !ready("start", 120*time.Second) {
				t.Fatalf("%s did not reach its prompt at the root (a trust dialog means the root is not trusted)", c.agent)
			}
			t.Logf("command line: %s", cliCmdline(c.agent, root))
			t.Logf("at its prompt:\n%s", screenText())

			for i, s := range c.steps {
				marker := fmt.Sprintf("NB-ROOT-STEP-%d", i)
				model.set(&scriptStep{marker: marker, tool: s.tool, input: s.input(root, web, docs)})
				if st, d := h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "please do " + marker}, nil); st != 202 {
					t.Fatalf("%s: deliver %d %v", s.name, st, d)
				}
				file := s.file(web, docs)
				outcome := ""
				deadline := time.Now().Add(90 * time.Second)
				for outcome == "" && time.Now().Before(deadline) {
					x := info()
					switch _, err := os.Stat(file); {
					case err == nil:
						outcome = "written without a question"
					case x["agentState"] == "needsYou":
						outcome = "asked"
					case model.toolResults() != "" && x["agentState"] == "ready" && x["busy"] == false:
						outcome = "refused: " + model.toolResults()
					}
					time.Sleep(250 * time.Millisecond)
				}
				if outcome == "" {
					outcome = "nothing within 90 s"
				}
				t.Logf("%s %s: %s", c.agent, s.name, outcome)
				if i == 0 {
					first := model.firstWith(marker, s.tool)
					t.Logf("in its first turn: the root's instruction file %v, the part's own file %v, the launch pointer %v; later in the conversation the part's own file %v",
						strings.Contains(first, "project root (made by NextBoard)"), strings.Contains(first, "WEB-PART-RULES"),
						strings.Contains(first, "You work at the root of project acme-shop"), strings.Contains(model.all(), "WEB-PART-RULES"))
				}
				if outcome == "asked" {
					t.Logf("screen:\n%s", screenText())
				}
				if got := outcome == "written without a question"; got != s.allowed {
					t.Errorf("%s %s: %s, want allowed=%v", c.agent, s.name, outcome, s.allowed)
				}
				if outcome == "asked" || outcome == "nothing within 90 s" {
					break // a question stays unanswered
				}
				if !ready("after "+s.name, 60*time.Second) {
					break
				}
				time.Sleep(time.Second) // the turn's last request is in
				model.mu.Lock()
				model.results = nil
				model.mu.Unlock()
			}
		})
	}
}

// cliCmdline is the command line of the CLI process working in dir.
func cliCmdline(cli, dir string) string {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		cwd, err := os.Readlink("/proc/" + e.Name() + "/cwd")
		if err != nil || cwd != dir {
			continue
		}
		b, _ := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		cl := strings.ReplaceAll(strings.TrimRight(string(b), "\x00"), "\x00", " ")
		if strings.Contains(cl, cli) && !strings.Contains(cl, "NB_WRAP") {
			return cl
		}
	}
	return "(not found)"
}

// outsideTmp is a new empty folder under /var/tmp, gone after the test.
func outsideTmp(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/var/tmp", "nb-root-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// TestRealRootAddDir is the control for TestRealRootConsole: the same root, Claude Code (-p,
// acceptEdits) and Codex (exec, workspace-write) asked to write through a part's link with and
// without --add-dir of the part's real folder. Without it both refuse, so the flag is what lets
// a root console change its parts.
//
//	NEXTBOARD_REAL_ROOT_TEST=1 go test ./internal/server -run TestRealRootAddDir -v
func TestRealRootAddDir(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_ROOT_TEST") != "1" {
		t.Skip("set NEXTBOARD_REAL_ROOT_TEST=1 (installed CLIs, a model on loopback, no account)")
	}
	home := t.TempDir()
	web := outsideTmp(t)
	root := filepath.Join(outsideTmp(t), "roots", "p_acme")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(web, filepath.Join(root, "Web")); err != nil {
		t.Fatal(err)
	}
	model := newScriptModel(t)
	run := func(env []string, name string, args ...string) string {
		t.Helper()
		c := exec.Command(name, args...)
		c.Dir, c.Stdin = root, nil
		c.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, env...)
		done := make(chan struct{})
		var out []byte
		go func() { out, _ = c.CombinedOutput(); close(done) }()
		select {
		case <-done:
		case <-time.After(90 * time.Second):
			_ = c.Process.Kill()
			<-done
			t.Fatalf("%s timed out:\n%s", name, out)
		}
		return string(out)
	}
	for _, c := range []struct {
		agent string
		tool  string
		input func(file string) map[string]any
		cmd   func(addDir bool, marker string) (env []string, args []string)
	}{
		{"claude", "Write", func(file string) map[string]any {
			return map[string]any{"file_path": filepath.Join(root, "Web", file), "content": "hi\n"}
		}, func(addDir bool, marker string) ([]string, []string) {
			args := []string{"-p", "please do " + marker, "--model", "fake-model", "--permission-mode", "acceptEdits", "--output-format", "text"}
			if addDir {
				args = append(args, "--add-dir", web)
			}
			return []string{"CLAUDE_CONFIG_DIR=" + filepath.Join(home, "claude"), "ANTHROPIC_API_KEY=not-a-real-key", "ANTHROPIC_BASE_URL=" + model.URL,
				"DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}, args
		}},
		{"codex", "exec_command", func(file string) map[string]any {
			return map[string]any{"cmd": "printf 'hi\\n' > Web/" + file}
		}, func(addDir bool, marker string) ([]string, []string) {
			args := []string{"exec", "--skip-git-repo-check", "--sandbox", "workspace-write", "-c", `model_provider="fake"`, "-c", `model="fake-model"`,
				"-c", `model_providers.fake={name="fake",base_url="` + model.URL + `/v1",wire_api="responses"}`,
				"-c", `projects={"` + root + `"={trust_level="trusted"}}`}
			if addDir {
				args = append(args, "--add-dir", web)
			}
			os.MkdirAll(filepath.Join(home, ".codex"), 0o700)
			return []string{"CODEX_HOME=" + filepath.Join(home, ".codex"), "OPENAI_API_KEY=not-a-real-key"}, append(args, "please do "+marker)
		}},
	} {
		t.Run(c.agent, func(t *testing.T) {
			if _, err := exec.LookPath(c.agent); err != nil {
				t.Skip("not installed")
			}
			for i, addDir := range []bool{false, true} {
				file := fmt.Sprintf("%s-%v.txt", c.agent, addDir)
				marker := fmt.Sprintf("NB-ADD-DIR-%d", i)
				model.set(&scriptStep{marker: marker, tool: c.tool, input: c.input(file)})
				model.mu.Lock()
				model.results = nil
				model.mu.Unlock()
				env, args := c.cmd(addDir, marker)
				out := run(env, c.agent, args...)
				_, err := os.Stat(filepath.Join(web, file))
				t.Logf("%s, --add-dir %v: written %v; tool result %s", c.agent, addDir, err == nil, model.toolResults())
				if (err == nil) != addDir {
					t.Errorf("%s --add-dir %v: written %v\n%s", c.agent, addDir, err == nil, out)
				}
			}
		})
	}
}
