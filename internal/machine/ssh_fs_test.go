package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// connectTestSSHD adds the throwaway sshd as a machine and accepts its key.
func connectTestSSHD(t *testing.T, reg *Registry, sshd *testSSHD) *SSH {
	t.Helper()
	port := 0
	fmt.Sscan(sshd.port, &port)
	m, err := reg.Add(AddRequest{Host: "127.0.0.1", Port: port, AuthMethod: "key", PrivateKey: sshd.userKey, PersistMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = m.Connect(ctx)
	if hk := m.Info().PendingHostKey; hk != nil {
		if err := m.AcceptHostKey(ctx, hk.Fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	if !m.Connected() {
		t.Fatalf("not connected: %+v", m.Info())
	}
	return m
}

func TestSSHFileAPI(t *testing.T) {
	if os.Getenv("NEXTBOARD_SSHD_TEST") == "" {
		t.Skip("set NEXTBOARD_SSHD_TEST=1 untuk menjalankan uji sshd")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	home, _ := os.UserHomeDir()
	dir := t.TempDir()
	txt := filepath.Join(dir, "it's notes.md")
	os.WriteFile(txt, []byte("remote ✓ text"), 0o644)
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2}, 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)

	reg := newTestRegistry(t)
	m := connectTestSSHD(t, reg, startSSHD(t, "Subsystem sftp internal-sftp"))

	t.Run("sftp", func(t *testing.T) {
		st, err := m.Stat(ctx, txt)
		if err != nil || !st.Exists || st.Dir || st.Size != int64(len("remote ✓ text")) || st.Mtime.IsZero() {
			t.Fatalf("stat %+v %v", st, err)
		}
		sc1, _, done1, _ := m.sftpClient(ctx)
		done1()
		if st, err := m.Stat(ctx, filepath.Join(dir, "missing")); err != nil || st.Exists {
			t.Fatalf("missing %+v %v", st, err)
		}
		if st, err := m.Stat(ctx, "~"); err != nil || !st.Dir || st.Path != home {
			t.Fatalf("~ %+v %v", st, err)
		}
		if _, err := m.Stat(ctx, "rel/path"); !errors.Is(err, ErrRelativePath) {
			t.Fatalf("relative %v", err)
		}
		if sc2, _, done2, _ := m.sftpClient(ctx); sc2 != sc1 {
			t.Fatal("SFTP client not reused")
		} else {
			done2()
		}

		fc, err := m.ReadFile(ctx, txt, 8) // "remote " + first byte of ✓
		if err != nil || fc.Content != "remote " || !fc.Truncated || fc.Binary {
			t.Fatalf("read %+v %v", fc, err)
		}
		if fc, _ := m.ReadFile(ctx, filepath.Join(dir, "blob.bin"), 100); !fc.Binary {
			t.Fatal("binary not detected")
		}
		if _, err := m.ReadFile(ctx, dir, 100); !errors.Is(err, ErrIsDir) {
			t.Fatalf("read dir %v", err)
		}
		rc, rst, err := m.OpenRaw(ctx, txt)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(rc)
		rc.Close()
		if string(raw) != "remote ✓ text" || rst.Name != "it's notes.md" {
			t.Fatalf("raw %q %+v", raw, rst)
		}

		out := filepath.Join(dir, "written.txt")
		ws, err := m.WriteFile(ctx, out, []byte("v1"), WriteOptions{})
		if err != nil || ws.Size != 2 {
			t.Fatalf("write %+v %v", ws, err)
		}
		stale := ws.Mtime.Add(-time.Hour)
		if _, err := m.WriteFile(ctx, out, []byte("v2"), WriteOptions{IfMtime: &stale}); !errors.Is(err, ErrMtimeMismatch) {
			t.Fatalf("stale ifMtime %v", err)
		}
		if _, err := m.WriteFile(ctx, out, []byte("v3"), WriteOptions{IfMtime: &ws.Mtime}); err != nil {
			t.Fatalf("ifMtime %v", err)
		}
		if b, _ := os.ReadFile(out); string(b) != "v3" {
			t.Fatalf("content %q", b)
		}
		for _, want := range []string{"it's notes-2.md", "it's notes-3.md"} {
			u, err := m.WriteFile(ctx, txt, []byte("copy"), WriteOptions{Unique: true})
			if err != nil || u.Path != filepath.Join(dir, want) {
				t.Fatalf("unique %+v %v", u, err)
			}
		}
		if b, _ := os.ReadFile(txt); string(b) != "remote ✓ text" {
			t.Fatal("unique write clobbered the original")
		}

		l, err := m.ListDir(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		if l.Entries[0].Name != "sub" || !l.Entries[0].Dir {
			t.Fatalf("dirs first %+v", l.Entries)
		}
		ok := false
		for _, e := range l.Entries {
			if e.Name == "it's notes.md" {
				ok = e.Size == int64(len("remote ✓ text")) && !e.Mtime.IsZero()
			}
		}
		if !ok {
			t.Fatalf("ls sizes %+v", l.Entries)
		}

		// Reconnect: the SFTP client is recreated on the new connection.
		connected := make(chan struct{}, 1)
		m.onConnect = func() { connected <- struct{}{} }
		m.EnsureLoop()
		m.currentClient().Close()
		select {
		case <-connected:
		case <-time.After(15 * time.Second):
			t.Fatal("no reconnect")
		}
		if st, err := m.Stat(ctx, txt); err != nil || !st.Exists {
			t.Fatalf("stat after reconnect %+v %v", st, err)
		}
		sc3, _, done3, _ := m.sftpClient(ctx)
		if sc3 == sc1 {
			t.Fatal("stale SFTP client reused after reconnect")
		}
		done3()
		m.mu.Lock()
		inflight := m.sftp.inflight
		m.mu.Unlock()
		if inflight != 0 {
			t.Fatalf("leaked SFTP refs: %d", inflight)
		}
	})

	t.Run("git", func(t *testing.T) {
		if g := GitStatus(ctx, m, dir); g.IsRepo || g.Error != "" {
			t.Fatalf("non-repo %+v", g)
		}
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git tidak ada")
		}
		if out, err := exec.Command("git", "-C", dir, "init", "-q", "-b", "dev").CombinedOutput(); err != nil {
			t.Fatalf("git init %v %s", err, out)
		}
		g := GitStatus(ctx, m, dir)
		if !g.IsRepo || g.Branch != "dev" || g.Changed < 3 {
			t.Fatalf("repo %+v", g)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git", "index")); err == nil {
			t.Fatal("git status wrote the index on the remote")
		}
	})

	t.Run("exec fallback", func(t *testing.T) {
		m2 := connectTestSSHD(t, reg, startSSHD(t)) // no SFTP subsystem
		st, err := m2.Stat(ctx, txt)
		if err != nil || !st.Exists || st.Size != int64(len("remote ✓ text")) || st.Mtime.IsZero() || st.Path != txt {
			t.Fatalf("exec stat %+v %v", st, err)
		}
		if _, _, _, err := m2.sftpClient(ctx); !errors.Is(err, ErrNoSFTP) {
			t.Fatalf("expected ErrNoSFTP, got %v", err)
		}
		if st, _ := m2.Stat(ctx, filepath.Join(dir, "missing")); st == nil || st.Exists {
			t.Fatalf("exec missing %+v", st)
		}
		if st, _ := m2.Stat(ctx, "~"); st == nil || !st.Dir || st.Path != home {
			t.Fatalf("exec ~ %+v", st)
		}
		fc, err := m2.ReadFile(ctx, txt, 8)
		if err != nil || fc.Content != "remote " || !fc.Truncated {
			t.Fatalf("exec read %+v %v", fc, err)
		}
		if fc, err := m2.ReadFile(ctx, txt, 1000); err != nil || fc.Content != "remote ✓ text" || fc.Truncated {
			t.Fatalf("exec full read %+v %v", fc, err)
		}
		if fc, _ := m2.ReadFile(ctx, filepath.Join(dir, "blob.bin"), 100); fc == nil || !fc.Binary {
			t.Fatal("exec binary")
		}
		if _, err := m2.ReadFile(ctx, filepath.Join(dir, "sub"), 10); !errors.Is(err, ErrIsDir) {
			t.Fatalf("exec read dir %v", err)
		}
		if _, err := m2.ReadFile(ctx, filepath.Join(dir, "missing"), 10); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("exec read missing %v", err)
		}
		rc, _, err := m2.OpenRaw(ctx, filepath.Join(dir, "blob.bin"))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(rc)
		if !bytes.Equal(raw, []byte{0, 1, 2}) {
			t.Fatalf("exec raw %v", raw)
		}
		if _, err := m2.WriteFile(ctx, filepath.Join(dir, "nope.txt"), []byte("x"), WriteOptions{}); !errors.Is(err, ErrNoSFTP) {
			t.Fatalf("write without SFTP: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "nope.txt")); err == nil {
			t.Fatal("exec fallback wrote a file")
		}
		l, err := m2.ListDir(ctx, dir)
		if err != nil || len(l.Entries) == 0 || !strings.HasPrefix(l.Path, dir) {
			t.Fatalf("exec ls %+v %v", l, err)
		}
	})
}
