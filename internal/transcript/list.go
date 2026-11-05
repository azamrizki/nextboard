package transcript

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"

	"nextboard/internal/machine"
)

// ListConversations lists the conversations of cwd for the resume picker.
// Claude: <dir>/projects/<slug(cwd)>/*.jsonl (dir = config dir); Codex:
// the newest ≤ MaxScanFiles rollouts under <dir>/sessions (dir = codex
// home) whose session_meta.cwd is cwd. Only HeaderBytes of each file are
// read; the scan stops after ScanBudget. limit is 1–200 (default 50).
func ListConversations(ctx context.Context, h machine.Host, kind, dir, cwd string, limit int, attached func(agentSessionID string) string) (*ConversationList, error) {
	if kind != KindClaude && kind != KindCodex {
		return nil, ErrUnknownKind
	}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	ctx, cancel := context.WithTimeout(ctx, ScanBudget)
	defer cancel()
	cwd = path.Clean(cwd)
	out := &ConversationList{Items: []Conversation{}}

	type file struct {
		path string
		e    machine.DirEntry
	}
	var files []file
	if kind == KindClaude {
		folder := path.Join(dir, "projects", ClaudeSlug(cwd))
		es, err := listFiles(ctx, h, folder, func(n string) bool { return strings.HasSuffix(n, ".jsonl") })
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			files = append(files, file{path.Join(folder, e.Name), e})
		}
		sort.Slice(files, func(i, j int) bool { return files[i].e.Mtime.After(files[j].e.Mtime) })
		if len(files) > MaxScanFiles {
			files, out.Truncated = files[:MaxScanFiles], true
		}
	} else {
		days, err := codexDayDirs(ctx, h, dir, 1<<20)
		if err != nil {
			return nil, err
		}
	walk:
		for _, d := range days {
			es, err := listFiles(ctx, h, d, isRollout)
			if err != nil {
				if ctx.Err() != nil {
					out.Truncated = true
					break
				}
				return nil, err
			}
			// Names embed the start time: newest first within the day.
			sort.Slice(es, func(i, j int) bool { return es[i].Name > es[j].Name })
			for _, e := range es {
				if len(files) >= MaxScanFiles {
					out.Truncated = true
					break walk
				}
				files = append(files, file{path.Join(d, e.Name), e})
			}
		}
	}

	for _, f := range files {
		if ctx.Err() != nil {
			out.Truncated = true
			break
		}
		hdr, err := readHeader(ctx, h, f.path)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				out.Truncated = true
				break
			}
			continue // vanished or unreadable
		}
		out.Scanned++
		c := Conversation{UpdatedAt: f.e.Mtime, SizeBytes: f.e.Size, Path: f.path}
		if kind == KindClaude {
			p := ParseClaude(hdr, false)
			c.AgentSessionID = strings.TrimSuffix(path.Base(f.path), ".jsonl")
			c.Title, c.StartedAt = claudeTitle(hdr), p.StartedAt
			c.Messages = countTurns(p.Messages)
		} else {
			id, mcwd, started, ok := codexMeta(hdr)
			if !ok || path.Clean(mcwd) != cwd {
				continue
			}
			p := ParseCodex(hdr, false)
			c.AgentSessionID, c.Title, c.StartedAt = id, p.Title, started
			c.Messages = countTurns(p.Messages)
		}
		if c.StartedAt.IsZero() {
			c.StartedAt = c.UpdatedAt
		}
		if attached != nil {
			if s := attached(c.AgentSessionID); s != "" {
				c.AttachedTo = &s
			}
		}
		out.Items = append(out.Items, c)
	}
	sort.SliceStable(out.Items, func(i, j int) bool { return out.Items[i].UpdatedAt.After(out.Items[j].UpdatedAt) })
	if len(out.Items) > limit {
		out.Items, out.Truncated = out.Items[:limit], true
	}
	return out, nil
}

// countTurns counts user and assistant messages outside sidechains.
func countTurns(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		if !m.Sidechain && (m.Role == "user" || m.Role == "assistant") {
			n++
		}
	}
	return n
}
