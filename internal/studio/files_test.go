package studio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/machine"
)

func filesHost(t *testing.T) machine.Host {
	t.Helper()
	return machine.NewLocal(machine.Config{})
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func gitInit(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
}

func TestListFilesGitLeavesOutIgnoredAndDeleted(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	writeTree(t, root, map[string]string{".gitignore": "secret.txt\nbuild/\n", "a.go": "x", "secret.txt": "x", "build/out.o": "x", "sub/b.go": "x", "gone.go": "x"})
	for _, args := range [][]string{{"add", "."}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-qm", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.Remove(filepath.Join(root, "gone.go")); err != nil {
		t.Fatal(err)
	}
	fl, err := ListFiles(context.Background(), filesHost(t), root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(fl.Items, " ")
	if fl.Source != "git" || got != ".gitignore a.go sub/b.go" {
		t.Fatalf("source %q items %q", fl.Source, got)
	}
}

func TestListFilesWalkPrunes(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "x", "node_modules/m/i.js": "x", "vendor/v.php": "x", "src/.hidden": "x"})
	fl, err := ListFiles(context.Background(), filesHost(t), root)
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(fl.Items, " ") + " "
	if fl.Source != "walk" || !strings.Contains(joined, " a.txt ") || !strings.Contains(joined, " src/.hidden ") || strings.Contains(joined, "node_modules") || strings.Contains(joined, "vendor") {
		t.Fatalf("source %q items %q", fl.Source, joined)
	}
}

func TestSearchOptions(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "Hello world\nhello there\nothello\n", "sub/b.md": "say HELLO\r\n", "c.bin": "hello\x00binary", "node_modules/m.js": "hello\n", "vendor/v.php": "hello\n"})
	h := filesHost(t)
	ctx := context.Background()
	count := func(o SearchOptions) int {
		t.Helper()
		r, err := Search(ctx, h, root, o)
		if err != nil {
			t.Fatal(err)
		}
		return r.Count
	}
	if n := count(SearchOptions{Query: "hello"}); n != 4 {
		t.Fatalf("insensitive: %d", n)
	}
	if n := count(SearchOptions{Query: "hello", MatchCase: true}); n != 2 {
		t.Fatalf("case: %d", n)
	}
	if n := count(SearchOptions{Query: "hello", WholeWord: true}); n != 3 {
		t.Fatalf("whole word: %d", n)
	}
	if n := count(SearchOptions{Query: "h[e]llo\\b", Regex: true, MatchCase: true}); n != 2 {
		t.Fatalf("regex: %d", n)
	}
	if n := count(SearchOptions{Query: "hello", Include: "*.md"}); n != 1 {
		t.Fatalf("include: %d", n)
	}
	if n := count(SearchOptions{Query: "hello", Exclude: "sub"}); n != 3 {
		t.Fatalf("exclude: %d", n)
	}
	if _, err := Search(ctx, h, root, SearchOptions{Query: "(", Regex: true}); err == nil {
		t.Fatal("bad regex accepted")
	}
}

func TestSearchColumnsAreUTF16(t *testing.T) {
	re := regexp.MustCompile(`needle`)
	out := []byte("grep\n./a.txt\x001:🙂 héllo needle tail\r\n")
	r := parseSearch(out, re, SearchOptions{Limit: 10})
	if len(r.Files) != 1 || r.Files[0].Rel != "a.txt" {
		t.Fatalf("%+v", r)
	}
	m := r.Files[0].Matches[0]
	// 🙂 is two UTF-16 units: "🙂 héllo " is 9.
	if m.Line != 1 || m.Col != 9 || m.Len != 6 || m.Text[m.TextCol:] == "" || strings.HasSuffix(m.Text, "\r") {
		t.Fatalf("%+v", m)
	}
}

func TestSearchLimitTruncates(t *testing.T) {
	re := regexp.MustCompile(`a`)
	r := parseSearch([]byte("rg\nf\x001:aaa\n"), re, SearchOptions{Limit: 2})
	if r.Count != 2 || !r.Truncated {
		t.Fatalf("%+v", r)
	}
}

