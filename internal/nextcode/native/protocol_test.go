package native

import (
	"encoding/json"
	"strings"
	"testing"
)

// The structs below are copies of what internal/nextcode/claude.go decodes (claudeLine,
// claudeUsage, claudeBlock, claudeMessage, the streamEvent and controlRequest shapes): every
// line the agent writes must give the driver what it reads, under the same JSON names.

type claudeLine struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	SessionID      string          `json:"session_id"`
	Parent         *string         `json:"parent_tool_use_id"`
	Event          json.RawMessage `json:"event"`
	Message        json.RawMessage `json:"message"`
	RequestID      string          `json:"request_id"`
	Request        json.RawMessage `json:"request"`
	Response       json.RawMessage `json:"response"`
	Model          string          `json:"model"`
	PermissionMode string          `json:"permissionMode"`
	IsError        bool            `json:"is_error"`
	Result         string          `json:"result"`
	Errors         []string        `json:"errors"`
	TotalCost      float64         `json:"total_cost_usd"`
	Usage          *claudeUsage    `json:"usage"`
	RateLimitInfo  json.RawMessage `json:"rate_limit_info"`
	Attempt        int             `json:"attempt"`
	MaxRetries     int             `json:"max_retries"`
	Status         string          `json:"status"`
}

type claudeUsage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
}

type claudeStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
	ContentBlock claudeBlock `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

type claudeControlRequest struct {
	Subtype        string          `json:"subtype"`
	ToolName       string          `json:"tool_name"`
	Input          json.RawMessage `json:"input"`
	ToolUseID      string          `json:"tool_use_id"`
	Suggestions    json.RawMessage `json:"permission_suggestions"`
	BlockedPath    string          `json:"blocked_path"`
	DecisionReason json.RawMessage `json:"decision_reason"`
}

// checkDecodable fails when a line would not give claude.go's decode what it reads.
func checkDecodable(t *testing.T, raw []byte) {
	t.Helper()
	var l claudeLine
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatalf("a line claude.go cannot decode: %v: %s", err, raw)
	}
	bad := func(why string) { t.Fatalf("%s: %s", why, raw) }
	switch l.Type {
	case "stream_event":
		checkStreamEvent(t, l, raw)
	case "assistant":
		var m claudeMessage
		var blocks []claudeBlock
		if json.Unmarshal(l.Message, &m) != nil || m.ID == "" || json.Unmarshal(m.Content, &blocks) != nil || len(blocks) == 0 {
			bad("an assistant line without an id or content blocks")
		}
		for _, b := range blocks {
			switch b.Type {
			case "text", "thinking":
			case "tool_use":
				if b.Name == "" || !isObject(b.Input) {
					bad("a tool_use block without a name or an object input")
				}
			default:
				bad("an assistant block of an unknown type")
			}
		}
		if l.Parent != nil {
			bad("parent_tool_use_id is not null")
		}
	case "user":
		var m claudeMessage
		var blocks []claudeBlock
		if json.Unmarshal(l.Message, &m) != nil || json.Unmarshal(m.Content, &blocks) != nil || len(blocks) == 0 {
			bad("a user line without content blocks")
		}
		for _, b := range blocks {
			var s string
			if b.Type != "tool_result" || b.ToolUseID == "" || json.Unmarshal(b.Content, &s) != nil || s == "" {
				bad("a tool_result without its id or text content")
			}
		}
	case "result":
		if l.Usage == nil || l.SessionID == "" || (l.Subtype != "success" && l.Subtype != "error_during_execution") {
			bad("a result without usage, session or subtype")
		}
		if l.IsError != (l.Subtype != "success") {
			bad("is_error does not match the subtype")
		}
	case "system":
		switch l.Subtype {
		case "init":
			var tools struct {
				Tools []string `json:"tools"`
			}
			if l.SessionID == "" || l.Model == "" || l.PermissionMode == "" || json.Unmarshal(raw, &tools) != nil || len(tools.Tools) == 0 {
				bad("an init without session, model, mode or tools")
			}
		case "api_retry":
			if l.Attempt == 0 || l.MaxRetries == 0 {
				bad("an api_retry without attempt or max_retries")
			}
		case "status", "compact_boundary":
		default:
			bad("a system line of an unknown subtype")
		}
	case "control_request":
		var r claudeControlRequest
		if json.Unmarshal(l.Request, &r) != nil || l.RequestID == "" || r.Subtype != "can_use_tool" || r.ToolName == "" || r.ToolUseID == "" || !isObject(r.Input) {
			bad("a can_use_tool request without its id, tool, tool_use_id or input")
		}
		if len(r.DecisionReason) > 0 {
			var s string
			if json.Unmarshal(r.DecisionReason, &s) != nil || s == "" {
				bad("decision_reason is not a text")
			}
		}
		if len(r.Suggestions) > 0 {
			var s []map[string]any
			if json.Unmarshal(r.Suggestions, &s) != nil || len(s) == 0 {
				bad("permission_suggestions is not a list of updates")
			}
		}
	case "control_cancel_request":
		if l.RequestID == "" {
			bad("a cancel without request_id")
		}
	case "control_response":
		var r struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(l.Response, &r) != nil || r.RequestID == "" || (r.Subtype != "success" && r.Subtype != "error") {
			bad("a control_response without subtype or request_id")
		}
		if r.Subtype == "error" && r.Error == "" {
			bad("an error response without its error")
		}
	default:
		bad("a line of an unknown type")
	}
}

func checkStreamEvent(t *testing.T, l claudeLine, raw []byte) {
	t.Helper()
	var s claudeStreamEvent
	if json.Unmarshal(l.Event, &s) != nil {
		t.Fatalf("a stream event claude.go cannot decode: %s", raw)
	}
	switch s.Type {
	case "message_start":
		if s.Message.ID == "" {
			t.Fatalf("message_start without the message id: %s", raw)
		}
	case "content_block_start":
		switch s.ContentBlock.Type {
		case "text", "thinking":
		case "tool_use":
			if s.ContentBlock.Name == "" {
				t.Fatalf("a tool_use start without its name: %s", raw)
			}
		default:
			t.Fatalf("a block start of an unknown type: %s", raw)
		}
	case "content_block_delta":
		ok := s.Delta.Type == "text_delta" && s.Delta.Text != "" ||
			s.Delta.Type == "thinking_delta" && s.Delta.Thinking != "" ||
			s.Delta.Type == "input_json_delta" && s.Delta.PartialJSON != ""
		if !ok {
			t.Fatalf("a delta without its text: %s", raw)
		}
	case "content_block_stop", "message_stop":
	default:
		t.Fatalf("a stream event of an unknown type: %s", raw)
	}
	if l.Parent != nil {
		t.Fatalf("parent_tool_use_id is not null: %s", raw)
	}
}

func TestUserText(t *testing.T) {
	cases := map[string]string{
		`{"role":"user","content":"plain"}`: "plain",
		`{"role":"user","content":[{"type":"text","text":"a"},{"type":"image"},{"type":"text","text":"b"}]}`: "a\n\nb",
		`{"role":"user"}`: "",
		`not json`:        "",
	}
	for in, want := range cases {
		if got := userText(json.RawMessage(in)); got != want {
			t.Errorf("userText(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestParseAnswer(t *testing.T) {
	id, a := parseAnswer(json.RawMessage(`{"subtype":"success","request_id":"nc_3","response":{"behavior":"allow","updatedInput":{"command":"ls"},
		"updatedPermissions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"ls:*"}],"behavior":"allow","destination":"session"},{"type":"setMode","mode":"acceptEdits","destination":"session"}]}}`))
	if id != "nc_3" || !a.allow || string(a.input) != `{"command":"ls"}` || len(a.perms) != 2 || a.perms[1].Mode != modeAccept {
		t.Fatalf("allow: %q %+v", id, a)
	}
	id, a = parseAnswer(json.RawMessage(`{"subtype":"success","request_id":"nc_4","response":{"behavior":"deny","message":"no","interrupt":true,"updatedPermissions":"junk"}}`))
	if id != "nc_4" || a.allow || a.message != "no" || !a.interrupt || a.perms != nil {
		t.Fatalf("deny: %q %+v", id, a)
	}
	id, a = parseAnswer(json.RawMessage(`{"subtype":"error","request_id":"nc_5","error":"gone"}`))
	if id != "nc_5" || a.allow || a.message != "gone" {
		t.Fatalf("error: %q %+v", id, a)
	}
	// an input that is not an object is not used
	_, a = parseAnswer(json.RawMessage(`{"subtype":"success","request_id":"nc_6","response":{"behavior":"allow","updatedInput":[1]}}`))
	if a.input != nil {
		t.Fatalf("updatedInput [1] was taken: %s", a.input)
	}
}

func TestRedact(t *testing.T) {
	var out, log lockedBuf
	o := newOutput(&out, &log, testKey)
	o.send(map[string]any{"type": "result", "result": "the key is " + testKey})
	o.logf("failed with %s", testKey)
	if strings.Contains(out.String(), testKey) || strings.Contains(log.String(), testKey) {
		t.Fatalf("the key went out: %s / %s", out.String(), log.String())
	}
	if !strings.Contains(out.String(), "[redacted]") {
		t.Fatalf("no redaction: %s", out.String())
	}
	// a key with characters JSON escapes
	odd := `sk-"quoted"\back`
	out = lockedBuf{}
	o = newOutput(&out, &log, odd)
	o.send(map[string]any{"x": "value " + odd})
	if strings.Contains(out.String(), `sk-\"quoted\"\\back`) {
		t.Fatalf("the escaped key went out: %s", out.String())
	}
	// a short key is not redacted: it would match ordinary text
	if redact("type abc", "abc") != "type abc" {
		t.Fatal("a short key was redacted")
	}
}
