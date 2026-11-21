package nextcode

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The Claude engine speaks Claude Code's headless protocol: `claude -p` with stream-json in
// and out, permission requests over the same pipe (--permission-prompt-tool stdio), the way
// Anthropic's Agent SDKs drive it. One JSON object per line each way.

// ClaudeArgs are the flags NextCode adds to the CLI's own launch (agent.Headless). mode is the
// permission mode to start in ("" = the CLI's).
func ClaudeArgs(mode string) []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--permission-prompt-tool", "stdio"}
	if mode != "" {
		args = append(args, "--permission-mode", mode)
	}
	return args
}

// PermissionModes are the modes the owner can switch a Claude NextCode console to.
var PermissionModes = []string{"default", "acceptEdits", "plan", "auto", "bypassPermissions"}

// ev is one effect of an output line on the conversation.
type ev struct {
	kind string
	// block / delta
	id, sub, name, parent, text string
	item                        *Item
	// tool result
	isError bool
	req     *Request
	// init
	conv, model, mode string
	commands          []string
	// result
	failed bool
	cost   float64
	usage  *Usage
	raw    json.RawMessage
	// reply is a line to write back to the CLI
	reply []byte
}

const (
	evBlock    = "block"
	evDelta    = "delta"
	evItem     = "item"
	evResult   = "toolResult"
	evRequest  = "request"
	evCancel   = "cancel"
	evInit     = "init"
	evCommands = "commands"
	evTurnEnd  = "turnEnd"
	evRate     = "rate"
	evNote     = "note"
	evNotice   = "notice"
	evActive   = "active"
	evReply    = "reply"
)

type claudeEngine struct {
	// streamed blocks of each API message, by index, and how many of them the CLI has since
	// given as whole messages: the k-th whole block of a message is its k-th streamed one
	streamed  map[string][]streamBlock
	finalized map[string]int
	reqN      int
}

type streamBlock struct{ id, kind string }

func newClaudeEngine() *claudeEngine {
	return &claudeEngine{streamed: map[string][]streamBlock{}, finalized: map[string]int{}}
}

func line(v any) []byte {
	b, _ := json.Marshal(v)
	return append(b, '\n')
}

func (e *claudeEngine) control(req map[string]any) []byte {
	e.reqN++
	return line(map[string]any{"type": "control_request", "request_id": "nb_" + strconv.Itoa(e.reqN), "request": req})
}

func (e *claudeEngine) argv(base []string, mode string) []string {
	return append(append([]string{}, base...), ClaudeArgs(mode)...)
}

// start is written when the CLI starts: the SDK's initialize. Claude Code resumes from its
// command line (--resume), so neither argument changes it.
func (e *claudeEngine) start(bool, string) []byte {
	return e.control(map[string]any{"subtype": "initialize", "hooks": nil})
}

