package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

const noBwrap = "The project's sandbox needs bubblewrap (bwrap), which is not on this PC; the command did not run."

// lookBwrap finds bubblewrap; tests replace it.
var lookBwrap = func() (string, error) { return exec.LookPath("bwrap") }

// sandboxArgs are bubblewrap's arguments for a command in the project's sandbox, up to the
// command itself: the whole file system read-only, writable only where the agent works (the
// working folder, the agent's other folders, the sandbox's write list and the temp folder),
// and the network only when the sandbox allows it.
//
// --unshare-pid gives the command a PID namespace of its own. --new-session moves the
// command out of the process group that a timeout or a Stop ends; the namespace is what still
// ends all of it then, since bubblewrap's end takes every process in it along.
func sandboxArgs(bwrap string, env *Env) []string {
	args := []string{bwrap, "--die-with-parent", "--new-session", "--unshare-pid",
		"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
	seen := map[string]bool{}
	bind := func(p string) {
		if p == "" {
			return
		}
		if !filepath.IsAbs(p) {
			p = resolve(env, p)
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return // missing: bubblewrap cannot bind it
		}
		if st, err := os.Stat(real); err != nil || seen[real] || real == "/" || !st.IsDir() && !st.Mode().IsRegular() {
			return
		}
		seen[real] = true
		args = append(args, "--bind", real, real)
	}
	bind(env.Cwd)
	for _, d := range env.AddDirs {
		bind(d)
	}
	for _, d := range env.Sandbox.Write {
		bind(d)
	}
	bind(os.TempDir())
	if !env.Sandbox.Network {
		args = append(args, "--unshare-net")
	}
	if env.Cwd != "" {
		args = append(args, "--chdir", env.Cwd)
	}
	return append(args, "--")
}
