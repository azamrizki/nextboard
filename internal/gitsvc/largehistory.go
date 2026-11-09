package gitsvc

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Files over the limit that already went into a commit (docs/API-studio.md §3.7).
// Untracking them is not enough: the blob is still in the commit, and the push
// still fails. The only cure is new commits without it, so this file rewrites
// the commits that no remote has yet, and nothing else.
//
// It works on plumbing with a private index instead of filter-branch, because
// filter-branch refuses a dirty work tree, and a file this size is rarely the
// only thing someone has open. The branch moves in one last step, so a failure
// or a timeout on the way leaves the repository as it was.

// maxRewriteCommits bounds both the scan and the rewrite. A branch this far
// from any remote is not a case for a button.
const maxRewriteCommits = 2000

// Exit codes of the rewrite and undo scripts.
const (
	exitRewriteMoved    = 80 // HEAD is not what the owner looked at
	exitRewriteDetached = 81
	exitRewriteBusy     = 82 // a merge, rebase, cherry-pick or bisect is under way
	exitRewriteNone     = 83 // no unpushed commits
	exitRewriteMany     = 84
	exitRewriteRace     = 85 // the branch moved during the rewrite
	exitRewriteNoBackup = 86
)

// LargeCommitted is one path over the limit in commits no remote has.
type LargeCommitted struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	// Commits counts the unpushed commits that hold it over the limit.
	Commits int `json:"commits"`
	// InPushed says a commit the remote has holds this path too, at a size it
	// accepted. Removing the path means the next push deletes it there.
	InPushed bool `json:"inPushed"`
}

// LargeHistory is the answer of a scan.
type LargeHistory struct {
	Head     string `json:"head"`
	Detached bool   `json:"detached"`
	// Unpushed counts the commits a rewrite would touch.
	Unpushed int              `json:"unpushed"`
	TooMany  bool             `json:"tooMany,omitempty"`
	Files    []LargeCommitted `json:"files"`
}

// RewriteResult is the answer of a rewrite.
type RewriteResult struct {
	// Backup names the old history (refs/nextboard/backup/<id>); id alone undoes it.
	Backup string `json:"backup"`
	// Head is the branch's new commit.
	Head string `json:"head"`
	// Rewritten and Dropped count commits; a dropped one held nothing else.
	Rewritten int     `json:"rewritten"`
	Dropped   int     `json:"dropped"`
	Status    *Status `json:"status"`
}

// unpushedRange is the commits a rewrite may touch: reachable from HEAD and
// from no remote-tracking ref.
const unpushedRange = `HEAD --not --remotes`

// ScanLargeHistory lists paths over the limit in the unpushed commits. It looks
// at every commit, not at each blob once, because one blob under two names has
// to go under both.
func (s *Service) ScanLargeHistory(ctx context.Context, r Repo) (*LargeHistory, error) {
	script := prelude(true, true) +
		`head=$(git rev-parse --verify -q HEAD) || exit 0; echo "H $head"; ` +
		`git symbolic-ref -q HEAD >/dev/null || echo D; ` +
		fmt.Sprintf(`n=$(git rev-list --count `+unpushedRange+`); echo "N $n"; [ "$n" -le %d ] || exit 0; `, maxRewriteCommits) +
		`git rev-list ` + unpushedRange + ` | while read -r c; do ` +
		// -z, then swap: a name may hold a newline, and awk cannot read NUL everywhere.
		`git ls-tree -r -l -z "$c" | tr '\n\000' '\001\n' | ` +
		fmt.Sprintf(`awk '{ i = index($0, "\t"); split(substr($0, 1, i - 1), a, " "); if (a[2] == "blob" && a[4] + 0 > %d) print "L " a[4] "\t" substr($0, i + 1) }'; `, LargeBytes) +
		`done`
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout, maxOutput: 8 << 20})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusConflict)
	}
	out := &LargeHistory{Files: []LargeCommitted{}}
	found := map[string]*LargeCommitted{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		switch {
		case strings.HasPrefix(line, "H "):
			out.Head = strings.TrimSpace(line[2:])
		case line == "D":
			out.Detached = true
		case strings.HasPrefix(line, "N "):
			out.Unpushed, _ = strconv.Atoi(strings.TrimSpace(line[2:]))
		case strings.HasPrefix(line, "L "):
			size, name, ok := strings.Cut(line[2:], "\t")
			if !ok {
				continue
			}
			name = strings.ReplaceAll(name, "\x01", "\n")
			n, _ := strconv.ParseInt(size, 10, 64)
			f := found[name]
			if f == nil {
				f = &LargeCommitted{Path: name}
				found[name] = f
			}
			f.Commits++
			if n > f.SizeBytes {
				f.SizeBytes = n
			}
		}
	}
	out.TooMany = out.Unpushed > maxRewriteCommits
	for _, f := range found {
		out.Files = append(out.Files, *f)
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	if len(out.Files) == 0 {
		return out, nil
	}
	if err := s.markPushed(ctx, r, out.Files); err != nil {
		return nil, err
	}
	return out, nil
}

