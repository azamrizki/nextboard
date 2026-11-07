package gitsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
)

// GitHub is GET /api/projects/{id}/github (docs/API-studio.md §3.3). The
// token stays in gh's own keyring on the machine; the hub never reads it.
type GitHub struct {
	GhInstalled   bool   `json:"ghInstalled"`
	Authenticated bool   `json:"authenticated"`
	User          string `json:"user"`
	RemoteURL     string `json:"remoteUrl"`
	RepoURL       string `json:"repoUrl"`
	InstallHint   string `json:"installHint"`
}

const (
	markNoGh  = "__NB_NOGH__"
	markHint  = "__NB_HINT__"
	markAuthd = "__NB_AUTH__"
)

var ghUserRe = []*regexp.Regexp{
	regexp.MustCompile(`Logged in to github\.com account ([A-Za-z0-9-]+)`),
	regexp.MustCompile(`Logged in to github\.com as ([A-Za-z0-9-]+)`),
}

// ghScript reports gh presence, an install hint per OS and `gh auth
// status` (read-only).
func (s *Service) ghScript() string {
	gh := machine.Quote(s.gh())
	return gitEnv +
		`if ! command -v ` + gh + ` >/dev/null 2>&1; then echo ` + markNoGh + `; ` +
		`if command -v brew >/dev/null 2>&1; then h='brew install gh'; ` +
		`elif command -v apt-get >/dev/null 2>&1; then h='sudo apt install gh'; ` +
		`elif command -v dnf >/dev/null 2>&1; then h='sudo dnf install gh'; ` +
		`elif command -v pacman >/dev/null 2>&1; then h='sudo pacman -S github-cli'; ` +
		`else h='https://cli.github.com'; fi; echo "` + markHint + `$h"; exit 0; fi; ` +
		gh + ` auth status --hostname github.com 2>&1 && echo ` + markAuthd + `; exit 0`
}

func (s *Service) gh() string {
	if s.Gh == "" {
		return "gh"
	}
	return s.Gh
}

// GitHubStatus probes gh on the project's machine.
func (s *Service) GitHubStatus(ctx context.Context, r Repo) (*GitHub, error) {
	res, err := s.exec(ctx, r, s.ghScript(), execOpts{timeout: ReadTimeout})
	if err != nil {
		if ge, ok := err.(*Error); ok && ge.Code == "git_missing" {
			return nil, err
		}
		return nil, err
	}
	out := string(res.Stdout)
	g := &GitHub{}
	switch {
	case strings.Contains(out, markNoGh):
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, markHint) {
				g.InstallHint = strings.TrimSpace(strings.TrimPrefix(l, markHint))
			}
		}
	default:
		g.GhInstalled = true
		g.Authenticated = strings.Contains(out, markAuthd)
		for _, re := range ghUserRe {
			if m := re.FindStringSubmatch(out); m != nil {
				g.User = m[1]
				break
			}
		}
	}
	if st, err := s.Status(ctx, r); err == nil {
		g.RemoteURL = st.RemoteURL
		if strings.HasPrefix(st.WebURL, "https://github.com/") {
			g.RepoURL = st.WebURL
		}
	}
	return g, nil
}

// WatchLogin polls `gh auth status` every 5 s while alive() holds (the
// `gh auth login` console lives) and publishes {"type":"github"} on every
// change and once at the end.
func (s *Service) WatchLogin(r Repo, alive func() bool) {
	s.mu.Lock()
	if s.ghWatch[r.ProjectID] {
		s.mu.Unlock()
		return
	}
	s.ghWatch[r.ProjectID] = true
	every := s.LoginPoll
	s.mu.Unlock()
	if every <= 0 {
		every = 5 * time.Second
	}
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.ghWatch, r.ProjectID)
			s.mu.Unlock()
		}()
		var last *GitHub
		deadline := time.Now().Add(30 * time.Minute)
		for {
			time.Sleep(every)
			done := !alive() || time.Now().After(deadline)
			ctx, cancel := context.WithTimeout(context.Background(), ReadTimeout+2*time.Second)
			g, err := s.GitHubStatus(ctx, r)
			cancel()
			if err == nil && (last == nil || *last != *g || done) {
				last = g
				if s.Publish != nil {
					s.Publish(map[string]any{"type": "github", "projectId": r.ProjectID, "github": g})
				}
			}
			if done {
				return
			}
		}
	}()
}

// PublishRequest is POST /api/projects/{id}/github/publish.
type PublishRequest struct {
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
	CommitAll  bool   `json:"commitAll"`
	Message    string `json:"message"`
}

