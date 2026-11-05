package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// codexLine is one line of a Codex rollout: {timestamp, type, payload}.
type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	// token_usage_record lines carry their fields at the top level.
	ResponseID string      `json:"response_id"`
	Usage      *codexUsage `json:"usage"`
}

type codexUsage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Total      int64 `json:"total_tokens"`
}

type codexPayload struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Timestamp string `json:"timestamp"`
	Role      string `json:"role"`
	Model     string `json:"model"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // function_call: JSON text in a string
	Input     json.RawMessage `json:"input"`     // custom_tool_call
	CallID    string          `json:"call_id"`
	Info      *struct {
		Total *codexUsage `json:"total_token_usage"`
		Last  *codexUsage `json:"last_token_usage"`
	} `json:"info"`
}

// codexMetaText reports user items Codex injects (environment, AGENTS.md,
// instructions) rather than a typed prompt.
func codexMetaText(s string) bool {
	s = strings.TrimSpace(s)
	for _, p := range []string{"<environment_context>", "<user_instructions>", "# AGENTS.md instructions", "<permissions instructions>",
		"<INSTRUCTIONS>", "<turn_aborted>", "<user_shell_command>"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// codexToolSummary summarises function_call arguments (a JSON string) or a
// custom tool input.
func codexToolSummary(args, input json.RawMessage) string {
	for _, raw := range []json.RawMessage{args, input} {
		if len(raw) == 0 {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if sum := inputSummary(json.RawMessage(s)); sum != "" {
				return sum
			}
			return oneLine(firstLine(s), 200)
		}
		if sum := inputSummary(raw); sum != "" {
			return sum
		}
	}
	return ""
}

// ParseCodex parses a Codex rollout (or a tail, with partialStart). Reply
// is the last assistant message (its output_text blocks). Usage comes from
// token_usage_record lines (one per response) when the rollout has them,
// else from event_msg token_count info.last_token_usage, skipping repeats
// of the same running total. Lines cut mid-way are skipped.
func ParseCodex(data []byte, partialStart bool) *Parsed {
	p := &Parsed{}
	var records, counts []TokenUsage
	var lastTotal int64 = -1
	model := ""
	for i, l := range lines(data, partialStart) {
		var ln codexLine
		if json.Unmarshal(l, &ln) != nil {
			continue
		}
		at := parseTime(ln.Timestamp)
		if p.StartedAt.IsZero() && !at.IsZero() {
			p.StartedAt = at
		}
		if ln.Type == "token_usage_record" {
			if u := ln.Usage; u != nil {
				records = append(records, TokenUsage{MessageID: ln.ResponseID, At: at, Model: model, Input: u.Input, Output: u.Output, CacheRead: u.Cached, CacheWrite: u.CacheWrite})
			}
			continue
		}
		var pl codexPayload
		if len(ln.Payload) == 0 || json.Unmarshal(ln.Payload, &pl) != nil {
			continue
		}
		switch ln.Type {
		case "session_meta":
			p.SessionID = pl.ID
			if p.SessionID == "" {
				p.SessionID = pl.SessionID
			}
			p.Cwd = pl.Cwd
			if t := parseTime(pl.Timestamp); !t.IsZero() {
				p.StartedAt = t
			}
		case "turn_context":
			if pl.Model != "" {
				model = pl.Model
			}
			if pl.Cwd != "" && p.Cwd == "" {
				p.Cwd = pl.Cwd
			}
		case "event_msg":
			if pl.Type != "token_count" || pl.Info == nil || pl.Info.Last == nil {
				continue
			}
			total := int64(-2)
			if pl.Info.Total != nil {
				total = pl.Info.Total.Total
			}
			if total == lastTotal {
				continue // same running total re-announced (rate-limit updates)
			}
			lastTotal = total
			u := pl.Info.Last
			id := fmt.Sprintf("tok-%d", total)
			if total < 0 {
				id = fmt.Sprintf("tok-l%d", i)
			}
			counts = append(counts, TokenUsage{MessageID: id, At: at, Model: model, Input: u.Input, Output: u.Output, CacheRead: u.Cached, CacheWrite: u.CacheWrite})
		case "response_item":
			id := pl.ID
			if id == "" {
				id = fmt.Sprintf("line-%d", i)
			}
			switch pl.Type {
			case "message":
				var parts []string
				for _, c := range pl.Content {
					if (c.Type == "output_text" || c.Type == "input_text" || c.Type == "text") && strings.TrimSpace(c.Text) != "" {
						parts = append(parts, c.Text)
					}
				}
				text := strings.Join(parts, "\n\n")
				switch {
				case text == "":
				case pl.Role == "assistant":
					p.Messages = append(p.Messages, Message{ID: id, Role: "assistant", At: at, Text: text})
					p.Reply = makeReply(text, pl.ID, at)
				case pl.Role == "user" && !codexMetaText(text):
					p.Messages = append(p.Messages, Message{ID: id, Role: "user", At: at, Text: text})
					if p.Title == "" {
						p.Title = oneLine(text, 120)
					}
				}
			case "function_call", "custom_tool_call", "local_shell_call":
				name := pl.Name
				if name == "" {
					name = pl.Type
				}
				if pl.CallID != "" {
					id = pl.CallID
				}
				p.Messages = append(p.Messages, Message{ID: id, Role: "tool", At: at, Tool: name, Text: codexToolSummary(pl.Arguments, pl.Input)})
			}
		}
	}
	p.Usage = counts
	if len(records) > 0 {
		p.Usage = records
	}
	return p
}

// codexMeta reads session_meta from the first line of a rollout header.
// A first line longer than the header (huge base instructions) is scanned
// for its leading string fields instead.
func codexMeta(header []byte) (id, cwd string, started time.Time, ok bool) {
	ls := lines(header, false)
	if len(ls) == 0 {
		return "", "", time.Time{}, false
	}
	var ln codexLine
	if err := json.Unmarshal(ls[0], &ln); err != nil {
		if !bytes.Contains(ls[0], []byte(`"type":"session_meta"`)) {
			return "", "", time.Time{}, false
		}
		id = scanString(ls[0], "session_id")
		if id == "" {
			id = scanString(ls[0], "id")
		}
		return id, scanString(ls[0], "cwd"), parseTime(scanString(ls[0], "timestamp")), id != ""
	}
	if ln.Type != "session_meta" {
		return "", "", time.Time{}, false
	}
	var pl codexPayload
	if json.Unmarshal(ln.Payload, &pl) != nil {
		return "", "", time.Time{}, false
	}
	id = pl.ID
	if id == "" {
		id = pl.SessionID
	}
	started = parseTime(pl.Timestamp)
	if started.IsZero() {
		started = parseTime(ln.Timestamp)
	}
	return id, pl.Cwd, started, true
}

// scanString returns the first "key":"value" string in b, unescaped; ""
// when absent or cut.
func scanString(b []byte, key string) string {
	pat := []byte(`"` + key + `":`)
	i := bytes.Index(b, pat)
	if i < 0 {
		return ""
	}
	rest := bytes.TrimLeft(b[i+len(pat):], " ")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	for j := 1; j < len(rest); j++ {
		switch rest[j] {
		case '\\':
			j++
		case '"':
			var s string
			if json.Unmarshal(rest[:j+1], &s) == nil {
				return s
			}
			return ""
		}
	}
	return ""
}
