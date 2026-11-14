package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/azamrizki/nextboard/internal/marker"
	"github.com/azamrizki/nextboard/internal/store"
)

func newStore(t *testing.T) *Store {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(st)
}

func req(t *testing.T, v map[string]any) Request {
	r := Request{}
	for k, x := range v {
		b, _ := json.Marshal(x)
		r[k] = b
	}
	return r
}

func TestCatalogOrderAndShape(t *testing.T) {
	s := newStore(t)
	all := s.All()
	if all[0].ID != "shell" || all[1].ID != "claude" || all[2].ID != "codex" || all[3].ID != "gemini" || all[4].Source != SourceCatalog {
		t.Fatalf("order %v %v %v", all[0].ID, all[1].ID, all[4].Source)
	}
	c, _ := s.Get("claude")
	if c.Resume.Kind != ResumeSessionID || c.LiveStatus != LiveClaudeHooks || c.ProfileEnv != "CLAUDE_CONFIG_DIR" ||
		strings.Join(c.ResumeArgs, " ") != "--continue" || c.Detect != "claude" {
		t.Fatalf("claude %+v", c)
	}
	for _, id := range []string{"cline", "cn", "copilot", "cursor-agent", "kimi", "pi", "vibe"} {
		a, ok := s.Get(id) // † candidates ship without yolo/resume
		if !ok || len(a.YoloArgs) != 0 || a.Resume.Kind != ResumeNone {
			t.Fatalf("%s %+v", id, a)
		}
	}
	if k, _ := s.Get("kiro"); k.Command != "kiro-cli" || k.Args[0] != "chat" || k.Detect != "kiro-cli" {
		t.Fatalf("kiro %+v", k)
	}
	b, _ := json.Marshal(c)
	for _, f := range []string{`"kind":"agent"`, `"source":"builtin"`, `"resumeArgs":["--continue"]`, `"yoloArgs":["--dangerously-skip-permissions"]`} {
		if !bytes.Contains(b, []byte(f)) {
			t.Fatalf("json lacks %s: %s", f, b)
		}
	}
	if bytes.Contains(b, []byte(`"available"`)) {
		t.Fatal("available must be omitted")
	}
}

