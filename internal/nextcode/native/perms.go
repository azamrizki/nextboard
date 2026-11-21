package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	guard "github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/nextcode/native/tools"
)

// planDenied is the result of a call that plan mode does not let run.
const planDenied = "You are in plan mode: change nothing yet. Finish your plan, then call ExitPlanMode with it for the owner to approve."

// verdict is what the permission mode says of one tool call: run it, refuse it (the result
// says why), or ask the owner.
type verdict struct {
	run  bool
	deny string
	ask  *askSpec
}

// askSpec is what a permission request says besides the call itself.
type askSpec struct {
	reason      string
	blockedPath string
	suggestions []any // the rules offered, in Claude Code's permission_suggestions form
}

// rule is a session rule the owner allowed: calls of tool that match content go ahead for the
// rest of the process. content "" = every call of the tool.
type rule struct {
	tool, content string
}

// decide applies the permission mode to a call of a built-in tool of the given kind.
func (a *agent) decide(kind, name string, input json.RawMessage) verdict {
	mode, rules := a.permissions()
	switch kind {
	case tools.KindRead, tools.KindMeta:
		return verdict{run: true}
	case tools.KindEdit:
		return a.decideEdit(mode, rules, name, input)
	case tools.KindExec:
		return a.decideExec(mode, rules, input)
	case tools.KindFetch:
		return decideFetch(mode, rules, name, input)
	}
	if mode == modeBypass {
		return verdict{run: true}
	}
	if mode == modePlan {
		return verdict{deny: planDenied}
	}
	return verdict{ask: &askSpec{reason: "This tool needs the owner's approval."}}
}

func (a *agent) decideEdit(mode string, rules []rule, name string, input json.RawMessage) verdict {
	path := a.cfg.editPath(a.env, input)
	inside := path != "" && a.inside(path)
	spec := &askSpec{}
	if path != "" && !inside {
		spec.blockedPath = path
	}
	switch mode {
	case modeBypass:
		return verdict{run: true}
	case modePlan:
		return verdict{deny: planDenied}
	}
	if allowedByRule(rules, name, "") {
		return verdict{run: true}
	}
	switch {
	case (mode == modeAccept || mode == modeAuto) && inside:
		return verdict{run: true}
	case path == "":
		spec.reason = "The path this edit would change is not known."
	case !inside:
		spec.reason = "The path is outside the working folders."
	default:
		spec.reason = "Edits need the owner's approval in default mode."
		spec.suggestions = []any{map[string]any{"type": "setMode", "mode": modeAccept, "destination": "session"}}
	}
	return verdict{ask: spec}
}

func (a *agent) decideExec(mode string, rules []rule, input json.RawMessage) verdict {
	cmd := strings.TrimSpace(stringField(input, "command"))
	if cmd == "" {
		return verdict{deny: "Bash needs a command."}
	}
	if p := guard.CommandBlocked(a.cfg.blocked, cmd); p != "" {
		return verdict{ask: &askSpec{reason: fmt.Sprintf("The command matches %s, on the project's list of commands that need the owner's approval.", strconv.Quote(p))}}
	}
	readOnly := a.cfg.readOnly(cmd)
	switch {
	case mode == modeBypass:
		return verdict{run: true}
	case mode == modePlan && readOnly:
		return verdict{run: true}
	case mode == modePlan:
		return verdict{deny: planDenied}
	case mode == modeAuto && readOnly:
		return verdict{run: true}
	case allowedByRule(rules, "Bash", cmd):
		return verdict{run: true}
	}
	spec := &askSpec{reason: fmt.Sprintf("Commands need the owner's approval in %s mode.", mode)}
	if mode == modeAuto {
		spec.reason = "Only read-only commands run without the owner's approval in auto mode."
	}
	if p := commandPrefix(cmd); p != "" && !compound(cmd) {
		spec.suggestions = []any{addRule("Bash", p+":*")}
	}
	return verdict{ask: spec}
}

func decideFetch(mode string, rules []rule, name string, input json.RawMessage) verdict {
	if mode == modeAuto || mode == modeBypass {
		return verdict{run: true}
	}
	host := hostOf(stringField(input, "url"))
	if allowedByRule(rules, name, host) {
		return verdict{run: true}
	}
	spec := &askSpec{reason: fmt.Sprintf("Fetching a URL needs the owner's approval in %s mode.", mode)}
	if host != "" {
		spec.suggestions = []any{addRule(name, "domain:"+host)}
	}
	return verdict{ask: spec}
}

func addRule(tool, content string) map[string]any {
	return map[string]any{"type": "addRules", "rules": []any{map[string]any{"toolName": tool, "ruleContent": content}},
		"behavior": "allow", "destination": "session"}
}

// allowedByRule reports whether a session rule lets a call go ahead: subject is the command of
// a Bash call, the host of a WebFetch call, "" for another tool. A Bash rule never matches a
// compound command, whose other parts the owner did not see in the rule.
func allowedByRule(rules []rule, tool, subject string) bool {
	for _, r := range rules {
		if r.tool != tool {
			continue
		}
		if r.content == "" {
			return true
		}
		switch tool {
		case "Bash":
			if compound(subject) {
				continue
			}
			if p, ok := strings.CutSuffix(r.content, ":*"); ok {
				p = strings.TrimSpace(p)
				if subject == p || strings.HasPrefix(subject, p+" ") {
					return true
				}
			} else if subject == strings.TrimSpace(r.content) {
				return true
			}
		case "WebFetch":
			if d, ok := strings.CutPrefix(r.content, "domain:"); ok && subject != "" && strings.EqualFold(d, subject) {
				return true
			}
		}
	}
	return false
}

