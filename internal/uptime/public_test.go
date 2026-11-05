package uptime

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testProject = "p1"

// Every value here is something a stranger must never see. Each is checked for by itself.
var publicCanaries = []string{
	"secret-host.internal", "8443", "/admin/health", "URLTOKEN", "basicuser", "basicpass",
	"Authorization", "HEADERSECRET", "KEYWORDSECRET", "INTERNAL-MONITOR-NAME",
	"10.20.30.40", "dial tcp", "connection refused", "m_srv-box", "tcp-secret-db", "5432",
	"dns-secret.internal", "PUSH", "ERRTEXT", "TLSSUBJECT", "NextBoard", "nextboard",
	"HIDDEN-MONITOR", "hidden-host.internal", "<script", "src=", "href=", "http://", "https://",
}

func assertNoCanary(t *testing.T, page string) {
	t.Helper()
	for _, c := range publicCanaries {
		if strings.Contains(page, c) {
			t.Errorf("the public page shows %q:\n%s", c, page)
		}
	}
}

func publicRig(t *testing.T) (*rig, Monitor, string) {
	t.Helper()
	r := newRig(t, t.TempDir())
	v, err := r.s.Create(Monitor{ProjectID: testProject, Name: "INTERNAL-MONITOR-NAME", Type: TypeHTTP,
		URL:     "https://basicuser:basicpass@secret-host.internal:8443/admin/health?t=URLTOKEN",
		Headers: []Header{{Name: "Authorization", Value: "Bearer HEADERSECRET"}}, Keyword: "KEYWORDSECRET",
		Public: true, PublicName: "Website", Active: true, Retries: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Create(Monitor{ProjectID: testProject, Name: "HIDDEN-MONITOR", Type: TypeHTTP, URL: "https://hidden-host.internal/", Active: true}); err != nil {
		t.Fatal(err)
	}
	c, err := r.s.SetPublic(testProject, true, "Acme status")
	if err != nil || c.Token == "" {
		t.Fatalf("%v %+v", err, c)
	}
	return r, v.Monitor, c.Token
}

func openPublic(t *testing.T, r *rig, token string) string {
	t.Helper()
	b, ok := r.s.PublicHTML(token)
	if !ok {
		t.Fatal("no page")
	}
	return string(b)
}

func TestPublicPageShowsOnlyWhatItMay(t *testing.T) {
	r, m, token := publicRig(t)
	r.beat(m.ID, Result{OK: true, Ms: 42})
	r.s.dropPublicCache()
	page := openPublic(t, r, token)
	for _, want := range []string{"Acme status", "Website", "Operational", "All services operational", "100.00% uptime"} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q in:\n%s", want, page)
		}
	}
	assertNoCanary(t, page)

	// An outage, with an error that names internal addresses.
	r.clock.add(10 * time.Minute)
	r.beat(m.ID, Result{Msg: "ERRTEXT dial tcp 10.20.30.40:5432: connection refused"})
	r.s.dropPublicCache()
	page = openPublic(t, r, token)
	for _, want := range []string{"Some services are down", "Down", "Outages in the last 30 days", "ongoing"} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q in:\n%s", want, page)
		}
	}
	assertNoCanary(t, page)

	r.clock.add(7 * time.Minute)
	r.beat(m.ID, Result{OK: true, Ms: 50})
	r.s.dropPublicCache()
	page = openPublic(t, r, token)
	if !strings.Contains(page, "7 min") || !strings.Contains(page, "All services operational") {
		t.Errorf("after coming back:\n%s", page)
	}
	if strings.Contains(page, "ongoing") {
		t.Errorf("still ongoing:\n%s", page)
	}
	assertNoCanary(t, page)
	// Response times are not shown, in any form.
	if strings.Contains(page, " ms") || strings.Contains(page, "42") {
		t.Errorf("a response time on the page:\n%s", page)
	}
}

