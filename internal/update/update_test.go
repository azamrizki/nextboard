package update_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/update"
	"github.com/azamrizki/nextboard/internal/update/updatetest"
)

const repo = "example-owner/nextboard"

func TestCheckFollowsTheChannel(t *testing.T) {
	gh := updatetest.New(t, repo)
	gh.Publish(t, "v0.8.1", false, []byte("old"))
	gh.Publish(t, "v0.9.0-beta.2", true, []byte("b2"))
	gh.Publish(t, "v0.9.0-beta.3", true, []byte("b3"))
	gh.Draft("v0.9.0-beta.9")
	c := gh.Client()
	ctx := context.Background()

	// a stable build looks only at the latest stable release
	ch, rel := c.CheckFor(ctx, "0.8.1")
	if ch.Status != update.StatusCurrent || ch.Latest != "v0.8.1" || rel == nil || ch.Channel != "stable" {
		t.Fatalf("stable on the latest: %+v", ch)
	}
	ch, _ = c.CheckFor(ctx, "0.8.0")
	if ch.Status != update.StatusAvailable || ch.Latest != "v0.8.1" || ch.Command != "nextboard update" || !strings.HasSuffix(ch.URL, "/releases/tag/v0.8.1") {
		t.Fatalf("stable behind: %+v", ch)
	}
	// a beta follows the newest beta, never a draft
	ch, rel = c.CheckFor(ctx, "0.9.0-beta.2")
	if ch.Status != update.StatusAvailable || ch.Latest != "v0.9.0-beta.3" || ch.Command != "nextboard update --version v0.9.0-beta.3" || ch.Channel != "beta" {
		t.Fatalf("beta behind: %+v", ch)
	}
	if len(rel.Assets) != 2 {
		t.Fatalf("assets of the beta: %+v", rel.Assets)
	}
	ch, _ = c.CheckFor(ctx, "0.9.0-beta.3")
	if ch.Status != update.StatusCurrent {
		t.Fatalf("beta on the newest: %+v", ch)
	}
	// ... and the stable release that follows it
	gh.Publish(t, "v0.9.0", false, []byte("stable"))
	ch, _ = c.CheckFor(ctx, "0.9.0-beta.3")
	if ch.Status != update.StatusAvailable || ch.Latest != "v0.9.0" || ch.Command != "nextboard update" {
		t.Fatalf("beta after its release: %+v", ch)
	}
	// never a step back: a beta ahead of every release is current
	ch, _ = c.CheckFor(ctx, "0.10.0-beta.1")
	if ch.Status != update.StatusCurrent {
		t.Fatalf("beta ahead: %+v", ch)
	}

	// a dev build asks nothing at all
	before := len(gh.Requests())
	ch, rel = c.CheckFor(ctx, "0.1.0-dev")
	if ch.Status != update.StatusDev || rel != nil || len(gh.Requests()) != before {
		t.Fatalf("dev: %+v, %d requests", ch, len(gh.Requests())-before)
	}
	ch, _ = c.CheckFor(ctx, "v0.8.1-7-g10ab3f5-dirty")
	if ch.Status != update.StatusDev {
		t.Fatalf("git describe: %+v", ch)
	}
}

func TestCheckOffline(t *testing.T) {
	gh := updatetest.New(t, repo)
	c := gh.Client()
	gh.Close()
	ch, rel := c.CheckFor(context.Background(), "0.9.0")
	if ch.Status != update.StatusUnknown || ch.Error == "" || rel != nil {
		t.Fatalf("offline: %+v", ch)
	}
}

