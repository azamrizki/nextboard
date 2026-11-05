package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// HTTP endpoints of docs/API-orchestration.md.

func (s *Server) orchRoutes(authed func(string, handler)) {
	authed("POST /api/sessions/{id}/deliveries", s.handleDeliveryCreate)
	authed("GET /api/sessions/{id}/deliveries", s.handleSessionDeliveries)
	authed("GET /api/deliveries", s.handleDeliveries)
	authed("DELETE /api/deliveries/{id}", s.handleDeliveryCancel)

	authed("POST /api/projects/{id}/commands", s.handleCommand)
	authed("GET /api/projects/{id}/mentions", s.handleMentions)
	authed("GET /api/planner", s.handlePlanner)
	authed("POST /api/confirmations/{id}", s.handleConfirmation)

	authed("GET /api/threads/{tid}/messages", s.handleThreadMessages)
	authed("DELETE /api/threads/{tid}", s.handleThreadClear)
	authed("GET /api/threads/{tid}/messages/{mid}/output", s.handleMessageOutput)
	authed("POST /api/threads/{tid}/messages/{mid}/output/save", s.handleMessageOutputSave)
	authed("GET /api/threads/{tid}/orders", s.handleThreadOrders)
	authed("GET /api/orders/{id}", s.handleOrderGet)
	authed("POST /api/orders/{id}/cancel", s.handleOrderCancel)
	authed("POST /api/orders/{id}/resend", s.handleOrderResend)

	authed("POST /api/workspaces/{id}/spawn", s.handleSpawn)
	authed("GET /api/workspaces/{id}/mission", s.handleMission)
	authed("POST /api/sessions/{id}/pause", s.handlePause)
	authed("POST /api/sessions/{id}/resume", s.handleResume)
	authed("POST /api/workspaces/{id}/pause-all", s.handlePauseAll)
	authed("POST /api/workspaces/{id}/resume-all", s.handleResumeAll)
	authed("POST /api/sessions/{id}/handoff", s.handleHandoff)
	authed("GET /api/projects/{id}/forwards", s.handleForwards)
	authed("DELETE /api/forwards/{id}", s.handleForwardCancel)

	authed("GET /api/loops/templates", s.handleLoopTemplates)
	authed("POST /api/loops/stop-all", s.handleLoopsStopAll)
	authed("GET /api/projects/{id}/loops", s.handleLoops)
	authed("POST /api/projects/{id}/loops", s.handleLoopCreate)
	authed("PATCH /api/loops/{id}", s.handleLoopPatch)
	authed("DELETE /api/loops/{id}", s.handleLoopDelete)
	authed("POST /api/loops/{id}/start", s.handleLoopAction)
	authed("POST /api/loops/{id}/stop", s.handleLoopAction)
	authed("POST /api/loops/{id}/run-now", s.handleLoopAction)
	authed("GET /api/loops/{id}/history", s.handleLoopHistory)

	authed("GET /api/projects/{id}/memory", s.handleMemoryGet)
	authed("PUT /api/projects/{id}/memory", s.handleMemoryPut)
	authed("POST /api/projects/{id}/memory/scan", s.handleMemoryScan)
	authed("POST /api/projects/{id}/memory/accept", s.handleMemoryAccept)
	authed("POST /api/projects/{id}/memory/refresh", s.handleMemoryRefresh)
	authed("POST /api/projects/{id}/memory/push", s.handleMemoryPush)
	authed("POST /api/projects/{id}/memory/plan", s.handleMemoryPlan)
	authed("POST /api/projects/{id}/memory/init", s.handleMemoryInit)
	authed("POST /api/projects/{id}/memory/init/apply", s.handleMemoryInitApply)
	authed("GET /api/memory/galaxy", s.handleMemoryGalaxy)
	authed("GET /api/memory/search", s.handleMemorySearch)
	authed("GET /api/memory/docs/{name}", s.handleMemoryDocGet)
	authed("PUT /api/memory/docs/{name}", s.handleMemoryDocPut)
	authed("DELETE /api/memory/docs/{name}", s.handleMemoryDocDelete)

	authed("GET /api/layouts", s.handleLayouts)
	authed("POST /api/layouts", s.handleLayoutCreate)
	authed("PATCH /api/layouts/{id}", s.handleLayoutPatch)
	authed("DELETE /api/layouts/{id}", s.handleLayoutDelete)
	authed("POST /api/workspaces/{id}/deploy", s.handleDeploy)
	authed("POST /api/workspaces/{id}/tiles/{nodeId}/push", s.handleTilePush)
}

