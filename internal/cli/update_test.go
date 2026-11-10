package cli

import "testing"

func TestNewerVersion(t *testing.T) {
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
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
