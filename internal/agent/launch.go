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
	WorkDir        string // the folder the CLI starts in ("" = not known)
	// Context is what the hub gives this launch (nil = nothing).
	Context *LaunchContext
	// Provider runs the CLI against an API provider instead of its login (provider.go).
	Provider *ProviderLaunch
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
	// Commands are the project's own commands, for a CLI that takes them at launch.
	Commands []ProjectCommand
	// Blocked are the commands that need the owner's approval (guard.go).
	Blocked []string
	// Sandbox limits what the CLI's commands may do; nil = what the CLI does by itself.
	Sandbox *Sandbox
	// AddDirs are folders outside WorkDir the CLI may change as if they were in it: at a project
	// root (docs/API-agents.md §21.2) the real folders behind its symlinks to the parts on This
	// PC, since the CLIs check permissions, and their sandboxes act, on the resolved path.
	AddDirs []string
	// TrustDir: WorkDir is a folder the hub made, which the CLI may trust without asking (Codex:
	// a trusted project for this launch). "" = none.
	TrustDir string
	// NextCode: a NextCode console (docs/API-agents.md §30, Clean NextCode). The CLI is only the
	// way to the model: nothing of its own setup goes in (its settings and hooks, its MCP servers
	// and the account's connectors, its skills, commands, plugins and instructions files), and
	// what it has is NextCode's: Prompt (for Claude Code NextCode's whole system prompt, which
	// replaces Claude Code's; for Codex its developer instructions), MCP (the servers Settings ›
	// NextCode gives it) and NextCodeTools.
	NextCode bool
}

// NextCodeTools are the built-in tools a NextCode console has on Claude Code: the ones NextCode's
// own agent has, by the same names, so a chat works the same on every account. A name the CLI
// does not have is left out by it (TodoWrite is not in every build).
const NextCodeTools = "Read,Write,Edit,Bash,Glob,Grep,WebFetch,WebSearch,TodoWrite,AskUserQuestion,ExitPlanMode"

// NextCodeCleanEnv keeps the account's claude.ai connectors out of a NextCode launch, as
// --strict-mcp-config does.
var NextCodeCleanEnv = []string{"ENABLE_CLAUDEAI_MCP_SERVERS=false"}

// Sandbox is a project's limit on an agent's commands (docs/API-agents.md §27): they write only
// in the working folder, the temp folder and Write, and reach the network only with Network.
// Claude Code builds it with bubblewrap, Codex with Landlock; other CLIs have none.
type Sandbox struct {
	Network bool
	Write   []string
}

