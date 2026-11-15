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
// the tests built, in a folder of its own, so the update replaces only that link.
func TestUpdateCommandSwapsAndRestarts(t *testing.T) {
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
	env := []string{"HOME=" + t.TempDir(), "PATH=" + tools + ":/usr/bin:/bin", "LANG=C.UTF-8", "TMPDIR=" + os.TempDir(), update.TestAPIEnv + "=" + gh.URL}

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("nextboard %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	// what runs: version, channel, commit, build time
	if out := run("version"); !regexp.MustCompile(`^nextboard 0\.1\.0-dev \(dev, commit ([0-9a-f]{7}(-dirty)?|unknown), built \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ\)\n$`).MatchString(out) {
		t.Fatalf("version: %q", out)
	}

	out := run("update", "--repo", "example-owner/nextboard")
	if !strings.Contains(out, "Updated 0.1.0-dev → v9.9.9 (the previous binary is kept as "+bin+".old)") {
		t.Fatalf("update said:\n%s", out)
	}
	if b, _ := os.ReadFile(bin); string(b) != "#!/bin/sh\necho fake 9.9.9\n" {
		t.Fatalf("binary is %d bytes", len(b))
	}
	if a, _ := os.Stat(built); a != nil {
		if b, _ := os.Stat(bin + ".old"); b == nil || !os.SameFile(a, b) {
			t.Fatal("the old binary is not kept")
		}
	}
	if b, _ := os.ReadFile(calls); strings.TrimSpace(string(b)) != "systemctl --user restart nextboard" {
		t.Fatalf("service calls: %q", b)
	}
	// never a beta by itself: only the latest stable release was asked for
	for _, r := range gh.Requests() {
		if strings.Contains(r, "beta") {
			t.Fatalf("asked for a beta: %v", gh.Requests())
		}
	}
}
