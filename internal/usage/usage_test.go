package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/store"
)

func fixture(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// TestParseRealScreens parses the screens captured from the real CLIs
// (Claude Code 2.1.312 /usage, Codex 0.165.0 /status).
func TestParseRealScreens(t *testing.T) {
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 10, 28, 19, 52, 0, 0, jkt)
	p := ParseScreen("claude", fixture(t, "claude-2.1.312-usage.txt"), now, time.UTC)
	if len(p.Windows) != 3 {
		t.Fatalf("claude windows %+v", p.Windows)
	}
	want := []struct {
		id, label string
		pct       int
		at        string
	}{
		{"session", "Current session", 16, "2026-10-28T17:00:00Z"},
		{"week", "Current week (all models)", 36, "2026-11-01T13:00:00Z"},
		{"week-fable", "Current week (Fable)", 4, "2026-11-01T13:00:00Z"},
	}
	for i, w := range want {
		g := p.Windows[i]
		if g.ID != w.id || g.Label != w.label || g.UsedPct != w.pct || g.ResetsAt == nil || g.ResetsAt.Format(time.RFC3339) != w.at {
			t.Fatalf("claude window %d: %+v (resets %v)", i, g, g.ResetsAt)
		}
	}
	if p.Windows[0].ResetsText != "Resets 12am (Asia/Jakarta)" {
		t.Fatalf("resets text %q", p.Windows[0].ResetsText)
	}
	// The usage-contribution list ("91%  Give …") is not a window.
	for _, w := range p.Windows {
		if w.UsedPct > 90 {
			t.Fatalf("contribution line parsed as a window: %+v", w)
		}
	}

	c := ParseScreen("codex", fixture(t, "codex-0.165.0-status.txt"), now, jkt)
	if len(c.Windows) != 2 || c.Windows[0].ID != "5h" || c.Windows[0].UsedPct != 0 || c.Windows[1].ID != "weekly" {
		t.Fatalf("codex windows %+v", c.Windows)
	}
	if c.Windows[0].ResetsAt == nil || c.Windows[0].ResetsAt.In(jkt).Format("2006-01-02 15:04") != "2026-10-29 00:52" ||
		c.Windows[1].ResetsAt == nil || c.Windows[1].ResetsAt.In(jkt).Format("2006-01-02 15:04") != "2026-11-04 13:53" {
		t.Fatalf("codex resets %v %v", c.Windows[0].ResetsAt, c.Windows[1].ResetsAt)
	}
	if len(c.Facts) != 2 || c.Facts[0].Label != "Model" || c.Facts[1].Label != "Account" {
		t.Fatalf("codex facts %+v", c.Facts)
	}
}

func TestParseVariants(t *testing.T) {
	now := time.Date(2026, 10, 28, 20, 0, 0, 0, time.UTC)
	p := ParseScreen("codex", []string{"│  5h limit: [███░░] 42% used (resets in 2h 5m) │", "│ Weekly limit: [█░░] 12% used │"}, now, time.UTC)
	if len(p.Windows) != 2 || p.Windows[0].UsedPct != 42 || p.Windows[0].ResetsAt == nil ||
		!p.Windows[0].ResetsAt.Equal(now.Add(2*time.Hour+5*time.Minute)) || p.Windows[1].ResetsAt != nil {
		t.Fatalf("%+v", p.Windows)
	}
	if u := ParseScreen("claude", []string{"/usage is only available for subscription plans."}, now, time.UTC); u.Unsupported == "" {
		t.Fatal("api-key login not reported as unsupported")
	}
	// Codex 0.165 limit lines: none for this login, on their way, an earlier
	// /status above the answer, stale.
	if u := ParseScreen("codex", []string{"Account: API key configured (run codex login to use ChatGPT)", "Limits:   not available for this account"}, now, time.UTC); u.Unsupported == "" || u.Pending || len(u.Facts) != 1 {
		t.Fatalf("no limits %+v", u)
	}
	for _, l := range []string{"Limits:   refresh requested; run /status again", "Limits:   data not available yet"} {
		if u := ParseScreen("codex", []string{l}, now, time.UTC); !u.Pending || u.Unsupported != "" {
			t.Fatalf("%q: %+v", l, u)
		}
	}
	if u := ParseScreen("codex", []string{"Limits: refresh requested; run /status again", "5h limit: [███] 58% left (resets 18:30)"}, now, time.UTC); u.Pending || len(u.Windows) != 1 {
		t.Fatalf("answer below an earlier status %+v", u)
	}
	if u := ParseScreen("codex", []string{"5h limit: [███] 58% left (resets 18:30)", "Warning: limits may be stale - start new turn to refresh."}, now, time.UTC); len(u.Windows) != 1 || len(u.Facts) != 1 || u.Facts[0].Label != "Warning" {
		t.Fatalf("stale %+v", u)
	}
	if u := ParseScreen("claude", []string{"hello world", "nothing here 42"}, now, time.UTC); len(u.Windows) != 0 {
		t.Fatalf("garbage parsed %+v", u.Windows)
	}
	// Unknown reset format → resetsAt null, text kept.
	q := ParseScreen("claude", []string{" Current session", " ████ 50% used", " Resets whenever the moon is full"}, now, time.UTC)
	if len(q.Windows) != 1 || q.Windows[0].ResetsAt != nil || q.Windows[0].ResetsText == "" {
		t.Fatalf("%+v", q.Windows)
	}
	// "6pm" after 6pm local → tomorrow.
	if r := parseReset("6pm", time.Date(2026, 10, 28, 19, 0, 0, 0, time.UTC), time.UTC); r == nil || r.Day() != 29 || r.Hour() != 18 {
		t.Fatalf("6pm %v", r)
	}
}

