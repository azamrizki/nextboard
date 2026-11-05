package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeOrchAgent behaves like Claude Code for the delivery engine: it fires
// hooks through NB_HOOK_SH (real marker protocol), enables bracketed
// paste, draws Claude's bordered input box with the cursor in it, and
// logs every line it receives. FAKE_MODE=update shows a Codex-like update
// dialog instead and touches UPDATED if anything is ever typed into it.
const fakeOrchAgent = `#!/bin/sh
sid=""
while [ $# -gt 0 ]; do case "$1" in --session-id|--resume) sid="$2"; shift;; esac; shift; done
tp="$FAKE_DIR/$sid.jsonl"
esc=$(printf '\033')
hook() { printf '%s' "$2" | sh -c "set -- hook $1; eval \"\$NB_HOOK_SH\""; }
stty -echo 2>/dev/null
box() { printf '\r\n──────────────────────\r\n❯ \r\n──────────────────────\r\n  ⏸ manual mode on\033[2A\033[3G'; }
if [ "$FAKE_MODE" = update ]; then
  hook SessionStart "{\"session_id\":\"$sid\",\"transcript_path\":\"$tp\",\"hook_event_name\":\"SessionStart\"}"
  printf '\033[2J\033[H  Update available · 2.1.312 → 2.2.0\r\n\r\n› 1. Update now\r\n  2. Skip\r\n\r\n  enter continue · esc skip\033[3;1H'
  read -r x && touch "$FAKE_DIR/UPDATED"
  exit 0
fi
printf '\033[?2004h'
hook SessionStart "{\"session_id\":\"$sid\",\"transcript_path\":\"$tp\",\"hook_event_name\":\"SessionStart\"}"
box
while IFS= read -r line; do
  # A paste is one message, sent with the Return after it, as in a real CLI.
  case "$line" in *"${esc}[200~"*)
    while :; do
      case "$line" in *"${esc}[201~"*) break;; esac
      IFS= read -r more || break
      line="$line
$more"
    done;;
  esac
  line=$(printf '%s' "$line" | sed "s/${esc}\[20[01]~//g")
  printf '%s\n' "$line" >> "$FAKE_DIR/received.log"
  case "$line" in quit) exit 0;; esac
  case "$line" in /clear|/compact)
    hook SessionStart "{\"session_id\":\"$sid\",\"transcript_path\":\"$tp\",\"hook_event_name\":\"SessionStart\",\"source\":\"${line#/}\"}"
    box
    continue;;
  esac
  hook UserPromptSubmit '{"hook_event_name":"UserPromptSubmit"}'
  if [ "$line" = perm ]; then
    hook PreToolUse '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"npm test"}}'
    printf '\033[2J\033[H Bash(npm test)\r\n\r\n Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. No\r\n\r\n Esc to cancel'
    sleep 0.6
    hook Notification '{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission"}'
    read -r ans
    hook PostToolUse '{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"npm test"}}'
    sleep 2
  fi
  printf '{"type":"user","uuid":"u-%s","timestamp":"2026-10-28T10:00:00.000Z","isSidechain":false,"message":{"role":"user","content":"%s"}}\n' "$$" "$line" >> "$tp"
  printf '{"type":"assistant","uuid":"a-%s","timestamp":"2026-10-28T10:00:01.000Z","isSidechain":false,"message":{"id":"msg_%s","role":"assistant","content":[{"type":"text","text":"Done: %s"}]}}\n' "$$" "$$" "$line" >> "$tp"
  printf '\r\n● Done: %s\r\n' "$line"
  hook Stop '{"hook_event_name":"Stop"}'
  box
done
`

// orchSetup creates the fake agent ("fake-claude", base claude) and a
// project in a scratch folder; it returns the project/workspace ids and
// the folder.
func orchSetup(t *testing.T, h *testHub) (pid, wid, dir string) {
	t.Helper()
	h.setup()
	dir = t.TempDir()
	bin := filepath.Join(dir, "fakeorch")
	if err := os.WriteFile(bin, []byte(fakeOrchAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DIR", dir)
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "fake-claude", "name": "Fake Claude", "command": bin, "base": "claude"}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "orch"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	return p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string), dir
}

