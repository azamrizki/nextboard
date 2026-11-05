package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"nextboard/internal/agentstate"
	"nextboard/internal/jobs"
	"nextboard/internal/session"
)

func (h *testHub) list(path string) []any {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.ts.URL+path, nil)
	resp, err := h.c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func (h *testHub) eventsCh(topics string) (*websocket.Conn, chan map[string]any) {
	h.t.Helper()
	hdr := http.Header{"Origin": {h.ts.URL}}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	path := "/api/events"
	if topics != "" {
		path += "?topics=" + topics
	}
	ws, _, err := h.dialWS(path, hdr)
	if err != nil {
		h.t.Fatal(err)
	}
	ch := make(chan map[string]any, 256)
	go func() {
		defer close(ch)
		for {
			_, b, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			ch <- m
		}
	}()
	h.t.Cleanup(func() { ws.Close() })
	return ws, ch
}

func waitEvt(t *testing.T, ch chan map[string]any, typ string) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatalf("events closed waiting for %s", typ)
			}
			if m["type"] == typ {
				return m
			}
		case <-deadline:
			t.Fatalf("no %s event", typ)
		}
	}
}

func TestAgentCatalogAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	_, evs := h.eventsCh("")
	all := h.list("/api/agents")
	if len(all) < 20 || all[0].(map[string]any)["id"] != "shell" {
		t.Fatalf("catalog %d", len(all))
	}
	presets := h.list("/api/presets") // phase 1–4 clients
	if len(presets) != len(all) || presets[1].(map[string]any)["resumeArgs"].([]any)[0] != "--continue" {
		t.Fatalf("presets %v", presets[1])
	}
	st, body := h.do("GET", "/api/machines/local/agents", nil, nil)
	agents, _ := body["agents"].([]any)
	if st != 200 || body["defaultAgent"] == "" || len(agents) != len(all) || agents[0].(map[string]any)["available"] != true {
		t.Fatalf("machine agents %d %v", st, body["defaultAgent"])
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"name": "Shelly", "command": "sh"}, nil); st != 201 || a["id"] != "shelly" || a["source"] != "custom" {
		t.Fatalf("create %d %v", st, a)
	}
	waitEvt(t, evs, "agents")
	if st, a := h.do("POST", "/api/agents", map[string]any{"name": "x", "command": "y", "liveStatus": "screen"}, nil); st != 400 || a["code"] != "bad_agent" {
		t.Fatalf("bad %d %v", st, a)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "shelly", "name": "x", "command": "y"}, nil); st != 409 || a["code"] != "agent_exists" {
		t.Fatalf("exists %d %v", st, a)
	}
	if st, a := h.do("PATCH", "/api/agents/claude", map[string]any{"name": "x"}, nil); st != 403 || a["code"] != "agent_builtin" {
		t.Fatalf("builtin %d %v", st, a)
	}
	if st, a := h.do("PATCH", "/api/agents/shelly", map[string]any{"color": "#123456"}, nil); st != 200 || a["color"] != "#123456" {
		t.Fatalf("patch %d %v", st, a)
	}
	// A custom without yolo never gets the flag, even when asked.
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "shelly", "yolo": true, "handle": "rex"}, nil)
	if st != 201 || in["yolo"] != false || in["handle"] != "rex" {
		t.Fatalf("session %d %v", st, in)
	}
	if st, a := h.do("DELETE", "/api/agents/shelly", nil, nil); st != 200 || a["ok"] != true {
		t.Fatalf("delete %d", st)
	}
	// The running console keeps working after its agent was deleted.
	if _, in2 := h.do("GET", "/api/sessions/"+in["id"].(string), nil, nil); in2["command"] != "sh" {
		t.Fatalf("console lost its command %v", in2)
	}
}

