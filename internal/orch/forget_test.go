package orch

import (
	"os"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
)

// TestArchivedProjectLoopsWait: a loop of an archived project does not fire; restored, it
// fires on its schedule from then on, and the runs it missed are not made up for.
func TestArchivedProjectLoopsWait(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	archived := true
	s.Loops.Paused = func(pid string) bool { return pid == p.ID && archived }
	l, err := s.Loops.Create(LoopDef{ProjectID: p.ID, WorkspaceID: wid, Name: "Guard", Enabled: true, Text: "Run the tests.",
		Trigger: LoopTrigger{Kind: "interval", EverySec: 60}, Targets: LoopTargets{Kind: "allAgents"}}, t0, false)
	if err != nil {
		t.Fatal(err)
	}
	s.Loops.Tick(t0.Add(5 * time.Minute))
	s.Loops.RunNow(l.ID, t0.Add(5*time.Minute))
	if len(h.deliveriesTo(ash.ID)) != 0 {
		t.Fatal("an archived project's loop fired")
	}
	archived = false
	s.Loops.Reschedule(p.ID, t0.Add(10*time.Minute))
	if d, _ := s.Loops.Get(l.ID); d.State.Status != "running" || !d.State.NextRunAt.Equal(t0.Add(11*time.Minute)) {
		t.Fatalf("rescheduled %+v", d.State)
	}
	s.Loops.Tick(t0.Add(10*time.Minute + 30*time.Second))
	if len(h.deliveriesTo(ash.ID)) != 0 {
		t.Fatal("a missed run was made up for")
	}
	s.Loops.Tick(t0.Add(11 * time.Minute))
	if len(h.deliveriesTo(ash.ID)) != 1 {
		t.Fatal("restored loop did not fire")
	}
}

// TestForgetProject: deleting a project for good takes what the orchestration keeps of it,
// and nothing of another project.
func TestForgetProject(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	q, qwid := h.project("web")
	for _, pr := range []struct{ id, wid string }{{p.ID, wid}, {q.ID, qwid}} {
		if _, err := s.Loops.Create(LoopDef{ProjectID: pr.id, WorkspaceID: pr.wid, Name: "Guard", Enabled: true, Text: "Run the tests.",
			Trigger: LoopTrigger{Kind: "interval", EverySec: 60}, Targets: LoopTargets{Kind: "allAgents"}}, t0, false); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Memory.Write("project", pr.id, "# Memory\n\nfacts\n", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Memory.WriteSub(pr.id, "deploy", "Deploy", "how it ships", "steps\n", ""); err != nil {
			t.Fatal(err)
		}
		s.Threads.Add(ThreadMessage{ThreadID: pr.id, At: t0, Role: "hub", Kind: "note", Text: "hello"})
		s.Orders.Put(Order{ID: "o_" + pr.id[2:], ThreadID: pr.id, ProjectID: pr.id, Text: "x", CreatedAt: t0})
		if _, err := s.SetBlocked(pr.id, []string{"git push"}); err != nil {
			t.Fatal(err)
		}
	}
	card := "n_card000001"
	s.Threads.Add(ThreadMessage{ThreadID: card, At: t0, Role: "hub", Kind: "note", Text: "card"})

	s.ForgetProject(p.ID, []string{card})
	if len(s.Loops.List(p.ID)) != 0 || len(s.Loops.List(q.ID)) != 1 {
		t.Fatal("loops")
	}
	if msgs, _ := s.Threads.List(p.ID, "", 10); len(msgs) != 0 {
		t.Fatal("project thread kept")
	}
	if msgs, _ := s.Threads.List(card, "", 10); len(msgs) != 0 {
		t.Fatal("card thread kept")
	}
	if msgs, _ := s.Threads.List(q.ID, "", 10); len(msgs) != 1 {
		t.Fatal("other project's thread went")
	}
	if len(s.Orders.List(func(o Order) bool { return o.ProjectID == p.ID })) != 0 || len(s.Orders.List(nil)) != 1 {
		t.Fatal("orders")
	}
	if _, err := os.Stat(s.st.Path("memory", p.ID)); !os.IsNotExist(err) {
		t.Fatalf("memory folder kept: %v", err)
	}
	if _, err := os.Stat(s.st.Path("memory", "subs", p.ID)); !os.IsNotExist(err) {
		t.Fatalf("sub-memories kept: %v", err)
	}
	if doc, err := s.Memory.Read("project", q.ID); err != nil || doc.Content == "" {
		t.Fatal("other project's memory went")
	}
	if len(s.BlockedList(p.ID)) != 0 || len(s.BlockedList(q.ID)) != 1 {
		t.Fatal("blocked lists")
	}
}
