package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/review"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/studio"
)

// Review of what agents changed, and the local history (docs/API-studio.md §7.10, §7.11).

const reviewRootsFor = time.Minute

type reviewRoots struct {
	at    time.Time
	roots []review.Root
}

// reviewState caches the work trees of a project, since a scan reads every repository.
type reviewState struct {
	mu    sync.Mutex
	cache map[string]reviewRoots
}

func (s *Server) setupReview() {
	rs := &reviewState{cache: map[string]reviewRoots{}}
	s.review = review.New(review.Config{
		Dir:     s.st.Path("review"),
		Roots:   func(pid string) []review.Root { return s.reviewRootsOf(rs, pid) },
		Publish: s.events.Publish,
	})
	onChange := s.sessions.OnChange
	s.sessions.OnChange = func(in session.Info) {
		if onChange != nil {
			onChange(in)
		}
		if in.ProjectID == "" {
			return
		}
		working := in.Foreground == agentstate.FgAgent && (in.AgentState == agentstate.Working || in.AgentState == agentstate.NeedsYou)
		name := "@" + in.Handle
		if in.Handle == "" {
			name = in.Name
		}
		s.review.SessionChanged(in.ProjectID, in.ID, name, in.Preset, working, in.StateSource == agentstate.SrcHooks)
	}
	s.sessions.OnFileTool = func(sp session.Spec, path string) {
		s.review.Hint(sp.ProjectID, sp.ID, path)
	}
	onRemove := s.sessions.OnRemove
	s.sessions.OnRemove = func(sp session.Spec) {
		if onRemove != nil {
			onRemove(sp)
		}
		s.review.SessionChanged(sp.ProjectID, sp.ID, "", "", false, false)
	}
}

func (s *Server) reviewRootsOf(rs *reviewState, pid string) []review.Root {
	rp, ok := s.gitRepo(pid)
	if !ok || rp.MachineID != machine.LocalID {
		return nil
	}
	rs.mu.Lock()
	c, ok := rs.cache[pid]
	rs.mu.Unlock()
	if ok && time.Since(c.at) < reviewRootsFor {
		return c.roots
	}
	root := filepath.Clean(rp.Path)
	// The default Home project is the whole home folder: ~/.ssh, every login, other projects.
	// A snapshot would copy all of that into the shadow repository, so such a project takes no part.
	if studio.Inside(homeDir(), root) {
		return nil
	}
	roots := []review.Root{{Path: root}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if list, err := s.gitPrev.git.Repos(ctx, rp); err == nil {
		for _, it := range list.Items {
			if it.Path != "" {
				roots = append(roots, review.Root{Rel: it.Path, Path: filepath.Join(root, filepath.FromSlash(it.Path))})
			}
		}
	}
	rs.mu.Lock()
	rs.cache[pid] = reviewRoots{at: time.Now(), roots: roots}
	rs.mu.Unlock()
	return roots
}

func (s *Server) routesReview(authed func(string, handler)) {
	authed("GET /api/projects/{id}/review", s.handleReview)
	authed("GET /api/projects/{id}/review/file", s.handleReviewFile)
	authed("POST /api/projects/{id}/review/accept", s.handleReviewAccept)
	authed("POST /api/projects/{id}/review/reject", s.handleReviewReject)
	authed("GET /api/projects/{id}/history", s.handleLocalHistory)
	authed("GET /api/projects/{id}/history/version", s.handleLocalVersion)
}

func reviewErr(w http.ResponseWriter, err error) {
	var re *review.Error
	if errors.As(err, &re) {
		writeErrCode(w, re.Status, re.Code, re.Msg)
		return
	}
	writeErrCode(w, http.StatusBadGateway, "review_failed", err.Error())
}

func (s *Server) reviewProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if _, ok := s.projects.Get(id); !ok {
		writeErrCode(w, http.StatusNotFound, "project_not_found", project.ErrNotFound.Error())
		return "", false
	}
	return id, true
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request, p *principal) {
	if id, ok := s.reviewProject(w, r); ok {
		writeJSON(w, http.StatusOK, s.review.List(id))
	}
}

func (s *Server) handleReviewFile(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.reviewProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	v, err := s.review.File(id, q.Get("session"), q.Get("path"))
	if err != nil {
		reviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleReviewAccept(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.reviewProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Session string  `json:"session"`
		Path    string  `json:"path"`
		Base    *string `json:"base"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.review.Accept(id, req.Session, req.Path, req.Base); err != nil {
		reviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleReviewReject(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.reviewProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Session string  `json:"session"`
		Path    string  `json:"path"`
		Text    *string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.review.Reject(id, req.Session, req.Path, req.Text); err != nil {
		reviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleLocalHistory(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.reviewProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, err := s.review.History(id, q.Get("path"), limit)
	if err != nil {
		reviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleLocalVersion(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.reviewProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	text, binary, err := s.review.Version(id, q.Get("path"), q.Get("commit"))
	if err != nil {
		reviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": text, "binary": binary})
}
