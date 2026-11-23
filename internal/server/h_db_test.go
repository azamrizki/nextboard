package server

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// The database browser's API (docs/API-db.md) against a Redis stand-in: connections of a
// project, the key browser, the console's confirmations, and what agents may do.

func dbTestHub(t *testing.T) (*testHub, string, *miniredis.Miniredis, map[string]any) {
	t.Helper()
	h := newTestHub(t)
	h.setup()
	st, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "shop"}, nil)
	if st != 201 && st != 200 {
		t.Fatalf("project %d %v", st, pr)
	}
	pid := pr["id"].(string)
	mr := miniredis.RunT(t)
	host, port, _ := net.SplitHostPort(mr.Addr())
	p, _ := strconv.Atoi(port)
	st, c := h.do("POST", "/api/projects/"+pid+"/dbs", map[string]any{
		"connection": map[string]any{"name": "cache", "driver": "redis", "host": host, "port": p, "env": "local", "savePassword": true,
			"agents": map[string]any{"access": "ask", "masked": []string{"*token*"}}},
		"secrets": map[string]any{"password": ""},
	}, nil)
	if st != 201 {
		t.Fatalf("create %d %v", st, c)
	}
	return h, pid, mr, c
}

func TestDBConnectionsOfAProject(t *testing.T) {
	h, pid, mr, c := dbTestHub(t)
	id := c["id"].(string)
	mr.Set("session:a", "1")
	mr.Set("session:b", "2")

	st, list := h.do("GET", "/api/projects/"+pid+"/dbs", nil, nil)
	if st != 200 || len(list["connections"].([]any)) != 1 {
		t.Fatalf("list %d %v", st, list)
	}
	st, sc := h.do("GET", "/api/dbs/"+id+"/schema", nil, nil)
	if st != 200 || sc["kv"] == nil {
		t.Fatalf("schema %d %v", st, sc)
	}
	st, keys := h.do("GET", "/api/dbs/"+id+"/keys?match=session:*", nil, nil)
	if st != 200 || len(keys["keys"].([]any)) != 2 {
		t.Fatalf("keys %d %v", st, keys)
	}
	st, out := h.do("POST", "/api/dbs/"+id+"/command", map[string]any{"line": "GET session:a"}, nil)
	if st != 200 || out["output"] != `"1"` {
		t.Errorf("GET %d %v", st, out)
	}
	// FLUSHDB: 409 confirm_name, then with the name typed
	st, out = h.do("POST", "/api/dbs/"+id+"/command", map[string]any{"line": "FLUSHDB"}, nil)
	if st != 409 || out["code"] != "confirm_name" {
		t.Fatalf("FLUSHDB %d %v", st, out)
	}
	if st, _ := h.do("POST", "/api/dbs/"+id+"/command", map[string]any{"line": "FLUSHDB", "confirmName": "cache"}, nil); st != 200 || mr.Exists("session:a") {
		t.Errorf("FLUSHDB with the name: %d", st)
	}
	// read-only refuses with 403, after the edit dialog saves it
	c["security"] = map[string]any{"readOnly": true}
	if st, _ := h.do("PUT", "/api/dbs/"+id, map[string]any{"connection": c}, nil); st != 200 {
		t.Fatalf("replace %d", st)
	}
	if st, out := h.do("POST", "/api/dbs/"+id+"/command", map[string]any{"line": "SET x 1"}, nil); st != 403 || out["code"] != "read_only" {
		t.Errorf("SET on read-only %d %v", st, out)
	}
	// the test of a form, and the connection gone with its project's delete
	host, port, _ := net.SplitHostPort(mr.Addr())
	p, _ := strconv.Atoi(port)
	if st, r := h.do("POST", "/api/dbs/test", map[string]any{"connection": map[string]any{"driver": "redis", "host": host, "port": p}}, nil); st != 200 || r["ok"] != true {
		t.Errorf("test %d %v", st, r)
	}
	if st, r := h.do("POST", "/api/dbs/test", map[string]any{"connection": map[string]any{"driver": "redis", "host": "127.0.0.1", "port": 1}}, nil); st != 200 || r["ok"] != false || !strings.Contains(r["error"].(string), "nothing listens") {
		t.Errorf("test of a closed port %d %v", st, r)
	}
	h.srv.dbRemoveProject(pid)
	if st, _ := h.do("GET", "/api/dbs/"+id, nil, nil); st != 404 {
		t.Errorf("connection after its project: %d", st)
	}
}

