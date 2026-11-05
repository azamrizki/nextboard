package transcript

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nextboard/internal/machine"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const wantClaudeReply = "## Done\n\nThe redirect now keeps the `next` parameter.\n\nRun `npm test` to confirm."

func TestParseClaudeReplyAfterLastToolResult(t *testing.T) {
	p := ParseClaude(fixture(t, "claude-text-tool-text.jsonl"), false)
	r := p.Reply
	if r == nil || r.Text != wantClaudeReply || r.MessageID != "msg_02B" || r.Paragraphs != 3 || r.Truncated {
		t.Fatalf("reply %+v", r)
	}
	if r.Chars != len([]rune(wantClaudeReply)) || !r.At.Equal(time.Date(2026, 10, 28, 10, 0, 7, 400e6, time.UTC)) {
		t.Fatalf("reply meta %+v", r)
	}
	if p.SessionID != "0b5e8c1a-1111-4222-8333-444455556666" || p.Cwd != "/home/alice/apps/demo" {
		t.Fatalf("ids %q %q", p.SessionID, p.Cwd)
	}
	// Usage: one entry per message.id (msg_01A spans three lines), latest
	// numbers, first timestamp; sidechain usage counts too.
	if len(p.Usage) != 3 || p.Usage[0].MessageID != "msg_01A" || p.Usage[1].MessageID != "msg_SIDE" || p.Usage[2].MessageID != "msg_02B" {
		t.Fatalf("usage %+v", p.Usage)
	}
	u := p.Usage[0]
	if u.Input != 1200 || u.Output != 340 || u.CacheRead != 50000 || u.CacheWrite != 900 || u.Model != "claude-opus-5-5" {
		t.Fatalf("usage 0 %+v", u)
	}
	if p.Usage[2].Output != 160 || !p.Usage[2].At.Equal(time.Date(2026, 10, 28, 10, 0, 7, 0, time.UTC)) {
		t.Fatalf("usage 2 %+v", p.Usage[2])
	}
	var roles []string
	for _, m := range p.Messages {
		roles = append(roles, m.Role)
		if strings.Contains(m.Text, "BLOB") {
			t.Fatal("api-request-blob line parsed as a message")
		}
	}
	if got := strings.Join(roles, ","); got != "user,assistant,tool,assistant,assistant,assistant" {
		t.Fatalf("roles %s", got)
	}
	tool := p.Messages[2]
	if tool.Tool != "Bash" || tool.Text != "grep -n redirect src/app.ts" || tool.ID != "toolu_01" {
		t.Fatalf("tool %+v", tool)
	}
	if !p.Messages[3].Sidechain || p.Messages[3].Text != "SIDECHAIN TEXT MUST NOT BE THE REPLY" {
		t.Fatalf("sidechain %+v", p.Messages[3])
	}
}

func TestParseClaudeTurnsAndTitle(t *testing.T) {
	data := fixture(t, "claude-turns.jsonl")
	p := ParseClaude(data, false)
	// Turn 3 has no answer yet: the newest answered turn wins.
	if p.Reply == nil || p.Reply.Text != "Answer two: fixed with a fake clock." || p.Reply.MessageID != "msg_T2" {
		t.Fatalf("reply %+v", p.Reply)
	}
	for _, u := range p.Usage {
		if u.Model == "<synthetic>" {
			t.Fatal("synthetic usage kept")
		}
	}
	if len(p.Usage) != 2 {
		t.Fatalf("usage %+v", p.Usage)
	}
	if n := countTurns(p.Messages); n != 5 {
		t.Fatalf("messages %d %+v", n, p.Messages)
	}
	if p.Messages[0].Text != "Why is the checkout test flaky?\nIt fails one run in ten." {
		t.Fatalf("slash command output listed as a prompt: %+v", p.Messages[0])
	}
	if got := claudeTitle(data); got != "Investigate flaky checkout test" {
		t.Fatalf("ai title %q", got)
	}
	custom := append([]byte(`{"type":"custom-title","customTitle":"Checkout flake","sessionId":"x"}`+"\n"), data...)
	if got := claudeTitle(custom); got != "Checkout flake" {
		t.Fatalf("custom title %q", got)
	}
	noTitle := bytes.SplitN(data, []byte("\n"), 2)[1]
	if got := claudeTitle(noTitle); got != "Why is the checkout test flaky? It fails one run in ten." {
		t.Fatalf("prompt title %q", got)
	}
}

