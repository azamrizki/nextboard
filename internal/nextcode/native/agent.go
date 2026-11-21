package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
	"github.com/azamrizki/nextboard/internal/nextcode/native/tools"
)

const (
	// shutdownWait is how long a turn may take to stop once stdin closed.
	shutdownWait = 5 * time.Second
	// fetchTimeout bounds the HTTP calls of the tools (WebFetch).
	fetchTimeout = 2 * time.Minute
	// compactCommand is the owner's message that compacts the conversation.
	compactCommand = "/compact"
)

// agent is one running NextCode agent. Two goroutines share it: the reader, which takes the
// driver's lines from stdin, and the worker, which runs the turns one at a time. What both
// touch is under mu; the conversation is the worker's alone.
type agent struct {
	cfg    *config
	out    *output
	client llm.Client
	tools  map[string]tools.Tool
	defs   []llm.ToolDef // the same for every call of the conversation, whatever the mode
	names  []string
	env    *tools.Env
	roots  []string // the real paths of the working folders
	store  store
	ledger *ledger // nil = none
	id     string  // the conversation's

	// the worker's
	conv   *conversation
	ids    map[string]bool // the tool_use ids in the conversation, kept unique
	msgTag string          // makes the stream's message ids unique to this process
	msgN   int
	notes  []string // per-turn notes for the next user message

	mu      sync.Mutex
	mode    string
	model   string // "" = --model
	rules   []rule
	queue   []string // the owner's messages waiting for a turn
	pending map[string]chan ownerAnswer
	cancel  context.CancelFunc // ends the running turn; nil when none runs
	closed  bool
	reqN    int
	wake    chan struct{}
}

// run is the agent's life: it answers the driver on stdout until stdin closes or ctx ends.
func run(ctx context.Context, cfg *config, stdin io.Reader, stdout, stderr io.Writer) int {
	a, err := newAgent(cfg, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "nextcode-agent: %s\n", redact(err.Error(), cfg.secret))
		return 1
	}
	return a.run(ctx, stdin)
}

func newAgent(cfg *config, stdout, stderr io.Writer) (*agent, error) {
	if cfg.editPath == nil {
		cfg.editPath = tools.EditPath
	}
	if cfg.readOnly == nil {
		cfg.readOnly = tools.ReadOnlyCommand
	}
	a := &agent{cfg: cfg, out: newOutput(stdout, stderr, cfg.secret), client: cfg.client, tools: map[string]tools.Tool{},
		store: store{dir: cfg.stateDir}, ids: map[string]bool{}, msgTag: randomHex(4), mode: cfg.mode,
		pending: map[string]chan ownerAnswer{}, wake: make(chan struct{}, 1)}
	a.env = &tools.Env{Cwd: cfg.cwd, AddDirs: cfg.addDirs, Environ: cfg.environ, Sandbox: cfg.sandbox,
		HTTP: &http.Client{Timeout: fetchTimeout}}
	for _, t := range cfg.tools {
		d := t.Def()
		if d.Name == "" || d.Name == toolAsk || d.Name == toolPlan || a.tools[d.Name] != nil {
			continue
		}
		a.tools[d.Name] = t
		a.defs = append(a.defs, d)
	}
	a.defs = append(a.defs, ownToolDefs()...)
	for _, d := range a.defs {
		a.names = append(a.names, d.Name)
	}
	for _, dir := range append([]string{cfg.cwd}, cfg.addDirs...) {
		if r := realPath(dir); r != "" {
			a.roots = append(a.roots, r)
		}
	}
	if cfg.homeDir != "" {
		a.ledger = newLedger(cfg.homeDir)
	}
	if err := a.open(); err != nil {
		return nil, err
	}
	return a, nil
}

