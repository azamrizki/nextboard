package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLines(t *testing.T, path string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRead(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Cwd: dir}
	writeLines(t, filepath.Join(dir, "ten.txt"), 10)
	writeLines(t, filepath.Join(dir, "long.txt"), 2500)
	os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "crlf.txt"), []byte("a\r\nb\r\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "nonl.txt"), []byte("one\ntwo"), 0o644)
	os.WriteFile(filepath.Join(dir, "wide.txt"), []byte(strings.Repeat("é", 2500)+"\nshort\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bin.dat"), []byte("abc\x00def"), 0o644)
	os.WriteFile(filepath.Join(dir, "pic.png"), []byte("not really"), 0o644)
	os.WriteFile(filepath.Join(dir, "pic.bin"), []byte("GIF89a....."), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)

	cases := []struct {
		name    string
		in      map[string]any
		isError bool
		want    []string
		not     []string
	}{
		{"whole", map[string]any{"file_path": filepath.Join(dir, "ten.txt")}, false,
			[]string{"     1\tline 1\n", "    10\tline 10"}, []string{"more line"}},
		{"relative", map[string]any{"file_path": "ten.txt"}, false, []string{"     1\tline 1"}, nil},
		{"offset and limit", map[string]any{"file_path": "ten.txt", "offset": 3, "limit": 2}, false,
			[]string{"     3\tline 3\n     4\tline 4\n… 6 more lines; read on with offset 5"}, []string{"line 2\n", "line 5"}},
		{"default limit", map[string]any{"file_path": "long.txt"}, false,
			[]string{"  2000\tline 2000\n… 500 more lines; read on with offset 2001"}, []string{"line 2001"}},
		{"offset past the end", map[string]any{"file_path": "ten.txt", "offset": 50}, false,
			[]string{"the file has 10 lines; offset 50 is past its end"}, nil},
		{"empty", map[string]any{"file_path": "empty.txt"}, false, []string{"(the file is empty)"}, nil},
		{"crlf", map[string]any{"file_path": "crlf.txt"}, false, []string{"     1\ta\n     2\tb"}, []string{"\r"}},
		{"no last newline", map[string]any{"file_path": "nonl.txt"}, false, []string{"     2\ttwo"}, nil},
		{"long line", map[string]any{"file_path": "wide.txt"}, false,
			[]string{"\t" + strings.Repeat("é", 2000) + "…\n", "     2\tshort"}, []string{strings.Repeat("é", 2001)}},
		{"binary", map[string]any{"file_path": "bin.dat"}, false, []string{"binary file, 7 bytes, not shown"}, nil},
		{"image by name", map[string]any{"file_path": "pic.png"}, false, []string{"image file (png, 10 bytes): images are not supported yet"}, nil},
		{"image by bytes", map[string]any{"file_path": "pic.bin"}, false, []string{"image file (gif, 11 bytes)"}, nil},
		{"directory", map[string]any{"file_path": "sub"}, true, []string{"is a directory; use Glob, or Bash ls"}, nil},
		{"missing", map[string]any{"file_path": "nope.txt"}, true, []string{"File does not exist", "nope.txt"}, nil},
		{"no path", map[string]any{"offset": 3}, true, []string{"file_path"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := call(t, "Read", env, c.in)
			if r.IsError != c.isError {
				t.Fatalf("IsError = %v: %s", r.IsError, r.Output)
			}
			mustContain(t, r, c.want...)
			for _, n := range c.not {
				if strings.Contains(r.Output, n) {
					t.Errorf("output has %q", n)
				}
			}
		})
	}
}

func TestReadSizeCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	line := strings.Repeat("x", 999) + "\n"
	os.WriteFile(path, []byte(strings.Repeat(line, 500)), 0o644)
	r := call(t, "Read", &Env{Cwd: dir}, map[string]any{"file_path": path})
	if r.IsError {
		t.Fatal(r.Output)
	}
	if len(r.Output) > maxReadSize+2*maxLineChars {
		t.Errorf("output is %d bytes", len(r.Output))
	}
	shown := strings.Count(r.Output, "\t")
	mustContain(t, r, fmt.Sprintf("… %d more lines; read on with offset %d", 500-shown, shown+1))
}