// Other kinds of monitor, whose secrets sit in other fields.
func TestPublicPageOfOtherTypes(t *testing.T) {
	r := newRig(t, t.TempDir())
	for _, m := range []Monitor{
		{Name: "tcp-secret-db", Type: TypeTCP, Host: "tcp-secret-db", Port: 5432, Public: true, PublicName: "Database"},
		{Name: "dns", Type: TypeDNS, Host: "dns-secret.internal", DNSServer: "10.20.30.40", Public: true, PublicName: "Names"},
		{Name: "PUSH cron", Type: TypePush, Public: true, PublicName: "Nightly job"},
	} {
		m.ProjectID, m.Active = testProject, true
		if _, err := r.s.Create(m); err != nil {
			t.Fatal(err)
		}
	}
	c, err := r.s.SetPublic(testProject, true, "")
	if err != nil {
		t.Fatal(err)
	}
	page := openPublic(t, r, c.Token)
	for _, want := range []string{"<title>Status</title>", "Database", "Names", "Nightly job", "No data"} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q in:\n%s", want, page)
		}
	}
	assertNoCanary(t, page)
	for _, m := range r.s.List(testProject) {
		if strings.Contains(page, m.PushToken) && m.PushToken != "" {
			t.Errorf("the push token is on the page")
		}
		if strings.Contains(page, m.ID) {
			t.Errorf("a monitor id is on the page")
		}
	}
}

// What is in the file is what the page's address depends on; it must survive a restart.
func TestPublicPageSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	if _, err := r.s.Create(Monitor{ProjectID: testProject, Name: "x", Type: TypePush, Public: true, PublicName: "Job", Active: true}); err != nil {
		t.Fatal(err)
	}
	c, err := r.s.SetPublic(testProject, true, "Title")
	if err != nil {
		t.Fatal(err)
	}
	// The webhook setter must not wipe it.
	if _, err := r.s.SetWebhook(testProject, "https://example.com/hook"); err != nil {
		t.Fatal(err)
	}
	r.s.Close()
	r2 := newRig(t, dir)
	if got := r2.s.Settings(testProject).Public; !got.Enabled || got.Token != c.Token || got.Title != "Title" {
		t.Fatalf("after restart %+v", got)
	}
	if page := openPublic(t, r2, c.Token); !strings.Contains(page, "Job") {
		t.Fatalf("page:\n%s", page)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, settingsFile)); !strings.Contains(string(raw), c.Token) {
		t.Fatalf("settings file: %s", raw)
	}
}

