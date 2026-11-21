package server

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// One NextCode (docs/API-agents.md §30): a spawn of NextCode starts on Settings › NextCode's
// account, a chat moves to a model of another account with the chat so far, and a removed account
// leaves the settings.
func TestNextCodeAccounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	t.Setenv("FAKE_DIR", dir)
	bin := fakeAPIAgent(t, dir)
	paths := map[string]any{"local": map[string]any{"claude": filepath.Join(dir, "claude"), "nextcode-api": bin}}
	if st, r := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": paths}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, r)
	}
	api := newFakeAPI(t)
	st, pv := h.do("POST", "/api/providers", map[string]any{"name": "Router", "kind": "openai", "api": "chat", "baseUrl": api.URL + "/v1", "model": "glm-4.6", "apiKey": providerKey}, nil)
	if st != 201 {
		t.Fatalf("provider %d %v", st, pv)
	}
	pid := pv["id"].(string)
	var profs []map[string]any
	h.getJSON("/api/profiles", &profs)
	home := ""
	for _, p := range profs {
		if p["agent"] == "nextcode" && p["home"] == true {
			home = p["id"].(string)
		}
	}
	if home == "" {
		t.Fatalf("no home profile of NextCode: %v", profs)
	}

	// a spawn of NextCode that names nothing starts on the account of Settings › NextCode
	if st, r := h.do("PATCH", "/api/settings", map[string]any{"nextcode": map[string]any{"accounts": []string{home, pid}, "account": pid, "model": "glm-4.6", "effort": "high"}}, nil); st != 200 {
		t.Fatalf("accounts %d %v", st, r)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode", "cwd": dir}, nil)
	if st != 201 || in["agent"] != "nextcode-api" || in["providerId"] != pid || in["model"] != "glm-4.6" || in["effort"] != "high" || in["name"] != "NextCode" {
		t.Fatalf("spawned %d %v", st, in)
	}
	id := in["id"].(string)
	// one that names a model or a login stays on the agent asked for
	st, other := h.do("POST", "/api/sessions", map[string]any{"agent": "nextcode", "model": "sonnet", "cwd": dir}, nil)
	if st != 201 || other["agent"] != "nextcode" || other["profileId"] != home || other["providerId"] != nil && other["providerId"] != "" {
		t.Fatalf("with a model %d %v", st, other)
	}
	h.do("DELETE", "/api/sessions/"+other["id"].(string), nil, nil)

	h.waitEngine(id, "api")
	h.do("POST", "/api/sessions/"+id+"/nextcode/messages", map[string]any{"text": "hello"}, nil)
	h.waitChat(id, "the reply on the provider", func(it map[string]any) bool { return it["text"] == "Done: hello" })

	// a model of another account: the chat moves there, and stays
	if st, r := h.do("PUT", "/api/sessions/"+id+"/nextcode/model", map[string]any{"account": home, "model": "opus"}, nil); st != 200 {
		t.Fatalf("switch %d %v", st, r)
	}
	h.waitEngine(id, "claude")
	_, info := h.do("GET", "/api/sessions/"+id, nil, nil)
	if info["agent"] != "nextcode" || info["profileId"] != home || info["model"] != "opus" || info["providerId"] != nil && info["providerId"] != "" {
		t.Fatalf("after the switch %v", info)
	}
	note := h.waitChat(id, "the notice", func(it map[string]any) bool { return it["kind"] == "notice" })
	if txt, _ := note["text"].(string); !strings.HasPrefix(txt, "Now on Claude · opus: a new conversation there") {
		t.Fatalf("notice %q", txt)
	}
	h.waitInfo(id, "ready", func(in map[string]any) bool { return in["agentState"] == "ready" })
	h.do("POST", "/api/sessions/"+id+"/nextcode/messages", map[string]any{"text": "next"}, nil)
	reply := h.waitChat(id, "the reply after the move", func(it map[string]any) bool {
		txt, _ := it["text"].(string)
		return it["kind"] == "text" && strings.HasSuffix(txt, "next") && txt != "Done: hello"
	})
	if txt := reply["text"].(string); !strings.Contains(txt, "<conversation_so_far>") || !strings.Contains(txt, "User:\nhello") || !strings.Contains(txt, "Assistant:\nDone: hello") {
		t.Fatalf("the new conversation did not get the chat so far: %q", txt)
	}
	_, chat := h.do("GET", "/api/sessions/"+id+"/nextcode", nil, nil)
	var users []string
	for _, x := range chat["items"].([]any) {
		if it := x.(map[string]any); it["kind"] == "user" {
			users = append(users, it["text"].(string))
		}
	}
	if !slices.Equal(users, []string{"hello", "next"}) {
		t.Fatalf("the chat is the owner's, as they wrote it: %q", users)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config")); !strings.Contains(string(b), h.srv.st.Path("agents", "nextcode")) {
		t.Fatalf("the Claude engine ran without NextCode's home login: %q", b)
	}

	// a model of the same account switches the model only
	if st, r := h.do("PUT", "/api/sessions/"+id+"/nextcode/model", map[string]any{"account": home, "model": "sonnet"}, nil); st != 200 {
		t.Fatalf("same account %d %v", st, r)
	}
	if _, info := h.do("GET", "/api/sessions/"+id, nil, nil); info["model"] != "sonnet" || info["agent"] != "nextcode" {
		t.Fatalf("same account %v", info)
	}
	_, chat = h.do("GET", "/api/sessions/"+id+"/nextcode", nil, nil)
	notices := 0
	for _, x := range chat["items"].([]any) {
		if x.(map[string]any)["kind"] == "notice" {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("notices %d: %v", notices, chat["items"])
	}
	for _, c := range []struct {
		account string
		st      int
		code    string
	}{{"bogus", 400, "nextcode_account"}, {"ap_nope01", 404, "provider_not_found"}, {"pf_nope01", 404, "profile_not_found"}} {
		if st, r := h.do("PUT", "/api/sessions/"+id+"/nextcode/model", map[string]any{"account": c.account}, nil); st != c.st || r["code"] != c.code {
			t.Fatalf("%s: %d %v", c.account, st, r)
		}
	}

	// a removed provider leaves NextCode's accounts and its default
	h.do("DELETE", "/api/sessions/"+id, nil, nil)
	if st, r := h.do("DELETE", "/api/providers/"+pid, nil, nil); st != 200 {
		t.Fatalf("delete %d %v", st, r)
	}
	if nc := h.srv.settings.Get().NextCode; !slices.Equal(nc.Accounts, []string{home}) || nc.Account != "" || nc.Model != "" || nc.Effort != "" {
		t.Fatalf("after the provider went %+v", nc)
	}

	// and so does a removed login
	st, pf := h.do("POST", "/api/profiles", map[string]any{"agent": "nextcode-codex", "name": "Work", "create": true}, nil)
	if st != 201 {
		t.Fatalf("profile %d %v", st, pf)
	}
	work := pf["id"].(string)
	h.do("PATCH", "/api/settings", map[string]any{"nextcode": map[string]any{"accounts": []string{work, home}, "account": work, "model": "gpt-5.5"}}, nil)
	if st, r := h.do("DELETE", "/api/profiles/"+work, nil, nil); st != 200 {
		t.Fatalf("delete profile %d %v", st, r)
	}
	if nc := h.srv.settings.Get().NextCode; !slices.Equal(nc.Accounts, []string{home}) || nc.Account != "" || nc.Model != "" {
		t.Fatalf("after the login went %+v", nc)
	}
}
