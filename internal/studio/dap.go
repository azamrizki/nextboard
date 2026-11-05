package studio

import (
	"bufio"
	"context"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"os"
)

// Debug adapters (docs/API-studio.md §7.5): the hub starts the machine's own
// adapter in the project folder and bridges it to a WebSocket with the same
// framing as a language server. Nothing is installed for the user, and nothing
// runs until the user starts a debug session.

// adapterCommand is one way to start an adapter. check is a POSIX test for
// whether it exists; tcp means it listens on a port it prints instead of
// speaking on stdio.
type adapterCommand struct {
	check string
	argv  []string
	tcp   bool
	// hub: argv runs this hub's own program, which is only on This PC.
	hub bool
}

// DebugAdapter is one entry of the catalog.
type DebugAdapter struct {
	ID        string
	Name      string
	Types     []string // launch.json "type" values
	Languages []string // Monaco language ids
	commands  []adapterCommand
	Install   string
}

func onPath(name string) string { return `command -v ` + name + ` >/dev/null 2>&1` }

func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return "nextboard"
	}
	return p
}

// DebugAdapters is the catalog.
var DebugAdapters = []DebugAdapter{
	{ID: "go", Name: "Delve", Types: []string{"go"}, Languages: []string{"go"}, Install: "go install github.com/go-delve/delve/cmd/dlv@latest",
		commands: []adapterCommand{{check: onPath("dlv"), argv: []string{"dlv", "dap", "--listen=127.0.0.1:0"}, tcp: true}}},
	{ID: "python", Name: "debugpy", Types: []string{"python", "debugpy"}, Languages: []string{"python"}, Install: "python3 -m pip install --user debugpy",
		commands: []adapterCommand{{check: `python3 -c 'import debugpy' >/dev/null 2>&1`, argv: []string{"python3", "-m", "debugpy.adapter"}}}},
	{ID: "native", Name: "LLDB / GDB", Types: []string{"lldb", "lldb-dap", "cppdbg", "gdb", "rust"}, Languages: []string{"c", "cpp", "rust"}, Install: "sudo apt install lldb",
		commands: []adapterCommand{
			{check: onPath("lldb-dap"), argv: []string{"lldb-dap"}},
			{check: onPath("lldb-vscode"), argv: []string{"lldb-vscode"}},
			{check: onPath("gdb"), argv: []string{"gdb", "--interpreter=dap"}},
		}},
	// no download: the hub itself speaks to Node's inspector
	{ID: "node", Name: "Node.js", Types: []string{"node", "pwa-node"}, Languages: []string{"javascript", "typescript"}, Install: "Install Node.js on This PC; on a server, debug from a console there.",
		commands: []adapterCommand{{check: onPath("node"), argv: []string{selfPath(), "dap-node"}, hub: true}}},
	{ID: "php", Name: "PHP Debug (Xdebug)", Types: []string{"php"}, Languages: []string{"php"}, Install: "Put php-debug-adapter from vscode-php-debug on the PATH, with Xdebug enabled",
		commands: []adapterCommand{{check: onPath("php-debug-adapter"), argv: []string{"php-debug-adapter"}}}},
}

// ErrNoDebugAdapter: the catalog has no such adapter or none is installed.
var ErrNoDebugAdapter = errors.New("debug adapter tidak ditemukan di machine ini")

// DebugAdapterStatus is one row of GET /api/projects/{id}/dap.
type DebugAdapterStatus struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Types     []string `json:"types"`
	Languages []string `json:"languages"`
	Available bool     `json:"available"`
	Command   string   `json:"command"`
	Install   string   `json:"install"`
}

// probeAdapters returns which commands exist, as "adapter/index".
func probeAdapters(ctx context.Context, h machine.Host, root string) (map[string]bool, error) {
	var b strings.Builder
	b.WriteString(lspPath)
	for _, a := range DebugAdapters {
		for i, c := range a.commands {
			if c.hub && h.ID() != machine.LocalID {
				continue
			}
			b.WriteString(c.check + ` && printf '%s\n' ` + machine.Quote(a.ID+"/"+strconv.Itoa(i)) + `; `)
		}
	}
	b.WriteString(`exit 0`)
	res, err := execScriptIn(ctx, h, root, b.String(), nil, lspProbeTimeout, 1<<16)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for _, n := range strings.Fields(string(res.Stdout)) {
		found[n] = true
	}
	return found, nil
}

// DebugAdapterList lists the catalog with what the machine has.
func DebugAdapterList(ctx context.Context, h machine.Host, root string) ([]DebugAdapterStatus, error) {
	found, err := probeAdapters(ctx, h, root)
	if err != nil {
		return nil, err
	}
	out := make([]DebugAdapterStatus, 0, len(DebugAdapters))
	for _, a := range DebugAdapters {
		st := DebugAdapterStatus{ID: a.ID, Name: a.Name, Types: a.Types, Languages: a.Languages, Install: a.Install}
		for i, c := range a.commands {
			if found[a.ID+"/"+strconv.Itoa(i)] {
				st.Available, st.Command = true, strings.Join(c.argv, " ")
				break
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// ResolvedAdapter says how to start an adapter on a machine.
type ResolvedAdapter struct {
	Script string
	TCP    bool
}

// ResolveDebugAdapter picks the first installed command of adapter id.
func ResolveDebugAdapter(ctx context.Context, h machine.Host, root, id string) (*ResolvedAdapter, error) {
	found, err := probeAdapters(ctx, h, root)
	if err != nil {
		return nil, err
	}
	for _, a := range DebugAdapters {
		if a.ID != id {
			continue
		}
		for i, c := range a.commands {
			if found[a.ID+"/"+strconv.Itoa(i)] {
				return &ResolvedAdapter{Script: lspPath + "exec " + machine.Join(c.argv...), TCP: c.tcp}, nil
			}
		}
	}
	return nil, ErrNoDebugAdapter
}

var adapterListening = regexp.MustCompile(`listening at:?\s*\S*?:(\d+)`)

// adapterPortWait bounds how long an adapter may take to announce its port.
const adapterPortWait = 20 * time.Second

// AdapterPort reads a TCP adapter's output until it prints the port it listens
// on, then keeps draining it so that the adapter never blocks on a full pipe.
func AdapterPort(stdout io.Reader) (int, error) {
	type result struct {
		port int
		err  error
	}
	got := make(chan result, 1)
	go func() {
		br := bufio.NewReader(stdout)
		for {
			line, err := br.ReadString('\n')
			if m := adapterListening.FindStringSubmatch(line); m != nil {
				p, _ := strconv.Atoi(m[1])
				got <- result{port: p}
				_, _ = io.Copy(io.Discard, br)
				return
			}
			if err != nil {
				got <- result{err: errors.New("debug adapter berhenti sebelum membuka port")}
				return
			}
		}
	}()
	select {
	case r := <-got:
		return r.port, r.err
	case <-time.After(adapterPortWait):
		return 0, errors.New("debug adapter tidak membuka port")
	}
}