// twoWords are the commands whose rule covers a subcommand: "git status", "npm test".
var twoWords = []string{"git", "npm", "pnpm", "yarn", "bun", "npx", "bunx", "deno", "go", "cargo", "make", "docker",
	"podman", "kubectl", "helm", "pip", "pip3", "uv", "poetry", "pipenv", "gh", "mvn", "gradle", "dotnet", "terraform",
	"composer", "bundle", "rails", "rake", "mix", "swift", "flutter", "dart", "brew", "systemctl", "nix"}

// commandPrefix is the start of a command that a session rule allows: its first word, or the
// first two for tools with subcommands ("git log"), or three for "python -m pytest". "" when
// none fits (an environment assignment or a flag where the subcommand would be).
func commandPrefix(cmd string) string {
	words := strings.Fields(cmd)
	if len(words) == 0 || strings.Contains(words[0], "=") {
		return ""
	}
	n := 1
	switch {
	case (words[0] == "python" || words[0] == "python3") && len(words) >= 3 && words[1] == "-m":
		n = 3
	case slices.Contains(twoWords, words[0]) && len(words) >= 2:
		if strings.HasPrefix(words[1], "-") {
			return ""
		}
		n = 2
	}
	return strings.Join(words[:n], " ")
}

// compound reports whether a command runs more than one command: ; && || | & ` $( or a
// newline. The redirections >& and &> are not.
func compound(cmd string) bool {
	if strings.ContainsAny(cmd, ";|`\n") || strings.Contains(cmd, "$(") || strings.Contains(cmd, "<(") || strings.Contains(cmd, ">(") {
		return true
	}
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '&' {
			continue
		}
		if (i > 0 && cmd[i-1] == '>') || (i+1 < len(cmd) && cmd[i+1] == '>') {
			continue
		}
		return true
	}
	return false
}

// stringField is a string field of a tool call's input.
func stringField(input json.RawMessage, name string) string {
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	s, _ := m[name].(string)
	return s
}

// hostOf is a URL's host, lower case; "" when it has none.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// ---- the working folders

// inside reports whether a path is in the working folder or another folder the agent works in,
// comparing real paths: symbolic links resolved, and for a file that does not exist yet the
// folder it would be made in.
func (a *agent) inside(path string) bool {
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.cfg.cwd, path)
	}
	real := realPath(path)
	if real == "" {
		return false
	}
	for _, root := range a.roots {
		if within(root, real) {
			return true
		}
	}
	return false
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// realPath is a path with every symbolic link in it resolved, a link that points nowhere yet
// included (writing it would create its target). The part that does not exist is kept as it
// is under the real path of the part that does. "" when it cannot be resolved.
func realPath(p string) string {
	p = filepath.Clean(p)
	var rest []string
	for hops := 0; hops < 64; hops++ {
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return ""
			}
			if !filepath.IsAbs(target) {
				dir, err := filepath.EvalSymlinks(filepath.Dir(p))
				if err != nil {
					return ""
				}
				target = filepath.Join(dir, target)
			}
			p = filepath.Clean(target)
			continue
		}
		if err == nil {
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				return ""
			}
			return filepath.Join(append([]string{real}, rest...)...)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(append([]string{p}, rest...)...)
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
	return ""
}

// ---- asking the owner

// ask sends a permission request for a tool call and waits for the owner's answer. It reports
// false when the turn was stopped first: the request is then withdrawn.
func (a *agent) ask(ctx context.Context, toolName, useID string, input json.RawMessage, spec *askSpec) (ownerAnswer, bool) {
	ch := make(chan ownerAnswer, 1)
	a.mu.Lock()
	a.reqN++
	id := "nc_" + strconv.Itoa(a.reqN)
	a.pending[id] = ch
	a.mu.Unlock()
	req := map[string]any{"subtype": "can_use_tool", "tool_name": toolName, "input": objectOr(input), "tool_use_id": useID}
	if spec != nil {
		if len(spec.suggestions) > 0 {
			req["permission_suggestions"] = spec.suggestions
		}
		if spec.blockedPath != "" {
			req["blocked_path"] = spec.blockedPath
		}
		if spec.reason != "" {
			req["decision_reason"] = spec.reason
		}
	}
	a.out.request(id, req)
	select {
	case ans := <-ch:
		return ans, true
	case <-ctx.Done():
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
		a.out.cancelRequest(id)
		return ownerAnswer{}, false
	}
}

// answered hands the owner's answer to the request waiting for it.
func (a *agent) answered(raw json.RawMessage) {
	id, ans := parseAnswer(raw)
	a.mu.Lock()
	ch := a.pending[id]
	delete(a.pending, id)
	a.mu.Unlock()
	if ch != nil {
		ch <- ans
	}
}

// apply takes in the rules the owner allowed with an answer, for the rest of the process:
// allow rules, and a new mode. It reports whether the mode changed.
func (a *agent) apply(perms []permUpdate) bool {
	changed := false
	for _, p := range perms {
		switch p.Type {
		case "addRules":
			if p.Behavior != "allow" {
				continue
			}
			a.mu.Lock()
			for _, r := range p.Rules {
				if r.ToolName != "" {
					a.rules = append(a.rules, rule{tool: r.ToolName, content: strings.TrimSpace(r.RuleContent)})
				}
			}
			a.mu.Unlock()
		case "setMode":
			if slices.Contains(modes, p.Mode) && a.setMode(p.Mode) {
				changed = true
			}
		}
	}
	return changed
}
