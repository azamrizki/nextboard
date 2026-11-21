package llm

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// chatText1 is a short text answer, as OpenAI streams it.
var chatText1 = []string{
	data(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`),
	data(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`),
	data(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`),
	data(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
	data(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":64}}}`),
	"data: [DONE]\n\n",
}

// chunk is one chunk of a stream with this delta.
func chunk(delta string, finish ...string) string {
	f := "null"
	if len(finish) > 0 {
		f = `"` + finish[0] + `"`
	}
	return data(`{"id":"c2","object":"chat.completion.chunk","created":1,"model":"deepseek-reasoner","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + f + `}]}`)
}

func chatRun(t *testing.T, cfg ProviderConfig, req Request, events ...string) (*Response, *recorder, *fake) {
	t.Helper()
	f := newFake(t, stream(events...))
	cfg.Kind, cfg.BaseURL = "openai", f.srv.URL
	rec := &recorder{}
	resp, err := NewClient(cfg, nil).Stream(context.Background(), req, rec.on)
	if err != nil {
		t.Fatal(err)
	}
	checkEvents(t, rec.events, resp)
	return resp, rec, f
}

func TestChatText(t *testing.T) {
	resp, _, f := chatRun(t, ProviderConfig{Key: "sk-o"}, Request{Model: "gpt-x"}, chatText1...)
	want := &Response{ID: "chatcmpl-1", Model: "gpt-x", StopReason: StopEnd, Usage: Usage{Input: 36, Output: 5, CacheRead: 64},
		Blocks: []Block{{Type: BlockText, Text: "Hello", Wire: WireChat, Model: "gpt-x"}}}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("response\n got %+v\nwant %+v", resp, want)
	}
	h := f.last(t).Header
	for k, v := range map[string]string{"Authorization": "Bearer sk-o", "Content-Type": "application/json", "User-Agent": "NextBoard-NextCode", "anthropic-version": ""} {
		if h.Get(k) != v {
			t.Errorf("header %s = %q, want %q", k, h.Get(k), v)
		}
	}
	_, _, f = chatRun(t, ProviderConfig{}, Request{Model: "llama3"}, chatText1...)
	if a, ok := f.last(t).Header["Authorization"]; ok {
		t.Errorf("Authorization %q with no key", a)
	}
}

func TestChatRequest(t *testing.T) {
	model := "deepseek-reasoner"
	req := Request{Model: model, System: "Be brief.", Effort: "max", MaxTokens: 1000,
		Tools: []ToolDef{{Name: "Bash", Description: "Runs a command.", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "Fix it"}}},
			{Role: RoleAssistant, Blocks: []Block{
				{Type: BlockThinking, Text: "old think", Wire: WireChat, Model: model},
				{Type: BlockText, Text: "Looking", Wire: WireChat},
				{Type: BlockToolUse, ID: "call_1", Name: "Read", Input: json.RawMessage(`{"file_path": "a"}`)},
			}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_1", Text: "contents"}}},
			{Role: RoleAssistant, Blocks: []Block{{Type: BlockText, Text: "Done."}}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "Now b <&>"}}},
			// the current turn: its thinking goes back, but never another wire's
			{Role: RoleAssistant, Blocks: []Block{
				{Type: BlockThinking, Text: "foreign", Raw: json.RawMessage(`{"type":"thinking"}`), Wire: WireAnthropic, Model: model},
				{Type: BlockThinking, Text: "new think", Wire: WireChat, Model: model},
				{Type: BlockToolUse, ID: "call_2", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
			}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_2", Text: "boom", IsError: true}}},
			{Role: RoleAssistant, Blocks: []Block{{Type: BlockThinking, Text: "more", Wire: WireChat, Model: model}, {Type: BlockToolUse, ID: "call_3", Name: "Bash"}}},
		}}
	_, _, f := chatRun(t, ProviderConfig{}, req, chatText1...)
	want := `{"model":"deepseek-reasoner","messages":[` +
		`{"role":"system","content":"Be brief."},` +
		`{"role":"user","content":"Fix it"},` +
		`{"role":"assistant","content":"Looking","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"a\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"contents"},` +
		`{"role":"assistant","content":"Done."},` +
		`{"role":"user","content":"Now b <&>"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_2","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}],"reasoning_content":"new think"},` +
		`{"role":"tool","tool_call_id":"call_2","content":"Error: boom"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_3","type":"function","function":{"name":"Bash","arguments":"{}"}}],"reasoning_content":"more"}` +
		`],"stream":true,"stream_options":{"include_usage":true},` +
		`"tools":[{"type":"function","function":{"name":"Bash","description":"Runs a command.","parameters":{"type":"object"}}}],` +
		`"reasoning_effort":"high","max_tokens":1000}`
	if got := f.last(t).Body; got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}
	if got := f.last(t).Path; got != "/chat/completions" {
		t.Errorf("path %s", got)
	}
}

