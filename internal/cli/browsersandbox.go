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
	"github.com/azamrizki/nextboard/internal/instance"
)

// cmdBrowserSandbox lets NextBoard Chromium make the user namespaces its sandbox is built on, where
// Linux restricts them (Ubuntu 23.10 and later), with an AppArmor profile like those browsers
// installed from packages have (docs/API-gui.md §8.3). It needs root. Under sudo the default data
// folder would be root's, so it is the invoking user's unless --data-dir names one. Each instance
// has a profile of its own (nextboard-browser, nextboard-browser-<name>), so allowing one
// instance's browser never replaces another's.
func cmdBrowserSandbox(args []string) error {
	fs, dir := newFlags("browser-sandbox")
	printOnly := fs.Bool("print", false, "print the profile instead of installing it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := fs.Lookup("instance").Value.String()
	var inst instance.Resolved
	var err error
	if sudoHome := sudoUserHome(); sudoHome != "" && !flagGiven(fs, "data-dir") {
		// root's own folders are not the user's: the user's instance, by name
		n := name
		if n == "" {
			n = instance.Default
		}
		inst, err = instance.Resolve(instance.Request{DataDir: instance.DirIn(sudoHome, n), Instance: n})
	} else {
		inst, err = resolveFlags(fs, *dir)
	}
	if err != nil {
		return err
	}
	abs := inst.DataDir
	profile, err := gui.SandboxProfileFor(abs, inst.Name)
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
		return fmt.Errorf("this needs root: sudo %s --data-dir %s", withInstance(self+" browser-sandbox", inst.Name), abs)
	}
	path, err := gui.SandboxProfileFile(inst.Name)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || !fi.IsDir() {
		return errors.New("AppArmor is not set up on this machine, so NextBoard Chromium needs no profile")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(profile), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	parser := "apparmor_parser"
	for _, p := range []string{"/usr/sbin/apparmor_parser", "/sbin/apparmor_parser"} {
		if _, err := os.Stat(p); err == nil {
			parser = p
			break
		}
	}
	if out, err := exec.Command(parser, "-r", path).CombinedOutput(); err != nil {
		return fmt.Errorf("loading the profile: %v: %s", err, out)
	}
	fmt.Printf("NextBoard Chromium in %s may now make its sandbox (%s).\n", filepath.Join(abs, "browsers"), path)
	return nil
}

// sudoUserHome is the home folder of the user who ran sudo, "" when not under sudo.
func sudoUserHome() string {
	name := os.Getenv("SUDO_USER")
	if name == "" || name == "root" {
		return ""
	}
	u, err := user.Lookup(name)
	if err != nil {
		return ""
	}
	return u.HomeDir
}

func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}
