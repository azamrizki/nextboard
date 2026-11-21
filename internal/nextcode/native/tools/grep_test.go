package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func grepFixture(t *testing.T) string {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	tree(t, dir, map[string]string{
		"main.go":      "package main\n\nfunc main() {\n\tprintln(\"Hello\")\n}\n",
		"util/util.go": "package util\n\n// hello helps\nfunc Hello() string { return \"hello\" }\n",
		"web/app.ts":   "export const hello = 1;\nexport const other = 2;\n",
		"notes.md":     "one\ntwo\nthree\nfour\nfive\nsix\nseven\n",
		".hidden.txt":  "hello from a dot file\n",
		"bin.dat":      "hello\x00binary\n",
		".git/config":  "hello in git\n",
		"-dash.txt":    "-e dash\n",
	}, []string{"web/app.ts", "util/util.go", "main.go", ".hidden.txt", "notes.md"})
	return dir
}

func TestGrep(t *testing.T) {
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			useEngine(t, e.useRg)
			dir := grepFixture(t)
			env := &Env{Cwd: dir}
			p := func(name string) string { return filepath.Join(dir, name) }
			cases := []struct {
				name string
				in   map[string]any
				want string
			}{
				{"files, newest first", map[string]any{"pattern": "hello"},
					"Found 3 files\n" + p("web/app.ts") + "\n" + p("util/util.go") + "\n" + p(".hidden.txt")},
				{"ignore case", map[string]any{"pattern": "hello", "-i": true},
					"Found 4 files\n" + p("web/app.ts") + "\n" + p("util/util.go") + "\n" + p("main.go") + "\n" + p(".hidden.txt")},
				{"glob", map[string]any{"pattern": "hello", "-i": true, "glob": "*.go"},
					"Found 2 files\n" + p("util/util.go") + "\n" + p("main.go")},
				{"glob with a folder", map[string]any{"pattern": "hello", "-i": true, "glob": "util/*.go"},
					"Found 1 file\n" + p("util/util.go")},
				{"type", map[string]any{"pattern": "hello", "type": "ts"}, "Found 1 file\n" + p("web/app.ts")},
				{"path", map[string]any{"pattern": "hello", "path": "util"}, "Found 1 file\n" + p("util/util.go")},
				{"a file as path", map[string]any{"pattern": "Hello", "path": p("main.go"), "output_mode": "content"},
					p("main.go") + ":4:\tprintln(\"Hello\")"},
				{"content", map[string]any{"pattern": "hello", "path": "util", "output_mode": "content"},
					p("util/util.go") + ":3:// hello helps\n" + p("util/util.go") + ":4:func Hello() string { return \"hello\" }"},
				{"content without numbers", map[string]any{"pattern": "helps", "output_mode": "content", "-n": false},
					p("util/util.go") + ":// hello helps"},
				{"context", map[string]any{"pattern": "^(two|six)$", "path": "notes.md", "output_mode": "content", "-C": 1},
					p("notes.md") + "-1-one\n" + p("notes.md") + ":2:two\n" + p("notes.md") + "-3-three\n--\n" +
						p("notes.md") + "-5-five\n" + p("notes.md") + ":6:six\n" + p("notes.md") + "-7-seven"},
				{"touching context", map[string]any{"pattern": "^(three|six)$", "path": "notes.md", "output_mode": "content", "-C": 1},
					p("notes.md") + "-2-two\n" + p("notes.md") + ":3:three\n" + p("notes.md") + "-4-four\n" +
						p("notes.md") + "-5-five\n" + p("notes.md") + ":6:six\n" + p("notes.md") + "-7-seven"},
				{"before and after", map[string]any{"pattern": "four", "path": "notes.md", "output_mode": "content", "-B": 2, "-A": 0},
					p("notes.md") + "-2-two\n" + p("notes.md") + "-3-three\n" + p("notes.md") + ":4:four"},
				{"head_limit on content", map[string]any{"pattern": ".", "path": "notes.md", "output_mode": "content", "head_limit": 2},
					p("notes.md") + ":1:one\n" + p("notes.md") + ":2:two\n(the first 2 lines shown: raise head_limit or narrow the search for more)"},
				{"head_limit on files", map[string]any{"pattern": "hello", "head_limit": 1},
					"Found 3 files\n" + p("web/app.ts") + "\n(1 of 3 files shown, the newest first: raise head_limit or narrow the search)"},
				{"count", map[string]any{"pattern": "hello", "-i": true, "output_mode": "count", "glob": "*.go"},
					p("main.go") + ":1\n" + p("util/util.go") + ":2\n\nFound 3 matching lines in 2 files."},
				{"multiline", map[string]any{"pattern": "func main\\(\\) \\{.*?\\}", "multiline": true, "output_mode": "content"},
					p("main.go") + ":3:func main() {\n" + p("main.go") + ":4:\tprintln(\"Hello\")\n" + p("main.go") + ":5:}"},
				{"no multiline", map[string]any{"pattern": "main\\(\\) \\{.*println"}, "No matches found"},
				{"a pattern like a flag", map[string]any{"pattern": "-e dash", "output_mode": "content"},
					p("-dash.txt") + ":1:-e dash"},
				{"nothing", map[string]any{"pattern": "zebra"}, "No matches found"},
				{"nothing to count", map[string]any{"pattern": "zebra", "output_mode": "count"}, "No matches found"},
			}
			for _, c := range cases {
				r := call(t, "Grep", env, c.in)
				if r.IsError || r.Output != c.want {
					t.Errorf("%s: got (error %v)\n%s\nwant\n%s", c.name, r.IsError, r.Output, c.want)
				}
			}
			for _, in := range []map[string]any{
				{"pattern": "("},
				{"pattern": "x", "output_mode": "lines"},
				{"pattern": "x", "path": "missing"},
				{"pattern": ""},
				{"pattern": "x", "type": "nosuchtype"},
			} {
				if r := call(t, "Grep", env, in); !r.IsError {
					t.Errorf("%v: %+v", in, r)
				}
			}
		})
	}
}

func TestGrepFallbackSkipsNodeModules(t *testing.T) {
	useEngine(t, false)
	dir := t.TempDir()
	tree(t, dir, map[string]string{"node_modules/x/i.js": "needle\n", "app.js": "needle\n"}, nil)
	r := call(t, "Grep", &Env{Cwd: dir}, map[string]any{"pattern": "needle"})
	if r.Output != "Found 1 file\n"+filepath.Join(dir, "app.js") {
		t.Errorf("got %s", r.Output)
	}
}

func TestGrepLongLinesAndSize(t *testing.T) {
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			useEngine(t, e.useRg)
			dir := t.TempDir()
			var b strings.Builder
			for i := range 300 {
				fmt.Fprintf(&b, "match %d %s\n", i, strings.Repeat("y", 3000))
			}
			os.WriteFile(filepath.Join(dir, "wide.txt"), []byte(b.String()), 0o644)
			r := call(t, "Grep", &Env{Cwd: dir}, map[string]any{"pattern": "match", "output_mode": "content", "head_limit": 1000})
			lines := strings.Split(r.Output, "\n")
			if r.IsError || len(r.Output) > grepMaxSize+3*maxLineChars || !strings.HasSuffix(lines[0], "…") ||
				!strings.Contains(lines[len(lines)-1], "about 100 KB") {
				t.Errorf("%d bytes, %d lines, first ends %q, last %q", len(r.Output), len(lines), lines[0][len(lines[0])-10:], lines[len(lines)-1])
			}
		})
	}
}
