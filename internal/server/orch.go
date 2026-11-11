package server

import (
	"context"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/preview"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/settings"
	"github.com/azamrizki/nextboard/internal/transcript"
)

// Phase 6 wiring (docs/API-orchestration.md): the delivery engine reads
// consoles through consoleAdapter; the orchestration service reaches the
// rest of the hub through hubAdapter.

// consoleAdapter exposes a session to the delivery engine.
type consoleAdapter struct{ s *session.Session }

func (c consoleAdapter) View() session.DeliveryView { return c.s.DeliveryView() }
func (c consoleAdapter) AgentForeground(ctx context.Context) (bool, error) {
	return c.s.AgentForeground(ctx)
}
func (c consoleAdapter) Write(p []byte) error { return c.s.WriteDelivery(p) }

// setupOrchestration creates the delivery engine and the service and
// connects them to sessions, boards and events.
func (s *Server) setupOrchestration() {
	d := delivery.New()
	d.Source = func(id string) (delivery.Console, bool) {
		sess, ok := s.sessions.Get(id)
		if !ok {
			return nil, false
		}
		return consoleAdapter{sess}, true
	}
	d.Settings = func() settings.Delivery { return s.settings.Get().Delivery }
	s.deliver = d
	s.orch = orch.New(hubAdapter{s}, s.st)
	d.OnChange = func(dl delivery.Delivery) {
		s.events.Publish(map[string]any{"type": "delivery", "delivery": dl})
		s.sessions.Changed(dl.SessionID)
		s.orch.OnDelivery(dl)
	}
	s.sessions.QueueLen = d.ActiveCount
	s.sessions.OnOutput = s.orch.OnOutput
	s.sessions.HandleConflict = func(wid, h string) bool {
		b, err := s.boards.Get(wid)
		if err != nil {
			return false
		}
		for _, n := range b.Nodes {
			if n.Kind == orch.KindGroup && orch.Norm(orch.GroupOf(n).Name) == orch.Norm(h) {
				return true
			}
		}
		return false
	}
	s.boards.Validate = orch.BoardValidator(s.sessions.HandleTaken)
}

// startOrchestration starts the tickers (Run and tests).
func (s *Server) startOrchestration() {
	s.deliver.Start()
	s.orch.Start()
}

func (s *Server) stopOrchestration() {
	s.deliver.Close()
	s.orch.Close()
}

// hubAdapter implements orch.Hub.
type hubAdapter struct{ s *Server }

func (h hubAdapter) Settings() settings.Settings { return h.s.settings.Get() }
func (h hubAdapter) Publish(v any)               { h.s.events.Publish(v) }
func (h hubAdapter) Sessions() []session.Info    { return h.s.sessions.List() }
func (h hubAdapter) SessionInfo(id string) (session.Info, bool) {
	return h.s.sessions.Info(id)
}
func (h hubAdapter) Workspace(wid string) (project.Workspace, project.Project, bool) {
	return h.s.projects.Workspace(wid)
}
func (h hubAdapter) Project(pid string) (project.Project, bool) { return h.s.projects.Get(pid) }
func (h hubAdapter) Projects() []project.Project                { return h.s.projects.List() }
func (h hubAdapter) Board(wid string) (board.Board, error)      { return h.s.boards.Get(wid) }
func (h hubAdapter) ApplyOps(wid string, ops []board.Op) error {
	_, err := h.s.boards.Apply(wid, "", ops)
	return err
}
func (h hubAdapter) Agents() []agent.Agent { return h.s.agents.All() }

func (h hubAdapter) DocumentText(docID, versionID string) (string, string, error) {
	d, err := h.s.studio.docs.Get(docID)
	if err != nil {
		return "", "", err
	}
	if versionID == "" {
		for _, v := range d.Versions {
			if v.State != "running" {
				versionID = v.ID
			}
		}
	}
	if versionID == "" {
		return d.Title, "", nil
	}
	_, md, err := h.s.studio.docs.Version(docID, versionID)
	return d.Title, md, err
}

