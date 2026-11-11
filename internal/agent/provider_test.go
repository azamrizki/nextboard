package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

const testKey = "sk-test-SECRET-1234567890"

func provider(kind, api, auth string) *ProviderLaunch {
	return &ProviderLaunch{ID: "ap_test1234", Name: `GLM "work"`, Kind: kind, BaseURL: "https://api.example.com/anthropic", API: api, Auth: auth,
		Model: "glm-4.6", FastModel: "glm-4.5-air", Key: testKey, ConfigDir: "/data/providers/ap_test1234/x"}
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// The key goes into the process environment and nowhere a command line or a client could show it.
func assertKeyOnlyInEnv(t *testing.T, p Plan) {
	t.Helper()
	if strings.Contains(p.Shell, testKey) {
		t.Fatal("the key is on the wrapper's command line")
	}
	for _, kv := range p.Env {
		if strings.HasPrefix(kv, "NB_WRAP=") && strings.Contains(kv, testKey) {
			t.Fatal("the key is in the wrapper script")
		}
	}
}

func TestProviderClaude(t *testing.T) {
	a, _ := builtinByID("claude")
	p, err := Build(a, Options{Mode: ModeFresh, ProfileDir: "/profiles/pf_x", Provider: provider("anthropic", "", "bearer")})
	if err != nil {
		t.Fatal(err)
	}
	assertKeyOnlyInEnv(t, p)
	env := envMap(p.Env)
	for k, want := range map[string]string{
		"CLAUDE_CONFIG_DIR":                        "/data/providers/ap_test1234/x", // not the profile: the subscription login stays apart
		"ANTHROPIC_BASE_URL":                       "https://api.example.com/anthropic",
		"ANTHROPIC_AUTH_TOKEN":                     testKey,
		"ANTHROPIC_API_KEY":                        "",
		"ANTHROPIC_MODEL":                          "glm-4.6",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":             "glm-4.6",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":            "glm-4.5-air",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	} {
		if got, ok := env[k]; !ok || got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(p.Shell, "NB_MODEL=glm-4.6") {
		t.Fatalf("model: %s", p.Shell)
	}
	// auto mode cannot reach Anthropic's check through a gateway: edits go ahead, commands ask
	if !strings.Contains(p.Shell+envMap(p.Env)["NB_WRAP"], "acceptEdits") {
		t.Fatalf("permission mode: %s", envMap(p.Env)["NB_WRAP"])
	}
	if y, _ := Build(a, Options{Mode: ModeFresh, Yolo: true, Provider: provider("anthropic", "", "bearer")}); strings.Contains(envMap(y.Env)["NB_WRAP"], "acceptEdits") {
		t.Fatal("a yolo console keeps skipping permissions")
	}
	if l, _ := Build(a, Options{Mode: ModeFresh}); strings.Contains(envMap(l.Env)["NB_WRAP"], "acceptEdits") {
		t.Fatal("a login console keeps its own mode")
	}
	// the commands its tools run keep the key only where bubblewrap lets Claude Code scrub it
	if _, ok := env["CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"]; ok {
		t.Fatal("scrub without bubblewrap")
	}
	scrub := provider("anthropic", "", "bearer")
	scrub.Scrub = true
	p, _ = Build(a, Options{Mode: ModeFresh, Provider: scrub})
	if envMap(p.Env)["CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"] != "1" {
		t.Fatal("no scrub with bubblewrap")
	}
	// the provider's own header
	p, _ = Build(a, Options{Mode: ModeFresh, Provider: provider("anthropic", "", "x-api-key")})
	if env := envMap(p.Env); env["ANTHROPIC_API_KEY"] != testKey || env["ANTHROPIC_AUTH_TOKEN"] != "" {
		t.Fatalf("x-api-key: %v", env)
	}
	// a model chosen for the console wins
	p, _ = Build(a, Options{Mode: ModeFresh, Model: "glm-4.5", Provider: provider("anthropic", "", "bearer")})
	if envMap(p.Env)["ANTHROPIC_MODEL"] != "glm-4.5" {
		t.Fatal("the chosen model is not used")
	}
}

func TestProviderCodex(t *testing.T) {
	a, _ := builtinByID("codex")
	pr := provider("openai", "responses", "")
	pr.BaseURL = "https://openrouter.ai/api/v1"
	p, err := Build(a, Options{Mode: ModeFresh, Provider: pr})
	if err != nil {
		t.Fatal(err)
	}
	assertKeyOnlyInEnv(t, p)
	env := envMap(p.Env)
	if env["CODEX_HOME"] != pr.ConfigDir || env["NB_PROVIDER_KEY"] != testKey {
		t.Fatalf("env %v", env)
	}
	table := env["NB_CODEX_PROVIDER"]
	if table != `model_providers.nextboard={name="GLM \"work\"",base_url="https://openrouter.ai/api/v1",env_key="NB_PROVIDER_KEY",wire_api="responses"}` {
		t.Fatalf("table %s", table)
	}
	wrap := env["NB_WRAP"]
	if !strings.Contains(wrap, `'model_provider="nextboard"'`) || !strings.Contains(wrap, `"$NB_CODEX_PROVIDER"`) {
		t.Fatalf("wrapper: %s", wrap)
	}
	// Codex's TUI would write the environment, key and all, to shell_snapshots
	if !strings.Contains(wrap, "features.shell_snapshot=false") || !strings.Contains(wrap, "features.shell_snapshot_v2=false") {
		t.Fatalf("shell snapshots stay on: %s", wrap)
	}
	// and the commands it runs do not get the key
	if !strings.Contains(wrap, `shell_environment_policy.exclude=["NB_PROVIDER_KEY","NB_CODEX_PROVIDER"]`) {
		t.Fatalf("commands get the key: %s", wrap)
	}
	if args := strings.Join(ProviderRunnerArgs(a, provider("openai", "responses", "")), " "); !strings.Contains(args, "shell_environment_policy.exclude") {
		t.Fatalf("hidden run: %s", args)
	}
}

func TestProviderOpenCode(t *testing.T) {
	a, _ := builtinByID("opencode")
	pr := provider("openai", "chat", "")
	pr.BaseURL = "http://localhost:11434/v1"
	p, err := Build(a, Options{Mode: ModeFresh, Provider: pr, Context: &LaunchContext{MCPURL: "http://127.0.0.1:1/nb/mcp/s"}})
	if err != nil {
		t.Fatal(err)
	}
	assertKeyOnlyInEnv(t, p)
	env := envMap(p.Env)
	if env["NB_PROVIDER_KEY"] != testKey || strings.Contains(env["OPENCODE_CONFIG_CONTENT"], testKey) {
		t.Fatal("the key is in the config, not the environment")
	}
	var cfg struct {
		Model    string         `json:"model"`
		MCP      map[string]any `json:"mcp"`
		Provider map[string]struct {
			NPM     string         `json:"npm"`
			Options map[string]any `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
		t.Fatal(err)
	}
	nb := cfg.Provider["nextboard"]
	if cfg.Model != "nextboard/glm-4.6" || nb.NPM != "@ai-sdk/openai-compatible" || nb.Options["baseURL"] != pr.BaseURL || nb.Options["apiKey"] != "{env:NB_PROVIDER_KEY}" {
		t.Fatalf("config %+v", cfg)
	}
	if _, ok := cfg.MCP["nextboard"]; !ok {
		t.Fatal("the hub's tools were lost in the merge")
	}
	if !strings.Contains(p.Shell, "NB_MODEL=nextboard/glm-4.6") {
		t.Fatalf("model: %s", p.Shell)
	}
	// Anthropic through OpenCode: its SDK wants /v1, and Bearer as a header
	p, _ = Build(a, Options{Mode: ModeFresh, Provider: provider("anthropic", "", "bearer")})
	if c := envMap(p.Env)["OPENCODE_CONFIG_CONTENT"]; !strings.Contains(c, `"@ai-sdk/anthropic"`) || !strings.Contains(c, `"baseURL":"https://api.example.com/anthropic/v1"`) || !strings.Contains(c, "Bearer {env:NB_PROVIDER_KEY}") {
		t.Fatalf("anthropic config: %s", c)
	}
}

// A server on this machine often needs no key, but the CLIs want one.
func TestProviderWithoutKey(t *testing.T) {
	a, _ := builtinByID("claude")
	pr := provider("anthropic", "", "bearer")
	pr.Key = ""
	p, _ := Build(a, Options{Mode: ModeFresh, Provider: pr})
	if envMap(p.Env)["ANTHROPIC_AUTH_TOKEN"] != placeholderKey {
		t.Fatal("no placeholder key")
	}
}
