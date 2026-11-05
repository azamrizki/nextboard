package studio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Workbench helpers (docs/API-studio.md §7.2): the whole file list for "Go to
// file", search and replace in files, which tree entries git ignores. All of it
// runs on the project's machine (local or SSH) through machine.Exec; nothing is
// installed there.

// Limits of the workbench file operations.
const (
	FilesMax         = 200_000
	filesTimeout     = 30 * time.Second
	filesMaxOutput   = 64 << 20
	SearchLimit      = 5000
	SearchFileMax    = 1 << 20 // bigger files are skipped by search and replace
	searchTimeout    = 30 * time.Second
	searchMaxOutput  = 32 << 20
	searchTextMax    = 240 // characters of a result line sent to the client
	searchTextBefore = 60
	replaceMaxFiles  = 2000
	ignoredMaxPaths  = 5000
	ignoredTimeout   = 10 * time.Second
	filesDeletedMark = "//deleted//"
	filesRepoMark    = "//repo//"
	filesWalkMark    = "//walk//"
	// How far the list looks for repositories below a folder that is none: the Git panel's limits.
	filesRepoDepth = 5
	filesRepoMax   = 50
)

// ErrBadPattern is an invalid search regular expression.
var ErrBadPattern = errors.New("pola pencarian tidak valid")

// gitSafe keeps git commands from running code out of a repo's own config:
// opening a project must never execute anything from it.
const gitSafe = `git -c core.fsmonitor=false -c core.hooksPath=/dev/null `

func execScriptIn(ctx context.Context, h machine.Host, root, script string, stdin []byte, timeout time.Duration, maxOutput int64) (*machine.ExecResult, error) {
	// Always /bin/sh: the login shell on SSH machines may be zsh.
	return machine.Exec(ctx, h, machine.ExecSpec{Cwd: root, Argv: []string{"/bin/sh", "-c", script}, Stdin: stdin, Timeout: timeout, MaxOutput: maxOutput})
}

// pruneFindArgs builds the -name list of find from the tree's prune rules.
func pruneFindArgs() string {
	var parts []string
	for name := range PruneDirs {
		parts = append(parts, "-name "+machine.Quote(name))
	}
	return strings.Join(parts, " -o ")
}

// FileList is GET /api/projects/{id}/files.
type FileList struct {
	Items     []string `json:"items"`
	Truncated bool     `json:"truncated"`
	Source    string   `json:"source"` // "git" (ignored files left out) or "walk"
}

// walkScript lists a folder that is not a work tree: each repository below it
// through its own git, which knows its .gitignore, and the rest with find. A
// stray .git folder is no repository, so its folder stays in the walk. A folder
// that cannot be read costs only itself: find's exit code says nothing else.
func walkScript() string {
	repos := fmt.Sprintf(`n=$#; c=0; for f do d=${f%%/.git}; [ "$c" -lt %d ] || break; `, filesRepoMax) +
		`( cd "$d" 2>/dev/null && [ "$(` + gitSafe + `rev-parse --show-toplevel 2>/dev/null)" = "$(pwd -P)" ] ) || continue; ` +
		`c=$((c+1)); printf '` + filesRepoMark + `\0%s\0' "$d"; ` +
		`( cd "$d" && ` + gitSafe + `ls-files -co --exclude-standard -z; printf '` + filesDeletedMark + `\0'; ` + gitSafe + `ls-files -d -z ); ` +
		`set -- "$@" -o -path "$d"; done; shift "$n"; ` +
		`printf '` + filesWalkMark + `\0'; find . \( -name .nb-none "$@" -o ` + pruneFindArgs() + ` \) -prune -o -type f -print0 2>/dev/null; exit 0`
	// The extra name makes every xargs run the script, also those that skip an empty input.
	return fmt.Sprintf(`printf 'walk\0'; { find . -maxdepth %d \( -name .git -prune -print0 \) -o \( %s \) -prune 2>/dev/null; printf './.nb-none/.git\0'; } | xargs -0 sh -c %s sh`,
		filesRepoDepth, pruneFindArgs(), machine.Quote(repos))
}