func TestParseClaudeTailCutAnywhere(t *testing.T) {
	data := fixture(t, "claude-text-tool-text.jsonl")
	a4 := bytes.Index(data, []byte(`{"parentUuid":"u-0002","isSidechain":false`))
	a5 := bytes.Index(data, []byte(`{"parentUuid":"a-0004"`))
	if a4 < 0 || a5 < 0 {
		t.Fatal("fixture markers")
	}
	for k := 0; k < len(data); k++ {
		p := ParseClaude(data[k:], k > 0) // must never panic or fail
		switch {
		case k < a4:
			if p.Reply == nil || p.Reply.Text != wantClaudeReply {
				t.Fatalf("cut %d: reply %+v", k, p.Reply)
			}
		case k < a5:
			if p.Reply == nil || p.Reply.Text != "Run `npm test` to confirm." {
				t.Fatalf("cut %d: reply %+v", k, p.Reply)
			}
		}
	}
}

func TestParseCodexRollout(t *testing.T) {
	data := fixture(t, "codex-rollout.jsonl")
	p := ParseCodex(data, false)
	if p.SessionID != "01a16161-3072-7200-901b-c1b06d92ca7e" || p.Cwd != "/work/app" || p.Title != "Add a health endpoint to the server" {
		t.Fatalf("meta %q %q %q", p.SessionID, p.Cwd, p.Title)
	}
	if r := p.Reply; r == nil || r.Text != "Added `GET /health`.\n\nIt returns 200 with the build version." || r.MessageID != "msg_f1" || r.Paragraphs != 2 {
		t.Fatalf("reply %+v", p.Reply)
	}
	if len(p.Usage) != 2 || p.Usage[0].MessageID != "tok-1000" || p.Usage[1].MessageID != "tok-1500" {
		t.Fatalf("usage %+v", p.Usage)
	}
	if u := p.Usage[1]; u.Input != 400 || u.CacheRead != 800 || u.Output != 100 || u.Model != "gpt-5.5" {
		t.Fatalf("usage 1 %+v", u)
	}
	var roles []string
	for _, m := range p.Messages {
		roles = append(roles, m.Role)
	}
	if got := strings.Join(roles, ","); got != "user,assistant,tool,assistant" {
		t.Fatalf("roles %s %+v", got, p.Messages)
	}
	if m := p.Messages[2]; m.Tool != "shell" || m.Text != "bash -lc ls src" || m.ID != "call_1" {
		t.Fatalf("tool %+v", m)
	}
	// A rollout cut at any byte (mid-line, as the last line above) parses.
	for k := 0; k <= len(data); k++ {
		ParseCodex(data[:k], false)
		ParseCodex(data[k:], k > 0)
	}
	if id, cwd, started, ok := codexMeta(data); !ok || id != p.SessionID || cwd != "/work/app" || started.IsZero() {
		t.Fatalf("meta %v %q %q %v", ok, id, cwd, started)
	}
	// session_meta longer than the header: fields are scanned from the prefix.
	cut := bytes.SplitN(data, []byte("\n"), 2)[0]
	cut = cut[:bytes.Index(cut, []byte(`"originator"`))]
	if id, cwd, _, ok := codexMeta(cut); !ok || id != p.SessionID || cwd != "/work/app" {
		t.Fatalf("cut meta %v %q %q", ok, id, cwd)
	}
}

