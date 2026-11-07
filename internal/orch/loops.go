package orch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/store"
)

// Loops (docs/API-orchestration.md §11) run in the hub around the clock,
// also without clients. The scheduler is driven by Tick(now) and events
// (turn finished, output) so tests use a fake clock.

const (
	loopsFile        = "loops.json"
	maxLoops         = 50
	loopHistoryKeep  = 200
	loopRunDeadline  = 60 // deliveries of a run do not queue long
	loopRetryAfter   = 60 * time.Second
	loopSettleMax    = 75 * time.Second
	loopEventEvery   = 120 * time.Second // agentIdle / output: ≤ 1 per console
	goalEvery        = 90 * time.Second
	goalConcurrent   = 2
	loopReplyForGoal = 8000
)

var (
	ErrBadLoop      = errors.New("invalid loop")
	ErrTooManyLoops = errors.New("too many loops (at most 50 per hub)")
	ErrLoopNotFound = errors.New("loop not found")
)

func badLoop(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrBadLoop, fmt.Sprintf(f, a...)) }

// LoopTrigger: interval | cron | agentIdle | output.
type LoopTrigger struct {
	Kind     string `json:"kind"`
	EverySec int    `json:"everySec,omitempty"`
	Expr     string `json:"expr,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Regex    bool   `json:"regex,omitempty"`
}

// LoopTargets: handles (session ids) | group | orchestrator (node ids) | allAgents.
type LoopTargets struct {
	Kind string   `json:"kind"`
	IDs  []string `json:"ids"`
}

// LoopGoal: agentJudged (runner) | command (exit 0 = met).
type LoopGoal struct {
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Command    string `json:"command,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"`
}

// LoopStop lists the stop conditions.
type LoopStop struct {
	MaxRuns        int        `json:"maxRuns,omitempty"`
	MaxDurationSec int        `json:"maxDurationSec,omitempty"`
	Until          *time.Time `json:"until,omitempty"`
	OutputPattern  string     `json:"outputPattern,omitempty"`
	Goal           *LoopGoal  `json:"goal,omitempty"`
}

// LoopState is the live state (persisted).
type LoopState struct {
	Status        string     `json:"status"` // running | paused | stopped | done
	Runs          int        `json:"runs"`
	StartedAt     *time.Time `json:"startedAt"`
	LastRunAt     *time.Time `json:"lastRunAt"`
	NextRunAt     *time.Time `json:"nextRunAt"`
	StoppedReason string     `json:"stoppedReason,omitempty"`
}

// LoopDef is a loop.
type LoopDef struct {
	ID          string      `json:"id"`
	ProjectID   string      `json:"projectId"`
	WorkspaceID string      `json:"workspaceId"`
	Name        string      `json:"name"`
	Enabled     bool        `json:"enabled"`
	Trigger     LoopTrigger `json:"trigger"`
	Text        string      `json:"text"`
	Targets     LoopTargets `json:"targets"`
	Stop        LoopStop    `json:"stop"`
	CreatedBy   string      `json:"createdBy"` // user | order
	CreatedAt   time.Time   `json:"createdAt"`
	State       LoopState   `json:"state"`
}

// LoopTargetResult is one console of a run.
type LoopTargetResult struct {
	SessionID string `json:"sessionId"`
	Result    string `json:"result"` // delivered | busy | needsYou | paused | notAgent
}

// GoalResult is a goal check outcome.
type GoalResult struct {
	Met    bool   `json:"met"`
	Reason string `json:"reason"`
}

// LoopHistoryEntry is one line of loops/<id>.jsonl.
type LoopHistoryEntry struct {
	At      time.Time          `json:"at"`
	Kind    string             `json:"kind"` // run | skip | retry | goalCheck | stop
	Note    string             `json:"note"`
	Targets []LoopTargetResult `json:"targets,omitempty"`
	Goal    *GoalResult        `json:"goal,omitempty"`
}

// ValidateLoop checks a definition. byPlanner forbids goal commands.
func ValidateLoop(l *LoopDef, byPlanner bool) error {
	l.Name = strings.TrimSpace(l.Name)
	if l.Name == "" {
		l.Name = "Loop"
	}
	if len([]rune(l.Name)) > 60 {
		return badLoop("name at most 60 characters")
	}
	if t := strings.TrimSpace(l.Text); t == "" || len([]rune(t)) > 4000 {
		return badLoop("text 1–4000 characters")
	}
	switch l.Trigger.Kind {
	case "interval":
		if l.Trigger.EverySec < 60 || l.Trigger.EverySec > 86400 {
			return badLoop("interval must be 60 s–24 h")
		}
	case "cron":
		if _, err := ParseCron(l.Trigger.Expr); err != nil {
			return badLoop("invalid cron: %v", err)
		}
	case "agentIdle":
	case "output":
		if len([]rune(l.Trigger.Pattern)) < 2 {
			return badLoop("output pattern at least 2 characters")
		}
		if l.Trigger.Regex {
			if _, err := regexp.Compile(l.Trigger.Pattern); err != nil {
				return badLoop("invalid regex")
			}
		}
	default:
		return badLoop("unknown trigger.kind")
	}
	switch l.Targets.Kind {
	case "handles", "group", "orchestrator":
		if len(l.Targets.IDs) == 0 || len(l.Targets.IDs) > 64 {
			return badLoop("targets.ids is required (at most 64)")
		}
		for _, id := range l.Targets.IDs {
			if !idRe.MatchString(id) {
				return badLoop("invalid target %q", id)
			}
		}
	case "allAgents":
		l.Targets.IDs = []string{}
	default:
		return badLoop("unknown targets.kind")
	}
	st := l.Stop
	if st.MaxRuns != 0 && (st.MaxRuns < 1 || st.MaxRuns > 500) {
		return badLoop("maxRuns must be 1–500")
	}
	if st.MaxDurationSec != 0 && (st.MaxDurationSec < 900 || st.MaxDurationSec > 86400) {
		return badLoop("maxDurationSec must be 900–86400")
	}
	if st.OutputPattern != "" && len([]rune(st.OutputPattern)) < 2 {
		return badLoop("stop.outputPattern at least 2 characters")
	}
	if g := st.Goal; g != nil {
		switch g.Kind {
		case "agentJudged":
			if strings.TrimSpace(g.Text) == "" || len(g.Text) > 2000 {
				return badLoop("goal.text is required (at most 2000)")
			}
			if g.Agent == "" {
				g.Agent = "claude"
			}
			if g.Agent != "claude" && g.Agent != "codex" {
				return badLoop("goal.agent must be claude or codex")
			}
		case "command":
			if byPlanner || l.CreatedBy == "order" {
				return badLoop("a goal command can only be made by the user explicitly")
			}
			if strings.TrimSpace(g.Command) == "" || len(g.Command) > 1000 || strings.ContainsAny(g.Command, "\x00\n\r") {
				return badLoop("goal.command 1–1000 characters, one line")
			}
			if g.TimeoutSec == 0 {
				g.TimeoutSec = 300
			}
			if g.TimeoutSec < 5 || g.TimeoutSec > 3600 {
				return badLoop("goal.timeoutSec 5–3600")
			}
		default:
			return badLoop("unknown goal.kind")
		}
	}
	if l.CreatedBy != "order" {
		l.CreatedBy = "user"
	}
	return nil
}

// patternMatcher compiles a trigger/stop pattern: plain text with smart
// case (case-insensitive unless it contains upper case), or RE2.
func patternMatcher(p string, isRegex bool) func(string) bool {
	if p == "" {
		return func(string) bool { return false }
	}
	if isRegex {
		r, err := regexp.Compile(p)
		if err != nil {
			return func(string) bool { return false }
		}
		return r.MatchString
	}
	if firstUpper(p) {
		return func(s string) bool { return strings.Contains(s, p) }
	}
	lp := strings.ToLower(p)
	return func(s string) bool { return strings.Contains(strings.ToLower(s), lp) }
}

// nextRun computes the next scheduled run after now (nil for event
// triggers).
func nextRun(l LoopDef, now time.Time, loc *time.Location) *time.Time {
	switch l.Trigger.Kind {
	case "interval":
		t := now.Add(time.Duration(l.Trigger.EverySec) * time.Second).UTC()
		return &t
	case "cron":
		c, err := ParseCron(l.Trigger.Expr)
		if err != nil {
			return nil
		}
		t := c.Next(now.In(loc))
		if t.IsZero() {
			return nil
		}
		u := t.UTC()
		return &u
	}
	return nil
}

// RunsPerHour estimates prompts per hour per target for a confirmation.
func RunsPerHour(l LoopDef, now time.Time, loc *time.Location) float64 {
	switch l.Trigger.Kind {
	case "interval":
		return 3600 / float64(l.Trigger.EverySec)
	case "cron":
		if c, err := ParseCron(l.Trigger.Expr); err == nil {
			return c.RunsPerHour(now.In(loc))
		}
	}
	return 0
}

// ---- store ----

// loopEnv is what the scheduler needs from the hub.
type loopEnv interface {
	loopTargets(l LoopDef) []session.Info
	deliverLoop(l LoopDef, in session.Info) (delivery.Delivery, error)
	deliveryOf(id string) (delivery.Delivery, bool)
	sessionInfo(id string) (session.Info, bool)
	lastReply(sid string) string
	// goalAgent / goalCommand run asynchronously and call done once;
	// ok=false means inconclusive (not logged in, timeout, …).
	goalAgent(l LoopDef, reply string, done func(met, ok bool, reason string))
	goalCommand(l LoopDef, done func(met, ok bool, reason string))
	loopAttention(kind string, l LoopDef, in *session.Info, body string)
	publish(v any)
}

type loopRun struct {
	at       time.Time
	kind     string // run | retry
	results  []LoopTargetResult
	pending  map[string]string // deliveryId → sessionId
	only     string            // event run for one console
	retrying bool
}

type loopRT struct {
	def       LoopDef
	run       *loopRun
	retryAt   time.Time
	retried   bool
	goalAt    time.Time
	goalBusy  bool
	waitGoal  bool // a due run waits for its goal check
	lastFire  map[string]time.Time
	idlePend  map[string]time.Time
	outPend   map[string]bool
	match     func(string) bool
	stopMatch func(string) bool
}

// Loops is the loop store and scheduler.
type Loops struct {
	st  *store.Store
	env loopEnv
	Loc *time.Location

	mu        sync.Mutex
	loops     map[string]*loopRT
	order     []string
	goalsBusy int
}

func newLoops(st *store.Store, env loopEnv) *Loops {
	_ = os.MkdirAll(st.Path("loops"), 0o700)
	l := &Loops{st: st, env: env, Loc: time.Local, loops: map[string]*loopRT{}}
	var list []LoopDef
	if _, err := st.Load(loopsFile, &list); err == nil {
		for _, d := range list {
			if d.ID == "" {
				continue
			}
			l.loops[d.ID] = newRT(d)
			l.order = append(l.order, d.ID)
		}
	}
	return l
}

func newRT(d LoopDef) *loopRT {
	rt := &loopRT{def: d, lastFire: map[string]time.Time{}, idlePend: map[string]time.Time{}, outPend: map[string]bool{}}
	rt.compile()
	return rt
}

func (rt *loopRT) compile() {
	rt.match = patternMatcher(rt.def.Trigger.Pattern, rt.def.Trigger.Regex)
	rt.stopMatch = patternMatcher(rt.def.Stop.OutputPattern, false)
}

// Restart recomputes nextRunAt of running loops from now: runs missed
// while the hub was down are not repeated (§11).
func (l *Loops) Restart(now time.Time) {
	l.mu.Lock()
	for _, rt := range l.loops {
		if rt.def.State.Status == "running" {
			rt.def.State.NextRunAt = nextRun(rt.def, now, l.Loc)
		}
	}
	l.mu.Unlock()
	l.save()
}

func (l *Loops) save() {
	l.mu.Lock()
	list := make([]LoopDef, 0, len(l.order))
	for _, id := range l.order {
		list = append(list, l.loops[id].def)
	}
	l.mu.Unlock()
	_ = l.st.Save(loopsFile, list)
}

// List returns loops of a project ("" = all).
func (l *Loops) List(projectID string) []LoopDef {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []LoopDef{}
	for _, id := range l.order {
		if d := l.loops[id].def; projectID == "" || d.ProjectID == projectID {
			out = append(out, d)
		}
	}
	return out
}

// Get returns one loop.
func (l *Loops) Get(id string) (LoopDef, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rt, ok := l.loops[id]
	if !ok {
		return LoopDef{}, false
	}
	return rt.def, true
}

// Create validates and adds a loop (running when enabled).
func (l *Loops) Create(d LoopDef, now time.Time, byPlanner bool) (LoopDef, error) {
	if err := ValidateLoop(&d, byPlanner); err != nil {
		return LoopDef{}, err
	}
	l.mu.Lock()
	if len(l.loops) >= maxLoops {
		l.mu.Unlock()
		return LoopDef{}, ErrTooManyLoops
	}
	d.ID = auth.RandomID("lp_")
	d.CreatedAt = now.UTC()
	d.State = LoopState{Status: "stopped"}
	if d.Enabled {
		start(&d, now, l.Loc)
	}
	l.loops[d.ID] = newRT(d)
	l.order = append(l.order, d.ID)
	l.mu.Unlock()
	l.save()
	l.env.publish(map[string]any{"type": "loop", "loop": d})
	return d, nil
}

func start(d *LoopDef, now time.Time, loc *time.Location) {
	t := now.UTC()
	d.Enabled = true
	d.State = LoopState{Status: "running", StartedAt: &t, NextRunAt: nextRun(*d, now, loc)}
}

// Patch changes fields of a loop (JSON merge of the definition).
func (l *Loops) Patch(id string, patch map[string]json.RawMessage, now time.Time) (LoopDef, error) {
	l.mu.Lock()
	rt, ok := l.loops[id]
	if !ok {
		l.mu.Unlock()
		return LoopDef{}, ErrLoopNotFound
	}
	cur := rt.def
	l.mu.Unlock()
	b, _ := json.Marshal(cur)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	for k, v := range patch {
		switch k {
		case "id", "projectId", "state", "createdAt", "createdBy":
			continue
		}
		m[k] = v
	}
	b, _ = json.Marshal(m)
	var next LoopDef
	if err := json.Unmarshal(b, &next); err != nil {
		return LoopDef{}, badLoop("invalid JSON")
	}
	if err := ValidateLoop(&next, false); err != nil {
		return LoopDef{}, err
	}
	l.mu.Lock()
	rt, ok = l.loops[id]
	if !ok {
		l.mu.Unlock()
		return LoopDef{}, ErrLoopNotFound
	}
	next.State = rt.def.State
	switch {
	case next.Enabled && next.State.Status != "running":
		start(&next, now, l.Loc)
	case !next.Enabled && next.State.Status == "running":
		next.State.Status, next.State.StoppedReason, next.State.NextRunAt = "stopped", "user", nil
	case next.State.Status == "running":
		next.State.NextRunAt = nextRun(next, now, l.Loc)
	}
	rt.def = next
	rt.compile()
	l.mu.Unlock()
	l.save()
	l.env.publish(map[string]any{"type": "loop", "loop": next})
	return next, nil
}

// Delete removes a loop and its history.
func (l *Loops) Delete(id string) error {
	l.mu.Lock()
	if _, ok := l.loops[id]; !ok {
		l.mu.Unlock()
		return ErrLoopNotFound
	}
	delete(l.loops, id)
	l.order = slices.DeleteFunc(l.order, func(x string) bool { return x == id })
	l.mu.Unlock()
	_ = os.Remove(l.histPath(id))
	l.save()
	l.env.publish(map[string]any{"type": "loopRemoved", "id": id})
	return nil
}

// Start / Stop by user.
func (l *Loops) Start(id string, now time.Time) (LoopDef, error) {
	l.mu.Lock()
	rt, ok := l.loops[id]
	if !ok {
		l.mu.Unlock()
		return LoopDef{}, ErrLoopNotFound
	}
	if rt.def.State.Status != "running" {
		start(&rt.def, now, l.Loc)
		rt.retried, rt.run = false, nil
	}
	d := rt.def
	l.mu.Unlock()
	l.save()
	l.env.publish(map[string]any{"type": "loop", "loop": d})
	return d, nil
}

func (l *Loops) Stop(id, reason string, now time.Time) (LoopDef, error) {
	l.mu.Lock()
	rt, ok := l.loops[id]
	if !ok {
		l.mu.Unlock()
		return LoopDef{}, ErrLoopNotFound
	}
	changed := l.stopLocked(rt, reason)
	d := rt.def
	l.mu.Unlock()
	if changed {
		l.finishStop(d, now)
	}
	return d, nil
}

// StopAll stops every running loop.
func (l *Loops) StopAll(now time.Time) int {
	return l.stopWhere(func(LoopDef) bool { return true }, now)
}

// StopProject stops the running loops of a project.
func (l *Loops) StopProject(projectID string, now time.Time) int {
	return l.stopWhere(func(d LoopDef) bool { return d.ProjectID == projectID }, now)
}

func (l *Loops) stopWhere(match func(LoopDef) bool, now time.Time) int {
	l.mu.Lock()
	var stopped []LoopDef
	for _, rt := range l.loops {
		if match(rt.def) && l.stopLocked(rt, "user") {
			stopped = append(stopped, rt.def)
		}
	}
	l.mu.Unlock()
	for _, d := range stopped {
		l.finishStop(d, now)
	}
	return len(stopped)
}

func (l *Loops) stopLocked(rt *loopRT, reason string) bool {
	if rt.def.State.Status != "running" {
		return false
	}
	status := "stopped"
	switch reason {
	case "goal", "maxRuns", "output":
		status = "done"
	}
	rt.def.State.Status, rt.def.State.StoppedReason, rt.def.State.NextRunAt = status, reason, nil
	if reason == "user" {
		rt.def.Enabled = false
	}
	rt.run, rt.waitGoal = nil, false
	return true
}

func (l *Loops) finishStop(d LoopDef, now time.Time) {
	l.save()
	l.addHistory(d.ID, LoopHistoryEntry{At: now.UTC(), Kind: "stop", Note: d.State.StoppedReason})
	l.env.publish(map[string]any{"type": "loop", "loop": d})
	l.env.loopAttention("loopEnded", d, nil, fmt.Sprintf("%s stopped (%s) after %d runs", d.Name, d.State.StoppedReason, d.State.Runs))
}

// RunNow starts a run immediately.
func (l *Loops) RunNow(id string, now time.Time) (LoopDef, error) {
	l.mu.Lock()
	rt, ok := l.loops[id]
	if !ok {
		l.mu.Unlock()
		return LoopDef{}, ErrLoopNotFound
	}
	d := rt.def
	busy := rt.run != nil
	l.mu.Unlock()
	if !busy {
		l.startRun(id, now, "run", "")
	}
	return d, nil
}

// ---- history ----

func (l *Loops) histPath(id string) string { return l.st.Path("loops", id+".jsonl") }

func (l *Loops) addHistory(id string, e LoopHistoryEntry) {
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(l.histPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		_, _ = f.Write(append(b, '\n'))
		f.Close()
	}
	if items := l.History(id, 1000); len(items) > 2*loopHistoryKeep {
		var buf bytes.Buffer
		for _, it := range items[len(items)-loopHistoryKeep:] {
			x, _ := json.Marshal(it)
			buf.Write(append(x, '\n'))
		}
		_ = store.WriteFileAtomic(l.histPath(id), buf.Bytes())
	}
	l.env.publish(map[string]any{"type": "loopRun", "loopId": id, "entry": e})
}

// History returns the last entries, oldest first.
func (l *Loops) History(id string, limit int) []LoopHistoryEntry {
	out := []LoopHistoryEntry{}
	f, err := os.Open(l.histPath(id))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e LoopHistoryEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// ---- scheduler ----

// Tick checks stop conditions, settles runs and starts due runs.
func (l *Loops) Tick(now time.Time) {
	type due struct {
		id, kind, only string
	}
	var starts []due
	var stops []LoopDef
	var settle []string
	var goals []string
	l.mu.Lock()
	for _, id := range l.order {
		rt := l.loops[id]
		st := &rt.def.State
		if st.Status != "running" {
			continue
		}
		reason := ""
		switch {
		case rt.def.Stop.MaxDurationSec > 0 && st.StartedAt != nil && now.Sub(*st.StartedAt) >= time.Duration(rt.def.Stop.MaxDurationSec)*time.Second:
			reason = "time"
		case rt.def.Stop.Until != nil && !now.Before(*rt.def.Stop.Until):
			reason = "time"
		}
		if reason != "" {
			if l.stopLocked(rt, reason) {
				stops = append(stops, rt.def)
			}
			continue
		}
		if rt.run != nil {
			settle = append(settle, id)
			continue
		}
		if rt.waitGoal {
			continue
		}
		if !rt.retryAt.IsZero() && !now.Before(rt.retryAt) {
			rt.retryAt = time.Time{}
			starts = append(starts, due{id, "retry", ""})
			continue
		}
		switch rt.def.Trigger.Kind {
		case "interval", "cron":
			if st.NextRunAt != nil && !now.Before(*st.NextRunAt) {
				st.NextRunAt = nextRun(rt.def, now, l.Loc)
				if rt.def.Stop.Goal != nil && now.Sub(rt.goalAt) >= goalEvery && !rt.goalBusy && l.goalsBusy < goalConcurrent {
					rt.waitGoal = true
					goals = append(goals, id)
				} else {
					starts = append(starts, due{id, "run", ""})
				}
			}
		case "agentIdle", "output":
			for sid := range rt.idlePend {
				in, ok := l.env.sessionInfo(sid)
				delete(rt.idlePend, sid)
				if ok && in.AgentState == agentstate.Ready && !in.Paused {
					starts = append(starts, due{id, "run", sid})
					break
				}
			}
			for sid := range rt.outPend {
				in, ok := l.env.sessionInfo(sid)
				if !ok {
					delete(rt.outPend, sid)
					continue
				}
				if in.AgentState == agentstate.Ready && !in.Paused {
					delete(rt.outPend, sid)
					starts = append(starts, due{id, "run", sid})
					break
				}
			}
		}
	}
	l.mu.Unlock()
	for _, d := range stops {
		l.finishStop(d, now)
	}
	for _, id := range settle {
		l.settle(id, now, false)
	}
	for _, id := range goals {
		l.checkGoal(id, now, true)
	}
	for _, d := range starts {
		l.startRun(d.id, now, d.kind, d.only)
	}
}

// checkGoal runs the goal check; with thenRun a due run follows unless
// the goal is met (inconclusive results still run).
func (l *Loops) checkGoal(id string, now time.Time, thenRun bool) {
	l.mu.Lock()
	rt, ok := l.loops[id]
	if !ok || rt.def.Stop.Goal == nil || rt.goalBusy {
		if ok {
			rt.waitGoal = false
		}
		l.mu.Unlock()
		return
	}
	rt.goalBusy, rt.goalAt = true, now
	l.goalsBusy++
	d := rt.def
	l.mu.Unlock()
	done := func(met, conclusive bool, reason string) {
		l.mu.Lock()
		rt, ok := l.loops[id]
		l.goalsBusy--
		if !ok {
			l.mu.Unlock()
			return
		}
		rt.goalBusy, rt.waitGoal = false, false
		running := rt.def.State.Status == "running"
		l.mu.Unlock()
		note := reason
		if !conclusive {
			note = "inconclusive: " + reason
		}
		l.addHistory(id, LoopHistoryEntry{At: now.UTC(), Kind: "goalCheck", Note: note, Goal: &GoalResult{Met: met && conclusive, Reason: reason}})
		if !running {
			return
		}
		if met && conclusive {
			l.Stop(id, "goal", now)
			return
		}
		if thenRun {
			l.startRun(id, now, "run", "")
		}
	}
	if d.Stop.Goal.Kind == "command" {
		l.env.goalCommand(d, done)
		return
	}
	reply := ""
	for _, in := range l.env.loopTargets(d) {
		if r := l.env.lastReply(in.ID); r != "" {
			reply += "## @" + in.Handle + "\n" + r + "\n\n"
		}
	}
	if len(reply) > loopReplyForGoal {
		reply = reply[len(reply)-loopReplyForGoal:]
	}
	l.env.goalAgent(d, reply, done)
}

// startRun delivers the loop text to its targets (only = one console for
// event triggers).
func (l *Loops) startRun(id string, now time.Time, kind, only string) {
	l.mu.Lock()
	rt, ok := l.loops[id]
	if !ok || rt.def.State.Status != "running" || rt.run != nil {
		l.mu.Unlock()
		return
	}
	d := rt.def
	run := &loopRun{at: now, kind: kind, pending: map[string]string{}, only: only, retrying: kind == "retry"}
	rt.run = run
	if only != "" {
		rt.lastFire[only] = now
	}
	l.mu.Unlock()
	targets := l.env.loopTargets(d)
	var results []LoopTargetResult
	pending := map[string]string{}
	for _, in := range targets {
		if only != "" && in.ID != only {
			continue
		}
		res := ""
		switch {
		case in.AgentKind != "agent" || in.Foreground == agentstate.FgShell || in.AgentState == agentstate.Shell:
			res = "notAgent"
		case in.Paused:
			res = "paused"
		case in.AgentState == agentstate.NeedsYou:
			res = "needsYou"
			cp := in
			l.env.loopAttention("loopHeld", d, &cp, fmt.Sprintf("%s skipped @%s: waiting for you", d.Name, in.Handle))
		case in.AgentState == agentstate.Exited || in.AgentState == agentstate.Error:
			res = "notAgent"
		}
		if res == "" {
			dl, err := l.env.deliverLoop(d, in)
			if err != nil {
				res = "busy"
				if errors.Is(err, delivery.ErrNotAgent) {
					res = "notAgent"
				}
			} else {
				pending[dl.ID] = in.ID
				continue
			}
		}
		results = append(results, LoopTargetResult{SessionID: in.ID, Result: res})
	}
	l.mu.Lock()
	if rt, ok := l.loops[id]; ok && rt.run == run {
		run.results, run.pending = results, pending
	}
	l.mu.Unlock()
	l.settle(id, now, false)
}

// settle records a run once its deliveries are final (or after 75 s).
func (l *Loops) settle(id string, now time.Time, force bool) {
	l.mu.Lock()
	rt, ok := l.loops[id]
	if !ok || rt.run == nil {
		l.mu.Unlock()
		return
	}
	run := rt.run
	for dlID, sid := range run.pending {
		dl, ok := l.env.deliveryOf(dlID)
		final := !ok || dl.Final() || dl.State == delivery.Sent
		if !final && !force && now.Sub(run.at) < loopSettleMax {
			continue
		}
		res := "busy"
		if ok && (dl.State == delivery.Sent || dl.State == delivery.Confirmed) {
			res = "delivered"
		} else if ok && (dl.Code == "not_agent" || dl.Code == "agent_exited") {
			res = "notAgent"
		}
		run.results = append(run.results, LoopTargetResult{SessionID: sid, Result: res})
		delete(run.pending, dlID)
	}
	if len(run.pending) > 0 {
		l.mu.Unlock()
		return
	}
	rt.run = nil
	delivered := 0
	for _, r := range run.results {
		if r.Result == "delivered" {
			delivered++
		}
	}
	st := &rt.def.State
	entry := LoopHistoryEntry{At: run.at.UTC(), Kind: run.kind, Targets: run.results}
	if delivered > 0 {
		st.Runs++
		t := run.at.UTC()
		st.LastRunAt = &t
		rt.retried = false
		entry.Note = fmt.Sprintf("delivered to %d console(s)", delivered)
	} else {
		entry.Kind = "skip"
		entry.Note = "no console reached"
		if !run.retrying && !rt.retried && run.only == "" {
			rt.retried = true
			rt.retryAt = now.Add(loopRetryAfter)
			entry.Note += "; retry in 60 s"
		}
	}
	stop := rt.def.Stop.MaxRuns > 0 && st.Runs >= rt.def.Stop.MaxRuns && l.stopLocked(rt, "maxRuns")
	d := rt.def
	l.mu.Unlock()
	l.addHistory(id, entry)
	l.save()
	l.env.publish(map[string]any{"type": "loop", "loop": d})
	if stop {
		l.finishStop(d, now)
	}
}

// TurnFinished feeds agentIdle triggers and goal checks after a turn.
func (l *Loops) TurnFinished(sid string, now time.Time) {
	var goals []string
	l.mu.Lock()
	for _, id := range l.order {
		rt := l.loops[id]
		if rt.def.State.Status != "running" || !l.targets(rt, sid) {
			continue
		}
		if rt.def.Trigger.Kind == "agentIdle" && now.Sub(rt.lastFire[sid]) >= loopEventEvery && rt.run == nil {
			rt.idlePend[sid] = now
		}
		if rt.def.Stop.Goal != nil && rt.run == nil && !rt.goalBusy && now.Sub(rt.goalAt) >= goalEvery && l.goalsBusy < goalConcurrent {
			goals = append(goals, id)
		}
	}
	l.mu.Unlock()
	for _, id := range goals {
		l.checkGoal(id, now, false)
	}
}

// Output feeds output triggers and stop.outputPattern.
func (l *Loops) Output(sid, text string, now time.Time) {
	var stops []string
	l.mu.Lock()
	for _, id := range l.order {
		rt := l.loops[id]
		if rt.def.State.Status != "running" || (rt.def.Stop.OutputPattern == "" && rt.def.Trigger.Kind != "output") || !l.targets(rt, sid) {
			continue
		}
		if rt.def.Stop.OutputPattern != "" && rt.stopMatch(text) {
			stops = append(stops, id)
			continue
		}
		if rt.def.Trigger.Kind == "output" && rt.match(text) && now.Sub(rt.lastFire[sid]) >= loopEventEvery && rt.run == nil {
			rt.outPend[sid] = true
		}
	}
	l.mu.Unlock()
	for _, id := range stops {
		l.Stop(id, "output", now)
	}
}

// targets reports whether a session is one of a loop's targets. Caller
// holds l.mu; resolution happens outside the lock in loopTargets, so this
// uses the cached ids for handles and asks the env otherwise.
func (l *Loops) targets(rt *loopRT, sid string) bool {
	if rt.def.Targets.Kind == "handles" {
		return slices.Contains(rt.def.Targets.IDs, sid)
	}
	for _, in := range l.env.loopTargets(rt.def) {
		if in.ID == sid {
			return true
		}
	}
	return false
}

// Templates are the built-in loop templates (names by NextBoard).
func LoopTemplates() []map[string]any {
	t := func(name, text string, trig LoopTrigger, stop LoopStop) map[string]any {
		return map[string]any{"name": name, "text": text, "trigger": trig, "stop": stop, "targets": LoopTargets{Kind: "allAgents", IDs: []string{}}}
	}
	return []map[string]any{
		t("Status ping", "Give a one-paragraph status update: what you finished, what you are doing, what blocks you.", LoopTrigger{Kind: "interval", EverySec: 1800}, LoopStop{MaxRuns: 16}),
		t("Nudge idle agents", "If you stopped before finishing the task, continue where you left off. If you are done, reply DONE.", LoopTrigger{Kind: "agentIdle"}, LoopStop{MaxRuns: 20}),
		t("Build fixer", "The build is failing. Read the latest error output, fix the cause and run the build again.", LoopTrigger{Kind: "output", Pattern: "error"}, LoopStop{MaxRuns: 10, OutputPattern: "Build succeeded"}),
		t("Test guard", "Run the test suite and fix any failure.", LoopTrigger{Kind: "interval", EverySec: 900}, LoopStop{MaxRuns: 20, MaxDurationSec: 7200}),
		t("Checkpoint commit", "If there are meaningful uncommitted changes, commit them with a short descriptive message. Do not push.", LoopTrigger{Kind: "interval", EverySec: 3600}, LoopStop{MaxRuns: 12}),
		t("Review round", "Review the changes made since your last review. List concrete problems with file and line, most important first.", LoopTrigger{Kind: "interval", EverySec: 3600}, LoopStop{MaxRuns: 8}),
		t("Daily summary", "Summarise today's work in this project: what changed, what is left, open questions.", LoopTrigger{Kind: "cron", Expr: "0 18 * * 1-5"}, LoopStop{MaxRuns: 30}),
	}
}
