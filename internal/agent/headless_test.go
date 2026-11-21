package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// NextCode (§30) is a built-in next to Claude Code: Claude Code's capabilities, logins and
// conversation files, no terminal state source of its own.
func TestNextCodeBuiltin(t *testing.T) {
	a, ok := builtinByID("nextcode")
	if !ok {
		t.Fatal("no nextcode built-in")
	}
	if a.Engine != "claude" || a.CapBase() != "claude" || a.ProfileEnv != "CLAUDE_CONFIG_DIR" || a.Transcript != TranscriptClaude {
		t.Fatalf("nextcode: %+v", a)
	}
	if !a.HasModels() || !a.HasEfforts() || !a.TakesContext() || a.LiveStatus != LiveNone || a.ScreenRules != "" {
		t.Fatalf("nextcode capabilities: %+v", a)
	}
	// the terminal agents are what they were
	for _, id := range []string{"claude", "codex", "gemini", "shell"} {
		b, _ := builtinByID(id)
		if b.Engine != "" || b.Base != "" || b.CapBase() != id {
			t.Fatalf("%s changed: engine %q base %q", id, b.Engine, b.Base)
		}
	}
}

func headlessArg(argv []string, flag string) (string, bool) {
	i := slices.Index(argv, flag)
	if i < 0 || i+1 >= len(argv) {
		return "", false
	}
	return argv[i+1], true
}

