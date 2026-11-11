package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/marker"
)

// fakeClaude behaves like Claude Code for the hub: it parses
// --session-id/--resume, fires hooks through NB_HOOK_SH exactly as the
// injected --settings hooks do, and writes a transcript.
const fakeClaude = `#!/bin/sh
sid=""; mode=fresh
while [ $# -gt 0 ]; do case "$1" in --session-id) sid="$2"; shift;; --resume) sid="$2"; mode=resume; shift;; --settings) set_ok=1;; esac; shift; done
[ -n "$set_ok" ] || echo "NO-SETTINGS"
tp="$FAKE_DIR/$sid.jsonl"
hook() { printf '%s' "$2" | sh -c "set -- hook $1; eval \"\$NB_HOOK_SH\""; }
echo "FAKE-START mode=$mode sid=$sid"
hook SessionStart "{\"session_id\":\"$sid\",\"transcript_path\":\"$tp\",\"hook_event_name\":\"SessionStart\"}"
printf '> '
while IFS= read -r line; do
  case "$line" in quit) exit 0;; fail) exit 3;; esac
  hook UserPromptSubmit '{"hook_event_name":"UserPromptSubmit"}'
  hook PreToolUse '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"npm test"}}'
  hook Notification '{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}'
  read -r ans
  hook PostToolUse '{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"npm test"}}'
  printf '{"type":"user","uuid":"u-%s","timestamp":"2026-10-28T10:00:00.000Z","sessionId":"%s","isSidechain":false,"message":{"role":"user","content":"%s"}}\n' "$line" "$sid" "$line" >> "$tp"
  printf '{"type":"assistant","uuid":"a-%s","timestamp":"2026-10-28T10:00:01.000Z","isSidechain":false,"message":{"id":"msg_%s","role":"assistant","content":[{"type":"text","text":"Done: %s"}],"usage":{"input_tokens":3,"output_tokens":5,"cache_read_input_tokens":7,"cache_creation_input_tokens":1}}}\n' "$line" "$line" "$line" >> "$tp"
  hook Stop '{"hook_event_name":"Stop"}'
  printf '> '
done
`

func setupFakeClaude(t *testing.T, h *testHub) (cwd string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakeclaude")
	if err := os.WriteFile(bin, []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DIR", dir)
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "fake-claude", "name": "Fake Claude", "command": bin, "base": "claude"}, nil); st != 201 || a["liveStatus"] != "claude-hooks" {
		t.Fatalf("agent %d %v", st, a)
	}
	return dir
}

