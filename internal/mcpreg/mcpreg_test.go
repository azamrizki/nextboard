package mcpreg

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.Open(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(st, box)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

const secretValue = "s3cret-value-do-not-show"

func TestValuesStaySealedAndHidden(t *testing.T) {
	s, dir := openStore(t)
	sv, err := s.Create(Server{ID: "sentry", Transport: TransportHTTP, URL: "https://mcp.example.com/mcp", Enabled: true,
		Headers: []Var{{Name: "Authorization", Value: "Bearer " + secretValue}}})
	if err != nil {
		t.Fatal(err)
	}
	if sv.Headers[0].Value != "" || !sv.Headers[0].Set {
		t.Fatalf("a client must not get the value back: %+v", sv.Headers)
	}
	if v := s.List()[0].Headers[0]; v.Value != "" || !v.Set {
		t.Fatalf("list: %+v", v)
	}
	b, _ := json.Marshal(s.List())
	if strings.Contains(string(b), secretValue) {
		t.Fatal("value in the API view")
	}
	raw, err := os.ReadFile(filepath.Join(dir, storeName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretValue) {
		t.Fatal("value in the clear on disk")
	}

	// An edit that says nothing about a value keeps it; one left out is removed.
	if _, err := s.Update("sentry", Server{Transport: TransportHTTP, URL: "https://mcp.example.com/v2", Enabled: true,
		Headers: []Var{{Name: "Authorization"}}}); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.opened("sentry"); o.Headers[0].Value != "Bearer "+secretValue || o.URL != "https://mcp.example.com/v2" {
		t.Fatalf("value lost: %+v", o)
	}
	if _, err := s.Update("sentry", Server{Transport: TransportHTTP, URL: "https://mcp.example.com/v2", Enabled: true, Headers: []Var{}}); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.opened("sentry"); len(o.Headers) != 0 {
		t.Fatalf("not removed: %+v", o)
	}
	// It survives a restart of the hub.
	if _, err := s.Update("sentry", Server{Transport: TransportHTTP, URL: "https://a.example.com/", Enabled: true, Headers: []Var{{Name: "X-Key", Value: "k1"}}}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(s.st, s.box)
	if err != nil {
		t.Fatal(err)
	}
	if o, err := s2.opened("sentry"); err != nil || o.Headers[0].Value != "k1" {
		t.Fatalf("after restart %+v %v", o, err)
	}
}

func TestBadServersAreRefused(t *testing.T) {
	s, _ := openStore(t)
	for name, sv := range map[string]Server{
		"reserved":     {ID: "nextboard", Transport: TransportHTTP, URL: "https://a.example.com"},
		"bad name":     {ID: "Bad Name", Transport: TransportHTTP, URL: "https://a.example.com"},
		"bad dots":     {ID: "a.b", Transport: TransportHTTP, URL: "https://a.example.com"},
		"no url":       {ID: "a", Transport: TransportHTTP},
		"not http":     {ID: "a", Transport: TransportHTTP, URL: "file:///etc/passwd"},
		"url secret":   {ID: "a", Transport: TransportHTTP, URL: "https://me:pw@a.example.com"},
		"no command":   {ID: "a", Transport: TransportStdio},
		"transport":    {ID: "a", Transport: "ftp"},
		"bad header":   {ID: "a", Transport: TransportHTTP, URL: "https://a.example.com", Headers: []Var{{Name: "X Y", Value: "v"}}},
		"no value":     {ID: "a", Transport: TransportHTTP, URL: "https://a.example.com", Headers: []Var{{Name: "X-Y"}}},
		"bad env":      {ID: "a", Transport: TransportStdio, Command: "x", Env: []Var{{Name: "A=B", Value: "v"}}},
		"shell in env": {ID: "a", Transport: TransportStdio, Command: "x", Env: []Var{{Name: "A;rm", Value: "v"}}},
		"twice":        {ID: "a", Transport: TransportStdio, Command: "x", Env: []Var{{Name: "K", Value: "1"}, {Name: "K", Value: "2"}}},
	} {
		var bad *BadError
		if _, err := s.Create(sv); !errors.As(err, &bad) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(s.List()) != 0 {
		t.Fatalf("saved: %+v", s.List())
	}
	if _, err := s.Create(Server{ID: "a", Transport: TransportStdio, Command: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(Server{ID: "a", Transport: TransportStdio, Command: "x"}); !errors.Is(err, ErrExists) {
		t.Fatalf("twice: %v", err)
	}
	if err := s.Delete("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
}

func TestForLaunchGivesWhatReachesThisConsole(t *testing.T) {
	s, _ := openStore(t)
	add := func(sv Server) {
		t.Helper()
		if _, err := s.Create(sv); err != nil {
			t.Fatal(err)
		}
	}
	add(Server{ID: "all-http", Transport: TransportHTTP, URL: "https://a.example.com", Enabled: true, OnServers: true, Headers: []Var{{Name: "Authorization", Value: "tok-a"}}})
	add(Server{ID: "local-cmd", Transport: TransportStdio, Command: "fs-server", Args: []string{"--root", "/"}, Enabled: true, Env: []Var{{Name: "API_KEY", Value: "tok-b"}}})
	add(Server{ID: "other-project", Transport: TransportHTTP, URL: "https://b.example.com", Enabled: true, OnServers: true, Projects: []string{"p_other"}})
	add(Server{ID: "off", Transport: TransportHTTP, URL: "https://c.example.com", Enabled: false, OnServers: true})
	names := func(project string, remote bool) string {
		list, _ := s.ForLaunch(project, remote)
		out := make([]string, len(list))
		for i, m := range list {
			out[i] = m.Name
		}
		return strings.Join(out, ",")
	}
	if got := names("p_mine", false); got != "all-http,local-cmd" {
		t.Fatalf("on This PC: %s", got)
	}
	if got := names("p_other", false); got != "all-http,local-cmd,other-project" {
		t.Fatalf("its project: %s", got)
	}
	// A command would act on This PC, not on the server the agent works on.
	if got := names("p_mine", true); got != "all-http" {
		t.Fatalf("on a server: %s", got)
	}
	list, env := s.ForLaunch("p_mine", false)
	if len(env) != 2 || list[0].Headers[0].Name != "Authorization" || list[1].Env[0].Name != "API_KEY" {
		t.Fatalf("%+v %v", list, env)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, list[0].Headers[0].Var+"=tok-a") || !strings.Contains(joined, list[1].Env[0].Var+"=tok-b") {
		t.Fatalf("env %v", env)
	}
	b, _ := json.Marshal(list)
	if strings.Contains(string(b), "tok-") {
		t.Fatalf("values must travel only in the environment: %s", b)
	}
}

// A small MCP server over HTTP, answering as JSON or as a stream.
func fakeMCP(t *testing.T, stream bool, wantHeader string) *httptest.Server {
	t.Helper()
	sessions := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantHeader != "" && r.Header.Get("Authorization") != wantHeader {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if len(msg.ID) == 0 {
			if r.Header.Get("Mcp-Session-Id") != "sess-1" {
				t.Errorf("no session on %s", msg.Method)
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result string
		switch msg.Method {
		case "initialize":
			sessions++
			w.Header().Set("Mcp-Session-Id", "sess-1")
			result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"x","version":"1"}}`
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "sess-1" {
				t.Errorf("no session on tools/list")
			}
			result = `{"tools":[{"name":"zeta"},{"name":"alpha"}]}`
		}
		body := `{"jsonrpc":"2.0","id":` + string(msg.ID) + `,"result":` + result + `}`
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: " + body + "\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestProbeHTTP(t *testing.T) {
	s, _ := openStore(t)
	for name, stream := range map[string]bool{"json": false, "stream": true} {
		srv := fakeMCP(t, stream, "Bearer tok")
		defer srv.Close()
		if _, err := s.Create(Server{ID: name, Transport: TransportHTTP, URL: srv.URL, Enabled: true, Headers: []Var{{Name: "Authorization", Value: "Bearer tok"}}}); err != nil {
			t.Fatal(err)
		}
		r, err := s.Probe(context.Background(), name)
		if err != nil || !r.OK || strings.Join(r.Tools, ",") != "alpha,zeta" {
			t.Fatalf("%s: %+v %v", name, r, err)
		}
	}
	// A login it wants is told apart from a server that is down.
	srv := fakeMCP(t, false, "Bearer other")
	defer srv.Close()
	if _, err := s.Create(Server{ID: "login", Transport: TransportHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Probe(context.Background(), "login"); r.OK || !r.NeedsLogin {
		t.Fatalf("login: %+v", r)
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	if _, err := s.Create(Server{ID: "down", Transport: TransportHTTP, URL: url}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Probe(context.Background(), "down"); r.OK || r.NeedsLogin || r.Error == "" {
		t.Fatalf("down: %+v", r)
	}
	if _, err := s.Probe(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

const stdioServer = `while read -r line; do
  case "$line" in
    *'"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"x","version":"1"}}}' ;;
    *'"tools/list"'*) printf '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"%s"}]}}\n' "$MY_KEY" ;;
  esac
done`

func TestProbeCommand(t *testing.T) {
	s, _ := openStore(t)
	if _, err := s.Create(Server{ID: "cmd", Transport: TransportStdio, Command: "sh", Args: []string{"-c", stdioServer}, Env: []Var{{Name: "MY_KEY", Value: "from-the-env"}}}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Probe(context.Background(), "cmd")
	if err != nil || !r.OK || strings.Join(r.Tools, ",") != "from-the-env" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := s.Create(Server{ID: "dies", Transport: TransportStdio, Command: "sh", Args: []string{"-c", "echo bad config >&2; exit 3"}}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Probe(context.Background(), "dies"); r.OK || !strings.Contains(r.Error, "bad config") {
		t.Fatalf("what it said must show: %+v", r)
	}
	if _, err := s.Create(Server{ID: "missing", Transport: TransportStdio, Command: "/no/such/command"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Probe(context.Background(), "missing"); r.OK || r.Error == "" {
		t.Fatalf("missing: %+v", r)
	}
}

// A server that never answers must not hang the hub or stay behind.
func TestProbeCommandThatHangsIsKilled(t *testing.T) {
	s, _ := openStore(t)
	marker := filepath.Join(t.TempDir(), "child.pid")
	if _, err := s.Create(Server{ID: "hang", Transport: TransportStdio, Command: "sh", Args: []string{"-c", `sleep 300 & echo $! > ` + marker + `; wait`}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, err := s.Probe(ctx, "hang")
	if err != nil || r.OK || time.Since(start) > 6*time.Second {
		t.Fatalf("%+v %v after %s", r, err, time.Since(start))
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(string(b))
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat("/proc/" + pid); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %s stayed behind", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
