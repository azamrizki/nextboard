package llm

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// The Anthropic Messages API (POST {base}/v1/messages), as Anthropic speaks it and the gateways
// and vendors built against it (GLM, Kimi, DeepSeek, MiniMax, OpenRouter): one request with
// stream true, its answer read as server-sent events.

// anthropicVersion is the API version every Anthropic-compatible gateway is built against; the
// hub's provider test sends it too.
const anthropicVersion = "2023-06-01"

// thinkingBudgets are the budget_tokens of each effort, for the models of other vendors behind
// an Anthropic-compatible API. Claude models think adaptively instead: the newest answer
// budget_tokens with a 400.
var thinkingBudgets = map[string]int{"low": 4096, "medium": 10000, "high": 20000, "xhigh": 32000, "max": 32000}

type anthropicClient struct{ c *conn }

func (a *anthropicClient) Stream(ctx context.Context, req Request, on func(Event)) (*Response, error) {
	body, err := anthropicBody(req, a.official())
	if err != nil {
		return nil, err
	}
	return a.c.run(ctx, on, func(ctx context.Context, emit func(Event)) (*Response, *callError) {
		rc, ce := a.c.open(ctx, "/v1/messages", body, a.header)
		if ce != nil {
			return nil, ce
		}
		defer rc.Close()
		return a.c.readAnthropic(rc, req.Model, emit)
	})
}

// official reports whether the provider is Anthropic's own API, which takes fields that
// Anthropic-compatible gateways may refuse (eager_input_streaming).
func (a *anthropicClient) official() bool {
	u, err := url.Parse(a.c.base)
	return err == nil && u.Hostname() == "api.anthropic.com"
}

func (a *anthropicClient) header(h http.Header) {
	h.Set("anthropic-version", anthropicVersion)
	switch key := a.c.cfg.Key; {
	case key == "":
	case a.c.cfg.Auth == "x-api-key":
		h.Set("x-api-key", key)
	default:
		h.Set("Authorization", "Bearer "+key)
	}
}

// ---- the request

type cacheControl struct {
	Type string `json:"type"`
}

// cached is the cache_control of a block that may end a cached prefix. Breakpoints go on the
// system text, the last tool and the last block of the last message: 3 of the 4 allowed, and
// never a top-level cache_control, which gateways may reject.
type cached struct {
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

func (c *cached) mark() { c.CacheControl = &cacheControl{Type: "ephemeral"} }

type antText struct {
	Type string `json:"type"`
	Text string `json:"text"`
	cached
}

type antToolUse struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	cached
}

type antToolResult struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
	cached
}

type antThinking struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
	// EagerInputStreaming: on Anthropic's own API only (anthropicBody).
	EagerInputStreaming bool `json:"eager_input_streaming,omitempty"`
	cached
}

// antAdaptive is the thinking of Claude models: adaptive, with its summaries shown (the newest
// models leave the thinking text out unless asked for them).
type antAdaptive struct {
	Type    string `json:"type"`
	Display string `json:"display"`
}

type antBudget struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// antItem is one content block to send: built from its fields (v), or a block's Raw (raw).
type antItem struct {
	v   interface{ mark() }
	raw []byte
}

// anthropicBody is the request of a call. It never has temperature, top_p or top_k: the
// newest Claude models refuse them. official: the provider is Anthropic's own API, where the
// tools' input streams as it is written (eager_input_streaming) rather than after each
// parameter is whole; the agent's tools check their input themselves, so a cut one is an error
// the model reads, as invalid JSON is.
func anthropicBody(req Request, official bool) ([]byte, error) {
	claude := strings.HasPrefix(req.Model, "claude-")
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16384
		if claude {
			maxTokens = 32000
		}
	}
	var thinking, output any
	switch budget, ok := thinkingBudgets[req.Effort]; {
	case req.Effort == "":
	case claude:
		thinking, output = antAdaptive{Type: "adaptive", Display: "summarized"}, map[string]string{"effort": req.Effort}
	case ok:
		thinking = antBudget{Type: "enabled", BudgetTokens: budget}
		maxTokens = max(maxTokens, budget+8192)
	}
	messages, err := anthropicMessages(req.Messages, req.Model)
	if err != nil {
		return nil, err
	}
	o := &object{}
	o.set("model", req.Model).set("max_tokens", maxTokens)
	if req.System != "" {
		sys := antText{Type: "text", Text: req.System}
		sys.mark()
		o.set("system", []antText{sys})
	}
	o.raw("messages", messages)
	if len(req.Tools) > 0 {
		tools, err := anthropicTools(req.Tools, official)
		if err != nil {
			return nil, err
		}
		o.set("tools", tools)
	}
	if thinking != nil {
		o.set("thinking", thinking)
	}
	if output != nil {
		o.set("output_config", output)
	}
	o.set("stream", true)
	return o.bytes()
}

