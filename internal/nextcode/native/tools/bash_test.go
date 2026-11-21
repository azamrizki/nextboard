package tools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func bashEnv(t *testing.T) *Env {
	return &Env{Cwd: t.TempDir(), Environ: os.Environ()}
}

// running counts the live processes whose command line is exactly argv.
func running(argv ...string) int {
	want := []byte(strings.Join(argv, "\x00") + "\x00")
	dirs, _ := os.ReadDir("/proc")
	n := 0
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join("/proc", d.Name(), "cmdline"))
		if err != nil || !bytes.Equal(b, want) {
			continue
		}
		stat, _ := os.ReadFile(filepath.Join("/proc", d.Name(), "stat"))
		if i := bytes.LastIndexByte(stat, ')'); i > 0 && i+2 < len(stat) && stat[i+2] == 'Z' {
			continue // a zombie: dead, not yet reaped
		}
		n++
	}
	return n
}

// gone waits a little for every process with argv to end.
func gone(argv ...string) bool {
	for range 100 {
		if running(argv...) == 0 {
			return true
		}
		time.Sleep(30 * time.Millisecond)
	}
	return false
}

func TestBashOutput(t *testing.T) {
	env := bashEnv(t)
	cases := []struct {
		name    string
		command string
		isError bool
		want    string
	}{
		{"stdout", "echo hello", false, "hello"},
		{"in order", "echo a; echo b >&2; echo c", false, "a\nb\nc"},
		{"no output", "true", false, "(no output)"},
		{"exit code", "echo out; echo err >&2; exit 3", true, "out\nerr\nExit code 3"},
		{"exit code, no output", "exit 1", true, "(no output)\nExit code 1"},
		{"signal", "kill -SEGV $$", true, "(no output)\nEnded by a signal (segmentation fault)"},
		{"cwd", "pwd", false, env.Cwd},
		{"stdin is empty", "cat; echo done", false, "done"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := call(t, "Bash", env, map[string]any{"command": c.command, "description": c.name})
			if r.IsError != c.isError || r.Output != c.want {
				t.Errorf("got %+v, want %q (error %v)", r, c.want, c.isError)
			}
		})
	}
	if r := call(t, "Bash", env, map[string]any{"command": "  "}); !r.IsError {
		t.Errorf("blank command: %+v", r)
	}
	if r := call(t, "Bash", &Env{Cwd: filepath.Join(env.Cwd, "missing")}, map[string]any{"command": "true"}); !r.IsError || !strings.Contains(r.Output, "Could not run") {
		t.Errorf("missing cwd: %+v", r)
	}
}

func TestBashEnvironVerbatim(t *testing.T) {
	t.Setenv("NB_TOOLS_TEST_LEAK", "leaked")
	env := &Env{Cwd: t.TempDir(), Environ: []string{"PATH=/usr/bin:/bin", "MY_VAR=mine", "PAGER=less"}}
	r := call(t, "Bash", env, map[string]any{"command": "env"})
	if r.IsError {
		t.Fatal(r.Output)
	}
	lines := strings.Split(r.Output, "\n")
	for _, want := range []string{"MY_VAR=mine", "PAGER=less", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "CI=1"} {
		if !slices.Contains(lines, want) {
			t.Errorf("env lacks %s:\n%s", want, r.Output)
		}
	}
	if strings.Contains(r.Output, "NB_TOOLS_TEST_LEAK") || slices.Contains(lines, "PAGER=cat") {
		t.Errorf("env has the agent's own variables or a second PAGER:\n%s", r.Output)
	}
	if got := commandEnv([]string{"CI=0", "A=1"}); !slices.Equal(got, []string{"CI=0", "A=1", "PAGER=cat", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0"}) {
		t.Errorf("commandEnv = %v", got)
	}
	if got := commandEnv(nil); got == nil || len(got) != 4 {
		t.Errorf("commandEnv(nil) = %#v", got)
	}
}

func TestBashClipsOutput(t *testing.T) {
	r := call(t, "Bash", bashEnv(t), map[string]any{"command": "printf 'h%.0s' $(seq 15000); printf 'm%.0s' $(seq 10000); printf 't%.0s' $(seq 15000)"})
	if r.IsError {
		t.Fatal(r.Output[:200])
	}
	want := strings.Repeat("h", 15000) + "\n… 10000 characters cut …\n" + strings.Repeat("t", 15000)
	if r.Output != want {
		t.Errorf("output is %d bytes, starts %q, has m: %v", len(r.Output), r.Output[:20], strings.Contains(r.Output, "m"))
	}
}

func TestBashTimeout(t *testing.T) {
	start := time.Now()
	r := call(t, "Bash", bashEnv(t), map[string]any{"command": "echo started; sleep 30.101", "timeout": 300})
	if !r.IsError || r.Output != "started\nTimed out after 0.3s" {
		t.Errorf("got %+v", r)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("took %v", d)
	}
	if !gone("sleep", "30.101") {
		t.Error("the sleep still runs")
	}
}

func TestBashStopKillsTheGroup(t *testing.T) {
	cases := []struct{ name, command, marker string }{
		{"background and wait", "sleep 30.201 & wait", "30.201"},
		{"a child shell", "bash -c 'sleep 30.202; true' & bash -c 'sleep 30.203; true'", "30.203"},
		{"ignores SIGTERM", "trap '' TERM; sleep 30.204; true", "30.204"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(300*time.Millisecond, cancel)
			start := time.Now()
			r := callCtx(ctx, t, "Bash", bashEnv(t), map[string]any{"command": c.command})
			if !r.IsError || r.Output != "(no output)\nStopped" {
				t.Errorf("got %+v", r)
			}
			if d := time.Since(start); d > 6*time.Second {
				t.Errorf("took %v", d)
			}
			if !gone("sleep", c.marker) {
				t.Errorf("sleep %s still runs", c.marker)
			}
		})
	}
	if !gone("sleep", "30.202") {
		t.Error("the first child shell's sleep still runs")
	}
}

