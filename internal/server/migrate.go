package server

import (
	"errors"
	"io/fs"
	"log"
	"os"

	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/machine"
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
//  4. every session gets a terminal node on its board (auto-placed).
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
		log.Printf("board.json lama tidak terbaca, posisi node diabaikan: %v", err)
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
				log.Printf("migrasi node %s gagal: %v", pl.ID, err)
			}
		}
	}
	for _, pl := range s.sessions.Placements() {
		if _, err := s.boards.AddTerminal(pl.WorkspaceID, pl.ID, nil); err != nil {
			log.Printf("node terminal %s gagal dibuat: %v", pl.ID, err)
		}
	}
	s.boards.Flush()
	if legacy {
		if err := os.Rename(s.st.Path(legacyBoardFile), s.st.Path(legacyBoardFile+".migrated")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		log.Printf("board.json lama dimigrasi ke board workspace")
	}
	return nil
}
