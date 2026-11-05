// Package agent is the agent catalog (docs/API-agents.md §1): built-in
// agents, the catalog of known CLIs, custom agents from presets.json, and
// how a console of an agent is launched through the exit-to-shell wrapper
// with per-launch hook injection (§3, §4).
package agent

import (
	"regexp"
	"strings"
)

// Kinds, sources and capability values.
const (
	KindAgent = "agent"
	KindShell = "shell"

	SourceBuiltin = "builtin"
	SourceCatalog = "catalog"
	SourceCustom  = "custom"

	ResumeSessionID = "session-id"
	ResumeContinue  = "continue"
	ResumePicker    = "picker"
	ResumeNone      = "none"

	LiveClaudeHooks = "claude-hooks"
	LiveCodexNotify = "codex-notify"
	LiveScreen      = "screen"
	LiveNone        = "none"

	TranscriptClaude = "claude"
	TranscriptCodex  = "codex"
	TranscriptNone   = "none"
)

// Placeholder is the only template allowed in agent arguments.
const Placeholder = "{agentSessionId}"

// Resume describes how an agent continues a conversation.
type Resume struct {
	Kind         string   `json:"kind"`
	LaunchArgs   []string `json:"launchArgs"`   // fresh (hub-chosen id)
	ResumeArgs   []string `json:"resumeArgs"`   // exactly agentSessionId
	ContinueArgs []string `json:"continueArgs"` // latest in this folder
	PickerArgs   []string `json:"pickerArgs"`   // the CLI's own picker
}

// Agent is a catalog entry (= Preset v2; the old Preset fields keep their
// meaning so phase-4 clients keep working).
type Agent struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Source      string   `json:"source"`
	Base        string   `json:"base"`
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	Env         []string `json:"env"`
	Color       string   `json:"color"`
	Detect      string   `json:"detect"`
	YoloArgs    []string `json:"yoloArgs"`
	YoloEnv     []string `json:"yoloEnv"`
	Resume      Resume   `json:"resume"`
	LiveStatus  string   `json:"liveStatus"`
	Transcript  string   `json:"transcript"`
	ProfileEnv  string   `json:"profileEnv"`
	ScreenRules string   `json:"screenRules"`
	// ResumeArgs is the legacy field (= Resume.ContinueArgs).
	ResumeArgs []string `json:"resumeArgs"`
	// Available is only set by GET /api/machines/{id}/agents.
	Available *bool `json:"available,omitempty"`
	// Disabled: installed perhaps, but the owner does not want it offered (settings.agents.disabled).
	Disabled bool `json:"disabled,omitempty"`
	// Models and Efforts: a launch can name them (models.go).
	Models  bool `json:"models,omitempty"`
	Efforts bool `json:"efforts,omitempty"`
}

// HasYolo reports whether a yolo launch changes anything.
func (a Agent) HasYolo() bool { return len(a.YoloArgs) > 0 || len(a.YoloEnv) > 0 }

// CapBase is the built-in whose capabilities a console uses: the agent
// itself for built-ins, its base for custom agents.
func (a Agent) CapBase() string {
	if a.Source == SourceCustom {
		return a.Base
	}
	return a.ID
}

// normalize fills nil slices so JSON always has arrays.
func (a Agent) normalize() Agent {
	for _, p := range []*[]string{&a.Args, &a.Env, &a.YoloArgs, &a.YoloEnv, &a.Resume.LaunchArgs,
		&a.Resume.ResumeArgs, &a.Resume.ContinueArgs, &a.Resume.PickerArgs} {
		if *p == nil {
			*p = []string{}
		}
	}
	if a.Resume.Kind == "" {
		a.Resume.Kind = ResumeNone
	}
	a.ResumeArgs = append([]string{}, a.Resume.ContinueArgs...)
	a.Models, a.Efforts = a.HasModels(), a.HasEfforts()
	return a
}

func s(v ...string) []string { return v }

// Builtins are always present (docs/API-agents.md §1.1).
func Builtins() []Agent {
	out := []Agent{
		{ID: "shell", Name: "Shell", Kind: KindShell, Command: "$SHELL", Args: s("-l"), Color: "#8b949e",
			LiveStatus: LiveNone, Transcript: TranscriptNone},
		{ID: "claude", Name: "Claude Code", Kind: KindAgent, Command: "claude", Color: "#d97757", Detect: "claude",
			YoloArgs: s("--dangerously-skip-permissions"),
			Resume: Resume{Kind: ResumeSessionID, LaunchArgs: s("--session-id", Placeholder), ResumeArgs: s("--resume", Placeholder),
				ContinueArgs: s("--continue"), PickerArgs: s("--resume")},
			LiveStatus: LiveClaudeHooks, Transcript: TranscriptClaude, ProfileEnv: "CLAUDE_CONFIG_DIR", ScreenRules: "claude"},
		{ID: "codex", Name: "Codex", Kind: KindAgent, Command: "codex", Color: "#10a37f", Detect: "codex",
			YoloArgs: s("--dangerously-bypass-approvals-and-sandbox"),
			Resume: Resume{Kind: ResumeSessionID, LaunchArgs: s(), ResumeArgs: s("resume", Placeholder),
				ContinueArgs: s("resume", "--last"), PickerArgs: s("resume")},
			LiveStatus: LiveCodexNotify, Transcript: TranscriptCodex, ProfileEnv: "CODEX_HOME", ScreenRules: "codex"},
		// Gemini's `--resume latest` is unverified (not installed on the
		// reference machine): resume stays off until tested.
		{ID: "gemini", Name: "Gemini CLI", Kind: KindAgent, Command: "gemini", Color: "#4285f4", Detect: "gemini",
			YoloArgs: s("--yolo"), LiveStatus: LiveScreen, Transcript: TranscriptNone, ScreenRules: "gemini"},
	}
	for i := range out {
		out[i].Source = SourceBuiltin
		out[i] = out[i].normalize()
	}
	return out
}

