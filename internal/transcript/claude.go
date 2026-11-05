package transcript

import (
	"encoding/json"
	"strings"
	"time"
)

// claudeLine is one JSONL line of a Claude Code transcript. Only lines of
// type user/assistant are messages; the rest (attachment, system,
// api-request-blob, cost-state, …) is ignored, except titles.
type claudeLine struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	Timestamp   string          `json:"timestamp"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	Message     json.RawMessage `json:"message"`
	CustomTitle string          `json:"customTitle"`
	AITitle     string          `json:"aiTitle"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *struct {
		Input      int64 `json:"input_tokens"`
		Output     int64 `json:"output_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type claudeBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// claudeContent splits message.content (a string or a block list) into
// its text and the tool blocks; hasResult reports tool_result blocks.
func claudeContent(raw json.RawMessage) (text string, tools []claudeBlock, hasResult bool) {
	if len(raw) == 0 {
		return "", nil, false
	}
	if raw[0] == '"' {
		_ = json.Unmarshal(raw, &text)
		return text, nil, false
	}
	var blocks []claudeBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", nil, false
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		case "tool_use":
			tools = append(tools, b)
		case "tool_result":
			hasResult = true
		}
	}
	return strings.Join(parts, "\n\n"), tools, hasResult
}

// claudeMetaText reports user text that Claude Code generated itself
// (slash commands, their output, caveats) rather than a typed prompt.
func claudeMetaText(s string) bool {
	s = strings.TrimSpace(s)
	for _, p := range []string{"<command-name>", "<command-message>", "<command-args>", "<local-command-stdout>",
		"<local-command-stderr>", "<local-command-caveat>", "Caveat:", "<system-reminder>", "<bash-input>", "<bash-stdout>",
		"<bash-stderr>", "<user-memory-input>", "[Request interrupted"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ParseClaude parses a Claude Code transcript (or a tail of it, with
// partialStart). Reply follows docs/API-agents.md §8.2: the text blocks
// of non-sidechain assistant lines after the last tool_result of a turn
// (a turn starts at a user line with real text); the newest turn that has
// such text wins, so an in-progress turn shows the previous answer.
// Messages: user prompts, assistant text, and one "tool" entry per
// tool_use; tool_result lines are not listed. Usage is deduped per
// message.id (one API message can span several lines).
func ParseClaude(data []byte, partialStart bool) *Parsed {
	p := &Parsed{}
	var turn []string // text after the last tool_result of the current turn
	var turnID string
	var turnAt time.Time
	usageAt := map[string]int{}
	flush := func() {
		if r := makeReply(strings.Join(turn, "\n\n"), turnID, turnAt); r != nil {
			p.Reply = r
		}
	}
	for _, l := range lines(data, partialStart) {
		var ln claudeLine
		if json.Unmarshal(l, &ln) != nil {
			continue
		}
		at := parseTime(ln.Timestamp)
		if p.StartedAt.IsZero() && !at.IsZero() {
			p.StartedAt = at
		}
		switch ln.Type {
		case "custom-title":
			if ln.CustomTitle != "" {
				p.Title = oneLine(ln.CustomTitle, 120)
			}
			continue
		case "ai-title":
			continue // handled by listTitle (lower priority than custom-title)
		case "user", "assistant":
		default:
			continue
		}
		if ln.SessionID != "" {
			p.SessionID = ln.SessionID
		}
		if ln.Cwd != "" {
			p.Cwd = ln.Cwd
		}
		var msg claudeMessage
		if json.Unmarshal(ln.Message, &msg) != nil {
			continue
		}
		text, tools, hasResult := claudeContent(msg.Content)
		if ln.Type == "user" {
			meta := ln.IsMeta || claudeMetaText(text)
			if text != "" && !meta {
				p.Messages = append(p.Messages, Message{ID: ln.UUID, Role: "user", At: at, Text: text, Sidechain: ln.IsSidechain})
			}
			if ln.IsSidechain {
				continue
			}
			if (text != "" && !meta) || hasResult {
				if text != "" && !meta {
					flush() // a new turn: remember the previous answer
				}
				turn, turnID, turnAt = nil, "", time.Time{}
			}
			continue
		}
		// assistant
		if msg.Usage != nil && msg.ID != "" && msg.Model != "<synthetic>" {
			u := TokenUsage{MessageID: msg.ID, At: at, Model: msg.Model, Input: msg.Usage.Input, Output: msg.Usage.Output,
				CacheRead: msg.Usage.CacheRead, CacheWrite: msg.Usage.CacheWrite}
			if i, ok := usageAt[msg.ID]; ok {
				u.At = p.Usage[i].At // keep the first time, the latest numbers
				p.Usage[i] = u
			} else {
				usageAt[msg.ID] = len(p.Usage)
				p.Usage = append(p.Usage, u)
			}
		}
		if text != "" {
			p.Messages = append(p.Messages, Message{ID: ln.UUID, Role: "assistant", At: at, Text: text, Sidechain: ln.IsSidechain})
			if !ln.IsSidechain {
				turn = append(turn, text)
				turnID, turnAt = msg.ID, at
			}
		}
		for _, t := range tools {
			id := t.ID
			if id == "" {
				id = ln.UUID
			}
			p.Messages = append(p.Messages, Message{ID: id, Role: "tool", At: at, Tool: t.Name, Text: inputSummary(t.Input), Sidechain: ln.IsSidechain})
		}
	}
	flush()
	return p
}

// claudeTitle is the picker title: /rename title, else Claude's own
// title, else the first typed prompt.
func claudeTitle(header []byte) string {
	p := ParseClaude(header, false)
	if p.Title != "" {
		return p.Title
	}
	for _, l := range lines(header, false) {
		var ln claudeLine
		if json.Unmarshal(l, &ln) == nil && ln.Type == "ai-title" && ln.AITitle != "" {
			return oneLine(ln.AITitle, 120)
		}
	}
	for _, m := range p.Messages {
		if m.Role == "user" && !m.Sidechain {
			return oneLine(m.Text, 120)
		}
	}
	return ""
}
