package gitsvc

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Commit from the panel runs the user's hooks (docs/API-studio.md §3), and nobody
// sits at a terminal to answer them. Whatever a hook does, a commit must end soon,
// say why, and leave the next commit possible.

func writeHook(t *testing.T, f *fixture, name, body string) {
	t.Helper()
	p := filepath.Join(f.dir, ".git", "hooks", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func dropHook(t *testing.T, f *fixture, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(f.dir, ".git", "hooks", name)); err != nil {
		t.Fatal(err)
	}
}

// commitChange changes a file and commits it through the service.
func commitChange(t *testing.T, f *fixture, text string) (*CommitResult, error, time.Duration) {
	t.Helper()
	write(t, f.dir, "README.md", text+"\n")
	start := time.Now()
	res, err := f.svc.Commit(context.Background(), f.repo, CommitRequest{Message: text, All: true})
	return res, err, time.Since(start)
}

func headSubject(t *testing.T, f *fixture) string {
	t.Helper()
	return git(t, f.dir, "log", "-1", "--format=%s")
}

func shortCommitTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := CommitTimeout
	CommitTimeout = d
	t.Cleanup(func() { CommitTimeout = old })
}

func TestHookRefusalSaysWhy(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	writeHook(t, f, "pre-commit", "echo 'lint failed: bad.go line 3' >&2\nexit 1\n")
	_, err, _ := commitChange(t, f, "refused")
	if code(err) != "git_failed" || !strings.Contains(err.Error(), "lint failed: bad.go line 3") {
		t.Fatalf("%v", err)
	}
	if got := headSubject(t, f); got != "init" {
		t.Errorf("committed anyway: %s", got)
	}
}

func TestHookThatPassesCommits(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	writeHook(t, f, "pre-commit", "echo checking\nexit 0\n")
	writeHook(t, f, "commit-msg", "echo 'with hook' >> \"$1\"\n")
	if _, err, _ := commitChange(t, f, "passes"); err != nil {
		t.Fatal(err)
	}
	if body := git(t, f.dir, "log", "-1", "--format=%B"); !strings.Contains(body, "with hook") {
		t.Errorf("commit-msg did not run: %q", body)
	}
}

// A hook asking on stdin gets an end of file, not a wait.
func TestHookReadingStdinIsNotAnswered(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	writeHook(t, f, "pre-commit", "printf 'Sure? '\nread answer || { echo 'no answer' >&2; exit 1; }\n")
	_, err, took := commitChange(t, f, "stdin")
	if code(err) != "git_failed" || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("%v", err)
	}
	if took > 20*time.Second {
		t.Errorf("took %s", took)
	}
}

const askTerminal = `printf 'Proceed? [y/N] ' > /dev/tty || { echo 'needs a terminal' >&2; exit 1; }
read answer < /dev/tty || { echo 'needs a terminal' >&2; exit 1; }
[ "$answer" = y ]
`

func TestHookAskingTheTerminalWithoutOne(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	writeHook(t, f, "pre-commit", askTerminal)
	_, err, took := commitChange(t, f, "no terminal")
	if code(err) != "git_failed" || !strings.Contains(err.Error(), "needs a terminal") {
		t.Fatalf("%v", err)
	}
	if took > 20*time.Second {
		t.Errorf("took %s", took)
	}
	dropHook(t, f, "pre-commit")
	if _, err, _ := commitChange(t, f, "next"); err != nil {
		t.Errorf("the next commit: %v", err)
	}
}

