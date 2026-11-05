package gitsvc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRepo creates a repository with one commit at dir/rel.
func (f *fixture) newRepo(rel string) string {
	f.t.Helper()
	d := filepath.Join(f.dir, rel)
	if err := os.MkdirAll(d, 0o755); err != nil {
		f.t.Fatal(err)
	}
	git(f.t, d, "init", "-q", "-b", "main")
	write(f.t, d, "README.md", "hello\n")
	git(f.t, d, "add", "-A")
	git(f.t, d, "commit", "-qm", "init")
	return d
}

func paths(l *RepoList) string {
	var out []string
	for _, it := range l.Items {
		out = append(out, it.Path)
	}
	return strings.Join(out, ",")
}

// TestReposScan: repositories at any level down to scanDepth are found with
// their summary; dependency folders, stray .git folders, symlinks and deeper
// levels are not. The project folder itself stays no repository.
func TestReposScan(t *testing.T) {
	f := newFixture(t, true)
	a := f.newRepo("app-a")
	f.newRepo("group/app-b")
	f.newRepo("one/two/three/four")
	f.newRepo("one/two/three/four/five") // inside a repo, but too deep
	f.newRepo("node_modules/dep")
	f.newRepo("group/vendor/dep")
	write(f.t, f.dir, "stray/.git/keep", "")
	outside := t.TempDir()
	git(t, outside, "init", "-q", "-b", "main")
	if err := os.Symlink(outside, filepath.Join(f.dir, "linked")); err != nil {
		t.Fatal(err)
	}
	write(t, a, "new.txt", "x\n")
	write(t, a, "dir/new2.txt", "y\n")

	l, err := f.svc.Repos(ctx(t), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(l); got != "app-a,group/app-b,one/two/three/four" || l.Truncated {
		t.Fatalf("repos %q truncated=%v", got, l.Truncated)
	}
	it := l.Items[0]
	if it.Name != "app-a" || it.Branch != "main" || it.Changed != 2 || it.Error != "" {
		t.Fatalf("summary %+v", it)
	}
	if real, _ := filepath.EvalSymlinks(a); it.Root != real {
		t.Fatalf("root %q want %q", it.Root, real)
	}
	if l.Items[1].Changed != 0 {
		t.Fatalf("clean repo %+v", l.Items[1])
	}
	// A scan does not create or change anything in the project folder.
	if got := git(t, a, "status", "--porcelain", "-b"); !strings.Contains(got, "new.txt") || strings.Contains(got, "M ") {
		t.Fatalf("status after scan: %s", got)
	}
	if st, err := f.svc.Status(ctx(t), f.repo); err != nil || st.IsRepo {
		t.Fatalf("root %+v %v", st, err)
	}
	f.mu.Lock()
	n := len(f.events)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("a scan published %d events", n)
	}
}

// TestReposOwnAndUpward: the project's own repository comes first with path
// "", also when the project folder is below its root.
func TestReposOwnAndUpward(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	sub := filepath.Join(f.dir, "src")
	inner := filepath.Join(sub, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, inner, "init", "-q", "-b", "dev")
	if l, err := f.svc.Repos(ctx(t), f.repo); err != nil || paths(l) != ",src/inner" || l.Items[0].Branch != "main" || l.Items[1].Branch != "dev" {
		t.Fatalf("own %+v %v", l, err)
	}
	r := f.repo
	r.Path = sub
	if l, err := f.svc.Repos(ctx(t), r); err != nil || paths(l) != ",inner" || l.Items[0].Name != filepath.Base(f.dir) {
		t.Fatalf("upward %+v %v", l, err)
	}
}