// A headless launch has the wrapper launch's arguments and environment, assembled without
// the wrapper.
func TestHeadlessLaunch(t *testing.T) {
	nc, _ := builtinByID("nextcode")
	ctx := &LaunchContext{MCPURL: "http://127.0.0.1:1/nb/mcp/s_1", MemoryDir: "/data/memory/p", WorkDir: "/home/me/shop",
		Prompt: "be kind", PromptFile: "/data/prompts/s_1.md", AddDirs: []string{"/home/me/shop-api"}}
	p, err := Headless(nc, Options{Mode: ModeFresh, Context: ctx, ProfileDir: "/home/me/.claude-work", Model: "sonnet", Effort: "high",
		Token: "tok", SessionID: "s_1"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != ModeFresh || len(p.AgentSessionID) != 36 || p.Check != "claude" {
		t.Fatalf("plan %+v", p)
	}
	if p.Argv[0] != "claude" {
		t.Fatalf("argv %q", p.Argv)
	}
	if v, _ := headlessArg(p.Argv, "--session-id"); v != p.AgentSessionID {
		t.Fatalf("--session-id %q in %q", v, p.Argv)
	}
	if v, _ := headlessArg(p.Argv, "--append-system-prompt-file"); v != "/data/prompts/s_1.md" {
		t.Fatalf("prompt file %q", v)
	}
	if v, _ := headlessArg(p.Argv, "--model"); v != "sonnet" {
		t.Fatalf("--model %q", v)
	}
	if v, _ := headlessArg(p.Argv, "--effort"); v != "high" {
		t.Fatalf("--effort %q", v)
	}
	// the values the wrapper's shell would expand are in argv; the tokens stay in env
	mcp, _ := headlessArg(p.Argv, "--mcp-config")
	var mc struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(mcp), &mc); err != nil || mc.MCPServers["nextboard"].URL != ctx.MCPURL ||
		mc.MCPServers["nextboard"].Headers["Authorization"] != "Bearer ${NB_TOKEN}" {
		t.Fatalf("--mcp-config %q (%v)", mcp, err)
	}
	settings, _ := headlessArg(p.Argv, "--settings")
	var st struct {
		Permissions struct {
			AdditionalDirectories []string `json:"additionalDirectories"`
		} `json:"permissions"`
		AutoMemoryDirectory string `json:"autoMemoryDirectory"`
	}
	if err := json.Unmarshal([]byte(settings), &st); err != nil || st.AutoMemoryDirectory != "/data/memory/p" ||
		!slices.Equal(st.Permissions.AdditionalDirectories, []string{"/home/me/shop-api"}) {
		t.Fatalf("--settings %q (%v)", settings, err)
	}
	for _, a := range p.Argv {
		if strings.HasPrefix(a, "$") {
			t.Fatalf("unexpanded %q in %q", a, p.Argv)
		}
	}
	env := envMap(p.Env)
	if env["CLAUDE_CONFIG_DIR"] != "/home/me/.claude-work" || env["NB_TOKEN"] != "tok" {
		t.Fatalf("env %v", env)
	}
	if _, ok := env["NB_WRAP"]; ok {
		t.Fatal("the wrapper script is in a headless launch's env")
	}
	// no hooks: the state comes from the stream (settings has no "hooks")
	if strings.Contains(settings, `"hooks"`) {
		t.Fatalf("hooks in %s", settings)
	}

	// resume and continue
	p, err = Headless(nc, Options{Mode: ModeResume, AgentSessionID: "0199a1b2-c3d4-4e5f-8a9b-0c1d2e3f4a5b"})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := headlessArg(p.Argv, "--resume"); v != "0199a1b2-c3d4-4e5f-8a9b-0c1d2e3f4a5b" || slices.Contains(p.Argv, "--session-id") {
		t.Fatalf("resume argv %q", p.Argv)
	}
	p, _ = Headless(nc, Options{Mode: ModeResume})
	if p.Mode != ModeContinue || !slices.Contains(p.Argv, "--continue") {
		t.Fatalf("resume without an id: %+v", p)
	}
	// no Yolo: NextCode has permission modes instead
	if nc.HasYolo() {
		t.Fatal("nextcode has a Yolo flag")
	}
	p, _ = Headless(nc, Options{Mode: ModeFresh, Yolo: true})
	if slices.Contains(p.Argv, "--dangerously-skip-permissions") {
		t.Fatalf("yolo argv %q", p.Argv)
	}

	if _, err := Headless(nc, Options{Mode: ModePick}); !errors.Is(err, ErrHeadlessMode) {
		t.Fatalf("pick: %v", err)
	}
	claude, _ := builtinByID("claude")
	if _, err := Headless(claude, Options{Mode: ModeFresh}); !errors.Is(err, ErrNotHeadless) {
		t.Fatalf("claude headless: %v", err)
	}
}

// With an API provider the headless launch runs against it like a terminal one would.
func TestHeadlessProvider(t *testing.T) {
	nc, _ := builtinByID("nextcode")
	p, err := Headless(nc, Options{Mode: ModeFresh, Provider: &ProviderLaunch{Kind: "anthropic", BaseURL: "https://gw.example/v1", Key: "sk-test",
		ConfigDir: "/data/providers/p1/claude"}})
	if err != nil {
		t.Fatal(err)
	}
	env := envMap(p.Env)
	if env["ANTHROPIC_BASE_URL"] != "https://gw.example/v1" || env["CLAUDE_CONFIG_DIR"] != "/data/providers/p1/claude" {
		t.Fatalf("provider env %v", env)
	}
	if strings.Contains(strings.Join(p.Argv, " "), "sk-test") {
		t.Fatal("the key is in argv")
	}
}

// NextCode on Codex: Codex's launch (its login, model and effort flags); the engine makes it
// `codex app-server` (internal/nextcode).
func TestHeadlessCodex(t *testing.T) {
	nc, ok := builtinByID("nextcode-codex")
	if !ok || nc.Engine != "codex" || nc.CapBase() != "codex" || nc.ProfileEnv != "CODEX_HOME" || nc.HasYolo() || !nc.HasModels() {
		t.Fatalf("nextcode-codex %+v", nc)
	}
	p, err := Headless(nc, Options{Mode: ModeFresh, ProfileDir: "/data/agents/nextcode-codex", Model: "gpt-6.1-sol", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Argv, " ") != "codex -m gpt-6.1-sol -c model_reasoning_effort=high" || p.AgentSessionID != "" {
		t.Fatalf("argv %q id %q", p.Argv, p.AgentSessionID)
	}
	if envMap(p.Env)["CODEX_HOME"] != "/data/agents/nextcode-codex" {
		t.Fatalf("env %v", p.Env)
	}
	p, _ = Headless(nc, Options{Mode: ModeResume, AgentSessionID: "0199a1b2-c3d4-4e5f-8a9b-0c1d2e3f4a5b"})
	if strings.Join(p.Argv, " ") != "codex resume 0199a1b2-c3d4-4e5f-8a9b-0c1d2e3f4a5b" {
		t.Fatalf("resume argv %q", p.Argv)
	}
}

// NextCode's own engine: the hub's own program on an API provider, no CLI to find and no login.
func TestNextCodeAPIBuiltin(t *testing.T) {
	a, ok := builtinByID("nextcode-api")
	if !ok {
		t.Fatal("no nextcode-api built-in")
	}
	if a.Name != "NextCode" || a.Kind != KindAgent || a.Engine != EngineAPI || a.Base != "" || a.CapBase() != "nextcode-api" || a.Color != "#a78bfa" ||
		strings.Join(a.Args, " ") != "nextcode-agent" || a.ProfileEnv != "NEXTCODE_HOME" || a.LiveStatus != LiveNone || a.Transcript != TranscriptNone {
		t.Fatalf("nextcode-api %+v", a)
	}
	r := a.Resume
	if r.Kind != ResumeSessionID || strings.Join(r.LaunchArgs, " ") != "--session-id "+Placeholder || strings.Join(r.ResumeArgs, " ") != "--resume "+Placeholder ||
		strings.Join(r.ContinueArgs, " ") != "--continue" {
		t.Fatalf("resume %+v", r)
	}
	if a.Detect != "" || a.HasYolo() || !a.HasModels() || !a.HasEfforts() || !a.TakesContext() || a.WorksOnServers() || !a.ProviderOnly() || a.TakesProfiles() {
		t.Fatalf("nextcode-api capabilities %+v", a)
	}
	if l := ListModels(context.Background(), a, "", ""); len(l.Models) != 0 || strings.Join(l.Efforts, " ") != "low medium high xhigh max" {
		t.Fatalf("models %+v", l)
	}
	// it takes only its own efforts; a CLI any effort name
	claude, _ := builtinByID("claude")
	if !ValidEffortFor(a, "") || !ValidEffortFor(a, "xhigh") || ValidEffortFor(a, "minimal") || !ValidEffortFor(claude, "minimal") {
		t.Fatal("efforts")
	}
	// the other NextCode engines and the terminal agents keep their logins
	for _, id := range []string{"claude", "codex", "nextcode", "nextcode-codex"} {
		if b, _ := builtinByID(id); b.ProviderOnly() || !b.TakesProfiles() {
			t.Fatalf("%s: provider only %v, profiles %v", id, b.ProviderOnly(), b.TakesProfiles())
		}
	}
}

// Its launch: the hub's own program with the agent's flags, the provider's place and kind, the
// model and effort, NextCode's context; the key only in the environment.
func TestHeadlessAPI(t *testing.T) {
	a, _ := builtinByID("nextcode-api")
	const key = "sk-nextcode-SECRET-0123456789"
	pl := &ProviderLaunch{ID: "ap_test1", Name: "Router", Kind: "openai", BaseURL: "https://router.example/v1", API: "chat", Model: "glm-4.6",
		Key: key, ConfigDir: "/data/providers/ap_test1/nextcode"}
	ctx := &LaunchContext{Prompt: "be careful", PromptFile: "/data/agents/prompts/s_1.md", AttachDir: "/data/attachments",
		AddDirs: []string{"/home/me/shop-api"}, Blocked: []string{"git push --force"}, Sandbox: &Sandbox{Network: true, Write: []string{"/tmp"}}}
	p, err := Headless(a, Options{Mode: ModeFresh, Command: "/opt/nextboard", Context: ctx, Provider: pl, Model: "kimi-k2", Effort: "high",
		Token: "tok", SessionID: "s_1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/nextboard", "nextcode-agent", "--session-id", p.AgentSessionID,
		"--append-system-prompt-file", "/data/agents/prompts/s_1.md", "--add-dir", "/data/attachments", "--add-dir", "/home/me/shop-api",
		"--provider-kind", "openai", "--base-url", "https://router.example/v1", "--api", "chat",
		"--model", "kimi-k2", "--effort", "high"}
	if len(p.AgentSessionID) != 36 || !slices.Equal(p.Argv, want) {
		t.Fatalf("argv\n%q\nwant\n%q", p.Argv, want)
	}
	env := envMap(p.Env)
	if env["NB_PROVIDER_KEY"] != key || env["NEXTCODE_HOME"] != "/data/providers/ap_test1/nextcode" || env["NB_TOKEN"] != "tok" {
		t.Fatalf("env %v", env)
	}
	var guard struct {
		Blocked []string `json:"blocked"`
		Sandbox *struct {
			Network bool     `json:"network"`
			Write   []string `json:"write"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal([]byte(env["NB_NEXTCODE_GUARD"]), &guard); err != nil || !slices.Equal(guard.Blocked, ctx.Blocked) ||
		guard.Sandbox == nil || !guard.Sandbox.Network || !slices.Equal(guard.Sandbox.Write, []string{"/tmp"}) {
		t.Fatalf("NB_NEXTCODE_GUARD %q (%v)", env["NB_NEXTCODE_GUARD"], err)
	}
	for _, x := range p.Argv {
		if strings.Contains(x, key) {
			t.Fatalf("the key is in argv %q", p.Argv)
		}
	}
	for k := range env {
		if k == "NB_WRAP" || k == "NB_HOOKS" || strings.HasPrefix(k, "ANTHROPIC_") {
			t.Fatalf("%s in its env", k)
		}
	}

	// an Anthropic provider with its header, the provider's model, no effort, no key, resume;
	// nothing to guard: no NB_NEXTCODE_GUARD
	pl = &ProviderLaunch{ID: "ap_test2", Kind: "anthropic", BaseURL: "http://127.0.0.1:11434", Auth: "x-api-key", Model: "glm-4.6",
		ConfigDir: "/data/providers/ap_test2/nextcode"}
	p, err = Headless(a, Options{Mode: ModeResume, AgentSessionID: "0199a1b2-c3d4-4e5f-8a9b-0c1d2e3f4a5b", Command: "/opt/nextboard", Provider: pl,
		Context: &LaunchContext{AttachDir: "/data/attachments"}})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"/opt/nextboard", "nextcode-agent", "--resume", "0199a1b2-c3d4-4e5f-8a9b-0c1d2e3f4a5b", "--add-dir", "/data/attachments",
		"--provider-kind", "anthropic", "--base-url", "http://127.0.0.1:11434", "--auth", "x-api-key", "--model", "glm-4.6"}
	if !slices.Equal(p.Argv, want) {
		t.Fatalf("argv\n%q\nwant\n%q", p.Argv, want)
	}
	env = envMap(p.Env)
	if v, ok := env["NB_PROVIDER_KEY"]; !ok || v != "" {
		t.Fatalf("a provider without a key: NB_PROVIDER_KEY %q (set %v)", v, ok)
	}
	if _, ok := env["NB_NEXTCODE_GUARD"]; ok {
		t.Fatalf("a guard with nothing to guard: %q", env["NB_NEXTCODE_GUARD"])
	}
	p, _ = Headless(a, Options{Mode: ModeResume, Command: "/opt/nextboard", Provider: pl})
	if p.Mode != ModeContinue || !slices.Contains(p.Argv, "--continue") {
		t.Fatalf("resume without an id: %+v", p)
	}

	// no provider, no launch
	if _, err := Headless(a, Options{Mode: ModeFresh, Command: "/opt/nextboard", ProfileDir: "/data/agents/nextcode-api"}); !errors.Is(err, ErrNeedsProvider) {
		t.Fatalf("without a provider: %v", err)
	}
	// and never on a server
	if _, err := Headless(a, Options{Mode: ModeFresh, Provider: pl, Context: &LaunchContext{Remote: true}}); err == nil {
		t.Fatal("on a server")
	}
}

// A NextCode console takes nothing of its CLI's own setup (Clean NextCode, docs/API-agents.md
// §30): no settings, hooks, MCP servers, connectors, skills or commands of the login's on Claude
// Code, which runs NextCode's whole prompt and tools; no instructions files or MCP servers of its
// config on Codex. Only NextCode's MCP servers go in.
func TestNextCodeCleanLaunch(t *testing.T) {
	mcp := []MCPServer{{Name: "docs", URL: "https://docs.example/mcp", Headers: []MCPVar{{Name: "Authorization", Var: "NB_MCP_0_DOCS_0"}}}}
	claude := Agent{ID: "nextcode", Kind: KindAgent, Base: "claude", Engine: "claude"}
	c := &LaunchContext{NextCode: true, Prompt: "NextCode's prompt", PromptFile: "/p.md", AddDirs: []string{"/kit/skills"}}
	p := planContext(claude, c)
	got := strings.Join(p.args, " ")
	want := "--setting-sources  --strict-mcp-config --disable-slash-commands --system-prompt-file /p.md --tools " + NextCodeTools
	if got != want || !slices.Contains(p.env, "ENABLE_CLAUDEAI_MCP_SERVERS=false") {
		t.Fatalf("claude, no MCP:\n got %q\nwant %q\nenv %v", got, want, p.env)
	}
	c.MCP = mcp
	p = planContext(claude, c)
	if got := strings.Join(p.args, " "); !strings.HasSuffix(got, "--mcp-config $NB_MCP") || !strings.Contains(strings.Join(p.env, " "), `"docs"`) {
		t.Fatalf("claude, NextCode's MCP: %q %v", got, p.env)
	}
	codex := Agent{ID: "nextcode-codex", Kind: KindAgent, Base: "codex", Engine: "codex"}
	p = planContext(codex, c)
	got = strings.Join(p.args, " ")
	if !strings.HasPrefix(got, "-c project_doc_max_bytes=0 -c mcp_servers={} -c $NB_CODEX_CFG") || strings.Contains(got, "nextboard") ||
		!strings.Contains(got, "--add-dir /kit/skills") || !strings.Contains(strings.Join(p.env, " "), "developer_instructions=") {
		t.Fatalf("codex: %q %v", got, p.env)
	}
	// a terminal agent is not touched
	if p := planContext(Agent{ID: "claude", Kind: KindAgent}, &LaunchContext{Prompt: "x", PromptFile: "/p.md"}); slices.Contains(p.args, "--setting-sources") {
		t.Fatalf("Claude Code's own launch %q", p.args)
	}
}
