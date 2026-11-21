package tools

import "strings"

// ReadOnlyCommand reports whether a Bash command only reads (ls, git status, rg, …): the
// permission modes let it run where they would ask for another command.
//
// It is conservative: true only when the whole command splits on &&, ||, ; and | into simple
// commands that each run an allowed program with arguments that cannot write, run another
// program or slip past this check. Whatever this small parser does not follow is false:
// expansions ($, backticks), subshells, background jobs, redirections that write, newlines,
// comments, a leading VAR=value (no allowed program has "=" in its name).
func ReadOnlyCommand(command string) bool {
	cmds, ok := splitCommand(command)
	if !ok {
		return false
	}
	for _, c := range cmds {
		if !readOnlySimple(c) {
			return false
		}
	}
	return true
}

// word is a word of a command with its quotes removed. glob is true when it held a wildcard or
// a brace outside quotes, which the shell would expand into words nobody checked.
type word struct {
	text   string
	glob   bool
	quoted bool
}

// splitCommand splits a command line into its simple commands, as the shell would. ok is false
// for anything it does not follow, and for an empty command anywhere ("ls &&").
func splitCommand(s string) (cmds [][]word, ok bool) {
	if strings.ContainsAny(s, "\n\r`\x00") {
		return nil, false
	}
	var (
		cur              []word
		w                strings.Builder
		in, glob, quoted bool // within a word, and what it held so far
	)
	endWord := func() {
		if in {
			cur = append(cur, word{w.String(), glob, quoted})
		}
		w.Reset()
		in, glob, quoted = false, false, false
	}
	endCmd := func() bool {
		endWord()
		if len(cur) == 0 {
			return false
		}
		cmds, cur = append(cmds, cur), nil
		return true
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '$', '(', ')':
			return nil, false
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			w.WriteString(s[i+1 : i+1+j])
			in, quoted = true, true
			i += j + 1
		case '"':
			j := i + 1
			for ; j < len(s) && s[j] != '"'; j++ {
				switch s[j] {
				case '$':
					return nil, false
				case '\\':
					if j+1 < len(s) && (s[j+1] == '"' || s[j+1] == '\\') {
						j++
					}
				}
				w.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, false
			}
			in, quoted = true, true
			i = j
		case '\\':
			if i+1 >= len(s) {
				return nil, false
			}
			i++
			w.WriteByte(s[i])
			in, quoted = true, true
		case ' ', '\t':
			endWord()
		case ';':
			if !endCmd() {
				return nil, false
			}
		case '|':
			if i+1 < len(s) && s[i+1] == '&' {
				return nil, false
			}
			if i+1 < len(s) && s[i+1] == '|' {
				i++
			}
			if !endCmd() {
				return nil, false
			}
		case '&':
			switch {
			case i+1 < len(s) && s[i+1] == '&':
				i++
				if !endCmd() {
					return nil, false
				}
			case i+1 < len(s) && s[i+1] == '>': // &>/dev/null
				endWord()
				end, ok := redirect(s, i+1)
				if !ok {
					return nil, false
				}
				i = end - 1
			default: // a background job
				return nil, false
			}
		case '>', '<':
			if in && !quoted && allDigits(w.String()) { // the descriptor of "2>"
				w.Reset()
				in, glob = false, false
			} else {
				endWord()
			}
			end, ok := redirect(s, i)
			if !ok {
				return nil, false
			}
			i = end - 1
		case '#':
			if !in { // a comment
				return nil, false
			}
			w.WriteByte(c)
		case '*', '?', '[', '{':
			w.WriteByte(c)
			in, glob = true, true
		default:
			w.WriteByte(c)
			in = true
		}
	}
	if !endCmd() {
		return nil, false
	}
	return cmds, true
}

