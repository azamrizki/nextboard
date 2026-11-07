package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/store"
)

// fakeTUI imitates the interactive CLIs closely enough for the driver: a
// raw-mode loop reading keys, a trust dialog (Claude), an input prompt in
// the CLI's real shape, and the usage screen. Anything typed at the prompt
// other than the usage or exit command is logged as UNEXPECTED (the check
// must never send a model prompt). FAKE_MODE: ok | login | unknown | hang |
// update | die | apikey | pending (limits never arrive) | pending1 (they
// arrive for the second /status).
// fakeTUIPath is the fake CLI script (also used by the SSH test in
// internal/machine). It sources "$0.env" when present, since environment
// variables of the test do not reach a remote shell.
const fakeTUIPath = "testdata/fake-tui.sh"

type fakeHub struct {
	mu       sync.Mutex
	local    *machine.Local
	settings Settings
	events   []map[string]any
	prof     Profile
	command  map[string]string
	dirs     map[string][]string
}

func (f *fakeHub) Host(id string) (machine.Host, bool) {
	if id == machine.LocalID {
		return f.local, true
	}
	return nil, false
}
func (f *fakeHub) Settings() Settings { f.mu.Lock(); defer f.mu.Unlock(); return f.settings }
func (f *fakeHub) Publish(v any) {
	f.mu.Lock()
	f.events = append(f.events, v.(map[string]any))
	f.mu.Unlock()
}
func (f *fakeHub) Profile(m, a, p string) (Profile, error) {
	if p == "" {
		return Profile{}, nil
	}
	return f.prof, nil
}
func (f *fakeHub) Command(m, a string) string             { return f.command[a] }
func (f *fakeHub) ProfileDirs(string) map[string][]string { return f.dirs }
func (f *fakeHub) MachineIDs() []string                   { return []string{machine.LocalID} }

func (f *fakeHub) eventsOf(typ string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, e := range f.events {
		if e["type"] == typ {
			out = append(out, e)
		}
	}
	return out
}

type usageFixture struct {
	t    *testing.T
	dir  string
	log  string
	hub  *fakeHub
	jm   *jobs.Manager
	svc  *Service
	st   *store.Store
	root string
}

// setRoot points the usage folder root at a scratch folder for one test.
func setRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "nbu")
	if err != nil {
		t.Fatal(err)
	}
	old := Root
	Root = root
	t.Cleanup(func() { Root = old; os.RemoveAll(root) })
	return root
}

func newUsageFixture(t *testing.T, env ...string) *usageFixture {
	t.Helper()
	root := setRoot(t)
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-cli")
	script, err := os.ReadFile(fakeTUIPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fake, script, 0o755); err != nil {
		t.Fatal(err)
	}
	logf := filepath.Join(dir, "fake.log")
	for _, kv := range append([]string{"FAKE_LOG=" + logf}, env...) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	st, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	hub := &fakeHub{local: machine.NewLocal(machine.Config{}), settings: Settings{Background: true, ChimeEnabled: true, ChimePlay: "after100", ChimeSound: "bell"},
		command: map[string]string{"claude": fake, "codex": fake}}
	jm := jobs.NewManager(st, func(id string) (machine.Host, bool) { return hub.Host(id) })
	svc := New(st, jm, hub, nil)
	t.Cleanup(func() { svc.Close(); jm.Close() })
	return &usageFixture{t: t, dir: dir, log: logf, hub: hub, jm: jm, svc: svc, st: st, root: root}
}

func (f *usageFixture) logText() string {
	b, _ := os.ReadFile(f.log)
	return string(b)
}

