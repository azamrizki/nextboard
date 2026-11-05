package gitsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nextboard/internal/jobs"
	"nextboard/internal/machine"
	"nextboard/internal/store"
)

// isolate points git at a throwaway global config (identity included) so
// the user's ~/.gitconfig, credential helpers and askpass never apply.
func isolate(t *testing.T, identity bool) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	body := "[init]\n\tdefaultBranch = main\n[advice]\n\tdetachedHead = false\n"
	if identity {
		body += "[user]\n\tname = Tester\n\temail = tester@example.invalid\n"
	}
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL",
		"GIT_ASKPASS", "SSH_ASKPASS", "GIT_SSH_COMMAND", "GIT_SSH", "GIT_DIR", "GIT_WORK_TREE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	t      *testing.T
	svc    *Service
	jm     *jobs.Manager
	repo   Repo
	dir    string
	events []map[string]any
	mu     sync.Mutex
}

func newFixture(t *testing.T, identity bool) *fixture {
	t.Helper()
	isolate(t, identity)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	local := machine.NewLocal(machine.Config{})
	host := func(id string) (machine.Host, bool) {
		if id == machine.LocalID {
			return local, true
		}
		return nil, false
	}
	f := &fixture{t: t, dir: t.TempDir()}
	f.jm = jobs.NewManager(st, host)
	t.Cleanup(f.jm.Close)
	f.svc = New()
	f.svc.Host, f.svc.Jobs = host, f.jm
	f.svc.Publish = func(v any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if m, ok := v.(map[string]any); ok {
			f.events = append(f.events, m)
		}
	}
	f.repo = Repo{ProjectID: "p_test", MachineID: machine.LocalID, Path: f.dir}
	return f
}

func (f *fixture) initRepo() {
	git(f.t, f.dir, "init", "-q", "-b", "main")
	write(f.t, f.dir, "README.md", "hello\n")
	write(f.t, f.dir, "src/a.go", "package a\n")
	git(f.t, f.dir, "add", "-A")
	git(f.t, f.dir, "commit", "-qm", "init")
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return "other:" + err.Error()
	}
	return ""
}

func (f *fixture) wait(j jobs.Job) jobs.Job {
	f.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		v, ok := f.jm.Get(j.ID)
		if ok && !v.Active() {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("job %s did not finish", j.ID)
	return jobs.Job{}
}

func (f *fixture) status() *Status {
	f.t.Helper()
	st, err := f.svc.Status(ctx(f.t), f.repo)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func TestStatusShapes(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	write(t, f.dir, "old.txt", "one\ntwo\n")
	git(t, f.dir, "add", "old.txt")
	git(t, f.dir, "commit", "-qm", "old")
	git(t, f.dir, "mv", "old.txt", "new.txt")
	write(t, f.dir, "src/a.go", "package a\n\nfunc A() {}\n")
	write(t, f.dir, "un tracked.txt", "x\ny\nz\n")
	write(t, f.dir, "-dash.txt", "dash\n")
	write(t, f.dir, "staged.txt", "s\n")
	git(t, f.dir, "add", "staged.txt")
	os.Remove(filepath.Join(f.dir, "README.md"))
	// Large untracked file (sparse, 101 MiB): reported, never read.
	big, _ := os.Create(filepath.Join(f.dir, "data", "dump.bin"))
	if big == nil {
		os.MkdirAll(filepath.Join(f.dir, "data"), 0o755)
		big, _ = os.Create(filepath.Join(f.dir, "data", "dump.bin"))
	}
	big.Truncate(101 << 20)
	big.Close()

	// Status from a subfolder of the repo works on the whole repo.
	sub := f.repo
	sub.Path = filepath.Join(f.dir, "src")
	st, err := f.svc.Status(ctx(t), sub)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsRepo || st.Branch != "main" || st.Detached || !st.HasCommits || st.Root != f.dir || st.HasRemote {
		t.Fatalf("status header %+v", st)
	}
	byPath := map[string]File{}
	for _, fl := range st.Files {
		byPath[fl.Path] = fl
	}
	want := map[string]string{"new.txt": "renamed", "src/a.go": "modified", "un tracked.txt": "untracked", "-dash.txt": "untracked",
		"staged.txt": "added", "README.md": "deleted", "data/dump.bin": "untracked"}
	for p, s := range want {
		if byPath[p].Status != s {
			t.Fatalf("%s: %+v (all %+v)", p, byPath[p], st.Files)
		}
	}
	if byPath["new.txt"].OldPath != "old.txt" || byPath["new.txt"].Index != "R" || byPath["new.txt"].Worktree != " " {
		t.Fatalf("rename %+v", byPath["new.txt"])
	}
	if a := byPath["src/a.go"]; a.Added != 2 || a.Deleted != 0 || a.Index != " " || a.Worktree != "M" {
		t.Fatalf("numstat %+v", a)
	}
	if u := byPath["un tracked.txt"]; u.Added != 3 {
		t.Fatalf("untracked lines %+v", u)
	}
	if d := byPath["README.md"]; d.Deleted != 1 {
		t.Fatalf("deleted %+v", d)
	}
	if len(st.LargeFiles) != 1 || st.LargeFiles[0].Path != "data/dump.bin" || st.LargeFiles[0].SizeBytes != 101<<20 || st.LargeFiles[0].Tracked {
		t.Fatalf("large %+v", st.LargeFiles)
	}
	if st.Changed != len(st.Files) || st.Truncated {
		t.Fatalf("counts %d %d %v", st.Changed, len(st.Files), st.Truncated)
	}

	// Not a repository.
	f2 := newFixture(t, true)
	st2, err := f2.svc.Status(ctx(t), f2.repo)
	if err != nil || st2.IsRepo || st2.Files == nil {
		t.Fatalf("non repo %+v %v", st2, err)
	}
	// Missing machine.
	if _, err := f.svc.Status(ctx(t), Repo{MachineID: "m_nope", Path: f.dir}); code(err) != "machine_not_found" {
		t.Fatalf("missing machine %v", err)
	}
	// Published once (summary), not again for the same state.
	f.status()
	f.mu.Lock()
	n := 0
	for _, e := range f.events {
		if e["type"] == "git" {
			n++
		}
	}
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("git events %d", n)
	}
}

func TestStatusTruncatedAndConflicts(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	for i := 0; i < MaxFiles+5; i++ {
		write(t, f.dir, fmt.Sprintf("many/f%03d.txt", i), "x\n")
	}
	st := f.status()
	if !st.Truncated || len(st.Files) != MaxFiles || st.Changed != MaxFiles+5 {
		t.Fatalf("truncate %v %d %d", st.Truncated, len(st.Files), st.Changed)
	}
	os.RemoveAll(filepath.Join(f.dir, "many"))

	// Merge conflict: commit refused with 409 conflicts.
	git(t, f.dir, "checkout", "-q", "-b", "other")
	write(t, f.dir, "README.md", "theirs\n")
	git(t, f.dir, "commit", "-qam", "theirs")
	git(t, f.dir, "checkout", "-q", "main")
	write(t, f.dir, "README.md", "ours\n")
	git(t, f.dir, "commit", "-qam", "ours")
	cmd := exec.Command("git", "merge", "other")
	cmd.Dir = f.dir
	cmd.Run()
	st = f.status()
	if !st.Merging || st.Conflicts != 1 || st.Files[0].Status != "conflict" {
		t.Fatalf("conflict %+v", st)
	}
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "x", All: true}); code(err) != "conflicts" {
		t.Fatalf("commit during conflict: %v", err)
	}
}

