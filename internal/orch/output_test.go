package orch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nextboard/internal/agentstate"
	"nextboard/internal/delivery"
	"nextboard/internal/session"
)

// reportsOf waits until a thread holds n reports.
func reportsOf(t *testing.T, s *Service, tid string, n int) []ThreadMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var out []ThreadMessage
		for _, m := range s.Threads.Last(tid, 100) {
			if m.Kind == "report" {
				out = append(out, m)
			}
		}
		if len(out) >= n {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d reports, want %d", len(out), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// oneTurn sends an order to a console and finishes it with this reply.
func oneTurn(t *testing.T, h *fakeHub, s *Service, wid string, in session.Info, order, reply string, at time.Time) {
	t.Helper()
	s.Now = func() time.Time { return at }
	cmd(t, s, wid, "@"+in.Handle+" "+order)
	ds := h.deliveriesTo(in.ID)
	h.setDelivery(s, ds[len(ds)-1].ID, delivery.Confirmed)
	h.mu.Lock()
	h.replies[in.ID] = &session.Reply{Text: reply, At: at, Chars: len([]rune(reply)), Source: "transcript", MessageID: order}
	h.mu.Unlock()
	finish(t, h, s, in.ID, at.Add(time.Second))
}

// A report keeps the reply of its own turn, whatever the console does next, also after a
// restart and after the console is gone.
func TestReportKeepsItsOwnReply(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	long := strings.Repeat("first turn ", 2000) + "END-OF-FIRST"
	oneTurn(t, h, s, wid, a, "one", long, t0.Add(time.Minute))
	first := reportsOf(t, s, p.ID, 1)[0]
	oneTurn(t, h, s, wid, a, "two", "second turn", t0.Add(2*time.Minute))
	ms := reportsOf(t, s, p.ID, 2)
	second := ms[1]
	got, err := s.Threads.Output(p.ID, first.ID)
	if err != nil || got.Text != long || got.Partial || got.Handle != "Ash" || got.ProjectID != p.ID || got.Source != "transcript" {
		t.Fatalf("first: %v partial %v, %d chars", err, got.Partial, got.Chars)
	}
	if !got.At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("first at %v", got.At)
	}
	if n := len([]rune(ms[0].Text)); n > maxReportText {
		t.Fatalf("the message itself holds %d characters", n)
	}
	if got, err = s.Threads.Output(p.ID, second.ID); err != nil || got.Text != "second turn" {
		t.Fatalf("second: %v %q", err, got.Text)
	}
	// the console moves on, then it is deleted, then the hub restarts
	h.mu.Lock()
	h.replies[a.ID] = &session.Reply{Text: "third turn", At: t0.Add(3 * time.Minute), Chars: 10}
	h.mu.Unlock()
	if err := h.DeleteSession(a.ID); err != nil {
		t.Fatal(err)
	}
	s2 := New(h, h.st)
	if got, err = s2.Threads.Output(p.ID, first.ID); err != nil || got.Text != long {
		t.Fatalf("after a restart: %v, %d chars", err, got.Chars)
	}
}

// A report from before replies were kept still opens: what its message holds, marked as cut
// when the reply was longer.
func TestOlderReportOpensItsMessageText(t *testing.T) {
	_, s := newFakeHub(t)
	const tid = "p_oldthread1"
	cut := s.Threads.Add(ThreadMessage{ThreadID: tid, Kind: "report", Role: "agent", SessionID: "s_x", Handle: "Ash", Text: "the first part",
		Report: &Report{State: TFinished, Chars: 12000}})
	whole := s.Threads.Add(ThreadMessage{ThreadID: tid, Kind: "report", Role: "agent", Text: "all of it", Report: &Report{State: TFinished, Chars: 9}})
	if o, err := s.Threads.Output(tid, cut.ID); err != nil || o.Text != "the first part" || !o.Partial || o.Chars != 12000 {
		t.Fatalf("cut: %v %+v", err, o)
	}
	if o, err := s.Threads.Output(tid, whole.ID); err != nil || o.Partial {
		t.Fatalf("whole: %v %+v", err, o)
	}
}

