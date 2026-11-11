package orch

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// Errors of POST /api/projects/{id}/commands.
var (
	ErrEmptyText      = errors.New("the command text is empty")
	ErrCommandTooLong = errors.New("the command text is longer than 4000 characters")
	ErrThreadBusy     = errors.New("3 commands are already queued behind the planner turn of this thread")
	ErrBadCard        = errors.New("cardId is not an orchestrator card in this workspace")
	ErrWrongProject   = errors.New("the workspace does not belong to this project")
	ErrMixedBoards    = errors.New("consoles on different boards cannot share a group")
)

const (
	maxCommand      = 4000
	maxPlannerText  = 600 // without talk first, longer text goes out as it is
	maxThreadQueued = 3
)

// CommandRequest is the body of POST /api/projects/{id}/commands.
type CommandRequest struct {
	Text        string   `json:"text"`
	Source      string   `json:"source"` // typed | voice
	WorkspaceID string   `json:"workspaceId"`
	CardID      string   `json:"cardId"`
	ClientID    string   `json:"clientId"`
	Selection   []string `json:"selection"`
	Planner     string   `json:"planner"` // auto | off
	ReplyTo     string   `json:"replyTo"` // id of the thread message this answers
	ProjectID   string   `json:"-"`
	By          string   `json:"-"` // "admin" | device id
}

// CommandResponse is the 202 body.
type CommandResponse struct {
	CommandID    string          `json:"commandId"`
	ThreadID     string          `json:"threadId"`
	HandledBy    string          `json:"handledBy"` // parser | planner | pending
	OrderID      string          `json:"orderId,omitempty"`
	UIActions    []UIAction      `json:"uiActions"`
	Confirmation *Confirmation   `json:"confirmation"`
	Messages     []ThreadMessage `json:"messages"`
}

// execCtx carries a command through planning, confirmation and execution.
type execCtx struct {
	commandID, threadID, projectID, workspaceID, cardID, clientID, source, by string
	selection                                                                 []string
	plannerMode                                                               string
	planned                                                                   bool
	msgs                                                                      []ThreadMessage
	ui                                                                        []UIAction
	orderID                                                                   string
	reply                                                                     *ReplyRef // the earlier message this answers
}

func (s *Service) say(c *execCtx, m ThreadMessage) ThreadMessage {
	m.ThreadID = c.threadID
	if m.Role == "" {
		m.Role = "hub"
	}
	m = s.Threads.Add(m)
	c.msgs = append(c.msgs, m)
	return m
}

func (s *Service) note(c *execCtx, text string) { s.say(c, ThreadMessage{Kind: "note", Text: text}) }

type queuedCmd struct {
	ctx  *execCtx
	text string
}

// threadQueue serialises a thread while a planner turn runs.
type threadQueue struct {
	busy      bool
	queue     []queuedCmd
	thinkID   string
	thinkSaid bool
	startedAt time.Time
	ctx       *execCtx
	text      string
}

var (
	confirmWords = regexp.MustCompile(`(?i)^(yes|y|yep|yeah|confirm|do it|go ahead|sure)[.! ]*$`)
	cancelWords  = regexp.MustCompile(`(?i)^(no|n|nope|cancel|don't|do not)[.! ]*$`)
)

// Command runs the pipeline of §3 for one command.
func (s *Service) Command(req CommandRequest) (CommandResponse, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return CommandResponse{}, ErrEmptyText
	}
	if len([]rune(text)) > maxCommand {
		return CommandResponse{}, ErrCommandTooLong
	}
	ws, proj, ok := s.hub.Workspace(req.WorkspaceID)
	if !ok {
		return CommandResponse{}, ErrWorkspaceNotFound
	}
	if req.ProjectID != "" && proj.ID != req.ProjectID {
		return CommandResponse{}, ErrWrongProject
	}
	tid := proj.ID
	if req.CardID != "" {
		if _, err := s.newScope(ws.ID, req.CardID); err != nil {
			return CommandResponse{}, err
		}
		tid = req.CardID
	}
	src := req.Source
	if src != "voice" {
		src = "typed"
	}
	c := &execCtx{commandID: auth.RandomID("cmd_"), threadID: tid, projectID: proj.ID, workspaceID: ws.ID,
		cardID: req.CardID, clientID: req.ClientID, source: src, by: req.By, selection: req.Selection, plannerMode: req.Planner,
		reply: s.replyRef(tid, req.ReplyTo)}
	s.mu.Lock()
	tq := s.thread[tid]
	if tq == nil {
		tq = &threadQueue{}
		s.thread[tid] = tq
	}
	if tq.busy {
		if len(tq.queue) >= maxThreadQueued {
			s.mu.Unlock()
			return CommandResponse{}, ErrThreadBusy
		}
		tq.queue = append(tq.queue, queuedCmd{ctx: c, text: text})
		s.mu.Unlock()
		s.say(c, ThreadMessage{Role: "user", Kind: "order", Text: text, Source: src, ReplyTo: c.reply})
		return s.response(c, "pending", nil), nil
	}
	s.mu.Unlock()
	s.say(c, ThreadMessage{Role: "user", Kind: "order", Text: text, Source: src, ReplyTo: c.reply})
	handled, cf := s.process(c, text)
	return s.response(c, handled, cf), nil
}

