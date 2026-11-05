package connlog

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWriteReadAndFilter(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)
	l.Write(Event{At: base, Kind: HubStart, Version: "1", PID: 7})
	l.Write(Event{At: base.Add(time.Second), Kind: Connected, MachineID: "m_a", Machine: "alpha", DurationMs: 40})
	l.Write(Event{At: base.Add(2 * time.Second), Kind: Connected, MachineID: "m_b", Machine: "beta"})
	l.Write(Event{At: base.Add(3 * time.Second), Kind: Disconnected, MachineID: "m_a", Machine: "alpha", Reason: ReasonLost, UptimeMs: 2000, Error: strings.Repeat("e", 900)})
	got := l.Read(Query{})
	if len(got) != 4 || got[0].Kind != Disconnected || got[3].Kind != HubStart {
		t.Fatalf("newest first: %+v", got)
	}
	if n := len([]rune(got[0].Error)); n != maxMessage {
		t.Fatalf("error of %d runes", n)
	}
	// By id or name; hub events stay, they explain gaps.
	if got := l.Read(Query{Machine: "alpha"}); len(got) != 3 {
		t.Fatalf("alpha %+v", got)
	}
	if got := l.Read(Query{Machine: "m_b"}); len(got) != 2 {
		t.Fatalf("m_b %+v", got)
	}
	if got := l.Read(Query{Since: base.Add(2 * time.Second)}); len(got) != 2 {
		t.Fatalf("since %+v", got)
	}
	if got := l.Read(Query{Limit: 1}); len(got) != 1 || got[0].Kind != Disconnected {
		t.Fatalf("limit %+v", got)
	}
	fi, err := os.Stat(l.path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file %v %v", fi, err)
	}
	// A line a crash cut short costs only that line.
	f, _ := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"at":"2026-11-03T02:00:09Z","kind":"conn`)
	f.Close()
	if got := l.Read(Query{}); len(got) != 4 {
		t.Fatalf("after a cut line: %d", len(got))
	}
}

func TestLogStaysBounded(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l.max = 4 << 10
	for i := 0; i < 2000; i++ {
		l.Write(Event{Kind: ConnectFailed, Machine: "alpha", Attempt: i, Error: "dial tcp: connection refused"})
	}
	var total int64
	for _, p := range []string{l.path, l.rotated()} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		total += fi.Size()
	}
	if total > 2*l.max {
		t.Fatalf("%d bytes on disk", total)
	}
	got := l.Read(Query{Limit: 1})
	if len(got) != 1 || got[0].Attempt != 1999 {
		t.Fatalf("the newest must survive rotation: %+v", got)
	}
}

func TestSummarizeAndDescribe(t *testing.T) {
	events := []Event{
		{Kind: Connected, DurationMs: 50},
		{Kind: LinkStats, RTTAvgMs: 40, RTTMaxMs: 90, UptimeMs: 1000},
		{Kind: Disconnected, Reason: ReasonKeepalive, UptimeMs: 60000, RTTAvgMs: 60, RTTMaxMs: 300},
		{Kind: ConnectFailed, Attempt: 1, Error: "refused"},
		{Kind: Connected, DowntimeMs: 4000, Attempt: 2},
		{Kind: Disconnected, Reason: ReasonLost, UptimeMs: 20000},
		{Kind: Connected, DowntimeMs: 8000},
		{Kind: Disconnected, Reason: ReasonClosed, UptimeMs: 40000},
		{Kind: ToolFailed, Tool: "bash", Reason: "not_connected"},
		{Kind: ToolSlow, Tool: "read_file", DurationMs: 6000},
		{Kind: HubUnclean},
		{Kind: ChannelLost, SessionID: "s_1"},
		{Kind: KeepaliveMiss, Misses: 1},
	}
	s := Summarize(events)
	if s.Connects != 3 || s.ConnectFailed != 1 || s.Disconnects != 2 || s.Reasons[ReasonKeepalive] != 1 || s.Reasons[ReasonLost] != 1 {
		t.Fatalf("%+v", s)
	}
	if s.AvgDowntimeMs != 6000 || s.MaxDowntimeMs != 8000 || s.AvgUptimeMs != 40000 || s.RTTAvgMs != 50 || s.RTTMaxMs != 300 {
		t.Fatalf("%+v", s)
	}
	if s.ToolFailed != 1 || s.ToolSlow != 1 || s.UncleanExits != 1 || s.ChannelsLost != 1 || s.KeepaliveMiss != 1 {
		t.Fatalf("%+v", s)
	}
	for _, e := range events {
		if d := Describe(e); d == "" || d == e.Kind {
			t.Fatalf("no text for %s: %q", e.Kind, d)
		}
	}
	if got := fmt.Sprint(Describe(Event{Kind: Connected, DurationMs: 40, DowntimeMs: 4000})); !strings.Contains(got, "putus 4s") {
		t.Fatal(got)
	}
	var nilLog *Log
	nilLog.Write(Event{Kind: HubStart})
	if got := nilLog.Read(Query{}); len(got) != 0 {
		t.Fatal("nil log")
	}
}
