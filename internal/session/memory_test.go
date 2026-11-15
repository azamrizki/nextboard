package session

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.Open(st.Path("secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := machine.OpenRegistry(st, box)
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(st, reg)
}

// ringOf is what a client opening the console gets, without waking it.
func ringOf(s *Session) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ringBytesLocked()
}

// wantRing is a scrollback of these writes, as a ring holds it.
func wantRing(parts ...string) []byte {
	r := NewRing(ScrollbackSize)
	for _, p := range parts {
		r.Write([]byte(p))
	}
	return r.Bytes()
}

// After a hub restart every console shows what it showed, with the same status, but the ones
// nothing runs in (asleep, on a machine not connected) hold neither their scrollback nor an
// emulator: they are read when they are needed.
func TestHubRestartLoadsOnlyWhatRuns(t *testing.T) {
	m := testManager(t)
	big := strings.Repeat("an old line of output\r\n", 60_000) // more than the ring holds
	cases := []struct {
		sp   Spec
		priv private
		old  string
	}{
		{Spec{ID: "s_asleep0001", Name: "a", Handle: "A", MachineID: machine.LocalID, Agent: "claude", Preset: "claude", AgentKind: agent.KindAgent,
			Command: "claude", Cols: 80, Rows: 24, Cwd: t.TempDir(), AgentSessionID: "b5025559-deaf-4775-a918-a2ea324b8802", Asleep: true}, private{HookToken: "t1"}, "asleep before\r\n"},
		{Spec{ID: "s_lazy000001", Name: "b", Handle: "B", MachineID: machine.LocalID, Agent: "codex", Preset: "codex", AgentKind: agent.KindAgent,
			Command: "codex", Cols: 80, Rows: 24, Cwd: t.TempDir(), AgentSessionID: "019a-codex-thread-0001"}, private{HookToken: "t2"}, big},
		{Spec{ID: "s_remote0001", Name: "c", Handle: "C", MachineID: "m_offline", Agent: "shell", Preset: "shell", AgentKind: agent.KindShell,
			Command: "$SHELL", Cols: 120, Rows: 40, Cwd: "/srv"}, private{HookToken: "t3"}, "remote before\r\n"},
		{Spec{ID: "s_shell00001", Name: "d", Handle: "D", MachineID: machine.LocalID, Agent: "custom-sh", AgentKind: agent.KindShell,
			Command: "/bin/sh", Cols: 80, Rows: 24, Cwd: t.TempDir()}, private{HookToken: "t4"}, "shell before\r\n"},
	}
	var list []stored
	for _, c := range cases {
		list = append(list, stored{Spec: c.sp, private: c.priv})
		if err := os.WriteFile(m.scrollbackPath(c.sp.ID), []byte(c.old), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.st.Save(sessionsFile, list); err != nil {
		t.Fatal(err)
	}
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	get := func(id string) *Session {
		s, ok := m.Get(id)
		if !ok {
			t.Fatalf("%s not loaded", id)
		}
		return s
	}
	for _, id := range []string{"s_asleep0001", "s_lazy000001", "s_remote0001"} {
		if scr, sb := get(id).InMemory(); scr || sb {
			t.Fatalf("%s loaded at hub start: screen %v scrollback %v", id, scr, sb)
		}
	}
	get("s_shell00001").scr.Sync()
	if scr, sb := get("s_shell00001").InMemory(); !scr || !sb {
		t.Fatalf("the shell that starts again was not loaded: screen %v scrollback %v", scr, sb)
	}
	if got := ringOf(get("s_remote0001")); !bytes.Equal(got, wantRing("remote before\r\n", hubRestartedNote)) {
		t.Fatalf("remote console shows %q", got)
	}
	if got := ringOf(get("s_shell00001")); !bytes.Equal(got, wantRing("shell before\r\n", hubRestartedNote)) {
		t.Fatalf("shell shows %q", got)
	}
	if err := m.Delete("s_shell00001"); err != nil { // no process to start here
		t.Fatal(err)
	}

	m.Start()
	defer m.Shutdown()
	want := map[string]struct {
		status string
		ring   []byte
	}{
		"s_asleep0001": {StatusAsleep, wantRing("asleep before\r\n", hubRestartedNote, asleepNote)},
		"s_lazy000001": {StatusAsleep, wantRing(big, hubRestartedNote, restartNote)},
		"s_remote0001": {StatusReconnecting, wantRing("remote before\r\n", hubRestartedNote)},
	}
	for id, w := range want {
		s := get(id)
		if in := m.SessionInfo(s); in.Status != w.status {
			t.Fatalf("%s status %s, want %s", id, in.Status, w.status)
		}
		if got := ringOf(s); !bytes.Equal(got, w.ring) {
			t.Fatalf("%s shows %d bytes ending %q, want %d ending %q", id, len(got), lastOf(got), len(w.ring), lastOf(w.ring))
		}
		if scr, sb := s.InMemory(); scr || sb {
			t.Fatalf("%s held after start: screen %v scrollback %v", id, scr, sb)
		}
	}
	// what the screen model of an asleep console says, now as before: its note
	if sn := get("s_asleep0001").scr.Snapshot(); !strings.Contains(sn.Text(), "asleep to free memory") {
		t.Fatalf("asleep screen %q", sn.Text())
	}
	// Stopping the hub writes the restart line of a console that never ran into its file, as
	// it did when every scrollback was loaded.
	m.Shutdown()
	b, _ := os.ReadFile(m.scrollbackPath("s_remote0001"))
	if !bytes.Equal(b, wantRing("remote before\r\n", hubRestartedNote)) {
		t.Fatalf("remote file after shutdown %q", b)
	}
}

func lastOf(b []byte) []byte { return b[max(0, len(b)-60):] }

// stopSaves cancels a pending sessions.json write, which would land in a removed temp dir.
func stopSaves(m *Manager) {
	m.saveMu.Lock()
	if m.saveTimer != nil {
		m.saveTimer.Stop()
		m.saveTimer = nil
	}
	m.saveMu.Unlock()
}

// Opening a console that ended reads its scrollback from its file and leaves it there.
func TestOpeningAnEndedConsoleKeepsItsScrollbackInItsFile(t *testing.T) {
	s := startingShell(t)
	if err := s.Input([]byte("echo nb-$((2+3))-bye; exit\r")); err != nil {
		t.Fatal(err)
	}
	start(s)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if scr, sb := s.InMemory(); !scr && !sb {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("an ended console still holds its screen or scrollback")
		}
		time.Sleep(20 * time.Millisecond)
	}
	snap, sub := s.Subscribe()
	defer s.Unsubscribe(sub)
	if !bytes.Contains(snap, []byte("nb-5-bye")) || !bytes.Contains(snap, []byte("process exited")) {
		t.Fatalf("snapshot %q", snap)
	}
	if _, sb := s.InMemory(); sb {
		t.Fatal("opening it took its scrollback back into memory")
	}
}

// A console waiting for its machine lets its screen and scrollback go; what it prints when it
// runs again follows on from both.
func TestReconnectingParksAndOutputFollowsOn(t *testing.T) {
	s := startingShell(t)
	if err := s.Input([]byte("echo nb-$((1+1))-one\r")); err != nil {
		t.Fatal(err)
	}
	start(s)
	waitFor(t, s, "nb-2-one")
	s.setStatus(StatusReconnecting, 0, "")
	if scr, sb := s.InMemory(); scr || sb {
		t.Fatalf("reconnecting holds screen %v scrollback %v", scr, sb)
	}
	if err := s.Input([]byte("echo nb-$((1+2))-two\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, "nb-2-one", "nb-3-two")
	s.scr.Sync()
	if txt := s.scr.Snapshot().Text(); !strings.Contains(txt, "nb-2-one") || !strings.Contains(txt, "nb-3-two") {
		t.Fatalf("screen after the reconnect %q", txt)
	}
}

// Killing a console that has no process (waiting for its machine) lets go of it as well.
func TestKillWithoutAProcessParks(t *testing.T) {
	m := testManager(t)
	s := newSession(m, Spec{ID: "s_remote0002", Name: "r", Handle: "R", MachineID: "m_offline", Agent: "shell", AgentKind: agent.KindShell, Cols: 80, Rows: 24})
	m.mu.Lock()
	m.sessions[s.spec.ID], m.order = s, append(m.order, s.spec.ID)
	m.mu.Unlock()
	s.mu.Lock()
	s.writeLocked("hello\r\n")
	s.status = StatusReconnecting
	s.mu.Unlock()
	s.scr.Sync()
	if scr, sb := s.InMemory(); !scr || !sb {
		t.Fatalf("set up: screen %v scrollback %v", scr, sb)
	}
	in, err := m.Kill(s.spec.ID)
	stopSaves(m)
	if err != nil || in.Status != StatusExited {
		t.Fatalf("kill %+v %v", in.Status, err)
	}
	if scr, sb := s.InMemory(); scr || sb {
		t.Fatalf("killed console holds screen %v scrollback %v", scr, sb)
	}
	if got := ringOf(s); !bytes.Equal(got, []byte("hello\r\n")) {
		t.Fatalf("scrollback %q", got)
	}
	s.scr.Close()
}

// A subscriber with more than SubMaxBytes of output waiting is dropped like one whose channel
// is full; what it read no longer counts.
func TestSubscriberDroppedOverItsBytes(t *testing.T) {
	m := testManager(t)
	s := newSession(m, Spec{ID: "s_subs000001", Name: "x", Handle: "X", MachineID: machine.LocalID, Agent: "shell", AgentKind: agent.KindShell, Cols: 80, Rows: 24})
	defer s.scr.Close()
	_, sub := s.Subscribe()
	chunk := make([]byte, 1<<20)
	s.mu.Lock()
	for range 4 {
		s.fanoutLocked(Msg{Data: chunk})
	}
	s.fanoutLocked(Msg{Status: &StatusMsg{Status: StatusRunning}}) // no output bytes
	_, kept := s.subs[sub]
	s.mu.Unlock()
	if !kept || len(sub.C) != 5 {
		t.Fatalf("dropped at the limit: kept %v, %d queued", kept, len(sub.C))
	}
	<-sub.C
	<-sub.C // the reader took 2 MiB
	s.mu.Lock()
	s.fanoutLocked(Msg{Data: chunk})
	s.fanoutLocked(Msg{Data: chunk})
	_, kept = s.subs[sub]
	s.fanoutLocked(Msg{Data: []byte("x")})
	_, keptOver := s.subs[sub]
	s.mu.Unlock()
	if !kept || keptOver {
		t.Fatalf("kept %v after reading, kept %v over the limit", kept, keptOver)
	}
	for range sub.C {
	}
}

// The parts of a ring are what Bytes returns, wrapped or not.
func TestRingPartsAreBytes(t *testing.T) {
	for _, writes := range [][]string{
		{"abc"},
		{"aaaa\nbbbb\ncccc"},
		{strings.Repeat("x", 70), "\nyy"},
		{"noline", strings.Repeat("z", 20)},
	} {
		for _, size := range []int{4, 10, 16, 64} {
			r := NewRing(size)
			for _, w := range writes {
				r.Write([]byte(w))
			}
			a, b := r.parts()
			if got, want := string(a)+string(b), string(r.Bytes()); got != want {
				t.Fatalf("%q in %d: parts %q, bytes %q", writes, size, got, want)
			}
		}
	}
	// the cut at the first newline only looks 64 KiB in, as Bytes does
	r := NewRing(cutWithin * 2)
	r.Write([]byte(strings.Repeat("q", cutWithin*2+10) + "\nend"))
	if a, b := r.parts(); !bytes.HasSuffix(append(append([]byte(nil), a...), b...), []byte("end")) || len(a)+len(b) != cutWithin*2 {
		t.Fatalf("far newline: %d bytes", len(a)+len(b))
	}
}

// The scrollback file the flush writes is what the ring holds.
func TestFlushWritesTheRing(t *testing.T) {
	m := testManager(t)
	s := newSession(m, Spec{ID: "s_flush00001", Name: "f", Handle: "F", MachineID: machine.LocalID, Agent: "shell", AgentKind: agent.KindShell, Cols: 80, Rows: 24})
	defer s.scr.Close()
	m.mu.Lock()
	m.sessions[s.spec.ID], m.order = s, append(m.order, s.spec.ID)
	m.mu.Unlock()
	s.mu.Lock()
	s.writeLocked(strings.Repeat("a line that wraps the ring\r\n", 50_000))
	s.writeLocked("last\r\n")
	want := s.ring.Bytes()
	s.mu.Unlock()
	m.flushScrollback(false)
	got, err := os.ReadFile(m.scrollbackPath(s.spec.ID))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("file %d bytes, ring %d (%v)", len(got), len(want), err)
	}
	s.mu.Lock()
	dirty := s.dirty
	s.mu.Unlock()
	if dirty {
		t.Fatal("still dirty after the flush")
	}
	if left, _ := os.ReadDir(m.st.Path("scrollback")); len(left) != 1 {
		t.Fatalf("files left: %v", left)
	}
}

// A round of idle sleep still running lets the next one pass.
func TestSleepRoundsDoNotOverlap(t *testing.T) {
	m := testManager(t)
	m.sleepRound.Store(true)
	m.sleepIdle(time.Now()) // returns at once: a round is running
	if !m.sleepRound.Load() {
		t.Fatal("a skipped round ended the running one")
	}
	m.sleepRound.Store(false)
	m.sleepIdle(time.Now())
	if m.sleepRound.Load() {
		t.Fatal("a round did not end")
	}
}

// An agent found running a command is asked again after sleepRetryAfter, not every round.
func TestSleepBacksOffWhileACommandRuns(t *testing.T) {
	m := testManager(t)
	s := newSession(m, Spec{ID: "s_sleepy0001", Name: "z", Handle: "Z", MachineID: machine.LocalID, Agent: "claude", Preset: "claude",
		AgentKind: agent.KindAgent, Cols: 80, Rows: 24, AgentSessionID: "b5025559-deaf-4775-a918-a2ea324b8802"})
	defer s.scr.Close()
	now := time.Now()
	s.mu.Lock()
	s.cur, s.status = &run{}, StatusRunning // no wrapper pid: as good as a command running
	s.eng.SetProc(StatusRunning, now)
	s.eng.AgentStart(now)
	s.eng.Hook(agentstate.Hook{Event: "SessionStart"}, now)
	s.mu.Unlock()
	m.mu.Lock()
	m.sessions[s.spec.ID], m.order = s, append(m.order, s.spec.ID)
	m.mu.Unlock()
	retryAt := func() time.Time {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.sleepRetryAt
	}
	if n := m.SleepIdle(0, now); n != 0 || !retryAt().Equal(now.Add(sleepRetryAfter)) {
		t.Fatalf("first round: %d asleep, retry at %v", n, retryAt())
	}
	m.SleepIdle(0, now.Add(time.Minute))
	if !retryAt().Equal(now.Add(sleepRetryAfter)) {
		t.Fatal("asked again within the back-off")
	}
	m.SleepIdle(0, now.Add(sleepRetryAfter+time.Second))
	if !retryAt().Equal(now.Add(2*sleepRetryAfter + time.Second)) {
		t.Fatalf("not asked again after the back-off: %v", retryAt())
	}
}
