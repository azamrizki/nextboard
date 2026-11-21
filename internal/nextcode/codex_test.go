package nextcode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCodex plays `codex app-server` for the tests (the test binary runs itself with
// NEXTCODE_FAKE=codex): JSON-RPC lines without "jsonrpc", in the shapes of Codex 0.165.0's
// generated schema, a few scripted turns chosen by the message's text. It is strict where the
// engine could go wrong: nothing before initialize and initialized, no turn/start before its
// thread exists or while a turn runs (Codex would steer the running turn with it), and an answer
// counts only with the request's id in the same JSON type. It writes every line it reads to
// NEXTCODE_FAKE_LOG ("<pid> <line>"); NEXTCODE_FAKE_SLOW delays the thread/start response.
func fakeCodex() {
	args := os.Args[1:]
	if f := os.Getenv("NEXTCODE_FAKE_ARGS"); f != "" {
		if fh, err := os.OpenFile(f, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			fmt.Fprintln(fh, strings.Join(args, " "))
			fh.Close()
		}
	}
	if len(args) == 0 || args[0] != "app-server" {
		fmt.Fprintln(os.Stderr, "fake codex: not app-server:", args)
		os.Exit(2)
	}
	pid := os.Getpid()
	model := "fake-codex"
	for i := 1; i+1 < len(args); i++ {
		if v, ok := strings.CutPrefix(args[i+1], "model="); ok && args[i] == "-c" {
			_ = json.Unmarshal([]byte(v), &model)
		}
	}
	out := bufio.NewWriter(os.Stdout)
	var omu sync.Mutex
	emit := func(v any) {
		b, _ := json.Marshal(v)
		omu.Lock()
		out.Write(append(b, '\n'))
		out.Flush()
		omu.Unlock()
	}
	in := make(chan map[string]any, 64)
	go func() {
		var lf *os.File
		if p := os.Getenv("NEXTCODE_FAKE_LOG"); p != "" {
			lf, _ = os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		}
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			if lf != nil {
				fmt.Fprintf(lf, "%d %s\n", pid, sc.Bytes())
			}
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				in <- m
			}
		}
		close(in)
	}()
	reply := func(id, result any) { emit(map[string]any{"id": id, "result": result}) }
	fail := func(id any, msg string) {
		emit(map[string]any{"id": id, "error": map[string]any{"code": -32600, "message": msg}})
	}
	notify := func(method string, params map[string]any) { emit(map[string]any{"method": method, "params": params}) }
	sameID := func(a, b any) bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return bytes.Equal(x, y)
	}
	// stray answers a request that comes while a turn runs
	stray := func(m map[string]any) {
		if method, _ := m["method"].(string); method != "" && m["id"] != nil {
			fail(m["id"], "fake codex: "+method+" while a turn runs")
		}
	}
	// ask sends a server request and waits for its answer
	ask := func(id any, method string, params map[string]any) map[string]any {
		emit(map[string]any{"id": id, "method": method, "params": params})
		for m := range in {
			if m["method"] == nil && sameID(m["id"], id) {
				return m
			}
			stray(m)
		}
		os.Exit(0)
		return nil
	}
	sandbox := map[string]any{"type": "workspaceWrite", "writableRoots": []any{"/w"}, "networkAccess": false,
		"excludeTmpdirEnvVar": false, "excludeSlashTmp": false}
	threadObj := func(id string) map[string]any {
		return map[string]any{"id": id, "sessionId": id, "projectId": nil, "preview": "", "ephemeral": false, "modelProvider": "openai",
			"model": model, "createdAt": 0, "updatedAt": 0, "status": map[string]any{"type": "idle"}, "path": nil, "cwd": "/w",
			"cliVersion": "0.165.0", "source": "appServer", "turns": []any{}}
	}
	threadResp := func(id string, p map[string]any) map[string]any {
		return map[string]any{"thread": threadObj(id), "model": model, "modelProvider": "openai", "cwd": "/w",
			"approvalPolicy": p["approvalPolicy"], "approvalsReviewer": "user", "sandbox": sandbox, "reasoningEffort": "high",
			"instructionSources": []any{}, "disabledPluginIds": []any{}}
	}

	inited, initialized := false, false
	thread := ""
	n, sreq := 0, 0
	turnRun := func(turn, said string) {
		item := func(started bool, it map[string]any) {
			if started {
				notify("item/started", map[string]any{"threadId": thread, "turnId": turn, "item": it, "startedAtMs": 0})
				return
			}
			notify("item/completed", map[string]any{"threadId": thread, "turnId": turn, "item": it, "completedAtMs": 0})
		}
		delta := func(id, s string) {
			notify("item/agentMessage/delta", map[string]any{"threadId": thread, "turnId": turn, "itemId": id, "delta": s})
		}
		done := func(status, errText string) {
			t := map[string]any{"id": turn, "status": status, "items": []any{}, "error": nil}
			if errText != "" {
				t["error"] = map[string]any{"message": errText, "codexErrorInfo": nil}
			}
			notify("turn/completed", map[string]any{"threadId": thread, "turn": t})
		}
		say := func(s string) {
			id := fmt.Sprintf("msg_%d_b", n)
			item(true, map[string]any{"type": "agentMessage", "id": id, "text": ""})
			delta(id, s)
			item(false, map[string]any{"type": "agentMessage", "id": id, "text": s})
		}
		resolved := func(id any) { notify("serverRequest/resolved", map[string]any{"threadId": thread, "requestId": id}) }
		switch said {
		case "hello":
			// a sub-agent's thread: not the chat's
			notify("thread/started", map[string]any{"thread": map[string]any{"id": "thr_child", "parentThreadId": thread}})
			notify("turn/completed", map[string]any{"threadId": "thr_child", "turn": map[string]any{"id": "turn_c", "status": "completed", "items": []any{}}})
			rs := fmt.Sprintf("rs_%d", n)
			item(true, map[string]any{"type": "reasoning", "id": rs, "summary": []any{}, "content": []any{}})
			notify("item/reasoning/summaryPartAdded", map[string]any{"threadId": thread, "turnId": turn, "itemId": rs, "summaryIndex": 0})
			notify("item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": turn, "itemId": rs, "summaryIndex": 0, "delta": "Greeting"})
			item(false, map[string]any{"type": "reasoning", "id": rs, "summary": []any{"Greeting"}, "content": []any{}})
			msg := fmt.Sprintf("msg_%d", n)
			item(true, map[string]any{"type": "agentMessage", "id": msg, "text": ""})
			delta(msg, "Hi")
			delta(msg, " there")
			item(false, map[string]any{"type": "agentMessage", "id": msg, "text": "Hi there"})
			usage := map[string]any{"inputTokens": 100, "cachedInputTokens": 40, "cacheWriteInputTokens": 0, "outputTokens": 5,
				"reasoningOutputTokens": 0, "totalTokens": 105}
			notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn,
				"tokenUsage": map[string]any{"last": usage, "total": usage, "modelContextWindow": 1000}})
			notify("account/rateLimits/updated", map[string]any{"rateLimits": map[string]any{"limitId": "codex", "planType": "plus",
				"primary": map[string]any{"usedPercent": 12, "resetsAt": 1795204062, "windowDurationMins": 300}, "secondary": nil}})
			done("completed", "")
		case "run", "rule":
			call := fmt.Sprintf("call_%d", n)
			item(true, map[string]any{"type": "commandExecution", "id": call, "command": "ls", "cwd": "/w", "status": "inProgress",
				"commandActions": []any{}})
			decisions := []any{"accept", "acceptForSession", "decline", "cancel"}
			if said == "rule" {
				decisions = []any{"accept", map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{"ls"}}}, "cancel"}
			}
			sreq++
			rid := sreq
			resp := ask(rid, codexCommandApproval, map[string]any{"threadId": thread, "turnId": turn, "itemId": call, "command": "ls",
				"cwd": "/w", "reason": "May I list the folder?", "availableDecisions": decisions, "commandActions": []any{}, "startedAtMs": 0})
			resolved(rid)
			res, _ := resp["result"].(map[string]any)
			dec := res["decision"]
			if dec == "decline" || dec == "cancel" {
				item(false, map[string]any{"type": "commandExecution", "id": call, "command": "ls", "cwd": "/w", "status": "declined",
					"commandActions": []any{}, "aggregatedOutput": nil, "exitCode": nil})
				if dec == "cancel" {
					done("interrupted", "")
					return
				}
				say("declined")
				done("completed", "")
				return
			}
			b, _ := json.Marshal(dec)
			item(false, map[string]any{"type": "commandExecution", "id": call, "command": "ls", "cwd": "/w", "status": "completed",
				"commandActions": []any{}, "aggregatedOutput": "a\nb " + string(b), "exitCode": 0})
			say("done")
			done("completed", "")
		case "edit":
			call := fmt.Sprintf("call_%d", n)
			changes := []any{map[string]any{"path": "/w/a.txt", "kind": map[string]any{"type": "add"}, "diff": "+hi\n"}}
			item(true, map[string]any{"type": "fileChange", "id": call, "changes": changes, "status": "inProgress"})
			sreq++
			rid := sreq
			resp := ask(rid, codexFileApproval, map[string]any{"threadId": thread, "turnId": turn, "itemId": call,
				"reason": "Write a.txt?", "grantRoot": nil, "startedAtMs": 0})
			resolved(rid)
			res, _ := resp["result"].(map[string]any)
			status := "completed"
			if res["decision"] != "accept" && res["decision"] != "acceptForSession" {
				status = "declined"
			}
			item(false, map[string]any{"type": "fileChange", "id": call, "changes": changes, "status": status})
			say("edited")
			done("completed", "")
		case "ask":
			resp := ask("q-1", codexUserInput, map[string]any{"threadId": thread, "turnId": turn, "itemId": "call_q", "isBlocking": true,
				"autoResolutionMs": nil, "questions": []any{map[string]any{"id": "colour", "header": "Colour", "question": "Colour?",
					"isOther": true, "isSecret": false, "options": []any{map[string]any{"label": "Red", "description": "red"},
						map[string]any{"label": "Blue", "description": "blue"}}}}})
			resolved("q-1")
			res, _ := resp["result"].(map[string]any)
			b, _ := json.Marshal(res["answers"])
			say("You chose " + string(b))
			done("completed", "")
		case "long":
			msg := fmt.Sprintf("msg_%d", n)
			item(true, map[string]any{"type": "agentMessage", "id": msg, "text": ""})
			tick := time.NewTicker(20 * time.Millisecond)
		loop:
			for {
				select {
				case c, ok := <-in:
					if !ok {
						os.Exit(0)
					}
					if c["method"] == "turn/interrupt" {
						p, _ := c["params"].(map[string]any)
						if p["turnId"] != turn || p["threadId"] != thread {
							fail(c["id"], "fake codex: no such turn")
							continue
						}
						reply(c["id"], map[string]any{})
						break loop
					}
					stray(c)
				case <-tick.C:
					delta(msg, "tick ")
				}
			}
			tick.Stop()
			item(false, map[string]any{"type": "agentMessage", "id": msg, "text": "tick tick"})
			done("interrupted", "")
		case "fail":
			notify("error", map[string]any{"threadId": thread, "turnId": turn, "willRetry": false,
				"error": map[string]any{"message": "boom", "codexErrorInfo": nil}})
			done("failed", "The model failed: boom")
		case "tool":
			sreq++
			resp := ask(sreq, "item/tool/call", map[string]any{"threadId": thread, "turnId": turn, "callId": "call_t", "tool": "x",
				"arguments": map[string]any{}})
			say(fmt.Sprintf("tool call answered with an error: %v", resp["error"] != nil))
			done("completed", "")
		default:
			say("echo " + said)
			done("completed", "")
		}
	}
	for m := range in {
		method, _ := m["method"].(string)
		id := m["id"]
		params, _ := m["params"].(map[string]any)
		if method == "" {
			continue
		}
		if id != nil && method != "initialize" && !initialized {
			fail(id, "Not initialized")
			continue
		}
		switch method {
		case "initialize":
			inited = true
			reply(id, map[string]any{"userAgent": "fake", "codexHome": "/x", "platformFamily": "unix", "platformOs": "linux"})
		case "initialized":
			if !inited {
				fmt.Fprintln(os.Stderr, "fake codex: initialized before initialize")
				os.Exit(4)
			}
			initialized = true
		case "thread/start":
			if d, _ := time.ParseDuration(os.Getenv("NEXTCODE_FAKE_SLOW")); d > 0 {
				time.Sleep(d)
			}
			thread = fmt.Sprintf("thr_%d", pid)
			notify("thread/started", map[string]any{"thread": threadObj(thread)})
			reply(id, threadResp(thread, params))
		case "thread/resume":
			tid, _ := params["threadId"].(string)
			if !strings.HasPrefix(tid, "thr_") {
				fail(id, "no rollout found for thread id "+tid)
				continue
			}
			thread = tid
			reply(id, threadResp(thread, params))
		case "turn/start":
			if tid, _ := params["threadId"].(string); thread == "" || tid != thread {
				fail(id, "thread not found: "+tid)
				continue
			}
			n++
			turn := fmt.Sprintf("turn_%d", n)
			reply(id, map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress", "items": []any{}, "error": nil}})
			notify("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "inProgress", "items": []any{}}})
			said := ""
			if input, _ := params["input"].([]any); len(input) > 0 {
				c, _ := input[0].(map[string]any)
				said, _ = c["text"].(string)
			}
			turnRun(turn, said)
		case "turn/interrupt":
			reply(id, map[string]any{}) // nothing runs
		default:
			if id != nil {
				fail(id, "fake codex: unknown method "+method)
			}
		}
	}
}

// codexBase is a Codex agent's own launch, as agent.Headless gives it.
func codexBase(resume bool) []string {
	b := []string{os.Args[0], "-c", `mcp_servers.nextboard.url="http://hub"`, "--add-dir", "/w2"}
	if resume {
		b = append(b, "resume", "thr_launch")
	}
	return append(b, "-m", "gpt-test", "-c", "model_reasoning_effort=high")
}

func newCodexRig(t *testing.T, dir string, idle time.Duration, mode string, env ...string) *rig {
	t.Helper()
	return newRigWith(t, dir, idle, rigOpts{engine: EngineCodex, base: codexBase, mode: mode, env: env})
}

// sentLine is a line the fake Codex read.
type sentLine struct {
	pid    string
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params map[string]any  `json:"params"`
	Result map[string]any  `json:"result"`
	Error  map[string]any  `json:"error"`
}

// sent is what the fake Codex read, in order; with methods, only those.
func (r *rig) sent(methods ...string) []sentLine {
	r.t.Helper()
	b, _ := os.ReadFile(r.log)
	var out []sentLine
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		pid, js, ok := strings.Cut(l, " ")
		if !ok {
			continue
		}
		var s sentLine
		if err := json.Unmarshal([]byte(js), &s); err != nil {
			r.t.Fatalf("the engine wrote %q: %v", js, err)
		}
		s.pid = pid
		if len(methods) == 0 || slices.Contains(methods, s.Method) {
			out = append(out, s)
		}
	}
	return out
}

