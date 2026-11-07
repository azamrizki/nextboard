// Package agentstate is the status engine of agent consoles
// (docs/API-agents.md §6): it folds wrapper markers, Claude Code hooks,
// Codex notify, per-CLI screen rules and output activity into one
// agentState with its source, activity text and turn timing.
//
// The engine is a plain value driven by its owner (one session) under the
// owner's lock; every input takes the current time so tests can replay the
// transition table deterministically.
package agentstate

import (
	"strings"
	"time"
	"unicode/utf8"
)

// States (§6.1).
const (
	Starting     = "starting"
	Working      = "working"
	NeedsYou     = "needsYou"
	Ready        = "ready"
	Shell        = "shell"
	Exited       = "exited"
	Reconnecting = "reconnecting"
	Error        = "error"
)

// Sources (stateSource).
const (
	SrcHooks    = "hooks"
	SrcNotify   = "notify"
	SrcScreen   = "screen"
	SrcActivity = "activity"
	SrcProcess  = "process"
	SrcWrapper  = "wrapper"
)

// Foreground values (§6.5).
const (
	FgAgent   = "agent"
	FgShell   = "shell"
	FgUnknown = "unknown"
)

// Live status values (Session.liveStatus).
const (
	LiveHooks   = "hooks"
	LiveNotify  = "notify"
	LivePending = "pending"
	LiveScreen  = "screen"
	LiveOff     = "off"
)

// Timings of the transition table (§6.3, §15).
const (
	StableFor    = 300 * time.Millisecond  // T15 / screen working
	ReadyQuiet   = 1500 * time.Millisecond // T17
	ActiveWindow = 2 * time.Second         // T18 working
	IdleAfter    = 3 * time.Second         // T18 ready
	// UnknownIdleAfter is T18 when the screen is not the agent's input prompt: it
	// may be thinking in silence, and a false ready ends an order early.
	UnknownIdleAfter = 15 * time.Second
	// IOHoldMax bounds how long reading alone (a model streaming its thinking, a
	// tool reading files) keeps a silent agent without screen rules working.
	IOHoldMax      = 5 * time.Minute
	StartingMax    = 60 * time.Second // T19
	ActivityMaxLen = 120
)

// Process statuses the engine receives (session.Status*).
const (
	ProcStarting     = "starting"
	ProcRunning      = "running"
	ProcExited       = "exited"
	ProcReconnecting = "reconnecting"
	ProcError        = "error"
)

// Config describes the console the engine follows.
type Config struct {
	Shell bool // agent kind "shell": always state shell
	// Notify: the agent reports finished turns through Codex notify.
	Notify bool
	// HasRules: per-CLI screen rules exist (else T18 output activity).
	HasRules bool
}

// LastTurn is the timing of the latest turn.
type LastTurn struct {
	StartedAt  time.Time  `json:"startedAt"`
	EndedAt    *time.Time `json:"endedAt"`
	DurationMs int64      `json:"durationMs"`
}

// Hook is a decoded Claude Code hook event.
type Hook struct {
	Event            string // hook_event_name / marker ev
	ToolName         string
	ToolSummary      string // short tool input (command, file path, …)
	NotificationType string
	Message          string
}

// Screen is the evaluation of screen rules on the current screen.
type Screen struct {
	NeedsYou string // matching line (or the rule's label)
	// Startup: NeedsYou is a dialog shown outside a turn (folder trust,
	// update, login); it may raise needsYou while hooks drive the state.
	Startup bool
	Working bool
	Ready   bool
}

// Event kinds handed to the owner (attention, reply refresh).
const (
	EvNeedsYou  = "needsYou"  // entered needsYou (Activity = reason)
	EvFinished  = "finished"  // working/needsYou → ready (Turn set)
	EvError     = "error"     // entered error
	EvAgentExit = "agentExit" // wrapper agent-exit (Rc)
)

// Event is a notable transition.
type Event struct {
	Kind     string
	Activity string
	Turn     LastTurn
	Rc       int
}

