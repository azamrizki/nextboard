package gui

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The browser extension that keeps the passkeys of sites in the vault (docs/API-vault.md). A page
// cannot be reached from outside without one. It is written to disk for the browser to load, with
// the hub's loopback address and a token beside it. It holds no key: those stay in the vault.

//go:embed passkeyext/*
var passkeyExtFiles embed.FS

// PasskeyExtension tells what a browser needs to load the extension. A nil one means it is off.
type PasskeyExtension struct {
	mu    sync.Mutex
	hub   string
	token string
	on    bool
	dir   string
}

// SetPasskeyExtension turns the extension on or off for browsers started from now on, and writes it.
func (m *Manager) SetPasskeyExtension(on bool, hub, token string) (string, error) {
	p := &m.passkeys
	p.mu.Lock()
	defer p.mu.Unlock()
	p.on, p.hub, p.token = on, hub, token
	p.dir = filepath.Join(m.cfg.Dir, "passkey-extension")
	if !on {
		return p.dir, nil
	}
	return p.dir, writePasskeyExtension(p.dir, hub, token)
}

// PasskeyExtensionDir is where the extension for a normally installed browser is.
func (m *Manager) PasskeyExtensionDir() string { return filepath.Join(m.cfg.Dir, "passkey-extension") }

func writePasskeyExtension(dir, hub, token string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := fs.WalkDir(passkeyExtFiles, "passkeyext", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := passkeyExtFiles.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, filepath.Base(p)), b, 0o600)
	}); err != nil {
		return err
	}
	b, err := json.Marshal(map[string]string{"hub": strings.TrimSuffix(hub, "/"), "token": token})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600)
}

// branded is Google's own build, which ignores --load-extension since Chrome 137. It takes the
// extension only through chrome://extensions, once per profile: tried on Chrome 156, where neither
// the flag, nor a folder of external extensions, nor loading over a debugging pipe lasted. The last
// also makes navigator.webdriver true, which is the sign sites refuse a login for.
func branded(bin string) bool { return strings.HasPrefix(filepath.Base(bin), "google-chrome") }

// passkeyFlags says how a browser about to start gets the extension.
func (m *Manager) passkeyFlags(pk appPackage, bin, profile string) ([]string, error) {
	p := &m.passkeys
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.on || pk.family != familyChromium || branded(bin) {
		return nil, nil
	}
	dir := p.dir
	if pk.kind != pkgNative {
		// a sandboxed package only reads inside its own folder
		dir = profile + "-passkey-extension"
		if err := writePasskeyExtension(dir, p.hub, p.token); err != nil {
			return nil, err
		}
	}
	return []string{"--load-extension=" + dir}, nil
}