func (e *claudeEngine) message(text string) []byte {
	return line(map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": []any{map[string]any{"type": "text", "text": text}}}, "parent_tool_use_id": nil, "session_id": ""})
}

func (e *claudeEngine) interrupt() []byte { return e.control(map[string]any{"subtype": "interrupt"}) }

func (e *claudeEngine) setModel(model string) []byte {
	var m any
	if model != "" {
		m = model
	}
	return e.control(map[string]any{"subtype": "set_model", "model": m})
}

func (e *claudeEngine) setMode(mode string) []byte {
	return e.control(map[string]any{"subtype": "set_permission_mode", "mode": mode})
}

// answer is the CLI's control_response to a request.
func (e *claudeEngine) answer(r *Request, a Answer) ([]byte, error) {
	var resp map[string]any
	deny := func(msg string) map[string]any {
		if msg == "" {
			msg = "The owner declined this in NextBoard."
		}
		m := map[string]any{"behavior": "deny", "message": msg}
		if a.Decision == DenyStop {
			m["interrupt"] = true
		}
		return m
	}
	switch a.Decision {
	case Deny, DenyStop:
		resp = deny(a.Message)
	case Allow, AllowAlways:
		input := r.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		resp = map[string]any{"behavior": "allow", "updatedInput": input}
		if r.Kind == ReqQuestion {
			var in map[string]any
			_ = json.Unmarshal(r.Input, &in)
			if in == nil {
				in = map[string]any{}
			}
			answers := map[string]any{}
			for q, labels := range a.Answers {
				if len(labels) == 1 {
					answers[q] = labels[0]
				} else {
					answers[q] = strings.Join(labels, ", ")
				}
			}
			in["answers"] = answers
			if a.Message != "" {
				in["response"] = a.Message
			}
			resp["updatedInput"] = in
		}
		if a.Decision == AllowAlways && len(r.suggestions) > 0 {
			resp["updatedPermissions"] = r.suggestions
		}
	default:
		return nil, fmt.Errorf("unknown decision %q", a.Decision)
	}
	return line(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": r.ID, "response": resp}}), nil
}

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

// decode turns one line of the CLI's output into effects.
func (e *claudeEngine) decode(b []byte) []ev {
	var l claudeLine
	if json.Unmarshal(b, &l) != nil {
		return nil
	}
	parent := ""
	if l.Parent != nil {
		parent = *l.Parent
	}
	switch l.Type {
	case "stream_event":
		return e.streamEvent(l.Event, parent)
	case "assistant":
		return e.assistant(l.Message, parent)
	case "user":
		return e.user(l.Message, parent)
	case "result":
		out := ev{kind: evTurnEnd, cost: l.TotalCost}
		if u := l.Usage; u != nil {
			out.usage = &Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
		}
		if l.IsError || (l.Subtype != "" && l.Subtype != "success") {
			out.failed = true
			out.text = l.Result
			if out.text == "" && len(l.Errors) > 0 {
				out.text = strings.Join(l.Errors, "\n")
			}
			if out.text == "" {
				out.text = strings.ReplaceAll(l.Subtype, "_", " ")
			}
			out.sub = l.Subtype
		}
		return []ev{out}
	case "system":
		switch l.Subtype {
		case "init":
			return []ev{{kind: evInit, conv: l.SessionID, model: l.Model, mode: l.PermissionMode}}
		case "api_retry":
			return []ev{{kind: evNote, text: fmt.Sprintf("Retrying (attempt %d of %d)…", l.Attempt, l.MaxRetries)}}
		case "compact_boundary":
			return []ev{{kind: evNotice, text: "The conversation was compacted."}}
		case "status":
			if l.Status == "compacting" {
				return []ev{{kind: evNote, text: "Compacting the conversation…"}}
			}
			return []ev{{kind: evNote}}
		}
	case "rate_limit_event":
		raw := l.RateLimitInfo
		if len(raw) == 0 {
			raw = json.RawMessage(b)
		}
		return []ev{{kind: evRate, raw: raw}}
	case "control_request":
		return e.controlRequest(l.RequestID, l.Request)
	case "control_cancel_request":
		return []ev{{kind: evCancel, id: l.RequestID}}
	case "control_response":
		var r struct {
			Subtype  string `json:"subtype"`
			Response struct {
				Commands []struct {
					Name string `json:"name"`
				} `json:"commands"`
			} `json:"response"`
		}
		if json.Unmarshal(l.Response, &r) == nil && len(r.Response.Commands) > 0 {
			names := make([]string, 0, len(r.Response.Commands))
			for _, c := range r.Response.Commands {
				if c.Name != "" {
					names = append(names, c.Name)
				}
			}
			return []ev{{kind: evCommands, commands: names}}
		}
	}
	return nil
}

func (e *claudeEngine) streamEvent(raw json.RawMessage, parent string) []ev {
	var s struct {
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
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	out := []ev{{kind: evActive}}
	switch s.Type {
	case "message_start":
		e.current(parent, s.Message.ID)
	case "content_block_start":
		msg := e.current(parent, "")
		kind := blockKind(s.ContentBlock.Type)
		if msg == "" || kind == "" {
			return out
		}
		id := fmt.Sprintf("%s/%d", msg, s.Index)
		blocks := e.streamed[msg]
		for len(blocks) <= s.Index {
			blocks = append(blocks, streamBlock{})
		}
		blocks[s.Index] = streamBlock{id: id, kind: kind}
		e.streamed[msg] = blocks
		out = append(out, ev{kind: evBlock, id: id, sub: kind, name: s.ContentBlock.Name, parent: parent})
	case "content_block_delta":
		msg := e.current(parent, "")
		blocks := e.streamed[msg]
		if s.Index >= len(blocks) || blocks[s.Index].id == "" {
			return out
		}
		b := blocks[s.Index]
		switch s.Delta.Type {
		case "text_delta":
			out = append(out, ev{kind: evDelta, id: b.id, sub: KindText, text: s.Delta.Text})
		case "thinking_delta":
			out = append(out, ev{kind: evDelta, id: b.id, sub: KindThinking, text: s.Delta.Thinking})
		case "input_json_delta":
			out = append(out, ev{kind: evDelta, id: b.id, sub: KindTool, text: s.Delta.PartialJSON})
		}
	}
	return out
}

// current is the message being streamed in the main conversation or a sub-agent; id sets it.
func (e *claudeEngine) current(parent, id string) string {
	key := "cur:" + parent
	if id != "" {
		e.streamed[key] = []streamBlock{{id: id}}
		return id
	}
	if c := e.streamed[key]; len(c) == 1 {
		return c[0].id
	}
	return ""
}

func blockKind(t string) string {
	switch t {
	case "text":
		return KindText
	case "thinking", "redacted_thinking":
		return KindThinking
	case "tool_use", "server_tool_use":
		return KindTool
	}
	return ""
}

func (e *claudeEngine) assistant(raw json.RawMessage, parent string) []ev {
	var m claudeMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var blocks []claudeBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	out := []ev{{kind: evActive}}
	for _, b := range blocks {
		kind := blockKind(b.Type)
		if kind == "" {
			if strings.HasSuffix(b.Type, "_tool_result") && b.ToolUseID != "" { // a server tool's result (web search)
				out = append(out, ev{kind: evResult, id: b.ToolUseID, text: toolOutput(b.Content)})
			}
			continue
		}
		// the stream id of this block: the k-th whole block of the message is its k-th streamed one
		stream := ""
		if m.ID != "" {
			k := e.finalized[m.ID]
			e.finalized[m.ID] = k + 1
			n := 0
			for _, sb := range e.streamed[m.ID] {
				if sb.id == "" {
					continue
				}
				if n == k && sb.kind == kind {
					stream = sb.id
				}
				n++
			}
		}
		it := &Item{Kind: kind, Parent: parent, Stream: stream}
		switch kind {
		case KindText:
			it.Text = b.Text
		case KindThinking:
			it.Text = b.Thinking
		case KindTool:
			it.ID = b.ID
			it.Tool = &Tool{Name: b.Name, Input: b.Input, Status: ToolRunning}
		}
		if it.ID == "" {
			it.ID = stream
			if it.ID == "" {
				it.ID = fmt.Sprintf("%s/w%d", m.ID, e.finalized[m.ID])
			}
		}
		out = append(out, ev{kind: evItem, item: it})
	}
	return out
}

func (e *claudeEngine) user(raw json.RawMessage, parent string) []ev {
	var m claudeMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		if parent != "" && text != "" {
			return []ev{{kind: evItem, item: &Item{Kind: KindPrompt, Text: text, Parent: parent}}}
		}
		return nil
	}
	var blocks []claudeBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []ev
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			out = append(out, ev{kind: evResult, id: b.ToolUseID, text: toolOutput(b.Content), isError: b.IsError})
		case "text":
			if parent != "" && b.Text != "" {
				out = append(out, ev{kind: evItem, item: &Item{Kind: KindPrompt, Text: b.Text, Parent: parent}})
			}
		}
	}
	return out
}

