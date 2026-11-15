package update

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// The service `nextboard install` sets up: a systemd user unit on Linux, a LaunchAgent on macOS.
const (
	ServiceName = "nextboard"
	LaunchLabel = "id.nextboard"
)

// UnitPath is the service's unit file (Linux) or LaunchAgent plist (macOS).
func UnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", ServiceName+".service"), nil
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", LaunchLabel+".plist"), nil
	}
	return "", fmt.Errorf("OS %s is not supported yet for the service", runtime.GOOS)
}

func launchTarget() string { return "gui/" + strconv.Itoa(os.Getuid()) + "/" + LaunchLabel }

// RestartService restarts the service and waits for the service manager: what a terminal runs.
func RestartService() error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("systemctl", "--user", "restart", ServiceName)
	case "darwin":
		cmd = exec.Command("launchctl", "kickstart", "-k", launchTarget())
	default:
		return errors.New("unsupported OS")
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// RestartSelf asks the service manager to restart the hub that calls it, without waiting: the
// caller is what gets stopped (gracefully, as for any restart), then the service starts the
// binary now in its place.
func RestartSelf() error {
	switch runtime.GOOS {
	case "linux":
		if out, err := exec.Command("systemctl", "--user", "--no-block", "restart", ServiceName).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl --user restart %s: %v %s", ServiceName, err, bytes.TrimSpace(out))
		}
		return nil
	case "darwin":
		// in a session of its own: launchd ends the hub's process group with the hub
		cmd := exec.Command("launchctl", "kickstart", "-k", launchTarget())
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	return errors.New("unsupported OS")
}

// ErrNotService: the hub cannot restart itself, because it does not run as the service.
var ErrNotService = errors.New("this hub was not started by the service that `nextboard install` sets up, so it cannot restart itself; update it with `nextboard update` where it runs")

// Managed returns nil when this process is the service `nextboard install` set up for bin: the
// unit (or LaunchAgent) starts bin, and the service manager names this process as its main
// one. A hub run by hand, under air, or by another unit is not, so it is not restarted.
func Managed(bin string) error {
	path, err := UnitPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ErrNotService
	}
	pid := strconv.Itoa(os.Getpid())
	switch runtime.GOOS {
	case "linux":
		if execStart(raw) != bin {
			return fmt.Errorf("%w (the service runs %s, this hub is %s)", ErrNotService, orNothing(execStart(raw)), bin)
		}
		out, err := exec.Command("systemctl", "--user", "show", "-p", "MainPID", "--value", ServiceName).Output()
		if err != nil || strings.TrimSpace(string(out)) != pid {
			return ErrNotService
		}
	case "darwin":
		if !bytes.Contains(raw, []byte("<string>"+bin+"</string>")) {
			return ErrNotService
		}
		out, err := exec.Command("launchctl", "print", launchTarget()).Output()
		if err != nil || !bytes.Contains(out, []byte("pid = "+pid+"\n")) {
			return ErrNotService
		}
	}
	return nil
}

// execStart is the program a unit's ExecStart= line runs.
func execStart(unit []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(unit))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "ExecStart="); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
}

func orNothing(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}
