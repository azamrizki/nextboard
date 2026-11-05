// Package preview is the web-tile preview proxy (docs/API-studio.md §4,
// B17–B19): one HTTP listener per preview on its own port (same bind
// address as the hub; a reverse proxy adds TLS), one-time launch tokens that
// become a cookie scoped to that listener, and a reverse proxy to a
// loopback port of the target machine (local dial or SSH direct-tcpip) or
// to a site on the tailnet (tailnet.go) that strips frame-blocking headers
// and injects a small bridge script into HTML. Previews live in memory only.
package preview

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
)

// Limits and timeouts (docs/API-studio.md §4, §15).
var (
	MaxListeners = 20
	TokenTTL     = 60 * time.Second
	CookieTTL    = 12 * time.Hour
	IdleClose    = 30 * time.Minute
	DialTimeout  = 5 * time.Second
	// probeTimeout and maxSiteRedirects bound the first look at a public site.
	probeTimeout     = 12 * time.Second
	maxSiteRedirects = 6
	HeaderTimeout    = 60 * time.Second
	ProbeTimeout     = 800 * time.Millisecond
	ReapEvery        = time.Minute
)

const (
	maxRequestBody = 100 << 20
	maxLogs        = 500
)

// States of a preview.
const (
	StateWaiting = "waiting"
	StateLive    = "live"
	StateError   = "error"
)

// Preview is the API view (docs/API-studio.md §4.2).
type Preview struct {
	ID           string    `json:"id"`
	MachineID    string    `json:"machineId"`
	TargetHost   string    `json:"targetHost,omitempty"`
	TargetPort   int       `json:"targetPort"`
	TargetScheme string    `json:"targetScheme"`
	Path         string    `json:"path"`
	ListenPort   int       `json:"listenPort"`
	SessionID    string    `json:"sessionId,omitempty"`
	Auto         bool      `json:"auto"`
	State        string    `json:"state"`
	Error        string    `json:"error"`
	CreatedAt    time.Time `json:"createdAt"`
	LastUsedAt   time.Time `json:"lastUsedAt"`
}

// Error is a failure with an HTTP status and a stable code.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func newErr(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Msg: fmt.Sprintf(format, a...)}
}

// ErrNotFound: unknown preview id.
var ErrNotFound = &Error{Status: http.StatusNotFound, Code: "preview_not_found", Msg: "preview tidak ditemukan"}

// Config wires the manager to the hub.
type Config struct {
	// BindHost is the host part of the hub's listen address ("" or
	// "0.0.0.0" = all interfaces).
	BindHost string
	// PortMin..PortMax is settings.previews.portRange.
	PortMin, PortMax int
	// LocalReserved reports ports of This PC that must never be a target
	// (the hub's own port, the hook listener, …). The preview range is
	// always reserved.
	LocalReserved func(port int) bool
	// LocalID is the id of This PC.
	LocalID string
	// Dial opens a connection to a loopback port of a machine.
	Dial func(ctx context.Context, machineID string, port int) (net.Conn, error)
	// Resolve, DialAddr and LocalAddr reach sites on the tailnet from This PC; empty = the
	// system's resolver, a plain dial and this PC's interfaces.
	Resolve   func(ctx context.Context, host string) ([]netip.Addr, error)
	DialAddr  func(ctx context.Context, addr netip.AddrPort) (net.Conn, error)
	LocalAddr func(addr netip.Addr) bool
	// HubOrigins lists the hub origins allowed to talk to the bridge, for
	// a request on host (hostname[:port]) of a preview listener.
	HubOrigins func(scheme, host string) []string
	// Publish sends /api/events messages.
	Publish func(v any)
	// Referenced reports whether a board web node points at a preview.
	Referenced func(id string) bool
}

// Manager owns all previews.
type Manager struct {
	cfg Config

	mu     sync.Mutex
	byID   map[string]*preview
	closed bool
	stop   chan struct{}
	wg     sync.WaitGroup
	now    func() time.Time
}

// New returns a manager; Start begins the idle reaper.
func New(cfg Config) *Manager {
	if cfg.PortMin <= 0 || cfg.PortMax < cfg.PortMin {
		cfg.PortMin, cfg.PortMax = 12320, 12339
	}
	if cfg.Resolve == nil {
		cfg.Resolve = resolveHost
	}
	if cfg.DialAddr == nil {
		cfg.DialAddr = dialAddr
	}
	if cfg.LocalAddr == nil {
		cfg.LocalAddr = isLocalAddr
	}
	return &Manager{cfg: cfg, byID: map[string]*preview{}, stop: make(chan struct{}), now: time.Now}
}

