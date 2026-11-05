package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"nextboard/internal/machine"
	"nextboard/internal/store"
)

// argAfter returns the value following flag in argv.
func argAfter(argv []string, flag string) (string, bool) {
	i := slices.Index(argv, flag)
	if i < 0 || i+1 >= len(argv) {
		return "", false
	}
	return argv[i+1], true
}

// TestRunnerArgvNeverUnsafe covers every purpose × agent × option: no yolo,
// no hooks, no --bare, and the tool policy of the purpose.
func TestRunnerArgvNeverUnsafe(t *testing.T) {
	banned := []string{"dangerously", "--settings", "--bare", "yolo", "bypassPermissions", "--approve-for-me", "danger-full-access", "acceptEdits"}
	for purpose, pol := range purposes {
		for _, agent := range []string{"claude", "codex"} {
			for _, schema := range []json.RawMessage{nil, json.RawMessage(`{"type":"object"}`)} {
				for _, stream := range []bool{false, true} {
					req := RunnerRequest{Purpose: purpose, Agent: agent, Cwd: "/repo", Schema: schema, Stream: stream, Model: "m1"}
					argv := RunnerArgv(req)
					script := runnerScript(req)
					for _, a := range append(argv, script) {
						for _, b := range banned {
							if strings.Contains(a, b) {
								t.Fatalf("%s/%s: %q contains %q", purpose, agent, a, b)
							}
						}
					}
					if argv[0] != agent {
						t.Fatalf("argv0 %v", argv)
					}
					if agent == "codex" {
						if v, _ := argAfter(argv, "--sandbox"); v != "read-only" || argv[len(argv)-1] != "-" || !slices.Contains(argv, "--ephemeral") {
							t.Fatalf("codex %v", argv)
						}
						continue
					}
					if v, _ := argAfter(argv, "--permission-mode"); v != "dontAsk" || !slices.Contains(argv, "-p") || !slices.Contains(argv, "--no-session-persistence") {
						t.Fatalf("claude mode %v", argv)
					}
					tools, ok := argAfter(argv, "--tools")
					allowed, hasAllowed := argAfter(argv, "--allowedTools")
					turns, hasTurns := argAfter(argv, "--max-turns")
					switch {
					case !ok:
						t.Fatalf("%s: no --tools in %v", purpose, argv)
					case pol.readOnly && (tools != "Read,Glob,Grep" || allowed != "Read,Glob,Grep" || hasTurns):
						t.Fatalf("%s read-only %v", purpose, argv)
					case !pol.readOnly && (tools != "" || hasAllowed || !hasTurns || turns != "1"):
						t.Fatalf("%s no-tools %v", purpose, argv)
					}
					if stream != slices.Contains(argv, "--verbose") {
						t.Fatalf("stream %v", argv)
					}
				}
			}
		}
	}
}

// TestRunnerScriptTempFolder: only the mktemp folder is ever removed.
func TestRunnerScriptTempFolder(t *testing.T) {
	s := runnerScript(RunnerRequest{Purpose: "diagram", Agent: "codex", Schema: json.RawMessage(`{"a":"it's"}`)})
	if strings.Count(s, "rm ") != 1 || !strings.Contains(s, `rm -rf -- "$d"`) ||
		!strings.Contains(s, `mktemp -d /tmp/nb-run.XXXXXX`) || !strings.Contains(s, `case "$d" in /tmp/nb-run.??????)`) ||
		!strings.Contains(s, `--output-schema "$d/schema.json"`) {
		t.Fatalf("script %s", s)
	}
	if s := runnerScript(RunnerRequest{Purpose: "diagram", Agent: "claude", Schema: json.RawMessage(`{}`)}); strings.Contains(s, "rm ") || strings.Contains(s, "mktemp") {
		t.Fatalf("claude script %s", s)
	}
}

// TestRealCLIRunner runs the real CLIs once each (uses the owner's plan):
//
//	NEXTBOARD_REAL_CLI_TEST=1 go test -race -run TestRealCLIRunner -v ./internal/jobs/
func TestRealCLIRunner(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_CLI_TEST") != "1" {
		t.Skip("set NEXTBOARD_REAL_CLI_TEST=1 untuk menjalankan CLI asli (memakai plan)")
	}
	base := "/tmp/claude-1000/-home-alice-apps-nextboard/d854f76b-7046-4ae7-8525-705b3ef7f891/scratchpad/runner-real"
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.MkdirTemp(base, "run-")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	local := machine.NewLocal(machine.Config{})
	m := NewManager(st, func(string) (machine.Host, bool) { return local, true })
	defer m.Close()
	var ev events
	m.Publish = ev.add
	agents := []string{"claude", "codex"}
	if a := os.Getenv("NEXTBOARD_REAL_CLI_AGENT"); a != "" {
		agents = []string{a}
	}
	for _, agent := range agents {
		req := RunnerRequest{Purpose: "commit-subject", Agent: agent, Cwd: cwd, Prompt: "Reply with the single word OK.", Stream: true,
			Timeout: 3 * time.Minute, IdleTimeout: 2 * time.Minute}
		if agent == "claude" {
			req.Model = "haiku"
		}
		t.Logf("%s argv: %q", agent, RunnerArgv(req))
		j, err := m.SubmitRunner(req)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		for ctx.Err() == nil {
			if j, _ = m.Get(j.ID); !j.Active() {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		cancel()
		m.mu.Lock()
		types := m.jobs[j.ID].types
		m.mu.Unlock()
		var phases []string
		for _, e := range ev.of("job", j.ID) {
			p := e["job"].(Job).Progress.Phase
			if len(phases) == 0 || phases[len(phases)-1] != p {
				phases = append(phases, p)
			}
		}
		t.Logf("%s: state=%s code=%q error=%q elapsed=%dms", agent, j.State, j.Code, j.Error, j.ElapsedMs)
		t.Logf("%s: result=%s", agent, j.Result)
		t.Logf("%s: event types=%v", agent, types)
		t.Logf("%s: phases=%v outputs=%d", agent, phases, len(ev.of("jobOutput", j.ID)))
		if j.State != StateDone {
			t.Errorf("%s did not finish: %+v", agent, j)
		}
	}
	ents, _ := filepath.Glob(filepath.Join(cwd, "*"))
	t.Logf("files in cwd after runs: %v", ents)
}
