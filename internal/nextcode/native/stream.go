package native

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
)

// streamer turns one model call's events into Claude's stream events: message_start, then for
// each block content_block_start and its deltas, then the stops. The driver matches the k-th
// block of the whole answer to the k-th streamed one, so the indexes are the answer's own.
type streamer struct {
	out *output
	id  string // the message id: unique to the call

	mu      sync.Mutex
	started bool
	blocks  []*streamed // by index
}

// streamed is a block as it came in so far.
type streamed struct {
	typ, id, name string
	text          strings.Builder
}

func newStreamer(out *output, id string) *streamer { return &streamer{out: out, id: id} }

// on takes one event of the call.
func (s *streamer) on(e llm.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch e.Kind {
	case llm.EvRetry:
		s.out.retry(e.Attempt, e.MaxAttempts)
	case llm.EvBlockStart:
		if e.Index < 0 || e.Index > 1<<16 {
			return
		}
		s.startBlock(e.Index, &streamed{typ: e.BlockType, id: e.ToolID, name: e.ToolName})
	case llm.EvDelta:
		b := s.block(e.Index)
		if b == nil || e.Text == "" {
			return
		}
		b.text.WriteString(e.Text)
		s.out.stream(map[string]any{"type": "content_block_delta", "index": e.Index, "delta": delta(b.typ, e.Text)})
	}
}

func (s *streamer) block(i int) *streamed {
	if i < 0 || i >= len(s.blocks) {
		return nil
	}
	return s.blocks[i]
}

func (s *streamer) startBlock(i int, b *streamed) {
	if !s.started {
		s.started = true
		s.out.stream(map[string]any{"type": "message_start", "message": map[string]any{"id": s.id, "type": "message", "role": "assistant"}})
	}
	for len(s.blocks) <= i {
		s.blocks = append(s.blocks, nil)
	}
	s.blocks[i] = b
	cb := map[string]any{"type": b.typ}
	switch b.typ {
	case llm.BlockText:
		cb["text"] = ""
	case llm.BlockThinking:
		cb["thinking"] = ""
	case llm.BlockToolUse:
		cb["id"], cb["name"], cb["input"] = b.id, b.name, map[string]any{}
	}
	s.out.stream(map[string]any{"type": "content_block_start", "index": i, "content_block": cb})
}

func delta(typ, text string) map[string]any {
	switch typ {
	case llm.BlockThinking:
		return map[string]any{"type": "thinking_delta", "thinking": text}
	case llm.BlockToolUse:
		return map[string]any{"type": "input_json_delta", "partial_json": text}
	}
	return map[string]any{"type": "text_delta", "text": text}
}

// finish ends the stream of a whole answer. A block of it that did not stream (a client that
// gave it only at the end) is started now, so that the indexes still match.
func (s *streamer) finish(blocks []llm.Block) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, b := range blocks {
		if s.block(i) == nil {
			s.startBlock(i, &streamed{typ: b.Type, id: b.ID, name: b.Name})
		}
	}
	s.stop()
}

// abort ends the stream of an answer that did not come whole.
func (s *streamer) abort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stop()
}

func (s *streamer) stop() {
	if !s.started {
		return
	}
	for i, b := range s.blocks {
		if b != nil {
			s.out.stream(map[string]any{"type": "content_block_stop", "index": i})
		}
	}
	s.out.stream(map[string]any{"type": "message_stop"})
	s.started = false
}

// partial is what streamed of an answer that did not come whole: content for the driver, every
// block in its order, and the text blocks the conversation keeps of it.
func (s *streamer) partial() ([]any, []llm.Block) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var shown []any
	var kept []llm.Block
	for _, b := range s.blocks {
		if b == nil {
			continue
		}
		text := b.text.String()
		switch b.typ {
		case llm.BlockText:
			shown = append(shown, map[string]any{"type": "text", "text": text})
			if strings.TrimSpace(text) != "" {
				kept = append(kept, llm.Block{Type: llm.BlockText, Text: text})
			}
		case llm.BlockThinking:
			shown = append(shown, map[string]any{"type": "thinking", "thinking": text})
		case llm.BlockToolUse:
			shown = append(shown, map[string]any{"type": "tool_use", "id": b.id, "name": b.name, "input": objectOr(json.RawMessage(text))})
		default:
			shown = append(shown, map[string]any{"type": b.typ})
		}
	}
	return shown, kept
}
