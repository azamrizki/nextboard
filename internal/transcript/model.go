package transcript

import (
	"context"
	"encoding/json"
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

// scanClaudeModel walks a Claude Code transcript: /model and /effort are local
// commands, each followed by what it printed.
func scanClaudeModel(data []byte, partial bool, since time.Time) ModelSeen {
	var seen ModelSeen
	cmd, arg := "", ""
	for _, l := range lines(data, partial) {
		var r struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Model   string          `json:"model"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(l, &r) != nil || (!since.IsZero() && !parseTime(r.Timestamp).After(since)) {
			continue
		}
		if r.Type == "assistant" {
			if r.Message.Model != "" && !strings.HasPrefix(r.Message.Model, "<") {
				seen.Replied = r.Message.Model
			}
			continue
		}
		if r.Type != "user" {
			continue
		}
		text := recordText(r.Message.Content)
		if m := localCmdRe.FindStringSubmatch(text); m != nil {
			cmd, arg = m[1], ""
			if a := localArgsRe.FindStringSubmatch(text); a != nil {
				arg = strings.TrimSpace(a[1])
			}
			continue
		}
		m := localOutRe.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		out := ansiRe.ReplaceAllString(strings.TrimSpace(m[1]), "")
		if s := setModelRe.FindStringSubmatch(out); s != nil {
			shown := s[1] + s[2]
			switch {
			case cmd == "model" && arg != "" && strings.ToLower(arg) != "default":
				seen.HasModel, seen.Model, seen.Follow = true, arg, false
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
		cmd, arg = "", ""
	}
	return seen
}

// scanCodexModel: a rollout records the model and effort of every turn.
func scanCodexModel(data []byte, partial bool, since time.Time) ModelSeen {
	var seen ModelSeen
	for _, l := range lines(data, partial) {
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
			continue
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
	return seen
}

// ReadModel reads the end of a transcript for what happened after since.
func ReadModel(ctx context.Context, h machine.Host, kind, path string, since time.Time) (ModelSeen, error) {
	if kind != KindClaude && kind != KindCodex {
		return ModelSeen{}, ErrUnknownKind
	}
	data, _, cut, err := readTail(ctx, h, path, TailMax)
	if err != nil {
		return ModelSeen{}, err
	}
	if kind == KindClaude {
		return scanClaudeModel(data, cut, since), nil
	}
	return scanCodexModel(data, cut, since), nil
}
