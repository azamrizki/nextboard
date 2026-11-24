package nextcode

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHandOverCarriesTheChatToANewConversation(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir, 0)
	_ = r.d.Send("hello")
	r.item(KindText)
	r.turnEnded()

	r.d.HandOver(EngineClaude, func(carry bool) string {
		if !carry {
			t.Error("there is a chat to bring")
		}
		return "Now on Codex · gpt-5.5."
	})
	n := r.item(KindNotice)
	if n.Text != "Now on Codex · gpt-5.5." {
		t.Fatalf("notice %+v", n)
	}
	r.until("the CLI ended", func(f Frame) bool { return f.T == FrameStatus && f.Status.Process == ProcStopped })
	if st := r.d.Status(); st.Conversation != "" || st.Model != "" || st.CostUSD != 0 || r.d.Started() || !r.d.HandingOver() {
		t.Fatalf("after the hand-over: status %+v started %v", st, r.d.Started())
	}
	for _, f := range r.frames {
		if f.T == FrameItem && f.Item.Kind == KindError {
			t.Fatalf("a hand-over is not an error: %+v", f.Item)
		}
	}

	// the chat comes back after a hub restart with the hand-over still to make
	r.d.Close()
	d2, err := New(Config{HistoryPath: filepath.Join(dir, "chat.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	if d2.Started() || !d2.HandingOver() || d2.Status().Conversation != "" {
		t.Fatalf("reloaded: started %v handing over %v status %+v", d2.Started(), d2.HandingOver(), d2.Status())
	}
	d2.Close()

	r = newRig(t, dir, 0)
	_ = r.d.Send("next")
	if u := r.item(KindUser); u.Text != "next" {
		t.Fatalf("the owner's item is what they wrote: %+v", u)
	}
	reply := r.item(KindText).Text
	for _, want := range []string{"echo <conversation_so_far>", "User:\nhello", "Assistant:\nHi there", "</conversation_so_far>\n\nnext"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("the new conversation's first message lacks %q:\n%s", want, reply)
		}
	}
	if strings.Contains(reply, "Now on Codex") {
		t.Fatalf("notices are the chat's, not the conversation's:\n%s", reply)
	}
	r.turnEnded()
	if r.d.HandingOver() || !r.d.Started() {
		t.Fatal("the hand-over is made once")
	}
	if starts := r.starts; len(starts) != 1 || starts[0] {
		t.Fatalf("a new conversation, not a resume: %v", starts)
	}

	// the message after it is the owner's alone
	_ = r.d.Send("third")
	r.until("third reply", func(f Frame) bool { return f.T == FrameItem && f.Item.Kind == KindText && f.Item.Text == "echo third" })
	r.d.Close()
	d3, err := New(Config{HistoryPath: filepath.Join(dir, "chat.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	defer d3.Close()
	if d3.HandingOver() || !d3.Started() {
		t.Fatalf("reloaded after the hand-over: handing over %v started %v", d3.HandingOver(), d3.Started())
	}
}

func TestHandOverOfAnEmptyChatCarriesNothing(t *testing.T) {
	r := newRig(t, t.TempDir(), 0)
	r.d.HandOver(EngineCodex, nil)
	if r.d.HandingOver() || r.d.Status().Engine != EngineCodex {
		t.Fatalf("handing over %v status %+v", r.d.HandingOver(), r.d.Status())
	}
}

func TestHandOverPrompt(t *testing.T) {
	in, _ := json.Marshal(map[string]any{"command": "npm test\nmore"})
	items := []Item{
		{Kind: KindUser, Text: "fix the tests"},
		{Kind: KindThinking, Text: "secret thoughts"},
		{Kind: KindTool, Tool: &Tool{Name: "Bash", Input: in, Output: "ok 12 tests\n", Status: ToolDone}},
		{Kind: KindTool, Parent: "toolu_x", Tool: &Tool{Name: "Read", Status: ToolDone}},
		{Kind: KindTool, Tool: &Tool{Name: "Edit", Input: json.RawMessage(`{"file_path":"/a.go"}`), Status: ToolDenied}},
		{Kind: KindAnswer, Text: "Store: Redis", Answer: &Answered{}},
		{Kind: KindText, Text: "Done."},
		{Kind: KindNotice, Text: "Stopped."},
	}
	got := handOverPrompt(items)
	want := "User:\nfix the tests\n\nAssistant used Bash: npm test\n  → ok 12 tests\n\nAssistant used Edit: /a.go (denied)\n\nUser answered the assistant's questions:\nStore: Redis\n\nAssistant:\nDone.\n</conversation_so_far>\n\n"
	if !strings.HasPrefix(got, "<conversation_so_far>\n") || !strings.HasSuffix(got, want) || strings.Contains(got, "secret") || strings.Contains(got, "Read") {
		t.Fatalf("prompt:\n%s", got)
	}
	if handOverPrompt([]Item{{Kind: KindNotice, Text: "x"}}) != "" {
		t.Fatal("nothing to hand over")
	}

	// long chats keep their latest parts, within the limit, and say that the first are left out
	var many []Item
	for i := 0; i < 40; i++ {
		many = append(many, Item{Kind: KindUser, Text: strings.Repeat("é", 4000)}, Item{Kind: KindText, Text: "reply"})
	}
	many = append(many, Item{Kind: KindUser, Text: "the last one"})
	long := handOverPrompt(many)
	if len(long) > handOverMax+1024 || !strings.Contains(long, "(The earliest part is left out.)") || !strings.Contains(long, "the last one") || !strings.Contains(long, "[…]") {
		t.Fatalf("long prompt: %d bytes", len(long))
	}
	if !utf8.ValidString(long) {
		t.Fatal("a clipped prompt cut a character in half")
	}
}

// A sub-agent's chat shows in the main one under its card, and what it asks is asked there and
// answered back to it (NextCode's multi-agent).
func TestAttachSubAgent(t *testing.T) {
	parent := newRig(t, t.TempDir(), 0)
	child := newRig(t, t.TempDir(), 0)
	parent.d.PutItem(Item{ID: "card1", Kind: KindTool, Tool: &Tool{Name: "Agent", Status: ToolRunning}})
	stop := parent.d.Attach(child.d, "card1", "card1/")
	defer stop()
	if err := child.d.Send("write"); err != nil {
		t.Fatal(err)
	}
	prompt := parent.until("the task", func(f Frame) bool { return f.T == FrameItem && f.Item.Kind == KindPrompt })
	if prompt.Item.Parent != "card1" || !strings.HasPrefix(prompt.Item.ID, "card1/") || prompt.Item.Text != "write" {
		t.Fatalf("the task %+v", prompt.Item)
	}
	req := parent.until("its request", func(f Frame) bool { return f.T == FrameRequest }).Request
	if !strings.HasPrefix(req.ID, "card1/") || req.Parent != "card1" || req.Tool != "Write" {
		t.Fatalf("request %+v", req)
	}
	if p := parent.d.Pending(); len(p) != 1 || p[0].ID != req.ID {
		t.Fatalf("pending in the main chat %+v", p)
	}
	if err := parent.d.Answer(req.ID, Answer{Decision: Allow}); err != nil {
		t.Fatal(err)
	}
	parent.until("resolved", func(f Frame) bool { return f.T == FrameResolved && f.ID == req.ID })
	done := parent.until("its tool done", func(f Frame) bool {
		return f.T == FrameItem && f.Item.Tool != nil && f.Item.Tool.Name == "Write" && f.Item.Tool.Status == ToolDone
	})
	if done.Item.Parent != "card1" {
		t.Fatalf("tool %+v", done.Item)
	}
	child.turnEnded()
	if len(parent.d.Pending()) != 0 || child.d.Final() == "" {
		t.Fatalf("pending %+v final %q", parent.d.Pending(), child.d.Final())
	}

	// its question is answered in the main chat; the answer shows under its card, naming its
	// question's tool call as the main chat does
	if err := child.d.Send("ask"); err != nil {
		t.Fatal(err)
	}
	q := parent.until("its question", func(f Frame) bool { return f.T == FrameRequest }).Request
	if err := parent.d.Answer(q.ID, Answer{Decision: Allow, Answers: map[string][]string{"Colour?": {"Red"}}}); err != nil {
		t.Fatal(err)
	}
	ans := parent.item(KindAnswer)
	if ans.Parent != "card1" || !strings.HasPrefix(ans.ID, "card1/") || ans.Answer.ToolUseID != "card1/toolu_q" || ans.Text != "Colour: Red" {
		t.Fatalf("answer %+v %+v", ans, ans.Answer)
	}
	child.turnEnded()
}
