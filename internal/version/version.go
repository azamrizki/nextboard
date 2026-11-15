// Package version holds build metadata injected via -ldflags: which version and commit a hub
// runs, and when it was built.
package version

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// Version is the release version (set by goreleaser, or by the Makefile from git describe).
	Version = "0.1.0-dev"
	// Commit is the short commit hash the binary was built from, with "-dirty" when the tree had
	// changes (set by -ldflags; else read from the Go build info, else "unknown").
	Commit = ""
	// Date is when the binary was built, RFC 3339 (set by -ldflags; else the binary's own file
	// time, else unknown).
	Date = ""
	// Repo is the GitHub "owner/name" used by `nextboard update`.
	Repo = "azamrizki/nextboard"
)

// Channels of a build: what kind of version it runs.
const (
	ChannelStable = "stable" // v0.9.0
	ChannelBeta   = "beta"   // v0.9.0-beta.2 (also alpha.N, rc.N)
	ChannelDev    = "dev"    // anything else: 0.1.0-dev, a git describe, a goreleaser snapshot
)

// UnknownCommit is the commit of a build that recorded none.
const UnknownCommit = "unknown"

// Build is what /api/health, /api/hub and `nextboard version` report.
type Build struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`  // short hash, "-dirty" suffix, or "unknown"
	BuiltAt string `json:"builtAt"` // RFC 3339 UTC, "" when not known
	Channel string `json:"channel"` // stable | beta | dev
}

var (
	once  sync.Once
	build Build
)

// Info is this binary's build, worked out once: the first call fixes it (the hub calls it as it
// starts, so a binary replaced on disk later does not change what the running hub reports).
func Info() Build {
	once.Do(func() {
		bi, _ := debug.ReadBuildInfo()
		build = resolve(Version, Commit, Date, bi, exeTime)
	})
	return build
}

// resolve fills what -ldflags left empty: the commit from the Go build info (vcs.revision and
// vcs.modified, present unless built with -buildvcs=false), the date from the binary's file time.
func resolve(ver, commit, date string, bi *debug.BuildInfo, fileTime func() (time.Time, bool)) Build {
	b := Build{Version: ver, Commit: strings.TrimSpace(commit), Channel: Channel(ver)}
	if b.Commit == "" && bi != nil {
		var rev string
		dirty := false
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if len(rev) > 7 {
			rev = rev[:7]
		}
		if rev != "" {
			b.Commit = rev
			if dirty {
				b.Commit += "-dirty"
			}
		}
	}
	if b.Commit == "" {
		b.Commit = UnknownCommit
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(date)); err == nil {
		b.BuiltAt = t.UTC().Format(time.RFC3339)
	} else if fileTime != nil {
		if t, ok := fileTime(); ok {
			b.BuiltAt = t.UTC().Format(time.RFC3339)
		}
	}
	return b
}

// exeTime is the running binary's modification time: when it was linked, for a binary that no
// release stamped.
func exeTime() (time.Time, bool) {
	p, err := os.Executable()
	if err != nil {
		return time.Time{}, false
	}
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}, false
	}
	return st.ModTime(), true
}

// String is what `nextboard version` prints.
func (b Build) String() string {
	s := "nextboard " + b.Version + " (" + b.Channel + ", commit " + b.Commit
	if b.BuiltAt != "" {
		s += ", built " + b.BuiltAt
	}
	return s + ")"
}

// Channel tells a stable release (1.2.3), a beta (1.2.3-beta.4, alpha.N, rc.N) and anything
// else, which is a dev build: 0.1.0-dev, a git describe (v0.8.1-7-g10ab3f5-dirty), a snapshot.
func Channel(v string) string {
	sv, ok := parse(v)
	if !ok {
		return ChannelDev
	}
	if len(sv.pre) == 0 {
		return ChannelStable
	}
	switch sv.pre[0] {
	case "alpha", "beta", "rc":
	default:
		return ChannelDev
	}
	for _, p := range sv.pre[1:] {
		if _, err := strconv.Atoi(p); err != nil {
			return ChannelDev
		}
	}
	return ChannelBeta
}

// Newer reports whether a is a later version than b, by the rules of semver.org: the numbers
// first, then a pre-release (1.2.0-beta.1) comes before its release (1.2.0). A version that is
// not semver ("dev") is never newer, nor older.
func Newer(a, b string) bool {
	va, okA := parse(a)
	vb, okB := parse(b)
	if !okA || !okB {
		return false
	}
	for i := range 3 {
		if va.core[i] != vb.core[i] {
			return va.core[i] > vb.core[i]
		}
	}
	switch {
	case len(va.pre) == 0 || len(vb.pre) == 0:
		return len(va.pre) == 0 && len(vb.pre) > 0
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		x, y := va.pre[i], vb.pre[i]
		if x == y {
			continue
		}
		nx, errX := strconv.Atoi(x)
		ny, errY := strconv.Atoi(y)
		switch {
		case errX == nil && errY == nil:
			return nx > ny
		case errX == nil:
			return false // numbers sort before words
		case errY == nil:
			return true
		default:
			return x > y
		}
	}
	return len(va.pre) > len(vb.pre)
}

type semver struct {
	core [3]int
	pre  []string
}

func parse(v string) (semver, bool) {
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+")
	v, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var out semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		out.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return semver{}, false
		}
		out.pre = strings.Split(pre, ".")
	}
	return out, true
}
