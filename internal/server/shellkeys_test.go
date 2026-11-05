package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A console whose agent exited sits at the user's own shell. Starting the agent
// again types a line into it (docs/API-agents.md §3.3), and that shell may be any
// shell in any mode, with anything left on its command line. Whatever was left
// there must not run: it was never submitted, and it could be anything.

type loginShell struct {
	name string
	rc   map[string]string // files below HOME
}

func testedShells() []loginShell {
	all := []loginShell{
		{name: "bash", rc: map[string]string{".bash_profile": "PS1='$ '\n"}},
		{name: "bash vi", rc: map[string]string{".bash_profile": "PS1='$ '\nset -o vi\n"}},
		{name: "zsh", rc: map[string]string{".zshrc": "PS1='$ '\n"}},
		{name: "zsh vi", rc: map[string]string{".zshrc": "PS1='$ '\nbindkey -v\n"}},
		{name: "fish", rc: map[string]string{".config/fish/config.fish": "set -g fish_greeting\n"}},
		{name: "fish vi", rc: map[string]string{".config/fish/config.fish": "set -g fish_greeting\nfish_vi_key_bindings\n"}},
	}
	var out []loginShell
	for _, s := range all {
		if _, err := exec.LookPath(strings.Fields(s.name)[0]); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// shellConsole is a console of an agent that exits at once, at a prompt of s.
type shellConsole struct {
	h    *testHub
	id   string
	runs string // one line per agent start
	term *termConn
}

func newShellConsole(t *testing.T, s loginShell) *shellConsole {
	t.Helper()
	shell, err := exec.LookPath(strings.Fields(s.name)[0])
	if err != nil {
		t.Skip(err)
	}
	home := t.TempDir()
	for name, text := range s.rc {
		p := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", shell)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	agentBin := filepath.Join(dir, "plainagent")
	if err := os.WriteFile(agentBin, []byte("#!/bin/sh\necho run >> "+runs+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "plain", "name": "Plain", "command": agentBin}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "plain", "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("console %d %v", st, in)
	}
	id := in["id"].(string)
	h.waitInfo(id, "the agent to exit into the shell", func(in map[string]any) bool { return in["foreground"] == "shell" })
	c := &shellConsole{h: h, id: id, runs: runs, term: h.openTerm(id, h.cookieHeader())}
	c.syncFish()
	return c
}

// syncFish is sync in words every shell here takes: fish has no $((…)).
func (c *shellConsole) syncFish() {
	c.term.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		c.term.syncs++
		n := strconv.Itoa(c.term.syncs)
		c.term.send(`echo nb-sync-"` + n + `"` + "\r")
		if c.term.waitFor("nb-sync-"+n+"\r\n", 2*time.Second) {
			break
		}
		if time.Now().After(deadline) {
			c.term.t.Fatalf("the shell does not answer; got %q", c.term.output())
		}
	}
	c.term.mu.Lock()
	c.term.out.Reset()
	c.term.mu.Unlock()
}

func (c *shellConsole) runCount() int {
	raw, err := os.ReadFile(c.runs)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), "\n")
}

// startAgent asks the hub to type the wrapper line, past its guard for a user who
// is still typing.
func (c *shellConsole) startAgent() (int, map[string]any) {
	time.Sleep(2200 * time.Millisecond)
	return c.h.do("POST", "/api/sessions/"+c.id+"/agent", map[string]any{"mode": "fresh"}, nil)
}

func TestStartingAnAgentInAnyShell(t *testing.T) {
	shells := testedShells()
	if len(shells) == 0 {
		t.Skip("no shell")
	}
	for _, s := range shells {
		t.Run(s.name, func(t *testing.T) {
			lines := map[string]string{
				"empty line":      "",
				"half a line":     "touch %s",
				"cursor mid-line": "touch %s\x1b[D\x1b[D\x1b[D",
				"a word after it": "touch %s; echo tail\x1b[D\x1b[D\x1b[D\x1b[D",
			}
			if strings.HasSuffix(s.name, " vi") {
				lines["vi command mode"] = "touch %s\x1b"
				lines["vi command mode, cursor at the start"] = "touch %s\x1b0"
			}
			for name, typed := range lines {
				t.Run(name, func(t *testing.T) {
					c := newShellConsole(t, s)
					c.term.t = t
					marker := filepath.Join(t.TempDir(), "ran")
					if typed != "" {
						c.term.send(strings.Replace(typed, "%s", marker, 1))
					}
					st, r := c.startAgent()
					if st != 200 {
						t.Errorf("start: %d %v", st, r)
					}
					time.Sleep(1500 * time.Millisecond)
					if _, err := os.Stat(marker); err == nil {
						t.Errorf("what was left on the line ran: screen %q", c.term.output())
					}
					if got := c.runCount(); got != 2 {
						t.Errorf("agent started %d times, want 2; screen %q", got, c.term.output())
					}
				})
			}
		})
	}
}

// Return is only pressed on a line the screen shows. Here the shell mangles what is
// typed, as a key binding might: nothing may run, and the hub says it failed.
func TestNoReturnOnALineTheScreenDoesNotShow(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	s := loginShell{name: "bash", rc: map[string]string{".bash_profile": "PS1='$ '\nbind '\"x\":\"touch " + marker + "\"'\n"}}
	c := newShellConsole(t, s)
	c.term.t = t
	st, r := c.startAgent()
	if st != 412 || r["code"] != "wrap_missing" {
		t.Errorf("start: %d %v", st, r)
	}
	time.Sleep(time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("Return ran a line nobody chose; screen %q", c.term.output())
	}
	if got := c.runCount(); got != 1 {
		t.Errorf("the agent started %d times", got)
	}
	// The line was cancelled again: the shell is usable.
	c.syncFish()
}

