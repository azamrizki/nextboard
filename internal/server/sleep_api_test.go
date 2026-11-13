package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/session"
)

// asleep reports whether a console is asleep, by its API view.
func asleep(in map[string]any) bool {
	return in["status"] == "asleep" && in["agentState"] == "asleep" && in["asleep"] == true
}

// quiet: ready, and nothing printed for a moment (keys echo).
func quiet(in map[string]any) bool { return in["agentState"] == "ready" && in["busy"] == false }

// fakeRunning reports whether a fake agent of this folder runs.
func fakeRunning(dir string) bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err == nil && strings.Contains(string(b), filepath.Join(dir, "fakeorch")) {
			return true
		}
	}
	return false
}

// TestSleepWake: an idle agent goes to sleep (its process ends, its conversation and what was
// typed at its prompt stay), and wakes when someone types into it, opens its terminal or sends
// it text; the draft is typed back first (ROADMAP #50).
func TestSleepWake(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	h := newTestHub(t)
	_, wid, dir := orchSetup(t, h)
	t.Setenv("FAKE_ECHO", "1")
	id := h.spawnAgent(wid)["id"].(string)
	h.waitInfo(id, "ready", state("ready"))
	h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "first message"}, nil)
	waitTrue(t, "first message", 10*time.Second, func() bool { return strings.Contains(received(dir), "first message\n") })
	h.waitInfo(id, "ready again", state("ready"))
	sess, _ := h.srv.sessions.Get(id)

	// a recent or watched console stays awake when the hub looks for idle ones
	if n := h.srv.sessions.SleepIdle(time.Hour, time.Now()); n != 0 {
		t.Fatal("slept while active")
	}
	_, sub := sess.Subscribe()
	if n := h.srv.sessions.SleepIdle(0, time.Now()); n != 0 {
		t.Fatal("slept while watched")
	}
	sess.Unsubscribe(sub)

	// pinned awake, and keys it cannot follow at its prompt
	if st, in := h.do("PATCH", "/api/sessions/"+id, map[string]any{"keepAwake": true}, nil); st != 200 || in["keepAwake"] != true {
		t.Fatalf("keep awake %d %v", st, in)
	}
	if st, b := h.do("POST", "/api/sessions/"+id+"/sleep", nil, nil); st != 409 || b["code"] != "cannot_sleep" || b["reason"] != session.SleepKeepAwake {
		t.Fatalf("pinned %d %v", st, b)
	}
	h.do("PATCH", "/api/sessions/"+id, map[string]any{"keepAwake": false}, nil)
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "x\x1b[D"}, nil)
	h.waitInfo(id, "quiet", quiet)
	if st, b := h.do("POST", "/api/sessions/"+id+"/sleep", nil, nil); st != 409 || b["reason"] != session.SleepDraftUnsure {
		t.Fatalf("unsure draft %d %v", st, b)
	}
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "\r"}, nil) // submit it
	waitTrue(t, "submitted", 10*time.Second, func() bool {
		return strings.Contains(received(dir), "x\x1b[D\n") || strings.Count(received(dir), "\n") >= 2
	})
	h.waitInfo(id, "ready after it", state("ready"))

	// typed and not sent: it sleeps with the draft
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "half typed"}, nil)
	waitTrue(t, "draft on screen", 5*time.Second, func() bool { return strings.Contains(strings.Join(sess.Screen().Lines, ""), "half typed") })
	h.waitInfo(id, "quiet with a draft", quiet)
	if n := h.srv.sessions.SleepIdle(0, time.Now()); n != 1 {
		_, in := h.do("GET", "/api/sessions/"+id, nil, nil)
		t.Fatalf("idle sleep: %d went; %v", n, in)
	}
	in := h.waitInfo(id, "asleep", asleep)
	if in["asleepAt"] == nil || in["unsentDraft"] != nil {
		t.Fatalf("asleep %v", in)
	}
	waitTrue(t, "process gone", 5*time.Second, func() bool { return !fakeRunning(dir) })
	if strings.Contains(received(dir), "half typed") {
		t.Fatal("the draft was sent")
	}
	if n, _ := h.srv.sessions.Awake(); n != 0 {
		t.Fatalf("awake %d", n)
	}

	// typing wakes it: the draft comes back first, then the new keys, then Return sends both
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": " and more"}, nil)
	h.waitInfo(id, "woken", state("ready"))
	waitTrue(t, "draft and keys on screen", 15*time.Second, func() bool {
		return strings.Contains(strings.Join(sess.Screen().Lines, ""), "half typed") && strings.Contains(strings.Join(sess.Screen().Lines, ""), "and more")
	})
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "\r"}, nil)
	waitTrue(t, "sent after waking", 10*time.Second, func() bool { return strings.Contains(received(dir), "half typed and more\n") })
	if _, in := h.do("GET", "/api/sessions/"+id, nil, nil); in["asleep"] == true || in["unsentDraft"] != nil {
		t.Fatalf("woken %v", in)
	}
	h.waitInfo(id, "quiet", quiet)

	// opening its terminal wakes it
	if st, in := h.do("POST", "/api/sessions/"+id+"/sleep", nil, nil); st != 200 {
		t.Fatalf("sleep now %d %v", st, in)
	}
	h.waitInfo(id, "asleep again", asleep)
	if st, b := h.do("POST", "/api/sessions/"+id+"/sleep", nil, nil); st != 409 || b["reason"] != session.SleepNotRunning {
		t.Fatalf("asleep twice %d %v", st, b)
	}
	_, sub = sess.Subscribe()
	h.waitInfo(id, "woken by its terminal", quiet)
	sess.Unsubscribe(sub)

	// text for it wakes it, and goes in
	h.do("POST", "/api/sessions/"+id+"/sleep", nil, nil)
	h.waitInfo(id, "asleep a third time", asleep)
	if st, b := h.do("POST", "/api/sessions/"+id+"/wake", nil, nil); st != 200 {
		t.Fatalf("wake %d %v", st, b)
	}
	h.waitInfo(id, "woken by wake", quiet)
	if st, b := h.do("POST", "/api/sessions/"+id+"/wake", nil, nil); st != 409 || b["code"] != "not_asleep" {
		t.Fatalf("wake an awake one %d %v", st, b)
	}
	h.do("POST", "/api/sessions/"+id+"/sleep", nil, nil)
	h.waitInfo(id, "asleep a fourth time", asleep)
	if st, d := h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "while asleep"}, nil); st != 202 {
		t.Fatalf("deliver %d %v", st, d)
	}
	waitTrue(t, "delivered to a woken agent", 20*time.Second, func() bool { return strings.Contains(received(dir), "while asleep\n") })

	// stopping an asleep console keeps it stopped
	h.waitInfo(id, "quiet", quiet)
	h.do("POST", "/api/sessions/"+id+"/sleep", nil, nil)
	h.waitInfo(id, "asleep a fifth time", asleep)
	if st, in := h.do("POST", "/api/sessions/"+id+"/kill", nil, nil); st != 200 || in["status"] != "exited" || in["asleep"] == true {
		t.Fatalf("kill asleep %d %v", st, in)
	}
	_, sub = sess.Subscribe()
	time.Sleep(time.Second)
	sess.Unsubscribe(sub)
	if _, in := h.do("GET", "/api/sessions/"+id, nil, nil); in["status"] != "exited" {
		t.Fatalf("a stopped console woke %v", in)
	}
}

