package uptime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"nextboard/internal/store"
)

// What a monitor remembers, in its own folder of append-only files (the store's
// JSON files are rewritten whole, which a heartbeat a minute should not do):
//
//	beats.jsonl   every check of the last days
//	hours.jsonl   one line per closed hour, for a year
//	events.jsonl  each time it went down or came back
//
// A line torn by a crash is skipped on load. A beat is on disk before anyone hears of it,
// and an hour not written yet is counted again from the beats.

const (
	statusDown    int8 = 0
	statusUp      int8 = 1
	statusPending int8 = 2

	keepBeats   = 3 * 24 * time.Hour
	keepHours   = 366 * 24 * time.Hour
	keepEvents  = 200
	compactEach = 6 * time.Hour
)

// Beat is one check. A beat that went fine keeps no message, which is most of them.
type Beat struct {
	T  int64  `json:"t"` // unix ms
	S  int8   `json:"s"` // 0 down, 1 up, 2 pending
	Ms int32  `json:"ms"`
	M  string `json:"m,omitempty"`
}

// Hour sums up one hour.
type Hour struct {
	T       int64 `json:"t"` // unix seconds, start of the hour
	Up      int32 `json:"up"`
	Down    int32 `json:"down"`
	Pending int32 `json:"pending,omitempty"`
	MsSum   int64 `json:"msSum"`
	MsN     int32 `json:"msN"`
	MsMin   int32 `json:"msMin"`
	MsMax   int32 `json:"msMax"`
}

func (h *Hour) add(b Beat) {
	switch b.S {
	case statusUp:
		h.Up++
	case statusDown:
		h.Down++
	default:
		h.Pending++
	}
	if b.Ms < 0 {
		return
	}
	if h.MsN == 0 || b.Ms < h.MsMin {
		h.MsMin = b.Ms
	}
	if h.MsN == 0 || b.Ms > h.MsMax {
		h.MsMax = b.Ms
	}
	h.MsSum += int64(b.Ms)
	h.MsN++
}

// Event is one change a person cares about.
type Event struct {
	T      int64  `json:"t"` // unix ms
	Status string `json:"status"`
	Msg    string `json:"msg,omitempty"`
	// DurSec is how long the previous state lasted.
	DurSec int64 `json:"durSec,omitempty"`
}

type history struct {
	dir string

	beats  []Beat // oldest first
	hours  []Hour // oldest first, the last one may still be open
	events []Event

	savedHour   int64 // the newest hour on disk
	nextCompact time.Time
}

func readLines[T any](path string, fn func(T)) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		n++
		var v T
		if json.Unmarshal(sc.Bytes(), &v) == nil {
			fn(v)
		}
	}
	return n
}

