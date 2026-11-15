package server

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealProviderScrub runs the installed Claude Code as hub consoles on an API provider pointed
// at a scripted model on loopback, from an empty home (no account, nothing of the owner's), with
// bubblewrap installed, so with CLAUDE_CODE_SUBPROCESS_ENV_SCRUB. That variable pins Claude Code
// to its default mode, where every edit asks; the hub's allow rules stand in for acceptEdits and
// for yolo. No dialog is answered: a question is the result. An edit in the project goes ahead
// without a question, one outside it asks. A yolo console runs a command without a question, and
// that command does not see the key: not in env, /proc/self/environ, its parent's environ, nor any
// environ it can read. Every request reaches the model with the key.
//
//	NEXTBOARD_REAL_SCRUB_TEST=1 go test ./internal/server -run TestRealProviderScrub -v
func TestRealProviderScrub(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_SCRUB_TEST") != "1" {
		t.Skip("set NEXTBOARD_REAL_SCRUB_TEST=1 (installed Claude Code and bubblewrap, a model on loopback, no account)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude is not installed")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not installed: no scrub")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		for _, pre := range []string{"CLAUDE", "ANTHROPIC", "XDG_"} {
			if strings.HasPrefix(k, pre) {
				t.Setenv(k, "")
				os.Unsetenv(k)
			}
		}
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("DISABLE_AUTOUPDATER", "1")

	const key = "sk-fake-NBSCRUB-0123456789abcdef"
	dir, outside := outsideTmp(t), outsideTmp(t)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("alpha\n"), 0o644)
	// trusted in the (empty) home's login, which the hub copies into the provider's config: no
	// trust dialog to answer
	trust, _ := json.Marshal(map[string]any{"projects": map[string]any{dir: map[string]any{"hasTrustDialogAccepted": true}}})
	os.WriteFile(filepath.Join(home, ".claude.json"), trust, 0o600)

	model := newScriptModel(t)
	h := newTestHub(t)
	h.setup()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "acme-notes"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	wid := p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	st, pv := h.do("POST", "/api/providers", map[string]any{"name": "Loopback", "kind": "anthropic", "baseUrl": model.URL, "model": "fake-model", "apiKey": key}, nil)
	if st != 201 {
		t.Fatalf("provider %d %v", st, pv)
	}

	console := func(t *testing.T, yolo bool) (id string, screen func() string, info func() map[string]any) {
		t.Helper()
		st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "claude", "workspaceId": wid, "providerId": pv["id"], "yolo": yolo}, nil)
		if st != 201 {
			t.Fatalf("console %d %v", st, in)
		}
		id = in["id"].(string)
		sess, _ := h.srv.sessions.Get(id)
		screen = func() string { return strings.Join(sess.Screen().Lines, "\n") }
		info = func() map[string]any { _, x := h.do("GET", "/api/sessions/"+id, nil, nil); return x }
		t.Cleanup(func() { h.do("POST", "/api/sessions/"+id+"/kill", nil, nil) })
		deadline := time.Now().Add(120 * time.Second)
		for {
			x := info()
			if x["agentState"] == "ready" && x["busy"] == false {
				break
			}
			if x["agentState"] == "needsYou" || time.Now().After(deadline) {
				t.Fatalf("not at its prompt (%v):\n%s", x["agentState"], screen())
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Logf("command line: %s", cliCmdline("claude", dir))
		if s := screen(); strings.Contains(s, "forced to default") {
			t.Errorf("Claude Code dropped a permission mode:\n%s", s)
		}
		return id, screen, info
	}
	// step asks for one tool call and reports what came of it
	step := func(t *testing.T, id string, screen func() string, info func() map[string]any, n int, tool string, input map[string]any, done func() bool) string {
		t.Helper()
		model.mu.Lock()
		model.results = nil
		model.mu.Unlock()
		marker := fmt.Sprintf("NB-SCRUB-STEP-%d", n)
		model.set(&scriptStep{marker: marker, tool: tool, input: input})
		if st, d := h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "please do " + marker}, nil); st != 202 {
			t.Fatalf("deliver %d %v", st, d)
		}
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			x := info()
			switch {
			case done():
				return "done without a question"
			case x["agentState"] == "needsYou":
				t.Logf("screen:\n%s", screen())
				return "asked"
			case model.toolResults() != "" && x["agentState"] == "ready" && x["busy"] == false:
				return "refused: " + model.toolResults()
			}
			time.Sleep(250 * time.Millisecond)
		}
		return "nothing within 90 s"
	}
	waitIdle := func(t *testing.T, info func() map[string]any) {
		waitTrue(t, "back at its prompt", 60*time.Second, func() bool { x := info(); return x["agentState"] == "ready" && x["busy"] == false })
		time.Sleep(time.Second) // the turn's last request is in
	}

	t.Run("edits", func(t *testing.T) {
		id, screen, info := console(t, false)
		notes := filepath.Join(dir, "notes.txt")
		got := step(t, id, screen, info, 0, "Edit", map[string]any{"file_path": notes, "old_string": "alpha", "new_string": "beta"}, func() bool {
			b, _ := os.ReadFile(notes)
			return string(b) == "beta\n"
		})
		t.Logf("Edit in the project: %s", got)
		if got != "done without a question" {
			t.Fatalf("Edit in the project: %s", got)
		}
		waitIdle(t, info)
		far := filepath.Join(outside, "far.txt")
		got = step(t, id, screen, info, 1, "Write", map[string]any{"file_path": far, "content": "hi\n"}, func() bool { _, err := os.Stat(far); return err == nil })
		t.Logf("Write outside the project: %s", got)
		if got != "asked" {
			t.Fatalf("Write outside the project: %s, want a question as acceptEdits", got)
		}
		h.do("POST", "/api/sessions/"+id+"/kill", nil, nil) // the question stays unanswered
	})

	t.Run("yolo command without the key", func(t *testing.T) {
		id, screen, info := console(t, true)
		// the key is put together in the shell, so it is not in the command's own text
		cmd := `k="NBSCRUB"; k="sk-fake-$k-0123456789abcdef"; echo "RAN path=${PATH:+yes}"; ` +
			`env | grep -c -F "$k" | sed 's/^/ENV=/'; ` +
			`tr '\0' '\n' < /proc/self/environ | grep -c -F "$k" | sed 's/^/SELF=/'; ` +
			`tr '\0' '\n' < /proc/$PPID/environ 2>/dev/null | grep -c -F "$k" | sed 's/^/PARENT=/'; ` +
			`echo "ANY=$(grep -l -F "$k" /proc/[0-9]*/environ 2>/dev/null | wc -l)"; ` +
			`echo "VARS=$(env | grep -c -E '^ANTHROPIC_(AUTH_TOKEN|API_KEY)=.')"`
		got := step(t, id, screen, info, 2, "Bash", map[string]any{"command": cmd, "description": "show the environment"}, func() bool {
			return strings.Contains(model.toolResults(), "RAN")
		})
		t.Logf("Bash in a yolo console: %s", got)
		if got != "done without a question" {
			t.Fatalf("Bash in a yolo console: %s", got)
		}
		waitIdle(t, info)
		out := model.toolResults()
		t.Logf("its output: %s", out)
		if strings.Contains(out, key) {
			t.Fatal("the command saw the key")
		}
		for _, want := range []string{"RAN path=yes", "ENV=0", "SELF=0", "ANY=0", "VARS=0"} {
			if !strings.Contains(out, want) {
				t.Errorf("no %q in %s", want, out)
			}
		}
		if strings.Contains(out, "PARENT=") && !strings.Contains(out, "PARENT=0") {
			t.Errorf("its parent's environment holds the key: %s", out)
		}
	})

	model.mu.Lock()
	auth := append([]string{}, model.auth...)
	model.mu.Unlock()
	messages, keyed := 0, 0
	for _, a := range auth {
		if strings.Contains(a, "/messages") {
			messages++
			if strings.Contains(a, "Bearer "+key) {
				keyed++
			}
		}
	}
	t.Logf("requests to the model: %d to /v1/messages, %d with the key", messages, keyed)
	if messages == 0 || keyed != messages {
		t.Fatalf("the provider did not get the key on every request: %v", auth)
	}
}
