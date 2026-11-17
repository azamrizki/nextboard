package server

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/chats"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/transcript"
)

// Agent chats (docs/API-agents.md §29): every conversation an agent console had in a project,
// at the place it ran, so it can be resumed there. The index (internal/chats) is told when a
// console's conversation becomes known or changes (OnChange), when a turn ends (OnAttention
// finished) and when a console is deleted (OnRemove); GET …/chats also looks in the project's
// places on disk for conversations from before (the backfill). Handlers are in h_chats.go.

const (
	// chatScanEvery: the backfill of a project runs at most this often.
	chatScanEvery = time.Minute
	// chatScanBudget bounds one backfill.
	chatScanBudget = 6 * time.Second
	// chatScanPerPlace: the newest Claude conversations read per folder.
	chatScanPerPlace = 50
	// chatReadyWithin: how long a resumed console gets to be ready for what it is told.
	chatReadyWithin = 150 * time.Second
	// chatTextMax bounds the conversation a chat continued elsewhere is summarised from.
	chatTextMax = 60000
	// chatCommitsMax and chatFactsMax bound what a resumed chat is told.
	chatCommitsMax = 20
	chatFactsMax   = 20
)

type chatHub struct {
	idx *chats.Index

	mu        sync.Mutex
	scannedAt map[string]time.Time
	// read: the transcript files the backfill has read for a project ("<pid>\x00<path>"), at the
	// mtime they had
	read map[string]time.Time
	// resuming: chats a request is resuming now
	resuming map[string]bool
	// memSeen: the memory files' mtimes when the index last looked at them, per project
	memSeen map[string]string

	// turnMu: one turn's end at a time, so a mark is never counted from twice
	turnMu sync.Mutex

	// summarize runs the summary job of a chat continued elsewhere (tests give a fake).
	summarize func(jobs.RunnerRequest) (jobs.Job, error)
	// readyWithin: see chatReadyWithin (tests shorten it).
	readyWithin time.Duration
}

// setupChats opens the index and follows the consoles.
func (s *Server) setupChats() {
	idx, err := chats.Open(s.st.Path("chats"))
	if err != nil {
		log.Printf("chats: %v", err)
		return
	}
	s.chats = &chatHub{idx: idx, scannedAt: map[string]time.Time{}, read: map[string]time.Time{}, resuming: map[string]bool{},
		memSeen: map[string]string{}, summarize: s.submitRunner, readyWithin: chatReadyWithin}
	onChange := s.sessions.OnChange
	s.sessions.OnChange = func(in session.Info) {
		if onChange != nil {
			onChange(in)
		}
		s.chatSeen(in)
	}
	onRemove := s.sessions.OnRemove
	s.sessions.OnRemove = func(sp session.Spec) {
		if onRemove != nil {
			onRemove(sp)
		}
		if c, ok := s.chats.idx.Release(sp.ID, sp.Handle); ok {
			s.publishChats([]chats.Chat{c})
		}
	}
	onAttention := s.sessions.OnAttention
	s.sessions.OnAttention = func(in session.Info, ev agentstate.Event) {
		if onAttention != nil {
			onAttention(in, ev)
		}
		if ev.Kind == agentstate.EvFinished && chatConsole(in) {
			// counted now, before the console can be deleted or move on (chatTurnEnded)
			s.chats.idx.BeginTurn(in.ID)
			go s.chatTurnEnded(in)
		}
	}
}

// chatConsole reports a console whose conversations are chats: an agent console of a project,
// not a profile's sign-in console. Hidden runner jobs are no consoles at all.
func chatConsole(in session.Info) bool {
	return in.ProjectID != "" && in.AgentKind == agent.KindAgent && !in.SignIn
}

// holdingOf is what the index is told about a console.
func (s *Server) holdingOf(in session.Info, a agent.Agent, partName bool) chats.Holding {
	h := chats.Holding{SessionID: in.ID, Kind: a.Transcript, Chat: chats.Chat{ProjectID: in.ProjectID, Agent: in.Agent, ProfileID: in.ProfileID,
		ProviderID: in.ProviderID, PartID: in.PartID, Root: in.Root, MachineID: in.MachineID, RemoteMachineID: in.RemoteMachineID,
		RemoteCwd: in.RemoteCwd, Cwd: in.Cwd, Handle: in.Handle, WorkspaceID: in.WorkspaceID}}
	if h.Kind == agent.TranscriptNone {
		h.Kind = ""
	}
	switch a.Resume.Kind {
	case agent.ResumeSessionID:
		h.AgentSessionID = in.AgentSessionID
	case agent.ResumeContinue:
		h.Imprecise = true
	}
	if partName && in.PartID != "" {
		if p, ok := s.projects.Get(in.ProjectID); ok {
			if pt, ok := p.PartByID(in.PartID); ok {
				h.PartName = pt.Name
			}
		}
	}
	return h
}

