package review

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	t      *testing.T
	m      *Manager
	root   string
	mu     sync.Mutex
	events []map[string]any
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, root: t.TempDir(), now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	f.m = New(Config{
		Dir:   t.TempDir(),
		Roots: func(string) []Root { return []Root{{Path: f.root}} },
		Publish: func(v any) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.events = append(f.events, v.(map[string]any))
		},
		Now: func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.now
		},
	})
	t.Cleanup(f.m.Close)
	return f
}

func (f *fixture) write(rel, text string) {
	f.t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(rel string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, rel))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// start and stop say that an agent is at work; each takes its snapshot before it returns.
func (f *fixture) start(id, name string, hooks bool) {
	f.t.Helper()
	f.m.SessionChanged("p", id, name, "claude", true, hooks)
	f.wait(func(p *project) bool { _, ok := p.active[id]; return ok })
}

func (f *fixture) stop(id string) {
	f.t.Helper()
	f.m.SessionChanged("p", id, "", "", false, false)
	f.wait(func(p *project) bool { _, ok := p.active[id]; return !ok })
}

func (f *fixture) wait(ok func(*project) bool) {
	f.t.Helper()
	p := f.m.project("p")
	for i := 0; i < 500; i++ {
		p.mu.Lock()
		done := ok(p)
		p.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("timed out")
}

func (f *fixture) snap() {
	f.t.Helper()
	if err := f.m.Snapshot("p"); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) files(id string) map[string]Change {
	out := map[string]Change{}
	for _, a := range f.m.List("p").Agents {
		if a.SessionID == id {
			for _, c := range a.Files {
				out[c.Path] = c
			}
		}
	}
	return out
}

func TestWhatAnAgentChangedIsItsOwn(t *testing.T) {
	f := newFixture(t)
	f.write("keep.txt", "one\ntwo\nthree\n")
	f.write("gone.txt", "bye\n")
	f.write("mine.txt", "mine\n")
	f.snap()

	// the owner edits before the turn: not the agent's
	f.write("mine.txt", "mine, edited\n")
	f.start("s1", "@Ash", true)
	f.write("keep.txt", "one\ntwo changed\nthree\nfour\n")
	if err := os.Remove(filepath.Join(f.root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	f.write("sub dir/new é.txt", "fresh\nfile\n")
	f.stop("s1")

	got := f.files("s1")
	if len(got) != 3 {
		t.Fatalf("files %+v", got)
	}
	if c := got["keep.txt"]; c.Status != "modified" || c.Add != 2 || c.Del != 1 {
		t.Errorf("keep %+v", c)
	}
	if c := got["gone.txt"]; c.Status != "deleted" || c.Del != 1 {
		t.Errorf("gone %+v", c)
	}
	if c := got["sub dir/new é.txt"]; c.Status != "added" || c.Add != 2 {
		t.Errorf("new %+v", c)
	}
	if _, ok := got["mine.txt"]; ok {
		t.Error("the owner's edit went to the agent")
	}

	// after the turn the owner's edits are theirs again
	f.write("later.txt", "x\n")
	f.snap()
	if _, ok := f.files("s1")["later.txt"]; ok {
		t.Error("an edit after the turn went to the agent")
	}

	v, err := f.m.File("p", "s1", "keep.txt")
	if err != nil || v.Base != "one\ntwo\nthree\n" || v.Current != "one\ntwo changed\nthree\nfour\n" || !v.BaseExists || !v.CurrentExists {
		t.Fatalf("view %+v %v", v, err)
	}
	if v, err := f.m.File("p", "s1", "gone.txt"); err != nil || v.Base != "bye\n" || v.CurrentExists {
		t.Fatalf("gone %+v %v", v, err)
	}
	if v, err := f.m.File("p", "s1", "sub dir/new é.txt"); err != nil || v.BaseExists || v.Current != "fresh\nfile\n" {
		t.Fatalf("new %+v %v", v, err)
	}
	if _, err := f.m.File("p", "s1", "mine.txt"); !errors.Is(err, errNoChange) {
		t.Fatalf("no change: %v", err)
	}
	if len(f.events) == 0 {
		t.Fatal("no event")
	}
}

func TestRejectAndAccept(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "a1\n")
	f.write("b.txt", "b1\n")
	f.write("c.sh", "#!/bin/sh\n")
	os.Chmod(filepath.Join(f.root, "c.sh"), 0o755)
	f.write("d.txt", "d1\n")
	f.snap()
	f.start("s1", "@Ash", true)
	f.write("a.txt", "a2\n")
	f.write("b.txt", "b2\n")
	f.write("c.sh", "#!/bin/sh\necho hi\n")
	f.write("new.txt", "n\n")
	os.Remove(filepath.Join(f.root, "d.txt"))
	f.stop("s1")

	if err := f.m.Reject("p", "s1", "a.txt", nil); err != nil {
		t.Fatal(err)
	}
	if f.read("a.txt") != "a1\n" {
		t.Fatalf("a %q", f.read("a.txt"))
	}
	if err := f.m.Reject("p", "s1", "new.txt", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an added file came back")
	}
	if err := f.m.Reject("p", "s1", "d.txt", nil); err != nil {
		t.Fatal(err)
	}
	if f.read("d.txt") != "d1\n" {
		t.Fatal("a deleted file did not come back")
	}
	if err := f.m.Reject("p", "s1", "c.sh", nil); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(f.root, "c.sh"))
	if err != nil || st.Mode().Perm()&0o100 == 0 || f.read("c.sh") != "#!/bin/sh\n" {
		t.Fatalf("mode %v %q %v", st.Mode(), f.read("c.sh"), err)
	}
	if err := f.m.Accept("p", "s1", "b.txt", nil); err != nil {
		t.Fatal(err)
	}
	if f.read("b.txt") != "b2\n" {
		t.Fatal("accept changed the file")
	}
	if got := f.files("s1"); len(got) != 0 {
		t.Fatalf("left %+v", got)
	}
	if err := f.m.Reject("p", "s1", "b.txt", nil); !errors.Is(err, errNoChange) {
		t.Fatalf("gone: %v", err)
	}

	// what the file held before an undo stays in the history
	vs, err := f.m.History("p", "a.txt", 0)
	if err != nil || len(vs) < 2 {
		t.Fatalf("history %+v %v", vs, err)
	}
	text, _, err := f.m.Version("p", "a.txt", vs[1].Commit)
	if err != nil || text != "a2\n" {
		t.Fatalf("version %q %v", text, err)
	}
}

func TestPartOfAChange(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "1\n2\n3\n")
	f.snap()
	f.start("s1", "@Ash", true)
	f.write("a.txt", "1 new\n2\n3 new\n")
	f.stop("s1")

	// take the first part: the rest stays to review
	base := "1 new\n2\n3\n"
	if err := f.m.Accept("p", "s1", "a.txt", &base); err != nil {
		t.Fatal(err)
	}
	c := f.files("s1")["a.txt"]
	if c.Add != 1 || c.Del != 1 {
		t.Fatalf("after accept %+v", c)
	}
	v, err := f.m.File("p", "s1", "a.txt")
	if err != nil || v.Base != base {
		t.Fatalf("base %+v %v", v, err)
	}
	// undo the other part: the file now equals the new base, nothing is left
	text := "1 new\n2\n3\n"
	if err := f.m.Reject("p", "s1", "a.txt", &text); err != nil {
		t.Fatal(err)
	}
	if f.read("a.txt") != text {
		t.Fatalf("file %q", f.read("a.txt"))
	}
	if got := f.files("s1"); len(got) != 0 {
		t.Fatalf("left %+v", got)
	}
}

