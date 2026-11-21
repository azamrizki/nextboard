// Package cli implements the `nextboard` subcommands.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/config"
	"github.com/azamrizki/nextboard/internal/connlog"
	"github.com/azamrizki/nextboard/internal/instance"
	"github.com/azamrizki/nextboard/internal/nextcode/native"
	"github.com/azamrizki/nextboard/internal/nodedap"
	"github.com/azamrizki/nextboard/internal/server"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/azamrizki/nextboard/internal/version"
)

const usage = `NextBoard — visual board for AI coding agents

Usage: nextboard <command> [options]

Commands:
  serve            Run in the foreground (used by the service, also for dev)
  install          Install the service (systemd user / LaunchAgent) and print a setup link;
                   with --instance NAME, a new instance with its own folder, ports and service
  uninstall        Remove the service (data stays unless --purge)
  status           Service status, addresses, number of sessions, machines, and devices
  logs [-f]        Show the service log
  connlog          Connection history to servers: drops, reconnects, latency
  setup-link       Create a new setup link (single use, 30 minutes)
  reset-password   Reset the admin password
  pair             Print a QR + pairing code in the terminal
  instances        Every instance on this machine: state, address, version, service, data folder
  update           Download the latest stable release (or --version tag), then restart every
                   instance's service that runs this binary
  browser-sandbox  Let NextBoard Chromium make its sandbox where Linux restricts it (run with sudo once)
  version          Print the version

Common options:
  --instance NAME  The instance to act on: its data folder is ~/.nextboard-NAME (default: the
                   default instance, ~/.nextboard, or $NEXTBOARD_INSTANCE inside a console)
  --data-dir DIR   Data folder (default ~/.nextboard, or $NEXTBOARD_HOME)
`

// Main runs the CLI and returns the process exit code.
func Main(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(rest)
	case "dap-node":
		// started by the hub for a debug session (docs/API-studio.md §7.5)
		err = nodedap.Serve(os.Stdin, os.Stdout)
	case "nextcode-agent":
		// started by the hub for a console of NextCode's own engine (docs/API-agents.md §30);
		// not in the help, like dap-node
		return native.Main(rest)
	case "install":
		err = cmdInstall(rest)
	case "uninstall":
		err = cmdUninstall(rest)
	case "status":
		err = cmdStatus(rest)
	case "logs":
		err = cmdLogs(rest)
	case "connlog":
		err = cmdConnLog(rest)
	case "setup-link":
		err = cmdSetupLink(rest)
	case "reset-password":
		err = cmdResetPassword(rest)
	case "pair":
		err = cmdPair(rest)
	case "update":
		err = cmdUpdate(rest)
	case "instances":
		err = cmdInstances(rest)
	case "browser-sandbox":
		err = cmdBrowserSandbox(rest)
	case "version", "--version", "-v":
		err = cmdVersion(rest)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 2
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("nextboard "+name, flag.ContinueOnError)
	dir := fs.String("data-dir", store.DefaultDir(), "data folder")
	fs.String("instance", "", "instance name: its data folder is ~/.nextboard-<name> (default: the default instance, ~/.nextboard)")
	return fs, dir
}

// resolveFlags is the instance a command with newFlags acts on, by its --instance and
// --data-dir.
func resolveFlags(fs *flag.FlagSet, dir string) (instance.Resolved, error) {
	return resolveInstance(fs, dir, fs.Lookup("instance").Value.String())
}

func cmdServe(args []string) error {
	fs, dir := newFlags("serve")
	listen := fs.String("listen", "", "listen address (default from config.json, 127.0.0.1:12312)")
	statusListen := fs.String("status-listen", "", "second address that serves only the public status page (default from config.json)")
	dev := fs.Bool("dev", false, "dev mode: allow the Origin of the local Vite server")
	devWebPort := fs.Int("dev-web-port", 5173, "dev mode: Vite server port")
	debugListen := fs.String("debug-listen", "", "loopback address that serves the hub's Go profiles (/debug/pprof, /debug/hub), e.g. 127.0.0.1:6060; off when empty")
	if err := fs.Parse(args); err != nil {
		return err
	}
	inst, err := resolveFlags(fs, *dir)
	if err != nil {
		return err
	}
	// the first start of a named instance makes its folder, ports and all, as install does
	if inst.Name != instance.Default && !inst.Exists && emptyOrMissing(inst.DataDir) {
		c, err := instance.Create(instance.CreateOptions{Name: inst.Name, DataDir: inst.DataDir, Listen: *listen, StatusListen: *statusListen, FolderOnly: true})
		if err != nil {
			return err
		}
		log.Printf("new instance %s: data in %s, hub on %s, previews on %d–%d", c.Instance, c.DataDir, c.Listen, c.PreviewRange[0], c.PreviewRange[1])
	}
	opts := server.Options{
		DataDir: inst.DataDir, Instance: inst.Name, Listen: *listen, StatusListen: *statusListen, Dev: *dev, DevWebPort: *devWebPort, DebugListen: *debugListen,
		PrintSetup: term.IsTerminal(int(os.Stdout.Fd())),
	}
	srv, err := server.New(opts)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return srv.Run(ctx, opts)
}

// resolveInstance is the instance a command acts on (instance.Resolve): dir counts only when
// --data-dir was given, name is the --instance flag ("" for none).
func resolveInstance(fs *flag.FlagSet, dir, name string) (instance.Resolved, error) {
	req := instance.Request{Instance: name}
	if flagGiven(fs, "data-dir") {
		req.DataDir = dir
	}
	return instance.Resolve(req)
}

