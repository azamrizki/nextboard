package orch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The lead (docs/API-orchestration.md §3.9): the orchestrator as a person, the
// owner's assistant and the agent who leads the others. It talks with the owner
// and hands work out at once. Only work that can destroy something waits as a
// proposal until the owner agrees.

const (
	proposalTTL = 30 * time.Minute
	// as settings.Lead allows; a card's data is not checked by the board
	maxPersonality = 800
	maxDanger      = 300
)

var (
	// A card keeps the lead's name until it is given one of its own.
	defaultCardTitle = regexp.MustCompile(`^Orchestrator( \d+)?$`)
	// Answers to a proposal, in the languages the owner writes.
	agreeWords   = regexp.MustCompile(`(?i)^(?:(?:ya|iya|yes|yep|yup|ok|oke|okay|okey|sip|siap|gas|lanjut|lanjutkan|setuju|jalankan|kerjakan|boleh|go|mantap|aja|saja|deh|dong|please)[ ,.!]*)+$`)
	declineWords = regexp.MustCompile(`(?i)^(?:(?:tidak|nggak|ngga|gak|enggak|batal|batalkan|jangan|stop|nanti|nope|no|cancel|dulu|aja|saja|deh)[ ,.!]*)+$`)
)

type leadProfile struct {
	name, personality string
	talkFirst         bool
}

// leadOf is who speaks in this scope: the hub's lead, or a card with a name and
// a personality of its own.
func (s *Service) leadOf(sc *scope) leadProfile {
	l := s.hub.Settings().Lead
	p := leadProfile{name: l.Name, personality: l.Personality, talkFirst: l.TalkFirst}
	if sc.card != nil {
		var d OrchestratorData
		_ = json.Unmarshal(sc.card.Data, &d)
		if t := strings.TrimSpace(d.Title); t != "" && !defaultCardTitle.MatchString(t) {
			p.name = t
		}
		if t := strings.TrimSpace(d.Personality); t != "" {
			p.personality = clipLen(t, maxPersonality)
		}
	}
	return p
}

// leadPrompt opens the planner's prompt. The owner writes the personality; the
// hub's rules that follow it still hold.
func leadPrompt(p leadProfile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, the lead agent of this project's team on NextBoard, a board of AI coding agent consoles (Claude Code, Codex, …).\n", p.name)
	b.WriteString("You are the owner's assistant and the one who leads the other agents: you talk with the owner, think the work through with them, then hand it out and keep track of it. You write no code yourself; the consoles do that.\n")
	b.WriteString("Talk like a capable colleague: warm, direct, no filler, in the language the owner writes in. You may have opinions and say when you would do something differently. The owner is \"the user\" below.\n")
	if p.personality != "" {
		fmt.Fprintf(&b, "Your personality, as the owner describes it:\n%s\n", p.personality)
	}
	if p.talkFirst {
		b.WriteString(`Talk first:
- A greeting, a question, an idea or anything not yet decided gets an answer in "reply" and no steps.
- When the owner wants work done and something important is unclear, ask in "reply" (one or two questions, only what you cannot decide yourself) and return no steps yet.
- When the work is clear, say in "reply" in a line or two who gets what, and put it in "steps". Steps run as soon as you return them, so do not ask whether to go ahead. Say who gets what, not that it is done: the hub may still hold a step that looks dangerous.
- Only when a step can destroy or lose something (deleting files or data, dropping or resetting a database, overwriting git history, changing production, exposing a secret), also set "danger" to what could be lost, in one short sentence in the owner's language. The hub then holds all the steps until the owner agrees, so say in "reply" that you are waiting for their OK.
- "pendingPlan" is a dangerous plan waiting for the owner's answer. If they change something, return the whole new plan in "steps", with "danger" if it still applies. If they only ask about it, answer and return no steps: it stays as it is.
`)
	} else {
		b.WriteString("Steps run as soon as you return them, so carry out what the owner asks right away.\n")
	}
	return b.String()
}

// addressedToLead: "@Nara what do you think" is for the lead herself, unless a
// console has that name.
func addressedToLead(sc *scope, lead leadProfile, text string) (string, bool) {
	if !strings.HasPrefix(text, "@") {
		return text, false
	}
	name, rest := text[1:], ""
	if i := strings.IndexAny(name, " ,:\n\t"); i >= 0 {
		name, rest = name[:i], strings.TrimLeft(name[i+1:], " ,:\n\t")
	}
	n := Norm(name)
	if n != Norm(lead.name) && n != "orchestrator" && n != "lead" {
		return text, false
	}
	if len(sc.resolve([]string{name}).targets) > 0 {
		return text, false
	}
	if rest == "" {
		return text, true
	}
	return rest, true
}

