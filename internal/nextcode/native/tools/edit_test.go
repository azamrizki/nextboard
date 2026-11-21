package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Cwd: dir}

	r := call(t, "Write", env, map[string]any{"file_path": "a/b/new.txt", "content": "one\ntwo\n"})
	path := filepath.Join(dir, "a", "b", "new.txt")
	if r.IsError || r.Output != "Created "+path+" (2 lines)" {
		t.Fatalf("got %+v", r)
	}
	if readFile(t, path) != "one\ntwo\n" || modeOf(t, path) != 0o644 || modeOf(t, filepath.Join(dir, "a")) != 0o755 {
		t.Errorf("file %q mode %v, folder mode %v", readFile(t, path), modeOf(t, path), modeOf(t, filepath.Join(dir, "a")))
	}

	os.Chmod(path, 0o600)
	r = call(t, "Write", env, map[string]any{"file_path": path, "content": "x"})
	if r.IsError || r.Output != "Updated "+path+" (1 line)" || readFile(t, path) != "x" || modeOf(t, path) != 0o600 {
		t.Errorf("got %+v, mode %v", r, modeOf(t, path))
	}

	r = call(t, "Write", env, map[string]any{"file_path": path, "content": ""})
	if r.IsError || r.Output != "Updated "+path+" (0 lines)" || readFile(t, path) != "" {
		t.Errorf("empty content: %+v", r)
	}

	for _, in := range []map[string]any{
		{"file_path": path},                // no content
		{"content": "x"},                   // no path
		{"file_path": dir, "content": "x"}, // a folder
	} {
		if r := call(t, "Write", env, in); !r.IsError {
			t.Errorf("%v: %+v", in, r)
		}
	}
}

func TestEdit(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Cwd: dir}
	put := func(name, text string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(p, mode)
		return p
	}

	cases := []struct {
		name     string
		text     string
		in       map[string]any
		isError  bool
		want     []string // in the output
		wantText string   // the file afterwards ("" = unchanged)
	}{
		{"once", "a\nb\nc\n", map[string]any{"old_string": "b", "new_string": "B"}, false,
			[]string{"Edited ", ": 1 replacement\n", "     1\ta\n     2\tB\n     3\tc"}, "a\nB\nc\n"},
		{"several without replace_all", "x y x\n", map[string]any{"old_string": "x", "new_string": "z"}, true,
			[]string{"2 matches of old_string", "give more context", "replace_all"}, ""},
		{"replace_all", "x y x\nx\n", map[string]any{"old_string": "x", "new_string": "z", "replace_all": true}, false,
			[]string{": 3 replacements"}, "z y z\nz\n"},
		{"delete", "keep\ndrop\nkeep\n", map[string]any{"old_string": "drop\n", "new_string": ""}, false,
			[]string{": 1 replacement"}, "keep\nkeep\n"},
		{"crlf file, lf old", "one\r\ntwo\r\nthree\r\n", map[string]any{"old_string": "one\ntwo\n", "new_string": "1\n2\n"}, false,
			[]string{"     1\t1\n     2\t2\n     3\tthree"}, "1\r\n2\r\nthree\r\n"},
		{"crlf file, new lines", "a\r\nb\r\n", map[string]any{"old_string": "a", "new_string": "a\nx"}, false,
			nil, "a\r\nx\r\nb\r\n"},
		{"lf file, crlf old", "one\ntwo\n", map[string]any{"old_string": "one\r\ntwo", "new_string": "x"}, true,
			[]string{"was not found", "other line endings"}, ""},
		{"trailing whitespace", "func a() {  \n\treturn\n}\n", map[string]any{"old_string": "func a() {\n\treturn", "new_string": "x"}, true,
			[]string{"was not found", "trailing whitespace"}, ""},
		{"indentation", "\tif x {\n\t\ty()\n", map[string]any{"old_string": "    if x {\n        y()", "new_string": "z"}, true,
			[]string{"indentation"}, ""},
		{"line numbers", "alpha\nbeta\n", map[string]any{"old_string": "     1\talpha\n     2\tbeta", "new_string": "z"}, true,
			[]string{"Read's line numbers"}, ""},
		{"not found", "alpha\n", map[string]any{"old_string": "gamma", "new_string": "z"}, true,
			[]string{"was not found", "exactly"}, ""},
		{"empty old", "alpha\n", map[string]any{"old_string": "", "new_string": "z"}, true, []string{"use Write to create a file"}, ""},
		{"same", "alpha\n", map[string]any{"old_string": "alpha", "new_string": "alpha"}, true, []string{"the same"}, ""},
		{"no new_string", "alpha\n", map[string]any{"old_string": "alpha"}, true, []string{"new_string"}, ""},
		{"no old_string", "alpha\n", map[string]any{"new_string": "alpha"}, true, []string{"old_string"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := put("f.txt", c.text, 0o640)
			c.in["file_path"] = p
			r := call(t, "Edit", env, c.in)
			if r.IsError != c.isError {
				t.Fatalf("IsError = %v: %s", r.IsError, r.Output)
			}
			mustContain(t, r, c.want...)
			want := c.wantText
			if want == "" {
				want = c.text
			}
			if got := readFile(t, p); got != want {
				t.Errorf("file = %q, want %q", got, want)
			}
			if modeOf(t, p) != 0o640 {
				t.Errorf("mode = %v", modeOf(t, p))
			}
		})
	}

	if r := call(t, "Edit", env, map[string]any{"file_path": "missing.txt", "old_string": "a", "new_string": "b"}); !r.IsError || !strings.Contains(r.Output, "does not exist") {
		t.Errorf("missing file: %+v", r)
	}
}

func TestEditSnippet(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString("line\n")
	}
	text := b.String()
	start := strings.Index(text, "line") + 5*49 // line 50
	got := snippet(text, start, start+4)
	lines := strings.Split(got, "\n")
	if len(lines) != 9 || !strings.HasPrefix(lines[0], "    46\t") || !strings.HasPrefix(lines[8], "    54\t") {
		t.Errorf("snippet:\n%s", got)
	}
	// a change over many lines is cut to snippetMax
	got = snippet(text, 0, len(text))
	if n := strings.Count(got, "\n"); n != snippetMax || !strings.Contains(got, "the change goes on to line 100") {
		t.Errorf("long snippet has %d lines:\n%s", n, got)
	}
}