func (h *testHub) spawnAgent(wid string) map[string]any {
	h.t.Helper()
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "workspaceId": wid}, nil)
	if st != 201 {
		h.t.Fatalf("create %d %v", st, in)
	}
	return in
}

func received(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "received.log"))
	return string(b)
}

func waitTrue(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// TestCommandOrderDeliveryReport: command → order → delivery typed with
// bracketed paste into the agent's input prompt → confirmed → report with
// the reply from the transcript, all through the HTTP API.
func TestCommandOrderDeliveryReport(t *testing.T) {
	h := newTestHub(t)
	pid, wid, dir := orchSetup(t, h)
	in := h.spawnAgent(wid)
	id := in["id"].(string)
	h.waitInfo(id, "ready", state("ready"))
	st, r := h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": "@Arkan run the tests", "workspaceId": wid, "clientId": "c_web", "planner": "off"}, nil)
	if st != 202 || r["handledBy"] != "parser" || r["orderId"] == "" || r["threadId"] != pid {
		t.Fatalf("command %d %v", st, r)
	}
	oid := r["orderId"].(string)
	waitTrue(t, "line received", 10*time.Second, func() bool { return strings.Contains(received(dir), "run the tests\n") })
	if got := received(dir); got != "run the tests\n" {
		t.Fatalf("received %q", got)
	}
	var order map[string]any
	waitTrue(t, "order finished", 10*time.Second, func() bool {
		_, order = h.do("GET", "/api/orders/"+oid, nil, nil)
		tg := order["targets"].([]any)[0].(map[string]any)
		return tg["state"] == "finished"
	})
	var items []any
	waitTrue(t, "delivery confirmed", 5*time.Second, func() bool {
		_, dl := h.do("GET", "/api/sessions/"+id+"/deliveries", nil, nil)
		items = dl["items"].([]any)
		return len(items) == 1 && items[0].(map[string]any)["state"] == "confirmed"
	})
	if items[0].(map[string]any)["source"] != "order" || items[0].(map[string]any)["confirmedAt"] == nil {
		t.Fatalf("deliveries %v", items)
	}
	var report map[string]any
	waitTrue(t, "report", 10*time.Second, func() bool {
		_, msgs := h.do("GET", "/api/threads/"+pid+"/messages?limit=50", nil, nil)
		for _, m := range msgs["items"].([]any) {
			mm := m.(map[string]any)
			if mm["kind"] == "report" && mm["orderId"] == oid {
				report = mm
				return true
			}
		}
		return false
	})
	if report["text"] != "Done: run the tests" || report["handle"] != "Arkan" || report["report"].(map[string]any)["state"] != "finished" {
		t.Fatalf("report %v", report)
	}
	// Deliveries API: shell console, too long, list by project.
	_, sh := h.do("POST", "/api/sessions", map[string]any{"agent": "shell", "workspaceId": wid}, nil)
	if st, b := h.do("POST", "/api/sessions/"+sh["id"].(string)+"/deliveries", map[string]any{"text": "ls"}, nil); st != 409 || b["code"] != "not_agent" {
		t.Fatalf("shell %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": strings.Repeat("é", 32001)}, nil); st != 413 || b["code"] != "text_too_long" {
		t.Fatalf("too long %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "second"}, nil); st != 202 || b["source"] != "api" {
		t.Fatalf("api delivery %d %v", st, b)
	}
	waitTrue(t, "api delivery", 10*time.Second, func() bool { return strings.Contains(received(dir), "second\n") })
	_, all := h.do("GET", "/api/deliveries?projectId="+pid, nil, nil)
	if len(all["items"].([]any)) != 2 {
		t.Fatalf("by project %v", all)
	}
	if st, b := h.do("DELETE", "/api/deliveries/"+items[0].(map[string]any)["id"].(string), nil, nil); st != 409 || b["code"] != "already_sent" {
		t.Fatalf("cancel sent %d %v", st, b)
	}
	// Mentions and planner endpoints.
	ms := h.list("/api/projects/" + pid + "/mentions?workspaceId=" + wid + "&q=ar")
	if len(ms) != 1 || ms[0].(map[string]any)["kind"] != "console" || ms[0].(map[string]any)["handle"] != "Arkan" {
		t.Fatalf("mentions %v", ms)
	}
	if st, pl := h.do("GET", "/api/planner", nil, nil); st != 200 || pl["available"] == nil {
		t.Fatalf("planner %d %v", st, pl)
	}
}

// TestDeliveryNeverTypesIntoUpdateDialog: hooks say ready, but the screen
// is an update dialog whose default is "Update now". The delivery waits
// ("dialog") and fails with prompt_unrecognized; no byte reaches the CLI.
func TestDeliveryNeverTypesIntoUpdateDialog(t *testing.T) {
	h := newTestHub(t)
	_, wid, dir := orchSetup(t, h)
	t.Setenv("FAKE_MODE", "update")
	if _, err := h.srv.settings.Patch([]byte(`{"delivery":{"promptTimeoutSec":10}}`)); err != nil {
		t.Fatal(err)
	}
	in := h.spawnAgent(wid)
	id := in["id"].(string)
	h.waitInfo(id, "ready (hooks)", state("ready"))
	time.Sleep(500 * time.Millisecond)
	st, d := h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "1"}, nil)
	if st != 202 {
		t.Fatalf("%d %v", st, d)
	}
	did := d["id"].(string)
	waitTrue(t, "waiting on dialog", 5*time.Second, func() bool {
		_, l := h.do("GET", "/api/sessions/"+id+"/deliveries", nil, nil)
		x := l["items"].([]any)[0].(map[string]any)
		return x["state"] == "waiting" && x["waitReason"] == "dialog"
	})
	var final map[string]any
	waitTrue(t, "prompt_unrecognized", 15*time.Second, func() bool {
		_, l := h.do("GET", "/api/sessions/"+id+"/deliveries", nil, nil)
		final = l["items"].([]any)[0].(map[string]any)
		return final["state"] == "failed"
	})
	if final["id"] != did || final["code"] != "prompt_unrecognized" || !strings.Contains(final["error"].(string), "Update") {
		t.Fatalf("final %v", final)
	}
	if _, err := os.Stat(filepath.Join(dir, "UPDATED")); err == nil {
		t.Fatal("the update dialog received input")
	}
}

