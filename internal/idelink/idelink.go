// Package idelink makes the editor card the IDE of Claude Code consoles
// (docs/API-agents.md §24). The CLI looks for an "ide" MCP server: a WebSocket on
// loopback, named by CLAUDE_CODE_SSE_PORT, whose token is in a lock file below its
// config folder. Each console gets its own server, so a request is known to come
// from that console; what the servers say about the editor comes from the browser
// showing the workspace's editor card.
package idelink

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	ideName = "NextBoard"
	// tokenHeader is how the CLI proves it read the lock file.
	tokenHeader = "X-Claude-Code-Ide-Authorization"
	maxFrame    = 64 << 20
	writeWait   = 10 * time.Second
	// askTimeout bounds what the browser answers at once. openDiff waits for a person.
	askTimeout = 8 * time.Second
)

// Options connects the links to the hub's sessions.
type Options struct {
	// WorkspaceOf and NameOf look a console up at the time of use: it can move.
	WorkspaceOf func(sessionID string) string
	NameOf      func(sessionID string) string
	Version     string
}

// Hub holds every console's server and every open editor.
type Hub struct {
	o      Options
	mu     sync.Mutex
	links  map[string]*link
	spaces map[string]*space
	nextID int
	closed bool
}

// New returns an empty hub.
func New(o Options) *Hub {
	return &Hub{o: o, links: map[string]*link{}, spaces: map[string]*space{}}
}

// space is what is known about one workspace's editor.
type space struct {
	// editors are the browsers showing it, the most recently used last.
	editors   []*Editor
	selection json.RawMessage // now, null when no editor shows a selection
	latest    json.RawMessage // the last one that was not empty
	tabs      json.RawMessage
}

func (h *Hub) spaceOf(workspace string) *space {
	sp := h.spaces[workspace]
	if sp == nil {
		sp = &space{}
		h.spaces[workspace] = sp
	}
	return sp
}

// link is the server of one console.
type link struct {
	hub     *Hub
	session string
	port    int
	token   string
	ln      net.Listener
	srv     *http.Server
	roots   []string
	locks   map[string]bool
	conns   map[*cliConn]struct{}
}

// Env starts the console's server if needed, writes its lock file below the
// console's config folder, and returns what to add to the console's environment.
func (h *Hub) Env(sessionID string, roots []string, configDir string) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("hub berhenti")
	}
	l := h.links[sessionID]
	if l == nil {
		var err error
		if l, err = h.startLink(sessionID); err != nil {
			return nil, err
		}
		h.links[sessionID] = l
	}
	l.roots = roots
	if err := l.writeLock(configDir); err != nil {
		return nil, err
	}
	return []string{
		"CLAUDE_CODE_SSE_PORT=" + strconv.Itoa(l.port),
		"ENABLE_IDE_INTEGRATION=true",
		// without this the CLI tries to install its extension in an IDE it finds
		"CLAUDE_CODE_IDE_SKIP_AUTO_INSTALL=1",
	}, nil
}

func (h *Hub) startLink(sessionID string) (*link, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l := &link{hub: h, session: sessionID, port: ln.Addr().(*net.TCPAddr).Port, token: hex.EncodeToString(token),
		ln: ln, locks: map[string]bool{}, conns: map[*cliConn]struct{}{}}
	l.srv = &http.Server{Handler: http.HandlerFunc(l.serveHTTP), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = l.srv.Serve(ln) }()
	return l, nil
}

type lockFile struct {
	PID              int      `json:"pid"`
	WorkspaceFolders []string `json:"workspaceFolders"`
	IDEName          string   `json:"ideName"`
	Transport        string   `json:"transport"`
	RunningInWindows bool     `json:"runningInWindows"`
	AuthToken        string   `json:"authToken"`
}

// writeLock writes the lock with the folder and file modes the CLI's own server
// uses: whoever can read the token can act as this console's IDE.
func (l *link) writeLock(configDir string) error {
	dir := filepath.Join(configDir, "ide")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	pruneLocks(dir)
	b, err := json.Marshal(lockFile{PID: os.Getpid(), WorkspaceFolders: l.roots, IDEName: ideName, Transport: "ws", AuthToken: l.token})
	if err != nil {
		return err
	}
	file := filepath.Join(dir, strconv.Itoa(l.port)+".lock")
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	l.locks[file] = true
	return nil
}

