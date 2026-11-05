package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// DefaultExecMaxOutput caps stdout/stderr of Exec (docs/API-studio.md §1.1).
const DefaultExecMaxOutput = 4 << 20

// ExecSpec is one non-interactive command for internal users (runner, git,
// usage). There is deliberately no public exec endpoint: every caller
// builds Argv/Script from a fixed template.
type ExecSpec struct {
	Cwd string
	// Argv runs a program directly; Script runs a POSIX script instead.
	// Exactly one of them is set.
	Argv   []string
	Script string
	Env    []string // KEY=VALUE
	Stdin  []byte   // nil = /dev/null
	// Timeout bounds the whole run; IdleTimeout kills a run that printed
	// nothing on stdout/stderr for that long. Zero = no limit.
	Timeout     time.Duration
	IdleTimeout time.Duration
	MaxOutput   int64 // default DefaultExecMaxOutput
	// OnStdout, when set, receives stdout chunks as they arrive (from one
	// goroutine), e.g. stream-json lines of a headless CLI.
	OnStdout func([]byte)
}

// ExecResult is the outcome of Exec. ExitCode is -1 when the process was
// killed (timeout, idle, cancel) or the transport failed.
type ExecResult struct {
	ExitCode   int
	Stdout     []byte
	Stderr     []byte
	TimedOut   bool
	IdleKilled bool
	Cancelled  bool
	Truncated  bool
}

// execPipeWait is how long output may still come after a local program ended.
const execPipeWait = 2 * time.Second

// errExecSpec: neither or both of Argv and Script.
var errExecSpec = errors.New("exec: isi tepat satu dari Argv atau Script")

// Exec runs spec on h: locally through os/exec with the login PATH, on SSH
// over a non-PTY channel in the login shell. A process that outlives its
// timeout, idles too long or whose ctx is cancelled is stopped (local:
// SIGTERM to its process group, SIGKILL 3 s later; SSH: signal TERM, then
// the channel is closed).
func Exec(ctx context.Context, h Host, spec ExecSpec) (*ExecResult, error) {
	if (len(spec.Argv) == 0) == (spec.Script == "") {
		return nil, errExecSpec
	}
	if spec.MaxOutput <= 0 {
		spec.MaxOutput = DefaultExecMaxOutput
	}
	switch m := h.(type) {
	case *Local:
		return m.exec(ctx, spec)
	case *SSH:
		return m.execSpec(ctx, spec)
	}
	return nil, errors.New("machine tidak didukung")
}

// execScript turns spec into one POSIX script: cd, exports, then the
// program (exec'd so signals reach it directly).
func execScript(spec ExecSpec) string {
	var b strings.Builder
	b.WriteString(cdExpr(spec.Cwd) + " || exit 126")
	if w := envWords(spec.Env); w != "" {
		b.WriteString("; export" + w)
	}
	b.WriteString("; ")
	if spec.Script != "" {
		b.WriteString(spec.Script)
	} else {
		b.WriteString("exec " + Join(spec.Argv...))
	}
	return b.String()
}

// watch tracks output activity and decides when a run must stop.
type execWatch struct {
	mu       sync.Mutex
	last     time.Time
	stdout   limitBuffer
	stderr   limitBuffer
	onStdout func([]byte)
}

func newExecWatch(spec ExecSpec) *execWatch {
	return &execWatch{last: time.Now(), stdout: limitBuffer{max: spec.MaxOutput}, stderr: limitBuffer{max: spec.MaxOutput}, onStdout: spec.OnStdout}
}

func (w *execWatch) touch() {
	w.mu.Lock()
	w.last = time.Now()
	w.mu.Unlock()
}

func (w *execWatch) idleFor() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return time.Since(w.last)
}

type watchWriter struct {
	w      *execWatch
	buf    *limitBuffer
	stream bool
}

func (ww watchWriter) Write(p []byte) (int, error) {
	ww.w.touch()
	ww.buf.Write(p)
	if ww.stream && ww.w.onStdout != nil {
		ww.w.onStdout(append([]byte(nil), p...))
	}
	return len(p), nil
}

