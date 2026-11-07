package uptime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/store"
)

func local() machine.Host { return machine.NewLocal(machine.Config{}) }

// away is a machine out of reach.
type away struct{ machine.Host }

func (away) Connected() bool    { return false }
func (away) Info() machine.Info { return machine.Info{Name: "srv"} }
func (away) ID() string         { return "m_srv" }

func httpMonitor(u string) Monitor {
	return Monitor{ProjectID: "p1", Name: "site", Type: TypeHTTP, URL: u, Active: true, Notify: true, Retries: 1}
}

func runCheck(t *testing.T, m Monitor) Result {
	t.Helper()
	if err := m.normalize(); err != nil {
		t.Fatal(err)
	}
	return check(context.Background(), m, local())
}

func TestNormalize(t *testing.T) {
	bad := []Monitor{
		{Type: TypeHTTP, URL: "https://a.b"},
		{Name: "x", Type: "gopher"},
		{Name: "x", Type: TypeHTTP, URL: "ftp://a.b"},
		{Name: "x", Type: TypeHTTP, URL: "https://"},
		{Name: "x", Type: TypeHTTP, URL: "https://a.b", Method: "DELETE"},
		{Name: "x", Type: TypeHTTP, URL: "https://a.b", Accepted: "abc"},
		{Name: "x", Type: TypeHTTP, URL: "https://a.b", Accepted: "299-200"},
		{Name: "x", Type: TypeHTTP, URL: "https://a.b", Method: "HEAD", Keyword: "ok"},
		{Name: "x", Type: TypeHTTP, URL: "https://a.b", Headers: []Header{{Name: "A\nB", Value: "c"}}},
		{Name: "x", Type: TypeHTTP, URL: "https://a.b", Headers: []Header{{Name: "A", Value: "c\r\nInjected: 1"}}},
		{Name: "x", Type: TypeTCP, Host: "a.b"},
		{Name: "x", Type: TypeTCP, Host: "a.b", Port: 70000},
		{Name: "x", Type: TypePing, Host: "-oProxyCommand=x"},
		{Name: "x", Type: TypePing, Host: "a b"},
		{Name: "x", Type: TypePing, Host: "a;reboot"},
		{Name: "x", Type: TypeDNS, Host: "a.b", DNSType: "SOA"},
		{Name: "x", Type: TypePing, Host: "a.b", IntervalSec: 5},
		{Name: "x", Type: TypePing, Host: "a.b", TimeoutSec: 90},
		{Name: "x", Type: TypePing, Host: "a.b", Retries: 11},
		{Name: strings.Repeat("x", 81), Type: TypePing, Host: "a.b"},
	}
	for i, m := range bad {
		if err := m.normalize(); err == nil {
			t.Errorf("#%d %+v harus ditolak", i, m)
		}
	}
	m := Monitor{Name: " x ", Type: TypeHTTP, URL: "https://a.b", Host: "stale", Port: 9, DNSType: "A", MachineID: "local"}
	if err := m.normalize(); err != nil {
		t.Fatal(err)
	}
	if m.Name != "x" || m.Method != "GET" || m.Accepted != "200-299" || m.IntervalSec != 60 || m.RetryIntervalSec != 60 || m.TimeoutSec != 30 {
		t.Fatalf("default salah: %+v", m)
	}
	if m.Host != "" || m.Port != 0 || m.DNSType != "" || m.MachineID != "" {
		t.Fatalf("sisa tipe lain tidak dibersihkan: %+v", m)
	}
	for _, h := range []string{"example.com", "10.0.0.1", "::1", "[::1]", "db-1.internal"} {
		p := Monitor{Name: "x", Type: TypePing, Host: h}
		if err := p.normalize(); err != nil {
			t.Errorf("host %q ditolak: %v", h, err)
		}
	}
}

func TestTargetHidesCredentials(t *testing.T) {
	m := Monitor{Type: TypeHTTP, URL: "https://user:secret@a.b/x"}
	if strings.Contains(m.Target(), "secret") {
		t.Fatalf("password bocor: %s", m.Target())
	}
}

