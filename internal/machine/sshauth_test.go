package machine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"nextboard/internal/secret"
	"nextboard/internal/store"
)

// authServer is an SSH server of which each test decides what it accepts. It counts
// what it was asked, because not bothering a server matters here.
type authServer struct {
	addr     string
	password string // "" = takes no passwords
	kbd      string // "" = no keyboard-interactive
	key      ssh.PublicKey
	takeKeys bool
	tries    atomic.Int32 // failed password attempts
}

func startAuthServer(t *testing.T, s *authServer) *authServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{MaxAuthTries: -1}
	cfg.AddHostKey(host)
	if s.password != "" {
		cfg.PasswordCallback = func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == s.password {
				return nil, nil
			}
			s.tries.Add(1)
			return nil, ssh.ErrNoAuth
		}
	}
	if s.kbd != "" {
		cfg.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, ask ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			ans, err := ask("", "", []string{"Verification: "}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(ans) == 1 && ans[0] == s.kbd {
				return nil, nil
			}
			s.tries.Add(1)
			return nil, ssh.ErrNoAuth
		}
	}
	if s.takeKeys || s.key != nil {
		cfg.PublicKeyCallback = func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if s.key != nil && string(k.Marshal()) == string(s.key.Marshal()) {
				return nil, nil
			}
			return nil, ssh.ErrNoAuth
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.Prohibited, "test")
				}
			}()
		}
	}()
	return s
}

// authHome is a hub user with only what the test gives it: no agent, no config, no keys.
func authHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func authRegistry(t *testing.T, dir string) *Registry {
	t.Helper()
	st, err := store.Open(dir)
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
	t.Cleanup(r.Close)
	return r
}

func authKey(t *testing.T, passphrase string) (ssh.PublicKey, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	return sp, pem.EncodeToMemory(block)
}

