package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/marker"
	"github.com/azamrizki/nextboard/internal/screen"
	"github.com/azamrizki/nextboard/internal/transcript"
)

// Errors of POST /api/sessions/{id}/agent (§3.3). The server maps them to
// {"error","code"}.
var (
	ErrNotAgent     = errors.New("console ini bukan console agen")
	ErrAgentRunning = errors.New("agen masih berjalan di console ini")
	ErrShellBusy    = errors.New("shell sedang menjalankan perintah lain atau baru saja diketik")
	ErrWrapMissing  = errors.New("wrapper agen tidak merespons; restart penuh console ini")
	ErrOffline      = errors.New("machine belum tersambung")
)

const (
	evalEvery    = 500 * time.Millisecond // screen rules ≤ 2×/s …
	evalQuiet    = 150 * time.Millisecond // … or 150 ms after output stops
	fgCheckEvery = 10 * time.Second
	// How often a shell console is asked what it runs. Locally it costs one ioctl. Over
	// SSH it is a command on the server, so a console where nothing happens asks rarely.
	jobCheckLocal  = 2 * time.Second
	jobCheckRemote = 3 * time.Second
	jobCheckQuiet  = 30 * time.Second
	codexFindFor   = 60 * time.Second
	agentWaitFor   = 10 * time.Second
	inputQuietFor  = 2 * time.Second
	replyScreenN   = 200

	// Typing into an idle shell (typeAtPrompt).
	promptCancel     = 0x03 // Ctrl-C
	promptCancelWait = 200 * time.Millisecond
	promptShowWait   = 3 * time.Second
	promptRowsBack   = 8 // the wrapper line wraps over fewer rows, even in a narrow tile
)

// markerLocked applies one marker (already token-checked).
func (s *Session) markerLocked(mk marker.Marker, now time.Time) {
	if s.m.OnMarker != nil {
		s.m.OnMarker(s.spec.ID, mk)
	}
	p := mk.Payload
	switch mk.Type {
	case marker.TypeAgentStart:
		if p.Pid > 0 && s.validPidLocked(p.Pid) {
			s.priv.WrapperPid = p.Pid
		}
		s.priv.LastForeground = agentstate.FgAgent
		if p.Sid != "" && p.Sid != "-" && validSessionID(p.Sid) {
			s.spec.AgentSessionID = p.Sid
		}
		s.resumeOffer = false
		s.subagents = 0
		s.eng.AgentStart(now)
		if s.ag.Transcript == agent.TranscriptCodex && s.spec.AgentSessionID == "" {
			s.codexFind = now
		}
		for _, ch := range s.startWait {
			close(ch)
		}
		s.startWait = nil
		s.m.saveSoon()
	case marker.TypeAgentExit:
		s.subagents = 0
		rc := 0
		if p.Rc != nil {
			rc = *p.Rc
		}
		if !s.m.isShuttingDown() { // the hub's own stop ended it: it comes back after the restart
			s.priv.LastForeground = agentstate.FgShell
		}
		s.codexFind = time.Time{}
		s.eng.AgentExit(rc, now)
		s.m.saveSoon()
	case marker.TypeHook:
		raw := mk.RawBytes()
		h := agentstate.Hook{
			Event:            first(marker.ScanString(raw, "hook_event_name"), p.Ev),
			ToolName:         marker.ScanString(raw, "tool_name"),
			NotificationType: marker.ScanString(raw, "notification_type"),
			Message:          marker.ScanString(raw, "message"),
		}
		for _, k := range []string{"command", "file_path", "path", "pattern", "url", "description", "prompt"} {
			if v := marker.ScanString(raw, k); v != "" {
				h.ToolSummary = v
				break
			}
		}
		if id := marker.ScanString(raw, "session_id"); validSessionID(id) && id != s.spec.AgentSessionID {
			s.spec.AgentSessionID = id
			s.m.saveSoon()
		}
		if tp := marker.ScanString(raw, "transcript_path"); tp != "" && strings.HasPrefix(tp, "/") && tp != s.priv.TranscriptPath {
			s.priv.TranscriptPath = tp
			s.m.saveSoon()
		}
		s.countSubagentsLocked(h)
		s.eng.Hook(h, now)
		if (h.Event == "PreToolUse" || h.Event == "PostToolUse") && fileTools[h.ToolName] && s.m.OnFileTool != nil {
			path := first(marker.ScanString(raw, "file_path"), marker.ScanString(raw, "notebook_path"))
			if path != "" {
				sp, cb := s.spec, s.m.OnFileTool
				go cb(sp, path)
			}
		}
	case marker.TypeNotify:
		raw := mk.RawBytes()
		tid := marker.ScanString(raw, "thread-id")
		if codexSideThread(raw) {
			return // Codex 0.163 also notifies for its internal title thread
		}
		if tid != "" && s.spec.AgentSessionID != "" && tid != s.spec.AgentSessionID && s.priv.TranscriptPath != "" {
			return // another thread than the console's known conversation
		}
		if id := tid; validSessionID(id) && id != s.spec.AgentSessionID {
			s.spec.AgentSessionID = id
			s.priv.TranscriptPath = ""
			s.codexFind = time.Time{}
			s.m.saveSoon()
		}
		if msg := marker.ScanString(raw, "last-assistant-message"); msg != "" {
			s.reply = screenReply(s.spec, msg, now, "transcript")
		}
		s.eng.Notify(marker.ScanString(raw, "type"), now)
	}
}

