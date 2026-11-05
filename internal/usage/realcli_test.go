package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nextboard/internal/jobs"
	"nextboard/internal/machine"
	"nextboard/internal/store"
)

// TestRealUsageCapture runs ONE real usage check per CLI on This PC (the
// owner's plan; /usage and /status do not send a model prompt) and saves
// every distinct screen under the scratchpad plus the final screen as a
// parser fixture:
//
//	NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_USAGE=1 NEXTBOARD_REAL_CLI_AGENT=claude \
//	  go test -run TestRealUsageCapture -v ./internal/usage/
func TestRealUsageCapture(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_CLI_TEST") != "1" || os.Getenv("NEXTBOARD_REAL_USAGE") != "1" {
		t.Skip("set NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_USAGE=1 untuk CLI asli")
	}
	agent := os.Getenv("NEXTBOARD_REAL_CLI_AGENT")
	if agent != "claude" && agent != "codex" {
		t.Fatal("NEXTBOARD_REAL_CLI_AGENT harus claude atau codex")
	}
	// Variables of the Claude Code process running this test must not
	// reach the checked CLI (a hub started as a service has none).
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "CLAUDECODE" || k == "AI_AGENT" || k == "CLAUDE_PID" || k == "CLAUDE_EFFORT" ||
			strings.HasPrefix(k, "CLAUDE_CODE_") || strings.HasPrefix(k, "CLAUDE_AGENT_") {
			v := os.Getenv(k)
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	home, _ := os.UserHomeDir()
	sum := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return "missing"
		}
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	watched := []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "config.toml")}
	before := map[string]string{}
	for _, p := range watched {
		before[p] = sum(p)
	}
	out := "/tmp/claude-1000/-home-alice-apps-nextboard/d854f76b-7046-4ae7-8525-705b3ef7f891/scratchpad/usage-real/" + agent
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := &fakeHub{local: machine.NewLocal(machine.Config{}), settings: Settings{Background: true}, command: map[string]string{}}
	jm := jobs.NewManager(st, func(id string) (machine.Host, bool) { return hub.Host(id) })
	defer jm.Close()
	svc := New(st, jm, hub, nil)
	defer svc.Close()
	var mu sync.Mutex
	n := 0
	svc.OnScreen = func(_ string, step string, lines []string) {
		mu.Lock()
		defer mu.Unlock()
		n++
		_ = os.WriteFile(filepath.Join(out, fmt.Sprintf("%02d-%s.txt", n, step)), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	}
	zero := 0
	_, j, err := svc.Check(CheckRequest{Agent: agent, MaxAgeSec: &zero, Trigger: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var v jobs.Job
	for deadline := time.Now().Add(2 * time.Minute); ; {
		v, _ = jm.Get(j.ID)
		if !v.Active() || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("job %s %s %s", v.State, v.Code, v.Error)
	t.Logf("result %s", v.Result)
	reps := svc.Reports("", agent, "", false)
	if len(reps) == 1 {
		_ = os.WriteFile(filepath.Join(out, "final.txt"), []byte(strings.Join(reps[0].Raw, "\n")+"\n"), 0o600)
	}
	for _, p := range watched {
		if got := sum(p); got != before[p] {
			// Codex 0.165 writes its screen-reader marker once (see
			// cliSpec "codex"); the owner restores the file by hand.
			t.Logf("WARNING %s changed by the CLI (%s → %s)", p, before[p], got)
		}
	}
	if ents, _ := filepath.Glob("/tmp/nb-usage-*"); len(ents) != 0 {
		t.Errorf("usage folder left: %v", ents)
	}
	if p := svc.Pending(); len(p) != 0 {
		t.Errorf("pending %v", p)
	}
	if ents, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "-tmp-nb-usage-*", "*")); len(ents) > 0 {
		t.Logf("Claude transcript files for the usage folder: %v", ents)
	}
}
