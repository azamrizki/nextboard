package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"sync"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/marker"
	"github.com/azamrizki/nextboard/internal/nextcode"
	"github.com/azamrizki/nextboard/internal/screen"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/azamrizki/nextboard/internal/transcript"
)

const (
	ScrollbackSize = 1 << 20 // 1 MiB per terminal
	SubBuffer      = 512
	busyWindow     = 2 * time.Second
	killGrace      = 3 * time.Second
	// activityEvery throttles activity-only status updates (§7).
	activityEvery = time.Second
	// maxEarlyInput is what is kept of input typed before a process exists: about what
	// a terminal's own line buffer holds.
	maxEarlyInput = 4 << 10
)

// Status values.
const (
	StatusStarting     = "starting"
	StatusRunning      = "running"
	StatusExited       = "exited"
	StatusReconnecting = "reconnecting"
	StatusError        = "error"
	// StatusAsleep: the agent's process was ended to free memory, or not started after a hub
	// restart; it resumes when woken (ROADMAP #50).
	StatusAsleep = "asleep"
)

// Spec is the persisted definition of a terminal (sessions.json).
type Spec struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Preset     string    `json:"preset"`
	MachineID  string    `json:"machineId"`
	Command    string    `json:"command"`
	Args       []string  `json:"args"`
	ResumeArgs []string  `json:"resumeArgs"`
	Env        []string  `json:"env"`
	Cwd        string    `json:"cwd"`
	Cols       uint16    `json:"cols"`
	Rows       uint16    `json:"rows"`
	TmuxName   string    `json:"tmuxName"`
	CreatedAt  time.Time `json:"createdAt"`
	// ProjectID/WorkspaceID place the terminal on a workspace board.
	ProjectID   string `json:"projectId"`
	WorkspaceID string `json:"workspaceId"`
	// PartID is the part of the project it works in (ROADMAP #49), by id. "" with Root: a root
	// console. "" without Root: in no part's folder (from v0.9.0-beta.2, once the start-up
	// migration has stamped the sessions from before with their part; until then "" was read as
	// the first part). Never resolve "" to the first part in new code.
	PartID string `json:"partId,omitempty"`
	// Root: the console works at the project root, reaching every part (v0.9.0-beta.2,
	// docs/API-board.md §1): on This PC (machineId "local"), cwd the hub-made root folder
	// <data>/roots/<projectId>, PartID "". Fixed for the console's life: restart, sleep and wake
	// keep it. A single-part project has no root, so its consoles never have it.
	Root bool `json:"root,omitempty"`
	// Panel: a terminal or agent of the Code workbench, with no board node.
	Panel bool `json:"panel,omitempty"`
	// Card: of a Panel session, the editor card (board node id) it was opened in, so that an
	// orchestrator card wired to that editor directs it (docs/API-studio.md §7.8). "" for one
	// opened before cards were recorded: the editor card of its placement holds it.
	Card string `json:"card,omitempty"`
	// SignIn: the console that signs a login profile in (Settings › Profiles). It is the
	// profile's own CLI: no order, loop or task ever types into it.
	SignIn bool `json:"signIn,omitempty"`

	// Phase 5 (docs/API-agents.md §7). Agent == Preset.
	// An agent runs on This PC (MachineID); these say which server folder it
	// works on through hub tools (docs/API-agents.md §21).
	RemoteMachineID string `json:"remoteMachineId,omitempty"`
	RemoteCwd       string `json:"remoteCwd,omitempty"`

	// PastRuns are the agents this console ran before, newest first, so that
	// going back continues their conversation.
	PastRuns []PastRun `json:"pastRuns,omitempty"`

	Handle    string `json:"handle"`
	Agent     string `json:"agent"`
	AgentKind string `json:"agentKind"`
	Yolo      bool   `json:"yolo"`
	ProfileID string `json:"profileId"`
	// ProviderID: the console runs against this API provider instead of a login (§10.2).
	ProviderID     string `json:"providerId,omitempty"`
	AgentSessionID string `json:"agentSessionId"`
	// Model and Effort survive a restart (docs/API-agents.md §25): "" = the CLI's own choice.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`

	// Phase 6 (docs/API-orchestration.md §8): the foreground process group
	// is stopped (SIGSTOP) by the hub. Persisted; local sessions restarted
	// by a hub restart come back unpaused.
	Paused   bool       `json:"paused"`
	PausedAt *time.Time `json:"pausedAt"`

	// Asleep (ROADMAP #50, docs/API-agents.md §28): the hub ended the agent's process to free
	// memory, or did not start it after a restart. It resumes its conversation when someone
	// opens its terminal, types into it or sends it work. KeepAwake: never put to sleep.
	Asleep    bool       `json:"asleep,omitempty"`
	AsleepAt  *time.Time `json:"asleepAt,omitempty"`
	KeepAwake bool       `json:"keepAwake,omitempty"`
}