// `nextboard serve` started by hand has the user's terminal. A hook must not get
// it: it would stop on the read, or take what the user types in that terminal.
func TestHookCannotReachTheHubsOwnTerminal(t *testing.T) {
	if os.Getenv("NB_TEST_TTY_CHILD") == "1" {
		t.Skip("this is the child")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookTerminalChild$", "-test.timeout=60s", "-test.v")
	cmd.Env = append(os.Environ(), "NB_TEST_TTY_CHILD=1")
	// Started on a terminal of its own, which becomes its controlling terminal.
	tty, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer tty.Close()
	var out bytes.Buffer
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, _ = io.Copy(&out, tty) // ends with an EIO when the child is gone
	}()
	werr := cmd.Wait()
	select {
	case <-copied:
	case <-time.After(5 * time.Second):
	}
	text := out.String()
	if strings.Contains(text, "--- SKIP") {
		t.Skip("the child has no controlling terminal")
	}
	if werr != nil || !strings.Contains(text, "--- PASS") {
		t.Fatalf("child: %v\n%s", werr, text)
	}
	if strings.Contains(text, "Proceed?") {
		t.Errorf("the hook wrote to the hub's terminal:\n%s", text)
	}
}

func TestHookTerminalChild(t *testing.T) {
	if os.Getenv("NB_TEST_TTY_CHILD") != "1" {
		t.Skip("runs under TestHookCannotReachTheHubsOwnTerminal")
	}
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err != nil {
		t.Skipf("no controlling terminal: %v", err)
	} else {
		f.Close()
	}
	shortCommitTimeout(t, 10*time.Second)
	f := newFixture(t, true)
	f.initRepo()
	writeHook(t, f, "pre-commit", askTerminal)
	_, err, took := commitChange(t, f, "terminal")
	if code(err) != "git_failed" || !strings.Contains(err.Error(), "needs a terminal") {
		t.Fatalf("took %s: %v", took, err)
	}
	if took > 5*time.Second {
		t.Errorf("took %s: it stopped on the terminal", took)
	}
}

func processes(t *testing.T, pattern string) int {
	t.Helper()
	out, _ := exec.Command("pgrep", "-fc", pattern).Output()
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func TestHookThatHangsIsStopped(t *testing.T) {
	shortCommitTimeout(t, 2*time.Second)
	f := newFixture(t, true)
	f.initRepo()
	const naptime = "5427"
	writeHook(t, f, "pre-commit", "sleep "+naptime+" &\nsleep "+naptime+"\n")
	_, err, took := commitChange(t, f, "hangs")
	if code(err) != "timeout" {
		t.Fatalf("%v", err)
	}
	if took > 10*time.Second {
		t.Errorf("took %s", took)
	}
	deadline := time.Now().Add(8 * time.Second)
	for processes(t, "^sleep "+naptime+"$") > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the hook is still running")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := headSubject(t, f); got != "init" {
		t.Errorf("committed anyway: %s", got)
	}
	// No lock left behind: the commit after it works.
	CommitTimeout = 30 * time.Second
	dropHook(t, f, "pre-commit")
	if _, err, _ := commitChange(t, f, "next"); err != nil {
		t.Errorf("the next commit: %v", err)
	}
}

// Hooks that start something and go, like rebuilding a tags file, are common.
func TestHookLeavingAProcessBehindDoesNotHoldTheCommit(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	const naptime = "5428"
	writeHook(t, f, "post-commit", "sleep "+naptime+" &\n")
	t.Cleanup(func() { _ = exec.Command("pkill", "-f", "^sleep "+naptime+"$").Run() })
	res, err, took := commitChange(t, f, "leaves a process")
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit.Subject != "leaves a process" {
		t.Errorf("%+v", res.Commit)
	}
	if took > 10*time.Second {
		t.Errorf("took %s: the commit waited for what the hook left running", took)
	}
}

// Signing that needs a passphrase asks on the terminal too.
func TestSigningThatNeedsATerminal(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	gpg := filepath.Join(t.TempDir(), "fake-gpg")
	if err := os.WriteFile(gpg, []byte("#!/bin/sh\necho 'gpg: signing failed: Inappropriate ioctl for device' >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, f.dir, "config", "commit.gpgsign", "true")
	git(t, f.dir, "config", "gpg.program", gpg)
	_, err, took := commitChange(t, f, "signed")
	if code(err) != "git_failed" || !strings.Contains(err.Error(), "gpg") {
		t.Fatalf("%v", err)
	}
	if took > 20*time.Second {
		t.Errorf("took %s", took)
	}
}
