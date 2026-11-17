package orch

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/board"
)

// The life of a mission (docs/API-orchestration.md §22): proposed by the lead (or POST
// /api/projects/{id}/missions) and approved once; running, the hub sends each task whose
// dependencies are done as an order and wakes the lead on what happens; the lead's
// answer may change the brief and the tasks, answer questions, ask the owner, or say
// the mission is done. Limits (rounds, hours, no progress) and risky steps hold it for
// the owner; the owner can pause, resume and stop it.

const (
	missionDebounce = 5 * time.Second
	// missionQuestionWait: a team question nobody answered by then wakes the lead.
	missionQuestionWait = 10 * time.Minute
	// missionStuckWakes: wakes in a row that found no task changed hold the mission.
	missionStuckWakes  = 3
	missionRetry       = time.Minute
	missionDocForLead  = 8000
	missionTeamForLead = 30
)

// missionWakeState is a mission's pending lead wake and what the hub remembers between
// wakes: the reasons gathered, when the wake is due, whether a lead turn of it runs now.
type missionWakeState struct {
	reasons []string
	due     time.Time
	busy    bool
	// sig is the tasks' states at the last wake, idle the wakes in a row that found the same.
	sig  string
	idle int
	// unresolved: tasks whose console was not found, said to the lead once.
	unresolved map[string]bool
	// dispatching: a dispatch runs; again: something changed meanwhile, dispatch once more.
	dispatching, again bool
	// unavailable: the lead could not think; said once until it can again.
	unavailable bool
	// endedSig: the tasks' states when the lead last heard that every task had ended.
	endedSig string
	// startFailed: a round could not start; said once until one does.
	startFailed bool
}

// wakeStateLocked returns the mission's wake state. Caller holds s.mu.
func (s *Service) wakeStateLocked(id string) *missionWakeState {
	w := s.mwake[id]
	if w == nil {
		w = &missionWakeState{unresolved: map[string]bool{}}
		s.mwake[id] = w
	}
	return w
}

// missionWake gathers a reason; the wake is due 5 s after the first one.
func (s *Service) missionWake(id, reason string, now time.Time) {
	if _, ok := s.Missions.Get(id); !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.wakeStateLocked(id)
	if reason = clipText(reason, 300); reason != "" && len(w.reasons) < 30 && !slices.Contains(w.reasons, reason) {
		w.reasons = append(w.reasons, reason)
	}
	if w.due.IsZero() {
		w.due = now.Add(missionDebounce)
	}
}

// ---- the mission object of the lead's plan and of POST /missions ----

// MissionSpec is the "mission" object of the lead's plan (PlanSchema) and the body of
// POST /api/projects/{id}/missions. Proposing: goal, summary, doneWhen, doc, tasks. On a
// wake: doc (the whole new text), tasks (new or changed by id), answers, done, askOwner.
type MissionSpec struct {
	Goal     string            `json:"goal"`
	Summary  string            `json:"summary"`
	DoneWhen string            `json:"doneWhen"`
	Doc      *string           `json:"doc"`
	Tasks    []MissionTaskSpec `json:"tasks"`
	Answers  []MissionAnswer   `json:"answers,omitempty"`
	Done     *MissionDoneSpec  `json:"done,omitempty"`
	AskOwner *MissionAskSpec   `json:"askOwner,omitempty"`
}

// MissionTaskSpec is a task as the lead or an API client writes it.
type MissionTaskSpec struct {
	ID        string   `json:"id"`
	Handle    string   `json:"handle"`
	Title     string   `json:"title"`
	Text      string   `json:"text"`
	DependsOn []string `json:"dependsOn"`
	State     string   `json:"state,omitempty"` // "cancelled" cancels the task
	sessionID string
}

// MissionAnswer is the lead's answer to a question asked of it.
type MissionAnswer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// MissionDoneSpec ends a mission with the summary for the owner.
type MissionDoneSpec struct {
	Summary string `json:"summary"`
}

// MissionAskSpec is the lead's question to the owner.
type MissionAskSpec struct {
	Question string `json:"question"`
}

// MissionRequest is POST /api/projects/{id}/missions.
type MissionRequest struct {
	MissionSpec
	ThreadID    string `json:"threadId"`
	CardID      string `json:"cardId"`
	WorkspaceID string `json:"workspaceId"`
	ProjectID   string `json:"-"`
	By          string `json:"-"`
}

func badMission(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadMission, fmt.Sprintf(format, a...))
}

var taskIDRe = regexp.MustCompile(`^t(\d{1,4})$`)

// mergeTasks applies task specs to a mission's tasks: a new id (or none) adds a task, a
// known id changes it, "cancelled" cancels it. Handles are resolved in the scope. strict
// (an API client) refuses what the lead's plan only loses with a note.
func mergeTasks(sc *scope, tasks []MissionTask, specs []MissionTaskSpec, strict bool, now time.Time) ([]MissionTask, []string, error) {
	tasks = slices.Clone(tasks)
	var notes []string
	next := 1
	for _, t := range tasks {
		if m := taskIDRe.FindStringSubmatch(t.ID); m != nil {
			if n, _ := strconv.Atoi(m[1]); n >= next {
				next = n + 1
			}
		}
	}
	for _, sp := range specs {
		if m := taskIDRe.FindStringSubmatch(strings.TrimSpace(sp.ID)); m != nil {
			if n, _ := strconv.Atoi(m[1]); n >= next {
				next = n + 1
			}
		}
	}
	for _, sp := range specs {
		id := strings.TrimSpace(sp.ID)
		i := slices.IndexFunc(tasks, func(t MissionTask) bool { return t.ID == id && id != "" })
		if i < 0 {
			if id != "" && !taskIDRe.MatchString(id) {
				if strict {
					return nil, nil, badMission("task id %q: use t1, t2, …", id)
				}
				id = ""
			}
			if id == "" {
				id = "t" + strconv.Itoa(next)
				next++
			}
			if sp.State == TaskCancelled {
				continue
			}
			if len(tasks) >= maxMissionTasks {
				if strict {
					return nil, nil, badMission("at most %d tasks", maxMissionTasks)
				}
				notes = append(notes, fmt.Sprintf("A mission has at most %d tasks; the rest were left out.", maxMissionTasks))
				break
			}
			tasks = append(tasks, MissionTask{ID: id, State: TaskWaiting, UpdatedAt: now})
			i = len(tasks) - 1
		}
		t := &tasks[i]
		if sp.State == TaskCancelled {
			// its order, if any, is cancelled by the caller (cancelledOrders): a task that
			// was cancelled no longer follows one
			if t.State == TaskWaiting || t.State == TaskSent {
				t.State, t.OrderID, t.UpdatedAt = TaskCancelled, "", now
			}
			continue
		}
		text := strings.TrimSpace(sp.Text)
		if text == "" && t.Text == "" {
			if strict {
				return nil, nil, badMission("task %s has no text", id)
			}
			tasks = slices.Delete(tasks, i, i+1)
			notes = append(notes, "Task "+id+" had no text; left out.")
			continue
		}
		if len([]rune(text)) > maxMissionTaskText || len([]rune(sp.Title)) > maxMissionTitle {
			if strict {
				return nil, nil, badMission("task %s: text ≤ %d, title ≤ %d characters", id, maxMissionTaskText, maxMissionTitle)
			}
			text = clipLen(text, maxMissionTaskText)
		}
		redo := text != "" && text != t.Text && t.Text != ""
		if text != "" {
			t.Text = text
		}
		if title := clipLen(strings.TrimSpace(sp.Title), maxMissionTitle); title != "" {
			t.Title = title
		} else if t.Title == "" {
			t.Title = clipText(t.Text, 80)
		}
		if sp.sessionID != "" {
			if in, ok := sc.byID[sp.sessionID]; ok {
				t.SessionID, t.Handle = in.ID, in.Handle
			}
		} else if h := strings.TrimPrefix(strings.TrimSpace(sp.Handle), "@"); h != "" && Norm(h) != Norm(t.Handle) {
			t.Handle, t.SessionID = h, ""
			if r := sc.resolve([]string{h}); len(r.targets) > 0 {
				t.SessionID, t.Handle = r.targets[0], sc.handle(r.targets[0])
			} else if strict {
				return nil, nil, badMission("task %s: no console @%s here", id, h)
			}
		}
		if t.Handle == "" {
			if strict {
				return nil, nil, badMission("task %s names no console", id)
			}
			tasks = slices.Delete(tasks, i, i+1)
			notes = append(notes, "Task "+id+" named no console; left out.")
			continue
		}
		if sp.DependsOn != nil {
			t.DependsOn = nil
			for _, d := range sp.DependsOn {
				if d = strings.TrimSpace(d); d != "" && d != t.ID && !slices.Contains(t.DependsOn, d) {
					t.DependsOn = append(t.DependsOn, d)
				}
			}
		}
		// new work for a task that ended is done again
		if redo && (t.State == TaskDone || t.State == TaskFailed || t.State == TaskCancelled) {
			t.State, t.OrderID, t.Result, t.ResultBy = TaskWaiting, "", "", ""
		}
		t.UpdatedAt = now
	}
	// dependencies on tasks that do not exist, and cycles, are refused or dropped
	ids := map[string]bool{}
	for _, t := range tasks {
		ids[t.ID] = true
	}
	for i := range tasks {
		var keep []string
		for _, d := range tasks[i].DependsOn {
			if ids[d] {
				keep = append(keep, d)
			} else if strict {
				return nil, nil, badMission("task %s depends on %s, which is not a task", tasks[i].ID, d)
			}
		}
		tasks[i].DependsOn = keep
	}
	if c := cycleOf(tasks); c != "" {
		if strict {
			return nil, nil, badMission("tasks depend on each other in a circle (%s)", c)
		}
		for i := range tasks {
			if tasks[i].State == TaskWaiting {
				tasks[i].DependsOn = nil
			}
		}
		notes = append(notes, "The tasks depended on each other in a circle; the waiting ones no longer wait.")
	}
	return tasks, notes, nil
}

