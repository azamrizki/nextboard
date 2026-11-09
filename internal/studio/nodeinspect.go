package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Finding and opening the inspector of a node process that already runs (docs/API-studio.md
// §7.5). This works the same on This PC and on a server: the scripts run on the project's
// machine, and the inspector is reached through machine.DialLoopback.

// Why an inspector cannot be used.
var (
	ErrNoNodeProcess    = errors.New("that process is gone, or belongs to another user")
	ErrNotNodeProcess   = errors.New("that process is not node")
	ErrNoProcFS         = errors.New("this machine has no /proc: start the program with --inspect and give the port")
	ErrNotNodeInspector = errors.New("no Node.js inspector answers on that port")
	ErrInspectorFailed  = errors.New("node did not open its inspector; another process may hold port 9229")
)

const (
	exitNoNodeProcess  = 90
	exitNotNodeProcess = 91
	exitNoProcFS       = 92
	maxNodeProcesses   = 100
	maxNodeCommand     = 300
	inspectorOpenWait  = 5 * time.Second
	inspectorPoll      = 250 * time.Millisecond
	inspectorProbe     = 3 * time.Second
	inspectorScriptMax = 30 * time.Second
	defaultInspectPort = 9229
)

// NodeProcess is one row of the attach picker.
type NodeProcess struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

// NodeInspector says where a process's inspector is, and whether it took this call to open it.
type NodeInspector struct {
	Port   int  `json:"port"`
	Opened bool `json:"opened"`
}

// NodeAdapterArgv starts the adapter that talks to an inspector. It always runs on This PC,
// whichever machine the inspector is on. A variable so that tests can point it elsewhere.
var NodeAdapterArgv = func() []string { return []string{selfPath(), "dap-node"} }

// readlink on /proc/<pid>/exe answers only for a process this user may trace. That makes the
// list the user's own processes, without asking ps, and the name is the program's: a node
// program can rename itself in ps with process.title.
const listNodeScript = `[ -d /proc/self ] || exit ` + "92" + `; ` +
	`for d in /proc/[0-9]*; do e=$(readlink "$d/exe" 2>/dev/null) || continue; e=${e% (deleted)}; ` +
	`case "${e##*/}" in node|nodejs) printf '%s\t%s\t' "${d#/proc/}" "$(readlink "$d/cwd" 2>/dev/null)"; ` +
	`tr '\000\n\t' '   ' < "$d/cmdline" 2>/dev/null; echo;; esac; done`

// NodeProcesses lists this user's node processes on the machine, the project's own first.
func NodeProcesses(ctx context.Context, h machine.Host, root string) ([]NodeProcess, error) {
	res, err := execScriptIn(ctx, h, root, listNodeScript, nil, inspectorScriptMax, 1<<20)
	if err != nil {
		return nil, err
	}
	if res.ExitCode == exitNoProcFS {
		return nil, ErrNoProcFS
	}
	out := []NodeProcess{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(f[2])
		if len(cmd) > maxNodeCommand {
			cmd = strings.ToValidUTF8(cmd[:maxNodeCommand], "") + "…"
		}
		out = append(out, NodeProcess{PID: pid, Cwd: f[1], Command: cmd})
	}
	inRoot := func(p NodeProcess) bool {
		return root != "" && (p.Cwd == root || strings.HasPrefix(p.Cwd, strings.TrimRight(root, "/")+"/"))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := inRoot(out[i]), inRoot(out[j]); a != b {
			return a
		}
		return out[i].PID < out[j].PID
	})
	if len(out) > maxNodeProcesses {
		out = out[:maxNodeProcesses]
	}
	return out, nil
}

// checkNodeScript stops unless pid is node. It runs right before every signal: SIGUSR1 kills a
// program that does not handle it, and pids get reused.
func checkNodeScript(pid int) string {
	return fmt.Sprintf(`pid=%d; [ -d /proc/self ] || exit %d; e=$(readlink "/proc/$pid/exe" 2>/dev/null) || exit %d; e=${e%% (deleted)}; `+
		`case "${e##*/}" in node|nodejs) ;; *) exit %d;; esac; `, pid, exitNoProcFS, exitNoNodeProcess, exitNotNodeProcess)
}