// pruneLocks removes what an earlier hub left behind after a crash.
func pruneLocks(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		file := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var lf lockFile
		if json.Unmarshal(b, &lf) != nil || lf.IDEName != ideName || processAlive(lf.PID) {
			continue
		}
		_ = os.Remove(file)
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

// Release ends a console's server, when the console is deleted.
func (h *Hub) Release(sessionID string) {
	h.mu.Lock()
	l := h.links[sessionID]
	delete(h.links, sessionID)
	workspace := ""
	if l != nil {
		workspace = h.workspaceOf(sessionID)
	}
	h.mu.Unlock()
	if l != nil {
		l.stop()
		h.pushConsoles(workspace)
	}
}

func (l *link) stop() {
	_ = l.srv.Close() // takes the listener; upgraded connections are closed below
	l.hub.mu.Lock()
	conns := make([]*cliConn, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	files := make([]string, 0, len(l.locks))
	for f := range l.locks {
		files = append(files, f)
	}
	l.hub.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
	for _, f := range files {
		_ = os.Remove(f)
	}
}

// Close ends everything, when the hub stops.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	links := make([]*link, 0, len(h.links))
	for _, l := range h.links {
		links = append(links, l)
	}
	h.links = map[string]*link{}
	h.mu.Unlock()
	for _, l := range links {
		l.stop()
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	Subprotocols:    []string{"mcp"},
	// A page in a browser can open a socket to loopback, and it would say where it
	// comes from. The CLI sends no Origin: see serveHTTP.
	CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
}

func (l *link) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		http.Error(w, "origin", http.StatusForbidden)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(l.token)) != 1 {
		http.Error(w, "token", http.StatusUnauthorized)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cc := &cliConn{l: l, c: c, ctx: ctx, cancel: cancel}
	l.hub.mu.Lock()
	if l.hub.links[l.session] != l {
		l.hub.mu.Unlock()
		cc.close()
		return
	}
	l.conns[cc] = struct{}{}
	l.hub.mu.Unlock()
	cc.run()
	l.hub.mu.Lock()
	delete(l.conns, cc)
	workspace := l.hub.workspaceOf(l.session)
	l.hub.mu.Unlock()
	l.hub.pushConsoles(workspace)
}

// workspaceOf runs with h.mu held: the session side must never call this package
// while holding its own locks.
func (h *Hub) workspaceOf(sessionID string) string {
	if h.o.WorkspaceOf == nil {
		return ""
	}
	return h.o.WorkspaceOf(sessionID)
}

func (h *Hub) nameOf(sessionID string) string {
	if h.o.NameOf == nil {
		return sessionID
	}
	return h.o.NameOf(sessionID)
}

// ---- one CLI ------------------------------------------------------------------------

type cliConn struct {
	l      *link
	c      *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	wmu    sync.Mutex
	// ready: past the handshake, so that notifications can reach it.
	ready bool
}

func (c *cliConn) close() {
	c.cancel()
	_ = c.c.Close()
}

func (c *cliConn) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.c.SetWriteDeadline(time.Now().Add(writeWait))
	if c.c.WriteMessage(websocket.TextMessage, b) != nil {
		go c.close()
	}
}

