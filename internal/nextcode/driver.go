package nextcode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Limits.
const (
	// SnapshotItems is how many of the latest items a view gets when it connects.
	SnapshotItems = 300
	// MaxMessage is the longest message the owner can send.
	MaxMessage = 100_000
	maxInput   = 64 << 10 // a tool call's input kept in the chat
	maxOutput  = 32 << 10 // a tool call's output kept in the chat
	subBuffer  = 1024     // frames a slow view may lag behind before it is dropped
	// exitWait is how long a CLI asked to end (stdin closed) may take before it is stopped.
	exitWait = 10 * time.Second
)

// Errors.
var (
	ErrClosed     = errors.New("this NextCode console is closed")
	ErrNoRequest  = errors.New("no such request (it was answered or is gone)")
	ErrEmpty      = errors.New("the message is empty")
	ErrTooLong    = errors.New("the message is too long")
	ErrNotWorking = errors.New("the agent is not working")
	ErrBadMode    = errors.New("unknown permission mode")
)

// Config describes a console's driver.
type Config struct {
	// Engine is the agent's engine (EngineClaude, EngineCodex).
	Engine string
	// HistoryPath is the chat's file (JSON lines), kept across restarts. "" = memory only.
	HistoryPath string
	// Start starts the CLI; resume: continue its conversation. argv turns the agent's own
	// launch into the engine's command line.
	Start func(resume bool, argv func(base []string) []string) (*Proc, error)
	// IdleExit: the CLI is asked to end after this long with nothing to do; the next message
	// starts it again on the same conversation. 0 = never.
	IdleExit time.Duration
	// PermissionMode is the mode a CLI starts in ("" = the CLI's own).
	PermissionMode string
	On             Listener
}

// Sub is a view of the console.
type Sub struct {
	C    chan Frame
	gone bool
}

// Driver is one NextCode console.
type Driver struct {
	cfg Config
	eng engine

	wmu sync.Mutex // writes to the CLI's stdin

	mu        sync.Mutex
	items     []Item
	index     map[string]int
	reqs      []*Request
	routes    map[string]func(Answer) error // a sub-agent's requests shown here (subagent.go)
	status    Status
	proc      *Proc
	gen       int
	expecting bool // the running CLI was asked to end
	started   bool // the CLI has the conversation: the next CLI resumes it
	handOver  bool // the next message starts a new conversation with the chat so far (HandOver)
	denied    map[string]bool
	subs      map[*Sub]struct{}
	idle      *time.Timer
	hist      *os.File
	closed    bool
	calls     []func()
}

