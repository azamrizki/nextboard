package review

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// A local folder plays the server: machine.Exec runs the same scripts through the same code, and
// internal/server has the test over a real sshd.
type serverFixture struct {
	*fixture
	mirror *Mirror
	online bool
}

func newServerFixture(t *testing.T) *serverFixture {
	t.Helper()
	f := newFixture(t)
	s := &serverFixture{fixture: f, online: true}
	local := machine.NewLocal(machine.Config{})
	s.mirror = &Mirror{Remote: f.root, Dir: filepath.Join(t.TempDir(), "mirror"), Host: func() (machine.Host, bool) { return local, s.online }}
	f.m.cfg.Roots = func(string) []Root { return []Root{{Path: s.mirror.Dir, Mirror: s.mirror, Origin: f.root}} }
	return s
}

func (s *serverFixture) inMirror(rel string) bool {
	_, err := os.Lstat(filepath.Join(s.mirror.Dir, rel))
	return err == nil
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestServerProjectIsReviewed(t *testing.T) {
	s := newServerFixture(t)
	s.write("keep.txt", "old\n")
	s.write("gone.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(s.root, "gone.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.start("a", "@a", false)
	s.write("keep.txt", "new\n")
	s.write("added.txt", "fresh\n")
	if err := os.Remove(filepath.Join(s.root, "gone.sh")); err != nil {
		t.Fatal(err)
	}
	s.snap()
	got := s.files("a")
	if got["keep.txt"].Status != "modified" || got["added.txt"].Status != "added" || got["gone.sh"].Status != "deleted" {
		t.Fatalf("changes: %+v", got)
	}
	if !s.inMirror("added.txt") || s.inMirror("gone.sh") {
		t.Fatal("the copy does not follow the server")
	}
	v, err := s.m.File("p", "a", "keep.txt")
	if err != nil || v.Base != "old\n" || v.Current != "new\n" {
		t.Fatalf("file: %+v %v", v, err)
	}
	// Undo writes on the server, and the copy follows.
	if err := s.m.Reject("p", "a", "", nil); err != nil {
		t.Fatal(err)
	}
	if s.read("keep.txt") != "old\n" {
		t.Fatalf("keep.txt = %q", s.read("keep.txt"))
	}
	if _, err := os.Stat(filepath.Join(s.root, "added.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("added.txt stayed: %v", err)
	}
	st, err := os.Stat(filepath.Join(s.root, "gone.sh"))
	if err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("gone.sh came back as %v, %v", st, err)
	}
	if len(s.files("a")) != 0 {
		t.Fatalf("still pending: %+v", s.files("a"))
	}
	s.stop("a")
}

func TestServerProjectPartialUndo(t *testing.T) {
	s := newServerFixture(t)
	s.write("f.txt", "one\ntwo\n")
	s.start("a", "@a", false)
	s.write("f.txt", "ONE\nTWO\n")
	s.snap()
	text := "one\nTWO\n"
	if err := s.m.Reject("p", "a", "f.txt", &text); err != nil {
		t.Fatal(err)
	}
	if s.read("f.txt") != text {
		t.Fatalf("server has %q", s.read("f.txt"))
	}
	s.stop("a")
}

// The hub reports a tool's file by its name on the server.
func TestServerHintDecidesWhoseChange(t *testing.T) {
	s := newServerFixture(t)
	s.write("f.txt", "one\n")
	s.start("a", "@a", false)
	s.start("b", "@b", false)
	s.write("f.txt", "two\n")
	s.m.Hint("p", "b", filepath.Join(s.root, "f.txt"))
	s.snap()
	if len(s.files("a")) != 0 || len(s.files("b")) != 1 || s.files("b")["f.txt"].Unsure {
		t.Fatalf("a %+v, b %+v", s.files("a"), s.files("b"))
	}
	s.stop("a")
	s.stop("b")
}

func TestServerEditorSaveIsNoAgentsChange(t *testing.T) {
	s := newServerFixture(t)
	s.write("f.txt", "one\n")
	s.start("a", "@a", false)
	s.write("f.txt", "two\n")
	s.m.UserWrote(filepath.Join(s.root, "f.txt"))
	s.snap()
	if len(s.files("a")) != 0 {
		t.Fatalf("the owner's save went to the agent: %+v", s.files("a"))
	}
	s.stop("a")
}

func TestServerLocalHistory(t *testing.T) {
	s := newServerFixture(t)
	s.write("f.txt", "first\n")
	s.snap()
	time.Sleep(10 * time.Millisecond)
	s.write("f.txt", "second\n")
	items, err := s.m.History("p", "f.txt", 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("history: %+v %v", items, err)
	}
	text, binary, err := s.m.Version("p", "f.txt", items[1].Commit)
	if err != nil || binary || text != "first\n" {
		t.Fatalf("version: %q %v %v", text, binary, err)
	}
}

// What a server keeps as credentials must not land where every agent on This PC can read.
func TestServerSecretsStayOnTheServer(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		name := "plain folder"
		if tracked {
			name = "tracked in git"
		}
		t.Run(name, func(t *testing.T) {
			s := newServerFixture(t)
			canary := "CANARY-do-not-copy"
			secrets := []string{".env", ".env.production", "deploy/server.pem", "config/tls.key", "auth.json", "keys/id_ed25519", ".git-credentials", "wp-config.php"}
			for _, rel := range secrets {
				s.write(rel, canary+"\n")
			}
			s.write(".env.example", "APP_KEY=\n")
			s.write("app.php", "<?php\n")
			if tracked {
				gitIn(t, s.root, "init", "-q")
				gitIn(t, s.root, "add", "-Af")
				gitIn(t, s.root, "commit", "-qm", "all of it")
			}
			s.snap()
			if !s.inMirror("app.php") || !s.inMirror(".env.example") {
				t.Fatal("normal files did not come over")
			}
			var leaked []string
			_ = filepath.WalkDir(s.mirror.Dir, func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(canary)) {
						leaked = append(leaked, p)
					}
				}
				return nil
			})
			if len(leaked) > 0 {
				t.Fatalf("copied: %v", leaked)
			}
			// nor did the shadow repository store them
			if _, err := s.m.History("p", ".env", 0); err != nil {
				t.Fatal(err)
			}
			if items, _ := s.m.History("p", ".env", 0); len(items) != 0 {
				t.Fatalf(".env has a history: %+v", items)
			}
		})
	}
}

