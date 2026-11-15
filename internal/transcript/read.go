package transcript

import (
	"context"
	"io/fs"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// readTail returns the last n bytes of p. partial reports that the file
// is longer; cut that data starts inside a line (one byte before the
// tail is read to tell, so a tail starting on a line keeps that line).
func readTail(ctx context.Context, h machine.Host, p string, n int64) (data []byte, partial, cut bool, err error) {
	st, err := h.Stat(ctx, p)
	if err != nil {
		return nil, false, false, err
	}
	if !st.Exists {
		return nil, false, false, fs.ErrNotExist
	}
	if st.Dir {
		return nil, false, false, machine.ErrIsDir
	}
	off := max(0, st.Size-n)
	if off == 0 {
		data, _, err = h.ReadAt(ctx, p, 0, st.Size)
		return data, false, false, err
	}
	data, _, err = h.ReadAt(ctx, p, off-1, st.Size-off+1)
	if err != nil {
		return nil, false, false, err
	}
	if len(data) > 0 && data[0] == '\n' {
		return data[1:], true, false, nil
	}
	return data, true, true, nil
}

// tailSlots bounds the transcript tails read and parsed at once in the hub: each takes up to
// TailMax bytes and a few times that while it is decoded, and agents waking together (a
// project opened, a hub restart) would otherwise all read at the same moment.
var tailSlots = make(chan struct{}, 2)

func takeSlot(ctx context.Context) error {
	select {
	case tailSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func giveSlot() { <-tailSlots }

// parseTail reads a growing tail (TailStart, x2, up to TailMax) until done
// accepts the result or the whole file was read.
func parseTail(ctx context.Context, h machine.Host, kind, p string, done func(*Parsed) bool) (*Parsed, bool, error) {
	return parseTailFrom(ctx, h, kind, p, TailStart, 2, func(p *Parsed, _ int64) bool { return done(p) })
}

// parseTailFrom is parseTail starting at start bytes, the tail growing by factor; done also
// gets the size of the tail it looks at.
func parseTailFrom(ctx context.Context, h machine.Host, kind, p string, start, factor int64, done func(*Parsed, int64) bool) (*Parsed, bool, error) {
	if kind != KindClaude && kind != KindCodex {
		return nil, false, ErrUnknownKind
	}
	if err := takeSlot(ctx); err != nil {
		return nil, false, err
	}
	defer giveSlot()
	for n := start; ; n *= factor {
		n = min(n, TailMax)
		data, partial, cut, err := readTail(ctx, h, p, n)
		if err != nil {
			return nil, false, err
		}
		parsed, _ := Parse(kind, data, cut)
		if done(parsed, n) || !partial || n >= TailMax {
			return parsed, partial, nil
		}
	}
}

// ReadReply returns the agent's last reply in the transcript at path. Most replies are in the
// last ReplyTailStart bytes; the tail doubles until the turn of the reply starts in it, or is
// at least ReplyWholeAt (the line before the reply can be a tool result of many MB, which no
// tail holds whole).
func ReadReply(ctx context.Context, h machine.Host, kind, path string) (*Reply, error) {
	p, _, err := parseTailFrom(ctx, h, kind, path, ReplyTailStart, 2, func(p *Parsed, n int64) bool {
		return p.Reply != nil && (p.replyWhole || n >= ReplyWholeAt)
	})
	if err != nil {
		return nil, err
	}
	if p.Reply == nil {
		return nil, ErrNoReply
	}
	return p.Reply, nil
}

// ReadMessages returns the last `last` (1–200) messages. truncated means
// older messages exist beyond the ones returned.
func ReadMessages(ctx context.Context, h machine.Host, kind, path string, last int) ([]Message, bool, error) {
	last = max(1, min(last, 200))
	p, partial, err := parseTail(ctx, h, kind, path, func(p *Parsed) bool { return len(p.Messages) >= last })
	if err != nil {
		return nil, false, err
	}
	msgs := p.Messages
	truncated := partial || len(msgs) > last
	if len(msgs) > last {
		msgs = msgs[len(msgs)-last:]
	}
	if msgs == nil {
		msgs = []Message{}
	}
	return msgs, truncated, nil
}

// ReadTokens returns the usage of assistant messages after since (all when
// since is zero), within the last TailMax bytes of the transcript.
func ReadTokens(ctx context.Context, h machine.Host, kind, path string, since time.Time) ([]TokenUsage, error) {
	p, _, err := parseTail(ctx, h, kind, path, func(p *Parsed) bool {
		// Enough once the oldest usage read is not after since.
		return !since.IsZero() && len(p.Usage) > 0 && !p.Usage[0].At.After(since)
	})
	if err != nil {
		return nil, err
	}
	out := []TokenUsage{}
	for _, u := range p.Usage {
		if since.IsZero() || u.At.After(since) {
			out = append(out, u)
		}
	}
	return out, nil
}