// Snapshot is the API view (Session fields of §7).
type Snapshot struct {
	AgentState    string     `json:"agentState"`
	StateSource   string     `json:"stateSource"`
	StateSince    time.Time  `json:"stateSince"`
	Activity      string     `json:"activity"`
	NeedsYouSince *time.Time `json:"needsYouSince"`
	Foreground    string     `json:"foreground"`
	ManualAgent   bool       `json:"manualAgent"`
	LiveStatus    string     `json:"liveStatus"`
	AgentExitCode *int       `json:"agentExitCode"`
	LastTurn      *LastTurn  `json:"lastTurn"`
}

// Engine is the state of one console.
type Engine struct {
	cfg Config

	proc     string // process status
	layer    string // agent-layer state: starting|working|needsYou|ready
	source   string // source of the layer state
	activity string

	fg     string
	manual bool
	live   string // configured live status of this run
	hooks  bool   // ≥ 1 hook marker in this run
	notify bool   // ≥ 1 notify in this run
	exit   *int

	startedAt  time.Time
	lastOutput time.Time
	lastSubmit time.Time // Return typed: the turn starts now, whatever was printed before
	lastIO     time.Time // the agent's processes read a stream's worth of bytes

	// generic is the last reading of the screen of an agent without rules.
	generic     Screen
	genericSeen bool
	answered    string // the dialog a Return just answered

	// screen stability
	needsCand   string
	needsCandAt time.Time
	workCandAt  time.Time
	needsSrc    string // source that put the layer in needsYou
	// staleNeeds: a hook ended a needsYou (e.g. PostToolUse after the
	// permission was answered) while the screen still showed a dialog. The
	// answered dialog may stay drawn for a while, possibly matching other
	// lines as it is repainted; until the screen has shown no dialog once,
	// it must not raise needsYou again (nor a second attention).
	staleNeeds bool
	lastTool   string // "Tool(summary)" of the last PreToolUse, for permission text

	// effective state bookkeeping
	eff        string
	effSrc     string
	effSince   time.Time
	needsSince time.Time
	turnStart  time.Time
	turn       *LastTurn

	events []Event
}

// New returns an engine for a console that is being started.
func New(cfg Config, now time.Time) *Engine {
	e := &Engine{cfg: cfg, proc: ProcStarting, layer: Starting, source: SrcProcess, fg: FgUnknown, live: LiveOff, startedAt: now}
	if cfg.Shell {
		e.fg = FgShell
	}
	e.refresh(now)
	return e
}

func (e *Engine) hooksMode() bool { return e.hooks }

// NewRun resets per-run state when the hub launches the process. live is
// the plan's initial live status; mode "shell" starts directly in the shell.
func (e *Engine) NewRun(mode, live string, now time.Time) {
	e.hooks, e.notify, e.exit, e.manual = false, false, nil, false
	e.live = live
	e.layer, e.source, e.activity = Starting, SrcProcess, ""
	e.startedAt, e.lastOutput = now, time.Time{}
	e.needsCand, e.needsSrc, e.staleNeeds = "", "", false
	e.turnStart = time.Time{}
	switch {
	case e.cfg.Shell, mode == "shell":
		e.fg = FgShell
	default:
		e.fg = FgUnknown
	}
	e.refresh(now)
}

// Reattach is a tmux re-attach after a hub restart or reconnect: markers
// of the gap are lost, so the state is recomputed from the screen and the
// process check until the next hook.
func (e *Engine) Reattach(lastForeground string, now time.Time) {
	e.hooks, e.notify = false, false
	if e.live == LiveHooks || e.live == LiveNotify {
		e.live = LivePending
	}
	e.layer, e.source, e.activity = Starting, SrcScreen, ""
	e.startedAt = now
	if lastForeground == FgShell || lastForeground == FgAgent {
		e.fg = lastForeground
	}
	if e.cfg.Shell {
		e.fg = FgShell
	}
	e.refresh(now)
}

// SetProc records the process status of the session.
func (e *Engine) SetProc(status string, now time.Time) {
	e.proc = status
	e.refresh(now)
}

// AgentStart is T1 (marker agent-start).
func (e *Engine) AgentStart(now time.Time) {
	if e.cfg.Shell {
		return
	}
	e.fg, e.manual, e.exit = FgAgent, false, nil
	e.hooks, e.notify = false, false
	if e.live == LiveHooks || e.live == LiveNotify {
		e.live = LivePending
	}
	e.startedAt = now
	e.staleNeeds = false
	e.setLayer(Starting, SrcWrapper, "", now)
}

