package vault

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// Censor hides secret values in what a command printed, before an agent reads it. It knows the
// value itself and the shapes a program commonly prints it in. It is a guard against accidents,
// such as a tool that echoes its config: a command written to leak, say by reversing the value,
// gets past it, and that is why the owner sees the command before it runs.
type Censor struct {
	forms    []string
	replacer *strings.Replacer
}

// Hidden is what stands where a secret was.
const Hidden = "••••"

// MinCensored is the shortest value a censor can hide without wiping out ordinary output.
const MinCensored = 4

// minLine is the shortest line of a multi-line value hidden on its own, like a line of a key.
const minLine = 8

// NewCensor prepares to hide these values.
func NewCensor(values []string) *Censor {
	seen := map[string]bool{}
	var forms []string
	add := func(f string, min int) {
		if len(f) >= min && !seen[f] {
			seen[f] = true
			forms = append(forms, f)
		}
	}
	for _, v := range values {
		if len(v) < MinCensored {
			continue
		}
		add(v, MinCensored)
		if t := strings.TrimRight(v, "\r\n"); t != v {
			add(t, MinCensored)
		}
		if strings.Contains(v, "\n") {
			for line := range strings.SplitSeq(v, "\n") {
				add(strings.TrimSpace(line), minLine)
			}
		}
		b := []byte(v)
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			add(enc.EncodeToString(b), MinCensored)
		}
		add(hex.EncodeToString(b), MinCensored)
		add(strings.ToUpper(hex.EncodeToString(b)), MinCensored)
		add(url.QueryEscape(v), MinCensored)
		add(url.PathEscape(v), MinCensored)
		add(jsonInner(v, false), MinCensored)
		add(jsonInner(v, true), MinCensored)
	}
	// Longest first, so that a value inside a longer form never leaves the rest of it behind.
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	pairs := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		pairs = append(pairs, f, Hidden)
	}
	return &Censor{forms: forms, replacer: strings.NewReplacer(pairs...)}
}

// jsonInner is v as it stands between the quotes of a JSON string.
func jsonInner(v string, escapeHTML bool) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(escapeHTML)
	if enc.Encode(v) != nil {
		return ""
	}
	s := strings.TrimSuffix(b.String(), "\n")
	return strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`)
}

// Clean returns out with every known form of every value hidden. cut says the output was cut off
// at a size limit, so that its end may hold the start of a value: that start is dropped too.
func (c *Censor) Clean(out string, cut bool) string {
	if cut {
		out = c.dropPartial(out)
	}
	if len(c.forms) == 0 {
		return out
	}
	return c.replacer.Replace(out)
}

// dropPartial removes an end of out that a value starts with.
func (c *Censor) dropPartial(out string) string {
	longest := 0
	for _, f := range c.forms {
		longest = max(longest, prefixAtEnd(f, out))
	}
	return out[:len(out)-longest]
}

// prefixAtEnd is the length of the longest start of f, shorter than f, that text ends with. KMP
// over the last len(f)-1 bytes, so a long value costs its length and not its square.
func prefixAtEnd(f, text string) int {
	if len(f) < 2 {
		return 0
	}
	if n := len(f) - 1; len(text) > n {
		text = text[len(text)-n:]
	}
	fail := make([]int, len(f))
	for i, k := 1, 0; i < len(f); i++ {
		for k > 0 && f[i] != f[k] {
			k = fail[k-1]
		}
		if f[i] == f[k] {
			k++
		}
		fail[i] = k
	}
	q := 0
	for i := 0; i < len(text); i++ {
		for q > 0 && text[i] != f[q] {
			q = fail[q-1]
		}
		if text[i] == f[q] {
			q++
		}
		if q == len(f) {
			q = fail[q-1]
		}
	}
	return q
}