// ErrWorkspaceNotFound mirrors project.ErrWorkspaceNotFound for the API.
var ErrWorkspaceNotFound = errors.New("workspace not found")

func (s *Service) response(c *execCtx, handled string, cf *Confirmation) CommandResponse {
	r := CommandResponse{CommandID: c.commandID, ThreadID: c.threadID, HandledBy: handled, OrderID: c.orderID,
		UIActions: c.ui, Messages: c.msgs}
	if r.UIActions == nil {
		r.UIActions = []UIAction{}
	}
	if r.Messages == nil {
		r.Messages = []ThreadMessage{}
	}
	if cf != nil {
		cp := *cf
		cp.ctx = nil
		r.Confirmation = &cp
	}
	return r
}

// process handles one command text (not queued). It returns handledBy and
// a new confirmation, if any.
func (s *Service) process(c *execCtx, text string) (string, *Confirmation) {
	now := s.Now()
	if cf, ok := s.Confirms.Pending(c.threadID, now); ok {
		proposal := cf.Kind == ConfProposal
		switch {
		case confirmWords.MatchString(text) || (proposal && agreeWords.MatchString(text)):
			s.decide(cf.ID, "confirm", "", c.by, c)
			return "parser", nil
		case cancelWords.MatchString(text) || (proposal && declineWords.MatchString(text)):
			s.decide(cf.ID, "cancel", "", c.by, c)
			return "parser", nil
		case proposal:
			// talking about the plan leaves it on the table
		default:
			if d, err := s.Confirms.Decide(cf.ID, "cancel", "", c.by, now); err == nil {
				s.publishConfirm(d)
				s.note(c, "Cancelled the pending confirmation.")
			}
		}
	}
	sc, err := s.newScope(c.workspaceID, c.cardID)
	if err != nil {
		s.say(c, ThreadMessage{Kind: "error", Text: err.Error()})
		return "parser", nil
	}
	lead := s.leadOf(sc)
	said, toLead := addressedToLead(sc, lead, text)
	if toLead {
		text = said
	}
	in := Parse(text, ParseContext{Agents: agentIDs(s.hub.Agents())})
	in.Text = withReply(in.Text, c.reply)
	var b built
	if toLead {
		b = built{planner: true}
	} else {
		b = s.build(c, sc, in, text)
		// a greeting is the start of a conversation; the parser's answer stays for when she can't
		if lead.talkFirst && in.Kind == IntentSmalltalk {
			b.planner = true
		}
	}
	if b.planner {
		limit := maxPlannerText
		if lead.talkFirst {
			limit = maxCommand
		}
		if ok, reason := s.PlannerAvailable(c.plannerMode); ok && len([]rune(text)) <= limit {
			ok, why := s.startPlanner(c, sc, text)
			if ok {
				return "pending", nil
			}
			if why != "" {
				b.notes = append([]string{"Planner unavailable: " + why}, b.notes...)
			}
		} else if reason != "" && reason != "disabled" {
			b.notes = append([]string{"Planner unavailable: " + reason}, b.notes...)
		}
		b = s.fallback(c, sc, in, text, b)
	}
	for _, n := range b.notes {
		s.note(c, n)
	}
	if b.reply != "" {
		s.say(c, ThreadMessage{Kind: "reply", Text: b.reply})
	}
	if b.confirm != nil {
		return "parser", s.addConfirm(c, b.confirm)
	}
	if cf := s.policy(c, sc, b.steps); cf != nil {
		return "parser", s.addConfirm(c, cf)
	}
	s.execute(c, b.steps)
	return "parser", nil
}

func agentIDs(l []agent.Agent) []string {
	out := make([]string, 0, len(l))
	for _, a := range l {
		out = append(out, a.ID)
	}
	return out
}

func (s *Service) addConfirm(c *execCtx, cf *Confirmation) *Confirmation {
	cf.ThreadID, cf.CommandID = c.threadID, c.commandID
	cf.ctx = c
	ttl := time.Duration(s.hub.Settings().Commands.ConfirmTTLSec) * time.Second
	if cf.Kind == ConfProposal {
		ttl = proposalTTL // a conversation takes longer than a yes or no
	}
	if old := s.Confirms.Add(cf, s.Now(), ttl); old != nil {
		s.publishConfirm(*old)
	}
	s.say(c, ThreadMessage{Kind: "confirm", Text: cf.Prompt, ConfirmationID: cf.ID})
	s.publishConfirm(*cf)
	return cf
}

