package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/gitsvc"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// Git panel, branches card and GitHub publishing (docs/API-studio.md §3).

// routesGitPreview registers the Phase 7 part A endpoints.
func (s *Server) routesGitPreview(authed func(string, handler)) {
	authed("GET /api/projects/{id}/git/repos", s.handleGitRepos)
	authed("GET /api/projects/{id}/git/status", s.handleGitStatus)
	authed("GET /api/projects/{id}/git/diff", s.handleGitDiff)
	authed("GET /api/projects/{id}/git/show", s.handleGitShow)
	authed("GET /api/projects/{id}/git/log", s.handleGitLog)
	authed("GET /api/projects/{id}/git/commits/{ref}", s.handleGitCommitDetail)
	authed("GET /api/projects/{id}/git/index", s.handleGitIndex)
	authed("GET /api/projects/{id}/git/pulls", s.handleGitPulls)
	authed("POST /api/projects/{id}/git/pulls", s.handleGitPullCreate)
	authed("GET /api/projects/{id}/git/pulls/{n}", s.handleGitPullRequest)
	authed("GET /api/projects/{id}/git/pulls/{n}/diff", s.handleGitPullDiff)
	authed("POST /api/projects/{id}/git/pulls/{n}/checkout", s.handleGitPullCheckout)
	authed("POST /api/projects/{id}/git/pulls/{n}/review", s.handleGitPullReview)
	authed("POST /api/projects/{id}/git/pulls/{n}/merge", s.handleGitPullMerge)
	authed("POST /api/projects/{id}/git/stage", s.handleGitStage)
	authed("POST /api/projects/{id}/git/unstage", s.handleGitUnstage)
	authed("GET /api/projects/{id}/git/stash", s.handleGitStashes)
	authed("POST /api/projects/{id}/git/stash", s.handleGitStashSave)
	authed("POST /api/projects/{id}/git/stash/{action}", s.handleGitStashAct)
	authed("POST /api/projects/{id}/git/commit-op", s.handleGitCommitOp)
	authed("POST /api/projects/{id}/git/tags", s.handleGitTag)
	authed("POST /api/projects/{id}/git/operation/{action}", s.handleGitOperation)
	authed("POST /api/projects/{id}/git/blame", s.handleGitBlame)
	authed("POST /api/projects/{id}/git/commit", s.handleGitCommit)
	authed("POST /api/projects/{id}/git/push", s.handleGitPush)
	authed("POST /api/projects/{id}/git/pull", s.handleGitPull)
	authed("POST /api/projects/{id}/git/fetch", s.handleGitFetch)
	authed("POST /api/projects/{id}/git/discard", s.handleGitDiscard)
	authed("GET /api/projects/{id}/git/trash", s.handleGitTrash)
	authed("POST /api/projects/{id}/git/trash/restore", s.handleGitTrashRestore)
	authed("POST /api/projects/{id}/git/init", s.handleGitInit)
	authed("POST /api/projects/{id}/git/untrack-large", s.handleGitUntrackLarge)
	authed("GET /api/projects/{id}/git/large-history", s.handleGitLargeHistory)
	authed("POST /api/projects/{id}/git/large-history/remove", s.handleGitLargeRemove)
	authed("POST /api/projects/{id}/git/large-history/undo", s.handleGitLargeUndo)
	authed("GET /api/projects/{id}/git/branches", s.handleGitBranches)
	authed("POST /api/projects/{id}/git/branches", s.handleGitBranchCreate)
	authed("DELETE /api/projects/{id}/git/branches/{name...}", s.handleGitBranchDelete)
	authed("POST /api/projects/{id}/git/switch", s.handleGitSwitch)
	authed("GET /api/projects/{id}/git/suggest", s.handleGitSuggest)
	authed("PUT /api/projects/{id}/git/selection", s.handleGitSelection)
	authed("GET /api/projects/{id}/github", s.handleGitHub)
	authed("POST /api/projects/{id}/github/login", s.handleGitHubLogin)
	authed("POST /api/projects/{id}/github/publish", s.handleGitHubPublish)

	authed("GET /api/machines/{id}/ports", s.handlePorts)
	authed("GET /api/machines/{id}/listening", s.handleListening)
	authed("GET /api/previews", s.handlePreviews)
	authed("POST /api/previews", s.handlePreviewCreate)
	authed("POST /api/previews/{id}/launch", s.handlePreviewLaunch)
	authed("DELETE /api/previews/{id}", s.handlePreviewDelete)
	authed("GET /api/previews/{id}/logs", s.handlePreviewLogs)
}

