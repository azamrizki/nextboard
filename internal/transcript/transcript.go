// Package transcript reads the conversation files that Claude Code and
// Codex write on a machine (docs/API-agents.md §8): the last reply for the
// output viewer, recent messages, token usage, and the conversation list of
// the resume picker. Files are only ever read, through machine.Host (local
// filesystem, SFTP, or the read-only exec fallback).
package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits (docs/API-agents.md §15).
const (
	TailStart     = 2 << 20 // first tail read for the last reply
	TailMax       = 8 << 20 // the tail grows (x2) up to this
	MaxReplyBytes = 200_000 // Reply.Text cap
	HeaderBytes   = 64 << 10
	MaxScanFiles  = 500
	ScanBudget    = 10 * time.Second
)

// Transcript kinds (Agent.transcript).
const (
	KindClaude = "claude"
	KindCodex  = "codex"
)

var (
	// ErrNoReply: the transcript has no assistant reply (yet).
	ErrNoReply = errors.New("belum ada balasan")
	// ErrUnknownKind: the agent has no supported transcript format.
	ErrUnknownKind = errors.New("jenis transcript tidak dikenal")
	// ErrBadID: an agent session id with characters outside [A-Za-z0-9-].
	ErrBadID = errors.New("id percakapan tidak valid")
)

// Message is one entry of GET /api/sessions/{id}/transcript.
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"` // user | assistant | tool
	At        time.Time `json:"at"`
	Text      string    `json:"text"`
	Tool      string    `json:"tool,omitempty"`
	Sidechain bool      `json:"sidechain"`
}

// TokenUsage is the usage of one assistant API message.
type TokenUsage struct {
	MessageID  string    `json:"messageId"`
	At         time.Time `json:"at"`
	Model      string    `json:"model"`
	Input      int64     `json:"input"`
	Output     int64     `json:"output"`
	CacheRead  int64     `json:"cacheRead"`
	CacheWrite int64     `json:"cacheWrite"`
}

// Reply is the agent's last answer (output viewer, "See output"). Chars
// and Paragraphs describe the full text, even when Text was cut.
type Reply struct {
	Text       string    `json:"text"`
	At         time.Time `json:"at"`
	MessageID  string    `json:"messageId"`
	Truncated  bool      `json:"truncated"`
	Chars      int       `json:"chars"`      // runes
	Paragraphs int       `json:"paragraphs"` // blank-line separated blocks
}

// Conversation is one entry of the resume picker.
type Conversation struct {
	AgentSessionID string    `json:"agentSessionId"`
	Title          string    `json:"title"`
	StartedAt      time.Time `json:"startedAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Messages       int       `json:"messages"` // counted in the first HeaderBytes: a lower bound for big files
	SizeBytes      int64     `json:"sizeBytes"`
	Path           string    `json:"path"`
	AttachedTo     *string   `json:"attachedTo"`
	// AttachedHandle is the handle of that console (filled by the hub).
	AttachedHandle *string `json:"attachedHandle"`
}

// ConversationList is GET /api/machines/{id}/conversations. Truncated means
// the scan stopped early (file cap or time budget) or more items matched
// than the limit.
type ConversationList struct {
	Items     []Conversation `json:"items"`
	Scanned   int            `json:"scanned"`
	Truncated bool           `json:"truncated"`
}

// Parsed is everything one pass over a transcript (or its tail) yields.
type Parsed struct {
	Messages  []Message
	Usage     []TokenUsage
	Reply     *Reply
	SessionID string
	Cwd       string
	// Title and StartedAt feed the conversation list.
	Title     string
	StartedAt time.Time
}

// ClaudeSlug is Claude Code's project folder name for cwd: every UTF-16
// code unit that is not [A-Za-z0-9] becomes '-' (Claude Code is JS, so a
// character outside the BMP, e.g. an emoji, becomes "--"). The full length
// is kept; FindClaude falls back to a scan when Claude shortened it.
func ClaudeSlug(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ClaudeConfigDir is $CLAUDE_CONFIG_DIR of the profile, else ~/.claude.
func ClaudeConfigDir(home, profileDir string) string {
	if profileDir != "" {
		return path.Clean(profileDir)
	}
	return path.Join(home, ".claude")
}

// CodexHome is $CODEX_HOME of the profile, else ~/.codex.
func CodexHome(home, profileDir string) string {
	if profileDir != "" {
		return path.Clean(profileDir)
	}
	return path.Join(home, ".codex")
}

// ClaudePath is <cfg>/projects/<slug>/<id>.jsonl.
func ClaudePath(cfgDir, cwd, id string) string {
	return path.Join(cfgDir, "projects", ClaudeSlug(path.Clean(cwd)), id+".jsonl")
}

// validID accepts UUID-like ids only (they become file names).
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Parse dispatches on kind.
func Parse(kind string, data []byte, partialStart bool) (*Parsed, error) {
	switch kind {
	case KindClaude:
		return ParseClaude(data, partialStart), nil
	case KindCodex:
		return ParseCodex(data, partialStart), nil
	}
	return nil, ErrUnknownKind
}

// lines splits JSONL data; a tail drops its first (possibly cut) line.
// The caller skips lines that do not decode (a file cut mid-line).
func lines(data []byte, partialStart bool) [][]byte {
	if partialStart {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return nil
		}
		data = data[i+1:]
	}
	var out [][]byte
	for len(data) > 0 {
		var l []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			l, data = data[:i], data[i+1:]
		} else {
			l, data = data, nil
		}
		if l = bytes.TrimSpace(l); len(l) > 0 && l[0] == '{' {
			out = append(out, l)
		}
	}
	return out
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// makeReply builds a Reply, capping Text at MaxReplyBytes.
func makeReply(text, id string, at time.Time) *Reply {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	r := &Reply{Text: text, At: at, MessageID: id, Chars: utf8.RuneCountInString(text), Paragraphs: paragraphs(text)}
	if len(text) > MaxReplyBytes {
		cut := MaxReplyBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		r.Text, r.Truncated = text[:cut], true
	}
	return r
}

// paragraphs counts non-empty blocks separated by blank lines.
func paragraphs(text string) int {
	n, in := 0, false
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == "" {
			in = false
			continue
		}
		if !in {
			n++
			in = true
		}
	}
	return n
}

// oneLine collapses whitespace and cuts s to max runes ("…" appended).
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

// inputSummary picks the most telling field of a tool input object.
func inputSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return oneLine(firstLine(s), 200)
		}
		return ""
	}
	for _, k := range []string{"command", "cmd", "file_path", "path", "pattern", "url", "query", "description", "prompt"} {
		v, ok := obj[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			return oneLine(s, 200)
		}
		var arr []string
		if json.Unmarshal(v, &arr) == nil && len(arr) > 0 {
			return oneLine(strings.Join(arr, " "), 200)
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
