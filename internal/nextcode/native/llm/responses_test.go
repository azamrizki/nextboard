package llm

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const respMsgDone = `{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Hi there"}],"role":"assistant"}`

// respText1 is a short text answer, as OpenAI streams it.
var respText1 = []string{
	ev("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.1","output":[],"usage":null}}`),
	ev("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","content":[],"role":"assistant"}}`),
	ev("response.content_part.added", `{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"text":""}}`),
	ev("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hi","logprobs":[]}`),
	ev("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":" there","logprobs":[]}`),
	ev("response.output_text.done", `{"type":"response.output_text.done","sequence_number":5,"item_id":"msg_1","output_index":0,"content_index":0,"text":"Hi there"}`),
	ev("response.content_part.done", `{"type":"response.content_part.done","sequence_number":6,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"text":"Hi there"}}`),
	ev("response.output_item.done", `{"type":"response.output_item.done","sequence_number":7,"output_index":0,"item":`+respMsgDone+`}`),
	ev("response.completed", `{"type":"response.completed","sequence_number":8,"response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.1","output":[],"usage":{"input_tokens":50,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":53}}}`),
}

func respEv(typ, fields string) string {
	return ev(typ, `{"type":"`+typ+`",`+fields+`}`)
}

func respRun(t *testing.T, cfg ProviderConfig, req Request, events ...string) (*Response, *recorder, *fake) {
	t.Helper()
	f := newFake(t, stream(events...))
	cfg.Kind, cfg.API, cfg.BaseURL = "openai", "responses", f.srv.URL
	rec := &recorder{}
	resp, err := NewClient(cfg, nil).Stream(context.Background(), req, rec.on)
	if err != nil {
		t.Fatal(err)
	}
	checkEvents(t, rec.events, resp)
	return resp, rec, f
}

func TestResponsesText(t *testing.T) {
	req := Request{Model: "gpt-5.1", System: "Be brief.", Effort: "max", MaxTokens: 2000,
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "Hi <&>"}}}},
		Tools:    []ToolDef{{Name: "Bash", Description: "Runs a command.", Schema: json.RawMessage(`{"type":"object"}`)}}}
	resp, _, f := respRun(t, ProviderConfig{Key: "sk-r"}, req, respText1...)
	got := f.last(t)
	want := `{"model":"gpt-5.1","instructions":"Be brief.",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi <&>"}]}],` +
		`"tools":[{"type":"function","name":"Bash","description":"Runs a command.","parameters":{"type":"object"},"strict":false}],` +
		`"stream":true,"store":false,"include":["reasoning.encrypted_content"],` +
		`"reasoning":{"effort":"xhigh","summary":"auto"},"max_output_tokens":2000}`
	if got.Body != want {
		t.Errorf("body\n got %s\nwant %s", got.Body, want)
	}
	if got.Path != "/responses" || got.Header.Get("Authorization") != "Bearer sk-r" || got.Header.Get("User-Agent") != "NextBoard-NextCode" {
		t.Errorf("path %s, headers %v", got.Path, got.Header)
	}
	wantResp := &Response{ID: "resp_1", Model: "gpt-5.1", StopReason: StopEnd, Usage: Usage{Input: 50, Output: 3},
		Blocks: []Block{{Type: BlockText, Text: "Hi there", Raw: json.RawMessage(respMsgDone), Wire: WireResponses, Model: "gpt-5.1"}}}
	if !reflect.DeepEqual(resp, wantResp) {
		t.Errorf("response\n got %+v\nwant %+v", resp, wantResp)
	}
}

