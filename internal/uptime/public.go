package uptime

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// A project's public status page (docs/API-uptime.md). It is reachable with no login, so what
// it shows is an allowlist: publicPage below is built from the name the owner chose for the
// public, up or down, and uptime and outage times rounded to the minute. It never holds a
// Monitor, and so it cannot show an address, a header, an error text or a response time.

const (
	maxPublicName  = 60
	maxPublicTitle = 80
	publicDays     = 30
	publicOutages  = 10
	// publicCacheFor keeps a page anyone can open from costing a computation per request.
	publicCacheFor = 15 * time.Second
)

// PublicSettings is a project's public page. The token is the address: 192 random bits.
type PublicSettings struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token,omitempty"`
	Title   string `json:"title,omitempty"`
}

var publicIPRe = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)

// checkPublic refuses a public name that gives away where the monitor looks. A name is the
// one free text on the page, and a monitor's own name is often its host.
func (m *Monitor) checkPublic() error {
	m.PublicName = strings.Join(strings.Fields(m.PublicName), " ")
	if !m.Public {
		return nil
	}
	if m.PublicName == "" {
		return errors.New("monitor publik butuh nama publik")
	}
	if len([]rune(m.PublicName)) > maxPublicName {
		return fmt.Errorf("nama publik maksimal %d karakter", maxPublicName)
	}
	low := strings.ToLower(m.PublicName)
	if strings.Contains(low, "://") || publicIPRe.MatchString(low) {
		return errors.New("nama publik tidak boleh berisi alamat")
	}
	hosts := []string{m.Host, m.DNSServer}
	if u, err := url.Parse(m.URL); err == nil {
		hosts = append(hosts, u.Hostname())
	}
	for _, h := range hosts {
		if h != "" && strings.Contains(low, strings.ToLower(h)) {
			return errors.New("nama publik tidak boleh berisi host yang dipantau")
		}
	}
	return nil
}

// SetPublic turns a project's page on or off and sets its title. The address stays the same
// across off and on; RotatePublic changes it.
func (s *Service) SetPublic(projectID string, enabled bool, title string) (PublicSettings, error) {
	title = strings.Join(strings.Fields(title), " ")
	if len([]rune(title)) > maxPublicTitle {
		return PublicSettings{}, fmt.Errorf("judul maksimal %d karakter", maxPublicTitle)
	}
	s.mu.Lock()
	c := s.settings[projectID]
	c.Public.Enabled, c.Public.Title = enabled, title
	if enabled && c.Public.Token == "" {
		token, err := newPublicToken()
		if err != nil {
			s.mu.Unlock()
			return PublicSettings{}, err
		}
		c.Public.Token = token
	}
	s.settings[projectID] = c
	s.mu.Unlock()
	s.saveSettings()
	s.dropPublicCache()
	return c.Public, nil
}

// RotatePublic gives the page a new address: the old one stops working at once.
func (s *Service) RotatePublic(projectID string) (PublicSettings, error) {
	token, err := newPublicToken()
	if err != nil {
		return PublicSettings{}, err
	}
	s.mu.Lock()
	c := s.settings[projectID]
	c.Public.Token = token
	s.settings[projectID] = c
	s.mu.Unlock()
	s.saveSettings()
	s.dropPublicCache()
	return c.Public, nil
}

func newPublicToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// publicOwner finds whose page a token opens. It looks through every project, comparing in
// constant time: an unknown token and a page turned off are the same to whoever asks.
func (s *Service) publicOwner(token string) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, title, found := "", "", false
	for id, c := range s.settings {
		if c.Public.Token != "" && subtle.ConstantTimeCompare([]byte(c.Public.Token), []byte(token)) == 1 && c.Public.Enabled {
			pid, title, found = id, c.Public.Title, true
		}
	}
	return pid, title, found
}

type publicCached struct {
	at   time.Time
	body []byte
}

type publicCacheT struct {
	mu    sync.Mutex
	pages map[string]publicCached
}

func (s *Service) dropPublicCache() {
	s.publicCache.mu.Lock()
	defer s.publicCache.mu.Unlock()
	s.publicCache.pages = nil
}

// PublicHTML is the page a token opens, false for anything else. Whether the token is good is
// asked every time, so turning a page off counts at once whatever is in the cache.
func (s *Service) PublicHTML(token string) ([]byte, bool) {
	if token == "" {
		return nil, false
	}
	pid, title, ok := s.publicOwner(token)
	if !ok {
		return nil, false
	}
	now := s.Now()
	s.publicCache.mu.Lock()
	defer s.publicCache.mu.Unlock()
	if c, ok := s.publicCache.pages[token]; ok && now.Sub(c.at) < publicCacheFor {
		return c.body, true
	}
	var b bytes.Buffer
	if err := publicTemplate.Execute(&b, s.publicPage(pid, title, now)); err != nil {
		return nil, false
	}
	if s.publicCache.pages == nil {
		s.publicCache.pages = map[string]publicCached{}
	}
	s.publicCache.pages[token] = publicCached{at: now, body: b.Bytes()}
	return b.Bytes(), true
}

