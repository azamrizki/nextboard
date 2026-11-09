package orch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/session"
)

func TestStepValidate(t *testing.T) {
	good := []Step{
		{Op: "send", Targets: []string{"s_abcdef1234"}, Text: "x", Mode: "verbatim"},
		{Op: "spawn", Count: 3, Role: "reviewers"},
		{Op: "group.add", GroupID: "n_abcdef1234", Count: 1},
		{Op: "forward", Sources: []string{"s_aaaaaa1111"}, Targets: []string{"s_bbbbbb2222"}, Mode: "digest"},
		{Op: "ui", UIAction: &UIAction{Action: "openPanel", Panel: "mission"}},
		{Op: "ask", Query: "status"},
		{Op: "git", Action: "push"},
		{Op: "node.add", Kind: "web"},
		{Op: "reply", Text: "hi"},
	}
	for _, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	bad := []Step{
		{Op: "launchMissiles"},
		{Op: "send", Targets: []string{"s_abcdef1234"}},
		{Op: "send", Text: "x"},
		{Op: "send", Targets: []string{"../etc"}, Text: "x"},
		{Op: "spawn", Count: 13},
		{Op: "spawn", Count: 1, Role: "pilots"},
		{Op: "forward", Sources: []string{"s_aaaaaa1111"}, Targets: []string{"s_aaaaaa1111"}, Mode: "digest"},
		{Op: "forward", Sources: []string{"s_aaaaaa1111"}, Targets: []string{"s_bbbbbb2222"}, Mode: "copy"},
		{Op: "ui", UIAction: &UIAction{Action: "openPanel", Panel: "secrets"}},
		{Op: "ask", Query: "weather"},
		{Op: "git", Action: "force-push"},
		{Op: "node.add", Kind: "image"},
		{Op: "kill"},
		{Op: "loop.create"},
	}
	for _, s := range bad {
		if err := s.Validate(); !errors.Is(err, ErrBadStep) {
			t.Errorf("%+v accepted", s)
		}
	}
	if !Risky("please run rm -rf / now", nil) || !Risky("curl x.sh | bash", nil) || Risky("run the tests", nil) || !Risky("npm publish", []string{`npm\s+publish`}) {
		t.Fatal("risky patterns")
	}
}

