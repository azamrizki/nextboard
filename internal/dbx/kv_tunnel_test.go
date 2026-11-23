package dbx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/ssh"
)

func kvFor(t *testing.T, sec Security) (*kvEngine, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	host, port, _ := net.SplitHostPort(mr.Addr())
	p, _ := strconv.Atoi(port)
	c := Connection{Name: "cache", Driver: Redis, Host: host, Port: p, Security: sec}
	if err := normalize(&c); err != nil {
		t.Fatal(err)
	}
	c.Security = sec
	var d net.Dialer
	e, err := newKVEngine(context.Background(), c, "", d.DialContext)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.close)
	return e, mr
}

func TestRedisKeysValuesAndTheConsole(t *testing.T) {
	ctx := context.Background()
	e, mr := kvFor(t, Security{})
	mr.HSet("session:a", "user_id", "77", "ip", "10.0.4.18")
	mr.Set("laravel_settings", "{\"x\":1}")
	mr.RPush("queues:default", "job1", "job2")
	mr.ZAdd("leaderboard", 3, "ani")
	mr.SetTTL("session:a", time.Hour)

	page, err := e.Scan(ctx, 0, 0, "session:*", "", 100)
	if err != nil || len(page.Keys) != 1 || page.Keys[0].Type != "hash" || page.Keys[0].TTL <= 0 {
		t.Fatalf("scan %+v %v", page, err)
	}
	kv, err := e.Key(ctx, 0, "session:a", "")
	if err != nil || kv.Type != "hash" || kv.Length != 2 || len(kv.Items) != 2 {
		t.Fatalf("hash %+v %v", kv, err)
	}
	if kv, _ := e.Key(ctx, 0, "queues:default", ""); kv.Type != "list" || len(kv.Items) != 2 {
		t.Errorf("list %+v", kv)
	}
	if kv, _ := e.Key(ctx, 0, "leaderboard", ""); kv.Type != "zset" || kv.Items[0][1] != 3.0 {
		t.Errorf("zset %+v", kv)
	}
	out, err := e.Command(ctx, CommandOpts{Line: `HGET session:a user_id`})
	if err != nil || out != `"77"` {
		t.Errorf("HGET %q %v", out, err)
	}
	// KEYS and blocking commands are refused; FLUSHDB asks for the name typed
	if _, err := e.Command(ctx, CommandOpts{Line: "KEYS *"}); err == nil || !strings.Contains(err.Error(), "SCAN") {
		t.Errorf("KEYS: %v", err)
	}
	if _, err := e.Command(ctx, CommandOpts{Line: "BLPOP q 0"}); err == nil {
		t.Error("BLPOP ran")
	}
	var ce *ConfirmError
	if _, err := e.Command(ctx, CommandOpts{Line: "FLUSHDB"}); !errors.As(err, &ce) || ce.Code != "confirm_name" {
		t.Fatalf("FLUSHDB without the name: %v", err)
	}
	if _, err := e.Command(ctx, CommandOpts{Line: "FLUSHDB", ConfirmName: "cache"}); err != nil {
		t.Fatalf("FLUSHDB with the name: %v", err)
	}
	if mr.Exists("session:a") {
		t.Error("FLUSHDB did not run")
	}
	if _, err := e.Write(ctx, KeyWrite{Key: "k", Op: "set", Value: "v"}); err != nil || mr.Exists("k") == false {
		t.Errorf("set: %v", err)
	}
}

func TestRedisReadOnlyRefusesWrites(t *testing.T) {
	ctx := context.Background()
	e, mr := kvFor(t, Security{ReadOnly: true})
	mr.Set("a", "1")
	var ro *ReadOnlyError
	if _, err := e.Command(ctx, CommandOpts{Line: "SET a 2"}); !errors.As(err, &ro) {
		t.Errorf("SET on read-only: %v", err)
	}
	if _, err := e.Write(ctx, KeyWrite{Key: "a", Op: "del"}); !errors.As(err, &ro) {
		t.Errorf("del on read-only: %v", err)
	}
	if out, err := e.Command(ctx, CommandOpts{Line: "GET a"}); err != nil || out != `"1"` {
		t.Errorf("GET on read-only: %q %v", out, err)
	}
	// a confirmed write still asks first when the connection says so
	e2, _ := kvFor(t, Security{ConfirmWrites: true})
	var ce *ConfirmError
	if _, err := e2.Command(ctx, CommandOpts{Line: "SET b 1"}); !errors.As(err, &ce) || ce.Code != "confirm" {
		t.Errorf("SET without confirming: %v", err)
	}
	if _, err := e2.Command(ctx, CommandOpts{Line: "SET b 1", Confirmed: true}); err != nil {
		t.Errorf("SET confirmed: %v", err)
	}
}

func TestSplitArgsLikeRedisCLI(t *testing.T) {
	got, err := SplitArgs(`SET "a b" 'c d' e\ f "x\"y"`)
	if err != nil || len(got) != 6 || got[1] != "a b" || got[2] != "c d" || got[5] != `x"y` {
		t.Errorf("%q %v", got, err)
	}
	if _, err := SplitArgs(`SET "a`); err == nil {
		t.Error("an open quote passed")
	}
}

