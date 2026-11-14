package machine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/connlog"
	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

const machinesFile = "machines.json"

var (
	ErrNotFound    = errors.New("machine not found")
	ErrLocal       = errors.New("This PC cannot be changed or deleted")
	ErrInvalidSpec = errors.New("invalid machine data")
)

// AddRequest is the body of POST /api/machines.
type AddRequest struct {
	Name          string `json:"name"`
	SSHConfigHost string `json:"sshConfigHost"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	User          string `json:"user"`
	AuthMethod    string `json:"authMethod"`
	PrivateKey    string `json:"privateKey"`
	Passphrase    string `json:"passphrase"`
	Password      string `json:"password"`
	PersistMode   string `json:"persistMode"`
}

// Registry owns all machines and persists machines.json.
type Registry struct {
	st  *store.Store
	box *secret.Box
	hk  *hostKeyStore

	// Callbacks, set before Start.
	OnChange     func(Info)
	OnRemoved    func(id string)
	OnConnect    func(id string)
	SessionCount func(id string) int
	// PresetCommands lists commands the probe should look for.
	PresetCommands func() []string
	// HookHandler serves POST /nb/hook on reverse port-forwards; WantForward
	// says whether SSH machines should open one (settings hookChannel).
	HookHandler http.Handler
	WantForward func() bool
	// Log records what happens to the links (package connlog).
	Log func(connlog.Event)

	mu    sync.Mutex
	local *Local
	ssh   map[string]*SSH
	order []string

	saveMu    sync.Mutex
	saveTimer *time.Timer
	saved     []byte         // what the file holds, as JSON
	saving    sync.WaitGroup // a save saveSoon scheduled and has not finished
	closed    bool           // Close ran: nothing more is saved later
}

// OpenRegistry loads machines.json.
func OpenRegistry(st *store.Store, box *secret.Box) (*Registry, error) {
	r := &Registry{
		st: st, box: box,
		hk:  &hostKeyStore{nbFile: st.Path("known_hosts")},
		ssh: map[string]*SSH{},
	}
	var cfgs []Config
	if _, err := st.Load(machinesFile, &cfgs); err != nil {
		return nil, err
	}
	var localCfg Config
	for _, c := range cfgs {
		if c.ID == LocalID || c.Kind == "local" {
			localCfg = c
			continue
		}
		r.addSSHLocked(c)
	}
	r.local = NewLocal(localCfg)
	return r, nil
}

func (r *Registry) addSSHLocked(c Config) *SSH {
	id := c.ID
	m := newSSH(c, r.hk, func() (*Creds, error) { return r.loadCreds(id) })
	m.keepCreds = func(typed Creds) error { return r.keepCreds(m, typed) }
	m.onChange = func() {
		r.emit(id)
		r.saveSoon()
	}
	m.onConnect = func() {
		if r.OnConnect != nil {
			r.OnConnect(id)
		}
	}
	m.logEvent = func(e connlog.Event) {
		if r.Log != nil {
			r.Log(e)
		}
	}
	m.hookHandler = func() http.Handler { return r.HookHandler }
	m.wantForward = func() bool { return r.HookHandler != nil && (r.WantForward == nil || r.WantForward()) }
	r.ssh[id] = m
	r.order = append(r.order, id)
	return m
}

// Start probes This PC and starts connection loops for remote machines.
func (r *Registry) Start() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := r.local.Probe(ctx, r.presetCommands()); err == nil {
			r.emit(LocalID)
			r.saveSoon()
		}
	}()
	r.mu.Lock()
	ms := make([]*SSH, 0, len(r.ssh))
	for _, id := range r.order {
		ms = append(ms, r.ssh[id])
	}
	r.mu.Unlock()
	for _, m := range ms {
		m.EnsureLoop()
	}
}

// Close disconnects every SSH machine and saves them once more; a save scheduled before it
// never writes after it.
func (r *Registry) Close() {
	r.saveMu.Lock()
	r.closed = true
	if r.saveTimer != nil && r.saveTimer.Stop() {
		r.saveTimer = nil
		r.saving.Done()
	}
	r.saveMu.Unlock()
	r.saving.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.ssh {
		m.Close()
	}
	r.saveLocked()
}

func (r *Registry) presetCommands() []string {
	if r.PresetCommands != nil {
		return r.PresetCommands()
	}
	return []string{"claude", "codex", "gemini"}
}

func (r *Registry) emit(id string) {
	if r.OnChange == nil {
		return
	}
	if in, ok := r.Info(id); ok {
		r.OnChange(in)
	}
}

func (r *Registry) saveSoon() {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	if r.saveTimer != nil || r.closed {
		return
	}
	r.saving.Add(1)
	r.saveTimer = time.AfterFunc(300*time.Millisecond, func() {
		defer r.saving.Done()
		r.saveMu.Lock()
		r.saveTimer = nil
		r.saveMu.Unlock()
		r.mu.Lock()
		r.saveLocked()
		r.mu.Unlock()
	})
}

func (r *Registry) saveLocked() {
	cfgs := []Config{r.local.Config()}
	for _, id := range r.order {
		cfgs = append(cfgs, r.ssh[id].Config())
	}
	// A change of state saves too (each reconnect attempt to a server that is down, every
	// 30 s or so), and the state is not in the file: write only what differs.
	b, err := json.Marshal(cfgs)
	if err == nil && bytes.Equal(b, r.saved) {
		return
	}
	if r.st.Save(machinesFile, cfgs) == nil && err == nil {
		r.saved = b
	}
}

// RefreshForwards re-evaluates the hook forward of every SSH machine.
func (r *Registry) RefreshForwards() {
	r.mu.Lock()
	ms := make([]*SSH, 0, len(r.ssh))
	for _, m := range r.ssh {
		ms = append(ms, m)
	}
	r.mu.Unlock()
	for _, m := range ms {
		m.RefreshForward()
	}
}

// Local returns This PC.
func (r *Registry) Local() *Local { return r.local }

// Host returns a machine by id.
func (r *Registry) Host(id string) (Host, bool) {
	if id == LocalID {
		return r.local, true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.ssh[id]
	return m, ok
}

// SSH returns a remote machine by id.
func (r *Registry) SSH(id string) (*SSH, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.ssh[id]
	return m, ok
}

// Info returns the API view of one machine.
func (r *Registry) Info(id string) (Info, bool) {
	h, ok := r.Host(id)
	if !ok {
		return Info{}, false
	}
	in := h.Info()
	if r.SessionCount != nil {
		in.SessionCount = r.SessionCount(id)
	}
	return in, true
}

// List returns all machines, This PC first.
func (r *Registry) List() []Info {
	r.mu.Lock()
	ids := append([]string{LocalID}, r.order...)
	r.mu.Unlock()
	out := make([]Info, 0, len(ids))
	for _, id := range ids {
		if in, ok := r.Info(id); ok {
			out = append(out, in)
		}
	}
	return out
}

// Add validates and registers a new SSH machine (not yet connected).
func (r *Registry) Add(req AddRequest) (*SSH, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.SSHConfigHost = strings.TrimSpace(req.SSHConfigHost)
	req.Host = strings.TrimSpace(req.Host)
	req.User = strings.TrimSpace(req.User)
	if req.SSHConfigHost == "" && req.Host == "" {
		return nil, fmt.Errorf("%w: set sshConfigHost or host", ErrInvalidSpec)
	}
	if strings.ContainsAny(req.SSHConfigHost+req.Host+req.User, " \t\r\n'\"`;$\\") {
		return nil, fmt.Errorf("%w: character not allowed in host/user", ErrInvalidSpec)
	}
	if req.Port < 0 || req.Port > 65535 {
		return nil, fmt.Errorf("%w: port", ErrInvalidSpec)
	}
	switch req.AuthMethod {
	case "":
		req.AuthMethod = "sshconfig"
	case "sshconfig", "agent", "key", "password":
	default:
		return nil, fmt.Errorf("%w: authMethod", ErrInvalidSpec)
	}
	if err := validPersist(&req.PersistMode); err != nil {
		return nil, err
	}
	if req.AuthMethod == "key" && strings.TrimSpace(req.PrivateKey) == "" {
		return nil, fmt.Errorf("%w: private key is empty", ErrInvalidSpec)
	}
	if req.AuthMethod == "password" && req.Password == "" {
		return nil, fmt.Errorf("%w: password is empty", ErrInvalidSpec)
	}
	if req.Name == "" {
		req.Name = req.SSHConfigHost
		if req.Name == "" {
			req.Name = req.Host
		}
	}
	if len(req.Name) > 64 {
		req.Name = req.Name[:64]
	}
	cfg := Config{
		ID:   "m_" + randomSuffix(),
		Name: req.Name, Kind: "ssh",
		Host: req.Host, Port: req.Port, User: req.User,
		SSHConfigHost: req.SSHConfigHost, AuthMethod: req.AuthMethod,
		PersistMode: req.PersistMode,
	}
	if cfg.SSHConfigHost == "" && cfg.Port == 0 {
		cfg.Port = 22
	}
	if req.PrivateKey != "" || req.Password != "" {
		creds := Creds{PrivateKey: req.PrivateKey, Passphrase: req.Passphrase, Password: req.Password}
		if err := r.saveCreds(cfg.ID, &creds); err != nil {
			return nil, err
		}
		cfg.CredRef = cfg.ID
	}
	r.mu.Lock()
	m := r.addSSHLocked(cfg)
	r.saveLocked()
	r.mu.Unlock()
	r.emit(cfg.ID)
	return m, nil
}

