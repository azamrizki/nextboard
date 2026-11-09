package gitsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sparse writes a file over the limit without using the disk.
func sparse(t *testing.T, dir, rel string, size int64) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

// largeRepo has a pushed base, then three unpushed commits: one with work, one
// with only a big file, one with work again.
func largeRepo(t *testing.T) (*fixture, string) {
	t.Helper()
	f, bare, _ := remoteFixture(t)
	git(t, f.dir, "push", "-q", "-u", "origin", "main")
	write(t, f.dir, "work1.txt", "one\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "first work")
	sparse(t, f.dir, "data/dump bin", LargeBytes+1)
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "add the dump")
	write(t, f.dir, "work2.txt", "two\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "second work\n\nwith a body\nand two lines")
	return f, bare
}

func TestLargeHistoryScan(t *testing.T) {
	f, _ := largeRepo(t)
	h, err := f.svc.ScanLargeHistory(ctx(t), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if h.Head != git(t, f.dir, "rev-parse", "HEAD") || h.Unpushed != 3 || h.Detached {
		t.Fatalf("scan = %+v", h)
	}
	if len(h.Files) != 1 || h.Files[0].Path != "data/dump bin" || h.Files[0].SizeBytes != LargeBytes+1 || h.Files[0].Commits != 2 || h.Files[0].InPushed {
		t.Fatalf("files = %+v", h.Files)
	}
}

// One blob under two names: both have to be found, or the push fails again.
func TestLargeHistoryScanFindsEveryName(t *testing.T) {
	f, _ := largeRepo(t)
	if err := os.Link(filepath.Join(f.dir, "data/dump bin"), filepath.Join(f.dir, "copy.bin")); err != nil {
		t.Fatal(err)
	}
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "same bytes, other name")
	h, err := f.svc.ScanLargeHistory(ctx(t), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Files) != 2 {
		t.Fatalf("files = %+v", h.Files)
	}
}

func TestLargeHistoryScanOddName(t *testing.T) {
	f, _ := largeRepo(t)
	name := "odd\tname\nwith \"quotes\".bin"
	sparse(t, f.dir, name, LargeBytes+5)
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "odd")
	h, err := f.svc.ScanLargeHistory(ctx(t), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	var got bool
	for _, x := range h.Files {
		got = got || x.Path == name
	}
	if !got {
		t.Fatalf("files = %+v", h.Files)
	}
	// The API takes no control characters in a path, here as everywhere.
	if _, err := f.svc.RemoveLargeFromHistory(ctx(t), f.repo, h.Head, []string{name}); code(err) != "bad_path" {
		t.Fatalf("control characters: %v", err)
	}
}

// Spaces, quotes and shell characters reach git as one literal path.
func TestLargeHistoryRemoveShellCharacters(t *testing.T) {
	f, _ := largeRepo(t)
	name := `we"ird $(touch pwned) *.bin`
	sparse(t, f.dir, name, LargeBytes+5)
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "odd")
	head := git(t, f.dir, "rev-parse", "HEAD")
	res, err := f.svc.RemoveLargeFromHistory(ctx(t), f.repo, head, []string{name, "data/dump bin"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != 2 || git(t, f.dir, "log", "--format=%h", "--", name) != "" {
		t.Fatalf("result: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "pwned")); err == nil {
		t.Fatal("a path ran as a command")
	}
	if ignored, _ := os.ReadFile(filepath.Join(f.dir, ".gitignore")); !strings.Contains(string(ignored), "data/dump bin") {
		t.Fatalf(".gitignore = %q", ignored)
	}
}

func TestLargeHistoryRemove(t *testing.T) {
	f, _ := largeRepo(t)
	base := git(t, f.dir, "rev-parse", "origin/main")
	old := git(t, f.dir, "rev-parse", "HEAD")
	// A dirty work tree is the normal case, and filter-branch would refuse it.
	write(t, f.dir, "README.md", "being edited\n")
	write(t, f.dir, "staged.txt", "staged\n")
	git(t, f.dir, "add", "staged.txt")
	before := git(t, f.dir, "log", "--format=%an|%ae|%ad|%cn|%ce|%cd|%B", "--date=raw", "-1")

	res, err := f.svc.RemoveLargeFromHistory(ctx(t), f.repo, old, []string{"data/dump bin"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rewritten != 2 || res.Dropped != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got := git(t, f.dir, "rev-parse", "HEAD"); got != res.Head || got == old {
		t.Fatalf("HEAD %s, result %s, old %s", got, res.Head, old)
	}
	if subj := git(t, f.dir, "log", "--format=%s", "origin/main..HEAD"); subj != "second work\nfirst work" {
		t.Fatalf("subjects = %q", subj)
	}
	if got := git(t, f.dir, "merge-base", "HEAD", "origin/main"); got != base {
		t.Fatalf("pushed commits changed: %s != %s", got, base)
	}
	// Author, committer, dates and the full message are the old commit's.
	if after := git(t, f.dir, "log", "--format=%an|%ae|%ad|%cn|%ce|%cd|%B", "--date=raw", "-1"); after != before {
		t.Fatalf("metadata changed:\n%s\n%s", before, after)
	}
	// No commit on the branch holds the file any more.
	if out := git(t, f.dir, "log", "--format=%h", "--", "data/dump bin"); out != "" {
		t.Fatalf("still in history: %s", out)
	}
	// The file itself, the edit and the staged file are untouched.
	if fi, err := os.Stat(filepath.Join(f.dir, "data/dump bin")); err != nil || fi.Size() != LargeBytes+1 {
		t.Fatalf("file on disk: %v %v", fi, err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.dir, "README.md")); string(got) != "being edited\n" {
		t.Fatalf("edit lost: %q", got)
	}
	st := git(t, f.dir, "status", "--porcelain")
	for _, want := range []string{"M README.md", "A  staged.txt", "?? .gitignore"} {
		if !strings.Contains(st, want) {
			t.Fatalf("status lacks %q:\n%s", want, st)
		}
	}
	if strings.Contains(st, "dump") {
		t.Fatalf("dump still shows:\n%s", st)
	}
	// The old history stays reachable until someone deletes the backup.
	if got := git(t, f.dir, "rev-parse", backupRef(res.Backup)); got != old {
		t.Fatalf("backup = %s, want %s", got, old)
	}
	// And a scan now finds nothing.
	h, err := f.svc.ScanLargeHistory(ctx(t), f.repo)
	if err != nil || len(h.Files) != 0 {
		t.Fatalf("scan after: %+v %v", h, err)
	}
}

func TestLargeHistoryPushWorksAfter(t *testing.T) {
	f, bare := largeRepo(t)
	h, err := f.svc.ScanLargeHistory(ctx(t), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RemoveLargeFromHistory(ctx(t), f.repo, h.Head, []string{"data/dump bin"}); err != nil {
		t.Fatal(err)
	}
	git(t, f.dir, "push", "-q", "origin", "main")
	if out := git(t, bare, "log", "--format=%s", "main", "--", "data/dump bin"); out != "" {
		t.Fatalf("remote got the file: %s", out)
	}
	// refs/nextboard/* never travels with a push.
	if out := git(t, bare, "for-each-ref", "refs/nextboard"); out != "" {
		t.Fatalf("backup reached the remote: %s", out)
	}
}

func TestLargeHistoryKeepsMerges(t *testing.T) {
	f, _ := largeRepo(t)
	git(t, f.dir, "switch", "-qc", "side", "origin/main")
	write(t, f.dir, "side.txt", "side\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "side work")
	git(t, f.dir, "switch", "-q", "main")
	git(t, f.dir, "merge", "-q", "--no-edit", "side")
	old := git(t, f.dir, "rev-parse", "HEAD")
	res, err := f.svc.RemoveLargeFromHistory(ctx(t), f.repo, old, []string{"data/dump bin"})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Fields(git(t, f.dir, "rev-list", "--parents", "-n", "1", "HEAD")); len(n) != 3 {
		t.Fatalf("merge lost a parent: %v (%+v)", n, res)
	}
	if out := git(t, f.dir, "log", "--all", "--not", "refs/nextboard/backup/"+res.Backup, "--format=%h", "--", "data/dump bin"); out != "" {
		t.Fatalf("still in history: %s", out)
	}
	if got, _ := os.ReadFile(filepath.Join(f.dir, "side.txt")); string(got) != "side\n" {
		t.Fatalf("merge lost its side: %q", got)
	}
}

// Everything that must leave the repository exactly as it was.
func TestLargeHistoryRefuses(t *testing.T) {
	f, _ := largeRepo(t)
	head := git(t, f.dir, "rev-parse", "HEAD")

	same := func(t *testing.T) {
		t.Helper()
		if got := git(t, f.dir, "rev-parse", "HEAD"); got != head {
			t.Fatalf("HEAD moved: %s", got)
		}
		if out := git(t, f.dir, "for-each-ref", "refs/nextboard"); out != "" {
			t.Fatalf("backup left behind: %s", out)
		}
	}
	for _, tc := range []struct {
		name  string
		head  string
		paths []string
		want  string
	}{
		{"normal file", head, []string{"work1.txt"}, "not_large"},
		{"missing file", head, []string{"nope.bin"}, "not_large"},
		{"moved head", strings.Repeat("a", 40), []string{"data/dump bin"}, "head_moved"},
		{"no paths", head, nil, "no_paths"},
		{"bad head", "HEAD", []string{"data/dump bin"}, "bad_ref"},
		{"outside", head, []string{"../x"}, "bad_path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.RemoveLargeFromHistory(ctx(t), f.repo, tc.head, tc.paths)
			if code(err) != tc.want {
				t.Fatalf("code = %q (%v)", code(err), err)
			}
			same(t)
		})
	}
	t.Run("detached", func(t *testing.T) {
		git(t, f.dir, "switch", "-q", "--detach")
		defer git(t, f.dir, "switch", "-q", "main")
		_, err := f.svc.RemoveLargeFromHistory(ctx(t), f.repo, head, []string{"data/dump bin"})
		if code(err) != "detached" {
			t.Fatalf("code = %q (%v)", code(err), err)
		}
		same(t)
	})
	t.Run("merge under way", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(f.dir, ".git", "MERGE_HEAD"), []byte(head+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(f.dir, ".git", "MERGE_HEAD"))
		_, err := f.svc.RemoveLargeFromHistory(ctx(t), f.repo, head, []string{"data/dump bin"})
		if code(err) != "operation_running" {
			t.Fatalf("code = %q (%v)", code(err), err)
		}
		same(t)
	})
}

// A path the remote already has at a normal size: removing it deletes it there,
// and the scan has to say so.
func TestLargeHistoryMarksPushedPath(t *testing.T) {
	f, _, _ := remoteFixture(t)
	write(t, f.dir, "data.bin", "small\n")
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "small data")
	git(t, f.dir, "push", "-q", "-u", "origin", "main")
	sparse(t, f.dir, "data.bin", LargeBytes+1)
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-qm", "big data")
	h, err := f.svc.ScanLargeHistory(ctx(t), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Files) != 1 || !h.Files[0].InPushed {
		t.Fatalf("files = %+v", h.Files)
	}
}

func TestLargeHistoryUndo(t *testing.T) {
	f, _ := largeRepo(t)
	old := git(t, f.dir, "rev-parse", "HEAD")
	res, err := f.svc.RemoveLargeFromHistory(ctx(t), f.repo, old, []string{"data/dump bin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UndoLargeRemoval(ctx(t), f.repo, "bad id", res.Head, []string{"data/dump bin"}); code(err) != "bad_ref" {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := f.svc.UndoLargeRemoval(ctx(t), f.repo, res.Backup, strings.Repeat("b", 40), []string{"data/dump bin"}); code(err) != "head_moved" {
		t.Fatalf("moved: %v", err)
	}
	if _, err := f.svc.UndoLargeRemoval(ctx(t), f.repo, res.Backup, res.Head, []string{"data/dump bin"}); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.dir, "rev-parse", "HEAD"); got != old {
		t.Fatalf("HEAD = %s, want %s", got, old)
	}
	if st := git(t, f.dir, "status", "--porcelain"); strings.Contains(st, "dump") {
		t.Fatalf("status after undo:\n%s", st)
	}
	if out := git(t, f.dir, "for-each-ref", "refs/nextboard"); out != "" {
		t.Fatalf("backup stayed: %s", out)
	}
	if _, err := f.svc.UndoLargeRemoval(ctx(t), f.repo, res.Backup, old, []string{"data/dump bin"}); code(err) != "no_backup" {
		t.Fatalf("second undo: %v", err)
	}
}

// Undo after a new commit would throw that commit away.
func TestLargeHistoryUndoRefusesNewCommits(t *testing.T) {
	f, _ := largeRepo(t)
	old := git(t, f.dir, "rev-parse", "HEAD")
	res, err := f.svc.RemoveLargeFromHistory(ctx(t), f.repo, old, []string{"data/dump bin"})
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.dir, "later.txt", "later\n")
	git(t, f.dir, "add", "later.txt")
	git(t, f.dir, "commit", "-qm", "later")
	if _, err := f.svc.UndoLargeRemoval(ctx(t), f.repo, res.Backup, res.Head, []string{"data/dump bin"}); code(err) != "head_moved" {
		t.Fatalf("undo over a new commit: %v", err)
	}
	if got := git(t, f.dir, "log", "-1", "--format=%s"); got != "later" {
		t.Fatalf("commit lost: %s", got)
	}
}