var repoNameRe = regexp.MustCompile(`^(?:[A-Za-z0-9-]{1,39}/)?[A-Za-z0-9._-]{1,100}$`)

// PublishResult is the result of a publish job.
type PublishResult struct {
	RepoURL   string  `json:"repoUrl"`
	Committed *Commit `json:"committed,omitempty"`
	Output    string  `json:"output"`
}

// PublishGitHub starts a publish job: commit everything (when asked), then
// `gh repo create <name> --private|--public --source . --remote origin
// --push`.
func (s *Service) PublishGitHub(ctx context.Context, r Repo, req PublishRequest, onFinish func(jobs.Job)) (jobs.Job, error) {
	req.Name = strings.TrimSpace(req.Name)
	if !repoNameRe.MatchString(req.Name) || strings.HasSuffix(req.Name, ".git") || strings.HasPrefix(path0(req.Name), ".") {
		return jobs.Job{}, newErr(http.StatusBadRequest, "bad_name", "invalid repository name")
	}
	switch req.Visibility {
	case "":
		req.Visibility = "private"
	case "private", "public":
	default:
		return jobs.Job{}, newErr(http.StatusBadRequest, "bad_visibility", `visibility must be "private" or "public"`)
	}
	st, err := s.Status(ctx, r)
	if err != nil {
		return jobs.Job{}, err
	}
	if !st.IsRepo {
		return jobs.Job{}, errNotRepo()
	}
	if st.HasRemote {
		return jobs.Job{}, newErr(http.StatusConflict, "remote_exists", "the repository already has a remote (%s)", st.RemoteName)
	}
	if st.Detached {
		return jobs.Job{}, newErr(http.StatusConflict, "detached", "HEAD is not on a branch")
	}
	if req.CommitAll && st.Changed > 0 {
		if st.Conflicts > 0 {
			return jobs.Job{}, newErr(http.StatusConflict, "conflicts", "resolve the merge conflicts first")
		}
		if len(st.LargeFiles) > 0 {
			var ps []string
			for _, l := range st.LargeFiles {
				ps = append(ps, l.Path)
			}
			return jobs.Job{}, &Error{Status: http.StatusConflict, Code: "large_files", Msg: "files over 100 MB cannot be pushed to GitHub", Paths: ps}
		}
	}
	if !req.CommitAll && !st.HasCommits {
		return jobs.Job{}, newErr(http.StatusConflict, "no_commits", "there are no commits to push yet")
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		msg = SuggestSubject(st.Files)
		if !st.HasCommits {
			msg = "Initial commit"
		}
	}
	commit := req.CommitAll && st.Changed > 0
	return s.submit(r, jobs.KindPublish, "publish", func(ctx context.Context, rep *jobs.Reporter) (json.RawMessage, error) {
		out := PublishResult{}
		if commit {
			rep.Progress("committing", "Committing…")
			c, err := s.commitLocked(ctx, r, CommitRequest{Message: msg, All: true}, msg, nil)
			if err == nil {
				out.Committed = &c.Commit
			}
			if err != nil {
				b, _ := json.Marshal(out)
				return b, err
			}
		}
		rep.Progress("pushing", "Creating repository…")
		script := gitEnv + `command -v ` + machine.Quote(s.gh()) + ` >/dev/null 2>&1 || { echo ` + markNoGh + `; exit 3; }; ` +
			prelude(true, false) +
			`exec ` + machine.Join(s.gh(), "repo", "create", req.Name, "--"+req.Visibility, "--source", ".", "--remote", "origin", "--push") + ` 2>&1`
		pw, err := s.netRun(ctx, r, rep, "pushing", script)
		out.Output = lastLines(pw.output(), 20)
		if m := ghRepoURL.FindString(pw.output()); m != "" {
			out.RepoURL = m
		}
		if err != nil {
			if strings.Contains(pw.output(), markNoGh) {
				err = &Error{Status: http.StatusConflict, Code: "gh_missing", Msg: "gh is not installed on this machine"}
			} else if ge, ok := err.(*Error); ok && (strings.Contains(strings.ToLower(pw.output()), "gh auth login") || strings.Contains(strings.ToLower(pw.output()), "not logged")) {
				ge.Code = "gh_not_logged_in"
			}
		}
		b, _ := json.Marshal(out)
		return b, err
	}, onFinish)
}

var ghRepoURL = regexp.MustCompile(`https://github\.com/[A-Za-z0-9._/-]+`)

func path0(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}
