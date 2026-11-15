package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// ModelSeen is what a transcript says about the model and effort of a console:
// a CLI's own /model and /effort change them without the hub being told.
type ModelSeen struct {
	// HasModel: the user chose one, or the CLI records it per turn. Model may
	// then be "": back to the CLI's default.
	HasModel bool
	Model    string
	// HasEffort is the same for the effort.
	HasEffort bool
	Effort    string
	// Replied is the model of the last reply. Follow: /model printed a name that
	// could not be turned into an id, so that reply is the only one to tell.
	Replied string
	Follow  bool
}

var (
	localCmdRe   = regexp.MustCompile(`<command-name>/(model|effort)</command-name>`)
	localArgsRe  = regexp.MustCompile(`<command-args>([^<]*)</command-args>`)
	localOutRe   = regexp.MustCompile(`(?s)<local-command-stdout>(.*?)</local-command-stdout>`)
	ansiRe       = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	setModelRe   = regexp.MustCompile(`^Set model to (?:` + "`" + `([^` + "`" + `]+)` + "`" + `|(\S+(?: \S+)*?))(?: and saved| \(|$)`)
	setEffortRe  = regexp.MustCompile(`^(?:Set effort level to|Effort level set to) ([a-z]+)`)
	claudeNameRe = regexp.MustCompile(`^(Opus|Sonnet|Haiku|Fable|Mythos) (\d+)(?:\.(\d+))?( \(1M context\))?$`)
)

// claudeModelID turns what /model prints into what --model takes. ok is false when
// it cannot be told: the next reply says.
func claudeModelID(shown string) (id string, ok bool) {
	shown = strings.TrimSpace(shown)
	if strings.HasPrefix(shown, "Default") {
		return "", true
	}
	if strings.HasPrefix(shown, "claude-") {
		return shown, true
	}
	switch strings.ToLower(shown) {
	case "opus", "sonnet", "haiku", "fable", "mythos":
		return strings.ToLower(shown), true
	}
	m := claudeNameRe.FindStringSubmatch(shown)
	if m == nil {
		return "", false
	}
	id = "claude-" + strings.ToLower(m[1]) + "-" + m[2]
	if m[3] != "" {
		id += "-" + m[3]
	}
	if m[4] != "" {
		id += "[1m]"
	}
	return id, true
}

func recordText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// What a line must contain to matter to the model scans; the others, tool results above all
// (most of a transcript), are never decoded. Only letters and '-' are looked for, which JSON
// never escapes.
var (
	markCommand = []byte("command-name")
	markStdout  = []byte("local-command-stdout")
	markReply   = []byte(`"assistant"`)
	markTurnCtx = []byte(`"turn_context"`)
)

// claudeModelScan is what scanClaudeModel knows between lines: what was seen so far and a
// /model or /effort command still waiting for what it printed.
type claudeModelScan struct {
	seen     ModelSeen
	cmd, arg string
}

// scanClaudeModel walks a Claude Code transcript: /model and /effort are local
// commands, each followed by what it printed.
func scanClaudeModel(data []byte, partial bool, since time.Time) ModelSeen {
	var sc claudeModelScan
	for _, l := range lines(data, partial) {
		sc.line(l, since)
	}
	return sc.seen
}

func (sc *claudeModelScan) line(l []byte, since time.Time) {
	if !bytes.Contains(l, markCommand) && !bytes.Contains(l, markStdout) {
		if !bytes.Contains(l, markReply) {
			return // neither a reply nor a local command: nothing here changes what is seen
		}
		// a reply: only its model is read, not its content
		var r struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(l, &r) != nil || (!since.IsZero() && !parseTime(r.Timestamp).After(since)) {
			return
		}
		if r.Type == "assistant" && r.Message.Model != "" && !strings.HasPrefix(r.Message.Model, "<") {
			sc.seen.Replied = r.Message.Model
		}
		return
	}
	var r struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(l, &r) != nil || (!since.IsZero() && !parseTime(r.Timestamp).After(since)) {
		return
	}
	if r.Type == "assistant" {
		if r.Message.Model != "" && !strings.HasPrefix(r.Message.Model, "<") {
			sc.seen.Replied = r.Message.Model
		}
		return
	}
	if r.Type != "user" {
		return
	}
	text := recordText(r.Message.Content)
	if m := localCmdRe.FindStringSubmatch(text); m != nil {
		sc.cmd, sc.arg = m[1], ""
		if a := localArgsRe.FindStringSubmatch(text); a != nil {
			sc.arg = strings.TrimSpace(a[1])
		}
		return
	}
	m := localOutRe.FindStringSubmatch(text)
	if m == nil {
		return
	}
	seen := &sc.seen
	out := ansiRe.ReplaceAllString(strings.TrimSpace(m[1]), "")
	if s := setModelRe.FindStringSubmatch(out); s != nil {
		shown := s[1] + s[2]
		switch {
		case sc.cmd == "model" && sc.arg != "" && strings.ToLower(sc.arg) != "default":
			seen.HasModel, seen.Model, seen.Follow = true, sc.arg, false
		default:
			if id, ok := claudeModelID(shown); ok {
				seen.HasModel, seen.Model, seen.Follow = true, id, false
			} else {
				seen.HasModel, seen.Follow, seen.Replied = false, true, ""
			}
		}
	} else if s := setEffortRe.FindStringSubmatch(out); s != nil {
		seen.HasEffort, seen.Effort = true, s[1]
		if seen.Effort == "auto" {
			seen.Effort = ""
		}
	}
	sc.cmd, sc.arg = "", ""
}

