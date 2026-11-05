package orch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"nextboard/internal/agentstate"
	"nextboard/internal/jobs"
	"nextboard/internal/session"
)

func (h *fakeHub) lastDelivery(sid string) string {
	ds := h.deliveriesTo(sid)
	if len(ds) == 0 {
		return ""
	}
	return ds[len(ds)-1].Text
}

func (h *fakeHub) called(prefix string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// TestSwitchInPlace: the same console takes the next agent, which is told where
// things stand; nothing new appears on the board.
func TestSwitchInPlace(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Working)
	h.transcr[src.ID] = "user: build the login page\n\nassistant: Login form done; tests pending."
	h.onMach["local/codex"] = true
	consoles := len(h.Sessions())

	ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex", InPlace: true, Note: "keep the styling"})
	if err != nil || !ho.InPlace {
		t.Fatalf("%+v %v", ho, err)
	}
	var run jobs.RunnerRequest
	waitFor(t, "summary job", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, r := range h.runs {
			if r.Purpose == "handoff-summary" {
				run = r
				return true
			}
		}
		return false
	})
	if len(h.called("pause:"+src.ID)) != 1 {
		t.Fatalf("a working agent must stand still while its work is written down: %v", h.called(""))
	}
	if len(h.called("switch:")) != 0 {
		t.Fatal("switched before the summary")
	}
	run.OnFinish(doneJob("## Goal\nLogin page. Next: tests.", nil))
	v, _ := s.HandoffView(ho.ID)
	if v.State != "waitingReady" || v.TargetSessionID == nil || *v.TargetSessionID != src.ID {
		t.Fatalf("after summary %+v", v)
	}
	if got := h.called("switch:"); len(got) != 1 || got[0] != "switch:"+src.ID+":codex:" {
		t.Fatalf("switch %v", got)
	}
	if len(h.called("resume:"+src.ID)) != 1 {
		t.Fatal("a paused process must be woken before it is stopped")
	}
	if n := len(h.Sessions()); n != consoles {
		t.Fatalf("%d consoles, was %d: nothing new may appear", n, consoles)
	}
	h.setState(src.ID, agentstate.Ready)
	s.Tick(t0.Add(time.Second))
	got := h.lastDelivery(src.ID)
	if !strings.Contains(got, "Login page. Next: tests.") || !strings.Contains(got, HandoffContinue) || strings.Contains(got, "You are back") {
		t.Fatalf("brief %q", got)
	}
}

// The old process's Ready must not count: the brief would be typed into nothing.
func TestSwitchWaitsForTheNewProcess(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.transcr[src.ID] = "user: hi\n\nassistant: hello"
	h.onMach["local/codex"] = true
	h.switchKeepsStart = true
	ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex", InPlace: true})
	if err != nil {
		t.Fatal(err)
	}
	var run jobs.RunnerRequest
	waitFor(t, "summary job", func() bool {
		r, ok := h.findRun("handoff-summary")
		run = r
		return ok
	})
	run.OnFinish(doneJob("brief", nil))
	s.Tick(t0.Add(time.Second))
	if v, _ := s.HandoffView(ho.ID); v.State != "waitingReady" || h.lastDelivery(src.ID) != "" {
		t.Fatalf("delivered to the old process: %+v", v)
	}
}

func TestSwitchWithoutBrief(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Working)
	h.transcr[src.ID] = "user: x\n\nassistant: y"
	h.onMach["local/codex"] = true
	ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex", InPlace: true, NoBrief: true})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "done", func() bool { v, _ := s.HandoffView(ho.ID); return v.State == "done" })
	if _, ok := h.findRun("handoff-summary"); ok {
		t.Fatal("no summary was asked for: it costs a run")
	}
	if len(h.called("pause:")) != 0 {
		t.Fatal("nothing to write down, nothing to pause")
	}
}

// An agent that has done nothing has nothing to hand on, and no run is spent.
func TestSwitchWithNothingToSay(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.onMach["local/codex"] = true
	ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex", InPlace: true})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "done", func() bool { v, _ := s.HandoffView(ho.ID); return v.State == "done" })
	if _, ok := h.findRun("handoff-summary"); ok {
		t.Fatal("no conversation, no summary")
	}
	if got := h.called("switch:"); len(got) != 1 {
		t.Fatalf("switch %v", got)
	}
}