// check runs a user check and waits for the job.
func (f *usageFixture) check(agent string) Report {
	f.t.Helper()
	zero := 0
	r, j, err := f.svc.Check(CheckRequest{Agent: agent, MaxAgeSec: &zero, Trigger: "user"})
	if err != nil {
		f.t.Fatal(err)
	}
	if r != nil {
		f.t.Fatalf("cached report %+v", r)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		v, _ := f.jm.Get(j.ID)
		if !v.Active() {
			if v.State != jobs.StateDone {
				f.t.Fatalf("job %s %s %s", v.State, v.Code, v.Error)
			}
			var rep Report
			if err := json.Unmarshal(v.Result, &rep); err != nil {
				f.t.Fatal(err)
			}
			return rep
		}
		if time.Now().After(deadline) {
			f.t.Fatal("check did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *usageFixture) assertFolderGone() {
	f.t.Helper()
	ents, _ := os.ReadDir(f.root)
	if len(ents) != 0 {
		f.t.Fatalf("usage folder left behind: %v", ents)
	}
	if p := f.svc.Pending(); len(p) != 0 {
		f.t.Fatalf("pending cleanup: %v", p)
	}
}

func TestCheckClaudeTrustAndUsage(t *testing.T) {
	f := newUsageFixture(t, "FAKE_TRUST=1", "FAKE_KIND=claude")
	r := f.check("claude")
	if r.State != "ok" || !r.Parsed || len(r.Windows) != 2 {
		t.Fatalf("report %+v\nlog %s", r, f.logText())
	}
	if r.Windows[0].ID != "session" || r.Windows[0].UsedPct != 45 || r.Windows[0].ResetsAt == nil ||
		r.Windows[1].ID != "week" || r.Windows[1].UsedPct != 100 || !strings.HasPrefix(r.Windows[1].ResetsText, "Resets Nov 3") {
		t.Fatalf("windows %+v", r.Windows)
	}
	lg := f.logText()
	uid := os.Getuid()
	wantDir := filepath.Join(f.root, "nb-usage-"+strconv.Itoa(uid))
	if !strings.Contains(lg, "start cwd="+wantDir) || !strings.Contains(lg, "noupdate=1") || !strings.Contains(lg, "trusted") ||
		!strings.Contains(lg, "cmd /usage") || !strings.Contains(lg, "exit /exit") || strings.Contains(lg, "UNEXPECTED") {
		t.Fatalf("log:\n%s", lg)
	}
	f.assertFolderGone()
	if ev := f.hub.eventsOf("usage"); len(ev) != 1 {
		t.Fatalf("usage events %v", ev)
	}
	// Cached within maxAgeSec: no second job.
	age := 180
	cached, j, err := f.svc.Check(CheckRequest{Agent: "claude", MaxAgeSec: &age})
	if err != nil || j != nil || cached == nil || cached.CheckedAt != r.CheckedAt {
		t.Fatalf("cache %v %v %v", cached, j, err)
	}
	// reports.json survives a restart.
	svc2 := New(f.st, f.jm, f.hub, nil)
	if got := svc2.Reports("", "claude", "", false); len(got) != 1 || got[0].Windows[1].UsedPct != 100 {
		t.Fatalf("reloaded %+v", got)
	}
}

func TestCheckCodexStatusLeft(t *testing.T) {
	f := newUsageFixture(t, "FAKE_KIND=codex")
	r := f.check("codex")
	if r.State != "ok" || len(r.Windows) != 2 || r.Windows[0].ID != "5h" || r.Windows[0].UsedPct != 42 ||
		r.Windows[1].ID != "weekly" || r.Windows[1].UsedPct != 10 || r.Windows[1].ResetsAt == nil {
		t.Fatalf("report %+v\n%s", r, f.logText())
	}
	if len(r.Facts) < 2 || r.Facts[0].Label != "Model" {
		t.Fatalf("facts %+v", r.Facts)
	}
	lg := f.logText()
	if !strings.Contains(lg, "check_for_update_on_startup=false") || !strings.Contains(lg, `trust_level="trusted"`) ||
		!strings.Contains(lg, "cmd /status") || !strings.Contains(lg, "exit /quit") || strings.Contains(lg, "UNEXPECTED") {
		t.Fatalf("log:\n%s", lg)
	}
	f.assertFolderGone()
}

// Codex prints "refresh requested; run /status again" until its limits arrive.
func TestCheckCodexRetriesWhileLimitsArePending(t *testing.T) {
	old := pendingGap
	pendingGap = 50 * time.Millisecond
	t.Cleanup(func() { pendingGap = old })

	f := newUsageFixture(t, "FAKE_KIND=codex", "FAKE_MODE=pending1")
	r := f.check("codex")
	if r.State != "ok" || len(r.Windows) != 2 {
		t.Fatalf("report %+v\n%s", r, f.logText())
	}
	lg := f.logText()
	if strings.Count(lg, "cmd /status") != 2 || strings.Contains(lg, "UNEXPECTED") {
		t.Fatalf("log:\n%s", lg)
	}
	f.assertFolderGone()

	// They never arrive: a bounded number of tries, then a report that says so.
	t.Setenv("FAKE_MODE", "pending")
	before := strings.Count(f.logText(), "cmd /status")
	r = f.check("codex")
	if r.State != "error" || r.Parsed || !strings.Contains(r.Error, "no limit data from the server yet") || len(r.Facts) != 2 {
		t.Fatalf("report %+v", r)
	}
	if n := strings.Count(f.logText(), "cmd /status") - before; n != pendingRetries+1 {
		t.Fatalf("%d attempts\n%s", n, f.logText())
	}
	f.assertFolderGone()
}

// An API key has no plan limits: not an error, and "run codex login" on the
// status screen does not mean signed out.
func TestCheckCodexLoginWithoutLimits(t *testing.T) {
	f := newUsageFixture(t, "FAKE_KIND=codex", "FAKE_MODE=apikey")
	r := f.check("codex")
	if r.State != "unsupported" || r.Error == "" || len(r.Windows) != 0 || len(r.Facts) != 2 || r.Facts[1].Label != "Account" {
		t.Fatalf("report %+v", r)
	}
	if strings.Count(f.logText(), "cmd /status") != 1 {
		t.Fatalf("log:\n%s", f.logText())
	}
	f.assertFolderGone()
}

// A CLI that quits at startup: the report carries its reason.
func TestCheckSaysWhyTheCLIQuit(t *testing.T) {
	f := newUsageFixture(t, "FAKE_KIND=codex", "FAKE_MODE=die")
	r := f.check("codex")
	if r.State != "error" || !strings.Contains(r.Error, "account/read failed") {
		t.Fatalf("report %+v", r)
	}
	f.assertFolderGone()
}

func TestCheckUpdateSkippedNeverInstalled(t *testing.T) {
	f := newUsageFixture(t, "FAKE_KIND=codex", "FAKE_MODE=update")
	r := f.check("codex")
	lg := f.logText()
	if r.State != "ok" || !strings.Contains(lg, "update-skipped") || strings.Contains(lg, "UNEXPECTED") {
		t.Fatalf("report %+v\n%s", r, lg)
	}
	f.assertFolderGone()
}

func TestCheckLoginAndUnknownScreen(t *testing.T) {
	f := newUsageFixture(t, "FAKE_KIND=claude", "FAKE_MODE=login")
	if r := f.check("claude"); r.State != "notLoggedIn" || len(r.Raw) == 0 {
		t.Fatalf("login %+v", r)
	}
	f.assertFolderGone()
	t.Setenv("FAKE_MODE", "unknown")
	r := f.check("claude")
	if r.Parsed || r.State != "error" || len(r.Raw) == 0 || !strings.Contains(strings.Join(r.Raw, "\n"), "completely different") {
		t.Fatalf("unknown %+v", r)
	}
	f.assertFolderGone()
}

func TestCheckHangTimesOutAndCleansUp(t *testing.T) {
	old := readyTimeout
	readyTimeout = 2 * time.Second
	t.Cleanup(func() { readyTimeout = old })
	f := newUsageFixture(t, "FAKE_KIND=claude", "FAKE_MODE=hang")
	r := f.check("claude")
	if r.State != "needsInteractive" {
		t.Fatalf("hang %+v", r)
	}
	f.assertFolderGone()
}

func TestCheckMissingCLIAndUnsupported(t *testing.T) {
	f := newUsageFixture(t)
	f.hub.command["claude"] = "/nonexistent/nb-no-such-cli"
	if r := f.check("claude"); r.State != "error" || !strings.Contains(r.Error, "not found") {
		t.Fatalf("missing %+v", r)
	}
	f.assertFolderGone()
	r, j, err := f.svc.Check(CheckRequest{Agent: "opencode"})
	if err != nil || j != nil || r == nil || r.State != "unsupported" {
		t.Fatalf("unsupported %v %v %v", r, j, err)
	}
	zero := 0
	f.hub.settings.Background = false
	if _, _, err := f.svc.Check(CheckRequest{Agent: "claude", MaxAgeSec: &zero, Trigger: "background"}); err != ErrBackground {
		t.Fatalf("background off: %v", err)
	}
	// Switched off per CLI (settings.usage.agents): refused for any trigger.
	f.hub.mu.Lock()
	f.hub.settings.Disabled = map[string]bool{"claude": true}
	f.hub.mu.Unlock()
	if _, _, err := f.svc.Check(CheckRequest{Agent: "claude", MaxAgeSec: &zero, Trigger: "user"}); err != ErrDisabled {
		t.Fatalf("disabled agent: %v", err)
	}
	f.hub.mu.Lock()
	f.hub.settings.Disabled = nil
	f.hub.mu.Unlock()
}

func TestCheckCancelRemovesFolder(t *testing.T) {
	f := newUsageFixture(t, "FAKE_KIND=claude", "FAKE_MODE=hang")
	zero := 0
	_, j, err := f.svc.Check(CheckRequest{Agent: "claude", MaxAgeSec: &zero, Trigger: "user"})
	if err != nil {
		t.Fatal(err)
	}
	// wait until the folder exists, then cancel
	for i := 0; i < 100; i++ {
		if ents, _ := os.ReadDir(f.root); len(ents) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.jm.Cancel(j.ID)
	for i := 0; i < 200; i++ {
		if v, _ := f.jm.Get(j.ID); !v.Active() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if v, _ := f.jm.Get(j.ID); v.State != jobs.StateCancelled {
		t.Fatalf("cancel %+v", v)
	}
	f.assertFolderGone()
}
