package screen

import (
	"strings"
	"testing"
)

// TestControlStringsNeverPrint: a window title holding a character whose UTF-8 has the byte
// 0x9C (the 8-bit ST), as Claude Code's spinner "✳" does, must not end early and print the rest
// over the screen. Claude then draws the spaces of a prompt by moving the cursor, trusting that
// the cells are blank (found 2026-11-13 with Claude Code 2.1.323: "draft for later" read as
// "draftdforolater").
func TestControlStringsNeverPrint(t *testing.T) {
	s := New(40, 4, nil)
	defer s.Close()
	s.Feed([]byte("\x1b[2;1H\x1b[K❯ "))
	s.Feed([]byte("\x1b]0;✳ Claude Code\x07"))
	s.Feed([]byte("\x1b]0;✶ Claude")) // split across two reads
	s.Feed([]byte(" Code\x07"))
	s.Feed([]byte("\x1b]8;;https://example.com\x1b\\")) // ended by ESC \
	s.Feed([]byte("\x1bP+q544e\x1b\\"))                 // a DCS query
	s.Feed([]byte("\x1b[2;3Hdraft\x1b[9Gfor\x1b[13Glater"))
	s.Sync()
	if got := strings.TrimRight(s.Snapshot().Lines[1], " "); got != "❯ draft for later" {
		t.Fatalf("%q", got)
	}
	for _, l := range s.Snapshot().Lines {
		if strings.Contains(l, "Claude") || strings.Contains(l, "example") || strings.Contains(l, "544e") {
			t.Fatalf("a control string printed: %q", l)
		}
	}
}

func TestStringStripper(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"plain text"}, "plain text"},
		{[]string{"a\x1b[31mb"}, "a\x1b[31mb"},                          // CSI passes
		{[]string{"a\x1b]0;title\x07b"}, "ab"},                          // OSC with BEL
		{[]string{"a\x1b]0;t", "itle\x1b", "\\b"}, "ab"},                // split, ESC \ across reads
		{[]string{"a\x1b]0;x\x1b[1mb"}, "a\x1b[1mb"},                    // aborted by another sequence
		{[]string{"a\x1b", "[2Jb"}, "a\x1b[2Jb"},                        // ESC at the end of a read
		{[]string{"\x1b_apc\x1b\\\x1b^pm\x1b\\\x1bXsos\x1b\\ok"}, "ok"}, // APC, PM, SOS
		{[]string{"a\x1b\x1b[Hb"}, "a\x1b\x1b[Hb"},                      // ESC ESC
	} {
		var f stringStripper
		got := ""
		for _, p := range c.in {
			got += string(f.strip([]byte(p)))
		}
		if got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}
