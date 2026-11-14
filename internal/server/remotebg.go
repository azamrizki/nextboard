package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Background commands on a server (docs/API-agents.md §21): a build, an install or a test run
// that takes minutes starts with bash run_in_background, and the agent reads what it has
// printed so far with bash_output, as Claude Code's own Bash lets it. Its output stays on the
// hub, the tail of it, until it is read.

const (
	bgMaxPerConsole = 4
	bgMaxRun        = time.Hour
	bgMaxOutput     = 1 << 20 // kept per command; older output is dropped
	bgWaitMax       = 30 * time.Second
	bgKeepDone      = 30 * time.Minute // a finished command is forgotten after this
)

type bgCommand struct {
	id, session, command string
	started              time.Time
	cancel               context.CancelFunc

	mu      sync.Mutex
	out     []byte // not read yet
	dropped int    // bytes dropped before being read, for want of room
	done    bool
	ended   time.Time
	exit    int
	err     string
	changed chan struct{} // closed and replaced when output arrives or it ends
}

type bgCommands struct {
	mu   sync.Mutex
	byID map[string]*bgCommand
}

func newBgID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "bg_" + hex.EncodeToString(b)
}

// start runs command on the server for a console, in the background.
func (b *bgCommands) start(host machine.Host, session, cwd, command string) (*bgCommand, error) {
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("command is empty")
	}
	b.mu.Lock()
	if b.byID == nil {
		b.byID = map[string]*bgCommand{}
	}
	running := 0
	for id, c := range b.byID {
		c.mu.Lock()
		if c.done && time.Since(c.ended) > bgKeepDone {
			delete(b.byID, id)
		} else if c.session == session && !c.done {
			running++
		}
		c.mu.Unlock()
	}
	if running >= bgMaxPerConsole {
		b.mu.Unlock()
		return nil, fmt.Errorf("%d background commands are running already: stop one with bash_kill first", running)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &bgCommand{id: newBgID(), session: session, command: command, started: time.Now(), cancel: cancel, changed: make(chan struct{})}
	b.byID[c.id] = c
	b.mu.Unlock()

	go func() {
		res, err := machine.Exec(ctx, host, machine.ExecSpec{Cwd: cwd, Script: `exec "${SHELL:-/bin/sh}" -lc ` + machine.Quote(command) + ` 2>&1`,
			Timeout: bgMaxRun, MaxOutput: 1, OnStdout: c.add})
		c.mu.Lock()
		c.done, c.ended, c.exit = true, time.Now(), -1
		switch {
		case err != nil:
			c.err = err.Error()
		case res.Cancelled:
			c.err = "stopped with bash_kill"
		case res.TimedOut:
			c.err = fmt.Sprintf("stopped after %s, the longest a background command may run", bgMaxRun)
		default:
			c.exit = res.ExitCode
		}
		c.signalLocked()
		c.mu.Unlock()
	}()
	return c, nil
}

func (c *bgCommand) add(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.out = append(c.out, p...)
	if over := len(c.out) - bgMaxOutput; over > 0 {
		c.out = c.out[over:]
		c.dropped += over
	}
	c.signalLocked()
}

func (c *bgCommand) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// consoleGone stops the commands of a deleted console and forgets them.
func (b *bgCommands) consoleGone(session string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, c := range b.byID {
		if c.session == session {
			c.cancel()
			delete(b.byID, id)
		}
	}
}

func (b *bgCommands) get(session, id string) (*bgCommand, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, c := range b.byID { // the finished ones go here too, not only when another starts
		c.mu.Lock()
		if c.done && time.Since(c.ended) > bgKeepDone {
			delete(b.byID, id)
		}
		c.mu.Unlock()
	}
	c, ok := b.byID[id]
	if !ok || c.session != session {
		return nil, false
	}
	return c, true
}

// read gives what was printed since the last read, waiting up to wait for something new.
func (c *bgCommand) read(ctx context.Context, wait time.Duration) string {
	c.mu.Lock()
	if len(c.out) == 0 && !c.done && wait > 0 {
		ch := c.changed
		c.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(wait):
		case <-ctx.Done():
		}
		c.mu.Lock()
	}
	defer c.mu.Unlock()
	var b strings.Builder
	if c.dropped > 0 {
		fmt.Fprintf(&b, "[%d bytes of earlier output were dropped]\n", c.dropped)
		c.dropped = 0
	}
	take := min(len(c.out), remoteOutputMax)
	b.Write(c.out[:take])
	c.out = c.out[take:]
	if len(c.out) == 0 {
		c.out = nil // not the read array: it stays until the command is forgotten
	}
	more := len(c.out) > 0
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	switch {
	case more:
		b.WriteString("[more output is waiting: call bash_output again]")
	case !c.done:
		fmt.Fprintf(&b, "[still running for %s]", time.Since(c.started).Round(time.Second))
	case c.err != "":
		fmt.Fprintf(&b, "[ended: %s]", c.err)
	default:
		fmt.Fprintf(&b, "[exited with code %d after %s]", c.exit, c.ended.Sub(c.started).Round(time.Second))
	}
	return b.String()
}

// stop ends a background command.
func (c *bgCommand) stop() {
	c.cancel()
}

// bgTool answers bash run_in_background, bash_output and bash_kill for a console.
func (s *Server) bgTool(ctx context.Context, host machine.Host, t remoteTarget, name string, args json.RawMessage) (string, bool) {
	if t.sessionID == "" {
		return "Background commands are for consoles.", true
	}
	var a struct {
		Command string `json:"command"`
		ID      string `json:"id"`
		WaitMs  int64  `json:"wait_ms"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "invalid arguments", true
	}
	switch name {
	case "bash":
		c, err := s.bg.start(host, t.sessionID, t.cwd, a.Command)
		if err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("Started in the background as %s. Read what it prints with bash_output (id %q); stop it with bash_kill.", c.id, c.id), false
	case "bash_output":
		c, ok := s.bg.get(t.sessionID, a.ID)
		if !ok {
			return fmt.Sprintf("No background command %q in this console.", a.ID), true
		}
		wait := time.Duration(a.WaitMs) * time.Millisecond
		if wait < 0 || a.WaitMs == 0 {
			wait = 0
		}
		return c.read(ctx, min(wait, bgWaitMax)), false
	case "bash_kill":
		c, ok := s.bg.get(t.sessionID, a.ID)
		if !ok {
			return fmt.Sprintf("No background command %q in this console.", a.ID), true
		}
		c.stop()
		return fmt.Sprintf("Stopping %s.", c.id), false
	}
	return "unknown tool " + name, true
}
