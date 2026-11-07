package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/azamrizki/nextboard/internal/connlog"
)

// Creds are credentials uploaded through the UI (stored encrypted).
type Creds struct {
	PrivateKey string `json:"privateKey,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Password   string `json:"password,omitempty"`
	// KeyPassphrases open key files of the hub user, by the path the config gives.
	KeyPassphrases map[string]string `json:"keyPassphrases,omitempty"`
}

var (
	keepaliveEvery = 15 * time.Second
	keepaliveMiss  = 3
	// linkStatsEvery: a healthy connection reports now and then, as a baseline.
	linkStatsEvery = 30 * time.Minute
	backoff        = []time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	tmuxNameRe     = regexp.MustCompile(`^nb-[A-Za-z0-9_-]+$`)
)

// SSH is an agentless remote machine: one TCP connection, one SSH channel
// per terminal. Nothing is installed or configured on the remote host.
type SSH struct {
	hk        *hostKeyStore
	loadCreds func() (*Creds, error)
	// keepCreds stores what a person typed, once a connection proved it right.
	keepCreds func(Creds) error

	// onChange fires after any visible state change (status, probe, name).
	onChange func()
	// onConnect fires after every successful (re)connection.
	onConnect func()
	// logEvent records what happens to the link (package connlog).
	logEvent func(connlog.Event)

	mu          sync.Mutex
	cfg         Config
	client      *ssh.Client
	jumps       []*ssh.Client
	gen         int
	status      string
	errMsg      string
	pending     *errHostKeyUnknown
	mismatch    bool
	needAuth    *AuthNeed
	answers     Creds // typed in the dialog: in memory, on disk only when asked to and proved right
	keepAnswers bool
	looping     bool
	dialing     bool
	closed      bool
	wakeLoop    chan struct{}
	lastAttempt time.Time
	connectedAt time.Time
	downSince   time.Time
	attempts    int
	link        linkStats

	sftpMu sync.Mutex // serialises SFTP client creation
	sftp   sftpState  // guarded by mu

	// forward is the reverse port-forward of the hook HTTP fallback
	// (docs/API-agents.md §4.5); guarded by mu.
	hookHandler func() http.Handler
	wantForward func() bool
	fwd         forwardState
}

// linkStats adds up since the last report.
type linkStats struct {
	rttSum, rttMax time.Duration
	rttN           int64
	calls, fails   int
}

func (l *linkStats) event(e connlog.Event) connlog.Event {
	if l.rttN > 0 {
		e.RTTAvgMs = (l.rttSum / time.Duration(l.rttN)).Milliseconds()
	}
	e.RTTMaxMs, e.Calls, e.Failures = l.rttMax.Milliseconds(), l.calls, l.fails
	return e
}

// note writes an event with the machine's identity. Not with mu held.
func (m *SSH) note(e connlog.Event) {
	if m.logEvent == nil {
		return
	}
	c := m.Config()
	e.MachineID, e.Machine = c.ID, c.Name
	m.logEvent(e)
}

// NoteCall counts a tool call for the link report.
func (m *SSH) NoteCall(failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.link.calls++
	if failed {
		m.link.fails++
	}
}

// forwardState is the remote loopback listener forwarded to the hub.
type forwardState struct {
	ln       net.Listener
	gen      int
	port     int
	lastPort int // asked for first after a reconnect
	err      string
	asked    bool
}

func newSSH(cfg Config, hk *hostKeyStore, loadCreds func() (*Creds, error)) *SSH {
	cfg.Kind = "ssh"
	if cfg.PersistMode == "" {
		cfg.PersistMode = "auto"
	}
	if cfg.AuthMethod == "" {
		cfg.AuthMethod = "sshconfig"
	}
	return &SSH{
		hk: hk, loadCreds: loadCreds, cfg: cfg,
		status: StatusDisconnected, wakeLoop: make(chan struct{}, 1),
	}
}

func (m *SSH) ID() string { return m.cfg.ID }

func (m *SSH) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *SSH) update(f func(c *Config)) {
	m.mu.Lock()
	f(&m.cfg)
	m.mu.Unlock()
	m.changed()
}

func (m *SSH) changed() {
	if m.onChange != nil {
		m.onChange()
	}
}

func (m *SSH) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.client != nil
}

// EffectivePersist resolves "auto" using the probe cache.
func (m *SSH) EffectivePersist() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return effectivePersist(m.cfg)
}

func effectivePersist(c Config) string {
	switch c.PersistMode {
	case "tmux":
		return "tmux"
	case "direct":
		return "direct"
	}
	if c.Probe != nil && c.Probe.Tmux {
		return "tmux"
	}
	return "direct"
}

func (m *SSH) Info() Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.cfg
	in := Info{
		ID: c.ID, Name: c.Name, Kind: c.Kind, Host: c.Host, Port: c.Port, User: c.User,
		SSHConfigHost: c.SSHConfigHost, AuthMethod: c.AuthMethod, PersistMode: c.PersistMode,
		EffectivePersist: effectivePersist(c), Status: m.status, Error: m.errMsg,
		HostKeyMismatch: m.mismatch, Probe: c.Probe,
	}
	switch {
	case m.fwd.port > 0:
		in.HookForward = &HookForward{Port: m.fwd.port}
	case m.fwd.err != "":
		in.HookForward = &HookForward{Error: m.fwd.err}
	}
	if m.pending != nil {
		hk := m.pending.info
		in.PendingHostKey = &hk
	}
	if m.needAuth != nil {
		need := *m.needAuth
		in.NeedsAuth = &need
	}
	if in.Port == 0 && c.SSHConfigHost == "" {
		in.Port = 22
	}
	return in
}

func (m *SSH) setState(status, errMsg string) {
	m.mu.Lock()
	m.status, m.errMsg = status, errMsg
	m.mu.Unlock()
	m.changed()
}

// Connect dials synchronously. It is a no-op when already connected.
func (m *SSH) Connect(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("machine dihapus")
	}
	if m.client != nil {
		m.mu.Unlock()
		return nil
	}
	if m.dialing {
		m.mu.Unlock()
		return errors.New("sedang menyambung")
	}
	m.dialing = true
	m.needAuth = nil // found out again below, unless the server is away this time
	m.lastAttempt = time.Now()
	if m.status != StatusReconnecting {
		m.status = StatusConnecting
	}
	cfg := m.cfg
	m.mu.Unlock()
	m.changed()

	dialStart := time.Now()
	client, jumps, err := m.dial(ctx, cfg)
	dialTime := time.Since(dialStart)

	m.mu.Lock()
	m.dialing = false
	if err != nil {
		var unk *errHostKeyUnknown
		var chg *errHostKeyChanged
		var ask *errAuthNeeded
		reason := ""
		switch {
		case errors.As(err, &ask):
			m.needAuth = &ask.need
			m.status, m.errMsg = StatusAuth, ""
			if ask.need.Wrong && ask.need.Kind == AuthPassword {
				m.answers.Password = ""
			}
			reason = "auth_needed"
		case errors.As(err, &unk):
			m.pending, m.mismatch = unk, false
			m.status, m.errMsg = StatusHostKey, ""
			reason = "hostkey_unknown"
		case errors.As(err, &chg):
			m.pending, m.mismatch = nil, true
			m.status, m.errMsg = StatusError, chg.Error()
			reason = "hostkey_changed"
		default:
			if m.status != StatusReconnecting {
				m.status = StatusError
			}
			m.errMsg = err.Error()
		}
		m.attempts++
		attempt := m.attempts
		m.mu.Unlock()
		// A server that stays away retries for days: the start and a sample are enough.
		if attempt <= 5 || attempt%20 == 0 || reason != "" {
			m.note(connlog.Event{Kind: connlog.ConnectFailed, Attempt: attempt, Reason: reason, Error: err.Error(), DurationMs: dialTime.Milliseconds()})
		}
		m.changed()
		return err
	}
	if m.closed {
		m.mu.Unlock()
		client.Close()
		return errors.New("machine dihapus")
	}
	m.gen++
	gen := m.gen
	m.client, m.jumps = client, jumps
	m.pending, m.mismatch, m.needAuth = nil, false, nil
	m.status, m.errMsg = StatusConnected, ""
	needProbe := m.cfg.Probe == nil
	keep := m.keepAnswers
	answers := m.answers
	connected := connlog.Event{Kind: connlog.Connected, Attempt: m.attempts + 1, DurationMs: dialTime.Milliseconds()}
	if !m.downSince.IsZero() {
		connected.DowntimeMs = time.Since(m.downSince).Milliseconds()
	}
	m.connectedAt, m.downSince, m.attempts, m.link = time.Now(), time.Time{}, 0, linkStats{}
	m.keepAnswers = false
	m.mu.Unlock()
	m.note(connected)
	if keep && m.keepCreds != nil {
		if err := m.keepCreds(answers); err != nil {
			log.Printf("machine %s: kredensial tidak tersimpan: %v", cfg.ID, err)
		}
	}

	go m.watch(client, gen)
	m.openForward(client, gen)
	if needProbe {
		pctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, _ = m.Probe(pctx, nil)
		cancel()
	}
	m.changed()
	if m.onConnect != nil {
		go m.onConnect()
	}
	return nil
}

// AcceptHostKey records the pending host key if fingerprint matches, then
// connects.
func (m *SSH) AcceptHostKey(ctx context.Context, fingerprint string) error {
	m.mu.Lock()
	p := m.pending
	m.mu.Unlock()
	if p == nil || p.info.Fingerprint != fingerprint {
		return ErrFingerprintMismatch
	}
	if err := m.hk.accept(p.addr, p.key); err != nil {
		return err
	}
	m.mu.Lock()
	m.pending = nil
	m.status = StatusConnecting
	m.mu.Unlock()
	if err := m.Connect(ctx); err != nil {
		return err
	}
	m.EnsureLoop()
	return nil
}

// ErrFingerprintMismatch: the confirmed fingerprint is not the pending one.
var ErrFingerprintMismatch = errors.New("fingerprint tidak cocok dengan host key yang menunggu konfirmasi")

// EnsureLoop starts the background reconnect loop if it isn't running.
func (m *SSH) EnsureLoop() {
	m.mu.Lock()
	if m.looping || m.closed {
		m.mu.Unlock()
		select {
		case m.wakeLoop <- struct{}{}:
		default:
		}
		return
	}
	m.looping = true
	m.mu.Unlock()
	go m.loop()
}

// loop keeps the connection up with backoff 1,2,5,10,30s. It pauses when a
// host key needs the user (unknown or changed).
func (m *SSH) loop() {
	defer func() {
		m.mu.Lock()
		m.looping = false
		m.mu.Unlock()
	}()
	attempt := 0
	for {
		m.mu.Lock()
		// A wrong password tried again and again ends in a ban from the server.
		closed, connected, blocked := m.closed, m.client != nil, m.pending != nil || m.mismatch || m.needAuth != nil
		m.mu.Unlock()
		if closed || blocked {
			return
		}
		if !connected {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := m.Connect(ctx)
			cancel()
			if err == nil {
				attempt = 0
			} else {
				m.mu.Lock()
				if m.pending != nil || m.mismatch || m.needAuth != nil {
					m.mu.Unlock()
					return
				}
				m.status = StatusReconnecting
				m.mu.Unlock()
				m.changed()
				d := backoff[min(attempt, len(backoff)-1)]
				attempt++
				select {
				case <-time.After(d):
				case <-m.wakeLoop:
				}
				continue
			}
		}
		// Connected: wait until something wakes us (disconnect).
		<-m.wakeLoop
	}
}

// watch runs keepalives and notices when the connection dies.
func (m *SSH) watch(c *ssh.Client, gen int) {
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = c.Wait()
		close(done)
	}()
	t := time.NewTicker(keepaliveEvery)
	defer t.Stop()
	misses := 0
	silent := false
	reported := time.Now()
loop:
	for {
		select {
		case <-done:
			break loop
		case <-t.C:
			if silent { // already given up, the client is closing
				continue
			}
			sent := time.Now()
			if sendKeepalive(c, keepaliveEvery) {
				misses = 0
				m.noteRTT(time.Since(sent))
			} else if misses++; misses >= keepaliveMiss {
				silent = true
				m.note(connlog.Event{Kind: connlog.KeepaliveMiss, Misses: misses})
				c.Close()
			} else {
				m.note(connlog.Event{Kind: connlog.KeepaliveMiss, Misses: misses})
			}
			if time.Since(reported) >= linkStatsEvery {
				reported = time.Now()
				m.mu.Lock()
				e := m.link.event(connlog.Event{Kind: connlog.LinkStats, UptimeMs: time.Since(m.connectedAt).Milliseconds()})
				m.link = linkStats{}
				m.mu.Unlock()
				m.note(e)
			}
		}
	}
	m.mu.Lock()
	if m.gen != gen { // replaced by a newer connection: nothing was lost
		m.mu.Unlock()
		return
	}
	lost := m.link.event(connlog.Event{Kind: connlog.Disconnected, UptimeMs: time.Since(m.connectedAt).Milliseconds()})
	switch {
	case m.closed:
		lost.Reason = connlog.ReasonClosed
	case silent:
		lost.Reason = connlog.ReasonKeepalive
	default:
		lost.Reason = connlog.ReasonLost
		if waitErr != nil {
			lost.Error = waitErr.Error()
		}
	}
	m.mu.Unlock()
	m.note(lost)
	m.mu.Lock()
	if m.gen != gen {
		m.mu.Unlock()
		return
	}
	for _, j := range m.jumps {
		j.Close()
	}
	m.client, m.jumps = nil, nil
	m.downSince = time.Now()
	sc := m.takeSFTPLocked()
	m.dropForwardLocked()
	closed := m.closed
	if !closed {
		m.status = StatusReconnecting
		m.errMsg = "koneksi terputus"
	}
	m.mu.Unlock()
	if sc != nil {
		go sc.Close()
	}
	if closed {
		return
	}
	m.changed()
	m.EnsureLoop()
}

func (m *SSH) noteRTT(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.link.rttSum += d
	m.link.rttN++
	m.link.rttMax = max(m.link.rttMax, d)
}

func sendKeepalive(c *ssh.Client, timeout time.Duration) bool {
	ch := make(chan error, 1)
	go func() {
		_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
		ch <- err
	}()
	select {
	case err := <-ch:
		return err == nil
	case <-time.After(timeout):
		return false
	}
}

// Close disconnects permanently (machine deleted).
func (m *SSH) Close() {
	m.mu.Lock()
	m.closed = true
	c, jumps := m.client, m.jumps
	m.client, m.jumps = nil, nil
	sc := m.takeSFTPLocked()
	m.dropForwardLocked()
	m.mu.Unlock()
	if sc != nil {
		go sc.Close()
	}
	if c != nil {
		c.Close()
	}
	for _, j := range jumps {
		j.Close()
	}
	select {
	case m.wakeLoop <- struct{}{}:
	default:
	}
}

// openForward asks the server to forward a loopback port to the hook
// handler (tcpip-forward on the hub's own connection; nothing is installed
// on the remote). The previous port is requested first so tmux sessions
// started before a reconnect keep a working NB_HOOK_URL.
func (m *SSH) openForward(c *ssh.Client, gen int) {
	if m.hookHandler == nil || m.wantForward == nil || !m.wantForward() {
		m.mu.Lock()
		m.fwd.port, m.fwd.err, m.fwd.asked = 0, "", false
		m.mu.Unlock()
		return
	}
	h := m.hookHandler()
	if h == nil {
		return
	}
	m.mu.Lock()
	last := m.fwd.lastPort
	m.mu.Unlock()
	var ln net.Listener
	var err error
	if last > 0 {
		ln, err = c.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(last)))
	}
	if ln == nil {
		ln, err = c.Listen("tcp", "127.0.0.1:0")
	}
	m.mu.Lock()
	if m.gen != gen || m.client != c {
		m.mu.Unlock()
		if ln != nil {
			ln.Close()
		}
		return
	}
	m.fwd.asked = true
	if err != nil {
		m.fwd.port, m.fwd.err = 0, "reverse port-forward ditolak server: "+err.Error()
		m.mu.Unlock()
		return
	}
	port := 0
	if a, ok := ln.Addr().(*net.TCPAddr); ok {
		port = a.Port
	}
	m.fwd.ln, m.fwd.gen, m.fwd.port, m.fwd.lastPort, m.fwd.err = ln, gen, port, port, ""
	m.mu.Unlock()
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
}

// dropForwardLocked closes the forward listener (connection gone).
func (m *SSH) dropForwardLocked() {
	if m.fwd.ln != nil {
		go m.fwd.ln.Close()
	}
	m.fwd.ln, m.fwd.port = nil, 0
}

// HookURL is the NB_HOOK_URL for terminals started now ("" when the
// forward is not open).
func (m *SSH) HookURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fwd.port == 0 {
		return ""
	}
	return "http://127.0.0.1:" + strconv.Itoa(m.fwd.port) + "/nb/hook"
}

// RefreshForward opens or closes the forward after a settings change.
func (m *SSH) RefreshForward() {
	m.mu.Lock()
	c, gen := m.client, m.gen
	want := m.wantForward != nil && m.wantForward()
	open := m.fwd.ln != nil
	if !want && open {
		m.dropForwardLocked()
		m.fwd.err = ""
	}
	m.mu.Unlock()
	if want && !open && c != nil {
		m.openForward(c, gen)
	}
	m.changed()
}

// currentClient returns the live client or nil.
func (m *SSH) currentClient() *ssh.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.client
}

// ---- dialing ----

func (m *SSH) dial(ctx context.Context, cfg Config) (*ssh.Client, []*ssh.Client, error) {
	t := resolveConfig(cfg)
	var jumps []*ssh.Client
	var prev *ssh.Client
	closeAll := func() {
		for i := len(jumps) - 1; i >= 0; i-- {
			jumps[i].Close()
		}
	}
	if t.proxyJump != "" {
		for _, hop := range strings.Split(t.proxyJump, ",") {
			ht := parseJump(strings.TrimSpace(hop))
			c, err := m.dialOne(ctx, cfg, ht, prev, true)
			if err != nil {
				closeAll()
				return nil, nil, fmt.Errorf("proxy jump %s: %w", ht.alias, err)
			}
			jumps = append(jumps, c)
			prev = c
		}
	}
	c, err := m.dialOne(ctx, cfg, t, prev, false)
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	return c, jumps, nil
}

// parseJump parses "[user@]host[:port]" and resolves host via ssh_config.
func parseJump(hop string) target {
	hop = strings.TrimPrefix(hop, "ssh://")
	userPart := ""
	if i := strings.LastIndex(hop, "@"); i >= 0 {
		userPart, hop = hop[:i], hop[i+1:]
	}
	portPart := 0
	if h, p, err := net.SplitHostPort(hop); err == nil {
		hop = h
		portPart, _ = strconv.Atoi(p)
	}
	t := resolveAlias(freshSettings(), hop)
	if userPart != "" {
		t.user = userPart
	}
	if portPart != 0 {
		t.port = portPart
	}
	if t.user == "" {
		t.user = resolveConfig(Config{Host: hop}).user
	}
	return t
}

func (m *SSH) dialOne(ctx context.Context, cfg Config, t target, via *ssh.Client, isJump bool) (*ssh.Client, error) {
	auth, at, cleanup, err := m.authMethods(cfg, t, isJump)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	cb, err := m.hk.callback()
	if err != nil {
		return nil, err
	}
	var hkErr error
	addr := t.addr()
	ccfg := &ssh.ClientConfig{
		User: t.user,
		Auth: auth,
		HostKeyCallback: func(h string, r net.Addr, k ssh.PublicKey) error {
			hkErr = cb(h, r, k)
			return hkErr
		},
		HostKeyAlgorithms: m.hk.knownAlgorithms(addr),
		Timeout:           15 * time.Second,
		ClientVersion:     "SSH-2.0-NextBoard",
	}
	var conn net.Conn
	if via != nil {
		conn, err = via.DialContext(ctx, "tcp", addr)
	} else {
		d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(25 * time.Second))
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, ccfg)
	if err != nil {
		conn.Close()
		if hkErr != nil {
			return nil, hkErr
		}
		if authEnded(err) {
			if need := at.need(t); need != nil {
				return nil, &errAuthNeeded{*need}
			}
			if at.noKeys != nil {
				return nil, at.noKeys
			}
		}
		return nil, cleanSSHError(err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

func cleanSSHError(err error) error {
	s := err.Error()
	if strings.Contains(s, "unable to authenticate") {
		return fmt.Errorf("autentikasi SSH gagal (%s)", s)
	}
	return err
}

// authMethods builds the auth chain. x/crypto/ssh tries each method name
// once, so all public-key signers are merged into one callback. The chain also
// finds out what the server takes: its methods only run when the server offers
// them, and one with nothing to answer asks a person instead (attempt.need).
func (m *SSH) authMethods(cfg Config, t target, isJump bool) ([]ssh.AuthMethod, *authAttempt, func(), error) {
	var signers []ssh.Signer
	var agentConn net.Conn
	cleanup := func() {
		if agentConn != nil {
			agentConn.Close()
		}
	}
	at := &authAttempt{}
	method := cfg.AuthMethod
	if isJump {
		method = "sshconfig"
	}
	creds := Creds{}
	if cfg.CredRef != "" && m.loadCreds != nil {
		c, err := m.loadCreds()
		if err != nil {
			return nil, nil, cleanup, fmt.Errorf("gagal membuka kredensial: %w", err)
		}
		creds = *c
	}
	m.mu.Lock()
	creds = creds.with(m.answers)
	m.mu.Unlock()
	fileSigners := func() {
		files := t.identityFiles
		if len(files) == 0 {
			files = []string{"~/.ssh/id_ed25519", "~/.ssh/id_ecdsa", "~/.ssh/id_rsa"}
		}
		for _, f := range files {
			full := expandPath(f, t)
			b, err := os.ReadFile(full)
			if err != nil {
				continue
			}
			s, locked, err := openKey(b, creds.KeyPassphrases[f])
			switch {
			case err != nil:
			case locked != keyOpen:
				// the agent may hold this key: it only comes up if nothing else works
				at.locked = append(at.locked, lockedKey{path: f, file: full, wrong: locked == keyWrong})
			default:
				signers = append(signers, s)
			}
		}
	}
	agentSigners := func() {
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return
		}
		c, err := net.DialTimeout("unix", sock, 2*time.Second)
		if err != nil {
			return
		}
		agentConn = c
		if ss, err := agent.NewClient(c).Signers(); err == nil {
			signers = append(signers, ss...)
		}
	}
	switch method {
	case "key":
		if creds.PrivateKey == "" {
			return nil, nil, cleanup, errors.New("private key belum di-upload")
		}
		s, locked, err := openKey([]byte(creds.PrivateKey), creds.Passphrase)
		switch {
		case err != nil:
			return nil, nil, cleanup, fmt.Errorf("private key tidak valid: %w", err)
		case locked != keyOpen:
			at.locked = append(at.locked, lockedKey{wrong: locked == keyWrong})
		default:
			signers = append(signers, s)
		}
	case "password":
	case "agent":
		agentSigners()
		if len(signers) == 0 {
			at.noKeys = errors.New("ssh-agent tidak tersedia (SSH_AUTH_SOCK kosong) atau tidak punya key")
		}
	default: // "sshconfig"
		fileSigners()
		agentSigners()
		if creds.PrivateKey != "" {
			if s, locked, err := openKey([]byte(creds.PrivateKey), creds.Passphrase); err == nil && locked == keyOpen {
				signers = append(signers, s)
			}
		}
		if len(signers) == 0 {
			at.noKeys = errors.New("tidak ada SSH key yang bisa dipakai di ~/.ssh atau ssh-agent")
		}
	}
	methods := []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		at.takesKeys = true
		return signers, nil
	})}
	if !isJump { // the password belongs to the machine, not to a host on the way
		pw := creds.Password
		methods = append(methods,
			ssh.PasswordCallback(func() (string, error) { return at.password(pw, "") }),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				if len(qs) == 0 {
					return nil, nil
				}
				a, err := at.password(pw, qs[0])
				if err != nil {
					return nil, err
				}
				ans := make([]string, len(qs))
				for i := range ans {
					ans[i] = a
				}
				return ans, nil
			}))
	}
	return methods, at, cleanup, nil
}

// ---- remote commands ----

// posixShells are login shells whose -lc accepts our POSIX command strings.
var posixShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "mksh": true, "ash": true}

// shellWord is the shell used to run commands as a login shell.
func (m *SSH) shellWord() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.cfg.Probe; p != nil && p.Shell != "" && posixShells[path.Base(p.Shell)] {
		return Quote(p.Shell)
	}
	return "/bin/sh"
}

// loginWrap runs a POSIX script in the user's login shell (falling back to
// /bin/sh -l for non-POSIX shells like fish), so PATH from the profile
// applies. Wrapped in /bin/sh -c because sshd runs commands via the user's
// own shell.
func loginWrap(script string) string {
	q := Quote(script)
	return "/bin/sh -c " + Quote(`case "${SHELL##*/}" in sh|bash|zsh|dash|ksh|mksh|ash) exec "$SHELL" -lc `+q+`;; *) exec /bin/sh -lc `+q+`;; esac`)
}

// Exec runs a non-interactive command and returns combined output.
func (m *SSH) Exec(ctx context.Context, cmd string) (string, error) {
	c := m.currentClient()
	if c == nil {
		return "", ErrNotConnected
	}
	s, err := openSession(ctx, c)
	if err != nil {
		return "", err
	}
	defer s.Close()
	// stdout and stderr are copied by separate goroutines.
	var buf lockedBuffer
	s.Stdout = &buf
	s.Stderr = &buf
	done := make(chan error, 1)
	if err := s.Start(cmd); err != nil {
		return "", err
	}
	go func() { done <- s.Wait() }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-ctx.Done():
		s.Close()
		return buf.String(), ctx.Err()
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Probe runs the read-only probe script.
func (m *SSH) Probe(ctx context.Context, commands []string) (*ProbeResult, error) {
	if commands == nil {
		commands = []string{"claude", "codex", "gemini"}
	}
	out, err := m.Exec(ctx, loginWrap(probeScript(commands)))
	if err != nil && !strings.Contains(out, probeEnd) {
		return nil, fmt.Errorf("probe gagal: %w", err)
	}
	p := parseProbe(out, commands)
	m.mu.Lock()
	m.cfg.Probe = p
	m.mu.Unlock()
	m.changed()
	return p, nil
}

// ListDir lists a remote directory over SFTP (with sizes and mtimes), or
// with a read-only `ls -1pA` when the SFTP subsystem is unavailable.
func (m *SSH) ListDir(ctx context.Context, p string) (*DirListing, error) {
	sc, home, done, err := m.sftpClient(ctx)
	if err == nil {
		defer done()
		return listDirSFTP(sc, home, p)
	}
	if !errors.Is(err, ErrNoSFTP) {
		return nil, err
	}
	script := cdExpr(p) + " && pwd && ls -1pA"
	out, err := m.Exec(ctx, "/bin/sh -c "+Quote(script))
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "/") {
		return nil, fmt.Errorf("output ls tidak terduga")
	}
	dir := lines[0]
	l := &DirListing{Path: dir, Entries: []DirEntry{}}
	if dir != "/" {
		l.Parent = path.Dir(dir)
	}
	for _, name := range lines[1:] {
		if name == "" {
			continue
		}
		isDir := strings.HasSuffix(name, "/")
		l.Entries = append(l.Entries, DirEntry{Name: strings.TrimSuffix(name, "/"), Dir: isDir})
	}
	sortEntries(l.Entries)
	return l, nil
}

// TmuxSessions lists nb-* tmux sessions on the machine.
func (m *SSH) TmuxSessions(ctx context.Context) ([]string, error) {
	out, err := m.Exec(ctx, loginWrap(`tmux list-sessions -F '#{session_name}' 2>/dev/null || true`))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if tmuxNameRe.MatchString(l) {
			names = append(names, l)
		}
	}
	return names, nil
}

// KillTmux kills one nb-* tmux session.
func (m *SSH) KillTmux(ctx context.Context, name string) error {
	if !tmuxNameRe.MatchString(name) {
		return fmt.Errorf("nama sesi tmux tidak valid: %q", name)
	}
	out, err := m.Exec(ctx, loginWrap("tmux kill-session -t "+Quote(name)))
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// envName matches a POSIX environment variable name.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envWords quotes KEY=VALUE pairs for an inline assignment or `export`.
// Entries without a valid name are dropped.
func envWords(env []string) string {
	var b strings.Builder
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !envName.MatchString(k) {
			continue
		}
		b.WriteString(" " + k + "=" + Quote(v))
	}
	return b.String()
}

// buildCommand returns the remote command line for a terminal:
//
//	direct:  <shell> -lc 'cd <cwd> && NEXTBOARD_SESSION_ID=<id> exec <cmd> <args>'
//	wrapper: <shell> -lc 'cd <cwd> && export NEXTBOARD_SESSION_ID=<id> NB_…=… && exec /bin/sh -c "$NB_WRAP" …'
//	tmux:    <shell> -lc 'tmux new-session -A -s nb-<id> -x C -y R '\''exec <shell> -lc …'\'' \; set-option status off \; set-option -wq allow-passthrough on'
//
// allow-passthrough is set on the session's own window only (-w), quietly
// (-q) so tmux < 3.3 does not fail; global options are never touched.
func (m *SSH) buildCommand(spec StartSpec) string {
	sh := m.shellWord()
	base := "NEXTBOARD_SESSION_ID=" + Quote(spec.SessionID) + " COLORTERM=truecolor" + envWords(spec.Env)
	var inner string
	if spec.Shell != "" {
		inner = cdExpr(spec.Cwd) + " && export " + base + " && " + spec.Shell
	} else {
		var prog string
		if spec.Command == "$SHELL" || spec.Command == "" {
			prog = sh
			if len(spec.Args) > 0 {
				prog += " " + Join(spec.Args...)
			}
		} else {
			prog = Join(append([]string{spec.Command}, spec.Args...)...)
		}
		inner = cdExpr(spec.Cwd) + " && " + base + " exec " + prog
	}
	if spec.TmuxName == "" {
		return sh + " -lc " + Quote(inner)
	}
	tmuxShellCmd := "exec " + sh + " -lc " + Quote(inner)
	tmux := fmt.Sprintf("tmux new-session -A -s %s -x %d -y %d %s \\; set-option status off \\; set-option -wq allow-passthrough on",
		Quote(spec.TmuxName), spec.Cols, spec.Rows, Quote(tmuxShellCmd))
	return sh + " -lc " + Quote(tmux)
}

// TmuxHas reports whether tmux session name exists (read-only).
func (m *SSH) TmuxHas(ctx context.Context, name string) (bool, error) {
	if !tmuxNameRe.MatchString(name) {
		return false, fmt.Errorf("nama sesi tmux tidak valid: %q", name)
	}
	out, err := m.Exec(ctx, loginWrap("tmux has-session -t "+Quote("="+name)+" 2>/dev/null && echo __NB_YES__; true"))
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "__NB_YES__"), nil
}

// Foreground asks ps for the terminal's foreground process group of the
// wrapper (pid) and its command name; read-only, 3 s timeout.
func (m *SSH) Foreground(ctx context.Context, pid int) (int, string, error) {
	if pid <= 0 {
		return 0, "", errors.New("pid wrapper belum diketahui")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := m.Exec(ctx, "/bin/sh -c "+Quote(remoteForegroundScript(pid)))
	pgid, comm, ok := parseRemoteForeground(out)
	if !ok {
		if err == nil {
			err = errors.New("proses wrapper tidak ditemukan")
		}
		return 0, "", err
	}
	return pgid, comm, nil
}

// Start opens a new channel with a PTY and runs the terminal command.
func (m *SSH) Start(spec StartSpec) (Proc, error) {
	c := m.currentClient()
	if c == nil {
		return nil, ErrNotConnected
	}
	if spec.TmuxName != "" && !tmuxNameRe.MatchString(spec.TmuxName) {
		return nil, fmt.Errorf("nama tmux tidak valid")
	}
	s, err := openSession(context.Background(), c)
	if err != nil {
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := s.RequestPty("xterm-256color", int(spec.Rows), int(spec.Cols), modes); err != nil {
		s.Close()
		return nil, err
	}
	stdin, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	stdout, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	s.Stderr = io.Discard // a PTY merges stderr into stdout
	if err := s.Start(m.buildCommand(spec)); err != nil {
		s.Close()
		return nil, err
	}
	p := &sshProc{m: m, s: s, stdin: stdin, stdout: stdout, tmux: spec.TmuxName, done: make(chan struct{})}
	go p.wait()
	return p, nil
}

type sshProc struct {
	m      *SSH
	s      *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader
	tmux   string

	mu     sync.Mutex
	killed bool
	done   chan struct{}
	code   int
	err    error
}

func (p *sshProc) wait() {
	err := p.s.Wait()
	p.mu.Lock()
	killed := p.killed
	p.mu.Unlock()
	var ee *ssh.ExitError
	switch {
	case err == nil:
		p.code = 0
	case errors.As(err, &ee):
		p.code = ee.ExitStatus()
		if ee.Signal() != "" && p.code == 0 {
			p.code = 129
		}
	case killed:
		p.code = 129
	default:
		// Channel closed without exit status: the connection dropped. The
		// process may still be alive (tmux) or is gone (direct).
		p.code = -1
		p.err = ErrDisconnected
	}
	close(p.done)
}

func (p *sshProc) Read(b []byte) (int, error)  { return p.stdout.Read(b) }
func (p *sshProc) Write(b []byte) (int, error) { return p.stdin.Write(b) }
func (p *sshProc) Close() error                { return p.s.Close() }

func (p *sshProc) Resize(cols, rows uint16) error {
	return p.s.WindowChange(int(rows), int(cols))
}

// Signal: in tmux mode "HUP" kills the tmux session through exec; in
// direct mode it sends SIGHUP over the channel. "KILL" closes the channel.
func (p *sshProc) Signal(sig string) error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	if sig == "KILL" {
		return p.s.Close()
	}
	if p.tmux != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return p.m.KillTmux(ctx, p.tmux)
	}
	return p.s.Signal(ssh.SIGHUP)
}

func (p *sshProc) Wait() (int, error) {
	<-p.done
	return p.code, p.err
}

// Foreground implements Foregrounder through a read-only ps exec.
func (p *sshProc) Foreground(ctx context.Context, wrapperPid int) (int, string, error) {
	return p.m.Foreground(ctx, wrapperPid)
}