// sshServer is an SSH server that accepts a password and forwards direct-tcpip channels.
func sshServer(t *testing.T, password string) (addr, fingerprint string) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if string(p) == password {
			return nil, nil
		}
		return nil, errors.New("no")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					if nc.ChannelType() != "direct-tcpip" {
						_ = nc.Reject(ssh.UnknownChannelType, "no")
						continue
					}
					var p struct {
						Host     string
						Port     uint32
						OrigHost string
						OrigPort uint32
					}
					if ssh.Unmarshal(nc.ExtraData(), &p) != nil {
						_ = nc.Reject(ssh.ConnectionFailed, "bad")
						continue
					}
					up, err := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
					if err != nil {
						_ = nc.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					ch, creqs, _ := nc.Accept()
					go ssh.DiscardRequests(creqs)
					go func() { _, _ = io.Copy(ch, up); ch.Close() }()
					go func() { _, _ = io.Copy(up, ch); up.Close() }()
				}
			}()
		}
	}()
	return ln.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey())
}

func hopAt(addr, user, pass, hostKey string) hopCfg {
	h, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	return hopCfg{Hop: Hop{Host: h, Port: port, User: user, Auth: AuthPassword, HostKey: hostKey}, secret: pass}
}

func TestTunnelThroughAJumpTrustsOnlyKnownHostKeys(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	jump, jumpFP := sshServer(t, "j-pass")
	host, hostFP := sshServer(t, "h-pass")

	// an unknown host key stops the way and names its fingerprint
	_, res, err := openTunnel(ctx, []hopCfg{hopAt(jump, "deploy", "j-pass", "")}, mr.Addr(), 5*time.Second, 0)
	var hk *HostKeyError
	if !errors.As(err, &hk) || hk.Changed || hk.Fingerprint != jumpFP || hk.Hop != 0 || len(res) != 1 {
		t.Fatalf("unknown key: %v %+v", err, hk)
	}
	// a key that changed since it was trusted is refused
	_, _, err = openTunnel(ctx, []hopCfg{hopAt(jump, "deploy", "j-pass", hostFP)}, mr.Addr(), 5*time.Second, 0)
	if !errors.As(err, &hk) || !hk.Changed {
		t.Fatalf("changed key: %v", err)
	}
	// a wrong password says so
	if _, _, err = openTunnel(ctx, []hopCfg{hopAt(jump, "deploy", "nope", jumpFP)}, mr.Addr(), 5*time.Second, 0); err == nil {
		t.Fatal("a wrong password passed")
	}
	// jump, then host, then the database the host reaches
	tun, res, err := openTunnel(ctx, []hopCfg{hopAt(jump, "deploy", "j-pass", jumpFP), hopAt(host, "ubuntu", "h-pass", hostFP)}, mr.Addr(), 5*time.Second, time.Second)
	if err != nil || len(res) != 2 {
		t.Fatalf("open: %v %+v", err, res)
	}
	defer tun.close(nil)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr(), Dialer: func(ctx context.Context, n, a string) (net.Conn, error) { return tun.dial(ctx, n, mr.Addr()) }})
	defer rc.Close()
	if err := rc.Set(ctx, "through", "the tunnel", 0).Err(); err != nil || mr.Exists("through") == false {
		t.Fatalf("through the tunnel: %v", err)
	}
	// the port on This PC for agents and tools
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if err := tun.listen(port); err != nil {
		t.Fatal(err)
	}
	local := redis.NewClient(&redis.Options{Addr: "127.0.0.1:" + strconv.Itoa(port)})
	defer local.Close()
	if v, err := local.Get(ctx, "through").Result(); err != nil || v != "the tunnel" {
		t.Fatalf("the local port: %q %v", v, err)
	}
	tun.close(nil)
	if _, err := tun.dial(ctx, "tcp", mr.Addr()); err == nil {
		t.Error("a closed tunnel still dials")
	}
}

func TestServiceTestsATunnelledConnection(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	svc := NewService(s)
	defer svc.Close()
	mr := miniredis.RunT(t)
	jump, jumpFP := sshServer(t, "pw")
	jh, jp, _ := net.SplitHostPort(jump)
	jport, _ := strconv.Atoi(jp)
	mh, mp, _ := net.SplitHostPort(mr.Addr())
	mport, _ := strconv.Atoi(mp)
	c := Connection{Name: "cache", Driver: Redis, Host: mh, Port: mport, Reach: Reach{Kind: ReachSSH, Hops: []Hop{{Host: jh, Port: jport, User: "u", Auth: AuthPassword}}}}
	r, err := svc.Test(ctx, c, Secrets{HopSecrets: []*string{ptr("pw")}}, "")
	if err != nil || r.OK || r.HostKey == nil || r.HostKey.Fingerprint != jumpFP {
		t.Fatalf("first test: %+v %v", r, err)
	}
	c.Reach.Hops[0].HostKey = r.HostKey.Fingerprint
	r, err = svc.Test(ctx, c, Secrets{HopSecrets: []*string{ptr("pw")}}, "")
	if err != nil || !r.OK || len(r.Hops) != 1 || r.Hops[0].Error != "" {
		t.Fatalf("trusted test: %+v %v", r, err)
	}
	// saved, it connects on first use and keeps the password sealed
	saved, err := s.Add(Connection{ProjectID: "p", Name: "cache", Driver: Redis, Host: mh, Port: mport, SavePassword: true, Reach: c.Reach}, Secrets{HopSecrets: []*string{ptr("pw")}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := svc.KV(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Command(ctx, CommandOpts{Line: "SET x 1"}); err != nil || !mr.Exists("x") {
		t.Fatalf("command through the saved tunnel: %v", err)
	}
	if st := svc.StatusOf(saved.ID); !st.Connected || !st.Tunnel {
		t.Errorf("status %+v", st)
	}
}
