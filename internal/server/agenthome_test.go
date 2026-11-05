package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/machine"
)

// TestAgentHomeDefaults: built-in agents get a state folder in the NextBoard
// data folder as their default once; consoles and runners follow it, the user
// can turn it off, and it stays off.
func TestAgentHomeDefaults(t *testing.T) {
	h := newTestHub(t)
	_, wid, _ := orchSetup(t, h)
	homes := map[string]map[string]any{}
	for _, x := range h.list("/api/profiles?machineId=local") {
		p := x.(map[string]any)
		if p["home"] == true {
			homes[p["agent"].(string)] = p
		}
	}
	claude, codex := homes["claude"], homes["codex"]
	if claude == nil || codex == nil || claude["dir"] != h.srv.st.Path("agents", "claude") || claude["exists"] != true {
		t.Fatalf("homes %v", homes)
	}
	defaults := h.srv.settings.Get().Agents.Profiles[machine.LocalID]
	if defaults["claude"] != claude["id"] || defaults["codex"] != codex["id"] {
		t.Fatalf("defaults %v", defaults)
	}
	want := "CLAUDE_CONFIG_DIR=" + claude["dir"].(string)
	if env := h.srv.defaultProfileEnv("claude"); len(env) != 1 || env[0] != want {
		t.Fatalf("runner env %v", env)
	}

	// A custom agent based on Claude Code shares the folder.
	if in := h.spawnAgent(wid); in["profileId"] != claude["id"] {
		t.Fatalf("console profile %v", in["profileId"])
	}

	if st, r := h.do("DELETE", "/api/profiles/"+claude["id"].(string)+"?purge=1", nil, nil); st != 409 || r["code"] != "profile_home" {
		t.Fatalf("delete %d %v", st, r)
	}

	// The CLI's plugin offer would take the keys of an order typed into a console.
	if b, err := os.ReadFile(filepath.Join(h.srv.st.Path("agents", "claude"), ".claude.json")); err != nil || !strings.Contains(string(b), `"lspRecommendationDisabled": true`) {
		t.Fatalf("offer still on: %s %v", b, err)
	}

	// Turned off: consoles use the CLI's own login, also after a restart.
	if st, r := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"profiles": map[string]any{"local": map[string]any{"claude": nil}}}}, nil); st != 200 {
		t.Fatalf("unset %d %v", st, r)
	}
	if err := h.srv.ensureAgentHomes(); err != nil {
		t.Fatal(err)
	}
	if id := h.srv.settings.Get().Agents.Profiles[machine.LocalID]["claude"]; id != "" {
		t.Fatalf("default came back: %q", id)
	}
	if env := h.srv.defaultProfileEnv("claude"); len(env) != 0 {
		t.Fatalf("runner env after unset %v", env)
	}
	if in := h.spawnAgent(wid); in["profileId"] != "" && in["profileId"] != nil {
		t.Fatalf("console profile after unset %v", in["profileId"])
	}
}

// TestAgentsStayOnThisPC: a server only gets plain shells.
func TestAgentsStayOnThisPC(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	m, err := h.srv.machines.Add(machine.AddRequest{Name: "srv", Host: "127.0.0.1", Port: 1, User: "u", AuthMethod: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	mid := m.ID()
	st, r := h.do("POST", "/api/sessions", map[string]any{"agent": "claude", "machineId": mid}, nil)
	if st != 422 || r["code"] != "agent_this_pc_only" {
		t.Fatalf("agent on a server %d %v", st, r)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "machineId": mid}, nil)
	if st != 201 {
		t.Fatalf("shell on a server %d %v", st, in)
	}
	id := in["id"].(string)
	st, r = h.do("POST", "/api/sessions/"+id+"/restart", map[string]any{"agent": "claude"}, nil)
	if st != 422 || r["code"] != "agent_this_pc_only" {
		t.Fatalf("switching a server shell to an agent %d %v", st, r)
	}
}

// Changing the agent of a console keeps it on the NextBoard account, however the
// change is asked for. The CLI's own login is only for a default turned off.
func TestSwitchingAgentStaysOnTheNextBoardAccount(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	home := func(agentID string) string {
		t.Helper()
		id := h.srv.settings.Get().Agents.Profiles[machine.LocalID][agentID]
		if id == "" {
			t.Fatalf("no default for %s", agentID)
		}
		return id
	}
	start := func() string {
		t.Helper()
		st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "claude", "machineId": "local"}, nil)
		if st != 201 || in["profileId"] != home("claude") {
			t.Fatalf("spawn: %d %v", st, in["profileId"])
		}
		return in["id"].(string)
	}
	profileOf := func(id string) string {
		t.Helper()
		st, in := h.do("GET", "/api/sessions/"+id, nil, nil)
		if st != 200 {
			t.Fatalf("session: %d", st)
		}
		p, _ := in["profileId"].(string)
		return p
	}

	id := start()
	if st, r := h.do("POST", "/api/sessions/"+id+"/restart", map[string]any{"agent": "codex"}, nil); st != 200 {
		t.Fatalf("restart: %d %v", st, r)
	}
	if got := profileOf(id); got != home("codex") {
		t.Fatalf("no profile given: %q, want %q", got, home("codex"))
	}

	// the way the switch dialog and a handoff ask: "" for the default
	id = start()
	if err := (hubAdapter{h.srv}).SwitchAgent(id, "codex", "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := profileOf(id); got != home("codex") {
		t.Fatalf("empty profile: %q, want %q", got, home("codex"))
	}

	// the same agent again, "" also means the default
	if st, r := h.do("POST", "/api/sessions/"+id+"/restart", map[string]any{"profileId": ""}, nil); st != 200 {
		t.Fatalf("restart: %d %v", st, r)
	}
	if got := profileOf(id); got != home("codex") {
		t.Fatalf("same agent: %q", got)
	}

	// back to a conversation: it keeps the login it was made under
	id = start()
	if err := (hubAdapter{h.srv}).SwitchAgent(id, "codex", "", "11111111-1111-4111-8111-111111111111", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := profileOf(id); got != "" {
		t.Fatalf("resume changed the login: %q", got)
	}

	// turned off: then and only then the CLI's own login
	if st, r := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"profiles": map[string]any{"local": map[string]any{"codex": nil}}}}, nil); st != 200 {
		t.Fatalf("unset %d %v", st, r)
	}
	id = start()
	if st, r := h.do("POST", "/api/sessions/"+id+"/restart", map[string]any{"agent": "codex"}, nil); st != 200 {
		t.Fatalf("restart: %d %v", st, r)
	}
	if got := profileOf(id); got != "" {
		t.Fatalf("default off: %q", got)
	}
}