// waitInfo polls GET /api/sessions/{id} until cond holds.
func (h *testHub) waitInfo(id string, what string, cond func(map[string]any) bool) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var in map[string]any
	for time.Now().Before(deadline) {
		_, in = h.do("GET", "/api/sessions/"+id, nil, nil)
		if cond(in) {
			return in
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("timeout waiting for %s; last %v", what, in)
	return nil
}

func state(want string) func(map[string]any) bool {
	return func(in map[string]any) bool { return in["agentState"] == want }
}

func TestAgentConsoleHooksEndToEnd(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := setupFakeClaude(t, h)
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "cwd": dir}, nil)
	if st != 201 || in["handle"] != "Arkan" || in["agent"] != "fake-claude" || in["agentKind"] != "agent" {
		t.Fatalf("create %d %v", st, in)
	}
	if _, ok := in["hookToken"]; ok {
		t.Fatal("hookToken leaked to the client")
	}
	id := in["id"].(string)
	in = h.waitInfo(id, "ready", state("ready"))
	uuid, _ := in["agentSessionId"].(string)
	if len(uuid) != 36 || in["liveStatus"] != "hooks" || in["stateSource"] != "hooks" || in["foreground"] != "agent" || in["status"] != "running" {
		t.Fatalf("ready %v", in)
	}
	// Terminal WS: status frames carry the agent fields.
	hdr := http.Header{"Origin": {h.ts.URL}}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	ws, _, err := h.dialWS("/api/sessions/"+id+"/ws", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	frames := make(chan map[string]any, 64)
	var raw syncBuffer
	go func() {
		for {
			typ, b, err := ws.ReadMessage()
			if err != nil {
				close(frames)
				return
			}
			if typ == websocket.BinaryMessage {
				raw.Write(b)
				continue
			}
			var f map[string]any
			_ = json.Unmarshal(b, &f)
			frames <- f
		}
	}()
	_ = ws.WriteJSON(map[string]any{"t": "i", "d": "hello\r"})
	in = h.waitInfo(id, "needsYou", state("needsYou"))
	if in["activity"] != "Permission: Bash(npm test)" || in["needsYouSince"] == nil {
		t.Fatalf("needsYou %v", in)
	}
	sawFrame := false
	for !sawFrame {
		select {
		case f := <-frames:
			sawFrame = f["agentState"] == "needsYou" && f["status"] == "running" && f["foreground"] == "agent"
		case <-time.After(5 * time.Second):
			t.Fatal("no needsYou status frame")
		}
	}
	_ = ws.WriteJSON(map[string]any{"t": "i", "d": "y\r"})
	in = h.waitInfo(id, "reply", func(m map[string]any) bool { return m["agentState"] == "ready" && m["reply"] != nil })
	if lt, _ := in["lastTurn"].(map[string]any); lt == nil || lt["endedAt"] == nil {
		t.Fatalf("lastTurn %v", in["lastTurn"])
	}
	if st, rep := h.do("GET", "/api/sessions/"+id+"/reply", nil, nil); st != 200 || rep["text"] != "Done: hello" || rep["source"] != "transcript" || rep["agentSessionId"] != uuid {
		t.Fatalf("reply %d %v", st, rep)
	}
	if st, tr := h.do("GET", "/api/sessions/"+id+"/transcript?last=5", nil, nil); st != 200 || len(tr["messages"].([]any)) != 2 {
		t.Fatalf("transcript %d %v", st, tr)
	}
	if st, tk := h.do("GET", "/api/sessions/"+id+"/tokens", nil, nil); st != 200 || len(tk["items"].([]any)) != 1 {
		t.Fatalf("tokens %d %v", st, tk)
	}
	// Attention: needsYou (finished is below finishedMinSec).
	_, at := h.do("GET", "/api/attention?since=0", nil, nil)
	items, _ := at["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["kind"] != "needsYou" || items[0].(map[string]any)["title"] != "Arkan needs you" || at["bootId"] == "" {
		t.Fatalf("attention %v", at)
	}
	// Agent exits → shell in place, process still running.
	_ = ws.WriteJSON(map[string]any{"t": "i", "d": "quit\r"})
	in = h.waitInfo(id, "shell", state("shell"))
	if in["status"] != "running" || in["foreground"] != "shell" || in["agentExitCode"] != float64(0) {
		t.Fatalf("shell %v", in)
	}
	// Markers never reach the ring or the WebSocket.
	sess, _ := h.srv.sessions.Get(id)
	snap, sub := sess.Subscribe()
	sess.Unsubscribe(sub)
	if bytes.Contains(snap, []byte("777;nb")) || strings.Contains(raw.String(), "777;nb") {
		t.Fatal("marker leaked into scrollback/WS")
	}
	if !bytes.Contains(snap, []byte("FAKE-START mode=fresh sid="+uuid)) {
		t.Fatalf("launch args: %q", snap)
	}

	// §3.3 resume from the idle shell keeps the conversation id.
	time.Sleep(2100 * time.Millisecond) // no client input in the last 2 s
	if st, body := h.do("POST", "/api/sessions/"+id+"/agent", map[string]any{"mode": "resume"}, nil); st != 200 {
		t.Fatalf("agent resume %d %v", st, body)
	}
	in = h.waitInfo(id, "ready after resume", state("ready"))
	if in["agentSessionId"] != uuid {
		t.Fatalf("resume changed id %v", in["agentSessionId"])
	}
	if st, body := h.do("POST", "/api/sessions/"+id+"/agent", map[string]any{"mode": "fresh"}, nil); st != 409 || body["code"] != "agent_running" {
		t.Fatalf("agent_running %d %v", st, body)
	}
	_ = ws.WriteJSON(map[string]any{"t": "i", "d": "quit\r"})
	h.waitInfo(id, "shell again", state("shell"))
	// A job in the foreground blocks §3.3. Wait for the shell to have started it: a fixed
	// pause was too short under a loaded test run.
	_ = ws.WriteJSON(map[string]any{"t": "i", "d": "sleep 30\r"})
	waitTrue(t, "sleep in the foreground", 15*time.Second, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		idle, comm, err := sess.Foreground(ctx)
		return err == nil && !idle && comm == "sleep"
	})
	time.Sleep(2200 * time.Millisecond) // no client input in the last 2 s
	if st, body := h.do("POST", "/api/sessions/"+id+"/agent", map[string]any{"mode": "fresh"}, nil); st != 409 || body["code"] != "shell_busy" {
		t.Fatalf("shell_busy %d %v", st, body)
	}
	_ = ws.WriteJSON(map[string]any{"t": "i", "d": "\x03"})
	time.Sleep(2200 * time.Millisecond)
	if st, body := h.do("POST", "/api/sessions/"+id+"/agent", map[string]any{"mode": "fresh"}, nil); st != 200 {
		t.Fatalf("agent fresh %d %v", st, body)
	}
	in = h.waitInfo(id, "ready fresh", state("ready"))
	if in["agentSessionId"] == uuid || len(in["agentSessionId"].(string)) != 36 {
		t.Fatalf("fresh must use a new UUID: %v", in["agentSessionId"])
	}
	// Exit agent, then the shell: the process ends.
	_ = ws.WriteJSON(map[string]any{"t": "i", "d": "fail\r"})
	in = h.waitInfo(id, "shell after fail", state("shell"))
	if in["agentExitCode"] != float64(3) {
		t.Fatalf("exit code %v", in["agentExitCode"])
	}
	_, at = h.do("GET", "/api/attention?since=1", nil, nil)
	found := false
	for _, it := range at["items"].([]any) {
		found = found || it.(map[string]any)["kind"] == "error"
	}
	if !found {
		t.Fatalf("no error attention for rc 3: %v", at)
	}
	_ = ws.WriteJSON(map[string]any{"t": "i", "d": "exit\r"})
	h.waitInfo(id, "exited", state("exited"))
	// Non-agent consoles cannot use §3.3.
	_, shIn := h.do("POST", "/api/sessions", map[string]any{"agent": "shell"}, nil)
	if shIn["handle"] != "Bintang" || shIn["agentKind"] != "shell" {
		t.Fatalf("shell console %v", shIn)
	}
	if st, body := h.do("POST", "/api/sessions/"+shIn["id"].(string)+"/agent", map[string]any{"mode": "fresh"}, nil); st != 409 || body["code"] != "not_agent" {
		t.Fatalf("not_agent %d %v", st, body)
	}
}

