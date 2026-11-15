package update

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/azamrizki/nextboard/internal/instance"
)

// The services `nextboard install` sets up, one per instance: a systemd user unit on Linux
// (nextboard, nextboard-<name>), a LaunchAgent on macOS (id.nextboard, id.nextboard.<name>).
// The names come from internal/instance.

func launchTarget(name string) string {
	return "gui/" + strconv.Itoa(os.Getuid()) + "/" + instance.LaunchLabel(name)
}

// RestartService restarts the instance's service and waits for the service manager: what a
// terminal runs.
func RestartService(name string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("systemctl", "--user", "restart", instance.ServiceName(name))
	case "darwin":
		cmd = exec.Command("launchctl", "kickstart", "-k", launchTarget(name))
	default:
		return errors.New("unsupported OS")
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// RestartNoWait asks the service manager to restart the instance's service, without waiting.
// A hub restarts itself (RestartSelf) and its siblings on the same binary this way: when it is
// the one that gets stopped (gracefully, as for any restart), the service starts the binary
// now in its place.
func RestartNoWait(name string) error {
	switch runtime.GOOS {
	case "linux":
		svc := instance.ServiceName(name)
		if out, err := exec.Command("systemctl", "--user", "--no-block", "restart", svc).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl --user restart %s: %v %s", svc, err, bytes.TrimSpace(out))
		}
		return nil
	case "darwin":
		// in a session of its own: launchd ends the hub's process group with the hub
		cmd := exec.Command("launchctl", "kickstart", "-k", launchTarget(name))
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	return errors.New("unsupported OS")
}

// RestartSelf asks the service manager to restart the hub of instance name, which calls it,
// without waiting (RestartNoWait).
func RestartSelf(name string) error { return RestartNoWait(name) }

// ErrNotService: the hub cannot restart itself, because it does not run as the service.
var ErrNotService = errors.New("this hub was not started by the service that `nextboard install` sets up, so it cannot restart itself; update it with `nextboard update` where it runs")

// Managed returns nil when this process is the service `nextboard install` set up for the
// instance name and the program bin: the instance's unit (or LaunchAgent) starts bin, and the
// service manager names this process as its main one. A hub run by hand, under air, or by
// another unit is not, so it is not restarted.
func Managed(name, bin string) error {
	svc, _, err := instance.FindService(name)
	if err != nil {
		return err
	}
	if svc == nil {
		return ErrNotService
	}
	if !instance.SameBinary(svc.Bin, bin) {
		return fmt.Errorf("%w (the service runs %s, this hub is %s)", ErrNotService, orNothing(svc.Bin), bin)
	}
	pid := strconv.Itoa(os.Getpid())
	switch runtime.GOOS {
	case "linux":
		out, err := exec.Command("systemctl", "--user", "show", "-p", "MainPID", "--value", instance.ServiceName(name)).Output()
		if err != nil || strings.TrimSpace(string(out)) != pid {
			return ErrNotService
		}
	case "darwin":
		out, err := exec.Command("launchctl", "print", launchTarget(name)).Output()
		if err != nil || !bytes.Contains(out, []byte("pid = "+pid+"\n")) {
			return ErrNotService
		}
	default:
		return ErrNotService
	}
	return nil
}

func orNothing(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}