// What is left after the cursor must not become part of the line: a redirection
// there would run with it, and empty the file it names.
func TestLeftoverAfterTheCursorDoesNotJoinTheLine(t *testing.T) {
	for _, s := range testedShells() {
		t.Run(s.name, func(t *testing.T) {
			c := newShellConsole(t, s)
			c.term.t = t
			victim := filepath.Join(t.TempDir(), "important.txt")
			if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
				t.Fatal(err)
			}
			tail := " > " + victim
			c.term.send("echo draft" + tail + strings.Repeat("\x1b[D", len(tail)))
			if st, r := c.startAgent(); st != 200 {
				t.Errorf("start: %d %v", st, r)
			}
			time.Sleep(time.Second)
			if raw, _ := os.ReadFile(victim); string(raw) != "keep me" {
				t.Errorf("the file was overwritten: %q; screen %q", raw, c.term.output())
			}
		})
	}
}

// A fake agent at a line prompt, for Ctrl-Z. It reports each line it gets.
func newPromptAgent(t *testing.T, h *testHub) (id, got string) {
	t.Helper()
	dir := t.TempDir()
	got = filepath.Join(dir, "got")
	bin := filepath.Join(dir, "promptagent")
	script := fmt.Sprintf(`#!/bin/sh
while :; do
  printf '> '
  read line || exit 0
  echo "$line" >> %s
done
`, got)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "promptagent", "name": "Prompt", "command": bin}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "promptagent", "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("console %d %v", st, in)
	}
	return in["id"].(string), got
}

func agentPid(t *testing.T, script string) int {
	t.Helper()
	raw, err := exec.Command("pgrep", "-f", "-n", script).Output()
	if err != nil {
		t.Fatalf("no agent process: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// The wrapper leads its terminal session, so the agent's process group is orphaned
// and the kernel ignores Ctrl-Z there: there is no shell to return to, and no fg.
func TestCtrlZDoesNotStopAnAgent(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	id, got := newPromptAgent(t, h)
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" })
	pid := agentPid(t, filepath.Join(filepath.Dir(got), "promptagent"))
	term := h.openTerm(id, h.cookieHeader())

	term.send("\x1a")
	time.Sleep(1500 * time.Millisecond)
	if st := procStateOf(pid); st == "T" {
		t.Fatalf("Ctrl-Z stopped the agent: state %s", st)
	}
	term.send("after\r")
	waitTrue(t, "the agent to take the line", 10*time.Second, func() bool {
		raw, _ := os.ReadFile(got)
		return strings.Contains(string(raw), "after\n")
	})
}

// A program can still stop itself with a real SIGSTOP, and Resume brings it back.
func TestResumeBringsBackAnAgentThatStoppedItself(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	id, got := newPromptAgent(t, h)
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" })
	pid := agentPid(t, filepath.Join(filepath.Dir(got), "promptagent"))
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "stopped", 3*time.Second, func() bool { return procStateOf(pid) == "T" })
	term := h.openTerm(id, h.cookieHeader())
	term.send("waiting\r")
	if st, r := h.do("POST", "/api/sessions/"+id+"/resume", nil, nil); st != 200 {
		t.Fatalf("resume %d %v", st, r)
	}
	waitTrue(t, "the agent to go on", 10*time.Second, func() bool {
		raw, _ := os.ReadFile(got)
		return strings.Contains(string(raw), "waiting\n")
	})
}

// In a plain shell job control is the shell's, and Ctrl-Z works as anywhere.
func TestCtrlZStillWorksInAShell(t *testing.T) {
	for _, s := range testedShells() {
		if strings.HasSuffix(s.name, " vi") {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			c := newShellConsole(t, s)
			c.term.t = t
			c.term.send("sleep 300\r")
			time.Sleep(time.Second)
			c.term.send("\x1a")
			time.Sleep(500 * time.Millisecond)
			// The shell is back: it answers, and the job is there to kill.
			c.syncFish()
			c.term.send("jobs\r")
			c.term.mustSee("sleep 300")
			c.term.send("kill %1\r")
		})
	}
}

// A pause from the hub is meant to stay until it is resumed.
func TestHubPauseIsNotUndone(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	id, got := newPromptAgent(t, h)
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" })
	if st, p := h.do("POST", "/api/sessions/"+id+"/pause", nil, nil); st != 200 || p["paused"] != true {
		t.Fatalf("pause %d %v", st, p)
	}
	term := h.openTerm(id, h.cookieHeader())
	term.send("while paused\r")
	time.Sleep(3 * time.Second)
	if raw, _ := os.ReadFile(got); strings.Contains(string(raw), "while paused") {
		t.Fatal("the pause was undone")
	}
	if st, p := h.do("POST", "/api/sessions/"+id+"/resume", nil, nil); st != 200 || p["paused"] != false {
		t.Fatalf("resume %d %v", st, p)
	}
	waitTrue(t, "the agent to go on after resume", 10*time.Second, func() bool {
		raw, _ := os.ReadFile(got)
		return strings.Contains(string(raw), "while paused\n")
	})
}