func (c *cliConn) notify(method string, params json.RawMessage) {
	c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

type rpcIn struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (c *cliConn) run() {
	defer c.close()
	c.c.SetReadLimit(maxFrame)
	for {
		typ, b, err := c.c.ReadMessage()
		if err != nil {
			return
		}
		if typ != websocket.TextMessage {
			continue
		}
		var m rpcIn
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		c.handle(m)
	}
}

func (c *cliConn) reply(id json.RawMessage, result any) {
	c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *cliConn) handle(m rpcIn) {
	if m.Method == "" {
		return // the answer to something we never ask
	}
	if len(m.ID) == 0 {
		// ide_connected says the CLI is ready to hear about the editor
		if m.Method == "ide_connected" {
			c.connected()
		}
		return
	}
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		version := p.ProtocolVersion
		if version == "" {
			version = "2025-03-26"
		}
		c.reply(m.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "nextboard-ide", "version": c.l.hub.o.Version},
		})
	case "ping":
		c.reply(m.ID, map[string]any{})
	case "tools/list":
		c.reply(m.ID, map[string]any{"tools": toolList})
	case "prompts/list":
		c.reply(m.ID, map[string]any{"prompts": []any{}})
	case "resources/list":
		c.reply(m.ID, map[string]any{"resources": []any{}})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(m.Params, &p) != nil {
			c.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32602, "message": "invalid params"}})
			return
		}
		// openDiff waits for a person: the connection must keep working meanwhile
		go func() { c.reply(m.ID, c.l.hub.callTool(c.ctx, c.l, p.Name, p.Arguments)) }()
	default:
		c.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "method not found: " + m.Method}})
	}
}

func (c *cliConn) connected() {
	h := c.l.hub
	h.mu.Lock()
	c.ready = true
	workspace := h.workspaceOf(c.l.session)
	sel := h.spaceOf(workspace).selection
	h.mu.Unlock()
	if len(sel) > 0 {
		c.notify("selection_changed", sel)
	}
	h.pushConsoles(workspace)
}

// ---- tools --------------------------------------------------------------------------

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var str = map[string]any{"type": "string"}

var toolList = []map[string]any{
	{"name": "openFile", "description": "Open a file in the editor, and select text in it if given.", "inputSchema": schema(map[string]any{
		"filePath": str, "preview": map[string]any{"type": "boolean"}, "startText": str, "endText": str,
		"selectToEndOfLine": map[string]any{"type": "boolean"}, "makeFrontmost": map[string]any{"type": "boolean"}}, "filePath")},
	{"name": "openDiff", "description": "Show proposed changes to a file and wait until the user accepts or rejects them.", "inputSchema": schema(map[string]any{
		"old_file_path": str, "new_file_path": str, "new_file_contents": str, "tab_name": str}, "old_file_path", "new_file_path", "new_file_contents", "tab_name")},
	{"name": "close_tab", "description": "Close a diff opened with openDiff.", "inputSchema": schema(map[string]any{"tab_name": str}, "tab_name")},
	{"name": "closeAllDiffTabs", "description": "Close every diff opened with openDiff.", "inputSchema": schema(map[string]any{})},
	{"name": "getDiagnostics", "description": "Get the errors and warnings the editor knows about, for one file or for all.", "inputSchema": schema(map[string]any{"uri": str})},
	{"name": "getCurrentSelection", "description": "Get the text selected in the active editor.", "inputSchema": schema(map[string]any{})},
	{"name": "getLatestSelection", "description": "Get the most recent text selection.", "inputSchema": schema(map[string]any{})},
	{"name": "getOpenEditors", "description": "List the files open in the editor.", "inputSchema": schema(map[string]any{})},
	{"name": "getWorkspaceFolders", "description": "List the folders of the workspace.", "inputSchema": schema(map[string]any{})},
	{"name": "checkDocumentDirty", "description": "Say whether an open file has unsaved changes.", "inputSchema": schema(map[string]any{"filePath": str}, "filePath")},
	{"name": "saveDocument", "description": "Save an open file.", "inputSchema": schema(map[string]any{"filePath": str}, "filePath")},
}

// text is a tool result of plain text items.
func text(items ...string) map[string]any {
	content := make([]map[string]any, len(items))
	for i, t := range items {
		content[i] = map[string]any{"type": "text", "text": t}
	}
	return map[string]any{"content": content}
}

func jsonText(v any) map[string]any {
	b, _ := json.Marshal(v)
	return text(string(b))
}

func failure(msg string) map[string]any {
	r := text(msg)
	r["isError"] = true
	return r
}

