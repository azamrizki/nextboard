// Package hostres reads what This PC, the computer that runs the hub, has to give: CPU, memory,
// GPUs, temperatures and disks, and which consoles use the most of it (docs/API.md, Host
// resources). It reads what the system offers without anything installed: /proc and /sys on
// Linux (other systems report Supported false). A sampler keeps the last hour.
package hostres

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"math"
	"slices"
	"sync"
	"time"
)

// Snapshot is one reading of This PC.
type Snapshot struct {
	At        time.Time `json:"at"`
	Supported bool      `json:"supported"`
	OS        string    `json:"os"`
	Hostname  string    `json:"hostname"`
	UptimeSec int64     `json:"uptimeSec"`
	CPU       CPU       `json:"cpu"`
	Memory    Memory    `json:"memory"`
	GPUs      []GPU     `json:"gpus"`
	Temps     []Temp    `json:"temps"`
	Disks     []Disk    `json:"disks"`
	// Hottest is the highest temperature of the CPU, GPUs and disks (nil: no sensor).
	Hottest *Temp `json:"hottest"`
	// Level is ok, warn or hot by the owner's thresholds; Why says what is over them.
	Level string `json:"level"`
	Why   string `json:"why"`
	// Consoles are what each console uses, heaviest first; only while someone looks. Those of
	// another NextBoard on This PC are among them: the hub names its own.
	Consoles []Console `json:"consoles,omitempty"`
}

// CPU is the processor's load over the last interval.
type CPU struct {
	Model   string     `json:"model"`
	Threads int        `json:"threads"`
	Percent float64    `json:"percent"`
	Cores   []float64  `json:"cores"`
	Load    [3]float64 `json:"load"`
}

// Memory in bytes. Used is what programs hold: total less available.
type Memory struct {
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	Available uint64 `json:"available"`
	Cached    uint64 `json:"cached"`
	SwapTotal uint64 `json:"swapTotal"`
	SwapUsed  uint64 `json:"swapUsed"`
}

// Percent is the share of memory programs hold.
func (m Memory) Percent() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.Used) * 100 / float64(m.Total)
}

// GPU is one graphics card; what its driver does not tell is nil.
type GPU struct {
	Name   string `json:"name"`
	Vendor string `json:"vendor"` // intel | amd | nvidia | other
	Driver string `json:"driver"`
	// Busy is the share of time it worked: amdgpu's and nvidia-smi's own figure, for Intel the
	// time out of its idle state (RC6).
	Busy      *float64 `json:"busy"`
	TempC     *float64 `json:"tempC"`
	PowerW    *float64 `json:"powerW"`
	VRAMUsed  *uint64  `json:"vramUsed"`
	VRAMTotal *uint64  `json:"vramTotal"`
}

// Temp is one sensor: the one a device is known by (a CPU's Tctl, a disk's Composite).
type Temp struct {
	Kind   string  `json:"kind"` // cpu | gpu | disk | board | other
	Label  string  `json:"label"`
	C      float64 `json:"c"`
	Sensor string  `json:"sensor"` // the driver, e.g. k10temp
}

