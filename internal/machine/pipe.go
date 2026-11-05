package machine

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// pipeStopWait is how long a stopped program may take before it is killed.
const pipeStopWait = 3 * time.Second

// pipeStderrMax is the end of stderr kept for error messages.
const pipeStderrMax = 4 << 10

// PipeProc is a long-running program with separate stdin and stdout and no
// PTY, for framed protocols such as the Language Server Protocol.
type PipeProc struct {
	Stdin  io.WriteCloser
	Stdout io.Reader

	stderr   *tailBuffer
	stop     func()
	stopOnce sync.Once
	done     chan struct{}
}

// Stop ends the program: SIGTERM and closed stdin first, a kill after
// pipeStopWait. It is safe to call more than once.
func (p *PipeProc) Stop() {
	p.stopOnce.Do(func() {
		_ = p.Stdin.Close()
		p.stop()
	})
}

// Done is closed when the program has exited.
func (p *PipeProc) Done() <-chan struct{} { return p.done }

// StderrTail returns the end of what the program wrote to stderr.
func (p *PipeProc) StderrTail() string { return p.stderr.String() }

// StartPipe starts spec (Cwd, Argv or Script, Env; the timeouts and Stdin are
// ignored) on h: locally with the login PATH, on SSH over a non-PTY channel in
// the login shell. The caller must call Stop.
func StartPipe(h Host, spec ExecSpec) (*PipeProc, error) {
	if (len(spec.Argv) == 0) == (spec.Script == "") {
		return nil, errExecSpec
	}
	switch m := h.(type) {
	case *Local:
		return m.startPipe(spec)
	case *SSH:
		return m.startPipe(spec)
	}
	return nil, errors.New("machine tidak didukung")
}

func (l *Local) startPipe(spec ExecSpec) (*PipeProc, error) {
	cmd := l.command(spec)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// io.Pipe rather than StdoutPipe: Wait must not close the reader while the
	// bridge is still draining the last frames.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	tail := &tailBuffer{max: pipeStderrMax}
	cmd.Stderr = tail
	cmd.WaitDelay = pipeStopWait
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &PipeProc{Stdin: stdin, Stdout: pr, stderr: tail, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		_ = pw.Close()
		close(p.done)
	}()
	p.stop = func() {
		pg := -cmd.Process.Pid
		_ = syscall.Kill(pg, syscall.SIGTERM)
		go func() {
			select {
			case <-p.done:
			case <-time.After(pipeStopWait):
				_ = syscall.Kill(pg, syscall.SIGKILL)
			}
			_ = pr.Close()
		}()
	}
	return p, nil
}

func (m *SSH) startPipe(spec ExecSpec) (*PipeProc, error) {
	c := m.currentClient()
	if c == nil {
		return nil, ErrNotConnected
	}
	s, err := c.NewSession()
	if err != nil {
		return nil, sessionErr(err)
	}
	stdin, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	stdout, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	tail := &tailBuffer{max: pipeStderrMax}
	s.Stderr = tail
	if err := s.Start(loginWrap(execScript(spec))); err != nil {
		s.Close()
		return nil, err
	}
	p := &PipeProc{Stdin: stdin, Stdout: stdout, stderr: tail, done: make(chan struct{})}
	go func() {
		_ = s.Wait()
		_ = s.Close()
		close(p.done)
	}()
	p.stop = func() {
		// Servers without signal support still exit on the closed stdin.
		_ = s.Signal(ssh.SIGTERM)
		go func() {
			select {
			case <-p.done:
			case <-time.After(pipeStopWait):
				_ = s.Close()
			}
		}()
	}
	return p, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b.Write(p)
	if over := t.b.Len() - t.max; over > 0 {
		t.b.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.b.String()
}
