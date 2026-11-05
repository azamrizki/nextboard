package gitsvc

import (
	"context"
	"errors"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Scan limits (docs/API-studio.md §3.6).
const (
	scanDepth   = 4 // folder levels below the project folder
	maxRepos    = 50
	scanReaders = 4 // status reads at a time: the machine may be a shared host
)

// scanSkip are folders that hold dependencies and build output, not the
// user's own repositories.
var scanSkip = []string{"node_modules", "bower_components", "vendor", "target", "dist", "build",
	".cache", ".gradle", ".venv", "venv", "__pycache__", ".next", ".terraform"}

// RepoInfo is one repository of a project (GET …/git/repos).
type RepoInfo struct {
	Path string `json:"path"` // relative to the project folder; "" = the project's own
	Name string `json:"name"`
	Root string `json:"root,omitempty"`
	Summary
	Error string `json:"error,omitempty"` // why the summary is missing
}

// RepoList is GET …/git/repos.
type RepoList struct {
	Items     []RepoInfo `json:"items"`
	Truncated bool       `json:"truncated"` // stopped at maxRepos or at the timeout
}

// labels names the repositories for messages ("." = the project's own).
func (l *RepoList) labels() []string {
	out := make([]string, 0, len(l.Items))
	for _, it := range l.Items {
		if it.Path == "" {
			out = append(out, ".")
		} else {
			out = append(out, it.Path)
		}
	}
	return out
}

// scanScript prints the repository around the project folder, then every
// repository below it. It is strictly read-only and does not follow
// symlinks. A hit counts only as its own root: a stray .git folder is not a
// repository, git would answer for the one around it. Exit 255 stops xargs.
func scanScript(m string) string {
	var skip []string
	for _, n := range scanSkip {
		skip = append(skip, `-name `+n)
	}
	verify := `n=0; for f do d=${f%/.git}; [ "$d" != . ] || continue; ` +
		`( cd "$d" 2>/dev/null && [ "$(git rev-parse --show-toplevel 2>/dev/null)" = "$(pwd -P)" ] ) || continue; ` +
		`printf '%s\000' "$d"; n=$((n+1)); [ "$n" -le ` + strconv.Itoa(maxRepos) + ` ] || exit 255; done`
	return prelude(false, true) +
		section(m, "top") + `git rev-parse --show-toplevel 2>/dev/null | tr -d '\n'; ` +
		section(m, "repos") + `find . -maxdepth ` + strconv.Itoa(scanDepth+1) + ` \( -type d \( ` + strings.Join(skip, ` -o `) + ` \) -prune \) -o -name .git -prune -print0 2>/dev/null | ` +
		`xargs -0 sh -c ` + machine.Quote(verify) + ` sh; ` +
		section(m, "end") + `:`
}

// scan finds the repositories of the project folder. A scan that hits the
// timeout returns what it found so far.
func (s *Service) scan(ctx context.Context, r Repo) (*RepoList, error) {
	m := nonce()
	res, err := s.exec(ctx, r, scanScript(m), execOpts{timeout: ReadTimeout})
	list := &RepoList{Items: []RepoInfo{}}
	if err != nil {
		var ge *Error
		if !errors.As(err, &ge) || (ge.Code != "timeout" && ge.Code != "idle_timeout") || res == nil {
			return nil, err
		}
		list.Truncated = true
	}
	secs := parseSections(res.Stdout, m)
	if _, ok := secs["end"]; !ok && !list.Truncated {
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	if top := string(secs["top"]); top != "" {
		list.Items = append(list.Items, RepoInfo{Name: path.Base(top), Root: top})
	}
	var found []string
	for _, d := range strings.Split(string(secs["repos"]), "\x00") {
		// A name git commands cannot take stays out of the list.
		if c, err := cleanPath(strings.TrimPrefix(d, "./")); d != "" && err == nil {
			found = append(found, c)
		}
	}
	sort.Strings(found)
	if len(found) > maxRepos {
		found, list.Truncated = found[:maxRepos], true
	}
	for _, d := range found {
		list.Items = append(list.Items, RepoInfo{Path: d, Name: path.Base(d)})
	}
	return list, nil
}

// Repos scans the project folder and reads each repository's summary, a few
// at a time. It never fetches and never publishes.
func (s *Service) Repos(ctx context.Context, r Repo) (*RepoList, error) {
	r.Sub = ""
	list, err := s.scan(ctx, r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*ReadTimeout)
	defer cancel()
	sem := make(chan struct{}, scanReaders)
	var wg sync.WaitGroup
	for i := range list.Items {
		sem <- struct{}{}
		wg.Add(1)
		go func(it *RepoInfo) {
			defer wg.Done()
			defer func() { <-sem }()
			s.readSummary(ctx, r, it)
		}(&list.Items[i])
	}
	wg.Wait()
	return list, nil
}

// readSummary fills in branch, ahead/behind and changed files. The count
// uses the same status mode as the Git panel, so both show one number.
func (s *Service) readSummary(ctx context.Context, r Repo, it *RepoInfo) {
	r.Sub = it.Path
	m := nonce()
	script := prelude(true, true) +
		section(m, "top") + `printf '%s' "$top"; ` +
		section(m, "status") + `git status --porcelain=v2 -b -z --untracked-files=all; ` +
		section(m, "end") + `:`
	res, err := s.exec(ctx, r, script, execOpts{timeout: ReadTimeout})
	if err != nil {
		it.Error = err.Error()
		return
	}
	secs := parseSections(res.Stdout, m)
	if _, ok := secs["end"]; !ok {
		it.Error = failed(res, "git_failed", http.StatusBadGateway).Msg
		return
	}
	st := parseStatus(secs)
	it.Root, it.Summary = st.Root, st.summary()
}

// Pick resolves the repository a command bar step works on: name is a path
// from the scan, "." for the project's own, or a unique folder name. Without
// a name a single repository is the answer; with several the user must say
// which, so a typed "commit" never lands in a repository they did not mean.
func (s *Service) Pick(ctx context.Context, r Repo, name string) (Repo, error) {
	r.Sub = ""
	list, err := s.scan(ctx, r)
	if err != nil {
		return r, err
	}
	refuse := func(code, msg string) (Repo, error) {
		return r, &Error{Status: http.StatusConflict, Code: code, Msg: msg, Paths: list.labels()}
	}
	if name == "" {
		switch len(list.Items) {
		case 0:
			return r, nil // the step reports "not a repository" as before
		case 1:
			r.Sub = list.Items[0].Path
			return r, nil
		}
		return refuse("repo_needed", "project ini punya beberapa repository; sebutkan salah satu")
	}
	if name == "." {
		for _, it := range list.Items {
			if it.Path == "" {
				return r, nil
			}
		}
		return refuse("repo_not_found", "project ini tidak punya repository di foldernya sendiri")
	}
	var hits []RepoInfo
	for _, it := range list.Items {
		if it.Path != "" && it.Path == name {
			r.Sub = it.Path
			return r, nil
		}
		if strings.EqualFold(it.Name, name) {
			hits = append(hits, it)
		}
	}
	switch len(hits) {
	case 0:
		return refuse("repo_not_found", "repository tidak ditemukan di project ini")
	case 1:
		r.Sub = hits[0].Path
		return r, nil
	}
	return refuse("repo_ambiguous", "ada beberapa repository dengan nama ini; sebutkan path-nya")
}
