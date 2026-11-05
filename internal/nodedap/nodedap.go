// Package nodedap is a debug adapter for Node.js (docs/API-studio.md §7.5). It speaks the Debug
// Adapter Protocol on stdio and Node's own inspector on the other side, so that debugging needs
// nothing but node. The hub starts it as `nextboard dap-node`, on This PC only.
package nodedap

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	threadID     = 1
	inspectWait  = 15 * time.Second
	cdpWait      = 15 * time.Second
	maxValueText = 4096
)

var inspectorURL = regexp.MustCompile(`Debugger listening on (ws://\S+)`)

type message struct {
	Seq       int             `json:"seq,omitempty"`
	Type      string          `json:"type"`
	Command   string          `json:"command,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Serve runs one debug session until the client disconnects or its input ends.
func Serve(in io.Reader, out io.Writer) error {
	a := &adapter{out: out, refs: map[int]string{}, pending: map[int]chan cdpReply{}, cdpToDap: map[string]int{}, bpIDs: map[string][]string{}, scripts: map[string]string{}}
	defer a.shutdown()
	r := bufio.NewReader(in)
	for {
		body, err := readFrame(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var m message
		if err := json.Unmarshal(body, &m); err != nil || m.Type != "request" {
			continue
		}
		done := a.handle(m)
		if done {
			return nil
		}
	}
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			if length, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
				return nil, err
			}
		}
	}
	if length < 0 {
		return nil, errors.New("no Content-Length")
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}

type cdpReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type callFrame struct {
	CallFrameID  string `json:"callFrameId"`
	FunctionName string `json:"functionName"`
	URL          string `json:"url"`
	Location     struct {
		ScriptID     string `json:"scriptId"`
		LineNumber   int    `json:"lineNumber"`
		ColumnNumber int    `json:"columnNumber"`
	} `json:"location"`
	ScopeChain []struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	} `json:"scopeChain"`
}

type adapter struct {
	out   io.Writer
	outMu sync.Mutex
	seq   int

	cmd      *exec.Cmd
	ws       *websocket.Conn
	wsMu     sync.Mutex
	cdpSeq   int
	cdpMu    sync.Mutex
	pending  map[int]chan cdpReply
	exitOnce sync.Once

	mu          sync.Mutex
	frames      []callFrame
	paused      bool
	refs        map[int]string
	nextRef     int
	cdpToDap    map[string]int
	bpIDs       map[string][]string
	scripts     map[string]string // script id → its URL
	nextBP      int
	stopOnEntry bool
	started     bool
	waitEntry   bool
	noDebug     bool
	terminated  bool
}

func (a *adapter) send(v map[string]any) {
	a.outMu.Lock()
	defer a.outMu.Unlock()
	a.seq++
	v["seq"] = a.seq
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(a.out, "Content-Length: %d\r\n\r\n", len(b))
	_, _ = a.out.Write(b)
}

func (a *adapter) event(name string, body map[string]any) {
	a.send(map[string]any{"type": "event", "event": name, "body": body})
}

func (a *adapter) reply(m message, body any) {
	if body == nil {
		body = map[string]any{}
	}
	a.send(map[string]any{"type": "response", "request_seq": m.Seq, "success": true, "command": m.Command, "body": body})
}

func (a *adapter) fail(m message, err error) {
	a.send(map[string]any{"type": "response", "request_seq": m.Seq, "success": false, "command": m.Command, "message": err.Error(),
		"body": map[string]any{"error": map[string]any{"id": 1, "format": err.Error()}}})
}

// handle answers one request; true ends the session.
func (a *adapter) handle(m message) bool {
	var err error
	var body any
	switch m.Command {
	case "initialize":
		body = map[string]any{
			"supportsConfigurationDoneRequest": true,
			"supportsEvaluateForHovers":        true,
			"supportsConditionalBreakpoints":   true,
			"supportsLogPoints":                true,
			"supportsTerminateRequest":         true,
			"exceptionBreakpointFilters": []map[string]any{
				{"filter": "uncaught", "label": "Uncaught Exceptions", "default": true},
				{"filter": "all", "label": "All Exceptions"},
			},
		}
	case "launch":
		err = a.launch(m.Arguments)
		if err == nil {
			a.reply(m, nil)
			a.event("initialized", map[string]any{})
			return false
		}
	case "attach":
		err = errors.New("attach is not supported: start the program from here")
	case "configurationDone":
		err = a.configured()
	case "setBreakpoints":
		body, err = a.setBreakpoints(m.Arguments)
	case "setExceptionBreakpoints":
		err = a.setExceptions(m.Arguments)
	case "threads":
		body = map[string]any{"threads": []map[string]any{{"id": threadID, "name": "main"}}}
	case "stackTrace":
		body = a.stackTrace()
	case "scopes":
		body, err = a.scopes(m.Arguments)
	case "variables":
		body, err = a.variables(m.Arguments)
	case "evaluate":
		body, err = a.evaluate(m.Arguments)
	case "continue":
		a.resumed()
		err = a.step("Debugger.resume")
		body = map[string]any{"allThreadsContinued": true}
	case "next":
		a.resumed()
		err = a.step("Debugger.stepOver")
	case "stepIn":
		a.resumed()
		err = a.step("Debugger.stepInto")
	case "stepOut":
		a.resumed()
		err = a.step("Debugger.stepOut")
	case "pause":
		err = a.step("Debugger.pause")
	case "terminate":
		a.reply(m, nil)
		a.kill(syscall.SIGTERM)
		return false
	case "disconnect":
		a.reply(m, nil)
		a.kill(syscall.SIGKILL)
		return true
	default:
		err = fmt.Errorf("%s is not supported", m.Command)
	}
	if err != nil {
		a.fail(m, err)
	} else {
		a.reply(m, body)
	}
	return false
}

func (a *adapter) launch(raw json.RawMessage) error {
	var c struct {
		Program           string            `json:"program"`
		Args              []string          `json:"args"`
		Cwd               string            `json:"cwd"`
		Env               map[string]string `json:"env"`
		RuntimeExecutable string            `json:"runtimeExecutable"`
		RuntimeArgs       []string          `json:"runtimeArgs"`
		StopOnEntry       bool              `json:"stopOnEntry"`
		NoDebug           bool              `json:"noDebug"`
		SkipFiles         []string          `json:"skipFiles"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	if c.Program == "" {
		return errors.New("launch needs a program: the file to run")
	}
	if c.Cwd == "" {
		c.Cwd = filepath.Dir(c.Program)
	}
	if !filepath.IsAbs(c.Program) {
		c.Program = filepath.Join(c.Cwd, c.Program)
	}
	node := c.RuntimeExecutable
	if node == "" {
		node = "node"
	}
	args := append([]string{"--inspect-brk=127.0.0.1:0"}, c.RuntimeArgs...)
	if c.NoDebug {
		args = append([]string{}, c.RuntimeArgs...)
	}
	args = append(append(args, c.Program), c.Args...)
	cmd := exec.Command(node, args...)
	cmd.Dir = c.Cwd
	cmd.Env = os.Environ()
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start %s: %w", node, err)
	}
	a.cmd = cmd
	a.stopOnEntry, a.noDebug = c.StopOnEntry, c.NoDebug
	a.event("process", map[string]any{"name": c.Program, "startMethod": "launch", "systemProcessId": cmd.Process.Pid})

	found := make(chan string, 1)
	var pipes sync.WaitGroup
	pipes.Add(2)
	go func() { defer pipes.Done(); a.pipeOutput(stdout, "stdout", nil) }()
	go func() { defer pipes.Done(); a.pipeOutput(stderr, "stderr", found) }()
	go func() {
		pipes.Wait()
		code := 0
		if err := cmd.Wait(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
		}
		a.finished(code)
	}()
	if c.NoDebug {
		return nil
	}
	var addr string
	select {
	case addr = <-found:
	case <-time.After(inspectWait):
		a.kill(syscall.SIGKILL)
		return errors.New("node did not open its inspector")
	}
	ws, _, err := websocket.DefaultDialer.Dial(addr, nil)
	if err != nil {
		a.kill(syscall.SIGKILL)
		return fmt.Errorf("could not reach the inspector: %w", err)
	}
	a.ws = ws
	go a.readCDP()
	if _, err := a.cdp("Runtime.enable", nil); err != nil {
		return err
	}
	if _, err := a.cdp("Debugger.enable", nil); err != nil {
		return err
	}
	// steps stay out of Node's own code, and what launch.json lists
	patterns := []string{"^node:"}
	for _, s := range c.SkipFiles {
		if s != "<node_internals>/**" {
			patterns = append(patterns, globRegex(s))
		}
	}
	_, _ = a.cdp("Debugger.setBlackboxPatterns", map[string]any{"patterns": patterns})
	return nil
}

