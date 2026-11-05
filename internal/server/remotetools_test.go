package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testSSHServer is a throwaway sshd that can go away and come back.
type testSSHServer struct {
	t    *testing.T
	dir  string
	port int
	key  string
	cmd  *exec.Cmd
}

func (s *testSSHServer) start() {
	s.t.Helper()
	s.cmd = exec.Command("/usr/sbin/sshd", "-D", "-e", "-f", filepath.Join(s.dir, "sshd_config"))
	if err := s.cmd.Start(); err != nil {
		s.t.Skipf("sshd tidak bisa jalan: %v", err)
	}
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", s.port)); err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Skip("sshd tidak listen")
}

func (s *testSSHServer) stop() {
	if s.cmd != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
		s.cmd = nil
	}
}

func newSSHServer(t *testing.T) *testSSHServer {
	t.Helper()
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		t.Skip("sshd tidak tersedia")
	}
	dir := t.TempDir()
	for _, f := range []string{"hostkey", "user"} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, f)).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
	}
	pub, _ := os.ReadFile(filepath.Join(dir, "user.pub"))
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cfg := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s/hostkey\nPidFile %s/sshd.pid\nAuthorizedKeysFile %s/authorized_keys\n"+
		"UsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nStrictModes no\nSubsystem sftp internal-sftp\n", port, dir, dir, dir)
	if err := os.WriteFile(filepath.Join(dir, "sshd_config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "user"))
	s := &testSSHServer{t: t, dir: dir, port: port, key: string(b)}
	s.start()
	t.Cleanup(s.stop)
	return s
}

// startSSHServer runs a throwaway sshd and returns its port and private key.
func startSSHServer(t *testing.T) (port int, key string) {
	t.Helper()
	s := newSSHServer(t)
	return s.port, s.key
}

// connectMachine adds the server to the hub and accepts its host key.
func (h *testHub) connectMachine(port int, key string) string {
	h.t.Helper()
	u, err := user.Current()
	if err != nil {
		h.t.Fatal(err)
	}
	st, m := h.do("POST", "/api/machines", map[string]any{"name": "srv", "host": "127.0.0.1", "port": port, "user": u.Username,
		"authMethod": "key", "privateKey": key, "persistMode": "direct"}, nil)
	if st != 201 {
		h.t.Fatalf("add machine %d %v", st, m)
	}
	id := m["id"].(string)
	var pending map[string]any
	waitTrue(h.t, "host key", 20*time.Second, func() bool {
		var list []map[string]any
		h.getJSON("/api/machines", &list)
		for _, x := range list {
			if x["id"] == id {
				pending, _ = x["pendingHostKey"].(map[string]any)
			}
		}
		return pending != nil
	})
	if st, r := h.do("POST", "/api/machines/"+id+"/hostkey", map[string]any{"accept": true, "fingerprint": pending["fingerprint"]}, nil); st != 200 {
		h.t.Fatalf("hostkey %d %v", st, r)
	}
	waitTrue(h.t, "connected", 20*time.Second, func() bool {
		var list []map[string]any
		h.getJSON("/api/machines", &list)
		for _, x := range list {
			if x["id"] == id {
				return x["status"] == "connected"
			}
		}
		return false
	})
	return id
}

// TestAgentWorksOnServer: the CLI runs on This PC with the hub's tools; the
// tools act on the server over SSH and only answer to the console's token.
func TestAgentWorksOnServer(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	port, key := startSSHServer(t)
	mid := h.connectMachine(port, key)

	dir := t.TempDir()
	spy := filepath.Join(dir, "spy")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + dir + "/argv\nenv > " + dir + "/env\nexec sleep 120\n"
	if err := os.WriteFile(spy, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "spy", "name": "Spy", "command": spy, "base": "claude"}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	srv := filepath.Join(dir, "srv")
	if err := os.MkdirAll(srv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srv, "CLAUDE.md"), []byte("Always say BANANA.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srv, "notes.txt"), []byte("alpha\nbeta\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	create := map[string]any{"agent": "spy", "machineId": mid, "cwd": srv}

	// Off by default: the server only gets shells.
	if st, r := h.do("POST", "/api/sessions", create, nil); st != 422 || r["code"] != "agent_this_pc_only" {
		t.Fatalf("setting off %d %v", st, r)
	}
	if st, r := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"remoteTools": true}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, r)
	}
	st, in := h.do("POST", "/api/sessions", create, nil)
	if st != 201 {
		t.Fatalf("create %d %v", st, in)
	}
	if in["machineId"] != "local" || in["remoteMachineId"] != mid || in["remoteCwd"] != srv || !strings.HasPrefix(in["cwd"].(string), h.srv.st.Path("agents", "remote")) {
		t.Fatalf("spec %v", in)
	}
	// The hub's own config folder trusts the folders the CLI runs in.
	conf, err := os.ReadFile(h.srv.st.Path("agents", "claude", ".claude.json"))
	if err != nil || !strings.Contains(string(conf), h.srv.st.Path("agents", "remote")) || !strings.Contains(string(conf), "hasTrustDialogAccepted") {
		waitTrue(t, "trust entry", 10*time.Second, func() bool {
			conf, err = os.ReadFile(h.srv.st.Path("agents", "claude", ".claude.json"))
			return err == nil && strings.Contains(string(conf), h.srv.st.Path("agents", "remote"))
		})
	}
	var argv, env string
	waitTrue(t, "the CLI started", 20*time.Second, func() bool {
		a, _ := os.ReadFile(filepath.Join(dir, "argv"))
		e, _ := os.ReadFile(filepath.Join(dir, "env"))
		argv, env = string(a), string(e)
		return strings.Contains(env, "NB_MCP=")
	})
	envOf := func(k string) string {
		for _, l := range strings.Split(env, "\n") {
			if v, ok := strings.CutPrefix(l, k+"="); ok {
				return v
			}
		}
		return ""
	}
	token := envOf("NB_TOKEN")
	if token == "" || strings.Contains(argv, token) || strings.Contains(envOf("NB_MCP"), token) {
		t.Fatalf("the token must stay out of argv and config:\n%s\n%s", argv, envOf("NB_MCP"))
	}
	for _, want := range []string{"--tools\nRead,Write,Edit,TodoWrite\n", "--mcp-config\n", "--strict-mcp-config\n", "--append-system-prompt-file\n"} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv lacks %q:\n%s", want, argv)
		}
	}
	lines := strings.Split(argv, "\n")
	for i, l := range lines {
		if l == "--append-system-prompt-file" {
			p, err := os.ReadFile(lines[i+1])
			if err != nil || !strings.Contains(string(p), srv) || !strings.Contains(string(p), "Always say BANANA.") || !strings.Contains(string(p), `"srv"`) {
				t.Fatalf("prompt %q %v", p, err)
			}
		}
	}
	var cfg struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(envOf("NB_MCP")), &cfg); err != nil {
		t.Fatal(err)
	}
	url := cfg.MCPServers["nextboard"].URL
	if !strings.HasPrefix(url, "http://127.0.0.1:") || cfg.MCPServers["nextboard"].Headers["Authorization"] != "Bearer ${NB_TOKEN}" {
		t.Fatalf("config %+v", cfg)
	}

	rpc := func(tok, method string, params any) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		st, out := rpc(token, "tools/call", map[string]any{"name": name, "arguments": args})
		if st != 200 {
			t.Fatalf("%s: %d %v", name, st, out)
		}
		res := out["result"].(map[string]any)
		text := res["content"].([]any)[0].(map[string]any)["text"].(string)
		return text, res["isError"] == true
	}
	if st, _ := rpc("wrong", "tools/list", nil); st != 401 {
		t.Fatalf("wrong token %d", st)
	}
	if resp, err := http.Get(url); err != nil || resp.StatusCode != 401 {
		t.Fatalf("no token %v %v", resp, err)
	}
	if _, out := rpc(token, "initialize", map[string]any{"protocolVersion": "2025-06-18"}); out["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize %v", out)
	}
	_, out := rpc(token, "tools/list", nil)
	var names []string
	for _, x := range out["result"].(map[string]any)["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "read_file,write_file,edit_file,grep,glob,bash,memory_search,memory_read,memory_save" {
		t.Fatalf("tools %v", names)
	}
	if _, out := rpc(token, "no/such", nil); out["error"] == nil {
		t.Fatalf("unknown method %v", out)
	}

	if text, bad := call("read_file", map[string]any{"path": "notes.txt"}); bad || !strings.Contains(text, "     1\talpha\n     2\tbeta\n") {
		t.Fatalf("read %q", text)
	}
	if text, bad := call("read_file", map[string]any{"path": "notes.txt", "offset": 2, "limit": 1}); bad || !strings.Contains(text, "     2\tbeta\n") || strings.Contains(text, "alpha") || !strings.Contains(text, "offset 3") {
		t.Fatalf("read window %q", text)
	}
	if text, bad := call("edit_file", map[string]any{"path": "notes.txt", "old_string": "beta", "new_string": "gamma"}); !bad || !strings.Contains(text, "2 times") {
		t.Fatalf("ambiguous edit %q", text)
	}
	if text, bad := call("edit_file", map[string]any{"path": "notes.txt", "old_string": "beta", "new_string": "gamma", "replace_all": true}); bad {
		t.Fatalf("edit %q", text)
	}
	if b, _ := os.ReadFile(filepath.Join(srv, "notes.txt")); string(b) != "alpha\ngamma\ngamma\n" {
		t.Fatalf("after edit %q", b)
	}
	if text, bad := call("write_file", map[string]any{"path": filepath.Join(srv, "a/b/new.txt"), "content": "made\n"}); bad {
		t.Fatalf("write %q", text)
	}
	if b, _ := os.ReadFile(filepath.Join(srv, "a/b/new.txt")); string(b) != "made\n" {
		t.Fatalf("written %q", b)
	}
	if text, bad := call("grep", map[string]any{"pattern": "gamma", "include": "*.txt"}); bad || !strings.Contains(text, "notes.txt:2:gamma") {
		t.Fatalf("grep %q", text)
	}
	if text, bad := call("grep", map[string]any{"pattern": "no-such-text-here"}); bad || text != "No matches." {
		t.Fatalf("grep without matches %q %v", text, bad)
	}
	if text, bad := call("glob", map[string]any{"pattern": "*.txt"}); bad || !strings.Contains(text, "notes.txt") || !strings.Contains(text, "new.txt") {
		t.Fatalf("glob %q", text)
	}
	if text, bad := call("bash", map[string]any{"command": "pwd; echo out; echo err >&2"}); bad || !strings.Contains(text, srv) || !strings.Contains(text, "out") || !strings.Contains(text, "err") {
		t.Fatalf("bash %q", text)
	}
	if text, bad := call("bash", map[string]any{"command": "echo before; exit 3"}); !bad || !strings.Contains(text, "exit code 3") || !strings.Contains(text, "before") {
		t.Fatalf("bash failure %q", text)
	}
	if text, bad := call("read_file", map[string]any{"path": "missing.txt"}); !bad {
		t.Fatalf("missing file %q", text)
	}

	// A CLI whose local tools cannot be turned off stays off servers.
	if st, r := h.do("POST", "/api/sessions/"+in["id"].(string)+"/restart", map[string]any{"agent": "aider"}, nil); st != 422 || r["code"] != "agent_this_pc_only" {
		t.Fatalf("switch %d %v", st, r)
	}
	if st, r := h.do("POST", "/api/sessions", map[string]any{"agent": "aider", "machineId": mid, "cwd": srv}, nil); st != 422 || r["code"] != "agent_this_pc_only" {
		t.Fatalf("aider on a server %d %v", st, r)
	}
}