// preview is the live record of one preview.
type preview struct {
	m *Manager
	Preview
	site    *site // nil for a port of a machine
	ln      net.Listener
	srv     *http.Server
	tr      *http.Transport
	tokens  map[string]token
	cookies map[string]time.Time // value → expiry
	logs    logRing
	beacon  rateLimit
	pubLogs []LogEntry
	pubT    *time.Timer
	lastPub time.Time
}

type token struct {
	exp time.Time
	to  string
}

// CookieName is the per-listener cookie. Cookies are scoped by host, not
// port, so every preview needs its own name. Plain HTTP has its own too: a
// browser refuses to let an http:// page overwrite a Secure cookie.
func CookieName(listenPort int, scheme string) string {
	if scheme == "https" {
		return "nb_pv_" + strconv.Itoa(listenPort)
	}
	return "nb_pv_http_" + strconv.Itoa(listenPort)
}

// Start runs the idle reaper.
func (m *Manager) Start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(ReapEvery)
		defer t.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
				m.Reap()
			}
		}
	}()
}

// Close shuts every listener.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.stop)
	all := m.byID
	m.byID = map[string]*preview{}
	m.mu.Unlock()
	for _, p := range all {
		p.shutdown()
	}
	m.wg.Wait()
}

// InRange reports whether port belongs to the preview listener range.
func (m *Manager) InRange(port int) bool { return port >= m.cfg.PortMin && port <= m.cfg.PortMax }

// PortRange returns the listener range.
func (m *Manager) PortRange() (int, int) { return m.cfg.PortMin, m.cfg.PortMax }

// CreateRequest is POST /api/previews.
type CreateRequest struct {
	MachineID string `json:"machineId"`
	Port      int    `json:"port"`
	Scheme    string `json:"scheme"`
	Path      string `json:"path"`
	// URL is a site on the tailnet or the internet, instead of a port of a machine (§4.9).
	URL       string `json:"url"`
	SessionID string `json:"sessionId"`
	Auto      bool   `json:"-"`
	// UserAgent is the browser's: a site judges the first look at it by this too.
	UserAgent string `json:"-"`
}

// cleanTarget validates a preview path ("/" default).
func cleanTarget(p string) (string, error) {
	if p == "" {
		return "/", nil
	}
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || len(p) > 2048 || strings.ContainsAny(p, "\x00\r\n\t ") {
		return "", newErr(http.StatusBadRequest, "bad_path", "path preview harus diawali / (tanpa host)")
	}
	if _, err := url.ParseRequestURI(p); err != nil {
		return "", newErr(http.StatusBadRequest, "bad_path", "path preview tidak valid")
	}
	return p, nil
}

// CheckPort validates a target port for a machine.
// publicSite reports a site reached over the internet, not over the tailnet.
func (m *Manager) publicSite(host string, port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), DialTimeout)
	defer cancel()
	addrs, err := m.siteAddrs(ctx, host, port)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if !IsPublic(a) {
			return false
		}
	}
	return true
}

// siteListener reports a port that serves a site.
func (m *Manager) siteListener(port int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.byID {
		if p.ListenPort == port {
			return p.site != nil
		}
	}
	return false
}

func (m *Manager) CheckPort(machineID string, port int) error {
	if port < 1 || port > 65535 {
		return newErr(http.StatusBadRequest, "bad_port", "port harus 1–65535")
	}
	if machineID == m.cfg.LocalID {
		if m.InRange(port) || (m.cfg.LocalReserved != nil && m.cfg.LocalReserved(port)) {
			return newErr(http.StatusBadRequest, "bad_port", "port %d dipakai hub NextBoard", port)
		}
	}
	return nil
}

// checkSite makes sure a site is an address the proxy may reach and answers, before any listener
// opens for it.
func (m *Manager) checkSite(host string, port int) error {
	ctx, cancel := context.WithTimeout(context.Background(), DialTimeout)
	defer cancel()
	c, err := m.dialSite(ctx, host, port)
	switch {
	case err == nil:
		c.Close()
		return nil
	case errors.Is(err, errNoHost):
		return newErr(http.StatusBadRequest, "no_such_host", "%s tidak ditemukan", host)
	case errors.Is(err, errPrivate):
		return newErr(http.StatusBadRequest, "private_address", "%s mengarah ke alamat lokal", host)
	case errors.Is(err, errOwnPort):
		return newErr(http.StatusBadRequest, "bad_port", "port %d dipakai hub NextBoard", port)
	}
	return newErr(http.StatusBadGateway, "site_unreachable", "%s tidak terjangkau: %v", host, err)
}

