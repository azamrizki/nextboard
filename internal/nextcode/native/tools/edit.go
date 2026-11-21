package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	snippetContext = 4  // lines shown around a change
	snippetMax     = 40 // the most lines a change's snippet shows
)

const editDescription = `Replaces exact text in a file: old_string becomes new_string.

- Read the file first, and copy old_string exactly as the file has it, with its indentation and whitespace, but without the line number and tab that Read shows before each line.
- old_string must occur exactly once, unless replace_all is set: include enough of the surrounding lines to make it unique. replace_all replaces every occurrence, for example to rename something throughout the file.
- new_string may be empty, to delete old_string. old_string may not be empty: create a file with Write.
- A file with CRLF line endings keeps them: write old_string and new_string with plain newlines.
- The result shows the changed lines, numbered as Read numbers them.`

const editSchema = `{
  "type": "object",
  "properties": {
    "file_path": {"type": "string", "description": "The absolute path of the file to change"},
    "old_string": {"type": "string", "description": "The exact text to replace"},
    "new_string": {"type": "string", "description": "The text to put in its place (different from old_string)"},
    "replace_all": {"type": "boolean", "default": false, "description": "Replace every occurrence of old_string (default false: it must occur once)"}
  },
  "required": ["file_path", "old_string", "new_string"],
  "additionalProperties": false
}`

func editTool() Tool { return newTool("Edit", KindEdit, editDescription, editSchema, runEdit) }

type editInput struct {
	FilePath   string  `json:"file_path"`
	OldString  *string `json:"old_string"`
	NewString  *string `json:"new_string"`
	ReplaceAll flag    `json:"replace_all"`
}

func runEdit(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in editInput
	if err := decode("Edit", input, &in); err != nil {
		return fail("%v", err)
	}
	path, err := pathArg(env, "Edit", "file_path: the file to change", in.FilePath)
	if err != nil {
		return fail("%v", err)
	}
	switch {
	case in.OldString == nil:
		return fail("Edit needs old_string: the exact text to replace")
	case in.NewString == nil:
		return fail(`Edit needs new_string: the text to put in its place ("" to delete old_string)`)
	case *in.OldString == "":
		return fail("old_string is empty: use Write to create a file, or give the exact text to replace")
	case *in.OldString == *in.NewString:
		return fail("old_string and new_string are the same: there is nothing to change")
	}
	st, err := os.Stat(path)
	if err != nil {
		return fail("%s", fileError(env, path, err))
	}
	if st.IsDir() {
		return fail("%s is a directory", path)
	}
	if irregular(st.Mode()) {
		return fail("%s is not a regular file (a device, a pipe or a socket)", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fail("%s", fileError(env, path, err))
	}
	if bytes.IndexByte(data[:min(len(data), sniffSize)], 0) >= 0 {
		return fail("%s is a binary file; Edit changes text files only", path)
	}
	text := string(data)
	old, new := *in.OldString, *in.NewString
	// a CRLF file is matched and changed with its own line endings
	if usesCRLF(text) {
		if o := toCRLF(old); strings.Contains(text, o) {
			old, new = o, toCRLF(new)
		}
	}
	n := strings.Count(text, old)
	if n == 0 {
		return fail("%s", notFound(path, text, *in.OldString))
	}
	if n > 1 && !in.ReplaceAll {
		return fail("%d matches of old_string in %s; give more context to make it unique, or set replace_all", n, path)
	}
	out, spans := replace(text, old, new, bool(in.ReplaceAll))
	if ctx.Err() != nil {
		return stopped()
	}
	if err := writeFile(path, []byte(out), st.Mode().Perm(), false); err != nil {
		return fail("Could not write %s: %v", path, err)
	}
	return ok(fmt.Sprintf("Edited %s: %s\n%s", path, plural(len(spans), "replacement", "replacements"), snippet(out, spans[0][0], spans[len(spans)-1][1])))
}

// usesCRLF is true when most of a text's line endings are CRLF.
func usesCRLF(s string) bool {
	crlf := strings.Count(s, "\r\n")
	return crlf > 0 && 2*crlf >= strings.Count(s, "\n")
}

// toCRLF gives every line ending of s as CRLF.
func toCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// replace replaces old with new in s, once or everywhere. spans are where the new text is in
// the result, as byte offsets.
func replace(s, old, new string, all bool) (string, [][2]int) {
	var b strings.Builder
	b.Grow(len(s) + len(new) - len(old))
	var spans [][2]int
	i := 0
	for {
		j := strings.Index(s[i:], old)
		if j < 0 {
			break
		}
		b.WriteString(s[i : i+j])
		start := b.Len()
		b.WriteString(new)
		spans = append(spans, [2]int{start, b.Len()})
		i += j + len(old)
		if !all {
			break
		}
	}
	b.WriteString(s[i:])
	return b.String(), spans
}

// snippet shows the changed region of an edited file, numbered as Read numbers it: from a few
// lines before the change's start to a few after its end, at most snippetMax lines.
func snippet(text string, start, end int) string {
	first := strings.Count(text[:start], "\n") // 0-based lines
	last := first
	if end > start {
		last = strings.Count(text[:end-1], "\n")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	from := max(0, first-snippetContext)
	to := min(len(lines)-1, last+snippetContext)
	cut := to-from+1 > snippetMax
	if cut {
		to = from + snippetMax - 1
	}
	var b strings.Builder
	for i := from; i <= to; i++ {
		numbered(&b, i+1, strings.TrimSuffix(lines[i], "\r"))
	}
	if cut {
		fmt.Fprintf(&b, "… the change goes on to line %d\n", last+1)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// readNumbers matches a line that starts the way Read numbers lines.
var readNumbers = regexp.MustCompile(`^\s*\d+\t`)

// notFound says that old_string is not in the file, with a hint when it nearly is: with other
// line endings, other trailing whitespace or other indentation, or with Read's line numbers in it.
func notFound(path, text, old string) string {
	msg := "old_string was not found in " + path
	lf := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	switch {
	case strings.Contains(lf(text), lf(old)):
		return msg + ". It would match with other line endings (CRLF and LF): copy the text as Read shows it."
	case strings.Contains(trimLines(lf(text), strings.TrimRight), trimLines(lf(old), strings.TrimRight)):
		return msg + ". It would match if trailing whitespace were ignored: the file's lines end with spaces or tabs that old_string lacks, or the other way round. Copy them exactly."
	case strings.Contains(trimLines(lf(text), strings.Trim), trimLines(lf(old), strings.Trim)):
		return msg + ". It would match if indentation were ignored: copy the leading spaces or tabs exactly as the file has them."
	case allLines(old, readNumbers.MatchString):
		return msg + ". old_string seems to hold Read's line numbers: leave out the number and the tab before each line."
	}
	return msg + ". It must match the file exactly, whitespace and indentation included; Read the file again if it may have changed."
}

// trimLines trims every line of s with trim (strings.TrimRight or strings.Trim) of spaces and tabs.
func trimLines(s string, trim func(string, string) string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = trim(l, " \t")
	}
	return strings.Join(lines, "\n")
}

func allLines(s string, f func(string) bool) bool {
	for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if !f(l) {
			return false
		}
	}
	return true
}