func TestWhoItWas(t *testing.T) {
	f := newFixture(t)
	f.write("x.txt", "x\n")
	f.write("y.txt", "y\n")
	f.write("z.txt", "z\n")
	f.write("w.txt", "w\n")
	f.snap()
	f.start("claude", "@Ash", true)
	f.start("codex", "@Bea", false)
	f.start("codex2", "@Cal", false)

	// a hook names the file: that agent alone
	f.m.Hint("p", "claude", filepath.Join(f.root, "x.txt"))
	f.write("x.txt", "x2\n")
	// nobody names it: the agents without hooks, and it is a guess between them
	f.write("y.txt", "y2\n")
	// the owner saved this one from the editor
	f.m.UserWrote(filepath.Join(f.root, "z.txt"))
	f.write("z.txt", "z2\n")
	f.snap()

	if got := f.files("claude"); len(got) != 1 || got["x.txt"].Path == "" || got["x.txt"].Unsure {
		t.Fatalf("claude %+v", got)
	}
	bea, cal := f.files("codex"), f.files("codex2")
	if _, ok := bea["x.txt"]; ok {
		t.Fatal("a named file went to another agent")
	}
	if !bea["y.txt"].Unsure || !cal["y.txt"].Unsure {
		t.Fatalf("guess %+v %+v", bea, cal)
	}
	if _, ok := bea["z.txt"]; ok {
		t.Fatal("the owner's save went to an agent")
	}

	// with only one agent without hooks at work, it is no guess
	f.stop("codex2")
	f.write("w.txt", "w2\n")
	f.snap()
	if c := f.files("codex")["w.txt"]; c.Path == "" || c.Unsure {
		t.Fatalf("one agent %+v", c)
	}
	f.stop("codex")
	f.stop("claude")
}

