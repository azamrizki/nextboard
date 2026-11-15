package instance

// The service that runs an instance: a systemd user unit on Linux, a LaunchAgent on macOS.
// `nextboard install` and Create write it; the templates are kept in sync with
// deploy/nextboard.service and deploy/id.nextboard.plist. The default instance's files are
// exactly what they always were.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// DirIn is DirFor in the home folder home: home/.nextboard, or home/.nextboard-<name>. It is
// for a command that acts for another user (browser-sandbox under sudo).
func DirIn(home, name string) string { return dirIn(home, name) }

// UnitSpec is what an instance's service runs.
type UnitSpec struct {
	Name    string // the instance
	Bin     string // the nextboard binary, absolute
	DataDir string // absolute
	Shell   string // the user's login shell, for consoles
	Home    string
}

var systemdUnit = template.Must(template.New("unit").Parse(`[Unit]
Description={{.Description}}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.Exec}}
Restart=on-failure
RestartSec=3
Environment=SHELL={{.Shell}}
Environment=HOME={{.Home}}
# Keep remote tmux sessions alive; local agents are relaunched with resumeArgs.
KillMode=mixed
TimeoutStopSec=15

[Install]
WantedBy=default.target
`))

var launchdPlist = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
{{- range .Args}}
    <string>{{.}}</string>
{{- end}}
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SHELL</key><string>{{.Shell}}</string>
    <key>HOME</key><string>{{.Home}}</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>{{.Log}}</string>
  <key>StandardErrorPath</key><string>{{.Log}}</string>
