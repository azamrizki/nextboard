package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// The project root (v0.9.0-beta.2, docs/API-agents.md §21.2): a folder on This PC the hub makes
// for a project of several parts, <data>/roots/<projectId>, the working folder of every console
// placed at the root. It is a view: one entry per part, named like the part. A part on This PC
// that agents may change is a symlink to its folder. A read-only part on This PC and a part on a
// server are a folder holding only a note (0755, the note 0444, so a plain `rm -rf` of the data
// folder works); nothing of a server is copied. The note and the instruction files say not to
// write there, and each sync removes what an agent put there anyway (plain files and folders,
// never through a link), so it never looks like that part's files. AGENTS.md and CLAUDE.md (the
// same text, Claude Code reads only CLAUDE.md) list every part: where it is and what an agent may
// do there. .nextboard/manifest.json records what the hub made, so a cleanup only removes its own
// entries, never follows a link, and leaves what an agent made at the root itself alone.

const (
	rootNoteFile    = "NEXTBOARD-PART.md"
	rootManifestDir = ".nextboard"
	rootManifest    = "manifest.json"
	rootDebounce    = 200 * time.Millisecond
	rootLink        = "link"
	rootNote        = "note"
)

// rootDocs are the instruction files at the root, in the order CLIs look for them.
var rootDocs = []string{"AGENTS.md", "CLAUDE.md"}