// Only a finished report with a reply has one to show, and an id names no other file.
func TestOutputOnlyOfFinishedReports(t *testing.T) {
	_, s := newFakeHub(t)
	const tid = "p_oldthread2"
	note := s.Threads.Add(ThreadMessage{ThreadID: tid, Kind: "note", Text: "x"})
	failed := s.Threads.Add(ThreadMessage{ThreadID: tid, Kind: "report", Text: "x", Report: &Report{State: TFailed}})
	empty := s.Threads.Add(ThreadMessage{ThreadID: tid, Kind: "report", Text: "@A finished (no reply text).", Report: &Report{State: TFinished}})
	for _, id := range []string{note.ID, failed.ID, empty.ID, "tm_nothing", "../../secret"} {
		if _, err := s.Threads.Output(tid, id); err != ErrNoOutput {
			t.Fatalf("%s: %v", id, err)
		}
	}
}

// Clearing a thread and compacting it take the replies along.
func TestOutputsLeaveWithTheirMessages(t *testing.T) {
	h, s := newFakeHub(t)
	const tid = "n_cardthread"
	add := func() ThreadMessage {
		m := ThreadMessage{ID: "tm_" + time.Now().Format("150405.000000000"), ThreadID: tid, Kind: "report", Text: "x", Report: &Report{State: TFinished, Chars: 1}}
		s.Threads.SaveOutput(ReplyOutput{MessageID: m.ID, ThreadID: tid, Text: "kept"})
		return s.Threads.Add(m)
	}
	dir := h.st.Path("threads", tid+".outputs")
	first := add()
	for range 2*cardThreadMax + 2 {
		add()
	}
	if _, err := os.Stat(filepath.Join(dir, first.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("the reply of a message long gone: %v", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) > 2*cardThreadMax+1 || len(ents) == 0 {
		t.Fatalf("%d replies kept", len(ents))
	}
	s.Threads.Clear(tid)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("still there after clearing: %v", err)
	}
}

// A reply that comes in late fills the report; the previous turn's never stands in for it.
func TestLateReplyFillsTheReport(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	at := t0.Add(time.Hour)
	h.mu.Lock()
	h.replies[a.ID] = &session.Reply{Text: "the previous turn", At: t0, Chars: 17, MessageID: "old"}
	h.mu.Unlock()
	s.Now = func() time.Time { return at }
	cmd(t, s, wid, "@Ash work")
	ds := h.deliveriesTo(a.ID)
	h.setDelivery(s, ds[len(ds)-1].ID, delivery.Confirmed)
	end := at.Add(time.Minute)
	s.OnAgentEvent(a, agentstate.Event{Kind: agentstate.EvFinished, Turn: agentstate.LastTurn{StartedAt: at, EndedAt: &end}})
	m := reportsOf(t, s, p.ID, 1)[0]
	if strings.Contains(m.Text, "previous turn") || m.Report.Chars != 0 {
		t.Fatalf("stale reply reported: %+v", m)
	}
	if _, err := s.Threads.Output(p.ID, m.ID); err != ErrNoOutput {
		t.Fatalf("output of an empty report: %v", err)
	}
	h.mu.Lock()
	h.replies[a.ID] = &session.Reply{Text: "this turn", At: end, Chars: 9, MessageID: "new"}
	h.mu.Unlock()
	deadline := time.Now().Add(8 * time.Second)
	for {
		ms := reportsOf(t, s, p.ID, 1)
		if ms[0].ID == m.ID && ms[0].Text == "this turn" && ms[0].Report.Chars == 9 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not filled: %+v", ms[0])
		}
		time.Sleep(50 * time.Millisecond)
	}
	if o, err := s.Threads.Output(p.ID, m.ID); err != nil || o.Text != "this turn" {
		t.Fatalf("%v %+v", err, o)
	}
}
