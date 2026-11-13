package session

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/screen"
)

// Sleeping agents (ROADMAP #50, docs/API-agents.md §28). An agent CLI takes 120–250 MB while it
// waits at its prompt. The hub ends the process of an agent that is idle, unwatched and holds no
// work, and starts it again with its conversation (resume) when someone opens its board, types
// into it, or sends it work. After a hub restart, agents stay asleep until then too. What was
// typed at the prompt and not sent is kept and typed back when it wakes.

const (
	sleepCheckEvery = 30 * time.Second
	// wakeReadyWait is how long a woken agent may take to reach its prompt before what was
	// typed is given up on (it stays available as an unsent draft).
	wakeReadyWait = 90 * time.Second
	// wakeSettle lets the prompt take keys after it first shows.
	wakeSettle    = 300 * time.Millisecond
	draftShowWait = 3 * time.Second
	// promptWait: the screen must show the agent's input prompt, not a dialog (an update
	// offer, a trust question), before anything is typed into it.
	promptWait = 5 * time.Second
	maxDraft   = 64 << 10
)

// Reasons a console cannot go to sleep now (409 cannot_sleep, "reason").
const (
	SleepNotAgent       = "not_agent"       // a shell, or an agent started by hand at a shell
	SleepRemote         = "remote"          // its process runs on a server
	SleepNotRunning     = "not_running"     // no process (asleep already, exited, starting)
	SleepKeepAwake      = "keep_awake"      // the owner pinned it awake
	SleepPaused         = "paused"          // stopped by Pause
	SleepBusy           = "busy"            // working, waiting for you, or starting
	SleepWatched        = "watched"         // someone has its terminal open
	SleepRecent         = "recent"          // active within the idle time
	SleepQueued         = "queued"          // a delivery is on its way to it
	SleepHeld           = "held"            // an order, handoff or loop is on it
	SleepNoConversation = "no_conversation" // its conversation id is not known: resume could pick another
	SleepDraftUnsure    = "draft_unsure"    // what is at its prompt cannot be put back exactly
	SleepCommandRunning = "command_running" // a command it started is still running
)

// SleepError says why a console stays awake.
type SleepError struct{ Reason string }

func (e *SleepError) Error() string { return "the console cannot sleep now: " + sleepWords[e.Reason] }

var sleepWords = map[string]string{
	SleepNotAgent:       "it is not an agent the hub started",
	SleepRemote:         "its process runs on a server",
	SleepNotRunning:     "its process is not running",
	SleepKeepAwake:      "it is set to keep awake",
	SleepPaused:         "it is paused",
	SleepBusy:           "it is working, waiting for you or starting",
	SleepWatched:        "its terminal is open",
	SleepRecent:         "it was active recently",
	SleepQueued:         "text is on its way to it",
	SleepHeld:           "an order, handoff or loop is on it",
	SleepNoConversation: "its conversation id is not known",
	SleepDraftUnsure:    "what is typed at its prompt cannot be put back exactly",
	SleepCommandRunning: "a command it started is still running",
}

// ErrNotAsleep: wake was asked of a console that is not asleep.
var ErrNotAsleep = errors.New("the console is not asleep")

const (
	asleepNote   = "\r\n\x1b[90m── asleep to free memory · wakes when you open it or type ──\x1b[0m\r\n"
	restartNote  = "\r\n\x1b[90m── asleep since the hub restarted · wakes when you open it or type ──\x1b[0m\r\n"
	pasteStart   = "\x1b[200~"
	pasteEnd     = "\x1b[201~"
	pastedMarker = "[Pastedtext" // Claude folds a long paste into "[Pasted text #1 +40 lines]"
)

// ---- what is typed at the prompt ----

// draftTrack follows what is typed at an agent's prompt since it last submitted, so a console
// put to sleep can get it back. Keys it cannot follow exactly (cursor keys, history, completion,
// editing shortcuts, a lone Escape) make it unsure until the next submit; an unsure console
// stays awake. Keys typed while the agent shows a dialog are not at the prompt: the caller
// leaves them out.
type draftTrack struct {
	text   []rune
	unsure bool
	paste  bool
	esc    []byte // an escape sequence split across writes
}

