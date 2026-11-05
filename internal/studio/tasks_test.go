package studio

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"nextboard/internal/machine"
)

func TestTasksFromFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"scripts":{"build":"tsc","test":"vitest","dev":"vite","bad name; rm -rf /":"x","-x":"y"}}`)
	write("pnpm-lock.yaml", "x")
	write("Makefile", ".PHONY: all\nall: build\n\ttouch x\nbuild:\n\tgo build\nVAR := 1\nCC = gcc\n%.o: %.c\n\tcc\ntest-unit: all\n\tgo test\n")
	write("go.mod", "module x\n")
	write("web/package.json", `{"scripts":{"lint":"eslint"}}`)
	write("web/yarn.lock", "x")
	write("api/Cargo.toml", "[package]\n")
	write("py/pyproject.toml", "[tool.pytest.ini_options]\n")
	write("other/pyproject.toml", "[tool.black]\n")
	write("justfile", "default:\n  echo\nfmt arg:\n  echo\nVAR := 1\n")
	write("composer.json", `{"scripts":{"phpunit":"phpunit"}}`)
	write("node_modules/dep/package.json", `{"scripts":{"nope":"x"}}`)
	write("deep/er/package.json", `{"scripts":{"nope":"x"}}`)
	write("broken/package.json", `{not json`)

	list, err := DetectTasks(context.Background(), machine.NewLocal(machine.Config{}), dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Task{}
	for _, task := range list {
		got[task.ID] = task
	}
	want := map[string]string{
		"pnpm:build":       "pnpm run build",
		"pnpm:test":        "pnpm run test",
		"pnpm:dev":         "pnpm run dev",
		"make:all":         "make all",
		"make:build":       "make build",
		"make:test-unit":   "make test-unit",
		"go:test":          "go test ./...",
		"go:build":         "go build ./...",
		"go:vet":           "go vet ./...",
		"web/yarn:lint":    "yarn run lint",
		"api/cargo:test":   "cargo test",
		"api/cargo:build":  "cargo build",
		"py/python:pytest": "python3 -m pytest",
		"just:default":     "just default",
		"just:fmt":         "just fmt",
		"composer:phpunit": "composer run phpunit",
	}
	for id, cmd := range want {
		if got[id].Command != cmd {
			t.Errorf("%s: %q", id, got[id].Command)
		}
	}
	if len(got) != len(want) {
		t.Errorf("extra tasks: %+v", list)
	}
	if got["web/yarn:lint"].Cwd != "web" || got["go:test"].Kind != "test" || got["make:build"].Kind != "build" || got["pnpm:dev"].Kind != "run" {
		t.Errorf("fields %+v", got)
	}
}
