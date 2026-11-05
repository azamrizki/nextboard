package studio

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Markdown studio (docs/API-studio.md §8, E5): the vault index of a
// project (.md/.markdown/.mdx with the §7 prune rules plus hidden folders,
// ≤ 2000 files), links extracted on the hub ([[wiki|alias#heading]] by
// case-insensitive basename, [text](rel.md) relative to the file), the
// link graph and backlinks, and Claude's agent memory of the project.
// Files are read only up to 256 KB each, 20 MB / 20 s per index, cached
// per (size, mtime).

var (
	MDMaxFiles   = 2000
	MDMaxFile    = int64(256 << 10)
	MDBudget     = int64(20 << 20)
	MDTimeBudget = 20 * time.Second
)

var errNotExist = fs.ErrNotExist

// MDFile is one vault file.
type MDFile struct {
	Path  string    `json:"path"`
	Rel   string    `json:"rel"`
	Size  int64     `json:"size"`
	Mtime time.Time `json:"mtime"`
	Title string    `json:"title"`
}

// MDLink is one extracted link.
type MDLink struct {
	From     string `json:"from"`
	To       string `json:"to"` // rel of the target when resolved, else the raw target
	Kind     string `json:"kind"`
	Resolved bool   `json:"resolved"`
	Heading  string `json:"heading,omitempty"`
}

// AgentMemory is Claude's memory folder of the project.
type AgentMemory struct {
	Dir   string   `json:"dir"`
	Files []MDFile `json:"files"`
}

// Vault is GET /api/projects/{id}/markdown.
type Vault struct {
	Files       []MDFile     `json:"files"`
	Links       []MDLink     `json:"links"`
	AgentMemory *AgentMemory `json:"agentMemory"`
	Truncated   bool         `json:"truncated"`
	ScannedAt   time.Time    `json:"scannedAt"`
}

type rawLink struct {
	kind, target, heading string
}

type mdEntry struct {
	size  int64
	mtime time.Time
	title string
	links []rawLink
}

// Vaults caches per-file parse results per (machine, root).
type Vaults struct {
	mu    sync.Mutex
	cache map[string]map[string]*mdEntry
}

func NewVaults() *Vaults { return &Vaults{cache: map[string]map[string]*mdEntry{}} }

// IsMarkdown reports whether name is a vault file.
func IsMarkdown(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".mdx":
		return true
	}
	return false
}

var (
	fenceRe   = regexp.MustCompile("(?ms)^\\s*(```|~~~).*?^\\s*(```|~~~)\\s*$")
	inlineRe  = regexp.MustCompile("`[^`\n]*`")
	wikiRe    = regexp.MustCompile(`\[\[([^\]\|#\n]+)(#[^\]\|\n]*)?(\|[^\]\n]*)?\]\]`)
	mdLinkRe  = regexp.MustCompile(`\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	h1Re      = regexp.MustCompile(`(?m)^#\s+(.+?)\s*#*\s*$`)
	fmTitleRe = regexp.MustCompile(`(?m)^title:\s*["']?(.+?)["']?\s*$`)
	schemeRe  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
)

// ParseMarkdown extracts the title and raw links of a document.
func ParseMarkdown(name, text string) (string, []rawLink) {
	title := ""
	body := text
	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			if m := fmTitleRe.FindStringSubmatch(text[4 : 4+end]); m != nil {
				title = strings.TrimSpace(m[1])
			}
			body = text[4+end+4:]
		}
	}
	body = fenceRe.ReplaceAllString(body, "")
	body = inlineRe.ReplaceAllString(body, "")
	if title == "" {
		if m := h1Re.FindStringSubmatch(body); m != nil {
			title = strings.TrimSpace(m[1])
		}
	}
	if title == "" {
		title = strings.TrimSuffix(name, path.Ext(name))
	}
	var links []rawLink
	for _, m := range wikiRe.FindAllStringSubmatch(body, -1) {
		links = append(links, rawLink{kind: "wiki", target: strings.TrimSpace(m[1]), heading: strings.TrimPrefix(m[2], "#")})
	}
	for _, m := range mdLinkRe.FindAllStringSubmatch(body, -1) {
		t := m[1]
		if schemeRe.MatchString(t) || strings.HasPrefix(t, "#") {
			continue
		}
		heading := ""
		if i := strings.IndexByte(t, '#'); i >= 0 {
			t, heading = t[:i], t[i+1:]
		}
		if i := strings.IndexByte(t, '?'); i >= 0 {
			t = t[:i]
		}
		if u, err := url.PathUnescape(t); err == nil {
			t = u
		}
		if !IsMarkdown(t) {
			continue
		}
		links = append(links, rawLink{kind: "md", target: t, heading: heading})
	}
	return title, links
}

