package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("NEXTBOARD_HOME", "/srv/hub")
	if got := DefaultDir(); got != "/srv/hub" {
		t.Errorf("with NEXTBOARD_HOME: %q", got)
	}
	t.Setenv("NEXTBOARD_HOME", "")
	t.Setenv("HOME", "/home/alice")
	if got := DefaultDir(); got != "/home/alice/.nextboard" {
		t.Errorf("without NEXTBOARD_HOME: %q", got)
	}
}

func TestOpenCreatesPrivateTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, s.Path("creds"), s.Path("scrollback")} {
		if got := mode(t, d); got != 0o700 {
			t.Errorf("%s: mode %o, want 700", d, got)
		}
	}
}

func TestOpenTightensAnExistingTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "creds"), 0o777); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, s.Path("creds")} {
		if got := mode(t, d); got != 0o700 {
			t.Errorf("%s: mode %o, want 700", d, got)
		}
	}
}

func TestOpenMakesThePathAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := Open("data")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(s.Dir) {
		t.Errorf("Dir = %q, want absolute: a later chdir must not move the data", s.Dir)
	}
}

func TestLoad(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	type doc struct {
		N int `json:"n"`
	}

	v := doc{N: 7}
	if ok, err := s.Load("missing.json", &v); ok || err != nil {
		t.Errorf("missing file: ok=%v err=%v", ok, err)
	}
	if v.N != 7 {
		t.Errorf("missing file changed the value to %d", v.N)
	}

	if err := os.WriteFile(s.Path("empty.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Load("empty.json", &v); ok || err != nil {
		t.Errorf("empty file: ok=%v err=%v", ok, err)
	}

	if err := os.WriteFile(s.Path("bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Load("bad.json", &v)
	if ok || err == nil {
		t.Fatalf("corrupt file: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("error does not name the file: %v", err)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]int{"a": 1}
	if err := s.Save("doc.json", in); err != nil {
		t.Fatal(err)
	}
	var out map[string]int
	if ok, err := s.Load("doc.json", &out); !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if out["a"] != 1 {
		t.Errorf("got %v", out)
	}
	if got := mode(t, s.Path("doc.json")); got != 0o600 {
		t.Errorf("mode %o, want 600", got)
	}
}

func TestSaveFailureKeepsTheOldFile(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save("doc.json", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("doc.json", make(chan int)); err == nil {
		t.Fatal("a value JSON cannot encode must fail")
	}
	var n int
	if ok, err := s.Load("doc.json", &n); !ok || err != nil || n != 1 {
		t.Errorf("n=%d ok=%v err=%v", n, ok, err)
	}
}

func TestWriteFileAtomicMakesAnOpenFilePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Errorf("content %q", got)
	}
	if got := mode(t, path); got != 0o600 {
		t.Errorf("mode %o, want 600", got)
	}
	if got := names(t, filepath.Dir(path)); len(got) != 1 {
		t.Errorf("leftovers next to the file: %v", got)
	}
}

func TestWriteFileAtomicLeavesNoTempOnFailure(t *testing.T) {
	dir := t.TempDir()
	// Rename onto a non-empty directory fails after the temp file was written.
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(target, []byte("x")); err == nil {
		t.Fatal("rename onto a directory must fail")
	}
	if got := names(t, dir); len(got) != 1 || got[0] != "target" {
		t.Errorf("leftovers: %v", got)
	}

	if err := WriteFileAtomic(filepath.Join(dir, "no", "such", "dir", "f"), []byte("x")); err == nil {
		t.Error("a missing directory must fail")
	}
}
