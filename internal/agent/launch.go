package agent

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Launch modes (docs/API-agents.md §3.1).
const (
	ModeFresh    = "fresh"
	ModeResume   = "resume"
	ModeContinue = "continue"
	ModePick     = "pick"
	ModeShell    = "shell"
)

// MaxCommandLine is the limit of a remote launch line (tmux messages are
// limited to 16 KiB).
const MaxCommandLine = 12 << 10

// ValidMode reports whether m is a launch mode.
func ValidMode(m string) bool {
	switch m {
	case ModeFresh, ModeResume, ModeContinue, ModePick, ModeShell:
		return true
	}
	return false
}

// Options are the per-launch inputs.
type Options struct {
	Mode           string
	AgentSessionID string // for resume; ignored otherwise
	Yolo           bool
	LiveStatus     bool   // settings.agents.liveStatus
	Command        string // path override ("" = Agent.Command)
	SessionID      string
	Token          string // hookToken
	HookURL        string // HTTP fallback ("" = tty only)
	ProfileDir     string // value for Agent.ProfileEnv ("" = none)
	Model          string // "" = the CLI's own choice
	Effort         string // "" = the CLI's own choice
	CoAuthor       bool   // settings.agents.coAuthor: the CLI may sign its commits
	// Context is what the hub gives this launch (nil = nothing).
	Context *LaunchContext
}

// LaunchContext is what the hub gives a launch, the same for every CLI that
// takes it: instructions and memory that outlive any one agent, the hub's tools,
// and for an agent working on a server, those tools instead of its own.
type LaunchContext struct {
	MCPURL     string // the hub's tool endpoint of this console
	Prompt     string // system prompt, "" = none
	PromptFile string // the same text in a file
	MemoryDir  string // the project's memory folder
	Remote     bool   // works on a server: local tools off
	WorkDir    string // the local folder the CLI runs in
	AttachDir  string // where pasted files wait; a CLI that asks before reading elsewhere may read here
	// MCP are the owner's MCP servers for this launch; MCPEnv holds their
	// secret values, for the process environment only.
	MCP    []MCPServer
	MCPEnv []string
	// Blocked are the commands that need the owner's approval (guard.go).
	Blocked []string
}

// MCPVar is a header or environment variable of an MCP server, whose value is in
// the process environment variable Var. Values never reach a command line.
type MCPVar struct{ Name, Var string }

// MCPServer is one of the owner's MCP servers: a URL or a command.
type MCPServer struct {
	Name    string
	URL     string
	Headers []MCPVar
	Command string
	Args    []string
	Env     []MCPVar
}

// RemoteToolNames are the tools the hub offers on a server.
var RemoteToolNames = []string{"read_file", "write_file", "edit_file", "grep", "glob", "bash", "bash_output", "bash_kill"}

// MemoryReadTools are the hub's tools that only read, approved without asking.
var MemoryReadTools = []string{"memory_search", "memory_read"}

// CredentialTools are approved without the CLI asking as well: the hub asks the owner in the
// browser for every command that gets a value (docs/API-vault.md), so a second prompt in the
// console would only be a click more.
var CredentialTools = []string{"credential_list", "credential_run", "credential_result"}

// TakesContext reports whether the hub can hand this CLI instructions, memory
// and tools per launch, without writing to its config.
func (a Agent) TakesContext() bool {
	switch a.CapBase() {
	case "claude", "codex", "opencode":
		return true
	}
	return false
}

// WorksOnServers reports whether this CLI can work on a server with the hub's
// tools and without its own. It is decided per CLI, on a verified way to turn
// the local tools off: otherwise the agent would quietly act on This PC.
func (a Agent) WorksOnServers() bool {
	switch a.CapBase() {
	case "claude", "codex", "opencode":
		return true
	}
	return false
}

// contextPlan is how one CLI takes a LaunchContext. yolo, for a server, only
// approves the hub's tools: no CLI gets its skip-everything flag there, since
// that would open This PC again.
type contextPlan struct{ args, yolo, env []string }

