package orch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/session"
)

type splitCase struct {
	h             *fakeHub
	s             *Service
	pid, wid      string
	ash, bea, cid string
	parts         []ReplyPart
}

// newSplit is @Ash's result about to be split between @Bea and @Cid.
func newSplit(t *testing.T) *splitCase {
	t.Helper()
	h, s := newFakeHub(t)
	h.planner = true
	p, wid := h.project("app")
	return &splitCase{h: h, s: s, pid: p.ID, wid: wid,
		ash:   h.console(p, wid, "Ash", "claude", agentstate.Ready).ID,
		bea:   h.console(p, wid, "Bea", "claude", agentstate.Ready).ID,
		cid:   h.console(p, wid, "Cid", "codex", agentstate.Ready).ID,
		parts: []ReplyPart{{Handle: "Ash", Agent: "Claude Code", Text: "Login is done. Tests and docs remain."}}}
}

func (x *splitCase) start() {
	c := &execCtx{threadID: x.pid, projectID: x.pid, commandID: "fwd_t"}
	x.s.sendParts(c, []string{x.ash}, x.parts, []string{x.bea, x.cid}, "split", 2, forwardLine{})
}

func (x *splitCase) answer(t *testing.T, j jobs.Job) {
	t.Helper()
	x.h.lastRun(t, "planner").OnFinish(j)
}

func (x *splitCase) answerParts(t *testing.T, parts ...string) {
	t.Helper()
	list := []any{}
	for i := 0; i+1 < len(parts); i += 2 {
		list = append(list, map[string]any{"target": parts[i], "text": parts[i+1]})
	}
	x.answer(t, doneJob("", map[string]any{"parts": list}))
}

func (x *splitCase) thread() string {
	var all []string
	for _, m := range x.s.Threads.Last(x.pid, 40) {
		all = append(all, m.Text)
	}
	return strings.Join(all, "\n")
}

func (x *splitCase) delivered(t *testing.T, sid string, n int) []delivery.Delivery {
	t.Helper()
	ds := x.h.deliveriesTo(sid)
	if len(ds) != n {
		t.Fatalf("%d deliveries, want %d: %+v", len(ds), n, ds)
	}
	return ds
}

func (x *splitCase) threadSays(t *testing.T, want string) {
	t.Helper()
	if got := x.thread(); !strings.Contains(got, want) {
		t.Fatalf("thread does not say %q:\n%s", want, got)
	}
}

func TestSplitSchemaIsJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(SplitSchema, &v); err != nil {
		t.Fatal(err)
	}
}

