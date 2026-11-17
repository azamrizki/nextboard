package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/instance"
	"github.com/azamrizki/nextboard/internal/update"
	"github.com/azamrizki/nextboard/internal/update/updatetest"
	"github.com/azamrizki/nextboard/internal/version"
)

func TestHealthAndHubTellTheBuild(t *testing.T) {
	h := newTestHub(t)
	b := version.Info()
	st, health := h.do("GET", "/api/health", nil, nil)
	if st != 200 || health["version"] != b.Version || health["commit"] != b.Commit || health["builtAt"] != b.BuiltAt || health["channel"] != b.Channel {
		t.Fatalf("health %d %v, build %+v", st, health, b)
	}
	if b.Commit == "" {
		t.Fatal("no commit, not even unknown")
	}
	h.setup()
	st, hub := h.do("GET", "/api/hub", nil, nil)
	if st != 200 || hub["version"] != b.Version || hub["commit"] != b.Commit || hub["builtAt"] != b.BuiltAt || hub["channel"] != b.Channel || hub["repo"] != version.Repo {
		t.Fatalf("hub %d %v", st, hub)
	}
}

// fakeRelease points the hub's update at a fake GitHub and a stand-in binary, as a release
// build run by its service; nothing real is restarted or replaced.
type fakeRelease struct {
	gh       *updatetest.Fake
	bin      string
	restarts atomic.Int32
	managed  error
	// services are the instances whose service runs bin; none read from the real home
	services []instance.Instance
}

