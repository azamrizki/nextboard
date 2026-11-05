package orch

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"sync"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/store"
)

// What memory goes to which agent (docs/API-orchestration.md §12). A memory tile is a
// stack of blocks: the project's own memory, always there, plus what the owner adds from
// all projects or from another project. What an agent holds in its context is what costs,
// so the hub remembers what each console was given and sends only what is new. When
// nothing is new, no turn is used.

const (
	maxMemoryBlocks = 32
	// plannerBlockChars bounds what memory tiles add to a planner's snapshot: the
	// planner runs on every command, and has no tools to look things up with.
	plannerBlockChars = 2000
	criticalKey       = "critical"
	memorySeenFile    = "memory-seen.json"
)

// MemoryBlock is one memory added to a tile or picked for a send. No ProjectID: the
// memory of all projects. Lookup: the agent is told it exists and reads it when needed.
type MemoryBlock struct {
	ProjectID string `json:"projectId,omitempty"`
	Name      string `json:"name"`
	Lookup    bool   `json:"lookup,omitempty"`
}

// MemorySend says what to send to whom.
type MemorySend struct {
	WorkspaceID  string
	MemoryNodeID string        // a tile: its stack, to what it is connected to
	TargetNodes  []string      // with a tile: only through these (a wire was just made)
	Targets      []string      // sessions
	Scopes       []string      // "project", "global", "critical": whole main memories
	Blocks       []MemoryBlock // these memories; empty but not nil means only critical notes
	Force        bool          // also what the agent already has
}

