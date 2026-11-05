// Package orch is the orchestration "brain" of the hub
// (docs/API-orchestration.md): the command pipeline (local parser →
// optional planner → steps → confirmations → execution), threads and
// orders, groups and orchestrator cards, the Mission Map, handoffs,
// forwards, loops, project memory and layout templates. Text reaches
// agents only through the delivery engine.
//
// The Service talks to the rest of the hub through the Hub interface, so
// it can be tested with a fake hub and a fake clock (Tick).
package orch

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nextboard/internal/agent"
	"nextboard/internal/agentstate"
	"nextboard/internal/board"
	"nextboard/internal/delivery"
	"nextboard/internal/jobs"
	"nextboard/internal/preview"
	"nextboard/internal/project"
	"nextboard/internal/session"
	"nextboard/internal/settings"
	"nextboard/internal/store"
)

// Attention is a loopHeld / loopEnded notification (API-agents §12).
type Attention struct {
	Kind        string
	Title       string
	Body        string
	SessionID   string
	Handle      string
	Agent       string
	ProjectID   string
	WorkspaceID string
}

// Hub is what the orchestration needs from the rest of the hub.
type Hub interface {
	Settings() settings.Settings
	Publish(v any)
	Sessions() []session.Info
	SessionInfo(id string) (session.Info, bool)
	Workspace(wid string) (project.Workspace, project.Project, bool)
	Project(pid string) (project.Project, bool)
	Projects() []project.Project
	Board(wid string) (board.Board, error)
	ApplyOps(wid string, ops []board.Op) error
	Agents() []agent.Agent
	// DefaultAgent is the default agent of a machine (API-agents §1.2).
	DefaultAgent(machineID string) string
	// CreateSession creates a console without placing its node.
	CreateSession(req session.CreateRequest) (session.Info, error)
	DeleteSession(id string) error
	Deliver(sessionID string, req delivery.Request) (delivery.Delivery, error)
	Delivery(id string) (delivery.Delivery, bool)
	CancelDelivery(id string) (delivery.Delivery, error)
	// SwitchAgent restarts a console with another agent or login, in the same
	// place. A sessionID continues that conversation.
	SwitchAgent(id, agentID, profileID, sessionID string, model, effort *string) error
	Pause(ctx context.Context, sessionID string) error
	Resume(ctx context.Context, sessionID string) error
	Restart(sessionID string) error
	Kill(sessionID string) error
	// RenameSession changes a console's name and handle.
	RenameSession(sessionID, name string) error
	Reply(sessionID string) (*session.Reply, bool)
	// TranscriptText is the recent conversation (≤ max chars, "" if none).
	TranscriptText(ctx context.Context, sessionID string, max int) string
	// ScreenText is the last lines of scrollback, ANSI removed.
	ScreenText(sessionID string, lines int) string
	SubmitRunner(req jobs.RunnerRequest) (jobs.Job, error)
	// Exec runs a goal command ($SHELL -lc) on a machine.
	Exec(ctx context.Context, machineID, cwd, script string, timeout time.Duration) (int, error)
	Attention(a Attention)
	// PlannerHost reports whether claude is detected on This PC.
	PlannerHost() (bool, string)
	// AgentOnMachine reports whether an agent CLI is detected on a machine.
	AgentOnMachine(machineID, agentID string) bool
	CreateWorkspace(projectID, name string) (project.Workspace, error)
	RenameWorkspace(wid, name string) (project.Workspace, error)
	CloseWorkspace(wid string) error
	// DocumentText is a document's title and text: the given version, or its newest
	// finished one.
	DocumentText(docID, versionID string) (title, text string, err error)
	// PreviewLog is what a preview's page reported after since, and the latest seq.
	PreviewLog(previewID string, since int64) ([]preview.LogEntry, int64, error)
	// BranchesText lists a project's branches, one per line.
	BranchesText(projectID string) (string, error)
	// AssetPath is where an uploaded image lives on This PC.
	AssetPath(id string) string
}

// Service is the orchestration engine.
type Service struct {
	hub Hub
	// tasksMu guards reading a task board and writing it back (tasks.go).
	tasksMu   sync.Mutex
	tasksLast time.Time
	st        *store.Store
	Now       func() time.Time

	Threads  *Threads
	Orders   *Orders
	Confirms *Confirms
	Loops    *Loops
	Memory   *Memory
	Layouts  *Layouts
	// Git runs command-bar git steps (Phase 7, git.go); set by the hub.
	Git GitActions
	// Previews opens hub previews for "open localhost <port>" (optional).
	Previews PreviewOpener

	mu        sync.Mutex
	plan      plannerState
	thread    map[string]*threadQueue // per thread: planner turn + queued commands
	forwards  map[string]*Forward
	handoffs  map[string]*Handoff
	edgeFired map[string]time.Time
	edgeQueue map[string]*edgeQueued // by edge id: results waiting for the edge's minute
	turnDepth map[string]int         // forward depth of the turn a console runs
	turnOrder map[string]string
	// turnContext: deliveries of context (memory, a tile), not work, whose turn has not ended.
	turnContext map[string][]string
	briefed     map[string]bool // memory autoPush done
	stop        chan struct{}
	once        sync.Once
	wg          sync.WaitGroup
}