// Decide is POST /api/confirmations/{id}: the first decision wins.
func (s *Service) Decide(id, decision, choice, by string) (Confirmation, error) {
	return s.decide(id, decision, choice, by, nil)
}

func (s *Service) decide(id, decision, choice, by string, in *execCtx) (Confirmation, error) {
	ctx := s.Confirms.ctxOf(id)
	cf, err := s.Confirms.Decide(id, decision, choice, by, s.Now())
	if err != nil {
		if errors.Is(err, ErrConfExpired) {
			s.publishConfirm(cf)
		}
		cf.ctx = nil
		return cf, err
	}
	s.publishConfirm(cf)
	if ctx == nil {
		return cf, nil
	}
	c := *ctx
	c.msgs, c.ui, c.orderID = nil, nil, ""
	if in != nil {
		c.clientID = in.clientID
	}
	if decision == "cancel" {
		if cf.Kind == ConfProposal {
			s.note(&c, "Nothing was sent to the team.")
		} else {
			s.note(&c, "Cancelled.")
		}
	} else {
		steps := cf.Steps
		if choice != "" && (cf.Kind == ConfDidYouMean || cf.Kind == ConfSendToAll) {
			for i := range steps {
				if len(steps[i].Targets) > 0 {
					steps[i].Targets = []string{choice}
				}
			}
		}
		s.execute(&c, steps)
	}
	if in != nil {
		in.msgs = append(in.msgs, c.msgs...)
		in.ui = append(in.ui, c.ui...)
		if in.orderID == "" {
			in.orderID = c.orderID
		}
	} else if len(c.ui) > 0 {
		s.hub.Publish(map[string]any{"type": "command", "commandId": c.commandID, "clientId": c.clientID, "state": "done", "uiActions": c.ui})
	}
	cf.ctx = nil
	return cf, nil
}

// built is the parser's plan for a command.
type built struct {
	steps   []Step
	notes   []string
	reply   string
	confirm *Confirmation
	planner bool // hand the text to the planner (fallback otherwise)
}

// resolveOrNote resolves names; problems become notes.
// noneKnown: no name of a command belongs to anything here, so it may not be about consoles at
// all ("stop the dev server", "gabungkan branch ini ke main"). The planner reads it when there
// is one; without one the hub says what it does not know.
func (s *Service) noneKnown(c *execCtx, r resolution) bool {
	if len(r.unknown) == 0 || len(r.targets) > 0 || len(r.fuzzy) > 0 || len(r.notWired) > 0 {
		return false
	}
	ok, _ := s.PlannerAvailable(c.plannerMode)
	return ok
}

func (s *Service) resolveOrNote(sc *scope, names []string, b *built) (resolution, bool) {
	r := sc.resolve(names)
	for _, h := range r.notWired {
		b.notes = append(b.notes, "@"+h+" is not wired to this card.")
	}
	if len(r.fuzzy) > 0 || len(r.notWired) > 0 {
		return r, false
	}
	if len(r.unknown) > 0 {
		b.notes = append(b.notes, "I don't know "+joinAnd(quoteAll(r.unknown))+".")
		return r, false
	}
	if len(r.targets) == 0 {
		b.notes = append(b.notes, "No agents matched.")
		return r, false
	}
	return r, true
}

func quoteAll(l []string) []string {
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = "\"" + x + "\""
	}
	return out
}

// keepOnBoard drops nodes of other boards: a card directs its own board.
func (sc *scope) keepOnBoard(nodes []string, b *built) []string {
	var out []string
	dropped := false
	for _, id := range nodes {
		if sc.onOtherBoard(id) {
			dropped = true
			continue
		}
		out = append(out, id)
	}
	if dropped {
		b.notes = append(b.notes, "An orchestrator card can only direct consoles on its own board.")
	}
	return out
}

// fuzzyConfirm turns fuzzy candidates into didYouMean / sendToAll.
func (s *Service) fuzzyConfirm(sc *scope, r resolution, step Step) *Confirmation {
	var cands []Candidate
	var ids []string
	for _, l := range r.fuzzy {
		for _, sid := range l {
			cands = append(cands, Candidate{SessionID: sid, Handle: sc.handle(sid)})
			ids = append(ids, sid)
		}
	}
	ids = uniq(append(r.targets, ids...))
	step.Targets = ids
	if len(cands) == 1 && len(r.targets) == 0 {
		step.Targets = []string{cands[0].SessionID}
		return &Confirmation{Kind: ConfDidYouMean, Prompt: "Did you mean @" + cands[0].Handle + "?", Candidates: cands,
			Options: []Option{{ID: "confirm", Label: "Send to @" + cands[0].Handle}, {ID: "cancel", Label: "Cancel"}}, Steps: []Step{step}}
	}
	return &Confirmation{Kind: ConfSendToAll, Prompt: "Send to " + sc.handles(ids) + "?", Candidates: cands,
		Options: []Option{{ID: "confirm", Label: fmt.Sprintf("Send to %d consoles", len(ids))}, {ID: "cancel", Label: "Cancel"}}, Steps: []Step{step}}
}

