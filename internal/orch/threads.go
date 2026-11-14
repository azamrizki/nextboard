package orch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/store"
)

// Threads (docs/API-orchestration.md §4): one per project (id p_…) and one
// per orchestrator card (id n_…), stored append-only in
// threads/<tid>.jsonl. An updated message (same id) is appended again;
// the last version wins on load. Files are compacted past their limit.
// The whole reply of a report lives beside them in threads/<tid>.outputs/,
// for as long as its message does: a console's last reply changes with its
// next turn, what a message reports does not.

const (
	projectThreadMax = 500
	cardThreadMax    = 200
	maxReportText    = 8000
	maxExcerpt       = 400
)

var threadIDRe = regexp.MustCompile(`^[pn]_[a-z0-9]{6,24}$`)

// ValidThreadID reports whether tid is a project or card thread id.
func ValidThreadID(tid string) bool { return threadIDRe.MatchString(tid) }

// ErrNoOutput: the message is gone, or it has no reply to show.
var ErrNoOutput = errors.New("this message has no saved reply")

// ReplyOutput is the reply a report message stands for.
type ReplyOutput struct {
	MessageID string    `json:"messageId"`
	ThreadID  string    `json:"threadId"`
	ProjectID string    `json:"projectId,omitempty"`
	OrderID   string    `json:"orderId,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	Handle    string    `json:"handle,omitempty"`
	Source    string    `json:"source,omitempty"` // transcript | screen
	Text      string    `json:"text"`
	At        time.Time `json:"at"`
	Chars     int       `json:"chars"`
	Truncated bool      `json:"truncated"`
	// Partial: a report from before replies were kept; only its message text is left.
	Partial bool `json:"partial,omitempty"`
}

// PlanNote says what was sent to whom.
type PlanNote struct {
	Handle string `json:"handle"`
	Text   string `json:"text"`
}

// Report is the result of one order target.
type Report struct {
	State      string `json:"state"` // finished | failed | timeout | skipped
	DurationMs int64  `json:"durationMs"`
	Excerpt    string `json:"excerpt"`
	Chars      int    `json:"chars"`
}

// ThreadMessage is one entry of a thread.
type ThreadMessage struct {
	ID             string     `json:"id"`
	ThreadID       string     `json:"threadId"`
	At             time.Time  `json:"at"`
	Role           string     `json:"role"` // user | hub | agent
	Kind           string     `json:"kind"` // order | plan | report | reply | note | confirm | error
	Text           string     `json:"text"`
	Source         string     `json:"source,omitempty"`
	OrderID        string     `json:"orderId,omitempty"`
	SessionID      string     `json:"sessionId,omitempty"`
	Handle         string     `json:"handle,omitempty"`
	Plan           []PlanNote `json:"plan,omitempty"`
	Report         *Report    `json:"report,omitempty"`
	ConfirmationID string     `json:"confirmationId,omitempty"`
	ReplyTo        *ReplyRef  `json:"replyTo,omitempty"`
	// Resend: the client offers to send OrderID again while a target is Undelivered.
	Resend bool `json:"resend,omitempty"`
}

type thread struct {
	msgs  []ThreadMessage
	lines int // lines in the file
}

// Threads stores every thread.
type Threads struct {
	st  *store.Store
	Now func() time.Time
	// OnMessage publishes {"type":"thread"} (new or updated message).
	OnMessage func(ThreadMessage)

	mu sync.Mutex
	th map[string]*thread
}

func newThreads(st *store.Store) *Threads {
	_ = os.MkdirAll(st.Path("threads"), 0o700)
	return &Threads{st: st, Now: time.Now, th: map[string]*thread{}}
}

func threadMax(tid string) int {
	if len(tid) > 0 && tid[0] == 'n' {
		return cardThreadMax
	}
	return projectThreadMax
}

func (t *Threads) path(tid string) string { return t.st.Path("threads", tid+".jsonl") }

// loadLocked reads a thread file once.
func (t *Threads) loadLocked(tid string) *thread {
	if th, ok := t.th[tid]; ok {
		return th
	}
	th := &thread{}
	t.th[tid] = th
	f, err := os.Open(t.path(tid))
	if err != nil {
		return th
	}
	defer f.Close()
	idx := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		th.lines++
		var m ThreadMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == "" {
			continue
		}
		if i, ok := idx[m.ID]; ok {
			th.msgs[i] = m
			continue
		}
		idx[m.ID] = len(th.msgs)
		th.msgs = append(th.msgs, m)
	}
	if max := threadMax(tid); len(th.msgs) > max {
		th.msgs = th.msgs[len(th.msgs)-max:]
	}
	return th
}

func (t *Threads) appendLine(tid string, m ThreadMessage) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	f, err := os.OpenFile(t.path(tid), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	f.Close()
}

// compactLocked rewrites the file with the kept messages.
func (t *Threads) compactLocked(tid string, th *thread) {
	var buf bytes.Buffer
	for _, m := range th.msgs {
		b, _ := json.Marshal(m)
		buf.Write(append(b, '\n'))
	}
	if store.WriteFileAtomic(t.path(tid), buf.Bytes()) == nil {
		th.lines = len(th.msgs)
	}
	t.pruneOutputsLocked(tid, th)
}

func (t *Threads) outputsDir(tid string) string { return t.st.Path("threads", tid+".outputs") }

// pruneOutputsLocked removes the replies of messages that left the thread.
func (t *Threads) pruneOutputsLocked(tid string, th *thread) {
	ents, err := os.ReadDir(t.outputsDir(tid))
	if err != nil {
		return
	}
	kept := make(map[string]bool, len(th.msgs))
	for _, m := range th.msgs {
		kept[m.ID+".json"] = true
	}
	for _, e := range ents {
		if !kept[e.Name()] {
			_ = os.Remove(filepath.Join(t.outputsDir(tid), e.Name()))
		}
	}
}

// SaveOutput keeps the reply of a report message. Written before the message exists, so
// whoever sees the message can read it.
func (t *Threads) SaveOutput(o ReplyOutput) {
	if !ValidThreadID(o.ThreadID) || o.MessageID == "" {
		return
	}
	b, err := json.Marshal(o)
	if err != nil || os.MkdirAll(t.outputsDir(o.ThreadID), 0o700) != nil {
		return
	}
	_ = store.WriteFileAtomic(filepath.Join(t.outputsDir(o.ThreadID), o.MessageID+".json"), b)
}

// Output returns the reply of a finished report. The id must be a message of the thread,
// which also keeps it from naming any other file.
func (t *Threads) Output(tid, mid string) (ReplyOutput, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	th := t.loadLocked(tid)
	i := slices.IndexFunc(th.msgs, func(m ThreadMessage) bool { return m.ID == mid })
	if i < 0 {
		return ReplyOutput{}, ErrNoOutput
	}
	m := th.msgs[i]
	if m.Report == nil || m.Report.State != TFinished || m.Report.Chars == 0 {
		return ReplyOutput{}, ErrNoOutput
	}
	if b, err := os.ReadFile(filepath.Join(t.outputsDir(tid), mid+".json")); err == nil {
		var o ReplyOutput
		if json.Unmarshal(b, &o) == nil && o.Text != "" {
			return o, nil
		}
	}
	return ReplyOutput{MessageID: mid, ThreadID: tid, OrderID: m.OrderID, SessionID: m.SessionID, Handle: m.Handle, Text: m.Text, At: m.At,
		Chars: m.Report.Chars, Partial: utf8.RuneCountInString(m.Text) < m.Report.Chars}, nil
}

// Add appends a new message (id and time filled in) and publishes it.
func (t *Threads) Add(m ThreadMessage) ThreadMessage {
	if !ValidThreadID(m.ThreadID) {
		return m
	}
	if m.ID == "" {
		m.ID = auth.RandomID("tm_")
	}
	if m.At.IsZero() {
		m.At = t.Now().UTC()
	}
	if len([]rune(m.Text)) > maxReportText {
		m.Text = string([]rune(m.Text)[:maxReportText])
	}
	t.mu.Lock()
	th := t.loadLocked(m.ThreadID)
	th.msgs = append(th.msgs, m)
	th.lines++
	t.appendLine(m.ThreadID, m)
	max := threadMax(m.ThreadID)
	if len(th.msgs) > max {
		th.msgs = slices.Clone(th.msgs[len(th.msgs)-max:])
	}
	if th.lines > 2*max {
		t.compactLocked(m.ThreadID, th)
	}
	t.mu.Unlock()
	if t.OnMessage != nil {
		t.OnMessage(m)
	}
	return m
}

// Update replaces a message with the same id (e.g. "Still thinking…").
func (t *Threads) Update(m ThreadMessage) {
	if !ValidThreadID(m.ThreadID) || m.ID == "" {
		return
	}
	t.mu.Lock()
	th := t.loadLocked(m.ThreadID)
	found := false
	for i := range th.msgs {
		if th.msgs[i].ID == m.ID {
			if m.At.IsZero() {
				m.At = th.msgs[i].At
			}
			th.msgs[i], found = m, true
			break
		}
	}
	if !found {
		t.mu.Unlock()
		return
	}
	th.lines++
	t.appendLine(m.ThreadID, m)
	if th.lines > 2*threadMax(m.ThreadID) {
		t.compactLocked(m.ThreadID, th)
	}
	t.mu.Unlock()
	if t.OnMessage != nil {
		t.OnMessage(m)
	}
}

// List returns up to limit messages before the message id "before" (or
// the newest), oldest first, and whether older ones exist.
func (t *Threads) List(tid, before string, limit int) ([]ThreadMessage, bool) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	t.mu.Lock()
	defer t.mu.Unlock()
	th := t.loadLocked(tid)
	end := len(th.msgs)
	if before != "" {
		end = 0
		for i, m := range th.msgs {
			if m.ID == before {
				end = i
				break
			}
		}
	}
	start := max(0, end-limit)
	out := slices.Clone(th.msgs[start:end])
	if out == nil {
		out = []ThreadMessage{}
	}
	return out, start > 0
}

// Last returns the last n messages.
func (t *Threads) Last(tid string, n int) []ThreadMessage {
	l, _ := t.List(tid, "", n)
	return l
}

// Merge moves one thread's messages into another, in time order, with the replies they keep; the
// other thread is then empty (a project merged into another, ROADMAP #49). The oldest go first
// when both together are more than a thread keeps.
func (t *Threads) Merge(into, from string) int {
	if into == from || !ValidThreadID(into) || !ValidThreadID(from) {
		return 0
	}
	t.mu.Lock()
	dst, src := t.loadLocked(into), t.loadLocked(from)
	n := len(src.msgs)
	if n > 0 {
		all := slices.Clone(dst.msgs)
		for _, m := range src.msgs {
			m.ThreadID = into
			all = append(all, m)
		}
		slices.SortStableFunc(all, func(a, b ThreadMessage) int { return a.At.Compare(b.At) })
		if max := threadMax(into); len(all) > max {
			all = all[len(all)-max:]
		}
		_ = os.MkdirAll(t.outputsDir(into), 0o700)
		if ents, err := os.ReadDir(t.outputsDir(from)); err == nil {
			for _, e := range ents {
				_ = os.Rename(filepath.Join(t.outputsDir(from), e.Name()), filepath.Join(t.outputsDir(into), e.Name()))
			}
		}
		dst.msgs = all
		t.compactLocked(into, dst)
	}
	t.th[from] = &thread{}
	_ = os.Remove(t.path(from))
	_ = os.RemoveAll(t.outputsDir(from))
	t.mu.Unlock()
	return n
}

// Clear empties a thread (DELETE /api/threads/{tid}).
func (t *Threads) Clear(tid string) {
	t.mu.Lock()
	t.th[tid] = &thread{}
	_ = os.Remove(t.path(tid))
	_ = os.RemoveAll(t.outputsDir(tid))
	t.mu.Unlock()
}

// ---- orders (§4) ----

// Order target states.
const (
	TQueued    = "queued"
	TDelivered = "delivered"
	TWorking   = "working"
	TNeedsYou  = "needsYou"
	TFinished  = "finished"
	TFailed    = "failed"
	TTimeout   = "timeout"
	TCancelled = "cancelled"
	TSkipped   = "skipped"
)

// OrderTarget is one console an order went to.
type OrderTarget struct {
	SessionID  string     `json:"sessionId"`
	Handle     string     `json:"handle"`
	DeliveryID string     `json:"deliveryId"`
	State      string     `json:"state"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Error      string     `json:"error,omitempty"`
	// SentAt is when the delivery's Return went out (turn matching).
	SentAt *time.Time `json:"sentAt,omitempty"`
	Depth  int        `json:"forwardDepth,omitempty"`
	// Undelivered: the hub restarted before the text reached this console. A person
	// can send it again (POST /api/orders/{id}/resend); the hub never does so itself,
	// the moment may have passed.
	Undelivered bool `json:"undelivered,omitempty"`
}