// runPlan carries out the lead's plan unless it has to wait: a plan that can destroy
// something goes to the owner first, with what is at stake. Kills and the like keep
// their own question.
func (s *Service) runPlan(c *execCtx, sc *scope, steps []Step, danger string) {
	cf := s.policy(c, sc, steps)
	if s.leadOf(sc).talkFirst && (cf == nil || cf.Kind == ConfRisky) {
		danger = strings.TrimSpace(danger)
		switch {
		case danger != "" && len(steps) > 0:
			cf = proposalOf(sc, steps, "Needs your OK: "+clipLen(danger, maxDanger))
		case cf != nil:
			cf = proposalOf(sc, steps, cf.Prompt)
		}
	}
	if cf != nil {
		s.addConfirm(c, cf)
		return
	}
	// New work replaces a plan the owner was still looking at, or it could run as well.
	if handsOutWork(steps) {
		if old, ok := s.Confirms.Pending(c.threadID, s.Now()); ok && old.Kind == ConfProposal {
			if d, err := s.Confirms.Decide(old.ID, "cancel", "", c.by, s.Now()); err == nil {
				s.publishConfirm(d)
			}
		}
	}
	s.execute(c, steps)
}

// handsOutWork: steps that give the team something to do. Tidying the board does not.
func handsOutWork(steps []Step) bool {
	for _, st := range steps {
		switch st.Op {
		case "send", "spawn", "group.create", "handoff", "forward", "loop.create", "git":
			return true
		}
	}
	return false
}

func proposalOf(sc *scope, steps []Step, prompt string) *Confirmation {
	return &Confirmation{Kind: ConfProposal, Prompt: prompt, Plan: describePlan(sc, steps), Steps: steps,
		Options: []Option{{ID: "confirm", Label: "Go ahead"}, {ID: "cancel", Label: "Not now"}}}
}

// describePlan says in words what the steps will do.
func describePlan(sc *scope, steps []Step) []PlanNote {
	var out []PlanNote
	line := func(format string, a ...any) { out = append(out, PlanNote{Text: fmt.Sprintf(format, a...)}) }
	for _, st := range steps {
		who := sc.handles(st.Targets)
		switch st.Op {
		case "send":
			for _, t := range st.Targets {
				out = append(out, PlanNote{Handle: sc.handle(t), Text: st.Text})
			}
		case "spawn":
			n := max(st.Count, 1)
			what := strings.TrimSpace(st.Role + " " + st.Agent)
			if what == "" {
				what = "new"
			}
			if n == 1 {
				line("Start a %s console", what)
			} else {
				line("Start %d %s consoles", n, what)
			}
		case "group.create":
			line("Make a group %s", st.Name)
		case "handoff":
			line("Hand the work of %s over to %s", who, st.Agent)
		case "forward":
			line("Pass replies on to %s", who)
		case "loop.create":
			line("Start a loop: %s", st.Text)
		case "git":
			line("Git: %s %s", st.Action, st.Message)
		case "kill":
			line("Stop %s", who)
		case "restart":
			line("Restart %s", who)
		case "pause":
			line("Pause %s", who)
		case "resume":
			line("Resume %s", who)
		case "node.add":
			line("Add a %s tile", st.Kind)
		case "node.update":
			line("Change %s", st.Tile)
		case "node.delete":
			line("Remove %s", strings.Join(st.Tiles, ", "))
		case "connect":
			line("Connect %s to %s", st.Source, who)
		case "disconnect":
			line("Disconnect %s from %s", st.Source, who)
		case "task.add":
			line("Add task %s", st.Task.Title)
		case "task.update":
			line("Update task %s", st.Task.Card)
		case "task.delete":
			line("Remove task %s", st.Task.Card)
		case "ui", "reply", "ask":
			// nothing the owner has to agree on
		default:
			line("%s", st.Op)
		}
	}
	return out
}

// ReplyRef is the earlier message an order answers.
type ReplyRef struct {
	ID      string `json:"id"`
	Handle  string `json:"handle,omitempty"`
	Excerpt string `json:"excerpt"`
}

// replyRef looks up the message being answered ("" or too old = none).
func (s *Service) replyRef(threadID, id string) *ReplyRef {
	if id == "" {
		return nil
	}
	for _, m := range s.Threads.Last(threadID, 200) {
		if m.ID != id {
			continue
		}
		r := &ReplyRef{ID: m.ID, Handle: m.Handle}
		text := m.Text
		switch {
		case m.Report != nil && m.Report.Excerpt != "":
			text = m.Report.Excerpt
		case len(m.Plan) > 0:
			text = m.Plan[0].Text
			if r.Handle == "" {
				r.Handle = m.Plan[0].Handle
			}
		}
		r.Excerpt = clipText(strings.Join(strings.Fields(text), " "), 300)
		return r
	}
	return nil
}

// withReply adds what is being answered to the text that goes on; the thread
// keeps the owner's words and shows the quote on its own.
func withReply(text string, r *ReplyRef) string {
	if r == nil || text == "" {
		return text
	}
	return text + replyNote(r)
}

// withoutReply is the text as the thread shows it: the quote is there already.
func withoutReply(text string, r *ReplyRef) string {
	if r == nil {
		return text
	}
	return strings.TrimSuffix(text, replyNote(r))
}

func replyNote(r *ReplyRef) string {
	if r.Handle != "" {
		return fmt.Sprintf("\n\n[This answers what @%s said: %q]", r.Handle, r.Excerpt)
	}
	return fmt.Sprintf("\n\n[This answers an earlier message: %q]", r.Excerpt)
}
