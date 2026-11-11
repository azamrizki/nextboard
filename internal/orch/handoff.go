package orch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/session"
)

// Handoff (docs/API-orchestration.md §9): summarise a console's work with
// the hidden runner, start a console of another agent next to it, and
// deliver the brief. The source keeps running; no confirmation.

const (
	handoffCollect    = 60000
	handoffScreen     = 400
	handoffRawBrief   = 12000
	handoffReady      = 120 * time.Second
	handoffNeedsExtra = 120 * time.Second
	handoffStartGrace = 8 * time.Second  // the wrapper's shell shows briefly at every launch
	handoffBackAfter  = 30 * time.Second // after a hub restart, how long a console gets to come back
	handoffsFile      = "handoffs.json"
	HandoffContinue   = "Continue this task from exactly this state. Do not redo finished work."
)

var (
	ErrHandoffRunning = errors.New("a handoff from this console is already running")
	ErrNotAgentSrc    = errors.New("the source console is not an agent console")
	ErrHandoffAgent   = errors.New("the target agent is unknown or not an agent")
)

// HandoffRequest is the body of POST /api/sessions/{id}/handoff.
type HandoffRequest struct {
	Agent       string `json:"agent"`
	MachineID   string `json:"machineId,omitempty"`
	ProfileID   string `json:"profileId,omitempty"`
	ProviderID  string `json:"providerId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	// Model and Effort: nil = the default in settings, "" = the CLI's own choice.
	Model    *string `json:"model,omitempty"`
	Effort   *string `json:"effort,omitempty"`
	Note     string  `json:"note,omitempty"`
	ThreadID string  `json:"threadId,omitempty"`
	// InPlace switches this console instead of starting another: the same tile,
	// the same folder. An agent that ran here before continues its conversation,
	// unless Fresh. NoBrief skips the summary.
	InPlace bool `json:"inPlace,omitempty"`
	NoBrief bool `json:"noBrief,omitempty"`
	Fresh   bool `json:"fresh,omitempty"`
}

// Handoff is the API view.
type Handoff struct {
	ID              string    `json:"id"`
	SourceSessionID string    `json:"sourceSessionId"`
	TargetSessionID *string   `json:"targetSessionId"`
	Agent           string    `json:"agent"`
	State           string    `json:"state"`
	JobID           string    `json:"jobId,omitempty"`
	Brief           string    `json:"brief,omitempty"`
	Error           string    `json:"error,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	InPlace         bool      `json:"inPlace,omitempty"`
	// Returning: the agent goes back to its earlier conversation.
	Returning bool `json:"returning,omitempty"`

	req HandoffRequest

	backTo string // the conversation to continue
	frozen bool   // the old agent is paused while its work is summarised

	remoteCwd    string
	startedAfter time.Time // in place: the state of the old process does not count
	sourceHandle string
	projectID    string
	workspaceID  string
	threadID     string
	phaseAt      time.Time
	needsAt      time.Time
	deliveryID   string
	content      string
	resume       bool // a hub restart interrupted it: handoffsTick picks it up
}

// savedHandoff is a handoff on disk: what the API shows, and what it takes to carry on.
type savedHandoff struct {
	Handoff
	Req          HandoffRequest `json:"req"`
	BackTo       string         `json:"backTo,omitempty"`
	Frozen       bool           `json:"frozen,omitempty"`
	RemoteCwd    string         `json:"remoteCwd,omitempty"`
	StartedAfter time.Time      `json:"startedAfter"`
	SourceHandle string         `json:"sourceHandle"`
	ProjectID    string         `json:"projectId"`
	WorkspaceID  string         `json:"workspaceId"`
	ThreadID     string         `json:"threadId"`
	PhaseAt      time.Time      `json:"phaseAt"`
	NeedsAt      time.Time      `json:"needsAt"`
	DeliveryID   string         `json:"deliveryId,omitempty"`
}

