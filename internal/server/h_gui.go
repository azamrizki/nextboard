package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/gui"
)

// Desktop apps of This PC on board tiles (docs/API-gui.md).

const (
	// guiViewerPath is the one page the web UI frames from its own origin.
	guiViewerPath = "/gui-viewer.html"
	guiViewerCSP  = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"

	// guiClientLimit bounds one browser message; a paste carries the text.
	guiClientLimit = 16 << 20
	// guiAckLimit bounds the small messages of the video and audio sockets.
	guiAckLimit = 4 << 10
	// guiVideoQueue is the most delay a full link may add to the picture.
	guiVideoQueue = 150 * time.Millisecond
	// guiVideoStartQueue applies until the first answer shows the link's delay.
	guiVideoStartQueue = time.Second
	guiVideoMaxPending = 60
	// Waiting most of a guiVideoSlowWindow means this rate doesn't fit the link.
	guiVideoSlowWindow = 2 * time.Second
	guiVideoSlowEvery  = 5 * time.Second
	// A picture taking this share of the rate's budget per frame is big motion.
	// A blinking cursor stays below it, or nothing would ever count as still.
	// web/src/gui/viewer.ts decides with the same numbers.
	guiMotionShare    = 0.25
	guiMotionMinBytes = 800
	// guiVideoSleepAfter is how long a still screen keeps the encoder awake.
	guiVideoSleepAfter = 2 * time.Second
	guiVideoIdleTick   = 50 * time.Millisecond
	// guiWakeBytes of RFB within guiWakeTicks means the screen moves: more than
	// typing sends, and little enough to happen on a slow link too, where RFB
	// can't go faster than the link.
	guiWakeBytes = 100 << 10
	guiWakeTicks = 10
)

// setupGUI wires the manager and adopts apps that survived a hub restart.
func (s *Server) setupGUI() {
	s.gui = gui.New(gui.Config{
		Dir:     s.st.Path("gui"),
		Publish: s.events.Publish,
		Path:    s.machines.Local().LoginPath,
	})
	s.gui.Start()
}

func (s *Server) routesGUI(authed func(string, handler)) {
	authed("GET /api/gui/apps", s.handleGUIApps)
	authed("GET /api/gui/apps/{id}/icon", s.handleGUIIcon)
	authed("GET /api/gui/sessions", s.handleGUISessions)
	authed("POST /api/gui/sessions", s.handleGUILaunch)
	authed("POST /api/gui/browser/open", s.handleGUIBrowserOpen)
	authed("DELETE /api/gui/sessions/{id}", s.handleGUIStop)
	authed("GET /api/gui/sessions/{id}/ws", s.handleGUIWS)
	authed("GET /api/gui/sessions/{id}/video", s.handleGUIVideo)
	authed("GET /api/gui/sessions/{id}/audio", s.handleGUIAudio)
}

// guiErr writes a manager error as {"error","code"[,"session"]}.
func guiErr(w http.ResponseWriter, err error) {
	var ge *gui.Error
	if errors.As(err, &ge) {
		body := map[string]any{"error": ge.Msg, "code": ge.Code}
		if ge.Session != nil {
			body["session"] = ge.Session
		}
		writeJSON(w, ge.Status, body)
		return
	}
	writeErrCode(w, http.StatusInternalServerError, "gui_failed", err.Error())
}

func (s *Server) handleGUIApps(w http.ResponseWriter, r *http.Request, p *principal) {
	st := s.gui.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"available":   st.Available,
		"missing":     st.Missing,
		"hint":        st.Hint,
		"display":     st.Display,
		"gpu":         st.GPU,
		"video":       st.Video,
		"videoCodecs": st.Codecs,
		"audio":       st.Audio,
		"apps":        s.gui.Apps(),
	})
}

func (s *Server) handleGUIIcon(w http.ResponseWriter, r *http.Request, p *principal) {
	path := s.gui.IconPath(r.PathValue("id"))
	if path == "" {
		writeErrCode(w, http.StatusNotFound, "icon_not_found", "icon not found")
		return
	}
	hd := w.Header()
	if strings.EqualFold(filepath.Ext(path), ".svg") {
		hd.Set("Content-Type", "image/svg+xml")
	} else {
		hd.Set("Content-Type", "image/png")
	}
	hd.Set("Cache-Control", "private, max-age=86400")
	hd.Set("Content-Security-Policy", "sandbox")
	http.ServeFile(w, r, path)
}