// build turns an intent into steps (§3.2–3.4).
func (s *Service) build(c *execCtx, sc *scope, in Intent, text string) built {
	var b built
	card := c.cardID != ""
	sendTo := func(targets []string, t, mode string) {
		b.steps = append(b.steps, Step{Op: "send", Targets: targets, Text: t, Mode: mode})
	}
	switch in.Kind {
	case IntentNone, IntentEveryone:
		if in.Kind == IntentEveryone {
			text = in.Text
		}
		b.planner = true
	case IntentSmalltalk:
		b.reply = in.Reply
	case IntentMentionOnly:
		if in.All {
			b.reply = "What should everyone do?"
			break
		}
		b.reply = "What should @" + strings.Join(in.Mentions, ", @") + " do?"
	case IntentSend:
		r := sc.resolve(in.Mentions)
		for _, h := range r.notWired {
			b.notes = append(b.notes, "@"+h+" is not wired to this card.")
		}
		if len(r.notWired) > 0 {
			break
		}
		if len(r.fuzzy) > 0 {
			b.confirm = s.fuzzyConfirm(sc, r, Step{Op: "send", Text: in.Text, Mode: "verbatim"})
			break
		}
		if len(r.unknown) > 0 {
			b.notes = append(b.notes, "I don't know "+joinAnd(quoteAll(r.unknown))+".")
			break
		}
		if len(r.targets) == 1 && !r.multi {
			sendTo(r.targets, in.Text, "verbatim")
			break
		}
		if len(r.targets) == 0 {
			b.notes = append(b.notes, "No agents matched.")
			break
		}
		b.planner = true
	case IntentTell:
		r := sc.resolve(in.Who)
		if in.Name == "address" && !r.ok() {
			b.planner = true // "Ok, …" etc.: not an addressee
			break
		}
		if len(r.fuzzy) > 0 {
			b.confirm = s.fuzzyConfirm(sc, r, Step{Op: "send", Text: in.Text, Mode: "verbatim"})
			break
		}
		if s.noneKnown(c, r) {
			b.planner = true
			break
		}
		if _, ok := s.resolveOrNote(sc, in.Who, &b); !ok {
			break
		}
		b.planner = true // the planner rewrites; without it the text goes verbatim
	case IntentSame:
		var ids []string
		for _, x := range sc.agentsInScope() {
			ids = append(ids, x.ID)
		}
		if len(ids) == 0 {
			b.notes = append(b.notes, "No agents in scope.")
			break
		}
		sendTo(ids, in.Text, "verbatim")
	case IntentSpawn:
		if in.Count > 12 {
			b.notes = append(b.notes, "At most 12 consoles per command.")
			break
		}
		st := Step{Op: "spawn", Count: in.Count, Agent: in.Agent, Role: in.Role}
		if card {
			st.OrchestratorID = c.cardID
		}
		b.steps = append(b.steps, st)
	case IntentGroupCreate:
		switch {
		case in.Count > 0:
			if in.Count > 12 {
				b.notes = append(b.notes, "A group has at most 12 consoles.")
				break
			}
			b.steps = append(b.steps, Step{Op: "spawn", Count: in.Count, Agent: in.Agent, Role: in.Role,
				NewGroup: &NewGroup{Name: strings.TrimSpace(in.Name), Role: in.Role}, OrchestratorID: map[bool]string{true: c.cardID}[card]})
		case in.Select:
			var members []string
			for _, nid := range c.selection {
				if sid, ok := sc.sidOf[nid]; ok {
					members = append(members, sid)
				}
			}
			if len(members) == 0 {
				b.notes = append(b.notes, "Select some consoles first.")
				break
			}
			b.steps = append(b.steps, Step{Op: "group.create", Members: members})
		default:
			if s.noneKnown(c, sc.resolve(in.Who)) {
				b.planner = true
				break
			}
			r, ok := s.resolveOrNote(sc, in.Who, &b)
			if !ok {
				if len(r.fuzzy) > 0 {
					b.confirm = s.fuzzyConfirm(sc, r, Step{Op: "group.create"})
					b.confirm.Steps[0].Members, b.confirm.Steps[0].Targets = b.confirm.Steps[0].Targets, nil
				}
				break
			}
			b.steps = append(b.steps, Step{Op: "group.create", Members: r.targets})
		}
	case IntentGroupAdd:
		g := sc.groupByName(in.Name)
		if g == nil {
			b.notes = append(b.notes, "There is no group \""+in.Name+"\".")
			break
		}
		b.steps = append(b.steps, Step{Op: "group.add", GroupID: g.ID, WorkspaceID: sc.wsOf[g.ID], Count: in.Count, Agent: in.Agent, Role: in.Role})
	case IntentDissolve:
		var gs []*board.Node
		for _, w := range in.Who {
			if g := sc.groupByName(w); g != nil {
				gs = append(gs, g)
			}
		}
		if len(gs) == 0 {
			if in.Name == "delete" || s.noneKnown(c, resolution{unknown: in.Who}) {
				b.planner = true
				break
			}
			b.notes = append(b.notes, "There is no group \""+strings.Join(in.Who, ", ")+"\".")
			break
		}
		for _, g := range gs {
			if in.Name == "delete" {
				if sids := sc.nodeSessions(g.ID); len(sids) > 0 {
					b.steps = append(b.steps, Step{Op: "kill", Targets: sids})
				}
			}
			b.steps = append(b.steps, Step{Op: "group.dissolve", GroupID: g.ID, WorkspaceID: sc.wsOf[g.ID]})
		}
	case IntentPause, IntentResume, IntentRestart, IntentKill:
		var ids []string
		if in.All {
			l := sc.agentsInScope()
			if in.Kind == IntentKill {
				l = sc.consolesInScope()
			}
			for _, x := range l {
				ids = append(ids, x.ID)
			}
			if len(ids) == 0 {
				b.notes = append(b.notes, "No consoles in scope.")
				break
			}
		} else {
			r := sc.resolve(in.Who)
			if len(r.fuzzy) > 0 {
				b.confirm = s.fuzzyConfirm(sc, r, Step{Op: in.Kind})
				break
			}
			if len(r.unknown) > 0 && (in.Kind == IntentResume || in.Kind == IntentPause) {
				b.planner = true // "continue with the refactor"
				break
			}
			if s.noneKnown(c, r) {
				b.planner = true // "stop the dev server"
				break
			}
			var ok bool
			if r, ok = s.resolveOrNote(sc, in.Who, &b); !ok {
				break
			}
			ids = r.targets
		}
		b.steps = append(b.steps, Step{Op: in.Kind, Targets: ids})
	case IntentHandoff:
		a, ok := agentByID(s.hub.Agents(), in.Agent)
		if !ok || a.Kind != agent.KindAgent {
			b.planner = true
			break
		}
		var src string
		if in.From != "" {
			r, ok := s.resolveOrNote(sc, []string{in.From}, &b)
			if !ok || len(r.targets) != 1 {
				if ok {
					b.notes = append(b.notes, "Hand off from one console at a time.")
				}
				break
			}
			src = r.targets[0]
		} else if src = s.lastOrderTarget(c.threadID); src == "" {
			if l := sc.agentsInScope(); len(l) == 1 {
				src = l[0].ID
			}
		}
		if src == "" {
			b.notes = append(b.notes, "Which console should hand off? Say \"hand off to "+in.Agent+" from @Name\".")
			break
		}
		b.steps = append(b.steps, Step{Op: "handoff", Source: src, Agent: a.ID})
	case IntentForward:
		rs, ok1 := s.resolveOrNote(sc, in.Who, &b)
		rt, ok2 := s.resolveOrNote(sc, in.To, &b)
		if !ok1 || !ok2 {
			break
		}
		b.steps = append(b.steps, Step{Op: "forward", Sources: rs.targets, Targets: rt.targets, Mode: "digest"})
	case IntentCancel:
		b.steps = append(b.steps, Step{Op: "cancel"})
	case IntentAsk:
		b.steps = append(b.steps, Step{Op: "ask", Query: in.Query})
	case IntentUI:
		u := *in.UI
		if card && (u.Action == "undo" || u.Action == "redo") {
			b.reply = "Use the main command bar."
			break
		}
		if u.Action == "focus" {
			if g := sc.groupByName(in.Focus); g != nil {
				u.Target = g.ID
			} else if r := sc.resolve([]string{in.Focus}); len(r.targets) == 1 && !r.multi {
				if n := sc.nodeOf[r.targets[0]]; n != nil {
					u.Target = n.ID
				}
			} else if len(r.fuzzy) == 1 {
				for _, l := range r.fuzzy {
					if len(l) == 1 && sc.nodeOf[l[0]] != nil {
						u.Target = sc.nodeOf[l[0]].ID
					}
				}
			}
			if u.Target == "" {
				if s.noneKnown(c, resolution{unknown: []string{in.Focus}}) {
					b.planner = true
					break
				}
				b.notes = append(b.notes, "I can't find \""+in.Focus+"\" on any board.")
				break
			}
			if sc.onOtherBoard(u.Target) {
				u.WorkspaceID = sc.wsOf[u.Target] // the client opens that board, then focuses
			}
		}
		b.steps = append(b.steps, Step{Op: "ui", UIAction: &u})
	case IntentNodeAdd:
		st := Step{Op: "node.add", Kind: in.NodeKind, Data: map[string]any{}}
		switch in.NodeKind {
		case "note":
			st.Data["text"], st.Data["color"] = in.NodeText, "yellow"
		case "web":
			st.Data["url"] = fmt.Sprintf("http://localhost:%d/", in.Port)
			st.Data["port"] = in.Port
		case "orchestrator":
			if len(in.Who) > 0 {
				for _, w := range in.Who {
					if g := sc.groupByName(w); g != nil {
						st.WireTo = append(st.WireTo, g.ID)
					} else if r := sc.resolve([]string{w}); r.ok() {
						for _, sid := range r.targets {
							if n := sc.nodeOf[sid]; n != nil {
								st.WireTo = append(st.WireTo, n.ID)
							}
						}
					}
				}
				st.WireTo = sc.keepOnBoard(st.WireTo, &b)
				if len(st.WireTo) == 0 {
					b.notes = append(b.notes, "I can't find \""+strings.Join(in.Who, ", ")+"\".")
					break
				}
			}
		}
		b.steps = append(b.steps, st)
	case IntentWire:
		orchID := ""
		for _, n := range sc.b.Nodes {
			if n.Kind != KindOrchestrator {
				continue
			}
			if in.Name != "" && Norm(TitleOf(n)) == Norm(in.Name) {
				orchID = n.ID
				break
			}
			if in.Name == "" && orchID == "" {
				orchID = n.ID
			} else if in.Name == "" {
				orchID = "-" // ambiguous
			}
		}
		if card && in.Name == "" {
			orchID = c.cardID
		}
		if orchID == "" || orchID == "-" {
			b.notes = append(b.notes, "Which orchestrator? Name it by its title.")
			break
		}
		var nodes []string
		for _, w := range in.Who {
			if g := sc.groupByName(w); g != nil {
				nodes = append(nodes, g.ID)
				continue
			}
			r, ok := s.resolveOrNote(sc, []string{w}, &b)
			if !ok {
				nodes = nil
				break
			}
			for _, sid := range r.targets {
				if n := sc.nodeOf[sid]; n != nil {
					nodes = append(nodes, n.ID)
				}
			}
		}
		if nodes = sc.keepOnBoard(nodes, &b); len(nodes) > 0 {
			b.steps = append(b.steps, Step{Op: "wire", OrchestratorID: orchID, Targets: uniq(nodes)})
		}
	case IntentWsGo, IntentWsCreate, IntentWsRename, IntentWsClose:
		if card {
			b.reply = "Use the main command bar."
			break
		}
		st := Step{Op: in.Kind, Name: strings.TrimSpace(in.Name)}
		if in.Kind == IntentWsGo || in.Kind == IntentWsClose {
			w, ok := findWorkspace(sc.proj.Workspaces, in.Name)
			if !ok {
				b.notes = append(b.notes, "There is no workspace \""+in.Name+"\".")
				break
			}
			st.WorkspaceID = w
		}
		if in.Kind == IntentWsRename {
			st.WorkspaceID = sc.ws.ID
		}
		b.steps = append(b.steps, st)
	case IntentLoop:
		if in.EverySec < 60 || in.EverySec > 86400 {
			b.notes = append(b.notes, "Loops run every 1 minute to 24 hours.")
			break
		}
		tg := LoopTargets{Kind: "allAgents", IDs: []string{}}
		if card {
			tg = LoopTargets{Kind: "orchestrator", IDs: []string{c.cardID}}
		}
		b.steps = append(b.steps, Step{Op: "loop.create", Loop: &LoopDef{ProjectID: sc.proj.ID, WorkspaceID: sc.ws.ID,
			Name: clipText(in.Text, 40), Enabled: true, Trigger: LoopTrigger{Kind: "interval", EverySec: in.EverySec},
			Text: in.Text, Targets: tg, CreatedBy: "order", Stop: LoopStop{MaxRuns: 20}}})
	case IntentGit:
		b.steps = append(b.steps, Step{Op: "git", Action: in.Git, Repo: in.Repo, Message: in.Message})
	case IntentMemPush:
		st := Step{Op: "memory.push"}
		if len(in.Who) > 0 {
			r, ok := s.resolveOrNote(sc, in.Who, &b)
			if !ok {
				break
			}
			st.Targets = r.targets
		}
		b.steps = append(b.steps, st)
	case IntentMemScan:
		b.steps = append(b.steps, Step{Op: "memory.scan"})
	case IntentDeploy:
		var id string
		for _, l := range s.Layouts.All() {
			if Norm(l.Name) == Norm(in.Name) {
				id = l.ID
				break
			}
		}
		if id == "" {
			b.notes = append(b.notes, "There is no layout \""+in.Name+"\".")
			break
		}
		b.steps = append(b.steps, Step{Op: "layout.deploy", LayoutID: id})
	}
	return b
}