func TestCheckHTTP(t *testing.T) {
	var gotHeader atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader.Store(r.Header.Get("X-Token"))
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, "hello world")
		case "/missing":
			w.WriteHeader(404)
		case "/moved":
			http.Redirect(w, r, "/ok", 302)
		case "/loop":
			http.Redirect(w, r, "/loop", 302)
		case "/slow":
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
		}
	}))
	defer srv.Close()
	for _, c := range []struct {
		name string
		m    Monitor
		ok   bool
		msg  string
	}{
		{"ok", Monitor{URL: srv.URL + "/ok"}, true, "200 OK"},
		{"404 is down", Monitor{URL: srv.URL + "/missing"}, false, "404 Not Found"},
		{"404 accepted", Monitor{URL: srv.URL + "/missing", Accepted: "200-299,404"}, true, "404 Not Found"},
		{"redirect followed", Monitor{URL: srv.URL + "/moved"}, true, "200 OK"},
		{"redirect loop", Monitor{URL: srv.URL + "/loop"}, false, "redirect"},
		{"keyword found", Monitor{URL: srv.URL + "/ok", Keyword: "world"}, true, "200 OK"},
		{"keyword missing", Monitor{URL: srv.URL + "/ok", Keyword: "nope"}, false, `"nope" not found`},
		{"keyword invert", Monitor{URL: srv.URL + "/ok", Keyword: "error", KeywordInvert: true}, true, "200 OK"},
		{"keyword invert hit", Monitor{URL: srv.URL + "/ok", Keyword: "hello", KeywordInvert: true}, false, `"hello" found`},
		{"timeout", Monitor{URL: srv.URL + "/slow", TimeoutSec: 1}, false, "Timeout"},
		{"HEAD", Monitor{URL: srv.URL + "/ok", Method: "HEAD"}, true, "200 OK"},
	} {
		c.m.Name, c.m.Type = "x", TypeHTTP
		r := runCheck(t, c.m)
		if r.OK != c.ok || !strings.Contains(r.Msg, c.msg) {
			t.Errorf("%s: ok=%v msg=%q", c.name, r.OK, r.Msg)
		}
		if r.OK && r.Ms < 0 {
			t.Errorf("%s: waktu tidak diukur", c.name)
		}
	}
	runCheck(t, Monitor{Name: "x", Type: TypeHTTP, URL: srv.URL + "/ok", Headers: []Header{{Name: "X-Token", Value: "abc"}}})
	if gotHeader.Load() != "abc" {
		t.Fatalf("header tidak terkirim: %v", gotHeader.Load())
	}
}

func TestCheckHTTPSAndCredentials(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	r := runCheck(t, Monitor{Name: "x", Type: TypeHTTP, URL: srv.URL})
	if r.OK || !strings.Contains(r.Msg, "certificate") {
		t.Fatalf("sertifikat tak dikenal harus gagal: %+v", r)
	}
	r = runCheck(t, Monitor{Name: "x", Type: TypeHTTP, URL: srv.URL, IgnoreTLS: true})
	if !r.OK || r.Cert == nil || r.Cert.NotAfter.IsZero() {
		t.Fatalf("ignoreTls: %+v", r)
	}
	// nothing listens here, and the error must not repeat the URL
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	r = runCheck(t, Monitor{Name: "x", Type: TypeHTTP, URL: "http://user:secret@" + addr + "/x?token=abc"})
	if r.OK || strings.Contains(r.Msg, "secret") || strings.Contains(r.Msg, "token=abc") {
		t.Fatalf("URL bocor ke pesan: %q", r.Msg)
	}
}

func TestCheckTCPPingDNS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if r := runCheck(t, Monitor{Name: "x", Type: TypeTCP, Host: "127.0.0.1", Port: port}); !r.OK {
		t.Fatalf("tcp terbuka: %+v", r)
	}
	ln.Close()
	if r := runCheck(t, Monitor{Name: "x", Type: TypeTCP, Host: "127.0.0.1", Port: port}); r.OK {
		t.Fatalf("tcp tertutup: %+v", r)
	}
	if _, err := exec.LookPath("ping"); err == nil {
		if r := runCheck(t, Monitor{Name: "x", Type: TypePing, Host: "127.0.0.1", TimeoutSec: 5}); !r.OK || r.Ms < 0 {
			t.Fatalf("ping: %+v", r)
		}
	}
	if r := runCheck(t, Monitor{Name: "x", Type: TypeDNS, Host: "localhost", TimeoutSec: 5}); !r.OK || !strings.Contains(r.Msg, "127.0.0.1") {
		t.Fatalf("dns: %+v", r)
	}
	if r := runCheck(t, Monitor{Name: "x", Type: TypeDNS, Host: "localhost", DNSExpect: "10.9.9.9", TimeoutSec: 5}); r.OK || !strings.Contains(r.Msg, "10.9.9.9") {
		t.Fatalf("dns expect: %+v", r)
	}
}

