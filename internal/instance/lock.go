package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// LockFile is held (flock) by the hub that runs on a data folder, for as long as it runs. It
// holds that hub's pid, which is only for messages: the lock is what counts.
const LockFile = "hub.lock"

// ErrLocked: another hub runs on the data folder (errors.Is a *LockedError).
var ErrLocked = errors.New("data folder in use by another hub")

// ErrLockUnsupported: the data folder's file system cannot lock files.
var ErrLockUnsupported = errors.New("cannot lock the data folder")

// LockedError says which hub holds the data folder.
type LockedError struct {
	DataDir string
	PID     int // 0 when the holder wrote none
}

func (e *LockedError) Error() string {
	who := "another NextBoard hub"
	if e.PID > 0 {
		who += " (pid " + strconv.Itoa(e.PID) + ")"
	}
	return fmt.Sprintf("%s is already running on the data folder %s: stop it first (`nextboard status` shows the service), or start this hub on another folder", who, e.DataDir)
}

func (e *LockedError) Is(target error) bool { return target == ErrLocked }

// lockWait: how long AcquireLock keeps trying, so a Running check that holds the lock for a
// moment never stops a hub from starting.
var lockWait = time.Second

// Lock is a held hub.lock.
type Lock struct {
	mu sync.Mutex
	f  *os.File
}

// AcquireLock takes the data folder's hub.lock for this process, or returns a *LockedError
// when another hub (in this process or another one) holds it. The folder must exist. The
// lock goes when Release is called or the process ends, however it ends; its file stays.
func AcquireLock(dataDir string) (*Lock, error) {
	path := filepath.Join(dataDir, LockFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // close-on-exec: no child keeps it
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockWait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("%w %s: %v", ErrLockUnsupported, path, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, &LockedError{DataDir: dataDir, PID: readPID(path)}
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return &Lock{f: f}, nil
}

// Release lets the lock go. It is safe to call more than once, and on nil.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	_ = l.f.Truncate(0)
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// Running reports whether a hub holds the data folder's hub.lock, and the pid it wrote there.
// It writes nothing and makes no file.
func Running(dataDir string) (bool, int) {
	path := filepath.Join(dataDir, LockFile)
	f, err := os.Open(path)
	if err != nil {
		return false, 0
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false, 0
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, readPID(path)
	}
	return false, 0
}

func readPID(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
