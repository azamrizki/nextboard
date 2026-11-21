package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
	"github.com/azamrizki/nextboard/internal/nextcode/native/tools"
)

func TestInitialize(t *testing.T) {
	h := startAgent(t, newConfig(t, newFake(), newKit()))
	h.send(map[string]any{"type": "control_request", "request_id": "nb_1", "request": map[string]any{"subtype": "initialize", "hooks": nil}})
	r := h.next()
	if r["type"] != "control_response" || str(r, "response", "subtype") != "success" || str(r, "response", "request_id") != "nb_1" {
		t.Fatalf("initialize answer: %v", r)
	}
	if str(r, "response", "response", "commands", "0", "name") != "compact" || str(r, "response", "response", "commands", "0", "description") == "" {
		t.Fatalf("no compact command: %v", r)
	}
	i := h.next()
	if i["type"] != "system" || i["subtype"] != "init" || i["session_id"] != "sess-1" || i["model"] != "claude-test" || i["permissionMode"] != "default" {
		t.Fatalf("init: %v", i)
	}
	var names []string
	for _, n := range i["tools"].([]any) {
		names = append(names, n.(string))
	}
	for _, want := range []string{"Read", "Write", "Edit", "Bash", "WebFetch", "TodoWrite", "AskUserQuestion", "ExitPlanMode"} {
		if !slices.Contains(names, want) {
			t.Errorf("tools %v: no %s", names, want)
		}
	}
	if code := h.close(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestTextTurn(t *testing.T) {
	fake := newFake(answer(llm.StopEnd, llm.Usage{Input: 100, Output: 20, CacheRead: 50, CacheWrite: 7}, thinkingBlock("hmm"), thinkingBlock(""), textBlock("Hello there")))
	cfg := newConfig(t, fake, newKit())
	h := startAgent(t, cfg)
	h.initialize()
	h.say("hi")
	res, before := h.result()

	var events []string
	var msgID string
	for _, l := range before {
		if l["type"] != "stream_event" {
			continue
		}
		e := l["event"].(map[string]any)
		s := e["type"].(string)
		switch s {
		case "message_start":
			msgID = str(e, "message", "id")
		case "content_block_start":
			s += ":" + str(e, "content_block", "type")
		case "content_block_delta":
			s += ":" + str(e, "delta", "type")
		}
		events = append(events, s)
	}
	want := []string{"message_start", "content_block_start:thinking", "content_block_delta:thinking_delta", "content_block_start:thinking",
		"content_block_start:text", "content_block_delta:text_delta", "content_block_stop", "content_block_stop", "content_block_stop", "message_stop"}
	if !slices.Equal(events, want) {
		t.Fatalf("stream events\n got %v\nwant %v", events, want)
	}
	var asst map[string]any
	for _, l := range before {
		if l["type"] == "assistant" {
			asst = l
		}
	}
	if asst == nil || str(asst, "message", "id") != msgID || msgID == "" {
		t.Fatalf("assistant line %v, stream id %q", asst, msgID)
	}
	blocks := field(asst, "message", "content").([]any)
	if len(blocks) != 3 || str(blocks, "0", "type") != "thinking" || str(blocks, "0", "thinking") != "hmm" ||
		str(blocks, "1", "type") != "thinking" || str(blocks, "2", "text") != "Hello there" {
		t.Fatalf("assistant content: %v", blocks)
	}
	if res["subtype"] != "success" || res["is_error"] != false || res["result"] != "Hello there" || res["session_id"] != "sess-1" {
		t.Fatalf("result: %v", res)
	}
	if field(res, "usage", "input_tokens") != 100.0 || field(res, "usage", "cache_read_input_tokens") != 50.0 ||
		field(res, "usage", "cache_creation_input_tokens") != 7.0 || field(res, "usage", "output_tokens") != 20.0 {
		t.Fatalf("usage: %v", res["usage"])
	}

	req := fake.request(t, 0)
	conv := loadConv(t, cfg, "sess-1")
	if req.System != conv.System || !strings.Contains(req.System, "NextCode") || !strings.Contains(req.System, cfg.cwd) {
		t.Fatalf("system prompt: %q", req.System)
	}
	if len(req.Tools) != 8 || req.Model != "claude-test" {
		t.Fatalf("tools %d, model %q", len(req.Tools), req.Model)
	}
	first := texts(req.Messages[0])
	if len(first) != 2 || !strings.Contains(first[0], "<system-reminder>Today's date is ") || first[1] != "hi" {
		t.Fatalf("first message: %q", first)
	}
	if len(conv.Messages) != 2 || conv.Messages[1].Role != llm.RoleAssistant || len(conv.Messages[1].Blocks) != 3 || conv.Model != "claude-test" {
		t.Fatalf("conversation: %+v", conv.Messages)
	}
	info, err := os.Stat(store{dir: cfg.stateDir}.path("sess-1"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("conversation file: %v %v", info.Mode(), err)
	}
	// the next turn tells the date no more
	fake.steps = append(fake.steps, say("again"))
	h.say("more")
	h.result()
	if got := texts(lastMessage(fake.request(t, 1))); !slices.Equal(got, []string{"more"}) {
		t.Fatalf("second message: %q", got)
	}
}

func TestToolAskAllow(t *testing.T) {
	fake := newFake(callTool("toolu_1", "Bash", `{"command":"npm test -- -run X"}`), say("done"))
	k := newKit()
	cfg := newConfig(t, fake, k)
	h := startAgent(t, cfg)
	h.initialize()
	h.say("run the tests")
	req, before := h.request()
	if str(req, "request_id") != "nc_1" || str(req, "request", "subtype") != "can_use_tool" || str(req, "request", "tool_name") != "Bash" ||
		str(req, "request", "tool_use_id") != "toolu_1" || str(req, "request", "input", "command") != "npm test -- -run X" {
		t.Fatalf("request: %v", req)
	}
	if str(req, "request", "permission_suggestions", "0", "rules", "0", "ruleContent") != "npm test:*" ||
		str(req, "request", "permission_suggestions", "0", "type") != "addRules" || str(req, "request", "permission_suggestions", "0", "destination") != "session" {
		t.Fatalf("suggestions: %v", field(req, "request", "permission_suggestions"))
	}
	if !strings.Contains(str(req, "request", "decision_reason"), "default mode") || field(req, "request", "blocked_path") != nil {
		t.Fatalf("reason or blocked path: %v", req["request"])
	}
	// the tool call was shown before the request
	if !slices.ContainsFunc(before, func(m map[string]any) bool { return m["type"] == "assistant" }) {
		t.Fatal("no assistant line before the request")
	}
	if len(k.bash.ran()) != 0 {
		t.Fatal("the command ran before the owner's answer")
	}
	h.allow("nc_1", map[string]any{"command": "npm test -- -run X"}, nil)
	res, before := h.result()
	rs := toolResults(before)
	if len(rs) != 1 || rs[0]["tool_use_id"] != "toolu_1" || rs[0]["content"] != "Bash ran: npm test -- -run X" || rs[0]["is_error"] != false {
		t.Fatalf("tool results: %v", rs)
	}
	if res["result"] != "done" || res["subtype"] != "success" || field(res, "usage", "input_tokens") != 20.0 {
		t.Fatalf("result: %v", res)
	}
	second := fake.request(t, 1)
	if len(second.Messages) != 3 || second.Messages[1].Role != llm.RoleAssistant || second.Messages[2].Blocks[0].Type != llm.BlockToolResult ||
		second.Messages[2].Blocks[0].ToolUseID != "toolu_1" || second.Messages[2].Blocks[0].Text != "Bash ran: npm test -- -run X" {
		t.Fatalf("second call's messages: %+v", second.Messages)
	}
	if !slices.EqualFunc(fake.request(t, 0).Tools, second.Tools, func(a, b llm.ToolDef) bool { return a.Name == b.Name && string(a.Schema) == string(b.Schema) }) {
		t.Fatal("the tools changed between calls")
	}
	checkHistory(t, loadConv(t, cfg, "sess-1").Messages)
}

func TestToolRunsWithUpdatedInput(t *testing.T) {
	fake := newFake(callTool("toolu_1", "Bash", `{"command":"make deploy"}`), say("ok"))
	k := newKit()
	h := startAgent(t, newConfig(t, fake, k))
	h.initialize()
	h.say("go")
	h.request()
	h.allow("nc_1", map[string]any{"command": "make deploy-staging"}, nil)
	h.result()
	if got := k.bash.ran(); len(got) != 1 || stringField(got[0], "command") != "make deploy-staging" {
		t.Fatalf("ran %s", got)
	}
}

func TestDeny(t *testing.T) {
	fake := newFake(callTool("toolu_1", "Bash", `{"command":"rm -rf build"}`), say("ok, I won't"))
	k := newKit()
	cfg := newConfig(t, fake, k)
	h := startAgent(t, cfg)
	h.initialize()
	h.say("clean")
	h.request()
	h.deny("nc_1", "not now", false)
	res, before := h.result()
	rs := toolResults(before)
	if len(rs) != 1 || rs[0]["content"] != "The owner denied this: not now" || rs[0]["is_error"] != true {
		t.Fatalf("tool results: %v", rs)
	}
	if res["result"] != "ok, I won't" || len(fake.requests()) != 2 || len(k.bash.ran()) != 0 {
		t.Fatalf("result %v, %d calls, ran %d", res, len(fake.requests()), len(k.bash.ran()))
	}
}

func TestDenyInterrupt(t *testing.T) {
	fake := newFake(answer(llm.StopToolUse, smallUsage, textBlock("Two things."), toolUse("t1", "Bash", `{"command":"make a"}`), toolUse("t2", "Bash", `{"command":"make b"}`)))
	k := newKit()
	cfg := newConfig(t, fake, k)
	h := startAgent(t, cfg)
	h.initialize()
	h.say("do both")
	h.request()
	h.deny("nc_1", "stop", true)
	res, before := h.result()
	if rs := toolResults(before); len(rs) != 1 || rs[0]["tool_use_id"] != "t1" || rs[0]["content"] != "The owner denied this: stop" {
		t.Fatalf("tool results: %v", rs)
	}
	if res["subtype"] != "success" || res["result"] != "Two things." {
		t.Fatalf("result: %v", res)
	}
	if n := len(fake.requests()); n != 1 || len(k.bash.ran()) != 0 {
		t.Fatalf("%d calls after the stop, ran %d", n, len(k.bash.ran()))
	}
	conv := loadConv(t, cfg, "sess-1")
	checkHistory(t, conv.Messages)
	last := conv.Messages[len(conv.Messages)-1]
	if len(last.Blocks) != 2 || last.Blocks[1].ToolUseID != "t2" || last.Blocks[1].Text != stoppedBefore || !last.Blocks[1].IsError {
		t.Fatalf("results kept: %+v", last.Blocks)
	}
}

func TestInterruptMidStream(t *testing.T) {
	fake := newFake(hang("Partial ans"), say("second answer"))
	cfg := newConfig(t, fake, newKit())
	h := startAgent(t, cfg)
	h.initialize()
	h.say("hi")
	h.until(func(m map[string]any) bool {
		return m["type"] == "stream_event" && str(m, "event", "type") == "content_block_delta"
	})
	h.control("nb_2", map[string]any{"subtype": "interrupt"})
	res, before := h.result()
	sawAnswer, sawAsst := false, false
	for _, l := range before {
		if l["type"] == "control_response" && str(l, "response", "request_id") == "nb_2" && str(l, "response", "subtype") == "success" {
			sawAnswer = true
		}
		if l["type"] == "assistant" && str(l, "message", "content", "0", "text") == "Partial ans" {
			sawAsst = true
		}
	}
	if !sawAnswer || !sawAsst || res["subtype"] != "success" || res["result"] != "Partial ans" {
		t.Fatalf("answer %v, partial %v, result %v", sawAnswer, sawAsst, res)
	}
	conv := loadConv(t, cfg, "sess-1")
	if len(conv.Messages) != 2 || texts(conv.Messages[1])[0] != "Partial ans" {
		t.Fatalf("kept: %+v", conv.Messages)
	}
	h.say("again")
	h.result()
	last := texts(lastMessage(fake.request(t, 1)))
	if len(last) != 2 || !strings.Contains(last[0], "stopped your last answer") || last[1] != "again" {
		t.Fatalf("next message: %q", last)
	}
}

func TestInterruptWhileAsking(t *testing.T) {
	fake := newFake(callTool("t1", "Bash", `{"command":"make deploy"}`))
	k := newKit()
	cfg := newConfig(t, fake, k)
	h := startAgent(t, cfg)
	h.initialize()
	h.say("deploy")
	h.request()
	h.control("nb_2", map[string]any{"subtype": "interrupt"})
	_, before := h.result()
	if !slices.ContainsFunc(before, func(m map[string]any) bool { return m["type"] == "control_cancel_request" && m["request_id"] == "nc_1" }) {
		t.Fatalf("no cancel of nc_1 in %v", before)
	}
	// the answer that comes too late changes nothing
	h.allow("nc_1", map[string]any{"command": "make deploy"}, nil)
	h.control("nb_3", map[string]any{"subtype": "set_permission_mode", "mode": "default"})
	h.until(is("control_response"))
	if len(k.bash.ran()) != 0 {
		t.Fatal("the command ran after the stop")
	}
	conv := loadConv(t, cfg, "sess-1")
	checkHistory(t, conv.Messages)
	last := conv.Messages[len(conv.Messages)-1].Blocks[0]
	if last.Text != stoppedBefore || !last.IsError {
		t.Fatalf("result kept: %+v", last)
	}
}

func TestInterruptWhileToolRuns(t *testing.T) {
	fake := newFake(callTool("t1", "Bash", `{"command":"sleep"}`), say("after"))
	k := newKit()
	cfg := newConfig(t, fake, k)
	cfg.mode = modeBypass
	h := startAgent(t, cfg)
	h.initialize()
	h.say("wait")
	select {
	case <-k.running:
	case <-time.After(waitFor):
		t.Fatal("the command did not start")
	}
	h.control("nb_2", map[string]any{"subtype": "interrupt"})
	_, before := h.result()
	if rs := toolResults(before); len(rs) != 0 {
		t.Fatalf("a stopped tool's result was shown: %v", rs) // the driver shows it as stopped
	}
	conv := loadConv(t, cfg, "sess-1")
	checkHistory(t, conv.Messages)
	last := conv.Messages[len(conv.Messages)-1].Blocks[0]
	if !strings.HasPrefix(last.Text, stoppedWhile) || !strings.Contains(last.Text, "slept a little") || !last.IsError {
		t.Fatalf("result kept: %+v", last)
	}
	// the next turn sends the stopped results and the owner's message as one user message
	h.say("next")
	h.result()
	m := lastMessage(fake.request(t, 1))
	if m.Role != llm.RoleUser || m.Blocks[0].Type != llm.BlockToolResult || texts(m)[len(texts(m))-1] != "next" {
		t.Fatalf("merged message: %+v", m)
	}
}

func TestQueuedMessages(t *testing.T) {
	release := make(chan struct{})
	fake := newFake(gate(release, say("one")), say("two"))
	h := startAgent(t, newConfig(t, fake, newKit()))
	h.initialize()
	h.say("first")
	<-fake.called
	h.say("second")
	h.say("third")
	h.control("nb_2", map[string]any{"subtype": "set_permission_mode", "mode": "default"}) // read after both
	h.until(is("control_response"))
	close(release)
	r1, _ := h.result()
	r2, _ := h.result()
	if r1["result"] != "one" || r2["result"] != "two" {
		t.Fatalf("results %v, %v", r1["result"], r2["result"])
	}
	if n := len(fake.requests()); n != 2 {
		t.Fatalf("%d calls", n)
	}
	if got := texts(lastMessage(fake.request(t, 1))); !slices.Equal(got, []string{"second", "third"}) {
		t.Fatalf("queued message: %q", got)
	}
}

func TestUnknownToolAndBadInput(t *testing.T) {
	fake := newFake(answer(llm.StopToolUse, smallUsage, toolUse("t1", "Nope", `{}`), toolUse("t2", "Bash", `[1,2]`), toolUse("t3", "Bash", `{"command":`)), say("ok"))
	cfg := newConfig(t, fake, newKit())
	h := startAgent(t, cfg)
	h.initialize()
	h.say("go")
	_, before := h.result()
	if slices.ContainsFunc(before, is("control_request")) {
		t.Fatal("a request for a call that cannot run")
	}
	rs := toolResults(before)
	if len(rs) != 3 || rs[0]["content"] != "There is no tool named Nope." || rs[1]["content"] != "The input of Bash was not a JSON object." ||
		rs[2]["content"] != "The input of Bash was not a JSON object." || rs[2]["is_error"] != true {
		t.Fatalf("results: %v", rs)
	}
	conv := loadConv(t, cfg, "sess-1")
	for _, b := range conv.Messages[1].Blocks {
		if !isObject(b.Input) {
			t.Fatalf("a tool input kept that no provider takes: %s", b.Input)
		}
	}
	checkHistory(t, conv.Messages)
}

func TestToolPanics(t *testing.T) {
	fake := newFake(callTool("t1", "Bash", `{"command":"panic"}`), say("ok"))
	cfg := newConfig(t, fake, newKit())
	cfg.mode = modeBypass
	h := startAgent(t, cfg)
	h.initialize()
	h.say("go")
	_, before := h.result()
	if rs := toolResults(before); len(rs) != 1 || !strings.Contains(rs[0]["content"].(string), "the tool broke") || rs[0]["is_error"] != true {
		t.Fatalf("results: %v", rs)
	}
}

func TestToolResultClipped(t *testing.T) {
	big := strings.Repeat("a", 20_000) + strings.Repeat("b", 20_000)
	k := newKit()
	k.read.run = func(_ context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
		if stringField(input, "file_path") == "empty" {
			return tools.Result{}
		}
		return tools.Result{Output: big}
	}
	fake := newFake(callTool("t1", "Read", `{"file_path":"x"}`), callTool("t2", "Read", `{"file_path":"empty"}`), say("ok"))
	h := startAgent(t, newConfig(t, fake, k))
	h.initialize()
	h.say("read")
	_, before := h.result()
	got := lastMessage(fake.request(t, 1)).Blocks[0].Text
	if n := len([]rune(got)); n > maxResult || !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, "bbb") {
		t.Fatalf("clipped to %d characters", n)
	}
	if rs := toolResults(before); len(rs) != 2 || rs[0]["content"] != got || rs[1]["content"] != "(no output)" {
		t.Fatalf("shown: %d results", len(rs))
	}
}

func TestStopMaxAndRefusal(t *testing.T) {
	fake := newFake(answer(llm.StopMax, smallUsage, textBlock("A long ans")), answer(llm.StopRefusal, smallUsage, textBlock("I can't")))
	h := startAgent(t, newConfig(t, fake, newKit()))
	h.initialize()
	h.say("write a lot")
	res, before := h.result()
	note := false
	for _, l := range before {
		if l["type"] == "assistant" && strings.Contains(str(l, "message", "content", "0", "text"), "cut off at the token limit") {
			note = true
		}
	}
	if !note || res["subtype"] != "success" || res["result"] != "A long ans" {
		t.Fatalf("note %v, result %v", note, res)
	}
	h.say("something bad")
	res, _ = h.result()
	if res["subtype"] != "error_during_execution" || res["is_error"] != true || !strings.Contains(res["result"].(string), "declined") {
		t.Fatalf("refusal result: %v", res)
	}
}

func TestAPIError(t *testing.T) {
	fake := newFake(failWith(&llm.APIError{Status: 401, Message: "invalid x-api-key"}), failWith(errors.New("dial tcp: refused")))
	h := startAgent(t, newConfig(t, fake, newKit()))
	h.initialize()
	h.say("hi")
	res, _ := h.result()
	if res["subtype"] != "error_during_execution" || res["is_error"] != true || res["result"] != "invalid x-api-key" {
		t.Fatalf("result: %v", res)
	}
	h.say("again")
	res, _ = h.result()
	if res["result"] != "dial tcp: refused" {
		t.Fatalf("result: %v", res)
	}
}

func TestRetryLine(t *testing.T) {
	fake := newFake(func(ctx context.Context, req llm.Request, on func(llm.Event)) (*llm.Response, error) {
		on(llm.Event{Kind: llm.EvRetry, Attempt: 1, MaxAttempts: 4, Text: "overloaded"})
		return say("ok")(ctx, req, on)
	})
	h := startAgent(t, newConfig(t, fake, newKit()))
	h.initialize()
	h.say("hi")
	r, _ := h.until(is("system", "api_retry"))
	if r["attempt"] != 1.0 || r["max_retries"] != 4.0 {
		t.Fatalf("retry: %v", r)
	}
	h.result()
}

func TestControlRequests(t *testing.T) {
	fake := newFake(say("a"), say("b"))
	h := startAgent(t, newConfig(t, fake, newKit()))
	h.initialize()
	h.control("nb_2", map[string]any{"subtype": "set_model", "model": "claude-other"})
	if r, _ := h.until(is("control_response")); str(r, "response", "subtype") != "success" || str(r, "response", "request_id") != "nb_2" {
		t.Fatalf("set_model: %v", r)
	}
	h.say("one")
	h.result()
	h.control("nb_3", map[string]any{"subtype": "set_model", "model": nil})
	h.until(is("control_response"))
	h.say("two")
	h.result()
	if m0, m1 := fake.request(t, 0).Model, fake.request(t, 1).Model; m0 != "claude-other" || m1 != "claude-test" {
		t.Fatalf("models %q, %q", m0, m1)
	}
	h.control("nb_4", map[string]any{"subtype": "rewind_files"})
	if r, _ := h.until(is("control_response")); str(r, "response", "subtype") != "error" || str(r, "response", "request_id") != "nb_4" || str(r, "response", "error") == "" {
		t.Fatalf("unknown subtype: %v", r)
	}
	h.control("nb_5", map[string]any{"subtype": "set_permission_mode", "mode": "yolo"})
	if r, _ := h.until(is("control_response")); str(r, "response", "subtype") != "error" {
		t.Fatalf("bad mode: %v", r)
	}
	h.control("nb_6", map[string]any{"subtype": "interrupt"}) // nothing runs: still a success
	if r, _ := h.until(is("control_response")); str(r, "response", "subtype") != "success" {
		t.Fatalf("idle interrupt: %v", r)
	}
}

func TestModeReminder(t *testing.T) {
	fake := newFake(say("a"), say("b"), say("c"))
	h := startAgent(t, newConfig(t, fake, newKit()))
	h.initialize()
	h.say("one")
	h.result()
	h.control("nb_2", map[string]any{"subtype": "set_permission_mode", "mode": "plan"})
	h.until(is("control_response"))
	h.say("two")
	h.result()
	got := texts(lastMessage(fake.request(t, 1)))
	if len(got) != 2 || !strings.Contains(got[0], "<system-reminder>The owner switched the permission mode to plan:") || got[1] != "two" {
		t.Fatalf("message after the switch: %q", got)
	}
	// switched away and back: nothing to tell
	h.control("nb_3", map[string]any{"subtype": "set_permission_mode", "mode": "default"})
	h.until(is("control_response"))
	h.control("nb_4", map[string]any{"subtype": "set_permission_mode", "mode": "plan"})
	h.until(is("control_response"))
	h.say("three")
	h.result()
	if got := texts(lastMessage(fake.request(t, 2))); !slices.Equal(got, []string{"three"}) {
		t.Fatalf("message: %q", got)
	}
	if fake.request(t, 0).System != fake.request(t, 2).System {
		t.Fatal("the system prompt changed with the mode")
	}
}

func TestToolIDsUnique(t *testing.T) {
	fake := newFake(callTool("call_0", "Read", `{"file_path":"a"}`), callTool("call_0", "Read", `{"file_path":"b"}`), say("ok"))
	cfg := newConfig(t, fake, newKit())
	h := startAgent(t, cfg)
	h.initialize()
	h.say("go")
	_, before := h.result()
	rs := toolResults(before)
	if len(rs) != 2 || rs[0]["tool_use_id"] != "call_0" || rs[1]["tool_use_id"] == "call_0" || rs[1]["tool_use_id"] == "" {
		t.Fatalf("results: %v", rs)
	}
	checkHistory(t, loadConv(t, cfg, "sess-1").Messages)
}

func TestStdinCloseStopsTurn(t *testing.T) {
	fake := newFake(hang("working"))
	cfg := newConfig(t, fake, newKit())
	h := startAgent(t, cfg)
	h.initialize()
	h.say("hi")
	<-fake.called
	if code := h.close(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	conv := loadConv(t, cfg, "sess-1")
	if len(conv.Messages) == 0 || texts(conv.Messages[0])[len(texts(conv.Messages[0]))-1] != "hi" {
		t.Fatalf("not saved: %+v", conv.Messages)
	}
}

func TestWire(t *testing.T) {
	u := func(s string) llm.Message { return llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{textBlock(s)}} }
	a := func(s string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{textBlock(s)}}
	}
	msgs := []llm.Message{u("1"), a("2"), u("3"), u("4"), a("5"), u("6")}
	got := wire(msgs)
	if len(got) != 5 || !slices.Equal(texts(got[2]), []string{"3", "4"}) || len(msgs[2].Blocks) != 1 {
		t.Fatalf("wire: %+v (the conversation: %+v)", got, msgs)
	}
	// the prefix before the last answer is the same as more messages come
	more := wire(append(slices.Clone(msgs), u("7")))
	b1, _ := json.Marshal(got[:4])
	b2, _ := json.Marshal(more[:4])
	if string(b1) != string(b2) {
		t.Fatal("the prefix changed")
	}
}

func TestClipMiddle(t *testing.T) {
	if s := clipMiddle("short", 30); s != "short" {
		t.Fatal(s)
	}
	big := strings.Repeat("h", 20_000) + strings.Repeat("é", 20_000) + strings.Repeat("t", 20_000)
	got := clipMiddle(big, maxResult)
	if n := len([]rune(got)); n > maxResult || n < maxResult-100 {
		t.Fatalf("%d characters", n)
	}
	if !strings.HasPrefix(got, "hhhh") || !strings.HasSuffix(got, "tttt") || !strings.Contains(got, "characters left out") {
		t.Fatal("head or tail lost")
	}
}

// TestRealTools runs the agent with the built-in tools of package tools, in a temp folder.
func TestRealTools(t *testing.T) {
	fake := newFake(
		callTool("t1", "Write", `{"file_path":"hello.txt","content":"hi there\n"}`),
		callTool("t2", "Bash", `{"command":"ls"}`),
		callTool("t3", "Bash", `{"command":"touch other.txt"}`),
		callTool("t4", "Read", `{"file_path":"hello.txt"}`),
		say("done"),
	)
	cfg := newConfig(t, fake, newKit())
	cfg.tools, cfg.editPath, cfg.readOnly = tools.All(), nil, nil
	cfg.mode = modeAuto
	h := startAgent(t, cfg)
	h.initialize()
	h.say("write and list")
	req, before := h.request()
	if str(req, "request", "tool_use_id") != "t3" {
		t.Fatalf("asked for %v", req)
	}
	rs := toolResults(before)
	if len(rs) != 2 || rs[0]["is_error"] != false || !strings.Contains(rs[1]["content"].(string), "hello.txt") {
		t.Fatalf("results: %v", rs)
	}
	h.deny(str(req, "request_id"), "no", false)
	_, before = h.result()
	if rs := toolResults(before); len(rs) != 2 || !strings.Contains(rs[1]["content"].(string), "hi there") {
		t.Fatalf("results: %v", rs)
	}
	if b, err := os.ReadFile(cfg.cwd + "/hello.txt"); err != nil || string(b) != "hi there\n" {
		t.Fatalf("hello.txt: %q %v", b, err)
	}
	if _, err := os.Stat(cfg.cwd + "/other.txt"); !os.IsNotExist(err) {
		t.Fatal("the denied command ran")
	}
}
