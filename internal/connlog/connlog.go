// Package connlog keeps a small lasting record of how connections to servers
// behave, to learn from what happens on real networks (docs/API.md, connection
// log). One JSON line per event in <data>/logs/connections.jsonl, rotated once,
// so it never holds more than about 8 MiB. It records what happened to the link,
// never what went over it: no commands, output, file contents or paths.
package connlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Event kinds.
const (
	HubStart      = "hub_start"
	HubStop       = "hub_stop"
	HubUnclean    = "hub_unclean_exit" // the last run ended without stopping: crash, kill, power
	Connected     = "connected"
	ConnectFailed = "connect_failed"
	Disconnected  = "disconnected"
	KeepaliveMiss = "keepalive_miss"
	LinkStats     = "link_stats" // periodic while connected: a baseline to compare with
	ToolFailed    = "tool_failed"
	ToolSlow      = "tool_slow"
	ChannelLost   = "channel_lost" // a server console's channel died
)

// Reasons of Disconnected.
const (
	ReasonKeepalive = "keepalive_timeout" // the network went silent
	ReasonLost      = "connection_lost"   // the connection was closed or reset
	ReasonClosed    = "closed"            // the hub or the machine stopped it
)

const (
	maxBytes   = 4 << 20
	maxMessage = 300
	maxRead    = 2000
)

