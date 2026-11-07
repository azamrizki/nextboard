package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/transcript"
)

// handleSessionAgent is §3.3: start/resume the agent from the idle shell
// of a wrapped console without a full restart.
func (s *Server) handleSessionAgent(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Mode           string `json:"mode"`
		AgentSessionID string `json:"agentSessionId"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	in, err := s.sessions.StartAgent(ctx, r.PathValue("id"), req.Mode, req.AgentSessionID)
	if err != nil {
		s.sessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) sessionByPath(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	sess, ok := s.sessions.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, session.ErrNotFound.Error())
	}
	return sess, ok
}

func (s *Server) handleSessionReply(w http.ResponseWriter, r *http.Request, p *principal) {
	sess, ok := s.sessionByPath(w, r)
	if !ok {
		return
	}
	rep, ok := sess.Reply()
	if !ok {
		writeErrCode(w, http.StatusNotFound, "no_reply", transcript.ErrNoReply.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handleSessionReplySave writes the last reply to
// <project>/<dir>/<YYYY-MM-DD-HHMM>-<handle>.md (explicit user action;
// unique name, never overwrites).
func (s *Server) handleSessionReplySave(w http.ResponseWriter, r *http.Request, p *principal) {
	sess, ok := s.sessionByPath(w, r)
	if !ok {
		return
	}
	dir, ok := replySaveDir(w, r)
	if !ok {
		return
	}
	rep, ok := sess.Reply()
	if !ok {
		writeErrCode(w, http.StatusNotFound, "no_reply", transcript.ErrNoReply.Error())
		return
	}
	sp := sess.Spec()
	handle := sp.Handle
	if handle == "" {
		handle = sp.ID
	}
	s.saveReplyFile(w, r, sp.ProjectID, dir, handle, rep.Text, time.Now())
}

// replySaveDir reads the folder a reply is saved to ("outputs" unless the request names one).
func replySaveDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Dir string `json:"dir"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return "", false
	}
	dir := strings.Trim(strings.TrimSpace(req.Dir), "/")
	if dir == "" {
		dir = "outputs"
	}
	if strings.Contains(dir, "..") || strings.ContainsAny(dir, "\x00\\") || len(dir) > 200 {
		writeErr(w, http.StatusBadRequest, "dir must be a relative folder inside the project")
		return "", false
	}
	return dir, true
}

// saveReplyFile writes a reply to <project>/<dir>/<YYYY-MM-DD-HHMM>-<handle>.md.
func (s *Server) saveReplyFile(w http.ResponseWriter, r *http.Request, projectID, dir, handle, text string, at time.Time) {
	pr, ok := s.projects.Get(projectID)
	if !ok {
		writeErr(w, http.StatusNotFound, project.ErrNotFound.Error())
		return
	}
	h, ok := s.machines.Host(pr.MachineID)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fsTimeout)
	defer cancel()
	folder := pr.Path
	for _, part := range strings.Split(dir, "/") {
		if part == "" || part == "." {
			continue
		}
		folder = path.Join(folder, part)
		if err := h.Mkdir(ctx, folder); err != nil {
			fsErr(w, err)
			return
		}
	}
	name := at.Format("2006-01-02-1504") + "-" + handle + ".md"
	body := fmt.Sprintf("<!-- %s · @%s -->\n\n%s\n", at.Format(time.RFC3339), handle, text)
	if s.hubPrivate(w, h, path.Join(folder, name)) {
		return
	}
	st, err := h.WriteFile(ctx, path.Join(folder, name), []byte(body), machine.WriteOptions{Unique: true})
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// transcriptOf resolves the session's transcript or answers 404.
func (s *Server) transcriptOf(w http.ResponseWriter, r *http.Request) (*session.Session, machine.Host, string, string, bool) {
	sess, ok := s.sessionByPath(w, r)
	if !ok {
		return nil, nil, "", "", false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	h, kind, p, err := sess.TranscriptLocation(ctx)
	if err != nil || p == "" {
		writeErrCode(w, http.StatusNotFound, "no_transcript", "transcript of this session not found")
		return nil, nil, "", "", false
	}
	return sess, h, kind, p, true
}

func (s *Server) handleSessionTranscript(w http.ResponseWriter, r *http.Request, p *principal) {
	last := 20
	if v := r.URL.Query().Get("last"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeErr(w, http.StatusBadRequest, "last must be 1–200")
			return
		}
		last = n
	}
	sess, h, kind, tp, ok := s.transcriptOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	msgs, truncated, err := transcript.ReadMessages(ctx, h, kind, tp, last)
	if err != nil {
		fsErr(w, err)
		return
	}
	if msgs == nil {
		msgs = []transcript.Message{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agentSessionId": sess.Spec().AgentSessionID, "messages": msgs, "truncated": truncated})
}

func (s *Server) handleSessionTokens(w http.ResponseWriter, r *http.Request, p *principal) {
	var since time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "since must be RFC 3339")
			return
		}
		since = t
	}
	_, h, kind, tp, ok := s.transcriptOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	items, err := transcript.ReadTokens(ctx, h, kind, tp, since)
	if err != nil {
		fsErr(w, err)
		return
	}
	if items == nil {
		items = []transcript.TokenUsage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleConversations is the resume picker list (§8.2).
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	q := r.URL.Query()
	a, ok := s.agents.Get(q.Get("agent"))
	if !ok || (a.Transcript != agent.TranscriptClaude && a.Transcript != agent.TranscriptCodex) {
		writeErrCode(w, http.StatusBadRequest, "no_transcript", "this agent has no transcript")
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeErr(w, http.StatusBadRequest, "limit must be 1–200")
			return
		}
		limit = n
	}
	cwd := strings.TrimSpace(q.Get("cwd"))
	if cwd == "" {
		writeErr(w, http.StatusBadRequest, "cwd is required")
		return
	}
	profileDir := ""
	if id := q.Get("profileId"); id != "" {
		pf, ok := s.profiles.Get(id)
		if !ok || pf.MachineID != h.ID() {
			writeErrCode(w, http.StatusNotFound, "profile_not_found", session.ErrProfileNotFound.Error())
			return
		}
		profileDir = pf.Dir
	}
	home := ""
	if pr := h.Info().Probe; pr != nil {
		home = pr.Home
	}
	if home == "" && h.ID() == machine.LocalID {
		home = homeDir()
	}
	dir := ""
	switch a.Transcript {
	case agent.TranscriptClaude:
		dir = transcript.ClaudeConfigDir(home, firstSet(profileDir, localEnvVar(h, "CLAUDE_CONFIG_DIR")))
	case agent.TranscriptCodex:
		dir = transcript.CodexHome(home, firstSet(profileDir, localEnvVar(h, "CODEX_HOME")))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	list, err := transcript.ListConversations(ctx, h, a.Transcript, dir, cwd, limit, func(id string) string {
		return s.sessions.AttachedTo(h.ID(), id)
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		fsErr(w, err)
		return
	}
	if list == nil {
		list = &transcript.ConversationList{Items: []transcript.Conversation{}}
	}
	for i := range list.Items {
		if at := list.Items[i].AttachedTo; at != nil {
			if in, ok := s.sessions.Info(*at); ok {
				hd := in.Handle
				list.Items[i].AttachedHandle = &hd
			}
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func firstSet(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
