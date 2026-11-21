package native

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
)

// The agent's side of Claude Code's stream-json protocol: the lines internal/nextcode's Claude
// engine decodes (claude.go's decode), and the lines its engine methods write to us. One JSON
// object per line each way.

// minSecret is the shortest key that is redacted from the output: a shorter one would match
// ordinary text and break the lines.
const minSecret = 8

// output writes the agent's lines to the driver, and short diagnostics to stderr. The key
// never goes out: every line is checked for it.
type output struct {
	mu      sync.Mutex
	w       io.Writer
	log     io.Writer
	secrets [][]byte
}

func newOutput(w, log io.Writer, key string) *output {
	o := &output{w: w, log: log}
	if len(key) >= minSecret {
		o.secrets = append(o.secrets, []byte(key))
		if b, err := json.Marshal(key); err == nil && string(b[1:len(b)-1]) != key {
			o.secrets = append(o.secrets, b[1:len(b)-1]) // as it is written inside a JSON string
		}
	}
	return o
}

// redact takes the key out of a text.
func redact(s, key string) string {
	if len(key) < minSecret {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}

func (o *output) scrub(b []byte) []byte {
	for _, s := range o.secrets {
		b = bytes.ReplaceAll(b, s, []byte("[redacted]"))
	}
	return b
}

// send writes one line.
func (o *output) send(v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		o.logf("cannot write a line: %v", err)
		return
	}
	b := o.scrub(buf.Bytes())
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = o.w.Write(b)
}

// logf writes a diagnostic to stderr.
func (o *output) logf(format string, args ...any) {
	s := strings.TrimRight(fmt.Sprintf("nextcode-agent: "+format, args...), "\n") + "\n"
	b := o.scrub([]byte(s))
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = o.log.Write(b)
}

// ---- output lines

// initLine is the system init: the conversation, the model, the mode and the tools.
func (o *output) initLine(session, model, mode string, tools []string) {
	o.send(map[string]any{"type": "system", "subtype": "init", "session_id": session, "model": model,
		"permissionMode": mode, "tools": tools})
}

// stream is one event of an answer as it streams.
func (o *output) stream(event map[string]any) {
	o.send(map[string]any{"type": "stream_event", "event": event, "parent_tool_use_id": nil})
}

// assistant is a whole answer: its blocks in the order they streamed.
func (o *output) assistant(id, model string, content []any) {
	o.send(map[string]any{"type": "assistant", "message": map[string]any{"id": id, "role": "assistant", "model": model,
		"content": content}, "parent_tool_use_id": nil})
}

// toolResult is the result of one tool call.
func (o *output) toolResult(id, text string, isError bool) {
	o.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": id, "content": text, "is_error": isError}}},
		"parent_tool_use_id": nil})
}

// request asks the driver (the owner) something.
func (o *output) request(id string, req map[string]any) {
	o.send(map[string]any{"type": "control_request", "request_id": id, "request": req})
}

// cancelRequest withdraws a request that is no longer wanted.
func (o *output) cancelRequest(id string) {
	o.send(map[string]any{"type": "control_cancel_request", "request_id": id})
}

// success answers a control request of the driver's; resp nil = nothing to say.
func (o *output) success(id string, resp any) {
	r := map[string]any{"subtype": "success", "request_id": id}
	if resp != nil {
		r["response"] = resp
	}
	o.send(map[string]any{"type": "control_response", "response": r})
}

// failure answers a control request the agent cannot do.
func (o *output) failure(id, msg string) {
	o.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": id, "error": msg}})
}

// retry tells that a call failed and is tried again.
func (o *output) retry(attempt, max int) {
	o.send(map[string]any{"type": "system", "subtype": "api_retry", "attempt": attempt, "max_retries": max})
}

// status is a passing state: "compacting", or nil when it is over.
func (o *output) status(s any) {
	o.send(map[string]any{"type": "system", "subtype": "status", "status": s})
}

