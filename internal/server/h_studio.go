package server

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/studio"
)

// ---- Documents (§5) ----

func (s *Server) handleDocuments(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.projectByID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.studio.docs.List(pr.ID))
}

func (s *Server) handleDocumentCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.projectByID(w, r)
	if !ok {
		return
	}
	var req struct {
		Type       string `json:"type"`
		Ask        string `json:"ask"`
		Effort     string `json:"effort"`
		Agent      string `json:"agent"`
		ProfileID  string `json:"profileId"`
		DocumentID string `json:"documentId"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Effort == "" {
		req.Effort = "balanced"
	}
	t, err := s.runnerFor(pr, req.Agent, req.ProfileID)
	if err != nil {
		runnerErr(w, err)
		return
	}
	doc, vid, j, err := s.studio.docs.Generate(pr.ID, studio.DocRequest{Type: req.Type, Ask: req.Ask, Effort: req.Effort, Agent: t.agent,
		DocumentID: req.DocumentID, MachineID: pr.MachineID, Cwd: pr.Path, Command: t.command, Env: t.env})
	switch {
	case errors.Is(err, studio.ErrBadDocType):
		writeErrCode(w, http.StatusBadRequest, "bad_type", err.Error())
	case errors.Is(err, studio.ErrBadEffort):
		writeErrCode(w, http.StatusBadRequest, "bad_effort", err.Error())
	case errors.Is(err, studio.ErrBadAsk):
		writeErrCode(w, http.StatusBadRequest, "bad_ask", err.Error())
	case errors.Is(err, studio.ErrDocNotFound):
		writeErrCode(w, http.StatusNotFound, "document_not_found", err.Error())
	case err != nil:
		runnerErr(w, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"document": doc, "versionId": vid, "job": j})
	}
}

func (s *Server) handleDocumentStats(w http.ResponseWriter, r *http.Request, p *principal) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, s.studio.docs.Stats(q.Get("machineId"), q.Get("agent")))
}

func docErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, studio.ErrDocNotFound):
		writeErrCode(w, http.StatusNotFound, "document_not_found", err.Error())
	case errors.Is(err, studio.ErrVersionNotFound):
		writeErrCode(w, http.StatusNotFound, "version_not_found", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleDocumentGet(w http.ResponseWriter, r *http.Request, p *principal) {
	d, err := s.studio.docs.Get(r.PathValue("docId"))
	if err != nil {
		docErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDocumentVersion(w http.ResponseWriter, r *http.Request, p *principal) {
	v, md, err := s.studio.docs.Version(r.PathValue("docId"), r.PathValue("vid"))
	if err != nil {
		docErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v, "markdown": md})
}

func (s *Server) handleDocumentDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	d, err := s.studio.docs.Get(r.PathValue("docId"))
	if err == nil {
		err = s.studio.docs.Delete(d.ID)
	}
	if err != nil {
		docErr(w, err)
		return
	}
	s.events.Publish(map[string]any{"type": "documentRemoved", "projectId": d.ProjectID, "documentId": d.ID})
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleDocumentVersionDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.studio.docs.DeleteVersion(r.PathValue("docId"), r.PathValue("vid")); err != nil {
		docErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

// ---- Diagrams (§6) ----

func (s *Server) handleDiagrams(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.projectByID(w, r)
	if !ok {
		return
	}
	type item struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Subject   string    `json:"subject"`
		CreatedAt time.Time `json:"createdAt"`
		Agent     string    `json:"agent"`
		State     string    `json:"state"`
	}
	out := []item{}
	for _, d := range s.studio.diagrams.List(pr.ID) {
		out = append(out, item{d.ID, d.Title, d.Subject, d.CreatedAt, d.Agent, d.State})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDiagramCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.projectByID(w, r)
	if !ok {
		return
	}
	var req struct {
		Subject   string `json:"subject"`
		Ask       string `json:"ask"`
		Effort    string `json:"effort"`
		Style     string `json:"style"`
		Agent     string `json:"agent"`
		ProfileID string `json:"profileId"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Effort == "" {
		req.Effort = "balanced"
	}
	t, err := s.runnerFor(pr, req.Agent, req.ProfileID)
	if err != nil {
		runnerErr(w, err)
		return
	}
	rec, j, err := s.studio.diagrams.Generate(pr.ID, studio.DiagramRequest{Subject: req.Subject, Ask: req.Ask, Effort: req.Effort,
		Style: req.Style, Agent: t.agent, MachineID: pr.MachineID, Cwd: pr.Path, Command: t.command, Env: t.env})
	switch {
	case errors.Is(err, studio.ErrBadSubject):
		writeErrCode(w, http.StatusBadRequest, "bad_subject", err.Error())
	case errors.Is(err, studio.ErrBadEffort):
		writeErrCode(w, http.StatusBadRequest, "bad_effort", err.Error())
	case errors.Is(err, studio.ErrBadStyle):
		writeErrCode(w, http.StatusBadRequest, "bad_style", err.Error())
	case errors.Is(err, studio.ErrBadAsk):
		writeErrCode(w, http.StatusBadRequest, "bad_ask", err.Error())
	case err != nil:
		runnerErr(w, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"diagramId": rec.ID, "job": j})
	}
}

