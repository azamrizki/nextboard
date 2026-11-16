package orch

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/delivery"
)

// Team tools (docs/API-orchestration.md §22): the agents of a mission talk to each other and
// to the lead only through the hub (owner decision 1a). Every message is a thread message of
// kind "team", where the owner sees who asked whom; what goes to a teammate is an ordinary
// delivery of source "team", queued while it works, never an order: no report, no task card.

const (
	teamTextMax = 4000
	// teamEvery: a console gets at most one team delivery this often, and never a second
	// while the first waits to be typed. What comes in meanwhile goes as one.
	teamEvery = 10 * time.Second
	// teamBundleMax: characters of held messages typed as one delivery.
	teamBundleMax = 16000
	// teamDeadline: a team message waits for a busy teammate as long as a mission may run.
	teamDeadline = 4 * 60 * 60
	teamReadLast = 15
	// A mission's agents may send each other this many messages an hour, and two of them this
	// many between them: two agents must not keep answering each other for ever, past the
	// lead's limits.
	teamBudgetWindow  = time.Hour
	teamBudgetMission = 60
	teamBudgetPair    = 20
)

// teamPace holds team messages back per console.
type teamPace struct {
	mu      sync.Mutex
	flush   sync.Mutex              // one flush at a time, so a message is never typed twice
	held    map[string][]teamHeld   // by console: oldest first
	last    map[string]time.Time    // by console: the last team delivery made
	pending map[string]*teamPending // by console: a team delivery not typed yet
	spent   map[string][]teamSpent  // by mission: messages between agents in the last hour
	woke    map[string]time.Time    // by mission: when the lead was told the budget is spent
}

// teamSpent is one message an agent sent a teammate; pair names both consoles.
type teamSpent struct {
	at   time.Time
	pair string
}

type teamHeld struct{ missionID, text string }

// teamPending is a team delivery in the engine, with what it carries in case it has to
// wait for the mission again.
type teamPending struct {
	id, missionID string
	texts         []string
}

func (p *teamPace) initLocked() {
	if p.held == nil {
		p.held, p.last, p.pending = map[string][]teamHeld{}, map[string]time.Time{}, map[string]*teamPending{}
		p.spent, p.woke = map[string][]teamSpent{}, map[string]time.Time{}
	}
}

// teamMember is the live mission of a console, with its handle there.
func (s *Service) teamMember(sessionID string) (MissionRun, string, error) {
	ms, ok := s.MissionOf(sessionID)
	if !ok {
		return MissionRun{}, "", ErrNotInMission
	}
	handle := ""
	if t, ok := ms.TaskOf(sessionID); ok {
		handle = t.Handle
	}
	if in, ok := s.hub.SessionInfo(sessionID); ok && handle == "" {
		handle = in.Handle
	}
	return ms, handle, nil
}

// teamText checks one message of an agent.
func teamText(text, what string) (string, error) {
	text = strings.TrimSpace(text)
	switch n := len([]rune(text)); {
	case n == 0:
		return "", fmt.Errorf("%s is empty", what)
	case n > teamTextMax:
		return "", fmt.Errorf("%s is %d characters long, at most %d: put the details in the brief with mission_doc and refer to them", what, n, teamTextMax)
	}
	return text, nil
}

// teamNames lists whom a console can reach in its mission.
func teamNames(ms MissionRun, sessionID string) string {
	var hs []string
	for _, t := range ms.Tasks {
		if t.SessionID != "" && t.SessionID != sessionID && !slices.Contains(hs, "@"+t.Handle) {
			hs = append(hs, "@"+t.Handle)
		}
	}
	return strings.Join(append(hs, "lead"), ", ")
}

func teamGoal(ms MissionRun) string { return fmt.Sprintf("%q", clipText(ms.Goal, 80)) }

