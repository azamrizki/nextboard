package orch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/jobs"
)

func summaryRuns(h *fakeHub) []jobs.RunnerRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []jobs.RunnerRequest
	for _, r := range h.runs {
		if r.Purpose == "handoff-summary" {
			out = append(out, r)
		}
	}
	return out
}

// What a restart leaves, Start's part included but not its ticker: the tests tick by hand.
func restartedHub(h *fakeHub, s *Service) *Service {
	next := afterRestart(h, s)
	next.recoverHandoffs(next.Now())
	return next
}

func handoffState(t *testing.T, s *Service, id, want string) Handoff {
	t.Helper()
	v, ok := s.HandoffView(id)
	if !ok || v.State != want {
		t.Fatalf("state %+v, want %s", v, want)
	}
	return v
}

// The summary job dies with the hub: it is written again and the handoff goes through.
func TestHandoffSurvivesRestartWhileSummarizing(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.transcr[src.ID] = "user: build the login page\n\nassistant: Login form done."
	h.onMach["local/codex"] = true
	ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex", Note: "keep the styling"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "summary job", func() bool { return len(summaryRuns(h)) == 1 })
	waitFor(t, "summarizing", func() bool { v, _ := s.HandoffView(ho.ID); return v.State == "summarizing" })

	s2 := restartedHub(h, s)
	if _, err := s2.StartHandoff(src.ID, HandoffRequest{Agent: "codex"}); !errors.Is(err, ErrHandoffRunning) {
		t.Fatalf("a second handoff next to the one going on: %v", err)
	}
	if notes := strings.Join(threadTexts(s2, p.ID, "note"), "|"); !strings.Contains(notes, "The hub restarted during the handoff from @Ash") {
		t.Fatalf("notes %s", notes)
	}
	s2.Tick(t0.Add(time.Second))
	waitFor(t, "a new summary job", func() bool { return len(summaryRuns(h)) == 2 })
	runs := summaryRuns(h)
	if !strings.Contains(runs[1].Prompt, "Login form done") || !strings.Contains(runs[1].Prompt, "keep the styling") {
		t.Fatalf("what the note and the transcript were: %s", runs[1].Prompt)
	}
	runs[1].OnFinish(doneJob("## Goal\nLogin page.", nil))
	v := handoffState(t, s2, ho.ID, "waitingReady")
	h.setState(*v.TargetSessionID, agentstate.Ready)
	s2.Tick(t0.Add(2 * time.Second))
	ds := h.deliveriesTo(*v.TargetSessionID)
	if len(ds) != 1 || !strings.HasPrefix(ds[0].Text, "## Goal") {
		t.Fatalf("deliveries %+v", ds)
	}
	h.setDelivery(s2, ds[0].ID, delivery.Confirmed)
	s2.Tick(t0.Add(3 * time.Second))
	handoffState(t, s2, ho.ID, "done")

	// Finished, it stays finished through the next restart, and another may start.
	s3 := restartedHub(h, s2)
	s3.Tick(t0.Add(4 * time.Second))
	handoffState(t, s3, ho.ID, "done")
	if len(h.deliveriesTo(*v.TargetSessionID)) != 1 {
		t.Fatal("typed again")
	}
	if _, err := s3.StartHandoff(src.ID, HandoffRequest{Agent: "codex"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "its summary", func() bool { return len(summaryRuns(h)) == 3 })
}

// A console that is not back yet gets time: an empty transcript would mean nothing was done.
func TestHandoffWaitsForItsConsole(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.transcr[src.ID] = "user: x\n\nassistant: y"
	h.onMach["local/codex"] = true
	ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "summary job", func() bool { return len(summaryRuns(h)) == 1 })
	waitFor(t, "summarizing", func() bool { v, _ := s.HandoffView(ho.ID); return v.State == "summarizing" })
	h.mu.Lock()
	h.sessions[src.ID].Status = "starting"
	h.mu.Unlock()
	s2 := restartedHub(h, s)
	s2.Tick(s2.Now().Add(time.Second))
	time.Sleep(50 * time.Millisecond)
	if n := len(summaryRuns(h)); n != 1 {
		t.Fatalf("did not wait: %d jobs", n)
	}
	s2.Tick(s2.Now().Add(handoffBackAfter + time.Second))
	waitFor(t, "it goes on after the wait", func() bool { return len(summaryRuns(h)) == 2 })
}

// Waiting for the new console costs nothing: it just goes on, and types once.
func TestHandoffSurvivesRestartWhileWaitingForReady(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.transcr[src.ID] = "user: x\n\nassistant: y"
	h.onMach["local/codex"] = true
	ho, _ := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex"})
	waitFor(t, "summary job", func() bool { return len(summaryRuns(h)) == 1 })
	summaryRuns(h)[0].OnFinish(doneJob("## Goal\nStay the course.", nil))
	v := handoffState(t, s, ho.ID, "waitingReady")

	s2 := restartedHub(h, s)
	if got := handoffState(t, s2, ho.ID, "waitingReady"); got.Brief != "## Goal\nStay the course." || got.TargetSessionID == nil {
		t.Fatalf("after restart %+v", got)
	}
	// The wait starts over: the console restarts with the hub.
	s2.Tick(s2.Now())
	s2.Tick(s2.Now().Add(handoffReady - time.Second))
	handoffState(t, s2, ho.ID, "waitingReady")
	h.setState(*v.TargetSessionID, agentstate.Ready)
	s2.Tick(s2.Now().Add(handoffReady - time.Second))
	if ds := h.deliveriesTo(*v.TargetSessionID); len(ds) != 1 {
		t.Fatalf("deliveries %+v", ds)
	}
	if n := len(summaryRuns(h)); n != 1 {
		t.Fatalf("summarised again: %d", n)
	}
}