func by(p *principal) string {
	if p.Kind == "admin" {
		return "admin"
	}
	return p.DeviceID
}

// orchErr maps orchestration errors to {"error","code"}.
func (s *Server) orchErr(w http.ResponseWriter, err error) {
	var ve *board.ValidationError
	switch {
	case errors.As(err, &ve):
		s.boardErr(w, err)
	case errors.Is(err, orch.ErrEmptyText), errors.Is(err, delivery.ErrEmptyText):
		writeErrCode(w, http.StatusBadRequest, "empty_text", err.Error())
	case errors.Is(err, orch.ErrCommandTooLong), errors.Is(err, delivery.ErrTextTooLong):
		writeErrCode(w, http.StatusRequestEntityTooLarge, "text_too_long", err.Error())
	case errors.Is(err, orch.ErrWorkspaceNotFound), errors.Is(err, project.ErrWorkspaceNotFound):
		writeErrCode(w, http.StatusNotFound, "workspace_not_found", err.Error())
	case errors.Is(err, orch.ErrWrongProject):
		writeErrCode(w, http.StatusNotFound, "workspace_not_found", err.Error())
	case errors.Is(err, orch.ErrThreadBusy):
		writeErrCode(w, http.StatusTooManyRequests, "thread_busy", err.Error())
	case errors.Is(err, orch.ErrNoOutput):
		writeErrCode(w, http.StatusNotFound, "no_output", err.Error())
	case errors.Is(err, orch.ErrBadCard):
		writeErrCode(w, http.StatusBadRequest, "bad_card", err.Error())
	case errors.Is(err, orch.ErrTileNotFound):
		writeErrCode(w, http.StatusNotFound, "tile_not_found", err.Error())
	case errors.Is(err, orch.ErrNotContentTile):
		writeErrCode(w, http.StatusBadRequest, "not_content_tile", err.Error())
	case errors.Is(err, delivery.ErrNotFound), errors.Is(err, orch.ErrSessionNotFound), errors.Is(err, session.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "session_not_found", err.Error())
	case errors.Is(err, delivery.ErrNotAgent), errors.Is(err, session.ErrNotAgent):
		writeErrCode(w, http.StatusConflict, "not_agent", err.Error())
	case errors.Is(err, orch.ErrNotAgentSrc):
		writeErrCode(w, http.StatusBadRequest, "not_agent", err.Error())
	case errors.Is(err, delivery.ErrTargetBusy):
		writeErrCode(w, http.StatusConflict, "target_busy", err.Error())
	case errors.Is(err, delivery.ErrQueueFull):
		writeErrCode(w, http.StatusTooManyRequests, "queue_full", err.Error())
	case errors.Is(err, delivery.ErrNoDelivery):
		writeErrCode(w, http.StatusNotFound, "delivery_not_found", err.Error())
	case errors.Is(err, delivery.ErrAlreadySent):
		writeErrCode(w, http.StatusConflict, "already_sent", err.Error())
	case errors.Is(err, orch.ErrAlreadyDecided):
		writeErrCode(w, http.StatusConflict, "already_decided", err.Error())
	case errors.Is(err, orch.ErrConfExpired):
		writeErrCode(w, http.StatusGone, "expired", err.Error())
	case errors.Is(err, orch.ErrConfNotFound):
		writeErrCode(w, http.StatusNotFound, "confirmation_not_found", err.Error())
	case errors.Is(err, orch.ErrBadDecision):
		writeErrCode(w, http.StatusBadRequest, "bad_decision", err.Error())
	case errors.Is(err, orch.ErrOrderNotFound):
		writeErrCode(w, http.StatusNotFound, "order_not_found", err.Error())
	case errors.Is(err, orch.ErrNothingToResend):
		writeErrCode(w, http.StatusConflict, "nothing_to_resend", err.Error())
	case errors.Is(err, orch.ErrTooMany):
		writeErrCode(w, http.StatusBadRequest, "too_many", err.Error())
	case errors.Is(err, orch.ErrGroupNotFound):
		writeErrCode(w, http.StatusNotFound, "group_not_found", err.Error())
	case errors.Is(err, orch.ErrMembersMissing):
		writeErrCode(w, http.StatusBadRequest, "member_conflict", err.Error())
	case errors.Is(err, orch.ErrHandoffRunning):
		writeErrCode(w, http.StatusConflict, "handoff_running", err.Error())
	case errors.Is(err, orch.ErrHandoffAgent):
		writeErrCode(w, http.StatusBadRequest, "unknown_agent", err.Error())
	case errors.Is(err, orch.ErrForwardCycle):
		writeErrCode(w, http.StatusBadRequest, "forward_cycle", err.Error())
	case errors.Is(err, orch.ErrBadLoop):
		writeErrCode(w, http.StatusBadRequest, "bad_loop", err.Error())
	case errors.Is(err, orch.ErrTooManyLoops):
		writeErrCode(w, http.StatusTooManyRequests, "too_many_loops", err.Error())
	case errors.Is(err, orch.ErrLoopNotFound):
		writeErrCode(w, http.StatusNotFound, "loop_not_found", err.Error())
	case errors.Is(err, orch.ErrMtimeMismatch):
		writeErrCode(w, http.StatusConflict, "mtime_mismatch", err.Error())
	case errors.Is(err, orch.ErrMemoryTooBig):
		writeErrCode(w, http.StatusRequestEntityTooLarge, "memory_too_big", err.Error())
	case errors.Is(err, orch.ErrBadScope):
		writeErrCode(w, http.StatusBadRequest, "bad_scope", err.Error())
	case errors.Is(err, orch.ErrBadSubName):
		writeErrCode(w, http.StatusBadRequest, "bad_memory_name", err.Error())
	case errors.Is(err, orch.ErrSubNotFound):
		writeErrCode(w, http.StatusNotFound, "memory_not_found", err.Error())
	case errors.Is(err, orch.ErrCriticalStays):
		writeErrCode(w, http.StatusConflict, "critical_required", err.Error())
	case errors.Is(err, orch.ErrTooManySubs):
		writeErrCode(w, http.StatusConflict, "too_many_memories", err.Error())
	case errors.Is(err, orch.ErrCriticalBig):
		writeErrCode(w, http.StatusRequestEntityTooLarge, "memory_too_big", err.Error())
	case errors.Is(err, orch.ErrMemoryEmpty):
		writeErrCode(w, http.StatusBadRequest, "memory_empty", err.Error())
	case errors.Is(err, orch.ErrProjectNotFound), errors.Is(err, project.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "project_not_found", err.Error())
	case errors.Is(err, orch.ErrLayoutNotFound):
		writeErrCode(w, http.StatusNotFound, "layout_not_found", err.Error())
	case errors.Is(err, orch.ErrLayoutBuiltin):
		writeErrCode(w, http.StatusForbidden, "layout_builtin", err.Error())
	case errors.Is(err, orch.ErrBadLayout):
		writeErrCode(w, http.StatusBadRequest, "bad_layout", err.Error())
	case errors.Is(err, machine.ErrPidUnknown):
		writeErrCode(w, http.StatusConflict, "pid_unknown", err.Error())
	case errors.Is(err, machine.ErrSignalRefused):
		writeErrCode(w, http.StatusConflict, "signal_refused", err.Error())
	case errors.Is(err, session.ErrOffline):
		writeErrCode(w, http.StatusServiceUnavailable, "machine_offline", err.Error())
	case errors.Is(err, jobs.ErrQueueFull):
		writeErrCode(w, http.StatusTooManyRequests, "queue_full", err.Error())
	case errors.Is(err, jobs.ErrRunnerUnsupported):
		writeErrCode(w, http.StatusBadRequest, "runner_unsupported", err.Error())
	default:
		s.sessionErr(w, err)
	}
}