func (s *Service) loadHandoffs() {
	var list []savedHandoff
	if _, err := s.st.Load(handoffsFile, &list); err != nil {
		return
	}
	for _, x := range list {
		if x.ID == "" {
			continue
		}
		h := x.Handoff
		h.req, h.backTo, h.frozen, h.remoteCwd, h.startedAfter = x.Req, x.BackTo, x.Frozen, x.RemoteCwd, x.StartedAfter
		h.sourceHandle, h.projectID, h.workspaceID, h.threadID = x.SourceHandle, x.ProjectID, x.WorkspaceID, x.ThreadID
		h.phaseAt, h.needsAt, h.deliveryID = x.PhaseAt, x.NeedsAt, x.DeliveryID
		s.handoffs[h.ID] = &h
	}
}

// saveHandoffs writes them all. One writer at a time, or an older list could land last.
func (s *Service) saveHandoffs() {
	s.handoffFile.Lock()
	defer s.handoffFile.Unlock()
	s.mu.Lock()
	list := make([]savedHandoff, 0, len(s.handoffs))
	for _, h := range s.handoffs {
		list = append(list, savedHandoff{Handoff: *h, Req: h.req, BackTo: h.backTo, Frozen: h.frozen, RemoteCwd: h.remoteCwd, StartedAfter: h.startedAfter,
			SourceHandle: h.sourceHandle, ProjectID: h.projectID, WorkspaceID: h.workspaceID, ThreadID: h.threadID,
			PhaseAt: h.phaseAt, NeedsAt: h.needsAt, DeliveryID: h.deliveryID})
	}
	s.mu.Unlock()
	_ = s.st.Save(handoffsFile, list)
}

// recoverHandoffs takes up what a hub restart interrupted (§9). Nothing was typed yet in
// every state but delivering, so those carry on. A brief on its way may have been typed:
// deliveries live in memory, so nobody knows, and the hub does not type a brief twice by
// itself (like orders, decision #34). It goes to the thread for the owner to judge.
func (s *Service) recoverHandoffs(now time.Time) {
	s.mu.Lock()
	var list []Handoff
	for _, h := range s.handoffs {
		if !h.final() {
			list = append(list, *h)
		}
	}
	s.mu.Unlock()
	for _, h := range list {
		if h.State == "delivering" {
			s.handoffManual(h.ID, "manual", "the hub restarted while the brief was going out, so look at the console first: it may have it already")
			continue
		}
		s.setHandoff(h.ID, func(x *Handoff) { x.resume, x.phaseAt, x.needsAt = true, now, time.Time{} })
		s.Threads.Add(ThreadMessage{ThreadID: h.threadID, Role: "hub", Kind: "note", SessionID: h.SourceSessionID,
			Text: "The hub restarted during the handoff from @" + h.sourceHandle + ". It carries on."})
	}
}

// resumeHandoff carries on from the state a restart left a handoff in.
func (s *Service) resumeHandoff(h Handoff, now time.Time) {
	in, ok := s.hub.SessionInfo(h.SourceSessionID)
	resumed := func(f func(x *Handoff)) {
		s.setHandoff(h.ID, func(x *Handoff) {
			x.resume = false
			if f != nil {
				f(x)
			}
		})
	}
	switch h.State {
	case "collecting", "summarizing":
		if !ok {
			s.handoffManual(h.ID, "failed", "its console is gone")
			return
		}
		// Its transcript may be out of reach until it is back, and an empty one means
		// nothing was done. The summary job died with the hub; the text is all still there.
		if in.Status != session.StatusRunning && now.Sub(h.phaseAt) < handoffBackAfter {
			return
		}
		resumed(func(x *Handoff) { x.State, x.JobID = "collecting", "" })
		if h.req.NoBrief {
			go s.switchInPlace(h.ID)
		} else {
			go s.collect(h.ID)
		}
	case "spawning":
		resumed(nil)
		if h.InPlace {
			go s.switchInPlace(h.ID)
		} else {
			go s.spawnTarget(h.ID)
		}
	case "switching":
		if !ok {
			s.handoffManual(h.ID, "failed", "its console is gone")
			return
		}
		if in.Agent != h.Agent {
			resumed(nil)
			go s.switchInPlace(h.ID)
			return
		}
		target := h.SourceSessionID
		resumed(func(x *Handoff) {
			x.TargetSessionID, x.startedAfter = &target, h.CreatedAt
			if strings.TrimSpace(x.Brief) == "" {
				x.State = "done"
				return
			}
			x.State = "waitingReady"
		})
	case "waitingReady":
		if h.TargetSessionID == nil {
			s.handoffManual(h.ID, "manual", "its console is not known after the hub restarted")
			return
		}
		resumed(nil)
	}
}

