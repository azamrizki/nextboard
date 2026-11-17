// Package chats is the hub's index of agent conversations per project (docs/API-agents.md §29):
// every conversation an agent console had in NextBoard, and the ones found in the project's
// places on disk, with the place it ran in, so that it can be resumed there and nowhere else.
// A CLI keeps a conversation per folder, so a chat is only found again in the folder it was
// started in.
//
// The index is plain data: the hub tells it what its consoles do (Hold, Ended, Release) and
// what a scan found (Merge), and reads it back. One file per project, <dir>/<projectId>.json.
package chats

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/store"
	"github.com/azamrizki/nextboard/internal/transcript"
)

// Chat is one agent conversation of a project.
type Chat struct {
	// ID is "ch_" and a hash of the agent's kind of transcript and its agentSessionId, or of the
	// agent and its folder for an agent that keeps no session ids (ChatID).
	ID             string `json:"id"`
	ProjectID      string `json:"projectId"`
	Agent          string `json:"agent"`
	AgentSessionID string `json:"agentSessionId,omitempty"`
	ProfileID      string `json:"profileId,omitempty"`
	ProviderID     string `json:"providerId,omitempty"`
	// Where it was started: a part, or the project root (Root), or neither (a console in no
	// part), and the folder its CLI ran in.
	PartID          string `json:"partId,omitempty"`
	Root            bool   `json:"root,omitempty"`
	MachineID       string `json:"machineId"`
	RemoteMachineID string `json:"remoteMachineId,omitempty"`
	RemoteCwd       string `json:"remoteCwd,omitempty"`
	Cwd             string `json:"cwd"`
	// Title is the first user prompt (at most TitleMax characters) until the owner renames it.
	Title     string    `json:"title"`
	Messages  int       `json:"messages"`
	StartedAt time.Time `json:"startedAt"`
	LastAt    time.Time `json:"lastAt"`
	// Handle is the handle of the console that held it last.
	Handle         string `json:"handle"`
	WorkspaceID    string `json:"workspaceId,omitempty"`
	MissionID      string `json:"missionId,omitempty"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
	Archived       bool   `json:"archived,omitempty"`
}

// TitleMax bounds a chat's title.
const TitleMax = 120

// record is a chat as the index keeps it.
type record struct {
	Chat
	// Holder is the console that has the conversation now ("" = none).
	Holder string `json:"holder,omitempty"`
	// Mark is the last message counted, so a turn's end only reads what came after it.
	Mark transcript.Mark `json:"mark,omitzero"`
	// Renamed: the owner's title stays.
	Renamed bool `json:"renamed,omitempty"`
	// PartName is the part's name when the chat last ran: what it is shown as once the part
	// is gone.
	PartName string `json:"partName,omitempty"`
	// Imprecise: an agent without session ids, whose chat is its last conversation in the folder.
	Imprecise bool `json:"imprecise,omitempty"`
}

// memLine is when the index first saw a line of the project's memory (docs: "main" or a part id).
type memLine struct {
	At  int64  `json:"at"` // unix milliseconds, 0 = before the index looked at the memory the first time
	Doc string `json:"doc"`
}

// projectFile is <dir>/<projectId>.json.
type projectFile struct {
	Chats []*record `json:"chats"`
	// Memory: the lines of the project's memory by hash, and when each was first seen; what a
	// resumed chat is told was added since it last ran.
	Memory         map[string]memLine `json:"memory,omitempty"`
	MemoryObserved bool               `json:"memoryObserved,omitempty"`
	// MemoryDocs are the documents the index has read once: the lines of one read for the
	// first time were there before.
	MemoryDocs map[string]bool `json:"memoryDocs,omitempty"`
}

var (
	ErrNotFound = errors.New("chat not found")
	ErrBadTitle = errors.New("the title must be 1–120 characters")
)

var projectIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Index is the chats of every project.
type Index struct {
	dir string
	// Now is the clock (tests set it).
	Now func() time.Time

	mu    sync.Mutex
	files map[string]*projectFile
	byID  map[string]*record
	// held: console → the chat it holds
	held map[string]string
	// pending: console → turn ends not counted yet (BeginTurn). A chat without messages that its
	// console lets go of meanwhile is not dropped before they are (deferred).
	pending  map[string]int
	deferred map[string][]string

	saveMu sync.Mutex
}

// Open loads every project's chats from dir.
func Open(dir string) (*Index, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	x := &Index{dir: dir, Now: time.Now, files: map[string]*projectFile{}, byID: map[string]*record{}, held: map[string]string{},
		pending: map[string]int{}, deferred: map[string][]string{}}
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	st := &store.Store{Dir: dir}
	for _, e := range es {
		pid, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || !projectIDRe.MatchString(pid) {
			continue
		}
		var f projectFile
		if _, err := st.Load(e.Name(), &f); err != nil {
			log.Printf("chats: %v", err)
			continue
		}
		x.files[pid] = &f
		for _, r := range f.Chats {
			r.ProjectID = pid
			x.byID[r.ID] = r
			if r.Holder != "" {
				x.held[r.Holder] = r.ID
			}
		}
	}
	return x, nil
}

// ChatID is the id of a conversation: of its agentSessionId, which is unique, for an agent that
// keeps session ids (kind is its kind of transcript, so a custom agent on Claude Code and Claude
// Code itself find the same chat); else of the agent and the folder it continues in.
func ChatID(kind, agentSessionID, agentID, machineID, cwd string) string {
	key := "s\x00" + kind + "\x00" + agentSessionID
	if agentSessionID == "" {
		key = "f\x00" + agentID + "\x00" + machineID + "\x00" + filepath.Clean(cwd)
	}
	sum := sha256.Sum256([]byte(key))
	return "ch_" + hex.EncodeToString(sum[:8])
}

// cleanTitle is a title on one line, at most TitleMax characters.
func cleanTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > TitleMax {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:TitleMax-1])) + "…"
	}
	return s
}

func (x *Index) fileLocked(pid string) *projectFile {
	f := x.files[pid]
	if f == nil {
		f = &projectFile{}
		x.files[pid] = f
	}
	return f
}

// save writes a project's file. One writer at a time, so an older copy never lands last.
func (x *Index) save(pid string) {
	x.saveMu.Lock()
	defer x.saveMu.Unlock()
	x.mu.Lock()
	f := x.files[pid]
	if f == nil { // deleted or moved meanwhile: its file is gone, and stays gone
		x.mu.Unlock()
		return
	}
	var cp projectFile
	cp.Memory, cp.MemoryObserved = make(map[string]memLine, len(f.Memory)), f.MemoryObserved
	for k, v := range f.Memory {
		cp.Memory[k] = v
	}
	cp.MemoryDocs = make(map[string]bool, len(f.MemoryDocs))
	for k, v := range f.MemoryDocs {
		cp.MemoryDocs[k] = v
	}
	cp.Chats = make([]*record, len(f.Chats))
	for i, r := range f.Chats {
		c := *r
		cp.Chats[i] = &c
	}
	x.mu.Unlock()
	st := &store.Store{Dir: x.dir}
	if err := st.Save(pid+".json", cp); err != nil {
		log.Printf("chats: saving %s: %v", pid, err)
	}
}

// Holding is a console as the index is told about it: the chat it has now.
type Holding struct {
	SessionID string
	// Kind is the agent's kind of transcript ("claude", "codex", or "" for none).
	Kind string
	// Imprecise: the agent keeps no session ids (resume = its last conversation in the folder).
	Imprecise bool
	// PartName is the part's name now.
	PartName string
	Chat
}

// Key is the chat a console has: "" while its conversation is not known.
func (h Holding) Key() string {
	if h.Imprecise {
		return ChatID("", "", h.Agent, h.MachineID, h.Cwd)
	}
	if h.AgentSessionID == "" {
		return ""
	}
	return ChatID(h.Kind, h.AgentSessionID, "", "", "")
}

// Hold records that a console has the conversation h names, and that it no longer has the one
// it had before (a resume or a fresh start in the same console). A conversation not yet in the
// index is added. It returns the chats that changed, and whether the console's chat is new to
// it (so its mission is worth looking up). A console whose conversation is not known yet lets
// go of the one it had.
func (x *Index) Hold(h Holding) (changed []Chat, fresh bool) {
	id := h.Key()
	x.mu.Lock()
	prev := x.held[h.SessionID]
	if prev != "" && prev == id {
		r := x.byID[id]
		if r != nil && r.Holder == h.SessionID && (h.Handle == "" || r.Handle == h.Handle) && (h.WorkspaceID == "" || r.WorkspaceID == h.WorkspaceID) &&
			(h.TranscriptPath == "" || r.TranscriptPath == h.TranscriptPath) {
			x.mu.Unlock()
			return nil, false
		}
	}
	saves := map[string]bool{}
	if prev != "" && prev != id {
		if c, pid, ok := x.releaseLocked(h.SessionID, ""); ok {
			saves[pid] = true
			if c.ID != "" {
				changed = append(changed, c)
			}
		}
	}
	if id == "" {
		x.mu.Unlock()
		x.saveAll(saves)
		return changed, false
	}
	now := x.Now().UTC()
	r := x.byID[id]
	if r == nil {
		r = &record{Chat: h.Chat, Imprecise: h.Imprecise}
		r.ID, r.Title, r.Messages, r.Archived, r.MissionID = id, "", 0, false, h.MissionID
		r.StartedAt, r.LastAt = now, now
		f := x.fileLocked(h.ProjectID)
		f.Chats = append(f.Chats, r)
		x.byID[id] = r
	}
	if o := r.Holder; o != "" && o != h.SessionID {
		delete(x.held, o) // two consoles on one conversation: the last one has it
	}
	fresh = r.Holder != h.SessionID
	r.Holder = h.SessionID
	x.held[h.SessionID] = id
	if h.Handle != "" {
		r.Handle = h.Handle
	}
	if h.WorkspaceID != "" {
		r.WorkspaceID = h.WorkspaceID
	}
	if h.TranscriptPath != "" {
		r.TranscriptPath = h.TranscriptPath
	}
	if h.PartName != "" {
		r.PartName = h.PartName
	}
	if fresh {
		// the console's login and provider are the chat's from now on
		r.ProfileID, r.ProviderID = h.ProfileID, h.ProviderID
	}
	saves[r.ProjectID] = true
	changed = append(changed, r.Chat)
	x.mu.Unlock()
	x.saveAll(saves)
	return changed, fresh
}

func (x *Index) saveAll(pids map[string]bool) {
	for pid := range pids {
		x.save(pid)
	}
}

// releaseLocked takes the chat of a console from it, with the console's handle when given.
// A chat that never had a message is dropped: there is nothing to resume (c.ID is "" then). While
// a turn end of the console is being counted, that waits for it (EndTurn): its first turn may be
// the one being counted.
func (x *Index) releaseLocked(sid, handle string) (c Chat, pid string, ok bool) {
	id := x.held[sid]
	delete(x.held, sid)
	r := x.byID[id]
	if r == nil || r.Holder != sid {
		return Chat{}, "", false
	}
	r.Holder = ""
	if handle != "" {
		r.Handle = handle
	}
	if r.Messages == 0 {
		if x.pending[sid] > 0 {
			x.deferred[sid] = append(x.deferred[sid], r.ID)
		} else {
			x.dropLocked(r)
		}
		return Chat{}, r.ProjectID, true
	}
	return r.Chat, r.ProjectID, true
}

// BeginTurn notes that a turn end of a console is about to be counted.
func (x *Index) BeginTurn(sid string) {
	x.mu.Lock()
	x.pending[sid]++
	x.mu.Unlock()
}

// EndTurn notes that a turn end was counted (or not). Once none is pending, the chats its console
// let go of meanwhile, and ids, are dropped if they still have no message and no console.
func (x *Index) EndTurn(sid string, ids ...string) {
	x.mu.Lock()
	x.pending[sid]--
	if x.pending[sid] > 0 {
		x.deferred[sid] = append(x.deferred[sid], ids...)
		x.mu.Unlock()
		return
	}
	delete(x.pending, sid)
	ids = append(x.deferred[sid], ids...)
	delete(x.deferred, sid)
	saves := map[string]bool{}
	for _, id := range ids {
		if r := x.byID[id]; r != nil && r.Holder == "" && r.Messages == 0 {
			x.dropLocked(r)
			saves[r.ProjectID] = true
		}
	}
	x.mu.Unlock()
	x.saveAll(saves)
}

// Note adds a conversation the index has not heard of, without a console: its console went before
// its first turn was counted. A chat it has stays as it is.
func (x *Index) Note(h Holding) {
	id := h.Key()
	if id == "" {
		return
	}
	x.mu.Lock()
	if x.byID[id] != nil {
		x.mu.Unlock()
		return
	}
	r := &record{Chat: h.Chat, Imprecise: h.Imprecise, PartName: h.PartName}
	now := x.Now().UTC()
	r.ID, r.Title, r.Messages, r.Archived, r.StartedAt, r.LastAt = id, "", 0, false, now, now
	f := x.fileLocked(h.ProjectID)
	f.Chats = append(f.Chats, r)
	x.byID[id] = r
	x.mu.Unlock()
	x.save(h.ProjectID)
}

func (x *Index) dropLocked(r *record) {
	delete(x.byID, r.ID)
	if f := x.files[r.ProjectID]; f != nil {
		for i, o := range f.Chats {
			if o == r {
				f.Chats = append(f.Chats[:i], f.Chats[i+1:]...)
				break
			}
		}
	}
}

// Release lets go of a console's chat: the console was deleted. The chat keeps the handle it
// had there. It returns the chat when one changed.
func (x *Index) Release(sid, handle string) (Chat, bool) {
	x.mu.Lock()
	c, pid, ok := x.releaseLocked(sid, handle)
	x.mu.Unlock()
	if ok {
		x.save(pid)
	}
	return c, ok && c.ID != ""
}

// HeldBy is the chat a console has ("" = none).
func (x *Index) HeldBy(sid string) string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.held[sid]
}

// Get returns a chat and the console that has it.
func (x *Index) Get(id string) (Chat, string, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	r := x.byID[id]
	if r == nil {
		return Chat{}, "", false
	}
	return r.Chat, r.Holder, true
}

// Entry is a chat with what the hub shows of it besides.
type Entry struct {
	Chat
	Holder    string
	PartName  string
	Imprecise bool
	Mark      transcript.Mark
	Renamed   bool
}

func entryOf(r *record) Entry {
	return Entry{Chat: r.Chat, Holder: r.Holder, PartName: r.PartName, Imprecise: r.Imprecise, Mark: r.Mark, Renamed: r.Renamed}
}

// Entry returns one chat with its bookkeeping.
func (x *Index) Entry(id string) (Entry, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	r := x.byID[id]
	if r == nil {
		return Entry{}, false
	}
	return entryOf(r), true
}

// List returns a project's chats, newest first.
func (x *Index) List(pid string) []Entry {
	x.mu.Lock()
	var out []Entry
	if f := x.files[pid]; f != nil {
		out = make([]Entry, 0, len(f.Chats))
		for _, r := range f.Chats {
			out = append(out, entryOf(r))
		}
	}
	x.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}

// Turn is what the end of a turn tells about a chat.
type Turn struct {
	At time.Time
	// New messages after the mark (Since), or all that were read.
	New   int
	Since bool
	Next  transcript.Mark
	// Title and StartedAt from the transcript: used when the chat has none.
	Title          string
	StartedAt      time.Time
	TranscriptPath string
	MissionID      string
}

// Ended records a turn of a chat. It returns the chat as it is now.
func (x *Index) Ended(id string, t Turn) (Chat, bool) {
	x.mu.Lock()
	r := x.byID[id]
	if r == nil {
		x.mu.Unlock()
		return Chat{}, false
	}
	if t.Since {
		r.Messages += t.New
	} else {
		r.Messages = max(r.Messages, t.New)
	}
	if t.Next.ID != "" || !t.Next.At.IsZero() {
		r.Mark = t.Next
	}
	if t.At.IsZero() {
		t.At = x.Now()
	}
	r.LastAt = t.At.UTC()
	if !r.Renamed && r.Title == "" {
		r.Title = cleanTitle(t.Title)
	}
	if !t.StartedAt.IsZero() && t.StartedAt.Before(r.StartedAt) {
		r.StartedAt = t.StartedAt.UTC()
	}
	if t.TranscriptPath != "" {
		r.TranscriptPath = t.TranscriptPath
	}
	if t.MissionID != "" {
		r.MissionID = t.MissionID
	}
	c, pid := r.Chat, r.ProjectID
	x.mu.Unlock()
	x.save(pid)
	return c, true
}

// SetMission notes the mission a chat's console works in.
func (x *Index) SetMission(id, missionID string) {
	x.mu.Lock()
	r := x.byID[id]
	if r == nil || missionID == "" || r.MissionID == missionID {
		x.mu.Unlock()
		return
	}
	r.MissionID = missionID
	pid := r.ProjectID
	x.mu.Unlock()
	x.save(pid)
}

// Patch renames or archives a chat.
func (x *Index) Patch(id string, title *string, archived *bool) (Chat, error) {
	x.mu.Lock()
	r := x.byID[id]
	if r == nil {
		x.mu.Unlock()
		return Chat{}, ErrNotFound
	}
	if title != nil {
		t := cleanTitle(*title)
		if t == "" || utf8.RuneCountInString(strings.TrimSpace(*title)) > TitleMax {
			x.mu.Unlock()
			return Chat{}, ErrBadTitle
		}
		r.Title, r.Renamed = t, true
	}
	if archived != nil {
		r.Archived = *archived
	}
	c, pid := r.Chat, r.ProjectID
	x.mu.Unlock()
	x.save(pid)
	return c, nil
}

// Found is a conversation a scan of the project's places found.
type Found struct {
	Kind string
	// PartName is the part's name now.
	PartName string
	Chat
}

// Merge adds what a scan found: a conversation the index has (by its agentSessionId) gets what
// the file says that the index does not know yet; one it has not is added without a console.
// It returns the chats that changed.
func (x *Index) Merge(pid string, found []Found) []Chat {
	x.mu.Lock()
	var changed []Chat
	saves := map[string]bool{}
	f := x.fileLocked(pid)
	bySID := map[string]*record{}
	for _, r := range f.Chats {
		if r.AgentSessionID != "" {
			bySID[r.AgentSessionID] = r
		}
	}
	for _, fd := range found {
		if fd.AgentSessionID == "" {
			continue
		}
		r := bySID[fd.AgentSessionID]
		if r == nil {
			id := ChatID(fd.Kind, fd.AgentSessionID, "", "", "")
			if o := x.byID[id]; o != nil {
				r = o // in another project's file: it stays there
			}
		}
		if r == nil {
			r = &record{Chat: fd.Chat, PartName: fd.PartName}
			r.ID, r.ProjectID, r.Holder, r.Title = ChatID(fd.Kind, fd.AgentSessionID, "", "", ""), pid, "", cleanTitle(fd.Title)
			if r.LastAt.IsZero() {
				r.LastAt = r.StartedAt
			}
			f.Chats = append(f.Chats, r)
			x.byID[r.ID] = r
			bySID[fd.AgentSessionID] = r
			changed = append(changed, r.Chat)
			saves[pid] = true
			continue
		}
		ch := false
		if fd.Messages > r.Messages {
			r.Messages, ch = fd.Messages, true
		}
		if fd.LastAt.After(r.LastAt) {
			r.LastAt, ch = fd.LastAt.UTC(), true
		}
		if r.TranscriptPath == "" && fd.TranscriptPath != "" {
			r.TranscriptPath, ch = fd.TranscriptPath, true
		}
		if r.Title == "" && !r.Renamed && fd.Title != "" {
			r.Title, ch = cleanTitle(fd.Title), true
		}
		if ch {
			changed = append(changed, r.Chat)
			saves[r.ProjectID] = true
		}
	}
	x.mu.Unlock()
	x.saveAll(saves)
	return changed
}

// MoveProject moves every chat of project from into project to (a merge): each keeps its
// conversation and folder, and its part the id that part has in to (partIDs: from's part id →
// to's). A chat of from's root stays at a root: to's root is another folder, so it is an orphan.
// from's file goes. It returns the chats moved.
func (x *Index) MoveProject(from, to string, partIDs map[string]string) []Chat {
	return x.move(from, to, func(r *record) bool {
		if n, ok := partIDs[r.PartID]; ok && r.PartID != "" {
			r.PartID = n
		}
		return true
	}, true)
}

// MovePart moves the chats of one part of project from into project to, which that part became
// (a split; the part keeps its id). Chats of from's root and other parts stay.
func (x *Index) MovePart(from, to, partID string) []Chat {
	return x.move(from, to, func(r *record) bool { return r.PartID == partID && !r.Root }, false)
}

func (x *Index) move(from, to string, take func(r *record) bool, all bool) []Chat {
	if from == to || !projectIDRe.MatchString(from) || !projectIDRe.MatchString(to) {
		return nil
	}
	x.mu.Lock()
	src := x.files[from]
	var moved []Chat
	if src != nil {
		dst := x.fileLocked(to)
		var keep []*record
		for _, r := range src.Chats {
			if !take(r) {
				keep = append(keep, r)
				continue
			}
			r.ProjectID = to
			dst.Chats = append(dst.Chats, r)
			moved = append(moved, r.Chat)
		}
		src.Chats = keep
		if all {
			delete(x.files, from) // its memory's lines are another project's now: to looks again
		}
	}
	x.mu.Unlock()
	if src == nil {
		return nil
	}
	x.save(to)
	if all {
		x.saveMu.Lock()
		_ = os.Remove(filepath.Join(x.dir, from+".json"))
		x.saveMu.Unlock()
	} else {
		x.save(from)
	}
	return moved
}

// DeleteProject forgets a project's chats.
func (x *Index) DeleteProject(pid string) {
	if !projectIDRe.MatchString(pid) {
		return
	}
	x.saveMu.Lock()
	defer x.saveMu.Unlock()
	x.mu.Lock()
	if f := x.files[pid]; f != nil {
		for _, r := range f.Chats {
			delete(x.byID, r.ID)
			if r.Holder != "" && x.held[r.Holder] == r.ID {
				delete(x.held, r.Holder)
			}
		}
	}
	delete(x.files, pid)
	x.mu.Unlock()
	_ = os.Remove(filepath.Join(x.dir, pid+".json"))
}
