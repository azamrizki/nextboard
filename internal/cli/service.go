package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/config"
	"github.com/azamrizki/nextboard/internal/instance"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/server"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/azamrizki/nextboard/internal/update"
	"github.com/azamrizki/nextboard/internal/version"
)

// The service of each instance (internal/instance): the systemd user unit nextboard[-<name>]
// on Linux, the LaunchAgent id.nextboard[.<name>] on macOS. The default instance keeps the
// names, files and folder it always had.

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func runQuiet(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// selfBin is this program, links resolved: what a service runs.
func selfBin() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(bin)
}

func cmdInstall(args []string) error {
	fs, dir := newFlags("install")
	statusListen := fs.String("status-listen", "", "second address that serves only the public status page (saved in config.json)")
	listen := fs.String("listen", "", "listen address, default 127.0.0.1:12312, or the next free block for another instance (saved in config.json)")
	publicURL := fs.String("public-url", "", "public HTTPS URL in nginx, e.g. https://board.example.com (saved in config.json)")
	noStart := fs.Bool("no-start", false, "install without starting the service")
	if err := fs.Parse(args); err != nil {
		return err
	}
	inst, err := resolveFlags(fs, *dir)
	if err != nil {
		return err
	}
	public := ""
	if *publicURL != "" {
		if !strings.HasPrefix(*publicURL, "https://") {
			return errors.New("--public-url must be https://")
		}
		public = strings.TrimRight(*publicURL, "/")
	}
	bin, err := selfBin()
	if err != nil {
		return err
	}
	if inst.Name != instance.Default && !inst.Exists {
		return installNew(inst, bin, *listen, *statusListen, public, *noStart)
	}
	return installExisting(inst, bin, *listen, *statusListen, public, *noStart)
}

// installNew makes a new instance with its own folder, ports and service (instance.Create).
func installNew(inst instance.Resolved, bin, listen, statusListen, public string, noStart bool) error {
	var extra []string
	if public != "" {
		extra = []string{public}
	}
	c, err := instance.Create(instance.CreateOptions{
		Name: inst.Name, DataDir: inst.DataDir, Listen: listen, StatusListen: statusListen, ExtraURLs: extra, NoStart: noStart,
		Bin: bin, Shell: machine.LoginShell(), URLs: server.HubURLs, Out: os.Stdout,
	})
	if c == nil {
		return err
	}
	fmt.Printf("New instance %s: data in %s, hub on %s, previews on %d–%d\n", c.Instance, c.DataDir, c.Listen, c.PreviewRange[0], c.PreviewRange[1])
	if c.Service != nil {
		fmt.Println("Service installed:", c.Service.UnitPath)
	}
	for _, w := range c.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if err != nil {
		return err
	}
	switch {
	case c.Started:
		fmt.Printf("\nNextBoard instance %s is running at http://%s\n", c.Instance, c.Listen)
	case c.Command != "":
		fmt.Printf("\nNo service manager answers here, so nothing started it. Start it with:\n  %s\n", c.Command)
	case noStart:
		fmt.Println("\nNot started (--no-start).")
	}
	fmt.Println("The hub serves only HTTP. Set up HTTPS in nginx: see deploy/nginx.conf.example.")
	fmt.Println("Open this setup link (single use, valid for 30 minutes):")
	for i, u := range c.SetupURLs {
		if i == 3 {
			break
		}
		fmt.Printf("  %s\n", u)
	}
	if c.Started {
		restartStaleSiblings(bin, c.Instance)
	}
	return nil
}

