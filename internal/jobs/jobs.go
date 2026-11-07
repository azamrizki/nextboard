// Package jobs runs long hub jobs (hidden CLI runner now; git, publish and
// usage later) with progress, cancel and events (docs/API-studio.md §1.2).
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/store"
)

const (
	jobsFile = "jobs.json"
	// keepJobs finished jobs are kept in memory and in jobs.json.
	keepJobs = 100
	// Runner concurrency (docs/API-studio.md §15).
	maxPerMachine = 2
	maxPerHub     = 4
	maxQueued     = 64
	// publishEvery throttles progress-only job events (≤ 2/s per job).
	publishEvery = 500 * time.Millisecond
	// maxStoredResult: larger results are not written to jobs.json.
	maxStoredResult = 64 << 10
	cancelWait      = 6 * time.Second
)

// Kinds and states.
const (
	KindRunner  = "runner"
	KindGit     = "git"
	KindPublish = "publish"
	KindUsage   = "usage"

	StateQueued    = "queued"
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

var (
	ErrNotFound          = errors.New("job not found")
	ErrRunnerUnsupported = errors.New("this agent is not supported for the runner")
	ErrQueueFull         = errors.New("the job queue is full, try again later")
	ErrBadPurpose        = errors.New("unknown runner purpose")
	ErrBadRequest        = errors.New("invalid runner request")
	ErrClosed            = errors.New("the hub is shutting down")
)

// Progress is the live phase of a job.
type Progress struct {
	Phase string   `json:"phase"`
	Text  string   `json:"text"`
	Pct   *float64 `json:"pct"`
}

// Job is the API view of a job.
type Job struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Purpose    string          `json:"purpose"`
	ProjectID  string          `json:"projectId"`
	MachineID  string          `json:"machineId"`
	Agent      string          `json:"agent"`
	State      string          `json:"state"`
	Progress   Progress        `json:"progress"`
	CreatedAt  time.Time       `json:"createdAt"`
	StartedAt  *time.Time      `json:"startedAt"`
	FinishedAt *time.Time      `json:"finishedAt"`
	ElapsedMs  int64           `json:"elapsedMs"`
	Error      string          `json:"error"`
	Code       string          `json:"code"`
	Result     json.RawMessage `json:"result"`
}

// Active reports whether the job is still queued or running.
func (j Job) Active() bool { return j.State == StateQueued || j.State == StateRunning }

// job is the manager's record; Job fields are guarded by Manager.mu.
type job struct {
	Job
	req       RunnerRequest
	cancel    context.CancelFunc
	userStop  bool // Cancel was called while running
	done      chan struct{}
	seq       int
	lastPub   time.Time
	pubTimer  *time.Timer
	finishedC bool
	types     []string // runner: JSON event types seen (diagnostics)
}

// Manager owns every job, the runner queue and jobs.json.
type Manager struct {
	// Publish sends an /api/events message ({"type":"job"} / {"type":"jobOutput"}).
	Publish func(v any)

	st   *store.Store
	host func(machineID string) (machine.Host, bool)

	pubMu sync.Mutex // orders events: taken before mu, never under it

	mu      sync.Mutex
	jobs    map[string]*job
	order   []string // creation order
	queue   []*job   // queued runner jobs, FIFO
	running map[string]int
	total   int
	closed  bool
	wg      sync.WaitGroup

	saveMu    sync.Mutex
	saveTimer *time.Timer
}

// NewManager loads jobs.json. Jobs that were queued or running when the
// hub stopped are reported as failed ("hub_restart").
func NewManager(st *store.Store, host func(machineID string) (machine.Host, bool)) *Manager {
	m := &Manager{st: st, host: host, jobs: map[string]*job{}, running: map[string]int{}}
	var saved []Job
	if _, err := st.Load(jobsFile, &saved); err != nil {
		saved = nil
	}
	sort.SliceStable(saved, func(i, k int) bool { return saved[i].CreatedAt.Before(saved[k].CreatedAt) })
	now := time.Now().UTC()
	for _, j := range saved {
		if j.ID == "" {
			continue
		}
		if j.Active() {
			j.State, j.Code, j.Error = StateFailed, "hub_restart", "the hub restarted before the job finished"
			j.FinishedAt = &now
		}
		rec := &job{Job: j, done: make(chan struct{})}
		close(rec.done)
		rec.finishedC = true
		m.jobs[j.ID] = rec
		m.order = append(m.order, j.ID)
	}
	m.pruneLocked()
	return m
}