func TestSplitGivesEachTargetItsOwnPart(t *testing.T) {
	x := newSplit(t)
	x.start()

	run := x.h.lastRun(t, "planner")
	if len(x.h.deliveriesTo(x.bea))+len(x.h.deliveriesTo(x.cid)) != 0 {
		t.Fatal("something was sent before the planner answered")
	}
	if string(run.Schema) != string(SplitSchema) {
		t.Errorf("schema %s", run.Schema)
	}
	for _, want := range []string{"Tests and docs remain", `"handle":"Bea"`, `"handle":"Cid"`, "Never drop, truncate or wipe a database"} {
		if !strings.Contains(run.Prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if !strings.Contains(x.thread(), "Splitting @Ash's result between @Bea and @Cid") {
		t.Errorf("thread:\n%s", x.thread())
	}

	x.answerParts(t, "Bea", "Write the tests.", "@cid", "Write the docs.")
	bea, cid := x.delivered(t, x.bea, 1), x.delivered(t, x.cid, 1)
	if len(bea) != 1 || !strings.Contains(bea[0].Text, "Write the tests.") || strings.Contains(bea[0].Text, "Write the docs.") {
		t.Errorf("Bea got %+v", bea)
	}
	if len(cid) != 1 || !strings.Contains(cid[0].Text, "Write the docs.") || strings.Contains(cid[0].Text, "Write the tests.") {
		t.Errorf("Cid got %+v", cid)
	}
	for _, d := range []delivery.Delivery{bea[0], cid[0]} {
		if d.Source != "forward" || d.ForwardDepth != 2 {
			t.Errorf("source %q depth %d: the loop limit must keep counting", d.Source, d.ForwardDepth)
		}
		if !strings.Contains(d.Text, "forwarded by NextBoard from @Ash (Claude Code)") {
			t.Errorf("the target cannot tell where this came from: %q", d.Text)
		}
	}
	x.threadSays(t, "Split @Ash's result between @Bea and @Cid.")
}

func TestSplitMayLeaveATargetFree(t *testing.T) {
	x := newSplit(t)
	x.start()
	x.answerParts(t, "Cid", "Write the docs.")
	x.delivered(t, x.cid, 1)
	x.threadSays(t, "between @Cid.")
	if n := len(x.h.deliveriesTo(x.bea)); n != 0 {
		t.Errorf("Bea got %d deliveries", n)
	}
}

// The planner's answer is untrusted: only the forward's targets get text.
func TestSplitOnlyReachesTheTargets(t *testing.T) {
	x := newSplit(t)
	x.start()
	x.answerParts(t, "Zed", "Ghost work.", "Ash", "Back to the source.", "Bea", "First part.", "Bea", "Second part.", "Cid", "   ")
	bea := x.delivered(t, x.bea, 1)
	x.threadSays(t, "between @Bea.")
	if len(bea) != 1 || !strings.Contains(bea[0].Text, "First part.\n\nSecond part.") {
		t.Errorf("repeats of one target must merge: %+v", bea)
	}
	if n := len(x.h.deliveriesTo(x.ash)); n != 0 {
		t.Errorf("the source got %d deliveries", n)
	}
	if n := len(x.h.deliveriesTo(x.cid)); n != 0 {
		t.Errorf("an empty part was sent %d times", n)
	}
	if got := x.thread(); !strings.Contains(got, "Zed") || !strings.Contains(got, "Ash, ") && !strings.Contains(got, ", Ash") {
		t.Errorf("dropped names are not reported:\n%s", got)
	}
}

func TestSplitFitsADelivery(t *testing.T) {
	x := newSplit(t)
	x.start()
	x.answerParts(t, "Bea", strings.Repeat("a", maxDigest), "Bea", strings.Repeat("b", maxDigest))
	bea := x.delivered(t, x.bea, 1)
	if n := len([]rune(bea[0].Text)); n > maxDigest || !strings.Contains(bea[0].Text, truncMark) {
		t.Errorf("%d characters, marker present: %v", n, strings.Contains(bea[0].Text, truncMark))
	}
}

// Whatever goes wrong, the results reach the first target whole.
func TestSplitFallsBackToTheWholeDigest(t *testing.T) {
	answers := map[string]struct {
		job  jobs.Job
		note string
	}{
		"failed job":     {failedJob("runner_failed"), "runner_failed"},
		"not signed in":  {failedJob("cli_not_logged_in"), "not signed in"},
		"timeout":        {failedJob("timeout"), "no answer in time"},
		"not JSON":       {doneJob("I cannot help with that.", nil), "invalid JSON"},
		"no parts":       {doneJob("", map[string]any{"parts": []any{}}), "no target any work"},
		"only strangers": {doneJob("", map[string]any{"parts": []any{map[string]any{"target": "Zed", "text": "x"}}}), "no target any work"},
	}
	for name, a := range answers {
		t.Run(name, func(t *testing.T) {
			x := newSplit(t)
			x.start()
			x.answer(t, a.job)
			bea := x.delivered(t, x.bea, 1)
			x.threadSays(t, "Forwarded @Ash's result to @Bea.")
			if bea[0].Text != Digest(x.parts) || bea[0].ForwardDepth != 2 {
				t.Errorf("%+v", bea[0])
			}
			if n := len(x.h.deliveriesTo(x.cid)); n != 0 {
				t.Errorf("Cid got %d deliveries", n)
			}
			if !strings.Contains(x.thread(), a.note) {
				t.Errorf("no reason %q in:\n%s", a.note, x.thread())
			}
		})
	}
}

func TestSplitWithoutAPlanner(t *testing.T) {
	t.Run("no claude", func(t *testing.T) {
		x := newSplit(t)
		x.h.planner = false
		x.start()
		if bea := x.h.deliveriesTo(x.bea); len(bea) != 1 || bea[0].Text != Digest(x.parts) {
			t.Errorf("%+v", bea)
		}
		if len(x.h.runs) != 0 {
			t.Errorf("runs %d", len(x.h.runs))
		}
	})
	t.Run("turned off", func(t *testing.T) {
		x := newSplit(t)
		x.h.set.Commands.Planner = "off"
		x.start()
		if bea := x.h.deliveriesTo(x.bea); len(bea) != 1 || len(x.h.runs) != 0 {
			t.Errorf("deliveries %+v runs %d", bea, len(x.h.runs))
		}
	})
	t.Run("job refused", func(t *testing.T) {
		x := newSplit(t)
		x.h.runErr = errors.New("queue full")
		x.start()
		if bea := x.h.deliveriesTo(x.bea); len(bea) != 1 || bea[0].Text != Digest(x.parts) {
			t.Errorf("%+v", bea)
		}
		if strings.Contains(x.thread(), "Splitting") {
			t.Errorf("says it is splitting:\n%s", x.thread())
		}
	})
	t.Run("one target needs no split", func(t *testing.T) {
		x := newSplit(t)
		c := &execCtx{threadID: x.pid, projectID: x.pid, commandID: "fwd_t"}
		x.s.sendParts(c, []string{x.ash}, x.parts, []string{x.bea}, "split", 1, forwardLine{})
		if bea := x.h.deliveriesTo(x.bea); len(bea) != 1 || len(x.h.runs) != 0 {
			t.Errorf("deliveries %+v runs %d", bea, len(x.h.runs))
		}
	})
}

// A planner that keeps failing stops being asked: each try costs a turn on the owner's plan.
func TestSplitFailuresCoolThePlannerDown(t *testing.T) {
	x := newSplit(t)
	for i := 1; i <= 2; i++ {
		x.start()
		x.answer(t, failedJob("runner_failed"))
		x.delivered(t, x.bea, i)
	}
	if ok, reason := x.s.PlannerAvailable(""); ok || reason != "cooldown" {
		t.Fatalf("available=%v reason=%q", ok, reason)
	}
	runs := len(x.h.runs)
	x.start()
	if len(x.h.runs) != runs {
		t.Error("asked the planner during its cooldown")
	}
	if n := len(x.h.deliveriesTo(x.bea)); n != 3 {
		t.Errorf("Bea got %d deliveries, want 3", n)
	}
}

func TestSplitCutShortByAHubRestart(t *testing.T) {
	x := newSplit(t)
	x.start()
	x.answer(t, jobs.Job{State: jobs.StateFailed, Code: "hub_restart"})
	if !strings.Contains(x.thread(), "Forward from @Ash not sent: the hub restarted") {
		t.Errorf("thread:\n%s", x.thread())
	}
	if n := len(x.h.deliveriesTo(x.bea)) + len(x.h.deliveriesTo(x.cid)); n != 0 {
		t.Errorf("%d deliveries while stopping", n)
	}
}

func TestRiskySplitWaitsForTheUser(t *testing.T) {
	start := func(t *testing.T) (*splitCase, Confirmation) {
		x := newSplit(t)
		x.start()
		x.answerParts(t, "Bea", "Write the tests.", "Cid", "Run rm -rf dist first.")
		x.threadSays(t, "can be dangerous")
		cf, ok := x.s.Confirms.Pending(x.pid, t0)
		if !ok || cf.Kind != ConfRisky || !strings.Contains(cf.Prompt, "@Cid") || !strings.Contains(cf.Prompt, "rm -rf") {
			t.Fatalf("confirmation %+v", cf)
		}
		// All or nothing: half a split is worse than none.
		if n := len(x.h.deliveriesTo(x.bea)) + len(x.h.deliveriesTo(x.cid)); n != 0 {
			t.Fatalf("%d deliveries before the answer", n)
		}
		return x, cf
	}
	t.Run("confirmed", func(t *testing.T) {
		x, cf := start(t)
		if _, err := x.s.Decide(cf.ID, "confirm", "", "owner"); err != nil {
			t.Fatal(err)
		}
		if bea := x.h.deliveriesTo(x.bea); len(bea) != 1 || !strings.Contains(bea[0].Text, "Write the tests.") {
			t.Errorf("Bea %+v", bea)
		}
		if cid := x.h.deliveriesTo(x.cid); len(cid) != 1 || !strings.Contains(cid[0].Text, "rm -rf dist") {
			t.Errorf("Cid %+v", cid)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		x, cf := start(t)
		if _, err := x.s.Decide(cf.ID, "cancel", "", "owner"); err != nil {
			t.Fatal(err)
		}
		if n := len(x.h.deliveriesTo(x.bea)) + len(x.h.deliveriesTo(x.cid)); n != 0 {
			t.Errorf("%d deliveries after cancel", n)
		}
	})
}

// newSplitEdge is a forward line from @Ash to a group holding @Bea and @Cid.
func newSplitEdge(t *testing.T) (*splitCase, string) {
	t.Helper()
	x := newSplit(t)
	na, nb, nc := x.h.node(x.wid, x.ash).ID, x.h.node(x.wid, x.bea).ID, x.h.node(x.wid, x.cid).ID
	const edgeID = "e_split00001"
	if err := x.h.ApplyOps(x.wid, []board.Op{
		{Op: "put", Node: groupNode("n_group00001", "Team", "backend", nb, nc)},
		{Op: "putEdge", Edge: &board.Edge{ID: edgeID, Kind: EdgeForward, Source: na, Target: "n_group00001", Data: json.RawMessage(`{"mode":"split","trigger":"turn"}`)}},
	}); err != nil {
		t.Fatal(err)
	}
	x.h.replies[x.ash] = &session.Reply{Text: "Login is done.", At: t0}
	return x, edgeID
}

func TestSplitOverAForwardLine(t *testing.T) {
	x, _ := newSplitEdge(t)
	finish(t, x.h, x.s, x.ash, t0)
	x.answerParts(t, "Bea", "Write the tests.", "Cid", "Write the docs.")
	if bea := x.delivered(t, x.bea, 1); bea[0].ForwardDepth != 1 {
		t.Errorf("depth %d", bea[0].ForwardDepth)
	}
	x.delivered(t, x.cid, 1)
}

func TestSplitOfHeldResultsOverAForwardLine(t *testing.T) {
	x, _ := newSplitEdge(t)
	finish(t, x.h, x.s, x.ash, t0)
	x.answerParts(t, "Bea", "First.", "Cid", "First too.")
	x.delivered(t, x.bea, 1)
	x.delivered(t, x.cid, 1)

	// Inside the line's minute the result waits, then goes through the planner like any other.
	x.h.mu.Lock()
	x.h.replies[x.ash] = &session.Reply{Text: "Second result.", At: t0.Add(30 * time.Second)}
	x.h.mu.Unlock()
	finish(t, x.h, x.s, x.ash, t0.Add(30*time.Second))
	before := len(x.h.runs)
	x.s.Now = func() time.Time { return t0.Add(61 * time.Second) }
	x.s.forwardsTick(t0.Add(61 * time.Second))
	if len(x.h.runs) != before+1 {
		t.Fatalf("runs %d, want %d", len(x.h.runs), before+1)
	}
	x.answerParts(t, "Bea", "Second.", "Cid", "Second too.")
	if bea := x.delivered(t, x.bea, 2); !strings.Contains(bea[1].Text, "Second.") {
		t.Errorf("%+v", bea[1])
	}
}

func TestLineRemovedWhileSplittingSendsNothing(t *testing.T) {
	x, edgeID := newSplitEdge(t)
	finish(t, x.h, x.s, x.ash, t0)
	if err := x.h.ApplyOps(x.wid, []board.Op{{Op: "delEdge", ID: edgeID}}); err != nil {
		t.Fatal(err)
	}
	x.answerParts(t, "Bea", "Write the tests.", "Cid", "Write the docs.")
	x.threadSays(t, "not sent: its line was removed")
	if n := len(x.h.deliveriesTo(x.bea)) + len(x.h.deliveriesTo(x.cid)); n != 0 {
		t.Errorf("%d deliveries over a removed line", n)
	}
}

func TestOneShotSplit(t *testing.T) {
	x := newSplit(t)
	x.h.replies[x.ash] = &session.Reply{Text: "Login is done.", At: t0}
	c := &execCtx{threadID: x.pid, projectID: x.pid, commandID: "cmd_t"}
	f, err := x.s.CreateForward(c, []string{x.ash}, []string{x.bea, x.cid}, "split")
	if err != nil {
		t.Fatal(err)
	}
	x.s.mu.Lock()
	x.s.forwards[f.ID].State, x.s.forwards[f.ID].OrderID = "collecting", "o_none000001"
	x.s.forwards[f.ID].Done = []string{x.ash}
	x.s.mu.Unlock()
	x.s.checkForward(f.ID, t0)
	x.answerParts(t, "Bea", "Write the tests.", "Cid", "Write the docs.")
	x.delivered(t, x.bea, 1)
	x.delivered(t, x.cid, 1)
}
