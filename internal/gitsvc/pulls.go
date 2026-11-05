package gitsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nextboard/internal/machine"
)

// Pull requests through the gh CLI of the project's machine (docs/API-studio.md §3.3): its login
// stays in gh's own keyring, as for publishing.

const (
	ghReadTimeout  = 45 * time.Second
	ghWriteTimeout = 3 * time.Minute
	maxPRDiff      = 4 << 20
	maxPRText      = 65536
)

// PullSummary is one row of the list.
type PullSummary struct {
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	URL            string     `json:"url"`
	Author         PullAuthor `json:"author"`
	HeadRefName    string     `json:"headRefName"`
	BaseRefName    string     `json:"baseRefName"`
	IsDraft        bool       `json:"isDraft"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	ReviewDecision string     `json:"reviewDecision"`
}

// PullAuthor is who wrote a pull request or a comment.
type PullAuthor struct {
	Login string `json:"login"`
}

// PullComment is a comment or a review.
type PullComment struct {
	Author    PullAuthor `json:"author"`
	Body      string     `json:"body"`
	State     string     `json:"state,omitempty"`
	CreatedAt time.Time  `json:"createdAt,omitempty"`
	// SubmittedAt is the date gh gives a review.
	SubmittedAt time.Time `json:"submittedAt,omitempty"`
}

// PullCheck is one check of the head commit. gh fills Name and Conclusion for a check run, and
// Context and State for a commit status.
type PullCheck struct {
	Name       string `json:"name,omitempty"`
	Context    string `json:"context,omitempty"`
	Status     string `json:"status,omitempty"`
	State      string `json:"state,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
}

// PullFile is one changed file.
type PullFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// PullDetail is one pull request.
type PullDetail struct {
	PullSummary
	Body              string        `json:"body"`
	State             string        `json:"state"`
	Additions         int           `json:"additions"`
	Deletions         int           `json:"deletions"`
	ChangedFiles      int           `json:"changedFiles"`
	Mergeable         string        `json:"mergeable"`
	CreatedAt         time.Time     `json:"createdAt"`
	StatusCheckRollup []PullCheck   `json:"statusCheckRollup"`
	Files             []PullFile    `json:"files"`
	Comments          []PullComment `json:"comments"`
	Reviews           []PullComment `json:"reviews"`
}

// gh runs the gh CLI in the repository.
func (s *Service) ghRun(ctx context.Context, r Repo, timeout time.Duration, maxOutput int64, args ...string) ([]byte, error) {
	script := gitEnv + `command -v ` + machine.Quote(s.gh()) + ` >/dev/null 2>&1 || { echo ` + markNoGh + ` >&2; exit 127; }; ` +
		`exec ` + machine.Join(append([]string{s.gh()}, args...)...)
	res, err := s.exec(ctx, r, script, execOpts{timeout: timeout, maxOutput: maxOutput})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		text := strings.ToLower(string(res.Stderr) + string(res.Stdout))
		switch {
		case strings.Contains(text, strings.ToLower(markNoGh)):
			return nil, newErr(http.StatusConflict, "gh_missing", "gh tidak terpasang di machine ini")
		case strings.Contains(text, "gh auth login"), strings.Contains(text, "not logged"):
			return nil, newErr(http.StatusConflict, "gh_auth", "gh belum login di machine ini")
		case strings.Contains(text, "no git remotes"), strings.Contains(text, "none of the git remotes"), strings.Contains(text, "could not determine"):
			return nil, newErr(http.StatusConflict, "no_github_remote", "repository ini tidak punya remote GitHub")
		}
		return nil, failed(res, "gh_failed", http.StatusBadGateway)
	}
	return res.Stdout, nil
}

// Pulls lists the open pull requests.
func (s *Service) Pulls(ctx context.Context, r Repo) ([]PullSummary, error) {
	out, err := s.ghRun(ctx, r, ghReadTimeout, 0, "pr", "list", "--state", "open", "--limit", "50", "--json", "number,title,url,author,headRefName,baseRefName,isDraft,updatedAt,reviewDecision")
	if err != nil {
		return nil, err
	}
	items := []PullSummary{}
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, newErr(http.StatusBadGateway, "gh_failed", "keluaran gh tidak terbaca")
	}
	return items, nil
}

func prNumber(n int) (string, error) {
	if n <= 0 {
		return "", newErr(http.StatusBadRequest, "bad_number", "nomor pull request tidak valid")
	}
	return strconv.Itoa(n), nil
}

