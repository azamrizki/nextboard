// Package usage is the usage pill (docs/API-studio.md §10, F2), the reset
// chime (F3) and the token history chart from transcripts (F4). Checks run
// the CLI's own usage screen in a hidden terminal (best-effort parsing of a
// VT screen model); token history only reads transcript files.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/screen"
	"github.com/azamrizki/nextboard/internal/store"
)

const reportsFile = "usage/reports.json"

// Report is the API view of one check (UsageReport).
type Report struct {
	MachineID string    `json:"machineId"`
	Agent     string    `json:"agent"`
	ProfileID string    `json:"profileId"`
	CheckedAt time.Time `json:"checkedAt"`
	Trigger   string    `json:"trigger"`
	State     string    `json:"state"` // ok | needsInteractive | notLoggedIn | unsupported | error
	Windows   []Window  `json:"windows"`
	Facts     []Fact    `json:"facts"`
	Raw       []string  `json:"raw"`
	Parsed    bool      `json:"parsed"`
	Error     string    `json:"error"`
}

func (r Report) key() string { return key(r.MachineID, r.Agent, r.ProfileID) }

func key(m, a, p string) string { return m + "|" + a + "|" + p }

var (
	ErrOffline     = errors.New("machine tidak tersambung")
	ErrBackground  = errors.New("pemeriksaan usage latar dimatikan di settings")
	ErrDisabled    = errors.New("pemeriksaan usage untuk agen ini dimatikan di settings")
	ErrUnsupported = errors.New("agen ini tidak punya pemeriksaan usage")
)

// Settings is the part of settings.usage the service reads.
type Settings struct {
	Background   bool
	Disabled     map[string]bool // agent id → checks switched off
	ChimeEnabled bool
	ChimePlay    string // after100 | always
	ChimeSound   string
}

// Profile is a resolved login profile for a check.
type Profile struct {
	Env string // CLAUDE_CONFIG_DIR | CODEX_HOME
	Dir string
}

// Hub is what the service needs from the rest of the hub.
type Hub interface {
	Host(machineID string) (machine.Host, bool)
	Settings() Settings
	Publish(v any)
	// Profile resolves profileID for (machine, agent); "" = default login.
	Profile(machineID, agentID, profileID string) (Profile, error)
	// Command is the path override of agent on machine ("" = default).
	Command(machineID, agentID string) string
	// ProfileDirs lists every profile folder of a machine per agent (token
	// history covers the default login plus all of them).
	ProfileDirs(machineID string) map[string][]string
	// MachineIDs lists every machine (background token refresh).
	MachineIDs() []string
}

// CheckRequest is POST /api/usage/check.
type CheckRequest struct {
	MachineID string `json:"machineId"`
	Agent     string `json:"agent"`
	ProfileID string `json:"profileId"`
	MaxAgeSec *int   `json:"maxAgeSec"`
	Trigger   string `json:"trigger"`
}

// Service owns reports, checks, chime timers and the token index.
type Service struct {
	hub  Hub
	st   *store.Store
	jobs *jobs.Manager
	tmp  *tempDirs

	mu      sync.Mutex
	reports map[string]Report
	running map[string]string        // report key → job id
	machine map[string]chan struct{} // one check per machine (fixed folder)
	hubSem  chan struct{}            // ≤ 2 checks per hub

	chime  *chime
	tokens *tokenIndex
	// OnScreen (real-CLI capture) sees every distinct screen of a check.
	OnScreen func(agentID, step string, lines []string)
}

// New loads usage/reports.json.
func New(st *store.Store, jm *jobs.Manager, hub Hub, clock Clock) *Service {
	if clock == nil {
		clock = realClock{}
	}
	s := &Service{hub: hub, st: st, jobs: jm, tmp: &tempDirs{st: st},
		reports: map[string]Report{}, running: map[string]string{}, machine: map[string]chan struct{}{},
		hubSem: make(chan struct{}, 2)}
	var list []Report
	if _, err := st.Load(reportsFile, &list); err == nil {
		for _, r := range list {
			s.reports[r.key()] = r
		}
	}
	s.chime = newChime(clock, func() Settings { return hub.Settings() }, hub.Publish)
	s.tokens = newTokenIndex(st, hub)
	return s
}

