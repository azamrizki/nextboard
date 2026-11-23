package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// Archive, restore and delete for good, one at a time or in bulk (docs/API-board.md §1, docs/API.md
// Machines). Archiving keeps everything and stops what runs; restoring brings it back as it was;
// deleting for good removes the NextBoard data. Folders and files on any machine are never touched.

// maxBulk is how many ids one bulk request may name.
const maxBulk = 200

// bulkWorkers: projects archived at once in a bulk request (each may wait for its consoles to end).
const bulkWorkers = 8

func permanent(r *http.Request) bool {
	switch r.URL.Query().Get("permanent") {
	case "1", "true", "yes":
		return true
	}
	return false
}

// projectArchived reports whether pid is archived (a review key "<project>~<part>" counts as its project).
func (s *Server) projectArchived(pid string) bool {
	if i := strings.IndexByte(pid, '~'); i >= 0 {
		pid = pid[:i]
	}
	return s.projects.IsArchived(pid)
}

func inProject(pr project.Project) func(session.Spec) bool {
	ws := map[string]bool{}
	for _, w := range pr.Workspaces {
		ws[w.ID] = true
	}
	return func(sp session.Spec) bool { return sp.ProjectID == pr.ID || ws[sp.WorkspaceID] }
}

// archiveProject marks a project archived, then stops its consoles (agents asleep with their
// conversation, everything else ended) and its monitors. Loops and snapshots wait by themselves.
func (s *Server) archiveProject(id string) (project.Project, error) {
	pr, err := s.projects.Archive(id)
	if err != nil {
		return pr, err
	}
	s.sessions.Freeze(inProject(pr))
	s.uptime.PauseProject(pr.ID)
	s.emitProject(pr.ID)
	return pr, nil
}

// restoreProject puts an archived project back: monitors are checked again, loops fire on their
// schedule from now, agents wake when opened, typed into or sent work, as asleep ones do.
func (s *Server) restoreProject(id string) (project.Project, error) {
	pr, err := s.projects.Restore(id)
	if err != nil {
		return pr, err
	}
	s.uptime.ResumeProject(pr.ID)
	if s.orch != nil {
		s.orch.Loops.Reschedule(pr.ID, time.Now())
	}
	s.emitProject(pr.ID)
	return pr, nil
}

// deleteProjectForever removes a project and all NextBoard keeps of it: its consoles with their
// scrollback, boards with their snapshots and archived copies, the desktop apps only its boards
// show, the threads of its lead and its orchestrator cards, its orders, handoffs and loops, its
// memory and sub-memories, review and Local History (shadows and server copies), uptime monitors
// and public status page, documents, pasted files, and the record. Its folders stay.
func (s *Server) deleteProjectForever(id string) error {
	pr, err := s.projects.Delete(id)
	if err != nil {
		return err
	}
	match := inProject(pr)
	wids := make([]string, 0, len(pr.Workspaces))
	var cards []string
	for _, ws := range pr.Workspaces {
		wids = append(wids, ws.ID)
		if b, err := s.boards.Get(ws.ID); err == nil {
			for _, n := range b.Nodes {
				if n.Kind == orch.KindOrchestrator && !slices.Contains(cards, n.ID) {
					cards = append(cards, n.ID)
				}
			}
		}
	}
	for _, j := range s.jobs.List(pr.ID, true, 100) {
		go s.jobs.Cancel(j.ID) // a running one may take a few seconds to stop
	}
	for _, app := range s.appsOnlyOn(pr.Workspaces) {
		_ = s.gui.Stop(app)
	}
	for _, ws := range pr.Workspaces {
		s.boards.Drop(ws.ID)
	}
	s.boards.DeleteArchives(pr.ID, wids)
	s.uptime.RemoveProject(pr.ID)
	s.dbRemoveProject(pr.ID)
	for _, pt := range pr.Parts {
		s.review.Forget(reviewKey(pr, pt.ID))
	}
	s.deleteSessions(func(pl session.Placement) bool {
		return match(session.Spec{ProjectID: pl.ProjectID, WorkspaceID: pl.WorkspaceID})
	})
	if s.orch != nil {
		s.orch.ForgetProject(pr.ID, cards)
	}
	if s.chats != nil {
		s.chats.idx.DeleteProject(pr.ID)
	}
	if s.studio != nil && s.studio.docs != nil {
		if err := s.studio.docs.RemoveProject(pr.ID); err != nil {
			log.Printf("documents of %s: %v", pr.ID, err)
		}
	}
	if attachID.MatchString(pr.ID) {
		_ = os.RemoveAll(s.st.Path("attachments", "project-"+pr.ID))
	}
	s.art.forget(pr.ID)
	s.roots.Remove(append([]string{pr.ID}, pr.Aliases...)...) // root folders: links go, never what they point to
	s.events.Publish(map[string]any{"type": "projectRemoved", "id": pr.ID})
	return nil
}

