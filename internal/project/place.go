package project

import (
	"errors"
	"strings"
)

// Placement in a project and the stable keys of its parts (v0.9.0-beta.2, docs/API-board.md §1).
// A console or an editor card works at the project root, reaching every part, or in one part.
// Every part is keyed the same way, so moving or removing the first one changes no key.

// Place is where a console works in a project: at the root (Root), reaching every part, or in
// one part (Part: its id, or its name in any case). Neither is the caller's default; both is
// ErrBadPlace. Wire form: {"root":true} or {"part":"<id or name>"}.
type Place struct {
	Root bool   `json:"root,omitempty"`
	Part string `json:"part,omitempty"`
}

// IsZero reports that no placement was named, so the caller's default applies.
func (pl Place) IsZero() bool { return !pl.Root && strings.TrimSpace(pl.Part) == "" }

// ErrBadPlace: a request placed a console at the root and in a part at once, or at the root
// with a machine or folder of its own (400 bad_placement). Orchestration returns it too (wrapped
// with %w); there is no separate orch error for it.
var ErrBadPlace = errors.New("place a console at the project root or in one part, not both")

// ResolvePlace resolves a placement in the project. root: at the root (only in a project of two
// or more parts); pt: the part, found by PartNamed. A single-part project's root is its part
// (root false, pt that part), so a single-part project never has a root of its own. ok is false
// for a zero Place: the caller applies its default. Errors: ErrBadPlace for both, ErrPartNotFound
// for a part the project lacks (or a project with no part).
func (p Project) ResolvePlace(pl Place) (root bool, pt Part, ok bool, err error) {
	name := strings.TrimSpace(pl.Part)
	switch {
	case pl.Root && name != "":
		return false, Part{}, false, ErrBadPlace
	case pl.Root:
		switch len(p.Parts) {
		case 0:
			return false, Part{}, false, ErrPartNotFound
		case 1:
			return false, p.Parts[0], true, nil
		}
		return true, Part{}, true, nil
	case name != "":
		pt, found := p.PartNamed(name)
		if !found {
			return false, Part{}, false, ErrPartNotFound
		}
		return false, pt, true, nil
	}
	return false, Part{}, false, nil
}

// PartKeySep separates the project id from the part id in a part key and a project ref.
const PartKeySep = "~"

// PartKey is the stable key of one part of a project: "<projectId>~<partId>", for every part,
// the first one included. Review and Local History (the folder review/<key>), the git service's
// state and its gitPart events, and the web's refs and studio state (lib/parts.ts partRef) all
// use it. It is also a project ref, which every project route accepts. Pass a real part id: with
// "" it gives the bare project id, a ref "without a part" that routes read as DefaultPart for old
// clients, and never a key to store. Project.KeyOf resolves "" for you.
func PartKey(projectID, partID string) string {
	if partID == "" {
		return projectID
	}
	return projectID + PartKeySep + partID
}

// SplitPartKey splits a part key or a project ref: "<projectId>~<partId>", or a bare project id
// (partID "").
func SplitPartKey(key string) (projectID, partID string) {
	projectID, partID, _ = strings.Cut(key, PartKeySep)
	return projectID, partID
}

// KeyOf is the part key of one of the project's parts; partID "" is DefaultPart (what a session,
// monitor or ref with no part meant before part ids were stamped). It never returns a bare
// project id while the project has a part.
func (p Project) KeyOf(partID string) string {
	if partID == "" {
		partID = p.DefaultPart().ID
	}
	return PartKey(p.ID, partID)
}
