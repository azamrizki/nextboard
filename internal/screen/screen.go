// Package screen keeps a virtual terminal model of each console in the hub
// (docs/API-agents.md §5): only the visible screen, fed with the already
// stripped output stream. It is used by the per-CLI screen rules of the
// status engine and, later, by delivery guards and usage parsing.
//
// The emulator is github.com/charmbracelet/x/vt behind the narrow Model
// interface, so it can be swapped.
package screen

import (
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// QueueMax bounds the bytes waiting for the model goroutine; beyond it the
// model is reset and marked stale until the next redraw.
const QueueMax = 4 << 20

// ReplayBytes of scrollback are replayed into a fresh model at hub start.
const ReplayBytes = 256 << 10

// Model is the narrow emulator interface the hub depends on.
type Model interface {
	// Write feeds the emulator. false means part of p was lost: see vtModel.Write.
	Write(p []byte) bool
	Resize(cols, rows int)
	Lines() []string // visible rows, trailing spaces trimmed
	Cursor() (x, y int)
	AltScreen() bool
	BracketedPaste() bool
	Reset()
}

// screenScrollback is the least the emulator accepts as history.
const screenScrollback = 1

const (
	writeChunk   = 32 << 10
	maxPanicLogs = 3 // per console: the same output can trip it again and again
)

// vtModel implements Model with charmbracelet/x/vt. Queries from the
// program (DA, DSR, OSC 10/11) are answered only when respond is set
// (hidden sessions); for normal consoles xterm.js in the client answers and
// the model's replies are discarded.
type vtModel struct {
	cols, rows int
	respond    func([]byte)
	e          *vt.Emulator
	paste      atomic.Bool
	panics     int
}

// NewModel returns a vt-backed model.
func NewModel(cols, rows int, respond func([]byte)) Model {
	m := &vtModel{cols: cols, rows: rows, respond: respond}
	m.Reset()
	return m
}

// stop ends the drain goroutine. Emulator.Close writes a flag that Read
// checks without a lock, so the pipe is closed directly instead.
func (m *vtModel) stop() {
	if m.e == nil {
		return
	}
	if c, ok := m.e.InputPipe().(interface{ CloseWithError(error) error }); ok {
		_ = c.CloseWithError(io.EOF)
	}
}

func (m *vtModel) Reset() {
	m.stop()
	m.paste.Store(false)
	e := vt.NewEmulator(max(m.cols, 1), max(m.rows, 1))
	// Only the visible screen is read here; the history lives in the session's
	// ring. Left at its default, this keeps 10 000 lines per console, ~150 MB.
	e.SetScrollbackSize(screenScrollback)
	e.SetCallbacks(vt.Callbacks{
		EnableMode: func(mode ansi.Mode) {
			if mode == ansi.ModeBracketedPaste {
				m.paste.Store(true)
			}
		},
		DisableMode: func(mode ansi.Mode) {
			if mode == ansi.ModeBracketedPaste {
				m.paste.Store(false)
			}
		},
	})
	// The emulator takes a bottom margin past the screen as it comes, and its next
	// scroll runs off the end of its buffer and panics. It happens when output for a
	// taller terminal reaches this one, as in a replay at hub start. Terminals clamp it.
	e.RegisterCsiHandler('r', func(params ansi.Params) bool {
		bottom, _, _ := params.Param(1, 0)
		if bottom <= e.Height() {
			return false // the emulator's own handler goes on
		}
		top, _, _ := params.Param(0, 1)
		_, _ = e.Write(fmt.Appendf(nil, "\x1b[%d;%dr", top, e.Height()))
		return true
	})
	// The same goes for a right margin, once a program has turned those on.
	e.RegisterCsiHandler('s', func(params ansi.Params) bool {
		right, _, _ := params.Param(1, 0)
		if right <= e.Width() {
			return false
		}
		left, _, _ := params.Param(0, 1)
		_, _ = e.Write(fmt.Appendf(nil, "\x1b[%d;%ds", left, e.Width()))
		return true
	})
	// It saves the cursor on CSI s and has nothing for the CSI u that restores it.
	e.RegisterCsiHandler('u', func(ansi.Params) bool {
		_, _ = e.Write([]byte("\x1b8"))
		return true
	})
	// It also does as often as it is told what has no effect past the screen, while
	// the screen's lock is held: repeat a character, erase characters, tab either way.
	limitCount := func(final rune, limit func() int) {
		e.RegisterCsiHandler(int(final), func(params ansi.Params) bool {
			n, _, _ := params.Param(0, 1)
			if n <= limit() {
				return false
			}
			_, _ = e.Write(fmt.Appendf(nil, "\x1b[%d%c", limit(), final))
			return true
		})
	}
	limitCount('b', func() int { return e.Width() * e.Height() })
	for _, final := range "IZX" {
		limitCount(final, e.Width)
	}
	m.e = e
	// The emulator answers queries into a pipe; it must always be drained
	// or Write blocks.
	respond := m.respond
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := e.Read(buf)
			if n > 0 && respond != nil {
				respond(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	}()
}

// guard runs one call into the emulator. It parses whatever a program prints, and a
// panic in it runs on a goroutine of the hub: one console's output must not take
// every console down, least of all at start, when the same output is replayed and
// the hub would die again each time. The emulator starts over instead.
func (m *vtModel) guard(f func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if m.panics++; m.panics <= maxPanicLogs {
				log.Printf("screen: emulator panic, starting it over: %v", r)
			}
			m.Reset()
		}
	}()
	f()
	return true
}

