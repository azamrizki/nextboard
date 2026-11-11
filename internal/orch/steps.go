package orch

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/store"
)

// Step is one unit of work of a command (docs/API-orchestration.md §3.4).
// Only the fields of its op are set.
type Step struct {
	Op      string   `json:"op"`
	Targets []string `json:"targets,omitempty"`
	Text    string   `json:"text,omitempty"`
	Mode    string   `json:"mode,omitempty"`
	Count   int      `json:"count,omitempty"`
	Agent   string   `json:"agent,omitempty"`
	// Account: spawn on this API provider (its name or id), or "login"; "" = settings lead.providers.
	Account        string         `json:"account,omitempty"`
	Role           string         `json:"role,omitempty"`
	GroupID        string         `json:"groupId,omitempty"`
	NewGroup       *NewGroup      `json:"newGroup,omitempty"`
	MachineID      string         `json:"machineId,omitempty"`
	Members        []string       `json:"members,omitempty"`
	Name           string         `json:"name,omitempty"`
	Source         string         `json:"source,omitempty"`
	Sources        []string       `json:"sources,omitempty"`
	OrderID        string         `json:"orderId,omitempty"`
	Query          string         `json:"query,omitempty"`
	UIAction       *UIAction      `json:"uiAction,omitempty"`
	Kind           string         `json:"kind,omitempty"`
	Data           map[string]any `json:"data,omitempty"`
	WireTo         []string       `json:"wireTo,omitempty"`
	OrchestratorID string         `json:"orchestratorId,omitempty"`
	WorkspaceID    string         `json:"workspaceId,omitempty"`
	Loop           *LoopDef       `json:"loop,omitempty"`
	Action         string         `json:"action,omitempty"`
	Message        string         `json:"message,omitempty"`
	Repo           string         `json:"repo,omitempty"` // git: one of the project's repositories
	LayoutID       string         `json:"layoutId,omitempty"`
	// Board control (§3.6).
	Tile    string    `json:"tile,omitempty"`
	Tiles   []string  `json:"tiles,omitempty"`
	Geom    *TileGeom `json:"geom,omitempty"`
	Planned bool      `json:"planned,omitempty"` // produced by the planner (risky policy)
	// Task: a task board change, or the card a send hands out (§3.10).
	Task  *TaskEdit       `json:"task,omitempty"`
	Extra map[string]bool `json:"-"`
}

// TileGeom is a tile's position and size; only what is set changes.
type TileGeom struct {
	X *float64 `json:"x,omitempty"`
	Y *float64 `json:"y,omitempty"`
	W *float64 `json:"w,omitempty"`
	H *float64 `json:"h,omitempty"`
}

// NewGroup is spawn.newGroup.
type NewGroup struct {
	Name string `json:"name,omitempty"`
	Role string `json:"role,omitempty"`
}

// Ops of §3.4.
var stepOps = map[string]bool{
	"send": true, "spawn": true, "group.create": true, "group.add": true, "group.dissolve": true,
	"pause": true, "resume": true, "restart": true, "kill": true, "handoff": true, "forward": true,
	"cancel": true, "ask": true, "ui": true, "node.add": true, "wire": true,
	"workspace.go": true, "workspace.create": true, "workspace.rename": true, "workspace.close": true,
	"loop.create": true, "git": true, "memory.push": true, "memory.scan": true, "layout.deploy": true, "reply": true,
	"node.update": true, "node.delete": true, "connect": true, "disconnect": true, "console.rename": true,
	"task.add": true, "task.update": true, "task.delete": true,
}

// PlannerOps is the subset the planner may emit (§3.5).
var PlannerOps = map[string]bool{
	"send": true, "spawn": true, "group.create": true, "group.add": true, "pause": true, "resume": true,
	"restart": true, "kill": true, "handoff": true, "forward": true, "loop.create": true, "node.add": true,
	"git": true, "ask": true, "memory.push": true, "reply": true,
	"node.update": true, "node.delete": true, "connect": true, "disconnect": true, "ui": true, "editor.open": true,
	"console.rename": true, "group.dissolve": true,
	"workspace.create": true, "workspace.rename": true, "workspace.go": true,
	"task.add": true, "task.update": true, "task.delete": true,
}

