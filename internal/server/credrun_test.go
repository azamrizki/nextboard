package server

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/vault"
)

// credHub is a hub with a vault, one item the owner marked for the Home project, and a console of
// an agent in that project.
type credHub struct {
	*testHub
	pid, wid string
	dir      string
	console  string
	token    string
	agents   int
}

const credItem = "item-stripe01"

func newCredHub(t *testing.T) *credHub {
	t.Helper()
	h := newTestHub(t)
	h.setup()
	h.srv.credReqs.wait = 300 * time.Millisecond
	c := &credHub{testHub: h, dir: t.TempDir()}
	c.pid, c.wid = h.homeWorkspace()
	if st, _ := h.do("POST", "/api/vault", vaultKey(), nil); st != 201 {
		t.Fatalf("vault %d", st)
	}
	if st, _ := h.do("PUT", "/api/vault/items/"+credItem, map[string]any{"blob": vaultBlob(1), "baseRev": 0}, nil); st != 200 {
		t.Fatalf("item %d", st)
	}
	c.console, c.token = c.start(c.wid)
	return c
}

// start opens a console of a stand-in agent and returns it with its hook token.
func (c *credHub) start(wid string) (string, string) {
	c.t.Helper()
	c.agents++
	bin := filepath.Join(c.dir, fmt.Sprintf("credagent-%d", c.agents))
	script := "#!/bin/sh\nenv > " + bin + ".env\nexec sleep 120\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		c.t.Fatal(err)
	}
	name := filepath.Base(bin)
	if st, a := c.do("POST", "/api/agents", map[string]any{"id": name, "name": name, "command": bin, "base": "claude"}, nil); st != 201 {
		c.t.Fatalf("agent %d %v", st, a)
	}
	st, in := c.do("POST", "/api/sessions", map[string]any{"agent": name, "workspaceId": wid, "cwd": c.dir}, nil)
	if st != 201 {
		c.t.Fatalf("console %d %v", st, in)
	}
	var token string
	waitTrue(c.t, "agent started", 20*time.Second, func() bool {
		env, _ := os.ReadFile(bin + ".env")
		for _, l := range strings.Split(string(env), "\n") {
			if v, ok := strings.CutPrefix(l, "NB_TOKEN="); ok {
				token = v
			}
		}
		return token != ""
	})
	return in["id"].(string), token
}

func (c *credHub) mark(projects ...string) {
	c.t.Helper()
	body := map[string]any{"name": "Stripe test", "type": "login", "projects": projects, "values": []string{"password", "token"}}
	if st, out := c.do("PUT", "/api/vault/agents/"+credItem, body, nil); st != 200 {
		c.t.Fatalf("mark %d %v", st, out)
	}
}

func (c *credHub) rpc(sid, tok, method string, params any) map[string]any {
	c.t.Helper()
	url := strings.TrimSuffix(c.srv.hookURL, "/nb/hook") + "/nb/mcp/" + sid
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		c.t.Fatalf("%s: %d %v", method, resp.StatusCode, out)
	}
	return out
}

// call runs one tool of the console and returns its text and whether it is an error.
func (c *credHub) call(name string, args any) (string, bool) {
	c.t.Helper()
	return c.callAs(c.console, c.token, name, args)
}

func (c *credHub) callAs(sid, tok, name string, args any) (string, bool) {
	c.t.Helper()
	out := c.rpc(sid, tok, "tools/call", map[string]any{"name": name, "arguments": args})
	res, _ := out["result"].(map[string]any)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		c.t.Fatalf("%s: %v", name, out)
	}
	return content[0].(map[string]any)["text"].(string), res["isError"] == true
}

