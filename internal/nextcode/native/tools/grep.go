package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	grepLimit   = 250       // lines or files one Grep shows when the call gives no head_limit
	grepMaxSize = 100 << 10 // about the most output one Grep shows, in bytes
	grepMaxFile = 32 << 20  // the fallback does not search a larger file
)

// Output modes.
const (
	grepFiles   = "files_with_matches"
	grepContent = "content"
	grepCount   = "count"
)

const grepDescription = `Searches file contents with a regular expression and lists the files that match, the matching lines, or counts per file. It is built on ripgrep.

- pattern is a regular expression (ripgrep's syntax): escape what is literal, "interface\\{\\}" to find interface{}, "\\.go" for ".go".
- path is a file or a folder to search: the working folder by default. Narrow it with glob ("*.ts", "*.{ts,tsx}", "src/**/*.go") or type ("go", "py", "js", "ts", "rust", "java", "md", "json", "yaml", …), which is faster than a broad search.
- output_mode: "files_with_matches" (the default: paths, the most recently changed first), "content" (the matching lines as path:line:text; -n line numbers are on by default, -A, -B and -C add lines of context) or "count" (matching lines per file).
- -i ignores case. multiline lets a match span lines, with . matching newlines too.
- head_limit caps the files or lines shown: 250 by default.
- .git is left out, and so are the files .gitignore ignores. Use Grep rather than grep or rg in Bash.`

const grepSchema = `{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "The regular expression to search file contents for"},
    "path": {"type": "string", "description": "The file or folder to search in (the working folder by default)"},
    "glob": {"type": "string", "description": "Only files matching this glob, e.g. \"*.js\" or \"*.{ts,tsx}\""},
    "type": {"type": "string", "description": "Only files of this type, e.g. go, py, js, ts, rust, java, md"},
    "output_mode": {"type": "string", "enum": ["content", "files_with_matches", "count"], "description": "What to show: matching lines (content), the paths of matching files (files_with_matches, the default) or counts per file (count)"},
    "-i": {"type": "boolean", "description": "Ignore case"},
    "-n": {"type": "boolean", "description": "Show line numbers (content mode; true by default)"},
    "-A": {"type": "number", "description": "Lines to show after each match (content mode)"},
    "-B": {"type": "number", "description": "Lines to show before each match (content mode)"},
    "-C": {"type": "number", "description": "Lines to show before and after each match (content mode)"},
    "head_limit": {"type": "number", "description": "Show only the first N lines or files (250 by default)"},
    "multiline": {"type": "boolean", "description": "Let a match span lines, with . matching newlines (default false)"}
  },
  "required": ["pattern"],
  "additionalProperties": false
}`

func grepTool() Tool { return newTool("Grep", KindRead, grepDescription, grepSchema, runGrep) }

type grepInput struct {
	Pattern    string  `json:"pattern"`
	Path       string  `json:"path"`
	Glob       string  `json:"glob"`
	Type       string  `json:"type"`
	OutputMode string  `json:"output_mode"`
	IgnoreCase flag    `json:"-i"`
	LineNums   *flag   `json:"-n"`
	After      *number `json:"-A"`
	Before     *number `json:"-B"`
	Context    *number `json:"-C"`
	HeadLimit  *number `json:"head_limit"`
	Multiline  flag    `json:"multiline"`
}

// grepQuery is a Grep call, checked and with its defaults.
type grepQuery struct {
	grepInput
	target        string // the file or folder searched, absolute
	mode          string
	lineNums      bool
	before, after int
	limit         int
}

type fileCount struct {
	path string
	n    int
}

// grepFound is what a search found, the same from ripgrep and from the fallback.
type grepFound struct {
	files  []string    // files_with_matches
	counts []fileCount // count
	lines  []string    // content
	more   bool        // content: it stopped at the limit
}