// walkMarkdown lists vault files below root.
func walkMarkdown(ctx context.Context, h machine.Host, root string) ([]MDFile, string, bool, error) {
	first, err := h.ListDir(ctx, root)
	if err != nil {
		return nil, "", false, err
	}
	base := first.Path
	var out []MDFile
	queue := []*machine.DirListing{first}
	for len(queue) > 0 {
		l := queue[0]
		queue = queue[1:]
		for _, e := range l.Entries {
			if ctx.Err() != nil {
				return out, base, true, nil
			}
			if strings.HasPrefix(e.Name, ".") || (e.Dir && PruneDirs[e.Name]) {
				continue
			}
			full := path.Join(l.Path, e.Name)
			if e.Dir {
				if sub, err := h.ListDir(ctx, full); err == nil {
					queue = append(queue, sub)
				}
				continue
			}
			if !IsMarkdown(e.Name) {
				continue
			}
			if len(out) >= MDMaxFiles {
				return out, base, true, nil
			}
			out = append(out, MDFile{Path: full, Rel: strings.TrimPrefix(full, base+"/"), Size: e.Size, Mtime: e.Mtime})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, base, false, nil
}

// Index builds the vault of root on h; memDir ("" = none) is Claude's
// memory folder of the project. refresh drops the cache first.
func (v *Vaults) Index(ctx context.Context, h machine.Host, root, memDir string, refresh bool) (*Vault, error) {
	ctx, cancel := context.WithTimeout(ctx, MDTimeBudget)
	defer cancel()
	files, base, truncated, err := walkMarkdown(ctx, h, root)
	if err != nil {
		return nil, err
	}
	key := h.ID() + "|" + base
	v.mu.Lock()
	if refresh || v.cache[key] == nil {
		v.cache[key] = map[string]*mdEntry{}
	}
	cache := v.cache[key]
	v.mu.Unlock()
	var budget int64
	entries := map[string]*mdEntry{}
	for i := range files {
		f := &files[i]
		v.mu.Lock()
		e := cache[f.Path]
		v.mu.Unlock()
		if e == nil || e.size != f.Size || !e.mtime.Equal(f.Mtime) {
			e = &mdEntry{size: f.Size, mtime: f.Mtime, title: strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path))}
			if f.Size <= MDMaxFile && budget+f.Size <= MDBudget && ctx.Err() == nil {
				fc, err := h.ReadFile(ctx, f.Path, MDMaxFile)
				if err == nil && !fc.Binary {
					budget += int64(len(fc.Content))
					e.title, e.links = ParseMarkdown(path.Base(f.Path), fc.Content)
				}
			} else if f.Size <= MDMaxFile {
				truncated = true
				e = nil // not cached: read next time
			}
			if e != nil {
				v.mu.Lock()
				cache[f.Path] = e
				v.mu.Unlock()
			}
		}
		if e != nil {
			f.Title = e.title
			entries[f.Rel] = e
		} else {
			f.Title = strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path))
		}
	}
	vault := &Vault{Files: files, Links: resolveLinks(files, entries), Truncated: truncated, ScannedAt: time.Now().UTC()}
	if vault.Files == nil {
		vault.Files = []MDFile{}
	}
	if memDir != "" {
		vault.AgentMemory = memory(ctx, h, memDir)
	}
	return vault, nil
}

// resolveLinks turns raw links into vault links: wiki targets match a
// file's basename (or rel path) without extension, case-insensitive;
// markdown links resolve relative to the linking file.
func resolveLinks(files []MDFile, entries map[string]*mdEntry) []MDLink {
	byName := map[string]string{}
	byRel := map[string]string{}
	for _, f := range files {
		n := strings.ToLower(strings.TrimSuffix(path.Base(f.Rel), path.Ext(f.Rel)))
		if _, dup := byName[n]; !dup {
			byName[n] = f.Rel // first by path order wins
		}
		byRel[strings.ToLower(f.Rel)] = f.Rel
		byRel[strings.ToLower(strings.TrimSuffix(f.Rel, path.Ext(f.Rel)))] = f.Rel
	}
	out := []MDLink{}
	for _, f := range files {
		e := entries[f.Rel]
		if e == nil {
			continue
		}
		for _, l := range e.links {
			ml := MDLink{From: f.Rel, To: l.target, Kind: l.kind, Heading: l.heading}
			switch l.kind {
			case "wiki":
				t := strings.ToLower(strings.TrimSpace(l.target))
				t = strings.TrimSuffix(t, path.Ext(t))
				if r, ok := byRel[t]; ok && strings.Contains(t, "/") {
					ml.To, ml.Resolved = r, true
				} else if r, ok := byName[path.Base(t)]; ok {
					ml.To, ml.Resolved = r, true
				}
			case "md":
				t := l.target
				var rel string
				if strings.HasPrefix(t, "/") {
					rel = path.Clean(strings.TrimPrefix(t, "/"))
				} else {
					rel = path.Clean(path.Join(path.Dir(f.Rel), t))
				}
				if r, ok := byRel[strings.ToLower(rel)]; ok {
					ml.To, ml.Resolved = r, true
				} else {
					ml.To = rel
				}
			}
			out = append(out, ml)
		}
	}
	return out
}

