package idelink

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newHub(t *testing.T) (*Hub, *sync.Map) {
	t.Helper()
	workspaces := &sync.Map{}
	h := New(Options{
		WorkspaceOf: func(id string) string {
			if v, ok := workspaces.Load(id); ok {
				return v.(string)
			}
			return ""
		},
		NameOf:  func(id string) string { return "@" + id },
		Version: "test",
	})
	t.Cleanup(h.Close)
	return h, workspaces
}

// fakeCLI connects the way the Claude CLI does.
type fakeCLI struct {
	t  *testing.T
	c  *websocket.Conn
	mu sync.Mutex
	in chan map[string]any
	id int
}

func envPort(t *testing.T, env []string) int {
	t.Helper()
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "CLAUDE_CODE_SSE_PORT="); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatalf("no port in %v", env)
	return 0
}

func readLock(t *testing.T, cfg string, port int) lockFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfg, "ide", strconv.Itoa(port)+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	var lf lockFile
	if err := json.Unmarshal(b, &lf); err != nil {
		t.Fatal(err)
	}
	return lf
}

func dial(port int, hdr http.Header) (*websocket.Conn, *http.Response, error) {
	return websocket.DefaultDialer.Dial("ws://127.0.0.1:"+strconv.Itoa(port)+"/", hdr)
}

func connectCLI(t *testing.T, cfg string, port int) *fakeCLI {
	t.Helper()
	lf := readLock(t, cfg, port)
	c, _, err := dial(port, http.Header{tokenHeader: {lf.AuthToken}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeCLI{t: t, c: c, in: make(chan map[string]any, 64)}
	t.Cleanup(func() { _ = c.Close() })
	go func() {
		defer close(f.in)
		for {
			var m map[string]any
			if c.ReadJSON(&m) != nil {
				return
			}
			f.in <- m
		}
	}()
	return f
}

func (f *fakeCLI) send(m map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m["jsonrpc"] = "2.0"
	if err := f.c.WriteJSON(m); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeCLI) next() map[string]any {
	f.t.Helper()
	select {
	case m, ok := <-f.in:
		if !ok {
			f.t.Fatal("connection closed")
		}
		return m
	case <-time.After(5 * time.Second):
		f.t.Fatal("nothing arrived")
	}
	return nil
}

func (f *fakeCLI) request(method string, params any) map[string]any {
	f.t.Helper()
	f.id++
	id := f.id
	f.send(map[string]any{"id": id, "method": method, "params": params})
	for {
		m := f.next()
		if m["id"] == float64(id) {
			return m
		}
	}
}

func (f *fakeCLI) handshake() {
	f.t.Helper()
	res := f.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "clientInfo": map[string]any{"name": "claude-code"}})
	if got := res["result"].(map[string]any)["protocolVersion"]; got != "2025-11-25" {
		f.t.Fatalf("version %v", got)
	}
	f.send(map[string]any{"method": "notifications/initialized"})
	f.send(map[string]any{"method": "ide_connected", "params": map[string]any{"pid": 1}})
}

func toolText(t *testing.T, res map[string]any) []string {
	t.Helper()
	r, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("%v", res)
	}
	var out []string
	for _, c := range r["content"].([]any) {
		out = append(out, c.(map[string]any)["text"].(string))
	}
	return out
}

func (f *fakeCLI) tool(name string, args map[string]any) []string {
	f.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	return toolText(f.t, f.request("tools/call", map[string]any{"name": name, "arguments": args}))
}

// fakeBrowser stands in for the editor card.
type fakeBrowser struct {
	e  *Editor
	in chan map[string]any
}

func openBrowser(h *Hub, workspace string) *fakeBrowser {
	b := &fakeBrowser{in: make(chan map[string]any, 64)}
	b.e = h.OpenEditor(workspace, func(raw []byte) error {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		b.in <- m
		return nil
	})
	return b
}