// BriefDoc describes one memory of a stack.
type BriefDoc struct {
	ProjectID string `json:"projectId,omitempty"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Owner     string `json:"owner"`
	Chars     int    `json:"chars"`
	Locked    bool   `json:"locked,omitempty"`
	Lookup    bool   `json:"lookup,omitempty"`
	Missing   bool   `json:"missing,omitempty"`
}

// BriefTarget is what a send would do to one console. Chars 0 and Missing empty: it
// already has everything.
type BriefTarget struct {
	SessionID   string   `json:"sessionId"`
	Handle      string   `json:"handle"`
	Agent       string   `json:"agent"`
	WorkspaceID string   `json:"workspaceId"`
	State       string   `json:"state"`
	CanSend     bool     `json:"canSend"`
	Chars       int      `json:"chars"`
	Lookup      []string `json:"lookup"`
}

// MemoryPlan is a send before it happens, with the sizes the owner decides on.
type MemoryPlan struct {
	Docs    []BriefDoc    `json:"docs"`
	Targets []BriefTarget `json:"targets"`
	Budget  int           `json:"budget"`
}

// ---- what a console already has ----

// seenDoc is what one console has of one memory.
type seenDoc struct {
	Lines []uint32 `json:"lines,omitempty"` // the lines it was given
	Told  uint32   `json:"told,omitempty"`  // the memory as it was when told where to read the rest
}

type seenPending struct {
	DeliveryID string             `json:"deliveryId"`
	Docs       map[string]seenDoc `json:"docs"`
}

type seenSession struct {
	Docs    map[string]seenDoc `json:"docs"`
	Pending []seenPending      `json:"pending,omitempty"`
}

// memorySeen survives a hub restart: consoles do, and what they were told is still in
// their context. Losing it only means a fact is sent twice.
type memorySeen struct {
	mu       sync.Mutex
	st       *store.Store
	sessions map[string]*seenSession
}

func newMemorySeen(st *store.Store) *memorySeen {
	m := &memorySeen{st: st, sessions: map[string]*seenSession{}}
	_, _ = st.Load(memorySeenFile, &m.sessions)
	if m.sessions == nil {
		m.sessions = map[string]*seenSession{}
	}
	return m
}

func (m *memorySeen) save() { _ = m.st.Save(memorySeenFile, m.sessions) }

func (m *memorySeen) session(sid string) *seenSession {
	ss := m.sessions[sid]
	if ss == nil {
		ss = &seenSession{Docs: map[string]seenDoc{}}
		m.sessions[sid] = ss
	}
	if ss.Docs == nil {
		ss.Docs = map[string]seenDoc{}
	}
	return ss
}

// view is what a console has now: a delivery that never reached it does not count, and
// one still in the queue does, so a second send does not repeat it.
func (m *memorySeen) view(sid string, lookup func(string) (delivery.Delivery, bool)) map[string]seenDoc {
	m.mu.Lock()
	defer m.mu.Unlock()
	ss := m.sessions[sid]
	if ss == nil {
		return map[string]seenDoc{}
	}
	if ss.Docs == nil {
		ss.Docs = map[string]seenDoc{}
	}
	var keep []seenPending
	changed := false
	for _, p := range ss.Pending {
		d, ok := lookup(p.DeliveryID)
		switch {
		case !ok || d.State == delivery.Failed || d.State == delivery.Expired || d.State == delivery.Cancelled:
			changed = true
		case d.SentAt != nil:
			for k, v := range p.Docs {
				ss.Docs[k] = v
			}
			changed = true
		default:
			keep = append(keep, p)
		}
	}
	ss.Pending = keep
	if changed {
		m.save()
	}
	out := make(map[string]seenDoc, len(ss.Docs))
	for k, v := range ss.Docs {
		out[k] = v
	}
	for _, p := range keep {
		for k, v := range p.Docs {
			out[k] = v
		}
	}
	return out
}

func (m *memorySeen) sending(sid, deliveryID string, docs map[string]seenDoc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ss := m.session(sid)
	ss.Pending = append(ss.Pending, seenPending{DeliveryID: deliveryID, Docs: docs})
	m.save()
}

// launched starts over: a launch's system prompt holds the latest of everything.
func (m *memorySeen) launched(sid string, docs map[string]seenDoc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[sid] = &seenSession{Docs: docs}
	m.save()
}

// learned adds a line the console wrote itself, which it does not need back.
func (m *memorySeen) learned(sid, key, line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ss := m.sessions[sid]
	if ss == nil {
		return
	}
	d := ss.Docs[key]
	d.Lines = append(d.Lines, lineHash(line))
	ss.Docs[key] = d
	m.save()
}

func (m *memorySeen) forget(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[sid]; ok {
		delete(m.sessions, sid)
		m.save()
	}
}

func lineHash(line string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.TrimSpace(line)))
	return h.Sum32()
}

// isFactLine: a heading says where a fact belongs and is not one itself.
func isFactLine(line string) bool {
	t := strings.TrimSpace(line)
	return t != "" && !strings.HasPrefix(t, "#")
}

func factHashes(content string) []uint32 {
	var out []uint32
	for _, l := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if isFactLine(l) {
			out = append(out, lineHash(l))
		}
	}
	return out
}

// contentHash never gives 0, which means "not told" in a seenDoc.
func contentHash(hashes []uint32) uint32 {
	h := fnv.New32a()
	for _, x := range hashes {
		_, _ = h.Write([]byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)})
	}
	if v := h.Sum32(); v != 0 {
		return v
	}
	return 1
}

// ---- the stack ----

// briefDoc is one memory on its way to an agent.
type briefDoc struct {
	MemoryBlock
	key, title, label, desc, owner, content string
	locked, missing                         bool
}

func memoryKey(owner, name string) string {
	if owner == "" {
		return "global/" + name
	}
	return owner + "/" + name
}

// agentName is how an agent of pid asks memory_read for it.
func (d briefDoc) agentName(pid string) string {
	if d.ProjectID == pid {
		return d.Name
	}
	return d.key
}

// briefDocs reads the memories of a stack. A critical note is never a block: its own
// project's and those of all projects are always sent, and another project's rules
// bind that project.
func (s *Service) briefDocs(pid string, blocks []MemoryBlock) []briefDoc {
	var out []briefDoc
	seen := map[string]bool{}
	for _, bl := range blocks {
		key := memoryKey(bl.ProjectID, bl.Name)
		if bl.Name == "" || bl.Name == CriticalSlug || seen[key] {
			continue
		}
		seen[key] = true
		d := briefDoc{MemoryBlock: bl, key: key, owner: "All projects"}
		if bl.ProjectID != "" {
			p, ok := s.hub.Project(bl.ProjectID)
			if !ok {
				d.missing, d.owner, d.title = true, "Deleted project", bl.Name
				out = append(out, d)
				continue
			}
			d.owner = p.Name
		}
		doc, err := s.ReadDoc(bl.ProjectID, bl.Name)
		if err != nil {
			d.missing, d.title = true, bl.Name
			out = append(out, d)
			continue
		}
		d.title, d.desc, d.content = doc.Title, doc.Description, doc.Content
		switch {
		case bl.ProjectID == pid && bl.Name == MainDoc:
			d.locked = true // has no label: it is the project this agent is working on
		case bl.ProjectID == pid:
			d.label = "Page of this project's memory: " + d.title
		case bl.ProjectID == "":
			d.label = "Memory for all projects: " + d.title
		default:
			d.label = fmt.Sprintf("From the memory of another project, %s: %s. It is about that project, not this one", d.owner, d.title)
		}
		out = append(out, d)
	}
	return out
}

// tileBlocks reads a memory tile's blocks. A tile from before blocks had a scope.
func tileBlocks(n *board.Node) []MemoryBlock {
	var d struct {
		Scope  string        `json:"scope"`
		Blocks []MemoryBlock `json:"blocks"`
	}
	_ = json.Unmarshal(n.Data, &d)
	if d.Blocks == nil && d.Scope == "global" {
		return []MemoryBlock{{Name: MainDoc}}
	}
	return d.Blocks
}

// tileIsManual: the owner sends this tile by hand; a wire or a new block types nothing.
func tileIsManual(n *board.Node) bool {
	var d struct {
		Manual bool `json:"manual"`
	}
	_ = json.Unmarshal(n.Data, &d)
	return d.Manual
}

// tileStack is a tile's memories in order: the project's own always comes first.
func (s *Service) tileStack(pid string, n *board.Node) []briefDoc {
	return s.briefDocs(pid, append([]MemoryBlock{{ProjectID: pid, Name: MainDoc}}, tileBlocks(n)...))
}

// memoryReach walks the wires from a node: consoles, groups, and through orchestrator
// and editor cards to what those are wired to. It returns the nodes reached and the
// sessions among them.
func memoryReach(b board.Board, starts []string) (nodes map[string]bool, sessions []string) {
	byID := map[string]*board.Node{}
	for _, n := range b.Nodes {
		byID[n.ID] = n
	}
	nodes = map[string]bool{}
	stack := slices.Clone(starts)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := byID[id]
		if n == nil || nodes[id] {
			continue
		}
		nodes[id] = true
		switch n.Kind {
		case board.KindTerminal, KindGroup:
			sessions = append(sessions, nodeSessionIDs(b, id)...)
		case KindOrchestrator, KindEditor:
			stack = append(stack, wireTargets(b, id)...)
		}
	}
	return nodes, uniq(sessions)
}

func wireTargets(b board.Board, id string) []string {
	var out []string
	for _, e := range b.Edges {
		if e.Kind == EdgeWire && e.Source == id {
			out = append(out, e.Target)
		}
	}
	return out
}

// attachedBlocks is what the memory tiles connected to a console add to its memory.
func (s *Service) attachedBlocks(sid, wid string) []MemoryBlock {
	if sid == "" || wid == "" {
		return nil
	}
	b, err := s.hub.Board(wid)
	if err != nil {
		return nil
	}
	var out []MemoryBlock
	for _, n := range b.Nodes {
		if n.Kind != KindMemory {
			continue
		}
		if _, sessions := memoryReach(b, wireTargets(b, n.ID)); slices.Contains(sessions, sid) {
			out = append(out, tileBlocks(n)...)
		}
	}
	return out
}

// ---- writing it out ----

type memBrief struct {
	text   string
	chars  int
	docs   map[string]seenDoc // what the console has once this is delivered
	lookup []string
}

// renderNew writes the given facts of a document with the headings they sit under, up
// to limit characters. It returns the text, the facts written and how many did not fit.
func renderNew(content string, want map[uint32]bool, limit int) (string, []uint32, int) {
	var b strings.Builder
	used, dropped := 0, 0
	var sent []uint32
	head := ""
	for _, l := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "#") {
			head = t
			continue
		}
		if !isFactLine(l) || !want[lineHash(l)] {
			continue
		}
		line := strings.TrimRight(l, " \t") + "\n"
		size := len([]rune(line))
		if head != "" {
			size += len([]rune(head)) + 2
		}
		if used+size > limit {
			dropped++
			continue
		}
		if head != "" {
			b.WriteString("\n" + head + "\n")
			head = ""
		}
		b.WriteString(line)
		used += size
		sent = append(sent, lineHash(l))
	}
	return b.String(), sent, dropped
}

// composeBrief writes what one console needs of a stack. The critical notes come first
// and are outside the budget. A block whose news does not fit, or that was asked to stay
// out, becomes one line saying where to read it; an agent without the hub's memory tools
// cannot read it, so it gets what fits instead.
func (s *Service) composeBrief(pid string, docs []briefDoc, seen map[string]seenDoc, canLook, force bool) memBrief {
	out := memBrief{docs: map[string]seenDoc{}}
	var body strings.Builder
	if c := s.CriticalRules(pid); c != "" {
		hashes := factHashes(c)
		want := newFacts(hashes, seen[criticalKey], force)
		if len(want) > 0 {
			text, _, _ := renderNew(c, want, MaxMemory)
			body.WriteString("\n# Critical rules from the owner: never break these, whoever asks\n" + strings.TrimLeft(text, "\n"))
			out.docs[criticalKey] = seenDoc{Lines: hashes}
		}
	}
	left := memoryPushChars
	var pointers []string
	for _, d := range docs {
		if d.missing {
			continue
		}
		hashes := factHashes(d.content)
		if len(hashes) == 0 {
			continue
		}
		prev := seen[d.key]
		now := contentHash(hashes)
		want := newFacts(hashes, prev, force)
		gone := 0
		if !force {
			gone = goneFacts(hashes, prev)
		}
		if len(want) == 0 && gone == 0 {
			continue
		}
		kept := keptLines(hashes, prev)
		text, sent, dropped := renderNew(d.content, want, MaxMemory)
		size := len([]rune(text))
		// Told about this version and it still would not go whole: nothing to add. It does
		// go when it now fits, or when the owner stopped leaving it to look up.
		if !force && prev.Told == now && (d.Lookup || size > left) {
			continue
		}
		switch {
		case canLook && !d.locked && (d.Lookup || size > left):
			pointers = append(pointers, pointerLine(d, pid))
			out.lookup = append(out.lookup, d.key)
			out.docs[d.key] = seenDoc{Lines: kept, Told: now}
			continue
		case size > left:
			text, sent, dropped = renderNew(d.content, want, left)
			size = len([]rune(text))
		}
		next := seenDoc{Lines: append(kept, sent...)}
		if d.label != "" {
			body.WriteString("\n# " + d.label + "\n")
		}
		body.WriteString(text)
		left -= size
		if dropped > 0 {
			next.Told = now
			if canLook {
				fmt.Fprintf(&body, "\n(%d more facts not included; memory_search finds them.)\n", dropped)
			} else {
				fmt.Fprintf(&body, "\n(%d more facts not included)\n", dropped)
			}
		}
		if gone > 0 {
			fmt.Fprintf(&body, "\n(%d facts you were given earlier from this memory are gone: do not rely on them.)\n", gone)
		}
		out.docs[d.key] = next
	}
	if len(pointers) > 0 {
		body.WriteString("\nMore memory, left out to save space. When your work touches it, read it with the memory_read tool of the nextboard MCP server and the name:\n")
		body.WriteString(strings.Join(pointers, "\n") + "\n")
	}
	if strings.TrimSpace(body.String()) == "" {
		return out
	}
	out.text = MemoryPushHead + "\n" + strings.TrimRight(body.String(), "\n")
	out.chars = len([]rune(out.text))
	return out
}

func pointerLine(d briefDoc, pid string) string {
	l := "- " + d.agentName(pid) + ": " + d.title
	if d.ProjectID != "" && d.ProjectID != pid {
		l += ", from project " + d.owner
	}
	if d.desc != "" {
		l += ". " + d.desc
	}
	return l
}

func newFacts(hashes []uint32, prev seenDoc, force bool) map[uint32]bool {
	want := map[uint32]bool{}
	for _, h := range hashes {
		if force || !slices.Contains(prev.Lines, h) {
			want[h] = true
		}
	}
	return want
}

func goneFacts(hashes []uint32, prev seenDoc) int {
	n := 0
	for _, h := range prev.Lines {
		if !slices.Contains(hashes, h) {
			n++
		}
	}
	return n
}

// keptLines is what the console has that is still true.
func keptLines(hashes []uint32, prev seenDoc) []uint32 {
	var out []uint32
	for _, h := range prev.Lines {
		if slices.Contains(hashes, h) {
			out = append(out, h)
		}
	}
	return out
}

// ---- sending ----

// resolveSend turns a send into memories and consoles. A tile sends only to what it is
// connected to: a tile connected to nothing sends to nothing.
func (s *Service) resolveSend(pid string, req MemorySend) ([]briefDoc, []string, error) {
	var docs []briefDoc
	targets := slices.Clone(req.Targets)
	if req.MemoryNodeID != "" {
		if req.WorkspaceID == "" { // no board of this project holds it
			return nil, nil, ErrTileNotFound
		}
		b, err := s.hub.Board(req.WorkspaceID)
		if err != nil {
			return nil, nil, err
		}
		var node *board.Node
		for _, n := range b.Nodes {
			if n.ID == req.MemoryNodeID && n.Kind == KindMemory {
				node = n
			}
		}
		if node == nil {
			return nil, nil, ErrTileNotFound
		}
		docs = s.tileStack(pid, node)
		from := req.TargetNodes
		if len(from) == 0 {
			from = wireTargets(b, node.ID)
		}
		_, sessions := memoryReach(b, from)
		targets = append(targets, sessions...)
	} else {
		if len(targets) == 0 {
			for _, in := range s.hub.Sessions() {
				if in.WorkspaceID == req.WorkspaceID || (req.WorkspaceID == "" && in.ProjectID == pid) {
					targets = append(targets, in.ID)
				}
			}
		}
		blocks := req.Blocks
		if blocks == nil {
			scopes := req.Scopes
			if len(scopes) == 0 {
				scopes = []string{"project", "global"}
			}
			blocks = []MemoryBlock{}
			for _, sc := range scopes {
				switch sc {
				case "project":
					blocks = append(blocks, MemoryBlock{ProjectID: pid, Name: MainDoc})
				case "global":
					blocks = append(blocks, MemoryBlock{Name: MainDoc})
				case CriticalSlug:
				default:
					return nil, nil, ErrBadScope
				}
			}
		}
		docs = s.briefDocs(pid, blocks)
	}
	return docs, uniq(targets), nil
}

// consoleCanLook reports whether a console has the hub's memory tools: those go in with
// the launch context.
func (s *Service) consoleCanLook(agentID string) bool {
	a, ok := agentByID(s.hub.Agents(), agentID)
	return ok && a.TakesContext()
}

func (s *Service) seenBy(sid string) map[string]seenDoc {
	return s.seen.view(sid, s.hub.Delivery)
}

// SendMemory types memory into agents, each getting only what it does not have.
func (s *Service) SendMemory(pid string, req MemorySend) (PushResult, error) {
	res := PushResult{DeliveryIDs: []string{}}
	docs, targets, err := s.resolveSend(pid, req)
	if err != nil {
		return res, err
	}
	empty := s.CriticalRules(pid) == ""
	for _, d := range docs {
		if strings.TrimSpace(d.content) != "" {
			empty = false
		}
	}
	if empty {
		return res, ErrMemoryEmpty
	}
	for _, sid := range targets {
		in, ok := s.hub.SessionInfo(sid)
		if !ok {
			continue
		}
		if !takesText(in) {
			res.NotAgent++
			continue
		}
		br := s.composeBrief(pid, docs, s.seenBy(sid), s.consoleCanLook(in.Agent), req.Force)
		if br.text == "" {
			res.UpToDate++
			continue
		}
		before := len(res.DeliveryIDs)
		s.deliverContext(&res, []string{sid}, br.text, SourceMemory)
		if len(res.DeliveryIDs) > before {
			s.seen.sending(sid, res.DeliveryIDs[before], br.docs)
			res.Chars += br.chars
		}
	}
	if req.MemoryNodeID != "" {
		lp, _ := json.Marshal(map[string]any{"lastPush": map[string]any{"at": s.Now().UTC(), "delivered": res.Delivered, "busy": res.Busy, "skipped": res.Skipped, "upToDate": res.UpToDate}})
		_ = s.hub.ApplyOps(req.WorkspaceID, []board.Op{{Op: "patch", ID: req.MemoryNodeID, Set: map[string]json.RawMessage{"data": lp}}})
	}
	return res, nil
}

// PlanMemory answers what SendMemory would do, and sends nothing.
func (s *Service) PlanMemory(pid string, req MemorySend) (MemoryPlan, error) {
	plan := MemoryPlan{Docs: []BriefDoc{}, Targets: []BriefTarget{}, Budget: memoryPushChars}
	docs, targets, err := s.resolveSend(pid, req)
	if err != nil {
		return plan, err
	}
	for _, d := range docs {
		plan.Docs = append(plan.Docs, BriefDoc{ProjectID: d.ProjectID, Name: d.Name, Title: d.title, Owner: d.owner, Chars: len([]rune(d.content)),
			Locked: d.locked && req.MemoryNodeID != "", Lookup: d.Lookup, Missing: d.missing})
	}
	for _, sid := range targets {
		in, ok := s.hub.SessionInfo(sid)
		if !ok || !isAgent(in) {
			continue
		}
		t := BriefTarget{SessionID: sid, Handle: in.Handle, Agent: in.Agent, WorkspaceID: in.WorkspaceID, State: in.AgentState, Lookup: []string{},
			CanSend: takesText(in) && !in.Paused && in.AgentState != agentstate.NeedsYou && in.AgentState != agentstate.Exited && in.AgentState != agentstate.Error}
		br := s.composeBrief(pid, docs, s.seenBy(sid), s.consoleCanLook(in.Agent), req.Force)
		t.Chars = br.chars
		if br.lookup != nil {
			t.Lookup = br.lookup
		}
		plan.Targets = append(plan.Targets, t)
	}
	return plan, nil
}

// ---- launch, tools, planner ----

// LaunchMemory is MemoryPrompt for one console: with what the tiles connected to it add,
// and the note of what it now has, so nothing here is typed to it again.
func (s *Service) LaunchMemory(sid, wid, pid string, nativeMemory bool) string {
	text, docs := s.memoryPrompt(pid, nativeMemory, s.briefDocs(pid, s.attachedBlocks(sid, wid)))
	s.seen.launched(sid, docs)
	return text
}

// attachedPrompt writes a launch's added blocks within what the budget has left, and
// returns the note of what the prompt holds. Main memories of this project and of all
// projects are in the prompt already.
func attachedPrompt(pid string, docs []briefDoc, left int, nativeMemory bool, seen map[string]seenDoc) (inline, pointers string) {
	var body, list strings.Builder
	for _, d := range docs {
		if d.missing || strings.TrimSpace(d.content) == "" {
			continue
		}
		hashes := factHashes(d.content)
		if d.Name == MainDoc && (d.ProjectID == "" || d.ProjectID == pid) {
			continue
		}
		// without a memory folder of its own, a console has this project's notes here already
		if d.ProjectID == pid && strings.HasPrefix(d.Name, NotePrefix) && !nativeMemory {
			seen[d.key] = seenDoc{Lines: hashes}
			continue
		}
		size := len([]rune(d.content))
		if d.Lookup || size > left {
			list.WriteString(pointerLine(d, pid) + "\n")
			seen[d.key] = seenDoc{Told: contentHash(hashes)}
			continue
		}
		fmt.Fprintf(&body, "\n# %s\n%s\n", d.label, strings.TrimSpace(d.content))
		seen[d.key] = seenDoc{Lines: hashes}
		left -= size
	}
	return body.String(), list.String()
}

// attachedDocName resolves a name an agent asks for against what its tiles added.
func attachedDocName(name string, blocks []MemoryBlock) (owner, doc string, ok bool) {
	o, d, cut := strings.Cut(name, "/")
	if !cut {
		return "", "", false
	}
	for _, bl := range blocks {
		if bl.ProjectID != "" && bl.ProjectID == o && bl.Name == d && d != CriticalSlug {
			return o, d, true
		}
	}
	return "", "", false
}

// MemoryReadFor answers memory_read for a console: like MemoryReadText, and it can also
// read what the tiles connected to it added from other projects, and nothing else of them.
func (s *Service) MemoryReadFor(sid, wid, pid, name, topic string) (string, error) {
	blocks := s.attachedBlocks(sid, wid)
	if name == "" && topic == "" {
		text := s.MemoryTextFor(pid)
		if idx := s.attachedIndex(pid, blocks); idx != "" {
			text += "\n\nAdded by the owner, read one with memory_read and its name:\n" + idx
		}
		return text, nil
	}
	if name == "" {
		return s.MemorySearchFor(sid, wid, pid, topic, 0), nil
	}
	if owner, doc, ok := attachedDocName(name, blocks); ok {
		return s.memoryDocText(owner, name, doc, topic)
	}
	return s.MemoryReadText(pid, name, topic)
}

// attachedIndex lists what the tiles add from other projects. The rest is on memoryIndex.
func (s *Service) attachedIndex(pid string, blocks []MemoryBlock) string {
	var b strings.Builder
	for _, d := range s.briefDocs(pid, blocks) {
		if !d.missing && d.ProjectID != "" && d.ProjectID != pid {
			b.WriteString(pointerLine(d, pid) + "\n")
		}
	}
	return b.String()
}

// MemorySearchFor answers memory_search for a console: this project, all projects, and
// what its tiles added from other projects.
func (s *Service) MemorySearchFor(sid, wid, pid, query string, limit int) string {
	blocks := s.attachedBlocks(sid, wid)
	var extra []memDoc
	for _, d := range s.briefDocs(pid, blocks) {
		if !d.missing && d.ProjectID != "" && d.ProjectID != pid && strings.TrimSpace(d.content) != "" {
			extra = append(extra, memDoc{projectID: d.ProjectID, project: d.owner, name: d.key, title: d.title, desc: d.desc, content: d.content})
		}
	}
	if len(extra) == 0 {
		return s.MemorySearchText(pid, query, limit)
	}
	return s.memorySearchText(pid, query, limit, extra, s.attachedIndex(pid, blocks))
}

// MemorySaved notes a fact a console saved through memory_save: it needs no copy back.
func (s *Service) MemorySaved(sid, pid, doc, fact string) {
	owner, name := pid, MainDoc
	if doc != "" && doc != MainDoc {
		owner, name = splitDocName(pid, doc)
	}
	s.seen.learned(sid, memoryKey(owner, name), "- "+cleanFact(fact))
}

// MemoryForgotten drops what a console was noted to have: a CLI that takes nothing at
// launch starts its conversation without any of it.
func (s *Service) MemoryForgotten(sid string) { s.seen.forget(sid) }

// plannerBlocks is what memory tiles connected to the orchestrator add for the planner.
// The main command bar is the same lead, so it gets what any orchestrator card on the
// board was given.
func (s *Service) plannerBlocks(sc *scope) string {
	var blocks []MemoryBlock
	for _, n := range sc.b.Nodes {
		if n.Kind != KindMemory {
			continue
		}
		nodes, _ := memoryReach(sc.b, wireTargets(sc.b, n.ID))
		reached := false
		for _, o := range sc.b.Nodes {
			if o.Kind == KindOrchestrator && nodes[o.ID] && (sc.card == nil || sc.card.ID == o.ID) {
				reached = true
			}
		}
		if reached {
			blocks = append(blocks, tileBlocks(n)...)
		}
	}
	var b strings.Builder
	left := plannerBlockChars
	for _, d := range s.briefDocs(sc.proj.ID, blocks) {
		if d.missing || strings.TrimSpace(d.content) == "" || (d.Name == MainDoc && (d.ProjectID == "" || d.ProjectID == sc.proj.ID)) {
			continue
		}
		text := strings.TrimSpace(d.content)
		if r := []rune(text); len(r) > left {
			fmt.Fprintf(&b, "# %s\n(%d characters, not included)\n", d.label, len(r))
			continue
		}
		fmt.Fprintf(&b, "# %s\n%s\n", d.label, text)
		left -= len([]rune(text))
	}
	return strings.TrimSpace(b.String())
}

// pushMemoryTile is the tile push of a memory tile. Without targets the owner asked;
// with them a wire was just made, which a tile kept by hand does not act on.
func (s *Service) pushMemoryTile(wid, pid string, n *board.Node, targetNodes []string) (TilePush, error) {
	res := TilePush{PushResult: PushResult{DeliveryIDs: []string{}}}
	if len(targetNodes) > 0 && tileIsManual(n) {
		return res, nil
	}
	r, err := s.SendMemory(pid, MemorySend{WorkspaceID: wid, MemoryNodeID: n.ID, TargetNodes: targetNodes})
	if errors.Is(err, ErrMemoryEmpty) {
		res.Empty = true
		return res, nil
	}
	res.PushResult = r
	return res, err
}
