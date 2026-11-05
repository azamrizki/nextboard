package gitsvc

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// File is one changed path of GitStatus.
type File struct {
	Path     string `json:"path"`
	OldPath  string `json:"oldPath,omitempty"`
	Index    string `json:"index"`
	Worktree string `json:"worktree"`
	Status   string `json:"status"` // modified|added|deleted|renamed|untracked|conflict|typechange
	Added    int    `json:"added"`
	Deleted  int    `json:"deleted"`
	Binary   bool   `json:"binary"`
}

// LargeFile is a changed file over LargeBytes.
type LargeFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	Tracked   bool   `json:"tracked"`
}

// Status is GitStatus (docs/API-studio.md §3.1). Root and HasCommits are
// NextBoard additions.
type Status struct {
	IsRepo     bool       `json:"isRepo"`
	Root       string     `json:"root,omitempty"`
	Branch     string     `json:"branch"`
	Detached   bool       `json:"detached"`
	HasCommits bool       `json:"hasCommits"`
	Upstream   string     `json:"upstream"`
	HasRemote  bool       `json:"hasRemote"`
	RemoteName string     `json:"remoteName,omitempty"`
	RemoteURL  string     `json:"remoteUrl"`
	WebURL     string     `json:"webUrl"`
	Ahead      int        `json:"ahead"`
	Behind     int        `json:"behind"`
	FetchedAt  *time.Time `json:"fetchedAt"`
	Merging    bool       `json:"merging"`
	// Operation is what is under way and can be aborted: merge, cherry-pick or revert.
	Operation  string      `json:"operation,omitempty"`
	Conflicts  int         `json:"conflicts"`
	Files      []File      `json:"files"`
	Changed    int         `json:"changed"` // all changed paths (Files is capped)
	Truncated  bool        `json:"truncated"`
	LargeFiles []LargeFile `json:"largeFiles"`
	// NestedRepos are repositories inside this one's folders that it does
	// not track. They are not changes of this repository.
	NestedRepos []string `json:"nestedRepos,omitempty"`
}

// file returns the entry of a path (or nil).
func (st *Status) file(p string) *File {
	for i := range st.Files {
		if st.Files[i].Path == p {
			return &st.Files[i]
		}
	}
	return nil
}

// statusScript prints framed sections (see parseSections). It is strictly
// read-only: GIT_OPTIONAL_LOCKS=0 keeps `git status` from refreshing the
// index.
func statusScript(m string) string {
	sec := func(name string) string { return section(m, name) }
	// Untracked line counts: first 50 files ≤ 1 MiB.
	untracked := `n=0; for f do n=$((n+1)); [ "$n" -le 50 ] || exit 0; [ -f "$f" ] || continue; ` +
		`s=$(wc -c < "$f" 2>/dev/null | tr -d ' '); r=$(printf 'x\tx'); ` +
		`if [ -n "$s" ] && [ "$s" -le 1048576 ]; then r=$(git diff --no-index --numstat -- /dev/null "$f" 2>/dev/null | head -n 1 | cut -f1,2); fi; ` +
		`printf '%s\t%s\t%s\000' "$s" "$r" "$f"; done`
	return prelude(true, true) +
		sec("top") + `printf '%s' "$top"; ` +
		sec("status") + `git status --porcelain=v2 -b -z --untracked-files=all; ` +
		`base=HEAD; git rev-parse -q --verify HEAD >/dev/null 2>&1 || base=$(git hash-object -t tree /dev/null); ` +
		sec("numstat") + `git diff --numstat -z --no-ext-diff --no-renames "$base" -- 2>/dev/null; ` +
		sec("remotes") + `git config --get-regexp '^remote\..*\.url$' 2>/dev/null; ` +
		sec("fetched") + `f=$(git rev-parse --git-path FETCH_HEAD); if [ -f "$f" ]; then date -r "$f" +%s 2>/dev/null || stat -c %Y "$f" 2>/dev/null || stat -f %m "$f" 2>/dev/null; fi; ` +
		sec("merging") + `[ -f "$(git rev-parse --git-path MERGE_HEAD)" ] && echo 1; ` +
		sec("operation") + `if [ -f "$(git rev-parse --git-path MERGE_HEAD)" ]; then echo merge; elif [ -f "$(git rev-parse --git-path CHERRY_PICK_HEAD)" ]; then echo cherry-pick; elif [ -f "$(git rev-parse --git-path REVERT_HEAD)" ]; then echo revert; fi; ` +
		sec("untracked") + `git ls-files -z -o --exclude-standard | xargs -0 sh -c ` + machine.Quote(untracked) + ` sh; ` +
		sec("large") + `{ git ls-files -z -o --exclude-standard; git diff -z --name-only --no-renames --diff-filter=AMT "$base" -- 2>/dev/null; } | ` +
		`xargs -0 wc -c -- 2>/dev/null | awk '$1 > ` + strconv.Itoa(LargeBytes) + `'; ` +
		sec("end") + `:`
}