// TestReposCap: the scan stops at maxRepos and says so.
func TestReposCap(t *testing.T) {
	f := newFixture(t, true)
	for i := 0; i < maxRepos+3; i++ {
		git(t, f.dir, "init", "-q", "-b", "main", fmt.Sprintf("r%02d", i))
	}
	l, err := f.svc.scan(ctx(t), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != maxRepos || !l.Truncated {
		t.Fatalf("items %d truncated=%v", len(l.Items), l.Truncated)
	}
}

// TestReposScanTimeout: a scan that runs out of time is no failure: it says
// the list may be incomplete.
func TestReposScanTimeout(t *testing.T) {
	f := newFixture(t, true)
	f.newRepo("app-a")
	old := ReadTimeout
	ReadTimeout = time.Nanosecond
	defer func() { ReadTimeout = old }()
	l, err := f.svc.scan(ctx(t), f.repo)
	if err != nil || !l.Truncated {
		t.Fatalf("list %+v err %v", l, err)
	}
	// A summary that cannot be read leaves the repository listed.
	it := RepoInfo{Path: "app-a", Name: "app-a"}
	f.svc.readSummary(ctx(t), f.repo, &it)
	if it.Error == "" {
		t.Fatalf("summary %+v", it)
	}
}

// TestSubRepoIsolation: ?repo= works on that repository only; lock, panel
// selection and events belong to it.
func TestSubRepoIsolation(t *testing.T) {
	f := newFixture(t, true)
	a, b := f.newRepo("app-a"), f.newRepo("group/app-b")
	write(t, a, "a.txt", "a\n")
	write(t, b, "b.txt", "b\n")
	ra, err := f.repo.In("app-a")
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := f.repo.In("group/app-b")
	if ra.Dir() != a {
		t.Fatalf("dir %q", ra.Dir())
	}

	st, err := f.svc.Status(ctx(t), ra)
	if err != nil || !st.IsRepo || len(st.Files) != 1 || st.Files[0].Path != "a.txt" {
		t.Fatalf("status %+v %v", st, err)
	}
	// A running operation in one repository leaves the other free.
	unlock, err := f.svc.lock(ra, "push")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Commit(ctx(t), ra, CommitRequest{Message: "x", All: true}); code(err) != "git_busy" {
		t.Fatalf("same repo: %v", err)
	}
	if _, err := f.svc.Commit(ctx(t), rb, CommitRequest{Message: "Add b", All: true}); err != nil {
		t.Fatalf("other repo: %v", err)
	}
	unlock()
	if git(t, b, "log", "-1", "--format=%s") != "Add b" || git(t, a, "log", "-1", "--format=%s") != "init" {
		t.Fatal("commit landed in the wrong repository")
	}
	if err := f.svc.SetSelection(ra, []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if len(f.svc.Selection(rb)) != 0 || len(f.svc.Selection(f.repo)) != 0 || len(f.svc.Selection(ra)) != 1 {
		t.Fatal("selection leaked between repositories")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	saw := false
	for _, e := range f.events {
		if e["type"] == "git" {
			t.Fatalf("a nested repository sent a project event: %v", e)
		}
		if e["type"] == "gitRepo" && e["repo"] == "group/app-b" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("no gitRepo event: %v", f.events)
	}
}

// TestSubRepoGuard: a selected path that is no repository root, or that
// leaves the project, never falls through to an outer repository.
func TestSubRepoGuard(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	// A plain subfolder of the outer repository.
	r, err := f.repo.In("src")
	if err != nil {
		t.Fatal(err)
	}
	if st, err := f.svc.Status(ctx(t), r); err != nil || st.IsRepo {
		t.Fatalf("plain folder %+v %v", st, err)
	}
	if _, err := f.svc.Commit(ctx(t), r, CommitRequest{Message: "x", All: true}); code(err) != "not_repo" {
		t.Fatalf("commit in plain folder: %v", err)
	}
	if _, err := f.svc.Init(ctx(t), r, ""); code(err) != "not_repo" {
		t.Fatalf("init in plain folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "src", ".git")); !os.IsNotExist(err) {
		t.Fatal("a repository appeared in the subfolder")
	}
	// Missing folder.
	r, _ = f.repo.In("nope")
	if st, err := f.svc.Status(ctx(t), r); err != nil || st.IsRepo {
		t.Fatalf("missing %+v %v", st, err)
	}
	// A symlink to a repository outside the project.
	outside := t.TempDir()
	git(t, outside, "init", "-q", "-b", "main")
	if err := os.Symlink(outside, filepath.Join(f.dir, "linked")); err != nil {
		t.Fatal(err)
	}
	r, _ = f.repo.In("linked")
	if st, err := f.svc.Status(ctx(t), r); err != nil || st.IsRepo {
		t.Fatalf("symlink %+v %v", st, err)
	}
	// Paths that leave the project or touch .git.
	for _, bad := range []string{"../x", "/etc", "a/../../x", ".git", "a/.git", "a\nb"} {
		if _, err := f.repo.In(bad); code(err) != "bad_path" {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if r, err := f.repo.In("."); err != nil || r.Sub != "" {
		t.Fatalf("dot %+v %v", r, err)
	}
}

// TestNestedInOuterRepo: the outer repository does not list a nested one as
// a change and "commit all" never records it as a gitlink, with or without
// commits in the nested repository.
func TestNestedInOuterRepo(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	inner := f.newRepo("libs/inner")
	empty := filepath.Join(f.dir, "empty repo [1]")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, empty, "init", "-q", "-b", "main")
	write(t, inner, "changed.txt", "x\n")

	st, err := f.svc.Status(ctx(t), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if st.Changed != 0 || len(st.Files) != 0 || strings.Join(st.NestedRepos, ",") != "empty repo [1],libs/inner" {
		t.Fatalf("nested only %+v", st)
	}
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "x", All: true}); code(err) != "nothing_to_commit" {
		t.Fatalf("nothing but nested: %v", err)
	}
	write(t, f.dir, "own.txt", "own\n")
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "Add own", All: true}); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "own.txt" {
		t.Fatalf("committed %q", got)
	}
	if got := git(t, f.dir, "ls-files", "-s"); strings.Contains(got, "160000") {
		t.Fatalf("gitlink recorded:\n%s", got)
	}
	// The nested repository keeps its own change.
	if got := git(t, inner, "status", "--porcelain"); !strings.Contains(got, "changed.txt") {
		t.Fatalf("inner %q", got)
	}
}

// TestInitRefusedAboveRepos: no outer repository around existing ones.
func TestInitRefusedAboveRepos(t *testing.T) {
	f := newFixture(t, true)
	f.newRepo("app-a")
	_, err := f.svc.Init(ctx(t), f.repo, "")
	if code(err) != "nested_repos" {
		t.Fatalf("init %v", err)
	}
	if e := err.(*Error); len(e.Paths) != 1 || e.Paths[0] != "app-a" {
		t.Fatalf("paths %v", e.Paths)
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".git")); !os.IsNotExist(err) {
		t.Fatal("init created a repository")
	}
}

// TestPick: which repository a command bar step means.
func TestPick(t *testing.T) {
	f := newFixture(t, true)

	// Nothing here: the step itself reports "not a repository".
	if r, err := f.svc.Pick(ctx(t), f.repo, ""); err != nil || r.Sub != "" {
		t.Fatalf("empty %+v %v", r, err)
	}
	// One repository below: no need to name it.
	f.newRepo("app-a")
	if r, err := f.svc.Pick(ctx(t), f.repo, ""); err != nil || r.Sub != "app-a" {
		t.Fatalf("single %+v %v", r, err)
	}
	// Several: refused without a name.
	f.newRepo("group/app-b")
	f.newRepo("other/app-b")
	_, err := f.svc.Pick(ctx(t), f.repo, "")
	if code(err) != "repo_needed" || strings.Join(err.(*Error).Paths, ",") != "app-a,group/app-b,other/app-b" {
		t.Fatalf("several %v", err)
	}
	for name, want := range map[string]string{"app-a": "app-a", "APP-A": "app-a", "group/app-b": "group/app-b"} {
		if r, err := f.svc.Pick(ctx(t), f.repo, name); err != nil || r.Sub != want {
			t.Fatalf("%q: %+v %v", name, r, err)
		}
	}
	if _, err := f.svc.Pick(ctx(t), f.repo, "app-b"); code(err) != "repo_ambiguous" {
		t.Fatalf("ambiguous %v", err)
	}
	if _, err := f.svc.Pick(ctx(t), f.repo, "nope"); code(err) != "repo_not_found" {
		t.Fatalf("missing %v", err)
	}
	if _, err := f.svc.Pick(ctx(t), f.repo, "."); code(err) != "repo_not_found" {
		t.Fatalf("no own repo %v", err)
	}
	// The project's own repository next to nested ones.
	git(t, f.dir, "init", "-q", "-b", "main")
	if r, err := f.svc.Pick(ctx(t), f.repo, "."); err != nil || r.Sub != "" {
		t.Fatalf("dot %+v %v", r, err)
	}
	if _, err := f.svc.Pick(ctx(t), f.repo, ""); code(err) != "repo_needed" {
		t.Fatalf("own plus nested %v", err)
	}
}