// fileTools are the tools whose hooks name the file they write.
var fileTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// countSubagentsLocked tracks running subagents (Mission Map §7): a
// PreToolUse of the Task tool (named Agent in newer Claude Code) without
// its PostToolUse yet.
func (s *Session) countSubagentsLocked(h agentstate.Hook) {
	switch {
	case h.Event == "PreToolUse" && (h.ToolName == "Task" || h.ToolName == "Agent"):
		s.subagents++
	case h.Event == "PostToolUse" && (h.ToolName == "Task" || h.ToolName == "Agent"):
		s.subagents = max(0, s.subagents-1)
	case h.Event == "Stop" || h.Event == "SessionStart" || h.Event == "SessionEnd":
		s.subagents = 0
	}
}

// codexSideThread recognises the notify of Codex's title generator:
// last-assistant-message is a JSON object with only a "title".
func codexSideThread(raw []byte) bool {
	msg := strings.TrimSpace(marker.ScanString(raw, "last-assistant-message"))
	if !strings.HasPrefix(msg, "{") {
		return false
	}
	var v map[string]any
	if json.Unmarshal([]byte(msg), &v) != nil {
		return false
	}
	_, ok := v["title"]
	return ok && len(v) == 1
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

func validSessionID(id string) bool { return sessionIDRe.MatchString(id) }

// validPidLocked: a pid from a marker is only trusted when it belongs to
// the session's terminal (local: same session id as the PTY leader).
func (s *Session) validPidLocked(pid int) bool {
	if s.cur == nil {
		return false
	}
	if v, ok := s.cur.proc.(interface{ SameSession(int) bool }); ok {
		return v.SameSession(pid)
	}
	return true // remote: checked by ps before use
}

// external applies a marker that came through POST /nb/hook.
func (s *Session) external(mk marker.Marker) {
	s.mu.Lock()
	if s.deleted || s.cur == nil {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	s.markerLocked(mk, now)
	notify, ev := s.afterLocked(now, false)
	s.mu.Unlock()
	s.finish(notify, ev)
}

// tick runs every 250 ms: held marker bytes, screen rules, engine timers,
// throttled activity, process checks and Codex rollout discovery.
func (s *Session) tick(now time.Time) {
	s.mu.Lock()
	if s.deleted || s.cur == nil {
		s.mu.Unlock()
		return
	}
	if held := s.strip.Flush(now); len(held) > 0 {
		s.emitLocked(held, now)
	}
	if s.spec.Paused {
		// agentState is frozen while the process group is stopped
		// (docs/API-orchestration.md §8): no screen rules, no timers.
		s.mu.Unlock()
		return
	}
	if s.rules != nil {
		if v := s.scr.Version(); v != s.scrVersion && (now.Sub(s.scrEvalAt) >= evalEvery || now.Sub(s.lastOutput) >= evalQuiet) {
			sn := s.scr.Snapshot()
			m := s.rules.Evaluate(sn.Lines, sn.CursorY)
			s.match = agentstate.Screen{NeedsYou: m.NeedsYou, Startup: m.Startup, Working: m.Working, Ready: m.Ready}
			s.scrVersion, s.scrEvalAt = sn.Version, now
		}
		s.eng.Screen(s.match, now)
	} else if s.ag.Kind == agent.KindAgent {
		if v := s.scr.Version(); v != s.scrVersion && (now.Sub(s.scrEvalAt) >= evalEvery || now.Sub(s.lastOutput) >= evalQuiet) {
			sn := s.scr.Snapshot()
			m := agent.GenericEvaluate(s.ag.ScreenRules, sn.Lines, sn.CursorY)
			s.match = agentstate.Screen{NeedsYou: m.NeedsYou, Working: m.Working, Ready: m.Ready}
			s.scrVersion, s.scrEvalAt = sn.Version, now
		}
		s.eng.GenericScreen(s.match, now)
	}
	s.eng.Tick(now)
	force := s.actPending && now.Sub(s.actSentAt) >= activityEvery
	notify, ev := s.afterLocked(now, force)
	checkFg := s.spec.MachineID == machine.LocalID && s.ag.Kind == agent.KindAgent &&
		s.eng.Foreground() != agentstate.FgAgent && now.Sub(s.fgCheckAt) >= fgCheckEvery
	if checkFg {
		s.fgCheckAt = now
	}
	checkJob := s.eng.Snapshot().AgentState == agentstate.Shell && !s.jobBusy && s.jobDueLocked(now)
	if checkJob {
		s.jobBusy = true
	}
	cur := s.cur
	findCodex := !s.codexFind.IsZero() && !s.refreshing && now.Sub(s.codexTry) >= 3*time.Second
	if findCodex && now.Sub(s.codexFind) > codexFindFor {
		s.codexFind, findCodex = time.Time{}, false
	}
	if findCodex {
		s.codexTry = now
	}
	s.mu.Unlock()
	s.finish(notify, ev)
	if checkFg {
		go s.checkForeground()
	}
	if checkJob {
		go s.checkJob(cur)
	}
	if findCodex {
		go s.findCodex()
	}
}

// Foreground runs the process check of §6.5 now: the foreground process
// group of the terminal, whether it is the wrapper (idle shell) and the
// command name of the job otherwise.
func (s *Session) Foreground(ctx context.Context) (idle bool, comm string, err error) {
	s.mu.Lock()
	r, pid := s.cur, s.priv.WrapperPid
	s.mu.Unlock()
	if r == nil {
		return false, "", errors.New("proses tidak berjalan")
	}
	fg, ok := r.proc.(machine.Foregrounder)
	if !ok {
		return false, "", errors.New("cek proses tidak didukung")
	}
	pgid, comm, err := fg.Foreground(ctx, pid)
	if err != nil {
		return false, "", err
	}
	if pid == 0 {
		if lp, ok := r.proc.(interface{ Pid() int }); ok {
			pid = lp.Pid()
		}
	}
	return pgid == pid, comm, nil
}

// checkForeground detects an agent CLI the user typed into the shell
// (manualAgent) and its exit.
func (s *Session) checkForeground() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	idle, comm, err := s.Foreground(ctx)
	if err != nil {
		return
	}
	manual := !idle && s.m.isAgentCommand(comm)
	s.mu.Lock()
	now := time.Now()
	switch {
	case manual:
		s.eng.SetForeground(agentstate.FgAgent, true, now)
	case s.eng.Snapshot().ManualAgent:
		s.eng.SetForeground(agentstate.FgShell, false, now)
	}
	notify, ev := s.afterLocked(now, false)
	s.mu.Unlock()
	s.finish(notify, ev)
}

// jobDueLocked says whether it is time to ask a shell console what it runs.
func (s *Session) jobDueLocked(now time.Time) bool {
	every := jobCheckLocal
	if s.spec.MachineID != machine.LocalID {
		every = jobCheckQuiet
		if s.lastOutput.After(s.jobAt) || s.lastInput.After(s.jobAt) {
			every = jobCheckRemote
		}
	}
	return now.Sub(s.jobAt) >= every
}

// checkJob looks up the command in the foreground of a shell console.
func (s *Session) checkJob(r *run) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, known := s.foregroundJob(ctx, r)
	s.mu.Lock()
	s.jobBusy, s.jobAt = false, time.Now()
	if s.cur != r || (job == s.job && known == s.jobKnown) {
		s.mu.Unlock()
		return
	}
	s.job, s.jobKnown = job, known
	s.fanoutLocked(s.statusMsgLocked())
	s.mu.Unlock()
	s.m.changed(s)
}

