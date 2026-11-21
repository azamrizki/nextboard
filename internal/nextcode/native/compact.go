package native

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
)

// Compaction: when the conversation fills the model's context, a summary of it takes its
// place. The summary starts a new message list, a fresh prefix rather than an edit of the old
// one, and the old list stays in the file under Archived.

// errNothing: there is nothing before the owner's latest message to summarize.
var errNothing = errors.New("there is nothing to compact yet")

// Context windows when --context-window is not given.
const (
	claudeWindow  = 200_000
	defaultWindow = 128_000
)

// window is the model's context window in tokens.
func (a *agent) window() int {
	if a.cfg.contextWindow > 0 {
		return a.cfg.contextWindow
	}
	if strings.HasPrefix(strings.ToLower(a.currentModel()), "claude") {
		return claudeWindow
	}
	return defaultWindow
}

// full reports whether the last call's context passed 80% of the context window.
func (a *agent) full() bool {
	return a.conv.Context > 0 && a.conv.Context*5 > a.window()*4
}

// compact summarizes the conversation and continues it from the summary. The owner's message
// that waits for an answer, when the conversation ends with one, is kept as it is after the
// summary. inTurn: a turn goes on after it (a summary with no message after it says to carry on).
func (a *agent) compact(t *turn, inTurn bool) error {
	msgs := a.conv.Messages
	var pending []llm.Message
	if n := len(msgs); n > 0 && ownerMessage(msgs[n-1]) {
		pending, msgs = msgs[n-1:], msgs[:n-1]
	}
	if len(msgs) == 0 {
		return errNothing
	}
	a.out.status("compacting")
	defer a.out.status(nil)
	summary, err := a.summarize(t, msgs)
	if err != nil {
		return err
	}
	head := "This conversation continues from an earlier one; its summary:\n" + summary
	if inTurn && len(pending) == 0 {
		head += "\n\nCarry on with the work from where it stopped."
	}
	today := time.Now().Format(time.DateOnly)
	notes := []string{"Today's date is " + today + "."}
	mode, _ := a.permissions()
	if mode != modeDefault {
		notes = append(notes, modeNote("", mode))
	}
	a.conv.Date, a.conv.Mode = today, mode
	first := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: head}, reminderBlock(notes)}}
	a.conv.Archived = append(a.conv.Archived, a.conv.Messages)
	a.conv.Messages = append([]llm.Message{first}, slices.Clone(pending)...)
	a.conv.Compactions++
	a.conv.Context = 0
	a.save()
	a.out.boundary()
	return nil
}

// compactTurn is the owner's /compact.
func (a *agent) compactTurn(ctx context.Context) {
	t := &turn{ctx: ctx}
	err := a.compact(t, false)
	switch {
	case ctx.Err() != nil:
		t.stopped = true
	case errors.Is(err, errNothing):
		t.text = "There is nothing to compact yet."
		a.note(t.text)
	case err != nil:
		t.err = "Compacting failed: " + errorText(err)
	default:
		t.text = "Compacted the conversation."
	}
	a.finish(t)
}

// ownerMessage reports whether a message is one of the owner's (not tool results).
func ownerMessage(m llm.Message) bool {
	if m.Role != llm.RoleUser {
		return false
	}
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolResult {
			return false
		}
	}
	return true
}

// summarize asks the model for a summary of msgs, with no tools: the messages go as one
// transcript, which every wire takes whatever the conversation holds. A transcript too long for
// the model is cut shorter and tried again.
func (a *agent) summarize(t *turn, msgs []llm.Message) (string, error) {
	model := a.currentModel()
	budget := a.window() * 2 // characters: about half the window in tokens
	for range 3 {
		text := summaryAsk + "\n\n<conversation>\n" + transcript(msgs, budget) + "\n</conversation>"
		req := llm.Request{Model: model, System: summarySystem, Effort: a.cfg.effort, MaxTokens: a.cfg.maxTokens,
			Messages: []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: text}}}}}
		resp, err := a.client.Stream(t.ctx, req, a.retries)
		if err == nil && resp == nil {
			err = errors.New("the provider gave no answer")
		}
		if err != nil {
			if overflow(err) && t.ctx.Err() == nil {
				budget /= 3
				continue
			}
			return "", err
		}
		a.spent(t, model, resp.Usage)
		s := strings.TrimSpace(textOf(resp.Blocks))
		if s == "" {
			return "", errors.New("the summary came back empty")
		}
		return s, nil
	}
	return "", errors.New("even a short part of the conversation is too long for the model")
}

