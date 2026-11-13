package session

import (
	"testing"

	"github.com/azamrizki/nextboard/internal/screen"
)

// TestDraftTrack: what is typed at the prompt is followed exactly, or marked unsure.
func TestDraftTrack(t *testing.T) {
	for _, c := range []struct {
		why   string
		in    []string
		draft string
		sure  bool
	}{
		{"nothing", nil, "", true},
		{"typed", []string{"fix the ", "login"}, "fix the login", true},
		{"backspace", []string{"fixx\x7f the bug"}, "fix the bug", true},
		{"submitted", []string{"first\r"}, "", true},
		{"submitted, then more", []string{"first\rsecond"}, "second", true},
		{"utf-8", []string{"perbaiki ✓ ü"}, "perbaiki ✓ ü", true},
		{"paste", []string{"see: ", "\x1b[200~line one\rline two\x1b[201~"}, "see: line one\nline two", true},
		{"paste with CRLF", []string{"\x1b[200~a\r\nb\x1b[201~"}, "a\nb", true},
		{"paste split", []string{"\x1b[20", "0~x\x1b[2", "01~y"}, "xy", true},
		{"arrow key", []string{"abc\x1b[D"}, "", false},
		{"history", []string{"\x1b[A"}, "", false},
		{"lone escape", []string{"abc\x1b"}, "", false},
		{"ctrl-u", []string{"abc\x15"}, "", false},
		{"tab", []string{"ab\t"}, "", false},
		{"unsure until submit", []string{"\x1b[Aedit\r", "new"}, "new", true},
		{"open paste", []string{"\x1b[200~half"}, "", false},
	} {
		var d draftTrack
		for _, p := range c.in {
			d.feed([]byte(p))
		}
		got, ok := d.get()
		if ok != c.sure || (ok && got != c.draft) {
			t.Errorf("%s: %q %v, want %q %v", c.why, got, ok, c.draft, c.sure)
		}
	}
}

func TestDraftShows(t *testing.T) {
	box := screen.Snapshot{Lines: []string{"● done", "╭──────────╮", "│ > fix the │", "│ login bug │", "╰──────────╯"}}
	if !draftShows(box, "fix the login bug") {
		t.Fatal("a wrapped draft in a box")
	}
	if draftShows(box, "something else") {
		t.Fatal("another text")
	}
	folded := screen.Snapshot{Lines: []string{"❯ [Pasted text #1 +40 lines] "}}
	if !draftShows(folded, "a\nb\nc") {
		t.Fatal("a folded paste")
	}
	multi := screen.Snapshot{Lines: []string{"› first line", "  second line"}}
	if !draftShows(multi, "first line\nsecond line\n") {
		t.Fatal("the last line of a multi-line draft")
	}
}

// TestCommandRunning: a shell below the agent is a command it runs; the wrapper shell above
// it and the agent's other helpers are not.
func TestCommandRunning(t *testing.T) {
	procs := []procInfo{
		{100, 1, "sh"},       // wrapper
		{101, 100, "claude"}, // the agent
		{102, 101, "node"},   // an MCP server
	}
	if commandRunning(procs, 100) {
		t.Fatal("idle agent")
	}
	procs = append(procs, procInfo{103, 101, "bash"}, procInfo{104, 103, "npm"})
	if !commandRunning(procs, 100) {
		t.Fatal("a command the agent started")
	}
	if !commandRunning([]procInfo{{100, 1, "sh"}, {101, 100, "codex"}, {102, 101, "node"}, {103, 102, "zsh"}}, 100) {
		t.Fatal("a shell deeper down")
	}
}