// Close stops running jobs (reported as failed "hub_restart") and saves.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, j := range m.jobs {
		if j.State == StateRunning && j.cancel != nil {
			j.cancel()
		}
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(cancelWait):
	}
	m.saveNow()
}

// view returns the API snapshot of j. Caller holds mu.
func (j *job) view(now time.Time) Job {
	v := j.Job
	switch {
	case v.StartedAt != nil && v.FinishedAt != nil:
		v.ElapsedMs = v.FinishedAt.Sub(*v.StartedAt).Milliseconds()
	case v.StartedAt != nil:
		v.ElapsedMs = now.Sub(*v.StartedAt).Milliseconds()
	}
	if v.Result != nil {
		v.Result = append(json.RawMessage(nil), v.Result...)
	}
	return v
}

// Get returns one job.
func (m *Manager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return j.view(time.Now()), true
}

// List returns jobs newest first, optionally only one project's and only
// active ones. limit defaults to 50 (max 100).
func (m *Manager) List(projectID string, activeOnly bool, limit int) []Job {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, keepJobs)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := []Job{}
	for i := len(m.order) - 1; i >= 0 && len(out) < limit; i-- {
		j := m.jobs[m.order[i]]
		if projectID != "" && j.ProjectID != projectID {
			continue
		}
		if activeOnly && !j.Active() {
			continue
		}
		v := j.view(now)
		v.Result = nil // the list stays small; GET /api/jobs/{id} carries it
		out = append(out, v)
	}
	return out
}

// Cancel stops a job: a queued one is cancelled at once, a running one has
// its process stopped (partial output kept when the purpose supports it).
// Finished jobs are returned unchanged.
func (m *Manager) Cancel(id string) (Job, error) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return Job{}, ErrNotFound
	}
	switch j.State {
	case StateQueued:
		for i, q := range m.queue {
			if q == j {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		m.finish(j, StateCancelled, "", "", nil, false)
	case StateRunning:
		j.userStop = true
		if j.cancel != nil {
			j.cancel()
		}
		done := j.done
		m.mu.Unlock()
		select {
		case <-done:
		case <-time.After(cancelWait):
		}
	default:
		m.mu.Unlock()
	}
	v, _ := m.Get(id)
	return v, nil
}

// enqueue registers a new job and starts it when a slot is free.
func (m *Manager) enqueue(j *job) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	if len(m.queue) >= maxQueued {
		m.mu.Unlock()
		return ErrQueueFull
	}
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.queue = append(m.queue, j)
	m.pruneLocked()
	m.mu.Unlock()
	m.emit(j, true)
	m.schedule()
	m.saveSoon()
	return nil
}

// schedule starts queued jobs in FIFO order while slots are free (≤ 2 per
// machine, ≤ 4 per hub); a job whose machine is full does not block jobs
// of other machines.
func (m *Manager) schedule() {
	var start []*job
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	rest := m.queue[:0:0]
	for _, j := range m.queue {
		if m.total < maxPerHub && m.running[j.MachineID] < maxPerMachine {
			m.total++
			m.running[j.MachineID]++
			now := time.Now().UTC()
			j.State, j.StartedAt = StateRunning, &now
			j.Progress = Progress{Phase: "launching", Text: "Launching…"}
			ctx, cancel := context.WithCancel(context.Background())
			j.cancel = cancel
			m.wg.Add(1)
			start = append(start, j)
			go func(j *job, ctx context.Context) {
				defer m.wg.Done()
				m.run(ctx, j)
			}(j, ctx)
			continue
		}
		rest = append(rest, j)
	}
	m.queue = rest
	m.mu.Unlock()
	for _, j := range start {
		m.emit(j, true)
	}
	if len(start) > 0 {
		m.saveSoon()
	}
}

