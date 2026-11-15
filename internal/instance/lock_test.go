package instance

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const lockHelperEnv = "NB_INSTANCE_TEST_LOCK_HELPER"

// TestMain doubles as a second process that holds a data folder's lock until its stdin closes
// (or 30 s pass).
func TestMain(m *testing.M) {
	if dir := os.Getenv(lockHelperEnv); dir != "" {
		lk, err := AcquireLock(dir)
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		fmt.Println("locked")
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
		}
		lk.Release()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLockInOneProcess(t *testing.T) {
	defer func(d time.Duration) { lockWait = d }(lockWait)
	lockWait = 100 * time.Millisecond
	dir := t.TempDir()
	if run, _ := Running(dir); run {
		t.Fatal("running before any lock")
	}
	if _, err := os.Stat(filepath.Join(dir, LockFile)); err == nil {
		t.Fatal("Running made the lock file")
	}
	lk, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, LockFile)); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file %v", fi)
	}
	if run, pid := Running(dir); !run || pid != os.Getpid() {
		t.Fatalf("Running %v %d", run, pid)
	}
	_, err = AcquireLock(dir)
	var le *LockedError
	if !errors.Is(err, ErrLocked) || !errors.As(err, &le) || le.PID != os.Getpid() || le.DataDir != dir {
		t.Fatalf("second lock: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "already running on the data folder "+dir) || !strings.Contains(msg, fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("message: %s", msg)
	}
	lk.Release()
	lk.Release() // twice is fine
	var none *Lock
	none.Release()
	if run, _ := Running(dir); run {
		t.Fatal("running after Release")
	}
	lk2, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("after Release: %v", err)
	}
	lk2.Release()
}

// A Running check holds the lock for a moment; a hub starting just then still starts.
func TestLockWaitsOutARunningCheck(t *testing.T) {
	dir := t.TempDir()
	lk, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	lk.Release()
	f, err := os.Open(filepath.Join(dir, LockFile))
	if err != nil {
		t.Fatal(err)
	}
	// what Running does, held longer
	if err := flockShared(f); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(200 * time.Millisecond); f.Close() }()
	lk, err = AcquireLock(dir)
	if err != nil {
		t.Fatalf("a check stopped the hub: %v", err)
	}
	lk.Release()
}

// Another process: its lock is seen, refused, and gone when it is killed.
func TestLockAcrossProcesses(t *testing.T) {
	defer func(d time.Duration) { lockWait = d }(lockWait)
	lockWait = 100 * time.Millisecond
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+dir)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if line != "locked\n" {
		t.Fatalf("helper said %q", line)
	}
	if run, pid := Running(dir); !run || pid != cmd.Process.Pid {
		t.Fatalf("Running %v %d, helper is %d", run, pid, cmd.Process.Pid)
	}
	_, err := AcquireLock(dir)
	var le *LockedError
	if !errors.As(err, &le) || le.PID != cmd.Process.Pid {
		t.Fatalf("second hub: %v", err)
	}
	// however the holder ends, the lock ends with it
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if run, _ := Running(dir); run {
		t.Fatal("running after the holder was killed")
	}
	lk, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("after the holder died: %v", err)
	}
	lk.Release()
}

func flockShared(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
}