// Write feeds p in pieces: a panic costs its piece, and the model still catches up
// with what follows. false means something was lost.
func (m *vtModel) Write(p []byte) bool {
	ok := true
	for len(p) > 0 {
		n := min(len(p), writeChunk)
		chunk := p[:n]
		p = p[n:]
		ok = m.guard(func() { _, _ = m.e.Write(chunk) }) && ok
	}
	return ok
}

func (m *vtModel) Resize(cols, rows int) {
	if cols < 1 || rows < 1 || (cols == m.cols && rows == m.rows) {
		return
	}
	m.cols, m.rows = cols, rows
	m.guard(func() { m.e.Resize(cols, rows) })
}

func (m *vtModel) Lines() (lines []string) {
	m.guard(func() {
		lines = strings.Split(m.e.String(), "\n")
		if len(lines) > m.rows {
			lines = lines[:m.rows]
		}
	})
	return lines
}

func (m *vtModel) Cursor() (x, y int) {
	m.guard(func() {
		p := m.e.CursorPosition()
		x, y = p.X, p.Y
	})
	return x, y
}

func (m *vtModel) AltScreen() (alt bool) {
	m.guard(func() { alt = m.e.IsAltScreen() })
	return alt
}

func (m *vtModel) BracketedPaste() bool { return m.paste.Load() }

func (m *vtModel) close() { m.stop() }

// Snapshot is a copy of the visible screen.
type Snapshot struct {
	Lines          []string
	CursorX        int
	CursorY        int
	Alt            bool
	BracketedPaste bool
	// Stale: the queue overflowed and the model was reset; contents are
	// incomplete until the program redraws.
	Stale bool
	// Version increases with every applied write (cheap change check).
	Version uint64
}

// Screen feeds a Model from a bounded queue on its own goroutine, so the
// terminal reader never waits for parsing.
type Screen struct {
	mu      sync.Mutex
	m       Model
	stale   bool
	version uint64

	qmu      sync.Mutex
	queue    [][]byte
	queued   int
	barriers []chan struct{}
	wake     chan struct{}
	closed   bool
	quit     chan struct{}
	done     chan struct{}
}

// New starts a screen of cols×rows. respond (may be nil) receives the
// model's answers to terminal queries.
func New(cols, rows int, respond func([]byte)) *Screen {
	s := &Screen{m: NewModel(cols, rows, respond), wake: make(chan struct{}, 1), quit: make(chan struct{}), done: make(chan struct{})}
	go s.loop()
	return s
}

