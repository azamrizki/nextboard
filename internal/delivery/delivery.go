// Package delivery is the only path through which orchestration text
// reaches an agent's terminal (docs/API-orchestration.md §2): bracketed
// paste + Return, only while the agent is in the foreground and its screen
// is positively recognised as the CLI's normal input prompt; queued while
// the agent is working, waiting for the user, starting, paused or
// reconnecting; never typed into a shell, a permission prompt or any other
// dialog.
//
// The engine is driven by Step(now) (a ticker in the hub, called directly
// with a fake clock in tests). It never holds its own lock while calling
// into a console.
package delivery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/settings"
)

// States (state machine of §2).
const (
	Queued    = "queued"
	Waiting   = "waiting"
	Typing    = "typing"
	Sent      = "sent"
	Confirmed = "confirmed"
	Failed    = "failed"
	Expired   = "expired"
	Cancelled = "cancelled"
)

// Wait reasons. "dialog" is a NextBoard addition: the agent's state says
// ready but the screen is not its input prompt (a dialog, menu, picker or
// unknown screen).
const (
	WaitStarting     = "starting"
	WaitNeedsYou     = "needsYou"
	WaitWorking      = "working"
	WaitPaused       = "paused"
	WaitReconnecting = "reconnecting"
	WaitHumanTyping  = "humanTyping"
	WaitDialog       = "dialog"
)

// Limits (§16).
const (
	MaxText       = 32000
	MaxQueue      = 20
	keepDone      = 20
	confirmWithin = 8 * time.Second
	// screenQuiet: the screen must not have changed for this long before
	// typing, so a dialog that is being drawn is never typed into.
	screenQuiet     = 300 * time.Millisecond
	humanTypingMax  = 10 * time.Second
	defaultDeadline = time.Hour
	checkTimeout    = 4 * time.Second
	tickEvery       = 100 * time.Millisecond
)

var (
	ErrNotFound    = errors.New("session not found")
	ErrNotAgent    = errors.New("there is no agent in the foreground of this console; text is never typed into a shell")
	ErrTargetBusy  = errors.New("the agent is not ready to take dictation (starting, needs you, or not in the foreground)")
	ErrTextTooLong = errors.New("the text is longer than 32 000 characters")
	ErrQueueFull   = errors.New("the delivery queue is full (at most 20)")
	ErrEmptyText   = errors.New("empty text")
	ErrNoDelivery  = errors.New("delivery not found")
	ErrAlreadySent = errors.New("the delivery was already typed or sent")
)

// Delivery is the API view (§2). ProjectID is a NextBoard addition used
// by GET /api/deliveries?projectId=.
type Delivery struct {
	ID           string     `json:"id"`
	SessionID    string     `json:"sessionId"`
	ProjectID    string     `json:"projectId"`
	Handle       string     `json:"handle"`
	Text         string     `json:"text"`
	Submit       bool       `json:"submit"`
	Source       string     `json:"source"`
	Guard        string     `json:"guard"`
	OrderID      string     `json:"orderId,omitempty"`
	ForwardDepth int        `json:"forwardDepth"`
	State        string     `json:"state"`
	WaitReason   string     `json:"waitReason"`
	CreatedAt    time.Time  `json:"createdAt"`
	Deadline     time.Time  `json:"deadline"`
	SentAt       *time.Time `json:"sentAt"`
	ConfirmedAt  *time.Time `json:"confirmedAt"`
	Error        string     `json:"error"`
	Code         string     `json:"code"`
}

// Final reports whether the delivery will not change any more. A sent
// delivery is final once its confirmation window closed.
func (d Delivery) Final() bool {
	switch d.State {
	case Confirmed, Failed, Expired, Cancelled:
		return true
	}
	return false
}

// Request is the body of POST /api/sessions/{id}/deliveries plus the
// hub-internal fields.
type Request struct {
	Text         string `json:"text"`
	Submit       *bool  `json:"submit"`
	Source       string `json:"source"`
	Guard        string `json:"guard"`
	DeadlineSec  int    `json:"deadlineSec"`
	OrderID      string `json:"-"`
	ForwardDepth int    `json:"-"`
}

// Console is one terminal the engine can deliver to.
type Console interface {
	View() session.DeliveryView
	// AgentForeground runs the process check (API-agents §6.5).
	AgentForeground(ctx context.Context) (bool, error)
	// Write types bytes (not counted as human input).
	Write(p []byte) error
}

