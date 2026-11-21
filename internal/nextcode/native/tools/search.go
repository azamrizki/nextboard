package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// lookRg finds ripgrep, which Glob and Grep use when it is there; tests replace it to try the
// Go fallback.
var lookRg = func() (string, error) { return exec.LookPath("rg") }

// rg runs ripgrep (never through a shell) in dir and gives its output lines, at most max of
// them (0 = all). more is true when it stopped there. Exit status 1 is "nothing found"; 2 is an
// error unless some output came anyway (an unreadable file among readable ones).
func rg(ctx context.Context, bin, dir string, args []string, max int) (lines []string, more bool, err error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, append([]string{"--no-config"}, args...)...)
	cmd.Dir = dir
	stderr := &clip{headN: 2000}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	br := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, ok, rerr := nextLine(br, lineKeep)
		if rerr != nil || !ok {
			break
		}
		if max > 0 && len(lines) == max {
			more = true
			cancel()
			break
		}
		lines = append(lines, string(line))
	}
	werr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return nil, false, ctx.Err()
	case more || werr == nil:
		return lines, more, nil
	}
	var ee *exec.ExitError
	if errors.As(werr, &ee) && (ee.ExitCode() == 1 || ee.ExitCode() == 2 && len(lines) > 0) {
		return lines, false, nil
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return nil, false, errors.New(msg)
	}
	return nil, false, werr
}

// skipDir is a folder the Go fallback never walks into.
func skipDir(name string) bool { return name == ".git" || name == "node_modules" }

// walk calls f for every file under root, skipping .git and node_modules, with its path
// relative to root in slash form. Unreadable folders are passed over.
func walk(ctx context.Context, root string, f func(path, rel string) error) error {
	n := 0
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if n++; n%256 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		return f(p, filepath.ToSlash(rel))
	})
}

// globRegexp compiles a glob the way ripgrep's -g reads one (as a .gitignore line does): a
// pattern without a slash matches a file's name at any depth, one with a slash matches its
// path from the search folder. * and ? stay within a folder, ** spans folders, [...] is a
// class ([!...] the opposite one) and {a,b} a choice.
func globRegexp(glob string) (*regexp.Regexp, error) {
	g := strings.TrimPrefix(glob, "./")
	prefix := ""
	if strings.Contains(strings.TrimSuffix(g, "/"), "/") {
		g = strings.TrimPrefix(g, "/")
	} else {
		prefix = `(?:.*/)?`
	}
	body, err := globBody(g)
	if err != nil {
		return nil, fmt.Errorf("invalid glob %q: %v", glob, err)
	}
	return regexp.Compile("^" + prefix + body + "$")
}

func globBody(g string) (string, error) {
	var b strings.Builder
	depth := 0 // within {…}
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				lead := i == 0 || g[i-1] == '/'
				switch j := i + 2; {
				case lead && j < len(g) && g[j] == '/': // "**/": any folders, or none
					b.WriteString(`(?:.*/)?`)
					i = j
				case lead && j == len(g): // a last "**": everything below
					b.WriteString(`.*`)
					i = j - 1
				default: // "**" within a name is a "*"
					b.WriteString(`[^/]*`)
					i++
				}
				continue
			}
			b.WriteString(`[^/]*`)
		case '?':
			b.WriteString(`[^/]`)
		case '[':
			j := i + 1
			if j < len(g) && (g[j] == '!' || g[j] == '^') {
				j++
			}
			if j < len(g) && g[j] == ']' {
				j++
			}
			for j < len(g) && g[j] != ']' {
				j++
			}
			if j >= len(g) {
				b.WriteString(`\[`)
				continue
			}
			b.WriteString(globClass(g[i+1 : j]))
			i = j
		case '{':
			depth++
			b.WriteString(`(?:`)
		case '}':
			if depth == 0 {
				b.WriteString(`\}`)
				continue
			}
			depth--
			b.WriteString(`)`)
		case ',':
			if depth > 0 {
				b.WriteString(`|`)
			} else {
				b.WriteString(`,`)
			}
		case '\\':
			if i+1 < len(g) {
				i++
			}
			b.WriteString(regexp.QuoteMeta(g[i : i+1]))
		default:
			b.WriteString(regexp.QuoteMeta(g[i : i+1]))
		}
	}
	if depth > 0 {
		return "", errors.New("a { is not closed")
	}
	return b.String(), nil
}

// globClass is a glob's [...] class as a regexp class; it never matches a slash.
func globClass(class string) string {
	var b strings.Builder
	b.WriteString("[")
	if strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^") {
		b.WriteString("^/")
		class = class[1:]
	}
	for i := 0; i < len(class); i++ {
		switch c := class[i]; c {
		case '\\', '[', ']', '^':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("]")
	return b.String()
}

// newestFirst sorts paths by their modification time, the newest first (then by name).
func newestFirst(paths []string) {
	mod := make(map[string]int64, len(paths))
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			mod[p] = st.ModTime().UnixNano()
		}
	}
	sort.SliceStable(paths, func(i, j int) bool {
		if mod[paths[i]] != mod[paths[j]] {
			return mod[paths[i]] > mod[paths[j]]
		}
		return paths[i] < paths[j]
	})
}

// hasMeta is true for a path part with a glob character in it.
func hasMeta(s string) bool { return strings.ContainsAny(s, "*?[{\\") }