func runGrep(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in grepInput
	if err := decode("Grep", input, &in); err != nil {
		return fail("%v", err)
	}
	q, err := grepQueryOf(env, in)
	if err != nil {
		return fail("%v", err)
	}
	var found grepFound
	if bin, lerr := lookRg(); lerr == nil {
		found, err = rgGrep(ctx, bin, env, q)
	} else {
		found, err = goGrep(ctx, q)
	}
	if ctx.Err() != nil {
		return stopped()
	}
	if err != nil {
		return fail("%v", err)
	}
	return ok(grepOutput(q, found))
}

func grepQueryOf(env *Env, in grepInput) (grepQuery, error) {
	q := grepQuery{grepInput: in, mode: in.OutputMode, lineNums: true, limit: grepLimit}
	if in.Pattern == "" {
		return q, fmt.Errorf("Grep needs pattern: the regular expression to search for")
	}
	switch q.mode {
	case "":
		q.mode = grepFiles
	case grepFiles, grepContent, grepCount:
	default:
		return q, fmt.Errorf("output_mode must be content, files_with_matches or count, not %q", in.OutputMode)
	}
	target := env.Cwd
	if p := strings.TrimSpace(in.Path); p != "" && p != "undefined" && p != "null" {
		target = p
	}
	abs, err := pathArg(env, "Grep", "path", target)
	if err != nil {
		return q, err
	}
	if _, err := os.Stat(abs); err != nil {
		return q, fmt.Errorf("%s", strings.Replace(fileError(env, abs, err), "File does not exist", "Path does not exist", 1))
	}
	q.target = abs
	if in.LineNums != nil {
		q.lineNums = bool(*in.LineNums)
	}
	if in.Context != nil {
		q.before, q.after = max(0, int(*in.Context)), max(0, int(*in.Context))
	}
	if in.Before != nil {
		q.before = max(0, int(*in.Before))
	}
	if in.After != nil {
		q.after = max(0, int(*in.After))
	}
	if in.HeadLimit != nil && *in.HeadLimit > 0 {
		q.limit = int(*in.HeadLimit)
	}
	return q, nil
}

// rgGrep searches with ripgrep, its arguments built one by one and never through a shell.
func rgGrep(ctx context.Context, bin string, env *Env, q grepQuery) (grepFound, error) {
	args := []string{"--hidden", "-g", "!.git", "--color", "never"}
	switch q.mode {
	case grepFiles:
		args = append(args, "-l")
	case grepCount:
		args = append(args, "-c", "-H")
	case grepContent:
		args = append(args, "-H", "--no-heading")
		if q.lineNums {
			args = append(args, "-n")
		} else {
			args = append(args, "-N")
		}
		if q.before > 0 {
			args = append(args, "-B", strconv.Itoa(q.before))
		}
		if q.after > 0 {
			args = append(args, "-A", strconv.Itoa(q.after))
		}
	}
	if q.IgnoreCase {
		args = append(args, "-i")
	}
	if q.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}
	if q.Glob != "" {
		args = append(args, "-g", q.Glob)
	}
	if q.Type != "" {
		args = append(args, "-t", q.Type)
	}
	args = append(args, "-e", q.Pattern, "--", q.target)
	dir := env.Cwd
	if dir == "" {
		dir = filepath.Dir(q.target)
	}
	max := 0
	if q.mode == grepContent {
		max = q.limit
	}
	lines, more, err := rg(ctx, bin, dir, args, max)
	if err != nil {
		return grepFound{}, err
	}
	var f grepFound
	switch q.mode {
	case grepFiles:
		f.files = lines
	case grepCount:
		for _, l := range lines {
			i := strings.LastIndexByte(l, ':')
			n, err := strconv.Atoi(l[i+1:])
			if i < 0 || err != nil {
				continue
			}
			f.counts = append(f.counts, fileCount{l[:i], n})
		}
	case grepContent:
		f.lines, f.more = lines, more
	}
	return f, nil
}

