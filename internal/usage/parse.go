package usage

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Window is one usage bar of a plan (docs/API-studio.md §10.1).
type Window struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	UsedPct    int        `json:"usedPct"`
	ResetsAt   *time.Time `json:"resetsAt"`
	ResetsText string     `json:"resetsText"`
}

// Fact is a label/value line of the stats screen.
type Fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Parsed is what a screen parser extracts.
type Parsed struct {
	Windows []Window
	Facts   []Fact
	// Unsupported: the CLI says usage is not available for this login
	// (e.g. an API key instead of a plan).
	Unsupported string
	// Pending: no bars yet because the CLI is still waiting for its limits;
	// the same command will show them.
	Pending bool
}

// The screen formats of /usage (Claude), /status (Codex) and /stats
// (Gemini) change between versions, so parsing is best-effort and keyed on
// short fragments: a percentage with "used" or "left", a "Resets …" text
// and the label line above a bar. Unknown screens yield no windows
// (Report.parsed:false with the raw lines).

var (
	pctRe     = regexp.MustCompile(`(?i)(\d{1,3}(?:\.\d+)?)\s*%\s*(used|left|remaining)?`)
	resetsRe  = regexp.MustCompile(`(?i)\b(resets?|renews?)\b\s*(?:at|on|in)?\s*[:]?\s*([^()│|]*(?:\([^)]*\))?)`)
	barRe     = regexp.MustCompile(`[█▉▊▋▌▍▎▏░▒▓■□━─]{3,}|\[[#=\-█░ ]{4,}\]`)
	sideRe    = regexp.MustCompile(`^[\s│|╭╮╰╯─]+|[\s│|╭╮╰╯─]+$`)
	notPlanRe = regexp.MustCompile(`(?i)only available for (subscription|claude|pro|max)|not available (for|with) (api|your)|usage limits? (are|is) not available|/usage is only|not available for this account`)
	labelRe   = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9 ()/.,'+-]{1,60}?)\s*:\s*(.*)$`)
	// Codex 0.165 /status before its rate limits arrived ("Limits: …").
	limitsPendingRe = regexp.MustCompile(`(?i)refresh requested; run /status again|limits:\s*data not available yet`)
)

// clean strips box borders and NO-BREAK SPACEs.
func clean(l string) string {
	l = strings.ReplaceAll(l, " ", " ")
	return strings.TrimSpace(sideRe.ReplaceAllString(l, ""))
}

// windowID turns a label into a stable id: "Current session" → "session",
// "Current week (all models)" → "week", "Current week (Sonnet only)" →
// "week-sonnet", "5h limit" → "5h", "Weekly limit" → "weekly".
func windowID(label string) string {
	l := strings.ToLower(label)
	l = strings.TrimPrefix(l, "current ")
	l = strings.ReplaceAll(l, "limit", "")
	l = strings.ReplaceAll(l, "(all models)", "")
	l = strings.ReplaceAll(l, " only", "")
	var b strings.Builder
	dash := false
	for _, r := range l {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// ParseScreen parses the usage screen of agent; now and loc resolve reset
// times ("Resets 6pm") to the next matching instant.
func ParseScreen(agentID string, lines []string, now time.Time, loc *time.Location) Parsed {
	var p Parsed
	cl := make([]string, len(lines))
	for i, l := range lines {
		cl[i] = clean(l)
	}
	all := strings.Join(cl, "\n")
	if m := notPlanRe.FindString(all); m != "" {
		p.Unsupported = m
		p.Facts = facts(agentID, cl)
		return p
	}
	if loc == nil {
		loc = time.Local
	}
	seen := map[string]bool{}
	for i, l := range cl {
		m := pctRe.FindStringSubmatch(l)
		if m == nil || !looksLikeBar(l, m) {
			continue
		}
		f, _ := strconv.ParseFloat(m[1], 64)
		used := int(f + 0.5)
		if k := strings.ToLower(m[2]); k == "left" || k == "remaining" {
			used = 100 - used
		}
		if used < 0 || used > 100 {
			continue
		}
		label, rest := barLabel(cl, i)
		if label == "" {
			continue
		}
		w := Window{ID: windowID(label), Label: label, UsedPct: used}
		// Reset text: on the bar line itself (Codex "(resets 18:00)") or
		// on one of the next two lines (Claude "Resets 6pm (…)").
		for _, cand := range []string{rest, l, at(cl, i+1), at(cl, i+2)} {
			if r := resetsRe.FindStringSubmatch(cand); r != nil && strings.TrimSpace(r[2]) != "" {
				w.ResetsText = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(r[0]), ")"))
				if strings.Count(w.ResetsText, "(") > strings.Count(w.ResetsText, ")") {
					w.ResetsText += ")"
				}
				w.ResetsText = upperFirst(w.ResetsText)
				w.ResetsAt = parseReset(strings.TrimSpace(r[2]), now, loc)
				break
			}
			if cand != rest && cand != l && pctRe.MatchString(cand) {
				break // the next bar: no reset text for this one
			}
		}
		if w.ID == "" || seen[w.ID] {
			continue
		}
		seen[w.ID] = true
		p.Windows = append(p.Windows, w)
	}
	p.Facts = facts(agentID, cl)
	p.Pending = len(p.Windows) == 0 && limitsPendingRe.MatchString(all)
	return p
}