func TestClipOutput(t *testing.T) {
	if s := clipOutput("short"); s != "short" {
		t.Fatal(s)
	}
	s := clipOutput("A" + strings.Repeat("x", remoteOutputMax*2) + "Z")
	if len(s) > remoteOutputMax+60 || s[0] != 'A' || s[len(s)-1] != 'Z' || !strings.Contains(s, "left out") {
		t.Fatalf("clipped %d", len(s))
	}
}

// TestRemoteRunnerReadsThroughHub: a hidden job on a server's project runs on
// This PC and may only read, for as long as it runs.
func TestRemoteRunnerReadsThroughHub(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	port, key := startSSHServer(t)
	mid := h.connectMachine(port, key)
	dir := t.TempDir()
	spy := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + dir + "/argv\nenv > " + dir + "/env\nwhile [ ! -f " + dir + "/go ]; do sleep 0.1; done\n" +
		`printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"{\"facts\":[]}","structured_output":{"facts":[]}}'` + "\n"
	if err := os.WriteFile(spy, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := filepath.Join(dir, "srv")
	if err := os.MkdirAll(srv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srv, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": mid, "path": srv, "name": "server"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	patch := map[string]any{"agents": map[string]any{"remoteTools": true, "paths": map[string]any{"local": map[string]any{"claude": spy}}}}
	if st, r := h.do("PATCH", "/api/settings", patch, nil); st != 200 {
		t.Fatalf("settings %d %v", st, r)
	}
	j, err := h.srv.orch.ScanMemory(p["id"].(string), "quick", "")
	if err != nil {
		t.Fatal(err)
	}
	if j.MachineID != "local" || j.Agent != "claude" {
		t.Fatalf("job %+v", j)
	}
	var argv, env string
	waitTrue(t, "the job started", 20*time.Second, func() bool {
		a, _ := os.ReadFile(filepath.Join(dir, "argv"))
		e, _ := os.ReadFile(filepath.Join(dir, "env"))
		argv, env = string(a), string(e)
		return strings.Contains(env, "NB_TOKEN=")
	})
	for _, want := range []string{"--strict-mcp-config\n", "mcp__nextboard__read_file,mcp__nextboard__grep,mcp__nextboard__glob\n", srv} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv lacks %q:\n%s", want, argv)
		}
	}
	token := ""
	for _, l := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(l, "NB_TOKEN="); ok {
			token = v
		}
	}
	if strings.Contains(argv, token) || !strings.Contains(env, "CLAUDE_CONFIG_DIR="+h.srv.st.Path("agents", "claude")) {
		t.Fatalf("token in argv or wrong login:\n%s", argv)
	}
	url := strings.TrimSuffix(h.srv.hookURL, "/nb/hook") + "/nb/mcp-job"
	rpc := func(method string, params any) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	_, out := rpc("tools/list", nil)
	if n := len(out["result"].(map[string]any)["tools"].([]any)); n != 3 {
		t.Fatalf("tools %v", out)
	}
	_, out = rpc("tools/call", map[string]any{"name": "read_file", "arguments": map[string]any{"path": "main.go"}})
	if !strings.Contains(fmt.Sprint(out), "package main") {
		t.Fatalf("read %v", out)
	}
	_, out = rpc("tools/call", map[string]any{"name": "bash", "arguments": map[string]any{"command": "touch " + srv + "/hacked"}})
	if !strings.Contains(fmt.Sprint(out), "may only read") {
		t.Fatalf("bash in a read-only job %v", out)
	}
	if _, err := os.Stat(filepath.Join(srv, "hacked")); err == nil {
		t.Fatal("the job wrote on the server")
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "the token ends with the job", 20*time.Second, func() bool {
		st, _ := rpc("tools/list", nil)
		return st == 401
	})
}