func (d *draftTrack) reset() {
	d.text, d.unsure, d.paste, d.esc = nil, false, false, nil
}

// get returns the draft; ok is false when it is not known exactly.
func (d *draftTrack) get() (string, bool) {
	if d.unsure || d.paste || len(d.esc) > 0 {
		return "", false
	}
	return string(d.text), true
}

func (d *draftTrack) add(r rune) {
	if len(d.text) >= maxDraft {
		d.unsure = true
		return
	}
	d.text = append(d.text, r)
}

func (d *draftTrack) feed(p []byte) {
	if len(d.esc) > 0 {
		p = append(d.esc, p...)
		d.esc = nil
	}
	for i := 0; i < len(p); {
		c := p[i]
		if c == 0x1b {
			n := escLen(p[i:])
			switch {
			case n < 0: // cut off: the rest comes with the next write
				d.esc = append([]byte(nil), p[i:]...)
				return
			case n == 0: // a lone Escape: clears or interrupts, depending on the CLI
				d.unsure = true
				i++
				continue
			}
			switch seq := string(p[i : i+n]); seq {
			case pasteStart:
				d.paste = true
			case pasteEnd:
				d.paste = false
			default:
				d.unsure = true
			}
			i += n
			continue
		}
		r, size := utf8.DecodeRune(p[i:])
		i += size
		if d.paste {
			switch r {
			case '\r':
				d.add('\n')
			case '\n':
				if len(d.text) == 0 || d.text[len(d.text)-1] != '\n' || i < 2 || p[i-2] != '\r' {
					d.add('\n')
				}
			default:
				d.add(r)
			}
			continue
		}
		switch {
		case r == '\r':
			d.reset() // submitted (or queued while it works)
		case r == 0x7f || r == 0x08:
			if len(d.text) > 0 {
				d.text = d.text[:len(d.text)-1]
			}
		case r < 0x20:
			d.unsure = true
		case r == utf8.RuneError && size == 1:
			d.unsure = true
		default:
			d.add(r)
		}
	}
}

// escLen is the length of the escape sequence at the start of p: 0 for a lone Escape, -1 when
// p ends before the sequence does.
func escLen(p []byte) int {
	if len(p) < 2 {
		return 0
	}
	switch p[1] {
	case '[':
		for j := 2; j < len(p); j++ {
			if p[j] >= 0x40 && p[j] <= 0x7e {
				return j + 1
			}
		}
		return -1
	case 'O':
		if len(p) < 3 {
			return -1
		}
		return 3
	}
	return 2 // Alt+key
}

// draftShows reports whether the screen shows the end of draft, or a folded paste.
func draftShows(sn screen.Snapshot, draft string) bool {
	var b strings.Builder
	for _, l := range sn.Lines {
		b.WriteString(strings.Map(func(r rune) rune {
			if strings.ContainsRune("│┃║╎╏▌▐", r) {
				return -1
			}
			return r
		}, l))
	}
	all := withoutSpace(b.String())
	if strings.Contains(all, pastedMarker) {
		return true
	}
	last := strings.TrimRight(draft, "\n")
	if i := strings.LastIndexByte(last, '\n'); i >= 0 {
		last = last[i+1:]
	}
	tail := []rune(withoutSpace(last))
	if len(tail) > 24 {
		tail = tail[len(tail)-24:]
	}
	return len(tail) > 0 && strings.Contains(all, string(tail))
}

// ---- sleeping ----

