package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

// SPEC F2.1: a machine added by its ~/.ssh/config name connects with nothing but
// that config, and adding it does not touch the server.
func TestAddMachineFromSSHConfig(t *testing.T) {
	if os.Getenv("NEXTBOARD_SSHD_TEST") == "" {
		t.Skip("set NEXTBOARD_SSHD_TEST=1 untuk menjalankan uji sshd")
	}
	sshd := startSSHD(t)
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(sshd.dir, "user")
	sshHome(t, map[string]string{"config": fmt.Sprintf(`
Host box
  HostName 127.0.0.1
  Port %s
  User %s
  IdentityFile %s
  IdentitiesOnly yes

Match exec "false"
  User nobody
`, sshd.port, u.Username, keyFile)})

	hosts := hostsByName(t)
	if h := hosts["box"]; h.HostName != "127.0.0.1" || h.IdentityFile != keyFile {
		t.Fatalf("listed %+v", h)
	}

	reg := newTestRegistry(t)
	m, err := reg.Add(AddRequest{Name: "box", SSHConfigHost: "box", AuthMethod: "sshconfig", PersistMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Connected() {
		t.Fatal("adding a machine must not connect")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = m.Connect(ctx)
	var unknown *errHostKeyUnknown
	if !errors.As(err, &unknown) {
		t.Fatalf("a new host must ask first: %v", err)
	}
	if err := m.AcceptHostKey(ctx, m.Info().PendingHostKey.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if !m.Connected() {
		t.Fatal("not connected")
	}
	out, err := m.Exec(ctx, "printf nb-%s-ok ssh")
	if err != nil || out != "nb-ssh-ok" {
		t.Fatalf("%q %v", out, err)
	}
}