func at(l []string, i int) string {
	if i < 0 || i >= len(l) {
		return ""
	}
	return l[i]
}

// looksLikeBar accepts a percentage line that has a bar, the word
// used/left, or a "limit"/"session"/"week" label.
func looksLikeBar(l string, m []string) bool {
	if m[2] != "" || barRe.MatchString(l) {
		return true
	}
	ll := strings.ToLower(l)
	return strings.Contains(ll, "limit") || strings.Contains(ll, "session") || strings.Contains(ll, "week")
}

// barLabel finds the label of the bar on line i: text before ":" on the
// same line (Codex "5h limit: [███] 42% used"), else the nearest label
// line above (Claude "Current session" over the bar).
func barLabel(cl []string, i int) (label, rest string) {
	l := cl[i]
	if j := strings.Index(l, ":"); j > 0 {
		head := strings.TrimSpace(l[:j])
		if !pctRe.MatchString(head) && !barRe.MatchString(head) && len(head) <= 40 {
			return head, l[j+1:]
		}
	}
	// text before the bar on the same line
	if loc := barRe.FindStringIndex(l); loc != nil && loc[0] > 0 {
		head := strings.TrimSpace(l[:loc[0]])
		if head != "" && !pctRe.MatchString(head) && len(head) <= 40 {
			return head, ""
		}
	}
	for k := i - 1; k >= 0 && k >= i-2; k-- {
		c := cl[k]
		if c == "" {
			continue
		}
		if pctRe.MatchString(c) || resetsRe.MatchString(c) || barRe.MatchString(c) {
			return "", ""
		}
		if len(c) <= 48 {
			return c, ""
		}
		return "", ""
	}
	return "", ""
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// factKeys are the stats lines shown as facts, per CLI (lower case).
var factKeys = map[string][]string{
	"claude": {"favorite model", "favourite model", "total tokens", "sessions", "current streak", "longest streak", "active days", "longest session"},
	"codex":  {"model", "account", "lifetime tokens", "total tokens", "token usage", "peak", "streak", "longest task", "plan", "warning"},
	"gemini": {"session id", "tool calls", "success rate", "wall time", "agent active"},
}

func facts(agentID string, cl []string) []Fact {
	keys := factKeys[agentID]
	var out []Fact
	seen := map[string]bool{}
	for _, l := range cl {
		m := labelRe.FindStringSubmatch(l)
		if m == nil || strings.TrimSpace(m[2]) == "" || pctRe.MatchString(m[2]) && barRe.MatchString(m[2]) {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(m[1]))
		for _, want := range keys {
			if k == want && !seen[k] {
				seen[k] = true
				out = append(out, Fact{Label: strings.TrimSpace(m[1]), Value: strings.TrimSpace(m[2])})
			}
		}
	}
	return out
}

var (
	zoneRe    = regexp.MustCompile(`\(([A-Za-z_]+(?:/[A-Za-z_+-]+)+|UTC)\)`)
	clockRe   = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b|\b(\d{1,2}):(\d{2})\b`)
	monthDay  = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2})\b`)
	dayMonth  = regexp.MustCompile(`(?i)\b(\d{1,2})\s+(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\b`)
	inDurRe   = regexp.MustCompile(`(?i)^in\s+((?:\d+\s*[dhm]\w*\s*)+)$|^((?:\d+\s*(?:d|h|hr|hrs|hours?|m|min|mins|minutes?)\s*)+)$`)
	durPartRe = regexp.MustCompile(`(?i)(\d+)\s*(d|h|m)`)
)