func TestPublicPageAddress(t *testing.T) {
	r, m, token := publicRig(t)
	if b, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(b) != 24 {
		t.Fatalf("token %q: %d bytes, %v", token, len(b), err)
	}
	for _, bad := range []string{"", "x", token[:len(token)-1], token + "x", strings.ToUpper(token)} {
		if _, ok := r.s.PublicHTML(bad); ok {
			t.Errorf("%q opened the page", bad)
		}
	}
	// Turned off, it counts at once, whatever is cached.
	openPublic(t, r, token)
	if _, err := r.s.SetPublic(testProject, false, "Acme status"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.s.PublicHTML(token); ok {
		t.Fatal("a page turned off still answers")
	}
	// Off and on keeps the address, so a link already given out comes back.
	c, err := r.s.SetPublic(testProject, true, "Acme status")
	if err != nil || c.Token != token {
		t.Fatalf("%v %+v", err, c)
	}
	openPublic(t, r, token)
	// A new address: the old one is gone at once.
	c, err = r.s.RotatePublic(testProject)
	if err != nil || c.Token == token || !c.Enabled {
		t.Fatalf("%v %+v", err, c)
	}
	if _, ok := r.s.PublicHTML(token); ok {
		t.Fatal("the old address still answers")
	}
	openPublic(t, r, c.Token)

	// Taken off the page, also at once. A cached page must not outlive that.
	if page := openPublic(t, r, c.Token); !strings.Contains(page, "Website") {
		t.Fatalf("page:\n%s", page)
	}
	if _, err := r.s.Update(m.ID, func(x *Monitor) error { x.Public = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if page := openPublic(t, r, c.Token); strings.Contains(page, "Website") {
		t.Fatalf("still on the page:\n%s", page)
	}
	// A project that is gone takes its page with it.
	r.s.RemoveProject(testProject)
	if _, ok := r.s.PublicHTML(c.Token); ok {
		t.Fatal("the page of a removed project")
	}
}

func TestPublicNameGivesNothingAway(t *testing.T) {
	r := newRig(t, t.TempDir())
	base := Monitor{ProjectID: testProject, Name: "x", Type: TypeHTTP, URL: "https://api.acme.internal/health", Active: true, Public: true}
	for _, bad := range []string{"", "   ", "api.acme.internal", "API.ACME.INTERNAL status", "see https://x", "db at 10.1.2.3", strings.Repeat("x", maxPublicName+1)} {
		m := base
		m.PublicName = bad
		if _, err := r.s.Create(m); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
	tcp := Monitor{ProjectID: testProject, Name: "x", Type: TypeTCP, Host: "db7", Port: 5432, Active: true, Public: true, PublicName: "Our db7"}
	if _, err := r.s.Create(tcp); err == nil {
		t.Error("the host as a name was taken")
	}
	m := base
	m.PublicName = "  Our   API "
	v, err := r.s.Create(m)
	if err != nil || v.PublicName != "Our API" {
		t.Fatalf("%v %+v", err, v.PublicName)
	}
	// Not public: a name may say anything, since nobody sees it.
	m = base
	m.Public, m.PublicName = false, "api.acme.internal"
	if _, err := r.s.Create(m); err != nil {
		t.Fatal(err)
	}
	// The name comes through escaped.
	x := base
	x.PublicName = `<b onload="x">Tag</b>`
	if _, err := r.s.Create(x); err != nil {
		t.Fatal(err)
	}
	c, _ := r.s.SetPublic(testProject, true, `<img src=x>`)
	page := openPublic(t, r, c.Token)
	if strings.Contains(page, "<img") || strings.Contains(page, "<b ") || !strings.Contains(page, "&lt;b onload") {
		t.Fatalf("not escaped:\n%s", page)
	}
}

// Anyone can open it as often as they like: a page is worked out once per 15 seconds.
func TestPublicPageIsCached(t *testing.T) {
	r, m, token := publicRig(t)
	r.beat(m.ID, Result{OK: true})
	r.s.dropPublicCache()
	first, _ := r.s.PublicHTML(token)
	r.clock.add(time.Second)
	r.beat(m.ID, Result{Msg: "down"})
	if second, _ := r.s.PublicHTML(token); string(second) != string(first) {
		t.Fatal("not cached")
	}
	r.clock.add(publicCacheFor)
	if third, _ := r.s.PublicHTML(token); !strings.Contains(string(third), "Some services are down") {
		t.Fatalf("stale:\n%s", third)
	}
}

// A blip inside the retries is not an outage to the public either.
func TestPublicPageIgnoresBlips(t *testing.T) {
	r := newRig(t, t.TempDir())
	v, err := r.s.Create(Monitor{ProjectID: testProject, Name: "x", Type: TypeHTTP, URL: "https://a.example/", Active: true, Retries: 2, Public: true, PublicName: "Web"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.s.SetPublic(testProject, true, "")
	r.beat(v.ID, Result{OK: true})
	r.clock.add(time.Minute)
	r.beat(v.ID, Result{Msg: "blip"})
	r.s.dropPublicCache()
	page := openPublic(t, r, c.Token)
	if !strings.Contains(page, "Operational") || strings.Contains(page, "Outages") {
		t.Fatalf("a blip:\n%s", page)
	}
}

// The headline claims only what was checked.
func TestPublicPageHeadline(t *testing.T) {
	r := newRig(t, t.TempDir())
	a, err := r.s.Create(Monitor{ProjectID: testProject, Name: "a", Type: TypeHTTP, URL: "https://a.example/", Active: true, Public: true, PublicName: "One"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.s.Create(Monitor{ProjectID: testProject, Name: "b", Type: TypeHTTP, URL: "https://b.example/", Active: true, Public: true, PublicName: "Two"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.s.SetPublic(testProject, true, "")
	headline := func(want string) {
		t.Helper()
		r.s.dropPublicCache()
		if page := openPublic(t, r, c.Token); !strings.Contains(page, want) {
			t.Fatalf("no %q in:\n%s", want, page)
		}
	}
	headline("No data yet")
	r.beat(a.ID, Result{OK: true})
	headline("No known problems, some services have no data")
	r.beat(b.ID, Result{OK: true})
	headline("All services operational")
	r.beat(b.ID, Result{Msg: "x"})
	r.beat(b.ID, Result{Msg: "x"})
	headline("Some services are down")
}