func anthropicTools(defs []ToolDef, eager bool) ([]antTool, error) {
	tools := make([]antTool, len(defs))
	for i, t := range defs {
		schema, err := schemaOf(t)
		if err != nil {
			return nil, err
		}
		tools[i] = antTool{Name: t.Name, Description: t.Description, InputSchema: schema, EagerInputStreaming: eager}
	}
	tools[len(tools)-1].mark()
	return tools, nil
}

func anthropicMessages(msgs []Message, model string) ([]byte, error) {
	msgs = merged(msgs)
	out := make([][]byte, 0, len(msgs))
	for i, m := range msgs {
		role, items := RoleUser, []antItem(nil)
		if m.Role == RoleAssistant {
			role, items = RoleAssistant, antAssistant(m.Blocks, model)
		} else {
			items = antUser(m.Blocks)
		}
		if i == len(msgs)-1 {
			markLast(items)
		}
		content := make([][]byte, 0, len(items))
		for _, it := range items {
			b := it.raw
			if it.v != nil {
				var err error
				if b, err = enc(it.v); err != nil {
					return nil, err
				}
			}
			content = append(content, b)
		}
		msg, err := (&object{}).set("role", role).raw("content", array(content)).bytes()
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return array(out), nil
}

// antUser is a user message's blocks: its tool results first, as the API wants them, then its
// text.
func antUser(blocks []Block) []antItem {
	var results, texts []antItem
	for _, b := range blocks {
		switch {
		case b.Type == BlockToolResult:
			results = append(results, antItem{v: &antToolResult{Type: "tool_result", ToolUseID: b.ToolUseID, Content: b.Text, IsError: b.IsError}})
		case b.Type == BlockText && b.Text != "":
			texts = append(texts, antItem{v: &antText{Type: "text", Text: b.Text}})
		}
	}
	items := append(results, texts...)
	if len(items) == 0 {
		items = []antItem{{v: &antText{Type: "text", Text: "(no message)"}}}
	}
	return items
}

// antAssistant is an assistant message's blocks. A block this wire gave is sent as its Raw,
// except the thinking of another model, which is left out; so is thinking of another wire.
// Empty text is left out too: the API refuses it.
func antAssistant(blocks []Block, model string) []antItem {
	var items []antItem
	for _, b := range blocks {
		switch {
		case b.Type == BlockText && b.Text == "":
		case replayable(b, WireAnthropic):
			if b.Type == BlockThinking && b.Model != model {
				continue
			}
			items = append(items, antItem{raw: b.Raw})
		case b.Type == BlockText:
			items = append(items, antItem{v: &antText{Type: "text", Text: b.Text}})
		case b.Type == BlockToolUse:
			items = append(items, antItem{v: &antToolUse{Type: "tool_use", ID: b.ID, Name: b.Name, Input: inputOf(b)}})
		}
	}
	if len(items) == 0 {
		items = []antItem{{v: &antText{Type: "text", Text: noAnswer}}}
	}
	return items
}

// markLast puts the conversation's breakpoint on the last block built here: a Raw block goes
// out as it is, and thinking takes no cache_control.
func markLast(items []antItem) {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].v != nil {
			items[i].v.mark()
			return
		}
	}
}

// ---- the stream