func TestResponsesBodyDefaults(t *testing.T) {
	b, err := responsesBody(Request{Model: "o4-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"model":"o4-mini","input":[],"stream":true,"store":false,"include":["reasoning.encrypted_content"]}`; string(b) != want {
		t.Errorf("body %s", b)
	}
	for in, want := range map[string]string{"low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "xhigh"} {
		if got := responsesEffort(in); got != want {
			t.Errorf("%q → %q", in, got)
		}
	}
}

func TestResponsesReasoningAndTwoTools(t *testing.T) {
	// spaced items: their Raw must be the bytes as given
	rs := `{"id": "rs_1", "type": "reasoning", "encrypted_content": "gAAAAB-x", "summary": [{"type": "summary_text", "text": "**Looking** at files"}, {"type": "summary_text", "text": "Then reading"}]}`
	fc1 := `{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"file_path\":\"/a.go\"}","call_id":"call_abc","name":"Read"}`
	fc2 := `{"id":"fc_2","type":"function_call","status":"completed","arguments":"{\"command\":\"ls\"}","call_id":"call_def","name":"Bash"}`
	events := []string{
		respEv("response.created", `"response":{"id":"resp_2","status":"in_progress","model":"gpt-5.1-codex"}`),
		respEv("response.output_item.added", `"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}`),
		respEv("response.reasoning_summary_part.added", `"item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}`),
		respEv("response.reasoning_summary_text.delta", `"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"**Looking**"`),
		respEv("response.reasoning_summary_text.delta", `"item_id":"rs_1","output_index":0,"summary_index":0,"delta":" at files"`),
		respEv("response.reasoning_summary_text.done", `"item_id":"rs_1","output_index":0,"summary_index":0,"text":"**Looking** at files"`),
		respEv("response.reasoning_summary_part.added", `"item_id":"rs_1","output_index":0,"summary_index":1,"part":{"type":"summary_text","text":""}`),
		respEv("response.reasoning_summary_text.delta", `"item_id":"rs_1","output_index":0,"summary_index":1,"delta":"Then reading"`),
		respEv("response.output_item.done", `"output_index":0,"item":`+rs),
		respEv("response.output_item.added", `"output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_abc","name":"Read"}`),
		respEv("response.function_call_arguments.delta", `"item_id":"fc_1","output_index":1,"delta":"{\"file_path\":"`),
		respEv("response.function_call_arguments.delta", `"item_id":"fc_1","output_index":1,"delta":"\"/a.go\"}"`),
		respEv("response.function_call_arguments.done", `"item_id":"fc_1","output_index":1,"arguments":"{\"file_path\":\"/a.go\"}"`),
		respEv("response.output_item.done", `"output_index":1,"item":`+fc1),
		respEv("response.output_item.added", `"output_index":2,"item":{"id":"fc_2","type":"function_call","status":"in_progress","arguments":"","call_id":"call_def","name":"Bash"}`),
		respEv("response.function_call_arguments.delta", `"item_id":"fc_2","output_index":2,"delta":"{\"command\":\"ls\"}"`),
		respEv("response.output_item.done", `"output_index":2,"item":`+fc2),
		respEv("response.completed", `"response":{"id":"resp_2","status":"completed","model":"gpt-5.1-codex","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800},"output_tokens":50,"output_tokens_details":{"reasoning_tokens":30}}}`),
	}
	model := "gpt-5.1-codex"
	resp, rec, _ := respRun(t, ProviderConfig{}, Request{Model: model}, events...)
	want := []Block{
		{Type: BlockThinking, Text: "**Looking** at files\n\nThen reading", Raw: json.RawMessage(rs)},
		{Type: BlockToolUse, ID: "call_abc", Name: "Read", Input: json.RawMessage(`{"file_path":"/a.go"}`), Raw: json.RawMessage(fc1)},
		{Type: BlockToolUse, ID: "call_def", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`), Raw: json.RawMessage(fc2)},
	}
	for i := range want {
		want[i].Wire, want[i].Model = WireResponses, model
	}
	if !reflect.DeepEqual(resp.Blocks, want) {
		t.Errorf("blocks\n got %s\nwant %s", mustJSON(t, resp.Blocks), mustJSON(t, want))
	}
	if string(resp.Blocks[0].Raw) != rs {
		t.Errorf("raw %s", resp.Blocks[0].Raw)
	}
	if resp.StopReason != StopToolUse || resp.Usage != (Usage{Input: 200, Output: 50, CacheRead: 800}) || resp.ID != "resp_2" || resp.Model != model {
		t.Errorf("stop %q, usage %+v, id %q, model %q", resp.StopReason, resp.Usage, resp.ID, resp.Model)
	}
	if s := rec.kind(EvBlockStart); len(s) != 3 || s[1].ToolID != "call_abc" || s[1].ToolName != "Read" {
		t.Errorf("starts %+v", s)
	}
}

func TestResponsesHiddenReasoningAndIgnoredItems(t *testing.T) {
	rs := `{"id":"rs_9","type":"reasoning","encrypted_content":"gAAAA-hidden","summary":[]}`
	resp, _, _ := respRun(t, ProviderConfig{}, Request{Model: "o4-mini"},
		respEv("response.output_item.added", `"output_index":0,"item":{"id":"rs_9","type":"reasoning","summary":[]}`),
		respEv("response.output_item.done", `"output_index":0,"item":`+rs),
		respEv("response.output_item.added", `"output_index":1,"item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}`),
		respEv("response.output_item.done", `"output_index":1,"item":{"id":"ws_1","type":"web_search_call","status":"completed"}`),
		// a server that skips the deltas: the text streams with the done item
		respEv("response.output_item.added", `"output_index":2,"item":{"id":"msg_2","type":"message","content":[],"role":"assistant"}`),
		respEv("response.output_item.done", `"output_index":2,"item":{"id":"msg_2","type":"message","content":[{"type":"output_text","text":"A"},{"type":"output_text","text":"B"}],"role":"assistant"}`),
		respEv("response.completed", `"response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":2}}`))
	if len(resp.Blocks) != 2 {
		t.Fatalf("blocks %s", mustJSON(t, resp.Blocks))
	}
	if b := resp.Blocks[0]; b.Type != BlockThinking || b.Text != "" || string(b.Raw) != rs {
		t.Errorf("reasoning %+v", b)
	}
	if b := resp.Blocks[1]; b.Type != BlockText || b.Text != "AB" {
		t.Errorf("text %+v", b)
	}
	if resp.StopReason != StopEnd {
		t.Errorf("stop %q", resp.StopReason)
	}
}

func TestResponsesReplay(t *testing.T) {
	model := "gpt-5.1-codex"
	rs := `{"id": "rs_1", "type": "reasoning", "encrypted_content": "gAAAAB-x", "summary": []}`
	msg := `{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"text":"On it."}],"role":"assistant"}`
	fc := `{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"file_path\":\"a.go\"}","call_id":"call_1","name":"Read"}`
	conv := func(thinkingModel string) []Message {
		return []Message{
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "Fix a.go"}}},
			{Role: RoleAssistant, Blocks: []Block{
				{Type: BlockThinking, Raw: json.RawMessage(rs), Wire: WireResponses, Model: thinkingModel},
				{Type: BlockText, Text: "On it.", Raw: json.RawMessage(msg), Wire: WireResponses, Model: thinkingModel},
				{Type: BlockToolUse, ID: "call_1", Name: "Read", Input: json.RawMessage(`{"file_path":"a.go"}`), Raw: json.RawMessage(fc), Wire: WireResponses, Model: thinkingModel},
			}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "and b"}, {Type: BlockToolResult, ToolUseID: "call_1", Text: "package a", IsError: true}}},
			// another wire's blocks are built from their fields, its thinking left out
			{Role: RoleAssistant, Blocks: []Block{
				{Type: BlockThinking, Text: "hmm", Raw: json.RawMessage(`{"type":"thinking"}`), Wire: WireAnthropic, Model: model},
				{Type: BlockToolUse, ID: "toolu_2", Name: "Bash", Raw: json.RawMessage(`{"type":"tool_use"}`), Wire: WireAnthropic},
			}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "toolu_2", Text: "ok"}}},
		}
	}
	tail := `{"type":"function_call_output","call_id":"call_1","output":"package a"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"and b"}]},` +
		`{"type":"function_call","call_id":"toolu_2","name":"Bash","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"toolu_2","output":"ok"}]`
	head := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"Fix a.go"}]},`

	got, err := responsesInput(conv(model), model)
	if err != nil {
		t.Fatal(err)
	}
	if want := head + rs + "," + msg + "," + fc + "," + tail; string(got) != want {
		t.Errorf("same model\n got %s\nwant %s", got, want)
	}

	// reasoning of another model is left out, and the rest of its answer loses its item ids
	got, err = responsesInput(conv("o3"), model)
	if err != nil {
		t.Fatal(err)
	}
	built := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"On it."}]},` +
		`{"type":"function_call","call_id":"call_1","name":"Read","arguments":"{\"file_path\":\"a.go\"}"},`
	if want := head + built + tail; string(got) != want {
		t.Errorf("another model\n got %s\nwant %s", got, want)
	}
}