// foregroundJob returns the foreground command ("" at the prompt), and false when the
// hub cannot tell.
func (s *Session) foregroundJob(ctx context.Context, r *run) (string, bool) {
	fg, ok := r.proc.(machine.Foregrounder)
	if !ok {
		return "", false
	}
	pid := s.shellPid(ctx, r)
	if pid <= 0 {
		return "", false
	}
	pgid, comm, err := fg.Foreground(ctx, pid)
	if err != nil {
		s.mu.Lock()
		s.panePid = 0 // the pane may be gone; ask tmux again next time
		s.mu.Unlock()
		return "", false
	}
	if pgid == pid {
		return "", true
	}
	return firstNonEmpty(comm, "process"), true
}

// shellPid is the shell at the top of the console: the wrapper an agent start reported, the
// local process, or the tmux pane. 0 for an SSH shell without tmux.
func (s *Session) shellPid(ctx context.Context, r *run) int {
	s.mu.Lock()
	pid, pane, tmux, mid := s.priv.WrapperPid, s.panePid, s.spec.TmuxName, s.spec.MachineID
	s.mu.Unlock()
	if pid > 0 {
		return pid
	}
	if lp, ok := r.proc.(interface{ Pid() int }); ok {
		return lp.Pid()
	}
	if pane > 0 || tmux == "" {
		return pane
	}
	sm, ok := s.m.machines.SSH(mid)
	if !ok {
		return 0
	}
	pane, err := sm.TmuxPanePid(ctx, tmux)
	if err != nil {
		return 0
	}
	s.mu.Lock()
	s.panePid = pane
	s.mu.Unlock()
	return pane
}

