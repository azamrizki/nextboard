package orch

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"nextboard/internal/agentstate"
	"nextboard/internal/board"
	"nextboard/internal/delivery"
	"nextboard/internal/jobs"
	"nextboard/internal/session"
)

// ---- memory (§12, §19.16) ----

func TestMemoryStoreAcceptPush(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	d, err := s.WriteMemory(p.ID, "project", "## Commands\n- make test runs everything\n", "")
	if err != nil || d.Mtime == "" {
		t.Fatal(err)
	}
	if _, err := s.WriteMemory(p.ID, "project", "x", "2001-01-01T00:00:00Z"); !errors.Is(err, ErrMtimeMismatch) {
		t.Fatalf("ifMtime: %v", err)
	}
	if _, err := s.WriteMemory(p.ID, "project", strings.Repeat("x", MaxMemory+1), ""); !errors.Is(err, ErrMemoryTooBig) {
		t.Fatal(err)
	}
	if _, err := s.WriteMemory(p.ID, "team", "x", ""); !errors.Is(err, ErrBadScope) {
		t.Fatal(err)
	}
	d, err = s.AcceptMemory(p.ID, "project", []Fact{{Kind: "commands", Text: "make test runs everything"}, {Kind: "commands", Text: "make lint checks style"},
		{Kind: "gotchas", Text: "  - The hub needs Go 1.26  "}})
	if err != nil {
		t.Fatal(err)
	}
	want := "## Commands\n- make test runs everything\n- make lint checks style\n\n## Gotchas\n- The hub needs Go 1.26\n"
	if d.Content != want {
		t.Fatalf("accept:\n%q\nwant\n%q", d.Content, want)
	}
	// Push: ≤ 4000 characters, facts that do not fit are dropped and counted.
	var facts []Fact
	for i := 0; i < 80; i++ {
		facts = append(facts, Fact{Kind: "context", Text: strings.Repeat("fact ", 20) + itoa(i)})
	}
	text := PushText(AcceptFacts("", facts))
	if !strings.HasPrefix(text, MemoryPushHead) || !strings.Contains(text, "more facts not included)") || len([]rune(text)) > 4000+len(MemoryPushHead)+60 {
		t.Fatalf("push text %d chars: %s", len([]rune(text)), text[len(text)-60:])
	}
	// Push via a memory node wired to a group.
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.Working)
	cas := h.console(p, wid, "Cass", "claude", agentstate.Ready)
	h.mu.Lock()
	h.sessions[cas.ID].Paused = true
	h.mu.Unlock()
	dov := h.console(p, wid, "Dov", "shell", "")
	mem, _ := json.Marshal(map[string]any{"scope": "project"})
	if err := h.ApplyOps(wid, []board.Op{
		{Op: "put", Node: groupNode("n_grp0000001", "Team", "general", h.node(wid, ash.ID).ID, h.node(wid, bea.ID).ID, h.node(wid, cas.ID).ID, h.node(wid, dov.ID).ID)},
		{Op: "put", Node: &board.Node{ID: "n_mem0000001", Kind: KindMemory, W: 300, H: 200, Data: mem}},
		{Op: "putEdge", Edge: &board.Edge{ID: "e_wire000001", Kind: EdgeWire, Source: "n_mem0000001", Target: "n_grp0000001"}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.PushMemory(p.ID, wid, "n_mem0000001", nil, []string{"project"})
	if err != nil || res.Delivered != 1 || res.Busy != 1 || res.Skipped != 1 || res.NotAgent != 1 || len(res.DeliveryIDs) != 2 {
		t.Fatalf("%v %+v", err, res)
	}
	if ds := h.deliveriesTo(ash.ID); len(ds) != 1 || !strings.Contains(ds[0].Text, "make lint checks style") || ds[0].Source != "memory" {
		t.Fatalf("push delivery %+v", ds)
	}
	b, _ := h.Board(wid)
	for _, n := range b.Nodes {
		if n.ID == "n_mem0000001" && !strings.Contains(string(n.Data), `"lastPush"`) {
			t.Fatalf("lastPush %s", n.Data)
		}
	}
}

// TestMemoryScanStoresNothing: the scan is a read-only runner job whose
// result is {"facts":[…]}; the memory file is untouched until accept.
func TestMemoryScanStoresNothing(t *testing.T) {
	h, s := newFakeHub(t)
	p, _ := h.project("app")
	if _, err := s.ScanMemory(p.ID, "thorough", ""); err != nil {
		t.Fatal(err)
	}
	run := h.lastRun(t, "memory-scan")
	if run.MachineID != "local" || run.Cwd != "/work/app" || run.Agent != "claude" || string(run.Schema) != string(ScanSchema) ||
		!strings.Contains(run.Prompt, "11 to 14") || run.Transform == nil {
		t.Fatalf("%+v", run)
	}
	out, err := run.Transform("", json.RawMessage(`{"facts":[{"kind":"Commands","text":"go test ./..."},{"kind":"weird","text":"x"}]}`))
	if err != nil || string(out) != `{"facts":[{"kind":"commands","text":"go test ./..."},{"kind":"context","text":"x"}]}` {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := run.Transform("not json", nil); err == nil {
		t.Fatal("bad output accepted")
	}
	if _, err := os.Stat(h.st.Path("memory", p.ID+".md")); err == nil {
		t.Fatal("scan wrote memory")
	}
}

// ---- forwards (§10, §19.14) ----

func TestDigestLimits(t *testing.T) {
	d := Digest([]ReplyPart{{Handle: "Ash", Agent: "Claude Code", Text: "A done"}, {Handle: "Bo", Agent: "Codex", Text: "B done"}})
	want := "Results forwarded by NextBoard from @Ash (Claude Code) and @Bo (Codex):\n\n## @Ash\nA done\n\n## @Bo\nB done\n\nUse these results for your next step."
	if d != want {
		t.Fatalf("%q", d)
	}
	big := Digest([]ReplyPart{{Handle: "Ash", Text: strings.Repeat("a", 25000)}})
	if !strings.Contains(big, truncMark) || len([]rune(big)) > maxReplyForward+300 {
		t.Fatalf("per reply %d", len([]rune(big)))
	}
	var parts []ReplyPart
	for i := 0; i < 5; i++ {
		parts = append(parts, ReplyPart{Handle: "H" + itoa(i), Text: strings.Repeat("x", 19000)})
	}
	all := Digest(parts)
	if len([]rune(all)) > maxDigest || !strings.Contains(all, truncMark) || !strings.HasSuffix(all, "Use these results for your next step.") {
		t.Fatalf("digest %d", len([]rune(all)))
	}
}

func finish(t *testing.T, h *fakeHub, s *Service, sid string, at time.Time) {
	t.Helper()
	s.Now = func() time.Time { return at }
	in, _ := h.SessionInfo(sid)
	end := at
	s.OnAgentEvent(in, agentstate.Event{Kind: agentstate.EvFinished, Turn: agentstate.LastTurn{StartedAt: at.Add(-time.Minute), EndedAt: &end}})
}

// TestOneShotForward: "when Ash finishes, pass it to Bea".
func TestOneShotForward(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "codex", agentstate.Ready)
	r := cmd(t, s, wid, "when Ash finishes, pass it to Bea")
	fws := s.Forwards(p.ID)
	if len(fws) != 1 || fws[0].State != "waiting" || !strings.Contains(texts(r), "forward the results to @Bea") {
		t.Fatalf("%+v %s", fws, texts(r))
	}
	r = cmd(t, s, wid, "@Ash write the API docs")
	if fws = s.Forwards(p.ID); fws[0].State != "collecting" || fws[0].OrderID != r.OrderID {
		t.Fatalf("attach %+v", fws)
	}
	h.setDelivery(s, h.deliveriesTo(ash.ID)[0].ID, delivery.Confirmed)
	h.replies[ash.ID] = &session.Reply{Text: "Docs are in docs/api.md", At: t0}
	finish(t, h, s, ash.ID, t0.Add(time.Minute))
	ds := h.deliveriesTo(bea.ID)
	if len(ds) != 1 || ds[0].Source != "forward" || ds[0].ForwardDepth != 1 || !strings.Contains(ds[0].Text, "## @Ash\nDocs are in docs/api.md") {
		t.Fatalf("forwarded %+v", ds)
	}
	if fws = s.Forwards(p.ID); fws[0].State != "sent" {
		t.Fatalf("state %+v", fws)
	}
	// Forward to itself is rejected.
	if r := cmd(t, s, wid, "when Ash finishes, pass it to Ash"); !strings.Contains(texts(r), "forward:") {
		t.Fatalf("cycle %s", texts(r))
	}
	// Without an order within 4 h it expires.
	h.console(p, wid, "Cass", "claude", agentstate.Ready)
	cmd(t, s, wid, "when Cass finishes, pass it to Ash")
	s.Tick(t0.Add(4*time.Hour + 2*time.Minute))
	exp := 0
	for _, f := range s.Forwards(p.ID) {
		if f.State == "expired" {
			exp++
		}
	}
	if exp != 1 {
		t.Fatalf("expiry %+v", s.Forwards(p.ID))
	}
}

// TestForwardEdgesLoopProtection: edges fire on finished turns, ≤ 1 per
// 60 s per source, never beyond forwardDepth 3; trigger "order" ignores
// turns started by hand; a group source waits for all members.
func TestForwardEdgesLoopProtection(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	b := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	c := h.console(p, wid, "Cass", "claude", agentstate.Ready)
	na, nb, nc := h.node(wid, a.ID).ID, h.node(wid, b.ID).ID, h.node(wid, c.ID).ID
	edge := func(id, src, dst, data string) board.Op {
		return board.Op{Op: "putEdge", Edge: &board.Edge{ID: id, Kind: EdgeForward, Source: src, Target: dst, Data: json.RawMessage(data)}}
	}
	if err := h.ApplyOps(wid, []board.Op{edge("e_fwd0000001", na, nb, `{"mode":"digest","trigger":"turn"}`)}); err != nil {
		t.Fatal(err)
	}
	h.replies[a.ID] = &session.Reply{Text: "A result", At: t0}
	finish(t, h, s, a.ID, t0) // a turn started by hand: depth 0 → 1
	if ds := h.deliveriesTo(b.ID); len(ds) != 1 || ds[0].ForwardDepth != 1 {
		t.Fatalf("edge %+v", ds)
	}
	finish(t, h, s, a.ID, t0.Add(30*time.Second))
	if len(h.deliveriesTo(b.ID)) != 1 {
		t.Fatal("edge fired twice within 60 s")
	}
	// A turn that came from a depth-3 delivery is not forwarded further.
	s.OnDelivery(delivery.Delivery{ID: "dl_deep000001", SessionID: a.ID, OrderID: "o_none000001", ForwardDepth: 3, State: delivery.Sent})
	s.mu.Lock()
	s.turnDepth[a.ID] = 3
	s.mu.Unlock()
	finish(t, h, s, a.ID, t0.Add(2*time.Minute))
	if len(h.deliveriesTo(b.ID)) != 1 {
		t.Fatal("forwarded beyond depth 3")
	}
	found := false
	for _, m := range s.Threads.Last(p.ID, 20) {
		found = found || strings.Contains(m.Text, "forward depth limit")
	}
	if !found {
		t.Fatal("no depth note")
	}
	// trigger "order": a turn started by hand is not forwarded.
	if err := h.ApplyOps(wid, []board.Op{edge("e_fwd0000002", nb, nc, `{"mode":"digest","trigger":"order"}`)}); err != nil {
		t.Fatal(err)
	}
	h.replies[b.ID] = &session.Reply{Text: "B result", At: t0}
	finish(t, h, s, b.ID, t0.Add(3*time.Minute))
	if len(h.deliveriesTo(c.ID)) != 0 {
		t.Fatal("order trigger fired for a hand-started turn")
	}
	r := cmd(t, s, wid, "@Bea summarise")
	h.setDelivery(s, h.deliveriesTo(b.ID)[1].ID, delivery.Confirmed)
	h.mu.Lock()
	h.replies[b.ID] = &session.Reply{Text: "B summary", At: t0.Add(4 * time.Minute)} // this turn's reply
	h.mu.Unlock()
	finish(t, h, s, b.ID, t0.Add(4*time.Minute))
	if ds := h.deliveriesTo(c.ID); len(ds) != 1 || ds[0].ForwardDepth != 1 {
		t.Fatalf("order trigger %+v (order %s)", ds, r.OrderID)
	}
	// Cycle via the board is impossible (validated), so ping-pong between
	// two consoles cannot be built from edges.
	if code, _ := codeOf(h.ApplyOps(wid, []board.Op{edge("e_fwd0000003", nc, na, `{}`)})); code != "forward_cycle" {
		t.Fatalf("cycle %s", code)
	}
}

// TestForwardWaitsForThisTurnsReply: at turn end the hub still holds the
// previous reply (it refreshes the transcript asynchronously); the forward
// must wait for and carry the new one instead of the stale text.
func TestForwardWaitsForThisTurnsReply(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	a := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	b := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	op := board.Op{Op: "putEdge", Edge: &board.Edge{ID: "e_fwd0000009", Kind: EdgeForward,
		Source: h.node(wid, a.ID).ID, Target: h.node(wid, b.ID).ID, Data: json.RawMessage(`{"mode":"digest","trigger":"turn"}`)}}
	if err := h.ApplyOps(wid, []board.Op{op}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.replies[a.ID] = &session.Reply{Text: "old reply", At: t0, MessageID: "m1"}
	h.mu.Unlock()
	finish(t, h, s, a.ID, t0.Add(10*time.Minute))
	if n := len(h.deliveriesTo(b.ID)); n != 0 {
		t.Fatalf("forwarded before the reply was refreshed (%d)", n)
	}
	h.mu.Lock()
	h.replies[a.ID] = &session.Reply{Text: "new reply", At: t0.Add(10 * time.Minute), MessageID: "m2"}
	h.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for len(h.deliveriesTo(b.ID)) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	ds := h.deliveriesTo(b.ID)
	if len(ds) != 1 || !strings.Contains(ds[0].Text, "new reply") || strings.Contains(ds[0].Text, "old reply") {
		t.Fatalf("forward %+v", ds)
	}
}

// ---- handoff (§9, §19.13) ----

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestHandoffFlow(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	src := h.console(p, wid, "Ash", "claude", agentstate.Working)
	h.transcr[src.ID] = "user: build the login page\n\nassistant: Login form done; tests pending."
	h.onMach["local/codex"] = true
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: groupNode("n_grp0000001", "Web", "frontend", h.node(wid, src.ID).ID)}}); err != nil {
		t.Fatal(err)
	}
	ho, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex", Note: "keep the styling"})
	if err != nil || ho.State != "collecting" {
		t.Fatal(err)
	}
	if _, err := s.StartHandoff(src.ID, HandoffRequest{Agent: "codex"}); !errors.Is(err, ErrHandoffRunning) {
		t.Fatalf("second handoff: %v", err)
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
	if run.Agent != "claude" || !strings.Contains(run.Prompt, "Login form done") || !strings.Contains(run.Prompt, "keep the styling") {
		t.Fatalf("summary job %+v", run)
	}
	run.OnFinish(doneJob("## Goal\nLogin page. Next: tests.", nil))
	v, _ := s.HandoffView(ho.ID)
	if v.State != "waitingReady" || v.TargetSessionID == nil || v.Brief != "## Goal\nLogin page. Next: tests." {
		t.Fatalf("after summary %+v", v)
	}
	tgt := *v.TargetSessionID
	if in, _ := h.SessionInfo(tgt); in.Agent != "codex" {
		t.Fatalf("target agent %s", in.Agent)
	}
	b, _ := h.Board(wid)
	for _, n := range b.Nodes {
		if n.ID == "n_grp0000001" && len(GroupOf(n).Members) != 2 {
			t.Fatal("target not in the source's group")
		}
	}
	s.Tick(t0.Add(time.Second)) // still starting
	h.setState(tgt, agentstate.Ready)
	s.Tick(t0.Add(2 * time.Second))
	ds := h.deliveriesTo(tgt)
	if len(ds) != 1 || ds[0].Source != "handoff" || !strings.HasSuffix(ds[0].Text, HandoffContinue) || !strings.HasPrefix(ds[0].Text, "## Goal") {
		t.Fatalf("delivery %+v", ds)
	}
	h.setDelivery(s, ds[0].ID, delivery.Confirmed)
	s.Tick(t0.Add(3 * time.Second))
	if v, _ = s.HandoffView(ho.ID); v.State != "done" {
		t.Fatalf("done %+v", v)
	}
	// Runner failure → raw brief; target waiting for the user too long → manual.
	src2 := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	h.screen[src2.ID] = "some screen output"
	ho2, _ := s.StartHandoff(src2.ID, HandoffRequest{Agent: "claude"})
	waitFor(t, "second summary job", func() bool { v, _ := s.HandoffView(ho2.ID); return v.State == "summarizing" })
	h.lastRun(t, "handoff-summary").OnFinish(failedJob("timeout"))
	v, _ = s.HandoffView(ho2.ID)
	if !strings.Contains(v.Brief, "some screen output") || !strings.Contains(v.Brief, "Summary unavailable") {
		t.Fatalf("raw brief %+v", v)
	}
	tgt2 := *v.TargetSessionID
	h.setState(tgt2, agentstate.NeedsYou) // e.g. the trust dialog
	s.Tick(t0.Add(10 * time.Second))
	s.Tick(t0.Add(129 * time.Second))
	if v, _ = s.HandoffView(ho2.ID); v.State != "waitingReady" {
		t.Fatalf("too early %+v", v.State)
	}
	s.Tick(t0.Add(131 * time.Second))
	if v, _ = s.HandoffView(ho2.ID); v.State != "manual" || len(h.deliveriesTo(tgt2)) != 0 {
		t.Fatalf("manual %+v", v)
	}
	found := false
	for _, m := range s.Threads.Last(p.ID, 30) {
		found = found || (strings.Contains(m.Text, "Copy this brief") && strings.Contains(m.Text, "some screen output"))
	}
	if !found {
		t.Fatal("brief not in thread")
	}
	sh := h.console(p, wid, "Sid", "shell", "")
	if _, err := s.StartHandoff(sh.ID, HandoffRequest{Agent: "codex"}); !errors.Is(err, ErrNotAgentSrc) {
		t.Fatalf("shell source %v", err)
	}
	if _, err := s.StartHandoff(src2.ID, HandoffRequest{Agent: "shell"}); !errors.Is(err, ErrHandoffAgent) {
		t.Fatalf("shell target %v", err)
	}
}