func withFakeRelease(t *testing.T, h *testHub, current string) *fakeRelease {
	t.Helper()
	f := &fakeRelease{gh: updatetest.New(t, "example-owner/nextboard"), bin: filepath.Join(t.TempDir(), "nextboard")}
	if err := os.WriteFile(f.bin, []byte("hub "+current), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &h.srv.hubUpdate
	u.current = func() string { return current }
	u.client = func(time.Duration) *update.Client { return f.gh.Client() }
	u.binPath = func() (string, error) { return f.bin, nil }
	u.managed = func(string) error { return f.managed }
	u.restart = func() error { f.restarts.Add(1); return nil }
	u.services = func(bin string) ([]instance.Instance, error) {
		if bin != f.bin {
			t.Errorf("services of %s, not %s", bin, f.bin)
		}
		return f.services, nil
	}
	return f
}

func TestHubUpdateInstallsThenRestarts(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	f := withFakeRelease(t, h, "0.9.0")
	f.gh.Publish(t, "v0.9.0", false, []byte("hub 0.9.0"))
	f.gh.Publish(t, "v0.9.1", false, []byte("hub 0.9.1"))
	// this hub (the default instance) and a sibling run the binary: both restart, named first
	f.services = []instance.Instance{
		{Name: "default", Listen: "127.0.0.1:12312", Service: &instance.Service{Kind: "systemd", Name: "nextboard.service"}},
		{Name: "work", Listen: "127.0.0.1:12412", Service: &instance.Service{Kind: "systemd", Name: "nextboard-work.service"}},
	}

	st, c := h.do("GET", "/api/hub/update", nil, nil)
	if st != 200 || c["status"] != "available" || c["latest"] != "v0.9.1" || c["current"] != "0.9.0" || c["channel"] != "stable" || c["command"] != "nextboard update" || c["blocked"] != nil {
		t.Fatalf("check %d %v", st, c)
	}
	if r, _ := c["restarts"].([]any); len(r) != 2 || r[0].(map[string]any)["self"] != true || r[1].(map[string]any)["name"] != "work" ||
		r[1].(map[string]any)["listen"] != "127.0.0.1:12412" || r[1].(map[string]any)["service"] != "nextboard-work.service" || r[1].(map[string]any)["self"] != nil {
		t.Fatalf("restarts %v", c["restarts"])
	}
	// asked once an hour, however often the panel opens
	asked := len(f.gh.Requests())
	h.do("GET", "/api/hub/update", nil, nil)
	if len(f.gh.Requests()) != asked {
		t.Fatalf("asked GitHub again: %v", f.gh.Requests())
	}

	st, j := h.do("POST", "/api/hub/update", map[string]any{"version": "v0.9.1"}, nil)
	if st != 202 || j["kind"] != "hubUpdate" || j["purpose"] != "v0.9.1" {
		t.Fatalf("start %d %v", st, j)
	}
	done := h.waitJob(j["id"].(string))
	if done["state"] != "done" {
		t.Fatalf("job %v", done)
	}
	res := done["result"].(map[string]any)
	if res["from"] != "0.9.0" || res["to"] != "v0.9.1" || res["old"] != f.bin+".old" || fmt.Sprint(res["restarts"]) != "[default work]" {
		t.Fatalf("result %v", res)
	}
	if b, _ := os.ReadFile(f.bin); string(b) != "hub 0.9.1" {
		t.Fatalf("binary %q", b)
	}
	if b, _ := os.ReadFile(f.bin + ".old"); string(b) != "hub 0.9.0" {
		t.Fatalf("old binary %q", b)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.restarts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if f.restarts.Load() != 1 {
		t.Fatalf("restarted %d times", f.restarts.Load())
	}
	// until the hub restarts into it, the panel says so and a second update is refused
	_, c = h.do("GET", "/api/hub/update", nil, nil)
	if c["installed"] != "v0.9.1" {
		t.Fatalf("after %v", c)
	}
	if st, e := h.do("POST", "/api/hub/update", nil, nil); st != 409 || e["code"] != "restart_pending" {
		t.Fatalf("again %d %v", st, e)
	}
}

func TestHubUpdateRefusals(t *testing.T) {
	h := newTestHub(t)
	h.setup()

	// a dev build (this test binary): no check, no update
	st, c := h.do("GET", "/api/hub/update", nil, nil)
	if st != 200 || c["status"] != "dev" {
		t.Fatalf("dev check %d %v", st, c)
	}
	if st, e := h.do("POST", "/api/hub/update", nil, nil); st != 409 || e["code"] != "dev_build" || !strings.Contains(e["error"].(string), "git pull") {
		t.Fatalf("dev %d %v", st, e)
	}

	f := withFakeRelease(t, h, "0.9.0-beta.2")
	f.gh.Publish(t, "v0.9.0-beta.2", true, []byte("b2"))
	f.gh.Publish(t, "v0.9.0-beta.3", true, []byte("b3"))

	// the web's CSRF header, and admins only
	if st, _ := h.do("POST", "/api/hub/update", nil, map[string]string{"X-NextBoard": ""}); st != 403 {
		t.Fatalf("no CSRF header: %d", st)
	}
	anon := &testHub{t: t, srv: h.srv, ts: h.ts, c: h.ts.Client()}
	if st, _ := anon.do("POST", "/api/hub/update", nil, nil); st != 401 {
		t.Fatalf("logged out: %d", st)
	}

	// not the installed service: it could not restart itself
	f.managed = update.ErrNotService
	_, c = h.do("GET", "/api/hub/update", nil, nil)
	if c["status"] != "available" || c["latest"] != "v0.9.0-beta.3" || c["blockedCode"] != "not_service" || c["command"] != "nextboard update --version v0.9.0-beta.3" {
		t.Fatalf("beta check %v", c)
	}
	if st, e := h.do("POST", "/api/hub/update", nil, nil); st != 409 || e["code"] != "not_service" || !strings.Contains(e["error"].(string), "nextboard update") {
		t.Fatalf("not a service %d %v", st, e)
	}
	f.managed = nil

	// a folder the hub cannot write
	if os.Getuid() != 0 {
		dir := filepath.Dir(f.bin)
		_ = os.Chmod(dir, 0o555)
		st, e := h.do("POST", "/api/hub/update", nil, nil)
		_ = os.Chmod(dir, 0o755)
		if st != 409 || e["code"] != "not_writable" {
			t.Fatalf("read-only folder %d %v", st, e)
		}
	}

	// the newest is not what the owner confirmed
	if st, e := h.do("POST", "/api/hub/update", map[string]any{"version": "v0.9.0-beta.9"}, nil); st != 409 || e["code"] != "version_changed" {
		t.Fatalf("other version %d %v", st, e)
	}

	// a release that does not check out: the job fails, nothing is swapped or restarted
	f.gh.Tamper("v0.9.0-beta.3", []byte("not an archive")) // so the job fails and nothing is swapped
	st, j := h.do("POST", "/api/hub/update", map[string]any{"version": "v0.9.0-beta.3"}, nil)
	if st != 202 {
		t.Fatalf("start %d %v", st, j)
	}
	failed := h.waitJob(j["id"].(string))
	if failed["state"] != "failed" || failed["code"] != "update_failed" || !strings.Contains(failed["error"].(string), "does not match") {
		t.Fatalf("tampered %v", failed)
	}
	if b, _ := os.ReadFile(f.bin); string(b) != "hub 0.9.0-beta.2" {
		t.Fatalf("binary changed: %q", b)
	}
	if _, err := os.Stat(f.bin + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old kept for a failed update: %v", err)
	}
	if f.restarts.Load() != 0 {
		t.Fatal("restarted after a failed update")
	}
	// one update at a time
	if !h.srv.hubUpdate.start.TryLock() {
		t.Fatal("start held")
	}
	st, e := h.do("POST", "/api/hub/update", nil, nil)
	h.srv.hubUpdate.start.Unlock()
	if st != 409 || e["code"] != "update_running" {
		t.Fatalf("while starting %d %v", st, e)
	}

	// up to date: never a step back
	f2 := withFakeRelease(t, h, "0.9.0-beta.3")
	f2.gh.Publish(t, "v0.9.0-beta.3", true, []byte("b3"))
	f2.gh.Publish(t, "v0.8.1", false, []byte("old"))
	if st, e := h.do("POST", "/api/hub/update", nil, nil); st != 409 || e["code"] != "up_to_date" {
		t.Fatalf("up to date %d %v", st, e)
	}
}

func TestHubUpdateOffline(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	f := withFakeRelease(t, h, "0.9.0")
	f.gh.Close()
	st, c := h.do("GET", "/api/hub/update", nil, nil)
	if st != 200 || c["status"] != "unknown" || c["error"] == nil {
		t.Fatalf("offline check %d %v", st, c)
	}
	if st, e := h.do("POST", "/api/hub/update", nil, nil); st != 502 || e["code"] != "check_failed" {
		t.Fatalf("offline update %d %v", st, e)
	}
}

// After the swap the siblings on the binary restart first, then this hub, none waiting: the
// service manager is what stops this hub.
func TestHubUpdateRestartsTheSiblingsThenItself(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	tools := t.TempDir()
	calls := filepath.Join(tools, "calls.log")
	if err := os.WriteFile(filepath.Join(tools, "systemctl"), []byte("#!/bin/sh\necho \"systemctl $*\" >> \""+calls+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	svc := func(name string) *instance.Service { return &instance.Service{Kind: "systemd", Name: name} }
	u := &hubUpdater{services: func(string) ([]instance.Instance, error) {
		return []instance.Instance{{Name: "default", Service: svc("nextboard.service")}, {Name: "personal", Service: svc("nextboard-personal.service")}, {Name: "work", Service: svc("nextboard-work.service")}}, nil
	}}
	if err := u.restartAll("personal", "/opt/nextboard"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(calls)
	want := "systemctl --user --no-block restart nextboard\nsystemctl --user --no-block restart nextboard-work\nsystemctl --user --no-block restart nextboard-personal\n"
	if string(raw) != want {
		t.Fatalf("calls:\n%s", raw)
	}
}

// The popup that offers a newer release when the app opens: it asks while one is out, "later"
// keeps it away for a day, "skip" for that release only, and the answer holds for the whole hub.
func TestHubUpdateReminder(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	f := withFakeRelease(t, h, "0.9.0")
	f.gh.Publish(t, "v0.9.0", false, []byte("hub 0.9.0"))
	f.gh.Publish(t, "v0.9.1", false, []byte("hub 0.9.1"))
	remind := func() (bool, map[string]any) {
		t.Helper()
		st, c := h.do("GET", "/api/hub/update", nil, nil)
		if st != 200 {
			t.Fatalf("check %d %v", st, c)
		}
		return c["remind"] == true, c
	}
	if on, c := remind(); !on || c["skipped"] != nil || c["remindAfter"] != nil {
		t.Fatalf("a new release asks: %v", c)
	}

	// later: not for 24 hours
	st, r := h.do("POST", "/api/hub/update/remind", map[string]any{"action": "later"}, nil)
	if st != 200 || r["remind"] != false {
		t.Fatalf("later %d %v", st, r)
	}
	at, err := time.Parse(time.RFC3339Nano, r["remindAfter"].(string))
	if err != nil || at.Sub(time.Now()) < 23*time.Hour || at.Sub(time.Now()) > 25*time.Hour {
		t.Fatalf("remindAfter %v %v", r["remindAfter"], err)
	}
	if on, c := remind(); on {
		t.Fatalf("asked again within the day: %v", c)
	}
	// a day later it asks again
	past := time.Now().Add(-time.Minute)
	if err := h.srv.st.Save(updateReminderFile, updateReminder{RemindAfter: &past}); err != nil {
		t.Fatal(err)
	}
	if on, _ := remind(); !on {
		t.Fatal("not asked again after the day")
	}

	// skip this version: never for v0.9.1, also once the day is over
	if st, r := h.do("POST", "/api/hub/update/remind", map[string]any{"action": "skip", "version": "v0.9.1"}, nil); st != 200 || r["skipped"] != "v0.9.1" {
		t.Fatalf("skip %d %v", st, r)
	}
	if on, c := remind(); on || c["skipped"] != "v0.9.1" {
		t.Fatalf("asked for a skipped release: %v", c)
	}
	// a newer release asks again
	f.gh.Publish(t, "v0.9.2", false, []byte("hub 0.9.2"))
	h.srv.hubUpdate.mu.Lock()
	h.srv.hubUpdate.at = time.Time{}
	h.srv.hubUpdate.mu.Unlock()
	if on, c := remind(); !on || c["latest"] != "v0.9.2" {
		t.Fatalf("a newer release than the skipped one asks: %v", c)
	}

	for _, bad := range []map[string]any{{"action": "skip"}, {"action": "skip", "version": "latest; rm"}, {"action": "never"}, {}} {
		if st, _ := h.do("POST", "/api/hub/update/remind", bad, nil); st != 400 {
			t.Errorf("%v: %d", bad, st)
		}
	}
}

// A dev build follows no release, so it never asks.
func TestHubUpdateReminderDevBuild(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	withFakeRelease(t, h, "dev")
	if st, c := h.do("GET", "/api/hub/update", nil, nil); st != 200 || c["remind"] != false {
		t.Fatalf("dev %d %v", st, c)
	}
}

// Check for updates (?fresh=1) asks GitHub now instead of the hour-old answer, though not twice
// within 30 seconds: a release out a minute after the last check is seen at once.
func TestHubUpdateCheckNow(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	f := withFakeRelease(t, h, "0.9.0")
	f.gh.Publish(t, "v0.9.0", false, []byte("hub 0.9.0"))
	if _, c := h.do("GET", "/api/hub/update", nil, nil); c["status"] != "current" {
		t.Fatalf("first check %v", c)
	}
	f.gh.Publish(t, "v0.9.1", false, []byte("hub 0.9.1"))
	// the cached answer, also when pressed right away (under 30 s)
	if _, c := h.do("GET", "/api/hub/update", nil, nil); c["status"] != "current" {
		t.Fatalf("cached %v", c)
	}
	asked := len(f.gh.Requests())
	if _, c := h.do("GET", "/api/hub/update?fresh=1", nil, nil); c["status"] != "current" || len(f.gh.Requests()) != asked {
		t.Fatalf("asked again within 30 s: %v", c)
	}
	// later than that, it asks
	h.srv.hubUpdate.mu.Lock()
	h.srv.hubUpdate.at = time.Now().Add(-time.Minute)
	h.srv.hubUpdate.mu.Unlock()
	if _, c := h.do("GET", "/api/hub/update?fresh=1", nil, nil); c["status"] != "available" || c["latest"] != "v0.9.1" || c["remind"] != true {
		t.Fatalf("check now %v", c)
	}
}