func (h hubAdapter) PreviewLog(previewID string, since int64) ([]preview.LogEntry, int64, error) {
	return h.s.gitPrev.previews.Logs(previewID, since)
}

func (h hubAdapter) BranchesText(projectID string) (string, error) {
	rp, ok := h.s.gitRepo(projectID)
	if !ok {
		return "", orch.ErrProjectNotFound
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, err := h.s.gitPrev.git.Branches(ctx, rp)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, it := range b.Items {
		l := "- " + it.Name
		if it.Name == b.Current {
			l += " (current)"
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n"), nil
}

func (h hubAdapter) AssetPath(id string) string { return h.s.assetPath(id) }

func (h hubAdapter) DefaultAgent(machineID string) string {
	host, ok := h.s.machines.Host(machineID)
	if !ok {
		return "shell"
	}
	_, def := h.s.machineAgents(host)
	return def
}

func (h hubAdapter) AgentOnMachine(machineID, agentID string) bool {
	host, ok := h.s.machines.Host(machineID)
	if !ok {
		return false
	}
	a, ok := h.s.agents.Get(agentID)
	return ok && h.s.available(host, a)
}

func (h hubAdapter) CreateSession(req session.CreateRequest) (session.Info, error) {
	in, err := h.s.sessions.Create(req)
	if err == nil {
		h.s.emitMachine(in.MachineID)
	}
	return in, err
}

func (h hubAdapter) RenameSession(id, name string) error {
	_, err := h.s.sessions.Update(id, &name, &name)
	return err
}

func (h hubAdapter) DeleteSession(id string) error { return h.s.sessions.Delete(id) }
func (h hubAdapter) Deliver(sid string, req delivery.Request) (delivery.Delivery, error) {
	return h.s.deliver.Submit(sid, req)
}
func (h hubAdapter) Delivery(id string) (delivery.Delivery, bool) { return h.s.deliver.Get(id) }
func (h hubAdapter) CancelDelivery(id string) (delivery.Delivery, error) {
	return h.s.deliver.Cancel(id)
}
func (h hubAdapter) SwitchAgent(id, agentID, profileID, providerID, sessionID string, model, effort *string) error {
	mode := agent.ModeFresh
	if sessionID != "" {
		mode = agent.ModeResume
	}
	// the choice made in the dialog: a provider, or "" for a login
	_, err := h.s.sessions.Restart(id, session.RestartRequest{Model: model, Effort: effort, Agent: agentID, ProfileID: &profileID, ProviderID: &providerID, Mode: mode, AgentSessionID: sessionID})
	return err
}
func (h hubAdapter) Pause(ctx context.Context, id string) error {
	_, err := h.s.sessions.Pause(ctx, id)
	return err
}
func (h hubAdapter) Resume(ctx context.Context, id string) error {
	_, err := h.s.sessions.Resume(ctx, id)
	return err
}

// Restart from a command keeps the conversation (mode resume when an id
// is known, else continue).
func (h hubAdapter) Restart(id string) error {
	t := true
	_, err := h.s.sessions.Restart(id, session.RestartRequest{Resume: &t})
	return err
}
func (h hubAdapter) Kill(id string) error {
	_, err := h.s.sessions.Kill(id)
	return err
}

func (h hubAdapter) Reply(id string) (*session.Reply, bool) {
	sess, ok := h.s.sessions.Get(id)
	if !ok {
		return nil, false
	}
	return sess.Reply()
}

// TranscriptText is the tail of the conversation as "role: text" lines.
func (h hubAdapter) TranscriptText(ctx context.Context, id string, max int) string {
	sess, ok := h.s.sessions.Get(id)
	if !ok {
		return ""
	}
	host, kind, p, err := sess.TranscriptLocation(ctx)
	if err != nil || p == "" {
		return ""
	}
	msgs, _, err := transcript.ReadMessages(ctx, host, kind, p, 200)
	if err != nil {
		return ""
	}
	var parts []string
	n := 0
	for i := len(msgs) - 1; i >= 0 && n < max; i-- {
		m := msgs[i]
		if m.Sidechain || strings.TrimSpace(m.Text) == "" {
			continue
		}
		line := m.Role
		if m.Tool != "" {
			line += " (" + m.Tool + ")"
		}
		line += ": " + m.Text
		n += len([]rune(line))
		parts = append([]string{line}, parts...)
	}
	out := strings.Join(parts, "\n\n")
	if r := []rune(out); len(r) > max {
		out = string(r[len(r)-max:])
	}
	return out
}

func (h hubAdapter) ScreenText(id string, lines int) string {
	sess, ok := h.s.sessions.Get(id)
	if !ok {
		return ""
	}
	return sess.ScrollbackText(lines)
}

func (h hubAdapter) SubmitRunner(req jobs.RunnerRequest) (jobs.Job, error) {
	return h.s.submitRunner(req)
}

// Exec runs a loop goal command through the login shell in the project
// folder; the exit code decides.
func (h hubAdapter) Exec(ctx context.Context, machineID, cwd, script string, timeout time.Duration) (int, error) {
	host, ok := h.s.machines.Host(machineID)
	if !ok {
		return -1, machine.ErrNotFound
	}
	res, err := machine.Exec(ctx, host, machine.ExecSpec{Cwd: cwd, Script: `exec "${SHELL:-/bin/sh}" -lc ` + machine.Quote(script),
		Timeout: timeout, IdleTimeout: timeout})
	if err != nil {
		return -1, err
	}
	if res.TimedOut || res.IdleKilled {
		return -1, context.DeadlineExceeded
	}
	return res.ExitCode, nil
}

func (h hubAdapter) Attention(a orch.Attention) {
	at := h.s.attn.add(Attention{Kind: a.Kind, Title: a.Title, Body: a.Body, SessionID: a.SessionID, Handle: a.Handle,
		Agent: a.Agent, ProjectID: a.ProjectID, WorkspaceID: a.WorkspaceID})
	h.s.events.Publish(map[string]any{"type": "attention", "attention": at})
}

// PlannerHost: the lead's CLI must be detected on This PC (decision #6).
func (h hubAdapter) PlannerHost(agentID string) (bool, string) {
	if h.AgentOnMachine(machine.LocalID, agentID) {
		return true, ""
	}
	return false, agentID + "_missing"
}

func (h hubAdapter) ProviderRunner(id string) (string, string, error) {
	return h.s.providerRunner(id)
}

func (h hubAdapter) Providers() []orch.ProviderInfo {
	return h.s.providerInfos()
}

func (h hubAdapter) CreateWorkspace(pid, name string) (project.Workspace, error) {
	ws, pr, err := h.s.projects.AddWorkspace(pid, name)
	if err == nil {
		h.s.emitProject(pr.ID)
	}
	return ws, err
}

func (h hubAdapter) RenameWorkspace(wid, name string) (project.Workspace, error) {
	ws, pr, err := h.s.projects.UpdateWorkspace(wid, &name, nil)
	if err == nil {
		h.s.emitProject(pr.ID)
	}
	return ws, err
}

func (h hubAdapter) CloseWorkspace(wid string) error {
	ws, pr, err := h.s.projects.DeleteWorkspace(wid)
	if err != nil {
		return err
	}
	h.s.closeBoards(pr.Name, []project.Workspace{ws})
	h.s.deleteSessions(func(pl session.Placement) bool { return pl.WorkspaceID == ws.ID })
	h.s.emitProject(pr.ID)
	return nil
}

// orchAttention forwards engine events to the orchestration (orders,
// forwards, loops).
func (s *Server) orchAttention(in session.Info, ev agentstate.Event) {
	if s.orch != nil {
		s.orch.OnAgentEvent(in, ev)
	}
}