// AgentExit is T12 (marker agent-exit).
func (e *Engine) AgentExit(rc int, now time.Time) {
	if e.cfg.Shell {
		return
	}
	e.fg, e.manual = FgShell, false
	e.exit = &rc
	e.activity = ""
	e.source = SrcWrapper
	e.events = append(e.events, Event{Kind: EvAgentExit, Rc: rc})
	e.refresh(now)
}

// SetForeground applies a process check (§6.5): a job named like a known
// CLI typed by the user makes foreground agent with manualAgent.
func (e *Engine) SetForeground(fg string, manual bool, now time.Time) {
	if e.cfg.Shell || fg == "" {
		return
	}
	if fg == e.fg && manual == e.manual {
		return
	}
	if fg == FgAgent && manual && e.fg != FgAgent {
		e.layer, e.source, e.startedAt = Starting, SrcScreen, now
	}
	e.fg, e.manual = fg, manual
	e.refresh(now)
}

// Hook applies a Claude Code hook event (T2–T11).
func (e *Engine) Hook(h Hook, now time.Time) {
	if e.cfg.Shell {
		return
	}
	e.hooks = true
	e.live = LiveHooks
	if e.fg != FgAgent {
		e.fg, e.manual = FgAgent, false
	}
	if e.layer == NeedsYou {
		defer func() {
			if e.layer != NeedsYou && e.needsCand != "" {
				e.staleNeeds = true
			}
		}()
	}
	switch h.Event {
	case "SessionStart":
		if e.layer == Starting {
			e.setLayer(Ready, SrcHooks, "", now) // T2
		}
	case "UserPromptSubmit":
		switch e.layer {
		case Ready, NeedsYou, Starting:
			e.setLayer(Working, SrcHooks, "Thinking", now) // T3
		case Working:
			e.activity = "Thinking"
		}
	case "PreToolUse":
		switch h.ToolName {
		case "AskUserQuestion":
			e.setNeedsYou(SrcHooks, "Question", now) // T5
		case "ExitPlanMode":
			e.setNeedsYou(SrcHooks, "Plan approval", now)
		default:
			e.lastTool = permTool(h)
			e.setLayer(Working, SrcHooks, toolActivity(h), now) // T4
		}
	case "PostToolUse":
		if e.layer == NeedsYou || e.layer == Working { // T6
			act := e.activity
			if e.layer == NeedsYou {
				act = toolActivity(h)
			}
			e.setLayer(Working, SrcHooks, act, now)
		}
	case "Notification":
		switch {
		case isPermission(h):
			// Claude 2.1.312 sends only "Claude needs your permission":
			// name the tool of the preceding PreToolUse instead.
			msg := h.Message
			if e.lastTool != "" && !strings.Contains(msg, e.lastTool) {
				msg = e.lastTool
			}
			e.setNeedsYou(SrcHooks, "Permission: "+msg, now) // T7
		case isIdle(h):
			if e.layer == Working { // T8: only closes a turn whose Stop was missed
				e.setLayer(Ready, SrcHooks, "", now)
			}
		}
	case "Stop":
		e.setLayer(Ready, SrcHooks, "", now) // T9
	case "SubagentStop":
		e.activity = "Subagent finished" // T10
		e.refresh(now)
	case "SessionEnd": // T11: wait for agent-exit
	}
}

// isPermission recognises permission notifications (T7).
func isPermission(h Hook) bool {
	t := strings.ToLower(h.NotificationType)
	if strings.Contains(t, "permission") {
		return true
	}
	return t == "" && strings.Contains(strings.ToLower(h.Message), "needs your permission")
}

// isIdle recognises "waiting for your input" notifications (T8).
func isIdle(h Hook) bool {
	t := strings.ToLower(h.NotificationType)
	if strings.Contains(t, "idle") {
		return true
	}
	return t == "" && strings.Contains(strings.ToLower(h.Message), "waiting for your input")
}

