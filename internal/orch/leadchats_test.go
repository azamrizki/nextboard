package orch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/jobs"
)

// Lead chats (§4.1): a thread with the lead is a list of chats, each message in one of them.

// ticking moves the service's clock a second at every reading, so chats sort by time.
func ticking(s *Service) {
	var mu sync.Mutex
	now := t0
	s.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Second)
		return now
	}
	s.Threads.Now = s.Now
}

// addMsgs adds n messages to a chat, the owner and the lead in turn, "<prefix> #NN;".
func addMsgs(s *Service, tid, chatID, prefix string, n int) []ThreadMessage {
	var out []ThreadMessage
	for i := range n {
		m := ThreadMessage{ThreadID: tid, ChatID: chatID, Role: "hub", Kind: "reply", Text: fmt.Sprintf("%s #%02d;", prefix, i+1)}
		if i%2 == 0 {
			m.Role, m.Kind = "user", "order"
		}
		out = append(out, s.Threads.Add(m))
	}
	return out
}

func chatMsgs(t *testing.T, th *Threads, tid, chatID string) []ThreadMessage {
	t.Helper()
	l, _, _, err := th.ListChat(tid, chatID, "", 200)
	if err != nil {
		t.Fatalf("chat %q of %s: %v", chatID, tid, err)
	}
	return l
}

func joined(msgs []ThreadMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Text + "\n")
	}
	return b.String()
}