// New loads threads, orders, loops, forwards, memory and layouts.
func New(hub Hub, st *store.Store) *Service {
	s := &Service{hub: hub, st: st, Now: time.Now,
		thread: map[string]*threadQueue{}, forwards: map[string]*Forward{}, handoffs: map[string]*Handoff{},
		edgeFired: map[string]time.Time{}, edgeQueue: map[string]*edgeQueued{}, turnDepth: map[string]int{}, turnOrder: map[string]string{},
		turnContext: map[string][]string{}, briefed: map[string]bool{},
		stop: make(chan struct{})}
	s.Threads = newThreads(st)
	s.Threads.OnMessage = func(m ThreadMessage) {
		hub.Publish(map[string]any{"type": "thread", "threadId": m.ThreadID, "message": m})
	}
	s.Orders = newOrders(st)
	s.Confirms = newConfirms(st)
	s.Memory = newMemory(st)
	s.Layouts = newLayouts(st)
	s.Loops = newLoops(st, s)
	s.loadForwards()
	return s
}

// Start marks interrupted orders, recomputes loop schedules and runs the
// 250 ms tick until Close.
func (s *Service) Start() {
	now := s.Now()
	for _, o := range s.Orders.interrupted(now) {
		s.Threads.Add(ThreadMessage{ThreadID: o.ThreadID, Role: "hub", Kind: "note", OrderID: o.ID,
			Text: "Order \"" + clipText(o.Text, 80) + "\" was interrupted by hub restart."})
	}
	s.Orders.Save(now)
	s.Loops.Restart(now)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.Tick(s.Now())
			}
		}
	}()
}

// Close stops the ticker and saves state.
func (s *Service) Close() {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
	s.Orders.Save(s.Now())
	s.saveForwards()
}

// Tick runs every timer of the orchestration (confirmation TTL, planner
// "Still thinking…", report timeouts, forwards, handoffs, loops).
func (s *Service) Tick(now time.Time) {
	for _, cf := range s.Confirms.Expire(now) {
		s.publishConfirm(cf)
		s.Threads.Add(ThreadMessage{ThreadID: cf.ThreadID, Role: "hub", Kind: "note", ConfirmationID: cf.ID, Text: "Confirmation expired; nothing was done."})
	}
	s.plannerTick(now)
	s.ordersTick(now)
	s.tasksTick(now)
	s.forwardsTick(now)
	s.handoffsTick(now)
	s.Loops.Tick(now)
}

func (s *Service) publishConfirm(cf Confirmation) {
	cf.ctx = nil
	s.hub.Publish(map[string]any{"type": "confirmation", "confirmation": cf})
}

// ---- scope and resolution (§3.3, §6) ----

// scope is the world a command is resolved in.
type scope struct {
	proj     project.Project
	ws       project.Workspace // the board the command was typed on
	b        board.Board
	others   map[string]board.Board // main bar: the project's other boards by workspace id
	all      []session.Info         // hub-wide
	reach    []session.Info         // consoles the command may address
	byID     map[string]session.Info
	nodeOf   map[string]*board.Node // sessionId → terminal node
	sidOf    map[string]string      // terminal node id → sessionId
	wsOf     map[string]string      // node id → workspace id
	groups   []*board.Node          // this board's first, then the others in tab order
	card     *board.Node
	directed map[string]bool // card scope: sessions the card directs
}

