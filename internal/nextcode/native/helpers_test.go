package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
	"github.com/azamrizki/nextboard/internal/nextcode/native/tools"
)

const testKey = "sk-test-SECRET-0123456789abcdef"

// waitFor bounds every wait of the tests.
const waitFor = 5 * time.Second

// ---- a scripted model

// step is one call of the fake model.
type step func(ctx context.Context, req llm.Request, on func(llm.Event)) (*llm.Response, error)

// fakeClient answers each call with the next step, and keeps the requests.
type fakeClient struct {
	mu     sync.Mutex
	steps  []step
	reqs   []llm.Request
	called chan int // the number of each call, as it starts
}

func newFake(steps ...step) *fakeClient {
	return &fakeClient{steps: steps, called: make(chan int, 64)}
}

func (f *fakeClient) Stream(ctx context.Context, req llm.Request, on func(llm.Event)) (*llm.Response, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	n := len(f.reqs)
	var s step
	if len(f.steps) > 0 {
		s, f.steps = f.steps[0], f.steps[1:]
	}
	f.mu.Unlock()
	f.called <- n
	if s == nil {
		return nil, errors.New("fake: no answer scripted")
	}
	return s(ctx, req, on)
}

func (f *fakeClient) requests() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Request(nil), f.reqs...)
}

func (f *fakeClient) request(t *testing.T, i int) llm.Request {
	t.Helper()
	reqs := f.requests()
	if i >= len(reqs) {
		t.Fatalf("call %d was not made (%d calls)", i+1, len(reqs))
	}
	return reqs[i]
}

var smallUsage = llm.Usage{Input: 10, Output: 5}

// answer streams blocks the way a client does, and returns them whole.
func answer(stop string, u llm.Usage, blocks ...llm.Block) step {
	return func(ctx context.Context, req llm.Request, on func(llm.Event)) (*llm.Response, error) {
		for i, b := range blocks {
			on(llm.Event{Kind: llm.EvBlockStart, Index: i, BlockType: b.Type, ToolID: b.ID, ToolName: b.Name})
			text := b.Text
			if b.Type == llm.BlockToolUse {
				text = string(b.Input)
			}
			if text != "" {
				on(llm.Event{Kind: llm.EvDelta, Index: i, Text: text})
			}
		}
		return &llm.Response{ID: "resp", Blocks: blocks, StopReason: stop, Usage: u}, nil
	}
}

func say(text string) step { return answer(llm.StopEnd, smallUsage, textBlock(text)) }

func callTool(id, name, input string) step {
	return answer(llm.StopToolUse, smallUsage, toolUse(id, name, input))
}

func textBlock(s string) llm.Block     { return llm.Block{Type: llm.BlockText, Text: s} }
func thinkingBlock(s string) llm.Block { return llm.Block{Type: llm.BlockThinking, Text: s} }
func toolUse(id, name, input string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

// hang streams some text, then waits for the call to be stopped.
func hang(partial string) step {
	return func(ctx context.Context, req llm.Request, on func(llm.Event)) (*llm.Response, error) {
		on(llm.Event{Kind: llm.EvBlockStart, Index: 0, BlockType: llm.BlockText})
		on(llm.Event{Kind: llm.EvDelta, Index: 0, Text: partial})
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

// gate waits for release before it takes the step.
func gate(release <-chan struct{}, s step) step {
	return func(ctx context.Context, req llm.Request, on func(llm.Event)) (*llm.Response, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s(ctx, req, on)
	}
}

func failWith(err error) step {
	return func(context.Context, llm.Request, func(llm.Event)) (*llm.Response, error) { return nil, err }
}

// ---- tools of the tests

// fakeTool records its calls; run says what it does (nil: "<name> ran").
type fakeTool struct {
	name, kind string
	run        func(ctx context.Context, env *tools.Env, input json.RawMessage) tools.Result

	mu    sync.Mutex
	calls []json.RawMessage
	envs  []*tools.Env
}

func (f *fakeTool) Def() llm.ToolDef {
	return llm.ToolDef{Name: f.name, Description: f.name + " of the tests", Schema: json.RawMessage(`{"type":"object"}`)}
}

func (f *fakeTool) Kind() string { return f.kind }

func (f *fakeTool) Run(ctx context.Context, env *tools.Env, input json.RawMessage) tools.Result {
	f.mu.Lock()
	f.calls = append(f.calls, input)
	f.envs = append(f.envs, env)
	f.mu.Unlock()
	if f.run != nil {
		return f.run(ctx, env, input)
	}
	return tools.Result{Output: f.name + " ran"}
}

func (f *fakeTool) ran() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]json.RawMessage(nil), f.calls...)
}

