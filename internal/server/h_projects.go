package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// counts returns live session counts per project and per workspace.
func (s *Server) counts() (byProject, byWorkspace map[string]project.Counts) {
	byProject, byWorkspace = map[string]project.Counts{}, map[string]project.Counts{}
	for _, pl := range s.sessions.Placements() {
		cp, cw := byProject[pl.ProjectID], byWorkspace[pl.WorkspaceID]
		cp.Total++
		cw.Total++
		if pl.Running {
			cp.Running++
			cw.Running++
		}
		byProject[pl.ProjectID], byWorkspace[pl.WorkspaceID] = cp, cw
	}
	return byProject, byWorkspace
}

func (s *Server) projectViews() []project.View {
	bp, bw := s.counts()
	list := s.projects.List()
	out := make([]project.View, 0, len(list))
	for _, p := range list {
		out = append(out, p.View(bp, bw))
	}
	return out
}

func (s *Server) projectView(p project.Project) project.View {
	bp, bw := s.counts()
	return p.View(bp, bw)
}

// emitProject publishes the current view of a project (no-op if gone).
func (s *Server) emitProject(id string) {
	if id == "" {
		return
	}
	if p, ok := s.projects.Get(id); ok {
		s.events.Publish(map[string]any{"type": "project", "project": s.projectView(p)})
	}
}

// trackSession emits a project event when a session first appears or its
// running state flips (sessionCount / runningCount changed).
func (s *Server) trackSession(in session.Info) {
	running := in.Status == session.StatusRunning
	s.runMu.Lock()
	prev, seen := s.running[in.ID]
	s.running[in.ID] = running
	s.runMu.Unlock()
	if !seen || prev != running {
		s.emitProject(in.ProjectID)
	}
}

func (s *Server) untrackSession(id string) {
	s.runMu.Lock()
	delete(s.running, id)
	s.runMu.Unlock()
}

// defaultWorkspace is where sessions without workspaceId go: the first
// workspace of the most recently opened project, or a new "Home".
func (s *Server) defaultWorkspace() (project.Project, project.Workspace, error) {
	if p, w, ok := s.projects.Default(); ok {
		return p, w, nil
	}
	p, created, err := s.projects.Create(machine.LocalID, homeDir(), "Home")
	if err != nil {
		return project.Project{}, project.Workspace{}, err
	}
	if created {
		s.emitProject(p.ID)
	}
	return p, p.Workspaces[0], nil
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && filepath.IsAbs(h) {
		return filepath.Clean(h)
	}
	return "/"
}

func projectErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound), errors.Is(err, project.ErrWorkspaceNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, project.ErrLastWorkspace):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, project.ErrBadName):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, project.ErrBadSandbox):
		writeErrCode(w, http.StatusBadRequest, "bad_sandbox", err.Error())
	case errors.Is(err, project.ErrBadPartAccess):
		writeErrCode(w, http.StatusBadRequest, "bad_part_access", err.Error())
	case errors.Is(err, project.ErrGroupNotFound):
		writeErrCode(w, http.StatusNotFound, "group_not_found", err.Error())
	case errors.Is(err, project.ErrGroupNameTaken):
		writeErrCode(w, http.StatusConflict, "name_conflict", err.Error())
	case errors.Is(err, project.ErrGroupTooDeep):
		writeErrCode(w, http.StatusBadRequest, "group_too_deep", err.Error())
	case errors.Is(err, project.ErrGroupCycle):
		writeErrCode(w, http.StatusBadRequest, "group_cycle", err.Error())
	case errors.Is(err, project.ErrBadTag):
		writeErrCode(w, http.StatusBadRequest, "bad_tag", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.projectViews())
}

