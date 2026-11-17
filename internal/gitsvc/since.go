package gitsvc

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Since is what happened in a repository after a time: what a chat that resumes is told
// (docs/API-agents.md §29).
type Since struct {
	// Commits are the newest first, at most the limit asked for; Total counts them all.
	Commits []Commit `json:"commits"`
	Total   int      `json:"total"`
	// Files is the number of files those commits changed.
	Files int `json:"files"`
}

const (
	sinceCount = "\x00@@COUNT@@\x00"
	sinceFiles = "\x00@@FILES@@\x00"
)

func sinceScript(since time.Time, limit int) string {
	at := machine.Quote(since.UTC().Format(time.RFC3339))
	return prelude(true, true) +
		`git rev-parse -q --verify HEAD >/dev/null 2>&1 || exit 0; ` +
		`git log --since=` + at + ` -n ` + strconv.Itoa(limit) + ` --no-color --format='%H%x00%h%x00%s%x00%an%x00%cI%x00' HEAD --; ` +
		`printf '\000@@COUNT@@\000'; git rev-list --count --since=` + at + ` HEAD --; ` +
		`printf '\000@@FILES@@\000'; git log --since=` + at + ` -n 1000 --no-color --format= --name-only HEAD -- | sed '/^$/d' | sort -u | wc -l`
}

// CommitsSince lists the commits of HEAD made after since (committer time), up to limit (≤ 100)
// of them, with their count and the number of files they changed. A repository without commits
// has none; a folder that is no repository is errNotRepo.
func (s *Service) CommitsSince(ctx context.Context, r Repo, since time.Time, limit int) (*Since, error) {
	if limit <= 0 || limit > MaxLog {
		limit = MaxLog
	}
	res, err := s.exec(ctx, r, sinceScript(since, limit), execOpts{timeout: ReadTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	return parseSince(string(res.Stdout)), nil
}

func parseSince(out string) *Since {
	logPart, rest, _ := strings.Cut(out, sinceCount)
	countPart, filesPart, _ := strings.Cut(rest, sinceFiles)
	res := &Since{Commits: parseLog(logPart + logMark)} // Pushed is not asked here
	res.Total, _ = strconv.Atoi(strings.TrimSpace(countPart))
	res.Files, _ = strconv.Atoi(strings.TrimSpace(filesPart))
	res.Total = max(res.Total, len(res.Commits))
	return res
}