// kit is the tools of a test agent.
type kit struct {
	read, write, edit, bash, fetch, todo *fakeTool
	running                              chan string // Bash's "sleep": it has started
}

func newKit() *kit {
	k := &kit{running: make(chan string, 8)}
	k.read = &fakeTool{name: "Read", kind: tools.KindRead}
	k.write = &fakeTool{name: "Write", kind: tools.KindEdit}
	k.edit = &fakeTool{name: "Edit", kind: tools.KindEdit}
	k.fetch = &fakeTool{name: "WebFetch", kind: tools.KindFetch}
	k.todo = &fakeTool{name: "TodoWrite", kind: tools.KindMeta}
	k.bash = &fakeTool{name: "Bash", kind: tools.KindExec, run: func(ctx context.Context, env *tools.Env, input json.RawMessage) tools.Result {
		cmd := stringField(input, "command")
		switch cmd {
		case "sleep":
			k.running <- cmd
			<-ctx.Done()
			return tools.Result{Output: "slept a little", IsError: true}
		case "env":
			return tools.Result{Output: strings.Join(env.Environ, "\n")}
		case "panic":
			panic("the tool broke")
		}
		return tools.Result{Output: "Bash ran: " + cmd}
	}}
	return k
}

func (k *kit) all() []tools.Tool {
	return []tools.Tool{k.read, k.write, k.edit, k.bash, k.fetch, k.todo}
}

// testEditPath is tools.EditPath for the tests: file_path, made absolute.
func testEditPath(env *tools.Env, input json.RawMessage) string {
	p := stringField(input, "file_path")
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(env.Cwd, p)
	}
	return filepath.Clean(p)
}

// testReadOnly is tools.ReadOnlyCommand for the tests.
func testReadOnly(cmd string) bool {
	return cmd == "git status" || strings.HasPrefix(cmd, "ls") || strings.HasPrefix(cmd, "cat ")
}

// newConfig is an agent's launch in temp folders, with the tools of k.
func newConfig(t *testing.T, c llm.Client, k *kit) *config {
	t.Helper()
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	return &config{
		provider: llm.ProviderConfig{Kind: "anthropic", BaseURL: "http://127.0.0.1:9"},
		model:    "claude-test", mode: modeDefault, sessionID: "sess-1",
		cwd: cwd, stateDir: filepath.Join(root, "state"), homeDir: filepath.Join(root, "home"),
		environ: []string{"PATH=/usr/bin:/bin", "HOME=" + root}, secret: testKey,
		client: c, tools: k.all(), editPath: testEditPath, readOnly: testReadOnly,
	}
}

// ---- the agent through pipes

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// line is one output line: decoded, and as written.
type line struct {
	m   map[string]any
	raw []byte
}

// harness drives an agent as the driver does: lines in on stdin, lines out on stdout.
type harness struct {
	t      *testing.T
	in     *io.PipeWriter
	lines  chan line
	stdout *lockedBuf
	stderr *lockedBuf
	done   chan int
	once   sync.Once
	code   int
}

// startAgent runs the agent of cfg.
func startAgent(t *testing.T, cfg *config) *harness {
	t.Helper()
	return startWith(t, func(stdin io.Reader, stdout, stderr io.Writer) int {
		return run(context.Background(), cfg, stdin, stdout, stderr)
	})
}

// startWith runs an agent with fn, given its pipes.
func startWith(t *testing.T, fn func(stdin io.Reader, stdout, stderr io.Writer) int) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := &harness{t: t, in: inW, lines: make(chan line, 1024), stdout: &lockedBuf{}, stderr: &lockedBuf{}, done: make(chan int, 1)}
	go func() {
		code := fn(inR, outW, h.stderr)
		_ = outW.Close()
		_ = inR.Close()
		h.done <- code
	}()
	go func() {
		defer close(h.lines)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			raw := append([]byte(nil), sc.Bytes()...)
			_, _ = h.stdout.Write(append(append([]byte(nil), raw...), '\n'))
			var m map[string]any
			if json.Unmarshal(raw, &m) != nil {
				m = map[string]any{"type": "<not json>", "raw": string(raw)}
			}
			h.lines <- line{m: m, raw: raw}
		}
	}()
	t.Cleanup(func() { h.close() })
	return h
}

// close closes stdin and waits for the agent's exit code.
func (h *harness) close() int {
	h.once.Do(func() {
		_ = h.in.Close()
		select {
		case h.code = <-h.done:
		case <-time.After(2 * waitFor):
			h.t.Errorf("the agent did not exit after stdin closed")
			h.code = -1
		}
	})
	return h.code
}

