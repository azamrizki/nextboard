package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/azamrizki/nextboard/internal/machine"
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
// The rules, in order:
//   - root and partId: bad_placement.
//   - root in a single-part project: that part (root false in the answer), so such a project
//     never gets a root folder.
//   - root in a project of several parts: machineId absent or "local" and no cwd, else
//     bad_placement; then This PC, cwd the root folder (rootDirs.Sync), partId "". A sync that
//     fails is root_unavailable. Shells and agents alike; never remoteMachineId.
//   - partId, or neither: placeInPart (the part's machine and folder; the compat rules).
func (s *Server) placeSession(proj project.Project, cr *session.CreateRequest) error {
	if !cr.Root {
		return placeInPart(proj, cr)
	}
	if strings.TrimSpace(cr.PartID) != "" {
		return project.ErrBadPlace
	}
	root, pt, _, err := proj.ResolvePlace(project.Place{Root: true})
	if err != nil {
		return err
	}
	if !root {
		cr.Root, cr.PartID = false, pt.ID
		return placeInPart(proj, cr)
	}
	if (cr.MachineID != "" && cr.MachineID != machine.LocalID) || strings.TrimSpace(cr.Cwd) != "" {
		return project.ErrBadPlace
	}
	if s.roots == nil {
		return errRootUnavailable
	}
	dir, err := s.roots.Sync(proj.ID)
	if err != nil {
		return fmt.Errorf("%w: %v", errRootUnavailable, err)
	}
	cr.MachineID, cr.Cwd, cr.PartID = machine.LocalID, dir, ""
	return nil
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
