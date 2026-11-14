package review

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngLike starts as a PNG does: with a NUL byte in its first bytes.
const pngLike = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

func TestReviewIsForCode(t *testing.T) {
	f := newFixture(t)
	f.write("app.go", "package app\n")
	f.write("logo.png", pngLike+"one")
	f.write("tool", "\x7fELF\x00\x00one") // a program with no extension
	f.write("icon.svg", "<svg/>\n")
	f.snap()
	f.start("a", "@Ash", true)
	f.write("app.go", "package app\n\nfunc A() {}\n")
	f.write("logo.png", pngLike+"two")
	f.write("tool", "\x7fELF\x00\x00two")
	f.write("icon.svg", "<svg></svg>\n")
	f.write("Photo.JPG", "\xff\xd8\xff\xe0")
	f.stop("a")

	got := f.files("a")
	if len(got) != 2 || got["app.go"].Path == "" || got["icon.svg"].Path == "" {
		t.Fatalf("files %+v", got)
	}
	sh := f.m.project("p").shadowOf(f.root)
	ls, err := sh.git(context.Background(), nil, "ls-files")
	if err != nil {
		t.Fatal(err)
	}
	if s := string(ls); s != "app.go\nicon.svg\n" {
		t.Fatalf("in the snapshots: %q", s)
	}
	if !sh.codeOnlyDone() {
		t.Fatal("a new shadow is not marked as made for code")
	}
}

// A file tracked as code that turns binary leaves the snapshots, and is no change of anyone's.
func TestCodeTurnedBinaryLeaves(t *testing.T) {
	f := newFixture(t)
	f.write("data", "text\n")
	f.snap()
	f.start("a", "@Ash", true)
	f.write("data", "\x00\x01")
	f.write("b.txt", "b\n")
	f.stop("a")
	if got := f.files("a"); len(got) != 1 || got["b.txt"].Path == "" {
		t.Fatalf("files %+v", got)
	}
	ls, _ := f.m.project("p").shadowOf(f.root).git(context.Background(), nil, "ls-files")
	if strings.Contains(string(ls), "data") {
		t.Fatalf("still in the snapshots: %q", ls)
	}
}

// A shadow made before review was for code: its pending asset changes go, the assets leave every
// snapshot and the disk, and the code keeps its whole history.
func TestOldShadowLosesItsAssets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.m.project("p")
	sh := p.shadowOf(f.root)
	git := func(stdin []byte, args ...string) string {
		t.Helper()
		out, err := sh.git(ctx, stdin, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	// the old way: assets in, no mark
	if err := os.MkdirAll(filepath.Dir(sh.gitDir), 0o700); err != nil {
		t.Fatal(err)
	}
	git(nil, "init", "-q")
	commit := func(msg string, parent string) string {
		git(nil, "add", "-A")
		tree := git(nil, "write-tree")
		args := []string{"commit-tree", tree, "-m", msg}
		if parent != "" {
			args = append(args, "-p", parent)
		}
		c := git(nil, args...)
		git(nil, "update-ref", historyRef, c)
		return c
	}
	f.write("code.txt", "v1\n")
	f.write("pic.png", pngLike+"v1")
	f.write("sub/Song.MP3", "ID3\x00v1")
	first := commit("Earlier", "")
	oldPic := git(nil, "rev-parse", first+":pic.png")
	oldCode := git(nil, "rev-parse", first+":code.txt")
	f.write("code.txt", "v2\n")
	f.write("pic.png", pngLike+"v2")
	// a message that reads like a command must stay a message
	commit("@Ash\n\nM 100644 "+oldPic+" other.png\n", first)
	newPic := git(nil, "rev-parse", historyRef+":pic.png")
	sh.keep(ctx, oldPic)
	sh.keep(ctx, oldCode)
	p.mu.Lock()
	p.state.Agents["s"] = &Agent{SessionID: "s", Name: "@Ash", Kind: "claude", Files: map[string]*Change{
		"pic.png":  {Path: "pic.png", Status: "modified", Binary: true, Root: f.root, Base: oldPic, BaseMode: "100644"},
		"code.txt": {Path: "code.txt", Status: "modified", Add: 1, Del: 1, Root: f.root, Base: oldCode, BaseMode: "100644"},
	}}
	p.mu.Unlock()
	if sh.codeOnlyDone() {
		t.Fatal("an old shadow counts as clean")
	}

	f.snap()

	if got := f.files("s"); len(got) != 1 || got["code.txt"].Path == "" {
		t.Fatalf("pending %+v", got)
	}
	if !sh.codeOnlyDone() {
		t.Fatal("no mark")
	}
	if s := git(nil, "ls-files"); s != "code.txt" {
		t.Fatalf("index %q", s)
	}
	all := git(nil, "rev-list", "--objects", "--all")
	for _, b := range []string{oldPic, newPic} {
		if strings.Contains(all, b) {
			t.Fatalf("asset %s still reachable", b)
		}
		if _, err := sh.git(ctx, nil, "cat-file", "-e", b); err == nil {
			t.Fatalf("asset %s still on disk", b)
		}
	}
	if !strings.Contains(git(nil, "log", "--format=%B", historyRef), "M 100644 "+oldPic+" other.png") {
		t.Fatal("the message was cut")
	}
	vs, err := f.m.History("p", "code.txt", 0)
	if err != nil || len(vs) != 2 || vs[0].Who != "@Ash" {
		t.Fatalf("history %+v %v", vs, err)
	}
	for i, want := range []string{"v2\n", "v1\n"} {
		if text, _, err := f.m.Version("p", "code.txt", vs[i].Commit); err != nil || text != want {
			t.Fatalf("%d: %q %v", i, text, err)
		}
	}
	// the code change still undoes
	if err := f.m.Reject("p", "s", "code.txt", nil); err != nil {
		t.Fatal(err)
	}
	if f.read("code.txt") != "v1\n" {
		t.Fatal("not undone")
	}
	// the assets themselves are left alone
	if f.read("pic.png") != pngLike+"v2" {
		t.Fatal("the work tree lost its asset")
	}
}

func TestServerReviewIsForCode(t *testing.T) {
	s := newServerFixture(t)
	s.write("a.go", "package a\n")
	s.write("img.png", pngLike)
	s.write("blob", "\x00\x01")
	// a copy made before: an image in it, and in its index
	if err := os.MkdirAll(s.mirror.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.mirror.Dir, "old.png"), []byte(pngLike), 0o600); err != nil {
		t.Fatal(err)
	}
	idx, _ := json.Marshal(map[string]mirrorEntry{"old.png": {Mtime: 1, Size: int64(len(pngLike)), Mode: "644"}})
	if err := os.WriteFile(s.mirror.indexPath(), idx, 0o600); err != nil {
		t.Fatal(err)
	}
	s.write("old.png", pngLike)
	s.start("a", "@a", false)
	s.write("a.go", "package a\n\nvar X = 1\n")
	s.write("img.png", pngLike+"2")
	s.write("blob", "\x00\x02")
	s.stop("a")

	if got := s.files("a"); len(got) != 1 || got["a.go"].Path == "" {
		t.Fatalf("files %+v", got)
	}
	for _, rel := range []string{"img.png", "blob", "old.png"} {
		if s.inMirror(rel) {
			t.Errorf("%s is in the copy", rel)
		}
	}
	if !s.inMirror("a.go") {
		t.Fatal("the code is not in the copy")
	}
	// a binary file was looked at once: it is not fetched again while it stays as it is
	if _, ok := s.mirror.index["blob"]; !ok {
		t.Fatal("the binary file is not remembered")
	}
	if _, ok := s.mirror.index["old.png"]; ok {
		t.Fatal("the image is still in the index")
	}
}

