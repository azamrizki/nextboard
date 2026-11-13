package server

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestRealSleepWake puts the installed Claude Code and Codex to sleep with a draft at their
// prompt and wakes them by typing (ROADMAP #50): the conversation resumes, the draft comes back
// before the new keys, and Return sends both. They talk to a model on loopback through an API
// provider from an empty home, so no account is involved.
//
//	NEXTBOARD_REAL_SLEEP_TEST=1 go test ./internal/server -run TestRealSleepWake -v
//
// Return is pressed only to send a line at the prompt, or on a question whether to trust the
// test's own empty folder; any other dialog (an update offer) fails the test untouched.
func TestRealSleepWake(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_SLEEP_TEST") != "1" {
		t.Skip("set NEXTBOARD_REAL_SLEEP_TEST=1 (installed CLIs, a model on loopback, no account)")
	}
	t.Setenv("HOME", t.TempDir()) // nothing of the owner's logins or settings is read
	// nor anything the shell that runs the test has for these CLIs (a Claude Code session sets its own)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		for _, pre := range []string{"CLAUDE", "ANTHROPIC", "CODEX", "OPENAI"} {
			if strings.HasPrefix(k, pre) {
				t.Setenv(k, "")
				os.Unsetenv(k)
			}
		}
	}
	for _, c := range []struct{ agent, kind, api string }{
		{"claude", "anthropic", ""},
		{"codex", "openai", "responses"},
	} {
		t.Run(c.agent, func(t *testing.T) {
			if _, err := exec.LookPath(c.agent); err != nil {
				t.Skip("not installed")
			}
			model := newFakeModel(t)
			h := newTestHub(t)
			h.setup()
			dir := t.TempDir()
			st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "real-" + c.agent}, nil)
			if st != 201 {
				t.Fatalf("project %d %v", st, p)
			}
			wid := p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
			body := map[string]any{"name": "Loopback", "kind": c.kind, "baseUrl": model.URL + "/v1", "model": "fake-model", "apiKey": "not-a-real-key"}
			if c.api != "" {
				body["api"] = c.api
			}
			st, pv := h.do("POST", "/api/providers", body, nil)
			if st != 201 {
				t.Fatalf("provider %d %v", st, pv)
			}
			st, in := h.do("POST", "/api/sessions", map[string]any{"agent": c.agent, "workspaceId": wid, "providerId": pv["id"]}, nil)
			if st != 201 {
				t.Fatalf("console %d %v", st, in)
			}
			id := in["id"].(string)
			sess, _ := h.srv.sessions.Get(id)
			screenText := func() string { return strings.Join(sess.Screen().Lines, "\n") }
			info := func() map[string]any { _, x := h.do("GET", "/api/sessions/"+id, nil, nil); return x }
			// Its prompt, past a question whether to trust the test's own folder. The "yes" is
			// picked by name and Return is pressed only with the marker on it: Claude Code's
			// default is "No, exit".
			yes := map[string]string{"claude": "Yes, I trust this folder", "codex": "Yes, allow"}[c.agent]
			input := func(s string) { h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": s}, nil) }
			marked := func() bool {
				for _, l := range sess.Screen().Lines {
					if strings.Contains(l, yes) && strings.ContainsAny(l, "❯›>") {
						return true
					}
				}
				return false
			}
			reach := func(what string) {
				t.Helper()
				trusted := false
				deadline := time.Now().Add(90 * time.Second)
				for time.Now().Before(deadline) {
					x := info()
					if x["agentState"] == "ready" && x["busy"] == false {
						return
					}
					if !trusted && strings.Contains(screenText(), yes) {
						for i := 0; i < 4 && !marked(); i++ {
							input("\x1b[B")
							time.Sleep(300 * time.Millisecond)
						}
						if !marked() {
							t.Fatalf("%s: cannot select %q:\n%s", what, yes, screenText())
						}
						trusted = true
						input("\r")
						time.Sleep(time.Second)
						continue
					}
					if x["agentState"] == "needsYou" && time.Until(deadline) < 80*time.Second {
						t.Fatalf("%s: a dialog the test does not answer:\n%s", what, screenText())
					}
					time.Sleep(200 * time.Millisecond)
				}
				t.Fatalf("%s: not at its prompt; %v\n%s", what, info(), screenText())
			}
			reach("start")

			// one turn, so there is a conversation to resume
			model.take()
			if st, d := h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "first turn"}, nil); st != 202 {
				t.Fatalf("deliver %d %v", st, d)
			}
			waitTrue(t, "the model got the first turn", 60*time.Second, func() bool { return strings.Contains(model.take(), "first turn") })
			reach("after the first turn")
			conv := info()["agentSessionId"]

			// a draft at the prompt, then sleep
			h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "draft for later"}, nil)
			for end := time.Now().Add(10 * time.Second); !strings.Contains(screenText(), "draft for later"); time.Sleep(100 * time.Millisecond) {
				if time.Now().After(end) {
					t.Fatalf("draft not on screen; %v\n%s", info(), screenText())
				}
			}
			reach("with a draft")
			if st, b := h.do("POST", "/api/sessions/"+id+"/sleep", nil, nil); st != 200 {
				t.Fatalf("sleep %d %v\n%s", st, b, screenText())
			}
			h.waitInfo(id, "asleep", asleep)
			waitTrue(t, "its process gone", 10*time.Second, func() bool { return !cliRunning(c.agent, conv) })

			// typing wakes it: the same conversation, the draft first, then the keys
			model.take()
			h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": " plus more"}, nil)
			reach("woken")
			if got := info()["agentSessionId"]; got != conv {
				t.Fatalf("another conversation: %v, was %v", got, conv)
			}
			waitTrue(t, "draft and keys at its prompt", 20*time.Second, func() bool {
				return strings.Contains(withoutSpaces(screenText()), "draftforlaterplusmore")
			})
			if x := info(); x["unsentDraft"] != nil {
				t.Fatalf("draft not put back: %v", x["unsentDraft"])
			}
			h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "\r"}, nil)
			var sent string
			waitTrue(t, "the model got the draft", 60*time.Second, func() bool {
				sent += model.take()
				return strings.Contains(sent, "draft for later plus more")
			})
			// the resumed conversation carries the first turn
			if !strings.Contains(sent, "first turn") {
				t.Fatalf("resumed without its conversation:\n%.2000s", sent)
			}
			h.do("POST", "/api/sessions/"+id+"/kill", nil, nil)
		})
	}
}

// cliRunning reports whether a process of the CLI with this conversation id runs.
func cliRunning(cli string, conv any) bool {
	id, _ := conv.(string)
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		cl := strings.ReplaceAll(string(b), "\x00", " ")
		if strings.Contains(cl, cli) && id != "" && strings.Contains(cl, id) {
			return true
		}
	}
	return false
}

func withoutSpaces(s string) string { return strings.Join(strings.Fields(s), "") }