// chatSeen follows a console's conversation: the one it has now is its chat, the one it had
// before is let go. An agent without session ids has a chat only once a turn ended there. A
// console that is no agent console any more (restarted as a plain shell) lets go of its chat.
func (s *Server) chatSeen(in session.Info) {
	if s.chats == nil {
		return
	}
	if !chatConsole(in) {
		if s.chats.idx.HeldBy(in.ID) != "" {
			if c, ok := s.chats.idx.Release(in.ID, in.Handle); ok {
				s.publishChats([]chats.Chat{c})
			}
		}
		return
	}
	a, ok := s.agents.Get(in.Agent)
	if !ok {
		return
	}
	h := s.holdingOf(in, a, false)
	key, held := h.Key(), s.chats.idx.HeldBy(in.ID)
	if h.Imprecise && held != key {
		h.Imprecise, key = false, "" // nothing to record before its first turn; what it had is let go
	}
	if key == "" && held == "" {
		return
	}
	if key != "" && held != key {
		h = s.holdingOf(in, a, true)
	}
	changed, fresh := s.chats.idx.Hold(h)
	if fresh && s.orch != nil {
		if m, ok := s.orch.MissionOf(in.ID); ok {
			s.chats.idx.SetMission(key, m.ID)
		}
	}
	s.publishChats(changed)
}

// chatTurnEnded counts the messages of a turn of a console, and gives a new chat its title. in is
// the console when its turn ended (BeginTurn was called then): the chat counted is the one it had
// then, even when the console has moved on to another conversation or was deleted since.
func (s *Server) chatTurnEnded(in session.Info) {
	ch := s.chats
	key := ""
	defer func() { ch.idx.EndTurn(in.ID, key) }()
	a, ok := s.agents.Get(in.Agent)
	if !ok || (a.Resume.Kind != agent.ResumeSessionID && a.Resume.Kind != agent.ResumeContinue) {
		return
	}
	time.Sleep(400 * time.Millisecond) // the CLI writes the end of its turn
	ch.turnMu.Lock()
	defer ch.turnMu.Unlock()
	h := s.holdingOf(in, a, true)
	key = h.Key()
	if key == "" {
		return
	}
	at := time.Now()
	// what is in the memory now was there for this turn: only what comes after is news to it
	s.observeMemory(in.ProjectID)
	cur, alive := s.sessions.Info(in.ID)
	same := alive && chatConsole(cur) && cur.Agent == in.Agent && s.holdingOf(cur, a, false).Key() == key
	if same {
		changed, _ := ch.idx.Hold(s.holdingOf(cur, a, true))
		s.publishChats(changed)
	} else {
		ch.idx.Note(h) // gone before the index heard of its conversation: kept without a console
	}
	e, ok := ch.idx.Entry(key)
	if !ok {
		return
	}
	t := chats.Turn{At: at}
	if e.Imprecise || (a.Transcript != agent.TranscriptClaude && a.Transcript != agent.TranscriptCodex) {
		t.New, t.Since = 2, true // a prompt and its answer: the CLI keeps nothing to count
		p, _ := s.projects.Get(e.ProjectID)
		t.Title = a.Name + " in " + placeLabelOf(p, e)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		var host machine.Host
		var kind, p string
		if sess, ok := s.sessions.Get(in.ID); ok && same {
			host, kind, p, _ = sess.TranscriptLocation(ctx)
		}
		if p == "" { // the console is gone or has another conversation: the chat's own file
			host, kind, p, _ = s.chatTranscript(ctx, e.Chat)
		}
		if p != "" {
			if n, err := transcript.CountAfter(ctx, host, kind, p, e.Mark); err == nil {
				t.New, t.Since, t.Next = n.New, n.Since, n.Next
			}
			t.TranscriptPath = p
			if e.Title == "" && !e.Renamed {
				if d, err := transcript.Describe(ctx, host, kind, p); err == nil {
					t.Title, t.StartedAt = d.Title, d.StartedAt
				}
			}
		}
		cancel()
	}
	if s.orch != nil && same {
		if m, ok := s.orch.MissionOf(in.ID); ok {
			t.MissionID = m.ID
		}
	}
	if c, ok := ch.idx.Ended(e.ID, t); ok {
		s.publishChats([]chats.Chat{c})
	}
}