func appendLine(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

func writeLines[T any](path string, list []T) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, v := range list {
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	return store.WriteFileAtomic(path, buf.Bytes())
}

// loadHistory reads what is on disk and brings the files back down to what is kept.
func loadHistory(dir string, now time.Time) (*history, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	h := &history{dir: dir}
	readLines(filepath.Join(dir, "hours.jsonl"), func(v Hour) {
		if len(h.hours) == 0 || v.T > h.hours[len(h.hours)-1].T {
			h.hours = append(h.hours, v)
		}
	})
	if n := len(h.hours); n > 0 {
		h.savedHour = h.hours[n-1].T
	}
	readLines(filepath.Join(dir, "beats.jsonl"), func(v Beat) {
		if v.T/1000/3600*3600 > h.savedHour {
			h.count(v)
		}
		h.beats = append(h.beats, v)
	})
	readLines(filepath.Join(dir, "events.jsonl"), func(v Event) { h.events = append(h.events, v) })
	h.compact(now)
	return h, nil
}

func (h *history) path(name string) string { return filepath.Join(h.dir, name) }

// count puts a beat in its hour.
func (h *history) count(b Beat) {
	t := b.T / 1000 / 3600 * 3600
	if n := len(h.hours); n == 0 || h.hours[n-1].T < t {
		h.hours = append(h.hours, Hour{T: t})
	} else if h.hours[n-1].T > t {
		return // the clock went back: not worth rewriting closed hours for
	}
	h.hours[len(h.hours)-1].add(b)
}

// add keeps a beat: on disk first, closing the hours that ended before it.
func (h *history) add(b Beat, now time.Time) error {
	if err := appendLine(h.path("beats.jsonl"), b); err != nil {
		return err
	}
	h.beats = append(h.beats, b)
	h.count(b)
	for i := range h.hours {
		e := h.hours[i]
		if e.T <= h.savedHour || i == len(h.hours)-1 {
			continue
		}
		if err := appendLine(h.path("hours.jsonl"), e); err != nil {
			return err
		}
		h.savedHour = e.T
	}
	if now.After(h.nextCompact) {
		h.compact(now)
	}
	return nil
}

func (h *history) addEvent(e Event) error {
	if err := appendLine(h.path("events.jsonl"), e); err != nil {
		return err
	}
	h.events = append(h.events, e)
	if len(h.events) > 2*keepEvents {
		h.events = append([]Event(nil), h.events[len(h.events)-keepEvents:]...)
		return writeLines(h.path("events.jsonl"), h.events)
	}
	return nil
}

// compact drops what has aged out, from memory and from the files. Failing is harmless:
// the same lines are dropped next time.
func (h *history) compact(now time.Time) {
	h.nextCompact = now.Add(compactEach)
	cut := now.Add(-keepBeats).UnixMilli()
	i := 0
	for i < len(h.beats) && h.beats[i].T < cut {
		i++
	}
	h.beats = append([]Beat(nil), h.beats[i:]...)
	_ = writeLines(h.path("beats.jsonl"), h.beats)

	cutHour := now.Add(-keepHours).Unix()
	i = 0
	for i < len(h.hours) && h.hours[i].T < cutHour {
		i++
	}
	h.hours = append([]Hour(nil), h.hours[i:]...)
	var closed []Hour
	for _, e := range h.hours {
		if e.T <= h.savedHour {
			closed = append(closed, e)
		}
	}
	_ = writeLines(h.path("hours.jsonl"), closed)

	if len(h.events) > keepEvents {
		h.events = append([]Event(nil), h.events[len(h.events)-keepEvents:]...)
		_ = writeLines(h.path("events.jsonl"), h.events)
	}
}

// recent is the last n beats, oldest first.
func (h *history) recent(n int) []Beat {
	if len(h.beats) > n {
		return append([]Beat(nil), h.beats[len(h.beats)-n:]...)
	}
	return append([]Beat(nil), h.beats...)
}

// Stats sums up a stretch of time.
type Stats struct {
	// Uptime is up / (up + down) in percent, nil without checks. A check waiting for its
	// retry is neither: a blip that passes is what retries are for. Time without checks
	// (the hub was off, the monitor paused) does not count either way.
	Uptime *float64 `json:"uptime"`
	AvgMs  int      `json:"avgMs"`
	MinMs  int      `json:"minMs"`
	MaxMs  int      `json:"maxMs"`
	Checks int      `json:"checks"`
}

func (s *Stats) finish(h Hour) {
	s.Checks = int(h.Up + h.Down + h.Pending)
	if h.Up+h.Down > 0 {
		v := float64(h.Up) * 100 / float64(h.Up+h.Down)
		s.Uptime = &v
	}
	if h.MsN > 0 {
		s.AvgMs, s.MinMs, s.MaxMs = int(h.MsSum/int64(h.MsN)), int(h.MsMin), int(h.MsMax)
	}
}

func merge(a *Hour, b Hour) {
	if b.MsN > 0 {
		if a.MsN == 0 || b.MsMin < a.MsMin {
			a.MsMin = b.MsMin
		}
		if a.MsN == 0 || b.MsMax > a.MsMax {
			a.MsMax = b.MsMax
		}
	}
	a.Up += b.Up
	a.Down += b.Down
	a.Pending += b.Pending
	a.MsSum += b.MsSum
	a.MsN += b.MsN
}

// over sums up the last d: exact from the beats while they reach that far, otherwise by
// whole hours.
func (h *history) over(d time.Duration, now time.Time) Stats {
	var sum Hour
	if d <= keepBeats {
		cut := now.Add(-d).UnixMilli()
		for i := len(h.beats) - 1; i >= 0 && h.beats[i].T >= cut; i-- {
			sum.add(h.beats[i])
		}
	} else {
		cut := now.Add(-d).Unix()
		for i := len(h.hours) - 1; i >= 0 && h.hours[i].T+3600 > cut; i-- {
			merge(&sum, h.hours[i])
		}
	}
	var s Stats
	s.finish(sum)
	return s
}

// Point is one column of the response time chart. Empty ones are left out.
type Point struct {
	T       int64 `json:"t"` // unix ms, start of the step
	Up      int32 `json:"up"`
	Down    int32 `json:"down"`
	Pending int32 `json:"pending,omitempty"`
	AvgMs   int   `json:"avgMs"` // -1 without a measured time
	MinMs   int   `json:"minMs"`
	MaxMs   int   `json:"maxMs"`
}

// Series is one range of the chart.
type Series struct {
	Range   string  `json:"range"`
	From    int64   `json:"from"` // unix ms
	To      int64   `json:"to"`
	StepSec int     `json:"stepSec"`
	Points  []Point `json:"points"`
	Stats   Stats   `json:"stats"`
}

// Ranges are what the chart offers: length and step.
var Ranges = map[string]struct {
	D    time.Duration
	Step time.Duration
}{
	"1h":  {time.Hour, time.Minute},
	"24h": {24 * time.Hour, 10 * time.Minute},
	"7d":  {7 * 24 * time.Hour, time.Hour},
	"30d": {30 * 24 * time.Hour, 6 * time.Hour},
	"1y":  {365 * 24 * time.Hour, 24 * time.Hour},
}

func (h *history) series(name string, now time.Time) Series {
	rg := Ranges[name]
	step := int64(rg.Step / time.Second)
	from := now.Add(-rg.D).Unix()
	from -= from % step
	sums := map[int64]*Hour{}
	bucket := func(t int64) *Hour {
		k := t - t%step
		b := sums[k]
		if b == nil {
			b = &Hour{}
			sums[k] = b
		}
		return b
	}
	if rg.D <= keepBeats {
		for _, b := range h.beats {
			if b.T/1000 >= from {
				bucket(b.T / 1000).add(b)
			}
		}
	} else {
		for _, e := range h.hours {
			if e.T >= from {
				merge(bucket(e.T), e)
			}
		}
	}
	out := Series{Range: name, From: from * 1000, To: now.UnixMilli(), StepSec: int(step), Points: []Point{}, Stats: h.over(rg.D, now)}
	for t := from; t <= now.Unix(); t += step {
		b := sums[t]
		if b == nil {
			continue
		}
		p := Point{T: t * 1000, Up: b.Up, Down: b.Down, Pending: b.Pending, AvgMs: -1}
		if b.MsN > 0 {
			p.AvgMs, p.MinMs, p.MaxMs = int(b.MsSum/int64(b.MsN)), int(b.MsMin), int(b.MsMax)
		}
		out.Points = append(out.Points, p)
	}
	return out
}