func (c *credHub) toolNames(sid, tok string) string {
	c.t.Helper()
	out := c.rpc(sid, tok, "tools/list", nil)
	var names []string
	for _, x := range out["result"].(map[string]any)["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	return strings.Join(names, ",")
}

var credIDRe = regexp.MustCompile(`cr_[A-Za-z0-9_-]+`)

// result waits for the request to end, as an agent would with credential_result.
func (c *credHub) result(id string) (string, bool) {
	c.t.Helper()
	for range 100 {
		text, isErr := c.call("credential_result", map[string]any{"id": id})
		if !strings.Contains(text, "is waiting for the owner") && !strings.Contains(text, "command is running") {
			return text, isErr
		}
	}
	c.t.Fatalf("request %s never ended", id)
	return "", false
}

func (c *credHub) request(id string) map[string]any {
	c.t.Helper()
	_, out := c.do("GET", "/api/vault/requests", nil, nil)
	for _, x := range out["items"].([]any) {
		if m := x.(map[string]any); m["id"] == id {
			return m
		}
	}
	c.t.Fatalf("no request %s in %v", id, out)
	return nil
}

func (c *credHub) newProject(name string) string {
	c.t.Helper()
	st, p := c.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": c.t.TempDir(), "name": name}, nil)
	if st != 201 {
		c.t.Fatalf("project %d %v", st, p)
	}
	return p["id"].(string)
}

func TestCredentialToolsOnlyWhereShared(t *testing.T) {
	c := newCredHub(t)
	if names := c.toolNames(c.console, c.token); strings.Contains(names, "credential_") {
		t.Fatalf("nothing is shared, yet the console has %s", names)
	}
	text, isErr := c.call("credential_run", map[string]any{"command": "true", "why": "x", "secrets": []any{map[string]any{"item": "Stripe test", "value": "password", "env": "K"}}})
	if !isErr || !strings.Contains(text, "shares no credentials") {
		t.Fatalf("run with nothing shared: %v %s", isErr, text)
	}

	// Marked for another project: still nothing here.
	other := c.newProject("Elsewhere")
	c.mark(other)
	if names := c.toolNames(c.console, c.token); strings.Contains(names, "credential_") {
		t.Fatalf("an item of another project gave this console %s", names)
	}
	c.mark(c.pid)
	if names := c.toolNames(c.console, c.token); !strings.Contains(names, "credential_list,credential_run,credential_result") {
		t.Fatalf("tools %s", names)
	}
	text, _ = c.call("credential_list", map[string]any{})
	if !strings.Contains(text, `"Stripe test" (login, id `+credItem+`): password, token`) {
		t.Fatalf("list:\n%s", text)
	}

	// A project the hub does not know is refused, and so is an item that is not there.
	if st, _ := c.do("PUT", "/api/vault/agents/"+credItem, map[string]any{"name": "x", "type": "login", "projects": []string{"p_nothere"}, "values": []string{"password"}}, nil); st != 400 {
		t.Fatalf("unknown project: %d", st)
	}
	if st, _ := c.do("PUT", "/api/vault/agents/item-missing1", map[string]any{"name": "x", "type": "login", "projects": []string{c.pid}, "values": []string{"password"}}, nil); st != 404 {
		t.Fatalf("missing item: %d", st)
	}
	_, list := c.do("GET", "/api/vault/agents", nil, nil)
	if n := len(list["items"].([]any)); n != 1 {
		t.Fatalf("catalog %v", list)
	}
}

// The whole way: an agent asks, the owner approves with the values its browser opened, the command
// gets them, and the agent reads its output without them. Nothing the hub writes holds a value.
func TestCredentialRunHidesTheValues(t *testing.T) {
	c := newCredHub(t)
	c.mark(c.pid)
	secret := "sk_live_CANARY-4d1f'\"$HOME`x`\\n\nsecond line\n"
	piped := "PIPED-CANARY-9b2e\nmore"
	cmd := `printf 'env=%s|\n' "$STRIPE_KEY"; printf %s "$STRIPE_KEY" | base64; printf %s "$STRIPE_KEY" > proof.env; cat > proof.stdin; echo done >&2`
	text, isErr := c.call("credential_run", map[string]any{
		"command": cmd, "why": "deploy the test build",
		"secrets": []any{map[string]any{"item": "stripe TEST", "value": "Password", "env": "STRIPE_KEY"}},
		"stdin":   map[string]any{"item": credItem, "value": "token"},
	})
	if isErr || !strings.Contains(text, "waiting for the owner") {
		t.Fatalf("run: %v %s", isErr, text)
	}
	id := credIDRe.FindString(text)
	r := c.request(id)
	if r["state"] != "waiting" || r["command"] != cmd || r["why"] != "deploy the test build" || r["cwd"] != c.dir || r["machine"] != "This PC" {
		t.Fatalf("request %v", r)
	}
	uses := r["uses"].([]any)
	if len(uses) != 2 || uses[0].(map[string]any)["env"] != "STRIPE_KEY" || uses[1].(map[string]any)["env"] != nil || uses[1].(map[string]any)["value"] != "token" {
		t.Fatalf("uses %v", uses)
	}
	if _, att := c.do("GET", "/api/attention?since=0", nil, nil); !strings.Contains(fmt.Sprint(att["items"]), "asks to use a credential") {
		t.Fatalf("no attention: %v", att)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "proof.env")); err == nil {
		t.Fatal("the command ran before anyone approved it")
	}

	// Values too short to hide, or the wrong number of them, are refused and change nothing.
	if st, out := c.do("POST", "/api/vault/requests/"+id+"/approve", map[string]any{"values": []string{"abc", piped}}, nil); st != 400 {
		t.Fatalf("short value: %d %v", st, out)
	}
	if st, _ := c.do("POST", "/api/vault/requests/"+id+"/approve", map[string]any{"values": []string{secret}}, nil); st != 400 {
		t.Fatalf("one value for two: %d", st)
	}
	if st, out := c.do("POST", "/api/vault/requests/"+id+"/approve", map[string]any{"values": []string{secret, piped}}, nil); st != 200 || out["state"] != "running" {
		t.Fatalf("approve %d %v", st, out)
	}
	if st, _ := c.do("POST", "/api/vault/requests/"+id+"/approve", map[string]any{"values": []string{secret, piped}}, nil); st != 409 {
		t.Fatalf("approve twice: %d", st)
	}

	text, isErr = c.result(id)
	if isErr || !strings.Contains(text, "env="+vault.Hidden+"|") || !strings.Contains(text, "done") {
		t.Fatalf("result: %v\n%s", isErr, text)
	}
	for _, leak := range []string{"CANARY", base64.StdEncoding.EncodeToString([]byte(secret))[:16], "second line"} {
		if strings.Contains(text, leak) {
			t.Fatalf("the agent read %q:\n%s", leak, text)
		}
	}
	got, _ := os.ReadFile(filepath.Join(c.dir, "proof.env"))
	gotIn, _ := os.ReadFile(filepath.Join(c.dir, "proof.stdin"))
	if string(got) != secret || string(gotIn) != piped {
		t.Fatalf("the command got %q and %q", got, gotIn)
	}
	if r := c.request(id); r["state"] != "done" || r["exitCode"] != float64(0) || r["by"] != "admin" {
		t.Fatalf("after: %v", r)
	}
	_, lg := c.do("GET", "/api/vault/agent-log", nil, nil)
	if items := lg["items"].([]any); len(items) != 1 || items[0].(map[string]any)["state"] != "done" {
		t.Fatalf("log %v", lg)
	}

	// Not one file of the hub holds a value, in any form the censor knows.
	forms := []string{"CANARY", hex.EncodeToString([]byte(piped))}
	_ = filepath.WalkDir(c.srv.st.Path(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		raw, _ := os.ReadFile(p)
		for _, f := range forms {
			if bytes.Contains(raw, []byte(f)) {
				t.Errorf("%s holds %q", p, f)
			}
		}
		return nil
	})
}

