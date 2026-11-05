package studio

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Editor extensions from Open VSX (docs/API-studio.md §7.6). Monaco has no
// extension host, so an extension's code never runs and is never kept on disk:
// only its declarative parts are, namely colour and icon themes, snippets,
// grammars and language definitions, all of them data files. For the extensions
// in extNative the editor does the main work itself.

const (
	openVSXBase       = "https://open-vsx.org/api"
	extMaxDownload    = 100 << 20
	extMaxUnpacked    = 64 << 20
	extMaxFile        = 16 << 20
	extRequestTimeout = 90 * time.Second
	// A search looks further than it shows, as many hits will not qualify.
	extSearchAsk     = 50
	extCheckWorkers  = 8
	extSearchTimeout = 25 * time.Second
	extManifestMax   = 4 << 20
	extVerdictsMax   = 5000
)

var (
	ErrExtensionNotFound = errors.New("extension tidak ditemukan")
	// ErrExtensionCodeOnly: nothing in it can work without running its code.
	ErrExtensionCodeOnly = errors.New("extension ini hanya berisi kode; editor hanya bisa memakai tema, snippet, dan grammar")
	ErrExtensionBadID    = errors.New("nama extension tidak valid")
)

var extNamePart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,120}$`)

// extDataFile lists what is unpacked: everything else in the package, code
// above all, stays out.
var extDataFile = map[string]bool{".json": true, ".jsonc": true, ".code-snippets": true, ".tmlanguage": true, ".tmtheme": true, ".plist": true, ".tmsnippet": true, ".svg": true}

// extNative names the extensions whose main work the editor does itself, by
// lower-case id: "lsp:" and a catalog id means a language server does it. Their
// code stays out as well, so these install even with nothing else in them.
var extNative = map[string]string{
	"esbenp.prettier-vscode":    "prettier",
	"dbaeumer.vscode-eslint":    "eslint",
	"eamodio.gitlens":           "gitlens",
	"usernamehw.errorlens":      "errorlens",
	"vscodevim.vim":             "vim",
	"ms-python.python":          "lsp:python",
	"golang.go":                 "lsp:go",
	"rust-lang.rust-analyzer":   "lsp:rust",
	"bradlc.vscode-tailwindcss": "lsp:tailwind",
	"vue.volar":                 "lsp:vue",
	"tamasfe.even-better-toml":  "lsp:toml",
}

// ExtTheme is a colour theme of an extension.
type ExtTheme struct {
	Label   string `json:"label"`
	UITheme string `json:"uiTheme"`
	Path    string `json:"path"`
}

// ExtIconTheme is a file icon theme of an extension.
type ExtIconTheme struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// ExtSnippets is a snippet file of an extension.
type ExtSnippets struct {
	Language string `json:"language"`
	Path     string `json:"path"`
}

// ExtGrammar is a TextMate grammar of an extension.
type ExtGrammar struct {
	Language  string `json:"language"`
	ScopeName string `json:"scopeName"`
	Path      string `json:"path"`
}

// ExtLanguage declares a language of an extension.
type ExtLanguage struct {
	ID            string   `json:"id"`
	Aliases       []string `json:"aliases"`
	Extensions    []string `json:"extensions"`
	Filenames     []string `json:"filenames"`
	Configuration string   `json:"configuration"`
}

// ExtContributes is the part of a manifest the editor can use.
type ExtContributes struct {
	Themes     []ExtTheme     `json:"themes"`
	IconThemes []ExtIconTheme `json:"iconThemes"`
	Snippets   []ExtSnippets  `json:"snippets"`
	Grammars   []ExtGrammar   `json:"grammars"`
	Languages  []ExtLanguage  `json:"languages"`
}

// Extension is one installed extension.
type Extension struct {
	ID          string `json:"id"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	// HasCode: the package also has code, which does not run here.
	HasCode bool `json:"hasCode"`
	// Native is the work the editor does for it, see extNative.
	Native      string         `json:"native,omitempty"`
	Contributes ExtContributes `json:"contributes"`
	InstalledAt time.Time      `json:"installedAt"`
}