func planContext(a Agent, c *LaunchContext) contextPlan {
	var p contextPlan
	switch a.CapBase() {
	case "claude":
		if c.Prompt != "" {
			p.args = append(p.args, "--append-system-prompt-file", c.PromptFile)
		}
		if c.AttachDir != "" {
			p.args = append(p.args, "--add-dir", c.AttachDir)
		}
		mcpTool := func(names []string) string {
			out := make([]string, len(names))
			for i, n := range names {
				out[i] = "mcp__nextboard__" + n
			}
			return strings.Join(out, ",")
		}
		if c.Remote {
			// The memory folder is local, so Read, Write and Edit stay; there
			// is no local Bash, so no command can run on the wrong machine.
			p.args = append(p.args, "--tools", "Read,Write,Edit,TodoWrite", "--mcp-config", "$NB_MCP", "--strict-mcp-config")
			p.yolo = []string{"--allowedTools", mcpTool(RemoteToolNames)}
			p.env = append(p.env, "NB_MCP="+ClaudeMCPConfig(c.MCPURL, c.MCP))
		} else if c.MemoryDir != "" || len(c.MCP) > 0 {
			// Adds to the user's own MCP servers.
			hub := c.MCPURL
			if c.MemoryDir == "" { // no project, so the hub has no tools for it
				hub = ""
			}
			p.args = append(p.args, "--mcp-config", "$NB_MCP")
			p.env = append(p.env, "NB_MCP="+ClaudeMCPConfig(hub, c.MCP))
		}
		if c.MemoryDir != "" {
			p.args = append(p.args, "--allowedTools", mcpTool(slices.Concat(MemoryReadTools, CredentialTools)))
		}
	case "codex":
		p.args = append(p.args, "-c", "mcp_servers.nextboard.url="+tomlString(c.MCPURL),
			"-c", `mcp_servers.nextboard.bearer_token_env_var="NB_TOKEN"`)
		if c.Prompt != "" {
			instructions, _ := json.Marshal(c.Prompt) // a JSON string is a TOML string
			p.args = append(p.args, "-c", "$NB_CODEX_CFG")
			p.env = append(p.env, "NB_CODEX_CFG=developer_instructions="+string(instructions))
		}
		// Through the environment too: many servers would not fit a launch line.
		for i, kv := range codexMCPConfig(c.MCP) {
			name := fmt.Sprintf("NB_CODEX_MCP_%d", i)
			p.args = append(p.args, "-c", "$"+name)
			p.env = append(p.env, name+"="+kv)
		}
		if c.Remote {
			workDir, _ := json.Marshal(c.WorkDir)
			p.args = append(p.args, "--disable", "shell_tool", "--disable", "unified_exec", "--sandbox", "read-only",
				"-c", "projects={"+string(workDir)+`={trust_level="trusted"}}`)
			p.yolo = []string{"-c", `mcp_servers.nextboard.default_tools_approval_mode="approve"`}
		}
	case "opencode":
		perm := map[string]any{"nextboard_*": "ask"}
		if len(c.Blocked) > 0 && !c.Remote {
			perm["bash"] = OpenCodeBash(c.Blocked)
		}
		for _, tool := range slices.Concat(MemoryReadTools, CredentialTools) {
			perm["nextboard_"+tool] = "allow"
		}
		if c.Remote {
			for _, tool := range []string{"bash", "edit", "read", "glob", "grep", "list", "webfetch", "websearch", "task", "todowrite", "skill", "lsp", "external_directory"} {
				perm[tool] = "deny"
			}
		}
		servers := map[string]any{"nextboard": map[string]any{"type": "remote", "url": c.MCPURL, "oauth": false,
			"headers": map[string]string{"Authorization": "Bearer {env:NB_TOKEN}"}}}
		for _, m := range c.MCP {
			perm[m.Name+"_*"] = "ask"
			if m.URL != "" {
				servers[m.Name] = map[string]any{"type": "remote", "url": m.URL, "oauth": false, "headers": mcpVarMap(m.Headers, "{env:%s}")}
			} else {
				servers[m.Name] = map[string]any{"type": "local", "command": append([]string{m.Command}, m.Args...), "environment": mcpVarMap(m.Env, "{env:%s}")}
			}
		}
		cfg := map[string]any{"mcp": servers, "permission": perm}
		if c.Prompt != "" {
			cfg["instructions"] = []string{c.PromptFile}
		}
		b, _ := json.Marshal(cfg)
		p.env = append(p.env, "OPENCODE_CONFIG_CONTENT="+string(b))
	}
	return p
}