// Engine queues and types deliveries, one active per session (FIFO).
type Engine struct {
	Source   func(sessionID string) (Console, bool)
	Settings func() settings.Delivery
	// OnChange receives every state / waitReason change (outside locks).
	OnChange func(Delivery)
	Now      func() time.Time

	mu     sync.Mutex
	queues map[string][]*item
	done   map[string][]*item
	byID   map[string]*item
	stop   chan struct{}
	once   sync.Once
}

type item struct {
	d           Delivery
	text        string    // sanitized
	hadAgent    bool      // the agent was in the foreground at submit
	reasonSince time.Time // start of the current wait reason
	typedAt     time.Time
	sentState   string
	sentSince   time.Time
	finished    bool // in done (sent counts once its window closed)
}

// New returns an engine; set Source/Settings/OnChange, then Start.
func New() *Engine {
	return &Engine{queues: map[string][]*item{}, done: map[string][]*item{}, byID: map[string]*item{},
		Now: time.Now, Settings: func() settings.Delivery { return settings.Defaults().Delivery }, stop: make(chan struct{})}
}

// Start runs Step every 100 ms until Close.
func (e *Engine) Start() {
	go func() {
		t := time.NewTicker(tickEvery)
		defer t.Stop()
		for {
			select {
			case <-e.stop:
				return
			case <-t.C:
				e.Step(e.Now())
			}
		}
	}()
}

// Close stops the ticker.
func (e *Engine) Close() { e.once.Do(func() { close(e.stop) }) }

// Sanitize removes C0 controls except tab and newline, ESC and DEL, and
// turns CRLF / CR into LF (§2 step 2).
func Sanitize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1 // C0, DEL and C1 (8-bit CSI/OSC)
		}
		return r
	}, s)
}

// Submit queues a delivery for a session.
func (e *Engine) Submit(sessionID string, req Request) (Delivery, error) {
	if utf8.RuneCountInString(req.Text) > MaxText {
		return Delivery{}, ErrTextTooLong
	}
	text := Sanitize(req.Text)
	if strings.TrimSpace(text) == "" {
		return Delivery{}, ErrEmptyText
	}
	c, ok := e.Source(sessionID)
	if !ok {
		return Delivery{}, ErrNotFound
	}
	v := c.View()
	if v.AgentKind != agent.KindAgent || v.ManualAgent || v.Foreground == agentstate.FgShell || v.AgentState == agentstate.Shell {
		return Delivery{}, ErrNotAgent
	}
	guard := req.Guard
	if guard == "" {
		guard = "agent"
	}
	submit := req.Submit == nil || *req.Submit
	if guard == "dictation" {
		submit = false
		if v.Foreground != agentstate.FgAgent || v.AgentState == agentstate.Starting || v.AgentState == agentstate.NeedsYou || v.Paused {
			return Delivery{}, ErrTargetBusy
		}
	} else if guard != "agent" {
		guard = "agent"
	}
	src := req.Source
	if src == "" {
		src = "api"
	}
	now := e.Now()
	dl := defaultDeadline
	if req.DeadlineSec > 0 {
		dl = time.Duration(min(max(req.DeadlineSec, 5), 86400)) * time.Second
	}
	it := &item{
		d: Delivery{ID: auth.RandomID("dl_"), SessionID: sessionID, ProjectID: v.ProjectID, Handle: v.Handle,
			Text: text, Submit: submit, Source: src, Guard: guard, OrderID: req.OrderID, ForwardDepth: req.ForwardDepth,
			State: Queued, CreatedAt: now.UTC(), Deadline: now.Add(dl).UTC()},
		text: text, hadAgent: v.Foreground == agentstate.FgAgent, reasonSince: now,
	}
	e.mu.Lock()
	if len(e.queues[sessionID]) >= MaxQueue {
		e.mu.Unlock()
		return Delivery{}, ErrQueueFull
	}
	e.queues[sessionID] = append(e.queues[sessionID], it)
	e.byID[it.d.ID] = it
	d := it.d
	e.mu.Unlock()
	e.emit(d)
	return d, nil
}

func (e *Engine) emit(d Delivery) {
	if e.OnChange != nil {
		e.OnChange(d)
	}
}

// Get returns one delivery.
func (e *Engine) Get(id string) (Delivery, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	it, ok := e.byID[id]
	if !ok {
		return Delivery{}, false
	}
	return it.d, true
}

