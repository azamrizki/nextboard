package machine

import (
	"os/exec"
	"testing"
)

func TestQuoteRoundTrip(t *testing.T) {
	cases := []string{
		"", "plain", "with space", "it's", `"double"`, "$(rm -rf /)", "`id`", "a;b|c&d",
		"new\nline", "tab\there", "back\\slash", "'", "''", "*glob?", "~/x", "ünïcødé", "-n",
	}
	for _, c := range cases {
		out, err := exec.Command("sh", "-c", "printf %s "+Quote(c)).Output()
		if err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		if string(out) != c {
			t.Errorf("Quote(%q) round-trip = %q", c, out)
		}
	}
}

func TestNestedQuote(t *testing.T) {
	inner := "printf %s " + Quote("it's $HOME")
	out, err := exec.Command("sh", "-c", "sh -c "+Quote(inner)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "it's $HOME" {
		t.Fatalf("got %q", out)
	}
}
