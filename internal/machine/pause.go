package machine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Pause / resume of an agent console (docs/API-orchestration.md §8): the
// foreground process group of the console's terminal is stopped with
// SIGSTOP and continued with SIGCONT. Signals never go to a process group
// outside the terminal session of the console.

var (
	// ErrPidUnknown: the wrapper pid of a remote console is not known (no
	// agent-start marker yet and no tmux pane to ask).
	ErrPidUnknown = errors.New("console process pid not known yet")
	// ErrSignalRefused: the foreground group failed validation (pgid ≤ 1,
	// the hub's own group, or another terminal session).
	ErrSignalRefused = errors.New("signal refused: the foreground process group is not valid for this console")
)

// ForegroundSignaler is implemented by procs that can stop/continue the
// foreground process group of their terminal. sig is "STOP" or "CONT";
// wrapperPid is the pid from the agent-start marker (remote only).
type ForegroundSignaler interface {
	SignalForeground(ctx context.Context, wrapperPid int, sig string) error
}

func signalOf(sig string) (syscall.Signal, error) {
	switch sig {
	case "STOP":
		return syscall.SIGSTOP, nil
	case "CONT":
		return syscall.SIGCONT, nil
	}
	return 0, fmt.Errorf("unsupported signal: %q", sig)
}

// SignalForeground (local): TIOCGPGRP on the PTY master gives the
// foreground group; it must be > 1, not the hub's own group, and in the
// session the PTY child leads.
func (p *localProc) SignalForeground(_ context.Context, _ int, sig string) error {
	s, err := signalOf(sig)
	if err != nil {
		return err
	}
	var pgid int
	if err := p.withFd(func() (err error) {
		pgid, err = foregroundPgrp(p.pty)
		return err
	}); err != nil {
		return err
	}
	if pgid <= 1 || pgid == syscall.Getpgrp() || !sameSession(p.Pid(), pgid) {
		return ErrSignalRefused
	}
	if err := syscall.Kill(-pgid, s); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// remoteSignalScript is the read-only check + signal of §8: the terminal's
// foreground group of the wrapper (tpgid) must be > 1 and belong to the
// wrapper's session. It prints __NB_SIG_OK__ only when the signal was sent.
// `kill -s SIG -- -pgid` instead of `kill -SIG -- -pgid`: dash rejects the
// latter ("Illegal number").
func remoteSignalScript(pid int, sig string) string {
	p := strconv.Itoa(pid)
	return `p=` + p + `; t=$(ps -o tpgid= -p "$p" 2>/dev/null | tr -d ' '); ` +
		`a=$(ps -o sid= -p "$p" 2>/dev/null | tr -d ' '); ` +
		`[ -n "$t" ] && [ -n "$a" ] || { echo __NB_SIG_PS__; exit 3; }; ` +
		`b=$(ps -o sid= -p "$t" 2>/dev/null | tr -d ' '); ` +
		`[ "$t" -gt 1 ] 2>/dev/null && [ "$a" = "$b" ] || { echo __NB_SIG_REFUSED__; exit 4; }; ` +
		`kill -s ` + sig + ` -- "-$t" && echo __NB_SIG_OK__`
}

// SignalForeground (SSH) runs remoteSignalScript over exec (3 s timeout).
func (m *SSH) SignalForeground(ctx context.Context, pid int, sig string) error {
	if _, err := signalOf(sig); err != nil {
		return err
	}
	if pid <= 1 {
		return ErrPidUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := m.Exec(ctx, "/bin/sh -c "+Quote(remoteSignalScript(pid, sig)))
	switch {
	case strings.Contains(out, "__NB_SIG_OK__"):
		return nil
	case strings.Contains(out, "__NB_SIG_PS__"):
		return ErrPidUnknown // ps without tpgid/sid (BusyBox) or the pid is gone
	case strings.Contains(out, "__NB_SIG_REFUSED__"):
		return ErrSignalRefused
	case err != nil:
		return err
	}
	return fmt.Errorf("signal failed: %s", strings.TrimSpace(out))
}

// TmuxPanePid asks tmux for the pid of the console's pane (read-only),
// used when no agent-start marker reported the wrapper pid.
func (m *SSH) TmuxPanePid(ctx context.Context, name string) (int, error) {
	if !tmuxNameRe.MatchString(name) {
		return 0, fmt.Errorf("invalid tmux session name: %q", name)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := m.Exec(ctx, loginWrap("tmux display -p -t "+Quote("="+name)+" '#{pane_pid}' 2>/dev/null"))
	if err != nil && strings.TrimSpace(out) == "" {
		return 0, err
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(out))
	if perr != nil || pid <= 1 {
		return 0, ErrPidUnknown
	}
	return pid, nil
}

// SignalForeground (SSH proc): the wrapper pid from the marker, or the
// tmux pane pid when the marker never came (re-attached sessions).
func (p *sshProc) SignalForeground(ctx context.Context, wrapperPid int, sig string) error {
	if wrapperPid <= 1 && p.tmux != "" {
		pid, err := p.m.TmuxPanePid(ctx, p.tmux)
		if err != nil {
			return err
		}
		wrapperPid = pid
	}
	return p.m.SignalForeground(ctx, wrapperPid, sig)
}
