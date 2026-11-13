package uptime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/store"
)

const (
	monitorsFile = "uptime/monitors.json"
	settingsFile = "uptime/settings.json"

	// recentBeats is what a view carries for the heartbeat bar.
	recentBeats = 60
	// maxChecks bounds checks running at once, however many monitors come due together.
	maxChecks = 32
	// startSpread spreads the first checks after a hub start.
	startSpread = 15 * time.Second
	pushMinGap  = time.Second
	pushGrace   = 5 * time.Second
	webhookWait = 10 * time.Second
)

// Certificate warnings, in days left.
var certThresholds = []int{14, 7, 1}

// Attention kinds of the alerts (docs/API-agents.md §12).
const (
	KindDown = "uptimeDown"
	KindUp   = "uptimeUp"
	KindCert = "certExpiring"
)

var (
	ErrNotFound  = errors.New("monitor not found")
	ErrPaused    = errors.New("the monitor is paused")
	ErrLimit     = fmt.Errorf("at most %d monitors per project", MaxPerProject)
	ErrPushCheck = errors.New("a push monitor waits for pushes and cannot be checked from here")
)

// Status of a monitor, as the API says it.
const (
	StatusUp      = "up"
	StatusDown    = "down"
	StatusPending = "pending"
	StatusPaused  = "paused"
	// StatusWaiting: nothing is known yet, or the machine it checks from is out of reach.
	StatusWaiting = "waiting"
)

func statusName(s int8) string {
	switch s {
	case statusUp:
		return StatusUp
	case statusDown:
		return StatusDown
	}
	return StatusPending
}

// Alert is something worth interrupting someone for.
type Alert struct {
	Kind      string
	ProjectID string
	MonitorID string
	Title     string
	Body      string
}

