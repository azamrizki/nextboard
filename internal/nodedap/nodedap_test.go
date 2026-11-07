package nodedap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// client plays the editor against a real node.
type client struct {
	t      *testing.T
	in     io.WriteCloser
	mu     sync.Mutex
	seq    int
	resp   map[int]chan map[string]any
	events chan map[string]any
	output strings.Builder
	done   chan struct{}
}

func start(t *testing.T) *client {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, resp: map[int]chan map[string]any{}, events: make(chan map[string]any, 200), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		_ = Serve(inR, outW)
		outW.Close()
	}()
	go func() {
		r := bufio.NewReader(outR)
		for {
			body, err := readFrame(r)
			if err != nil {
				return
			}
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			if m["type"] == "response" {
				c.mu.Lock()
				ch := c.resp[int(m["request_seq"].(float64))]
				c.mu.Unlock()
				if ch != nil {
					ch <- m
				}
			} else if m["event"] == "output" {
				c.mu.Lock()
				c.output.WriteString(m["body"].(map[string]any)["output"].(string))
				c.mu.Unlock()
			} else {
				c.events <- m
			}
		}
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
		}
	})
	return c
}

func (c *client) req(cmd string, args any) map[string]any {
	c.t.Helper()
	r := c.try(cmd, args)
	if r["success"] != true {
		c.t.Fatalf("%s failed: %v", cmd, r["message"])
	}
	b, _ := r["body"].(map[string]any)
	return b
}

func (c *client) try(cmd string, args any) map[string]any {
	c.t.Helper()
	c.mu.Lock()
	c.seq++
	seq := c.seq
	ch := make(chan map[string]any, 1)
	c.resp[seq] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"seq": seq, "type": "request", "command": cmd, "arguments": args})
	fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(b), b)
	select {
	case r := <-ch:
		return r
	case <-time.After(20 * time.Second):
		c.t.Fatalf("%s: no answer", cmd)
		return nil
	}
}

func (c *client) wait(name string) map[string]any {
	c.t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case e := <-c.events:
			if e["event"] == name {
				b, _ := e["body"].(map[string]any)
				return b
			}
		case <-timeout:
			c.t.Fatalf("no %s event; output: %s", name, c.outputText())
			return nil
		}
	}
}

func (c *client) outputText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.output.String()
}

func (c *client) launch(program string, extra map[string]any, breaks map[string][]map[string]any) map[string]any {
	c.t.Helper()
	caps := c.req("initialize", map[string]any{"adapterID": "node"})
	args := map[string]any{"program": program, "cwd": filepath.Dir(program)}
	for k, v := range extra {
		args[k] = v
	}
	c.req("launch", args)
	c.wait("initialized")
	for path, list := range breaks {
		c.req("setBreakpoints", map[string]any{"source": map[string]any{"path": path}, "breakpoints": list})
	}
	c.req("setExceptionBreakpoints", map[string]any{"filters": []string{"uncaught"}})
	c.req("configurationDone", nil)
	return caps
}

func (c *client) topFrame() map[string]any {
	c.t.Helper()
	return c.req("stackTrace", map[string]any{"threadId": 1})["stackFrames"].([]any)[0].(map[string]any)
}

