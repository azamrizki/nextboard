package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/providers"
	"github.com/azamrizki/nextboard/internal/usage"
)

const providerKey = "sk-provider-SECRET-0123456789abcdef"

// fakeAPI answers like an Anthropic- and OpenAI-compatible service, and only with the key.
type fakeAPI struct {
	*httptest.Server
	mu   sync.Mutex
	hits []string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		f.mu.Lock()
		f.hits = append(f.hits, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+providerKey && r.Header.Get("x-api-key") != providerKey {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"invalid key"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			fmt.Fprint(w, `{"object":"list","data":[{"id":"glm-4.6"},{"id":"glm-4.5-air"}]}`)
		default:
			fmt.Fprint(w, `{"id":"m","content":[{"type":"text","text":"pong"}]}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func envFile(t *testing.T, path string) map[string]string {
	t.Helper()
	var b []byte
	for i := 0; i < 100; i++ {
		var err error
		if b, err = os.ReadFile(path); err == nil && len(b) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	out := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(l, "=")
		out[k] = v
	}
	return out
}

func TestProvidersAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	api := newFakeAPI(t)
	out := t.TempDir()
	bin := filepath.Join(out, "fakebin")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nenv > \""+out+"/$NB_FAKE_NAME.env\"\necho FAKE-START\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, x := range [][2]string{{"fake-claude", "claude"}, {"fake-codex", "codex"}} {
		if _, err := h.srv.agents.Create(map[string]json.RawMessage{"id": raw(`"` + x[0] + `"`), "name": raw(`"` + x[0] + `"`), "command": raw(`"` + bin + `"`), "base": raw(`"` + x[1] + `"`), "env": raw(`["NB_FAKE_NAME=` + x[0] + `"]`)}); err != nil {
			t.Fatal(err)
		}
	}

	// bad input
	for _, body := range []map[string]any{
		{"name": "x", "kind": "gemini", "baseUrl": api.URL, "model": "m", "apiKey": "k"},
		{"name": "x", "kind": "anthropic", "baseUrl": "ftp://example.com", "model": "m", "apiKey": "k"},
		{"name": "x", "kind": "anthropic", "baseUrl": `https://e.com/"x`, "model": "m", "apiKey": "k"},
		{"name": "x", "kind": "anthropic", "baseUrl": "https://api.example.com", "model": "m"}, // no key for a remote service
		{"name": "x", "kind": "openai", "api": "rest", "baseUrl": api.URL, "model": "m", "apiKey": "k"},
	} {
		if st, b := h.do("POST", "/api/providers", body, nil); st != 400 || b["code"] != "bad_provider" {
			t.Fatalf("%v: %d %v", body, st, b)
		}
	}

	// testing a connection before saving: the right key lists the models, a wrong one is refused
	st, res := h.do("POST", "/api/providers/test", map[string]any{"kind": "anthropic", "baseUrl": api.URL, "model": "glm-4.6", "apiKey": providerKey}, nil)
	if st != 200 || res["ok"] != true || res["checked"] != "models" || len(res["models"].([]any)) != 2 {
		t.Fatalf("test %d %v", st, res)
	}
	st, res = h.do("POST", "/api/providers/test", map[string]any{"kind": "anthropic", "baseUrl": api.URL, "model": "glm-4.6", "apiKey": "wrong-key"}, nil)
	if st != 200 || res["ok"] == true || !strings.Contains(res["error"].(string), "refused") || strings.Contains(fmt.Sprint(res), "wrong-key") {
		t.Fatalf("wrong key %d %v", st, res)
	}

	st, pv := h.do("POST", "/api/providers", map[string]any{"name": "GLM", "kind": "anthropic", "baseUrl": api.URL + "/v1", "model": "glm-4.6", "fastModel": "glm-4.5-air", "apiKey": providerKey,
		"prices": map[string]any{"glm-4.6": map[string]any{"input": 2, "output": 8}}, "budget": 1}, nil)
	if st != 201 || pv["hasKey"] != true || pv["baseUrl"] != api.URL || pv["auth"] != "bearer" {
		t.Fatalf("create %d %v", st, pv)
	}
	id := pv["id"].(string)
	st, oa := h.do("POST", "/api/providers", map[string]any{"name": "OpenRouter", "kind": "openai", "api": "responses", "baseUrl": api.URL + "/v1", "model": "openai/gpt-5", "apiKey": providerKey}, nil)
	if st != 201 {
		t.Fatalf("create openai %d %v", st, oa)
	}

	// the key is nowhere a client or a file in the clear could show it
	_, list := h.do("GET", "/api/providers", nil, nil)
	_, sets := h.do("GET", "/api/settings", nil, nil)
	disk, _ := os.ReadFile(h.srv.st.Path("providers.json"))
	for what, v := range map[string]string{"list": fmt.Sprint(list), "settings": fmt.Sprint(sets), "providers.json": string(disk)} {
		if strings.Contains(v, providerKey) {
			t.Fatalf("the key is in %s", what)
		}
	}
	// a saved provider is tested with its saved key, and keeps the models found
	if st, res := h.do("POST", "/api/providers/test", map[string]any{"id": id}, nil); st != 200 || res["ok"] != true {
		t.Fatalf("saved test %d %v", st, res)
	}
	if p, _ := h.srv.providers.Get(id); len(p.Models) != 2 {
		t.Fatalf("models not kept: %v", p.Models)
	}
	// a change without a key keeps the key
	if st, b := h.do("PATCH", "/api/providers/"+id, map[string]any{"model": "glm-4.6", "apiKey": ""}, nil); st != 200 || b["hasKey"] != true {
		t.Fatalf("patch %d %v", st, b)
	}

	// a console on it
	if st, b := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-codex", "providerId": id}, nil); st != 400 || b["code"] != "provider_mismatch" {
		t.Fatalf("codex on an Anthropic provider %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "providerId": "ap_nope1234"}, nil); st != 404 || b["code"] != "provider_not_found" {
		t.Fatalf("unknown %d %v", st, b)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "providerId": id}, nil)
	if st != 201 || in["providerId"] != id || in["profileId"] != "" || strings.Contains(fmt.Sprint(in), providerKey) {
		t.Fatalf("console %d %v", st, in)
	}
	sid := in["id"].(string)
	env := envFile(t, filepath.Join(out, "fake-claude.env"))
	dir := h.srv.st.Path("providers", id, "claude")
	for k, want := range map[string]string{"CLAUDE_CONFIG_DIR": dir, "ANTHROPIC_BASE_URL": api.URL, "ANTHROPIC_AUTH_TOKEN": providerKey, "ANTHROPIC_MODEL": "glm-4.6", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "glm-4.5-air"} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".claude.json")); err != nil || !strings.Contains(string(b), `"hasCompletedOnboarding": true`) {
		t.Fatalf("first run not answered: %v %s", err, b)
	}
	st, cx := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-codex", "providerId": oa["id"]}, nil)
	if st != 201 {
		t.Fatalf("codex console %d %v", st, cx)
	}
	cenv := envFile(t, filepath.Join(out, "fake-codex.env"))
	if cenv["NB_PROVIDER_KEY"] != providerKey || !strings.Contains(cenv["NB_CODEX_PROVIDER"], `base_url="`+api.URL+`/v1"`) || cenv["CODEX_HOME"] != h.srv.st.Path("providers", oa["id"].(string), "codex") {
		t.Fatalf("codex env %v", cenv)
	}

	// what it cost: its consoles' transcripts and its hidden runs, at its prices
	tdir := filepath.Join(dir, "projects", "-w")
	_ = os.MkdirAll(tdir, 0o700)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	line := func(id string, in, out int) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":"glm-4.6","usage":{"input_tokens":%d,"output_tokens":%d}}}`, now, id, in, out)
	}
	if err := os.WriteFile(filepath.Join(tdir, "a.jsonl"), []byte(line("m1", 100000, 10000)+"\n"+line("m2", 50000, 5000)+"\n"+line("m2", 50000, 5000)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.srv.noteProviderRun(id, "glm-4.5-air", "planner", usageTokens(1000, 100))
	_, spend := h.do("GET", "/api/providers/"+id+"/spend", nil, nil)
	// 150k in × $2 + 15k out × $8 per million = 0.30 + 0.12; the fast model has no price
	if c := spend["cost"].(float64); c < 0.4199 || c > 0.4201 || fmt.Sprint(spend["unpriced"]) != "[glm-4.5-air]" {
		t.Fatalf("spend %v", spend)
	}
	// over budget: no new console, and no restart that would quietly use the subscription
	if _, err := h.srv.providers.Update(id, providerInputBudget(0.30)); err != nil {
		t.Fatal(err)
	}
	if st, b := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "providerId": id}, nil); st != 409 || b["code"] != "provider_budget" {
		t.Fatalf("budget %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions/"+sid+"/restart", map[string]any{}, nil); st != 409 || b["code"] != "provider_budget" {
		t.Fatalf("restart over budget %d %v", st, b)
	}

	// in use: not removed; removing it clears the hidden runs' choice
	if st, b := h.do("DELETE", "/api/providers/"+id, nil, nil); st != 409 || b["code"] != "provider_in_use" {
		t.Fatalf("delete in use %d %v", st, b)
	}
	h.do("DELETE", "/api/sessions/"+sid, nil, nil)
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"runner": map[string]any{"provider": id}}, nil); st != 200 {
		t.Fatalf("runner provider %d %v", st, b)
	}
	if st, _ := h.do("DELETE", "/api/providers/"+id, nil, nil); st != 200 {
		t.Fatal(st)
	}
	if h.srv.settings.Get().Runner.Provider != "" {
		t.Fatal("the hidden runs still name a removed provider")
	}
}