func (h *harness) send(v any) {
	h.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.in.Write(append(b, '\n')); err != nil {
		h.t.Fatalf("write to the agent: %v", err)
	}
}

// next is the next output line; every line read is checked against claude.go's decode.
func (h *harness) next() map[string]any {
	h.t.Helper()
	select {
	case l, ok := <-h.lines:
		if !ok {
			h.t.Fatalf("the agent's output ended; stderr: %s", h.stderr.String())
		}
		checkDecodable(h.t, l.raw)
		return l.m
	case <-time.After(waitFor):
		h.t.Fatalf("no output line in time; stderr: %s", h.stderr.String())
	}
	return nil
}

// until reads lines up to the first one match takes; it returns that line and the ones before.
func (h *harness) until(match func(map[string]any) bool) (map[string]any, []map[string]any) {
	h.t.Helper()
	var before []map[string]any
	for {
		m := h.next()
		if match(m) {
			return m, before
		}
		before = append(before, m)
	}
}

func is(typ string, subtype ...string) func(map[string]any) bool {
	return func(m map[string]any) bool {
		if m["type"] != typ {
			return false
		}
		return len(subtype) == 0 || m["subtype"] == subtype[0]
	}
}

func (h *harness) initialize() {
	h.t.Helper()
	h.send(map[string]any{"type": "control_request", "request_id": "nb_1", "request": map[string]any{"subtype": "initialize", "hooks": nil}})
	h.until(is("system", "init"))
}

// say sends the owner's message, as the driver does.
func (h *harness) say(text string) {
	h.t.Helper()
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}},
		"parent_tool_use_id": nil, "session_id": ""})
}

func (h *harness) control(id string, req map[string]any) {
	h.t.Helper()
	h.send(map[string]any{"type": "control_request", "request_id": id, "request": req})
}

// request waits for the agent's next permission request.
func (h *harness) request() (map[string]any, []map[string]any) {
	h.t.Helper()
	m, before := h.until(is("control_request"))
	return m, before
}

// result waits for the end of the turn.
func (h *harness) result() (map[string]any, []map[string]any) {
	h.t.Helper()
	return h.until(is("result"))
}

func (h *harness) allow(id string, input any, perms any) {
	h.t.Helper()
	resp := map[string]any{"behavior": "allow", "updatedInput": input}
	if perms != nil {
		resp["updatedPermissions"] = perms
	}
	h.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": resp}})
}

func (h *harness) deny(id, msg string, interrupt bool) {
	h.t.Helper()
	resp := map[string]any{"behavior": "deny", "message": msg}
	if interrupt {
		resp["interrupt"] = true
	}
	h.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": resp}})
}

// toolResults are the tool_result blocks of user lines.
func toolResults(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["type"] != "user" {
			continue
		}
		msg, _ := l["message"].(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_result" {
				out = append(out, bm)
			}
		}
	}
	return out
}

// field digs a value out of decoded JSON: keys of objects, or indexes of arrays as "0", "1".
func field(v any, path ...string) any {
	for _, p := range path {
		switch x := v.(type) {
		case map[string]any:
			v = x[p]
		case []any:
			i := 0
			for _, c := range p {
				i = i*10 + int(c-'0')
			}
			if i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

func str(v any, path ...string) string {
	s, _ := field(v, path...).(string)
	return s
}

// loadConv reads a test agent's conversation file.
func loadConv(t *testing.T, cfg *config, id string) *conversation {
	t.Helper()
	c, err := store{dir: cfg.stateDir}.load(id)
	if err != nil {
		t.Fatalf("load the conversation: %v", err)
	}
	return c
}

// lastMessage is the last message of a request.
func lastMessage(req llm.Request) llm.Message {
	if len(req.Messages) == 0 {
		return llm.Message{}
	}
	return req.Messages[len(req.Messages)-1]
}

// texts are a message's text blocks.
func texts(m llm.Message) []string {
	var out []string
	for _, b := range m.Blocks {
		if b.Type == llm.BlockText {
			out = append(out, b.Text)
		}
	}
	return out
}

// checkHistory fails when a tool_use of the conversation has no tool_result right after it.
func checkHistory(t *testing.T, msgs []llm.Message) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != llm.RoleAssistant {
			continue
		}
		var ids []string
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse {
				ids = append(ids, b.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		if i+1 >= len(msgs) {
			t.Fatalf("message %d: tool calls %v have no results", i, ids)
		}
		got := map[string]bool{}
		for _, b := range msgs[i+1].Blocks {
			if b.Type == llm.BlockToolResult {
				got[b.ToolUseID] = true
			}
		}
		for _, id := range ids {
			if !got[id] {
				t.Fatalf("message %d: tool call %s has no result in the next message", i, id)
			}
		}
	}
}