// fileLines reads a thread file as messages, line by line.
func fileLines(t *testing.T, s *Service, tid string) []ThreadMessage {
	t.Helper()
	b, err := os.ReadFile(s.Threads.path(tid))
	if err != nil {
		t.Fatal(err)
	}
	var out []ThreadMessage
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m ThreadMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func leadChatEvents(h *fakeHub, tid string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, ev := range h.events {
		if ev["type"] == "leadChats" && ev["threadId"] == tid {
			out = append(out, ev["active"].(string))
		}
	}
	return out
}

// TestLeadChatMigration: a thread from before chats becomes one chat holding every message,
// titled by the first owner message; the file is rewritten once, keeps every message and
// the replies, and a hub that stopped between the two files finds the same chat again.
func TestLeadChatMigration(t *testing.T) {
	_, s := newFakeHub(t)
	tid := "p_oldthread1"
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }
	old := []ThreadMessage{
		{ID: "tm_a1", ThreadID: tid, At: at(0), Role: "hub", Kind: "note", Text: "Board opened."},
		{ID: "tm_a2", ThreadID: tid, At: at(1), Role: "user", Kind: "order", Text: "Fix the login page and run the tests"},
		{ID: "tm_a3", ThreadID: tid, At: at(2), Role: "hub", Kind: "report", Text: "Working…"},
		{ID: "tm_a3", ThreadID: tid, At: at(2), Role: "hub", Kind: "report", Text: "Login fixed, tests pass.", Report: &Report{State: TFinished, Chars: 40}},
		{ID: "tm_a4", ThreadID: tid, At: at(3), Role: "user", Kind: "order", Text: "Now the signup page"},
	}
	var raw bytes.Buffer
	for _, m := range old {
		b, _ := json.Marshal(m)
		raw.Write(append(b, '\n'))
	}
	if err := os.WriteFile(s.Threads.path(tid), raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	full := "Login fixed, tests pass. The form now checks the e-mail first."
	s.Threads.SaveOutput(ReplyOutput{MessageID: "tm_a3", ThreadID: tid, Text: full, Chars: len(full)})
	// a report coming in after a restart: its reply is saved before its message, whose Add
	// is the first to read the thread
	next := "Signup page done. It shares the form checks with the login page."
	s.Threads.SaveOutput(ReplyOutput{MessageID: "tm_a5", ThreadID: tid, Text: next, Chars: len(next)})
	s.Threads.Add(ThreadMessage{ID: "tm_a5", ThreadID: tid, At: at(4), Role: "hub", Kind: "report", Text: "Signup page done.", Report: &Report{State: TFinished, Chars: len(next)}})
	if o, err := s.Threads.Output(tid, "tm_a5"); err != nil || o.Text != next || o.Partial {
		t.Fatalf("the reply of the first report after the migration: %+v %v", o, err)
	}

	active, items := s.Threads.Chats(tid)
	if len(items) != 1 || items[0].ID != active || !ValidChatID(active) {
		t.Fatalf("chats %q %+v", active, items)
	}
	c := items[0]
	if c.Title != "Fix the login page and run the tests" || !c.Titled || c.Messages != 5 || !c.CreatedAt.Equal(at(0)) || !c.LastAt.Equal(at(4)) {
		t.Fatalf("migrated chat %+v", c)
	}
	lines := fileLines(t, s, tid)
	if len(lines) != 5 {
		t.Fatalf("the file keeps one line a message: %d", len(lines))
	}
	for _, m := range lines {
		if m.ChatID != active {
			t.Fatalf("line without its chat: %+v", m)
		}
	}
	if lines[2].ID != "tm_a3" || lines[2].Text != "Login fixed, tests pass." {
		t.Fatalf("the last version of a message is kept: %+v", lines[2])
	}
	if o, err := s.Threads.Output(tid, "tm_a3"); err != nil || o.Text != full {
		t.Fatalf("the reply was lost: %+v %v", o, err)
	}

	// a restart reads the same chat; nothing is made twice
	again := newThreads(s.st)
	a2, items2 := again.Chats(tid)
	msgs := chatMsgs(t, again, tid, "")
	if a2 != active || len(items2) != 1 || items2[0].Messages != 5 || len(msgs) != 5 || msgs[0].ChatID != active {
		t.Fatalf("after a restart %q %+v %+v", a2, items2, msgs)
	}

	// the hub stopped after the messages and before the list: the list is made again
	if err := os.Remove(s.Threads.chatsPath(tid)); err != nil {
		t.Fatal(err)
	}
	third := newThreads(s.st)
	a3, items3 := third.Chats(tid)
	if a3 != active || len(items3) != 1 || items3[0].Title != c.Title || items3[0].Messages != 5 {
		t.Fatalf("made again %q %+v", a3, items3)
	}
	if _, err := os.Stat(third.chatsPath(tid)); err != nil {
		t.Fatalf("the list made again is saved: %v", err)
	}
}

// TestLeadChatMigrationLeavesAFileItCannotRead: a thread file with a line too long to read is
// not rewritten at load, so what comes after that line stays in it.
func TestLeadChatMigrationLeavesAFileItCannotRead(t *testing.T) {
	_, s := newFakeHub(t)
	tid := "p_bigline01"
	first, _ := json.Marshal(ThreadMessage{ID: "tm_b1", ThreadID: tid, At: t0, Role: "user", Kind: "order", Text: "Deploy the docs"})
	after, _ := json.Marshal(ThreadMessage{ID: "tm_b3", ThreadID: tid, At: t0, Role: "hub", Kind: "note", Text: "after the long line"})
	raw := string(first) + "\n" + `{"id":"tm_b2","text":"` + strings.Repeat("x", 5<<20) + `"}` + "\n" + string(after) + "\n"
	if err := os.WriteFile(s.Threads.path(tid), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	active, items := s.Threads.Chats(tid)
	if len(items) != 1 || items[0].Messages != 1 {
		t.Fatalf("chats %+v", items)
	}
	if b, _ := os.ReadFile(s.Threads.path(tid)); string(b) != raw {
		t.Fatal("a file read only in part was rewritten")
	}
	again := newThreads(s.st)
	if a2, _ := again.Chats(tid); a2 != active {
		t.Fatalf("the chat changed at the next load: %q → %q", active, a2)
	}
	if msgs := chatMsgs(t, again, tid, ""); len(msgs) != 1 || msgs[0].ChatID != active {
		t.Fatalf("messages %+v", msgs)
	}
}

// TestLeadChatsNewAndPerChat: a new chat becomes the active one and the old one is kept; each
// chat lists its own messages; the first owner message names a chat; rename.
func TestLeadChatsNewAndPerChat(t *testing.T) {
	h, s := newFakeHub(t)
	ticking(s)
	p, wid := h.project("app")
	r := cmd(t, s, wid, "help")
	first := s.Threads.ActiveChat(p.ID)
	if len(r.Messages) < 2 {
		t.Fatalf("help: %s", texts(r))
	}
	for _, m := range r.Messages {
		if m.ChatID != first {
			t.Fatalf("message outside the active chat: %+v", m)
		}
	}

	b, err := s.NewLeadChat(p.ID)
	if err != nil || b.ID == first || b.Title != newChatTitle || b.Titled || b.Messages != 0 {
		t.Fatalf("new chat %+v %v", b, err)
	}
	if ev := leadChatEvents(h, p.ID); len(ev) == 0 || ev[len(ev)-1] != b.ID {
		t.Fatalf("leadChats events %q", ev)
	}
	if again, _ := s.NewLeadChat(p.ID); again.ID != b.ID {
		t.Fatalf("a chat nobody wrote in stays the new one: %s, %s", again.ID, b.ID)
	}

	r2 := cmd(t, s, wid, "what can you do")
	inB := chatMsgs(t, s.Threads, p.ID, "")
	if len(inB) != len(r2.Messages) || inB[0].Text != "what can you do" {
		t.Fatalf("active chat %q", joined(inB))
	}
	if inA := chatMsgs(t, s.Threads, p.ID, first); len(inA) != len(r.Messages) || strings.Contains(joined(inA), "what can you do") {
		t.Fatalf("first chat %q", joined(inA))
	}
	if _, _, _, err := s.Threads.ListChat(p.ID, "lc_missing01", "", 50); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("missing chat: %v", err)
	}

	active, items := s.Threads.Chats(p.ID)
	if active != b.ID || len(items) != 2 || items[0].ID != b.ID || items[1].ID != first {
		t.Fatalf("list %q %+v", active, items)
	}
	if items[0].Title != "what can you do" || !items[0].Titled || items[0].Messages != len(r2.Messages) || items[1].Title != "help" {
		t.Fatalf("titles %+v", items)
	}

	if c, err := s.Threads.RenameChat(p.ID, first, "  Getting   started  "); err != nil || c.Title != "Getting started" || !c.Titled {
		t.Fatalf("rename %+v %v", c, err)
	}
	if _, err := s.Threads.RenameChat(p.ID, first, "   "); !errors.Is(err, ErrBadChatTitle) {
		t.Fatalf("empty title: %v", err)
	}
	if _, err := s.Threads.RenameChat(p.ID, "lc_missing01", "x"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("rename a missing chat: %v", err)
	}

	// a restart keeps the list, the titles and the active chat
	again := newThreads(s.st)
	a2, items2 := again.Chats(p.ID)
	if a2 != b.ID || len(items2) != 2 || items2[1].Title != "Getting started" {
		t.Fatalf("after a restart %q %+v", a2, items2)
	}
}

// TestClearEmptiesOnlyTheActiveChat: DELETE /api/threads/{tid} empties the chat in use; the
// others keep their messages, in memory and in the file.
func TestClearEmptiesOnlyTheActiveChat(t *testing.T) {
	h, s := newFakeHub(t)
	ticking(s)
	p, wid := h.project("app")
	ra := cmd(t, s, wid, "help")
	a := s.Threads.ActiveChat(p.ID)
	b, _ := s.NewLeadChat(p.ID)
	cmd(t, s, wid, "what can you do")
	if got := s.ClearLeadChat(p.ID); got != b.ID {
		t.Fatalf("cleared %s, want %s", got, b.ID)
	}
	c, _ := s.Threads.Chat(p.ID, b.ID)
	if c.Messages != 0 || c.Title != newChatTitle || c.Titled || len(chatMsgs(t, s.Threads, p.ID, "")) != 0 {
		t.Fatalf("cleared chat %+v", c)
	}
	if active, items := s.Threads.Chats(p.ID); active != b.ID || len(items) != 2 {
		t.Fatalf("the cleared chat stays the active one: %q %+v", active, items)
	}
	if inA := chatMsgs(t, s.Threads, p.ID, a); len(inA) != len(ra.Messages) {
		t.Fatalf("the other chat lost messages: %q", joined(inA))
	}
	for _, m := range fileLines(t, s, p.ID) {
		if m.ChatID != a {
			t.Fatalf("a cleared message is still in the file: %+v", m)
		}
	}
	again := newThreads(s.st)
	if inA := chatMsgs(t, again, p.ID, a); len(inA) != len(ra.Messages) {
		t.Fatalf("after a restart: %q", joined(inA))
	}
	if inB := chatMsgs(t, again, p.ID, b.ID); len(inB) != 0 {
		t.Fatalf("after a restart the cleared chat has %q", joined(inB))
	}
}

// TestLeadChatBusyAndDeleteRules: no new chat while the lead thinks; the answer lands in the
// chat it was asked in; a chat in use, one the lead still answers in, and a missing one are
// not deleted; a deleted chat leaves the file with its messages.
func TestLeadChatBusyAndDeleteRules(t *testing.T) {
	h, s := newFakeHub(t)
	ticking(s)
	p, wid := h.project("app")
	h.console(p, wid, "Ash", "claude", agentstate.Ready)
	cmd(t, s, wid, "help")
	a := s.Threads.ActiveChat(p.ID)
	b, _ := s.NewLeadChat(p.ID)
	if err := s.DeleteLeadChat(p.ID, b.ID); !errors.Is(err, ErrChatActive) {
		t.Fatalf("delete the active chat: %v", err)
	}
	if err := s.DeleteLeadChat(p.ID, "lc_missing01"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("delete a missing chat: %v", err)
	}
	if _, err := s.ActivateLeadChat(p.ID, a); err != nil {
		t.Fatal(err)
	}

	// the lead thinks about an order given in the first chat
	if _, err := s.Command(CommandRequest{Text: "tidy up the old logs", WorkspaceID: wid}); err != nil {
		t.Fatal(err)
	}
	if plannerRuns(h) != 1 {
		t.Fatal("no lead turn")
	}
	if _, err := s.NewLeadChat(p.ID); !errors.Is(err, ErrLeadBusy) {
		t.Fatalf("new chat while the lead thinks: %v", err)
	}
	// nor resuming another chat: the answer would land in the one the owner left
	if _, err := s.ActivateLeadChat(p.ID, b.ID); !errors.Is(err, ErrResumeBusy) {
		t.Fatalf("resuming another chat meanwhile: %v", err)
	}
	if _, err := s.ActivateLeadChat(p.ID, "lc_missing01"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("resuming a missing chat meanwhile: %v", err)
	}
	before := len(chatMsgs(t, s.Threads, p.ID, a))
	if ch, err := s.ActivateLeadChat(p.ID, a); err != nil || ch.ID != a || len(chatMsgs(t, s.Threads, p.ID, a)) != before {
		t.Fatalf("the chat in use stays in use, without a divider: %+v %v", ch, err)
	}
	if active, _ := s.Threads.Chats(p.ID); active != a {
		t.Fatalf("active %q, want %q", active, a)
	}
	if err := s.DeleteLeadChat(p.ID, a); !errors.Is(err, ErrChatAnswering) {
		t.Fatalf("delete the chat the lead answers in: %v", err)
	}
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "Ash will tidy them.", "steps": []any{}}))
	if !strings.Contains(joined(chatMsgs(t, s.Threads, p.ID, a)), "Ash will tidy them.") || strings.Contains(joined(chatMsgs(t, s.Threads, p.ID, b.ID)), "Ash will tidy them.") {
		t.Fatal("the answer is not in the chat it was asked in")
	}

	if _, err := s.ActivateLeadChat(p.ID, b.ID); err != nil {
		t.Fatalf("resuming after the turn: %v", err)
	}
	if _, err := s.NewLeadChat(p.ID); err != nil {
		t.Fatalf("new chat after the turn: %v", err)
	}
	if err := s.DeleteLeadChat(p.ID, a); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Threads.Chat(p.ID, a); ok {
		t.Fatal("the chat is still there")
	}
	for _, m := range fileLines(t, s, p.ID) {
		if m.ChatID == a {
			t.Fatalf("a message of the deleted chat is in the file: %+v", m)
		}
	}
}