// TeamAsk is team_ask: a question to a teammate, delivered when it is ready, or to the lead,
// which wakes it. The answer comes back as a message, never as the tool's result.
func (s *Service) TeamAsk(sessionID, to, question string) (string, error) {
	ms, me, err := s.teamMember(sessionID)
	if err != nil {
		return "", err
	}
	if question, err = teamText(question, "the question"); err != nil {
		return "", err
	}
	if strings.TrimSpace(to) == "" {
		return "", errors.New("say whom you ask in to: " + teamNames(ms, sessionID))
	}
	q := MissionQuestion{ID: NewQuestionID(), From: me, FromID: sessionID, Text: question, At: s.Now().UTC()}
	lead := IsMissionLead(ms, to)
	if lead {
		q.To = "lead"
	} else {
		t, ok := ms.MemberByHandle(to)
		switch {
		case !ok:
			return "", fmt.Errorf("%s is not in this mission. You can ask: %s", strings.TrimSpace(to), teamNames(ms, sessionID))
		case t.SessionID == sessionID:
			return "", errors.New("that is you: ask a teammate or the lead")
		}
		if in, ok := s.hub.SessionInfo(t.SessionID); !ok || !takesText(in) {
			return "", fmt.Errorf("@%s's console is gone or has no agent in it now: ask the lead instead", t.Handle)
		}
		q.To, q.ToID = t.Handle, t.SessionID
	}
	if ms.OpenQuestions() >= MaxOpenQuestions {
		return "", fmt.Errorf("this mission has %d open questions already: wait for answers, or tell the lead what you need with mission_report", MaxOpenQuestions)
	}
	if !lead {
		if err := s.teamSpend(ms, sessionID, q.ToID, me, q.To); err != nil {
			return "", err
		}
	}
	full := false
	ms, ok := s.UpdateMission(ms.ID, func(m *MissionRun) bool {
		if m.OpenQuestions() >= MaxOpenQuestions {
			full = true
			return false
		}
		m.Questions = append(m.Questions, q)
		return true
	})
	if full {
		return "", fmt.Errorf("this mission has %d open questions already: wait for answers, or tell the lead what you need with mission_report", MaxOpenQuestions)
	}
	if !ok {
		return "", ErrNotInMission
	}
	s.MissionThreadMessage(ms, ThreadMessage{SessionID: sessionID, Handle: me, Text: question,
		Team: &TeamRef{From: me, To: q.To, Kind: "ask", QuestionID: q.ID}})
	if lead {
		s.MissionEvent(ms.ID, fmt.Sprintf("@%s asks you (%s): %s", me, q.ID, question))
		return fmt.Sprintf("Asked the lead as %s. The answer comes to you as a message: go on with what does not depend on it, or end your turn.", q.ID), nil
	}
	s.teamSend(ms.ID, q.ToID, fmt.Sprintf("@%s asks you (mission %s, question %s): %s\nAnswer with team_reply, question %s.", me, teamGoal(ms), q.ID, question, q.ID))
	return fmt.Sprintf("Asked @%s as %s. The hub delivers it when @%s is ready, and the answer comes to you as a message: go on with what does not depend on it, or end your turn.", q.To, q.ID, q.To), nil
}

// TeamReply is team_reply: the answer to a question asked of this console, delivered to the
// asker. It closes the question.
func (s *Service) TeamReply(sessionID, questionID, answer string) (string, error) {
	ms, me, err := s.teamMember(sessionID)
	if err != nil {
		return "", err
	}
	if answer, err = teamText(answer, "the answer"); err != nil {
		return "", err
	}
	questionID = strings.TrimSpace(questionID)
	q, ok := ms.Question(questionID)
	switch {
	case !ok:
		return "", fmt.Errorf("there is no question %q in this mission; mission_read lists the ones asked of you", questionID)
	case !q.Open():
		return "", fmt.Errorf("%s was answered already", q.ID)
	case q.ToID != sessionID && q.To == "lead":
		return "", fmt.Errorf("%s was asked of the lead, not of you", q.ID)
	case q.ToID != sessionID:
		return "", fmt.Errorf("%s was asked of @%s, not of you", q.ID, q.To)
	}
	if in, ok := s.hub.SessionInfo(q.FromID); ok && takesText(in) {
		if err := s.teamSpend(ms, sessionID, q.FromID, me, q.From); err != nil {
			return "", err
		}
	}
	now := s.Now().UTC()
	answered := false
	ms, ok = s.UpdateMission(ms.ID, func(m *MissionRun) bool {
		for i := range m.Questions {
			if x := &m.Questions[i]; x.ID == q.ID && x.Open() {
				x.Answer, x.AnsweredAt = clipLen(answer, maxMissionAnswer), &now
				answered = true
				return true
			}
		}
		return false
	})
	if !ok || !answered {
		return "", fmt.Errorf("%s was answered already", q.ID)
	}
	s.MissionThreadMessage(ms, ThreadMessage{SessionID: sessionID, Handle: me, Text: answer,
		Team: &TeamRef{From: me, To: q.From, Kind: "reply", QuestionID: q.ID}})
	if in, ok := s.hub.SessionInfo(q.FromID); !ok || !takesText(in) {
		return fmt.Sprintf("Answered %s. @%s's console is gone, so the answer is only in the thread.", q.ID, q.From), nil
	}
	s.teamSend(ms.ID, q.FromID, fmt.Sprintf("@%s answers your question %s: %s", me, q.ID, answer))
	return fmt.Sprintf("Answered %s; @%s gets it as a message.", q.ID, q.From), nil
}