// callTool answers what the hub knows itself and asks the browser for the rest.
func (h *Hub) callTool(ctx context.Context, l *link, name string, args json.RawMessage) any {
	h.mu.Lock()
	workspace := h.workspaceOf(l.session)
	sp := h.spaceOf(workspace)
	hasEditor := len(sp.editors) > 0
	selection, latest, tabs, roots := sp.selection, sp.latest, sp.tabs, l.roots
	h.mu.Unlock()
	switch name {
	case "getWorkspaceFolders":
		folders := make([]map[string]any, len(roots))
		root := ""
		for i, r := range roots {
			folders[i] = map[string]any{"name": filepath.Base(r), "uri": "file://" + r, "path": r}
			if i == 0 {
				root = r
			}
		}
		return jsonText(map[string]any{"success": true, "folders": folders, "rootPath": root})
	case "getCurrentSelection":
		if !hasEditor || !hasText(selection) {
			return jsonText(map[string]any{"success": false, "message": "No active editor found"})
		}
		return selectionResult(selection)
	case "getLatestSelection":
		if len(latest) == 0 {
			return jsonText(map[string]any{"success": false, "message": "No selection available"})
		}
		return selectionResult(latest)
	case "getOpenEditors":
		if !hasEditor || len(tabs) == 0 {
			return jsonText(map[string]any{"tabs": []any{}})
		}
		return jsonText(map[string]any{"tabs": tabs})
	case "openDiff":
		res, err := h.ask(ctx, workspace, l.session, name, args, 0)
		if err != nil {
			// the CLI then asks in the terminal, which works without an editor
			return failure("No editor is open to show this diff.")
		}
		return res
	}
	res, err := h.ask(ctx, workspace, l.session, name, args, askTimeout)
	if err == nil {
		return res
	}
	switch name {
	case "close_tab":
		return text("TAB_CLOSED")
	case "closeAllDiffTabs":
		return text("CLOSED_0_DIFF_TABS")
	case "getDiagnostics":
		return text("[]")
	case "checkDocumentDirty", "saveDocument":
		return jsonText(map[string]any{"success": false, "message": "Document not open"})
	case "openFile":
		return failure("No editor is open.")
	}
	return failure("Unknown tool: " + name)
}

func hasText(selection json.RawMessage) bool {
	var s struct {
		FilePath string `json:"filePath"`
	}
	return len(selection) > 0 && json.Unmarshal(selection, &s) == nil && s.FilePath != ""
}

// selectionResult is a selection as getCurrentSelection tells it.
func selectionResult(selection json.RawMessage) map[string]any {
	var m map[string]json.RawMessage
	if json.Unmarshal(selection, &m) != nil {
		return jsonText(map[string]any{"success": false, "message": "No selection available"})
	}
	m["success"] = json.RawMessage("true")
	delete(m, "fileUrl")
	return jsonText(m)
}

var errNoEditor = errors.New("no editor")

// ask sends a tool call to the browser most recently used, and waits for its answer.
// A timeout of 0 waits as long as the caller does.
func (h *Hub) ask(ctx context.Context, workspace, session, tool string, args json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	h.mu.Lock()
	sp := h.spaceOf(workspace)
	if len(sp.editors) == 0 {
		h.mu.Unlock()
		return nil, errNoEditor
	}
	e := sp.editors[len(sp.editors)-1]
	h.nextID++
	id := strconv.Itoa(h.nextID)
	ch := make(chan json.RawMessage, 1)
	e.pending[id] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(e.pending, id)
		h.mu.Unlock()
	}()
	e.push(map[string]any{"t": "call", "id": id, "tool": tool, "args": args, "session": map[string]any{"id": session, "name": h.nameOf(session)}})
	var after <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		after = t.C
	}
	select {
	case res, ok := <-ch:
		if !ok {
			return nil, errNoEditor
		}
		return res, nil
	case <-after:
		return nil, errNoEditor
	case <-ctx.Done():
		// the CLI went away: its diff has no reader any more
		e.push(map[string]any{"t": "cancel", "id": id})
		return nil, ctx.Err()
	}
}

// ---- the browser --------------------------------------------------------------------

// Editor is one browser showing a workspace's editor card.
type Editor struct {
	hub       *Hub
	workspace string
	send      func([]byte) error
	pending   map[string]chan json.RawMessage
}