// open finds the conversation to carry on, or starts a new one.
func (a *agent) open() error {
	id := a.cfg.sessionID
	switch {
	case a.cfg.resume != "":
		id = a.cfg.resume
	case a.cfg.cont:
		latest, err := a.store.latest()
		if err != nil {
			return err
		}
		id = latest
		if id == "" {
			a.out.logf("no conversation to continue; starting a new one")
		}
	}
	if id == "" {
		id = newUUID()
	}
	c, err := a.store.load(id)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if a.cfg.resume != "" {
			a.out.logf("no conversation %s yet; starting a new one with that id", id)
		}
		c = newConversation(id, systemPrompt(a.cfg.cwd, a.cfg.addDirs, a.cfg.appendPrompt))
	case err != nil:
		return err
	}
	a.conv, a.id = c, id
	for _, list := range append(slices.Clone(c.Archived), c.Messages) {
		for _, m := range list {
			for _, b := range m.Blocks {
				if b.Type == llm.BlockToolUse && b.ID != "" {
					a.ids[b.ID] = true
				}
			}
		}
	}
	return nil
}

func (a *agent) run(ctx context.Context, stdin io.Reader) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	worked := make(chan struct{})
	go func() {
		defer close(worked)
		a.work(ctx)
	}()
	read := make(chan struct{})
	go func() {
		defer close(read)
		a.read(stdin)
	}()
	select {
	case <-read:
	case <-ctx.Done():
	}
	a.shutdown()
	select {
	case <-worked:
	case <-time.After(shutdownWait):
		a.out.logf("the turn did not stop in time")
	}
	return 0
}

// shutdown stops the running turn and lets no other start.
func (a *agent) shutdown() {
	a.mu.Lock()
	a.closed = true
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Unlock()
	a.signal()
}

func (a *agent) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// read takes the driver's lines until stdin closes.
func (a *agent) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		b, err := br.ReadBytes('\n')
		if b = bytes.TrimSpace(b); len(b) > 0 {
			a.dispatch(b)
		}
		if err != nil {
			return
		}
	}
}

// dispatch handles one line of the driver's.
func (a *agent) dispatch(b []byte) {
	var l inLine
	if json.Unmarshal(b, &l) != nil {
		a.out.logf("ignored a line that is not JSON")
		return
	}
	switch l.Type {
	case "user":
		a.enqueue(userText(l.Message))
	case "control_request":
		a.control(l.RequestID, l.Request)
	case "control_response":
		a.answered(l.Response)
	}
}

// control answers one of the driver's control requests.
func (a *agent) control(id string, raw json.RawMessage) {
	var r struct {
		Subtype string          `json:"subtype"`
		Model   json.RawMessage `json:"model"`
		Mode    string          `json:"mode"`
	}
	if json.Unmarshal(raw, &r) != nil {
		a.out.failure(id, "the request is not a JSON object")
		return
	}
	switch r.Subtype {
	case "initialize":
		a.out.success(id, map[string]any{"commands": []any{map[string]any{"name": "compact",
			"description": "Summarize the conversation so far, to free up its context", "argumentHint": ""}}})
		a.sendInit()
	case "interrupt":
		a.interrupt()
		a.out.success(id, nil)
	case "set_model":
		var m *string
		_ = json.Unmarshal(r.Model, &m)
		a.mu.Lock()
		a.model = ""
		if m != nil {
			a.model = strings.TrimSpace(*m)
		}
		a.mu.Unlock()
		a.out.success(id, nil)
	case "set_permission_mode":
		if !slices.Contains(modes, r.Mode) {
			a.out.failure(id, fmt.Sprintf("unknown permission mode %q", r.Mode))
			return
		}
		a.setMode(r.Mode)
		a.out.success(id, nil)
	default:
		a.out.failure(id, "nextcode-agent does not handle "+r.Subtype)
	}
}

// sendInit tells the driver the conversation, the model, the mode and the tools.
func (a *agent) sendInit() {
	mode, _ := a.permissions()
	a.out.initLine(a.id, a.currentModel(), mode, a.names)
}

// interrupt stops the running turn, if one runs.
func (a *agent) interrupt() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

// setMode changes the permission mode; it reports whether it changed.
func (a *agent) setMode(mode string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	changed := a.mode != mode
	a.mode = mode
	return changed
}

// permissions is the mode and the session rules, as they are now.
func (a *agent) permissions() (string, []rule) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode, slices.Clone(a.rules)
}

// currentModel is the model of the next call.
func (a *agent) currentModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.model != "" {
		return a.model
	}
	return a.cfg.model
}

// enqueue takes the owner's message: it starts a turn, or waits for the running one to end.
func (a *agent) enqueue(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.mu.Lock()
	a.queue = append(a.queue, text)
	a.mu.Unlock()
	a.signal()
}

