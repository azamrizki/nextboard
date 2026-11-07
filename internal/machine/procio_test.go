package machine

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestReadBytesCountsTheProcessAndItsChildren(t *testing.T) {
	if _, err := os.Stat("/proc/self/io"); err != nil {
		t.Skip("no /proc")
	}
	// a shell whose child keeps reading (a byte at a time, until a newline that never comes)
	cmd := exec.Command("/bin/sh", "-c", `sh -c 'read -r x </dev/zero' & wait`)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = exec.Command("pkill", "-KILL", "-P", strconv.Itoa(cmd.Process.Pid)).Run()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	before, ok := ReadBytes(cmd.Process.Pid)
	if !ok {
		t.Fatal("no answer for a live process")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		after, _ := ReadBytes(cmd.Process.Pid)
		if after-before >= 64<<10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("read %d bytes, want the child's reading too", after-before)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok := ReadBytes(1 << 30); ok {
		t.Fatal("an answer for no process")
	}
}
