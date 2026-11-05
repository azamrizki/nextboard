package orch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nextboard/internal/agentstate"
	"nextboard/internal/board"
	"nextboard/internal/jobs"
)

// leadHub is a hub with the lead talking first and two consoles.
func leadHub(t *testing.T) (*fakeHub, *Service, string, string, string, string) {
	t.Helper()
	h, s := newFakeHub(t)
	h.set.Lead.TalkFirst = true
	p, wid := h.project("app")
	ash := h.console(p, wid, "Ash", "claude", agentstate.Ready)
	bea := h.console(p, wid, "Bea", "claude", agentstate.Ready)
	return h, s, p.ID, wid, ash.ID, bea.ID
}

func sendStep(to, text string) map[string]any {
	return map[string]any{"op": "send", "targets": []any{to}, "text": text}
}

// afterRestart is what a hub restart leaves: a new service over the same files.
func afterRestart(h *fakeHub, s *Service) *Service {
	next := New(h, s.st)
	next.Now = s.Now
	return next
}

// TestPlanOutlivesHubRestart: the plan is still on the table after a restart, for the
// button and for a yes in words, and a yes needs no planner turn.
func TestPlanOutlivesHubRestart(t *testing.T) {
	for _, how := range []string{"words", "button"} {
		t.Run(how, func(t *testing.T) {
			h, s, pid, wid, ash, _ := leadHub(t)
			s.Command(CommandRequest{Text: "bersihkan log", WorkspaceID: wid})
			s.plannerDone(pid, doneJob("", map[string]any{"reply": "Aku kasih ke Ash. Lanjut?", "steps": []any{sendStep("Ash", "Delete the old logs")}}))
			before, ok := s.Confirms.Pending(pid, t0)
			if !ok {
				t.Fatal("no plan")
			}

			s = afterRestart(h, s)
			cf, ok := s.Confirms.Pending(pid, t0)
			if !ok || cf.ID != before.ID || cf.Kind != ConfProposal || len(cf.Plan) != 1 || cf.Plan[0].Text != "Delete the old logs" {
				t.Fatalf("after a restart: %+v", cf)
			}
			listed := s.Confirms.ForThread(pid)
			if len(listed) != 1 || listed[0].ID != cf.ID {
				t.Fatalf("listed %+v", listed)
			}
			if n := len(h.deliveriesTo(ash)); n != 0 {
				t.Fatalf("%d deliveries before the owner agreed", n)
			}

			runs := len(h.runs)
			if how == "words" {
				s.Command(CommandRequest{Text: "ya lanjut", WorkspaceID: wid})
			} else if _, err := s.Decide(cf.ID, "confirm", "", "admin"); err != nil {
				t.Fatal(err)
			}
			if d := h.deliveriesTo(ash); len(d) != 1 || !strings.Contains(d[0].Text, "Delete the old logs") {
				t.Fatalf("deliveries %+v", d)
			}
			if len(h.runs) != runs {
				t.Fatal("agreeing to a plan took a planner turn")
			}

			// the decision survives as well: it cannot be carried out twice
			s = afterRestart(h, s)
			if _, err := s.Decide(cf.ID, "confirm", "", "admin"); err != ErrAlreadyDecided {
				t.Fatalf("second decision: %v", err)
			}
			if n := len(h.deliveriesTo(ash)); n != 1 {
				t.Fatalf("%d deliveries", n)
			}
		})
	}
}