func TestTwoAgentsOnOneFile(t *testing.T) {
	f := newFixture(t)
	f.write("x.txt", "x\n")
	f.snap()
	f.start("a", "@Ash", true)
	f.m.Hint("p", "a", filepath.Join(f.root, "x.txt"))
	f.write("x.txt", "x from Ash\n")
	f.snap()
	f.start("b", "@Bea", true)
	f.m.Hint("p", "b", filepath.Join(f.root, "x.txt"))
	f.write("x.txt", "x from Bea\n")
	f.snap()

	ca, cb := f.files("a")["x.txt"], f.files("b")["x.txt"]
	if len(ca.Also) != 1 || ca.Also[0] != "@Bea" || len(cb.Also) != 1 || cb.Also[0] != "@Ash" {
		t.Fatalf("also %+v %+v", ca, cb)
	}
	// each compares with what it found
	if v, _ := f.m.File("p", "a", "x.txt"); v.Base != "x\n" {
		t.Fatalf("a base %q", v.Base)
	}
	if v, _ := f.m.File("p", "b", "x.txt"); v.Base != "x from Ash\n" {
		t.Fatalf("b base %q", v.Base)
	}
	f.mu.Lock()
	var overlaps int
	for _, e := range f.events {
		if e["type"] == "reviewOverlap" && e["path"] == "x.txt" {
			overlaps++
		}
	}
	f.mu.Unlock()
	if overlaps != 1 {
		t.Fatalf("overlap events %d", overlaps)
	}
	f.stop("a")
	f.stop("b")
}

func TestABackAndForthLeavesNothing(t *testing.T) {
	f := newFixture(t)
	f.write("x.txt", "x\n")
	f.snap()
	f.start("a", "@Ash", true)
	f.write("x.txt", "tmp\n")
	f.snap()
	if len(f.files("a")) != 1 {
		t.Fatal("not seen")
	}
	f.write("x.txt", "x\n")
	f.stop("a")
	if got := f.files("a"); len(got) != 0 {
		t.Fatalf("left %+v", got)
	}
}

