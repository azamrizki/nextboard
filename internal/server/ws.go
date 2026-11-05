package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/session"
)

const (
	wsPingEvery  = 30 * time.Second
	wsReadWait   = 75 * time.Second
	wsWriteWait  = 10 * time.Second
	wsCloseRevok = 4401
	wsMaxBatch   = 64 << 10
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  16 << 10,
	WriteBufferSize: 32 << 10,
	// Origin is verified in checkOrigin before upgrading.
	CheckOrigin: func(*http.Request) bool { return true },
}

// checkOrigin enforces same-origin for cookie-authenticated WebSockets.
// Bearer-token clients (apps) don't send a browser Origin and are exempt.
func (s *Server) checkOrigin(r *http.Request, p *principal) bool {
	if p.Bearer {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	return s.dev && (u.Host == joinHostPort("localhost", s.devWeb) || u.Host == joinHostPort("127.0.0.1", s.devWeb))
}

// wsConn serialises every write (data, status, ping, close) through one
// goroutine, as gorilla/websocket allows a single concurrent writer.
type wsConn struct {
	c        *websocket.Conn
	killOnce sync.Once
	kill     chan struct{}
}

func newWSConn(c *websocket.Conn) *wsConn {
	return &wsConn{c: c, kill: make(chan struct{})}
}

func (w *wsConn) revoke() { w.killOnce.Do(func() { close(w.kill) }) }

func (w *wsConn) write(typ int, b []byte) error {
	_ = w.c.SetWriteDeadline(time.Now().Add(wsWriteWait))
	return w.c.WriteMessage(typ, b)
}

func (w *wsConn) closeWith(code int, reason string) {
	_ = w.c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
}

// readLoop handles pongs and passes text frames to fn until the peer goes
// away. done is closed on return.
func (w *wsConn) readLoop(limit int64, fn func([]byte), done chan struct{}) {
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
		if typ == websocket.TextMessage && fn != nil {
			fn(b)
		}
	}
}

type termIn struct {
	T string `json:"t"`
	D string `json:"d"`
	C uint16 `json:"c"`
	R uint16 `json:"r"`
}

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request, p *principal) {
	sess, ok := s.sessions.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, session.ErrNotFound.Error())
		return
	}
	if !s.checkOrigin(r, p) {
		writeErr(w, http.StatusForbidden, "origin tidak diizinkan")
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

	snapshot, sub := sess.Subscribe()
	defer sess.Unsubscribe(sub)

	readerDone := make(chan struct{})
	go conn.readLoop(64<<10, func(b []byte) {
		var m termIn
		if json.Unmarshal(b, &m) != nil {
			return
		}
		switch m.T {
		case "i":
			_ = s.sessionInput(r.PathValue("id"), sess, []byte(m.D))
		case "r":
			if m.C > 0 && m.R > 0 {
				sess.Resize(m.C, m.R)
			}
		}
	}, readerDone)

	// First binary frame is always the scrollback snapshot (maybe empty).
	if conn.write(websocket.BinaryMessage, snapshot) != nil {
		return
	}
	ping := time.NewTicker(wsPingEvery)
	defer ping.Stop()
	for {
		select {
		case msg, ok := <-sub.C:
			if !ok {
				// Dropped as a slow subscriber or session deleted.
				conn.closeWith(websocket.CloseTryAgainLater, "resubscribe")
				return
			}
			if msg.Status != nil {
				b, _ := json.Marshal(statusFrame(msg.Status))
				if conn.write(websocket.TextMessage, b) != nil {
					return
				}
				continue
			}
			// Coalesce queued output into one frame. Data slices are shared
			// between subscribers, so copy before appending.
			buf := append(make([]byte, 0, len(msg.Data)), msg.Data...)
		drain:
			for len(buf) < wsMaxBatch {
				select {
				case next, ok := <-sub.C:
					if !ok {
						break drain
					}
					if next.Status != nil {
						if conn.write(websocket.BinaryMessage, buf) != nil {
							return
						}
						buf = nil
						b, _ := json.Marshal(statusFrame(next.Status))
						if conn.write(websocket.TextMessage, b) != nil {
							return
						}
						break drain
					}
					buf = append(buf, next.Data...)
				default:
					break drain
				}
			}
			if len(buf) > 0 && conn.write(websocket.BinaryMessage, buf) != nil {
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

// statusFrame is the terminal WS status frame; the phase 1–4 fields keep
// their meaning, agent fields are added (docs/API-agents.md §7).
func statusFrame(st *session.StatusMsg) map[string]any {
	return map[string]any{"t": "status", "status": st.Status, "exitCode": st.ExitCode, "busy": st.Busy,
		"agentState": st.AgentState, "activity": st.Activity, "needsYouSince": st.NeedsYouSince, "foreground": st.Foreground, "job": st.Job, "active": st.Active}
}

// snapshotKeys maps event topics to the snapshot fields they cover.
var snapshotKeys = map[string]string{"session": "sessions", "machine": "machines", "device": "devices", "project": "projects", "projectGroups": "projectGroups"}

func (s *Server) handleEventsWS(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.checkOrigin(r, p) {
		writeErr(w, http.StatusForbidden, "origin tidak diizinkan")
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	conn := newWSConn(c)
	var topics []string
	if t := r.URL.Query().Get("topics"); t != "" {
		for _, x := range strings.Split(t, ",") {
			if x = strings.TrimSpace(x); x != "" {
				topics = append(topics, x)
			}
		}
	}
	sub := s.events.Subscribe(topics...)
	defer s.events.Unsubscribe(sub)
	unregister := s.conns.add(p.connKey(), conn.revoke)
	defer unregister()

	readerDone := make(chan struct{})
	go conn.readLoop(4<<10, nil, readerDone)

	full := map[string]any{
		"type": "snapshot", "sessions": s.sessions.List(), "machines": s.machines.List(), "devices": s.deviceList(),
		"projects": s.projectViews(), "projectGroups": s.projects.Groups(),
	}
	if len(topics) > 0 {
		keep := map[string]bool{}
		for _, t := range topics {
			keep[snapshotKeys[t]] = true
		}
		for k := range full {
			if k != "type" && !keep[k] {
				delete(full, k)
			}
		}
	}
	snap, _ := json.Marshal(full)
	if conn.write(websocket.TextMessage, snap) != nil {
		return
	}
	ping := time.NewTicker(wsPingEvery)
	defer ping.Stop()
	for {
		select {
		case msg, ok := <-sub.C:
			if !ok {
				conn.closeWith(websocket.CloseTryAgainLater, "resubscribe")
				return
			}
			if conn.write(websocket.TextMessage, msg) != nil {
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
