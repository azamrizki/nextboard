package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/marker"
	"github.com/azamrizki/nextboard/internal/profiles"
	"github.com/azamrizki/nextboard/internal/screen"
	"github.com/azamrizki/nextboard/internal/settings"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/azamrizki/nextboard/internal/transcript"
)

const sessionsFile = "sessions.json"

var (
	ErrNotFound        = errors.New("session not found")
	ErrUnknownPreset   = errors.New("unknown preset")
	ErrBadRequest      = errors.New("invalid request")
	ErrBadHandle       = errors.New("invalid handle (a letter first, then letters/digits/-, at most 20; not a reserved word)")
	ErrHandleTaken     = errors.New("the handle is already used by another console")
	ErrProfileNotFound = errors.New("profile not found")
	ErrProfileMismatch = errors.New("the profile is not for this machine/agent")
	ErrAgentDisabled   = errors.New("this agent is turned off in Settings › Agents")
	// API providers (§10.2); the hub's ProviderHub wraps these with what went wrong.
	ErrProviderNotFound = errors.New("API provider not found")
	ErrProviderMismatch = errors.New("this agent cannot run on that API provider")
	ErrProviderBudget   = errors.New("the API provider's budget for this month is spent")
	ErrAgentThisPC      = errors.New("agents always run on This PC; a server runs only plain shells")
	ErrBadMode          = errors.New("unknown launch mode")
)

// LaunchRequest selects how the agent starts (§3.1).
type LaunchRequest struct {
	Mode           string `json:"mode"`
	AgentSessionID string `json:"agentSessionId"`
}

// CreateRequest is the body of POST /api/sessions.
type CreateRequest struct {
	MachineID string  `json:"machineId"`
	Preset    string  `json:"preset"`
	Agent     string  `json:"agent"` // alias of preset
	Name      string  `json:"name"`
	Cwd       string  `json:"cwd"`
	Cols      uint16  `json:"cols"`
	Rows      uint16  `json:"rows"`
	Handle    string  `json:"handle"`
	Yolo      *bool   `json:"yolo"`
	ProfileID *string `json:"profileId"`
	// ProviderID runs the console on an API provider instead of a login (§10.2).
	ProviderID *string        `json:"providerId"`
	Launch     *LaunchRequest `json:"launch"`
	// Model and Effort: nil = the default in settings, "" = the CLI's own choice.
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
	// WorkspaceID is resolved by the server, which also sets ProjectID.
	WorkspaceID string `json:"workspaceId"`
	ProjectID   string `json:"-"`
	// Panel: no board node (a terminal or agent of the Code workbench).
	Panel bool `json:"panel"`
}

// RestartRequest is the body of POST /api/sessions/{id}/restart.
// AgentSessionID switches the console to that conversation in one call
// (mode defaults to "resume").
type RestartRequest struct {
	Resume         *bool   `json:"resume"`
	Mode           string  `json:"mode"`
	AgentSessionID string  `json:"agentSessionId"`
	Agent          string  `json:"agent"`
	ProfileID      *string `json:"profileId"`
	// ProviderID: nil keeps the console's, "" goes back to a login, an id runs on that provider.
	ProviderID *string `json:"providerId"`
	Yolo       *bool   `json:"yolo"`
	// Model and Effort: nil keeps what the console has, "" = the CLI's own choice.
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
}

// Manager owns every session, persists specs and scrollback, and relaunches
// terminals after a hub restart or an SSH reconnect.
type Manager struct {
	st       *store.Store
	machines *machine.Registry

	// Agents is the catalog; Settings returns the current settings;
	// Profiles resolves profileId. All are set before Load.
	Agents   *agent.Store
	Settings func() settings.Settings
	Profiles *profiles.Store
	// Providers resolves providerId (nil = none can be used).
	Providers ProviderHub
	// HookURL returns NB_HOOK_URL for a machine ("" = tty only).
	HookURL func(machineID string) string
	// RemoteWorkDir is the local folder of an agent that works on a server.
	RemoteWorkDir func(machineID, cwd string) (string, error)
	// OnChannelLost: the channel of a console on a server died.
	OnChannelLost func(sp Spec)
	// LaunchContext is what the hub gives an agent's launch on This PC.
	LaunchContext func(sp Spec) (*agent.LaunchContext, error)
	// CommitHooks is the folder of hooks that keep agents from signing commits ("" = none).
	CommitHooks string
	// IDELink connects a Claude console to the editor card (docs/API-agents.md §24):
	// it returns what to add to the console's environment. configDir is the CLI's.
	IDELink func(sp Spec, configDir string) ([]string, error)

	// HubName returns the hub's display name (used as "This PC" label).
	HubName  func() string
	OnChange func(Info)
	// OnFileTool says that a hook named a file an agent's tool works on.
	OnFileTool func(sp Spec, path string)
	// OnContextLost says that an agent dropped its conversation or compacted it (/clear,
	// /compact), so what was typed into it may be gone. Its system prompt stays.
	OnContextLost func(sessionID string)
	// OnRemove fires after a session was deleted, with its last spec.
	OnRemove func(sp Spec)
	// OnAttention receives notable transitions (needsYou, finished, error).
	OnAttention func(in Info, ev agentstate.Event)
	// OnMarker, when set, sees every accepted marker (debug logging and
	// the gated real-CLI checks). Called with the session lock held.
	OnMarker func(sessionID string, mk marker.Marker)
	// OnOutput receives clean output (markers stripped) after the session
	// lock is released (loop output triggers, docs/API-orchestration.md §11).
	OnOutput func(sessionID string, clean []byte)
	// QueueLen returns the number of active deliveries of a session.
	QueueLen func(sessionID string) int
	// HandleConflict reports whether a handle equals a group name in the
	// workspace (phase 6 rename rule, API-agents §2).
	HandleConflict func(workspaceID, handle string) bool

	mu       sync.Mutex
	sessions map[string]*Session
	order    []string
	shutting atomic.Bool

	saveMu     sync.Mutex
	saveTimer  *time.Timer
	stopTicker chan struct{}
}