// scanCodexModel: a rollout records the model and effort of every turn.
func scanCodexModel(data []byte, partial bool, since time.Time) ModelSeen {
	var seen ModelSeen
	for _, l := range lines(data, partial) {
		codexModelLine(&seen, l, since)
	}
	return seen
}

func codexModelLine(seen *ModelSeen, l []byte, since time.Time) {
	if !bytes.Contains(l, markTurnCtx) {
		return
	}
	var r struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Model  string `json:"model"`
			Effort string `json:"effort"`
			Mode   struct {
				Settings struct {
					Effort string `json:"reasoning_effort"`
				} `json:"settings"`
			} `json:"collaboration_mode"`
		} `json:"payload"`
	}
	if json.Unmarshal(l, &r) != nil || r.Type != "turn_context" || (!since.IsZero() && !parseTime(r.Timestamp).After(since)) {
		return
	}
	if r.Payload.Model != "" {
		seen.HasModel, seen.Model, seen.Replied = true, r.Payload.Model, r.Payload.Model
	}
	effort := r.Payload.Effort
	if effort == "" {
		effort = r.Payload.Mode.Settings.Effort
	}
	if effort != "" {
		seen.HasEffort, seen.Effort = true, effort
	}
}

// ModelScan carries ReadModelFrom from one call to the next for one console: how far its
// transcript was read and what that said, so that the next call reads only what was
// appended. The zero value starts with the transcript's last TailMax bytes.
type ModelScan struct {
	path, kind string
	since      time.Time
	off        int64 // the end of the last complete line read
	claude     claudeModelScan
	codex      ModelSeen
}

func (sc *ModelScan) seen() ModelSeen {
	if sc.kind == KindClaude {
		return sc.claude.seen
	}
	return sc.codex
}

// ReadModel reads the end of a transcript for what happened after since.
func ReadModel(ctx context.Context, h machine.Host, kind, path string, since time.Time) (ModelSeen, error) {
	var sc ModelScan
	return ReadModelFrom(ctx, h, kind, path, since, &sc)
}

// ReadModelFrom is ReadModel continued from sc: only what was appended to the transcript since
// the last call is read. It starts again from the last TailMax bytes when the path, the kind or
// since changed, when the file shrank (rewritten), or when more than TailMax bytes were added.
func ReadModelFrom(ctx context.Context, h machine.Host, kind, path string, since time.Time, sc *ModelScan) (ModelSeen, error) {
	if kind != KindClaude && kind != KindCodex {
		return ModelSeen{}, ErrUnknownKind
	}
	if err := takeSlot(ctx); err != nil {
		return ModelSeen{}, err
	}
	defer giveSlot()
	st, err := h.Stat(ctx, path)
	if err != nil {
		return ModelSeen{}, err
	}
	if !st.Exists {
		return ModelSeen{}, fs.ErrNotExist
	}
	if st.Dir {
		return ModelSeen{}, machine.ErrIsDir
	}
	if sc.path != path || sc.kind != kind || !sc.since.Equal(since) || st.Size < sc.off || st.Size-sc.off > TailMax {
		*sc = ModelScan{path: path, kind: kind, since: since}
	}
	base, cut := sc.off, false
	var data []byte
	switch {
	case st.Size <= base:
		return sc.seen(), nil
	case base == 0 && st.Size > TailMax:
		// as readTail: one byte before the tail tells whether it starts on a line
		base = st.Size - TailMax - 1
		if data, _, err = h.ReadAt(ctx, path, base, TailMax+1); err != nil {
			return ModelSeen{}, err
		}
		if len(data) > 0 && data[0] == '\n' {
			data, base = data[1:], base+1
		} else {
			cut = true
		}
	default:
		if data, _, err = h.ReadAt(ctx, path, base, st.Size-base); err != nil {
			return ModelSeen{}, err
		}
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return sc.seen(), nil // no complete line yet: it is read when it is
	}
	for _, l := range lines(data[:end+1], cut) {
		if kind == KindClaude {
			sc.claude.line(l, since)
		} else {
			codexModelLine(&sc.codex, l, since)
		}
	}
	sc.off = base + int64(end) + 1
	return sc.seen(), nil
}
