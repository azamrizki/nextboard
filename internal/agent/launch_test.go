package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// At a project root (docs/API-agents.md §21.2) the real folders behind the root's links are the
// CLI's own: Claude Code and Codex get --add-dir for each, OpenCode an external_directory allow,
// and Codex trusts the hub-made root. On a server none of it applies.
func TestRootLaunchDirs(t *testing.T) {
	ctx := func(remote bool) *LaunchContext {
		return &LaunchContext{MCPURL: "http://127.0.0.1:1/nb/mcp/s", MemoryDir: "/data/memory/p", WorkDir: "/data/roots/p_shop",
			AddDirs: []string{"/home/me/shop-web", "/home/me/shop-api"}, TrustDir: "/data/roots/p_shop", Remote: remote}
	}
	claude, _ := builtinByID("claude")
	p, err := Build(claude, Options{Mode: ModeFresh, Context: ctx(false)})
	if err != nil {
		t.Fatal(err)
	}
	wrap := envMap(p.Env)["NB_WRAP"]
	for _, want := range []string{"--add-dir /home/me/shop-web", "--add-dir /home/me/shop-api"} {
		if !strings.Contains(wrap, want) {
			t.Fatalf("claude: no %s in %s", want, wrap)
		}
	}
	p, _ = Build(claude, Options{Mode: ModeFresh, Context: ctx(true)})
	if strings.Contains(envMap(p.Env)["NB_WRAP"], "shop-web") {
		t.Fatal("claude on a server gets the local folders")
	}

	codex, _ := builtinByID("codex")
	p, _ = Build(codex, Options{Mode: ModeResume, AgentSessionID: "0199a1b2-c3d4-4e5f-8a9b-0c1d2e3f4a5b", Context: ctx(false)})
	wrap = envMap(p.Env)["NB_WRAP"]
	for _, want := range []string{"--add-dir /home/me/shop-web", "--add-dir /home/me/shop-api", `projects={"/data/roots/p_shop"={trust_level="trusted"}}`} {
		if !strings.Contains(wrap, want) {
			t.Fatalf("codex: no %s in %s", want, wrap)
		}
	}
	p, _ = Build(codex, Options{Mode: ModeFresh, Context: ctx(true)})
	if strings.Contains(envMap(p.Env)["NB_WRAP"], "--add-dir") {
		t.Fatal("codex on a server gets the local folders")
	}

	opencode, _ := builtinByID("opencode")
	p, _ = Build(opencode, Options{Mode: ModeFresh, Context: ctx(false)})
	var cfg struct {
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(envMap(p.Env)["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
		t.Fatal(err)
	}
	ext, _ := cfg.Permission["external_directory"].(map[string]any)
	for _, k := range []string{"/home/me/shop-web", "/home/me/shop-web/*", "/home/me/shop-api/*"} {
		if ext[k] != "allow" {
			t.Fatalf("opencode external_directory %v", cfg.Permission)
		}
	}
	p, _ = Build(opencode, Options{Mode: ModeFresh, Context: ctx(true)})
	_ = json.Unmarshal([]byte(envMap(p.Env)["OPENCODE_CONFIG_CONTENT"]), &cfg)
	if cfg.Permission["external_directory"] != "deny" {
		t.Fatalf("opencode on a server: %v", cfg.Permission["external_directory"])
	}
}
