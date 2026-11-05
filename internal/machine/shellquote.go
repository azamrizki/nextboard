package machine

import "strings"

// Quote returns s escaped for POSIX sh. Safe strings are returned as-is;
// anything else is wrapped in single quotes, with each embedded quote
// written as close-quote, escaped quote, reopen:
//
//	it's  ->  'it'\''s'
//
// Never build remote commands by raw concatenation; always Quote.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("-_./=:@,+%^", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join quotes each word and joins with spaces.
func Join(words ...string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = Quote(w)
	}
	return strings.Join(q, " ")
}

// cdExpr returns a shell fragment that changes to dir, keeping "~" and
// "~/x" relative to $HOME (quoted parts are never expanded).
func cdExpr(dir string) string {
	switch {
	case dir == "" || dir == "~":
		return `cd "$HOME"`
	case strings.HasPrefix(dir, "~/"):
		return `cd "$HOME"/` + Quote(dir[2:])
	default:
		return "cd -- " + Quote(dir)
	}
}