// listenScript prints, in hex, the ports this process itself listens on, reachable on loopback.
const listenScript = `ls -l "/proc/$pid/fd" 2>/dev/null | sed -n 's/.*socket:\[\([0-9]*\)\].*/\1/p' | ` +
	`awk 'NR == FNR && FILENAME == "-" { s[$1] = 1; next } ` +
	`$4 == "0A" && $2 ~ /^(0100007F|00000000|00000000000000000000000001000000|00000000000000000000000000000000):/ && ($10 in s) { split($2, a, ":"); print a[2] }' ` +
	`- "/proc/$pid/net/tcp" "/proc/$pid/net/tcp6" 2>/dev/null`

func scriptFailed(res *machine.ExecResult) error {
	switch res.ExitCode {
	case 0:
		return nil
	case exitNoNodeProcess:
		return ErrNoNodeProcess
	case exitNotNodeProcess:
		return ErrNotNodeProcess
	case exitNoProcFS:
		return ErrNoProcFS
	}
	msg := strings.TrimSpace(string(res.Stderr))
	if msg == "" {
		msg = fmt.Sprintf("exit %d", res.ExitCode)
	}
	return errors.New(msg)
}

// OpenNodeInspector finds the inspector of pid, and opens it with SIGUSR1 if it has none.
func OpenNodeInspector(ctx context.Context, h machine.Host, root string, pid int) (*NodeInspector, error) {
	if pid < 2 {
		return nil, ErrNoNodeProcess
	}
	if port, err := findProcessInspector(ctx, h, root, pid); err != nil || port != 0 {
		if err != nil {
			return nil, err
		}
		return &NodeInspector{Port: port}, nil
	}
	res, err := execScriptIn(ctx, h, root, checkNodeScript(pid)+`kill -USR1 "$pid"`, nil, inspectorScriptMax, 1<<16)
	if err != nil {
		return nil, err
	}
	if err := scriptFailed(res); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(inspectorOpenWait)
	for time.Now().Before(deadline) {
		if port, err := findProcessInspector(ctx, h, root, pid); err != nil {
			return nil, err
		} else if port != 0 {
			return &NodeInspector{Port: port, Opened: true}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(inspectorPoll):
		}
	}
	return nil, ErrInspectorFailed
}

// findProcessInspector asks each port the process listens on. An application's own server is
// among them, so it may see one GET of /json/version.
func findProcessInspector(ctx context.Context, h machine.Host, root string, pid int) (int, error) {
	res, err := execScriptIn(ctx, h, root, checkNodeScript(pid)+listenScript, nil, inspectorScriptMax, 1<<16)
	if err != nil {
		return 0, err
	}
	if err := scriptFailed(res); err != nil {
		return 0, err
	}
	var ports []int
	for _, f := range strings.Fields(string(res.Stdout)) {
		if n, err := strconv.ParseInt(f, 16, 32); err == nil && n > 0 {
			ports = append(ports, int(n))
		}
	}
	sort.SliceStable(ports, func(i, j int) bool { return ports[i] == defaultInspectPort && ports[j] != defaultInspectPort })
	for _, port := range ports {
		switch err := CheckNodeInspector(ctx, h, port); {
		case err == nil:
			return port, nil
		case errors.Is(err, machine.ErrForwardingDisabled), errors.Is(err, machine.ErrNotConnected):
			return 0, err
		}
	}
	return 0, nil
}

// CheckNodeInspector says whether a Node.js inspector answers on a loopback port of the machine.
// A browser's debugging port answers the same request and is refused.
func CheckNodeInspector(ctx context.Context, h machine.Host, port int) error {
	if port < 1 || port > 65535 {
		return ErrNotNodeInspector
	}
	client := &http.Client{Timeout: inspectorProbe, Transport: &http.Transport{DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return machine.DialLoopback(ctx, h, port) }}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, machine.ErrForwardingDisabled) || errors.Is(err, machine.ErrNotConnected) {
			return err
		}
		return ErrNotNodeInspector
	}
	defer res.Body.Close()
	var v struct {
		Browser string `json:"Browser"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&v) != nil || !strings.HasPrefix(v.Browser, "node.js/") {
		return ErrNotNodeInspector
	}
	return nil
}
