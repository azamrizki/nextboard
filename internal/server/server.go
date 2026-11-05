// Package server is the hub's HTTPS front end: REST API, WebSockets and the
// embedded SPA.
package server

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"nextboard/internal/agent"
	"nextboard/internal/agentstate"
	"nextboard/internal/auth"
	"nextboard/internal/board"
	"nextboard/internal/config"
	"nextboard/internal/connlog"
	"nextboard/internal/delivery"
	"nextboard/internal/gui"
	"nextboard/internal/idelink"
	"nextboard/internal/jobs"
	"nextboard/internal/machine"
	"nextboard/internal/mcpreg"
	"nextboard/internal/orch"
	"nextboard/internal/preview"
	"nextboard/internal/profiles"
	"nextboard/internal/project"
	"nextboard/internal/review"
	"nextboard/internal/secret"
	"nextboard/internal/session"
	"nextboard/internal/settings"
	"nextboard/internal/store"
	"nextboard/internal/studio"
	"nextboard/internal/uptime"
	"nextboard/internal/vault"
	"nextboard/internal/version"
)

// Options configure Run (command-line overrides of config.json).
type Options struct {
	DataDir string
	Listen  string
	// Dev accepts WebSocket Origin http://localhost:5173 (Vite dev server).
	Dev bool
	// DevWebPort is the Vite port accepted in Dev mode (default 5173).
	DevWebPort int
	// PrintSetup prints a fresh setup link to stdout when setup is needed.
	PrintSetup bool
}

// Server wires every component together.
type Server struct {
	st         *store.Store
	cfgMu      sync.Mutex
	cfg        *config.Config
	auth       *auth.Store
	machines   *machine.Registry
	sessions   *session.Manager
	uptime     *uptime.Service
	credVault  *vault.Service
	projects   *project.Store
	boards     *board.Manager
	agents     *agent.Store
	settings   *settings.Store
	extensions *studio.Extensions
	ide        *idelink.Hub
	profiles   *profiles.Store
	mcp        *mcpreg.Store
	jobs       *jobs.Manager
	deliver    *delivery.Engine
	orch       *orch.Service
	attn       *attentionRing
	events     *Broker
	conns      *connRegistry
	dev        bool
	devWeb     int // Vite port accepted in dev mode

	listen string

	limiter  *auth.RateLimiter
	mirror   inputMirror
	connLog  *connlog.Log
	stopBeat chan struct{}
	// remoteJobs: token of a running job → the server it may read (§21).
	remoteJobs sync.Map
	mux        *http.ServeMux
	spa        http.Handler

	// running remembers each session's last running state, so project
	// events fire only when sessionCount/runningCount change.
	runMu   sync.Mutex
	running map[string]bool

	// hookLn is the loopback HTTP fallback of the hook channel on This PC.
	hookLn  net.Listener
	hookURL string

	// gitPrev is the Phase 7 git service and preview proxy (gitpreview.go).
	gitPrev *gitPreview
	review  *review.Manager
	// studio holds the Phase 7 studios and usage (studio.go).
	studio *studioHub
	// gui runs desktop apps of This PC for board tiles (h_gui.go).
	gui *gui.Manager
}