var idRe = regexp.MustCompile(`^[a-z]{1,3}_[A-Za-z0-9]{4,32}$`)

var uiActions = map[string]bool{"fit": true, "zoomIn": true, "zoomOut": true, "zoomReset": true, "focus": true,
	"openPanel": true, "closePanel": true, "tool": true, "undo": true, "redo": true, "goWorkspace": true, "openFile": true}
var uiPanels = map[string]bool{"loops": true, "memory": true, "mission": true, "settings": true, "git": true, "usage": true,
	"documents": true, "code": true, "markdown": true, "skills": true}

// ErrBadStep is a step that does not match the schema.
var ErrBadStep = errors.New("invalid step")

func badStep(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrBadStep, fmt.Sprintf(f, a...)) }

// Validate checks a step against the schema of §3.4 (ids are only checked
// for shape; the executor checks they exist).
func (s Step) Validate() error {
	if !stepOps[s.Op] {
		return badStep("unknown op %q", s.Op)
	}
	ids := func(name string, l []string, need bool) error {
		if need && len(l) == 0 {
			return badStep("%s: %s is required", s.Op, name)
		}
		if len(l) > 64 {
			return badStep("%s: too many %s", s.Op, name)
		}
		for _, id := range l {
			if !idRe.MatchString(id) {
				return badStep("%s: invalid id %q", s.Op, id)
			}
		}
		return nil
	}
	switch s.Op {
	case "send":
		if strings.TrimSpace(s.Text) == "" || len([]rune(s.Text)) > 32000 {
			return badStep("send: text 1–32000 characters")
		}
		if s.Mode != "" && s.Mode != "verbatim" && s.Mode != "rewritten" {
			return badStep("send: mode")
		}
		return ids("targets", s.Targets, true)
	case "spawn":
		if s.Count < 1 || s.Count > 12 {
			return badStep("spawn: count 1–12")
		}
		if s.Role != "" && RoleOf(s.Role) == "" {
			return badStep("spawn: role %q", s.Role)
		}
	case "group.create":
		if len(s.Members) > 12 {
			return badStep("group.create: at most 12 members")
		}
		return ids("members", s.Members, false)
	case "group.add":
		if s.Count < 1 || s.Count > 12 || !idRe.MatchString(s.GroupID) {
			return badStep("group.add: groupId and count 1–12")
		}
	case "group.dissolve":
		if !idRe.MatchString(s.GroupID) {
			return badStep("group.dissolve: groupId")
		}
	case "pause", "resume", "restart", "kill":
		return ids("targets", s.Targets, true)
	case "handoff":
		if !idRe.MatchString(s.Source) || s.Agent == "" {
			return badStep("handoff: source and agent are required")
		}
	case "forward":
		if s.Mode != "digest" && s.Mode != "split" {
			return badStep("forward: mode digest|split")
		}
		if err := ids("sources", s.Sources, true); err != nil {
			return err
		}
		if err := ids("targets", s.Targets, true); err != nil {
			return err
		}
		for _, a := range s.Sources {
			for _, b := range s.Targets {
				if a == b {
					return badStep("forward: a source is the same as a target")
				}
			}
		}
	case "ask":
		switch s.Query {
		case "status", "finished", "needsYou", "summary", "help":
		default:
			return badStep("ask: query %q", s.Query)
		}
	case "ui":
		if s.UIAction == nil || !uiActions[s.UIAction.Action] {
			return badStep("ui: uiAction")
		}
		if s.UIAction.Action == "openPanel" && !uiPanels[s.UIAction.Panel] {
			return badStep("ui: panel %q", s.UIAction.Panel)
		}
		if s.UIAction.Action == "tool" && s.UIAction.Tool != "select" && s.UIAction.Tool != "pen" && s.UIAction.Tool != "eraser" {
			return badStep("ui: tool %q", s.UIAction.Tool)
		}
	case "node.add":
		switch s.Kind {
		case "note", "checklist", "kanban", "calendar", "uptime", "orchestrator", "web", "text", "shape", "branches", KindEditor, KindMemory, "file":
		default:
			return badStep("node.add: kind %q", s.Kind)
		}
		return ids("wireTo", s.WireTo, false)
	case "node.update":
		if !idRe.MatchString(s.Tile) {
			return badStep("node.update: tile")
		}
	case "node.delete":
		return ids("tiles", s.Tiles, true)
	case "task.add", "task.update", "task.delete":
		if !idRe.MatchString(s.Tile) || s.Task == nil {
			return badStep("%s: tile", s.Op)
		}
	case "connect", "disconnect":
		if !idRe.MatchString(s.Source) {
			return badStep("%s: source", s.Op)
		}
		return ids("tiles", s.Tiles, true)
	case "console.rename":
		if strings.TrimSpace(s.Name) == "" {
			return badStep("console.rename: name is required")
		}
		return ids("targets", s.Targets, true)
	case "wire":
		if !idRe.MatchString(s.OrchestratorID) {
			return badStep("wire: orchestratorId")
		}
		return ids("targets", s.Targets, true)
	case "loop.create":
		if s.Loop == nil {
			return badStep("loop.create: loop is required")
		}
	case "git":
		switch s.Action {
		case "commitPush", "commit", "push", "pull":
		default:
			return badStep("git: action %q", s.Action)
		}
	case "memory.push":
		return ids("targets", s.Targets, false)
	case "layout.deploy":
		if s.LayoutID == "" {
			return badStep("layout.deploy: layoutId")
		}
	case "reply":
		if strings.TrimSpace(s.Text) == "" {
			return badStep("reply: text")
		}
	}
	return nil
}

