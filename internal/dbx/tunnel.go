package dbx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyError: an SSH server's key is not known yet (trust it after checking the
// fingerprint), or it changed since it was trusted (refused).
type HostKeyError struct {
	Hop         int    `json:"hop"`
	Host        string `json:"host"`
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"keyType"`
	Changed     bool   `json:"changed"`
}

func (e *HostKeyError) Error() string {
	if e.Changed {
		return fmt.Sprintf("the host key of %s changed (%s): refused, it may not be the same server", e.Host, e.Fingerprint)
	}
	return fmt.Sprintf("%s is not a known host: check its key %s and trust it", e.Host, e.Fingerprint)
}

// HopResult is how one hop of a tunnel went in a test.
type HopResult struct {
	Host  string `json:"host"`
	MS    int64  `json:"ms"`
	Error string `json:"error,omitempty"`
}

// hopCfg is a hop with its secrets opened.
type hopCfg struct {
	Hop
	secret string
	key    string
	// identity files of an ~/.ssh/config Host
	identities []string
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// hopsFromConfig resolves a Host of This PC's ~/.ssh/config into hops: its ProxyJump chain,
// then the host itself.
func hopsFromConfig(alias string) ([]hopCfg, error) {
	hosts, err := machine.ListSSHConfigHosts()
	if err != nil {
		return nil, err
	}
	byName := map[string]machine.SSHConfigHost{}
	for _, h := range hosts {
		byName[h.Host] = h
	}
	resolve := func(spec string) (hopCfg, error) {
		if h, ok := byName[spec]; ok {
			return hopCfg{Hop: Hop{Host: orDefault(h.HostName, h.Host), Port: orPort(h.Port), User: h.User, Auth: AuthAgent}, identities: identityList(h.IdentityFile)}, nil
		}
		user, rest, ok := strings.Cut(spec, "@")
		if !ok {
			rest, user = spec, ""
		}
		host, port := rest, 22
		if hh, pp, err := net.SplitHostPort(rest); err == nil {
			host = hh
			port, _ = strconv.Atoi(pp)
		}
		if h, ok := byName[host]; ok {
			c := hopCfg{Hop: Hop{Host: orDefault(h.HostName, h.Host), Port: orPort(h.Port), User: orDefault(user, h.User), Auth: AuthAgent}, identities: identityList(h.IdentityFile)}
			return c, nil
		}
		if user == "" {
			user = os.Getenv("USER")
		}
		return hopCfg{Hop: Hop{Host: host, Port: port, User: user, Auth: AuthAgent}, identities: defaultIdentities()}, nil
	}
	h, ok := byName[alias]
	if !ok {
		return nil, invalid("no Host %q in ~/.ssh/config", alias)
	}
	var out []hopCfg
	if h.ProxyJump != "" && !strings.EqualFold(h.ProxyJump, "none") {
		for _, j := range strings.Split(h.ProxyJump, ",") {
			c, err := resolve(strings.TrimSpace(j))
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
	}
	last, _ := resolve(alias)
	if last.User == "" {
		last.User = os.Getenv("USER")
	}
	return append(out, last), nil
}

func orPort(p int) int {
	if p == 0 {
		return 22
	}
	return p
}

func identityList(f string) []string {
	if f == "" {
		return defaultIdentities()
	}
	return []string{f}
}

func defaultIdentities() []string {
	return []string{"~/.ssh/id_ed25519", "~/.ssh/id_ecdsa", "~/.ssh/id_rsa"}
}

func authFor(h hopCfg) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	cleanup := func() {}
	var signers []ssh.Signer
	addKey := func(b []byte, pass string) error {
		var s ssh.Signer
		var err error
		if pass != "" {
			s, err = ssh.ParsePrivateKeyWithPassphrase(b, []byte(pass))
		} else {
			s, err = ssh.ParsePrivateKey(b)
		}
		if err != nil {
			var miss *ssh.PassphraseMissingError
			if errors.As(err, &miss) {
				return errors.New("the private key needs its passphrase")
			}
			return err
		}
		signers = append(signers, s)
		return nil
	}
	switch h.Auth {
	case AuthPassword:
		pw := h.secret
		methods = append(methods, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			out := make([]string, len(qs))
			for i := range out {
				out[i] = pw
			}
			return out, nil
		}))
		return methods, cleanup, nil
	case AuthKey:
		switch {
		case h.key != "":
			if err := addKey([]byte(h.key), h.secret); err != nil {
				return nil, cleanup, err
			}
		case h.KeyPath != "":
			b, err := os.ReadFile(expandHome(h.KeyPath))
			if err != nil {
				return nil, cleanup, fmt.Errorf("private key %s: %w", h.KeyPath, err)
			}
			if err := addKey(b, h.secret); err != nil {
				return nil, cleanup, err
			}
		default:
			return nil, cleanup, errors.New("no private key: choose one on This PC or upload it")
		}
	}
	// the SSH agent, and the identity files of an ~/.ssh/config Host
	for _, f := range h.identities {
		if b, err := os.ReadFile(expandHome(f)); err == nil {
			_ = addKey(b, "")
		}
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" && (h.Auth == AuthAgent || len(signers) == 0) {
		if c, err := net.Dial("unix", sock); err == nil {
			ag := agent.NewClient(c)
			if list, err := ag.Signers(); err == nil {
				signers = append(signers, list...)
			}
			cleanup = func() { c.Close() }
		}
	}
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if len(methods) == 0 {
		return nil, cleanup, errors.New("no way to sign in: no key and no SSH agent")
	}
	return methods, cleanup, nil
}

// hostKeyCheck accepts a key ~/.ssh/known_hosts knows, or the hop's trusted fingerprint.
func hostKeyCheck(i int, h hopCfg) ssh.HostKeyCallback {
	var files []string
	if home, err := os.UserHomeDir(); err == nil {
		for _, f := range []string{"known_hosts", "known_hosts2"} {
			p := filepath.Join(home, ".ssh", f)
			if _, err := os.Stat(p); err == nil {
				files = append(files, p)
			}
		}
	}
	var known ssh.HostKeyCallback
	if len(files) > 0 {
		known, _ = knownhosts.New(files...)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		if h.HostKey != "" {
			if h.HostKey == fp {
				return nil
			}
			return &HostKeyError{Hop: i, Host: h.Host, Fingerprint: fp, KeyType: key.Type(), Changed: true}
		}
		if known != nil {
			err := known(hostname, remote, key)
			if err == nil {
				return nil
			}
			var ke *knownhosts.KeyError
			if errors.As(err, &ke) && len(ke.Want) > 0 {
				return &HostKeyError{Hop: i, Host: h.Host, Fingerprint: fp, KeyType: key.Type(), Changed: true}
			}
		}
		return &HostKeyError{Hop: i, Host: h.Host, Fingerprint: fp, KeyType: key.Type()}
	}
}

// tunnel is an SSH chain to the last hop, and the database dialled from there.
type tunnel struct {
	clients []*ssh.Client
	remote  string
	ln      net.Listener
	used    atomic.Int64
	done    chan struct{}
	once    sync.Once
	err     atomic.Value // error that ended it
}

func (t *tunnel) last() *ssh.Client { return t.clients[len(t.clients)-1] }

func (t *tunnel) touch() { t.used.Store(time.Now().UnixNano()) }

func (t *tunnel) idle() time.Duration { return time.Since(time.Unix(0, t.used.Load())) }

func (t *tunnel) alive() bool {
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

func (t *tunnel) close(why error) {
	t.once.Do(func() {
		if why != nil {
			t.err.Store(why)
		}
		close(t.done)
		if t.ln != nil {
			t.ln.Close()
		}
		for i := len(t.clients) - 1; i >= 0; i-- {
			t.clients[i].Close()
		}
	})
}

// dial opens a connection to the database through the tunnel.
func (t *tunnel) dial(ctx context.Context, _ string, addr string) (net.Conn, error) {
	if !t.alive() {
		return nil, errors.New("the SSH tunnel is down")
	}
	t.touch()
	c, err := t.last().DialContext(ctx, "tcp", addr)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "administratively prohibited") {
			return nil, errors.New("the SSH server refuses port forwarding (AllowTcpForwarding no)")
		}
		return nil, err
	}
	return deadlined(c), nil
}

// openTunnel dials every hop in turn; results says how each went.
func openTunnel(ctx context.Context, hops []hopCfg, remote string, timeout, keepAlive time.Duration) (*tunnel, []HopResult, error) {
	t := &tunnel{remote: remote, done: make(chan struct{})}
	var results []HopResult
	var prev *ssh.Client
	for i, h := range hops {
		start := time.Now()
		res := HopResult{Host: h.Host}
		auth, cleanup, err := authFor(h)
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			t.close(nil)
			return nil, results, fmt.Errorf("%s: %w", h.Host, err)
		}
		cfg := &ssh.ClientConfig{User: h.User, Auth: auth, HostKeyCallback: hostKeyCheck(i, h), Timeout: timeout, ClientVersion: "SSH-2.0-NextBoard"}
		addr := h.addr()
		var conn net.Conn
		dctx, cancel := context.WithTimeout(ctx, timeout)
		if prev != nil {
			conn, err = prev.DialContext(dctx, "tcp", addr)
		} else {
			d := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
			conn, err = d.DialContext(dctx, "tcp", addr)
		}
		cancel()
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(timeout + 10*time.Second))
			var c ssh.Conn
			var chans <-chan ssh.NewChannel
			var reqs <-chan *ssh.Request
			c, chans, reqs, err = ssh.NewClientConn(conn, addr, cfg)
			if err == nil {
				_ = conn.SetDeadline(time.Time{})
				prev = ssh.NewClient(c, chans, reqs)
				t.clients = append(t.clients, prev)
			} else {
				conn.Close()
			}
		}
		cleanup()
		res.MS = time.Since(start).Milliseconds()
		if err != nil {
			var hk *HostKeyError
			if !errors.As(err, &hk) {
				err = fmt.Errorf("%s: %w", h.Host, err)
			}
			res.Error = err.Error()
			results = append(results, res)
			t.close(nil)
			return nil, results, err
		}
		results = append(results, res)
	}
	t.touch()
	if keepAlive > 0 {
		go t.keepAlive(keepAlive)
	}
	return t, results, nil
}

func (t *tunnel) keepAlive(every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
			errc := make(chan error, 1)
			go func() {
				_, _, err := t.last().SendRequest("keepalive@openssh.com", true, nil)
				errc <- err
			}()
			select {
			case err := <-errc:
				if err != nil {
					t.close(fmt.Errorf("the SSH tunnel dropped: %w", err))
					return
				}
			case <-time.After(every):
				t.close(errors.New("the SSH tunnel stopped answering"))
				return
			}
		}
	}
}

// listen opens 127.0.0.1:port on This PC, forwarded to the database, for agents and tools.
func (t *tunnel) listen(port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("127.0.0.1:%d: %w", port, err)
	}
	t.ln = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				r, err := t.dial(ctx, "tcp", t.remote)
				cancel()
				if err != nil {
					return
				}
				defer r.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(r, c); done <- struct{}{} }()
				go func() { _, _ = io.Copy(c, r); done <- struct{}{} }()
				<-done
				t.touch()
			}()
		}
	}()
	return nil
}

// deadlined gives a connection that takes deadlines: an SSH channel does not ("deadline not
// supported"), and every database driver sets them. It is piped through net.Pipe, which does.
func deadlined(c net.Conn) net.Conn {
	if c.SetDeadline(time.Time{}) == nil {
		return c
	}
	a, b := net.Pipe()
	go func() { _, _ = io.Copy(c, b); c.Close(); b.Close() }()
	go func() { _, _ = io.Copy(b, c); b.Close(); c.Close() }()
	return a
}