func (c *client) locals(frame map[string]any) map[string]map[string]any {
	c.t.Helper()
	scopes := c.req("scopes", map[string]any{"frameId": frame["id"]})["scopes"].([]any)
	first := scopes[0].(map[string]any)
	vars := c.req("variables", map[string]any{"variablesReference": first["variablesReference"]})["variables"].([]any)
	out := map[string]map[string]any{}
	for _, v := range vars {
		m := v.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

const sample = `const util = require('node:util');
function add(a, b) {
  const label = 'sum';
  const items = [a, b];
  const total = a + b;
  return total;
}
console.log('before');
const result = add(2, 3);
console.error('after ' + result);
for (let i = 0; i < 3; i++) {
  add(i, 10);
}
process.exit(result === 5 ? 7 : 1);
`

func TestBreakpointsSteppingAndValues(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app with space.js")
	if err := os.WriteFile(file, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	c := start(t)
	caps := c.launch(file, nil, map[string][]map[string]any{file: {{"line": 5}}})
	if caps["supportsConditionalBreakpoints"] != true {
		t.Fatalf("caps %v", caps)
	}
	st := c.wait("stopped")
	if st["reason"] != "breakpoint" {
		t.Fatalf("stopped %v", st)
	}
	top := c.topFrame()
	if top["name"] != "add" || top["line"].(float64) != 5 || top["source"].(map[string]any)["path"] != file {
		t.Fatalf("frame %v", top)
	}
	locals := c.locals(top)
	if locals["a"]["value"] != "2" || locals["b"]["value"] != "3" || locals["label"]["value"] != `"sum"` {
		t.Fatalf("locals %v", locals)
	}
	items := locals["items"]
	if items["variablesReference"].(float64) == 0 || !strings.HasPrefix(items["value"].(string), "Array(2)") {
		t.Fatalf("items %v", items)
	}
	kids := c.req("variables", map[string]any{"variablesReference": items["variablesReference"]})["variables"].([]any)
	if kids[0].(map[string]any)["value"] != "2" {
		t.Fatalf("kids %v", kids)
	}
	ev := c.req("evaluate", map[string]any{"expression": "a * b + 1", "frameId": top["id"], "context": "repl"})
	if ev["result"] != "7" {
		t.Fatalf("evaluate %v", ev)
	}
	if r := c.try("evaluate", map[string]any{"expression": "nope.nope", "frameId": top["id"], "context": "repl"}); r["success"] != false || !strings.Contains(fmt.Sprint(r["message"]), "nope") {
		t.Fatalf("a bad expression %v", r)
	}
	// looking with the mouse changes nothing
	if r := c.try("evaluate", map[string]any{"expression": "globalThis.touched = 1", "frameId": top["id"], "context": "hover"}); r["success"] != false {
		t.Fatalf("hover with a side effect: %v", r)
	}

	c.req("next", map[string]any{"threadId": 1})
	if st := c.wait("stopped"); st["reason"] != "step" {
		t.Fatalf("after next %v", st)
	}
	if top = c.topFrame(); top["line"].(float64) != 6 {
		t.Fatalf("next went to %v", top["line"])
	}
	c.req("stepOut", map[string]any{"threadId": 1})
	c.wait("stopped")
	if top = c.topFrame(); top["line"].(float64) != 10 && top["line"].(float64) != 9 {
		t.Fatalf("stepOut went to %v", top["line"])
	}
	// the breakpoint comes off: the program runs to its end
	c.req("setBreakpoints", map[string]any{"source": map[string]any{"path": file}, "breakpoints": []any{}})
	c.req("continue", map[string]any{"threadId": 1})
	ex := c.wait("exited")
	if ex["exitCode"].(float64) != 7 {
		t.Fatalf("exit %v", ex)
	}
	c.wait("terminated")
	out := c.outputText()
	if !strings.Contains(out, "before") || !strings.Contains(out, "after 5") || strings.Contains(out, "Debugger listening") || strings.Contains(out, "Debugger attached") {
		t.Fatalf("output %q", out)
	}
}

func TestConditionsAndLogpoints(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app.js")
	if err := os.WriteFile(file, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	c := start(t)
	c.launch(file, nil, map[string][]map[string]any{file: {{"line": 3, "logMessage": "adding {a} and {b} `x`"}, {"line": 5, "condition": "a === 2 && b === 10"}}})
	c.wait("stopped")
	top := c.topFrame()
	locals := c.locals(top)
	if locals["a"]["value"] != "2" || locals["b"]["value"] != "10" {
		t.Fatalf("stopped at the wrong call: %v", locals)
	}
	if out := c.outputText(); !strings.Contains(out, "adding 2 and 3 `x`") || !strings.Contains(out, "adding 0 and 10") {
		t.Fatalf("logpoint output %q", out)
	}
	c.req("disconnect", map[string]any{})
}

func TestStopOnEntryAndExceptions(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "boom.js")
	if err := os.WriteFile(file, []byte("const x = 1;\nfunction boom() {\n  throw new TypeError('kaput ' + x);\n}\nboom();\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := start(t)
	c.launch(file, map[string]any{"stopOnEntry": true}, nil)
	if st := c.wait("stopped"); st["reason"] != "entry" {
		t.Fatalf("entry %v", st)
	}
	if top := c.topFrame(); top["line"].(float64) != 1 {
		t.Fatalf("entry line %v", top["line"])
	}
	c.req("continue", map[string]any{"threadId": 1})
	st := c.wait("stopped")
	if st["reason"] != "exception" || !strings.Contains(fmt.Sprint(st["text"]), "kaput 1") {
		t.Fatalf("exception %v", st)
	}
	if top := c.topFrame(); top["name"] != "boom" || top["line"].(float64) != 3 {
		t.Fatalf("exception frame %v", top)
	}
	c.req("continue", map[string]any{"threadId": 1})
	if ex := c.wait("exited"); ex["exitCode"].(float64) == 0 {
		t.Fatalf("exit %v", ex)
	}
}

func TestTypeScriptAndModules(t *testing.T) {
	out, err := exec.Command("node", "-p", "process.versions.node.split('.')[0]").Output()
	if err != nil {
		t.Skip("no node")
	}
	major := strings.TrimSpace(string(out))
	dir := t.TempDir()
	mod := filepath.Join(dir, "lib.mjs")
	file := filepath.Join(dir, "main.mjs")
	if err := os.WriteFile(mod, []byte("export function double(n) {\n  return n * 2;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("import { double } from './lib.mjs';\nconsole.log(double(21));\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := start(t)
	c.launch(file, nil, map[string][]map[string]any{mod: {{"line": 2}}})
	c.wait("stopped")
	if top := c.topFrame(); top["name"] != "double" || top["source"].(map[string]any)["path"] != mod {
		t.Fatalf("module frame %v", top)
	}
	c.req("continue", map[string]any{"threadId": 1})
	c.wait("exited")
	if !strings.Contains(c.outputText(), "42") {
		t.Fatalf("output %q", c.outputText())
	}

	if major < "24" && len(major) == 2 {
		t.Log("TypeScript needs Node 24 or a flag: skipped on", major)
		return
	}
	ts := filepath.Join(dir, "typed.ts")
	if err := os.WriteFile(ts, []byte("interface User {\n  id: number;\n}\nconst u: User = { id: 5 };\nconst n: number = u.id + 1;\nconsole.log(n);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := start(t)
	c2.launch(ts, nil, map[string][]map[string]any{ts: {{"line": 6}}})
	c2.wait("stopped")
	if top := c2.topFrame(); top["line"].(float64) != 6 {
		t.Fatalf("ts line %v", top["line"])
	}
	c2.req("continue", map[string]any{"threadId": 1})
	c2.wait("exited")
}

func TestStoppingEndsTheProgram(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "loop.js")
	if err := os.WriteFile(file, []byte("console.log(process.pid);\nsetInterval(() => {}, 1000);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := start(t)
	c.launch(file, nil, nil)
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		fmt.Sscan(lastLine(c.outputText()), &pid)
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatalf("no pid: %q", c.outputText())
	}
	c.req("pause", map[string]any{"threadId": 1})
	if st := c.wait("stopped"); st["reason"] != "pause" && st["reason"] != "step" {
		t.Fatalf("pause %v", st)
	}
	c.req("disconnect", map[string]any{})
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the program %d lives on", pid)
}

func lastLine(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

func TestLaunchProblems(t *testing.T) {
	c := start(t)
	c.req("initialize", map[string]any{"adapterID": "node"})
	if r := c.try("launch", map[string]any{}); r["success"] != false || !strings.Contains(fmt.Sprint(r["message"]), "program") {
		t.Fatalf("no program %v", r)
	}
	if r := c.try("launch", map[string]any{"program": "/tmp/x.js", "runtimeExecutable": "/no/such/node"}); r["success"] != false {
		t.Fatalf("no runtime %v", r)
	}
}

func TestLogCondition(t *testing.T) {
	if got := logCondition("v={a.b} `q` ${x}"); got != "console.log(`v=${a.b} \\`q\\` \\$${x}`), false" {
		t.Fatalf("%s", got)
	}
}

// Compiled TypeScript with source maps: a breakpoint in the .ts stops the .js there, and the
// stack shows the .ts. testdata/ts was compiled by tsc; it is copied, so that its paths are
// this test's own.
func TestSourceMappedBreakpoints(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	for _, f := range []string{"src/greet.ts", "dist/greet.js", "dist/greet.js.map"} {
		b, err := os.ReadFile(filepath.Join("testdata/ts", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(dir, "src", "greet.ts")
	c := start(t)
	resp := c.launch(filepath.Join(dir, "dist", "greet.js"), nil, map[string][]map[string]any{src: {{"line": 7}}})
	_ = resp
	c.wait("stopped")
	top := c.topFrame()
	if top["source"].(map[string]any)["path"] != src || top["line"].(float64) != 7 || top["name"] != "greet" {
		t.Fatalf("frame %v", top)
	}
	if v := c.locals(top); v["p"] == nil {
		t.Fatalf("locals %v", v)
	}
	c.req("continue", map[string]any{"threadId": 1})
	c.wait("exited")
	if !strings.Contains(c.outputText(), "Hello, Ana!") {
		t.Fatalf("output %q", c.outputText())
	}

	// required later by a script without a map: its breakpoints go in before it runs
	main := filepath.Join(dir, "main.js")
	if err := os.WriteFile(main, []byte("setTimeout(() => require('./dist/greet.js'), 100);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := start(t)
	c2.launch(main, nil, map[string][]map[string]any{src: {{"line": 8}}})
	c2.wait("stopped")
	if top := c2.topFrame(); top["source"].(map[string]any)["path"] != src || top["line"].(float64) != 8 {
		t.Fatalf("later module: %v", top)
	}
	c2.req("continue", map[string]any{"threadId": 1})
	c2.wait("exited")
}

// A node process the program forks is a thread of the session: a breakpoint in it stops that
// thread, with its own stack and values, and the program runs on. Without the bootloader the
// child inherited --inspect-brk and waited for a debugger forever.
func TestForkedChildIsAThread(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	main := filepath.Join(dir, "main.js")
	child := filepath.Join(dir, "child.js")
	if err := os.WriteFile(main, []byte("const { fork } = require('child_process');\nconst c = fork(__dirname + '/child.js', ['7']);\nc.on('exit', (code) => console.log('child exit ' + code));\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("const n = Number(process.argv[2]);\nconst twice = n * 2;\nconsole.log('child says ' + twice);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := start(t)
	c.launch(main, nil, map[string][]map[string]any{child: {{"line": 3}}})
	ev := c.wait("stopped")
	tid := ev["threadId"].(float64)
	if tid == 1 || ev["reason"] != "breakpoint" {
		t.Fatalf("stopped %v", ev)
	}
	threads := c.req("threads", nil)["threads"].([]any)
	if len(threads) != 2 {
		t.Fatalf("threads %v", threads)
	}
	frames := c.req("stackTrace", map[string]any{"threadId": tid})["stackFrames"].([]any)
	top := frames[0].(map[string]any)
	if top["source"].(map[string]any)["path"] != child || top["line"].(float64) != 3 {
		t.Fatalf("child frame %v", top)
	}
	if v := c.locals(top); v["twice"] == nil || v["twice"]["value"] != "14" {
		t.Fatalf("child locals %v", v)
	}
	if r := c.req("evaluate", map[string]any{"expression": "n + 1", "frameId": top["id"], "context": "repl"}); r["result"] != "8" {
		t.Fatalf("evaluate in the child: %v", r)
	}
	c.req("continue", map[string]any{"threadId": tid})
	c.wait("exited")
	out := c.outputText()
	if !strings.Contains(out, "child says 14") || !strings.Contains(out, "child exit 0") {
		t.Fatalf("output %q", out)
	}

	// spawned rather than forked: the environment carries the bootloader
	if err := os.WriteFile(main, []byte("const { spawn } = require('child_process');\nspawn(process.execPath, [__dirname + '/child.js', '21'], { stdio: 'inherit' }).on('exit', (code) => console.log('child exit ' + code));\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := start(t)
	c2.launch(main, nil, map[string][]map[string]any{child: {{"line": 3}}})
	ev = c2.wait("stopped")
	frames = c2.req("stackTrace", map[string]any{"threadId": ev["threadId"]})["stackFrames"].([]any)
	if v := c2.locals(frames[0].(map[string]any)); v["twice"] == nil || v["twice"]["value"] != "42" {
		t.Fatalf("spawned child locals %v", v)
	}
	c2.req("continue", map[string]any{"threadId": ev["threadId"]})
	c2.wait("exited")
	if !strings.Contains(c2.outputText(), "child says 42") {
		t.Fatalf("output %q", c2.outputText())
	}
}