// jobLocked is the shell's command; an agent in the foreground has its own state.
func (s *Session) jobLocked() string {
	if s.eng.Snapshot().AgentState != agentstate.Shell {
		return ""
	}
	return s.job
}

// activeLocked says the console is really processing: an agent in a turn, or a shell with a
// command in the foreground. Without a way to ask, printing is the only sign left.
func (s *Session) activeLocked() bool {
	if s.status != StatusRunning || s.spec.Paused {
		return false
	}
	switch s.eng.Snapshot().AgentState {
	case agentstate.Working:
		return true
	case agentstate.Shell:
		if s.jobKnown {
			return s.job != ""
		}
		return s.busy
	}
	return false
}

// homeOf returns the home folder of the session's machine.
func (s *Session) homeOf(h machine.Host) string {
	if p := h.Info().Probe; p != nil && p.Home != "" {
		return p.Home
	}
	if h.ID() == machine.LocalID {
		home, _ := os.UserHomeDir()
		return home
	}
	return ""
}

// transcriptLocation returns kind and path of the current conversation's
// transcript ("" when unknown).
func (s *Session) transcriptLocation(ctx context.Context, h machine.Host) (string, string) {
	s.mu.Lock()
	spec, tp, kind := s.spec, s.priv.TranscriptPath, s.ag.Transcript
	s.mu.Unlock()
	if kind != transcript.KindClaude && kind != transcript.KindCodex {
		return "", ""
	}
	if tp != "" {
		return kind, tp
	}
	if spec.AgentSessionID == "" {
		return kind, ""
	}
	home := s.homeOf(h)
	profileDir := s.m.profileDir(spec)
	var p string
	switch kind {
	case transcript.KindClaude:
		cfg := transcript.ClaudeConfigDir(home, firstNonEmpty(profileDir, localEnv(h, "CLAUDE_CONFIG_DIR")))
		p, _ = transcript.FindClaude(ctx, h, cfg, spec.Cwd, spec.AgentSessionID)
	case transcript.KindCodex:
		ch := transcript.CodexHome(home, firstNonEmpty(profileDir, localEnv(h, "CODEX_HOME")))
		p, _ = transcript.CodexPathByID(ctx, h, ch, spec.AgentSessionID, time.Now())
	}
	if p != "" {
		s.mu.Lock()
		if s.spec.AgentSessionID == spec.AgentSessionID {
			s.priv.TranscriptPath = p
		}
		s.mu.Unlock()
		s.m.saveSoon()
	}
	return kind, p
}

