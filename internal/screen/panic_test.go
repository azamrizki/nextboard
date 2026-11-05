package screen

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func model(t *testing.T, cols, rows int) *vtModel {
	t.Helper()
	m := NewModel(cols, rows, nil).(*vtModel)
	t.Cleanup(m.close)
	return m
}

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "\x1b[%d;1Hrow%d", i, i)
	}
	return b.String()
}

// Seen for real: output for a taller terminal, replayed at hub start, panicked in
// the emulator and stopped the hub, again at every start.
func TestBottomMarginPastTheScreen(t *testing.T) {
	for name, seq := range map[string]string{
		"scroll up":  "\x1b[5;30r\x1b[2S",
		"line feeds": "\x1b[5;30r\x1b[24;1H\n\n\n",
		"only top":   "\x1b[1;100r\x1b[S",
		"huge":       "\x1b[1;99999r\x1b[3S",
	} {
		t.Run(name, func(t *testing.T) {
			m := model(t, 80, 24)
			if !m.Write([]byte(seq)) {
				t.Fatal("the emulator panicked")
			}
			if m.panics != 0 {
				t.Errorf("%d panics", m.panics)
			}
		})
	}
}

// Clamped the way a terminal does it: the region ends at the last row.
func TestBottomMarginPastTheScreenIsClamped(t *testing.T) {
	m := model(t, 40, 10)
	m.Write([]byte(numbered(10)))
	m.Write([]byte("\x1b[5;30r\x1b[2S"))
	lines := m.Lines()
	for i, want := range map[int]string{0: "row1", 3: "row4", 4: "row7", 7: "row10", 8: "", 9: ""} {
		if got := strings.TrimSpace(lines[i]); got != want {
			t.Errorf("row %d = %q, want %q; screen %q", i, got, want, lines)
		}
	}
}

func TestMarginsInsideTheScreenStayTheEmulators(t *testing.T) {
	m := model(t, 40, 10)
	m.Write([]byte(numbered(10)))
	m.Write([]byte("\x1b[3;6r\x1b[S"))
	lines := m.Lines()
	for i, want := range map[int]string{0: "row1", 1: "row2", 2: "row4", 4: "row6", 5: "", 6: "row7", 9: "row10"} {
		if got := strings.TrimSpace(lines[i]); got != want {
			t.Errorf("row %d = %q, want %q; screen %q", i, got, want, lines)
		}
	}
}

func TestMarginClampLeavesTheParserWorking(t *testing.T) {
	m := model(t, 40, 5)
	m.Write([]byte("\x1b[1;30r\x1b[1;1Hhello \x1b[31mred\x1b[m after"))
	if got := strings.TrimSpace(m.Lines()[0]); got != "hello red after" {
		t.Errorf("%q", got)
	}
}

// Whatever else the emulator panics on: the console starts over, the hub lives.
func TestEmulatorPanicStartsItOver(t *testing.T) {
	m := model(t, 40, 5)
	old := m.e
	if m.guard(func() { panic("boom") }) {
		t.Fatal("guard reported no failure")
	}
	if m.e == old || m.panics != 1 {
		t.Fatal("the emulator did not start over")
	}
	if !m.Write([]byte("back")) || strings.TrimSpace(m.Lines()[0]) != "back" {
		t.Errorf("after the panic: %q", m.Lines())
	}
}

func TestCountsPastTheScreenDoWhatTheySay(t *testing.T) {
	m := model(t, 10, 2)
	m.Write([]byte("abcdefghij\x1b[1;4H\x1b[99999999X"))
	if got := strings.TrimRight(m.Lines()[0], " "); got != "abc" {
		t.Errorf("erase to the end of the line: %q", got)
	}
	m = model(t, 20, 2)
	m.Write([]byte("\x1b[99999999Ix"))
	if x, _ := m.Cursor(); x != 20 && x != 19 {
		t.Errorf("tab to the right edge: cursor at %d", x)
	}
	m.Write([]byte("\r\x1b[1;15H\x1b[99999999Zy"))
	if x, _ := m.Cursor(); x != 1 {
		t.Errorf("back tab to the left edge: cursor at %d", x)
	}
	m = model(t, 20, 2)
	m.Write([]byte("\x1b[2Ix"))
	if x, _ := m.Cursor(); x != 17 {
		t.Errorf("a small tab count: cursor at %d", x)
	}
}

// A model that loses output, to see what the screen makes of it.
type losingModel struct {
	Model
	lose bool
}

func (l *losingModel) Write(p []byte) bool {
	l.Model.Write(p)
	return !l.lose
}

