package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
	"github.com/azamrizki/nextboard/internal/nextcode/native/tools"
)

const (
	// maxResult is the longest tool result the model gets, in characters (head and tail kept).
	maxResult = 30_000

	stoppedBefore = "Stopped by the owner before it ran."
	stoppedWhile  = "Stopped by the owner while it ran."
)

// turn is one turn of the conversation: from the owner's message to an answer with no tool
// call in it, or to the owner's stop.
type turn struct {
	ctx     context.Context
	usage   llm.Usage // of all its calls
	text    string    // the agent's last text
	stopped bool
	err     string // why it failed ("" = it did not)
}

// toolCall is a tool_use block of an answer, and whether its input was a JSON object.
type toolCall struct {
	use llm.Block
	ok  bool
}

// runTurn runs one turn for the owner's messages (several that waited go as one message).
func (a *agent) runTurn(ctx context.Context, texts []string) {
	t := &turn{ctx: ctx}
	var blocks []llm.Block
	if notes := a.reminders(); len(notes) > 0 {
		blocks = append(blocks, reminderBlock(notes))
	}
	for _, s := range texts {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: s})
	}
	a.appendMessage(llm.Message{Role: llm.RoleUser, Blocks: blocks})
	a.loop(t)
	a.finish(t)
}

// loop calls the model and runs the tools it asks for until it answers with no tool call.
func (a *agent) loop(t *turn) {
	retried, compactFailed := false, false
	for {
		if t.ctx.Err() != nil {
			t.stopped = true
			return
		}
		if !compactFailed && a.full() {
			if err := a.compact(t, true); err != nil && !errors.Is(err, errNothing) {
				if t.ctx.Err() != nil {
					t.stopped = true
					return
				}
				compactFailed = true
				a.out.logf("compacting failed: %s", errorText(err))
			}
		}
		calls, stop, err := a.call(t)
		if err != nil {
			if t.ctx.Err() != nil {
				t.stopped = true
				return
			}
			if overflow(err) && !retried {
				retried = true
				if cerr := a.compact(t, true); cerr != nil {
					if t.ctx.Err() != nil {
						t.stopped = true
						return
					}
					t.err = "The conversation is too long for the model, and compacting it failed: " + errorText(cerr)
					return
				}
				continue
			}
			t.err = errorText(err)
			return
		}
		retried = false
		if stop == llm.StopRefusal {
			a.closeCalls(calls, "Not run: the model declined to answer.")
			t.err = "The model declined to answer."
			return
		}
		if len(calls) == 0 {
			if stop == llm.StopMax {
				a.note("The answer was cut off at the token limit.")
			}
			return
		}
		results, stopped := a.runTools(t, calls)
		if notes := a.reminders(); len(notes) > 0 {
			results = append(results, reminderBlock(notes))
		}
		a.appendMessage(llm.Message{Role: llm.RoleUser, Blocks: results})
		if stopped {
			t.stopped = true
			return
		}
	}
}

// finish ends the turn with its result line.
func (a *agent) finish(t *turn) {
	a.out.result(a.id, a.currentModel(), t.text, t.err, t.usage)
}

// call asks the model for its next answer, streaming it to the driver, and adds it to the
// conversation. It returns the answer's tool calls and why it stopped.
func (a *agent) call(t *turn) ([]toolCall, string, error) {
	model := a.currentModel()
	s := newStreamer(a.out, a.nextMessageID())
	req := llm.Request{Model: model, System: a.conv.System, Messages: wire(a.conv.Messages), Tools: a.defs,
		Effort: a.cfg.effort, MaxTokens: a.cfg.maxTokens}
	resp, err := a.client.Stream(t.ctx, req, s.on)
	if err == nil && resp == nil {
		err = errors.New("the provider gave no answer")
	}
	if err != nil {
		s.abort()
		a.keepPartial(t, s, model)
		return nil, "", err
	}
	a.spent(t, model, resp.Usage)
	a.conv.Model = model
	a.conv.Context = resp.Usage.Input + resp.Usage.CacheRead + resp.Usage.CacheWrite
	blocks, calls := a.sanitize(resp.Blocks)
	s.finish(blocks)
	if len(blocks) == 0 {
		a.save() // an empty answer is not kept: no provider takes one back
		return nil, resp.StopReason, nil
	}
	a.out.assistant(s.id, model, content(blocks))
	a.appendMessage(llm.Message{Role: llm.RoleAssistant, Blocks: blocks})
	if text := textOf(blocks); text != "" {
		t.text = text
	}
	return calls, resp.StopReason, nil
}

func (a *agent) nextMessageID() string {
	a.msgN++
	return fmt.Sprintf("msg_%s_%d", a.msgTag, a.msgN)
}