// antEvent is one event of the stream; its type says which fields it has.
type antEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID    string    `json:"id"`
		Model string    `json:"model"`
		Usage *antUsage `json:"usage"`
	} `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *antUsage `json:"usage"`
}

type antUsage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
}

// antStream is an answer being read.
type antStream struct {
	ans   answer
	index map[int]int // the provider's block index → ours
	id    string
	model string
	stop  string
	usage Usage
}

// readAnthropic reads the stream of one attempt. It ends at message_stop, or at the end of a
// stream that gave its stop reason (some gateways send no message_stop).
func (c *conn) readAnthropic(r io.Reader, model string, emit func(Event)) (*Response, *callError) {
	st := &antStream{ans: answer{emit: emit}, index: map[int]int{}}
	sse := newSSE(r)
	for {
		ev, err := sse.next()
		if err != nil {
			if err == io.EOF && st.stop != "" {
				return st.response(model), nil
			}
			return nil, c.broken(err)
		}
		var e antEvent
		jerr := json.Unmarshal(ev.data, &e)
		if ev.name == "error" || (jerr == nil && e.Type == "error") {
			d, ok := parseDetail(ev.data)
			if !ok {
				d.message = string(ev.data)
			}
			return nil, c.streamError(d)
		}
		if jerr != nil {
			continue
		}
		if st.event(cmp.Or(e.Type, ev.name), &e) {
			return st.response(model), nil
		}
	}
}

// event takes one event in; done = the answer is whole.
func (st *antStream) event(typ string, e *antEvent) (done bool) {
	switch typ {
	case "message_start":
		st.id, st.model = e.Message.ID, e.Message.Model
		st.take(e.Message.Usage)
	case "content_block_start":
		st.blockStart(e.Index, e.ContentBlock)
	case "content_block_delta":
		st.delta(e)
	case "message_delta":
		if e.Delta.StopReason != "" {
			st.stop = e.Delta.StopReason
		}
		st.take(e.Usage)
	case "message_stop":
		return true
	}
	return false
}

// take keeps the last non-zero count of each kind: some gateways give the usage only in
// message_delta, some in both events.
func (st *antStream) take(u *antUsage) {
	if u == nil {
		return
	}
	keep(&st.usage.Input, u.Input)
	keep(&st.usage.Output, u.Output)
	keep(&st.usage.CacheRead, u.CacheRead)
	keep(&st.usage.CacheWrite, u.CacheWrite)
}

func keep(to *int, n int) {
	if n > 0 {
		*to = n
	}
}

// blockStart begins a block of a kind this client keeps; there are no server tools, so the
// others are left out.
func (st *antStream) blockStart(index int, raw json.RawMessage) {
	var cb struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Input     json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &cb) != nil {
		return
	}
	var i int
	switch cb.Type {
	case "text":
		i = st.ans.start(Block{Type: BlockText})
		st.ans.add(i, cb.Text)
	case "thinking":
		i = st.ans.start(Block{Type: BlockThinking})
		st.ans.add(i, cb.Thinking)
		st.ans.parts[i].sig = cb.Signature
	case "redacted_thinking":
		i = st.ans.start(Block{Type: BlockThinking})
		st.ans.parts[i].raw = raw
	case "tool_use":
		i = st.ans.start(Block{Type: BlockToolUse, ID: cb.ID, Name: cb.Name})
		st.ans.parts[i].init = cb.Input
	default:
		return
	}
	st.index[index] = i
}

func (st *antStream) delta(e *antEvent) {
	i, ok := st.index[e.Index]
	if !ok {
		return
	}
	p := st.ans.parts[i]
	switch d := e.Delta; {
	case d.Type == "text_delta" && p.Type == BlockText:
		st.ans.add(i, d.Text)
	case d.Type == "thinking_delta" && p.Type == BlockThinking && p.raw == nil:
		st.ans.add(i, d.Thinking)
	case d.Type == "signature_delta" && p.Type == BlockThinking && p.raw == nil:
		p.sig += d.Signature
	case d.Type == "input_json_delta" && p.Type == BlockToolUse:
		st.ans.add(i, d.PartialJSON)
	}
}

func (st *antStream) response(model string) *Response {
	blocks := make([]Block, len(st.ans.parts))
	for i, p := range st.ans.parts {
		blocks[i] = st.block(i, p, model)
	}
	return &Response{ID: st.id, Model: st.model, Blocks: blocks, StopReason: anthropicStop(st.stop), Usage: st.usage}
}

// block is part i as the answer keeps it, with its Raw in the exact form the API takes back.
func (st *antStream) block(i int, p *part, model string) Block {
	b := Block{Type: p.Type, Wire: WireAnthropic, Model: model}
	var v any
	switch {
	case p.raw != nil: // redacted_thinking, as given
		b.Raw = p.raw
		return b
	case p.Type == BlockText:
		b.Text = p.buf.String()
		v = antText{Type: "text", Text: b.Text}
	case p.Type == BlockThinking:
		b.Text = p.buf.String()
		v = antThinking{Type: "thinking", Thinking: b.Text, Signature: p.sig}
	default:
		// a gateway that gave the whole input at the start, and no deltas
		if s := strings.TrimSpace(string(p.init)); p.buf.Len() == 0 && s != "" && s != "{}" && s != "null" {
			st.ans.add(i, s)
		}
		b.ID, b.Name, b.Input = p.ID, p.Name, objectInput(p.buf.String())
		v = antToolUse{Type: "tool_use", ID: b.ID, Name: b.Name, Input: b.Input}
	}
	b.Raw, _ = enc(v) // strings and a JSON object always encode
	return b
}

func anthropicStop(r string) string {
	switch r {
	case "", "stop_sequence", "pause_turn":
		return StopEnd
	}
	return r
}
