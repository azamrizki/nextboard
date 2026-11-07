// Package gitsvc is the hub's Git service (docs/API-studio.md §3, B20–B22):
// status, diff, log, commit, push/pull/fetch jobs, discard to a trash
// folder inside the repo, init, branches, commit-subject suggestions and
// GitHub publishing through the `gh` CLI already installed on the machine.
//
// Every command is built from a fixed template (paths after `--`, refs
// checked with `git check-ref-format`), runs through machine.Exec with a
// timeout and an idle watchdog, and never prompts: GIT_TERMINAL_PROMPT=0,
// SSH BatchMode, no askpass helpers, stdin /dev/null. Nothing is written to
// a repository except on an explicit user action.
package gitsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
)

// Timeouts (docs/API-studio.md §3.2, §15).
var (
	ReadTimeout   = 10 * time.Second
	CommitTimeout = 120 * time.Second
	NetTimeout    = 15 * time.Minute
	NetIdle       = 5 * time.Minute
)

// Limits.
const (
	MaxFiles     = 400
	MaxDiff      = 512 << 10
	MaxLog       = 100
	MaxBranches  = 200
	LargeBytes   = 100 << 20 // GitHub's hard limit (100 MiB)
	maxPaths     = 1000
	maxMessage   = 16 << 10
	maxTrashList = 50
)

// Repo is the repository a request works on.
type Repo struct {
	ProjectID string
	MachineID string
	Path      string // project folder (may be below the repo root)
	Sub       string // repository below Path, relative to it ("" = Path's own)
}

// In selects the repository at sub, relative to the project folder ("" or
// "." = the project's own).
func (r Repo) In(sub string) (Repo, error) {
	r.Sub = ""
	if sub == "" || sub == "." {
		return r, nil
	}
	c, err := cleanPath(sub)
	if err != nil {
		return r, err
	}
	r.Sub = c
	return r, nil
}

// Dir is the folder git runs in.
func (r Repo) Dir() string {
	if r.Sub == "" {
		return r.Path
	}
	return path.Join(r.Path, r.Sub)
}

// key identifies the repository in the service's state.
func (r Repo) key() string {
	if r.Sub == "" {
		return r.ProjectID
	}
	return r.ProjectID + "/" + r.Sub
}

// Error is a failure with an HTTP status and a stable code.
type Error struct {
	Status int
	Code   string
	Msg    string
	Paths  []string
}

func (e *Error) Error() string { return e.Msg }

func newErr(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Msg: fmt.Sprintf(format, a...)}
}

var (
	errBusy = func() *Error {
		return newErr(http.StatusConflict, "git_busy", "another git operation is running in this project")
	}
	errNotRepo = func() *Error {
		return newErr(http.StatusConflict, "not_repo", "the project folder is not a git repository")
	}
	errNoGit = func() *Error {
		return newErr(http.StatusNotImplemented, "git_missing", "git was not found on this machine")
	}
	errNotFound = func() *Error {
		return newErr(http.StatusNotFound, "machine_not_found", "%s", machine.ErrNotFound.Error())
	}
)

// Service runs git for projects. Host, Jobs and Publish are set by the hub.
type Service struct {
	Host func(machineID string) (machine.Host, bool)
	Jobs *jobs.Manager
	// Publish sends an /api/events message ({"type":"git"}, {"type":"github"}).
	Publish func(v any)
	// Runner submits the commit-subject runner job.
	Runner func(req jobs.RunnerRequest) (jobs.Job, error)
	// RunnerAgent picks the runner agent and command for a machine
	// (settings runner.defaultAgent and agents.paths).
	RunnerAgent func(machineID string) (agent, command string)
	// Gh is the GitHub CLI command ("gh"); tests point it at a fake.
	Gh string
	// Now is the clock (trash folder names).
	Now func() time.Time
	// LoginPoll is the `gh auth status` interval while a login console
	// lives (5 s).
	LoginPoll time.Duration

	mu        sync.Mutex
	busy      map[string]string   // Repo.key → operation
	selection map[string][]string // Repo.key → last checked panel paths
	last      map[string]Summary  // Repo.key → last published summary
	ghWatch   map[string]bool     // projectID → login watcher running
}

