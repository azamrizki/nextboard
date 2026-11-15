package orch

import (
	"errors"
	"fmt"
	"slices"
	"strings"
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
