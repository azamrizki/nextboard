package orch

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/store"
)

// Missions (docs/API-orchestration.md §22): work the lead runs with a team of consoles
// to the end. The owner approves the plan once; the hub sends each task whose
// dependencies are done as an order, the lead is woken by what happens on the way, and
// the agents talk to each other through the hub (team tools, missionteam.go), in the
// thread where the owner sees it.

// Mission states.
const (
	MissionAwaiting = "awaiting_approval"
	MissionRunning  = "running"
	MissionWaiting  = "waiting_owner"
	MissionPaused   = "paused"
	MissionDone     = "done"
	MissionStopped  = "stopped"
	MissionFailed   = "failed"
)

// Why a mission waits for the owner (MissionRun.WaitKind, state waiting_owner).
const (
	WaitLimit      = "limit"       // its rounds or hours ran out: go on?
	WaitNoProgress = "no_progress" // rounds in a row changed nothing: go on?
	WaitQuestion   = "question"    // the lead asked the owner (askOwner)
	WaitHeld       = "held"        // a risky step, or one beyond the plan, waits for an OK
)

// Mission task states.
const (
	TaskWaiting   = "waiting"
	TaskSent      = "sent"
	TaskWorking   = "working"
	TaskDone      = "done"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
)

// Limits of a mission.
const (
	maxMissionGoal     = 2000
	maxMissionSummary  = 2000
	maxMissionDoneWhen = 1000
	maxMissionDoc      = 32 << 10
	maxMissionTasks    = 40
	maxMissionTitle    = 200
	maxMissionTaskText = 8000
	maxMissionResult   = 8000
	maxTaskResult      = 2000
	maxMissionAnswer   = 4000
	// MaxOpenQuestions: team_ask refuses more open questions than this per mission.
	MaxOpenQuestions = 20
	missionsFile     = "missions.json"
	keepMissions     = 100
	missionKeep      = 30 * 24 * time.Hour
)

// Errors of the mission API.
var (
	ErrMissionNotFound = errors.New("mission not found")
	ErrMissionActive   = errors.New("this thread already has a mission that has not ended")
	ErrMissionState    = errors.New("the mission cannot do that in its current state")
	ErrBadMission      = errors.New("bad mission")
	ErrNotInMission    = errors.New("You are not in a mission.")
)