func globRegex(glob string) string {
	s := regexp.QuoteMeta(glob)
	s = strings.ReplaceAll(s, `\*\*`, ".*")
	s = strings.ReplaceAll(s, `\*`, "[^/]*")
	return s
}

// pipeOutput sends what the program prints to the client. Node's own lines about its inspector
// are no output of the program; they are how the adapter finds it.
func (a *adapter) pipeOutput(r io.Reader, category string, found chan<- string) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			skip := false
			if found != nil {
				if m := inspectorURL.FindStringSubmatch(line); m != nil {
					select {
					case found <- m[1]:
					default:
					}
					skip = true
				}
				if strings.HasPrefix(line, "For help, see: ") || strings.HasPrefix(line, "Debugger attached.") ||
					strings.HasPrefix(line, "Waiting for the debugger to disconnect...") || strings.HasPrefix(line, "Debugger ending on ") {
					skip = true
				}
			}
			if !skip {
				a.event("output", map[string]any{"category": category, "output": line})
			}
		}
		if err != nil {
			return
		}
	}
}

func (a *adapter) finished(code int) {
	a.exitOnce.Do(func() {
		a.mu.Lock()
		a.terminated = true
		a.mu.Unlock()
		a.event("exited", map[string]any{"exitCode": code})
		a.event("terminated", map[string]any{})
		a.closeCDP()
	})
}

