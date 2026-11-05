package machine

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeBin is a folder of commands that do nothing, for a PATH under the test's control.
func fakeBin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The probe runs in whatever login shell the server has. The default of
// `useradd` on Debian is /bin/sh, which is dash.
func TestProbeFindsEveryCommandInEachShell(t *testing.T) {
	bin := fakeBin(t, "tmux", "curl", "claude", "codex")
	system := "/usr/bin:/bin"
	commands := []string{"claude", "codex", "gemini", "$SHELL", ""}
	for _, shell := range []string{"sh", "dash", "bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		t.Run(shell, func(t *testing.T) {
			cmd := exec.Command(path, "-c", probeScript(commands))
			cmd.Env = []string{"PATH=" + bin + ":" + system, "HOME=/home/alice", "SHELL=" + path}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			p := parseProbe(string(out), commands)
			if !p.Tmux {
				t.Error("tmux not found")
			}
			for _, c := range []string{"curl", "claude", "codex"} {
				if p.Commands[c] != filepath.Join(bin, c) {
					t.Errorf("%s = %q, want it in %s", c, p.Commands[c], bin)
				}
			}
			if _, ok := p.Commands["gemini"]; ok {
				t.Error("found a command that is not there")
			}
			if p.Home != "/home/alice" || p.Shell != path || p.OS == "" {
				t.Errorf("%+v", p)
			}
		})
	}
}

func TestParseProbe(t *testing.T) {
	out := "Welcome to the server!\n" + probeBegin + "\nLinux x86_64\n/bin/bash\n/usr/bin/tmux\n/usr/bin/curl\n/opt/bin/claude\n" +
		probeTmuxV + "tmux 3.4\n/home/alice\n" + probeEnd + "\nbye\n"
	p := parseProbe(out, []string{"claude", "$SHELL"})
	if p.OS != "Linux" || p.Arch != "x86_64" || p.Shell != "/bin/bash" || p.Home != "/home/alice" {
		t.Errorf("%+v", p)
	}
	if !p.Tmux || p.TmuxVersion != "3.4" {
		t.Errorf("tmux %v %q", p.Tmux, p.TmuxVersion)
	}
	if p.Commands["curl"] != "/usr/bin/curl" || p.Commands["claude"] != "/opt/bin/claude" {
		t.Errorf("%v", p.Commands)
	}

	// Nothing installed.
	p = parseProbe(probeBegin+"\nLinux x86_64\n/bin/sh\n"+probeTmuxV+"\n/root\n"+probeEnd, []string{"claude"})
	if p.Tmux || p.TmuxVersion != "" || len(p.Commands) != 0 || p.Home != "/root" {
		t.Errorf("%+v", p)
	}

	// Output cut short: no guessing.
	if p := parseProbe("garbage", nil); p.Tmux || p.Home != "" || p.OS != "" {
		t.Errorf("%+v", p)
	}
}
