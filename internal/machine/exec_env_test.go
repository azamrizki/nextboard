package machine

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

// A run's variables reach the program, in the place of an export, and never the command line
// of the shell that starts it, where ps would show them (an API provider's key).
func TestLocalRunEnvOffTheCommandLine(t *testing.T) {
	const secret = "sk-test-NOT-ON-ARGV-123"
	l := NewLocal(Config{ID: LocalID})
	spec := ExecSpec{Argv: []string{"/bin/sh", "-c", `printf '%s|%s|%s' "$NB_PROVIDER_KEY" "$TERM" "${BAD-unset}"`}, Cwd: t.TempDir(), MaxOutput: 1 << 16,
		Env: []string{"NB_PROVIDER_KEY=" + secret, "TERM=xterm-test", "BAD NAME=x", "noequals"}}
	cmd := l.command(spec)
	if strings.Contains(strings.Join(cmd.Args, " "), secret) {
		t.Fatalf("the key is on the command line: %q", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "NB_PROVIDER_KEY="+secret) || slices.Contains(cmd.Env, "BAD NAME=x") {
		t.Fatalf("env: %q", cmd.Env)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := l.exec(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	// the run's TERM wins over the default one, as the export did
	if got, want := strings.TrimSpace(string(res.Stdout)), secret+"|xterm-test|unset"; got != want {
		t.Fatalf("the program saw %q, want %q", got, want)
	}

	// a pipe run (NextCode's CLIs) the same
	p, err := StartPipe(l, spec)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Stdin.Close()
	out, _ := io.ReadAll(p.Stdout)
	<-p.Done()
	if !strings.HasPrefix(string(out), secret+"|") {
		t.Fatalf("pipe run saw %q", out)
	}
}
