package server

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The Code workbench over HTTP: file list, search and replace, ignored files,
// commands on save, a file at HEAD, panel terminals, the language server bridge.

func filesProject(t *testing.T, h *testHub) (id, dir string) {
	t.Helper()
	dir = t.TempDir()
	if st, b := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "w"}, nil); st != 201 {
		t.Fatalf("project: %d %v", st, b)
	} else {
		id = b["id"].(string)
	}
	return id, dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=a@b", "-c", "user.name=a"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git %v: %v %s", args, err, out)
	}
}

func TestWorkbenchFilesAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	pid, dir := filesProject(t, h)
	base := "/api/projects/" + pid
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "*.log\n")
	write("a.txt", "needle one\r\nNEEDLE two\r\n")
	write("sub/b.txt", "no match\n")
	write("x.log", "needle ignored\n")
	git(t, dir, "init", "-q")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "first")

	st, list := h.do("GET", base+"/files", nil, nil)
	if st != 200 || list["source"] != "git" {
		t.Fatalf("files: %d %v", st, list)
	}
	items := list["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("files items: %v", items)
	}

	st, res := h.do("POST", base+"/files/search", map[string]any{"query": "needle"}, nil)
	if st != 200 || res["count"].(float64) != 2 {
		t.Fatalf("search: %d %v", st, res)
	}
	st, res = h.do("POST", base+"/files/search", map[string]any{"query": "(", "regex": true}, nil)
	if st != 400 || res["code"] != "bad_pattern" {
		t.Fatalf("bad regex: %d %v", st, res)
	}

	st, res = h.do("POST", base+"/files/replace", map[string]any{"query": "needle", "replacement": "pin", "paths": []string{"a.txt"}}, nil)
	if st != 200 || res["count"].(float64) != 2 {
		t.Fatalf("replace: %d %v", st, res)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "pin one\r\npin two\r\n" {
		t.Fatalf("replaced: %q", b)
	}
	st, res = h.do("POST", base+"/files/replace", map[string]any{"query": "x", "replacement": "y", "paths": []string{"../escape"}}, nil)
	if st != 403 || res["code"] != "outside_project" {
		t.Fatalf("escape: %d %v", st, res)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "o.txt"), []byte("needle"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "o.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	st, res = h.do("POST", base+"/files/replace", map[string]any{"query": "needle", "replacement": "y", "paths": []string{"link.txt"}}, nil)
	if st != 200 || res["count"].(float64) != 0 {
		t.Fatalf("link: %d %v", st, res)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "o.txt")); string(b) != "needle" {
		t.Fatalf("a link wrote outside the project: %q", b)
	}

	st, res = h.do("POST", base+"/files/ignored", map[string]any{"paths": []string{"x.log", "a.txt"}}, nil)
	if got := res["items"].([]any); st != 200 || len(got) != 1 || got[0] != "x.log" {
		t.Fatalf("ignored: %d %v", st, res)
	}

	st, res = h.do("GET", base+"/git/show?path=a.txt", nil, nil)
	if st != 200 || res["exists"] != true || res["content"] != "needle one\r\nNEEDLE two\r\n" {
		t.Fatalf("show: %d %v", st, res)
	}
	st, res = h.do("GET", base+"/git/show?path=new.txt", nil, nil)
	if st != 200 || res["exists"] != false {
		t.Fatalf("show missing: %d %v", st, res)
	}
	st, res = h.do("GET", base+"/git/show?path=a.txt&ref=--output=x", nil, nil)
	if st != 400 || res["code"] != "bad_ref" {
		t.Fatalf("show bad ref: %d %v", st, res)
	}

	// no rules yet: nothing runs
	st, res = h.do("POST", base+"/files/on-save", map[string]any{"path": "a.txt"}, nil)
	if st != 200 || len(res["runs"].([]any)) != 0 {
		t.Fatalf("on-save empty: %d %v", st, res)
	}
	st, res = h.do("PATCH", "/api/settings", map[string]any{"editor": map[string]any{"onSave": []map[string]string{{"pattern": "*.txt", "command": "printf saved > marker"}}}}, nil)
	if st != 200 {
		t.Fatalf("settings: %d %v", st, res)
	}
	st, res = h.do("POST", base+"/files/on-save", map[string]any{"path": "a.txt"}, nil)
	if st != 200 || len(res["runs"].([]any)) != 1 {
		t.Fatalf("on-save: %d %v", st, res)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "marker")); string(b) != "saved" {
		t.Fatalf("marker: %q", b)
	}
	st, res = h.do("POST", base+"/files/on-save", map[string]any{"path": "../a.txt"}, nil)
	if st != 403 {
		t.Fatalf("on-save escape: %d %v", st, res)
	}
	st, res = h.do("PATCH", "/api/settings", map[string]any{"editor": map[string]any{"onSave": []map[string]string{{"pattern": " ", "command": "x"}}}}, nil)
	if st != 400 {
		t.Fatalf("bad rule accepted: %d %v", st, res)
	}
}