// ---- risky patterns (§3.7) ----

// A lead's order goes out without asking unless it matches one of these, so they lean towards
// asking: the cost of a false match is one click.
var defaultRisky = []string{`\brm\s+(-\S+\s+)*\S+`, `\bsudo\b`, `curl[^|\n]*\|\s*(ba)?sh`, `\.env\b`, `--dangerously`, `--yolo`,
	`\b(delete|deletes|deleting|hapus\w*|menghapus\w*|dihapus\w*|penghapusan)\b`,
	`\bdrop\s+(table|database|schema|collection)\b`, `\btruncate\b`, `\b(migrate:(fresh|reset)|db:(drop|reset|wipe))\b`,
	`\bgit\s+push\b[^\n]*\s(-f|--force)`, `\bgit\s+reset\s+--hard\b`, `\bgit\s+clean\s+-`, `\bgit\s+branch\s+-d\b`,
	`\bmkfs\b`, `\bdd\s+if=`}

var (
	riskyMu    sync.Mutex
	riskyCache = map[string]*regexp.Regexp{}
)

// Risky reports whether text matches a built-in or configured pattern.
func Risky(text string, extra []string) bool { return riskyMatch(text, extra) != "" }

// riskyMatch returns the part of text that matches, "" when none does.
func riskyMatch(text string, extra []string) string {
	riskyMu.Lock()
	defer riskyMu.Unlock()
	for _, p := range append(append([]string{}, defaultRisky...), extra...) {
		r, ok := riskyCache[p]
		if !ok {
			var err error
			if r, err = regexp.Compile("(?i)" + p); err != nil {
				continue
			}
			riskyCache[p] = r
		}
		if m := r.FindString(text); m != "" {
			return m
		}
	}
	return ""
}

// ---- confirmations (§3.7) ----

// Confirmation kinds and states.
const (
	ConfDestructive = "destructive"
	ConfDidYouMean  = "didYouMean"
	ConfSendToAll   = "sendToAll"
	ConfRisky       = "risky"
	ConfLoop        = "loop"
	ConfBulkSpawn   = "bulkSpawn"
	ConfProposal    = "proposal" // the lead's dangerous plan, held until the owner agrees

	ConfPending   = "pending"
	ConfConfirmed = "confirmed"
	ConfCancelled = "cancelled"
	ConfExpired   = "expired"
)