// Create opens a preview, or returns the existing one of (machine, host, port)
// with a fresh launch token (created=false).
func (m *Manager) Create(req CreateRequest) (Preview, string, bool, error) {
	if req.MachineID == "" {
		req.MachineID = m.cfg.LocalID
	}
	var targetHost string
	if req.URL != "" {
		scheme, host, port, path, perr := parseSiteURL(req.URL)
		if perr != nil {
			return Preview{}, "", false, perr
		}
		if err := m.checkSite(host, port); err != nil {
			return Preview{}, "", false, err
		}
		at := siteAddr{scheme, host, port, path}
		if m.publicSite(host, port) {
			var err error
			if at, err = m.probeSite(req.UserAgent, at); err != nil {
				return Preview{}, "", false, err
			}
		}
		// the hub dials a site itself, whatever machine the project is on
		req.MachineID, req.Scheme, req.Port, req.Path, targetHost = m.cfg.LocalID, at.scheme, at.port, at.path, at.host
	} else if err := m.CheckPort(req.MachineID, req.Port); err != nil {
		return Preview{}, "", false, err
	}
	switch req.Scheme {
	case "":
		req.Scheme = "http"
	case "http", "https":
	default:
		return Preview{}, "", false, newErr(http.StatusBadRequest, "bad_scheme", `scheme harus "http" atau "https"`)
	}
	path, err := cleanTarget(req.Path)
	if err != nil {
		return Preview{}, "", false, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Preview{}, "", false, newErr(http.StatusServiceUnavailable, "closed", "hub sedang dimatikan")
	}
	for _, p := range m.byID {
		if p.MachineID == req.MachineID && p.TargetPort == req.Port && p.TargetHost == targetHost {
			p.TargetScheme = req.Scheme
			if req.SessionID != "" && p.SessionID == "" {
				p.SessionID = req.SessionID
			}
			p.LastUsedAt = m.now().UTC()
			lp := p.newTokenLocked(path)
			v := p.Preview
			m.mu.Unlock()
			return v, lp, false, nil
		}
	}
	if len(m.byID) >= MaxListeners {
		m.mu.Unlock()
		return Preview{}, "", false, newErr(http.StatusServiceUnavailable, "no_preview_port", "semua port preview terpakai (maks %d)", MaxListeners)
	}
	used := map[int]bool{}
	for _, p := range m.byID {
		used[p.ListenPort] = true
	}
	var ln net.Listener
	port := 0
	// A site tends to get the same port every time: its origin, and with it what it keeps in
	// the browser, stays when the hub restarts. The bridge wipes what another site left there.
	size, first := m.cfg.PortMax-m.cfg.PortMin+1, 0
	if targetHost != "" && size > 0 {
		sum := sha256.Sum256([]byte(targetHost + ":" + strconv.Itoa(req.Port)))
		first = int(binary.BigEndian.Uint32(sum[:4]) % uint32(size))
	}
	for i := range max(size, 0) {
		pt := m.cfg.PortMin + (first+i)%size
		if used[pt] {
			continue
		}
		l, err := net.Listen("tcp", net.JoinHostPort(bindHost(m.cfg.BindHost), strconv.Itoa(pt)))
		if err == nil {
			ln, port = l, pt
			break
		}
	}
	if ln == nil {
		m.mu.Unlock()
		return Preview{}, "", false, newErr(http.StatusServiceUnavailable, "no_preview_port", "tidak ada port preview yang bebas di %d–%d", m.cfg.PortMin, m.cfg.PortMax)
	}
	now := m.now().UTC()
	p := &preview{m: m, Preview: Preview{ID: auth.RandomID("pv_"), MachineID: req.MachineID, TargetHost: targetHost, TargetPort: req.Port, TargetScheme: req.Scheme,
		Path: path, ListenPort: port, SessionID: req.SessionID, Auto: req.Auto, State: StateWaiting, CreatedAt: now, LastUsedAt: now},
		ln: ln, tokens: map[string]token{}, cookies: map[string]time.Time{}, beacon: rateLimit{per: time.Second, max: 10}}
	if targetHost != "" {
		p.site = newSite(req.Scheme, targetHost, req.Port)
		p.State = StateLive // checkSite just reached it
	}
	p.tr = m.transport(p)
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	m.byID[p.ID] = p
	lp := p.newTokenLocked(path)
	v := p.Preview
	m.mu.Unlock()
	go func() { _ = p.srv.Serve(ln) }()
	m.publish(v)
	if p.site == nil {
		go m.checkLive(p.ID)
	}
	return v, lp, true, nil
}