func TestInitCommitLogAndErrors(t *testing.T) {
	f := newFixture(t, true)
	write(t, f.dir, "a.txt", "a\n")
	write(t, f.dir, "b.txt", "b\n")
	st, err := f.svc.Init(ctx(t), f.repo, "")
	if err != nil || !st.IsRepo || st.Branch != "main" || st.HasCommits {
		t.Fatalf("init %+v %v", st, err)
	}
	if _, err := f.svc.Init(ctx(t), f.repo, "main"); code(err) != "exists" {
		t.Fatalf("init twice %v", err)
	}
	if _, err := f.svc.Init(ctx(t), f.repo, "bad name"); code(err) != "bad_ref" {
		t.Fatalf("bad branch %v", err)
	}
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "  ", All: true}); code(err) != "empty_message" {
		t.Fatalf("empty message %v", err)
	}
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "x", Paths: []string{"../etc/passwd"}}); code(err) != "bad_path" {
		t.Fatalf("bad path %v", err)
	}
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "x", Paths: []string{".git/config"}}); code(err) != "bad_path" {
		t.Fatalf(".git path %v", err)
	}
	// Only the selected path is committed (first commit of the repo).
	res, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "Add a", Paths: []string{"a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit.Subject != "Add a" || len(res.Commit.Hash) < 40 || res.Status == nil || len(res.Status.Files) != 1 || res.Status.Files[0].Path != "b.txt" {
		t.Fatalf("commit %+v %+v", res.Commit, res.Status)
	}
	if got := git(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "a.txt" {
		t.Fatalf("committed %q", got)
	}
	// Paths with spaces, dashes and glob characters stay literal.
	write(t, f.dir, "-x [1]*.txt", "lit\n")
	write(t, f.dir, "-x 1a.txt", "other\n")
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "Literal", Paths: []string{"-x [1]*.txt"}}); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.dir, "show", "--name-only", "--format=", "-z", "HEAD"); got != "-x [1]*.txt\x00" && got != "-x [1]*.txt" {
		t.Fatalf("literal %q", got)
	}
	// Nothing to commit for an unchanged path.
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "x", Paths: []string{"a.txt"}}); code(err) != "nothing_to_commit" {
		t.Fatalf("nothing %v", err)
	}
	items, err := f.svc.Log(ctx(t), f.repo, 10)
	if err != nil || len(items) != 2 || items[0].Subject != "Literal" || items[0].Pushed || items[0].At.IsZero() || items[0].Author != "Tester" {
		t.Fatalf("log %+v %v", items, err)
	}
	// Large file refused unless allowed; untrack-large keeps it off.
	big, _ := os.Create(filepath.Join(f.dir, "big.bin"))
	big.Truncate(LargeBytes + 1)
	big.Close()
	_, err = f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "big", All: true})
	var ge *Error
	if !errors.As(err, &ge) || ge.Code != "large_files" || !slices.Equal(ge.Paths, []string{"big.bin"}) {
		t.Fatalf("large %v", err)
	}
	st, err = f.svc.UntrackLarge(ctx(t), f.repo, []string{"big.bin"})
	if err != nil {
		t.Fatal(err)
	}
	gi, _ := os.ReadFile(filepath.Join(f.dir, ".gitignore"))
	if string(gi) != "/big.bin\n" || len(st.LargeFiles) != 0 {
		t.Fatalf("gitignore %q %+v", gi, st.LargeFiles)
	}
	// Running it twice does not duplicate the line.
	f.svc.UntrackLarge(ctx(t), f.repo, []string{"big.bin"})
	if gi, _ := os.ReadFile(filepath.Join(f.dir, ".gitignore")); string(gi) != "/big.bin\n" {
		t.Fatalf("gitignore twice %q", gi)
	}
	if gitignoreLine("a*b[1] ") != `/a\*b\[1]\ ` {
		t.Fatalf("escape %q", gitignoreLine("a*b[1] "))
	}
	// Diff: tracked, staged and untracked.
	write(t, f.dir, "a.txt", "a\nmore\n")
	d, err := f.svc.Diff(ctx(t), f.repo, "a.txt", false)
	if err != nil || !strings.Contains(d.Diff, "+more") || d.Truncated {
		t.Fatalf("diff %+v %v", d, err)
	}
	if d, _ := f.svc.Diff(ctx(t), f.repo, "a.txt", true); d.Diff != "" {
		t.Fatalf("staged diff %q", d.Diff)
	}
	d, err = f.svc.Diff(ctx(t), f.repo, "-x 1a.txt", false)
	if err != nil || !strings.Contains(d.Diff, "+other") {
		t.Fatalf("untracked diff %+v %v", d, err)
	}
}

func TestIdentityMissing(t *testing.T) {
	f := newFixture(t, false)
	f.svc.Init(ctx(t), f.repo, "main")
	write(t, f.dir, "a.txt", "a\n")
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "x", All: true}); code(err) != "git_identity" {
		t.Fatalf("identity %v", err)
	}
}

func TestBusy(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	unlock, err := f.svc.lock(f.repo, "push")
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.dir, "a.txt", "a\n")
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "x", All: true}); code(err) != "git_busy" {
		t.Fatalf("busy %v", err)
	}
	if _, err := f.svc.Push(f.repo, false, nil); code(err) != "git_busy" {
		t.Fatalf("busy push %v", err)
	}
	unlock()
	if _, err := f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "x", All: true}); err != nil {
		t.Fatal(err)
	}
}

// remoteFixture: a local bare remote and a second clone.
func remoteFixture(t *testing.T) (*fixture, string, string) {
	f := newFixture(t, true)
	bare := filepath.Join(t.TempDir(), "remote.git")
	git(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	f.initRepo()
	git(t, f.dir, "remote", "add", "origin", bare)
	other := filepath.Join(t.TempDir(), "other")
	return f, bare, other
}

func TestPushPullFetch(t *testing.T) {
	f, bare, other := remoteFixture(t)
	st := f.status()
	if !st.HasRemote || st.RemoteURL != bare || st.Upstream != "" || st.FetchedAt != nil {
		t.Fatalf("remote status %+v", st)
	}
	// Push without upstream publishes the branch with -u.
	j, err := f.svc.Push(f.repo, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != jobs.KindGit || j.Purpose != "push" || j.ProjectID != "p_test" {
		t.Fatalf("job %+v", j)
	}
	j = f.wait(j)
	if j.State != jobs.StateDone {
		t.Fatalf("push %+v", j)
	}
	var nr NetResult
	json.Unmarshal(j.Result, &nr)
	if !strings.Contains(nr.Output, "main") {
		t.Fatalf("push output %+v", nr)
	}
	st = f.status()
	if st.Upstream != "origin/main" || st.Ahead != 0 || st.Behind != 0 {
		t.Fatalf("after push %+v", st)
	}
	items, _ := f.svc.Log(ctx(t), f.repo, 5)
	if len(items) != 1 || !items[0].Pushed {
		t.Fatalf("pushed flag %+v", items)
	}
	// Local commit → ahead 1, not pushed.
	write(t, f.dir, "c.txt", "c\n")
	f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "c", All: true})
	st = f.status()
	items, _ = f.svc.Log(ctx(t), f.repo, 5)
	if st.Ahead != 1 || items[0].Pushed || !items[1].Pushed {
		t.Fatalf("ahead %+v %+v", st, items)
	}
	if j := f.wait(must(f.svc.Push(f.repo, false, nil))); j.State != jobs.StateDone {
		t.Fatalf("push2 %+v", j)
	}
	// Someone else pushes; fetch then pull --ff-only.
	git(t, filepath.Dir(other), "clone", "-q", bare, other)
	write(t, other, "d.txt", "d\n")
	git(t, other, "add", "-A")
	git(t, other, "-c", "user.name=O", "-c", "user.email=o@x", "commit", "-qm", "d")
	git(t, other, "push", "-q")
	if j := f.wait(must(f.svc.Fetch(f.repo, nil))); j.State != jobs.StateDone {
		t.Fatalf("fetch %+v", j)
	}
	st = f.status()
	if st.Behind != 1 || st.FetchedAt == nil {
		t.Fatalf("behind %+v", st)
	}
	if j := f.wait(must(f.svc.Pull(f.repo, nil))); j.State != jobs.StateDone {
		t.Fatalf("pull %+v", j)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "d.txt")); err != nil {
		t.Fatal("pull did not fast-forward")
	}
	// Diverged: pull fails with code diverged, nothing merged.
	write(t, other, "e.txt", "e\n")
	git(t, other, "add", "-A")
	git(t, other, "-c", "user.name=O", "-c", "user.email=o@x", "commit", "-qm", "e")
	git(t, other, "push", "-q")
	write(t, f.dir, "f.txt", "f\n")
	f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "f", All: true})
	f.wait(must(f.svc.Fetch(f.repo, nil)))
	head := git(t, f.dir, "rev-parse", "HEAD")
	j = f.wait(must(f.svc.Pull(f.repo, nil)))
	if j.State != jobs.StateFailed || j.Code != "diverged" || !strings.Contains(j.Error, "diverged") {
		t.Fatalf("diverged %+v", j)
	}
	if git(t, f.dir, "rev-parse", "HEAD") != head {
		t.Fatal("diverged pull changed HEAD")
	}
	// Rejected push (remote has commits we don't).
	j = f.wait(must(f.svc.Push(f.repo, false, nil)))
	if j.State != jobs.StateFailed || j.Code != "rejected" {
		t.Fatalf("rejected %+v", j)
	}
	// The project lock is free again after jobs.
	if f.svc.Busy(f.repo) != "" {
		t.Fatal("lock held")
	}
}