// boundary marks where a compacted conversation starts again.
func (o *output) boundary() {
	o.send(map[string]any{"type": "system", "subtype": "compact_boundary"})
}

// result ends a turn. errText "" = a success (a stopped turn too).
func (o *output) result(session, model, text, errText string, u llm.Usage) {
	sub, isError := "success", false
	if errText != "" {
		sub, isError, text = "error_during_execution", true, errText
	}
	o.send(map[string]any{"type": "result", "subtype": sub, "is_error": isError, "result": text, "session_id": session,
		"model": model, "usage": map[string]any{"input_tokens": u.Input, "output_tokens": u.Output,
			"cache_read_input_tokens": u.CacheRead, "cache_creation_input_tokens": u.CacheWrite}, "total_cost_usd": 0})
}

// content is an answer's blocks as Claude's message content.
func content(blocks []llm.Block) []any {
	out := make([]any, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case llm.BlockText:
			out = append(out, map[string]any{"type": "text", "text": b.Text})
		case llm.BlockThinking:
			out = append(out, map[string]any{"type": "thinking", "thinking": b.Text})
		case llm.BlockToolUse:
			out = append(out, map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": objectOr(b.Input)})
		default:
			out = append(out, map[string]any{"type": b.Type})
		}
	}
	return out
}

// objectOr is a tool call's input when it is a JSON object, else an empty one.
func objectOr(in json.RawMessage) json.RawMessage {
	if isObject(in) {
		return in
	}
	return json.RawMessage(`{}`)
}

// isObject reports whether raw is a JSON object.
func isObject(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
		return false
	}
	return true
}

// ---- input lines

// inLine is one line from the driver.
type inLine struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
	Message   json.RawMessage `json:"message"`
}

// userText is the text of the owner's message: its content is a string or text blocks.
func userText(raw json.RawMessage) string {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ownerAnswer is the owner's answer to one of the agent's requests.
type ownerAnswer struct {
	allow     bool
	input     json.RawMessage // updatedInput (nil = the call's own)
	perms     []permUpdate
	message   string
	interrupt bool
}

// permUpdate is one of Claude Code's permission updates: the rules the agent offered
// (permission_suggestions), given back when the owner allows calls like this one from now on.
type permUpdate struct {
	Type        string     `json:"type"`
	Rules       []ruleJSON `json:"rules,omitempty"`
	Behavior    string     `json:"behavior,omitempty"`
	Mode        string     `json:"mode,omitempty"`
	Destination string     `json:"destination"`
}

type ruleJSON struct {
	ToolName    string `json:"toolName"`
	RuleContent string `json:"ruleContent,omitempty"`
}

// parseAnswer reads the driver's control_response to a request: its id and the answer.
func parseAnswer(raw json.RawMessage) (string, ownerAnswer) {
	var r struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Error     string          `json:"error"`
		Response  json.RawMessage `json:"response"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", ownerAnswer{}
	}
	if r.Subtype != "success" {
		return r.RequestID, ownerAnswer{message: r.Error}
	}
	var resp struct {
		Behavior           string          `json:"behavior"`
		UpdatedInput       json.RawMessage `json:"updatedInput"`
		UpdatedPermissions json.RawMessage `json:"updatedPermissions"`
		Message            string          `json:"message"`
		Interrupt          bool            `json:"interrupt"`
	}
	_ = json.Unmarshal(r.Response, &resp)
	a := ownerAnswer{allow: resp.Behavior == "allow", message: resp.Message, interrupt: resp.Interrupt}
	if isObject(resp.UpdatedInput) {
		a.input = resp.UpdatedInput
	}
	var perms []json.RawMessage
	if json.Unmarshal(resp.UpdatedPermissions, &perms) == nil {
		for _, p := range perms {
			var u permUpdate
			if json.Unmarshal(p, &u) == nil {
				a.perms = append(a.perms, u)
			}
		}
	}
	return r.RequestID, a
}
