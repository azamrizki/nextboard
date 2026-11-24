package nextcode

// Moving a chat to another account (docs/API-agents.md §30, "One NextCode"): the owner picks a
// model of another login or API provider, so another CLI, often another engine, takes the chat
// over. No CLI can continue another's conversation, so the next message starts a new one there,
// and carries the chat so far, written out from the items the driver keeps. The chat itself stays
// as it was, with a notice where the conversation moved.

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Hand-over limits: what the next CLI is given of the chat so far, in bytes.
const (
	handOverMax   = 48 << 10 // all of it: the latest parts, the earlier left out
	handOverText  = 6 << 10  // one message or reply (head and tail)
	handOverInput = 300      // a tool call's input, in a line
	handOverOut   = 800      // a tool call's output (its tail)
)

// HandOver moves the chat to engine: the running CLI ends (a turn in progress is stopped), the
// next message starts a new conversation on the console's launch as it is then (another login,
// provider or model), and that message brings the chat so far. note is what the chat says there,
// told whether there is a chat to bring ("" = nothing). The chat, its history file and the
// permission mode stay.
func (d *Driver) HandOver(engine string, note func(carry bool) string) {
	if engine == "" {
		engine = EngineClaude
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	p := d.proc
	if p != nil {
		d.expecting = true
	}
	carry := false
	for _, it := range d.items {
		carry = carry || it.Kind == KindUser
	}
	d.cfg.Engine = engine
	d.started, d.handOver = false, carry
	d.status.Engine, d.status.Conversation, d.status.Model = engine, "", ""
	d.status.CostUSD, d.status.Usage, d.status.RateLimit, d.status.Commands = 0, nil, nil, nil
	d.persistLocked(record{Meta: &meta{Engine: engine, New: true, HandOver: carry}})
	if text := callNote(note, carry); text != "" {
		it := Item{ID: newID("n"), Kind: KindNotice, At: time.Now().UTC(), Text: text}
		d.upsertLocked(it)
		d.persistLocked(record{Item: &it})
		d.broadcastLocked(Frame{T: FrameItem, Item: &it})
	}
	d.statusChangedLocked()
	d.unlock()
	if p != nil {
		p.Stop()
	}
}

func callNote(note func(bool) string, carry bool) string {
	if note == nil {
		return ""
	}
	return note(carry)
}

// HandingOver reports whether the next message brings the chat so far to a new conversation.
func (d *Driver) HandingOver() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.handOver
}

// handOverPrompt is the chat so far, for the CLI that takes it over, put before the owner's
// message. items are the chat before that message.
func handOverPrompt(items []Item) string {
	var parts []string
	for _, it := range items {
		if it.Parent != "" {
			continue // a sub-agent's work: its result is in its tool call
		}
		var s string
		switch it.Kind {
		case KindUser:
			s = "User:\n" + clipMiddle(it.Text, handOverText)
		case KindText:
			s = "Assistant:\n" + clipMiddle(it.Text, handOverText)
		case KindError:
			s = "(Error: " + clipMiddle(it.Text, handOverInput) + ")"
		case KindAnswer:
			s = "User answered the assistant's questions:\n" + clipMiddle(it.Text, handOverText)
		case KindTool:
			s = toolLine(it.Tool)
		}
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	// the latest parts that fit
	from, size := len(parts), 0
	for from > 0 && size+len(parts[from-1])+2 <= handOverMax {
		from--
		size += len(parts[from]) + 2
	}
	var b strings.Builder
	b.WriteString("<conversation_so_far>\n")
	b.WriteString("This conversation began earlier in NextCode, with another model. You are taking it over: what the user and the assistant said and did so far is below, long parts shortened. Continue from it, and do not do again what is already done; read the files again before you change them.\n\n")
	if from > 0 {
		b.WriteString("(The earliest part is left out.)\n\n")
	}
	b.WriteString(strings.Join(parts[from:], "\n\n"))
	b.WriteString("\n</conversation_so_far>\n\n")
	return b.String()
}

// toolLine is a tool call in a line or two: its name, what it acted on, how it ended, and the
// tail of what it gave.
func toolLine(t *Tool) string {
	if t == nil {
		return ""
	}
	in := toolSummary(t.Input)
	if in == "" && len(t.Input) > 2 {
		in = string(t.Input)
	}
	line := "Assistant used " + t.Name
	if in != "" {
		line += ": " + clipMiddle(strings.TrimSpace(in), handOverInput)
	}
	if t.Status != "" && t.Status != ToolDone {
		line += " (" + t.Status + ")"
	}
	if out := strings.TrimSpace(t.Output); out != "" {
		line += "\n  → " + strings.ReplaceAll(clipTail(out, handOverOut), "\n", "\n    ")
	}
	return line
}

// clipMiddle keeps the head and the tail of s within n bytes.
func clipMiddle(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	half := n / 2
	return validHead(s[:half]) + "\n[…]\n" + validTail(s[len(s)-half:])
}

// clipTail keeps the end of s within n bytes.
func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "[…] " + validTail(s[len(s)-n:])
}

func validHead(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func validTail(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[1:]
	}
	return s
}

// Note puts what the owner typed and NextCode's answer in the chat, with no turn of the CLI: a
// command of NextCode's own (/effort, /model, /mode), which it carries out itself on every
// engine. answer "" puts the command only.
func (d *Driver) Note(command, answer string) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrClosed
	}
	now := time.Now().UTC()
	for _, it := range []Item{{ID: newID("u"), Kind: KindUser, At: now, Text: command}, {ID: newID("n"), Kind: KindNotice, At: now, Text: answer}} {
		if it.Text == "" {
			continue
		}
		d.upsertLocked(it)
		d.persistLocked(record{Item: &it})
		d.broadcastLocked(Frame{T: FrameItem, Item: &it})
	}
	d.unlock()
	return nil
}
