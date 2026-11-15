package instance

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/config"
	"github.com/azamrizki/nextboard/internal/store"
)

var (
	// ErrExists: the instance's data folder, service or name is already there (409
	// instance_exists).
	ErrExists = errors.New("instance already exists")
	// ErrPortInUse: a chosen port cannot be bound, or another instance claims it (409
	// port_in_use).
	ErrPortInUse = errors.New("port in use")
	// ErrBadListen: a listen address that is not host:port (400 bad_listen).
	ErrBadListen = errors.New("bad listen address")
)

// DefaultHealthWait is how long Create waits for a new instance's hub to answer.
const DefaultHealthWait = 15 * time.Second

// CreateOptions says what Create makes.
type CreateOptions struct {
	Name string
	// DataDir is the data folder; "" is DirFor(Name). Only the CLI names one: the web never
	// does, so it can never make a folder anywhere else.
	DataDir string
	// Listen is the hub's address as the user gave it (--listen, or host:port from the web);
	// "" is the picked block's port on Host.
	Listen string
	// Host goes with the picked port when Listen is ""; "" is 127.0.0.1.
	Host         string
	StatusListen string
	ExtraURLs    []string
	HubName      string
	// FolderOnly makes the data folder and nothing else: what `serve` does on its first start
	// of a named instance, as the hub it then runs. No unit, no start, no setup code.
	FolderOnly bool
	// NoStart writes the service's unit but does not start it (install --no-start).
	NoStart bool
	// Bin is the program the service runs; "" is this one.
	Bin string
	// Shell is the user's login shell for the service's consoles; "" is $SHELL, else /bin/sh.
	Shell string
	// Siblings are the instances already on this machine; nil asks List.
	Siblings []Instance
	// URLs lists the addresses a hub on listen is reached at (server.HubURLs); nil is just
	// http://<listen>.
	URLs func(listen string, extra []string) []string
	// HealthWait: how long to wait for the new hub; 0 is DefaultHealthWait.
	HealthWait time.Duration
	// Out gets the service manager's output (the CLI passes its stdout); nil keeps it for the
	// errors.
	Out io.Writer

	// tests: ports moved by shift, and what can be bound
	shift int
	free  func(int) bool
}