// ListFiles returns every file below root relative to it. In a git work tree
// gitignored files are left out, and in the repositories below any other folder
// too; everywhere else the tree's prune folders are.
func ListFiles(ctx context.Context, h machine.Host, root string) (*FileList, error) {
	script := `export GIT_OPTIONAL_LOCKS=0; if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then printf 'git\0'; ` +
		gitSafe + `ls-files -co --exclude-standard -z; printf '` + filesDeletedMark + `\0'; ` + gitSafe + `ls-files -d -z; ` +
		`else ` + walkScript() + `; fi`
	res, err := execScriptIn(ctx, h, root, script, nil, filesTimeout, filesMaxOutput)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 && !res.TimedOut && !res.Truncated {
		return nil, fmt.Errorf("daftar file gagal: %s", strings.TrimSpace(string(res.Stderr)))
	}
	return parseFileList(res.Stdout, res.TimedOut || res.Truncated), nil
}

func parseFileList(out []byte, truncated bool) *FileList {
	fl := &FileList{Items: []string{}, Truncated: truncated}
	parts := bytes.Split(out, []byte{0})
	if len(parts) == 0 {
		return fl
	}
	fl.Source = string(parts[0])
	rest := parts[1:]
	deleted := map[string]bool{}
	names := make([]string, 0, len(rest))
	prefix, isDeleted := "", false
	for i := 0; i < len(rest); i++ {
		s := string(rest[i])
		switch s {
		case filesDeletedMark:
			isDeleted = true
			continue
		case filesWalkMark:
			prefix, isDeleted = "", false
			continue
		case filesRepoMark:
			i++
			if i < len(rest) {
				prefix, isDeleted = strings.TrimPrefix(string(rest[i]), "./")+"/", false
			}
			continue
		}
		s = prefix + strings.TrimPrefix(s, "./")
		if isDeleted {
			deleted[s] = true
		} else {
			names = append(names, s)
		}
	}
	seen := make(map[string]bool, len(names))
	for _, s := range names {
		// A name ending in a slash is a repository inside a repository, not a file.
		if s == "" || strings.HasSuffix(s, "/") || seen[s] || deleted[s] ||
			strings.HasPrefix(s, ".nextboard-trash/") || strings.Contains(s, "/.nextboard-trash/") {
			continue
		}
		seen[s] = true
		if len(fl.Items) >= FilesMax {
			fl.Truncated = true
			break
		}
		fl.Items = append(fl.Items, s)
	}
	return fl
}

// SearchOptions is the query of search and replace in files.
type SearchOptions struct {
	Query     string `json:"query"`
	MatchCase bool   `json:"matchCase"`
	WholeWord bool   `json:"wholeWord"`
	Regex     bool   `json:"regex"`
	// Include and Exclude are comma separated globs (*.ts, src/**, vendor).
	Include string `json:"include"`
	Exclude string `json:"exclude"`
	Limit   int    `json:"limit"`
}

// SearchMatch is one match. Col and Len count UTF-16 code units (editor
// columns, 0-based); TextCol is where the match starts inside Text, a clipped
// copy of the line.
type SearchMatch struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Len     int    `json:"len"`
	Text    string `json:"text"`
	TextCol int    `json:"textCol"`
}

// SearchFile groups the matches of one file.
type SearchFile struct {
	Rel     string        `json:"rel"`
	Matches []SearchMatch `json:"matches"`
}

// SearchResult is POST /api/projects/{id}/files/search.
type SearchResult struct {
	Files     []SearchFile `json:"files"`
	Count     int          `json:"count"`
	Truncated bool         `json:"truncated"`
	Engine    string       `json:"engine"`
}

// compileSearch builds the regular expression that locates matches in a line.
func compileSearch(o SearchOptions) (*regexp.Regexp, error) {
	if o.Query == "" {
		return nil, fmt.Errorf("%w: kosong", ErrBadPattern)
	}
	expr := o.Query
	if !o.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	if o.WholeWord {
		expr = `\b(?:` + expr + `)\b`
	}
	if !o.MatchCase {
		expr = `(?i)` + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadPattern, err)
	}
	return re, nil
}