func TestBashEndsBackgroundJobs(t *testing.T) {
	start := time.Now()
	r := call(t, "Bash", bashEnv(t), map[string]any{"command": "sleep 30.301 & echo left"})
	if r.IsError || r.Output != "left" {
		t.Errorf("got %+v", r)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("took %v", d)
	}
	if !gone("sleep", "30.301") {
		t.Error("the background sleep still runs")
	}
}

func TestBashFallsBackToSh(t *testing.T) {
	old := shells
	shells = []string{"/nonexistent/bash", "/bin/sh"}
	t.Cleanup(func() { shells = old })
	if r := call(t, "Bash", bashEnv(t), map[string]any{"command": "echo via sh"}); r.IsError || r.Output != "via sh" {
		t.Errorf("got %+v", r)
	}
}

func TestBashSandboxWithoutBwrap(t *testing.T) {
	old := lookBwrap
	lookBwrap = func() (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { lookBwrap = old })
	env := bashEnv(t)
	env.Sandbox = &Sandbox{}
	marker := filepath.Join(env.Cwd, "ran")
	r := call(t, "Bash", env, map[string]any{"command": "touch " + marker})
	if !r.IsError || r.Output != noBwrap {
		t.Errorf("got %+v", r)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran")
	}
}

func TestSandboxArgs(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	cwd, add, extra := filepath.Join(base, "cwd"), filepath.Join(base, "add"), filepath.Join(base, "extra")
	for _, d := range []string{cwd, add, extra} {
		os.Mkdir(d, 0o755)
	}
	link := filepath.Join(base, "link")
	os.Symlink(extra, link)
	env := &Env{Cwd: cwd, AddDirs: []string{add, filepath.Join(base, "missing")}, Sandbox: &Sandbox{Write: []string{link, cwd}}}
	got := strings.Join(sandboxArgs("bwrap", env), " ")
	want := "bwrap --die-with-parent --new-session --unshare-pid --ro-bind / / --dev /dev --proc /proc" +
		" --bind " + cwd + " " + cwd + " --bind " + add + " " + add + " --bind " + extra + " " + extra
	if !strings.HasPrefix(got, want) || strings.Contains(got, "missing") || strings.Count(got, "--bind "+cwd) != 1 {
		t.Errorf("args:\n%s\nwant prefix:\n%s", got, want)
	}
	if tmp, _ := filepath.EvalSymlinks(os.TempDir()); !strings.Contains(got, "--bind "+tmp+" "+tmp) {
		t.Errorf("no temp folder: %s", got)
	}
	if !strings.HasSuffix(got, "--unshare-net --chdir "+cwd+" --") {
		t.Errorf("end: %s", got)
	}
	env.Sandbox.Network = true
	if got := strings.Join(sandboxArgs("bwrap", env), " "); strings.Contains(got, "--unshare-net") {
		t.Errorf("network allowed, yet: %s", got)
	}
}

func TestBashSandbox(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap is not installed")
	}
	base := t.TempDir()
	cwd, tmp, outside := filepath.Join(base, "cwd"), filepath.Join(base, "tmp"), filepath.Join(base, "outside")
	for _, d := range []string{cwd, tmp, outside} {
		os.Mkdir(d, 0o755)
	}
	t.Setenv("TMPDIR", tmp) // os.TempDir(), which the sandbox lets commands write in
	env := &Env{Cwd: cwd, Environ: os.Environ(), Sandbox: &Sandbox{}}

	r := call(t, "Bash", env, map[string]any{"command": "touch inside " + filepath.Join(tmp, "t") + " && pwd"})
	if r.IsError || r.Output != cwd {
		t.Fatalf("writing inside: %+v", r)
	}
	r = call(t, "Bash", env, map[string]any{"command": "touch " + filepath.Join(outside, "x")})
	if !r.IsError || !strings.Contains(r.Output, "Read-only file system") {
		t.Errorf("writing outside: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); err == nil {
		t.Error("the file outside was written")
	}
	env.Sandbox.Write = []string{outside}
	if r := call(t, "Bash", env, map[string]any{"command": "touch " + filepath.Join(outside, "y")}); r.IsError {
		t.Errorf("writing in Sandbox.Write: %+v", r)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	r = callCtx(ctx, t, "Bash", env, map[string]any{"command": "sleep 30.401 & wait"})
	if !r.IsError || !strings.HasSuffix(r.Output, "Stopped") {
		t.Errorf("stop: %+v", r)
	}
	if !gone("sleep", "30.401") {
		t.Error("the sandboxed sleep still runs")
	}
}

func TestClip(t *testing.T) {
	c := &clip{headN: 3, tailN: 3}
	for _, s := range []string{"ab", "cé", "fgh", "ij", "€k"} { // 11 characters, chunked across a cut
		c.Write([]byte(s))
	}
	if got := c.String(); got != "abc\n… 5 characters cut …\nj€k" {
		t.Errorf("clip = %q", got)
	}
	c = &clip{headN: 3, tailN: 3}
	c.Write([]byte("abcdéf"))
	if got := c.String(); got != "abcdéf" {
		t.Errorf("short clip = %q", got)
	}
	big := &clip{headN: 10, tailN: 10}
	for range 1000 {
		big.Write([]byte(strings.Repeat("x", 1000)))
	}
	if got := big.String(); !strings.Contains(got, "… 999980 characters cut …") || len(big.tail) > 2*10*4 {
		t.Errorf("big clip = %q (tail %d bytes)", got, len(big.tail))
	}
}
