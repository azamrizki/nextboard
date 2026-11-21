package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	globMax  = 500    // paths one Glob shows
	globScan = 100000 // paths one Glob collects before it sorts them
)

const globDescription = `Finds files by name with a glob pattern, and lists their absolute paths, the most recently changed first.

- Patterns: * (any characters but /), ** (any number of folders), ?, [abc] and {a,b}. A pattern without a / matches file names at any depth ("*.go"); one with a / matches paths from the search folder ("src/**/*.ts", "cmd/*/main.go").
- path is the folder to search: the working folder by default. Leave it out rather than passing "." or "undefined".
- .git is left out, and so are the files .gitignore ignores (or node_modules, where ripgrep is missing).
- At most 500 paths are shown. To search inside files, use Grep. When several searches may help, run them in parallel calls.`

const globSchema = `{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "The glob pattern to match files against"},
    "path": {"type": "string", "description": "The folder to search in (the working folder by default)"}
  },
  "required": ["pattern"],
  "additionalProperties": false
}`

func globTool() Tool { return newTool("Glob", KindRead, globDescription, globSchema, runGlob) }

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func runGlob(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in globInput
	if err := decode("Glob", input, &in); err != nil {
		return fail("%v", err)
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return fail("Glob needs pattern: the glob to match file names against")
	}
	root := env.Cwd
	if p := strings.TrimSpace(in.Path); p != "" && p != "undefined" && p != "null" {
		root = p
	}
	root, err := pathArg(env, "Glob", "path", root)
	if err != nil {
		return fail("%v", err)
	}
	root, pattern := splitPattern(root, strings.TrimSpace(in.Pattern))
	st, err := os.Stat(root)
	if err != nil {
		return fail("%s", strings.Replace(fileError(env, root, err), "File does not exist", "Path does not exist", 1))
	}
	if !st.IsDir() {
		return fail("%s is not a folder: Glob searches a folder (use Read for a file)", root)
	}
	files, err := findFiles(ctx, root, pattern)
	if ctx.Err() != nil {
		return stopped()
	}
	if err != nil {
		return fail("%v", err)
	}
	if len(files) == 0 {
		return ok("No files found")
	}
	newestFirst(files)
	out := strings.Join(files[:min(len(files), globMax)], "\n")
	switch {
	case len(files) >= globScan:
		out += fmt.Sprintf("\n(%d shown of at least %d files: narrow the pattern or the path)", globMax, globScan)
	case len(files) > globMax:
		out += fmt.Sprintf("\n(%d shown of %d files, the newest first: narrow the pattern or the path for the rest)", globMax, len(files))
	}
	return ok(out)
}

// splitPattern takes the leading folders of an absolute pattern that hold no glob character
// as the folder to search, so "/src/app/**/*.ts" searches /src/app for "**/*.ts". A pattern
// left without a slash is anchored there with a leading "/".
func splitPattern(root, pattern string) (string, string) {
	pattern = strings.TrimPrefix(pattern, "./")
	if !filepath.IsAbs(pattern) {
		return root, pattern
	}
	parts := strings.Split(filepath.Clean(pattern), "/")
	k := 1
	for k < len(parts)-1 && !hasMeta(parts[k]) {
		k++
	}
	base, rest := "/"+strings.Join(parts[1:k], "/"), strings.Join(parts[k:], "/")
	if !strings.Contains(rest, "/") {
		rest = "/" + rest
	}
	return base, rest
}

// findFiles lists the files under root that match pattern, as absolute paths: with ripgrep,
// which honours .gitignore, or else with a walk of its own.
func findFiles(ctx context.Context, root, pattern string) ([]string, error) {
	if bin, err := lookRg(); err == nil {
		lines, _, err := rg(ctx, bin, root, []string{"--files", "--hidden", "-g", "!.git", "-g", pattern}, globScan)
		if err != nil {
			return nil, err
		}
		for i, l := range lines {
			lines[i] = filepath.Join(root, l)
		}
		return lines, nil
	}
	re, err := globRegexp(pattern)
	if err != nil {
		return nil, err
	}
	var files []string
	err = walk(ctx, root, func(path, rel string) error {
		if re.MatchString(rel) {
			files = append(files, path)
			if len(files) >= globScan {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return files, err
}
