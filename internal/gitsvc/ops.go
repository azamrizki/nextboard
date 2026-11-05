package gitsvc

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/machine"
)

// cleanPath validates a repo-relative path from a client: relative, inside
// the repository, no control characters, never the .git folder.
func cleanPath(p string) (string, error) {
	bad := func() error { return newErr(http.StatusBadRequest, "bad_path", "path tidak valid: %q", p) }
	if p == "" || len(p) > 4096 || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\r\n") || !utf8.ValidString(p) {
		return "", bad()
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", bad()
	}
	for _, seg := range strings.Split(c, "/") {
		if seg == ".git" {
			return "", bad()
		}
	}
	return c, nil
}

func cleanPaths(ps []string) ([]string, error) {
	if len(ps) > maxPaths {
		return nil, newErr(http.StatusBadRequest, "too_many", "maksimal %d path", maxPaths)
	}
	out := make([]string, 0, len(ps))
	seen := map[string]bool{}
	for _, p := range ps {
		c, err := cleanPath(p)
		if err != nil {
			return nil, err
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// Diff is GET …/git/diff.
type Diff struct {
	Path      string `json:"path"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

// Diff returns the patch of one path: against HEAD (staged=false) or the
// staged part only; an untracked file is shown as a new file.
func (s *Service) Diff(ctx context.Context, r Repo, p string, staged bool) (*Diff, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	q := machine.Quote(p)
	script := prelude(true, true) +
		`base=HEAD; git rev-parse -q --verify HEAD >/dev/null 2>&1 || base=$(git hash-object -t tree /dev/null); `
	opts := `--no-color --no-ext-diff --no-textconv `
	if staged {
		script += `git diff ` + opts + `--cached "$base" -- ` + q
	} else {
		script += `if git ls-files --error-unmatch -- ` + q + ` >/dev/null 2>&1 || git cat-file -e "$base:"` + q + ` 2>/dev/null; then ` +
			`git diff ` + opts + `"$base" -- ` + q + `; else ` +
			`git diff --no-index ` + opts + `-- /dev/null ` + q + `; [ $? -le 1 ]; fi`
	}
	res, err := s.exec(ctx, r, script, execOpts{timeout: ReadTimeout, maxOutput: MaxDiff})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	d := &Diff{Path: p, Diff: strings.ToValidUTF8(string(res.Stdout), "�"), Truncated: res.Truncated}
	d.Binary = strings.Contains(d.Diff, "\nBinary files ") || strings.HasPrefix(d.Diff, "Binary files ")
	return d, nil
}

// FileAt is one file as it is in a commit (the editor's "compare with HEAD").
type FileAt struct {
	Path      string `json:"path"`
	Ref       string `json:"ref"`
	Exists    bool   `json:"exists"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

const (
	exitNoBlob = 44
	// MaxShow bounds one file of Show.
	MaxShow = 10 << 20
)

var showRef = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./~^-]*$`)

// Show returns the content of a repository-relative path at ref (HEAD by
// default). A path the commit does not have is Exists=false, not an error.
func (s *Service) Show(ctx context.Context, r Repo, p, ref string) (*FileAt, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		ref = "HEAD"
	}
	if !showRef.MatchString(ref) {
		return nil, newErr(http.StatusBadRequest, "bad_ref", "ref tidak valid")
	}
	q := machine.Quote(ref + ":" + p)
	script := prelude(true, true) + fmt.Sprintf(`git cat-file -e %s 2>/dev/null || exit %d; git cat-file blob %s`, q, exitNoBlob, q)
	res, err := s.exec(ctx, r, script, execOpts{timeout: ReadTimeout, maxOutput: MaxShow})
	if err != nil {
		return nil, err
	}
	f := &FileAt{Path: p, Ref: ref}
	switch {
	case res.ExitCode == exitNoBlob:
		return f, nil
	case res.ExitCode != 0:
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	f.Exists, f.Truncated = true, res.Truncated
	if bytes.IndexByte(res.Stdout[:min(len(res.Stdout), 8<<10)], 0) >= 0 || !utf8.Valid(res.Stdout) {
		f.Binary = true
		return f, nil
	}
	f.Content = string(res.Stdout)
	return f, nil
}

// Commit is one log entry.
type Commit struct {
	Hash    string    `json:"hash"`
	Short   string    `json:"short"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	At      time.Time `json:"at"`
	Pushed  bool      `json:"pushed"`
	// Parents and Refs are only filled for the history graph.
	Parents []string `json:"parents,omitempty"`
	Refs    []Ref    `json:"refs,omitempty"`
}

// Ref is a branch or tag that points at a commit. Kind is "head" (what is
// checked out), "branch", "remote" or "tag".
type Ref struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func logScript(limit int) string {
	return prelude(true, true) +
		`git rev-parse -q --verify HEAD >/dev/null 2>&1 || exit 0; ` +
		`git log -n ` + strconv.Itoa(limit) + ` --no-color --format='%H%x00%h%x00%s%x00%an%x00%aI%x00' HEAD --; ` +
		`printf '\000@@UNPUSHED@@\000'; ` +
		`if git rev-parse -q --verify '@{u}' >/dev/null 2>&1; then git rev-list -n 1000 '@{u}..HEAD' --; ` +
		`else git rev-list -n 1000 HEAD --not --remotes --; fi`
}

// Log returns up to limit commits of HEAD (≤ 100), newest first.
func (s *Service) Log(ctx context.Context, r Repo, limit int) ([]Commit, error) {
	if limit <= 0 || limit > MaxLog {
		limit = MaxLog
	}
	res, err := s.exec(ctx, r, logScript(limit), execOpts{timeout: ReadTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	return parseLog(string(res.Stdout)), nil
}

// GraphOptions chooses the commits of the history graph.
type GraphOptions struct {
	Limit, Skip int
	// All takes every branch, remote branch and tag, not only HEAD.
	All bool
}

const logMark = "\x00@@UNPUSHED@@\x00"

func graphScript(o GraphOptions) string {
	scope, unpushed := `HEAD`, `if git rev-parse -q --verify '@{u}' >/dev/null 2>&1; then git rev-list -n 1000 '@{u}..HEAD' --; else git rev-list -n 1000 HEAD --not --remotes --; fi`
	if o.All {
		scope, unpushed = `--branches --tags --remotes HEAD`, `git rev-list -n 1000 --branches HEAD --not --remotes --`
	}
	return prelude(true, true) +
		`git rev-parse -q --verify HEAD >/dev/null 2>&1 || exit 0; ` +
		`git log -n ` + strconv.Itoa(o.Limit) + ` --skip=` + strconv.Itoa(o.Skip) + ` --no-color --topo-order --decorate=full ` +
		`--format='%H%x00%h%x00%s%x00%an%x00%aI%x00%P%x00%D%x00' ` + scope + ` --; ` +
		`printf '\000@@UNPUSHED@@\000'; ` + unpushed
}

// LogGraph returns one page of the history graph, children before their parents.
func (s *Service) LogGraph(ctx context.Context, r Repo, o GraphOptions) ([]Commit, error) {
	if o.Limit <= 0 || o.Limit > MaxLog {
		o.Limit = MaxLog
	}
	o.Skip = max(o.Skip, 0)
	res, err := s.exec(ctx, r, graphScript(o), execOpts{timeout: ReadTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	return parseGraph(string(res.Stdout)), nil
}

func parseGraph(out string) []Commit {
	logPart, unpushedPart, _ := strings.Cut(out, logMark)
	unpushed := map[string]bool{}
	for _, h := range strings.Fields(unpushedPart) {
		unpushed[h] = true
	}
	items := []Commit{}
	f := strings.Split(logPart, "\x00")
	for i := 0; i+6 < len(f); i += 7 {
		h := strings.TrimSpace(f[i])
		if h == "" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, strings.TrimSpace(f[i+4]))
		items = append(items, Commit{Hash: h, Short: f[i+1], Subject: f[i+2], Author: f[i+3], At: at.UTC(), Pushed: !unpushed[h],
			Parents: strings.Fields(f[i+5]), Refs: parseRefs(f[i+6])})
	}
	return items
}

// parseRefs reads %D with --decorate=full, as in "HEAD -> refs/heads/main,
// tag: refs/tags/v1, refs/remotes/origin/main". Names hold no spaces.
func parseRefs(d string) []Ref {
	var out []Ref
	for _, part := range strings.Split(d, ", ") {
		part = strings.TrimSpace(part)
		switch {
		case part == "HEAD":
			out = append(out, Ref{Name: "HEAD", Kind: "head"})
		case strings.HasPrefix(part, "HEAD -> "):
			out = append(out, Ref{Name: strings.TrimPrefix(strings.TrimPrefix(part, "HEAD -> "), "refs/heads/"), Kind: "head"})
		case strings.HasPrefix(part, "tag: "):
			out = append(out, Ref{Name: strings.TrimPrefix(strings.TrimPrefix(part, "tag: "), "refs/tags/"), Kind: "tag"})
		case strings.HasPrefix(part, "refs/heads/"):
			out = append(out, Ref{Name: strings.TrimPrefix(part, "refs/heads/"), Kind: "branch"})
		case strings.HasPrefix(part, "refs/remotes/") && !strings.HasSuffix(part, "/HEAD"):
			out = append(out, Ref{Name: strings.TrimPrefix(part, "refs/remotes/"), Kind: "remote"})
		}
	}
	return out
}

// CommitFile is one file a commit changed, against its first parent.
type CommitFile struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	Status  string `json:"status"`
	Add     int    `json:"add"`
	Del     int    `json:"del"`
	Binary  bool   `json:"binary,omitempty"`
}

// CommitDetail is GET …/git/commits/{ref}.
type CommitDetail struct {
	Hash           string       `json:"hash"`
	Short          string       `json:"short"`
	Parents        []string     `json:"parents"`
	Refs           []Ref        `json:"refs"`
	Subject        string       `json:"subject"`
	Body           string       `json:"body"`
	Author         string       `json:"author"`
	Email          string       `json:"email"`
	At             time.Time    `json:"at"`
	Committer      string       `json:"committer"`
	CommitterEmail string       `json:"committerEmail"`
	CommittedAt    time.Time    `json:"committedAt"`
	Files          []CommitFile `json:"files"`
	// Truncated says that Files stops at MaxCommitFiles.
	Truncated bool `json:"truncated"`
}

// MaxCommitFiles bounds the file list of one commit.
const MaxCommitFiles = 2000

const commitFilesMark = "\x00@@FILES@@\x00"
const commitStatsMark = "\x00@@STATS@@\x00"

// CommitDetail describes one commit and what it changed. A merge shows what it
// changed against its first parent, a first commit everything it added.
func (s *Service) CommitDetail(ctx context.Context, r Repo, ref string) (*CommitDetail, error) {
	if !showRef.MatchString(ref) {
		return nil, newErr(http.StatusBadRequest, "bad_ref", "ref tidak valid")
	}
	script := prelude(true, true) +
		`h=$(git rev-parse -q --verify ` + machine.Quote(ref+"^{commit}") + ` 2>/dev/null) || exit ` + strconv.Itoa(exitNoBlob) + `; ` +
		`git show -s --no-color --decorate=full --format='%H%x00%h%x00%P%x00%D%x00%s%x00%b%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI' "$h" --; ` +
		`p=$(git rev-parse -q --verify "$h^1" 2>/dev/null); if [ -n "$p" ]; then a="$p $h"; else a="--root $h"; fi; ` +
		`printf '\000@@FILES@@\000'; git diff-tree -r -M --no-commit-id --name-status -z $a; ` +
		`printf '\000@@STATS@@\000'; git diff-tree -r -M --no-commit-id --numstat -z $a`
	res, err := s.exec(ctx, r, script, execOpts{timeout: ReadTimeout})
	if err != nil {
		return nil, err
	}
	switch {
	case res.ExitCode == exitNoBlob:
		return nil, newErr(http.StatusNotFound, "commit_not_found", "commit tidak ditemukan")
	case res.ExitCode != 0:
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	d := parseCommitDetail(string(res.Stdout))
	if d == nil {
		return nil, newErr(http.StatusBadGateway, "git_failed", "keluaran git tidak terbaca")
	}
	return d, nil
}

func parseCommitDetail(out string) *CommitDetail {
	head, rest, ok := strings.Cut(out, commitFilesMark)
	if !ok {
		return nil
	}
	names, stats, _ := strings.Cut(rest, commitStatsMark)
	f := strings.Split(head, "\x00")
	if len(f) < 12 {
		return nil
	}
	at, _ := time.Parse(time.RFC3339, strings.TrimSpace(f[8]))
	committed, _ := time.Parse(time.RFC3339, strings.TrimSpace(f[11]))
	d := &CommitDetail{Hash: strings.TrimSpace(f[0]), Short: f[1], Parents: strings.Fields(f[2]), Refs: parseRefs(f[3]), Subject: f[4],
		Body: strings.TrimSpace(f[5]), Author: f[6], Email: f[7], At: at.UTC(), Committer: f[9], CommitterEmail: f[10], CommittedAt: committed.UTC(), Files: []CommitFile{}}
	if d.Parents == nil {
		d.Parents = []string{}
	}
	if d.Refs == nil {
		d.Refs = []Ref{}
	}
	counts := parseNumstat(stats)
	t := strings.Split(names, "\x00")
	for i := 0; i+1 < len(t) && t[i] != ""; {
		file := CommitFile{Status: commitStatus(t[i]), Path: t[i+1]}
		i += 2
		if file.Status == "renamed" && i < len(t) {
			file.OldPath, file.Path = file.Path, t[i]
			i++
		}
		if len(d.Files) == MaxCommitFiles {
			d.Truncated = true
			break
		}
		if c, ok := counts[file.Path]; ok {
			file.Add, file.Del, file.Binary = c.Add, c.Del, c.Binary
		}
		d.Files = append(d.Files, file)
	}
	return d
}

func commitStatus(code string) string {
	switch code[0] {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'T':
		return "typechange"
	}
	return "modified"
}

// parseNumstat reads "add\tdel\tpath\0", or for a rename "add\tdel\t\0old\0new\0";
// a binary file counts "-".
func parseNumstat(out string) map[string]CommitFile {
	counts := map[string]CommitFile{}
	t := strings.Split(out, "\x00")
	for i := 0; i < len(t); i++ {
		add, rest, ok := strings.Cut(t[i], "\t")
		del, path, ok2 := strings.Cut(rest, "\t")
		if !ok || !ok2 {
			continue
		}
		if path == "" && i+2 < len(t) {
			path = t[i+2]
			i += 2
		}
		a, _ := strconv.Atoi(add)
		b, _ := strconv.Atoi(del)
		counts[path] = CommitFile{Add: a, Del: b, Binary: add == "-"}
	}
	return counts
}

// FileLog returns the commits that changed a file (followed through renames),
// or only one line of it when line > 0.
func (s *Service) FileLog(ctx context.Context, r Repo, p string, line, limit int) ([]Commit, error) {
	c, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxLog {
		limit = MaxLog
	}
	const format = `--no-color --format='%H%x00%h%x00%s%x00%an%x00%aI%x00'`
	script := prelude(true, true) + `git rev-parse -q --verify HEAD >/dev/null 2>&1 || exit 0; git log -n ` + strconv.Itoa(limit) + ` ` + format + ` `
	if line > 0 {
		n := strconv.Itoa(line)
		script += `-s -L ` + machine.Quote(n+","+n+":"+c) + ` HEAD`
	} else {
		script += `--follow HEAD -- ` + machine.Quote(c)
	}
	res, err := s.exec(ctx, r, script, execOpts{timeout: ReadTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		// a file git does not know has no history; that is no error
		if strings.Contains(string(res.Stderr), "no such path") || strings.Contains(string(res.Stderr), "There is no path") {
			return []Commit{}, nil
		}
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	return parseLog(string(res.Stdout)), nil
}

// BlameCommit is one commit of a blame. A line that is not committed yet has
// the all-zero hash.
type BlameCommit struct {
	Hash    string    `json:"hash"`
	Author  string    `json:"author"`
	Email   string    `json:"email"`
	At      time.Time `json:"at"`
	Summary string    `json:"summary"`
}

// Blame is POST …/git/blame.
type Blame struct {
	Commits []BlameCommit `json:"commits"`
	// Lines holds the index in Commits of each line of the file, from the first.
	Lines []int `json:"lines"`
	// Me is the repository's user.email, to tell the user's own lines apart.
	Me string `json:"me"`
}

const blameMaxOutput = 64 << 20

// Blame says who last changed each line of a file. With text it blames that
// text, an editor's unsaved state, against HEAD.
func (s *Service) Blame(ctx context.Context, r Repo, p string, text *string) (*Blame, error) {
	c, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	script := prelude(true, true) + `git rev-parse -q --verify HEAD >/dev/null 2>&1 || exit 0; git config user.email; printf '\000'; git blame --porcelain `
	o := execOpts{timeout: ReadTimeout, maxOutput: blameMaxOutput}
	if text != nil {
		script += `--contents - `
		o.stdin = []byte(*text)
	}
	res, err := s.exec(ctx, r, script+`-- `+machine.Quote(c), o)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		// new and ignored files simply have nobody to blame yet
		if strings.Contains(string(res.Stderr), "no such path") {
			return &Blame{Commits: []BlameCommit{}, Lines: []int{}}, nil
		}
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	return parseBlame(string(res.Stdout)), nil
}

var blameHead = regexp.MustCompile(`^([0-9a-f]{40,64}) \d+ \d+`)

func parseBlame(out string) *Blame {
	me, body, _ := strings.Cut(out, "\x00")
	b := &Blame{Commits: []BlameCommit{}, Lines: []int{}, Me: strings.TrimSpace(me)}
	seen := map[string]int{}
	cur := -1
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "\t") {
			if cur >= 0 {
				b.Lines = append(b.Lines, cur)
			}
			continue
		}
		if m := blameHead.FindStringSubmatch(l); m != nil {
			i, ok := seen[m[1]]
			if !ok {
				i = len(b.Commits)
				seen[m[1]] = i
				b.Commits = append(b.Commits, BlameCommit{Hash: m[1]})
			}
			cur = i
			continue
		}
		if cur < 0 {
			continue
		}
		k, v, _ := strings.Cut(l, " ")
		c := &b.Commits[cur]
		switch k {
		case "author":
			c.Author = v
		case "author-mail":
			c.Email = strings.Trim(v, "<>")
		case "author-time":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				c.At = time.Unix(n, 0).UTC()
			}
		case "summary":
			c.Summary = v
		}
	}
	return b
}

func (st *Status) hasStaged() bool {
	for _, f := range st.Files {
		if f.Index != "" && f.Index != " " && f.Index != "?" {
			return true
		}
	}
	return false
}

func parseLog(out string) []Commit {
	logPart, unpushedPart, _ := strings.Cut(out, "\x00@@UNPUSHED@@\x00")
	unpushed := map[string]bool{}
	for _, h := range strings.Fields(unpushedPart) {
		unpushed[h] = true
	}
	items := []Commit{}
	f := strings.Split(logPart, "\x00")
	for i := 0; i+4 < len(f); i += 5 {
		h := strings.TrimSpace(f[i])
		if h == "" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, strings.TrimSpace(f[i+4]))
		items = append(items, Commit{Hash: h, Short: f[i+1], Subject: f[i+2], Author: f[i+3], At: at.UTC(), Pushed: !unpushed[h]})
	}
	return items
}

// CommitRequest is POST …/git/commit.
type CommitRequest struct {
	Message string   `json:"message"`
	Paths   []string `json:"paths"`
	All     bool     `json:"all"`
	// Staged commits what the index holds and nothing else.
	Staged     bool `json:"staged"`
	AllowLarge bool `json:"allowLarge"`
}

// CommitResult is the commit response.
type CommitResult struct {
	Commit Commit  `json:"commit"`
	Status *Status `json:"status"`
}

const markNoIdent = "__NB_NOIDENT__"

// Commit stages and commits the chosen paths (or everything). User commit
// hooks run as usual (no --no-verify).
func (s *Service) Commit(ctx context.Context, r Repo, req CommitRequest) (*CommitResult, error) {
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		return nil, newErr(http.StatusBadRequest, "empty_message", "pesan commit kosong")
	}
	if len(msg) > maxMessage || strings.ContainsRune(msg, 0) {
		return nil, newErr(http.StatusBadRequest, "bad_message", "pesan commit tidak valid")
	}
	paths, err := cleanPaths(req.Paths)
	if err != nil {
		return nil, err
	}
	if !req.All && !req.Staged && len(paths) == 0 {
		return nil, newErr(http.StatusBadRequest, "no_paths", "pilih file yang akan di-commit")
	}
	unlock, err := s.lock(r, "commit")
	if err != nil {
		return nil, err
	}
	defer unlock()
	out, err := s.commitLocked(ctx, r, req, msg, paths)
	if err != nil {
		return nil, err
	}
	unlock()
	out.Status = s.refresh(r)
	return out, nil
}

// addAll stages every change except nested repositories: git would record
// one as a gitlink whose files a clone never gets, and refuses one without
// commits.
func addAll(nested []string) string {
	if len(nested) == 0 {
		return `git add -A`
	}
	args := []string{"."}
	for _, n := range nested {
		args = append(args, ":(exclude,literal)"+n)
	}
	return `GIT_LITERAL_PATHSPECS=0 git add -A -- ` + machine.Join(args...)
}

// commitLocked commits while the caller holds the repository lock.
func (s *Service) commitLocked(ctx context.Context, r Repo, req CommitRequest, msg string, paths []string) (*CommitResult, error) {
	st, err := s.Status(ctx, r)
	if err != nil {
		return nil, err
	}
	if !st.IsRepo {
		return nil, errNotRepo()
	}
	if st.Conflicts > 0 {
		return nil, newErr(http.StatusConflict, "conflicts", "selesaikan konflik merge dulu (%d file)", st.Conflicts)
	}
	// Resolve the selection against the current changes; a renamed
	// file brings its old path along.
	var sel []string
	if req.Staged {
		if !st.hasStaged() {
			return nil, newErr(http.StatusConflict, "nothing_to_commit", "tidak ada yang di-stage")
		}
	} else if !req.All {
		set := map[string]bool{}
		for _, p := range paths {
			f := st.file(p)
			if f == nil {
				continue
			}
			set[f.Path] = true
			if f.OldPath != "" {
				set[f.OldPath] = true
			}
		}
		for p := range set {
			sel = append(sel, p)
		}
		sort.Strings(sel)
		if len(sel) == 0 && !st.Truncated {
			return nil, newErr(http.StatusConflict, "nothing_to_commit", "tidak ada perubahan pada file yang dipilih")
		}
		if len(sel) == 0 {
			sel = paths // beyond the 400 listed files; git decides
		}
	} else if st.Changed == 0 {
		return nil, newErr(http.StatusConflict, "nothing_to_commit", "tidak ada perubahan untuk di-commit")
	}
	if !req.AllowLarge {
		var big []string
		inSel := map[string]bool{}
		for _, p := range sel {
			inSel[p] = true
		}
		for _, lf := range st.LargeFiles {
			if req.All || inSel[lf.Path] {
				big = append(big, lf.Path)
			}
		}
		if len(big) > 0 {
			return nil, &Error{Status: http.StatusConflict, Code: "large_files", Msg: "file lebih dari 100 MB tidak bisa di-push ke GitHub", Paths: big}
		}
	}
	ident := `[ -n "$(git config user.email)" ] || [ -n "${GIT_AUTHOR_EMAIL:-}${EMAIL:-}" ] || { echo ` + markNoIdent + ` >&2; exit 3; }; ` +
		`[ -n "$(git config user.name)" ] || [ -n "${GIT_AUTHOR_NAME:-}" ] || { echo ` + markNoIdent + ` >&2; exit 3; }; `
	script := prelude(true, false) + ident
	qm := machine.Quote(msg)
	switch {
	case req.Staged:
		script += `git commit -q -m ` + qm
	case req.All:
		script += addAll(st.NestedRepos) + ` && git commit -q -m ` + qm
	case st.Merging:
		// A merge cannot be committed partially: stage the chosen paths
		// and conclude the merge with the whole index.
		script += `git add -A -- ` + machine.Join(sel...) + ` && git commit -q -m ` + qm
	default:
		script += `git add -A -- ` + machine.Join(sel...) + ` && git commit -q -m ` + qm + ` -- ` + machine.Join(sel...)
	}
	script += ` && git log -n 1 --no-color --format='%H%x00%h%x00%s%x00%an%x00%aI%x00' HEAD --`
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		stderr := string(res.Stderr)
		if strings.Contains(stderr, markNoIdent) || strings.Contains(stderr, "Please tell me who you are") || strings.Contains(stderr, "Author identity unknown") {
			return nil, newErr(http.StatusUnprocessableEntity, "git_identity", "user.name / user.email git belum diset di machine ini (git config --global user.email …)")
		}
		if strings.Contains(stderr, "nothing to commit") || strings.Contains(string(res.Stdout), "nothing to commit") {
			return nil, newErr(http.StatusConflict, "nothing_to_commit", "tidak ada perubahan untuk di-commit")
		}
		return nil, failed(res, "git_failed", http.StatusConflict)
	}
	items := parseLog(string(res.Stdout) + "\x00@@UNPUSHED@@\x00")
	out := &CommitResult{}
	if len(items) > 0 {
		out.Commit = items[0]
		out.Commit.Pushed = false
	}
	return out, nil
}

var tsRe = regexp.MustCompile(`^\d{8}-\d{6}(-\d{1,3})?$`)

// DiscardResult is POST …/git/discard.
type DiscardResult struct {
	TrashedTo string   `json:"trashedTo"`
	Trashed   []string `json:"trashed"`
	Status    *Status  `json:"status"`
}

const markTrash = "__NB_TRASH__"

// Discard throws away changes: the worktree version of tracked files is
// copied into <gitdir>/nextboard-trash/<ts>/ before `git restore`, and
// untracked files are moved there. Nothing is deleted.
func (s *Service) Discard(ctx context.Context, r Repo, paths []string) (*DiscardResult, error) {
	paths, err := cleanPaths(paths)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, newErr(http.StatusBadRequest, "no_paths", "pilih file yang akan dibuang")
	}
	unlock, err := s.lock(r, "discard")
	if err != nil {
		return nil, err
	}
	defer unlock()
	st, err := s.Status(ctx, r)
	if err != nil {
		return nil, err
	}
	if !st.IsRepo {
		return nil, errNotRepo()
	}
	var untracked, tracked []string
	seen := map[string]bool{}
	for _, p := range paths {
		f := st.file(p)
		if f == nil || seen[p] {
			continue
		}
		seen[p] = true
		if f.Status == "untracked" {
			untracked = append(untracked, f.Path)
			continue
		}
		tracked = append(tracked, f.Path)
		if f.OldPath != "" && !seen[f.OldPath] {
			seen[f.OldPath] = true
			tracked = append(tracked, f.OldPath)
		}
	}
	if len(untracked)+len(tracked) == 0 {
		return nil, newErr(http.StatusConflict, "nothing_to_discard", "tidak ada perubahan pada file yang dipilih")
	}
	ts := s.Now().Format("20060102-150405")
	// mkdir without -p on the item folder: a second discard within the
	// same second gets a -2/-3… suffix instead of mixing items. A path
	// known to HEAD is copied to the trash and restored from HEAD; a path
	// HEAD does not have (new, untracked, rename target) is moved there.
	script := prelude(true, false) + trashCD +
		`mkdir -p "$gd/nextboard-trash" || exit 4; id=` + machine.Quote(ts) + `; n=1; ` +
		`while ! mkdir "$gd/nextboard-trash/$id" 2>/dev/null; do n=$((n+1)); [ $n -le 50 ] || exit 4; id=` + machine.Quote(ts) + `-$n; done; ` +
		`T="$gd/nextboard-trash/$id"; printf '%s%s\n' ` + markTrash + ` "$id"; ` +
		`there() { [ -e "$1" ] || [ -L "$1" ]; }; ` +
		`keep() { mkdir -p -- "$T/$(dirname -- "$1")" || exit 5; }; ` +
		`away() { if there "$1"; then keep "$1"; mv -- "$1" "$T/$1" || exit 5; fi; }; `
	for _, p := range tracked {
		q := machine.Quote(p)
		script += `p=` + q + `; if git cat-file -e "HEAD:$p" 2>/dev/null; then ` +
			`if there "$p"; then keep "$p"; cp -pP -- "$p" "$T/$p" || exit 5; fi; ` +
			`git restore --source=HEAD --staged --worktree -- "$p" || exit 6; ` +
			`else away "$p"; git rm -q --cached --ignore-unmatch -- "$p" >/dev/null || exit 6; fi; `
	}
	for _, p := range untracked {
		script += `away ` + machine.Quote(p) + `; `
	}
	script += `:`
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	id := ""
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if strings.HasPrefix(l, markTrash) {
			id = strings.TrimSpace(strings.TrimPrefix(l, markTrash))
		}
	}
	if res.ExitCode != 0 {
		e := failed(res, "git_failed", http.StatusConflict)
		if id != "" {
			e.Msg += " (salinan ada di .git/nextboard-trash/" + id + ")"
		}
		return nil, e
	}
	out := &DiscardResult{TrashedTo: ".git/nextboard-trash/" + id, Trashed: append(tracked, untracked...)}
	if id == "" {
		out.TrashedTo = ""
	}
	unlock()
	out.Status = s.refresh(r)
	return out, nil
}

