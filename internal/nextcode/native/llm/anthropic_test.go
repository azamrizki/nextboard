package llm

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// antText1 is a short text answer, as Anthropic streams it.
var antText1 = []string{
	ev("message_start", `{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000,"output_tokens":1}}}`),
	ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
	ev("ping", `{"type": "ping"}`),
	ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
	ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", <world> & co"}}`),
	ev("content_block_stop", `{"type":"content_block_stop","index":0}`),
	ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":7}}`),
	ev("message_stop", `{"type":"message_stop"}`),
}

func antStart(i int, block string) string {
	return ev("content_block_start", `{"type":"content_block_start","index":`+itoa(i)+`,"content_block":`+block+`}`)
}

func antDelta(i int, delta string) string {
	return ev("content_block_delta", `{"type":"content_block_delta","index":`+itoa(i)+`,"delta":`+delta+`}`)
}

func antStop(i int) string {
	return ev("content_block_stop", `{"type":"content_block_stop","index":`+itoa(i)+`}`)
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func antEnd(stop, usage string) []string {
	return []string{ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+stop+`","stop_sequence":null},"usage":`+usage+`}`),
		ev("message_stop", `{"type":"message_stop"}`)}
}

const antMsgStart = `{"type":"message_start","message":{"id":"msg_02","type":"message","role":"assistant","model":"glm-4.6","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}`

func antRun(t *testing.T, cfg ProviderConfig, req Request, events ...string) (*Response, *recorder, *fake) {
	t.Helper()
	f := newFake(t, stream(events...))
	cfg.Kind, cfg.BaseURL = "anthropic", f.srv.URL
	rec := &recorder{}
	resp, err := NewClient(cfg, nil).Stream(context.Background(), req, rec.on)
	if err != nil {
		t.Fatal(err)
	}
	checkEvents(t, rec.events, resp)
	return resp, rec, f
}

func TestAnthropicText(t *testing.T) {
	req := Request{Model: "claude-opus-5-5", System: "Be brief.",
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "Hi <there>"}}}},
		Tools: []ToolDef{{Name: "Read", Description: "Reads a file.", Schema: json.RawMessage(`{"type":"object"}`)},
			{Name: "Bash", Description: "Runs a command.", Schema: json.RawMessage(`{"type": "object", "properties": {"command": {"type": "string"}}}`)}}}
	resp, _, f := antRun(t, ProviderConfig{Key: "sk-ant-1", Auth: "x-api-key"}, req, antText1...)

	got := f.last(t)
	want := `{"model":"claude-opus-5-5","max_tokens":32000,` +
		`"system":[{"type":"text","text":"Be brief.","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"Hi <there>","cache_control":{"type":"ephemeral"}}]}],` +
		`"tools":[{"name":"Read","description":"Reads a file.","input_schema":{"type":"object"}},` +
		`{"name":"Bash","description":"Runs a command.","input_schema":{"type":"object","properties":{"command":{"type":"string"}}},"cache_control":{"type":"ephemeral"}}],` +
		`"stream":true}`
	if got.Body != want {
		t.Errorf("body\n got %s\nwant %s", got.Body, want)
	}
	if got.Path != "/v1/messages" {
		t.Errorf("path %s", got.Path)
	}
	for k, v := range map[string]string{"anthropic-version": "2023-06-01", "x-api-key": "sk-ant-1", "Authorization": "",
		"Content-Type": "application/json", "User-Agent": "NextBoard-NextCode"} {
		if got.Header.Get(k) != v {
			t.Errorf("header %s = %q, want %q", k, got.Header.Get(k), v)
		}
	}

	wantResp := &Response{ID: "msg_01", Model: "claude-opus-5-5", StopReason: StopEnd,
		Usage: Usage{Input: 12, Output: 7, CacheRead: 2000, CacheWrite: 100},
		Blocks: []Block{{Type: BlockText, Text: "Hello, <world> & co", Raw: json.RawMessage(`{"type":"text","text":"Hello, <world> & co"}`),
			Wire: WireAnthropic, Model: "claude-opus-5-5"}}}
	if !reflect.DeepEqual(resp, wantResp) {
		t.Errorf("response\n got %+v\nwant %+v", resp, wantResp)
	}
}