// markPushed finds which paths also live in the commits right below the range.
func (s *Service) markPushed(ctx context.Context, r Repo, files []LargeCommitted) error {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Path)
	}
	script := prelude(true, true) +
		`git rev-list --boundary ` + unpushedRange + ` | sed -n 's/^-//p' | while read -r b; do ` +
		`git ls-tree -r -z --name-only "$b" -- ` + machine.Join(names...) + ` | tr '\n\000' '\001\n'; done`
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout, maxOutput: 1 << 20})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return failed(res, "git_failed", http.StatusConflict)
	}
	in := map[string]bool{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		in[strings.ReplaceAll(line, "\x01", "\n")] = true
	}
	for i := range files {
		files[i].InPushed = in[files[i].Path]
	}
	return nil
}

var hexHead = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// ignoreLines adds an anchored .gitignore line per path, once.
func ignoreLines(paths []string) string {
	script := `if [ -s .gitignore ] && [ -n "$(tail -c 1 .gitignore)" ]; then echo >> .gitignore; fi; `
	for _, p := range paths {
		l := machine.Quote(gitignoreLine(p))
		script += `grep -qxF -- ` + l + ` .gitignore 2>/dev/null || printf '%s\n' ` + l + ` >> .gitignore || exit 5; `
	}
	return script
}