// Going out when the hub stopped: maybe typed, maybe not. It is not typed a second time.
func TestHandoffRestartWhileDeliveringGoesToTheOwner(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.transcr[src.ID] = "user: x\n\nassistant: y"
	h.onMach["local/codex"] = true
	ho, _ := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex"})
	waitFor(t, "summary job", func() bool { return len(summaryRuns(h)) == 1 })
	summaryRuns(h)[0].OnFinish(doneJob("## Goal\nDo not repeat me.", nil))
	v := handoffState(t, s, ho.ID, "waitingReady")
	h.setState(*v.TargetSessionID, agentstate.Ready)
	s.Tick(t0.Add(time.Second))
	handoffState(t, s, ho.ID, "delivering")

	s2 := restartedHub(h, s)
	handoffState(t, s2, ho.ID, "manual")
	s2.Tick(t0.Add(2 * time.Second))
	if ds := h.deliveriesTo(*v.TargetSessionID); len(ds) != 1 {
		t.Fatalf("typed twice: %+v", ds)
	}
	if got := strings.Join(threadTexts(s2, p.ID, "reply"), "|"); !strings.Contains(got, "Do not repeat me") || !strings.Contains(got, "may have it already") {
		t.Fatalf("thread %s", got)
	}
}

// Switching in place, the restart can come before or after the switch.
func TestHandoffRestartWhileSwitching(t *testing.T) {
	for _, switched := range []bool{false, true} {
		h, s := newFakeHub(t)
		p, wid := h.project("app")
		src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
		h.onMach["local/codex"] = true
		ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex", InPlace: true, NoBrief: true})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the switch", func() bool { v, _ := s.HandoffView(ho.ID); return v.final() })
		h.mu.Lock()
		if switched {
			h.sessions[src.ID].Agent = "codex"
		} else {
			h.sessions[src.ID].Agent = "claude"
		}
		h.calls = nil
		h.mu.Unlock()
		s.setHandoff(ho.ID, func(x *Handoff) { x.State, x.Brief = "switching", "## Goal\nThe brief." })

		s2 := restartedHub(h, s)
		s2.Tick(s2.Now())
		if switched {
			got := handoffState(t, s2, ho.ID, "waitingReady")
			if got.TargetSessionID == nil || *got.TargetSessionID != src.ID {
				t.Fatalf("%+v", got)
			}
			h.mu.Lock()
			calls := len(h.calls)
			h.mu.Unlock()
			if calls != 0 {
				t.Fatal("switched a second time")
			}
			continue
		}
		waitFor(t, "the switch happens", func() bool { in, _ := h.SessionInfo(src.ID); return in.Agent == "codex" })
		waitFor(t, "and it waits for the new agent", func() bool { v, _ := s2.HandoffView(ho.ID); return v.State == "waitingReady" })
	}
}

// Started before the restart and not yet spawned: it spawns now, with the brief it had.
func TestHandoffRestartWhileSpawning(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.transcr[src.ID] = "user: x\n\nassistant: y"
	h.onMach["local/codex"] = true
	ho, _ := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex"})
	waitFor(t, "summary job", func() bool { return len(summaryRuns(h)) == 1 })
	waitFor(t, "summarizing", func() bool { v, _ := s.HandoffView(ho.ID); return v.State == "summarizing" })
	s.setHandoff(ho.ID, func(x *Handoff) { x.State, x.Brief = "spawning", "## Goal\nThe brief." })

	s2 := restartedHub(h, s)
	s2.Tick(s2.Now())
	waitFor(t, "the console", func() bool {
		v, _ := s2.HandoffView(ho.ID)
		return v.State == "waitingReady" && v.TargetSessionID != nil
	})
	if n := len(summaryRuns(h)); n != 1 {
		t.Fatalf("summarised again: %d", n)
	}
}

// Its console deleted meanwhile: it stops, and says so in plain words.
func TestHandoffRestartWithoutItsConsole(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.transcr[src.ID] = "user: x\n\nassistant: y"
	h.onMach["local/codex"] = true
	ho, _ := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex"})
	waitFor(t, "summary job", func() bool { return len(summaryRuns(h)) == 1 })
	waitFor(t, "summarizing", func() bool { v, _ := s.HandoffView(ho.ID); return v.State == "summarizing" })
	h.mu.Lock()
	delete(h.sessions, src.ID)
	h.mu.Unlock()
	s2 := restartedHub(h, s)
	s2.Tick(s2.Now())
	handoffState(t, s2, ho.ID, "failed")
	got := strings.Join(threadTexts(s2, p.ID, "reply"), "|")
	if !strings.Contains(got, "its console is gone") || strings.Contains(got, "Copy this brief") {
		t.Fatalf("thread %s", got)
	}
	if n := len(summaryRuns(h)); n != 1 {
		t.Fatalf("summarised for nobody: %d", n)
	}
}

// The real Start takes them up too.
func TestStartTakesUpHandoffs(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.transcr[src.ID] = "user: x\n\nassistant: y"
	h.onMach["local/codex"] = true
	ho, _ := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex"})
	waitFor(t, "summary job", func() bool { return len(summaryRuns(h)) == 1 })
	waitFor(t, "summarizing", func() bool { v, _ := s.HandoffView(ho.ID); return v.State == "summarizing" })
	s2 := afterRestart(h, s)
	s2.Start()
	defer s2.Close()
	waitFor(t, "a new summary job", func() bool { return len(summaryRuns(h)) == 2 })
	if v, ok := s2.HandoffView(ho.ID); !ok || v.final() {
		t.Fatalf("%+v", v)
	}
}