func (h *Handoff) final() bool {
	return h.State == "done" || h.State == "failed" || h.State == "manual"
}

func (s *Service) publishHandoff(h *Handoff) {
	c := *h
	s.hub.Publish(map[string]any{"type": "handoff", "handoff": c})
}

// HandoffView returns a handoff by id.
func (s *Service) HandoffView(id string) (Handoff, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.handoffs[id]
	if !ok {
		return Handoff{}, false
	}
	return *h, true
}

// StartHandoff begins a handoff from a console.
func (s *Service) StartHandoff(sourceID string, req HandoffRequest) (*Handoff, error) {
	in, ok := s.hub.SessionInfo(sourceID)
	if !ok {
		return nil, ErrSessionNotFound
	}
	if !isAgent(in) {
		return nil, ErrNotAgentSrc
	}
	a, ok := agentByID(s.hub.Agents(), req.Agent)
	if !ok || a.Kind != agent.KindAgent {
		return nil, ErrHandoffAgent
	}
	req.Agent = a.ID
	if req.InPlace {
		if !s.hub.AgentOnMachine(in.MachineID, a.ID) {
			return nil, ErrHandoffAgent
		}
		req.MachineID, req.WorkspaceID = "", ""
	}
	// The next agent works where this one works.
	remoteCwd := ""
	if in.RemoteMachineID != "" && req.MachineID == "" {
		req.MachineID, remoteCwd = in.RemoteMachineID, in.RemoteCwd
	}
	wid := in.WorkspaceID
	if req.WorkspaceID != "" {
		wid = req.WorkspaceID
	}
	tid := req.ThreadID
	if tid == "" || !ValidThreadID(tid) {
		tid = in.ProjectID
	}
	now := s.Now()
	h := &Handoff{ID: auth.RandomID("ho_"), SourceSessionID: sourceID, Agent: a.ID, State: "collecting", CreatedAt: now.UTC(),
		InPlace: req.InPlace, req: req, remoteCwd: remoteCwd, sourceHandle: in.Handle, projectID: in.ProjectID, workspaceID: wid, threadID: tid, phaseAt: now}
	if req.InPlace && !req.Fresh {
		for _, r := range in.PastRuns {
			if r.Agent == a.ID && r.AgentSessionID != "" && (req.ProfileID == "" || req.ProfileID == r.ProfileID) {
				h.backTo, h.Returning = r.AgentSessionID, true
				if req.ProfileID == "" {
					h.req.ProfileID = r.ProfileID
				}
				break
			}
		}
	}
	s.mu.Lock()
	for _, o := range s.handoffs {
		if o.SourceSessionID == sourceID && !o.final() {
			s.mu.Unlock()
			return nil, ErrHandoffRunning
		}
	}
	s.handoffs[h.ID] = h
	s.mu.Unlock()
	s.saveHandoffs()
	s.publishHandoff(h)
	cp := *h // copied before collect may change h
	go func() {
		// Its work stands still while it is written down.
		if req.InPlace && !req.NoBrief && in.AgentState == agentstate.Working {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := s.hub.Pause(ctx, sourceID)
			cancel()
			if err == nil {
				s.setHandoff(h.ID, func(x *Handoff) { x.frozen = true })
			}
		}
		if req.NoBrief {
			s.switchInPlace(h.ID)
			return
		}
		s.collect(h.ID)
	}()
	return &cp, nil
}