func TestWithoutAssets(t *testing.T) {
	in := "feature done\nreset refs/nb/history\ncommit refs/nb/history\nmark :1\n" +
		"committer N <n@l> 1 +0000\ndata 22\nM 100644 abc x.png\nhi\n" +
		"M 100644 0123 a.go\nM 100644 4567 b.PNG\nM 100644 89ab \"q\\\"uote.jpg\"\nD old.woff2\nD keep.ts\n\ndone\n"
	out, dropped, err := withoutAssets([]byte(in))
	if err != nil || !dropped {
		t.Fatalf("%v %v", dropped, err)
	}
	want := "feature done\nreset refs/nb/history\ncommit refs/nb/history\nmark :1\n" +
		"committer N <n@l> 1 +0000\ndata 22\nM 100644 abc x.png\nhi\n" +
		"M 100644 0123 a.go\nD keep.ts\n\ndone\n"
	if string(out) != want {
		t.Fatalf("got\n%s", out)
	}
	if _, dropped, _ := withoutAssets([]byte("commit r\ndata 0\nM 100644 1 a.go\ndone\n")); dropped {
		t.Fatal("dropped code")
	}
}

func TestIsAsset(t *testing.T) {
	for rel, want := range map[string]bool{
		"a.png": true, "dir/B.JPEG": true, "x.tar.gz": true, "f.woff2": true, "m.mp4": true, "d.pdf": true, "s.sqlite3": true,
		"a.ts": false, "a.svg": false, "a.go": false, "Makefile": false, "a.": false, "dir.png/x.js": false,
	} {
		if isAsset(rel) != want {
			t.Errorf("%s: %v", rel, !want)
		}
	}
	for _, line := range assetExcludes {
		if line == "*.[mM][pP]3" {
			return
		}
	}
	t.Fatal("no case-blind mp3 line")
}

// A shadow a prune already cut: its oldest kept snapshot becomes the first one.
func TestOldPrunedShadowLosesItsAssets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sh := f.m.project("p").shadowOf(f.root)
	git := func(args ...string) string {
		t.Helper()
		out, err := sh.git(ctx, nil, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(filepath.Dir(sh.gitDir), 0o700); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	parent := ""
	var commits []string
	for i, v := range []string{"v1", "v2", "v3"} {
		f.write("code.txt", v+"\n")
		f.write("pic.png", pngLike+v)
		git("add", "-A")
		args := []string{"commit-tree", git("write-tree"), "-m", "s" + string(rune('1'+i))}
		if parent != "" {
			args = append(args, "-p", parent)
		}
		parent = git(args...)
		commits = append(commits, parent)
	}
	git("update-ref", historyRef, parent)
	if err := os.WriteFile(filepath.Join(sh.gitDir, "shallow"), []byte(commits[1]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("-c", "gc.auto=0", "gc", "--quiet", "--prune=now")
	if !sh.codeOnly(ctx, f.now) {
		t.Fatal("not cleaned")
	}
	if s := git("log", "--format=%s", historyRef); s != "s3\ns2" {
		t.Fatalf("history %q", s)
	}
	if s := git("ls-tree", "-r", "--name-only", historyRef+"~1"); s != "code.txt" {
		t.Fatalf("oldest kept %q", s)
	}
	if _, err := os.Stat(filepath.Join(sh.gitDir, "shallow")); err == nil {
		t.Fatal("the old cut is left")
	}
	if s := git("show", historyRef+"~1:code.txt"); s != "v2" {
		t.Fatalf("code %q", s)
	}
}