func (a *adapter) closeCDP() {
	a.wsMu.Lock()
	ws := a.ws
	a.wsMu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
	a.cdpMu.Lock()
	for id, ch := range a.pending {
		close(ch)
		delete(a.pending, id)
	}
	a.cdpMu.Unlock()
}

func (a *adapter) kill(sig syscall.Signal) {
	if a.cmd != nil && a.cmd.Process != nil {
		_ = syscall.Kill(-a.cmd.Process.Pid, sig)
	}
}

func (a *adapter) shutdown() {
	a.kill(syscall.SIGKILL)
	a.closeCDP()
}

// cdp sends one inspector command and waits for its answer.
func (a *adapter) cdp(method string, params map[string]any) (json.RawMessage, error) {
	if a.ws == nil {
		return nil, errors.New("not running under the debugger")
	}
	a.cdpMu.Lock()
	a.cdpSeq++
	id := a.cdpSeq
	ch := make(chan cdpReply, 1)
	a.pending[id] = ch
	a.cdpMu.Unlock()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	a.wsMu.Lock()
	err := a.ws.WriteJSON(msg)
	a.wsMu.Unlock()
	if err != nil {
		a.cdpMu.Lock()
		delete(a.pending, id)
		a.cdpMu.Unlock()
		return nil, errors.New("the program is gone")
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return nil, errors.New("the program is gone")
		}
		if r.Error != nil {
			return nil, errors.New(r.Error.Message)
		}
		return r.Result, nil
	case <-time.After(cdpWait):
		a.cdpMu.Lock()
		delete(a.pending, id)
		a.cdpMu.Unlock()
		return nil, errors.New("the inspector did not answer")
	}
}

func (a *adapter) readCDP() {
	for {
		_, data, err := a.ws.ReadMessage()
		if err != nil {
			return
		}
		var m struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			cdpReply
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.ID != 0 {
			a.cdpMu.Lock()
			ch := a.pending[m.ID]
			delete(a.pending, m.ID)
			a.cdpMu.Unlock()
			if ch != nil {
				ch <- m.cdpReply
			}
			continue
		}
		switch m.Method {
		case "Debugger.scriptParsed":
			var p struct {
				ScriptID string `json:"scriptId"`
				URL      string `json:"url"`
			}
			if json.Unmarshal(m.Params, &p) == nil {
				a.mu.Lock()
				a.scripts[p.ScriptID] = p.URL
				a.mu.Unlock()
			}
		case "Debugger.paused":
			a.onPaused(m.Params)
		case "Debugger.resumed":
			a.resumed()
		case "Debugger.breakpointResolved":
			a.onResolved(m.Params)
		case "Runtime.executionContextDestroyed":
			// the program is done, and node waits for the debugger to let go
			go func() {
				time.Sleep(50 * time.Millisecond)
				a.wsMu.Lock()
				ws := a.ws
				a.wsMu.Unlock()
				if ws != nil {
					_ = ws.Close()
				}
			}()
		}
	}
}

