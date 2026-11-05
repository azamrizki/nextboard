package mcpreg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Trying a server: the hub speaks to it the way a CLI would, once, so that a typo
// shows here and not as a tool that is quietly missing in every console.

const (
	probeHTTPTimeout  = 20 * time.Second
	probeStdioTimeout = 45 * time.Second // npx may download first
	probeBodyLimit    = 4 << 20
	probeStderrLimit  = 2 << 10
	probeProtocol     = "2025-06-18"
)

// ProbeResult says whether a server answers, and with what.
type ProbeResult struct {
	OK    bool     `json:"ok"`
	Tools []string `json:"tools"`
	// NeedsLogin: it asks for a login the hub cannot do for it.
	NeedsLogin bool   `json:"needsLogin,omitempty"`
	Error      string `json:"error,omitempty"`
	TookMs     int64  `json:"tookMs"`
}

// Probe tries one server.
func (s *Store) Probe(ctx context.Context, id string) (ProbeResult, error) {
	sv, err := s.opened(id)
	if err != nil {
		return ProbeResult{}, err
	}
	start := time.Now()
	var r ProbeResult
	if sv.Transport == TransportHTTP {
		c, cancel := context.WithTimeout(ctx, probeHTTPTimeout)
		defer cancel()
		r = probeHTTP(c, sv)
	} else {
		c, cancel := context.WithTimeout(ctx, probeStdioTimeout)
		defer cancel()
		r = probeStdio(c, sv, s.st.Path("."))
	}
	r.TookMs = time.Since(start).Milliseconds()
	if r.Tools == nil {
		r.Tools = []string{}
	}
	return r, nil
}

func initializeParams() map[string]any {
	return map[string]any{"protocolVersion": probeProtocol, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "nextboard", "version": "1"}}
}

type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func toolNames(raw json.RawMessage) ([]string, error) {
	var v struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(v.Tools))
	for _, t := range v.Tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out, nil
}

// ---- HTTP ----

type httpProbe struct {
	ctx     context.Context
	sv      Server
	session string
}

// call sends one message; want is the id of the answer to wait for ("" = none).
func (p *httpProbe) call(id int, method string, params any) (*rpcReply, int, error) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if id > 0 {
		msg["id"] = id
	}
	if params != nil {
		msg["params"] = params
	}
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(p.ctx, http.MethodPost, p.sv.URL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", probeProtocol)
	for _, h := range p.sv.Headers {
		req.Header.Set(h.Name, h.Value)
	}
	if p.session != "" {
		req.Header.Set("Mcp-Session-Id", p.session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		p.session = s
	}
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, probeBodyLimit))
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if id == 0 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, probeBodyLimit))
		return nil, resp.StatusCode, nil
	}
	body2 := io.LimitReader(resp.Body, probeBodyLimit)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(body2)
		sc.Buffer(make([]byte, 64<<10), probeBodyLimit)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data:")
			if !ok {
				continue
			}
			var r rpcReply
			if json.Unmarshal([]byte(strings.TrimSpace(data)), &r) == nil && string(r.ID) == fmt.Sprint(id) {
				return &r, resp.StatusCode, nil
			}
		}
		return nil, resp.StatusCode, errors.New("stream ended without an answer")
	}
	var r rpcReply
	if err := json.NewDecoder(body2).Decode(&r); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("not JSON-RPC: %v", err)
	}
	return &r, resp.StatusCode, nil
}

func probeHTTP(ctx context.Context, sv Server) ProbeResult {
	p := &httpProbe{ctx: ctx, sv: sv}
	r, status, err := p.call(1, "initialize", initializeParams())
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ProbeResult{NeedsLogin: true, Error: fmt.Sprintf("the server answers %d: it wants a login", status)}
	}
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	if r.Error != nil {
		return ProbeResult{Error: r.Error.Message}
	}
	_, _, _ = p.call(0, "notifications/initialized", nil)
	r, _, err = p.call(2, "tools/list", nil)
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	if r.Error != nil {
		return ProbeResult{Error: r.Error.Message}
	}
	tools, err := toolNames(r.Result)
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	return ProbeResult{OK: true, Tools: tools}
}

// ---- a command ----

type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > probeStderrLimit {
		t.b = t.b[len(t.b)-probeStderrLimit:]
	}
	return len(p), nil
}

func probeStdio(ctx context.Context, sv Server, dir string) ProbeResult {
	cmd := exec.CommandContext(ctx, sv.Command, sv.Args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for _, v := range sv.Env {
		cmd.Env = append(cmd.Env, v.Name+"="+v.Value)
	}
	// Its children go with it: this host runs other services.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return ProbeResult{Error: err.Error()}
	}
	var once sync.Once
	end := func() {
		once.Do(func() {
			in.Close()
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		})
	}
	defer end()
	fail := func(msg string) ProbeResult {
		end() // what it said is complete once it is gone
		if t := strings.TrimSpace(string(stderr.b)); t != "" {
			msg += ": " + t
		}
		return ProbeResult{Error: msg}
	}
	send := func(id int, method string, params any) error {
		msg := map[string]any{"jsonrpc": "2.0", "method": method}
		if id > 0 {
			msg["id"] = id
		}
		if params != nil {
			msg["params"] = params
		}
		b, _ := json.Marshal(msg)
		_, err := in.Write(append(b, '\n'))
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), probeBodyLimit)
	wait := func(id int) (*rpcReply, error) {
		for sc.Scan() {
			var r rpcReply
			if json.Unmarshal(sc.Bytes(), &r) == nil && string(r.ID) == fmt.Sprint(id) {
				return &r, nil
			}
		}
		if ctx.Err() != nil {
			return nil, errors.New("no answer in time")
		}
		return nil, errors.New("the command ended without an answer")
	}
	if err := send(1, "initialize", initializeParams()); err != nil {
		return fail(err.Error())
	}
	r, err := wait(1)
	if err != nil {
		return fail(err.Error())
	}
	if r.Error != nil {
		return fail(r.Error.Message)
	}
	if err := send(0, "notifications/initialized", nil); err != nil {
		return fail(err.Error())
	}
	if err := send(2, "tools/list", nil); err != nil {
		return fail(err.Error())
	}
	if r, err = wait(2); err != nil {
		return fail(err.Error())
	}
	if r.Error != nil {
		return fail(r.Error.Message)
	}
	tools, err := toolNames(r.Result)
	if err != nil {
		return fail(err.Error())
	}
	return ProbeResult{OK: true, Tools: tools}
}