func TestCredentialRunRefusals(t *testing.T) {
	c := newCredHub(t)
	c.mark(c.pid)
	one := func(env string) []any {
		return []any{map[string]any{"item": "Stripe test", "value": "password", "env": env}}
	}
	for name, args := range map[string]map[string]any{
		"no why":          {"command": "true", "secrets": one("K")},
		"no secret":       {"command": "true", "why": "x"},
		"shell variable":  {"command": "true", "why": "x", "secrets": one("PATH")},
		"loader variable": {"command": "true", "why": "x", "secrets": one("LD_PRELOAD")},
		"not a name":      {"command": "true", "why": "x", "secrets": one("A-B")},
		"unknown item":    {"command": "true", "why": "x", "secrets": []any{map[string]any{"item": "Nope", "value": "password", "env": "K"}}},
		"unknown value":   {"command": "true", "why": "x", "secrets": []any{map[string]any{"item": "Stripe test", "value": "pin", "env": "K"}}},
		"blocked":         {"command": "git push --force origin main", "why": "x", "secrets": one("K")},
		"same env":        {"command": "true", "why": "x", "secrets": append(one("K"), one("K")...)},
	} {
		text, isErr := c.call("credential_run", args)
		if !isErr {
			t.Errorf("%s: accepted: %s", name, text)
		}
	}
	if _, out := c.do("GET", "/api/vault/requests", nil, nil); len(out["items"].([]any)) != 0 {
		t.Fatalf("a refused call left a request: %v", out)
	}
}

