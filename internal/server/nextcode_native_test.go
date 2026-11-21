package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/settings"
)

// fakeAPIAgent plays NextCode's own agent with the Claude Code fake (the agent speaks the same
// stream-json) and keeps the environment it was started with in $FAKE_DIR/env.
func fakeAPIAgent(t *testing.T, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(fakeNextCodeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "nextcode-agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nenv > \"$FAKE_DIR/env\"\nexec \"$FAKE_DIR/claude\" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// waitChat waits for an item of the console's chat that ok accepts.
func (h *testHub) waitChat(id, what string, ok func(it map[string]any) bool) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, chat := h.do("GET", "/api/sessions/"+id+"/nextcode", nil, nil)
		items, _ := chat["items"].([]any)
		for _, x := range items {
			if it, _ := x.(map[string]any); it != nil && ok(it) {
				return it
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("waiting for %s: %v", what, chat)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitEngine waits until the console's chat runs on engine.
func (h *testHub) waitEngine(id, engine string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, chat := h.do("GET", "/api/sessions/"+id+"/nextcode", nil, nil)
		if s, _ := chat["status"].(map[string]any); s != nil && s["engine"] == engine {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("not on the %s engine: %v", engine, chat["status"])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// machineAgent is one agent of GET /api/machines/{id}/agents.
func (h *testHub) machineAgent(machineID, agentID string) map[string]any {
	h.t.Helper()
	_, r := h.do("GET", "/api/machines/"+machineID+"/agents?all=1", nil, nil)
	list, _ := r["agents"].([]any)
	for _, x := range list {
		if a, _ := x.(map[string]any); a["id"] == agentID {
			return a
		}
	}
	h.t.Fatalf("no %s on %s: %v", agentID, machineID, r)
	return nil
}

// NextCode's own engine (docs/API-agents.md §30) with a fake agent: there on This PC only, no
// login, always on an API provider (the one picked, else Settings › NextCode's), its launch, what
// its usage ledger costs, and its conversation removed with the console.
func TestNextCodeAPIEngine(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.ssh/config of the machine running the test
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	bin := fakeAPIAgent(t, dir)
	t.Setenv("FAKE_DIR", dir)
	if st, r := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"nextcode-api": bin}}}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, r)
	}

	// no CLI to find on This PC; never on a server, even where agents work on servers
	if a := h.machineAgent("local", "nextcode-api"); a["available"] != true || a["engine"] != "api" || a["models"] != true || a["efforts"] != true {
		t.Fatalf("on This PC %v", a)
	}
	st, m := h.do("POST", "/api/machines", map[string]any{"name": "srv", "host": "127.0.0.1", "port": 1, "user": "alice", "authMethod": "agent"}, nil)
	if st != 201 {
		t.Fatalf("machine %d %v", st, m)
	}
	mid := m["id"].(string)
	if !h.srv.settings.View().Agents.RemoteTools {
		t.Fatal("agents.remoteTools is off")
	}
	if a := h.machineAgent(mid, "nextcode-api"); a["available"] != false {
		t.Fatalf("on a server %v", a)
	}
	if st, l := h.do("GET", "/api/machines/local/agents/nextcode-api/models", nil, nil); st != 200 || len(l["models"].([]any)) != 0 || fmt.Sprint(l["efforts"]) != "[low medium high xhigh max]" {
		t.Fatalf("models %d %v", st, l)
	}

	// no login: no home folder and no profiles of its own
	var profs []map[string]any
	h.getJSON("/api/profiles", &profs)
	ncHome := ""
	for _, p := range profs {
		if p["agent"] == "nextcode-api" {
			t.Fatalf("a profile of its own: %v", p)
		}
		if p["agent"] == "nextcode" && p["home"] == true {
			ncHome = p["id"].(string)
		}
	}
	if _, err := os.Stat(h.srv.st.Path("agents", "nextcode-api")); !os.IsNotExist(err) {
		t.Fatalf("a home folder: %v", err)
	}
	if st, r := h.do("POST", "/api/profiles", map[string]any{"agent": "nextcode-api", "name": "Mine", "create": true}, nil); st != 400 || r["code"] != "profile_unsupported" {
		t.Fatalf("profile %d %v", st, r)
	}

	// no provider picked and none in the settings
	if st, r := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode-api", "cwd": dir}, nil); st != 400 || r["code"] != "nextcode_needs_provider" ||
		!strings.Contains(r["error"].(string), "Settings › API providers") {
		t.Fatalf("without a provider %d %v", st, r)
	}

	api := newFakeAPI(t)
	st, pv := h.do("POST", "/api/providers", map[string]any{"name": "Router", "kind": "openai", "api": "chat", "baseUrl": api.URL + "/v1", "model": "glm-4.6",
		"apiKey": providerKey, "prices": map[string]any{"glm-4.6": map[string]any{"input": 1, "output": 2}}}, nil)
	if st != 201 {
		t.Fatalf("provider %d %v", st, pv)
	}
	pid := pv["id"].(string)
	// a Chat Completions provider runs no Claude Code nor Codex, but NextCode's own agent, as the lead sees
	for _, pi := range h.srv.providerInfos() {
		if pi.ID == pid && (!slices.Contains(pi.Agents, "nextcode-api") || slices.Contains(pi.Agents, "nextcode")) {
			t.Fatalf("lead's providers %+v", pi)
		}
	}

	// a login profile, a server and a read-only part are refused
	if ncHome == "" {
		t.Fatal("no NextCode home profile")
	}
	if st, r := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode-api", "providerId": pid, "profileId": ncHome, "cwd": dir}, nil); st != 400 || r["code"] != "profile_mismatch" {
		t.Fatalf("with a profile %d %v", st, r)
	}
	if st, r := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode-api", "providerId": pid, "machineId": mid}, nil); st != 422 || r["code"] != "nextcode_this_pc_only" {
		t.Fatalf("on a server %d %v", st, r)
	}
	shop, ops := filepath.Join(dir, "shop"), filepath.Join(dir, "ops")
	for _, d := range []string{shop, ops} {
		_ = os.MkdirAll(d, 0o755)
	}
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": shop, "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	prID, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	_, p = h.do("POST", "/api/projects/"+prID+"/parts", map[string]any{"name": "Ops", "machineId": "local", "path": ops}, nil)
	opsID := p["parts"].([]any)[1].(map[string]any)["id"].(string)
	h.do("PATCH", "/api/projects/"+prID+"/parts/"+opsID, map[string]any{"readOnly": true}, nil)
	if st, r := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode-api", "providerId": pid, "workspaceId": wid, "partId": opsID}, nil); st != 422 || r["code"] != "nextcode_this_pc_only" {
		t.Fatalf("in a read-only part %d %v", st, r)
	}

	// an effort it does not take
	if st, r := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode-api", "providerId": pid, "effort": "minimal", "cwd": dir}, nil); st != 400 {
		t.Fatalf("effort %d %v", st, r)
	}

	// none picked: Settings › NextCode's provider, on its model
	if st, r := h.do("PATCH", "/api/settings", map[string]any{"nextcode": map[string]any{"provider": pid}}, nil); st != 200 {
		t.Fatalf("nextcode.provider %d %v", st, r)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode-api", "cwd": dir}, nil)
	if st != 201 || in["providerId"] != pid || in["profileId"] != "" || in["agent"] != "nextcode-api" || in["model"] != nil {
		t.Fatalf("console %d %v", st, in)
	}
	id := in["id"].(string)
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" && in["status"] == "running" })
	h.waitEngine(id, "api")
	if st, r := h.do("POST", "/api/sessions/"+id+"/nextcode/messages", map[string]any{"text": "hello"}, nil); st != 202 {
		t.Fatalf("message %d %v", st, r)
	}
	h.waitChat(id, "the reply", func(it map[string]any) bool { return it["kind"] == "text" && it["text"] == "Done: hello" })
	_, info := h.do("GET", "/api/sessions/"+id, nil, nil)
	aid, _ := info["agentSessionId"].(string)
	if len(aid) != 36 {
		t.Fatalf("agentSessionId %v", info["agentSessionId"])
	}

	// its launch: the hub's context, where and how to call the provider, its model, the mode;
	// the key in the environment only
	b, _ := os.ReadFile(filepath.Join(dir, "args"))
	want := strings.Join([]string{"nextcode-agent", "--session-id", aid,
		"--append-system-prompt-file", h.srv.st.Path("agents", "prompts", id+".md"), "--add-dir", h.srv.st.Path("attachments"),
		"--provider-kind", "openai", "--base-url", api.URL + "/v1", "--api", "chat", "--model", "glm-4.6", "--permission-mode", "default"}, " ")
	if got := strings.TrimSpace(string(b)); got != want {
		t.Fatalf("the agent's arguments\n%s\nwant\n%s", got, want)
	}
	env := envFile(t, filepath.Join(dir, "env"))
	home := h.srv.st.Path("providers", pid, "nextcode")
	for k, v := range map[string]string{"NB_PROVIDER_KEY": providerKey, "NEXTCODE_HOME": home, "NEXTCODE_STATE": h.srv.st.Path("nextcode", "api"), "NEXTBOARD_SESSION_ID": id} {
		if env[k] != v {
			t.Errorf("%s is not %q", k, v)
		}
	}
	// the commands that ask the owner first (the owner's list, here the default one); no sandbox
	// outside a project that has one
	var guard map[string]any
	if err := json.Unmarshal([]byte(env["NB_NEXTCODE_GUARD"]), &guard); err != nil || !strings.Contains(fmt.Sprint(guard["blocked"]), "git push --force") || guard["sandbox"] != nil {
		t.Errorf("NB_NEXTCODE_GUARD %q (%v)", env["NB_NEXTCODE_GUARD"], err)
	}
	if env["CLAUDE_CONFIG_DIR"] != os.Getenv("CLAUDE_CONFIG_DIR") || env["ANTHROPIC_BASE_URL"] != os.Getenv("ANTHROPIC_BASE_URL") {
		t.Error("a Claude Code launch's environment")
	}
	for _, d := range []string{home, h.srv.st.Path("nextcode", "api")} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, err, fi)
		}
	}
	if list, _ := os.ReadDir(home); len(list) != 0 {
		t.Fatalf("seeded %v", list)
	}

	// what it spent: its usage ledger, at the provider's prices; a spent budget refuses a console
	now := time.Now().UTC().Format(time.RFC3339Nano)
	line := fmt.Sprintf(`{"at":%q,"model":"glm-4.6","purpose":"nextcode","input":1000000,"output":100000,"cacheRead":0,"cacheWrite":0}`, now)
	if err := os.WriteFile(filepath.Join(home, "usage.jsonl"), []byte(line+"\n"+line+"\n"+`{"at":"`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, spend := h.do("GET", "/api/providers/"+pid+"/spend", nil, nil)
	// 2M in × $1 + 200k out × $2 per million
	if c, _ := spend["cost"].(float64); c < 2.3999 || c > 2.4001 {
		t.Fatalf("spend %v", spend)
	}
	if _, err := h.srv.providers.Update(pid, providerInputBudget(2)); err != nil {
		t.Fatal(err)
	}
	if st, r := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode-api", "cwd": dir}, nil); st != 409 || r["code"] != "provider_budget" {
		t.Fatalf("over budget %d %v", st, r)
	}
	if _, err := h.srv.providers.Update(pid, providerInputBudget(0)); err != nil {
		t.Fatal(err)
	}

	// deleting the console removes its conversation and the agent's files beside it, only those
	state := h.srv.st.Path("nextcode", "api")
	gone := []string{aid + ".json", aid + ".todos.json"}
	kept := []string{"0199a1b2-c3d4-4e5f-8a9b-0c1d2e3f4a5b.json", aid + ".json.bak", aid + "x.json"}
	for _, f := range append(slices.Clone(gone), kept...) {
		if err := os.WriteFile(filepath.Join(state, f), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := h.do("DELETE", "/api/sessions/"+id, nil, nil); st != 200 && st != 204 {
		t.Fatalf("delete %d", st)
	}
	for _, f := range gone {
		if _, err := os.Stat(filepath.Join(state, f)); !os.IsNotExist(err) {
			t.Errorf("%s after the delete: %v", f, err)
		}
	}
	for _, f := range kept {
		if _, err := os.Stat(filepath.Join(state, f)); err != nil {
			t.Errorf("%s went with the console: %v", f, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "args")); strings.Contains(string(b), providerKey) {
		t.Fatal("the key is on the agent's command line")
	}
}

// A NextCode console changes engine in place to and from NextCode's own one: that engine always
// gets a provider (Settings › NextCode's when none is picked), the Claude one goes back to its
// login when the provider cannot run it. The lead's way of starting consoles works the same, and
// removing the provider clears the setting.
func TestNextCodeAPIEngineSwitch(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	api := newFakeAPI(t)
	st, pv := h.do("POST", "/api/providers", map[string]any{"name": "Router", "kind": "openai", "api": "chat", "baseUrl": api.URL + "/v1", "model": "glm-4.6", "apiKey": providerKey}, nil)
	if st != 201 {
		t.Fatalf("provider %d %v", st, pv)
	}
	pid := pv["id"].(string)
	h.do("PATCH", "/api/settings", map[string]any{"nextcode": map[string]any{"provider": pid}}, nil)

	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode", "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("console %d %v", st, in)
	}
	id, login := in["id"].(string), in["profileId"]
	h.waitEngine(id, "claude")
	restart := func(body map[string]any, wantSt int, wantCode string) map[string]any {
		t.Helper()
		st, r := h.do("POST", "/api/sessions/"+id+"/restart", body, nil)
		if st != wantSt || (wantCode != "" && r["code"] != wantCode) {
			t.Fatalf("restart %v: %d %v", body, st, r)
		}
		return r
	}
	providerOf := func(in map[string]any) string { s, _ := in["providerId"].(string); return s }
	if r := restart(map[string]any{"agent": "nextcode-api"}, 200, ""); providerOf(r) != pid || r["profileId"] != "" || r["agent"] != "nextcode-api" {
		t.Fatalf("to its own engine %v", r)
	}
	h.waitEngine(id, "api")
	// back: Claude Code cannot run on a Chat Completions provider, so its own login again
	if r := restart(map[string]any{"agent": "nextcode"}, 200, ""); providerOf(r) != "" || r["profileId"] != login || login == "" {
		t.Fatalf("back to the Claude engine %v", r)
	}
	h.waitEngine(id, "claude")
	h.do("PATCH", "/api/settings", map[string]any{"nextcode": map[string]any{"provider": ""}}, nil)
	restart(map[string]any{"agent": "nextcode-api"}, 400, "nextcode_needs_provider")
	restart(map[string]any{"agent": "nextcode-api", "providerId": pid, "profileId": login}, 400, "profile_mismatch")
	if r := restart(map[string]any{"agent": "nextcode-api", "providerId": pid}, 200, ""); providerOf(r) != pid {
		t.Fatalf("with a provider %v", r)
	}
	h.waitEngine(id, "api")
	// its provider stays across a plain restart, and cannot be turned into a login
	if r := restart(map[string]any{}, 200, ""); providerOf(r) != pid {
		t.Fatalf("restart %v", r)
	}
	restart(map[string]any{"providerId": ""}, 400, "nextcode_needs_provider")

	// started as the lead starts consoles: with a provider, or refused with the reason
	proj := filepath.Join(dir, "shop")
	_ = os.MkdirAll(proj, 0o755)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": proj, "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	wid := p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	if st, r := h.do("POST", "/api/workspaces/"+wid+"/spawn", map[string]any{"slots": []any{map[string]any{"agent": "nextcode-api"}}}, nil); st != 400 || r["code"] != "nextcode_needs_provider" {
		t.Fatalf("spawn without a provider %d %v", st, r)
	}
	st, r := h.do("POST", "/api/workspaces/"+wid+"/spawn", map[string]any{"slots": []any{map[string]any{"agent": "nextcode-api", "providerId": pid}}}, nil)
	if st != 201 {
		t.Fatalf("spawn %d %v", st, r)
	}
	spawned := r["sessions"].([]any)[0].(map[string]any)
	if spawned["agent"] != "nextcode-api" || spawned["providerId"] != pid {
		t.Fatalf("spawned %v", spawned)
	}

	// removing the provider clears Settings › NextCode's choice
	h.do("PATCH", "/api/settings", map[string]any{"nextcode": map[string]any{"provider": pid}}, nil)
	if st, r := h.do("DELETE", "/api/providers/"+pid, nil, nil); st != 409 || r["code"] != "provider_in_use" {
		t.Fatalf("delete in use %d %v", st, r)
	}
	h.do("DELETE", "/api/sessions/"+id, nil, nil)
	h.do("DELETE", "/api/sessions/"+spawned["id"].(string), nil, nil)
	if st, r := h.do("DELETE", "/api/providers/"+pid, nil, nil); st != 200 {
		t.Fatalf("delete %d %v", st, r)
	}
	if v := h.srv.settings.Get().NextCode.Provider; v != "" {
		t.Fatalf("nextcode.provider still %q", v)
	}
}

// An instance's copy keeps Settings › NextCode's provider when it copied that provider, and none
// otherwise, with a note: that engine has no login to go back to.
func TestRemapNextCodeProvider(t *testing.T) {
	src := settings.Defaults()
	src.NextCode.Provider = "ap_kept1"
	next, notes := remapImportedSettings(src, settings.Defaults(), map[string]string{"ap_kept1": "ap_new01"}, map[string]bool{"local": true})
	if next.NextCode.Provider != "ap_new01" || len(notes) != 0 {
		t.Fatalf("copied: %q %v", next.NextCode.Provider, notes)
	}
	next, notes = remapImportedSettings(src, settings.Defaults(), map[string]string{}, map[string]bool{"local": true})
	if next.NextCode.Provider != "" || len(notes) != 1 || !strings.Contains(notes[0], "nextcode.provider") {
		t.Fatalf("not copied: %q %v", next.NextCode.Provider, notes)
	}
}

// ---- end to end through the real agent

// fakeChatModel is an OpenAI-compatible Chat Completions API with a scripted model, streamed as
// SSE: "run tests" asks for Bash {"command":"echo ok"}, and given its result says so; anything
// else gets a text answer. Each answer reports its tokens. Only the provider's key is let in.
type fakeChatModel struct {
	*httptest.Server
	mu    sync.Mutex
	calls int
	bad   []string // what a request had wrong
}

const fakeChatAnswer, fakeChatAfterTool = "Hello from the fake model.", "Tests ran: ok"

// chatMessage is a message of a Chat Completions request, as far as the fake reads it.
type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func newFakeChatModel(t *testing.T) *fakeChatModel {
	f := &fakeChatModel{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+providerKey {
			f.note("a request without the provider's key: " + r.Method + " " + r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"invalid key"}}`)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"fake-chat-1"}]}`)
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			f.note("an unexpected request: " + r.Method + " " + r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model    string        `json:"model"`
			Stream   bool          `json:"stream"`
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
			f.note(fmt.Sprintf("a request that is not one: %v", err))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Model != "fake-chat-1" {
			f.note("the model " + req.Model)
		}
		f.mu.Lock()
		f.calls++
		n := f.calls
		f.mu.Unlock()
		// the turn: after the last message of the owner, a tool's result, or "run tests", or anything
		user := -1
		for i, m := range req.Messages {
			if m.Role == "user" {
				user = i
			}
		}
		var text []string
		var tool *map[string]any
		switch {
		case user >= 0 && slices.ContainsFunc(req.Messages[user+1:], func(m chatMessage) bool {
			return m.Role == "tool" && strings.Contains(chatText(m.Content), "ok")
		}):
			text = []string{"Tests ", "ran: ok"}
		case user >= 0 && strings.Contains(chatText(req.Messages[user].Content), "run tests"):
			tool = &map[string]any{"id": "call_" + strconv.Itoa(n), "name": "Bash", "arguments": `{"command":"echo ok"}`}
		default:
			text = []string{"Hello", " from the fake", " model."}
		}
		id := "chatcmpl-" + strconv.Itoa(n)
		usage := map[string]any{"prompt_tokens": 1200, "completion_tokens": 30, "total_tokens": 1230}
		if !req.Stream {
			msg := map[string]any{"role": "assistant", "content": strings.Join(text, "")}
			finish := "stop"
			if tool != nil {
				call := *tool
				msg["content"], finish = nil, "tool_calls"
				msg["tool_calls"] = []any{map[string]any{"id": call["id"], "type": "function", "function": map[string]any{"name": call["name"], "arguments": call["arguments"]}}}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "object": "chat.completion", "created": 1, "model": "fake-chat-1",
				"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}, "usage": usage})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fl, _ := w.(http.Flusher)
		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		}
		chunk := func(delta map[string]any, finish any) map[string]any {
			return map[string]any{"id": id, "object": "chat.completion.chunk", "created": 1, "model": "fake-chat-1",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		}
		send(chunk(map[string]any{"role": "assistant", "content": ""}, nil))
		finish := "stop"
		if tool != nil {
			call := *tool
			send(chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": call["id"], "type": "function",
				"function": map[string]any{"name": call["name"], "arguments": ""}}}}, nil))
			args := call["arguments"].(string)
			for _, part := range []string{args[:len(args)/2], args[len(args)/2:]} {
				send(chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": part}}}}, nil))
			}
			finish = "tool_calls"
		}
		for _, s := range text {
			send(chunk(map[string]any{"content": s}, nil))
		}
		send(chunk(map[string]any{}, finish))
		send(map[string]any{"id": id, "object": "chat.completion.chunk", "created": 1, "model": "fake-chat-1", "choices": []any{}, "usage": usage})
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeChatModel) note(s string) {
	f.mu.Lock()
	f.bad = append(f.bad, s)
	f.mu.Unlock()
}

func (f *fakeChatModel) problems() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.bad)
}

