package usage

import (
	"sync"
	"time"
)

// The reset chime (docs/API-studio.md §10.2, F3): a timer at resetsAt +
// 30 s for every window of the latest reports. When it fires and
// settings.usage.chime allows it (enabled; play "always", or "after100" and
// the window was at 100 %), the hub publishes usageReset; clients play the
// bundled sound or post a notification.

// chimeDelay is how long after the reset the event fires.
const chimeDelay = 30 * time.Second

// chimeLate: a reset missed while the hub was down still chimes at start
// when it is at most this old.
const chimeLate = 10 * time.Minute

// Clock is injectable for tests.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a stoppable timer.
type Timer interface{ Stop() bool }

type realClock struct{}

func (realClock) Now() time.Time                            { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

type chime struct {
	clock    Clock
	settings func() Settings
	publish  func(v any)

	mu     sync.Mutex
	timers map[string]map[string]Timer // report key → window id → timer
	fired  map[string]bool             // report key|window|resetsAt
}

func newChime(c Clock, settings func() Settings, publish func(v any)) *chime {
	return &chime{clock: c, settings: settings, publish: publish, timers: map[string]map[string]Timer{}, fired: map[string]bool{}}
}

// arm replaces the timers of r's key with one per window that has a reset
// time.
func (c *chime) arm(r Report) {
	k := r.key()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.timers[k] {
		t.Stop()
	}
	delete(c.timers, k)
	if r.State != "ok" {
		return
	}
	now := c.clock.Now()
	for _, w := range r.Windows {
		if w.ResetsAt == nil {
			continue
		}
		at := w.ResetsAt.Add(chimeDelay)
		fk := k + "|" + w.ID + "|" + w.ResetsAt.UTC().Format(time.RFC3339)
		if c.fired[fk] {
			continue
		}
		d := at.Sub(now)
		if d < -chimeLate {
			continue
		}
		w := w
		if c.timers[k] == nil {
			c.timers[k] = map[string]Timer{}
		}
		c.timers[k][w.ID] = c.clock.AfterFunc(max(d, 0), func() { c.fire(r, w, fk) })
	}
}

func (c *chime) fire(r Report, w Window, fk string) {
	c.mu.Lock()
	if c.fired[fk] {
		c.mu.Unlock()
		return
	}
	c.fired[fk] = true
	if m := c.timers[r.key()]; m != nil {
		delete(m, w.ID)
	}
	c.mu.Unlock()
	st := c.settings()
	if !st.ChimeEnabled {
		return
	}
	if st.ChimePlay != "always" && w.UsedPct < 100 {
		return
	}
	c.publish(map[string]any{"type": "usageReset", "machineId": r.MachineID, "agent": r.Agent, "profileId": r.ProfileID,
		"window": w.ID, "at": w.ResetsAt.UTC(), "sound": st.ChimeSound})
}

func (c *chime) stopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.timers {
		for _, t := range m {
			t.Stop()
		}
	}
	c.timers = map[string]map[string]Timer{}
}
