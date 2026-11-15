package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/update"
	"github.com/azamrizki/nextboard/internal/update/updatetest"
)

// `nextboard update` as a user runs it, against a fake GitHub: the binary is a link to the one
// the tests built, in a folder of its own, so the update replaces only that link. The services of
// every instance that runs it restart, listed first; a service of another binary does not.
func TestUpdateCommandSwapsAndRestarts(t *testing.T) {
	u := newUpdateBox(t)
	run := u.run

	// what runs: version, channel, commit, build time
	if out := run("version"); !regexp.MustCompile(`^nextboard 0\.1\.0-dev \(dev, commit ([0-9a-f]{7}(-dirty)?|unknown), built \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ\)\n$`).MatchString(out) {
		t.Fatalf("version: %q", out)
	}

	out := run("update", "--repo", "example-owner/nextboard")
	listed := strings.Index(out, "After the update these instances restart (local agents relaunch with their resume arguments): default (127.0.0.1:12312), work (127.0.0.1:12412)\n")
	updated := strings.Index(out, "Updated 0.1.0-dev → v9.9.9 (the previous binary is kept as "+u.bin+".old)")
	if listed < 0 || updated < listed {
		t.Fatalf("update said:\n%s", out)
	}
	if b, _ := os.ReadFile(u.bin); string(b) != "#!/bin/sh\necho fake 9.9.9\n" {
		t.Fatalf("binary is %d bytes", len(b))
	}
	if a, _ := os.Stat(u.built); a != nil {
		if b, _ := os.Stat(u.bin + ".old"); b == nil || !os.SameFile(a, b) {
			t.Fatal("the old binary is not kept")
		}
	}
	if b, _ := os.ReadFile(u.calls); strings.TrimSpace(string(b)) != "systemctl --user restart nextboard\nsystemctl --user restart nextboard-work" {
		t.Fatalf("service calls: %q", b)
	}
	// never a beta by itself: only the latest stable release was asked for
	for _, r := range u.gh.Requests() {
		if strings.Contains(r, "beta") {
			t.Fatalf("asked for a beta: %v", u.gh.Requests())
		}
	}
}

// update --instance restarts that instance's service only, and names the others still on the
// old build.
func TestUpdateCommandOneInstance(t *testing.T) {
	u := newUpdateBox(t)
	out := u.run("update", "--repo", "example-owner/nextboard", "--instance", "work")
	if !strings.Contains(out, "these instances restart (local agents relaunch with their resume arguments): work (127.0.0.1:12412)\n") ||
		!strings.Contains(out, "These still run 0.1.0-dev until they restart: default (127.0.0.1:12312)") {
		t.Fatalf("update said:\n%s", out)
	}
	if b, _ := os.ReadFile(u.calls); strings.TrimSpace(string(b)) != "systemctl --user restart nextboard-work" {
		t.Fatalf("service calls: %q", b)
	}
}

type updateBox struct {
	bin, built, calls string
	gh                *updatetest.Fake
	run               func(args ...string) string
}

// newUpdateBox: a fake GitHub with a release, the binary in a folder of its own, and a scratch
// home with the services of three instances, two on this binary.
func newUpdateBox(t *testing.T) *updateBox {
	t.Helper()
	built := nextboardBinary(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "nextboard")
	if err := os.Link(built, bin); err != nil {
		t.Fatal(err)
	}
	gh := updatetest.New(t, "example-owner/nextboard")
	gh.Publish(t, "v9.9.9", false, []byte("#!/bin/sh\necho fake 9.9.9\n"))
	gh.Publish(t, "v10.0.0-beta.1", true, []byte("#!/bin/sh\necho fake beta\n"))
	tools := t.TempDir()
	calls := filepath.Join(tools, "calls.log")
	writeExec(t, filepath.Join(tools, "systemctl"), `echo "systemctl $*" >> "`+calls+`"`+"\n")
	home := t.TempDir()
	units := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, exec := range map[string]string{
		"nextboard.service":       bin + " serve --data-dir " + filepath.Join(home, ".nextboard"),
		"nextboard-work.service":  bin + " serve --instance work --data-dir " + filepath.Join(home, ".nextboard-work"),
		"nextboard-other.service": "/opt/other/nextboard serve --instance other",
	} {
		if err := os.WriteFile(filepath.Join(units, file), []byte("[Service]\nExecStart="+exec+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	work := filepath.Join(home, ".nextboard-work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "config.json"), []byte(`{"instance":"work","listen":"127.0.0.1:12412"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "PATH=" + tools + ":/usr/bin:/bin", "LANG=C.UTF-8", "TMPDIR=" + os.TempDir(), update.TestAPIEnv + "=" + gh.URL}
	u := &updateBox{bin: bin, built: built, calls: calls, gh: gh}
	u.run = func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("nextboard %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	return u
}