func TestCustomCRUD(t *testing.T) {
	s := newStore(t)
	a, err := s.Create(req(t, map[string]any{"name": "My Bot 2", "command": "mybot", "args": []string{"--x"}}))
	if err != nil || a.ID != "my-bot-2" || a.Source != SourceCustom || a.LiveStatus != LiveScreen || a.Transcript != TranscriptNone || a.Resume.Kind != ResumeNone || a.HasYolo() {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := s.Create(req(t, map[string]any{"id": "my-bot-2", "name": "x", "command": "y"})); !errors.Is(err, ErrExists) {
		t.Fatalf("dup %v", err)
	}
	for _, id := range []string{"claude", "opencode"} {
		if _, err := s.Create(req(t, map[string]any{"id": id, "name": "x", "command": "y"})); !errors.Is(err, ErrExists) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	var be *BadAgentError
	for name, body := range map[string]map[string]any{
		"bad id":         {"id": "9x", "name": "x", "command": "y"},
		"no command":     {"name": "x"},
		"forbidden":      {"name": "x", "command": "y", "liveStatus": "claude-hooks"},
		"transcript":     {"name": "x", "command": "y", "transcript": "claude"},
		"bad base":       {"name": "x", "command": "y", "base": "gemini"},
		"resume no base": {"name": "x", "command": "y", "resume": map[string]any{"kind": "session-id", "resumeArgs": []string{"-r", "{agentSessionId}"}}},
		"template":       {"name": "x", "command": "y", "args": []string{"--cwd={cwd}"}},
		"placeholder":    {"name": "x", "command": "y", "args": []string{"{agentSessionId}"}},
		"env":            {"name": "x", "command": "y", "env": []string{"NOEQUALS"}},
		"color":          {"name": "x", "command": "y", "color": "red"},
	} {
		if _, err := s.Create(req(t, body)); !errors.As(err, &be) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// With a base, capabilities are inherited.
	cb, err := s.Create(req(t, map[string]any{"id": "work-claude", "name": "Work Claude", "command": "/opt/claude", "base": "claude"}))
	if err != nil || cb.LiveStatus != LiveClaudeHooks || cb.Transcript != TranscriptClaude || cb.Resume.Kind != ResumeSessionID || !cb.HasYolo() || cb.CapBase() != "claude" {
		t.Fatalf("%+v %v", cb, err)
	}
	cb, err = s.Update("work-claude", req(t, map[string]any{"yoloArgs": []string{}, "name": "Renamed"}))
	if err != nil || cb.HasYolo() || cb.Name != "Renamed" {
		t.Fatalf("update %+v %v", cb, err)
	}
	if _, err := s.Update("claude", req(t, map[string]any{"name": "x"})); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("builtin patch %v", err)
	}
	if _, err := s.Update("work-claude", req(t, map[string]any{"id": "other"})); !errors.As(err, &be) {
		t.Fatalf("id patch %v", err)
	}
	if err := s.Delete("opencode"); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("catalog delete %v", err)
	}
	if err := s.Delete("my-bot-2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("my-bot-2"); ok {
		t.Fatal("not deleted")
	}
	if all := s.All(); all[len(all)-1].ID != "work-claude" {
		t.Fatal("custom not last")
	}
}

func TestLegacyPresetsFile(t *testing.T) {
	s := newStore(t)
	if err := s.st.Save(File, []map[string]any{
		{"id": "claude", "name": "Claude (work)", "command": "claude-work", "resumeArgs": []string{"--continue"}},
		{"id": "echoer", "name": "Echoer", "command": "sh", "args": []string{"-c", "echo"}, "resumeArgs": []string{"again"}},
		{"id": "myshell", "name": "Bash", "command": "$SHELL"},
	}); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	c, _ := s.Get("claude")
	if c.Source != SourceCustom || c.Base != "claude" || c.Command != "claude-work" || c.LiveStatus != LiveClaudeHooks || c.Resume.Kind != ResumeContinue {
		t.Fatalf("override %+v", c)
	}
	if s.All()[1].ID != "claude" {
		t.Fatal("override moved")
	}
	e, _ := s.Get("echoer")
	if e.Kind != KindAgent || e.Resume.Kind != ResumeContinue || e.ResumeArgs[0] != "again" {
		t.Fatalf("legacy %+v", e)
	}
	if m, _ := s.Get("myshell"); m.Kind != KindShell || m.Detect != "" {
		t.Fatalf("legacy shell %+v", m)
	}
}

func TestDefaultAgent(t *testing.T) {
	all := append(Builtins(), Catalog()...)
	avail := func(ids ...string) func(Agent) bool {
		return func(a Agent) bool {
			for _, id := range ids {
				if a.ID == id {
					return true
				}
			}
			return false
		}
	}
	for _, c := range []struct {
		conf string
		av   []string
		want string
	}{
		{"", []string{"codex", "claude"}, "claude"},
		{"codex", []string{"codex", "claude"}, "codex"},
		{"codex", []string{"claude"}, "claude"},
		{"", []string{"qwen", "aider"}, "aider"},
		{"", nil, "shell"},
	} {
		if got := DefaultFor(all, c.conf, avail(c.av...)); got != c.want {
			t.Errorf("%v: got %s want %s", c, got, c.want)
		}
	}
}

func TestBuildModes(t *testing.T) {
	s := newStore(t)
	c, _ := s.Get("claude")
	p, err := Build(c, Options{Mode: ModeFresh, LiveStatus: true, Token: "tk", SessionID: "s_1", Yolo: true})
	if err != nil || p.Mode != ModeFresh || len(p.AgentSessionID) != 36 || p.LiveStatus != "pending" || !strings.HasSuffix(p.Shell, " 1") {
		t.Fatalf("%+v %v", p, err)
	}
	p2, _ := Build(c, Options{Mode: ModeFresh})
	if p2.AgentSessionID == p.AgentSessionID || p2.LiveStatus != "off" {
		t.Fatal("fresh must create a new UUID; liveStatus off")
	}
	if p, _ := Build(c, Options{Mode: ModeResume, AgentSessionID: "abc"}); p.Mode != ModeResume || p.AgentSessionID != "abc" {
		t.Fatalf("resume %+v", p)
	}
	if p, _ := Build(c, Options{Mode: ModeResume}); p.Mode != ModeContinue {
		t.Fatalf("resume without id → continue, got %s", p.Mode)
	}
	cx, _ := s.Get("codex")
	if p, _ := Build(cx, Options{Mode: ModeFresh, LiveStatus: true}); p.AgentSessionID != "" || !strings.Contains(envOf(p, "NB_WRAP"), `-c 'notify=["sh","-c","eval \"$NB_HOOK_SH\"","nb","notify","turn"]'`) || envOf(p, "NB_HOOKS") != "" {
		t.Fatalf("codex %q", envOf(p, "NB_WRAP"))
	}
	g, _ := s.Get("gemini")
	if p, _ := Build(g, Options{Mode: ModeFresh, LiveStatus: true}); p.LiveStatus != "screen" {
		t.Fatal(p.LiveStatus)
	}
	sh, _ := s.Get("shell")
	if p, _ := Build(sh, Options{}); p.Wrapped {
		t.Fatal("shell wrapped")
	}
	// Custom agents without yolo never get a yolo flag.
	cu, _ := s.Create(req(t, map[string]any{"name": "Nope", "command": "nope"}))
	if p, _ := Build(cu, Options{Mode: ModeFresh, Yolo: true}); strings.HasSuffix(p.Shell, " 1") || strings.Contains(envOf(p, "NB_WRAP"), "[ \"$3\" = 1 ]") {
		t.Fatalf("custom yolo %q", p.Shell)
	}
	// Profile env.
	if p, _ := Build(c, Options{Mode: ModeFresh, ProfileDir: "/p/x"}); envOf(p, "CLAUDE_CONFIG_DIR") != "/p/x" {
		t.Fatal("profile env")
	}
}

func envOf(p Plan, k string) string {
	for _, kv := range p.Env {
		if v, ok := strings.CutPrefix(kv, k+"="); ok {
			return v
		}
	}
	return ""
}

func TestHooksJSON(t *testing.T) {
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Timeout int    `json:"timeout"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(HooksJSON()), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Hooks) != 8 || doc.Hooks["PreToolUse"][0].Matcher != "*" || doc.Hooks["Stop"][0].Hooks[0].Command != `set -- hook Stop; eval "$NB_HOOK_SH"` || doc.Hooks["Stop"][0].Hooks[0].Timeout != 5 {
		t.Fatalf("%+v", doc)
	}
}

// ptyRun runs argv in a PTY with env and returns its output after it exits.
func ptyRun(t *testing.T, env []string, input string, argv ...string) []byte {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	f, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var mu sync.Mutex
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	if input != "" {
		time.Sleep(300 * time.Millisecond)
		f.Write([]byte(input))
	}
	werr := make(chan error, 1)
	go func() { werr <- cmd.Wait() }()
	select {
	case <-werr:
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("timeout; output %q", out.String())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]byte(nil), out.Bytes()...)
}

func TestWrapperRunsAgentThenShell(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakeclaude")
	os.WriteFile(fake, []byte("#!/bin/sh\nprintf 'ARGS:'; for a in \"$@\"; do printf '[%s]' \"$a\"; done; echo; exit 3\n"), 0o755)
	s := newStore(t)
	c, _ := s.Get("claude")
	p, err := Build(c, Options{Mode: ModeFresh, LiveStatus: true, Token: "tok123", Command: fake, Yolo: true})
	if err != nil {
		t.Fatal(err)
	}
	env := append(p.Env, "SHELL=/bin/sh", "PS1=$ ", "ENV=")
	out := ptyRun(t, env, "echo SHELL-$((40+2))\rexit 0\r", "/bin/sh", "-c", p.Shell)
	st := &marker.Stripper{Token: "tok123"}
	clean, ms := st.Feed(out, time.Now())
	if len(ms) != 2 || ms[0].Type != marker.TypeAgentStart || ms[0].Payload.Mode != "fresh" || ms[0].Payload.Sid != p.AgentSessionID || ms[0].Payload.Pid == 0 {
		t.Fatalf("markers %+v\n%q", ms, out)
	}
	if ms[1].Type != marker.TypeAgentExit || ms[1].Payload.Rc == nil || *ms[1].Payload.Rc != 3 {
		t.Fatalf("exit marker %+v", ms[1])
	}
	want := "ARGS:[--session-id][" + p.AgentSessionID + "][--settings][" + claudeSettings(true, "", true, nil, nil) + "][--dangerously-skip-permissions]"
	if !bytes.Contains(clean, []byte(want)) {
		t.Fatalf("args: want %s in %q", want, clean)
	}
	if !bytes.Contains(clean, []byte("SHELL-42")) {
		t.Fatalf("no shell after exit: %q", clean)
	}
	if bytes.Contains(clean, []byte("777;nb")) {
		t.Fatal("marker left in output")
	}
	// Mode shell: no agent, no marker.
	out = ptyRun(t, env, "exit 0\r", "/bin/sh", "-c", WrapperLine(ModeShell, "", false, "", ""))
	if _, ms := (&marker.Stripper{Token: "tok123"}).Feed(out, time.Now()); len(ms) != 0 {
		t.Fatalf("shell mode markers %+v", ms)
	}
}

func TestWrapperSurvivesCtrlC(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "agent")
	os.WriteFile(fake, []byte("#!/bin/sh\necho READY; sleep 5; echo NOT-INTERRUPTED\n"), 0o755)
	cu := resolve(entry{ID: "x", Name: "x", Command: fake})
	p, _ := Build(cu, Options{Mode: ModeFresh, Token: "t"})
	env := append(p.Env, "SHELL=/bin/sh", "PS1=$ ", "ENV=")
	cmd := exec.Command("/bin/sh", "-c", p.Shell)
	cmd.Env = append(os.Environ(), env...)
	f, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var buf bytes.Buffer
	var mu sync.Mutex
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := f.Read(b)
			mu.Lock()
			buf.Write(b[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	wait := func(s string) {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := bytes.Contains(buf.Bytes(), []byte(s))
			mu.Unlock()
			if ok {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("timeout waiting %q in %q", s, buf.String())
	}
	wait("READY")
	f.Write([]byte{3})
	wait("777;nb;agent-exit")
	f.Write([]byte("echo ALIVE-$((1+1))\r"))
	wait("ALIVE-2")
	f.Write([]byte("exit\r"))
	cmd.Wait()
	mu.Lock()
	defer mu.Unlock()
	if bytes.Contains(buf.Bytes(), []byte("NOT-INTERRUPTED")) {
		t.Fatal("agent not interrupted")
	}
	_, ms := (&marker.Stripper{Token: "t"}).Feed(buf.Bytes(), time.Now())
	if len(ms) != 2 || *ms[1].Payload.Rc != 130 {
		t.Fatalf("markers %+v", ms)
	}
}

func TestHookScriptTTYAndHTTPFallback(t *testing.T) {
	payload := `{"session_id":"abc","hook_event_name":"Notification","message":"Claude needs your permission to use Bash","notification_type":"permission_prompt"}`
	env := []string{"NB_TOKEN=tk", "NB_HOOK_SH=" + HookScript}
	in := "printf %s '" + payload + "' | sh -c 'set -- hook Notification; eval \"$NB_HOOK_SH\"'; echo RC=$?"
	out := ptyRun(t, env, "", "/bin/sh", "-c", in)
	st := &marker.Stripper{Token: "tk"}
	clean, ms := st.Feed(out, time.Now())
	if len(ms) != 1 || ms[0].Payload.Ev != "Notification" || string(ms[0].RawBytes()) != payload {
		t.Fatalf("markers %+v out %q", ms, out)
	}
	if strings.TrimSpace(string(clean)) != "RC=0" {
		t.Fatalf("hook printed to stdout: %q", clean)
	}
	// Large stdin is cut at 3072 bytes and drained.
	big := strings.Repeat("x", 10000)
	out = ptyRun(t, env, "", "/bin/sh", "-c", "printf %s "+big+" | sh -c 'set -- hook Stop; eval \"$NB_HOOK_SH\"'; echo RC=$?")
	if _, ms := (&marker.Stripper{Token: "tk"}).Feed(out, time.Now()); len(ms) != 1 || len(ms[0].RawBytes()) != marker.RawMax {
		t.Fatalf("big %+v", ms)
	}
	// Codex notify passes the JSON as $3.
	out = ptyRun(t, env, "", "/bin/sh", "-c", `sh -c 'eval "$NB_HOOK_SH"' nb notify turn '{"type":"agent-turn-complete","thread-id":"t1"}'`)
	if _, ms := (&marker.Stripper{Token: "tk"}).Feed(out, time.Now()); len(ms) != 1 || ms[0].Type != "notify" || !strings.Contains(string(ms[0].RawBytes()), `"thread-id":"t1"`) {
		t.Fatalf("notify %+v %q", ms, out)
	}
	// Without a controlling terminal the hook POSTs to NB_HOOK_URL.
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid tidak ada")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl tidak ada")
	}
	// No controlling terminal but the parent has one (as Claude Code runs
	// hooks): the marker goes to the parent's terminal.
	out = ptyRun(t, env, "", "/bin/sh", "-c", `printf '{"hook_event_name":"Stop"}' | setsid sh -c 'set -- hook Stop; eval "$NB_HOOK_SH"'; sleep 0.2`)
	if _, ms := (&marker.Stripper{Token: "tk"}).Feed(out, time.Now()); len(ms) != 1 || ms[0].Payload.Ev != "Stop" {
		t.Fatalf("parent tty channel %+v %q", ms, out)
	}
	// hookChannel http skips the terminal.
	out = ptyRun(t, append(env, "NB_HOOK_CH=http"), "", "/bin/sh", "-c", `printf '{}' | sh -c 'set -- hook Stop; eval "$NB_HOOK_SH"'`)
	if _, ms := (&marker.Stripper{Token: "tk"}).Feed(out, time.Now()); len(ms) != 0 {
		t.Fatalf("http mode wrote to the terminal: %+v", ms)
	}
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r.URL.Path + " " + string(b)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	cmd := exec.Command("setsid", "sh", "-c", `printf '{"hook_event_name":"Stop"}' | sh -c 'set -- hook Stop; eval "$NB_HOOK_SH"'`)
	cmd.Env = append(os.Environ(), append(env, "NB_HOOK_URL="+srv.URL+"/nb/hook")...)
	if b, err := cmd.CombinedOutput(); err != nil || len(b) != 0 {
		t.Fatalf("setsid run %v %q", err, b)
	}
	select {
	case s := <-got:
		path, body, _ := strings.Cut(s, " ")
		m, err := marker.Parse([]byte(body))
		if path != "/nb/hook" || err != nil || m.Type != "hook" || m.Payload.K != "tk" || string(m.RawBytes()) != `{"hook_event_name":"Stop"}` {
			t.Fatalf("http %q %+v %v", s, m, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no HTTP fallback request")
	}
}

func TestScreenRulesEvaluate(t *testing.T) {
	r := RulesFor("claude")
	lines := []string{"", "Bash command", "  rm -rf dist", "Do you want to proceed?", "❯ 1. Yes", "  2. No", ""}
	if m := r.Evaluate(lines, 4); m.NeedsYou == "" || m.Ready {
		t.Fatalf("%+v", m)
	}
	if m := r.Evaluate([]string{"✻ Thinking… (12s · ↓ 300 tokens · esc to interrupt)", "> "}, 1); !m.Working || m.Ready {
		t.Fatalf("%+v", m)
	}
	if m := RulesFor("nope").Evaluate(lines, 0); m != (ScreenMatch{}) {
		t.Fatal("nil rules")
	}
}

// TestTrustDialogNarrow: the trust dialog is recognised (label "Trust
// folder?") also when a narrow console wraps its question.
func TestTrustDialogNarrow(t *testing.T) {
	lines := []string{
		" Accessing workspace:", "", " /work/app", "",
		" Quick safety check: Is this a project you", " created or one you trust? (Like your own",
		" code, a well-known open source project, or", " work from your team).", "",
		" ❯ No, exit", "   Yes, I trust this", "   folder", "", " Enter to confirm ·", " Esc to cancel",
	}
	m := RulesFor("claude").Evaluate(lines, 9)
	if m.NeedsYou != "Trust folder?" || !m.Startup || m.Ready {
		t.Fatalf("%+v", m)
	}
	if ok, _ := InputPrompt("claude", lines, 9); ok {
		t.Fatal("delivery would type into the trust dialog")
	}
	// The real 110-column dump has the same label.
	if m := RulesFor("claude").Evaluate(readFixture(t, "testdata/screens/claude-2.1.312-needsYou-trust.txt")); m.NeedsYou != "Trust folder?" {
		t.Fatalf("real fixture %+v", m)
	}
}

func readFixture(t *testing.T, f string) ([]string, int) {
	lines, cy, _, _ := readScreen(t, f)
	return lines, cy
}

// Get answers as All lists: built-ins, catalog, custom agents and overrides of a built-in.
func TestGetMatchesAll(t *testing.T) {
	s := newStore(t)
	for _, v := range []map[string]any{
		{"name": "My Bot", "command": "mybot", "args": []string{"--x"}},
		{"name": "Based", "command": "based", "base": "claude"},
	} {
		if _, err := s.Create(req(t, v)); err != nil {
			t.Fatal(err)
		}
	}
	all := s.All()
	for _, a := range all {
		got, ok := s.Get(a.ID)
		if !ok || !reflect.DeepEqual(got, a) {
			t.Fatalf("Get(%q) = %+v, %v; All has %+v", a.ID, got, ok, a)
		}
	}
	if _, ok := s.Get("no-such-agent"); ok {
		t.Fatal("found an agent that is not there")
	}
}
