package agent

import (
	"errors"
	"fmt"
	"strings"
)

// HeadlessPlan runs an agent's CLI without the terminal wrapper: the program and its arguments
// and environment, for NextCode (docs/API-agents.md §30), which drives the CLI over pipes. The
// engine (internal/nextcode) adds its own flags, e.g. Claude Code's stream-json ones.
type HeadlessPlan struct {
	Mode           string // resolved, as Plan.Mode
	AgentSessionID string // as Plan.AgentSessionID: Claude fresh gets a UUID chosen by the hub
	Argv           []string
	Env            []string
	// Check is the program that must resolve on the login PATH.
	Check string
}

// ErrNotHeadless: the agent has no NextCode engine.
var ErrNotHeadless = errors.New("this agent does not run headless")

// ErrHeadlessMode: a headless launch starts fresh, resumes an id or continues; it has no picker.
var ErrHeadlessMode = errors.New("a headless launch cannot open the CLI's picker")

// Headless prepares a launch of a NextCode agent: the same arguments and environment as its
// wrapper launch (Build: login profile or API provider, the hub's context and tools, settings,
// model, effort), assembled here instead of by the wrapper script, with nothing of the
// wrapper's own (no markers, no shell after the agent).
func Headless(a Agent, o Options) (HeadlessPlan, error) {
	if a.Engine == "" || a.Kind != KindAgent {
		return HeadlessPlan{}, ErrNotHeadless
	}
	p, parts, err := buildFull(a, o)
	if err != nil {
		return HeadlessPlan{}, err
	}
	vars := map[string]string{}
	env := make([]string, 0, len(p.Env))
	for _, kv := range p.Env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
		if k == "NB_WRAP" { // the wrapper script: nothing runs it
			continue
		}
		env = append(env, kv)
	}
	id := p.AgentSessionID
	var missing error
	expand := func(args []string) []string {
		out := make([]string, 0, len(args))
		for _, x := range args {
			// "$NB_HOOKS" and its kin: values the wrapper's shell expands from the environment
			if name, ok := strings.CutPrefix(x, "$"); ok && (x == "$NB_HOOKS" || x == "$NB_MCP" || x == "$NB_CODEX_CFG" ||
				x == "$NB_CODEX_PROVIDER" || codexMCPVar.MatchString(x)) {
				v, set := vars[name]
				if !set && missing == nil {
					missing = fmt.Errorf("the launch variable %s is not set", name)
				}
				out = append(out, v)
				continue
			}
			out = append(out, strings.ReplaceAll(x, Placeholder, id))
		}
		return out
	}
	argv := append([]string{parts.command}, expand(a.Args)...)
	r := a.Resume
	switch p.Mode {
	case ModeFresh:
		argv = append(argv, expand(r.LaunchArgs)...)
	case ModeResume:
		argv = append(argv, expand(r.ResumeArgs)...)
	case ModeContinue:
		argv = append(argv, expand(r.ContinueArgs)...)
	default:
		return HeadlessPlan{}, ErrHeadlessMode
	}
	argv = append(argv, expand(parts.args)...)
	if parts.yolo {
		argv = append(argv, expand(parts.yoloArgs)...)
		for _, kv := range parts.yoloEnv {
			if k, _, _ := strings.Cut(kv, "="); envKeyRe.MatchString(k) {
				env = append(env, kv)
			}
		}
	}
	if missing != nil {
		return HeadlessPlan{}, missing
	}
	f := modelFlags[a.CapBase()]
	if parts.model != "" && f.model != "" {
		argv = append(argv, f.model, parts.model)
	}
	if parts.effort != "" && f.effort != "" {
		if flag, ok := strings.CutSuffix(f.effort, "="); ok { // Codex: -c model_reasoning_effort=<v>
			name, value, _ := strings.Cut(flag, " ")
			argv = append(argv, name, value+"="+parts.effort)
		} else {
			argv = append(argv, f.effort, parts.effort)
		}
	}
	return HeadlessPlan{Mode: p.Mode, AgentSessionID: id, Argv: argv, Env: env, Check: parts.command}, nil
}