func emptyOrMissing(dir string) bool {
	entries, err := os.ReadDir(dir)
	return errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0)
}

// existingInstance is the instance a command that reads it acts on (needInstance).
func existingInstance(fs *flag.FlagSet, dir string) (instance.Resolved, error) {
	inst, err := resolveFlags(fs, dir)
	if err != nil {
		return inst, err
	}
	return inst, needInstance(inst)
}

// hubInfo loads what CLI commands need to print links.
type hubInfo struct {
	st  *store.Store
	cfg *config.Config
}

func loadHub(dir string) (*hubInfo, error) {
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(st)
	if err != nil {
		return nil, err
	}
	return &hubInfo{st: st, cfg: cfg}, nil
}

func cmdSetupLink(args []string) error {
	fs, dir := newFlags("setup-link")
	if err := fs.Parse(args); err != nil {
		return err
	}
	inst, err := existingInstance(fs, *dir)
	if err != nil {
		return err
	}
	h, err := loadHub(inst.DataDir)
	if err != nil {
		return err
	}
	return printSetupLink(h)
}

func printSetupLink(h *hubInfo) error {
	as, err := auth.Open(h.st)
	if err != nil {
		return err
	}
	code, _, err := as.NewSetupCode()
	if errors.Is(err, auth.ErrAlreadySetup) {
		fmt.Println("Hub is already set up. Use `nextboard reset-password` if you forgot the password.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Println("Open this setup link (single use, valid for 30 minutes):")
	for i, u := range server.HubURLs(h.cfg.Listen, h.cfg.ExtraURLs) {
		if i == 3 {
			break
		}
		fmt.Printf("  %s/setup?code=%s\n", u, code)
	}
	return nil
}

func cmdResetPassword(args []string) error {
	fs, dir := newFlags("reset-password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	inst, err := existingInstance(fs, *dir)
	if err != nil {
		return err
	}
	st, err := store.Open(inst.DataDir)
	if err != nil {
		return err
	}
	as, err := auth.Open(st)
	if err != nil {
		return err
	}
	pw1, err := readPassword("New password (min 8 characters): ")
	if err != nil {
		return err
	}
	pw2, err := readPassword("Repeat password: ")
	if err != nil {
		return err
	}
	if pw1 != pw2 {
		return errors.New("passwords do not match")
	}
	if err := as.SetPassword(pw1); err != nil {
		return err
	}
	fmt.Println("Admin password changed. All browsers need to log in again.")
	return nil
}

func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	var s string
	_, err := fmt.Fscanln(os.Stdin, &s)
	return strings.TrimSpace(s), err
}

// cmdConnLog reads the connection log directly, so it also works while the hub
// is down, which is often when it is needed.
func cmdConnLog(args []string) error {
	fs, dir := newFlags("connlog")
	mach := fs.String("machine", "", "only this machine (name or id)")
	n := fs.Int("n", 50, "number of events")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	inst, err := existingInstance(fs, *dir)
	if err != nil {
		return err
	}
	l, err := connlog.Open(filepath.Join(inst.DataDir, "logs"))
	if err != nil {
		return err
	}
	items := l.Read(connlog.Query{Machine: *mach, Limit: *n})
	sum := connlog.Summarize(items)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"items": items, "summary": sum})
	}
	if len(items) == 0 {
		fmt.Println("No events recorded yet.")
		return nil
	}
	for i := len(items) - 1; i >= 0; i-- {
		e := items[i]
		name := e.Machine
		if name == "" {
			name = "hub"
		}
		fmt.Printf("%s  %-16s %s\n", e.At.Local().Format("2006-01-02 15:04:05"), name, connlog.Describe(e))
	}
	fmt.Printf("\nSummary of %d events: %d disconnects %v, average downtime %s (max %s), average latency %d ms (max %d ms), %d tool failures, %d unexpected hub exits.\n",
		len(items), sum.Disconnects, sum.Reasons, time.Duration(sum.AvgDowntimeMs)*time.Millisecond, time.Duration(sum.MaxDowntimeMs)*time.Millisecond,
		sum.RTTAvgMs, sum.RTTMaxMs, sum.ToolFailed, sum.UncleanExits)
	return nil
}

// cmdVersion prints this binary's build; with --instance (or --data-dir), also the build that
// instance's hub runs, which differs until it restarts after an update.
func cmdVersion(args []string) error {
	fmt.Println(version.Info())
	if len(args) == 0 {
		return nil
	}
	fs, dir := newFlags("version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	inst, err := existingInstance(fs, *dir)
	if err != nil {
		return err
	}
	listen := config.DefaultListen
	if raw, err := os.ReadFile(filepath.Join(inst.DataDir, config.File)); err == nil {
		var c config.Config
		if json.Unmarshal(raw, &c) == nil && c.Listen != "" {
			listen = c.Listen
		}
	}
	h, err := instance.CheckHealth(listen, 3*time.Second)
	if err != nil {
		fmt.Printf("Instance %s (%s) is not running.\n", inst.Name, listen)
		return nil
	}
	fmt.Printf("Instance %s (%s) runs %s (commit %s).\n", inst.Name, listen, h.Version, h.Commit)
	return nil
}