// Created is what Create made, as POST /api/instances answers it.
type Created struct {
	Instance     string   `json:"instance"`
	HubID        string   `json:"hubId"`
	DataDir      string   `json:"dataDir"`
	ConfigFile   string   `json:"configFile"`
	Listen       string   `json:"listen"`
	PreviewRange [2]int   `json:"previewRange"`
	Service      *Service `json:"service,omitempty"`
	// Started: the service manager started the hub; Healthy: it answered in time.
	Started bool `json:"started"`
	Healthy bool `json:"healthy"`
	// Command starts the hub by hand when no service manager could (Started false).
	Command string   `json:"command,omitempty"`
	URLs    []string `json:"urls"`
	// SetupCode opens the new hub once, for 30 minutes; it is in SetupURLs.
	SetupCode string    `json:"-"`
	SetupURLs []string  `json:"setupUrls,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	Warnings  []string  `json:"warnings,omitempty"`
}

// Create makes a new instance, the one way every new instance is made (`install --instance`,
// the first `serve --instance`, POST /api/instances):
//
//  1. the name must be valid, and no folder (one that holds anything), service or sibling may
//     have it already;
//  2. it picks the ports: a free block (PickBlock), or the --listen port with a preview range
//     from the next free block; every port must be bindable and no sibling's;
//  3. it makes the data folder (0700) with config.json (instance, hubId, listen), settings.json
//     (the preview range) and presets.json;
//  4. unless FolderOnly: a single-use setup code (30 minutes); then, when this user's service
//     manager answers, the unit or plist, started and waited for (HealthWait). Without one, the
//     result says how to start the hub (Command) and the setup code works once it runs.
//
// A failure before the service starts leaves nothing behind. When the service fails to start,
// Create returns what it made with the error.
func Create(opts CreateOptions) (*Created, error) {
	name := opts.Name
	if err := ValidName(name); err != nil {
		return nil, err
	}
	dir := opts.DataDir
	if dir == "" {
		d, err := DirFor(name)
		if err != nil {
			return nil, err
		}
		dir = d
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if used, err := holdsSomething(dir); err != nil {
		return nil, err
	} else if used {
		return nil, fmt.Errorf("%w: %s is already there", ErrExists, dir)
	}
	siblings := opts.Siblings
	if siblings == nil {
		if siblings, err = List(); err != nil {
			return nil, err
		}
	}
	var others []Instance
	for _, s := range siblings {
		same := canonical(s.DataDir) == canonical(dir)
		if s.Name == name && !(opts.FolderOnly && same) {
			return nil, fmt.Errorf("%w: an instance named %s is already on this machine (%s)", ErrExists, name, s.DataDir)
		}
		if !same {
			others = append(others, s)
		}
	}
	if !opts.FolderOnly {
		if path, err := UnitPath(name); err == nil {
			if _, err := os.Lstat(path); err == nil {
				return nil, fmt.Errorf("%w: the service %s is already there", ErrExists, path)
			}
		}
	}

	block, listen, err := pickPorts(opts, others)
	if err != nil {
		return nil, err
	}

	made := false
	defer func() {
		if !made {
			_ = os.RemoveAll(dir)
		}
	}()
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	extra := opts.ExtraURLs
	if extra == nil {
		extra = []string{}
	}
	cfg := &config.Config{Instance: name, HubID: NewHubID(), HubName: opts.HubName, Listen: listen, StatusListen: opts.StatusListen, ExtraURLs: extra}
	if err := cfg.Save(st); err != nil {
		return nil, err
	}
	if err := st.Save("settings.json", map[string]any{"previews": map[string]any{"portRange": block.PreviewRange}}); err != nil {
		return nil, err
	}
	if err := store.WriteFileAtomic(st.Path("presets.json"), []byte("[]\n")); err != nil {
		return nil, err
	}
	out := &Created{Instance: name, HubID: cfg.HubID, DataDir: st.Dir, ConfigFile: st.Path(config.File), Listen: listen, PreviewRange: block.PreviewRange}
	if opts.URLs != nil {
		out.URLs = opts.URLs(listen, extra)
	}
	if len(out.URLs) == 0 {
		out.URLs = []string{strings.TrimSuffix(HealthURL(listen), "/api/health")}
	}
	if opts.FolderOnly {
		made = true
		return out, nil
	}

	as, err := auth.Open(st)
	if err != nil {
		return nil, err
	}
	code, exp, err := as.NewSetupCode()
	if err != nil {
		return nil, err
	}
	out.SetupCode, out.ExpiresAt = code, exp
	for _, u := range out.URLs {
		out.SetupURLs = append(out.SetupURLs, strings.TrimRight(u, "/")+"/setup?code="+code)
	}

	if !ServiceManager() {
		out.Command = "nextboard serve --instance " + name
		if def, _ := DirFor(name); def != st.Dir {
			out.Command += " --data-dir " + shellWord(st.Dir)
		}
		made = true
		return out, nil
	}
	spec, err := unitSpec(opts, name, st.Dir)
	if err != nil {
		return nil, err
	}
	path, err := WriteUnit(spec)
	if err != nil {
		return nil, err
	}
	made = true // from here on the folder and the unit stay: they are the instance
	out.Service = &Service{UnitPath: path, Bin: spec.Bin}
	if runtime.GOOS == "darwin" {
		out.Service.Kind, out.Service.Name = "launchd", LaunchLabel(name)
	} else {
		out.Service.Kind, out.Service.Name = "systemd", ServiceName(name)+".service"
	}
	if opts.NoStart {
		return out, nil
	}
	warn, err := StartService(name, path, opts.Out)
	out.Warnings = append(out.Warnings, warn...)
	if err != nil {
		return out, fmt.Errorf("the service %s did not start: %w", out.Service.Name, err)
	}
	out.Started = true
	wait := opts.HealthWait
	if wait <= 0 {
		wait = DefaultHealthWait
	}
	out.Healthy = WaitHealthy(listen, wait)
	if !out.Healthy {
		out.Warnings = append(out.Warnings, fmt.Sprintf("the hub does not answer on %s yet; check `nextboard logs --instance %s`", listen, name))
	}
	return out, nil
}

// holdsSomething: path is there and is not an empty folder.
func holdsSomething(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return true, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

func splitListen(listen string) (string, int, error) {
	host, p, err := net.SplitHostPort(listen)
	if err != nil {
		return "", 0, fmt.Errorf("%w %q: use host:port, e.g. 127.0.0.1:12412", ErrBadListen, listen)
	}
	port, err := strconv.Atoi(p)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("%w %q: the port must be 1 to 65535", ErrBadListen, listen)
	}
	return host, port, nil
}

// pickPorts is the new instance's block and hub address, every port checked.
func pickPorts(opts CreateOptions, others []Instance) (Block, string, error) {
	free := opts.free
	if free == nil {
		free = PortFree
	}
	claimed := map[int]string{}
	for _, s := range others {
		for _, p := range s.Ports() {
			if _, ok := claimed[p]; !ok {
				claimed[p] = s.Name
			}
		}
	}
	check := func(port int, what string) error {
		if by, ok := claimed[port]; ok {
			return fmt.Errorf("%w: %s %d is instance %s's", ErrPortInUse, what, port, by)
		}
		if !free(port) {
			return fmt.Errorf("%w: %s %d is taken on this machine", ErrPortInUse, what, port)
		}
		return nil
	}
	hubPort := 0
	if opts.Listen != "" {
		_, p, err := splitListen(opts.Listen)
		if err != nil {
			return Block{}, "", err
		}
		if err := check(p, "port"); err != nil {
			return Block{}, "", err
		}
		hubPort = p
	}
	block, err := pickBlock(opts.shift, others, hubPort, free)
	if err != nil {
		return Block{}, "", err
	}
	listen := opts.Listen
	if listen == "" {
		host := opts.Host
		if host == "" {
			host = "127.0.0.1"
		}
		if strings.ContainsAny(host, "/ \t") || (net.ParseIP(host) == nil && strings.Contains(host, ":")) {
			return Block{}, "", fmt.Errorf("%w: host %q", ErrBadListen, host)
		}
		listen = net.JoinHostPort(host, strconv.Itoa(block.HubPort))
	}
	if opts.StatusListen != "" {
		_, p, err := splitListen(opts.StatusListen)
		if err != nil {
			return Block{}, "", fmt.Errorf("--status-listen: %w", err)
		}
		if p == block.HubPort || (p >= block.PreviewRange[0] && p <= block.PreviewRange[1]) {
			return Block{}, "", fmt.Errorf("%w: the status port %d is one of the instance's own", ErrPortInUse, p)
		}
		if err := check(p, "status port"); err != nil {
			return Block{}, "", err
		}
	}
	return block, listen, nil
}

func unitSpec(opts CreateOptions, name, dir string) (UnitSpec, error) {
	bin := opts.Bin
	if bin == "" {
		self, err := os.Executable()
		if err != nil {
			return UnitSpec{}, err
		}
		if bin, err = filepath.EvalSymlinks(self); err != nil {
			return UnitSpec{}, err
		}
	}
	h, err := home()
	if err != nil {
		return UnitSpec{}, err
	}
	shell := opts.Shell
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	return UnitSpec{Name: name, Bin: bin, DataDir: dir, Shell: shell, Home: h}, nil
}

// shellWord quotes s for a command line a user copies, when it needs it.
func shellWord(s string) string {
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || r == '~' || r == '+' || r == ',' || r == ':' || r == '@' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ClaimedBy is the instance among siblings (List's result) that claims port as its hub, status
// or preview port, "" for none.
func ClaimedBy(siblings []Instance, port int) string {
	for _, s := range siblings {
		if slices.Contains(s.Ports(), port) {
			return s.Name
		}
	}
	return ""
}
