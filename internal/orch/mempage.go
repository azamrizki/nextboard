package orch

import (
	"errors"
	"fmt"
	"strings"
)

// Pages that agents make and fill (docs/API-orchestration.md §12). Asked to "make a sub-memory for
// the API", an agent names a page that does not exist yet: memory_save makes it, named as the
// agent wrote it (lowercase, with hyphens), with a title and a description saying when to read
// it. memory_move then takes facts out of one memory and puts them into another in one step, so
// an agent can sort the main memory into pages without leaving copies behind. Critical notes are
// the owner's: neither tool touches them, and the memory of all projects stays the owner's too.

// ErrPageName: a page name an agent gave that makes no page name.
var ErrPageName = errors.New("a page name needs letters or digits, such as \"api\" or \"design-system\"")

// ErrFactNotFound: none of the facts to move is in the memory they were to come from.
var ErrFactNotFound = errors.New("none of those facts is in that memory: give them as memory_read shows them")

// pageFor finds the page an agent means in a project: by its name, or by its title whatever the
// case ("API" finds the page api). It returns the name to write and whether the page exists.
func (s *Service) pageFor(pid, name string) (string, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, MainDoc) {
		return MainDoc, true, nil
	}
	subs := s.Memory.Subs(pid)
	for _, sub := range subs {
		if sub.Slug == name {
			return sub.Slug, true, nil
		}
	}
	for _, sub := range subs {
		if strings.EqualFold(sub.Title, name) || sub.Slug == slugOf(name) {
			return sub.Slug, true, nil
		}
	}
	slug := slugOf(name)
	if slug == "" || !subSlugRe.MatchString(slug) {
		return "", false, ErrPageName
	}
	if slug == CriticalSlug {
		return "", false, ErrCriticalOwner
	}
	if IsPartSlug(slug) && s.partPages(pid) != nil {
		if _, ok := s.pagePart(pid, slug); !ok {
			return "", false, ErrPartNotFound
		}
	}
	return slug, false, nil
}