// section prints the frame parseSections looks for.
func section(m, name string) string { return `printf '\000@@NB` + m + `@@` + name + `\000'; ` }

// parseSections splits framed script output into named sections.
func parseSections(out []byte, m string) map[string][]byte {
	sep := []byte("\x00@@NB" + m + "@@")
	parts := bytes.Split(out, sep)
	secs := map[string][]byte{}
	for _, p := range parts[1:] {
		name, body, ok := bytes.Cut(p, []byte{0})
		if !ok {
			continue
		}
		secs[string(name)] = body
	}
	return secs
}

// Status reads the repository state (docs/API-studio.md §3.2).
func (s *Service) Status(ctx context.Context, r Repo) (*Status, error) {
	m := nonce()
	res, err := s.exec(ctx, r, statusScript(m), execOpts{timeout: ReadTimeout})
	if err != nil {
		if ge, ok := err.(*Error); ok && ge.Code == "not_repo" {
			return &Status{Files: []File{}, LargeFiles: []LargeFile{}}, nil
		}
		return nil, err
	}
	secs := parseSections(res.Stdout, m)
	if _, ok := secs["end"]; !ok {
		return nil, failed(res, "git_failed", 502)
	}
	st := parseStatus(secs)
	s.publishStatus(r, st, false)
	return st, nil
}

func parseStatus(secs map[string][]byte) *Status {
	st := &Status{IsRepo: true, Root: string(secs["top"]), Files: []File{}, LargeFiles: []LargeFile{}}
	recs := strings.Split(string(secs["status"]), "\x00")
	var files []File
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if rec == "" {
			continue
		}
		switch rec[0] {
		case '#':
			parseHeader(st, rec)
		case '1':
			f := strings.SplitN(rec, " ", 9)
			if len(f) == 9 {
				files = append(files, entry(f[1], f[8], "", f[2]))
			}
		case '2':
			f := strings.SplitN(rec, " ", 10)
			if len(f) == 10 {
				old := ""
				if i+1 < len(recs) {
					old = recs[i+1]
					i++
				}
				e := entry(f[1], f[9], old, f[2])
				if strings.HasPrefix(f[8], "C") {
					e.OldPath = "" // copy: the source is unchanged
					e.Status = "added"
				}
				files = append(files, e)
			}
		case 'u':
			f := strings.SplitN(rec, " ", 11)
			if len(f) == 11 {
				e := entry(f[1], f[10], "", f[2])
				e.Status = "conflict"
				files = append(files, e)
				st.Conflicts++
			}
		case '?':
			// With --untracked-files=all git only stops at a folder when it
			// is a repository of its own.
			if strings.HasSuffix(rec, "/") {
				st.NestedRepos = append(st.NestedRepos, strings.TrimSuffix(rec[2:], "/"))
				continue
			}
			files = append(files, File{Path: rec[2:], Index: "?", Worktree: "?", Status: "untracked"})
		}
	}
	// Line counts: tracked from numstat, untracked from the probe loop.
	counts := map[string][3]int{} // added, deleted, binary
	ns := strings.Split(string(secs["numstat"]), "\x00")
	for _, l := range ns {
		a, rest, ok := strings.Cut(l, "\t")
		if !ok {
			continue
		}
		d, p, ok := strings.Cut(rest, "\t")
		if !ok || p == "" {
			continue
		}
		counts[p] = numCounts(a, d)
	}
	for _, l := range strings.Split(string(secs["untracked"]), "\x00") {
		f := strings.SplitN(l, "\t", 4)
		if len(f) != 4 {
			continue
		}
		if f[1] == "x" {
			continue
		}
		counts[f[3]] = numCounts(f[1], f[2])
	}
	for i := range files {
		if c, ok := counts[files[i].Path]; ok {
			files[i].Added, files[i].Deleted, files[i].Binary = c[0], c[1], c[2] == 1
		}
	}
	sort.SliceStable(files, func(i, k int) bool { return files[i].Path < files[k].Path })
	st.Changed = len(files)
	if len(files) > MaxFiles {
		files, st.Truncated = files[:MaxFiles], true
	}
	st.Files = files
	// Remotes.
	remotes := map[string]string{}
	var names []string
	for _, l := range strings.Split(string(secs["remotes"]), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), " ")
		if !ok || !strings.HasPrefix(k, "remote.") || !strings.HasSuffix(k, ".url") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(k, "remote."), ".url")
		if _, dup := remotes[name]; !dup {
			names = append(names, name)
		}
		remotes[name] = v
	}
	sort.Strings(names)
	st.HasRemote = len(names) > 0
	pick := ""
	for _, n := range names {
		if st.Upstream != "" && strings.HasPrefix(st.Upstream, n+"/") && len(n) > len(pick) {
			pick = n
		}
	}
	if pick == "" && remotes["origin"] != "" {
		pick = "origin"
	}
	if pick == "" && len(names) > 0 {
		pick = names[0]
	}
	if pick != "" {
		st.RemoteName = pick
		st.RemoteURL = SafeRemoteURL(remotes[pick])
		st.WebURL = WebURL(remotes[pick])
	}
	if sec := strings.TrimSpace(string(secs["fetched"])); sec != "" {
		if n, err := strconv.ParseInt(sec, 10, 64); err == nil && n > 0 {
			t := time.Unix(n, 0).UTC()
			st.FetchedAt = &t
		}
	}
	st.Merging = strings.TrimSpace(string(secs["merging"])) == "1"
	st.Operation = strings.TrimSpace(string(secs["operation"]))
	// Large files: only changed paths count.
	tracked := map[string]bool{}
	changed := map[string]bool{}
	for _, f := range files {
		changed[f.Path] = true
		tracked[f.Path] = f.Status != "untracked"
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(string(secs["large"]), "\n") {
		m := largeRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		size, _ := strconv.ParseInt(m[1], 10, 64)
		p := m[2]
		if !changed[p] || seen[p] || size <= LargeBytes {
			continue
		}
		seen[p] = true
		st.LargeFiles = append(st.LargeFiles, LargeFile{Path: p, SizeBytes: size, Tracked: tracked[p]})
	}
	return st
}

