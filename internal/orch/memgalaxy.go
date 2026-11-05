package orch

import (
	"regexp"
	"sort"
	"strings"
)

// What the memory page draws: every main memory with its documents, their
// headings and the [[links]] between them. No content: it is read on opening.

// GalaxySection is a heading of a document.
type GalaxySection struct {
	Title string `json:"title"`
	Chars int    `json:"chars"`
}

// GalaxyDoc is one document of a main memory.
type GalaxyDoc struct {
	Name        string          `json:"name"`
	Kind        string          `json:"kind"` // main | sub | note
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Critical    bool            `json:"critical,omitempty"`
	Chars       int             `json:"chars"`
	Facts       int             `json:"facts"`
	Mtime       string          `json:"mtime,omitempty"`
	Sections    []GalaxySection `json:"sections"`
	Links       []string        `json:"links"`
}

// GalaxyOwner is a main memory with what belongs to it.
type GalaxyOwner struct {
	ProjectID string      `json:"projectId,omitempty"`
	Name      string      `json:"name"`
	Docs      []GalaxyDoc `json:"docs"`
}

// GalaxyView is GET /api/memory/galaxy.
type GalaxyView struct {
	Global   GalaxyOwner   `json:"global"`
	Projects []GalaxyOwner `json:"projects"`
}

var wikiLink = regexp.MustCompile(`\[\[([^\]\n]+)\]\]`)

func docStats(content string) (sections []GalaxySection, facts int) {
	sections = []GalaxySection{}
	for _, p := range memSplit(content) {
		for _, l := range strings.Split(p.text, "\n") {
			if bulletText(l) != "" {
				facts++
			}
		}
		if p.section == "" {
			continue
		}
		if n := len(sections); n > 0 && sections[n-1].Title == p.section {
			sections[n-1].Chars += len([]rune(p.text))
			continue
		}
		sections = append(sections, GalaxySection{Title: p.section, Chars: len([]rune(p.text))})
	}
	return sections, facts
}

// linkTargets gives the [[links]] of a text. A link may carry an alias or a
// heading: [[name|shown]], [[name#part]].
func linkTargets(content string) []string {
	out := []string{}
	for _, m := range wikiLink.FindAllStringSubmatch(content, -1) {
		t := m[1]
		if i := strings.IndexAny(t, "|#"); i >= 0 {
			t = t[:i]
		}
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (s *Service) galaxyOwner(pid, name string) GalaxyOwner {
	o := GalaxyOwner{ProjectID: pid, Name: name, Docs: []GalaxyDoc{}}
	links := map[string][]string{}
	add := func(d GalaxyDoc, content string) {
		d.Sections, d.Facts = docStats(content)
		d.Chars = len([]rune(content))
		d.Links = []string{}
		links[d.Name] = linkTargets(content)
		o.Docs = append(o.Docs, d)
	}
	if main, err := s.ReadDoc(pid, MainDoc); err == nil {
		add(GalaxyDoc{Name: MainDoc, Kind: "main", Title: main.Title, Mtime: main.Mtime}, main.Content)
	}
	for _, sub := range s.Memory.Subs(pid) {
		if full, err := s.Memory.Sub(pid, sub.Slug); err == nil {
			add(GalaxyDoc{Name: full.Slug, Kind: "sub", Title: full.Title, Description: full.Description, Critical: full.Critical, Mtime: full.Mtime}, full.Content)
		}
	}
	if pid != "" {
		for _, n := range s.Memory.Notes(pid) {
			add(GalaxyDoc{Name: NotePrefix + n.Name, Kind: "note", Title: strings.TrimSuffix(n.Name, ".md")}, n.Content)
		}
	}
	byName := map[string]string{}
	for _, d := range o.Docs {
		byName[strings.ToLower(d.Name)] = d.Name
		byName[strings.ToLower(d.Title)] = d.Name
		if t, ok := strings.CutPrefix(d.Name, NotePrefix); ok {
			byName[strings.ToLower(strings.TrimSuffix(t, ".md"))] = d.Name
		}
	}
	for i := range o.Docs {
		d := &o.Docs[i]
		seen := map[string]bool{d.Name: true}
		for _, t := range links[d.Name] {
			if to, ok := byName[strings.ToLower(t)]; ok && !seen[to] {
				seen[to] = true
				d.Links = append(d.Links, to)
			}
		}
	}
	return o
}

// Galaxy is every main memory on this hub.
func (s *Service) Galaxy() GalaxyView {
	v := GalaxyView{Global: s.galaxyOwner("", "All projects"), Projects: []GalaxyOwner{}}
	// Oldest first: a project keeps its place and colour when another is opened
	// or added.
	projects := s.hub.Projects()
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].CreatedAt.Before(projects[j].CreatedAt) })
	for _, p := range projects {
		v.Projects = append(v.Projects, s.galaxyOwner(p.ID, p.Name))
	}
	return v
}
