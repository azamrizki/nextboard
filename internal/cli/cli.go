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

const usage = `NextBoard — visual board untuk AI coding agents

Pemakaian: nextboard <perintah> [opsi]

Perintah:
  serve            Jalan di foreground (dipakai service, juga untuk dev)
  install          Pasang service (systemd user / LaunchAgent) dan cetak link setup
  uninstall        Lepas service (data tetap ada kecuali --purge)
  status           Status service, alamat, jumlah sesi, machine, dan device
  logs [-f]        Tampilkan log service
  connlog          Riwayat koneksi ke server: putus, sambung ulang, latensi
  setup-link       Buat link setup baru (sekali pakai, 30 menit)
  reset-password   Reset password admin
  pair             Cetak QR + kode pairing di terminal
  update           Unduh rilis terbaru lalu restart service
  version          Cetak versi

Opsi umum:
  --data-dir DIR   Folder data (default ~/.nextboard, atau $NEXTBOARD_HOME)
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
		fmt.Fprintf(os.Stderr, "perintah tidak dikenal: %s\n\n%s", cmd, usage)
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
	dir := fs.String("data-dir", store.DefaultDir(), "folder data")
	return fs, dir
}

func cmdServe(args []string) error {
	fs, dir := newFlags("serve")
	listen := fs.String("listen", "", "alamat listen (default dari config.json, 127.0.0.1:12312)")
	dev := fs.Bool("dev", false, "mode dev: izinkan Origin server Vite lokal")
	devWebPort := fs.Int("dev-web-port", 5173, "mode dev: port server Vite")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := server.Options{
		DataDir: *dir, Listen: *listen, Dev: *dev, DevWebPort: *devWebPort,
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
		fmt.Println("Hub sudah di-setup. Pakai `nextboard reset-password` kalau lupa password.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Println("Buka link setup ini (sekali pakai, berlaku 30 menit):")
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
	pw1, err := readPassword("Password baru (min 8 karakter): ")
	if err != nil {
		return err
	}
	pw2, err := readPassword("Ulangi password: ")
	if err != nil {
		return err
	}
	if pw1 != pw2 {
		return errors.New("password tidak sama")
	}
	if err := as.SetPassword(pw1); err != nil {
		return err
	}
	fmt.Println("Password admin diganti. Semua browser perlu login ulang.")
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
	mach := fs.String("machine", "", "hanya machine ini (nama atau id)")
	n := fs.Int("n", 50, "jumlah kejadian")
	asJSON := fs.Bool("json", false, "keluaran JSON")
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
		fmt.Println("Belum ada kejadian tercatat.")
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
	fmt.Printf("\nRingkasan %d kejadian: %d putus %v, putus rata-rata %s (maks %s), latensi rata-rata %d ms (maks %d ms), %d tool gagal, %d kali hub mati mendadak.\n",
		len(items), sum.Disconnects, sum.Reasons, time.Duration(sum.AvgDowntimeMs)*time.Millisecond, time.Duration(sum.MaxDowntimeMs)*time.Millisecond,
		sum.RTTAvgMs, sum.RTTMaxMs, sum.ToolFailed, sum.UncleanExits)
	return nil
}