var largeRe = regexp.MustCompile(`^\s*(\d+) (.+)$`)

func numCounts(a, d string) [3]int {
	if a == "-" || d == "-" {
		return [3]int{0, 0, 1}
	}
	x, _ := strconv.Atoi(a)
	y, _ := strconv.Atoi(d)
	return [3]int{x, y, 0}
}

func parseHeader(st *Status, rec string) {
	f := strings.Fields(rec)
	if len(f) < 3 {
		return
	}
	switch f[1] {
	case "branch.oid":
		st.HasCommits = f[2] != "(initial)"
	case "branch.head":
		if f[2] == "(detached)" {
			st.Detached, st.Branch = true, "HEAD"
		} else {
			st.Branch = strings.Join(f[2:], " ")
		}
	case "branch.upstream":
		st.Upstream = f[2]
	case "branch.ab":
		if len(f) >= 4 {
			st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[2], "+"))
			st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[3], "-"))
		}
	}
}

// entry builds a File from porcelain v2 XY.
func entry(xy, p, old, sub string) File {
	x, y := ".", "."
	if len(xy) == 2 {
		x, y = xy[:1], xy[1:]
	}
	sp := func(c string) string {
		if c == "." {
			return " "
		}
		return c
	}
	e := File{Path: p, OldPath: old, Index: sp(x), Worktree: sp(y)}
	switch {
	case old != "":
		e.Status = "renamed"
	case x == "D" || y == "D":
		e.Status = "deleted"
	case x == "A":
		e.Status = "added"
	case x == "T" || y == "T":
		e.Status = "typechange"
	default:
		e.Status = "modified"
	}
	_ = sub
	return e
}

var scpLike = regexp.MustCompile(`^(?:[A-Za-z0-9._~-]+@)?([A-Za-z0-9.-]+):(.+)$`)

// SafeRemoteURL hides credentials of an http(s) remote URL.
func SafeRemoteURL(raw string) string {
	u, err := url.Parse(raw)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.User != nil {
		u.User = nil
		return u.String()
	}
	return raw
}

// WebURL turns a remote URL into the repository's web page (best effort;
// "" for local paths).
func WebURL(raw string) string {
	raw = strings.TrimSpace(raw)
	var host, p string
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		switch u.Scheme {
		case "http", "https", "ssh", "git", "git+ssh", "ssh+git":
		default:
			return ""
		}
		host, p = u.Hostname(), u.Path
		if u.Scheme == "http" || u.Scheme == "https" {
			host = u.Host
		}
	} else if m := scpLike.FindStringSubmatch(raw); m != nil && !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "/") {
		host, p = m[1], m[2]
	} else {
		return ""
	}
	p = strings.TrimSuffix(strings.Trim(path.Clean("/"+p), "/"), ".git")
	if host == "" || p == "" || p == "." {
		return ""
	}
	return fmt.Sprintf("https://%s/%s", host, p)
}