func agentByID(l []agent.Agent, id string) (agent.Agent, bool) {
	for _, a := range l {
		if strings.EqualFold(a.ID, id) || Norm(a.Name) == Norm(id) {
			return a, true
		}
	}
	return agent.Agent{}, false
}

// findWorkspace resolves "2" (tab number) or a workspace name.
func findWorkspace(list []project.Workspace, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if n := Number(name); n > 0 && n <= len(list) {
		return list[n-1].ID, true
	}
	for _, w := range list {
		if Norm(w.Name) == Norm(name) {
			return w.ID, true
		}
	}
	return "", false
}

// lastOrderTarget is the single console of the thread's latest order.
func (s *Service) lastOrderTarget(tid string) string {
	for _, o := range s.Orders.List(func(o Order) bool { return o.ThreadID == tid }) {
		if len(o.Targets) == 1 {
			return o.Targets[0].SessionID
		}
		return ""
	}
	return ""
}

// fallback is §3.11: what happens without the planner.
func (s *Service) fallback(c *execCtx, sc *scope, in Intent, text string, b built) built {
	b.planner = false
	if needsComposing(text) {
		b.notes = append(b.notes, "Writing instructions for agents needs the planner. Name an agent with @ and give the task in your words.")
		return b
	}
	var targets []string
	body := text
	switch in.Kind {
	case IntentSend, IntentTell:
		r := sc.resolve(append(in.Mentions, in.Who...))
		targets, body = r.targets, in.Text
	case IntentEveryone:
		body = in.Text
	}
	if len(targets) == 0 {
		// No addressee: exactly one agent in scope gets it.
		l := sc.agentsInScope()
		switch {
		case in.Kind == IntentNone || in.Kind == IntentEveryone:
			if in.Kind == IntentEveryone && (explicitSame(body) || singleLine(body)) && len(l) > 0 {
				var ids []string
				for _, x := range l {
					ids = append(ids, x.ID)
				}
				b.steps = append(b.steps, Step{Op: "send", Targets: ids, Text: body, Mode: "verbatim"})
				return b
			}
			if len(l) == 1 {
				b.steps = append(b.steps, Step{Op: "send", Targets: []string{l[0].ID}, Text: body, Mode: "verbatim"})
				return b
			}
			note := "Name an agent with @ or enable the planner."
			// the planner is on and only this turn failed: the setting is not the problem
			if ok, why := s.PlannerAvailable(c.plannerMode); ok || why == "cooldown" {
				note = "Name an agent with @ to reach them directly."
			}
			b.notes = append(b.notes, note)
			return b
		}
		return b
	}
	if len(targets) == 1 {
		b.steps = append(b.steps, Step{Op: "send", Targets: targets, Text: body, Mode: "verbatim"})
		return b
	}
	if explicitSame(body) || singleLine(body) {
		b.steps = append(b.steps, Step{Op: "send", Targets: targets, Text: body, Mode: "verbatim"})
		return b
	}
	// Several consoles named: one free agent receives the task.
	var free []string
	for _, sid := range targets {
		if in, ok := sc.byID[sid]; ok && s.free(in) {
			free = append(free, sid)
		}
	}
	pick := targets[0]
	if len(free) > 0 {
		pick = free[0]
	}
	left := len(free) - 1
	if len(free) == 0 {
		left = 0
	}
	b.steps = append(b.steps, Step{Op: "send", Targets: []string{pick}, Text: body, Mode: "verbatim"})
	b.notes = append(b.notes, fmt.Sprintf("Sent to @%s; %d left free.", sc.handle(pick), left))
	return b
}

