package server

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/chats"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/transcript"
)

// Agent chats REST (docs/API-agents.md §29).

func (s *Server) chatRoutes(authed func(string, handler)) {
	authed("GET /api/projects/{id}/chats", s.handleProjectChats)
	authed("GET /api/chats/{id}", s.handleChatGet)
	authed("GET /api/chats/{id}/transcript", s.handleChatTranscript)
	authed("PATCH /api/chats/{id}", s.handleChatPatch)
	authed("POST /api/chats/{id}/resume", s.handleChatResume)
	authed("POST /api/chats/{id}/continue", s.handleChatContinue)
}

const (
	chatListMax     = 500
	chatSearchFiles = 40 // transcripts a search looks into
	chatReplyMax    = 600
)

// chatEntry resolves {id}, or answers 404.
func (s *Server) chatEntry(w http.ResponseWriter, r *http.Request) (chats.Entry, project.Project, bool) {
	if s.chats == nil {
		writeErrCode(w, http.StatusServiceUnavailable, "chats_unavailable", "the chat index could not be opened")
		return chats.Entry{}, project.Project{}, false
	}
	e, ok := s.chats.idx.Entry(r.PathValue("id"))
	if !ok {
		writeErrCode(w, http.StatusNotFound, "chat_not_found", chats.ErrNotFound.Error())
		return e, project.Project{}, false
	}
	p, _ := s.projects.Get(e.ProjectID)
	return e, p, true
}

