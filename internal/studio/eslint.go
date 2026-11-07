package studio

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// ESLint (docs/API-studio.md §7.6): the project's own eslint, or else the
// machine's, reads one file from stdin. Either way it loads the project's config,
// which is code, so the editor calls this only once the user allowed that project.

const (
	eslintTimeout   = 60 * time.Second
	eslintMaxOutput = 32 << 20
	// eslintNone is the exit code for "no eslint here"; eslint itself uses 0, 1, 2.
	eslintNone = 64
)

// ErrBadLintPath: the path is not a file inside the project.
var ErrBadLintPath = errors.New("invalid path")

// ESLintResult is POST /api/projects/{id}/eslint.
type ESLintResult struct {
	Available bool `json:"available"`
	// Error is why eslint could not lint the file, such as a broken config.
	Error string `json:"error,omitempty"`
	// Messages are eslint's own, passed on as they are.
	Messages json.RawMessage `json:"messages,omitempty"`
	// Output is the whole fixed text, when fix was asked for and changed something.
	Output *string `json:"output,omitempty"`
}

func eslintScript(rel string, fix bool) string {
	flags := `--no-color --format json --stdin --stdin-filename "$abs"`
	if fix {
		flags += ` --fix-dry-run`
	}
	q := machine.Quote(rel)
	return lspPath + `abs="$PWD"/` + q + `; d=$(dirname -- ` + q + `); bin=; ` +
		`while :; do ` +
		`if [ -x "$d/node_modules/.bin/eslint" ]; then bin="$PWD/$d/node_modules/.bin/eslint"; cd -- "$d" || exit 1; break; fi; ` +
		`[ "$d" = . ] && break; d=$(dirname -- "$d"); ` +
		`done; ` +
		`if [ -z "$bin" ]; then bin=$(command -v eslint) || exit ` + strconv.Itoa(eslintNone) + `; fi; ` +
		`exec "$bin" ` + flags
}

// RunESLint lints text as the file rel below root.
func RunESLint(ctx context.Context, h machine.Host, root, rel, text string, fix bool) (*ESLintResult, error) {
	clean := path.Clean(rel)
	if rel == "" || path.IsAbs(rel) || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsAny(rel, "\x00\r\n") {
		return nil, ErrBadLintPath
	}
	res, err := execScriptIn(ctx, h, root, eslintScript(clean, fix), []byte(text), eslintTimeout, eslintMaxOutput)
	if err != nil {
		return nil, err
	}
	if res.ExitCode == eslintNone {
		return &ESLintResult{}, nil
	}
	out := &ESLintResult{Available: true}
	if res.ExitCode == 0 || res.ExitCode == 1 {
		var files []struct {
			Messages json.RawMessage `json:"messages"`
			Output   *string         `json:"output"`
		}
		if json.Unmarshal(res.Stdout, &files) == nil && len(files) > 0 {
			out.Messages, out.Output = files[0].Messages, files[0].Output
			return out, nil
		}
	}
	msg := strings.TrimSpace(string(res.Stderr))
	if msg == "" {
		msg = strings.TrimSpace(string(res.Stdout))
	}
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	if msg == "" {
		msg = "eslint stopped without a message"
	}
	out.Error = msg
	return out, nil
}
