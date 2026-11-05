package orch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"nextboard/internal/agent"
	"nextboard/internal/agentstate"
	"nextboard/internal/auth"
	"nextboard/internal/board"
	"nextboard/internal/delivery"
	"nextboard/internal/jobs"
	"nextboard/internal/preview"
	"nextboard/internal/project"
	"nextboard/internal/session"
	"nextboard/internal/settings"
	"nextboard/internal/store"
)

var t0 = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

// fakeHub is an in-memory hub: real board and project stores, scripted
// consoles, deliveries and runner jobs.
type fakeHub struct {
	t        *testing.T
	mu       sync.Mutex
	st       *store.Store
	boards   *board.Manager
	projects *project.Store
	set      settings.Settings
	sessions map[string]*session.Info
	order    []string
	replies  map[string]*session.Reply
	dels     map[string]*delivery.Delivery
	delOrder []string
	delReqs  []delivery.Request
	delErr   map[string]error // sessionId → error from Deliver
	runs     []jobs.RunnerRequest
	runErr   error
	// SwitchAgent: an error to give, and whether the process stays the old one.
	switchErr        error
	switchKeepsStart bool
	calls            []string
	events           []map[string]any
	attn             []Attention
	planner          bool
	onMach           map[string]bool // "machine/agent"
	execCode         int
	execErr          error
	screen           map[string]string
	docText          string
	pageLog          []preview.LogEntry
	transcr          map[string]string
}

func newFakeHub(t *testing.T) (*fakeHub, *Service) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHub{t: t, st: st, set: settings.Defaults(), sessions: map[string]*session.Info{}, replies: map[string]*session.Reply{},
		dels: map[string]*delivery.Delivery{}, delErr: map[string]error{}, onMach: map[string]bool{"local/claude": true}, planner: true,
		screen: map[string]string{}, transcr: map[string]string{}}
	h.set.Lead.TalkFirst = false // the plain pipeline; lead_test.go turns it on
	h.boards = board.NewManager(st)
	h.boards.SessionWorkspace = func(id string) (string, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		in, ok := h.sessions[id]
		if !ok {
			return "", false
		}
		return in.WorkspaceID, true
	}
	h.boards.Validate = BoardValidator(func(name string) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, in := range h.sessions {
			if Norm(in.Handle) == Norm(name) {
				return true
			}
		}
		return false
	})
	t.Cleanup(h.boards.Close)
	if h.projects, err = project.Open(st); err != nil {
		t.Fatal(err)
	}
	svc := New(h, st)
	svc.Now = func() time.Time { return t0 }
	svc.Threads.Now = svc.Now
	svc.Loops.Loc = time.UTC
	return h, svc
}

// project creates a project with one workspace.
func (h *fakeHub) project(name string) (project.Project, string) {
	p, _, err := h.projects.Create("local", "/work/"+name, name)
	if err != nil {
		h.t.Fatal(err)
	}
	return p, p.Workspaces[0].ID
}

// console adds a running agent console with a terminal node.
func (h *fakeHub) console(p project.Project, wid, handle, agentID, state string) session.Info {
	h.mu.Lock()
	id := auth.RandomID("s_")
	in := &session.Info{Spec: session.Spec{ID: id, Handle: handle, Agent: agentID, AgentKind: agent.KindAgent, MachineID: "local",
		ProjectID: p.ID, WorkspaceID: wid, CreatedAt: t0.Add(time.Duration(len(h.order)) * time.Second)}, Status: session.StatusRunning}
	if agentID == "shell" {
		in.AgentKind = agent.KindShell
		state = agentstate.Shell
	}
	in.AgentState, in.Foreground = state, agentstate.FgAgent
	h.sessions[id] = in
	h.order = append(h.order, id)
	h.mu.Unlock()
	if _, err := h.boards.AddTerminal(wid, id, nil); err != nil {
		h.t.Fatal(err)
	}
	return *in
}

func (h *fakeHub) setState(id, state string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[id].AgentState = state
}

func (h *fakeHub) node(wid, sid string) *board.Node {
	b, _ := h.boards.Get(wid)
	for _, n := range b.Nodes {
		if n.SessionID() == sid {
			return n
		}
	}
	h.t.Fatalf("no node for %s", sid)
	return nil
}

// ---- Hub ----

