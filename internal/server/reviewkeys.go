package server

import (
	"log"
	"os"
	"path/filepath"

	"github.com/azamrizki/nextboard/internal/project"
)

// migrateReviewKeys moves what review keeps of each project's first part (its snapshots, Local
// History and server copies) from the bare project id, review/<projectId> as before
// v0.9.0-beta.2, to that part's key, review/<projectId>~<first part id> (project.PartKey). Then
// every part is keyed the same way, and moving or removing the first part moves nothing.
//
// It runs at every start, before setupReview (reviewKey returns project.KeyOf for every part).
// dir is the review folder
// (s.st.Path("review")), projects every project, archived ones too (Store.ListAll). It is
// idempotent: a project with nothing under its bare id is skipped, and so is one whose part key
// already has data (both folders are kept, and it is logged: the bare one cannot be told apart).
// A rename is atomic, so a crash leaves one of the two states. It returns how many folders moved.
func migrateReviewKeys(dir string, projects []project.Project) int {
	moved := 0
	for _, pr := range projects {
		if len(pr.Parts) == 0 {
			continue
		}
		src := filepath.Join(dir, pr.ID)
		dst := filepath.Join(dir, project.PartKey(pr.ID, pr.Parts[0].ID))
		if _, err := os.Lstat(src); err != nil {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			log.Printf("review: %s and %s both exist; the first part keeps %s", src, dst, dst)
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			log.Printf("review: moving %s to %s: %v", src, dst, err)
			continue
		}
		moved++
	}
	return moved
}
