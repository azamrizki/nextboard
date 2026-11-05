package machine

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Foregrounder is implemented by procs that can tell which process group
// owns the terminal (docs/API-agents.md §6.5). wrapperPid is the PID the
// wrapper reported in its agent-start marker; local procs ignore it and ask
// the PTY master directly.
type Foregrounder interface {
	Foreground(ctx context.Context, wrapperPid int) (pgid int, comm string, err error)
}

// foregroundPgrp returns the foreground process group of a PTY master.
func foregroundPgrp(f *os.File) (int, error) {
	return unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPGRP)
}

// processComm returns the command name of pid ("" when unknown).
func processComm(pid int) string {
	if pid <= 0 {
		return ""
	}
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm"); err == nil {
			return strings.TrimSpace(string(b))
		}
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return baseComm(strings.TrimSpace(string(out)))
}

// baseComm strips a path and a leading "-" (login shells) from a ps name.
func baseComm(s string) string {
	s = strings.TrimPrefix(s, "-")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// sameSession reports whether pid exists and shares the session of leader.
func sameSession(leader, pid int) bool {
	if leader <= 0 || pid <= 0 {
		return false
	}
	if pid == leader {
		return true
	}
	a, err := unix.Getsid(leader)
	if err != nil {
		return false
	}
	b, err := unix.Getsid(pid)
	return err == nil && a == b
}

// remoteForegroundScript asks ps for the terminal's foreground group of the
// wrapper and the name of that group's leader (read-only).
func remoteForegroundScript(pid int) string {
	p := strconv.Itoa(pid)
	return `t=$(ps -o tpgid= -p ` + p + ` 2>/dev/null | tr -d ' '); [ -n "$t" ] || exit 3; ` +
		`echo "__NB_FG__ $t $(ps -o comm= -p "$t" 2>/dev/null)"`
}

// parseRemoteForeground parses remoteForegroundScript output.
func parseRemoteForeground(out string) (int, string, bool) {
	i := strings.Index(out, "__NB_FG__ ")
	if i < 0 {
		return 0, "", false
	}
	f := strings.Fields(out[i+len("__NB_FG__ "):])
	if len(f) == 0 {
		return 0, "", false
	}
	pgid, err := strconv.Atoi(f[0])
	if err != nil {
		return 0, "", false
	}
	comm := ""
	if len(f) > 1 {
		comm = baseComm(f[1])
	}
	return pgid, comm, true
}