func TestSurvivesARestart(t *testing.T) {
	f := newFixture(t)
	f.write("x.txt", "x\n")
	f.snap()
	f.start("a", "@Ash", true)
	f.write("x.txt", "x2\n")
	f.stop("a")
	dir := f.m.cfg.Dir
	again := New(Config{Dir: dir, Roots: f.m.cfg.Roots, Now: f.m.cfg.Now})
	defer again.Close()
	list := again.List("p")
	if len(list.Agents) != 1 || list.Agents[0].Name != "@Ash" || len(list.Agents[0].Files) != 1 {
		t.Fatalf("list %+v", list)
	}
	if err := again.Reject("p", "a", "x.txt", nil); err != nil {
		t.Fatal(err)
	}
	if f.read("x.txt") != "x\n" {
		t.Fatal("not restored after a restart")
	}
}

func TestWhatIsLeftOut(t *testing.T) {
	f := newFixture(t)
	f.write(".gitignore", "ignored.log\n")
	f.write("x.txt", "x\n")
	f.snap()
	f.start("a", "@Ash", true)
	f.write("ignored.log", "noise\n")
	f.write("node_modules/dep/index.js", "x\n")
	big := strings.Repeat("x", MaxFile+1)
	f.write("big.bin", big)
	f.write("x.txt", "x2\n")
	f.stop("a")
	got := f.files("a")
	if len(got) != 1 || got["x.txt"].Path == "" {
		t.Fatalf("got %+v", got)
	}
}