// neverSaved reports that Claude holds no conversation for this console's id,
// as after a launch that got no message: it writes the transcript on the first
// one and ends with "No conversation found" when told to resume before that.
// Codex learns its id from the first turn, so it cannot get here. Anything short
// of a clear answer is false: a fresh start would leave a real conversation behind.
func (s *Session) neverSaved() bool {
	s.mu.Lock()
	spec, tp := s.spec, s.priv.TranscriptPath
	s.mu.Unlock()
	if spec.AgentSessionID == "" || s.m.agentFor(spec).Transcript != transcript.KindClaude {
		return false
	}
	h, ok := s.m.machines.Host(spec.MachineID)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if tp != "" {
		if st, err := h.Stat(ctx, tp); err != nil || st.Exists {
			return false
		}
	}
	cfg := transcript.ClaudeConfigDir(s.homeOf(h), firstNonEmpty(s.m.profileDir(spec), localEnv(h, "CLAUDE_CONFIG_DIR")))
	if !path.IsAbs(cfg) {
		return false
	}
	p, err := transcript.FindClaude(ctx, h, cfg, spec.Cwd, spec.AgentSessionID)
	return err == nil && p == ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// localEnv reads the hub's own env for This PC (agents inherit it).
func localEnv(h machine.Host, k string) string {
	if h.ID() != machine.LocalID {
		return ""
	}
	return os.Getenv(k)
}

// TranscriptLocation is the exported form for the transcript endpoints.
func (s *Session) TranscriptLocation(ctx context.Context) (machine.Host, string, string, error) {
	h, ok := s.m.machines.Host(s.Spec().MachineID)
	if !ok {
		return nil, "", "", machine.ErrNotFound
	}
	kind, p := s.transcriptLocation(ctx, h)
	return h, kind, p, nil
}

// findCodex looks for the rollout of a Codex run started without an id.
func (s *Session) findCodex() {
	s.mu.Lock()
	if s.refreshing || s.codexFind.IsZero() {
		s.mu.Unlock()
		return
	}
	s.refreshing = true
	spec, started := s.spec, s.codexFind
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.refreshing = false
		s.mu.Unlock()
	}()
	h, ok := s.m.machines.Host(spec.MachineID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home := transcript.CodexHome(s.homeOf(h), firstNonEmpty(s.m.profileDir(spec), localEnv(h, "CODEX_HOME")))
	p, id, err := transcript.FindCodex(ctx, h, home, spec.Cwd, started, func(path string) bool { return s.m.claimed(s, path) })
	if err != nil || p == "" {
		return
	}
	s.mu.Lock()
	if s.codexFind.Equal(started) && s.spec.AgentSessionID == "" {
		s.priv.TranscriptPath, s.spec.AgentSessionID, s.codexFind = p, id, time.Time{}
	}
	s.mu.Unlock()
	s.m.saveSoon()
	s.m.changed(s)
}

// refreshReply re-reads the last reply (after Stop / notify / ready) and
// publishes reply.chars with the session event.
func (s *Session) refreshReply() {
	go func() {
		h, ok := s.m.machines.Host(s.Spec().MachineID)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		time.Sleep(300 * time.Millisecond) // let the CLI flush the transcript
		s.syncModel()
		kind, p := s.transcriptLocation(ctx, h)
		var rep *Reply
		if p != "" {
			if r, err := transcript.ReadReply(ctx, h, kind, p); err == nil {
				sp := s.Spec()
				rep = fromTranscript(sp.ID, sp.AgentSessionID, r)
			}
		}
		s.mu.Lock()
		if rep == nil && kind == "" {
			rep = s.screenReplyLocked()
		}
		if rep == nil {
			s.mu.Unlock()
			return
		}
		s.reply = rep
		s.mu.Unlock()
		s.m.changed(s)
	}()
}

// syncModel takes over what the user chose inside the CLI, with its own /model
// and /effort: those change that CLI's default for every console of the login, so
// only what this console remembers brings it back after a restart. A console that
// chose nothing keeps the model it has been answering with.
func (s *Session) syncModel() {
	spec := s.Spec()
	if spec.AgentKind != agent.KindAgent || !s.m.agentFor(spec).HasModels() {
		return
	}
	h, ok := s.m.machines.Host(spec.MachineID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kind, p := s.transcriptLocation(ctx, h)
	if p == "" {
		return
	}
	s.mu.Lock()
	since := s.priv.ModelAt
	s.mu.Unlock()
	seen, err := transcript.ReadModel(ctx, h, kind, p, since)
	if err != nil {
		return
	}
	s.mu.Lock()
	before := s.spec
	switch {
	case s.spec.AgentSessionID != spec.AgentSessionID || s.spec.Agent != spec.Agent:
	case seen.HasModel && agent.ValidModel(seen.Model):
		s.spec.Model = seen.Model
	case (s.spec.Model == "" || seen.Follow) && seen.Replied != "" && agent.ValidModel(seen.Replied):
		s.spec.Model = seen.Replied
	}
	if seen.HasEffort && agent.ValidEffort(seen.Effort) && s.spec.AgentSessionID == spec.AgentSessionID && s.spec.Agent == spec.Agent {
		s.spec.Effort = seen.Effort
	}
	changed := before.Model != s.spec.Model || before.Effort != s.spec.Effort
	s.mu.Unlock()
	if changed {
		s.m.saveSoon()
		s.m.changed(s)
	}
}

// screenReplyLocked is the reply of an agent without transcript: the last
// lines printed since the turn started, ANSI removed.
func (s *Session) screenReplyLocked() *Reply {
	b := s.ring.Bytes()
	since := s.outTotal - s.turnOut
	if since > 0 && since < int64(len(b)) {
		b = b[int64(len(b))-since:]
	}
	text := stripANSI(b)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > replyScreenN {
		lines = lines[len(lines)-replyScreenN:]
	}
	text = strings.TrimSpace(strings.Join(lines, "\n"))
	if text == "" {
		return nil
	}
	return screenReply(s.spec, text, time.Now(), "screen")
}

func screenReply(sp Spec, text string, at time.Time, source string) *Reply {
	r := &Reply{SessionID: sp.ID, AgentSessionID: sp.AgentSessionID, Source: source, At: at}
	r.Chars = utf8.RuneCountInString(text)
	r.Paragraphs = paragraphs(text)
	if len(text) > transcript.MaxReplyBytes {
		cut := transcript.MaxReplyBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text, r.Truncated = text[:cut], true
	}
	r.Text = text
	return r
}

func paragraphs(t string) int {
	n := 0
	for _, p := range strings.Split(strings.ReplaceAll(t, "\r\n", "\n"), "\n\n") {
		if strings.TrimSpace(p) != "" {
			n++
		}
	}
	return n
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?<>=!]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[PX^_][^\x1b]*\x1b\\|\x1b[()][0-9A-Za-z]|\x1b[=>78DEHMNOZc]`)

// stripANSI removes escape sequences and carriage-return overwrites.
func stripANSI(b []byte) string {
	b = ansiRe.ReplaceAll(b, nil)
	b = bytes.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f {
			return r
		}
		if r == '\r' {
			return '\r'
		}
		return -1
	}, b)
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if i := strings.LastIndex(strings.TrimRight(l, "\r"), "\r"); i >= 0 {
			l = l[i+1:]
		}
		out = append(out, strings.TrimRight(l, "\r "))
	}
	return strings.Join(out, "\n")
}

// StartAgent is POST /api/sessions/{id}/agent: type the wrapper line into
// an idle shell and wait for agent-start.
func (s *Session) StartAgent(ctx context.Context, mode, agentSessionID string) error {
	s.syncModel()
	s.mu.Lock()
	ag, fg, cur, lastIn := s.ag, s.eng.Foreground(), s.cur, s.lastInput
	spec := s.spec
	s.mu.Unlock()
	if ag.Kind != agent.KindAgent {
		return ErrNotAgent
	}
	if cur == nil {
		return ErrOffline
	}
	if h, ok := s.m.machines.Host(spec.MachineID); !ok || !h.Connected() {
		return ErrOffline
	}
	if fg == agentstate.FgAgent {
		return ErrAgentRunning
	}
	if time.Since(lastIn) < inputQuietFor {
		return ErrShellBusy
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	idle, comm, err := s.Foreground(cctx)
	cancel()
	if err != nil {
		return ErrShellBusy
	}
	if !idle {
		if s.m.isAgentCommand(comm) {
			return ErrAgentRunning
		}
		return ErrShellBusy
	}
	if mode == agent.ModeResume && agentSessionID == "" {
		agentSessionID = spec.AgentSessionID
	}
	m, id := agent.ResolveMode(ag, mode, agentSessionID)
	wait := make(chan struct{})
	s.mu.Lock()
	s.startWait = append(s.startWait, wait)
	if id != "" || m == agent.ModeFresh {
		s.spec.AgentSessionID = id
		s.priv.TranscriptPath = ""
	}
	s.resumeOffer = false
	s.mu.Unlock()
	line := " " + agent.WrapperLine(m, id, spec.Yolo && ag.HasYolo(), spec.Model, spec.Effort)
	shown, err := s.typeAtPrompt(ctx, cur, line)
	if err != nil {
		return err
	}
	if shown {
		select {
		case <-wait:
			s.m.saveSoon()
			s.m.changed(s)
			return nil
		case <-time.After(agentWaitFor):
		case <-ctx.Done():
		}
	}
	s.mu.Lock()
	for i, ch := range s.startWait {
		if ch == wait {
			s.startWait = append(s.startWait[:i], s.startWait[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	return ErrWrapMissing
}

// typeAtPrompt submits line at an idle shell's prompt, whatever the shell, its
// editing mode and what was left on its command line. Return runs what is on the
// line, and what was left there was never submitted:
//
//   - Ctrl-U only clears up to the cursor, so what is after it would join the line:
//     a redirection there empties the file it names. In vi command mode it does
//     nothing at all, and the typed keys edit the line as commands.
//   - Ctrl-C cancels the line in every shell and mode here, and only the shell gets
//     it: the caller checked that the shell itself is in the foreground.
//   - Return is pressed only when the screen shows this line up to the cursor.
//     Otherwise the line is cancelled again, nothing ran, and the result is false.
func (s *Session) typeAtPrompt(ctx context.Context, cur *run, line string) (bool, error) {
	if _, err := cur.proc.Write([]byte{promptCancel}); err != nil {
		return false, err
	}
	select {
	case <-time.After(promptCancelWait): // the input is flushed when the signal comes
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if _, err := cur.proc.Write([]byte(line)); err != nil {
		return false, err
	}
	deadline := time.Now().Add(promptShowWait)
	for !endsAtCursor(s.scr.Snapshot(), line) {
		if time.Now().After(deadline) || ctx.Err() != nil {
			_, _ = cur.proc.Write([]byte{promptCancel})
			return false, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := cur.proc.Write([]byte("\r")); err != nil {
		return false, err
	}
	return true, nil
}

// endsAtCursor reports whether the text up to the cursor ends with line. What is
// after the cursor is not looked at: fish draws suggestions there. A long line may
// wrap, and rows are padded, so whitespace does not count.
func endsAtCursor(sn screen.Snapshot, line string) bool {
	if sn.CursorY < 0 || sn.CursorY >= len(sn.Lines) {
		return false
	}
	var b strings.Builder
	for _, row := range sn.Lines[max(0, sn.CursorY-promptRowsBack):sn.CursorY] {
		b.WriteString(row)
	}
	cur := []rune(sn.Lines[sn.CursorY])
	b.WriteString(string(cur[:min(sn.CursorX, len(cur))]))
	return strings.HasSuffix(withoutSpace(b.String()), withoutSpace(line))
}

func withoutSpace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// baseName of a command path (for manual agent detection).
func baseName(c string) string { return path.Base(strings.TrimSpace(c)) }
