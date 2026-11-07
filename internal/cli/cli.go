// Package cli implements the `nextboard` subcommands.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	"github.com/azamrizki/nextboard/internal/nodedap"
	"github.com/azamrizki/nextboard/internal/server"
	"github.com/azamrizki/nextboard/internal/store"
	"github.com/azamrizki/nextboard/internal/version"
)

const usage = `NextBoard — visual board for AI coding agents

Usage: nextboard <command> [options]

Commands:
  serve            Run in the foreground (used by the service, also for dev)
  install          Install the service (systemd user / LaunchAgent) and print a setup link
  uninstall        Remove the service (data stays unless --purge)
  status           Service status, addresses, number of sessions, machines, and devices
  logs [-f]        Show the service log
  connlog          Connection history to servers: drops, reconnects, latency
  setup-link       Create a new setup link (single use, 30 minutes)
  reset-password   Reset the admin password
  pair             Print a QR + pairing code in the terminal
  update           Download the latest release, then restart the service
  version          Print the version

Common options:
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
	case "version", "--version", "-v":
		fmt.Println("nextboard", version.Version)
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
	return fs, dir
}

func cmdServe(args []string) error {
	fs, dir := newFlags("serve")
	listen := fs.String("listen", "", "listen address (default from config.json, 127.0.0.1:12312)")
	statusListen := fs.String("status-listen", "", "second address that serves only the public status page (default from config.json)")
	dev := fs.Bool("dev", false, "dev mode: allow the Origin of the local Vite server")
	devWebPort := fs.Int("dev-web-port", 5173, "dev mode: Vite server port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := server.Options{
		DataDir: *dir, Listen: *listen, StatusListen: *statusListen, Dev: *dev, DevWebPort: *devWebPort,
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
	h, err := loadHub(*dir)
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
	st, err := store.Open(*dir)
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
	l, err := connlog.Open(filepath.Join(*dir, "logs"))
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