// permTool formats a tool call as in the CLI: Write(nb.txt), Bash(npm test).
func permTool(h Hook) string {
	if h.ToolName == "" {
		return ""
	}
	sum := oneLine(h.ToolSummary)
	if strings.HasPrefix(sum, "/") {
		sum = sum[strings.LastIndexByte(sum, '/')+1:]
	}
	if sum == "" {
		return h.ToolName
	}
	return h.ToolName + "(" + clip(sum, 80) + ")"
}

func toolActivity(h Hook) string {
	if h.ToolName == "" {
		return ""
	}
	if h.ToolSummary == "" {
		return h.ToolName
	}
	return h.ToolName + ": " + clip(oneLine(h.ToolSummary), 80)
}

// Notify applies a Codex notification (T13).
func (e *Engine) Notify(kind string, now time.Time) {
	if e.cfg.Shell {
		return
	}
	e.notify = true
	e.live = LiveNotify
	if e.fg != FgAgent {
		e.fg, e.manual = FgAgent, false
	}
	if kind == "" || kind == "agent-turn-complete" {
		switch e.layer {
		case Working, NeedsYou, Starting:
			e.setLayer(Ready, SrcNotify, "", now)
		}
	}
}

// Input is client or delivery input; a Return while ready starts a turn
// (T14) unless hooks report the state.
func (e *Engine) Input(data []byte, now time.Time) {
	if e.cfg.Shell || e.hooksMode() || e.fg == FgShell {
		return
	}
	if strings.ContainsRune(string(data), '\r') {
		e.lastSubmit = now
		switch {
		case e.layer == Ready:
			e.setLayer(Working, SrcActivity, "", now)
		case e.layer == NeedsYou && e.needsSrc == SrcScreen && e.genericSeen:
			e.answered = e.needsCand
			e.setLayer(Working, SrcActivity, "", now)
		}
	}
}

// Output records printed output (T17/T18 timing).
func (e *Engine) Output(now time.Time) { e.lastOutput = now }

// Screen applies the screen rules (T15–T17). With hooks active (verified
// on Claude Code 2.1.312) the screen still may raise needsYou, because the
// permission Notification arrives ~6 s after the dialog, and may end a
// needsYou (or a screen-set working) at the empty prompt, because a
// permission denied with Esc fires no hook at all; everything else stays
// with the hooks.
func (e *Engine) Screen(m Screen, now time.Time) {
	if e.cfg.Shell || e.fg == FgShell || !e.cfg.HasRules {
		return
	}
	if e.hooksMode() {
		e.hookScreen(m, now)
		return
	}
	if m.NeedsYou != "" {
		if m.NeedsYou != e.needsCand {
			e.needsCand, e.needsCandAt = m.NeedsYou, now
		}
		if now.Sub(e.needsCandAt) >= StableFor {
			switch e.layer {
			case Working, Ready, Starting: // T15 (+ dialogs at startup)
				e.setNeedsYou(SrcScreen, clip(m.NeedsYou, ActivityMaxLen), now)
			case NeedsYou:
				if e.needsSrc == SrcScreen {
					e.activity = clip(m.NeedsYou, ActivityMaxLen)
				}
			}
		}
		e.workCandAt = time.Time{}
		e.refresh(now)
		return
	}
	e.needsCand = ""
	if e.layer == NeedsYou && e.needsSrc == SrcScreen { // T16
		e.setLayer(Working, SrcScreen, "", now)
		return
	}
	if m.Working {
		if e.workCandAt.IsZero() {
			e.workCandAt = now
		}
		if (e.layer == Ready || e.layer == Starting) && now.Sub(e.workCandAt) >= StableFor {
			e.setLayer(Working, SrcScreen, "", now)
		}
		return
	}
	e.workCandAt = time.Time{}
	if m.Ready && now.Sub(e.lastOutput) >= ReadyQuiet { // T17
		switch {
		case e.layer == Starting:
			e.setLayer(Ready, SrcScreen, "", now)
		case e.layer == Working && (!(e.cfg.Notify && e.notify) || e.source == SrcScreen):
			// With notify working, only a screen-set working ends here
			// (a notify never comes for it).
			e.setLayer(Ready, SrcScreen, "", now)
		}
	}
}