// MissionTask is one piece of a mission, done by one console.
type MissionTask struct {
	ID        string   `json:"id"`     // "t1", "t2", … (stable within the mission)
	Handle    string   `json:"handle"` // the console that does it
	SessionID string   `json:"sessionId,omitempty"`
	Title     string   `json:"title"`
	Text      string   `json:"text"`
	DependsOn []string `json:"dependsOn,omitempty"`
	State     string   `json:"state"`
	OrderID   string   `json:"orderId,omitempty"`
	// Result: mission_report's summary, else the reply excerpt.
	Result string `json:"result,omitempty"`
	// ResultBy says where Result came from: "report" (mission_report) wins over "reply".
	ResultBy  string    `json:"resultBy,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MissionQuestion is a team_ask: open until team_reply (or the lead) answers it.
type MissionQuestion struct {
	ID         string     `json:"id"`   // "q_…"
	From       string     `json:"from"` // the asker's handle
	FromID     string     `json:"fromId,omitempty"`
	To         string     `json:"to"` // a teammate's handle or "lead"
	ToID       string     `json:"toId,omitempty"`
	Text       string     `json:"text"`
	At         time.Time  `json:"at"`
	Answer     string     `json:"answer,omitempty"`
	AnsweredAt *time.Time `json:"answeredAt,omitempty"`
	// Nudged: the lead was woken because nobody answered in time.
	Nudged bool `json:"nudged,omitempty"`
}

// Open reports whether the question still waits for an answer.
func (q MissionQuestion) Open() bool { return q.AnsweredAt == nil }

// MissionRun is one mission.
type MissionRun struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"projectId"`
	ThreadID    string            `json:"threadId"`
	WorkspaceID string            `json:"workspaceId"`
	CardID      string            `json:"cardId,omitempty"`
	Lead        string            `json:"lead"`
	Goal        string            `json:"goal"`
	Summary     string            `json:"summary"`
	DoneWhen    string            `json:"doneWhen"`
	Doc         string            `json:"doc"`
	DocRev      int               `json:"docRev"`
	Tasks       []MissionTask     `json:"tasks"`
	Questions   []MissionQuestion `json:"questions,omitempty"`
	State       string            `json:"state"`
	Waiting     string            `json:"waiting,omitempty"`
	// WaitKind says why it waits for the owner (Wait…); "" in any other state.
	WaitKind       string     `json:"waitKind,omitempty"`
	Rounds         int        `json:"rounds"`
	MaxRounds      int        `json:"maxRounds"`
	Deadline       time.Time  `json:"deadline"`
	Result         string     `json:"result,omitempty"`
	ConfirmationID string     `json:"confirmationId,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	ApprovedAt     *time.Time `json:"approvedAt,omitempty"`
	EndedAt        *time.Time `json:"endedAt,omitempty"`
}

// Ended reports a mission that is over (done, stopped or failed).
func (m MissionRun) Ended() bool {
	switch m.State {
	case MissionDone, MissionStopped, MissionFailed:
		return true
	}
	return false
}

// Live reports a mission whose team works or may go on: approved and not ended.
func (m MissionRun) Live() bool {
	return !m.Ended() && m.State != MissionAwaiting
}

// Task returns a task by id.
func (m MissionRun) Task(id string) (MissionTask, bool) {
	for _, t := range m.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return MissionTask{}, false
}

// Question returns a question by id.
func (m MissionRun) Question(id string) (MissionQuestion, bool) {
	for _, q := range m.Questions {
		if q.ID == id {
			return q, true
		}
	}
	return MissionQuestion{}, false
}

// OpenQuestions counts the questions nobody answered yet.
func (m MissionRun) OpenQuestions() int {
	n := 0
	for _, q := range m.Questions {
		if q.Open() {
			n++
		}
	}
	return n
}

// Member reports whether a console has a task in the mission.
func (m MissionRun) Member(sessionID string) bool {
	return slices.ContainsFunc(m.Tasks, func(t MissionTask) bool { return t.SessionID == sessionID && sessionID != "" })
}

// TaskOf is the task a console works on now: the one sent to it or in work, else its
// latest done or failed one, else (nothing of it went out yet) its latest waiting one. A
// waiting task never went out: mission_report cannot finish it (CompleteMissionTask).
func (m MissionRun) TaskOf(sessionID string) (MissionTask, bool) {
	var ended, waiting *MissionTask
	later := func(a, b *MissionTask) bool { return a == nil || !b.UpdatedAt.Before(a.UpdatedAt) }
	for i := range m.Tasks {
		t := &m.Tasks[i]
		if t.SessionID != sessionID || sessionID == "" {
			continue
		}
		switch t.State {
		case TaskSent, TaskWorking:
			return *t, true
		case TaskDone, TaskFailed:
			if later(ended, t) {
				ended = t
			}
		case TaskWaiting:
			if later(waiting, t) {
				waiting = t
			}
		}
	}
	switch {
	case ended != nil:
		return *ended, true
	case waiting != nil:
		return *waiting, true
	}
	return MissionTask{}, false
}

func cloneMission(x *MissionRun) MissionRun {
	c := *x
	c.Tasks = slices.Clone(x.Tasks)
	for i := range c.Tasks {
		c.Tasks[i].DependsOn = slices.Clone(c.Tasks[i].DependsOn)
	}
	c.Questions = slices.Clone(x.Questions)
	return c
}

// Missions keeps missions in memory and missions.json.
type Missions struct {
	st  *store.Store
	mu  sync.Mutex
	all map[string]*MissionRun
}

func newMissions(st *store.Store) *Missions {
	m := &Missions{st: st, all: map[string]*MissionRun{}}
	var list []*MissionRun
	if _, err := st.Load(missionsFile, &list); err == nil {
		for _, x := range list {
			if x != nil && x.ID != "" {
				if x.Tasks == nil {
					x.Tasks = []MissionTask{}
				}
				m.all[x.ID] = x
			}
		}
	}
	return m
}

// Get returns a copy of a mission.
func (m *Missions) Get(id string) (MissionRun, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	x, ok := m.all[id]
	if !ok {
		return MissionRun{}, false
	}
	return cloneMission(x), true
}

// List returns the missions keep accepts (nil = all), newest first.
func (m *Missions) List(keep func(MissionRun) bool) []MissionRun {
	m.mu.Lock()
	out := []MissionRun{}
	for _, x := range m.all {
		if keep == nil || keep(*x) {
			out = append(out, cloneMission(x))
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Active returns the thread's mission that has not ended, if any.
func (m *Missions) Active(threadID string) (MissionRun, bool) {
	l := m.List(func(x MissionRun) bool { return x.ThreadID == threadID && !x.Ended() })
	if len(l) == 0 {
		return MissionRun{}, false
	}
	return l[0], true
}

// put stores a new mission (and saves).
func (m *Missions) put(x MissionRun, now time.Time) {
	m.mu.Lock()
	c := cloneMission(&x)
	m.all[x.ID] = &c
	m.mu.Unlock()
	m.save(now)
}

// mutate changes a mission under the lock; f reports whether it changed. A change is
// saved at once.
func (m *Missions) mutate(id string, now time.Time, f func(x *MissionRun) bool) (MissionRun, bool) {
	m.mu.Lock()
	x, ok := m.all[id]
	if !ok || !f(x) {
		m.mu.Unlock()
		return MissionRun{}, false
	}
	if x.State != MissionWaiting {
		x.WaitKind = "" // it waits for nothing once it left waiting_owner
	}
	x.UpdatedAt = now.UTC()
	out := cloneMission(x)
	m.mu.Unlock()
	m.save(now)
	return out, true
}

// DeleteProject forgets a project's missions (a project deleted for good).
func (m *Missions) DeleteProject(projectID string, now time.Time) int {
	m.mu.Lock()
	n := 0
	for id, x := range m.all {
		if x.ProjectID == projectID {
			delete(m.all, id)
			n++
		}
	}
	m.mu.Unlock()
	if n > 0 {
		m.save(now)
	}
	return n
}

// save writes every mission not ended, and ended ones younger than 30 days (≤ 100).
func (m *Missions) save(now time.Time) {
	m.mu.Lock()
	var live, ended []*MissionRun
	for _, x := range m.all {
		switch {
		case !x.Ended():
			live = append(live, x)
		case x.EndedAt == nil || now.Sub(*x.EndedAt) < missionKeep:
			ended = append(ended, x)
		default:
			delete(m.all, x.ID)
		}
	}
	sort.Slice(ended, func(i, j int) bool { return ended[i].UpdatedAt.After(ended[j].UpdatedAt) })
	if len(ended) > keepMissions {
		for _, x := range ended[keepMissions:] {
			delete(m.all, x.ID)
		}
		ended = ended[:keepMissions]
	}
	list := make([]MissionRun, 0, len(live)+len(ended))
	for _, x := range append(live, ended...) {
		list = append(list, cloneMission(x))
	}
	m.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	_ = m.st.Save(missionsFile, list)
}

// ---- the API of the team tools (missionteam.go) ----

// MissionOf returns the mission a console is a member of (it has a task in it) and that
// is approved and not ended: running, waiting for the owner or paused. The newest wins.
func (s *Service) MissionOf(sessionID string) (MissionRun, bool) {
	if sessionID == "" {
		return MissionRun{}, false
	}
	l := s.Missions.List(func(x MissionRun) bool { return x.Live() && x.Member(sessionID) })
	if len(l) == 0 {
		return MissionRun{}, false
	}
	return l[0], true
}

// UpdateMission changes a mission under its lock (f reports whether it changed), then
// saves it and publishes {"type":"mission"}. false: no such mission or nothing changed.
func (s *Service) UpdateMission(id string, f func(m *MissionRun) bool) (MissionRun, bool) {
	ms, ok := s.Missions.mutate(id, s.Now(), f)
	if ok {
		s.publishMission(ms)
	}
	return ms, ok
}

// UpdateMissionTask changes one task of a mission (f reports whether it changed); the
// task's UpdatedAt is set. A task that moves to done, failed or cancelled lets the
// tasks that depend on it go out (while the mission runs).
func (s *Service) UpdateMissionTask(missionID, taskID string, f func(t *MissionTask) bool) (MissionRun, bool) {
	now := s.Now().UTC()
	ms, ok := s.UpdateMission(missionID, func(m *MissionRun) bool {
		for i := range m.Tasks {
			if m.Tasks[i].ID == taskID {
				if f(&m.Tasks[i]) {
					m.Tasks[i].UpdatedAt = now
					return true
				}
				return false
			}
		}
		return false
	})
	if ok {
		s.dispatchMission(ms.ID)
	}
	return ms, ok
}

// CompleteMissionTask marks a task that went out done with what the agent reported
// (mission_report done; false for a waiting or cancelled one): the summary is its Result and stays over the reply excerpt the turn's end
// brings. The tasks that wait for it go out and the lead is woken.
func (s *Service) CompleteMissionTask(missionID, taskID, summary string) (MissionRun, bool) {
	ms, ok := s.UpdateMissionTask(missionID, taskID, func(t *MissionTask) bool {
		if t.State == TaskCancelled || t.State == TaskWaiting {
			return false // it never went out: there is nothing of it to finish
		}
		t.State, t.Result, t.ResultBy = TaskDone, clipLen(strings.TrimSpace(summary), maxTaskResult), "report"
		return true
	})
	if ok {
		t, _ := ms.Task(taskID)
		s.MissionEvent(missionID, "@"+t.Handle+" reported "+taskID+" done")
	}
	return ms, ok
}

// MissionEvent wakes the lead of a mission for reason (one line, said in its input).
// Events within 5 s are one wake; while a lead turn of the mission runs, the next waits
// for it. A mission not running keeps the reasons for when it runs again.
func (s *Service) MissionEvent(id, reason string) {
	s.missionWake(id, reason, s.Now())
}

// MissionThreadMessage records a message of the mission in its thread: Kind "team" when
// m.Team is set (Role "agent" unless given), else "mission" (Role "hub"). ThreadID and
// MissionID are filled in. The message is published like every thread message.
func (s *Service) MissionThreadMessage(ms MissionRun, m ThreadMessage) ThreadMessage {
	m.ThreadID, m.MissionID = ms.ThreadID, ms.ID
	if m.Kind == "" {
		m.Kind = "mission"
		if m.Team != nil {
			m.Kind = "team"
		}
	}
	if m.Role == "" {
		m.Role = "hub"
		if m.Team != nil {
			m.Role = "agent"
		}
	}
	return s.Threads.Add(m)
}

// TeamMessages are the mission's last n team messages, oldest first (from its thread).
func (s *Service) TeamMessages(ms MissionRun, n int) []ThreadMessage {
	all := s.Threads.Last(ms.ThreadID, 200)
	var out []ThreadMessage
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		if all[i].MissionID == ms.ID && all[i].Kind == "team" {
			out = append(out, all[i])
		}
	}
	slices.Reverse(out)
	return out
}

// IsMissionLead reports whether a team_ask's "to" means the lead: "lead", "orchestrator"
// or the lead's name.
func IsMissionLead(ms MissionRun, to string) bool {
	n := Norm(strings.TrimPrefix(strings.TrimSpace(to), "@"))
	return n != "" && (n == "lead" || n == "orchestrator" || n == Norm(ms.Lead))
}

// MemberByHandle finds the session of a team member by handle ("@" optional).
func (ms MissionRun) MemberByHandle(handle string) (MissionTask, bool) {
	n := Norm(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
	for _, t := range ms.Tasks {
		if t.SessionID != "" && Norm(t.Handle) == n {
			return t, true
		}
	}
	return MissionTask{}, false
}

// NewQuestionID is the id of a team question.
func NewQuestionID() string { return auth.RandomID("q_") }

func (s *Service) publishMission(ms MissionRun) {
	s.hub.Publish(map[string]any{"type": "mission", "mission": ms})
}
