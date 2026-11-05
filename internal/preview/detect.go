package preview

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// urlRe is the B18 pattern (docs/API-studio.md §4.6).
var urlRe = regexp.MustCompile(`https?://(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):(\d{2,5})(/[^\s"'<>\\]*)?`)

// Found is one detected localhost URL.
type Found struct {
	SessionID string
	MachineID string
	Port      int
	Scheme    string // http | https
	URL       string // as printed, host normalised to localhost
	Path      string
}

// Detector scans console output for localhost URLs (after markers and
// ANSI are stripped), once per (session, port) per agent run, and
// reports those whose port answers on the session's machine.
type Detector struct {
	// Enabled is settings.previews.auto.
	Enabled func() bool
	// Session returns the machine of a session and a key that changes
	// with every run of its process (e.g. its start time).
	Session func(sid string) (machineID, runKey string, ok bool)
	// Probe reports whether a loopback port of a machine is open.
	Probe func(ctx context.Context, machineID string, port int) bool
	// Allowed filters ports (hub and preview ports of This PC).
	Allowed func(machineID string, port int) bool
	// OnFound receives URLs whose port is open. Returning false (the
	// preview could not be opened) lets a later print of the URL retry.
	OnFound func(f Found) bool
	// Tries × Every is the port check (3 × 1 s).
	Tries int
	Every time.Duration

	mu    sync.Mutex
	tails map[string]string
	seen  map[string]bool
	wg    sync.WaitGroup
}

// tailKeep is how much unmatched text is carried to the next chunk (a URL
// split across chunks).
const tailKeep = 96

// Feed scans one output chunk of a session.
func (d *Detector) Feed(sid string, clean []byte) {
	if d.Enabled != nil && !d.Enabled() {
		return
	}
	if !strings.Contains(string(clean), ":") && !d.hasTail(sid) {
		return
	}
	d.mu.Lock()
	if d.tails == nil {
		d.tails, d.seen = map[string]string{}, map[string]bool{}
	}
	text := d.tails[sid] + ansi.Strip(string(clean))
	var hits []Found
	keepFrom := max(0, len(text)-tailKeep)
	for _, l := range urlRe.FindAllStringSubmatchIndex(text, -1) {
		if l[1] == len(text) {
			// May continue in the next chunk ("…:51" + "73/").
			keepFrom = l[0]
			break
		}
		keepFrom = max(keepFrom, l[1])
		port, err := strconv.Atoi(text[l[4]:l[5]])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		path := "/"
		if l[6] >= 0 {
			if path = strings.TrimRight(text[l[6]:l[7]], ".,;:!?)]}"); path == "" {
				path = "/"
			}
		}
		scheme := "http"
		if strings.HasPrefix(text[l[0]:], "https") {
			scheme = "https"
		}
		hits = append(hits, Found{SessionID: sid, Port: port, Path: path, Scheme: scheme, URL: scheme + "://localhost:" + strconv.Itoa(port) + path})
	}
	d.tails[sid] = text[keepFrom:]
	d.mu.Unlock()
	for _, h := range hits {
		d.check(h)
	}
}

func (d *Detector) hasTail(sid string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tails[sid] != ""
}

// Forget drops a removed session.
func (d *Detector) Forget(sid string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.tails, sid)
	for k := range d.seen {
		if strings.HasPrefix(k, sid+"|") {
			delete(d.seen, k)
		}
	}
}

// Wait blocks until running port checks finish (tests).
func (d *Detector) Wait() { d.wg.Wait() }

func (d *Detector) check(f Found) {
	if d.Session == nil {
		return
	}
	mid, run, ok := d.Session(f.SessionID)
	if !ok {
		return
	}
	f.MachineID = mid
	if d.Allowed != nil && !d.Allowed(mid, f.Port) {
		return
	}
	key := f.SessionID + "|" + strconv.Itoa(f.Port) + "|" + run
	d.mu.Lock()
	if d.seen[key] {
		d.mu.Unlock()
		return
	}
	d.seen[key] = true
	d.mu.Unlock()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		tries, every := d.Tries, d.Every
		if tries <= 0 {
			tries = 3
		}
		if every <= 0 {
			every = time.Second
		}
		for i := 0; i < tries; i++ {
			if i > 0 {
				time.Sleep(every)
			}
			ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
			open := d.Probe != nil && d.Probe(ctx, mid, f.Port)
			cancel()
			if open {
				if d.OnFound == nil || d.OnFound(f) {
					return
				}
				break
			}
		}
		// Closed (or not opened): a later print of the same URL may try again.
		d.mu.Lock()
		delete(d.seen, key)
		d.mu.Unlock()
	}()
}
