package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/azamrizki/nextboard/internal/gui"
	"github.com/azamrizki/nextboard/internal/store"
)

// cmdBrowserSandbox lets NextBoard Chromium make the user namespaces its sandbox is built on, where
// Linux restricts them (Ubuntu 23.10 and later), with an AppArmor profile like those browsers
// installed from packages have (docs/API-gui.md §8.3). It needs root. Under sudo the default data
// folder would be root's, so it is the invoking user's unless --data-dir names one.
func cmdBrowserSandbox(args []string) error {
	fs, dir := newFlags("browser-sandbox")
	printOnly := fs.Bool("print", false, "print the profile instead of installing it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data := *dir
	if !flagGiven(fs, "data-dir") {
		if name := os.Getenv("SUDO_USER"); name != "" && name != "root" {
			if u, err := user.Lookup(name); err == nil {
				data = filepath.Join(u.HomeDir, ".nextboard")
			}
		} else {
			data = store.DefaultDir()
		}
	}
	abs, err := filepath.Abs(data)
	if err != nil {
		return err
	}
	profile, err := gui.SandboxProfile(abs)
	if err != nil {
		return err
	}
	if *printOnly {
		fmt.Print(profile)
		return nil
	}
	if runtime.GOOS != "linux" {
		return errors.New("only Linux restricts the browser's sandbox this way; nothing to do")
	}
	if os.Geteuid() != 0 {
		self, _ := os.Executable()
		return fmt.Errorf("this needs root: sudo %s browser-sandbox --data-dir %s", self, abs)
	}
	if fi, err := os.Stat(filepath.Dir(gui.SandboxProfilePath)); err != nil || !fi.IsDir() {
		return errors.New("AppArmor is not set up on this machine, so NextBoard Chromium needs no profile")
	}
	tmp := gui.SandboxProfilePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(profile), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, gui.SandboxProfilePath); err != nil {
		return err
	}
	parser := "apparmor_parser"
	for _, p := range []string{"/usr/sbin/apparmor_parser", "/sbin/apparmor_parser"} {
		if _, err := os.Stat(p); err == nil {
			parser = p
			break
		}
	}
	if out, err := exec.Command(parser, "-r", gui.SandboxProfilePath).CombinedOutput(); err != nil {
		return fmt.Errorf("loading the profile: %v: %s", err, out)
	}
	fmt.Printf("NextBoard Chromium in %s may now make its sandbox (%s).\n", filepath.Join(abs, "browsers"), gui.SandboxProfilePath)
	return nil
}

func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}
