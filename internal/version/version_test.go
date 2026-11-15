package version

import (
	"runtime/debug"
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	built := time.Date(2026, 11, 14, 8, 30, 0, 0, time.UTC)
	fileTime := func() (time.Time, bool) { return built, true }
	noFile := func() (time.Time, bool) { return time.Time{}, false }
	vcs := func(rev, modified string) *debug.BuildInfo {
		bi := &debug.BuildInfo{}
		if rev != "" {
			bi.Settings = append(bi.Settings, debug.BuildSetting{Key: "vcs.revision", Value: rev})
		}
		if modified != "" {
			bi.Settings = append(bi.Settings, debug.BuildSetting{Key: "vcs.modified", Value: modified})
		}
		return bi
	}
	for _, c := range []struct {
		name              string
		ver, commit, date string
		bi                *debug.BuildInfo
		file              func() (time.Time, bool)
		want              Build
	}{
		{"a release: ldflags win over the build info", "0.9.0", "3f2a1c9", "2026-11-10T09:00:00Z", vcs("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "true"), fileTime,
			Build{Version: "0.9.0", Commit: "3f2a1c9", BuiltAt: "2026-11-10T09:00:00Z", Channel: "stable"}},
		{"the Makefile marks a dirty tree", "v0.8.1-7-g10ab3f5-dirty", "10ab3f5-dirty", "2026-11-15T01:02:03Z", nil, noFile,
			Build{Version: "v0.8.1-7-g10ab3f5-dirty", Commit: "10ab3f5-dirty", BuiltAt: "2026-11-15T01:02:03Z", Channel: "dev"}},
		{"goreleaser's date in another zone is told in UTC", "0.9.0-beta.2", "3f2a1c9", "2026-11-10T16:00:00+07:00", nil, noFile,
			Build{Version: "0.9.0-beta.2", Commit: "3f2a1c9", BuiltAt: "2026-11-10T09:00:00Z", Channel: "beta"}},
		{"go build in a checkout: the build info's commit, shortened", "0.1.0-dev", "", "", vcs("4b825dc642cb6eb9a060e54bf8d69288fbee4904", "false"), fileTime,
			Build{Version: "0.1.0-dev", Commit: "4b825dc", BuiltAt: "2026-11-14T08:30:00Z", Channel: "dev"}},
		{"go build of a changed tree", "0.1.0-dev", "", "", vcs("4b825dc642cb6eb9a060e54bf8d69288fbee4904", "true"), fileTime,
			Build{Version: "0.1.0-dev", Commit: "4b825dc-dirty", BuiltAt: "2026-11-14T08:30:00Z", Channel: "dev"}},
		{"-buildvcs=false (the dev hub, the e2e hub): no commit", "0.1.0-dev", "", "", vcs("", ""), fileTime,
			Build{Version: "0.1.0-dev", Commit: "unknown", BuiltAt: "2026-11-14T08:30:00Z", Channel: "dev"}},
		{"no build info at all", "0.1.0-dev", "  ", "not a date", nil, noFile,
			Build{Version: "0.1.0-dev", Commit: "unknown", BuiltAt: "", Channel: "dev"}},
		{"a date that does not parse falls back to the file time", "0.9.0", "3f2a1c9", "yesterday", nil, fileTime,
			Build{Version: "0.9.0", Commit: "3f2a1c9", BuiltAt: "2026-11-14T08:30:00Z", Channel: "stable"}},
	} {
		if got := resolve(c.ver, c.commit, c.date, c.bi, c.file); got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestInfoIsTheVariables(t *testing.T) {
	b := Info()
	if b.Version != Version || b.Channel != Channel(Version) || b.Commit == "" {
		t.Fatalf("Info() = %+v with Version %q", b, Version)
	}
	if b.BuiltAt != "" {
		if _, err := time.Parse(time.RFC3339, b.BuiltAt); err != nil {
			t.Fatalf("builtAt %q: %v", b.BuiltAt, err)
		}
	}
	if Info() != b {
		t.Fatal("Info changed between calls")
	}
}

func TestString(t *testing.T) {
	b := Build{Version: "0.9.0", Commit: "3f2a1c9", BuiltAt: "2026-11-10T09:00:00Z", Channel: "stable"}
	if got, want := b.String(), "nextboard 0.9.0 (stable, commit 3f2a1c9, built 2026-11-10T09:00:00Z)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	b.BuiltAt = ""
	if got, want := b.String(), "nextboard 0.9.0 (stable, commit 3f2a1c9)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestChannel(t *testing.T) {
	for v, want := range map[string]string{
		"0.9.0":                    "stable",
		"v0.9.0":                   "stable",
		"1.2.3+build.5":            "stable",
		"0.9.0-beta.2":             "beta",
		"v0.9.0-beta.10":           "beta",
		"0.9.0-rc.1":               "beta",
		"0.9.0-alpha.3":            "beta",
		"0.9.0-beta":               "beta",
		"0.1.0-dev":                "dev",
		"v0.8.1-7-g10ab3f5-dirty":  "dev",
		"v0.9.0-beta.2-3-gabc1234": "dev",
		"v0.9.0-dirty":             "dev",
		"0.9.1-SNAPSHOT-abc1234":   "dev",
		"10ab3f5":                  "dev",
		"":                         "dev",
	} {
		if got := Channel(v); got != want {
			t.Errorf("Channel(%q) = %q, want %q", v, got, want)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.4.0", "0.3.0", true},
		{"0.3.0", "0.4.0", false},
		{"1.0.0", "0.99.99", true},
		{"0.3.10", "0.3.9", true},
		// a beta of the next version is newer than the last release, older than its own
		{"0.4.0-beta.1", "0.3.0", true},
		{"0.3.0", "0.4.0-beta.1", false},
		{"0.4.0", "0.4.0-beta.1", true},
		{"0.4.0-beta.1", "0.4.0", false},
		{"0.4.0-beta.2", "0.4.0-beta.1", true},
		{"0.4.0-beta.10", "0.4.0-beta.9", true},
		{"0.4.0-rc.1", "0.4.0-beta.3", true},
		{"0.4.0-beta.1", "0.4.0-beta", true},
		{"0.4.0-beta", "0.4.0-1", true},
		{"v0.4.0", "0.3.0", true},
		{"0.4.0+build.5", "0.3.0", true},
		{"0.4.0", "0.4.0", false},
		// not semver: never newer, so a plain update still works from a dev build
		{"dev", "0.3.0", false},
		{"0.3.0", "dev", false},
		{"0.4", "0.3.0", false},
		{"0.4.0-", "0.3.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