func TestChatToolMessagesComeFirst(t *testing.T) {
	msgs := chatMessages(Request{Messages: []Message{
		{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "a"}, {Type: BlockToolResult, ToolUseID: "t1", Text: "r1"},
			{Type: BlockText, Text: "b"}, {Type: BlockToolResult, ToolUseID: "t2", Text: "r2", IsError: true}}},
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockThinking, Text: "only thinking", Wire: WireAnthropic}}},
	}})
	want := `[{"role":"tool","tool_call_id":"t1","content":"r1"},{"role":"tool","tool_call_id":"t2","content":"Error: r2"},` +
		`{"role":"user","content":"a\n\nb"},{"role":"assistant","content":"(no answer)"}]`
	if got := mustJSON(t, msgs); got != want {
		t.Errorf("messages\n got %s\nwant %s", got, want)
	}
}

func TestChatEffortAndBodyDefaults(t *testing.T) {
	for in, want := range map[string]string{"": "", "low": "low", "medium": "medium", "high": "high", "xhigh": "high", "max": "high"} {
		if got := chatEffort(in); got != want {
			t.Errorf("%q → %q", in, got)
		}
	}
	b, err := chatBody(Request{Model: "m", Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`; string(b) != want {
		t.Errorf("body %s", b)
	}
}

func TestChatThinkingAndTwoTools(t *testing.T) {
	model := "deepseek-reasoner"
	resp, rec, _ := chatRun(t, ProviderConfig{}, Request{Model: model},
		chunk(`{"role":"assistant","content":null,"reasoning_content":"Let me"}`),
		chunk(`{"content":null,"reasoning_content":" think."}`),
		chunk(`{"content":"Reading both."}`),
		chunk(`{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"Read","arguments":""}}]}`),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"file_path\":"}}]}`),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\"/a.go\"}"}}]}`),
		chunk(`{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"Bash","arguments":"{\"command\":"}}]}`),
		chunk(`{"tool_calls":[{"index":1,"function":{"arguments":"\"ls\"}"}}]}`),
		chunk(`{}`, "tool_calls"),
		data(`{"id":"c2","choices":[],"usage":{"prompt_tokens":500,"completion_tokens":40,"prompt_cache_hit_tokens":300,"prompt_cache_miss_tokens":200}}`),
		"data: [DONE]\n\n")
	want := []Block{
		{Type: BlockThinking, Text: "Let me think."},
		{Type: BlockText, Text: "Reading both."},
		{Type: BlockToolUse, ID: "call_a", Name: "Read", Input: json.RawMessage(`{"file_path":"/a.go"}`)},
		{Type: BlockToolUse, ID: "call_b", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
	}
	for i := range want {
		want[i].Wire, want[i].Model = WireChat, model
	}
	if !reflect.DeepEqual(resp.Blocks, want) {
		t.Errorf("blocks\n got %s\nwant %s", mustJSON(t, resp.Blocks), mustJSON(t, want))
	}
	if resp.StopReason != StopToolUse || resp.Usage != (Usage{Input: 200, Output: 40, CacheRead: 300}) || resp.ID != "c2" {
		t.Errorf("stop %q, usage %+v, id %q", resp.StopReason, resp.Usage, resp.ID)
	}
	if s := rec.kind(EvBlockStart); len(s) != 4 || s[3].ToolID != "call_b" || s[3].ToolName != "Bash" {
		t.Errorf("starts %+v", s)
	}
}

func TestChatQuirks(t *testing.T) {
	// OpenRouter's reasoning, a call with no id, a second call under a used index, "stop"
	// with tool calls, <think> left as text, and no [DONE]
	resp, _, _ := chatRun(t, ProviderConfig{}, Request{Model: "m"},
		chunk(`{"reasoning":"hmm"}`),
		chunk(`{"content":"<think>x</think>ok"}`),
		chunk(`{"tool_calls":[{"index":0,"function":{"name":"Read","arguments":"{\"file_path\":\"a\"}"}}]}`),
		chunk(`{"tool_calls":[{"index":1,"id":"g1","function":{"name":"Glob","arguments":"{\"pattern\":\"*\"}"}}]}`),
		chunk(`{"tool_calls":[{"index":1,"id":"g2","function":{"name":"Grep","arguments":{"pattern":"x"}}}]}`),
		chunk(`{"tool_calls":[{"index":2,"id":"bad","function":{"name":"Edit","arguments":"{\"file_path\":"}}]}`),
		chunk(`{}`, "stop"))
	type b struct{ typ, id, name, text, input string }
	var got []b
	for _, x := range resp.Blocks {
		got = append(got, b{x.Type, x.ID, x.Name, x.Text, string(x.Input)})
	}
	want := []b{
		{BlockThinking, "", "", "hmm", ""},
		{BlockText, "", "", "<think>x</think>ok", ""},
		{BlockToolUse, "call_0", "Read", "", `{"file_path":"a"}`},
		{BlockToolUse, "g1", "Glob", "", `{"pattern":"*"}`},
		{BlockToolUse, "g2", "Grep", "", `{"pattern":"x"}`},
		{BlockToolUse, "bad", "Edit", "", `{"_invalid_json":"{\"file_path\":"}`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("blocks\n got %+v\nwant %+v", got, want)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop %q", resp.StopReason)
	}
}

func TestChatStopReasons(t *testing.T) {
	for in, want := range map[string]string{"stop": StopEnd, "": StopEnd, "tool_calls": StopToolUse, "function_call": StopToolUse,
		"length": StopMax, "content_filter": StopRefusal, "eos": "eos"} {
		if got := chatStop(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	resp, _, _ := chatRun(t, ProviderConfig{}, Request{Model: "m"}, chunk(`{"content":"cut"}`), chunk(`{}`, "length"), "data: [DONE]\n\n")
	if resp.StopReason != StopMax {
		t.Errorf("stop %q", resp.StopReason)
	}
}

func TestChatStreamOptionsFallback(t *testing.T) {
	f := newFake(t, reply(400, `{"error":{"message":"Unrecognized request argument supplied: stream_options","type":"invalid_request_error","param":null,"code":null}}`),
		stream(chatText1...))
	c := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil)
	for range 2 {
		rec := &recorder{}
		resp, err := c.Stream(context.Background(), Request{Model: "m"}, rec.on)
		if err != nil || resp.Blocks[0].Text != "Hello" {
			t.Fatalf("%+v %v", resp, err)
		}
		if n := len(rec.kind(EvRetry)); n != 0 {
			t.Errorf("%d retries", n)
		}
	}
	got := f.requests()
	if len(got) != 3 {
		t.Fatalf("%d requests", len(got))
	}
	for i, r := range got {
		if has := strings.Contains(r.Body, "stream_options"); has != (i == 0) {
			t.Errorf("request %d: stream_options %v", i, has)
		}
	}
}

func TestChatStreamedErrors(t *testing.T) {
	// before any content: tried again
	f := newFake(t, stream(chunk(`{"role":"assistant"}`), data(`{"id":"c","error":{"message":"upstream failed","code":502}}`)), stream(chatText1...))
	rec := &recorder{}
	resp, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, rec.on)
	if err != nil || resp.Blocks[0].Text != "Hello" {
		t.Fatalf("%+v %v", resp, err)
	}
	if r := rec.kind(EvRetry); len(r) != 1 || r[0].Text != "502 bad gateway" {
		t.Errorf("retries %+v", r)
	}
	// after content: the caller's error
	f = newFake(t, stream(chunk(`{"content":"Half"}`), data(`{"error":{"message":"Internal error","type":"server_error"}}`)), stream(chatText1...))
	_, err = NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	if e := apiErr(t, err); e.Status != 500 || e.Message != "500 Internal error" || len(f.requests()) != 1 {
		t.Errorf("error %+v after %d requests", e, len(f.requests()))
	}
}

func TestChatErrors(t *testing.T) {
	for _, tc := range []struct {
		status   int
		body     string
		message  string
		overflow bool
	}{
		{401, `{"error":{"message":"Incorrect API key provided: sk-proj-secret.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`, "401 Incorrect API key provided: ….", false},
		{400, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`, "", true},
		{400, `{"error":{"message":"Please reduce the length of the messages.","type":"invalid_request_error","code":"context_length_exceeded"}}`, "", true},
		{400, `{"object":"error","message":"This model's maximum context length is 4096 tokens.","type":"BadRequestError","param":null,"code":400}`, "", true},
		{400, `{"error":{"message":"Requested tokens exceed Context Length of 32768","code":"invalid"}}`, "", true},
		{400, `{"error":{"message":"Invalid value for 'tool_choice'","type":"invalid_request_error"}}`, "400 Invalid value for 'tool_choice'", false},
	} {
		f := newFake(t, reply(tc.status, tc.body))
		_, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL, Key: "sk-proj-secret"}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
		e := apiErr(t, err)
		if e.Status != tc.status || e.ContextOverflow != tc.overflow || (tc.message != "" && e.Message != tc.message) {
			t.Errorf("%s: %+v", tc.body, e)
		}
	}
}
