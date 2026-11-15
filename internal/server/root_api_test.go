package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRootAgent: an agent at the root of a project (a fake Codex) runs in the root folder with
// the real folders of the writable parts on This PC as extra writable folders and its root
// trusted; the prompt points at AGENTS.md. The hub's file tools need a part there: a part it
// changes with its own tools is read-only through them, a read-only part and a server part are
// reached through them as from This PC, with the project's access. Its shell's blocked list is
// every part's; with the project sandbox it writes in the linked parts only.
func TestRootAgent(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	bin := spyCodex(t, h)
	pid, wid, web, docs, ids := rootProject(t, h)
	h.do("PATCH", "/api/projects/"+pid+"/parts/"+ids["Docs"], map[string]any{"blocked": []string{"touch"}}, nil)
	sa := newSpyAgents(t, h, bin, wid)
	dir := h.srv.roots.Dir(pid)

	sid, tok, argv, env := sa.start(map[string]any{"root": true})
	realWeb, _ := filepath.EvalSymlinks(web)
	for _, want := range []string{"--add-dir\n" + realWeb + "\n", `projects={"` + dir + `"={trust_level="trusted"}}`} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv without %q:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "--add-dir\n"+docs) || strings.Contains(argv, "shell_tool") {
		t.Fatalf("a read-only part writable, or local tools off:\n%s", argv)
	}
	if !strings.Contains(env, "You work at the root of project Acme Shop, which spans 3 parts. ./AGENTS.md lists them") {
		t.Fatalf("prompt:\n%s", env)
	}
	if strings.Contains(env, "This project spans several parts") {
		t.Fatalf("the parts list instead of the pointer:\n%s", env)
	}
	if got := h.srv.rootBlocked(pid); !slices.Contains(got, "touch") {
		t.Fatalf("blocked at the root %v", got)
	}

	ts := sa.tools(sid, tok)
	schema := ts["read_file"]["inputSchema"].(map[string]any)
	if req := schema["required"].([]any); !slices.Contains(req, any("part")) {
		t.Fatalf("part not required: %v", schema)
	}
	if d := schema["properties"].(map[string]any)["part"].(map[string]any)["description"].(string); !strings.Contains(d, "Web (on This PC: change it with your own tools)") || !strings.Contains(d, "Docs (read only)") {
		t.Fatalf("part description %q", d)
	}
	// only reads go through the hub here: Web is changed with its own tools, the others are read only
	if ts["write_file"] != nil || ts["bash"] != nil || ts["grep"] == nil {
		t.Fatalf("tools %v", ts)
	}
	call := func(name string, args map[string]any) string { return sa.call(sid, tok, name, args) }
	if out := call("read_file", map[string]any{"path": "index.html"}); !strings.Contains(out, "At the project root, name the part: Web") {
		t.Fatalf("no part: %s", out)
	}
	if out := call("read_file", map[string]any{"part": "docs", "path": "guide.md"}); !strings.Contains(out, "# guide") {
		t.Fatalf("read the read-only part: %s", out)
	}
	if out := call("read_file", map[string]any{"part": "Web", "path": "index.html"}); !strings.Contains(out, "acme-shop") {
		t.Fatalf("read a linked part: %s", out)
	}
	if out := call("write_file", map[string]any{"part": "Web", "path": "x.txt", "content": "x"}); !strings.Contains(out, "Web is on This PC: use your own tools in ./Web") {
		t.Fatalf("write in a linked part: %s", out)
	}
	if out := call("bash", map[string]any{"part": "Web", "command": "touch y.txt"}); !strings.Contains(out, "use your own tools") {
		t.Fatalf("bash in a linked part: %s", out)
	}
	if out := call("write_file", map[string]any{"part": "Docs", "path": "x.txt", "content": "x"}); !strings.Contains(out, "Docs is read only for you") {
		t.Fatalf("write in the read-only part: %s", out)
	}
	if out := call("write_file", map[string]any{"part": "Api", "path": "x.txt", "content": "x"}); !strings.Contains(out, "Api is read only for you") {
		t.Fatalf("write on the server without full access: %s", out)
	}
	if out := call("read_file", map[string]any{"part": "Nope", "path": "x"}); !strings.Contains(out, `no part \"Nope\"`) && !strings.Contains(out, "no part") {
		t.Fatalf("unknown part: %s", out)
	}
	for _, f := range []string{filepath.Join(web, "x.txt"), filepath.Join(web, "y.txt"), filepath.Join(docs, "x.txt")} {
		if _, err := os.Stat(f); err == nil {
			t.Fatalf("written: %s", f)
		}
	}
	// the info: at the root, This PC, its folder
	_, in := h.do("GET", "/api/sessions/"+sid, nil, nil)
	if in["root"] != true || in["cwd"] != dir || in["remoteMachineId"] != nil {
		t.Fatalf("session %v", in)
	}

	// the project sandbox: the linked part's real folder is writable, the read-only one not
	h.do("PATCH", "/api/projects/"+pid, map[string]any{"sandbox": "workspace"}, nil)
	_, _, argv, env = sa.start(map[string]any{"root": true})
	roots := ""
	for _, l := range strings.Split(argv, "\n") {
		if v, ok := strings.CutPrefix(l, "sandbox_workspace_write.writable_roots="); ok {
			roots = v
		}
	}
	if !strings.Contains(roots, `"`+realWeb+`"`) || strings.Contains(roots, docs) {
		t.Fatalf("writable roots %s\n%s", roots, argv)
	}
	if !strings.Contains(env, "the parts on This PC you may change") {
		t.Fatalf("sandbox rule:\n%s", env)
	}
}

