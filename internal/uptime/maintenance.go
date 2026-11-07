package uptime

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// StatusMaintenance: a maintenance window of the project covers the monitor. It is not
// checked, nothing is counted, nobody is alerted.
const StatusMaintenance = "maintenance"

const (
	MaintManual = "manual" // on until turned off
	MaintOnce   = "once"   // from start to end
	MaintWeekly = "weekly" // on these days, from this time, for this long

	maxWindows    = 20
	maxWindowMins = 7 * 24 * 60
	// maintRecheck: how soon a monitor in maintenance looks again, so that the end of
	// the window is noticed whatever the monitor's interval.
	maintRecheck = time.Minute
)

// Window is a maintenance window of a project.
type Window struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// MonitorIDs are the monitors it covers; none means all of the project.
	MonitorIDs []string  `json:"monitorIds,omitempty"`
	Mode       string    `json:"mode"`
	Active     bool      `json:"active,omitempty"`  // manual
	Start      time.Time `json:"start,omitzero"`    // once
	End        time.Time `json:"end,omitzero"`      // once
	Days       []int     `json:"days,omitempty"`    // weekly: 0 Sunday to 6 Saturday
	From       string    `json:"from,omitempty"`    // weekly: "HH:MM"
	Minutes    int       `json:"minutes,omitempty"` // weekly
	TZ         string    `json:"tz,omitempty"`      // weekly: an IANA zone; "" is the hub's
}

func newWindowID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "w_" + hex.EncodeToString(b)
}

// normalizeWindows checks a project's windows and gives each one an id. ids are the
// project's monitors: a window cannot name another project's.
func normalizeWindows(in []Window, ids map[string]bool) ([]Window, error) {
	if len(in) > maxWindows {
		return nil, fmt.Errorf("at most %d maintenance windows", maxWindows)
	}
	out := make([]Window, 0, len(in))
	seen := map[string]bool{}
	for _, w := range in {
		w.Title = strings.TrimSpace(w.Title)
		if w.Title == "" {
			return nil, errors.New("a maintenance window needs a title")
		}
		if len([]rune(w.Title)) > maxNameLen {
			return nil, fmt.Errorf("a window's title is at most %d characters", maxNameLen)
		}
		if w.ID == "" || seen[w.ID] {
			w.ID = newWindowID()
		}
		seen[w.ID] = true
		var mons []string
		for _, id := range w.MonitorIDs {
			if !ids[id] {
				return nil, fmt.Errorf("%s: no such monitor in this project", w.Title)
			}
			if !slices.Contains(mons, id) {
				mons = append(mons, id)
			}
		}
		w.MonitorIDs = mons
		o := w
		w.Active, w.Start, w.End, w.Days, w.From, w.Minutes, w.TZ = false, time.Time{}, time.Time{}, nil, "", 0, ""
		switch o.Mode {
		case MaintManual:
			w.Active = o.Active
		case MaintOnce:
			if o.Start.IsZero() || o.End.IsZero() || !o.End.After(o.Start) {
				return nil, fmt.Errorf("%s: the end must come after the start", w.Title)
			}
			w.Start, w.End = o.Start.UTC(), o.End.UTC()
		case MaintWeekly:
			for _, d := range o.Days {
				if d < 0 || d > 6 {
					return nil, fmt.Errorf("%s: days are 0 (Sunday) to 6", w.Title)
				}
				if !slices.Contains(w.Days, d) {
					w.Days = append(w.Days, d)
				}
			}
			slices.Sort(w.Days)
			if len(w.Days) == 0 {
				return nil, fmt.Errorf("%s: pick at least one day", w.Title)
			}
			if _, _, ok := hhmm(o.From); !ok {
				return nil, fmt.Errorf("%s: the time is HH:MM", w.Title)
			}
			w.From = o.From
			if o.Minutes < 1 || o.Minutes > maxWindowMins {
				return nil, fmt.Errorf("%s: a window lasts 1 minute to 7 days", w.Title)
			}
			w.Minutes = o.Minutes
			if o.TZ != "" {
				if _, err := time.LoadLocation(o.TZ); err != nil {
					return nil, fmt.Errorf("%s: unknown time zone %q", w.Title, o.TZ)
				}
			}
			w.TZ = o.TZ
		default:
			return nil, errors.New("a window's mode is manual, once or weekly")
		}
		out = append(out, w)
	}
	return out, nil
}

func hhmm(s string) (int, int, bool) {
	var h, m int
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(s, "%02d:%02d", &h, &m); err != nil || h > 23 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// covers says whether the window applies to the monitor.
func (w *Window) covers(monitorID string) bool {
	return len(w.MonitorIDs) == 0 || slices.Contains(w.MonitorIDs, monitorID)
}

// activeAt says whether the window is on at now.
func (w *Window) activeAt(now time.Time) bool {
	switch w.Mode {
	case MaintManual:
		return w.Active
	case MaintOnce:
		return !now.Before(w.Start) && now.Before(w.End)
	case MaintWeekly:
		loc := time.Local
		if w.TZ != "" {
			if l, err := time.LoadLocation(w.TZ); err == nil {
				loc = l
			}
		}
		h, m, ok := hhmm(w.From)
		if !ok {
			return false
		}
		t := now.In(loc)
		length := time.Duration(w.Minutes) * time.Minute
		// a window of up to 7 days may have begun on any of the last 7 days
		for k := 0; k <= 7; k++ {
			day := t.AddDate(0, 0, -k)
			if !slices.Contains(w.Days, int(day.Weekday())) {
				continue
			}
			start := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc)
			if !t.Before(start) && t.Before(start.Add(length)) {
				return true
			}
		}
	}
	return false
}

// maintenanceFor is the first window that is on for the monitor now, if any.
func maintenanceFor(windows []Window, monitorID string, now time.Time) *Window {
	for i := range windows {
		if windows[i].covers(monitorID) && windows[i].activeAt(now) {
			return &windows[i]
		}
	}
	return nil
}
