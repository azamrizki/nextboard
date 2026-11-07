package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/session"
)

// The editor card's side of the link to Claude consoles (docs/API-agents.md §24).

// ideMaxFrame is more than a proposed file and its edits as JSON.
const ideMaxFrame = 64 << 20

// ideEnv is what a console gets at launch: its folder and the project's are the
// workspace folders the CLI compares with where it runs.
func (s *Server) ideEnv(sp session.Spec, configDir string) ([]string, error) {
	roots := []string{sp.Cwd}
	if _, pr, ok := s.projects.Workspace(sp.WorkspaceID); ok && pr.Path != "" && pr.Path != sp.Cwd {
		roots = append(roots, pr.Path)
	}
	return s.ide.Env(sp.ID, roots, configDir)
}

func (s *Server) ideConsoleName(id string) string {
	sess, ok := s.sessions.Get(id)
	if !ok {
		return ""
	}
	sp := sess.Spec()
	if sp.Handle != "" {
		return "@" + sp.Handle
	}
	return sp.Name
}

func (s *Server) handleIDESocket(w http.ResponseWriter, r *http.Request, p *principal) {
	ws, _, ok := s.workspace(w, r)
	if !ok {
		return
	}
	if !s.checkOrigin(r, p) {
		writeErr(w, http.StatusForbidden, "origin not allowed")
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

	// one writer, as a websocket allows: everything for the browser passes here
	out := make(chan []byte, 256)
	gone := make(chan struct{})
	defer close(gone)
	editor := s.ide.OpenEditor(ws.ID, func(b []byte) error {
		select {
		case out <- b:
			return nil
		case <-gone:
			return nil
		default:
			conn.revoke()
			return errors.New("the browser editor fell behind")
		}
	})
	defer editor.Close()

	readerDone := make(chan struct{})
	go conn.readLoop(ideMaxFrame, editor.Receive, readerDone)
	ping := time.NewTicker(wsPingEvery)
	defer ping.Stop()
	for {
		select {
		case b := <-out:
			if conn.write(websocket.TextMessage, b) != nil {
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
