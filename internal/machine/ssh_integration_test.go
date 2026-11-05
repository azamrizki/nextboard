package machine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

// testSSHD runs an unprivileged OpenSSH sshd on a random loopback port with
// throwaway host/user keys. Nothing in ~/.ssh is touched.
type testSSHD struct {
	dir, port, userKey string
	cmd                *exec.Cmd
}

// startSSHD starts the throwaway sshd; extra lines are appended to its
// config (e.g. "Subsystem sftp internal-sftp"). Without one, the server has
// no SFTP subsystem.
func startSSHD(t *testing.T, extra ...string) *testSSHD {
	t.Helper()
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		t.Skip("sshd tidak tersedia")
	}
	dir := t.TempDir()
	keygen := func(path string) {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
	}
	keygen(filepath.Join(dir, "hostkey"))
	keygen(filepath.Join(dir, "user"))
	pub, _ := os.ReadFile(filepath.Join(dir, "user.pub"))
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()
	cfg := fmt.Sprintf(`Port %s
ListenAddress 127.0.0.1
HostKey %s/hostkey
PidFile %s/sshd.pid
AuthorizedKeysFile %s/authorized_keys
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
StrictModes no
`, port, dir, dir, dir) + strings.Join(extra, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "sshd_config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &testSSHD{dir: dir, port: port}
	s.start(t)
	b, _ := os.ReadFile(filepath.Join(dir, "user"))
	s.userKey = string(b)
	return s
}

func (s *testSSHD) start(t *testing.T) {
	t.Helper()
	s.cmd = exec.Command("/usr/sbin/sshd", "-D", "-e", "-f", filepath.Join(s.dir, "sshd_config"))
	if err := s.cmd.Start(); err != nil {
		t.Skipf("sshd tidak bisa jalan: %v", err)
	}
	t.Cleanup(func() { s.stop() })
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", "127.0.0.1:"+s.port); err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Skip("sshd tidak listen")
}

func (s *testSSHD) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
		s.cmd = nil
	}
}

func newTestRegistry(t *testing.T) *Registry {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.Open(st.Path("secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenRegistry(st, box)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSSHAgentlessEndToEnd(t *testing.T) {
	if os.Getenv("NEXTBOARD_SSHD_TEST") == "" {
		t.Skip("set NEXTBOARD_SSHD_TEST=1 untuk menjalankan uji sshd")
	}
	sshd := startSSHD(t)
	reg := newTestRegistry(t)
	connected := make(chan string, 4)
	reg.OnConnect = func(id string) { connected <- id }
	port := 0
	fmt.Sscan(sshd.port, &port)
	m, err := reg.Add(AddRequest{Name: "test", Host: "127.0.0.1", Port: port, AuthMethod: "key", PrivateKey: sshd.userKey, PersistMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Unknown host → pending confirmation (TOFU), nothing accepted yet.
	err = m.Connect(ctx)
	var unk *errHostKeyUnknown
	if !errors.As(err, &unk) {
		t.Fatalf("expected unknown host key, got %v", err)
	}
	in := m.Info()
	if in.Status != StatusHostKey || in.PendingHostKey == nil || !strings.HasPrefix(in.PendingHostKey.Fingerprint, "SHA256:") {
		t.Fatalf("info %+v", in)
	}
	if err := m.AcceptHostKey(ctx, "SHA256:wrong"); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("wrong fp accepted: %v", err)
	}
	if err := m.AcceptHostKey(ctx, in.PendingHostKey.Fingerprint); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !m.Connected() {
		t.Fatal("not connected after accept")
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("OnConnect not called")
	}

	// 2. Probe ran on first connect.
	p := m.Info().Probe
	if p == nil || p.Home == "" || p.OS == "" || p.Shell == "" {
		raw, err := m.Exec(ctx, loginWrap(probeScript([]string{"claude"})))
		t.Fatalf("probe %+v\nraw=%q err=%v", p, raw, err)
	}

	// 3. Folder picker.
	weird := filepath.Join(t.TempDir(), "it's a \"dir\" $HOME")
	if err := os.MkdirAll(filepath.Join(weird, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := m.ListDir(ctx, weird)
	if err != nil || l.Path != weird || len(l.Entries) != 1 || l.Entries[0].Name != "sub" || !l.Entries[0].Dir {
		t.Fatalf("ls %+v %v", l, err)
	}

	// 4. Terminal over an SSH channel with PTY, cwd quoting and env inline.
	proc, err := m.Start(StartSpec{
		SessionID: "s_test", Command: "sh", Args: []string{"-c", `pwd; echo "id=$NEXTBOARD_SESSION_ID"; stty size; exit 7`},
		Cwd: weird, Cols: 91, Rows: 27,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(proc)
	code, werr := proc.Wait()
	s := string(out)
	if !strings.Contains(s, weird) || !strings.Contains(s, "id=s_test") || !strings.Contains(s, "27 91") || code != 7 || werr != nil {
		t.Fatalf("remote run: code=%d err=%v out=%q", code, werr, s)
	}

	// 5. Resize + kill (direct mode: SIGHUP then channel close).
	proc, err = m.Start(StartSpec{SessionID: "s_test2", Command: "sh", Args: []string{"-c", "trap 'echo resized; stty size' WINCH; while :; do sleep 0.1; done"}, Cwd: "~", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, proc)
	time.Sleep(300 * time.Millisecond)
	if err := proc.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	proc.Signal("HUP")
	done := make(chan struct{})
	go func() { proc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		proc.Signal("KILL")
		<-done
	}

	// 6. Connection drop → Wait reports ErrDisconnected, loop reconnects.
	proc, err = m.Start(StartSpec{SessionID: "s_test3", Command: "sleep", Args: []string{"30"}, Cwd: "~", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	m.EnsureLoop()
	m.currentClient().Close()
	if _, err := proc.Wait(); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("expected ErrDisconnected, got %v", err)
	}
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("did not reconnect")
	}

	// 7. Host key changes → refused with mismatch, no auto-accept.
	sshd.stop()
	os.Remove(filepath.Join(sshd.dir, "hostkey"))
	os.Remove(filepath.Join(sshd.dir, "hostkey.pub"))
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(sshd.dir, "hostkey")).Run()
	sshd.start(t)
	m.Close()
	m2 := newSSH(m.Config(), reg.hk, func() (*Creds, error) { return reg.loadCreds(m.ID()) })
	err = m2.Connect(ctx)
	var chg *errHostKeyChanged
	if !errors.As(err, &chg) || !m2.Info().HostKeyMismatch || m2.Info().PendingHostKey != nil {
		t.Fatalf("expected host key changed, got %v %+v", err, m2.Info())
	}
}