func TestAnthropicAuth(t *testing.T) {
	for _, tc := range []struct {
		cfg          ProviderConfig
		bearer, xkey string
	}{
		{ProviderConfig{Key: "sk-1"}, "Bearer sk-1", ""},
		{ProviderConfig{Key: "sk-1", Auth: "bearer"}, "Bearer sk-1", ""},
		{ProviderConfig{Key: "sk-1", Auth: "x-api-key"}, "", "sk-1"},
		{ProviderConfig{}, "", ""},
		{ProviderConfig{Auth: "x-api-key"}, "", ""},
	} {
		_, _, f := antRun(t, tc.cfg, Request{Model: "claude-opus-5-5"}, antText1...)
		h := f.last(t).Header
		if h.Get("Authorization") != tc.bearer || h.Get("x-api-key") != tc.xkey || len(h.Values("x-api-key")) > 1 {
			t.Errorf("%+v: Authorization %q, x-api-key %q", tc.cfg, h.Get("Authorization"), h.Get("x-api-key"))
		}
		if _, ok := h["X-Api-Key"]; tc.xkey == "" && ok {
			t.Errorf("%+v: an empty x-api-key was sent", tc.cfg)
		}
	}
}

func TestAnthropicThinkingAndTwoTools(t *testing.T) {
	events := []string{ev("message_start", antMsgStart),
		antStart(0, `{"type":"thinking","thinking":"","signature":""}`),
		antDelta(0, `{"type":"thinking_delta","thinking":"Let me look"}`),
		antDelta(0, `{"type":"thinking_delta","thinking":" at it."}`),
		antDelta(0, `{"type":"signature_delta","signature":"sig-abc"}`),
		antStop(0),
		antStart(1, `{"type":"text","text":""}`),
		antDelta(1, `{"type":"text_delta","text":"Reading both."}`),
		antDelta(1, `{"type":"citations_delta","citation":{"type":"char_location"}}`),
		antStop(1),
		antStart(2, `{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}`),
		antDelta(2, `{"type":"input_json_delta","partial_json":"{\"query\":\"x\"}"}`),
		antStop(2),
		antStart(3, `{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}`),
		antDelta(3, `{"type":"input_json_delta","partial_json":""}`),
		antDelta(3, `{"type":"input_json_delta","partial_json":"{\"file_path\": \"/a.go\""}`),
		antDelta(3, `{"type":"input_json_delta","partial_json":"}"}`),
		antStop(3),
		antStart(4, `{"type":"tool_use","id":"toolu_2","name":"Bash","input":{}}`),
		antDelta(4, `{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}`),
		antStop(4),
	}
	events = append(events, antEnd("tool_use", `{"input_tokens":300,"cache_read_input_tokens":50,"output_tokens":90}`)...)
	resp, rec, _ := antRun(t, ProviderConfig{}, Request{Model: "glm-4.6"}, events...)

	want := []Block{
		{Type: BlockThinking, Text: "Let me look at it.", Raw: json.RawMessage(`{"type":"thinking","thinking":"Let me look at it.","signature":"sig-abc"}`)},
		{Type: BlockText, Text: "Reading both.", Raw: json.RawMessage(`{"type":"text","text":"Reading both."}`)},
		{Type: BlockToolUse, ID: "toolu_1", Name: "Read", Input: json.RawMessage(`{"file_path":"/a.go"}`),
			Raw: json.RawMessage(`{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/a.go"}}`)},
		{Type: BlockToolUse, ID: "toolu_2", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`),
			Raw: json.RawMessage(`{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"ls"}}`)},
	}
	for i := range want {
		want[i].Wire, want[i].Model = WireAnthropic, "glm-4.6"
	}
	if !reflect.DeepEqual(resp.Blocks, want) {
		t.Errorf("blocks\n got %s\nwant %s", mustJSON(t, resp.Blocks), mustJSON(t, want))
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop %q", resp.StopReason)
	}
	if resp.Usage != (Usage{Input: 300, Output: 90, CacheRead: 50}) {
		t.Errorf("usage %+v", resp.Usage)
	}
	starts := rec.kind(EvBlockStart)
	if len(starts) != 4 || starts[2].ToolID != "toolu_1" || starts[2].ToolName != "Read" || starts[0].BlockType != BlockThinking {
		t.Errorf("starts %+v", starts)
	}
}

func TestAnthropicRedactedThinking(t *testing.T) {
	block := `{"type": "redacted_thinking", "data": "EmwKAhgBEgy3va3pzix"}`
	events := append([]string{ev("message_start", antMsgStart), antStart(0, block), antStop(0),
		antStart(1, `{"type":"text","text":""}`), antDelta(1, `{"type":"text_delta","text":"ok"}`), antStop(1)},
		antEnd("end_turn", `{"output_tokens":3}`)...)
	resp, _, _ := antRun(t, ProviderConfig{}, Request{Model: "claude-opus-5-5"}, events...)
	b := resp.Blocks[0]
	if b.Type != BlockThinking || b.Text != "" || string(b.Raw) != block || b.Wire != WireAnthropic || b.Model != "claude-opus-5-5" {
		t.Errorf("block %+v (raw %s)", b, b.Raw)
	}
}

func TestAnthropicToolInputs(t *testing.T) {
	events := append([]string{ev("message_start", antMsgStart),
		antStart(0, `{"type":"tool_use","id":"toolu_1","name":"Edit","input":{}}`),
		antDelta(0, `{"type":"input_json_delta","partial_json":"{\"file_path\": \"a.go\", \"old"}`),
		antStop(0),
		// a gateway that gives the whole input at the start
		antStart(1, `{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"ls"}}`),
		antStop(1),
		antStart(2, `{"type":"tool_use","id":"toolu_3","name":"TodoWrite","input":{}}`),
		antStop(2),
	}, antEnd("max_tokens", `{"output_tokens":3}`)...)
	resp, _, _ := antRun(t, ProviderConfig{}, Request{Model: "m"}, events...)
	for i, want := range []string{`{"_invalid_json":"{\"file_path\": \"a.go\", \"old"}`, `{"command":"ls"}`, `{}`} {
		if got := string(resp.Blocks[i].Input); got != want {
			t.Errorf("input %d = %s, want %s", i, got, want)
		}
	}
	if resp.StopReason != StopMax {
		t.Errorf("stop %q", resp.StopReason)
	}
}

func TestAnthropicUsageOnlyInMessageDeltaAndNoMessageStop(t *testing.T) {
	events := []string{ev("message_start", `{"type":"message_start","message":{"id":"m","model":"kimi-k2"}}`),
		antStart(0, `{"type":"text","text":""}`), antDelta(0, `{"type":"text_delta","text":"hi"}`), antStop(0),
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":40,"output_tokens":2,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}`),
	}
	resp, _, _ := antRun(t, ProviderConfig{}, Request{Model: "kimi-k2"}, events...)
	if resp.Usage != (Usage{Input: 40, Output: 2, CacheRead: 10, CacheWrite: 5}) || resp.Blocks[0].Text != "hi" {
		t.Errorf("%+v", resp)
	}
}