// gitErr writes a git service error as {"error","code"[,"paths"]}.
func gitErr(w http.ResponseWriter, err error) {
	var ge *gitsvc.Error
	switch {
	case errors.As(err, &ge):
		status := ge.Status
		if status < 400 || status > 599 {
			status = http.StatusConflict
		}
		body := map[string]any{"error": ge.Msg, "code": ge.Code}
		if len(ge.Paths) > 0 {
			body["paths"] = ge.Paths
		}
		writeJSON(w, status, body)
	case errors.Is(err, jobs.ErrQueueFull), errors.Is(err, jobs.ErrClosed):
		writeErrCode(w, http.StatusServiceUnavailable, "jobs_unavailable", err.Error())
	case errors.Is(err, jobs.ErrRunnerUnsupported):
		writeErrCode(w, http.StatusBadRequest, "runner_unsupported", err.Error())
	default:
		writeErrCode(w, http.StatusInternalServerError, "git_failed", err.Error())
	}
}

// repo resolves {id} and ?repo= (one of the project's repositories, relative
// to its folder) or writes the error.
func (s *Server) repo(w http.ResponseWriter, r *http.Request) (gitsvc.Repo, bool) {
	rp, ok := s.gitRepoIn(r.PathValue("id"), r.URL.Query().Get("part"))
	if !ok {
		if _, _, err := s.projectRef(r.PathValue("id"), ""); err == nil || errors.Is(err, project.ErrPartNotFound) {
			writeErrCode(w, http.StatusNotFound, "part_not_found", project.ErrPartNotFound.Error())
		} else {
			writeErrCode(w, http.StatusNotFound, "project_not_found", project.ErrNotFound.Error())
		}
		return rp, false
	}
	rp, err := rp.In(r.URL.Query().Get("repo"))
	if err != nil {
		gitErr(w, err)
		return rp, false
	}
	return rp, true
}

// optional decodes a JSON body that may be empty.
func optional(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	return decode(w, r, v)
}

