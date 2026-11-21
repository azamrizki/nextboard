package llm

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"strings"
)

// The Responses API (POST {base}/responses), as OpenAI speaks it. Every call sends the whole
// conversation with store false, the reasoning items replayed with their encrypted content:
// how Codex runs with no state kept on the server.

type responsesClient struct{ c *conn }

func (rc *responsesClient) Stream(ctx context.Context, req Request, on func(Event)) (*Response, error) {
	body, err := responsesBody(req)
	if err != nil {
		return nil, err
	}
	return rc.c.run(ctx, on, func(ctx context.Context, emit func(Event)) (*Response, *callError) {
		r, ce := rc.c.open(ctx, "/responses", body, rc.c.bearer)
		if ce != nil {
			return nil, ce
		}
		defer r.Close()
		return rc.c.readResponses(r, req.Model, emit)
	})
}

// ---- the request

type respMessage struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []respContent `json:"content"`
}

type respContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type respCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type respOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type respTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type respReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
}

func responsesBody(req Request) ([]byte, error) {
	input, err := responsesInput(req.Messages, req.Model)
	if err != nil {
		return nil, err
	}
	o := &object{}
	o.set("model", req.Model)
	if req.System != "" {
		o.set("instructions", req.System)
	}
	o.raw("input", input)
	if len(req.Tools) > 0 {
		tools := make([]respTool, len(req.Tools))
		for i, t := range req.Tools {
			schema, err := schemaOf(t)
			if err != nil {
				return nil, err
			}
			tools[i] = respTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: schema}
		}
		o.set("tools", tools)
	}
	o.set("stream", true).set("store", false).set("include", []string{"reasoning.encrypted_content"})
	if req.Effort != "" {
		o.set("reasoning", respReasoning{Effort: responsesEffort(req.Effort), Summary: "auto"})
	}
	if req.MaxTokens > 0 {
		o.set("max_output_tokens", req.MaxTokens)
	}
	return o.bytes()
}

// responsesEffort is the reasoning effort of an effort: the API's highest is xhigh.
func responsesEffort(e string) string {
	if e == "max" {
		return "xhigh"
	}
	return e
}

// responsesInput is the conversation as input items: a user message as its tool outputs and
// then its text, an assistant message as its items.
func responsesInput(msgs []Message, model string) ([]byte, error) {
	var items [][]byte
	add := func(v any) error {
		if raw, ok := v.(verbatim); ok {
			items = append(items, raw)
			return nil
		}
		b, err := enc(v)
		items = append(items, b)
		return err
	}
	for _, m := range merged(msgs) {
		if m.Role == RoleAssistant {
			for _, it := range respAssistant(m.Blocks, model) {
				if err := add(it); err != nil {
					return nil, err
				}
			}
			continue
		}
		var texts []respContent
		for _, b := range m.Blocks {
			switch {
			case b.Type == BlockToolResult:
				if err := add(respOutput{Type: "function_call_output", CallID: b.ToolUseID, Output: b.Text}); err != nil {
					return nil, err
				}
			case b.Type == BlockText && b.Text != "":
				texts = append(texts, respContent{Type: "input_text", Text: b.Text})
			}
		}
		if len(texts) > 0 {
			if err := add(respMessage{Type: "message", Role: RoleUser, Content: texts}); err != nil {
				return nil, err
			}
		}
	}
	return array(items), nil
}

// verbatim is an item replayed as the provider gave it, byte for byte: it is written as it
// is, never through encoding/json, which would compact it.
type verbatim []byte