// publishChats sends {"type":"chat"} for each chat that changed and has something to show.
func (s *Server) publishChats(list []chats.Chat) {
	for _, c := range list {
		e, ok := s.chats.idx.Entry(c.ID)
		if !ok || e.Messages == 0 {
			continue
		}
		p, _ := s.projects.Get(e.ProjectID)
		s.events.Publish(map[string]any{"type": "chat", "chat": s.chatViewOf(p, e)})
	}
}

// ---- memory ----

// memoryDocs reads a project's main memory and its parts' pages straight from their files
// ("main" and part ids: memory/<pid>/MEMORY.md, memory/subs/<pid>/part-….md), and a signature of
// their mtimes. Reading them through orch would make the folders and critical notes.
func (s *Server) memoryDocs(pid string) (chats.MemoryDocs, string) {
	docs := chats.MemoryDocs{}
	var sig strings.Builder
	// a file that is not there is read as empty; one that cannot be read is left out, so the
	// index keeps what it knew of it (chats.ObserveMemory)
	read := func(key, p string) {
		fi, err := os.Stat(p)
		if os.IsNotExist(err) {
			docs[key] = ""
			sig.WriteString(key + "@none;")
			return
		}
		if err != nil {
			return
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return
		}
		docs[key] = string(b)
		sig.WriteString(key + "@" + fi.ModTime().UTC().Format(time.RFC3339Nano) + ";")
	}
	read("main", s.st.Path("memory", pid, "MEMORY.md"))
	if p, ok := s.projects.Get(pid); ok && len(p.Parts) > 1 {
		for _, pt := range p.Parts {
			read(pt.ID, s.st.Path("memory", "subs", pid, orch.PartSlug(pt.ID)+".md"))
		}
	}
	return docs, sig.String()
}

// observeMemory notes the lines of the project's memory not seen before (chats.ObserveMemory),
// when a file changed since the last look.
func (s *Server) observeMemory(pid string) {
	docs, sig := s.memoryDocs(pid)
	ch := s.chats
	ch.mu.Lock()
	same := ch.memSeen[pid] == sig && sig != ""
	ch.memSeen[pid] = sig
	ch.mu.Unlock()
	if !same {
		ch.idx.ObserveMemory(pid, docs, time.Now())
	}
}

// ---- views ----

// chatView is a chat as the API shows it (docs/API-agents.md §29).
type chatView struct {
	chats.Chat
	Status string `json:"status"`
	// SessionID is the console that has it (live or asleep).
	SessionID   string `json:"sessionId,omitempty"`
	PlaceLabel  string `json:"placeLabel"`
	PartName    string `json:"partName,omitempty"`
	MachineName string `json:"machineName,omitempty"`
	// Precise: false for an agent without session ids, whose resume continues its last
	// conversation in that folder.
	Precise bool `json:"precise"`
	// OrphanReason says why an orphan cannot resume where it started.
	OrphanReason string `json:"orphanReason,omitempty"`
}

func (s *Server) machineName(id string) string {
	if h, ok := s.machines.Host(id); ok {
		return h.Info().Name
	}
	return id
}

func (s *Server) chatPlaces(p project.Project) chats.Places {
	return chats.Places{RootDir: s.roots.Dir(p.ID), WorkPath: s.remoteWorkPath, DirExists: func(dir string) bool {
		fi, err := os.Stat(dir)
		return err == nil && fi.IsDir()
	}}
}

// placeLabelOf is "Root", the part's name now (or when it last ran, once it is gone), or the
// folder of a console in no part.
func placeLabelOf(p project.Project, e chats.Entry) string {
	switch {
	case e.Root:
		return "Root"
	case e.PartID != "":
		if pt, ok := p.PartByID(e.PartID); ok {
			return pt.Name
		}
		if e.PartName != "" {
			return e.PartName
		}
		return "part"
	}
	return path.Base(firstSet(e.RemoteCwd, e.Cwd))
}