func (s *Server) handleGitRepos(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	l, err := s.gitPrev.git.Repos(r.Context(), rp)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	st, err := s.gitPrev.git.Status(r.Context(), rp)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	staged := r.URL.Query().Get("staged") == "1"
	d, err := s.gitPrev.git.Diff(r.Context(), rp, r.URL.Query().Get("path"), staged)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleGitShow(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f, err := s.gitPrev.git.Show(r.Context(), rp, q.Get("path"), q.Get("ref"))
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handleGitLog(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	var items []gitsvc.Commit
	var err error
	if file := q.Get("path"); file != "" {
		line, _ := strconv.Atoi(q.Get("line"))
		items, err = s.gitPrev.git.FileLog(r.Context(), rp, file, line, limit)
	} else if q.Get("graph") == "1" {
		skip, _ := strconv.Atoi(q.Get("skip"))
		items, err = s.gitPrev.git.LogGraph(r.Context(), rp, gitsvc.GraphOptions{Limit: limit, Skip: skip, All: q.Get("all") == "1"})
	} else {
		items, err = s.gitPrev.git.Log(r.Context(), rp, limit)
	}
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGitCommitDetail(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	d, err := s.gitPrev.git.CommitDetail(r.Context(), rp, r.PathValue("ref"))
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleGitPulls(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	items, err := s.gitPrev.git.Pulls(r.Context(), rp)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGitPullCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req gitsvc.PullCreate
	if !decode(w, r, &req) {
		return
	}
	url, err := s.gitPrev.git.CreatePull(r.Context(), rp, req)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"url": url})
}

func prNumberOf(r *http.Request) int {
	n, _ := strconv.Atoi(r.PathValue("n"))
	return n
}

func (s *Server) handleGitPullRequest(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	d, err := s.gitPrev.git.PullRequest(r.Context(), rp, prNumberOf(r))
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleGitPullDiff(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	diff, err := s.gitPrev.git.PullDiff(r.Context(), rp, prNumberOf(r))
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"diff": diff})
}

func (s *Server) handleGitPullCheckout(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	st, err := s.gitPrev.git.CheckoutPull(r.Context(), rp, prNumberOf(r))
	gitStatusReply(w, st, err)
}

func (s *Server) handleGitPullReview(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Event string `json:"event"`
		Body  string `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.gitPrev.git.ReviewPull(r.Context(), rp, prNumberOf(r), req.Event, req.Body); err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleGitPullMerge(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Method       string `json:"method"`
		DeleteBranch bool   `json:"deleteBranch"`
	}
	if !decode(w, r, &req) {
		return
	}
	st, err := s.gitPrev.git.MergePull(r.Context(), rp, prNumberOf(r), req.Method, req.DeleteBranch)
	gitStatusReply(w, st, err)
}

func (s *Server) handleGitIndex(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	f, err := s.gitPrev.git.ReadIndex(r.Context(), rp, r.URL.Query().Get("path"))
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// gitStatusReply answers an action on the repository with the status it left.
func gitStatusReply(w http.ResponseWriter, st *gitsvc.Status, err error) {
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleGitStage(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Paths []string `json:"paths"`
		Text  *string  `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	st, err := s.gitPrev.git.Stage(r.Context(), rp, req.Paths, req.Text)
	gitStatusReply(w, st, err)
}

func (s *Server) handleGitUnstage(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if !decode(w, r, &req) {
		return
	}
	st, err := s.gitPrev.git.Unstage(r.Context(), rp, req.Paths)
	gitStatusReply(w, st, err)
}

func (s *Server) handleGitStashes(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	items, err := s.gitPrev.git.Stashes(r.Context(), rp)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGitStashSave(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Message   string `json:"message"`
		Untracked bool   `json:"untracked"`
	}
	if !optional(w, r, &req) {
		return
	}
	st, err := s.gitPrev.git.StashSave(r.Context(), rp, req.Message, req.Untracked)
	gitStatusReply(w, st, err)
}

func (s *Server) handleGitStashAct(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Ref string `json:"ref"`
	}
	if !decode(w, r, &req) {
		return
	}
	st, err := s.gitPrev.git.StashAct(r.Context(), rp, r.PathValue("action"), req.Ref)
	gitStatusReply(w, st, err)
}

func (s *Server) handleGitCommitOp(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req gitsvc.CommitOp
	if !decode(w, r, &req) {
		return
	}
	// a hard reset throws away what is not committed: the local history keeps it
	if req.Op == "reset" && req.Mode == "hard" {
		key := rp.ProjectID
		if rp.Part != "" {
			key += "~" + rp.Part
		}
		_ = s.review.Snapshot(key)
	}
	st, err := s.gitPrev.git.DoCommitOp(r.Context(), rp, req)
	gitStatusReply(w, st, err)
}

func (s *Server) handleGitTag(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Name    string `json:"name"`
		Ref     string `json:"ref"`
		Message string `json:"message"`
	}
	if !decode(w, r, &req) {
		return
	}
	st, err := s.gitPrev.git.CreateTag(r.Context(), rp, req.Name, req.Ref, req.Message)
	gitStatusReply(w, st, err)
}

func (s *Server) handleGitOperation(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	st, err := s.gitPrev.git.Operation(r.Context(), rp, r.PathValue("action"))
	gitStatusReply(w, st, err)
}