// Sandboxes reports whether an agent's CLI can keep its commands in a Sandbox.
func Sandboxes(a Agent) bool {
	b := a.CapBase()
	return b == "claude" || b == "codex"
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

// MissionReadTools only read a team's mission (docs/API-orchestration.md §22), approved without
// asking as well; the team's other tools follow the CLI's rules like memory_save.
var MissionReadTools = []string{"mission_read"}

// CredentialTools are approved without the CLI asking as well: the hub asks the owner in the
// browser for every command that gets a value (docs/API-vault.md), so a second prompt in the
// console would only be a click more.
var CredentialTools = []string{"credential_list", "credential_run", "credential_result"}

// DatabaseTools are approved without the CLI asking too: reads are within the connection's agent
// access, and a write that needs the owner waits for the owner's approval in the browser
// (docs/API-db.md, Agents).
var DatabaseTools = []string{"db_connections", "db_schema", "db_table", "db_query", "db_explain", "db_execute", "db_result", "redis_scan", "redis_get", "redis_command"}

// TakesContext reports whether the hub can hand this CLI instructions, memory
// and tools per launch, without writing to its config. NextCode's own agent takes the context of
// a NextCode console (its folders, the commands that ask, the sandbox, the prompt).
func (a Agent) TakesContext() bool {
	switch a.CapBase() {
	case "claude", "codex", "opencode", "nextcode-api":
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

// ProjectCommand is one command of the project on a server, already disarmed.
type ProjectCommand struct{ Name, Description, Template string }

// contextPlan is how one CLI takes a LaunchContext. yolo, for a server, only
// approves the hub's tools: no CLI gets its skip-everything flag there, since
// that would open This PC again.
type contextPlan struct{ args, yolo, env []string }

func planContext(a Agent, c *LaunchContext) contextPlan {
	var p contextPlan
	if c.NextCode {
		if cp, ok := planNextCode(a, c); ok {
			return cp
		}
	}
	switch a.CapBase() {
	case "claude":
		if c.Prompt != "" {
			p.args = append(p.args, "--append-system-prompt-file", c.PromptFile)
		}
		// The attachments folder and a root's part folders (claudeAddDirs) go in through
		// --settings as permissions.additionalDirectories, not --add-dir: since 2.1.32x Claude
		// Code asks "Adding a new directory … Yes, add it" for every --add-dir in every new
		// session, a second dialog after the folder trust one; a folder from settings joins the
		// workspace the same way without it (measured with 2.1.327).
		mcpTool := func(names []string) string {
			out := make([]string, len(names))
			for i, n := range names {
				out[i] = "mcp__nextboard__" + n
			}
			return strings.Join(out, ",")
		}
		if c.Remote {
			// The memory folder is local, so Read, Write and Edit stay; there
			// is no local Bash, so no command can run on the wrong machine. Skill
			// lets the model use the project's skills, not only the owner's /name.
			p.args = append(p.args, "--tools", "Read,Write,Edit,TodoWrite,Skill", "--mcp-config", "$NB_MCP", "--strict-mcp-config")
			p.yolo = []string{"--allowedTools", "Skill," + mcpTool(RemoteToolNames)}
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
			p.args = append(p.args, "--allowedTools", mcpTool(slices.Concat(MemoryReadTools, MissionReadTools, CredentialTools, DatabaseTools)))
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
		if !c.Remote {
			for _, d := range c.AddDirs {
				p.args = append(p.args, "--add-dir", d)
			}
			if c.TrustDir != "" {
				trusted, _ := json.Marshal(c.TrustDir)
				p.args = append(p.args, "-c", "projects={"+string(trusted)+`={trust_level="trusted"}}`)
			}
		}
		if sb := c.Sandbox; sb != nil && !c.Remote {
			roots, _ := json.Marshal(append([]string{}, sb.Write...)) // a JSON array of strings is a TOML one
			p.args = append(p.args, "--sandbox", "workspace-write",
				"-c", fmt.Sprintf("sandbox_workspace_write.network_access=%t", sb.Network),
				"-c", "sandbox_workspace_write.writable_roots="+string(roots))
			// yolo stops the questions, not the sandbox (its own flag would lift both)
			p.yolo = []string{"--ask-for-approval", "never"}
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
		for _, tool := range slices.Concat(MemoryReadTools, MissionReadTools, CredentialTools, DatabaseTools) {
			perm["nextboard_"+tool] = "allow"
		}
		if len(c.AddDirs) > 0 && !c.Remote {
			// the folders behind the root's links: as if inside the project, whichever path is used
			ext := map[string]any{}
			for _, d := range c.AddDirs {
				ext[d] = "allow"
				ext[strings.TrimSuffix(d, "/")+"/*"] = "allow"
			}
			perm["external_directory"] = ext
		}
		if c.Remote {
			for _, tool := range []string{"bash", "edit", "read", "glob", "grep", "list", "webfetch", "websearch", "task", "todowrite", "lsp", "external_directory"} {
				perm[tool] = "deny"
			}
			perm["skill"] = "allow" // loads the project's skills, which the hub copied next to it
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
		if len(c.Commands) > 0 {
			cmds := map[string]any{}
			for _, x := range c.Commands {
				cmds[x.Name] = map[string]string{"description": x.Description, "template": x.Template}
			}
			cfg["command"] = cmds
		}
		b, _ := json.Marshal(cfg)
		p.env = append(p.env, "OPENCODE_CONFIG_CONTENT="+string(b))
	case "nextcode-api":
		// NextCode's own agent (This PC only): the prompt and its folders as flags of its own, the
		// commands that ask and the sandbox for it to enforce in NB_NEXTCODE_GUARD
		if c.Prompt != "" {
			p.args = append(p.args, "--append-system-prompt-file", c.PromptFile)
		}
		if c.AttachDir != "" {
			p.args = append(p.args, "--add-dir", c.AttachDir)
		}
		if !c.Remote {
			for _, d := range c.AddDirs {
				p.args = append(p.args, "--add-dir", d)
			}
		}
		if g := nextcodeGuard(c.Blocked, c.Sandbox); g != "" {
			p.env = append(p.env, "NB_NEXTCODE_GUARD="+g)
		}
		// the MCP servers Settings › NextCode gives it, their values in variables of their own
		var mcpServers []map[string]any
		if len(c.MCP) > 0 {
			type v struct {
				Name string `json:"name"`
				Var  string `json:"var"`
			}
			vars := func(list []MCPVar) []v {
				out := make([]v, len(list))
				for i, x := range list {
					out[i] = v{x.Name, x.Var}
				}
				return out
			}
			servers := make([]map[string]any, len(c.MCP))
			for i, m := range c.MCP {
				servers[i] = map[string]any{"name": m.Name, "url": m.URL, "headers": vars(m.Headers), "command": m.Command, "args": m.Args, "env": vars(m.Env)}
			}
			mcpServers = servers
		}
		// the hub's: NextCode's agent tool (its sub-agents)
		if c.MCPURL != "" {
			mcpServers = append(mcpServers, map[string]any{"name": "nextboard", "url": c.MCPURL,
				"headers": []map[string]string{{"name": "Authorization", "var": "NB_TOKEN", "prefix": "Bearer "}}})
		}
		if len(mcpServers) > 0 {
			b, _ := json.Marshal(mcpServers)
			p.env = append(p.env, "NB_NEXTCODE_MCP="+string(b))
		}
	}
	return p
}

// claudeSettings is the inline value of --settings: hooks, where this launch keeps
// its auto memory, whether it signs commits, the folders it works in besides its own, the
// commands that ask and the tools allowed by rule. Nothing is written to any settings file.
func claudeSettings(hooks bool, memoryDir string, noCoAuthor bool, blocked, allow, dirs []string, sandbox *Sandbox) string {
	v := map[string]any{}
	if sb := sandbox; sb != nil {
		s := map[string]any{"enabled": true,
			// no command runs outside it: not when it cannot start, not on a retry
			"failIfUnavailable": true, "allowUnsandboxedCommands": false, "autoAllowBashIfSandboxed": true,
			"filesystem": map[string]any{"allowWrite": append([]string{}, sb.Write...)}}
		if sb.Network {
			s["network"] = map[string]any{"allowedDomains": []string{"*"}}
		}
		v["sandbox"] = s
	}
	if len(blocked) > 0 || len(allow) > 0 || len(dirs) > 0 {
		perm := map[string]any{}
		if len(dirs) > 0 {
			perm["additionalDirectories"] = dirs
		}
		if len(blocked) > 0 {
			perm["ask"] = ClaudeAskRules(blocked) // ask wins over allow
		}
		if len(allow) > 0 {
			perm["allow"] = allow
		}
		v["permissions"] = perm
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
		if a == "$NB_HOOKS" || a == "$NB_MCP" || a == "$NB_CODEX_CFG" || a == "$NB_CODEX_PROVIDER" || codexMCPVar.MatchString(a) {
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

// ErrNeedsProvider: NextCode's own engine has no login, it runs on one of the hub's API providers.
var ErrNeedsProvider = errors.New("NextCode's own engine runs on an API provider only")

// Build prepares a launch of agent a.
func Build(a Agent, o Options) (Plan, error) {
	p, _, err := buildFull(a, o)
	return p, err
}

// launchParts are the pieces of a launch that the wrapper script assembles in the shell.
type launchParts struct {
	command  string
	args     []string // after the mode's arguments: live status, context, settings, provider
	yoloArgs []string
	yoloEnv  []string
	yolo     bool
	model    string
	effort   string
}

// buildFull prepares a launch of agent a: the wrapper plan, and the parts a headless launch
// (Headless) runs the CLI with instead of the wrapper.
func buildFull(a Agent, o Options) (Plan, launchParts, error) {
	if a.Kind == KindShell {
		return Plan{Mode: ModeShell, LiveStatus: "off"}, launchParts{}, nil
	}
	if !ValidMode(o.Mode) && o.Mode != "" {
		return Plan{}, launchParts{}, fmt.Errorf("unknown launch mode: %q", o.Mode)
	}
	if a.ProviderOnly() && o.Provider == nil {
		return Plan{}, launchParts{}, ErrNeedsProvider
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
			return Plan{}, launchParts{}, fmt.Errorf("%s cannot work on servers yet", a.Name)
		}
		if a.TakesContext() {
			cp := planContext(a, c)
			wrapperArgs = append(wrapperArgs, cp.args...)
			contextEnv = cp.env
			if cp.yolo != nil {
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
	var sandbox *Sandbox
	if o.Context != nil && !o.Context.Remote && a.CapBase() == "claude" { // on a server its Bash is off, and the hub's tool looks
		blocked, sandbox = o.Context.Blocked, o.Context.Sandbox
	}
	// Claude Code's default, auto mode, checks each action on Anthropic's servers, which a gateway
	// does not reach: every tool call then stops on "Auto mode can't check actions" (Claude Code
	// 2.1.322 on a real router). So a provider console runs as acceptEdits (edits go ahead, commands
	// ask, as that dialog suggests), or by allow rules where the scrub pins the default mode.
	var allow []string
	permissionMode := ""
	if o.Provider != nil && a.CapBase() == "claude" {
		scrubbed := claudeScrubbed(a, o.Provider)
		switch {
		case o.Yolo && a.HasYolo() && scrubbed && (o.Context == nil || !o.Context.Remote):
			// --dangerously-skip-permissions would be dropped as well (on a server the yolo args
			// are already allow rules for the hub's tools)
			a.YoloArgs = claudeScrubYolo(a.YoloArgs, o.Context)
		case o.Yolo && a.HasYolo():
		case scrubbed:
			allow = ClaudeEditRules(o.WorkDir, claudeAddDirs(o.Context, a.TakesContext()))
		default:
			permissionMode = "acceptEdits"
		}
	}
	var dirs []string
	if a.CapBase() == "claude" {
		dirs = claudeAddDirs(o.Context, a.TakesContext())
	}
	if (memoryDir != "" || noCoAuthor || len(blocked) > 0 || sandbox != nil || len(allow) > 0 || len(dirs) > 0) && !claudeHooks {
		wrapperArgs = append(wrapperArgs, "--settings", "$NB_HOOKS")
	}
	model, effort := o.Model, o.Effort
	if o.Provider != nil {
		model = ProviderModel(a, o.Provider, model)
		wrapperArgs = append(wrapperArgs, providerArgs(a, o.Provider)...)
		if permissionMode != "" {
			wrapperArgs = append(wrapperArgs, "--permission-mode", permissionMode)
		}
	} else if !a.HasModels() {
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
	if claudeHooks || memoryDir != "" || noCoAuthor || len(blocked) > 0 || sandbox != nil || len(allow) > 0 || len(dirs) > 0 {
		p.Env = append(p.Env, "NB_HOOKS="+claudeSettings(claudeHooks, memoryDir, noCoAuthor, blocked, allow, dirs, sandbox))
	}
	if o.HookURL != "" {
		p.Env = append(p.Env, "NB_HOOK_URL="+o.HookURL)
	}
	profileDir := o.ProfileDir
	if o.Provider != nil {
		profileDir = o.Provider.ConfigDir
	}
	if a.ProfileEnv != "" && profileDir != "" {
		p.Env = append(p.Env, a.ProfileEnv+"="+profileDir)
	}
	p.Env = append(p.Env, a.Env...)
	n := len(p.Shell)
	for _, kv := range p.Env {
		n += len(machine.Quote(kv)) + 1
	}
	if n > MaxCommandLine/2 {
		return Plan{}, launchParts{}, fmt.Errorf("the launch command line is too long (%d bytes)", n)
	}
	// Only This PC gets these: the process env, never a command line.
	if o.Provider != nil && a.CapBase() == "opencode" {
		contextEnv = withOpenCodeProvider(contextEnv, o.Provider, model)
	}
	p.Env = append(p.Env, contextEnv...)
	if c := o.Context; c != nil && a.TakesContext() {
		p.Env = append(p.Env, c.MCPEnv...)
	}
	if o.Provider != nil {
		p.Env = append(p.Env, providerEnv(a, o.Provider, model)...)
	}
	parts := launchParts{command: command, args: wrapperArgs, yoloArgs: a.YoloArgs, yoloEnv: a.YoloEnv,
		yolo: o.Yolo && a.HasYolo(), model: model, effort: effort}
	return p, parts, nil
}

// planNextCode is a clean NextCode launch on Claude Code or Codex (LaunchContext.NextCode); ok
// false for NextCode's own agent, which has its own case and nothing of a CLI's to keep out.
func planNextCode(a Agent, c *LaunchContext) (contextPlan, bool) {
	var p contextPlan
	switch a.CapBase() {
	case "claude":
		// no settings, hooks or plugins of the login's, no MCP but NextCode's (also none of the
		// account's connectors), no skills or slash commands; NextCode's prompt instead of
		// Claude Code's (--bare would do the same, but it reads no subscription login)
		p.args = append(p.args, "--setting-sources", "", "--strict-mcp-config", "--disable-slash-commands")
		p.env = append(p.env, NextCodeCleanEnv...)
		if c.Prompt != "" {
			p.args = append(p.args, "--system-prompt-file", c.PromptFile)
		}
		var servers []MCPServer
		hub := c.MCPURL // NextCode's agent tool, and a server's tools there
		if hub != "" {
			// spawning sub-agents asks nothing (what they do asks); a call lasts as long as they work
			p.args = append(p.args, "--allowedTools", "mcp__nextboard__agent")
			p.env = append(p.env, "MCP_TOOL_TIMEOUT=3600000")
		}
		if c.Remote {
			// on a server: the local file tools see an empty folder; the work goes through the
			// hub's tools of that server
			p.args = append(p.args, "--tools", "Read,Write,Edit,TodoWrite,AskUserQuestion,ExitPlanMode")
			names := make([]string, len(RemoteToolNames))
			for i, n := range RemoteToolNames {
				names[i] = "mcp__nextboard__" + n
			}
			p.yolo = []string{"--allowedTools", strings.Join(names, ",")}
		} else {
			p.args = append(p.args, "--tools", NextCodeTools)
		}
		servers = c.MCP
		if hub != "" || len(servers) > 0 {
			p.args = append(p.args, "--mcp-config", "$NB_MCP")
			p.env = append(p.env, "NB_MCP="+ClaudeMCPConfig(hub, servers))
		}
		return p, true
	case "codex":
		// its own home already (CODEX_HOME); no instructions files of the project read by
		// itself, no MCP servers of its config: NextCode's only
		p.args = append(p.args, "-c", "project_doc_max_bytes=0", "-c", "mcp_servers={}")
		if c.MCPURL != "" {
			// the hub's: NextCode's agent tool (a call lasts as long as its sub-agents work)
			p.args = append(p.args, "-c", "mcp_servers.nextboard.url="+tomlString(c.MCPURL),
				"-c", `mcp_servers.nextboard.bearer_token_env_var="NB_TOKEN"`, "-c", "mcp_servers.nextboard.tool_timeout_sec=3600")
			if !c.Remote {
				p.args = append(p.args, "-c", `mcp_servers.nextboard.default_tools_approval_mode="approve"`)
			}
		}
		if c.Prompt != "" {
			instructions, _ := json.Marshal(c.Prompt) // a JSON string is a TOML string
			p.args = append(p.args, "-c", "$NB_CODEX_CFG")
			p.env = append(p.env, "NB_CODEX_CFG=developer_instructions="+string(instructions))
		}
		for i, kv := range codexMCPConfig(c.MCP) {
			name := fmt.Sprintf("NB_CODEX_MCP_%d", i)
			p.args = append(p.args, "-c", "$"+name)
			p.env = append(p.env, name+"="+kv)
		}
		for _, d := range c.AddDirs {
			p.args = append(p.args, "--add-dir", d)
		}
		if c.TrustDir != "" {
			trusted, _ := json.Marshal(c.TrustDir)
			p.args = append(p.args, "-c", "projects={"+string(trusted)+`={trust_level="trusted"}}`)
		}
		if sb := c.Sandbox; sb != nil {
			roots, _ := json.Marshal(append([]string{}, sb.Write...))
			p.args = append(p.args, "-c", fmt.Sprintf("sandbox_workspace_write.network_access=%t", sb.Network),
				"-c", "sandbox_workspace_write.writable_roots="+string(roots))
		}
		return p, true
	}
	return p, false
}
