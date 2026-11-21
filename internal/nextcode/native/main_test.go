package native

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
	"github.com/azamrizki/nextboard/internal/nextcode/native/tools"
)

var goodArgs = []string{"--provider-kind", "anthropic", "--base-url", "https://api.example.com", "--model", "claude-x"}

func TestBadFlags(t *testing.T) {
	t.Setenv(stateEnv, t.TempDir())
	t.Setenv(guardEnv, "")
	prompt := filepath.Join(t.TempDir(), "missing.md")
	cases := map[string][]string{
		"no provider":     {"--base-url", "https://x", "--model", "m"},
		"bad provider":    {"--provider-kind", "gemini", "--base-url", "https://x", "--model", "m"},
		"no base url":     {"--provider-kind", "openai", "--model", "m"},
		"bad base url":    {"--provider-kind", "openai", "--base-url", "ftp://x", "--model", "m"},
		"no model":        {"--provider-kind", "openai", "--base-url", "https://x"},
		"bad api":         append(goodArgs, "--api", "graphql"),
		"bad auth":        append(goodArgs, "--auth", "basic"),
		"bad effort":      append(goodArgs, "--effort", "extreme"),
		"bad mode":        append(goodArgs, "--permission-mode", "yolo"),
		"unknown flag":    append(goodArgs, "--frobnicate"),
		"positional":      append(goodArgs, "extra"),
		"two ids":         append(goodArgs, "--session-id", "a", "--resume", "b"),
		"continue and id": append(goodArgs, "--continue", "--session-id", "a"),
		"bad id":          append(goodArgs, "--resume", "../../etc/passwd"),
		"negative":        append(goodArgs, "--max-tokens", "-1"),
		"not a number":    append(goodArgs, "--context-window", "big"),
		"prompt file":     append(goodArgs, "--append-system-prompt-file", prompt),
	}
	for name, args := range cases {
		var stderr lockedBuf
		code := start(context.Background(), args, strings.NewReader(""), io.Discard, &stderr, noFactory(t))
		if code != 2 || !strings.HasPrefix(stderr.String(), "nextcode-agent: ") {
			t.Errorf("%s: exit %d, stderr %q", name, code, stderr.String())
		}
	}
	// a missing state folder and a guard that is not JSON
	for name, env := range map[string][2]string{"no state": {stateEnv, ""}, "bad guard": {guardEnv, "{blocked"}} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(env[0], env[1])
			var stderr lockedBuf
			if code := start(context.Background(), goodArgs, strings.NewReader(""), io.Discard, &stderr, noFactory(t)); code != 2 || stderr.String() == "" {
				t.Errorf("exit %d, stderr %q", code, stderr.String())
			}
		})
	}
}

// noFactory fails the test when the agent gets as far as making its client.
func noFactory(t *testing.T) factory {
	return factory{client: func(llm.ProviderConfig, *http.Client) llm.Client {
		t.Error("the client was made")
		return newFake()
	}, tools: func() []tools.Tool { return nil }}
}