func (b *fakeBrowser) nextOf(t *testing.T, kind string) map[string]any {
	t.Helper()
	for {
		select {
		case m := <-b.in:
			if m["t"] == kind {
				return m
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no %s", kind)
		}
	}
}

func (b *fakeBrowser) answer(id string, result any) {
	raw, _ := json.Marshal(map[string]any{"t": "reply", "id": id, "result": result})
	b.e.Receive(raw)
}

func TestLockFileAndWhoMayConnect(t *testing.T) {
	h, ws := newHub(t)
	ws.Store("s1", "w1")
	cfg := t.TempDir()
	env, err := h.Env("s1", []string{"/proj"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "ENABLE_IDE_INTEGRATION=true") || !strings.Contains(joined, "CLAUDE_CODE_IDE_SKIP_AUTO_INSTALL=1") {
		t.Fatal(env)
	}
	port := envPort(t, env)
	dir := filepath.Join(cfg, "ide")
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("%v %v", st.Mode(), err)
	}
	file := filepath.Join(dir, strconv.Itoa(port)+".lock")
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st.Mode(), err)
	}
	lf := readLock(t, cfg, port)
	if lf.PID != os.Getpid() || lf.IDEName != "NextBoard" || lf.Transport != "ws" || len(lf.AuthToken) != 32 || lf.WorkspaceFolders[0] != "/proj" {
		t.Fatalf("%+v", lf)
	}
	if _, res, err := dial(port, nil); err == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %v", err)
	}
	if _, res, err := dial(port, http.Header{tokenHeader: {"wrong"}}); err == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: %v", err)
	}
	// the right token from a web page still does not get in
	if _, res, err := dial(port, http.Header{tokenHeader: {lf.AuthToken}, "Origin": {"http://evil.example"}}); err == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("origin: %v", err)
	}
	c, _, err := dial(port, http.Header{tokenHeader: {lf.AuthToken}})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	// launching again keeps the server; a second profile gets its own copy of the lock
	cfg2 := t.TempDir()
	env2, err := h.Env("s1", []string{"/proj"}, cfg2)
	if err != nil || envPort(t, env2) != port {
		t.Fatalf("%v %v", env2, err)
	}
	if readLock(t, cfg2, port).AuthToken != lf.AuthToken {
		t.Fatal("token differs")
	}
	h.Release("s1")
	for _, f := range []string{file, filepath.Join(cfg2, "ide", strconv.Itoa(port)+".lock")} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if _, _, err := dial(port, http.Header{tokenHeader: {lf.AuthToken}}); err == nil {
		t.Fatal("still listening")
	}
}

func TestLeftoversOfACrashAreRemoved(t *testing.T) {
	h, _ := newHub(t)
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "ide")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dead, _ := json.Marshal(lockFile{PID: 1 << 30, IDEName: ideName})
	other, _ := json.Marshal(lockFile{PID: 1 << 30, IDEName: "Visual Studio Code"})
	alive, _ := json.Marshal(lockFile{PID: os.Getpid(), IDEName: ideName})
	for name, b := range map[string][]byte{"1.lock": dead, "2.lock": other, "3.lock": alive} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Env("s1", nil, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.lock")); !os.IsNotExist(err) {
		t.Fatal("a dead hub's lock stayed")
	}
	for _, n := range []string{"2.lock", "3.lock"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
}

func TestHandshakeAndToolsWithoutAnEditor(t *testing.T) {
	h, ws := newHub(t)
	ws.Store("s1", "w1")
	cfg := t.TempDir()
	env, _ := h.Env("s1", []string{"/proj/a"}, cfg)
	cli := connectCLI(t, cfg, envPort(t, env))
	cli.handshake()
	res := cli.request("tools/list", map[string]any{})
	names := map[string]bool{}
	for _, x := range res["result"].(map[string]any)["tools"].([]any) {
		m := x.(map[string]any)
		names[m["name"].(string)] = true
		if m["inputSchema"] == nil {
			t.Fatalf("%v", m)
		}
	}
	for _, n := range []string{"openDiff", "close_tab", "getDiagnostics", "openFile", "getCurrentSelection", "saveDocument"} {
		if !names[n] {
			t.Fatalf("%s missing from %v", n, names)
		}
	}
	if m := cli.request("no/such", nil); m["error"] == nil {
		t.Fatalf("%v", m)
	}
	if got := cli.tool("getWorkspaceFolders", nil)[0]; !strings.Contains(got, `"rootPath":"/proj/a"`) {
		t.Fatal(got)
	}
	if got := cli.tool("getCurrentSelection", nil)[0]; !strings.Contains(got, "No active editor found") {
		t.Fatal(got)
	}
	if got := cli.tool("getLatestSelection", nil)[0]; !strings.Contains(got, "No selection available") {
		t.Fatal(got)
	}
	if got := cli.tool("getDiagnostics", nil)[0]; got != "[]" {
		t.Fatal(got)
	}
	if got := cli.tool("close_tab", map[string]any{"tab_name": "x"})[0]; got != "TAB_CLOSED" {
		t.Fatal(got)
	}
	if got := cli.tool("closeAllDiffTabs", nil)[0]; got != "CLOSED_0_DIFF_TABS" {
		t.Fatal(got)
	}
	if got := cli.tool("saveDocument", map[string]any{"filePath": "/x"})[0]; !strings.Contains(got, "Document not open") {
		t.Fatal(got)
	}
	// no editor: the CLI must get an error at once, and then asks in the terminal
	m := cli.request("tools/call", map[string]any{"name": "openDiff", "arguments": map[string]any{"old_file_path": "/a", "new_file_path": "/a", "new_file_contents": "x", "tab_name": "t"}})
	if m["result"].(map[string]any)["isError"] != true {
		t.Fatalf("%v", m)
	}
}