func (a *adapter) onPaused(raw json.RawMessage) {
	var p struct {
		CallFrames     []callFrame `json:"callFrames"`
		Reason         string      `json:"reason"`
		HitBreakpoints []string    `json:"hitBreakpoints"`
		Data           struct {
			Description string `json:"description"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	a.mu.Lock()
	if a.waitEntry {
		// the stop node makes before the first line, waiting for the debugger
		a.waitEntry = false
		stop := a.stopOnEntry
		a.mu.Unlock()
		if !stop {
			go func() { _, _ = a.cdp("Debugger.resume", nil) }()
			return
		}
		p.Reason = "entry"
		a.mu.Lock()
	}
	a.frames = p.CallFrames
	a.paused = true
	a.refs = map[int]string{}
	a.nextRef = 0
	a.mu.Unlock()
	reason := "pause"
	switch {
	case p.Reason == "entry":
		reason = "entry"
	case len(p.HitBreakpoints) > 0:
		reason = "breakpoint"
	case p.Reason == "exception" || p.Reason == "promiseRejection":
		reason = "exception"
	case p.Reason == "other" || p.Reason == "ambiguous" || p.Reason == "step":
		reason = "step"
	}
	body := map[string]any{"reason": reason, "threadId": threadID, "allThreadsStopped": true}
	if reason == "exception" {
		body["text"] = firstLine(p.Data.Description)
		body["description"] = "Paused on exception"
	}
	a.event("stopped", body)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func (a *adapter) resumed() {
	a.mu.Lock()
	a.paused = false
	a.frames = nil
	a.mu.Unlock()
}

func (a *adapter) step(method string) error {
	_, err := a.cdp(method, nil)
	return err
}

func (a *adapter) configured() error {
	if a.noDebug || a.ws == nil {
		return nil
	}
	a.mu.Lock()
	a.waitEntry = true
	a.started = true
	a.mu.Unlock()
	_, err := a.cdp("Runtime.runIfWaitingForDebugger", nil)
	return err
}

func (a *adapter) onResolved(raw json.RawMessage) {
	var p struct {
		BreakpointID string `json:"breakpointId"`
		Location     struct {
			LineNumber int `json:"lineNumber"`
		} `json:"location"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	a.mu.Lock()
	id, ok := a.cdpToDap[p.BreakpointID]
	a.mu.Unlock()
	if ok {
		a.event("breakpoint", map[string]any{"reason": "changed", "breakpoint": map[string]any{"id": id, "verified": true, "line": p.Location.LineNumber + 1}})
	}
}

var logPart = regexp.MustCompile(`\{([^{}]*)\}`)

// logCondition is a condition that prints and never stops: how a logpoint works in V8.
func logCondition(msg string) string {
	text := strings.NewReplacer("\\", "\\\\", "`", "\\`", "${", "\\${").Replace(msg)
	text = logPart.ReplaceAllString(text, "$${$1}")
	return "console.log(`" + text + "`), false"
}

// urlRegexFor matches a script's URL: Node gives a file:// URL, encoded, or on older versions the path.
func urlRegexFor(path string) string {
	return "^(file://" + regexp.QuoteMeta((&url.URL{Path: path}).EscapedPath()) + "|" + regexp.QuoteMeta(path) + ")$"
}

func (a *adapter) setBreakpoints(raw json.RawMessage) (any, error) {
	var p struct {
		Source struct {
			Path string `json:"path"`
		} `json:"source"`
		Breakpoints []struct {
			Line       int    `json:"line"`
			Column     int    `json:"column"`
			Condition  string `json:"condition"`
			LogMessage string `json:"logMessage"`
		} `json:"breakpoints"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	a.mu.Lock()
	old := a.bpIDs[p.Source.Path]
	delete(a.bpIDs, p.Source.Path)
	a.mu.Unlock()
	for _, id := range old {
		_, _ = a.cdp("Debugger.removeBreakpoint", map[string]any{"breakpointId": id})
		a.mu.Lock()
		delete(a.cdpToDap, id)
		a.mu.Unlock()
	}
	out := make([]map[string]any, 0, len(p.Breakpoints))
	for _, b := range p.Breakpoints {
		a.mu.Lock()
		a.nextBP++
		dapID := a.nextBP
		a.mu.Unlock()
		cond := b.Condition
		if b.LogMessage != "" {
			cond = logCondition(b.LogMessage)
		}
		params := map[string]any{"lineNumber": b.Line - 1, "urlRegex": urlRegexFor(p.Source.Path)}
		if cond != "" {
			params["condition"] = cond
		}
		res, err := a.cdp("Debugger.setBreakpointByUrl", params)
		if err != nil {
			out = append(out, map[string]any{"id": dapID, "verified": false, "line": b.Line, "message": err.Error()})
			continue
		}
		var r struct {
			BreakpointID string `json:"breakpointId"`
			Locations    []struct {
				LineNumber int `json:"lineNumber"`
			} `json:"locations"`
		}
		_ = json.Unmarshal(res, &r)
		a.mu.Lock()
		a.cdpToDap[r.BreakpointID] = dapID
		a.bpIDs[p.Source.Path] = append(a.bpIDs[p.Source.Path], r.BreakpointID)
		a.mu.Unlock()
		line := b.Line
		if len(r.Locations) > 0 {
			line = r.Locations[0].LineNumber + 1
		}
		// before the file is loaded V8 can't tell yet: a breakpoint event says so when it does
		out = append(out, map[string]any{"id": dapID, "verified": len(r.Locations) > 0, "line": line})
	}
	return map[string]any{"breakpoints": out}, nil
}

func (a *adapter) setExceptions(raw json.RawMessage) error {
	var p struct {
		Filters []string `json:"filters"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	state := "none"
	for _, f := range p.Filters {
		if f == "all" {
			state = "all"
		} else if f == "uncaught" && state == "none" {
			state = "uncaught"
		}
	}
	_, err := a.cdp("Debugger.setPauseOnExceptions", map[string]any{"state": state})
	return err
}

func pathOf(raw string) string {
	if p, ok := strings.CutPrefix(raw, "file://"); ok {
		if u, err := url.PathUnescape(p); err == nil {
			return u
		}
		return p
	}
	if strings.HasPrefix(raw, "/") {
		return raw
	}
	return ""
}

func (a *adapter) stackTrace() any {
	a.mu.Lock()
	defer a.mu.Unlock()
	frames := make([]map[string]any, 0, len(a.frames))
	for i, f := range a.frames {
		name := f.FunctionName
		if name == "" {
			name = "(anonymous)"
		}
		fr := map[string]any{"id": i + 1, "name": name, "line": f.Location.LineNumber + 1, "column": f.Location.ColumnNumber + 1}
		// a frame's own url is deprecated and left empty for modules
		where := f.URL
		if s := a.scripts[f.Location.ScriptID]; s != "" {
			where = s
		}
		if p := pathOf(where); p != "" {
			fr["source"] = map[string]any{"name": filepath.Base(p), "path": p}
		} else {
			fr["source"] = map[string]any{"name": where, "presentationHint": "deemphasize"}
			fr["presentationHint"] = "subtle"
		}
		frames = append(frames, fr)
	}
	return map[string]any{"stackFrames": frames, "totalFrames": len(frames)}
}

func (a *adapter) refFor(objectID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextRef++
	a.refs[a.nextRef] = objectID
	return a.nextRef
}

var scopeNames = map[string]string{"local": "Local", "closure": "Closure", "module": "Module", "global": "Global", "block": "Block", "catch": "Catch", "with": "With", "script": "Script", "eval": "Eval"}

func (a *adapter) scopes(raw json.RawMessage) (any, error) {
	var p struct {
		FrameID int `json:"frameId"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if p.FrameID < 1 || p.FrameID > len(a.frames) {
		a.mu.Unlock()
		return map[string]any{"scopes": []any{}}, nil
	}
	chain := a.frames[p.FrameID-1].ScopeChain
	a.mu.Unlock()
	out := make([]map[string]any, 0, len(chain))
	for _, s := range chain {
		name := scopeNames[s.Type]
		if name == "" {
			name = s.Type
		}
		if s.Name != "" && s.Type == "closure" {
			name = "Closure (" + s.Name + ")"
		}
		out = append(out, map[string]any{"name": name, "variablesReference": a.refFor(s.Object.ObjectID), "expensive": s.Type == "global"})
	}
	return map[string]any{"scopes": out}, nil
}

type remoteObject struct {
	Type                string          `json:"type"`
	Subtype             string          `json:"subtype"`
	ClassName           string          `json:"className"`
	Value               json.RawMessage `json:"value"`
	UnserializableValue string          `json:"unserializableValue"`
	Description         string          `json:"description"`
	ObjectID            string          `json:"objectId"`
}

// show is how the client sees a value. Text keeps its quotes, so that "1" and 1 differ.
func (a *adapter) show(o remoteObject) (string, string, int) {
	ref := 0
	if o.ObjectID != "" {
		ref = a.refFor(o.ObjectID)
	}
	text := o.Description
	switch {
	case o.Type == "undefined":
		text = "undefined"
	case o.Subtype == "null":
		text = "null"
	case o.UnserializableValue != "":
		text = o.UnserializableValue
	case len(o.Value) > 0 && o.Type != "object" && o.Type != "function":
		text = string(o.Value)
	}
	if len(text) > maxValueText {
		text = text[:maxValueText] + "…"
	}
	typ := o.Type
	if o.Subtype != "" {
		typ = o.Subtype
	} else if o.ClassName != "" {
		typ = o.ClassName
	}
	return text, typ, ref
}

func (a *adapter) variables(raw json.RawMessage) (any, error) {
	var p struct {
		VariablesReference int `json:"variablesReference"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	a.mu.Lock()
	id, ok := a.refs[p.VariablesReference]
	a.mu.Unlock()
	if !ok {
		return map[string]any{"variables": []any{}}, nil
	}
	res, err := a.cdp("Runtime.getProperties", map[string]any{"objectId": id, "ownProperties": true, "generatePreview": false})
	if err != nil {
		return nil, err
	}
	var r struct {
		Result []struct {
			Name  string        `json:"name"`
			Value *remoteObject `json:"value"`
			Get   *remoteObject `json:"get"`
		} `json:"result"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(r.Result))
	for _, v := range r.Result {
		if v.Value == nil {
			if v.Get != nil && v.Get.Type == "function" {
				out = append(out, map[string]any{"name": v.Name, "value": "(getter)", "variablesReference": 0})
			}
			continue
		}
		text, typ, ref := a.show(*v.Value)
		out = append(out, map[string]any{"name": v.Name, "value": text, "type": typ, "variablesReference": ref})
	}
	return map[string]any{"variables": out}, nil
}

func (a *adapter) evaluate(raw json.RawMessage) (any, error) {
	var p struct {
		Expression string `json:"expression"`
		FrameID    int    `json:"frameId"`
		Context    string `json:"context"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	a.mu.Lock()
	var frameID string
	if p.FrameID >= 1 && p.FrameID <= len(a.frames) {
		frameID = a.frames[p.FrameID-1].CallFrameID
	}
	a.mu.Unlock()
	params := map[string]any{"expression": p.Expression, "generatePreview": false, "objectGroup": "repl"}
	// looking at a value with the mouse must not change the program
	if p.Context == "hover" {
		params["throwOnSideEffect"] = true
	}
	method := "Runtime.evaluate"
	if frameID != "" {
		method = "Debugger.evaluateOnCallFrame"
		params["callFrameId"] = frameID
	}
	res, err := a.cdp(method, params)
	if err != nil {
		return nil, err
	}
	var r struct {
		Result           remoteObject `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	if d := r.ExceptionDetails; d != nil {
		if d.Exception != nil && d.Exception.Description != "" {
			return nil, errors.New(firstLine(d.Exception.Description))
		}
		return nil, errors.New(d.Text)
	}
	text, typ, ref := a.show(r.Result)
	return map[string]any{"result": text, "type": typ, "variablesReference": ref}, nil
}