// fakePid finds the running fake agent process (its cmdline holds the
// script path).
func fakePid(t *testing.T, dir string) int {
	t.Helper()
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, _ := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if strings.Contains(string(b), filepath.Join(dir, "fakeorch")) {
			return pid
		}
	}
	t.Fatal("fake agent process not found")
	return 0
}

func procStateOf(pid int) string {
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	s := string(b)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2 : i+3]
	}
	return ""
}

// TestPauseResumeLocal: SIGSTOP of the foreground group of a real PTY
// child; a delivery waits while paused and goes out after resume.
func TestPauseResumeLocal(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("butuh /proc")
	}
	h := newTestHub(t)
	_, wid, dir := orchSetup(t, h)
	in := h.spawnAgent(wid)
	id := in["id"].(string)
	h.waitInfo(id, "ready", state("ready"))
	pid := fakePid(t, dir)
	st, p := h.do("POST", "/api/sessions/"+id+"/pause", nil, nil)
	if st != 200 || p["paused"] != true || p["pausedAt"] == nil {
		t.Fatalf("pause %d %v", st, p)
	}
	waitTrue(t, "stopped", 3*time.Second, func() bool { return procStateOf(pid) == "T" })
	if st, p := h.do("POST", "/api/sessions/"+id+"/pause", nil, nil); st != 200 || p["paused"] != true {
		t.Fatal("pause is idempotent")
	}
	h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "while paused"}, nil)
	time.Sleep(1500 * time.Millisecond)
	_, l := h.do("GET", "/api/sessions/"+id+"/deliveries", nil, nil)
	if x := l["items"].([]any)[0].(map[string]any); x["waitReason"] != "paused" || received(dir) != "" {
		t.Fatalf("paused delivery %v / %q", x, received(dir))
	}
	if _, info := h.do("GET", "/api/sessions/"+id, nil, nil); info["queue"].(float64) != 1 || info["agentState"] != "ready" {
		t.Fatalf("queue/state while paused %v", info)
	}
	st, p = h.do("POST", "/api/sessions/"+id+"/resume", nil, nil)
	if st != 200 || p["paused"] != false {
		t.Fatalf("resume %d %v", st, p)
	}
	waitTrue(t, "running", 3*time.Second, func() bool { return procStateOf(pid) != "T" })
	waitTrue(t, "delivered after resume", 10*time.Second, func() bool { return strings.Contains(received(dir), "while paused\n") })
	// pause-all / resume-all and a shell console.
	_, sh := h.do("POST", "/api/sessions", map[string]any{"agent": "shell", "workspaceId": wid}, nil)
	if st, b := h.do("POST", "/api/sessions/"+sh["id"].(string)+"/pause", nil, nil); st != 409 || b["code"] != "not_agent" {
		t.Fatalf("shell pause %d %v", st, b)
	}
	_, all := h.do("POST", "/api/workspaces/"+wid+"/pause-all", nil, nil)
	if len(all["sessions"].([]any)) != 1 {
		t.Fatalf("pause-all %v", all)
	}
	waitTrue(t, "stopped again", 3*time.Second, func() bool { return procStateOf(pid) == "T" })
	h.do("POST", "/api/workspaces/"+wid+"/resume-all", nil, nil)
	waitTrue(t, "resumed again", 3*time.Second, func() bool { return procStateOf(pid) != "T" })
}