// chatText is the text of a Chat Completions message's content: a string, or text parts.
func chatText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// readChat reads the chat's frames until an item that ok accepts; an error item fails the test.
func readChat(t *testing.T, ws *websocket.Conn, what string, ok func(it map[string]any) bool) map[string]any {
	t.Helper()
	var got map[string]any
	readFrame(t, ws, what, func(f map[string]any) bool {
		if f["t"] == "request" && what != "the request" {
			return false
		}
		it := itemOf(f)
		if it == nil {
			return false
		}
		if it["kind"] == "error" {
			t.Fatalf("waiting for %s: the agent failed: %v", what, it["text"])
		}
		if ok(it) {
			got = it
			return true
		}
		return false
	})
	return got
}

// agentProcs are the command lines and the environments they started with of the running
// NextCode agents that call baseURL (Linux: /proc).
func agentProcs(baseURL string) (argvs [][]string, envs []map[string]string) {
	list, _ := os.ReadDir("/proc")
	for _, e := range list {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if len(argv) < 2 || argv[1] != "nextcode-agent" || !slices.Contains(argv, baseURL) {
			continue
		}
		env := map[string]string{}
		if b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ")); err == nil {
			for _, kv := range strings.Split(string(b), "\x00") {
				k, v, _ := strings.Cut(kv, "=")
				env[k] = v
			}
		}
		argvs, envs = append(argvs, argv), append(envs, env)
	}
	return argvs, envs
}

