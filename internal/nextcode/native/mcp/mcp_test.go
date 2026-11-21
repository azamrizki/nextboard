package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain runs the test binary as a stdio MCP server when asked (MCP_FAKE=1).
func TestMain(m *testing.M) {
	if os.Getenv("MCP_FAKE") == "1" {
		fakeStdio()
		return
	}
	os.Exit(m.Run())
}

// answer is a fake server's answer to one request.
func answer(method string, params json.RawMessage, secret string) any {
	switch method {
	case "initialize":
		return map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake"}}
	case "tools/list":
		var p struct{ Cursor string }
		_ = json.Unmarshal(params, &p)
		if p.Cursor == "" {
			return map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Echoes", "inputSchema": map[string]any{"type": "object"}}}, "nextCursor": "2"}
		}
		return map[string]any{"tools": []any{map[string]any{"name": "secret", "description": "Says the secret"}}}
	case "tools/call":
		var p struct {
			Name      string
			Arguments map[string]any
		}
		_ = json.Unmarshal(params, &p)
		if p.Name == "secret" {
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": secret}}}
		}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": fmt.Sprint("echo ", p.Arguments["text"])}}, "isError": p.Arguments["text"] == "fail"}
	}
	return nil
}

func fakeStdio() {
	dec := json.NewDecoder(os.Stdin)
	for {
		var m message
		if dec.Decode(&m) != nil {
			return
		}
		if len(m.ID) == 0 {
			continue
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": answer(m.Method, m.Params, os.Getenv("FAKE_SECRET"))})
		os.Stdout.Write(append(b, '\n'))
	}
}

func check(t *testing.T, c *Client, wantSecret string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := c.Tools(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "echo" || list[1].Name != "secret" {
		t.Fatalf("tools %+v %v", list, err)
	}
	if text, isErr, err := c.Call(ctx, "echo", json.RawMessage(`{"text":"hi"}`)); err != nil || isErr || text != "echo hi" {
		t.Fatalf("call %q %v %v", text, isErr, err)
	}
	if _, isErr, _ := c.Call(ctx, "echo", json.RawMessage(`{"text":"fail"}`)); !isErr {
		t.Fatal("a failed call")
	}
	if text, _, _ := c.Call(ctx, "secret", nil); text != wantSecret {
		t.Fatalf("the value of its variable %q", text)
	}
}

func TestStdio(t *testing.T) {
	env := append(os.Environ(), "MCP_FAKE=1")
	vals := map[string]string{"NB_MCP_0_FAKE_0": "s3cret"}
	c, err := Connect(context.Background(), Server{Name: "fake", Command: os.Args[0], Env: []Var{{Name: "FAKE_SECRET", Var: "NB_MCP_0_FAKE_0"}}}, env, func(k string) string { return vals[k] })
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	check(t, c, "s3cret")
}

func TestStreamableHTTP(t *testing.T) {
	var sawSession, sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m message
		_ = json.Unmarshal(b, &m)
		sawAuth = sawAuth || r.Header.Get("Authorization") == "Bearer tok"
		if m.Method != "initialize" && r.Header.Get("Mcp-Session-Id") == "sess-1" {
			sawSession = true
		}
		if len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Mcp-Session-Id", "sess-1")
		res, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": answer(m.Method, m.Params, r.Header.Get("Authorization"))})
		if m.Method == "tools/call" {
			// an SSE stream: a notification first, then the answer
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", `{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", res)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(res)
	}))
	defer srv.Close()
	c, err := Connect(context.Background(), Server{Name: "web", URL: srv.URL, Headers: []Var{{Name: "Authorization", Var: "NB_MCP_1_WEB_0"}}}, nil,
		func(k string) string { return map[string]string{"NB_MCP_1_WEB_0": "Bearer tok"}[k] })
	if err != nil {
		t.Fatal(err)
	}
	check(t, c, "Bearer tok")
	if !sawSession || !sawAuth {
		t.Fatalf("session %v auth %v", sawSession, sawAuth)
	}
	if _, err := Connect(context.Background(), Server{Name: "x"}, nil, os.Getenv); err == nil || !strings.Contains(err.Error(), "neither") {
		t.Fatalf("no URL or command %v", err)
	}
}