// answerTo is the engine's answer to the fake's request id (as JSON).
func (r *rig) answerTo(id string) sentLine {
	r.t.Helper()
	for _, s := range r.sent() {
		if s.Method == "" && string(s.ID) == id {
			return s
		}
	}
	r.t.Fatalf("no answer to %s in %+v", id, r.sent())
	return sentLine{}
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func noErrors(t *testing.T, r *rig) {
	t.Helper()
	for _, f := range r.frames {
		if f.T == FrameItem && f.Item.Kind == KindError {
			t.Fatalf("error item %+v", f.Item)
		}
	}
}

func TestCodexArgv(t *testing.T) {
	e := newCodexEngine()
	got := e.argv([]string{"codex", "-c", "a=1", "--add-dir", "/w2", "resume", "thr_x", "--enable", "f", "-m", "gpt-6.1-sol",
		"-c", "model_reasoning_effort=high", "--dangerously-bypass-approvals-and-sandbox", "-s", "read-only", "a prompt", "--add-dir", "/w3"}, "plan")
	want := []string{"codex", "app-server", "-c", "a=1", "--enable", "f", "-c", `model="gpt-6.1-sol"`, "-c", "model_reasoning_effort=high"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
	if !slices.Equal(e.addDirs, []string{"/w2", "/w3"}) || e.launchModel != "gpt-6.1-sol" || e.mode != "plan" {
		t.Fatalf("engine %+v", e)
	}
	// initialize first; initialized and the thread once Codex answers it
	var init map[string]any
	_ = json.Unmarshal(e.start(false, ""), &init)
	p, _ := init["params"].(map[string]any)
	if init["method"] != "initialize" || init["id"] != 1.0 || asJSON(p["clientInfo"]) != `{"name":"nextboard","title":"NextBoard","version":"1"}` ||
		asJSON(p["capabilities"]) != `{"experimentalApi":true}` {
		t.Fatalf("initialize %v", init)
	}
	evs := e.decode([]byte(`{"id":1,"result":{"userAgent":"x"}}`))
	if len(evs) != 1 || evs[0].kind != evReply {
		t.Fatalf("after initialize %+v", evs)
	}
	lines := strings.Split(strings.TrimSpace(string(evs[0].reply)), "\n")
	if len(lines) != 2 || lines[0] != `{"method":"initialized"}` ||
		lines[1] != `{"id":2,"method":"thread/start","params":{"approvalPolicy":"on-request","approvalsReviewer":"user","sandbox":"read-only"}}` {
		t.Fatalf("after initialize %q", lines)
	}
}

func TestCodexModeMapping(t *testing.T) {
	for _, c := range []struct{ mode, approval, sandbox, reviewer string }{
		{"default", "on-request", "workspace-write", "user"},
		{"acceptEdits", "on-request", "workspace-write", "user"},
		{"plan", "on-request", "read-only", "user"},
		{"auto", "on-request", "workspace-write", "auto_review"},
		{"bypassPermissions", "never", "danger-full-access", "user"},
		{"", "", "", ""},
	} {
		a, s, r := codexMode(c.mode)
		if a != c.approval || s != c.sandbox || r != c.reviewer {
			t.Fatalf("%q: %s %s %s", c.mode, a, s, r)
		}
	}
}

func TestCodexStreamedTurnAndHistory(t *testing.T) {
	dir := t.TempDir()
	r := newCodexRig(t, dir, 0, "default")
	if err := r.d.Send("hello"); err != nil {
		t.Fatal(err)
	}
	if u := r.item(KindUser); u.Text != "hello" {
		t.Fatalf("user item %+v", u)
	}
	if th := r.item(KindThinking); th.Text != "Greeting" || th.ID != "rs_1" || th.Stream != "rs_1" {
		t.Fatalf("thinking %+v", th)
	}
	blk := r.until("text block", func(f Frame) bool { return f.T == FrameBlock && f.Kind == KindText })
	if blk.ID != "msg_1" {
		t.Fatalf("block %+v", blk)
	}
	var streamed string
	r.until("deltas", func(f Frame) bool {
		if f.T == FrameDelta && f.ID == "msg_1" {
			streamed += f.Text
		}
		return streamed == "Hi there"
	})
	txt := r.item(KindText)
	if txt.Text != "Hi there" || txt.Stream != "msg_1" || txt.ID != "msg_1" {
		t.Fatalf("text item %+v", txt)
	}
	r.turnEnded()
	st := r.d.Status()
	if st.Engine != EngineCodex || st.Model != "gpt-test" || !strings.HasPrefix(st.Conversation, "thr_") || st.Conversation == "thr_child" ||
		st.Usage == nil || *st.Usage != (Usage{Input: 60, Output: 5, CacheRead: 40}) || !strings.Contains(string(st.RateLimit), `"usedPercent":12`) {
		t.Fatalf("status %+v", st)
	}
	if got := strings.Join(r.called(), "|"); got != "start|conv "+st.Conversation+"|end " {
		t.Fatalf("listener %q", got)
	}
	// the launch's config, as app-server's
	b, _ := os.ReadFile(r.args)
	if got := strings.TrimSpace(string(b)); got != `app-server -c mcp_servers.nextboard.url="http://hub" -c model="gpt-test" -c model_reasoning_effort=high` {
		t.Fatalf("args %q", got)
	}
	var methods []string
	for _, s := range r.sent() {
		methods = append(methods, s.Method)
	}
	if got := strings.Join(methods, " "); got != "initialize initialized thread/start turn/start" {
		t.Fatalf("sent %q", got)
	}
	ts := r.sent("thread/start")[0].Params
	if asJSON(ts) != `{"approvalPolicy":"on-request","approvalsReviewer":"user","sandbox":"workspace-write"}` {
		t.Fatalf("thread/start %v", ts)
	}
	// the thread's sandbox (its writable root /w) with the launch's --add-dir
	turn := r.sent("turn/start")[0].Params
	if turn["threadId"] != st.Conversation || asJSON(turn["input"]) != `[{"text":"hello","type":"text"}]` ||
		turn["approvalPolicy"] != "on-request" || turn["model"] != nil ||
		asJSON(turn["sandboxPolicy"]) != `{"excludeSlashTmp":false,"excludeTmpdirEnvVar":false,"networkAccess":false,"type":"workspaceWrite","writableRoots":["/w","/w2"]}` {
		t.Fatalf("turn/start %v", turn)
	}
	// the chat comes back after a restart
	r.d.Close()
	d2, err := New(Config{Engine: EngineCodex, HistoryPath: dir + "/chat.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	items, _ := d2.Items("", 0)
	if len(items) != 3 || items[0].Kind != KindUser || items[1].Kind != KindThinking || items[2].Text != "Hi there" || !d2.Started() ||
		d2.Status().Conversation != st.Conversation {
		t.Fatalf("history %+v started %v", items, d2.Started())
	}
}

func TestCodexPermissions(t *testing.T) {
	r := newCodexRig(t, t.TempDir(), 0, "default")
	request := func() *Request {
		return r.until("request", func(f Frame) bool { return f.T == FrameRequest }).Request
	}
	toolEnd := func(id string) *Item {
		return r.until("tool "+id, func(f Frame) bool {
			return f.T == FrameItem && f.Item.ID == id && f.Item.Tool != nil && f.Item.Tool.Status != ToolRunning
		}).Item
	}

	// allow: accept
	_ = r.d.Send("run")
	req := request()
	if req.Kind != ReqPermission || req.Tool != "Bash" || req.ID != "1" || req.ToolUseID != "call_1" || !req.CanRemember ||
		req.Reason != "May I list the folder?" || string(req.Input) != `{"command":"ls","cwd":"/w"}` {
		t.Fatalf("request %+v", req)
	}
	if err := r.d.Answer(req.ID, Answer{Decision: Allow}); err != nil {
		t.Fatal(err)
	}
	if it := toolEnd("call_1"); it.Tool.Status != ToolDone || it.Tool.Output != `a`+"\n"+`b "accept"` || it.Tool.Name != "Bash" {
		t.Fatalf("tool %+v", it.Tool)
	}
	r.turnEnded()
	if a := r.answerTo("1"); asJSON(a.Result) != `{"decision":"accept"}` {
		t.Fatalf("answer %+v", a)
	}

	// allowAlways: acceptForSession
	_ = r.d.Send("run")
	req = request()
	_ = r.d.Answer(req.ID, Answer{Decision: AllowAlways})
	if it := toolEnd("call_2"); it.Tool.Output != "a\nb \"acceptForSession\"" {
		t.Fatalf("tool %+v", it.Tool)
	}
	r.turnEnded()

	// allowAlways where Codex offers a rule instead (as 0.165 does for commands)
	_ = r.d.Send("rule")
	req = request()
	if !req.CanRemember {
		t.Fatalf("request %+v", req)
	}
	_ = r.d.Answer(req.ID, Answer{Decision: AllowAlways})
	if it := toolEnd("call_3"); !strings.Contains(it.Tool.Output, `{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["ls"]}}`) {
		t.Fatalf("tool %+v", it.Tool)
	}
	r.turnEnded()

	// deny: decline, the turn goes on
	_ = r.d.Send("run")
	req = request()
	_ = r.d.Answer(req.ID, Answer{Decision: Deny, Message: "not now"})
	if it := toolEnd("call_4"); it.Tool.Status != ToolDenied {
		t.Fatalf("tool %+v", it.Tool)
	}
	if txt := r.item(KindText); txt.Text != "declined" {
		t.Fatalf("reply %+v", txt)
	}
	r.turnEnded()
	if a := r.answerTo(req.ID); asJSON(a.Result) != `{"decision":"decline"}` {
		t.Fatalf("answer %+v", a)
	}

	// denyStop: cancel, the turn stops
	_ = r.d.Send("run")
	req = request()
	_ = r.d.Answer(req.ID, Answer{Decision: DenyStop})
	if it := toolEnd("call_5"); it.Tool.Status != ToolDenied {
		t.Fatalf("tool %+v", it.Tool)
	}
	if n := r.item(KindNotice); n.Text != "Stopped." {
		t.Fatalf("notice %+v", n)
	}
	r.turnEnded()
	if a := r.answerTo(req.ID); asJSON(a.Result) != `{"decision":"cancel"}` {
		t.Fatalf("answer %+v", a)
	}

	// a file change: Patch, with the changes of its item
	_ = r.d.Send("edit")
	req = request()
	if req.Tool != "Patch" || req.ToolUseID != "call_6" || !req.CanRemember ||
		string(req.Input) != `{"changes":[{"diff":"+hi\n","kind":"add","path":"/w/a.txt"}]}` {
		t.Fatalf("request %+v", req)
	}
	_ = r.d.Answer(req.ID, Answer{Decision: Allow})
	if it := toolEnd("call_6"); it.Tool.Status != ToolDone || it.Tool.Output != "Added /w/a.txt" {
		t.Fatalf("tool %+v", it.Tool)
	}
	r.turnEnded()
	noErrors(t, r)
	calls := strings.Join(r.called(), "|")
	if !strings.Contains(calls, "tool Bash ls|needs permission Bash(ls)|answered") || !strings.Contains(calls, "needs permission Patch(/w/a.txt)") {
		t.Fatalf("listener %q", calls)
	}
}

func TestCodexQuestion(t *testing.T) {
	r := newCodexRig(t, t.TempDir(), 0, "default")
	_ = r.d.Send("ask")
	req := r.until("question", func(f Frame) bool { return f.T == FrameRequest }).Request
	if req.Kind != ReqQuestion || req.Tool != "AskUserQuestion" || req.ID != "q-1" || req.ToolUseID != "call_q" ||
		string(req.Input) != `{"questions":[{"header":"Colour","multiSelect":false,"options":[{"description":"red","label":"Red"},{"description":"blue","label":"Blue"}],"question":"Colour?"}]}` {
		t.Fatalf("request %+v", req)
	}
	_ = r.d.Answer(req.ID, Answer{Decision: Allow, Answers: map[string][]string{"Colour?": {"Blue"}}})
	if txt := r.item(KindText); txt.Text != `You chose {"colour":{"answers":["Blue"]}}` {
		t.Fatalf("reply %q", txt.Text)
	}
	r.turnEnded()
	// the string id is echoed as a string
	if a := r.answerTo(`"q-1"`); a.Result == nil {
		t.Fatalf("answer %+v", a)
	}
	// a free reply alone answers the first question
	_ = r.d.Send("ask")
	req = r.until("question", func(f Frame) bool { return f.T == FrameRequest }).Request
	_ = r.d.Answer(req.ID, Answer{Decision: Allow, Message: "Green"})
	if txt := r.item(KindText); txt.Text != `You chose {"colour":{"answers":["Green"]}}` {
		t.Fatalf("reply %q", txt.Text)
	}
	r.turnEnded()
	if calls := strings.Join(r.called(), "|"); !strings.Contains(calls, "needs question Question") {
		t.Fatalf("listener %q", calls)
	}
}

func TestCodexInterrupt(t *testing.T) {
	r := newCodexRig(t, t.TempDir(), 0, "default")
	if err := r.d.Interrupt(); !errors.Is(err, ErrNotWorking) {
		t.Fatalf("idle interrupt: %v", err)
	}
	_ = r.d.Send("long")
	r.until("a delta", func(f Frame) bool { return f.T == FrameDelta })
	if err := r.d.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if n := r.item(KindNotice); n.Text != "Stopped." {
		t.Fatalf("notice %+v", n)
	}
	r.turnEnded()
	noErrors(t, r)
	if calls := r.called(); calls[len(calls)-1] != "end " {
		t.Fatalf("listener %q", calls)
	}
	in := r.sent("turn/interrupt")
	if len(in) != 1 || in[0].Params["turnId"] != "turn_1" || in[0].Params["threadId"] != r.d.Status().Conversation {
		t.Fatalf("interrupt %+v", in)
	}
}

func TestCodexFailedTurn(t *testing.T) {
	r := newCodexRig(t, t.TempDir(), 0, "default")
	_ = r.d.Send("fail")
	if e := r.item(KindError); e.Text != "The model failed: boom" {
		t.Fatalf("error %+v", e)
	}
	r.turnEnded()
	if calls := strings.Join(r.called(), "|"); !strings.Contains(calls, "end The model failed: boom") {
		t.Fatalf("listener %q", calls)
	}
	if st := r.d.Status(); st.Note != "" || st.Working {
		t.Fatalf("status %+v", st)
	}
	// the thread goes on
	_ = r.d.Send("again")
	if txt := r.item(KindText); txt.Text != "echo again" {
		t.Fatalf("reply %+v", txt)
	}
}

func TestCodexQueuedMessages(t *testing.T) {
	// the thread opens late: both messages wait, then go one turn after the other
	r := newCodexRig(t, t.TempDir(), 0, "default", "NEXTCODE_FAKE_SLOW=200ms")
	if err := r.d.Send("first"); err != nil {
		t.Fatal(err)
	}
	if err := r.d.Send("second"); err != nil {
		t.Fatal(err)
	}
	if txt := r.item(KindText); txt.Text != "echo first" {
		t.Fatalf("reply %+v", txt)
	}
	if txt := r.item(KindText); txt.Text != "echo second" {
		t.Fatalf("reply %+v", txt)
	}
	r.turnEnded()
	noErrors(t, r)
	var said []string
	for _, s := range r.sent("turn/start") {
		in, _ := s.Params["input"].([]any)
		said = append(said, asJSON(in))
	}
	if len(said) != 2 || !strings.Contains(said[0], `"first"`) || !strings.Contains(said[1], `"second"`) {
		t.Fatalf("turn/start %q", said)
	}
	if calls := strings.Join(r.called(), "|"); strings.Count(calls, "end ") != 2 {
		t.Fatalf("listener %q", calls)
	}
}

func TestCodexIdleExitAndResume(t *testing.T) {
	r := newCodexRig(t, t.TempDir(), 150*time.Millisecond, "default")
	_ = r.d.Send("hello")
	r.turnEnded()
	conv := r.d.Status().Conversation
	r.until("idle exit", func(f Frame) bool { return f.T == FrameStatus && f.Status.Process == ProcStopped })
	noErrors(t, r)
	_ = r.d.Send("again")
	if txt := r.item(KindText); txt.Text != "echo again" {
		t.Fatalf("reply %+v", txt)
	}
	r.turnEnded()
	if st := r.d.Status(); st.Conversation != conv {
		t.Fatalf("conversation %q, was %q", st.Conversation, conv)
	}
	r.mu.Lock()
	starts := append([]bool{}, r.starts...)
	r.mu.Unlock()
	if !slices.Equal(starts, []bool{false, true}) {
		t.Fatalf("starts (resume?) %v", starts)
	}
	// the second CLI resumes the thread by its id (the launch's resume subcommand is not app-server's)
	b, _ := os.ReadFile(r.args)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || lines[0] != lines[1] || strings.Contains(lines[1], "resume") {
		t.Fatalf("CLI starts %q", lines)
	}
	threads := r.sent("thread/start", "thread/resume")
	if len(threads) != 2 || threads[0].Method != "thread/start" || threads[1].Method != "thread/resume" || threads[0].pid == threads[1].pid ||
		threads[1].Params["threadId"] != conv || threads[1].Params["excludeTurns"] != true || threads[1].Params["model"] != "gpt-test" ||
		threads[1].Params["approvalPolicy"] != "on-request" {
		t.Fatalf("threads %+v", threads)
	}
	if calls := strings.Join(r.called(), "|"); strings.Contains(calls, "exit") || strings.Count(calls, "conv ") != 1 {
		t.Fatalf("listener %q", calls)
	}
}

func TestCodexModesAndModel(t *testing.T) {
	r := newCodexRig(t, t.TempDir(), 0, "bypassPermissions")
	_ = r.d.Send("hello")
	r.turnEnded()
	if p := r.sent("thread/start")[0].Params; p["approvalPolicy"] != "never" || p["sandbox"] != "danger-full-access" {
		t.Fatalf("thread/start %v", p)
	}
	if p := r.sent("turn/start")[0].Params; p["approvalPolicy"] != "never" || asJSON(p["sandboxPolicy"]) != `{"type":"dangerFullAccess"}` {
		t.Fatalf("turn/start %v", p)
	}
	// a mode and a model apply from the next turn
	if err := r.d.SetPermissionMode("plan"); err != nil {
		t.Fatal(err)
	}
	if err := r.d.SetModel("gpt-other"); err != nil {
		t.Fatal(err)
	}
	_ = r.d.Send("one")
	r.item(KindText)
	r.turnEnded()
	if p := r.sent("turn/start")[1].Params; p["approvalPolicy"] != "on-request" || p["approvalsReviewer"] != "user" ||
		asJSON(p["sandboxPolicy"]) != `{"type":"readOnly"}` || p["model"] != "gpt-other" {
		t.Fatalf("turn/start %v", p)
	}
	_ = r.d.SetPermissionMode("auto")
	_ = r.d.Send("two")
	r.item(KindText)
	r.turnEnded()
	if p := r.sent("turn/start")[2].Params; p["approvalsReviewer"] != "auto_review" || p["model"] != "gpt-other" ||
		asJSON(p["sandboxPolicy"]) != `{"excludeSlashTmp":false,"excludeTmpdirEnvVar":false,"networkAccess":false,"type":"workspaceWrite","writableRoots":["/w","/w2"]}` {
		t.Fatalf("turn/start %v", p)
	}
	if st := r.d.Status(); st.Model != "gpt-other" || st.PermissionMode != "auto" {
		t.Fatalf("status %+v", st)
	}
}

func TestCodexUnknownServerRequest(t *testing.T) {
	r := newCodexRig(t, t.TempDir(), 0, "default")
	_ = r.d.Send("tool")
	if txt := r.item(KindText); txt.Text != "tool call answered with an error: true" {
		t.Fatalf("reply %+v", txt)
	}
	r.turnEnded()
	if a := r.answerTo("1"); a.Error["code"] != -32601.0 || !strings.Contains(fmt.Sprint(a.Error["message"]), "item/tool/call") {
		t.Fatalf("answer %+v", a)
	}
	if len(r.d.Pending()) != 0 {
		t.Fatalf("pending %+v", r.d.Pending())
	}
}

// The engine alone, for what the fake does not script.
func TestCodexEngine(t *testing.T) {
	e := newCodexEngine()
	e.argv(codexBase(true), "default")
	reply := func(evs []ev, i int) map[string]any {
		t.Helper()
		if len(evs) <= i || evs[i].kind != evReply {
			t.Fatalf("no reply %d in %+v", i, evs)
		}
		var m map[string]any
		if err := json.Unmarshal(evs[i].reply, &m); err != nil {
			t.Fatalf("reply %q: %v", evs[i].reply, err)
		}
		return m
	}

	// a thread that cannot be resumed: a new one, said in the chat
	e.start(true, "thr_gone")
	evs := e.decode([]byte(`{"id":1,"result":{}}`))
	lines := strings.Split(strings.TrimSpace(string(evs[0].reply)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], `"method":"thread/resume"`) || !strings.Contains(lines[1], `"threadId":"thr_gone"`) {
		t.Fatalf("resume %q", lines)
	}
	if b := e.message("hi"); b != nil {
		t.Fatalf("a message before the thread: %s", b)
	}
	evs = e.decode([]byte(`{"id":2,"error":{"code":-32600,"message":"no rollout found"}}`))
	if len(evs) != 2 || evs[0].kind != evNotice || evs[0].text != "Could not continue the conversation; a new one started." ||
		reply(evs, 1)["method"] != "thread/start" {
		t.Fatalf("failed resume %+v", evs)
	}
	// the thread opens: the waiting message goes, with the thread's own sandbox (network on) and the launch's folders
	evs = e.decode([]byte(`{"id":3,"result":{"thread":{"id":"thr_new"},"model":"m1","sandbox":{"type":"workspaceWrite","writableRoots":["/r"],"networkAccess":true}}}`))
	if len(evs) != 2 || evs[0].kind != evInit || evs[0].conv != "thr_new" || evs[0].model != "m1" {
		t.Fatalf("thread %+v", evs)
	}
	ts := reply(evs, 1)
	p, _ := ts["params"].(map[string]any)
	if ts["method"] != "turn/start" || p["threadId"] != "thr_new" ||
		asJSON(p["sandboxPolicy"]) != `{"networkAccess":true,"type":"workspaceWrite","writableRoots":["/r","/w2"]}` {
		t.Fatalf("turn/start %v", ts)
	}
	// while it runs, a message waits; interrupt needs the turn's id
	if b := e.message("later"); b != nil {
		t.Fatalf("a message during a turn: %s", b)
	}
	if b := e.interrupt(); b != nil {
		t.Fatalf("interrupt before the turn id: %s", b)
	}
	if evs := e.decode([]byte(`{"id":4,"result":{"turn":{"id":"turn_9","status":"inProgress","items":[]}}}`)); evs != nil {
		t.Fatalf("turn/start response %+v", evs)
	}
	if b := e.interrupt(); !strings.Contains(string(b), `"turnId":"turn_9"`) {
		t.Fatalf("interrupt %s", b)
	}
	// another thread's (a sub-agent's) events are not the chat's
	if evs := e.decode([]byte(`{"method":"turn/completed","params":{"threadId":"thr_sub","turn":{"id":"t","status":"completed","items":[]}}}`)); evs != nil {
		t.Fatalf("a sub-agent's turn %+v", evs)
	}
	if evs := e.decode([]byte(`{"method":"thread/started","params":{"thread":{"id":"thr_sub","parentThreadId":"thr_new"}}}`)); evs != nil {
		t.Fatalf("a sub-agent's thread %+v", evs)
	}

	// permissions: what was asked, for the turn or the session; deny grants nothing
	perm := func(id int) *Request {
		evs := e.decode(fmt.Appendf(nil, `{"id":%d,"method":"item/permissions/requestApproval","params":{"itemId":"i","cwd":"/w","threadId":"thr_new","turnId":"turn_9","startedAtMs":0,"reason":"net","permissions":{"network":{"enabled":true}}}}`, id))
		if len(evs) != 1 || evs[0].kind != evRequest || evs[0].req.Tool != "Permissions" || evs[0].req.Reason != "net" {
			t.Fatalf("permissions %+v", evs)
		}
		return evs[0].req
	}
	for _, c := range []struct {
		decision, want string
	}{
		{Allow, `{"id":20,"result":{"permissions":{"network":{"enabled":true}},"scope":"turn"}}`},
		{AllowAlways, `{"id":21,"result":{"permissions":{"network":{"enabled":true}},"scope":"session"}}`},
		{Deny, `{"id":22,"result":{"permissions":{}}}`},
	} {
		req := perm(20 + slices.IndexFunc([]string{Allow, AllowAlways, Deny}, func(s string) bool { return s == c.decision }))
		b, err := e.answer(req, Answer{Decision: c.decision})
		if err != nil || strings.TrimSpace(string(b)) != c.want {
			t.Fatalf("%s: %s %v", c.decision, b, err)
		}
	}
	// an elicitation: denyStop cancels it and stops the turn
	evs = e.decode([]byte(`{"id":"el","method":"mcpServer/elicitation/request","params":{"serverName":"docs","threadId":"thr_new","turnId":"turn_9","mode":"form","message":"Pick one","requestedSchema":{}}}`))
	if len(evs) != 1 || evs[0].req.Tool != "mcp__docs" || evs[0].req.Title != "Pick one" || evs[0].req.ID != "el" {
		t.Fatalf("elicitation %+v", evs)
	}
	b, _ := e.answer(evs[0].req, Answer{Decision: DenyStop})
	lines = strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || lines[0] != `{"id":"el","result":{"action":"cancel"}}` || !strings.Contains(lines[1], `"method":"turn/interrupt"`) {
		t.Fatalf("elicitation answer %q", lines)
	}
	if _, err := e.answer(evs[0].req, Answer{Decision: Allow}); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("second answer: %v", err)
	}
	// a request Codex settles itself goes away
	evs = e.decode([]byte(`{"id":30,"method":"item/fileChange/requestApproval","params":{"itemId":"f","threadId":"thr_new","turnId":"turn_9","startedAtMs":0}}`))
	if _, err := e.answer(evs[0].req, Answer{Decision: "maybe"}); err == nil {
		t.Fatal("an unknown decision")
	}
	if evs := e.decode([]byte(`{"method":"serverRequest/resolved","params":{"threadId":"thr_new","requestId":30}}`)); len(evs) != 1 ||
		evs[0].kind != evCancel || evs[0].id != "30" {
		t.Fatalf("resolved %+v", evs)
	}
	// the turn ends: a request of it still open goes away, the waiting message goes
	e.decode([]byte(`{"id":31,"method":"item/commandExecution/requestApproval","params":{"itemId":"c","command":"ls","threadId":"thr_new","turnId":"turn_9","startedAtMs":0}}`))
	evs = e.decode([]byte(`{"method":"turn/completed","params":{"threadId":"thr_new","turn":{"id":"turn_9","status":"interrupted","items":[],"error":null}}}`))
	if len(evs) != 3 || evs[0].kind != evTurnEnd || evs[0].failed || evs[1].kind != evCancel || evs[1].id != "31" ||
		!strings.Contains(string(evs[2].reply), `"text":"later"`) {
		t.Fatalf("turn end %+v", evs)
	}
	// a turn/start Codex refuses ends that turn as failed
	evs = e.decode([]byte(`{"id":7,"error":{"code":-32600,"message":"bad model"}}`)) // the turn/start of "later"
	if len(evs) != 1 || evs[0].kind != evTurnEnd || !evs[0].failed || evs[0].text != "bad model" {
		t.Fatalf("refused turn %+v", evs)
	}
}
