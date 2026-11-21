package tools

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// engines are the two ways Glob and Grep search: with ripgrep and with the Go fallback.
var engines = []struct {
	name  string
	useRg bool
}{{"rg", true}, {"go", false}}

// useEngine makes lookRg find ripgrep, or not, for the rest of the test.
func useEngine(t *testing.T, rg bool) {
	t.Helper()
	old := lookRg
	t.Cleanup(func() { lookRg = old })
	if !rg {
		lookRg = func() (string, error) { return "", errors.New("no rg") }
		return
	}
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
}

// tree makes files under dir, each a minute older than the one before it, so that newest
// first is their order here.
func tree(t *testing.T, dir string, files map[string]string, order []string) {
	t.Helper()
	for name, text := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for i, name := range order {
		at := now.Add(-time.Duration(i) * time.Minute)
		os.Chtimes(filepath.Join(dir, name), at, at)
	}
}

func globFixture(t *testing.T) string {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	order := []string{"src/sub/c.go", "a.go", "src/b.go", "src/d.ts", "src/sub/e.tsx", ".hid/f.go", "README.md", "src/README.md", ".git/h.go"}
	files := map[string]string{}
	for _, f := range order {
		files[f] = "x\n"
	}
	tree(t, dir, files, order)
	return dir
}

func TestGlob(t *testing.T) {
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			useEngine(t, e.useRg)
			dir := globFixture(t)
			env := &Env{Cwd: dir}
			abs := func(names ...string) string {
				for i, n := range names {
					names[i] = filepath.Join(dir, n)
				}
				return strings.Join(names, "\n")
			}
			cases := []struct {
				pattern, path, want string
			}{
				{"*.go", "", abs("src/sub/c.go", "a.go", "src/b.go", ".hid/f.go")},
				{"**/*.go", "", abs("src/sub/c.go", "a.go", "src/b.go", ".hid/f.go")},
				{"src/*.go", "", abs("src/b.go")},
				{"src/**/*.go", "", abs("src/sub/c.go", "src/b.go")},
				{"**/sub/*", "", abs("src/sub/c.go", "src/sub/e.tsx")},
				{"*.{ts,tsx}", "", abs("src/d.ts", "src/sub/e.tsx")},
				{"[ab].go", "", abs("a.go", "src/b.go")},
				{"?.go", "src", abs("src/sub/c.go", "src/b.go")},
				{"README.md", "", abs("README.md", "src/README.md")},
				{"./src/*.ts", "", abs("src/d.ts")},
				{"*.go", "src", abs("src/sub/c.go", "src/b.go")},
				{filepath.Join(dir, "src", "**", "*.go"), "", abs("src/sub/c.go", "src/b.go")},
				{filepath.Join(dir, "a.go"), "", abs("a.go")},
				{"*.rs", "", "No files found"},
				{"src", "", "No files found"}, // folders are not files
			}
			for _, c := range cases {
				r := call(t, "Glob", env, map[string]any{"pattern": c.pattern, "path": c.path})
				if r.IsError || r.Output != c.want {
					t.Errorf("Glob(%q, %q) =\n%s\nwant\n%s", c.pattern, c.path, r.Output, c.want)
				}
			}
			for _, in := range []map[string]any{{"pattern": "*", "path": "missing"}, {"pattern": "*", "path": "a.go"}, {"path": dir}} {
				if r := call(t, "Glob", env, in); !r.IsError {
					t.Errorf("%v: %+v", in, r)
				}
			}
		})
	}
}

func TestGlobFallbackSkipsNodeModules(t *testing.T) {
	useEngine(t, false)
	dir := t.TempDir()
	tree(t, dir, map[string]string{"node_modules/x/i.js": "", "app.js": "", "lib/node_modules/y.js": ""}, nil)
	r := call(t, "Glob", &Env{Cwd: dir}, map[string]any{"pattern": "**/*.js"})
	if r.Output != filepath.Join(dir, "app.js") {
		t.Errorf("got %s", r.Output)
	}
}

func TestGlobCap(t *testing.T) {
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			useEngine(t, e.useRg)
			dir := t.TempDir()
			for i := range globMax + 20 {
				os.WriteFile(filepath.Join(dir, strings.Repeat("f", 1+i%7)+string(rune('a'+i%26))+time.Duration(i).String()+".txt"), nil, 0o644)
			}
			r := call(t, "Glob", &Env{Cwd: dir}, map[string]any{"pattern": "*.txt"})
			lines := strings.Split(r.Output, "\n")
			if len(lines) != globMax+1 || lines[globMax] != "(500 shown of 520 files, the newest first: narrow the pattern or the path for the rest)" {
				t.Errorf("%d lines, last %q", len(lines), lines[len(lines)-1])
			}
		})
	}
}

func TestGlobRegexp(t *testing.T) {
	cases := []struct {
		glob string
		yes  []string
		no   []string
	}{
		{"*.go", []string{"a.go", "x/y/a.go", ".a.go"}, []string{"a.goo", "a.go/x"}},
		{"src/*.go", []string{"src/a.go"}, []string{"src/x/a.go", "x/src/a.go"}},
		{"/src/*.go", []string{"src/a.go"}, []string{"x/src/a.go"}},
		{"src/**", []string{"src/a", "src/x/y"}, []string{"src", "srcx/a"}},
		{"**/x/*.go", []string{"x/a.go", "a/b/x/c.go"}, []string{"x/y/a.go"}},
		{"a/**/b", []string{"a/b", "a/x/b", "a/x/y/b"}, []string{"a/xb"}},
		{"*.{ts,tsx,d.ts}", []string{"a.ts", "a.tsx", "b/a.d.ts"}, []string{"a.t"}},
		{"{src,lib}/**/*.{js,mjs}", []string{"src/a.js", "lib/x/b.mjs"}, []string{"test/a.js"}},
		{"[!a]*.go", []string{"b.go"}, []string{"a.go"}},
		{"[a-c]?.md", []string{"ax.md", "cz.md"}, []string{"dx.md", "a.md"}},
		{"file\\*.txt", []string{"file*.txt"}, []string{"filex.txt"}},
		{"é*.txt", []string{"été.txt"}, []string{"e.txt"}},
		{"a+b(c).txt", []string{"a+b(c).txt"}, []string{"aab(c).txt"}},
	}
	for _, c := range cases {
		re, err := globRegexp(c.glob)
		if err != nil {
			t.Errorf("%s: %v", c.glob, err)
			continue
		}
		for _, p := range c.yes {
			if !re.MatchString(p) {
				t.Errorf("%s should match %s (%s)", c.glob, p, re)
			}
		}
		for _, p := range c.no {
			if re.MatchString(p) {
				t.Errorf("%s should not match %s (%s)", c.glob, p, re)
			}
		}
	}
	if _, err := globRegexp("*.{go,ts"); err == nil {
		t.Error("an unclosed { is no error")
	}
}