// TestLazyStart: after a hub restart, an agent that would resume stays asleep until its
// terminal is opened (ROADMAP #50). The second hub opens a copy of the first one's data.
func TestLazyStart(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	h := newTestHub(t)
	_, wid, dir := orchSetup(t, h)
	id := h.spawnAgent(wid)["id"].(string)
	h.waitInfo(id, "ready", state("ready"))
	h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "remember this"}, nil)
	waitTrue(t, "sent", 10*time.Second, func() bool { return strings.Contains(received(dir), "remember this\n") })
	h.waitInfo(id, "ready again", state("ready"))
	_, _ = h.srv.sessions.SetKeepAwake(id, false) // saved now
	h.do("POST", "/api/sessions/"+id+"/kill", nil, nil)
	waitTrue(t, "first hub's agent gone", 5*time.Second, func() bool { return !fakeRunning(dir) })
	// the restart sees it as it was before the kill: running, with its conversation
	data := t.TempDir()
	if out, err := exec.Command("cp", "-a", h.srv.st.Path()+"/.", data).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(data, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "sessions.json"), []byte(strings.ReplaceAll(string(raw), `"lastForeground":"shell"`, `"lastForeground":"agent"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	srv2, err := New(Options{DataDir: data, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	srv2.sessions.Start()
	t.Cleanup(func() {
		srv2.jobs.Close()
		srv2.sessions.Shutdown()
		srv2.review.Close()
		srv2.boards.Close()
	})
	in, ok := srv2.sessions.Info(id)
	if !ok || in.Status != session.StatusAsleep || !in.Asleep {
		t.Fatalf("after restart %+v", in)
	}
	time.Sleep(time.Second)
	if fakeRunning(dir) {
		t.Fatal("started before anyone opened it")
	}
	s2, _ := srv2.sessions.Get(id)
	snap, sub := s2.Subscribe()
	defer s2.Unsubscribe(sub)
	if !strings.Contains(string(snap), "asleep since the hub restarted") {
		t.Fatalf("no note in its terminal: %q", snap[max(0, len(snap)-300):])
	}
	waitTrue(t, "woken by its terminal", 15*time.Second, func() bool {
		in, _ := srv2.sessions.Info(id)
		return in.AgentState == "ready" && fakeRunning(dir)
	})
	// with lazyStart off, a restart starts it as before
	if _, err := srv2.settings.Patch([]byte(`{"agents":{"lazyStart":false}}`)); err != nil {
		t.Fatal(err)
	}
	if srv2.sessions.Settings().Agents.LazyStart {
		t.Fatal("setting")
	}
}

// TestWakeIntoDialog: when the woken agent shows a dialog instead of its prompt (here an update
// offer), nothing is typed into it; the draft and the keys typed meanwhile wait to be copied
// (ROADMAP #50).
func TestWakeIntoDialog(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	h := newTestHub(t)
	_, wid, dir := orchSetup(t, h)
	t.Setenv("FAKE_ECHO", "1")
	id := h.spawnAgent(wid)["id"].(string)
	h.waitInfo(id, "ready", state("ready"))
	h.do("POST", "/api/sessions/"+id+"/deliveries", map[string]any{"text": "hi"}, nil)
	waitTrue(t, "sent", 10*time.Second, func() bool { return strings.Contains(received(dir), "hi\n") })
	h.waitInfo(id, "quiet", quiet)
	sess, _ := h.srv.sessions.Get(id)
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": "keep me"}, nil)
	waitTrue(t, "draft on screen", 5*time.Second, func() bool { return strings.Contains(strings.Join(sess.Screen().Lines, ""), "keep me") })
	h.waitInfo(id, "quiet with a draft", quiet)
	if st, b := h.do("POST", "/api/sessions/"+id+"/sleep", nil, nil); st != 200 {
		t.Fatalf("sleep %d %v", st, b)
	}
	h.waitInfo(id, "asleep", asleep)
	t.Setenv("FAKE_MODE", "update") // what the woken CLI shows
	h.do("POST", "/api/sessions/"+id+"/input", map[string]any{"data": " too"}, nil)
	in := h.waitInfo(id, "the draft to copy", func(in map[string]any) bool { return in["unsentDraft"] != nil })
	if in["unsentDraft"] != "keep me too" {
		t.Fatalf("unsent %v", in["unsentDraft"])
	}
	if _, err := os.Stat(filepath.Join(dir, "UPDATED")); err == nil {
		t.Fatal("typed into the update offer")
	}
	if st, in := h.do("DELETE", "/api/sessions/"+id+"/draft", nil, nil); st != 200 || in["unsentDraft"] != nil {
		t.Fatalf("dismiss %d %v", st, in)
	}
}