// TestActivateLeadChatSummary: resuming a long chat writes a divider and starts a summary job
// of all but the last 20; the lead answers meanwhile from the last 20, then gets the summary
// with them; a later resume sums up from that summary on.
func TestActivateLeadChatSummary(t *testing.T) {
	h, s := newFakeHub(t)
	ticking(s)
	p, wid := h.project("app")
	tid := p.ID
	a := s.Threads.ActiveChat(tid)
	long := addMsgs(s, tid, a, "login", 50)
	b, _ := s.NewLeadChat(tid)
	addMsgs(s, tid, b.ID, "other", 3)

	ch, err := s.ActivateLeadChat(tid, a)
	if err != nil || ch.ID != a || ch.Messages != 51 {
		t.Fatalf("activate %+v %v", ch, err)
	}
	if ev := leadChatEvents(h, tid); ev[len(ev)-1] != a {
		t.Fatalf("leadChats events %q", ev)
	}
	msgs := chatMsgs(t, s.Threads, tid, "")
	div := msgs[len(msgs)-1]
	if div.Source != "resume" || div.Role != "hub" || div.Kind != "note" || div.ChatID != a || !strings.Contains(div.Text, "reading the 30 earlier messages") {
		t.Fatalf("divider %+v", div)
	}
	run := h.lastRun(t, "chat-summary")
	if run.ProjectID != tid || !strings.Contains(run.Prompt, "login #01;") || !strings.Contains(run.Prompt, "login #30;") ||
		strings.Contains(run.Prompt, "login #31;") || strings.Contains(run.Prompt, "other #") {
		t.Fatalf("summary prompt:\n%s", run.Prompt)
	}

	// nothing waits for the summary: the lead answers at once from the last 20 of this chat
	if _, err := s.Command(CommandRequest{Text: "where were we", WorkspaceID: wid}); err != nil {
		t.Fatal(err)
	}
	pr := h.lastRun(t, "planner").Prompt
	if !strings.Contains(pr, "login #50;") || !strings.Contains(pr, "login #33;") || strings.Contains(pr, "login #31;") ||
		strings.Contains(pr, "other #") || strings.Contains(pr, `"chatSummary":`) {
		t.Fatalf("planner input before the summary:\n%s", pr)
	}
	s.plannerDone(tid, doneJob("", map[string]any{"reply": "On the login page.", "steps": []any{}}))

	summary := "The owner wants the login page fixed; Ash did the API."
	run.OnFinish(doneJob(summary, nil))
	ch, _ = s.Threads.Chat(tid, a)
	if ch.Summary != summary || ch.SummaryUpTo != long[29].ID {
		t.Fatalf("summary %q up to %s, want %s", ch.Summary, ch.SummaryUpTo, long[29].ID)
	}
	msgs = chatMsgs(t, s.Threads, tid, "")
	if got := msgs[50]; got.ID != div.ID || got.Text != "Resumed — Aruna has a summary of the 30 earlier messages and the last 20 in full." {
		t.Fatalf("divider after the summary %+v", got)
	}

	if _, err := s.Command(CommandRequest{Text: "and next?", WorkspaceID: wid}); err != nil {
		t.Fatal(err)
	}
	if pr := h.lastRun(t, "planner").Prompt; !strings.Contains(pr, `"chatSummary":"`+summary+`"`) {
		t.Fatalf("planner input lacks the summary:\n%s", pr)
	}
	s.plannerDone(tid, doneJob("", map[string]any{"reply": "The signup page.", "steps": []any{}}))

	// resumed again later: the job reads the summary and the messages after it
	s.ActivateLeadChat(tid, b.ID)
	s.ActivateLeadChat(tid, a)
	run2 := h.lastRun(t, "chat-summary")
	if !strings.Contains(run2.Prompt, "Summary of what came before:\n"+summary) || !strings.Contains(run2.Prompt, "login #31;") || strings.Contains(run2.Prompt, "login #30;") {
		t.Fatalf("second summary prompt:\n%s", run2.Prompt)
	}
}