func (s *Server) chatViewOf(p project.Project, e chats.Entry) chatView {
	v := chatView{Chat: e.Chat, Precise: !e.Imprecise, PlaceLabel: placeLabelOf(p, e)}
	if v.Title == "" {
		v.Title = "Untitled chat"
	}
	machineID := firstSet(e.RemoteMachineID, e.MachineID)
	if pt, ok := p.PartByID(e.PartID); ok && e.PartID != "" {
		v.PartName, machineID = pt.Name, pt.MachineID
	} else if e.PartID != "" {
		v.PartName = e.PartName
	}
	if e.Root {
		machineID = machine.LocalID
	}
	v.MachineName = s.machineName(machineID)
	if e.Holder != "" {
		if in, ok := s.sessions.Info(e.Holder); ok {
			v.Status, v.SessionID = chats.StatusLive, e.Holder
			if in.Asleep || in.Status == session.StatusAsleep || in.AgentState == agentstate.Asleep {
				v.Status = chats.StatusAsleep
			}
		}
	}
	if v.Status == "" {
		if p.ID == "" {
			v.Status, v.OrphanReason = chats.StatusOrphan, chats.OrphanPartRemoved
		} else if r := chats.Orphan(e.Chat, p, s.chatPlaces(p)); r != "" {
			v.Status, v.OrphanReason = chats.StatusOrphan, r
		} else {
			v.Status = chats.StatusClosed
		}
	}
	return v
}

// handleTakenView: the console that has the chat's old handle now (docs/API-agents.md §29).
type handleTakenView struct {
	Taken     string  `json:"taken"`
	By        string  `json:"by"`
	ByHandle  string  `json:"byHandle"`
	ByChat    *string `json:"byChat"`
	Next      string  `json:"next"`
	Swappable bool    `json:"swappable"`
	Reason    string  `json:"reason,omitempty"`
}

// chatHandleTaken is nil when the chat's old handle is free (or the chat is open in a console).
func (s *Server) chatHandleTaken(v chatView) *handleTakenView {
	if v.Status == chats.StatusLive || v.Status == chats.StatusAsleep || v.Handle == "" {
		return nil
	}
	by := s.sessions.HandleHolder(v.Handle)
	if by == "" {
		return nil
	}
	in, ok := s.sessions.Info(by)
	if !ok {
		return nil
	}
	ht := &handleTakenView{Taken: v.Handle, By: by, ByHandle: in.Handle, Next: s.sessions.NextHandle()}
	if id := s.chats.idx.HeldBy(by); id != "" {
		if c, _, ok := s.chats.idx.Get(id); ok && c.Title != "" {
			t := c.Title
			ht.ByChat = &t
		}
	}
	ht.Reason = chats.SwapReason(v.Chat, in)
	if v.Status == chats.StatusOrphan && ht.Reason == "" {
		ht.Reason = chats.SwapOtherFolder
	}
	ht.Swappable = ht.Reason == ""
	return ht
}

// ---- transcripts ----

// agentHome is the home folder of a machine.
func agentHome(h machine.Host) string {
	if pr := h.Info().Probe; pr != nil && pr.Home != "" {
		return pr.Home
	}
	if h.ID() == machine.LocalID {
		return homeDir()
	}
	return ""
}

// chatKind is the kind of transcript a chat's agent keeps ("" = none).
func (s *Server) chatKind(c chats.Chat) (agent.Agent, string) {
	a, ok := s.agents.Get(c.Agent)
	if !ok {
		return agent.Agent{}, ""
	}
	if a.Transcript == agent.TranscriptClaude || a.Transcript == agent.TranscriptCodex {
		return a, a.Transcript
	}
	return a, ""
}

