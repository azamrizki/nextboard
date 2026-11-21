package native

import (
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
)

// statusLines are the compaction's lines among lines, in their order.
func statusLines(lines []map[string]any) []string {
	var out []string
	for _, l := range lines {
		if l["type"] != "system" {
			continue
		}
		switch l["subtype"] {
		case "status":
			s, _ := l["status"].(string)
			out = append(out, "status:"+s)
		case "compact_boundary":
			out = append(out, "boundary")
		}
	}
	return out
}

func TestCompactThreshold(t *testing.T) {
	fake := newFake(
		answer(llm.StopEnd, llm.Usage{Input: 100, CacheRead: 700, CacheWrite: 50, Output: 10}, textBlock("big answer")), // 850 of 1000
		answer(llm.StopEnd, llm.Usage{Input: 300, Output: 40}, textBlock("SUMMARY OF IT")),
		say("after"),
	)
	cfg := newConfig(t, fake, newKit())
	cfg.contextWindow = 1000
	cfg.mode = modeAuto
	h := startAgent(t, cfg)
	h.initialize()
	h.say("first question")
	h.result()
	if c := loadConv(t, cfg, "sess-1"); c.Context != 850 {
		t.Fatalf("context %d", c.Context)
	}
	h.say("next question")
	res, before := h.result()
	if got := strings.Join(statusLines(before), ","); got != "status:compacting,boundary,status:" {
		t.Fatalf("compaction lines: %s", got)
	}
	sum := fake.request(t, 1)
	if sum.System != summarySystem || sum.Tools != nil || len(sum.Messages) != 1 {
		t.Fatalf("summary call: %+v", sum)
	}
	tr := sum.Messages[0].Blocks[0].Text
	if !strings.Contains(tr, "[The owner]") || !strings.Contains(tr, "first question") || !strings.Contains(tr, "[You]\nbig answer") || strings.Contains(tr, "next question") {
		t.Fatalf("transcript: %s", tr)
	}
	main := fake.request(t, 2)
	if main.System != fake.request(t, 0).System || len(main.Tools) != len(fake.request(t, 0).Tools) {
		t.Fatal("the system prompt or tools changed with the compaction")
	}
	if len(main.Messages) != 1 {
		t.Fatalf("after compaction: %d messages", len(main.Messages))
	}
	got := texts(main.Messages[0])
	if !strings.HasPrefix(got[0], "This conversation continues from an earlier one; its summary:\nSUMMARY OF IT") ||
		!strings.Contains(got[1], "The permission mode is auto") || got[len(got)-1] != "next question" {
		t.Fatalf("new start: %q", got)
	}
	if field(res, "usage", "input_tokens") != 310.0 { // the summary and the answer
		t.Fatalf("usage: %v", res["usage"])
	}
	c := loadConv(t, cfg, "sess-1")
	if c.Compactions != 1 || len(c.Archived) != 1 || len(c.Archived[0]) != 3 || len(c.Messages) != 3 || c.System != fake.request(t, 0).System {
		t.Fatalf("file: compactions %d, archived %d, messages %d", c.Compactions, len(c.Archived), len(c.Messages))
	}
}

func TestCompactOverflow(t *testing.T) {
	fake := newFake(
		say("one"),
		failWith(&llm.APIError{Status: 400, Message: "prompt is too long", ContextOverflow: true}),
		say("SUM"),
		say("fine"),
	)
	cfg := newConfig(t, fake, newKit())
	h := startAgent(t, cfg)
	h.initialize()
	h.say("hello")
	h.result()
	h.say("a huge paste")
	res, before := h.result()
	if res["subtype"] != "success" || res["result"] != "fine" || strings.Join(statusLines(before), ",") != "status:compacting,boundary,status:" {
		t.Fatalf("result %v, lines %v", res, statusLines(before))
	}
	retry := fake.request(t, 3)
	if got := texts(retry.Messages[0]); got[len(got)-1] != "a huge paste" || !strings.Contains(got[0], "SUM") {
		t.Fatalf("retry: %q", got)
	}
	if c := loadConv(t, cfg, "sess-1"); c.Compactions != 1 {
		t.Fatalf("compactions %d", c.Compactions)
	}
}

