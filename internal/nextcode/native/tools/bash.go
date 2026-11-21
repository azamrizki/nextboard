package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	bashTimeout    = 120 * time.Second
	bashMaxTimeout = 600 * time.Second
	bashKeep       = 15000 // characters of output kept at each end
	killGrace      = 2 * time.Second
)

// shells are the shells a command may run in: the first one present.
var shells = []string{"/bin/bash", "/bin/sh"}

const bashDescription = `Runs a shell command (bash -c) and returns its output: stdout and stderr together, in order.

- Each call runs in a new shell in the working folder: a cd or a variable does not carry over to the next call. Use absolute paths, or cd within the command (cd web && npm test).
- Nothing is interactive: stdin is empty and there is no terminal. Give commands the flags that keep them from asking (-y, --yes, --no-edit); pagers are off.
- timeout is in milliseconds: 120000 (2 minutes) by default, at most 600000 (10 minutes). When it runs out, the command and everything it started are stopped.
- What a command starts in the background (&) ends with it: start a server and the commands that use it in the same call.
- Output over 30000 characters is cut in the middle: its first and last 15000 characters are kept. Keep long output short with grep, head, tail or a quiet flag.
- A non-zero exit code is reported, and the call counts as failed.
- When the project has a sandbox, commands can write only in the working folders and the temp folder, and may have no network.
- Prefer the dedicated tools: Read rather than cat, head or tail; Edit and Write rather than sed, awk or echo >; Glob rather than find or ls -R; Grep rather than grep or rg.
- Chain dependent commands with &&; make independent commands separate calls, in parallel.
- description: what the command does, in 5 to 10 words.`

const bashSchema = `{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "The command to run"},
    "timeout": {"type": "number", "description": "Optional timeout in milliseconds (default 120000, at most 600000)"},
    "description": {"type": "string", "description": "What the command does, in 5 to 10 words"}
  },
  "required": ["command"],
  "additionalProperties": false
}`

func bashTool() Tool { return newTool("Bash", KindExec, bashDescription, bashSchema, runBash) }

type bashInput struct {
	Command     string  `json:"command"`
	Timeout     *number `json:"timeout"`
	Description string  `json:"description"`
}

func runBash(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in bashInput
	if err := decode("Bash", input, &in); err != nil {
		return fail("%v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return fail("Bash needs command: the command to run")
	}
	timeout := bashTimeout
	if in.Timeout != nil && *in.Timeout > 0 {
		timeout = min(time.Duration(*in.Timeout)*time.Millisecond, bashMaxTimeout)
	}
	argv, err := commandLine(env, in.Command)
	if err != nil {
		return fail("%v", err)
	}
	return runCommand(ctx, env, argv, timeout)
}

// commandLine is how a command runs: in the shell, inside bubblewrap when the project has a
// sandbox.
func commandLine(env *Env, command string) ([]string, error) {
	argv := []string{shell(), "-c", command}
	if env.Sandbox == nil {
		return argv, nil
	}
	bwrap, err := lookBwrap()
	if err != nil {
		return nil, errors.New(noBwrap)
	}
	return append(sandboxArgs(bwrap, env), argv...), nil
}

func shell() string {
	for _, s := range shells {
		if st, err := os.Stat(s); err == nil && !st.IsDir() {
			return s
		}
	}
	return "/bin/sh"
}

// commandEnv is the environment of a command: the agent's, as it is, plus what keeps a
// command from waiting on a pager, a password prompt or a terminal, unless it sets them itself.
func commandEnv(environ []string) []string {
	out := append([]string{}, environ...)
	set := map[string]bool{}
	for _, kv := range environ {
		if k, _, ok := strings.Cut(kv, "="); ok {
			set[k] = true
		}
	}
	for _, kv := range []string{"PAGER=cat", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "CI=1"} {
		if k, _, _ := strings.Cut(kv, "="); !set[k] {
			out = append(out, kv)
		}
	}
	return out
}

// runCommand runs argv in its own process group, with its output in one pipe so that stdout
// and stderr stay in order. Whatever ends the command (its end, the timeout, the turn's Stop),
// what is left of its group is ended with it: SIGTERM, then SIGKILL after killGrace.
func runCommand(ctx context.Context, env *Env, argv []string, timeout time.Duration) Result {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = env.Cwd
	cmd.Env = commandEnv(env.Environ)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	r, w, err := os.Pipe()
	if err != nil {
		return fail("Could not run the command: %v", err)
	}
	defer r.Close()
	cmd.Stdout, cmd.Stderr = w, w
	err = cmd.Start()
	w.Close()
	if err != nil {
		return fail("Could not run the command: %v", err)
	}
	pgid := cmd.Process.Pid
	out := &clip{headN: bashKeep, tailN: bashKeep}
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, r)
		close(copied)
	}()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var werr error
	ended := "" // why the command was ended before its end: "timeout" or "stopped"
	select {
	case werr = <-waited:
	case <-timer.C:
		ended = "timeout"
	case <-ctx.Done():
		ended = "stopped"
	}
	endGroup(pgid)
	if ended != "" {
		select {
		case werr = <-waited:
		case <-time.After(killGrace): // a process the kernel does not let go of: give up on it
		}
	}
	// the output ends when the last holder of the pipe is gone; one that left the group
	// (setsid) is not waited for
	select {
	case <-copied:
	case <-time.After(killGrace):
		r.Close()
		<-copied
	}

	text := strings.TrimRight(out.String(), "\n")
	if text == "" {
		text = "(no output)"
	}
	switch ended {
	case "timeout":
		return Result{Output: text + "\nTimed out after " + seconds(timeout), IsError: true}
	case "stopped":
		return Result{Output: text + "\nStopped", IsError: true}
	}
	var ee *exec.ExitError
	switch {
	case werr == nil:
		return Result{Output: text}
	case errors.As(werr, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return Result{Output: fmt.Sprintf("%s\nEnded by a signal (%v)", text, ws.Signal()), IsError: true}
		}
		return Result{Output: fmt.Sprintf("%s\nExit code %d", text, ee.ExitCode()), IsError: true}
	}
	return Result{Output: text + "\n" + werr.Error(), IsError: true}
}

// endGroup ends what is left of a process group: SIGTERM, then SIGKILL for what still runs
// after killGrace. It returns at once when the group is already gone.
func endGroup(pgid int) {
	if syscall.Kill(-pgid, syscall.SIGTERM) != nil {
		return
	}
	deadline := time.Now().Add(killGrace)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// seconds is a duration as "120s" or "0.5s".
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}