// TestTempDirLifecycle: the fixed folder is created fresh (leftovers of a
// crash removed first), recorded, removed after use, and a hub that died
// mid-check removes it when the machine connects again. Invalid recorded
// paths are never passed to rm; a symlink at the fixed path is refused.
func TestTempDirLifecycle(t *testing.T) {
	root := setRoot(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	local := machine.NewLocal(machine.Config{})
	td := &tempDirs{st: st}
	ctx := context.Background()
	want := fmt.Sprintf("%s/nb-usage-%d", root, os.Getuid())
	// A leftover of a crashed check (with content) is replaced.
	if err := os.MkdirAll(filepath.Join(want, "junk"), 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := td.make(ctx, local)
	if err != nil || d != want {
		t.Fatalf("make %q %v", d, err)
	}
	if ents, _ := os.ReadDir(d); len(ents) != 0 {
		t.Fatalf("folder not fresh: %v", ents)
	}
	if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if p := td.Pending(); len(p) != 1 || p[0].Path != want {
		t.Fatalf("pending %+v", p)
	}
	// "Hub killed mid-check": a new service on the same data dir cleans up
	// on connect.
	_ = os.WriteFile(filepath.Join(d, "state.json"), []byte("{}"), 0o600)
	hub := &fakeHub{local: local}
	jm := jobs.NewManager(st, func(id string) (machine.Host, bool) { return hub.Host(id) })
	defer jm.Close()
	svc := New(st, jm, hub, nil)
	defer svc.Close()
	svc.MachineConnected(machine.LocalID)
	for i := 0; i < 100 && len(svc.Pending()) > 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) || len(svc.Pending()) != 0 {
		t.Fatalf("not cleaned after restart: %v %v", err, svc.Pending())
	}
	// Recorded paths outside the pattern are dropped, never removed.
	victim := filepath.Join(t.TempDir(), "keep")
	_ = os.MkdirAll(victim, 0o700)
	for _, bad := range []string{victim, root + "/nb-usage-1/../../etc", root + "/nb-usage-x", "/tmp/nb-usage.abc123", root + "/nb-usage-"} {
		td.record(machine.LocalID, bad)
	}
	td.cleanup(ctx, local)
	if _, err := os.Stat(victim); err != nil || len(td.Pending()) != 0 {
		t.Fatalf("bad paths: %v %+v", err, td.Pending())
	}
	// A symlink at the fixed path is refused (not followed, not removed).
	target := t.TempDir()
	_ = os.WriteFile(filepath.Join(target, "precious"), []byte("x"), 0o600)
	if err := os.Symlink(target, want); err != nil {
		t.Fatal(err)
	}
	if _, err := td.make(ctx, local); err != ErrUnsafeTemp {
		t.Fatalf("symlink make: %v", err)
	}
	td.record(machine.LocalID, want)
	if err := td.remove(ctx, local, want); err != ErrUnsafeTemp {
		t.Fatalf("symlink remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "precious")); err != nil {
		t.Fatal("symlink target touched")
	}
	os.Remove(want)
	// The shell scripts validate the path on their own too.
	if s := removeScript(root, "/etc"); !strings.Contains(s, "exit 125") || !strings.Contains(s, `case "$d" in`) {
		t.Fatal(s)
	}
}

// fakeClock fires timers when advanced.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func (t *fakeTimer) Stop() bool { s := !t.stopped && !t.fired; t.stopped = true; return s }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.at.After(c.now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

func TestResetChime(t *testing.T) {
	clk := &fakeClock{now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	hub := &fakeHub{settings: Settings{ChimeEnabled: true, ChimePlay: "after100", ChimeSound: "chime"}}
	c := newChime(clk, hub.Settings, hub.Publish)
	at := func(m int) *time.Time { t := clk.now.Add(time.Duration(m) * time.Minute); return &t }
	r := Report{MachineID: "local", Agent: "claude", State: "ok", Windows: []Window{
		{ID: "session", UsedPct: 100, ResetsAt: at(10)},
		{ID: "week", UsedPct: 40, ResetsAt: at(20)},
		{ID: "none", UsedPct: 100},
	}}
	c.arm(r)
	clk.advance(10 * time.Minute) // not yet: fires 30 s after the reset
	if len(hub.eventsOf("usageReset")) != 0 {
		t.Fatal("chime too early")
	}
	clk.advance(30 * time.Second)
	ev := hub.eventsOf("usageReset")
	if len(ev) != 1 || ev[0]["window"] != "session" || ev[0]["agent"] != "claude" || ev[0]["sound"] != "chime" {
		t.Fatalf("after100 %v", ev)
	}
	clk.advance(15 * time.Minute) // week was at 40 %: no chime with after100
	if len(hub.eventsOf("usageReset")) != 1 {
		t.Fatal("after100 chimed below 100 %")
	}
	// Re-arming the same report does not chime twice.
	c.arm(r)
	clk.advance(time.Minute)
	if len(hub.eventsOf("usageReset")) != 1 {
		t.Fatal("double chime")
	}
	// play "always" chimes for any window; disabled never.
	hub.settings.ChimePlay = "always"
	r2 := Report{MachineID: "local", Agent: "codex", State: "ok", Windows: []Window{{ID: "5h", UsedPct: 3, ResetsAt: at(5)}}}
	c.arm(r2)
	clk.advance(6 * time.Minute)
	if ev := hub.eventsOf("usageReset"); len(ev) != 2 || ev[1]["agent"] != "codex" {
		t.Fatalf("always %v", ev)
	}
	hub.settings.ChimeEnabled = false
	r3 := Report{MachineID: "local", Agent: "codex", ProfileID: "pf_x", State: "ok", Windows: []Window{{ID: "5h", UsedPct: 100, ResetsAt: at(1)}}}
	c.arm(r3)
	clk.advance(2 * time.Minute)
	if len(hub.eventsOf("usageReset")) != 2 {
		t.Fatal("disabled chime fired")
	}
	// A newer report replaces the timers of its key.
	hub.settings.ChimeEnabled = true
	r4 := Report{MachineID: "m", Agent: "claude", State: "ok", Windows: []Window{{ID: "session", UsedPct: 100, ResetsAt: at(10)}}}
	c.arm(r4)
	r4.Windows[0].ResetsAt = at(60)
	c.arm(r4)
	clk.advance(11 * time.Minute)
	if len(hub.eventsOf("usageReset")) != 2 {
		t.Fatal("stale timer fired")
	}
	clk.advance(50 * time.Minute)
	if len(hub.eventsOf("usageReset")) != 3 {
		t.Fatal("new timer did not fire")
	}
	// At start, a reset missed less than 10 min ago still chimes; older not.
	old := Report{MachineID: "o", Agent: "claude", State: "ok", Windows: []Window{{ID: "a", UsedPct: 100, ResetsAt: at(-5)}, {ID: "b", UsedPct: 100, ResetsAt: at(-60)}}}
	c.arm(old)
	clk.advance(time.Second)
	if ev := hub.eventsOf("usageReset"); len(ev) != 4 || ev[3]["window"] != "a" {
		t.Fatalf("late %v", ev)
	}
}

// ---- token history ----

func claudeLine(id, ts string, in, out int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":"claude-opus-5-5","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`, ts, id, in, out)
}

// TestTokenStats: sessions, favourite model and longest session per CLI, and
// the second read that catches up an index written before they existed, without
// counting any token twice.
func TestTokenStats(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hub := &fakeHub{local: machine.NewLocal(machine.Config{}), dirs: map[string][]string{}}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	ts := func(h int) string { return base.Add(time.Duration(h) * time.Hour).Format(time.RFC3339Nano) }
	dir := filepath.Join(home, ".claude", "projects", "-work-app")
	_ = os.MkdirAll(dir, 0o700)
	line := func(id, model string, h, in int) string {
		return strings.Replace(claudeLine(id, ts(h), in, 0), "claude-opus-5-5", model, 1) + "\n"
	}
	// s1: the long one, mostly Fable. s2: the most tokens per turn, but less in all. A transcript
	// without a turn is no session.
	_ = os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(line("a", "claude-fable-5-1", 0, 500)+line("a", "claude-fable-5-1", 0, 500)+line("b", "claude-fable-5-1", 30, 500)), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "s2.jsonl"), []byte(line("c", "claude-opus-5-5", 1, 600)), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "empty.jsonl"), []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"), 0o600)
	xdir := filepath.Join(home, ".codex", "sessions", "2026", "10", "28")
	_ = os.MkdirAll(xdir, 0o700)
	rollout := fmt.Sprintf(`{"timestamp":%q,"type":"turn_context","payload":{"model":"gpt-6-astra"}}
{"timestamp":%q,"type":"token_usage_record","response_id":"r1","usage":{"input_tokens":40,"cached_input_tokens":0,"output_tokens":2}}
`, ts(0), ts(2))
	_ = os.WriteFile(filepath.Join(xdir, "rollout-2026-10-28T10-00-00-abc.jsonl"), []byte(rollout), 0o600)

	check := func(x *tokenIndex, what string) (int64, int64) {
		t.Helper()
		c, err := x.chart(context.Background(), 30, []string{machine.LocalID}, "")
		if err != nil {
			t.Fatal(err)
		}
		by := map[string]AgentTotals{}
		for _, a := range c.ByAgent {
			by[a.Agent] = a
		}
		cl, cx := by["claude"], by["codex"]
		if cl.Sessions != 2 || cl.FavoriteModel != "claude-fable-5-1" || cl.LongestSessionSec != 30*3600 {
			t.Fatalf("%s: claude %+v", what, cl)
		}
		if cx.Sessions != 1 || cx.FavoriteModel != "gpt-6-astra" {
			t.Fatalf("%s: codex %+v", what, cx)
		}
		return cl.AllTime.Total, cx.AllTime.Total
	}
	x := newTokenIndex(st, hub)
	cl, cx := check(x, "first scan")

	// Nothing new: nothing counted again, and that survives a restart.
	if a, b := check(x, "second scan"); a != cl || b != cx {
		t.Fatalf("second scan %d %d, want %d %d", a, b, cl, cx)
	}
	x = newTokenIndex(st, hub)
	check(x, "reloaded")

	// An index from before the statistics: same tokens, statistics come back.
	ix := x.load(machine.LocalID)
	ix.Stats = map[string]*agentStats{}
	for _, fs := range ix.Files {
		fs.StatOffset, fs.Model, fs.First, fs.Last = 0, "", 0, 0
	}
	if err := st.Save(indexFile(machine.LocalID), ix); err != nil {
		t.Fatal(err)
	}
	old := newTokenIndex(st, hub)
	if a, b := check(old, "backfilled"); a != cl || b != cx {
		t.Fatalf("backfill changed tokens: %d %d, want %d %d", a, b, cl, cx)
	}
	for p, fs := range old.load(machine.LocalID).Files {
		if fs.StatOffset != fs.Offset || fs.Backfill != nil {
			t.Fatalf("%s not caught up: %+v", p, fs)
		}
	}

	// New turns after the backfill land in both.
	fh, _ := os.OpenFile(filepath.Join(dir, "s2.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(line("d", "claude-opus-5-5", 40, 1000))
	fh.Close()
	c, err := old.chart(context.Background(), 30, []string{machine.LocalID}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if a := c.ByAgent[0]; a.FavoriteModel != "claude-opus-5-5" || a.LongestSessionSec != 39*3600 || a.Sessions != 2 || a.AllTime.Total != cl+1015 {
		t.Fatalf("after new turns %+v", a)
	}
}

func TestTokenIndexIncremental(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := machine.NewLocal(machine.Config{})
	prof := t.TempDir()
	hub := &fakeHub{local: local, dirs: map[string][]string{"claude": {prof}}}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	x := newTokenIndex(st, hub)
	today := time.Now().In(time.Local)
	ts := func(daysAgo int) string {
		return time.Date(today.Year(), today.Month(), today.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -daysAgo).UTC().Format(time.RFC3339Nano)
	}
	cdir := filepath.Join(home, ".claude", "projects", "-work-app")
	_ = os.MkdirAll(cdir, 0o700)
	f := filepath.Join(cdir, "s1.jsonl")
	// one message spans three lines (same id) → counted once; synthetic skipped
	body := claudeLine("msg_1", ts(0), 100, 20) + "\n" + claudeLine("msg_1", ts(0), 100, 20) + "\n" +
		`{"type":"user","message":{"role":"user","content":"hi"}}` + "\n" + claudeLine("msg_1", ts(0), 100, 20) + "\n" +
		strings.Replace(claudeLine("msg_s", ts(0), 999, 999), "claude-opus-5-5", "<synthetic>", 1) + "\n" +
		claudeLine("msg_2", ts(3), 1, 2) + "\n" +
		claudeLine("msg_3", ts(1), 7, 3)[:40] // incomplete last line
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// a profile folder and a codex rollout with records + counts
	pdir := filepath.Join(prof, "projects", "-x")
	_ = os.MkdirAll(pdir, 0o700)
	_ = os.WriteFile(filepath.Join(pdir, "p.jsonl"), []byte(claudeLine("msg_p", ts(0), 1000, 0)+"\n"), 0o600)
	xdir := filepath.Join(home, ".codex", "sessions", "2026", "10", "28")
	_ = os.MkdirAll(xdir, 0o700)
	rollout := fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":60},"last_token_usage":{"input_tokens":50,"cached_input_tokens":10,"output_tokens":10,"total_tokens":60}}}}
{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":60},"last_token_usage":{"input_tokens":50,"cached_input_tokens":10,"output_tokens":10,"total_tokens":60}}}}
`, ts(0), ts(0))
	_ = os.WriteFile(filepath.Join(xdir, "rollout-2026-10-28T10-00-00-abc.jsonl"), []byte(rollout), 0o600)

	ctx := context.Background()
	c, err := x.chart(ctx, 30, []string{machine.LocalID}, "")
	if err != nil {
		t.Fatal(err)
	}
	byAgent := map[string]AgentTotals{}
	for _, a := range c.ByAgent {
		byAgent[a.Agent] = a
	}
	// claude: msg_1 (100+20+10+5) + msg_2 (1+2+15) + profile msg_p (1000+15)
	if got := byAgent["claude"].AllTime.Total; got != 135+18+1015 {
		t.Fatalf("claude total %d (%+v)", got, c)
	}
	// codex: one turn (repeat of the same running total skipped): 40+10+10
	if got := byAgent["codex"].AllTime.Total; got != 60 {
		t.Fatalf("codex total %d", got)
	}
	if c.Top != "claude" || c.BusiestDay == nil || c.BusiestDay.Date != today.Format("2006-01-02") || c.Partial {
		t.Fatalf("chart %+v", c)
	}
	// Second scan: nothing new → same totals, no re-read (offset at EOF of
	// complete lines).
	ix := x.load(machine.LocalID)
	off := ix.Files[f].Offset
	if off != int64(len(body)-40) {
		t.Fatalf("offset %d want %d", off, len(body)-40)
	}
	c2, _ := x.chart(ctx, 30, []string{machine.LocalID}, "claude")
	if c2.ByAgent[0].AllTime.Total != byAgent["claude"].AllTime.Total || len(c2.ByAgent) != 1 {
		t.Fatalf("rescan changed totals %+v", c2.ByAgent)
	}
	// Appending completes the cut line and adds a repeat of msg_2 (dedupe).
	fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(claudeLine("msg_3", ts(1), 7, 3)[40:] + "\n" + claudeLine("msg_2", ts(3), 1, 2) + "\n")
	fh.Close()
	c3, _ := x.chart(ctx, 30, []string{machine.LocalID}, "claude")
	if got := c3.ByAgent[0].AllTime.Total; got != byAgent["claude"].AllTime.Total+25 {
		t.Fatalf("incremental %d", got)
	}
	// The index persists and only new bytes are read after a restart.
	x2 := newTokenIndex(st, hub)
	c4, _ := x2.chart(ctx, 30, []string{machine.LocalID}, "claude")
	if c4.ByAgent[0].AllTime.Total != c3.ByAgent[0].AllTime.Total {
		t.Fatalf("reloaded %d", c4.ByAgent[0].AllTime.Total)
	}
	b, _ := json.Marshal(c4)
	if !strings.Contains(string(b), `"series"`) {
		t.Fatal(string(b))
	}
	// An exhausted budget reports partial.
	x3 := newTokenIndex(st, hub)
	x3.budget = time.Nanosecond
	time.Sleep(time.Millisecond)
	c5, _ := x3.chart(ctx, 30, []string{machine.LocalID}, "")
	if !c5.Partial {
		t.Fatal("partial not reported")
	}
}
