package machine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GitInfo is the read-only repo summary shown on the project home screen.
type GitInfo struct {
	IsRepo  bool   `json:"isRepo"`
	Branch  string `json:"branch"`
	Ahead   int    `json:"ahead"`
	Behind  int    `json:"behind"`
	Changed int    `json:"changed"`
	Error   string `json:"error"`
}

const (
	gitTimeout = 5 * time.Second
	gitMarker  = "__NB_GIT__"
	gitNoGit   = "__NB_NOGIT__"
	gitNoRepo  = "__NB_NOREPO__"
)

// gitScript is strictly read-only: GIT_OPTIONAL_LOCKS=0 stops `git status`
// from refreshing (writing) the index.
func gitScript(dir string) string {
	return cdExpr(dir) + ` || exit 3; echo ` + gitMarker + `; ` +
		`command -v git >/dev/null 2>&1 || { echo ` + gitNoGit + `; exit 0; }; ` +
		`export GIT_OPTIONAL_LOCKS=0 LC_ALL=C; ` +
		`[ "$(git rev-parse --is-inside-work-tree 2>/dev/null)" = true ] || { echo ` + gitNoRepo + `; exit 0; }; ` +
		`git status --porcelain -b 2>/dev/null`
}

// GitStatus runs gitScript on the machine (5 s timeout). Failures are
// reported in GitInfo.Error, never as a write or a retry.
func GitStatus(ctx context.Context, h Host, dir string) *GitInfo {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	var out string
	var err error
	switch m := h.(type) {
	case *Local:
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", gitScript(dir))
		cmd.Env = append(os.Environ(), "PATH="+m.pathEnv(), "HOME="+m.home)
		var b []byte
		b, err = cmd.Output()
		out = string(b)
	case *SSH:
		out, err = m.Exec(ctx, loginWrap(gitScript(dir)))
	default:
		return &GitInfo{Error: "machine tidak didukung"}
	}
	i := strings.Index(out, gitMarker)
	switch {
	case i >= 0:
	case errors.Is(err, ErrNotConnected):
		return &GitInfo{Error: err.Error()}
	case ctx.Err() != nil:
		return &GitInfo{Error: "git timeout"}
	default:
		return &GitInfo{Error: "folder tidak bisa dibuka"}
	}
	info := parseGitStatus(out[i+len(gitMarker):])
	if info.IsRepo && ctx.Err() != nil {
		info.Error = "git timeout"
	}
	return info
}

// parseGitStatus parses `git status --porcelain -b`:
//
//	## main...origin/main [ahead 1, behind 2]
//	 M file.go
//	?? new.txt
func parseGitStatus(out string) *GitInfo {
	g := &GitInfo{}
	switch {
	case strings.Contains(out, gitNoGit):
		g.Error = "git tidak ditemukan"
		return g
	case strings.Contains(out, gitNoRepo):
		return g
	}
	g.IsRepo = true
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		if strings.HasPrefix(l, "## ") {
			g.Branch, g.Ahead, g.Behind = parseBranchLine(l[3:])
			continue
		}
		g.Changed++
	}
	return g
}

func parseBranchLine(s string) (branch string, ahead, behind int) {
	for _, p := range []string{"No commits yet on ", "Initial commit on "} {
		if strings.HasPrefix(s, p) {
			return strings.TrimSpace(s[len(p):]), 0, 0
		}
	}
	if strings.HasPrefix(s, "HEAD (no branch)") {
		return "HEAD", 0, 0
	}
	head, track, _ := strings.Cut(s, " [")
	branch, _, _ = strings.Cut(head, "...")
	track = strings.TrimSuffix(track, "]")
	for _, part := range strings.Split(track, ",") {
		f := strings.Fields(part)
		if len(f) != 2 {
			continue
		}
		n, _ := strconv.Atoi(f[1])
		switch f[0] {
		case "ahead":
			ahead = n
		case "behind":
			behind = n
		}
	}
	return strings.TrimSpace(branch), ahead, behind
}