// List returns a session's active deliveries and its last 20 finished.
func (e *Engine) List(sessionID string) []Delivery {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []Delivery{}
	for _, it := range e.queues[sessionID] {
		out = append(out, it.d)
	}
	for _, it := range e.done[sessionID] {
		out = append(out, it.d)
	}
	return out
}

// ListAll returns deliveries of a project ("" = all), optionally only
// active ones.
func (e *Engine) ListAll(projectID string, active bool) []Delivery {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []Delivery{}
	add := func(list []*item) {
		for _, it := range list {
			if projectID == "" || it.d.ProjectID == projectID {
				out = append(out, it.d)
			}
		}
	}
	for _, q := range e.queues {
		add(q)
	}
	if !active {
		for _, q := range e.done {
			add(q)
		}
	}
	return out
}

// ActiveCount is Session.queue.
func (e *Engine) ActiveCount(sessionID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.queues[sessionID])
}

// Cancel cancels a delivery that was not typed yet.
func (e *Engine) Cancel(id string) (Delivery, error) {
	e.mu.Lock()
	it, ok := e.byID[id]
	if !ok {
		e.mu.Unlock()
		return Delivery{}, ErrNoDelivery
	}
	switch it.d.State {
	case Typing, Sent, Confirmed:
		d := it.d
		e.mu.Unlock()
		return d, ErrAlreadySent
	case Failed, Expired, Cancelled:
		d := it.d
		e.mu.Unlock()
		return d, nil
	}
	e.finishLocked(it, Cancelled, "", "")
	d := it.d
	e.mu.Unlock()
	e.emit(d)
	return d, nil
}

// SessionRemoved fails every delivery of a deleted session.
func (e *Engine) SessionRemoved(sessionID string) {
	e.mu.Lock()
	var out []Delivery
	for _, it := range append([]*item(nil), e.queues[sessionID]...) {
		if it.d.State == Sent {
			e.finishLocked(it, Sent, "", "")
		} else {
			e.finishLocked(it, Failed, "session_gone", "the session was deleted")
		}
		out = append(out, it.d)
	}
	delete(e.done, sessionID)
	e.mu.Unlock()
	for _, d := range out {
		e.emit(d)
	}
}

// finishLocked moves it out of the active queue.
func (e *Engine) finishLocked(it *item, state, code, msg string) {
	it.d.State, it.d.Code, it.d.Error, it.d.WaitReason = state, code, msg, ""
	if it.finished {
		return
	}
	it.finished = true
	sid := it.d.SessionID
	q := e.queues[sid]
	for i, x := range q {
		if x == it {
			q = append(q[:i], q[i+1:]...)
			break
		}
	}
	if len(q) == 0 {
		delete(e.queues, sid)
	} else {
		e.queues[sid] = q
	}
	e.done[sid] = append(e.done[sid], it)
	if n := len(e.done[sid]); n > keepDone {
		for _, old := range e.done[sid][:n-keepDone] {
			delete(e.byID, old.d.ID)
		}
		e.done[sid] = append([]*item(nil), e.done[sid][n-keepDone:]...)
	}
}

// Step advances the head delivery of every session.
func (e *Engine) Step(now time.Time) {
	e.mu.Lock()
	heads := make([]*item, 0, len(e.queues))
	for _, q := range e.queues {
		if len(q) > 0 {
			heads = append(heads, q[0])
		}
	}
	e.mu.Unlock()
	var wg sync.WaitGroup
	for _, it := range heads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.process(it, now)
		}()
	}
	wg.Wait()
}

// update applies f to it under the lock unless it finished meanwhile
// (cancelled) and publishes a change.
func (e *Engine) update(it *item, f func(it *item) bool) bool {
	e.mu.Lock()
	if it.finished || it.d.State == Cancelled {
		e.mu.Unlock()
		return false
	}
	before := it.d
	if !f(it) {
		e.mu.Unlock()
		return false
	}
	d := it.d
	e.mu.Unlock()
	if d.State != before.State || d.WaitReason != before.WaitReason || d.Code != before.Code {
		e.emit(d)
	}
	return true
}

func (e *Engine) fail(it *item, state, code, msg string) {
	e.update(it, func(it *item) bool {
		e.finishLocked(it, state, code, msg)
		return true
	})
}

func (e *Engine) wait(it *item, reason string, now time.Time) {
	e.update(it, func(it *item) bool {
		if it.d.WaitReason != reason || it.d.State != Waiting {
			it.reasonSince = now
		}
		it.d.State, it.d.WaitReason = Waiting, reason
		return true
	})
}