// TrashItem is one discard in the trash.
type TrashItem struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Paths []string  `json:"paths"`
}

const trashCD = `gd=$(git rev-parse --absolute-git-dir 2>/dev/null || (cd "$(git rev-parse --git-dir)" && pwd)) || exit 4; `

// Trash lists the discards kept in <gitdir>/nextboard-trash (newest first).
func (s *Service) Trash(ctx context.Context, r Repo) ([]TrashItem, error) {
	script := prelude(true, true) + trashCD +
		`[ -d "$gd/nextboard-trash" ] || exit 0; cd "$gd/nextboard-trash" || exit 0; ` +
		`for d in */; do d=${d%/}; [ -d "$d" ] || continue; printf '\000@@ITEM@@%s\000' "$d"; ` +
		`(cd "$d" && find . \( -type f -o -type l \) -print 2>/dev/null | head -n 400 | tr '\n' '\000'); done`
	res, err := s.exec(ctx, r, script, execOpts{timeout: ReadTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	items := []TrashItem{}
	for _, part := range strings.Split(string(res.Stdout), "\x00@@ITEM@@")[1:] {
		f := strings.Split(part, "\x00")
		id := f[0]
		if !tsRe.MatchString(id) {
			continue
		}
		it := TrashItem{ID: id, Paths: []string{}}
		if t, err := time.ParseInLocation("20060102-150405", id[:15], time.Local); err == nil {
			it.At = t.UTC()
		}
		for _, p := range f[1:] {
			if p = strings.TrimPrefix(p, "./"); p != "" {
				it.Paths = append(it.Paths, p)
			}
		}
		if len(it.Paths) == 0 {
			continue
		}
		sort.Strings(it.Paths)
		items = append(items, it)
	}
	sort.Slice(items, func(i, k int) bool { return items[i].ID > items[k].ID })
	if len(items) > maxTrashList {
		items = items[:maxTrashList]
	}
	return items, nil
}

const markExists = "__NB_EXISTS__"

// RestoreTrash moves trashed files back; nothing is overwritten (409
// exists with the paths in the way).
func (s *Service) RestoreTrash(ctx context.Context, r Repo, id string, paths []string) ([]string, error) {
	if !tsRe.MatchString(id) {
		return nil, newErr(http.StatusBadRequest, "bad_id", "id trash tidak valid")
	}
	paths, err := cleanPaths(paths)
	if err != nil {
		return nil, err
	}
	unlock, err := s.lock(r, "restore")
	if err != nil {
		return nil, err
	}
	defer unlock()
	items, err := s.Trash(ctx, r)
	if err != nil {
		return nil, err
	}
	var item *TrashItem
	for i := range items {
		if items[i].ID == id {
			item = &items[i]
		}
	}
	if item == nil {
		return nil, newErr(http.StatusNotFound, "not_found", "item trash tidak ditemukan")
	}
	want := item.Paths
	if len(paths) > 0 {
		have := map[string]bool{}
		for _, p := range item.Paths {
			have[p] = true
		}
		want = nil
		for _, p := range paths {
			if !have[p] {
				return nil, newErr(http.StatusNotFound, "not_found", "path tidak ada di trash: %s", p)
			}
			want = append(want, p)
		}
	}
	script := prelude(true, false) + trashCD + `T="$gd/nextboard-trash/"` + machine.Quote(id) + `; [ -d "$T" ] || exit 4; e=0; `
	// A path in the way blocks the restore, unless it is a tracked file
	// identical to HEAD (what the discard left behind): nothing is lost
	// by putting the user's version back over it.
	script += `clean() { git ls-files --error-unmatch -- "$1" >/dev/null 2>&1 && [ ! -L "$1" ] && [ -f "$1" ] && git diff --quiet HEAD -- "$1" 2>/dev/null; }; `
	for _, p := range want {
		q := machine.Quote(p)
		script += `if { [ -e ` + q + ` ] || [ -L ` + q + ` ]; } && ! clean ` + q + `; then printf '%s%s\000' ` + markExists + ` ` + q + `; e=1; fi; `
	}
	script += `[ $e = 0 ] || exit 9; `
	for _, p := range want {
		q := machine.Quote(p)
		script += `mkdir -p -- "$(dirname -- ` + q + `)" && mv -f -- "$T/"` + q + ` ` + q + ` || exit 5; `
	}
	// Only empty folders of this item are removed.
	script += `find "$T" -depth -type d -exec rmdir {} \; 2>/dev/null; :`
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 9 {
		var in []string
		for _, f := range strings.Split(string(res.Stdout), "\x00") {
			if strings.HasPrefix(f, markExists) {
				in = append(in, strings.TrimPrefix(f, markExists))
			}
		}
		return nil, &Error{Status: http.StatusConflict, Code: "exists", Msg: "file dengan nama yang sama sudah ada; tidak ditimpa", Paths: in}
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusConflict)
	}
	unlock()
	s.refresh(r)
	return want, nil
}

// Init creates a repository in the project folder (`init -b <branch>`,
// older git: init + symbolic-ref).
func (s *Service) Init(ctx context.Context, r Repo, branch string) (*Status, error) {
	if branch == "" {
		branch = "main"
	}
	if err := checkBranchName(branch); err != nil {
		return nil, err
	}
	unlock, err := s.lock(r, "init")
	if err != nil {
		return nil, err
	}
	defer unlock()
	st, err := s.Status(ctx, r)
	if err != nil {
		return nil, err
	}
	if st.IsRepo {
		return nil, newErr(http.StatusConflict, "exists", "folder ini sudah berada di dalam repository git (%s)", st.Root)
	}
	// A repository here would wrap the ones below it.
	if l, err := s.scan(ctx, r); err == nil && len(l.Items) > 0 {
		return nil, &Error{Status: http.StatusConflict, Code: "nested_repos", Msg: "folder ini berisi repository git di subfolder; pilih salah satu", Paths: l.labels()}
	}
	qb := machine.Quote(branch)
	script := prelude(false, false) + `git check-ref-format --branch ` + qb + ` >/dev/null || exit 7; ` +
		`git init -q -b ` + qb + ` . 2>/dev/null || { git init -q . && git symbolic-ref HEAD refs/heads/` + qb + `; }`
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 7 {
		return nil, newErr(http.StatusBadRequest, "bad_ref", "nama branch tidak valid")
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}

// gitignoreLine escapes a repo path as an anchored .gitignore pattern
// (the leading slash also makes "#" and "!" literal).
func gitignoreLine(p string) string {
	trimmed := strings.TrimRight(p, " ")
	var b strings.Builder
	b.WriteByte('/')
	for _, r := range trimmed {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat(`\ `, len(p)-len(trimmed)))
	return b.String()
}

// UntrackLarge keeps large files off GitHub: `rm --cached` and an
// anchored line per file in .gitignore.
func (s *Service) UntrackLarge(ctx context.Context, r Repo, paths []string) (*Status, error) {
	paths, err := cleanPaths(paths)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, newErr(http.StatusBadRequest, "no_paths", "pilih file")
	}
	unlock, err := s.lock(r, "untrack")
	if err != nil {
		return nil, err
	}
	defer unlock()
	script := prelude(true, false) +
		`git rm -q -r --cached --ignore-unmatch -- ` + machine.Join(paths...) + ` >/dev/null || exit 5; ` +
		`if [ -s .gitignore ] && [ -n "$(tail -c 1 .gitignore)" ]; then echo >> .gitignore; fi; `
	for _, p := range paths {
		l := machine.Quote(gitignoreLine(p))
		script += `grep -qxF -- ` + l + ` .gitignore 2>/dev/null || printf '%s\n' ` + l + ` >> .gitignore || exit 5; `
	}
	script += `:`
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}
