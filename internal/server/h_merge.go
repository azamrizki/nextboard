package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/project"
)

// Merge and split (docs/API-board.md §1, ROADMAP #49). Merging moves a project that was kept apart
// into another as parts: its folders, boards, terminals (none restarts), monitors, loops, memory
// (as a sub-memory), and review. Its critical notes join only when the owner says so. Splitting
// makes a part a project of its own again. Folders on disk are never touched.

// mergePreview is GET /api/projects/{id}/merge?from=: what merging would do, or why it cannot.
type mergePreview struct {
	From       string              `json:"from"`
	Parts      []project.Part      `json:"parts"`
	Workspaces []project.Workspace `json:"workspaces"`
	Sessions   int                 `json:"sessions"`
	Running    int                 `json:"running"`
	Monitors   int                 `json:"monitors"`
	Loops      int                 `json:"loops"`
	Memory     int                 `json:"memoryChars"`
	Subs       int                 `json:"subMemories"`
	Critical   int                 `json:"criticalChars"`
	CriticalOK bool                `json:"criticalFits"`
	PublicPage bool                `json:"publicPage"`
	Problem    string              `json:"problem,omitempty"`
	Code       string              `json:"code,omitempty"`
	partIDs    map[string]string
	fromProj   project.Project
}

func (s *Server) mergePreviewOf(into, from string) (mergePreview, error) {
	a, ok := s.projects.Get(into)
	if !ok {
		return mergePreview{}, project.ErrNotFound
	}
	b, ok := s.projects.Get(from)
	if !ok {
		return mergePreview{}, project.ErrNotFound
	}
	v := mergePreview{From: b.ID, fromProj: b, Parts: []project.Part{}, Workspaces: []project.Workspace{}}
	plan, err := s.projects.PlanMerge(a.ID, b.ID)
	if err != nil {
		return v, err
	}
	v.Parts, v.Workspaces, v.partIDs = plan.Parts, plan.Workspaces, plan.PartIDs
	for _, pl := range s.sessions.AllPlacements() {
		if pl.ProjectID == b.ID {
			v.Sessions++
			if pl.Running {
				v.Running++
			}
		}
	}
	v.Monitors = len(s.uptime.List(b.ID))
	v.PublicPage = s.uptime.Settings(b.ID).Public.Enabled
	if s.orch != nil {
		v.Loops = len(s.orch.Loops.List(b.ID))
		if doc, err := s.orch.Memory.Read("project", b.ID); err == nil {
			v.Memory = len(doc.Content)
		}
		for _, n := range s.orch.Memory.Notes(b.ID) {
			v.Memory += len(n.Content)
		}
		for _, sub := range s.orch.Memory.Subs(b.ID) {
			if sub.Critical {
				v.Critical = sub.Chars
			} else {
				v.Subs++
			}
		}
		mine := 0
		if c, err := s.orch.Memory.Sub(a.ID, orch.CriticalSlug); err == nil {
			mine = len(c.Content)
		}
		v.CriticalOK = mine+v.Critical+len(criticalJoin(b.Name)) <= criticalMax
	}
	return v, nil
}

// criticalMax is orch's limit on critical notes (16 KB).
const criticalMax = 16 << 10

func criticalJoin(name string) string { return "\n\n## From " + name + "\n\n" }

func mergeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrMergeSelf):
		writeErrCode(w, http.StatusBadRequest, "merge_self", err.Error())
	default:
		partErr(w, err)
	}
}

