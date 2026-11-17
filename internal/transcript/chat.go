package transcript

import (
	"context"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// What the hub's chat index (docs/API-agents.md §29) reads of one conversation file: its picker
// entry, the messages added since it last looked, and a bounded text match for its search.

// Describe is the picker entry of one conversation file (ListConversations reads every file of
// a folder the same way): its title and start from the first HeaderBytes, its size and time.
// Messages is counted in the header, a lower bound for a big file.
func Describe(ctx context.Context, h machine.Host, kind, p string) (Conversation, error) {
	if kind != KindClaude && kind != KindCodex {
		return Conversation{}, ErrUnknownKind
	}
	st, err := h.Stat(ctx, p)
	if err != nil {
		return Conversation{}, err
	}
	if !st.Exists || st.Dir {
		return Conversation{}, fs.ErrNotExist
	}
	hdr, err := readHeader(ctx, h, p)
	if err != nil {
		return Conversation{}, err
	}
	c, _ := describeHeader(kind, p, st.Mtime, st.Size, hdr)
	return c, nil
}

// describeHeader is the picker entry of a file from its first HeaderBytes, and for Codex the
// folder its session ran in.
func describeHeader(kind, p string, mtime time.Time, size int64, hdr []byte) (Conversation, string) {
	c := Conversation{UpdatedAt: mtime, SizeBytes: size, Path: p}
	cwd := ""
	if kind == KindClaude {
		ps := ParseClaude(hdr, false)
		c.AgentSessionID = strings.TrimSuffix(path.Base(p), ".jsonl")
		c.Title, c.StartedAt = claudeTitleOf(ps), ps.StartedAt
		c.Messages = countTurns(ps.Messages)
	} else {
		id, mcwd, started, _ := codexMeta(hdr)
		ps := ParseCodex(hdr, false)
		c.AgentSessionID, c.Title, c.StartedAt = id, ps.Title, started
		c.Messages = countTurns(ps.Messages)
		cwd = mcwd
	}
	if c.StartedAt.IsZero() {
		c.StartedAt = c.UpdatedAt
	}
	return c, cwd
}

// Found is a conversation ScanPlaces found, and the folder (one of those asked for) it ran in.
type Found struct {
	Conversation
	Cwd string
}

// ScanPlaces finds the conversations kept in dir (a Claude config dir or a Codex home) that ran
// in one of cwds: the chat index's backfill looks at every place of a project in one go. Claude
// keeps a folder per cwd, of which the newest perCwd files are read; Codex keeps its rollouts by
// day, of which the newest MaxScanFiles are looked at once for all cwds. known(path, mtime)
// leaves out a file the caller has already read as it is now, and read tells it which files were
// read (the Codex ones of other folders too). Only the header of a file is read. truncated: the
// scan stopped early (ctx done, the file cap).
func ScanPlaces(ctx context.Context, h machine.Host, kind, dir string, cwds []string, perCwd int,
	known func(p string, mtime time.Time) bool, read func(p string, mtime time.Time)) ([]Found, bool, error) {
	if kind != KindClaude && kind != KindCodex {
		return nil, false, ErrUnknownKind
	}
	if perCwd <= 0 {
		perCwd = 50
	}
	var out []Found
	// look reads one file; false: the scan is out of time
	look := func(p string, e machine.DirEntry, want func(cwd string) (string, bool)) bool {
		if known != nil && known(p, e.Mtime) {
			return true
		}
		hdr, err := readHeader(ctx, h, p)
		if err != nil {
			return ctx.Err() == nil // vanished or unreadable: the next one
		}
		if read != nil {
			read(p, e.Mtime)
		}
		c, mcwd := describeHeader(kind, p, e.Mtime, e.Size, hdr)
		if cwd, ok := want(mcwd); ok && c.AgentSessionID != "" {
			out = append(out, Found{Conversation: c, Cwd: cwd})
		}
		return true
	}
	if kind == KindClaude {
		truncated := false
		for _, cwd := range cwds {
			folder := path.Join(dir, "projects", ClaudeSlug(path.Clean(cwd)))
			es, err := listFiles(ctx, h, folder, func(n string) bool { return strings.HasSuffix(n, ".jsonl") })
			if err != nil {
				if ctx.Err() != nil {
					return out, true, nil
				}
				continue
			}
			sort.Slice(es, func(i, j int) bool { return es[i].Mtime.After(es[j].Mtime) })
			if len(es) > perCwd {
				es, truncated = es[:perCwd], true
			}
			for _, e := range es {
				if !look(path.Join(folder, e.Name), e, func(string) (string, bool) { return cwd, true }) {
					return out, true, nil
				}
			}
		}
		return out, truncated, nil
	}
	want := map[string]string{}
	for _, c := range cwds {
		want[path.Clean(c)] = c
	}
	match := func(mcwd string) (string, bool) {
		if mcwd == "" {
			return "", false
		}
		c, ok := want[path.Clean(mcwd)]
		return c, ok
	}
	days, err := codexDayDirs(ctx, h, dir, 1<<20)
	if err != nil {
		return nil, ctx.Err() != nil, err
	}
	n := 0
	for _, d := range days {
		es, err := listFiles(ctx, h, d, isRollout)
		if err != nil {
			return out, true, nil
		}
		// names embed the start time: newest first within the day
		sort.Slice(es, func(i, j int) bool { return es[i].Name > es[j].Name })
		for _, e := range es {
			if n >= MaxScanFiles {
				return out, true, nil
			}
			n++
			if !look(path.Join(d, e.Name), e, match) {
				return out, true, nil
			}
		}
	}
	return out, false, nil
}

// Mark is the last message the chat index counted: its id, and its time for a message whose id
// is made up from its line in what was read (Codex's own input has none).
type Mark struct {
	ID string    `json:"id,omitempty"`
	At time.Time `json:"at,omitzero"`
}

// stable reports whether the mark's id names the same message in any read of the file.
func (m Mark) stable() bool { return m.ID != "" && !strings.HasPrefix(m.ID, "line-") }

// Count is what CountAfter found.
type Count struct {
	// New: the user and assistant messages after the mark when Since, else every one read.
	New   int
	Since bool
	// Partial: the file is longer than what was read, so without Since, New is a lower bound.
	Partial bool
	Next    Mark
}

// CountAfter counts the user and assistant messages (outside sidechains) after mark. The tail
// it reads grows from ReplyTailStart until it reaches back to the mark (at most TailMax), so a
// count after each turn reads little. A zero mark, or one no longer in the file, counts what was
// read instead (Since false).
func CountAfter(ctx context.Context, h machine.Host, kind, p string, mark Mark) (Count, error) {
	stable := mark.stable()
	reached := func(ps *Parsed) bool {
		for _, m := range ps.Messages {
			if stable && m.ID == mark.ID {
				return true
			}
			if !stable && !mark.At.IsZero() && !m.At.IsZero() && !m.At.After(mark.At) {
				return true
			}
		}
		return false
	}
	ps, partial, err := parseTailFrom(ctx, h, kind, p, ReplyTailStart, 4, func(ps *Parsed, _ int64) bool { return reached(ps) })
	if err != nil {
		return Count{}, err
	}
	var msgs []Message
	for _, m := range ps.Messages {
		if !m.Sidechain && (m.Role == "user" || m.Role == "assistant") {
			msgs = append(msgs, m)
		}
	}
	c := Count{New: len(msgs), Partial: partial, Next: mark}
	switch {
	case stable:
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].ID == mark.ID {
				c.New, c.Since = len(msgs)-i-1, true
				break
			}
		}
	case !mark.At.IsZero() && reached(ps):
		n := 0
		for _, m := range msgs {
			if m.At.After(mark.At) {
				n++
			}
		}
		c.New, c.Since = n, true
	}
	if len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		c.Next = Mark{ID: last.ID, At: last.At}
	}
	return c, nil
}

// searchTail is how much of a transcript's end Contains looks at, next to its header.
const searchTail = 256 << 10

// Contains reports whether a user or assistant message in the first HeaderBytes or the last
// searchTail bytes of the transcript holds q, ignoring case: a bounded look for the chats' search
// box, not a full search of a long conversation.
func Contains(ctx context.Context, h machine.Host, kind, p, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" || (kind != KindClaude && kind != KindCodex) {
		return false
	}
	if takeSlot(ctx) != nil {
		return false
	}
	defer giveSlot()
	has := func(ps *Parsed) bool {
		for _, m := range ps.Messages {
			if (m.Role == "user" || m.Role == "assistant") && strings.Contains(strings.ToLower(m.Text), q) {
				return true
			}
		}
		return false
	}
	hdr, err := readHeader(ctx, h, p)
	if err != nil {
		return false
	}
	if ps, _ := Parse(kind, hdr, false); ps != nil && has(ps) {
		return true
	}
	if len(hdr) < HeaderBytes {
		return false // the header was all of it
	}
	data, _, cut, err := readTail(ctx, h, p, searchTail)
	if err != nil {
		return false
	}
	ps, _ := Parse(kind, data, cut)
	return ps != nil && has(ps)
}
