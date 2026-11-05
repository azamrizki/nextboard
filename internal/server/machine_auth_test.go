package server

import (
	"crypto/ed25519"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"nextboard/internal/jobs"
	"nextboard/internal/session"
)

// Against a real sshd: a key locked with a passphrase, through the same calls the dialog makes.
func TestMachineAskForPassphrase(t *testing.T) {
	port, key := startSSHServer(t)
	h := newTestHub(t)
	h.setup()

	raw, err := ssh.ParseRawPrivateKey([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	priv, ok := raw.(*ed25519.PrivateKey)
	if !ok {
		t.Fatalf("kunci tes: %T", raw)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(*priv, "", []byte("open sesame"))
	if err != nil {
		t.Fatal(err)
	}
	st, m := h.do("POST", "/api/machines", map[string]any{"name": "locked", "host": "127.0.0.1", "port": port,
		"authMethod": "key", "privateKey": string(pem.EncodeToMemory(block)), "persistMode": "direct"}, nil)
	if st != 201 {
		t.Fatalf("tambah: %d %v", st, m)
	}
	id := m["id"].(string)
	pending, _ := m["pendingHostKey"].(map[string]any)
	if pending == nil {
		t.Fatalf("host key dulu: %v", m)
	}
	st, m = h.do("POST", "/api/machines/"+id+"/hostkey", map[string]any{"accept": true, "fingerprint": pending["fingerprint"]}, nil)
	if st != 200 {
		t.Fatalf("host key: %d %v", st, m)
	}
	need, _ := m["needsAuth"].(map[string]any)
	if m["status"] != "auth" || need["kind"] != "passphrase" || need["wrong"] != nil {
		t.Fatalf("mau passphrase: %v", m)
	}

	if st, _ := h.do("POST", "/api/machines/"+id+"/credentials", map[string]any{"kind": "passphrase", "value": ""}, nil); st != 400 {
		t.Fatalf("kosong: %d", st)
	}
	st, m = h.do("POST", "/api/machines/"+id+"/credentials", map[string]any{"kind": "passphrase", "value": "typo-secret"}, nil)
	need, _ = m["needsAuth"].(map[string]any)
	if st != 200 || m["status"] != "auth" || need["wrong"] != true {
		t.Fatalf("salah: %d %v", st, m)
	}
	if b, _ := json.Marshal(m); strings.Contains(string(b), "typo-secret") {
		t.Fatalf("jawaban kembali di respons: %s", b)
	}
	st, m = h.do("POST", "/api/machines/"+id+"/credentials", map[string]any{"kind": "passphrase", "value": "open sesame", "remember": true}, nil)
	if st != 200 || m["status"] != "connected" || m["needsAuth"] != nil {
		t.Fatalf("benar: %d %v", st, m)
	}
	if st, r := h.do("POST", "/api/machines/"+id+"/credentials", map[string]any{"kind": "password", "value": "x"}, nil); st != 409 || r["code"] != "auth_not_needed" {
		t.Fatalf("tidak ditanya: %d %v", st, r)
	}
	if st, _ := h.do("POST", "/api/machines/m_nope/credentials", map[string]any{"kind": "password", "value": "x"}, nil); st != 404 {
		t.Fatalf("machine tak ada: %d", st)
	}
}

func TestAgentDisabled(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "fakecat", "name": "Fake Cat", "command": "/bin/cat"}, nil); st != 201 {
		t.Fatalf("agen: %d %v", st, a)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "fakedog", "name": "Fake Dog", "command": "/bin/cat"}, nil); st != 201 {
		t.Fatalf("agen: %d %v", st, a)
	}
	patch := func(body map[string]any) {
		t.Helper()
		if st, r := h.do("PATCH", "/api/settings", map[string]any{"agents": body}, nil); st != 200 {
			t.Fatalf("settings %v: %d %v", body, st, r)
		}
	}
	type machineAgents struct {
		DefaultAgent string `json:"defaultAgent"`
		Agents       []struct {
			ID        string `json:"id"`
			Available *bool  `json:"available"`
			Disabled  bool   `json:"disabled"`
		} `json:"agents"`
	}
	list := func(path string) machineAgents {
		t.Helper()
		var out machineAgents
		if st := h.getJSON(path, &out); st != 200 {
			t.Fatalf("%s: %d", path, st)
		}
		return out
	}
	has := func(l machineAgents, id string) bool {
		for _, a := range l.Agents {
			if a.ID == id {
				return true
			}
		}
		return false
	}
	create := func(agent string) (int, map[string]any) {
		return h.do("POST", "/api/sessions", map[string]any{"machineId": "local", "preset": agent, "cols": 80, "rows": 24, "cwd": dir}, nil)
	}

	patch(map[string]any{"defaults": map[string]any{"local": "fakecat"}})
	if d := list("/api/machines/local/agents").DefaultAgent; d != "fakecat" {
		t.Fatalf("default: %s", d)
	}
	st, running := create("fakecat")
	if st != 201 {
		t.Fatalf("console: %d %v", st, running)
	}
	sid := running["id"].(string)
	t.Cleanup(func() { h.do("DELETE", "/api/sessions/"+sid, nil, nil) })

	// turned off: gone from what clients offer, and never the default
	patch(map[string]any{"disabled": map[string]any{"fakecat": true}})
	l := list("/api/machines/local/agents")
	if has(l, "fakecat") || !has(l, "fakedog") || !has(l, "shell") || l.DefaultAgent == "fakecat" {
		t.Fatalf("daftar: default %s, %+v", l.DefaultAgent, l.Agents)
	}
	var presets []struct {
		ID string `json:"id"`
	}
	h.getJSON("/api/machines/local/presets", &presets)
	for _, p := range presets {
		if p.ID == "fakecat" {
			t.Fatal("masih ada di presets")
		}
	}

	// the settings page still sees it, with what is known about it
	all := list("/api/machines/local/agents?all=1")
	found := false
	for _, a := range all.Agents {
		if a.ID == "fakecat" {
			found = true
			if !a.Disabled || a.Available == nil || !*a.Available {
				t.Fatalf("all=1: %+v", a)
			}
		} else if a.Disabled {
			t.Fatalf("%s ikut dimatikan", a.ID)
		}
	}
	if !found {
		t.Fatal("all=1 tidak menampilkannya")
	}

	// nothing new starts with it, whoever asks
	if st, r := create("fakecat"); st != 409 || r["code"] != "agent_disabled" {
		t.Fatalf("console baru: %d %v", st, r)
	}
	st, other := create("fakedog")
	if st != 201 {
		t.Fatalf("console lain: %d %v", st, other)
	}
	oid := other["id"].(string)
	t.Cleanup(func() { h.do("DELETE", "/api/sessions/"+oid, nil, nil) })
	if st, r := h.do("POST", "/api/sessions/"+oid+"/restart", map[string]any{"agent": "fakecat"}, nil); st != 409 || r["code"] != "agent_disabled" {
		t.Fatalf("ganti ke yang dimatikan: %d %v", st, r)
	}

	// what already runs it keeps running, and can restart
	var in map[string]any
	if h.getJSON("/api/sessions/"+sid, &in); in["status"] == "exited" {
		t.Fatalf("console yang jalan ikut mati: %v", in)
	}
	if st, r := h.do("POST", "/api/sessions/"+sid+"/restart", map[string]any{}, nil); st != 200 {
		t.Fatalf("restart console yang sudah ada: %d %v", st, r)
	}

	// a shell is no agent
	patch(map[string]any{"disabled": map[string]any{"shell": true}})
	if l := list("/api/machines/local/agents"); !has(l, "shell") {
		t.Fatal("shell hilang")
	}
	st, sh := create("shell")
	if st != 201 {
		t.Fatalf("shell: %d %v", st, sh)
	}
	shID := sh["id"].(string)
	t.Cleanup(func() { h.do("DELETE", "/api/sessions/"+shID, nil, nil) })
	patch(map[string]any{"disabled": map[string]any{"shell": nil}})

	// hidden runs as well: the planner stops, and nothing starts the CLI behind the scenes
	if ok, _ := (hubAdapter{h.srv}).PlannerHost(); ok {
		patch(map[string]any{"disabled": map[string]any{"claude": true}})
		if ok, _ := (hubAdapter{h.srv}).PlannerHost(); ok {
			t.Fatal("planner masih memakai Claude Code yang dimatikan")
		}
		patch(map[string]any{"disabled": map[string]any{"claude": nil}})
	}
	patch(map[string]any{"disabled": map[string]any{"codex": true}})
	if _, err := h.srv.submitRunner(jobs.RunnerRequest{Agent: "codex", Prompt: "x"}); !errors.Is(err, session.ErrAgentDisabled) {
		t.Fatalf("runner tersembunyi: %v", err)
	}
	patch(map[string]any{"disabled": map[string]any{"codex": nil}})

	// a bad id is refused, and null turns it on again with the old default back
	if st, _ := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"disabled": map[string]any{"BAD ID": true}}}, nil); st == 200 {
		t.Fatal("id buruk diterima")
	}
	patch(map[string]any{"disabled": map[string]any{"fakecat": nil}})
	l = list("/api/machines/local/agents")
	if !has(l, "fakecat") || l.DefaultAgent != "fakecat" {
		t.Fatalf("menyala lagi: default %s", l.DefaultAgent)
	}
	time.Sleep(10 * time.Millisecond)
}
