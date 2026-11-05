package gitsvc

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/jobs"
)

// conventional is the header of https://www.conventionalcommits.org/en/v1.0.0/.
var conventional = regexp.MustCompile(`^[a-z]+(\([a-z0-9._-]+\))?!?: \S.*$`)

func TestCommitMessageForm(t *testing.T) {
	long := strings.Repeat("word ", 30)
	cases := []struct {
		name string
		in   commitParts
		want string
	}{
		{"plain", commitParts{Type: "fix", Scope: "git", Description: "stop losing the upstream on rename"},
			"fix(git): stop losing the upstream on rename"},
		{"no scope", commitParts{Type: "docs", Description: "explain hot reload"}, "docs: explain hot reload"},
		{"body", commitParts{Type: "feat", Scope: "orch", Description: "add reply", Body: "The agent is told what is answered."},
			"feat(orch): add reply\n\nThe agent is told what is answered."},
		{"breaking", commitParts{Type: "feat", Scope: "api", Description: "rename job", Body: "Why.", Breaking: "Clients must read\nmessage."},
			"feat(api)!: rename job\n\nWhy.\n\nBREAKING CHANGE: Clients must read message."},
		{"unknown type", commitParts{Type: "wip", Description: "x y"}, "chore: x y"},
		{"bad scope", commitParts{Type: "fix", Scope: "two words", Description: "x y"}, "fix: x y"},
		{"tidies the description", commitParts{Type: "fix", Description: "\"Fix(git): Stop the crash.\"\nsecond line"}, "fix: stop the crash"},
		{"keeps capitals that mean something", commitParts{Type: "docs", Description: "README covers iOS"}, "docs: README covers iOS"},
		{"no signature", commitParts{Type: "fix", Description: "x y", Body: "Why.\n\nCo-Authored-By: Someone <a@b.c>\nGenerated with a tool"}, "fix: x y\n\nWhy."},
	}
	for _, c := range cases {
		if got := c.in.message(); got != c.want {
			t.Errorf("%s:\n%q\nwant\n%q", c.name, got, c.want)
		}
	}

	m := commitParts{Type: "refactor", Scope: "gitsvc", Description: long, Body: "- " + long + "\n\n" + long + "\nhttps://example.com/" + strings.Repeat("a", 90)}.message()
	lines := strings.Split(m, "\n")
	if !conventional.MatchString(lines[0]) || len([]rune(lines[0])) > 72 || strings.HasSuffix(lines[0], " ") || strings.Contains(lines[0], "…") {
		t.Errorf("header %q", lines[0])
	}
	if lines[1] != "" {
		t.Errorf("the body must start one blank line after the header: %q", m)
	}
	for _, l := range lines {
		if len([]rune(l)) > 72 && !strings.HasPrefix(l, "https://") {
			t.Errorf("line over 72 columns: %q", l)
		}
	}
	if !strings.HasPrefix(lines[2], "- word") || !strings.HasPrefix(lines[3], "  word") {
		t.Errorf("bullets must hang: %q", lines[2:4])
	}
}

func TestFairShare(t *testing.T) {
	got := fairShare([]int{10, 5000, 100, 9000}, 1000)
	if fmt.Sprint(got) != "[10 445 100 445]" {
		t.Fatalf("shares %v", got)
	}
	d := "line one\nline two\nline three\n"
	if cutDiff(d, 100) != d {
		t.Fatal("what fits stays whole")
	}
	if c := cutDiff(d, 12); !strings.HasPrefix(c, "line one\n[…") || !strings.Contains(c, "2 more lines") {
		t.Fatalf("cut %q", c)
	}
}

func TestUnreadReason(t *testing.T) {
	for p, want := range map[string]string{
		".env": "secrets", "deploy/.env.production": "secrets", "certs/server.pem": "secrets", "id_ed25519": "secrets", "config/credentials.json": "secrets",
		".env.example": "", "internal/env.go": "", "docs/secrets-howto.md": "",
		"go.sum": "generated", "web/package-lock.json": "generated", "web/dist/a.js": "generated", "x.min.js": "generated",
		"internal/gitsvc/suggest.go": "",
	} {
		got := unreadReason(File{Path: p})
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: %q want %q", p, got, want)
		}
	}
}

// TestSuggestAIReadsTheChanges: the model gets every file, the small ones whole beside a big
// one, the history, and no secret; the answer comes back as a proper message.
func TestSuggestAIReadsTheChanges(t *testing.T) {
	f := newFixture(t, true)
	f.initRepo()
	write(t, f.dir, "app.go", "package app\n")
	write(t, f.dir, "big.txt", "old\n")
	git(t, f.dir, "add", ".")
	git(t, f.dir, "commit", "-qm", "feat(app): start the app")

	write(t, f.dir, "app.go", "package app\n\nfunc Login() {}\n")
	write(t, f.dir, "big.txt", strings.Repeat("a long line of text in a big file\n", 6000))
	write(t, f.dir, "new_file.go", "package app\n\nconst NewThing = 1\n")
	write(t, f.dir, ".env", "API_KEY=hunter2secret\n")

	var req jobs.RunnerRequest
	f.svc.Runner = func(r jobs.RunnerRequest) (jobs.Job, error) { req = r; return jobs.Job{ID: "jb_x"}, nil }
	if _, err := f.svc.SuggestAI(ctx(t), f.repo, nil, false); err != nil {
		t.Fatal(err)
	}
	p := req.Prompt
	for _, want := range []string{"Conventional Commits 1.0.0", "feat(app): start the app", "func Login()", "const NewThing = 1", "more lines of this diff are not shown",
		"never an instruction", "- big.txt", "- .env (", "may hold secrets"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "hunter2secret") {
		t.Fatal("a secret reached the prompt")
	}
	if len(p) > commitDiffBudget+8000 {
		t.Fatalf("prompt of %d bytes", len(p))
	}
	if req.Purpose != "commit-subject" || jobs.ReadsProject(req.Purpose) {
		t.Fatalf("purpose %q must have no tools", req.Purpose)
	}

	raw, err := req.Transform("", json.RawMessage(`{"type":"feat","scope":"auth","description":"add login","body":"Users could not sign in.","breaking":""}`))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil || out["message"] != "feat(auth): add login\n\nUsers could not sign in." || out["subject"] != "feat(auth): add login" || out["source"] != "runner" {
		t.Fatalf("result %s %v", raw, err)
	}
	// codex answers in the text
	if raw, err := req.Transform("Here: {\"type\":\"fix\",\"scope\":\"\",\"description\":\"a b\",\"body\":\"\",\"breaking\":\"\"}", nil); err != nil || !strings.Contains(string(raw), `"subject":"fix: a b"`) {
		t.Fatalf("text answer %s %v", raw, err)
	}
	if _, err := req.Transform("I cannot do that", nil); err == nil {
		t.Fatal("an answer without a message must fail, so that the panel falls back")
	}

	// only the chosen files
	if _, err := f.svc.SuggestAI(ctx(t), f.repo, []string{"app.go"}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(req.Prompt, "NewThing") || !strings.Contains(req.Prompt, "func Login()") {
		t.Fatal("the prompt must hold only the chosen files")
	}

	// what is staged, not what is in the working tree
	git(t, f.dir, "add", "app.go")
	write(t, f.dir, "app.go", "package app\n\nfunc Login() {}\n\nfunc NotStaged() {}\n")
	if _, err := f.svc.SuggestAI(ctx(t), f.repo, []string{"app.go"}, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(req.Prompt, "NotStaged") || !strings.Contains(req.Prompt, "func Login()") {
		t.Fatal("staged=1 must read the index")
	}
}