// TeamUpdate is team_update: a note for the team. Delivered to a teammate named in to;
// otherwise, and to the lead, only kept in the thread for the lead's next turn.
func (s *Service) TeamUpdate(sessionID, text, to string) (string, error) {
	ms, me, err := s.teamMember(sessionID)
	if err != nil {
		return "", err
	}
	if text, err = teamText(text, "the note"); err != nil {
		return "", err
	}
	ref := &TeamRef{From: me, Kind: "update"}
	var target MissionTask
	switch {
	case strings.TrimSpace(to) == "":
	case IsMissionLead(ms, to):
		ref.To = "lead"
	default:
		t, ok := ms.MemberByHandle(to)
		switch {
		case !ok:
			return "", fmt.Errorf("%s is not in this mission. Your team: %s", strings.TrimSpace(to), teamNames(ms, sessionID))
		case t.SessionID == sessionID:
			return "", errors.New("that is you: leave to out to note it for the team, or name a teammate")
		}
		target, ref.To = t, t.Handle
		if in, ok := s.hub.SessionInfo(t.SessionID); ok && takesText(in) {
			if err := s.teamSpend(ms, sessionID, t.SessionID, me, t.Handle); err != nil {
				return "", err
			}
		}
	}
	s.MissionThreadMessage(ms, ThreadMessage{SessionID: sessionID, Handle: me, Text: text, Team: ref})
	if target.SessionID == "" {
		return "Noted in the mission's thread; the lead reads it on its next turn.", nil
	}
	if in, ok := s.hub.SessionInfo(target.SessionID); !ok || !takesText(in) {
		return fmt.Sprintf("Noted in the thread. @%s's console is gone or has no agent in it now, so nothing was delivered.", target.Handle), nil
	}
	s.teamSend(ms.ID, target.SessionID, fmt.Sprintf("@%s tells you (mission %s): %s", me, teamGoal(ms), text))
	return fmt.Sprintf("Noted; @%s gets it as a message.", target.Handle), nil
}

// MissionDocEdit is mission_doc: append to the shared brief, or replace it at the rev the
// agent read, so a newer change of a teammate is never overwritten. It wakes nobody.
func (s *Service) MissionDocEdit(sessionID, text, mode string, rev *int) (string, error) {
	ms, me, err := s.teamMember(sessionID)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	switch {
	case mode != "append" && mode != "replace":
		return "", errors.New(`mode is "append" or "replace"`)
	case text == "":
		return "", errors.New("text is empty")
	case mode == "replace" && rev == nil:
		return "", errors.New("replace needs rev: the brief's rev as mission_read shows it")
	}
	var problem error
	ms, ok := s.UpdateMission(ms.ID, func(m *MissionRun) bool {
		next := text
		if mode == "append" {
			if strings.TrimSpace(m.Doc) != "" {
				next = strings.TrimRight(m.Doc, "\n") + "\n\n" + text
			}
		} else if *rev != m.DocRev {
			problem = fmt.Errorf("the brief changed since rev %d (it is at rev %d): read it again with mission_read and redo your change on it", *rev, m.DocRev)
			return false
		}
		if len(next) > maxMissionDoc {
			problem = fmt.Errorf("the brief would be %d bytes, at most %d: shorten it", len(next), maxMissionDoc)
			return false
		}
		m.Doc = next
		m.DocRev++
		return true
	})
	if problem != nil {
		return "", problem
	}
	if !ok {
		return "", ErrNotInMission
	}
	note := fmt.Sprintf("updated the brief (rev %d)", ms.DocRev)
	if mode == "append" {
		note += ": " + clipText(text, 600)
	}
	s.MissionThreadMessage(ms, ThreadMessage{SessionID: sessionID, Handle: me, Text: note, Team: &TeamRef{From: me, Kind: "doc"}})
	return fmt.Sprintf("The brief is at rev %d now. Tell the teammates it affects with team_update.", ms.DocRev), nil
}