func TestServerFollowsGitignoreAndSkipsHeavyFolders(t *testing.T) {
	s := newServerFixture(t)
	s.write(".gitignore", "storage/logs/\n")
	s.write("storage/logs/laravel.log", "line\n")
	s.write("app/a.php", "<?php\n")
	gitIn(t, s.root, "init", "-q")
	s.write("untracked.php", "<?php\n")
	s.snap()
	if s.inMirror("storage/logs/laravel.log") {
		t.Fatal("an ignored file came over")
	}
	if !s.inMirror("untracked.php") || !s.inMirror("app/a.php") || !s.inMirror(".gitignore") {
		t.Fatal("work is missing from the copy")
	}

	p := newServerFixture(t)
	p.write("vendor/x/big.php", "<?php\n")
	p.write("node_modules/y/i.js", "1\n")
	p.write("src/a.go", "package a\n")
	p.snap()
	if p.inMirror("vendor/x/big.php") || p.inMirror("node_modules/y/i.js") || !p.inMirror("src/a.go") {
		t.Fatal("a folder without git copied the wrong things")
	}
}

func TestServerLinksDoNotComeOver(t *testing.T) {
	s := newServerFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.write("a.txt", "a\n")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(s.root, "file-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.root, "dir-link")); err != nil {
		t.Fatal(err)
	}
	s.snap()
	if s.inMirror("file-link") || s.inMirror("dir-link") || !s.inMirror("a.txt") {
		entries, _ := os.ReadDir(s.mirror.Dir)
		t.Fatalf("mirror holds %v", entries)
	}
}

