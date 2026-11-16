package orch

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
)

// Part memories (docs/API-orchestration.md §12). A project in several parts keeps what holds for
// the whole project in its main memory, and what holds for one part only on that part's page: a
// sub-memory named PartSlug(part id). The page's title is the part's name as it is now and its
// description says where the part is, so a rename shows at once; what a client sends for them is
// not kept. A project in one part has no part pages: its part is the whole project, and a page of
// that name is an ordinary one.

const partSlugPrefix = "part-"

// PartSlug is the name of a part's page in its project's memory. Part ids are "pt_" and 10 of
// [a-z2-9], so the name is a valid sub-memory name.
func PartSlug(partID string) string { return partSlugPrefix + strings.TrimPrefix(partID, "pt_") }

// partSlugRe is the shape PartSlug gives: only such names are kept for parts, so a page called
// "part-time-work" is an ordinary page.
var partSlugRe = regexp.MustCompile(`^part-[a-z2-9]{10}$`)

// IsPartSlug reports a name a part's page could have.
func IsPartSlug(name string) bool { return partSlugRe.MatchString(name) }

// partPages maps the page names of a project's parts to the parts; nil for a project in one part.
func (s *Service) partPages(pid string) map[string]project.Part {
	if pid == "" {
		return nil
	}
	p, ok := s.hub.Project(pid)
	if !ok || len(p.Parts) < 2 {
		return nil
	}
	out := make(map[string]project.Part, len(p.Parts))
	for _, pt := range p.Parts {
		out[PartSlug(pt.ID)] = pt
	}
	return out
}

// pagePart is the part whose page slug is.
func (s *Service) pagePart(pid, slug string) (project.Part, bool) {
	if !IsPartSlug(slug) {
		return project.Part{}, false
	}
	pt, ok := s.partPages(pid)[slug]
	return pt, ok
}

func (s *Service) machineName(id string) string {
	if s.MachineName != nil {
		if n := s.MachineName(id); n != "" {
			return n
		}
	}
	if id == machine.LocalID {
		return "This PC"
	}
	return id
}

func (s *Service) partDesc(pt project.Part) string {
	return fmt.Sprintf("What holds for the %s part only (%s, %s).", pt.Name, s.machineName(pt.MachineID), pt.Path)
}

// asPart shows a part's page as one: its part, the part's name and where it is.
func (s *Service) asPart(pages map[string]project.Part, sub *SubMemory) {
	if pt, ok := pages[sub.Slug]; ok {
		sub.Part, sub.Title, sub.Description = pt.ID, pt.Name, s.partDesc(pt)
	}
}

// subs lists a main memory's sub-memories, the part pages as such.
func (s *Service) subs(pid string) []SubMemory {
	out := s.Memory.Subs(pid)
	pages := s.partPages(pid)
	if pages == nil {
		return out
	}
	for i := range out {
		s.asPart(pages, &out[i])
	}
	sortSubs(out)
	return out
}

// PartPage names the page of the part an agent names (its name or id): "main" in a project in one
// part, whose part is the whole project.
func (s *Service) PartPage(pid, name string) (string, error) {
	p, ok := s.hub.Project(pid)
	if !ok {
		return "", ErrProjectNotFound
	}
	pt, ok := p.PartNamed(name)
	if !ok {
		names := make([]string, len(p.Parts))
		for i, x := range p.Parts {
			names[i] = x.Name
		}
		return "", fmt.Errorf("%w: %q; the parts are %s", ErrPartNotFound, name, strings.Join(names, ", "))
	}
	if len(p.Parts) < 2 {
		return MainDoc, nil
	}
	return PartSlug(pt.ID), nil
}

// factParts turns the part names a run gave its facts into part ids; a name the project does not
// have, and any name in a project of one part, is the whole project's.
func (s *Service) factParts(pid string, facts []Fact) []Fact {
	p, ok := s.hub.Project(pid)
	for i := range facts {
		part := ""
		if ok && len(p.Parts) > 1 {
			if pt, found := p.PartNamed(facts[i].Part); found {
				part = pt.ID
			}
		}
		facts[i].Part = part
	}
	return facts
}

// AcceptResult is what accepting facts wrote: the main memory, and the part pages by part id.
type AcceptResult struct {
	MemoryDoc
	Parts map[string]MemoryDoc `json:"parts,omitempty"`
}

