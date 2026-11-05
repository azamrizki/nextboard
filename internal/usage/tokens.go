package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/store"
)

// Token history (docs/API-studio.md §10.3, F4): an incremental index per
// machine in usage/tokens/<machineId>.json. Claude
// <cfg>/projects/*/*.jsonl and Codex <home>/sessions/**/rollout-*.jsonl are
// read for the default login and every profile of the machine; only bytes
// after the stored offset are read (complete lines only). Claude usage is
// deduplicated per message.id (last 200 ids per file), Codex per
// response_id (token_usage_record) or per running total (token_count).
// Dates use the hub's time zone. This is not a bill.
//
// The index also keeps a few statistics per CLI (agentStats). Files indexed
// before those existed are read once more, for the statistics alone: their
// tokens are counted already, and the transcripts behind older days may be gone.

const (
	tokenBudget   = 30 * time.Second
	tokenRefresh  = 30 * time.Minute
	readChunk     = 4 << 20
	keepIDs       = 200
	maxIndexFiles = 20000
)

var machineIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type fileState struct {
	Agent     string    `json:"agent"`
	Size      int64     `json:"size"`
	Offset    int64     `json:"offset"`
	Mtime     time.Time `json:"mtime"`
	IDs       []string  `json:"ids,omitempty"`
	Mode      string    `json:"mode,omitempty"` // codex: records | counts
	LastTotal int64     `json:"lastTotal,omitempty"`
	Model     string    `json:"model,omitempty"` // of the latest turn
	First     int64     `json:"first,omitempty"` // unix seconds of the first and last turn
	Last      int64     `json:"last,omitempty"`
	// StatOffset trails Offset while the statistics catch up; Backfill is the
	// dedupe state of that second read.
	StatOffset int64      `json:"statOffset,omitempty"`
	Backfill   *fileState `json:"backfill,omitempty"`
}

// Tokens is one day×agent aggregate.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

func (t Tokens) total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

// agentStats outlive the transcripts they come from, like the day aggregates.
type agentStats struct {
	Models     map[string]int64 `json:"models,omitempty"` // tokens per model
	Sessions   int              `json:"sessions"`         // transcripts with at least one turn
	LongestSec int64            `json:"longestSec"`       // first to last turn of one transcript
}

type machineIndex struct {
	Files     map[string]*fileState         `json:"files"`
	Days      map[string]map[string]*Tokens `json:"days"` // date → agent → tokens
	Stats     map[string]*agentStats        `json:"stats,omitempty"`
	ScannedAt time.Time                     `json:"scannedAt"`
}

// noteTurn adds one counted turn of a transcript to the statistics.
func (ix *machineIndex) noteTurn(fs *fileState, model string, at time.Time, t Tokens) {
	a := ix.Stats[fs.Agent]
	if a == nil {
		a = &agentStats{Models: map[string]int64{}}
		ix.Stats[fs.Agent] = a
	}
	if model != "" {
		if a.Models == nil {
			a.Models = map[string]int64{}
		}
		a.Models[model] += t.total()
	}
	sec := at.Unix()
	if fs.First == 0 {
		a.Sessions++
		fs.First, fs.Last = sec, sec
	}
	fs.First, fs.Last = min(fs.First, sec), max(fs.Last, sec)
	a.LongestSec = max(a.LongestSec, fs.Last-fs.First)
}

type tokenIndex struct {
	st  *store.Store
	hub Hub

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	indexes map[string]*machineIndex

	quit chan struct{}
	once sync.Once
	// budget is the per-request scan budget (tests shorten it).
	budget time.Duration
}

func newTokenIndex(st *store.Store, hub Hub) *tokenIndex {
	return &tokenIndex{st: st, hub: hub, locks: map[string]*sync.Mutex{}, indexes: map[string]*machineIndex{},
		quit: make(chan struct{}), budget: tokenBudget}
}