// MissionReport is mission_report on the console's own task: done marks it done with the
// summary as its result; blocked and needs wake the lead; progress is only recorded.
func (s *Service) MissionReport(sessionID, status, summary, needs string) (string, error) {
	ms, me, err := s.teamMember(sessionID)
	if err != nil {
		return "", err
	}
	if status != "done" && status != "blocked" && status != "progress" {
		return "", errors.New(`status is "done", "blocked" or "progress"`)
	}
	if summary, err = teamText(summary, "the summary"); err != nil {
		return "", err
	}
	if needs = strings.TrimSpace(needs); needs != "" {
		if needs, err = teamText(needs, "needs"); err != nil {
			return "", err
		}
	}
	task, ok := ms.TaskOf(sessionID)
	if !ok {
		return "", errors.New("you have no task in this mission to report on")
	}
	if status == "done" && task.State == TaskCancelled {
		return "", fmt.Errorf("%s was cancelled: there is nothing to finish", task.ID)
	}
	text := task.ID + " " + status + ": " + summary
	if needs != "" {
		text += "\nNeeds: " + needs
	}
	s.MissionThreadMessage(ms, ThreadMessage{SessionID: sessionID, Handle: me, Text: text, Team: &TeamRef{From: me, To: "lead", Kind: "report"}})
	switch status {
	case "done":
		if _, ok := s.CompleteMissionTask(ms.ID, task.ID, summary); !ok {
			return "", fmt.Errorf("%s could not be marked done", task.ID)
		}
		if needs != "" {
			s.MissionEvent(ms.ID, fmt.Sprintf("@%s needs: %s", me, needs))
		}
		return fmt.Sprintf("Reported %s done; the lead has your summary. End your turn unless something is left to do for it.", task.ID), nil
	case "blocked":
		reason := fmt.Sprintf("@%s is blocked on %s: %s", me, task.ID, summary)
		if needs != "" {
			reason += " Needs: " + needs
		}
		s.MissionEvent(ms.ID, reason)
		return "Told the lead you are blocked. Its answer comes as a message: end your turn.", nil
	}
	if needs != "" {
		s.MissionEvent(ms.ID, fmt.Sprintf("@%s needs (%s): %s", me, task.ID, needs))
		return "Noted, and the lead is told what you need. Go on with what you can.", nil
	}
	return "Noted.", nil
}