func (s *Server) handleProjectArchive(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, err := s.archiveProject(r.PathValue("id"))
	if err != nil {
		projectErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.projectView(pr))
}

func (s *Server) handleProjectRestore(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, err := s.restoreProject(r.PathValue("id"))
	if err != nil {
		projectErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.projectView(pr))
}

// handleProjectDelete archives (older clients' delete), or with ?permanent=1 deletes for good.
func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if !permanent(r) {
		s.handleProjectArchive(w, r, p)
		return
	}
	if err := s.deleteProjectForever(r.PathValue("id")); err != nil {
		projectErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

// bulkResult is one id's outcome in a bulk answer.
type bulkResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}

type bulkRequest struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
}

// readBulk decodes and checks a bulk body; ids are kept in order without repeats.
func readBulk(w http.ResponseWriter, r *http.Request, actions ...string) (bulkRequest, bool) {
	var req bulkRequest
	if !decode(w, r, &req) {
		return req, false
	}
	if !slices.Contains(actions, req.Action) {
		writeErrCode(w, http.StatusBadRequest, "bad_action", `action must be one of "`+strings.Join(actions, `", "`)+`"`)
		return req, false
	}
	var ids []string
	for _, id := range req.IDs {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > maxBulk {
		writeErrCode(w, http.StatusBadRequest, "bad_ids", "name 1 to 200 ids")
		return req, false
	}
	req.IDs = ids
	return req, true
}

// runBulk runs do for each id, at most workers at once, and keeps the results in order.
func runBulk(ids []string, workers int, do func(id string) (code string, err error)) []bulkResult {
	out := make([]bulkResult, len(ids))
	sem := make(chan struct{}, max(1, workers))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res := bulkResult{ID: id, OK: true}
			if code, err := do(id); err != nil {
				res = bulkResult{ID: id, Error: err.Error(), Code: code}
			}
			out[i] = res
		}()
	}
	wg.Wait()
	return out
}

func projectCode(err error) string {
	switch {
	case errors.Is(err, project.ErrNotFound):
		return "project_not_found"
	case errors.Is(err, project.ErrNotArchived):
		return "not_archived"
	}
	return "failed"
}

func (s *Server) handleProjectsBulk(w http.ResponseWriter, r *http.Request, p *principal) {
	req, ok := readBulk(w, r, "archive", "restore", "delete")
	if !ok {
		return
	}
	workers := bulkWorkers
	if req.Action != "archive" {
		workers = 1 // fast, and one store write after another
	}
	res := runBulk(req.IDs, workers, func(id string) (string, error) {
		var err error
		switch req.Action {
		case "archive":
			_, err = s.archiveProject(id)
		case "restore":
			_, err = s.restoreProject(id)
		case "delete":
			err = s.deleteProjectForever(id)
		}
		return projectCode(err), err
	})
	s.events.Publish(map[string]any{"type": "projects"})
	writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

// ---- machines ----

type projectRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// machineUsers lists the projects (archived ones too) with a part on the machine or a console
// on it, and how many consoles it has.
func (s *Server) machineUsers(id string) ([]projectRef, int) {
	refs := []projectRef{}
	seen := map[string]bool{}
	add := func(pr project.Project) {
		if !seen[pr.ID] {
			seen[pr.ID] = true
			refs = append(refs, projectRef{ID: pr.ID, Name: pr.Name})
		}
	}
	for _, pr := range s.projects.ListAll() {
		for _, pt := range pr.Parts {
			if pt.MachineID == id {
				add(pr)
				break
			}
		}
	}
	consoles := 0
	for _, in := range s.sessions.List() {
		if in.MachineID != id {
			continue
		}
		consoles++
		if pr, ok := s.projects.Get(in.ProjectID); ok {
			add(pr)
		}
	}
	return refs, consoles
}

// machineFree refuses This PC (400) and a machine something uses (409 in_use).
func (s *Server) machineFree(w http.ResponseWriter, id string) bool {
	if id == machine.LocalID {
		writeErr(w, http.StatusBadRequest, machine.ErrLocal.Error())
		return false
	}
	if _, ok := s.machines.SSH(id); !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return false
	}
	if refs, n := s.machineUsers(id); len(refs) > 0 || n > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "in_use", "code": "in_use", "projects": refs, "sessions": n,
			"message": "projects or consoles use this machine; remove their parts and close the consoles first"})
		return false
	}
	return true
}

func machineErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, machine.ErrLocal):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, machine.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, machine.ErrNotArchived):
		writeErrCode(w, http.StatusConflict, "not_archived", err.Error())
	case errors.Is(err, machine.ErrArchived):
		writeErrCode(w, http.StatusConflict, "archived", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) archiveMachine(id string) (machine.Info, error) {
	in, err := s.machines.Archive(id)
	if err == nil {
		s.emitMachine(id)
	}
	return in, err
}

func (s *Server) handleMachineArchive(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	if !s.machineFree(w, id) {
		return
	}
	in, err := s.archiveMachine(id)
	if err != nil {
		machineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleMachineRestore(w http.ResponseWriter, r *http.Request, p *principal) {
	in, err := s.machines.Restore(r.PathValue("id"))
	if err != nil {
		machineErr(w, err)
		return
	}
	s.emitMachine(in.ID)
	writeJSON(w, http.StatusOK, in)
}

// handleMachineDelete archives (older clients' delete), or with ?permanent=1 deletes the machine,
// its sealed credentials and the host keys only it used.
func (s *Server) handleMachineDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if !permanent(r) {
		s.handleMachineArchive(w, r, p)
		return
	}
	id := r.PathValue("id")
	if !s.machineFree(w, id) {
		return
	}
	if err := s.machines.Remove(id); err != nil {
		machineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleMachinesBulk(w http.ResponseWriter, r *http.Request, p *principal) {
	req, ok := readBulk(w, r, "archive", "restore", "delete")
	if !ok {
		return
	}
	res := runBulk(req.IDs, 4, func(id string) (string, error) {
		if id == machine.LocalID {
			return "local", machine.ErrLocal
		}
		if _, ok := s.machines.SSH(id); !ok {
			return "machine_not_found", machine.ErrNotFound
		}
		if req.Action != "restore" {
			if refs, n := s.machineUsers(id); len(refs) > 0 || n > 0 {
				names := make([]string, 0, len(refs))
				for _, ref := range refs {
					names = append(names, ref.Name)
				}
				msg := "in use"
				if len(names) > 0 {
					msg = "in use by " + strings.Join(names, ", ")
				}
				return "in_use", errors.New(msg)
			}
		}
		var err error
		switch req.Action {
		case "archive":
			_, err = s.archiveMachine(id)
		case "restore":
			var in machine.Info
			if in, err = s.machines.Restore(id); err == nil {
				s.emitMachine(in.ID)
			}
		case "delete":
			err = s.machines.Remove(id)
		}
		code := "failed"
		if errors.Is(err, machine.ErrNotArchived) {
			code = "not_archived"
		}
		return code, err
	})
	s.events.Publish(map[string]any{"type": "machines"})
	writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

// ---- groups ----

func (s *Server) handleProjectGroupsBulk(w http.ResponseWriter, r *http.Request, p *principal) {
	req, ok := readBulk(w, r, "delete")
	if !ok {
		return
	}
	moved := map[string]bool{}
	res := runBulk(req.IDs, 1, func(id string) (string, error) {
		ids, err := s.projects.DeleteGroup(id)
		for _, pid := range ids {
			moved[pid] = true
		}
		if errors.Is(err, project.ErrGroupNotFound) {
			return "group_not_found", err
		}
		return "failed", err
	})
	s.emitProjectGroups()
	for pid := range moved {
		s.emitProject(pid)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

// decodeImage reads `"cover": {"assetId":"…"} | null` of a project PATCH.
func (s *Server) decodeImage(raw json.RawMessage) (*project.ImageChange, error) {
	if raw == nil {
		return nil, nil
	}
	if string(raw) == "null" {
		return &project.ImageChange{}, nil
	}
	var v struct {
		AssetID string `json:"assetId"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || !s.isImageAsset(v.AssetID) {
		return nil, errBadAsset
	}
	return &project.ImageChange{Image: &project.Image{AssetID: v.AssetID, Source: project.ImageUpload, At: time.Now().UTC()}}, nil
}

var errBadAsset = errors.New("assetId must name an uploaded image (POST /api/assets)")
