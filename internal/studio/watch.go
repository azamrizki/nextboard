package studio

import (
	"context"
	"errors"
	"sync"
	"time"

	"nextboard/internal/auth"
	"nextboard/internal/machine"
)

// The live file watch (E5 "changes by an agent show up within 3 s") polls
// stats; there is no inotify on remote machines and nothing is installed.
// Clients may poll fs/stat-batch themselves (§8) or register a watch: the
// hub stats the paths every WatchInterval while the watch is renewed and
// publishes {"type":"fsChanged","watchId","machineId","changes":[Stat…]}.

// Limits.
const (
	StatBatchMax   = 200
	WatchMaxPaths  = 200
	WatchMaxActive = 64
	WatchTTL       = 30 * time.Second
	WatchMaxTTL    = 5 * time.Minute
)

var (
	ErrTooManyPaths = errors.New("terlalu banyak path (maks 200)")
	ErrWatchLimit   = errors.New("terlalu banyak watch aktif")
	ErrWatchUnknown = errors.New("watch tidak ditemukan atau kedaluwarsa")
)

// StatResult is one entry of fs/stat-batch (Error set when the stat
// failed; the entry then has only Path).
type StatResult struct {
	machine.FileStat
	Error string `json:"error,omitempty"`
}

// StatBatch stats every path (in order), never failing as a whole.
func StatBatch(ctx context.Context, h machine.Host, paths []string) ([]StatResult, error) {
	if len(paths) > StatBatchMax {
		return nil, ErrTooManyPaths
	}
	out := make([]StatResult, len(paths))
	for i, p := range paths {
		st, err := h.Stat(ctx, p)
		if err != nil {
			out[i] = StatResult{FileStat: machine.FileStat{Path: p}, Error: err.Error()}
			continue
		}
		out[i] = StatResult{FileStat: *st}
	}
	return out, nil
}

type statKey struct {
	exists bool
	dir    bool
	size   int64
	mtime  time.Time
}

type watch struct {
	id, machineID string
	paths         []string
	last          map[string]statKey
	expires       time.Time
	busy          bool
}

// Watcher polls registered watches.
type Watcher struct {
	Host     func(id string) (machine.Host, bool)
	Publish  func(v any)
	Interval time.Duration

	mu      sync.Mutex
	watches map[string]*watch
	quit    chan struct{}
	once    sync.Once
}

func NewWatcher() *Watcher {
	return &Watcher{Interval: 3 * time.Second, watches: map[string]*watch{}, quit: make(chan struct{})}
}

// Start runs the poll loop until Close.
func (w *Watcher) Start() {
	go func() {
		t := time.NewTicker(w.Interval)
		defer t.Stop()
		for {
			select {
			case <-w.quit:
				return
			case <-t.C:
				w.poll()
			}
		}
	}()
}

func (w *Watcher) Close() { w.once.Do(func() { close(w.quit) }) }

func key(st StatResult) statKey {
	return statKey{exists: st.Exists, dir: st.Dir, size: st.Size, mtime: st.Mtime}
}

// Add registers a watch and returns its id and the current stats.
func (w *Watcher) Add(ctx context.Context, machineID string, paths []string, ttl time.Duration) (string, []StatResult, error) {
	if len(paths) == 0 || len(paths) > WatchMaxPaths {
		return "", nil, ErrTooManyPaths
	}
	h, ok := w.Host(machineID)
	if !ok {
		return "", nil, machine.ErrNotFound
	}
	stats, err := StatBatch(ctx, h, paths)
	if err != nil {
		return "", nil, err
	}
	if ttl <= 0 {
		ttl = WatchTTL
	}
	ttl = min(ttl, WatchMaxTTL)
	wt := &watch{id: auth.RandomID("fw_"), machineID: machineID, paths: append([]string(nil), paths...),
		last: map[string]statKey{}, expires: time.Now().Add(ttl)}
	for i, s := range stats {
		wt.last[paths[i]] = key(s)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.expireLocked()
	if len(w.watches) >= WatchMaxActive {
		return "", nil, ErrWatchLimit
	}
	w.watches[wt.id] = wt
	return wt.id, stats, nil
}

// Renew extends a watch.
func (w *Watcher) Renew(id string, ttl time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.expireLocked()
	wt, ok := w.watches[id]
	if !ok {
		return ErrWatchUnknown
	}
	if ttl <= 0 {
		ttl = WatchTTL
	}
	wt.expires = time.Now().Add(min(ttl, WatchMaxTTL))
	return nil
}

// Remove drops a watch.
func (w *Watcher) Remove(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.watches[id]
	delete(w.watches, id)
	return ok
}

func (w *Watcher) expireLocked() {
	now := time.Now()
	for id, wt := range w.watches {
		if now.After(wt.expires) {
			delete(w.watches, id)
		}
	}
}

// poll stats every live watch once (a slow machine skips a round instead
// of piling up).
func (w *Watcher) poll() {
	w.mu.Lock()
	w.expireLocked()
	var due []*watch
	for _, wt := range w.watches {
		if !wt.busy {
			wt.busy = true
			due = append(due, wt)
		}
	}
	w.mu.Unlock()
	for _, wt := range due {
		go w.pollOne(wt)
	}
}

func (w *Watcher) pollOne(wt *watch) {
	defer func() {
		w.mu.Lock()
		wt.busy = false
		w.mu.Unlock()
	}()
	h, ok := w.Host(wt.machineID)
	if !ok || !h.Connected() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), max(w.Interval*3, 5*time.Second))
	defer cancel()
	stats, err := StatBatch(ctx, h, wt.paths)
	if err != nil {
		return
	}
	var changes []StatResult
	w.mu.Lock()
	for i, s := range stats {
		if s.Error != "" {
			continue
		}
		k := key(s)
		if wt.last[wt.paths[i]] != k {
			wt.last[wt.paths[i]] = k
			changes = append(changes, s)
		}
	}
	_, alive := w.watches[wt.id]
	w.mu.Unlock()
	if len(changes) > 0 && alive && w.Publish != nil {
		w.Publish(map[string]any{"type": "fsChanged", "watchId": wt.id, "machineId": wt.machineID, "changes": changes})
	}
}
