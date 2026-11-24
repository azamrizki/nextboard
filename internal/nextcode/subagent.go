package nextcode

// Sub-agents (docs/API-agents.md §30, NextCode's multi-agent): NextCode's own "agent" tool hands
// tasks to other conversations that work side by side, on every engine. Each one is a driver of
// its own (a CLI of its own); its chat shows in the main one under a card of its own, and what
// it asks the owner is asked in the main chat, answered there and passed back to it.

import (
	"encoding/json"
	"strings"
	"time"
)

// PutItem adds an item the hub makes in the chat, or replaces the one with its id (a sub-agent's
// card as it goes).
func (d *Driver) PutItem(it Item) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	if it.At.IsZero() {
		it.At = time.Now().UTC()
	}
	d.upsertLocked(it)
	d.persistLocked(record{Item: &it})
	d.broadcastLocked(Frame{T: FrameItem, Item: &it})
	d.unlock()
}

// RunningTool is the newest tool call of the main conversation still running whose name holds
// name and whose input holds every one of want; "" when there is none.
func (d *Driver) RunningTool(name string, want ...string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.items) - 1; i >= 0; i-- {
		it := d.items[i]
		if it.Tool == nil || it.Tool.Status != ToolRunning || it.Parent != "" || !strings.Contains(it.Tool.Name, name) {
			continue
		}
		in := string(it.Tool.Input)
		ok := true
		for _, w := range want {
			// the input is JSON: compare with the text as JSON writes it
			b, _ := json.Marshal(w)
			ok = ok && (strings.Contains(in, w) || strings.Contains(in, strings.Trim(string(b), `"`)))
		}
		if ok {
			return it.ID
		}
	}
	return ""
}

// Attach shows child's chat in d's: its items under parent (ids with prefix, so two never meet),
// the task it was given as a prompt item, what it streams, and its requests, which the owner
// answers in d and which go back to child. stop ends it; the requests still open leave d.
func (d *Driver) Attach(child *Driver, parent, prefix string) (stop func()) {
	_, sub := child.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for f := range sub.C {
			d.relay(child, f, parent, prefix)
		}
	}()
	return func() {
		child.Unsubscribe(sub)
		<-done
		d.mu.Lock()
		for id := range d.routes {
			if strings.HasPrefix(id, prefix) {
				d.dropRouteLocked(id)
			}
		}
		d.unlock()
	}
}

func (d *Driver) relay(child *Driver, f Frame, parent, prefix string) {
	under := func(p string) string {
		if p == "" {
			return parent
		}
		return prefix + p
	}
	switch f.T {
	case FrameItem:
		it := *f.Item
		it.ID, it.Parent = prefix+it.ID, under(it.Parent)
		if it.Stream != "" {
			it.Stream = prefix + it.Stream
		}
		if it.Kind == KindUser {
			it.Kind = KindPrompt // the task it was given
		}
		if a := it.Answer; a != nil && a.ToolUseID != "" {
			b := *a
			b.ToolUseID = prefix + a.ToolUseID // its question's tool call, as this chat names it
			it.Answer = &b
		}
		d.PutItem(it)
	case FrameBlock, FrameDelta:
		g := f
		g.ID, g.Parent = prefix+f.ID, under(f.Parent)
		d.mu.Lock()
		if !d.closed {
			d.broadcastLocked(g)
		}
		d.mu.Unlock()
	case FrameRequest:
		r := *f.Request
		orig := r.ID
		r.ID, r.Parent = prefix+orig, under(r.Parent)
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return
		}
		if d.routes == nil {
			d.routes = map[string]func(Answer) error{}
		}
		d.routes[r.ID] = func(a Answer) error { return child.Answer(orig, a) }
		d.reqs = append(d.reqs, &r)
		d.broadcastLocked(Frame{T: FrameRequest, Request: &r})
		if on := d.cfg.On.NeedsYou; on != nil {
			kind, text := r.Kind, needsText(&r)
			d.later(func() { on(kind, text) })
		}
		d.unlock()
	case FrameResolved:
		d.mu.Lock()
		d.dropRouteLocked(prefix + f.ID)
		d.unlock()
	}
}

// dropRouteLocked takes a sub-agent's request out of the chat (answered, or gone with it).
func (d *Driver) dropRouteLocked(id string) {
	if _, ok := d.routes[id]; !ok {
		return
	}
	delete(d.routes, id)
	for i, r := range d.reqs {
		if r.ID == id {
			d.reqs = append(d.reqs[:i], d.reqs[i+1:]...)
			break
		}
	}
	d.broadcastLocked(Frame{T: FrameResolved, ID: id})
	if len(d.reqs) == 0 {
		d.later(d.cfg.On.Answered)
	}
}

// answerRouted answers a sub-agent's request shown in this chat; handled false for one of the
// CLI's own.
func (d *Driver) answerRouted(id string, a Answer) (handled bool, err error) {
	d.mu.Lock()
	route, ok := d.routes[id]
	if !ok {
		d.mu.Unlock()
		return false, nil
	}
	d.dropRouteLocked(id)
	d.unlock()
	return true, route(a)
}

// Final is the sub-agent's report: the text of its replies after the last thing it was told,
// "" when it gave none.
func (d *Driver) Final() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var parts []string
	for i := len(d.items) - 1; i >= 0; i-- {
		it := d.items[i]
		if it.Kind == KindUser {
			break
		}
		if it.Kind == KindText && it.Parent == "" && strings.TrimSpace(it.Text) != "" {
			parts = append([]string{it.Text}, parts...)
		}
	}
	return strings.Join(parts, "\n\n")
}

// LastError is the text of the sub-agent's last error item, if its last turn failed.
func (d *Driver) LastError() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.items) - 1; i >= 0; i-- {
		switch d.items[i].Kind {
		case KindError:
			return d.items[i].Text
		case KindUser, KindText:
			return ""
		}
	}
	return ""
}