func TestHandles(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	ids := []string{}
	for i := 0; i < 26; i++ {
		_, in := h.do("POST", "/api/sessions", map[string]any{"agent": "shell"}, nil)
		ids = append(ids, in["id"].(string))
		if i == 0 && in["handle"] != "Arkan" || i == 23 && in["handle"] != "Zahra" || i == 24 && in["handle"] != "Arkan2" || i == 25 && in["handle"] != "Bintang2" {
			t.Fatalf("#%d handle %v", i, in["handle"])
		}
	}
	if st, b := h.do("PATCH", "/api/sessions/"+ids[1], map[string]any{"handle": "arkan"}, nil); st != 409 || b["code"] != "handle_taken" {
		t.Fatalf("case-insensitive %d %v", st, b)
	}
	for _, bad := range []string{"everyone", "Hub", "9lives", "a b", strings.Repeat("x", 21)} {
		if st, b := h.do("PATCH", "/api/sessions/"+ids[1], map[string]any{"handle": bad}, nil); st != 400 || b["code"] != "bad_handle" {
			t.Fatalf("%q: %d %v", bad, st, b)
		}
	}
	if st, b := h.do("PATCH", "/api/sessions/"+ids[1], map[string]any{"handle": "Rex-2", "name": "Renamed"}, nil); st != 200 || b["handle"] != "Rex-2" || b["name"] != "Renamed" {
		t.Fatalf("rename %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions", map[string]any{"agent": "shell", "handle": "REX-2"}, nil); st != 409 || b["code"] != "handle_taken" {
		t.Fatalf("create taken %d %v", st, b)
	}
	h.do("DELETE", "/api/sessions/"+ids[0], nil, nil) // frees Arkan
	if _, in := h.do("POST", "/api/sessions", map[string]any{"agent": "shell"}, nil); in["handle"] != "Arkan" {
		t.Fatalf("handle not released: %v", in["handle"])
	}
}

func TestSettingsAPIAndEvent(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	_, evs := h.eventsCh("settings")
	if snap := waitEvt(t, evs, "snapshot"); snap["sessions"] != nil || snap["machines"] != nil {
		t.Fatalf("snapshot not filtered: %v", snap)
	}
	st, doc := h.do("GET", "/api/settings", nil, nil)
	if st != 200 || doc["schemaVersion"] != float64(1) {
		t.Fatalf("get %d %v", st, doc)
	}
	st, doc = h.do("PATCH", "/api/settings", map[string]any{"notifications": map[string]any{"finished": false}}, nil)
	if st != 200 || doc["notifications"].(map[string]any)["finished"] != false || doc["agents"].(map[string]any)["hookChannel"] != "auto" {
		t.Fatalf("patch %d %v", st, doc)
	}
	ev := waitEvt(t, evs, "settings")
	if ev["settings"].(map[string]any)["notifications"].(map[string]any)["finished"] != false {
		t.Fatalf("event %v", ev)
	}
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"shortcuts": map[string]any{"pen": "v"}}, nil); st != 400 || b["code"] != "bad_shortcut" {
		t.Fatalf("dup shortcut %d %v", st, b)
	}
	// voice settings are per client (API-studio §11): never a hub key
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"voice": map[string]any{}}, nil); st != 400 || b["code"] != "bad_setting" {
		t.Fatalf("unknown key %d %v", st, b)
	}
}

func TestAttentionFilterAndTopics(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	_, evs := h.eventsCh("attention")
	waitEvt(t, evs, "snapshot")
	in := session.Info{Spec: session.Spec{ID: "s_x", Handle: "Arkan", Agent: "claude"}, MachineName: "devbox"}
	h.srv.onAttention(in, agentstate.Event{Kind: agentstate.EvNeedsYou, Activity: "Permission: Bash(npm install)"})
	ev := waitEvt(t, evs, "attention")
	a := ev["attention"].(map[string]any)
	if a["kind"] != "needsYou" || a["title"] != "Arkan needs you" || a["body"] != "Permission: Bash(npm install)" || a["seq"] != float64(1) {
		t.Fatalf("attention %v", a)
	}
	// finished below finishedMinSec and disabled kinds are filtered at the hub.
	h.srv.onAttention(in, agentstate.Event{Kind: agentstate.EvFinished, Turn: agentstate.LastTurn{DurationMs: 5000}})
	h.srv.onAttention(in, agentstate.Event{Kind: agentstate.EvFinished, Turn: agentstate.LastTurn{DurationMs: 45000}})
	h.do("PATCH", "/api/settings", map[string]any{"notifications": map[string]any{"error": false}}, nil)
	time.Sleep(100 * time.Millisecond)
	h.srv.onAttention(in, agentstate.Event{Kind: agentstate.EvError})
	h.srv.onAttention(in, agentstate.Event{Kind: agentstate.EvAgentExit, Rc: 0})
	_, body := h.do("GET", "/api/attention?since=1", nil, nil)
	items := body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["kind"] != "finished" || body["seq"] != float64(2) {
		t.Fatalf("since %v", body)
	}
	// A session event is not delivered to an attention-only subscriber.
	h.do("POST", "/api/sessions", map[string]any{"agent": "shell"}, nil)
	select {
	case m := <-evs:
		if m["type"] != "attention" {
			t.Fatalf("topic filter leaked %v", m["type"])
		}
	case <-time.After(500 * time.Millisecond):
	}
	// bootId changes with a new hub.
	h2 := newTestHub(t)
	if h2.srv.attn.bootID == h.srv.attn.bootID || !strings.HasPrefix(h.srv.attn.bootID, "b_") {
		t.Fatal("bootId")
	}
}

