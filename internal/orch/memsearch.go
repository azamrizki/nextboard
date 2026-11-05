package orch

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Looking up memory by topic, a small Context7 for one project: agents do not
// carry all of it, they ask for what the work at hand needs and get the matching
// passages. BM25 over headed passages, in process, no index to keep up to date;
// a project's memory is small enough to read each time.

const (
	memChunkChars   = 1200
	memSearchChars  = 4000
	memSearchLimit  = 6
	memHeadingBoost = 3
	memPrefixWeight = 0.6
	bm25K1          = 1.2
	bm25B           = 0.75
)

// MemoryHit is one passage that matched.
type MemoryHit struct {
	ProjectID string  `json:"projectId,omitempty"`
	Project   string  `json:"project,omitempty"`
	Doc       string  `json:"doc"` // the name to read it by
	Title     string  `json:"title"`
	Section   string  `json:"section,omitempty"`
	Text      string  `json:"text"`
	Critical  bool    `json:"critical,omitempty"`
	Global    bool    `json:"global,omitempty"`
	Score     float64 `json:"score"`
}

type memDoc struct {
	projectID, project, name, title, desc, content string
	critical, global                               bool
}

// memDocs is everything an agent of the project can look up. pid "" is only
// the memory of all projects.
func (s *Service) memDocs(pid, projectName string) []memDoc {
	var out []memDoc
	add := func(owner, label string, global bool) {
		scope := "project"
		if global {
			scope = "global"
		}
		if d, err := s.Memory.Read(scope, owner); err == nil && strings.TrimSpace(d.Content) != "" {
			out = append(out, memDoc{projectID: owner, project: label, name: MainDoc, title: mainTitle(global), content: d.Content, global: global})
		}
		for _, sub := range s.Memory.Subs(owner) {
			if full, err := s.Memory.Sub(owner, sub.Slug); err == nil && strings.TrimSpace(full.Content) != "" {
				out = append(out, memDoc{projectID: owner, project: label, name: sub.Slug, title: full.Title, desc: full.Description, content: full.Content, critical: full.Critical, global: global})
			}
		}
		if !global {
			for _, n := range s.Memory.Notes(owner) {
				out = append(out, memDoc{projectID: owner, project: label, name: NotePrefix + n.Name, title: strings.TrimSuffix(n.Name, ".md"), content: n.Content})
			}
		}
	}
	if pid != "" {
		add(pid, projectName, false)
	}
	add("", "", true)
	return out
}

func mainTitle(global bool) string {
	if global {
		return "All projects"
	}
	return "Main memory"
}

type memChunk struct {
	doc     *memDoc
	section string
	text    string
	tf      map[string]float64
	length  float64
}

// memTokens splits text into lowercase words, and identifiers into their parts:
// "userAuth_token" gives user, auth, token.
func memTokens(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 1 || (len(cur) == 1 && unicode.IsDigit(cur[0])) {
			if w := string(cur); !memStop[w] {
				out = append(out, w)
			}
		}
		cur = cur[:0]
	}
	prevLower := false
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			prevLower = false
			continue
		}
		if unicode.IsUpper(r) && prevLower {
			flush()
		}
		prevLower = unicode.IsLower(r)
		cur = append(cur, unicode.ToLower(r))
	}
	flush()
	return out
}

var memStop = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields("the an and or of to in on for with is are be this that it as at by from yang dan di ke dari untuk dengan ini itu atau pada adalah") {
		m[w] = true
	}
	return m
}()

// memSplit cuts a document at its headings, and long parts at blank lines.
func memSplit(content string) []struct{ section, text string } {
	var out []struct{ section, text string }
	section := ""
	var cur []string
	flush := func() {
		text := strings.TrimSpace(strings.Join(cur, "\n"))
		cur = cur[:0]
		if text == "" {
			return
		}
		for _, part := range memBreak(text) {
			out = append(out, struct{ section, text string }{section, part})
		}
	}
	for _, l := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "#") && strings.Contains(t, " ") {
			flush()
			section = strings.TrimSpace(strings.TrimLeft(t, "#"))
			continue
		}
		cur = append(cur, l)
	}
	flush()
	return out
}

func memBreak(text string) []string {
	if len([]rune(text)) <= memChunkChars {
		return []string{text}
	}
	var out []string
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if b.Len() > 0 && len([]rune(b.String()))+len([]rune(line)) > memChunkChars {
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
		}
		b.WriteString(line + "\n")
	}
	if strings.TrimSpace(b.String()) != "" {
		out = append(out, strings.TrimSpace(b.String()))
	}
	return out
}

func memChunks(docs []memDoc) []memChunk {
	var out []memChunk
	for i := range docs {
		d := &docs[i]
		for _, p := range memSplit(d.content) {
			c := memChunk{doc: d, section: p.section, text: p.text, tf: map[string]float64{}}
			for _, w := range memTokens(p.text) {
				c.tf[w]++
				c.length++
			}
			for _, w := range memTokens(d.title + " " + d.desc + " " + p.section) {
				c.tf[w] += memHeadingBoost
				c.length += memHeadingBoost
			}
			out = append(out, c)
		}
	}
	return out
}

