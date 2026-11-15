// Package instance tells the NextBoard hubs of one machine apart: each instance has a name,
// its own data folder, service, and block of ports. The default instance is the one that was
// always there: ~/.nextboard, the service `nextboard`, port 12312.
//
// Nothing here keeps a registry: instances are found on disk (List), and which one a command
// acts on comes from its flags and the environment (Resolve).
package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Default is the name of the instance that lives in ~/.nextboard.
const Default = "default"

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

// ErrBadName: an instance name that does not fit ^[a-z][a-z0-9-]{0,23}$.
var ErrBadName = errors.New("bad instance name")

// ValidName reports whether name can name an instance. "default" is valid: it names the
// default instance.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%w %q: use 1 to 24 characters a-z, 0-9 and -, starting with a letter", ErrBadName, name)
	}
	return nil
}

func home() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home folder to find NextBoard instances in: %w", err)
	}
	return h, nil
}

// DirFor is the data folder an instance gets when nothing else names one: ~/.nextboard for
// the default instance, ~/.nextboard-<name> for the others.
func DirFor(name string) (string, error) {
	h, err := home()
	if err != nil {
		return "", err
	}
	return dirIn(h, name), nil
}

func dirIn(home, name string) string {
	if name == Default {
		return filepath.Join(home, ".nextboard")
	}
	return filepath.Join(home, ".nextboard-"+name)
}

// LegacyName is the name a data folder whose config.json does not record one gets: a folder
// named .nextboard-<x> is x, every other folder (~/.nextboard, $NEXTBOARD_HOME=/srv/nb,
// .devdata, a test's) is the default instance, which keeps today's service and cookie names.
func LegacyName(dataDir string) string {
	if x, ok := strings.CutPrefix(filepath.Base(dataDir), ".nextboard-"); ok && ValidName(x) == nil {
		return x
	}
	return Default
}

// boundName is the name a data folder's own path commits it to, if any: ~/.nextboard is the
// default instance's, .nextboard-<x> is x's. Other folders can be claimed by any name.
func boundName(home, dataDir string) (string, bool) {
	if filepath.Clean(dataDir) == dirIn(home, Default) {
		return Default, true
	}
	if x, ok := strings.CutPrefix(filepath.Base(dataDir), ".nextboard-"); ok && ValidName(x) == nil {
		return x, true
	}
	return "", false
}

// ServiceName is the systemd user unit an instance's service runs as, without ".service":
// nextboard for the default instance, nextboard-<name> for the others.
func ServiceName(name string) string {
	if name == Default {
		return "nextboard"
	}
	return "nextboard-" + name
}

// LaunchLabel is the label of an instance's LaunchAgent on macOS: id.nextboard for the default
// instance, id.nextboard.<name> for the others.
func LaunchLabel(name string) string {
	if name == Default {
		return "id.nextboard"
	}
	return "id.nextboard." + name
}

// UnitPath is where an instance's unit file (Linux) or LaunchAgent plist (macOS) lives.
func UnitPath(name string) (string, error) {
	h, err := home()
	if err != nil {
		return "", err
	}
	return unitPathIn(h, runtime.GOOS, name)
}

func unitPathIn(home, goos, name string) (string, error) {
	switch goos {
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", ServiceName(name)+".service"), nil
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", LaunchLabel(name)+".plist"), nil
	}
	return "", fmt.Errorf("OS %s is not supported yet for the service", goos)
}
