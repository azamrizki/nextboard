package preview

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// LogEntry is one console/error/network record of the bridge (B19 log
// card).
type LogEntry struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	Type    string    `json:"type"` // console | error | net
	Level   string    `json:"level,omitempty"`
	Args    []string  `json:"args,omitempty"`
	Message string    `json:"message,omitempty"`
	Source  string    `json:"source,omitempty"`
	Line    int       `json:"line,omitempty"`
	Col     int       `json:"col,omitempty"`
	Stack   string    `json:"stack,omitempty"`
	Method  string    `json:"method,omitempty"`
	URL     string    `json:"url,omitempty"`
	Status  int       `json:"status,omitempty"`
	Ms      int       `json:"ms,omitempty"`
	PageURL string    `json:"page,omitempty"`
}

const (
	maxBeacon   = 64 << 10
	maxArg      = 2 << 10
	maxArgs     = 20
	publishEach = 250 * time.Millisecond // ≤ 4 previewLog events/s
)

// logRing keeps the last maxLogs entries.
type logRing struct {
	items []LogEntry
	seq   int64
}

func (r *logRing) add(e LogEntry) LogEntry {
	r.seq++
	e.Seq = r.seq
	r.items = append(r.items, e)
	if len(r.items) > maxLogs {
		r.items = append([]LogEntry(nil), r.items[len(r.items)-maxLogs:]...)
	}
	return e
}

func (r *logRing) since(seq int64) []LogEntry {
	out := []LogEntry{}
	for _, e := range r.items {
		if e.Seq > seq {
			out = append(out, e)
		}
	}
	return out
}

// rateLimit allows max events per window.
type rateLimit struct {
	mu    sync.Mutex
	per   time.Duration
	max   int
	start time.Time
	n     int
}

func (l *rateLimit) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.start) >= l.per {
		l.start, l.n = now, 0
	}
	if l.n >= l.max {
		return false
	}
	l.n++
	return true
}

func clipStr(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// clean validates and clips a bridge entry.
func (e LogEntry) clean() (LogEntry, bool) {
	out := LogEntry{Type: e.Type, Ms: e.Ms, Status: e.Status, Line: e.Line, Col: e.Col}
	switch e.Type {
	case "console":
		switch e.Level {
		case "log", "info", "warn", "error", "debug":
			out.Level = e.Level
		default:
			out.Level = "log"
		}
		for i, a := range e.Args {
			if i >= maxArgs {
				break
			}
			out.Args = append(out.Args, clipStr(a, maxArg))
		}
	case "error":
		out.Message = clipStr(e.Message, maxArg)
		out.Source = clipStr(e.Source, 512)
		out.Stack = clipStr(e.Stack, 4*maxArg)
	case "net":
		out.Method = clipStr(strings.ToUpper(e.Method), 16)
		out.URL = clipStr(e.URL, 1024)
	default:
		return LogEntry{}, false
	}
	out.PageURL = clipStr(e.PageURL, 1024)
	return out, true
}

// serveLog accepts a beacon batch from the bridge (≤ 64 KB, ≤ 10/s).
func (p *preview) serveLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !p.beacon.allow(p.m.now()) {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBeacon))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	var batch struct {
		Entries []LogEntry `json:"entries"`
	}
	if json.Unmarshal(body, &batch) != nil {
		var arr []LogEntry
		if json.Unmarshal(body, &arr) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		batch.Entries = arr
	}
	now := p.m.now().UTC()
	var added []LogEntry
	p.m.mu.Lock()
	for i, e := range batch.Entries {
		if i >= 200 {
			break
		}
		c, ok := e.clean()
		if !ok {
			continue
		}
		c.At = now
		added = append(added, p.logs.add(c))
	}
	p.pubLogs = append(p.pubLogs, added...)
	p.schedulePublishLocked()
	p.m.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// schedulePublishLocked coalesces previewLog events (≤ 4/s).
func (p *preview) schedulePublishLocked() {
	if len(p.pubLogs) == 0 || p.pubT != nil {
		return
	}
	wait := publishEach - p.m.now().Sub(p.lastPub)
	if wait < 0 {
		wait = 0
	}
	p.pubT = time.AfterFunc(wait, func() {
		p.m.mu.Lock()
		entries := p.pubLogs
		p.pubLogs, p.pubT, p.lastPub = nil, nil, p.m.now()
		id := p.ID
		p.m.mu.Unlock()
		if len(entries) > 0 && p.m.cfg.Publish != nil {
			p.m.cfg.Publish(map[string]any{"type": "previewLog", "previewId": id, "entries": entries})
		}
	})
}

// Logs returns entries after since and the latest seq.
func (m *Manager) Logs(id string, since int64) ([]LogEntry, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return nil, 0, ErrNotFound
	}
	return p.logs.since(since), p.logs.seq, nil
}

func sortPreviews(ps []Preview) {
	sort.Slice(ps, func(i, k int) bool {
		if !ps[i].CreatedAt.Equal(ps[k].CreatedAt) {
			return ps[i].CreatedAt.Before(ps[k].CreatedAt)
		}
		return ps[i].ID < ps[k].ID
	})
}