// New loads all state from the data directory.
func New(opts Options) (*Server, error) {
	st, err := store.Open(opts.DataDir)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(st)
	if err != nil {
		return nil, err
	}
	if opts.Listen != "" {
		cfg.Listen = opts.Listen
	}
	box, err := secret.Open(st.Path("secret.key"))
	if err != nil {
		return nil, err
	}
	as, err := auth.Open(st)
	if err != nil {
		return nil, err
	}
	reg, err := machine.OpenRegistry(st, box)
	if err != nil {
		return nil, err
	}
	sets, err := settings.Open(st)
	if err != nil {
		return nil, err
	}
	mcpServers, err := mcpreg.Open(st, box)
	if err != nil {
		return nil, err
	}
	profs, err := profiles.Open(st)
	if err != nil {
		return nil, err
	}
	connLog, err := connlog.Open(st.Path("logs"))
	if err != nil {
		log.Printf("log koneksi tidak tersedia: %v", err)
	}
	mgr := session.NewManager(st, reg)
	mgr.Settings, mgr.Profiles = sets.Get, profs
	if err := mgr.Load(); err != nil {
		return nil, err
	}
	projects, err := project.Open(st)
	if err != nil {
		return nil, err
	}
	s := &Server{
		st: st, cfg: cfg, auth: as, machines: reg, sessions: mgr, connLog: connLog,
		projects: projects, boards: board.NewManager(st),
		mcp: mcpServers, agents: mgr.Agents, settings: sets, extensions: studio.NewExtensions(st.Path("extensions")), profiles: profs, jobs: jobs.NewManager(st, reg.Host), attn: newAttentionRing(),
		events: NewBroker(), conns: newConnRegistry(), dev: opts.Dev, devWeb: cmp.Or(opts.DevWebPort, 5173),
		listen:  cfg.Listen,
		limiter: auth.NewRateLimiter(5, 10*time.Minute),
		running: map[string]bool{},
	}
	if err := s.startHookListener(); err != nil {
		log.Printf("listener hook lokal gagal: %v", err)
	}
	s.wire()
	s.setupOrchestration()
	s.setupGitPreview()
	s.setupReview()
	s.setupStudio()
	s.setupGUI()
	s.setupAttachments()
	s.setupVault()
	if err := s.setupUptime(); err != nil {
		return nil, err
	}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrasi project: %w", err)
	}
	s.routes()
	s.spa = spaHandler()
	if err := s.ensureAgentHomes(); err != nil {
		return nil, err
	}
	return s, nil
}

// wire connects component callbacks to the event broker.
func (s *Server) wire() {
	s.sessions.HubName = s.hubName
	s.sessions.OnChange = func(in session.Info) {
		s.events.Publish(map[string]any{"type": "session", "session": in})
		s.trackSession(in)
		if s.orch != nil {
			s.orch.SessionChanged(in)
		}
	}
	s.sessions.OnRemove = func(sp session.Spec) {
		_ = os.Remove(s.st.Path("agents", "prompts", sp.ID+".md"))
		s.events.Publish(map[string]any{"type": "removed", "id": sp.ID})
		if s.deliver != nil {
			s.deliver.SessionRemoved(sp.ID)
			s.orch.SessionRemoved(sp.ID)
		}
		s.untrackSession(sp.ID)
		if sp.WorkspaceID != "" {
			_ = s.boards.RemoveTerminal(sp.WorkspaceID, sp.ID)
		}
		s.emitProject(sp.ProjectID)
	}
	s.boards.SessionWorkspace = s.sessions.WorkspaceOf
	s.boards.Names = func(wid string) (string, string) {
		ws, p, _ := s.projects.Workspace(wid)
		return p.Name, ws.Name
	}
	s.boards.OnOps = func(wid string, rev int64, clientID string, ops []board.Op) {
		s.events.Publish(map[string]any{"type": "boardOps", "workspaceId": wid, "rev": rev, "clientId": clientID, "ops": ops})
	}
	s.machines.SessionCount = s.sessions.CountByMachine
	s.machines.PresetCommands = s.detectCommands
	s.machines.HookHandler = s.sessions.HookHandler()
	s.machines.WantForward = func() bool { return s.settings.Get().Agents.HookChannel != "tty" }
	s.sessions.HookURL = s.hookURLFor
	s.machines.Log = s.connLog.Write
	s.sessions.OnChannelLost = func(sp session.Spec) {
		name := ""
		if h, ok := s.machines.Host(sp.MachineID); ok {
			name = h.Info().Name
		}
		s.connLog.Write(connlog.Event{Kind: connlog.ChannelLost, MachineID: sp.MachineID, Machine: name, SessionID: sp.ID})
	}
	// a console works without them, only its agent may sign its commits
	if dir := s.st.Path("agents", "git-hooks"); agent.WriteCommitHooks(dir) != nil {
		log.Printf("hook commit agen %s tidak bisa ditulis", dir)
	} else {
		s.sessions.CommitHooks = dir
	}
	s.sessions.RemoteWorkDir, s.sessions.LaunchContext = s.remoteWorkDir, s.launchContext
	s.ide = idelink.New(idelink.Options{
		WorkspaceOf: func(id string) string { w, _ := s.sessions.WorkspaceOf(id); return w },
		NameOf:      s.ideConsoleName,
		Version:     version.Version,
	})
	s.sessions.IDELink = s.ideEnv
	if prev := s.sessions.OnRemove; prev != nil {
		s.sessions.OnRemove = func(sp session.Spec) {
			prev(sp)
			s.ide.Release(sp.ID)
		}
	}
	s.sessions.OnAttention = func(in session.Info, ev agentstate.Event) {
		s.onAttention(in, ev)
		s.orchAttention(in, ev)
	}
	s.jobs.Publish = s.events.Publish
	s.settings.OnChange = func(v settings.Settings) {
		s.events.Publish(map[string]any{"type": "settings", "settings": v})
		s.machines.RefreshForwards()
	}
	s.machines.OnChange = func(in machine.Info) {
		s.events.Publish(map[string]any{"type": "machine", "machine": in})
	}
	s.machines.OnRemoved = func(id string) {
		s.events.Publish(map[string]any{"type": "machineRemoved", "id": id})
	}
	s.machines.OnConnect = s.sessions.MachineConnected
	s.auth.OnDevice = func(d auth.Device, removed bool) {
		if removed {
			s.conns.kill(d.ID)
			s.events.Publish(map[string]any{"type": "deviceRemoved", "id": d.ID})
			return
		}
		s.events.Publish(map[string]any{"type": "device", "device": s.deviceView(d)})
	}
	s.conns.onChange = func(deviceID string) {
		if d, ok := s.auth.Device(deviceID); ok {
			s.events.Publish(map[string]any{"type": "device", "device": s.deviceView(d)})
		}
	}
}

