package server

import (
	"errors"
	"net/http"

	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// Where a new console works in its project (v0.9.0-beta.2, docs/API-board.md §1 Sessions): at
// the project root (cr.Root), in a part (cr.PartID), or, when the request names neither, by the
// compat rules from its machine and folder.

// errRootUnavailable: the project's root folder could not be made (500 root_unavailable).
var errRootUnavailable = errors.New("the project's root folder could not be made")

// placeSession fills in a new session's placement before session.Manager.Create: machine,
// folder and part, or the root. POST /api/sessions and the orchestration adapter
// (hubAdapter.CreateSession) both go through it, so a console is placed by one rule. Its errors
// go to the client through placeErr: project.ErrBadPlace (bad_placement), project.ErrPartNotFound
// (part_not_found), errPartMachine (part_machine), errRootUnavailable (root_unavailable).
//
// For now it is placeInPart, which knows no root; the root and the rest of the placement table
// come with the root folder.
func (s *Server) placeSession(proj project.Project, cr *session.CreateRequest) error {
	return placeInPart(proj, cr)
}

// placeErr writes an error of placeSession.
func placeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPartMachine):
		writeErrCode(w, http.StatusBadRequest, "part_machine", err.Error())
	case errors.Is(err, errRootUnavailable):
		writeErrCode(w, http.StatusInternalServerError, "root_unavailable", err.Error())
	default:
		partErr(w, err)
	}
}
