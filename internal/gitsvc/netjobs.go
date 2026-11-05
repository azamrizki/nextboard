package gitsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
)

// NetResult is the result of a push/pull/fetch job.
type NetResult struct {
	Action string `json:"action"`
	Output string `json:"output"` // tail of git's progress/output
	Branch string `json:"branch,omitempty"`
	Remote string `json:"remote,omitempty"`
}

// progressRe matches git's --progress lines ("Writing objects:  45% (9/20)").
var progressRe = regexp.MustCompile(`([A-Za-z][A-Za-z ]+):\s+(\d{1,3})%`)

// progressWriter turns streamed git output into job progress (throttled by
// the job manager) and keeps the last 4 KB.
type progressWriter struct {
	mu    sync.Mutex
	rep   *jobs.Reporter
	phase string
	tail  []byte
	part  string
}

func (p *progressWriter) write(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tail = append(p.tail, b...)
	if len(p.tail) > 4096 {
		p.tail = p.tail[len(p.tail)-4096:]
	}
	p.part += string(b)
	// git redraws a progress line with \r; the last complete piece wins.
	for {
		i := strings.IndexAny(p.part, "\r\n")
		if i < 0 {
			break
		}
		line := strings.TrimSpace(p.part[:i])
		p.part = p.part[i+1:]
		if line == "" || p.rep == nil {
			continue
		}
		text := line
		if m := progressRe.FindStringSubmatch(line); m != nil {
			text = strings.TrimSpace(m[1]) + " " + m[2] + "%"
		}
		if r := []rune(text); len(r) > 120 {
			text = string(r[:120])
		}
		p.rep.Progress(p.phase, text)
	}
	if len(p.part) > 4096 {
		p.part = p.part[len(p.part)-4096:]
	}
}

func (p *progressWriter) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(strings.ToValidUTF8(string(p.tail), ""))
}

// netRun runs a network script (stderr merged into stdout) with the
// network timeout and idle watchdog, classifying failures.
func (s *Service) netRun(ctx context.Context, r Repo, rep *jobs.Reporter, phase, script string) (*progressWriter, error) {
	pw := &progressWriter{rep: rep, phase: phase}
	res, err := s.exec(ctx, r, script, execOpts{timeout: NetTimeout, idle: NetIdle, onStdout: pw.write})
	if err != nil {
		return pw, err
	}
	if res.ExitCode != 0 {
		return pw, classifyNet(pw.output(), res.ExitCode)
	}
	return pw, nil
}

const (
	markNoBranch   = "__NB_NOBRANCH__"
	markNoRemote   = "__NB_NOREMOTE__"
	markNoUpstream = "__NB_NOUPSTREAM__"
)

// classifyNet maps git network failures to job codes.
func classifyNet(out string, exit int) *Error {
	lower := strings.ToLower(out)
	msg := lastLines(out, 8)
	code := "git_failed"
	switch {
	case strings.Contains(out, markNoBranch):
		code, msg = "detached", "HEAD tidak berada di branch (detached); buat branch dulu"
	case strings.Contains(out, markNoRemote):
		code, msg = "no_remote", "repository belum punya remote"
	case strings.Contains(out, markNoUpstream):
		code, msg = "no_upstream", "branch ini belum punya upstream; push dulu (Publish branch)"
	case strings.Contains(lower, "not possible to fast-forward"), strings.Contains(lower, "diverging branches"),
		strings.Contains(lower, "have diverged"):
		code, msg = "diverged", "Branches diverged — merge in a console"
	case strings.Contains(lower, "gh001") || strings.Contains(lower, "exceeds github's file size limit") || strings.Contains(lower, "large files detected"):
		code = "large_files"
	case strings.Contains(lower, "[rejected]") || strings.Contains(lower, "non-fast-forward") || strings.Contains(lower, "fetch first"):
		code = "rejected"
	case strings.Contains(lower, "terminal prompts disabled") || strings.Contains(lower, "could not read username") ||
		strings.Contains(lower, "could not read password") || strings.Contains(lower, "authentication failed") ||
		strings.Contains(lower, "permission denied (publickey") || strings.Contains(lower, "permission denied (") ||
		strings.Contains(lower, "host key verification failed") || strings.Contains(lower, "invalid username or password"):
		code = "auth_failed"
	case strings.Contains(lower, "would be overwritten"):
		code = "git_refused"
	case strings.Contains(lower, "could not resolve host") || strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "could not read from remote repository") || strings.Contains(lower, "unable to access"):
		code = "network"
	}
	if msg == "" {
		msg = "git keluar dengan kode " + strconv.Itoa(exit)
	}
	return &Error{Status: http.StatusBadGateway, Code: code, Msg: msg}
}

func lastLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "hint:") || strings.HasPrefix(l, "__NB_") || progressRe.MatchString(l) {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}

// pushScript pushes HEAD's branch; without an upstream (or setUpstream) it
// publishes the branch with -u to origin (or the first remote).
func pushScript(setUpstream bool) string {
	su := "0"
	if setUpstream {
		su = "1"
	}
	return prelude(true, false) +
		`b=$(git symbolic-ref -q --short HEAD) || { echo ` + markNoBranch + `; exit 3; }; ` +
		`[ -n "$(git remote)" ] || { echo ` + markNoRemote + `; exit 3; }; ` +
		`if [ ` + su + ` = 1 ] || ! git rev-parse -q --verify '@{u}' >/dev/null 2>&1; then ` +
		`r=$(git config --get "branch.$b.remote" 2>/dev/null); [ -n "$r" ] || r=origin; ` +
		`git remote get-url "$r" >/dev/null 2>&1 || r=$(git remote | head -n 1); ` +
		`printf 'Publishing %s to %s\n' "$b" "$r"; exec git push --progress -u "$r" "refs/heads/$b:refs/heads/$b" 2>&1; fi; ` +
		`exec git push --progress 2>&1`
}

// Push starts a push job (202 Job). Network jobs hold the project lock.
func (s *Service) Push(r Repo, setUpstream bool, onFinish func(jobs.Job)) (jobs.Job, error) {
	return s.submit(r, jobs.KindGit, "push", func(ctx context.Context, rep *jobs.Reporter) (json.RawMessage, error) {
		rep.Progress("pushing", "Pushing…")
		pw, err := s.netRun(ctx, r, rep, "pushing", pushScript(setUpstream))
		return netResult("push", pw), err
	}, onFinish)
}

// Pull starts a fast-forward-only pull job; diverged branches fail with
// code "diverged".
func (s *Service) Pull(r Repo, onFinish func(jobs.Job)) (jobs.Job, error) {
	script := prelude(true, false) +
		`git symbolic-ref -q HEAD >/dev/null || { echo ` + markNoBranch + `; exit 3; }; ` +
		`git rev-parse -q --verify '@{u}' >/dev/null 2>&1 || { echo ` + markNoUpstream + `; exit 3; }; ` +
		`exec git pull --no-rebase --ff-only --progress 2>&1`
	return s.submit(r, jobs.KindGit, "pull", func(ctx context.Context, rep *jobs.Reporter) (json.RawMessage, error) {
		rep.Progress("pulling", "Pulling…")
		pw, err := s.netRun(ctx, r, rep, "pulling", script)
		return netResult("pull", pw), err
	}, onFinish)
}

// Fetch starts a fetch job (only on a user's click).
func (s *Service) Fetch(r Repo, onFinish func(jobs.Job)) (jobs.Job, error) {
	script := prelude(true, false) +
		`[ -n "$(git remote)" ] || { echo ` + markNoRemote + `; exit 3; }; exec git fetch --progress 2>&1`
	return s.submit(r, jobs.KindGit, "fetch", func(ctx context.Context, rep *jobs.Reporter) (json.RawMessage, error) {
		rep.Progress("fetching", "Fetching…")
		pw, err := s.netRun(ctx, r, rep, "fetching", script)
		return netResult("fetch", pw), err
	}, onFinish)
}

func netResult(action string, pw *progressWriter) json.RawMessage {
	if pw == nil {
		return nil
	}
	b, _ := json.Marshal(NetResult{Action: action, Output: lastLines(pw.output(), 20)})
	return b
}

var _ = machine.Quote
