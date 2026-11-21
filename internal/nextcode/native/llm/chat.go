package llm

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// The OpenAI-compatible Chat Completions API (POST {base}/chat/completions), as DeepSeek, Groq,
// vLLM, Ollama, LM Studio, OpenRouter and many more speak it. Its thinking is the
// reasoning_content (or reasoning) of the deltas, with no Raw: nothing of it is signed.

type chatClient struct {
	c *conn
	// plain: the server refused stream_options once, so it is never sent them again
	plain atomic.Bool
}

func (cc *chatClient) Stream(ctx context.Context, req Request, on func(Event)) (*Response, error) {
	withOpts, err := chatBody(req, true)
	if err != nil {
		return nil, err
	}
	without, err := chatBody(req, false)
	if err != nil {
		return nil, err
	}
	return cc.c.run(ctx, on, func(ctx context.Context, emit func(Event)) (*Response, *callError) {
		rc, ce := cc.open(ctx, withOpts, without)
		if ce != nil {
			return nil, ce
		}
		defer rc.Close()
		return cc.c.readChat(rc, req.Model, emit)
	})
}

// open posts the request. A server that refuses stream_options (a 400 that names them) is
// asked again without them, at once, and is never sent them again.
func (cc *chatClient) open(ctx context.Context, withOpts, without []byte) (io.ReadCloser, *callError) {
	if cc.plain.Load() {
		return cc.c.open(ctx, "/chat/completions", without, cc.c.bearer)
	}
	rc, ce := cc.c.open(ctx, "/chat/completions", withOpts, cc.c.bearer)
	if ce != nil && (ce.api.Status == http.StatusBadRequest || ce.api.Status == http.StatusUnprocessableEntity) &&
		strings.Contains(strings.ToLower(ce.api.Message), "stream_options") {
		cc.plain.Store(true)
		return cc.c.open(ctx, "/chat/completions", without, cc.c.bearer)
	}
	return rc, ce
}

// ---- the request

type chatRequest struct {
	Model           string             `json:"model"`
	Messages        []any              `json:"messages"`
	Stream          bool               `json:"stream"`
	StreamOptions   *chatStreamOptions `json:"stream_options,omitempty"`
	Tools           []chatTool         `json:"tools,omitempty"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
	MaxTokens       int                `json:"max_tokens,omitempty"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatTool struct {
	Type     string      `json:"type"`
	Function chatToolDef `json:"function"`
}

type chatToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatText struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatToolMessage struct {
	Role       string `json:"role"`
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
}

type chatAssistant struct {
	Role             string         `json:"role"`
	Content          *string        `json:"content"` // null when it only calls tools
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
}

type chatToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function chatCall `json:"function"`
}

type chatCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func chatBody(req Request, streamOptions bool) ([]byte, error) {
	r := chatRequest{Model: req.Model, Messages: chatMessages(req), Stream: true,
		ReasoningEffort: chatEffort(req.Effort), MaxTokens: max(req.MaxTokens, 0)}
	if streamOptions {
		r.StreamOptions = &chatStreamOptions{IncludeUsage: true}
	}
	for _, t := range req.Tools {
		schema, err := schemaOf(t)
		if err != nil {
			return nil, err
		}
		r.Tools = append(r.Tools, chatTool{Type: "function", Function: chatToolDef{Name: t.Name, Description: t.Description, Parameters: schema}})
	}
	return enc(r)
}

// chatEffort is the reasoning_effort of an effort: the API has nothing above high.
func chatEffort(e string) string {
	switch e {
	case "xhigh", "max":
		return "high"
	}
	return e
}

// chatMessages is the conversation as the API takes it: the system message, then each
// message, a user one as its tool messages and then its text.
func chatMessages(req Request) []any {
	var out []any
	if req.System != "" {
		out = append(out, chatText{Role: "system", Content: req.System})
	}
	msgs := merged(req.Messages)
	// the current turn starts after the last user message with text: its tool loop is where
	// DeepSeek's and Kimi's thinking models want their reasoning_content back
	turn := -1
	for i, m := range msgs {
		if m.Role != RoleAssistant && joinText(m.Blocks) != "" {
			turn = i
		}
	}
	for i, m := range msgs {
		if m.Role == RoleAssistant {
			out = append(out, chatAssistantOf(m.Blocks, i > turn))
			continue
		}
		for _, b := range m.Blocks {
			if b.Type == BlockToolResult {
				content := b.Text
				if b.IsError {
					content = "Error: " + content
				}
				out = append(out, chatToolMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: content})
			}
		}
		if t := joinText(m.Blocks); t != "" {
			out = append(out, chatText{Role: "user", Content: t})
		}
	}
	return out
}