// keepPartial shows what streamed of an answer that did not come whole. When the owner stopped
// it, the conversation keeps its text (a tool call cut short cannot be kept).
func (a *agent) keepPartial(t *turn, s *streamer, model string) {
	shown, kept := s.partial()
	if len(shown) > 0 {
		a.out.assistant(s.id, model, shown)
	}
	if t.ctx.Err() == nil {
		return
	}
	if len(kept) > 0 {
		a.appendMessage(llm.Message{Role: llm.RoleAssistant, Blocks: kept})
		t.text = textOf(kept)
	}
	a.notes = append(a.notes, "The owner stopped your last answer before it was finished.")
}

// sanitize readies an answer's blocks for the conversation file, in the same order: a tool
// call's input that is not a JSON object becomes {} (no provider takes it back), and a tool
// call gets an id of its own when it has none or one the conversation already has.
func (a *agent) sanitize(in []llm.Block) ([]llm.Block, []toolCall) {
	out := make([]llm.Block, 0, len(in))
	var calls []toolCall
	for _, b := range in {
		if len(b.Raw) > 0 && !json.Valid(b.Raw) {
			b.Raw = nil
		}
		if b.Type == llm.BlockToolUse {
			ok := isObject(b.Input)
			if !ok {
				b.Input = json.RawMessage(`{}`)
			}
			if b.ID == "" || a.ids[b.ID] {
				b.ID = "toolu_nc" + randomHex(10)
			}
			a.ids[b.ID] = true
			calls = append(calls, toolCall{use: b, ok: ok})
		}
		out = append(out, b)
	}
	return out, calls
}

// spent counts a call's tokens in the turn and in the hub's ledger.
func (a *agent) spent(t *turn, model string, u llm.Usage) {
	t.usage.Input += u.Input
	t.usage.Output += u.Output
	t.usage.CacheRead += u.CacheRead
	t.usage.CacheWrite += u.CacheWrite
	if a.ledger == nil {
		return
	}
	if err := a.ledger.add(model, u); err != nil {
		a.out.logf("cannot write the usage ledger: %v", err)
	}
}

// note shows the owner a short text of the agent's own, outside the conversation.
func (a *agent) note(text string) {
	a.out.assistant(a.nextMessageID(), a.currentModel(), []any{map[string]any{"type": "text", "text": text}})
}

// ---- tools

// runTools runs an answer's tool calls in order, each once its permission allows it. It
// returns their results, one for every call, and whether the turn stops.
func (a *agent) runTools(t *turn, calls []toolCall) ([]llm.Block, bool) {
	results := make([]llm.Block, 0, len(calls))
	for i, c := range calls {
		if t.ctx.Err() != nil {
			return append(results, stoppedResults(calls[i:])...), true
		}
		res, stop := a.useTool(t, c)
		results = append(results, res)
		if stop {
			return append(results, stoppedResults(calls[i+1:])...), true
		}
	}
	return results, false
}

// useTool decides one call's permission, asking the owner when the mode says so, and runs it.
func (a *agent) useTool(t *turn, c toolCall) (llm.Block, bool) {
	u := c.use
	switch u.Name {
	case toolAsk:
		return a.askOwner(t, c)
	case toolPlan:
		return a.exitPlan(t, c)
	}
	tool := a.tools[u.Name]
	switch {
	case tool == nil:
		return a.failed(u, fmt.Sprintf("There is no tool named %s.", u.Name)), false
	case !c.ok:
		return a.failed(u, fmt.Sprintf("The input of %s was not a JSON object.", u.Name)), false
	}
	input := u.Input
	v := a.decide(tool.Kind(), u.Name, input)
	switch {
	case v.deny != "":
		return a.failed(u, v.deny), false
	case v.ask != nil:
		ans, ok := a.ask(t.ctx, u.Name, u.ID, input, v.ask)
		if !ok {
			return toolResult(u.ID, stoppedBefore, true), true
		}
		if !ans.allow {
			return a.failed(u, saying("The owner denied this", ans.message)), ans.interrupt
		}
		if a.apply(ans.perms) {
			a.sendInit()
		}
		if ans.input != nil {
			input = ans.input
		}
	}
	return a.execute(t, tool, u, input)
}

// execute runs an allowed call. A call the owner stopped while it ran stops the turn.
func (a *agent) execute(t *turn, tool tools.Tool, u llm.Block, input json.RawMessage) (llm.Block, bool) {
	res := runTool(t.ctx, tool, a.env, input)
	if t.ctx.Err() != nil {
		text := stoppedWhile
		if out := strings.TrimSpace(res.Output); out != "" {
			text += "\n" + out
		}
		return toolResult(u.ID, clipMiddle(text, maxResult), true), true
	}
	return a.result(u, res.Output, res.IsError), false
}