// TestRootOnAServer: a root console reaches a part on a real (throwaway) sshd through the hub's
// tools: it reads there, writes only with full access, and that part's blocked commands hold on
// its bash. A file pasted into an order for an agent that works on the server is copied there.
func TestRootOnAServer(t *testing.T) {
	port, key := startSSHServer(t)
	h := newTestHub(t)
	h.setup()
	h.srv.attachRoot = t.TempDir() // the server's paste folders, not in /tmp
	mid := h.connectMachine(port, key)
	bin := spyCodex(t, h)
	web, api := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(api, "routes.php"), []byte("<?php // routes\n"), 0o644)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": web, "name": "acme-shop", "partName": "Web"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	st, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Api", "machineId": mid, "path": api}, nil)
	if st != 201 {
		t.Fatalf("server part %d %v", st, p)
	}
	apiID := p["parts"].([]any)[1].(map[string]any)["id"].(string)
	h.do("PATCH", "/api/projects/"+pid+"/parts/"+apiID, map[string]any{"blocked": []string{"touch"}}, nil)
	sa := newSpyAgents(t, h, bin, wid)

	sid, tok, _, _ := sa.start(map[string]any{"root": true})
	dir := h.srv.roots.Dir(pid)
	if !strings.Contains(readText(t, filepath.Join(dir, "AGENTS.md")), `| Api | server "srv" | `+api+` | ./Api (note) | read only`) {
		t.Fatalf("AGENTS.md:\n%s", readText(t, filepath.Join(dir, "AGENTS.md")))
	}
	if out := sa.call(sid, tok, "read_file", map[string]any{"part": "api", "path": "routes.php"}); !strings.Contains(out, "// routes") {
		t.Fatalf("read on the server: %s", out)
	}
	if out := sa.call(sid, tok, "write_file", map[string]any{"part": "Api", "path": "new.php", "content": "<?php\n"}); !strings.Contains(out, "Api is read only for you") {
		t.Fatalf("write without full access: %s", out)
	}
	h.do("PATCH", "/api/projects/"+pid, map[string]any{"partAccess": "full"}, nil)
	sid, tok, _, _ = sa.start(map[string]any{"root": true})
	if ts := sa.tools(sid, tok); ts["write_file"] == nil || ts["bash"] == nil {
		t.Fatalf("no write tools with full access: %v", ts)
	}
	if out := sa.call(sid, tok, "write_file", map[string]any{"part": "Api", "path": "new.php", "content": "<?php\n"}); strings.Contains(out, "isError:true") {
		t.Fatalf("write with full access: %s", out)
	}
	if b, err := os.ReadFile(filepath.Join(api, "new.php")); err != nil || string(b) != "<?php\n" {
		t.Fatalf("written over SSH %q %v", b, err)
	}
	if out := sa.call(sid, tok, "bash", map[string]any{"part": "Api", "command": "pwd"}); !strings.Contains(out, api) {
		t.Fatalf("bash on the server: %s", out)
	}
	if out := sa.call(sid, tok, "bash", map[string]any{"part": "Api", "command": "touch made.txt"}); !strings.Contains(out, "the owner blocked this command") {
		t.Fatalf("a command blocked in Api: %s", out)
	}
	if _, err := os.Stat(filepath.Join(api, "made.txt")); err == nil {
		t.Fatal("the blocked command ran")
	}

	// an order's pasted file, for an agent in the server's part: a copy on the server too
	rid, _, _, _ := sa.start(map[string]any{"partId": apiID})
	code, raw, _ := h.doRaw("POST", "/api/projects/"+pid+"/attach?name=notes.txt", "text/plain", []byte("pasted notes\n"))
	if code != 200 {
		t.Fatalf("attach %d %s", code, raw)
	}
	var att struct{ Path string }
	if err := json.Unmarshal(raw, &att); err != nil || att.Path == "" {
		t.Fatalf("attach %s %v", raw, err)
	}
	if h.srv.deliver.Prepare == nil {
		t.Fatal("deliveries are not prepared")
	}
	text := h.srv.orderAttachments(rid, "read "+att.Path+" please")
	_, copyPath, ok := strings.Cut(text, "(a copy on srv: ")
	copyPath, _, _ = strings.Cut(copyPath, ")")
	if !ok || !strings.HasPrefix(text, "read "+att.Path+" (a copy on srv: ") || readText(t, copyPath) != "pasted notes\n" {
		t.Fatalf("text %q", text)
	}
	// a root console is on This PC: nothing to copy
	if got := h.srv.orderAttachments(sid, "read "+att.Path); got != "read "+att.Path {
		t.Fatalf("root console %q", got)
	}
}