func (s *Server) handleProjectCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		MachineID string `json:"machineId"`
		Path      string `json:"path"`
		Name      string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.MachineID == "" {
		req.MachineID = machine.LocalID
	}
	h, ok := s.machines.Host(req.MachineID)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	if req.Name != "" {
		if _, err := project.CleanName(req.Name); err != nil {
			projectErr(w, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	st, err := h.Stat(ctx, strings.TrimSpace(req.Path))
	if err != nil {
		fsErr(w, err)
		return
	}
	if !st.Exists || !st.Dir {
		writeErr(w, http.StatusBadRequest, "path is not a folder: "+st.Path)
		return
	}
	pr, created, err := s.projects.Create(req.MachineID, st.Path, req.Name)
	if err != nil {
		projectErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		s.emitProject(pr.ID)
	}
	writeJSON(w, status, s.projectView(pr))
}

func (s *Server) handleProjectUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name    *string   `json:"name"`
		Opened  bool      `json:"opened"`
		GroupID *string   `json:"groupId"`
		Tags    *[]string `json:"tags"`
		Sandbox *string   `json:"sandbox"`
		// PartAccess: what agents may do in the parts on other machines than their own (ROADMAP #49)
		PartAccess *string `json:"partAccess"`
	}
	if !decode(w, r, &req) {
		return
	}
	pr, err := s.projects.Patch(r.PathValue("id"), project.Patch{Name: req.Name, Opened: req.Opened, GroupID: req.GroupID, Tags: req.Tags, Sandbox: req.Sandbox, PartAccess: req.PartAccess})
	if err != nil {
		projectErr(w, err)
		return
	}
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusOK, s.projectView(pr))
}

// handleProjectDelete ends every session of the project and archives its
// boards. Files on disk are not touched.
func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, err := s.projects.Delete(r.PathValue("id"))
	if err != nil {
		projectErr(w, err)
		return
	}
	inProject := map[string]bool{}
	for _, ws := range pr.Workspaces {
		inProject[ws.ID] = true
	}
	s.closeBoards(pr.Name, pr.Workspaces)
	s.uptime.RemoveProject(pr.ID)
	for _, pt := range pr.Parts {
		s.review.Forget(reviewKey(pr, pt.ID))
	}
	s.deleteSessions(func(pl session.Placement) bool { return pl.ProjectID == pr.ID || inProject[pl.WorkspaceID] })
	s.events.Publish(map[string]any{"type": "projectRemoved", "id": pr.ID})
	writeJSON(w, http.StatusOK, okBody)
}

// handleProjectCloseAllPreview says what closing every board would end, for the
// question asked first.
func (s *Server) handleProjectCloseAllPreview(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, project.ErrNotFound.Error())
		return
	}
	onBoards := map[string]bool{}
	tiles := 0
	for _, ws := range pr.Workspaces {
		onBoards[ws.ID] = true
		if b, err := s.boards.Get(ws.ID); err == nil {
			for _, n := range b.Nodes {
				if n.Kind != board.KindTerminal {
					tiles++
				}
			}
		}
	}
	sessions, running := 0, 0
	for _, pl := range s.sessions.Placements() {
		if pl.ProjectID == pr.ID || onBoards[pl.WorkspaceID] {
			sessions++
			if pl.Running {
				running++
			}
		}
	}
	loops := 0
	for _, l := range s.orch.Loops.List(pr.ID) {
		if l.State.Status == "running" {
			loops++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"boards": len(pr.Workspaces), "tiles": tiles, "sessions": sessions, "running": running,
		"apps": len(s.appsOnlyOn(pr.Workspaces)), "loops": loops})
}

// handleProjectCloseAll closes every board of the project and leaves one new
// empty board: running loops stop, sessions and the desktop apps only these
// boards show end, and the boards go to the archive. Files on disk are not
// touched.
func (s *Server) handleProjectCloseAll(w http.ResponseWriter, r *http.Request, p *principal) {
	ws, closed, pr, err := s.projects.CloseAllWorkspaces(r.PathValue("id"))
	if err != nil {
		projectErr(w, err)
		return
	}
	gone := map[string]bool{}
	for _, c := range closed {
		gone[c.ID] = true
	}
	loops := s.orch.Loops.StopProject(pr.ID, time.Now())
	archived, apps := s.closeBoards(pr.Name, closed)
	sessions := s.deleteSessions(func(pl session.Placement) bool {
		return pl.WorkspaceID != ws.ID && (pl.ProjectID == pr.ID || gone[pl.WorkspaceID])
	})
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusOK, map[string]any{"workspace": s.workspaceView(ws), "closed": len(closed), "archived": archived,
		"sessions": sessions, "apps": apps, "loops": loops})
}