func TestReplaceKeepsLineEndingsAndChecksPaths(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "\xef\xbb\xbfone foo\r\ntwo foo\r\n", "b.txt": "nothing"})
	h := filesHost(t)
	r, err := Replace(context.Background(), h, root, SearchOptions{Query: "foo", MatchCase: true}, "$bar", []string{"a.txt", "b.txt", "../x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Count != 2 || r.Files[1].Count != 0 || r.Files[2].Error == "" {
		t.Fatalf("%+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(b) != "\xef\xbb\xbfone $bar\r\ntwo $bar\r\n" {
		t.Fatalf("%q", b)
	}
	r, err = Replace(context.Background(), h, root, SearchOptions{Query: `(\w+) \$bar`, Regex: true}, "$1!", []string{"a.txt"}, nil)
	if err != nil || r.Count != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	b, _ = os.ReadFile(filepath.Join(root, "a.txt"))
	if string(b) != "\xef\xbb\xbfone!\r\ntwo!\r\n" {
		t.Fatalf("%q", b)
	}
}

func TestIgnored(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	writeTree(t, root, map[string]string{".gitignore": "*.log\ndist/\n", "dist/x": "x"})
	got, err := Ignored(context.Background(), filesHost(t), root, []string{"a.log", "a.go", "dist", "src/x.log"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "a.log dist src/x.log" {
		t.Fatalf("%q", got)
	}
	plain := t.TempDir()
	got, err = Ignored(context.Background(), filesHost(t), plain, []string{"a.log"})
	if err != nil || len(got) != 0 {
		t.Fatalf("%q %v", got, err)
	}

	// a parent folder of repositories: each asks its own git, the rest is not ignored
	parent := t.TempDir()
	writeTree(t, parent, map[string]string{"app/.gitignore": "*.log\ndist/\n", "app/dist/x": "x", "app/src/k.go": "k", "notes/x.log": "x", "web/.gitignore": "out/\n", "web/out/y": "y"})
	gitInit(t, filepath.Join(parent, "app"))
	gitInit(t, filepath.Join(parent, "web"))
	got, err = Ignored(context.Background(), filesHost(t), parent, []string{
		"top.log", "app", "app/a.log", "app/b.go", "app/dist", "app/src/c.log", "app/src/k.go", "notes/x.log", "web/out", "web/in", "gone/x.log", "app/.git/x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "app/a.log app/dist app/src/c.log web/out" {
		t.Fatalf("parent: %q", got)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		g, p string
		want bool
	}{
		{"*.rs", "src/a.rs", true},
		{"*.rs", "src/a.go", false},
		{"*.{js,ts}", "x/y.ts", true},
		{"*.{js,ts}", "x/y.tsx", false},
		{"src/**/*.php", "src/a/b/c.php", true},
		{"src/**/*.php", "src/c.php", true},
		{"src/**/*.php", "lib/c.php", false},
		{"src/*.php", "src/a/c.php", false},
		{"a?c.txt", "abc.txt", true},
	}
	for _, c := range cases {
		if got := MatchGlob(c.g, c.p); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v", c.g, c.p, got)
		}
	}
}

func TestRunOnSave(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a b.txt": "x"})
	rules := []OnSaveRule{
		{Pattern: "*.go", Command: "echo never"},
		{Pattern: "*.txt", Command: "printf '%s' {file} > seen"},
		{Pattern: "*.txt", Command: "echo bad >&2; exit 3"},
		{Pattern: "*.txt", Command: "echo after"},
	}
	runs, err := RunOnSave(context.Background(), filesHost(t), root, "a b.txt", rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[1].ExitCode != 3 || !strings.Contains(runs[1].Output, "bad") {
		t.Fatalf("%+v", runs)
	}
	seen, _ := os.ReadFile(filepath.Join(root, "seen"))
	if string(seen) != filepath.Join(root, "a b.txt") {
		t.Fatalf("%q", seen)
	}
	if _, err := RunOnSave(context.Background(), filesHost(t), root, "../x", rules); err == nil {
		t.Fatal("path outside the project accepted")
	}
}

func TestLSPFraming(t *testing.T) {
	var b bytes.Buffer
	if err := WriteLSPFrame(&b, []byte(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	b.WriteString("Content-Type: x\r\nContent-Length: 2\r\n\r\n{}")
	r := bufio.NewReader(&b)
	for _, want := range []string{`{"id":1}`, `{}`} {
		got, err := ReadLSPFrame(r)
		if err != nil || string(got) != want {
			t.Fatalf("%q %v", got, err)
		}
	}
	if _, err := ReadLSPFrame(bufio.NewReader(strings.NewReader("Content-Length: x\r\n\r\n"))); err == nil {
		t.Fatal("bad length accepted")
	}
	if _, err := ReadLSPFrame(bufio.NewReader(strings.NewReader("\r\n"))); err == nil {
		t.Fatal("missing length accepted")
	}
}

func TestPipeBridgesLangServerStyleProcess(t *testing.T) {
	root := t.TempDir()
	h := filesHost(t)
	// A tiny echo server: one framed message in, the same message out.
	script := `head -c 22 >/dev/null; printf 'Content-Length: 2\r\n\r\n{}'; cat >/dev/null`
	p, err := machine.StartPipe(h, machine.ExecSpec{Cwd: root, Script: script})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	if err := WriteLSPFrame(p.Stdin, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	body, err := ReadLSPFrame(bufio.NewReader(p.Stdout))
	if err != nil || string(body) != "{}" {
		t.Fatalf("%q %v", body, err)
	}
	p.Stop()
	<-p.Done()
}

func TestLangServerStatusFindsVuePlugin(t *testing.T) {
	root := t.TempDir()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(home, "lib", "@vue", "language-server")
	entry := filepath.Join(pkg, "bin", "vue-language-server.js")
	bin := filepath.Join(home, ".local", "bin")
	for _, d := range []string{filepath.Dir(entry), bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(entry, filepath.Join(bin, "vue-language-server")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	h := machine.NewLocal(machine.Config{})
	plugin := func() string {
		t.Helper()
		rows, err := LangServerStatus(context.Background(), h, root)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == "vue" {
				if !r.Available {
					t.Fatalf("%+v", r)
				}
				return r.Plugin
			}
		}
		t.Fatal("no vue row")
		return ""
	}
	version := func(v string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@vue/language-server","version": "`+v+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	version("3.3.14")
	if got := plugin(); got != "" {
		t.Fatalf("no plugin installed, got %q", got)
	}
	if err := os.MkdirAll(filepath.Join(pkg, "node_modules", "@vue", "typescript-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := plugin(); got != pkg {
		t.Fatalf("got %q, want %q", got, pkg)
	}
	// version 2 reads the script itself
	version("2.2.10")
	if got := plugin(); got != "" {
		t.Fatalf("version 2 got %q", got)
	}
}

func TestLangServerStatusNeedsTsserver(t *testing.T) {
	root := t.TempDir()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	modules := filepath.Join(home, "lib", "node_modules")
	entry := filepath.Join(modules, "typescript-language-server", "lib", "cli.mjs")
	bin := filepath.Join(home, ".local", "bin")
	for _, d := range []string{filepath.Dir(entry), bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(entry, filepath.Join(bin, "typescript-language-server")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	h := machine.NewLocal(machine.Config{})
	available := func() bool {
		t.Helper()
		rows, err := LangServerStatus(context.Background(), h, root)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == "typescript" {
				return r.Available
			}
		}
		t.Fatal("no typescript row")
		return false
	}
	tsserver := func(dir string) {
		t.Helper()
		lib := filepath.Join(dir, "typescript", "lib")
		if err := os.MkdirAll(lib, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(lib, "tsserver.js"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if available() {
		t.Fatal("no TypeScript anywhere, yet available")
	}
	// TypeScript 7 next to it: a package without tsserver
	if err := os.MkdirAll(filepath.Join(modules, "typescript", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if available() {
		t.Fatal("TypeScript without tsserver, yet available")
	}
	tsserver(filepath.Join(root, "node_modules"))
	if !available() {
		t.Fatal("the project's own TypeScript was not seen")
	}
	if err := os.RemoveAll(filepath.Join(root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	tsserver(modules)
	if !available() {
		t.Fatal("the TypeScript next to the server was not seen")
	}
}

// gopls runs go for every package it loads. A server over SSH often has it only
// in the PATH of an interactive shell, so the hub has to find it on its own, and
// without it the row says what to install instead of gopls failing on each file.
func TestGoplsNeedsGo(t *testing.T) {
	if _, err := os.Stat("/usr/local/go/bin/go"); err == nil {
		t.Skip("this machine has go in a folder the hub always adds")
	}
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	executable := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A login shell whose PATH has no go, whatever the machine running the test has.
	shell := filepath.Join(t.TempDir(), "shell")
	executable(shell, "printf '__NB_PATH__%s' "+machine.Quote(t.TempDir())+"\n")
	t.Setenv("SHELL", shell)
	// This gopls prints the go it would run.
	executable(filepath.Join(home, "go", "bin", "gopls"), "command -v go\n")

	ctx := context.Background()
	h := machine.NewLocal(machine.Config{})
	row := func() LSPStatus {
		t.Helper()
		rows, err := LangServerStatus(ctx, h, root)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == "go" {
				return r
			}
		}
		t.Fatal("no go row")
		return LSPStatus{}
	}

	if r := row(); r.Available || r.Install != goToolchainInstall {
		t.Fatalf("no go on the machine: %+v", r)
	}
	if _, err := ResolveLangServer(ctx, h, root, "go"); !errors.Is(err, ErrNoLangServer) {
		t.Fatalf("started without go: %v", err)
	}

	tarball := filepath.Join(home, ".local", "go", "bin", "go")
	executable(tarball, "")
	if r := row(); !r.Available || r.Install == goToolchainInstall {
		t.Fatalf("go in a tarball folder: %+v", r)
	}
	script, err := ResolveLangServer(ctx, h, root, "go")
	if err != nil {
		t.Fatal(err)
	}
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Cwd: root, Script: script, Timeout: lspProbeTimeout, MaxOutput: 1 << 12})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != tarball {
		t.Fatalf("gopls would run %q, want %q", got, tarball)
	}
}

func TestLangServerStatusFindsPath(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	bin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	h := machine.NewLocal(machine.Config{})
	// Written after the host exists: only the script's own $HOME lookup finds it.
	for _, name := range []string{"gopls", "go"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := LangServerStatus(context.Background(), h, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == "go" {
			if !r.Available || r.Command != "gopls" {
				t.Fatalf("%+v", r)
			}
			script, err := ResolveLangServer(context.Background(), h, root, "go")
			if err != nil || !strings.HasSuffix(script, "exec gopls") {
				t.Fatalf("%q %v", script, err)
			}
			if _, err := ResolveLangServer(context.Background(), h, root, "nope"); err == nil {
				t.Fatal("unknown server resolved")
			}
			return
		}
	}
	t.Fatal("no go row")
}