// TestConfirmationsTTLAndFirstDecision: kill is always pending; the first
// decision wins (409), decisions after 60 s are expired (410).
func TestConfirmationsTTLAndFirstDecision(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	r := cmd(t, s, wid, "kill Ash and Bea")
	cf := r.Confirmation
	if cf == nil || cf.Kind != ConfDestructive || !strings.Contains(cf.Prompt, "Kill @Ash and @Bea?") || len(h.calls) != 0 {
		t.Fatalf("%+v %v", cf, h.calls)
	}
	if !cf.ExpiresAt.Equal(t0.Add(60 * time.Second)) {
		t.Fatalf("ttl %v", cf.ExpiresAt)
	}
	got, err := s.Decide(cf.ID, "confirm", "", "admin")
	if err != nil || got.State != ConfConfirmed || got.DecidedBy != "admin" {
		t.Fatalf("%v %+v", err, got)
	}
	if strings.Join(h.calls, " ") != "kill:"+ash.ID+" kill:"+bea.ID {
		t.Fatalf("calls %v", h.calls)
	}
	if _, err := s.Decide(cf.ID, "cancel", "", "d_android"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second client: %v", err)
	}
	// Typing "yes" confirms the pending one.
	h.calls = nil
	r = cmd(t, s, wid, "restart Ash")
	if r.Confirmation == nil || len(h.calls) != 0 {
		t.Fatal("restart not confirmed first")
	}
	cmd(t, s, wid, "yes")
	if len(h.calls) != 1 || h.calls[0] != "restart:"+ash.ID {
		t.Fatalf("yes: %v", h.calls)
	}
	// Other text cancels the pending confirmation and runs as a command.
	h.calls = nil
	r = cmd(t, s, wid, "kill Ash")
	r2 := cmd(t, s, wid, "pause Bea")
	if c, _ := s.Confirms.Get(r.Confirmation.ID); c.State != ConfCancelled {
		t.Fatalf("not cancelled: %+v", c)
	}
	if strings.Join(h.calls, " ") != "pause:"+bea.ID || !strings.Contains(texts(r2), "Cancelled the pending confirmation") {
		t.Fatalf("%v %s", h.calls, texts(r2))
	}
	// Expiry after the TTL.
	r = cmd(t, s, wid, "kill Bea")
	s.Tick(t0.Add(61 * time.Second))
	if _, err := s.Decide(r.Confirmation.ID, "confirm", "", "admin"); !errors.Is(err, ErrConfExpired) {
		t.Fatalf("expired: %v", err)
	}
	if strings.Contains(strings.Join(h.calls, " "), "kill") {
		t.Fatal("expired confirmation executed")
	}
	// "no" cancels.
	r = cmd(t, s, wid, "kill Bea")
	cmd(t, s, wid, "no")
	if c, _ := s.Confirms.Get(r.Confirmation.ID); c.State != ConfCancelled {
		t.Fatal("no did not cancel")
	}
	// Bulk spawn and loops always ask.
	if r := cmd(t, s, wid, "spawn 8 agents"); r.Confirmation == nil || r.Confirmation.Kind != ConfBulkSpawn {
		t.Fatalf("bulk %+v", r.Confirmation)
	}
	if r := cmd(t, s, wid, "every 15 minutes run the tests"); r.Confirmation == nil || r.Confirmation.Kind != ConfLoop ||
		!strings.Contains(r.Confirmation.Prompt, "every 15 minutes") || !strings.Contains(r.Confirmation.Prompt, "× 2 console") {
		t.Fatalf("loop %+v", r.Confirmation)
	}
}

// TestMentionsFuzzyAndFallback: @Ahs → didYouMean; two candidates →
// sendToAll; mention without text → nudge; no-planner fallback rules.
func TestMentionsFuzzyAndFallback(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "codex", agentstate.Ready)
	r := cmd(t, s, wid, "@Ahs run the tests")
	if r.Confirmation == nil || r.Confirmation.Kind != ConfDidYouMean || r.Confirmation.Prompt != "Did you mean @Ash?" || len(h.delReqs) != 0 {
		t.Fatalf("%+v", r.Confirmation)
	}
	if _, err := s.Decide(r.Confirmation.ID, "confirm", "", "admin"); err != nil {
		t.Fatal(err)
	}
	if len(h.deliveriesTo(ash.ID)) != 1 || h.deliveriesTo(ash.ID)[0].Text != "run the tests" {
		t.Fatalf("didYouMean send %v", h.delReqs)
	}
	h.console(p, wid, "Bex", "claude", agentstate.Ready)
	r = cmd(t, s, wid, "@Bez check lint")
	if r.Confirmation == nil || r.Confirmation.Kind != ConfSendToAll || len(r.Confirmation.Candidates) != 2 {
		t.Fatalf("sendToAll %+v", r.Confirmation)
	}
	if _, err := s.Decide(r.Confirmation.ID, "confirm", bea.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if len(h.deliveriesTo(bea.ID)) != 1 {
		t.Fatal("choice not used")
	}
	if r := cmd(t, s, wid, "@Ash"); !strings.Contains(texts(r), "reply:What should @Ash do?") || r.OrderID != "" {
		t.Fatalf("nudge %s", texts(r))
	}
	// One mention + text = verbatim order.
	r = cmd(t, s, wid, "@Bea fix the flaky test")
	o, ok := s.Orders.Get(r.OrderID)
	if !ok || len(o.Targets) != 1 || o.Targets[0].SessionID != bea.ID || o.Text != "fix the flaky test" || r.HandledBy != "parser" {
		t.Fatalf("order %+v", o)
	}
	// No addressee, several agents → nothing sent.
	n := len(h.delReqs)
	if r := cmd(t, s, wid, "the login page is broken"); !strings.Contains(texts(r), "Name an agent with @ or enable the planner") || len(h.delReqs) != n {
		t.Fatalf("no addressee %s", texts(r))
	}
	// @everyone = no mention: a short directive goes to everyone.
	cmd(t, s, wid, "@everyone commit your work")
	if len(h.delReqs) != n+3 {
		t.Fatalf("everyone %d", len(h.delReqs)-n)
	}
	// Several consoles named, long task, no planner: one free agent.
	h.setState(bea.ID, agentstate.Working)
	r = cmd(t, s, wid, "tell Ash and Bea to refactor the payment module and then update every caller of the old API")
	if !strings.Contains(texts(r), "Sent to @Ash; 0 left free") {
		t.Fatalf("one free %s", texts(r))
	}
	// Requests that need composing are not sent without the planner.
	n = len(h.delReqs)
	if r := cmd(t, s, wid, "@everyone write a brief for the new hires about the architecture and our conventions"); len(h.delReqs) != n ||
		!strings.Contains(texts(r), "needs the planner") {
		t.Fatalf("compose %s", texts(r))
	}
}