// Start arms the reset timers of the stored reports (recomputed at start).
func (s *Service) Start() {
	s.mu.Lock()
	list := make([]Report, 0, len(s.reports))
	for _, r := range s.reports {
		list = append(list, r)
	}
	s.mu.Unlock()
	for _, r := range list {
		s.chime.arm(r)
	}
	s.tokens.start()
}

// Close stops timers and the background token refresh.
func (s *Service) Close() {
	s.chime.stopAll()
	s.tokens.stop()
}

// MachineConnected removes usage folders a crashed hub left behind on
// that machine (only the recorded paths).
func (s *Service) MachineConnected(machineID string) {
	h, ok := s.hub.Host(machineID)
	if !ok {
		return
	}
	go s.tmp.cleanup(context.Background(), h)
}

// Pending lists recorded usage folders (tests).
func (s *Service) Pending() []string {
	var out []string
	for _, e := range s.tmp.Pending() {
		out = append(out, e.MachineID+":"+e.Path)
	}
	return out
}

// Reports lists cached reports, filtered by non-empty fields.
func (s *Service) Reports(machineID, agentID, profileID string, filterProfile bool) []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Report{}
	for _, r := range s.reports {
		if (machineID != "" && r.MachineID != machineID) || (agentID != "" && r.Agent != agentID) || (filterProfile && r.ProfileID != profileID) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// Check returns a fresh enough cached report, or starts (or joins) a check
// job. Unsupported agents get an "unsupported" report without a job.
func (s *Service) Check(req CheckRequest) (*Report, *jobs.Job, error) {
	if req.MachineID == "" {
		req.MachineID = machine.LocalID
	}
	if req.Trigger != "user" {
		req.Trigger = "background"
	}
	maxAge := 180
	if req.MaxAgeSec != nil {
		maxAge = max(*req.MaxAgeSec, 0)
	}
	if !Supported(req.Agent) {
		r := Report{MachineID: req.MachineID, Agent: req.Agent, ProfileID: req.ProfileID, CheckedAt: time.Now().UTC(),
			Trigger: req.Trigger, State: "unsupported", Windows: []Window{}, Facts: []Fact{}, Raw: []string{}}
		return &r, nil, nil
	}
	if s.hub.Settings().Disabled[req.Agent] {
		return nil, nil, ErrDisabled
	}
	h, ok := s.hub.Host(req.MachineID)
	if !ok {
		return nil, nil, machine.ErrNotFound
	}
	k := key(req.MachineID, req.Agent, req.ProfileID)
	s.mu.Lock()
	if r, ok := s.reports[k]; ok && maxAge > 0 && time.Since(r.CheckedAt) <= time.Duration(maxAge)*time.Second {
		s.mu.Unlock()
		return &r, nil, nil
	}
	if id, ok := s.running[k]; ok {
		s.mu.Unlock()
		if j, ok := s.jobs.Get(id); ok && j.Active() {
			return nil, &j, nil
		}
		s.mu.Lock()
		delete(s.running, k)
	}
	s.mu.Unlock()
	if req.Trigger == "background" && !s.hub.Settings().Background {
		return nil, nil, ErrBackground
	}
	if !h.Connected() {
		return nil, nil, ErrOffline
	}
	prof, err := s.hub.Profile(req.MachineID, req.Agent, req.ProfileID)
	if err != nil {
		return nil, nil, err
	}
	spec := CheckSpec{MachineID: req.MachineID, Agent: req.Agent, ProfileID: req.ProfileID, ProfileEnv: prof.Env,
		ProfileDir: prof.Dir, Command: s.hub.Command(req.MachineID, req.Agent), Trigger: req.Trigger}
	if s.OnScreen != nil {
		on := s.OnScreen
		spec.OnScreen = func(step string, sn screen.Snapshot) { on(req.Agent, step, sn.Lines) }
	}
	s.mu.Lock()
	if id, ok := s.running[k]; ok { // raced with another request
		s.mu.Unlock()
		if j, ok := s.jobs.Get(id); ok {
			return nil, &j, nil
		}
		s.mu.Lock()
	}
	j, err := s.jobs.SubmitFunc(jobs.FuncJob{Kind: jobs.KindUsage, Purpose: "usage", MachineID: req.MachineID, Agent: req.Agent,
		Run: func(ctx context.Context, rep *jobs.Reporter) (json.RawMessage, error) {
			r, err := s.run(ctx, h, spec, rep)
			if err != nil {
				return nil, err
			}
			return json.Marshal(r)
		},
		OnFinish: func(jobs.Job) {
			s.mu.Lock()
			delete(s.running, k)
			s.mu.Unlock()
		}})
	if err != nil {
		s.mu.Unlock()
		return nil, nil, err
	}
	if j.Active() {
		s.running[k] = j.ID
	}
	s.mu.Unlock()
	return nil, &j, nil
}

func (s *Service) machineLock(id string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.machine[id]
	if !ok {
		c = make(chan struct{}, 1)
		s.machine[id] = c
	}
	return c
}

// run is one check job: wait for a slot (≤ 2 per hub, one per machine
// because the folder path is fixed), make the folder, drive the CLI,
// always remove the folder, store and publish the report.
func (s *Service) run(ctx context.Context, h machine.Host, sp CheckSpec, rep *jobs.Reporter) (Report, error) {
	rep.Progress("queued", "Waiting for a usage slot…")
	select {
	case s.hubSem <- struct{}{}:
	case <-ctx.Done():
		return Report{}, ctx.Err()
	}
	defer func() { <-s.hubSem }()
	ml := s.machineLock(sp.MachineID)
	select {
	case ml <- struct{}{}:
	case <-ctx.Done():
		return Report{}, ctx.Err()
	}
	defer func() { <-ml }()
	r := Report{MachineID: sp.MachineID, Agent: sp.Agent, ProfileID: sp.ProfileID, Trigger: sp.Trigger,
		Windows: []Window{}, Facts: []Fact{}, Raw: []string{}}
	rep.Progress("launching", "Preparing a private folder…")
	dir, err := s.tmp.make(ctx, h)
	if err != nil {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		r.State, r.Error = "error", err.Error()
		r.CheckedAt = time.Now().UTC()
		s.store(r)
		return r, nil
	}
	out := drive(ctx, h, sp, dir, rep.Progress)
	if err := s.tmp.remove(ctx, h, dir); err != nil {
		r.Error = "folder sementara: " + err.Error() // kept in pending-cleanup.json
	}
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return Report{}, ctx.Err()
	}
	r.CheckedAt = time.Now().UTC()
	r.State = out.state
	if out.err != "" {
		r.Error = out.err
	}
	if out.state != "ok" {
		r.Raw = rawLines(out.snap.Lines)
		s.store(r)
		return r, nil
	}
	r.Raw = out.raw
	p := ParseScreen(sp.Agent, out.raw, r.CheckedAt, time.Local)
	switch {
	case p.Unsupported != "":
		r.State, r.Error = "unsupported", p.Unsupported
	case len(p.Windows) == 0:
		r.State, r.Parsed = "error", false
		if r.Error == "" {
			r.Error = "layar usage tidak dikenali"
			if p.Pending {
				r.Error = "belum menerima data limit dari server; coba lagi sebentar lagi"
			}
		}
	default:
		r.Parsed, r.Windows = true, p.Windows
	}
	if p.Facts != nil {
		r.Facts = p.Facts
	}
	s.store(r)
	return r, nil
}

// store caches, persists and publishes a report and re-arms its chime.
func (s *Service) store(r Report) {
	s.mu.Lock()
	s.reports[r.key()] = r
	list := make([]Report, 0, len(s.reports))
	for _, x := range s.reports {
		list = append(list, x)
	}
	s.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].key() < list[j].key() })
	if err := ensureDir(s.st, "usage"); err == nil {
		_ = s.st.Save(reportsFile, list)
	}
	s.hub.Publish(map[string]any{"type": "usage", "report": r})
	s.chime.arm(r)
}

// Tokens is GET /api/usage/tokens.
func (s *Service) Tokens(ctx context.Context, days int, machineIDs []string, agentID string) (*TokenChart, error) {
	return s.tokens.chart(ctx, days, machineIDs, agentID)
}

func ensureDir(st *store.Store, name string) error {
	return os.MkdirAll(st.Path(name), 0o700)
}