func (e *Engine) process(it *item, now time.Time) {
	e.mu.Lock()
	state := it.d.State
	finished := it.finished
	e.mu.Unlock()
	if finished {
		return
	}
	c, ok := e.Source(it.d.SessionID)
	if !ok {
		if state == Sent {
			e.fail(it, Sent, "", "")
			return
		}
		e.fail(it, Failed, "session_gone", "the session no longer exists")
		return
	}
	switch state {
	case Queued, Waiting:
		e.gate(it, c, now)
	case Typing:
		e.submit(it, c, now)
	case Sent:
		e.confirm(it, c, now)
	}
}

// reasonTimeout returns how long a wait reason may last and the code used
// when it runs out (0 = only the deadline applies).
func reasonTimeout(st settings.Delivery, reason string) (time.Duration, string, string) {
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	switch reason {
	case WaitStarting:
		return sec(st.StartingTimeoutSec), Expired, "starting_timeout"
	case WaitNeedsYou:
		return sec(st.NeedsYouTimeoutSec), Expired, "needs_you_timeout"
	case WaitWorking:
		return sec(st.WorkingTimeoutSec), Expired, "working_timeout"
	case WaitReconnecting:
		return sec(st.ReconnectTimeoutSec), Expired, "reconnect_timeout"
	case WaitHumanTyping:
		return humanTypingMax, Expired, "human_typing"
	case WaitDialog:
		return sec(st.PromptTimeoutSec), Failed, "prompt_unrecognized"
	}
	return 0, "", ""
}

// blocked evaluates the conditions of the §2 table on a view. It returns
// a terminal outcome (state+code), a wait reason, or nothing (may type).
func blocked(v session.DeliveryView, hadAgent bool, st settings.Delivery, now time.Time, forSubmit bool) (final, code, msg, reason string) {
	switch {
	case v.Status == session.StatusExited || v.Status == session.StatusError || v.AgentState == agentstate.Exited || v.AgentState == agentstate.Error:
		return Failed, "session_gone", "the console process has ended", ""
	case v.AgentKind != agent.KindAgent || v.ManualAgent:
		return Failed, "not_agent", ErrNotAgent.Error(), ""
	case v.Foreground == agentstate.FgShell || v.AgentState == agentstate.Shell:
		if hadAgent {
			return Failed, "agent_exited", "the agent exited before the text was typed", ""
		}
		return Failed, "not_agent", ErrNotAgent.Error(), ""
	case v.Paused:
		return "", "", "", WaitPaused
	case v.Status == session.StatusReconnecting || v.AgentState == agentstate.Reconnecting || !v.Running:
		return "", "", "", WaitReconnecting
	case v.Status == session.StatusStarting || v.AgentState == agentstate.Starting:
		return "", "", "", WaitStarting
	case v.AgentState == agentstate.NeedsYou:
		return "", "", "", WaitNeedsYou
	case v.AgentState == agentstate.Working && st.WhileWorking != "type" && !forSubmit:
		return "", "", "", WaitWorking
	case v.AgentState != agentstate.Ready && v.AgentState != agentstate.Working:
		return "", "", "", WaitStarting
	}
	return "", "", "", ""
}