// An agent swaps a folder for a link out of the project. Undoing its change must not write there.
func TestServerUndoDoesNotWriteThroughALink(t *testing.T) {
	s := newServerFixture(t)
	outside := t.TempDir()
	s.write("d/a.txt", "base\n")
	s.start("a", "@a", false)
	if err := os.Rename(filepath.Join(s.root, "d"), filepath.Join(s.root, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.root, "d")); err != nil {
		t.Fatal(err)
	}
	s.snap()
	if err := s.m.Reject("p", "a", "d/a.txt", nil); !errors.Is(err, errBadPath) {
		t.Fatalf("reject: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "a.txt")); err == nil {
		t.Fatal("wrote through the link")
	}
	s.stop("a")
}

// A server that was taken over can send any archive it likes.
func TestMirrorRefusesWhatWasNotAskedFor(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	m := &Mirror{Dir: filepath.Join(dir, "mirror"), index: map[string]mirrorEntry{}, loaded: true}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// a link already in the copy, however it came there
	if err := os.Symlink(outside, filepath.Join(m.Dir, "linked")); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	add := func(name string, typ byte, link string, body string) {
		t.Helper()
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: typ, Linkname: link, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("./ok.txt", tar.TypeReg, "", "fine\n")
	add("./not-asked.txt", tar.TypeReg, "", "x")
	add("../escape.txt", tar.TypeReg, "", "x")
	add("/tmp/abs-escape.txt", tar.TypeReg, "", "x")
	add("./.git/config", tar.TypeReg, "", "x")
	add("./.env", tar.TypeReg, "", "x")
	add("./evil-link", tar.TypeSymlink, outside, "")
	add("./hard-link", tar.TypeLink, "/etc/passwd", "")
	add("./linked/through.txt", tar.TypeReg, "", "landed\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"ok.txt": true, "../escape.txt": true, "/tmp/abs-escape.txt": true, ".git/config": true, ".env": true, "evil-link": true, "hard-link": true, "linked/through.txt": true}
	got, err := m.extract(&b, want, map[string]mirrorEntry{"ok.txt": {Mode: "644"}, "linked/through.txt": {Mode: "644"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("took %d", got)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote out of the mirror: %v", entries)
	}
	if st, err := os.Lstat(filepath.Join(m.Dir, "linked")); err != nil || !st.IsDir() {
		t.Fatalf("the link was not replaced: %v %v", st, err)
	}
	for _, name := range []string{"evil-link", "hard-link", ".env", ".git", "not-asked.txt"} {
		if _, err := os.Lstat(filepath.Join(m.Dir, name)); err == nil {
			t.Fatalf("%s is in the mirror", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("../ got out")
	}
}

func TestMirrorListing(t *testing.T) {
	if _, _, err := parseMirrorList("1000\nR \n5 3 644 regular file|a.txt\n"); err == nil {
		t.Fatal("a listing cut off was believed")
	}
	out := "1000\nR \n" +
		"5 3 644 regular file|a.txt\n" +
		"5 0 755 regular empty file|./run.sh\n" +
		"5 3 777 symbolic link|link\n" +
		"5 3 755 directory|sub\n" +
		"5 3 644 regular file|../up.txt\n" +
		"5 3 644 regular file|/etc/passwd\n" +
		"5 3 644 regular file|a/../../b.txt\n" +
		"5 3 644 regular file|.git/hooks/pre-commit\n" +
		"5 3 644 regular file|.env\n" +
		"5 3 644 regular file|x|y.txt\n" +
		"tail of a name with a newline\n" +
		"R sub\n5 3 644 regular file|in.txt\n" +
		listMarkerEnd + "\n"
	now, files, err := parseMirrorList(out)
	if err != nil || now != 1000 {
		t.Fatal(now, err)
	}
	for _, want := range []string{"a.txt", "run.sh", "x|y.txt", "sub/in.txt"} {
		if _, ok := files[want]; !ok {
			t.Fatalf("missing %s in %v", want, files)
		}
	}
	if len(files) != 4 {
		t.Fatalf("kept too much: %v", files)
	}
}

// Without a connection nothing is decided: no file looks deleted, and what is pending stays.
func TestServerOffline(t *testing.T) {
	s := newServerFixture(t)
	s.write("f.txt", "one\n")
	s.start("a", "@a", false)
	s.write("f.txt", "two\n")
	s.snap()
	s.online = false
	if err := s.m.Snapshot("p"); !errors.Is(err, errMirrorOffline) {
		t.Fatalf("snapshot: %v", err)
	}
	if len(s.files("a")) != 1 || !s.inMirror("f.txt") {
		t.Fatalf("state changed: %+v", s.files("a"))
	}
	s.online = true
	s.stop("a")
}

func TestServerHomeFolderTakesNoPart(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{home, "/"} {
		local := machine.NewLocal(machine.Config{})
		m := &Mirror{Remote: root, Dir: filepath.Join(t.TempDir(), "mirror"), Host: func() (machine.Host, bool) { return local, true }}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := m.Pull(ctx)
		cancel()
		if !errors.Is(err, errMirrorHome) {
			t.Fatalf("%s: %v", root, err)
		}
		if _, err := os.Stat(m.Dir); err == nil {
			t.Fatalf("%s: something was copied", root)
		}
	}
}

func TestServerTooBig(t *testing.T) {
	s := newServerFixture(t)
	for i := 0; i < int(mirrorMaxBytes/MaxFile)+2; i++ {
		f, err := os.Create(filepath.Join(s.root, "part"+strings.Repeat("x", i%3)+"-"+string(rune('a'+i%26))+string(rune('a'+i/26))))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(MaxFile); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if err := s.m.Snapshot("p"); !errors.Is(err, errMirrorTooBig) {
		t.Fatalf("snapshot: %v", err)
	}
	if entries, _ := os.ReadDir(s.mirror.Dir); len(entries) != 0 {
		t.Fatalf("copied before knowing: %v", entries)
	}
}

func TestServerNestedRepositories(t *testing.T) {
	s := newServerFixture(t)
	s.write("top.txt", "t\n")
	s.write("api/a.txt", "a\n")
	gitIn(t, filepath.Join(s.root, "api"), "init", "-q")
	s.mirror.SetNested([]string{"api"})
	s.m.cfg.Roots = func(string) []Root {
		return []Root{{Path: s.mirror.Dir, Mirror: s.mirror, Origin: s.root}, {Rel: "api", Path: filepath.Join(s.mirror.Dir, "api"), Mirror: s.mirror, Origin: filepath.Join(s.root, "api")}}
	}
	s.start("x", "@x", false)
	s.write("api/a.txt", "b\n")
	s.write("top.txt", "u\n")
	s.snap()
	got := s.files("x")
	if got["api/a.txt"].Status != "modified" || got["top.txt"].Status != "modified" || len(got) != 2 {
		t.Fatalf("changes: %+v", got)
	}
	if err := s.m.Reject("p", "x", "api/a.txt", nil); err != nil {
		t.Fatal(err)
	}
	if s.read("api/a.txt") != "a\n" {
		t.Fatalf("api/a.txt = %q", s.read("api/a.txt"))
	}
	s.stop("x")
}
