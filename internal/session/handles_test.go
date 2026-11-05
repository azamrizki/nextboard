package session

import (
	"strings"
	"testing"
)

// TestDefaultHandles: every default name can be used as a handle, none repeats (handles
// ignore case), and none takes the lead's default name, which @mentions also reach.
func TestDefaultHandles(t *testing.T) {
	seen := map[string]bool{}
	for _, h := range handleNames {
		if !ValidHandle(h) {
			t.Errorf("%s is not a valid handle", h)
		}
		l := strings.ToLower(h)
		if seen[l] || l == "nara" {
			t.Errorf("%s repeats or takes the lead's name", h)
		}
		seen[l] = true
	}
}