// MissionReadText is mission_read: the mission as an agent of it needs to see it.
func (s *Service) MissionReadText(sessionID string) (string, error) {
	ms, me, err := s.teamMember(sessionID)
	if err != nil {
		return "", err
	}
	now := s.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "Mission: %s\n", ms.Goal)
	state := ms.State
	switch ms.State {
	case MissionPaused:
		state += " (by the owner: finish what you do, start nothing new)"
	case MissionWaiting:
		state += " (the lead waits for the owner)"
	}
	fmt.Fprintf(&b, "State: %s\n", state)
	if strings.TrimSpace(ms.DoneWhen) != "" {
		fmt.Fprintf(&b, "Done when: %s\n", ms.DoneWhen)
	}
	lead := ms.Lead
	if lead == "" {
		lead = "the lead"
	}
	fmt.Fprintf(&b, "Lead: %s (team_ask with to \"lead\")\nYou: @%s\n", lead, me)

	b.WriteString("\nYour tasks:\n")
	for _, t := range ms.Tasks {
		if t.SessionID != sessionID {
			continue
		}
		fmt.Fprintf(&b, "- %s [%s] %s", t.ID, t.State, t.Title)
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(&b, " (after %s)", strings.Join(t.DependsOn, ", "))
		}
		b.WriteString("\n")
		switch {
		case t.State == TaskDone || t.State == TaskFailed:
			if t.Result != "" {
				fmt.Fprintf(&b, "  Result: %s\n", clipText(t.Result, 600))
			}
		case t.State != TaskCancelled && strings.TrimSpace(t.Text) != "":
			b.WriteString("  " + strings.ReplaceAll(clipLen(strings.TrimSpace(t.Text), teamTextMax), "\n", "\n  ") + "\n")
		}
	}

	b.WriteString("\nTeam:\n")
	roles, parts := s.teamRoles(ms)
	var seen []string
	for _, t := range ms.Tasks {
		if t.SessionID == "" || slices.Contains(seen, t.SessionID) {
			continue
		}
		seen = append(seen, t.SessionID)
		var about []string
		if r := roles[t.SessionID]; r != "" {
			about = append(about, "role "+r)
		}
		if p := parts[t.SessionID]; p != "" {
			about = append(about, "part "+p)
		}
		if in, ok := s.hub.SessionInfo(t.SessionID); ok {
			st := in.AgentState
			if in.Paused {
				st = "paused"
			}
			about = append(about, st)
		} else {
			about = append(about, "console gone")
		}
		var tasks []string
		for _, x := range ms.Tasks {
			if x.SessionID == t.SessionID {
				tasks = append(tasks, fmt.Sprintf("%s %s [%s]", x.ID, clipText(x.Title, 80), x.State))
			}
		}
		you := ""
		if t.SessionID == sessionID {
			you = " (you)"
		}
		fmt.Fprintf(&b, "- @%s%s: %s; %s\n", t.Handle, you, strings.Join(about, ", "), strings.Join(tasks, "; "))
	}

	fmt.Fprintf(&b, "\nShared brief (rev %d; mission_doc changes it):\n", ms.DocRev)
	if strings.TrimSpace(ms.Doc) == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(strings.TrimRight(ms.Doc, "\n") + "\n")
	}

	var toMe, mine []string
	for _, q := range ms.Questions {
		if !q.Open() {
			continue
		}
		switch {
		case q.ToID == sessionID:
			toMe = append(toMe, fmt.Sprintf("- %s from @%s: %s", q.ID, q.From, clipText(q.Text, 600)))
		case q.FromID == sessionID:
			to := "@" + q.To
			if q.To == "lead" {
				to = "the lead"
			}
			mine = append(mine, fmt.Sprintf("- %s to %s, %s ago: %s", q.ID, to, teamAgo(now, q.At), clipText(q.Text, 200)))
		}
	}
	if len(toMe) > 0 {
		b.WriteString("\nQuestions to you, answer each with team_reply:\n" + strings.Join(toMe, "\n") + "\n")
	}
	if len(mine) > 0 {
		b.WriteString("\nYour questions still waiting for an answer:\n" + strings.Join(mine, "\n") + "\n")
	}

	var msgs []ThreadMessage
	for _, m := range s.TeamMessages(ms, 200) {
		if m.Team != nil && (m.SessionID == sessionID || m.Team.To == "" || Norm(m.Team.To) == Norm(me) || Norm(m.Team.From) == Norm(me)) {
			msgs = append(msgs, m)
		}
	}
	if len(msgs) > teamReadLast {
		msgs = msgs[len(msgs)-teamReadLast:]
	}
	if len(msgs) > 0 {
		b.WriteString("\nLatest team messages:\n")
		for _, m := range msgs {
			to := m.Team.To
			switch to {
			case "":
				to = "team"
			case "lead":
			default:
				to = "@" + to
			}
			kind := m.Team.Kind
			if m.Team.QuestionID != "" {
				kind += " " + m.Team.QuestionID
			}
			fmt.Fprintf(&b, "- %s ago, @%s → %s (%s): %s\n", teamAgo(now, m.At), m.Team.From, to, kind, clipText(m.Text, 600))
		}
	}
	return b.String(), nil
}

// teamRoles are the group roles and the parts of a mission's consoles, by session id.
func (s *Service) teamRoles(ms MissionRun) (map[string]string, map[string]string) {
	roles, parts := map[string]string{}, map[string]string{}
	sc, err := s.newScope(ms.WorkspaceID, "")
	if err != nil {
		return roles, parts
	}
	for _, t := range ms.Tasks {
		if t.SessionID == "" {
			continue
		}
		if g := sc.groupOf(t.SessionID); g != nil {
			roles[t.SessionID] = firstNonEmpty(GroupOf(g).Role, GroupOf(g).Name)
		}
		if in, ok := sc.byID[t.SessionID]; ok && len(sc.proj.Parts) > 1 {
			if pt, ok := sc.proj.PartByID(in.PartID); ok {
				parts[t.SessionID] = pt.Name
			} else if in.Root {
				parts[t.SessionID] = "project root"
			}
		}
	}
	return roles, parts
}

