package studio

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "add", ".")
	runGit(t, dir, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-qm", "x")
}

func listed(t *testing.T, root string) *FileList {
	t.Helper()
	fl, err := ListFiles(context.Background(), filesHost(t), root)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(fl.Items)
	return fl
}

func TestListFilesFollowsEachRepositoryBelowTheFolder(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"readme.txt": "x", "plain/note.txt": "x", "plain/debug.log": "x", "node_modules/m/i.js": "x",
		"app a/.gitignore": "*.log\nout/\n", "app a/main.go": "x", "app a/debug.log": "x", "app a/out/big.bin": "x", "app a/gone.go": "x",
		"group/lib/.gitignore": "secret.env\n", "group/lib/lib.go": "x", "group/lib/secret.env": "x",
	})
	gitInit(t, filepath.Join(root, "app a"))
	commitAll(t, filepath.Join(root, "app a"))
	if err := os.Remove(filepath.Join(root, "app a", "gone.go")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{"app a/new.go": "x"}) // untracked, not ignored
	gitInit(t, filepath.Join(root, "group", "lib"))

	fl := listed(t, root)
	want := []string{
		"app a/.gitignore", "app a/main.go", "app a/new.go",
		"group/lib/.gitignore", "group/lib/lib.go",
		"plain/debug.log", "plain/note.txt", "readme.txt",
	}
	if fl.Source != "walk" || fl.Truncated || !slices.Equal(fl.Items, want) {
		t.Fatalf("source %q truncated %v\ngot  %q\nwant %q", fl.Source, fl.Truncated, fl.Items, want)
	}
}

func TestListFilesWithARepositoryInsideARepositoryBelowTheFolder(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"outer/a.go": "x", "outer/inner/.gitignore": "*.tmp\n", "outer/inner/b.go": "x", "outer/inner/c.tmp": "x"})
	gitInit(t, filepath.Join(root, "outer"))
	gitInit(t, filepath.Join(root, "outer", "inner"))
	fl := listed(t, root)
	want := []string{"outer/a.go", "outer/inner/.gitignore", "outer/inner/b.go"}
	if !slices.Equal(fl.Items, want) {
		t.Fatalf("got %q, want %q", fl.Items, want)
	}
}

// A stray .git folder is no repository: its files must not vanish from the list.
func TestListFilesKeepsAFolderWithAStrayGit(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"stray/.git/junk": "x", "stray/file.txt": "x"})
	fl := listed(t, root)
	if !slices.Contains(fl.Items, "stray/file.txt") || slices.Contains(fl.Items, "stray/.git/junk") {
		t.Fatalf("items %q", fl.Items)
	}
}

// Listing a folder must never run code from a repository's config below it.
func TestListFilesRunsNothingFromARepositoryBelowTheFolder(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	writeTree(t, root, map[string]string{"app/a.go": "x"})
	gitInit(t, repo)
	commitAll(t, repo)
	marker := filepath.Join(root, "ran")
	script := filepath.Join(root, "evil.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "core.fsmonitor", script)
	fl := listed(t, root)
	if !slices.Contains(fl.Items, "app/a.go") {
		t.Fatalf("items %q", fl.Items)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's core.fsmonitor ran")
	}
}

func TestRipgrepFollowsEachRepositoryBelowTheFolder(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("no ripgrep")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"app/.gitignore": "secret.env\n", "app/a.go": "needle\n", "app/secret.env": "needle\n", "plain.txt": "needle\n"})
	gitInit(t, filepath.Join(root, "app"))
	found, err := Search(context.Background(), filesHost(t), root, SearchOptions{Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range found.Files {
		rels = append(rels, f.Rel)
	}
	slices.Sort(rels)
	if want := []string{"app/a.go", "plain.txt"}; !slices.Equal(rels, want) {
		t.Fatalf("got %q, want %q", rels, want)
	}
}

// Whichever search tool the machine has: finding nothing next to a folder that
// cannot be read is an empty result, not a failed search or a bad pattern.
func TestSearchSurvivesAnUnreadableFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "needle\n", "data/secret": "needle\n"})
	data := filepath.Join(root, "data")
	if err := os.Chmod(data, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(data, 0o755) })
	for _, regex := range []bool{false, true} {
		found, err := Search(context.Background(), filesHost(t), root, SearchOptions{Query: "needle", Regex: regex})
		if err != nil || len(found.Files) != 1 || found.Files[0].Rel != "a.txt" {
			t.Errorf("regex %v: %+v err=%v", regex, found, err)
		}
		none, err := Search(context.Background(), filesHost(t), root, SearchOptions{Query: "nothing-like-this", Regex: regex})
		if err != nil || len(none.Files) != 0 {
			t.Errorf("regex %v, no match: %+v err=%v", regex, none, err)
		}
	}
}

// A folder the user cannot read, like a database volume owned by root, costs
// only that folder.
func TestListFilesSurvivesAnUnreadableFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "x", "data/secret": "x", "app/b.go": "x"})
	gitInit(t, filepath.Join(root, "app"))
	data := filepath.Join(root, "data")
	if err := os.Chmod(data, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(data, 0o755) })

	fl, err := ListFiles(context.Background(), filesHost(t), root)
	if err != nil {
		t.Fatalf("one unreadable folder failed the whole list: %v", err)
	}
	got := " " + strings.Join(fl.Items, " ") + " "
	if !strings.Contains(got, " a.txt ") || !strings.Contains(got, " app/b.go ") {
		t.Fatalf("items %q", fl.Items)
	}
}