func TestAnthropicStopReasons(t *testing.T) {
	for in, want := range map[string]string{"end_turn": StopEnd, "tool_use": StopToolUse, "max_tokens": StopMax,
		"refusal": StopRefusal, "stop_sequence": StopEnd, "pause_turn": StopEnd, "": StopEnd,
		"model_context_window_exceeded": "model_context_window_exceeded"} {
		if got := anthropicStop(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func antBody(t *testing.T, req Request) map[string]any {
	t.Helper()
	b, err := anthropicBody(req, false)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"temperature", "top_p", "top_k", "cache_control"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s was sent", k)
		}
	}
	return m
}

func TestAnthropicThinkingAndEffort(t *testing.T) {
	for _, tc := range []struct {
		model, effort string
		maxTokens     int
		thinking      string
		output        string
		wantMax       float64
	}{
		{"claude-opus-5-5", "high", 0, `{"display":"summarized","type":"adaptive"}`, `{"effort":"high"}`, 32000},
		{"claude-sonnet-5-5", "max", 0, `{"display":"summarized","type":"adaptive"}`, `{"effort":"max"}`, 32000},
		{"claude-opus-5-5", "", 0, "", "", 32000},
		{"claude-haiku-5-5", "low", 1000, `{"display":"summarized","type":"adaptive"}`, `{"effort":"low"}`, 1000},
		{"glm-4.6", "high", 0, `{"budget_tokens":20000,"type":"enabled"}`, "", 28192},
		{"glm-4.6", "low", 64000, `{"budget_tokens":4096,"type":"enabled"}`, "", 64000},
		{"kimi-k2", "medium", 0, `{"budget_tokens":10000,"type":"enabled"}`, "", 18192},
		{"deepseek-chat", "xhigh", 0, `{"budget_tokens":32000,"type":"enabled"}`, "", 40192},
		{"MiniMax-M2", "max", 0, `{"budget_tokens":32000,"type":"enabled"}`, "", 40192},
		{"glm-4.6", "", 0, "", "", 16384},
	} {
		m := antBody(t, Request{Model: tc.model, Effort: tc.effort, MaxTokens: tc.maxTokens})
		got := func(k string) string {
			if v, ok := m[k]; ok {
				return mustJSON(t, v)
			}
			return ""
		}
		if got("thinking") != tc.thinking || got("output_config") != tc.output || m["max_tokens"] != tc.wantMax {
			t.Errorf("%s/%s: thinking %s, output_config %s, max_tokens %v", tc.model, tc.effort, got("thinking"), got("output_config"), m["max_tokens"])
		}
	}
}

func TestAnthropicReplay(t *testing.T) {
	thinking := `{"type":"thinking", "thinking":"hmm","signature":"s1"}` // spaced: it must go out byte for byte
	tool := `{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"a.go"}}`
	model := "claude-opus-5-5"
	req := Request{Model: model, Messages: []Message{
		{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "Fix a.go"}}},
		{Role: RoleAssistant, Blocks: []Block{
			{Type: BlockThinking, Text: "hmm", Raw: json.RawMessage(thinking), Wire: WireAnthropic, Model: model},
			{Type: BlockText, Text: "On it.", Raw: json.RawMessage(`{"type":"text","text":"On it."}`), Wire: WireAnthropic, Model: model},
			{Type: BlockToolUse, ID: "toolu_1", Name: "Read", Input: json.RawMessage(`{"file_path":"a.go"}`), Raw: json.RawMessage(tool), Wire: WireAnthropic, Model: model},
		}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "also b"}, {Type: BlockToolResult, ToolUseID: "toolu_1", Text: "package a"}}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "and c"}}},
		// thinking of another model, of another wire, and empty text: nothing left
		{Role: RoleAssistant, Blocks: []Block{
			{Type: BlockThinking, Text: "old", Raw: json.RawMessage(`{"type":"thinking","thinking":"old","signature":"s0"}`), Wire: WireAnthropic, Model: "claude-sonnet-4-5"},
			{Type: BlockThinking, Text: "chat thinking", Wire: WireChat, Model: model},
			{Type: BlockText, Text: ""},
		}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "toolu_2", Text: "boom", IsError: true}, {Type: BlockText, Text: ""}}},
		// blocks of another wire are built from their fields
		{Role: RoleAssistant, Blocks: []Block{
			{Type: BlockText, Text: "x", Wire: WireChat},
			{Type: BlockToolUse, ID: "call_0", Name: "Bash", Wire: WireChat},
		}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_0", Text: ""}}},
	}}
	b, err := anthropicBody(req, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"claude-opus-5-5","max_tokens":32000,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"Fix a.go"}]},` +
		`{"role":"assistant","content":[` + thinking + `,{"type":"text","text":"On it."},` + tool + `]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"package a"},{"type":"text","text":"also b"},{"type":"text","text":"and c"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"(no answer)"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"boom","is_error":true}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"x"},{"type":"tool_use","id":"call_0","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_0","content":"","cache_control":{"type":"ephemeral"}}]}` +
		`],"stream":true}`
	if string(b) != want {
		t.Errorf("body\n got %s\nwant %s", b, want)
	}
}

func TestAnthropicCacheBreakpoints(t *testing.T) {
	req := Request{Model: "claude-opus-5-5", System: "sys",
		Tools: []ToolDef{{Name: "A"}, {Name: "B"}, {Name: "C"}},
		Messages: []Message{
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "1"}}},
			{Role: RoleAssistant, Blocks: []Block{{Type: BlockText, Text: "2"}}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "t", Text: "3"}, {Type: BlockText, Text: "4"}}},
		}}
	m := antBody(t, req)
	b := mustJSON(t, m)
	if n := strings.Count(b, "cache_control"); n != 3 {
		t.Errorf("%d breakpoints: %s", n, b)
	}
	tools := m["tools"].([]any)
	if _, ok := tools[2].(map[string]any)["cache_control"]; !ok {
		t.Error("the last tool has no breakpoint")
	}
	if !strings.Contains(mustJSON(t, tools[2]), `"input_schema":{"properties":{},"type":"object"}`) {
		t.Errorf("an empty schema went out as %s", mustJSON(t, tools[2]))
	}
	msgs := m["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)["content"].([]any)
	if _, ok := last[len(last)-1].(map[string]any)["cache_control"]; !ok {
		t.Errorf("the last block has no breakpoint: %s", mustJSON(t, last))
	}
}

func TestAnthropicRetries(t *testing.T) {
	ok := stream(antText1...)
	for _, tc := range []struct {
		name  string
		first func() *fake
		why   string
	}{
		{"429 with Retry-After 0", func() *fake {
			return newFake(t, reply(429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, "Retry-After", "0"), ok)
		}, "429 rate limited"},
		{"529", func() *fake {
			return newFake(t, reply(529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), ok)
		}, "529 overloaded"},
		{"a streamed overload before any content", func() *fake {
			return newFake(t, stream(ev("message_start", antMsgStart), ev("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)), ok)
		}, "529 overloaded"},
		{"a streamed api_error", func() *fake {
			return newFake(t, stream(ev("error", `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`)), ok)
		}, "500 server error"},
	} {
		f := tc.first()
		rec := &recorder{}
		resp, err := NewClient(ProviderConfig{Kind: "anthropic", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, rec.on)
		if err != nil || resp.Blocks[0].Text != "Hello, <world> & co" {
			t.Fatalf("%s: %+v %v", tc.name, resp, err)
		}
		retries := rec.kind(EvRetry)
		if len(retries) != 1 || retries[0] != (Event{Kind: EvRetry, Attempt: 2, MaxAttempts: 4, Text: tc.why}) {
			t.Errorf("%s: retries %+v", tc.name, retries)
		}
		if n := len(f.requests()); n != 2 {
			t.Errorf("%s: %d requests", tc.name, n)
		}
		checkEvents(t, rec.events, resp)
	}
}

func TestAnthropicNoRetryOnceContentStreamed(t *testing.T) {
	f := newFake(t, stream(ev("message_start", antMsgStart), antStart(0, `{"type":"text","text":""}`),
		antDelta(0, `{"type":"text_delta","text":"Half"}`),
		ev("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)), stream(antText1...))
	rec := &recorder{}
	_, err := NewClient(ProviderConfig{Kind: "anthropic", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, rec.on)
	e := apiErr(t, err)
	if e.Status != 529 || e.Message != "529 Overloaded" {
		t.Errorf("error %+v", e)
	}
	if len(f.requests()) != 1 || len(rec.kind(EvRetry)) != 0 {
		t.Errorf("%d requests, %d retries", len(f.requests()), len(rec.kind(EvRetry)))
	}
}

func TestAnthropicErrors(t *testing.T) {
	for _, tc := range []struct {
		status   int
		body     string
		message  string
		overflow bool
	}{
		{401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key: sk-ant-secret-9"}}`, "401 invalid x-api-key: …", false},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`, "400 prompt is too long: 210000 tokens > 200000 maximum", true},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"input length and ` + "`max_tokens`" + ` exceed context limit: 197000 + 21333 > 200000"}}`, "", true},
		{413, `{"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum allowed number of bytes."}}`, "", true},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 200000 > 128000, which is the maximum allowed number of output tokens for claude-opus-5-5"}}`, "", false},
		{404, "<html>\n  <body>Not   Found</body>\n</html>", "404 <html> <body>Not Found</body> </html>", false},
	} {
		f := newFake(t, reply(tc.status, tc.body))
		_, err := NewClient(ProviderConfig{Kind: "anthropic", BaseURL: f.srv.URL, Key: "sk-ant-secret-9"}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
		e := apiErr(t, err)
		if e.Status != tc.status || e.ContextOverflow != tc.overflow || (tc.message != "" && e.Message != tc.message) {
			t.Errorf("%d %s: %+v", tc.status, tc.body, e)
		}
		if strings.Contains(e.Message, "sk-ant-secret-9") {
			t.Errorf("the key is in %q", e.Message)
		}
	}
}

// Anthropic's own API streams the tools' input as it is written; a gateway gets no such field.
func TestAnthropicEagerInputOnlyOnAnthropic(t *testing.T) {
	req := Request{Model: "claude-opus-5-5", Tools: []ToolDef{{Name: "Write", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}}}
	for _, official := range []bool{true, false} {
		b, err := anthropicBody(req, official)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(b), `"eager_input_streaming":true`); got != official {
			t.Errorf("official %v: eager_input_streaming %v in %s", official, got, b)
		}
	}
	for base, want := range map[string]bool{"https://api.anthropic.com": true, "https://api.anthropic.com/": true, "https://api.z.ai/api/anthropic": false} {
		a := NewClient(ProviderConfig{Kind: "anthropic", BaseURL: base}, nil).(*anthropicClient)
		if got := a.official(); got != want {
			t.Errorf("%s: official %v, want %v", base, got, want)
		}
	}
}