// ---- deliveries (§2) ----

func (s *Server) handleDeliveryCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req delivery.Request
	if !decode(w, r, &req) {
		return
	}
	switch req.Source {
	case "", "api", "reply", "skill", "dictation":
	default:
		req.Source = "api" // orchestration sources are hub-internal
	}
	d, err := s.deliver.Submit(r.PathValue("id"), req)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

func (s *Server) handleSessionDeliveries(w http.ResponseWriter, r *http.Request, p *principal) {
	if _, ok := s.sessionByPath(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.deliver.List(r.PathValue("id"))})
}

func (s *Server) handleDeliveries(w http.ResponseWriter, r *http.Request, p *principal) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, map[string]any{"items": s.deliver.ListAll(q.Get("projectId"), q.Get("active") == "1")})
}

func (s *Server) handleDeliveryCancel(w http.ResponseWriter, r *http.Request, p *principal) {
	if _, err := s.deliver.Cancel(r.PathValue("id")); err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

// ---- commands (§3) ----

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request, p *principal) {
	var req orch.CommandRequest
	if !decode(w, r, &req) {
		return
	}
	if _, ok := s.projects.Get(r.PathValue("id")); !ok {
		writeErrCode(w, http.StatusNotFound, "project_not_found", project.ErrNotFound.Error())
		return
	}
	if !clientIDRe.MatchString(req.ClientID) {
		writeErr(w, http.StatusBadRequest, "clientId tidak valid")
		return
	}
	req.ProjectID, req.By = r.PathValue("id"), by(p)
	res, err := s.orch.Command(req)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

// mention is one item of the @ menu.
type mention struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Label      string `json:"label"`
	Handle     string `json:"handle,omitempty"`
	AgentState string `json:"agentState,omitempty"`
	Board      string `json:"board,omitempty"` // tab name when not on the current board
}

