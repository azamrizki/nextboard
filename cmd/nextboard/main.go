// Command nextboard is the NextBoard hub: a self-hosted visual board for AI
// coding agent terminals.
package main

import (
	"os"

	"github.com/azamrizki/nextboard/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