// TestCardScope: a card only directs wired consoles.
func TestCardScope(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.console(p, wid, "Bo", "claude", agentstate.Ready)
	card := "n_card000001"
	raw, _ := json.Marshal(map[string]any{"title": "Planning"})
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: &board.Node{ID: card, Kind: KindOrchestrator, W: 400, H: 500, Data: raw}},
		{Op: "putEdge", Edge: &board.Edge{ID: "e_wire000001", Kind: EdgeWire, Source: card, Target: h.node(wid, ash.ID).ID}}}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Command(CommandRequest{Text: "@Bo run tests", WorkspaceID: wid, CardID: card, Planner: "off"})
	if err != nil || r.ThreadID != card || !strings.Contains(texts(r), "@Bo is not wired to this card") || len(h.delReqs) != 0 {
		t.Fatalf("%v %s", err, texts(r))
	}
	r, _ = s.Command(CommandRequest{Text: "undo", WorkspaceID: wid, CardID: card})
	if !strings.Contains(texts(r), "Use the main command bar") || len(r.UIActions) != 0 {
		t.Fatalf("undo %s", texts(r))
	}
	r, _ = s.Command(CommandRequest{Text: "new workspace", WorkspaceID: wid, CardID: card})
	if !strings.Contains(texts(r), "Use the main command bar") {
		t.Fatal("workspace in card")
	}
	// "everyone" in a card = the wired consoles; one agent → it gets it.
	r, _ = s.Command(CommandRequest{Text: "run the linter", WorkspaceID: wid, CardID: card, Planner: "off"})
	if o, ok := s.Orders.Get(r.OrderID); !ok || len(o.Targets) != 1 || o.Targets[0].SessionID != ash.ID || o.ThreadID != card {
		t.Fatalf("card order %+v", o)
	}
	if _, err := s.Command(CommandRequest{Text: "x", WorkspaceID: wid, CardID: "n_nothere0001"}); !errors.Is(err, ErrBadCard) {
		t.Fatal(err)
	}
	if _, err := s.Command(CommandRequest{Text: "  ", WorkspaceID: wid}); !errors.Is(err, ErrEmptyText) {
		t.Fatal(err)
	}
	if _, err := s.Command(CommandRequest{Text: strings.Repeat("x", 4001), WorkspaceID: wid}); !errors.Is(err, ErrCommandTooLong) {
		t.Fatal(err)
	}
	if _, err := s.Command(CommandRequest{Text: "x", WorkspaceID: "w_missing0001"}); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatal(err)
	}
}

