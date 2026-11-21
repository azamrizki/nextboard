package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

// `nextboard nextcode-agent` is NextCode's own engine, started by the hub: it is not in the help,
// and it goes to the agent rather than being refused as an unknown command.
func TestNextCodeAgentCommand(t *testing.T) {
	if strings.Contains(usage, "nextcode-agent") {
		t.Fatal("nextcode-agent is in the help")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	code := Main([]string{"nextcode-agent", "--no-such-flag"})
	os.Stderr = stderr
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if code == 0 || strings.Contains(string(out), "unknown command") {
		t.Fatalf("exit %d: %s", code, out)
	}
}
