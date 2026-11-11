package agent

// A console on an API provider (docs/API-agents.md §10.2): the CLI talks to an Anthropic- or
// OpenAI-compatible endpoint with the provider's key instead of a subscription login. Everything
// here goes into the process environment only, after the check of the command line, like the
// secret values of MCP servers: the key never reaches a command line, a client or a log.

import (
	"encoding/json"
	"strings"
)

// ProviderLaunch is what one launch needs of a provider.
type ProviderLaunch struct {
	ID        string
	Name      string
	Kind      string // anthropic or openai
	BaseURL   string
	API       string // openai: chat or responses
	Auth      string // anthropic: bearer or x-api-key
	Model     string // the provider's default model
	FastModel string
	Key       string
	// ConfigDir is the CLI's own folder for this provider (CLAUDE_CONFIG_DIR, CODEX_HOME), so
	// that the subscription login is neither used nor overwritten.
	ConfigDir string
	// Scrub: bubblewrap is on This PC, which Claude Code needs to keep the key out of the
	// commands its tools run (CLAUDE_CODE_SUBPROCESS_ENV_SCRUB).
	Scrub bool
}

// codexProviderID names the provider inside Codex's config.
const codexProviderID = "nextboard"

// placeholderKey is sent to a server on this machine that needs no key: the CLIs want one.
const placeholderKey = "nextboard-local"

// ProviderModel is the model a provider console runs: the one chosen for it, else the provider's.
// OpenCode names models as provider/model.
func ProviderModel(a Agent, p *ProviderLaunch, chosen string) string {
	m := chosen
	if m == "" {
		m = p.Model
	}
	if a.CapBase() == "opencode" && !strings.HasPrefix(m, codexProviderID+"/") {
		m = codexProviderID + "/" + m
	}
	return m
}

// providerArgs are the CLI's flags for a provider, on top of its own.
func providerArgs(a Agent, p *ProviderLaunch) []string {
	if a.CapBase() == "codex" {
		return append([]string{"-c", `model_provider="` + codexProviderID + `"`, "-c", "$NB_CODEX_PROVIDER"}, codexKeepKey...)
	}
	return nil
}

// codexKeepKey keeps the key where Codex needs it, its own process, measured with Codex 0.165
// against a real router: the TUI writes the login shell's environment to shell_snapshots/*.sh,
// and the commands it runs get the whole environment, NB_PROVIDER_KEY too.
var codexKeepKey = []string{"-c", "features.shell_snapshot=false", "-c", "features.shell_snapshot_v2=false",
	"-c", `shell_environment_policy.exclude=["NB_PROVIDER_KEY","NB_CODEX_PROVIDER"]`}

// providerEnv is the process environment of a provider console. model is ProviderModel's.
func providerEnv(a Agent, p *ProviderLaunch, model string) []string {
	key := p.Key
	if key == "" {
		key = placeholderKey
	}
	switch a.CapBase() {
	case "claude":
		fast := p.FastModel
		if fast == "" {
			fast = model
		}
		env := []string{
			"ANTHROPIC_BASE_URL=" + p.BaseURL,
			"ANTHROPIC_MODEL=" + model,
			// the CLI's own names for its models all mean this provider's
			"ANTHROPIC_DEFAULT_OPUS_MODEL=" + model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL=" + model,
			"ANTHROPIC_DEFAULT_HAIKU_MODEL=" + fast,
			"ANTHROPIC_SMALL_FAST_MODEL=" + fast,
			// no traffic to Anthropic's own services from a console that is not theirs
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
			"DISABLE_TELEMETRY=1",
			"DISABLE_ERROR_REPORTING=1",
		}
		if p.Scrub {
			// the commands its tools run do not get the key (they would see it in their environment)
			env = append(env, "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1")
		}
		if p.Auth == "x-api-key" {
			return append(env, "ANTHROPIC_API_KEY="+key, "ANTHROPIC_AUTH_TOKEN=")
		}
		return append(env, "ANTHROPIC_AUTH_TOKEN="+key, "ANTHROPIC_API_KEY=")
	case "codex":
		table := "model_providers." + codexProviderID + "={name=" + tomlString(p.Name) + ",base_url=" + tomlString(p.BaseURL) +
			`,env_key="NB_PROVIDER_KEY",wire_api="responses"}`
		return []string{"NB_PROVIDER_KEY=" + key, "NB_CODEX_PROVIDER=" + table}
	case "opencode":
		return []string{"NB_PROVIDER_KEY=" + key}
	}
	return nil
}

// openCodeProvider adds the provider to OpenCode's config: an OpenAI-compatible endpoint through
// Chat Completions, an Anthropic one through its Messages API. The key stays in the environment.
func openCodeProvider(cfg map[string]any, p *ProviderLaunch, model string) {
	pkg, base := "@ai-sdk/openai-compatible", p.BaseURL
	opts := map[string]any{"apiKey": "{env:NB_PROVIDER_KEY}"}
	if p.Kind == "anthropic" {
		pkg, base = "@ai-sdk/anthropic", p.BaseURL+"/v1"
		if p.Auth != "x-api-key" {
			opts["headers"] = map[string]string{"Authorization": "Bearer {env:NB_PROVIDER_KEY}"}
		}
	}
	opts["baseURL"] = base
	name := strings.TrimPrefix(model, codexProviderID+"/")
	models := map[string]any{name: map[string]any{"name": name, "tool_call": true}}
	if p.FastModel != "" && p.FastModel != name {
		models[p.FastModel] = map[string]any{"name": p.FastModel, "tool_call": true}
	}
	cfg["provider"] = map[string]any{codexProviderID: map[string]any{"npm": pkg, "name": p.Name, "options": opts, "models": models}}
	cfg["model"] = model
	if p.FastModel != "" {
		cfg["small_model"] = codexProviderID + "/" + p.FastModel
	}
}

// withOpenCodeProvider puts the provider into the OPENCODE_CONFIG_CONTENT of env, or makes one.
func withOpenCodeProvider(env []string, p *ProviderLaunch, model string) []string {
	const k = "OPENCODE_CONFIG_CONTENT="
	cfg := map[string]any{}
	at := -1
	for i, kv := range env {
		if v, ok := strings.CutPrefix(kv, k); ok {
			_ = json.Unmarshal([]byte(v), &cfg)
			at = i
		}
	}
	openCodeProvider(cfg, p, model)
	b, _ := json.Marshal(cfg)
	if at >= 0 {
		env[at] = k + string(b)
		return env
	}
	return append(env, k+string(b))
}

// ProviderEnv is the process environment a provider run needs, without the CLI's config folder.
func ProviderEnv(a Agent, p *ProviderLaunch, model string) []string { return providerEnv(a, p, model) }

// ProviderRunnerArgs are a hidden run's flags for a provider: Codex's provider table, which holds
// no secret (the key stays in NB_PROVIDER_KEY), on the command line itself.
func ProviderRunnerArgs(a Agent, p *ProviderLaunch) []string {
	if a.CapBase() != "codex" {
		return nil
	}
	return append([]string{"-c", `model_provider="` + codexProviderID + `"`, "-c", strings.TrimPrefix(providerEnv(a, p, "")[1], "NB_CODEX_PROVIDER=")}, codexKeepKey...)
}
