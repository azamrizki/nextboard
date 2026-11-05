package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScreenFixtures runs the rules over screens dumped from the installed
// CLIs (testdata/screens/<cli>-<version>-<state>[-what].txt; first line is
// "cursor=x,y alt=…"). The state in the name is what the rules must report.
func TestScreenFixtures(t *testing.T) {
	files, _ := filepath.Glob("testdata/screens/*.txt")
	if len(files) < 9 {
		t.Fatalf("fixtures missing: %v", files)
	}
	for _, f := range files {
		name := filepath.Base(f)
		parts := strings.Split(strings.TrimSuffix(name, ".txt"), "-")
		cli, want := parts[0], parts[2]
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		head, body, _ := strings.Cut(string(b), "\n")
		var cx, cy int
		fmt.Sscanf(head, "cursor=%d,%d", &cx, &cy)
		lines := strings.Split(body, "\n")
		m := RulesFor(cli).Evaluate(lines, cy)
		ok := false
		switch want {
		case "needsYou":
			ok = m.NeedsYou != ""
		case "working":
			ok = m.Working && m.NeedsYou == ""
		case "ready":
			ok = m.Ready
		}
		if !ok {
			t.Errorf("%s: got %+v, want %s", name, m, want)
		}
	}
}