func TestDBConnectionsFileHasNoPasswords(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	_, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "s"}, nil)
	st, c := h.do("POST", "/api/projects/"+pr["id"].(string)+"/dbs", map[string]any{
		"connection": map[string]any{"name": "shop", "driver": "mysql", "user": "shop", "savePassword": true},
		"secrets":    map[string]any{"password": "hunter2-db"},
	}, nil)
	if st != 201 || c["hasPassword"] != true || c["password"] != nil {
		t.Fatalf("create %d %v", st, c)
	}
	raw, _ := os.ReadFile(filepath.Join(h.srv.st.Dir, "dbs", "connections.json"))
	if strings.Contains(string(raw), "hunter2-db") {
		t.Error("the password is in connections.json as typed")
	}
}

func TestAgentsReadMaskedAndWriteWithApproval(t *testing.T) {
	h, pid, mr, c := dbTestHub(t)
	s := h.srv
	ctx := context.Background()
	mr.HSet("user:1", "name", "Ani", "api_token", "tok-secret")
	tgt := remoteTarget{projectID: pid, databases: true, sessionID: "s_agent"}

	out, isErr := s.dbTool(ctx, tgt, "db_connections", nil)
	if isErr || !strings.Contains(out, "cache") || !strings.Contains(out, "ask the owner to write") {
		t.Fatalf("connections %q", out)
	}
	args := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	if out, isErr := s.dbTool(ctx, tgt, "redis_command", args(map[string]any{"connection": "cache", "command": "HGET user:1 name"})); isErr || out != `"Ani"` {
		t.Errorf("read %q %v", out, isErr)
	}
	// a write waits for the owner: approve it once
	done := make(chan string, 1)
	go func() {
		out, _ := s.dbTool(ctx, tgt, "redis_command", args(map[string]any{"connection": "cache", "command": "SET greeting hello", "why": "a test"}))
		done <- out
	}()
	var reqID string
	for i := 0; i < 100 && reqID == ""; i++ {
		time.Sleep(20 * time.Millisecond)
		_, list := h.do("GET", "/api/db-requests", nil, nil)
		for _, x := range list["items"].([]any) {
			if m := x.(map[string]any); m["state"] == "waiting" {
				reqID = m["id"].(string)
			}
		}
	}
	if reqID == "" {
		t.Fatal("no request waits")
	}
	if mr.Exists("greeting") {
		t.Fatal("the write ran before approval")
	}
	if st, _ := h.do("POST", "/api/db-requests/"+reqID+"/approve", map[string]any{}, nil); st != 200 {
		t.Fatalf("approve %d", st)
	}
	select {
	case out := <-done:
		if !strings.Contains(out, "OK") || !mr.Exists("greeting") {
			t.Errorf("after approval %q", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the agent's call did not end")
	}
	// FLUSHDB from an agent needs the owner to type the name
	go func() {
		out, _ := s.dbTool(ctx, tgt, "redis_command", args(map[string]any{"connection": "cache", "command": "FLUSHDB", "why": "clean"}))
		done <- out
	}()
	reqID = ""
	for i := 0; i < 100 && reqID == ""; i++ {
		time.Sleep(20 * time.Millisecond)
		_, list := h.do("GET", "/api/db-requests", nil, nil)
		for _, x := range list["items"].([]any) {
			if m := x.(map[string]any); m["state"] == "waiting" && m["danger"] != nil {
				reqID = m["id"].(string)
			}
		}
	}
	if st, out := h.do("POST", "/api/db-requests/"+reqID+"/approve", map[string]any{}, nil); st != 409 || out["code"] != "confirm_name" {
		t.Errorf("approve FLUSHDB without the name: %d %v", st, out)
	}
	if st, _ := h.do("POST", "/api/db-requests/"+reqID+"/deny", map[string]any{"note": "no"}, nil); st != 200 {
		t.Errorf("deny %d", st)
	}
	if out := <-done; !strings.Contains(out, "said no") || !mr.Exists("greeting") {
		t.Errorf("after deny %q", out)
	}
	// the activity log keeps what agents did
	_, act := h.do("GET", "/api/dbs/"+c["id"].(string)+"/activity", nil, nil)
	if n := len(act["items"].([]any)); n < 3 {
		t.Errorf("activity has %d items", n)
	}
	// none access: the tools do not see it
	c["agents"] = map[string]any{"access": "none"}
	h.do("PUT", "/api/dbs/"+c["id"].(string), map[string]any{"connection": c}, nil)
	if s.dbAgentsMay(pid) {
		t.Error("agents may still use a connection shared with none")
	}
	if out, isErr := s.dbTool(ctx, tgt, "redis_command", args(map[string]any{"connection": "cache", "command": "GET greeting"})); !isErr || !strings.Contains(out, "does not share") {
		t.Errorf("none access %q", out)
	}
}