// ExtSearchItem is one search hit.
type ExtSearchItem struct {
	Namespace     string  `json:"namespace"`
	Name          string  `json:"name"`
	Version       string  `json:"version"`
	DisplayName   string  `json:"displayName"`
	Description   string  `json:"description"`
	DownloadCount int64   `json:"downloadCount"`
	AverageRating float64 `json:"averageRating"`
	// What it would do here, told before it is installed.
	HasCode bool     `json:"hasCode,omitempty"`
	Native  string   `json:"native,omitempty"`
	Gives   []string `json:"gives,omitempty"`
}

// ExtSearch is GET /api/extensions/search: only what can be installed here.
type ExtSearch struct {
	Items []ExtSearchItem `json:"items"`
	// Hidden counts the hits left out because they cannot work here, Unchecked
	// those left out because their manifest did not arrive.
	Hidden    int `json:"hidden"`
	Unchecked int `json:"unchecked"`
}

// extVerdict is what a package's manifest and size say; a version never changes.
type extVerdict struct {
	ok      bool
	hasCode bool
	native  string
	gives   []string
}

// Extensions stores installed extensions below dir.
type Extensions struct {
	dir    string
	client *http.Client
	base   string
	mu     sync.Mutex

	verdictMu sync.Mutex
	verdicts  map[string]extVerdict
}

// NewExtensions returns the store.
func NewExtensions(dir string) *Extensions {
	return &Extensions{dir: dir, client: &http.Client{Timeout: extRequestTimeout, CheckRedirect: httpsOnly}, base: openVSXBase}
}

// httpsOnly keeps every step of a download on HTTPS.
func httpsOnly(r *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("terlalu banyak redirect")
	}
	if r.URL.Scheme != "https" {
		return errors.New("redirect ke alamat bukan https")
	}
	return nil
}