func (s *Server) handleGUISessions(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.gui.List())
}

func (s *Server) handleGUILaunch(w http.ResponseWriter, r *http.Request, p *principal) {
	var req gui.LaunchRequest
	if !decode(w, r, &req) {
		return
	}
	sess, err := s.gui.Launch(req)
	if err != nil {
		guiErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

// handleGUIBrowserOpen shows a web page in the browser of This PC: 201 when
// that started the browser, 200 when the page became a tab of the open one.
func (s *Server) handleGUIBrowserOpen(w http.ResponseWriter, r *http.Request, p *principal) {
	var req gui.OpenURLRequest
	if !decode(w, r, &req) {
		return
	}
	opened, err := s.gui.OpenURL(req)
	if err != nil {
		guiErr(w, err)
		return
	}
	status := http.StatusCreated
	if opened.NewTab {
		status = http.StatusOK
	}
	writeJSON(w, status, opened)
}

func (s *Server) handleGUIStop(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.gui.Stop(r.PathValue("id")); err != nil {
		guiErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

// handleGUIWS bridges binary WebSocket frames to the session's VNC socket;
// the RFB protocol itself runs between the server and the tile.
func (s *Server) handleGUIWS(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.checkOrigin(r, p) {
		writeErr(w, http.StatusForbidden, "origin not allowed")
		return
	}
	vnc, err := s.gui.Dial(r.PathValue("id"))
	if err != nil {
		guiErr(w, err)
		return
	}
	defer vnc.Close()
	rfbBytes, err := s.gui.RFBCounter(r.PathValue("id"))
	if err != nil {
		guiErr(w, err)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	conn := newWSConn(c)
	unregister := s.conns.add(p.connKey(), conn.revoke)
	defer unregister()

	readerDone := make(chan struct{})
	go conn.pipeBinary(guiClientLimit, vnc, readerDone)

	frames := make(chan []byte, 8)
	go func() {
		defer close(frames)
		for {
			buf := make([]byte, wsMaxBatch)
			n, err := vnc.Read(buf)
			if n > 0 {
				rfbBytes.Add(int64(n))
				select {
				case frames <- buf[:n]:
				case <-readerDone:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(wsPingEvery)
	defer ping.Stop()
	for {
		select {
		case b, ok := <-frames:
			if !ok {
				conn.closeWith(websocket.CloseNormalClosure, "session ended")
				return
			}
			if conn.write(websocket.BinaryMessage, b) != nil {
				return
			}
		case <-ping.C:
			if conn.c.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)) != nil {
				return
			}
		case <-conn.kill:
			conn.closeWith(wsCloseRevok, "revoked")
			return
		case <-readerDone:
			return
		}
	}
}

// handleGUIVideo streams H.264 or AV1 from the session's display, one binary frame
// per picture with a flags byte in front (bit 0: decoding can start here). Text
// frames carry control messages. The viewer acknowledges every decoded picture
// with a message of its own, which lets videoPacer hold the encoder back when
// the link is full: that costs frame rate, not a growing delay.
//
// A still screen stops the encoder, as capturing it would cost a third of a
// core per open tile. RFB then shows the picture, so RFB traffic tells when the
// screen moves again. The new encoder starts with a key frame, which the
// viewer's decoder takes in stride on the same socket.
func (s *Server) handleGUIVideo(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.checkOrigin(r, p) {
		writeErr(w, http.StatusForbidden, "origin not allowed")
		return
	}
	id := r.PathValue("id")
	q := r.URL.Query()
	width, _ := strconv.Atoi(q.Get("w"))
	height, _ := strconv.Atoi(q.Get("h"))
	mbps, _ := strconv.ParseFloat(q.Get("mbps"), 64)
	rfbBytes, err := s.gui.RFBCounter(id)
	if err != nil {
		guiErr(w, err)
		return
	}
	st, err := s.gui.VideoStream(id, width, height, mbps, q.Get("codec"))
	if err != nil {
		guiErr(w, err)
		return
	}
	defer func() { st.Stop() }()
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	conn := newWSConn(c)
	unregister := s.conns.add(p.connKey(), conn.revoke)
	defer unregister()

	pacer := newVideoPacer()
	readerDone := make(chan struct{})
	go conn.pipeBinary(guiAckLimit, writerFunc(func(b []byte) (int, error) {
		pacer.acked()
		return len(b), nil
	}), readerDone)

	hello, _ := json.Marshal(map[string]any{"t": "hello", "mbps": st.Mbps, "codec": st.Codec})
	if conn.write(websocket.TextMessage, hello) != nil {
		return
	}

	type outgoing struct {
		typ int
		b   []byte
	}
	frames := make(chan outgoing, 2)
	motionBytes := max(guiMotionMinBytes, int(st.Mbps*1e6*guiMotionShare/8/60))
	var lastMotion atomic.Int64
	lastMotion.Store(time.Now().UnixMilli())
	ended := make(chan *gui.Stream, 1)
	pump := func(st *gui.Stream) {
		_ = st.Pictures(func(au []byte, key bool) error {
			if len(au) > motionBytes {
				lastMotion.Store(time.Now().UnixMilli())
			}
			if pacer.wait(readerDone) {
				select {
				case frames <- outgoing{websocket.TextMessage, []byte(`{"t":"slow"}`)}:
				case <-readerDone:
					return io.EOF
				}
			}
			flags := byte(0)
			if key {
				flags = 1
			}
			pacer.sent()
			select {
			case frames <- outgoing{websocket.BinaryMessage, append([]byte{flags}, au...)}:
			case <-readerDone:
				return io.EOF
			}
			return nil
		})
		select {
		case ended <- st:
		case <-readerDone:
		}
	}
	go pump(st)

	sleeping := false
	var rfbSeen [guiWakeTicks]int64
	tick := 0
	idle := time.NewTicker(guiVideoIdleTick)
	defer idle.Stop()

	ping := time.NewTicker(wsPingEvery)
	defer ping.Stop()
	for {
		select {
		case m := <-frames:
			if conn.write(m.typ, m.b) != nil {
				return
			}
		case gone := <-ended:
			// Only an encoder that wasn't put to sleep ends the stream, for
			// example because the display got smaller than its frame.
			if gone == st && !sleeping {
				conn.closeWith(websocket.CloseNormalClosure, "stream ended")
				return
			}
		case <-idle.C:
			now := rfbBytes.Load()
			oldest := rfbSeen[tick%guiWakeTicks]
			rfbSeen[tick%guiWakeTicks] = now
			tick++
			if sleeping && now-oldest > guiWakeBytes {
				next, err := s.gui.VideoStream(id, width, height, st.Mbps, st.Codec)
				if err != nil {
					conn.closeWith(websocket.CloseTryAgainLater, "no encoder")
					return
				}
				st = next
				sleeping = false
				lastMotion.Store(time.Now().UnixMilli())
				go pump(st)
			} else if !sleeping && time.Now().UnixMilli()-lastMotion.Load() > guiVideoSleepAfter.Milliseconds() {
				sleeping = true
				st.Stop()
				// What came before the sleep doesn't count as waking up.
				for i := range rfbSeen {
					rfbSeen[i] = now
				}
			}
		case <-ping.C:
			if conn.c.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)) != nil {
				return
			}
		case <-conn.kill:
			conn.closeWith(wsCloseRevok, "revoked")
			return
		case <-readerDone:
			return
		}
	}
}

// videoPacer limits what may be on its way to the viewer or in its decoder by
// time instead of by count: a fixed number of pictures is a third of a second
// at 60 fps, but two seconds at 10. The fastest answer seen is the link's own
// delay. Anything beyond it is queueing, of which guiVideoQueue is allowed.
type videoPacer struct {
	mu      sync.Mutex
	pending []time.Time
	minAge  time.Duration
	changed chan struct{}

	windowStart time.Time
	waited      time.Duration
	lastSlow    time.Time
}

func newVideoPacer() *videoPacer {
	return &videoPacer{changed: make(chan struct{}, 1), windowStart: time.Now()}
}

func (p *videoPacer) sent() {
	p.mu.Lock()
	p.pending = append(p.pending, time.Now())
	p.mu.Unlock()
}

func (p *videoPacer) acked() {
	p.mu.Lock()
	if len(p.pending) > 0 {
		age := time.Since(p.pending[0])
		p.pending = p.pending[1:]
		switch {
		case p.minAge == 0 || age < p.minAge:
			p.minAge = age
		default:
			// Slowly upward, so a link that got slower isn't held to its past.
			p.minAge += (age - p.minAge) / 256
		}
	}
	p.mu.Unlock()
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

func (p *videoPacer) ready() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pending) == 0 {
		return true
	}
	if len(p.pending) >= guiVideoMaxPending {
		return false
	}
	limit := guiVideoStartQueue
	if p.minAge > 0 {
		limit = p.minAge + guiVideoQueue
	}
	return time.Since(p.pending[0]) < limit
}

// wait blocks until the next picture may go. It reports true when the viewer
// should ask for a lower rate: this waiting took most of the last while.
func (p *videoPacer) wait(done <-chan struct{}) bool {
	start := time.Now()
	for !p.ready() {
		select {
		case <-p.changed:
		case <-done:
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
	now := time.Now()
	p.waited += now.Sub(start)
	if now.Sub(p.windowStart) < guiVideoSlowWindow {
		return false
	}
	slow := p.waited > guiVideoSlowWindow/2 && now.Sub(p.lastSlow) > guiVideoSlowEvery
	if slow {
		p.lastSlow = now
	}
	p.windowStart, p.waited = now, 0
	return slow
}

// handleGUIAudio streams the session's sound as raw PCM, one binary frame per
// 20 ms. Silence is left out, so a quiet app costs no bandwidth, and on a slow
// link frames are dropped: late sound is worse than a short gap.
func (s *Server) handleGUIAudio(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.checkOrigin(r, p) {
		writeErr(w, http.StatusForbidden, "origin not allowed")
		return
	}
	st, err := s.gui.AudioStream(r.PathValue("id"))
	if err != nil {
		guiErr(w, err)
		return
	}
	defer st.Stop()
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	conn := newWSConn(c)
	unregister := s.conns.add(p.connKey(), conn.revoke)
	defer unregister()

	readerDone := make(chan struct{})
	go conn.pipeBinary(guiAckLimit, io.Discard, readerDone)

	frames := make(chan []byte, 8)
	go func() {
		defer close(frames)
		for {
			buf := make([]byte, gui.AudioFrameBytes)
			if _, err := io.ReadFull(st.Out, buf); err != nil {
				return
			}
			if isSilence(buf) {
				continue
			}
			select {
			case frames <- buf:
			case <-readerDone:
				return
			default:
			}
		}
	}()

	ping := time.NewTicker(wsPingEvery)
	defer ping.Stop()
	for {
		select {
		case b, ok := <-frames:
			if !ok {
				conn.closeWith(websocket.CloseNormalClosure, "stream ended")
				return
			}
			if conn.write(websocket.BinaryMessage, b) != nil {
				return
			}
		case <-ping.C:
			if conn.c.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)) != nil {
				return
			}
		case <-conn.kill:
			conn.closeWith(wsCloseRevok, "revoked")
			return
		case <-readerDone:
			return
		}
	}
}

func isSilence(pcm []byte) bool {
	for _, b := range pcm {
		if b != 0 {
			return false
		}
	}
	return true
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// pipeBinary is readLoop for binary streams: frames go to dst as they arrive.
func (w *wsConn) pipeBinary(limit int64, dst io.Writer, done chan struct{}) {
	defer close(done)
	w.c.SetReadLimit(limit)
	_ = w.c.SetReadDeadline(time.Now().Add(wsReadWait))
	w.c.SetPongHandler(func(string) error {
		return w.c.SetReadDeadline(time.Now().Add(wsReadWait))
	})
	for {
		typ, b, err := w.c.ReadMessage()
		if err != nil {
			return
		}
		_ = w.c.SetReadDeadline(time.Now().Add(wsReadWait))
		if typ == websocket.BinaryMessage {
			if _, err := dst.Write(b); err != nil {
				return
			}
		}
	}
}