// AcceptMemoryFacts adds accepted facts where they belong: a fact of one part on that part's page,
// made when missing, the others in the main memory. A part the project no longer has is the whole
// project's.
func (s *Service) AcceptMemoryFacts(pid, scope string, facts []Fact) (AcceptResult, error) {
	var root []Fact
	byPart := map[string][]Fact{}
	var order []project.Part
	pages := map[string]project.Part{}
	if scope == "project" {
		pages = s.partPages(pid)
	}
	p, _ := s.hub.Project(pid)
	for _, f := range facts {
		pt, ok := p.PartNamed(f.Part)
		if _, paged := pages[PartSlug(pt.ID)]; !ok || !paged {
			root = append(root, f)
			continue
		}
		if byPart[pt.ID] == nil {
			order = append(order, pt)
		}
		byPart[pt.ID] = append(byPart[pt.ID], f)
	}
	var res AcceptResult
	// the parts' pages first, then the main memory: a page that cannot take its facts (64 pages
	// already) stops the accept before the main memory changes
	for _, pt := range order {
		d, err := s.acceptPart(pid, pt, byPart[pt.ID])
		if err != nil {
			return AcceptResult{}, err
		}
		if res.Parts == nil {
			res.Parts = map[string]MemoryDoc{}
		}
		res.Parts[pt.ID] = d
	}
	var err error
	if len(root) > 0 || len(order) == 0 {
		res.MemoryDoc, err = s.AcceptMemory(pid, scope, root)
		if errors.Is(err, ErrMtimeMismatch) { // someone else just wrote
			res.MemoryDoc, err = s.AcceptMemory(pid, scope, root)
		}
	} else {
		res.MemoryDoc, err = s.Memory.Read(scope, pid)
	}
	if err != nil {
		return AcceptResult{}, err
	}
	return res, nil
}

// acceptPart adds facts to a part's page, made when missing.
func (s *Service) acceptPart(pid string, pt project.Part, facts []Fact) (MemoryDoc, error) {
	slug := PartSlug(pt.ID)
	// read, add and write in one step: two first writers of the page both keep their facts
	d, wrote, err := s.Memory.UpdateSub(pid, slug, func(cur SubMemory, _ bool) (string, string, string, bool) {
		next := AcceptFacts(cur.Content, facts)
		return pt.Name, s.partDesc(pt), next, next != cur.Content
	})
	if err != nil {
		return MemoryDoc{}, err
	}
	if wrote {
		s.publishMemory(pid, slug)
	}
	return MemoryDoc{Content: d.Content, Mtime: d.Mtime}, nil
}