// handleProjectChats lists a project's chats, newest first: ?agent=, ?place=root|<partId>,
// ?q= (the title, and the start and end of the newest transcripts), ?status=
// (live|asleep|closed|orphan|archived; archived ones only with archived), ?limit= (≤ 500).
func (s *Server) handleProjectChats(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, project.ErrNotFound.Error())
		return
	}
	if s.chats == nil {
		writeErrCode(w, http.StatusServiceUnavailable, "chats_unavailable", "the chat index could not be opened")
		return
	}
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > chatListMax {
			writeErr(w, http.StatusBadRequest, "limit must be 1–500")
			return
		}
		limit = n
	}
	s.chatBackfill(pr)
	agentID, place, status := q.Get("agent"), q.Get("place"), q.Get("status")
	text := strings.ToLower(strings.TrimSpace(q.Get("q")))
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	searched := 0
	items := []chatView{}
	for _, e := range s.chats.idx.List(pr.ID) {
		if e.Messages == 0 { // never began: nothing to resume
			continue
		}
		if (agentID != "" && e.Agent != agentID) || (place == "root" && !e.Root) || (place != "" && place != "root" && e.PartID != place) {
			continue
		}
		if e.Archived != (status == "archived") {
			continue
		}
		v := s.chatViewOf(pr, e)
		if status != "" && status != "archived" && v.Status != status {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(e.Title), text) {
			if searched >= chatSearchFiles || ctx.Err() != nil {
				continue
			}
			searched++
			h, kind, tp, ok := s.chatTranscript(ctx, e.Chat)
			if !ok || !transcript.Contains(ctx, h, kind, tp, text) {
				continue
			}
		}
		items = append(items, v)
		if len(items) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// chatDetail is GET /api/chats/{id}.
type chatDetail struct {
	chatView
	LastReply   string           `json:"lastReply,omitempty"`
	HandleTaken *handleTakenView `json:"handleTaken"`
	Changes     *chats.Changes   `json:"changes,omitempty"`
}

func (s *Server) handleChatGet(w http.ResponseWriter, r *http.Request, p *principal) {
	e, pr, ok := s.chatEntry(w, r)
	if !ok {
		return
	}
	v := s.chatViewOf(pr, e)
	d := chatDetail{chatView: v, HandleTaken: s.chatHandleTaken(v)}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if h, kind, tp, ok := s.chatTranscript(ctx, e.Chat); ok {
		if rep, err := transcript.ReadReply(ctx, h, kind, tp); err == nil {
			d.LastReply = strings.TrimSpace(rep.Text)
			if rr := []rune(d.LastReply); len(rr) > chatReplyMax {
				d.LastReply = strings.TrimSpace(string(rr[:chatReplyMax-1])) + "…"
			}
		}
	}
	if r.URL.Query().Get("changes") == "1" {
		d.Changes = s.chatChanges(ctx, e.Chat)
	}
	writeJSON(w, http.StatusOK, d)
}

// handleChatTranscript is GET /api/sessions/{id}/transcript for a chat: ?last= (or ?limit=) 1–200.
func (s *Server) handleChatTranscript(w http.ResponseWriter, r *http.Request, p *principal) {
	e, _, ok := s.chatEntry(w, r)
	if !ok {
		return
	}
	last := 50
	for _, k := range []string{"last", "limit"} {
		if v := r.URL.Query().Get(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 200 {
				writeErr(w, http.StatusBadRequest, k+" must be 1–200")
				return
			}
			last = n
			break
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	h, kind, tp, ok := s.chatTranscript(ctx, e.Chat)
	if !ok {
		writeErrCode(w, http.StatusNotFound, "no_transcript", "the transcript of this chat was not found")
		return
	}
	msgs, truncated, err := transcript.ReadMessages(ctx, h, kind, tp, last)
	if err != nil {
		fsErr(w, err)
		return
	}
	if msgs == nil {
		msgs = []transcript.Message{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agentSessionId": e.AgentSessionID, "messages": msgs, "truncated": truncated})
}

func (s *Server) handleChatPatch(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
	}
	if !decode(w, r, &req) {
		return
	}
	e, pr, ok := s.chatEntry(w, r)
	if !ok {
		return
	}
	if _, err := s.chats.idx.Patch(e.ID, req.Title, req.Archived); err != nil {
		writeErrCode(w, http.StatusBadRequest, "bad_title", err.Error())
		return
	}
	e, _ = s.chats.idx.Entry(e.ID)
	v := s.chatViewOf(pr, e)
	s.events.Publish(map[string]any{"type": "chat", "chat": v})
	writeJSON(w, http.StatusOK, v)
}

// chatWorkspace is the board a chat resumes on: the one asked for, which must be of the chat's
// project, else the one it was on, else the project's first.
func (s *Server) chatWorkspace(w http.ResponseWriter, pr project.Project, asked, had string) (project.Workspace, bool) {
	if asked != "" {
		ws, wp, ok := s.projects.Workspace(asked)
		if !ok {
			writeErr(w, http.StatusNotFound, project.ErrWorkspaceNotFound.Error())
			return ws, false
		}
		if wp.ID != pr.ID {
			writeErrCode(w, http.StatusBadRequest, "other_project", "that board is not of the chat's project")
			return ws, false
		}
		return ws, true
	}
	if ws, wp, ok := s.projects.Workspace(had); ok && wp.ID == pr.ID {
		return ws, true
	}
	if len(pr.Workspaces) == 0 {
		writeErr(w, http.StatusNotFound, project.ErrWorkspaceNotFound.Error())
		return project.Workspace{}, false
	}
	return pr.Workspaces[0], true
}

// claimChat keeps two requests from resuming one chat at once.
func (s *Server) claimChat(id string) bool {
	s.chats.mu.Lock()
	defer s.chats.mu.Unlock()
	if s.chats.resuming[id] {
		return false
	}
	s.chats.resuming[id] = true
	return true
}

func (s *Server) unclaimChat(id string) {
	s.chats.mu.Lock()
	delete(s.chats.resuming, id)
	s.chats.mu.Unlock()
}

// handleTakenBody is 409 handle_taken: who has the handle, the chat it has, the next free name.
func (s *Server) handleTakenBody(c chats.Chat, handle string) map[string]any {
	body := map[string]any{"error": "@" + handle + " is used by another console", "code": "handle_taken", "taken": handle,
		"by": "", "byChat": nil, "next": s.sessions.NextHandle(), "swappable": false}
	if by := s.sessions.HandleHolder(handle); by != "" {
		body["by"] = by
		if in, ok := s.sessions.Info(by); ok {
			body["byHandle"] = in.Handle
			reason := chats.SwapReason(c, in)
			body["swappable"] = reason == ""
			if reason != "" {
				body["reason"] = reason
			}
		}
		if id := s.chats.idx.HeldBy(by); id != "" {
			if bc, _, ok := s.chats.idx.Get(id); ok && bc.Title != "" {
				body["byChat"] = bc.Title
			}
		}
	}
	return body
}

// chatResult is what resume and continue answer: the console, as POST /api/sessions does, and
// the chat it has; missionId when the chat's mission still runs (it does not rejoin it).
type chatResult struct {
	session.Info
	ChatID    string `json:"chatId"`
	MissionID string `json:"missionId,omitempty"`
}

// handleChatResume opens a chat again in the folder it was started in: a new console at its
// place (same agent, login, part or root, machine and folder, mode resume with its id), or the
// console `into` (idle, same agent and folder) switched to it.
func (s *Server) handleChatResume(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		WorkspaceID string   `json:"workspaceId"`
		Into        string   `json:"into"`
		Handle      string   `json:"handle"`
		TellChanges *bool    `json:"tellChanges"`
		Panel       bool     `json:"panel"`
		Card        string   `json:"card"`
		X           *float64 `json:"x"`
		Y           *float64 `json:"y"`
	}
	if !decode(w, r, &req) {
		return
	}
	e, pr, ok := s.chatEntry(w, r)
	if !ok {
		return
	}
	if pr.ID == "" {
		writeErr(w, http.StatusNotFound, project.ErrNotFound.Error())
		return
	}
	if !s.claimChat(e.ID) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this chat is being resumed", "code": "chat_live", "sessionId": ""})
		return
	}
	defer s.unclaimChat(e.ID)
	// read again under the claim: a resume that finished meanwhile gave it a console
	if e, ok = s.chats.idx.Entry(e.ID); !ok {
		writeErrCode(w, http.StatusNotFound, "chat_not_found", chats.ErrNotFound.Error())
		return
	}
	v := s.chatViewOf(pr, e)
	switch v.Status {
	case chats.StatusLive, chats.StatusAsleep:
		writeJSON(w, http.StatusConflict, map[string]any{"error": "a console has this chat open", "code": "chat_live", "sessionId": v.SessionID})
		return
	case chats.StatusOrphan:
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this chat cannot resume where it started", "code": "chat_orphan", "reason": v.OrphanReason})
		return
	}
	ws, ok := s.chatWorkspace(w, pr, req.WorkspaceID, e.WorkspaceID)
	if !ok {
		return
	}
	a, _ := s.chatKind(e.Chat)
	if a.ID == "" || a.Kind != agent.KindAgent {
		writeErrCode(w, http.StatusConflict, "unknown_agent", "the chat's agent is no longer in the catalog")
		return
	}
	tell := s.settings.View().Agents.ResumeTellChanges
	if req.TellChanges != nil {
		tell = *req.TellChanges
	}
	mode, sid := agent.ModeResume, e.AgentSessionID
	if e.Imprecise || sid == "" {
		mode, sid = agent.ModeContinue, ""
	}
	t0 := time.Now()
	var in session.Info
	if req.Into != "" {
		target, ok := s.sessions.Info(req.Into)
		if !ok {
			writeErr(w, http.StatusNotFound, session.ErrNotFound.Error())
			return
		}
		if reason := chats.SwapReason(e.Chat, target); reason != "" {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "that console cannot take this chat", "code": "not_swappable",
				"reason": reason, "sessionId": req.Into})
			return
		}
		var err error
		if target.AgentState == agentstate.Shell || target.AgentState == agentstate.Exited {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			in, err = s.sessions.StartAgent(ctx, req.Into, mode, sid) // §3.3, from its shell
			cancel()
		} else {
			in, err = s.sessions.Restart(req.Into, session.RestartRequest{Mode: mode, AgentSessionID: sid})
		}
		if err != nil {
			s.sessionErr(w, err)
			return
		}
	} else {
		handle, clash := chats.PickHandle(e.Handle, req.Handle, s.sessions.HandleTaken)
		if clash {
			writeJSON(w, http.StatusConflict, s.handleTakenBody(e.Chat, e.Handle))
			return
		}
		cr := session.CreateRequest{Agent: e.Agent, Handle: handle, ProfileID: &e.ProfileID, Panel: req.Panel, Card: req.Card,
			Launch: &session.LaunchRequest{Mode: mode, AgentSessionID: sid}}
		if e.ProviderID != "" {
			pv := e.ProviderID
			cr.ProviderID = &pv
		}
		// exactly the folder it ran in: the root folder, or the part's machine and folder (an
		// agent for a server's folder, or a read-only part's, runs in that folder's work dir)
		switch {
		case e.Root:
			cr.Root = true
		default:
			cr.PartID = e.PartID
			cr.MachineID, cr.Cwd = firstSet(e.RemoteMachineID, e.MachineID), firstSet(e.RemoteCwd, e.Cwd)
		}
		if err := s.placeSession(pr, &cr); err != nil {
			placeErr(w, err)
			return
		}
		if e.Root && path.Clean(cr.Cwd) != path.Clean(e.Cwd) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "the project's root folder moved", "code": "chat_orphan", "reason": chats.OrphanFolderMissing})
			return
		}
		cr.ProjectID, cr.WorkspaceID = pr.ID, ws.ID
		var err error
		in, err = s.sessions.Create(cr)
		if errors.Is(err, session.ErrHandleTaken) {
			writeJSON(w, http.StatusConflict, s.handleTakenBody(e.Chat, handle))
			return
		}
		if err != nil {
			s.sessionErr(w, err)
			return
		}
		if !cr.Panel {
			var rect *board.Rect
			if req.X != nil && req.Y != nil {
				rect = &board.Rect{X: *req.X, Y: *req.Y, W: board.TermW, H: board.TermH}
			}
			if _, err := s.boards.AddTerminal(ws.ID, in.ID, rect); err != nil {
				_ = s.sessions.Delete(in.ID)
				s.boardErr(w, err)
				return
			}
		}
		s.emitMachine(in.MachineID)
	}
	// the console has the chat from now on (its OnChange may have said so already)
	changed, _ := s.chats.idx.Hold(s.holdingOf(in, a, true))
	s.publishChats(changed)
	if e.Handle != "" && !strings.EqualFold(e.Handle, in.Handle) && s.orch != nil {
		s.orch.Threads.Add(orch.ThreadMessage{ThreadID: pr.ID, Role: "hub", Kind: "note", SessionID: in.ID,
			Text: "Chat “" + v.Title + "” (was @" + e.Handle + ") is now in @" + in.Handle + "."})
	}
	if tell {
		go s.tellChatChanges(e.Chat, in.ID, t0)
	}
	res := chatResult{Info: in, ChatID: e.ID}
	if e.MissionID != "" && s.orch != nil {
		if m, ok := s.orch.Missions.Get(e.MissionID); ok && m.Live() {
			res.MissionID = m.ID
		}
	}
	writeJSON(w, http.StatusCreated, res)
}

