package gitsvc

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// commitAt makes a commit whose committer time is at.
func (f *fixture) commitAt(at time.Time, file, msg string) {
	f.t.Helper()
	write(f.t, f.dir, file, msg+"\n")
	git(f.t, f.dir, "add", "-A")
	cmd := exec.Command("git", "commit", "-qm", msg)
	cmd.Dir = f.dir
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+at.Format(time.RFC3339), "GIT_AUTHOR_DATE="+at.Format(time.RFC3339), "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("commit: %v %s", err, out)
	}
}

func TestCommitsSince(t *testing.T) {
	f := newFixture(t, true)
	// a repository without commits has nothing since
	git(t, f.dir, "init", "-q", "-b", "main")
	got, err := f.svc.CommitsSince(ctx(t), f.repo, time.Now().Add(-time.Hour), 20)
	if err != nil || got.Total != 0 || len(got.Commits) != 0 {
		t.Fatalf("empty repo: %+v %v", got, err)
	}
	since := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	f.commitAt(since.Add(-48*time.Hour), "old.txt", "old work")
	f.commitAt(since.Add(time.Hour), "a.txt", "add a")
	f.commitAt(since.Add(2*time.Hour), "b.txt", "add b")
	f.commitAt(since.Add(3*time.Hour), "a.txt", "change a again")
	got, err = f.svc.CommitsSince(ctx(t), f.repo, since, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || got.Files != 2 || len(got.Commits) != 2 || got.Commits[0].Subject != "change a again" || got.Commits[1].Subject != "add b" {
		t.Fatalf("since: %+v", got)
	}
	if got, err := f.svc.CommitsSince(ctx(t), f.repo, since.Add(4*time.Hour), 20); err != nil || got.Total != 0 || got.Files != 0 {
		t.Fatalf("nothing after: %+v %v", got, err)
	}
	// a folder that is no repository
	other := f.repo
	other.Path = t.TempDir()
	if _, err := f.svc.CommitsSince(ctx(t), other, since, 20); code(err) != "not_repo" {
		t.Fatalf("not a repo: %v", err)
	}
}