// installExisting (re)installs the service of an instance that is there already, or of the
// default instance, as `install` always did: the flags go into its config.json, its unit is
// written again and (re)started.
func installExisting(inst instance.Resolved, bin, listen, statusListen, public string, noStart bool) error {
	if listen != "" || statusListen != "" {
		if err := siblingsKeepOff(inst.DataDir, listen, statusListen); err != nil {
			return err
		}
	}
	st, err := store.Open(inst.DataDir)
	if err != nil {
		return err
	}
	cfg, err := config.Load(st)
	if err != nil {
		return err
	}
	if listen != "" {
		cfg.Listen = listen
	}
	if statusListen != "" {
		cfg.StatusListen = statusListen
	}
	if public != "" && !slices.Contains(cfg.ExtraURLs, public) {
		cfg.ExtraURLs = append([]string{public}, cfg.ExtraURLs...)
	}
	if !inst.Exists && !instance.PortFree(cfg.Port()) {
		fmt.Fprintf(os.Stderr, "warning: port %d is taken already, perhaps by another hub. For a second hub on this machine use `nextboard install --instance <name>`.\n", cfg.Port())
	}
	if err := cfg.Save(st); err != nil {
		return err
	}
	if _, err := os.Stat(st.Path("presets.json")); os.IsNotExist(err) {
		if err := store.WriteFileAtomic(st.Path("presets.json"), []byte("[]\n")); err != nil {
			return err
		}
	}
	home, _ := os.UserHomeDir()
	path, err := instance.WriteUnit(instance.UnitSpec{Name: inst.Name, Bin: bin, DataDir: st.Dir, Shell: machine.LoginShell(), Home: home})
	if err != nil {
		return err
	}
	fmt.Println("Service installed:", path)

	if !noStart {
		warn, err := instance.StartService(inst.Name, path, os.Stdout)
		for _, w := range warn {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
		if err != nil {
			return err
		}
	}

	h, err := loadHub(st.Dir)
	if err != nil {
		return err
	}
	if !noStart {
		waitHealthy(h, inst.Name, 15*time.Second)
	}
	fmt.Printf("\nNextBoard is running at http://%s\n", h.cfg.Listen)
	fmt.Println("The hub serves only HTTP. Set up HTTPS in nginx: see deploy/nginx.conf.example.")
	if !noStart {
		restartStaleSiblings(bin, inst.Name)
	}
	as, err := auth.Open(h.st)
	if err != nil {
		return err
	}
	if as.NeedsSetup() {
		return printSetupLink(h)
	}
	return nil
}

// siblingsKeepOff refuses a hub or status address whose port another instance claims.
func siblingsKeepOff(dataDir, listen, statusListen string) error {
	all, err := instance.List()
	if err != nil {
		return err
	}
	var others []instance.Instance
	for _, in := range all {
		if !sameDir(in.DataDir, dataDir) {
			others = append(others, in)
		}
	}
	for _, a := range []struct{ flag, addr string }{{"--listen", listen}, {"--status-listen", statusListen}} {
		if a.addr == "" {
			continue
		}
		p := (&config.Config{Listen: a.addr}).Port()
		if by := instance.ClaimedBy(others, p); by != "" {
			return fmt.Errorf("%s %s: %w: port %d is instance %s's", a.flag, a.addr, instance.ErrPortInUse, p, by)
		}
	}
	return nil
}

func sameDir(a, b string) bool {
	clean := func(d string) string {
		if abs, err := filepath.Abs(d); err == nil {
			d = abs
		}
		if real, err := filepath.EvalSymlinks(d); err == nil {
			return real
		}
		return filepath.Clean(d)
	}
	return clean(a) == clean(b)
}

// restartStaleSiblings restarts the other instances whose service runs bin when their hub runs
// another build than this one: install.sh has just replaced the binary under them.
func restartStaleSiblings(bin, self string) {
	ins, err := instance.ServicesOn(bin)
	if err != nil {
		return
	}
	me := version.Info()
	var stale []instance.Instance
	var was []string
	for _, in := range ins {
		if in.Name == self {
			continue
		}
		h, err := instance.CheckHealth(in.Listen, 2*time.Second)
		if err != nil || (h.Version == me.Version && h.Commit == me.Commit) {
			continue
		}
		stale = append(stale, in)
		was = append(was, fmt.Sprintf("%s (%s, was %s)", in.Name, in.Listen, h.Version))
	}
	if len(stale) == 0 {
		return
	}
	fmt.Printf("\nRestarting the other instances on %s, so they run %s too: %s\n", bin, me.Version, strings.Join(was, ", "))
	for _, in := range stale {
		if err := update.RestartService(in.Name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: restarting %s failed: %v\n", in.Name, err)
		}
	}
}

func cmdUninstall(args []string) error {
	fs, dir := newFlags("uninstall")
	purge := fs.Bool("purge", false, "also delete the instance's data folder (~/.nextboard, ~/.nextboard-<name>)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	inst, err := resolveFlags(fs, *dir)
	if err != nil {
		return err
	}
	svc, served, err := instance.FindService(inst.Name)
	if err != nil {
		return err
	}
	if *purge {
		if inst.DataDir == "/" || sameDir(inst.DataDir, os.Getenv("HOME")) {
			return fmt.Errorf("refusing to delete %s", inst.DataDir)
		}
		if svc != nil && !sameDir(served, inst.DataDir) {
			return fmt.Errorf("the service %s runs %s, not %s: --purge deletes only the folder of the service it removes", svc.Name, served, inst.DataDir)
		}
	}
	path, err := instance.UnitPath(inst.Name)
	if err != nil {
		return err
	}
	instance.StopService(inst.Name)
	if err := os.Remove(path); err == nil {
		fmt.Println("Service removed:", path)
	}
	if runtime.GOOS == "linux" {
		_, _ = runQuiet("systemctl", "--user", "daemon-reload")
	}
	if !*purge {
		fmt.Println("Data kept in", inst.DataDir, "(use --purge to delete it)")
		return nil
	}
	// the service has stopped by now; a hub run by hand still holds the folder
	for i := 0; ; i++ {
		running, pid := instance.Running(inst.DataDir)
		if !running {
			break
		}
		if i == 30 {
			return fmt.Errorf("a hub (pid %d) still runs on %s: stop it first, then run this again", pid, inst.DataDir)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := removeData(inst.DataDir); err != nil {
		return err
	}
	fmt.Println("Data deleted:", inst.DataDir)
	return nil
}

// removeData deletes the data folder. Should a folder in it be read only (a project root's note
// folder of an earlier build was 0555, or an agent made one so in a root), the folders are made
// writable and it goes again. Neither step follows a link: a project root links to parts' folders
// on This PC, which stay as they are.
func removeData(abs string) error {
	if err := os.RemoveAll(abs); err == nil {
		return nil
	}
	_ = filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Type()&os.ModeSymlink == 0 {
			_ = os.Chmod(p, 0o700) // WalkDir never descends into a link
		}
		return nil
	})
	return os.RemoveAll(abs)
}

// localClient talks to the local hub over loopback.
func localClient() *http.Client { return &http.Client{Timeout: 3 * time.Second} }

func localURL(cfg *config.Config) string {
	return "http://127.0.0.1:" + strconv.Itoa(cfg.Port())
}

func waitHealthy(h *hubInfo, name string, timeout time.Duration) bool {
	if instance.WaitHealthy(h.cfg.Listen, timeout) {
		return true
	}
	fmt.Fprintf(os.Stderr, "warning: hub not responding yet; check `%s`\n", withInstance("nextboard logs", name))
	return false
}

// withInstance is a command for instance name: as it is for the default instance.
func withInstance(cmd, name string) string {
	if name == "" || name == instance.Default {
		return cmd
	}
	return cmd + " --instance " + name
}

// serviceState is the instance's service as `status` and `instances` show it.
func serviceState(name string) (svc *instance.Service, state string) {
	svc, _, _ = instance.FindService(name)
	if svc == nil {
		return nil, "not installed"
	}
	state = instance.ServiceState(name)
	if state == "" {
		state = "unknown"
	}
	return svc, state
}

func cmdStatus(args []string) error {
	fs, dir := newFlags("status")
	all := fs.Bool("all", false, "every instance on this machine")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *all {
		ins, err := instance.List()
		if err != nil {
			return err
		}
		if len(ins) == 0 {
			fmt.Println("No NextBoard instance on this machine yet. Install one with `nextboard install`.")
		}
		for i, in := range ins {
			if i > 0 {
				fmt.Println()
			}
			if _, err := os.Stat(in.DataDir); errors.Is(err, os.ErrNotExist) {
				_, state := serviceState(in.Name)
				fmt.Printf("Instance:    %s\nService:     %s\nData:        %s (missing)\n", in.Name, state, in.DataDir)
			} else if err := printStatus(in.Name, in.DataDir); err != nil {
				return err
			}
			for _, p := range in.Problems {
				fmt.Printf("Problem:     %s\n", p)
			}
		}
		return nil
	}
	inst, err := resolveFlags(fs, *dir)
	if err != nil {
		return err
	}
	if err := needInstance(inst); err != nil {
		return err
	}
	if err := printStatus(inst.Name, inst.DataDir); err != nil {
		return err
	}
	// the other instances, in a line
	if ins, err := instance.List(); err == nil {
		var others []string
		for _, in := range ins {
			if sameDir(in.DataDir, inst.DataDir) {
				for _, p := range in.Problems {
					fmt.Printf("Problem:     %s\n", p)
				}
				continue
			}
			state := "stopped"
			if in.Running {
				state = "running"
			}
			if in.Service != nil {
				_, state = serviceState(in.Name)
			}
			others = append(others, fmt.Sprintf("%s (%s, %s)", in.Name, state, in.Listen))
		}
		if len(others) > 0 {
			fmt.Printf("Other instances: %s (`nextboard instances` lists them)\n", strings.Join(others, ", "))
		}
	}
	return nil
}

func printStatus(name, dir string) error {
	h, err := loadHub(dir)
	if err != nil {
		return err
	}
	svc, state := serviceState(name)
	if svc != nil {
		state += " (" + svc.Name + ")"
	}
	health := "not responding"
	if resp, err := localClient().Get(localURL(h.cfg) + "/api/health"); err == nil {
		var body struct {
			Version string `json:"version"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		health = "OK (version " + body.Version + ")"
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
	hub := h.cfg.HubName
	if hub == "" {
		hub = "(not set up yet)"
	}
	fmt.Printf("Instance:    %s\n", name)
	fmt.Printf("Hub:         %s\n", hub)
	fmt.Printf("Service:     %s\n", state)
	fmt.Printf("Health:      %s\n", health)
	fmt.Printf("Listen:      %s\n", h.cfg.Listen)
	fmt.Printf("Address:     %s\n", strings.Join(server.HubURLs(h.cfg.Listen, h.cfg.ExtraURLs), "\n             "))
	fmt.Printf("Sessions:    %d\n", len(sessions))
	fmt.Printf("Machine:     This PC + %d remote\n", remote)
	fmt.Printf("Device:      %d\n", len(devices))
	fmt.Printf("Data:        %s\n", h.st.Dir)
	return nil
}

func cmdLogs(args []string) error {
	fs, dir := newFlags("logs")
	follow := fs.Bool("f", false, "follow the log")
	if err := fs.Parse(args); err != nil {
		return err
	}
	inst, err := resolveFlags(fs, *dir)
	if err != nil {
		return err
	}
	if err := needInstance(inst); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "linux":
		a := []string{"--user", "-u", instance.ServiceName(inst.Name), "-n", "200", "--no-pager"}
		if *follow {
			a = append(a, "-f")
		}
		return run("journalctl", a...)
	case "darwin":
		a := []string{"-n", "200"}
		if *follow {
			a = append(a, "-f")
		}
		return run("tail", append(a, filepath.Join(inst.DataDir, "nextboard.log"))...)
	}
	return errors.New("unsupported OS")
}

// instanceRow is one line of `nextboard instances`.
type instanceRow struct {
	instance.Instance
	// State is the service's (active, inactive, failed…), or running/stopped for a hub
	// without one.
	State   string   `json:"state"`
	Version string   `json:"version,omitempty"`
	URLs    []string `json:"urls"`
}

func cmdInstances(args []string) error {
	fs, _ := newFlags("instances")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ins, err := instance.List()
	if err != nil {
		return err
	}
	rows := make([]instanceRow, 0, len(ins))
	for _, in := range ins {
		r := instanceRow{Instance: in, State: "stopped"}
		if in.Running {
			r.State = "running"
		}
		if in.Service != nil {
			_, r.State = serviceState(in.Name)
		}
		if in.Running || r.State == "active" || r.State == "loaded" {
			if h, err := instance.CheckHealth(in.Listen, 1500*time.Millisecond); err == nil {
				r.Version = h.Version
			}
		}
		var extra []string
		if raw, err := os.ReadFile(in.ConfigFile); err == nil {
			var c config.Config
			if json.Unmarshal(raw, &c) == nil {
				extra = c.ExtraURLs
			}
		}
		r.URLs = server.HubURLs(in.Listen, extra)
		rows = append(rows, r)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"items": rows})
	}
	if len(rows) == 0 {
		fmt.Println("No NextBoard instance on this machine yet. Install one with `nextboard install`.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tLISTEN\tVERSION\tSERVICE\tDATA FOLDER")
	for _, r := range rows {
		svc := "none"
		if r.Service != nil {
			svc = r.Service.Name
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.State, r.Listen, orDash(r.Version), svc, r.DataDir)
	}
	tw.Flush()
	for _, r := range rows {
		for _, p := range r.Problems {
			fmt.Printf("problem: %s: %s\n", r.Name, p)
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// needInstance refuses to act on a named instance that is not there yet, where the command
// would otherwise make its folder as a side effect.
func needInstance(r instance.Resolved) error {
	if r.Exists || r.Name == instance.Default || r.From == instance.FromDataDir || r.From == instance.FromEnvHome {
		return nil
	}
	if _, err := os.Stat(r.DataDir); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fmt.Errorf("there is no instance %s on this machine (no %s); make it with `nextboard install --instance %s`", r.Name, r.DataDir, r.Name)
}