// RemoveLargeFromHistory rewrites the unpushed commits without paths. head is
// the commit the owner looked at: if the branch moved since, nothing happens.
// Only paths a fresh scan finds over the limit are accepted, so a mistaken or
// forged call cannot take a normal file out of history.
func (s *Service) RemoveLargeFromHistory(ctx context.Context, r Repo, head string, paths []string) (*RewriteResult, error) {
	paths, err := cleanPaths(paths)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, newErr(http.StatusBadRequest, "no_paths", "choose the files")
	}
	if !hexHead.MatchString(head) {
		return nil, newErr(http.StatusBadRequest, "bad_ref", "that commit is not valid")
	}
	unlock, err := s.lock(r, "rewrite")
	if err != nil {
		return nil, err
	}
	defer unlock()

	scan, err := s.ScanLargeHistory(ctx, r)
	if err != nil {
		return nil, err
	}
	if scan.Head != head {
		return nil, newErr(http.StatusConflict, "head_moved", "the branch moved since this was checked")
	}
	if scan.TooMany {
		return nil, newErr(http.StatusConflict, "too_many_commits", "more than %d commits are not pushed", maxRewriteCommits)
	}
	large := map[string]bool{}
	for _, f := range scan.Files {
		large[f.Path] = true
	}
	var refused []string
	for _, p := range paths {
		if !large[p] {
			refused = append(refused, p)
		}
	}
	if len(refused) > 0 {
		return nil, &Error{Status: http.StatusConflict, Code: "not_large", Msg: "only files over 100 MB in commits that are not pushed can be removed", Paths: refused}
	}

	id := s.backupID()
	quoted := machine.Join(paths...)
	script := prelude(true, false) +
		fmt.Sprintf(`old=$(git rev-parse --verify -q HEAD); [ "$old" = %s ] || exit %d; `, machine.Quote(head), exitRewriteMoved) +
		fmt.Sprintf(`ref=$(git symbolic-ref -q HEAD) || exit %d; `, exitRewriteDetached) +
		`gd=$(git rev-parse --absolute-git-dir) || exit 5; ` +
		`for f in MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD BISECT_LOG rebase-merge rebase-apply; do ` +
		fmt.Sprintf(`[ ! -e "$gd/$f" ] || exit %d; done; `, exitRewriteBusy) +
		`tmp=$(mktemp -d) || exit 5; trap 'rm -rf "$tmp"' EXIT; mkdir "$tmp/map" || exit 5; ` +
		`git rev-list --reverse --topo-order ` + unpushedRange + ` > "$tmp/list" || exit 5; ` +
		fmt.Sprintf(`[ -s "$tmp/list" ] || exit %d; `, exitRewriteNone) +
		fmt.Sprintf(`[ "$(wc -l < "$tmp/list")" -le %d ] || exit %d; `, maxRewriteCommits, exitRewriteMany) +
		`rewritten=0; dropped=0; ` +
		`while read -r c; do ` +
		`GIT_INDEX_FILE="$tmp/idx" git read-tree "$c" || exit 5; ` +
		`GIT_INDEX_FILE="$tmp/idx" git update-index --force-remove -- ` + quoted + ` || exit 5; ` +
		`tree=$(GIT_INDEX_FILE="$tmp/idx" git write-tree) || exit 5; ` +
		`set --; only=; n=0; ` +
		`for p in $(git rev-list --parents -n 1 "$c" | cut -s -d' ' -f2-); do ` +
		`if [ -f "$tmp/map/$p" ]; then np=$(cat "$tmp/map/$p"); else np=$p; fi; ` +
		`set -- "$@" -p "$np"; only=$np; n=$((n+1)); done; ` +
		// A commit that held nothing but the removed file would stay as an empty one.
		`if [ "$n" = 1 ] && [ "$tree" = "$(git rev-parse "$only^{tree}")" ] && [ "$(git rev-parse "$c^{tree}")" != "$(git rev-parse "$c^^{tree}")" ]; then ` +
		`printf %s "$only" > "$tmp/map/$c" || exit 5; dropped=$((dropped+1)); continue; fi; ` +
		`git -c log.showSignature=false log -1 --date=raw --format='%an%n%ae%n%ad%n%cn%n%ce%n%cd' "$c" > "$tmp/who" || exit 5; ` +
		`{ IFS= read -r an; IFS= read -r ae; IFS= read -r ad; IFS= read -r cn; IFS= read -r ce; IFS= read -r cd; } < "$tmp/who"; ` +
		`new=$(git cat-file commit "$c" | sed '1,/^$/d' | ` +
		`GIT_AUTHOR_NAME="$an" GIT_AUTHOR_EMAIL="$ae" GIT_AUTHOR_DATE="@$ad" GIT_COMMITTER_NAME="$cn" GIT_COMMITTER_EMAIL="$ce" GIT_COMMITTER_DATE="@$cd" ` +
		`git commit-tree "$tree" "$@") || exit 5; ` +
		`printf %s "$new" > "$tmp/map/$c" || exit 5; rewritten=$((rewritten+1)); ` +
		`done < "$tmp/list"; ` +
		`new=$(cat "$tmp/map/$old") || exit 5; ` +
		// The backup comes first: without it the old commits would be gone from every ref.
		`git update-ref ` + machine.Quote(backupRef(id)) + ` "$old" || exit 5; ` +
		fmt.Sprintf(`git update-ref -m 'nextboard: remove large files from history' "$ref" "$new" "$old" || { git update-ref -d %s; exit %d; }; `, machine.Quote(backupRef(id)), exitRewriteRace) +
		`git update-index --force-remove -- ` + quoted + ` || exit 5; ` +
		ignoreLines(paths) +
		`echo "R $new $rewritten $dropped"`
	res, err := s.exec(ctx, r, script, execOpts{timeout: 2 * CommitTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, rewriteFailed(res)
	}
	out := &RewriteResult{Backup: id}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if strings.HasPrefix(line, "R ") {
			if f := strings.Fields(line); len(f) == 4 {
				out.Head = f[1]
				out.Rewritten, _ = strconv.Atoi(f[2])
				out.Dropped, _ = strconv.Atoi(f[3])
			}
		}
	}
	unlock()
	out.Status = s.refresh(r)
	return out, nil
}