// respAssistant is an assistant message's items. An item this wire gave goes back as its Raw,
// except the reasoning of another model, which is left out. Then the rest of that answer is
// built from its fields, with no item ids: OpenAI refuses an item whose reasoning item is
// missing.
func respAssistant(blocks []Block, model string) []any {
	asRaw := true
	for _, b := range blocks {
		if b.Type == BlockThinking && !(replayable(b, WireResponses) && b.Model == model) {
			asRaw = false
		}
	}
	var items []any
	for _, b := range blocks {
		switch {
		case b.Type == BlockThinking:
			if asRaw {
				items = append(items, verbatim(b.Raw))
			}
		case b.Type == BlockText && b.Text == "":
		case asRaw && replayable(b, WireResponses):
			items = append(items, verbatim(b.Raw))
		case b.Type == BlockText:
			items = append(items, respMessage{Type: "message", Role: RoleAssistant, Content: []respContent{{Type: "output_text", Text: b.Text}}})
		case b.Type == BlockToolUse:
			items = append(items, respCall{Type: "function_call", CallID: b.ID, Name: b.Name, Arguments: string(inputOf(b))})
		}
	}
	return items
}

// ---- the stream

type respEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Delta       json.RawMessage `json:"delta"`
	Item        json.RawMessage `json:"item"`
	Response    *respResponse   `json:"response"`
}

type respResponse struct {
	ID                string     `json:"id"`
	Model             string     `json:"model"`
	Status            string     `json:"status"`
	Usage             *respUsage `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error json.RawMessage `json:"error"`
}

type respUsage struct {
	Input   int `json:"input_tokens"`
	Output  int `json:"output_tokens"`
	Details *struct {
		Cached int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

type respItem struct {
	Type      string          `json:"type"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Content   []respContent   `json:"content"`
	Summary   []respContent   `json:"summary"`
}

// respStream is an answer being read. A block's index is the order of output_item.added,
// which is the output_index order.
type respStream struct {
	ans        answer
	index      map[int]int       // output_index → ours
	summaries  map[int]int       // ours → the summary parts so far
	items      map[int]*respItem // ours → its final item
	id         string
	model      string
	incomplete string // incomplete_details.reason
	usage      Usage
}

// readResponses reads the stream of one attempt, up to response.completed or
// response.incomplete.
func (c *conn) readResponses(r io.Reader, model string, emit func(Event)) (*Response, *callError) {
	st := &respStream{ans: answer{emit: emit}, index: map[int]int{}, summaries: map[int]int{}, items: map[int]*respItem{}}
	sse := newSSE(r)
	for {
		ev, err := sse.next()
		if err != nil {
			return nil, c.broken(err)
		}
		var e respEvent
		jerr := json.Unmarshal(ev.data, &e)
		typ := cmp.Or(e.Type, ev.name)
		switch {
		case typ == "error":
			d, ok := parseDetail(ev.data)
			if !ok {
				d.message = string(ev.data)
			}
			return nil, c.streamError(d)
		case jerr != nil:
			continue
		case typ == "response.failed":
			return nil, c.streamError(failedDetail(e.Response))
		}
		if st.event(typ, &e) {
			return st.response(model), nil
		}
	}
}

func failedDetail(r *respResponse) detail {
	if r != nil {
		if d, ok := parseDetail(r.Error); ok {
			return d
		}
	}
	return detail{message: "the response failed"}
}

// event takes one event in; done = the answer is whole.
func (st *respStream) event(typ string, e *respEvent) (done bool) {
	switch typ {
	case "response.created", "response.in_progress":
		st.meta(e.Response)
	case "response.output_item.added":
		var it respItem
		if json.Unmarshal(e.Item, &it) == nil {
			if b, ok := respBlock(it); ok {
				st.index[e.OutputIndex] = st.ans.start(b)
			}
		}
	case "response.reasoning_summary_part.added":
		if i, ok := st.at(e.OutputIndex, BlockThinking); ok {
			if st.summaries[i]++; st.summaries[i] > 1 {
				st.ans.add(i, "\n\n")
			}
		}
	case "response.reasoning_summary_text.delta":
		st.delta(e, BlockThinking)
	case "response.output_text.delta":
		st.delta(e, BlockText)
	case "response.function_call_arguments.delta":
		st.delta(e, BlockToolUse)
	case "response.output_item.done":
		st.itemDone(e.OutputIndex, e.Item)
	case "response.completed", "response.incomplete":
		st.meta(e.Response)
		if r := e.Response; r != nil {
			if u := r.Usage; u != nil {
				cached := 0
				if u.Details != nil {
					cached = u.Details.Cached
				}
				st.usage = Usage{Input: max(u.Input-cached, 0), Output: u.Output, CacheRead: cached}
			}
			if r.IncompleteDetails != nil && (typ == "response.incomplete" || r.Status == "incomplete") {
				st.incomplete = r.IncompleteDetails.Reason
			}
		}
		return true
	}
	return false
}

