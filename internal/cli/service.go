package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/config"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/server"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/store"
)

const (
	serviceName = "nextboard"
	launchLabel = "id.nextboard"
)

// Kept in sync with deploy/nextboard.service and deploy/id.nextboard.plist.
var systemdUnit = template.Must(template.New("unit").Parse(`[Unit]
Description=NextBoard hub (visual board untuk AI coding agents)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.Bin}} serve --data-dir {{.DataDir}}
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
  <key>Label</key><string>id.nextboard</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.Bin}}</string>
    <string>serve</string>
    <string>--data-dir</string>
    <string>{{.DataDir}}</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SHELL</key><string>{{.Shell}}</string>
    <key>HOME</key><string>{{.Home}}</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>{{.DataDir}}/nextboard.log</string>
  <key>StandardErrorPath</key><string>{{.DataDir}}/nextboard.log</string>
</dict>
</plist>
`))

type unitData struct {
	Bin, DataDir, Shell, Home string
}

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", serviceName+".service"), nil
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist"), nil
	}
	return "", fmt.Errorf("OS %s belum didukung untuk service", runtime.GOOS)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func runQuiet(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func cmdInstall(args []string) error {
	fs, dir := newFlags("install")
	listen := fs.String("listen", "", "alamat listen, default 127.0.0.1:12312 (disimpan di config.json)")
	publicURL := fs.String("public-url", "", "URL HTTPS publik di nginx, mis. https://board.example.com (disimpan di config.json)")
	noStart := fs.Bool("no-start", false, "pasang tanpa menyalakan service")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := store.Open(*dir)
	if err != nil {
		return err
	}
	cfg, err := config.Load(st)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *publicURL != "" {
		if !strings.HasPrefix(*publicURL, "https://") {
			return errors.New("--public-url harus https://")
		}
		u := strings.TrimRight(*publicURL, "/")
		if !slices.Contains(cfg.ExtraURLs, u) {
			cfg.ExtraURLs = append([]string{u}, cfg.ExtraURLs...)
		}
	}
	if err := cfg.Save(st); err != nil {
		return err
	}
	if _, err := os.Stat(st.Path("presets.json")); os.IsNotExist(err) {
		if err := store.WriteFileAtomic(st.Path("presets.json"), []byte("[]\n")); err != nil {
			return err
		}
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	data := unitData{Bin: bin, DataDir: st.Dir, Shell: machine.LoginShell(), Home: home}
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	tmpl := systemdUnit
	if runtime.GOOS == "darwin" {
		tmpl = launchdPlist
	}
	err = tmpl.Execute(f, data)
	f.Close()
	if err != nil {
		return err
	}
	fmt.Println("Service dipasang:", path)

	if !*noStart {
		if err := startService(path); err != nil {
			return err
		}
	}

	h, err := loadHub(*dir)
	if err != nil {
		return err
	}
	if !*noStart {
		waitHealthy(h, 15*time.Second)
	}
	fmt.Printf("\nNextBoard berjalan di http://%s\n", h.cfg.Listen)
	fmt.Println("Hub hanya melayani HTTP. Pasang HTTPS di nginx: lihat deploy/nginx.conf.example.")
	as, err := auth.Open(h.st)
	if err != nil {
		return err
	}
	if as.NeedsSetup() {
		return printSetupLink(h)
	}
	return nil
}

func startService(path string) error {
	switch runtime.GOOS {
	case "linux":
		if err := run("systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %w", err)
		}
		if err := run("systemctl", "--user", "enable", serviceName); err != nil {
			return err
		}
		if err := run("systemctl", "--user", "restart", serviceName); err != nil {
			return err
		}
		u, _ := user.Current()
		if out, err := runQuiet("loginctl", "enable-linger", u.Username); err != nil {
			fmt.Fprintf(os.Stderr, "peringatan: loginctl enable-linger gagal (%s). Jalankan manual: sudo loginctl enable-linger %s\n", out, u.Username)
		}
	case "darwin":
		uid := strconv.Itoa(os.Getuid())
		_, _ = runQuiet("launchctl", "bootout", "gui/"+uid+"/"+launchLabel)
		if err := run("launchctl", "bootstrap", "gui/"+uid, path); err != nil {
			return err
		}
	}
	return nil
}

func stopService(path string) {
	switch runtime.GOOS {
	case "linux":
		_, _ = runQuiet("systemctl", "--user", "disable", "--now", serviceName)
	case "darwin":
		_, _ = runQuiet("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+launchLabel)
	}
}

func restartService() error {
	switch runtime.GOOS {
	case "linux":
		return run("systemctl", "--user", "restart", serviceName)
	case "darwin":
		return run("launchctl", "kickstart", "-k", "gui/"+strconv.Itoa(os.Getuid())+"/"+launchLabel)
	}
	return errors.New("OS tidak didukung")
}

func cmdUninstall(args []string) error {
	fs, dir := newFlags("uninstall")
	purge := fs.Bool("purge", false, "hapus juga folder data (~/.nextboard)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := unitPath()
	if err != nil {
		return err
	}
	stopService(path)
	if err := os.Remove(path); err == nil {
		fmt.Println("Service dilepas:", path)
	}
	if runtime.GOOS == "linux" {
		_, _ = runQuiet("systemctl", "--user", "daemon-reload")
	}
	if *purge {
		abs, _ := filepath.Abs(*dir)
		if abs == "/" || abs == os.Getenv("HOME") {
			return fmt.Errorf("menolak menghapus %s", abs)
		}
		if err := os.RemoveAll(abs); err != nil {
			return err
		}
		fmt.Println("Data dihapus:", abs)
	} else {
		fmt.Println("Data tetap ada di", *dir, "(pakai --purge untuk menghapus)")
	}
	return nil
}

// localClient talks to the local hub over loopback.
func localClient() *http.Client { return &http.Client{Timeout: 3 * time.Second} }

func localURL(cfg *config.Config) string {
	return "http://127.0.0.1:" + strconv.Itoa(cfg.Port())
}

func waitHealthy(h *hubInfo, timeout time.Duration) bool {
	c := localClient()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := c.Get(localURL(h.cfg) + "/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "peringatan: hub belum merespons; cek `nextboard logs`")
	return false
}

func cmdStatus(args []string) error {
	fs, dir := newFlags("status")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := loadHub(*dir)
	if err != nil {
		return err
	}
	state := "tidak terpasang"
	switch runtime.GOOS {
	case "linux":
		if out, _ := runQuiet("systemctl", "--user", "is-active", serviceName); out != "" {
			state = out
		}
	case "darwin":
		if _, err := runQuiet("launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+launchLabel); err == nil {
			state = "loaded"
		}
	}
	health := "tidak merespons"
	if resp, err := localClient().Get(localURL(h.cfg) + "/api/health"); err == nil {
		var body struct {
			Version string `json:"version"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		health = "OK (versi " + body.Version + ")"
	}
	var sessions []session.Spec
	_, _ = h.st.Load("sessions.json", &sessions)
	var machines []machine.Config
	_, _ = h.st.Load("machines.json", &machines)
	remote := 0
	for _, m := range machines {
		if m.Kind == "ssh" {
			remote++
		}
	}
	var devices []auth.Device
	_, _ = h.st.Load("devices.json", &devices)
	name := h.cfg.HubName
	if name == "" {
		name = "(belum di-setup)"
	}
	fmt.Printf("Hub:         %s\n", name)
	fmt.Printf("Service:     %s\n", state)
	fmt.Printf("Health:      %s\n", health)
	fmt.Printf("Listen:      %s\n", h.cfg.Listen)
	fmt.Printf("Alamat:      %s\n", strings.Join(server.HubURLs(h.cfg.Listen, h.cfg.ExtraURLs), "\n             "))
	fmt.Printf("Sesi:        %d\n", len(sessions))
	fmt.Printf("Machine:     This PC + %d remote\n", remote)
	fmt.Printf("Device:      %d\n", len(devices))
	fmt.Printf("Data:        %s\n", h.st.Dir)
	return nil
}

func cmdLogs(args []string) error {
	fs, dir := newFlags("logs")
	follow := fs.Bool("f", false, "ikuti log")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "linux":
		a := []string{"--user", "-u", serviceName, "-n", "200", "--no-pager"}
		if *follow {
			a = append(a, "-f")
		}
		return run("journalctl", a...)
	case "darwin":
		a := []string{"-n", "200"}
		if *follow {
			a = append(a, "-f")
		}
		return run("tail", append(a, filepath.Join(*dir, "nextboard.log"))...)
	}
	return errors.New("OS tidak didukung")
}