// NextCode's own engine end to end through the real agent (internal/nextcode/native): the test
// binary is the hub's own program (TestMain) on an OpenAI-compatible Chat Completions API faked
// here. A streamed answer; a command the agent asks to run, the owner's allow, its output and the
// answer after it; the console's states; what the agent's usage ledger costs; and the key in the
// agent's environment, never on its command line.
func TestNextCodeAPIEngineEndToEnd(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	model := newFakeChatModel(t)
	base := model.URL + "/v1"
	st, pv := h.do("POST", "/api/providers", map[string]any{"name": "Fake", "kind": "openai", "api": "chat", "baseUrl": base, "model": "fake-chat-1",
		"apiKey": providerKey, "prices": map[string]any{"fake-chat-1": map[string]any{"input": 1, "output": 2}}}, nil)
	if st != 201 {
		t.Fatalf("provider %d %v", st, pv)
	}
	pid := pv["id"].(string)
	// the hub's own program is this test binary; say so, as agents.paths can
	if h.srv.sessions.SelfPath != exe {
		t.Fatalf("SelfPath %q, want %q", h.srv.sessions.SelfPath, exe)
	}
	h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"nextcode-api": exe}}}}, nil)

	dir := t.TempDir()
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode-api", "providerId": pid, "cwd": dir}, nil)
	if st != 201 {
		t.Fatalf("console %d %v", st, in)
	}
	id := in["id"].(string)
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" && in["status"] == "running" })

	hdr := http.Header{"Origin": {h.ts.URL}}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	ws, _, err := h.dialWS("/api/sessions/"+id+"/nextcode/ws", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	snap := readFrame(t, ws, "snapshot", func(f map[string]any) bool { return f["t"] == "snapshot" })
	if s, _ := snap["status"].(map[string]any); s["engine"] != "api" || s["process"] != "stopped" {
		t.Fatalf("snapshot %v", snap)
	}

	// a streamed answer
	if st, r := h.do("POST", "/api/sessions/"+id+"/nextcode/messages", map[string]any{"text": "hello"}, nil); st != 202 {
		t.Fatalf("message %d %v", st, r)
	}
	readChat(t, ws, "the reply", func(it map[string]any) bool { return it["kind"] == "text" && it["text"] == fakeChatAnswer })
	h.waitInfo(id, "ready after the turn", func(in map[string]any) bool { return in["agentState"] == "ready" })

	// the key is never on the agent's command line (the model got it, from its environment)
	if runtime.GOOS == "linux" {
		argvs, envs := agentProcs(base)
		if len(argvs) == 0 {
			t.Fatal("no running agent found")
		}
		_, info := h.do("GET", "/api/sessions/"+id, nil, nil)
		for i, argv := range argvs {
			line := strings.Join(argv, " ")
			if strings.Contains(line, providerKey) {
				t.Fatal("the key is on the agent's command line")
			}
			for _, want := range []string{"--provider-kind openai", "--base-url " + base, "--api chat", "--model fake-chat-1", "--permission-mode default",
				fmt.Sprint("--session-id ", info["agentSessionId"])} {
				if !strings.Contains(line, want) {
					t.Fatalf("no %q in the agent's command line %q", want, line)
				}
			}
			if envs[i]["NEXTCODE_HOME"] != h.srv.st.Path("providers", pid, "nextcode") || envs[i]["NEXTCODE_STATE"] != h.srv.st.Path("nextcode", "api") {
				t.Fatalf("the agent's folders: NEXTCODE_HOME %q, NEXTCODE_STATE %q", envs[i]["NEXTCODE_HOME"], envs[i]["NEXTCODE_STATE"])
			}
		}
	}

	// a command it asks to run: the owner allows it, its output, the answer after it
	h.do("POST", "/api/sessions/"+id+"/nextcode/messages", map[string]any{"text": "run tests"}, nil)
	var req map[string]any
	readFrame(t, ws, "the request", func(f map[string]any) bool {
		if it := itemOf(f); it != nil && it["kind"] == "error" {
			t.Fatalf("waiting for the request: the agent failed: %v", it["text"])
		}
		if f["t"] == "request" {
			req, _ = f["request"].(map[string]any)
			return true
		}
		return false
	})
	if input, _ := req["input"].(map[string]any); req["tool"] != "Bash" || req["kind"] != "permission" || input["command"] != "echo ok" {
		t.Fatalf("request %v", req)
	}
	in = h.waitInfo(id, "needs you", func(in map[string]any) bool { return in["agentState"] == "needsYou" })
	if act, _ := in["activity"].(string); !strings.Contains(act, "Bash") {
		t.Fatalf("activity %q", act)
	}
	if st, r := h.do("POST", "/api/sessions/"+id+"/nextcode/requests/"+req["id"].(string), map[string]any{"decision": "allow"}, nil); st != 200 {
		t.Fatalf("answer %d %v", st, r)
	}
	readChat(t, ws, "the tool's result", func(it map[string]any) bool {
		tool, _ := it["tool"].(map[string]any)
		out, _ := tool["output"].(string)
		return tool != nil && tool["name"] == "Bash" && tool["status"] == "done" && strings.Contains(out, "ok")
	})
	readChat(t, ws, "the answer after it", func(it map[string]any) bool { return it["kind"] == "text" && it["text"] == fakeChatAfterTool })
	h.waitInfo(id, "ready after the tests", func(in map[string]any) bool { return in["agentState"] == "ready" })

	// what it spent: the agent's usage ledger in the provider's folder, three calls of the model
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, spend := h.do("GET", "/api/providers/"+pid+"/spend", nil, nil)
		tok, _ := spend["tokens"].(map[string]any)
		if in, _ := tok["input"].(float64); in >= 3*1200 {
			if c, _ := spend["cost"].(float64); c <= 0 || fmt.Sprint(spend["unpriced"]) != "<nil>" {
				t.Fatalf("spend %v", spend)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("spend %v", spend)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if p := model.problems(); len(p) > 0 {
		t.Fatalf("the model was called wrongly: %v", p)
	}

	// deleting the console removes its conversation
	_, info := h.do("GET", "/api/sessions/"+id, nil, nil)
	aid, _ := info["agentSessionId"].(string)
	if st, _ := h.do("DELETE", "/api/sessions/"+id, nil, nil); st != 200 && st != 204 {
		t.Fatalf("delete %d", st)
	}
	if _, err := os.Stat(h.srv.st.Path("nextcode", "api", aid+".json")); aid == "" || !os.IsNotExist(err) {
		t.Fatalf("the conversation %q after the delete: %v", aid, err)
	}
}
