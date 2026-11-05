package machine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// LocalID is the fixed id of "This PC".
const LocalID = "local"

// CommandNotFoundError is returned by Start when the command is not on the
// login PATH. The session manager prints it in red into the terminal.
type CommandNotFoundError struct{ Command string }

func (e *CommandNotFoundError) Error() string {
	return fmt.Sprintf("perintah tidak ditemukan: %s", e.Command)
}

// Local runs processes on the hub's own machine through creack/pty.
type Local struct {
	mu        sync.Mutex
	cfg       Config
	shell     string
	loginPath string
	home      string
}

// NewLocal builds the local machine. cfg carries the persisted probe cache.
func NewLocal(cfg Config) *Local {
	cfg.ID = LocalID
	cfg.Kind = "local"
	if cfg.Name == "" {
		cfg.Name = "This PC"
	}
	home, _ := os.UserHomeDir()
	l := &Local{cfg: cfg, shell: LoginShell(), home: home}
	l.loginPath = readLoginPath(l.shell)
	return l
}

// LoginShell returns the user's login shell: $SHELL, then /etc/passwd, then
// a sensible default. systemd user services often run without $SHELL.
func LoginShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if u, err := user.Current(); err == nil {
		if f, err := os.Open("/etc/passwd"); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				parts := strings.Split(sc.Text(), ":")
				if len(parts) >= 7 && (parts[0] == u.Username || parts[2] == u.Uid) && parts[6] != "" {
					return parts[6]
				}
			}
		}
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// readLoginPath asks a login shell for its PATH so that CLIs installed in
// ~/.local/bin etc. resolve even under a minimal service environment.
func readLoginPath(shell string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-lc", `printf '__NB_PATH__%s' "$PATH"`).Output()
	if err == nil {
		s := string(out)
		if i := strings.LastIndex(s, "__NB_PATH__"); i >= 0 {
			if p := strings.TrimSpace(s[i+len("__NB_PATH__"):]); p != "" {
				return p
			}
		}
	}
	return os.Getenv("PATH")
}

func (l *Local) ID() string      { return LocalID }
func (l *Local) Connected() bool { return true }

// pathEnv returns the login PATH (refreshed by Probe).
func (l *Local) pathEnv() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loginPath
}

// LoginPath returns the login shell's PATH.
func (l *Local) LoginPath() string { return l.pathEnv() }

// Shell returns the login shell used for local sessions.
func (l *Local) Shell() string { return l.shell }

func (l *Local) Config() Config {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cfg
}

func (l *Local) setProbe(p *ProbeResult) {
	l.mu.Lock()
	l.cfg.Probe = p
	l.mu.Unlock()
}

func (l *Local) Info() Info {
	c := l.Config()
	return Info{
		ID: c.ID, Name: c.Name, Kind: c.Kind, Port: 0,
		PersistMode: "", Status: StatusLocal, Probe: c.Probe,
	}
}

func (l *Local) expand(p string) string {
	switch {
	case p == "" || p == "~":
		return l.home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(l.home, p[2:])
	}
	return p
}

// lookPath resolves cmd against the login PATH.
func (l *Local) lookPath(cmd string) (string, error) {
	if strings.Contains(cmd, "/") {
		p := l.expand(cmd)
		if isExecutable(p) {
			return p, nil
		}
		return "", &CommandNotFoundError{cmd}
	}
	for _, dir := range filepath.SplitList(l.pathEnv()) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, cmd)
		if isExecutable(p) {
			return p, nil
		}
	}
	return "", &CommandNotFoundError{cmd}
}

// Has reports whether cmd resolves on the login PATH.
func (l *Local) Has(cmd string) bool {
	if cmd == "$SHELL" {
		return true
	}
	_, err := l.lookPath(cmd)
	return err == nil
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// strippedEnv reports whether an inherited variable must not reach a
// terminal: markers of an enclosing Claude Code / NextBoard process would
// confuse the agents and the hub (docs/API-agents.md §3.2).
func strippedEnv(kv string) bool {
	k, _, _ := strings.Cut(kv, "=")
	switch {
	case k == "CLAUDECODE", strings.HasPrefix(k, "CLAUDE_CODE_"), strings.HasPrefix(k, "NB_"), strings.HasPrefix(k, "NEXTBOARD_"):
		return true
	case k == "PATH", k == "TERM", k == "COLORTERM":
		return true // set by the hub
	}
	return false
}

// posixLogin returns the shell that runs wrapper command lines: the login
// shell when it is POSIX, /bin/sh otherwise (fish, nushell, …).
func posixLogin(shell string) string {
	if posixShells[filepath.Base(shell)] {
		return shell
	}
	return "/bin/sh"
}

// Start launches spec in a new PTY. Commands run through the login shell
// (`$SHELL -lc 'exec cmd args'`) so profile environment applies, mirroring
// how remote sessions are started.
func (l *Local) Start(spec StartSpec) (Proc, error) {
	var argv []string
	switch {
	case spec.Shell != "":
		if spec.Check != "" {
			if _, err := l.lookPath(spec.Check); err != nil {
				return nil, err
			}
		}
		argv = []string{posixLogin(l.shell), "-lc", spec.Shell}
	case spec.Command == "$SHELL" || spec.Command == "":
		argv = append([]string{l.shell}, spec.Args...)
	default:
		if _, err := l.lookPath(spec.Command); err != nil {
			return nil, err
		}
		argv = []string{l.shell, "-lc", "exec " + Join(append([]string{spec.Command}, spec.Args...)...)}
	}
	dir := l.expand(spec.Cwd)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("folder tidak ditemukan: %s", dir)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		if !strippedEnv(kv) {
			env = append(env, kv)
		}
	}
	env = append(env,
		"PATH="+l.pathEnv(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"NEXTBOARD_SESSION_ID="+spec.SessionID,
	)
	if os.Getenv("SHELL") == "" {
		env = append(env, "SHELL="+l.shell)
	}
	env = append(env, spec.Env...)
	cmd.Env = env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: spec.Cols, Rows: spec.Rows})
	if err != nil {
		return nil, err
	}
	p := &localProc{cmd: cmd, pty: f, done: make(chan struct{})}
	go p.wait()
	return p, nil
}