// newScope builds the world of a command. A card directs consoles of its own
// board; the main command bar reaches every board of the project.
func (s *Service) newScope(wid, cardID string) (*scope, error) {
	ws, proj, ok := s.hub.Workspace(wid)
	if !ok {
		return nil, project.ErrWorkspaceNotFound
	}
	b, err := s.hub.Board(wid)
	if err != nil {
		return nil, err
	}
	sc := &scope{proj: proj, ws: ws, b: b, all: s.hub.Sessions(), byID: map[string]session.Info{},
		nodeOf: map[string]*board.Node{}, sidOf: map[string]string{}, wsOf: map[string]string{}, others: map[string]board.Board{}}
	for _, in := range sc.all {
		sc.byID[in.ID] = in
		// A terminal of the Code workbench's panel is the user's own shell. An
		// agent opened in the editor takes orders like any other.
		if in.Panel && !isAgent(in) {
			continue
		}
		if in.WorkspaceID == wid || (cardID == "" && in.ProjectID == proj.ID) {
			sc.reach = append(sc.reach, in)
		}
	}
	sc.addBoard(wid, b)
	if cardID == "" {
		for _, w := range proj.Workspaces {
			if w.ID == wid {
				continue
			}
			// A board that cannot be read does not stop the command.
			if ob, err := s.hub.Board(w.ID); err == nil {
				sc.others[w.ID] = ob
				sc.addBoard(w.ID, ob)
			}
		}
	}
	for _, n := range b.Nodes {
		if cardID != "" && n.ID == cardID && n.Kind == KindOrchestrator {
			sc.card = n
		}
	}
	if cardID != "" {
		if sc.card == nil {
			return nil, ErrBadCard
		}
		sc.directed = map[string]bool{}
		for _, sid := range sc.wiredSessions(cardID) {
			sc.directed[sid] = true
		}
	}
	return sc, nil
}

func (sc *scope) addBoard(wid string, b board.Board) {
	for _, n := range b.Nodes {
		sc.wsOf[n.ID] = wid
		switch n.Kind {
		case board.KindTerminal:
			sc.nodeOf[n.SessionID()] = n
			sc.sidOf[n.ID] = n.SessionID()
		case KindGroup:
			sc.groups = append(sc.groups, n)
		}
	}
}

// wiredSessions lists consoles wired to a node directly or through a group.
func (sc *scope) wiredSessions(src string) []string {
	var out []string
	wired := func(b board.Board) {
		for _, e := range b.Edges {
			if e.Kind == EdgeWire && e.Source == src {
				out = append(out, sc.nodeSessions(e.Target)...)
			}
		}
	}
	wired(sc.b)
	for _, b := range sc.others {
		wired(b)
	}
	return uniq(out)
}

// boardName is the tab name of a workspace of the project.
func (sc *scope) boardName(wid string) string {
	for _, w := range sc.proj.Workspaces {
		if w.ID == wid {
			return w.Name
		}
	}
	return ""
}

// onOtherBoard reports a node that is not on the board the command came from.
func (sc *scope) onOtherBoard(nodeID string) bool {
	w, ok := sc.wsOf[nodeID]
	return ok && w != sc.ws.ID
}

// boardsNote says how many boards a list of consoles spans, "" for one.
func (sc *scope) boardsNote(sids []string) string {
	seen := map[string]bool{}
	for _, sid := range sids {
		if in, ok := sc.byID[sid]; ok {
			seen[in.WorkspaceID] = true
		}
	}
	if len(seen) < 2 {
		return ""
	}
	return fmt.Sprintf(" (on %d boards)", len(seen))
}

// nodeSessions: a terminal node's session or a group's members' sessions.
func (sc *scope) nodeSessions(nodeID string) []string {
	if sid, ok := sc.sidOf[nodeID]; ok {
		return []string{sid}
	}
	for _, g := range sc.groups {
		if g.ID == nodeID {
			var out []string
			for _, m := range GroupOf(g).Members {
				if sid, ok := sc.sidOf[m]; ok {
					out = append(out, sid)
				}
			}
			return out
		}
	}
	return nil
}

