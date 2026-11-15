package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/gitsvc"
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
	mu      sync.Mutex
	cache   map[string]reviewRoots
	mirrors map[string]*review.Mirror
}

func (s *Server) setupReview() {
	rs := &reviewState{cache: map[string]reviewRoots{}, mirrors: map[string]*review.Mirror{}}
	s.review = review.New(review.Config{
		Dir:     s.st.Path("review"),
		Roots:   func(pid string) []review.Root { return s.reviewRootsOf(rs, pid) },
		Publish: s.events.Publish,
		Paused:  s.projectArchived,
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
		for _, k := range s.sessionReviews(in.Spec) {
			s.review.SessionChanged(k.key, in.ID, name, in.Preset, working && k.changes, in.StateSource == agentstate.SrcHooks)
		}
	}
	s.sessions.OnFileTool = func(sp session.Spec, path string) {
		if key, real, ok := s.fileToolReview(sp, path); ok {
			s.review.Hint(key, sp.ID, real)
		}
	}
	onRemove := s.sessions.OnRemove
	s.sessions.OnRemove = func(sp session.Spec) {
		if onRemove != nil {
			onRemove(sp)
		}
		for _, k := range s.sessionReviews(sp) {
			s.review.SessionChanged(k.key, sp.ID, "", "", false, false)
		}
	}
}

// sessionReview is one review a console's work goes to; changes: it may change that part with
// its own tools, so a turn of it is watched there.
type sessionReview struct {
	key     string
	changes bool
}

// sessionReviews are the reviews a console's turns go to: its part's; at the project root every
// part's, watched where it may change the part with its own tools (the parts on This PC that are
// not read only; its changes on a server come through the hub's tools, noteRemoteChange). A
// console in no part's folder has none.
func (s *Server) sessionReviews(sp session.Spec) []sessionReview {
	pr, ok := s.projects.Get(sp.ProjectID)
	if !ok {
		return nil
	}
	if sp.Root {
		out := make([]sessionReview, 0, len(pr.Parts))
		for _, pt := range pr.Parts {
			out = append(out, sessionReview{key: reviewKey(pr, pt.ID), changes: pt.MachineID == machine.LocalID && !pt.ReadOnly})
		}
		return out
	}
	if _, ok := pr.PartByID(sp.PartID); !ok {
		return nil
	}
	return []sessionReview{{key: reviewKey(pr, sp.PartID), changes: true}}
}

// fileToolReview is where a file a console's own tool named goes in review: the part that holds
// it and its real path. At the root a path through a part's link maps to that part's folder; a
// path in no part is dropped. In a part, a file in another part on its machine goes to that
// part, anything else to its own.
func (s *Server) fileToolReview(sp session.Spec, p string) (string, string, bool) {
	pr, ok := s.projects.Get(sp.ProjectID)
	if !ok {
		return "", "", false
	}
	if sp.Root {
		pt, real, ok := s.rootFilePart(sp, p)
		if !ok {
			return "", "", false
		}
		return reviewKey(pr, pt.ID), real, true
	}
	if filepath.IsAbs(p) && sp.RemoteMachineID == "" {
		if pt, ok := pr.PartAt(sp.MachineID, filepath.Clean(p)); ok && pt.ID != sp.PartID {
			return reviewKey(pr, pt.ID), p, true
		}
	}
	if _, ok := pr.PartByID(sp.PartID); !ok {
		return "", "", false
	}
	return reviewKey(pr, sp.PartID), p, true
}

// reviewRootsOf gives the review of one part its work trees. pid is its part key
// "<project>~<part>" (reviewKey); a bare project id gets none, so nothing is ever kept under it
// again (migrateReviewKeys moved what was).
func (s *Server) reviewRootsOf(rs *reviewState, pid string) []review.Root {
	if _, part := project.SplitPartKey(pid); part == "" {
		return nil
	}
	rp, ok := s.gitRepoIn(pid, "")
	if !ok {
		return nil
	}
	rs.mu.Lock()
	c, ok := rs.cache[pid]
	rs.mu.Unlock()
	if ok && time.Since(c.at) < reviewRootsFor {
		return c.roots
	}
	if rp.MachineID != machine.LocalID {
		return s.reviewServerRoots(rs, pid, rp)
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

// reviewServerRoots gives a project on a server its copy on This PC. The folder on the server
// is the name of the copy, so a project pointed at another folder starts over.
func (s *Server) reviewServerRoots(rs *reviewState, pid string, rp gitsvc.Repo) []review.Root {
	sum := sha256.Sum256([]byte(rp.MachineID + "\x00" + rp.Path))
	rs.mu.Lock()
	m := rs.mirrors[pid]
	dir := filepath.Join(s.st.Path("review"), pid, "mirror", hex.EncodeToString(sum[:6]))
	if m == nil || m.Dir != dir {
		id := rp.MachineID
		m = &review.Mirror{Remote: rp.Path, Dir: dir, Host: func() (machine.Host, bool) { return s.machines.Host(id) }}
		rs.mirrors[pid] = m
	}
	rs.mu.Unlock()
	roots := []review.Root{{Path: dir, Mirror: m, Origin: rp.Path}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var nested []string
	if list, err := s.gitPrev.git.Repos(ctx, rp); err == nil {
		for _, it := range list.Items {
			if it.Path != "" {
				nested = append(nested, it.Path)
				roots = append(roots, review.Root{Rel: it.Path, Path: filepath.Join(dir, filepath.FromSlash(it.Path)), Mirror: m, Origin: path.Join(rp.Path, it.Path)})
			}
		}
	}
	m.SetNested(nested)
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

// reviewProject is the review a request is about: the part its ref or ?part= names, else
// (compat) the project's DefaultPart (ROADMAP #49).
func (s *Server) reviewProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	pr, part, err := s.projectRef(r.PathValue("id"), r.URL.Query().Get("part"))
	switch {
	case errors.Is(err, project.ErrPartNotFound):
		writeErrCode(w, http.StatusNotFound, "part_not_found", err.Error())
		return "", false
	case err != nil:
		writeErrCode(w, http.StatusNotFound, "project_not_found", project.ErrNotFound.Error())
		return "", false
	}
	return reviewKey(pr, part), true
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request, p *principal) {
	if id, ok := s.reviewProject(w, r); ok {
		writeJSON(w, http.StatusOK, s.review.List(id))
		go s.review.Prepare(id)
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
