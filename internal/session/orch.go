package session

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/machine"
)

// Phase 6 hooks of a console: what the delivery engine looks at before it
// types (docs/API-orchestration.md §2), the write path it uses, and
// pause/resume (§8).

// ErrPidUnknown: a remote console without a known wrapper pid cannot be
// paused (409 pid_unknown).
var ErrPidUnknown = machine.ErrPidUnknown

// DeliveryView is a consistent snapshot for the delivery guards.
type DeliveryView struct {
	Handle      string
	ProjectID   string
	WorkspaceID string
	Agent       string
	AgentKind   string
	Status      string // process status
	AgentState  string
	StateSince  time.Time
	// TurnStartedAt is when the latest turn started (zero = none yet).
	TurnStartedAt time.Time
	Foreground    string
	ManualAgent   bool
	Paused        bool
	Running       bool // a process generation is attached
	// LastHuman is the last client input (WebSocket / POST input);
	// deliveries do not count.
	LastHuman  time.Time
	LastOutput time.Time
	// ScreenRules is the agent's screen rule id ("" = generic).
	ScreenRules    string
	Lines          []string
	CursorY        int
	BracketedPaste bool
	ScreenStale    bool
}

// DeliveryView returns the state the delivery engine checks. The screen
// model is synced first so the snapshot includes all output read so far.
func (s *Session) DeliveryView() DeliveryView {
	s.scr.Sync()
	sn := s.scr.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	es := s.eng.Snapshot()
	var turn time.Time
	if es.LastTurn != nil {
		turn = es.LastTurn.StartedAt
	}
	return DeliveryView{TurnStartedAt: turn,
		Handle: s.spec.Handle, ProjectID: s.spec.ProjectID, WorkspaceID: s.spec.WorkspaceID,
		Agent: s.spec.Agent, AgentKind: s.ag.Kind, Status: s.status, AgentState: es.AgentState, StateSince: es.StateSince,
		Foreground: es.Foreground, ManualAgent: es.ManualAgent, Paused: s.spec.Paused, Running: s.cur != nil,
		LastHuman: s.lastInput, LastOutput: s.lastOutput, ScreenRules: s.ag.ScreenRules,
		Lines: sn.Lines, CursorY: sn.CursorY, BracketedPaste: sn.BracketedPaste, ScreenStale: sn.Stale,
	}
}

// WriteDelivery writes text typed by the delivery engine. Unlike Input it
// does not count as human input; a Return still starts a turn (T14).
func (s *Session) WriteDelivery(p []byte) error {
	s.mu.Lock()
	r := s.cur
	if r != nil {
		s.eng.Input(p, time.Now())
		s.trackLocked(p)
	}
	notify, ev := s.afterLocked(time.Now(), false)
	s.mu.Unlock()
	s.finish(notify, ev)
	if r == nil {
		return errors.New("the process is not running")
	}
	_, err := r.proc.Write(p)
	return err
}

// AgentForeground is the process check before typing (API-agents §6.5):
// the markers must say the agent runs, and the terminal's foreground
// group must be the wrapper's (the agent runs in it) or a job named like
// the agent's CLI. err is set when the check could not run.
func (s *Session) AgentForeground(ctx context.Context) (bool, error) {
	s.mu.Lock()
	fg, manual, a := s.eng.Foreground(), s.eng.Snapshot().ManualAgent, s.ag
	remote := s.spec.MachineID != machine.LocalID
	pid := s.priv.WrapperPid
	s.mu.Unlock()
	if a.Kind != agent.KindAgent || fg != agentstate.FgAgent || manual {
		return false, nil
	}
	if remote && pid <= 0 {
		// No pid yet (re-attached tmux without marker): the markers are
		// all we have.
		return true, nil
	}
	idle, comm, err := s.Foreground(ctx)
	if err != nil {
		return false, err
	}
	if idle {
		return true, nil // the wrapper's group: the agent runs under it
	}
	return comm != "" && (comm == baseName(a.Detect) || comm == baseName(a.Command)), nil
}

// Pause stops the foreground process group of an agent console. It is
// idempotent.
func (s *Session) Pause(ctx context.Context) error {
	s.mu.Lock()
	a, fg, r, paused := s.ag, s.eng.Foreground(), s.cur, s.spec.Paused
	pid, mid := s.priv.WrapperPid, s.spec.MachineID
	s.mu.Unlock()
	if paused {
		return nil
	}
	if a.Kind != agent.KindAgent || fg != agentstate.FgAgent {
		return ErrNotAgent
	}
	if r == nil {
		return ErrOffline
	}
	if h, ok := s.m.machines.Host(mid); !ok || !h.Connected() {
		return ErrOffline
	}
	sg, ok := r.proc.(machine.ForegroundSignaler)
	if !ok {
		return errors.New("pause is not supported for this console")
	}
	if err := sg.SignalForeground(ctx, pid, "STOP"); err != nil {
		return err
	}
	now := time.Now().UTC()
	s.mu.Lock()
	s.spec.Paused, s.spec.PausedAt = true, &now
	s.mu.Unlock()
	s.m.saveNow()
	s.m.changed(s)
	return nil
}

// Resume continues the foreground group (also when a program stopped itself: the
// hub did not cause that) and nudges the TUI to redraw with a one-column resize.
func (s *Session) Resume(ctx context.Context) error {
	s.mu.Lock()
	r, pid, mid := s.cur, s.priv.WrapperPid, s.spec.MachineID
	cols, rows := s.spec.Cols, s.spec.Rows
	s.mu.Unlock()
	if r == nil {
		return ErrOffline
	}
	if h, ok := s.m.machines.Host(mid); !ok || !h.Connected() {
		return ErrOffline
	}
	sg, ok := r.proc.(machine.ForegroundSignaler)
	if !ok {
		return errors.New("resume is not supported for this console")
	}
	if err := sg.SignalForeground(ctx, pid, "CONT"); err != nil {
		return err
	}
	if cols > 10 {
		_ = r.proc.Resize(cols-1, rows)
		time.AfterFunc(100*time.Millisecond, func() {
			s.mu.Lock()
			cur, c, rw := s.cur, s.spec.Cols, s.spec.Rows
			s.mu.Unlock()
			if cur == r {
				_ = r.proc.Resize(c, rw)
			}
		})
	}
	s.mu.Lock()
	was := s.spec.Paused
	s.spec.Paused, s.spec.PausedAt = false, nil
	s.mu.Unlock()
	if was {
		s.m.saveNow()
	}
	s.m.changed(s)
	return nil
}

// Pause / Resume by id.
func (m *Manager) Pause(ctx context.Context, id string) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	err := s.Pause(ctx)
	return m.SessionInfo(s), err
}

func (m *Manager) Resume(ctx context.Context, id string) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	err := s.Resume(ctx)
	return m.SessionInfo(s), err
}

// Changed republishes a session (e.g. its delivery queue changed).
func (m *Manager) Changed(id string) {
	if s, ok := m.Get(id); ok {
		m.changed(s)
	}
}

// ScrollbackText returns the last lines of scrollback with escape
// sequences removed (handoff without transcript).
func (s *Session) ScrollbackText(lines int) string {
	s.mu.Lock()
	b := s.ring.Bytes()
	s.mu.Unlock()
	text := stripANSI(b)
	l := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(l) > lines {
		l = l[len(l)-lines:]
	}
	return strings.TrimSpace(strings.Join(l, "\n"))
}
