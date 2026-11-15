package transcript

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/azamrizki/nextboard/internal/machine"
)

func appendTo(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func claudeReplyLine(at, model string) string {
	return `{"type":"assistant","timestamp":"` + at + `","message":{"model":"` + model + `","content":[{"type":"text","text":"ok"}]}}` + "\n"
}

func claudeCmdLine(at, name, args string) string {
	return `{"type":"user","timestamp":"` + at + `","message":{"content":"<command-name>/` + name + `</command-name> <command-args>` + args + `</command-args>"}}` + "\n"
}

func claudeOutLine(at, out string) string {
	return `{"type":"user","timestamp":"` + at + `","message":{"content":"<local-command-stdout>` + out + `</local-command-stdout>"}}` + "\n"
}

// toolResult is a big user line of the kind most of a transcript is made of.
func toolResult(at string, n int) string {
	return `{"type":"user","timestamp":"` + at + `","message":{"content":[{"type":"tool_result","content":"` + strings.Repeat("r", n) + `"}]}}` + "\n"
}

// A console's model scan reads only what was appended since its last read, and carries what
// it saw, including a /model command whose output comes with a later read.
func TestReadModelFromReadsOnlyWhatWasAppended(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	p := filepath.Join(t.TempDir(), "c.jsonl")
	const t1, t2, t3 = "2026-11-04T01:00:00.000Z", "2026-11-04T02:00:00.000Z", "2026-11-04T03:00:00.000Z"
	first := claudeReplyLine(t1, "claude-opus-5-5") + toolResult(t1, 5000)
	write(t, p, []byte(first), time.Time{})
	var sc ModelScan
	seen, err := ReadModelFrom(ctx, h, KindClaude, p, time.Time{}, &sc)
	if err != nil || seen.Replied != "claude-opus-5-5" || sc.off != int64(len(first)) {
		t.Fatalf("first read %+v off %d %v", seen, sc.off, err)
	}
	// What was read is not read again: the same bytes made unreadable change nothing.
	blank := strings.Repeat(" ", len(first)-1) + "\n"
	write(t, p, []byte(blank+claudeCmdLine(t2, "model", "claude-sonnet-5-5")), time.Time{})
	if seen, _ = ReadModelFrom(ctx, h, KindClaude, p, time.Time{}, &sc); seen.Replied != "claude-opus-5-5" || seen.HasModel {
		t.Fatalf("after the command %+v", seen)
	}
	// its output comes later, half a line first
	out := claudeOutLine(t2, "Set model to `Sonnet 5.5`")
	appendTo(t, p, out[:20])
	if seen, _ = ReadModelFrom(ctx, h, KindClaude, p, time.Time{}, &sc); seen.HasModel {
		t.Fatalf("half a line counted %+v", seen)
	}
	appendTo(t, p, out[20:]+claudeReplyLine(t3, "claude-sonnet-5-5"))
	seen, _ = ReadModelFrom(ctx, h, KindClaude, p, time.Time{}, &sc)
	if !seen.HasModel || seen.Model != "claude-sonnet-5-5" || seen.Replied != "claude-sonnet-5-5" {
		t.Fatalf("command and output across reads %+v", seen)
	}
	if fi, _ := os.Stat(p); sc.off != fi.Size() {
		t.Fatalf("off %d, file %d", sc.off, fi.Size())
	}
	// A later since starts over: what came before it does not count.
	if seen, _ = ReadModelFrom(ctx, h, KindClaude, p, parseTime(t3), &sc); seen.HasModel || seen.Replied != "" {
		t.Fatalf("since moved %+v", seen)
	}
	// A file that shrank is read again from its start.
	write(t, p, []byte(claudeReplyLine(t1, "claude-haiku-5-5")), time.Time{})
	if seen, _ = ReadModelFrom(ctx, h, KindClaude, p, time.Time{}, &sc); seen.Replied != "claude-haiku-5-5" || seen.HasModel {
		t.Fatalf("rewritten %+v", seen)
	}
	// Another file is another scan.
	q := filepath.Join(filepath.Dir(p), "x.jsonl")
	write(t, q, []byte(`{"timestamp":"`+t2+`","type":"turn_context","payload":{"model":"gpt-6-luna","effort":"low"}}`+"\n"), time.Time{})
	if seen, _ = ReadModelFrom(ctx, h, KindCodex, q, time.Time{}, &sc); !seen.HasModel || seen.Model != "gpt-6-luna" || seen.Effort != "low" {
		t.Fatalf("codex %+v", seen)
	}
}

// The first read takes the last TailMax bytes, as ReadModel always did, and so does a read
// after more than that was appended at once.
func TestReadModelFromTail(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	p := filepath.Join(t.TempDir(), "c.jsonl")
	const t1, t2 = "2026-11-04T01:00:00.000Z", "2026-11-04T02:00:00.000Z"
	data := claudeCmdLine(t1, "model", "claude-sonnet-5-5") + claudeOutLine(t1, "Set model to `Sonnet 5.5`") +
		toolResult(t1, TailMax) + claudeReplyLine(t2, "claude-opus-5-5")
	write(t, p, []byte(data), time.Time{})
	var sc ModelScan
	seen, err := ReadModelFrom(ctx, h, KindClaude, p, time.Time{}, &sc)
	if err != nil || seen.HasModel || seen.Replied != "claude-opus-5-5" {
		t.Fatalf("tail %+v %v", seen, err)
	}
	want, err := ReadModel(ctx, h, KindClaude, p, time.Time{})
	if err != nil || want != seen {
		t.Fatalf("ReadModel %+v %v, ReadModelFrom %+v", want, err, seen)
	}
	appendTo(t, p, claudeCmdLine(t2, "effort", "high")+toolResult(t2, TailMax)+claudeReplyLine(t2, "claude-fable-5-1"))
	if seen, _ = ReadModelFrom(ctx, h, KindClaude, p, time.Time{}, &sc); seen.HasEffort || seen.Replied != "claude-fable-5-1" {
		t.Fatalf("after more than TailMax %+v", seen)
	}
}

// A reply whose text runs past the first tail is read whole: the tail grows until the turn
// starts in it.
func TestReadReplyWholeTurn(t *testing.T) {
	ctx := context.Background()
	h := localHost()
	p := filepath.Join(t.TempDir(), "c.jsonl")
	long := strings.Repeat("a", ReplyTailStart+50_000)
	data := string(filler(2<<20)) +
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T01:00:00.000Z","message":{"role":"user","content":"go"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-04T01:00:01.000Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"` + long + `"}]}}` + "\n" +
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-04T01:00:02.000Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"end"}]}}` + "\n"
	write(t, p, []byte(data), time.Time{})
	r, err := ReadReply(ctx, h, KindClaude, p)
	if err != nil || r.Chars != len(long)+len("\n\nend") || !r.Truncated || !strings.HasPrefix(r.Text, "aaaa") {
		t.Fatalf("reply chars %d truncated %v %v", r.Chars, r.Truncated, err)
	}
	// a short reply comes from the first tail
	q := filepath.Join(filepath.Dir(p), "d.jsonl")
	write(t, q, append(filler(TailMax), fixture(t, "claude-text-tool-text.jsonl")...), time.Time{})
	if r, err := ReadReply(ctx, h, KindClaude, q); err != nil || r.Text != wantClaudeReply {
		t.Fatalf("short reply %+v %v", r, err)
	}
}

// At most two tails are read at once; one that waits gives up with its context.
func TestTailSlots(t *testing.T) {
	for range cap(tailSlots) {
		tailSlots <- struct{}{}
	}
	defer func() {
		for range cap(tailSlots) {
			<-tailSlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	p := filepath.Join(t.TempDir(), "c.jsonl")
	write(t, p, fixture(t, "claude-text-tool-text.jsonl"), time.Time{})
	if _, err := ReadReply(ctx, localHost(), KindClaude, p); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reply without a slot: %v", err)
	}
	if _, err := ReadModel(ctx, localHost(), KindClaude, p, time.Time{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("model without a slot: %v", err)
	}
}

// A cut reply is a copy, not a window on the whole text, which it would keep alive.
func TestReplyCutIsACopy(t *testing.T) {
	text := strings.Repeat("b", MaxReplyBytes*2)
	r := makeReply(text, "m", time.Time{})
	if !r.Truncated || len(r.Text) != MaxReplyBytes || unsafe.StringData(r.Text) == unsafe.StringData(text) {
		t.Fatalf("cut %d, shares the text: %v", len(r.Text), unsafe.StringData(r.Text) == unsafe.StringData(text))
	}
}

// countingHost counts the bytes read through it.
type countingHost struct {
	machine.Host
	read int64
}

func (c *countingHost) ReadAt(ctx context.Context, p string, off, n int64) ([]byte, *machine.FileStat, error) {
	b, st, err := c.Host.ReadAt(ctx, p, off, n)
	c.read += int64(len(b))
	return b, st, err
}

// A reply after a tool result bigger than any tail is taken from a 2 MiB tail, not 8 MiB.
func TestReadReplyAfterAHugeToolResult(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.jsonl")
	data := `{"type":"user","uuid":"u1","timestamp":"2026-11-04T01:00:00.000Z","message":{"role":"user","content":"go"}}` + "\n" +
		toolResult("2026-11-04T01:00:01.000Z", 3<<20) +
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-04T01:00:02.000Z","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"
	write(t, p, []byte(data), time.Time{})
	h := &countingHost{Host: localHost()}
	r, err := ReadReply(context.Background(), h, KindClaude, p)
	if err != nil || r.Text != "done" {
		t.Fatalf("reply %+v %v", r, err)
	}
	if h.read > 2*ReplyWholeAt {
		t.Fatalf("read %d bytes", h.read)
	}
}