func (s *Server) handleDiagramGet(w http.ResponseWriter, r *http.Request, p *principal) {
	d, err := s.studio.diagrams.Get(r.PathValue("id"))
	if err != nil {
		writeErrCode(w, http.StatusNotFound, "diagram_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// handleDiagramLand (NextBoard extension; the web client may lay out with
// elkjs itself) lands a finished diagram on a workspace board as loose
// shape/line/text nodes in one ops batch.
func (s *Server) handleDiagramLand(w http.ResponseWriter, r *http.Request, p *principal) {
	d, err := s.studio.diagrams.Get(r.PathValue("id"))
	if err != nil {
		writeErrCode(w, http.StatusNotFound, "diagram_not_found", err.Error())
		return
	}
	var req struct {
		WorkspaceID string   `json:"workspaceId"`
		X           *float64 `json:"x"`
		Y           *float64 `json:"y"`
		Style       string   `json:"style"`
		Arrows      string   `json:"arrows"`
		ArrowColor  string   `json:"arrowColor"`
	}
	if !decode(w, r, &req) {
		return
	}
	if d.Diagram == nil {
		writeErrCode(w, http.StatusConflict, "diagram_not_ready", "diagram not finished yet")
		return
	}
	ws, pr, ok := s.projects.Workspace(req.WorkspaceID)
	if !ok || pr.ID != d.ProjectID {
		writeErrCode(w, http.StatusNotFound, "workspace_not_found", "workspace not found in the project of this diagram")
		return
	}
	style := d.Style
	if req.Style == "ink" || req.Style == "color" {
		style = req.Style
	}
	x, y := 0.0, 0.0
	if req.X != nil && req.Y != nil {
		x, y = *req.X, *req.Y
	} else if b, err := s.boards.Get(ws.ID); err == nil {
		// right of everything on the board
		for i, n := range b.Nodes {
			if i == 0 || n.X+n.W+80 > x {
				x = n.X + n.W + 80
			}
			if i == 0 || n.Y < y {
				y = n.Y
			}
		}
	}
	look := studio.Look{}
	if req.Arrows == "curved" || req.Arrows == "wiring" {
		look.Arrows = req.Arrows
	}
	if req.ArrowColor == "mono" || req.ArrowColor == "color" {
		look.ArrowColor = req.ArrowColor
	}
	ops := studio.Land(d.Diagram, style, d.ID, x, y, look)
	rev, err := s.boards.Apply(ws.ID, "", ops)
	if err != nil {
		var ve *board.ValidationError
		if errors.As(err, &ve) {
			writeErrCode(w, http.StatusBadRequest, "bad_ops", ve.Msg)
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ids := make([]string, 0, len(ops))
	for _, op := range ops {
		ids = append(ids, op.Node.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rev": rev, "nodeIds": ids})
}

// ---- Code studio (§7) ----

func (s *Server) handleFSTree(w http.ResponseWriter, r *http.Request, p *principal) {
	h, fp, ok := s.fsHost(w, r)
	if !ok || s.hubPrivate(w, h, fp) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fsTimeout)
	defer cancel()
	t, err := studio.Tree(ctx, h, fp, r.URL.Query().Get("hidden") == "1")
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleFSFind (extension) searches file names below root.
func (s *Server) handleFSFind(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	q := r.URL.Query()
	root := q.Get("root")
	if root == "" {
		writeErr(w, http.StatusBadRequest, "root is required")
		return
	}
	if s.hubPrivate(w, h, root) {
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, truncated, err := studio.Find(r.Context(), h, root, q.Get("q"), limit)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

func (s *Server) handleFSMkdir(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	var req struct {
		Path    string       `json:"path"`
		Confirm *confirmBody `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	if s.hubPrivate(w, h, req.Path) || !s.remoteConfigConfirm(w, r, h, []string{req.Path}, req.Confirm) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fsTimeout)
	defer cancel()
	st, err := h.Stat(ctx, req.Path)
	if err != nil {
		fsErr(w, err)
		return
	}
	if st.Exists {
		writeErrCode(w, http.StatusConflict, "exists", machine.ErrExists.Error())
		return
	}
	if err := h.Mkdir(ctx, req.Path); err != nil {
		fsErr(w, err)
		return
	}
	if st, err = h.Stat(ctx, req.Path); err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleFSRename(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	var req struct {
		From    string       `json:"from"`
		To      string       `json:"to"`
		Confirm *confirmBody `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.From == "" || req.To == "" {
		writeErr(w, http.StatusBadRequest, "from and to are required")
		return
	}
	if s.hubPrivate(w, h, req.From) || s.hubPrivate(w, h, req.To) ||
		!s.remoteConfigConfirm(w, r, h, []string{req.From, req.To}, req.Confirm) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fsTimeout)
	defer cancel()
	st, err := machine.Rename(ctx, h, req.From, req.To)
	if errors.Is(err, machine.ErrExists) {
		writeErrCode(w, http.StatusConflict, "exists", err.Error())
		return
	}
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleFSCopy(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	var req struct {
		From    string       `json:"from"`
		To      string       `json:"to"`
		Unique  bool         `json:"unique"`
		Confirm *confirmBody `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.From == "" || req.To == "" {
		writeErr(w, http.StatusBadRequest, "from and to are required")
		return
	}
	if s.hubPrivate(w, h, req.From) || s.hubPrivate(w, h, req.To) ||
		!s.remoteConfigConfirm(w, r, h, []string{req.To}, req.Confirm) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fsCopyTimeout)
	defer cancel()
	st, err := machine.Copy(ctx, h, req.From, req.To, req.Unique)
	if errors.Is(err, machine.ErrExists) {
		writeErrCode(w, http.StatusConflict, "exists", err.Error())
		return
	}
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleFSTrash(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	var req struct {
		Path      string `json:"path"`
		ProjectID string `json:"projectId"`
	}
	if !decode(w, r, &req) {
		return
	}
	pr, part, perr := s.projectRef(req.ProjectID, "")
	folder, here := partFolder(pr, h.ID(), req.Path)
	if part != "" && pr.MachineID == h.ID() {
		folder, here = pr.Path, true
	}
	if perr != nil || !here {
		writeErrCode(w, http.StatusNotFound, "project_not_found", "project not found on this machine")
		return
	}
	if req.Path == "" || s.hubPrivate(w, h, req.Path) {
		if req.Path == "" {
			writeErr(w, http.StatusBadRequest, "path is required")
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fsTimeout)
	defer cancel()
	abs := req.Path
	if st, err := h.Stat(ctx, req.Path); err == nil {
		abs = st.Path
	}
	root := folder
	if st, err := h.Stat(ctx, folder); err == nil {
		root = st.Path
	}
	to, err := studio.MoveToTrash(ctx, h, root, abs, time.Now())
	switch {
	case errors.Is(err, studio.ErrOutsideProject):
		writeErrCode(w, http.StatusForbidden, "outside_project", err.Error())
	case errors.Is(err, machine.ErrExists):
		writeErrCode(w, http.StatusConflict, "exists", err.Error())
	case err != nil:
		fsErr(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"trashedTo": to})
	}
}

func (s *Server) handleFSStatBatch(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if !decode(w, r, &req) {
		return
	}
	for _, fp := range req.Paths {
		if s.hubPrivate(w, h, fp) {
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), fsTimeout)
	defer cancel()
	out, err := studio.StatBatch(ctx, h, req.Paths)
	if errors.Is(err, studio.ErrTooManyPaths) {
		writeErrCode(w, http.StatusBadRequest, "too_many_paths", err.Error())
		return
	}
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFSWatch (extension) registers a polling watch (event fsChanged).
func (s *Server) handleFSWatch(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	var req struct {
		Paths  []string `json:"paths"`
		TTLSec int      `json:"ttlSec"`
	}
	if !decode(w, r, &req) {
		return
	}
	for _, fp := range req.Paths {
		if s.hubPrivate(w, h, fp) {
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), fsTimeout)
	defer cancel()
	id, stats, err := s.studio.watcher.Add(ctx, h.ID(), req.Paths, time.Duration(req.TTLSec)*time.Second)
	switch {
	case errors.Is(err, studio.ErrTooManyPaths), errors.Is(err, studio.ErrWatchLimit):
		writeErrCode(w, http.StatusBadRequest, "bad_watch", err.Error())
	case err != nil:
		fsErr(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"watchId": id, "stats": stats, "intervalMs": s.studio.watcher.Interval.Milliseconds()})
	}
}

func (s *Server) handleFSWatchRenew(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		TTLSec int `json:"ttlSec"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.studio.watcher.Renew(r.PathValue("wid"), time.Duration(req.TTLSec)*time.Second); err != nil {
		writeErrCode(w, http.StatusNotFound, "watch_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleFSWatchDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	s.studio.watcher.Remove(r.PathValue("wid"))
	writeJSON(w, http.StatusOK, okBody)
}

// ---- Markdown studio (§8) ----

func (s *Server) vault(w http.ResponseWriter, r *http.Request) (*studio.Vault, bool) {
	pr, ok := s.projectByID(w, r)
	if !ok {
		return nil, false
	}
	h, ok := s.machines.Host(pr.MachineID)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return nil, false
	}
	if s.hubPrivate(w, h, pr.Path) {
		return nil, false
	}
	v, err := s.studio.vaults.Index(r.Context(), h, pr.Path, s.memoryDir(r.Context(), pr), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		fsErr(w, err)
		return nil, false
	}
	return v, true
}

func (s *Server) handleMarkdown(w http.ResponseWriter, r *http.Request, p *principal) {
	if v, ok := s.vault(w, r); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) handleMarkdownGraph(w http.ResponseWriter, r *http.Request, p *principal) {
	if v, ok := s.vault(w, r); ok {
		writeJSON(w, http.StatusOK, studio.BuildGraph(v))
	}
}

func (s *Server) handleMarkdownBacklinks(w http.ResponseWriter, r *http.Request, p *principal) {
	rel := strings.TrimPrefix(path.Clean("/"+r.URL.Query().Get("path")), "/")
	v, ok := s.vault(w, r)
	if !ok {
		return
	}
	in, out, err := studio.Backlinks(v, rel)
	if err != nil {
		writeErrCode(w, http.StatusNotFound, "not_in_vault", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": rel, "backlinks": in, "outgoing": out})
}