func TestHookHTTPFallbackNeedsToken(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := setupFakeClaude(t, h)
	_, in := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "cwd": dir}, nil)
	id := in["id"].(string)
	h.waitInfo(id, "ready", state("ready"))
	sess, _ := h.srv.sessions.Get(id)
	tok := sess.HookToken()
	post := func(k string) int {
		body := marker.Encode(marker.TypeHook, marker.Payload{K: k, Ev: "UserPromptSubmit", Raw: ""})
		body = bytes.TrimPrefix(bytes.TrimSuffix(body, []byte("\a")), []byte("\x1b]777;nb;"))
		resp, err := http.Post(h.srv.hookURL, "text/plain", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("forged"); code != 204 {
		t.Fatalf("forged %d", code)
	}
	time.Sleep(300 * time.Millisecond)
	if _, in := h.do("GET", "/api/sessions/"+id, nil, nil); in["agentState"] != "ready" {
		t.Fatalf("forged token changed state: %v", in["agentState"])
	}
	if code := post(tok); code != 204 {
		t.Fatalf("valid %d", code)
	}
	h.waitInfo(id, "working via HTTP", state("working"))
	if !strings.HasPrefix(h.srv.hookURL, "http://127.0.0.1:") {
		t.Fatalf("hook listener not loopback: %s", h.srv.hookURL)
	}
}

func TestHubRestartResumeModes(t *testing.T) {
	const hook, config = "hook", "config"
	for _, c := range []struct {
		setting, saved, wantMode string
		offer                    bool
	}{
		{"auto", hook, "resume", false},
		{"auto", config, "resume", false}, // the hook's path is gone, the file is in another project folder
		{"auto", "", "fresh", false},      // no message yet: Claude would answer "No conversation found"
		{"ask", hook, "shell", true},
		{"ask", "", "shell", false},
		{"never", hook, "shell", false},
	} {
		t.Run(c.setting+"/"+c.wantMode+"/"+c.saved, func(t *testing.T) {
			dir := hubDataDir(t)
			srv1, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(srv1.review.Close) // a snapshot still running would write the folder back
			bin := filepath.Join(dir, "fakeclaude")
			os.WriteFile(bin, []byte(fakeClaude), 0o755)
			fakeDir := t.TempDir()
			t.Setenv("FAKE_DIR", fakeDir)
			if _, err := srv1.agents.Create(map[string]json.RawMessage{"id": raw(`"fake-claude"`), "name": raw(`"F"`), "command": raw(`"` + bin + `"`), "base": raw(`"claude"`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := srv1.settings.Patch([]byte(`{"agents":{"resumeOnRestart":"` + c.setting + `"}}`)); err != nil {
				t.Fatal(err)
			}
			in, err := srv1.sessions.Create(sessionCreate("fake-claude"))
			if err != nil {
				t.Fatal(err)
			}
			waitScrollback(t, srv1, in.ID, "FAKE-START mode=fresh")
			deadline := time.Now().Add(10 * time.Second)
			var uuid string
			for time.Now().Before(deadline) {
				i, _ := srv1.sessions.Info(in.ID)
				if i.AgentState == "ready" {
					uuid = i.AgentSessionID
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if uuid == "" {
				t.Fatal("no conversation id")
			}
			switch c.saved {
			case hook:
				os.WriteFile(filepath.Join(fakeDir, uuid+".jsonl"), []byte("{}\n"), 0o644)
			case config:
				other := srv1.st.Path("agents", "claude", "projects", "elsewhere")
				os.MkdirAll(other, 0o755)
				os.WriteFile(filepath.Join(other, uuid+".jsonl"), []byte("{}\n"), 0o644)
			}
			srv1.sessions.Shutdown()
			srv1.boards.Close()

			srv2, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(srv2.review.Close) // a snapshot still running would write the folder back
			defer srv2.sessions.Shutdown()
			defer srv2.boards.Close()
			srv2.sessions.Start()
			switch c.wantMode {
			case "resume":
				waitScrollback(t, srv2, in.ID, "FAKE-START mode=resume sid="+uuid)
				return
			case "fresh":
				deadline = time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					i, _ := srv2.sessions.Info(in.ID)
					if i.AgentState == "ready" && i.AgentSessionID != uuid {
						b := waitScrollback(t, srv2, in.ID, "FAKE-START mode=fresh sid="+i.AgentSessionID)
						if bytes.Contains(b, []byte("mode=resume")) {
							t.Fatalf("resumed a conversation that was never saved: %q", b)
						}
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
				t.Fatal("no fresh launch")
			}
			deadline = time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				i, _ := srv2.sessions.Info(in.ID)
				if i.AgentState == "shell" && i.Status == "running" {
					if i.ResumeOffer != c.offer {
						t.Fatalf("resumeOffer %v", i.ResumeOffer)
					}
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			i, _ := srv2.sessions.Info(in.ID)
			t.Fatalf("not in shell: %+v", i)
		})
	}
}

// TestHubStopKeepsAgentForeground: a hub started under nohup hands its wrappers
// an ignored SIGHUP, so one lives to report the exit of a CLI that the stop ended.
// That is not the user leaving the agent: it has to come back after the restart.
func TestHubStopKeepsAgentForeground(t *testing.T) {
	signal.Ignore(syscall.SIGHUP)
	t.Cleanup(func() { signal.Reset(syscall.SIGHUP) })
	dir := hubDataDir(t)
	srv1, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv1.review.Close) // a snapshot still running would write the folder back
	quit := filepath.Join(dir, "quit")
	t.Setenv("FAKE_QUIT", quit)
	bin := filepath.Join(dir, "fakeclaude")
	script := "#!/bin/sh\necho \"ARGS $*\"\nwhile [ ! -e \"$FAKE_QUIT\" ]; do sleep 0.05; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := srv1.agents.Create(map[string]json.RawMessage{"id": raw(`"fake-claude"`), "name": raw(`"F"`), "command": raw(`"` + bin + `"`), "base": raw(`"claude"`)}); err != nil {
		t.Fatal(err)
	}
	in, err := srv1.sessions.Create(sessionCreate("fake-claude"))
	if err != nil {
		t.Fatal(err)
	}
	waitScrollback(t, srv1, in.ID, "ARGS ")
	i, _ := srv1.sessions.Info(in.ID)
	saved := srv1.st.Path("agents", "claude", "projects", "elsewhere")
	os.MkdirAll(saved, 0o755)
	os.WriteFile(filepath.Join(saved, i.AgentSessionID+".jsonl"), []byte("{}\n"), 0o644)
	stopped := make(chan struct{})
	go func() {
		srv1.sessions.Shutdown()
		close(stopped)
	}()
	time.Sleep(200 * time.Millisecond) // the stop has begun, the wrapper ignored its SIGHUP
	os.WriteFile(quit, nil, 0o644)
	<-stopped
	srv1.boards.Close()
	os.Remove(quit)

	srv2, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv2.review.Close) // a snapshot still running would write the folder back
	defer srv2.sessions.Shutdown()
	defer srv2.boards.Close()
	srv2.sessions.Start()
	waitScrollback(t, srv2, in.ID, "--resume "+i.AgentSessionID)
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) {
	s.mu.Lock()
	s.b.Write(p)
	s.mu.Unlock()
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestScreenFallbackNeedsYou: without hooks, the claude screen rules turn a
// confirmation dialog into needsYou and its disappearance back into work.
func TestScreenFallbackNeedsYou(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	bin := filepath.Join(dir, "dialog")
	os.WriteFile(bin, []byte("#!/bin/sh\nprintf ' Quick safety check: Is this a project you created or one you trust?\\r\\n\\r\\n ❯ No, exit\\r\\n   Yes, I trust this folder\\r\\n\\r\\n Enter to confirm · Esc to cancel'\nread x\nprintf '\\033[2J\\033[H✻ Working… (3s · esc to interrupt)'\nsleep 30\n"), 0o755)
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "dlg", "name": "Dlg", "command": bin, "base": "claude"}, nil); st != 201 {
		t.Fatalf("%d %v", st, a)
	}
	_, in := h.do("POST", "/api/sessions", map[string]any{"agent": "dlg", "cwd": dir}, nil)
	id := in["id"].(string)
	in = h.waitInfo(id, "needsYou from screen", state("needsYou"))
	if in["stateSource"] != "screen" || in["activity"] != "Trust folder?" {
		t.Fatalf("%v", in)
	}
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "\r"}, nil)
	h.waitInfo(id, "working after dialog", state("working"))
}

// TestSilentThinkingThatReadsStaysWorking: an agent without screen rules whose
// prompt stays on the screen while it reads a stream (a model thinking) stays
// working past the 3 s of quiet, and turns ready once the stream ends.
func TestSilentThinkingThatReadsStaysWorking(t *testing.T) {
	if _, err := os.Stat("/proc/self/io"); err != nil {
		t.Skip("no /proc")
	}
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	bin := filepath.Join(dir, "thinker")
	os.WriteFile(bin, []byte("#!/bin/sh\nprintf '> '; while :; do read x; printf '> '; timeout 7 dd if=/dev/zero of=/dev/null bs=4k 2>/dev/null; done\n"), 0o755)
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "thinker", "name": "Thinker", "command": bin}, nil); st != 201 {
		t.Fatalf("%d %v", st, a)
	}
	_, in := h.do("POST", "/api/sessions", map[string]any{"agent": "thinker", "cwd": dir}, nil)
	id := in["id"].(string)
	h.waitInfo(id, "ready at the prompt", state("ready"))
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "go\r"}, nil)
	h.waitInfo(id, "working", state("working"))
	time.Sleep(5 * time.Second)
	if _, in := h.do("GET", "/api/sessions/"+id, nil, nil); in["agentState"] != "working" {
		t.Fatalf("5 s into the stream: %v (%v)", in["agentState"], in["stateSource"])
	}
	h.waitInfo(id, "ready after the stream", state("ready"))
}

