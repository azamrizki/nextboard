package skills

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Frontmatter is the parsed YAML header of a SKILL.md or command file. Only
// the YAML subset skills use is understood: scalars (plain or quoted),
// folded/literal block scalars, inline [a, b] and "- item" lists; nested
// maps (metadata, hooks) are recorded by key only.
type Frontmatter struct {
	Keys   []string          // in file order
	Values map[string]string // scalars (lists joined with ", ")
	Lists  map[string][]string
	Body   string
	Has    bool // the file starts with a --- block
	// BodyLine is the 1-based line where the body starts.
	BodyLine int
}

var keyRe = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*:\s*(.*)$`)

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		q := s[0]
		s = s[1 : len(s)-1]
		if q == '\'' {
			s = strings.ReplaceAll(s, "''", "'")
		} else {
			s = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n").Replace(s)
		}
		return s
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// ParseFrontmatter splits content into frontmatter and body.
func ParseFrontmatter(content string) Frontmatter {
	fm := Frontmatter{Values: map[string]string{}, Lists: map[string][]string{}, Body: content, BodyLine: 1}
	content = strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return fm
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " ") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return fm
	}
	fm.Has = true
	fm.Body = strings.Join(lines[end+1:], "\n")
	fm.BodyLine = end + 2
	head := lines[1:end]
	for i := 0; i < len(head); i++ {
		l := head[i]
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") || l[0] == ' ' || l[0] == '\t' {
			continue
		}
		m := keyRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		k, v := m[1], strings.TrimSpace(m[2])
		if _, dup := fm.Values[k]; !dup {
			fm.Keys = append(fm.Keys, k)
		}
		// indented continuation lines
		var cont []string
		for i+1 < len(head) && (strings.TrimSpace(head[i+1]) == "" || head[i+1][0] == ' ' || head[i+1][0] == '\t') {
			i++
			cont = append(cont, head[i])
		}
		switch {
		case v == "|" || v == ">" || strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">"):
			var parts []string
			for _, c := range cont {
				parts = append(parts, strings.TrimSpace(c))
			}
			sep := " "
			if strings.HasPrefix(v, "|") {
				sep = "\n"
			}
			fm.Values[k] = strings.TrimSpace(strings.Join(parts, sep))
		case strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]"):
			var items []string
			for _, it := range strings.Split(v[1:len(v)-1], ",") {
				if it = unquote(it); it != "" {
					items = append(items, it)
				}
			}
			fm.Lists[k] = items
			fm.Values[k] = strings.Join(items, ", ")
		case v == "" && len(cont) > 0 && strings.HasPrefix(strings.TrimSpace(cont[0]), "- "):
			var items []string
			for _, c := range cont {
				if t := strings.TrimSpace(c); strings.HasPrefix(t, "- ") {
					items = append(items, unquote(t[2:]))
				}
			}
			fm.Lists[k] = items
			fm.Values[k] = strings.Join(items, ", ")
		case v == "" && len(cont) > 0:
			fm.Values[k] = "" // nested map (metadata, hooks)
		default:
			s := unquote(v)
			for _, c := range cont { // plain multi-line scalar
				s += " " + strings.TrimSpace(c)
			}
			fm.Values[k] = strings.TrimSpace(s)
		}
	}
	return fm
}

// Finding is one lint result.
type Finding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"` // error | warn | info
	Message  string `json:"message"`
}

// knownKeys are the frontmatter keys Claude Code understands for skills
// and commands.
var knownKeys = map[string]bool{
	"name": true, "description": true, "argument-hint": true, "allowed-tools": true, "model": true,
	"disable-model-invocation": true, "user-invocable": true, "context": true, "agent": true, "hooks": true,
	"license": true, "version": true, "metadata": true, "compatibility": true, "when_to_use": true, "when-to-use": true,
}

var nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// Lint limits.
const (
	MaxNameLen     = 64
	MaxDescLen     = 1024
	MaxDescPlusUse = 1536
	MaxBodyLines   = 500
)

// Lint checks a skill or command without AI. name is the folder name of a
// skill ("" for commands, where the file name is the name).
func Lint(content, kind, folderName string) []Finding {
	fm := ParseFrontmatter(content)
	out := []Finding{}
	add := func(rule, sev, msg string) { out = append(out, Finding{Rule: rule, Severity: sev, Message: msg}) }
	name := fm.Values["name"]
	if kind == "command" {
		if name == "" {
			name = folderName
		}
	} else {
		if !fm.Has {
			add("frontmatter-missing", "error", "SKILL.md needs a --- frontmatter block with name and description")
		}
		if name == "" {
			add("name-missing", "error", "frontmatter has no name")
		} else if folderName != "" && name != folderName {
			add("name-folder", "warn", "name \""+name+"\" differs from its folder \""+folderName+"\"")
		}
	}
	// The frontmatter name and the folder (how the skill is invoked) must
	// both be lowercase letters, digits and hyphens.
	for _, n := range []string{name, folderName} {
		if n != "" && (!nameRe.MatchString(n) || len(n) > MaxNameLen) {
			add("name-format", "warn", "name \""+n+"\" should be lowercase letters, digits and hyphens, at most 64 characters")
			break
		}
	}
	desc := fm.Values["description"]
	switch {
	case strings.TrimSpace(desc) == "":
		add("description-missing", "warn", "description is empty: Claude cannot tell when to use it")
	case utf8.RuneCountInString(desc) > MaxDescLen:
		add("description-length", "warn", "description is longer than 1024 characters")
	}
	if use := fm.Values["when_to_use"] + fm.Values["when-to-use"]; utf8.RuneCountInString(desc)+utf8.RuneCountInString(use) > MaxDescPlusUse {
		add("description-length", "warn", "description plus usage note is longer than 1536 characters")
	}
	body := strings.TrimSpace(fm.Body)
	if body == "" {
		add("body-empty", "warn", "the body is empty")
	} else if n := strings.Count(body, "\n") + 1; n > MaxBodyLines {
		add("body-length", "warn", "the body has more than 500 lines; move details into sibling files")
	}
	for _, k := range fm.Keys {
		if !knownKeys[k] {
			add("unknown-key", "warn", "unknown frontmatter key \""+k+"\"")
		}
	}
	for _, k := range fm.Keys {
		if v := fm.Values[k]; strings.ContainsAny(v, "<>") {
			add("angle-brackets", "warn", "\""+k+"\" contains < or >, which some uploads reject")
		}
	}
	return out
}