func (s *Server) handleMentions(w http.ResponseWriter, r *http.Request, p *principal) {
	q := r.URL.Query()
	ws, pr, ok := s.projects.Workspace(q.Get("workspaceId"))
	if !ok || pr.ID != r.PathValue("id") {
		writeErrCode(w, http.StatusNotFound, "workspace_not_found", project.ErrWorkspaceNotFound.Error())
		return
	}
	b, err := s.boards.Get(ws.ID)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	needle := orch.Norm(q.Get("q"))
	match := func(label string) bool { return needle == "" || strings.Contains(orch.Norm(label), needle) }
	card := q.Get("cardId")
	wired := map[string]bool{}
	if card != "" {
		nodeSid := map[string]string{}
		for _, n := range b.Nodes {
			if n.Kind == board.KindTerminal {
				nodeSid[n.ID] = n.SessionID()
			}
		}
		for _, e := range b.Edges {
			if e.Kind != orch.EdgeWire || e.Source != card {
				continue
			}
			if sid, ok := nodeSid[e.Target]; ok {
				wired[sid] = true
			}
			for _, n := range b.Nodes {
				if n.ID == e.Target && n.Kind == orch.KindGroup {
					for _, m := range orch.GroupOf(n).Members {
						wired[nodeSid[m]] = true
					}
				}
			}
		}
	}
	// The main bar reaches every board of the project, a card only its own.
	boards := []board.Board{b}
	boardName := func(wid string) string {
		if wid == ws.ID {
			return ""
		}
		for _, w := range pr.Workspaces {
			if w.ID == wid {
				return w.Name
			}
		}
		return ""
	}
	if card == "" {
		for _, w := range pr.Workspaces {
			if w.ID != ws.ID {
				if ob, err := s.boards.Get(w.ID); err == nil {
					boards = append(boards, ob)
				}
			}
		}
	}
	out := []mention{}
	if match("everyone") {
		out = append(out, mention{Kind: "everyone", ID: "everyone", Label: "everyone"})
	}
	sessions := s.sessions.List()
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].WorkspaceID == ws.ID && sessions[j].WorkspaceID != ws.ID })
	agentsSeen := map[string]bool{}
	for _, in := range sessions {
		if card != "" && !wired[in.ID] {
			continue
		}
		if in.Handle != "" && match(in.Handle) {
			out = append(out, mention{Kind: "console", ID: in.ID, Label: in.Handle, Handle: in.Handle, AgentState: in.AgentState, Board: boardName(in.WorkspaceID)})
		}
		if (in.WorkspaceID == ws.ID || (card == "" && in.ProjectID == pr.ID)) && in.AgentKind == agent.KindAgent {
			agentsSeen[in.Agent] = true
		}
	}
	roles := map[string]bool{}
	for _, gb := range boards {
		for _, n := range gb.Nodes {
			if n.Kind == orch.KindGroup {
				g := orch.GroupOf(n)
				roles[g.Role] = true
				if match(g.Name) {
					out = append(out, mention{Kind: "group", ID: n.ID, Label: g.Name, Board: boardName(gb.WorkspaceID)})
				}
			}
		}
	}
	for _, role := range orch.Roles {
		if roles[role] && match(role) {
			out = append(out, mention{Kind: "role", ID: role, Label: role})
		}
	}
	var ags []string
	for a := range agentsSeen {
		ags = append(ags, a)
	}
	sort.Strings(ags)
	for _, a := range ags {
		if match(a) {
			out = append(out, mention{Kind: "agent", ID: a, Label: a})
		}
	}
	if len(out) > 30 {
		out = out[:30]
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePlanner(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.orch.Planner())
}

