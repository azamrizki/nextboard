package server

import (
	"errors"
	"net/http"
	"regexp"

	"nextboard/internal/board"
	"nextboard/internal/project"
)

var clientIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)

func (s *Server) boardErr(w http.ResponseWriter, err error) {
	var ve *board.ValidationError
	switch {
	case errors.As(err, &ve) && ve.Code != "":
		st := ve.Status
		if st == 0 {
			st = http.StatusBadRequest
		}
		writeErrCode(w, st, ve.Code, ve.Msg)
	case errors.As(err, &ve):
		writeErr(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, board.ErrNotFound), errors.Is(err, board.ErrSnapshotNotFound), errors.Is(err, board.ErrArchiveNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, board.ErrReadOnly):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// workspace resolves {id} to an existing workspace or writes 404.
func (s *Server) workspace(w http.ResponseWriter, r *http.Request) (project.Workspace, project.Project, bool) {
	ws, pr, ok := s.projects.Workspace(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, project.ErrWorkspaceNotFound.Error())
	}
	return ws, pr, ok
}

func (s *Server) handleBoardGet(w http.ResponseWriter, r *http.Request, p *principal) {
	ws, _, ok := s.workspace(w, r)
	if !ok {
		return
	}
	b, err := s.boards.Get(ws.ID)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// handleBoardOps applies a client op batch. baseRev is informational only:
// stale batches are not rejected, clients converge through the broadcast.
func (s *Server) handleBoardOps(w http.ResponseWriter, r *http.Request, p *principal) {
	ws, _, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var req struct {
		ClientID string     `json:"clientId"`
		BaseRev  int64      `json:"baseRev"`
		Ops      []board.Op `json:"ops"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !clientIDRe.MatchString(req.ClientID) {
		writeErr(w, http.StatusBadRequest, "clientId tidak valid")
		return
	}
	rev, err := s.boards.Apply(ws.ID, req.ClientID, req.Ops)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"rev": rev})
}

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request, p *principal) {
	ws, _, ok := s.workspace(w, r)
	if !ok {
		return
	}
	list, err := s.boards.Snapshots(ws.ID)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSnapshotRestore(w http.ResponseWriter, r *http.Request, p *principal) {
	ws, _, ok := s.workspace(w, r)
	if !ok {
		return
	}
	b, err := s.boards.RestoreSnapshot(ws.ID, r.PathValue("sid"))
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.boards.ListArchive())
}

// handleArchiveRestore brings an archived board back as a new workspace of
// a project (without terminal nodes).
func (s *Server) handleArchiveRestore(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		ProjectID string `json:"projectId"`
	}
	if !decode(w, r, &req) {
		return
	}
	meta, err := s.boards.ArchiveMetaOf(r.PathValue("id"))
	if err != nil {
		s.boardErr(w, err)
		return
	}
	if _, ok := s.projects.Get(req.ProjectID); !ok {
		writeErr(w, http.StatusNotFound, project.ErrNotFound.Error())
		return
	}
	name, err := project.CleanName(meta.WorkspaceName)
	if err != nil {
		name = "Restored"
	}
	ws, pr, err := s.projects.AddWorkspace(req.ProjectID, name)
	if err != nil {
		projectErr(w, err)
		return
	}
	if _, err := s.boards.RestoreArchive(meta.ID, ws.ID); err != nil {
		_, _, _ = s.projects.DeleteWorkspace(ws.ID)
		s.boards.Drop(ws.ID)
		s.boardErr(w, err)
		return
	}
	s.emitProject(pr.ID)
	writeJSON(w, http.StatusCreated, s.workspaceView(ws))
}