// memRank scores the chunks against the query, best first; those that match
// nothing are left out.
func memRank(chunks []memChunk, query string) []scoredChunk {
	terms := memTokens(query)
	if len(terms) == 0 || len(chunks) == 0 {
		return nil
	}
	var total float64
	for i := range chunks {
		total += chunks[i].length
	}
	avg := total / float64(len(chunks))
	n := float64(len(chunks))
	match := func(c *memChunk, term string) float64 {
		f := c.tf[term]
		if len(term) >= 3 {
			for w, count := range c.tf {
				if w != term && strings.HasPrefix(w, term) {
					f += memPrefixWeight * count
				}
			}
		}
		return f
	}
	var out []scoredChunk
	for i := range chunks {
		c := &chunks[i]
		score, matched := 0.0, 0.0
		for _, term := range terms {
			df := 0.0
			for j := range chunks {
				if match(&chunks[j], term) > 0 {
					df++
				}
			}
			f := match(c, term)
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			score += idf * f * (bm25K1 + 1) / (f + bm25K1*(1-bm25B+bm25B*c.length/avg))
			matched++
		}
		if score > 0 {
			// A passage with all the words beats one with a lot of one word.
			out = append(out, scoredChunk{c, score * matched / float64(len(terms))})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

type scoredChunk struct {
	c     *memChunk
	score float64
}

// SearchMemory finds passages about a topic in what a project's agents can read,
// within limit passages and maxChars characters.
func (s *Service) SearchMemory(pid, query string, limit, maxChars int) []MemoryHit {
	name := ""
	if p, ok := s.hub.Project(pid); ok {
		name = p.Name
	}
	return memHits(memChunks(s.memDocs(pid, name)), query, limit, maxChars)
}

// SearchAllMemory searches every project, for the owner.
func (s *Service) SearchAllMemory(query string, limit, maxChars int) []MemoryHit {
	docs := s.memDocs("", "")
	for _, p := range s.hub.Projects() {
		for _, d := range s.memDocs(p.ID, p.Name) {
			if !d.global {
				docs = append(docs, d)
			}
		}
	}
	return memHits(memChunks(docs), query, limit, maxChars)
}

func memHits(chunks []memChunk, query string, limit, maxChars int) []MemoryHit {
	if limit <= 0 {
		limit = memSearchLimit
	}
	if maxChars <= 0 {
		maxChars = memSearchChars
	}
	out := []MemoryHit{}
	used := 0
	for _, sc := range memRank(chunks, query) {
		size := len([]rune(sc.c.text))
		if len(out) > 0 && used+size > maxChars {
			continue
		}
		d := sc.c.doc
		out = append(out, MemoryHit{ProjectID: d.projectID, Project: d.project, Doc: d.name, Title: d.title, Section: sc.c.section, Text: sc.c.text,
			Critical: d.critical, Global: d.global, Score: math.Round(sc.score*100) / 100})
		used += size
		if len(out) == limit {
			break
		}
	}
	return out
}

func docLabel(h MemoryHit) string {
	name := h.Doc
	if h.Global {
		name = "global/" + name
	}
	if h.Section != "" {
		name += " › " + h.Section
	}
	return name
}

// MemorySearchText answers memory_search.
func (s *Service) MemorySearchText(pid, query string, limit int) string {
	if strings.TrimSpace(query) == "" {
		return "Say what to look for: query is empty."
	}
	hits := s.SearchMemory(pid, query, limit, 0)
	if len(hits) == 0 {
		return fmt.Sprintf("Nothing in the project memory about %q.\n\nWhat there is:\n%s", query, s.memoryIndex(pid))
	}
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "[%s]\n%s", docLabel(h), h.Text)
	}
	return b.String()
}

// MemoryReadText answers memory_read: the overview, one document, or the part of
// a document about a topic.
func (s *Service) MemoryReadText(pid, name, topic string) (string, error) {
	if name == "" && topic == "" {
		return s.MemoryTextFor(pid), nil
	}
	if name == "" {
		return s.MemorySearchText(pid, topic, 0), nil
	}
	owner, doc := splitDocName(pid, name)
	d, err := s.ReadDoc(owner, doc)
	if err != nil {
		return "", fmt.Errorf("no memory named %q. What there is:\n%s", name, s.memoryIndex(pid))
	}
	if topic == "" {
		if strings.TrimSpace(d.Content) == "" {
			return d.Title + " is empty.", nil
		}
		return d.Content, nil
	}
	hits := memHits(memChunks([]memDoc{{name: d.Name, title: d.Title, desc: d.Description, content: d.Content, global: owner == ""}}), topic, 0, 0)
	if len(hits) == 0 {
		return fmt.Sprintf("Nothing about %q in %s. It has these parts: %s", topic, d.Title, strings.Join(docSections(d.Content), ", ")), nil
	}
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if h.Section != "" {
			b.WriteString("## " + h.Section + "\n")
		}
		b.WriteString(h.Text)
	}
	return b.String(), nil
}

func docSections(content string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range memSplit(content) {
		if p.section != "" && !seen[p.section] {
			seen[p.section] = true
			out = append(out, p.section)
		}
	}
	return out
}