// searchScript picks the best tool on the machine: ripgrep, git grep inside a
// work tree, plain grep. Each prints "path NUL line:text"; the first line of
// stdout names the engine.
func searchScript(o SearchOptions) string {
	q := machine.Quote(o.Query)
	var rg, gg, gr []string
	rg = append(rg, "rg", "--null", "--line-number", "--no-heading", "--color=never", "--no-messages", "--hidden", "--max-filesize", "1M", "-g", "'!.git'")
	gg = append(gg, gitSafe+"grep", "-z", "-n", "-I", "--untracked", "--exclude-standard")
	gr = append(gr, "grep", "-rnIs", "--null", "--exclude-dir=.git")
	for name := range PruneDirs {
		gr = append(gr, "--exclude-dir="+machine.Quote(name))
	}
	if o.Regex {
		gg = append(gg, "-E")
		gr = append(gr, "-E")
	} else {
		rg = append(rg, "-F")
		gg = append(gg, "-F")
		gr = append(gr, "-F")
	}
	if !o.MatchCase {
		rg = append(rg, "-i")
		gg = append(gg, "-i")
		gr = append(gr, "-i")
	}
	if o.WholeWord {
		rg = append(rg, "-w")
		gg = append(gg, "-w")
		gr = append(gr, "-w")
	}
	rg = append(rg, "-e", q)
	gg = append(gg, "-e", q)
	gr = append(gr, "-e", q, ".")
	// Outside a work tree there is no .gitignore: the tree's prune folders stand in.
	var rgPrune []string
	for name := range PruneDirs {
		rgPrune = append(rgPrune, "-g", machine.Quote("!"+name))
	}
	inGit := `git rev-parse --is-inside-work-tree >/dev/null 2>&1`
	return `export GIT_OPTIONAL_LOCKS=0; ` +
		`if command -v rg >/dev/null 2>&1; then printf 'rg\n'; ` +
		`if ` + inGit + `; then exec ` + strings.Join(rg, " ") + ` ./; else exec ` + strings.Join(rg, " ") + ` ` + strings.Join(rgPrune, " ") + ` ./; fi; ` +
		`elif ` + inGit + `; then printf 'git\n'; exec ` + strings.Join(gg, " ") + `; ` +
		`else printf 'grep\n'; exec ` + strings.Join(gr, " ") + `; fi`
}

// Search looks for o.Query in the project's text files. Binary files, files
// over 1 MB (with ripgrep) and ignored files are skipped.
func Search(ctx context.Context, h machine.Host, root string, o SearchOptions) (*SearchResult, error) {
	re, err := compileSearch(o)
	if err != nil {
		return nil, err
	}
	if o.Limit <= 0 || o.Limit > SearchLimit {
		o.Limit = SearchLimit
	}
	res, err := execScriptIn(ctx, h, root, searchScript(o), nil, searchTimeout, searchMaxOutput)
	if err != nil {
		return nil, err
	}
	// Tools exit 1 for "no match" and 2 (git grep: 128) for a bad pattern or
	// usage; a partial run is still useful. They also exit 2 for a folder they
	// could not read, but rg --no-messages and grep -s say nothing about that:
	// silence means nothing matched in what could be read.
	msg := strings.TrimSpace(string(res.Stderr))
	if res.ExitCode > 1 && msg != "" && !res.TimedOut && !res.Truncated && len(bytes.TrimSpace(res.Stdout)) <= 5 {
		if o.Regex {
			return nil, fmt.Errorf("%w: %s", ErrBadPattern, msg)
		}
		return nil, fmt.Errorf("pencarian gagal: %s", msg)
	}
	out := parseSearch(res.Stdout, re, o)
	out.Truncated = out.Truncated || res.TimedOut || res.Truncated
	return out, nil
}

// parseSearch turns tool output into results, finding the match columns itself
// so that every engine reports them the same way.
func parseSearch(out []byte, re *regexp.Regexp, o SearchOptions) *SearchResult {
	r := &SearchResult{Files: []SearchFile{}}
	engine, rest, ok := bytes.Cut(out, []byte{'\n'})
	if !ok {
		return r
	}
	r.Engine = string(engine)
	include, exclude := splitGlobs(o.Include), splitGlobs(o.Exclude)
	index := map[string]int{}
	for len(rest) > 0 {
		var line []byte
		line, rest, _ = bytes.Cut(rest, []byte{'\n'})
		name, tail, ok := bytes.Cut(line, []byte{0})
		if !ok {
			continue
		}
		num, text, ok := cutLineNumber(tail)
		if !ok {
			continue
		}
		rel := strings.TrimPrefix(string(name), "./")
		if !wantedPath(rel, include, exclude) {
			continue
		}
		s := strings.ToValidUTF8(strings.TrimSuffix(string(text), "\r"), "�")
		for _, loc := range re.FindAllStringIndex(s, -1) {
			if loc[0] == loc[1] {
				continue
			}
			if r.Count >= o.Limit {
				r.Truncated = true
				return r
			}
			i, seen := index[rel]
			if !seen {
				i = len(r.Files)
				index[rel] = i
				r.Files = append(r.Files, SearchFile{Rel: rel})
			}
			r.Files[i].Matches = append(r.Files[i].Matches, makeMatch(num, s, loc))
			r.Count++
		}
	}
	return r
}

