package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/nextcode"
	"github.com/azamrizki/nextboard/internal/session"
)

// The chat of a NextCode console (docs/API-agents.md §30): its history, the owner's messages
// and answers, and a WebSocket with everything live.

func (s *Server) nextcodeRoutes(authed func(string, handler)) {
	authed("GET /api/sessions/{id}/nextcode", s.handleNextCodeGet)
	authed("GET /api/sessions/{id}/nextcode/ws", s.handleNextCodeWS)
	authed("POST /api/sessions/{id}/nextcode/messages", s.handleNextCodeMessage)
	authed("POST /api/sessions/{id}/nextcode/requests/{rid}", s.handleNextCodeAnswer)
	authed("POST /api/sessions/{id}/nextcode/interrupt", s.handleNextCodeInterrupt)
	authed("PUT /api/sessions/{id}/nextcode/mode", s.handleNextCodeMode)
	authed("PUT /api/sessions/{id}/nextcode/model", s.handleNextCodeModel)
}

// nextcodeDriver is the driver of the console in the path, or writes why there is none.
func (s *Server) nextcodeDriver(w http.ResponseWriter, r *http.Request) (*session.Session, *nextcode.Driver, bool) {
	sess, ok := s.sessions.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, session.ErrNotFound.Error())
		return nil, nil, false
	}
	d := sess.Chat()
	if d == nil {
		if sess.IsNextCode() {
			writeErrCode(w, http.StatusConflict, "nextcode_not_running", "the console's chat is not open (it is starting or it failed)")
		} else {
			writeErrCode(w, http.StatusConflict, "not_nextcode", session.ErrNotNextCode.Error())
		}
		return nil, nil, false
	}
	return sess, d, true
}

func nextcodeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nextcode.ErrEmpty), errors.Is(err, nextcode.ErrBadMode):
		writeErrCode(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, nextcode.ErrTooLong):
		writeErrCode(w, http.StatusRequestEntityTooLarge, "too_long", err.Error())
	case errors.Is(err, nextcode.ErrNoRequest):
		writeErrCode(w, http.StatusNotFound, "request_gone", err.Error())
	case errors.Is(err, nextcode.ErrNotWorking):
		writeErrCode(w, http.StatusConflict, "not_working", err.Error())
	case errors.Is(err, nextcode.ErrClosed):
		writeErrCode(w, http.StatusConflict, "nextcode_closed", err.Error())
	default:
		writeErrCode(w, http.StatusBadGateway, "cli_failed", err.Error())
	}
}

// handleNextCodeGet is the chat: ?before=<item id> pages back, ?limit= 1–1000 (default 300).
func (s *Server) handleNextCodeGet(w http.ResponseWriter, r *http.Request, p *principal) {
	_, d, ok := s.nextcodeDriver(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, more := d.Items(r.URL.Query().Get("before"), limit)
	if items == nil {
		items = []nextcode.Item{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "more": more, "requests": d.Pending(), "status": d.Status()})
}

func (s *Server) handleNextCodeMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	_, d, ok := s.nextcodeDriver(w, r)
	if !ok {
		return
	}
	if err := d.Send(req.Text); err != nil {
		nextcodeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, okBody)
}

func (s *Server) handleNextCodeAnswer(w http.ResponseWriter, r *http.Request, p *principal) {
	var a nextcode.Answer
	if !decode(w, r, &a) {
		return
	}
	switch a.Decision {
	case nextcode.Allow, nextcode.AllowAlways, nextcode.Deny, nextcode.DenyStop:
	default:
		writeErrCode(w, http.StatusBadRequest, "bad_decision", "decision is allow, allowAlways, deny or denyStop")
		return
	}
	if len(a.Message) > nextcode.MaxMessage {
		writeErrCode(w, http.StatusRequestEntityTooLarge, "too_long", nextcode.ErrTooLong.Error())
		return
	}
	_, d, ok := s.nextcodeDriver(w, r)
	if !ok {
		return
	}
	if err := d.Answer(r.PathValue("rid"), a); err != nil {
		nextcodeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleNextCodeInterrupt(w http.ResponseWriter, r *http.Request, p *principal) {
	_, d, ok := s.nextcodeDriver(w, r)
	if !ok {
		return
	}
	if err := d.Interrupt(); err != nil {
		nextcodeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleNextCodeMode(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &req) {
		return
	}
	_, d, ok := s.nextcodeDriver(w, r)
	if !ok {
		return
	}
	if err := d.SetPermissionMode(req.Mode); err != nil {
		nextcodeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": d.Status()})
}

// handleNextCodeModel switches the console's model and effort: {"model"?, "effort"?}, a field
// left out is unchanged and "" is the CLI's own. The model switches at once; the effort from
// the CLI's next start.
func (s *Server) handleNextCodeModel(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Model  *string `json:"model"`
		Effort *string `json:"effort"`
		// Account: the login profile or API provider the model is on (One NextCode); another
		// than the console's moves the chat there.
		Account string `json:"account"`
	}
	if !decode(w, r, &req) {
		return
	}
	sess, d, ok := s.nextcodeDriver(w, r)
	if !ok {
		return
	}
	if req.Account != "" {
		if _, err := s.sessions.SwitchChatAccount(sess.ID(), req.Account, req.Model, req.Effort); err != nil {
			switch {
			case errors.Is(err, session.ErrBadRequest):
				writeErrCode(w, http.StatusBadRequest, "bad_model", "not a model or effort name")
			case errors.Is(err, session.ErrNextCodeAccount):
				writeErrCode(w, http.StatusBadRequest, "nextcode_account", err.Error())
			case errors.Is(err, session.ErrNotNextCode):
				writeErrCode(w, http.StatusConflict, "not_nextcode", err.Error())
			default:
				s.sessionErr(w, err)
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": d.Status()})
		return
	}
	if err := sess.SetChatModel(req.Model, req.Effort); err != nil {
		if errors.Is(err, session.ErrBadRequest) {
			writeErrCode(w, http.StatusBadRequest, "bad_model", "not a model or effort name")
			return
		}
		nextcodeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": d.Status()})
}

// handleNextCodeWS streams the chat: a "snapshot" frame first, then every change (nextcode.Frame
// as JSON text). A view too slow to keep up is closed with 1013 and reconnects.
func (s *Server) handleNextCodeWS(w http.ResponseWriter, r *http.Request, p *principal) {
	_, d, ok := s.nextcodeDriver(w, r)
	if !ok {
		return
	}
	if !s.checkOrigin(r, p) {
		writeErr(w, http.StatusForbidden, "origin not allowed")
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	conn := newWSConn(c)
	unregister := s.conns.add(p.connKey(), conn.revoke)
	defer unregister()

	snapshot, sub := d.Subscribe()
	defer d.Unsubscribe(sub)
	readerDone := make(chan struct{})
	go conn.readLoop(4<<10, nil, readerDone) // the view sends nothing but pongs

	b, _ := json.Marshal(snapshot)
	if conn.write(websocket.TextMessage, b) != nil {
		return
	}
	ping := time.NewTicker(wsPingEvery)
	defer ping.Stop()
	for {
		select {
		case f, ok := <-sub.C:
			if !ok {
				conn.closeWith(websocket.CloseTryAgainLater, "resubscribe")
				return
			}
			b, _ := json.Marshal(f)
			if conn.write(websocket.TextMessage, b) != nil {
				return
			}
		case <-ping.C:
			if conn.c.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)) != nil {
				return
			}
		case <-conn.kill:
			conn.closeWith(wsCloseRevok, "revoked")
			return
		case <-readerDone:
			return
		}
	}
}