// ---- the service ----

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	t      *testing.T
	s      *Service
	st     *store.Store
	clock  *clock
	mu     sync.Mutex
	alerts []Alert
}

func newRig(t *testing.T, dir string) *rig {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(st)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, s: s, st: st, clock: &clock{t: time.Date(2026, 11, 3, 10, 30, 0, 0, time.UTC)}}
	s.Now = r.clock.now
	s.Host = func(string) (machine.Host, bool) { return local(), true }
	s.Alert = func(a Alert) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.alerts = append(r.alerts, a)
	}
	t.Cleanup(s.Close)
	return r
}

func (r *rig) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, a := range r.alerts {
		out = append(out, a.Kind)
	}
	return out
}

// beat records a check's outcome the way the loop does, without waiting for its timer.
func (r *rig) beat(id string, res Result) View {
	r.t.Helper()
	e := r.s.entry(id)
	if e == nil {
		r.t.Fatalf("monitor %s tidak ada", id)
	}
	e.mu.Lock()
	m := e.m
	e.mu.Unlock()
	r.s.record(e, m, res, false)
	return r.view(id)
}

func (r *rig) view(id string) View {
	r.t.Helper()
	for _, v := range r.s.List("p1") {
		if v.ID == id {
			return v
		}
	}
	r.t.Fatalf("monitor %s tidak ada", id)
	return View{}
}

func up() Result   { return Result{OK: true, Ms: 12, Msg: "200 OK"} }
func down() Result { return Result{Ms: -1, Msg: "connection refused"} }

