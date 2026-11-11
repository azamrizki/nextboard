package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// A project's sandbox reaches Claude Code as settings and Codex as flags; yolo stops the
// questions there, never the sandbox; other CLIs have none.
func TestSandboxLaunch(t *testing.T) {
	ctx := func(sb *Sandbox) *LaunchContext {
		return &LaunchContext{MCPURL: "http://127.0.0.1:1/nb/mcp/s", Sandbox: sb}
	}
	claude, _ := builtinByID("claude")
	p, err := Build(claude, Options{Mode: ModeFresh, Context: ctx(&Sandbox{Write: []string{"/home/u/.cache"}})})
	if err != nil {
		t.Fatal(err)
	}
	var set struct {
		Sandbox struct {
			Enabled, FailIfUnavailable, AllowUnsandboxedCommands bool
			Filesystem                                           struct{ AllowWrite []string }
			Network                                              *struct{ AllowedDomains []string }
		}
	}
	if err := json.Unmarshal([]byte(envMap(p.Env)["NB_HOOKS"]), &set); err != nil {
		t.Fatalf("settings: %v", err)
	}
	sb := set.Sandbox
	if !sb.Enabled || !sb.FailIfUnavailable || sb.AllowUnsandboxedCommands || strings.Join(sb.Filesystem.AllowWrite, ",") != "/home/u/.cache" || sb.Network != nil {
		t.Fatalf("claude offline sandbox %+v", sb)
	}
	if !strings.Contains(envMap(p.Env)["NB_WRAP"], "$NB_HOOKS") {
		t.Fatal("no --settings")
	}
	p, _ = Build(claude, Options{Mode: ModeFresh, Context: ctx(&Sandbox{Network: true})})
	_ = json.Unmarshal([]byte(envMap(p.Env)["NB_HOOKS"]), &set)
	if set.Sandbox.Network == nil || strings.Join(set.Sandbox.Network.AllowedDomains, ",") != "*" {
		t.Fatalf("claude with network %+v", set.Sandbox)
	}

	codex, _ := builtinByID("codex")
	p, _ = Build(codex, Options{Mode: ModeFresh, Yolo: true, Context: ctx(&Sandbox{Write: []string{"/home/u/.cache"}})})
	wrap := envMap(p.Env)["NB_WRAP"]
	for _, want := range []string{"workspace-write", "sandbox_workspace_write.network_access=false", `sandbox_workspace_write.writable_roots=["/home/u/.cache"]`, "--ask-for-approval", "never"} {
		if !strings.Contains(wrap, want) {
			t.Errorf("codex: no %s in %s", want, wrap)
		}
	}
	if strings.Contains(wrap, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatal("yolo lifts the sandbox")
	}
	p, _ = Build(codex, Options{Mode: ModeFresh, Context: ctx(&Sandbox{Network: true})})
	if !strings.Contains(envMap(p.Env)["NB_WRAP"], "network_access=true") {
		t.Fatal("codex network")
	}
	// without a sandbox nothing changes
	p, _ = Build(codex, Options{Mode: ModeFresh, Yolo: true, Context: ctx(nil)})
	if w := envMap(p.Env)["NB_WRAP"]; strings.Contains(w, "workspace-write") || !strings.Contains(w, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("codex without sandbox: %s", w)
	}
	opencode, _ := builtinByID("opencode")
	if Sandboxes(opencode) || !Sandboxes(claude) || !Sandboxes(codex) {
		t.Fatal("which CLIs sandbox")
	}
}