// chatTranscript finds a chat's transcript file: the one it was seen in, else where its CLI
// keeps it for that folder and login.
func (s *Server) chatTranscript(ctx context.Context, c chats.Chat) (machine.Host, string, string, bool) {
	a, kind := s.chatKind(c)
	if kind == "" {
		return nil, "", "", false
	}
	h, ok := s.machines.Host(c.MachineID)
	if !ok {
		return nil, "", "", false
	}
	if c.TranscriptPath != "" {
		if st, err := h.Stat(ctx, c.TranscriptPath); err == nil && st.Exists && !st.Dir {
			return h, kind, c.TranscriptPath, true
		}
	}
	if c.AgentSessionID == "" {
		return nil, "", "", false
	}
	dir := ""
	switch {
	case c.ProviderID != "":
		dir = providerHub{s}.ConfigDir(c.ProviderID, a)
	case c.ProfileID != "":
		if pf, ok := s.profiles.Get(c.ProfileID); ok {
			dir = pf.Dir
		}
	}
	home := agentHome(h)
	var p string
	if kind == agent.TranscriptClaude {
		p, _ = transcript.FindClaude(ctx, h, transcript.ClaudeConfigDir(home, firstSet(dir, localEnvVar(h, "CLAUDE_CONFIG_DIR"))), c.Cwd, c.AgentSessionID)
	} else {
		p, _ = transcript.CodexPathByID(ctx, h, transcript.CodexHome(home, firstSet(dir, localEnvVar(h, "CODEX_HOME"))), c.AgentSessionID, c.StartedAt)
	}
	return h, kind, p, p != ""
}

// chatText is the end of a chat's conversation as "role: text" lines, at most max characters
// (what a handoff summarises). Tool output is left out: it is what files and commands printed,
// not the conversation, and it is the easiest place for text that pretends to be instructions.
func (s *Server) chatText(ctx context.Context, c chats.Chat, max int) string {
	h, kind, p, ok := s.chatTranscript(ctx, c)
	if !ok {
		return ""
	}
	msgs, _, err := transcript.ReadMessages(ctx, h, kind, p, 200)
	if err != nil {
		return ""
	}
	var parts []string
	n := 0
	for i := len(msgs) - 1; i >= 0 && n < max; i-- {
		m := msgs[i]
		if m.Sidechain || m.Role == "tool" || strings.TrimSpace(m.Text) == "" {
			continue
		}
		line := m.Role
		if m.Tool != "" {
			line += " (" + m.Tool + ")"
		}
		line += ": " + m.Text
		n += len([]rune(line))
		parts = append([]string{line}, parts...)
	}
	out := strings.Join(parts, "\n\n")
	if r := []rune(out); len(r) > max {
		out = string(r[len(r)-max:])
	}
	return out
}

// ---- backfill ----

// chatDir is a folder where a CLI keeps conversations: a login profile's, or the default one.
type chatDir struct {
	kind, dir, profileID string
}

func (s *Server) chatDirs(h machine.Host) []chatDir {
	home := agentHome(h)
	var out []chatDir
	seen := map[string]bool{}
	add := func(d chatDir) {
		if d.dir == "" || seen[d.kind+"\x00"+path.Clean(d.dir)] {
			return
		}
		seen[d.kind+"\x00"+path.Clean(d.dir)] = true
		out = append(out, d)
	}
	for _, kind := range []string{agent.TranscriptClaude, agent.TranscriptCodex} {
		for _, pf := range s.profiles.List(h.ID()) {
			if a, ok := s.agents.Get(pf.Agent); ok && a.Transcript == kind {
				add(chatDir{kind: kind, dir: pf.Dir, profileID: pf.ID})
			}
		}
		if kind == agent.TranscriptClaude {
			add(chatDir{kind: kind, dir: transcript.ClaudeConfigDir(home, localEnvVar(h, "CLAUDE_CONFIG_DIR"))})
		} else {
			add(chatDir{kind: kind, dir: transcript.CodexHome(home, localEnvVar(h, "CODEX_HOME"))})
		}
	}
	return out
}

// chatPlace is a folder of This PC where a project's agents run: the root, a part on This PC,
// or the work folder of a part on a server or a read-only part.
type chatPlace struct {
	partID, partName string
	root             bool
	cwd              string
	remoteMachineID  string
	remoteCwd        string
}

func (s *Server) chatPlacesOf(p project.Project) []chatPlace {
	var out []chatPlace
	if len(p.Parts) > 1 {
		out = append(out, chatPlace{root: true, cwd: s.roots.Dir(p.ID)})
	}
	for _, pt := range p.Parts {
		pl := chatPlace{partID: pt.ID, partName: pt.Name, cwd: pt.Path}
		if pt.MachineID != machine.LocalID || pt.ReadOnly {
			pl.cwd, pl.remoteMachineID, pl.remoteCwd = s.remoteWorkPath(pt.MachineID, pt.Path), pt.MachineID, pt.Path
		}
		out = append(out, pl)
	}
	return out
}

