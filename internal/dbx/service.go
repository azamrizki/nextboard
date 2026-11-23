package dbx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// idle times: a pool closes after this long unused; a tunnel that opens a port for tools stays
// longer, as tools use it without the hub knowing.
const (
	idleClose      = 10 * time.Minute
	idleTunnelPort = 30 * time.Minute
	sessionIdle    = 30 * time.Minute
)

// ErrPasswordNeeded: the connection does not keep its password and none was given since the
// hub started.
var ErrPasswordNeeded = errors.New("password_needed")

type live struct {
	c   Connection
	tun *tunnel
	sql *sqlEngine
	kv  *kvEngine
	at  time.Time
	ver string
	ms  int64
}

func (l *live) close() {
	if l.sql != nil {
		l.sql.close()
	}
	if l.kv != nil {
		l.kv.close()
	}
	if l.tun != nil {
		l.tun.close(nil)
	}
}

func (l *live) used() time.Time {
	t := l.at
	if l.tun != nil {
		if u := time.Unix(0, l.tun.used.Load()); u.After(t) {
			t = u
		}
	}
	return t
}

// Service holds the open connections of every project.
type Service struct {
	Store *Store
	// Machine gives a machine of the hub by id, for connections reached through one.
	Machine func(id string) (machine.Host, bool)

	mu        sync.Mutex
	live      map[string]*live
	opening   map[string]*sync.Mutex
	passwords map[string]string
	stop      chan struct{}
	once      sync.Once
}

// NewService makes the service of a store.
func NewService(st *Store) *Service {
	return &Service{Store: st, live: map[string]*live{}, opening: map[string]*sync.Mutex{}, passwords: map[string]string{}, stop: make(chan struct{})}
}

// Start reaps idle connections.
func (s *Service) Start() {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.reap()
			}
		}
	}()
}

// Close closes every connection.
func (s *Service) Close() {
	s.once.Do(func() { close(s.stop) })
	s.mu.Lock()
	for id, l := range s.live {
		l.close()
		delete(s.live, id)
	}
	s.mu.Unlock()
}

func (s *Service) reap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, l := range s.live {
		idle := idleClose
		if l.tun != nil && l.tun.ln != nil {
			idle = idleTunnelPort
		}
		if l.tun != nil && !l.tun.alive() || time.Since(l.used()) > idle {
			l.close()
			delete(s.live, id)
			continue
		}
		if l.sql != nil {
			l.sql.reap(sessionIdle)
		}
	}
}

// Drop closes a connection (its settings changed, or it was removed).
func (s *Service) Drop(id string) {
	s.mu.Lock()
	l := s.live[id]
	delete(s.live, id)
	delete(s.passwords, id)
	s.mu.Unlock()
	if l != nil {
		l.close()
	}
}

// SetPassword keeps a password in memory for a connection that does not save it.
func (s *Service) SetPassword(id, pw string) {
	s.mu.Lock()
	s.passwords[id] = pw
	s.mu.Unlock()
	s.Drop2(id)
}

// Drop2 closes the live connection but keeps the password given in memory.
func (s *Service) Drop2(id string) {
	s.mu.Lock()
	l := s.live[id]
	delete(s.live, id)
	s.mu.Unlock()
	if l != nil {
		l.close()
	}
}