// supervise waits for done while enforcing ctx, Timeout and IdleTimeout.
// stop is called once with the reason; it must make done fire eventually.
func supervise(ctx context.Context, spec ExecSpec, w *execWatch, done <-chan struct{}, stop func()) (timedOut, idle, cancelled bool) {
	var deadline <-chan time.Time
	if spec.Timeout > 0 {
		t := time.NewTimer(spec.Timeout)
		defer t.Stop()
		deadline = t.C
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-deadline:
			timedOut = true
		case <-ctx.Done():
			cancelled = true
		case <-tick.C:
			if spec.IdleTimeout <= 0 || w.idleFor() < spec.IdleTimeout {
				continue
			}
			idle = true
		}
		stop()
		<-done
		return
	}
}

func (r *ExecResult) fill(w *execWatch, timedOut, idle, cancelled bool) {
	r.Stdout, r.Stderr = w.stdout.Bytes(), w.stderr.Bytes()
	r.Truncated = w.stdout.dropped() || w.stderr.dropped()
	r.TimedOut, r.IdleKilled, r.Cancelled = timedOut, idle, cancelled
	if timedOut || idle || cancelled {
		r.ExitCode = -1
	}
}

// command builds the process of spec: its own process group, the login PATH.
func (l *Local) command(spec ExecSpec) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", execScript(spec))
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		if !strippedEnv(kv) {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "PATH="+l.pathEnv(), "HOME="+l.home, "TERM=dumb")
	cmd.Dir = l.home
	// A session of its own, so no terminal: a hub started by hand has the user's, and
	// a git hook asking on /dev/tty would stop there, or take what the user types.
	// It leads its own process group as well, which stopping it relies on.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// What a program leaves running can hold its output open for hours, like a git
	// hook starting something in the background. The run is over when the program is.
	cmd.WaitDelay = execPipeWait
	return cmd
}

func (l *Local) exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	cmd := l.command(spec)
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	w := newExecWatch(spec)
	cmd.Stdout = watchWriter{w, &w.stdout, true}
	cmd.Stderr = watchWriter{w, &w.stderr, false}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	var werr error
	go func() { werr = cmd.Wait(); close(done) }()
	timedOut, idle, cancelled := supervise(ctx, spec, w, done, func() {
		pg := -cmd.Process.Pid
		_ = syscall.Kill(pg, syscall.SIGTERM)
		go func() {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = syscall.Kill(pg, syscall.SIGKILL)
			}
		}()
	})
	res := &ExecResult{}
	var ee *exec.ExitError
	switch {
	case werr == nil, errors.Is(werr, exec.ErrWaitDelay):
	case errors.As(werr, &ee):
		res.ExitCode = ee.ExitCode()
	default:
		return nil, werr
	}
	res.fill(w, timedOut, idle, cancelled)
	return res, nil
}

// sessionErr: opening a channel over a dying link fails with whatever transport
// error is at hand. Only a refusal comes from the server itself.
func sessionErr(err error) error {
	var refused *ssh.OpenChannelError
	if errors.As(err, &refused) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrDisconnected, err)
}

func (m *SSH) execSpec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	c := m.currentClient()
	if c == nil {
		return nil, ErrNotConnected
	}
	s, err := c.NewSession()
	if err != nil {
		return nil, sessionErr(err)
	}
	defer s.Close()
	w := newExecWatch(spec)
	s.Stdout = watchWriter{w, &w.stdout, true}
	s.Stderr = watchWriter{w, &w.stderr, false}
	if spec.Stdin != nil {
		s.Stdin = bytes.NewReader(spec.Stdin)
	} else {
		s.Stdin = bytes.NewReader(nil)
	}
	if err := s.Start(loginWrap(execScript(spec))); err != nil {
		return nil, sessionErr(err)
	}
	done := make(chan struct{})
	var werr error
	go func() { werr = s.Wait(); close(done) }()
	timedOut, idle, cancelled := supervise(ctx, spec, w, done, func() {
		_ = s.Signal(ssh.SIGTERM)
		go func() {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = s.Close()
			}
		}()
	})
	res := &ExecResult{}
	var ee *ssh.ExitError
	var miss *ssh.ExitMissingError
	switch {
	case werr == nil:
	case errors.As(werr, &ee):
		res.ExitCode = ee.ExitStatus()
	case errors.As(werr, &miss), errors.Is(werr, io.EOF):
		res.ExitCode = -1
		if !timedOut && !idle && !cancelled {
			res.fill(w, false, false, false)
			return res, fmt.Errorf("%w: %v", ErrDisconnected, werr)
		}
	default:
		return nil, werr
	}
	res.fill(w, timedOut, idle, cancelled)
	return res, nil
}