// rootEntry is one entry the hub made at a root.
type rootEntry struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`             // rootLink or rootNote
	Target string `json:"target,omitempty"` // a link's folder
	PartID string `json:"partId"`
}

type rootManifestFile struct {
	ProjectID string      `json:"projectId"`
	Entries   []rootEntry `json:"entries"`
}

// rootDirs keeps the project roots of a hub.
type rootDirs struct {
	s    *Server
	base string

	mu sync.Mutex // one sync or removal at a time

	tmu    sync.Mutex
	timers map[string]*time.Timer
	closed bool
}

func newRootDirs(s *Server) *rootDirs {
	return &rootDirs{s: s, base: s.st.Path("roots"), timers: map[string]*time.Timer{}}
}

// Dir is the root folder of a project (whether or not it exists yet). It never changes, because
// Claude Code finds a conversation by its folder.
func (r *rootDirs) Dir(pid string) string { return filepath.Join(r.base, pid) }

// Sync makes the project's root current and returns its folder: at every launch of a root
// console (session.Manager.RootDir) and after a change of the project (Changed). It is
// idempotent. Folders of projects merged into this one (its aliases) that a root console still
// works in are kept current too; one that no console uses goes.
func (r *rootDirs) Sync(pid string) (string, error) {
	if !attachID.MatchString(pid) {
		return "", errors.New("invalid project id")
	}
	pr, ok := r.s.projects.Get(pid)
	if !ok {
		return "", project.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(r.base, 0o700); err != nil {
		return "", err
	}
	dir := r.Dir(pr.ID)
	if err := r.syncInto(dir, pr); err != nil {
		return "", err
	}
	for _, alias := range pr.Aliases {
		if !attachID.MatchString(alias) {
			continue
		}
		ad := r.Dir(alias)
		if _, err := os.Lstat(ad); err != nil {
			continue
		}
		if !r.inUse(ad) {
			r.removeLocked(ad)
			continue
		}
		if err := r.syncInto(ad, pr); err != nil {
			log.Printf("project root %s (merged into %s): %v", alias, pr.ID, err)
		}
	}
	return dir, nil
}

// Changed resyncs a project's root a moment after the project changed (parts added, renamed,
// moved, made read-only or removed, a split or merge, partAccess), only when it has one.
func (r *rootDirs) Changed(pid string) {
	if r == nil || pid == "" {
		return
	}
	r.tmu.Lock()
	defer r.tmu.Unlock()
	if r.closed {
		return
	}
	if t := r.timers[pid]; t != nil {
		t.Stop()
	}
	r.timers[pid] = time.AfterFunc(rootDebounce, func() {
		r.tmu.Lock()
		delete(r.timers, pid)
		closed := r.closed
		r.tmu.Unlock()
		if !closed {
			r.changedNow(pid)
		}
	})
}

func (r *rootDirs) changedNow(pid string) {
	pr, ok := r.s.projects.Get(pid)
	if !ok {
		return
	}
	has := false
	for _, id := range append([]string{pr.ID}, pr.Aliases...) {
		if _, err := os.Lstat(r.Dir(id)); err == nil && attachID.MatchString(id) {
			has = true
		}
	}
	if !has {
		return
	}
	if _, err := r.Sync(pid); err != nil {
		log.Printf("project root %s: %v", pid, err)
	}
}

// Remove deletes the root folders of these projects (a permanent delete: the project and the
// ids merged into it). Links go as links: the folders they point to are not touched.
func (r *rootDirs) Remove(ids ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if attachID.MatchString(id) {
			r.removeLocked(r.Dir(id))
		}
	}
}

// Prune removes, at hub start, the roots of projects that are gone.
func (r *rootDirs) Prune() {
	ents, err := os.ReadDir(r.base)
	if err != nil {
		return
	}
	known := map[string]bool{}
	for _, pr := range r.s.projects.ListAll() {
		known[pr.ID] = true
		for _, a := range pr.Aliases {
			known[a] = true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range ents {
		if !known[e.Name()] {
			r.removeLocked(filepath.Join(r.base, e.Name()))
		}
	}
}

// Close stops the resyncs still waiting.
func (r *rootDirs) Close() {
	if r == nil {
		return
	}
	r.tmu.Lock()
	defer r.tmu.Unlock()
	r.closed = true
	for _, t := range r.timers {
		t.Stop()
	}
}

// inUse reports whether a root console works in dir.
func (r *rootDirs) inUse(dir string) bool {
	for _, pl := range r.s.sessions.AllPlacements() {
		if !pl.Root {
			continue
		}
		if sess, ok := r.s.sessions.Get(pl.ID); ok && filepath.Clean(sess.Spec().Cwd) == dir {
			return true
		}
	}
	return false
}

// rootPart is one part as the root shows it.
type rootPart struct {
	pt    project.Part
	entry rootEntry
	// local: on This PC. own: changed with the agent's own tools, through the link. change: the
	// hub's write tools may change it (a server part with full access, not read only).
	local, own, change bool
	machine            string // "This PC" or `server "srv-a"`
	// here: how the entry shows at the root, "" when something else has its name there
	here string
	// docs: the part's own instruction files on This PC that exist
	docs []string
}

// rootParts is what the root of pr holds: every part, from This PC's point of view.
func (s *Server) rootParts(pr project.Project) []rootPart {
	here := project.Part{MachineID: machine.LocalID}
	out := make([]rootPart, 0, len(pr.Parts))
	for _, pt := range pr.Parts {
		rp := rootPart{pt: pt, local: pt.MachineID == machine.LocalID}
		rp.own = rp.local && !pt.ReadOnly
		rp.change = !rp.local && mayChange(pr, here, pt)
		rp.machine = "This PC"
		if !rp.local {
			name := pt.MachineID
			if h, ok := s.machines.Host(pt.MachineID); ok && h.Info().Name != "" {
				name = h.Info().Name
			}
			rp.machine = fmt.Sprintf("server %q", name)
		}
		rp.entry = rootEntry{Name: rootEntryName(pt), Kind: rootNote, PartID: pt.ID}
		if rp.own {
			rp.entry.Kind, rp.entry.Target = rootLink, filepath.Clean(pt.Path)
			for _, d := range []string{"CLAUDE.md", "AGENTS.md"} {
				if fi, err := os.Stat(filepath.Join(pt.Path, d)); err == nil && fi.Mode().IsRegular() {
					rp.docs = append(rp.docs, d)
				}
			}
		}
		out = append(out, rp)
	}
	return out
}

// rootEntryName is the entry of a part at the root: its name, which is path-safe
// (project.CleanPartName). A name from before that rule gets a safe stand-in until it is renamed.
func rootEntryName(pt project.Part) string {
	if n, err := project.CleanPartName(pt.Name); err == nil && n == pt.Name {
		return n
	}
	var b strings.Builder
	for _, c := range strings.TrimLeft(strings.TrimSpace(pt.Name), ".") {
		switch {
		case c == '/' || c == '\\' || c == 0 || c < 0x20:
			b.WriteByte('-')
		default:
			b.WriteRune(c)
		}
	}
	if n, err := project.CleanPartName(b.String()); err == nil && n == b.String() {
		return n
	}
	return "part-" + strings.TrimPrefix(pt.ID, "pt_")
}

// syncInto brings one root folder to what pr's parts are now. Caller holds r.mu.
func (r *rootDirs) syncInto(dir string, pr project.Project) error {
	if fi, err := os.Lstat(dir); err == nil && !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	parts := r.s.rootParts(pr)
	want := map[string]rootEntry{}
	for _, rp := range parts {
		want[rp.entry.Name] = rp.entry
	}
	old := readRootManifest(dir)
	ours := map[string]bool{}
	var keep []rootEntry
	for _, e := range old.Entries {
		if !safeEntryName(e.Name) {
			continue
		}
		if w, ok := want[e.Name]; ok && w.Kind == e.Kind && w.Target == e.Target {
			ours[e.Name] = true
			keep = append(keep, e)
			continue
		}
		removeRootEntry(dir, e)
	}
	// What may be made next is recorded first, so a crash halfway leaves nothing unrecorded.
	pending := slices.Clone(keep)
	for _, rp := range parts {
		if !ours[rp.entry.Name] {
			pending = append(pending, rp.entry)
		}
	}
	if err := writeRootManifest(dir, pr.ID, pending); err != nil {
		return err
	}
	var made []rootEntry
	for i := range parts {
		rp := &parts[i]
		if ok := makeRootEntry(dir, pr, rp, ours[rp.entry.Name]); ok {
			rp.here = "./" + rp.entry.Name + " (" + rp.entry.Kind + ")"
			made = append(made, rp.entry)
		}
	}
	doc := []byte(rootDoc(pr, parts))
	for _, name := range rootDocs {
		if err := writeIfChanged(filepath.Join(dir, name), doc, 0o444); err != nil {
			return err
		}
	}
	return writeRootManifest(dir, pr.ID, made)
}

// makeRootEntry makes or updates one part's entry; false when something the hub did not make
// holds its name (it is left alone).
func makeRootEntry(dir string, pr project.Project, rp *rootPart, ours bool) bool {
	e := rp.entry
	p := filepath.Join(dir, e.Name)
	fi, err := os.Lstat(p)
	switch e.Kind {
	case rootLink:
		if err == nil {
			if fi.Mode()&os.ModeSymlink == 0 {
				log.Printf("project root %s: %s is in the way of the link to %s; left as it is", pr.ID, p, e.Target)
				return false
			}
			if t, _ := os.Readlink(p); t == e.Target {
				return true
			}
		}
		tmp := filepath.Join(dir, ".nb-link-"+randomHex(6))
		if err := os.Symlink(e.Target, tmp); err != nil {
			log.Printf("project root %s: %v", pr.ID, err)
			return false
		}
		if err := os.Rename(tmp, p); err != nil { // replaces an old link in one step
			_ = os.Remove(tmp)
			log.Printf("project root %s: %v", pr.ID, err)
			return false
		}
		return true
	default:
		switch {
		case err != nil:
			if err := os.Mkdir(p, 0o755); err != nil {
				log.Printf("project root %s: %v", pr.ID, err)
				return false
			}
		case !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0:
			log.Printf("project root %s: %s is in the way of the note of %s; left as it is", pr.ID, p, rp.pt.Name)
			return false
		case !ours:
			// a folder of that name the hub did not make: it may hold an agent's work
			if _, err := os.Lstat(filepath.Join(p, rootNoteFile)); err != nil {
				log.Printf("project root %s: %s was not made by NextBoard; left as it is", pr.ID, p)
				return false
			}
		}
		// 0755, also for a folder made read only (0555) by an earlier build
		if err := os.Chmod(p, 0o755); err != nil {
			return false
		}
		if gone, left := clearNoteDir(p, true); gone > 0 || left > 0 {
			log.Printf("project root %s: removed %d entries an agent put in the note of %s (nothing there reaches the part); %d left (links, or what could not go)", pr.ID, gone, rp.pt.Name, left)
		}
		if err := writeIfChanged(filepath.Join(p, rootNoteFile), []byte(rootNoteText(pr, *rp)), 0o444); err != nil {
			log.Printf("project root %s: note of %s: %v", pr.ID, rp.pt.Name, err)
			return false
		}
		return true
	}
}

// clearNoteDir removes what is not the note in a note folder (top) and below: an agent wrote
// there although the note says not to, and nothing written there reaches the part. Plain files
// and folders go; a link stays and is never followed (nor is what it points to touched), and so
// does anything else that is not a plain file or a folder, and a folder still holding one. gone
// and left count the entries removed and kept.
func clearNoteDir(dir string, top bool) (gone, left int) {
	ents, err := os.ReadDir(dir) // the types are the entries' own (lstat): a link is a link
	if err != nil {
		return 0, 1
	}
	for _, e := range ents {
		q := filepath.Join(dir, e.Name())
		t := e.Type()
		switch {
		case top && e.Name() == rootNoteFile && t.IsRegular():
			// the note itself: writeIfChanged brings it back to its text and mode
		case t.IsRegular():
			if os.Remove(q) == nil {
				gone++
			} else {
				left++
			}
		case t.IsDir():
			_ = os.Chmod(q, 0o700) // a folder an agent made read only; q is no link (lstat)
			g, l := clearNoteDir(q, false)
			gone, left = gone+g, left+l
			if l == 0 && os.Remove(q) == nil {
				gone++
			} else {
				left++
			}
		default: // a link (to a file or a folder), a socket, a fifo…: left alone
			left++
		}
	}
	return gone, left
}

// removeRootEntry removes what the hub made for a part that is gone or changed: a link as a
// link, never what it points to; a note folder with its note and what an agent put there
// (clearNoteDir), and only when no link or the like is left in it.
func removeRootEntry(dir string, e rootEntry) {
	if !safeEntryName(e.Name) {
		return
	}
	p := filepath.Join(dir, e.Name)
	fi, err := os.Lstat(p)
	if err != nil {
		return
	}
	switch {
	case e.Kind == rootLink && fi.Mode()&os.ModeSymlink != 0:
		_ = os.Remove(p)
	case e.Kind == rootNote && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0:
		_ = os.Chmod(p, 0o755)
		clearNoteDir(p, false)
		if err := os.Remove(p); err != nil {
			log.Printf("project root: %s holds links NextBoard did not make; left as it is", p)
		}
	default:
		log.Printf("project root: %s is no longer what NextBoard made; left as it is", p)
	}
}

// removeLocked deletes a root folder. Every entry is removed without following links (first the
// hub's own by the manifest, then what is left, folders made writable first: an agent's, or a note
// folder of an earlier build, 0555). Caller holds r.mu.
func (r *rootDirs) removeLocked(dir string) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(dir)
		return
	}
	for _, e := range readRootManifest(dir).Entries {
		removeRootEntry(dir, e)
	}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Type()&os.ModeSymlink == 0 {
			_ = os.Chmod(p, 0o700) // WalkDir never descends into a link
		}
		return nil
	})
	if err := os.RemoveAll(dir); err != nil { // RemoveAll removes a link, not what it points to
		log.Printf("project root %s: %v", dir, err)
	}
}

func safeEntryName(n string) bool {
	return n != "" && n != "." && n != ".." && !strings.HasPrefix(n, ".") && !strings.ContainsAny(n, "/\\\x00") &&
		!slices.ContainsFunc(rootDocs, func(d string) bool { return strings.EqualFold(d, n) })
}

func readRootManifest(dir string) rootManifestFile {
	var m rootManifestFile
	b, err := os.ReadFile(filepath.Join(dir, rootManifestDir, rootManifest))
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeRootManifest(dir, pid string, entries []rootEntry) error {
	md := filepath.Join(dir, rootManifestDir)
	if fi, err := os.Lstat(md); err == nil && (!fi.IsDir() || fi.Mode()&os.ModeSymlink != 0) {
		_ = os.Remove(md)
	}
	if err := os.MkdirAll(md, 0o700); err != nil {
		return err
	}
	if entries == nil {
		entries = []rootEntry{}
	}
	b, _ := json.MarshalIndent(rootManifestFile{ProjectID: pid, Entries: entries}, "", "  ")
	return writeIfChanged(filepath.Join(md, rootManifest), append(b, '\n'), 0o600)
}

// writeIfChanged writes a file through a temp file and a rename, only when its content or mode
// would change. Never through a link someone put at that path.
func writeIfChanged(p string, data []byte, mode os.FileMode) error {
	if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm() == mode {
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
			return nil
		}
	}
	tmp := filepath.Join(filepath.Dir(p), ".nb-tmp-"+randomHex(6))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// rootMay is the "You may" of a part, for an agent at the root on This PC (§21.1).
func rootMay(rp rootPart) string {
	name := fmt.Sprintf("part: %q", rp.pt.Name)
	switch {
	case rp.own:
		return "change it with your own tools, in ./" + rp.entry.Name
	case rp.change:
		return "read: read_file, grep, glob with " + name + "; change: write_file, edit_file, bash with " + name
	}
	return "read only: read_file, grep, glob with " + name
}

// rootDoc is AGENTS.md and CLAUDE.md of a root: the same for every root console of the project.
func rootDoc(pr project.Project, parts []rootPart) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — project root (made by NextBoard)\n\n", flatLine(pr.Name))
	b.WriteString("This folder is a view of the project: one entry per part. Files stay on their machines; nothing here is a copy. NextBoard keeps this file current: read it again if a part seems missing.\n\n")
	b.WriteString("| Part | Machine | Folder | Here | You may |\n|---|---|---|---|---|\n")
	var notes, servers []string
	for _, rp := range parts {
		here := rp.here
		if here == "" {
			here = "— (something else has its name here)"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", cell(rp.pt.Name), cell(rp.machine), cell(rp.pt.Path), cell(here), cell(rootMay(rp)))
		if !rp.own {
			notes = append(notes, "./"+rp.entry.Name)
		}
		if !rp.local {
			servers = append(servers, rp.pt.Name)
		}
	}
	b.WriteString("\n")
	var ways []string
	for _, rp := range parts {
		if rp.own {
			ways = append(ways, fmt.Sprintf("`cd %s && …`", rp.entry.Name))
			break
		}
	}
	for _, rp := range parts {
		if rp.change {
			ways = append(ways, fmt.Sprintf("bash with part: %q on its server", rp.pt.Name))
			break
		}
	}
	if len(ways) > 0 {
		fmt.Fprintf(&b, "- Work about one part happens in that part. A command that needs a part's folder runs there: %s.\n", strings.Join(ways, ", or "))
	}
	if len(notes) > 0 {
		fmt.Fprintf(&b, "- %s: only notes, not those folders. Never write there. Their files are reachable only through the nextboard tools, with `part`.\n", strings.Join(notes, ", "))
		b.WriteString("- An agent without the nextboard tools (a plain shell, or a CLI NextBoard gives no tools) cannot reach those parts: ask the owner.\n")
	}
	b.WriteString("- Each part has its own instructions. Before working in a part, read its CLAUDE.md or AGENTS.md")
	var have []string
	for _, rp := range parts {
		for _, d := range rp.docs {
			have = append(have, "./"+rp.entry.Name+"/"+d)
		}
	}
	if len(have) > 0 {
		b.WriteString(" (here: " + strings.Join(have, ", ") + ")")
	}
	if len(servers) > 0 {
		fmt.Fprintf(&b, "; on a server: read_file {part: %q, path: \"CLAUDE.md\"} or AGENTS.md", servers[0])
	}
	b.WriteString(".\n")
	b.WriteString("- Commands the owner blocked in a part need the owner's approval there; at the root, every part's list applies to your own shell.\n")
	b.WriteString("- Git: each part is its own repository. The root itself is not one.\n")
	return b.String()
}

// rootNoteText is NEXTBOARD-PART.md in a part's note folder: the row of its part, in prose.
func rootNoteText(pr project.Project, rp rootPart) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — a part of project %s (made by NextBoard)\n\n", flatLine(rp.pt.Name), flatLine(pr.Name))
	if rp.local {
		fmt.Fprintf(&b, "This folder is only a note. %s is a read-only part on This PC, in %s; nothing of it is here.\n\n", rp.pt.Name, rp.pt.Path)
	} else {
		fmt.Fprintf(&b, "This folder is only a note. %s's files are on %s, in %s; nothing of them is copied here, and nothing written here would reach them.\n\n", rp.pt.Name, rp.machine, rp.pt.Path)
	}
	b.WriteString("Do not write in this folder: NextBoard removes what is put here.\n\n")
	fmt.Fprintf(&b, "Read it with the nextboard tools read_file, grep and glob, giving part: %q.", rp.pt.Name)
	if rp.change {
		fmt.Fprintf(&b, " Change it with write_file, edit_file and bash, giving part: %q.", rp.pt.Name)
	} else {
		b.WriteString(" It is read only for agents at the project root.")
	}
	b.WriteString(" Paths there start in its folder.\n")
	return b.String()
}

func flatLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// cell is text for one table cell.
func cell(s string) string { return strings.ReplaceAll(flatLine(s), "|", `\|`) }

// rootPrompt is the launch prompt of a root console: a pointer to the instruction file, which
// the CLI loads from its folder by itself.
func (s *Server) rootPrompt(pr project.Project, claude bool) string {
	doc := "./AGENTS.md"
	if claude {
		doc = "./CLAUDE.md"
	}
	n := fmt.Sprintf("%d parts", len(pr.Parts))
	if len(pr.Parts) == 1 {
		n = "1 part"
	}
	return fmt.Sprintf("You work at the root of project %s, which spans %s. %s lists them and how to reach each; NextBoard keeps it current. Give `part` to every nextboard file tool (read_file, write_file, edit_file, grep, glob, bash).\n",
		flatLine(pr.Name), n, doc)
}

// rootAddDirs are the real folders of the parts a root console changes with its own tools.
func rootAddDirs(pr project.Project) []string {
	var out []string
	for _, pt := range pr.Parts {
		if pt.MachineID != machine.LocalID || pt.ReadOnly {
			continue
		}
		p := filepath.Clean(pt.Path)
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// rootBlocked is what needs the owner's approval in a root console's own shell: the hub's list,
// the project's and every part's, since it can cd into any part.
func (s *Server) rootBlocked(pid string) []string {
	var out []string
	if s.orch != nil {
		out = s.orch.BlockedCommands(pid)
	}
	if pr, ok := s.projects.Get(pid); ok {
		for _, pt := range pr.Parts {
			for _, c := range pt.Blocked {
				if !slices.Contains(out, c) {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// rootFilePart maps a file a root console's own tool named to the part it is in and its real
// path: through the root's entries (./Web/x → Web's folder), else by its real path. false for a
// file of no part.
func (s *Server) rootFilePart(sp session.Spec, p string) (project.Part, string, bool) {
	pr, ok := s.projects.Get(sp.ProjectID)
	if !ok {
		return project.Part{}, "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(sp.Cwd, p)
	}
	p = filepath.Clean(p)
	if rel, err := filepath.Rel(filepath.Clean(sp.Cwd), p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		name, rest, _ := strings.Cut(rel, string(filepath.Separator))
		for _, e := range readRootManifest(sp.Cwd).Entries {
			if e.Name != name || e.Kind != rootLink {
				continue
			}
			if pt, ok := pr.PartByID(e.PartID); ok && pt.MachineID == machine.LocalID {
				return pt, filepath.Join(pt.Path, rest), true
			}
		}
	}
	real := p
	if r, err := filepath.EvalSymlinks(p); err == nil {
		real = r
	} else if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		real = filepath.Join(r, filepath.Base(p)) // a file about to be made
	}
	for _, cand := range []string{p, real} {
		if pt, ok := pr.PartAt(machine.LocalID, cand); ok {
			return pt, cand, true
		}
	}
	// a part's folder that is itself behind a link
	for _, pt := range pr.Parts {
		if pt.MachineID != machine.LocalID {
			continue
		}
		if rp, err := filepath.EvalSymlinks(pt.Path); err == nil && (real == rp || strings.HasPrefix(real, rp+string(filepath.Separator))) {
			return pt, filepath.Join(pt.Path, strings.TrimPrefix(real, rp)), true
		}
	}
	return project.Part{}, "", false
}

// rootSandboxRule is sandboxRule at the root: the parts on This PC it may change are where it
// writes.
func rootSandboxRule(network bool) string {
	r := "Your shell commands run in a sandbox: they may write only in the parts on This PC you may change (through their links here), the temp folder and package caches"
	if !network {
		return r + ", and they have no network. If a task needs more, say so instead of working around it."
	}
	return r + ". If a task needs to write elsewhere, say so instead of working around it."
}