// connectFirst adds the machine and gets past the unknown host key.
func connectFirst(t *testing.T, r *Registry, req AddRequest, addr string) *SSH {
	t.Helper()
	host, port, _ := net.SplitHostPort(addr)
	req.Host = host
	req.User = "dev"
	for _, c := range port {
		req.Port = req.Port*10 + int(c-'0')
	}
	m, err := r.Add(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = m.Connect(ctx)
	// known already when a test adds a second machine for the same server
	if in := m.Info(); in.PendingHostKey != nil {
		_ = m.AcceptHostKey(ctx, in.PendingHostKey.Fingerprint)
	}
	return m
}

func answer(t *testing.T, m *SSH, kind, value string, remember bool) Info {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = m.Answer(ctx, kind, value, remember)
	return m.Info()
}

func wantNeed(t *testing.T, in Info, kind string, wrong bool) AuthNeed {
	t.Helper()
	if in.Status != StatusAuth || in.NeedsAuth == nil || in.NeedsAuth.Kind != kind || in.NeedsAuth.Wrong != wrong {
		t.Fatalf("mau %s wrong=%v, dapat status %s need %+v err %q", kind, wrong, in.Status, in.NeedsAuth, in.Error)
	}
	return *in.NeedsAuth
}

func TestPasswordAsked(t *testing.T) {
	authHome(t)
	dir := t.TempDir()
	r := authRegistry(t, dir)
	srv := startAuthServer(t, &authServer{password: "right"})
	m := connectFirst(t, r, AddRequest{Name: "pw"}, srv.addr)

	need := wantNeed(t, m.Info(), AuthPassword, false)
	if need.Host != "dev@127.0.0.1" || need.CanPassword {
		t.Fatalf("need: %+v", need)
	}

	// it waits for a person: no trying again in a loop, which would get the hub banned
	time.Sleep(3 * time.Second)
	if got := m.Info().Status; got != StatusAuth {
		t.Fatalf("status berubah sendiri: %s", got)
	}
	if n := srv.tries.Load(); n != 0 {
		t.Fatalf("server sudah dicoba %d kali tanpa password", n)
	}

	// wrong: asked again, and only the one try reached the server
	wantNeed(t, answer(t, m, AuthPassword, "nope", true), AuthPassword, true)
	if n := srv.tries.Load(); n != 1 {
		t.Fatalf("percobaan salah: %d", n)
	}
	credFile := filepath.Join(dir, "creds", m.ID()+".enc")
	if _, err := os.Stat(credFile); !os.IsNotExist(err) {
		t.Fatalf("password salah tersimpan: %v", err)
	}

	in := answer(t, m, AuthPassword, "right", true)
	if in.Status != StatusConnected || in.NeedsAuth != nil {
		t.Fatalf("tersambung: %+v", in)
	}
	blob, err := os.ReadFile(credFile)
	if err != nil {
		t.Fatalf("tidak tersimpan: %v", err)
	}
	if strings.Contains(string(blob), "right") {
		t.Fatal("password tersimpan apa adanya")
	}

	// the next hub start needs nobody
	r.Close()
	r2 := authRegistry(t, dir)
	m2, ok := r2.SSH(m.ID())
	if !ok {
		t.Fatal("machine hilang")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m2.Connect(ctx); err != nil || m2.Info().Status != StatusConnected {
		t.Fatalf("setelah restart: %v %+v", err, m2.Info())
	}
}

func TestPasswordNotRemembered(t *testing.T) {
	authHome(t)
	dir := t.TempDir()
	r := authRegistry(t, dir)
	srv := startAuthServer(t, &authServer{password: "right"})
	m := connectFirst(t, r, AddRequest{Name: "pw"}, srv.addr)
	if in := answer(t, m, AuthPassword, "right", false); in.Status != StatusConnected {
		t.Fatalf("tersambung: %+v", in)
	}
	if _, err := os.Stat(filepath.Join(dir, "creds", m.ID()+".enc")); !os.IsNotExist(err) {
		t.Fatalf("tersimpan padahal tidak diminta: %v", err)
	}
	// it still reconnects while the hub lives
	m.mu.Lock()
	c := m.client
	m.mu.Unlock()
	c.Close()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		again := m.client != nil && m.client != c
		m.mu.Unlock()
		if again {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if in := m.Info(); in.Status != StatusConnected {
		t.Fatalf("tidak tersambung lagi: %+v", in)
	}
	// and asks again after a restart
	r.Close()
	r2 := authRegistry(t, dir)
	m2, _ := r2.SSH(m.ID())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = m2.Connect(ctx)
	wantNeed(t, m2.Info(), AuthPassword, false)
}

func TestPassphraseOfKeyFile(t *testing.T) {
	home := authHome(t)
	dir := t.TempDir()
	pub, pemBytes := authKey(t, "open sesame")
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	r := authRegistry(t, dir)
	srv := startAuthServer(t, &authServer{key: pub})
	m := connectFirst(t, r, AddRequest{Name: "key"}, srv.addr)

	need := wantNeed(t, m.Info(), AuthPassphrase, false)
	if need.KeyPath != "~/.ssh/id_ed25519" || need.CanPassword {
		t.Fatalf("need: %+v", need)
	}
	if in := answer(t, m, AuthPassword, "x", true); in.Status != StatusAuth {
		t.Fatalf("password untuk server yang tidak menerimanya: %+v", in)
	}
	wantNeed(t, answer(t, m, AuthPassphrase, "typo", true), AuthPassphrase, true)
	in := answer(t, m, AuthPassphrase, "open sesame", true)
	if in.Status != StatusConnected {
		t.Fatalf("tersambung: %+v", in)
	}
	r.Close()
	r2 := authRegistry(t, dir)
	m2, _ := r2.SSH(m.ID())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m2.Connect(ctx); err != nil {
		t.Fatalf("passphrase tidak tersimpan: %v %+v", err, m2.Info())
	}
}

// The key in ~/.ssh may be for other servers: a password has to be possible instead.
func TestPassphraseOrPassword(t *testing.T) {
	home := authHome(t)
	_, pemBytes := authKey(t, "unrelated")
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	r := authRegistry(t, t.TempDir())
	srv := startAuthServer(t, &authServer{password: "right", takeKeys: true})
	m := connectFirst(t, r, AddRequest{Name: "both"}, srv.addr)

	if need := wantNeed(t, m.Info(), AuthPassphrase, false); !need.CanPassword {
		t.Fatalf("need: %+v", need)
	}
	wantNeed(t, answer(t, m, AuthPassword, "nope", false), AuthPassword, true)
	if in := answer(t, m, AuthPassword, "right", false); in.Status != StatusConnected {
		t.Fatalf("tersambung: %+v", in)
	}

	// and the opposite order: the passphrase is right, the key is not accepted, then the password
	m2 := connectFirst(t, r, AddRequest{Name: "both2"}, srv.addr)
	wantNeed(t, m2.Info(), AuthPassphrase, false)
	if need := wantNeed(t, answer(t, m2, AuthPassphrase, "unrelated", false), AuthPassword, false); need.Wrong {
		t.Fatalf("need: %+v", need)
	}
}

func TestPassphraseOfUploadedKey(t *testing.T) {
	authHome(t)
	pub, pemBytes := authKey(t, "uploaded secret")
	r := authRegistry(t, t.TempDir())
	srv := startAuthServer(t, &authServer{key: pub})
	m := connectFirst(t, r, AddRequest{Name: "up", AuthMethod: "key", PrivateKey: string(pemBytes)}, srv.addr)
	if need := wantNeed(t, m.Info(), AuthPassphrase, false); need.KeyPath != "" {
		t.Fatalf("need: %+v", need)
	}
	wantNeed(t, answer(t, m, AuthPassphrase, "typo", true), AuthPassphrase, true)
	if in := answer(t, m, AuthPassphrase, "uploaded secret", true); in.Status != StatusConnected {
		t.Fatalf("tersambung: %+v", in)
	}
	// typed into the add form, and wrong
	m2 := connectFirst(t, r, AddRequest{Name: "up2", AuthMethod: "key", PrivateKey: string(pemBytes), Passphrase: "typo"}, srv.addr)
	wantNeed(t, m2.Info(), AuthPassphrase, true)
}

func TestKeyboardInteractive(t *testing.T) {
	authHome(t)
	r := authRegistry(t, t.TempDir())
	srv := startAuthServer(t, &authServer{kbd: "right"})
	m := connectFirst(t, r, AddRequest{Name: "kbd"}, srv.addr)
	if need := wantNeed(t, m.Info(), AuthPassword, false); need.Prompt != "Verification:" {
		t.Fatalf("need: %+v", need)
	}
	if in := answer(t, m, AuthPassword, "right", false); in.Status != StatusConnected {
		t.Fatalf("tersambung: %+v", in)
	}
}

// A password chosen in the form and refused goes straight back to the person.
func TestPasswordMethodWrong(t *testing.T) {
	authHome(t)
	r := authRegistry(t, t.TempDir())
	srv := startAuthServer(t, &authServer{password: "right"})
	m := connectFirst(t, r, AddRequest{Name: "pw", AuthMethod: "password", Password: "nope"}, srv.addr)
	wantNeed(t, m.Info(), AuthPassword, true)
	if in := answer(t, m, AuthPassword, "right", true); in.Status != StatusConnected {
		t.Fatalf("tersambung: %+v", in)
	}
}

// Nothing a person could type helps here: the old message stays, and it keeps retrying.
func TestNoKeysAndNoPasswords(t *testing.T) {
	authHome(t)
	r := authRegistry(t, t.TempDir())
	srv := startAuthServer(t, &authServer{takeKeys: true})
	m := connectFirst(t, r, AddRequest{Name: "none"}, srv.addr)
	in := m.Info()
	if in.NeedsAuth != nil || in.Status == StatusAuth || !strings.Contains(in.Error, "tidak ada SSH key") {
		t.Fatalf("status: %+v", in)
	}
}

func TestAnswerNotAsked(t *testing.T) {
	authHome(t)
	r := authRegistry(t, t.TempDir())
	m, err := r.Add(AddRequest{Name: "x", Host: "127.0.0.1", Port: 1, User: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Answer(context.Background(), AuthPassword, "x", true); err != ErrAuthNotNeeded {
		t.Fatalf("tidak ditanya: %v", err)
	}
}

func TestOpenKey(t *testing.T) {
	_, plain := authKey(t, "")
	if _, st, err := openKey(plain, ""); err != nil || st != keyOpen {
		t.Fatalf("tanpa passphrase: %v %v", st, err)
	}
	_, locked := authKey(t, "secret")
	if _, st, err := openKey(locked, ""); err != nil || st != keyLocked {
		t.Fatalf("terkunci: %v %v", st, err)
	}
	if _, st, err := openKey(locked, "wrong"); err != nil || st != keyWrong {
		t.Fatalf("salah: %v %v", st, err)
	}
	if _, st, err := openKey(locked, "secret"); err != nil || st != keyOpen {
		t.Fatalf("benar: %v %v", st, err)
	}
	if _, _, err := openKey([]byte("not a key"), ""); err == nil {
		t.Fatal("bukan key harus error")
	}
}