// ProjectSettings are a project's alert settings.
type ProjectSettings struct {
	// Webhook gets a JSON POST for each alert. It reaches someone with no board open.
	Webhook     string    `json:"webhook,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
	LastErrorAt time.Time `json:"lastErrorAt,omitzero"`
	// Public is the project's public status page (public.go).
	Public PublicSettings `json:"publicPage,omitzero"`
	// Maintenance are the project's maintenance windows (maintenance.go).
	Maintenance []Window `json:"maintenance,omitempty"`
}

// View is a monitor with what is known about it.
type View struct {
	Monitor
	Status    string     `json:"status"`
	Target    string     `json:"target"`
	Since     *time.Time `json:"since,omitempty"` // in this state since
	Msg       string     `json:"msg,omitempty"`
	Ms        int        `json:"ms"`
	LastCheck *time.Time `json:"lastCheck,omitempty"`
	AvgMs24h  int        `json:"avgMs24h"`
	Uptime24h *float64   `json:"uptime24h"`
	Uptime7d  *float64   `json:"uptime7d"`
	Uptime30d *float64   `json:"uptime30d"`
	Uptime1y  *float64   `json:"uptime1y"`
	Cert      *Cert      `json:"cert,omitempty"`
	Recent    []Beat     `json:"recent,omitempty"`
}

type entry struct {
	mu   sync.Mutex
	m    Monitor
	hist *history

	status  string
	settled int8 // the last up or down, -1 before there was one
	since   time.Time
	msg     string
	ms      int
	lastAt  time.Time
	cert    *Cert
	fails   int
	// lastPush and started tell a push monitor when one is overdue.
	lastPush time.Time
	started  time.Time

	stop context.CancelFunc
	done chan struct{}
	poke chan struct{}
}

// timeNow is the clock while loading, before a test can set Service.Now.
var timeNow = time.Now

// Service runs the checks.
type Service struct {
	Host    func(id string) (machine.Host, bool)
	Publish func(v any)
	Alert   func(Alert)
	Now     func() time.Time // tests

	st     *store.Store
	client *http.Client

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	sem    chan struct{}

	admin sync.Mutex // one change of configuration at a time; never held by a loop

	mu       sync.Mutex
	started  bool
	entries  map[string]*entry
	byToken  map[string]*entry
	settings map[string]ProjectSettings

	publicCache publicCacheT
}

// New loads what is on disk. Nothing is checked before Start.
func New(st *store.Store) (*Service, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		st: st, ctx: ctx, cancel: cancel, sem: make(chan struct{}, maxChecks),
		Now: timeNow, Publish: func(any) {}, Alert: func(Alert) {},
		client:  &http.Client{Timeout: webhookWait},
		entries: map[string]*entry{}, byToken: map[string]*entry{}, settings: map[string]ProjectSettings{},
	}
	if err := os.MkdirAll(st.Path("uptime"), 0o700); err != nil {
		return nil, err
	}
	var list []Monitor
	if _, err := st.Load(monitorsFile, &list); err != nil {
		return nil, err
	}
	if _, err := st.Load(settingsFile, &s.settings); err != nil {
		return nil, err
	}
	if s.settings == nil {
		s.settings = map[string]ProjectSettings{}
	}
	now := s.Now()
	for _, m := range list {
		e, err := s.newEntry(m, now)
		if err != nil {
			return nil, err
		}
		s.entries[m.ID] = e
		if m.PushToken != "" {
			s.byToken[m.PushToken] = e
		}
	}
	return s, nil
}

func (s *Service) dir(id string) string { return s.st.Path("uptime", id) }

// newEntry brings a monitor back to where it was: a restart is no reason to alert anyone.
func (s *Service) newEntry(m Monitor, now time.Time) (*entry, error) {
	h, err := loadHistory(s.dir(m.ID), now)
	if err != nil {
		return nil, err
	}
	e := &entry{m: m, hist: h, status: StatusWaiting, settled: -1, ms: -1, started: now, poke: make(chan struct{}, 1)}
	for i := len(h.beats) - 1; i >= 0; i-- {
		if h.beats[i].S != statusPending {
			e.settled = h.beats[i].S
			break
		}
	}
	if n := len(h.events); n > 0 {
		e.since = time.UnixMilli(h.events[n-1].T)
		if e.settled < 0 && h.events[n-1].Status == StatusDown {
			e.settled = statusDown
		} else if e.settled < 0 && h.events[n-1].Status == StatusUp {
			e.settled = statusUp
		}
	}
	if n := len(h.beats); n > 0 {
		b := h.beats[n-1]
		e.status, e.msg, e.ms, e.lastAt = statusName(b.S), b.M, int(b.Ms), time.UnixMilli(b.T)
		e.lastPush = e.lastAt
	}
	if !m.Active {
		e.status = StatusPaused
	}
	return e, nil
}

// Start begins checking.
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	for _, e := range s.entries {
		if e.m.Active {
			spread := min(time.Duration(e.m.IntervalSec)*time.Second, startSpread)
			s.startLoop(e, mrand.N(spread))
		}
	}
}

// Close stops every check and waits for them.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

// startLoop runs the monitor's checks. The caller holds s.mu or s.admin.
func (s *Service) startLoop(e *entry, delay time.Duration) {
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	e.mu.Lock()
	m := e.m
	e.stop, e.done = cancel, done
	e.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(done)
		s.loop(ctx, e, m, delay)
	}()
}

func (s *Service) stopLoop(e *entry) {
	e.mu.Lock()
	stop, done := e.stop, e.done
	e.stop, e.done = nil, nil
	e.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

func (s *Service) loop(ctx context.Context, e *entry, m Monitor, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-e.poke:
		}
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		next := s.runOnce(ctx, e, m)
		<-s.sem
		if ctx.Err() != nil {
			return
		}
		timer.Reset(next)
	}
}

// runOnce does one check and says when the next one is.
func (s *Service) runOnce(ctx context.Context, e *entry, m Monitor) time.Duration {
	if w := s.maintenance(m); w != nil {
		return s.recordMaintenance(e, m, w.Title)
	}
	if m.Type == TypePush {
		r, next, due := s.pushDue(e, m)
		if !due {
			return next
		}
		s.record(e, m, r, false)
		return next
	}
	var h machine.Host
	if m.Type != TypeDNS {
		id := m.MachineID
		if id == "" {
			id = machine.LocalID
		}
		host, ok := s.Host(id)
		if !ok {
			return s.record(e, m, Result{Ms: -1, Waiting: true, Msg: "Machine removed"}, false)
		}
		if !host.Connected() {
			return s.record(e, m, Result{Ms: -1, Waiting: true, Msg: "Waiting for " + host.Info().Name}, false)
		}
		h = host
	}
	r := check(ctx, m, h)
	if ctx.Err() != nil {
		return 0 // stopped on the way: this says nothing about the target
	}
	return s.record(e, m, r, false)
}

// maintenance is the window that covers m now, if any.
func (s *Service) maintenance(m Monitor) *Window {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maintenanceFor(s.settings[m.ProjectID].Maintenance, m.ID, s.Now())
}

// recordMaintenance shows the monitor in maintenance: no heartbeat, nothing counted, no
// alert. A push monitor's clock starts again when the window ends.
func (s *Service) recordMaintenance(e *entry, m Monitor, title string) time.Duration {
	now := s.Now()
	e.mu.Lock()
	changed := e.status != StatusMaintenance || e.msg != title
	e.status, e.msg, e.ms = StatusMaintenance, title, -1
	e.started = now
	v := s.viewLocked(e, true)
	e.mu.Unlock()
	if changed {
		s.Publish(map[string]any{"type": "uptimeMonitor", "monitor": v})
	}
	return min(time.Duration(m.IntervalSec)*time.Second, maintRecheck)
}

// SetMaintenance replaces a project's maintenance windows. Its monitors look again at
// once, so that a window turned on or off shows now.
func (s *Service) SetMaintenance(projectID string, windows []Window) (ProjectSettings, error) {
	s.admin.Lock()
	defer s.admin.Unlock()
	s.mu.Lock()
	ids := map[string]bool{}
	var mine []*entry
	for _, e := range s.entries {
		if e.m.ProjectID == projectID {
			ids[e.m.ID] = true
			mine = append(mine, e)
		}
	}
	s.mu.Unlock()
	norm, err := normalizeWindows(windows, ids)
	if err != nil {
		return ProjectSettings{}, err
	}
	s.mu.Lock()
	c := s.settings[projectID]
	c.Maintenance = norm
	s.settings[projectID] = c
	s.mu.Unlock()
	s.saveSettings()
	for _, e := range mine {
		e.mu.Lock()
		if e.status == StatusMaintenance && maintenanceFor(norm, e.m.ID, s.Now()) == nil {
			e.status = StatusWaiting // until the check that comes now
		}
		e.mu.Unlock()
		select {
		case e.poke <- struct{}{}:
		default:
		}
	}
	return c, nil
}

// pushDue looks at a push monitor: nothing is checked, a push is either overdue or not.
func (s *Service) pushDue(e *entry, m Monitor) (Result, time.Duration, bool) {
	now := s.Now()
	interval := time.Duration(m.IntervalSec) * time.Second
	e.mu.Lock()
	last, started := e.lastPush, e.started
	retrying := e.status == StatusPending
	e.mu.Unlock()
	if last.IsZero() {
		return Result{Ms: -1, Waiting: true, Msg: "Waiting for the first push"}, interval, true
	}
	// The hub being off is no missed push: the clock starts when the hub is back.
	ref := last
	if started.After(ref) {
		ref = started
	}
	wait := interval + pushGrace
	if retrying {
		wait = time.Duration(m.RetryIntervalSec) * time.Second
	}
	if d := ref.Add(wait).Sub(now); d > 0 {
		return Result{}, d, false
	}
	return failed("No push in " + (time.Duration(m.IntervalSec) * time.Second).String()), interval, true
}

// record keeps the outcome of a check and tells whoever should hear of it. It returns
// when the next check is due.
func (s *Service) record(e *entry, m Monitor, r Result, final bool) time.Duration {
	now := s.Now()
	next := time.Duration(m.IntervalSec) * time.Second
	e.mu.Lock()
	if r.Waiting {
		e.status, e.msg, e.ms = StatusWaiting, r.Msg, -1
		v := s.viewLocked(e, true)
		e.mu.Unlock()
		s.Publish(map[string]any{"type": "uptimeMonitor", "monitor": v})
		return next
	}
	var st int8
	switch {
	case r.OK:
		st = statusUp
		e.fails = 0
	case final || e.settled == statusDown || e.fails >= m.Retries:
		st = statusDown
	default:
		st = statusPending
		e.fails++
		next = time.Duration(m.RetryIntervalSec) * time.Second
	}
	b := Beat{T: now.UnixMilli(), S: st, Ms: int32(r.Ms)}
	if st != statusUp {
		b.M = r.Msg
	}
	if err := e.hist.add(b, now); err != nil {
		log.Printf("uptime %s: %v", m.ID, err)
	}
	e.status, e.msg, e.ms, e.lastAt = statusName(st), r.Msg, r.Ms, now

	var alerts []Alert
	if st != statusPending && st != e.settled {
		prev := e.settled
		ev := Event{T: now.UnixMilli(), Status: statusName(st), Msg: r.Msg}
		if prev >= 0 && !e.since.IsZero() {
			ev.DurSec = int64(now.Sub(e.since).Seconds())
		}
		if err := e.hist.addEvent(ev); err != nil {
			log.Printf("uptime %s: %v", m.ID, err)
		}
		e.settled, e.since = st, now
		switch {
		case st == statusDown:
			alerts = append(alerts, Alert{Kind: KindDown, Title: m.Name + " is down", Body: r.Msg})
		case prev == statusDown:
			alerts = append(alerts, Alert{Kind: KindUp, Title: m.Name + " is back up", Body: "Down for " + durText(ev.DurSec)})
		}
	}
	certChanged := false
	if r.Cert != nil {
		e.cert = r.Cert
		if a, changed := certAlert(e, m.Name, r.Cert, now); changed {
			certChanged = true
			if a != nil {
				alerts = append(alerts, *a)
			}
		}
	}
	notify := e.m.Notify
	v := s.viewLocked(e, false)
	e.mu.Unlock()

	if certChanged {
		s.saveMonitors()
	}
	s.Publish(map[string]any{"type": "uptimeBeat", "projectId": m.ProjectID, "monitorId": m.ID, "beat": b, "monitor": v})
	if notify {
		for _, a := range alerts {
			a.ProjectID, a.MonitorID = m.ProjectID, m.ID
			s.Alert(a)
			s.sendWebhook(m, a)
		}
	}
	return next
}

// certAlert decides whether a certificate is worth a warning now, once per threshold.
func certAlert(e *entry, name string, c *Cert, now time.Time) (*Alert, bool) {
	changed := false
	if !e.m.CertNotAfter.Equal(c.NotAfter) {
		e.m.CertNotAfter, e.m.CertWarned = c.NotAfter, 0
		changed = true
	}
	days := int(c.NotAfter.Sub(now).Hours() / 24)
	reached := 0
	for _, t := range certThresholds {
		if days <= t {
			reached = t
		}
	}
	if reached == 0 || (e.m.CertWarned != 0 && e.m.CertWarned <= reached) {
		return nil, changed
	}
	e.m.CertWarned = reached
	body := fmt.Sprintf("Expires %s", c.NotAfter.Format("2 Jan 2006"))
	title := fmt.Sprintf("%s: certificate expires in %d days", name, days)
	if days == 1 {
		title = name + ": certificate expires tomorrow"
	} else if days <= 0 {
		title = name + ": certificate expires today"
	}
	return &Alert{Kind: KindCert, Title: title, Body: body}, true
}

func durText(sec int64) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%d s", sec)
	case sec < 3600:
		return fmt.Sprintf("%d min", sec/60)
	case sec < 86400:
		return fmt.Sprintf("%d h %d min", sec/3600, sec%3600/60)
	}
	return fmt.Sprintf("%d d %d h", sec/86400, sec%86400/3600)
}

// viewLocked needs e.mu.
func (s *Service) viewLocked(e *entry, recent bool) View {
	now := s.Now()
	v := View{Monitor: e.m, Status: e.status, Target: e.m.Target(), Msg: e.msg, Ms: e.ms, Cert: e.cert}
	if !e.since.IsZero() {
		t := e.since
		v.Since = &t
	}
	if !e.lastAt.IsZero() {
		t := e.lastAt
		v.LastCheck = &t
	}
	day := e.hist.over(24*time.Hour, now)
	v.AvgMs24h, v.Uptime24h = day.AvgMs, day.Uptime
	v.Uptime7d = e.hist.over(7*24*time.Hour, now).Uptime
	v.Uptime30d = e.hist.over(30*24*time.Hour, now).Uptime
	v.Uptime1y = e.hist.over(365*24*time.Hour, now).Uptime
	if recent {
		v.Recent = e.hist.recent(recentBeats)
	}
	return v
}

func (s *Service) entry(id string) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[id]
}

// List is a project's monitors, oldest first.
func (s *Service) List(projectID string) []View {
	s.mu.Lock()
	var list []*entry
	for _, e := range s.entries {
		if e.m.ProjectID == projectID {
			list = append(list, e)
		}
	}
	s.mu.Unlock()
	out := make([]View, 0, len(list))
	for _, e := range list {
		e.mu.Lock()
		out = append(out, s.viewLocked(e, true))
		e.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// prepare checks a configuration before it is taken over.
func (s *Service) prepare(m *Monitor) error {
	token := m.PushToken
	if err := m.normalize(); err != nil {
		return err
	}
	if err := m.checkPublic(); err != nil {
		return err
	}
	if m.MachineID != "" {
		if _, ok := s.Host(m.MachineID); !ok {
			return machine.ErrNotFound
		}
	}
	if m.Type == TypePush {
		m.PushToken = token
		if m.PushToken == "" {
			b := make([]byte, 24)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			m.PushToken = base64.RawURLEncoding.EncodeToString(b)
		}
	}
	return nil
}

// Create adds a monitor, which is checked right away.
func (s *Service) Create(m Monitor) (View, error) {
	s.admin.Lock()
	defer s.admin.Unlock()
	defer s.dropPublicCache()
	s.mu.Lock()
	count := 0
	for _, e := range s.entries {
		if e.m.ProjectID == m.ProjectID {
			count++
		}
	}
	s.mu.Unlock()
	if count >= MaxPerProject {
		return View{}, ErrLimit
	}
	m.ID = auth.RandomID("u_")
	m.PushToken = ""
	m.CreatedAt = s.Now().UTC()
	if err := s.prepare(&m); err != nil {
		return View{}, err
	}
	e, err := s.newEntry(m, s.Now())
	if err != nil {
		return View{}, err
	}
	s.mu.Lock()
	s.entries[m.ID] = e
	if m.PushToken != "" {
		s.byToken[m.PushToken] = e
	}
	started := s.started
	s.mu.Unlock()
	s.saveMonitors()
	e.mu.Lock()
	v := s.viewLocked(e, true)
	e.mu.Unlock()
	// Before the first check: whoever hears of its heartbeat must know the monitor by then.
	s.Publish(map[string]any{"type": "uptimeMonitor", "monitor": v})
	if started && m.Active {
		s.startLoop(e, 0)
	}
	return v, nil
}

// Update changes a monitor. History stays, and the next check is right away.
func (s *Service) Update(id string, apply func(*Monitor) error) (View, error) {
	s.admin.Lock()
	defer s.admin.Unlock()
	defer s.dropPublicCache()
	e := s.entry(id)
	if e == nil {
		return View{}, ErrNotFound
	}
	e.mu.Lock()
	old := e.m
	e.mu.Unlock()
	next := old
	if err := apply(&next); err != nil {
		return View{}, err
	}
	next.ID, next.ProjectID, next.CreatedAt = old.ID, old.ProjectID, old.CreatedAt
	next.CertNotAfter, next.CertWarned, next.PushToken = old.CertNotAfter, old.CertWarned, old.PushToken
	if err := s.prepare(&next); err != nil {
		return View{}, err
	}
	s.stopLoop(e)
	now := s.Now()
	e.mu.Lock()
	e.m = next
	e.fails = 0
	e.started = now
	switch {
	case !next.Active:
		e.status = StatusPaused
	case e.status == StatusPaused:
		e.status = StatusWaiting
		if e.settled >= 0 {
			e.status = statusName(e.settled)
		}
	}
	v := s.viewLocked(e, true)
	e.mu.Unlock()
	s.mu.Lock()
	if old.PushToken != "" && old.PushToken != next.PushToken {
		delete(s.byToken, old.PushToken)
	}
	if next.PushToken != "" {
		s.byToken[next.PushToken] = e
	}
	started := s.started
	s.mu.Unlock()
	s.saveMonitors()
	s.Publish(map[string]any{"type": "uptimeMonitor", "monitor": v})
	if started && next.Active {
		s.startLoop(e, 0)
	}
	return v, nil
}

// Delete stops a monitor and forgets everything about it.
func (s *Service) Delete(id string) error {
	s.admin.Lock()
	defer s.admin.Unlock()
	return s.deleteLocked(id, true)
}

// RemoveProject goes with its project.
func (s *Service) RemoveProject(projectID string) {
	s.admin.Lock()
	defer s.admin.Unlock()
	s.mu.Lock()
	var ids []string
	for id, e := range s.entries {
		if e.m.ProjectID == projectID {
			ids = append(ids, id)
		}
	}
	_, had := s.settings[projectID]
	delete(s.settings, projectID)
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.deleteLocked(id, false)
	}
	if len(ids) > 0 {
		s.saveMonitors()
	}
	if had {
		s.saveSettings()
	}
}

// MoveProject gives one project's monitors to another, as when projects merge (ROADMAP #49): each
// keeps its history and its checks. None stays on a public page, which was the old project's, and
// the old project's settings go with it. part maps a monitor's part to its id in the new project.
func (s *Service) MoveProject(from, to string, part func(string) string) int {
	s.admin.Lock()
	defer s.admin.Unlock()
	defer s.dropPublicCache()
	s.mu.Lock()
	var moved []*entry
	for _, e := range s.entries {
		e.mu.Lock()
		if e.m.ProjectID == from {
			e.m.ProjectID, e.m.Public, e.m.PartID = to, false, part(e.m.PartID)
			moved = append(moved, e)
		}
		e.mu.Unlock()
	}
	_, had := s.settings[from]
	delete(s.settings, from)
	s.mu.Unlock()
	if len(moved) > 0 {
		s.saveMonitors()
	}
	if had {
		s.saveSettings()
	}
	for _, e := range moved {
		e.mu.Lock()
		v := s.viewLocked(e, true)
		e.mu.Unlock()
		s.Publish(map[string]any{"type": "uptimeMonitor", "monitor": v})
	}
	return len(moved)
}

func (s *Service) deleteLocked(id string, save bool) error {
	defer s.dropPublicCache()
	e := s.entry(id)
	if e == nil {
		return ErrNotFound
	}
	s.stopLoop(e)
	s.mu.Lock()
	delete(s.entries, id)
	if e.m.PushToken != "" {
		delete(s.byToken, e.m.PushToken)
	}
	// windows forget it; one that covered only gone monitors goes too, rather than
	// coming to cover all of them
	pruned := false
	if c, ok := s.settings[e.m.ProjectID]; ok && len(c.Maintenance) > 0 {
		var keep []Window
		for _, w := range c.Maintenance {
			if i := slices.Index(w.MonitorIDs, id); i >= 0 {
				pruned = true
				w.MonitorIDs = slices.Delete(slices.Clone(w.MonitorIDs), i, i+1)
				if len(w.MonitorIDs) == 0 {
					continue
				}
			}
			keep = append(keep, w)
		}
		c.Maintenance = keep
		s.settings[e.m.ProjectID] = c
	}
	s.mu.Unlock()
	if save {
		s.saveMonitors()
		if pruned {
			s.saveSettings()
		}
	}
	_ = os.RemoveAll(s.dir(id))
	s.Publish(map[string]any{"type": "uptimeRemoved", "projectId": e.m.ProjectID, "id": id})
	return nil
}

// CheckNow asks for a check at once; its result arrives like any other.
func (s *Service) CheckNow(id string) error {
	e := s.entry(id)
	if e == nil {
		return ErrNotFound
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.m.Type == TypePush {
		return ErrPushCheck
	}
	if !e.m.Active || e.stop == nil {
		return ErrPaused
	}
	select {
	case e.poke <- struct{}{}:
	default:
	}
	return nil
}

// Push takes a report from outside. It returns false for a token nobody has, or a
// paused monitor. Reports faster than one a second count as one.
func (s *Service) Push(token string, down bool, msg string, ms int) bool {
	s.mu.Lock()
	e := s.byToken[token]
	s.mu.Unlock()
	if e == nil {
		return false
	}
	now := s.Now()
	e.mu.Lock()
	m := e.m
	if !m.Active {
		e.mu.Unlock()
		return false
	}
	if now.Sub(e.lastPush) < pushMinGap {
		e.mu.Unlock()
		return true
	}
	e.lastPush = now
	e.mu.Unlock()
	if s.maintenance(m) != nil {
		return true // heard, not counted
	}
	if msg == "" {
		msg = "OK"
		if down {
			msg = "Reported down"
		}
	}
	if ms < 0 {
		ms = -1
	}
	s.record(e, m, Result{OK: !down, Ms: ms, Msg: clip(msg)}, down)
	return true
}

// History is one range of the chart.
func (s *Service) History(id, rangeName string) (Series, error) {
	e := s.entry(id)
	if e == nil {
		return Series{}, ErrNotFound
	}
	if _, ok := Ranges[rangeName]; !ok {
		return Series{}, errors.New("range must be 1h, 24h, 7d, 30d or 1y")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hist.series(rangeName, s.Now()), nil
}

// Events are the last changes, newest first.
func (s *Service) Events(id string, limit int) ([]Event, error) {
	e := s.entry(id)
	if e == nil {
		return nil, ErrNotFound
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Event, 0, min(limit, len(e.hist.events)))
	for i := len(e.hist.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, e.hist.events[i])
	}
	return out, nil
}

func (s *Service) saveMonitors() {
	s.mu.Lock()
	list := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		list = append(list, e)
	}
	s.mu.Unlock()
	out := make([]Monitor, 0, len(list))
	for _, e := range list {
		e.mu.Lock()
		out = append(out, e.m)
		e.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if err := s.st.Save(monitorsFile, out); err != nil {
		log.Printf("uptime: save monitors: %v", err)
	}
}

func (s *Service) saveSettings() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.st.Save(settingsFile, s.settings); err != nil {
		log.Printf("uptime: save settings: %v", err)
	}
}

// Settings are a project's alert settings.
func (s *Service) Settings(projectID string) ProjectSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings[projectID]
}

// SetWebhook sets where a project's alerts go, "" for nowhere.
func (s *Service) SetWebhook(projectID, webhook string) (ProjectSettings, error) {
	if webhook != "" {
		u, err := url.Parse(webhook)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return ProjectSettings{}, errors.New("the webhook must be an http:// or https:// URL")
		}
	}
	s.mu.Lock()
	old := s.settings[projectID]
	c := ProjectSettings{Webhook: webhook, Public: old.Public, Maintenance: old.Maintenance}
	s.settings[projectID] = c
	s.mu.Unlock()
	s.saveSettings()
	return c, nil
}

// webhookBody fits the common receivers at once: Slack and Telegram read text, Discord
// reads content, anything else can use the fields.
func webhookBody(m Monitor, a Alert, at time.Time) []byte {
	text := a.Title
	if a.Body != "" {
		text += ": " + a.Body
	}
	if t := m.Target(); t != "" && t != "push" {
		text += " (" + t + ")"
	}
	b, _ := json.Marshal(map[string]any{
		"text": text, "content": text, "kind": a.Kind, "monitor": m.Name, "target": m.Target(),
		"message": a.Body, "at": at.UTC().Format(time.RFC3339),
	})
	return b
}

// postWebhook never says the URL when it fails: for most receivers, the URL is the secret.
func (s *Service) postWebhook(ctx context.Context, webhook string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.New(errText(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("receiver answered %s", resp.Status)
	}
	return nil
}

// Webhook retries: a receiver's hiccup should not lose the news of an outage.
var webhookRetries = []time.Duration{0, 5 * time.Second, 30 * time.Second}

// sendWebhook delivers in the background: a slow receiver must not hold up the checks.
func (s *Service) sendWebhook(m Monitor, a Alert) {
	c := s.Settings(m.ProjectID)
	if c.Webhook == "" {
		return
	}
	body := webhookBody(m, a, s.Now())
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		var err error
		for _, wait := range webhookRetries {
			select {
			case <-time.After(wait):
			case <-s.ctx.Done():
				return
			}
			if err = s.postWebhook(s.ctx, c.Webhook, body); err == nil {
				break
			}
		}
		s.noteWebhook(m.ProjectID, c.Webhook, err)
	}()
}

// noteWebhook keeps how the last delivery went, unless the setting changed meanwhile.
func (s *Service) noteWebhook(projectID, webhook string, err error) {
	s.mu.Lock()
	c, ok := s.settings[projectID]
	if !ok || c.Webhook != webhook || (err == nil && c.LastError == "") {
		s.mu.Unlock()
		return
	}
	c.LastError, c.LastErrorAt = "", time.Time{}
	if err != nil {
		c.LastError, c.LastErrorAt = err.Error(), s.Now().UTC()
		log.Printf("uptime: webhook project %s: %v", projectID, err)
	}
	s.settings[projectID] = c
	s.mu.Unlock()
	s.saveSettings()
}

// TestWebhook sends one message and says how it went.
func (s *Service) TestWebhook(ctx context.Context, projectID string) error {
	c := s.Settings(projectID)
	if c.Webhook == "" {
		return errors.New("no webhook is set")
	}
	a := Alert{Kind: "test", Title: "NextBoard uptime alerts are on", Body: "This is a test message"}
	err := s.postWebhook(ctx, c.Webhook, webhookBody(Monitor{Type: TypePush, Name: "test"}, a, s.Now()))
	s.noteWebhook(projectID, c.Webhook, err)
	return err
}