// UndoLargeRemoval puts the branch back on the backup, as long as nothing was
// committed since. The file goes back into the index, so git sees no change, and
// the backup ref goes away. .gitignore keeps its line: a tracked file ignores it.
func (s *Service) UndoLargeRemoval(ctx context.Context, r Repo, id, head string, paths []string) (*Status, error) {
	paths, err := cleanPaths(paths)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, newErr(http.StatusBadRequest, "no_paths", "choose the files")
	}
	if !validBackupID(id) {
		return nil, newErr(http.StatusBadRequest, "bad_ref", "that backup is not valid")
	}
	if !hexHead.MatchString(head) {
		return nil, newErr(http.StatusBadRequest, "bad_ref", "that commit is not valid")
	}
	unlock, err := s.lock(r, "rewrite")
	if err != nil {
		return nil, err
	}
	defer unlock()
	bref := machine.Quote(backupRef(id))
	script := prelude(true, false) +
		fmt.Sprintf(`cur=$(git rev-parse --verify -q HEAD); [ "$cur" = %s ] || exit %d; `, machine.Quote(head), exitRewriteMoved) +
		fmt.Sprintf(`ref=$(git symbolic-ref -q HEAD) || exit %d; `, exitRewriteDetached) +
		fmt.Sprintf(`old=$(git rev-parse --verify -q %s^{commit}) || exit %d; `, machine.Quote(backupRef(id)), exitRewriteNoBackup) +
		fmt.Sprintf(`git update-ref -m 'nextboard: undo large file removal' "$ref" "$old" "$cur" || exit %d; `, exitRewriteRace) +
		`git reset -q "$old" -- ` + machine.Join(paths...) + ` >/dev/null || exit 5; ` +
		`git update-ref -d ` + bref + ` || exit 5`
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, rewriteFailed(res)
	}
	unlock()
	return s.refresh(r), nil
}

func rewriteFailed(res *machine.ExecResult) error {
	switch res.ExitCode {
	case exitRewriteMoved:
		return newErr(http.StatusConflict, "head_moved", "the branch moved since this was checked")
	case exitRewriteDetached:
		return newErr(http.StatusConflict, "detached", "not on a branch")
	case exitRewriteBusy:
		return newErr(http.StatusConflict, "operation_running", "finish or abort the merge, rebase, cherry-pick or bisect first")
	case exitRewriteNone:
		return newErr(http.StatusConflict, "nothing_unpushed", "every commit is already on a remote")
	case exitRewriteMany:
		return newErr(http.StatusConflict, "too_many_commits", "more than %d commits are not pushed", maxRewriteCommits)
	case exitRewriteRace:
		return newErr(http.StatusConflict, "head_moved", "the branch moved during the rewrite")
	case exitRewriteNoBackup:
		return newErr(http.StatusNotFound, "no_backup", "that backup does not exist any more")
	}
	return failed(res, "git_failed", http.StatusConflict)
}

func backupRef(id string) string { return "refs/nextboard/backup/" + id }

func (s *Service) backupID() string {
	return s.Now().UTC().Format("20060102-150405") + "-" + nonce()[:6]
}

var backupIDRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[A-Za-z0-9_-]{1,16}$`)

func validBackupID(id string) bool { return backupIDRe.MatchString(id) }