func TestCompactOverflowTwice(t *testing.T) {
	over := &llm.APIError{Status: 400, Message: "prompt is too long", ContextOverflow: true}
	fake := newFake(say("one"), failWith(over), say("SUM"), failWith(over))
	h := startAgent(t, newConfig(t, fake, newKit()))
	h.initialize()
	h.say("hello")
	h.result()
	h.say("a huge paste")
	res, _ := h.result()
	if res["is_error"] != true || res["result"] != "prompt is too long" || len(fake.requests()) != 4 {
		t.Fatalf("result %v after %d calls", res, len(fake.requests()))
	}
}

func TestCompactMidTurn(t *testing.T) {
	fake := newFake(
		answer(llm.StopToolUse, llm.Usage{Input: 900}, toolUse("t1", "Read", `{"file_path":"big"}`)),
		say("SUM"),
		say("carried on"),
	)
	cfg := newConfig(t, fake, newKit())
	cfg.contextWindow = 1000
	h := startAgent(t, cfg)
	h.initialize()
	h.say("read it")
	res, _ := h.result()
	if res["result"] != "carried on" {
		t.Fatalf("result %v", res)
	}
	tr := fake.request(t, 1).Messages[0].Blocks[0].Text
	if !strings.Contains(tr, "[You called Read]") || !strings.Contains(tr, "[Result of Read]\nRead ran") {
		t.Fatalf("transcript: %s", tr)
	}
	main := fake.request(t, 2)
	if len(main.Messages) != 1 || !strings.Contains(texts(main.Messages[0])[0], "Carry on with the work") {
		t.Fatalf("after: %+v", main.Messages)
	}
	checkHistory(t, loadConv(t, cfg, "sess-1").Messages)
}

func TestCompactCommand(t *testing.T) {
	fake := newFake(say("one"), say("SUM"))
	cfg := newConfig(t, fake, newKit())
	h := startAgent(t, cfg)
	h.initialize()
	h.say("/compact")
	res, _ := h.result()
	if res["result"] != "There is nothing to compact yet." || len(fake.requests()) != 0 {
		t.Fatalf("empty: %v", res)
	}
	h.say("hello")
	h.result()
	h.say("  /compact ")
	res, before := h.result()
	if res["subtype"] != "success" || res["result"] != "Compacted the conversation." || strings.Join(statusLines(before), ",") != "status:compacting,boundary,status:" {
		t.Fatalf("result %v, lines %v", res, statusLines(before))
	}
	c := loadConv(t, cfg, "sess-1")
	if len(c.Messages) != 1 || len(c.Archived) != 1 || strings.Contains(texts(c.Messages[0])[0], "Carry on") {
		t.Fatalf("file: %+v", c.Messages)
	}
	for _, m := range c.Messages {
		for _, s := range texts(m) {
			if strings.Contains(s, "/compact") {
				t.Fatal("the command went into the conversation")
			}
		}
	}
}

func TestCompactCommandQueued(t *testing.T) {
	release := make(chan struct{})
	fake := newFake(gate(release, say("one")), say("SUM"), say("three"))
	h := startAgent(t, newConfig(t, fake, newKit()))
	h.initialize()
	h.say("first")
	<-fake.called
	h.say("/compact")
	h.say("third")
	h.control("nb_2", map[string]any{"subtype": "set_permission_mode", "mode": "default"})
	h.until(is("control_response"))
	close(release)
	var results []any
	for range 3 {
		r, _ := h.result()
		results = append(results, r["result"])
	}
	if results[0] != "one" || results[1] != "Compacted the conversation." || results[2] != "three" {
		t.Fatalf("results %v", results)
	}
}

func TestTranscriptBudget(t *testing.T) {
	var msgs []llm.Message
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{textBlock("THE TASK")}})
	for i := range 50 {
		id := "t" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{toolUse(id, "Read", `{"file_path":"f"}`)}},
			llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: id, Text: strings.Repeat("x", 5000)}}})
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{textBlock("THE LATEST")}})
	full := transcript(msgs, 1_000_000)
	if len(full) < 50*3900 || !strings.Contains(full, strings.Repeat("x", 1900)) {
		t.Fatalf("a roomy budget cut the outputs too short: %d characters", len(full))
	}
	small := transcript(msgs, 8000)
	if len(small) > 8200 || !strings.Contains(small, "THE TASK") || !strings.Contains(small, "THE LATEST") || !strings.Contains(small, "earlier messages are left out") {
		t.Fatalf("%d characters: %s", len(small), small)
	}
}