// TestRestartToConversationAndAttachedHandle covers restart with
// agentSessionId (one-call switch) and attachedHandle in the picker list.
func TestRestartToConversationAndAttachedHandle(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := setupFakeClaude(t, h)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	_, in := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "cwd": dir}, nil)
	id := in["id"].(string)
	h.waitInfo(id, "ready", state("ready"))
	conv := "11111111-2222-3333-4444-555555555555"
	if st, b := h.do("POST", "/api/sessions/"+id+"/restart", map[string]any{"agentSessionId": "../x"}, nil); st != 400 {
		t.Fatalf("bad id %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/sessions/"+id+"/restart", map[string]any{"agentSessionId": conv}, nil); st != 200 {
		t.Fatalf("restart %d %v", st, b)
	}
	waitScrollback(t, h.srv, id, "FAKE-START mode=resume sid="+conv)
	in = h.waitInfo(id, "ready on the chosen conversation", func(m map[string]any) bool {
		return m["agentState"] == "ready" && m["agentSessionId"] == conv
	})
	// The picker shows which console holds the conversation.
	slugDir := filepath.Join(cfg, "projects", strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, dir))
	os.MkdirAll(slugDir, 0o755)
	os.WriteFile(filepath.Join(slugDir, conv+".jsonl"), []byte(`{"type":"user","uuid":"u1","timestamp":"2026-10-28T10:00:00.000Z","sessionId":"`+conv+`","isSidechain":false,"message":{"role":"user","content":"Fix the login redirect"}}`+"\n"), 0o644)
	st, list := h.do("GET", "/api/machines/local/conversations?agent=claude&cwd="+dir, nil, nil)
	items, _ := list["items"].([]any)
	if st != 200 || len(items) != 1 {
		t.Fatalf("conversations %d %v", st, list)
	}
	it := items[0].(map[string]any)
	if it["attachedTo"] != id || it["attachedHandle"] != in["handle"] || it["title"] != "Fix the login redirect" {
		t.Fatalf("item %v", it)
	}
	if st, b := h.do("GET", "/api/machines/local/conversations?agent=gemini&cwd="+dir, nil, nil); st != 400 || b["code"] != "no_transcript" {
		t.Fatalf("no transcript %d %v", st, b)
	}
}

