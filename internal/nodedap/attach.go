package nodedap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Attaching to a node process that already runs (docs/API-studio.md §7.5). The adapter did not
// start this program, so everything that ends a launch has to leave it alone: stopping the
// session lets go of the process, and the process stays.

// InspectSocketEnv names a unix socket that leads to the inspector. The hub sets it so that an
// inspector on a server reaches the adapter on This PC without a TCP port here: every login on
// this machine could reach that port, and through it run code in the process.
const InspectSocketEnv = "NEXTBOARD_INSPECT_SOCKET"

var inspectorID = regexp.MustCompile(`^[0-9a-f-]{36}$`)

func (a *adapter) attach(raw json.RawMessage) error {
	var c struct {
		Port           int      `json:"port"`
		Address        string   `json:"address"`
		Cwd            string   `json:"cwd"`
		SkipFiles      []string `json:"skipFiles"`
		OutFiles       []string `json:"outFiles"`
		CloseInspector bool     `json:"closeInspector"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	socket := os.Getenv(InspectSocketEnv)
	host := "127.0.0.1"
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		if socket != "" {
			return d.DialContext(ctx, "unix", socket)
		}
		return d.DialContext(ctx, network, addr)
	}
	if socket == "" {
		if c.Port < 1 || c.Port > 65535 {
			return errors.New("attach needs the inspector's port, or a process to pick")
		}
		if c.Address != "" && c.Address != "localhost" {
			if ip := net.ParseIP(c.Address); ip == nil || !ip.IsLoopback() {
				return errors.New("attach only reaches an inspector on this machine's loopback")
			}
		}
		host = net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port))
	}
	id, err := findInspector(dial, host)
	if err != nil {
		return err
	}
	d := websocket.Dialer{NetDialContext: dial, HandshakeTimeout: inspectWait}
	ws, _, err := d.Dial("ws://"+host+"/"+id, nil)
	if err != nil {
		return fmt.Errorf("could not reach the inspector: %w", err)
	}
	a.mu.Lock()
	a.attached = true
	a.attachedAt = float64(time.Now().UnixMilli())
	a.closeInspector = c.CloseInspector
	a.blackbox = skipPatterns(c.SkipFiles)
	a.outRoots = outRoots(c.Cwd, c.OutFiles)
	a.mu.Unlock()
	t := a.newTarget("main", ws)
	go t.readCDP()
	if err := t.setup(); err != nil {
		return err
	}
	a.event("process", map[string]any{"name": "node", "startMethod": "attach"})
	a.event("output", map[string]any{"category": "console", "output": "Attached. Only console.* calls show here; what the program writes to its own stdout does not. Stopping lets go of the process, which keeps running.\n"})
	return nil
}

// findInspector asks for the inspector's session, and refuses anything that is not node: a
// browser's debugging port answers the same request.
func findInspector(dial func(context.Context, string, string) (net.Conn, error), host string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}}
	get := func(path string, v any) error {
		res, err := client.Get("http://" + host + path)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
	}
	var version struct {
		Browser string `json:"Browser"`
	}
	if err := get("/json/version", &version); err != nil {
		return "", fmt.Errorf("no inspector answers there: %w", err)
	}
	if !strings.HasPrefix(version.Browser, "node.js/") {
		return "", errors.New("what answers there is not a Node.js inspector")
	}
	var list []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := get("/json/list", &list); err != nil {
		return "", fmt.Errorf("no inspector answers there: %w", err)
	}
	for _, t := range list {
		if inspectorID.MatchString(t.ID) {
			return t.ID, nil
		}
	}
	return "", errors.New("the inspector has no session to attach to")
}

// skipPatterns is what steps stay out of: Node's own code, the bootloader, and launch.json's list.
func skipPatterns(skip []string) []string {
	patterns := []string{"^node:", regexp.QuoteMeta(bootFile) + "$"}
	for _, s := range skip {
		if s != "<node_internals>/**" {
			patterns = append(patterns, globRegex(s))
		}
	}
	return patterns
}

// detach lets go of the process. A paused program would otherwise stay paused for whoever comes
// next, and an inspector this session opened with a signal would stay open to every login on
// the machine.
func (a *adapter) detach() {
	a.mu.Lock()
	attached, closing := a.attached, a.closeInspector
	a.mu.Unlock()
	if !attached {
		return
	}
	a.detachOnce.Do(func() {
		for _, t := range a.allTargets() {
			within(2*time.Second, func() { _, _ = t.cdp("Debugger.resume", nil) })
			if closing {
				// The inspector closes under this call, so no answer comes.
				within(2*time.Second, func() {
					_, _ = t.cdp("Runtime.evaluate", map[string]any{"expression": "process._debugEnd()"})
				})
			}
		}
		a.closeCDP()
	})
}

func within(d time.Duration, f func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// attachEnded is the process going away by itself, or the session letting go of it.
func (a *adapter) attachEnded() {
	a.exitOnce.Do(func() {
		a.mu.Lock()
		a.terminated = true
		a.mu.Unlock()
		a.event("terminated", map[string]any{})
	})
}

// onConsole shows what a process prints through console.*. A launch reads the program's real
// output instead, so only an attached session comes here. Runtime.enable replays what was
// printed before, which is not what anyone just attached is looking for.
func (t *target) onConsole(raw json.RawMessage) {
	var p struct {
		Type      string         `json:"type"`
		Args      []remoteObject `json:"args"`
		Timestamp float64        `json:"timestamp"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	t.a.mu.Lock()
	attached, since := t.a.attached, t.a.attachedAt
	t.a.mu.Unlock()
	if !attached || p.Timestamp < since {
		return
	}
	parts := make([]string, 0, len(p.Args))
	for _, o := range p.Args {
		parts = append(parts, consoleText(o))
	}
	category := "stdout"
	switch p.Type {
	case "error", "warning", "assert", "trace":
		category = "stderr"
	}
	t.a.event("output", map[string]any{"category": category, "output": strings.Join(parts, " ") + "\n"})
}

func consoleText(o remoteObject) string {
	switch {
	case o.Type == "string":
		var s string
		if json.Unmarshal(o.Value, &s) == nil {
			return s
		}
	case o.UnserializableValue != "":
		return o.UnserializableValue
	case o.Description != "":
		return o.Description
	case len(o.Value) > 0:
		return string(o.Value)
	}
	return o.Type
}