func TestParseCodexUsageRecords(t *testing.T) {
	p := ParseCodex(fixture(t, "codex-records.jsonl"), false)
	if len(p.Usage) != 1 || p.Usage[0].MessageID != "resp_abc" || p.Usage[0].CacheWrite != 2 || p.Usage[0].Model != "gpt-5.5" {
		t.Fatalf("usage %+v", p.Usage)
	}
	if p.Reply == nil || p.Reply.Text != "Hi!" {
		t.Fatalf("reply %+v", p.Reply)
	}
}

func TestReplyCap(t *testing.T) {
	long := strings.Repeat("é", MaxReplyBytes) // 2 bytes per rune
	r := makeReply(long, "m", time.Time{})
	if !r.Truncated || len(r.Text) > MaxReplyBytes || !strings.HasPrefix(long, r.Text) || r.Chars != MaxReplyBytes {
		t.Fatalf("cap len=%d chars=%d trunc=%v", len(r.Text), r.Chars, r.Truncated)
	}
	if makeReply("  \n ", "m", time.Time{}) != nil {
		t.Fatal("blank reply")
	}
}

func TestClaudeSlug(t *testing.T) {
	cases := map[string]string{
		"/home/alice/apps/nextboard":  "-home-alice-apps-nextboard",
		"/home/alice/.agent/w1": "-home-alice--agent-w1",
		"/srv/my_app-2.0":           "-srv-my-app-2-0",
		"/tmp/café":                 "-tmp-caf-", // one BMP rune → one '-'
		"/tmp/a😀b":                  "-tmp-a--b", // outside the BMP: two UTF-16 units
	}
	for in, want := range cases {
		if got := ClaudeSlug(in); got != want {
			t.Errorf("ClaudeSlug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ClaudePath("/home/alice/.claude", "/home/alice/apps/x/", "abc"); got != "/home/alice/.claude/projects/-home-alice-apps-x/abc.jsonl" {
		t.Fatal(got)
	}
	if ClaudeConfigDir("/home/u", "") != "/home/u/.claude" || ClaudeConfigDir("/home/u", "/p/w/") != "/p/w" ||
		CodexHome("/home/u", "") != "/home/u/.codex" || CodexHome("/home/u", "/c") != "/c" {
		t.Fatal("config dirs")
	}
}

func localHost() machine.Host { return machine.NewLocal(machine.Config{}) }

func write(t *testing.T, p string, data []byte, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadFromHost(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	dir := t.TempDir()
	cp := filepath.Join(dir, "c.jsonl")
	write(t, cp, fixture(t, "claude-text-tool-text.jsonl"), time.Time{})
	r, err := ReadReply(ctx, h, KindClaude, cp)
	if err != nil || r.Text != wantClaudeReply {
		t.Fatalf("reply %+v %v", r, err)
	}
	toks, err := ReadTokens(ctx, h, KindClaude, cp, time.Time{})
	if err != nil || len(toks) != 3 {
		t.Fatalf("tokens %+v %v", toks, err)
	}
	toks, _ = ReadTokens(ctx, h, KindClaude, cp, time.Date(2026, 10, 28, 10, 0, 6, 0, time.UTC))
	if len(toks) != 1 || toks[0].MessageID != "msg_02B" {
		t.Fatalf("tokens since %+v", toks)
	}
	msgs, trunc, err := ReadMessages(ctx, h, KindClaude, cp, 2)
	if err != nil || len(msgs) != 2 || !trunc || msgs[1].Text != "Run `npm test` to confirm." {
		t.Fatalf("last 2 %+v %v %v", msgs, trunc, err)
	}
	if msgs, trunc, _ := ReadMessages(ctx, h, KindClaude, cp, 500); len(msgs) != 6 || trunc {
		t.Fatalf("all %d %v", len(msgs), trunc)
	}
	xp := filepath.Join(dir, "x.jsonl")
	write(t, xp, fixture(t, "codex-rollout.jsonl"), time.Time{})
	if r, err := ReadReply(ctx, h, KindCodex, xp); err != nil || r.MessageID != "msg_f1" {
		t.Fatalf("codex reply %+v %v", r, err)
	}
	if _, err := ReadReply(ctx, h, "none", cp); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("kind %v", err)
	}
	if _, err := ReadReply(ctx, h, KindClaude, filepath.Join(dir, "missing.jsonl")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing %v", err)
	}
	empty := filepath.Join(dir, "e.jsonl")
	write(t, empty, []byte(`{"type":"queue-operation"}`+"\n"), time.Time{})
	if _, err := ReadReply(ctx, h, KindClaude, empty); !errors.Is(err, ErrNoReply) {
		t.Fatalf("no reply %v", err)
	}
}

// filler is n bytes of non-message Claude lines.
func filler(n int) []byte {
	line := []byte(`{"type":"progress","data":"` + strings.Repeat("x", 1000) + `"}` + "\n")
	return bytes.Repeat(line, n/len(line)+1)
}

func TestReadReplyGrowsTail(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	p := filepath.Join(t.TempDir(), "big.jsonl")
	// The reply sits 3 MiB before the end: found on the 4 MiB pass.
	data := append(fixture(t, "claude-text-tool-text.jsonl"), filler(3<<20)...)
	write(t, p, data, time.Time{})
	r, err := ReadReply(ctx, h, KindClaude, p)
	if err != nil || r.Text != wantClaudeReply {
		t.Fatalf("grown tail %+v %v", r, err)
	}
	// Beyond TailMax: not found.
	write(t, p, append(fixture(t, "claude-text-tool-text.jsonl"), filler(TailMax+1<<20)...), time.Time{})
	if _, err := ReadReply(ctx, h, KindClaude, p); !errors.Is(err, ErrNoReply) {
		t.Fatalf("beyond max %v", err)
	}
	// A tail starting exactly on a line boundary keeps that line.
	tail := fixture(t, "claude-text-tool-text.jsonl")
	a5 := bytes.Index(tail, []byte(`{"parentUuid":"a-0004"`))
	write(t, p, append(bytes.Repeat([]byte("\n"), TailStart), tail...), time.Time{})
	data2, partial, cut, err := readTail(ctx, h, p, int64(len(tail)-a5))
	if err != nil || !partial || cut || !bytes.HasPrefix(data2, []byte(`{"parentUuid":"a-0004"`)) {
		t.Fatalf("boundary %v %v %v %q", partial, cut, err, data2[:20])
	}
}

// codexRollout renders a minimal rollout for cwd.
func codexRollout(id, cwd, prompt string, at time.Time) []byte {
	ts := at.UTC().Format(time.RFC3339Nano)
	return []byte(fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"session_id":%q,"id":%q,"timestamp":%q,"cwd":%q,"cli_version":"0.163.0"}}
{"timestamp":%q,"type":"response_item","payload":{"type":"message","id":"u0","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]}}
{"timestamp":%q,"type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":%q}]}}
{"timestamp":%q,"type":"response_item","payload":{"type":"message","id":"a1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}
`, ts, id, id, ts, cwd, ts, ts, prompt, ts))
}

func rolloutName(at time.Time, id string) string {
	return "rollout-" + at.UTC().Format("2006-01-02T15-04-05") + "-" + id + ".jsonl"
}

func TestFindCodex(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	home := t.TempDir()
	start := time.Now().UTC().Truncate(time.Second)
	day := codexDayDir(home, start)
	yday := codexDayDir(home, start.Add(-24*time.Hour))
	idA, idB, idC, idD := "aaaaaaaa-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002", "cccccccc-0000-0000-0000-000000000003", "dddddddd-0000-0000-0000-000000000004"
	pA := filepath.Join(day, rolloutName(start, idA))
	write(t, pA, codexRollout(idA, "/work/app", "first", start), start.Add(time.Second))
	write(t, filepath.Join(day, rolloutName(start, idB)), codexRollout(idB, "/work/other", "x", start), start.Add(time.Second))
	write(t, filepath.Join(day, rolloutName(start.Add(-time.Hour), idC)), codexRollout(idC, "/work/app", "old", start.Add(-time.Hour)), start.Add(-time.Hour))
	p, id, err := FindCodex(ctx, h, home, "/work/app/", start, nil)
	if err != nil || p != pA || id != idA {
		t.Fatalf("find %q %q %v", p, id, err)
	}
	// A second console in the same cwd: ambiguous until notify.
	pD := filepath.Join(yday, rolloutName(start, idD))
	write(t, pD, codexRollout(idD, "/work/app", "second", start), start.Add(2*time.Second))
	if p, _, _ := FindCodex(ctx, h, home, "/work/app", start, nil); p != "" {
		t.Fatalf("ambiguous should be empty, got %q", p)
	}
	if p, id, _ := FindCodex(ctx, h, home, "/work/app", start, func(c string) bool { return c == pA }); p != pD || id != idD {
		t.Fatalf("claimed skip %q %q", p, id)
	}
	if p, _, err := FindCodex(ctx, h, filepath.Join(home, "nope"), "/work/app", start, nil); p != "" || err != nil {
		t.Fatalf("no sessions dir %q %v", p, err)
	}

	if p, err := CodexPathByID(ctx, h, home, idD, start); err != nil || p != pD {
		t.Fatalf("by id near %q %v", p, err)
	}
	old := time.Date(2025, 1, 15, 8, 0, 0, 0, time.UTC)
	idE := "eeeeeeee-0000-0000-0000-000000000005"
	pE := filepath.Join(codexDayDir(home, old), rolloutName(old, idE))
	write(t, pE, codexRollout(idE, "/work/app", "ancient", old), old)
	if p, err := CodexPathByID(ctx, h, home, idE, start); err != nil || p != pE {
		t.Fatalf("by id fallback %q %v", p, err)
	}
	if p, _ := CodexPathByID(ctx, h, home, "ffffffff-0000-0000-0000-000000000009", start); p != "" {
		t.Fatalf("unknown id %q", p)
	}
	if _, err := CodexPathByID(ctx, h, home, "../etc", start); !errors.Is(err, ErrBadID) {
		t.Fatalf("bad id %v", err)
	}
}

func TestFindClaude(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	cfg := t.TempDir()
	id := "0b5e8c1a-1111-4222-8333-444455556666"
	p := ClaudePath(cfg, "/home/alice/apps/demo", id)
	write(t, p, fixture(t, "claude-text-tool-text.jsonl"), time.Time{})
	if got, err := FindClaude(ctx, h, cfg, "/home/alice/apps/demo", id); err != nil || got != p {
		t.Fatalf("slug %q %v", got, err)
	}
	// Shortened slug folder: found by the scan.
	other := filepath.Join(cfg, "projects", "-home-alice-apps-de-1a2b3c", "1111aaaa-2222-3333-4444-555566667777.jsonl")
	write(t, other, []byte("{}\n"), time.Time{})
	if got, _ := FindClaude(ctx, h, cfg, "/home/alice/apps/demo-with-a-very-long-name", "1111aaaa-2222-3333-4444-555566667777"); got != other {
		t.Fatalf("scan %q", got)
	}
	if got, err := FindClaude(ctx, h, cfg, "/x", "99999999-0000-0000-0000-000000000000"); got != "" || err != nil {
		t.Fatalf("missing %q %v", got, err)
	}
	if got, err := FindClaude(ctx, h, filepath.Join(cfg, "none"), "/x", id); got != "" || err != nil {
		t.Fatalf("no projects %q %v", got, err)
	}
	if _, err := FindClaude(ctx, h, cfg, "/x", "a/../b"); !errors.Is(err, ErrBadID) {
		t.Fatalf("bad id %v", err)
	}
}

func TestListConversations(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	base := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

	cfg := t.TempDir()
	cwd := "/home/alice/apps/demo"
	ids := []string{"11111111-0000-0000-0000-000000000001", "22222222-0000-0000-0000-000000000002", "33333333-0000-0000-0000-000000000003"}
	write(t, ClaudePath(cfg, cwd, ids[0]), fixture(t, "claude-text-tool-text.jsonl"), base.Add(1*time.Hour))
	write(t, ClaudePath(cfg, cwd, ids[1]), fixture(t, "claude-turns.jsonl"), base.Add(3*time.Hour))
	write(t, ClaudePath(cfg, cwd, ids[2]), []byte(`{"type":"user","message":{"role":"user","content":"Third"},"uuid":"x","timestamp":"2026-10-20T00:00:00Z"}`+"\n"), base.Add(2*time.Hour))
	write(t, filepath.Join(cfg, "projects", ClaudeSlug(cwd), "notes.txt"), []byte("x"), base)
	attached := func(id string) string {
		if id == ids[1] {
			return "s_abc"
		}
		return ""
	}
	l, err := ListConversations(ctx, h, KindClaude, cfg, cwd, 0, attached)
	if err != nil || len(l.Items) != 3 || l.Scanned != 3 || l.Truncated {
		t.Fatalf("claude list %+v %v", l, err)
	}
	if l.Items[0].AgentSessionID != ids[1] || l.Items[1].AgentSessionID != ids[2] || l.Items[2].AgentSessionID != ids[0] {
		t.Fatalf("order %+v", l.Items)
	}
	c := l.Items[0]
	if c.Title != "Investigate flaky checkout test" || c.AttachedTo == nil || *c.AttachedTo != "s_abc" || c.Messages != 5 ||
		!c.UpdatedAt.Equal(base.Add(3*time.Hour)) || c.SizeBytes == 0 || c.StartedAt.IsZero() {
		t.Fatalf("item %+v", c)
	}
	if l.Items[2].Title != "Fix the login redirect in app.ts" || l.Items[2].AttachedTo != nil || l.Items[2].Messages != 4 {
		t.Fatalf("item 2 %+v", l.Items[2])
	}
	if l, _ := ListConversations(ctx, h, KindClaude, cfg, cwd, 2, nil); len(l.Items) != 2 || !l.Truncated {
		t.Fatalf("limit %+v", l)
	}
	if l, err := ListConversations(ctx, h, KindClaude, cfg, "/elsewhere", 10, nil); err != nil || len(l.Items) != 0 || l.Items == nil {
		t.Fatalf("empty %+v %v", l, err)
	}

	home := t.TempDir()
	for i, x := range []struct {
		id, cwd, prompt string
		at              time.Time
	}{
		{"aaaaaaaa-0000-0000-0000-00000000000a", "/work/app", "Old task", base.Add(-48 * time.Hour)},
		{"bbbbbbbb-0000-0000-0000-00000000000b", "/work/other", "Other repo", base},
		{"cccccccc-0000-0000-0000-00000000000c", "/work/app", "New task", base.Add(time.Hour)},
	} {
		write(t, filepath.Join(codexDayDir(home, x.at), rolloutName(x.at, x.id)), codexRollout(x.id, x.cwd, x.prompt, x.at), x.at.Add(time.Duration(i)*time.Minute))
	}
	l, err = ListConversations(ctx, h, KindCodex, home, "/work/app", 50, func(id string) string {
		if strings.HasPrefix(id, "aaaa") {
			return "s_old"
		}
		return ""
	})
	if err != nil || len(l.Items) != 2 || l.Scanned != 3 {
		t.Fatalf("codex list %+v %v", l, err)
	}
	if l.Items[0].Title != "New task" || l.Items[1].Title != "Old task" || l.Items[1].AttachedTo == nil || l.Items[0].Messages != 2 ||
		!l.Items[0].StartedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("codex items %+v", l.Items)
	}
	if _, err := ListConversations(ctx, h, "none", home, "/w", 1, nil); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("kind %v", err)
	}
}