// installed is a stand-in for an installed hub binary, in a folder of its own.
func installed(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "nextboard")
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallSwapsAndKeepsTheOldBinary(t *testing.T) {
	gh := updatetest.New(t, repo)
	exe := bytes.Repeat([]byte("new hub "), 64<<10) // big enough to be read in several parts
	gh.Publish(t, "v0.9.1", false, exe)
	c := gh.Client()
	rel, err := c.ByTag(context.Background(), "v0.9.1")
	if err != nil {
		t.Fatal(err)
	}
	bin := installed(t, "old hub")
	var phases []string
	var pcts []float64
	err = c.Install(context.Background(), rel, bin, func(phase, text string, pct float64) {
		if len(phases) == 0 || phases[len(phases)-1] != phase {
			phases = append(phases, phase)
		}
		if pct >= 0 {
			pcts = append(pcts, pct)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, bin); got != string(exe) {
		t.Fatalf("the binary is %d bytes, not the new one", len(got))
	}
	if got := read(t, update.OldPath(bin)); got != "old hub" {
		t.Fatalf("%s holds %q", update.OldPath(bin), got)
	}
	if fi, _ := os.Stat(bin); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if _, err := os.Stat(bin + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s.new left behind: %v", bin, err)
	}
	if strings.Join(phases, ",") != "download,verify,swap" {
		t.Fatalf("phases %v", phases)
	}
	if len(pcts) < 2 || pcts[0] != 0 || pcts[len(pcts)-1] != 100 {
		t.Fatalf("download progress %v", pcts)
	}
	// a second update keeps only the one before it
	gh.Publish(t, "v0.9.2", false, []byte("newer hub"))
	rel, _ = c.ByTag(context.Background(), "v0.9.2")
	if err := c.Install(context.Background(), rel, bin, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, bin) != "newer hub" || read(t, update.OldPath(bin)) != string(exe) {
		t.Fatal("the second update did not keep the first as .old")
	}
}

func TestInstallRefusesWhatDoesNotCheckOut(t *testing.T) {
	ctx := context.Background()
	gh := updatetest.New(t, repo)
	gh.Publish(t, "v0.9.1", false, []byte("real"))
	gh.Tamper("v0.9.1", updatetest.Archive(t, []byte("evil")))
	c := gh.Client()
	rel, _ := c.ByTag(ctx, "v0.9.1")
	bin := installed(t, "old hub")
	if err := c.Install(ctx, rel, bin, nil); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered archive: %v", err)
	}
	assertUntouched(t, bin)

	// an archive from anywhere but the repository's releases is not even downloaded
	gh2 := updatetest.New(t, repo)
	gh2.Publish(t, "v0.9.1", false, []byte("real"))
	gh2.MoveAsset("v0.9.1", gh2.URL+"/someone-else/nextboard/releases/download/v0.9.1/"+update.AssetName("v0.9.1"))
	c2 := gh2.Client()
	rel, _ = c2.ByTag(ctx, "v0.9.1")
	before := len(gh2.Requests())
	if err := c2.Install(ctx, rel, bin, nil); err == nil || !strings.Contains(err.Error(), "not a download of "+repo) {
		t.Fatalf("foreign asset: %v", err)
	}
	if len(gh2.Requests()) != before {
		t.Fatalf("downloaded anyway: %v", gh2.Requests()[before:])
	}
	assertUntouched(t, bin)

	// a release without this machine's archive
	rel.Assets = rel.Assets[1:]
	if err := c2.Install(ctx, rel, bin, nil); err == nil || !strings.Contains(err.Error(), "missing from release") {
		t.Fatalf("no archive: %v", err)
	}
	assertUntouched(t, bin)
}

func assertUntouched(t *testing.T, bin string) {
	t.Helper()
	if got := read(t, bin); got != "old hub" {
		t.Fatalf("the binary changed: %q", got)
	}
	for _, p := range []string{update.OldPath(bin), bin + ".new"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists: %v", p, err)
		}
	}
}

func TestWritable(t *testing.T) {
	dir := t.TempDir()
	if err := update.Writable(dir); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := update.Writable(dir); err == nil {
		t.Fatal("a read-only folder is writable")
	}
}

// fakeSystemctl puts a systemctl on PATH that logs its calls and names mainPID as the service's.
func fakeSystemctl(t *testing.T, mainPID string) (calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"systemctl $*\" >> \"" + calls + "\"\ncase \"$*\" in\n*MainPID*) echo " + mainPID + " ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return calls
}

func unit(t *testing.T, bin string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[Service]\nType=simple\nExecStart=" + bin + " serve --data-dir " + filepath.Join(home, ".nextboard") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "nextboard.service"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManagedOnlyAsTheInstalledService(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the systemd user unit")
	}
	bin := installed(t, "hub")
	me := strconv.Itoa(os.Getpid())

	t.Setenv("HOME", t.TempDir())
	fakeSystemctl(t, me)
	if err := update.Managed(bin); !errors.Is(err, update.ErrNotService) {
		t.Fatalf("no unit: %v", err)
	}
	unit(t, bin)
	if err := update.Managed(bin); err != nil {
		t.Fatalf("the service: %v", err)
	}
	if err := update.Managed(installed(t, "another")); !errors.Is(err, update.ErrNotService) {
		t.Fatalf("a unit for another binary: %v", err)
	}
	// the right unit, but this process is not the one it runs (by hand, under air)
	fakeSystemctl(t, "4242")
	if err := update.Managed(bin); !errors.Is(err, update.ErrNotService) {
		t.Fatalf("another main pid: %v", err)
	}
	fakeSystemctl(t, "0")
	if err := update.Managed(bin); !errors.Is(err, update.ErrNotService) {
		t.Fatalf("service stopped: %v", err)
	}
}

func TestRestartSelfDoesNotWait(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	calls := fakeSystemctl(t, "1")
	if err := update.RestartSelf(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(read(t, calls)); got != "systemctl --user --no-block restart nextboard" {
		t.Fatalf("called %q", got)
	}
	// the terminal's restart waits for systemd, as before
	calls = fakeSystemctl(t, "1")
	if err := update.RestartService(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(read(t, calls)); got != "systemctl --user restart nextboard" {
		t.Fatalf("called %q", got)
	}
}

func TestNewClientTestOverride(t *testing.T) {
	t.Setenv(update.TestAPIEnv, "")
	c := update.NewClient(repo, 0)
	if c.API != "https://api.github.com" || c.Downloads != "https://github.com" {
		t.Fatalf("defaults %+v", c)
	}
	t.Setenv(update.TestAPIEnv, "http://127.0.0.1:9/")
	c = update.NewClient(repo, 0)
	if c.API != "http://127.0.0.1:9" || c.Downloads != "http://127.0.0.1:9" {
		t.Fatalf("override %+v", c)
	}
}