func memory(ctx context.Context, h machine.Host, dir string) *AgentMemory {
	am := &AgentMemory{Dir: dir, Files: []MDFile{}}
	st, err := h.Stat(ctx, dir)
	if err != nil || !st.Exists || !st.Dir {
		if st != nil && st.Path != "" {
			am.Dir = st.Path
		}
		return am
	}
	l, err := h.ListDir(ctx, dir)
	if err != nil {
		return am
	}
	am.Dir = l.Path
	for _, e := range l.Entries {
		if e.Dir || !IsMarkdown(e.Name) {
			continue
		}
		f := MDFile{Path: path.Join(l.Path, e.Name), Rel: e.Name, Size: e.Size, Mtime: e.Mtime,
			Title: strings.TrimSuffix(e.Name, path.Ext(e.Name))}
		if e.Size <= MDMaxFile {
			if fc, err := h.ReadFile(ctx, f.Path, MDMaxFile); err == nil && !fc.Binary {
				f.Title, _ = ParseMarkdown(e.Name, fc.Content)
			}
		}
		am.Files = append(am.Files, f)
	}
	// MEMORY.md (the index Claude loads) first.
	sort.SliceStable(am.Files, func(i, j int) bool {
		return am.Files[i].Rel == "MEMORY.md" && am.Files[j].Rel != "MEMORY.md"
	})
	return am
}

// GraphNode / GraphEdge / Graph are GET …/markdown/graph.
type GraphNode struct {
	ID     string `json:"id"` // rel path
	Title  string `json:"title"`
	In     int    `json:"in"`
	Out    int    `json:"out"`
	Orphan bool   `json:"orphan"`
}

type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

type Graph struct {
	Nodes      []GraphNode `json:"nodes"`
	Edges      []GraphEdge `json:"edges"`
	Unresolved int         `json:"unresolved"`
	Truncated  bool        `json:"truncated"`
}

// BuildGraph turns resolved links into a deduplicated graph; every vault
// file is a node, orphans included.
func BuildGraph(v *Vault) Graph {
	g := Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Truncated: v.Truncated}
	idx := map[string]int{}
	for _, f := range v.Files {
		idx[f.Rel] = len(g.Nodes)
		g.Nodes = append(g.Nodes, GraphNode{ID: f.Rel, Title: f.Title})
	}
	ek := map[string]int{}
	for _, l := range v.Links {
		if !l.Resolved {
			g.Unresolved++
			continue
		}
		if l.From == l.To {
			continue
		}
		k := l.From + "\x00" + l.To + "\x00" + l.Kind
		if i, ok := ek[k]; ok {
			g.Edges[i].Count++
			continue
		}
		ek[k] = len(g.Edges)
		g.Edges = append(g.Edges, GraphEdge{From: l.From, To: l.To, Kind: l.Kind, Count: 1})
		g.Nodes[idx[l.From]].Out++
		g.Nodes[idx[l.To]].In++
	}
	for i := range g.Nodes {
		g.Nodes[i].Orphan = g.Nodes[i].In == 0 && g.Nodes[i].Out == 0
	}
	return g
}

// Backlink is one incoming link of a file.
type Backlink struct {
	From    string `json:"from"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Heading string `json:"heading,omitempty"`
}

// Backlinks returns the incoming and outgoing links of rel.
func Backlinks(v *Vault, rel string) (in []Backlink, out []MDLink, err error) {
	titles := map[string]string{}
	found := false
	for _, f := range v.Files {
		titles[f.Rel] = f.Title
		if f.Rel == rel {
			found = true
		}
	}
	if !found {
		return nil, nil, errors.New("file tidak ada di vault")
	}
	in, out = []Backlink{}, []MDLink{}
	for _, l := range v.Links {
		if l.Resolved && l.To == rel && l.From != rel {
			in = append(in, Backlink{From: l.From, Title: titles[l.From], Kind: l.Kind, Heading: l.Heading})
		}
		if l.From == rel {
			out = append(out, l)
		}
	}
	return in, out, nil
}