func (x *tokenIndex) start() {
	go func() {
		t := time.NewTicker(tokenRefresh)
		defer t.Stop()
		for {
			select {
			case <-x.quit:
				return
			case <-t.C:
				for _, id := range x.connected() {
					ctx, cancel := context.WithTimeout(context.Background(), x.budget)
					if _, err := x.scan(ctx, id); err != nil {
						log.Printf("usage: indeks token %s: %v", id, err)
					}
					cancel()
				}
			}
		}
	}()
}

func (x *tokenIndex) stop() { x.once.Do(func() { close(x.quit) }) }

func (x *tokenIndex) connected() []string {
	var out []string
	for _, id := range x.hub.MachineIDs() {
		if h, ok := x.hub.Host(id); ok && h.Connected() {
			out = append(out, id)
		}
	}
	return out
}

func (x *tokenIndex) lock(id string) *sync.Mutex {
	x.mu.Lock()
	defer x.mu.Unlock()
	l, ok := x.locks[id]
	if !ok {
		l = &sync.Mutex{}
		x.locks[id] = l
	}
	return l
}

func indexFile(id string) string { return "usage/tokens/" + id + ".json" }

// load returns the index of a machine (from disk the first time). Caller
// holds the machine lock.
func (x *tokenIndex) load(id string) *machineIndex {
	x.mu.Lock()
	ix := x.indexes[id]
	x.mu.Unlock()
	if ix != nil {
		return ix
	}
	ix = &machineIndex{}
	_, _ = x.st.Load(indexFile(id), ix)
	if ix.Files == nil {
		ix.Files = map[string]*fileState{}
	}
	if ix.Days == nil {
		ix.Days = map[string]map[string]*Tokens{}
	}
	if ix.Stats == nil {
		ix.Stats = map[string]*agentStats{}
	}
	x.mu.Lock()
	x.indexes[id] = ix
	x.mu.Unlock()
	return ix
}

// source is one transcript file to index.
type source struct {
	path, agent string
	size        int64
	mtime       time.Time
}

// listSources finds the transcript files of a machine.
func (x *tokenIndex) listSources(ctx context.Context, h machine.Host, machineID string) ([]source, error) {
	dirs := x.hub.ProfileDirs(machineID)
	claude := append([]string{"~/.claude"}, dirs["claude"]...)
	codex := append([]string{"~/.codex"}, dirs["codex"]...)
	var out []source
	seenRoot := map[string]bool{}
	for _, cfg := range claude {
		root := path.Join(cfg, "projects")
		l, err := listDir(ctx, h, root)
		if err != nil || l == nil || seenRoot[l.Path] {
			continue
		}
		seenRoot[l.Path] = true
		for _, e := range l.Entries {
			if !e.Dir {
				continue
			}
			sub, err := listDir(ctx, h, path.Join(l.Path, e.Name))
			if err != nil || sub == nil {
				continue
			}
			for _, f := range sub.Entries {
				if !f.Dir && strings.HasSuffix(f.Name, ".jsonl") {
					out = append(out, source{path: path.Join(sub.Path, f.Name), agent: "claude", size: f.Size, mtime: f.Mtime})
				}
			}
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
		}
	}
	for _, home := range codex {
		root := path.Join(home, "sessions")
		l, err := listDir(ctx, h, root)
		if err != nil || l == nil || seenRoot[l.Path] {
			continue
		}
		seenRoot[l.Path] = true
		var walk func(dir string, depth int)
		walk = func(dir string, depth int) {
			if depth > 4 || ctx.Err() != nil {
				return
			}
			sub, err := listDir(ctx, h, dir)
			if err != nil || sub == nil {
				return
			}
			for _, f := range sub.Entries {
				switch {
				case f.Dir:
					walk(path.Join(sub.Path, f.Name), depth+1)
				case strings.HasPrefix(f.Name, "rollout-") && strings.HasSuffix(f.Name, ".jsonl"):
					out = append(out, source{path: path.Join(sub.Path, f.Name), agent: "codex", size: f.Size, mtime: f.Mtime})
				}
			}
		}
		walk(l.Path, 0)
	}
	if len(out) > maxIndexFiles {
		sort.Slice(out, func(i, j int) bool { return out[i].mtime.After(out[j].mtime) })
		out = out[:maxIndexFiles]
	}
	return out, ctx.Err()
}