// cancelledOrders are the orders of tasks that a merge cancelled (before → after), to cancel
// outside the mission's lock.
func cancelledOrders(before, after []MissionTask) []string {
	var out []string
	for _, b := range before {
		if b.OrderID == "" || b.State == TaskCancelled {
			continue
		}
		if i := slices.IndexFunc(after, func(a MissionTask) bool { return a.ID == b.ID }); i >= 0 && after[i].State == TaskCancelled {
			out = append(out, b.OrderID)
		}
	}
	return out
}

// cancelOrders cancels what was not typed yet of these orders.
func (s *Service) cancelOrders(ids []string) {
	for _, id := range ids {
		if o, ok := s.Orders.Get(id); ok && o.Active() {
			_, _ = s.CancelOrder(id)
		}
	}
}

// cycleOf names a task that depends on itself through others, "" when none does.
func cycleOf(tasks []MissionTask) string {
	deps := map[string][]string{}
	for _, t := range tasks {
		deps[t.ID] = t.DependsOn
	}
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(id string) bool
	visit = func(id string) bool {
		switch state[id] {
		case 1:
			return true
		case 2:
			return false
		}
		state[id] = 1
		for _, d := range deps[id] {
			if visit(d) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for _, t := range tasks {
		if visit(t.ID) {
			return t.ID
		}
	}
	return ""
}

// CreateMission is POST /api/projects/{id}/missions: a mission awaiting the owner's
// approval, in the project's thread or an orchestrator card's.
func (s *Service) CreateMission(req MissionRequest) (MissionRun, error) {
	proj, ok := s.hub.Project(req.ProjectID)
	if !ok {
		return MissionRun{}, ErrProjectNotFound
	}
	tid, cardID := strings.TrimSpace(req.ThreadID), strings.TrimSpace(req.CardID)
	if cardID == "" && strings.HasPrefix(tid, "n_") {
		cardID = tid
	}
	if tid == "" {
		tid = firstNonEmpty(cardID, proj.ID)
	}
	if tid != proj.ID && tid != cardID {
		return MissionRun{}, badMission("threadId is the project's thread or the card's")
	}
	wid := strings.TrimSpace(req.WorkspaceID)
	if cardID != "" {
		wid = ""
		for _, w := range proj.Workspaces {
			if b, err := s.hub.Board(w.ID); err == nil && slices.ContainsFunc(b.Nodes, func(n *board.Node) bool { return n.ID == cardID && n.Kind == KindOrchestrator }) {
				wid = w.ID
			}
		}
		if wid == "" {
			return MissionRun{}, ErrBadCard
		}
	}
	if wid == "" && len(proj.Workspaces) > 0 {
		wid = proj.Workspaces[0].ID
	}
	if ws, p, ok := s.hub.Workspace(wid); !ok || p.ID != proj.ID {
		return MissionRun{}, ErrWorkspaceNotFound
	} else {
		wid = ws.ID
	}
	sc, err := s.newScope(wid, cardID)
	if err != nil {
		return MissionRun{}, err
	}
	if ms, ok := s.Missions.Active(tid); ok && ms.State != MissionAwaiting {
		return MissionRun{}, ErrMissionActive
	}
	c := &execCtx{commandID: auth.RandomID("cmd_"), threadID: tid, projectID: proj.ID, workspaceID: wid, cardID: cardID, by: req.By,
		chatID: s.Threads.ActiveChat(tid)}
	ms, _, err := s.proposeMission(c, sc, req.MissionSpec, true)
	return ms, err
}

// proposeMission makes a mission that awaits the owner's approval, or changes the one of
// the thread still awaiting it, and asks for the approval.
func (s *Service) proposeMission(c *execCtx, sc *scope, sp MissionSpec, strict bool) (MissionRun, []string, error) {
	now := s.Now().UTC()
	goal, summary, doneWhen := strings.TrimSpace(sp.Goal), strings.TrimSpace(sp.Summary), strings.TrimSpace(sp.DoneWhen)
	doc := ""
	if sp.Doc != nil {
		doc = strings.TrimSpace(*sp.Doc)
	}
	if goal == "" {
		return MissionRun{}, nil, badMission("goal is required")
	}
	if strict && (len([]rune(goal)) > maxMissionGoal || len([]rune(summary)) > maxMissionSummary || len([]rune(doneWhen)) > maxMissionDoneWhen || len(doc) > maxMissionDoc) {
		return MissionRun{}, nil, badMission("goal ≤ %d, summary ≤ %d, doneWhen ≤ %d characters, doc ≤ 32 KB", maxMissionGoal, maxMissionSummary, maxMissionDoneWhen)
	}
	if strict && len(sp.Tasks) > maxMissionTasks {
		return MissionRun{}, nil, badMission("at most %d tasks", maxMissionTasks)
	}
	tasks, notes, err := mergeTasks(sc, nil, sp.Tasks, strict, now)
	if err != nil {
		return MissionRun{}, nil, err
	}
	if len(tasks) == 0 {
		return MissionRun{}, notes, badMission("a mission needs at least one task")
	}
	set := s.hub.Settings().Lead
	ms := MissionRun{ID: auth.RandomID("ms_"), ProjectID: c.projectID, ThreadID: c.threadID, WorkspaceID: c.workspaceID, CardID: c.cardID,
		Lead: s.leadOf(sc).name, CreatedAt: now, MaxRounds: missionRounds(set.MissionRounds), ChatID: s.chatOf(c)}
	if old, ok := s.Missions.Active(c.threadID); ok {
		if old.State != MissionAwaiting {
			return MissionRun{}, notes, ErrMissionActive
		}
		ms.ID, ms.CreatedAt = old.ID, old.CreatedAt
		ms.ChatID = firstNonEmpty(old.ChatID, ms.ChatID) // a changed plan stays in the chat it was proposed in
	}
	ms.Goal, ms.Summary, ms.DoneWhen = clipLen(goal, maxMissionGoal), clipLen(summary, maxMissionSummary), clipLen(doneWhen, maxMissionDoneWhen)
	ms.Doc = clipBytes(doc, maxMissionDoc)
	if ms.Doc != "" {
		ms.DocRev = 1
	}
	ms.Tasks, ms.State, ms.UpdatedAt = tasks, MissionAwaiting, now
	c.missionID = ""
	cf := &Confirmation{Kind: ConfMission, MissionID: ms.ID, Prompt: "Mission: " + clipText(ms.Goal, 200) + ". Approve the plan?",
		Plan: missionPlanNotes(ms), Options: []Option{{ID: "approve", Label: "Approve"}, {ID: "cancel", Label: "Cancel"}}}
	s.Missions.put(ms, now)
	s.MissionThreadMessage(ms, ThreadMessage{Handle: ms.Lead, Text: "Mission proposed: " + clipText(ms.Goal, 300)})
	s.addConfirm(c, cf)
	ms, _ = s.UpdateMission(ms.ID, func(m *MissionRun) bool { m.ConfirmationID = cf.ID; return true })
	return ms, notes, nil
}

// missionPlanNotes is the plan per agent for the approval card.
func missionPlanNotes(ms MissionRun) []PlanNote {
	var out []PlanNote
	for _, t := range ms.Tasks {
		if t.State == TaskCancelled {
			continue
		}
		text := t.ID + " · " + t.Title
		if len(t.DependsOn) > 0 {
			text += " (after " + strings.Join(t.DependsOn, ", ") + ")"
		}
		out = append(out, PlanNote{Handle: t.Handle, Text: text})
	}
	return out
}

func missionRounds(n int) int {
	if n <= 0 {
		return 30
	}
	return n
}

func missionHours(n int) time.Duration {
	if n <= 0 {
		n = 4
	}
	return time.Duration(n) * time.Hour
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// missionCtx is the context the hub acts in for a mission (its orders, its questions).
func (s *Service) missionCtx(ms MissionRun) *execCtx {
	return &execCtx{commandID: auth.RandomID("cmd_"), threadID: ms.ThreadID, projectID: ms.ProjectID, workspaceID: ms.WorkspaceID,
		cardID: ms.CardID, by: "lead", missionID: ms.ID, chatID: s.missionChat(ms)}
}

// ---- decisions ----

// missionDecided follows the owner's answer to a mission confirmation: the approval of
// the plan, going on past a limit or no progress, or adding consoles to the team.
func (s *Service) missionDecided(cf Confirmation, decision string, ctx *execCtx) {
	ms, ok := s.Missions.Get(cf.MissionID)
	if !ok || ms.ConfirmationID != cf.ID {
		return
	}
	now := s.Now().UTC()
	set := s.hub.Settings().Lead
	switch {
	case ms.State == MissionAwaiting && decision == "cancel":
		s.endMission(ms.ID, MissionStopped, "Not approved; nothing was sent to the team.")
	case ms.State == MissionAwaiting:
		ms, ok = s.UpdateMission(ms.ID, func(m *MissionRun) bool {
			if m.State != MissionAwaiting {
				return false
			}
			m.State, m.ApprovedAt, m.ConfirmationID, m.Waiting = MissionRunning, &now, "", ""
			m.MaxRounds, m.Deadline, m.Rounds = missionRounds(set.MissionRounds), now.Add(missionHours(set.MissionHours)), 0
			return true
		})
		if !ok {
			return
		}
		s.mu.Lock()
		w := s.wakeStateLocked(ms.ID)
		w.sig, w.idle = missionSig(ms), 0
		s.mu.Unlock()
		s.MissionThreadMessage(ms, ThreadMessage{Handle: ms.Lead, Text: "Mission started: " + clipText(ms.Goal, 300)})
		s.dispatchMission(ms.ID)
	case decision == "cancel" && len(cf.Steps) > 0:
		s.missionGoOn(ms.ID, "")
		s.MissionEvent(ms.ID, "the owner said no to: "+planText(cf.Plan))
	case decision == "cancel":
		s.endMission(ms.ID, MissionStopped, "Stopped by the owner. "+missionProgress(ms))
	case len(cf.Steps) > 0:
		ms, _ = s.missionGoOn(ms.ID, "")
		c := s.missionCtx(ms)
		c.teamOK = true
		if ctx != nil {
			c.commandID, c.clientID = ctx.commandID, ctx.clientID
		}
		s.MissionEvent(ms.ID, "the owner agreed to: "+planText(cf.Plan))
		// agreeing to go beyond the plan is not agreeing to what is risky in it: that asks
		// on its own, as any step does
		sc, err := s.newScope(ms.WorkspaceID, ms.CardID)
		if err != nil {
			// without its board the policy cannot look at the steps, so they do not run
			s.note(c, "Not done: the mission's board or orchestrator card is gone ("+err.Error()+").")
			return
		}
		if held := s.policy(c, sc, cf.Steps); held != nil {
			s.addConfirm(c, held)
			return
		}
		s.execute(c, cf.Steps)
	default:
		s.missionGoOn(ms.ID, "extend")
	}
}

func planText(p []PlanNote) string {
	var out []string
	for _, n := range p {
		if n.Handle != "" {
			out = append(out, "@"+n.Handle+": "+clipText(n.Text, 80))
		} else {
			out = append(out, clipText(n.Text, 80))
		}
	}
	return strings.Join(out, "; ")
}

// missionGoOn puts a mission that waited for the owner back to running; "extend" also
// gives it new limits when it reached them.
func (s *Service) missionGoOn(id, how string) (MissionRun, bool) {
	now := s.Now().UTC()
	set := s.hub.Settings().Lead
	ms, ok := s.UpdateMission(id, func(m *MissionRun) bool {
		if m.Ended() || m.State == MissionAwaiting {
			return false
		}
		m.State, m.Waiting, m.ConfirmationID = MissionRunning, "", ""
		if how == "extend" {
			if m.Rounds >= m.MaxRounds {
				m.MaxRounds = m.Rounds + missionRounds(set.MissionRounds)
			}
			if !now.Before(m.Deadline) {
				m.Deadline = now.Add(missionHours(set.MissionHours))
			}
		}
		return true
	})
	if !ok {
		return ms, false
	}
	s.mu.Lock()
	w := s.wakeStateLocked(id)
	w.idle, w.sig = 0, missionSig(ms)
	if len(w.reasons) > 0 && w.due.IsZero() {
		w.due = now.Add(missionDebounce)
	}
	// a lead turn of the mission only runs while the thread's turn is that wake's: anything
	// else left behind would keep its wakes from ever coming
	if tq := s.thread[ms.ThreadID]; w.busy && (tq == nil || !tq.busy || tq.ctx == nil || !tq.ctx.wake || tq.ctx.missionID != id) {
		w.busy = false
	}
	s.mu.Unlock()
	s.dispatchMission(id)
	return ms, true
}

// missionStepDecided: a step of the mission the hub held (risky, dangerous) was answered;
// the mission goes on by itself and the lead hears what the owner said.
func (s *Service) missionStepDecided(cf Confirmation, decision string) {
	ms, ok := s.Missions.Get(cf.MissionID)
	if !ok || ms.Ended() {
		return
	}
	if ms.State == MissionWaiting && ms.ConfirmationID == cf.ID {
		s.missionGoOn(ms.ID, "")
	}
	what := planText(cf.Plan)
	if what == "" {
		what = clipText(cf.Prompt, 200)
	}
	if decision == "cancel" {
		s.MissionEvent(ms.ID, "the owner said no to: "+what)
	} else {
		s.MissionEvent(ms.ID, "the owner agreed to: "+what)
	}
}

// missionHeld: a confirmation raised while the lead ran the mission holds it for the owner.
func (s *Service) missionHeld(id string, cf *Confirmation) {
	ms, ok := s.UpdateMission(id, func(m *MissionRun) bool {
		// waiting already: the new question took the old one's place in the thread, so the
		// mission waits on the new one. A paused mission stays paused; the question waits on its own.
		if m.State != MissionRunning && m.State != MissionWaiting {
			return false
		}
		m.State, m.Waiting, m.WaitKind, m.ConfirmationID = MissionWaiting, clipText(cf.Prompt, 300), WaitHeld, cf.ID
		return true
	})
	if ok {
		s.MissionThreadMessage(ms, ThreadMessage{Text: "Waiting for you: " + ms.Waiting})
	}
}

// missionConfirmGone: a mission's question that expired, or gave way to another one of the
// thread (why). An approval nobody gave ends the proposal; a held step is dropped and the
// mission goes on.
func (s *Service) missionConfirmGone(cf Confirmation, why string) {
	ms, ok := s.Missions.Get(cf.MissionID)
	if !ok || ms.ConfirmationID != cf.ID {
		return
	}
	switch {
	case ms.State == MissionAwaiting:
		s.endMission(ms.ID, MissionStopped, "The plan was "+why+"; nothing was sent to the team.")
	case cf.Kind != ConfMission || len(cf.Steps) > 0:
		s.missionGoOn(ms.ID, "")
		s.MissionEvent(ms.ID, "the owner did not answer about: "+firstNonEmpty(planText(cf.Plan), clipText(cf.Prompt, 200)))
	default:
		// a limit or no progress keeps waiting: the owner resumes it, or stops it
		s.UpdateMission(ms.ID, func(m *MissionRun) bool { m.ConfirmationID = ""; return true })
	}
}

// ---- owner controls ----

var (
	missionPauseWords  = regexp.MustCompile(`(?i)^(?:please )?(?:pause|hold) (?:the )?mission[.! ]*$|^(?:tolong )?jeda(?:kan)? misi(?:nya)?(?: dulu)?[.! ]*$`)
	missionResumeWords = regexp.MustCompile(`(?i)^(?:please )?(?:resume|continue) (?:the )?mission[.! ]*$|^(?:tolong )?(?:lanjutkan|lanjut|teruskan) misi(?:nya)?[.! ]*$`)
	missionStopWords   = regexp.MustCompile(`(?i)^(?:please )?(?:stop|end|cancel) (?:the )?mission[.! ]*$|^(?:tolong )?(?:hentikan|stop|batalkan|akhiri) misi(?:nya)?[.! ]*$`)
)

// missionControl recognises "pause mission", "resume mission", "stop mission" (and
// "jeda misi", "lanjutkan misi", "hentikan misi").
func missionControl(text string) string {
	t := strings.TrimSpace(text)
	switch {
	case missionPauseWords.MatchString(t):
		return "pause"
	case missionResumeWords.MatchString(t):
		return "resume"
	case missionStopWords.MatchString(t):
		return "stop"
	}
	return ""
}

// missionCommand carries out a control word typed in the thread.
func (s *Service) missionCommand(c *execCtx, ms MissionRun, what string) {
	var err error
	switch what {
	case "pause":
		_, err = s.PauseMission(ms.ID)
	case "resume":
		_, err = s.ResumeMission(ms.ID)
	case "stop":
		_, err = s.StopMission(ms.ID)
	}
	if err != nil {
		s.say(c, ThreadMessage{Kind: "reply", Text: "The mission is " + strings.ReplaceAll(ms.State, "_", " ") + "; it cannot " + what + " now."})
	}
}

// PauseMission (POST /api/missions/{id}/pause): deliveries not typed yet are cancelled and
// nothing new is sent; agents finish what they are doing.
func (s *Service) PauseMission(id string) (MissionRun, error) {
	ms, ok := s.Missions.Get(id)
	if !ok {
		return MissionRun{}, ErrMissionNotFound
	}
	if ms.State != MissionRunning && ms.State != MissionWaiting {
		return ms, ErrMissionState
	}
	question := ""
	// checked again as it is written: a mission that ended meanwhile stays ended
	paused, ok := s.UpdateMission(id, func(m *MissionRun) bool {
		if m.State != MissionRunning && m.State != MissionWaiting {
			return false
		}
		question = m.ConfirmationID
		m.State, m.Waiting, m.ConfirmationID = MissionPaused, "", ""
		return true
	})
	if !ok {
		ms, _ = s.Missions.Get(id)
		return ms, ErrMissionState
	}
	s.dropMissionConfirm(MissionRun{ConfirmationID: question})
	s.cancelMissionDeliveries(paused)
	ms, _ = s.Missions.Get(id)
	s.MissionThreadMessage(ms, ThreadMessage{Text: "Mission paused: nothing new goes out; the agents finish what they are doing."})
	return ms, nil
}

// ResumeMission (POST /api/missions/{id}/resume) runs a paused mission, or one waiting for
// the owner, again; a limit it reached starts over.
func (s *Service) ResumeMission(id string) (MissionRun, error) {
	ms, ok := s.Missions.Get(id)
	if !ok {
		return MissionRun{}, ErrMissionNotFound
	}
	if ms.State != MissionPaused && ms.State != MissionWaiting {
		return ms, ErrMissionState
	}
	s.dropMissionConfirm(ms)
	ms, _ = s.missionGoOn(id, "extend")
	s.MissionThreadMessage(ms, ThreadMessage{Text: "Mission resumed."})
	s.MissionEvent(id, "the owner resumed the mission")
	ms, _ = s.Missions.Get(id)
	return ms, nil
}

// StopMission (POST /api/missions/{id}/stop) ends a mission: its deliveries not typed yet
// are cancelled, and the thread says where it got to.
func (s *Service) StopMission(id string) (MissionRun, error) {
	ms, ok := s.Missions.Get(id)
	if !ok {
		return MissionRun{}, ErrMissionNotFound
	}
	if ms.Ended() {
		return ms, ErrMissionState
	}
	text := "Stopped by the owner. " + missionProgress(ms)
	if ms.State == MissionAwaiting {
		text = "Not approved; nothing was sent to the team."
	}
	return s.endMission(id, MissionStopped, text), nil
}

// dropMissionConfirm cancels the mission's open question to the owner, quietly.
func (s *Service) dropMissionConfirm(ms MissionRun) {
	if ms.ConfirmationID == "" {
		return
	}
	if d, err := s.Confirms.Decide(ms.ConfirmationID, "cancel", "", "hub", s.Now()); err == nil {
		s.publishConfirm(d)
	}
}

// cancelMissionDeliveries cancels the mission's orders not typed into a console yet.
func (s *Service) cancelMissionDeliveries(ms MissionRun) {
	for _, t := range ms.Tasks {
		if t.OrderID == "" || (t.State != TaskSent && t.State != TaskWaiting) {
			continue
		}
		if o, ok := s.Orders.Get(t.OrderID); ok && o.Active() {
			_, _ = s.CancelOrder(o.ID)
		}
	}
	s.teamHold() // and the team's messages not typed yet (missionteam.go)
}

// endMission ends a mission (done, stopped, failed) with the text the owner reads.
func (s *Service) endMission(id, state, result string) MissionRun {
	now := s.Now().UTC()
	question := ""
	// checked as it is written: of two ends at the same moment only one counts and speaks
	ms, ok := s.UpdateMission(id, func(m *MissionRun) bool {
		if m.Ended() {
			return false
		}
		question = m.ConfirmationID
		m.State, m.Result, m.EndedAt, m.Waiting, m.ConfirmationID = state, clipLen(result, maxMissionResult), &now, "", ""
		for i := range m.Tasks {
			if m.Tasks[i].State == TaskWaiting {
				m.Tasks[i].State, m.Tasks[i].UpdatedAt = TaskCancelled, now
			}
		}
		return true
	})
	if !ok {
		ms, _ = s.Missions.Get(id)
		return ms
	}
	s.dropMissionConfirm(MissionRun{ConfirmationID: question})
	// the mission as it was ended, not as read before: an order made in between is cancelled too
	s.cancelMissionDeliveries(ms)
	s.mu.Lock()
	delete(s.mwake, id)
	s.mu.Unlock()
	head := map[string]string{MissionDone: "Mission done: ", MissionStopped: "Mission stopped: ", MissionFailed: "Mission failed: "}[state]
	s.MissionThreadMessage(ms, ThreadMessage{Handle: ms.Lead, Text: head + clipText(ms.Goal, 200) + "\n\n" + ms.Result})
	ms, _ = s.Missions.Get(id)
	return ms
}

// missionProgress says where a mission got to, for its end.
func missionProgress(ms MissionRun) string {
	var done, working, open []string
	for _, t := range ms.Tasks {
		h := t.ID + " (@" + t.Handle + ")"
		switch t.State {
		case TaskDone:
			done = append(done, h)
		case TaskSent, TaskWorking:
			working = append(working, h)
		case TaskWaiting:
			open = append(open, h)
		}
	}
	var out []string
	if len(done) > 0 {
		out = append(out, "Done: "+strings.Join(done, ", ")+".")
	}
	if len(working) > 0 {
		out = append(out, "Still at work: "+strings.Join(working, ", ")+".")
	}
	if len(open) > 0 {
		out = append(out, "Not started: "+strings.Join(open, ", ")+".")
	}
	if len(out) == 0 {
		return "No task had started."
	}
	return strings.Join(out, " ")
}

// ---- dispatch ----

// dispatchMission sends every task whose dependencies are done, while the mission runs.
// A dispatch asked for while one runs (an order it sends changes the mission) runs again
// after it, never inside it.
func (s *Service) dispatchMission(id string) {
	s.mu.Lock()
	w := s.wakeStateLocked(id)
	if w.dispatching {
		w.again = true
		s.mu.Unlock()
		return
	}
	w.dispatching = true
	s.mu.Unlock()
	for {
		s.dispatchOnce(id)
		s.mu.Lock()
		if !w.again {
			w.dispatching = false
			s.mu.Unlock()
			return
		}
		w.again = false
		s.mu.Unlock()
	}
}

func (s *Service) dispatchOnce(id string) {
	ms, ok := s.Missions.Get(id)
	if !ok || ms.State != MissionRunning {
		return
	}
	var ready []MissionTask
	for _, t := range ms.Tasks {
		if t.State != TaskWaiting {
			continue
		}
		if depsDone(ms.Tasks, t) {
			ready = append(ready, t)
			continue
		}
		// a cancelled task is no work done: what waits for it waits until the lead decides
		for _, d := range t.DependsOn {
			if dep, ok := ms.Task(d); ok && dep.State == TaskCancelled {
				s.mu.Lock()
				w := s.wakeStateLocked(id)
				said := w.unresolved[t.ID+"<"+d]
				w.unresolved[t.ID+"<"+d] = true
				s.mu.Unlock()
				if !said {
					s.MissionEvent(id, t.ID+" waits for "+d+", which was cancelled: give "+t.ID+" other dependsOn, or cancel it")
				}
			}
		}
	}
	if len(ready) == 0 {
		s.maybeAllDone(ms)
		return
	}
	sc, err := s.newScope(ms.WorkspaceID, ms.CardID)
	if err != nil {
		s.endMission(id, MissionFailed, "Its board or orchestrator card is gone. "+missionProgress(ms))
		return
	}
	for _, t := range ready {
		sid := t.SessionID
		if in, ok := sc.byID[sid]; !ok || !isAgent(in) {
			sid = ""
			if r := sc.resolve([]string{t.Handle}); len(r.targets) > 0 {
				sid = r.targets[0]
			}
		}
		if sid == "" {
			s.mu.Lock()
			w := s.wakeStateLocked(id)
			said := w.unresolved[t.ID]
			w.unresolved[t.ID] = true
			s.mu.Unlock()
			if !said {
				s.MissionEvent(id, "task "+t.ID+" cannot go out: no console @"+t.Handle+" here")
			}
			continue
		}
		// claimed first, from the mission as it is now: a pause, a stop or the lead's cancel
		// that came after the list above wins over it
		cur, claimed := s.UpdateMission(id, func(m *MissionRun) bool {
			if m.State != MissionRunning {
				return false
			}
			i := slices.IndexFunc(m.Tasks, func(x MissionTask) bool { return x.ID == t.ID })
			if i < 0 || m.Tasks[i].State != TaskWaiting || !depsDone(m.Tasks, m.Tasks[i]) {
				return false
			}
			x := &m.Tasks[i]
			x.State, x.SessionID, x.Handle, x.OrderID, x.UpdatedAt = TaskSent, sid, sc.handle(sid), "", s.Now().UTC()
			return true
		})
		if !claimed {
			continue
		}
		task, _ := cur.Task(t.ID)
		o := s.sendOrder(s.missionCtx(cur), []string{sid}, s.missionOrderText(cur, task, sid, sc), "order", 0)
		cancel := false
		s.UpdateMission(id, func(m *MissionRun) bool {
			i := slices.IndexFunc(m.Tasks, func(x MissionTask) bool { return x.ID == t.ID })
			if i < 0 || m.Tasks[i].OrderID != "" || m.Tasks[i].State != TaskSent {
				cancel = true // the lead cancelled it meanwhile: it follows no order
				return false
			}
			m.Tasks[i].OrderID = o.ID
			cancel = m.State == MissionPaused || m.Ended()
			return true
		})
		if cancel {
			_, _ = s.CancelOrder(o.ID) // what was not typed yet; the task follows the order
		}
		if x, ok := s.Orders.Get(o.ID); ok {
			s.missionOrderChanged(x)
		}
	}
}

// depsDone reports whether the tasks a task depends on are done. A cancelled one is not:
// its work never happened.
func depsDone(tasks []MissionTask, t MissionTask) bool {
	for _, d := range t.DependsOn {
		i := slices.IndexFunc(tasks, func(x MissionTask) bool { return x.ID == d })
		if i >= 0 && tasks[i].State != TaskDone {
			return false
		}
	}
	return true
}

// maybeAllDone wakes the lead once every task has ended, to sum the mission up.
func (s *Service) maybeAllDone(ms MissionRun) {
	if len(ms.Tasks) == 0 {
		return
	}
	for _, t := range ms.Tasks {
		if t.State == TaskWaiting || t.State == TaskSent || t.State == TaskWorking {
			return
		}
	}
	sig := missionSig(ms)
	s.mu.Lock()
	w := s.wakeStateLocked(ms.ID)
	said := w.endedSig == sig
	w.endedSig = sig
	s.mu.Unlock()
	if !said {
		s.MissionEvent(ms.ID, "every task has ended")
	}
}

// missionOrderText is a task as its console reads it: the task, then what the hub says
// about the mission, the team and the team tools.
func (s *Service) missionOrderText(ms MissionRun, t MissionTask, sid string, sc *scope) string {
	var b strings.Builder
	b.WriteString(t.Text)
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "This is task %s (%s) of a mission led by %s. Goal: %s\n", t.ID, clipText(t.Title, 120), ms.Lead, clipText(ms.Goal, 400))
	if ms.DoneWhen != "" {
		fmt.Fprintf(&b, "Done when: %s\n", clipText(ms.DoneWhen, 300))
	}
	fmt.Fprintf(&b, "You are @%s%s.", sc.handle(sid), memberRole(sc, sid))
	var team []string
	seen := map[string]bool{sid: true}
	for _, x := range ms.Tasks {
		if x.SessionID == "" || seen[x.SessionID] || x.State == TaskCancelled {
			continue
		}
		seen[x.SessionID] = true
		team = append(team, "@"+x.Handle+memberRole(sc, x.SessionID))
	}
	if len(team) > 0 {
		fmt.Fprintf(&b, " Your team: %s.", strings.Join(team, ", "))
	}
	b.WriteString("\nWork with the team through the NextBoard tools: mission_read shows the goal, your tasks, the team and the shared brief")
	if ms.DocRev > 0 {
		fmt.Fprintf(&b, " (rev %d)", ms.DocRev)
	}
	b.WriteString("; team_ask {to, question} asks a teammate by handle, or \"lead\"; the answer comes later as a message, so go on or end your turn meanwhile; team_reply {question, answer} answers a question asked of you; team_update tells the team about a change; mission_doc edits the shared brief. When your task is done or you are blocked, call mission_report {status, summary}.")
	return b.String()
}

// memberRole is " (frontend, part web)" from a console's group role and part.
func memberRole(sc *scope, sid string) string {
	var bits []string
	if g := sc.groupOf(sid); g != nil {
		if r := GroupOf(g).Role; r != "" && r != "general" {
			bits = append(bits, r)
		}
	}
	if in, ok := sc.byID[sid]; ok && len(sc.proj.Parts) > 1 {
		if pt, ok := sc.proj.PartByID(in.PartID); ok {
			bits = append(bits, "part "+pt.Name)
		} else if in.Root {
			bits = append(bits, "project root")
		}
	}
	if len(bits) == 0 {
		return ""
	}
	return " (" + strings.Join(bits, ", ") + ")"
}

// ---- following the orders (hooks of track.go) ----

// missionTaskState maps an order target's state to a task's, with a result for a failure.
func missionTaskState(t OrderTarget) (state, result string, failed bool) {
	switch t.State {
	case TQueued, TDelivered:
		return TaskSent, "", false
	case TWorking, TNeedsYou:
		return TaskWorking, "", false
	case TFinished:
		return TaskDone, "", false
	case TFailed, TSkipped:
		return TaskFailed, firstNonEmpty(t.Error, "failed"), true
	case TTimeout:
		return TaskFailed, "no reply in time", true
	case TCancelled:
		return TaskCancelled, "", false
	}
	return "", "", false
}

// missionOrderChanged moves the task an order carries with its target.
func (s *Service) missionOrderChanged(o Order) {
	if o.MissionID == "" {
		return
	}
	ms, ok := s.Missions.Get(o.MissionID)
	if !ok {
		return
	}
	i := slices.IndexFunc(ms.Tasks, func(t MissionTask) bool { return t.OrderID == o.ID })
	if i < 0 {
		return
	}
	task := ms.Tasks[i]
	if task.State == TaskCancelled {
		return // cancelled: what its order still does is no longer the task's
	}
	ti := slices.IndexFunc(o.Targets, func(t OrderTarget) bool { return t.SessionID == task.SessionID })
	if ti < 0 {
		ti = 0
	}
	if len(o.Targets) == 0 {
		return
	}
	state, result, failed := missionTaskState(o.Targets[ti])
	if state == "" {
		return
	}
	if state == TaskCancelled && ms.State == MissionPaused {
		state = TaskWaiting // sent again on resume
	}
	if state == TaskCancelled && !ms.Ended() && ms.State != MissionPaused {
		failed = true
		result = "the order was cancelled"
	}
	changed := false
	s.UpdateMissionTask(ms.ID, task.ID, func(t *MissionTask) bool {
		if t.OrderID != o.ID || t.State == state || t.State == TaskCancelled {
			return false
		}
		// what mission_report said stays: the turn's end only confirms it
		if t.State == TaskDone && t.ResultBy == "report" {
			return false
		}
		if t.State == TaskDone && state != TaskDone {
			return false
		}
		t.State = state
		if state == TaskWaiting {
			t.OrderID = ""
		}
		if result != "" {
			t.Result, t.ResultBy = clipLen(result, maxTaskResult), "reply"
		}
		changed = true
		return true
	})
	if changed && failed {
		s.MissionEvent(ms.ID, "@"+task.Handle+" failed "+task.ID+": "+result)
	}
}

// missionReplied keeps a finished task's reply as its result (unless mission_report gave
// one) and wakes the lead. first: the report's first message, not its later fill-in.
func (s *Service) missionReplied(o Order, t OrderTarget, m ThreadMessage, first bool) {
	if o.MissionID == "" {
		return
	}
	ms, ok := s.Missions.Get(o.MissionID)
	if !ok {
		return
	}
	i := slices.IndexFunc(ms.Tasks, func(x MissionTask) bool { return x.OrderID == o.ID })
	if i < 0 {
		return
	}
	task := ms.Tasks[i]
	if task.ResultBy == "report" || task.State == TaskCancelled || ms.Ended() {
		return // mission_report already woke the lead, or the reply no longer counts
	}
	text := ""
	if m.Report != nil && m.Report.Chars > 0 {
		text = clipLen(strings.TrimSpace(m.Text), maxTaskResult)
	}
	if text != "" {
		s.UpdateMissionTask(ms.ID, task.ID, func(x *MissionTask) bool {
			if x.ResultBy == "report" || x.Result == text {
				return false
			}
			x.Result, x.ResultBy = text, "reply"
			return true
		})
	}
	if first {
		s.MissionEvent(ms.ID, "@"+t.Handle+" finished "+task.ID+" ("+clipText(task.Title, 80)+")")
	}
}

// missionSessionGone: a console of a team was deleted. A mission none of whose consoles is
// left has failed.
func (s *Service) missionSessionGone(sid string) {
	for _, ms := range s.Missions.List(func(m MissionRun) bool { return m.Live() && m.Member(sid) }) {
		left := false
		for _, t := range ms.Tasks {
			if t.SessionID == "" || t.SessionID == sid {
				continue
			}
			if _, ok := s.hub.SessionInfo(t.SessionID); ok {
				left = true
				break
			}
		}
		if !left {
			s.endMission(ms.ID, MissionFailed, "No console of the team is left. "+missionProgress(ms))
		}
	}
}

// missionSend turns a send of the lead to the team's consoles into tasks of the mission it
// runs, so the hub sends and follows them like the others, and returns the targets left for
// a plain order. Only while the mission runs (or waits for the owner); a console outside the
// team joins it only when the owner agreed to that (c.teamOK).
func (s *Service) missionSend(c *execCtx, st Step) []string {
	ms, ok := s.Missions.Get(c.missionID)
	// waiting for the owner by the same turn (its question, a held step): still the mission's
	// work, sent when it runs again. Paused: the owner stopped it, so a plain order.
	if !ok || (ms.State != MissionRunning && ms.State != MissionWaiting) {
		return st.Targets
	}
	sc, err := s.newScope(ms.WorkspaceID, ms.CardID)
	if err != nil {
		return st.Targets
	}
	var specs []MissionTaskSpec
	var rest []string
	for _, sid := range uniq(st.Targets) {
		if !c.teamOK && !ms.Member(sid) {
			rest = append(rest, sid)
			continue
		}
		sp := MissionTaskSpec{Title: st.Message, Text: st.Text, DependsOn: st.Sources, sessionID: sid, Handle: sc.handle(sid)}
		if len(st.Targets) == 1 {
			sp.ID = st.Name
		}
		specs = append(specs, sp)
	}
	if len(specs) == 0 {
		return rest
	}
	var notes, cancel []string
	ms, ok = s.UpdateMission(ms.ID, func(m *MissionRun) bool {
		tasks, n, err := mergeTasks(sc, m.Tasks, specs, false, s.Now().UTC())
		notes = n
		if err != nil {
			return false
		}
		cancel = cancelledOrders(m.Tasks, tasks)
		m.Tasks = tasks
		return true
	})
	s.cancelOrders(cancel)
	for _, n := range notes {
		s.note(c, n)
	}
	if ok {
		s.dispatchMission(ms.ID)
	}
	return rest
}

// ---- the lead's turns ----

// missionsTick runs the mission timers: the deadline, questions nobody answered, and the
// lead's wakes that are due.
func (s *Service) missionsTick(now time.Time) {
	for _, ms := range s.Missions.List(func(m MissionRun) bool { return m.State == MissionRunning }) {
		// the owner's own question in the thread stays theirs: the mission neither asks nor
		// thinks over it, and tries again once it is answered
		foreign := s.foreignQuestion(ms)
		if !ms.Deadline.IsZero() && !now.Before(ms.Deadline) {
			if !foreign {
				s.limitReached(ms)
			}
			continue
		}
		for _, q := range ms.Questions {
			if q.Open() && !q.Nudged && now.Sub(q.At) >= missionQuestionWait {
				s.UpdateMission(ms.ID, func(m *MissionRun) bool {
					for i := range m.Questions {
						if m.Questions[i].ID == q.ID {
							m.Questions[i].Nudged = true
							return true
						}
					}
					return false
				})
				who := "you"
				if !IsMissionLead(ms, q.To) {
					who = "@" + q.To
				}
				s.MissionEvent(ms.ID, fmt.Sprintf("@%s's question %s to %s has no answer after 10 min: %s", q.From, q.ID, who, clipText(q.Text, 160)))
			}
		}
		s.mu.Lock()
		w := s.mwake[ms.ID]
		due := w != nil && !w.busy && !w.due.IsZero() && !now.Before(w.due)
		if tq := s.thread[ms.ThreadID]; tq != nil && (tq.busy || len(tq.queue) > 0) {
			due = false // the owner's turn first; the wake follows it
		}
		s.mu.Unlock()
		if due && !foreign {
			s.missionRound(ms, now)
		}
	}
}

// limitReached holds a mission at its rounds or hours for the owner.
func (s *Service) limitReached(ms MissionRun) {
	set := s.hub.Settings().Lead
	prompt := fmt.Sprintf("Limit reached (%d rounds / %d h): continue?", missionRounds(set.MissionRounds), int(missionHours(set.MissionHours)/time.Hour))
	s.askToGoOn(ms, WaitLimit, prompt)
}

// askToGoOn puts a mission in waiting_owner (kind: WaitLimit, WaitNoProgress) with an
// approve/cancel question.
func (s *Service) askToGoOn(ms MissionRun, kind, prompt string) {
	if s.foreignQuestion(ms) {
		return // asked once the owner's own question is answered
	}
	c := s.missionCtx(ms)
	c.missionID = ""
	cf := &Confirmation{Kind: ConfMission, MissionID: ms.ID, Prompt: prompt,
		Options: []Option{{ID: "approve", Label: "Continue"}, {ID: "cancel", Label: "Stop the mission"}}}
	ms, ok := s.UpdateMission(ms.ID, func(m *MissionRun) bool {
		if m.State != MissionRunning {
			return false
		}
		m.State, m.Waiting, m.WaitKind = MissionWaiting, prompt, kind
		return true
	})
	if !ok {
		return
	}
	s.MissionThreadMessage(ms, ThreadMessage{Text: "Waiting for you: " + prompt})
	s.addConfirm(c, cf)
	s.UpdateMission(ms.ID, func(m *MissionRun) bool { m.ConfirmationID = cf.ID; return m.State == MissionWaiting })
}

// foreignQuestion reports a question open in the mission's thread that is not the mission's
// own (a kill the owner typed, another plan): the thread has one open question, so raising
// the mission's would set the owner's aside and the owner's next "yes" would answer the lead.
func (s *Service) foreignQuestion(ms MissionRun) bool {
	cf, ok := s.Confirms.Pending(ms.ThreadID, s.Now())
	return ok && cf.MissionID != ms.ID
}

// missionSig is what counts as progress between two wakes: the tasks' states, the brief's
// revision, the questions answered.
func missionSig(ms MissionRun) string {
	var b strings.Builder
	for _, t := range ms.Tasks {
		b.WriteString(t.ID + ":" + t.State + ",")
	}
	n := 0
	for _, q := range ms.Questions {
		if !q.Open() {
			n++
		}
	}
	fmt.Fprintf(&b, "doc%d,answered%d", ms.DocRev, n)
	return b.String()
}

// missionRound wakes the lead for one round: one planner turn with the mission in its input.
func (s *Service) missionRound(ms MissionRun, now time.Time) {
	if ms.Rounds >= ms.MaxRounds {
		s.limitReached(ms)
		return
	}
	if ok, reason := s.PlannerAvailable(""); !ok {
		s.mu.Lock()
		w := s.wakeStateLocked(ms.ID)
		said := w.unavailable
		w.unavailable, w.due = true, now.Add(missionRetry)
		s.mu.Unlock()
		if !said {
			s.MissionThreadMessage(ms, ThreadMessage{Text: "The lead cannot think right now (" + reason + "); the mission waits for it. Tasks that are ready still go out."})
		}
		return
	}
	sig := missionSig(ms)
	s.mu.Lock()
	w := s.wakeStateLocked(ms.ID)
	idle := 0
	if sig == w.sig {
		idle = w.idle + 1
	}
	s.mu.Unlock()
	if idle >= missionStuckWakes {
		s.mu.Lock()
		w.idle = 0
		s.mu.Unlock()
		s.askToGoOn(ms, WaitNoProgress, fmt.Sprintf("No progress in the last %d rounds: continue?", missionStuckWakes))
		return
	}
	sc, err := s.newScope(ms.WorkspaceID, ms.CardID)
	if err != nil {
		s.endMission(ms.ID, MissionFailed, "Its board or orchestrator card is gone. "+missionProgress(ms))
		return
	}
	s.mu.Lock()
	reasons := w.reasons
	w.busy, w.reasons, w.due = true, nil, time.Time{}
	s.mu.Unlock()
	c := s.missionCtx(ms)
	c.wake, c.wakeReasons = true, reasons
	// what woke the lead quotes the agents: it goes in mission.wokenBy, never in the order,
	// which is the owner's word alone
	ok, why := s.startPlanner(c, sc, missionWakeOrder)
	s.mu.Lock()
	if ok {
		w.sig, w.idle, w.unavailable, w.startFailed = sig, idle, false, false
		s.mu.Unlock()
		return
	}
	w.busy, w.reasons = false, append(reasons, w.reasons...)
	said := true
	if why == plannerBusy {
		w.due = now.Add(missionDebounce) // the owner's turn first; the wake follows it
	} else {
		w.due, said, w.startFailed = now.Add(missionRetry), w.startFailed, true
	}
	s.mu.Unlock()
	if !said {
		s.MissionThreadMessage(ms, ThreadMessage{Text: "The lead could not start a round: " + why})
	}
	if why != plannerBusy {
		s.drain(ms.ThreadID, c) // its claim, and the commands that queued behind it
	}
}

// missionWakeOrder is the order of a wake's lead turn.
const missionWakeOrder = "Mission wake: see mission.wokenBy"

// missionTurnStarted counts a lead turn that has a running mission in its input.
func (s *Service) missionTurnStarted(c *execCtx) {
	ms, ok := s.Missions.Active(c.threadID)
	if !ok || !ms.Live() {
		return
	}
	c.missionID = ms.ID
	if ms.State == MissionRunning {
		s.UpdateMission(ms.ID, func(m *MissionRun) bool { m.Rounds++; return true })
	}
}

// missionRoundFailed: the lead gave no answer; what woke it is asked again in a minute.
func (s *Service) missionRoundFailed(c *execCtx) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.mwake[c.missionID]; w != nil {
		w.reasons = append(slices.Clone(c.wakeReasons), w.reasons...)
		w.due = s.Now().Add(missionRetry)
	}
}

// missionTurnDone ends a lead turn of a mission: a wake gathered meanwhile is due soon.
func (s *Service) missionTurnDone(c *execCtx) {
	if !c.wake || c.missionID == "" {
		return
	}
	now := s.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.mwake[c.missionID]; w != nil {
		w.busy = false
		if len(w.reasons) > 0 && w.due.IsZero() {
			w.due = now.Add(missionDebounce)
		}
	}
}

// missionBriefFor is the mission of the thread as the lead reads it in its input.
func (s *Service) missionBriefFor(c *execCtx) map[string]any {
	ms, ok := s.Missions.Active(c.threadID)
	if !ok {
		return nil
	}
	now := s.Now()
	tasks := []map[string]any{}
	for _, t := range ms.Tasks {
		x := map[string]any{"id": t.ID, "handle": t.Handle, "title": t.Title, "state": t.State, "text": clipText(t.Text, 600)}
		if len(t.DependsOn) > 0 {
			x["dependsOn"] = t.DependsOn
		}
		if t.Result != "" {
			x["result"] = clipText(t.Result, 1200)
		}
		tasks = append(tasks, x)
	}
	team := []map[string]any{}
	for _, m := range s.TeamMessages(ms, missionTeamForLead) {
		if m.Team == nil {
			continue
		}
		x := map[string]any{"from": m.Team.From, "kind": m.Team.Kind, "text": clipText(m.Text, 600)}
		if m.Team.To != "" {
			x["to"] = m.Team.To
		}
		if m.Team.QuestionID != "" {
			x["question"] = m.Team.QuestionID
		}
		team = append(team, x)
	}
	open := []map[string]any{}
	for _, q := range ms.Questions {
		if q.Open() {
			to := q.To
			if IsMissionLead(ms, q.To) {
				to = "lead"
			}
			open = append(open, map[string]any{"id": q.ID, "from": q.From, "to": to, "text": clipText(q.Text, 600), "minutes": int(now.Sub(q.At).Minutes())})
		}
	}
	out := map[string]any{"id": ms.ID, "state": ms.State, "goal": ms.Goal, "summary": ms.Summary, "doneWhen": ms.DoneWhen,
		"doc": clipLen(ms.Doc, missionDocForLead), "docRev": ms.DocRev, "tasks": tasks, "team": team, "openQuestions": open}
	if len(ms.Doc) > missionDocForLead {
		out["docCut"] = true
	}
	if ms.Live() {
		out["roundsLeft"] = max(ms.MaxRounds-ms.Rounds, 0)
		out["minutesLeft"] = max(int(ms.Deadline.Sub(now).Minutes()), 0)
	}
	if ms.Waiting != "" {
		out["waiting"] = ms.Waiting
	}
	if c.wake {
		out["wokenBy"] = c.wakeReasons
	}
	return out
}

// missionPlan applies the "mission" of the lead's plan and returns the steps still to run.
// A mission running in the thread takes the lead's sends as its tasks; on a wake, a step
// that adds consoles to the team waits for the owner. Without one, a mission object
// proposes a new mission (or changes the one awaiting approval).
func (s *Service) missionPlan(c *execCtx, sc *scope, plan *planOutput, steps []Step) []Step {
	ms, has := s.Missions.Active(c.threadID)
	sp := plan.Mission
	if c.wake && (!has || ms.ID != c.missionID || !ms.Live()) {
		return nil // the mission that woke the lead ended meanwhile
	}
	if has && ms.Live() {
		c.missionID = ms.ID
		if c.wake && ms.State != MissionRunning {
			// paused, or held for the owner, while the lead thought: only its answers and the
			// brief still count; its steps and changes of state wait for the next round
			if sp != nil {
				s.missionNotes(ms, *sp)
			}
			return nil
		}
		if !c.wake && ms.State == MissionWaiting && ms.ConfirmationID == "" {
			// the owner answered the lead's question: the mission goes on
			ms, _ = s.missionGoOn(ms.ID, "")
		}
		var keep, held []Step
		for _, st := range steps {
			switch {
			case !c.wake || wakeStep(ms, st):
				keep = append(keep, st)
			case st.Op == "ask":
				// what it would read is in the wake's input already
			default:
				held = append(held, st)
			}
		}
		if sp != nil {
			h, gated := s.applyMissionOutput(c, sc, ms, *sp, plan.Danger)
			held, keep = append(held, h...), append(keep, gated...)
			if now, ok := s.Missions.Get(ms.ID); !ok || now.Ended() {
				// done (or ended) by this answer: its sends would go out untracked after the end
				keep = slices.DeleteFunc(keep, func(st Step) bool { return st.Op == "send" })
				held = nil
			}
		}
		if len(held) > 0 {
			s.holdForOwner(c, sc, ms.ID, held)
		}
		return keep
	}
	if sp == nil || strings.TrimSpace(sp.Goal) == "" || len(sp.Tasks) == 0 {
		return steps
	}
	if has && ms.State != MissionAwaiting {
		return steps
	}
	// the tasks' work goes out with the mission, not before it
	members := map[string]bool{}
	for _, t := range sp.Tasks {
		if r := sc.resolve([]string{strings.TrimPrefix(t.Handle, "@")}); len(r.targets) > 0 {
			members[r.targets[0]] = true
		}
	}
	var keep []Step
	for _, st := range steps {
		if st.Op == "send" && !slices.ContainsFunc(st.Targets, func(sid string) bool { return !members[sid] }) {
			continue
		}
		keep = append(keep, st)
	}
	_, notes, err := s.proposeMission(c, sc, *sp, false)
	for _, n := range notes {
		s.note(c, n)
	}
	if err != nil {
		s.note(c, "The mission could not be proposed: "+err.Error())
		return steps
	}
	return keep
}

// applyMissionOutput carries out the lead's "mission" on a running mission: the brief,
// tasks, answers, then done or a question to the owner. New work for a console outside
// the team comes back as sends the owner must agree to first (held); new work that is
// risky, or a plan with "danger", as sends the policy holds (gated) like any other.
func (s *Service) applyMissionOutput(c *execCtx, sc *scope, ms MissionRun, sp MissionSpec, danger string) (held, gated []Step) {
	now := s.Now().UTC()
	ms = s.missionBrief(ms, sp)
	if len(sp.Tasks) > 0 {
		var specs []MissionTaskSpec
		extra := s.hub.Settings().Commands.RiskyPatterns
		for _, t := range sp.Tasks {
			id := strings.TrimSpace(t.ID)
			old, known := ms.Task(id)
			if t.State == TaskCancelled {
				specs = append(specs, t)
				continue
			}
			text := strings.TrimSpace(t.Text)
			newWork := text != "" && (!known || text != old.Text)
			handle := strings.TrimPrefix(firstNonEmpty(strings.TrimSpace(t.Handle), old.Handle), "@")
			renamed := !known || Norm(handle) != Norm(old.Handle)
			sid := ""
			if !renamed && old.SessionID != "" {
				sid = old.SessionID
			} else if r := sc.resolve([]string{handle}); len(r.targets) > 0 {
				sid = r.targets[0]
			}
			// the approved team is checked whenever a task gets work or another console
			if newWork || sid != old.SessionID {
				if sid == "" {
					if c.wake && renamed {
						s.note(c, "Task "+firstNonEmpty(id, "without an id")+" names no console of the team (@"+handle+"); left out.")
						continue
					}
					specs = append(specs, t) // the owner's turn: found by name when it goes out
					continue
				}
				st := Step{Op: "send", Targets: []string{sid}, Text: firstNonEmpty(text, old.Text), Mode: "rewritten", Planned: true,
					Name: id, Message: t.Title, Sources: t.DependsOn}
				switch {
				case c.wake && !ms.Member(sid):
					held = append(held, st)
					continue
				case newWork && (strings.TrimSpace(danger) != "" || riskyMatch(text, extra) != ""):
					gated = append(gated, st)
					continue
				}
			}
			specs = append(specs, t)
		}
		var notes, cancel []string
		if ms2, ok := s.UpdateMission(ms.ID, func(m *MissionRun) bool {
			tasks, n, err := mergeTasks(sc, m.Tasks, specs, false, now)
			notes = n
			if err != nil || len(specs) == 0 {
				return false
			}
			cancel = cancelledOrders(m.Tasks, tasks)
			m.Tasks = tasks
			return true
		}); ok {
			ms = ms2
		}
		s.cancelOrders(cancel)
		for _, n := range notes {
			s.note(c, n)
		}
	}
	for _, a := range sp.Answers {
		s.leadAnswers(ms, a)
	}
	if ms2, ok := s.Missions.Get(ms.ID); ok {
		ms = ms2
	}
	if ms.State != MissionRunning && (ms.State != MissionWaiting || ms.ConfirmationID != "") {
		return held, gated // paused or held meanwhile: done and questions wait for the next round
	}
	switch {
	case sp.Done != nil && strings.TrimSpace(sp.Done.Summary) != "":
		s.endMission(ms.ID, MissionDone, strings.TrimSpace(sp.Done.Summary))
		return nil, nil
	case sp.AskOwner != nil && strings.TrimSpace(sp.AskOwner.Question) != "":
		q := clipLen(strings.TrimSpace(sp.AskOwner.Question), maxMissionSummary)
		if ms2, ok := s.UpdateMission(ms.ID, func(m *MissionRun) bool {
			if m.State != MissionRunning && (m.State != MissionWaiting || m.ConfirmationID != "") {
				return false
			}
			m.State, m.Waiting, m.WaitKind, m.ConfirmationID = MissionWaiting, q, WaitQuestion, ""
			return true
		}); ok {
			s.MissionThreadMessage(ms2, ThreadMessage{Handle: ms2.Lead, Text: q})
		}
	}
	s.dispatchMission(ms.ID)
	return held, gated
}

// wakeStep: what a wake's lead turn may do by itself (§22.4): send to the team's consoles,
// change task boards, move the owner's view, reply. The rest waits for the owner.
func wakeStep(ms MissionRun, st Step) bool {
	switch st.Op {
	case "send":
		return len(st.Targets) > 0 && !slices.ContainsFunc(st.Targets, func(sid string) bool { return !ms.Member(sid) })
	case "reply", "task.add", "task.update", "task.delete", "ui":
		return true
	}
	return false
}

// missionBrief applies the lead's new brief.
func (s *Service) missionBrief(ms MissionRun, sp MissionSpec) MissionRun {
	if sp.Doc == nil || strings.TrimSpace(*sp.Doc) == strings.TrimSpace(ms.Doc) {
		return ms
	}
	doc := clipBytes(strings.TrimSpace(*sp.Doc), maxMissionDoc)
	ms2, ok := s.UpdateMission(ms.ID, func(m *MissionRun) bool { m.Doc, m.DocRev = doc, m.DocRev+1; return true })
	if !ok {
		return ms
	}
	s.MissionThreadMessage(ms2, ThreadMessage{Role: "hub", Handle: ms2.Lead, Text: "updated the brief (rev " + strconv.Itoa(ms2.DocRev) + ")",
		Team: &TeamRef{From: ms2.Lead, Kind: "doc"}})
	return ms2
}

// missionNotes keeps what a wake that came too late still says: its brief and answers.
func (s *Service) missionNotes(ms MissionRun, sp MissionSpec) {
	ms = s.missionBrief(ms, sp)
	for _, a := range sp.Answers {
		s.leadAnswers(ms, a)
	}
}

// leadAnswers gives the lead's answer to a question asked of it back to the asker.
func (s *Service) leadAnswers(ms MissionRun, a MissionAnswer) {
	answer := clipLen(strings.TrimSpace(a.Answer), maxMissionAnswer)
	q, ok := ms.Question(strings.TrimSpace(a.Question))
	if !ok || !q.Open() || answer == "" {
		return
	}
	now := s.Now().UTC()
	if _, ok := s.UpdateMission(ms.ID, func(m *MissionRun) bool {
		for i := range m.Questions {
			if m.Questions[i].ID == q.ID && m.Questions[i].Open() {
				m.Questions[i].Answer, m.Questions[i].AnsweredAt = answer, &now
				return true
			}
		}
		return false
	}); !ok {
		return
	}
	s.MissionThreadMessage(ms, ThreadMessage{Role: "hub", Handle: ms.Lead, Text: answer, Team: &TeamRef{From: ms.Lead, To: q.From, Kind: "reply", QuestionID: q.ID}})
	if q.FromID != "" {
		s.teamSend(ms.ID, q.FromID, "@"+ms.Lead+" answers your question "+q.ID+": "+answer)
	}
}

// holdForOwner asks the owner before steps of a wake that go beyond the approved plan run:
// work for consoles outside the team, and every step a wake may not take by itself.
func (s *Service) holdForOwner(c *execCtx, sc *scope, id string, steps []Step) {
	ms, ok := s.Missions.Get(id)
	if !ok || !ms.Live() {
		return
	}
	if ms.State != MissionRunning {
		s.note(c, "Not done while the mission "+map[bool]string{true: "waits for you", false: "is paused"}[ms.State == MissionWaiting]+": "+planText(describePlan(sc, steps))+".")
		return
	}
	if s.foreignQuestion(ms) {
		what := planText(describePlan(sc, steps))
		s.note(c, "Not done yet: answer the open question in the thread first ("+clipText(what, 160)+").")
		s.MissionEvent(id, "not done, the owner had another question open: "+what)
		return
	}
	cf := &Confirmation{Kind: ConfMission, MissionID: id, Prompt: "The lead wants to go beyond the plan: " + planText(describePlan(sc, steps)) + ". Agree?",
		Plan: describePlan(sc, steps), Steps: steps, Options: []Option{{ID: "approve", Label: "Agree"}, {ID: "cancel", Label: "No"}}}
	ms, ok = s.UpdateMission(id, func(m *MissionRun) bool {
		if m.State != MissionRunning {
			return false // paused or ended meanwhile
		}
		m.State, m.Waiting, m.WaitKind = MissionWaiting, clipText(cf.Prompt, 300), WaitHeld
		return true
	})
	if !ok {
		return
	}
	cc := *c
	cc.missionID = ""
	s.addConfirm(&cc, cf)
	s.UpdateMission(id, func(m *MissionRun) bool { m.ConfirmationID = cf.ID; return m.State == MissionWaiting })
	s.MissionThreadMessage(ms, ThreadMessage{Text: "Waiting for you: " + ms.Waiting})
}

// missionsAfterRestart pauses what a hub restart interrupted: a mission does not go on by
// itself after one. Tasks whose text never reached the console go out again on resume;
// tasks that were at work are failed, for the lead to decide.
func (s *Service) missionsAfterRestart(now time.Time) {
	for _, ms := range s.Missions.List(func(m MissionRun) bool { return m.Live() }) {
		wasPaused := ms.State == MissionPaused
		resent := map[string]string{} // order id → session the mission sends it to again
		ms, ok := s.UpdateMission(ms.ID, func(m *MissionRun) bool {
			m.State, m.Waiting, m.ConfirmationID = MissionPaused, "", ""
			for i := range m.Tasks {
				t := &m.Tasks[i]
				if t.State != TaskSent && t.State != TaskWorking {
					continue
				}
				o, found := s.Orders.Get(t.OrderID)
				switch {
				case found && slices.ContainsFunc(o.Targets, func(x OrderTarget) bool { return x.SessionID == t.SessionID && x.Undelivered }):
					resent[o.ID] = t.SessionID
					t.State, t.OrderID = TaskWaiting, ""
				case !found || !o.Active():
					t.State, t.Result, t.ResultBy = TaskFailed, "interrupted by a hub restart", "reply"
				}
				t.UpdatedAt = now.UTC()
			}
			return true
		})
		// the mission sends those tasks again on resume, as new orders: the old ones offer
		// no "Send again", or they would go out twice
		for oid, sid := range resent {
			if o, changed := s.Orders.Mutate(oid, func(x *Order) bool {
				ch := false
				for i := range x.Targets {
					if x.Targets[i].SessionID == sid && x.Targets[i].Undelivered {
						x.Targets[i].Undelivered, x.Targets[i].Error = false, "interrupted by hub restart; the mission sends it again when it is resumed"
						ch = true
					}
				}
				return ch
			}); changed {
				s.hub.Publish(map[string]any{"type": "order", "order": o})
			}
		}
		if len(resent) > 0 {
			s.Orders.Save(now)
		}
		if ok && !wasPaused {
			s.MissionThreadMessage(ms, ThreadMessage{Text: "Paused by a hub restart. Resume?"})
			s.MissionEvent(ms.ID, "the hub restarted; tasks that were at work were interrupted")
		}
	}
}
