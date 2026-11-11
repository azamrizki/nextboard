package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A console keeps its model and effort over restarts, whatever else changes the
// CLI's default; what the user chooses inside the CLI counts, and so does what the
// hub is told afterwards (docs/API-agents.md §25).
func TestModelSurvivesRestart(t *testing.T) {
	h := newTestHub(t)
	_, wid, dir := orchSetup(t, h)
	bin := filepath.Join(dir, "fakemodel")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"ARGS: $*\"\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "fake-model", "name": "Fake Model", "command": bin, "base": "claude"}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "fake-plain", "name": "Fake Plain", "command": bin}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	create := func(body map[string]any) map[string]any {
		t.Helper()
		body["agent"], body["cwd"] = "fake-model", dir
		st, in := h.do("POST", "/api/sessions", body, nil)
		if st != 201 {
			t.Fatalf("create %d %v", st, in)
		}
		return in
	}
	restart := func(id string, body map[string]any) map[string]any {
		t.Helper()
		st, in := h.do("POST", "/api/sessions/"+id+"/restart", body, nil)
		if st != 200 {
			t.Fatalf("restart %d %v", st, in)
		}
		return in
	}
	stamp := func(d time.Duration) string { return time.Now().Add(d).UTC().Format("2006-01-02T15:04:05.000Z") }
	model := func(arg, printed string) string {
		at := stamp(time.Second)
		return fmt.Sprintf(`{"type":"user","timestamp":"%s","message":{"content":"<command-name>/model</command-name> <command-args>%s</command-args>"}}`+"\n"+
			`{"type":"user","timestamp":"%s","message":{"content":"<local-command-stdout>Set model to %s</local-command-stdout>"}}`+"\n", at, arg, at, printed)
	}

	// What was asked for reaches the CLI.
	in := create(map[string]any{"model": "sonnet", "effort": "high"})
	id := in["id"].(string)
	if in["model"] != "sonnet" || in["effort"] != "high" {
		t.Fatalf("created %v %v", in["model"], in["effort"])
	}
	waitScrollback(t, h.srv, id, "--model sonnet --effort high")

	// Another console changing the CLI's default does not touch this one.
	in = restart(id, map[string]any{"mode": "fresh"})
	if in["model"] != "sonnet" {
		t.Fatalf("restart lost the model: %v", in["model"])
	}

	// The CLI's own /model counts, even with no turn after it.
	tp := filepath.Join(dir, "conv.jsonl")
	if err := os.WriteFile(tp, []byte(model("claude-haiku-5-5", "`Haiku 5.5`")), 0o600); err != nil {
		t.Fatal(err)
	}
	h.srv.sessions.TestSetTranscript(id, tp)
	in = restart(id, map[string]any{"mode": "fresh"})
	if in["model"] != "claude-haiku-5-5" {
		t.Fatalf("/model not taken over: %v", in["model"])
	}
	waitScrollback(t, h.srv, id, "--model claude-haiku-5-5")

	// A choice made here later wins over the older /model in the file.
	time.Sleep(1100 * time.Millisecond) // past the stamp of that /model
	h.srv.sessions.TestSetTranscript(id, tp)
	in = restart(id, map[string]any{"mode": "fresh", "model": "opus"})
	if in["model"] != "opus" {
		t.Fatalf("explicit model: %v", in["model"])
	}
	waitScrollback(t, h.srv, id, "--model opus")
	h.srv.sessions.TestSetTranscript(id, tp)
	if in = restart(id, map[string]any{"mode": "fresh"}); in["model"] != "opus" {
		t.Fatalf("an old /model came back: %v", in["model"])
	}
	// "" hands the choice back to the CLI.
	if in = restart(id, map[string]any{"mode": "fresh", "model": "", "effort": ""}); in["model"] != nil || in["effort"] != nil {
		t.Fatalf("cleared %v %v", in["model"], in["effort"])
	}

	// A console that chose nothing stays on what it has been answering with.
	in = create(map[string]any{})
	other := in["id"].(string)
	if in["model"] != nil {
		t.Fatalf("no default set, yet %v", in["model"])
	}
	tp2 := filepath.Join(dir, "conv2.jsonl")
	reply := fmt.Sprintf(`{"type":"assistant","timestamp":"%s","message":{"model":"claude-sonnet-5-5","content":[]}}`+"\n", stamp(0))
	if err := os.WriteFile(tp2, []byte(reply), 0o600); err != nil {
		t.Fatal(err)
	}
	h.srv.sessions.TestSetTranscript(other, tp2)
	if in = restart(other, map[string]any{"mode": "fresh"}); in["model"] != "claude-sonnet-5-5" {
		t.Fatalf("not pinned to its replies: %v", in["model"])
	}
	// A console on an API provider is not: its answers may name the model otherwise than the
	// provider takes it (a router's cx/gpt-x answers as gpt-x).
	api := newFakeAPI(t)
	_, pv := h.do("POST", "/api/providers", map[string]any{"name": "Router", "kind": "anthropic", "baseUrl": api.URL, "model": "cx/gpt-x", "apiKey": providerKey}, nil)
	in = create(map[string]any{"providerId": pv["id"]})
	onAPI := in["id"].(string)
	h.srv.sessions.TestSetTranscript(onAPI, tp2)
	if in = restart(onAPI, map[string]any{"mode": "fresh"}); in["model"] != nil {
		t.Fatalf("a provider console took its reply's model: %v", in["model"])
	}

	// Bad values never reach a command line.
	if st, b := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-model", "cwd": dir, "model": "x; rm -rf /"}, nil); st != 400 {
		t.Fatalf("bad model %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions/"+other+"/restart", map[string]any{"effort": "$(id)"}, nil); st != 400 {
		t.Fatalf("bad effort %d %v", st, b)
	}

	// Settings give the default; the request overrides it, also with "the CLI's own".
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{
		"models":     map[string]any{"fake-model": map[string]any{"model": "default-model", "effort": "low"}},
		"roleAgents": map[string]any{"testers": "fake-model"},
		"roleModels": map[string]any{"testers": map[string]any{"model": "tester-model", "effort": "max"}},
	}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, b)
	}
	if in = create(map[string]any{}); in["model"] != "default-model" || in["effort"] != "low" {
		t.Fatalf("agent default %v %v", in["model"], in["effort"])
	}
	if in = create(map[string]any{"model": "", "effort": "high"}); in["model"] != nil || in["effort"] != "high" {
		t.Fatalf("override %v %v", in["model"], in["effort"])
	}

	// A group takes its role's default, and a slot overrides that.
	st, r := h.do("POST", "/api/workspaces/"+wid+"/spawn", map[string]any{
		"group": map[string]any{"name": "tests", "role": "testers"},
		"slots": []any{
			map[string]any{"agent": "fake-model"},
			map[string]any{"agent": "fake-model", "model": "picked", "effort": "medium"},
		},
	}, nil)
	if st != 201 {
		t.Fatalf("spawn %d %v", st, r)
	}
	ss := r["sessions"].([]any)
	if a := ss[0].(map[string]any); a["model"] != "tester-model" || a["effort"] != "max" {
		t.Fatalf("role default %v %v", a["model"], a["effort"])
	}
	if b := ss[1].(map[string]any); b["model"] != "picked" || b["effort"] != "medium" {
		t.Fatalf("slot override %v %v", b["model"], b["effort"])
	}

	// Another agent starts from its own default, and a CLI with no flag gets none.
	in = restart(id, map[string]any{"agent": "fake-plain"})
	if in["model"] != nil || in["effort"] != nil {
		t.Fatalf("switched %v %v", in["model"], in["effort"])
	}
	if b := waitScrollback(t, h.srv, id, "ARGS:"); strings.Count(string(b), "ARGS:") == 0 {
		t.Fatal("no start")
	}

	// The list comes from the CLI's side.
	if st, l := h.do("GET", "/api/machines/local/agents/fake-model/models", nil, nil); st != 200 || l["source"] != "known" || len(l["models"].([]any)) < 3 {
		t.Fatalf("models %d %v", st, l)
	}
	if st, l := h.do("GET", "/api/machines/local/agents/fake-plain/models", nil, nil); st != 200 || l["source"] != "none" {
		t.Fatalf("no models %d %v", st, l)
	}
	if st, _ := h.do("GET", "/api/machines/local/agents/nope/models", nil, nil); st != 404 {
		t.Fatalf("unknown agent %d", st)
	}
}