// Catalog lists known CLIs (§1.1). Candidates marked † in the contract are
// shipped without yolo/resume until verified on a real CLI version.
func Catalog() []Agent {
	type c struct {
		id, name, cmd, color string
		args, yolo, yoloEnv  []string
		cont                 []string
	}
	list := []c{
		{id: "opencode", name: "OpenCode", cmd: "opencode", color: "#f0b429", cont: s("--continue")},
		{id: "aider", name: "Aider", cmd: "aider", color: "#14b8a6", yolo: s("--yes-always")},
		{id: "amp", name: "Amp", cmd: "amp", color: "#f97316", yolo: s("--dangerously-allow-all")},
		{id: "auggie", name: "Auggie", cmd: "auggie", color: "#22c55e"},
		{id: "cline", name: "Cline CLI", cmd: "cline", color: "#a3a3a3"},
		{id: "codebuff", name: "Codebuff", cmd: "codebuff", color: "#84cc16"},
		{id: "cn", name: "Continue CLI", cmd: "cn", color: "#6366f1"},
		{id: "copilot", name: "GitHub Copilot CLI", cmd: "copilot", color: "#8b5cf6"},
		{id: "crush", name: "Crush", cmd: "crush", color: "#ec4899", yolo: s("--yolo")},
		{id: "cursor-agent", name: "Cursor Agent", cmd: "cursor-agent", color: "#94a3b8"},
		{id: "droid", name: "Droid", cmd: "droid", color: "#f59e0b"},
		{id: "goose", name: "Goose", cmd: "goose", color: "#64748b", yoloEnv: s("GOOSE_MODE=auto")},
		{id: "kimi", name: "Kimi CLI", cmd: "kimi", color: "#0ea5e9"},
		{id: "kiro", name: "Kiro CLI", cmd: "kiro-cli", color: "#a855f7", args: s("chat"), yolo: s("--trust-all-tools")},
		{id: "pi", name: "Pi", cmd: "pi", color: "#10b981"},
		{id: "qwen", name: "Qwen Code", cmd: "qwen", color: "#6d28d9", yolo: s("--approval-mode", "yolo")},
		{id: "vibe", name: "Mistral Vibe", cmd: "vibe", color: "#ff7000"},
	}
	out := make([]Agent, 0, len(list))
	for _, e := range list {
		a := Agent{ID: e.id, Name: e.name, Kind: KindAgent, Source: SourceCatalog, Command: e.cmd, Args: e.args,
			Color: e.color, Detect: e.cmd, YoloArgs: e.yolo, YoloEnv: e.yoloEnv,
			LiveStatus: LiveScreen, Transcript: TranscriptNone}
		if e.cont != nil {
			a.Resume = Resume{Kind: ResumeContinue, ContinueArgs: e.cont}
		}
		if e.id == "opencode" {
			a.ScreenRules = "opencode" // its input prompt only, see inputShapes
		}
		out = append(out, a.normalize())
	}
	return out
}

// defaultOrder is the preference for the default agent of a machine.
var defaultOrder = []string{"claude", "codex", "gemini"}

// DefaultFor picks the default agent: the configured one if available,
// else the first available of claude, codex, gemini, then the catalog
// (alphabetical), then custom agents; "shell" when nothing is available.
func DefaultFor(list []Agent, configured string, available func(Agent) bool) string {
	byID := map[string]Agent{}
	for _, a := range list {
		byID[a.ID] = a
	}
	if a, ok := byID[configured]; ok && available(a) {
		return a.ID
	}
	for _, id := range defaultOrder {
		if a, ok := byID[id]; ok && available(a) {
			return id
		}
	}
	var cat, custom []Agent
	for _, a := range list {
		switch {
		case a.Kind == KindShell:
		case a.Source == SourceCatalog:
			cat = append(cat, a)
		case a.Source == SourceCustom:
			custom = append(custom, a)
		}
	}
	sortByID(cat)
	for _, a := range append(cat, custom...) {
		if available(a) {
			return a.ID
		}
	}
	return "shell"
}

func sortByID(l []Agent) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && l[j].ID < l[j-1].ID; j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

// DetectCommands lists the commands the probe looks for (plus overrides).
func DetectCommands(list []Agent, overrides []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		if c != "" && c != "$SHELL" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, a := range list {
		add(a.Detect)
	}
	for _, o := range overrides {
		add(o)
	}
	return out
}

var templateRe = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_]*\}`)

// badTemplate reports a {template} other than {agentSessionId}.
func badTemplate(args []string) bool {
	for _, a := range args {
		for _, m := range templateRe.FindAllString(a, -1) {
			if m != Placeholder {
				return true
			}
		}
	}
	return false
}

// hasPlaceholder reports whether any arg uses {agentSessionId}.
func hasPlaceholder(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, Placeholder) {
			return true
		}
	}
	return false
}
