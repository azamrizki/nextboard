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
	a := &adapter{out: out, targets: map[int]*target{}, refs: map[int]varRef{}, srcBPs: map[string][]srcBP{}, exceptions: "none"}
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
	exitOnce sync.Once

	mu          sync.Mutex
	main        *target         // the program started
	targets     map[int]*target // by thread id: the program and the node processes it started
	nextTarget  int
	refs        map[int]varRef
	nextRef     int
	srcBPs      map[string][]srcBP   // the client's breakpoints, by source path
	outRoots    []string             // where compiled scripts are looked for (outFiles, or cwd)
	outIndex    map[string][]outFile // source → the compiled scripts that came from it
	indexed     bool
	nextBP      int
	stopOnEntry bool
	started     bool
	noDebug     bool
	terminated  bool
	blackbox    []string // patterns for steps to skip
	exceptions  string   // "none", "uncaught" or "all"
	childDir    string   // where child processes say where their inspector is (children.go)
}

// varRef is what a variablesReference stands for: an object of one target.
type varRef struct {
	t  *target
	id string
}

// target is one node process under the debugger: one thread of the session. Its state is
// guarded by the adapter's mu.
type target struct {
	a    *adapter
	id   int
	name string

	ws      *websocket.Conn
	wsMu    sync.Mutex
	cdpSeq  int
	cdpMu   sync.Mutex
	pending map[int]chan cdpReply

	frames    []callFrame
	paused    bool
	waitEntry bool
	cdpToDap  map[string]int        // CDP breakpoint → DAP breakpoint
	bpIDs     map[string][]string   // a source path's CDP breakpoints set by URL
	mappedBPs map[string][]string   // a source's CDP breakpoints set through maps
	bpLine    map[string]int        // CDP breakpoint → the source line to report when it resolves
	scripts   map[string]string     // script id → its URL
	maps      map[string]*sourceMap // script id → its source map
}

func (a *adapter) newTarget(name string, ws *websocket.Conn) *target {
	t := &target{a: a, name: name, ws: ws, pending: map[int]chan cdpReply{}, cdpToDap: map[string]int{}, bpIDs: map[string][]string{},
		mappedBPs: map[string][]string{}, bpLine: map[string]int{}, scripts: map[string]string{}, maps: map[string]*sourceMap{}}
	a.mu.Lock()
	a.nextTarget++
	t.id = a.nextTarget
	a.targets[t.id] = t
	if a.main == nil {
		a.main = t
	}
	a.mu.Unlock()
	return t
}

// target is the thread a request names; the program when it names none.
func (a *adapter) target(threadID int) *target {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t := a.targets[threadID]; t != nil {
		return t
	}
	return a.main
}

func (a *adapter) allTargets() []*target {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*target, 0, len(a.targets))
	for id := 1; id <= a.nextTarget; id++ {
		if t := a.targets[id]; t != nil {
			out = append(out, t)
		}
	}
	return out
}

// frameBase splits a frame id into its thread and its place in that thread's stack.
const frameBase = 1_000_000

