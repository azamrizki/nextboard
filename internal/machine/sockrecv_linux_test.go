//go:build linux

package machine

import (
	"bufio"
	"net"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// A process that reads its socket with recv(2), as Python does, leaves rchar alone; what its
// socket received still counts in ReadBytes.
func TestReadBytesCountsRecv(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	cmd := exec.Command("python3", "-I", "-c", `
import socket, sys
s = socket.create_connection(("127.0.0.1", `+port+`))
print("ready", flush=True)
while True:
    b = s.recv(65536)
    if not b:
        break
`)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "ready\n" {
		t.Fatalf("child: %q", line)
	}
	before, ok := ReadBytes(cmd.Process.Pid)
	if !ok {
		t.Fatal("no reading for the child")
	}
	r1, _ := rchar(cmd.Process.Pid)
	const n = 1 << 20
	if _, err := conn.Write(make([]byte, n)); err != nil {
		t.Fatal(err)
	}
	var after uint64
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(diagCacheFor + 100*time.Millisecond)
		if after, _ = ReadBytes(cmd.Process.Pid); after >= before+n {
			break
		}
	}
	r2, _ := rchar(cmd.Process.Pid)
	if after < before+n {
		t.Fatalf("a megabyte through recv: %d → %d", before, after)
	}
	if r2-r1 >= n {
		t.Logf("rchar saw it too (%d): this Python reads with read(2)", r2-r1)
	}
}