func TestPanelTerminalHasNoBoardNode(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "w"}, nil)
	if st != 201 {
		t.Fatalf("project: %d %v", st, pr)
	}
	wid := pr["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	st, sess := h.do("POST", "/api/sessions", map[string]any{"workspaceId": wid, "preset": "shell", "panel": true}, nil)
	if st != 201 || sess["panel"] != true {
		t.Fatalf("create: %d %v", st, sess)
	}
	st, b := h.do("GET", "/api/workspaces/"+wid+"/board", nil, nil)
	if st != 200 || strings.Contains(fmt.Sprint(b), sess["id"].(string)) {
		t.Fatalf("board has the panel terminal: %d %v", st, b)
	}
	if st, _ := h.do("DELETE", "/api/sessions/"+sess["id"].(string), nil, nil); st != 200 {
		t.Fatalf("delete: %d", st)
	}
}

func TestLSPStatusAndBridge(t *testing.T) {
	// A server that is not on this PC, so that only the fake below is found.
	if _, err := exec.LookPath("docker-langserver"); err == nil {
		t.Skip("docker-langserver is installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// A fake language server: answers the first framed message.
	fake := `#!/bin/sh
head -c 20 >/dev/null
printf 'Content-Length: 11\r\n\r\n{"ok":true}'
cat >/dev/null
`
	if err := os.WriteFile(filepath.Join(bin, "docker-langserver"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newTestHub(t)
	h.setup()
	pid, _ := filesProject(t, h)
	st, res := h.do("GET", "/api/projects/"+pid+"/lsp", nil, nil)
	if st != 200 {
		t.Fatalf("status: %d %v", st, res)
	}
	var row map[string]any
	for _, x := range res["items"].([]any) {
		if r := x.(map[string]any); r["id"] == "dockerfile" {
			row = r
		}
	}
	if row == nil || row["available"] != true || row["command"] != "docker-langserver --stdio" {
		t.Fatalf("status row: %v", row)
	}

	hdr := http.Header{"Origin": {"https://evil.example"}}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	path := "/api/projects/" + pid + "/lsp/dockerfile/ws"
	if _, resp, err := h.dialWS(path, hdr); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("cross-origin should be 403: %v", err)
	}
	hdr.Set("Origin", h.ts.URL)
	if _, resp, err := h.dialWS("/api/projects/"+pid+"/lsp/nope/ws", hdr); err == nil || resp == nil || resp.StatusCode != 404 {
		t.Fatalf("unknown server should be 404: %v", err)
	}
	ws, _, err := h.dialWS(path, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := ws.WriteMessage(websocket.TextMessage, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	typ, b, err := ws.ReadMessage()
	if err != nil || typ != websocket.TextMessage || string(b) != `{"ok":true}` {
		t.Fatalf("frame: %v %q %v", typ, b, err)
	}
}

func TestDAPStatusAndBridges(t *testing.T) {
	for _, name := range []string{"dlv", "lldb-dap"} {
		if _, err := exec.LookPath(name); err == nil {
			t.Skip(name + " is installed: the fakes below would not be found first")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// stdio adapter: answers the first framed message.
	stdio := `#!/bin/sh
head -c 20 >/dev/null
printf 'Content-Length: 11\r\n\r\n{"ok":"io"}'
cat >/dev/null
`
	// TCP adapter, the way Delve works: announces a port, then speaks on it.
	tcp := `#!/usr/bin/env python3
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
s.listen(1)
print("DAP server listening at: 127.0.0.1:%d" % s.getsockname()[1], flush=True)
c, _ = s.accept()
c.recv(100)
c.sendall(b'Content-Length: 12\r\n\r\n{"ok":"tcp"}')
c.recv(100)
`
	for name, body := range map[string]string{"lldb-dap": stdio, "dlv": tcp} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := newTestHub(t)
	h.setup()
	pid, _ := filesProject(t, h)
	st, res := h.do("GET", "/api/projects/"+pid+"/dap", nil, nil)
	if st != 200 {
		t.Fatalf("status: %d %v", st, res)
	}
	seen := map[string]bool{}
	for _, x := range res["items"].([]any) {
		r := x.(map[string]any)
		seen[r["id"].(string)] = r["available"].(bool)
	}
	if !seen["go"] || !seen["native"] {
		t.Fatalf("adapters: %v", seen)
	}
	hdr := http.Header{"Origin": {h.ts.URL}}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	if _, resp, err := h.dialWS("/api/projects/"+pid+"/dap/nope/ws", hdr); err == nil || resp == nil || resp.StatusCode != 404 {
		t.Fatalf("unknown adapter should be 404: %v", err)
	}
	for adapter, want := range map[string]string{"native": `{"ok":"io"}`, "go": `{"ok":"tcp"}`} {
		ws, _, err := h.dialWS("/api/projects/"+pid+"/dap/"+adapter+"/ws", hdr)
		if err != nil {
			t.Fatalf("%s: %v", adapter, err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
		if err := ws.WriteMessage(websocket.TextMessage, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		_, b, err := ws.ReadMessage()
		if err != nil || string(b) != want {
			t.Fatalf("%s frame: %q %v", adapter, b, err)
		}
		ws.Close()
	}
}

func TestExtensionEndpointsValidate(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, res := h.do("GET", "/api/extensions", nil, nil)
	if st != 200 || len(res["items"].([]any)) != 0 {
		t.Fatalf("list: %d %v", st, res)
	}
	if st, res := h.do("POST", "/api/extensions", map[string]any{"namespace": "../x", "name": "y"}, nil); st != 400 || res["code"] != "bad_extension" {
		t.Fatalf("bad id: %d %v", st, res)
	}
	if st, res := h.do("DELETE", "/api/extensions/not.there", nil, nil); st != 404 {
		t.Fatalf("delete: %d %v", st, res)
	}
	if st, _ := h.do("GET", "/api/extensions/not.there/file?path=a.json", nil, nil); st != 404 {
		t.Fatalf("file: %d", st)
	}
}

func TestCodeCompleteJob(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-claude")
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"return 1"}'
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newTestHub(t)
	h.setup()
	pid, _ := filesProject(t, h)
	if st, res := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": fake}}}}, nil); st != 200 {
		t.Fatalf("settings: %d %v", st, res)
	}
	st, j := h.do("POST", "/api/projects/"+pid+"/code/complete", map[string]any{"path": "a.go", "language": "go", "before": "func a() int {\n", "after": "\n}"}, nil)
	if st != 202 {
		t.Fatalf("complete: %d %v", st, j)
	}
	done := h.waitStudioJob(j["id"].(string))
	if done["state"] != "done" || done["purpose"] != "code-complete" || !strings.Contains(fmt.Sprint(done["result"]), "return 1") {
		t.Fatalf("job: %v", done)
	}
	big := strings.Repeat("x", 300<<10)
	if st, res := h.do("POST", "/api/projects/"+pid+"/code/complete", map[string]any{"before": big}, nil); st != 413 {
		t.Fatalf("too big: %d %v", st, res)
	}
	// a question about the selection: read-only, no console needed
	st, j = h.do("POST", "/api/projects/"+pid+"/code/ask", map[string]any{"question": "why?", "path": "a.go", "language": "go", "selection": "x := 1", "startLine": 3, "endLine": 3}, nil)
	if st != 202 {
		t.Fatalf("ask: %d %v", st, j)
	}
	if done := h.waitStudioJob(j["id"].(string)); done["purpose"] != "code-ask" || done["state"] != "done" {
		t.Fatalf("ask job: %v", done)
	}
	if st, res := h.do("POST", "/api/projects/"+pid+"/code/ask", map[string]any{"question": "  "}, nil); st != 400 || res["code"] != "bad_question" {
		t.Fatalf("empty question: %d %v", st, res)
	}
}

func TestESLintAndBlameTakeAnEditorsText(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	pid, dir := filesProject(t, h)
	base := "/api/projects/" + pid
	git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-qm", "first")

	// more than the usual 64 KB limit of a request
	text := "one\n" + strings.Repeat("x\n", 200_000)
	st, res := h.do("POST", base+"/git/blame", map[string]any{"path": "a.txt", "text": text}, nil)
	if st != 200 || len(res["lines"].([]any)) != 200_001 {
		t.Fatalf("blame: %d", st)
	}
	st, res = h.do("GET", base+"/git/log?path=a.txt&limit=5", nil, nil)
	if st != 200 || len(res["items"].([]any)) != 1 {
		t.Fatalf("file log: %d %v", st, res)
	}
	st, res = h.do("GET", base+"/git/log?path=../x", nil, nil)
	if st != 400 || res["code"] != "bad_path" {
		t.Fatalf("bad log path: %d %v", st, res)
	}

	bin := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat >/dev/null\nprintf '[{\"messages\":[{\"ruleId\":\"semi\",\"severity\":2,\"message\":\"m\",\"line\":1,\"column\":1}]}]'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "eslint"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	st, res = h.do("POST", base+"/eslint", map[string]any{"path": "a.js", "text": text}, nil)
	if st != 200 || res["available"] != true || len(res["messages"].([]any)) != 1 {
		t.Fatalf("eslint: %d %v", st, res)
	}
	st, res = h.do("POST", base+"/eslint", map[string]any{"path": "../a.js", "text": ""}, nil)
	if st != 400 || res["code"] != "bad_path" {
		t.Fatalf("eslint escape: %d %v", st, res)
	}
}
