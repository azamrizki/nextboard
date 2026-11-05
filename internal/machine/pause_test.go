package machine

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// procState returns the state letter of /proc/<pid>/stat ("T" = stopped).
func procState(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return ""
	}
	return s[i+2 : i+3]
}

type lockedOut struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *lockedOut) add(p []byte) { o.mu.Lock(); o.b.Write(p); o.mu.Unlock() }
func (o *lockedOut) len() int     { o.mu.Lock(); defer o.mu.Unlock(); return o.b.Len() }

// TestLocalSignalForeground stops and continues the foreground process
// group of a real PTY child.
func TestLocalSignalForeground(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("butuh /proc")
	}
	l := NewLocal(Config{})
	p, err := l.Start(StartSpec{SessionID: "s_pause", Command: "/bin/sh", Args: []string{"-c", "while :; do echo tick; sleep 0.05; done"}, Cols: 80, Rows: 24, Cwd: t.TempDir()})
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
	time.Sleep(300 * time.Millisecond)
	pid := p.(*localProc).Pid()
	fs := p.(ForegroundSignaler)
	if err := fs.SignalForeground(context.Background(), 0, "STOP"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for procState(pid) != "T" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := procState(pid); st != "T" {
		t.Fatalf("state after STOP = %q", st)
	}
	time.Sleep(150 * time.Millisecond)
	n := out.len()
	time.Sleep(400 * time.Millisecond)
	if out.len() != n {
		t.Fatal("output continued while stopped")
	}
	if err := fs.SignalForeground(context.Background(), 0, "CONT"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for out.len() == n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if out.len() == n || procState(pid) == "T" {
		t.Fatalf("not continued: state %q", procState(pid))
	}
	if err := fs.SignalForeground(context.Background(), 0, "KILL"); err == nil {
		t.Fatal("only STOP/CONT allowed")
	}
}

// TestRemoteSignalScriptRefusesForeignSession runs the remote script with
// a fake ps: a foreground group in another session is never signalled,
// nor a group ≤ 1, nor one whose ps output is missing.
func TestRemoteSignalScriptRefusesForeignSession(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "kill.log")
	// fake ps: "-o tpgid= -p P" → $TPGID; "-o sid= -p X" → SID_<X>.
	ps := `#!/bin/sh
f=$2; p=$4
case "$f" in tpgid=) [ -n "$TPGID" ] && echo " $TPGID";; sid=) eval "v=\$SID_$p"; [ -n "$v" ] && echo " $v";; esac
`
	kill := "#!/bin/sh\necho \"$@\" >> " + log + "\n"
	os.WriteFile(filepath.Join(dir, "ps"), []byte(ps), 0o755)
	os.WriteFile(filepath.Join(dir, "kill"), []byte(kill), 0o755)
	run := func(env ...string) string {
		// "kill" is a shell builtin; the script is rewritten to call the
		// fake binary so the test observes what would be signalled.
		script := strings.Replace(remoteSignalScript(500, "STOP"), "kill -s STOP", "command \"$KILLBIN\" -s STOP", 1)
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Env = append([]string{"PATH=" + dir + ":/usr/bin:/bin", "KILLBIN=" + filepath.Join(dir, "kill")}, env...)
		b, _ := cmd.CombinedOutput()
		return string(b)
	}
	if out := run("TPGID=700", "SID_500=500", "SID_700=900"); !strings.Contains(out, "__NB_SIG_REFUSED__") {
		t.Fatalf("foreign session: %q", out)
	}
	if out := run("TPGID=1", "SID_500=1", "SID_1=1"); !strings.Contains(out, "__NB_SIG_REFUSED__") {
		t.Fatalf("pgid 1: %q", out)
	}
	if out := run("SID_500=500"); !strings.Contains(out, "__NB_SIG_PS__") {
		t.Fatalf("no tpgid: %q", out)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("kill was called: %q", b)
	}
	if out := run("TPGID=700", "SID_500=500", "SID_700=500"); !strings.Contains(out, "__NB_SIG_OK__") {
		t.Fatalf("same session: %q", out)
	}
	if b, _ := os.ReadFile(log); strings.TrimSpace(string(b)) != "-s STOP -- -700" {
		t.Fatalf("kill args %q", b)
	}
}