// A Claude console gets the editor link in its environment; other consoles do not.
func TestClaudeConsoleGetsTheEditorLink(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := t.TempDir()
	script := "#!/bin/sh\nenv > \"" + out + "/$NB_FAKE_NAME.env\"\necho FAKE-START\nsleep 60\n"
	bin := filepath.Join(out, "fakebin")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	add := func(id, base string) {
		t.Helper()
		if _, err := h.srv.agents.Create(map[string]json.RawMessage{"id": raw(`"` + id + `"`), "name": raw(`"` + id + `"`), "command": raw(`"` + bin + `"`), "base": raw(`"` + base + `"`), "env": raw(`["NB_FAKE_NAME=` + id + `"]`)}); err != nil {
			t.Fatal(err)
		}
	}
	add("fake-claude", "claude")
	add("fake-codex", "codex")
	dir := t.TempDir()
	st, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "w"}, nil)
	if st != 201 {
		t.Fatalf("project: %d %v", st, pr)
	}
	wid := pr["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	start := func(agentID string, workspace bool) string {
		t.Helper()
		body := map[string]any{"agent": agentID, "cwd": dir, "machineId": "local"}
		if workspace {
			body["workspaceId"] = wid
		}
		st, res := h.do("POST", "/api/sessions", body, nil)
		if st != 201 {
			t.Fatalf("create %s: %d %v", agentID, st, res)
		}
		return res["id"].(string)
	}
	envOf := func(name string) string {
		t.Helper()
		file := filepath.Join(out, name+".env")
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			if b, err := os.ReadFile(file); err == nil && len(b) > 0 {
				_ = os.Remove(file)
				return string(b)
			}
		}
		t.Fatalf("%s never ran", name)
		return ""
	}
	id := start("fake-claude", true)
	env := envOf("fake-claude")
	if !strings.Contains(env, "ENABLE_IDE_INTEGRATION=true") || !strings.Contains(env, "CLAUDE_CODE_IDE_SKIP_AUTO_INSTALL=1") || !strings.Contains(env, "CLAUDE_CODE_SSE_PORT=") {
		t.Fatalf("no link in:\n%s", env)
	}
	locks := func() []string {
		m, _ := filepath.Glob(filepath.Join(h.srv.st.Path("agents"), "*", "ide", "*.lock"))
		h2, _ := filepath.Glob(filepath.Join(home, ".claude", "ide", "*.lock"))
		return append(m, h2...)
	}
	if got := locks(); len(got) != 1 {
		t.Fatalf("locks: %v", got)
	}
	// the other agent stays out of it
	start("fake-codex", true)
	if env := envOf("fake-codex"); strings.Contains(env, "CLAUDE_CODE_SSE_PORT") {
		t.Fatal("a Codex console got the link")
	}
	if st, _ := h.do("DELETE", "/api/sessions/"+id, nil, nil); st != 200 {
		t.Fatalf("delete: %d", st)
	}
	if got := locks(); len(got) != 0 {
		t.Fatalf("lock stayed after the console: %v", got)
	}
	// turned off in the settings
	if st, res := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"ideLink": false}}, nil); st != 200 {
		t.Fatalf("settings: %d %v", st, res)
	}
	start("fake-claude", true)
	if env := envOf("fake-claude"); strings.Contains(env, "CLAUDE_CODE_SSE_PORT") {
		t.Fatal("the setting did not turn it off")
	}
}