// publicPage, publicMonitor, publicDay and publicOutage are all there is on the page.
type publicPage struct {
	Title    string
	Overall  string
	State    string // up | down | none
	Updated  string
	Monitors []publicMonitor
	Outages  []publicOutage
}

type publicMonitor struct {
	Name   string
	State  string // up | down | none
	Label  string
	Uptime string
	Days   []publicDay
}

type publicDay struct {
	State string // up | partial | down | none
	Title string
}

type publicOutage struct {
	Name  string
	Start string
	Long  string
}

// projectEntries are a project's monitors, oldest first, so the page does not reshuffle.
func (s *Service) projectEntries(pid string) []*entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*entry
	for _, e := range s.entries {
		if e.m.ProjectID == pid {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].m.CreatedAt.Equal(out[j].m.CreatedAt) {
			return out[i].m.CreatedAt.Before(out[j].m.CreatedAt)
		}
		return out[i].m.ID < out[j].m.ID
	})
	return out
}

func (s *Service) publicPage(pid, title string, now time.Time) publicPage {
	if title == "" {
		title = "Status"
	}
	page := publicPage{Title: title, Updated: now.UTC().Format("2006-01-02 15:04") + " UTC"}
	var outages []struct {
		t int64
		o publicOutage
	}
	for _, e := range s.projectEntries(pid) {
		e.mu.Lock()
		if !e.m.Public {
			e.mu.Unlock()
			continue
		}
		name := e.m.PublicName
		state := "none"
		switch {
		case e.status == StatusDown:
			state = "down"
		case e.status == StatusUp, e.status == StatusPending && e.settled == statusUp:
			state = "up" // a blip within the retries is not an outage
		}
		mon := publicMonitor{Name: name, State: state, Label: map[string]string{"up": "Operational", "down": "Down", "none": "No data"}[state], Uptime: "–"}
		if u := e.hist.over(publicDays*24*time.Hour, now).Uptime; u != nil {
			mon.Uptime = fmt.Sprintf("%.2f%%", math.Floor(*u*100)/100)
		}
		mon.Days = publicDaysOf(e.hist.series("1y", now), now)
		for i := len(e.hist.events) - 1; i >= 0; i-- {
			ev := e.hist.events[i]
			at := time.UnixMilli(ev.T).UTC()
			if now.Sub(at) > publicDays*24*time.Hour {
				break
			}
			switch {
			case ev.Status == statusName(statusDown) && i == len(e.hist.events)-1:
				outages = append(outages, struct {
					t int64
					o publicOutage
				}{ev.T, publicOutage{Name: name, Start: at.Format("2006-01-02 15:04") + " UTC", Long: "ongoing"}})
			case ev.Status == statusName(statusUp) && ev.DurSec > 0:
				start := at.Add(-time.Duration(ev.DurSec) * time.Second)
				outages = append(outages, struct {
					t int64
					o publicOutage
				}{start.UnixMilli(), publicOutage{Name: name, Start: start.Format("2006-01-02 15:04") + " UTC", Long: publicLong(ev.DurSec)}})
			}
		}
		e.mu.Unlock()
		page.Monitors = append(page.Monitors, mon)
	}
	sort.Slice(outages, func(i, j int) bool { return outages[i].t > outages[j].t })
	for i, o := range outages {
		if i == publicOutages {
			break
		}
		page.Outages = append(page.Outages, o.o)
	}
	page.State, page.Overall = "none", "No services to show"
	if len(page.Monitors) > 0 {
		counts := map[string]int{}
		for _, m := range page.Monitors {
			counts[m.State]++
		}
		switch {
		case counts["down"] > 0:
			page.State, page.Overall = "down", "Some services are down"
		case counts["up"] == 0:
			page.State, page.Overall = "none", "No data yet"
		case counts["none"] > 0: // "all operational" would claim what nobody has checked
			page.State, page.Overall = "none", "No known problems, some services have no data"
		default:
			page.State, page.Overall = "up", "All services operational"
		}
	}
	return page
}

func publicLong(sec int64) string {
	min := int64(math.Ceil(float64(sec) / 60))
	if min < 60 {
		return fmt.Sprintf("%d min", min)
	}
	return fmt.Sprintf("%d h %d min", min/60, min%60)
}