// List returns what is installed, by name.
func (e *Extensions) List() ([]Extension, error) {
	ents, err := os.ReadDir(e.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Extension{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Extension{}
	for _, d := range ents {
		if !d.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(e.dir, d.Name(), "nextboard-manifest.json"))
		if err != nil {
			continue
		}
		var x Extension
		if json.Unmarshal(b, &x) == nil && x.ID == d.Name() {
			// from the table, so that one installed earlier gets it too
			x.Native = extNative[strings.ToLower(x.ID)]
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (e *Extensions) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return ErrExtensionNotFound
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("open-vsx.org menjawab %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(v)
}

// Search asks Open VSX and keeps the hits that can be installed here. A hit does
// not say what is in it, so each one's manifest is read, and remembered per version.
func (e *Extensions) Search(ctx context.Context, q string, size int) (*ExtSearch, error) {
	if size <= 0 || size > extSearchAsk {
		size = 24
	}
	ctx, cancel := context.WithTimeout(ctx, extSearchTimeout)
	defer cancel()
	var r struct {
		Extensions []struct {
			ExtSearchItem
			Files map[string]string `json:"files"`
		} `json:"extensions"`
	}
	u := e.base + "/-/search?size=" + strconv.Itoa(extSearchAsk) + "&sortBy=downloadCount&sortOrder=desc&query=" + url.QueryEscape(q)
	if err := e.getJSON(ctx, u, &r); err != nil {
		return nil, err
	}
	verdicts := make([]extVerdict, len(r.Extensions))
	errs := make([]error, len(r.Extensions))
	slots := make(chan struct{}, extCheckWorkers)
	var wg sync.WaitGroup
	for i := range r.Extensions {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()
			h := &r.Extensions[i]
			verdicts[i], errs[i] = e.verdict(ctx, h.Namespace, h.Name, h.Version, h.Files["download"])
		}(i)
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil && errors.Is(err, context.Canceled) {
		return nil, err
	}
	out := &ExtSearch{Items: []ExtSearchItem{}}
	var firstErr error
	for i := range r.Extensions {
		switch {
		case errs[i] != nil:
			out.Unchecked++
			if firstErr == nil {
				firstErr = errs[i]
			}
		case !verdicts[i].ok:
			out.Hidden++
		case len(out.Items) < size:
			it, v := r.Extensions[i].ExtSearchItem, verdicts[i]
			it.HasCode, it.Native, it.Gives = v.hasCode, v.native, v.gives
			out.Items = append(out.Items, it)
		}
	}
	// nothing could be checked: that is a failure, not an empty result
	if out.Unchecked > 0 && out.Unchecked == len(r.Extensions) {
		return nil, firstErr
	}
	return out, nil
}

// verdict decides from the manifest and the package's size, without downloading it.
// What stays works as its author meant: a package of data, a theme whose code only
// adds extras, or one the editor stands in for. A language or tool package whose main
// work is in its code is left out, even when a grammar of it would install.
func (e *Extensions) verdict(ctx context.Context, namespace, name, version, download string) (extVerdict, error) {
	id := namespace + "." + name
	key := id + "@" + version
	e.verdictMu.Lock()
	v, ok := e.verdicts[key]
	e.verdictMu.Unlock()
	if ok {
		return v, nil
	}
	v, err := e.readVerdict(ctx, id, download)
	if err != nil {
		return v, err
	}
	e.verdictMu.Lock()
	if e.verdicts == nil || len(e.verdicts) >= extVerdictsMax {
		e.verdicts = map[string]extVerdict{}
	}
	e.verdicts[key] = v
	e.verdictMu.Unlock()
	return v, nil
}

func (e *Extensions) readVerdict(ctx context.Context, id, download string) (extVerdict, error) {
	u, err := url.Parse(download)
	if err != nil || u.Scheme != "https" {
		return extVerdict{}, nil // nothing to download means nothing to install
	}
	// the manifest sits next to the package, for one built per platform as well
	u.RawQuery, u.Fragment = "", ""
	u.Path = path.Join(path.Dir(u.Path), "package.json")
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return extVerdict{}, err
	}
	res, err := e.client.Do(req)
	if err != nil {
		return extVerdict{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return extVerdict{}, fmt.Errorf("manifest %s menjawab %d", id, res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, extManifestMax+1))
	if err != nil {
		return extVerdict{}, err
	}
	if len(b) > extManifestMax {
		return extVerdict{}, nil
	}
	x, err := parseExtManifest(b, nil)
	if err != nil {
		return extVerdict{}, nil
	}
	var m struct {
		Categories []string `json:"categories"`
	}
	_ = json.Unmarshal(b, &m)
	v := extVerdict{hasCode: x.HasCode, native: extNative[strings.ToLower(id)], gives: declaredData(&x.Contributes)}
	v.ok = v.native != "" || (len(v.gives) > 0 && (!x.HasCode || slices.Contains(m.Categories, "Themes")))
	if v.ok && e.tooBig(ctx, download) {
		v.ok = false
	}
	return v, nil
}

// tooBig asks for the package's size. Without an answer the install itself decides.
func (e *Extensions) tooBig(ctx context.Context, download string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, download, nil)
	if err != nil {
		return false
	}
	res, err := e.client.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK && res.ContentLength > extMaxDownload
}

// dataPath says whether Install would keep a file a manifest points at.
func dataPath(p string) bool {
	return p != "" && p != ".." && !strings.HasPrefix(p, "../") && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "node_modules/") && extDataFile[strings.ToLower(path.Ext(p))]
}

// declaredData names what in a manifest the editor can use. A grammar without a
// language only adds to other grammars, which Monaco cannot do.
func declaredData(c *ExtContributes) []string {
	var out []string
	add := func(name string, has bool) {
		if has {
			out = append(out, name)
		}
	}
	add("colour theme", slices.ContainsFunc(c.Themes, func(t ExtTheme) bool { return dataPath(t.Path) }))
	add("icon theme", slices.ContainsFunc(c.IconThemes, func(t ExtIconTheme) bool { return dataPath(t.Path) }))
	add("syntax", slices.ContainsFunc(c.Grammars, func(g ExtGrammar) bool { return g.Language != "" && dataPath(g.Path) }))
	add("snippets", slices.ContainsFunc(c.Snippets, func(s ExtSnippets) bool { return dataPath(s.Path) }))
	return out
}

// Install downloads an extension and keeps its data files.
func (e *Extensions) Install(ctx context.Context, namespace, name string) (*Extension, error) {
	if !extNamePart.MatchString(namespace) || !extNamePart.MatchString(name) {
		return nil, ErrExtensionBadID
	}
	var info struct {
		Version string            `json:"version"`
		Files   map[string]string `json:"files"`
	}
	if err := e.getJSON(ctx, e.base+"/"+url.PathEscape(namespace)+"/"+url.PathEscape(name), &info); err != nil {
		return nil, err
	}
	dl := info.Files["download"]
	if u, err := url.Parse(dl); err != nil || u.Scheme != "https" {
		return nil, errors.New("alamat unduhan extension tidak valid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dl, nil)
	if err != nil {
		return nil, err
	}
	res, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unduhan menjawab %d", res.StatusCode)
	}
	pkg, err := io.ReadAll(io.LimitReader(res.Body, extMaxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(pkg) > extMaxDownload {
		return nil, errors.New("paket extension lebih besar dari 100 MB")
	}
	return e.installPackage(pkg, namespace, name, info.Version)
}

// installPackage unpacks a .vsix: only data files, each one checked to stay
// below the extension's folder. The folder is swapped in at the end, so a failed
// update leaves the old version in place.
func (e *Extensions) installPackage(pkg []byte, namespace, name, version string) (*Extension, error) {
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		return nil, fmt.Errorf("paket extension rusak: %w", err)
	}
	id := namespace + "." + name
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(e.dir, ".install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	var manifest []byte
	nls := map[string]string{}
	var total int64
	for _, f := range zr.File {
		rel, ok := strings.CutPrefix(f.Name, "extension/")
		if !ok || f.FileInfo().IsDir() {
			continue
		}
		clean := path.Clean(rel)
		if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "node_modules/") {
			continue
		}
		if !extDataFile[strings.ToLower(path.Ext(clean))] || f.UncompressedSize64 > extMaxFile {
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > extMaxUnpacked {
			return nil, errors.New("isi extension terlalu besar")
		}
		b, err := readZipFile(f)
		if err != nil {
			return nil, err
		}
		switch clean {
		case "package.json":
			manifest = b
		case "package.nls.json":
			_ = json.Unmarshal(b, &nls)
		}
		dst := filepath.Join(tmp, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return nil, err
		}
	}
	if manifest == nil {
		return nil, errors.New("paket extension tanpa package.json")
	}
	x, err := parseExtManifest(manifest, nls)
	if err != nil {
		return nil, err
	}
	x.ID, x.Namespace, x.Name, x.Version, x.InstalledAt = id, namespace, name, version, time.Now().UTC()
	x.Native = extNative[strings.ToLower(id)]
	keepExisting(tmp, &x.Contributes)
	if len(declaredData(&x.Contributes)) == 0 && x.Native == "" {
		return nil, ErrExtensionCodeOnly
	}
	b, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "nextboard-manifest.json"), b, 0o644); err != nil {
		return nil, err
	}
	dst := filepath.Join(e.dir, id)
	if err := os.RemoveAll(dst); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return nil, err
	}
	return x, nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, extMaxFile+1))
}