// PullRequest describes one pull request.
func (s *Service) PullRequest(ctx context.Context, r Repo, number int) (*PullDetail, error) {
	n, err := prNumber(number)
	if err != nil {
		return nil, err
	}
	out, err := s.ghRun(ctx, r, ghReadTimeout, 0, "pr", "view", n, "--json",
		"number,title,url,author,headRefName,baseRefName,isDraft,updatedAt,reviewDecision,body,state,additions,deletions,changedFiles,mergeable,createdAt,statusCheckRollup,files,comments,reviews")
	if err != nil {
		return nil, err
	}
	d := &PullDetail{}
	if err := json.Unmarshal(out, d); err != nil {
		return nil, newErr(http.StatusBadGateway, "gh_failed", "keluaran gh tidak terbaca")
	}
	return d, nil
}

// PullDiff returns the unified diff of a pull request.
func (s *Service) PullDiff(ctx context.Context, r Repo, number int) (string, error) {
	n, err := prNumber(number)
	if err != nil {
		return "", err
	}
	out, err := s.ghRun(ctx, r, ghReadTimeout, maxPRDiff, "pr", "diff", n, "--color", "never")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// PullCreate is POST …/git/pulls.
type PullCreate struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Base  string `json:"base"`
	Draft bool   `json:"draft"`
}

// CreatePull pushes the current branch and opens a pull request for it. It returns its address.
func (s *Service) CreatePull(ctx context.Context, r Repo, req PullCreate) (string, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" || len(title) > 1000 || len(req.Body) > maxPRText || strings.ContainsRune(title+req.Body, 0) {
		return "", newErr(http.StatusBadRequest, "bad_message", "judul atau isi pull request tidak valid")
	}
	unlock, err := s.lock(r, "pull-request")
	if err != nil {
		return "", err
	}
	defer unlock()
	res, err := s.exec(ctx, r, prelude(true, false)+`git push -q -u origin HEAD`, execOpts{timeout: NetTimeout, idle: NetIdle})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", failed(res, "push_failed", http.StatusConflict)
	}
	args := []string{"pr", "create", "--title", title, "--body", req.Body}
	if req.Base != "" {
		if err := checkBranchName(req.Base); err != nil {
			return "", err
		}
		args = append(args, "--base", req.Base)
	}
	if req.Draft {
		args = append(args, "--draft")
	}
	out, err := s.ghRun(ctx, r, ghWriteTimeout, 0, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CheckoutPull brings the branch of a pull request here.
func (s *Service) CheckoutPull(ctx context.Context, r Repo, number int) (*Status, error) {
	n, err := prNumber(number)
	if err != nil {
		return nil, err
	}
	unlock, err := s.lock(r, "pull-request")
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := s.ghRun(ctx, r, ghWriteTimeout, 0, "pr", "checkout", n); err != nil {
		return nil, err
	}
	unlock()
	return s.refresh(r), nil
}

// ReviewPull approves, asks for changes or comments.
func (s *Service) ReviewPull(ctx context.Context, r Repo, number int, event, body string) error {
	n, err := prNumber(number)
	if err != nil {
		return err
	}
	if len(body) > maxPRText || strings.ContainsRune(body, 0) {
		return newErr(http.StatusBadRequest, "bad_message", "isi review tidak valid")
	}
	if (event == "comment" || event == "request-changes") && strings.TrimSpace(body) == "" {
		return newErr(http.StatusBadRequest, "bad_message", "tulis dulu isinya")
	}
	if event != "approve" && event != "comment" && event != "request-changes" {
		return newErr(http.StatusBadRequest, "bad_action", "jenis review tidak valid")
	}
	args := []string{"pr", "review", n, "--" + event}
	if body != "" {
		args = append(args, "--body", body)
	}
	_, err = s.ghRun(ctx, r, ghWriteTimeout, 0, args...)
	return err
}

// MergePull merges a pull request by merge, squash or rebase.
func (s *Service) MergePull(ctx context.Context, r Repo, number int, method string, deleteBranch bool) (*Status, error) {
	n, err := prNumber(number)
	if err != nil {
		return nil, err
	}
	if method != "merge" && method != "squash" && method != "rebase" {
		return nil, newErr(http.StatusBadRequest, "bad_action", "cara merge tidak valid")
	}
	unlock, err := s.lock(r, "pull-request")
	if err != nil {
		return nil, err
	}
	defer unlock()
	args := []string{"pr", "merge", n, "--" + method}
	if deleteBranch {
		args = append(args, "--delete-branch")
	}
	if _, err := s.ghRun(ctx, r, ghWriteTimeout, 0, args...); err != nil {
		return nil, err
	}
	unlock()
	return s.refresh(r), nil
}