func TestResponsesIncomplete(t *testing.T) {
	for reason, want := range map[string]string{"max_output_tokens": StopMax, "content_filter": StopRefusal} {
		resp, _, _ := respRun(t, ProviderConfig{}, Request{Model: "m"},
			respEv("response.output_item.added", `"output_index":0,"item":{"id":"fc_1","type":"function_call","arguments":"","call_id":"c1","name":"Write"}`),
			respEv("response.function_call_arguments.delta", `"output_index":0,"delta":"{\"file_path\":\"a\",\"content\":\"tr"`),
			respEv("response.incomplete", `"response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"`+reason+`"},"usage":{"input_tokens":5,"output_tokens":100}}`))
		if resp.StopReason != want {
			t.Errorf("%s: stop %q", reason, resp.StopReason)
		}
		if got := string(resp.Blocks[0].Input); got != `{"_invalid_json":"{\"file_path\":\"a\",\"content\":\"tr"}` {
			t.Errorf("%s: input %s", reason, got)
		}
	}
}

func TestResponsesRetries(t *testing.T) {
	ok := stream(respText1...)
	for _, tc := range []struct {
		name  string
		first string
		why   string
	}{
		{"response.failed", respEv("response.failed", `"response":{"id":"r","status":"failed","error":{"code":"server_error","message":"The server had an error"}}`), "500 server error"},
		{"error event", ev("error", `{"type":"error","code":"rate_limit_exceeded","message":"Rate limit reached","param":null,"sequence_number":1}`), "429 rate limited"},
	} {
		f := newFake(t, stream(respText1[0], tc.first), ok)
		rec := &recorder{}
		resp, err := NewClient(ProviderConfig{Kind: "openai", API: "responses", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, rec.on)
		if err != nil || resp.Blocks[0].Text != "Hi there" {
			t.Fatalf("%s: %+v %v", tc.name, resp, err)
		}
		if r := rec.kind(EvRetry); len(r) != 1 || r[0].Text != tc.why || r[0].Attempt != 2 {
			t.Errorf("%s: retries %+v", tc.name, r)
		}
	}
	// after content: the caller's error
	f := newFake(t, stream(append(append([]string{}, respText1[:4]...),
		respEv("response.failed", `"response":{"status":"failed","error":{"code":"server_error","message":"The server had an error"}}`))...), ok)
	_, err := NewClient(ProviderConfig{Kind: "openai", API: "responses", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	if e := apiErr(t, err); e.Status != 500 || e.Message != "500 The server had an error" || len(f.requests()) != 1 {
		t.Errorf("error %+v after %d requests", e, len(f.requests()))
	}
}

func TestResponsesErrors(t *testing.T) {
	f := newFake(t, reply(400, `{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","param":"input","code":"context_length_exceeded"}}`))
	_, err := NewClient(ProviderConfig{Kind: "openai", API: "responses", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	if e := apiErr(t, err); e.Status != 400 || !e.ContextOverflow || !strings.HasPrefix(e.Message, "400 Your input exceeds") {
		t.Errorf("error %+v", e)
	}

	f = newFake(t, stream(respEv("response.failed", `"response":{"status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`)))
	_, err = NewClient(ProviderConfig{Kind: "openai", API: "responses", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	if e := apiErr(t, err); e.Status != 400 || !e.ContextOverflow || len(f.requests()) != 1 {
		t.Errorf("error %+v after %d requests", e, len(f.requests()))
	}

	f = newFake(t, reply(401, `{"error":{"message":"Incorrect API key provided: sk-live-777","type":"invalid_request_error","code":"invalid_api_key"}}`))
	_, err = NewClient(ProviderConfig{Kind: "openai", API: "responses", BaseURL: f.srv.URL, Key: "sk-live-777"}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	if e := apiErr(t, err); e.Message != "401 Incorrect API key provided: …" || e.ContextOverflow {
		t.Errorf("error %+v", e)
	}
}