// TestStalePermissionSingleAttention (phase 5 regression from the web
// e2e): with hooks live, the answered permission dialog stays drawn for a
// while; it must not raise needsYou again nor a second attention.
func TestStalePermissionSingleAttention(t *testing.T) {
	h := newTestHub(t)
	_, wid, _ := orchSetup(t, h)
	in := h.spawnAgent(wid)
	id := in["id"].(string)
	h.waitInfo(id, "ready", state("ready"))
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "perm\r"}, nil)
	h.waitInfo(id, "needsYou", state("needsYou"))
	time.Sleep(1200 * time.Millisecond) // Notification arrives
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "1\r"}, nil)
	h.waitInfo(id, "working after answer", state("working"))
	deadline := time.Now().Add(1800 * time.Millisecond)
	for time.Now().Before(deadline) { // the stale dialog is still on screen
		if _, x := h.do("GET", "/api/sessions/"+id, nil, nil); x["agentState"] == "needsYou" {
			t.Fatalf("stale dialog re-raised needsYou: %v", x["activity"])
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.waitInfo(id, "ready after turn", state("ready"))
	_, at := h.do("GET", "/api/attention?since=0", nil, nil)
	n := 0
	for _, a := range at["items"].([]any) {
		if a.(map[string]any)["kind"] == "needsYou" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d needsYou attentions: %v", n, at["items"])
	}
}

// TestSpawnGroupsDeployAPI: batch spawn in one boardOps event, group
// validation codes over the board API, handle/group name rule, Mission
// Map, and a layout deploy from a paired device (bearer).
func TestSpawnGroupsDeployAPI(t *testing.T) {
	h := newTestHub(t)
	pid, wid, _ := orchSetup(t, h)
	ws, ch := h.eventsCh("boardOps")
	defer ws.Close()
	<-ch // snapshot
	h.ops(wid, map[string]any{"op": "put", "node": map[string]any{"id": "n_orch000001", "kind": "orchestrator", "x": 0, "y": 0, "w": 400, "h": 500, "data": map[string]any{"title": "Orchestrator"}}})
	<-ch
	st, res := h.do("POST", "/api/workspaces/"+wid+"/spawn", map[string]any{
		"slots": []any{map[string]any{"agent": "shell"}, map[string]any{"agent": "shell"}, map[string]any{"agent": "shell"}, map[string]any{"agent": "shell"}},
		"group": map[string]any{"name": "Backend", "role": "backend"}, "orchestratorId": "n_orch000001"}, nil)
	if st != 201 || len(res["sessions"].([]any)) != 4 || res["groupNodeId"] == "" {
		t.Fatalf("spawn %d %v", st, res)
	}
	ev := <-ch
	kinds := map[string]int{}
	for _, op := range ev["ops"].([]any) {
		o := op.(map[string]any)
		if n, ok := o["node"].(map[string]any); ok {
			kinds[n["kind"].(string)]++
		}
		if e, ok := o["edge"].(map[string]any); ok {
			kinds["edge:"+e["kind"].(string)]++
		}
	}
	if kinds["terminal"] != 4 || kinds["group"] != 1 || kinds["edge:wire"] != 1 || ev["clientId"] != "" {
		t.Fatalf("one boardOps %v", kinds)
	}
	select {
	case extra := <-ch:
		t.Fatalf("second boardOps: %v", extra)
	case <-time.After(300 * time.Millisecond):
	}
	if st, b := h.do("POST", "/api/workspaces/"+wid+"/spawn", map[string]any{"count": 13, "agent": "shell"}, nil); st != 400 || b["code"] != "too_many" {
		t.Fatalf("too many %d %v", st, b)
	}
	// Board API validation with codes.
	first := res["sessions"].([]any)[0].(map[string]any)
	if st, b := h.ops(wid, map[string]any{"op": "put", "node": map[string]any{"id": "n_grp0000009", "kind": "group", "x": 0, "y": 0, "w": 10, "h": 10,
		"data": map[string]any{"name": first["handle"], "role": "general", "members": []any{}}}}); st != 409 || b["code"] != "name_conflict" {
		t.Fatalf("name = handle %d %v", st, b)
	}
	if st, b := h.ops(wid, map[string]any{"op": "putEdge", "edge": map[string]any{"id": "e_bad0000001", "kind": "wire", "source": res["groupNodeId"], "target": "n_orch000001"}}); st != 400 || b["code"] != "bad_edge" {
		t.Fatalf("bad edge %d %v", st, b)
	}
	if st, b := h.do("PATCH", "/api/sessions/"+first["id"].(string), map[string]any{"handle": "backend"}, nil); st != 409 || b["code"] != "handle_taken" {
		t.Fatalf("handle = group %d %v", st, b)
	}
	// Mission Map.
	st, m := h.do("GET", "/api/workspaces/"+wid+"/mission", nil, nil)
	if st != 200 || len(m["consoles"].([]any)) != 4 || len(m["groups"].([]any)) != 1 || len(m["directors"].([]any)) != 2 {
		t.Fatalf("mission %d %v", st, m)
	}
	// Deploy from Android (bearer), lane override with machine.
	st, ps := h.do("POST", "/api/pair/start", nil, nil)
	if st != 200 {
		t.Fatal(st)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	anon := &testHub{t: t, srv: h.srv, ts: h.ts, c: &http.Client{Transport: h.ts.Client().Transport}}
	_, pc := anon.do("POST", "/api/pair/complete", map[string]any{"code": ps["code"], "deviceName": "Pixel", "platform": "android", "publicKey": base64.StdEncoding.EncodeToString(pub)}, nil)
	_, chal := anon.do("POST", "/api/auth/challenge", map[string]any{"deviceId": pc["deviceId"]}, nil)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(chal["nonce"].(string))))
	_, tok := anon.do("POST", "/api/auth/device", map[string]any{"deviceId": pc["deviceId"], "nonce": chal["nonce"], "sig": sig}, nil)
	bearer := map[string]string{"Authorization": "Bearer " + tok["accessToken"].(string)}
	layouts := h.list("/api/layouts")
	if len(layouts) != 22 || layouts[0].(map[string]any)["id"] != "builtin:duo" {
		t.Fatalf("layouts %v", layouts)
	}
	st, dep := anon.do("POST", "/api/workspaces/"+wid+"/deploy", map[string]any{"layoutId": "builtin:duo", "fill": map[string]any{"agent": "shell"},
		"laneOverrides": map[string]any{"a2": map[string]any{"agent": "fake-claude", "machineId": "local"}}}, bearer)
	if st != 201 || len(dep["sessions"].([]any)) != 2 || len(dep["nodeIds"].([]any)) != 3 {
		t.Fatalf("deploy %d %v", st, dep)
	}
	ag := []string{dep["sessions"].([]any)[0].(map[string]any)["agent"].(string), dep["sessions"].([]any)[1].(map[string]any)["agent"].(string)}
	if ag[0] != "shell" || ag[1] != "fake-claude" {
		t.Fatalf("lane agents %v", ag)
	}
	if st, b := anon.do("POST", "/api/workspaces/"+wid+"/deploy", map[string]any{"layoutId": "nope"}, bearer); st != 404 || b["code"] != "layout_not_found" {
		t.Fatalf("missing layout %d %v", st, b)
	}
	// Save a layout from the board, rename, delete; built-ins are read-only.
	st, ly := h.do("POST", "/api/layouts", map[string]any{"workspaceId": wid}, nil)
	if st != 201 || ly["name"] != "Set 1" || len(ly["lanes"].([]any)) != 6 {
		t.Fatalf("save layout %d %v", st, ly)
	}
	if st, b := h.do("DELETE", "/api/layouts/builtin:duo", nil, nil); st != 403 || b["code"] != "layout_builtin" {
		t.Fatalf("builtin %d %v", st, b)
	}
	if st, _ := h.do("PATCH", "/api/layouts/"+ly["id"].(string), map[string]any{"name": "Mine"}, nil); st != 200 {
		t.Fatal(st)
	}
	if st, _ := h.do("DELETE", "/api/layouts/"+ly["id"].(string), nil, nil); st != 200 {
		t.Fatal(st)
	}
	_ = pid
}

