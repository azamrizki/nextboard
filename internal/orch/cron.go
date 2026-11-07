package orch

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed 5-field expression (minute hour day-of-month month
// day-of-week), evaluated in the hub's time zone (§11, open issue 9).
type Cron struct {
	min, hour, dom, month, dow [64]bool
	domStar, dowStar           bool
}

var cronNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8,
	"sep": 9, "oct": 10, "nov": 11, "dec": 12, "sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseCron parses "*/15 9-18 * * 1-5" style expressions.
func ParseCron(expr string) (*Cron, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron needs 5 fields")
	}
	c := &Cron{}
	specs := []struct {
		set    *[64]bool
		lo, hi int
	}{{&c.min, 0, 59}, {&c.hour, 0, 23}, {&c.dom, 1, 31}, {&c.month, 1, 12}, {&c.dow, 0, 7}}
	for i, s := range specs {
		if err := cronField(f[i], s.lo, s.hi, s.set); err != nil {
			return nil, fmt.Errorf("cron field %d: %w", i+1, err)
		}
	}
	if c.dow[7] {
		c.dow[0] = true
	}
	c.domStar, c.dowStar = f[2] == "*", f[4] == "*"
	return c, nil
}

func cronValue(s string, lo, hi int) (int, error) {
	if n, ok := cronNames[strings.ToLower(s)]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("value %q is outside %d–%d", s, lo, hi)
	}
	return n, nil
}

func cronField(s string, lo, hi int, set *[64]bool) error {
	for _, part := range strings.Split(s, ",") {
		step := 1
		if a, b, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(b)
			if err != nil || n < 1 || n > hi {
				return fmt.Errorf("invalid step %q", b)
			}
			part, step = a, n
		}
		from, to := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			var err error
			if from, err = cronValue(a, lo, hi); err != nil {
				return err
			}
			if to, err = cronValue(b, lo, hi); err != nil {
				return err
			}
			if from > to {
				return fmt.Errorf("range %q is reversed", part)
			}
		default:
			v, err := cronValue(part, lo, hi)
			if err != nil {
				return err
			}
			from = v
			if step == 1 {
				to = v
			}
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return nil
}

func (c *Cron) dayMatch(t time.Time) bool {
	d, w := c.dom[t.Day()], c.dow[int(t.Weekday())]
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return w
	case c.dowStar:
		return d
	}
	return d || w // both restricted: either (classic cron)
}

// Next returns the first matching minute strictly after t (zero time if
// none within ~4 years).
func (c *Cron) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(4, 0, 0)
	for t.Before(limit) {
		switch {
		case !c.month[int(t.Month())]:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
		case !c.dayMatch(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
		case !c.hour[t.Hour()]:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
		case !c.min[t.Minute()]:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// RunsPerHour estimates how often the expression fires (over 7 days).
func (c *Cron) RunsPerHour(from time.Time) float64 {
	n := 0
	end := from.Add(7 * 24 * time.Hour)
	for t := c.Next(from); !t.IsZero() && t.Before(end) && n < 20000; t = c.Next(t) {
		n++
	}
	return float64(n) / (7 * 24)
}
