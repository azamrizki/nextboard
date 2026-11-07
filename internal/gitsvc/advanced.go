package gitsvc

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Staging parts of a file, the stash, and what can be done with a commit of the history graph
// (docs/API-studio.md §3.2, §7.2). Git decides and refuses; a refusal comes back with its words.

// IndexFile is a path as the index holds it.
type IndexFile struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Binary bool   `json:"binary"`
	Text   string `json:"text"`
}

// ReadIndex returns what is staged for a path; a path not in the index has Exists=false.
func (s *Service) ReadIndex(ctx context.Context, r Repo, p string) (*IndexFile, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	q := machine.Quote(":" + p)
	script := prelude(true, true) + `git cat-file -e ` + q + ` 2>/dev/null || exit ` + strconv.Itoa(exitNoBlob) + `; git cat-file blob ` + q
	res, err := s.exec(ctx, r, script, execOpts{timeout: ReadTimeout, maxOutput: MaxShow})
	if err != nil {
		return nil, err
	}
	f := &IndexFile{Path: p}
	switch {
	case res.ExitCode == exitNoBlob:
		return f, nil
	case res.ExitCode != 0:
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	f.Exists = true
	if strings.IndexByte(string(res.Stdout[:min(len(res.Stdout), 8<<10)]), 0) >= 0 {
		f.Binary = true
		return f, nil
	}
	f.Text = string(res.Stdout)
	return f, nil
}

// Stage puts paths in the index as they are in the work tree. With text, one path takes exactly
// that text instead, which is how a part of a file gets staged.
func (s *Service) Stage(ctx context.Context, r Repo, paths []string, text *string) (*Status, error) {
	clean, err := cleanPaths(paths)
	if err != nil {
		return nil, err
	}
	if len(clean) == 0 {
		return nil, newErr(http.StatusBadRequest, "bad_path", "no path given")
	}
	unlock, err := s.lock(r, "stage")
	if err != nil {
		return nil, err
	}
	defer unlock()
	script := prelude(true, false)
	o := execOpts{timeout: CommitTimeout}
	if text != nil {
		if len(clean) != 1 {
			return nil, newErr(http.StatusBadRequest, "bad_path", "text works for one path only")
		}
		q := machine.Quote(clean[0])
		o.stdin = []byte(*text)
		// the file's mode stays, a new file is a plain one
		script += `m=$(git ls-files -s -- ` + q + ` | cut -d' ' -f1); [ -n "$m" ] || m=100644; ` +
			`b=$(git hash-object -w --stdin) && git update-index --add --cacheinfo "$m,$b,"` + q
	} else {
		script += `git add -A -- ` + machine.Join(clean...)
	}
	res, err := s.exec(ctx, r, script, o)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_refused", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}

// Unstage takes paths out of the index; the work tree stays as it is.
func (s *Service) Unstage(ctx context.Context, r Repo, paths []string) (*Status, error) {
	clean, err := cleanPaths(paths)
	if err != nil {
		return nil, err
	}
	if len(clean) == 0 {
		return nil, newErr(http.StatusBadRequest, "bad_path", "no path given")
	}
	unlock, err := s.lock(r, "unstage")
	if err != nil {
		return nil, err
	}
	defer unlock()
	j := machine.Join(clean...)
	// before the first commit there is no HEAD to go back to
	script := prelude(true, false) + `if git rev-parse -q --verify HEAD >/dev/null 2>&1; then git reset -q -- ` + j + `; else git rm -q --cached -f -- ` + j + `; fi`
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	// reset exits 1 when paths stay changed in the work tree on old versions of git
	if res.ExitCode != 0 && res.ExitCode != 1 {
		return nil, failed(res, "git_refused", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}

// Stash is one entry of the stash.
type Stash struct {
	Ref     string    `json:"ref"`
	Hash    string    `json:"hash"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

var stashRef = regexp.MustCompile(`^stash@\{[0-9]{1,5}\}$`)

// Stashes lists the stash, newest first.
func (s *Service) Stashes(ctx context.Context, r Repo) ([]Stash, error) {
	script := prelude(true, true) + `git rev-parse -q --verify refs/stash >/dev/null 2>&1 || exit 0; git stash list --no-color --format='%gd%x00%H%x00%gs%x00%aI%x00'`
	res, err := s.exec(ctx, r, script, execOpts{timeout: ReadTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_failed", http.StatusBadGateway)
	}
	items := []Stash{}
	f := strings.Split(string(res.Stdout), "\x00")
	for i := 0; i+3 < len(f); i += 4 {
		at, _ := time.Parse(time.RFC3339, strings.TrimSpace(f[i+3]))
		items = append(items, Stash{Ref: strings.TrimSpace(f[i]), Hash: f[i+1], Message: f[i+2], At: at.UTC()})
	}
	return items, nil
}

// StashSave stashes the changes, new files too when untracked is set.
func (s *Service) StashSave(ctx context.Context, r Repo, message string, untracked bool) (*Status, error) {
	if len(message) > maxMessage || strings.ContainsAny(message, "\x00\n") {
		return nil, newErr(http.StatusBadRequest, "bad_message", "invalid stash message")
	}
	unlock, err := s.lock(r, "stash")
	if err != nil {
		return nil, err
	}
	defer unlock()
	// stash cleans new files with the pathspec :/, which a literal reading would not find
	script := prelude(true, false) + `GIT_LITERAL_PATHSPECS=0 git stash push -q`
	if untracked {
		script += ` --include-untracked`
	}
	if message = strings.TrimSpace(message); message != "" {
		script += ` -m ` + machine.Quote(message)
	}
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_refused", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}

// StashAct applies, pops or drops a stash entry.
func (s *Service) StashAct(ctx context.Context, r Repo, action, ref string) (*Status, error) {
	if !stashRef.MatchString(ref) {
		return nil, newErr(http.StatusBadRequest, "bad_ref", "invalid stash")
	}
	if action != "apply" && action != "pop" && action != "drop" {
		return nil, newErr(http.StatusBadRequest, "bad_action", "invalid stash action")
	}
	unlock, err := s.lock(r, "stash")
	if err != nil {
		return nil, err
	}
	defer unlock()
	res, err := s.exec(ctx, r, prelude(true, false)+`GIT_LITERAL_PATHSPECS=0 git stash `+action+` -q `+machine.Quote(ref), execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		// a conflict leaves the markers in the files: the status shows them
		if strings.Contains(string(res.Stdout)+string(res.Stderr), "CONFLICT") {
			unlock()
			return s.refresh(r), nil
		}
		return nil, failed(res, "git_refused", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}

// CommitOp is an action on a commit of the history graph.
type CommitOp struct {
	Op  string `json:"op"` // checkout|cherry-pick|revert|merge|reset
	Ref string `json:"ref"`
	// Mode of a reset: soft, mixed or hard.
	Mode string `json:"mode"`
}

// DoCommitOp runs the action. A conflict is no failure: the status then shows the operation
// under way and the files to resolve.
func (s *Service) DoCommitOp(ctx context.Context, r Repo, op CommitOp) (*Status, error) {
	if !showRef.MatchString(op.Ref) {
		return nil, newErr(http.StatusBadRequest, "bad_ref", "invalid ref")
	}
	q := machine.Quote(op.Ref)
	var cmd string
	switch op.Op {
	case "checkout":
		cmd = `git checkout -q ` + q + ` --`
	case "cherry-pick":
		cmd = `git cherry-pick ` + q
	case "revert":
		cmd = `git revert --no-edit ` + q
	case "merge":
		cmd = `git merge --no-edit ` + q
	case "reset":
		if op.Mode != "soft" && op.Mode != "mixed" && op.Mode != "hard" {
			return nil, newErr(http.StatusBadRequest, "bad_mode", "invalid reset mode")
		}
		cmd = `git reset -q --` + op.Mode + ` ` + q + ` --`
	default:
		return nil, newErr(http.StatusBadRequest, "bad_action", "invalid action")
	}
	unlock, err := s.lock(r, op.Op)
	if err != nil {
		return nil, err
	}
	defer unlock()
	res, err := s.exec(ctx, r, prelude(true, false)+cmd, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 && !strings.Contains(string(res.Stdout)+string(res.Stderr), "CONFLICT") {
		if strings.Contains(string(res.Stderr), markNoIdent) || strings.Contains(string(res.Stderr), "Please tell me who you are") {
			return nil, newErr(http.StatusUnprocessableEntity, "git_identity", "git user.name / user.email is not set on this machine")
		}
		return nil, failed(res, "git_refused", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}

// CreateTag tags a commit; with a message the tag is annotated.
func (s *Service) CreateTag(ctx context.Context, r Repo, name, ref, message string) (*Status, error) {
	if !showRef.MatchString(ref) {
		return nil, newErr(http.StatusBadRequest, "bad_ref", "invalid ref")
	}
	if len(message) > maxMessage || strings.ContainsRune(message, 0) {
		return nil, newErr(http.StatusBadRequest, "bad_message", "invalid tag message")
	}
	unlock, err := s.lock(r, "tag")
	if err != nil {
		return nil, err
	}
	defer unlock()
	qn := machine.Quote(name)
	script := prelude(true, false) + `git check-ref-format "refs/tags/"` + qn + ` || exit 11; git rev-parse -q --verify "refs/tags/"` + qn + ` >/dev/null 2>&1 && exit 12; git tag `
	if message = strings.TrimSpace(message); message != "" {
		script += `-a -m ` + machine.Quote(message) + ` `
	}
	res, err := s.exec(ctx, r, script+`-- `+qn+` `+machine.Quote(ref), execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 0:
	case 11:
		return nil, newErr(http.StatusBadRequest, "bad_ref", "invalid tag name")
	case 12:
		return nil, newErr(http.StatusConflict, "exists", "tag %s already exists", name)
	default:
		return nil, failed(res, "git_refused", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}

// Operation aborts or continues the merge, cherry-pick or revert under way.
func (s *Service) Operation(ctx context.Context, r Repo, action string) (*Status, error) {
	if action != "abort" && action != "continue" {
		return nil, newErr(http.StatusBadRequest, "bad_action", "invalid action")
	}
	unlock, err := s.lock(r, "operation")
	if err != nil {
		return nil, err
	}
	defer unlock()
	script := prelude(true, false) +
		`if [ -f "$(git rev-parse --git-path MERGE_HEAD)" ]; then op=merge; ` +
		`elif [ -f "$(git rev-parse --git-path CHERRY_PICK_HEAD)" ]; then op=cherry-pick; ` +
		`elif [ -f "$(git rev-parse --git-path REVERT_HEAD)" ]; then op=revert; else exit 13; fi; `
	if action == "abort" {
		script += `git "$op" --abort`
	} else if action == "continue" {
		script += `if [ "$op" = merge ]; then git commit -q --no-edit; else git "$op" --continue; fi`
	}
	res, err := s.exec(ctx, r, script, execOpts{timeout: CommitTimeout})
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 13 {
		return nil, newErr(http.StatusConflict, "no_operation", "no operation is in progress")
	}
	if res.ExitCode != 0 {
		return nil, failed(res, "git_refused", http.StatusConflict)
	}
	unlock()
	return s.refresh(r), nil
}
