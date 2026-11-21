// Package mcp is the client side of MCP for NextCode's own agent (docs/API-agents.md §30, Clean
// NextCode): the servers Settings › NextCode gives it, over stdio (a command it starts) or
// HTTP (Streamable HTTP: JSON or an SSE stream back), for their tools. Only what a tool call
// needs: initialize, tools/list, tools/call; a request of the server's is answered "not
// supported", a notification ignored.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion is the MCP version the client asks for.
const ProtocolVersion = "2025-06-18"

// Var is a header or environment variable whose value is in the process environment variable
// Var (the hub never puts a value on a command line).
type Var struct {
	Name string `json:"name"`
	Var  string `json:"var"`
}

// Server is one server as the hub hands it (NB_NEXTCODE_MCP): a URL or a command.
type Server struct {
	Name    string   `json:"name"`
	URL     string   `json:"url,omitempty"`
	Headers []Var    `json:"headers,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []Var    `json:"env,omitempty"`
}

// Tool is one tool of a server.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Client talks to one server.
type Client struct {
	srv  Server
	conn transport
	next atomic.Int64
}

type transport interface {
	call(ctx context.Context, id int64, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	close()
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Connect starts or reaches the server and initializes the session. environ is the environment
// a command starts with; getenv reads the values of the server's variables.
func Connect(ctx context.Context, srv Server, environ []string, getenv func(string) string) (*Client, error) {
	c := &Client{srv: srv}
	var err error
	switch {
	case srv.URL != "":
		headers := map[string]string{}
		for _, h := range srv.Headers {
			headers[h.Name] = getenv(h.Var)
		}
		c.conn = &httpConn{url: srv.URL, headers: headers, client: &http.Client{Timeout: 5 * time.Minute}}
	case srv.Command != "":
		env := append([]string{}, environ...)
		for _, v := range srv.Env {
			env = append(env, v.Name+"="+getenv(v.Var))
		}
		c.conn, err = startStdio(srv.Command, srv.Args, env)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("neither a URL nor a command")
	}
	init := map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "nextcode", "version": "1"}}
	if _, err := c.conn.call(ctx, c.next.Add(1), "initialize", init); err != nil {
		c.conn.close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := c.conn.notify(ctx, "notifications/initialized", nil); err != nil {
		c.conn.close()
		return nil, err
	}
	return c, nil
}

// Tools lists the server's tools, every page.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	cursor := ""
	for range 50 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.conn.call(ctx, c.next.Add(1), "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Tools...)
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// Call calls a tool: its text, and whether the server says it failed.
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	raw, err := c.conn.call(ctx, c.next.Add(1), "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", true, err
	}
	var res struct {
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			MimeType string          `json:"mimeType"`
			Resource json.RawMessage `json:"resource"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", true, err
	}
	var b strings.Builder
	for _, x := range res.Content {
		switch x.Type {
		case "text":
			b.WriteString(x.Text)
		case "resource":
			b.Write(x.Resource)
		default:
			fmt.Fprintf(&b, "[%s %s]", x.Type, x.MimeType)
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 && len(res.Structured) > 0 {
		b.Write(res.Structured)
	}
	return strings.TrimRight(b.String(), "\n"), res.IsError, nil
}

// Close ends the session (a command is stopped).
func (c *Client) Close() { c.conn.close() }

// ---- stdio ----

type stdioConn struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	wmu     sync.Mutex
	mu      sync.Mutex
	waiting map[string]chan message
	done    chan struct{}
}

func startStdio(command string, args, env []string) (*stdioConn, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = env
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &stdioConn{cmd: cmd, in: in, waiting: map[string]chan message{}, done: make(chan struct{})}
	go s.read(out)
	return s, nil
}

func (s *stdioConn) read(out io.Reader) {
	defer close(s.done)
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Method != "" {
			if len(m.ID) > 0 { // a request of the server's: none is supported
				s.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "not supported"}})
			}
			continue
		}
		s.mu.Lock()
		ch := s.waiting[string(m.ID)]
		delete(s.waiting, string(m.ID))
		s.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
}

func (s *stdioConn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.in.Write(append(b, '\n'))
	return err
}

func (s *stdioConn) call(ctx context.Context, id int64, method string, params any) (json.RawMessage, error) {
	key := fmt.Sprint(id)
	ch := make(chan message, 1)
	s.mu.Lock()
	s.waiting[key] = ch
	s.mu.Unlock()
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := s.write(req); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		return result(m)
	case <-s.done:
		return nil, errors.New("the server ended")
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.waiting, key)
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (s *stdioConn) notify(_ context.Context, method string, params any) error {
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	return s.write(req)
}

func (s *stdioConn) close() {
	_ = s.in.Close()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
}

// ---- Streamable HTTP ----

type httpConn struct {
	url     string
	headers map[string]string
	client  *http.Client
	mu      sync.Mutex
	session string
}

func (h *httpConn) post(ctx context.Context, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	h.mu.Lock()
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}
	h.mu.Unlock()
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.mu.Lock()
		h.session = sid
		h.mu.Unlock()
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

func (h *httpConn) call(ctx context.Context, id int64, method string, params any) (json.RawMessage, error) {
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	resp, err := h.post(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	want := fmt.Sprint(id)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// the answer is one of the events; the others (notifications) are passed over
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 32<<20)
		var data strings.Builder
		for sc.Scan() {
			line := sc.Text()
			if d, ok := strings.CutPrefix(line, "data:"); ok {
				data.WriteString(strings.TrimPrefix(d, " "))
				continue
			}
			if line != "" || data.Len() == 0 {
				continue
			}
			var m message
			if json.Unmarshal([]byte(data.String()), &m) == nil && m.Method == "" && string(m.ID) == want {
				return result(m)
			}
			data.Reset()
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("the stream ended without an answer")
	}
	var m message
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&m); err != nil {
		return nil, err
	}
	return result(m)
}

func (h *httpConn) notify(ctx context.Context, method string, params any) error {
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	resp, err := h.post(ctx, req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (h *httpConn) close() {}

func result(m message) (json.RawMessage, error) {
	if m.Error != nil {
		return nil, fmt.Errorf("%s (%d)", m.Error.Message, m.Error.Code)
	}
	return m.Result, nil
}