func (s *Server) hubName() string {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.cfg.HubName
}

func (s *Server) urls() []string {
	s.cfgMu.Lock()
	extra := append([]string{}, s.cfg.ExtraURLs...)
	s.cfgMu.Unlock()
	return HubURLs(s.listen, extra)
}

// urlsFor lists the addresses to give a client on request r: the origin of
// a client that came through the HTTPS reverse proxy first, then the rest
// of AdvertisedURLs.
func (s *Server) urlsFor(r *http.Request) []string {
	urls := AdvertisedURLs(s.urls())
	if preview.SchemeOf(r) != "https" {
		return urls
	}
	out := []string{"https://" + r.Host}
	for _, u := range urls {
		if strings.HasPrefix(u, "https://") && u != out[0] {
			out = append(out, u)
		}
	}
	return out
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context, opts Options) error {
	ln, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.noteHubStart()
	s.machines.Start()
	s.sessions.Start()
	s.startOrchestration()
	s.startStudio()
	s.uptime.Start()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	log.Printf("NextBoard %s berjalan di http://%s", version.Version, s.listen)
	if !isLoopbackListen(s.listen) {
		log.Printf("PERINGATAN: hub melayani HTTP tanpa enkripsi di %s. Password, token, dan isi terminal bisa disadap di jaringan ini. Untuk deploy: listen 127.0.0.1 dan pasang HTTPS di nginx (deploy/nginx.conf.example).", s.listen)
	}
	for _, u := range s.urls() {
		log.Printf("  %s", u)
	}
	s.announceSetup(opts.PrintSetup)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			s.shutdown()
			return err
		}
	}
	log.Printf("mematikan hub…")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	s.shutdown()
	return nil
}

func (s *Server) shutdown() {
	s.uptime.Close()
	s.stopOrchestration()
	s.closeGitPreview()
	s.closeStudio()
	s.gui.Close()
	s.jobs.Close()
	if s.hookLn != nil {
		s.hookLn.Close()
	}
	s.sessions.Shutdown()
	s.review.Close()
	s.ide.Close()
	s.boards.Close()
	s.machines.Close()
	s.auth.Flush()
	s.noteHubStop()
}