func TestEditorSocketOfTheLink(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "w"}, nil)
	if st != 201 {
		t.Fatalf("project: %d %v", st, pr)
	}
	wid := pr["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	hdr := http.Header{}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.String())
	}
	if _, res, err := h.dialWS("/api/workspaces/nope/ide/ws", hdr); err == nil || res == nil || res.StatusCode != 404 {
		t.Fatalf("unknown workspace: %v", err)
	}
	evil := hdr.Clone()
	evil.Set("Origin", "http://evil.example")
	if _, res, err := h.dialWS("/api/workspaces/"+wid+"/ide/ws", evil); err == nil || res == nil || res.StatusCode != 403 {
		t.Fatalf("foreign origin: %v", err)
	}
	if _, res, err := h.dialWS("/api/workspaces/"+wid+"/ide/ws", nil); err == nil || res == nil || res.StatusCode != 401 {
		t.Fatalf("no login: %v", err)
	}
	c, _, err := h.dialWS("/api/workspaces/"+wid+"/ide/ws", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var m map[string]any
	if err := c.ReadJSON(&m); err != nil || m["t"] != "consoles" {
		t.Fatalf("%v %v", m, err)
	}
}

// Sessions with no tile are the editor's: they end with their board, not after it.
func TestEditorSessionsGoWithTheirBoard(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "w"}, nil)
	if st != 201 {
		t.Fatalf("project: %d %v", st, pr)
	}
	first := pr["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	st, ws := h.do("POST", "/api/projects/"+pr["id"].(string)+"/workspaces", map[string]any{"name": "second"}, nil)
	if st != 201 {
		t.Fatalf("workspace: %d %v", st, ws)
	}
	second := ws["id"].(string)
	create := func(wid string) string {
		t.Helper()
		st, res := h.do("POST", "/api/sessions", map[string]any{"workspaceId": wid, "preset": "shell", "panel": true}, nil)
		if st != 201 {
			t.Fatalf("create: %d %v", st, res)
		}
		return res["id"].(string)
	}
	stays, goes := create(first), create(second)
	if st, res := h.do("DELETE", "/api/workspaces/"+second, nil, nil); st != 200 {
		t.Fatalf("delete: %d %v", st, res)
	}
	if st, _ := h.do("GET", "/api/sessions/"+goes, nil, nil); st != 404 {
		t.Fatalf("the session outlived its board: %d", st)
	}
	if st, _ := h.do("GET", "/api/sessions/"+stays, nil, nil); st != 200 {
		t.Fatalf("the other board's session went too: %d", st)
	}
	// and still no tile for either
	st, b := h.do("GET", "/api/workspaces/"+first+"/board", nil, nil)
	if st != 200 || strings.Contains(fmt.Sprint(b), stays) {
		t.Fatalf("%d %v", st, b)
	}
}