var (
	ErrAlreadyDecided = errors.New("the confirmation is already decided")
	ErrConfExpired    = errors.New("the confirmation has expired")
	ErrConfNotFound   = errors.New("confirmation not found")
	ErrBadDecision    = errors.New(`decision must be "confirm" or "cancel"`)
)

// Option is a confirmation button.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Candidate is a console offered by didYouMean / sendToAll.
type Candidate struct {
	SessionID string `json:"sessionId"`
	Handle    string `json:"handle"`
}

// Confirmation is a pending decision in a thread.
type Confirmation struct {
	ID         string      `json:"id"`
	ThreadID   string      `json:"threadId"`
	CommandID  string      `json:"commandId"`
	Kind       string      `json:"kind"`
	Prompt     string      `json:"prompt"`
	Options    []Option    `json:"options"`
	Candidates []Candidate `json:"candidates,omitempty"`
	Steps      []Step      `json:"steps"`
	Plan       []PlanNote  `json:"plan,omitempty"` // a proposal in words
	ExpiresAt  time.Time   `json:"expiresAt"`
	State      string      `json:"state"`
	DecidedBy  string      `json:"decidedBy,omitempty"`
	Choice     string      `json:"choice,omitempty"`
	// hub-side context to execute the steps later
	ctx *execCtx
}

const confirmsFile = "confirmations.json"

// Confirms keeps confirmations in memory and confirmations.json: a question the owner has
// not answered outlives a hub restart, with what it takes to carry it out (§15).
type Confirms struct {
	st      *store.Store
	mu      sync.Mutex
	byID    map[string]*Confirmation
	pending map[string]string // threadId → pending confirmation id
}

// savedConfirm is one entry of confirmations.json.
type savedConfirm struct {
	Confirmation
	Ctx *savedCtx `json:"ctx,omitempty"`
}

// savedCtx is what carrying out a confirmation needs from its command.
type savedCtx struct {
	CommandID   string    `json:"commandId"`
	ThreadID    string    `json:"threadId"`
	ProjectID   string    `json:"projectId"`
	WorkspaceID string    `json:"workspaceId"`
	CardID      string    `json:"cardId,omitempty"`
	ClientID    string    `json:"clientId,omitempty"`
	Source      string    `json:"source,omitempty"`
	By          string    `json:"by,omitempty"`
	Selection   []string  `json:"selection,omitempty"`
	PlannerMode string    `json:"plannerMode,omitempty"`
	Planned     bool      `json:"planned,omitempty"`
	Reply       *ReplyRef `json:"reply,omitempty"`
}

func newConfirms(st *store.Store) *Confirms {
	c := &Confirms{st: st, byID: map[string]*Confirmation{}, pending: map[string]string{}}
	var list []savedConfirm
	if _, err := st.Load(confirmsFile, &list); err != nil {
		return c
	}
	for _, saved := range list {
		cf := saved.Confirmation
		if x := saved.Ctx; x != nil {
			cf.ctx = &execCtx{commandID: x.CommandID, threadID: x.ThreadID, projectID: x.ProjectID, workspaceID: x.WorkspaceID,
				cardID: x.CardID, clientID: x.ClientID, source: x.Source, by: x.By, selection: x.Selection,
				plannerMode: x.PlannerMode, planned: x.Planned, reply: x.Reply}
		}
		c.byID[cf.ID] = &cf
		if cf.State == ConfPending {
			c.pending[cf.ThreadID] = cf.ID
		}
	}
	return c
}

// saveLocked writes every confirmation still in memory. Caller holds mu.
func (c *Confirms) saveLocked() {
	list := make([]savedConfirm, 0, len(c.byID))
	for _, cf := range c.byID {
		saved := savedConfirm{Confirmation: *cf}
		if x := cf.ctx; x != nil {
			saved.Ctx = &savedCtx{CommandID: x.commandID, ThreadID: x.threadID, ProjectID: x.projectID, WorkspaceID: x.workspaceID,
				CardID: x.cardID, ClientID: x.clientID, Source: x.source, By: x.by, Selection: x.selection,
				PlannerMode: x.plannerMode, Planned: x.planned, Reply: x.reply}
		}
		list = append(list, saved)
	}
	_ = c.st.Save(confirmsFile, list)
}