// switchInPlace restarts the console with the next agent; handoffsTick delivers
// the brief once it is ready.
func (s *Service) switchInPlace(id string) {
	h, ok := s.HandoffView(id)
	if !ok {
		return
	}
	if h.frozen {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.hub.Resume(ctx, h.SourceSessionID) // a stopped process ignores the hangup
		cancel()
	}
	s.setHandoff(id, func(x *Handoff) { x.State, x.phaseAt = "switching", s.Now() })
	before := h.CreatedAt
	if err := s.hub.SwitchAgent(h.SourceSessionID, h.Agent, h.req.ProfileID, h.req.ProviderID, h.backTo, h.req.Model, h.req.Effort); err != nil {
		s.handoffManual(id, "failed", err.Error())
		return
	}
	verb := "Switched"
	if h.Returning {
		verb = "Went back"
	}
	s.Threads.Add(ThreadMessage{ThreadID: h.threadID, Role: "hub", Kind: "note", SessionID: h.SourceSessionID, Text: fmt.Sprintf("%s to %s on @%s.", verb, h.Agent, h.sourceHandle)})
	target := h.SourceSessionID
	s.setHandoff(id, func(x *Handoff) {
		x.TargetSessionID, x.startedAfter = &target, before
		if strings.TrimSpace(x.Brief) == "" {
			x.State = "done"
			return
		}
		x.State, x.phaseAt = "waitingReady", s.Now()
	})
}

// ErrSessionNotFound is a missing console.
var ErrSessionNotFound = errors.New("session not found")

func (s *Service) setHandoff(id string, f func(h *Handoff)) (Handoff, bool) {
	s.mu.Lock()
	h, ok := s.handoffs[id]
	if !ok {
		s.mu.Unlock()
		return Handoff{}, false
	}
	f(h)
	c := *h
	s.mu.Unlock()
	s.saveHandoffs()
	s.publishHandoff(&c)
	return c, true
}

// collect reads the transcript (or the screen) and starts the summary.
func (s *Service) collect(id string) {
	h, _ := s.HandoffView(id)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	text := s.hub.TranscriptText(ctx, h.SourceSessionID, handoffCollect)
	cancel()
	in, _ := s.hub.SessionInfo(h.SourceSessionID)
	if strings.TrimSpace(text) == "" {
		// An agent that keeps a transcript and has none has not done anything:
		// its screen would only be a welcome message to summarise.
		if a, ok := agentByID(s.hub.Agents(), in.Agent); h.InPlace && ok && a.Transcript != agent.TranscriptNone {
			s.switchInPlace(id)
			return
		}
		text = s.hub.ScreenText(h.SourceSessionID, handoffScreen)
	}
	if len([]rune(text)) > handoffCollect {
		r := []rune(text)
		text = string(r[len(r)-handoffCollect:])
	}
	machineID, runAgent := machine.LocalID, "claude"
	if p, ok := s.hub.Project(h.projectID); ok {
		switch {
		case s.hub.AgentOnMachine(p.MachineID, "claude"):
			machineID, runAgent = p.MachineID, "claude"
		case s.hub.AgentOnMachine(p.MachineID, "codex"):
			machineID, runAgent = p.MachineID, "codex"
		}
	}
	if machineID == machine.LocalID && !s.hub.AgentOnMachine(machine.LocalID, "claude") && s.hub.AgentOnMachine(machine.LocalID, "codex") {
		runAgent = "codex"
	}
	prompt := "You are writing a handoff brief so another coding agent can continue this work.\n" +
		"From the conversation below, write: the goal, what is already done, the current state, files touched, next steps, open questions. " +
		"At most 1500 words. Plain Markdown, no preamble.\n"
	if h.req.Note != "" {
		prompt += "Note from the user: " + h.req.Note + "\n"
	}
	prompt += "\nConversation of @" + in.Handle + ":\n\n" + text
	var cmd string
	if machineID == machine.LocalID && runAgent == "claude" {
		cmd = s.claudeCommand()
	}
	j, err := s.hub.SubmitRunner(jobs.RunnerRequest{Purpose: "handoff-summary", ProjectID: h.projectID, MachineID: machineID,
		Agent: runAgent, Command: cmd, Prompt: prompt, Cwd: projectCwd(s, h.projectID, machineID),
		OnFinish: func(j jobs.Job) { s.summarised(id, j, text) }})
	if err != nil {
		s.summarised(id, jobs.Job{State: jobs.StateFailed, Error: err.Error()}, text)
		return
	}
	s.setHandoff(id, func(h *Handoff) {
		if h.State == "collecting" {
			h.State, h.JobID, h.content = "summarizing", j.ID, ""
		}
	})
}

