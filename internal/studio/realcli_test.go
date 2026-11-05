package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nextboard/internal/jobs"
	"nextboard/internal/machine"
	"nextboard/internal/store"
)

// TestRealDocumentJob generates ONE quick document with the real Claude
// Code (the owner's plan; --model haiku, read-only tools, no session
// persistence) in a tiny scratch repository:
//
//	NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_DOC=1 go test -run TestRealDocumentJob -v ./internal/studio/
func TestRealDocumentJob(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_CLI_TEST") != "1" || os.Getenv("NEXTBOARD_REAL_DOC") != "1" {
		t.Skip("set NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_DOC=1 untuk CLI asli")
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "CLAUDECODE" || k == "AI_AGENT" || k == "CLAUDE_PID" || k == "CLAUDE_EFFORT" ||
			strings.HasPrefix(k, "CLAUDE_CODE_") || strings.HasPrefix(k, "CLAUDE_AGENT_") {
			v := os.Getenv(k)
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	t.Setenv("DISABLE_AUTOUPDATER", "1")
	home, _ := os.UserHomeDir()
	settingsFile := filepath.Join(home, ".claude", "settings.json")
	sum := func() string {
		b, _ := os.ReadFile(settingsFile)
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	before := sum()
	repo := "/tmp/claude-1000/-home-alice-apps-nextboard/d854f76b-7046-4ae7-8525-705b3ef7f891/scratchpad/doc-real/tinyrepo"
	_ = os.MkdirAll(repo, 0o700)
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("# greet\n\nA tiny CLI that prints a greeting.\n"), 0o600)
	_ = os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n"), 0o600)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	local := machine.NewLocal(machine.Config{})
	jm := jobs.NewManager(st, func(string) (machine.Host, bool) { return local, true })
	defer jm.Close()
	ev := &evlog{}
	jm.Publish = ev.add
	d := NewDocuments(st)
	d.Submit = jm.SubmitRunner
	doc, vid, j, err := d.Generate("p_realdoc01", DocRequest{Type: "overview", Effort: "quick", Agent: "claude", Cwd: repo, Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(7 * time.Minute); ; time.Sleep(500 * time.Millisecond) {
		if v, _ := jm.Get(j.ID); !v.Active() || time.Now().After(deadline) {
			j = v
			break
		}
	}
	time.Sleep(200 * time.Millisecond)
	v, md, err := d.Version(doc.ID, vid)
	t.Logf("job state=%s code=%s err=%s elapsedMs=%d", j.State, j.Code, j.Error, j.ElapsedMs)
	t.Logf("version %+v (err %v), jobOutput chunks=%d", v, err, len(ev.of("jobOutput")))
	phases := map[string]bool{}
	for _, e := range ev.of("job") {
		phases[e["job"].(jobs.Job).Progress.Phase] = true
	}
	t.Logf("phases seen %v", phases)
	_ = os.WriteFile(filepath.Join(filepath.Dir(repo), "document.md"), []byte(md), 0o600)
	if j.State != jobs.StateDone || v.State != "complete" || !strings.HasPrefix(md, "# ") {
		t.Fatalf("document job failed: %+v", j)
	}
	if got := sum(); got != before {
		t.Errorf("~/.claude/settings.json changed")
	}
}