// TestCodexWorksOnServer: Codex gets the same tools, its shell and sandbox
// locked, and the same token rules.
func TestCodexWorksOnServer(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	port, key := startSSHServer(t)
	mid := h.connectMachine(port, key)
	dir := t.TempDir()
	spy := filepath.Join(dir, "spy")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + dir + "/argv\nenv > " + dir + "/env\nexec sleep 120\n"
	if err := os.WriteFile(spy, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, a := h.do("POST", "/api/agents", map[string]any{"id": "spycodex", "name": "Spy Codex", "command": spy, "base": "codex"}, nil); st != 201 {
		t.Fatalf("agent %d %v", st, a)
	}
	srv := filepath.Join(dir, "srv")
	if err := os.MkdirAll(srv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srv, "AGENTS.md"), []byte("Always say ZEBRA.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, r := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"remoteTools": true}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, r)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "spycodex", "machineId": mid, "cwd": srv, "yolo": true}, nil)
	if st != 201 || in["remoteMachineId"] != mid {
		t.Fatalf("create %d %v", st, in)
	}
	var argv, env string
	waitTrue(t, "the CLI started", 20*time.Second, func() bool {
		a, _ := os.ReadFile(filepath.Join(dir, "argv"))
		e, _ := os.ReadFile(filepath.Join(dir, "env"))
		argv, env = string(a), string(e)
		return strings.Contains(env, "NB_CODEX_CFG=")
	})
	for _, want := range []string{"--disable\nshell_tool\n", "--disable\nunified_exec\n", "--sandbox\nread-only\n", "bearer_token_env_var=\"NB_TOKEN\"", "default_tools_approval_mode=\"approve\"", "developer_instructions=", "Always say ZEBRA."} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv lacks %q:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "dangerously") {
		t.Fatalf("yolo must not open This PC:\n%s", argv)
	}
	if !strings.Contains(env, "CODEX_HOME="+h.srv.st.Path("agents", "codex")) {
		t.Fatalf("login folder:\n%s", env)
	}
}

// TestProjectMemoryAcrossCLIs: on This PC too, Claude Code keeps the project's
// memory folder as its auto memory and Codex reaches the same memory through the
// hub, so switching CLI loses nothing.
func TestProjectMemoryAcrossCLIs(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	spy := func(name, base string) string {
		bin := filepath.Join(dir, name)
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + bin + ".argv\nenv > " + bin + ".env\nexec sleep 120\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		if st, a := h.do("POST", "/api/agents", map[string]any{"id": name, "name": name, "command": bin, "base": base}, nil); st != 201 {
			t.Fatalf("agent %d %v", st, a)
		}
		return bin
	}
	claude, codex := spy("spyclaude", "claude"), spy("spycodex", "codex")
	pid, wid := h.homeWorkspace()
	memDir := h.srv.st.Path("memory", pid)
	start := func(agentID, bin string) (string, string, string) {
		t.Helper()
		st, in := h.do("POST", "/api/sessions", map[string]any{"agent": agentID, "workspaceId": wid, "cwd": dir}, nil)
		if st != 201 {
			t.Fatalf("console %d %v", st, in)
		}
		var argv, env string
		waitTrue(t, agentID+" started", 20*time.Second, func() bool {
			a, _ := os.ReadFile(bin + ".argv")
			e, _ := os.ReadFile(bin + ".env")
			argv, env = string(a), string(e)
			return strings.Contains(env, "NB_TOKEN=")
		})
		_ = os.Remove(bin + ".argv")
		_ = os.Remove(bin + ".env")
		return in["id"].(string), argv, env
	}
	envOf := func(env, k string) string {
		for _, l := range strings.Split(env, "\n") {
			if v, ok := strings.CutPrefix(l, k+"="); ok {
				return v
			}
		}
		return ""
	}

	// Claude Code: the project's folder is its auto memory, no local tool lost.
	cid, argv, env := start("spyclaude", claude)
	var settings map[string]any
	if err := json.Unmarshal([]byte(envOf(env, "NB_HOOKS")), &settings); err != nil {
		t.Fatalf("settings %v", err)
	}
	if settings["autoMemoryDirectory"] != memDir || settings["hooks"] == nil {
		t.Fatalf("settings %v", settings)
	}
	if strings.Contains(argv, "--tools") || strings.Contains(argv, "--strict-mcp-config") {
		t.Fatalf("a console on This PC keeps its own tools:\n%s", argv)
	}
	if !strings.Contains(argv, "--append-system-prompt-file") {
		t.Fatalf("argv:\n%s", argv)
	}
	// Its folder holds the memory, the pages it looks up through the hub, like
	// every other CLI, and looking something up needs no approval.
	for _, want := range []string{"--mcp-config\n", "--allowedTools\nmcp__nextboard__memory_search,mcp__nextboard__memory_read\n"} {
		if !strings.Contains(argv, want) {
			t.Fatalf("no %q in:\n%s", want, argv)
		}
	}
	url := strings.TrimSuffix(h.srv.hookURL, "/nb/hook") + "/nb/mcp/"
	rpc := func(sid, tok, method string, params any) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", url+sid, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	st, out := rpc(cid, envOf(env, "NB_TOKEN"), "tools/list", nil)
	if n := len(out["result"].(map[string]any)["tools"].([]any)); st != 200 || n != 3 {
		t.Fatalf("claude endpoint %d: %v", st, out)
	}
	_, out = rpc(cid, envOf(env, "NB_TOKEN"), "tools/call", map[string]any{"name": "memory_search", "arguments": map[string]any{"query": "database"}})
	if !strings.Contains(fmt.Sprint(out), "Never drop") {
		t.Fatalf("memory_search: %v", out)
	}
	_, out = rpc(cid, envOf(env, "NB_TOKEN"), "tools/call", map[string]any{"name": "memory_save", "arguments": map[string]any{"fact": "no rules", "page": "critical"}})
	if !strings.Contains(fmt.Sprint(out), "isError:true") {
		t.Fatalf("an agent must not write the critical notes: %v", out)
	}

	// Claude Code learned something: it writes straight into the folder.
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("- [Deploy](deploy.md): friday\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "deploy.md"), []byte("---\nname: deploy\n---\nDeploys happen on Friday.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Switch to Codex: it knows, and keeps its own tools on This PC.
	xid, argv, env := start("spycodex", codex)
	cfg := envOf(env, "NB_CODEX_CFG")
	if !strings.Contains(cfg, "Deploys happen on Friday") || !strings.Contains(argv, "mcp_servers.nextboard.url=") {
		t.Fatalf("codex context:\n%s\n%s", cfg, argv)
	}
	if strings.Contains(argv, "shell_tool") || strings.Contains(argv, "read-only") {
		t.Fatalf("a console on This PC keeps its own tools:\n%s", argv)
	}
	tok := envOf(env, "NB_TOKEN")
	_, out = rpc(xid, tok, "tools/list", nil)
	if n := len(out["result"].(map[string]any)["tools"].([]any)); n != 3 {
		t.Fatalf("memory tools only: %v", out)
	}
	_, out = rpc(xid, tok, "tools/call", map[string]any{"name": "bash", "arguments": map[string]any{"command": "true"}})
	if !strings.Contains(fmt.Sprint(out), "This PC") {
		t.Fatalf("no server, no file tools: %v", out)
	}
	_, out = rpc(xid, tok, "tools/call", map[string]any{"name": "memory_read", "arguments": map[string]any{}})
	if !strings.Contains(fmt.Sprint(out), "Deploys happen on Friday") {
		t.Fatalf("memory_read %v", out)
	}
	_, out = rpc(xid, tok, "tools/call", map[string]any{"name": "memory_save", "arguments": map[string]any{"fact": "the build command is make zebra", "section": "Commands"}})
	if strings.Contains(fmt.Sprint(out), "isError:true") {
		t.Fatalf("memory_save %v", out)
	}

	// Back to Claude Code: Codex's fact is in the file it loads.
	b, _ := os.ReadFile(filepath.Join(memDir, "MEMORY.md"))
	if !strings.Contains(string(b), "make zebra") || !strings.Contains(string(b), "[Deploy](deploy.md)") {
		t.Fatalf("memory:\n%s", b)
	}
	// And the panel sees all of it.
	var view map[string]any
	if st := h.getJSON("/api/projects/"+pid+"/memory", &view); st != 200 {
		t.Fatalf("view %d", st)
	}
	if !strings.Contains(fmt.Sprint(view["project"]), "make zebra") || len(view["notes"].([]any)) != 1 {
		t.Fatalf("view %v", view)
	}
}
