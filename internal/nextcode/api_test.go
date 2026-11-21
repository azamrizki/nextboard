package nextcode

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// The API engine's command line is the agent's own launch and the permission mode, none of
// Claude Code's flags; the base is not changed.
func TestAPIEngineArgv(t *testing.T) {
	e, ok := newEngine(EngineAPI).(*apiEngine)
	if !ok {
		t.Fatalf("engine %T", newEngine(EngineAPI))
	}
	base := []string{"/opt/nextboard", "nextcode-agent", "--session-id", "0199a1b2-c3d4-4e5f-8a9b-0c1d2e3f4a5b", "--model", "glm-4.6"}
	keep := slices.Clone(base)
	if got, want := e.argv(base, "acceptEdits"), append(slices.Clone(base), "--permission-mode", "acceptEdits"); !slices.Equal(got, want) {
		t.Fatalf("argv %q, want %q", got, want)
	}
	if got := e.argv(base, ""); !slices.Equal(got, base) {
		t.Fatalf("argv without a mode %q", got)
	}
	if !slices.Equal(base, keep) {
		t.Fatalf("base changed: %q", base)
	}
}

// The agent speaks Claude Code's stream-json, so a console on the API engine runs a turn the
// way a Claude one does (the Claude fake plays the agent here), with its own command line.
func TestAPIEngineTurn(t *testing.T) {
	dir := t.TempDir()
	r := newRigWith(t, dir, 0, rigOpts{engine: EngineAPI, mode: "acceptEdits", env: []string{"NEXTCODE_FAKE=claude"},
		base: func(resume bool) []string {
			if resume {
				return []string{os.Args[0], "nextcode-agent", "--resume", "conv-1"}
			}
			return []string{os.Args[0], "nextcode-agent", "--session-id", "conv-1"}
		}})
	if err := r.d.Send("hello"); err != nil {
		t.Fatal(err)
	}
	if txt := r.item(KindText); txt.Text != "Hi there" {
		t.Fatalf("text item %+v", txt)
	}
	r.turnEnded()
	if st := r.d.Status(); st.Engine != EngineAPI || st.Conversation != "conv-1" {
		t.Fatalf("status %+v", st)
	}
	b, err := os.ReadFile(r.args)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "nextcode-agent --session-id conv-1 --permission-mode acceptEdits" {
		t.Fatalf("the agent's arguments %q", got)
	}
}