// TargetFinal reports whether a target state is final.
func TargetFinal(s string) bool {
	switch s {
	case TFinished, TFailed, TTimeout, TCancelled, TSkipped:
		return true
	}
	return false
}

// Order is text sent to one or more consoles from a thread.
type Order struct {
	ID        string        `json:"id"`
	ThreadID  string        `json:"threadId"`
	ProjectID string        `json:"projectId"`
	CommandID string        `json:"commandId"`
	Text      string        `json:"text"`
	Source    string        `json:"source,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	Targets   []OrderTarget `json:"targets"`
}

// Active reports whether a target still runs.
func (o Order) Active() bool {
	for _, t := range o.Targets {
		if !TargetFinal(t.State) {
			return true
		}
	}
	return false
}

const (
	ordersFile = "orders.json"
	keepOrders = 200
	orderKeep  = 24 * time.Hour
)

// Orders keeps orders in memory and orders.json.
type Orders struct {
	st  *store.Store
	mu  sync.Mutex
	all map[string]*Order
}

func newOrders(st *store.Store) *Orders {
	o := &Orders{st: st, all: map[string]*Order{}}
	var list []*Order
	if _, err := st.Load(ordersFile, &list); err == nil {
		for _, x := range list {
			if x != nil && x.ID != "" {
				o.all[x.ID] = x
			}
		}
	}
	return o
}

// interrupted marks targets that were running when the hub stopped
// (deliveries live only in memory) and returns the touched orders.
func (o *Orders) interrupted(now time.Time) []Order {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []Order
	for _, x := range o.all {
		touched := false
		for i := range x.Targets {
			if !TargetFinal(x.Targets[i].State) {
				t := now.UTC()
				x.Targets[i].Undelivered = x.Targets[i].State == TQueued
				x.Targets[i].State, x.Targets[i].Error, x.Targets[i].FinishedAt = TFailed, "interrupted by hub restart", &t
				touched = true
			}
		}
		if touched {
			out = append(out, cloneOrder(x))
		}
	}
	return out
}

func cloneOrder(x *Order) Order {
	c := *x
	c.Targets = slices.Clone(x.Targets)
	return c
}

// Put stores (a copy of) an order.
func (o *Orders) Put(x Order) {
	o.mu.Lock()
	c := cloneOrder(&x)
	o.all[x.ID] = &c
	o.mu.Unlock()
}

// Get returns an order.
func (o *Orders) Get(id string) (Order, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	x, ok := o.all[id]
	if !ok {
		return Order{}, false
	}
	return cloneOrder(x), true
}

// Mutate changes an order under the lock; f reports whether it changed.
func (o *Orders) Mutate(id string, f func(x *Order) bool) (Order, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	x, ok := o.all[id]
	if !ok || !f(x) {
		return Order{}, false
	}
	return cloneOrder(x), true
}

// List returns orders matching keep, newest first.
func (o *Orders) List(keep func(Order) bool) []Order {
	o.mu.Lock()
	out := []Order{}
	for _, x := range o.all {
		// asked first, then copied: the tick lists the active ones four times a second
		if keep == nil || keep(*x) {
			out = append(out, cloneOrder(x))
		}
	}
	o.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Save writes active orders and finished ones younger than 24 h (≤ 200).
func (o *Orders) Save(now time.Time) {
	list := o.List(nil)
	keep := []Order{}
	for _, x := range list {
		if x.Active() || now.Sub(x.CreatedAt) < orderKeep {
			keep = append(keep, x)
		}
	}
	if len(keep) > keepOrders {
		keep = keep[:keepOrders]
	}
	o.mu.Lock()
	ids := map[string]bool{}
	for _, x := range keep {
		ids[x.ID] = true
	}
	for id := range o.all {
		if !ids[id] {
			delete(o.all, id)
		}
	}
	o.mu.Unlock()
	_ = o.st.Save(ordersFile, keep)
}

// threadDir is used by tests.
func threadDir(st *store.Store) string { return filepath.Join(st.Dir, "threads") }
