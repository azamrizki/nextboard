package studio

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"nextboard/internal/machine"
)

// Task is something a project knows how to run: found in its files, never given by a client
// (docs/API-studio.md §7.12).
type Task struct {
	ID string `json:"id"`
	// Group is where it comes from, such as "npm" or "web/npm".
	Group string `json:"group"`
	Name  string `json:"name"`
	// Kind is "test", "build" or "run".
	Kind    string `json:"kind"`
	Command string `json:"command"`
	// Cwd is relative to the folder that was scanned.
	Cwd string `json:"cwd"`
}

const (
	taskScan     = 15 * time.Second
	taskFileSize = 256 << 10
	maxTasks     = 300
)

// The files that say what can be run. Lock files only say which package manager.
const taskFinder = `find . -maxdepth 2 \( -name node_modules -o -name .git -o -name vendor -o -name target -o -name dist -o -name build -o -name .venv -o -name venv \) -prune -o -type f \( ` +
	`-name package.json -o -name Makefile -o -name makefile -o -name GNUmakefile -o -name go.mod -o -name Cargo.toml -o -name pyproject.toml -o -name pytest.ini -o -name composer.json -o -name justfile -o -name pnpm-lock.yaml -o -name yarn.lock -o -name bun.lockb -o -name bun.lock ` +
	`\) -print | head -n 200 | while IFS= read -r f; do printf '\000%s\000' "$f"; case "$f" in *.lock|*lock.yaml|*.lockb) ;; *) head -c 262144 "$f" ;; esac; done`

// DetectTasks reads what root and the folders right below it can run.
func DetectTasks(ctx context.Context, h machine.Host, root string) ([]Task, error) {
	ctx, cancel := context.WithTimeout(ctx, taskScan)
	defer cancel()
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Cwd: root, Script: "exec /bin/sh -c " + machine.Quote(taskFinder), Timeout: taskScan, MaxOutput: 16 << 20})
	if err != nil {
		return nil, err
	}
	if res.TimedOut {
		return nil, errors.New("reading the project took too long")
	}
	files := map[string]string{}
	parts := strings.Split(string(res.Stdout), "\x00")
	for i := 1; i+1 < len(parts); i += 2 {
		files[strings.TrimPrefix(parts[i], "./")] = parts[i+1]
	}
	return tasksFrom(files), nil
}

var (
	makeTarget = regexp.MustCompile(`(?m)^([A-Za-z0-9][A-Za-z0-9_.-]*)\s*:(?:[^=]|$)`)
	justRecipe = regexp.MustCompile(`(?m)^([A-Za-z0-9][A-Za-z0-9_-]*)(?:\s+[^:=\n]*)?:(?:[^=]|$)`)
)

func taskKind(name string) string {
	low := strings.ToLower(name)
	switch {
	case strings.Contains(low, "test") || strings.Contains(low, "spec"):
		return "test"
	case strings.Contains(low, "build") || strings.Contains(low, "compile"):
		return "build"
	}
	return "run"
}

func safeTaskName(name string) bool {
	if name == "" || len(name) > 120 || strings.HasPrefix(name, "-") {
		return false
	}
	for _, r := range name {
		if !(r == ':' || r == '-' || r == '_' || r == '.' || r == '/' || r == '+' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func tasksFrom(files map[string]string) []Task {
	var out []Task
	add := func(dir, tool, name, kind, cmd string) {
		group := tool
		if dir != "" {
			group = dir + "/" + tool
		}
		out = append(out, Task{ID: group + ":" + name, Group: group, Name: name, Kind: kind, Command: cmd, Cwd: dir})
	}
	dirOf := func(p string) string {
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			return p[:i]
		}
		return ""
	}
	has := func(dir, name string) bool {
		if dir != "" {
			name = dir + "/" + name
		}
		_, ok := files[name]
		return ok
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		dir := dirOf(p)
		base := p[len(dir):]
		base = strings.TrimPrefix(base, "/")
		text := files[p]
		switch base {
		case "package.json":
			var pkg struct {
				Scripts map[string]string `json:"scripts"`
			}
			if json.Unmarshal([]byte(text), &pkg) != nil {
				continue
			}
			pm := "npm"
			switch {
			case has(dir, "pnpm-lock.yaml"):
				pm = "pnpm"
			case has(dir, "yarn.lock"):
				pm = "yarn"
			case has(dir, "bun.lockb"), has(dir, "bun.lock"):
				pm = "bun"
			}
			names := make([]string, 0, len(pkg.Scripts))
			for n := range pkg.Scripts {
				if safeTaskName(n) {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			for _, n := range names {
				add(dir, pm, n, taskKind(n), pm+" run "+n)
			}
		case "Makefile", "makefile", "GNUmakefile":
			seen := map[string]bool{}
			for _, m := range makeTarget.FindAllStringSubmatch(text, -1) {
				if n := m[1]; safeTaskName(n) && !seen[n] {
					seen[n] = true
					add(dir, "make", n, taskKind(n), "make "+n)
				}
			}
		case "justfile":
			seen := map[string]bool{}
			for _, m := range justRecipe.FindAllStringSubmatch(text, -1) {
				if n := m[1]; safeTaskName(n) && !seen[n] {
					seen[n] = true
					add(dir, "just", n, taskKind(n), "just "+n)
				}
			}
		case "go.mod":
			add(dir, "go", "test", "test", "go test ./...")
			add(dir, "go", "build", "build", "go build ./...")
			add(dir, "go", "vet", "run", "go vet ./...")
		case "Cargo.toml":
			add(dir, "cargo", "test", "test", "cargo test")
			add(dir, "cargo", "build", "build", "cargo build")
		case "pyproject.toml":
			if strings.Contains(text, "pytest") {
				add(dir, "python", "pytest", "test", "python3 -m pytest")
			}
		case "pytest.ini":
			if !has(dir, "pyproject.toml") || !strings.Contains(files[strings.TrimPrefix(dir+"/pyproject.toml", "/")], "pytest") {
				add(dir, "python", "pytest", "test", "python3 -m pytest")
			}
		case "composer.json":
			var pkg struct {
				Scripts map[string]json.RawMessage `json:"scripts"`
			}
			if json.Unmarshal([]byte(text), &pkg) != nil {
				continue
			}
			names := make([]string, 0, len(pkg.Scripts))
			for n := range pkg.Scripts {
				if safeTaskName(n) {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			for _, n := range names {
				add(dir, "composer", n, taskKind(n), "composer run "+n)
			}
		}
	}
	if len(out) > maxTasks {
		out = out[:maxTasks]
	}
	if out == nil {
		out = []Task{}
	}
	return out
}