// hookScreen is the narrow screen fallback while hooks drive the state
// (§6.2 rule 3): T15h raises needsYou during a turn (the permission
// Notification comes ~6 s late) and startup dialogs (folder trust, update)
// raise it outside a turn; a dialog a hook already resolved never does.
func (e *Engine) hookScreen(m Screen, now time.Time) {
	if m.NeedsYou != "" {
		if e.staleNeeds {
			return // the answered dialog is still drawn
		}
		if m.NeedsYou != e.needsCand {
			e.needsCand, e.needsCandAt = m.NeedsYou, now
		}
		raise := e.layer == Working || (m.Startup && (e.layer == Ready || e.layer == Starting))
		if raise && now.Sub(e.needsCandAt) >= StableFor {
			e.setNeedsYou(SrcScreen, clip(m.NeedsYou, ActivityMaxLen), now)
		}
		return
	}
	e.needsCand, e.staleNeeds = "", false
	if !m.Ready || m.Working || now.Sub(e.lastOutput) < ReadyQuiet {
		return
	}
	if e.layer == NeedsYou || (e.layer == Working && e.source == SrcScreen) {
		e.setLayer(Ready, SrcScreen, "", now)
	}
}

// GenericScreen takes the reading of the screen of an agent without rules
// (agent.GenericEvaluate). A dialog raises needsYou once it stands still, which
// keeps prose in a streaming reply from doing so, and its end returns to working;
// the rest of the reading refines T18 in Tick.
func (e *Engine) GenericScreen(m Screen, now time.Time) {
	if e.cfg.Shell || e.cfg.HasRules || e.fg == FgShell || e.hooksMode() {
		return
	}
	e.generic, e.genericSeen = m, true
	if m.NeedsYou != "" && m.NeedsYou == e.answered {
		return // the question just answered, still on the screen
	}
	e.answered = ""
	if m.NeedsYou == "" {
		e.needsCand = ""
		if e.layer == NeedsYou && e.needsSrc == SrcScreen { // T16
			e.setLayer(Working, SrcScreen, "", now)
		}
		return
	}
	if m.NeedsYou != e.needsCand {
		e.needsCand, e.needsCandAt = m.NeedsYou, now
	}
	if now.Sub(e.needsCandAt) >= StableFor && now.Sub(e.lastOutput) >= ReadyQuiet {
		switch e.layer {
		case Working, Ready, Starting:
			e.setNeedsYou(SrcScreen, clip(m.NeedsYou, ActivityMaxLen), now)
		case NeedsYou:
			if e.needsSrc == SrcScreen {
				e.activity = clip(m.NeedsYou, ActivityMaxLen)
			}
		}
	}
}

// Tick runs the timers: T18 for agents without screen rules and T19.
func (e *Engine) Tick(now time.Time) {
	if e.cfg.Shell || e.fg == FgShell || e.proc != ProcRunning {
		e.refresh(now)
		return
	}
	if e.layer == Starting && now.Sub(e.startedAt) > StartingMax { // T19
		e.setLayer(Ready, SrcActivity, "", now)
		return
	}
	if e.cfg.HasRules || e.hooksMode() || e.notify {
		e.refresh(now)
		return
	}
	idleAfter := IdleAfter
	if e.genericSeen && !e.generic.Ready {
		idleAfter = UnknownIdleAfter
	}
	switch { // T18
	case !e.lastOutput.IsZero() && now.Sub(e.lastOutput) < ActiveWindow, e.generic.Working:
		if e.layer == Ready || e.layer == Starting {
			e.setLayer(Working, SrcActivity, "", now)
		}
	case now.Sub(e.lastOutput) >= idleAfter && now.Sub(e.lastSubmit) >= idleAfter && now.Sub(e.startedAt) >= IdleAfter && !e.ioHolds(now, idleAfter):
		if e.layer == Working || e.layer == Starting {
			e.setLayer(Ready, SrcActivity, "", now)
		}
	}
	e.refresh(now)
}

// IO says the agent's processes just read a stream's worth of bytes: a model
// thinking with nothing on the screen yet. For an agent without screen rules it
// keeps working from turning ready (T18), for at most IOHoldMax after the last
// output or Return. It never makes a ready agent working: an update check would.
func (e *Engine) IO(now time.Time) {
	e.lastIO = now
}