func uniq(l []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range l {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func isAgent(in session.Info) bool { return in.AgentKind == agent.KindAgent }

// takesText reports a console the delivery engine may type into: an agent NextBoard
// started, still in the foreground.
func takesText(in session.Info) bool {
	return isAgent(in) && in.AgentState != agentstate.Shell && !in.ManualAgent
}

// agentsInScope are the agent consoles a command may address.
func (sc *scope) agentsInScope() []session.Info {
	var out []session.Info
	for _, in := range sc.reach {
		if !isAgent(in) {
			continue
		}
		if sc.directed != nil && !sc.directed[in.ID] {
			continue
		}
		out = append(out, in)
	}
	return out
}

// consolesInScope includes shells (kill all).
func (sc *scope) consolesInScope() []session.Info {
	var out []session.Info
	for _, in := range sc.reach {
		if sc.directed != nil && !sc.directed[in.ID] {
			continue
		}
		out = append(out, in)
	}
	return out
}

func (sc *scope) groupByName(name string) *board.Node {
	n := Norm(name)
	for _, g := range sc.groups {
		if Norm(GroupOf(g).Name) == n {
			return g
		}
	}
	return nil
}

func (sc *scope) groupOf(sid string) *board.Node {
	node := sc.nodeOf[sid]
	if node == nil {
		return nil
	}
	for _, g := range sc.groups {
		if slices.Contains(GroupOf(g).Members, node.ID) {
			return g
		}
	}
	return nil
}

// resolution is the outcome of resolving a list of names.
type resolution struct {
	targets  []string            // session ids
	groups   []string            // group node ids named directly
	multi    bool                // a group/role/agent/everyone name was used
	notWired []string            // handles outside the card
	unknown  []string            // names without any match
	fuzzy    map[string][]string // name → candidate session ids
}

func (r resolution) ok() bool {
	return len(r.unknown) == 0 && len(r.fuzzy) == 0 && len(r.notWired) == 0 && len(r.targets) > 0
}

// resolve applies §3.3 to each name: handle (hub-wide; only wired ones in
// a card) → group name → role → agent id; then fuzzy candidates.
func (sc *scope) resolve(names []string) resolution {
	r := resolution{fuzzy: map[string][]string{}}
	add := func(ids ...string) { r.targets = append(r.targets, ids...) }
	for _, raw := range names {
		name := strings.TrimPrefix(strings.TrimSpace(raw), "@")
		n := Norm(name)
		if n == "" {
			continue
		}
		if isEveryone(name) {
			r.multi = true
			for _, in := range sc.agentsInScope() {
				add(in.ID)
			}
			continue
		}
		var hit *session.Info
		for i := range sc.all {
			if Norm(sc.all[i].Handle) == n {
				hit = &sc.all[i]
				break
			}
		}
		if hit != nil {
			if sc.directed != nil && !sc.directed[hit.ID] {
				r.notWired = append(r.notWired, hit.Handle)
				continue
			}
			add(hit.ID)
			continue
		}
		if g := sc.groupByName(name); g != nil {
			r.multi = true
			r.groups = append(r.groups, g.ID)
			for _, sid := range sc.nodeSessions(g.ID) {
				if sc.directed == nil || sc.directed[sid] {
					add(sid)
				}
			}
			continue
		}
		if role := RoleOf(name); role != "" {
			found := false
			for _, g := range sc.groups {
				if GroupOf(g).Role != role {
					continue
				}
				found = true
				for _, sid := range sc.nodeSessions(g.ID) {
					if sc.directed == nil || sc.directed[sid] {
						add(sid)
					}
				}
			}
			if found {
				r.multi = true
				continue
			}
		}
		agentHit := false
		for _, in := range sc.agentsInScope() {
			if Norm(in.Agent) == n || (strings.HasSuffix(n, "s") && Norm(in.Agent) == strings.TrimSuffix(n, "s")) {
				add(in.ID)
				agentHit = true
			}
		}
		if agentHit {
			r.multi = true
			continue
		}
		var cands []string
		for _, in := range sc.all {
			if in.Handle == "" || (sc.directed != nil && !sc.directed[in.ID]) {
				continue
			}
			if Similar(n, Norm(in.Handle)) {
				cands = append(cands, in.ID)
			}
		}
		if len(cands) > 0 {
			r.fuzzy[name] = cands
			continue
		}
		r.unknown = append(r.unknown, name)
	}
	r.targets = uniq(r.targets)
	return r
}

func (sc *scope) handle(sid string) string {
	if in, ok := sc.byID[sid]; ok && in.Handle != "" {
		return in.Handle
	}
	return sid
}

func (sc *scope) handles(sids []string) string {
	var hs []string
	for _, sid := range sids {
		hs = append(hs, "@"+sc.handle(sid))
	}
	return joinAnd(hs)
}

func joinAnd(l []string) string {
	switch len(l) {
	case 0:
		return ""
	case 1:
		return l[0]
	}
	return strings.Join(l[:len(l)-1], ", ") + " and " + l[len(l)-1]
}

// free reports whether a console can take an order now (§3.6): ready,
// no active delivery or order.
func (s *Service) free(in session.Info) bool {
	if !isAgent(in) || in.AgentState != agentstate.Ready || in.Paused || in.Queue > 0 {
		return false
	}
	for _, o := range s.Orders.List(func(o Order) bool { return o.Active() }) {
		for _, t := range o.Targets {
			if t.SessionID == in.ID && !TargetFinal(t.State) {
				return false
			}
		}
	}
	return true
}

func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// sortSessions orders by handle for stable output.
func sortSessions(l []session.Info) {
	sort.SliceStable(l, func(i, j int) bool { return l[i].CreatedAt.Before(l[j].CreatedAt) })
}

func itoa(n int) string { return strconv.Itoa(n) }