// New opens a console's driver; its chat comes back from HistoryPath.
func New(cfg Config) (*Driver, error) {
	if cfg.Engine == "" {
		cfg.Engine = "claude"
	}
	d := &Driver{cfg: cfg, eng: newEngine(cfg.Engine), index: map[string]int{}, denied: map[string]bool{}, subs: map[*Sub]struct{}{}}
	d.status = Status{Engine: cfg.Engine, Process: ProcStopped, PermissionMode: cfg.PermissionMode}
	if cfg.HistoryPath != "" {
		if err := d.load(); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// record is one line of the history file.
type record struct {
	Item *Item `json:"item,omitempty"`
	Meta *meta `json:"meta,omitempty"`
}

type meta struct {
	Started      bool   `json:"started,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	Mode         string `json:"mode,omitempty"`
	// New: the chat moved to another account (HandOver) on Engine; a new conversation starts
	// here, and with HandOver its first message brings the chat so far, until HandedOver.
	New        bool   `json:"new,omitempty"`
	Engine     string `json:"engine,omitempty"`
	HandOver   bool   `json:"handOver,omitempty"`
	HandedOver bool   `json:"handedOver,omitempty"`
}

func (d *Driver) load() error {
	b, err := os.ReadFile(d.cfg.HistoryPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range bytes.Split(b, []byte("\n")) {
		if len(l) == 0 {
			continue
		}
		var r record
		if json.Unmarshal(l, &r) != nil {
			continue // a line cut short by a crash
		}
		if r.Item != nil {
			d.upsertLocked(*r.Item)
		}
		if m := r.Meta; m != nil {
			if m.New {
				d.started, d.handOver, d.status.Conversation = false, m.HandOver, ""
			}
			if m.HandedOver {
				d.handOver = false
			}
			d.started = d.started || m.Started
			if m.Conversation != "" {
				d.status.Conversation = m.Conversation
			}
			if m.Mode != "" {
				d.status.PermissionMode = m.Mode
			}
		}
	}
	// what was running when the hub stopped did not finish here
	for i := range d.items {
		if t := d.items[i].Tool; t != nil && t.Status == ToolRunning {
			t.Status = ToolStopped
		}
	}
	if err := os.MkdirAll(filepath.Dir(d.cfg.HistoryPath), 0o700); err != nil {
		return err
	}
	d.hist, err = os.OpenFile(d.cfg.HistoryPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	return err
}

func (d *Driver) persistLocked(r record) {
	if d.hist == nil {
		return
	}
	b, _ := json.Marshal(r)
	if _, err := d.hist.Write(append(b, '\n')); err != nil {
		log.Printf("nextcode history: %v", err)
	}
}

// upsertLocked adds an item, or replaces the one with its id; it reports whether it was new.
func (d *Driver) upsertLocked(it Item) bool {
	if i, ok := d.index[it.ID]; ok {
		d.items[i] = it
		return false
	}
	d.index[it.ID] = len(d.items)
	d.items = append(d.items, it)
	return true
}

// Subscribe returns a view's snapshot and its subscription.
func (d *Driver) Subscribe() (Frame, *Sub) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sub := &Sub{C: make(chan Frame, subBuffer)}
	if d.closed {
		close(sub.C)
		sub.gone = true
		return d.snapshotLocked(), sub
	}
	d.subs[sub] = struct{}{}
	return d.snapshotLocked(), sub
}

// Unsubscribe ends a view's subscription.
func (d *Driver) Unsubscribe(sub *Sub) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropLocked(sub)
}

func (d *Driver) dropLocked(sub *Sub) {
	if _, ok := d.subs[sub]; ok {
		delete(d.subs, sub)
		if !sub.gone {
			sub.gone = true
			close(sub.C)
		}
	}
}

func (d *Driver) snapshotLocked() Frame {
	from := max(len(d.items)-SnapshotItems, 0)
	items := append([]Item{}, d.items[from:]...)
	reqs := make([]Request, len(d.reqs))
	for i, r := range d.reqs {
		reqs[i] = *r
	}
	st := d.statusLocked()
	return Frame{T: FrameSnapshot, Items: items, More: from > 0, Requests: reqs, Status: &st}
}

func (d *Driver) statusLocked() Status {
	st := d.status
	st.Commands = append([]string(nil), d.status.Commands...)
	return st
}

// broadcastLocked sends a frame to every view; a view too far behind is dropped (it reconnects
// and gets a snapshot).
func (d *Driver) broadcastLocked(f Frame) {
	for sub := range d.subs {
		select {
		case sub.C <- f:
		default:
			d.dropLocked(sub)
		}
	}
}

func (d *Driver) statusChangedLocked() {
	st := d.statusLocked()
	d.broadcastLocked(Frame{T: FrameStatus, Status: &st})
}

// later queues a listener call for after the lock.
func (d *Driver) later(f func()) {
	if f != nil {
		d.calls = append(d.calls, f)
	}
}

// unlock releases the lock and makes the queued listener calls.
func (d *Driver) unlock() {
	calls := d.calls
	d.calls = nil
	d.mu.Unlock()
	for _, f := range calls {
		f()
	}
}

// Status is the console's live state.
func (d *Driver) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.statusLocked()
}

// Pending is the requests waiting for the owner.
func (d *Driver) Pending() []Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Request, len(d.reqs))
	for i, r := range d.reqs {
		out[i] = *r
	}
	return out
}

// Items returns up to limit items before the item with id before ("" = the latest), and
// whether older ones exist.
func (d *Driver) Items(before string, limit int) ([]Item, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	end := len(d.items)
	if before != "" {
		i, ok := d.index[before]
		if !ok {
			return nil, false
		}
		end = i
	}
	if limit <= 0 || limit > 1000 {
		limit = SnapshotItems
	}
	from := max(end-limit, 0)
	return append([]Item{}, d.items[from:end]...), from > 0
}

// Started reports whether the CLI has the conversation (a new CLI resumes it).
func (d *Driver) Started() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.started
}

// Send sends the owner's message, starting the CLI when none runs.
func (d *Driver) Send(text string) error {
	text = strings.TrimRight(text, " \t\r\n")
	if strings.TrimSpace(text) == "" {
		return ErrEmpty
	}
	if len(text) > MaxMessage {
		return ErrTooLong
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrClosed
	}
	if err := d.startLocked(); err != nil {
		d.unlock()
		return err
	}
	now := time.Now().UTC()
	it := Item{ID: newID("u"), Kind: KindUser, At: now, Text: text}
	d.upsertLocked(it)
	d.persistLocked(record{Item: &it})
	d.broadcastLocked(Frame{T: FrameItem, Item: &it})
	d.turnStartLocked(now)
	d.stopIdleLocked()
	msg := text
	if d.handOver && !d.started {
		// the first message of the conversation that took the chat over brings the chat so far
		msg = handOverPrompt(d.items[:len(d.items)-1]) + text
		d.handOver = false
		d.persistLocked(record{Meta: &meta{HandedOver: true}})
	}
	b := d.eng.message(msg)
	d.unlock()
	if b == nil {
		return nil // the engine sends it once the CLI is ready for it
	}
	return d.write(b)
}

// Warm starts the CLI when none runs, so that the first message goes faster.
func (d *Driver) Warm() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrClosed
	}
	err := d.startLocked()
	if err == nil && !d.status.Working && len(d.reqs) == 0 {
		d.idleLocked()
	}
	d.unlock()
	return err
}

// Answer answers a request.
func (d *Driver) Answer(id string, a Answer) error {
	if a.Mode != "" && !slices.Contains(PermissionModes, a.Mode) {
		return ErrBadMode
	}
	if handled, err := d.answerRouted(id, a); handled {
		return err
	}
	d.mu.Lock()
	idx := -1
	for i, r := range d.reqs {
		if r.ID == id {
			idx = i
		}
	}
	if idx < 0 || d.proc == nil {
		d.mu.Unlock()
		return ErrNoRequest
	}
	r := d.reqs[idx]
	b, err := d.eng.answer(r, a)
	if err != nil {
		d.mu.Unlock()
		return err
	}
	d.reqs = append(d.reqs[:idx], d.reqs[idx+1:]...)
	if (a.Decision == Deny || a.Decision == DenyStop) && r.ToolUseID != "" {
		d.denied[r.ToolUseID] = true
	}
	mode := ""
	if r.Kind == ReqPlan && (a.Decision == Allow || a.Decision == AllowAlways) {
		mode = a.Mode
	}
	if r.Kind == ReqQuestion || r.Kind == ReqPlan {
		// the answer stays in the chat: the CLI's own record of it is a tool result, or nothing
		it := answerItem(r, a, time.Now().UTC())
		d.upsertLocked(it)
		d.persistLocked(record{Item: &it})
		d.broadcastLocked(Frame{T: FrameItem, Item: &it})
	}
	if a.Decision == DenyStop {
		d.status.Stopping = true
		d.statusChangedLocked()
	}
	d.broadcastLocked(Frame{T: FrameResolved, ID: id})
	if len(d.reqs) == 0 {
		d.later(d.cfg.On.Answered)
	}
	d.unlock()
	if err := d.write(b); err != nil || mode == "" {
		return err
	}
	// the hub's mode, and the CLI's in case it took the approval's mode before leaving plan mode
	return d.SetPermissionMode(mode)
}

// Interrupt stops the current turn.
func (d *Driver) Interrupt() error {
	d.mu.Lock()
	if d.proc == nil || !d.status.Working {
		d.mu.Unlock()
		return ErrNotWorking
	}
	b := d.eng.interrupt()
	if b == nil {
		d.mu.Unlock()
		return ErrNotWorking
	}
	d.status.Stopping = true
	d.statusChangedLocked()
	d.mu.Unlock()
	return d.write(b)
}

// SetModel switches the model ("" = the CLI's default) of the running CLI.
func (d *Driver) SetModel(model string) error {
	d.mu.Lock()
	if d.proc == nil {
		d.mu.Unlock()
		return nil // the next CLI starts with the console's model
	}
	b := d.eng.setModel(model)
	d.status.Model = model
	d.statusChangedLocked()
	d.mu.Unlock()
	if b == nil {
		return nil
	}
	return d.write(b)
}

// SetPermissionMode switches the permission mode, now and for the CLIs after this one.
func (d *Driver) SetPermissionMode(mode string) error {
	ok := false
	for _, m := range PermissionModes {
		ok = ok || m == mode
	}
	if !ok {
		return ErrBadMode
	}
	d.mu.Lock()
	d.status.PermissionMode = mode
	d.persistLocked(record{Meta: &meta{Mode: mode}})
	d.statusChangedLocked()
	var b []byte
	if d.proc != nil {
		b = d.eng.setMode(mode)
	}
	d.mu.Unlock()
	if b == nil {
		return nil
	}
	return d.write(b)
}

// Reset ends the running CLI (a turn in progress is stopped), so that the next message starts
// one with the console's launch as it is now (another model, login or permission mode). The
// chat and the conversation stay.
func (d *Driver) Reset() {
	d.mu.Lock()
	p := d.proc
	if p != nil {
		d.expecting = true
	}
	d.mu.Unlock()
	if p != nil {
		p.Stop()
	}
}

// Close stops the CLI and ends every view.
func (d *Driver) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.stopIdleLocked()
	p := d.proc
	d.expecting = true
	for sub := range d.subs {
		d.dropLocked(sub)
	}
	if d.hist != nil {
		_ = d.hist.Close()
		d.hist = nil
	}
	d.mu.Unlock()
	if p != nil {
		p.Stop()
	}
}

func (d *Driver) write(b []byte) error {
	d.mu.Lock()
	p := d.proc
	d.mu.Unlock()
	if p == nil {
		return ErrClosed
	}
	d.wmu.Lock()
	defer d.wmu.Unlock()
	_, err := p.Stdin.Write(b)
	return err
}

// startLocked starts the CLI when none runs.
func (d *Driver) startLocked() error {
	if d.proc != nil {
		return nil
	}
	mode := d.status.PermissionMode
	eng := newEngine(d.cfg.Engine)
	p, err := d.cfg.Start(d.started, func(base []string) []string { return eng.argv(base, mode) })
	if err != nil {
		d.status.Error = err.Error()
		d.statusChangedLocked()
		return err
	}
	d.gen++
	gen := d.gen
	d.proc, d.expecting = p, false
	d.eng = eng
	d.status.Process, d.status.Error, d.status.Note = ProcStarting, "", ""
	d.statusChangedLocked()
	// first, before any message: written here, under the lock, so nothing goes ahead of it
	d.wmu.Lock()
	_, _ = p.Stdin.Write(d.eng.start(d.started, d.status.Conversation))
	d.wmu.Unlock()
	go d.read(gen, p, eng)
	go d.wait(gen, p)
	return nil
}

// read decodes a CLI's output with that CLI's engine (a newer CLI has its own).
func (d *Driver) read(gen int, p *Proc, eng engine) {
	r := bufio.NewReaderSize(p.Stdout, 64<<10)
	for {
		b, err := r.ReadBytes('\n')
		if len(b) > 0 {
			b = bytes.TrimSpace(b)
			if len(b) > 0 && b[0] == '{' {
				d.handle(gen, eng, b)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, os.ErrClosed) {
				log.Printf("nextcode read: %v", err)
			}
			return
		}
	}
}

func (d *Driver) wait(gen int, p *Proc) {
	<-p.Done
	d.mu.Lock()
	if gen != d.gen || d.proc != p {
		d.mu.Unlock()
		return
	}
	d.proc = nil
	d.stopIdleLocked()
	expected := d.expecting || d.closed
	now := time.Now().UTC()
	for i := range d.items {
		if t := d.items[i].Tool; t != nil && t.Status == ToolRunning {
			t.Status = ToolStopped
			it := d.items[i]
			d.persistLocked(record{Item: &it})
			d.broadcastLocked(Frame{T: FrameItem, Item: &it})
		}
	}
	for _, r := range d.reqs {
		d.broadcastLocked(Frame{T: FrameResolved, ID: r.ID})
	}
	d.reqs = nil
	wasWorking := d.status.Working
	d.status.Process, d.status.Working, d.status.TurnSince, d.status.Stopping, d.status.Note = ProcStopped, false, nil, false, ""
	if !expected {
		msg := exitText(p)
		d.status.Error = msg
		it := Item{ID: newID("e"), Kind: KindError, At: now, Text: msg}
		d.upsertLocked(it)
		d.persistLocked(record{Item: &it})
		d.broadcastLocked(Frame{T: FrameItem, Item: &it})
		if on := d.cfg.On.Exit; on != nil {
			d.later(func() { on(msg) })
		}
	} else if wasWorking {
		if on := d.cfg.On.TurnEnd; on != nil {
			d.later(func() { on("") })
		}
	}
	d.statusChangedLocked()
	d.unlock()
}

// exitText says why a CLI ended by itself.
func exitText(p *Proc) string {
	tail := ""
	if p.Stderr != nil {
		tail = strings.TrimSpace(p.Stderr())
	}
	if tail == "" {
		return "The agent's CLI ended unexpectedly."
	}
	lines := strings.Split(tail, "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return "The agent's CLI ended: " + strings.Join(lines, "\n")
}

// handle applies one line of the CLI's output.
func (d *Driver) handle(gen int, eng engine, b []byte) {
	evs := eng.decode(b)
	if len(evs) == 0 {
		return
	}
	var replies [][]byte
	d.mu.Lock()
	if gen != d.gen || d.closed {
		d.mu.Unlock()
		return
	}
	if d.status.Process == ProcStarting {
		d.status.Process = ProcRunning
		d.statusChangedLocked()
	}
	now := time.Now().UTC()
	for _, e := range evs {
		switch e.kind {
		case evActive:
			if !d.status.Working {
				d.turnStartLocked(now) // the CLI started a turn by itself
			}
		case evBlock:
			d.broadcastLocked(Frame{T: FrameBlock, ID: e.id, Kind: e.sub, Name: e.name, Parent: e.parent})
		case evDelta:
			d.broadcastLocked(Frame{T: FrameDelta, ID: e.id, Kind: e.sub, Text: e.text})
		case evItem:
			it := *e.item
			it.At = now
			if t := it.Tool; t != nil {
				t.Input, t.Clipped = clipInput(t.Input)
				if on := d.cfg.On.Tool; on != nil && it.Parent == "" {
					name, sum := t.Name, toolSummary(t.Input)
					d.later(func() { on(name, sum) })
				}
			}
			if old, ok := d.index[it.ID]; ok && d.items[old].Tool != nil && it.Tool != nil {
				it.Tool.Output, it.Tool.Status = d.items[old].Tool.Output, d.items[old].Tool.Status
			}
			d.upsertLocked(it)
			d.persistLocked(record{Item: &it})
			d.broadcastLocked(Frame{T: FrameItem, Item: &it})
		case evResult:
			i, ok := d.index[e.id]
			if !ok || d.items[i].Tool == nil {
				continue
			}
			it := d.items[i]
			t := *it.Tool
			t.Output = clipOutput(e.text)
			switch {
			case d.denied[e.id]:
				t.Status = ToolDenied
			case e.isError:
				t.Status = ToolError
			default:
				t.Status = ToolDone
			}
			it.Tool = &t
			d.items[i] = it
			d.persistLocked(record{Item: &it})
			d.broadcastLocked(Frame{T: FrameItem, Item: &it})
		case evRequest:
			r := e.req
			r.At = now
			if t, ok := d.index[r.ToolUseID]; ok {
				r.Parent = d.items[t].Parent
			}
			d.reqs = append(d.reqs, r)
			cp := *r
			d.broadcastLocked(Frame{T: FrameRequest, Request: &cp})
			d.stopIdleLocked()
			if on := d.cfg.On.NeedsYou; on != nil {
				kind, text := r.Kind, needsText(r)
				d.later(func() { on(kind, text) })
			}
		case evCancel:
			for i, r := range d.reqs {
				if r.ID == e.id {
					d.reqs = append(d.reqs[:i], d.reqs[i+1:]...)
					d.broadcastLocked(Frame{T: FrameResolved, ID: e.id})
					break
				}
			}
		case evInit:
			changed := false
			if e.conv != "" && e.conv != d.status.Conversation {
				d.status.Conversation = e.conv
				d.persistLocked(record{Meta: &meta{Conversation: e.conv}})
				if on := d.cfg.On.Conversation; on != nil {
					id := e.conv
					d.later(func() { on(id) })
				}
				changed = true
			}
			if e.model != "" && e.model != d.status.Model {
				d.status.Model, changed = e.model, true
			}
			if e.mode != "" && e.mode != d.status.PermissionMode {
				d.status.PermissionMode, changed = e.mode, true
			}
			if changed {
				d.statusChangedLocked()
			}
		case evCommands:
			d.status.Commands = e.commands
			d.statusChangedLocked()
		case evRate:
			d.status.RateLimit = e.raw
			d.statusChangedLocked()
		case evNote:
			if d.status.Note != e.text {
				d.status.Note = e.text
				d.statusChangedLocked()
			}
		case evNotice:
			it := Item{ID: newID("n"), Kind: KindNotice, At: now, Text: e.text}
			d.upsertLocked(it)
			d.persistLocked(record{Item: &it})
			d.broadcastLocked(Frame{T: FrameItem, Item: &it})
		case evReply:
			replies = append(replies, e.reply)
		case evTurnEnd:
			d.turnEndLocked(e, now)
		}
	}
	d.unlock()
	for _, r := range replies {
		_ = d.write(r)
	}
}

// needsText is what a request waits for, as the console's activity says it.
func needsText(r *Request) string {
	switch r.Kind {
	case ReqQuestion:
		return "Question"
	case ReqPlan:
		return "Plan approval"
	}
	if s := toolSummary(r.Input); s != "" {
		if len(s) > 80 {
			s = s[:80] + "…"
		}
		return r.Tool + "(" + s + ")"
	}
	return r.Tool
}

func (d *Driver) turnStartLocked(now time.Time) {
	if d.status.Working {
		return
	}
	d.status.Working, d.status.Stopping = true, false
	t := now
	d.status.TurnSince = &t
	d.statusChangedLocked()
	d.later(d.cfg.On.TurnStart)
}

func (d *Driver) turnEndLocked(e ev, now time.Time) {
	stopped := d.status.Stopping
	if !d.started {
		d.started = true
		d.persistLocked(record{Meta: &meta{Started: true}})
	}
	d.status.Working, d.status.TurnSince, d.status.Stopping, d.status.Note = false, nil, false, ""
	d.status.CostUSD = e.cost
	if e.usage != nil {
		d.status.Usage = e.usage
	}
	// a tool whose result never came did not finish
	for i := range d.items {
		if t := d.items[i].Tool; t != nil && t.Status == ToolRunning {
			t.Status = ToolStopped
			it := d.items[i]
			d.persistLocked(record{Item: &it})
			d.broadcastLocked(Frame{T: FrameItem, Item: &it})
		}
	}
	errText := ""
	switch {
	case stopped:
		it := Item{ID: newID("n"), Kind: KindNotice, At: now, Text: "Stopped."}
		d.upsertLocked(it)
		d.persistLocked(record{Item: &it})
		d.broadcastLocked(Frame{T: FrameItem, Item: &it})
	case e.failed:
		errText = e.text
		// a CLI that cannot answer ("Not logged in") says it as the reply and again as the
		// result: the reply becomes the error, not a second copy of it
		if n := len(d.items); n > 0 && d.items[n-1].Kind == KindText && strings.TrimSpace(d.items[n-1].Text) == strings.TrimSpace(e.text) {
			it := d.items[n-1]
			it.Kind = KindError
			d.items[n-1] = it
			d.persistLocked(record{Item: &it})
			d.broadcastLocked(Frame{T: FrameItem, Item: &it})
			break
		}
		it := Item{ID: newID("e"), Kind: KindError, At: now, Text: e.text}
		d.upsertLocked(it)
		d.persistLocked(record{Item: &it})
		d.broadcastLocked(Frame{T: FrameItem, Item: &it})
	}
	d.statusChangedLocked()
	if on := d.cfg.On.TurnEnd; on != nil {
		d.later(func() { on(errText) })
	}
	if len(d.reqs) == 0 {
		d.idleLocked()
	}
}

// idleLocked arms the idle exit.
func (d *Driver) idleLocked() {
	d.stopIdleLocked()
	if d.cfg.IdleExit <= 0 || d.proc == nil {
		return
	}
	gen := d.gen
	d.idle = time.AfterFunc(d.cfg.IdleExit, func() { d.idleExit(gen) })
}

func (d *Driver) stopIdleLocked() {
	if d.idle != nil {
		d.idle.Stop()
		d.idle = nil
	}
}

// idleExit asks an idle CLI to end: its stdin closes, and a CLI with nothing to do ends.
func (d *Driver) idleExit(gen int) {
	d.mu.Lock()
	p := d.proc
	if gen != d.gen || p == nil || d.status.Working || len(d.reqs) > 0 || d.closed {
		d.mu.Unlock()
		return
	}
	d.expecting = true
	d.mu.Unlock()
	d.wmu.Lock()
	_ = p.Stdin.Close()
	d.wmu.Unlock()
	go func() {
		select {
		case <-p.Done:
		case <-time.After(exitWait):
			p.Stop()
		}
	}()
}

// clipInput keeps a tool call's input under maxInput: long strings in it are cut.
func clipInput(in json.RawMessage) (json.RawMessage, bool) {
	if len(in) <= maxInput {
		return in, false
	}
	var m map[string]any
	if json.Unmarshal(in, &m) != nil {
		return json.RawMessage(`{}`), true
	}
	for k, v := range m {
		if s, ok := v.(string); ok && len(s) > 8<<10 {
			m[k] = s[:8<<10] + "\n…"
		}
	}
	b, _ := json.Marshal(m)
	if len(b) > maxInput {
		return json.RawMessage(`{}`), true
	}
	return b, true
}

// clipOutput keeps the start and the end of a long output.
func clipOutput(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	head, tail := s[:maxOutput/2], s[len(s)-maxOutput/2:]
	return strings.ToValidUTF8(head, "") + "\n…\n" + strings.ToValidUTF8(tail, "")
}