// TestChatSummariesRunOneAtATime: resuming chat after chat starts one summary job at a time,
// so the lead's turns keep a runner place; a failed job says so in its divider.
func TestChatSummariesRunOneAtATime(t *testing.T) {
	h, s := newFakeHub(t)
	ticking(s)
	p, _ := h.project("app")
	tid := p.ID
	a := s.Threads.ActiveChat(tid)
	addMsgs(s, tid, a, "first", 45)
	b, _ := s.NewLeadChat(tid)
	addMsgs(s, tid, b.ID, "second", 45)
	c, _ := s.NewLeadChat(tid)
	addMsgs(s, tid, c.ID, "third", 2)
	summaries := func() []jobs.RunnerRequest {
		h.mu.Lock()
		defer h.mu.Unlock()
		var out []jobs.RunnerRequest
		for _, r := range h.runs {
			if r.Purpose == "chat-summary" {
				out = append(out, r)
			}
		}
		return out
	}
	s.ActivateLeadChat(tid, a)
	s.ActivateLeadChat(tid, b.ID)
	if n := len(summaries()); n != 1 {
		t.Fatalf("%d summary jobs at once", n)
	}
	summaries()[0].OnFinish(failedJob("timeout"))
	inA := chatMsgs(t, s.Threads, tid, a)
	if div := inA[len(inA)-1]; div.Text != "Resumed — Aruna has the last 20 messages in full; the 25 earlier ones could not be summed up (timeout)." {
		t.Fatalf("divider of a failed job %+v", div)
	}
	got := summaries()
	if len(got) != 2 || !strings.Contains(got[1].Prompt, "second #01;") {
		t.Fatalf("the next job after the first: %d", len(got))
	}
	got[1].OnFinish(doneJob("Second chat, summed up.", nil))
	if ch, _ := s.Threads.Chat(tid, b.ID); ch.Summary != "Second chat, summed up." {
		t.Fatalf("second chat %+v", ch)
	}
	// none under way: the next starts at once
	s.ActivateLeadChat(tid, c.ID)
	s.ActivateLeadChat(tid, a)
	if n := len(summaries()); n != 3 {
		t.Fatalf("%d summary jobs", n)
	}
}