// OpenEditor registers a browser; send must not block for long.
func (h *Hub) OpenEditor(workspace string, send func([]byte) error) *Editor {
	e := &Editor{hub: h, workspace: workspace, send: send, pending: map[string]chan json.RawMessage{}}
	h.mu.Lock()
	sp := h.spaceOf(workspace)
	sp.editors = append(sp.editors, e)
	h.mu.Unlock()
	h.pushConsoles(workspace)
	return e
}

func (e *Editor) push(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if err := e.send(b); err != nil {
		log.Printf("idelink: %v", err)
	}
}

// Close forgets a browser. A question it never answered is answered as if none was open.
func (e *Editor) Close() {
	h := e.hub
	h.mu.Lock()
	sp := h.spaceOf(e.workspace)
	for i, x := range sp.editors {
		if x == e {
			sp.editors = append(sp.editors[:i], sp.editors[i+1:]...)
			break
		}
	}
	if len(sp.editors) == 0 {
		sp.selection, sp.tabs = nil, nil
	}
	for id, ch := range e.pending {
		close(ch)
		delete(e.pending, id)
	}
	h.mu.Unlock()
}

// consoles is who is connected in a workspace.
func (h *Hub) consoles(workspace string) []map[string]string {
	h.mu.Lock()
	ids := []string{}
	for id, l := range h.links {
		if h.workspaceOf(id) != workspace {
			continue
		}
		for c := range l.conns {
			if c.ready {
				ids = append(ids, id)
				break
			}
		}
	}
	h.mu.Unlock()
	out := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]string{"id": id, "name": h.nameOf(id)})
	}
	return out
}

func (h *Hub) pushConsoles(workspace string) {
	if workspace == "" {
		return
	}
	items := h.consoles(workspace)
	h.mu.Lock()
	editors := append([]*Editor{}, h.spaceOf(workspace).editors...)
	h.mu.Unlock()
	for _, e := range editors {
		e.push(map[string]any{"t": "consoles", "items": items})
	}
}

// forEachCLI calls fn for the ready connections of a workspace, or of one console.
func (h *Hub) forEachCLI(workspace, session string, fn func(*cliConn)) {
	h.mu.Lock()
	var conns []*cliConn
	for id, l := range h.links {
		if (session != "" && id != session) || h.workspaceOf(id) != workspace {
			continue
		}
		for c := range l.conns {
			if c.ready {
				conns = append(conns, c)
			}
		}
	}
	h.mu.Unlock()
	for _, c := range conns {
		fn(c)
	}
}

type editorIn struct {
	T         string          `json:"t"`
	ID        string          `json:"id"`
	Result    json.RawMessage `json:"result"`
	Selection json.RawMessage `json:"selection"`
	Tabs      json.RawMessage `json:"tabs"`
	Session   string          `json:"session"`
	Mention   json.RawMessage `json:"mention"`
}

// Receive takes one message from the browser.
func (e *Editor) Receive(b []byte) {
	var m editorIn
	if json.Unmarshal(b, &m) != nil {
		return
	}
	h := e.hub
	switch m.T {
	case "reply":
		h.mu.Lock()
		ch := e.pending[m.ID]
		delete(e.pending, m.ID)
		h.mu.Unlock()
		if ch != nil {
			ch <- m.Result
		}
	case "selection":
		h.mu.Lock()
		sp := h.spaceOf(e.workspace)
		sp.touch(e)
		sp.selection = m.Selection
		if hasText(m.Selection) {
			sp.latest = m.Selection
		}
		h.mu.Unlock()
		h.forEachCLI(e.workspace, "", func(c *cliConn) { c.notify("selection_changed", m.Selection) })
	case "tabs":
		h.mu.Lock()
		h.spaceOf(e.workspace).tabs = m.Tabs
		h.mu.Unlock()
	case "mention":
		h.forEachCLI(e.workspace, m.Session, func(c *cliConn) { c.notify("at_mentioned", m.Mention) })
	}
}

// touch makes e the browser that answers from now on.
func (sp *space) touch(e *Editor) {
	for i, x := range sp.editors {
		if x == e {
			sp.editors = append(append(sp.editors[:i:i], sp.editors[i+1:]...), e)
			return
		}
	}
}