// Feed queues p (copied). When the queue is over QueueMax the pending data
// is dropped and the model reset (Snapshot.Stale until more output).
func (s *Screen) Feed(p []byte) {
	if len(p) == 0 {
		return
	}
	s.qmu.Lock()
	if s.closed {
		s.qmu.Unlock()
		return
	}
	if s.queued+len(p) > QueueMax {
		s.queue, s.queued = nil, 0
		s.qmu.Unlock()
		s.mu.Lock()
		s.m.Reset()
		s.stale = true
		s.version++
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, append([]byte(nil), p...))
	s.queued += len(p)
	s.qmu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Screen) loop() {
	defer close(s.done)
	for {
		select {
		case <-s.wake:
		case <-s.quit:
			return
		}
		for {
			s.qmu.Lock()
			if s.closed {
				s.qmu.Unlock()
				return
			}
			q, bar := s.queue, s.barriers
			s.queue, s.queued, s.barriers = nil, 0, nil
			s.qmu.Unlock()
			if len(q) == 0 {
				for _, b := range bar {
					close(b)
				}
				break
			}
			s.mu.Lock()
			for _, b := range q {
				lost := !s.m.Write(b)
				if s.stale && isRedraw(b) {
					s.stale = false
				}
				if lost {
					s.stale = true // the screen is not what the program drew
				}
			}
			s.version++
			s.mu.Unlock()
			for _, b := range bar {
				close(b)
			}
		}
	}
}

// Sync waits until everything queued so far has been applied (tests,
// replay at start).
func (s *Screen) Sync() {
	done := make(chan struct{})
	s.qmu.Lock()
	if s.closed {
		s.qmu.Unlock()
		return
	}
	s.barriers = append(s.barriers, done)
	s.qmu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	<-done
}

// isRedraw reports whether b repaints the whole screen (clear, alt screen,
// full reset), which ends the stale state after an overflow.
func isRedraw(b []byte) bool {
	for _, seq := range []string{"\x1b[2J", "\x1b[3J", "\x1b[?1049h", "\x1b[?1049l", "\x1bc"} {
		if strings.Contains(string(b), seq) {
			return true
		}
	}
	return false
}

// Resize follows the session size.
func (s *Screen) Resize(cols, rows int) {
	s.Sync()
	s.mu.Lock()
	s.m.Resize(cols, rows)
	s.version++
	s.mu.Unlock()
}

// Reset clears the model (e.g. a new process generation).
func (s *Screen) Reset() {
	s.qmu.Lock()
	s.queue, s.queued = nil, 0
	s.qmu.Unlock()
	s.mu.Lock()
	s.m.Reset()
	s.stale = false
	s.version++
	s.mu.Unlock()
}

// Snapshot copies the visible screen.
func (s *Screen) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, y := s.m.Cursor()
	return Snapshot{
		Lines: s.m.Lines(), CursorX: x, CursorY: y, Alt: s.m.AltScreen(),
		BracketedPaste: s.m.BracketedPaste(), Stale: s.stale, Version: s.version,
	}
}

// Version returns the change counter without copying the screen.
func (s *Screen) Version() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// Close stops the goroutine and the emulator.
func (s *Screen) Close() {
	s.qmu.Lock()
	if s.closed {
		s.qmu.Unlock()
		return
	}
	s.closed = true
	s.queue = nil
	for _, b := range s.barriers {
		close(b)
	}
	s.barriers = nil
	s.qmu.Unlock()
	close(s.quit)
	<-s.done
	s.mu.Lock()
	if c, ok := s.m.(interface{ close() }); ok {
		c.close()
	}
	s.mu.Unlock()
}

// Text returns the non-empty lines joined, for debugging and fixtures.
func (sn Snapshot) Text() string {
	return strings.TrimRight(strings.Join(sn.Lines, "\n"), "\n ")
}
