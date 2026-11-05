package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
)

// FuncJob is a non-runner job (usage checks; docs/API-studio.md §1.2)
// whose work is a Go function. It starts at once, outside the runner queue:
// the submitting service enforces its own concurrency limits.
type FuncJob struct {
	Kind      string // KindUsage, …
	Purpose   string
	ProjectID string
	MachineID string
	Agent     string
	// Run does the work. A *FuncError gives the failure code; a cancelled
	// ctx (Cancel, hub shutdown) ends the job as cancelled.
	Run      func(ctx context.Context, r *Reporter) (json.RawMessage, error)
	OnFinish func(Job)
}

// FuncError is a coded failure of a FuncJob.
type FuncError struct {
	Code string
	Msg  string
}

func (e *FuncError) Error() string { return e.Msg }

// Reporter lets a FuncJob report progress and streamed text.
type Reporter struct {
	m *Manager
	j *job
}

// Progress sets the job phase (events throttled like runner jobs).
func (r *Reporter) Progress(phase, text string) { r.m.setProgress(r.j, phase, text) }

// Output streams a text chunk ({"type":"jobOutput"}).
func (r *Reporter) Output(text string) { r.m.output(r.j, text) }

// ID is the job id.
func (r *Reporter) ID() string { return r.j.ID }

// SubmitFunc registers and starts a FuncJob.
func (m *Manager) SubmitFunc(fj FuncJob) (Job, error) {
	if fj.Run == nil || fj.Kind == "" {
		return Job{}, ErrBadRequest
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{
		Job: Job{
			ID: auth.RandomID("jb_"), Kind: fj.Kind, Purpose: fj.Purpose,
			ProjectID: fj.ProjectID, MachineID: fj.MachineID, Agent: fj.Agent,
			State: StateRunning, Progress: Progress{Phase: "launching", Text: "Launching…"},
			CreatedAt: now, StartedAt: &now,
		},
		req:    RunnerRequest{OnFinish: fj.OnFinish},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return Job{}, ErrClosed
	}
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.pruneLocked()
	m.wg.Add(1)
	m.mu.Unlock()
	m.emit(j, true)
	m.saveSoon()
	go func() {
		defer m.wg.Done()
		res, err := fj.Run(ctx, &Reporter{m: m, j: j})
		var fe *FuncError
		switch {
		case err == nil:
			m.finish(j, StateDone, "", "", res, false)
		case ctx.Err() != nil || errors.Is(err, context.Canceled):
			m.finish(j, StateCancelled, "", "", res, false)
		case errors.As(err, &fe):
			m.finish(j, StateFailed, fe.Code, fe.Msg, res, false)
		default:
			m.finish(j, StateFailed, "failed", err.Error(), res, false)
		}
	}()
	v, _ := m.Get(j.ID)
	return v, nil
}