// NewManager creates an empty manager; set Agents etc., then Load, Start.
func NewManager(st *store.Store, machines *machine.Registry) *Manager {
	return &Manager{st: st, machines: machines, sessions: map[string]*Session{}, stopTicker: make(chan struct{}),
		Agents: agent.NewStore(st), Settings: settings.Defaults}
}

// Presets returns the agent catalog (superset of the old presets).
func (m *Manager) Presets() []agent.Agent { return m.Agents.All() }

// ResetPresetCache forces the next read of presets.json.
func (m *Manager) ResetPresetCache() { m.Agents.Reload() }

func (m *Manager) isShuttingDown() bool { return m.shutting.Load() }

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// agentFor resolves the agent of a spec; a deleted custom agent falls back
// to the command stored in the spec.
func (m *Manager) agentFor(sp Spec) agent.Agent {
	id := sp.Agent
	if id == "" {
		id = sp.Preset
	}
	if a, ok := m.Agents.Get(id); ok {
		return a
	}
	kind := sp.AgentKind
	if kind == "" {
		kind = agent.KindAgent
		if sp.Command == "$SHELL" {
			kind = agent.KindShell
		}
	}
	a := agent.Agent{ID: id, Name: sp.Name, Kind: kind, Source: agent.SourceCustom, Command: sp.Command,
		Args: sp.Args, Env: sp.Env, LiveStatus: agent.LiveScreen, Transcript: agent.TranscriptNone,
		Resume: agent.Resume{Kind: agent.ResumeContinue, ContinueArgs: sp.ResumeArgs}}
	if len(sp.ResumeArgs) == 0 {
		a.Resume.Kind = agent.ResumeNone
	}
	if kind == agent.KindShell {
		a.LiveStatus = agent.LiveNone
	}
	return a
}

// Load reads sessions.json and each scrollback file, migrating phase 1–4
// sessions (agent, kind, hookToken, handle).
func (m *Manager) Load() error {
	var list []stored
	if _, err := m.st.Load(sessionsFile, &list); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var noHandle []*Session
	for _, e := range list {
		sp := e.Spec
		if sp.ID == "" {
			continue
		}
		if sp.Agent == "" {
			sp.Agent = sp.Preset
		}
		if sp.Preset == "" {
			sp.Preset = sp.Agent
		}
		if sp.AgentKind == "" {
			sp.AgentKind = m.agentFor(sp).Kind
		}
		s := newSession(m, sp)
		s.priv = e.private
		if s.priv.HookToken == "" {
			s.priv.HookToken = newToken()
		}
		if b, err := os.ReadFile(m.scrollbackPath(sp.ID)); err == nil && len(b) > 0 {
			s.ring.Write(b)
			s.ring.Write([]byte("\r\n\x1b[90m── hub restarted ──\x1b[0m\r\n"))
			replay := s.ring.Bytes()
			if len(replay) > screen.ReplayBytes {
				replay = replay[len(replay)-screen.ReplayBytes:]
			}
			s.scr.Feed(replay)
		}
		if sp.MachineID != machine.LocalID {
			s.status = StatusReconnecting
			s.eng.SetProc(StatusReconnecting, time.Now())
		} else {
			s.spec.Paused, s.spec.PausedAt = false, nil // the stopped process died with the hub
		}
		if sp.Handle == "" {
			noHandle = append(noHandle, s)
		}
		m.sessions[sp.ID] = s
		m.order = append(m.order, sp.ID)
	}
	sort.SliceStable(noHandle, func(i, j int) bool { return noHandle[i].spec.CreatedAt.Before(noHandle[j].spec.CreatedAt) })
	for _, s := range noHandle {
		s.spec.Handle = m.nextHandleLocked()
	}
	return nil
}

// restartMode decides how a console starts after a hub restart or when a
// direct SSH channel is relaunched (settings.agents.resumeOnRestart).
func (m *Manager) restartMode(s *Session) (mode string, offer bool) {
	s.mu.Lock()
	last := s.priv.LastForeground
	s.mu.Unlock()
	switch m.Settings().Agents.ResumeOnRestart {
	case "never":
		return agent.ModeShell, false
	case "ask":
		return agent.ModeShell, last != agentstate.FgShell
	}
	if last == agentstate.FgShell {
		return agent.ModeShell, false
	}
	return agent.ModeResume, false
}

// Start relaunches restored sessions and starts background tickers.
// Local sessions start immediately; remote ones as soon as their machine
// connects (MachineConnected).
func (m *Manager) Start() {
	for _, s := range m.all() {
		if s.Spec().MachineID == machine.LocalID {
			go m.relaunch(s)
		}
	}
	go m.tick()
}

// relaunch starts s with the restart mode.
func (m *Manager) relaunch(s *Session) {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	mode, offer := m.restartMode(s)
	if (mode == agent.ModeResume || offer) && s.neverSaved() {
		offer = false
		if mode == agent.ModeResume {
			mode = agent.ModeFresh
		}
	}
	s.mu.Lock()
	s.resumeOffer = offer
	s.mu.Unlock()
	s.launch(mode)
}