// private is persisted in sessions.json but never sent to clients.
type private struct {
	HookToken      string `json:"hookToken,omitempty"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
	LastForeground string `json:"lastForeground,omitempty"`
	WrapperPid     int    `json:"wrapperPid,omitempty"`
	// ModelAt: what a transcript says before this no longer counts.
	ModelAt time.Time `json:"modelAt,omitempty"`
	// Draft is what was typed at the agent's prompt and not sent when it went to sleep; it is
	// typed back when it wakes. DraftFailed: that did not work, so the owner gets it to copy.
	Draft       string `json:"draft,omitempty"`
	DraftFailed bool   `json:"draftFailed,omitempty"`
}

// PastRun is an agent's conversation that was left when the console switched.
type PastRun struct {
	Agent          string    `json:"agent"`
	AgentSessionID string    `json:"agentSessionId,omitempty"`
	ProfileID      string    `json:"profileId,omitempty"`
	ProviderID     string    `json:"providerId,omitempty"`
	Model          string    `json:"model,omitempty"`
	Effort         string    `json:"effort,omitempty"`
	At             time.Time `json:"at"`
}

// stored is one entry of sessions.json.
type stored struct {
	Spec
	private
}

// ReplyInfo summarises the cached last reply (null until there is one).
type ReplyInfo struct {
	At         time.Time `json:"at"`
	Chars      int       `json:"chars"`
	Paragraphs int       `json:"paragraphs"`
}

// Info is the API view of a session.
type Info struct {
	Spec
	Status       string    `json:"status"`
	ExitCode     int       `json:"exitCode"`
	Busy         bool      `json:"busy"`
	StartedAt    time.Time `json:"startedAt"`
	LastOutputAt time.Time `json:"lastOutputAt"`
	Error        string    `json:"error"`
	Color        string    `json:"color"`
	MachineName  string    `json:"machineName"`
	agentstate.Snapshot
	ResumeOffer bool       `json:"resumeOffer"`
	Reply       *ReplyInfo `json:"reply"`
	// Queue is the number of active deliveries (docs/API-orchestration.md
	// §2); Subagents counts Task/Agent tool calls without their
	// PostToolUse (hooks only, Mission Map §7).
	Queue     int `json:"queue"`
	Subagents int `json:"subagents"`
	// Job is the command in the foreground of a shell console; Active says the console
	// is really processing (docs/API-agents.md §6.1).
	Job    string `json:"job"`
	Active bool   `json:"active"`
	// UnsentDraft is what was typed before the console slept and could not be typed back.
	UnsentDraft string `json:"unsentDraft,omitempty"`
}

// StatusMsg is pushed to terminal subscribers when status changes.
type StatusMsg struct {
	Status        string     `json:"status"`
	ExitCode      int        `json:"exitCode"`
	Busy          bool       `json:"busy"`
	AgentState    string     `json:"agentState"`
	Activity      string     `json:"activity"`
	NeedsYouSince *time.Time `json:"needsYouSince"`
	Foreground    string     `json:"foreground"`
	Job           string     `json:"job"`
	Active        bool       `json:"active"`
}

// SizeMsg is the terminal's size after a change: every view sets its screen to it, so what the
// program draws for that size shows right in each, however big the view is.
type SizeMsg struct {
	Cols uint16
	Rows uint16
}

// Msg is one item on a subscriber channel: output bytes, a status change or a new size.
type Msg struct {
	Data   []byte
	Status *StatusMsg
	Size   *SizeMsg
}

// Sub is a terminal subscriber (one WebSocket). C is closed when the
// subscriber is dropped (too slow, session deleted).
type Sub struct {
	C    chan Msg
	once sync.Once

	// sizes are the output bytes of the messages sent to C, oldest first, and queued their
	// sum: those still in C are the last len(C) of them (one sender, under the session's lock).
	sizes  []int
	queued int

	// the size its view would take (0 until it says) and when it was last typed into or
	// focused: the view last used decides the terminal's size (the session's lock)
	wantCols, wantRows uint16
	used               time.Time
}

func (s *Sub) close() { s.once.Do(func() { close(s.C) }) }

// SubMaxBytes is the output a subscriber may have waiting; one that falls further behind is
// dropped like one whose channel is full.
const SubMaxBytes = 4 << 20

// fits accounts for a message of n output bytes about to be sent, and reports whether the
// subscriber may have it: what the reader took since the last send no longer counts.
func (s *Sub) fits(n int) bool {
	if gone := len(s.sizes) - len(s.C); gone > 0 {
		for _, k := range s.sizes[:gone] {
			s.queued -= k
		}
		s.sizes = s.sizes[gone:]
	}
	if n > 0 && s.queued+n > SubMaxBytes && s.queued > 0 {
		return false
	}
	return true
}

func (s *Sub) sent(n int) {
	s.sizes = append(s.sizes, n)
	s.queued += n
}

// run is one launched process generation.
type run struct {
	proc machine.Proc
	gen  int
	done chan struct{}
}

// Session is one terminal node.
type Session struct {
	m *Manager

	launchMu sync.Mutex // serialises launch / stop / restart
	// inMu keeps what clients send (input, size) in order, also across the start of a process.
	inMu sync.Mutex

	mu       sync.Mutex
	spec     Spec
	priv     private
	status   string
	exitCode int
	errMsg   string
	busy     bool
	// What runs in the foreground of a shell console ("" at its prompt). jobKnown says
	// the hub can tell: an SSH shell without tmux has no pid to ask about.
	job      string
	jobKnown bool
	jobAt    time.Time
	jobBusy  bool // a check is on its way
	panePid  int  // the tmux pane of an SSH shell
	// scrollBy: wheel lines not sent to its tmux yet; scrolling: one Scroll is sending them
	scrollBy   int
	scrolling  bool
	startedAt  time.Time
	lastOutput time.Time
	ring       *Ring
	subs       map[*Sub]struct{}
	// driver is the subscriber whose view decides the terminal's size (SubResize, Activate)
	driver  *Sub
	cur     *run
	gen     int
	killing bool
	deleted bool
	dirty   bool // scrollback changed since last flush
	// ringOff: the scrollback is only in its file (parkRingLocked); ring is empty until
	// ringLocked reads it back.
	ringOff bool
	// ringNote goes after the file's scrollback when it is read back: the "hub restarted" line
	// of a console that was not loaded at hub start (Manager.Load).
	ringNote string
	early    []byte // typed while the process was starting; it gets this once it exists

	// Phase 5: status engine, marker stripper and VT model.
	ag          agent.Agent // agent of the current launch
	eng         *agentstate.Engine
	strip       marker.Stripper
	scr         *screen.Screen
	rules       *agent.ScreenRules
	match       agentstate.Screen
	scrVersion  uint64
	scrEvalAt   time.Time
	lastInput   time.Time
	lastSnap    agentstate.Snapshot
	actPending  bool
	actSentAt   time.Time
	resumeOffer bool
	reply       *Reply
	outTotal    int64 // clean bytes written since the session was loaded
	turnOut     int64 // outTotal when the current turn started
	startWait   []chan struct{}
	fgCheckAt   time.Time
	io          ioSample  // what the agent's processes read, for agents without screen rules
	codexFind   time.Time // agent-start of a Codex run still looking for its rollout
	codexTry    time.Time
	refreshing  bool
	subagents   int

	// Sleeping (sleep.go): what is typed at the prompt, the process being ended to sleep, and
	// a wake in progress with the keys typed meanwhile.
	draft    draftTrack
	sleeping bool
	waking   bool
	wakeIn   []byte
	// wakeAfter: its terminal was opened while it went to sleep.
	wakeAfter bool
	// sleepRetryAt: the idle sleep found a command running under it; it is not asked again
	// before this (sleepRetryAfter).
	sleepRetryAt time.Time

	// modelScan is how far syncModel has read the transcript (modelMu).
	modelMu   sync.Mutex
	modelScan transcript.ModelScan

	// nc drives a NextCode console's CLI (nextcode.go); nil for a terminal console.
	nc *nextcode.Driver
	// handOver: the next launch moves the chat to another account (SwitchChatAccount).
	handOver bool
}

func newSession(m *Manager, spec Spec) *Session {
	s := &Session{m: m, spec: spec, status: StatusStarting, ring: NewRing(ScrollbackSize), subs: map[*Sub]struct{}{}}
	s.scr = screen.New(int(max(spec.Cols, 10)), int(max(spec.Rows, 4)), nil)
	s.setAgentLocked(m.agentFor(spec))
	return s
}

// setAgentLocked (re)creates the engine for the console's agent.
func (s *Session) setAgentLocked(a agent.Agent) {
	s.ag = a
	s.rules = agent.RulesFor(a.ScreenRules)
	cfg := agentstate.Config{Shell: a.Kind == agent.KindShell, Notify: a.LiveStatus == agent.LiveCodexNotify, HasRules: s.rules != nil}
	now := time.Now()
	s.eng = agentstate.New(cfg, now)
	s.eng.SetProc(s.status, now)
	s.lastSnap = s.eng.Snapshot()
}

// ID returns the session id.
func (s *Session) ID() string { return s.spec.ID }

// Spec returns a copy of the persisted spec.
// HookTokenOK reports whether tok is this console's own token.
func (s *Session) HookTokenOK(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return marker.TokenOK(tok, s.priv.HookToken)
}

func (s *Session) Spec() Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spec
}

func (s *Session) stored() stored {
	s.mu.Lock()
	defer s.mu.Unlock()
	return stored{Spec: s.spec, private: s.priv}
}

func (s *Session) infoLocked() Info {
	in := Info{
		Spec: s.spec, Status: s.status, ExitCode: s.exitCode, Busy: s.busy,
		StartedAt: s.startedAt, LastOutputAt: s.lastOutput, Error: s.errMsg,
		Snapshot: s.eng.Snapshot(), ResumeOffer: s.resumeOffer, Subagents: s.subagents,
		Job: s.jobLocked(), Active: s.activeLocked(),
	}
	if s.reply != nil {
		in.Reply = &ReplyInfo{At: s.reply.At, Chars: s.reply.Chars, Paragraphs: s.reply.Paragraphs}
	}
	if s.priv.DraftFailed {
		in.UnsentDraft = s.priv.Draft
	}
	return in
}

// ringLocked is the scrollback, read back from its file if it was let go.
func (s *Session) ringLocked() *Ring {
	if s.ringOff {
		s.ringOff = false
		s.ring = s.fileRingLocked()
		s.ringNote = ""
	}
	return s.ring
}

// fileRingLocked is the scrollback as its file holds it, with ringNote after it.
func (s *Session) fileRingLocked() *Ring {
	r := NewRing(ScrollbackSize)
	if b, err := os.ReadFile(s.m.scrollbackPath(s.spec.ID)); err == nil {
		r.Write(b)
	}
	if s.ringNote != "" {
		r.Write([]byte(s.ringNote))
	}
	return r
}

// ringBytesLocked is what the scrollback holds, without taking it back into memory.
func (s *Session) ringBytesLocked() []byte {
	if s.ringOff {
		if s.ringNote != "" {
			return s.fileRingLocked().Bytes()
		}
		b, _ := os.ReadFile(s.m.scrollbackPath(s.spec.ID))
		return b
	}
	return s.ring.Bytes()
}

// parkRingLocked writes the scrollback to its file and lets it go, for a console whose process
// has ended (asleep, exited): up to ScrollbackSize each, for consoles nobody may open for days.
// Opened, or running again, it is read back.
func (s *Session) parkRingLocked() {
	if s.ringOff || s.deleted || s.ring.Len() == 0 || s.m.isShuttingDown() {
		return
	}
	if store.WriteFileAtomic(s.m.scrollbackPath(s.spec.ID), s.ring.Bytes()) != nil {
		return // kept in memory, as before
	}
	s.dirty = false
	s.ring, s.ringOff = NewRing(ScrollbackSize), true
}

// InMemory reports whether the console's screen emulator and its scrollback are held in
// memory; neither is while it sleeps or after it ended.
func (s *Session) InMemory() (screen, scrollback bool) {
	s.mu.Lock()
	scrollback = !s.ringOff
	s.mu.Unlock()
	return !s.scr.Parked(), scrollback
}

// Subscribe returns the scrollback snapshot and a live subscriber.
// An asleep agent wakes: its terminal is open.
func (s *Session) Subscribe() ([]byte, *Sub) {
	s.mu.Lock()
	sub := &Sub{C: make(chan Msg, SubBuffer)}
	if s.deleted {
		s.mu.Unlock()
		sub.close()
		return nil, sub
	}
	s.subs[sub] = struct{}{}
	// read from its file when it was let go, and left there: opening a console that ended
	// does not take its scrollback back into memory (output does, through ringLocked)
	snap := s.ringBytesLocked()
	asleep := s.spec.Asleep
	if s.sleeping {
		s.wakeAfter = true
	}
	s.mu.Unlock()
	if asleep {
		s.Wake()
	}
	return snap, sub
}

// Unsubscribe removes a subscriber. When its view decided the size, the view used last of
// those left decides it now.
func (s *Session) Unsubscribe(sub *Sub) {
	s.mu.Lock()
	delete(s.subs, sub)
	var next *Sub
	if s.driver == sub {
		s.driver = nil
		for o := range s.subs {
			if o.wantCols > 0 && (next == nil || o.used.After(next.used)) {
				next = o
			}
		}
		s.driver = next
	}
	s.mu.Unlock()
	sub.close()
	if next != nil {
		s.Resize(next.wantCols, next.wantRows)
	}
}

// Size is the terminal's size now.
func (s *Session) Size() (cols, rows uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spec.Cols, s.spec.Rows
}

// SubResize records the size sub's view would take. It applies when that view decides the
// size: the one last typed into or focused (Activate), else the first to say. Two views of one
// console then never pull its size back and forth, which made a program draw for one size what
// the other showed at another (a full-screen agent's input box twice).
func (s *Session) SubResize(sub *Sub, cols, rows uint16) {
	s.mu.Lock()
	_, live := s.subs[sub]
	sub.wantCols, sub.wantRows = cols, rows
	_, driverLive := s.subs[s.driver]
	drive := live && (s.driver == sub || s.driver == nil || !driverLive)
	if drive {
		s.driver = sub
	}
	s.mu.Unlock()
	if drive {
		s.Resize(cols, rows)
	}
}

// Activate makes sub's view the one that decides the size: it was typed into or focused.
func (s *Session) Activate(sub *Sub) {
	s.mu.Lock()
	if _, live := s.subs[sub]; !live {
		s.mu.Unlock()
		return
	}
	sub.used = time.Now()
	was := s.driver
	s.driver = sub
	cols, rows := sub.wantCols, sub.wantRows
	s.mu.Unlock()
	if was != sub && cols > 0 && rows > 0 {
		s.Resize(cols, rows)
	}
}

// fanoutLocked delivers msg to every subscriber; full ones, and ones with more than
// SubMaxBytes of output waiting, are dropped (the client reconnects and repaints from the
// snapshot).
func (s *Session) fanoutLocked(msg Msg) {
	n := len(msg.Data)
	for sub := range s.subs {
		if !sub.fits(n) {
			delete(s.subs, sub)
			sub.close()
			continue
		}
		select {
		case sub.C <- msg:
			sub.sent(n)
		default:
			delete(s.subs, sub)
			sub.close()
		}
	}
}

// writeLocked appends hub-generated text to scrollback and subscribers.
func (s *Session) writeLocked(text string) {
	b := []byte(text)
	s.ringLocked().Write(b)
	s.dirty = true
	s.scr.Feed(b)
	s.fanoutLocked(Msg{Data: b})
}

func (s *Session) statusMsgLocked() Msg {
	sn := s.eng.Snapshot()
	return Msg{Status: &StatusMsg{Status: s.status, ExitCode: s.exitCode, Busy: s.busy,
		AgentState: sn.AgentState, Activity: sn.Activity, NeedsYouSince: sn.NeedsYouSince, Foreground: sn.Foreground,
		Job: s.jobLocked(), Active: s.activeLocked()}}
}

// setStatus updates status and notifies subscribers + the manager.
func (s *Session) setStatus(status string, code int, errMsg string) {
	s.mu.Lock()
	s.status, s.exitCode, s.errMsg = status, code, errMsg
	if status != StatusRunning {
		s.busy = false
	}
	now := time.Now()
	s.eng.SetProc(status, now)
	s.lastSnap = s.eng.Snapshot()
	s.fanoutLocked(s.statusMsgLocked())
	ev := s.eng.TakeEvents()
	s.mu.Unlock()
	if parks(status) {
		s.park(status)
	}
	s.m.changed(s)
	s.m.attention(s, ev)
}

// parks reports whether a console in status has no process drawing on its screen until it
// runs again: asleep, done, failed to start, or waiting for its machine.
func parks(status string) bool {
	switch status {
	case StatusAsleep, StatusExited, StatusError, StatusReconnecting:
		return true
	}
	return false
}

// park lets the screen's emulator go (the text stays) and leaves the scrollback in its file,
// unless the console moved on from status meanwhile.
func (s *Session) park(status string) {
	s.scr.Park()
	s.mu.Lock()
	if s.status == status {
		s.parkRingLocked()
	}
	s.mu.Unlock()
}

// output handles one read from the process: markers are stripped first,
// so they never reach scrollback, WebSockets or the VT model.
func (s *Session) output(p []byte, gen int) {
	s.mu.Lock()
	if gen != s.gen || s.deleted {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	clean, ms := s.strip.Feed(p, now)
	if s.sleeping {
		// going to sleep: what it prints on the way out is kept, its state changes are not
		s.emitLocked(clean, now)
		s.mu.Unlock()
		return
	}
	becameBusy := s.emitLocked(clean, now)
	for _, mk := range ms {
		s.markerLocked(mk, now)
	}
	notify, ev := s.afterLocked(now, becameBusy)
	id := s.spec.ID
	s.mu.Unlock()
	s.finish(notify, ev)
	if len(clean) > 0 && s.m.OnOutput != nil {
		s.m.OnOutput(id, clean)
	}
}

// emitLocked passes clean output on. It reports whether busy flipped on.
func (s *Session) emitLocked(clean []byte, now time.Time) bool {
	if len(clean) == 0 {
		return false
	}
	s.ringLocked().Write(clean)
	s.dirty = true
	s.outTotal += int64(len(clean))
	s.lastOutput = now
	s.eng.Output(now)
	s.scr.Feed(clean)
	becameBusy := !s.busy && s.status == StatusRunning
	if becameBusy {
		s.busy = true
	}
	s.fanoutLocked(Msg{Data: append([]byte(nil), clean...)})
	return becameBusy
}

// afterLocked compares the engine with the last published snapshot. State,
// foreground and live-status changes go out at once; activity-only changes
// at most once per second.
func (s *Session) afterLocked(now time.Time, force bool) (bool, []agentstate.Event) {
	sn := s.eng.Snapshot()
	old := s.lastSnap
	major := force || sn.AgentState != old.AgentState || sn.Foreground != old.Foreground || sn.LiveStatus != old.LiveStatus ||
		!eqIntPtr(sn.AgentExitCode, old.AgentExitCode) || sn.ManualAgent != old.ManualAgent
	minor := sn.Activity != old.Activity
	ev := s.eng.TakeEvents()
	for _, e := range ev {
		if e.Kind == agentstate.EvFinished || e.Kind == agentstate.EvNeedsYou {
			major = true
		}
	}
	if sn.AgentState == agentstate.Working && old.AgentState != agentstate.Working && old.AgentState != agentstate.NeedsYou {
		s.turnOut = s.outTotal
	}
	switch {
	case major:
	case minor && now.Sub(s.actSentAt) >= activityEvery:
	case minor:
		s.actPending = true
		return false, ev
	default:
		return false, ev
	}
	s.lastSnap, s.actPending, s.actSentAt = sn, false, now
	s.fanoutLocked(s.statusMsgLocked())
	return true, ev
}

func eqIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// finish publishes after the lock is released.
func (s *Session) finish(notify bool, ev []agentstate.Event) {
	if notify {
		s.m.changed(s)
	}
	s.m.attention(s, ev)
	for _, e := range ev {
		if e.Kind == agentstate.EvFinished {
			s.refreshReply()
		}
	}
}

// checkIdle clears busy after busyWindow without output.
func (s *Session) checkIdle(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy && now.Sub(s.lastOutput) > busyWindow {
		s.busy = false
		s.fanoutLocked(s.statusMsgLocked())
		return true
	}
	return false
}

// launch starts the process in the given mode. Caller holds launchMu.
func (s *Session) launch(mode string) {
	// However this ends, what was typed during this start has no later process to go to.
	defer func() {
		s.mu.Lock()
		s.early = nil
		s.mu.Unlock()
	}()
	s.syncModel()
	s.mu.Lock()
	if s.deleted || s.m.isShuttingDown() {
		s.mu.Unlock()
		return
	}
	spec := s.spec
	s.gen++
	gen := s.gen
	s.killing = false
	s.mu.Unlock()
	if a := s.m.agentFor(spec); a.Engine != "" {
		s.launchChat(spec, a, mode)
		return
	}

	host, ok := s.m.machines.Host(spec.MachineID)
	if !ok {
		s.mu.Lock()
		s.writeLocked("\r\n\x1b[31mnextboard: machine not found\x1b[0m\r\n")
		s.mu.Unlock()
		s.setStatus(StatusError, -1, "machine not found")
		return
	}
	s.setStatus(StatusStarting, 0, "")
	// A root console's folder lists the project's parts as they are now (Manager.RootDir).
	if spec.Root {
		var err error
		if s.m.RootDir == nil {
			err = errors.New("this hub has no project root folders")
		} else {
			_, err = s.m.RootDir(spec.ProjectID)
		}
		if err != nil {
			s.mu.Lock()
			s.writeLocked(fmt.Sprintf("\r\n\x1b[1;31mnextboard: the project's root folder could not be made: %s\x1b[0m\r\n", err))
			s.mu.Unlock()
			s.setStatus(StatusError, -1, "root_unavailable: "+err.Error())
			return
		}
	}

	a := s.m.agentFor(spec)
	ss := machine.StartSpec{
		SessionID: spec.ID, Cwd: spec.Cwd, Env: spec.Env, Cols: spec.Cols, Rows: spec.Rows, TmuxName: spec.TmuxName,
	}
	reattach := false
	if spec.TmuxName != "" {
		if sm, ok := s.m.machines.SSH(spec.MachineID); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			reattach, _ = sm.TmuxHas(ctx, spec.TmuxName)
			cancel()
		}
	}
	var plan agent.Plan
	if a.Kind == agent.KindShell {
		ss.Command, ss.Args = spec.Command, spec.Args
		plan = agent.Plan{Mode: agent.ModeShell, LiveStatus: agentstate.LiveOff}
	} else {
		var err error
		plan, err = s.m.plan(s, a, spec, mode)
		if err != nil {
			s.mu.Lock()
			s.writeLocked(fmt.Sprintf("\r\n\x1b[1;31mnextboard: %s\x1b[0m\r\n", err))
			s.mu.Unlock()
			s.setStatus(StatusError, -1, err.Error())
			return
		}
		ss.Shell, ss.Env, ss.Check = plan.Shell, plan.Env, plan.Check
		if spec.MachineID != machine.LocalID {
			ss.Check = ""
		}
	}
	if spec.MachineID == machine.LocalID && len(s.m.LocalEnv) > 0 {
		ss.Env = append(ss.Env[:len(ss.Env):len(ss.Env)], s.m.LocalEnv...) // a copy: spec.Env stays
	}
	proc, err := host.Start(ss)
	if err != nil {
		var nf *machine.CommandNotFoundError
		switch {
		case errors.Is(err, machine.ErrNotConnected):
			s.setStatus(StatusReconnecting, 0, "waiting for the machine to connect")
		case errors.As(err, &nf):
			s.mu.Lock()
			s.writeLocked(fmt.Sprintf("\r\n\x1b[1;31mnextboard: command not found: %s\x1b[0m\r\n\x1b[31mCheck that the CLI is installed and on the login shell's PATH.\x1b[0m\r\n", nf.Command))
			s.mu.Unlock()
			s.setStatus(StatusError, 127, err.Error())
		default:
			s.mu.Lock()
			s.writeLocked(fmt.Sprintf("\r\n\x1b[1;31mnextboard: failed to run: %s\x1b[0m\r\n", err))
			s.mu.Unlock()
			s.setStatus(StatusError, -1, err.Error())
		}
		return
	}
	r := &run{proc: proc, gen: gen, done: make(chan struct{})}
	s.inMu.Lock()
	defer s.inMu.Unlock()
	s.mu.Lock()
	if gen != s.gen || s.deleted {
		s.mu.Unlock()
		proc.Signal("KILL")
		proc.Close()
		return
	}
	s.cur = r
	s.startedAt = time.Now()
	s.job, s.jobKnown, s.jobAt, s.panePid = "", false, time.Time{}, 0
	if !reattach {
		s.spec.Paused, s.spec.PausedAt = false, nil // a new process is never stopped
		s.draft.reset()
	}
	s.spec.Asleep, s.spec.AsleepAt = false, nil
	s.subagents = 0
	if s.ag.ID != a.ID || s.ag.Kind != a.Kind {
		s.setAgentLocked(a)
	} else {
		s.ag = a
	}
	s.strip = marker.Stripper{Token: s.priv.HookToken}
	now := time.Now()
	if reattach {
		s.eng.Reattach(s.priv.LastForeground, now)
	} else {
		s.eng.NewRun(plan.Mode, plan.LiveStatus, now)
		s.priv.WrapperPid = 0
		if plan.Mode == agent.ModeShell {
			s.priv.LastForeground = agentstate.FgShell
		}
		if a.Kind != agent.KindShell && plan.Mode != agent.ModeShell {
			s.spec.AgentSessionID = plan.AgentSessionID
			if plan.Mode == agent.ModeFresh || plan.AgentSessionID == "" {
				s.priv.TranscriptPath = ""
			}
		}
	}
	if lp, ok := proc.(interface{ Pid() int }); ok && spec.MachineID == machine.LocalID && a.Kind != agent.KindShell {
		s.priv.WrapperPid = lp.Pid()
	}
	early := s.early
	s.early = nil
	if reattach {
		early = nil // it was already running: it is not waiting for this
	}
	if len(early) > 0 {
		s.lastInput = now
		s.eng.Input(early, now)
		s.draft.unsure = true // typed before its prompt was there
	}
	cols, rows := s.spec.Cols, s.spec.Rows
	s.mu.Unlock()
	if cols != spec.Cols || rows != spec.Rows {
		_ = proc.Resize(cols, rows) // a client's size from while the process was starting
	}
	s.m.saveSoon()
	s.setStatus(StatusRunning, 0, "")

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := proc.Read(buf)
			if n > 0 {
				s.output(buf[:n], gen)
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		code, werr := proc.Wait()
		select {
		case <-readerDone:
		case <-time.After(500 * time.Millisecond):
		}
		proc.Close()
		<-readerDone
		s.exited(r, code, werr)
	}()
	// Last, with its output being read: a terminal echoes what it is given.
	if len(early) > 0 {
		_, _ = proc.Write(early)
	}
}

func (s *Session) exited(r *run, code int, werr error) {
	defer close(r.done)
	s.mu.Lock()
	if s.cur == r {
		s.cur = nil
	}
	if r.gen != s.gen || s.deleted || s.m.isShuttingDown() {
		s.mu.Unlock()
		return
	}
	if held := s.strip.Flush(time.Now().Add(marker.HoldMax)); len(held) > 0 {
		s.emitLocked(held, time.Now())
	}
	if s.sleeping {
		s.sleeping = false
		s.asleepLocked(asleepNote)
		back := s.wakeAfter || len(s.wakeIn) > 0 // someone came back while it went
		s.wakeAfter = false
		s.mu.Unlock()
		s.setStatus(StatusAsleep, 0, "")
		s.m.saveNow()
		if back {
			s.Wake()
		}
		return
	}
	killed := s.killing
	disconnected := errors.Is(werr, machine.ErrDisconnected) && !killed
	if disconnected {
		s.writeLocked("\r\n\x1b[33m── connection lost, reconnecting… ──\x1b[0m\r\n")
	} else {
		color := "90"
		if code != 0 && !killed {
			color = "31"
		}
		s.writeLocked(fmt.Sprintf("\r\n\x1b[%sm── process exited (code %d) ──\x1b[0m\r\n", color, code))
	}
	s.mu.Unlock()
	if disconnected {
		s.setStatus(StatusReconnecting, 0, "")
		s.m.relaunchWhenConnected(s)
		return
	}
	s.setStatus(StatusExited, code, "")
}

// stop terminates the current process: HUP, then KILL after 3 s.
// Caller holds launchMu.
func (s *Session) stop() {
	s.mu.Lock()
	r := s.cur
	s.killing = true
	s.mu.Unlock()
	if r == nil {
		return
	}
	_ = r.proc.Signal("HUP")
	select {
	case <-r.done:
		return
	case <-time.After(killGrace):
	}
	_ = r.proc.Signal("KILL")
	select {
	case <-r.done:
	case <-time.After(killGrace):
		r.proc.Close()
	}
}

// Input writes keyboard input to the process. A Return while the agent is
// ready starts a turn (T14). What is typed while the process is starting goes to it
// when it is there, like typing ahead in any terminal.
func (s *Session) Input(p []byte) error {
	s.inMu.Lock()
	defer s.inMu.Unlock()
	s.mu.Lock()
	if s.ag.Engine != "" {
		s.mu.Unlock()
		return ErrNextCodeInput
	}
	if s.spec.Asleep || s.sleeping || s.waking {
		// it wakes, and gets the keys at its prompt (after what it had there)
		typed := TypedInput(p)
		if len(s.wakeIn)+len(typed) <= maxEarlyInput {
			s.wakeIn = append(s.wakeIn, typed...)
		}
		asleep := s.spec.Asleep
		s.mu.Unlock()
		if asleep {
			s.Wake()
		}
		return nil
	}
	s.mu.Unlock()
	return s.inputLocked(p)
}

// inputLocked is Input with inMu held.
func (s *Session) inputLocked(p []byte) error {
	s.mu.Lock()
	r := s.cur
	if r == nil && s.status == StatusStarting {
		// keys only: a terminal's own reports answer a process that is gone
		typed := TypedInput(p)
		if len(s.early)+len(typed) <= maxEarlyInput {
			s.early = append(s.early, typed...)
			s.mu.Unlock()
			return nil
		}
	}
	if r != nil {
		now := time.Now()
		s.lastInput = now
		s.eng.Input(p, now)
		s.trackLocked(TypedInput(p))
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

// trackLocked follows what reaches the agent's prompt; keys for a dialog do not.
func (s *Session) trackLocked(p []byte) {
	if len(p) == 0 || s.eng.Snapshot().AgentState == agentstate.NeedsYou {
		return
	}
	s.draft.feed(p)
}

// Resize sets the terminal's size (SubResize decides which view's size it is).
func (s *Session) Resize(cols, rows uint16) {
	cols = clamp(cols, 10, 500)
	rows = clamp(rows, 4, 300)
	s.inMu.Lock()
	defer s.inMu.Unlock()
	s.mu.Lock()
	if s.spec.Cols == cols && s.spec.Rows == rows {
		s.mu.Unlock()
		return
	}
	s.spec.Cols, s.spec.Rows = cols, rows
	r := s.cur
	// every view takes the new size (SizeMsg)
	s.fanoutLocked(Msg{Size: &SizeMsg{Cols: cols, Rows: rows}})
	s.mu.Unlock()
	s.scr.Resize(int(cols), int(rows))
	if r != nil {
		_ = r.proc.Resize(cols, rows)
	}
	s.m.saveSoon()
}

// Scroll moves a console kept in tmux on a server by n lines of its history, up when n < 0, for
// a mouse wheel over it (machine.SSH.TmuxScroll). Lines asked for while one command is on its way
// add up and go in the next, so a fast wheel costs a command at a time. Other consoles: nothing,
// the browser scrolls their scrollback itself.
func (s *Session) Scroll(n int) {
	s.mu.Lock()
	name, mid := s.spec.TmuxName, s.spec.MachineID
	if name == "" || n == 0 {
		s.mu.Unlock()
		return
	}
	s.scrollBy += n
	if s.scrolling {
		s.mu.Unlock()
		return
	}
	s.scrolling = true
	s.mu.Unlock()
	sm, ok := s.m.machines.SSH(mid)
	go func() {
		for {
			s.mu.Lock()
			k := s.scrollBy
			s.scrollBy = 0
			if k == 0 || !ok {
				s.scrolling = false
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = sm.TmuxScroll(ctx, name, k)
			cancel()
		}
	}()
}

// Screen returns a copy of the VT model's visible screen.
func (s *Session) Screen() screen.Snapshot { return s.scr.Snapshot() }

// Reply returns the cached last reply.
func (s *Session) Reply() (*Reply, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reply == nil {
		return nil, false
	}
	r := *s.reply
	return &r, true
}

// Reply is GET /api/sessions/{id}/reply.
type Reply struct {
	SessionID      string    `json:"sessionId"`
	AgentSessionID string    `json:"agentSessionId"`
	Source         string    `json:"source"` // transcript | screen
	Text           string    `json:"text"`
	At             time.Time `json:"at"`
	Chars          int       `json:"chars"`
	Paragraphs     int       `json:"-"`
	Truncated      bool      `json:"truncated"`
	MessageID      string    `json:"messageId"`
}

func fromTranscript(sid, aid string, r *transcript.Reply) *Reply {
	return &Reply{SessionID: sid, AgentSessionID: aid, Source: "transcript", Text: r.Text, At: r.At,
		Chars: r.Chars, Paragraphs: r.Paragraphs, Truncated: r.Truncated, MessageID: r.MessageID}
}

func clamp(v, lo, hi uint16) uint16 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// HookToken returns the session's marker token. It is for hub-internal
// use only and never part of any API response.
func (s *Session) HookToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.priv.HookToken
}