// TestOrderTrackingAndReports: delivered → working → finished with a
// report from the reply; timeout after 30 min; cancel only undelivered.
func TestOrderTrackingAndReports(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	r := cmd(t, s, wid, "@Ash run the tests")
	d := h.deliveriesTo(ash.ID)[0]
	if d.OrderID != r.OrderID || d.Source != "order" {
		t.Fatalf("%+v", d)
	}
	h.setDelivery(s, d.ID, delivery.Confirmed)
	o, _ := s.Orders.Get(r.OrderID)
	if o.Targets[0].State != TWorking || o.Targets[0].SentAt == nil {
		t.Fatalf("%+v", o.Targets[0])
	}
	h.replies[ash.ID] = &session.Reply{Text: "All 42 tests pass. " + strings.Repeat("detail ", 100), MessageID: "m2", Chars: 700, At: t0.Add(time.Minute)}
	end := t0.Add(time.Minute)
	in, _ := h.SessionInfo(ash.ID)
	s.OnAgentEvent(in, agentstate.Event{Kind: agentstate.EvFinished, Turn: agentstate.LastTurn{StartedAt: t0, EndedAt: &end, DurationMs: 60000}})
	if o, _ = s.Orders.Get(r.OrderID); o.Targets[0].State != TFinished || o.Active() {
		t.Fatalf("not finished %+v", o.Targets[0])
	}
	var rep ThreadMessage
	for i := 0; i < 100 && rep.ID == ""; i++ {
		for _, m := range s.Threads.Last(p.ID, 20) {
			if m.Kind == "report" && m.OrderID == r.OrderID {
				rep = m
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if rep.Report == nil || rep.Report.State != TFinished || rep.Report.DurationMs != 60000 || len([]rune(rep.Report.Excerpt)) > maxExcerpt ||
		!strings.HasPrefix(rep.Text, "All 42 tests pass") || rep.Handle != "Ash" || rep.Role != "agent" {
		t.Fatalf("report %+v", rep)
	}
	// Timeout.
	r = cmd(t, s, wid, "@Bea refactor")
	h.setDelivery(s, h.deliveriesTo(bea.ID)[0].ID, delivery.Sent)
	s.Tick(t0.Add(31 * time.Minute))
	o, _ = s.Orders.Get(r.OrderID)
	if o.Targets[0].State != TTimeout {
		t.Fatalf("timeout %+v", o.Targets[0])
	}
	found := false
	for _, m := range s.Threads.Last(p.ID, 30) {
		found = found || strings.Contains(m.Text, "No reply from @Bea after 30 min")
	}
	if !found {
		t.Fatal("no timeout note")
	}
	// cancel that: only the delivery not typed yet.
	h.setState(ash.ID, agentstate.Working)
	r = cmd(t, s, wid, "@everyone stop") // ≤ 40 chars: same text to all
	ds := []delivery.Delivery{h.deliveriesTo(ash.ID)[1], h.deliveriesTo(bea.ID)[1]}
	h.setDelivery(s, ds[1].ID, delivery.Sent)
	r2 := cmd(t, s, wid, "cancel that")
	o, _ = s.Orders.Get(r.OrderID)
	states := map[string]string{}
	for _, t := range o.Targets {
		states[t.Handle] = t.State
	}
	if states["Ash"] != TCancelled || states["Bea"] != TDelivered || !strings.Contains(texts(r2), "for 1 console") {
		t.Fatalf("cancel %v %s", states, texts(r2))
	}
	// A failed delivery fails its target with a report.
	h.delErr[bea.ID] = delivery.ErrNotAgent
	r = cmd(t, s, wid, "@Bea hello")
	if o, _ = s.Orders.Get(r.OrderID); o.Targets[0].State != TFailed {
		t.Fatalf("%+v", o.Targets[0])
	}
}

// TestPlannerAvailabilityFallbackCooldown: §19.8.
func TestPlannerAvailabilityFallbackCooldown(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.console(p, wid, "Bea", "codex", agentstate.Ready)
	h.replies[ash.ID] = &session.Reply{Text: "SECRET-TRANSCRIPT-CONTENT", MessageID: "m1"}
	h.planner = false
	if st := s.Planner(); st.Available || st.Reason != "claude_missing" {
		t.Fatalf("%+v", st)
	}
	r, _ := s.Command(CommandRequest{Text: "tell Ash to fix the build", WorkspaceID: p.Workspaces[0].ID})
	if r.HandledBy != "parser" || len(h.deliveriesTo(ash.ID)) != 1 || !strings.Contains(texts(r), "Planner unavailable: claude_missing") {
		t.Fatalf("fallback %s", texts(r))
	}
	h.planner = true
	// Planner turn: invalid JSON → parser fallback with a note.
	r, _ = s.Command(CommandRequest{Text: "tell Ash to fix the build", WorkspaceID: wid})
	if r.HandledBy != "pending" {
		t.Fatalf("not planned: %s", r.HandledBy)
	}
	run := h.lastRun(t, "planner")
	if run.Agent != "claude" || string(run.Schema) != string(PlanSchema) || run.MachineID != "" {
		t.Fatalf("runner %+v", run)
	}
	if strings.Contains(run.Prompt, "SECRET-TRANSCRIPT-CONTENT") || !strings.Contains(run.Prompt, `"handle":"Ash"`) {
		t.Fatal("snapshot must name consoles but carry no transcript/reply content")
	}
	// A second command waits behind the planner turn; ≤ 3 queued.
	for i := 0; i < 3; i++ {
		if r, err := s.Command(CommandRequest{Text: "status", WorkspaceID: wid}); err != nil || r.HandledBy != "pending" {
			t.Fatalf("queue %d: %v %s", i, err, r.HandledBy)
		}
	}
	if _, err := s.Command(CommandRequest{Text: "status", WorkspaceID: wid}); !errors.Is(err, ErrThreadBusy) {
		t.Fatalf("thread busy: %v", err)
	}
	s.Tick(t0.Add(4 * time.Second)) // "Still thinking…"
	before := len(h.deliveriesTo(ash.ID))
	s.plannerDone(p.ID, doneJob("this is not json", nil))
	if len(h.deliveriesTo(ash.ID)) != before+1 {
		t.Fatal("no fallback send after invalid planner output")
	}
	msgs := s.Threads.Last(p.ID, 40)
	thinking, notes := false, 0
	for _, m := range msgs {
		thinking = thinking || (m.Text == "Planner unavailable: invalid JSON")
		if m.Kind == "reply" && strings.Contains(m.Text, "agent(s)") {
			notes++
		}
	}
	if !thinking || notes != 3 {
		t.Fatalf("thinking message not updated / queue not drained (%d status replies)", notes)
	}
	// Second failure in a row → cooldown 60 s.
	s.Command(CommandRequest{Text: "tell Bea to review", WorkspaceID: wid})
	s.plannerDone(p.ID, failedJob("timeout"))
	if st := s.Planner(); st.Available || st.Reason != "cooldown" {
		t.Fatalf("cooldown %+v", st)
	}
	s.Now = func() time.Time { return t0.Add(61 * time.Second) }
	if st := s.Planner(); !st.Available {
		t.Fatalf("after cooldown %+v", st)
	}
	// Not logged in → unavailable for a while.
	s.Command(CommandRequest{Text: "tell Bea to review", WorkspaceID: wid})
	s.plannerDone(p.ID, failedJob("cli_not_logged_in"))
	if st := s.Planner(); st.Reason != "not_logged_in" {
		t.Fatalf("login %+v", st)
	}
}

// TestPlannerTranslation: names → ids, unknown names and foreign ops
// dropped, planned risky steps confirmed.
func TestPlannerTranslation(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "codex", agentstate.Ready)
	s.Command(CommandRequest{Text: "split the auth refactor between Ash and Bea", WorkspaceID: wid})
	plan := map[string]any{"reply": "Split it in two.", "steps": []any{
		map[string]any{"op": "send", "targets": []string{"Ash"}, "text": "Refactor the login handler."},
		map[string]any{"op": "send", "targets": []string{"bea"}, "text": "Update the session store."},
		map[string]any{"op": "send", "targets": []string{"Zed"}, "text": "ghost"},
		map[string]any{"op": "workspace.close"},
	}}
	s.plannerDone(p.ID, doneJob("", plan))
	if len(h.deliveriesTo(ash.ID)) != 1 || h.deliveriesTo(ash.ID)[0].Text != "Refactor the login handler." || len(h.deliveriesTo(bea.ID)) != 1 {
		t.Fatalf("plan sends %v", h.delReqs)
	}
	all := ""
	for _, m := range s.Threads.Last(p.ID, 20) {
		all += m.Text + "\n"
	}
	if !strings.Contains(all, "unknown consoles (Zed)") || !strings.Contains(all, `"workspace.close" dropped`) || !strings.Contains(all, "Split it in two.") {
		t.Fatalf("notes:\n%s", all)
	}
	// A planned send with a risky pattern needs confirmation.
	s.Command(CommandRequest{Text: "clean up the build output", WorkspaceID: wid})
	s.plannerDone(p.ID, doneJob("", map[string]any{"reply": "", "steps": []any{map[string]any{"op": "send", "targets": []string{"Ash"}, "text": "rm -rf dist"}}}))
	cf, ok := s.Confirms.Pending(p.ID, t0)
	if !ok || cf.Kind != ConfRisky || len(h.deliveriesTo(ash.ID)) != 1 {
		t.Fatalf("risky %+v", cf)
	}
}

// An agent opened inside the editor has no tile, and takes orders like any other.
// The editor's own terminals stay the user's shells.
func TestOrdersReachAnAgentInTheEditor(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	panel := func(handle, agentID string) session.Info {
		h.mu.Lock()
		defer h.mu.Unlock()
		id := auth.RandomID("s_")
		kind := agent.KindAgent
		if agentID == "shell" {
			kind = agent.KindShell
		}
		in := &session.Info{Spec: session.Spec{ID: id, Handle: handle, Agent: agentID, AgentKind: kind, MachineID: "local", Panel: true,
			ProjectID: p.ID, WorkspaceID: wid, CreatedAt: t0}, Status: session.StatusRunning}
		in.AgentState, in.Foreground = agentstate.Ready, agentstate.FgAgent
		h.sessions[id] = in
		h.order = append(h.order, id)
		return *in
	}
	ash := panel("Ash", "claude")
	panel("Tom", "shell")
	r, err := s.Command(CommandRequest{Text: "@Ash run the tests", WorkspaceID: wid, Planner: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.dels) != 1 {
		t.Fatalf("%s %+v", texts(r), h.dels)
	}
	for _, d := range h.dels {
		if d.SessionID != ash.ID {
			t.Fatalf("%+v", d)
		}
	}
	r, err = s.Command(CommandRequest{Text: "@Tom run the tests", WorkspaceID: wid, Planner: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.dels) != 1 {
		t.Fatalf("a shell of the editor got an order: %s %+v", texts(r), h.dels)
	}
}

// restart stops s the way a hub does and starts a new service on the same data.
func restart(t *testing.T, h *fakeHub, s *Service) *Service {
	t.Helper()
	s.Orders.Save(t0)
	n := New(h, h.st)
	n.Now = func() time.Time { return t0 }
	n.Threads.Now = n.Now
	n.Start()
	t.Cleanup(n.Close)
	return n
}

func TestResendAfterRestart(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	cal := h.console(p, wid, "Cal", "claude", agentstate.Ready)
	r := cmd(t, s, wid, "@Ash @Bea @Cal run the tests")
	// Bea already has the text. Ash and Cal are still queued.
	h.setDelivery(s, h.deliveriesTo(bea.ID)[0].ID, delivery.Confirmed)

	s = restart(t, h, s)
	o, _ := s.Orders.Get(r.OrderID)
	state := map[string]OrderTarget{}
	for _, x := range o.Targets {
		state[x.Handle] = x
	}
	if !state["Ash"].Undelivered || !state["Cal"].Undelivered || state["Bea"].Undelivered || o.Active() {
		t.Fatalf("after restart %+v", o.Targets)
	}
	var note ThreadMessage
	for _, m := range s.Threads.Last(p.ID, 20) {
		if m.OrderID == r.OrderID && m.Resend {
			note = m
		}
	}
	if !strings.Contains(note.Text, "interrupted by hub restart") || !strings.Contains(note.Text, "@Ash and @Cal did not get it") {
		t.Fatalf("note %+v", note)
	}
	// Nothing goes out on its own.
	if n := len(h.deliveriesTo(ash.ID)); n != 1 {
		t.Fatalf("Ash got %d deliveries by itself", n)
	}

	// Ash is still resuming, so it stays offered. Cal takes it. Bea had it and gets nothing.
	h.delErr[ash.ID] = delivery.ErrTargetBusy
	o, err := s.ResendOrder(r.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range o.Targets {
		state[x.Handle] = x
	}
	if !state["Ash"].Undelivered || state["Ash"].State != TFailed {
		t.Fatalf("busy Ash %+v", state["Ash"])
	}
	if state["Cal"].Undelivered || state["Cal"].State != TQueued || state["Cal"].Error != "" || state["Cal"].FinishedAt != nil || !o.Active() {
		t.Fatalf("Cal %+v", state["Cal"])
	}
	if len(h.deliveriesTo(cal.ID)) != 2 || len(h.deliveriesTo(bea.ID)) != 1 {
		t.Fatalf("deliveries cal=%d bea=%d", len(h.deliveriesTo(cal.ID)), len(h.deliveriesTo(bea.ID)))
	}

	// The same order goes on: its delivery still updates the target.
	last := h.deliveriesTo(cal.ID)[1]
	if last.OrderID != r.OrderID || last.Text != "run the tests" || last.Source != "order" {
		t.Fatalf("delivery %+v", last)
	}
	h.setDelivery(s, last.ID, delivery.Confirmed)
	if o, _ = s.Orders.Get(r.OrderID); targetOf(o, "Cal").State != TWorking {
		t.Fatalf("Cal after delivery %+v", targetOf(o, "Cal"))
	}

	// Ash is ready now, and a second click finds nothing left.
	delete(h.delErr, ash.ID)
	if o, err = s.ResendOrder(r.OrderID); err != nil || targetOf(o, "Ash").Undelivered || len(h.deliveriesTo(ash.ID)) != 2 {
		t.Fatalf("Ash resend %v %+v", err, targetOf(o, "Ash"))
	}
	if _, err := s.ResendOrder(r.OrderID); !errors.Is(err, ErrNothingToResend) {
		t.Fatalf("second resend %v", err)
	}
	if _, err := s.ResendOrder("o_missing"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("missing order %v", err)
	}
}

func TestResendForAGoneConsoleEnds(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	r := cmd(t, s, wid, "@Ash run the tests")
	s = restart(t, h, s)
	h.mu.Lock()
	delete(h.sessions, ash.ID)
	h.mu.Unlock()
	o, err := s.ResendOrder(r.OrderID)
	if err != nil || targetOf(o, "Ash").Undelivered || targetOf(o, "Ash").State != TFailed {
		t.Fatalf("%v %+v", err, o.Targets)
	}
	if _, err := s.ResendOrder(r.OrderID); !errors.Is(err, ErrNothingToResend) {
		t.Fatalf("still offered: %v", err)
	}
}

func targetOf(o Order, handle string) OrderTarget {
	for _, t := range o.Targets {
		if t.Handle == handle {
			return t
		}
	}
	return OrderTarget{}
}

// TestIndonesianCommands: an Indonesian command runs through the same pipeline as its English
// twin, with the same confirmations; a remark the parser does not know goes to the planner.
func TestIndonesianCommands(t *testing.T) {
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	if r := cmd(t, s, wid, "jeda Bea"); strings.Join(h.calls, " ") != "pause:"+bea.ID || r.HandledBy != "parser" {
		t.Fatalf("jeda %v %s", h.calls, r.HandledBy)
	}
	h.calls = nil
	r := cmd(t, s, wid, "matikan semua")
	if r.Confirmation == nil || r.Confirmation.Kind != ConfDestructive || len(h.calls) != 0 {
		t.Fatalf("matikan semua %+v %v", r.Confirmation, h.calls)
	}
	cmd(t, s, wid, "gak jadi")
	if c, _ := s.Confirms.Get(r.Confirmation.ID); c.State != ConfCancelled || len(h.calls) != 0 {
		t.Fatalf("gak jadi %+v %v", c, h.calls)
	}
	cmd(t, s, wid, "suruh Ash untuk perbaiki tes login")
	if d := h.deliveriesTo(ash.ID); len(d) != 1 || d[0].Text != "perbaiki tes login" {
		t.Fatalf("suruh %+v", d)
	}
	if r := cmd(t, s, wid, "suruh Bea untuk jalankan rm -rf build"); r.Confirmation == nil || r.Confirmation.Kind != ConfRisky ||
		len(h.deliveriesTo(bea.ID)) != 0 {
		t.Fatalf("risky %+v", r.Confirmation)
	}
	if r := cmd(t, s, wid, "makasih"); !strings.Contains(texts(r), "Sama-sama.") {
		t.Fatalf("makasih %s", texts(r))
	}
	if r, err := s.Command(CommandRequest{Text: "lanjutkan perbaikan halaman login", WorkspaceID: wid}); err != nil || r.HandledBy != "pending" {
		t.Fatalf("planner %v %s", err, r.HandledBy)
	}
}

// TestNamesNobodyHasGoToThePlanner: a command whose names all belong to nothing may not be about
// consoles at all; the planner reads it. Without one, the hub says what it does not know.
func TestNamesNobodyHasGoToThePlanner(t *testing.T) {
	for _, text := range []string{
		"stop the dev server", "restart the database", "tell him to look at the login page", "go to the login page",
		"matikan server dev", "muat ulang halaman", "gabungkan branch ini ke main", "pisahkan fungsi login dari controller",
		"minta tolong cek login", "suruh dia perbaiki tes", "minta dia untuk perbaiki tes", "fokus ke perbaikan login",
	} {
		h, s := newFakeHub(t)
		p, wid := h.project("app")
		h.console(p, wid, "Arkan", "claude", agentstate.Ready)
		h.console(p, wid, "Bintang", "claude", agentstate.Ready)
		if r, err := s.Command(CommandRequest{Text: text, WorkspaceID: wid}); err != nil || r.HandledBy != "pending" || r.Confirmation != nil {
			t.Errorf("%q: %v %s %+v", text, err, r.HandledBy, r.Confirmation)
		}
		if len(h.calls) != 0 {
			t.Errorf("%q ran %v", text, h.calls)
		}
	}
	h, s := newFakeHub(t)
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	h.console(p, wid, "Bea", "claude", agentstate.Ready)
	for text, want := range map[string]string{
		"stop the dev server":          "I don't know",
		"tell Zedekiah to fix it":      "I don't know",
		"gabungkan Zedekiah dan Yosef": "I don't know",
		"bubarkan Zedekiah":            "There is no group",
		"fokus ke Zedekiah":            "I can't find",
	} {
		if r := cmd(t, s, wid, text); !strings.Contains(texts(r), want) {
			t.Errorf("no planner, %q: %s", text, texts(r))
		}
	}
	// one known name among strangers is about consoles: the hub says which one it does not know
	r, err := s.Command(CommandRequest{Text: "kill Ash and Zedekiah", WorkspaceID: wid})
	if err != nil || r.HandledBy == "pending" || !strings.Contains(texts(r), "I don't know") {
		t.Fatalf("%v %s %s", err, r.HandledBy, texts(r))
	}
	// a known name still goes straight through, with its confirmation
	if r, _ := s.Command(CommandRequest{Text: "matikan Ash", WorkspaceID: wid}); r.Confirmation == nil || !strings.Contains(r.Confirmation.Prompt, "@"+ash.Handle) {
		t.Fatalf("%+v", r.Confirmation)
	}
}