// retries passes a call's retries on to the driver.
func (a *agent) retries(e llm.Event) {
	if e.Kind == llm.EvRetry {
		a.out.retry(e.Attempt, e.MaxAttempts)
	}
}

const summarySystem = `You write the summary of a conversation between a coding agent and its owner. The agent will carry on the work from your summary alone: the conversation itself will be gone. Write it to the agent, as concise Markdown, in the language the owner writes in.`

const summaryAsk = `Summarize the conversation below so that the work can go on from the summary alone. Cover:
1. The owner's requests and goals, in their own words where the wording matters.
2. The decisions made, and the constraints and preferences the owner stated.
3. The files read, created or changed, with their paths and what changed in them.
4. The commands that mattered and what came of them; the errors met and how they were solved.
5. The current state: what is done, and what was in progress when the conversation stopped, with the latest results it depends on.
6. The next steps, and any question still open.
Leave out what no longer matters. Answer with the summary only.`

// transcript is the conversation as text, within budget characters: tool outputs are cut
// shorter and shorter, and at last the oldest messages are left out (the first one stays).
func transcript(msgs []llm.Message, budget int) string {
	names := map[string]string{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse {
				names[b.ID] = b.Name
			}
		}
	}
	var parts []string
	for _, limit := range []int{4000, 1000, 250} {
		parts = parts[:0]
		size := 0
		for _, m := range msgs {
			if p := renderMessage(m, names, limit); p != "" {
				parts = append(parts, p)
				size += len(p) + 2
			}
		}
		if size <= budget {
			return strings.Join(parts, "\n\n")
		}
	}
	return keepEnds(parts, budget)
}

// renderMessage is one message of the transcript; limit bounds each tool input and output.
func renderMessage(m llm.Message, names map[string]string, limit int) string {
	var b strings.Builder
	add := func(s string) {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(s)
	}
	for _, blk := range m.Blocks {
		switch blk.Type {
		case llm.BlockText:
			who := "[The owner]"
			if m.Role == llm.RoleAssistant {
				who = "[You]"
			}
			add(who + "\n" + clipMiddle(blk.Text, 4*limit))
		case llm.BlockToolUse:
			add(fmt.Sprintf("[You called %s] %s", blk.Name, clipMiddle(string(blk.Input), limit)))
		case llm.BlockToolResult:
			name := names[blk.ToolUseID]
			if name == "" {
				name = "a tool"
			}
			label := "[Result of " + name + "]"
			if blk.IsError {
				label = "[" + name + " failed]"
			}
			add(label + "\n" + clipMiddle(blk.Text, limit))
		}
	}
	return b.String()
}

// keepEnds keeps the first part and as many of the last ones as fit in budget.
func keepEnds(parts []string, budget int) string {
	if len(parts) == 0 {
		return ""
	}
	first := clipMiddle(parts[0], budget/4)
	room := budget - len(first)
	var tail []string
	for i := len(parts) - 1; i > 0; i-- {
		if len(parts[i])+2 > room {
			if len(tail) == 0 && room > 200 {
				tail = append(tail, clipMiddle(parts[i], room-100))
			}
			break
		}
		room -= len(parts[i]) + 2
		tail = append(tail, parts[i])
	}
	slices.Reverse(tail)
	out := first
	if left := len(parts) - 1 - len(tail); left > 0 {
		out += fmt.Sprintf("\n\n[… %d earlier messages are left out …]", left)
	}
	if len(tail) > 0 {
		out += "\n\n" + strings.Join(tail, "\n\n")
	}
	return out
}