// gate decides whether a queued/waiting delivery can be typed now.
func (e *Engine) gate(it *item, c Console, now time.Time) {
	if now.After(it.d.Deadline) {
		e.fail(it, Expired, "deadline", "the delivery deadline passed")
		return
	}
	st := e.Settings()
	v := c.View()
	final, code, msg, reason := blocked(v, it.hadAgent, st, now, false)
	if final != "" {
		e.fail(it, final, code, msg)
		return
	}
	guard := time.Duration(st.HumanTypingGuardSec) * time.Second
	var why string
	switch {
	case reason != "":
	case !v.LastHuman.IsZero() && now.Sub(v.LastHuman) < guard:
		reason = WaitHumanTyping
	default:
		ok, w := agent.InputPrompt(v.ScreenRules, v.Lines, v.CursorY)
		if !ok || v.ScreenStale {
			reason, why = WaitDialog, w
		}
	}
	if reason != "" {
		e.mu.Lock()
		since := it.reasonSince
		if it.d.WaitReason != reason || it.d.State != Waiting {
			since = now
		}
		e.mu.Unlock()
		if lim, fstate, fcode := reasonTimeout(st, reason); lim > 0 && now.Sub(since) >= lim {
			m := "the agent was not ready in time (" + reason + ")"
			if reason == WaitDialog {
				m = "the console screen is not recognized as an agent input prompt; not typed"
				if why != "" {
					m += " (" + why + ")"
				}
			}
			e.fail(it, fstate, fcode, m)
			return
		}
		e.wait(it, reason, now)
		return
	}
	if now.Sub(v.LastOutput) < screenQuiet {
		e.wait(it, "", now) // the screen is still changing
		return
	}
	// Process check right before typing.
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	fg, err := c.AgentForeground(ctx)
	cancel()
	if err != nil {
		e.wait(it, WaitReconnecting, now)
		return
	}
	if !fg {
		e.fail(it, Failed, "agent_exited", "process check: the agent is not in the foreground")
		return
	}
	// The check may have taken a while (SSH): look at the screen again.
	v = c.View()
	if final, _, _, reason := blocked(v, it.hadAgent, st, now, false); final != "" || reason != "" {
		return // next step decides
	}
	if ok, _ := agent.InputPrompt(v.ScreenRules, v.Lines, v.CursorY); !ok || v.ScreenStale {
		return
	}
	var payload string
	if v.BracketedPaste {
		payload = "\x1b[200~" + it.text + "\x1b[201~"
	} else {
		payload = strings.ReplaceAll(it.text, "\n", " ")
	}
	if !e.update(it, func(it *item) bool {
		it.d.State, it.d.WaitReason, it.typedAt = Typing, "", now
		return true
	}) {
		return // cancelled meanwhile
	}
	if err := c.Write([]byte(payload)); err != nil {
		e.fail(it, Failed, "write_failed", err.Error())
		return
	}
	if !it.d.Submit {
		e.update(it, func(it *item) bool {
			t := now.UTC()
			it.d.SentAt = &t
			e.finishLocked(it, Sent, "", "")
			return true
		})
	}
}

// submit presses Return after submitDelayMs, but only if the console still
// shows the agent's input prompt (no dialog appeared, no human typed).
func (e *Engine) submit(it *item, c Console, now time.Time) {
	st := e.Settings()
	if now.Sub(it.typedAt) < time.Duration(st.SubmitDelayMs)*time.Millisecond {
		return
	}
	v := c.View()
	final, _, _, reason := blocked(v, it.hadAgent, st, now, true)
	switch {
	case final != "" || reason != "":
		e.fail(it, Failed, "submit_aborted", "the console state changed after the text was pasted; Return not sent (the text stays in the agent's input)")
		return
	case v.LastHuman.After(it.typedAt):
		e.fail(it, Failed, "submit_aborted", "a person typed after the text was pasted; Return not sent")
		return
	}
	if ok, why := agent.InputPromptPasted(v.ScreenRules, v.Lines, v.CursorY); !ok || v.ScreenStale {
		e.fail(it, Failed, "submit_aborted", "the screen is no longer an agent input prompt; Return not sent ("+why+")")
		return
	}
	if !e.update(it, func(it *item) bool { return it.d.State == Typing }) {
		return
	}
	if err := c.Write([]byte("\r")); err != nil {
		e.fail(it, Failed, "write_failed", err.Error())
		return
	}
	e.update(it, func(it *item) bool {
		t := now.UTC()
		it.d.State, it.d.SentAt = Sent, &t
		it.sentState, it.sentSince = v.AgentState, v.StateSince
		return true
	})
}

// confirm marks a sent delivery confirmed when the agent starts working
// within 8 s — seen as working/needsYou now, or as a turn that started
// after the Return (a short turn may already be over); otherwise it stays
// "sent" (not confirmed) and leaves the queue.
func (e *Engine) confirm(it *item, c Console, now time.Time) {
	v := c.View()
	busy := v.AgentState == agentstate.Working || v.AgentState == agentstate.NeedsYou
	fresh := it.sentState != agentstate.Working && it.sentState != agentstate.NeedsYou
	e.mu.Lock()
	sentAt := *it.d.SentAt
	e.mu.Unlock()
	turned := !v.TurnStartedAt.IsZero() && !v.TurnStartedAt.Before(sentAt)
	if (busy && (fresh || v.StateSince.After(it.sentSince))) || turned {
		e.update(it, func(it *item) bool {
			t := now.UTC()
			it.d.ConfirmedAt = &t
			e.finishLocked(it, Confirmed, "", "")
			return true
		})
		return
	}
	if now.Sub(sentAt) >= confirmWithin {
		e.update(it, func(it *item) bool {
			e.finishLocked(it, Sent, "", "")
			return true
		})
	}
}