// sleepBlockLocked is why s cannot sleep now ("" = it can, as far as the session itself
// knows). force (Sleep now) ignores who is watching and how recently it was active.
func (s *Session) sleepBlockLocked(now time.Time, idle time.Duration, force bool) string {
	sn := s.eng.Snapshot()
	switch {
	case s.ag.Kind != agent.KindAgent || sn.ManualAgent:
		return SleepNotAgent
	case s.spec.MachineID != machine.LocalID:
		return SleepRemote
	case s.cur == nil || s.status != StatusRunning || s.sleeping || s.waking:
		return SleepNotRunning
	case s.spec.KeepAwake:
		return SleepKeepAwake
	case s.spec.Paused:
		return SleepPaused
	case sn.Foreground != agentstate.FgAgent:
		return SleepNotAgent
	case sn.AgentState != agentstate.Ready || (s.busy && !force):
		// busy is output in the last 2 s: a redraw when a terminal opens counts too, so only
		// the idle sleep waits for it to pass
		return SleepBusy
	case !validSessionID(s.spec.AgentSessionID):
		return SleepNoConversation
	}
	if !force {
		if len(s.subs) > 0 {
			return SleepWatched
		}
		last := s.startedAt
		for _, t := range []time.Time{s.lastOutput, s.lastInput, sn.StateSince} {
			if t.After(last) {
				last = t
			}
		}
		if now.Sub(last) < idle {
			return SleepRecent
		}
	}
	if _, ok := s.draft.get(); !ok {
		return SleepDraftUnsure
	}
	return ""
}

// sleep ends the agent's process, keeping its conversation and what is typed at its prompt.
// procs is the process table of This PC (nil: it is read here).
func (s *Session) sleep(idle time.Duration, force bool, procs []procInfo) error {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	s.mu.Lock()
	why := s.sleepBlockLocked(time.Now(), idle, force)
	draft, _ := s.draft.get()
	r, pid, id := s.cur, s.priv.WrapperPid, s.spec.ID
	s.mu.Unlock()
	if why != "" {
		return &SleepError{why}
	}
	if s.m.QueueLen != nil && s.m.QueueLen(id) > 0 {
		return &SleepError{SleepQueued}
	}
	if s.m.StayAwake != nil && s.m.StayAwake(id) {
		return &SleepError{SleepHeld}
	}
	if procs == nil {
		var err error
		if procs, err = readProcs(); err != nil {
			return &SleepError{SleepCommandRunning} // cannot tell: stay awake
		}
	}
	if pid <= 0 || commandRunning(procs, pid) {
		return &SleepError{SleepCommandRunning}
	}
	if draft != "" {
		s.scr.Sync()
		if !draftShows(s.scr.Snapshot(), draft) {
			return &SleepError{SleepDraftUnsure}
		}
	}
	s.mu.Lock()
	if s.cur != r || s.eng.Snapshot().AgentState != agentstate.Ready {
		s.mu.Unlock()
		return &SleepError{SleepBusy}
	}
	if d, ok := s.draft.get(); !ok || d != draft {
		s.mu.Unlock()
		return &SleepError{SleepDraftUnsure}
	}
	s.sleeping = true
	s.priv.Draft, s.priv.DraftFailed = draft, false
	s.mu.Unlock()
	s.stop() // exited() sees sleeping
	return nil
}

// asleepLocked marks s asleep; the caller publishes the status.
func (s *Session) asleepLocked(note string) {
	now := time.Now().UTC()
	s.spec.Asleep, s.spec.AsleepAt = true, &now
	s.writeLocked(note)
}

// Wake starts an asleep agent again with its conversation; what was typed at its prompt
// before it slept is typed back once it is ready, then what was typed while it woke. It
// returns false when s was not asleep.
func (s *Session) Wake() bool {
	s.mu.Lock()
	if !s.spec.Asleep || s.waking || s.sleeping || s.deleted {
		s.mu.Unlock()
		return false
	}
	s.waking = true
	s.spec.Asleep, s.spec.AsleepAt = false, nil
	draft := ""
	if !s.priv.DraftFailed {
		draft = s.priv.Draft
	}
	s.mu.Unlock()
	s.setStatus(StatusStarting, 0, "")
	s.m.saveSoon()
	go func() {
		s.launchMu.Lock()
		mode := agent.ModeResume
		if s.neverSaved() {
			mode = agent.ModeFresh
		}
		s.launch(mode)
		s.launchMu.Unlock()
		s.restore(draft)
	}()
	return true
}