// What was lost is not what the program drew, and nothing may be typed by it.
func TestLostOutputMarksTheScreenStale(t *testing.T) {
	s := New(40, 5, nil)
	defer s.Close()
	lm := &losingModel{Model: s.m, lose: true}
	s.mu.Lock()
	s.m = lm
	s.mu.Unlock()

	s.Feed([]byte("partial"))
	s.Sync()
	if !s.Snapshot().Stale {
		t.Fatal("not stale after losing output")
	}
	lm.lose = false
	s.Feed([]byte("more"))
	s.Sync()
	if !s.Snapshot().Stale {
		t.Error("output without a redraw cannot end it")
	}
	s.Feed([]byte("\x1b[2J"))
	s.Sync()
	if s.Snapshot().Stale {
		t.Error("a redraw must end it")
	}
}

// A replay as at hub start, with the sequence in the middle of it, and output after.
func TestReplayWithAMarginPastTheScreen(t *testing.T) {
	var b bytes.Buffer
	for b.Len() < ReplayBytes/2 {
		fmt.Fprintf(&b, "line %d\r\n", b.Len())
	}
	b.WriteString("\x1b[1;200r\x1b[2S\n\n")
	for b.Len() < ReplayBytes {
		fmt.Fprintf(&b, "line %d\r\n", b.Len())
	}
	s := New(80, 24, nil)
	defer s.Close()
	s.Feed(b.Bytes())
	s.Sync()
	sn := s.Snapshot()
	if sn.Stale || !strings.Contains(strings.Join(sn.Lines, "\n"), "line ") {
		t.Errorf("stale %v: %q", sn.Stale, sn.Lines[:3])
	}
}

// The screen's lock is held while the emulator works: one sequence must not hold it
// for long. A repeat count of 99999999 took over 3 seconds, and it is printable text
// away from any program that prints binary data.
func TestHugeCountsEndSoon(t *testing.T) {
	for _, final := range "ABCDEFGHIJKLMPSTXZ@`bdfgrstnc" {
		seq := "x\x1b[99999999" + string(final)
		m := model(t, 80, 24)
		done := make(chan struct{})
		go func() {
			m.Write([]byte(seq))
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("%q still runs after 5 s", seq)
		}
	}
}

func TestRepeatFillsTheScreenAndNoMore(t *testing.T) {
	m := model(t, 10, 3)
	m.Write([]byte("x\x1b[99999999b"))
	joined := strings.Join(m.Lines(), "")
	if got := strings.Count(joined, "x"); got < 10 || got > 30 {
		t.Errorf("%d characters on a 10×3 screen: %q", got, m.Lines())
	}
	m2 := model(t, 10, 3)
	m2.Write([]byte("ab\x1b[3b"))
	if got := strings.TrimSpace(m2.Lines()[0]); got != "abbbb" {
		t.Errorf("a small repeat: %q", got)
	}
}

// Programs print anything, and a replay starts anywhere. No input may stop the hub.
func TestModelSurvivesAnything(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	pieces := []string{
		"text ", "\r\n", "\n", "\t", "\b", "\x1b[2J", "\x1b[H", "\x1bc", "\x1b[?1049h", "\x1b[?1049l", "\x1b[?6h", "\x1b[?6l", "\x1b[?69h", "\x1b[?69l",
		"\x1b[r", "\x1b[s", "\x1b[u", "\x1b7", "\x1b8", "\x1bM", "\x1bD", "\x1bE", "\x1b(0", "\x1b(B", "\x1b[m", "é", "漢", "\xff\xfe", "\x1b", "\x1b[", "\x1b]0;",
		"\x07", "\x1b\\", "\x1bP", "\x1b_", "\x90", "\x9b",
	}
	finals := []string{"A", "B", "C", "D", "E", "F", "G", "H", "J", "K", "L", "M", "P", "S", "T", "X", "Z", "@", "`", "b", "d", "f", "g", "r", "s", "t", "n", "c"}
	nums := []string{"", "0", "1", "2", "5", "24", "80", "200", "999"}
	for size := 0; size < 6; size++ {
		m := model(t, []int{1, 2, 10, 80, 200, 40}[size], []int{1, 3, 5, 24, 60, 2}[size])
		for i := 0; i < 4000; i++ {
			var seq string
			if r.Intn(2) == 0 {
				seq = pieces[r.Intn(len(pieces))]
			} else {
				seq = "\x1b[" + nums[r.Intn(len(nums))] + ";" + nums[r.Intn(len(nums))] + finals[r.Intn(len(finals))]
			}
			m.Write([]byte(seq))
			if i%500 == 0 {
				m.Resize(1+r.Intn(200), 1+r.Intn(60))
				_ = m.Lines()
				_, _ = m.Cursor()
				_ = m.AltScreen()
			}
		}
		t.Logf("size %d: %d panics started the emulator over", size, m.panics)
	}
}