func TestProjectsOfSeveralRepositories(t *testing.T) {
	f := newFixture(t)
	top := f.root
	sub := filepath.Join(top, "apps", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", sub, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	f.m.cfg.Roots = func(string) []Root { return []Root{{Path: top}, {Rel: "apps/web", Path: sub}} }
	f.write("top.txt", "t\n")
	f.write("apps/web/inner.txt", "i\n")
	f.snap()
	f.start("a", "@Ash", true)
	f.write("top.txt", "t2\n")
	f.write("apps/web/inner.txt", "i2\n")
	f.stop("a")
	got := f.files("a")
	if len(got) != 2 || got["top.txt"].Path == "" || got["apps/web/inner.txt"].Path == "" {
		t.Fatalf("got %+v", got)
	}
	if v, err := f.m.File("p", "a", "apps/web/inner.txt"); err != nil || v.Base != "i\n" || v.Current != "i2\n" {
		t.Fatalf("view %+v %v", v, err)
	}
	if err := f.m.Reject("p", "a", "apps/web/inner.txt", nil); err != nil {
		t.Fatal(err)
	}
	if f.read("apps/web/inner.txt") != "i\n" {
		t.Fatal("not restored")
	}
	// the project's own repository was not touched
	if _, err := os.Stat(filepath.Join(sub, ".git", "refs", "nb")); err == nil {
		t.Fatal("the shadow wrote into the repository")
	}
}

func TestLocalHistory(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "v1\n")
	f.snap()
	f.write("a.txt", "v2\n")
	f.snap()
	f.start("s", "@Ash", true)
	f.write("a.txt", "v3\n")
	f.stop("s")
	os.Remove(filepath.Join(f.root, "a.txt"))
	vs, err := f.m.History("p", "a.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 4 {
		t.Fatalf("versions %+v", vs)
	}
	if vs[0].Exists || vs[0].Who != "You" {
		t.Fatalf("deleted first %+v", vs[0])
	}
	if vs[1].Who != "@Ash" {
		t.Fatalf("who %+v", vs[1])
	}
	for i, want := range map[int]string{1: "v3\n", 2: "v2\n", 3: "v1\n"} {
		text, _, err := f.m.Version("p", "a.txt", vs[i].Commit)
		if err != nil || text != want {
			t.Fatalf("%d: %q %v", i, text, err)
		}
	}
	for _, bad := range []string{"", "HEAD", "abc", strings.Repeat("z", 40), vs[0].Commit + ":x"} {
		if _, _, err := f.m.Version("p", "a.txt", bad); !errors.Is(err, errNoVersion) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, bad := range []string{"../x", "a/../../x", "/etc/passwd"} {
		if _, err := f.m.History("p", bad, 0); !errors.Is(err, errBadPath) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if vs, err := f.m.History("p", "never.txt", 0); err != nil || len(vs) != 0 {
		t.Fatalf("never %+v %v", vs, err)
	}
}

func TestHistoryIsPruned(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "old\n")
	f.write("b.txt", "b\n")
	sh := f.m.project("p").shadowOf(f.root)
	f.snap()
	// a pending change from long ago keeps its base
	f.start("s", "@Ash", true)
	f.write("b.txt", "b2\n")
	f.stop("s")
	// everything so far is older than the history keeps
	run := func(args ...string) string {
		out, err := sh.git(t.Context(), nil, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	oldVersions, err := f.m.History("p", "a.txt", 0)
	if err != nil || len(oldVersions) != 1 {
		t.Fatalf("old %+v %v", oldVersions, err)
	}
	f.write("a.txt", "new\n")
	f.snap()
	f.mu.Lock()
	f.now = time.Now().Add(HistoryAge + 48*time.Hour)
	f.mu.Unlock()
	f.write("a.txt", "newer\n")
	f.snap()
	sh.prune(t.Context(), time.Now().Add(HistoryAge+time.Hour))
	_ = run("rev-parse", historyRef)
	if _, _, err := f.m.Version("p", "a.txt", oldVersions[0].Commit); err == nil {
		// everything is too old by then except what the snapshot just wrote, which git dates now
		t.Log("the oldest commit may stay as the shallow boundary")
	}
	if v, err := f.m.File("p", "s", "b.txt"); err != nil || v.Base != "b\n" {
		t.Fatalf("a pending base was lost: %+v %v", v, err)
	}
}

func TestNotOnThisPC(t *testing.T) {
	f := newFixture(t)
	f.m.cfg.Roots = func(string) []Root { return nil }
	if s := f.m.List("p"); s.Supported {
		t.Fatal("supported")
	}
	if err := f.m.Snapshot("p"); !errors.Is(err, errUnsupported) {
		t.Fatal(err)
	}
	if _, err := f.m.History("p", "a", 0); !errors.Is(err, errUnsupported) {
		t.Fatal(err)
	}
}

// The day between prunes counts across restarts of the hub, and the first snapshot of a shadow
// packs what it wrote.
func TestPruneTimeOutlivesTheHub(t *testing.T) {
	f := newFixture(t)
	for i := range 30 {
		f.write(fmt.Sprintf("f%d.txt", i), strings.Repeat("line\n", i+1))
	}
	p := f.m.project("p")
	sh := p.shadowOf(f.root)
	f.snap()
	mark := filepath.Join(sh.gitDir, prunedMark)
	st, err := os.Stat(mark)
	if err != nil {
		t.Fatalf("no mark after the first snapshot: %v", err)
	}
	first := st.ModTime()
	loose := func() int {
		n := 0
		_ = filepath.WalkDir(filepath.Join(sh.gitDir, "objects"), func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && !strings.Contains(path, string(filepath.Separator)+"pack"+string(filepath.Separator)) && !strings.Contains(path, "info") {
				n++
			}
			return nil
		})
		return n
	}
	if n := loose(); n > 5 {
		t.Fatalf("%d loose objects after the first snapshot's prune", n)
	}
	// a restart: nothing in memory, the mark says it was pruned an hour ago
	later := func(d time.Duration) {
		f.mu.Lock()
		f.now = f.now.Add(d)
		f.mu.Unlock()
		p.mu.Lock()
		p.pruned = map[string]time.Time{}
		p.mu.Unlock()
	}
	later(time.Hour)
	f.write("f0.txt", "changed\n")
	f.snap()
	if st, _ := os.Stat(mark); !st.ModTime().Equal(first) {
		t.Fatalf("pruned again after an hour: %v, was %v", st.ModTime(), first)
	}
	later(pruneEvery)
	f.write("f0.txt", "changed again\n")
	f.snap()
	if st, _ := os.Stat(mark); !st.ModTime().After(first) {
		t.Fatal("not pruned a day later")
	}
}
