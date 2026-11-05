package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func catalogAgent(t *testing.T, id string) Agent {
	t.Helper()
	for _, a := range append(Builtins(), Catalog()...) {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no agent %s", id)
	return Agent{}
}

func TestRemotePlans(t *testing.T) {
	r := &LaunchContext{Remote: true, MCPURL: "http://127.0.0.1:9/nb/mcp/s_1", Prompt: "Work on \"srv\".\nUse <tools>.", PromptFile: "/d/p.md", WorkDir: "/d/.devdata/w", MemoryDir: "/d/memory/p_1"}
	env := func(p Plan, k string) string {
		for _, kv := range p.Env {
			if v, ok := strings.CutPrefix(kv, k+"="); ok {
				return v
			}
		}
		return ""
	}
	for _, id := range []string{"claude", "codex"} {
		a := catalogAgent(t, id)
		if !a.WorksOnServers() {
			t.Fatalf("%s must work on servers", id)
		}
		p, err := Build(a, Options{Mode: ModeFresh, Yolo: true, Token: "sekrit-777", SessionID: "s_1", Context: r})
		if err != nil {
			t.Fatal(err)
		}
		wrap := env(p, "NB_WRAP")
		// Yolo never hands out the flag that would open This PC again.
		if strings.Contains(wrap, "dangerously") {
			t.Fatalf("%s: yolo flag in a server launch:\n%s", id, wrap)
		}
		if strings.Contains(wrap, "sekrit-777") {
			t.Fatalf("%s: token in the wrapper:\n%s", id, wrap)
		}
	}

	wrap := func(id string) string {
		p, err := Build(catalogAgent(t, id), Options{Mode: ModeFresh, Yolo: true, Token: "sekrit-777", SessionID: "s_1", Context: r})
		if err != nil {
			t.Fatal(err)
		}
		return env(p, "NB_WRAP")
	}
	claude := wrap("claude")
	for _, want := range []string{"Read,Write,Edit,TodoWrite", `"$NB_MCP"`, "--strict-mcp-config", "mcp__nextboard__bash"} {
		if !strings.Contains(claude, want) {
			t.Fatalf("claude lacks %q:\n%s", want, claude)
		}
	}
	codex := wrap("codex")
	for _, want := range []string{"shell_tool", "read-only", `"$NB_CODEX_CFG"`, `{trust_level="trusted"}`} {
		if !strings.Contains(codex, want) {
			t.Fatalf("codex lacks %q:\n%s", want, codex)
		}
	}
	p, err := Build(catalogAgent(t, "codex"), Options{Mode: ModeFresh, Token: "sekrit-777", SessionID: "s_1", Context: r})
	if err != nil {
		t.Fatal(err)
	}
	if cfg := env(p, "NB_CODEX_CFG"); !strings.HasPrefix(cfg, "developer_instructions=\"Work on \\\"srv\\\".\\n") {
		t.Fatalf("codex instructions %q", cfg)
	}

	a := catalogAgent(t, "opencode")
	p, err = Build(a, Options{Mode: ModeFresh, Token: "sekrit-777", SessionID: "s_1", Context: r})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCP        map[string]map[string]any `json:"mcp"`
		Permission map[string]string         `json:"permission"`
	}
	raw := env(p, "OPENCODE_CONFIG_CONTENT")
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("opencode config %v: %s", err, raw)
	}
	for _, tool := range []string{"bash", "edit", "read", "glob", "grep", "list"} {
		if cfg.Permission[tool] != "deny" {
			t.Fatalf("opencode keeps %s: %v", tool, cfg.Permission)
		}
	}
	if cfg.MCP["nextboard"]["url"] != r.MCPURL || strings.Contains(raw, "sekrit-777") {
		t.Fatalf("opencode mcp %s", raw)
	}

	// A long prompt does not hit the command line limit: it is process env.
	long := *r
	long.Prompt = strings.Repeat("x", 16<<10)
	if _, err := Build(catalogAgent(t, "codex"), Options{Mode: ModeFresh, Token: "sekrit-777", SessionID: "s_1", Context: &long}); err != nil {
		t.Fatal(err)
	}

	// Others: their local tools cannot be turned off yet.
	for _, id := range []string{"aider", "copilot", "gemini", "goose"} {
		a := catalogAgent(t, id)
		if a.WorksOnServers() {
			t.Fatalf("%s must stay off servers until verified", id)
		}
		if _, err := Build(a, Options{Mode: ModeFresh, Token: "sekrit-777", SessionID: "s_1", Context: r}); err == nil {
			t.Fatalf("%s built a server launch", id)
		}
	}
}