func (s *Service) lockOpening(id string) func() {
	s.mu.Lock()
	m := s.opening[id]
	if m == nil {
		m = &sync.Mutex{}
		s.opening[id] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// dialer builds how the hub reaches a connection's database, opening its tunnel if it has one.
func (s *Service) dialer(ctx context.Context, c Connection, sec opened) (DialFunc, *tunnel, []HopResult, error) {
	timeout := time.Duration(c.Reach.TimeoutSec) * time.Second
	switch c.Reach.Kind {
	case ReachMachine:
		if s.Machine == nil {
			return nil, nil, nil, errors.New("no machines")
		}
		h, ok := s.Machine(c.Reach.MachineID)
		if !ok {
			return nil, nil, nil, fmt.Errorf("the machine %s is not there anymore", c.Reach.MachineID)
		}
		return func(ctx context.Context, _, addr string) (net.Conn, error) {
			conn, err := machine.Dial(ctx, h, addr)
			if errors.Is(err, machine.ErrForwardingDisabled) {
				return nil, errors.New("the machine's SSH server refuses port forwarding (AllowTcpForwarding no)")
			}
			if errors.Is(err, machine.ErrNotConnected) {
				return nil, errors.New("the machine is not connected: connect it in Machines first")
			}
			if err != nil {
				return nil, err
			}
			return deadlined(conn), nil
		}, nil, nil, nil
	case ReachSSH:
		var hops []hopCfg
		if c.Reach.ConfigHost != "" {
			var err error
			if hops, err = hopsFromConfig(c.Reach.ConfigHost); err != nil {
				return nil, nil, nil, err
			}
		} else {
			for i, h := range c.Reach.Hops {
				hc := hopCfg{Hop: h}
				if i < len(sec.HopSecrets) {
					hc.secret = sec.HopSecrets[i]
				}
				if i < len(sec.HopKeys) {
					hc.key = sec.HopKeys[i]
				}
				hops = append(hops, hc)
			}
		}
		remote := net.JoinHostPort(orDefault(c.Reach.RemoteHost, c.Host), strconv.Itoa(orInt(c.Reach.RemotePort, c.Port)))
		t, res, err := openTunnel(ctx, hops, remote, timeout, time.Duration(c.Reach.KeepAliveSec)*time.Second)
		if err != nil {
			return nil, nil, res, err
		}
		return func(ctx context.Context, n, _ string) (net.Conn, error) { return t.dial(ctx, n, remote) }, t, res, nil
	}
	d := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	return d.DialContext, nil, nil, nil
}

func orInt(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

func (s *Service) openLive(ctx context.Context, c Connection, sec opened) (*live, []HopResult, error) {
	start := time.Now()
	dial, tun, hops, err := s.dialer(ctx, c, sec)
	if err != nil {
		return nil, hops, err
	}
	l := &live{c: c, tun: tun, at: time.Now()}
	if c.Driver.IsSQL() {
		e, err := newSQLEngine(ctx, c, sec.Password, dial, s.Store)
		if err != nil {
			l.close()
			return nil, hops, err
		}
		l.sql, l.ver = e, e.ver
	} else {
		e, err := newKVEngine(ctx, c, sec.Password, dial)
		if err != nil {
			l.close()
			return nil, hops, err
		}
		l.kv, l.ver = e, e.ver
	}
	l.ms = time.Since(start).Milliseconds()
	return l, hops, nil
}

func (s *Service) get(ctx context.Context, id string) (*live, error) {
	c, ok := s.Store.Get(id)
	if !ok {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	l := s.live[id]
	s.mu.Unlock()
	if l != nil && l.c.UpdatedAt.Equal(c.UpdatedAt) && (l.tun == nil || l.tun.alive()) {
		l.at = time.Now()
		return l, nil
	}
	unlock := s.lockOpening(id)
	defer unlock()
	s.mu.Lock()
	l = s.live[id]
	s.mu.Unlock()
	if l != nil && l.c.UpdatedAt.Equal(c.UpdatedAt) && (l.tun == nil || l.tun.alive()) {
		return l, nil
	}
	if l != nil {
		l.close()
	}
	sec, err := s.Store.secrets(id)
	if err != nil {
		return nil, err
	}
	if !c.SavePassword && c.User != "" {
		s.mu.Lock()
		pw, ok := s.passwords[id]
		s.mu.Unlock()
		if !ok {
			return nil, ErrPasswordNeeded
		}
		sec.Password = pw
	}
	l, _, err = s.openLive(ctx, c, sec)
	if err != nil {
		return nil, err
	}
	if c.Reach.Kind == ReachSSH && c.Reach.OpenLocal && l.tun != nil {
		if err := l.tun.listen(c.Reach.LocalPort); err != nil {
			l.close()
			return nil, err
		}
	}
	s.mu.Lock()
	s.live[id] = l
	s.mu.Unlock()
	return l, nil
}

// SQL gives the open SQL engine of a connection.
func (s *Service) SQL(ctx context.Context, id string) (*sqlEngine, error) {
	l, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if l.sql == nil {
		return nil, invalid("not an SQL connection")
	}
	return l.sql, nil
}

// KV gives the open key-value engine of a connection.
func (s *Service) KV(ctx context.Context, id string) (*kvEngine, error) {
	l, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if l.kv == nil {
		return nil, invalid("not a key-value connection")
	}
	return l.kv, nil
}

// Status is what the hub knows of a connection right now.
type Status struct {
	Connected bool   `json:"connected"`
	Version   string `json:"version,omitempty"`
	Since     string `json:"since,omitempty"`
	Tunnel    bool   `json:"tunnel"`
	LocalPort int    `json:"localPort,omitempty"`
	// PasswordNeeded: the password is asked, as it is not saved
	PasswordNeeded bool   `json:"passwordNeeded,omitempty"`
	Error          string `json:"error,omitempty"`
}

// StatusOf says whether a connection is open, without opening it.
func (s *Service) StatusOf(id string) Status {
	c, ok := s.Store.Get(id)
	if !ok {
		return Status{}
	}
	s.mu.Lock()
	l := s.live[id]
	_, havePW := s.passwords[id]
	s.mu.Unlock()
	st := Status{PasswordNeeded: !c.SavePassword && c.User != "" && !havePW}
	if l == nil {
		return st
	}
	st.Connected, st.Version, st.Since = true, l.ver, l.at.UTC().Format(time.RFC3339)
	if l.tun != nil {
		st.Tunnel = true
		st.Connected = l.tun.alive()
		if l.tun.ln != nil {
			st.LocalPort = c.Reach.LocalPort
		}
		if err, ok := l.tun.err.Load().(error); ok && err != nil {
			st.Error = err.Error()
		}
	}
	return st
}

// TestResult is how a test of a connection's settings went.
type TestResult struct {
	OK      bool          `json:"ok"`
	Version string        `json:"version,omitempty"`
	MS      int64         `json:"ms"`
	Hops    []HopResult   `json:"hops,omitempty"`
	Tables  int           `json:"tables"`
	Keys    int64         `json:"keys"`
	Error   string        `json:"error,omitempty"`
	HostKey *HostKeyError `json:"hostKey,omitempty"`
}

// Test opens a connection with unsaved settings (secrets from sec, else those kept for id),
// says how it went and closes it.
func (s *Service) Test(ctx context.Context, c Connection, sec Secrets, id string) (*TestResult, error) {
	if err := normalize(&c); err != nil {
		return nil, err
	}
	var o opened
	if id != "" {
		kept, err := s.Store.secrets(id)
		if err == nil {
			o = kept
		}
		s.mu.Lock()
		if pw, ok := s.passwords[id]; ok && o.Password == "" {
			o.Password = pw
		}
		s.mu.Unlock()
	}
	if sec.Password != nil {
		o.Password = *sec.Password
	}
	for len(o.HopSecrets) < len(c.Reach.Hops) {
		o.HopSecrets = append(o.HopSecrets, "")
	}
	for len(o.HopKeys) < len(c.Reach.Hops) {
		o.HopKeys = append(o.HopKeys, "")
	}
	o.HopSecrets, o.HopKeys = o.HopSecrets[:len(c.Reach.Hops)], o.HopKeys[:len(c.Reach.Hops)]
	for i := range c.Reach.Hops {
		if i < len(sec.HopSecrets) && sec.HopSecrets[i] != nil {
			o.HopSecrets[i] = *sec.HopSecrets[i]
		}
		if i < len(sec.HopKeys) && sec.HopKeys[i] != nil {
			o.HopKeys[i] = *sec.HopKeys[i]
		}
	}
	c.ID = ""
	start := time.Now()
	l, hops, err := s.openLive(ctx, c, o)
	r := &TestResult{Hops: hops, MS: time.Since(start).Milliseconds()}
	if err != nil {
		var hk *HostKeyError
		if errors.As(err, &hk) {
			r.HostKey = hk
		}
		r.Error = Friendly(err)
		return r, nil
	}
	defer l.close()
	r.OK, r.Version = true, l.ver
	if l.sql != nil {
		if sc, err := l.sql.Schema(ctx, ""); err == nil {
			r.Tables = len(sc.Tables)
		}
	}
	if l.kv != nil {
		r.Keys, _ = l.kv.rc.DBSize(ctx).Result()
	}
	return r, nil
}

// Friendly turns driver errors into what to do about them.
func Friendly(err error) string {
	m := err.Error()
	low := strings.ToLower(m)
	switch {
	case strings.Contains(low, "connection refused"):
		return m + ": nothing listens there. Is the server running, and is the host and port as the hub (or the SSH host) sees it?"
	case strings.Contains(low, "i/o timeout") || strings.Contains(low, "deadline exceeded"):
		return m + ": no answer. A firewall, a wrong address, or the database only listens on the server itself: use the SSH tab"
	case strings.Contains(low, "access denied") || strings.Contains(low, "password authentication failed") || strings.Contains(low, "wrongpass") || strings.Contains(low, "noauth"):
		return m + ": check the user name and the password"
	case strings.Contains(low, "unable to authenticate"):
		return m + ": the SSH server refused the key or the password"
	case strings.Contains(low, "no such host"):
		return m + ": the host name is not known from here; behind a tunnel, it is looked up by the SSH host"
	}
	return m
}

// ---- masking for agents --------------------------------------------------------------------------

// Masked says which result columns an agent must not see, by the connection's patterns
// ("*password*", "email", "users.email": a table.column pattern masks that column wherever it shows).
func Masked(patterns []string, cols []ResultColumn) []bool {
	out := make([]bool, len(cols))
	for i, c := range cols {
		name := strings.ToLower(c.Name)
		for _, p := range patterns {
			p = strings.ToLower(strings.TrimSpace(p))
			if p == "" {
				continue
			}
			if j := strings.LastIndexByte(p, '.'); j >= 0 && !strings.ContainsAny(p[:j], "*?") {
				p = p[j+1:]
			}
			if ok, _ := path.Match(p, name); ok {
				out[i] = true
				break
			}
		}
	}
	return out
}

// MaskRows replaces the masked cells of rows with •••.
func MaskRows(mask []bool, rows [][]any) {
	for _, r := range rows {
		for i := range r {
			if i < len(mask) && mask[i] && r[i] != nil {
				r[i] = "•••"
			}
		}
	}
}