func TestRetriesAlertsAndUptime(t *testing.T) {
	r := newRig(t, t.TempDir())
	v, err := r.s.Create(httpMonitor("http://127.0.0.1:1/"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusWaiting {
		t.Fatalf("baru dibuat: %s", v.Status)
	}
	id := v.ID
	if v = r.beat(id, up()); v.Status != StatusUp || len(r.seen()) != 0 {
		t.Fatalf("up pertama tidak perlu alert: %s %v", v.Status, r.seen())
	}
	r.clock.add(time.Minute)
	// one retry: the first failure is only pending
	if v = r.beat(id, down()); v.Status != StatusPending || len(r.seen()) != 0 {
		t.Fatalf("gagal pertama: %s %v", v.Status, r.seen())
	}
	r.clock.add(time.Minute)
	if v = r.beat(id, down()); v.Status != StatusDown || v.Msg != "connection refused" {
		t.Fatalf("gagal kedua: %+v", v)
	}
	if got := strings.Join(r.seen(), ","); got != KindDown {
		t.Fatalf("alert: %s", got)
	}
	// still down: no second alert, and no pending again
	r.clock.add(time.Minute)
	if v = r.beat(id, down()); v.Status != StatusDown || len(r.seen()) != 1 {
		t.Fatalf("masih down: %s %v", v.Status, r.seen())
	}
	r.clock.add(2 * time.Minute)
	if v = r.beat(id, up()); v.Status != StatusUp {
		t.Fatalf("pulih: %s", v.Status)
	}
	if got := strings.Join(r.seen(), ","); got != KindDown+","+KindUp {
		t.Fatalf("alert: %s", got)
	}
	r.mu.Lock()
	body := r.alerts[1].Body
	r.mu.Unlock()
	if body != "Down for 3 min" {
		t.Fatalf("lama down: %q", body)
	}
	// up up pending down down up: pending counts neither way
	if v.Uptime24h == nil || int(*v.Uptime24h) != 50 {
		t.Fatalf("uptime 24 jam: %v", v.Uptime24h)
	}
	evs, _ := r.s.Events(id, 10)
	if len(evs) != 3 || evs[0].Status != StatusUp || evs[0].DurSec != 180 || evs[1].Status != StatusDown {
		t.Fatalf("event: %+v", evs)
	}
	// a blip that passes within the retries: no event, no alert
	before := len(r.seen())
	r.beat(id, down())
	r.beat(id, up())
	if len(r.seen()) != before {
		t.Fatalf("blip memberi alert: %v", r.seen())
	}
	if evs, _ = r.s.Events(id, 10); len(evs) != 3 {
		t.Fatalf("blip jadi event: %+v", evs)
	}
}

func TestNotifyOff(t *testing.T) {
	r := newRig(t, t.TempDir())
	m := httpMonitor("http://127.0.0.1:1/")
	m.Notify, m.Retries = false, 0
	v, err := r.s.Create(m)
	if err != nil {
		t.Fatal(err)
	}
	if r.beat(v.ID, down()); len(r.seen()) != 0 {
		t.Fatalf("notify mati tetap alert: %v", r.seen())
	}
}

func TestRestartKeepsStateAndDoesNotAlert(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	m := httpMonitor("http://127.0.0.1:1/")
	m.Retries = 0
	v, err := r.s.Create(m)
	if err != nil {
		t.Fatal(err)
	}
	id := v.ID
	r.beat(id, up())
	r.clock.add(time.Minute)
	r.beat(id, down())
	if got := r.seen(); len(got) != 1 {
		t.Fatalf("alert: %v", got)
	}
	since := r.view(id).Since
	r.s.Close()

	// a line torn by a crash is skipped
	f, err := os.OpenFile(filepath.Join(dir, "uptime", id, "beats.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(f, `{"t":17`)
	f.Close()

	// the hub comes back two hours later and loads by that clock, not the wall's: the beats
	// are from a fixed day, and a later day would have aged them out
	later := r.clock.now().Add(time.Hour * 2)
	saved := timeNow
	timeNow = func() time.Time { return later }
	r2 := newRig(t, dir)
	timeNow = saved
	r2.clock.t = later
	v = r2.view(id)
	if v.Status != StatusDown || v.Since == nil || !v.Since.Equal(*since) || len(v.Recent) != 2 {
		t.Fatalf("setelah restart: %+v", v)
	}
	if v.Uptime24h == nil || int(*v.Uptime24h) != 50 {
		t.Fatalf("uptime setelah restart: %v", v.Uptime24h)
	}
	// still down: nothing to say. Back up: that news goes out.
	r2.beat(id, down())
	if got := r2.seen(); len(got) != 0 {
		t.Fatalf("restart memberi alert: %v", got)
	}
	r2.beat(id, up())
	if got := strings.Join(r2.seen(), ","); got != KindUp {
		t.Fatalf("alert setelah restart: %s", got)
	}
}

func TestHoursAndCompaction(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	m := httpMonitor("http://127.0.0.1:1/")
	m.Retries = 0
	v, err := r.s.Create(m)
	if err != nil {
		t.Fatal(err)
	}
	id := v.ID
	// five days, a check an hour; down on the last check of the third day
	for i := 0; i < 5*24; i++ {
		res := up()
		if i == 3*24-1 {
			res = down()
		}
		r.beat(id, res)
		r.clock.add(time.Hour)
	}
	lines := func(name string) int {
		b, err := os.ReadFile(filepath.Join(dir, "uptime", id, name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(b), "\n")
	}
	if n := lines("hours.jsonl"); n != 5*24-1 {
		t.Fatalf("jam tertutup: %d", n)
	}
	if n := lines("beats.jsonl"); n > 3*24+7 {
		t.Fatalf("beat lama tidak dibuang: %d baris", n)
	}
	series := func(rs *Service, name string) Series {
		t.Helper()
		out, err := rs.History(id, name)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	s7 := series(r.s, "7d")
	if s7.Stats.Checks != 5*24 || len(s7.Points) != 5*24 || s7.StepSec != 3600 {
		t.Fatalf("7d: %d cek, %d titik, langkah %d", s7.Stats.Checks, len(s7.Points), s7.StepSec)
	}
	if s7.Stats.Uptime == nil || *s7.Stats.Uptime >= 100 {
		t.Fatalf("7d uptime: %v", s7.Stats.Uptime)
	}
	if s := series(r.s, "1h"); s.Stats.Checks != 1 || len(s.Points) != 1 {
		t.Fatalf("1h: %+v", s)
	}
	if _, err := r.s.History(id, "2w"); err == nil {
		t.Fatal("range tak dikenal harus ditolak")
	}
	// a restart counts nothing twice
	checks := s7.Stats.Checks
	r.s.Close()
	now := r.clock.now()
	r2 := newRig(t, dir)
	r2.clock.t = now
	if got := series(r2.s, "7d").Stats.Checks; got != checks {
		t.Fatalf("setelah restart %d cek, sebelumnya %d", got, checks)
	}
	r2.beat(id, up())
	if got := series(r2.s, "7d").Stats.Checks; got != checks+1 {
		t.Fatalf("beat baru setelah restart: %d", got)
	}
}

func TestCertWarnings(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	m := httpMonitor("https://a.b/")
	v, err := r.s.Create(m)
	if err != nil {
		t.Fatal(err)
	}
	id := v.ID
	res := up()
	res.Cert = &Cert{Subject: "a.b", Issuer: "CA", NotAfter: r.clock.now().Add(30 * 24 * time.Hour)}
	r.beat(id, res)
	if len(r.seen()) != 0 {
		t.Fatalf("30 hari: %v", r.seen())
	}
	r.clock.add(17 * 24 * time.Hour)
	r.beat(id, res)
	r.beat(id, res)
	if got := r.seen(); len(got) != 1 || got[0] != KindCert {
		t.Fatalf("13 hari, sekali saja: %v", got)
	}
	// a restart does not repeat it
	now := r.clock.now()
	r.s.Close()
	r2 := newRig(t, dir)
	r2.clock.t = now
	r2.beat(id, res)
	if got := r2.seen(); len(got) != 0 {
		t.Fatalf("restart mengulang peringatan: %v", got)
	}
	// the next threshold does get one
	r2.clock.add(7 * 24 * time.Hour)
	r2.beat(id, res)
	if got := r2.seen(); len(got) != 1 {
		t.Fatalf("6 hari: %v", got)
	}
	// a renewed certificate starts over
	res.Cert = &Cert{NotAfter: r2.clock.now().Add(90 * 24 * time.Hour)}
	r2.beat(id, res)
	res.Cert = &Cert{NotAfter: r2.clock.now().Add(10 * 24 * time.Hour)}
	r2.beat(id, res)
	if got := r2.seen(); len(got) != 2 {
		t.Fatalf("sertifikat baru: %v", got)
	}
}

func TestPush(t *testing.T) {
	r := newRig(t, t.TempDir())
	v, err := r.s.Create(Monitor{ProjectID: "p1", Name: "cron", Type: TypePush, IntervalSec: 60, Active: true, Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.PushToken) < 30 {
		t.Fatalf("token terlalu pendek: %q", v.PushToken)
	}
	e := r.s.entry(v.ID)
	m := e.m
	// nothing yet: waiting, not down
	res, _, due := r.s.pushDue(e, m)
	if !due || !res.Waiting {
		t.Fatalf("belum pernah push: %+v", res)
	}
	if r.s.Push("nope", false, "", -1) {
		t.Fatal("token asing diterima")
	}
	if !r.s.Push(v.PushToken, false, "backup done", 42) {
		t.Fatal("push ditolak")
	}
	if got := r.view(v.ID); got.Status != StatusUp || got.Ms != 42 || got.Msg != "backup done" {
		t.Fatalf("setelah push: %+v", got)
	}
	// a flood counts once
	r.s.Push(v.PushToken, false, "", -1)
	if got := r.view(v.ID); len(got.Recent) != 1 {
		t.Fatalf("banjir push: %d beat", len(got.Recent))
	}
	r.clock.add(30 * time.Second)
	if _, next, due := r.s.pushDue(e, m); due || next <= 0 {
		t.Fatalf("belum waktunya: %v %v", next, due)
	}
	r.clock.add(36 * time.Second)
	res, _, due = r.s.pushDue(e, m)
	if !due || res.OK || res.Waiting {
		t.Fatalf("terlambat: %+v %v", res, due)
	}
	r.s.record(e, m, res, false)
	if got := r.view(v.ID); got.Status != StatusDown {
		t.Fatalf("tanpa push: %s", got.Status)
	}
	if got := strings.Join(r.seen(), ","); got != KindDown {
		t.Fatalf("alert: %s", got)
	}
	r.clock.add(2 * time.Second)
	r.s.Push(v.PushToken, false, "", -1)
	if got := r.view(v.ID); got.Status != StatusUp {
		t.Fatalf("push lagi: %s", got.Status)
	}
	// a job can report its own failure, with no retries in between
	r.clock.add(2 * time.Second)
	r.s.Push(v.PushToken, true, "disk full", -1)
	if got := r.view(v.ID); got.Status != StatusDown || got.Msg != "disk full" {
		t.Fatalf("lapor gagal: %+v", got)
	}
	// paused: not taken, and the token keeps working after
	if _, err := r.s.Update(v.ID, func(m *Monitor) error { m.Active = false; return nil }); err != nil {
		t.Fatal(err)
	}
	r.clock.add(2 * time.Second)
	if r.s.Push(v.PushToken, false, "", -1) {
		t.Fatal("monitor pause menerima push")
	}
}

func TestPushToleratesHubDowntime(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	v, err := r.s.Create(Monitor{ProjectID: "p1", Name: "cron", Type: TypePush, IntervalSec: 60, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	r.s.Push(v.PushToken, false, "", -1)
	now := r.clock.now()
	r.s.Close()
	// the hub comes back an hour later: its own absence is no missed push
	st, _ := store.Open(dir)
	r2 := &rig{t: t, st: st, clock: &clock{t: now.Add(time.Hour)}}
	saved := timeNow
	timeNow = r2.clock.now
	defer func() { timeNow = saved }()
	s2, err := New(st)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	s2.Now = r2.clock.now
	e := s2.entry(v.ID)
	if res, _, due := s2.pushDue(e, e.m); due {
		t.Fatalf("hub mati dianggap push terlewat: %+v", res)
	}
	r2.clock.add(70 * time.Second)
	if _, _, due := s2.pushDue(e, e.m); !due {
		t.Fatal("setelah interval penuh harus jatuh tempo")
	}
}

func TestMachineAway(t *testing.T) {
	r := newRig(t, t.TempDir())
	r.s.Host = func(string) (machine.Host, bool) { return away{}, true }
	m := httpMonitor("http://127.0.0.1:1/")
	m.MachineID, m.Retries = "m_srv", 0
	v, err := r.s.Create(m)
	if err != nil {
		t.Fatal(err)
	}
	e := r.s.entry(v.ID)
	r.s.runOnce(context.Background(), e, e.m)
	got := r.view(v.ID)
	if got.Status != StatusWaiting || got.Msg != "Waiting for srv" || len(got.Recent) != 0 || len(r.seen()) != 0 {
		t.Fatalf("machine jauh bukan target mati: %+v %v", got, r.seen())
	}
	r.s.Host = func(string) (machine.Host, bool) { return nil, false }
	r.s.runOnce(context.Background(), e, e.m)
	if got := r.view(v.ID); got.Status != StatusWaiting || got.Msg != "Machine removed" {
		t.Fatalf("machine dihapus: %+v", got)
	}
	if _, err := r.s.Update(v.ID, func(m *Monitor) error { m.Name = "x"; return nil }); err == nil {
		t.Fatal("machine yang hilang harus ditolak saat disimpan")
	}
}

func TestLoopChecksAndStops(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	r := newRig(t, t.TempDir())
	r.s.Now = time.Now
	beats := make(chan string, 16)
	var firstEvent atomic.Value
	r.s.Publish = func(v any) {
		ev, ok := v.(map[string]any)
		if !ok {
			return
		}
		firstEvent.CompareAndSwap(nil, ev["type"])
		if ev["type"] == "uptimeBeat" {
			beats <- ev["monitorId"].(string)
		}
	}
	r.s.Start()
	v, err := r.s.Create(httpMonitor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	wait := func(what string) {
		t.Helper()
		select {
		case <-beats:
		case <-time.After(5 * time.Second):
			t.Fatalf("tidak ada beat: %s", what)
		}
	}
	wait("langsung setelah dibuat")
	if got := firstEvent.Load(); got != "uptimeMonitor" {
		t.Fatalf("heartbeat datang sebelum monitornya dikenal: %v", got)
	}
	if err := r.s.CheckNow(v.ID); err != nil {
		t.Fatal(err)
	}
	wait("check now")
	if _, err := r.s.Update(v.ID, func(m *Monitor) error { m.Active = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := r.view(v.ID).Status; got != StatusPaused {
		t.Fatalf("pause: %s", got)
	}
	if err := r.s.CheckNow(v.ID); err != ErrPaused {
		t.Fatalf("check saat pause: %v", err)
	}
	n := hits.Load()
	if _, err := r.s.Update(v.ID, func(m *Monitor) error { m.Active = true; return nil }); err != nil {
		t.Fatal(err)
	}
	wait("lanjut setelah pause")
	if hits.Load() != n+1 {
		t.Fatalf("hit: %d lalu %d", n, hits.Load())
	}
	if got := r.view(v.ID).Status; got != StatusUp {
		t.Fatalf("setelah lanjut: %s", got)
	}
	if err := r.s.Delete(v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.st.Path("uptime", v.ID)); !os.IsNotExist(err) {
		t.Fatalf("folder monitor masih ada: %v", err)
	}
	if err := r.s.Delete(v.ID); err != ErrNotFound {
		t.Fatalf("hapus dua kali: %v", err)
	}
}

func TestRemoveProjectAndLimit(t *testing.T) {
	r := newRig(t, t.TempDir())
	for i := 0; i < MaxPerProject; i++ {
		if _, err := r.s.Create(Monitor{ProjectID: "p1", Name: "x", Type: TypePing, Host: "127.0.0.1"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.s.Create(Monitor{ProjectID: "p1", Name: "x", Type: TypePing, Host: "127.0.0.1"}); err != ErrLimit {
		t.Fatalf("batas: %v", err)
	}
	other, err := r.s.Create(Monitor{ProjectID: "p2", Name: "y", Type: TypePing, Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	r.s.RemoveProject("p1")
	if n := len(r.s.List("p1")); n != 0 {
		t.Fatalf("masih %d monitor", n)
	}
	if n := len(r.s.List("p2")); n != 1 || r.s.entry(other.ID) == nil {
		t.Fatalf("project lain ikut hilang: %d", n)
	}
}

func TestWebhook(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(500)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		mu.Lock()
		got = append(got, v)
		mu.Unlock()
	}))
	defer srv.Close()
	saved := webhookRetries
	webhookRetries = []time.Duration{0, 10 * time.Millisecond}
	defer func() { webhookRetries = saved }()

	r := newRig(t, t.TempDir())
	if _, err := r.s.SetWebhook("p1", "ftp://x"); err == nil {
		t.Fatal("skema selain http harus ditolak")
	}
	if err := r.s.TestWebhook(context.Background(), "p1"); err == nil {
		t.Fatal("tes tanpa webhook harus gagal")
	}
	secretURL := srv.URL + "/hooks/secret-token"
	if _, err := r.s.SetWebhook("p1", secretURL); err != nil {
		t.Fatal(err)
	}
	if err := r.s.TestWebhook(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	m := httpMonitor("https://user:pw@a.b/")
	m.Retries = 0
	v, err := r.s.Create(m)
	if err != nil {
		t.Fatal(err)
	}
	r.beat(v.ID, down())
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	if len(got) != 2 {
		mu.Unlock()
		t.Fatalf("webhook: %d kiriman", len(got))
	}
	a := got[1]
	mu.Unlock()
	if a["kind"] != KindDown || a["text"] != a["content"] || !strings.Contains(a["text"].(string), "site is down: connection refused") {
		t.Fatalf("isi webhook: %v", a)
	}
	if strings.Contains(fmt.Sprint(a), "pw") {
		t.Fatalf("password URL bocor ke webhook: %v", a)
	}

	// a receiver that fails is noted, without saying the URL
	fail.Store(true)
	err = r.s.TestWebhook(context.Background(), "p1")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("gagal: %v", err)
	}
	if c := r.s.Settings("p1"); c.LastError == "" || strings.Contains(c.LastError, "secret-token") {
		t.Fatalf("catatan gagal: %+v", c)
	}
	srv.Close()
	err = r.s.TestWebhook(context.Background(), "p1")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("URL bocor saat receiver mati: %v", err)
	}
}