func TestParse(t *testing.T) {
	cwd := t.TempDir()
	prompt := filepath.Join(cwd, "ctx.md")
	if err := os.WriteFile(prompt, []byte("HUB CONTEXT"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{stateEnv: "/state", homeEnv: "/home", guardEnv: `{"blocked":["git push",""],"sandbox":{"network":true,"write":["/tmp"]}}`}
	args := []string{"--provider-kind", "openai", "--base-url", "http://127.0.0.1:8080/v1", "--api", "responses", "--model", "gpt-x", "--effort", "high",
		"--resume", "abc-123", "--permission-mode", "auto", "--append-system-prompt-file", prompt, "--add-dir", "rel", "--add-dir", "/abs/dir",
		"--max-tokens", "4096", "--context-window", "64000"}
	c, err := parse(args, func(k string) string { return env[k] }, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if c.provider.Wire() != llm.WireResponses || c.model != "gpt-x" || c.effort != "high" || c.resume != "abc-123" || c.mode != modeAuto ||
		c.appendPrompt != "HUB CONTEXT" || c.maxTokens != 4096 || c.contextWindow != 64000 || c.stateDir != "/state" || c.homeDir != "/home" {
		t.Fatalf("config: %+v", c)
	}
	if len(c.addDirs) != 2 || c.addDirs[0] != filepath.Join(cwd, "rel") || c.addDirs[1] != "/abs/dir" {
		t.Fatalf("add dirs: %v", c.addDirs)
	}
	if len(c.blocked) != 1 || c.blocked[0] != "git push" || c.sandbox == nil || !c.sandbox.Network || c.sandbox.Write[0] != "/tmp" {
		t.Fatalf("guard: %v %+v", c.blocked, c.sandbox)
	}
	c, err = parse(goodArgs, func(k string) string { return env[k] }, cwd)
	if err != nil || c.mode != modeDefault || c.provider.Wire() != llm.WireAnthropic {
		t.Fatalf("defaults: %+v %v", c, err)
	}
}

func TestKeyNeverLeaks(t *testing.T) {
	state, home := t.TempDir(), t.TempDir()
	t.Setenv(keyEnv, testKey)
	t.Setenv(stateEnv, state)
	t.Setenv(homeEnv, home)
	t.Setenv(guardEnv, `{"blocked":["git push"]}`)
	t.Setenv("NB_TOKEN", "hub-token")
	t.Setenv("NB_HOOK_URL", "http://hook")
	t.Setenv("NB_MCP_URL", "http://mcp")
	t.Setenv("NEXTCODE_OTHER", "x")
	t.Setenv("COPY_OF_KEY", "Bearer "+testKey)
	t.Setenv("KEEP_ME", "yes")

	k := newKit()
	var gotKey string
	keyInEnv := "unchecked"
	k.bash.run = func(_ context.Context, env *tools.Env, input json.RawMessage) tools.Result {
		keyInEnv = os.Getenv(keyEnv)
		return tools.Result{Output: strings.Join(env.Environ, "\n") + "\nfile says " + testKey}
	}
	fake := newFake(callTool("t1", "Bash", `{"command":"env"}`), failWith(&llm.APIError{Status: 401, Message: "bad key " + testKey}))
	f := factory{
		client: func(p llm.ProviderConfig, _ *http.Client) llm.Client { gotKey = p.Key; return fake },
		tools:  k.all,
	}
	args := append(append([]string{}, goodArgs...), "--permission-mode", "bypassPermissions", "--session-id", "k1")
	h := startWith(t, func(stdin io.Reader, stdout, stderr io.Writer) int {
		return start(context.Background(), args, stdin, stdout, stderr, f)
	})
	h.initialize()
	h.say("show me the environment")
	res, _ := h.result()
	if code := h.close(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if gotKey != testKey {
		t.Fatalf("the client got key %q", gotKey)
	}
	if keyInEnv != "" || os.Getenv(keyEnv) != "" {
		t.Fatal("the key stayed in the environment")
	}
	env := k.bash.envs[0].Environ
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.Contains(kv, testKey) || name == keyEnv || name == "NB_TOKEN" || name == guardEnv || strings.HasPrefix(name, "NB_HOOK") ||
			strings.HasPrefix(name, "NB_MCP") || strings.HasPrefix(name, "NEXTCODE_") || name == "COPY_OF_KEY" {
			t.Errorf("in a tool's environment: %s", name)
		}
	}
	if !strings.Contains(strings.Join(env, "\n"), "KEEP_ME=yes") {
		t.Error("the tool's environment lost the owner's variables")
	}
	for name, out := range map[string]string{"stdout": h.stdout.String(), "stderr": h.stderr.String()} {
		if strings.Contains(out, testKey) {
			t.Errorf("the key is in %s", name)
		}
	}
	if res["is_error"] != true || !strings.Contains(res["result"].(string), "[redacted]") {
		t.Fatalf("result: %v", res)
	}
	b, _ := os.ReadFile(filepath.Join(state, "k1.json"))
	if len(b) == 0 || strings.Contains(string(b), testKey) {
		t.Fatal("the key is in the conversation file")
	}
	if strings.Contains(fake.request(t, 1).Messages[2].Blocks[0].Text, testKey) {
		t.Fatal("the key went to the model")
	}
}

func TestToolEnviron(t *testing.T) {
	got := toolEnviron([]string{"PATH=/bin", "NB_PROVIDER_KEY=k", "NB_TOKEN=t", "NB_HOOK_X=1", "NB_MCP=1", "NB_NEXTCODE_GUARD={}",
		"NEXTCODE_STATE=/s", "NB_OTHER=keep", "AWS=" + testKey, "NOEQUALS"}, testKey)
	want := []string{"PATH=/bin", "NB_OTHER=keep", "NOEQUALS"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v", got)
	}
}
