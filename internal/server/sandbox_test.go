package server

import (
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/session"
)

// A project's sandbox is set over the API and reaches the launch of the CLIs that have one.
func TestProjectSandbox(t *testing.T) {
	h := newTestHub(t)
	pid, _, dir := orchSetup(t, h)
	if st, b := h.do("PATCH", "/api/projects/"+pid, map[string]any{"sandbox": "everything"}, nil); st != 400 || b["code"] != "bad_sandbox" {
		t.Fatalf("bad level %d %v", st, b)
	}
	launch := func(agentID string) (*session.Spec, *agentLaunch) {
		t.Helper()
		sp := session.Spec{ID: "s_sandbox01", Agent: agentID, MachineID: machine.LocalID, ProjectID: pid, Cwd: dir}
		c, err := h.srv.launchContext(sp)
		if err != nil {
			t.Fatal(err)
		}
		if c == nil {
			return &sp, nil
		}
		return &sp, &agentLaunch{sandbox: c.Sandbox != nil, network: c.Sandbox != nil && c.Sandbox.Network, prompt: c.Prompt}
	}
	if _, l := launch("fake-claude"); l == nil || l.sandbox {
		t.Fatalf("no sandbox yet: %+v", l)
	}
	for level, network := range map[string]bool{"workspace": true, "offline": false} {
		if st, b := h.do("PATCH", "/api/projects/"+pid, map[string]any{"sandbox": level}, nil); st != 200 || b["sandbox"] != level {
			t.Fatalf("%s: %d %v", level, st, b)
		}
		_, l := launch("fake-claude")
		if l == nil || !l.sandbox || l.network != network || !strings.Contains(l.prompt, "run in a sandbox") {
			t.Fatalf("%s: %+v", level, l)
		}
		if network == strings.Contains(l.prompt, "no network") {
			t.Fatalf("%s: the rule says the wrong thing about the network: %s", level, l.prompt)
		}
		if _, l := launch("opencode"); l != nil && l.sandbox {
			t.Fatal("OpenCode has no sandbox")
		}
	}
	if st, b := h.do("PATCH", "/api/projects/"+pid, map[string]any{"sandbox": ""}, nil); st != 200 || b["sandbox"] != nil {
		t.Fatalf("off %d %v", st, b)
	}
	st, b := h.do("GET", "/api/agents/sandbox", nil, nil)
	if st != 200 || b["codex"] == nil || b["claude"] == nil {
		t.Fatalf("status %d %v", st, b)
	}
}

type agentLaunch struct {
	sandbox, network bool
	prompt           string
}