func TestProfilesAPILocal(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	if st, b := h.do("POST", "/api/profiles", map[string]any{"machineId": "local", "agent": "gemini", "name": "x", "create": true}, nil); st != 400 || b["code"] != "profile_unsupported" {
		t.Fatalf("unsupported %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/profiles", map[string]any{"agent": "claude", "name": "Old", "dir": "/definitely/not/here"}, nil); st != 400 || b["code"] != "profile_dir_missing" {
		t.Fatalf("missing dir %d %v", st, b)
	}
	st, pf := h.do("POST", "/api/profiles", map[string]any{"machineId": "local", "agent": "claude", "name": "Work", "create": true}, nil)
	if st != 201 || pf["createdByHub"] != true || pf["exists"] != true {
		t.Fatalf("create %d %v", st, pf)
	}
	dir := pf["dir"].(string)
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 || filepath.Dir(dir) != h.srv.st.Path("profiles") {
		t.Fatalf("dir %v %v", fi, err)
	}
	// Work, plus the NextBoard folders of Claude Code and Codex.
	if l := h.list("/api/profiles?machineId=local"); len(l) != 3 || l[2].(map[string]any)["exists"] != true {
		t.Fatalf("list %v", l)
	}
	// Spawn with the profile: env CLAUDE_CONFIG_DIR reaches the wrapper.
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "codex", "profileId": pf["id"]}, nil)
	if st != 400 || in["code"] != "profile_mismatch" {
		t.Fatalf("mismatch %d %v", st, in)
	}
	if st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "claude", "profileId": "pf_nope1234"}, nil); st != 404 || in["code"] != "profile_not_found" {
		t.Fatalf("not found %d %v", st, in)
	}
	if st, _ := h.do("PATCH", "/api/profiles/"+pf["id"].(string), map[string]any{"name": "Personal"}, nil); st != 200 {
		t.Fatal(st)
	}
	if st, _ := h.do("DELETE", "/api/profiles/"+pf["id"].(string)+"?purge=1", nil, nil); st != 200 {
		t.Fatal(st)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("purge kept the folder")
	}
}

func TestJobsAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	os.WriteFile(fake, []byte("#!/bin/sh\ncat >/dev/null\necho '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"OK\"}'\n"), 0o755)
	slow := filepath.Join(dir, "slow")
	os.WriteFile(slow, []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	j, err := h.srv.jobs.SubmitRunner(jobs.RunnerRequest{Purpose: "commit-subject", MachineID: "local", Cwd: dir, Agent: "claude", Command: fake, Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, b := h.do("GET", "/api/jobs/"+j.ID, nil, nil); b["state"] == "done" {
			if !strings.Contains(string(mustJSON(b["result"])), `"text":"OK"`) {
				t.Fatalf("result %v", b["result"])
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	j2, _ := h.srv.jobs.SubmitRunner(jobs.RunnerRequest{Purpose: "commit-subject", MachineID: "local", Cwd: dir, Agent: "claude", Command: slow, Prompt: "hi"})
	time.Sleep(300 * time.Millisecond)
	if st, b := h.do("POST", "/api/jobs/"+j2.ID+"/cancel", nil, nil); st != 200 || b["state"] != "cancelled" {
		t.Fatalf("cancel %d %v", st, b)
	}
	if _, b := h.do("GET", "/api/jobs?limit=10", nil, nil); len(b["items"].([]any)) != 2 {
		t.Fatalf("list %v", b)
	}
	if st, b := h.do("GET", "/api/jobs/jb_nope", nil, nil); st != 404 || b["code"] != "job_not_found" {
		t.Fatalf("404 %d %v", st, b)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestFSReadTailAndOffset(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	p := filepath.Join(t.TempDir(), "log.txt")
	os.WriteFile(p, []byte("héllo world ✓ end"), 0o644)
	_, b := h.do("GET", "/api/machines/local/fs/read?path="+p+"&tail=4", nil, nil)
	if b["content"] != " end" || b["truncated"] != false || b["offset"] == nil {
		t.Fatalf("tail %v", b)
	}
	_, b = h.do("GET", "/api/machines/local/fs/read?path="+p+"&offset=1&max=4", nil, nil)
	if b["content"] != "éll" || b["truncated"] != true || b["offset"] != float64(1) {
		t.Fatalf("offset %v", b) // starts inside "é": moved to the rune start? (é is at 1..2)
	}
	if st, _ := h.do("GET", "/api/machines/local/fs/read?path="+p+"&offset=1&tail=2", nil, nil); st != 400 {
		t.Fatalf("both %d", st)
	}
	// Without offset/tail the old behaviour stays.
	if _, b := h.do("GET", "/api/machines/local/fs/read?path="+p, nil, nil); b["content"] != "héllo world ✓ end" || b["offset"] != nil {
		t.Fatalf("plain %v", b)
	}
}
