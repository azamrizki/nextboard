package studio

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func vsix(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const themeManifest = `{"displayName":"%name%","description":"d","main":"./out/ext.js","contributes":{
 "themes":[{"label":"Night","uiTheme":"vs-dark","path":"./themes/night.json"},{"label":"Gone","path":"./themes/missing.json"}],
 "snippets":[{"language":["go","python"],"path":"./snip.code-snippets"},{"language":"rust","path":"./missing.json"}],
 "grammars":[{"language":"foo","scopeName":"source.foo","path":"./syntaxes/foo.tmLanguage.json"}],
 "languages":[{"id":"foo","extensions":[".foo"],"aliases":["Foo"],"configuration":"./lang.json"}]}}`

func TestExtensionInstallKeepsDataNotCode(t *testing.T) {
	dir := t.TempDir()
	e := NewExtensions(dir)
	x, err := e.installPackage(vsix(t, map[string]string{
		"extension/package.json":                 themeManifest,
		"extension/package.nls.json":             `{"name":"Night Theme"}`,
		"extension/themes/night.json":            `{"colors":{}}`,
		"extension/snip.code-snippets":           `{}`,
		"extension/syntaxes/foo.tmLanguage.json": `{}`,
		"extension/lang.json":                    `{}`,
		"extension/out/ext.js":                   `process.exit(1)`,
		"extension/bin/tool.sh":                  `#!/bin/sh`,
		"extension/node_modules/x/package.json":  `{}`,
		"extension/../../escape.json":            `{}`,
		"evil.json":                              `{}`,
	}), "acme", "night", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if x.ID != "acme.night" || x.DisplayName != "Night Theme" || !x.HasCode || x.Version != "1.2.3" {
		t.Fatalf("%+v", x)
	}
	c := x.Contributes
	if len(c.Themes) != 1 || c.Themes[0].Path != "themes/night.json" {
		t.Fatalf("themes %+v", c.Themes)
	}
	if len(c.Snippets) != 2 || c.Snippets[1].Language != "python" {
		t.Fatalf("snippets %+v", c.Snippets)
	}
	if len(c.Grammars) != 1 || len(c.Languages) != 1 || c.Languages[0].Configuration != "lang.json" {
		t.Fatalf("%+v", c)
	}
	for _, gone := range []string{"out/ext.js", "bin/tool.sh", "node_modules/x/package.json"} {
		if _, err := os.Stat(filepath.Join(dir, "acme.night", gone)); err == nil {
			t.Fatalf("%s was unpacked", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.json")); err == nil {
		t.Fatal("zip entry escaped the folder")
	}
	list, err := e.List()
	if err != nil || len(list) != 1 || list[0].ID != "acme.night" {
		t.Fatalf("%+v %v", list, err)
	}
	if b, err := e.File("acme.night", "themes/night.json"); err != nil || string(b) != `{"colors":{}}` {
		t.Fatalf("%q %v", b, err)
	}
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd", "out/ext.js", ""} {
		if _, err := e.File("acme.night", bad); err == nil {
			t.Fatalf("file %q served", bad)
		}
	}
	if _, err := e.File("../x", "a.json"); !errors.Is(err, ErrExtensionBadID) {
		t.Fatalf("bad id: %v", err)
	}
	if err := e.Remove("acme.night"); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("acme.night"); !errors.Is(err, ErrExtensionNotFound) {
		t.Fatalf("second remove: %v", err)
	}
}

func TestExtensionCodeOnlyIsRefusedAndOldVersionKept(t *testing.T) {
	dir := t.TempDir()
	e := NewExtensions(dir)
	if _, err := e.installPackage(vsix(t, map[string]string{"extension/package.json": `{"main":"x.js","contributes":{"commands":[]}}`}), "a", "code", "1"); !errors.Is(err, ErrExtensionCodeOnly) {
		t.Fatalf("got %v", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}
	good := map[string]string{"extension/package.json": `{"contributes":{"themes":[{"label":"T","path":"./t.json"}]}}`, "extension/t.json": `{}`}
	if _, err := e.installPackage(vsix(t, good), "a", "t", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.installPackage([]byte("not a zip"), "a", "t", "2"); err == nil {
		t.Fatal("bad package accepted")
	}
	list, _ := e.List()
	if len(list) != 1 || list[0].Version != "1" {
		t.Fatalf("%+v", list)
	}
}

func TestExtensionInstallAndSearchOverHTTP(t *testing.T) {
	pkg := vsix(t, map[string]string{"extension/package.json": `{"displayName":"T","contributes":{"themes":[{"label":"T","path":"t.json"}]}}`, "extension/t.json": `{}`})
	mux := http.NewServeMux()
	var ts *httptest.Server
	mux.HandleFunc("/api/-/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != "dark theme" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"extensions": []map[string]any{{"namespace": "a", "name": "t", "displayName": "T", "downloadCount": 3, "version": "9", "files": map[string]string{"download": ts.URL + "/pkg/a.t.vsix"}}}})
	})
	mux.HandleFunc("/pkg/package.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"contributes":{"themes":[{"label":"T","path":"t.json"}]}}`))
	})
	mux.HandleFunc("/pkg/a.t.vsix", func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Content-Length", "10") })
	mux.HandleFunc("/api/a/t", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "9", "files": map[string]string{"download": ts.URL + "/dl"}})
	})
	mux.HandleFunc("/dl", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(pkg) })
	ts = httptest.NewTLSServer(mux)
	defer ts.Close()
	e := NewExtensions(t.TempDir())
	e.base = ts.URL + "/api"
	e.client = ts.Client()
	hits, err := e.Search(context.Background(), "dark theme", 5)
	if err != nil || len(hits.Items) != 1 || hits.Items[0].DownloadCount != 3 {
		t.Fatalf("%+v %v", hits, err)
	}
	x, err := e.Install(context.Background(), "a", "t")
	if err != nil || x.Version != "9" {
		t.Fatalf("%+v %v", x, err)
	}
	if _, err := e.Install(context.Background(), "a/../b", "t"); !errors.Is(err, ErrExtensionBadID) {
		t.Fatalf("bad name: %v", err)
	}
	if _, err := e.Install(context.Background(), "a", "missing"); !errors.Is(err, ErrExtensionNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestAdapterPortAndCatalog(t *testing.T) {
	r := strings.NewReader("some banner\nDAP server listening at: 127.0.0.1:43217\nmore\n")
	if p, err := AdapterPort(r); err != nil || p != 43217 {
		t.Fatalf("%d %v", p, err)
	}
	if _, err := AdapterPort(strings.NewReader("no port here\n")); err == nil {
		t.Fatal("no port accepted")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	h := filesHost(t)
	if err := os.WriteFile(filepath.Join(bin, "lldb-vscode"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rows, err := DebugAdapterList(context.Background(), h, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "native" && !row.Available {
			t.Fatalf("native row %+v", row)
		}
	}
	res, err := ResolveDebugAdapter(context.Background(), h, t.TempDir(), "native")
	if err != nil || res.TCP || !strings.Contains(res.Script, "exec ") {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := ResolveDebugAdapter(context.Background(), h, t.TempDir(), "nope"); !errors.Is(err, ErrNoDebugAdapter) {
		t.Fatalf("unknown adapter: %v", err)
	}
}

func TestNativeExtensionInstallsWithoutData(t *testing.T) {
	dir := t.TempDir()
	e := NewExtensions(dir)
	code := map[string]string{"extension/package.json": `{"displayName":"Vim","main":"./out/ext.js","contributes":{"commands":[]}}`, "extension/out/ext.js": `process.exit(1)`}
	x, err := e.installPackage(vsix(t, code), "vscodevim", "vim", "1")
	if err != nil {
		t.Fatal(err)
	}
	if x.Native != "vim" || !x.HasCode {
		t.Fatalf("%+v", x)
	}
	if _, err := os.Stat(filepath.Join(dir, "vscodevim.vim", "out", "ext.js")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("code was kept: %v", err)
	}
	// the id decides, in any case; a stranger with the same contents stays refused
	if x, err = e.installPackage(vsix(t, code), "Vue", "volar", "1"); err != nil || x.Native != "lsp:vue" {
		t.Fatalf("%+v %v", x, err)
	}
	if _, err := e.installPackage(vsix(t, code), "someone", "vim", "1"); !errors.Is(err, ErrExtensionCodeOnly) {
		t.Fatalf("got %v", err)
	}
	// installed before the table knew it
	old := filepath.Join(dir, "esbenp.prettier-vscode")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "nextboard-manifest.json"), []byte(`{"id":"esbenp.prettier-vscode"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	natives := map[string]string{}
	for _, l := range list {
		natives[l.ID] = l.Native
	}
	if natives["esbenp.prettier-vscode"] != "prettier" {
		t.Fatalf("%+v", natives)
	}
}

func TestIconThemeKeepsItsPictures(t *testing.T) {
	dir := t.TempDir()
	e := NewExtensions(dir)
	x, err := e.installPackage(vsix(t, map[string]string{
		"extension/package.json":     `{"main":"x.js","contributes":{"iconThemes":[{"id":"mat","label":"%l%","path":"./dist/icons.json"},{"id":"gone","path":"./dist/gone.json"}]}}`,
		"extension/package.nls.json": `{"l":"Material"}`,
		"extension/dist/icons.json":  `{"iconDefinitions":{"go":{"iconPath":"./../icons/go.svg"}},"file":"go"}`,
		"extension/icons/go.svg":     `<svg xmlns="http://www.w3.org/2000/svg"/>`,
		"extension/x.js":             `process.exit(1)`,
	}), "PKief", "material-icon-theme", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Contributes.IconThemes) != 1 || x.Contributes.IconThemes[0].Label != "Material" || x.Contributes.IconThemes[0].Path != "dist/icons.json" {
		t.Fatalf("%+v", x.Contributes.IconThemes)
	}
	b, err := e.File(x.ID, "icons/go.svg")
	if err != nil || !strings.Contains(string(b), "<svg") {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := e.File(x.ID, "x.js"); !errors.Is(err, ErrExtensionNotFound) {
		t.Fatalf("got %v", err)
	}
}

// A fake Open VSX: one hit per entry, its manifest next to its package.
func searchServer(t *testing.T, manifests map[string]string, size map[string]int64, fails map[string]bool) (*Extensions, *atomic.Int64, *httptest.Server) {
	t.Helper()
	var manifestReads atomic.Int64
	mux := http.NewServeMux()
	var ts *httptest.Server
	names := make([]string, 0, len(manifests))
	for n := range manifests {
		names = append(names, n)
	}
	sort.Strings(names)
	mux.HandleFunc("/api/-/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("size") != "50" {
			t.Errorf("asked for %q hits", r.URL.Query().Get("size"))
		}
		hits := []map[string]any{}
		for _, n := range names {
			ns, name, _ := strings.Cut(n, ".")
			hits = append(hits, map[string]any{"namespace": ns, "name": name, "displayName": n, "version": "1", "files": map[string]string{"download": ts.URL + "/p/" + n + "/x.vsix"}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"extensions": hits})
	})
	mux.HandleFunc("/p/{id}/package.json", func(w http.ResponseWriter, r *http.Request) {
		manifestReads.Add(1)
		if fails[r.PathValue("id")] {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(manifests[r.PathValue("id")]))
	})
	mux.HandleFunc("/p/{id}/x.vsix", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("the package was downloaded for a search: %s", r.Method)
		}
		w.Header().Set("Content-Length", strconv.FormatInt(size[r.PathValue("id")], 10))
	})
	ts = httptest.NewTLSServer(mux)
	t.Cleanup(ts.Close)
	e := NewExtensions(t.TempDir())
	e.base = ts.URL + "/api"
	e.client = ts.Client()
	return e, &manifestReads, ts
}

func TestSearchShowsOnlyWhatCanBeInstalled(t *testing.T) {
	code := `{"main":"./extension.js","contributes":{"commands":[{"command":"x"}],"views":{}}}`
	e, reads, _ := searchServer(t, map[string]string{
		"anthropic.claude-code": code,
		"a.code":                code,
		"a.pack":                `{"extensionPack":["b.c"]}`,
		"a.injection":           `{"main":"x.js","contributes":{"grammars":[{"scopeName":"s","path":"g.json","injectTo":["source.ts"]}]}}`,
		"a.jsgrammar":           `{"main":"x.js","contributes":{"grammars":[{"language":"l","scopeName":"s","path":"out/g.js"}]}}`,
		"a.huge":                `{"contributes":{"themes":[{"label":"T","path":"t.json"}]}}`,
		"a.theme":               `{"contributes":{"themes":[{"label":"T","path":"./t.json"}]}}`,
		"a.partial":             `{"main":"x.js","contributes":{"grammars":[{"language":"l","scopeName":"s","path":"g.json"}],"snippets":[{"language":"l","path":"s.json"}]}}`,
		"a.tooltheme":           `{"main":"x.js","categories":["Debuggers"],"contributes":{"themes":[{"label":"T","path":"t.json"}]}}`,
		"a.syntax":              `{"contributes":{"grammars":[{"language":"l","scopeName":"s","path":"g.json"}],"snippets":[{"language":"l","path":"s.json"}]}}`,
		"a.codetheme":           `{"main":"x.js","categories":["Themes"],"contributes":{"iconThemes":[{"id":"i","path":"./i.json"}]}}`,
		"a.icons":               `{"contributes":{"iconThemes":[{"id":"i","path":"./i.json"}]}}`,
		"vscodevim.vim":         code,
	}, map[string]int64{"a.huge": extMaxDownload + 1, "a.theme": 10}, nil)
	res, err := e.Search(context.Background(), "x", 24)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ExtSearchItem{}
	for _, h := range res.Items {
		got[h.Namespace+"."+h.Name] = h
	}
	if len(got) != 5 || res.Hidden != 8 || res.Unchecked != 0 {
		t.Fatalf("%+v", res)
	}
	if h := got["vscodevim.vim"]; h.Native != "vim" || !h.HasCode {
		t.Fatalf("%+v", h)
	}
	if h := got["a.syntax"]; h.HasCode || !slices.Equal(h.Gives, []string{"syntax", "snippets"}) {
		t.Fatalf("%+v", h)
	}
	if h := got["a.codetheme"]; !h.HasCode || !slices.Equal(h.Gives, []string{"icon theme"}) {
		t.Fatalf("%+v", h)
	}
	if h := got["a.theme"]; h.HasCode || !slices.Equal(h.Gives, []string{"colour theme"}) {
		t.Fatalf("%+v", h)
	}
	if h := got["a.icons"]; !slices.Equal(h.Gives, []string{"icon theme"}) {
		t.Fatalf("%+v", h)
	}
	// a version never changes: asking again costs nothing
	before := reads.Load()
	if res, err = e.Search(context.Background(), "x", 2); err != nil || len(res.Items) != 2 || res.Hidden != 8 || reads.Load() != before {
		t.Fatalf("%+v %v reads %d→%d", res, err, before, reads.Load())
	}
}

func TestSearchWhenManifestsDoNotArrive(t *testing.T) {
	theme := `{"contributes":{"themes":[{"label":"T","path":"t.json"}]}}`
	e, _, _ := searchServer(t, map[string]string{"a.one": theme, "a.two": theme}, nil, map[string]bool{"a.two": true})
	res, err := e.Search(context.Background(), "x", 24)
	if err != nil || len(res.Items) != 1 || res.Unchecked != 1 || res.Hidden != 0 {
		t.Fatalf("one down: %+v %v", res, err)
	}
	e, _, _ = searchServer(t, map[string]string{"a.one": theme, "a.two": theme}, nil, map[string]bool{"a.one": true, "a.two": true})
	if res, err := e.Search(context.Background(), "x", 24); err == nil {
		t.Fatalf("all down looks like no results: %+v", res)
	}
	// a failure is not remembered
	e, reads, _ := searchServer(t, map[string]string{"a.one": theme}, nil, map[string]bool{"a.one": true})
	_, _ = e.Search(context.Background(), "x", 24)
	_, _ = e.Search(context.Background(), "x", 24)
	if reads.Load() != 2 {
		t.Fatalf("reads %d", reads.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Search(ctx, "x", 24); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestInjectionGrammarAloneIsRefused(t *testing.T) {
	e := NewExtensions(t.TempDir())
	_, err := e.installPackage(vsix(t, map[string]string{
		"extension/package.json": `{"main":"x.js","contributes":{"grammars":[{"scopeName":"s","path":"g.json","injectTo":["source.ts"]}]}}`,
		"extension/g.json":       `{}`,
	}), "a", "injection", "1")
	if !errors.Is(err, ErrExtensionCodeOnly) {
		t.Fatalf("got %v", err)
	}
}