// TestMissionWritesToItsOwnChat: a mission keeps writing to the chat it started in whatever
// chat the owner has open; the lead's wakes read that chat; the chat stays while it runs.
func TestMissionWritesToItsOwnChat(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	ms := proposeLogin(t, s, pid, wid)
	a := s.Threads.ActiveChat(pid)
	if ms.ChatID != a {
		t.Fatalf("mission chat %q, active %q", ms.ChatID, a)
	}
	if cf, ok := s.Confirms.Pending(pid, t0); !ok || cf.ChatID != a {
		t.Fatalf("the approval asks in its chat: %+v", cf)
	}
	b, err := s.NewLeadChat(pid)
	if err != nil {
		t.Fatal(err)
	}
	s.Threads.Add(ThreadMessage{ThreadID: pid, Role: "hub", Kind: "note", Text: "note in the second chat"})

	ms = approve(t, s, ms)
	d := h.deliveriesTo(ash)
	if len(d) != 1 {
		t.Fatalf("%d deliveries", len(d))
	}
	if o, _ := s.Orders.Get(d[0].OrderID); o.ChatID != a {
		t.Fatalf("the mission's order is in chat %q", o.ChatID)
	}
	finishTurn(t, h, s, ash, "API ready on :8080.")
	waitUntil(t, "t1's result", func() bool { x, _ := mustMission(t, s, ms.ID).Task("t1"); return x.Result != "" })
	waitUntil(t, "t1's report", func() bool { return strings.Contains(joined(chatMsgs(t, s.Threads, pid, a)), "API ready on :8080.") })

	inA, inB := chatMsgs(t, s.Threads, pid, a), chatMsgs(t, s.Threads, pid, b.ID)
	if !strings.Contains(joined(inA), "Mission started: Login feature") {
		t.Fatalf("mission chat:\n%s", joined(inA))
	}
	if len(inB) != 1 || inB[0].Text != "note in the second chat" {
		t.Fatalf("the chat open meanwhile:\n%s", joined(inB))
	}
	all, _ := s.Threads.List(pid, "", 200)
	for _, m := range all {
		if m.MissionID == ms.ID && m.ChatID != a {
			t.Fatalf("mission message outside its chat: %+v", m)
		}
	}
	if ch, _ := s.Threads.Chat(pid, a); len(ch.MissionIDs) != 1 || ch.MissionIDs[0] != ms.ID {
		t.Fatalf("chat missions %+v", ch.MissionIDs)
	}

	// the lead's wake reads the mission's chat, not the one open
	run := wakeRound(t, h, s, ms, t0.Add(6*time.Second), doneJob("", map[string]any{"reply": "", "steps": []any{}}))
	if !strings.Contains(run.Prompt, "Mission started: Login feature") || strings.Contains(run.Prompt, "note in the second chat") {
		t.Fatalf("wake input:\n%s", run.Prompt)
	}

	if err := s.DeleteLeadChat(pid, a); !errors.Is(err, ErrChatMission) {
		t.Fatalf("delete the chat of a running mission: %v", err)
	}
	if _, err := s.StopMission(ms.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLeadChat(pid, a); err != nil {
		t.Fatalf("delete after the mission ended: %v", err)
	}
}

// TestConfirmationsCarryTheirChat: a question names the chat it was asked in, and its expiry
// note goes there even after the owner moved to another chat.
func TestConfirmationsCarryTheirChat(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	h.console(p, wid, "Bea", "claude", agentstate.Ready)
	cmd(t, s, wid, "help")
	a := s.Threads.ActiveChat(p.ID)
	b, _ := s.NewLeadChat(p.ID)
	r := cmd(t, s, wid, "suruh Bea untuk jalankan rm -rf build")
	if r.Confirmation == nil || r.Confirmation.ChatID != b.ID {
		t.Fatalf("confirmation %+v", r.Confirmation)
	}
	if _, err := s.ActivateLeadChat(p.ID, a); err != nil {
		t.Fatal(err)
	}
	s.Tick(t0.Add(time.Hour))
	if !strings.Contains(joined(chatMsgs(t, s.Threads, p.ID, b.ID)), "Confirmation expired") || strings.Contains(joined(chatMsgs(t, s.Threads, p.ID, a)), "Confirmation expired") {
		t.Fatal("the expiry note is not in the chat that asked")
	}
}

// TestThreadLimitsPerChat: a chat keeps the thread's limit of its own and the thread twice that,
// so an old chat goes before a long one is cut; compaction keeps every message's chat.
func TestThreadLimitsPerChat(t *testing.T) {
	_, s := newFakeHub(t)
	th := s.Threads
	tid := "n_cardchat01" // a card thread: 200 a chat, 400 in all
	a := th.ActiveChat(tid)
	addMsgs(s, tid, a, "a", 150)
	b := th.NewChat(tid)
	addMsgs(s, tid, b.ID, "b", 150)
	c := th.NewChat(tid)
	addMsgs(s, tid, c.ID, "c", 150)
	count := func(th *Threads, id string) int { ch, _ := th.Chat(tid, id); return ch.Messages }
	if count(th, a) != 100 || count(th, b.ID) != 150 || count(th, c.ID) != 150 {
		t.Fatalf("counts %d %d %d", count(th, a), count(th, b.ID), count(th, c.ID))
	}
	if l := chatMsgs(t, th, tid, a); l[0].Text != "a #51;" {
		t.Fatalf("the oldest of the old chat go first: %q", l[0].Text)
	}
	// one chat past its own limit: its oldest go; the file is compacted on the way
	addMsgs(s, tid, c.ID, "d", 200)
	if count(th, a) != 50 || count(th, b.ID) != 150 || count(th, c.ID) != 200 {
		t.Fatalf("counts %d %d %d", count(th, a), count(th, b.ID), count(th, c.ID))
	}
	lines := fileLines(t, s, tid)
	if len(lines) >= 600 {
		t.Fatalf("not compacted: %d lines", len(lines))
	}
	for _, m := range lines {
		if m.ChatID == "" {
			t.Fatalf("a line without its chat: %+v", m)
		}
	}
	again := newThreads(s.st)
	if count(again, a) != 50 || count(again, b.ID) != 150 || count(again, c.ID) != 200 {
		t.Fatalf("after a restart %d %d %d", count(again, a), count(again, b.ID), count(again, c.ID))
	}
}

// TestMergeBringsTheChats: a merged project's chats come along with their messages; the chat
// the owner has open stays the active one; a thread with no messages takes the other's.
func TestMergeBringsTheChats(t *testing.T) {
	_, s := newFakeHub(t)
	ticking(s)
	th := s.Threads
	into, from := "p_intoproj01", "p_fromproj01"
	a := th.ActiveChat(into)
	addMsgs(s, into, a, "into", 2)
	f1 := th.ActiveChat(from)
	addMsgs(s, from, f1, "from", 2)
	f2 := th.NewChat(from)
	addMsgs(s, from, f2.ID, "later", 1)
	open := th.NewChat(into) // the owner opened a new chat here, nothing in it yet
	if n := th.Merge(into, from); n != 3 {
		t.Fatalf("merged %d", n)
	}
	active, items := th.Chats(into)
	if active != open.ID || len(items) != 4 {
		t.Fatalf("merged chats %q %+v", active, items)
	}
	for _, c := range items {
		if c.ThreadID != into {
			t.Fatalf("chat of the other thread %+v", c)
		}
	}
	if l := chatMsgs(t, th, into, f1); len(l) != 2 || l[0].ThreadID != into || l[0].Text != "from #01;" {
		t.Fatalf("merged messages %+v", l)
	}
	if _, err := os.Stat(th.chatsPath(from)); !os.IsNotExist(err) {
		t.Fatalf("the other thread's list is still there: %v", err)
	}
	again := newThreads(s.st)
	if a2, items2 := again.Chats(into); a2 != open.ID || len(items2) != 4 {
		t.Fatalf("after a restart %q %+v", a2, items2)
	}

	empty, other := "p_emptyproj1", "p_otherproj1"
	th.ActiveChat(empty) // looked at, never written in
	x := th.ActiveChat(other)
	addMsgs(s, other, x, "other", 1)
	th.Merge(empty, other)
	if active, items := th.Chats(empty); active != x || len(items) != 1 {
		t.Fatalf("an empty thread takes the other's chat: %q %+v", active, items)
	}
}

// TestLeadChatCountEvents: a new message's count goes out as leadChats at most every
// chatsEvery a thread, the last change included; a list change goes out at once and stands for
// the one waiting; nothing goes out after Close.
func TestLeadChatCountEvents(t *testing.T) {
	old := chatsEvery
	chatsEvery = 150 * time.Millisecond
	t.Cleanup(func() { chatsEvery = old })
	h, s := newFakeHub(t)
	p, _ := h.project("app")
	tid := p.ID
	a := s.Threads.ActiveChat(tid)
	events := func() int { return len(leadChatEvents(h, tid)) }

	s.Threads.Add(ThreadMessage{ThreadID: tid, Role: "hub", Kind: "note", Text: "first"})
	if events() != 1 {
		t.Fatalf("the first count goes out at once: %d events", events())
	}
	for i := range 5 {
		s.Threads.Add(ThreadMessage{ThreadID: tid, Role: "hub", Kind: "note", Text: fmt.Sprint("more ", i)})
	}
	if events() != 1 {
		t.Fatalf("a burst waits: %d events", events())
	}
	waitUntil(t, "the last count", func() bool { return events() == 2 })
	if ev := leadChatEvents(h, tid); ev[1] != a {
		t.Fatalf("event %q, want the active chat %q", ev[1], a)
	}
	time.Sleep(2 * chatsEvery)
	if events() != 2 {
		t.Fatalf("one event for the burst: %d", events())
	}

	// a list change goes out at once, and the count waiting with it is not sent again
	s.Threads.Add(ThreadMessage{ThreadID: tid, Role: "hub", Kind: "note", Text: "again"})
	s.Threads.Add(ThreadMessage{ThreadID: tid, Role: "hub", Kind: "note", Text: "and again"})
	n := events()
	b := s.Threads.NewChat(tid)
	if events() != n+1 || leadChatEvents(h, tid)[n] != b.ID {
		t.Fatalf("new chat event: %q", leadChatEvents(h, tid))
	}
	time.Sleep(2 * chatsEvery)
	if events() != n+1 {
		t.Fatalf("the count that waited went out too: %q", leadChatEvents(h, tid))
	}

	// nothing after Close
	s.Threads.Add(ThreadMessage{ThreadID: tid, Role: "hub", Kind: "note", Text: "late"})
	s.Threads.Add(ThreadMessage{ThreadID: tid, Role: "hub", Kind: "note", Text: "later"})
	n = events()
	s.Close()
	time.Sleep(2 * chatsEvery)
	if events() != n {
		t.Fatalf("an event after Close: %q", leadChatEvents(h, tid))
	}
}