// ForThread returns the thread's confirmations, for a client that was not connected
// when they were published.
func (c *Confirms) ForThread(threadID string) []Confirmation {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Confirmation{}
	for _, cf := range c.byID {
		if cf.ThreadID == threadID {
			cp := *cf
			cp.ctx = nil
			out = append(out, cp)
		}
	}
	return out
}

// Add registers a pending confirmation; an older pending one of the same
// thread is cancelled (returned).
func (c *Confirms) Add(cf *Confirmation, now time.Time, ttl time.Duration) (old *Confirmation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cf.ID == "" {
		cf.ID = auth.RandomID("cf_")
	}
	cf.State = ConfPending
	cf.ExpiresAt = now.Add(ttl).UTC()
	if id, ok := c.pending[cf.ThreadID]; ok {
		if o := c.byID[id]; o != nil && o.State == ConfPending {
			o.State = ConfCancelled
			cp := *o
			old = &cp
		}
	}
	c.byID[cf.ID] = cf
	c.pending[cf.ThreadID] = cf.ID
	c.saveLocked()
	return old
}

// Get returns a copy.
func (c *Confirms) Get(id string) (Confirmation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cf, ok := c.byID[id]
	if !ok {
		return Confirmation{}, false
	}
	return *cf, true
}

// Pending returns the thread's pending confirmation.
func (c *Confirms) Pending(threadID string, now time.Time) (Confirmation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.pending[threadID]
	if !ok {
		return Confirmation{}, false
	}
	cf := c.byID[id]
	if cf == nil || cf.State != ConfPending || !now.Before(cf.ExpiresAt) {
		return Confirmation{}, false
	}
	return *cf, true
}

// Decide applies the first decision; later ones get ErrAlreadyDecided,
// decisions after the TTL ErrConfExpired.
func (c *Confirms) Decide(id, decision, choice, by string, now time.Time) (Confirmation, error) {
	if decision != "confirm" && decision != "cancel" {
		return Confirmation{}, ErrBadDecision
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cf, ok := c.byID[id]
	if !ok {
		return Confirmation{}, ErrConfNotFound
	}
	switch {
	case cf.State == ConfExpired:
		return *cf, ErrConfExpired
	case cf.State != ConfPending:
		return *cf, ErrAlreadyDecided
	case !now.Before(cf.ExpiresAt):
		cf.State = ConfExpired
		c.saveLocked()
		return *cf, ErrConfExpired
	}
	cf.State, cf.DecidedBy, cf.Choice = ConfCancelled, by, choice
	if decision == "confirm" {
		cf.State = ConfConfirmed
	}
	if c.pending[cf.ThreadID] == id {
		delete(c.pending, cf.ThreadID)
	}
	c.saveLocked()
	return *cf, nil
}

// Expire marks every pending confirmation past its TTL as expired.
func (c *Confirms) Expire(now time.Time) []Confirmation {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Confirmation
	changed := false
	for id, cf := range c.byID {
		if cf.State == ConfPending && !now.Before(cf.ExpiresAt) {
			cf.State = ConfExpired
			if c.pending[cf.ThreadID] == id {
				delete(c.pending, cf.ThreadID)
			}
			out = append(out, *cf)
			changed = true
		}
		if cf.State != ConfPending && now.Sub(cf.ExpiresAt) > time.Hour {
			delete(c.byID, id)
			changed = true
		}
	}
	if changed {
		c.saveLocked()
	}
	return out
}

// context of the confirmation (unexported, kept with the record).
func (c *Confirms) ctxOf(id string) *execCtx {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cf, ok := c.byID[id]; ok {
		return cf.ctx
	}
	return nil
}