// Event is one line of the log.
type Event struct {
	At         time.Time  `json:"at"`
	Kind       string     `json:"kind"`
	MachineID  string     `json:"machineId,omitempty"`
	Machine    string     `json:"machine,omitempty"`
	SessionID  string     `json:"sessionId,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	Error      string     `json:"error,omitempty"`
	Tool       string     `json:"tool,omitempty"`
	Attempt    int        `json:"attempt,omitempty"`
	Misses     int        `json:"misses,omitempty"`
	DurationMs int64      `json:"durationMs,omitempty"`
	UptimeMs   int64      `json:"uptimeMs,omitempty"`
	DowntimeMs int64      `json:"downtimeMs,omitempty"`
	RTTAvgMs   int64      `json:"rttAvgMs,omitempty"`
	RTTMaxMs   int64      `json:"rttMaxMs,omitempty"`
	Calls      int        `json:"calls,omitempty"`
	Failures   int        `json:"failures,omitempty"`
	Version    string     `json:"version,omitempty"`
	PID        int        `json:"pid,omitempty"`
	LastSeen   *time.Time `json:"lastSeen,omitempty"`
}

// Log appends events; a nil Log ignores them.
type Log struct {
	mu   sync.Mutex
	path string
	max  int64
	Now  func() time.Time
}

// Open prepares dir (0700).
func Open(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Log{path: filepath.Join(dir, "connections.jsonl"), max: maxBytes, Now: time.Now}, nil
}

func (l *Log) rotated() string { return l.path[:len(l.path)-len(".jsonl")] + ".1.jsonl" }

// Write appends an event. Logging must never get in the way, so errors only
// lose the event.
func (l *Log) Write(e Event) {
	if l == nil {
		return
	}
	if e.At.IsZero() {
		e.At = l.Now().UTC()
	}
	if r := []rune(e.Error); len(r) > maxMessage {
		e.Error = string(r[:maxMessage])
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if fi, err := os.Stat(l.path); err == nil && fi.Size()+int64(len(b))+1 > l.max {
		_ = os.Rename(l.path, l.rotated())
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// Query selects events. Machine matches an id or a name.
type Query struct {
	Machine string
	Since   time.Time
	Limit   int // default 200, at most 2000
}

// Read returns matching events, newest first. A line a crash cut short is skipped.
func (l *Log) Read(q Query) []Event {
	out := []Event{}
	if l == nil {
		return out
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, maxRead)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, path := range []string{l.rotated(), l.path} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			if q.Machine != "" && e.MachineID != q.Machine && e.Machine != q.Machine && e.MachineID != "" {
				continue
			}
			if !q.Since.IsZero() && e.At.Before(q.Since) {
				continue
			}
			out = append(out, e)
			// the files are in the order things happened, the newest last: keep only the
			// newest limit of them, not every event of both files before cutting
			if len(out) >= 2*limit {
				out = append([]Event(nil), out[len(out)-limit:]...)
			}
		}
		f.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Summary is what the events add up to.
type Summary struct {
	From          *time.Time     `json:"from,omitempty"`
	To            *time.Time     `json:"to,omitempty"`
	Connects      int            `json:"connects"`
	ConnectFailed int            `json:"connectFailed"`
	Disconnects   int            `json:"disconnects"`
	Reasons       map[string]int `json:"reasons"`
	AvgDowntimeMs int64          `json:"avgDowntimeMs"`
	MaxDowntimeMs int64          `json:"maxDowntimeMs"`
	AvgUptimeMs   int64          `json:"avgUptimeMs"`
	KeepaliveMiss int            `json:"keepaliveMiss"`
	RTTAvgMs      int64          `json:"rttAvgMs"`
	RTTMaxMs      int64          `json:"rttMaxMs"`
	ToolFailed    int            `json:"toolFailed"`
	ToolSlow      int            `json:"toolSlow"`
	ChannelsLost  int            `json:"channelsLost"`
	UncleanExits  int            `json:"uncleanExits"`
}

// Summarize adds events up.
func Summarize(events []Event) Summary {
	s := Summary{Reasons: map[string]int{}}
	var down, up, rtt, downN, upN, rttN int64
	for i := range events {
		e := &events[i]
		if s.To == nil || e.At.After(*s.To) {
			s.To = &e.At
		}
		if s.From == nil || e.At.Before(*s.From) {
			s.From = &e.At
		}
		switch e.Kind {
		case Connected:
			s.Connects++
			if e.DowntimeMs > 0 {
				down += e.DowntimeMs
				downN++
				s.MaxDowntimeMs = max(s.MaxDowntimeMs, e.DowntimeMs)
			}
		case ConnectFailed:
			s.ConnectFailed++
		case Disconnected:
			if e.Reason != ReasonClosed {
				s.Disconnects++
				s.Reasons[e.Reason]++
			}
			if e.UptimeMs > 0 {
				up += e.UptimeMs
				upN++
			}
		case KeepaliveMiss:
			s.KeepaliveMiss++
		case ToolFailed:
			s.ToolFailed++
		case ToolSlow:
			s.ToolSlow++
		case ChannelLost:
			s.ChannelsLost++
		case HubUnclean:
			s.UncleanExits++
		}
		if e.RTTAvgMs > 0 {
			rtt += e.RTTAvgMs
			rttN++
		}
		s.RTTMaxMs = max(s.RTTMaxMs, e.RTTMaxMs)
	}
	if downN > 0 {
		s.AvgDowntimeMs = down / downN
	}
	if upN > 0 {
		s.AvgUptimeMs = up / upN
	}
	if rttN > 0 {
		s.RTTAvgMs = rtt / rttN
	}
	return s
}

// Describe is one line for people reading the log in a terminal.
func Describe(e Event) string {
	d := func(ms int64) string {
		if ms < 1000 {
			return fmt.Sprintf("%d ms", ms)
		}
		return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
	}
	switch e.Kind {
	case HubStart:
		return fmt.Sprintf("hub running (version %s, pid %d)", e.Version, e.PID)
	case HubStop:
		return "hub stopped normally"
	case HubUnclean:
		if e.LastSeen != nil {
			return "the previous hub died suddenly, last alive " + e.LastSeen.Local().Format("15:04:05")
		}
		return "the previous hub died suddenly"
	case Connected:
		s := "connected in " + d(e.DurationMs)
		if e.DowntimeMs > 0 {
			s += fmt.Sprintf(", after %s disconnected", d(e.DowntimeMs))
		}
		return s
	case ConnectFailed:
		return fmt.Sprintf("could not connect (attempt %d): %s", e.Attempt, firstOf(e.Reason, e.Error))
	case Disconnected:
		s := fmt.Sprintf("disconnected: %s, after %s connected", e.Reason, d(e.UptimeMs))
		if e.Error != "" {
			s += " (" + e.Error + ")"
		}
		return s
	case KeepaliveMiss:
		return fmt.Sprintf("keepalive not answered (%d in a row)", e.Misses)
	case LinkStats:
		return fmt.Sprintf("connection healthy: average latency %d ms, max %d ms, %d calls, %d failed", e.RTTAvgMs, e.RTTMaxMs, e.Calls, e.Failures)
	case ToolFailed:
		return fmt.Sprintf("tool %s failed after %s: %s", e.Tool, d(e.DurationMs), e.Reason)
	case ToolSlow:
		return fmt.Sprintf("tool %s slow: %s", e.Tool, d(e.DurationMs))
	case ChannelLost:
		return "console channel lost: " + e.SessionID
	}
	return e.Kind
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
