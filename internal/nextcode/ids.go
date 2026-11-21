package nextcode

import (
	"crypto/rand"
	"encoding/hex"
)

// newID is a fresh item id with a prefix.
func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
