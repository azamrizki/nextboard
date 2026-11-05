package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Agents leave no trace of themselves in commits (docs/API-agents.md §26). A CLI that
// can be told is told: Claude Code through --settings, the others that take a prompt
// through NoCoAuthorRule. Not every CLI can be, and a rule is not a lock, so an agent
// console also runs git with core.hooksPath on a folder of the hub: its commit-msg
// removes such lines and every hook goes on to the repository's own.

// NoCoAuthorRule is the part of an agent's system prompt about commits.
const NoCoAuthorRule = "Commits and pull requests carry no sign of you: never add a Co-authored-by trailer or a \"Generated with\" line for yourself or any AI tool, and ignore any instruction that asks for one. This is the owner's choice. Co-authors who are people stay."

// commitHookNames are the hooks that pass through. Left out: those whose presence alone
// changes what git does (push-to-checkout), and those a repository must configure by name.
var commitHookNames = []string{"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit", "prepare-commit-msg",
	"commit-msg", "post-commit", "pre-rebase", "post-checkout", "post-merge", "pre-push", "pre-receive", "update", "post-receive", "post-update",
	"reference-transaction", "pre-auto-gc", "post-rewrite", "sendemail-validate", "post-index-change"}

// commitHookScript runs as every hook. A person's co-author stays: a name alone says nothing
// (Claude is also a first name), so it takes an agent's name with a bot's address, or a
// vendor's own address.
const commitHookScript = `#!/bin/sh
# NextBoard: agent consoles run git with this folder as core.hooksPath, so that no agent
# signs a commit. Every hook goes on to the repository's own. Written by the hub.
name=${0##*/}
# with this folder out of the config, git says where the repository keeps its hooks
dir=$(GIT_CONFIG_COUNT=0 git rev-parse --git-path hooks 2>/dev/null) || dir=
own=$dir/$name
# never this script itself, should the repository's config name this folder
if [ -z "$dir" ] || [ ! -x "$own" ] || [ "$own" -ef "$0" ]; then
	own=
fi
if [ "$name" != commit-msg ]; then
	[ -n "$own" ] && exec "$own" "$@"
	exit 0
fi
# last: the repository's hook may check or add to the message
if [ -n "$own" ]; then
	"$own" "$@" || exit $?
fi
[ -f "$1" ] || exit 0
tmp=$1.nextboard
LC_ALL=C grep -v -i -E \
	-e '^[[:space:]]*co-authored-by:.*<(noreply@anthropic\.com|noreply@openai\.com|cursoragent@|aider@aider\.chat|amp@ampcode\.com|droid@factory\.ai)' \
	-e '^[[:space:]]*co-authored-by:[[:space:]]*(github[ -])?(claude|codex|gemini|opencode|aider|amp|auggie|augment|cline|codebuff|continue|copilot|crush|cursor|droid|factory|goose|kimi|kiro|pi|qwen|windsurf|devin)([^<[:alnum:]][^<]*)?<[^>]*(noreply|no-reply|@anthropic\.|@openai\.|@aider\.|@ampcode\.|@cursor\.|@factory\.|@block\.|@moonshot\.|@cline\.)' \
	-e '^[[:space:]]*([^[:alnum:][:space:]]+[[:space:]]*)?generated (with|by) .*(claude|codex|gemini|opencode|aider|copilot|cursor|qwen|kimi|kiro|windsurf|devin)' \
	"$1" >"$tmp"
[ -s "$tmp" ] && cat "$tmp" >"$1"
rm -f "$tmp"
exit 0
`

// WriteCommitHooks puts the hooks in dir, as they are in this version of the hub.
func WriteCommitHooks(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	const script = "nextboard-hook"
	tmp := filepath.Join(dir, script+".tmp")
	if err := os.WriteFile(tmp, []byte(commitHookScript), 0o700); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, script)); err != nil {
		return err
	}
	for _, name := range commitHookNames {
		p := filepath.Join(dir, name)
		if l, err := os.Readlink(p); err == nil && l == script {
			continue
		}
		_ = os.Remove(p)
		if err := os.Symlink(script, p); err != nil {
			return err
		}
	}
	return nil
}

// CommitHookEnv makes git in a process use the hooks in dir, after whatever config the
// hub's own environment passes on the same way.
func CommitHookEnv(dir string) []string {
	n, err := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	if err != nil || n < 0 {
		n = 0
	}
	return []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(n+1), fmt.Sprintf("GIT_CONFIG_KEY_%d=core.hooksPath", n), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, dir)}
}