// TestMCPServersPerCLI: the owner's servers reach every CLI, and their values
// stay in the environment: argv can be read by anyone on the machine.
func TestMCPServersPerCLI(t *testing.T) {
	const secret = "tok-very-secret"
	servers := []MCPServer{
		{Name: "docs", URL: "https://mcp.example.com/mcp", Headers: []MCPVar{{Name: "Authorization", Var: "NB_MCP_0_DOCS_0"}}},
		{Name: "tool", Command: "npx", Args: []string{"-y", "my tool"}, Env: []MCPVar{{Name: "API_KEY", Var: "NB_MCP_1_TOOL_1"}}},
	}
	c := &LaunchContext{MCPURL: "http://127.0.0.1:1/nb/mcp/s_1", MemoryDir: "/mem", MCP: servers,
		MCPEnv: []string{"NB_MCP_0_DOCS_0=Bearer " + secret, "NB_MCP_1_TOOL_1=" + secret}}
	for _, id := range []string{"claude", "codex", "opencode"} {
		a := catalogAgent(t, id)
		p, err := Build(a, Options{Mode: ModeFresh, SessionID: "s_1", Token: "t", Context: c})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		wrap := envOf(p, "NB_WRAP")
		if strings.Contains(wrap, secret) || strings.Contains(p.Shell, secret) {
			t.Fatalf("%s: a value reached the command line:\n%s", id, wrap)
		}
		// A variable the wrapper does not expand would reach the CLI as literal text.
		if strings.Contains(wrap, `'$NB_`) {
			t.Fatalf("%s: a quoted variable:\n%s", id, wrap)
		}
		var all strings.Builder
		for _, kv := range p.Env {
			if strings.HasPrefix(kv, "NB_MCP_0_") || strings.HasPrefix(kv, "NB_MCP_1_") {
				continue
			}
			all.WriteString(kv + "\n")
		}
		if strings.Contains(all.String(), secret) {
			t.Fatalf("%s: a value outside its own variable:\n%s", id, all.String())
		}
		if envOf(p, "NB_MCP_0_DOCS_0") != "Bearer "+secret || envOf(p, "NB_MCP_1_TOOL_1") != secret {
			t.Fatalf("%s: values missing from the environment", id)
		}
		text := all.String()
		for _, want := range []string{"mcp.example.com", "npx", "my tool", "NB_MCP_0_DOCS_0", "NB_MCP_1_TOOL_1"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s: no %q in:\n%s", id, want, text)
			}
		}
	}
	// The owner's server does not replace the hub's memory tools.
	if cfg := ClaudeMCPConfig(c.MCPURL, servers); !strings.Contains(cfg, `"nextboard"`) || !strings.Contains(cfg, `"${NB_MCP_0_DOCS_0}"`) || !strings.Contains(cfg, `"${NB_MCP_1_TOOL_1}"`) {
		t.Fatalf("claude %s", cfg)
	}
	// A console with no project has no hub tools to offer, the owner's servers still come.
	if cfg := ClaudeMCPConfig("", servers); strings.Contains(cfg, `"nextboard"`) {
		t.Fatalf("no hub %s", cfg)
	}
	// Codex forwards a variable only under its own name, so two servers can both want API_KEY.
	cx := strings.Join(codexMCPConfig(servers), "\n")
	for _, want := range []string{`mcp_servers.docs.env_http_headers={"Authorization"="NB_MCP_0_DOCS_0"}`, `mcp_servers.tool.command="sh"`, `mcp_servers.tool.env_vars=["NB_MCP_1_TOOL_1"]`, `API_KEY=\"$NB_MCP_1_TOOL_1\" exec`} {
		if !strings.Contains(cx, want) {
			t.Fatalf("codex: no %q in:\n%s", want, cx)
		}
	}
	// Without servers nothing changes.
	if strings.Contains(strings.Join(codexMCPConfig(nil), ""), "mcp_servers") {
		t.Fatal("empty list")
	}
}

// A pasted file waits outside the project: Claude Code may read it without asking.
func TestAttachDirLaunch(t *testing.T) {
	a := catalogAgent(t, "claude")
	plan := planContext(a, &LaunchContext{AttachDir: "/data/attachments"})
	if !strings.Contains(strings.Join(plan.args, " "), "--add-dir /data/attachments") {
		t.Fatalf("args: %v", plan.args)
	}
	if plan = planContext(a, &LaunchContext{}); strings.Contains(strings.Join(plan.args, " "), "--add-dir") {
		t.Fatalf("tanpa folder, tanpa flag: %v", plan.args)
	}
}