type localProc struct {
	cmd  *exec.Cmd
	pty  *os.File
	done chan struct{}
	code int
	err  error
	// fdMu orders ioctls on the raw fd (resize, TIOCGPGRP) against Close:
	// os.File.Fd is not safe concurrently with Close.
	fdMu   sync.RWMutex
	closed bool
}

func (p *localProc) wait() {
	err := p.cmd.Wait()
	p.code = 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				p.code = 128 + int(ws.Signal())
			} else {
				p.code = ee.ExitCode()
			}
		} else {
			p.err = err
			p.code = -1
		}
	}
	close(p.done)
}

func (p *localProc) Read(b []byte) (int, error) {
	n, err := p.pty.Read(b)
	if err != nil && (errors.Is(err, syscall.EIO) || errors.Is(err, fs.ErrClosed)) {
		err = io.EOF
	}
	return n, err
}

func (p *localProc) Write(b []byte) (int, error) { return p.pty.Write(b) }

func (p *localProc) Close() error {
	p.fdMu.Lock()
	defer p.fdMu.Unlock()
	p.closed = true
	return p.pty.Close()
}

// withFd runs f with the PTY fd unless it was closed.
func (p *localProc) withFd(f func() error) error {
	p.fdMu.RLock()
	defer p.fdMu.RUnlock()
	if p.closed {
		return os.ErrClosed
	}
	return f()
}

func (p *localProc) Resize(cols, rows uint16) error {
	return p.withFd(func() error { return pty.Setsize(p.pty, &pty.Winsize{Cols: cols, Rows: rows}) })
}

// Signal sends to the whole process group (pty.Start makes the child a
// session leader, so pgid == pid).
func (p *localProc) Signal(sig string) error {
	if p.cmd.Process == nil {
		return nil
	}
	s := syscall.SIGHUP
	if sig == "KILL" {
		s = syscall.SIGKILL
	}
	err := syscall.Kill(-p.cmd.Process.Pid, s)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (p *localProc) Wait() (int, error) {
	<-p.done
	return p.code, p.err
}

// Pid returns the PID of the process started in the PTY (the wrapper).
func (p *localProc) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Foreground reports the foreground process group of the PTY (TIOCGPGRP
// on the master) and the command name of its leader.
func (p *localProc) Foreground(context.Context, int) (int, string, error) {
	var pgid int
	err := p.withFd(func() (err error) {
		pgid, err = foregroundPgrp(p.pty)
		return err
	})
	if err != nil {
		return 0, "", err
	}
	return pgid, processComm(pgid), nil
}

// SameSession reports whether pid belongs to the terminal session started
// by this process (used to validate a pid sent in a marker).
func (p *localProc) SameSession(pid int) bool {
	return sameSession(p.Pid(), pid)
}

// Probe runs the same probe script as remote machines, via the login shell.
func (l *Local) Probe(ctx context.Context, commands []string) (*ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lp := readLoginPath(l.shell)
	l.mu.Lock()
	l.loginPath = lp
	l.mu.Unlock()
	out, err := exec.CommandContext(ctx, l.shell, "-lc", probeScript(commands)).Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	p := parseProbe(string(out), commands)
	if p.Shell == "" {
		p.Shell = l.shell
	}
	if p.Home == "" {
		p.Home = l.home
	}
	// Resolve against the login PATH too, in case the profile differs.
	for _, c := range commands {
		if c == "" || c == "$SHELL" || p.Commands[c] != "" {
			continue
		}
		if path, err := l.lookPath(c); err == nil {
			p.Commands[c] = path
		}
	}
	if !p.Tmux {
		if _, err := l.lookPath("tmux"); err == nil {
			p.Tmux = true
		}
	}
	l.setProbe(p)
	return p, nil
}

// ListDir lists a local directory (dirs first).
func (l *Local) ListDir(_ context.Context, p string) (*DirListing, error) {
	dir := filepath.Clean(l.expand(p))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(l.home, dir)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := &DirListing{Path: dir, Entries: []DirEntry{}}
	if dir != "/" {
		out.Parent = filepath.Dir(dir)
	}
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue // vanished while listing
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			if target, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				fi = target
			}
		}
		de := DirEntry{Name: e.Name(), Dir: fi.IsDir(), Mtime: fi.ModTime().UTC()}
		if !de.Dir {
			de.Size = fi.Size()
		}
		out.Entries = append(out.Entries, de)
	}
	sortEntries(out.Entries)
	return out, nil
}

func sortEntries(e []DirEntry) {
	sort.Slice(e, func(i, j int) bool {
		if e[i].Dir != e[j].Dir {
			return e[i].Dir
		}
		return strings.ToLower(e[i].Name) < strings.ToLower(e[j].Name)
	})
}