// policy is the confirmation table of §3.7.
func (s *Service) policy(c *execCtx, sc *scope, steps []Step) *Confirmation {
	var kills, restarts []string
	risky := ""
	typed := true // what is risky was typed by the owner, not planned
	yours := ""
	for _, st := range steps {
		switch st.Op {
		case "kill":
			kills = append(kills, st.Targets...)
		case "restart":
			restarts = append(restarts, st.Targets...)
		case "node.delete":
			return &Confirmation{Kind: ConfDestructive, Steps: steps,
				Prompt:  fmt.Sprintf("Delete from the board: %s?", clipText(st.Text, 200)),
				Options: []Option{{ID: "confirm", Label: "Delete"}, {ID: "cancel", Label: "Cancel"}}}
		case "workspace.close":
			running := 0
			for _, in := range sc.all {
				if in.WorkspaceID == st.WorkspaceID && in.Status == session.StatusRunning {
					running++
				}
			}
			if running > 0 {
				return &Confirmation{Kind: ConfDestructive, Steps: steps,
					Prompt:  fmt.Sprintf("Close this workspace? %d running console(s) stop.", running),
					Options: []Option{{ID: "confirm", Label: "Close workspace"}, {ID: "cancel", Label: "Cancel"}}}
			}
		case "loop.create":
			l := st.Loop
			// A loop belongs to one board: "allAgents" means that board's agents.
			n := 0
			switch l.Targets.Kind {
			case "orchestrator":
				n = len(sc.directed)
			case "handles":
				n = len(l.Targets.IDs)
			default:
				for _, in := range sc.agentsInScope() {
					if in.WorkspaceID == l.WorkspaceID {
						n++
					}
				}
			}
			per := RunsPerHour(*l, s.Now(), s.Loops.Loc)
			return &Confirmation{Kind: ConfLoop, Steps: steps,
				Prompt: fmt.Sprintf("Start a loop every %s: \"%s\"? About %.0f prompt(s)/hour × %d console(s); stops after %d runs.",
					everyText(l.Trigger.EverySec), clipText(l.Text, 120), per, n, l.Stop.MaxRuns),
				Options: []Option{{ID: "confirm", Label: "Start loop"}, {ID: "cancel", Label: "Cancel"}}}
		case "spawn":
			if st.Count > 6 {
				return &Confirmation{Kind: ConfBulkSpawn, Steps: steps, Prompt: fmt.Sprintf("Start %d consoles?", st.Count),
					Options: []Option{{ID: "confirm", Label: fmt.Sprintf("Start %d consoles", st.Count)}, {ID: "cancel", Label: "Cancel"}}}
			}
		case "git", "node.add", "send":
			if risky != "" {
				continue
			}
			switch {
			case st.Op == "send":
				// the owner's own order to an agent too: a slip on the keyboard costs as
				// much as a plan's mistake, and "@Ash delete …" is one word from it. Only
				// the owner's words count: the quote of what is being answered is the
				// agent's own, and "Next I can delete …" in it would hold every answer.
				if m := riskyMatch(withoutReply(st.Text, c.reply), s.hub.Settings().Commands.RiskyPatterns); m != "" {
					risky = fmt.Sprintf("an order for %s that says %q", sc.handles(st.Targets), m)
					yours = fmt.Sprintf("Your order for %s says %q", sc.handles(st.Targets), m)
					typed = typed && !st.Planned
				}
			case !st.Planned:
			case st.Op == "git":
				risky, typed = "a git step", false
			case st.Op == "node.add" && st.Kind == "web":
				risky, typed = "a web tile", false
			}
		}
	}
	if len(kills) > 0 || len(restarts) > 0 {
		verb, ids := "Kill", uniq(kills)
		tail := "Their processes stop; the tiles stay on the board."
		if len(kills) == 0 {
			verb, ids, tail = "Restart", uniq(restarts), "Their current turn is lost."
		}
		return &Confirmation{Kind: ConfDestructive, Steps: steps, Prompt: fmt.Sprintf("%s %s%s? %s", verb, sc.handles(ids), sc.boardsNote(ids), tail),
			Options: []Option{{ID: "confirm", Label: fmt.Sprintf("%s %d console(s)", verb, len(ids))}, {ID: "cancel", Label: "Cancel"}}}
	}
	if risky != "" {
		if typed {
			// the owner's own words, not a plan: say it as such
			return &Confirmation{Kind: ConfRisky, Steps: steps, Prompt: yours + ", which can be dangerous. Send it?",
				Options: []Option{{ID: "confirm", Label: "Send"}, {ID: "cancel", Label: "Cancel"}}}
		}
		return &Confirmation{Kind: ConfRisky, Steps: steps, Prompt: "The plan has " + risky + ", which can be dangerous. Run it?",
			Options: []Option{{ID: "confirm", Label: "Run"}, {ID: "cancel", Label: "Cancel"}}}
	}
	return nil
}

func everyText(sec int) string {
	switch {
	case sec%3600 == 0 && sec >= 3600:
		if sec == 3600 {
			return "hour"
		}
		return itoa(sec/3600) + " hours"
	case sec == 60:
		return "minute"
	}
	return itoa(sec/60) + " minutes"
}