// chatBackfill looks in the project's places for conversations the index does not have: those
// from before it, or from a CLI started by hand there. At most once a minute per project.
func (s *Server) chatBackfill(p project.Project) {
	ch := s.chats
	ch.mu.Lock()
	if time.Since(ch.scannedAt[p.ID]) < chatScanEvery {
		ch.mu.Unlock()
		return
	}
	ch.scannedAt[p.ID] = time.Now()
	ch.mu.Unlock()
	h, ok := s.machines.Host(machine.LocalID)
	if !ok {
		return
	}
	places := s.chatPlacesOf(p)
	byCwd := map[string]chatPlace{}
	var cwds []string
	for _, pl := range places {
		byCwd[pl.cwd] = pl
		cwds = append(cwds, pl.cwd)
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatScanBudget)
	defer cancel()
	// per project: a Codex rollout read for another project's folders may be one of this one's
	known := func(f string, mtime time.Time) bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		at, ok := ch.read[p.ID+"\x00"+f]
		return ok && at.Equal(mtime)
	}
	read := func(f string, mtime time.Time) {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		if len(ch.read) > 50000 {
			ch.read = map[string]time.Time{}
		}
		ch.read[p.ID+"\x00"+f] = mtime
	}
	var found []chats.Found
	for _, d := range s.chatDirs(h) {
		list, _, err := transcript.ScanPlaces(ctx, h, d.kind, d.dir, cwds, chatScanPerPlace, known, read)
		if err != nil {
			continue
		}
		for _, f := range list {
			pl := byCwd[f.Cwd]
			found = append(found, chats.Found{Kind: d.kind, PartName: pl.partName, Chat: chats.Chat{ProjectID: p.ID, Agent: d.kind,
				AgentSessionID: f.AgentSessionID, ProfileID: d.profileID, PartID: pl.partID, Root: pl.root, MachineID: machine.LocalID,
				RemoteMachineID: pl.remoteMachineID, RemoteCwd: pl.remoteCwd, Cwd: f.Cwd, Title: f.Title, Messages: f.Messages,
				StartedAt: f.StartedAt.UTC(), LastAt: f.UpdatedAt.UTC(), TranscriptPath: f.Path}})
		}
		if ctx.Err() != nil {
			break
		}
	}
	if len(found) > 0 {
		s.publishChats(ch.idx.Merge(p.ID, found))
	}
}

// ---- what changed since ----

// chatChanges is what changed in a chat's place since its last message: the new commits of its
// part's repository (at the root, of every part's), and the lines added to the project's main
// memory and that part's page (at the root, every part's).
func (s *Server) chatChanges(ctx context.Context, c chats.Chat) *chats.Changes {
	out := &chats.Changes{Since: c.LastAt}
	p, ok := s.projects.Get(c.ProjectID)
	if !ok {
		out.Finish()
		return out
	}
	var parts []project.Part
	switch {
	case c.Root:
		parts = p.Parts
	case c.PartID != "":
		if pt, ok := p.PartByID(c.PartID); ok {
			parts = []project.Part{pt}
		}
	}
	if s.gitPrev != nil {
		for _, pt := range parts {
			repo, ok := s.gitRepoIn(p.ID, pt.ID)
			if !ok {
				continue
			}
			got, err := s.gitPrev.git.CommitsSince(ctx, repo, c.LastAt, chatCommitsMax)
			if err != nil {
				continue // no repository there, or the server is away
			}
			pc := chats.PartCommits{PartID: pt.ID, Part: pt.Name, Count: got.Total, Files: got.Files}
			for _, cm := range got.Commits {
				pc.Subjects = append(pc.Subjects, cm.Subject)
			}
			out.Commits = append(out.Commits, pc)
		}
	}
	s.observeMemory(p.ID)
	docs, _ := s.memoryDocs(p.ID)
	keys := []string{"main"}
	if len(p.Parts) > 1 {
		for _, pt := range parts {
			keys = append(keys, pt.ID)
		}
	}
	out.Facts = s.chats.idx.MemoryAddedSince(p.ID, docs, keys, c.LastAt, chatFactsMax)
	out.Finish()
	return out
}

