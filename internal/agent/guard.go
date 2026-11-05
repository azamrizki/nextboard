package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Blocked commands (docs/API-orchestration.md §12): the owner's list of commands an agent
// needs their approval for, enforced by the CLI itself or, for the hub's own bash tool, by
// the hub. A model can talk itself past an instruction but not past this. It is still no
// sandbox: an agent with a shell can write a script that does the same thing.
//
// A pattern is the start of a command: "git push --force" also covers what comes after it.
// "*" stands for anything, so "*db:reset*" finds it anywhere in the command.

// codexRulesFile is ours inside a config folder the hub made.
const codexRulesFile = "nextboard.rules"

// anywhere is a pattern for a CLI that matches the text it is about to run. Claude Code only
// strips a few wrappers, and "Bash(rm *)" does not stop `bash -c 'rm x'`, so the pattern may
// sit anywhere in the text. A commit message quoting the command also asks, which is the
// right side to err on.
func anywhere(p string) string {
	return "*" + strings.TrimPrefix(strings.TrimSuffix(p, "*"), "*") + "*"
}

// ClaudeAskRules are permission rules: the CLI asks in the console before running it. Its
// documentation says ask rules hold in every mode, bypassPermissions included.
func ClaudeAskRules(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, "Bash("+anywhere(p)+")")
	}
	return out
}

// OpenCodeBash is the bash entry of OpenCode's permission config. The last match wins, and
// the keys go out sorted, so "*" comes first.
func OpenCodeBash(patterns []string) map[string]string {
	out := map[string]string{"*": "allow"}
	for _, p := range patterns {
		out[anywhere(p)] = "ask"
	}
	return out
}

// CodexRules is an execpolicy file. Codex matches whole words from the start of a command,
// so a pattern with "*" anywhere but its end cannot be written for it.
func CodexRules(patterns []string) string {
	var b strings.Builder
	for _, p := range patterns {
		p = strings.TrimSuffix(p, "*")
		if strings.Contains(p, "*") {
			continue
		}
		words := strings.Fields(p)
		if len(words) == 0 {
			continue
		}
		quoted := make([]string, len(words))
		for i, w := range words {
			quoted[i] = tomlString(w) // a JSON string is a Starlark string
		}
		fmt.Fprintf(&b, "prefix_rule(pattern = [%s], decision = \"prompt\", justification = \"Blocked by the owner in NextBoard\")\n", strings.Join(quoted, ", "))
	}
	return b.String()
}

// WriteCodexRules puts the rules in a Codex config folder, which all its consoles share;
// no rules, no file.
func WriteCodexRules(codexHome string, patterns []string) error {
	file := filepath.Join(codexHome, "rules", codexRulesFile)
	rules := CodexRules(patterns)
	if rules == "" {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(rules), 0o600)
}

var (
	commandSeparators = regexp.MustCompile("&&|\\|\\||[;|&\\n()`{}]")
	commandEnvPrefix  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=\S*$`)
	commandWrappers   = []string{"sudo", "env", "command", "exec", "nohup", "time", "nice"}
	commandShellC     = regexp.MustCompile(`^(?:ba|z|da)?sh\s+-l?c\s+(.+)$`)
)

// CommandBlocked returns the pattern a command falls under, "" when none. Each part of a
// compound command is looked at on its own. It splits without reading quotes, so it may
// look at too much, which errs on the side of asking.
func CommandBlocked(patterns []string, command string) string {
	if len(patterns) == 0 {
		return ""
	}
	for _, part := range commandParts(command, 0) {
		for _, p := range patterns {
			if blockedMatch(p, part) {
				return p
			}
		}
	}
	return ""
}

func commandParts(command string, depth int) []string {
	var out []string
	for _, raw := range commandSeparators.Split(command, -1) {
		words := strings.Fields(raw)
		for len(words) > 0 && (commandEnvPrefix.MatchString(words[0]) || slices.Contains(commandWrappers, words[0])) {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		part := strings.Join(words, " ")
		if m := commandShellC.FindStringSubmatch(part); m != nil && depth < 3 {
			out = append(out, commandParts(strings.Trim(m[1], `"'`), depth+1)...)
			continue
		}
		out = append(out, part)
	}
	// What a split would cut in two is still found in the whole.
	if depth == 0 {
		out = append(out, strings.Join(strings.Fields(command), " "))
	}
	return out
}

// blockedMatch: a pattern covers the start of a command, and "*" stands for anything.
func blockedMatch(pattern, command string) bool {
	pieces := strings.Split(strings.Join(strings.Fields(pattern), " "), "*")
	if !strings.HasPrefix(command, pieces[0]) {
		return false
	}
	rest := command[len(pieces[0]):]
	for _, piece := range pieces[1:] {
		i := strings.Index(rest, piece)
		if i < 0 {
			return false
		}
		rest = rest[i+len(piece):]
	}
	return true
}
