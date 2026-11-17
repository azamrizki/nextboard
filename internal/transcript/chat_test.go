package transcript

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claudeTurn is one user prompt and its answer as Claude Code writes them.
func claudeTurn(i int, at time.Time) string {
	ts := at.UTC().Format(time.RFC3339Nano)
	return fmt.Sprintf(`{"type":"user","uuid":"u-%d","timestamp":%q,"isSidechain":false,"message":{"role":"user","content":"prompt number %d"}}
{"type":"assistant","uuid":"a-%d","timestamp":%q,"isSidechain":false,"message":{"id":"msg_%d","role":"assistant","content":[{"type":"text","text":"answer %d"}]}}
`, i, ts, i, i, ts, i, i)
}

func appendFile(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestDescribeAndCountAfter(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	dir := t.TempDir()
	p := filepath.Join(dir, "0d7e5b1a-1111-4222-8333-444455556666.jsonl")
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	appendFile(t, p, claudeTurn(1, at)+claudeTurn(2, at.Add(time.Minute)))
	c, err := Describe(ctx, h, KindClaude, p)
	if err != nil || c.AgentSessionID != "0d7e5b1a-1111-4222-8333-444455556666" || c.Title != "prompt number 1" || c.Messages != 4 || !c.StartedAt.Equal(at) {
		t.Fatalf("describe %+v %v", c, err)
	}
	// a first count has no mark: everything read
	n, err := CountAfter(ctx, h, KindClaude, p, Mark{})
	if err != nil || n.Since || n.New != 4 || n.Partial || n.Next.ID != "a-2" {
		t.Fatalf("first %+v %v", n, err)
	}
	// the next turn: only what came after the mark
	appendFile(t, p, claudeTurn(3, at.Add(2*time.Minute)))
	n2, err := CountAfter(ctx, h, KindClaude, p, n.Next)
	if err != nil || !n2.Since || n2.New != 2 || n2.Next.ID != "a-3" {
		t.Fatalf("after %+v %v", n2, err)
	}
	// the same clock on every message (a fake CLI): the ids still tell
	appendFile(t, p, claudeTurn(4, at.Add(2*time.Minute)))
	if n3, _ := CountAfter(ctx, h, KindClaude, p, n2.Next); !n3.Since || n3.New != 2 {
		t.Fatalf("same time %+v", n3)
	}
	// a mark far back in a long file: the tail grows until it holds it
	appendFile(t, p, string(filler(600<<10))+claudeTurn(5, at.Add(3*time.Minute)))
	if n4, _ := CountAfter(ctx, h, KindClaude, p, Mark{ID: "a-4"}); !n4.Since || n4.New != 2 {
		t.Fatalf("grown %+v", n4)
	}

	// Codex: its own input has no id, so the time tells
	x := filepath.Join(dir, "rollout.jsonl")
	appendFile(t, x, string(codexRollout("019a0000-aaaa-7bbb-8ccc-ddddeeeeffff", "/srv/shop", "make the cart", at)))
	c, err = Describe(ctx, h, KindCodex, x)
	if err != nil || c.AgentSessionID != "019a0000-aaaa-7bbb-8ccc-ddddeeeeffff" || c.Title != "make the cart" || c.Messages != 2 {
		t.Fatalf("codex describe %+v %v", c, err)
	}
	m := Mark{At: at}
	later := at.Add(time.Hour).Format(time.RFC3339Nano)
	appendFile(t, x, `{"timestamp":"`+later+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"and the checkout"}]}}`+"\n")
	if n, _ := CountAfter(ctx, h, KindCodex, x, m); !n.Since || n.New != 1 || !n.Next.At.Equal(at.Add(time.Hour)) {
		t.Fatalf("codex after %+v", n)
	}
	if _, err := Describe(ctx, h, KindClaude, filepath.Join(dir, "missing.jsonl")); err == nil {
		t.Fatal("a missing file described")
	}
}

func TestContains(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	p := filepath.Join(t.TempDir(), "c.jsonl")
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	appendFile(t, p, claudeTurn(1, at))
	if !Contains(ctx, h, KindClaude, p, "PROMPT number") || !Contains(ctx, h, KindClaude, p, "answer 1") || Contains(ctx, h, KindClaude, p, "checkout") {
		t.Fatal("short file")
	}
	// the middle of a long file is not looked at; its start and end are
	appendFile(t, p, string(filler(400<<10))+strings.Replace(claudeTurn(2, at), "prompt number 2", "the checkout flow", 1)+string(filler(400<<10))+claudeTurn(3, at))
	if !Contains(ctx, h, KindClaude, p, "prompt number 1") || !Contains(ctx, h, KindClaude, p, "answer 3") || Contains(ctx, h, KindClaude, p, "checkout") {
		t.Fatal("long file")
	}
	if Contains(ctx, h, KindClaude, p, "  ") || Contains(ctx, h, "none", p, "answer") {
		t.Fatal("empty query or kind")
	}
}

// The chat index's backfill: the conversations of several folders at once, the newest per
// Claude folder, the Codex rollouts of those folders only, and files already read left out.
func TestScanPlaces(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	cfg, home := t.TempDir(), t.TempDir()
	api, web := "/srv/shop/api", "/srv/shop/web"
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	ids := []string{"0d7e5b1a-1111-4222-8333-444455556666", "0d7e5b1a-2222-4222-8333-444455556666", "0d7e5b1a-3333-4222-8333-444455556666"}
	for i, id := range ids[:2] {
		write(t, ClaudePath(cfg, api, id), []byte(claudeTurn(i+1, at)), at.Add(time.Duration(i)*time.Hour))
	}
	write(t, ClaudePath(cfg, web, ids[2]), []byte(claudeTurn(9, at)), at)
	write(t, ClaudePath(cfg, "/srv/other", "0d7e5b1a-4444-4222-8333-444455556666"), []byte(claudeTurn(1, at)), at)
	read := map[string]time.Time{}
	known := func(p string, m time.Time) bool { r, ok := read[p]; return ok && r.Equal(m) }
	note := func(p string, m time.Time) { read[p] = m }
	got, truncated, err := ScanPlaces(ctx, h, KindClaude, cfg, []string{api, web}, 1, known, note)
	if err != nil || !truncated || len(got) != 2 {
		t.Fatalf("claude %v %v %+v", truncated, err, got)
	}
	byCwd := map[string]Found{}
	for _, f := range got {
		byCwd[f.Cwd] = f
	}
	if byCwd[api].AgentSessionID != ids[1] || byCwd[api].Title != "prompt number 2" || byCwd[web].AgentSessionID != ids[2] || byCwd[web].Messages != 2 {
		t.Fatalf("found %+v", got)
	}
	// what was read as it is now is not read again
	if again, _, _ := ScanPlaces(ctx, h, KindClaude, cfg, []string{api, web}, 1, known, note); len(again) != 0 {
		t.Fatalf("again %+v", again)
	}

	day := filepath.Join(home, "sessions", "2026", "11", "14")
	x1, x2 := "019a0000-aaaa-7bbb-8ccc-ddddeeeeffff", "019a0000-bbbb-7bbb-8ccc-ddddeeeeffff"
	write(t, filepath.Join(day, rolloutName(at, x1)), codexRollout(x1, web, "make the cart", at), at)
	write(t, filepath.Join(day, rolloutName(at.Add(time.Minute), x2)), codexRollout(x2, "/srv/other", "elsewhere", at), at)
	got, _, err = ScanPlaces(ctx, h, KindCodex, home, []string{api, web + "/"}, 0, nil, note)
	if err != nil || len(got) != 1 || got[0].AgentSessionID != x1 || got[0].Cwd != web+"/" || got[0].Title != "make the cart" {
		t.Fatalf("codex %v %+v", err, got)
	}
	if len(read) != 4 {
		t.Fatalf("read %v", read)
	}
}