// hubMarker exists while the hub runs and is touched every minute. Finding one
// at start means the last run never stopped, and when it was last alive.
func (s *Server) hubMarker() string { return s.st.Path("logs", "hub.running") }

func (s *Server) noteHubStart() {
	marker := s.hubMarker()
	if fi, err := os.Stat(marker); err == nil {
		seen := fi.ModTime().UTC()
		s.connLog.Write(connlog.Event{Kind: connlog.HubUnclean, LastSeen: &seen})
	}
	_ = os.WriteFile(marker, nil, 0o600)
	s.connLog.Write(connlog.Event{Kind: connlog.HubStart, Version: version.Version, PID: os.Getpid()})
	s.stopBeat = make(chan struct{})
	go func(stop <-chan struct{}) {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				_ = os.Chtimes(marker, now, now)
			}
		}
	}(s.stopBeat)
}

func (s *Server) noteHubStop() {
	if s.stopBeat == nil {
		return
	}
	close(s.stopBeat)
	s.stopBeat = nil
	s.connLog.Write(connlog.Event{Kind: connlog.HubStop})
	_ = os.Remove(s.hubMarker())
}

// announceSetup prints (TTY) or logs how to complete first-time setup.
// Setup codes are never written to the log.
func (s *Server) announceSetup(print bool) {
	if !s.auth.NeedsSetup() {
		return
	}
	if print {
		code, _, err := s.auth.NewSetupCode()
		if err == nil {
			fmt.Fprintf(os.Stdout, "\nHub belum di-setup. Buka link setup ini (sekali pakai, berlaku 30 menit):\n  %s/setup?code=%s\n\n",
				PrimaryURL(s.listen, nil), code)
			return
		}
	}
	log.Printf("hub belum di-setup: jalankan `nextboard setup-link` untuk membuat link setup")
}

// ServeHTTP applies global headers, CSRF and body limits.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.URL.Path == guiViewerPath {
			h.Set("X-Frame-Options", "SAMEORIGIN")
			h.Set("Content-Security-Policy", guiViewerCSP)
			h.Set("Cache-Control", "no-cache")
		} else {
			h.Set("Content-Security-Policy", s.spaCSP(preview.SchemeOf(r), r.Host))
		}
		s.spa.ServeHTTP(w, r)
		return
	}
	h.Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		if r.Header.Get("X-NextBoard") != "1" {
			writeErr(w, http.StatusForbidden, "header X-NextBoard: 1 wajib ada")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, bodyLimit(r.URL.Path))
	}
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: 200}
	s.mux.ServeHTTP(rec, r)
	// Path only: queries may carry access tokens.
	if rec.status >= 400 && rec.status != http.StatusUnauthorized {
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	}
}

// startHookListener opens the loopback HTTP fallback for hooks on This PC
// (docs/API-agents.md §4.5): 127.0.0.1 only, POST /nb/hook, token checked.
func (s *Server) startHookListener() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.hookLn = ln
	s.hookURL = "http://" + ln.Addr().String() + "/nb/hook"
	mux := http.NewServeMux()
	mux.Handle("/nb/hook", s.sessions.HookHandler())
	mux.HandleFunc("/nb/mcp/{sid}", s.handleRemoteMCP)
	mux.HandleFunc("/nb/mcp-job", s.handleRemoteJobMCP)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// hookURLFor is NB_HOOK_URL for consoles started on a machine now.
// ensureAgentHomes gives every built-in agent with a config folder its state
// folder in the NextBoard data folder and makes it the default once, so the
// agents share one login and memory and the user's own CLI setup stays apart.
// A default the user turned off later stays off.
func (s *Server) ensureAgentHomes() error {
	for _, a := range s.agents.All() {
		if a.Kind != agent.KindAgent || a.Source != agent.SourceBuiltin || a.ProfileEnv == "" {
			continue
		}
		pf, created, err := s.profiles.EnsureHome(machine.LocalID, a.ID)
		if err != nil {
			return err
		}
		if !created || s.settings.Get().Agents.Profiles[machine.LocalID][a.ID] != "" {
			continue
		}
		patch, _ := json.Marshal(map[string]any{"agents": map[string]any{"profiles": map[string]any{machine.LocalID: map[string]string{a.ID: pf.ID}}}})
		if _, err := s.settings.Patch(patch); err != nil {
			return err
		}
	}
	for _, pf := range s.profiles.List(machine.LocalID) {
		s.noLSPOffer(pf)
	}
	return nil
}