func (st *respStream) meta(r *respResponse) {
	if r != nil {
		st.id, st.model = cmp.Or(r.ID, st.id), cmp.Or(r.Model, st.model)
	}
}

// respBlock is the block an output item starts; ok = it is one this client keeps.
func respBlock(it respItem) (Block, bool) {
	switch it.Type {
	case "reasoning":
		return Block{Type: BlockThinking}, true
	case "message":
		return Block{Type: BlockText}, true
	case "function_call":
		return Block{Type: BlockToolUse, ID: it.CallID, Name: it.Name}, true
	}
	return Block{}, false
}

// at is our block of an output index, when it is of type typ.
func (st *respStream) at(out int, typ string) (int, bool) {
	i, ok := st.index[out]
	return i, ok && st.ans.parts[i].Type == typ
}

func (st *respStream) delta(e *respEvent, typ string) {
	if i, ok := st.at(e.OutputIndex, typ); ok {
		s, _ := jsonString(e.Delta)
		st.ans.add(i, s)
	}
}

// itemDone keeps an item's final JSON, verbatim, as its block's Raw. A server that skipped the
// item's deltas has its text streamed now, in one piece.
func (st *respStream) itemDone(out int, raw json.RawMessage) {
	var it respItem
	if json.Unmarshal(raw, &it) != nil {
		return
	}
	i, ok := st.index[out]
	if !ok {
		b, known := respBlock(it)
		if !known {
			return
		}
		i = st.ans.start(b)
		st.index[out] = i
	}
	p := st.ans.parts[i]
	p.raw, st.items[i] = raw, &it
	if final := itemText(p.Type, &it); p.buf.Len() == 0 && final != "" {
		st.ans.add(i, final)
	}
}

// itemText is what a final item says: a message's output text, a reasoning's summaries
// (joined by blank lines, as they streamed), a function call's arguments.
func itemText(typ string, it *respItem) string {
	var parts []string
	switch typ {
	case BlockText:
		for _, c := range it.Content {
			if c.Type == "output_text" {
				parts = append(parts, c.Text)
			}
		}
		return strings.Join(parts, "")
	case BlockThinking:
		for _, s := range it.Summary {
			parts = append(parts, s.Text)
		}
		return strings.Join(parts, "\n\n")
	}
	return argText(it.Arguments)
}

func (st *respStream) response(model string) *Response {
	resp := &Response{ID: st.id, Model: st.model, Blocks: make([]Block, len(st.ans.parts)), Usage: st.usage}
	calls := false
	for i, p := range st.ans.parts {
		b := Block{Type: p.Type, Wire: WireResponses, Model: model}
		text := p.buf.String()
		if it := st.items[i]; it != nil {
			b.Raw, text = p.raw, itemText(p.Type, it)
			if p.Type == BlockToolUse {
				p.ID, p.Name = cmp.Or(it.CallID, p.ID), cmp.Or(it.Name, p.Name)
			}
		}
		if p.Type == BlockToolUse {
			b.ID, b.Name, b.Input = p.ID, p.Name, objectInput(text)
			calls = true
		} else {
			b.Text = text
		}
		resp.Blocks[i] = b
	}
	switch {
	case st.incomplete == "max_output_tokens":
		resp.StopReason = StopMax
	case st.incomplete == "content_filter":
		resp.StopReason = StopRefusal
	case st.incomplete != "":
		resp.StopReason = st.incomplete
	case calls:
		resp.StopReason = StopToolUse
	default:
		resp.StopReason = StopEnd
	}
	return resp
}
