package chats

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// What a chat that resumes is told was added to the project's memory since it last ran. The
// memory files keep no history, and agents write MEMORY.md themselves (Claude Code's own memory),
// so the index looks at the memory at every turn's end in the project and notes when it first saw
// each line. A line it saw at or before a chat's last message was there for that chat.

// MemoryDocs are the memory documents to look at: "main" and part ids, to their text.
type MemoryDocs map[string]string

// FactLines are the lines of a memory document that hold something: not headings, front matter,
// fences, comments or blank lines. A list item is given without its marker.
func FactLines(content string) []string {
	var out []string
	inFence, front := false, false
	for i, l := range strings.Split(content, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case i == 0 && t == "---":
			front = true
			continue
		case front:
			if t == "---" {
				front = false
			}
			continue
		case strings.HasPrefix(t, "```"):
			inFence = !inFence
			continue
		case inFence, t == "", strings.HasPrefix(t, "#"), strings.HasPrefix(t, "<!--"), t == "---":
			continue
		}
		for _, p := range []string{"- [ ] ", "- [x] ", "- ", "* ", "+ "} {
			if s, ok := strings.CutPrefix(t, p); ok {
				t = strings.TrimSpace(s)
				break
			}
		}
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func memKey(doc, line string) string {
	sum := sha256.Sum256([]byte(doc + "\x00" + line))
	return hex.EncodeToString(sum[:8])
}

// ObserveMemory notes the lines of docs not seen before as first seen at. docs holds the documents
// that were read now ("" for one that is not there); one left out (unreadable, or not looked at) is
// left as it was. A document read for the first time, the first look at a project's memory too,
// was there before: its lines are known. Lines no longer in a document that was read are
// forgotten.
func (x *Index) ObserveMemory(pid string, docs MemoryDocs, at time.Time) {
	if !projectIDRe.MatchString(pid) {
		return
	}
	x.mu.Lock()
	f := x.fileLocked(pid)
	changed := !f.MemoryObserved
	if f.Memory == nil {
		f.Memory = map[string]memLine{}
	}
	if f.MemoryDocs == nil {
		f.MemoryDocs = map[string]bool{}
	}
	now := map[string]bool{}
	for doc, content := range docs {
		first := !f.MemoryDocs[doc]
		if first {
			f.MemoryDocs[doc], changed = true, true
		}
		for _, l := range FactLines(content) {
			k := memKey(doc, l)
			now[k] = true
			if _, ok := f.Memory[k]; ok {
				continue
			}
			ts := at.UnixMilli()
			if first {
				ts = 0
			}
			f.Memory[k] = memLine{At: ts, Doc: doc}
			changed = true
		}
	}
	for k, m := range f.Memory {
		if _, read := docs[m.Doc]; read && !now[k] {
			delete(f.Memory, k)
			changed = true
		}
	}
	f.MemoryObserved = true
	x.mu.Unlock()
	if changed {
		x.save(pid)
	}
}

// MemoryAddedSince are the lines of docs first seen after since, at most n, in the order of the
// documents given (main first, then the parts in keys order).
func (x *Index) MemoryAddedSince(pid string, docs MemoryDocs, keys []string, since time.Time, n int) []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	f := x.files[pid]
	if f == nil || !f.MemoryObserved {
		return nil
	}
	var out []string
	for _, doc := range keys {
		for _, l := range FactLines(docs[doc]) {
			m, ok := f.Memory[memKey(doc, l)]
			if ok && m.At > 0 && m.At > since.UnixMilli() {
				out = append(out, l)
				if len(out) >= n {
					return out
				}
			}
		}
	}
	return out
}