// grepOutput is what the model reads of a search.
func grepOutput(q grepQuery, f grepFound) string {
	switch q.mode {
	case grepFiles:
		if len(f.files) == 0 {
			return "No matches found"
		}
		newestFirst(f.files)
		out := "Found " + plural(len(f.files), "file", "files") + "\n" + strings.Join(f.files[:min(len(f.files), q.limit)], "\n")
		if len(f.files) > q.limit {
			out += fmt.Sprintf("\n(%d of %d files shown, the newest first: raise head_limit or narrow the search)", q.limit, len(f.files))
		}
		return out
	case grepCount:
		if len(f.counts) == 0 {
			return "No matches found"
		}
		sort.Slice(f.counts, func(i, j int) bool { return f.counts[i].path < f.counts[j].path })
		var b strings.Builder
		total := 0
		for i, c := range f.counts {
			total += c.n
			if i < q.limit {
				fmt.Fprintf(&b, "%s:%d\n", c.path, c.n)
			}
		}
		if len(f.counts) > q.limit {
			fmt.Fprintf(&b, "(%d of %d files shown: raise head_limit or narrow the search)\n", q.limit, len(f.counts))
		}
		fmt.Fprintf(&b, "\nFound %s in %s.", plural(total, "matching line", "matching lines"), plural(len(f.counts), "file", "files"))
		return b.String()
	}
	if len(f.lines) == 0 {
		return "No matches found"
	}
	var b strings.Builder
	shown := 0
	for _, l := range f.lines {
		if b.Len() >= grepMaxSize {
			break
		}
		b.WriteString(cutLine(l, maxLineChars))
		b.WriteByte('\n')
		shown++
	}
	out := strings.TrimSuffix(b.String(), "\n")
	switch {
	case shown < len(f.lines):
		out += fmt.Sprintf("\n(the first %d lines shown, about 100 KB: narrow the search or use head_limit)", shown)
	case f.more:
		out += fmt.Sprintf("\n(the first %d lines shown: raise head_limit or narrow the search for more)", shown)
	}
	return out
}

// grepTypes are the file types the fallback knows, as ripgrep names them.
var grepTypes = map[string][]string{
	"go":       {"*.go"},
	"js":       {"*.js", "*.jsx", "*.mjs", "*.cjs", "*.vue"},
	"ts":       {"*.ts", "*.tsx", "*.mts", "*.cts"},
	"py":       {"*.py", "*.pyi"},
	"rust":     {"*.rs"},
	"java":     {"*.java"},
	"kotlin":   {"*.kt", "*.kts"},
	"swift":    {"*.swift"},
	"c":        {"*.c", "*.h"},
	"cpp":      {"*.cpp", "*.cc", "*.cxx", "*.hpp", "*.hh", "*.hxx", "*.h"},
	"cs":       {"*.cs"},
	"ruby":     {"*.rb", "Gemfile", "*.gemspec"},
	"php":      {"*.php"},
	"md":       {"*.md", "*.markdown", "*.mdx"},
	"markdown": {"*.md", "*.markdown", "*.mdx"},
	"json":     {"*.json", "*.jsonl"},
	"yaml":     {"*.yaml", "*.yml"},
	"toml":     {"*.toml"},
	"xml":      {"*.xml"},
	"css":      {"*.css", "*.scss", "*.sass", "*.less"},
	"html":     {"*.html", "*.htm", "*.xhtml"},
	"sh":       {"*.sh", "*.bash", "*.zsh"},
	"sql":      {"*.sql"},
	"txt":      {"*.txt"},
}