// New returns a service; callers set Host/Jobs/Publish.
func New() *Service {
	return &Service{Gh: "gh", Now: time.Now, LoginPoll: 5 * time.Second, busy: map[string]string{}, selection: map[string][]string{},
		last: map[string]Summary{}, ghWatch: map[string]bool{}}
}

// lock marks a mutating operation on a repository (≤ 1 per repository).
func (s *Service) lock(r Repo, op string) (func(), error) {
	k := r.key()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.busy[k]; ok {
		return nil, errBusy()
	}
	s.busy[k] = op
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.busy, k)
			s.mu.Unlock()
		})
	}, nil
}

// Busy reports the running mutating operation of a repository ("" = none).
func (s *Service) Busy(r Repo) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy[r.key()]
}

// SetSelection remembers the paths checked in the Git panel; the command
// bar's "commit and push" commits them (API-studio §3.4).
func (s *Service) SetSelection(r Repo, paths []string) error {
	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		c, err := cleanPath(p)
		if err != nil {
			return err
		}
		clean = append(clean, c)
	}
	if len(clean) > maxPaths {
		return newErr(http.StatusBadRequest, "too_many", "at most %d paths", maxPaths)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(clean) == 0 {
		delete(s.selection, r.key())
	} else {
		s.selection[r.key()] = clean
	}
	return nil
}

// Selection returns the last panel selection of a repository.
func (s *Service) Selection(r Repo) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.selection[r.key()]...)
}

func (s *Service) host(r Repo) (machine.Host, error) {
	if s.Host == nil {
		return nil, errNotFound()
	}
	h, ok := s.Host(r.MachineID)
	if !ok {
		return nil, errNotFound()
	}
	if !h.Connected() {
		return nil, newErr(http.StatusServiceUnavailable, "machine_offline", "%s", machine.ErrNotConnected.Error())
	}
	return h, nil
}

// Exit codes and stderr markers of the script prelude.
const (
	exitNoGit  = 120
	exitNoRepo = 121
	markNoGit  = "__NB_NOGIT__"
	markNoRepo = "__NB_NOREPO__"
)

// gitEnv makes git non-interactive (docs/API-studio.md §3.2). A user's
// own GIT_SSH_COMMAND / GIT_SSH / core.sshCommand is kept as is.
const gitEnv = `unset GIT_ASKPASS SSH_ASKPASS; ` +
	`export GIT_TERMINAL_PROMPT=0 SSH_ASKPASS_REQUIRE=never GCM_INTERACTIVE=never GIT_EDITOR=true ` +
	`GIT_SEQUENCE_EDITOR=true GIT_MERGE_AUTOEDIT=no GIT_PAGER=cat PAGER=cat LC_ALL=C GIT_LITERAL_PATHSPECS=1 ` +
	`GH_PROMPT_DISABLED=1 GH_NO_UPDATE_NOTIFIER=1 NO_COLOR=1; ` +
	`[ -n "${GIT_SSH_COMMAND:-}" ] || [ -n "${GIT_SSH:-}" ] || [ -n "$(git config --get core.sshCommand 2>/dev/null)" ] || ` +
	`export GIT_SSH_COMMAND='ssh -o BatchMode=yes'; `

// prelude checks git, sets the environment and (repo=true) changes to the
// repository root.
func prelude(repo, readOnly bool) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`command -v git >/dev/null 2>&1 || { echo %s >&2; exit %d; }; `, markNoGit, exitNoGit))
	b.WriteString(gitEnv)
	if readOnly {
		b.WriteString(`export GIT_OPTIONAL_LOCKS=0; `)
		b.WriteString(machine.GitNoRepoCode)
	}
	if repo {
		b.WriteString(fmt.Sprintf(`top=$(git rev-parse --show-toplevel 2>/dev/null) && [ -n "$top" ] || { echo %s >&2; exit %d; }; cd "$top" || exit 122; `, markNoRepo, exitNoRepo))
	}
	if readOnly {
		// Looking at a repository must never run anything out of its config.
		b.WriteString(machine.GitNoRepoFilters)
	}
	return b.String()
}