// TestLoopsMemoryConfirmAPI covers the loop, memory and confirmation
// endpoints.
func TestLoopsMemoryConfirmAPI(t *testing.T) {
	h := newTestHub(t)
	pid, wid, _ := orchSetup(t, h)
	in := h.spawnAgent(wid)
	id := in["id"].(string)
	h.waitInfo(id, "ready", state("ready"))
	// Loops.
	if st, b := h.do("POST", "/api/projects/"+pid+"/loops", map[string]any{"text": "x", "trigger": map[string]any{"kind": "interval", "everySec": 30}, "targets": map[string]any{"kind": "allAgents"}}, nil); st != 400 || b["code"] != "bad_loop" {
		t.Fatalf("bad loop %d %v", st, b)
	}
	st, l := h.do("POST", "/api/projects/"+pid+"/loops", map[string]any{"name": "Ping", "enabled": true, "text": "loop ping",
		"trigger": map[string]any{"kind": "interval", "everySec": 600}, "targets": map[string]any{"kind": "handles", "ids": []any{id}}}, nil)
	if st != 201 || l["state"].(map[string]any)["status"] != "running" || l["createdBy"] != "user" || l["workspaceId"] != wid {
		t.Fatalf("loop %d %v", st, l)
	}
	lid := l["id"].(string)
	if st, _ := h.do("POST", "/api/loops/"+lid+"/run-now", nil, nil); st != 200 {
		t.Fatal(st)
	}
	dir := os.Getenv("FAKE_DIR")
	waitTrue(t, "loop delivery", 10*time.Second, func() bool { return strings.Contains(received(dir), "loop ping\n") })
	waitTrue(t, "loop history", 10*time.Second, func() bool {
		_, hist := h.do("GET", "/api/loops/"+lid+"/history", nil, nil)
		items := hist["items"].([]any)
		return len(items) == 1 && items[0].(map[string]any)["kind"] == "run"
	})
	if tpl := h.list("/api/loops/templates"); len(tpl) != 7 {
		t.Fatalf("templates %d", len(tpl))
	}
	if st, p := h.do("PATCH", "/api/loops/"+lid, map[string]any{"name": "Ping 2"}, nil); st != 200 || p["name"] != "Ping 2" {
		t.Fatalf("patch %d %v", st, p)
	}
	if st, b := h.do("POST", "/api/loops/stop-all", nil, nil); st != 200 || b["stopped"].(float64) != 1 {
		t.Fatalf("stop-all %d %v", st, b)
	}
	if st, _ := h.do("DELETE", "/api/loops/"+lid, nil, nil); st != 200 {
		t.Fatal(st)
	}
	// Memory.
	st, mem := h.do("PUT", "/api/projects/"+pid+"/memory", map[string]any{"scope": "project", "content": "## Commands\n- make test\n"}, nil)
	if st != 200 || mem["mtime"] == "" {
		t.Fatalf("memory put %d %v", st, mem)
	}
	if st, b := h.do("PUT", "/api/projects/"+pid+"/memory", map[string]any{"scope": "project", "content": "x", "ifMtime": "2001-01-01T00:00:00Z"}, nil); st != 409 || b["code"] != "mtime_mismatch" {
		t.Fatalf("mtime %d %v", st, b)
	}
	st, acc := h.do("POST", "/api/projects/"+pid+"/memory/accept", map[string]any{"scope": "project", "facts": []any{map[string]any{"kind": "commands", "text": "make test"}, map[string]any{"kind": "gotchas", "text": "Go 1.26 only"}}}, nil)
	if st != 200 || strings.Count(acc["content"].(string), "make test") != 1 || !strings.Contains(acc["content"].(string), "## Gotchas") {
		t.Fatalf("accept %d %v", st, acc)
	}
	_, got := h.do("GET", "/api/projects/"+pid+"/memory", nil, nil)
	if got["project"].(map[string]any)["content"] != acc["content"] {
		t.Fatalf("get %v", got)
	}
	st, push := h.do("POST", "/api/projects/"+pid+"/memory/push", map[string]any{"targets": []any{id}, "scopes": []any{"project"}}, nil)
	if st != 200 || push["delivered"].(float64) != 1 {
		t.Fatalf("push %d %v", st, push)
	}
	waitTrue(t, "memory delivered", 10*time.Second, func() bool { return strings.Contains(received(dir), "Project context from NextBoard") })
	// Confirmations: the first decision wins, the second client gets 409.
	_, r := h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": "kill Arkan", "workspaceId": wid, "planner": "off"}, nil)
	cf := r["confirmation"].(map[string]any)
	if cf["kind"] != "destructive" {
		t.Fatalf("confirmation %v", cf)
	}
	if st, b := h.do("POST", "/api/confirmations/"+cf["id"].(string), map[string]any{"decision": "cancel"}, nil); st != 200 || b["state"] != "cancelled" {
		t.Fatalf("decide %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/confirmations/"+cf["id"].(string), map[string]any{"decision": "confirm"}, nil); st != 409 || b["code"] != "already_decided" {
		t.Fatalf("second decision %d %v", st, b)
	}
	if _, x := h.do("GET", "/api/sessions/"+id, nil, nil); x["status"] != "running" {
		t.Fatal("cancelled kill ran")
	}
	if st, _ := h.do("DELETE", "/api/threads/"+pid, nil, nil); st != 200 {
		t.Fatal(st)
	}
	if _, msgs := h.do("GET", "/api/threads/"+pid+"/messages", nil, nil); len(msgs["items"].([]any)) != 0 {
		t.Fatal("thread not cleared")
	}
}

// TestMemoryDocsAPI: sub-memories, the critical notes that stay, search and the
// view of all memories, through the API.
func TestMemoryDocsAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, pr := h.do("POST", "/api/projects", map[string]any{"name": "memapp", "machineId": "local", "path": t.TempDir()}, nil)
	if st != 200 && st != 201 {
		t.Fatalf("project %d %v", st, pr)
	}
	pid := pr["id"].(string)
	doc := func(name string) string { return "/api/memory/docs/" + name + "?projectId=" + pid }

	st, d := h.do("PUT", doc("design-system"), map[string]any{"title": "Design system", "description": "UI rules.", "content": "## Buttons\n- ghost on glass\nSee [[critical]].\n"}, nil)
	if st != 200 || d["kind"] != "sub" || d["title"] != "Design system" {
		t.Fatalf("put %d %v", st, d)
	}
	if st, b := h.do("PUT", doc("design-system"), map[string]any{"content": "x", "ifMtime": "2000-01-01T00:00:00Z"}, nil); st != 409 {
		t.Fatalf("stale write %d %v", st, b)
	}
	if st, b := h.do("PUT", doc("Bad%20Name"), map[string]any{"content": "x"}, nil); st != 400 || b["code"] != "bad_memory_name" {
		t.Fatalf("bad name %d %v", st, b)
	}
	if st, b := h.do("DELETE", doc("critical"), nil, nil); st != 409 || b["code"] != "critical_required" {
		t.Fatalf("critical deleted? %d %v", st, b)
	}
	if st, b := h.do("GET", doc("nope"), nil, nil); st != 404 || b["code"] != "memory_not_found" {
		t.Fatalf("missing %d %v", st, b)
	}
	if st, b := h.do("GET", "/api/memory/docs/main?projectId=p_nosuchproject", nil, nil); st != 404 {
		t.Fatalf("unknown project %d %v", st, b)
	}
	// The memory of all projects needs no project.
	if st, b := h.do("PUT", "/api/memory/docs/critical", map[string]any{"content": "- Never touch /srv/shared.\n"}, nil); st != 200 || b["critical"] != true {
		t.Fatalf("global critical %d %v", st, b)
	}
	if st, b := h.do("PUT", "/api/memory/docs/main", map[string]any{"content": "## Context\n- house rule\n"}, nil); st != 200 || b["kind"] != "main" {
		t.Fatalf("global main %d %v", st, b)
	}

	_, found := h.do("GET", "/api/memory/search?q=ghost&projectId="+pid, nil, nil)
	hits, _ := found["hits"].([]any)
	if len(hits) == 0 || hits[0].(map[string]any)["doc"] != "design-system" || hits[0].(map[string]any)["section"] != "Buttons" {
		t.Fatalf("search %v", found)
	}
	_, all := h.do("GET", "/api/memory/search?q=ghost", nil, nil)
	if hits, _ := all["hits"].([]any); len(hits) == 0 || hits[0].(map[string]any)["project"] != "memapp" {
		t.Fatalf("search everywhere %v", all)
	}

	_, g := h.do("GET", "/api/memory/galaxy", nil, nil)
	if !strings.Contains(fmt.Sprint(g), "Design system") || !strings.Contains(fmt.Sprint(g), "memapp") || !strings.Contains(fmt.Sprint(g), "Critical notes") {
		t.Fatalf("galaxy %v", g)
	}
	if strings.Contains(fmt.Sprint(g), "ghost on glass") {
		t.Fatal("the view must not carry content")
	}

	// What was accepted of an init run.
	st, g = h.do("POST", "/api/projects/"+pid+"/memory/init/apply", map[string]any{
		"facts":    []any{map[string]any{"kind": "commands", "text": "make test runs the tests"}},
		"subs":     []any{map[string]any{"slug": "testing", "title": "Testing", "description": "How.", "content": "## Setup\n- make test\n"}},
		"critical": []any{"Never run make wipe."}}, nil)
	if st != 200 || !strings.Contains(fmt.Sprint(g), "Testing") {
		t.Fatalf("apply %d %v", st, g)
	}
	if st, b := h.do("GET", doc("critical"), nil, nil); st != 200 || !strings.Contains(b["content"].(string), "make wipe") {
		t.Fatalf("critical after apply %d %v", st, b)
	}
	if st, b := h.do("DELETE", doc("testing"), nil, nil); st != 200 {
		t.Fatalf("delete %d %v", st, b)
	}
}

func TestOrderResendRoute(t *testing.T) {
	h := newTestHub(t)
	pid, wid, _ := orchSetup(t, h)
	in := h.spawnAgent(wid)
	h.waitInfo(in["id"].(string), "ready", state("ready"))
	if st, b := h.do("POST", "/api/orders/o_none/resend", nil, nil); st != http.StatusNotFound || b["code"] != "order_not_found" {
		t.Fatalf("missing order: %d %v", st, b)
	}
	st, r := h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": "@Arkan run the tests", "workspaceId": wid}, nil)
	if st != 202 {
		t.Fatalf("command %d %v", st, r)
	}
	// Nothing was interrupted, so there is nothing to send again.
	if st, b := h.do("POST", "/api/orders/"+r["orderId"].(string)+"/resend", nil, nil); st != http.StatusConflict || b["code"] != "nothing_to_resend" {
		t.Fatalf("not interrupted: %d %v", st, b)
	}
}