// goGrep is the search without ripgrep: Go's regexp (the same syntax family) over a walk that
// skips .git, node_modules and binary files.
func goGrep(ctx context.Context, q grepQuery) (grepFound, error) {
	flags := ""
	if q.IgnoreCase {
		flags += "i"
	}
	if q.Multiline {
		flags += "ms"
	}
	expr := q.Pattern
	if flags != "" {
		expr = "(?" + flags + ")" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return grepFound{}, fmt.Errorf("invalid regular expression: %v", err)
	}
	keep, err := grepFilter(q)
	if err != nil {
		return grepFound{}, err
	}
	var f grepFound
	search := func(path string) error {
		lines, matched := grepFile(path, re, bool(q.Multiline))
		if len(matched) == 0 {
			return nil
		}
		switch q.mode {
		case grepFiles:
			f.files = append(f.files, path)
		case grepCount:
			f.counts = append(f.counts, fileCount{path, len(matched)})
		case grepContent:
			if len(f.lines) > 0 && (q.before > 0 || q.after > 0) {
				f.lines = append(f.lines, "--")
			}
			f.lines = append(f.lines, contentLines(q, path, lines, matched)...)
			if len(f.lines) > q.limit {
				f.lines, f.more = f.lines[:q.limit], true
				return filepath.SkipAll
			}
		}
		return nil
	}
	if st, serr := os.Stat(q.target); serr == nil && !st.IsDir() {
		err = search(q.target) // a file given by path is searched whatever glob and type say
	} else {
		err = walk(ctx, q.target, func(path, rel string) error {
			if !keep(rel) {
				return nil
			}
			return search(path)
		})
	}
	if err == filepath.SkipAll { // the limit was reached
		err = nil
	}
	return f, err
}

// grepFilter is whether a file, by its path from the search folder, is searched: glob and type.
func grepFilter(q grepQuery) (func(rel string) bool, error) {
	var re *regexp.Regexp
	if q.Glob != "" {
		var err error
		if re, err = globRegexp(q.Glob); err != nil {
			return nil, err
		}
	}
	var types []string
	if q.Type != "" {
		var known bool
		if types, known = grepTypes[q.Type]; !known {
			names := make([]string, 0, len(grepTypes))
			for k := range grepTypes {
				names = append(names, k)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("unknown type %q; known: %s", q.Type, strings.Join(names, ", "))
		}
	}
	return func(rel string) bool {
		if re != nil && !re.MatchString(rel) {
			return false
		}
		if types == nil {
			return true
		}
		base := filepath.Base(rel)
		for _, t := range types {
			if m, _ := filepath.Match(t, base); m {
				return true
			}
		}
		return false
	}, nil
}

// grepFile gives a text file's lines and the (0-based) lines that match; nothing for a binary
// file, one too large or one it cannot read.
func grepFile(path string, re *regexp.Regexp, multiline bool) ([]string, []int) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > grepMaxFile {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.IndexByte(data[:min(len(data), sniffSize)], 0) >= 0 {
		return nil, nil
	}
	text := string(data)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	var matched []int
	if !multiline {
		for i, l := range lines {
			if re.MatchString(l) {
				matched = append(matched, i)
			}
		}
		return lines, matched
	}
	mark := map[int]bool{}
	for _, m := range re.FindAllStringIndex(text, -1) {
		first := strings.Count(text[:m[0]], "\n")
		last := first
		if m[1] > m[0] {
			last = strings.Count(text[:m[1]-1], "\n")
		}
		for i := first; i <= last && i < len(lines); i++ {
			mark[i] = true
		}
	}
	for i := range lines {
		if mark[i] {
			matched = append(matched, i)
		}
	}
	return lines, matched
}

// contentLines are a file's matching lines with their context, as ripgrep prints them: a match
// as path:N:text, context as path-N-text, and "--" between groups that do not touch.
func contentLines(q grepQuery, path string, lines []string, matched []int) []string {
	isMatch := make(map[int]bool, len(matched))
	for _, i := range matched {
		isMatch[i] = true
	}
	var out []string
	next := 0 // the first line not yet shown
	for k, i := range matched {
		from := max(i-q.before, next)
		if k > 0 && from > next {
			out = append(out, "--")
		}
		to := min(i+q.after, len(lines)-1)
		for j := from; j <= to; j++ {
			sep := "-"
			if isMatch[j] {
				sep = ":"
			}
			if q.lineNums {
				out = append(out, path+sep+strconv.Itoa(j+1)+sep+lines[j])
			} else {
				out = append(out, path+sep+lines[j])
			}
		}
		next = max(next, to+1)
	}
	return out
}