func (s *Server) handleConfirmation(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Decision string `json:"decision"`
		Choice   string `json:"choice"`
	}
	if !decode(w, r, &req) {
		return
	}
	cf, err := s.orch.Decide(r.PathValue("id"), req.Decision, req.Choice, by(p))
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cf)
}

// ---- threads and orders (§4) ----

func (s *Server) threadID(w http.ResponseWriter, r *http.Request) (string, bool) {
	tid := r.PathValue("tid")
	if !orch.ValidThreadID(tid) {
		writeErrCode(w, http.StatusBadRequest, "bad_thread", "id thread tidak valid")
		return "", false
	}
	return tid, true
}

func (s *Server) handleThreadMessages(w http.ResponseWriter, r *http.Request, p *principal) {
	tid, ok := s.threadID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, more := s.orch.Threads.List(tid, r.URL.Query().Get("before"), limit)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "hasMore": more,
		"confirmations": s.orch.Confirms.ForThread(tid), "planning": s.orch.Planning(tid)})
}

// handleMessageOutput is the reply a report message stands for, whatever its console did since.
func (s *Server) handleMessageOutput(w http.ResponseWriter, r *http.Request, p *principal) {
	tid, ok := s.threadID(w, r)
	if !ok {
		return
	}
	out, err := s.orch.Threads.Output(tid, r.PathValue("mid"))
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMessageOutputSave writes that reply to the project's outputs folder.
func (s *Server) handleMessageOutputSave(w http.ResponseWriter, r *http.Request, p *principal) {
	tid, ok := s.threadID(w, r)
	if !ok {
		return
	}
	out, err := s.orch.Threads.Output(tid, r.PathValue("mid"))
	if err != nil {
		s.orchErr(w, err)
		return
	}
	dir, ok := replySaveDir(w, r)
	if !ok {
		return
	}
	handle := out.Handle
	if handle == "" {
		handle = out.SessionID
	}
	s.saveReplyFile(w, r, s.outputProject(tid, out), dir, handle, out.Text, out.At)
}

// outputProject finds the project of a reply. Reports from before replies were kept do not
// say: a project thread is its project, otherwise their order or console may still know.
func (s *Server) outputProject(tid string, out orch.ReplyOutput) string {
	if out.ProjectID != "" {
		return out.ProjectID
	}
	if strings.HasPrefix(tid, "p_") {
		return tid
	}
	if o, ok := s.orch.Orders.Get(out.OrderID); ok {
		return o.ProjectID
	}
	if sess, ok := s.sessions.Get(out.SessionID); ok {
		return sess.Spec().ProjectID
	}
	return ""
}

func (s *Server) handleThreadClear(w http.ResponseWriter, r *http.Request, p *principal) {
	tid, ok := s.threadID(w, r)
	if !ok {
		return
	}
	s.orch.Threads.Clear(tid)
	s.events.Publish(map[string]any{"type": "threadCleared", "threadId": tid})
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleThreadOrders(w http.ResponseWriter, r *http.Request, p *principal) {
	tid, ok := s.threadID(w, r)
	if !ok {
		return
	}
	active := r.URL.Query().Get("active") == "1"
	items := s.orch.Orders.List(func(o orch.Order) bool { return o.ThreadID == tid && (!active || o.Active()) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleOrderGet(w http.ResponseWriter, r *http.Request, p *principal) {
	o, ok := s.orch.Orders.Get(r.PathValue("id"))
	if !ok {
		s.orchErr(w, orch.ErrOrderNotFound)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleOrderCancel(w http.ResponseWriter, r *http.Request, p *principal) {
	o, err := s.orch.CancelOrder(r.PathValue("id"))
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleOrderResend(w http.ResponseWriter, r *http.Request, p *principal) {
	o, err := s.orch.ResendOrder(r.PathValue("id"))
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// ---- groups, Mission Map, pause (§5, §7, §8) ----

func (s *Server) handleSpawn(w http.ResponseWriter, r *http.Request, p *principal) {
	ws, _, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var req orch.SpawnRequest
	if !decode(w, r, &req) {
		return
	}
	req.WorkspaceID = ws.ID
	res, err := s.orch.Spawn(req)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleMission(w http.ResponseWriter, r *http.Request, p *principal) {
	ws, _, ok := s.workspace(w, r)
	if !ok {
		return
	}
	m, err := s.orch.Mission(ws.ID, r.URL.Query().Get("scope") == "project")
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	in, err := s.sessions.Pause(ctx, r.PathValue("id"))
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request, p *principal) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	in, err := s.sessions.Resume(ctx, r.PathValue("id"))
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) pauseAll(w http.ResponseWriter, r *http.Request, resume bool) {
	ws, _, ok := s.workspace(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out := []string{}
	for _, in := range s.sessions.List() {
		if in.WorkspaceID != ws.ID || in.AgentKind != agent.KindAgent {
			continue
		}
		var err error
		if resume {
			_, err = s.sessions.Resume(ctx, in.ID)
		} else {
			_, err = s.sessions.Pause(ctx, in.ID)
		}
		if err == nil {
			out = append(out, in.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handlePauseAll(w http.ResponseWriter, r *http.Request, p *principal) {
	s.pauseAll(w, r, false)
}
func (s *Server) handleResumeAll(w http.ResponseWriter, r *http.Request, p *principal) {
	s.pauseAll(w, r, true)
}

// ---- handoff, forwards (§9, §10) ----

func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request, p *principal) {
	var req orch.HandoffRequest
	if !decode(w, r, &req) {
		return
	}
	h, err := s.orch.StartHandoff(r.PathValue("id"), req)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, h)
}

func (s *Server) handleForwards(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.orch.Forwards(r.PathValue("id"))})
}

func (s *Server) handleForwardCancel(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.orch.CancelForward(r.PathValue("id")); err != nil {
		writeErrCode(w, http.StatusNotFound, "forward_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

// ---- loops (§11) ----

func (s *Server) handleLoopTemplates(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, orch.LoopTemplates())
}

func (s *Server) handleLoops(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.orch.Loops.List(r.PathValue("id")))
}

func (s *Server) handleLoopCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.projects.Get(r.PathValue("id"))
	if !ok {
		s.orchErr(w, orch.ErrProjectNotFound)
		return
	}
	var d orch.LoopDef
	if !decode(w, r, &d) {
		return
	}
	d.ProjectID, d.CreatedBy = pr.ID, "user"
	if d.WorkspaceID == "" && len(pr.Workspaces) > 0 {
		d.WorkspaceID = pr.Workspaces[0].ID
	}
	if ws, wp, ok := s.projects.Workspace(d.WorkspaceID); !ok || wp.ID != pr.ID {
		s.orchErr(w, orch.ErrWorkspaceNotFound)
		return
	} else {
		d.WorkspaceID = ws.ID
	}
	l, err := s.orch.Loops.Create(d, time.Now(), false)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (s *Server) handleLoopPatch(w http.ResponseWriter, r *http.Request, p *principal) {
	var patch map[string]json.RawMessage
	if !decode(w, r, &patch) {
		return
	}
	l, err := s.orch.Loops.Patch(r.PathValue("id"), patch, time.Now())
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) handleLoopDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.orch.Loops.Delete(r.PathValue("id")); err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleLoopAction(w http.ResponseWriter, r *http.Request, p *principal) {
	id, now := r.PathValue("id"), time.Now()
	var l orch.LoopDef
	var err error
	switch {
	case strings.HasSuffix(r.URL.Path, "/start"):
		l, err = s.orch.Loops.Start(id, now)
	case strings.HasSuffix(r.URL.Path, "/stop"):
		l, err = s.orch.Loops.Stop(id, "user", now)
	default:
		l, err = s.orch.Loops.RunNow(id, now)
	}
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) handleLoopsStopAll(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, map[string]int{"stopped": s.orch.Loops.StopAll(time.Now())})
}

func (s *Server) handleLoopHistory(w http.ResponseWriter, r *http.Request, p *principal) {
	if _, ok := s.orch.Loops.Get(r.PathValue("id")); !ok {
		s.orchErr(w, orch.ErrLoopNotFound)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.orch.Loops.History(r.PathValue("id"), limit)})
}

// ---- memory (§12) ----

func (s *Server) memoryProject(w http.ResponseWriter, r *http.Request) (project.Project, bool) {
	pr, ok := s.projects.Get(r.PathValue("id"))
	if !ok {
		s.orchErr(w, orch.ErrProjectNotFound)
	}
	return pr, ok
}

func (s *Server) handleMemoryGet(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.memoryProject(w, r)
	if !ok {
		return
	}
	v, err := s.orch.ReadMemory(pr.ID)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleMemoryPut(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.memoryProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Scope   string `json:"scope"`
		Content string `json:"content"`
		IfMtime string `json:"ifMtime"`
	}
	if !decode(w, r, &req) {
		return
	}
	d, err := s.orch.WriteMemory(pr.ID, req.Scope, req.Content, req.IfMtime)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleMemoryScan(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.memoryProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Effort string `json:"effort"`
		Agent  string `json:"agent"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	j, err := s.orch.ScanMemory(pr.ID, req.Effort, req.Agent)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) handleMemoryAccept(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.memoryProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Scope string      `json:"scope"`
		Facts []orch.Fact `json:"facts"`
	}
	if !decode(w, r, &req) {
		return
	}
	d, err := s.orch.AcceptMemory(pr.ID, req.Scope, req.Facts)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleMemoryRefresh(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.memoryProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Fact string `json:"fact"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Fact) == "" {
		writeErrCode(w, http.StatusBadRequest, "empty_text", "fact kosong")
		return
	}
	j, err := s.orch.RefreshMemory(pr.ID, req.Fact)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) handleMemoryPush(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, req, ok := s.memorySendRequest(w, r)
	if !ok {
		return
	}
	res, err := s.orch.SendMemory(pr.ID, req)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleMemoryPlan says what the same request to memory/push would do.
func (s *Server) handleMemoryPlan(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, req, ok := s.memorySendRequest(w, r)
	if !ok {
		return
	}
	res, err := s.orch.PlanMemory(pr.ID, req)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) memorySendRequest(w http.ResponseWriter, r *http.Request) (project.Project, orch.MemorySend, bool) {
	pr, ok := s.memoryProject(w, r)
	if !ok {
		return pr, orch.MemorySend{}, false
	}
	var req struct {
		MemoryNodeID string             `json:"memoryNodeId"`
		WorkspaceID  string             `json:"workspaceId"`
		Targets      []string           `json:"targets"`
		Scopes       []string           `json:"scopes"`
		Blocks       []orch.MemoryBlock `json:"blocks"`
		Force        bool               `json:"force"`
	}
	if !decode(w, r, &req) {
		return pr, orch.MemorySend{}, false
	}
	wid := req.WorkspaceID
	if req.MemoryNodeID != "" && wid == "" {
		// find the workspace whose board holds the memory node
		for _, ws := range pr.Workspaces {
			if b, err := s.boards.Get(ws.ID); err == nil {
				for _, n := range b.Nodes {
					if n.ID == req.MemoryNodeID {
						wid = ws.ID
					}
				}
			}
		}
	}
	return pr, orch.MemorySend{WorkspaceID: wid, MemoryNodeID: req.MemoryNodeID, Targets: req.Targets, Scopes: req.Scopes, Blocks: req.Blocks, Force: req.Force}, true
}

// handleTilePush sends a content tile to consoles (§1): the given target nodes, or
// without them everything it is wired to.
func (s *Server) handleTilePush(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Targets []string `json:"targets"`
	}
	if !decode(w, r, &req) {
		return
	}
	res, err := s.orch.PushTile(r.PathValue("id"), r.PathValue("nodeId"), req.Targets)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- layouts (§13) ----

func (s *Server) handleLayouts(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.orch.Layouts.All())
}

func (s *Server) handleLayoutCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name        string   `json:"name"`
		WorkspaceID string   `json:"workspaceId"`
		NodeIDs     []string `json:"nodeIds"`
	}
	if !decode(w, r, &req) {
		return
	}
	ws, _, ok := s.projects.Workspace(req.WorkspaceID)
	if !ok {
		s.orchErr(w, orch.ErrWorkspaceNotFound)
		return
	}
	b, err := s.boards.Get(ws.ID)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	sessions := map[string]session.Info{}
	for _, in := range s.sessions.List() {
		sessions[in.ID] = in
	}
	l := orch.LayoutFromBoard(b, req.NodeIDs, sessions)
	l.Name = req.Name
	saved, err := s.orch.Layouts.Add(l, time.Now())
	if err != nil {
		s.orchErr(w, err)
		return
	}
	s.events.Publish(map[string]any{"type": "layouts"})
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) handleLayoutPatch(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	l, err := s.orch.Layouts.Rename(r.PathValue("id"), req.Name)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	s.events.Publish(map[string]any{"type": "layouts"})
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) handleLayoutDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.orch.Layouts.Delete(r.PathValue("id")); err != nil {
		s.orchErr(w, err)
		return
	}
	s.events.Publish(map[string]any{"type": "layouts"})
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request, p *principal) {
	ws, _, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var req orch.DeployRequest
	if !decode(w, r, &req) {
		return
	}
	res, err := s.orch.Deploy(ws.ID, req)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleMemoryInit(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.memoryProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Effort string `json:"effort"`
		Agent  string `json:"agent"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	j, err := s.orch.InitMemory(pr.ID, req.Effort, req.Agent)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) handleMemoryInitApply(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.memoryProject(w, r)
	if !ok {
		return
	}
	var req orch.InitResult
	if !decode(w, r, &req) {
		return
	}
	if err := s.orch.ApplyInit(pr.ID, req); err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.orch.Galaxy())
}

// A person's search shows more than an agent's.
const (
	memorySearchMax   = 20
	memorySearchChars = 40000
)

func (s *Server) handleMemoryGalaxy(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.orch.Galaxy())
}

func (s *Server) handleMemorySearch(w http.ResponseWriter, r *http.Request, p *principal) {
	pid, ok := s.memoryOwner(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > memorySearchMax {
		limit = memorySearchMax
	}
	q := r.URL.Query().Get("q")
	var hits []orch.MemoryHit
	if pid == "" {
		hits = s.orch.SearchAllMemory(q, limit, memorySearchChars)
	} else {
		hits = s.orch.SearchMemory(pid, q, limit, memorySearchChars)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

// memoryOwner reads ?projectId=; none means the memory of all projects.
func (s *Server) memoryOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	pid := r.URL.Query().Get("projectId")
	if pid == "" {
		return "", true
	}
	if _, ok := s.projects.Get(pid); !ok {
		s.orchErr(w, orch.ErrProjectNotFound)
		return "", false
	}
	return pid, true
}

func (s *Server) handleMemoryDocGet(w http.ResponseWriter, r *http.Request, p *principal) {
	pid, ok := s.memoryOwner(w, r)
	if !ok {
		return
	}
	d, err := s.orch.ReadDoc(pid, r.PathValue("name"))
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleMemoryDocPut(w http.ResponseWriter, r *http.Request, p *principal) {
	pid, ok := s.memoryOwner(w, r)
	if !ok {
		return
	}
	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Content     string `json:"content"`
		IfMtime     string `json:"ifMtime"`
	}
	if !decode(w, r, &req) {
		return
	}
	d, err := s.orch.WriteDoc(pid, r.PathValue("name"), req.Title, req.Description, req.Content, req.IfMtime)
	if err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleMemoryDocDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	pid, ok := s.memoryOwner(w, r)
	if !ok {
		return
	}
	if err := s.orch.DeleteDoc(pid, r.PathValue("name")); err != nil {
		s.orchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}