// addToPage adds facts to a page of a project, making it when missing (title and desc then name
// it; a part's page always takes its part's name). It returns the page's name and whether it
// was made.
func (s *Service) addToPage(pid, name, title, desc string, facts []Fact) (string, bool, error) {
	slug, _, err := s.pageFor(pid, name)
	if err != nil {
		return "", false, err
	}
	if slug == MainDoc {
		_, err := s.AcceptMemory(pid, "project", facts)
		if errors.Is(err, ErrMtimeMismatch) { // someone else just wrote
			_, err = s.AcceptMemory(pid, "project", facts)
		}
		return MainDoc, false, err
	}
	if pt, ok := s.pagePart(pid, slug); ok {
		_, err := s.acceptPart(pid, pt, facts)
		return slug, false, err
	}
	if slug == CriticalSlug {
		return "", false, ErrCriticalOwner
	}
	made := false
	_, wrote, err := s.Memory.UpdateSub(pid, slug, func(cur SubMemory, ok bool) (string, string, string, bool) {
		next := AcceptFacts(cur.Content, facts)
		if ok {
			return "", "", next, next != cur.Content
		}
		made = true
		t := strings.TrimSpace(title)
		if t == "" {
			t = strings.TrimSpace(name)
		}
		d := strings.TrimSpace(desc)
		if d == "" {
			d = "What agents learned about " + t + "."
		}
		return clip(t, maxSubTitle), clip(d, maxSubDesc), next, true
	})
	if err != nil {
		return "", false, err
	}
	if wrote {
		s.publishMemory(pid, slug)
	}
	return slug, made, nil
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// MoveResult says what memory_move did.
type MoveResult struct {
	From, To string
	Moved    []string
	Missing  []string
	Made     bool // the page moved to was made for them
}

// MoveMemoryFacts takes facts out of one memory of a project (from: "" or "main" for the main
// memory, a page, or a part's page) and adds them to another (to, made when it is a page that does
// not exist yet), each under the section it had. A fact is matched by its text as memory_read
// shows it, or by a part of it of at least 12 characters that only one line has. The target is
// written first: a failure never loses a fact, at worst it is in both.
func (s *Service) MoveMemoryFacts(pid, from, to, toTitle, toDesc string, facts []string) (MoveResult, error) {
	src, srcExists, err := s.pageFor(pid, from)
	if err != nil {
		return MoveResult{}, err
	}
	if !srcExists {
		return MoveResult{}, fmt.Errorf("%w: %q", ErrSubNotFound, from)
	}
	if src == CriticalSlug {
		return MoveResult{}, ErrCriticalOwner
	}
	content, err := s.docContent(pid, src)
	if err != nil {
		return MoveResult{}, err
	}
	res := MoveResult{From: src}
	picked, kept := pickFacts(content, facts)
	var moving []Fact
	for i, f := range facts {
		if picked[i] == nil {
			res.Missing = append(res.Missing, f)
			continue
		}
		moving = append(moving, *picked[i])
		res.Moved = append(res.Moved, picked[i].Text)
	}
	if len(moving) == 0 {
		return res, ErrFactNotFound
	}
	dst, made, err := s.addToPage(pid, to, toTitle, toDesc, moving)
	if err != nil {
		return res, err
	}
	res.To, res.Made = dst, made
	if dst == src {
		return res, nil
	}
	// what is left of the source, without the lines that moved, as it is now
	if err := s.dropLines(pid, src, kept); err != nil {
		return res, fmt.Errorf("the facts are in %s now, but could not be taken out of %s: %w", dst, src, err)
	}
	return res, nil
}

// docContent is a project memory document's text: the main memory or a page.
func (s *Service) docContent(pid, doc string) (string, error) {
	if doc == MainDoc {
		d, err := s.Memory.Read("project", pid)
		return d.Content, err
	}
	d, err := s.Memory.Sub(pid, doc)
	return d.Content, err
}

// pickFacts finds each wanted fact's bullet in content: picked[i] is the fact with the kind of
// the section it was under, nil when none or several lines match. keep says, of the lines the
// facts were found in, which bullet texts go (lowercase).
func pickFacts(content string, wanted []string) ([]*Fact, map[string]bool) {
	type line struct{ text, section string }
	var lines []line
	section := ""
	for _, l := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		if h, ok := strings.CutPrefix(t, "## "); ok {
			section = strings.TrimSpace(h)
			continue
		}
		if b := bulletText(l); b != "" {
			lines = append(lines, line{b, section})
		}
	}
	picked := make([]*Fact, len(wanted))
	gone := map[string]bool{}
	for i, w := range wanted {
		want := strings.ToLower(cleanFact(w))
		if want == "" {
			continue
		}
		var hit *line
		for j := range lines {
			if strings.ToLower(lines[j].text) == want {
				hit = &lines[j]
				break
			}
		}
		if hit == nil && len([]rune(want)) >= 12 {
			n := 0
			for j := range lines {
				if strings.Contains(strings.ToLower(lines[j].text), want) {
					hit, n = &lines[j], n+1
				}
			}
			if n != 1 {
				hit = nil
			}
		}
		if hit == nil || gone[strings.ToLower(hit.text)] {
			continue
		}
		gone[strings.ToLower(hit.text)] = true
		picked[i] = &Fact{Kind: strings.ToLower(hit.section), Text: hit.text}
	}
	return picked, gone
}

// dropLines takes the bullets whose text gone holds out of a project memory document, as it is at
// that moment; a section left empty goes with them.
func (s *Service) dropLines(pid, doc string, gone map[string]bool) error {
	strip := func(content string) string {
		var out []string
		for _, l := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
			if b := bulletText(l); b != "" && gone[strings.ToLower(b)] {
				continue
			}
			out = append(out, l)
		}
		return dropEmptySections(strings.Join(out, "\n"))
	}
	if doc == MainDoc {
		for try := 0; ; try++ {
			cur, err := s.Memory.Read("project", pid)
			if err != nil {
				return err
			}
			if _, err = s.WriteMemory(pid, "project", strip(cur.Content), cur.Mtime); !errors.Is(err, ErrMtimeMismatch) || try == 2 {
				return err
			}
		}
	}
	_, wrote, err := s.Memory.UpdateSub(pid, doc, func(cur SubMemory, ok bool) (string, string, string, bool) {
		next := strip(cur.Content)
		return "", "", next, ok && next != cur.Content
	})
	if err == nil && wrote {
		s.publishMemory(pid, doc)
	}
	return err
}

// dropEmptySections removes "## Heading" lines with no line of text under them.
func dropEmptySections(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "## ") {
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j == len(lines) || strings.HasPrefix(strings.TrimSpace(lines[j]), "## ") {
				i = j - 1
				continue
			}
		}
		out = append(out, lines[i])
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}