// runTool runs a tool; a tool that panics fails its call, not the agent.
func runTool(ctx context.Context, tool tools.Tool, env *tools.Env, input json.RawMessage) (res tools.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = tools.Result{Output: fmt.Sprintf("The tool failed: %v", r), IsError: true}
		}
	}()
	return tool.Run(ctx, env, input)
}

// askOwner asks the owner the agent's questions (AskUserQuestion).
func (a *agent) askOwner(t *turn, c toolCall) (llm.Block, bool) {
	u := c.use
	if !c.ok || !validQuestions(u.Input) {
		return a.failed(u, "AskUserQuestion needs at least one question, each with its options."), false
	}
	ans, ok := a.ask(t.ctx, toolAsk, u.ID, u.Input, nil)
	if !ok {
		return toolResult(u.ID, stoppedBefore, true), true
	}
	if !ans.allow {
		return a.failed(u, saying("The owner did not answer", ans.message)), ans.interrupt
	}
	if a.apply(ans.perms) {
		a.sendInit()
	}
	return a.result(u, answersText(u.Input, ans.input), false), false
}

// exitPlan asks the owner to approve the agent's plan (ExitPlanMode). Approved, plan mode ends.
func (a *agent) exitPlan(t *turn, c toolCall) (llm.Block, bool) {
	u := c.use
	if mode, _ := a.permissions(); mode != modePlan {
		return a.failed(u, "You are not in plan mode."), false
	}
	if !c.ok {
		return a.failed(u, "The input of ExitPlanMode was not a JSON object."), false
	}
	ans, ok := a.ask(t.ctx, toolPlan, u.ID, u.Input, nil)
	if !ok {
		return toolResult(u.ID, stoppedBefore, true), true
	}
	if !ans.allow {
		return a.failed(u, saying("The owner wants you to keep planning", ans.message)), ans.interrupt
	}
	a.setMode(modeDefault)
	a.conv.Mode = modeDefault // the result tells the model
	a.apply(ans.perms)
	a.sendInit()
	return a.result(u, "The owner approved the plan. Carry it out now.", false), false
}

// result is a call's result for the model, shown to the driver too. The key is taken out of
// it: a tool cannot have it in its environment, but a file it read might.
func (a *agent) result(u llm.Block, text string, isError bool) llm.Block {
	text = clipMiddle(redact(text, a.cfg.secret), maxResult)
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	a.out.toolResult(u.ID, text, isError)
	return toolResult(u.ID, text, isError)
}

func (a *agent) failed(u llm.Block, text string) llm.Block { return a.result(u, text, true) }

// closeCalls gives every call of an answer that will not run its result.
func (a *agent) closeCalls(calls []toolCall, text string) {
	if len(calls) == 0 {
		return
	}
	results := make([]llm.Block, 0, len(calls))
	for _, c := range calls {
		results = append(results, a.failed(c.use, text))
	}
	a.appendMessage(llm.Message{Role: llm.RoleUser, Blocks: results})
}

// stoppedResults are the results of calls the turn stopped before. The driver is not told: a
// tool whose result never came shows as stopped.
func stoppedResults(calls []toolCall) []llm.Block {
	out := make([]llm.Block, 0, len(calls))
	for _, c := range calls {
		out = append(out, toolResult(c.use.ID, stoppedBefore, true))
	}
	return out
}

func toolResult(id, text string, isError bool) llm.Block {
	return llm.Block{Type: llm.BlockToolResult, ToolUseID: id, Text: text, IsError: isError}
}

// saying is a refusal with the owner's message, when they gave one.
func saying(prefix, msg string) string {
	if msg = strings.TrimSpace(msg); msg == "" {
		return prefix + "."
	}
	return prefix + ": " + msg
}

// textOf is the text of an answer's text blocks.
func textOf(blocks []llm.Block) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == llm.BlockText && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// errorText is a failed call's error as the owner reads it.
func errorText(err error) string {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		if ae.Message != "" {
			return ae.Message
		}
		if ae.Status != 0 {
			return fmt.Sprintf("The provider answered with status %d.", ae.Status)
		}
		return "The provider could not be reached."
	}
	return err.Error()
}

// overflow reports whether a call failed because the conversation is too long for the model.
func overflow(err error) bool {
	var ae *llm.APIError
	return errors.As(err, &ae) && ae.ContextOverflow
}

// clipMiddle keeps a text under limit characters, its head and its tail.
func clipMiddle(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = strings.ToValidUTF8(s, "�")
	n := utf8.RuneCountInString(s)
	if n <= limit {
		return s
	}
	r := []rune(s)
	keep := max(limit-64, limit/2)
	head := keep / 2
	tail := keep - head
	return string(r[:head]) + fmt.Sprintf("\n\n[… %d characters left out …]\n\n", n-keep) + string(r[n-tail:])
}