func projectCwd(s *Service, pid, machineID string) string {
	if p, ok := s.hub.Project(pid); ok && p.MachineID == machineID {
		return p.Path
	}
	return ""
}

// summarised takes the brief (or the raw tail on failure) and spawns.
func (s *Service) summarised(id string, j jobs.Job, raw string) {
	brief := ""
	note := ""
	if j.State == jobs.StateDone {
		var r struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(j.Result, &r) == nil {
			brief = strings.TrimSpace(r.Text)
		}
	}
	if brief == "" {
		rr := []rune(raw)
		if len(rr) > handoffRawBrief {
			rr = rr[len(rr)-handoffRawBrief:]
		}
		brief = "(Summary unavailable — raw recent context follows.)\n\n" + string(rr)
		note = "Handoff summary failed"
		if j.Error != "" {
			note += ": " + j.Error
		}
		note += "; sending the raw recent context."
	}
	h, ok := s.setHandoff(id, func(h *Handoff) { h.Brief, h.State = brief, "spawning" })
	if !ok {
		return
	}
	if note != "" {
		s.Threads.Add(ThreadMessage{ThreadID: h.threadID, Role: "hub", Kind: "note", SessionID: h.SourceSessionID, Text: note})
	}
	if h.InPlace {
		s.switchInPlace(id)
		return
	}
	s.spawnTarget(id)
}

// spawnTarget starts the next console next to the source, in its group.
func (s *Service) spawnTarget(id string) {
	h, ok := s.HandoffView(id)
	if !ok {
		return
	}
	req := SpawnRequest{WorkspaceID: h.workspaceID, Slots: []SpawnSlot{{Agent: h.Agent, Model: h.req.Model, Effort: h.req.Effort, MachineID: h.req.MachineID, ProfileID: h.req.ProfileID, ProviderID: h.req.ProviderID, Cwd: h.remoteCwd}}}
	if b, err := s.hub.Board(h.workspaceID); err == nil {
		for _, n := range b.Nodes {
			if n.Kind == board.KindTerminal && n.SessionID() == h.SourceSessionID {
				req.Near = n.ID
				for _, g := range b.Nodes {
					if g.Kind == KindGroup && containsStr(GroupOf(g).Members, n.ID) {
						req.Group = &SpawnGroup{ID: g.ID}
						req.At = &Point{X: n.X + n.W + 40, Y: n.Y}
					}
				}
			}
		}
	}
	res, err := s.Spawn(req)
	if err != nil || len(res.Sessions) == 0 {
		msg := "spawn failed"
		if err != nil {
			msg = err.Error()
		}
		s.handoffManual(id, "failed", msg)
		return
	}
	tid := res.Sessions[0].ID
	s.setHandoff(id, func(h *Handoff) { h.TargetSessionID, h.State, h.phaseAt = &tid, "waitingReady", s.Now() })
	s.Threads.Add(ThreadMessage{ThreadID: h.threadID, Role: "hub", Kind: "note", SessionID: tid,
		Text: fmt.Sprintf("Started @%s (%s) to take over from @%s.", res.Sessions[0].Handle, h.Agent, h.sourceHandle)})
}

func containsStr(l []string, x string) bool {
	for _, y := range l {
		if y == x {
			return true
		}
	}
	return false
}