var months = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

// parseReset resolves a reset text best-effort: "6pm (Asia/Jakarta)",
// "Nov 3, 10am (Asia/Jakarta)", "18:00", "09:00 on 3 Nov", "in 2h 5m".
// nil when the format is unknown.
func parseReset(s string, now time.Time, loc *time.Location) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if m := zoneRe.FindStringSubmatch(s); m != nil {
		if l, err := time.LoadLocation(m[1]); err == nil {
			loc = l
		}
		s = strings.TrimSpace(zoneRe.ReplaceAllString(s, ""))
	}
	if m := inDurRe.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		var d time.Duration
		for _, p := range durPartRe.FindAllStringSubmatch(s, -1) {
			n, _ := strconv.Atoi(p[1])
			switch strings.ToLower(p[2]) {
			case "d":
				d += time.Duration(n) * 24 * time.Hour
			case "h":
				d += time.Duration(n) * time.Hour
			case "m":
				d += time.Duration(n) * time.Minute
			}
		}
		if d > 0 {
			t := now.Add(d).Truncate(time.Minute)
			return &t
		}
	}
	hour, minute, haveClock := 0, 0, false
	if m := clockRe.FindStringSubmatch(s); m != nil {
		if m[3] != "" {
			hour, _ = strconv.Atoi(m[1])
			if m[2] != "" {
				minute, _ = strconv.Atoi(m[2])
			}
			ap := strings.ToLower(m[3])
			if hour == 12 {
				hour = 0
			}
			if ap == "pm" {
				hour += 12
			}
		} else {
			hour, _ = strconv.Atoi(m[4])
			minute, _ = strconv.Atoi(m[5])
		}
		haveClock = hour < 24 && minute < 60
	}
	var month time.Month
	day := 0
	if m := monthDay.FindStringSubmatch(s); m != nil {
		month = months[strings.ToLower(m[1][:3])]
		day, _ = strconv.Atoi(m[2])
	} else if m := dayMonth.FindStringSubmatch(s); m != nil {
		month = months[strings.ToLower(m[2][:3])]
		day, _ = strconv.Atoi(m[1])
	}
	ln := now.In(loc)
	switch {
	case month != 0 && day >= 1 && day <= 31:
		t := time.Date(ln.Year(), month, day, hour, minute, 0, 0, loc)
		if t.Before(ln.Add(-24 * time.Hour)) {
			t = t.AddDate(1, 0, 0) // "Jan 2" seen in late December
		}
		u := t.UTC()
		return &u
	case haveClock:
		t := time.Date(ln.Year(), ln.Month(), ln.Day(), hour, minute, 0, 0, loc)
		if !t.After(ln) {
			t = t.AddDate(0, 0, 1)
		}
		u := t.UTC()
		return &u
	}
	return nil
}
