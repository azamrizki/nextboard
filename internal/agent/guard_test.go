package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandBlocked(t *testing.T) {
	patterns := []string{"git push --force", "git reset --hard", "*db:reset*", "rm -rf ~", "*drop database*"}
	blocked := map[string]string{
		"git push --force":                        "git push --force",
		"git push --force origin main":            "git push --force",
		"git push --force-with-lease":             "git push --force",
		"  git   push   --force  ":                "git push --force",
		"cd app && git reset --hard HEAD~1":       "git reset --hard",
		"make build; git reset --hard":            "git reset --hard",
		"true | git reset --hard":                 "git reset --hard",
		"(git reset --hard)":                      "git reset --hard",
		"echo $(git reset --hard)":                "git reset --hard",
		"echo `git reset --hard`":                 "git reset --hard",
		"FOO=1 BAR=2 git reset --hard":            "git reset --hard",
		"sudo git reset --hard":                   "git reset --hard",
		"env FOO=1 git reset --hard":              "git reset --hard",
		`bash -c "git reset --hard"`:              "git reset --hard",
		`sh -lc 'cd x && git reset --hard'`:       "git reset --hard",
		"npm run db:reset":                        "*db:reset*",
		"cd api && npm run db:reset -- --seed":    "*db:reset*",
		"rm -rf ~/projects":                       "rm -rf ~",
		`psql -c "drop database app"`:             "*drop database*",
		"mysql -e 'select 1; drop database app;'": "*drop database*",
		"git reset --hard\nrm -rf ~":              "git reset --hard",
	}
	for cmd, want := range blocked {
		if got := CommandBlocked(patterns, cmd); got != want {
			t.Errorf("%q: %q, want %q", cmd, got, want)
		}
	}
	for _, cmd := range []string{"", "git push", "git push origin main", "git reset HEAD~1", "git status", "npm test", "rm -rf build", "echo reset", "ls ~"} {
		if got := CommandBlocked(patterns, cmd); got != "" {
			t.Errorf("%q fell under %q", cmd, got)
		}
	}
	if got := CommandBlocked(nil, "git reset --hard"); got != "" {
		t.Errorf("no list: %q", got)
	}
}

func TestBlockedForEachCLI(t *testing.T) {
	patterns := []string{"git push --force", "npm run db:reset*", "*drop database*", "git push *--force"}
	claude := ClaudeAskRules(patterns)
	want := []string{"Bash(*git push --force*)", "Bash(*npm run db:reset*)", "Bash(*drop database*)", "Bash(*git push *--force*)"}
	if strings.Join(claude, "|") != strings.Join(want, "|") {
		t.Fatalf("claude %q", claude)
	}
	open := OpenCodeBash(patterns)
	if open["*"] != "allow" || open["*git push --force*"] != "ask" || open["*drop database*"] != "ask" || open["*npm run db:reset*"] != "ask" {
		t.Fatalf("opencode %v", open)
	}
	// Codex matches words from the start: what has "*" before its end stays out.
	rules := CodexRules(patterns)
	if strings.Count(rules, "prefix_rule(") != 2 || !strings.Contains(rules, `pattern = ["git", "push", "--force"], decision = "prompt"`) || !strings.Contains(rules, `["npm", "run", "db:reset"]`) {
		t.Fatalf("codex:\n%s", rules)
	}
	dir := t.TempDir()
	if err := WriteCodexRules(dir, patterns); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "rules", codexRulesFile)
	b, err := os.ReadFile(file)
	if err != nil || string(b) != rules {
		t.Fatalf("%v %s", err, b)
	}
	if err := WriteCodexRules(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("an empty list leaves the file: %v", err)
	}
	if err := WriteCodexRules(t.TempDir(), nil); err != nil {
		t.Fatalf("nothing to remove: %v", err)
	}
}

func TestClaudeSettingsCarryBlocked(t *testing.T) {
	var v struct {
		Permissions struct {
			Ask []string `json:"ask"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(claudeSettings(false, "", false, []string{"git push --force"})), &v); err != nil || len(v.Permissions.Ask) != 1 || v.Permissions.Ask[0] != "Bash(*git push --force*)" {
		t.Fatalf("%v %+v", err, v)
	}
	if s := claudeSettings(false, "", false, nil); strings.Contains(s, "permissions") {
		t.Fatalf("no list: %s", s)
	}
}