func bindHost(h string) string {
	if h == "0.0.0.0" || h == "::" || h == "[::]" {
		return ""
	}
	return strings.Trim(h, "[]")
}

// checkLive probes the target once after creation.
func (m *Manager) checkLive(id string) {
	m.mu.Lock()
	p, ok := m.byID[id]
	m.mu.Unlock()
	if !ok || m.cfg.Dial == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
	defer cancel()
	c, err := m.cfg.Dial(ctx, p.MachineID, p.TargetPort)
	if err == nil {
		c.Close()
		m.setState(id, StateLive, "")
		return
	}
	st, msg := stateFor(err)
	m.setState(id, st, msg)
}

// stateFor maps a dial error to a state and message.
func stateFor(err error) (string, string) {
	msg := err.Error()
	if strings.Contains(msg, "AllowTcpForwarding") || strings.Contains(msg, "tersambung") || strings.Contains(msg, "terputus") {
		return StateError, msg
	}
	return StateWaiting, ""
}

func (m *Manager) setState(id, state, msg string) {
	m.mu.Lock()
	p, ok := m.byID[id]
	if !ok || (p.State == state && p.Error == msg) {
		m.mu.Unlock()
		return
	}
	p.State, p.Error = state, msg
	v := p.Preview
	m.mu.Unlock()
	m.publish(v)
}

func (m *Manager) publish(v Preview) {
	if m.cfg.Publish != nil {
		m.cfg.Publish(map[string]any{"type": "preview", "preview": v})
	}
}

// newTokenLocked mints a single-use launch token (60 s) and returns the
// launch path.
func (p *preview) newTokenLocked(to string) string {
	now := p.m.now()
	for k, t := range p.tokens {
		if now.After(t.exp) {
			delete(p.tokens, k)
		}
	}
	tok := randToken()
	p.tokens[tok] = token{exp: now.Add(TokenTTL), to: to}
	return "/__nb/launch?t=" + tok + "&to=" + url.QueryEscape(to)
}

func randToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Launch mints a new launch path for an existing preview.
func (m *Manager) Launch(id, path string) (string, time.Time, error) {
	if path == "" {
		m.mu.Lock()
		if p, ok := m.byID[id]; ok {
			path = p.Path
		}
		m.mu.Unlock()
	}
	path, err := cleanTarget(path)
	if err != nil {
		return "", time.Time{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return "", time.Time{}, ErrNotFound
	}
	p.LastUsedAt = m.now().UTC()
	return p.newTokenLocked(path), m.now().Add(TokenTTL).UTC(), nil
}

// List returns every preview (creation order).
func (m *Manager) List() []Preview {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Preview, 0, len(m.byID))
	for _, p := range m.byID {
		out = append(out, p.Preview)
	}
	sortPreviews(out)
	return out
}

// Get returns one preview.
func (m *Manager) Get(id string) (Preview, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return Preview{}, false
	}
	return p.Preview, true
}

// Find returns the preview of (machine, port).
func (m *Manager) Find(machineID string, port int) (Preview, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.byID {
		if p.MachineID == machineID && p.TargetPort == port && p.TargetHost == "" {
			return p.Preview, true
		}
	}
	return Preview{}, false
}

// Delete closes a preview and its listener.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	p, ok := m.byID[id]
	if ok {
		delete(m.byID, id)
	}
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	p.shutdown()
	if m.cfg.Publish != nil {
		m.cfg.Publish(map[string]any{"type": "previewRemoved", "id": id})
	}
	return nil
}

// Reap closes previews idle for IdleClose that no web node references.
func (m *Manager) Reap() {
	now := m.now()
	m.mu.Lock()
	var idle []string
	for id, p := range m.byID {
		if now.Sub(p.LastUsedAt) >= IdleClose {
			idle = append(idle, id)
		}
	}
	m.mu.Unlock()
	for _, id := range idle {
		if m.cfg.Referenced != nil && m.cfg.Referenced(id) {
			continue
		}
		_ = m.Delete(id)
	}
}

func (p *preview) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.srv.Shutdown(ctx)
	_ = p.srv.Close()
	p.tr.CloseIdleConnections()
	p.m.mu.Lock()
	if p.pubT != nil {
		p.pubT.Stop()
	}
	p.m.mu.Unlock()
}

// touch records activity (idle reaper).
func (p *preview) touch() {
	p.m.mu.Lock()
	p.LastUsedAt = p.m.now().UTC()
	p.m.mu.Unlock()
}