// waitChatReady waits for a console that was just started or switched (at t0) to be at its
// agent's prompt: false when it is gone, or not ready in time (a trust dialog nobody answered).
func (s *Server) waitChatReady(sid string, t0 time.Time) (session.Info, bool) {
	deadline := time.Now().Add(s.chats.readyWithin)
	moved := false
	for time.Now().Before(deadline) {
		in, ok := s.sessions.Info(sid)
		if !ok {
			return in, false
		}
		if in.StartedAt.After(t0) || in.AgentState != agentstate.Ready {
			moved = true // its agent (re)started since: the ready below is the new one's
		}
		if moved && in.Status == session.StatusRunning && in.AgentState == agentstate.Ready && in.Foreground == agentstate.FgAgent && !in.Paused {
			return in, true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return session.Info{}, false
}

// tellChatChanges tells a resumed chat what changed in its place since it last ran, once its
// agent is ready: context, never an order. Nothing when nothing changed.
func (s *Server) tellChatChanges(c chats.Chat, sid string, t0 time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	ch := s.chatChanges(ctx, c)
	cancel()
	if ch.Empty() || s.orch == nil {
		return
	}
	in, ok := s.waitChatReady(sid, t0)
	if !ok {
		return // gone, or not ready in time: the owner sees the changes in the dialog
	}
	if _, err := s.orch.TellContext(sid, ch.Text); err != nil {
		log.Printf("chats: telling @%s what changed: %v", in.Handle, err)
	}
}

// continueChat summarises a chat with the hidden runner, as a handoff does, and gives the
// summary to the console that continues it as its first order. Without a summary the console
// keeps its fresh start: the raw conversation is never sent as an order. The thread hears of
// either; a brief that cannot be delivered goes there as a note to copy (never as a reply, which
// the lead would take for its own answer).
func (s *Server) continueChat(c chats.Chat, sid, handle string, t0 time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	text := s.chatText(ctx, c, chatTextMax)
	cancel()
	note := func(text string) {
		if s.orch != nil {
			s.orch.Threads.Add(orch.ThreadMessage{ThreadID: c.ProjectID, Role: "hub", Kind: "note", SessionID: sid, Text: text})
		}
	}
	if strings.TrimSpace(text) == "" {
		note("Chat “" + c.Title + "” has no transcript to continue from: @" + handle + " starts fresh.")
		return
	}
	runAgent := "claude"
	if !(hubAdapter{s}).AgentOnMachine(machine.LocalID, "claude") && (hubAdapter{s}).AgentOnMachine(machine.LocalID, "codex") {
		runAgent = "codex"
	}
	who := c.Handle
	if who == "" {
		who = "agent"
	}
	done := make(chan jobs.Job, 1)
	req := jobs.RunnerRequest{Purpose: "handoff-summary", ProjectID: c.ProjectID, MachineID: machine.LocalID, Agent: runAgent,
		Command: s.settings.View().Agents.Paths[machine.LocalID][runAgent], Prompt: orch.HandoffBriefPrompt(who, "", text),
		OnFinish: func(j jobs.Job) { done <- j }}
	var j jobs.Job
	if _, err := s.chats.summarize(req); err != nil {
		j = jobs.Job{State: jobs.StateFailed, Error: err.Error()}
	} else {
		select {
		case j = <-done:
		case <-time.After(6 * time.Minute):
			j = jobs.Job{State: jobs.StateFailed, Error: "it took too long"}
		}
	}
	brief := ""
	if j.State == jobs.StateDone {
		var r struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(j.Result, &r) == nil {
			brief = strings.TrimSpace(r.Text)
		}
	}
	if brief == "" {
		why := j.Error
		if why == "" {
			why = "it came back empty"
		}
		note("The summary of chat “" + c.Title + "” failed (" + why + "): @" + handle + " starts fresh without it. Its transcript is in the Chats panel.")
		return
	}
	if _, ok := s.waitChatReady(sid, t0); !ok {
		note("@" + handle + " was not ready for the summary of chat “" + c.Title + "”. To continue, copy it into the console:\n\n" + brief)
		return
	}
	if _, err := s.deliver.Submit(sid, delivery.Request{Text: brief + "\n\n" + orch.HandoffContinue, Source: "handoff"}); err != nil {
		note("The summary of chat “" + c.Title + "” could not be sent to @" + handle + " (" + err.Error() + "). To continue, copy it into the console:\n\n" + brief)
		return
	}
	note("Chat “" + c.Title + "” continues in @" + handle + ", from a summary of it.")
}