func (m *Manager) tick() {
	t := time.NewTicker(250 * time.Millisecond)
	flush := time.NewTicker(30 * time.Second)
	defer t.Stop()
	defer flush.Stop()
	for {
		select {
		case <-m.stopTicker:
			return
		case now := <-t.C:
			for _, s := range m.all() {
				if s.checkIdle(now) {
					m.changed(s)
				}
				s.tick(now)
			}
		case <-flush.C:
			m.flushScrollback(false)
		}
	}
}

// MachineConnected relaunches sessions waiting on machine id.
func (m *Manager) MachineConnected(id string) {
	for _, s := range m.all() {
		if s.Spec().MachineID != id {
			continue
		}
		go m.relaunchIfWaiting(s)
	}
}

func (m *Manager) relaunchIfWaiting(s *Session) {
	s.mu.Lock()
	waiting := s.status == StatusReconnecting && !s.deleted
	s.mu.Unlock()
	if waiting {
		m.relaunch(s)
	}
}

// relaunchWhenConnected handles a dropped remote channel. If the machine
// link is still up (only the channel died) retry shortly; otherwise the
// machine's reconnect will trigger MachineConnected.
func (m *Manager) relaunchWhenConnected(s *Session) {
	if m.OnChannelLost != nil {
		m.OnChannelLost(s.Spec())
	}
	h, ok := m.machines.Host(s.Spec().MachineID)
	if !ok || !h.Connected() {
		return
	}
	time.AfterFunc(2*time.Second, func() { m.relaunchIfWaiting(s) })
}

func (m *Manager) all() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.sessions[id])
	}
	return out
}

// Get returns a session by id.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

func (m *Manager) enrich(in Info) Info {
	if m.QueueLen != nil {
		in.Queue = m.QueueLen(in.ID)
	}
	if a, ok := m.Agents.Get(in.Agent); ok {
		in.Color = a.Color
	} else {
		in.Color = "#8b949e"
	}
	if mi, ok := m.machines.Info(in.MachineID); ok {
		in.MachineName = mi.Name
		if in.MachineID == machine.LocalID && mi.Name == "This PC" && m.HubName != nil {
			if hn := m.HubName(); hn != "" {
				in.MachineName = hn
			}
		}
	} else {
		in.MachineName = in.MachineID
	}
	if in.Args == nil {
		in.Args = []string{}
	}
	if in.ResumeArgs == nil {
		in.ResumeArgs = []string{}
	}
	if in.Env == nil {
		in.Env = []string{}
	}
	return in
}

// SessionInfo returns the API view of s.
func (m *Manager) SessionInfo(s *Session) Info {
	s.mu.Lock()
	in := s.infoLocked()
	s.mu.Unlock()
	return m.enrich(in)
}

// Info returns the API view of one session.
func (m *Manager) Info(id string) (Info, bool) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, false
	}
	return m.SessionInfo(s), true
}