// claudeSettings is the inline value of --settings: hooks, where this launch keeps
// its auto memory, and whether it signs commits. Nothing is written to any settings file.
func claudeSettings(hooks bool, memoryDir string, noCoAuthor bool, blocked []string) string {
	v := map[string]any{}
	if len(blocked) > 0 {
		v["permissions"] = map[string]any{"ask": ClaudeAskRules(blocked)}
	}
	if hooks {
		_ = json.Unmarshal([]byte(HooksJSON()), &v)
	}
	if memoryDir != "" {
		v["autoMemoryDirectory"] = memoryDir
	}
	if noCoAuthor {
		// attribution is the current name; older versions only know the other
		v["attribution"] = map[string]string{"commit": "", "pr": ""}
		v["includeCoAuthoredBy"] = false
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func tomlString(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ClaudeMCPConfig is the value of --mcp-config. It keeps tokens out of argv: the
// CLI expands ${NB_TOKEN} and the variables of the owner's servers. hubURL "" leaves
// the hub's own tools out.
func ClaudeMCPConfig(hubURL string, servers []MCPServer) string {
	list := map[string]any{}
	if hubURL != "" {
		list["nextboard"] = map[string]any{"type": "http", "url": hubURL, "headers": map[string]string{"Authorization": "Bearer ${NB_TOKEN}"}}
	}
	for _, m := range servers {
		if m.URL != "" {
			list[m.Name] = map[string]any{"type": "http", "url": m.URL, "headers": mcpVarMap(m.Headers, "${%s}")}
		} else {
			list[m.Name] = map[string]any{"type": "stdio", "command": m.Command, "args": m.Args, "env": mcpVarMap(m.Env, "${%s}")}
		}
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": list})
	return string(b)
}

func mcpVarMap(vars []MCPVar, ref string) map[string]string {
	out := map[string]string{}
	for _, v := range vars {
		out[v.Name] = fmt.Sprintf(ref, v.Var)
	}
	return out
}

// codexMCPVar is how a Codex launch names its overrides of the owner's servers.
var codexMCPVar = regexp.MustCompile(`^\$NB_CODEX_MCP_[0-9]+$`)

// codexMCPConfig gives the -c overrides of the owner's servers. Codex takes headers
// from named variables. For a command it only forwards variables under their own
// name, so a small sh gives each its real name: two servers can both want API_KEY.
func codexMCPConfig(servers []MCPServer) []string {
	var out []string
	for _, m := range servers {
		key := "mcp_servers." + m.Name + "."
		if m.URL != "" {
			out = append(out, key+"url="+tomlString(m.URL))
			if len(m.Headers) > 0 {
				parts := make([]string, len(m.Headers))
				for i, h := range m.Headers {
					parts[i] = tomlString(h.Name) + "=" + tomlString(h.Var)
				}
				out = append(out, key+"env_http_headers={"+strings.Join(parts, ",")+"}")
			}
			continue
		}
		command, args := m.Command, m.Args
		if len(m.Env) > 0 {
			var script strings.Builder
			names := make([]string, len(m.Env))
			for i, v := range m.Env {
				script.WriteString(v.Name + `="$` + v.Var + `" `)
				names[i] = v.Var
			}
			script.WriteString(`exec "$0" "$@"`)
			command, args = "sh", append([]string{"-c", script.String(), m.Command}, m.Args...)
			list, _ := json.Marshal(names)
			out = append(out, key+"env_vars="+string(list))
		}
		list, _ := json.Marshal(args)
		if args == nil {
			list = []byte("[]")
		}
		out = append(out, key+"command="+tomlString(command), key+"args="+string(list))
	}
	return out
}

// Plan is what the session hands to machine.Start.
type Plan struct {
	Mode           string // resolved (resume without id → continue)
	AgentSessionID string // Claude fresh: new UUID chosen by the hub
	Shell          string // command line for the login shell
	Env            []string
	Check          string // command that must exist (local pre-check)
	// LiveStatus is the initial Session.liveStatus: "pending" (hooks or
	// notify injected, waiting for the first event), "screen", or "off".
	LiveStatus string
	Wrapped    bool // false for kind shell (legacy `exec $SHELL -l`)
}

// NewUUID returns a random RFC 4122 v4 UUID (Claude --session-id).
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ResolveMode applies the fallbacks of §3.1 for an agent.
func ResolveMode(a Agent, mode, id string) (string, string) {
	if mode == "" {
		mode = ModeFresh
	}
	if mode == ModeResume && id == "" {
		mode = ModeContinue
	}
	if mode == ModeResume && a.Resume.Kind != ResumeSessionID {
		mode = ModeContinue
	}
	if mode != ModeResume {
		id = ""
	}
	if mode == ModeFresh && hasPlaceholder(a.Resume.LaunchArgs) {
		id = NewUUID()
	}
	return mode, id
}

// LiveArgs are the live-status arguments appended for a launch (empty when
// the agent has no injection or it is switched off).
func LiveArgs(a Agent, on bool) []string {
	if !on {
		return nil
	}
	switch a.LiveStatus {
	case LiveClaudeHooks:
		return []string{"--settings", "$NB_HOOKS"} // expanded by the wrapper
	case LiveCodexNotify:
		return []string{"-c", CodexNotify}
	}
	return nil
}

// CodexNotify is the TOML value of `-c notify=…`: Codex appends the
// notification JSON as the last argv, which NB_HOOK_SH reads as $3.
const CodexNotify = `notify=["sh","-c","eval \"$NB_HOOK_SH\"","nb","notify","turn"]`

// HookEvents are the Claude Code events injected with --settings.
var HookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "SubagentStop", "SessionEnd"}

// HooksJSON is NB_HOOKS: hooks for --settings as an inline JSON string
// (nothing is written to any settings file).
func HooksJSON() string {
	hooks := map[string]any{}
	for _, ev := range HookEvents {
		h := map[string]any{"hooks": []any{map[string]any{
			"type": "command", "timeout": 5,
			"command": `set -- hook ` + ev + `; eval "$NB_HOOK_SH"`,
		}}}
		if ev == "PreToolUse" || ev == "PostToolUse" {
			h["matcher"] = "*"
		}
		hooks[ev] = []any{h}
	}
	b, _ := json.Marshal(map[string]any{"hooks": hooks})
	return string(b)
}

// HookScript is NB_HOOK_SH ($1 = marker type, $2 = event, $3 = JSON from
// argv when present, else stdin). It never writes to stdout (hook output
// of SessionStart/UserPromptSubmit becomes model context) and always exits
// 0. Channels, in order (verified on Claude Code 2.1.312: hooks run in a
// new session without a controlling terminal, so /dev/tty fails there):
//
//  1. /dev/tty;
//  2. the terminal of the parent CLI (/proc/$PPID/fd/0, else ps -o tty=),
//     written directly — the hub strips the marker bytes, so interleaving
//     with the TUI's own writes is harmless;
//  3. POST $NB_HOOK_URL with curl (loopback / reverse port-forward).
//
// NB_HOOK_CH=tty skips 3, NB_HOOK_CH=http skips 1–2 (settings hookChannel).
const HookScript = `if [ -n "$3" ]; then r=$(printf %s "$3" | head -c 3072 | base64 | tr -d '\n')
else r=$(head -c 3072 | base64 | tr -d '\n'); cat >/dev/null; fi
p=$(printf '{"k":"%s","ev":"%s","raw":"%s"}' "$NB_TOKEN" "$2" "$r" | base64 | tr -d '\n=' | tr '+/' '-_')
if [ -n "$TMUX" ]; then f='\033Ptmux;\033\033]777;nb;%s;%s\007\033\\'; else f='\033]777;nb;%s;%s\007'; fi
nb_t() { { printf "$f" "$1" "$p" >/dev/tty; } 2>/dev/null && return 0
  t=$(readlink "/proc/$PPID/fd/0" 2>/dev/null)
  case "$t" in /dev/pts/*|/dev/tty?*) ;; *) t=/dev/$(ps -o tty= -p "$PPID" 2>/dev/null | tr -d ' ');; esac
  case "$t" in /dev/pts/*|/dev/tty?*) ;; *) return 1;; esac
  [ -c "$t" ] && { printf "$f" "$1" "$p" >"$t"; } 2>/dev/null; }
nb_h() { [ -n "$NB_HOOK_URL" ] && curl -fsS -m 2 --data-binary "$1;$p" "$NB_HOOK_URL" >/dev/null 2>&1; }
case "$NB_HOOK_CH" in http) nb_h "$1";; tty) nb_t "$1";; *) nb_t "$1" || nb_h "$1";; esac
exit 0`

// shellWords turns args into shell words; {agentSessionId} becomes "$2"
// (the wrapper's session id argument) and "$NB_HOOKS" stays an expansion.
func shellWords(args []string) string {
	var out []string
	for _, a := range args {
		if a == "$NB_HOOKS" || a == "$NB_MCP" || a == "$NB_CODEX_CFG" || codexMCPVar.MatchString(a) {
			out = append(out, `"`+a+`"`)
			continue
		}
		parts := strings.Split(a, Placeholder)
		for i := range parts {
			parts[i] = machine.Quote(parts[i])
			if parts[i] == "''" && len(parts) > 1 {
				parts[i] = ""
			}
		}
		w := strings.Join(parts, `"$2"`)
		if w == "" {
			w = "''"
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

func setArgs(args []string) string {
	if len(args) == 0 {
		return ":"
	}
	return `set -- "$@" ` + shellWords(args)
}

// WrapperScript is NB_WRAP for one agent (§3.2): it announces the agent,
// runs it, reports the exit code, and drops into the user's login shell in
// place. $1 = mode, $2 = agent session id or "-", $3 = yolo 0|1.
func WrapperScript(a Agent, command string, liveArgs []string) string {
	if command == "" {
		command = a.Command
	}
	var b strings.Builder
	b.WriteString(`nb_m() { p=$(printf '%s' "$2" | base64 | tr -d '\n=' | tr '+/' '-_')
  if [ -n "$TMUX" ]; then printf '\033Ptmux;\033\033]777;nb;%s;%s\007\033\\' "$1" "$p"
  else printf '\033]777;nb;%s;%s\007' "$1" "$p"; fi >/dev/tty 2>/dev/null; }
[ "$1" = shell ] && exec "${SHELL:-/bin/sh}" -l
trap : INT QUIT
nb_m agent-start "{\"k\":\"$NB_TOKEN\",\"pid\":$$,\"mode\":\"$1\",\"sid\":\"$2\"}"
`)
	b.WriteString(`set -- "$1" "$2" "$3" ` + shellWords(append([]string{command}, a.Args...)) + "\n")
	r := a.Resume
	b.WriteString(`case "$1" in fresh) ` + setArgs(r.LaunchArgs) + `;; resume) ` + setArgs(r.ResumeArgs) +
		`;;` + "\n  " + `continue) ` + setArgs(r.ContinueArgs) + `;; pick) ` + setArgs(pickArgs(r)) + `;; esac` + "\n")
	if len(liveArgs) > 0 {
		b.WriteString(setArgs(liveArgs) + "\n")
	}
	if len(a.YoloArgs) > 0 {
		b.WriteString(`[ "$3" = 1 ] && ` + setArgs(a.YoloArgs) + "\n")
	}
	for _, kv := range a.YoloEnv {
		k, v, _ := strings.Cut(kv, "=")
		if envKeyRe.MatchString(k) {
			b.WriteString(`[ "$3" = 1 ] && export ` + k + "=" + machine.Quote(v) + "\n")
		}
	}
	b.WriteString(modelScript(a))
	b.WriteString(`command -v "$4" >/dev/null 2>&1 || printf '\r\n\033[1;31mnextboard: command not found: %s\033[0m\r\n' "$4"
shift 3; "$@"; nb_rc=$?
trap - INT QUIT
nb_m agent-exit "{\"k\":\"$NB_TOKEN\",\"rc\":$nb_rc}"
exec "${SHELL:-/bin/sh}" -l`)
	return b.String()
}

// pickArgs: the CLI's own picker, else "latest in this folder".
func pickArgs(r Resume) []string {
	if len(r.PickerArgs) > 0 {
		return r.PickerArgs
	}
	return r.ContinueArgs
}

// WrapperLine is what runs the wrapper; also typed into an idle shell by
// POST /api/sessions/{id}/agent (leading space keeps it out of history).
func WrapperLine(mode, id string, yolo bool, model, effort string) string {
	if id == "" {
		id = "-"
	}
	y := "0"
	if yolo {
		y = "1"
	}
	env := ""
	if model != "" {
		env += "NB_MODEL=" + machine.Quote(model) + " "
	}
	if effort != "" {
		env += "NB_EFFORT=" + machine.Quote(effort) + " "
	}
	return env + `exec /bin/sh -c "$NB_WRAP" nb ` + mode + " " + machine.Quote(id) + " " + y
}

// ErrNotWrapped: shell agents run without the wrapper.
var ErrNotWrapped = errors.New("shell agents do not use the wrapper")

// Build prepares a launch of agent a.
func Build(a Agent, o Options) (Plan, error) {
	if a.Kind == KindShell {
		return Plan{Mode: ModeShell, LiveStatus: "off"}, nil
	}
	if !ValidMode(o.Mode) && o.Mode != "" {
		return Plan{}, fmt.Errorf("unknown launch mode: %q", o.Mode)
	}
	mode, id := ResolveMode(a, o.Mode, o.AgentSessionID)
	command := o.Command
	if command == "" {
		command = a.Command
	}
	live := LiveArgs(a, o.LiveStatus)
	wrapperArgs := slices.Clone(live)
	var contextEnv []string
	memoryDir := ""
	if c := o.Context; c != nil {
		if c.Remote && !a.WorksOnServers() {
			return Plan{}, fmt.Errorf("%s cannot work on servers yet", a.Name)
		}
		if a.TakesContext() {
			cp := planContext(a, c)
			wrapperArgs = append(wrapperArgs, cp.args...)
			contextEnv = cp.env
			if c.Remote {
				a.YoloArgs, a.YoloEnv = cp.yolo, nil
			}
			if a.CapBase() == "claude" {
				memoryDir = c.MemoryDir
			}
		}
	}
	claudeHooks := a.LiveStatus == LiveClaudeHooks && len(live) > 0
	noCoAuthor := a.CapBase() == "claude" && !o.CoAuthor
	var blocked []string
	if o.Context != nil && !o.Context.Remote && a.CapBase() == "claude" { // on a server its Bash is off, and the hub's tool looks
		blocked = o.Context.Blocked
	}
	if (memoryDir != "" || noCoAuthor || len(blocked) > 0) && !claudeHooks {
		wrapperArgs = append(wrapperArgs, "--settings", "$NB_HOOKS")
	}
	model, effort := o.Model, o.Effort
	if !a.HasModels() {
		model = ""
	}
	if !a.HasEfforts() {
		effort = ""
	}
	p := Plan{Mode: mode, AgentSessionID: id, Check: command, Wrapped: true,
		Shell: WrapperLine(mode, id, o.Yolo && a.HasYolo(), model, effort)}
	switch {
	case len(live) > 0:
		p.LiveStatus = "pending"
	case !o.LiveStatus && a.LiveStatus != LiveNone:
		p.LiveStatus = "off"
	case a.LiveStatus == LiveNone:
		p.LiveStatus = "off"
	default:
		p.LiveStatus = "screen"
	}
	p.Env = []string{
		"NB_TOKEN=" + o.Token,
		"NB_WRAP=" + WrapperScript(a, command, wrapperArgs),
		"NB_HOOK_SH=" + HookScript,
		"TERM_PROGRAM=nextboard",
	}
	if claudeHooks || memoryDir != "" || noCoAuthor || len(blocked) > 0 {
		p.Env = append(p.Env, "NB_HOOKS="+claudeSettings(claudeHooks, memoryDir, noCoAuthor, blocked))
	}
	if o.HookURL != "" {
		p.Env = append(p.Env, "NB_HOOK_URL="+o.HookURL)
	}
	if a.ProfileEnv != "" && o.ProfileDir != "" {
		p.Env = append(p.Env, a.ProfileEnv+"="+o.ProfileDir)
	}
	p.Env = append(p.Env, a.Env...)
	n := len(p.Shell)
	for _, kv := range p.Env {
		n += len(machine.Quote(kv)) + 1
	}
	if n > MaxCommandLine/2 {
		return Plan{}, fmt.Errorf("the launch command line is too long (%d bytes)", n)
	}
	// Only This PC gets these: the process env, never a command line.
	p.Env = append(p.Env, contextEnv...)
	if c := o.Context; c != nil && a.TakesContext() {
		p.Env = append(p.Env, c.MCPEnv...)
	}
	return p, nil
}
