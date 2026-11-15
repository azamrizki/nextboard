package secret

import (
	"fmt"
	"os"
)

// OpenExisting loads a master key that must already be there: another instance's, whose secrets
// are being copied (POST /api/instances/import). Unlike Open it never writes a new key, so it
// cannot leave a file behind in that instance's data folder.
func OpenExisting(path string) (*Box, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("no master key at %s: %w", path, err)
	}
	return Open(path)
}
