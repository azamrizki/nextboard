package vault

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
)

func TestCensorHidesEveryForm(t *testing.T) {
	secret := `sk_test_51H<x>&"y"/z+ä`
	c := NewCensor([]string{secret, "abc"})
	b := []byte(secret)
	out := strings.Join([]string{
		"raw " + secret,
		"b64 " + base64.StdEncoding.EncodeToString(b),
		"b64url " + base64.RawURLEncoding.EncodeToString(b),
		"hex " + hex.EncodeToString(b),
		"HEX " + strings.ToUpper(hex.EncodeToString(b)),
		"query " + url.QueryEscape(secret),
		"path " + url.PathEscape(secret),
		`json {"k":"` + jsonInner(secret, true) + `"}`,
		`json2 {"k":"` + jsonInner(secret, false) + `"}`,
		"twice " + secret + secret,
		"short abc stays: a value under 4 characters cannot be hidden without wiping out output",
	}, "\n")
	got := c.Clean(out, false)
	if strings.Contains(got, "sk_test") || strings.Contains(got, base64.StdEncoding.EncodeToString(b)[:12]) || strings.Contains(got, hex.EncodeToString(b)[:12]) {
		t.Fatalf("a form got through:\n%s", got)
	}
	if strings.Count(got, Hidden) != 11 {
		t.Fatalf("expected 11 hidden spots:\n%s", got)
	}
	if !strings.Contains(got, "short abc stays") {
		t.Fatalf("a value too short to hide must not wipe output:\n%s", got)
	}
}

func TestCensorMultilineValue(t *testing.T) {
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAAB\nAAAAMwAAAAtzc2gtZWQyNTUxOQ\n-----END OPENSSH PRIVATE KEY-----\n"
	c := NewCensor([]string{key})
	got := c.Clean("debug: line two is b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAAB here\nall:\n"+strings.TrimRight(key, "\n"), false)
	if strings.Contains(got, "b3BlbnNzaC1rZXkt") || strings.Contains(got, "AAAAMwAAAAtz") {
		t.Fatalf("a line of the key got through:\n%s", got)
	}
}

func TestCensorDropsAValueCutInHalf(t *testing.T) {
	secret := "hunter2-very-secret"
	c := NewCensor([]string{secret})
	for _, cut := range []int{1, 5, len(secret) - 1} {
		out := "progress 99%\nkey=" + secret[:cut]
		got := c.Clean(out, true)
		if got != "progress 99%\nkey=" {
			t.Fatalf("cut at %d: %q", cut, got)
		}
		// Not cut off: the end is ordinary output and stays.
		if got := c.Clean("ends with h", false); got != "ends with h" {
			t.Fatalf("uncut output changed: %q", got)
		}
	}
	// A long value costs its length, not its square.
	long := strings.Repeat("ab", 32<<10)
	c = NewCensor([]string{long})
	if got := c.Clean(strings.Repeat("x", 1<<20)+long[:len(long)-1], true); got != strings.Repeat("x", 1<<20) {
		t.Fatalf("long cut: %d bytes left", len(got))
	}
}

func TestPrefixAtEnd(t *testing.T) {
	for _, tc := range []struct {
		f, text string
		want    int
	}{
		{"abcabd", "xxabcab", 5},
		{"aaaa", "baaa", 3},
		{"aaaa", "aaaaaa", 3},
		{"abc", "xyz", 0},
		{"abc", "", 0},
		{"a", "a", 0},
	} {
		if got := prefixAtEnd(tc.f, tc.text); got != tc.want {
			t.Errorf("prefixAtEnd(%q, %q) = %d, want %d", tc.f, tc.text, got, tc.want)
		}
	}
}