// Disk is the filesystem of a folder that matters: the hub's data folder and /.
type Disk struct {
	Path  string `json:"path"`
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

// Console is what the processes of one console use together.
type Console struct {
	SessionID string  `json:"sessionId"`
	CPU       float64 `json:"cpu"`
	RSS       uint64  `json:"rss"`
	Procs     int     `json:"procs"`
}

// Point is one minute's worth of history: the reading kept every HistoryStep.
type Point struct {
	At  int64    `json:"at"` // unix seconds
	CPU float64  `json:"cpu"`
	Mem float64  `json:"mem"`
	GPU *float64 `json:"gpu"`
	// Temp is the hottest sensor then.
	Temp *float64 `json:"temp"`
}

// Thresholds are where a reading turns amber (warn) and red (hot).
type Thresholds struct {
	WarnTempC  float64
	HotTempC   float64
	WarnMemPct float64
	HotMemPct  float64
}

// DefaultThresholds are the settings' defaults.
var DefaultThresholds = Thresholds{WarnTempC: 80, HotTempC: 90, WarnMemPct: 85, HotMemPct: 95}

// busyCPU is a CPU that is warn after busyFor at this load.
const (
	busyCPU = 90
	busyFor = time.Minute
)

// History is kept for an hour, one point every HistoryStep.
const (
	HistoryStep = 15 * time.Second
	historyLen  = int(time.Hour / HistoryStep)
)

// How often the sampler reads: while someone looks at the details, while a page with the
// header is open, and otherwise (for the history only).
var (
	WatchedEvery = 2 * time.Second
	OpenEvery    = 5 * time.Second
	IdleEvery    = 30 * time.Second
	// watchFor is how long one Watch keeps the fast pace.
	watchFor = 6 * time.Second
)

// reader is what reads the system; one per platform.
type reader interface {
	read(withConsoles bool) Snapshot
	// stuckAt is the file a read waits on now ("" none).
	stuckAt() string
}

// readTimeout: a read that takes this long waits on a driver that does not answer (an i915
// fan sensor read twice in a row did, for good). That file is not read again.
var readTimeout = 5 * time.Second

// badFiles are files a read got stuck on.
type badFiles struct {
	mu sync.Mutex
	l  map[string]bool
}

func (b *badFiles) add(p string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.l == nil {
		b.l = map[string]bool{}
	}
	b.l[p] = true
}

func (b *badFiles) has(p string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.l[p]
}

// Sampler reads This PC on its own pace and keeps the last hour.
type Sampler struct {
	// Open reports whether a page that shows the header is open (event subscribers).
	Open func() bool
	// Thresholds gives the owner's thresholds at each reading.
	Thresholds func() Thresholds
	// OnSample is called with each reading.
	OnSample func(Snapshot)

	r     reader
	newR  func() reader
	bad   *badFiles
	wake  chan struct{}
	mu    sync.Mutex
	last  Snapshot
	hist  []Point
	until time.Time // fast pace until then (Watch)
	busy  time.Time // since when the CPU is over busyCPU
}

// NewSampler reads This PC; dataDir is the hub's data folder, whose disk is shown.
func NewSampler(dataDir string) *Sampler {
	s := &Sampler{bad: &badFiles{}, wake: make(chan struct{}, 1)}
	s.newR = func() reader { return newReader(dataDir, s.bad) }
	s.r = s.newR()
	return s
}

// Run reads until ctx ends.
func (s *Sampler) Run(ctx context.Context) {
	for {
		s.sample()
		t := time.NewTimer(s.every())
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// Watch keeps the fast pace (and the consoles) for a few seconds: someone looks at the
// details. The first call of a while reads at once.
func (s *Sampler) Watch() {
	s.mu.Lock()
	idle := time.Now().After(s.until)
	s.until = time.Now().Add(watchFor)
	s.mu.Unlock()
	if idle {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *Sampler) watched() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.until)
}

func (s *Sampler) every() time.Duration {
	switch {
	case s.watched():
		return WatchedEvery
	case s.Open != nil && s.Open():
		return OpenEvery
	}
	return IdleEvery
}

// Latest is the last reading.
func (s *Sampler) Latest() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// History is the last hour, oldest first.
func (s *Sampler) History() []Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.hist)
}

// readOnce reads with readTimeout. A read that does not end leaves its reader to it: the file
// it waits on is skipped from then on, by a new reader.
func (s *Sampler) readOnce() (Snapshot, bool) {
	r, consoles := s.r, s.watched()
	done := make(chan Snapshot, 1)
	go func() { done <- r.read(consoles) }()
	t := time.NewTimer(readTimeout)
	defer t.Stop()
	select {
	case snap := <-done:
		return snap, true
	case <-t.C:
		p := r.stuckAt()
		if p != "" {
			s.bad.add(p)
		}
		log.Printf("host resources: reading %s took over %s; it is no longer read", cmp.Or(p, "the system"), readTimeout)
		s.r = s.newR()
		return Snapshot{}, false
	}
}