// enterSub moves a script from the project folder into the selected
// repository. It must still be a repository root there, reached without a
// symlink: otherwise git would walk up to an outer repository or work
// outside the project.
func enterSub(sub string) string {
	q := machine.Quote(sub)
	return `nb_base=$(pwd -P); nb_want=${nb_base%/}/` + q + `; cd -- ` + q + ` 2>/dev/null && [ "$(pwd -P)" = "$nb_want" ] && ` +
		`{ ! command -v git >/dev/null 2>&1 || [ "$(git rev-parse --show-toplevel 2>/dev/null)" = "$nb_want" ]; } || ` +
		fmt.Sprintf(`{ echo %s >&2; exit %d; }; `, markNoRepo, exitNoRepo)
}

// execOpts tune one script run.
type execOpts struct {
	timeout, idle time.Duration
	maxOutput     int64
	onStdout      func([]byte)
	stdin         []byte
}

// exec runs a git script in the project folder and maps transport and
// prelude failures to *Error.
func (s *Service) exec(ctx context.Context, r Repo, script string, o execOpts) (*machine.ExecResult, error) {
	h, err := s.host(r)
	if err != nil {
		return nil, err
	}
	if o.timeout <= 0 {
		o.timeout = ReadTimeout
	}
	if o.idle <= 0 {
		o.idle = o.timeout
	}
	if r.Sub != "" {
		script = enterSub(r.Sub) + script
	}
	// The scripts are POSIX sh. On SSH machines Exec runs them in the
	// user's login shell (zsh would read "$b:r" as a history modifier and
	// fail on unmatched globs), so they always run under /bin/sh; the
	// login PATH is inherited.
	spec := machine.ExecSpec{Cwd: r.Path, Script: "exec /bin/sh -c " + machine.Quote(script), Timeout: o.timeout, IdleTimeout: o.idle,
		MaxOutput: o.maxOutput, OnStdout: o.onStdout, Stdin: o.stdin}
	res, err := machine.Exec(ctx, h, spec)
	switch {
	case errors.Is(err, machine.ErrNotConnected), errors.Is(err, machine.ErrDisconnected):
		return nil, newErr(http.StatusServiceUnavailable, "machine_offline", "%v", err)
	case errors.Is(err, machine.ErrSessionsFull):
		return nil, newErr(http.StatusServiceUnavailable, "ssh_sessions_full", "%v", err)
	case err != nil:
		return nil, newErr(http.StatusBadGateway, "git_failed", "%v", err)
	}
	switch {
	case res.Cancelled:
		return res, newErr(499, "cancelled", "cancelled")
	case res.TimedOut:
		return res, newErr(http.StatusGatewayTimeout, "timeout", "git took longer than %s", o.timeout)
	case res.IdleKilled:
		return res, newErr(http.StatusGatewayTimeout, "idle_timeout", "git gave no output for %s", o.idle)
	case res.ExitCode == exitNoGit && strings.Contains(string(res.Stderr), markNoGit):
		return res, errNoGit()
	case res.ExitCode == exitNoRepo && strings.Contains(string(res.Stderr), markNoRepo):
		return res, errNotRepo()
	case res.ExitCode == 126:
		return res, newErr(http.StatusNotFound, "not_found", "the project folder cannot be opened")
	}
	return res, nil
}

// gitMessage is the useful tail of git's stderr (≤ 600 chars).
func gitMessage(res *machine.ExecResult) string {
	if res == nil {
		return ""
	}
	msg := strings.TrimSpace(string(res.Stderr))
	if msg == "" {
		msg = strings.TrimSpace(string(res.Stdout))
	}
	var keep []string
	for _, l := range strings.Split(msg, "\n") {
		l = strings.TrimSpace(strings.TrimRight(l, "\r"))
		if l == "" || strings.HasPrefix(l, "hint:") {
			continue
		}
		if i := strings.LastIndex(l, "\r"); i >= 0 {
			l = l[i+1:]
		}
		keep = append(keep, l)
	}
	msg = strings.Join(keep, "\n")
	if r := []rune(msg); len(r) > 600 {
		msg = "…" + string(r[len(r)-600:])
	}
	return msg
}