// cutLineNumber splits "12:text" (git grep may also use NUL as the separator).
func cutLineNumber(b []byte) (int, []byte, bool) {
	i := 0
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	if i == 0 || i == len(b) || (b[i] != ':' && b[i] != 0) {
		return 0, nil, false
	}
	n, err := strconv.Atoi(string(b[:i]))
	if err != nil {
		return 0, nil, false
	}
	return n, b[i+1:], true
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func makeMatch(line int, s string, loc []int) SearchMatch {
	m := SearchMatch{Line: line, Col: utf16Len(s[:loc[0]]), Len: utf16Len(s[loc[0]:loc[1]])}
	before := []rune(s[:loc[0]])
	start := 0
	if len(before) > searchTextBefore {
		start = len(before) - searchTextBefore
	}
	after := []rune(s[loc[0]:])
	take := min(len(after), searchTextMax-(len(before)-start))
	prefix := string(before[start:])
	m.Text = strings.TrimRight(prefix+string(after[:take]), " \t")
	m.TextCol = utf16Len(prefix)
	return m
}

func splitGlobs(s string) []string {
	var out []string
	depth := 0
	cur := strings.Builder{}
	flush := func() {
		if g := strings.TrimSpace(cur.String()); g != "" {
			out = append(out, g)
		}
		cur.Reset()
	}
	for _, c := range s {
		switch {
		case c == '{':
			depth++
		case c == '}' && depth > 0:
			depth--
		case c == ',' && depth == 0:
			flush()
			continue
		}
		cur.WriteRune(c)
	}
	flush()
	return out
}

func wantedPath(rel string, include, exclude []string) bool {
	if rel == ".git" || strings.HasPrefix(rel, ".git/") || strings.HasPrefix(rel, ".nextboard-trash/") {
		return false
	}
	for _, g := range exclude {
		if matchFolderGlob(g, rel) {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, g := range include {
		if matchFolderGlob(g, rel) {
			return true
		}
	}
	return false
}

// matchFolderGlob is MatchGlob where a name without a slash also matches a
// folder anywhere on the path, and a pattern matches everything below itself.
func matchFolderGlob(g, rel string) bool {
	g = strings.TrimPrefix(strings.TrimSuffix(g, "/"), "./")
	if !strings.Contains(g, "/") {
		for _, seg := range strings.Split(rel, "/") {
			if MatchGlob(g, seg) {
				return true
			}
		}
		return false
	}
	return MatchGlob(g, rel) || MatchGlob(g+"/**", rel)
}

// MatchGlob matches rel against a glob: *, ?, ** and {a,b}. A pattern without a
// slash matches the file name, one with a slash matches from the project folder.
func MatchGlob(pattern, rel string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	if !strings.Contains(pattern, "/") {
		rel = path.Base(rel)
	}
	re, err := regexp.Compile("^" + globRegexp(pattern) + "$")
	return err == nil && re.MatchString(rel)
}

func globRegexp(g string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case c == '*' && strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '{':
			depth++
			b.WriteString("(?:")
		case c == '}' && depth > 0:
			depth--
			b.WriteString(")")
		case c == ',' && depth > 0:
			b.WriteString("|")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	for ; depth > 0; depth-- {
		b.WriteString(")")
	}
	return b.String()
}

// ReplaceFile is the outcome for one file of a replace.
type ReplaceFile struct {
	Rel   string `json:"rel"`
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`
}

// ReplaceResult is POST /api/projects/{id}/files/replace.
type ReplaceResult struct {
	Files []ReplaceFile `json:"files"`
	Count int           `json:"count"`
}

// RelInside resolves a client-sent relative path below root, refusing anything
// that climbs out of it or touches .git.
func RelInside(root, rel string) (string, bool) {
	c := path.Clean(rel)
	if rel == "" || strings.HasPrefix(rel, "/") || c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	for _, seg := range strings.Split(c, "/") {
		if seg == ".git" {
			return "", false
		}
	}
	return path.Join(root, c), true
}

// Replace applies the replacement in the given files (relative paths), bytes
// other than the matches untouched, so line endings and a BOM survive. A file
// that changed since it was read fails on its own; allowed vets each write.
func Replace(ctx context.Context, h machine.Host, root string, o SearchOptions, replacement string, rels []string, allowed func(abs string) error) (*ReplaceResult, error) {
	re, err := compileSearch(o)
	if err != nil {
		return nil, err
	}
	if len(rels) > replaceMaxFiles {
		return nil, fmt.Errorf("maksimal %d file sekaligus", replaceMaxFiles)
	}
	out := &ReplaceResult{Files: []ReplaceFile{}}
	for _, rel := range rels {
		f := ReplaceFile{Rel: rel}
		f.Count, err = replaceOne(ctx, h, root, rel, re, o.Regex, replacement, allowed)
		if err != nil {
			f.Error = err.Error()
		}
		out.Count += f.Count
		out.Files = append(out.Files, f)
	}
	return out, nil
}

func replaceOne(ctx context.Context, h machine.Host, root, rel string, re *regexp.Regexp, regex bool, replacement string, allowed func(string) error) (int, error) {
	abs, ok := RelInside(root, rel)
	if !ok {
		return 0, ErrOutsideProject
	}
	if allowed != nil {
		if err := allowed(abs); err != nil {
			return 0, err
		}
	}
	fc, err := h.ReadFile(ctx, abs, SearchFileMax+1)
	if err != nil {
		return 0, err
	}
	if fc.Binary || fc.Truncated {
		return 0, errors.New("file biner atau lebih besar dari 1 MB dilewati")
	}
	n := len(re.FindAllStringIndex(fc.Content, -1))
	if n == 0 {
		return 0, nil
	}
	var next string
	if regex {
		next = re.ReplaceAllString(fc.Content, replacement)
	} else {
		next = re.ReplaceAllLiteralString(fc.Content, replacement)
	}
	mtime := fc.Mtime
	if _, err := h.WriteFile(ctx, abs, []byte(next), machine.WriteOptions{IfMtime: &mtime}); err != nil {
		return 0, err
	}
	return n, nil
}

// Ignored returns the subset of rels (relative paths, folders too) that git
// ignores. Outside a git work tree nothing is ignored.
func Ignored(ctx context.Context, h machine.Host, root string, rels []string) ([]string, error) {
	if len(rels) == 0 {
		return []string{}, nil
	}
	if len(rels) > ignoredMaxPaths {
		rels = rels[:ignoredMaxPaths]
	}
	stdin := []byte(strings.Join(rels, "\x00") + "\x00")
	script := `export GIT_OPTIONAL_LOCKS=0; ` + gitSafe + `check-ignore --stdin -z 2>/dev/null; exit 0`
	res, err := execScriptIn(ctx, h, root, script, stdin, ignoredTimeout, 8<<20)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, p := range bytes.Split(res.Stdout, []byte{0}) {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out, nil
}

// OnSaveRule is one "run a command when a file is saved" rule.
type OnSaveRule struct {
	Pattern string
	Command string
}

// OnSaveRun is the outcome of one rule.
type OnSaveRun struct {
	Pattern  string `json:"pattern"`
	Command  string `json:"command"`
	ExitCode int    `json:"exitCode"`
	TimedOut bool   `json:"timedOut"`
	Output   string `json:"output"`
}

const (
	onSaveTimeout   = 60 * time.Second
	onSaveMaxOutput = 64 << 10
)

// RunOnSave runs the matching rules for the saved file, in order, in the
// project folder. {file} in a command is the saved file's full path. The first
// failure stops the rest.
func RunOnSave(ctx context.Context, h machine.Host, root, rel string, rules []OnSaveRule) ([]OnSaveRun, error) {
	abs, ok := RelInside(root, rel)
	if !ok {
		return nil, ErrOutsideProject
	}
	out := []OnSaveRun{}
	for _, r := range rules {
		if !MatchGlob(r.Pattern, rel) {
			continue
		}
		cmd := strings.ReplaceAll(r.Command, "{file}", machine.Quote(abs))
		res, err := machine.Exec(ctx, h, machine.ExecSpec{Cwd: root, Argv: []string{"/bin/sh", "-c", cmd}, Timeout: onSaveTimeout, MaxOutput: onSaveMaxOutput})
		if err != nil {
			return out, err
		}
		run := OnSaveRun{Pattern: r.Pattern, Command: r.Command, ExitCode: res.ExitCode, TimedOut: res.TimedOut,
			Output: strings.ToValidUTF8(string(res.Stdout)+string(res.Stderr), "�")}
		out = append(out, run)
		if res.ExitCode != 0 || res.TimedOut {
			break
		}
	}
	return out, nil
}