// joinText is the text blocks of a message, joined by blank lines.
func joinText(blocks []Block) string {
	var texts []string
	for _, b := range blocks {
		if b.Type == BlockText && b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// chatAssistantOf is an assistant message. Its reasoning_content is the thinking this wire
// gave, and only in the current turn: the servers that want it there reject or ignore it
// elsewhere.
func chatAssistantOf(blocks []Block, current bool) chatAssistant {
	msg := chatAssistant{Role: RoleAssistant}
	var thinking []string
	for _, b := range blocks {
		switch b.Type {
		case BlockToolUse:
			msg.ToolCalls = append(msg.ToolCalls, chatToolCall{ID: b.ID, Type: "function", Function: chatCall{Name: b.Name, Arguments: string(inputOf(b))}})
		case BlockThinking:
			if b.Wire == WireChat && b.Text != "" {
				thinking = append(thinking, b.Text)
			}
		}
	}
	if t := joinText(blocks); t != "" {
		msg.Content = &t
	} else if len(msg.ToolCalls) == 0 {
		t := noAnswer
		msg.Content = &t
	}
	if current {
		msg.ReasoningContent = strings.Join(thinking, "\n\n")
	}
	return msg
}

// ---- the stream

type chatChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent json.RawMessage `json:"reasoning_content"`
			Reasoning        json.RawMessage `json:"reasoning"`
			ToolCalls        []chatToolDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage      `json:"usage"`
	Error json.RawMessage `json:"error"`
}

type chatToolDelta struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type chatUsage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	Details    *struct {
		Cached int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CacheHit int `json:"prompt_cache_hit_tokens"` // DeepSeek
}

// chatStream is an answer being read. Each block gets its index when it first appears: in
// practice the thinking, then the text, then the tool calls.
type chatStream struct {
	ans      answer
	thinking int         // the thinking block, -1 = none yet
	text     int         // the text block, -1 = none yet
	calls    map[int]int // a tool call's index in the deltas → ours
	madeUp   map[int]bool
	ncalls   int
	id       string
	model    string
	finish   string
	usage    *chatUsage
}

// readChat reads the stream of one attempt, up to data: [DONE], or the end of a stream that
// gave its finish_reason.
func (c *conn) readChat(r io.Reader, model string, emit func(Event)) (*Response, *callError) {
	st := &chatStream{ans: answer{emit: emit}, thinking: -1, text: -1, calls: map[int]int{}, madeUp: map[int]bool{}}
	sse := newSSE(r)
	for {
		ev, err := sse.next()
		if err != nil {
			if err == io.EOF && st.finish != "" {
				break
			}
			return nil, c.broken(err)
		}
		data := bytes.TrimSpace(ev.data)
		if string(data) == "[DONE]" {
			break
		}
		var ch chatChunk
		jerr := json.Unmarshal(data, &ch)
		if ev.name == "error" || (jerr == nil && len(ch.Error) > 0 && string(ch.Error) != "null") {
			d, ok := parseDetail(data)
			if !ok {
				d.message = string(data)
			}
			return nil, c.streamError(d)
		}
		if jerr == nil {
			st.chunk(&ch)
		}
	}
	return st.response(model), nil
}

func (st *chatStream) chunk(ch *chatChunk) {
	st.id, st.model = cmp.Or(st.id, ch.ID), cmp.Or(st.model, ch.Model)
	if ch.Usage != nil {
		st.usage = ch.Usage
	}
	if len(ch.Choices) == 0 {
		return
	}
	c := ch.Choices[0]
	reasoning, _ := jsonString(c.Delta.ReasoningContent)
	if reasoning == "" {
		reasoning, _ = jsonString(c.Delta.Reasoning)
	}
	if reasoning != "" {
		if st.thinking < 0 {
			st.thinking = st.ans.start(Block{Type: BlockThinking})
		}
		st.ans.add(st.thinking, reasoning)
	}
	if text, _ := jsonString(c.Delta.Content); text != "" {
		if st.text < 0 {
			st.text = st.ans.start(Block{Type: BlockText})
		}
		st.ans.add(st.text, text)
	}
	for j, tc := range c.Delta.ToolCalls {
		st.toolCall(j, tc)
	}
	if c.FinishReason != "" {
		st.finish = c.FinishReason
	}
}

// toolCall is a piece of a tool call: its id and name come with its first piece. A new id
// under an index seen before is another call, as some servers send them.
func (st *chatStream) toolCall(j int, tc chatToolDelta) {
	key := j
	if tc.Index != nil {
		key = *tc.Index
	}
	i, ok := st.calls[key]
	if ok && tc.ID != "" && !st.madeUp[i] && st.ans.parts[i].ID != tc.ID {
		ok = false
	}
	if !ok {
		b := Block{Type: BlockToolUse, ID: tc.ID, Name: tc.Function.Name}
		if b.ID == "" {
			b.ID = fmt.Sprintf("call_%d", st.ncalls)
		}
		i = st.ans.start(b)
		st.madeUp[i] = tc.ID == ""
		st.calls[key] = i
		st.ncalls++
	} else if p := st.ans.parts[i]; p.Name == "" {
		p.Name = tc.Function.Name
	}
	st.ans.add(i, argText(tc.Function.Arguments))
}

func (st *chatStream) response(model string) *Response {
	resp := &Response{ID: st.id, Model: st.model, Blocks: make([]Block, len(st.ans.parts)), StopReason: chatStop(st.finish)}
	calls := false
	for i, p := range st.ans.parts {
		b := Block{Type: p.Type, Wire: WireChat, Model: model}
		if p.Type == BlockToolUse {
			b.ID, b.Name, b.Input = p.ID, p.Name, objectInput(p.buf.String())
			calls = true
		} else {
			b.Text = p.buf.String()
		}
		resp.Blocks[i] = b
	}
	// some servers finish an answer of tool calls with "stop"
	if calls && resp.StopReason == StopEnd {
		resp.StopReason = StopToolUse
	}
	if u := st.usage; u != nil {
		cached := u.CacheHit
		if u.Details != nil && u.Details.Cached > 0 {
			cached = u.Details.Cached
		}
		resp.Usage = Usage{Input: max(u.Prompt-cached, 0), Output: u.Completion, CacheRead: cached}
	}
	return resp
}

func chatStop(r string) string {
	switch r {
	case "", "stop":
		return StopEnd
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMax
	case "content_filter":
		return StopRefusal
	}
	return r
}