// Hidden runs go through the provider of settings.runner.provider when it can run their CLI.
func TestRunnerThroughProvider(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	api := newFakeAPI(t)
	_, pv := h.do("POST", "/api/providers", map[string]any{"name": "GLM", "kind": "anthropic", "baseUrl": api.URL, "model": "glm-4.6", "fastModel": "glm-4.5-air", "apiKey": providerKey}, nil)
	id := pv["id"].(string)
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"runner": map[string]any{"provider": id}}, nil); st != 200 {
		t.Fatalf("%d %v", st, b)
	}
	req := jobs.RunnerRequest{Purpose: "planner", Agent: "claude", Model: "claude-opus-4-1", Env: []string{"CLAUDE_CONFIG_DIR=/the/login"}}
	if err := h.srv.withRunnerProvider(&req); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, kv := range req.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env["CLAUDE_CONFIG_DIR"] != h.srv.st.Path("providers", id, "claude") || env["ANTHROPIC_AUTH_TOKEN"] != providerKey || env["ANTHROPIC_MODEL"] != "glm-4.6" || req.Model != "" {
		t.Fatalf("claude run %v model %q", env, req.Model)
	}
	if strings.Contains(strings.Join(jobs.RunnerArgv(req), " "), providerKey) {
		t.Fatal("the key is on a hidden run's command line")
	}
	// the CLI's own alias goes to the fast model
	req = jobs.RunnerRequest{Purpose: "code-complete", Agent: "claude", Model: "haiku"}
	_ = h.srv.withRunnerProvider(&req)
	if req.Model != "haiku" {
		t.Fatalf("alias %q", req.Model)
	}
	// what it used goes to the ledger
	req.OnFinish(jobs.Job{Usage: &jobs.RunUsage{Model: "glm-4.5-air", Input: 10, Output: 5}})
	if turns := h.srv.ledgerTurns(id); len(turns) != 1 || turns[0].Model != "glm-4.5-air" || turns[0].Input != 10 {
		t.Fatalf("ledger %v", turns)
	}
	// a CLI the provider cannot run stays on its login
	req = jobs.RunnerRequest{Purpose: "commit-subject", Agent: "codex", Env: []string{"CODEX_HOME=/login"}}
	_ = h.srv.withRunnerProvider(&req)
	if len(req.Env) != 1 || req.Env[0] != "CODEX_HOME=/login" || len(req.Extra) != 0 {
		t.Fatalf("codex left %v %v", req.Env, req.Extra)
	}
}

func usageTokens(in, out int64) usage.Tokens { return usage.Tokens{Input: in, Output: out} }

func providerInputBudget(b float64) providers.Input { return providers.Input{Budget: &b} }
