// Package version holds build metadata injected via -ldflags.
package version

var (
	// Version is the release version (set by goreleaser).
	Version = "0.1.0-dev"
	// Repo is the GitHub "owner/name" used by `nextboard update`.
	Repo = ""
)
