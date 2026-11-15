package orch

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// Placement errors of orchestration (v0.9.0-beta.2, docs/API-orchestration.md §5, §13). A spawn,
// a step, a handoff or a deploy lane placed at the root and in a part at once is
// project.ErrBadPlace (400 bad_placement), wrapped with %w; a part the project lacks is
// ErrPartNotFound (404 part_not_found).

// ErrPlaceUnmatched: a saved layout places groups or lanes in parts, by name, that this project
// does not have, and the deploy request placed none of them itself (409 place_unmatched). It
// comes as a *PlaceUnmatchedError, which lists them.
var ErrPlaceUnmatched = errors.New("the layout places consoles in parts this project does not have")

// UnmatchedPlace is one group or lane of a layout whose saved part is not in the project.
type UnmatchedPlace struct {
	// Key is the group's or the lane's key in the template.
	Key string `json:"key"`
	// Part is the part name the template saved.
	Part string `json:"part"`
}

// PlaceUnmatchedError is ErrPlaceUnmatched with the groups and lanes to place:
// {"error","code":"place_unmatched","unmatched":[{"key","part"}]}.
type PlaceUnmatchedError struct {
	Unmatched []UnmatchedPlace
}

func (e *PlaceUnmatchedError) Error() string {
	names := make([]string, 0, len(e.Unmatched))
	for _, u := range e.Unmatched {
		if !slices.Contains(names, u.Part) {
			names = append(names, u.Part)
		}
	}
	return fmt.Sprintf("%s: %s", ErrPlaceUnmatched.Error(), strings.Join(names, ", "))
}

func (e *PlaceUnmatchedError) Is(target error) bool { return target == ErrPlaceUnmatched }

// spot is where one new console goes: the project root, a part, or, when nothing placed it, a
// machine and folder of its own (the compat rule of POST /api/sessions, docs/API-board.md §1).
type spot struct {
	root    bool
	part    project.Part // placed in this part (ID set)
	machine string       // not placed: this machine
	cwd     string       // not placed: this folder ("" = its part's folder on that machine)
}

// placeIn resolves a placement for a new console. ok is false for a zero Place: the caller goes
// on to its next default. A single-part project's root is its part. Errors: project.ErrBadPlace
// (root and a part), ErrPartNotFound (a part the project lacks, by name).
func placeIn(proj project.Project, pl project.Place) (spot, bool, error) {
	root, pt, ok, err := proj.ResolvePlace(pl)
	switch {
	case errors.Is(err, project.ErrPartNotFound):
		return spot{}, false, fmt.Errorf("%w: %q", ErrPartNotFound, strings.TrimSpace(pl.Part))
	case err != nil:
		return spot{}, false, err
	case !ok:
		return spot{}, false, nil
	case root:
		return spot{root: true}, true, nil
	}
	return spot{part: pt}, true, nil
}

// unplaced is the compat spot of a console nothing placed: machineID ("" = the project's,
// that is the first part's, for clients that name no part) and cwd ("" = the folder of the first
// part, in display order, on that machine, as POST /api/sessions does).
func unplaced(proj project.Project, machineID, cwd string) spot {
	if machineID == "" {
		machineID = proj.DefaultPart().MachineID // compat: no part named
	}
	if machineID == "" {
		machineID = machine.LocalID
	}
	return spot{machine: machineID, cwd: cwd}
}

// machineID is where the console's agent is picked from.
func (sp spot) machineID() string {
	switch {
	case sp.root:
		return machine.LocalID
	case sp.part.ID != "":
		return sp.part.MachineID
	}
	return sp.machine
}

// fill writes the spot into a request. A part sends its id, machine and folder (the hub's
// placeSession checks them again); the root sends root on This PC with no folder, which the hub
// makes; a compat spot sends its machine and folder, and the part that folder is in.
func (sp spot) fill(proj project.Project, cr *session.CreateRequest) {
	cr.Root, cr.PartID = false, ""
	switch {
	case sp.root:
		cr.Root, cr.MachineID, cr.Cwd = true, machine.LocalID, ""
	case sp.part.ID != "":
		cr.PartID, cr.MachineID, cr.Cwd = sp.part.ID, sp.part.MachineID, sp.part.Path
	default:
		cr.MachineID, cr.Cwd = sp.machine, sp.cwd
		if cr.Cwd == "" {
			// compat: the folder of the first part on that machine
			for _, pt := range proj.Parts {
				if pt.MachineID == sp.machine {
					cr.Cwd, cr.PartID = pt.Path, pt.ID
					break
				}
			}
		}
	}
}

// placeOfSession is where a console works, as a Place: the root, its part (by id), or zero for
// one in no part of the project (a shell in ~, a part that is gone).
func placeOfSession(proj project.Project, in session.Info) project.Place {
	if in.Root {
		return project.Place{Root: true}
	}
	if pt, ok := proj.PartByID(in.PartID); ok {
		return project.Place{Part: pt.ID}
	}
	return project.Place{}
}

// editorPlace is the placement of an editor card as a key: "" for the Root card (data.part
// absent, empty, or a part that is gone), else its part's id. In a single-part project the
// root and the part are one placement, so every card there is "".
func editorPlace(proj project.Project, n *board.Node) string {
	if n == nil || len(proj.Parts) < 2 {
		return ""
	}
	var d struct {
		Part string `json:"part"`
	}
	_ = json.Unmarshal(n.Data, &d)
	if pt, ok := proj.PartByID(d.Part); ok {
		return pt.ID
	}
	return ""
}

// editorKey is the placement an editor card is asked for, as editorPlace gives it: a zero
// Place or the root is "", a part its id (in a single-part project "").
func editorKey(proj project.Project, pl project.Place) (string, error) {
	sp, ok, err := placeIn(proj, pl)
	if err != nil || !ok || sp.root || len(proj.Parts) < 2 {
		return "", err
	}
	return sp.part.ID, nil
}