func (s *Server) handleGitBlame(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string  `json:"path"`
		Text *string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	b, err := s.gitPrev.git.Blame(r.Context(), rp, req.Path, req.Text)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleGitCommit(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req gitsvc.CommitRequest
	if !decode(w, r, &req) {
		return
	}
	res, err := s.gitPrev.git.Commit(r.Context(), rp, req)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGitPush(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		SetUpstream bool `json:"setUpstream"`
	}
	if !optional(w, r, &req) {
		return
	}
	j, err := s.gitPrev.git.Push(rp, req.SetUpstream, nil)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) handleGitPull(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	j, err := s.gitPrev.git.Pull(rp, nil)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) handleGitFetch(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	j, err := s.gitPrev.git.Fetch(rp, nil)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

type pathsBody struct {
	Paths []string `json:"paths"`
}

func (s *Server) handleGitDiscard(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req pathsBody
	if !decode(w, r, &req) {
		return
	}
	res, err := s.gitPrev.git.Discard(r.Context(), rp, req.Paths)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGitTrash(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	items, err := s.gitPrev.git.Trash(r.Context(), rp)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGitTrashRestore(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		ID    string   `json:"id"`
		Paths []string `json:"paths"`
	}
	if !decode(w, r, &req) {
		return
	}
	got, err := s.gitPrev.git.RestoreTrash(r.Context(), rp, req.ID, req.Paths)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restored": got})
}

func (s *Server) handleGitInit(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Branch string `json:"branch"`
	}
	if !optional(w, r, &req) {
		return
	}
	st, err := s.gitPrev.git.Init(r.Context(), rp, req.Branch)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleGitUntrackLarge(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req pathsBody
	if !decode(w, r, &req) {
		return
	}
	st, err := s.gitPrev.git.UntrackLarge(r.Context(), rp, req.Paths)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleGitLargeHistory(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	h, err := s.gitPrev.git.ScanLargeHistory(r.Context(), rp)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleGitLargeRemove(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Head  string   `json:"head"`
		Paths []string `json:"paths"`
	}
	if !decode(w, r, &req) {
		return
	}
	res, err := s.gitPrev.git.RemoveLargeFromHistory(r.Context(), rp, req.Head, req.Paths)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGitLargeUndo(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Backup string   `json:"backup"`
		Head   string   `json:"head"`
		Paths  []string `json:"paths"`
	}
	if !decode(w, r, &req) {
		return
	}
	st, err := s.gitPrev.git.UndoLargeRemoval(r.Context(), rp, req.Backup, req.Head, req.Paths)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleGitBranches(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	b, err := s.gitPrev.git.Branches(r.Context(), rp)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleGitBranchCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Name   string `json:"name"`
		From   string `json:"from"`
		Switch *bool  `json:"switch"`
	}
	if !decode(w, r, &req) {
		return
	}
	sw := req.Switch == nil || *req.Switch
	st, err := s.gitPrev.git.CreateBranch(r.Context(), rp, req.Name, req.From, sw)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleGitBranchDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	if err := s.gitPrev.git.DeleteBranch(r.Context(), rp, r.PathValue("name")); err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleGitSwitch(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	st, err := s.gitPrev.git.Switch(r.Context(), rp, req.Name)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleGitSuggest: ai=0 → heuristic subject; ai=1 → 202 commit-subject
// runner job, staged=1 reading the index. paths= (comma separated is
// ambiguous) → repeat ?path=.
func (s *Server) handleGitSuggest(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	paths := r.URL.Query()["path"]
	if r.URL.Query().Get("ai") == "1" {
		j, err := s.gitPrev.git.SuggestAI(r.Context(), rp, paths, r.URL.Query().Get("staged") == "1")
		if err != nil {
			gitErr(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, j)
		return
	}
	sg, err := s.gitPrev.git.Suggest(r.Context(), rp, paths)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sg)
}

// handleGitSelection stores the panel's checked paths for the command
// bar's "commit and push" (NextBoard addition to §3.4).
func (s *Server) handleGitSelection(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req pathsBody
	if !decode(w, r, &req) {
		return
	}
	if err := s.gitPrev.git.SetSelection(rp, req.Paths); err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"paths": s.gitPrev.git.Selection(rp)})
}

func (s *Server) handleGitHub(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	g, err := s.gitPrev.git.GitHubStatus(r.Context(), rp)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleGitHubLogin opens a normal console on the project's machine that
// runs `gh auth login`, then watches `gh auth status` while it lives.
func (s *Server) handleGitHubLogin(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req struct {
		WorkspaceID string `json:"workspaceId"`
	}
	if !optional(w, r, &req) {
		return
	}
	pr, _ := s.projects.Get(rp.ProjectID)
	wid := req.WorkspaceID
	if wid != "" {
		if ws, wp, ok := s.projects.Workspace(wid); !ok || wp.ID != pr.ID {
			writeErrCode(w, http.StatusNotFound, "workspace_not_found", project.ErrWorkspaceNotFound.Error())
			return
		} else {
			wid = ws.ID
		}
	} else if len(pr.Workspaces) > 0 {
		wid = pr.Workspaces[0].ID
	}
	if wid == "" {
		writeErrCode(w, http.StatusNotFound, "workspace_not_found", project.ErrWorkspaceNotFound.Error())
		return
	}
	in, err := s.sessions.Create(session.CreateRequest{MachineID: pr.MachineID, Preset: "shell", Name: "GitHub login",
		Cwd: pr.Path, ProjectID: pr.ID, WorkspaceID: wid})
	if err != nil {
		s.sessionErr(w, err)
		return
	}
	if _, err := s.boards.AddTerminal(wid, in.ID, nil); err != nil {
		_ = s.sessions.Delete(in.ID)
		s.boardErr(w, err)
		return
	}
	s.emitMachine(in.MachineID)
	sid := in.ID
	go func() {
		// Type the command once the shell has drawn its prompt.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if i, ok := s.sessions.Info(sid); !ok || !i.LastOutputAt.IsZero() {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)
		if sess, ok := s.sessions.Get(sid); ok {
			_ = sess.Input([]byte(gitsvcGh(s) + " auth login\r"))
		}
	}()
	s.gitPrev.git.WatchLogin(rp, func() bool {
		i, ok := s.sessions.Info(sid)
		return ok && i.Status == session.StatusRunning
	})
	writeJSON(w, http.StatusCreated, in)
}

// gitsvcGh is the gh command typed into the login console.
func gitsvcGh(s *Server) string {
	if g := s.gitPrev.git.Gh; g != "" && !strings.ContainsAny(g, " \t\r\n;&|$`'\"\\<>()") {
		return g
	}
	return "gh"
}

func (s *Server) handleGitHubPublish(w http.ResponseWriter, r *http.Request, p *principal) {
	rp, ok := s.repo(w, r)
	if !ok {
		return
	}
	var req gitsvc.PublishRequest
	if !decode(w, r, &req) {
		return
	}
	j, err := s.gitPrev.git.PublishGitHub(r.Context(), rp, req, nil)
	if err != nil {
		gitErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

// gitActions implements orch.GitActions on top of the git service: the
// command bar "presses the panel buttons" (API-studio §3.4).
type gitActions struct{ s *Server }

func (g gitActions) GitStep(pid, repo, action, message string, report func(string, bool)) (string, error) {
	rp, ok := g.s.gitRepo(pid)
	if !ok {
		return "", project.ErrNotFound
	}
	svc := g.s.gitPrev.git
	pickCtx, cancelPick := context.WithTimeout(context.Background(), gitsvc.ReadTimeout+2*time.Second)
	rp, err := svc.Pick(pickCtx, rp, repo)
	cancelPick()
	if err != nil {
		return "", stepErr(err)
	}
	// With several repositories every message says which one it is about.
	named := func(text string) string {
		if rp.Sub == "" || text == "" {
			return text
		}
		return rp.Sub + ": " + text
	}
	pushed := func(j jobs.Job) {
		switch {
		case j.State == jobs.StateDone:
			report(named(fmt.Sprintf("%s done.", strings.ToUpper(j.Purpose[:1])+j.Purpose[1:])), false)
		case j.Code == "diverged":
			report(named("Branches diverged — merge in a console."), true)
		case j.State == jobs.StateCancelled:
			report(named(strings.ToUpper(j.Purpose[:1])+j.Purpose[1:]+" cancelled."), true)
		default:
			report(named(fmt.Sprintf("%s failed: %s", strings.ToUpper(j.Purpose[:1])+j.Purpose[1:], firstLine(j.Error))), true)
		}
	}
	switch action {
	case "commit", "commitPush":
		ctx, cancel := context.WithTimeout(context.Background(), gitsvc.CommitTimeout+30*time.Second)
		defer cancel()
		st, err := svc.Status(ctx, rp)
		if err != nil {
			return "", stepErr(err)
		}
		if !st.IsRepo {
			return "", errors.New("this project is not a git repository")
		}
		req := gitsvc.CommitRequest{Message: strings.TrimSpace(message), All: true}
		changed := map[string]bool{}
		for _, f := range st.Files {
			changed[f.Path] = true
		}
		var sel []string
		for _, p := range svc.Selection(rp) {
			if changed[p] {
				sel = append(sel, p)
			}
		}
		if len(sel) > 0 {
			req.All, req.Paths = false, sel
		}
		if req.Message == "" {
			files := st.Files
			if len(sel) > 0 {
				files = nil
				for _, f := range st.Files {
					for _, p := range sel {
						if f.Path == p {
							files = append(files, f)
						}
					}
				}
			}
			req.Message = gitsvc.SuggestSubject(files)
		}
		text := ""
		if st.Changed == 0 {
			if action == "commit" {
				return named("Nothing to commit."), nil
			}
			text = "Nothing to commit."
		} else {
			res, err := svc.Commit(ctx, rp, req)
			if err != nil {
				return "", stepErr(err)
			}
			n := len(req.Paths)
			if req.All {
				n = st.Changed
			}
			text = fmt.Sprintf("Committed %s “%s” (%d file%s).", res.Commit.Short, res.Commit.Subject, n, plural(n))
		}
		if action == "commit" {
			return named(text), nil
		}
		if _, err := svc.Push(rp, false, pushed); err != nil {
			return named(text), stepErr(err)
		}
		return named(strings.TrimSpace(text + " Pushing…")), nil
	case "push":
		if _, err := svc.Push(rp, false, pushed); err != nil {
			return "", stepErr(err)
		}
		return named("Pushing…"), nil
	case "pull":
		if _, err := svc.Pull(rp, pushed); err != nil {
			return "", stepErr(err)
		}
		return named("Pulling…"), nil
	}
	return "", fmt.Errorf("unknown git action %q", action)
}

// stepErr turns a git service error into a short English thread message.
func stepErr(err error) error {
	var ge *gitsvc.Error
	if !errors.As(err, &ge) {
		return err
	}
	switch ge.Code {
	case "git_busy":
		return errors.New("another git action is still running")
	case "conflicts":
		return errors.New("resolve the merge conflicts first")
	case "large_files":
		return fmt.Errorf("files over 100 MB (%s): keep them off GitHub in the Git panel", strings.Join(ge.Paths, ", "))
	case "git_identity":
		return errors.New("set git user.name and user.email on this machine first")
	case "nothing_to_commit":
		return errors.New("nothing to commit")
	case "not_repo":
		return errors.New("this project is not a git repository")
	case "machine_offline":
		return errors.New("the project's machine is not connected")
	case "repo_needed":
		return fmt.Errorf("this project has %d repositories (%s): name one, e.g. “repo %s commit and push”", len(ge.Paths), strings.Join(ge.Paths, ", "), ge.Paths[0])
	case "repo_not_found":
		return fmt.Errorf("no such repository in this project (it has: %s)", strings.Join(ge.Paths, ", "))
	case "repo_ambiguous":
		return fmt.Errorf("several repositories have that name: use its path (%s)", strings.Join(ge.Paths, ", "))
	}
	return errors.New(firstLine(ge.Msg))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
