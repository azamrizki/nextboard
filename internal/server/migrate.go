package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

const legacyBoardFile = "board.json"

// legacyBoard is the phase 1–3 board.json: one position per session.
type legacyBoard struct {
	Nodes map[string]board.Rect `json:"nodes"`
}

// migrate brings the data directory to the project/workspace model. Every
// step is idempotent, so a crash at any point is repaired on next start:
//
//  1. no projects.json yet → create "Home" (This PC, home folder) with
//     workspace "Main";
//  2. sessions without a known workspace move to the default workspace;
//  3. an old board.json becomes terminal nodes at their old positions,
//     then is renamed board.json.migrated;
//  4. every session gets a terminal node on its board (auto-placed);
//  5. a session in a project with no part and not at the root gets the part it works in
//     (v0.9.0-beta.2), so that "" stops meaning "the first part";
//  6. parts whose old names were not safe as folder names, renamed when the projects were
//     opened, are told in their project's thread.
func (s *Server) migrate() error {
	if !s.projects.Existed() {
		if _, _, err := s.projects.Create(machine.LocalID, homeDir(), "Home"); err != nil {
			return err
		}
	}
	for _, pl := range s.sessions.Placements() {
		if _, _, ok := s.projects.Workspace(pl.WorkspaceID); ok {
			continue
		}
		p, ws, err := s.defaultWorkspace()
		if err != nil {
			return err
		}
		if err := s.sessions.Assign(pl.ID, p.ID, ws.ID); err != nil {
			return err
		}
	}

	var old legacyBoard
	legacy := false
	if found, err := s.st.Load(legacyBoardFile, &old); err != nil {
		log.Printf("cannot read the old board.json, node positions ignored: %v", err)
		legacy = true
	} else {
		legacy = found
	}
	if legacy {
		for _, pl := range s.sessions.Placements() {
			r, ok := old.Nodes[pl.ID]
			if !ok || r.Validate() != nil || s.boards.HasTerminal(pl.WorkspaceID, pl.ID) {
				continue
			}
			if _, err := s.boards.AddTerminal(pl.WorkspaceID, pl.ID, &r); err != nil {
				log.Printf("migrating node %s failed: %v", pl.ID, err)
			}
		}
	}
	for _, pl := range s.sessions.Placements() {
		if _, err := s.boards.AddTerminal(pl.WorkspaceID, pl.ID, nil); err != nil {
			log.Printf("could not make terminal node %s: %v", pl.ID, err)
		}
	}
	s.boards.Flush()
	if legacy {
		if err := os.Rename(s.st.Path(legacyBoardFile), s.st.Path(legacyBoardFile+".migrated")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		log.Printf("old board.json migrated to the workspace boards")
	}
	if n := s.sessions.FillParts(s.partOfOldSession); n > 0 {
		log.Printf("migrate: %d sessions got the part they work in", n)
	}
	s.tellPartRenames(s.projects.TakePartRenames())
	return nil
}

// partOfOldSession is the part a session from before part ids works in: the part that holds its
// folder (on a server or in a read-only part, the folder it works on there), else the project's
// DefaultPart, which is what "" meant then.
func (s *Server) partOfOldSession(sp session.Spec) string {
	pr, ok := s.projects.Get(sp.ProjectID)
	if !ok {
		return ""
	}
	mid, cwd := sp.MachineID, sp.Cwd
	if sp.RemoteMachineID != "" {
		mid, cwd = sp.RemoteMachineID, sp.RemoteCwd
	}
	if strings.HasPrefix(cwd, "/") {
		if pt, ok := pr.PartAt(mid, path.Clean(cwd)); ok {
			return pt.ID
		}
	}
	return pr.DefaultPart().ID
}

// tellPartRenames logs every part Open renamed and tells the owner, in each project's thread,
// about the ones whose names were not safe as folder names at the project root.
func (s *Server) tellPartRenames(list []project.PartRename) {
	notes := map[string][]string{}
	var order []string
	for _, r := range list {
		log.Printf("projects: part %q of %s (%s) is now %q", r.From, r.ProjectName, r.ProjectID, r.To)
		if !r.Unsafe {
			continue
		}
		if _, ok := notes[r.ProjectID]; !ok {
			order = append(order, r.ProjectID)
		}
		notes[r.ProjectID] = append(notes[r.ProjectID], fmt.Sprintf("%q is now %q", r.From, r.To))
	}
	if s.orch == nil {
		return
	}
	for _, pid := range order {
		s.orch.Threads.Add(orch.ThreadMessage{ThreadID: pid, At: time.Now().UTC(), Role: "hub", Kind: "note",
			Text: "Renamed parts: " + strings.Join(notes[pid], ", ") + `. A part's name is also its folder's name at the project root, so it cannot hold "/" or "\", start with a dot, or be AGENTS.md or CLAUDE.md. Rename them in Edit parts if you like.`})
	}
}