// partFactsNote is what Scan and Init add to their prompt for a project in several parts: every
// fact says the part it holds for, and what each part's page has already.
func (s *Service) partFactsNote(p project.Project) string {
	if len(p.Parts) < 2 {
		return ""
	}
	names := make([]string, len(p.Parts))
	for i, pt := range p.Parts {
		names[i] = fmt.Sprintf("%q", pt.Name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nEach part has a memory of its own besides the main one. Give every fact a \"part\": the name of the one part it holds for (%s), or \"\" when it holds for the whole project or for more than one part. A fact about one part names that part in \"part\", not in its text.",
		strings.Join(names, ", "))
	// what the parts' pages hold, within one launch's budget: eight full pages would crowd out the code
	left := memoryPromptChars
	for _, pt := range p.Parts {
		d, err := s.Memory.Sub(p.ID, PartSlug(pt.ID))
		text := strings.TrimSpace(d.Content)
		if err != nil || text == "" {
			continue
		}
		if r := []rune(text); len(r) > left {
			if left < 200 {
				fmt.Fprintf(&b, "\n\nThe part %s has a memory of its own too; it did not fit here.", pt.Name)
				continue
			}
			text = string(r[:left]) + "\n(cut here: the rest of this part's memory is not shown)"
		}
		left -= len([]rune(text))
		fmt.Fprintf(&b, "\n\nExisting memory of the part %s:\n%s", pt.Name, text)
	}
	return b.String()
}

// withPartField adds a required "part" to a schema's facts for a project in several parts: one of
// its part names, or "" for the whole project.
func withPartField(schema json.RawMessage, p project.Project) json.RawMessage {
	if len(p.Parts) < 2 {
		return schema
	}
	var v map[string]any
	if json.Unmarshal(schema, &v) != nil {
		return schema
	}
	props, _ := v["properties"].(map[string]any)
	facts, _ := props["facts"].(map[string]any)
	item, _ := facts["items"].(map[string]any)
	fields, _ := item["properties"].(map[string]any)
	required, _ := item["required"].([]any)
	if fields == nil {
		return schema
	}
	names := []any{""}
	for _, pt := range p.Parts {
		names = append(names, pt.Name)
	}
	fields["part"] = map[string]any{"type": "string", "enum": names}
	item["required"] = append(required, "part")
	out, err := json.Marshal(v)
	if err != nil {
		return schema
	}
	return out
}

// ---- when parts go ----

// PartRemoved keeps what was learned about a part taken out of its project: its page stays, as an
// ordinary page named for what it was. before is the project as it was.
func (s *Service) PartRemoved(before project.Project, partID string) {
	pt, ok := before.PartByID(partID)
	if !ok || len(before.Parts) < 2 {
		return
	}
	slug := PartSlug(partID)
	desc := fmt.Sprintf("What held for the %s part (%s, %s) before it was removed from this project.", pt.Name, s.machineName(pt.MachineID), pt.Path)
	// renamed with what it holds at that moment: a fact saved meanwhile stays
	if _, wrote, err := s.Memory.UpdateSub(before.ID, slug, func(cur SubMemory, exists bool) (string, string, string, bool) {
		return pt.Name + " (removed part)", desc, cur.Content, exists
	}); err != nil {
		log.Printf("memory of the removed part %s: %v", partID, err)
	} else if wrote {
		s.publishMemory(before.ID, slug)
	}
	s.settleLastPart(before.ID)
}

// PartSplit moves a part's page along when the part becomes a project of its own (to): there it
// is the whole project, so its facts join that project's main memory. If they do not fit, the
// page stays where it was, as an ordinary page.
func (s *Service) PartSplit(before project.Project, to, partID string) {
	pt, ok := before.PartByID(partID)
	if !ok || len(before.Parts) < 2 {
		return
	}
	slug := PartSlug(partID)
	cur, err := s.Memory.Sub(before.ID, slug)
	if err != nil {
		s.settleLastPart(before.ID)
		return
	}
	moved := strings.TrimSpace(cur.Content) == ""
	if !moved {
		main, err := s.Memory.Read("project", to)
		next := cur.Content
		if err == nil && strings.TrimSpace(main.Content) != "" {
			next = mergeLines(main.Content, cur.Content)
		}
		if _, err := s.WriteMemory(to, "project", next, ""); err == nil {
			moved = true
		} else {
			log.Printf("memory of the split part %s: %v", partID, err)
		}
	}
	if moved {
		if err := s.DeleteDoc(before.ID, slug); err != nil {
			log.Printf("memory of the split part %s: %v", partID, err)
		}
	} else {
		desc := fmt.Sprintf("What held for the %s part (%s, %s) before it became a project of its own.", pt.Name, s.machineName(pt.MachineID), pt.Path)
		if _, wrote, err := s.Memory.UpdateSub(before.ID, slug, func(now SubMemory, exists bool) (string, string, string, bool) {
			return pt.Name + " (split off)", desc, now.Content, exists
		}); err == nil && wrote {
			s.publishMemory(before.ID, slug)
		}
	}
	s.settleLastPart(before.ID)
}

// settleLastPart: in a project left with one part, that part's page is an ordinary page again.
// It keeps the part's name and place as they are now, not as they were when last written.
func (s *Service) settleLastPart(pid string) {
	p, ok := s.hub.Project(pid)
	if !ok || len(p.Parts) != 1 {
		return
	}
	pt := p.Parts[0]
	slug := PartSlug(pt.ID)
	desc := s.partDesc(pt)
	if _, wrote, err := s.Memory.UpdateSub(pid, slug, func(cur SubMemory, exists bool) (string, string, string, bool) {
		return pt.Name, desc, cur.Content, exists
	}); err == nil && wrote {
		s.publishMemory(pid, slug)
	}
}

// MergedPartPages carries the part pages of a project merged into another (to) over to the parts
// its parts became there (partIDs: old id → new id). It returns the pages it carried, which the
// merge leaves out of the pages it copies under the other project's name.
func (s *Service) MergedPartPages(from project.Project, to string, partIDs map[string]string) map[string]bool {
	done := map[string]bool{}
	if len(from.Parts) < 2 {
		return done
	}
	for _, pt := range from.Parts {
		slug, next := PartSlug(pt.ID), partIDs[pt.ID]
		cur, err := s.Memory.Sub(from.ID, slug)
		if err != nil || next == "" {
			continue
		}
		into := PartSlug(next)
		if _, _, err := s.Memory.UpdateSub(to, into, func(have SubMemory, exists bool) (string, string, string, bool) {
			if exists {
				return pt.Name, cur.Description, mergeLines(have.Content, cur.Content), true
			}
			return pt.Name, cur.Description, cur.Content, true
		}); err != nil {
			log.Printf("memory of the merged part %s: %v", pt.ID, err)
			continue
		}
		done[slug] = true
	}
	return done
}

// ---- for agents ----

// partInline is a console's own part page for its launch: the page, and the text that fits.
func partInline(content string, left int) (string, bool) {
	text := strings.TrimSpace(content)
	if text == "" || left <= 0 {
		return "", false
	}
	if r := []rune(text); len(r) > left {
		return string(r[:left]) + partCutNote, true
	}
	return text, false
}

const partCutNote = "\n(Your part's memory goes on; memory_read with your part shows all of it.)"

// partSaveNote tells a console of a project in several parts where a fact goes.
func partSaveNote(pt *project.Part, nativeMemory bool) string {
	main := "the main memory"
	if nativeMemory {
		main = "your memory folder (the main memory)"
	}
	if pt == nil {
		return fmt.Sprintf("This project is in several parts. What holds for the whole project goes in %s; what holds for one part only goes on that part's page: save it with memory_save and part set to the part's name.\n", main)
	}
	return fmt.Sprintf("This project is in several parts and you work in the part %s. What holds for the whole project goes in %s; what holds only for your part goes on its page: save it with memory_save and part: %q.\n", pt.Name, main, pt.Name)
}