func (a *adapter) frameOf(frameID int) (*target, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.targets[frameID/frameBase]
	i := frameID%frameBase - 1
	if t == nil || i < 0 || i >= len(t.frames) {
		return nil, -1
	}
	return t, i
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

// threadOf is the threadId of a request's arguments; 0 when there is none.
func threadOf(raw json.RawMessage) int {
	var p struct {
		ThreadID int `json:"threadId"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.ThreadID
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
		threads := []map[string]any{}
		for _, t := range a.allTargets() {
			threads = append(threads, map[string]any{"id": t.id, "name": t.name})
		}
		if len(threads) == 0 {
			threads = append(threads, map[string]any{"id": threadID, "name": "main"})
		}
		body = map[string]any{"threads": threads}
	case "stackTrace":
		body = a.stackTrace(threadOf(m.Arguments))
	case "scopes":
		body, err = a.scopes(m.Arguments)
	case "variables":
		body, err = a.variables(m.Arguments)
	case "evaluate":
		body, err = a.evaluate(m.Arguments)
	case "continue", "next", "stepIn", "stepOut", "pause":
		t := a.target(threadOf(m.Arguments))
		if t == nil {
			err = errors.New("not running under the debugger")
			break
		}
		method := map[string]string{"continue": "Debugger.resume", "next": "Debugger.stepOver", "stepIn": "Debugger.stepInto", "stepOut": "Debugger.stepOut", "pause": "Debugger.pause"}[m.Command]
		if m.Command != "pause" {
			t.resumed()
		}
		_, err = t.cdp(method, nil)
		if m.Command == "continue" {
			body = map[string]any{"allThreadsContinued": false}
		}
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
		OutFiles          []string          `json:"outFiles"`
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
	if !c.NoDebug {
		// the node processes the program starts report in, and wait to be attached
		dir, err := childSetup()
		if err != nil {
			return err
		}
		a.childDir = dir
		opts, ok := c.Env["NODE_OPTIONS"]
		if !ok {
			opts = os.Getenv("NODE_OPTIONS")
		}
		cmd.Env = append(cmd.Env, "NODE_OPTIONS="+strings.TrimSpace(opts+" --require "+strconv.Quote(filepath.Join(dir, bootFile))), childDirEnv+"="+dir)
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
	a.outRoots = outRoots(c.Cwd, c.OutFiles)
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
	// steps stay out of Node's own code, the bootloader, and what launch.json lists
	patterns := []string{"^node:", regexp.QuoteMeta(bootFile) + "$"}
	for _, s := range c.SkipFiles {
		if s != "<node_internals>/**" {
			patterns = append(patterns, globRegex(s))
		}
	}
	a.mu.Lock()
	a.blackbox = patterns
	a.mu.Unlock()
	t := a.newTarget("main", ws)
	go t.readCDP()
	if err := t.setup(); err != nil {
		return err
	}
	go a.watchChildren()
	return nil
}

// setup turns on what a target needs before it runs: its runtime and debugger, the steps to
// skip, the stop before a mapped script runs (sourcemap.go), and the exceptions to stop at.
func (t *target) setup() error {
	if _, err := t.cdp("Runtime.enable", nil); err != nil {
		return err
	}
	if _, err := t.cdp("Debugger.enable", nil); err != nil {
		return err
	}
	t.a.mu.Lock()
	patterns, exceptions := t.a.blackbox, t.a.exceptions
	t.a.mu.Unlock()
	_, _ = t.cdp("Debugger.setBlackboxPatterns", map[string]any{"patterns": patterns})
	_, _ = t.cdp("Debugger.setInstrumentationBreakpoint", map[string]any{"instrumentation": "beforeScriptWithSourceMapExecution"})
	if exceptions != "none" {
		_, _ = t.cdp("Debugger.setPauseOnExceptions", map[string]any{"state": exceptions})
	}
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
	for _, t := range a.allTargets() {
		t.close()
	}
}

func (t *target) close() {
	t.wsMu.Lock()
	ws := t.ws
	t.wsMu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
	t.cdpMu.Lock()
	for id, ch := range t.pending {
		close(ch)
		delete(t.pending, id)
	}
	t.cdpMu.Unlock()
}

func (a *adapter) kill(sig syscall.Signal) {
	if a.cmd != nil && a.cmd.Process != nil {
		_ = syscall.Kill(-a.cmd.Process.Pid, sig)
	}
}

func (a *adapter) shutdown() {
	a.kill(syscall.SIGKILL)
	a.closeCDP()
	a.mu.Lock()
	dir := a.childDir
	a.childDir = ""
	a.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// cdp sends one inspector command to a target and waits for its answer.
func (t *target) cdp(method string, params map[string]any) (json.RawMessage, error) {
	if t == nil || t.ws == nil {
		return nil, errors.New("not running under the debugger")
	}
	t.cdpMu.Lock()
	t.cdpSeq++
	id := t.cdpSeq
	ch := make(chan cdpReply, 1)
	t.pending[id] = ch
	t.cdpMu.Unlock()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	t.wsMu.Lock()
	err := t.ws.WriteJSON(msg)
	t.wsMu.Unlock()
	if err != nil {
		t.cdpMu.Lock()
		delete(t.pending, id)
		t.cdpMu.Unlock()
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
		t.cdpMu.Lock()
		delete(t.pending, id)
		t.cdpMu.Unlock()
		return nil, errors.New("the inspector did not answer")
	}
}

// main is the thread of the program the session started.
func (a *adapter) mainTarget() *target {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.main
}

func (t *target) readCDP() {
	defer t.gone()
	for {
		_, data, err := t.ws.ReadMessage()
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
			t.cdpMu.Lock()
			ch := t.pending[m.ID]
			delete(t.pending, m.ID)
			t.cdpMu.Unlock()
			if ch != nil {
				ch <- m.cdpReply
			}
			continue
		}
		switch m.Method {
		case "Debugger.scriptParsed":
			var p struct {
				ScriptID     string `json:"scriptId"`
				URL          string `json:"url"`
				SourceMapURL string `json:"sourceMapURL"`
			}
			if json.Unmarshal(m.Params, &p) == nil {
				t.a.mu.Lock()
				t.scripts[p.ScriptID] = p.URL
				t.a.mu.Unlock()
				t.loadMap(p.ScriptID, p.URL, p.SourceMapURL)
			}
		case "Debugger.paused":
			t.onPaused(m.Params)
		case "Debugger.resumed":
			t.resumed()
		case "Debugger.breakpointResolved":
			t.onResolved(m.Params)
		case "Runtime.executionContextDestroyed":
			// the program is done, and node waits for the debugger to let go
			go func() {
				time.Sleep(50 * time.Millisecond)
				t.wsMu.Lock()
				ws := t.ws
				t.wsMu.Unlock()
				if ws != nil {
					_ = ws.Close()
				}
			}()
		}
	}
}

// gone ends a child's thread when its connection does; the program's ends with the session.
func (t *target) gone() {
	a := t.a
	a.mu.Lock()
	child := a.targets[t.id] == t && t != a.main
	if child {
		delete(a.targets, t.id)
		for ref, v := range a.refs {
			if v.t == t {
				delete(a.refs, ref)
			}
		}
	}
	a.mu.Unlock()
	t.cdpMu.Lock()
	for id, ch := range t.pending {
		close(ch)
		delete(t.pending, id)
	}
	t.cdpMu.Unlock()
	if child {
		a.event("thread", map[string]any{"reason": "exited", "threadId": t.id})
	}
}

func (t *target) onPaused(raw json.RawMessage) {
	var p struct {
		CallFrames     []callFrame `json:"callFrames"`
		Reason         string      `json:"reason"`
		HitBreakpoints []string    `json:"hitBreakpoints"`
		Data           struct {
			Description  string `json:"description"`
			ScriptID     string `json:"scriptId"`
			URL          string `json:"url"`
			SourceMapURL string `json:"sourceMapURL"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	if p.Reason == "instrumentation" {
		// not a stop the person sees: the breakpoints of its sources go in, and it runs on
		go func() {
			t.loadMap(p.Data.ScriptID, p.Data.URL, p.Data.SourceMapURL)
			t.applyMapped(p.Data.ScriptID)
			_, _ = t.cdp("Debugger.resume", nil)
		}()
		return
	}
	a := t.a
	a.mu.Lock()
	if t.waitEntry {
		// the stop node makes before the first line, waiting for the debugger. The main
		// script is parsed by now, and its map loaded: its sources' breakpoints go in first.
		t.waitEntry = false
		stop := a.stopOnEntry
		a.mu.Unlock()
		if !stop {
			go func() {
				t.applyAllMapped()
				_, _ = t.cdp("Debugger.resume", nil)
			}()
			return
		}
		go func() {
			t.applyAllMapped()
			t.stoppedAt(p.CallFrames, "entry", "")
		}()
		return
	}
	a.mu.Unlock()
	reason := "pause"
	switch {
	case len(p.HitBreakpoints) > 0:
		reason = "breakpoint"
	case p.Reason == "exception" || p.Reason == "promiseRejection":
		reason = "exception"
	case p.Reason == "other" || p.Reason == "ambiguous" || p.Reason == "step":
		reason = "step"
	}
	t.stoppedAt(p.CallFrames, reason, p.Data.Description)
}

// stoppedAt tells the client where a thread stopped. The others run on.
func (t *target) stoppedAt(frames []callFrame, reason, description string) {
	a := t.a
	a.mu.Lock()
	t.frames = frames
	t.paused = true
	for ref, v := range a.refs {
		if v.t == t {
			delete(a.refs, ref)
		}
	}
	a.mu.Unlock()
	body := map[string]any{"reason": reason, "threadId": t.id, "allThreadsStopped": false}
	if reason == "exception" {
		body["text"] = firstLine(description)
		body["description"] = "Paused on exception"
	}
	a.event("stopped", body)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func (t *target) resumed() {
	t.a.mu.Lock()
	t.paused = false
	t.frames = nil
	t.a.mu.Unlock()
}

func (a *adapter) configured() error {
	t := a.mainTarget()
	if a.noDebug || t == nil {
		return nil
	}
	a.mu.Lock()
	t.waitEntry = true
	a.started = true
	a.mu.Unlock()
	_, err := t.cdp("Runtime.runIfWaitingForDebugger", nil)
	return err
}

func (t *target) onResolved(raw json.RawMessage) {
	var p struct {
		BreakpointID string `json:"breakpointId"`
		Location     struct {
			LineNumber int `json:"lineNumber"`
		} `json:"location"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	t.a.mu.Lock()
	id, ok := t.cdpToDap[p.BreakpointID]
	line, mapped := t.bpLine[p.BreakpointID]
	t.a.mu.Unlock()
	if !mapped {
		line = p.Location.LineNumber + 1
	}
	if ok {
		t.a.event("breakpoint", map[string]any{"reason": "changed", "breakpoint": map[string]any{"id": id, "verified": true, "line": line}})
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

// setBreakpoints takes the client's breakpoints of one source, and sets them in every thread.
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
	wanted := make([]srcBP, 0, len(p.Breakpoints))
	a.mu.Lock()
	for _, b := range p.Breakpoints {
		a.nextBP++
		cond := b.Condition
		if b.LogMessage != "" {
			cond = logCondition(b.LogMessage)
		}
		wanted = append(wanted, srcBP{dapID: a.nextBP, line: b.Line, cond: cond})
	}
	a.srcBPs[p.Source.Path] = wanted
	a.mu.Unlock()
	got := map[int]setBP{}
	for i, t := range a.allTargets() {
		res := t.applyPath(p.Source.Path, wanted)
		if i == 0 {
			got = res
		}
	}
	out := make([]map[string]any, 0, len(wanted))
	for _, b := range wanted {
		r, ok := got[b.dapID]
		bp := map[string]any{"id": b.dapID, "verified": ok && r.verified, "line": b.line}
		if ok && r.line > 0 {
			bp["line"] = r.line
		}
		if ok && r.message != "" {
			bp["message"] = r.message
		}
		out = append(out, bp)
	}
	return map[string]any{"breakpoints": out}, nil
}

// applyPath sets a source's breakpoints in one thread, in place of the ones it had: by the
// path's URL (a .js run as it is), through the maps of scripts already loaded (a .ts compiled
// to them), or by the URL of compiled scripts not loaded yet. It says where each landed.
func (t *target) applyPath(path string, wanted []srcBP) map[int]setBP {
	a := t.a
	a.mu.Lock()
	old := append(append([]string{}, t.bpIDs[path]...), t.mappedBPs[path]...)
	delete(t.bpIDs, path)
	delete(t.mappedBPs, path)
	a.mu.Unlock()
	for _, id := range old {
		_, _ = t.cdp("Debugger.removeBreakpoint", map[string]any{"breakpointId": id})
		a.mu.Lock()
		delete(t.cdpToDap, id)
		delete(t.bpLine, id)
		a.mu.Unlock()
	}
	got := map[int]setBP{}
	for _, b := range wanted {
		params := map[string]any{"lineNumber": b.line - 1, "urlRegex": urlRegexFor(path)}
		if b.cond != "" {
			params["condition"] = b.cond
		}
		res, err := t.cdp("Debugger.setBreakpointByUrl", params)
		if err != nil {
			got[b.dapID] = setBP{dapID: b.dapID, message: err.Error()}
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
		t.cdpToDap[r.BreakpointID] = b.dapID
		t.bpIDs[path] = append(t.bpIDs[path], r.BreakpointID)
		a.mu.Unlock()
		// before the file is loaded V8 can't tell yet: a breakpoint event says so when it does
		if len(r.Locations) > 0 {
			got[b.dapID] = setBP{dapID: b.dapID, line: r.Locations[0].LineNumber + 1, verified: true}
		}
	}
	a.mu.Lock()
	var scripts []string
	for id, sm := range t.maps {
		if sm.has(path) {
			scripts = append(scripts, id)
		}
	}
	a.mu.Unlock()
	for _, id := range scripts {
		for _, s := range t.setMapped(id, path, wanted) {
			s.verified = true
			got[s.dapID] = s
		}
	}
	if len(scripts) == 0 {
		for _, s := range t.setByOutFile(path, wanted) {
			if _, ok := got[s.dapID]; !ok {
				got[s.dapID] = s
			}
		}
	}
	return got
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
	a.mu.Lock()
	a.exceptions = state
	a.mu.Unlock()
	var first error
	for _, t := range a.allTargets() {
		if _, err := t.cdp("Debugger.setPauseOnExceptions", map[string]any{"state": state}); err != nil && first == nil {
			first = err
		}
	}
	return first
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

func (a *adapter) stackTrace(threadID int) any {
	t := a.target(threadID)
	a.mu.Lock()
	defer a.mu.Unlock()
	frames := []map[string]any{}
	if t == nil {
		return map[string]any{"stackFrames": frames, "totalFrames": 0}
	}
	for i, f := range t.frames {
		name := f.FunctionName
		if name == "" {
			name = "(anonymous)"
		}
		fr := map[string]any{"id": t.id*frameBase + i + 1, "name": name, "line": f.Location.LineNumber + 1, "column": f.Location.ColumnNumber + 1}
		// a frame's own url is deprecated and left empty for modules
		where := f.URL
		if s := t.scripts[f.Location.ScriptID]; s != "" {
			where = s
		}
		if sm := t.maps[f.Location.ScriptID]; sm != nil {
			if src, line, col, ok := sm.original(f.Location.LineNumber, f.Location.ColumnNumber); ok && filepath.IsAbs(src) {
				where = src
				fr["line"], fr["column"] = line+1, col+1
			}
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

func (a *adapter) refFor(t *target, objectID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextRef++
	a.refs[a.nextRef] = varRef{t: t, id: objectID}
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
	t, i := a.frameOf(p.FrameID)
	if t == nil {
		return map[string]any{"scopes": []any{}}, nil
	}
	a.mu.Lock()
	chain := t.frames[i].ScopeChain
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
		out = append(out, map[string]any{"name": name, "variablesReference": a.refFor(t, s.Object.ObjectID), "expensive": s.Type == "global"})
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
func (a *adapter) show(t *target, o remoteObject) (string, string, int) {
	ref := 0
	if o.ObjectID != "" {
		ref = a.refFor(t, o.ObjectID)
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
	v, ok := a.refs[p.VariablesReference]
	a.mu.Unlock()
	if !ok {
		return map[string]any{"variables": []any{}}, nil
	}
	res, err := v.t.cdp("Runtime.getProperties", map[string]any{"objectId": v.id, "ownProperties": true, "generatePreview": false})
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
	for _, x := range r.Result {
		if x.Value == nil {
			if x.Get != nil && x.Get.Type == "function" {
				out = append(out, map[string]any{"name": x.Name, "value": "(getter)", "variablesReference": 0})
			}
			continue
		}
		text, typ, ref := a.show(v.t, *x.Value)
		out = append(out, map[string]any{"name": x.Name, "value": text, "type": typ, "variablesReference": ref})
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
	t, i := a.frameOf(p.FrameID)
	var frameID string
	if t != nil {
		a.mu.Lock()
		frameID = t.frames[i].CallFrameID
		a.mu.Unlock()
	} else {
		t = a.mainTarget()
	}
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
	res, err := t.cdp(method, params)
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
	text, typ, ref := a.show(t, r.Result)
	return map[string]any{"result": text, "type": typ, "variablesReference": ref}, nil
}