// closeBoards archives (reason "closed") and drops workspace boards, and closes
// the desktop apps only they show: those outlive the hub, so nothing else would
// end them. It returns how many boards reached the archive and how many apps
// closed.
func (s *Server) closeBoards(projectName string, closing []project.Workspace) (archived, apps int) {
	for _, id := range s.appsOnlyOn(closing) {
		if s.gui.Stop(id) == nil {
			apps++
		}
	}
	for _, ws := range closing {
		id, err := s.boards.Archive(ws.ID, projectName, ws.Name, board.ReasonClosed)
		if err != nil {
			log.Printf("archiving board %s failed: %v", ws.ID, err)
		} else if id != "" {
			archived++
		}
		s.boards.Drop(ws.ID)
	}
	return archived, apps
}

// appsOnlyOn lists the running desktop apps shown by tiles on these workspaces
// and on no other board. One app has one session, so another project's board
// may show the same one.
func (s *Server) appsOnlyOn(closing []project.Workspace) []string {
	running := s.gui.List()
	if len(running) == 0 {
		return nil
	}
	shown := map[string]bool{}
	skip := map[string]bool{}
	for _, ws := range closing {
		skip[ws.ID] = true
		for _, id := range s.boardApps(ws.ID) {
			shown[id] = true
		}
	}
	for _, pr := range s.projects.List() {
		for _, ws := range pr.Workspaces {
			if skip[ws.ID] {
				continue
			}
			for _, id := range s.boardApps(ws.ID) {
				delete(shown, id)
			}
		}
	}
	var out []string
	for _, g := range running {
		if shown[g.ID] {
			out = append(out, g.ID)
		}
	}
	return out
}

// boardApps lists the desktop app sessions named by a board's tiles.
func (s *Server) boardApps(wid string) []string {
	b, err := s.boards.Get(wid)
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range b.Nodes {
		if n.Kind != "app" && n.Kind != "web" {
			continue
		}
		var d struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(n.Data, &d) == nil && d.SessionID != "" {
			out = append(out, d.SessionID)
		}
	}
	return out
}

// deleteSessions stops and removes matching sessions in parallel (each
// stop may wait for the HUP→KILL grace period). It returns how many matched.
func (s *Server) deleteSessions(match func(session.Placement) bool) int {
	var wg sync.WaitGroup
	machines := map[string]bool{}
	n := 0
	for _, pl := range s.sessions.AllPlacements() {
		if !match(pl) {
			continue
		}
		n++
		if in, ok := s.sessions.Info(pl.ID); ok {
			machines[in.MachineID] = true
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = s.sessions.Delete(id)
		}(pl.ID)
	}
	wg.Wait()
	for id := range machines {
		s.emitMachine(id)
	}
	return n
}

func (s *Server) handleProjectGit(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, ok := s.projects.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, project.ErrNotFound.Error())
		return
	}
	h, ok := s.machines.Host(pr.MachineID)
	if !ok {
		writeJSON(w, http.StatusOK, machine.GitInfo{Error: machine.ErrNotFound.Error()})
		return
	}
	writeJSON(w, http.StatusOK, machine.GitStatus(r.Context(), h, pr.Path))
}

func (s *Server) workspaceView(ws project.Workspace) project.WorkspaceView {
	_, bw := s.counts()
	return ws.View(bw)
}

func (s *Server) handleWorkspaceCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name string `json:"name"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	ws, pr, err := s.projects.AddWorkspace(r.PathValue("id"), req.Name)
	if err != nil {
		projectErr(w, err)
		return
	}
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusCreated, s.workspaceView(ws))
}

func (s *Server) handleWorkspaceUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name  *string `json:"name"`
		Index *int    `json:"index"`
	}
	if !decode(w, r, &req) {
		return
	}
	ws, pr, err := s.projects.UpdateWorkspace(r.PathValue("id"), req.Name, req.Index)
	if err != nil {
		projectErr(w, err)
		return
	}
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusOK, s.workspaceView(ws))
}

// handleWorkspaceDelete ends the workspace's sessions and archives its
// board. The last workspace of a project cannot be deleted (409).
func (s *Server) handleWorkspaceDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	ws, pr, err := s.projects.DeleteWorkspace(r.PathValue("id"))
	if err != nil {
		projectErr(w, err)
		return
	}
	s.closeBoards(pr.Name, []project.Workspace{ws})
	s.deleteSessions(func(pl session.Placement) bool { return pl.WorkspaceID == ws.ID })
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusOK, okBody)
}
