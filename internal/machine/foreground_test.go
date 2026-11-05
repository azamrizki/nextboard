package machine

import (
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
)

// The check an SSH console runs on the server, against a real interactive shell: at the
// prompt the shell itself is in the foreground, with a command that command is.
func TestRemoteForegroundScriptSeesTheCommand(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-i")
	f, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = f.Close()
	}()
	go func() { _, _ = io.Copy(io.Discard, f) }()
	pid := cmd.Process.Pid
	ask := func() (int, string) {
		out, _ := exec.Command("/bin/sh", "-c", remoteForegroundScript(pid)).Output()
		pgid, comm, ok := parseRemoteForeground(string(out))
		if !ok {
			t.Fatalf("no answer in %q", out)
		}
		return pgid, comm
	}
	wait := func(want func(int, string) bool) (int, string) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			pgid, comm := ask()
			if want(pgid, comm) || time.Now().After(deadline) {
				return pgid, comm
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if pgid, comm := wait(func(p int, _ string) bool { return p == pid }); pgid != pid {
		t.Fatalf("at the prompt: group %d (%s), shell %d", pgid, comm, pid)
	}
	if _, err := f.Write([]byte("sleep 30\n")); err != nil {
		t.Fatal(err)
	}
	if pgid, comm := wait(func(p int, c string) bool { return p != pid && c == "sleep" }); pgid == pid || comm != "sleep" {
		t.Fatalf("with a command: group %d (%s), shell %d", pgid, comm, pid)
	}
	if _, err := f.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	if pgid, comm := wait(func(p int, _ string) bool { return p == pid }); pgid != pid {
		t.Fatalf("after Ctrl-C: group %d (%s), shell %d", pgid, comm, pid)
	}
}