// toolOutput is a tool result's content as text.
func toolOutput(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			switch p.Type {
			case "text":
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			case "image":
				b.WriteString("[image]")
			}
		}
		return b.String()
	}
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

func (e *claudeEngine) controlRequest(id string, raw json.RawMessage) []ev {
	var r struct {
		Subtype        string          `json:"subtype"`
		ToolName       string          `json:"tool_name"`
		Input          json.RawMessage `json:"input"`
		ToolUseID      string          `json:"tool_use_id"`
		Suggestions    json.RawMessage `json:"permission_suggestions"`
		BlockedPath    string          `json:"blocked_path"`
		DecisionReason json.RawMessage `json:"decision_reason"`
		Title          string          `json:"title"`
		DisplayName    string          `json:"display_name"`
		Description    string          `json:"description"`
		AgentID        string          `json:"agent_id"`
	}
	if json.Unmarshal(raw, &r) != nil || id == "" {
		return nil
	}
	if r.Subtype != "can_use_tool" {
		// nothing of NextBoard's registered for it (hooks, SDK MCP servers): say so, or it waits
		return []ev{{kind: evReply, reply: line(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": id, "error": "NextBoard does not handle " + r.Subtype}})}}
	}
	q := &Request{ID: id, Kind: ReqPermission, Tool: r.ToolName, ToolUseID: r.ToolUseID, Input: r.Input, Title: r.Title,
		DisplayName: r.DisplayName, Description: r.Description, BlockedPath: r.BlockedPath, Reason: reasonText(r.DecisionReason)}
	switch r.ToolName {
	case "AskUserQuestion":
		q.Kind = ReqQuestion
	case "ExitPlanMode":
		q.Kind = ReqPlan
	}
	if s := r.Suggestions; len(s) > 0 && string(s) != "null" && string(s) != "[]" {
		q.suggestions = s
		q.CanRemember = true
	}
	return []ev{{kind: evRequest, req: q}}
}

// reasonText is the CLI's reason for asking, as text.
func reasonText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var r struct {
		Reason string `json:"reason"`
		Type   string `json:"type"`
	}
	if json.Unmarshal(raw, &r) == nil {
		if r.Reason != "" {
			return r.Reason
		}
		return r.Type
	}
	return ""
}

// toolSummary is a tool call's main argument, for the console's activity line.
func toolSummary(input json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "notebook_path", "path", "pattern", "url", "query", "skill", "description", "prompt"} {
		if s, ok := in[k].(string); ok && s != "" {
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[:i]
			}
			return s
		}
	}
	// Codex's file changes (Patch): the first file
	if ch, ok := in["changes"].([]any); ok && len(ch) > 0 {
		if c, ok := ch[0].(map[string]any); ok {
			s, _ := c["path"].(string)
			return s
		}
	}
	return ""
}