// finish moves j to a terminal state exactly once, frees its slot and
// starts the next queued job.
func (m *Manager) finish(j *job, state, code, msg string, result json.RawMessage, slot bool) {
	m.mu.Lock()
	if j.finishedC {
		m.mu.Unlock()
		return
	}
	j.finishedC = true
	if m.closed && state != StateDone {
		state, code, msg = StateFailed, "hub_restart", "the hub shut down before the job finished"
	}
	now := time.Now().UTC()
	j.State, j.Code, j.Error, j.FinishedAt = state, code, msg, &now
	if result != nil {
		j.Result = result
	}
	switch state {
	case StateDone:
		j.Progress = Progress{Phase: "done", Text: "Done"}
	case StateCancelled:
		j.Progress = Progress{Phase: "cancelled", Text: "Cancelled"}
	default:
		j.Progress = Progress{Phase: "failed", Text: "Failed"}
	}
	if slot {
		m.total--
		m.running[j.MachineID]--
		if m.running[j.MachineID] <= 0 {
			delete(m.running, j.MachineID)
		}
	}
	if j.cancel != nil {
		j.cancel()
	}
	close(j.done)
	onFinish := j.req.OnFinish
	snap := j.view(now)
	m.pruneLocked()
	m.mu.Unlock()
	m.emit(j, true)
	m.saveSoon()
	m.schedule()
	if onFinish != nil {
		onFinish(snap)
	}
}

// setProgress updates the phase; the event is throttled.
func (m *Manager) setProgress(j *job, phase, text string) {
	m.mu.Lock()
	if j.State != StateRunning || (j.Progress.Phase == phase && j.Progress.Text == text) {
		m.mu.Unlock()
		return
	}
	j.Progress = Progress{Phase: phase, Text: text}
	m.mu.Unlock()
	m.emit(j, false)
}

// emit publishes {"type":"job"}. force = state change (sent now); other
// changes are coalesced to at most one event per publishEvery.
func (m *Manager) emit(j *job, force bool) {
	m.pubMu.Lock()
	defer m.pubMu.Unlock()
	m.mu.Lock()
	now := time.Now()
	if !force && now.Sub(j.lastPub) < publishEvery {
		if j.pubTimer == nil {
			j.pubTimer = time.AfterFunc(publishEvery-now.Sub(j.lastPub), func() {
				m.mu.Lock()
				j.pubTimer = nil
				m.mu.Unlock()
				m.emit(j, true)
			})
		}
		m.mu.Unlock()
		return
	}
	if j.pubTimer != nil {
		j.pubTimer.Stop()
		j.pubTimer = nil
	}
	j.lastPub = now
	snap := j.view(now)
	pub := m.Publish
	m.mu.Unlock()
	if pub != nil {
		pub(map[string]any{"type": "job", "job": snap})
	}
}

// output publishes a streamed text chunk ({"type":"jobOutput"}).
func (m *Manager) output(j *job, text string) {
	if text == "" {
		return
	}
	m.pubMu.Lock()
	defer m.pubMu.Unlock()
	m.mu.Lock()
	j.seq++
	seq := j.seq
	pub := m.Publish
	m.mu.Unlock()
	if pub != nil {
		pub(map[string]any{"type": "jobOutput", "jobId": j.ID, "seq": seq, "text": text})
	}
}

// pruneLocked keeps at most keepJobs finished jobs (oldest dropped).
func (m *Manager) pruneLocked() {
	finished := 0
	for _, id := range m.order {
		if !m.jobs[id].Active() {
			finished++
		}
	}
	if finished <= keepJobs {
		return
	}
	drop := finished - keepJobs
	kept := m.order[:0]
	for _, id := range m.order {
		if drop > 0 && !m.jobs[id].Active() {
			delete(m.jobs, id)
			drop--
			continue
		}
		kept = append(kept, id)
	}
	m.order = kept
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

// saveNow writes job metadata; large results are left out.
func (m *Manager) saveNow() {
	m.mu.Lock()
	now := time.Now()
	out := make([]Job, 0, len(m.order))
	for _, id := range m.order {
		v := m.jobs[id].view(now)
		if len(v.Result) > maxStoredResult {
			v.Result = nil
		}
		out = append(out, v)
	}
	m.mu.Unlock()
	_ = m.st.Save(jobsFile, out)
}
