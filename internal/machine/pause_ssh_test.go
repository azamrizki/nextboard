package machine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestSSHSignalForeground pauses and resumes a process running in a real
// SSH PTY (throwaway sshd on loopback) with the ps-validated script, and
// shows the script refuses a pid outside any terminal session.
func TestSSHSignalForeground(t *testing.T) {
	if os.Getenv("NEXTBOARD_SSHD_TEST") == "" {
		t.Skip("set NEXTBOARD_SSHD_TEST=1 untuk menjalankan uji sshd")
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("butuh /proc")
	}
	reg := newTestRegistry(t)
	m := connectTestSSHD(t, reg, startSSHD(t, "Subsystem sftp internal-sftp"))
	p, err := m.Start(StartSpec{SessionID: "s_rpause", Command: "/bin/sh",
		Args: []string{"-c", `echo "PID=$$"; while :; do echo tick; sleep 0.05; done`}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { p.Signal("KILL"); p.Close() }()
	out := &lockedOut{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			out.add(buf[:n])
			if err != nil {
				return
			}
		}
	}()
	pidRe := regexp.MustCompile(`PID=(\d+)`)
	var pid int
	for deadline := time.Now().Add(10 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		out.mu.Lock()
		if mm := pidRe.FindSubmatch(out.b.Bytes()); mm != nil {
			pid, _ = strconv.Atoi(string(mm[1]))
		}
		out.mu.Unlock()
	}
	if pid == 0 {
		t.Fatalf("no PID line: %q", out.b.String())
	}
	ctx := context.Background()
	fs := p.(ForegroundSignaler)
	if err := fs.SignalForeground(ctx, pid, "STOP"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); procState(pid) != "T" && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if procState(pid) != "T" {
		t.Fatalf("remote STOP: state %q", procState(pid))
	}
	time.Sleep(150 * time.Millisecond)
	n := out.len()
	time.Sleep(400 * time.Millisecond)
	if out.len() != n {
		t.Fatal("output continued while stopped")
	}
	if err := fs.SignalForeground(ctx, pid, "CONT"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); out.len() == n && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if out.len() == n {
		t.Fatal("remote CONT: no output")
	}
	// A process without a terminal (own session, tpgid -1) is refused.
	other := exec.Command("sleep", "30")
	other.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { other.Process.Kill(); other.Wait() }()
	if err := m.SignalForeground(ctx, other.Process.Pid, "STOP"); !errors.Is(err, ErrSignalRefused) && !errors.Is(err, ErrPidUnknown) {
		t.Fatalf("foreign pid: %v", err)
	}
	if procState(other.Process.Pid) == "T" {
		t.Fatal("foreign process was stopped")
	}
	if err := m.SignalForeground(ctx, 0, "STOP"); !errors.Is(err, ErrPidUnknown) {
		t.Fatalf("pid 0: %v", err)
	}
}