// parseExtManifest reads a VS Code package.json, resolving %placeholders%.
func parseExtManifest(b []byte, nls map[string]string) (*Extension, error) {
	var m struct {
		DisplayName string          `json:"displayName"`
		Description string          `json:"description"`
		Main        string          `json:"main"`
		Browser     string          `json:"browser"`
		Contributes json.RawMessage `json:"contributes"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("package.json extension rusak: %w", err)
	}
	loc := func(s string) string {
		if k, ok := strings.CutPrefix(s, "%"); ok && strings.HasSuffix(k, "%") {
			if v, ok := nls[strings.TrimSuffix(k, "%")]; ok {
				return v
			}
		}
		return s
	}
	x := &Extension{DisplayName: loc(m.DisplayName), Description: loc(m.Description), HasCode: m.Main != "" || m.Browser != ""}
	var c struct {
		Themes []struct {
			Label   string `json:"label"`
			ID      string `json:"id"`
			UITheme string `json:"uiTheme"`
			Path    string `json:"path"`
		} `json:"themes"`
		IconThemes []struct {
			Label string `json:"label"`
			ID    string `json:"id"`
			Path  string `json:"path"`
		} `json:"iconThemes"`
		Snippets []struct {
			Language json.RawMessage `json:"language"`
			Path     string          `json:"path"`
		} `json:"snippets"`
		Grammars []struct {
			Language  string `json:"language"`
			ScopeName string `json:"scopeName"`
			Path      string `json:"path"`
		} `json:"grammars"`
		Languages []struct {
			ID            string   `json:"id"`
			Aliases       []string `json:"aliases"`
			Extensions    []string `json:"extensions"`
			Filenames     []string `json:"filenames"`
			Configuration string   `json:"configuration"`
		} `json:"languages"`
	}
	if len(m.Contributes) > 0 {
		// One odd section must not lose the others, so errors are not fatal here.
		_ = json.Unmarshal(m.Contributes, &c)
	}
	for _, t := range c.Themes {
		label := loc(t.Label)
		if label == "" {
			label = t.ID
		}
		x.Contributes.Themes = append(x.Contributes.Themes, ExtTheme{Label: label, UITheme: t.UITheme, Path: cleanExtPath(t.Path)})
	}
	for _, t := range c.IconThemes {
		label := loc(t.Label)
		if label == "" {
			label = t.ID
		}
		x.Contributes.IconThemes = append(x.Contributes.IconThemes, ExtIconTheme{Label: label, Path: cleanExtPath(t.Path)})
	}
	for _, s := range c.Snippets {
		var langs []string
		var one string
		if json.Unmarshal(s.Language, &one) == nil {
			langs = []string{one}
		} else {
			_ = json.Unmarshal(s.Language, &langs)
		}
		for _, l := range langs {
			x.Contributes.Snippets = append(x.Contributes.Snippets, ExtSnippets{Language: l, Path: cleanExtPath(s.Path)})
		}
	}
	for _, g := range c.Grammars {
		x.Contributes.Grammars = append(x.Contributes.Grammars, ExtGrammar{Language: g.Language, ScopeName: g.ScopeName, Path: cleanExtPath(g.Path)})
	}
	for _, l := range c.Languages {
		x.Contributes.Languages = append(x.Contributes.Languages, ExtLanguage{ID: l.ID, Aliases: l.Aliases, Extensions: l.Extensions, Filenames: l.Filenames, Configuration: cleanExtPath(l.Configuration)})
	}
	return x, nil
}

func cleanExtPath(p string) string {
	if p == "" {
		return ""
	}
	return path.Clean(strings.TrimPrefix(p, "./"))
}

// keepExisting drops contributions whose file was not unpacked.
func keepExisting(root string, c *ExtContributes) {
	has := func(p string) bool {
		if p == "" || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
			return false
		}
		st, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
		return err == nil && !st.IsDir()
	}
	themes := c.Themes[:0]
	for _, t := range c.Themes {
		if has(t.Path) {
			themes = append(themes, t)
		}
	}
	c.Themes = themes
	icons := c.IconThemes[:0]
	for _, t := range c.IconThemes {
		if has(t.Path) {
			icons = append(icons, t)
		}
	}
	c.IconThemes = icons
	snippets := c.Snippets[:0]
	for _, s := range c.Snippets {
		if has(s.Path) {
			snippets = append(snippets, s)
		}
	}
	c.Snippets = snippets
	grammars := c.Grammars[:0]
	for _, g := range c.Grammars {
		if has(g.Path) {
			grammars = append(grammars, g)
		}
	}
	c.Grammars = grammars
	for i := range c.Languages {
		if !has(c.Languages[i].Configuration) {
			c.Languages[i].Configuration = ""
		}
	}
}

func (e *Extensions) validID(id string) bool {
	return extNamePart.MatchString(id) && strings.Contains(id, ".") && !strings.HasPrefix(id, ".")
}

// File reads a data file of an installed extension.
func (e *Extensions) File(id, rel string) ([]byte, error) {
	if !e.validID(id) {
		return nil, ErrExtensionBadID
	}
	clean := path.Clean(rel)
	if rel == "" || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || !extDataFile[strings.ToLower(path.Ext(clean))] {
		return nil, ErrExtensionNotFound
	}
	b, err := os.ReadFile(filepath.Join(e.dir, id, filepath.FromSlash(clean)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrExtensionNotFound
	}
	return b, err
}

// Remove uninstalls an extension.
func (e *Extensions) Remove(id string) error {
	if !e.validID(id) {
		return ErrExtensionBadID
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	dir := filepath.Join(e.dir, id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return ErrExtensionNotFound
	}
	return os.RemoveAll(dir)
}