// failed wraps a non-zero git exit.
func failed(res *machine.ExecResult, code string, status int) *Error {
	msg := gitMessage(res)
	if msg == "" {
		msg = fmt.Sprintf("git exited with code %d", res.ExitCode)
	}
	return &Error{Status: status, Code: code, Msg: msg}
}

// Summary is the compact status carried by {"type":"git"} events.
type Summary struct {
	Branch  string `json:"branch"`
	Ahead   int    `json:"ahead"`
	Behind  int    `json:"behind"`
	Changed int    `json:"changed"`
}

// summary is the compact form of a status.
func (st *Status) summary() Summary {
	sum := Summary{Branch: st.Branch, Ahead: st.Ahead, Behind: st.Behind, Changed: len(st.Files)}
	if st.Truncated {
		sum.Changed = st.Changed
	}
	return sum
}

// publishStatus emits {"type":"git"} when the summary changed (force:
// always, after a mutation). A repository below the project folder uses
// {"type":"gitRepo"}: clients that only know the project's own repository
// must not read its summary as theirs.
func (s *Service) publishStatus(r Repo, st *Status, force bool) {
	if st == nil || r.ProjectID == "" || s.Publish == nil {
		return
	}
	sum := st.summary()
	s.mu.Lock()
	prev, ok := s.last[r.key()]
	s.last[r.key()] = sum
	s.mu.Unlock()
	if ok && prev == sum && !force {
		return
	}
	if r.Sub != "" {
		s.Publish(map[string]any{"type": "gitRepo", "projectId": r.ProjectID, "repo": r.Sub, "status": sum})
		return
	}
	s.Publish(map[string]any{"type": "git", "projectId": r.ProjectID, "status": sum})
}

// refresh returns the status after a mutation (and publishes it); a
// failing status read is not an error of the mutation.
func (s *Service) refresh(r Repo) *Status {
	ctx, cancel := context.WithTimeout(context.Background(), ReadTimeout+2*time.Second)
	defer cancel()
	st, err := s.Status(ctx, r)
	if err != nil {
		return nil
	}
	s.publishStatus(r, st, true)
	return st
}

// submit starts a git/publish job holding the repository lock until it ends.
func (s *Service) submit(r Repo, kind, purpose string, run func(ctx context.Context, rep *jobs.Reporter) (json.RawMessage, error), onFinish func(jobs.Job)) (jobs.Job, error) {
	if s.Jobs == nil {
		return jobs.Job{}, newErr(http.StatusServiceUnavailable, "jobs_unavailable", "jobs are not available")
	}
	if _, err := s.host(r); err != nil {
		return jobs.Job{}, err
	}
	unlock, err := s.lock(r, purpose)
	if err != nil {
		return jobs.Job{}, err
	}
	j, err := s.Jobs.SubmitFunc(jobs.FuncJob{Kind: kind, Purpose: purpose, ProjectID: r.ProjectID, MachineID: r.MachineID,
		Run: func(ctx context.Context, rep *jobs.Reporter) (json.RawMessage, error) {
			defer unlock()
			res, err := run(ctx, rep)
			var ge *Error
			if errors.As(err, &ge) && ge.Code != "cancelled" {
				return res, &jobs.FuncError{Code: ge.Code, Msg: ge.Msg}
			}
			return res, err
		},
		OnFinish: func(j jobs.Job) {
			unlock()
			s.refresh(r)
			if onFinish != nil {
				onFinish(j)
			}
		}})
	if err != nil {
		unlock()
		return jobs.Job{}, err
	}
	return j, nil
}

// nonce returns a random marker for section framing.
func nonce() string { return strings.TrimPrefix(auth.RandomID("x"), "x") }