// List returns every session in creation order.
func (m *Manager) List() []Info {
	all := m.all()
	out := make([]Info, 0, len(all))
	for _, s := range all {
		out = append(out, m.SessionInfo(s))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (m *Manager) changed(s *Session) {
	if m.OnChange != nil && !m.isShuttingDown() {
		m.OnChange(m.SessionInfo(s))
	}
}

func (m *Manager) attention(s *Session, ev []agentstate.Event) {
	if len(ev) == 0 || m.OnAttention == nil || m.isShuttingDown() {
		return
	}
	in := m.SessionInfo(s)
	for _, e := range ev {
		m.OnAttention(in, e)
	}
}

// ---- handles (§2) ----

var (
	handleNames = strings.Fields("Arkan Bintang Cantika Daffa Elvan Farel Gibran Haikal Ilham Jihan Kayla Luna Maura Naufal Olivia Pandu Rafa Reyhan Salsa Tiara Vania Wildan Yasmin Zahra")
	handleRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,19}$`)
	reserved    = map[string]bool{"everyone": true, "all": true, "everybody": true, "me": true, "bar": true, "hub": true,
		"orchestrator": true, "group": true, "loop": true, "memory": true}
)

// ValidHandle checks syntax and reserved words.
func ValidHandle(h string) bool { return handleRe.MatchString(h) && !reserved[strings.ToLower(h)] }

func (m *Manager) takenLocked(except *Session) map[string]bool {
	taken := map[string]bool{}
	for _, s := range m.sessions {
		if s == except {
			continue
		}
		taken[strings.ToLower(s.spec.Handle)] = true
	}
	return taken
}

// nextHandleLocked returns the first free default name, then Arkan2, Bintang2, …
func (m *Manager) nextHandleLocked() string {
	taken := m.takenLocked(nil)
	for n := 1; ; n++ {
		for _, h := range handleNames {
			c := h
			if n > 1 {
				c += strconv.Itoa(n)
			}
			if !taken[strings.ToLower(c)] {
				return c
			}
		}
	}
}

// ---- creation and changes ----

// validateProfile checks that profile id belongs to the machine and agent.
// ProviderHub is what consoles need of the hub's API providers (internal/providers).
type ProviderHub interface {
	// Check: the provider exists, can run this agent, and has budget left this month.
	Check(id string, a agent.Agent) error
	// Launch: what a launch needs, its key included. It also checks, as Check does.
	Launch(id string, a agent.Agent) (*agent.ProviderLaunch, error)
	// ConfigDir: the CLI's folder for the provider, where its conversations are ("" = none).
	ConfigDir(id string, a agent.Agent) string
}

func (m *Manager) checkProvider(id string, a agent.Agent) error {
	if m.Providers == nil {
		return ErrProviderNotFound
	}
	return m.Providers.Check(id, a)
}

func (m *Manager) validateProfile(id, machineID string, a agent.Agent) error {
	if id == "" {
		return nil
	}
	if m.Profiles == nil {
		return ErrProfileNotFound
	}
	p, ok := m.Profiles.Get(id)
	if !ok {
		return ErrProfileNotFound
	}
	if a.ProfileEnv == "" || p.MachineID != machineID || (p.Agent != a.ID && p.Agent != a.CapBase()) {
		return ErrProfileMismatch
	}
	return nil
}

// defaultProfile is the login a console of this agent gets unless told otherwise:
// the NextBoard account (docs/API-agents.md §10), or none when that default was
// turned off. A stale default never blocks a launch.
func (m *Manager) defaultProfile(machineID string, a agent.Agent) string {
	if a.ProfileEnv == "" {
		return ""
	}
	st := m.Settings()
	id := st.Agents.Profiles[machineID][a.ID]
	if id == "" {
		id = st.Agents.Profiles[machineID][a.CapBase()]
	}
	if m.validateProfile(id, machineID, a) != nil {
		return ""
	}
	return id
}

// profileDir is the folder of the session's profile ("" = default login), or of its API provider.
func (m *Manager) profileDir(sp Spec) string {
	if sp.ProviderID != "" {
		if m.Providers == nil {
			return ""
		}
		a, ok := m.Agents.Get(sp.Agent)
		if !ok {
			return ""
		}
		return m.Providers.ConfigDir(sp.ProviderID, a)
	}
	if sp.ProfileID == "" || m.Profiles == nil {
		return ""
	}
	if p, ok := m.Profiles.Get(sp.ProfileID); ok && p.MachineID == sp.MachineID {
		return p.Dir
	}
	return ""
}

// plan builds the wrapper launch of an agent console.
func (m *Manager) plan(s *Session, a agent.Agent, sp Spec, mode string) (agent.Plan, error) {
	st := m.Settings()
	s.mu.Lock()
	token := s.priv.HookToken
	s.mu.Unlock()
	o := agent.Options{Mode: mode, AgentSessionID: sp.AgentSessionID, Yolo: sp.Yolo, LiveStatus: st.Agents.LiveStatus,
		SessionID: sp.ID, Token: token, ProfileDir: m.profileDir(sp), Model: sp.Model, Effort: sp.Effort, CoAuthor: st.Agents.CoAuthor}
	if p := st.Agents.Paths[sp.MachineID][a.ID]; p != "" {
		o.Command = p
	}
	if sp.ProviderID != "" {
		if m.Providers == nil {
			return agent.Plan{}, ErrProviderNotFound
		}
		pl, err := m.Providers.Launch(sp.ProviderID, a)
		if err != nil {
			return agent.Plan{}, err
		}
		o.Provider, o.ProfileDir = pl, ""
	}
	if st.Agents.HookChannel != "tty" && m.HookURL != nil {
		o.HookURL = m.HookURL(sp.MachineID)
	}
	if m.LaunchContext != nil && a.Kind == agent.KindAgent && sp.MachineID == machine.LocalID {
		c, err := m.LaunchContext(sp)
		switch {
		case err == nil:
			o.Context = c
		case sp.RemoteMachineID != "":
			return agent.Plan{}, err
		default:
			log.Printf("launch context %s: %v", sp.ID, err)
		}
	}
	// Never launch without the tools: the agent would act on This PC.
	if sp.RemoteMachineID != "" && (o.Context == nil || !o.Context.Remote) {
		return agent.Plan{}, ErrAgentThisPC
	}
	p, err := agent.Build(a, o)
	if err != nil {
		return p, err
	}
	if ch := st.Agents.HookChannel; ch == "tty" || (ch == "http" && o.HookURL != "") {
		p.Env = append(p.Env, "NB_HOOK_CH="+ch)
	}
	if m.CommitHooks != "" && !st.Agents.CoAuthor && a.Kind == agent.KindAgent && sp.MachineID == machine.LocalID {
		p.Env = append(p.Env, agent.CommitHookEnv(m.CommitHooks)...)
	}
	// The link is an extra: a console works without it.
	if m.IDELink != nil && st.Agents.IDELink && a.Kind == agent.KindAgent && a.CapBase() == "claude" && sp.MachineID == machine.LocalID && sp.RemoteMachineID == "" && sp.WorkspaceID != "" {
		if h, ok := m.machines.Host(sp.MachineID); ok {
			cfg := transcript.ClaudeConfigDir(s.homeOf(h), firstNonEmpty(o.ProfileDir, localEnv(h, "CLAUDE_CONFIG_DIR")))
			env, err := m.IDELink(sp, cfg)
			if err != nil {
				log.Printf("editor link %s: %v", sp.ID, err)
			}
			p.Env = append(p.Env, env...)
		}
	}
	return p, nil
}

// Create adds a session and launches it asynchronously.
func (m *Manager) Create(req CreateRequest) (Info, error) {
	id := req.Agent
	if id == "" {
		id = req.Preset
	}
	a, ok := m.Agents.Get(id)
	if !ok {
		return Info{}, ErrUnknownPreset
	}
	if req.MachineID == "" {
		req.MachineID = machine.LocalID
	}
	h, ok := m.machines.Host(req.MachineID)
	if !ok {
		return Info{}, machine.ErrNotFound
	}
	st := m.Settings()
	if a.Kind == agent.KindAgent && st.Agents.Disabled[a.ID] {
		return Info{}, ErrAgentDisabled
	}
	target := req.MachineID
	if a.Kind == agent.KindAgent && target != machine.LocalID {
		if !st.Agents.RemoteTools || !a.WorksOnServers() || m.RemoteWorkDir == nil {
			return Info{}, ErrAgentThisPC
		}
		req.MachineID = machine.LocalID
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = a.Name
	}
	if len(req.Name) > 80 || len(req.Cwd) > 4096 || strings.ContainsRune(req.Cwd, 0) {
		return Info{}, ErrBadRequest
	}
	mode := agent.ModeFresh
	aid := ""
	if req.Launch != nil {
		if req.Launch.Mode != "" && !agent.ValidMode(req.Launch.Mode) {
			return Info{}, ErrBadMode
		}
		if req.Launch.Mode != "" {
			mode = req.Launch.Mode
		}
		if req.Launch.AgentSessionID != "" && !validSessionID(req.Launch.AgentSessionID) {
			return Info{}, ErrBadRequest
		}
		aid = req.Launch.AgentSessionID
	}
	yolo := st.Agents.YoloDefault
	if req.Yolo != nil {
		yolo = *req.Yolo
	}
	yolo = yolo && a.HasYolo()
	def := st.Agents.ModelFor("", a.ID)
	model, effort, err := chooseModel(a, req.Model, req.Effort, def.Model, def.Effort)
	if err != nil {
		return Info{}, err
	}
	profileID := m.defaultProfile(req.MachineID, a)
	if req.ProfileID != nil {
		profileID = *req.ProfileID
		if a.ProfileEnv == "" {
			profileID = ""
		}
		if err := m.validateProfile(profileID, req.MachineID, a); err != nil {
			return Info{}, err
		}
	}
	providerID := ""
	if req.ProviderID != nil && *req.ProviderID != "" {
		if err := m.checkProvider(*req.ProviderID, a); err != nil {
			return Info{}, err
		}
		// its own folder instead of a login; its own model unless one was chosen
		providerID, profileID = *req.ProviderID, ""
		if req.Model == nil {
			model = ""
		}
	}
	cwd := strings.TrimSpace(req.Cwd)
	if cwd == "" {
		cwd = "~"
		if pr := h.Info().Probe; pr != nil && pr.Home != "" {
			cwd = pr.Home
		} else if target == machine.LocalID {
			if home, err := os.UserHomeDir(); err == nil {
				cwd = home
			}
		}
	}
	if req.Cols == 0 {
		req.Cols = 120
	}
	if req.Rows == 0 {
		req.Rows = 32
	}
	sid := auth.RandomID("s_")
	spec := Spec{
		ID: sid, Name: req.Name, Preset: a.ID, MachineID: req.MachineID,
		Command: a.Command, Args: append([]string{}, a.Args...), ResumeArgs: append([]string{}, a.Resume.ContinueArgs...),
		Env: append([]string{}, a.Env...), Cwd: cwd,
		Cols: clamp(req.Cols, 10, 500), Rows: clamp(req.Rows, 4, 300),
		CreatedAt: time.Now().UTC(), ProjectID: req.ProjectID, WorkspaceID: req.WorkspaceID, Panel: req.Panel,
		Agent: a.ID, AgentKind: a.Kind, Yolo: yolo, ProfileID: profileID, ProviderID: providerID, AgentSessionID: aid,
		Model: model, Effort: effort,
	}
	if target != req.MachineID {
		dir, err := m.RemoteWorkDir(target, cwd)
		if err != nil {
			return Info{}, err
		}
		spec.RemoteMachineID, spec.RemoteCwd, spec.Cwd = target, cwd, dir
	}
	if sm, ok := m.machines.SSH(req.MachineID); ok && sm.EffectivePersist() == "tmux" {
		spec.TmuxName = "nb-" + sid
	}
	m.mu.Lock()
	if req.Handle != "" {
		if !ValidHandle(req.Handle) {
			m.mu.Unlock()
			return Info{}, ErrBadHandle
		}
		if m.takenLocked(nil)[strings.ToLower(req.Handle)] {
			m.mu.Unlock()
			return Info{}, ErrHandleTaken
		}
		spec.Handle = req.Handle
	} else {
		spec.Handle = m.nextHandleLocked()
	}
	s := newSession(m, spec)
	s.priv.HookToken = newToken()
	if model != "" || effort != "" {
		s.priv.ModelAt = spec.CreatedAt
	}
	m.sessions[sid] = s
	m.order = append(m.order, sid)
	m.mu.Unlock()
	m.saveNow()
	m.changed(s)
	go func() {
		s.launchMu.Lock()
		defer s.launchMu.Unlock()
		s.launch(mode)
	}()
	return m.SessionInfo(s), nil
}

// chooseModel is what a console of a runs: what was asked for, else the fallback.
// What this CLI cannot be told is dropped.
func chooseModel(a agent.Agent, model, effort *string, defModel, defEffort string) (string, string, error) {
	m, e := defModel, defEffort
	if model != nil {
		m = strings.TrimSpace(*model)
	}
	if effort != nil {
		e = strings.TrimSpace(*effort)
	}
	if !agent.ValidModel(m) || !agent.ValidEffort(e) {
		return "", "", ErrBadRequest
	}
	if !a.HasModels() {
		m = ""
	}
	if !a.HasEfforts() {
		e = ""
	}
	return m, e, nil
}

// HandleTaken reports whether a handle is used by any console (case-insensitive).
func (m *Manager) HandleTaken(h string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.takenLocked(nil)[strings.ToLower(h)]
}

// Update renames and/or changes the handle (PATCH /api/sessions/{id}).
func (m *Manager) Update(id string, name, handle *string) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	if name != nil {
		n := strings.TrimSpace(*name)
		if n == "" || len(n) > 80 {
			return Info{}, ErrBadRequest
		}
		name = &n
	}
	if handle != nil && m.HandleConflict != nil && m.HandleConflict(s.Spec().WorkspaceID, *handle) {
		return Info{}, ErrHandleTaken
	}
	m.mu.Lock()
	if handle != nil {
		if !ValidHandle(*handle) {
			m.mu.Unlock()
			return Info{}, ErrBadHandle
		}
		if m.takenLocked(s)[strings.ToLower(*handle)] {
			m.mu.Unlock()
			return Info{}, ErrHandleTaken
		}
	}
	s.mu.Lock()
	if name != nil {
		s.spec.Name = *name
	}
	if handle != nil {
		s.spec.Handle = *handle
	}
	s.mu.Unlock()
	m.mu.Unlock()
	m.saveNow()
	m.changed(s)
	return m.SessionInfo(s), nil
}

// Rename changes the display name.
func (m *Manager) Rename(id, name string) (Info, error) { return m.Update(id, &name, nil) }

// Restart stops the process and launches it again. Changing agent,
// profile or yolo always takes a full restart.
func (m *Manager) Restart(id string, req RestartRequest) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	if req.Mode != "" && !agent.ValidMode(req.Mode) {
		return Info{}, ErrBadMode
	}
	if req.AgentSessionID != "" && !validSessionID(req.AgentSessionID) {
		return Info{}, ErrBadRequest
	}
	s.syncModel()
	sp := s.Spec()
	a := m.agentFor(sp)
	left := m.runToRemember(s, sp, a)
	pinned := false
	if req.Agent != "" && req.Agent != sp.Agent {
		na, ok := m.Agents.Get(req.Agent)
		if !ok {
			return Info{}, ErrUnknownPreset
		}
		if na.Kind == agent.KindAgent && m.Settings().Agents.Disabled[na.ID] {
			return Info{}, ErrAgentDisabled
		}
		if na.Kind == agent.KindAgent && (sp.MachineID != machine.LocalID || (sp.RemoteMachineID != "" && !na.WorksOnServers())) {
			return Info{}, ErrAgentThisPC
		}
		if sp.Name == a.Name { // a name the user chose stays
			sp.Name = na.Name
		}
		sp.PastRuns = rememberRun(sp.PastRuns, left)
		a = na
		sp.Agent, sp.Preset, sp.AgentKind = na.ID, na.ID, na.Kind
		sp.Command, sp.Args, sp.Env = na.Command, append([]string{}, na.Args...), append([]string{}, na.Env...)
		sp.ResumeArgs = append([]string{}, na.Resume.ContinueArgs...)
		sp.AgentSessionID = ""
		if req.ProfileID == nil {
			sp.ProfileID = m.defaultProfile(sp.MachineID, na)
		}
		sp.Yolo = sp.Yolo && na.HasYolo()
		// a conversation left earlier comes back with its model, anything else with the default
		c := m.Settings().Agents.ModelFor("", na.ID)
		sp.Model, sp.Effort = c.Model, c.Effort
		for _, r := range sp.PastRuns {
			if r.Agent == na.ID && req.AgentSessionID != "" && r.AgentSessionID == req.AgentSessionID {
				sp.Model, sp.Effort = r.Model, r.Effort
			}
		}
		pinned = true
	}
	if req.Model != nil || req.Effort != nil {
		model, effort, err := chooseModel(a, req.Model, req.Effort, sp.Model, sp.Effort)
		if err != nil {
			return Info{}, err
		}
		sp.Model, sp.Effort, pinned = model, effort, true
	}
	// "" asks for the default. Going back to a conversation keeps the login it was
	// made under: another one would not find it.
	if req.ProfileID != nil && *req.ProfileID == "" && req.AgentSessionID == "" {
		def := m.defaultProfile(sp.MachineID, a)
		req.ProfileID = &def
	}
	if req.ProfileID != nil {
		if err := m.validateProfile(*req.ProfileID, sp.MachineID, a); err != nil {
			return Info{}, err
		}
		if a.ID == left.Agent && *req.ProfileID != sp.ProfileID { // the same CLI, another login
			sp.PastRuns = rememberRun(sp.PastRuns, left)
		}
		sp.ProfileID = *req.ProfileID
	}
	backToLogin := func() {
		sp.ProviderID = ""
		if req.ProfileID == nil {
			sp.ProfileID = m.defaultProfile(sp.MachineID, a)
		}
		if req.Model == nil {
			c := m.Settings().Agents.ModelFor("", a.ID)
			sp.Model, sp.Effort = c.Model, c.Effort
		}
	}
	switch {
	case req.ProviderID != nil && *req.ProviderID != "":
		if err := m.checkProvider(*req.ProviderID, a); err != nil {
			return Info{}, err
		}
		if (*req.ProviderID != sp.ProviderID || a.ID != left.Agent) && req.Model == nil {
			sp.Model = "" // the provider's own
		}
		sp.ProviderID, sp.ProfileID = *req.ProviderID, ""
	case req.ProviderID != nil:
		if sp.ProviderID != "" {
			backToLogin()
		}
	case sp.ProviderID != "":
		switch err := m.checkProvider(sp.ProviderID, a); {
		case errors.Is(err, ErrProviderBudget):
			// never moved to the subscription quietly
			return Info{}, err
		case err != nil:
			// another agent that cannot run on it, or a provider that is gone
			backToLogin()
		case a.ID != left.Agent && req.Model == nil:
			sp.Model = ""
		}
	}
	if req.Yolo != nil {
		sp.Yolo = *req.Yolo && a.HasYolo()
	}
	mode := req.Mode
	switch {
	case mode != "":
	case req.AgentSessionID != "", req.Resume != nil && *req.Resume:
		mode = agent.ModeResume
	default:
		mode = agent.ModeFresh
	}
	switchConv := req.AgentSessionID != "" && req.AgentSessionID != sp.AgentSessionID
	if req.AgentSessionID != "" {
		sp.AgentSessionID = req.AgentSessionID
	}
	if mode != agent.ModeFresh { // back in that conversation: it is no longer left
		sp.PastRuns = slices.DeleteFunc(slices.Clone(sp.PastRuns), func(r PastRun) bool {
			return r.Agent == sp.Agent && (r.AgentSessionID == sp.AgentSessionID || r.AgentSessionID == "")
		})
	}
	s.mu.Lock()
	changedAgent := s.spec.Agent != sp.Agent
	if switchConv {
		s.priv.TranscriptPath = ""
		s.reply = nil
	}
	s.spec = sp
	s.resumeOffer = false
	if changedAgent {
		s.priv.TranscriptPath = ""
		s.reply = nil
	}
	if pinned || switchConv {
		s.priv.ModelAt = time.Now().UTC()
	}
	s.mu.Unlock()
	m.saveNow()
	go func() {
		s.launchMu.Lock()
		defer s.launchMu.Unlock()
		s.stop()
		s.launch(mode)
	}()
	return m.SessionInfo(s), nil
}

// TestSetTranscript marks a console as having had a conversation, for tests of
// what happens to it next.
func (m *Manager) TestSetTranscript(id, path string) {
	if s, ok := m.Get(id); ok {
		s.mu.Lock()
		s.priv.TranscriptPath = path
		s.mu.Unlock()
	}
}

// maxPastRuns bounds what a console remembers of its earlier agents.
const maxPastRuns = 8

// runToRemember is the conversation a switch would leave; Agent "" when there is
// nothing to come back to.
func (m *Manager) runToRemember(s *Session, sp Spec, a agent.Agent) PastRun {
	if sp.AgentKind != agent.KindAgent {
		return PastRun{}
	}
	s.mu.Lock()
	hadTurn := s.reply != nil || s.priv.TranscriptPath != ""
	s.mu.Unlock()
	switch {
	case a.Resume.Kind == agent.ResumeSessionID && (sp.AgentSessionID == "" || !hadTurn):
		return PastRun{} // the CLI could not find a conversation that never began
	case a.Resume.Kind == agent.ResumeNone:
		return PastRun{}
	}
	return PastRun{Agent: sp.Agent, AgentSessionID: sp.AgentSessionID, Model: sp.Model, Effort: sp.Effort, ProfileID: sp.ProfileID, ProviderID: sp.ProviderID, At: time.Now().UTC()}
}

func rememberRun(list []PastRun, r PastRun) []PastRun {
	if r.Agent == "" {
		return list
	}
	out := []PastRun{r}
	for _, o := range list {
		if (o.Agent != r.Agent || o.AgentSessionID != r.AgentSessionID) && len(out) < maxPastRuns {
			out = append(out, o)
		}
	}
	return out
}

// StartAgent is §3.3 (restart / resume from the shell without a restart).
func (m *Manager) StartAgent(ctx context.Context, id, mode, agentSessionID string) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	switch mode {
	case agent.ModeFresh, agent.ModeResume, agent.ModeContinue, agent.ModePick:
	default:
		return Info{}, ErrBadMode
	}
	if agentSessionID != "" && !validSessionID(agentSessionID) {
		return Info{}, ErrBadRequest
	}
	if err := s.StartAgent(ctx, mode, agentSessionID); err != nil {
		return m.SessionInfo(s), err
	}
	return m.SessionInfo(s), nil
}

// Kill stops the process but keeps the node.
func (m *Manager) Kill(id string) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	s.launchMu.Lock()
	s.stop()
	s.launchMu.Unlock()
	s.mu.Lock()
	if s.status == StatusReconnecting || s.status == StatusStarting {
		s.gen++ // cancel pending relaunch
		s.status = StatusExited
		s.eng.SetProc(StatusExited, time.Now())
	}
	s.mu.Unlock()
	m.changed(s)
	return m.SessionInfo(s), nil
}

// Delete kills the process and removes the node (its handle is freed).
func (m *Manager) Delete(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrNotFound
	}
	s.launchMu.Lock()
	s.stop()
	s.mu.Lock()
	s.deleted = true
	s.gen++
	for sub := range s.subs {
		sub.close()
	}
	s.subs = map[*Sub]struct{}{}
	s.mu.Unlock()
	s.launchMu.Unlock()
	s.scr.Close()

	m.mu.Lock()
	delete(m.sessions, id)
	for i, x := range m.order {
		if x == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	_ = os.Remove(m.scrollbackPath(id))
	m.saveNow()
	if m.OnRemove != nil {
		m.OnRemove(s.Spec())
	}
	return nil
}

// Assign moves a session to a project/workspace (migration and repair of
// sessions whose workspace no longer exists).
func (m *Manager) Assign(id, projectID, workspaceID string) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrNotFound
	}
	s.mu.Lock()
	s.spec.ProjectID, s.spec.WorkspaceID = projectID, workspaceID
	s.mu.Unlock()
	m.saveNow()
	m.changed(s)
	return nil
}

// WorkspaceOf returns the workspace a live session belongs to.
func (m *Manager) WorkspaceOf(id string) (string, bool) {
	s, ok := m.Get(id)
	if !ok {
		return "", false
	}
	return s.Spec().WorkspaceID, true
}

// Placement is where a session lives and whether its process runs.
type Placement struct {
	ID          string
	ProjectID   string
	WorkspaceID string
	Running     bool
}

// Placements lists the placement of every session on a board, in creation order.
func (m *Manager) Placements() []Placement { return m.placements(false) }

// AllPlacements adds the sessions with no board node: the editor's terminals and
// agents, which go when their board does.
func (m *Manager) AllPlacements() []Placement { return m.placements(true) }

func (m *Manager) placements(panels bool) []Placement {
	all := m.all()
	out := make([]Placement, 0, len(all))
	for _, s := range all {
		s.mu.Lock()
		if s.spec.Panel && !panels {
			s.mu.Unlock()
			continue
		}
		out = append(out, Placement{ID: s.spec.ID, ProjectID: s.spec.ProjectID, WorkspaceID: s.spec.WorkspaceID, Running: s.status == StatusRunning})
		s.mu.Unlock()
	}
	return out
}

// CountByMachine counts sessions on a machine.
func (m *Manager) CountByMachine(machineID string) int {
	n := 0
	for _, s := range m.all() {
		if s.Spec().MachineID == machineID {
			n++
		}
	}
	return n
}

// TmuxNames returns the tmux session names owned by nodes on a machine.
func (m *Manager) TmuxNames(machineID string) map[string]bool {
	out := map[string]bool{}
	for _, s := range m.all() {
		sp := s.Spec()
		if sp.MachineID == machineID && sp.TmuxName != "" {
			out[sp.TmuxName] = true
		}
	}
	return out
}

// AttachedTo returns the session currently holding a conversation id.
func (m *Manager) AttachedTo(machineID, agentSessionID string) string {
	for _, s := range m.all() {
		sp := s.Spec()
		if sp.MachineID == machineID && sp.AgentSessionID == agentSessionID && agentSessionID != "" {
			return sp.ID
		}
	}
	return ""
}

// claimed reports whether another console already uses a transcript.
func (m *Manager) claimed(self *Session, p string) bool {
	for _, s := range m.all() {
		if s == self {
			continue
		}
		s.mu.Lock()
		hit := s.priv.TranscriptPath == p
		s.mu.Unlock()
		if hit {
			return true
		}
	}
	return false
}

// isAgentCommand reports whether a foreground job name is a catalog CLI.
func (m *Manager) isAgentCommand(comm string) bool {
	if comm == "" {
		return false
	}
	for _, a := range m.Agents.All() {
		if a.Kind == agent.KindAgent && a.Detect != "" && baseName(a.Detect) == comm {
			return true
		}
	}
	return false
}

// HookHandler serves POST /nb/hook, the HTTP fallback of the marker
// channel (loopback listener on This PC, reverse forward on SSH). It
// always answers 204 so a wrong token reveals nothing.
func (m *Manager) HookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/nb/hook" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, marker.MaxLen))
		if err == nil {
			if mk, err := marker.Parse(body); err == nil {
				for _, s := range m.all() {
					s.mu.Lock()
					ok := marker.TokenOK(mk.Payload.K, s.priv.HookToken)
					s.mu.Unlock()
					if ok {
						s.external(mk)
						break
					}
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func (m *Manager) scrollbackPath(id string) string {
	return m.st.Path("scrollback", id+".log")
}

func (m *Manager) saveSoon() {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	if m.saveTimer != nil {
		return
	}
	m.saveTimer = time.AfterFunc(500*time.Millisecond, func() {
		m.saveMu.Lock()
		m.saveTimer = nil
		m.saveMu.Unlock()
		m.saveNow()
	})
}

func (m *Manager) saveNow() {
	list := []stored{}
	for _, s := range m.all() {
		list = append(list, s.stored())
	}
	_ = m.st.Save(sessionsFile, list)
}

func (m *Manager) flushScrollback(all bool) {
	for _, s := range m.all() {
		s.mu.Lock()
		if !s.dirty && !all {
			s.mu.Unlock()
			continue
		}
		b := s.ring.Bytes()
		s.dirty = false
		id := s.spec.ID
		s.mu.Unlock()
		_ = store.WriteFileAtomic(m.scrollbackPath(id), b)
	}
}

// Shutdown persists everything, then stops processes: local ones get
// SIGHUP (they are relaunched per resumeOnRestart on next start); remote
// channels are closed, which leaves tmux sessions running.
func (m *Manager) Shutdown() {
	if m.shutting.Swap(true) {
		return
	}
	close(m.stopTicker)
	m.flushScrollback(true)
	m.saveNow()
	var wg sync.WaitGroup
	for _, s := range m.all() {
		s.mu.Lock()
		r := s.cur
		local := s.spec.MachineID == machine.LocalID
		s.mu.Unlock()
		if r == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !local {
				r.proc.Close()
				return
			}
			_ = r.proc.Signal("HUP")
			select {
			case <-r.done:
			case <-time.After(2 * time.Second):
				_ = r.proc.Signal("KILL")
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	for _, s := range m.all() {
		s.scr.Close()
	}
}