// redirect reads the redirection at s[i] (a '>' or a '<') and gives the index after it. Only
// those that write nothing pass: into /dev/null, a copy of another descriptor (2>&1), and
// reading a file (<).
func redirect(s string, i int) (int, bool) {
	j := i + 1
	if s[i] == '<' {
		if j < len(s) && strings.IndexByte("<>&", s[j]) >= 0 {
			return 0, false
		}
		end, target, ok := redirectTarget(s, j)
		return end, ok && target != ""
	}
	switch {
	case j < len(s) && s[j] == '&': // >&N
		k := j + 1
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k == j+1 || k < len(s) && strings.IndexByte(" \t;&|", s[k]) < 0 {
			return 0, false
		}
		return k, true
	case j < len(s) && s[j] == '>':
		j++
	case j < len(s) && s[j] == '|':
		return 0, false
	}
	end, target, ok := redirectTarget(s, j)
	return end, ok && target == "/dev/null"
}

// redirectTarget reads the word a redirection goes to, plain characters only.
func redirectTarget(s string, i int) (end int, target string, ok bool) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	j := i
	for ; j < len(s) && strings.IndexByte(" \t;&|<>()", s[j]) < 0; j++ {
		if strings.IndexByte(`'"\$*?[{`, s[j]) >= 0 {
			return 0, "", false
		}
	}
	return j, s[i:j], true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// readOnlySimple is whether one simple command only reads: an allowed program, called by its
// bare name, with arguments its check lets through.
func readOnlySimple(ws []word) bool {
	prog := ws[0]
	check, ok := readOnlyPrograms[prog.text]
	if !ok || prog.glob {
		return false
	}
	args := make([]string, 0, len(ws)-1)
	for _, w := range ws[1:] {
		if w.glob && strictPrograms[prog.text] {
			return false
		}
		args = append(args, w.text)
	}
	return check(args)
}

// readOnlyPrograms are the programs a read-only command may run, each with the check of its
// arguments.
var readOnlyPrograms = map[string]func([]string) bool{
	"ls": anyArgs, "pwd": anyArgs, "cat": anyArgs, "head": anyArgs, "tail": anyArgs, "wc": anyArgs,
	"stat": anyArgs, "du": anyArgs, "df": anyArgs, "which": anyArgs, "type": anyArgs, "echo": anyArgs,
	"printf": anyArgs, "uname": anyArgs, "whoami": anyArgs, "id": anyArgs, "realpath": anyArgs,
	"basename": anyArgs, "dirname": anyArgs, "grep": anyArgs, "cut": anyArgs, "tr": anyArgs,
	"diff": anyArgs, "cmp": anyArgs, "jq": anyArgs, "column": anyArgs, "nl": anyArgs,
	"cd": anyArgs, "true": anyArgs, "false": anyArgs,
	"file": without("C", "--compile"), // -C writes a compiled magic file
	"date": without("s", "--set"),     // -s sets the clock
	"tree": without("oR", "--output"), // -o writes its listing to a file, -R writes 00Tree.html files
	"sort": without("o", "--output", "--compress-program"),
	"rg":   without("", "--pre", "--pre-glob", "--hostname-bin"), // these run other programs
	"ag":   without("", "--pager"),
	"uniq": uniqArgs,
	"find": findArgs,
	"git":  gitArgs,
	"go":   goArgs,
	"npm":  npmArgs,
	"node": versionOnly, "python": versionOnly, "python3": versionOnly,
}

// strictPrograms are those with options that write or run something: a wildcard or a brace
// in their arguments could expand into such an option (a file named "-delete"), so it is not
// let through.
var strictPrograms = map[string]bool{
	"find": true, "rg": true, "ag": true, "sort": true, "uniq": true, "tree": true, "file": true,
	"date": true, "git": true, "go": true, "npm": true, "node": true, "python": true, "python3": true,
}

func anyArgs([]string) bool { return true }

func versionOnly(args []string) bool { return len(args) == 1 && args[0] == "--version" }

// without is a check that refuses some options: a short one also within a cluster (-uo), a
// long one also with a value (--output=x) or abbreviated as GNU tools take it (--out).
func without(short string, long ...string) func([]string) bool {
	return func(args []string) bool {
		for _, a := range args {
			if hasOption(a, short, long) {
				return false
			}
		}
		return true
	}
}