func TestCredentialDeniedNeverRuns(t *testing.T) {
	c := newCredHub(t)
	c.mark(c.pid)
	text, _ := c.call("credential_run", map[string]any{"command": "touch ran", "why": "x",
		"secrets": []any{map[string]any{"item": "Stripe test", "value": "password", "env": "K"}}})
	id := credIDRe.FindString(text)

	// Another console cannot read or wait for it.
	other, tok := c.start(c.wid)
	if text, isErr := c.callAs(other, tok, "credential_result", map[string]any{"id": id}); !isErr || !strings.Contains(text, "no request") {
		t.Fatalf("another console: %v %s", isErr, text)
	}

	if st, _ := c.do("POST", "/api/vault/requests/"+id+"/deny", map[string]any{"note": "not now"}, nil); st != 200 {
		t.Fatalf("deny %d", st)
	}
	text, isErr := c.result(id)
	if !isErr || text != "The owner denied this request. They said: not now" {
		t.Fatalf("denied: %v %s", isErr, text)
	}
	if st, _ := c.do("POST", "/api/vault/requests/"+id+"/approve", map[string]any{"values": []string{"long enough"}}, nil); st != 409 {
		t.Fatalf("approve after deny: %d", st)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "ran")); err == nil {
		t.Fatal("a denied command ran")
	}
}

func TestCredentialRequestExpires(t *testing.T) {
	c := newCredHub(t)
	c.mark(c.pid)
	c.srv.credReqs.expiry, c.srv.credReqs.wait = 200*time.Millisecond, 50*time.Millisecond
	text, _ := c.call("credential_run", map[string]any{"command": "touch ran", "why": "x",
		"secrets": []any{map[string]any{"item": "Stripe test", "value": "password", "env": "K"}}})
	id := credIDRe.FindString(text)
	text, isErr := c.result(id)
	if !isErr || !strings.Contains(text, "Nobody approved") {
		t.Fatalf("expired: %v %s", isErr, text)
	}
	if st, _ := c.do("POST", "/api/vault/requests/"+id+"/approve", map[string]any{"values": []string{"long enough"}}, nil); st != 409 {
		t.Fatalf("approve after expiry: %d", st)
	}
}

func TestCredentialRunFailureIsCensored(t *testing.T) {
	c := newCredHub(t)
	c.mark(c.pid)
	text, _ := c.call("credential_run", map[string]any{"command": `echo "bad key $K" >&2; exit 3`, "why": "x",
		"secrets": []any{map[string]any{"item": "Stripe test", "value": "password", "env": "K"}}})
	id := credIDRe.FindString(text)
	if st, _ := c.do("POST", "/api/vault/requests/"+id+"/approve", map[string]any{"values": []string{"CANARY-key"}}, nil); st != 200 {
		t.Fatal(st)
	}
	text, isErr := c.result(id)
	if !isErr || text != "exit code 3\nbad key "+vault.Hidden+"\n" {
		t.Fatalf("failure: %v %q", isErr, text)
	}
}

// credScript must give every byte back in each shell a login may use, as sh on the server runs it.
func TestCredScriptRoundTrip(t *testing.T) {
	values := []string{"a'b\"c$HOME`x`\\n\\\\z %s%b\\0101", "ä я 😀", "line1\nline2\n", "-n", "\x01\x7f", "STDIN\nraw\\0101"}
	uses := []credUse{{Env: "A1"}, {Env: "A2"}, {Env: "A3"}, {Env: "A4"}, {Env: "A5"}, {}}
	for _, sh := range []string{"sh", "dash", "bash", "zsh", "busybox"} {
		bin, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) {
			dir := t.TempDir()
			script, stdin := credScript(`for k in A1 A2 A3 A4 A5; do eval "printf %s \"\$$k\"" > "$k"; done; cat > IN`, uses, values)
			args := []string{"-c", script}
			env := append(os.Environ(), "SHELL="+bin)
			if sh == "busybox" {
				args = append([]string{"sh"}, args...)
				env = append(os.Environ(), "SHELL=/bin/sh")
			}
			cmd := exec.Command(bin, args...)
			cmd.Dir, cmd.Stdin, cmd.Env = dir, bytes.NewReader(stdin), env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			for i, name := range []string{"A1", "A2", "A3", "A4", "A5", "IN"} {
				got, _ := os.ReadFile(filepath.Join(dir, name))
				if string(got) != values[i] {
					t.Errorf("%s: %q, want %q", name, got, values[i])
				}
			}
			if strings.Contains(script, "line1") || strings.Contains(script, "STDIN") {
				t.Fatal("a value stands in the script, where ps shows it")
			}
		})
	}
}