func (h *fakeHub) Settings() settings.Settings { return h.set }
func (h *fakeHub) Publish(v any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := v.(map[string]any); ok {
		h.events = append(h.events, m)
	}
}
func (h *fakeHub) Sessions() []session.Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []session.Info
	for _, id := range h.order {
		if in, ok := h.sessions[id]; ok {
			c := *in
			c.Queue = 0
			for _, d := range h.dels {
				if d.SessionID == id && !d.Final() && d.State != delivery.Sent {
					c.Queue++
				}
			}
			out = append(out, c)
		}
	}
	return out
}
func (h *fakeHub) SessionInfo(id string) (session.Info, bool) {
	for _, in := range h.Sessions() {
		if in.ID == id {
			return in, true
		}
	}
	return session.Info{}, false
}
func (h *fakeHub) Workspace(wid string) (project.Workspace, project.Project, bool) {
	return h.projects.Workspace(wid)
}
func (h *fakeHub) Project(pid string) (project.Project, bool) { return h.projects.Get(pid) }
func (h *fakeHub) Projects() []project.Project                { return h.projects.List() }
func (h *fakeHub) Board(wid string) (board.Board, error)      { return h.boards.Get(wid) }
func (h *fakeHub) ApplyOps(wid string, ops []board.Op) error {
	_, err := h.boards.Apply(wid, "", ops)
	return err
}
func (h *fakeHub) Agents() []agent.Agent { return append(agent.Builtins(), agent.Catalog()...) }
func (h *fakeHub) DefaultAgent(string) string {
	return "claude"
}
func (h *fakeHub) AgentOnMachine(m, a string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.onMach[m+"/"+a]
}
func (h *fakeHub) CreateSession(req session.CreateRequest) (session.Info, error) {
	p, _ := h.projects.Get(req.ProjectID)
	h.mu.Lock()
	n := len(h.order)
	h.mu.Unlock()
	handle := fmt.Sprintf("N%d", n+1)
	in := session.Info{Spec: session.Spec{ID: auth.RandomID("s_"), Handle: handle, Agent: req.Agent, AgentKind: agent.KindAgent,
		MachineID: req.MachineID, Cwd: req.Cwd, ProjectID: p.ID, WorkspaceID: req.WorkspaceID, CreatedAt: t0.Add(time.Duration(n) * time.Second)},
		Status: session.StatusRunning}
	if req.Agent == "shell" {
		in.AgentKind = agent.KindShell
	}
	in.AgentState = agentstate.Starting
	h.mu.Lock()
	h.sessions[in.ID] = &in
	h.order = append(h.order, in.ID)
	h.calls = append(h.calls, "create:"+req.Agent+"@"+req.MachineID)
	h.mu.Unlock()
	return in, nil
}
func (h *fakeHub) RenameSession(id, name string) error {
	in, ok := h.sessions[id]
	if !ok {
		return errors.New("no such session")
	}
	in.Name, in.Handle = name, name
	h.sessions[id] = in
	return nil
}

func (h *fakeHub) DeleteSession(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, id)
	h.calls = append(h.calls, "delete:"+id)
	return nil
}
func (h *fakeHub) Deliver(sid string, req delivery.Request) (delivery.Delivery, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.delErr[sid]; err != nil {
		return delivery.Delivery{}, err
	}
	in, ok := h.sessions[sid]
	if !ok {
		return delivery.Delivery{}, delivery.ErrNotFound
	}
	if in.AgentKind != agent.KindAgent || in.AgentState == agentstate.Shell {
		return delivery.Delivery{}, delivery.ErrNotAgent
	}
	d := &delivery.Delivery{ID: auth.RandomID("dl_"), SessionID: sid, Handle: in.Handle, Text: req.Text, Source: req.Source,
		OrderID: req.OrderID, ForwardDepth: req.ForwardDepth, State: delivery.Queued, CreatedAt: t0, ProjectID: in.ProjectID}
	h.dels[d.ID] = d
	h.delOrder = append(h.delOrder, d.ID)
	h.delReqs = append(h.delReqs, req)
	return *d, nil
}
func (h *fakeHub) Delivery(id string) (delivery.Delivery, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.dels[id]
	if !ok {
		return delivery.Delivery{}, false
	}
	return *d, true
}
func (h *fakeHub) CancelDelivery(id string) (delivery.Delivery, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.dels[id]
	if !ok {
		return delivery.Delivery{}, delivery.ErrNoDelivery
	}
	if d.State != delivery.Queued && d.State != delivery.Waiting {
		return *d, delivery.ErrAlreadySent
	}
	d.State = delivery.Cancelled
	return *d, nil
}

// setDelivery moves a delivery and notifies the service.
func (h *fakeHub) setDelivery(s *Service, id, state string) delivery.Delivery {
	h.mu.Lock()
	d := h.dels[id]
	d.State = state
	if state == delivery.Sent || state == delivery.Confirmed {
		t := s.Now().UTC()
		d.SentAt = &t
		if state == delivery.Confirmed {
			d.ConfirmedAt = &t
		}
	}
	c := *d
	h.mu.Unlock()
	s.OnDelivery(c)
	return c
}

// deliveriesTo returns deliveries of a session in creation order.
func (h *fakeHub) deliveriesTo(sid string) []delivery.Delivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []delivery.Delivery
	for _, id := range h.delOrder {
		if d := h.dels[id]; d.SessionID == sid {
			out = append(out, *d)
		}
	}
	return out
}