func (s *Server) handleMergePreview(w http.ResponseWriter, r *http.Request, p *principal) {
	v, err := s.mergePreviewOf(r.PathValue("id"), r.URL.Query().Get("from"))
	if errors.Is(err, project.ErrNotFound) {
		writeErrCode(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	if err != nil {
		// still a preview: it says why the merge cannot happen
		v.Problem = err.Error()
		switch {
		case errors.Is(err, project.ErrTooManyParts):
			v.Code = "too_many_parts"
		case errors.Is(err, project.ErrPartOverlap):
			v.Code = "part_overlap"
		case errors.Is(err, project.ErrMergeSelf):
			v.Code = "merge_self"
		}
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleMerge(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		From string `json:"from"`
		// Critical: "append" adds the other project's critical notes under a heading; else they stay out.
		Critical string `json:"critical"`
	}
	if !decode(w, r, &req) {
		return
	}
	into := r.PathValue("id")
	v, err := s.mergePreviewOf(into, req.From)
	if errors.Is(err, project.ErrNotFound) {
		writeErrCode(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	if err != nil {
		mergeErr(w, err)
		return
	}
	if req.Critical == "append" && v.Critical > 0 && !v.CriticalOK {
		writeErrCode(w, http.StatusConflict, "critical_too_big", "the critical notes of both projects together are larger than 16 KB; shorten them first")
		return
	}
	a, b, plan, err := s.projects.Merge(into, req.From)
	if err != nil {
		mergeErr(w, err)
		return
	}
	part := func(old string) string {
		if id, ok := plan.PartIDs[old]; ok {
			return id
		}
		return plan.PartIDs[""]
	}
	sessions := s.sessions.MoveProject(b.ID, a.ID, part)
	monitors := s.uptime.MoveProject(b.ID, a.ID, func(old string) string {
		if old == "" {
			return part("") // it watched what the other project was: its first folder
		}
		return part(old)
	})
	loops := 0
	if s.orch != nil {
		loops = s.orch.Loops.MoveProject(b.ID, a.ID)
		s.mergeMemory(a, b, req.Critical == "append")
	}
	// review: each of its parts keeps its history under the name it has now
	for _, pt := range b.Parts {
		s.review.Rename(reviewKey(b, pt.ID), reviewKey(a, part(pt.ID)))
	}
	if s.orch != nil {
		s.orch.Threads.Merge(a.ID, b.ID) // its lead's thread joins this one's, in time order
		s.orch.Threads.Add(orch.ThreadMessage{ThreadID: a.ID, At: time.Now().UTC(), Role: "hub", Kind: "note",
			Text: fmt.Sprintf("%s is now part of this project: %s. Its boards, %d terminals, %d monitors and %d loops came along, and its memory is the sub-memory %q.",
				b.Name, partList(plan.Parts), sessions, monitors, loops, mergeSlug(b.Name))})
	}
	s.emitProject(a.ID)
	s.events.Publish(map[string]any{"type": "projectRemoved", "id": b.ID, "mergedInto": a.ID})
	writeJSON(w, http.StatusOK, s.projectView(a))
}

func partList(parts []project.Part) string {
	names := make([]string, 0, len(parts))
	for _, pt := range parts {
		names = append(names, pt.Name)
	}
	return strings.Join(names, ", ")
}

var slugCut = regexp.MustCompile(`[^a-z0-9]+`)

// mergeSlug names the sub-memory a merged project's memory becomes.
func mergeSlug(name string) string {
	s := strings.Trim(slugCut.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" || s == orch.CriticalSlug || s == orch.MainDoc {
		s = "merged-" + s
	}
	return strings.Trim(s, "-")
}

// mergeMemory keeps what was learned about the other project: its memory and notes as one
// sub-memory, its own sub-memories under its name, and its critical notes only when asked.
func (s *Server) mergeMemory(a, b project.Project, critical bool) {
	m := s.orch.Memory
	slug := mergeSlug(b.Name)
	taken := map[string]bool{}
	for _, sub := range m.Subs(a.ID) {
		taken[sub.Slug] = true
	}
	free := func(base string) string {
		if len(base) > 48 {
			base = strings.TrimRight(base[:48], "-")
		}
		c := base
		for n := 2; taken[c]; n++ {
			c = fmt.Sprintf("%s-%d", strings.TrimRight(base[:min(len(base), 45)], "-"), n)
		}
		taken[c] = true
		return c
	}
	var body strings.Builder
	if doc, err := m.Read("project", b.ID); err == nil && strings.TrimSpace(doc.Content) != "" {
		body.WriteString(strings.TrimSpace(doc.Content) + "\n")
	}
	for _, n := range m.Notes(b.ID) {
		if strings.TrimSpace(n.Content) == "" {
			continue
		}
		fmt.Fprintf(&body, "\n## %s\n\n%s\n", n.Name, strings.TrimSpace(n.Content))
	}
	if body.Len() > 0 {
		text := body.String()
		if len(text) > orch.MaxMemory {
			text = text[:orch.MaxMemory-200] + "\n\n(cut: the rest is in the merged project's memory folder)\n"
		}
		if _, err := m.WriteSub(a.ID, free(slug), b.Name, "Memory of "+b.Name+" before it became a part of this project", text, ""); err != nil {
			log.Printf("merge memory of %s: %v", b.ID, err)
		}
	}
	for _, sub := range m.Subs(b.ID) {
		full, err := m.Sub(b.ID, sub.Slug)
		if err != nil {
			continue
		}
		if sub.Critical {
			if !critical || strings.TrimSpace(full.Content) == "" {
				continue
			}
			mine, _ := m.Sub(a.ID, orch.CriticalSlug)
			if _, err := m.WriteSub(a.ID, orch.CriticalSlug, "", "", strings.TrimRight(mine.Content, "\n")+criticalJoin(b.Name)+strings.TrimSpace(full.Content)+"\n", ""); err != nil {
				log.Printf("merge critical notes of %s: %v", b.ID, err)
			}
			continue
		}
		if _, err := m.WriteSub(a.ID, free(slug+"-"+sub.Slug), b.Name+": "+full.Title, full.Description, full.Content, ""); err != nil {
			log.Printf("merge sub-memory %s of %s: %v", sub.Slug, b.ID, err)
		}
	}
}

// handleSplit makes a part a project of its own. Terminals in it must be closed first.
func (s *Server) handleSplit(w http.ResponseWriter, r *http.Request, p *principal) {
	pid, partID := r.PathValue("id"), r.PathValue("part")
	n := 0
	for _, pl := range s.sessions.AllPlacements() {
		if pl.ProjectID == pid && pl.PartID == partID && partID != "" {
			n++
		}
	}
	if n > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "close the terminals in that part first", "code": "part_busy", "sessions": n})
		return
	}
	before, _ := s.projects.Get(pid)
	np, pr, err := s.projects.Split(pid, partID)
	if err != nil {
		partErr(w, err)
		return
	}
	s.review.Rename(reviewKey(before, partID), np.ID)
	s.emitProject(pr.ID)
	s.emitProject(np.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"project": s.projectView(np), "from": s.projectView(pr)})
}
