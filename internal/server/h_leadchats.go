package server

import (
	"errors"
	"net/http"

	"github.com/azamrizki/nextboard/internal/orch"
)

// Lead chats (docs/API-orchestration.md §4.1): a thread with the lead is a list of chats.
// The thread's messages endpoint and its clear work on the active chat (h_orch.go).

func (s *Server) leadChatRoutes(authed func(string, handler)) {
	authed("GET /api/threads/{tid}/chats", s.handleLeadChats)
	authed("POST /api/threads/{tid}/chats", s.handleLeadChatNew)
	authed("POST /api/threads/{tid}/chats/{chatId}/activate", s.handleLeadChatActivate)
	authed("PATCH /api/threads/{tid}/chats/{chatId}", s.handleLeadChatPatch)
	authed("DELETE /api/threads/{tid}/chats/{chatId}", s.handleLeadChatDelete)
}

// leadChatErr maps the errors of lead chats; the rest go to orchErr's codes.
func leadChatErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, orch.ErrChatNotFound):
		writeErrCode(w, http.StatusNotFound, "chat_not_found", err.Error())
	case errors.Is(err, orch.ErrLeadBusy), errors.Is(err, orch.ErrResumeBusy), errors.Is(err, orch.ErrChatAnswering):
		writeErrCode(w, http.StatusConflict, "lead_busy", err.Error())
	case errors.Is(err, orch.ErrChatActive):
		writeErrCode(w, http.StatusConflict, "chat_active", err.Error())
	case errors.Is(err, orch.ErrChatMission):
		writeErrCode(w, http.StatusConflict, "chat_mission", err.Error())
	case errors.Is(err, orch.ErrBadChatTitle):
		writeErrCode(w, http.StatusBadRequest, "bad_title", err.Error())
	default:
		writeErrCode(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// chatIDOf reads {chatId}; an id of another shape is no chat of the thread.
func chatIDOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("chatId")
	if !orch.ValidChatID(id) {
		leadChatErr(w, orch.ErrChatNotFound)
		return "", false
	}
	return id, true
}

func (s *Server) handleLeadChats(w http.ResponseWriter, r *http.Request, p *principal) {
	tid, ok := s.threadID(w, r)
	if !ok {
		return
	}
	active, items := s.orch.Threads.Chats(tid)
	writeJSON(w, http.StatusOK, map[string]any{"active": active, "items": items})
}

func (s *Server) handleLeadChatNew(w http.ResponseWriter, r *http.Request, p *principal) {
	tid, ok := s.threadID(w, r)
	if !ok {
		return
	}
	ch, err := s.orch.NewLeadChat(tid)
	if err != nil {
		leadChatErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ch)
}

func (s *Server) handleLeadChatActivate(w http.ResponseWriter, r *http.Request, p *principal) {
	tid, ok := s.threadID(w, r)
	if !ok {
		return
	}
	id, ok := chatIDOf(w, r)
	if !ok {
		return
	}
	ch, err := s.orch.ActivateLeadChat(tid, id)
	if err != nil {
		leadChatErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) handleLeadChatPatch(w http.ResponseWriter, r *http.Request, p *principal) {
	tid, ok := s.threadID(w, r)
	if !ok {
		return
	}
	id, ok := chatIDOf(w, r)
	if !ok {
		return
	}
	var req struct {
		Title *string `json:"title"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Title == nil {
		ch, ok := s.orch.Threads.Chat(tid, id)
		if !ok {
			leadChatErr(w, orch.ErrChatNotFound)
			return
		}
		writeJSON(w, http.StatusOK, ch)
		return
	}
	ch, err := s.orch.Threads.RenameChat(tid, id, *req.Title)
	if err != nil {
		leadChatErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) handleLeadChatDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	tid, ok := s.threadID(w, r)
	if !ok {
		return
	}
	id, ok := chatIDOf(w, r)
	if !ok {
		return
	}
	if err := s.orch.DeleteLeadChat(tid, id); err != nil {
		leadChatErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}