// handoffManual ends a handoff without delivery: the brief goes to the
// thread so the user can copy it.
func (s *Service) handoffManual(id, state, msg string) {
	h, ok := s.setHandoff(id, func(h *Handoff) { h.State, h.Error = state, msg })
	if !ok {
		return
	}
	if h.frozen && state == "failed" { // it stays: give it its turn back
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.hub.Resume(ctx, h.SourceSessionID)
		cancel()
	}
	text := "Handoff from @" + h.sourceHandle + " needs you (" + msg + "). Copy this brief into the new console:\n\n" + h.Brief
	if strings.TrimSpace(h.Brief) == "" {
		text = "Handoff from @" + h.sourceHandle + " stopped: " + msg + "."
	}
	s.Threads.Add(ThreadMessage{ThreadID: h.threadID, Role: "hub", Kind: "reply", SessionID: h.SourceSessionID, Text: text})
}

// handoffsTick advances waitingReady and delivering handoffs.
func (s *Service) handoffsTick(now time.Time) {
	s.mu.Lock()
	var list []Handoff
	pruned := false
	for _, h := range s.handoffs {
		if h.resume || h.State == "waitingReady" || h.State == "delivering" {
			list = append(list, *h)
		}
		if h.final() && now.Sub(h.CreatedAt) > 24*time.Hour {
			delete(s.handoffs, h.ID)
			pruned = true
		}
	}
	s.mu.Unlock()
	if pruned {
		s.saveHandoffs()
	}
	for _, h := range list {
		if h.resume {
			s.resumeHandoff(h, now)
			continue
		}
		switch h.State {
		case "waitingReady":
			in, ok := s.hub.SessionInfo(*h.TargetSessionID)
			if !ok {
				s.handoffManual(h.ID, "failed", "the target console is gone")
				continue
			}
			if h.InPlace && !in.StartedAt.After(h.startedAfter) {
				// The old process is still going down: its state is not the new one's.
				if now.Sub(h.phaseAt) >= handoffReady {
					s.handoffManual(h.ID, "manual", "the console was not ready within 120 s")
				}
				continue
			}
			// Back at the prompt: it did not start. No use waiting two minutes.
			if h.InPlace && (in.AgentState == agentstate.Shell || in.AgentState == agentstate.Exited) && now.Sub(h.phaseAt) >= handoffStartGrace {
				s.handoffManual(h.ID, "manual", h.Agent+" did not start; its error is in the console")
				continue
			}
			switch {
			case in.AgentState == agentstate.Ready && !in.Paused:
				text := h.Brief + "\n\n" + HandoffContinue
				if h.Returning {
					text = "You are back on this work; another agent had it in the meantime. What it did:\n\n" + text
				}
				d, err := s.hub.Deliver(in.ID, delivery.Request{Text: text, Source: "handoff"})
				if err != nil {
					s.handoffManual(h.ID, "manual", err.Error())
					continue
				}
				s.setHandoff(h.ID, func(x *Handoff) { x.State, x.deliveryID = "delivering", d.ID })
			case in.AgentState == agentstate.NeedsYou:
				if h.needsAt.IsZero() {
					s.setHandoff(h.ID, func(x *Handoff) { x.needsAt = now })
				} else if now.Sub(h.needsAt) >= handoffNeedsExtra {
					s.handoffManual(h.ID, "manual", "the new console waits for you (e.g. a trust prompt)")
				}
			default:
				lim := handoffReady
				if !h.needsAt.IsZero() {
					lim += handoffNeedsExtra
				}
				if now.Sub(h.phaseAt) >= lim {
					s.handoffManual(h.ID, "manual", "the new console was not ready within 120 s")
				}
			}
		case "delivering":
			d, ok := s.hub.Delivery(h.deliveryID)
			switch {
			case !ok:
			case d.State == delivery.Sent || d.State == delivery.Confirmed:
				s.setHandoff(h.ID, func(x *Handoff) { x.State = "done" })
				text := "Handoff from @" + h.sourceHandle + " delivered to @" + d.Handle + "."
				if h.InPlace {
					text = "Brief delivered to " + h.Agent + " on @" + d.Handle + "."
				}
				s.Threads.Add(ThreadMessage{ThreadID: h.threadID, Role: "hub", Kind: "note", SessionID: d.SessionID, Text: text})
			case d.Final():
				s.handoffManual(h.ID, "manual", "delivery "+d.State+": "+d.Code)
			}
		}
	}
}