func must(j jobs.Job, err error) jobs.Job {
	if err != nil {
		panic(err)
	}
	return j
}

func TestPushNeverPrompts(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	// An HTTP remote that asks for credentials: git must fail at once
	// (GIT_TERMINAL_PROMPT=0) instead of waiting for a password.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	git(t, f.dir, "remote", "add", "origin", srv.URL+"/repo.git")
	start := time.Now()
	j := f.wait(must(f.svc.Push(f.repo, false, nil)))
	if j.State != jobs.StateFailed || j.Code != "auth_failed" || time.Since(start) > 20*time.Second {
		t.Fatalf("prompting push %+v after %s", j, time.Since(start))
	}
	// Credentials in a remote URL are never shown.
	git(t, f.dir, "remote", "set-url", "origin", "https://me:s3cret@example.invalid/me/app.git")
	st := f.status()
	if strings.Contains(st.RemoteURL, "s3cret") || st.WebURL != "https://example.invalid/me/app" {
		t.Fatalf("remote url %+v", st)
	}
	// An ssh remote runs with BatchMode (no password prompt either).
	if !strings.Contains(gitEnv, "BatchMode=yes") || !strings.Contains(gitEnv, "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("env")
	}
}

func TestDiscardAndTrash(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	now := time.Date(2026, 10, 28, 14, 15, 0, 0, time.Local)
	f.svc.Now = func() time.Time { return now }
	write(t, f.dir, "src/a.go", "package a // mine\n")
	write(t, f.dir, "notes/new.txt", "new\n")
	write(t, f.dir, "added.txt", "added\n")
	git(t, f.dir, "add", "added.txt")
	res, err := f.svc.Discard(ctx(t), f.repo, []string{"src/a.go", "notes/new.txt", "added.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if res.TrashedTo != ".git/nextboard-trash/20261028-141500" || len(res.Status.Files) != 0 {
		t.Fatalf("discard %+v %+v", res, res.Status)
	}
	trash := filepath.Join(f.dir, ".git", "nextboard-trash", "20261028-141500")
	for rel, want := range map[string]string{"src/a.go": "package a // mine\n", "notes/new.txt": "new\n", "added.txt": "added\n"} {
		b, err := os.ReadFile(filepath.Join(trash, rel))
		if err != nil || string(b) != want {
			t.Fatalf("trash %s: %q %v", rel, b, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "src/a.go")); string(b) != "package a\n" {
		t.Fatalf("restored %q", b)
	}
	// A second discard in the same second gets its own folder.
	write(t, f.dir, "README.md", "changed\n")
	res2, err := f.svc.Discard(ctx(t), f.repo, []string{"README.md"})
	if err != nil || res2.TrashedTo != ".git/nextboard-trash/20261028-141500-2" {
		t.Fatalf("second %+v %v", res2, err)
	}
	items, err := f.svc.Trash(ctx(t), f.repo)
	if err != nil || len(items) != 2 || items[0].ID != "20261028-141500-2" || !slices.Equal(items[1].Paths, []string{"added.txt", "notes/new.txt", "src/a.go"}) {
		t.Fatalf("trash list %+v %v", items, err)
	}
	// Restore never overwrites.
	write(t, f.dir, "notes/new.txt", "someone else\n")
	_, err = f.svc.RestoreTrash(ctx(t), f.repo, "20261028-141500", []string{"notes/new.txt"})
	var ge *Error
	if !errors.As(err, &ge) || ge.Code != "exists" || !slices.Equal(ge.Paths, []string{"notes/new.txt"}) {
		t.Fatalf("overwrite %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "notes/new.txt")); string(b) != "someone else\n" {
		t.Fatal("overwritten")
	}
	os.Remove(filepath.Join(f.dir, "notes/new.txt"))
	got, err := f.svc.RestoreTrash(ctx(t), f.repo, "20261028-141500", nil)
	if err != nil || len(got) != 3 {
		t.Fatalf("restore %v %v", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "src/a.go")); string(b) != "package a // mine\n" {
		t.Fatalf("restored mine %q", b)
	}
	if _, err := os.Stat(trash); !os.IsNotExist(err) {
		t.Fatal("empty trash item kept")
	}
	if _, err := f.svc.RestoreTrash(ctx(t), f.repo, "../x", nil); code(err) != "bad_id" {
		t.Fatalf("bad id %v", err)
	}
	if _, err := f.svc.Discard(ctx(t), f.repo, []string{"README.md"}); code(err) != "nothing_to_discard" {
		t.Fatalf("nothing %v", err)
	}
}

func TestBranches(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	st, err := f.svc.CreateBranch(ctx(t), f.repo, "feature/x", "", true)
	if err != nil || st.Branch != "feature/x" {
		t.Fatalf("create %+v %v", st, err)
	}
	if _, err := f.svc.CreateBranch(ctx(t), f.repo, "feature/x", "", true); code(err) != "exists" {
		t.Fatalf("exists %v", err)
	}
	for _, bad := range []string{"-x", "a..b", "a b", "x~1", "x.lock"} {
		if _, err := f.svc.CreateBranch(ctx(t), f.repo, bad, "", false); code(err) != "bad_ref" {
			t.Fatalf("bad %q: %v", bad, err)
		}
	}
	if _, err := f.svc.CreateBranch(ctx(t), f.repo, "y", "nope", false); code(err) != "bad_ref" {
		t.Fatalf("bad from %v", err)
	}
	if _, err := f.svc.CreateBranch(ctx(t), f.repo, "side", "main", false); err != nil {
		t.Fatal(err)
	}
	write(t, f.dir, "f.txt", "f\n")
	f.svc.Commit(ctx(t), f.repo, CommitRequest{Message: "f", All: true})
	bs, err := f.svc.Branches(ctx(t), f.repo)
	if err != nil || bs.Current != "feature/x" || len(bs.Items) != 3 || !bs.Items[0].Current || bs.Items[0].LastCommitAt == nil {
		t.Fatalf("branches %+v %v", bs, err)
	}
	// Switch refused when it would overwrite local work.
	git(t, f.dir, "checkout", "-q", "main")
	write(t, f.dir, "f.txt", "local\n")
	if _, err := f.svc.Switch(ctx(t), f.repo, "feature/x"); code(err) != "git_refused" {
		t.Fatalf("refused %v", err)
	}
	os.Remove(filepath.Join(f.dir, "f.txt"))
	st, err = f.svc.Switch(ctx(t), f.repo, "feature/x")
	if err != nil || st.Branch != "feature/x" {
		t.Fatalf("switch %+v %v", st, err)
	}
	if _, err := f.svc.Switch(ctx(t), f.repo, "ghost"); code(err) != "not_found" {
		t.Fatalf("ghost %v", err)
	}
	git(t, f.dir, "checkout", "-q", "main")
	if err := f.svc.DeleteBranch(ctx(t), f.repo, "feature/x"); code(err) != "not_merged" {
		t.Fatalf("not merged %v", err)
	}
	if err := f.svc.DeleteBranch(ctx(t), f.repo, "side"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteBranch(ctx(t), f.repo, "main"); code(err) != "git_refused" {
		t.Fatalf("current %v", err)
	}
}

func TestSuggestSubject(t *testing.T) {
	F := func(p, s string) File { return File{Path: p, Status: s} }
	cases := []struct {
		files []File
		want  string
	}{
		{[]File{F("src/app.ts", "modified")}, "chore: update app.ts"},
		{[]File{F("src/app.ts", "untracked")}, "chore: add app.ts"},
		{[]File{F("src/app.ts", "deleted")}, "chore: remove app.ts"},
		{[]File{F("internal/orch/lead.go", "modified")}, "chore(orch): update lead.go"},
		{[]File{{Path: "web/src/new.ts", OldPath: "web/src/old.ts", Status: "renamed"}}, "chore(web): rename old.ts to new.ts"},
		{[]File{F("src/a.ts", "modified"), F("src/b.ts", "added")}, "chore: update 2 files"},
		{[]File{F("web/src/git/a.ts", "modified"), F("web/src/git/b.ts", "added")}, "chore(git): update 2 files"},
		{[]File{F("README.md", "modified"), F("docs/API.md", "modified")}, "docs: update API.md, README.md"},
		{[]File{F("a_test.go", "modified"), F("web/x.test.ts", "modified")}, "test: update a_test.go, x.test.ts"},
		{[]File{F("internal/orch/a_test.go", "modified")}, "test(orch): update a_test.go"},
		{[]File{F(".github/workflows/ci.yml", "modified")}, "ci: update ci.yml"},
		{[]File{F("go.mod", "modified"), F("go.sum", "modified")}, "build: update 2 files"},
		{[]File{F("web/src/a.ts", "modified"), F("web/lib/b.ts", "modified"), F("go.mod", "modified")}, "chore(web): update 3 files"},
		{[]File{F("web/src/a.ts", "modified"), F("web/lib/b.ts", "modified")}, "chore(web): update 2 files"},
		{[]File{F("a.go", "modified"), F("b.go", "modified")}, "chore: update 2 files"},
	}
	for _, c := range cases {
		if got := SuggestSubject(c.files); got != c.want {
			t.Errorf("%v: %q want %q", c.files, got, c.want)
		}
	}
	long := []File{}
	for i := 0; i < 30; i++ {
		long = append(long, F(fmt.Sprintf("docs/very-long-document-name-%d.md", i), "modified"), F(fmt.Sprintf("other/x%d.md", i), "modified"))
	}
	if got := SuggestSubject(long); len([]rune(got)) > 72 || got != "docs: update 60 files" {
		t.Errorf("long %q", got)
	}
	if WebURL("git@github.com:me/app.git") != "https://github.com/me/app" || WebURL("ssh://git@host:22/a/b.git") != "https://host/a/b" ||
		WebURL("/srv/git/x.git") != "" || WebURL("file:///x") != "" {
		t.Error("web url")
	}
}

// fakeGh writes a gh stand-in: `auth status` succeeds, `repo create`
// creates a local bare repo, adds it as origin and pushes. It records its
// arguments; it never talks to GitHub.
func fakeGh(t *testing.T, loggedIn bool) (string, string) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	remotes := filepath.Join(dir, "remotes")
	os.Mkdir(remotes, 0o755)
	status := `echo "github.com"; echo "  ✓ Logged in to github.com account octo-cat (keyring)"; exit 0`
	if !loggedIn {
		status = `echo "You are not logged into any GitHub hosts. To log in, run: gh auth login" >&2; exit 1`
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + machine.Quote(log) + `
case "$1 $2" in
"auth status") ` + status + ` ;;
"repo create")
  name=$3; bare=` + machine.Quote(remotes) + `/$(basename "$name").git
  git init -q --bare "$bare" || exit 1
  git remote add origin "$bare" || exit 1
  git push -q -u origin HEAD || exit 1
  echo "https://github.com/octo-cat/$name"; exit 0 ;;
esac
exit 2
`
	p := filepath.Join(dir, "gh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p, log
}

func TestGitHubPublishWithFakeGh(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	gh, log := fakeGh(t, true)
	f.svc.Gh = gh
	g, err := f.svc.GitHubStatus(ctx(t), f.repo)
	if err != nil || !g.GhInstalled || !g.Authenticated || g.User != "octo-cat" || g.RepoURL != "" {
		t.Fatalf("github %+v %v", g, err)
	}
	write(t, f.dir, "new.txt", "n\n")
	if _, err := f.svc.PublishGitHub(ctx(t), f.repo, PublishRequest{Name: "bad name"}, nil); code(err) != "bad_name" {
		t.Fatalf("bad name %v", err)
	}
	j, err := f.svc.PublishGitHub(ctx(t), f.repo, PublishRequest{Name: "my-app", Visibility: "private", CommitAll: true, Message: "Ship it"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	j = f.wait(j)
	if j.State != jobs.StateDone || j.Kind != jobs.KindPublish {
		t.Fatalf("publish %+v", j)
	}
	var pr PublishResult
	json.Unmarshal(j.Result, &pr)
	if pr.RepoURL != "https://github.com/octo-cat/my-app" || pr.Committed == nil || pr.Committed.Subject != "Ship it" {
		t.Fatalf("result %+v", pr)
	}
	args, _ := os.ReadFile(log)
	if !strings.Contains(string(args), "repo create my-app --private --source . --remote origin --push") {
		t.Fatalf("gh args %q", args)
	}
	st := f.status()
	if !st.HasRemote || st.Upstream != "origin/main" || len(st.Files) != 0 {
		t.Fatalf("after publish %+v", st)
	}
	if _, err := f.svc.PublishGitHub(ctx(t), f.repo, PublishRequest{Name: "again"}, nil); code(err) != "remote_exists" {
		t.Fatalf("remote exists %v", err)
	}
	// Not logged in / not installed.
	gh2, _ := fakeGh(t, false)
	f.svc.Gh = gh2
	if g, _ := f.svc.GitHubStatus(ctx(t), f.repo); !g.GhInstalled || g.Authenticated {
		t.Fatalf("logged out %+v", g)
	}
	f.svc.Gh = filepath.Join(t.TempDir(), "no-gh")
	if g, _ := f.svc.GitHubStatus(ctx(t), f.repo); g.GhInstalled || g.InstallHint == "" {
		t.Fatalf("missing gh %+v", g)
	}
}

func TestWatchLogin(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	gh, _ := fakeGh(t, true)
	f.svc.Gh = gh
	f.svc.LoginPoll = 50 * time.Millisecond
	var mu sync.Mutex
	alive := true
	f.svc.WatchLogin(f.repo, func() bool { mu.Lock(); defer mu.Unlock(); return alive })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := 0
		for _, e := range f.events {
			if e["type"] == "github" {
				n++
			}
		}
		f.mu.Unlock()
		if n >= 1 {
			mu.Lock()
			alive = false
			mu.Unlock()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no github event")
}

func TestBlameAndFileHistory(t *testing.T) {
	f := newFixture(t, true)
	write(t, f.dir, "a.txt", "one\ntwo\n")
	git(t, f.dir, "init", "-q", "-b", "main")
	if b, err := f.svc.Blame(ctx(t), f.repo, "a.txt", nil); err != nil || len(b.Lines) != 0 {
		t.Fatalf("no commits yet: %+v %v", b, err)
	}
	git(t, f.dir, "add", "a.txt")
	git(t, f.dir, "commit", "-qm", "First")
	write(t, f.dir, "a.txt", "one\nTWO\nthree\n")
	git(t, f.dir, "commit", "-qam", "Second")

	b, err := f.svc.Blame(ctx(t), f.repo, "a.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Commits) != 2 || len(b.Lines) != 3 || b.Me != "tester@example.invalid" {
		t.Fatalf("%+v", b)
	}
	if got := b.Commits[b.Lines[0]]; got.Summary != "First" || got.Author != "Tester" || got.Email != "tester@example.invalid" || got.At.IsZero() {
		t.Fatalf("%+v", got)
	}
	if b.Commits[b.Lines[1]].Summary != "Second" || b.Commits[b.Lines[2]].Summary != "Second" {
		t.Fatalf("%+v", b)
	}

	// unsaved text: the new line belongs to nobody yet, the lines below keep theirs
	text := "one\nnew\nTWO\nthree\n"
	b, err = f.svc.Blame(ctx(t), f.repo, "a.txt", &text)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Lines) != 4 || strings.Trim(b.Commits[b.Lines[1]].Hash, "0") != "" || b.Commits[b.Lines[2]].Summary != "Second" {
		t.Fatalf("%+v", b)
	}

	write(t, f.dir, "new.txt", "x\n")
	if b, err := f.svc.Blame(ctx(t), f.repo, "new.txt", nil); err != nil || len(b.Lines) != 0 {
		t.Fatalf("untracked: %+v %v", b, err)
	}
	if _, err := f.svc.Blame(ctx(t), f.repo, "../x", nil); code(err) != "bad_path" {
		t.Fatalf("bad path %v", err)
	}

	git(t, f.dir, "mv", "a.txt", "b name.txt")
	git(t, f.dir, "commit", "-qm", "Rename")
	h, err := f.svc.FileLog(ctx(t), f.repo, "b name.txt", 0, 10)
	if err != nil || len(h) != 3 || h[0].Subject != "Rename" || h[2].Subject != "First" {
		t.Fatalf("%+v %v", h, err)
	}
	// line 1 never changed
	h, err = f.svc.FileLog(ctx(t), f.repo, "b name.txt", 1, 10)
	if err != nil || len(h) != 1 || h[0].Subject != "First" {
		t.Fatalf("line: %+v %v", h, err)
	}
	if h, err = f.svc.FileLog(ctx(t), f.repo, "new.txt", 0, 10); err != nil || len(h) != 0 {
		t.Fatalf("untracked: %+v %v", h, err)
	}
	if h, err = f.svc.FileLog(ctx(t), f.repo, "new.txt", 1, 10); err != nil || len(h) != 0 {
		t.Fatalf("untracked line: %+v %v", h, err)
	}
}

func TestHistoryGraph(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	ctx := context.Background()
	root := git(t, f.dir, "rev-parse", "HEAD")

	git(t, f.dir, "checkout", "-q", "-b", "feature/login")
	write(t, f.dir, "login.txt", "one\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "Add login")
	side := git(t, f.dir, "rev-parse", "HEAD")

	git(t, f.dir, "checkout", "-q", "main")
	write(t, f.dir, "README.md", "hello\nthere\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "Update readme")
	git(t, f.dir, "tag", "-a", "v1.0", "-m", "first")
	git(t, f.dir, "merge", "-q", "--no-ff", "-m", "Merge login", "feature/login")
	merge := git(t, f.dir, "rev-parse", "HEAD")

	git(t, f.dir, "checkout", "-q", "-b", "stale", root)
	write(t, f.dir, "stale.txt", "x\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "Stale work")
	stale := git(t, f.dir, "rev-parse", "HEAD")
	git(t, f.dir, "checkout", "-q", "main")
	// a remote branch and a stash must not disturb the refs
	git(t, f.dir, "update-ref", "refs/remotes/origin/main", merge)
	git(t, f.dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	write(t, f.dir, "README.md", "dirty\n")
	git(t, f.dir, "stash", "-q")

	all, err := f.svc.LogGraph(ctx, f.repo, GraphOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	byHash := map[string]Commit{}
	for i, c := range all {
		seen[c.Hash] = i
		byHash[c.Hash] = c
	}
	if len(all) != 5 {
		t.Fatalf("all branches: %d commits %+v", len(all), all)
	}
	for _, c := range all {
		for _, p := range c.Parents {
			if j, ok := seen[p]; !ok || j <= seen[c.Hash] {
				t.Errorf("%s comes before its parent %s", c.Subject, p)
			}
		}
		if strings.Contains(c.Subject, "WIP on") {
			t.Errorf("the stash came along: %+v", c)
		}
	}
	m := byHash[merge]
	if len(m.Parents) != 2 || m.Parents[1] != side {
		t.Fatalf("merge parents %+v", m)
	}
	if len(byHash[root].Parents) != 0 {
		t.Fatalf("root parents %+v", byHash[root])
	}
	if !reflect.DeepEqual(m.Refs, []Ref{{"main", "head"}, {"origin/main", "remote"}}) {
		t.Fatalf("merge refs %+v", m.Refs)
	}
	if got := byHash[side].Refs; !reflect.DeepEqual(got, []Ref{{"feature/login", "branch"}}) {
		t.Fatalf("branch refs %+v", got)
	}
	var tagged bool
	for _, c := range all {
		tagged = tagged || (c.Subject == "Update readme" && reflect.DeepEqual(c.Refs, []Ref{{"v1.0", "tag"}}))
	}
	if !tagged {
		t.Fatalf("no tag %+v", all)
	}
	if !m.Pushed || byHash[stale].Pushed || !byHash[side].Pushed {
		t.Fatalf("pushed: merge %v stale %v side %v", m.Pushed, byHash[stale].Pushed, byHash[side].Pushed)
	}

	// Only what HEAD reaches.
	cur, err := f.svc.LogGraph(ctx, f.repo, GraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) != 4 || cur[0].Hash != merge {
		t.Fatalf("current: %+v", cur)
	}
	for _, c := range cur {
		if c.Hash == stale {
			t.Fatal("another branch came along")
		}
	}

	// Pages follow each other.
	one, err := f.svc.LogGraph(ctx, f.repo, GraphOptions{All: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	two, err := f.svc.LogGraph(ctx, f.repo, GraphOptions{All: true, Limit: 2, Skip: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 2 || len(two) != 2 || one[0].Hash != all[0].Hash || two[1].Hash != all[3].Hash {
		t.Fatalf("pages %+v %+v", one, two)
	}

	// Detached HEAD says so, and a repository without commits has no history.
	git(t, f.dir, "checkout", "-q", side)
	det, err := f.svc.LogGraph(ctx, f.repo, GraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(det[0].Refs, []Ref{{"HEAD", "head"}, {"feature/login", "branch"}}) {
		t.Fatalf("detached %+v", det[0].Refs)
	}
	empty := newFixture(t, true)
	git(t, empty.dir, "init", "-q", "-b", "main")
	if none, err := empty.svc.LogGraph(ctx, empty.repo, GraphOptions{All: true}); err != nil || len(none) != 0 {
		t.Fatalf("empty %+v %v", none, err)
	}
}

func TestCommitDetail(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	ctx := context.Background()
	root := git(t, f.dir, "rev-parse", "HEAD")

	// the first commit: everything is new
	d, err := f.svc.CommitDetail(ctx, f.repo, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Parents) != 0 || len(d.Files) == 0 || d.Files[0].Status != "added" || d.Files[0].Add == 0 {
		t.Fatalf("root %+v", d)
	}

	write(t, f.dir, "keep.txt", strings.Repeat("a line that stays the same\n", 40))
	write(t, f.dir, "gone.txt", "bye\n")
	write(t, f.dir, "logo.bin", "\x00\x01\x02")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "Add files")

	if err := os.MkdirAll(filepath.Join(f.dir, "dir with space"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, f.dir, "mv", "keep.txt", "dir with space/kept é.txt")
	write(t, f.dir, "dir with space/kept é.txt", strings.Repeat("a line that stays the same\n", 40)+"new line\n")
	git(t, f.dir, "rm", "-q", "gone.txt")
	write(t, f.dir, "README.md", "hello\nthere\n")
	write(t, f.dir, "logo.bin", "\x00\x03")
	write(t, f.dir, "new.txt", "one\ntwo\n")
	git(t, f.dir, "add", "-A")
	cmd := exec.Command("git", "commit", "-q", "-F", "-")
	cmd.Dir = f.dir
	cmd.Stdin = strings.NewReader("Rework files, with \"quotes\"\n\nA body\nof two lines.\n\nFixes: #12\n")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ann Author", "GIT_AUTHOR_EMAIL=ann@example.invalid", "GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_NAME=Cal Committer", "GIT_COMMITTER_EMAIL=cal@example.invalid", "GIT_COMMITTER_DATE=2026-02-03T04:05:06Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	git(t, f.dir, "tag", "v2")
	head := git(t, f.dir, "rev-parse", "HEAD")
	short := git(t, f.dir, "rev-parse", "--short", "HEAD")
	parent := git(t, f.dir, "rev-parse", "HEAD^")

	for _, ref := range []string{head, short, "HEAD", "v2"} {
		d, err = f.svc.CommitDetail(ctx, f.repo, ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if d.Hash != head {
			t.Fatalf("%s: %s", ref, d.Hash)
		}
	}
	if d.Short != short || !reflect.DeepEqual(d.Parents, []string{parent}) {
		t.Fatalf("head %+v", d)
	}
	if d.Subject != `Rework files, with "quotes"` || d.Body != "A body\nof two lines.\n\nFixes: #12" {
		t.Fatalf("message %q %q", d.Subject, d.Body)
	}
	if d.Author != "Ann Author" || d.Email != "ann@example.invalid" || d.Committer != "Cal Committer" || d.CommitterEmail != "cal@example.invalid" {
		t.Fatalf("people %+v", d)
	}
	if !d.At.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) || !d.CommittedAt.Equal(time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)) {
		t.Fatalf("dates %v %v", d.At, d.CommittedAt)
	}
	if !reflect.DeepEqual(d.Refs, []Ref{{"main", "head"}, {"v2", "tag"}}) && !reflect.DeepEqual(d.Refs, []Ref{{"v2", "tag"}, {"main", "head"}}) {
		t.Fatalf("refs %+v", d.Refs)
	}
	files := map[string]CommitFile{}
	for _, c := range d.Files {
		files[c.Path] = c
	}
	want := map[string]CommitFile{
		"README.md":                 {Path: "README.md", Status: "modified", Add: 1, Del: 0},
		"dir with space/kept é.txt": {Path: "dir with space/kept é.txt", OldPath: "keep.txt", Status: "renamed", Add: 1, Del: 0},
		"gone.txt":                  {Path: "gone.txt", Status: "deleted", Add: 0, Del: 1},
		"logo.bin":                  {Path: "logo.bin", Status: "modified", Binary: true},
		"new.txt":                   {Path: "new.txt", Status: "added", Add: 2},
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files\n%+v\nwant\n%+v", files, want)
	}

	// A merge shows what it brought in, against its first parent.
	git(t, f.dir, "checkout", "-q", "-b", "side", parent)
	write(t, f.dir, "side.txt", "s\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "Side")
	git(t, f.dir, "checkout", "-q", "main")
	git(t, f.dir, "merge", "-q", "--no-ff", "-m", "Merge side", "side")
	d, err = f.svc.CommitDetail(ctx, f.repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Parents) != 2 || len(d.Files) != 1 || d.Files[0].Path != "side.txt" || d.Body != "" {
		t.Fatalf("merge %+v", d)
	}

	// Refusals.
	for ref, code := range map[string]string{
		"--output=/tmp/x": "bad_ref",
		"a b":             "bad_ref",
		"HEAD;id":         "bad_ref",
		"0123456789abcdef0123456789abcdef01234567": "commit_not_found",
		"nope": "commit_not_found",
	} {
		_, err := f.svc.CommitDetail(ctx, f.repo, ref)
		var ge *Error
		if !errors.As(err, &ge) || ge.Code != code {
			t.Errorf("%s: %v", ref, err)
		}
	}
	// A tree is no commit.
	if _, err := f.svc.CommitDetail(ctx, f.repo, "HEAD^{tree}"); err == nil {
		t.Error("a tree passed")
	}
}

func TestCommitDetailBounds(t *testing.T) {
	var names, stats strings.Builder
	for i := 0; i < MaxCommitFiles+5; i++ {
		fmt.Fprintf(&names, "M\x00f%d\x00", i)
		fmt.Fprintf(&stats, "1\t0\tf%d\x00", i)
	}
	head := strings.Repeat("x\x00", 11) + "2026-01-02T03:04:05Z"
	d := parseCommitDetail(head + commitFilesMark + names.String() + commitStatsMark + stats.String())
	if d == nil || len(d.Files) != MaxCommitFiles || !d.Truncated || d.Files[3].Add != 1 {
		t.Fatalf("%v", d != nil)
	}
	if parseCommitDetail("garbage") != nil {
		t.Fatal("garbage parsed")
	}
}

func TestStagePartsOfAFile(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	ctx := context.Background()
	write(t, f.dir, "a.txt", "1\n2\n3\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "base")
	write(t, f.dir, "a.txt", "1 new\n2\n3 new\n")

	// only the first change goes in
	text := "1 new\n2\n3\n"
	st, err := f.svc.Stage(ctx, f.repo, []string{"a.txt"}, &text)
	if err != nil {
		t.Fatal(err)
	}
	if !st.hasStaged() {
		t.Fatalf("not staged %+v", st.Files)
	}
	if got := git(t, f.dir, "show", ":a.txt"); got != strings.TrimSpace(text) {
		t.Fatalf("index %q", got)
	}
	idx, err := f.svc.ReadIndex(ctx, f.repo, "a.txt")
	if err != nil || !idx.Exists || idx.Text != text {
		t.Fatalf("read %+v %v", idx, err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.dir, "a.txt")); string(got) != "1 new\n2\n3 new\n" {
		t.Fatalf("work tree changed %q", got)
	}
	// committing the staged part leaves the other change in the work tree
	if _, err := f.svc.Commit(ctx, f.repo, CommitRequest{Message: "first part", Staged: true}); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.dir, "show", "HEAD:a.txt"); got != strings.TrimSpace(text) {
		t.Fatalf("committed %q", got)
	}
	if got := git(t, f.dir, "diff", "--numstat"); !strings.HasPrefix(got, "1\t1\ta.txt") {
		t.Fatalf("rest %q", got)
	}
	if _, err := f.svc.Commit(ctx, f.repo, CommitRequest{Message: "nothing", Staged: true}); err == nil {
		t.Fatal("nothing staged passed")
	}

	// a whole file, then out again
	if _, err := f.svc.Stage(ctx, f.repo, []string{"a.txt"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.dir, "diff", "--cached", "--numstat"); !strings.HasPrefix(got, "1\t1\ta.txt") {
		t.Fatalf("staged %q", got)
	}
	if _, err := f.svc.Unstage(ctx, f.repo, []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.dir, "diff", "--cached", "--numstat"); got != "" {
		t.Fatalf("still staged %q", got)
	}

	// a new file keeps a plain mode, an executable stays one
	nt := "new\n"
	if _, err := f.svc.Stage(ctx, f.repo, []string{"new.txt"}, &nt); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.dir, "ls-files", "-s", "new.txt"); !strings.HasPrefix(got, "100644 ") {
		t.Fatalf("mode %q", got)
	}
	write(t, f.dir, "run.sh", "#!/bin/sh\n")
	os.Chmod(filepath.Join(f.dir, "run.sh"), 0o755)
	git(t, f.dir, "add", "run.sh")
	x := "#!/bin/sh\necho\n"
	if _, err := f.svc.Stage(ctx, f.repo, []string{"run.sh"}, &x); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.dir, "ls-files", "-s", "run.sh"); !strings.HasPrefix(got, "100755 ") {
		t.Fatalf("mode %q", got)
	}
	for _, bad := range []string{"../x", "/etc/passwd"} {
		if _, err := f.svc.Stage(ctx, f.repo, []string{bad}, nil); err == nil {
			t.Errorf("%s passed", bad)
		}
	}
}

func TestStash(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	ctx := context.Background()
	if list, err := f.svc.Stashes(ctx, f.repo); err != nil || len(list) != 0 {
		t.Fatalf("empty %+v %v", list, err)
	}
	write(t, f.dir, "README.md", "changed\n")
	write(t, f.dir, "new.txt", "n\n")
	if _, err := f.svc.StashSave(ctx, f.repo, "work in progress", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("the new file stayed")
	}
	list, err := f.svc.Stashes(ctx, f.repo)
	if err != nil || len(list) != 1 || list[0].Ref != "stash@{0}" || !strings.Contains(list[0].Message, "work in progress") || list[0].At.IsZero() {
		t.Fatalf("list %+v %v", list, err)
	}
	d, err := f.svc.CommitDetail(ctx, f.repo, list[0].Hash)
	if err != nil || len(d.Files) != 1 || d.Files[0].Path != "README.md" {
		t.Fatalf("detail %+v %v", d, err)
	}
	if _, err := f.svc.StashAct(ctx, f.repo, "apply", "stash@{0}"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.dir, "README.md")); string(got) != "changed\n" {
		t.Fatalf("applied %q", got)
	}
	if list, _ := f.svc.Stashes(ctx, f.repo); len(list) != 1 {
		t.Fatal("apply dropped it")
	}
	git(t, f.dir, "checkout", "-q", "--", ".")
	os.Remove(filepath.Join(f.dir, "new.txt"))
	if _, err := f.svc.StashAct(ctx, f.repo, "pop", "stash@{0}"); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.svc.Stashes(ctx, f.repo); len(list) != 0 {
		t.Fatal("pop left it")
	}
	if _, err := f.svc.StashSave(ctx, f.repo, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.StashAct(ctx, f.repo, "drop", "stash@{0}"); err != nil {
		t.Fatal(err)
	}
	for ref, action := range map[string]string{"HEAD": "apply", "stash@{0};id": "pop", "stash@{1}": "explode"} {
		if _, err := f.svc.StashAct(ctx, f.repo, action, ref); err == nil {
			t.Errorf("%s %s passed", action, ref)
		}
	}
}

func TestCommitOps(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	ctx := context.Background()
	write(t, f.dir, "a.txt", "base\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "base")
	base := git(t, f.dir, "rev-parse", "HEAD")
	git(t, f.dir, "checkout", "-q", "-b", "side")
	write(t, f.dir, "side.txt", "s\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "side file")
	sideFile := git(t, f.dir, "rev-parse", "HEAD")
	write(t, f.dir, "a.txt", "from side\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "side edit")
	sideEdit := git(t, f.dir, "rev-parse", "HEAD")
	git(t, f.dir, "checkout", "-q", "main")
	write(t, f.dir, "a.txt", "from main\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "main edit")

	// cherry-pick without trouble
	if _, err := f.svc.DoCommitOp(ctx, f.repo, CommitOp{Op: "cherry-pick", Ref: sideFile}); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.dir, "log", "-1", "--format=%s"); got != "side file" {
		t.Fatalf("picked %q", got)
	}
	// revert it
	if _, err := f.svc.DoCommitOp(ctx, f.repo, CommitOp{Op: "revert", Ref: "HEAD"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "side.txt")); !os.IsNotExist(err) {
		t.Fatal("revert left the file")
	}
	// a conflict is no failure: the status says what is under way
	st, err := f.svc.DoCommitOp(ctx, f.repo, CommitOp{Op: "cherry-pick", Ref: sideEdit})
	if err != nil {
		t.Fatal(err)
	}
	if st.Operation != "cherry-pick" || st.Conflicts != 1 {
		t.Fatalf("conflict %+v", st)
	}
	if st, err = f.svc.Operation(ctx, f.repo, "abort"); err != nil || st.Operation != "" || st.Conflicts != 0 {
		t.Fatalf("abort %+v %v", st, err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.dir, "a.txt")); string(got) != "from main\n" {
		t.Fatalf("after abort %q", got)
	}
	// merge with a conflict, resolved in a file and continued
	st, err = f.svc.DoCommitOp(ctx, f.repo, CommitOp{Op: "merge", Ref: "side"})
	if err != nil || st.Operation != "merge" || !st.Merging || st.Conflicts != 1 {
		t.Fatalf("merge %+v %v", st, err)
	}
	if _, err := f.svc.Operation(ctx, f.repo, "continue"); err == nil {
		t.Fatal("continued with a conflict")
	}
	write(t, f.dir, "a.txt", "both\n")
	if _, err := f.svc.Stage(ctx, f.repo, []string{"a.txt"}, nil); err != nil {
		t.Fatal(err)
	}
	if st, err = f.svc.Operation(ctx, f.repo, "continue"); err != nil || st.Operation != "" {
		t.Fatalf("continue %+v %v", st, err)
	}
	if got := git(t, f.dir, "log", "-1", "--format=%p"); len(strings.Fields(got)) != 2 {
		t.Fatalf("not a merge %q", got)
	}
	if _, err := f.svc.Operation(ctx, f.repo, "abort"); err == nil {
		t.Fatal("aborted nothing")
	}
	// checkout refuses to lose work
	write(t, f.dir, "a.txt", "dirty\n")
	if _, err := f.svc.DoCommitOp(ctx, f.repo, CommitOp{Op: "checkout", Ref: base}); err == nil {
		t.Fatal("checkout over a dirty file")
	}
	// resets
	if _, err := f.svc.DoCommitOp(ctx, f.repo, CommitOp{Op: "reset", Ref: "HEAD~1", Mode: "bad"}); err == nil {
		t.Fatal("bad mode")
	}
	if _, err := f.svc.DoCommitOp(ctx, f.repo, CommitOp{Op: "reset", Ref: "HEAD", Mode: "hard"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.dir, "a.txt")); string(got) != "both\n" {
		t.Fatalf("hard reset %q", got)
	}
	head := git(t, f.dir, "rev-parse", "HEAD")
	if _, err := f.svc.DoCommitOp(ctx, f.repo, CommitOp{Op: "reset", Ref: base, Mode: "soft"}); err != nil {
		t.Fatal(err)
	}
	if git(t, f.dir, "rev-parse", "HEAD") != base || git(t, f.dir, "diff", "--cached", "--name-only") == "" {
		t.Fatal("soft reset")
	}
	git(t, f.dir, "reset", "-q", "--hard", head)
	// detached HEAD on a commit
	if st, err = f.svc.DoCommitOp(ctx, f.repo, CommitOp{Op: "checkout", Ref: base}); err != nil || !st.Detached {
		t.Fatalf("checkout %+v %v", st, err)
	}
	git(t, f.dir, "checkout", "-q", "main")
	// tags
	if _, err := f.svc.CreateTag(ctx, f.repo, "v1", base, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateTag(ctx, f.repo, "v2", "HEAD", "the second"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.dir, "cat-file", "-t", "v2"); got != "tag" {
		t.Fatalf("annotated %q", got)
	}
	if git(t, f.dir, "rev-parse", "v1") != base {
		t.Fatal("v1")
	}
	var ge *Error
	if _, err := f.svc.CreateTag(ctx, f.repo, "v1", base, ""); !errors.As(err, &ge) || ge.Code != "exists" {
		t.Fatalf("exists %v", err)
	}
	if _, err := f.svc.CreateTag(ctx, f.repo, "bad name", base, ""); !errors.As(err, &ge) || ge.Code != "bad_ref" {
		t.Fatalf("bad name %v", err)
	}
	for _, op := range []CommitOp{{Op: "explode", Ref: "HEAD"}, {Op: "merge", Ref: "--abort"}, {Op: "checkout", Ref: "a b"}} {
		if _, err := f.svc.DoCommitOp(ctx, f.repo, op); err == nil {
			t.Errorf("%+v passed", op)
		}
	}
}

// A gh that writes down what it is asked and answers from a script: no call leaves the machine.
func scriptedGh(t *testing.T, script string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "gh")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n" + script + "\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestPullRequests(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	ctx := context.Background()
	bare := t.TempDir()
	git(t, bare, "init", "-q", "--bare")
	git(t, f.dir, "remote", "add", "origin", bare)
	git(t, f.dir, "checkout", "-q", "-b", "feature")
	bin, log := scriptedGh(t, `case "$1 $2" in
"pr list") echo '[{"number":7,"title":"Add a thing","url":"https://github.example/o/r/pull/7","author":{"login":"ann"},"headRefName":"feature","baseRefName":"main","isDraft":false,"updatedAt":"2026-11-03T10:00:00Z","reviewDecision":"REVIEW_REQUIRED"}]' ;;
"pr view") echo '{"number":7,"title":"Add a thing","url":"https://github.example/o/r/pull/7","author":{"login":"ann"},"headRefName":"feature","baseRefName":"main","isDraft":false,"updatedAt":"2026-11-03T10:00:00Z","reviewDecision":"","body":"Why it is needed","state":"OPEN","additions":3,"deletions":1,"changedFiles":2,"mergeable":"MERGEABLE","createdAt":"2026-11-02T10:00:00Z","statusCheckRollup":[{"name":"ci","status":"COMPLETED","conclusion":"SUCCESS"},{"context":"lint","state":"FAILURE"}],"files":[{"path":"a.go","additions":3,"deletions":1}],"comments":[{"author":{"login":"bob"},"body":"nice","createdAt":"2026-11-02T11:00:00Z"}],"reviews":[{"author":{"login":"cal"},"body":"ok","state":"APPROVED","submittedAt":"2026-11-02T12:00:00Z"}]}' ;;
"pr diff") echo 'diff --git a/a.go b/a.go' ;;
"pr create") echo 'https://github.example/o/r/pull/8' ;;
"pr merge") case "$3" in 404) echo 'no pull requests found' >&2; exit 1;; esac ;;
esac`)
	f.svc.Gh = bin

	list, err := f.svc.Pulls(ctx, f.repo)
	if err != nil || len(list) != 1 || list[0].Number != 7 || list[0].Author.Login != "ann" {
		t.Fatalf("list %+v %v", list, err)
	}
	d, err := f.svc.PullRequest(ctx, f.repo, 7)
	if err != nil || d.Body != "Why it is needed" || len(d.StatusCheckRollup) != 2 || d.Reviews[0].State != "APPROVED" || d.Comments[0].Author.Login != "bob" || d.Files[0].Path != "a.go" {
		t.Fatalf("detail %+v %v", d, err)
	}
	if diff, err := f.svc.PullDiff(ctx, f.repo, 7); err != nil || !strings.Contains(diff, "a/a.go") {
		t.Fatalf("diff %q %v", diff, err)
	}
	// it pushes first, then opens; odd words stay one argument
	url, err := f.svc.CreatePull(ctx, f.repo, PullCreate{Title: "Fix $(id) \"now\"", Body: "line one\n`rm -rf`", Base: "main", Draft: true})
	if err != nil || url != "https://github.example/o/r/pull/8" {
		t.Fatalf("create %q %v", url, err)
	}
	if got := git(t, bare, "rev-parse", "--verify", "refs/heads/feature"); got == "" {
		t.Fatal("not pushed")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "pr create --title Fix $(id) \"now\" --body line one") || !strings.Contains(string(calls), "--base main --draft") {
		t.Fatalf("calls %s", calls)
	}
	if err := f.svc.ReviewPull(ctx, f.repo, 7, "approve", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReviewPull(ctx, f.repo, 7, "comment", "well done"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.MergePull(ctx, f.repo, 7, "squash", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CheckoutPull(ctx, f.repo, 7); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(log)
	for _, want := range []string{"pr review 7 --approve", "pr review 7 --comment --body well done", "pr merge 7 --squash --delete-branch", "pr checkout 7"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("no %q in %s", want, calls)
		}
	}
	// what is refused never reaches gh
	before := string(calls)
	var ge *Error
	for name, err := range map[string]error{
		"event":   f.svc.ReviewPull(ctx, f.repo, 7, "--exec=x", "b"),
		"empty":   f.svc.ReviewPull(ctx, f.repo, 7, "comment", " "),
		"changes": f.svc.ReviewPull(ctx, f.repo, 7, "request-changes", ""),
		"number":  f.svc.ReviewPull(ctx, f.repo, -1, "approve", ""),
	} {
		if !errors.As(err, &ge) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.svc.MergePull(ctx, f.repo, 7, "--admin", false); !errors.As(err, &ge) {
		t.Errorf("method: %v", err)
	}
	if _, err := f.svc.CreatePull(ctx, f.repo, PullCreate{Title: " "}); !errors.As(err, &ge) {
		t.Errorf("title: %v", err)
	}
	calls, _ = os.ReadFile(log)
	if string(calls) != before {
		t.Fatalf("gh was called: %s", string(calls)[len(before):])
	}

	// what gh says comes back as something to act on
	for script, code := range map[string]string{
		"echo 'To get started with GitHub CLI, please run:  gh auth login' >&2; exit 4": "gh_auth",
		"echo 'no git remotes found' >&2; exit 1":                                       "no_github_remote",
		"echo 'something else' >&2; exit 1":                                             "gh_failed",
	} {
		bin, _ := scriptedGh(t, script)
		f.svc.Gh = bin
		if _, err := f.svc.Pulls(ctx, f.repo); !errors.As(err, &ge) || ge.Code != code {
			t.Errorf("%s: %v", code, err)
		}
	}
	f.svc.Gh = filepath.Join(t.TempDir(), "no-such-gh")
	if _, err := f.svc.Pulls(ctx, f.repo); !errors.As(err, &ge) || ge.Code != "gh_missing" {
		t.Errorf("missing: %v", err)
	}
}

// A repository that arrives with its .git/config (archive, shared folder) must not
// run anything when it is only looked at. Each case first proves that plain git runs
// the command, so a passing test means the hub stopped it.
func TestReadsRunNothingFromRepoConfig(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	marks := t.TempDir()
	hook := func(name string) string {
		p := filepath.Join(marks, name+".sh")
		body := "#!/bin/sh\ntouch '" + filepath.Join(marks, name+".ran") + "'\ncat >/dev/null 2>&1\nexit 0\n"
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ran := func() []string {
		l, _ := filepath.Glob(filepath.Join(marks, "*.ran"))
		for _, p := range l {
			os.Remove(p)
		}
		slices.Sort(l)
		for i, p := range l {
			l[i] = strings.TrimSuffix(filepath.Base(p), ".ran")
		}
		return l
	}
	inc := filepath.Join(marks, "included.cfg")
	if err := os.WriteFile(inc, []byte("[filter \"inc\"]\n\tclean = "+hook("included")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	write(t, f.dir, ".gitattributes", "tracked.txt filter=evil\nstar.txt filter=*\ninc.txt filter=inc\n")
	for _, n := range []string{"tracked.txt", "star.txt", "inc.txt"} {
		write(t, f.dir, n, "one\n")
	}
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "files")
	// A file git would glob "*" to if the driver name were expanded.
	write(t, f.dir, "filter.decoy.clean", "x\n")

	// A signed commit: log.showSignature hands it to gpg.program.
	head := strings.TrimSpace(git(t, f.dir, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(git(t, f.dir, "rev-parse", "HEAD^{tree}"))
	raw := "tree " + tree + "\nparent " + head + "\nauthor T <t@example.invalid> 1700000000 +0000\ncommitter T <t@example.invalid> 1700000000 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n abc\n -----END PGP SIGNATURE-----\n\nsigned\n"
	obj := filepath.Join(marks, "commit.txt")
	if err := os.WriteFile(obj, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	signed := strings.TrimSpace(git(t, f.dir, "hash-object", "-t", "commit", "-w", obj))
	git(t, f.dir, "update-ref", "refs/heads/main", signed)

	for k, v := range map[string]string{
		"core.fsmonitor":    hook("fsmonitor"),
		"filter.evil.clean": hook("filter"),
		"filter.*.clean":    hook("star"),
		"log.showSignature": "true",
		"gpg.program":       hook("gpg"),
		"include.path":      inc,
	} {
		git(t, f.dir, "config", k, v)
	}

	// Same size: a size change alone tells git the file differs, without the filter.
	dirty := func() {
		for _, n := range []string{"tracked.txt", "star.txt", "inc.txt"} {
			write(t, f.dir, n, "two\n")
		}
	}
	dirty()
	git(t, f.dir, "status", "--porcelain")
	git(t, f.dir, "log", "-n", "1", "--format=%H")
	if got, want := ran(), []string{"filter", "fsmonitor", "gpg", "included", "star"}; !slices.Equal(got, want) {
		t.Fatalf("plain git ran %v, want %v: the test proves nothing", got, want)
	}

	dirty()
	st := f.status()
	if got := ran(); len(got) > 0 {
		t.Errorf("Status ran %v", got)
	}
	if st.Changed == 0 {
		t.Errorf("Status lost the changes: %+v", st)
	}
	if _, err := f.svc.Diff(ctx(t), f.repo, "tracked.txt", false); err != nil {
		t.Fatal(err)
	}
	if got := ran(); len(got) > 0 {
		t.Errorf("Diff ran %v", got)
	}
	if l, err := f.svc.Log(ctx(t), f.repo, 5); err != nil || len(l) == 0 {
		t.Fatalf("log %v %v", l, err)
	}
	if _, err := f.svc.LogGraph(ctx(t), f.repo, GraphOptions{Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if got := ran(); len(got) > 0 {
		t.Errorf("Log ran %v", got)
	}
	if _, err := f.svc.Repos(ctx(t), f.repo); err != nil {
		t.Fatal(err)
	}
	if got := ran(); len(got) > 0 {
		t.Errorf("Repos ran %v", got)
	}
	host, _ := f.svc.Host(machine.LocalID)
	if info := machine.GitStatus(ctx(t), host, f.dir); !info.IsRepo || info.Error != "" {
		t.Fatalf("home screen status: %+v", info)
	}
	if got := ran(); len(got) > 0 {
		t.Errorf("home screen status ran %v", got)
	}

	// Staging is an explicit action and keeps the repo's own filter.
	git(t, f.dir, "add", "tracked.txt")
	if got := ran(); !slices.Contains(got, "filter") {
		t.Errorf("git add should run the filter, ran %v", got)
	}
}

// A driver from the user's own config, such as git-lfs, keeps working.
func TestReadsKeepGlobalFilters(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	mark := filepath.Join(t.TempDir(), "global.ran")
	cfg := os.Getenv("GIT_CONFIG_GLOBAL")
	git(t, f.dir, "config", "--file", cfg, "filter.lfslike.clean", "touch '"+mark+"'; cat")
	write(t, f.dir, ".gitattributes", "data.bin filter=lfslike\n")
	write(t, f.dir, "data.bin", "one\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "data")
	os.Remove(mark)
	write(t, f.dir, "data.bin", "two\n")
	f.status()
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("the global filter did not run on status")
	}
}
