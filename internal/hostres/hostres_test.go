package hostres

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLevel(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	snap := func(tempC, memPct, cpu float64) Snapshot {
		s := Snapshot{At: now, CPU: CPU{Percent: cpu}, Memory: Memory{Total: 1000, Used: uint64(memPct * 10)}}
		if tempC > 0 {
			s.Hottest = &Temp{Kind: "cpu", Label: "CPU", C: tempC}
		}
		return s
	}
	th := DefaultThresholds
	for _, c := range []struct {
		name      string
		s         Snapshot
		busySince time.Time
		level     string
		why       string
	}{
		{"all well", snap(62, 43, 20), time.Time{}, "ok", ""},
		{"warm", snap(80, 43, 20), time.Time{}, "warn", "CPU 80 °C"},
		{"hot", snap(91, 43, 20), time.Time{}, "hot", "CPU 91 °C"},
		{"memory nearly full", snap(62, 86, 20), time.Time{}, "warn", "memory 86%"},
		{"memory full, and warm", snap(81, 96, 20), time.Time{}, "hot", "memory 96%, CPU 81 °C"},
		{"busy for a moment", snap(62, 43, 97), now.Add(-30 * time.Second), "ok", ""},
		{"busy for a minute", snap(62, 43, 97), now.Add(-time.Minute), "warn", "CPU 97% for a minute"},
		{"no sensors, no memory read", Snapshot{At: now}, time.Time{}, "ok", ""},
	} {
		level, why := levelOf(c.s, th, c.busySince)
		if level != c.level || why != c.why {
			t.Errorf("%s: %s %q, want %s %q", c.name, level, why, c.level, c.why)
		}
	}
}

func TestHottestLeavesOutBoardAndOthers(t *testing.T) {
	l := []Temp{{Kind: "board", Label: "Board", C: 95}, {Kind: "other", Label: "wifi", C: 99}, {Kind: "disk", Label: "NVMe", C: 49}, {Kind: "gpu", Label: "GPU", C: 55}}
	if h := hottestOf(l); h == nil || h.Label != "NVMe" && h.Label != "GPU" || h.C != 55 {
		t.Fatalf("%+v", h)
	}
	if hottestOf([]Temp{{Kind: "board", C: 50}}) != nil {
		t.Fatal("a board alone is no hottest")
	}
}

// fakeReader gives readings the test sets; block makes the next read wait until released.
type fakeReader struct {
	mu    sync.Mutex
	at    time.Time
	cpu   float64
	reads int
	block chan struct{}
	stuck string
	asked []bool
}

func (f *fakeReader) read(consoles bool) Snapshot {
	f.mu.Lock()
	b := f.block
	f.reads++
	f.asked = append(f.asked, consoles)
	s := Snapshot{At: f.at, Supported: true, CPU: CPU{Percent: f.cpu}, Memory: Memory{Total: 100, Used: 40}}
	f.mu.Unlock()
	if b != nil {
		<-b
	}
	return s
}

func (f *fakeReader) stuckAt() string { return f.stuck }

func TestSamplerHistoryAndPace(t *testing.T) {
	f := &fakeReader{at: time.Unix(1_800_000_000, 0)}
	s := &Sampler{bad: &badFiles{}, wake: make(chan struct{}, 1), r: f}
	s.newR = func() reader { return f }
	var got []Snapshot
	s.OnSample = func(x Snapshot) { got = append(got, x) }

	// a reading every 5 s for a minute: one point every HistoryStep
	for i := range 13 {
		f.at = time.Unix(1_800_000_000+int64(i*5), 0)
		s.sample()
	}
	if h := s.History(); len(h) != 5 || h[1].At-h[0].At != int64(HistoryStep/time.Second) || h[0].Mem != 40 {
		t.Fatalf("history %+v", h)
	}
	if len(got) != 13 || s.Latest().Level != "ok" {
		t.Fatalf("samples %d %+v", len(got), s.Latest())
	}
	// an hour at most
	for i := range historyLen + 20 {
		f.at = time.Unix(1_900_000_000+int64(i)*int64(HistoryStep/time.Second), 0)
		s.sample()
	}
	if h := s.History(); len(h) != historyLen || h[len(h)-1].At != f.at.Unix() {
		t.Fatalf("history kept %d, last %d", len(h), h[len(h)-1].At)
	}

	// the pace: idle, a page open, someone looking (with the consoles)
	open := false
	s.Open = func() bool { return open }
	if s.every() != IdleEvery {
		t.Fatalf("idle pace %s", s.every())
	}
	open = true
	if s.every() != OpenEvery {
		t.Fatalf("open pace %s", s.every())
	}
	s.Watch()
	if s.every() != WatchedEvery {
		t.Fatalf("watched pace %s", s.every())
	}
	select {
	case <-s.wake:
	default:
		t.Fatal("Watch after a while does not read at once")
	}
	s.Watch()
	select {
	case <-s.wake:
		t.Fatal("Watch while watched reads again at once")
	default:
	}
	s.sample()
	if !f.asked[len(f.asked)-1] {
		t.Fatal("a watched reading does not ask for the consoles")
	}
}

func TestSamplerStuckRead(t *testing.T) {
	old := readTimeout
	readTimeout = 50 * time.Millisecond
	defer func() { readTimeout = old }()
	stuck := &fakeReader{at: time.Unix(1_800_000_000, 0), block: make(chan struct{}), stuck: "/sys/class/drm/card1/device/hwmon/hwmon5/fan1_input"}
	fresh := &fakeReader{at: time.Unix(1_800_000_010, 0), cpu: 12}
	s := &Sampler{bad: &badFiles{}, wake: make(chan struct{}, 1), r: stuck}
	s.newR = func() reader { return fresh }
	s.sample()
	if !s.bad.has(stuck.stuck) || s.r != fresh || !s.Latest().At.IsZero() {
		t.Fatalf("after a stuck read: bad %v, reader swapped %v, latest %+v", s.bad.has(stuck.stuck), s.r == fresh, s.Latest())
	}
	close(stuck.block)
	s.sample()
	if s.Latest().CPU.Percent != 12 {
		t.Fatalf("the next reader's reading %+v", s.Latest())
	}
}

func TestHeaviest(t *testing.T) {
	l := Heaviest([]Console{{SessionID: "a", CPU: 1, RSS: 10}, {SessionID: "b", CPU: 5}, {SessionID: "c", CPU: 1, RSS: 99}, {SessionID: "d"}}, 3)
	var ids []string
	for _, c := range l {
		ids = append(ids, c.SessionID)
	}
	if strings.Join(ids, ",") != "b,c,a" {
		t.Fatalf("%v", ids)
	}
}
