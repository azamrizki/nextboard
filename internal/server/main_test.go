package server

import (
	"os"
	"testing"

	"github.com/azamrizki/nextboard/internal/nextcode/native"
)

// TestMain lets the test binary be NextCode's own agent, as the hub's own program is (`nextboard
// nextcode-agent`, docs/API-agents.md §30): a console of that engine started by a test hub runs
// it as its child.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "nextcode-agent" {
		os.Exit(native.Main(os.Args[2:]))
	}
	os.Exit(m.Run())
}
