package gitsvc

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Branch is one local branch.
type Branch struct {
	Name         string     `json:"name"`
	Current      bool       `json:"current"`
	Upstream     string     `json:"upstream"`
	Ahead        int        `json:"ahead"`
	Behind       int        `json:"behind"`
	Gone         bool       `json:"gone,omitempty"`
	LastCommitAt *time.Time `json:"lastCommitAt"`
}

// Branches is GET …/git/branches.
type Branches struct {
	Current string   `json:"current"`
	Items   []Branch `json:"items"`
}

// checkBranchName is a cheap local pre-check; git check-ref-format
// --branch decides in the script.
func checkBranchName(name string) error {
	bad := newErr(http.StatusBadRequest, "bad_ref", "invalid branch name: %q", name)
	if name == "" || len(name) > 200 || strings.HasPrefix(name, "-") {
		return bad
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune("~^:?*[\\", r) {
			return bad
		}
	}
	return nil
}

// Branches lists local branches (≤ 200, most recent first).
func (s *Service) Branches(ctx context.Context, r Repo) (*Branches, error) {
	script := prelude(true, true) +
		`git symbolic-ref -q --short HEAD; printf '\000'; ` +
		`git for-each-ref --count=` + strconv.Itoa(MaxBranches) + ` --sort=-committerdate ` +
		`--format='%(refname:short)%00%(HEAD)%00%(upstream:short)%00%(upstream:track)%00%(committerdate:iso-strict)%00' refs/heads/`
	res, err := s.exec(ctx, r, script, execOpts{timeout: ReadTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	cur, rest, _ := strings.Cut(string(res.Stdout), "\x00")
	out := &Branches{Current: strings.TrimSpace(cur), Items: []Branch{}}
	f := strings.Split(rest, "\x00")
	for i := 0; i+4 < len(f); i += 5 {
		name := strings.TrimSpace(f[i])
		if name == "" {
			continue
		}
		b := Branch{Name: name, Current: strings.TrimSpace(f[i+1]) == "*", Upstream: f[i+2]}
		track := strings.Trim(f[i+3], "[]")
		if track == "gone" {
			b.Gone = true
		}
		for _, part := range strings.Split(track, ",") {
			w := strings.Fields(part)
			if len(w) == 2 {
				n, _ := strconv.Atoi(w[1])
				switch w[0] {
				case "ahead":
					b.Ahead = n
				case "behind":
					b.Behind = n
				}
			}
		}
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(f[i+4])); err == nil {
			t = t.UTC()
			b.LastCommitAt = &t
		}
		out.Items = append(out.Items, b)
	}
	return out, nil
}

// Exit codes of the branch scripts.
const (
	exitBadRef   = 7
	exitExists   = 8
	exitNoBranch = 10
	exitBadFrom  = 11
)

// CreateBranch creates name (from `from`, default HEAD) and switches to it
// unless sw is false.
func (s *Service) CreateBranch(ctx context.Context, r Repo, name, from string, sw bool) (*Status, error) {
	if err := checkBranchName(name); err != nil {
		return nil, err
	}
	if from != "" && (strings.HasPrefix(from, "-") || len(from) > 256 || strings.ContainsAny(from, "\x00\n\r")) {
		return nil, newErr(http.StatusBadRequest, "bad_ref", "invalid branch start point")
	}
	unlock, err := s.lock(r, "branch")
	if err != nil {
		return nil, err
	}
	defer unlock()
	qn := machine.Quote(name)
	script := prelude(true, false) +
		`n=$(git check-ref-format --branch ` + qn + ` 2>/dev/null) && [ "$n" = ` + qn + ` ] || exit 7; ` +
		`git show-ref --verify --quiet refs/heads/` + qn + ` && exit 8; `
	start := ""
	if from != "" {
		qf := machine.Quote(from)
		script += `git rev-parse -q --verify ` + qf + `'^{commit}' >/dev/null || exit 11; `
		start = " " + qf
	}
	if sw {
		script += `git switch -q -c ` + qn + start + ` 2>/dev/null || git checkout -q -b ` + qn + start
	} else {
		script += `git branch ` + qn + start
	}
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 0:
	case exitBadRef:
		return nil, newErr(http.StatusBadRequest, "bad_ref", "invalid branch name: %q", name)
	case exitExists:
		return nil, newErr(http.StatusConflict, "exists", "branch %s already exists", name)
	case exitBadFrom:
		return nil, newErr(http.StatusBadRequest, "bad_ref", "start point %q not found", from)
	default:
		return nil, failed(res, "git_refused", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}

// Switch checks out an existing local branch. No confirmation: git itself
// refuses a switch that would overwrite work (409 git_refused).
func (s *Service) Switch(ctx context.Context, r Repo, name string) (*Status, error) {
	if err := checkBranchName(name); err != nil {
		return nil, err
	}
	unlock, err := s.lock(r, "switch")
	if err != nil {
		return nil, err
	}
	defer unlock()
	qn := machine.Quote(name)
	script := prelude(true, false) +
		`git show-ref --verify --quiet refs/heads/` + qn + ` || exit 10; ` +
		`if git switch -h >/dev/null 2>&1; then git switch -q --no-guess ` + qn + `; else git checkout -q ` + qn + ` --; fi`
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 0:
	case exitNoBranch:
		return nil, newErr(http.StatusNotFound, "not_found", "branch %s does not exist", name)
	default:
		return nil, failed(res, "git_refused", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}

// DeleteBranch runs `git branch -d` (refuses unmerged branches: 409
// not_merged).
func (s *Service) DeleteBranch(ctx context.Context, r Repo, name string) error {
	if err := checkBranchName(name); err != nil {
		return err
	}
	unlock, err := s.lock(r, "branch")
	if err != nil {
		return err
	}
	defer unlock()
	qn := machine.Quote(name)
	script := prelude(true, false) +
		`git show-ref --verify --quiet refs/heads/` + qn + ` || exit 10; git branch -d ` + qn
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return err
	}
	switch {
	case res.ExitCode == 0:
	case res.ExitCode == exitNoBranch:
		return newErr(http.StatusNotFound, "not_found", "branch %s does not exist", name)
	case strings.Contains(string(res.Stderr), "not fully merged"):
		e := failed(res, "not_merged", http.StatusConflict)
		return e
	default:
		return failed(res, "git_refused", http.StatusConflict)
	}
	unlock()
	s.refresh(r)
	return nil
}