func TestDiffWaitsForAPersonWhileTheConnectionKeepsWorking(t *testing.T) {
	h, ws := newHub(t)
	ws.Store("s1", "w1")
	cfg := t.TempDir()
	env, _ := h.Env("s1", nil, cfg)
	b := openBrowser(h, "w1")
	defer b.e.Close()
	cli := connectCLI(t, cfg, envPort(t, env))
	cli.handshake()
	if got := b.nextOf(t, "consoles")["items"]; got == nil {
		t.Fatal("no consoles message")
	}

	done := make(chan []string, 1)
	cli.send(map[string]any{"id": 777, "method": "tools/call", "params": map[string]any{"name": "openDiff", "arguments": map[string]any{"old_file_path": "/a", "new_file_path": "/a", "new_file_contents": "new", "tab_name": "t"}}})
	call := b.nextOf(t, "call")
	if call["tool"] != "openDiff" || call["session"].(map[string]any)["name"] != "@s1" || call["args"].(map[string]any)["new_file_contents"] != "new" {
		t.Fatalf("%v", call)
	}
	// while it waits the CLI asks something else
	go func() {
		c := b.nextOf(t, "call")
		b.answer(c["id"].(string), map[string]any{"content": []any{map[string]any{"type": "text", "text": "[]"}}})
	}()
	f := cli
	f.id = 1000
	if got := cli.tool("getDiagnostics", nil)[0]; got != "[]" {
		t.Fatal(got)
	}
	go func() {
		for m := range cli.in {
			if m["id"] == float64(777) {
				done <- toolText(t, m)
				return
			}
		}
	}()
	select {
	case got := <-done:
		t.Fatalf("answered by itself: %v", got)
	case <-time.After(300 * time.Millisecond):
	}
	b.answer(call["id"].(string), map[string]any{"content": []any{map[string]any{"type": "text", "text": "FILE_SAVED"}, map[string]any{"type": "text", "text": "edited"}}})
	select {
	case got := <-done:
		if len(got) != 2 || got[0] != "FILE_SAVED" || got[1] != "edited" {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}
}

func TestDiffEndsWhenEitherSideGoes(t *testing.T) {
	h, ws := newHub(t)
	ws.Store("s1", "w1")
	cfg := t.TempDir()
	env, _ := h.Env("s1", nil, cfg)
	b := openBrowser(h, "w1")
	cli := connectCLI(t, cfg, envPort(t, env))
	cli.handshake()
	args := map[string]any{"old_file_path": "/a", "new_file_path": "/a", "new_file_contents": "x", "tab_name": "t"}
	// the browser goes away: the CLI hears at once, and asks in the terminal instead
	cli.send(map[string]any{"id": 900, "method": "tools/call", "params": map[string]any{"name": "openDiff", "arguments": args}})
	b.nextOf(t, "call")
	b.e.Close()
	for {
		m := cli.next()
		if m["id"] == float64(900) {
			if m["result"].(map[string]any)["isError"] != true {
				t.Fatalf("%v", m)
			}
			break
		}
	}
	// the CLI goes away: the browser is told to drop the diff
	b2 := openBrowser(h, "w1")
	defer b2.e.Close()
	cli.send(map[string]any{"id": 901, "method": "tools/call", "params": map[string]any{"name": "openDiff", "arguments": args}})
	call := b2.nextOf(t, "call")
	_ = cli.c.Close()
	if got := b2.nextOf(t, "cancel"); got["id"] != call["id"] {
		t.Fatalf("%v %v", got, call)
	}
}

func TestSelectionAndMentionReachTheRightConsoles(t *testing.T) {
	h, ws := newHub(t)
	ws.Store("s1", "w1")
	ws.Store("s2", "w1")
	ws.Store("s3", "w2")
	cfg := t.TempDir()
	clis := map[string]*fakeCLI{}
	for _, id := range []string{"s1", "s2", "s3"} {
		env, err := h.Env(id, nil, cfg)
		if err != nil {
			t.Fatal(err)
		}
		clis[id] = connectCLI(t, cfg, envPort(t, env))
		clis[id].handshake()
	}
	b := openBrowser(h, "w1")
	defer b.e.Close()
	sel := `{"text":"abc","filePath":"/p/a.go","fileUrl":"file:///p/a.go","selection":{"start":{"line":1,"character":0},"end":{"line":1,"character":3},"isEmpty":false}}`
	b.e.Receive([]byte(`{"t":"selection","selection":` + sel + `}`))
	nextNote := func(f *fakeCLI, method string) map[string]any {
		t.Helper()
		for {
			m := f.next()
			if m["method"] == method {
				return m["params"].(map[string]any)
			}
		}
	}
	for _, id := range []string{"s1", "s2"} {
		if got := nextNote(clis[id], "selection_changed"); got["text"] != "abc" {
			t.Fatalf("%s %v", id, got)
		}
	}
	if got := clis["s1"].tool("getCurrentSelection", nil)[0]; !strings.Contains(got, `"text":"abc"`) || !strings.Contains(got, `"success":true`) {
		t.Fatal(got)
	}
	// nothing selected any more: current is gone, latest stays
	b.e.Receive([]byte(`{"t":"selection","selection":{"text":"","filePath":"","fileUrl":"","selection":{"start":{"line":0,"character":0},"end":{"line":0,"character":0},"isEmpty":true}}}`))
	if got := clis["s1"].tool("getCurrentSelection", nil)[0]; !strings.Contains(got, "No active editor found") {
		t.Fatal(got)
	}
	if got := clis["s1"].tool("getLatestSelection", nil)[0]; !strings.Contains(got, `"text":"abc"`) {
		t.Fatal(got)
	}
	b.e.Receive([]byte(`{"t":"mention","session":"s2","mention":{"filePath":"/p/a.go","lineStart":1,"lineEnd":2}}`))
	if got := nextNote(clis["s2"], "at_mentioned"); got["filePath"] != "/p/a.go" {
		t.Fatal(got)
	}
	// the other board heard nothing at all
	clis["s3"].send(map[string]any{"id": 55, "method": "ping"})
	for {
		m := clis["s3"].next()
		if m["method"] != nil {
			t.Fatalf("leaked to another workspace: %v", m)
		}
		if m["id"] == float64(55) {
			break
		}
	}
	b.e.Receive([]byte(`{"t":"tabs","tabs":[{"uri":"file:///p/a.go","isActive":true,"label":"a.go","languageId":"go","isDirty":false}]}`))
	if got := clis["s1"].tool("getOpenEditors", nil)[0]; !strings.Contains(got, `"label":"a.go"`) {
		t.Fatal(got)
	}
	if got := clis["s3"].tool("getOpenEditors", nil)[0]; strings.Contains(got, "a.go") {
		t.Fatal(got)
	}
}

func TestANewConsoleHearsTheCurrentSelectionAndTheNewestBrowserAnswers(t *testing.T) {
	h, ws := newHub(t)
	ws.Store("s1", "w1")
	cfg := t.TempDir()
	env, _ := h.Env("s1", nil, cfg)
	b1 := openBrowser(h, "w1")
	defer b1.e.Close()
	b2 := openBrowser(h, "w1")
	defer b2.e.Close()
	b1.e.Receive([]byte(`{"t":"selection","selection":{"text":"abc","filePath":"/a","fileUrl":"file:///a","selection":{"isEmpty":false}}}`))
	cli := connectCLI(t, cfg, envPort(t, env))
	cli.handshake()
	for {
		m := cli.next()
		if m["method"] == "selection_changed" {
			break
		}
	}
	// b1 was used last, so b1 answers
	go func() {
		c := b1.nextOf(t, "call")
		b1.answer(c["id"].(string), map[string]any{"content": []any{map[string]any{"type": "text", "text": "from b1"}}})
	}()
	if got := cli.tool("getDiagnostics", nil)[0]; got != "from b1" {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.ask(ctx, "w1", "s1", "x", nil, 0); err == nil {
		t.Fatal("a cancelled ask went through")
	}
}