// handleChatContinue continues a chat at another place (any chat, an orphan's too): a fresh
// console there, whose first order is a summary of the chat (as a handoff's brief).
func (s *Server) handleChatContinue(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		WorkspaceID string `json:"workspaceId"`
		Place       struct {
			PartID string `json:"partId"`
			Root   bool   `json:"root"`
		} `json:"place"`
		Agent string `json:"agent"`
	}
	if !decode(w, r, &req) {
		return
	}
	e, pr, ok := s.chatEntry(w, r)
	if !ok {
		return
	}
	if pr.ID == "" {
		writeErr(w, http.StatusNotFound, project.ErrNotFound.Error())
		return
	}
	if !req.Place.Root && strings.TrimSpace(req.Place.PartID) == "" {
		writeErrCode(w, http.StatusBadRequest, "bad_placement", "place needs a part or the root")
		return
	}
	ws, ok := s.chatWorkspace(w, pr, req.WorkspaceID, e.WorkspaceID)
	if !ok {
		return
	}
	agentID := firstSet(req.Agent, e.Agent)
	a, ok := s.agents.Get(agentID)
	if !ok || a.Kind != agent.KindAgent {
		writeErrCode(w, http.StatusBadRequest, "unknown_agent", session.ErrUnknownPreset.Error())
		return
	}
	cr := session.CreateRequest{Agent: agentID, Root: req.Place.Root, PartID: req.Place.PartID}
	if agentID == e.Agent { // the same account
		cr.ProfileID = &e.ProfileID
		if e.ProviderID != "" {
			pv := e.ProviderID
			cr.ProviderID = &pv
		}
	}
	if err := s.placeSession(pr, &cr); err != nil {
		placeErr(w, err)
		return
	}
	cr.ProjectID, cr.WorkspaceID = pr.ID, ws.ID
	t0 := time.Now()
	in, err := s.sessions.Create(cr)
	if err != nil {
		s.sessionErr(w, err)
		return
	}
	if _, err := s.boards.AddTerminal(ws.ID, in.ID, nil); err != nil {
		_ = s.sessions.Delete(in.ID)
		s.boardErr(w, err)
		return
	}
	s.emitMachine(in.MachineID)
	title := e.Title
	if title == "" {
		title = "Untitled chat"
	}
	c := e.Chat
	c.Title = title
	go s.continueChat(c, in.ID, in.Handle, t0)
	writeJSON(w, http.StatusCreated, chatResult{Info: in, ChatID: e.ID})
}