// TestGoingBackContinuesTheConversation: an agent that ran here before picks up
// its own conversation, and hears what happened meanwhile.
func TestGoingBackContinuesTheConversation(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "codex", agentstate.Ready)
	h.transcr[src.ID] = "user: x\n\nassistant: tests added"
	h.onMach["local/claude"] = true
	h.setPastRuns(src.ID, []session.PastRun{{Agent: "claude", AgentSessionID: "conv-1", ProfileID: "pf_1"}})

	ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "claude", InPlace: true})
	if err != nil || !ho.Returning {
		t.Fatalf("%+v %v", ho, err)
	}
	var run jobs.RunnerRequest
	waitFor(t, "summary job", func() bool {
		r, ok := h.findRun("handoff-summary")
		run = r
		return ok
	})
	run.OnFinish(doneJob("Tests were added.", nil))
	if got := h.called("switch:"); len(got) != 1 || got[0] != "switch:"+src.ID+":claude:conv-1" {
		t.Fatalf("switch %v", got)
	}
	if v, _ := s.HandoffView(ho.ID); v.req.ProfileID != "pf_1" {
		t.Fatalf("its own login: %+v", v.req)
	}
	h.setState(src.ID, agentstate.Ready)
	s.Tick(t0.Add(time.Second))
	if got := h.lastDelivery(src.ID); !strings.Contains(got, "You are back") || !strings.Contains(got, "Tests were added.") {
		t.Fatalf("brief %q", got)
	}

	// Asked to start over, it does.
	src2 := h.console(p, wid, "Bea", "codex", agentstate.Ready)
	h.setPastRuns(src2.ID, []session.PastRun{{Agent: "claude", AgentSessionID: "conv-2"}})
	ho, err = s.StartHandoff(src2.ID, HandoffRequest{Agent: "claude", InPlace: true, Fresh: true, NoBrief: true})
	if err != nil || ho.Returning {
		t.Fatalf("%+v %v", ho, err)
	}
	waitFor(t, "fresh switch", func() bool { return len(h.called("switch:"+src2.ID)) == 1 })
	if got := h.called("switch:" + src2.ID); got[0] != "switch:"+src2.ID+":claude:" {
		t.Fatalf("fresh %v", got)
	}
}

// A switch that fails leaves the old agent running: no brief is lost, and if it
// was paused for the summary it gets its turn back.
func TestSwitchFailureKeepsTheOldAgent(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Working)
	h.transcr[src.ID] = "user: x\n\nassistant: y"
	h.onMach["local/codex"] = true
	h.switchErr = errors.New("no such login")
	ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex", InPlace: true})
	if err != nil {
		t.Fatal(err)
	}
	var run jobs.RunnerRequest
	waitFor(t, "summary job", func() bool {
		r, ok := h.findRun("handoff-summary")
		run = r
		return ok
	})
	run.OnFinish(doneJob("brief text", nil))
	v, _ := s.HandoffView(ho.ID)
	if v.State != "failed" || !strings.Contains(v.Error, "no such login") {
		t.Fatalf("%+v", v)
	}
	if len(h.called("resume:"+src.ID)) == 0 {
		t.Fatal("the old agent stayed frozen")
	}
}

// A next agent that falls back to the prompt is said at once, with the brief kept.
func TestSwitchNoticesAnAgentThatDidNotStart(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.transcr[src.ID] = "user: x\n\nassistant: y"
	h.onMach["local/codex"] = true
	ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex", InPlace: true})
	if err != nil {
		t.Fatal(err)
	}
	var run jobs.RunnerRequest
	waitFor(t, "summary job", func() bool {
		r, ok := h.findRun("handoff-summary")
		run = r
		return ok
	})
	run.OnFinish(doneJob("brief text", nil))
	h.setState(src.ID, agentstate.Shell)
	s.Tick(t0.Add(time.Second)) // the wrapper's shell shows at every launch
	if v, _ := s.HandoffView(ho.ID); v.State != "waitingReady" {
		t.Fatalf("too soon: %+v", v)
	}
	s.Tick(t0.Add(handoffStartGrace + time.Second))
	v, _ := s.HandoffView(ho.ID)
	if v.State != "manual" || !strings.Contains(v.Error, "did not start") {
		t.Fatalf("%+v", v)
	}
}

func TestSwitchRefusesAnAgentNotInstalled(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.onMach["local/codex"] = false
	if _, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex", InPlace: true}); !errors.Is(err, ErrHandoffAgent) {
		t.Fatalf("%v", err)
	}
}
