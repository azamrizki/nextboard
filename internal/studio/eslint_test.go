package studio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/machine"
)

// fakeESLint answers like eslint --format json. It records its folder, arguments
// and stdin next to itself, and fails with exit 2 when the text says so.
const fakeESLint = `#!/bin/sh
here=$(dirname "$0")
pwd > "$here/cwd"
printf '%s\n' "$@" > "$here/args"
cat > "$here/stdin"
if grep -q BROKEN "$here/stdin"; then echo 'Oops! config is broken' >&2; exit 2; fi
out=
case " $* " in *" --fix-dry-run "*) out=',"output":"fixed\n"' ;; esac
printf '[{"messages":[{"ruleId":"semi","severity":2,"message":"Missing semicolon.","line":1,"column":4,"fix":{"range":[3,3],"text":";"}}]%s}]' "$out"
exit 1
`

func writeExe(t *testing.T, file, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestESLintUsesTheNearestOneAndReportsThroughStdin(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "/usr/bin:/bin")
	h := machine.NewLocal(machine.Config{})
	ctx := context.Background()

	res, err := RunESLint(ctx, h, root, "src/a.js", "let a", false)
	if err != nil || res.Available {
		t.Fatalf("no eslint: %+v %v", res, err)
	}

	outer := filepath.Join(root, "node_modules", ".bin")
	inner := filepath.Join(root, "packages", "web", "node_modules", ".bin")
	writeExe(t, filepath.Join(outer, "eslint"), fakeESLint)
	writeExe(t, filepath.Join(inner, "eslint"), fakeESLint)

	res, err = RunESLint(ctx, h, root, "src/a.js", "let a", false)
	if err != nil || !res.Available || res.Error != "" || !strings.Contains(string(res.Messages), "Missing semicolon") || res.Output != nil {
		t.Fatalf("%+v %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(outer, "stdin")); string(b) != "let a" {
		t.Fatalf("stdin %q", b)
	}
	args, _ := os.ReadFile(filepath.Join(outer, "args"))
	if !strings.Contains(string(args), filepath.Join(root, "src", "a.js")) {
		t.Fatalf("args %s", args)
	}

	// a package with its own eslint runs from that package, for its config
	if res, err = RunESLint(ctx, h, root, "packages/web/src/b.js", "let b", true); err != nil || res.Output == nil || *res.Output != "fixed\n" {
		t.Fatalf("%+v %v", res, err)
	}
	cwd, _ := os.ReadFile(filepath.Join(inner, "cwd"))
	if strings.TrimSpace(string(cwd)) != filepath.Join(root, "packages", "web") {
		t.Fatalf("cwd %s", cwd)
	}
	args, _ = os.ReadFile(filepath.Join(inner, "args"))
	if !strings.Contains(string(args), "--fix-dry-run") {
		t.Fatalf("args %s", args)
	}

	res, err = RunESLint(ctx, h, root, "src/a.js", "BROKEN", false)
	if err != nil || !res.Available || !strings.Contains(res.Error, "config is broken") || res.Messages != nil {
		t.Fatalf("%+v %v", res, err)
	}

	for _, bad := range []string{"", "/etc/passwd", "../x.js", "a/../../x.js", "a\nb.js"} {
		if _, err := RunESLint(ctx, h, root, bad, "", false); !errors.Is(err, ErrBadLintPath) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestESLintFallsBackToTheMachine(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	h := machine.NewLocal(machine.Config{})
	writeExe(t, filepath.Join(home, ".npm-global", "bin", "eslint"), fakeESLint)
	res, err := RunESLint(context.Background(), h, root, "a name's.js", "let a", false)
	if err != nil || !res.Available || res.Messages == nil {
		t.Fatalf("%+v %v", res, err)
	}
}