func (s *Sampler) sample() {
	snap, ok := s.readOnce()
	if !ok {
		return
	}
	th := DefaultThresholds
	if s.Thresholds != nil {
		th = s.Thresholds()
	}
	s.mu.Lock()
	if snap.CPU.Percent >= busyCPU {
		if s.busy.IsZero() {
			s.busy = snap.At
		}
	} else {
		s.busy = time.Time{}
	}
	busySince := s.busy
	s.mu.Unlock()
	snap.Level, snap.Why = levelOf(snap, th, busySince)
	s.mu.Lock()
	s.last = snap
	if n := len(s.hist); n == 0 || snap.At.Sub(time.Unix(s.hist[n-1].At, 0)) >= HistoryStep {
		s.hist = append(s.hist, pointOf(snap))
		if len(s.hist) > historyLen {
			s.hist = slices.Delete(s.hist, 0, len(s.hist)-historyLen)
		}
	}
	s.mu.Unlock()
	if s.OnSample != nil {
		s.OnSample(snap)
	}
}

func pointOf(s Snapshot) Point {
	p := Point{At: s.At.Unix(), CPU: round1(s.CPU.Percent), Mem: round1(s.Memory.Percent())}
	for _, g := range s.GPUs {
		if g.Busy != nil {
			v := round1(*g.Busy)
			p.GPU = &v
			break
		}
	}
	if s.Hottest != nil {
		v := round1(s.Hottest.C)
		p.Temp = &v
	}
	return p
}

// levelOf says how This PC is doing: hot from the temperature or memory alone, warn also from
// a CPU that has been over busyCPU for busyFor.
func levelOf(s Snapshot, th Thresholds, busySince time.Time) (string, string) {
	mem := s.Memory.Percent()
	var hot, warn []string
	if t := s.Hottest; t != nil {
		switch {
		case t.C >= th.HotTempC:
			hot = append(hot, fmt.Sprintf("%s %.0f °C", t.Label, t.C))
		case t.C >= th.WarnTempC:
			warn = append(warn, fmt.Sprintf("%s %.0f °C", t.Label, t.C))
		}
	}
	switch {
	case s.Memory.Total > 0 && mem >= th.HotMemPct:
		hot = append(hot, fmt.Sprintf("memory %.0f%%", mem))
	case s.Memory.Total > 0 && mem >= th.WarnMemPct:
		warn = append(warn, fmt.Sprintf("memory %.0f%%", mem))
	}
	if !busySince.IsZero() && s.At.Sub(busySince) >= busyFor {
		warn = append(warn, fmt.Sprintf("CPU %.0f%% for a minute", s.CPU.Percent))
	}
	switch {
	case len(hot) > 0:
		return "hot", join(append(hot, warn...))
	case len(warn) > 0:
		return "warn", join(warn)
	}
	return "ok", ""
}

func join(l []string) string {
	out := ""
	for i, x := range l {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// hottestOf is the hottest sensor of the CPU, a GPU or a disk.
func hottestOf(l []Temp) *Temp {
	var best *Temp
	for i := range l {
		switch l[i].Kind {
		case "cpu", "gpu", "disk":
		default:
			continue
		}
		if best == nil || l[i].C > best.C {
			best = &l[i]
		}
	}
	if best == nil {
		return nil
	}
	t := *best
	return &t
}

// Heaviest sorts consoles by CPU, then memory, and keeps n.
func Heaviest(l []Console, n int) []Console {
	slices.SortFunc(l, func(a, b Console) int {
		if c := cmp.Compare(b.CPU, a.CPU); c != 0 {
			return c
		}
		return cmp.Compare(b.RSS, a.RSS)
	})
	return l[:min(n, len(l))]
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