// restore types the draft back once the woken agent is at its input prompt, then what was
// typed while it woke. Nothing is typed into anything else (an update offer, a trust question):
// the draft and those keys then stay available to copy (unsentDraft), as does a draft that does
// not show again.
func (s *Session) restore(draft string) {
	ready := s.waitReady(wakeReadyWait) && s.waitPrompt(promptWait)
	s.inMu.Lock()
	defer s.inMu.Unlock()
	s.mu.Lock()
	r := s.cur
	typed := s.wakeIn
	s.wakeIn, s.waking = nil, false
	s.mu.Unlock()
	if !ready || r == nil {
		if draft != "" || len(typed) > 0 {
			// what was typed during the wake, after the draft, as far as it can be followed
			t := draftTrack{text: []rune(draft)}
			t.feed(typed)
			kept, ok := t.get()
			if !ok {
				kept = draft
			}
			s.mu.Lock()
			s.priv.Draft, s.priv.DraftFailed = kept, kept != ""
			s.mu.Unlock()
			s.m.saveSoon()
			s.m.changed(s)
		}
		return
	}
	if draft != "" {
		ok := s.typeDraft(r, draft)
		s.mu.Lock()
		if ok {
			s.priv.Draft, s.priv.DraftFailed = "", false
		} else {
			s.priv.DraftFailed = true
		}
		s.mu.Unlock()
		s.m.saveSoon()
		s.m.changed(s)
	}
	if len(typed) > 0 {
		_ = s.inputLocked(typed)
	}
}

