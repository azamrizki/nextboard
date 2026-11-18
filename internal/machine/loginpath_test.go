package machine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// What ~/.zshrc puts on PATH (nvm's node, …) reaches the consoles: a login shell alone does not
// read it, and a Claude Code plugin hook running `node` then failed with exit 127.
func TestLoginPathReadsInteractiveRC(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	home := t.TempDir()
	extra := filepath.Join(home, "nvm-node-bin")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("echo rc-noise\nexport PATH=\""+extra+":$PATH\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	if p := readLoginPath(zsh); !strings.HasPrefix(p, extra+":") {
		t.Fatalf("PATH %q lacks what .zshrc adds", p)
	}
	if p := pathFromShell(zsh, "-lc"); strings.Contains(p, extra) {
		t.Fatalf("a login shell read .zshrc after all: %q (the test proves nothing)", p)
	}
}

// A shell that cannot run gives the hub's own PATH.
func TestLoginPathFallsBack(t *testing.T) {
	t.Setenv("PATH", "/nb/fallback:/usr/bin:/bin")
	if p := readLoginPath("/nonexistent/zsh"); p != "/nb/fallback:/usr/bin:/bin" {
		t.Fatalf("PATH %q", p)
	}
}
