package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/machine"
)

// noRipgrep runs the remote search tools as on a server without ripgrep: a login sh whose PATH
// has what /usr/bin has but rg, and a home of its own.
func noRipgrep(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	entries, err := os.ReadDir("/usr/bin")
	if err != nil {
		t.Skip(err)
	}
	for _, e := range entries {
		if e.Name() != "rg" {
			_ = os.Symlink(filepath.Join("/usr/bin", e.Name()), filepath.Join(bin, e.Name()))
		}
	}
	for _, name := range []string{"sh", "git", "grep"} {
		if _, err := os.Stat(filepath.Join(bin, name)); err != nil {
			t.Skipf("no %s", name)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Without ripgrep a search in a git work tree leaves out what .gitignore does (logs that took
// a plain grep minutes), and outside of one it leaves out logs, dependencies and .git. A file it
// may not read never costs what it found.
func TestRemoteGrepWithoutRipgrep(t *testing.T) {
	noRipgrep(t)
	ctx := context.Background()
	host := machine.NewLocal(machine.Config{})

	repo := t.TempDir()
	write(t, repo, map[string]string{
		".gitignore":      "logs/\nstorage/\n",
		"app/Prize.php":   "<?php\n$prize_history = 1;\n",
		"app/notes.txt":   "prize_history too\n",
		"logs/access.log": "prize_history in a log\n",
		"storage/x.php":   "prize_history kept out\n",
		"new/Draft.php":   "prize_history not yet added\n",
	})
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".gitignore", "app")
	git("commit", "-qm", "init")

	text, err := runRemoteGrep(ctx, host, repo, "prize_history", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"app/Prize.php:2:", "app/notes.txt:1:", "new/Draft.php:1:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %q", want, text)
		}
	}
	if strings.Contains(text, "logs/") || strings.Contains(text, "storage/") {
		t.Fatalf("searched what .gitignore leaves out: %q", text)
	}
	if text, err := runRemoteGrep(ctx, host, repo, "prize_history", "app", "*.php", false); err != nil || strings.TrimSpace(text) != "app/Prize.php:2:$prize_history = 1;" {
		t.Fatalf("include in a folder %q %v", text, err)
	}
	if text, err := runRemoteGrep(ctx, host, repo, "PRIZE_HISTORY", "", "*.txt", true); err != nil || !strings.Contains(text, "app/notes.txt:1:") || strings.Contains(text, ".php") {
		t.Fatalf("ignore case and include %q %v", text, err)
	}
	if text, err := runRemoteGrep(ctx, host, repo, "no-such-text", "", "", false); err != nil || text != "No matches." {
		t.Fatalf("nothing found %q %v", text, err)
	}

	// a folder of several repositories, itself none: the plain grep
	plain := t.TempDir()
	write(t, plain, map[string]string{
		"svc/main.go":                 "prize_history here\n",
		"svc/logs/today.log":          "prize_history in a log\n",
		"svc/node_modules/x/index.js": "prize_history in a dependency\n",
		"svc/.git/config":             "prize_history in git\n",
		"locked/secret.txt":           "prize_history unreadable\n",
	})
	if err := os.Chmod(filepath.Join(plain, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(plain, "locked"), 0o755) })
	text, err = runRemoteGrep(ctx, host, plain, "prize_history", "", "", false)
	if err != nil || strings.TrimSpace(text) != "./svc/main.go:1:prize_history here" {
		t.Fatalf("plain grep %q %v", text, err)
	}
	if text, err := runRemoteGrep(ctx, host, plain, "no-such-text", "", "", false); err != nil || text != "No matches." {
		t.Fatalf("plain grep, nothing found past a folder it may not read %q %v", text, err)
	}
	if _, err := runRemoteGrep(ctx, host, plain, "(unclosed", "", "", false); err == nil {
		t.Fatal("a broken pattern is an error")
	}
}

// Claude Code runs the tools marked readOnlyHint side by side: those that only read, never one
// that changes something on the server.
func TestRemoteReadToolsRunSideBySide(t *testing.T) {
	t.Parallel()
	ro := map[string]bool{}
	for _, x := range remoteToolList {
		a, _ := x["annotations"].(map[string]any)
		ro[x["name"].(string)] = a["readOnlyHint"] == true
	}
	for name, want := range map[string]bool{"read_file": true, "grep": true, "glob": true, "bash_output": true,
		"write_file": false, "edit_file": false, "bash": false, "bash_kill": false} {
		if ro[name] != want {
			t.Errorf("%s readOnlyHint %v, want %v", name, ro[name], want)
		}
	}
	for name := range remoteReadTools {
		if !ro[name] {
			t.Errorf("%s reads only but is not marked so", name)
		}
	}
}