// listDir lists dir or returns nil when it does not exist.
func listDir(ctx context.Context, h machine.Host, dir string) (*machine.DirListing, error) {
	st, err := h.Stat(ctx, dir)
	if err != nil || !st.Exists || !st.Dir {
		return nil, err
	}
	l, err := h.ListDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	for i, e := range l.Entries { // SSH exec fallback has no sizes
		if !e.Dir && e.Mtime.IsZero() {
			if fs, err := h.Stat(ctx, path.Join(l.Path, e.Name)); err == nil && fs.Exists {
				l.Entries[i].Size, l.Entries[i].Mtime = fs.Size, fs.Mtime
			}
		}
	}
	return l, nil
}

// scan reads new bytes of every transcript of a machine within ctx.
// partial reports that the budget ran out first.
func (x *tokenIndex) scan(ctx context.Context, machineID string) (partial bool, err error) {
	if !machineIDRe.MatchString(machineID) {
		return false, machine.ErrNotFound
	}
	h, ok := x.hub.Host(machineID)
	if !ok {
		return false, machine.ErrNotFound
	}
	if !h.Connected() {
		return false, ErrOffline
	}
	l := x.lock(machineID)
	l.Lock()
	defer l.Unlock()
	ix := x.load(machineID)
	defer func() {
		ix.ScannedAt = time.Now().UTC()
		if e := ensureDir(x.st, "usage/tokens"); e == nil {
			x.mu.Lock()
			_ = x.st.Save(indexFile(machineID), ix)
			x.mu.Unlock()
		}
	}()
	srcs, err := x.listSources(ctx, h, machineID)
	if ctx.Err() != nil {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	live := map[string]bool{}
	for _, s := range srcs {
		live[s.path] = true
	}
	x.mu.Lock()
	for p := range ix.Files {
		if !live[p] {
			delete(ix.Files, p) // aggregates stay (history is kept)
		}
	}
	x.mu.Unlock()
	for _, s := range srcs {
		if ctx.Err() != nil {
			return true, nil
		}
		x.mu.Lock()
		fs := ix.Files[s.path]
		x.mu.Unlock()
		if fs == nil {
			fs = &fileState{Agent: s.agent}
		}
		if fs.Size == s.size && fs.Offset == s.size && fs.StatOffset == s.size && fs.Mtime.Equal(s.mtime) {
			continue
		}
		if s.size < fs.Offset { // rewritten: start over (best-effort)
			fs = &fileState{Agent: s.agent}
		}
		if !x.backfill(ctx, h, ix, fs, s.path) {
			x.put(ix, s.path, fs)
			return true, nil
		}
		for fs.Offset < s.size {
			if ctx.Err() != nil {
				x.put(ix, s.path, fs)
				return true, nil
			}
			data, st, err := h.ReadAt(ctx, s.path, fs.Offset, min(int64(readChunk), s.size-fs.Offset))
			if err != nil || len(data) == 0 {
				break
			}
			cut := bytes.LastIndexByte(data, '\n')
			if cut < 0 {
				if int64(len(data)) >= readChunk { // a line longer than a chunk: skip it
					fs.Offset += int64(len(data))
					continue
				}
				break // incomplete last line: read again later
			}
			x.mu.Lock()
			x.consume(ix, fs, data[:cut+1])
			x.mu.Unlock()
			fs.Offset += int64(cut + 1)
			fs.StatOffset = fs.Offset
			if st != nil {
				s.size = max(s.size, st.Size)
			}
		}
		fs.Size, fs.Mtime = s.size, s.mtime
		x.put(ix, s.path, fs)
	}
	return false, nil
}

// backfill reads the indexed part of a file again for the statistics alone,
// until they have caught up with its tokens. False: the budget ran out first.
func (x *tokenIndex) backfill(ctx context.Context, h machine.Host, ix *machineIndex, fs *fileState, p string) bool {
	for fs.StatOffset < fs.Offset {
		if ctx.Err() != nil {
			return false
		}
		data, _, err := h.ReadAt(ctx, p, fs.StatOffset, min(int64(readChunk), fs.Offset-fs.StatOffset))
		if err != nil || len(data) == 0 {
			break // an unreadable file only costs its statistics
		}
		cut := bytes.LastIndexByte(data, '\n')
		if cut < 0 {
			fs.StatOffset += int64(len(data)) // a line longer than a chunk: tokens skip it too
			continue
		}
		if fs.Backfill == nil {
			fs.Backfill = &fileState{Agent: fs.Agent}
		}
		x.mu.Lock()
		for _, l := range bytes.Split(data[:cut+1], []byte{'\n'}) {
			if at, t, ok := parseTurn(fs.Backfill, bytes.TrimSpace(l)); ok {
				ix.noteTurn(fs, fs.Backfill.Model, at, t)
			}
		}
		x.mu.Unlock()
		fs.StatOffset += int64(cut + 1)
	}
	fs.StatOffset, fs.Backfill = fs.Offset, nil
	return true
}

func (x *tokenIndex) put(ix *machineIndex, p string, fs *fileState) {
	x.mu.Lock()
	ix.Files[p] = fs
	x.mu.Unlock()
}

// consume adds the usage of complete JSONL lines. Caller holds x.mu.
func (x *tokenIndex) consume(ix *machineIndex, fs *fileState, data []byte) {
	for _, l := range bytes.Split(data, []byte{'\n'}) {
		at, t, ok := parseTurn(fs, bytes.TrimSpace(l))
		if !ok {
			continue
		}
		ix.noteTurn(fs, fs.Model, at, t)
		date := at.In(time.Local).Format("2006-01-02")
		day := ix.Days[date]
		if day == nil {
			day = map[string]*Tokens{}
			ix.Days[date] = day
		}
		a := day[fs.Agent]
		if a == nil {
			a = &Tokens{}
			day[fs.Agent] = a
		}
		a.add(t)
	}
}

// parseTurn reads one line of a transcript; ok means a dated, counted turn.
func parseTurn(fs *fileState, l []byte) (at time.Time, t Tokens, ok bool) {
	if len(l) == 0 || l[0] != '{' {
		return at, t, false
	}
	if fs.Agent == "claude" {
		at, t, ok = claudeUsage(fs, l)
	} else {
		at, t, ok = codexUsage(fs, l)
	}
	return at, t, ok && !at.IsZero()
}

func remember(fs *fileState, id string) bool {
	for _, x := range fs.IDs {
		if x == id {
			return false
		}
	}
	fs.IDs = append(fs.IDs, id)
	if len(fs.IDs) > keepIDs {
		fs.IDs = fs.IDs[len(fs.IDs)-keepIDs:]
	}
	return true
}

func claudeUsage(fs *fileState, l []byte) (time.Time, Tokens, bool) {
	if !bytes.Contains(l, []byte(`"usage"`)) {
		return time.Time{}, Tokens{}, false
	}
	var ln struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage *struct {
				Input      int64 `json:"input_tokens"`
				Output     int64 `json:"output_tokens"`
				CacheRead  int64 `json:"cache_read_input_tokens"`
				CacheWrite int64 `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(l, &ln) != nil || ln.Type != "assistant" || ln.Message.Usage == nil || ln.Message.ID == "" || ln.Message.Model == "<synthetic>" {
		return time.Time{}, Tokens{}, false
	}
	if !remember(fs, ln.Message.ID) {
		return time.Time{}, Tokens{}, false
	}
	fs.Model = ln.Message.Model
	u := ln.Message.Usage
	at, _ := time.Parse(time.RFC3339Nano, ln.Timestamp)
	return at, Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}, true
}

type codexTok struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Total      int64 `json:"total_tokens"`
}

// tokens: Codex input_tokens include the cached part.
func (u codexTok) tokens() Tokens {
	return Tokens{Input: max(0, u.Input-u.Cached), Output: u.Output, CacheRead: u.Cached, CacheWrite: u.CacheWrite}
}

func codexUsage(fs *fileState, l []byte) (time.Time, Tokens, bool) {
	// The token lines don't name the model: the turn's context before them does.
	if bytes.Contains(l, []byte(`"turn_context"`)) {
		var c struct {
			Type    string `json:"type"`
			Payload struct {
				Model string `json:"model"`
			} `json:"payload"`
		}
		if json.Unmarshal(l, &c) == nil && c.Type == "turn_context" && c.Payload.Model != "" {
			fs.Model = c.Payload.Model
		}
		return time.Time{}, Tokens{}, false
	}
	if !bytes.Contains(l, []byte(`token_`)) {
		return time.Time{}, Tokens{}, false
	}
	var ln struct {
		Type       string    `json:"type"`
		Timestamp  string    `json:"timestamp"`
		ResponseID string    `json:"response_id"`
		Usage      *codexTok `json:"usage"`
		Payload    struct {
			Type string `json:"type"`
			Info *struct {
				Total *codexTok `json:"total_token_usage"`
				Last  *codexTok `json:"last_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(l, &ln) != nil {
		return time.Time{}, Tokens{}, false
	}
	at, _ := time.Parse(time.RFC3339Nano, ln.Timestamp)
	switch {
	case ln.Type == "token_usage_record" && ln.Usage != nil:
		fs.Mode = "records"
		if ln.ResponseID != "" && !remember(fs, ln.ResponseID) {
			return time.Time{}, Tokens{}, false
		}
		return at, ln.Usage.tokens(), true
	case ln.Type == "event_msg" && ln.Payload.Type == "token_count" && ln.Payload.Info != nil && ln.Payload.Info.Last != nil:
		if fs.Mode == "records" {
			return time.Time{}, Tokens{}, false
		}
		fs.Mode = "counts"
		total := int64(-1)
		if ln.Payload.Info.Total != nil {
			total = ln.Payload.Info.Total.Total
		}
		if total >= 0 && total == fs.LastTotal {
			return time.Time{}, Tokens{}, false // same running total re-announced
		}
		fs.LastTotal = total
		return at, ln.Payload.Info.Last.tokens(), true
	}
	return time.Time{}, Tokens{}, false
}

// SeriesPoint is one day×agent of the chart.
type SeriesPoint struct {
	Date  string `json:"date"`
	Agent string `json:"agent"`
	Tokens
}

// AgentTotals is one row of the by-agent list.
type AgentTotals struct {
	Agent  string `json:"agent"`
	Days30 struct {
		Total  int64 `json:"total"`
		Output int64 `json:"output"`
	} `json:"days30"`
	AllTime struct {
		Total int64 `json:"total"`
	} `json:"allTime"`
	Sessions          int    `json:"sessions"`
	FavoriteModel     string `json:"favoriteModel"` // most tokens
	LongestSessionSec int64  `json:"longestSessionSec"`
}

// TokenChart is GET /api/usage/tokens.
type TokenChart struct {
	From       string        `json:"from"`
	To         string        `json:"to"`
	Series     []SeriesPoint `json:"series"`
	ByAgent    []AgentTotals `json:"byAgent"`
	BusiestDay *struct {
		Date  string `json:"date"`
		Total int64  `json:"total"`
	} `json:"busiestDay"`
	Top       string    `json:"top"`
	ScannedAt time.Time `json:"scannedAt"`
	Partial   bool      `json:"partial"`
}

// chart scans the machines (within the request budget) and aggregates.
func (x *tokenIndex) chart(ctx context.Context, days int, machineIDs []string, agentID string) (*TokenChart, error) {
	if days <= 0 {
		days = 30
	}
	days = min(days, 366)
	if len(machineIDs) == 0 {
		machineIDs = x.connected()
	}
	ctx, cancel := context.WithTimeout(ctx, x.budget)
	defer cancel()
	out := &TokenChart{Series: []SeriesPoint{}, ByAgent: []AgentTotals{}}
	for _, id := range machineIDs {
		partial, err := x.scan(ctx, id)
		if err != nil && len(machineIDs) == 1 {
			return nil, err
		}
		out.Partial = out.Partial || partial
	}
	now := time.Now().In(time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	from := today.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	from30 := today.AddDate(0, 0, -29).Format("2006-01-02")
	to := today.Format("2006-01-02")
	out.From, out.To = from, to
	series := map[string]map[string]*Tokens{}
	byAgent := map[string]*AgentTotals{}
	models := map[string]map[string]int64{}
	for _, id := range machineIDs {
		l := x.lock(id)
		l.Lock()
		ix := x.load(id)
		x.mu.Lock()
		if ix.ScannedAt.After(out.ScannedAt) {
			out.ScannedAt = ix.ScannedAt
		}
		for ag, st := range ix.Stats {
			if agentID != "" && ag != agentID {
				continue
			}
			a := byAgent[ag]
			if a == nil {
				a = &AgentTotals{Agent: ag}
				byAgent[ag] = a
			}
			a.Sessions += st.Sessions
			a.LongestSessionSec = max(a.LongestSessionSec, st.LongestSec)
			if models[ag] == nil {
				models[ag] = map[string]int64{}
			}
			for m, n := range st.Models {
				models[ag][m] += n
			}
		}
		for date, per := range ix.Days {
			for ag, t := range per {
				if agentID != "" && ag != agentID {
					continue
				}
				a := byAgent[ag]
				if a == nil {
					a = &AgentTotals{Agent: ag}
					byAgent[ag] = a
				}
				a.AllTime.Total += t.total()
				if date >= from30 && date <= to {
					a.Days30.Total += t.total()
					a.Days30.Output += t.Output
				}
				if date < from || date > to {
					continue
				}
				if series[date] == nil {
					series[date] = map[string]*Tokens{}
				}
				if series[date][ag] == nil {
					series[date][ag] = &Tokens{}
				}
				series[date][ag].add(*t)
			}
		}
		x.mu.Unlock()
		l.Unlock()
	}
	dayTotal := map[string]int64{}
	agentTotal := map[string]int64{}
	for date, per := range series {
		for ag, t := range per {
			out.Series = append(out.Series, SeriesPoint{Date: date, Agent: ag, Tokens: *t})
			dayTotal[date] += t.total()
			agentTotal[ag] += t.total()
		}
	}
	sort.Slice(out.Series, func(i, j int) bool {
		if out.Series[i].Date != out.Series[j].Date {
			return out.Series[i].Date < out.Series[j].Date
		}
		return out.Series[i].Agent < out.Series[j].Agent
	})
	for date, t := range dayTotal {
		if t > 0 && (out.BusiestDay == nil || t > out.BusiestDay.Total || t == out.BusiestDay.Total && date > out.BusiestDay.Date) {
			out.BusiestDay = &struct {
				Date  string `json:"date"`
				Total int64  `json:"total"`
			}{date, t}
		}
	}
	var best int64
	for ag, t := range agentTotal {
		if t > best || t == best && ag < out.Top {
			best, out.Top = t, ag
		}
	}
	for _, a := range byAgent {
		var most int64
		for m, n := range models[a.Agent] {
			if n > most || n == most && m < a.FavoriteModel {
				most, a.FavoriteModel = n, m
			}
		}
		out.ByAgent = append(out.ByAgent, *a)
	}
	sort.Slice(out.ByAgent, func(i, j int) bool { return out.ByAgent[i].Agent < out.ByAgent[j].Agent })
	return out, nil
}