// teamAgo says how long ago t was, for an agent.
func teamAgo(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", max(int(d.Seconds()), 0))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

// ---- deliveries to teammates ----

// teamHolds reports a live mission whose team talk waits: paused, not approved yet, or
// waiting for the owner to let it go on past a limit or no progress.
func teamHolds(ms MissionRun) bool {
	switch ms.State {
	case MissionPaused, MissionAwaiting:
		return true
	case MissionWaiting:
		// a limit reached or no progress (askToGoOn, missionrun.go); released when the owner
		// says go on
		return ms.WaitKind == WaitLimit || ms.WaitKind == WaitNoProgress
	}
	return false
}

// teamSpend counts one message from an agent to a teammate against the mission's budget,
// or says why it may not go. The lead hears it once an hour when the budget is spent.
func (s *Service) teamSpend(ms MissionRun, fromSID, toSID, from, to string) error {
	now := s.Now()
	pair := fromSID + "|" + toSID
	if toSID < fromSID {
		pair = toSID + "|" + fromSID
	}
	p := &s.team
	p.mu.Lock()
	p.initLocked()
	var recent []teamSpent
	between := 0
	for _, x := range p.spent[ms.ID] {
		if now.Sub(x.at) < teamBudgetWindow {
			recent = append(recent, x)
			if x.pair == pair {
				between++
			}
		}
	}
	var err error
	reason := ""
	switch {
	case len(recent) >= teamBudgetMission:
		err = fmt.Errorf("the team has sent %d messages between agents in the last hour, the most a mission may, so this one was not sent. Go on with your task; what you need goes to the lead with mission_report (needs)", teamBudgetMission)
		reason = fmt.Sprintf("the team's messages are used up (%d between agents in the last hour); the agents can only reach you now", teamBudgetMission)
	case between >= teamBudgetPair:
		err = fmt.Errorf("you and @%s have sent each other %d messages in the last hour, the most two agents may, so this one was not sent. Settle it in the brief with mission_doc, or tell the lead what is left with mission_report (needs)", to, teamBudgetPair)
		reason = fmt.Sprintf("@%s and @%s have used up their messages to each other (%d in the last hour)", from, to, teamBudgetPair)
	default:
		p.spent[ms.ID] = append(recent, teamSpent{now, pair})
		p.mu.Unlock()
		return nil
	}
	p.spent[ms.ID] = recent
	last, told := p.woke[ms.ID]
	wake := !told || now.Sub(last) >= teamBudgetWindow
	if wake {
		p.woke[ms.ID] = now
	}
	p.mu.Unlock()
	if wake {
		s.MissionEvent(ms.ID, reason)
	}
	return err
}

// teamHold takes back the team deliveries not typed yet of a mission that stopped going on
// (paused or ended) at once, not on the next tick; cancelMissionDeliveries calls it. While a
// flush runs it does nothing: the tick 250 ms later does it.
func (s *Service) teamHold() {
	p := &s.team
	if !p.flush.TryLock() {
		return
	}
	defer p.flush.Unlock()
	s.teamFlushAll(s.Now())
}

// teamSend queues a message for a teammate's console and sends it as soon as the pace allows.
func (s *Service) teamSend(missionID, sessionID, text string) {
	p := &s.team
	p.mu.Lock()
	p.initLocked()
	p.held[sessionID] = append(p.held[sessionID], teamHeld{missionID, text})
	p.mu.Unlock()
	s.teamFlush(s.Now())
}

// teamTick sends what was held back once its console may get it.
func (s *Service) teamTick(now time.Time) {
	p := &s.team
	p.mu.Lock()
	idle := len(p.held) == 0 && len(p.pending) == 0
	p.mu.Unlock()
	if !idle {
		s.teamFlush(now)
	}
}

func (s *Service) teamFlush(now time.Time) {
	p := &s.team
	p.flush.Lock()
	defer p.flush.Unlock()
	s.teamFlushAll(now)
}

// teamFlushAll is teamFlush with p.flush held.
func (s *Service) teamFlushAll(now time.Time) {
	p := &s.team
	p.mu.Lock()
	p.initLocked()
	var ids []string
	for id := range p.held {
		ids = append(ids, id)
	}
	for id := range p.pending {
		if _, ok := p.held[id]; !ok {
			ids = append(ids, id)
		}
	}
	p.mu.Unlock()
	slices.Sort(ids)
	for _, id := range ids {
		s.teamFlushTo(id, now)
	}
}

// teamFlushTo follows the console's team delivery in the engine, then sends what is held for
// it when there is none and its 10 s are up. A delivery not typed yet waits while the mission
// holds the team (teamHolds): it is taken back and goes again when the mission goes on. Once
// the mission ended it is cancelled for good.
func (s *Service) teamFlushTo(sid string, now time.Time) {
	p := &s.team
	p.mu.Lock()
	pend := p.pending[sid]
	p.mu.Unlock()
	if pend != nil {
		d, ok := s.hub.Delivery(pend.id)
		ms, live := s.Missions.Get(pend.missionID)
		live = live && !ms.Ended()
		hold := !live || teamHolds(ms)
		again := false
		switch {
		case !ok:
		case d.State == delivery.Queued || d.State == delivery.Waiting:
			if !hold {
				return // one at a time: what comes meanwhile goes with the next
			}
			if _, err := s.hub.CancelDelivery(pend.id); err != nil {
				return
			}
			again = live
		case d.State == delivery.Typing:
			return
		case d.State == delivery.Cancelled:
			again = live && hold // the owner's cancel is final; a hold's is not
		case d.State == delivery.Expired:
			again = live
		case d.State == delivery.Failed:
			if live {
				s.teamNote(ms, fmt.Sprintf("A team message to @%s was not delivered: %s", d.Handle, firstNonEmpty(d.Error, d.Code)))
			}
		}
		p.mu.Lock()
		delete(p.pending, sid)
		if again {
			back := make([]teamHeld, 0, len(pend.texts)+len(p.held[sid]))
			for _, t := range pend.texts {
				back = append(back, teamHeld{pend.missionID, t})
			}
			p.held[sid] = append(back, p.held[sid]...)
		}
		p.mu.Unlock()
	}

	p.mu.Lock()
	held := p.held[sid]
	if len(held) == 0 || now.Sub(p.last[sid]) < teamEvery {
		if len(held) == 0 {
			delete(p.held, sid)
		}
		p.mu.Unlock()
		return
	}
	mid := held[0].missionID
	p.mu.Unlock()

	ms, ok := s.Missions.Get(mid)
	if !ok || ms.Ended() {
		s.teamTake(sid, mid, -1)
		return
	}
	if teamHolds(ms) {
		return
	}
	var texts []string
	n := 0
	for _, h := range held {
		if h.missionID != mid {
			continue
		}
		l := len([]rune(h.text)) + 2
		if len(texts) > 0 && n+l > teamBundleMax {
			break
		}
		n += l
		texts = append(texts, h.text)
	}
	in, ok := s.hub.SessionInfo(sid)
	if !ok || !takesText(in) {
		s.teamTake(sid, mid, -1)
		who := "a teammate"
		if ok {
			who = "@" + in.Handle
		}
		s.teamNote(ms, fmt.Sprintf("%d team message(s) to %s were not delivered: the console is gone or has no agent in it.", len(texts), who))
		return
	}
	d, err := s.hub.Deliver(sid, delivery.Request{Text: strings.Join(texts, "\n\n"), Source: "team", DeadlineSec: teamDeadline})
	p.mu.Lock()
	p.last[sid] = now
	p.mu.Unlock()
	if errors.Is(err, delivery.ErrQueueFull) {
		return // its queue is full of work: try again in 10 s
	}
	s.teamTake(sid, mid, len(texts))
	if err != nil {
		s.teamNote(ms, fmt.Sprintf("A team message to @%s was not delivered: %v", in.Handle, err))
		return
	}
	p.mu.Lock()
	p.pending[sid] = &teamPending{id: d.ID, missionID: mid, texts: texts}
	p.mu.Unlock()
	// talk is not work: the turn it brings is no result for forward lines (track.go)
	s.mu.Lock()
	s.turnContext[sid] = append(s.turnContext[sid], d.ID)
	s.mu.Unlock()
}

// teamTake takes the first n messages held for a console in a mission out of its queue, all
// of them for n < 0; what came in meanwhile stays.
func (s *Service) teamTake(sid, missionID string, n int) {
	p := &s.team
	p.mu.Lock()
	defer p.mu.Unlock()
	var rest []teamHeld
	for _, h := range p.held[sid] {
		if h.missionID == missionID && n != 0 {
			n--
			continue
		}
		rest = append(rest, h)
	}
	if len(rest) == 0 {
		delete(p.held, sid)
	} else {
		p.held[sid] = rest
	}
}

func (s *Service) teamNote(ms MissionRun, text string) {
	s.MissionThreadMessage(ms, ThreadMessage{Kind: "note", Role: "hub", Text: text})
}
