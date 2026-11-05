package transcript

import (
	"testing"
	"time"
)

func TestClaudeModelFromTranscript(t *testing.T) {
	reply := func(at, model string) string {
		return `{"type":"assistant","timestamp":"` + at + `","message":{"model":"` + model + `","content":[{"type":"text","text":"ok"}]}}` + "\n"
	}
	cmd := func(at, name, args, out string) string {
		return `{"type":"user","timestamp":"` + at + `","message":{"content":"<command-name>/` + name + `</command-name> <command-message>` + name + `</command-message> <command-args>` + args + `</command-args>"}}` + "\n" +
			`{"type":"user","timestamp":"` + at + `","message":{"content":"<local-command-stdout>` + out + `</local-command-stdout>"}}` + "\n"
	}
	const t1, t2, t3 = "2026-11-04T01:00:00.000Z", "2026-11-04T02:00:00.000Z", "2026-11-04T03:00:00.000Z"

	seen := scanClaudeModel([]byte(reply(t1, "claude-opus-5-5")), false, time.Time{})
	if seen.HasModel || seen.Replied != "claude-opus-5-5" {
		t.Fatalf("a reply alone: %+v", seen)
	}
	// named on the command line
	seen = scanClaudeModel([]byte(reply(t1, "claude-opus-5-5")+cmd(t2, "model", "claude-sonnet-5-5", "Set model to `Sonnet 5.5` and saved as your default for new sessions")), false, time.Time{})
	if !seen.HasModel || seen.Model != "claude-sonnet-5-5" {
		t.Fatalf("/model with a name: %+v", seen)
	}
	// from the picker: only the printed name to go on
	for shown, want := range map[string]string{
		"Set model to `Sonnet 5.5` and saved as your default for new sessions": "claude-sonnet-5-5",
		"Set model to \\u001b[1mHaiku 5.5\\u001b[22m":                            "claude-haiku-5-5",
		"Set model to `Fable 5.1 (1M context)`":                                "claude-fable-5-1[1m]",
		"Set model to `claude-opus-5-5`":                                       "claude-opus-5-5",
		"Set model to `Default (Opus 5.5)`":                                    "",
	} {
		seen = scanClaudeModel([]byte(cmd(t2, "model", "", shown)), false, time.Time{})
		if !seen.HasModel || seen.Model != want {
			t.Fatalf("%q: %+v, want %q", shown, seen, want)
		}
	}
	// a name nobody can read: the next reply tells
	seen = scanClaudeModel([]byte(cmd(t2, "model", "", "Set model to `Something New`")+reply(t3, "claude-new-1")), false, time.Time{})
	if seen.HasModel || !seen.Follow || seen.Replied != "claude-new-1" {
		t.Fatalf("unreadable name: %+v", seen)
	}
	// a /model that did not take changes nothing
	seen = scanClaudeModel([]byte(cmd(t2, "model", "nonsense", "Model nonsense not found")), false, time.Time{})
	if seen.HasModel {
		t.Fatalf("a refused /model: %+v", seen)
	}
	// effort
	seen = scanClaudeModel([]byte(cmd(t2, "effort", "xhigh", "Set effort level to xhigh (saved as your default for new sessions): Deeper reasoning")), false, time.Time{})
	if !seen.HasEffort || seen.Effort != "xhigh" {
		t.Fatalf("/effort: %+v", seen)
	}
	seen = scanClaudeModel([]byte(cmd(t2, "effort", "auto", "Set effort level to auto")), false, time.Time{})
	if !seen.HasEffort || seen.Effort != "" {
		t.Fatalf("/effort auto: %+v", seen)
	}
	// what came before the hub set the model no longer counts
	data := []byte(cmd(t1, "model", "claude-sonnet-5-5", "Set model to `Sonnet 5.5`"))
	seen = scanClaudeModel(data, false, parseTime(t2))
	if seen.HasModel {
		t.Fatalf("an old /model: %+v", seen)
	}
}

func TestCodexModelFromRollout(t *testing.T) {
	ctx := func(at, rest string) string {
		return `{"timestamp":"` + at + `","type":"turn_context","payload":{` + rest + `}}` + "\n"
	}
	const t1, t2 = "2026-11-04T01:00:00.000Z", "2026-11-04T02:00:00.000Z"
	seen := scanCodexModel([]byte(ctx(t1, `"model":"gpt-6-luna","effort":"low"`)+ctx(t2, `"model":"gpt-6.1-sol","collaboration_mode":{"settings":{"reasoning_effort":"high"}}`)), false, time.Time{})
	if !seen.HasModel || seen.Model != "gpt-6.1-sol" || !seen.HasEffort || seen.Effort != "high" {
		t.Fatalf("%+v", seen)
	}
	seen = scanCodexModel([]byte(ctx(t2, `"model":"gpt-6.1-sol","collaboration_mode":{"settings":{"reasoning_effort":null}}`)), false, time.Time{})
	if seen.HasEffort {
		t.Fatalf("no effort recorded: %+v", seen)
	}
	seen = scanCodexModel([]byte(ctx(t1, `"model":"gpt-6-luna"`)), false, parseTime(t2))
	if seen.HasModel {
		t.Fatalf("before since: %+v", seen)
	}
}
