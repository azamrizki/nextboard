package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/marker"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/transcript"
)

// TestRealCLI drives the installed Claude Code / Codex through the hub to
// verify the open items of docs/API-agents.md §19. It spends the owner's
// plan, so it never runs in a normal `go test`:
//
//	NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_CLI=claude|codex \
//	NEXTBOARD_REAL_CLI_PROMPTS=0|1|2|perm NEXTBOARD_SCREEN_DUMP=/scratch/dir \
//	[NEXTBOARD_REAL_CLI_DIR=/scratch] [NEXTBOARD_REAL_CLI_CWD=/trusted/dir] \
//	[NEXTBOARD_REAL_CLI_CHANNEL=tty|auto|http] [NEXTBOARD_REAL_CLI_EXTRA="--debug-file x"] \
//	go test -run TestRealCLI -v ./internal/server/
//
// PROMPTS=0 only starts, exits and resumes (no model turn); "perm" asks
// for one file write and denies it. Prompts are tiny; nothing runs with
// yolo. Answering CLI dialogs by keystroke can trigger them (Codex's
// update dialog defaults to "Update now"): watch the screen dumps.
func TestRealCLI(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_CLI_TEST") != "1" {
		t.Skip("set NEXTBOARD_REAL_CLI_TEST=1 (memakai plan CLI pemilik)")
	}
	cli := os.Getenv("NEXTBOARD_REAL_CLI")
	prompts := os.Getenv("NEXTBOARD_REAL_CLI_PROMPTS")
	dump := os.Getenv("NEXTBOARD_SCREEN_DUMP")
	for _, k := range []string{"CLAUDE_EFFORT", "CLAUDE_PID", "CLAUDE_AGENT_SDK_VERSION"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	home, _ := os.UserHomeDir()
	sum := func(p string) string {
		b, _ := os.ReadFile(p)
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:8])
	}
	cfgFiles := []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "config.toml")}
	before := map[string]string{}
	for _, f := range cfgFiles {
		before[f] = sum(f)
	}

	h := newTestHub(t)
	h.setup()
	channel := os.Getenv("NEXTBOARD_REAL_CLI_CHANNEL")
	if channel == "" {
		channel = "tty"
	}
	if _, err := h.srv.settings.Patch([]byte(`{"agents":{"hookChannel":"` + channel + `"}}`)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var log []string
	h.srv.sessions.OnMarker = func(id string, mk marker.Marker) {
		mu.Lock()
		defer mu.Unlock()
		rb := mk.RawBytes()
		raw := ""
		for _, k := range []string{"hook_event_name", "source", "notification_type", "message", "tool_name", "command", "file_path", "type", "thread-id", "turn-id", "last-assistant-message", "cwd"} {
			if v := marker.ScanString(rb, k); v != "" {
				raw += fmt.Sprintf(" %s=%q", k, v)
			}
		}
		if mk.Type == marker.TypeNotify {
			raw += " json=" + string(rb)
		}
		log = append(log, fmt.Sprintf("%s %s ev=%s mode=%s sid=%s pid=%d rc=%v raw=%s", time.Now().Format("15:04:05.000"), mk.Type, mk.Payload.Ev, mk.Payload.Mode, mk.Payload.Sid, mk.Payload.Pid, deref(mk.Payload.Rc), raw))
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range log {
			t.Log("MARKER " + l)
		}
		for _, f := range cfgFiles {
			if a := sum(f); a != before[f] {
				t.Errorf("%s changed during the run", f)
			} else {
				t.Logf("unchanged: %s (%s)", f, a)
			}
		}
	})
	base := os.Getenv("NEXTBOARD_REAL_CLI_DIR") // scratch root; default the OS temp dir
	if base == "" {
		base = filepath.Join(os.TempDir(), "nb-realcli")
	}
	scratch := filepath.Join(base, cli+"-"+time.Now().Format("150405"))
	if d := os.Getenv("NEXTBOARD_REAL_CLI_CWD"); d != "" {
		scratch = d // reuse an already trusted folder
	}
	os.MkdirAll(scratch, 0o755)
	var args []string
	switch cli {
	case "claude":
		args = []string{"--model", "haiku", "--permission-mode", "manual"}
	case "codex":
		// Trust the scratch folder for this launch only (no config.toml write).
		args = []string{"-s", "read-only", "-a", "on-request", "-c", `projects."` + scratch + `".trust_level="trusted"`}
	default:
		t.Fatalf("NEXTBOARD_REAL_CLI=claude|codex")
	}
	if x := os.Getenv("NEXTBOARD_REAL_CLI_EXTRA"); x != "" {
		args = append(args, strings.Fields(x)...)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "real-" + cli, "name": "Real " + cli, "command": cli, "args": args, "base": cli}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "real-" + cli, "cwd": scratch, "cols": 110, "rows": 34}, nil)
	if st != 201 {
		t.Fatalf("create %d %v", st, in)
	}
	id := in["id"].(string)
	sess, _ := h.srv.sessions.Get(id)
	n := 0
	shot := func(name string) string {
		sn := sess.Screen()
		txt := sn.Text()
		n++
		if dump != "" {
			os.MkdirAll(dump, 0o755)
			os.WriteFile(filepath.Join(dump, fmt.Sprintf("%s-%02d-%s.txt", cli, n, name)), []byte(fmt.Sprintf("cursor=%d,%d alt=%v\n%s\n", sn.CursorX, sn.CursorY, sn.Alt, txt)), 0o644)
		}
		return txt
	}
	info := func() session.Info { i, _ := h.srv.sessions.Info(id); return i }
	waitFor := func(what string, d time.Duration, cond func(session.Info, string) bool) bool {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if cond(info(), sess.Screen().Text()) {
				return true
			}
			time.Sleep(200 * time.Millisecond)
		}
		i := info()
		t.Logf("TIMEOUT %s: state=%s src=%s act=%q fg=%s", what, i.AgentState, i.StateSource, i.Activity, i.Foreground)
		shot("timeout-" + strings.ReplaceAll(what, " ", "_"))
		return false
	}
	send := func(s string) {
		if err := sess.Input([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	type stamp struct {
		at    time.Duration
		state string
	}
	var states []stamp
	start := time.Now()
	stop := make(chan struct{})
	go func() {
		last := ""
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
			i := info()
			k := i.AgentState + "/" + i.StateSource + " " + i.Activity
			if time.Since(i.LastOutputAt) < 150*time.Millisecond {
				k += " [output]"
			}
			if k != last {
				last = k
				mu.Lock()
				states = append(states, stamp{time.Since(start).Round(100 * time.Millisecond), k})
				mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		mu.Lock()
		defer mu.Unlock()
		for _, s := range states {
			t.Logf("STATE +%v %s", s.at, s.state)
		}
	})
	trustRe := regexp.MustCompile(`(?i)trust`)
	answerTrust := func() {
		if waitFor("startup screen", 25*time.Second, func(i session.Info, s string) bool {
			return trustRe.MatchString(s) || i.AgentState == "ready"
		}) && trustRe.MatchString(sess.Screen().Text()) && info().AgentState != "ready" {
			time.Sleep(800 * time.Millisecond)
			shot("trust-dialog")
			t.Logf("trust dialog: state=%s src=%s act=%q", info().AgentState, info().StateSource, info().Activity)
			if strings.Contains(sess.Screen().Text(), "❯ No, exit") { // 2.1.312 defaults to "No"
				send("\x1b[B")
				time.Sleep(400 * time.Millisecond)
				shot("trust-yes-selected")
			}
			send("\r")
		}
	}
	answerTrust()
	if !waitFor("ready", 40*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "ready" }) {
		t.Fatal("agent never became ready")
	}
	time.Sleep(1500 * time.Millisecond)
	shot("ready")
	i := info()
	t.Logf("ready: src=%s live=%s agentSessionId=%s", i.StateSource, i.LiveStatus, i.AgentSessionID)

	if prompts == "perm" {
		send("Create a file named nb.txt containing the word hi.")
		time.Sleep(300 * time.Millisecond)
		send("\r")
		if waitFor("needsYou", 90*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "needsYou" }) {
			time.Sleep(1200 * time.Millisecond)
			shot("needsYou")
			i := info()
			t.Logf("needsYou: src=%s activity=%q", i.StateSource, i.Activity)
			send("\x1b") // deny
		}
		time.Sleep(8 * time.Second)
		shot("after-deny")
		i := info()
		t.Logf("after deny: state=%s src=%s activity=%q", i.AgentState, i.StateSource, i.Activity)
		waitFor("ready after deny", 60*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "ready" })
	}
	if prompts >= "1" && prompts != "perm" {
		send("Reply with the single word OK.")
		time.Sleep(300 * time.Millisecond)
		send("\r")
		waitFor("working", 20*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "working" })
		time.Sleep(700 * time.Millisecond)
		shot("working")
		waitFor("ready after turn", 120*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "ready" && i.Reply != nil })
		time.Sleep(1500 * time.Millisecond)
		shot("ready-after-turn")
		if rep, ok := sess.Reply(); ok {
			t.Logf("reply: source=%s text=%q chars=%d", rep.Source, rep.Text, rep.Chars)
		}
		i = info()
		_, _, tp, _ := sess.TranscriptLocation(t.Context())
		t.Logf("after turn: agentSessionId=%s transcript=%s lastTurn=%+v", i.AgentSessionID, tp, i.LastTurn)
		if cli == "claude" {
			want := transcript.ClaudePath(filepath.Join(home, ".claude"), scratch, i.AgentSessionID)
			t.Logf("computed slug path %s (match=%v)", want, want == tp)
		}
	}
	if prompts >= "2" && prompts != "perm" {
		q := "Run the shell command: echo nb-check"
		if cli == "codex" {
			q = "Create a file named nb.txt containing the word hi."
		}
		send(q)
		time.Sleep(300 * time.Millisecond)
		send("\r")
		if waitFor("needsYou", 90*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "needsYou" }) {
			time.Sleep(1200 * time.Millisecond)
			shot("needsYou")
			i = info()
			t.Logf("needsYou: src=%s activity=%q", i.StateSource, i.Activity)
			if cli == "claude" {
				send("1") // approve the harmless echo
			} else {
				send("\x1b") // decline the write
			}
		}
		waitFor("ready after second turn", 120*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "ready" })
		time.Sleep(1500 * time.Millisecond)
		shot("ready-after-second")
		if cli == "claude" {
			// The idle notification arrives ~60 s after the turn (T8 payload).
			waitFor("idle notification", 75*time.Second, func(session.Info, string) bool {
				mu.Lock()
				defer mu.Unlock()
				for _, l := range log {
					if strings.Contains(l, "ev=Notification") && strings.Contains(l, "idle") {
						return true
					}
				}
				return false
			})
			i = info()
			t.Logf("after idle notification: state=%s", i.AgentState)
		}
	}
	// Exit the agent → shell in place.
	exitCmd := "/exit"
	if cli == "codex" {
		exitCmd = "/quit"
	}
	send(exitCmd)
	time.Sleep(400 * time.Millisecond)
	send("\r")
	if !waitFor("shell", 20*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "shell" }) {
		send("\x03")
		time.Sleep(300 * time.Millisecond)
		send("\x03")
		waitFor("shell after ^C", 10*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "shell" })
	}
	shot("shell")
	i = info()
	t.Logf("shell: status=%s exit=%v fg=%s", i.Status, deref(i.AgentExitCode), i.Foreground)
	prev := i.AgentSessionID
	// Resume the exact conversation from the shell (§3.3).
	time.Sleep(2200 * time.Millisecond)
	st, body := h.do("POST", "/api/sessions/"+id+"/agent", map[string]any{"mode": "resume"}, nil)
	t.Logf("POST agent resume: %d %v", st, body["code"])
	answerTrust()
	waitFor("ready after resume", 40*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "ready" })
	time.Sleep(2 * time.Second)
	shot("resumed")
	i = info()
	t.Logf("resumed: agentSessionId=%s (before %s, same=%v)", i.AgentSessionID, prev, i.AgentSessionID == prev)
	send(exitCmd)
	time.Sleep(400 * time.Millisecond)
	send("\r")
	waitFor("shell after resume", 20*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "shell" })
	send("exit\r")
	waitFor("exited", 10*time.Second, func(i session.Info, _ string) bool { return i.AgentState == "exited" })
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// TestRealMemoryTile sends a memory tile's stack through a real Claude Code, on the
// cheapest model. It spends the owner's plan: three short turns.
//
//	NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_MEMORY=1 NEXTBOARD_REAL_CLI_CWD=/already/trusted \
//	[NEXTBOARD_SCREEN_DUMP=/scratch/dir] go test -run TestRealMemoryTile -v ./internal/server/
//
// The folder must be one Claude Code already trusts: this test never answers a dialog.
func TestRealMemoryTile(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_CLI_TEST") != "1" || os.Getenv("NEXTBOARD_REAL_MEMORY") != "1" {
		t.Skip("set NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_MEMORY=1 (memakai plan CLI pemilik)")
	}
	cwd := os.Getenv("NEXTBOARD_REAL_CLI_CWD")
	if cwd == "" {
		t.Fatal("NEXTBOARD_REAL_CLI_CWD: a folder Claude Code already trusts")
	}
	for _, k := range []string{"CLAUDE_EFFORT", "CLAUDE_PID", "CLAUDE_AGENT_SDK_VERSION"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	home, _ := os.UserHomeDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	sum := func() string {
		b, _ := os.ReadFile(settings)
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:8])
	}
	before := sum()
	t.Cleanup(func() {
		if sum() != before {
			t.Errorf("%s changed during the run", settings)
		}
	})

	h := newTestHub(t)
	h.setup()
	must := func(want int, method, path string, body any) map[string]any {
		t.Helper()
		st, out := h.do(method, path, body, nil)
		if st != want {
			t.Fatalf("%s %s: %d %v", method, path, st, out)
		}
		return out
	}
	// the CLI's own login, not a folder the hub made
	must(200, "PATCH", "/api/settings", map[string]any{"agents": map[string]any{"profiles": map[string]any{"local": map[string]any{"claude": nil}}}})
	must(201, "POST", "/api/agents", map[string]any{"id": "real-claude", "name": "Real Claude", "command": "claude", "base": "claude",
		"args": []any{"--model", "haiku", "--permission-mode", "manual"}})
	p := must(201, "POST", "/api/projects", map[string]any{"machineId": "local", "path": cwd, "name": "Portal"})
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	other := h.otherProject("Acme")
	must(200, "PUT", "/api/projects/"+pid+"/memory", map[string]any{"scope": "project", "content": "## Context\n- The project codeword is zebra-1842.\n"})
	for name, content := range map[string]string{
		"design": "## Tokens\n- The design codeword is walrus-5521.\n",
		"deploy": "## Steps\n- The deploy codeword is heron-7733.\n",
		"secret": "- The secret codeword is otter-9911.\n",
	} {
		title := map[string]string{"design": "Design system", "deploy": "Deployment", "secret": "Secret"}[name]
		must(200, "PUT", "/api/memory/docs/"+name+"?projectId="+other, map[string]any{"title": title, "content": content})
	}
	in := must(201, "POST", "/api/sessions", map[string]any{"agent": "real-claude", "workspaceId": wid, "cwd": cwd, "cols": 110, "rows": 34})
	id := in["id"].(string)
	t.Cleanup(func() { h.do("DELETE", "/api/sessions/"+id, nil, nil) })
	sess, _ := h.srv.sessions.Get(id)
	info := func() session.Info { i, _ := h.srv.sessions.Info(id); return i }
	dump := func(name string) {
		if d := os.Getenv("NEXTBOARD_SCREEN_DUMP"); d != "" {
			os.MkdirAll(d, 0o755)
			os.WriteFile(filepath.Join(d, "memtile-"+name+".txt"), []byte(sess.Screen().Text()), 0o600)
		}
	}
	// Anything but a plain prompt is a dialog, and typing into a dialog can answer it.
	ready := func(what string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if info().AgentState == "ready" {
				time.Sleep(1500 * time.Millisecond)
				if info().AgentState == "ready" {
					screen := strings.ToLower(sess.Screen().Text())
					for _, dialog := range []string{"trust", "install", "plugin", "do you want"} {
						if strings.Contains(screen, dialog) {
							dump("dialog")
							t.Fatalf("%s: a dialog on screen (%q), nothing typed:\n%s", what, dialog, screen)
						}
					}
					return
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		dump("timeout")
		t.Fatalf("%s: not ready: %s %q\n%s", what, info().AgentState, info().Activity, sess.Screen().Text())
	}
	ready("first launch")
	must(200, "POST", "/api/workspaces/"+wid+"/board/ops", map[string]any{"clientId": "c_tab1", "baseRev": 0, "ops": []any{
		memTileOp(map[string]any{"blocks": []any{map[string]any{"projectId": other, "name": "design"}, map[string]any{"projectId": other, "name": "deploy", "lookup": true}}}),
		wireEdge("e_wire000001", testMemTile, h.terminalNode(wid, id)),
	}})
	must(200, "POST", "/api/sessions/"+id+"/restart", nil)
	ready("launch with the tile")
	if c := must(200, "POST", "/api/projects/"+pid+"/memory/plan", map[string]any{"memoryNodeId": testMemTile})["targets"].([]any)[0].(map[string]any)["chars"].(float64); c != 0 {
		t.Fatalf("the launch should leave nothing to type: %v", c)
	}

	// turn waits for what a turn answered: an order typed by the hub, or a memory push.
	turn := func(name string, start func()) string {
		t.Helper()
		ready(name)
		began := time.Now()
		start()
		// The same words can come twice ("OK"), so it goes by when the reply was written.
		deadline := time.Now().Add(150 * time.Second)
		for time.Now().Before(deadline) {
			if rep, ok := sess.Reply(); ok && info().AgentState == "ready" && rep.At.After(began) {
				t.Logf("%s: reply %q", name, rep.Text)
				dump(name)
				return rep.Text
			}
			time.Sleep(300 * time.Millisecond)
		}
		dump("timeout-" + name)
		if d := os.Getenv("NEXTBOARD_SCREEN_DUMP"); d != "" {
			raw, sub := sess.Subscribe()
			sess.Unsubscribe(sub)
			os.WriteFile(filepath.Join(d, "memtile-raw.bin"), raw, 0o600)
		}
		_, ds := h.do("GET", "/api/sessions/"+id+"/deliveries", nil, nil)
		t.Fatalf("%s: no reply: %s %q\n%v\n%s", name, info().AgentState, info().Activity, ds, sess.Screen().Text())
		return ""
	}
	ask := func(name, text string) string {
		t.Helper()
		return turn(name, func() {
			must(202, "POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": text, "source": "api"})
		})
	}

	// In the system prompt and the memory folder: no tool, no extra turn.
	got := ask("launch", "Without using any tool, answer on one line with only two words: the project codeword from your memory, then the design codeword from the memory of project Acme.")
	if !strings.Contains(got, "zebra-1842") || !strings.Contains(got, "walrus-5521") || strings.Contains(got, "heron") {
		t.Errorf("launch: %q", got)
	}
	// Left out, with a name it can follow by itself.
	got = ask("lookup", "Your instructions list a page called Deployment from project Acme, left out to be read by name. Read it with the memory_read tool of the nextboard MCP server, then answer with only the codeword in it.")
	if !strings.Contains(got, "heron-7733") {
		t.Errorf("lookup: %q", got)
	}

	// A new fact: typed once, only that, as background.
	must(200, "POST", "/api/projects/"+pid+"/memory/accept", map[string]any{"scope": "project", "facts": []any{map[string]any{"kind": "context", "text": "The typed codeword is lynx-3317."}}})
	got = turn("typed", func() {
		if r := must(200, "POST", "/api/projects/"+pid+"/memory/push", map[string]any{"memoryNodeId": testMemTile}); r["delivered"].(float64) != 1 {
			t.Fatalf("push %v", r)
		}
	})
	if !strings.Contains(strings.ToUpper(got), "OK") || len(got) > 40 {
		t.Errorf("background became work: %q", got)
	}
	list := must(200, "GET", "/api/sessions/"+id+"/deliveries", nil)["items"].([]any)
	typed := list[len(list)-1].(map[string]any)["text"].(string)
	if !strings.Contains(typed, "lynx-3317") || strings.Contains(typed, "zebra") || strings.Contains(typed, "walrus") {
		t.Errorf("typed more than the new fact:\n%s", typed)
	}
	t.Logf("typed %d characters", len([]rune(typed)))

	// Nothing new: no turn.
	ready("after the push")
	n := len(list)
	if r := must(200, "POST", "/api/projects/"+pid+"/memory/push", map[string]any{"memoryNodeId": testMemTile}); r["upToDate"].(float64) != 1 || r["delivered"].(float64) != 0 {
		t.Errorf("nothing new: %v", r)
	}
	if after := must(200, "GET", "/api/sessions/"+id+"/deliveries", nil)["items"].([]any); len(after) != n {
		t.Errorf("typed with nothing new: %d deliveries, was %d", len(after), n)
	}

	// /compact and /clear, as the CLI really signals them. The fact was typed, so it is
	// missing again; what the launch gave is not. /clear uses no turn, /compact a short one.
	missing := func() float64 {
		return must(200, "POST", "/api/projects/"+pid+"/memory/plan", map[string]any{"memoryNodeId": testMemTile})["targets"].([]any)[0].(map[string]any)["chars"].(float64)
	}
	for _, command := range []string{"/compact", "/clear"} {
		ready("before " + command)
		if c := missing(); c != 0 {
			t.Fatalf("before %s: %v", command, c)
		}
		if err := sess.Input([]byte(command)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(400 * time.Millisecond)
		if err := sess.Input([]byte("\r")); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(120 * time.Second)
		for missing() == 0 {
			if time.Now().After(deadline) {
				dump("timeout-" + command[1:])
				t.Fatalf("%s: the hub did not notice:\n%s", command, sess.Screen().Text())
			}
			time.Sleep(300 * time.Millisecond)
		}
		chars := missing()
		t.Logf("%s: %v characters missing", command, chars)
		if chars > 400 {
			t.Errorf("%s: more than the typed fact is missing: %v", command, chars)
		}
		if command == "/compact" {
			turn("typed again", func() {
				if r := must(200, "POST", "/api/projects/"+pid+"/memory/push", map[string]any{"memoryNodeId": testMemTile}); r["delivered"].(float64) != 1 {
					t.Fatalf("push %v", r)
				}
			})
		}
	}
}

// TestRealBlockedCommand asks a real Claude Code, on the cheapest model, to run a command the
// owner blocked. It spends the owner's plan: one short turn. The command only writes a mark in
// the folder of this test.
//
//	NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_MEMORY=1 NEXTBOARD_REAL_CLI_CWD=/already/trusted \
//	go test -run TestRealBlockedCommand -v ./internal/server/
func TestRealBlockedCommand(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_CLI_TEST") != "1" || os.Getenv("NEXTBOARD_REAL_MEMORY") != "1" {
		t.Skip("set NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_MEMORY=1 (memakai plan CLI pemilik)")
	}
	cwd := os.Getenv("NEXTBOARD_REAL_CLI_CWD")
	if cwd == "" {
		t.Fatal("NEXTBOARD_REAL_CLI_CWD: a folder Claude Code already trusts")
	}
	for _, k := range []string{"CLAUDE_EFFORT", "CLAUDE_PID", "CLAUDE_AGENT_SDK_VERSION"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	mark := filepath.Join(cwd, "nb-guard-mark")
	script := filepath.Join(cwd, "nb-guard-check.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ran > "+mark+"\necho guard-script-ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(script); os.Remove(mark) })

	for _, mode := range [][]any{{"--permission-mode", "manual"}} {
		name := fmt.Sprint(mode[0])
		t.Run(name, func(t *testing.T) {
			os.Remove(mark)
			h := newTestHub(t)
			h.setup()
			must := func(want int, method, path string, body any) map[string]any {
				t.Helper()
				st, out := h.do(method, path, body, nil)
				if st != want {
					t.Fatalf("%s %s: %d %v", method, path, st, out)
				}
				return out
			}
			must(200, "PATCH", "/api/settings", map[string]any{"agents": map[string]any{"profiles": map[string]any{"local": map[string]any{"claude": nil}}}})
			must(201, "POST", "/api/agents", map[string]any{"id": "real-claude", "name": "Real Claude", "command": "claude", "base": "claude",
				"args": append([]any{"--model", "haiku"}, mode...)})
			p := must(201, "POST", "/api/projects", map[string]any{"machineId": "local", "path": cwd, "name": "Portal"})
			pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
			must(200, "PUT", "/api/memory/blocked?projectId="+pid, map[string]any{"commands": []any{"nb-guard-check.sh"}})
			in := must(201, "POST", "/api/sessions", map[string]any{"agent": "real-claude", "workspaceId": wid, "cwd": cwd, "cols": 110, "rows": 34})
			id := in["id"].(string)
			t.Cleanup(func() { h.do("DELETE", "/api/sessions/"+id, nil, nil) })
			sess, _ := h.srv.sessions.Get(id)
			info := func() session.Info { i, _ := h.srv.sessions.Info(id); return i }
			deadline := time.Now().Add(60 * time.Second)
			for info().AgentState != "ready" {
				if time.Now().After(deadline) {
					t.Fatalf("not ready: %s\n%s", info().AgentState, sess.Screen().Text())
				}
				time.Sleep(200 * time.Millisecond)
			}
			time.Sleep(1500 * time.Millisecond)
			screen := strings.ToLower(sess.Screen().Text())
			for _, dialog := range []string{"trust", "install", "plugin", "do you want", "bypass permissions mode"} {
				if info().AgentState != "ready" || (strings.Contains(screen, dialog) && !strings.Contains(screen, "bypass permissions on")) {
					t.Fatalf("a dialog on screen (%q), nothing typed:\n%s", dialog, screen)
				}
			}
			// Through a wrapper the CLI does not strip: the rule has to see inside it.
			must(202, "POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "Run this exact shell command with your Bash tool and tell me what it printed: bash -c './nb-guard-check.sh'", "source": "api"})
			// What must happen: it stops and asks, and the script does not run.
			asked := false
			deadline = time.Now().Add(90 * time.Second)
			for time.Now().Before(deadline) && !asked {
				if info().AgentState == "needsYou" {
					asked = true
					break
				}
				if _, err := os.Stat(mark); err == nil {
					t.Fatalf("the blocked command ran:\n%s", sess.Screen().Text())
				}
				time.Sleep(200 * time.Millisecond)
			}
			t.Logf("state %s activity %q\n%s", info().AgentState, info().Activity, sess.Screen().Text())
			if !asked {
				t.Fatal("it never asked")
			}
			time.Sleep(1500 * time.Millisecond)
			if _, err := os.Stat(mark); err == nil {
				t.Fatal("the blocked command ran while asking")
			}
			if err := sess.Input([]byte("\x1b")); err != nil { // refuse
				t.Fatal(err)
			}
			time.Sleep(2 * time.Second)
			if _, err := os.Stat(mark); err == nil {
				t.Fatal("refused and it ran")
			}
		})
	}
}

// TestRealCredentialRun has a real Claude Code, on the cheapest model, use a credential through
// the hub: it must ask with credential_run, never with Bash, and read the output with the value
// hidden. It spends the owner's plan: one short turn. The test approves with a value made up here,
// and then looks for it on the screen and in Claude Code's own transcript.
//
//	NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_MEMORY=1 NEXTBOARD_REAL_CLI_CWD=/already/trusted \
//	go test -run TestRealCredentialRun -v ./internal/server/
func TestRealCredentialRun(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_CLI_TEST") != "1" || os.Getenv("NEXTBOARD_REAL_MEMORY") != "1" {
		t.Skip("set NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_MEMORY=1 (memakai plan CLI pemilik)")
	}
	cwd := os.Getenv("NEXTBOARD_REAL_CLI_CWD")
	if cwd == "" {
		t.Fatal("NEXTBOARD_REAL_CLI_CWD: a folder Claude Code already trusts")
	}
	for _, k := range []string{"CLAUDE_EFFORT", "CLAUDE_PID", "CLAUDE_AGENT_SDK_VERSION"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	sum := sha256.Sum256([]byte(time.Now().String()))
	canary := "nb-real-CANARY-" + hex.EncodeToString(sum[:6])
	started := time.Now()
	// A deploy tool that prints its token in its log: the accident the censor is for. Asked to
	// print a secret on purpose, Claude Code refuses, as the tool's description tells it to.
	script := filepath.Join(cwd, "nb-deploy.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"deploy: connecting with token $DEMO_TOKEN\"\necho \"deploy: ok\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(script) })

	h := newTestHub(t)
	h.setup()
	h.srv.credReqs.wait = 5 * time.Second
	must := func(want int, method, path string, body any) map[string]any {
		t.Helper()
		st, out := h.do(method, path, body, nil)
		if st != want {
			t.Fatalf("%s %s: %d %v", method, path, st, out)
		}
		return out
	}
	must(200, "PATCH", "/api/settings", map[string]any{"agents": map[string]any{"profiles": map[string]any{"local": map[string]any{"claude": nil}}}})
	// Manual: anything but the hub's own tools would stop and ask, and the test sees that.
	must(201, "POST", "/api/agents", map[string]any{"id": "real-claude", "name": "Real Claude", "command": "claude", "base": "claude",
		"args": []any{"--model", "haiku", "--permission-mode", "manual"}})
	p := must(201, "POST", "/api/projects", map[string]any{"machineId": "local", "path": cwd, "name": "Portal"})
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	must(201, "POST", "/api/vault", vaultKey())
	must(200, "PUT", "/api/vault/items/item-demo0001", map[string]any{"blob": vaultBlob(7), "baseRev": 0})
	must(200, "PUT", "/api/vault/agents/item-demo0001", map[string]any{"name": "Demo token", "type": "login", "projects": []string{pid}, "values": []string{"password"}})

	in := must(201, "POST", "/api/sessions", map[string]any{"agent": "real-claude", "workspaceId": wid, "cwd": cwd, "cols": 110, "rows": 34})
	id := in["id"].(string)
	t.Cleanup(func() { h.do("DELETE", "/api/sessions/"+id, nil, nil) })
	sess, _ := h.srv.sessions.Get(id)
	info := func() session.Info { i, _ := h.srv.sessions.Info(id); return i }
	deadline := time.Now().Add(60 * time.Second)
	for info().AgentState != "ready" {
		if time.Now().After(deadline) {
			t.Fatalf("not ready: %s\n%s", info().AgentState, sess.Screen().Text())
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(1500 * time.Millisecond)
	screen := strings.ToLower(sess.Screen().Text())
	for _, dialog := range []string{"trust", "install", "plugin", "do you want", "bypass permissions mode"} {
		if info().AgentState != "ready" || strings.Contains(screen, dialog) {
			t.Fatalf("a dialog on screen (%q), nothing typed:\n%s", dialog, screen)
		}
	}
	must(202, "POST", "/api/sessions/"+id+"/deliveries", map[string]any{"source": "api",
		"text": `Deploy with ./nb-deploy.sh. It needs the password of the credential "Demo token" in the environment variable DEMO_TOKEN, so run it with the nextboard credential tools, not Bash, and wait with credential_result if needed. Then tell me what it printed.`})

	// The request comes in, and is approved here as the owner's browser would.
	var req map[string]any
	deadline = time.Now().Add(120 * time.Second)
	for req == nil {
		if time.Now().After(deadline) {
			t.Fatalf("no request (state %s):\n%s", info().AgentState, sess.Screen().Text())
		}
		if info().AgentState == "needsYou" {
			t.Fatalf("it asked for a permission instead:\n%s", sess.Screen().Text())
		}
		_, out := h.do("GET", "/api/vault/requests", nil, nil)
		for _, x := range out["items"].([]any) {
			if m := x.(map[string]any); m["state"] == "waiting" {
				req = m
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("request: %v", req)
	if !strings.Contains(fmt.Sprint(req["command"]), "nb-deploy.sh") || fmt.Sprint(req["uses"]) != "[map[env:DEMO_TOKEN item:item-demo0001 name:Demo token value:password]]" {
		t.Fatalf("request %v", req)
	}
	must(200, "POST", "/api/vault/requests/"+req["id"].(string)+"/approve", map[string]any{"values": []string{canary}})

	// Its answer: the output with the value hidden.
	deadline = time.Now().Add(150 * time.Second)
	for !strings.Contains(sess.Screen().Text(), "token ••••") || info().AgentState != "ready" {
		if time.Now().After(deadline) {
			t.Fatalf("no answer (state %s):\n%s", info().AgentState, sess.Screen().Text())
		}
		if info().AgentState == "needsYou" {
			t.Fatalf("it asked for a permission:\n%s", sess.Screen().Text())
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("screen:\n%s", sess.Screen().Text())
	if strings.Contains(sess.Screen().Text()+sess.ScrollbackText(2000), canary) {
		t.Fatal("the value reached the console")
	}
	// Claude Code keeps what the model saw: the value must not be in it.
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".claude", "projects", transcript.ClaudeSlug(cwd))
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	looked := 0
	for _, f := range files {
		if st, err := os.Stat(f); err != nil || st.ModTime().Before(started) {
			continue
		}
		looked++
		raw, _ := os.ReadFile(f)
		if strings.Contains(string(raw), canary) {
			t.Fatalf("the value is in the transcript %s", f)
		}
		if !strings.Contains(string(raw), "token ••••") {
			t.Errorf("the transcript %s does not show the hidden output", f)
		}
	}
	if looked == 0 {
		t.Fatalf("no transcript of this run in %s", dir)
	}
}