func validPersist(p *string) error {
	switch *p {
	case "":
		*p = "auto"
	case "auto", "tmux", "direct":
	default:
		return fmt.Errorf("%w: persistMode", ErrInvalidSpec)
	}
	return nil
}

// Update renames a machine and/or changes its persist mode.
func (r *Registry) Update(id string, name, persist *string) (Info, error) {
	if id == LocalID {
		if name != nil && strings.TrimSpace(*name) != "" {
			r.local.mu.Lock()
			r.local.cfg.Name = strings.TrimSpace(*name)
			r.local.mu.Unlock()
			r.emit(id)
			r.saveSoon()
			in, _ := r.Info(id)
			return in, nil
		}
		return Info{}, ErrLocal
	}
	m, ok := r.SSH(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	if persist != nil {
		if err := validPersist(persist); err != nil {
			return Info{}, err
		}
	}
	m.update(func(c *Config) {
		if name != nil && strings.TrimSpace(*name) != "" {
			c.Name = strings.TrimSpace(*name)
		}
		if persist != nil {
			c.PersistMode = *persist
		}
	})
	in, _ := r.Info(id)
	return in, nil
}

// Remove deletes an SSH machine and its stored credentials.
func (r *Registry) Remove(id string) error {
	if id == LocalID {
		return ErrLocal
	}
	r.mu.Lock()
	m, ok := r.ssh[id]
	if !ok {
		r.mu.Unlock()
		return ErrNotFound
	}
	delete(r.ssh, id)
	for i, x := range r.order {
		if x == id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	r.saveLocked()
	r.mu.Unlock()
	m.Close()
	_ = os.Remove(r.st.Path("creds", id+".enc"))
	if r.OnRemoved != nil {
		r.OnRemoved(id)
	}
	return nil
}

// Probe re-probes a machine.
func (r *Registry) Probe(ctx context.Context, id string) (Info, error) {
	h, ok := r.Host(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	if _, err := h.Probe(ctx, r.presetCommands()); err != nil {
		return Info{}, err
	}
	r.emit(id)
	r.saveSoon()
	in, _ := r.Info(id)
	return in, nil
}

// keepCreds puts what was typed in a dialog next to what is stored already.
func (r *Registry) keepCreds(m *SSH, typed Creds) error {
	m.mu.Lock()
	id, ref := m.cfg.ID, m.cfg.CredRef
	m.mu.Unlock()
	stored := Creds{}
	if ref != "" {
		c, err := r.loadCreds(id)
		if err != nil {
			return err
		}
		stored = *c
	}
	merged := stored.with(typed)
	if err := r.saveCreds(id, &merged); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg.CredRef = id
	m.answers = Creds{}
	m.mu.Unlock()
	r.saveSoon()
	return nil
}

func (r *Registry) saveCreds(id string, c *Creds) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	blob, err := r.box.Seal(b)
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(r.st.Path("creds", id+".enc"), blob)
}

func (r *Registry) loadCreds(id string) (*Creds, error) {
	blob, err := os.ReadFile(r.st.Path("creds", id+".enc"))
	if err != nil {
		return nil, err
	}
	b, err := r.box.Open(blob)
	if err != nil {
		return nil, err
	}
	var c Creds
	return &c, json.Unmarshal(b, &c)
}

func randomSuffix() string {
	const alpha = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return string(b)
}
