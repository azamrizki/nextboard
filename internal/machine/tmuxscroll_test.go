package machine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The wheel over a console kept in tmux: the script TmuxScroll runs, against a tmux that only
// says what it was asked and answers display-message from ALT and MODE.
func TestTmuxScrollScript(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\necho \"$*\" >> \"$LOG\"\n" +
		"case \"$*\" in display-message*alternate_on*) echo \"$ALT\";; display-message*pane_in_mode*) echo \"$MODE\";; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(n int, alt, mode string) []string {
		t.Helper()
		script, err := tmuxScrollScript("nb-h1-s1", n)
		if err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(t.TempDir(), "log")
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "LOG="+log, "ALT="+alt, "MODE="+mode)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		b, _ := os.ReadFile(log)
		var calls []string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if !strings.HasPrefix(l, "display-message") {
				calls = append(calls, l)
			}
		}
		return calls
	}
	// up at the shell: copy mode, then three lines of history
	if got := run(-3, "0", "0"); len(got) != 1 || got[0] != "copy-mode -e -t nb-h1-s1 ; send-keys -X -t nb-h1-s1 scroll-up ; send-keys -X -t nb-h1-s1 scroll-up ; send-keys -X -t nb-h1-s1 scroll-up" {
		t.Errorf("up: %q", got)
	}
	// down while in copy mode: back toward the bottom
	if got := run(2, "0", "1"); len(got) != 1 || got[0] != "send-keys -X -t nb-h1-s1 scroll-down ; send-keys -X -t nb-h1-s1 scroll-down" {
		t.Errorf("down in copy mode: %q", got)
	}
	// down at the bottom already: nothing, never arrow keys into the shell
	if got := run(2, "0", "0"); len(got) != 0 {
		t.Errorf("down at the bottom: %q", got)
	}
	// a full-screen program (vim, less) gets the keys the wheel sent before
	if got := run(-2, "1", "0"); len(got) != 1 || got[0] != "send-keys -t nb-h1-s1 Up Up" {
		t.Errorf("up in vim: %q", got)
	}
	if got := run(1, "1", "0"); len(got) != 1 || got[0] != "send-keys -t nb-h1-s1 Down" {
		t.Errorf("down in vim: %q", got)
	}
	if s, err := tmuxScrollScript("nb-h1-s1", 0); s != "" || err != nil {
		t.Errorf("zero: %q %v", s, err)
	}
	if _, err := tmuxScrollScript("x; rm -rf /", -1); err == nil {
		t.Error("a bad name was taken")
	}
	if s, _ := tmuxScrollScript("nb-h1-s1", -1000); strings.Count(s, "scroll-up") != MaxTmuxScroll {
		t.Errorf("not bounded: %d", strings.Count(s, "scroll-up"))
	}
}