func (h *fakeHub) Pause(_ context.Context, id string) error  { return h.call("pause:" + id) }
func (h *fakeHub) Resume(_ context.Context, id string) error { return h.call("resume:" + id) }
func (h *fakeHub) SwitchAgent(id, agentID, profileID, sessionID string, model, effort *string) error {
	if err := h.call("switch:" + id + ":" + agentID + ":" + sessionID); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.switchErr != nil {
		return h.switchErr
	}
	if s, ok := h.sessions[id]; ok {
		s.Agent = agentID
		if !h.switchKeepsStart {
			s.StartedAt = t0.Add(time.Hour)
		}
	}
	return nil
}
func (h *fakeHub) findRun(purpose string) (jobs.RunnerRequest, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.runs {
		if r.Purpose == purpose {
			return r, true
		}
	}
	return jobs.RunnerRequest{}, false
}
func (h *fakeHub) setPastRuns(id string, runs []session.PastRun) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[id].PastRuns = runs
}
func (h *fakeHub) Restart(id string) error { return h.call("restart:" + id) }
func (h *fakeHub) Kill(id string) error    { return h.call("kill:" + id) }
func (h *fakeHub) call(c string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, c)
	return nil
}
func (h *fakeHub) Reply(id string) (*session.Reply, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.replies[id]
	return r, ok
}
func (h *fakeHub) TranscriptText(_ context.Context, id string, max int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.transcr[id]
}
func (h *fakeHub) DocumentText(docID, versionID string) (string, string, error) {
	return "Spec", h.docText, nil
}

func (h *fakeHub) PreviewLog(previewID string, since int64) ([]preview.LogEntry, int64, error) {
	var out []preview.LogEntry
	var seq int64
	for _, e := range h.pageLog {
		seq = e.Seq
		if e.Seq > since {
			out = append(out, e)
		}
	}
	return out, seq, nil
}

func (h *fakeHub) BranchesText(projectID string) (string, error) {
	return "- main (current)\n- dev", nil
}

func (h *fakeHub) AssetPath(id string) string { return "/data/assets/" + id }

func (h *fakeHub) ScreenText(id string, lines int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.screen[id]
}
func (h *fakeHub) SubmitRunner(req jobs.RunnerRequest) (jobs.Job, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.runErr != nil {
		return jobs.Job{}, h.runErr
	}
	h.runs = append(h.runs, req)
	return jobs.Job{ID: auth.RandomID("jb_"), Purpose: req.Purpose, State: jobs.StateQueued}, nil
}
func (h *fakeHub) lastRun(t *testing.T, purpose string) jobs.RunnerRequest {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.runs) - 1; i >= 0; i-- {
		if h.runs[i].Purpose == purpose {
			return h.runs[i]
		}
	}
	t.Fatalf("no runner job %s", purpose)
	return jobs.RunnerRequest{}
}
func (h *fakeHub) Exec(context.Context, string, string, string, time.Duration) (int, error) {
	return h.execCode, h.execErr
}
func (h *fakeHub) Attention(a Attention) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attn = append(h.attn, a)
}
func (h *fakeHub) PlannerHost() (bool, string) { return h.planner, "" }
func (h *fakeHub) CreateWorkspace(pid, name string) (project.Workspace, error) {
	w, _, err := h.projects.AddWorkspace(pid, name)
	return w, err
}
func (h *fakeHub) RenameWorkspace(wid, name string) (project.Workspace, error) {
	w, _, err := h.projects.UpdateWorkspace(wid, &name, nil)
	return w, err
}
func (h *fakeHub) CloseWorkspace(wid string) error {
	_, _, err := h.projects.DeleteWorkspace(wid)
	return err
}

// job builds a finished runner job with a result.
func doneJob(text string, structured any) jobs.Job {
	r := map[string]any{"text": text}
	if structured != nil {
		r["structured"] = structured
	}
	b, _ := json.Marshal(r)
	st, fin := t0, t0.Add(2*time.Second)
	return jobs.Job{State: jobs.StateDone, Result: b, StartedAt: &st, FinishedAt: &fin}
}

func failedJob(code string) jobs.Job {
	return jobs.Job{State: jobs.StateFailed, Code: code, Error: code}
}

// cmd runs a command in the project thread.
func cmd(t *testing.T, s *Service, wid, text string) CommandResponse {
	t.Helper()
	r, err := s.Command(CommandRequest{Text: text, WorkspaceID: wid, ClientID: "c_test", Planner: "off"})
	if err != nil {
		t.Fatalf("%q: %v", text, err)
	}
	return r
}

func texts(r CommandResponse) string {
	var out []string
	for _, m := range r.Messages {
		out = append(out, m.Kind+":"+m.Text)
	}
	return fmt.Sprint(out)
}