func hasOption(a, short string, long []string) bool {
	if name, ok := strings.CutPrefix(a, "--"); ok {
		name, _, _ = strings.Cut(name, "=")
		for _, l := range long {
			if name != "" && strings.HasPrefix(strings.TrimPrefix(l, "--"), name) {
				return true
			}
		}
		return false
	}
	return len(a) > 1 && a[0] == '-' && strings.ContainsAny(a[1:], short)
}

// only is true when every argument is one of allowed.
func only(args []string, allowed ...string) bool {
	for _, a := range args {
		ok := false
		for _, x := range allowed {
			ok = ok || a == x
		}
		if !ok {
			return false
		}
	}
	return true
}

// findArgs refuses find's actions that run a program, delete or write a file.
func findArgs(args []string) bool {
	for _, a := range args {
		switch a {
		case "-exec", "-execdir", "-delete", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
			return false
		}
	}
	return true
}

// uniqArgs refuses a second file name, which uniq writes its output to.
func uniqArgs(args []string) bool {
	n := 0
	for _, a := range args {
		if a == "-" || !strings.HasPrefix(a, "-") {
			n++
		}
	}
	return n <= 1
}

// gitArgs lets through git's subcommands that only read, after -C <dir> or --no-pager.
func gitArgs(args []string) bool {
	i := 0
	for i < len(args) && (args[i] == "-C" || args[i] == "--no-pager" || args[i] == "-P") {
		if args[i] == "-C" {
			i++
		}
		i++
	}
	if i >= len(args) {
		return false
	}
	sub, rest := args[i], args[i+1:]
	for _, a := range rest {
		if hasOption(a, "", []string{"--output", "--ext-diff", "--open-files-in-pager"}) {
			return false
		}
	}
	switch sub {
	case "status", "diff", "log", "show", "rev-parse", "ls-files", "ls-tree", "blame", "describe", "shortlog":
		return true
	case "grep":
		for _, a := range rest {
			if strings.HasPrefix(a, "-O") { // opens the files in a pager it runs
				return false
			}
		}
		return true
	case "branch":
		return only(rest, "-a", "-r", "-v", "-vv", "--list", "--show-current")
	case "tag":
		return only(rest, "-l", "--list")
	case "remote":
		return len(rest) <= 1 && only(rest, "-v")
	case "config":
		if len(rest) == 0 {
			return false
		}
		switch rest[0] {
		case "--list", "-l":
			return len(rest) == 1
		case "--get", "--get-all":
			if len(rest) < 2 || len(rest) > 3 {
				return false
			}
			for _, a := range rest[1:] {
				if strings.HasPrefix(a, "-") {
					return false
				}
			}
			return true
		}
		return false
	case "stash":
		return len(rest) >= 1 && rest[0] == "list"
	}
	return false
}

// goArgs lets through go version, go env without -w or -u, and go list without the flags that
// run a program or may change go.mod.
func goArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "version":
		return true
	case "env":
		for _, a := range args[1:] {
			if n := goFlag(a); n == "w" || n == "u" {
				return false
			}
		}
		return true
	case "list":
		for _, a := range args[1:] {
			switch goFlag(a) {
			case "toolexec", "exec", "mod", "modfile", "overlay", "export", "compiled", "pgo":
				return false
			}
		}
		return true
	}
	return false
}

// goFlag is the name of a go command flag (-name, --name, -name=value); "" for an argument.
func goFlag(a string) string {
	if !strings.HasPrefix(a, "-") {
		return ""
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
	return name
}

// npmSafe are the options npm ls and npm view may take; others (such as --logs-dir or --cache)
// would have npm write somewhere.
var npmSafe = map[string]bool{
	"json": true, "all": true, "a": true, "depth": true, "long": true, "l": true, "parseable": true, "p": true,
	"global": true, "g": true, "omit": true, "include": true, "link": true, "prod": true, "production": true,
	"dev": true, "workspace": true, "workspaces": true, "w": true,
}

// npmArgs lets through npm --version, npm ls (list) and npm view with the options above.
func npmArgs(args []string) bool {
	if versionOnly(args) {
		return true
	}
	if len(args) == 0 || args[0] != "ls" && args[0] != "list" && args[0] != "view" {
		return false
	}
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-") && !npmSafe[goFlag(a)] {
			return false
		}
	}
	return true
}