// work runs the turns, one at a time, until the agent shuts down.
func (a *agent) work(ctx context.Context) {
	for {
		batch, tctx, ok := a.next(ctx)
		if !ok {
			return
		}
		if isCompact(batch[0]) {
			a.compactTurn(tctx)
		} else {
			a.runTurn(tctx, batch)
		}
		a.mu.Lock()
		a.cancel()
		a.cancel = nil
		a.mu.Unlock()
	}
}

// next waits for messages and takes the next turn's: every waiting message up to a /compact,
// or the /compact alone. The turn's context is set under the lock, so that an interrupt sent
// as it starts stops it.
func (a *agent) next(ctx context.Context) ([]string, context.Context, bool) {
	for {
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return nil, nil, false
		}
		if len(a.queue) > 0 {
			n := 1
			if !isCompact(a.queue[0]) {
				for n < len(a.queue) && !isCompact(a.queue[n]) {
					n++
				}
			}
			batch := slices.Clone(a.queue[:n])
			a.queue = slices.Clone(a.queue[n:])
			tctx, cancel := context.WithCancel(ctx)
			a.cancel = cancel
			a.mu.Unlock()
			return batch, tctx, true
		}
		a.mu.Unlock()
		select {
		case <-a.wake:
		case <-ctx.Done():
			return nil, nil, false
		}
	}
}

func isCompact(s string) bool { return strings.TrimSpace(s) == compactCommand }

// ---- the conversation

// appendMessage adds a message to the conversation and saves it.
func (a *agent) appendMessage(m llm.Message) {
	a.conv.Messages = append(a.conv.Messages, m)
	a.save()
}

func (a *agent) save() {
	if err := a.store.save(a.conv); err != nil {
		a.out.logf("cannot save the conversation: %v", err)
	}
}

// reminders are the notes the next user message carries: the date and the permission mode
// when they changed since the model was last told, and the per-turn notes.
func (a *agent) reminders() []string {
	var out []string
	if today := time.Now().Format(time.DateOnly); a.conv.Date != today {
		out = append(out, "Today's date is "+today+".")
		a.conv.Date = today
	}
	mode, _ := a.permissions()
	told := a.conv.Mode
	if told == "" {
		told = modeDefault
	}
	if mode != told {
		out = append(out, modeNote(a.conv.Mode, mode))
	}
	a.conv.Mode = mode
	out = append(out, a.notes...)
	a.notes = nil
	return out
}

// reminderBlock is notes as one text block of a user message.
func reminderBlock(notes []string) llm.Block {
	var b strings.Builder
	for i, n := range notes {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("<system-reminder>" + n + "</system-reminder>")
	}
	return llm.Block{Type: llm.BlockText, Text: b.String()}
}

var modeNotes = map[string]string{
	modeDefault: "the owner approves each edit, command and fetch before it runs.",
	modeAccept:  "edits to files in the working folders go ahead without asking; commands and fetches still need the owner's approval.",
	modePlan:    "research and plan only. Read and search as much as you need, but do not edit files or run commands that change anything. When the plan is ready, call ExitPlanMode with it for the owner to approve.",
	modeAuto:    "edits in the working folders, read-only commands and fetches go ahead without asking; other commands need the owner's approval.",
	modeBypass:  "tools run without asking, except commands on the project's list of commands that need the owner's approval.",
}

// modeNote tells the model the permission mode; told is the mode it was told before ("" = none).
func modeNote(told, mode string) string {
	if told == "" {
		return fmt.Sprintf("The permission mode is %s: %s", mode, modeNotes[mode])
	}
	return fmt.Sprintf("The owner switched the permission mode to %s: %s", mode, modeNotes[mode])
}

// wire is the conversation as a call sends it: a user message that follows another (after a
// turn that ended before the model answered) goes with it as one. The merge only ever touches
// the last user messages, so what a call sends before its last answer never changes.
func wire(msgs []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && m.Role == llm.RoleUser && out[n-1].Role == llm.RoleUser {
			out[n-1] = llm.Message{Role: llm.RoleUser, Blocks: append(slices.Clone(out[n-1].Blocks), m.Blocks...)}
			continue
		}
		out = append(out, m)
	}
	return out
}