// noLSPOffer keeps the CLI's plugin offer out of a Claude Code folder the hub made.
func (s *Server) noLSPOffer(pf profiles.Profile) {
	if !pf.CreatedByHub || pf.MachineID != machine.LocalID {
		return
	}
	if a, ok := s.agents.Get(pf.Agent); !ok || a.CapBase() != "claude" {
		return
	}
	if err := profiles.NoLSPOffer(pf.Dir); err != nil {
		log.Printf("tawaran plugin LSP agen: %v", err)
	}
}

// defaultProfileEnv is the env of the default profile of an agent on This PC.
func (s *Server) defaultProfileEnv(agentID string) []string {
	id := s.settings.Get().Agents.Profiles[machine.LocalID][agentID]
	pf, ok := s.profiles.Get(id)
	a, aok := s.agents.Get(agentID)
	if !ok || !aok || pf.MachineID != machine.LocalID || a.ProfileEnv == "" {
		return nil
	}
	return []string{a.ProfileEnv + "=" + pf.Dir}
}

func (s *Server) hookURLFor(machineID string) string {
	if machineID == machine.LocalID {
		return s.hookURL
	}
	if m, ok := s.machines.SSH(machineID); ok {
		return m.HookURL()
	}
	return ""
}

// detectCommands lists what the probe looks for: every agent's detect
// command plus path overrides from settings.
func (s *Server) detectCommands() []string {
	var over []string
	for _, per := range s.settings.Get().Agents.Paths {
		for _, p := range per {
			over = append(over, p)
		}
	}
	return agent.DetectCommands(s.agents.All(), over)
}

// bodyLimit is 64 KB except for the few routes that carry content.
func bodyLimit(p string) int64 {
	switch {
	case strings.HasPrefix(p, "/api/workspaces/") && strings.HasSuffix(p, "/board/ops"):
		return 2 << 20
	case p == "/api/assets", strings.HasSuffix(p, "/attach") && (strings.HasPrefix(p, "/api/sessions/") || strings.HasPrefix(p, "/api/projects/")):
		return 20 << 20
	case strings.HasPrefix(p, "/api/machines/") && strings.HasSuffix(p, "/fs/write"):
		return 28 << 20 // 20 MB as base64, as JSON
	case strings.HasSuffix(p, "/deliveries"), strings.HasSuffix(p, "/memory"), strings.HasSuffix(p, "/memory/accept"),
		strings.HasPrefix(p, "/api/memory/docs/"), strings.HasSuffix(p, "/memory/init/apply"):
		return 512 << 10 // 32 000 / 64 KB of multi-byte text as JSON
	case strings.HasPrefix(p, "/api/machines/") && (strings.HasSuffix(p, "/skills") || strings.HasSuffix(p, "/skills/item") || strings.HasSuffix(p, "/skills/lint")):
		return 2 << 20 // skill content ≤ 512 KB as JSON
	case p == "/api/vault/batch":
		return 16 << 20 // an import, in chunks of at most this
	case strings.HasPrefix(p, "/api/vault/items/"):
		return 128 << 10 // one item, at most 64 KB as base64
	case strings.HasPrefix(p, "/api/projects/") && (strings.HasSuffix(p, "/eslint") || strings.HasSuffix(p, "/git/blame")):
		return 8 << 20 // an editor's unsaved text, up to the 1 MB it can edit, as JSON
	}
	return 64 << 10
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack/Flush pass through for WebSockets.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack tidak didukung")
	}
	return hj.Hijack()
}