</dict>
</plist>
`))

// serveArgs is what the service runs: `<bin> serve --data-dir D` for the default instance (as
// always), `<bin> serve --instance N --data-dir D` for the others.
func (u UnitSpec) serveArgs() []string {
	args := []string{u.Bin, "serve"}
	if u.Name != Default {
		args = append(args, "--instance", u.Name)
	}
	return append(args, "--data-dir", u.DataDir)
}

// unitWord quotes a word of an ExecStart= line only when it needs it, so the usual unit is
// word for word what it always was.
func unitWord(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// RenderUnit is the unit file (goos "linux") or LaunchAgent plist ("darwin") of an instance.
func RenderUnit(goos string, u UnitSpec) ([]byte, error) {
	if err := ValidName(u.Name); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(u.Bin) || !filepath.IsAbs(u.DataDir) {
		return nil, fmt.Errorf("the service needs absolute paths, not %q and %q", u.Bin, u.DataDir)
	}
	if strings.ContainsAny(u.Bin+u.DataDir+u.Shell+u.Home, "\n\r\x00") {
		return nil, errors.New("a path with a line break cannot go in a service file")
	}
	var b bytes.Buffer
	switch goos {
	case "linux":
		desc := "NextBoard hub (visual board for AI coding agents)"
		if u.Name != Default {
			desc = `NextBoard hub "` + u.Name + `" (visual board for AI coding agents)`
		}
		words := u.serveArgs()
		for i, w := range words {
			words[i] = unitWord(w)
		}
		err := systemdUnit.Execute(&b, map[string]string{"Description": desc, "Exec": strings.Join(words, " "), "Shell": u.Shell, "Home": u.Home})
		if err != nil {
			return nil, err
		}
	case "darwin":
		args := u.serveArgs()
		for i, a := range args {
			args[i] = xmlText(a)
		}
		err := launchdPlist.Execute(&b, map[string]any{"Label": LaunchLabel(u.Name), "Args": args, "Shell": xmlText(u.Shell), "Home": xmlText(u.Home),
			"Log": xmlText(filepath.Join(u.DataDir, "nextboard.log"))})
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("OS %s is not supported yet for the service", goos)
	}
	return b.Bytes(), nil
}

// WriteUnit writes the instance's unit file or plist (UnitPath) and returns its path.
func WriteUnit(u UnitSpec) (string, error) {
	path, err := UnitPath(u.Name)
	if err != nil {
		return "", err
	}
	body, err := RenderUnit(runtime.GOOS, u)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// FindService is the service of instance name found on disk, with the data folder it serves;
// nil when the instance has none.
func FindService(name string) (*Service, string, error) {
	h, err := home()
	if err != nil {
		return nil, "", err
	}
	for _, u := range listUnits(h, runtime.GOOS) {
		if u.name == name {
			svc := u.svc
			return &svc, u.dataDir, nil
		}
	}
	return nil, "", nil
}

// SameBinary reports whether two paths name one program, links resolved.
func SameBinary(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// ServicesOn are the instances whose service runs the program bin, as List finds them: the
// ones an update of bin restarts.
func ServicesOn(bin string) ([]Instance, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	var out []Instance
	for _, in := range all {
		if in.Service != nil && SameBinary(in.Service.Bin, bin) {
			out = append(out, in)
		}
	}
	return out, nil
}

// ServiceManager reports whether this user's service manager answers: `systemctl --user` on
// Linux, launchd's gui domain on macOS. A hub run without one (a container, a machine without
// systemd) cannot start an instance as a service.
func ServiceManager() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err != nil {
			return false
		}
		return exec.CommandContext(ctx, "systemctl", "--user", "show-environment").Run() == nil
	case "darwin":
		if _, err := exec.LookPath("launchctl"); err != nil {
			return false
		}
		return exec.CommandContext(ctx, "launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())).Run() == nil
	}
	return false
}

// runTo runs a command with its output to out, or, when out is nil, into the error.
func runTo(out io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if out != nil {
		cmd.Stdout, cmd.Stderr = out, out
		return cmd.Run()
	}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		if s := strings.TrimSpace(string(raw)); s != "" {
			return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// StartService loads the instance's service from its unit file or plist at path and (re)starts
// it: daemon-reload, enable and restart, and linger so it runs without a login (Linux), or
// bootstrap into the gui domain (macOS). Command output goes to out (nil: into the error).
// What failed without stopping the start (linger) comes back as warnings.
func StartService(name, path string, out io.Writer) (warnings []string, err error) {
	switch runtime.GOOS {
	case "linux":
		svc := ServiceName(name)
		if err := runTo(out, "systemctl", "--user", "daemon-reload"); err != nil {
			return nil, fmt.Errorf("systemctl --user daemon-reload: %w", err)
		}
		if err := runTo(out, "systemctl", "--user", "enable", svc); err != nil {
			return nil, err
		}
		if err := runTo(out, "systemctl", "--user", "restart", svc); err != nil {
			return nil, err
		}
		u, uerr := user.Current()
		if uerr != nil {
			return []string{"loginctl enable-linger: " + uerr.Error()}, nil
		}
		if raw, err := exec.Command("loginctl", "enable-linger", u.Username).CombinedOutput(); err != nil {
			why := strings.TrimSpace(string(raw))
			if why == "" {
				why = err.Error()
			}
			return []string{fmt.Sprintf("loginctl enable-linger failed (%s). Run it manually: sudo loginctl enable-linger %s", why, u.Username)}, nil
		}
		return nil, nil
	case "darwin":
		domain := "gui/" + strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain+"/"+LaunchLabel(name)).Run()
		return nil, runTo(out, "launchctl", "bootstrap", domain, path)
	}
	return nil, fmt.Errorf("OS %s is not supported yet for the service", runtime.GOOS)
}

// StopService stops the instance's service and keeps it from starting again (disable --now,
// or bootout). A service that is not there is no error.
func StopService(name string) {
	switch runtime.GOOS {
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", ServiceName(name)).Run()
	case "darwin":
		_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+LaunchLabel(name)).Run()
	}
}

// ServiceState is what the service manager says about the instance's service: active,
// inactive, failed… on Linux, loaded or not loaded on macOS, "" when it says nothing.
func ServiceState(name string) string {
	switch runtime.GOOS {
	case "linux":
		raw, _ := exec.Command("systemctl", "--user", "is-active", ServiceName(name)).Output()
		return strings.TrimSpace(string(raw))
	case "darwin":
		if exec.Command("launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+LaunchLabel(name)).Run() == nil {
			return "loaded"
		}
		return "not loaded"
	}
	return ""
}

// HealthURL is where a hub that listens on listen answers /api/health from this machine:
// loopback for a hub on every address or on loopback, its own address otherwise.
func HealthURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = "", strconv.Itoa(portOf(listen))
	}
	if ip := net.ParseIP(host); host == "" || host == "localhost" || (ip != nil && (ip.IsUnspecified() || ip.IsLoopback())) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/health"
}

// Health is the build a hub says it runs (/api/health).
type Health struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// CheckHealth asks the hub on listen which build it runs.
func CheckHealth(listen string, timeout time.Duration) (*Health, error) {
	c := &http.Client{Timeout: timeout}
	resp, err := c.Get(HealthURL(listen))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health: %s", resp.Status)
	}
	var h Health
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&h); err != nil {
		return nil, err
	}
	return &h, nil
}

// WaitHealthy waits up to timeout for the hub on listen to answer /api/health.
func WaitHealthy(listen string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := CheckHealth(listen, 2*time.Second); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(300 * time.Millisecond)
	}
}