func (e *Engine) ioHolds(now time.Time, quiet time.Duration) bool {
	if e.lastIO.IsZero() || now.Sub(e.lastIO) >= quiet {
		return false
	}
	busy := e.lastOutput
	if e.lastSubmit.After(busy) {
		busy = e.lastSubmit
	}
	return now.Sub(busy) < IOHoldMax
}

func (e *Engine) setNeedsYou(src, act string, now time.Time) {
	e.needsSrc = src
	if e.layer == NeedsYou {
		e.source, e.activity = src, clip(act, ActivityMaxLen)
		e.refresh(now)
		return
	}
	e.setLayer(NeedsYou, src, act, now)
}

func (e *Engine) setLayer(layer, src, act string, now time.Time) {
	if layer != NeedsYou {
		e.needsSrc = ""
	}
	if layer != e.layer {
		e.workCandAt = time.Time{} // a stale indicator must be stable again
	}
	e.layer, e.source = layer, src
	e.activity = clip(act, ActivityMaxLen)
	e.refresh(now)
}

// effective applies the source priority of §6.2.
func (e *Engine) effective() (string, string) {
	switch e.proc {
	case ProcError:
		return Error, SrcProcess
	case ProcReconnecting:
		return Reconnecting, SrcProcess
	case ProcExited:
		return Exited, SrcProcess
	case ProcStarting:
		return Starting, SrcProcess
	}
	if e.cfg.Shell {
		return Shell, SrcProcess
	}
	if e.fg == FgShell {
		if e.exit != nil {
			return Shell, SrcWrapper
		}
		return Shell, SrcProcess
	}
	return e.layer, e.source
}

// refresh recomputes the effective state and records turn timing and
// events on changes.
func (e *Engine) refresh(now time.Time) {
	st, src := e.effective()
	e.effSrc = src
	if st == e.eff {
		return
	}
	old := e.eff
	e.eff, e.effSince = st, now
	switch {
	case (st == Working || st == NeedsYou) && (old == Ready || old == Starting || old == ""):
		e.turnStart = now
		e.turn = &LastTurn{StartedAt: now}
	case st == Ready && (old == Working || old == NeedsYou) && !e.turnStart.IsZero():
		end := now
		t := LastTurn{StartedAt: e.turnStart, EndedAt: &end, DurationMs: now.Sub(e.turnStart).Milliseconds()}
		e.turn = &t
		e.turnStart = time.Time{}
		e.events = append(e.events, Event{Kind: EvFinished, Turn: t})
	case st != Working && st != NeedsYou:
		e.turnStart = time.Time{}
	}
	if st == NeedsYou {
		e.needsSince = now
		e.events = append(e.events, Event{Kind: EvNeedsYou, Activity: e.activity})
	} else {
		e.needsSince = time.Time{}
	}
	if st == Error {
		e.events = append(e.events, Event{Kind: EvError})
	}
}

// TakeEvents returns and clears pending events.
func (e *Engine) TakeEvents() []Event {
	ev := e.events
	e.events = nil
	return ev
}

// Snapshot returns the API view.
func (e *Engine) Snapshot() Snapshot {
	s := Snapshot{AgentState: e.eff, StateSource: e.effSrc, StateSince: e.effSince, Foreground: e.fg,
		ManualAgent: e.manual, LiveStatus: e.live, AgentExitCode: e.exit}
	if e.eff == Working || e.eff == NeedsYou {
		s.Activity = e.activity
	}
	if !e.needsSince.IsZero() {
		t := e.needsSince
		s.NeedsYouSince = &t
	}
	if e.turn != nil {
		t := *e.turn
		s.LastTurn = &t
	}
	if s.LiveStatus == "" {
		s.LiveStatus = LiveOff
	}
	return s
}

// Foreground returns the current foreground value.
func (e *Engine) Foreground() string { return e.fg }

// Layer returns the agent-layer state (tests, delivery guards).
func (e *Engine) Layer() string { return e.layer }

// HooksActive reports whether hooks drive the state in this run.
func (e *Engine) HooksActive() bool { return e.hooks }

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i] + " …"
	}
	return s
}

// clip shortens s to n runes (with an ellipsis).
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