// waitPrompt waits until the screen shows the agent's input prompt (the delivery engine's check).
func (s *Session) waitPrompt(limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		s.scr.Sync()
		sn := s.scr.Snapshot()
		s.mu.Lock()
		rules := s.ag.ScreenRules
		s.mu.Unlock()
		if ok, _ := agent.InputPrompt(rules, sn.Lines, sn.CursorY); ok && !sn.Stale {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitReady waits for the agent's prompt after a wake.
func (s *Session) waitReady(limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		st, deleted, cur := s.eng.Snapshot().AgentState, s.deleted, s.cur
		status := s.status
		s.mu.Unlock()
		switch {
		case deleted:
			return false
		case st == agentstate.Ready && cur != nil:
			time.Sleep(wakeSettle)
			return true
		case status == StatusExited || status == StatusError || st == agentstate.Exited || st == agentstate.Error || st == agentstate.Shell:
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// typeDraft pastes draft at the prompt without submitting it and waits until it shows.
// Caller holds inMu.
func (s *Session) typeDraft(r *run, draft string) bool {
	sn := s.scr.Snapshot()
	var b []byte
	switch {
	case sn.BracketedPaste:
		// as a terminal pastes: line ends as Return, inside paste brackets
		b = []byte(pasteStart + strings.ReplaceAll(draft, "\n", "\r") + pasteEnd)
	case !strings.ContainsAny(draft, "\r\n"):
		b = []byte(draft)
	default:
		return false // a Return would send it
	}
	s.mu.Lock()
	s.draft.feed(b)
	s.mu.Unlock()
	if _, err := r.proc.Write(b); err != nil {
		return false
	}
	deadline := time.Now().Add(draftShowWait)
	for {
		s.scr.Sync()
		if draftShows(s.scr.Snapshot(), draft) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ---- commands an agent started ----

type procInfo struct {
	pid, ppid int
	comm      string
}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true, "ksh": true}

// readProcs is the process table of This PC.
func readProcs() ([]procInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,comm=").Output()
	if err != nil {
		return nil, err
	}
	var l []procInfo
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := strings.Fields(string(line))
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		l = append(l, procInfo{pid, ppid, strings.Join(f[2:], " ")})
	}
	return l, nil
}

// commandRunning reports whether the agent under wrapper pid runs a command: a shell below
// the agent itself (Bash tools, background commands, dev servers started from them).
func commandRunning(procs []procInfo, wrapper int) bool {
	kids := map[int][]procInfo{}
	for _, p := range procs {
		kids[p.ppid] = append(kids[p.ppid], p)
	}
	var walk func(pid, depth int) bool
	walk = func(pid, depth int) bool {
		for _, k := range kids[pid] {
			// depth 1 is the agent itself, which the wrapper shell runs
			if depth >= 2 && shells[baseName(k.comm)] {
				return true
			}
			if walk(k.pid, depth+1) {
				return true
			}
		}
		return false
	}
	return walk(wrapper, 1)
}

// ---- the manager's side ----

// startAsleep leaves a restored agent asleep instead of starting it (lazy start after a hub
// restart): it starts when someone opens it, types into it or sends it work.
func (m *Manager) startAsleep(s *Session) bool {
	s.mu.Lock()
	sp, a := s.spec, s.ag
	s.mu.Unlock()
	if sp.MachineID != machine.LocalID || a.Kind != agent.KindAgent {
		return false
	}
	if !sp.Asleep {
		mode, offer := m.restartMode(s)
		if !m.Settings().Agents.LazyStart || sp.KeepAwake || mode != agent.ModeResume || offer || !validSessionID(sp.AgentSessionID) {
			return false
		}
	}
	s.mu.Lock()
	note := restartNote
	if sp.Asleep {
		note = asleepNote
	}
	s.asleepLocked(note)
	s.mu.Unlock()
	s.setStatus(StatusAsleep, 0, "")
	return true
}

// sleepIdle puts to sleep the agents idle for the configured time (settings.agents.sleepAfterMin).
func (m *Manager) sleepIdle(now time.Time) {
	if mins := m.Settings().Agents.SleepAfterMin; mins > 0 {
		m.SleepIdle(time.Duration(mins)*time.Minute, now)
	}
}

// SleepIdle puts to sleep every agent that may sleep and was idle for idle; it returns how
// many went. The hub runs it every 30 s with the configured time; tests call it directly.
func (m *Manager) SleepIdle(idle time.Duration, now time.Time) int {
	if m.isShuttingDown() {
		return 0
	}
	var cands []*Session
	for _, s := range m.all() {
		s.mu.Lock()
		why := s.sleepBlockLocked(now, idle, false)
		s.mu.Unlock()
		if why == "" {
			cands = append(cands, s)
		}
	}
	if len(cands) == 0 {
		return 0
	}
	procs, err := readProcs()
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range cands {
		if s.sleep(idle, false, procs) == nil {
			n++
		}
	}
	return n
}

// Sleep puts a console to sleep now (POST /api/sessions/{id}/sleep): its terminal may be open
// and it may have been active a moment ago; everything else must allow it.
func (m *Manager) Sleep(id string) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	err := s.sleep(0, true, nil)
	return m.SessionInfo(s), err
}

// Wake starts an asleep console (POST /api/sessions/{id}/wake).
func (m *Manager) Wake(id string) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	if !s.Wake() {
		s.mu.Lock()
		waking := s.waking
		s.mu.Unlock()
		if !waking {
			return m.SessionInfo(s), ErrNotAsleep
		}
	}
	return m.SessionInfo(s), nil
}

// SetKeepAwake pins a console awake, or lets it sleep again.
func (m *Manager) SetKeepAwake(id string, on bool) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	s.mu.Lock()
	s.spec.KeepAwake = on
	s.mu.Unlock()
	m.saveNow()
	m.changed(s)
	return m.SessionInfo(s), nil
}

// DismissDraft forgets a draft that could not be typed back (DELETE /api/sessions/{id}/draft).
func (m *Manager) DismissDraft(id string) (Info, error) {
	s, ok := m.Get(id)
	if !ok {
		return Info{}, ErrNotFound
	}
	s.mu.Lock()
	s.priv.Draft, s.priv.DraftFailed = "", false
	s.mu.Unlock()
	m.saveNow()
	m.changed(s)
	return m.SessionInfo(s), nil
}

// Awake counts the agent consoles of This PC that run and those asleep.
func (m *Manager) Awake() (awake, asleep int) {
	for _, s := range m.all() {
		s.mu.Lock()
		if s.ag.Kind == agent.KindAgent && s.spec.MachineID == machine.LocalID {
			switch {
			case s.spec.Asleep:
				asleep++
			case s.cur != nil:
				awake++
			}
		}
		s.mu.Unlock()
	}
	return
}