// TestExpiredPlanStaysExpiredAfterRestart: the time a plan has does not start over.
func TestExpiredPlanStaysExpiredAfterRestart(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	s.Command(CommandRequest{Text: "bersihkan log", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Lanjut?", "steps": []any{sendStep("Ash", "Delete the old logs")}}))
	cf, _ := s.Confirms.Pending(pid, t0)

	s = afterRestart(h, s)
	later := t0.Add(proposalTTL + time.Minute)
	s.Tick(later)
	if _, ok := s.Confirms.Pending(pid, later); ok {
		t.Fatal("still pending")
	}
	s.Now = func() time.Time { return later }
	if _, err := s.Decide(cf.ID, "confirm", "", "admin"); err != ErrConfExpired {
		t.Fatalf("decide: %v", err)
	}
	if n := len(h.deliveriesTo(ash)); n != 0 {
		t.Fatalf("%d deliveries", n)
	}
}

// TestPlannerTurnEndedByRestart: the thread says so and nothing is sent on the way down.
func TestPlannerTurnEndedByRestart(t *testing.T) {
	h, s, pid, wid, _, _ := leadHub(t)
	r, err := s.Command(CommandRequest{Text: "kerjakan login", WorkspaceID: wid})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Planning(pid); got == "" || got != r.CommandID {
		t.Fatalf("planning %q, command %q", got, r.CommandID)
	}
	h.lastRun(t, "planner").OnFinish(jobs.Job{State: jobs.StateFailed, Code: "hub_restart"})
	notes := threadTexts(s, pid, "note")
	if len(notes) != 1 || !strings.Contains(notes[0], "hub restarted") {
		t.Fatalf("notes %q", notes)
	}
	if len(h.delReqs) != 0 {
		t.Fatalf("sent while stopping: %+v", h.delReqs)
	}
	if got := afterRestart(h, s).Planning(pid); got != "" {
		t.Fatalf("planning after a restart: %q", got)
	}
}

// TestPlannerTimeoutSaysWhatToDo: the planner is on, so the setting is not the way out.
func TestPlannerTimeoutSaysWhatToDo(t *testing.T) {
	_, s, pid, wid, _, _ := leadHub(t)
	s.Command(CommandRequest{Text: "kerjakan login", WorkspaceID: wid})
	s.plannerDone(pid, jobs.Job{State: jobs.StateFailed, Code: "timeout"})
	notes := strings.Join(threadTexts(s, pid, "note"), " | ")
	if !strings.Contains(notes, "Send it again") || !strings.Contains(notes, "Name an agent with @ to reach them directly") || strings.Contains(notes, "enable the planner") {
		t.Fatalf("notes %q", notes)
	}
	if got := s.Planning(pid); got != "" {
		t.Fatalf("planning %q", got)
	}
}

// TestLeadHandsOutWorkAtOnce: the lead talks, and what it hands out needs no go ahead.
func TestLeadHandsOutWorkAtOnce(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)

	// a greeting is a conversation, with the lead's own name in the prompt
	if _, err := s.Command(CommandRequest{Text: "halo", WorkspaceID: wid}); err != nil {
		t.Fatal(err)
	}
	prompt := h.lastRun(t, "planner").Prompt
	if !strings.Contains(prompt, "You are Nara, the lead agent") || !strings.Contains(prompt, "Talk first:") || !strings.Contains(prompt, `set "danger"`) || !strings.Contains(prompt, "do not ask whether to go ahead") {
		t.Fatalf("prompt: %.300s", prompt)
	}
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Halo! Lagi mikirin apa?", "steps": []any{}}))
	if got := threadTexts(s, pid, "reply"); len(got) != 1 || got[0] != "Halo! Lagi mikirin apa?" {
		t.Fatalf("replies %q", got)
	}
	if _, ok := s.Confirms.Pending(pid, t0); ok {
		t.Fatal("a chat is not a proposal")
	}

	// work goes straight out, and the reply stays in the thread
	s.Command(CommandRequest{Text: "perbaiki bug login", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Aku kasih ke Ash.", "steps": []any{sendStep("Ash", "Fix the login bug")}}))
	if d := h.deliveriesTo(ash); len(d) != 1 || d[0].Text != "Fix the login bug" {
		t.Fatalf("deliveries %+v", d)
	}
	if cf, ok := s.Confirms.Pending(pid, t0); ok {
		t.Fatalf("plain work waits: %+v", cf)
	}
	if got := threadTexts(s, pid, "reply"); got[len(got)-1] != "Aku kasih ke Ash." {
		t.Fatalf("replies %q", got)
	}
}

// TestDangerousWorkWaits: what can destroy something waits, whoever noticed it: the hub's
// patterns in the order's text, or the lead's own judgement of an innocent looking order.
func TestDangerousWorkWaits(t *testing.T) {
	for _, tc := range []struct{ name, text, danger, says string }{
		{"rm", "run rm -rf build", "", `"rm -rf build"`},
		{"rm alone", "rm old.log", "", `"rm old.log"`},
		{"delete", "Delete the old logs", "", `"Delete"`},
		{"hapus", "hapus folder build", "", `"hapus"`},
		{"dihapus", "file lama dihapus saja", "", `"dihapus"`},
		{"drop", "DROP TABLE users", "", `"DROP TABLE"`},
		{"force push", "git push origin main --force", "", "--force"},
		{"reset", "git reset --hard HEAD~3", "", "git reset --hard"},
		{"migrate", "php artisan migrate:fresh", "", "migrate:fresh"},
		{"judgement", "Clean up the staging environment", "Data staging bisa hilang.", "Data staging bisa hilang."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s, pid, wid, ash, _ := leadHub(t)
			s.Command(CommandRequest{Text: "tolong beresin", WorkspaceID: wid})
			s.plannerDone(pid, doneJob("", map[string]any{"reply": "Nunggu OK-mu.", "danger": tc.danger, "steps": []any{sendStep("Ash", tc.text)}}))
			if n := len(h.deliveriesTo(ash)); n != 0 {
				t.Fatalf("%d deliveries before the owner agreed", n)
			}
			cf, ok := s.Confirms.Pending(pid, t0)
			if !ok || cf.Kind != ConfProposal || len(cf.Plan) != 1 || cf.Plan[0].Handle != "Ash" || cf.Plan[0].Text != tc.text {
				t.Fatalf("proposal %+v", cf)
			}
			if !strings.Contains(cf.Prompt, tc.says) {
				t.Fatalf("prompt %q does not say %q", cf.Prompt, tc.says)
			}
			if !cf.ExpiresAt.Equal(t0.Add(proposalTTL)) {
				t.Fatalf("expires %v", cf.ExpiresAt)
			}
			// one yes is enough: the owner has just seen what the danger is
			s.Command(CommandRequest{Text: "ya", WorkspaceID: wid})
			if d := h.deliveriesTo(ash); len(d) != 1 || d[0].Text != tc.text {
				t.Fatalf("deliveries %+v", d)
			}
			if _, ok := s.Confirms.Pending(pid, t0); ok {
				t.Fatal("a second question")
			}
		})
	}
}

// TestOrdinaryOrdersDoNotWait: the words that are not dangerous must not start asking.
func TestOrdinaryOrdersDoNotWait(t *testing.T) {
	for _, text := range []string{"Fix the login bug", "run the tests", "git push origin main", "form validation for the orm layer", "tambahkan tombol simpan", "review the delivery queue", "firm up the docs"} {
		h, s, pid, wid, ash, _ := leadHub(t)
		s.Command(CommandRequest{Text: "tolong", WorkspaceID: wid})
		s.plannerDone(pid, doneJob("", map[string]any{"reply": "Oke.", "steps": []any{sendStep("Ash", text)}}))
		if cf, ok := s.Confirms.Pending(pid, t0); ok {
			t.Errorf("%q waits: %s", text, cf.Prompt)
		}
		if len(h.deliveriesTo(ash)) != 1 {
			t.Errorf("%q was not sent", text)
		}
	}
}

// TestKillKeepsItsOwnQuestion: the board's destructive steps ask as they always did.
func TestKillKeepsItsOwnQuestion(t *testing.T) {
	_, s, pid, wid, _, _ := leadHub(t)
	s.Command(CommandRequest{Text: "matikan Ash", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Oke.", "steps": []any{map[string]any{"op": "kill", "targets": []any{"Ash"}}}}))
	if cf, ok := s.Confirms.Pending(pid, t0); !ok || cf.Kind != ConfDestructive {
		t.Fatalf("confirmation %+v", cf)
	}
}

// TestTalkingAboutADangerousPlan: it stays on the table while it is discussed, a change
// replaces it, and work that needs no OK does not leave the old one able to run.
func TestTalkingAboutADangerousPlan(t *testing.T) {
	h, s, pid, wid, ash, bea := leadHub(t)
	s.Command(CommandRequest{Text: "bersihkan", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Lanjut?", "steps": []any{sendStep("Ash", "Delete the build folder")}}))
	cf, ok := s.Confirms.Pending(pid, t0)
	if !ok {
		t.Fatal("no plan")
	}

	// talking about it leaves it on the table, and the lead can see it
	s.Command(CommandRequest{Text: "kenapa Ash, bukan Bea?", WorkspaceID: wid})
	if !strings.Contains(h.lastRun(t, "planner").Prompt, `"pendingPlan":[{"handle":"Ash"`) {
		t.Fatal("the lead does not see the plan under discussion")
	}
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Ash yang pegang auth.", "steps": []any{}}))
	if cur, ok := s.Confirms.Pending(pid, t0); !ok || cur.ID != cf.ID {
		t.Fatalf("a question dropped the plan: %+v", cur)
	}

	// changing it replaces it
	s.Command(CommandRequest{Text: "kasih Bea aja", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Oke, Bea.", "steps": []any{sendStep("Bea", "Delete the build folder")}}))
	next, ok := s.Confirms.Pending(pid, t0)
	if !ok || next.ID == cf.ID || next.Plan[0].Handle != "Bea" {
		t.Fatalf("revised plan %+v", next)
	}
	if old, _ := s.Confirms.Get(cf.ID); old.State != ConfCancelled {
		t.Fatalf("old plan %s", old.State)
	}
	if len(h.deliveriesTo(ash))+len(h.deliveriesTo(bea)) != 0 {
		t.Fatal("something went out before the owner agreed")
	}

	// agreeing in plain words sends it, once, without another planner turn
	before := len(h.runs)
	if _, err := s.Command(CommandRequest{Text: "oke gas", WorkspaceID: wid}); err != nil {
		t.Fatal(err)
	}
	if len(h.runs) != before {
		t.Fatal("agreeing asked the planner again")
	}
	if d := h.deliveriesTo(bea); len(d) != 1 || d[0].Text != "Delete the build folder" {
		t.Fatalf("deliveries %+v", d)
	}
	if len(h.deliveriesTo(ash)) != 0 {
		t.Fatal("the replaced plan still ran")
	}

	// a safe new plan goes out and takes the dangerous one off the table
	s.Command(CommandRequest{Text: "bersihkan lagi", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Lanjut?", "steps": []any{sendStep("Ash", "Delete the cache")}}))
	held, ok := s.Confirms.Pending(pid, t0)
	if !ok {
		t.Fatal("no plan")
	}
	s.Command(CommandRequest{Text: "ga usah, jalankan test saja", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Oke.", "steps": []any{sendStep("Ash", "Run the tests")}}))
	if d := h.deliveriesTo(ash); len(d) != 1 || d[0].Text != "Run the tests" {
		t.Fatalf("deliveries %+v", d)
	}
	if old, _ := s.Confirms.Get(held.ID); old.State != ConfCancelled {
		t.Fatalf("the dangerous plan is still %s", old.State)
	}
}

func TestLeadPlanAnswers(t *testing.T) {
	for word, sent := range map[string]bool{"ya": true, "Gas!": true, "lanjut": true, "oke, jalankan": true, "go ahead": true,
		"jangan dulu": false, "batal": false, "nanti aja": false, "no": false} {
		h, s, pid, wid, ash, _ := leadHub(t)
		s.Command(CommandRequest{Text: "bersihkan log", WorkspaceID: wid})
		s.plannerDone(pid, doneJob("", map[string]any{"reply": "Lanjut?", "steps": []any{sendStep("Ash", "Delete the old logs")}}))
		before := len(h.runs)
		s.Command(CommandRequest{Text: word, WorkspaceID: wid})
		if len(h.runs) != before {
			t.Errorf("%q went to the planner", word)
		}
		if got := len(h.deliveriesTo(ash)) == 1; got != sent {
			t.Errorf("%q: sent %v", word, got)
		}
		if _, pending := s.Confirms.Pending(pid, t0); pending {
			t.Errorf("%q left the plan pending", word)
		}
	}
}

// TestDirectOrdersSkipTheTalk: naming an agent, or a plain command, stays immediate.
func TestDirectOrdersSkipTheTalk(t *testing.T) {
	h, s, pid, wid, ash, bea := leadHub(t)
	before := len(h.runs)
	s.Command(CommandRequest{Text: "@Ash run the tests", WorkspaceID: wid})
	if d := h.deliveriesTo(ash); len(d) != 1 || d[0].Text != "run the tests" || len(h.runs) != before {
		t.Fatalf("direct order: %+v, %d planner runs", d, len(h.runs)-before)
	}
	// two names go through the planner to split the work, and still run at once
	s.Command(CommandRequest{Text: "@Ash @Bea split the refactor between you", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "", "steps": []any{sendStep("Bea", "Refactor the API")}}))
	if len(h.deliveriesTo(bea)) != 1 {
		t.Fatal("named agents should not need agreement")
	}
	if _, pending := s.Confirms.Pending(pid, t0); pending {
		t.Fatal("named agents became a proposal")
	}
	// the owner's own words to an agent are not the lead's plan
	s.Command(CommandRequest{Text: "@Ash delete the old logs", WorkspaceID: wid})
	if d := h.deliveriesTo(ash); len(d) != 2 || d[1].Text != "delete the old logs" {
		t.Fatalf("direct order: %+v", d)
	}
}

func TestTidyingTheBoardNeedsNoAgreement(t *testing.T) {
	h, s, pid, wid, _, _ := leadHub(t)
	s.Command(CommandRequest{Text: "tambahkan catatan di board", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Sudah.", "steps": []any{map[string]any{"op": "node.add", "kind": "note", "text": "Ingat"}}}))
	if _, pending := s.Confirms.Pending(pid, t0); pending {
		t.Fatal("a note became a proposal")
	}
	b, _ := h.Board(wid)
	found := false
	for _, n := range b.Nodes {
		found = found || n.Kind == "note"
	}
	if !found {
		t.Fatal("the note was not added")
	}
}

func TestPlanExpires(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	s.Command(CommandRequest{Text: "bersihkan log", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Lanjut?", "steps": []any{sendStep("Ash", "Delete the old logs")}}))
	s.Tick(t0.Add(proposalTTL - time.Minute))
	if _, ok := s.Confirms.Pending(pid, t0.Add(proposalTTL-time.Minute)); !ok {
		t.Fatal("gone too early")
	}
	s.Tick(t0.Add(proposalTTL + time.Minute))
	if _, ok := s.Confirms.Pending(pid, t0.Add(proposalTTL+time.Minute)); ok {
		t.Fatal("still pending after its time")
	}
	if len(h.deliveriesTo(ash)) != 0 {
		t.Fatal("an expired plan ran")
	}
}

func TestAddressingTheLead(t *testing.T) {
	h, s, pid, wid, _, _ := leadHub(t)
	before := len(h.runs)
	r, err := s.Command(CommandRequest{Text: "@Nara menurutmu gimana?", WorkspaceID: wid})
	if err != nil || r.HandledBy != "pending" || len(h.runs) != before+1 {
		t.Fatalf("%v %s, %d runs", err, r.HandledBy, len(h.runs)-before)
	}
	if !strings.Contains(h.lastRun(t, "planner").Prompt, `"order":"menurutmu gimana?"`) {
		t.Fatal("the lead's name stays in the order")
	}
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Bagus.", "steps": []any{}}))
	if strings.Contains(strings.Join(threadTexts(s, pid, "note"), " "), "I don't know") {
		t.Fatal("the lead is not a stranger")
	}
	// a console with that name wins: it was there to be addressed
	p, _ := s.hub.Project(pid)
	nara := h.console(p, wid, "Nara", "claude", agentstate.Ready)
	s.Command(CommandRequest{Text: "@Nara run the tests", WorkspaceID: wid})
	if len(h.deliveriesTo(nara.ID)) != 1 {
		t.Fatal("a console named like the lead did not get its order")
	}
}

// TestACardIsItsOwnPerson: its title is its name, its personality its own.
func TestACardIsItsOwnPerson(t *testing.T) {
	h, s, _, wid, ash, _ := leadHub(t)
	h.set.Lead.Personality = "Santai"
	card := "n_card000001"
	raw, _ := json.Marshal(map[string]any{"title": "Rimba", "personality": "Tegas dan singkat"})
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: &board.Node{ID: card, Kind: KindOrchestrator, W: 400, H: 500, Data: raw}},
		{Op: "putEdge", Edge: &board.Edge{ID: "e_wire000001", Kind: EdgeWire, Source: card, Target: h.node(wid, ash).ID}}}); err != nil {
		t.Fatal(err)
	}
	s.Command(CommandRequest{Text: "apa kabar tim?", WorkspaceID: wid, CardID: card})
	prompt := h.lastRun(t, "planner").Prompt
	if !strings.Contains(prompt, "You are Rimba,") || !strings.Contains(prompt, "Tegas dan singkat") || strings.Contains(prompt, "Santai") {
		t.Fatalf("prompt: %.400s", prompt)
	}

	// a card still called Orchestrator is the hub's lead
	plain := "n_card000002"
	raw, _ = json.Marshal(map[string]any{"title": "Orchestrator 2"})
	if err := h.ApplyOps(wid, []board.Op{{Op: "put", Node: &board.Node{ID: plain, Kind: KindOrchestrator, W: 400, H: 500, Data: raw}}}); err != nil {
		t.Fatal(err)
	}
	s.Command(CommandRequest{Text: "halo", WorkspaceID: wid, CardID: plain})
	prompt = h.lastRun(t, "planner").Prompt
	if !strings.Contains(prompt, "You are Nara,") || !strings.Contains(prompt, "Santai") {
		t.Fatalf("prompt: %.400s", prompt)
	}
}

func TestWithoutThePlannerTheParserStillAnswers(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	h.planner = false
	before := len(h.runs)
	s.Command(CommandRequest{Text: "thanks", WorkspaceID: wid})
	if len(h.runs) != before || len(threadTexts(s, pid, "reply")) != 1 {
		t.Fatalf("smalltalk: %d runs, replies %q", len(h.runs)-before, threadTexts(s, pid, "reply"))
	}
	s.Command(CommandRequest{Text: "@Ash run the tests", WorkspaceID: wid})
	if len(h.deliveriesTo(ash)) != 1 {
		t.Fatal("direct orders must work without the planner")
	}
}

func TestLeadSettingsOff(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	h.set.Lead.TalkFirst = false
	s.Command(CommandRequest{Text: "jalankan test", WorkspaceID: wid})
	if !strings.Contains(h.lastRun(t, "planner").Prompt, "Steps run as soon as you return them") {
		t.Fatal("prompt still promises to wait")
	}
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Jalan.", "steps": []any{sendStep("Ash", "Run the tests")}}))
	if len(h.deliveriesTo(ash)) != 1 {
		t.Fatal("with talk first off the plan runs at once")
	}
	// it still asks about what the hub finds dangerous
	s.Command(CommandRequest{Text: "bersihkan", WorkspaceID: wid})
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Jalan.", "steps": []any{sendStep("Ash", "rm -rf build")}}))
	if cf, ok := s.Confirms.Pending(pid, t0); !ok || cf.Kind != ConfRisky || len(h.deliveriesTo(ash)) != 1 {
		t.Fatalf("confirmation %+v", cf)
	}
}

// TestRiskySaysWhatItFound: a real plan quoted the critical notes back at the agents, and the
// owner was asked about "a risky step" with nothing to go on. The card now names it, and the
// planner is told the agents have those rules already.
func TestRiskySaysWhatItFound(t *testing.T) {
	h, s, pid, wid, _, _ := leadHub(t)
	s.Command(CommandRequest{Text: "tolong beresin bug login", WorkspaceID: wid})
	if p := h.lastRun(t, "planner").Prompt; !strings.Contains(p, "do not repeat them in the orders") {
		t.Fatal("the planner is not told to leave the rules out of its orders")
	}
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Oke.", "steps": []any{
		map[string]any{"op": "send", "targets": []any{"Ash"}, "text": "Cek bug login. Jangan cetak isi .env."}}}))
	cf, ok := s.Confirms.Pending(pid, s.Now())
	if !ok || cf.Kind != ConfProposal || !strings.Contains(cf.Prompt, "@Ash") || !strings.Contains(cf.Prompt, `".env"`) {
		t.Fatalf("confirmation %+v", cf)
	}
}

// TestReplyToAMessage: the thread keeps the owner's words and shows the quote; what goes on
// carries it, so the agent or the lead knows what is being answered.
func TestReplyToAMessage(t *testing.T) {
	h, s, pid, wid, ash, _ := leadHub(t)
	report := s.Threads.Add(ThreadMessage{ThreadID: pid, Role: "agent", Kind: "report", Handle: "Ash", Text: "All done.\nTests pass.", Report: &Report{State: "finished", Excerpt: "All done. Tests pass."}})

	s.Command(CommandRequest{Text: "@Ash now push it", WorkspaceID: wid, ReplyTo: report.ID})
	d := h.deliveriesTo(ash)
	if len(d) != 1 || !strings.HasPrefix(d[0].Text, "now push it") || !strings.Contains(d[0].Text, `what @Ash said: "All done. Tests pass."`) {
		t.Fatalf("delivery %+v", d)
	}
	var order ThreadMessage
	for _, m := range s.Threads.Last(pid, 10) {
		if m.Kind == "order" {
			order = m
		}
	}
	if order.Text != "@Ash now push it" || order.ReplyTo == nil || order.ReplyTo.ID != report.ID || order.ReplyTo.Handle != "Ash" || order.ReplyTo.Excerpt != "All done. Tests pass." {
		t.Fatalf("order %+v reply %+v", order, order.ReplyTo)
	}

	// the plan row shows what was asked; the quote is on the message above it
	for _, m := range s.Threads.Last(pid, 10) {
		if m.Kind == "plan" && (len(m.Plan) != 1 || m.Plan[0].Text != "now push it") {
			t.Fatalf("plan row %+v", m.Plan)
		}
	}

	// asking the lead about it: the lead sees the quote, the thread keeps the words
	s.Command(CommandRequest{Text: "ini aman?", WorkspaceID: wid, ReplyTo: report.ID})
	if !strings.Contains(h.lastRun(t, "planner").Prompt, `This answers what @Ash said`) {
		t.Fatal("the lead does not see what is being answered")
	}
	s.plannerDone(pid, doneJob("", map[string]any{"reply": "Aman.", "steps": []any{}}))

	// a message that is gone is just no quote
	s.Command(CommandRequest{Text: "@Ash lagi", WorkspaceID: wid, ReplyTo: "tm_gone00000"})
	if d := h.deliveriesTo(ash); d[len(d)-1].Text != "lagi" {
		t.Fatalf("delivery %+v", d[len(d)-1])
	}
}