// publicDaysOf is one cell per day, oldest first, ending today.
func publicDaysOf(series Series, now time.Time) []publicDay {
	byDay := map[string]Point{}
	for _, p := range series.Points {
		byDay[time.UnixMilli(p.T).UTC().Format("2006-01-02")] = p
	}
	out := make([]publicDay, 0, publicDays)
	for i := publicDays - 1; i >= 0; i-- {
		day := now.UTC().AddDate(0, 0, -i).Format("2006-01-02")
		d := publicDay{State: "none", Title: day + ": no data"}
		if p, ok := byDay[day]; ok && p.Up+p.Down > 0 {
			u := float64(p.Up) * 100 / float64(p.Up+p.Down)
			d.State, d.Title = "up", fmt.Sprintf("%s: %.2f%%", day, math.Floor(u*100)/100)
			switch {
			case p.Up == 0:
				d.State = "down"
			case p.Down > 0:
				d.State = "partial"
			}
		}
		out = append(out, d)
	}
	return out
}

// No script, nothing loaded from anywhere, and no sign of what software this is.
var publicTemplate = template.Must(template.New("status").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta http-equiv="refresh" content="60">
<title>{{.Title}}</title>
<style>
:root{color-scheme:light dark;--bg:#fafafa;--card:#fff;--text:#0d0d0d;--muted:#5b6478;--line:rgba(0,0,0,.1);--up:#0d0d0d;--none:rgba(0,0,0,.12);--down:#c62828;--partial:#b26a00}
@media (prefers-color-scheme:dark){:root{--bg:#0a0a0a;--card:#141414;--text:#f5f5f5;--muted:#9aa3b5;--line:rgba(255,255,255,.12);--up:#f5f5f5;--none:rgba(255,255,255,.14);--down:#ef5350;--partial:#ffb74d}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:720px;margin:0 auto;padding:40px 16px 64px}
h1{margin:0;font-size:24px;line-height:1.25;overflow-wrap:anywhere}
h2{margin:32px 0 12px;font-size:13px;font-weight:600;letter-spacing:.04em;text-transform:uppercase;color:var(--muted)}
.overall{display:flex;align-items:center;gap:10px;margin:16px 0 0;padding:14px 16px;background:var(--card);border:1px solid var(--line);border-radius:12px;font-weight:600}
.dot{flex:none;width:10px;height:10px;border-radius:50%;background:var(--none)}
.up .dot{background:var(--up)}
.down .dot{background:var(--down)}
.card{margin-top:10px;padding:14px 16px;background:var(--card);border:1px solid var(--line);border-radius:12px}
.row{display:flex;align-items:center;gap:10px}
.name{flex:1;min-width:0;font-weight:600;overflow-wrap:anywhere}
.label{color:var(--muted);font-size:13px;white-space:nowrap}
.down .label{color:var(--down);font-weight:600}
.days{display:flex;gap:2px;margin-top:12px}
.days i{flex:1;height:26px;border-radius:2px;background:var(--none)}
.days .up{background:var(--up)}
.days .partial{background:var(--partial)}
.days .down{background:var(--down)}
.foot{display:flex;justify-content:space-between;gap:12px;margin-top:6px;color:var(--muted);font-size:12px}
ul{margin:0;padding:0;list-style:none}
li{display:flex;flex-wrap:wrap;justify-content:space-between;gap:4px 12px;padding:10px 0;border-top:1px solid var(--line);font-size:14px}
li:first-child{border-top:0}
li span:last-child{color:var(--muted)}
.updated{margin-top:32px;color:var(--muted);font-size:12px}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p class="overall {{.State}}"><span class="dot"></span>{{.Overall}}</p>
{{if .Monitors}}<h2>Services</h2>
{{range .Monitors}}<section class="card {{.State}}">
<div class="row"><span class="dot"></span><span class="name">{{.Name}}</span><span class="label">{{.Label}}</span></div>
<div class="days" role="img" aria-label="The last 30 days">{{range .Days}}<i class="{{.State}}" title="{{.Title}}"></i>{{end}}</div>
<div class="foot"><span>30 days ago</span><span>{{.Uptime}} uptime</span><span>Today</span></div>
</section>
{{end}}{{end}}{{if .Outages}}<h2>Outages in the last 30 days</h2>
<section class="card"><ul>{{range .Outages}}<li><span>{{.Name}}</span><span>{{.Start}} · {{.Long}}</span></li>{{end}}</ul></section>
{{end}}<p class="updated">Updated {{.Updated}}</p>
</main>
</body>
</html>
`))
