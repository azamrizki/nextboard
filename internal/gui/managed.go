package gui

// NextBoard Chromium (docs/API-gui.md §8.3): Google's Chrome for Testing of the stable channel,
// which the hub downloads on request into <data>/browsers and keeps up to date. It is Chromium
// with Chrome's media codecs, and unlike branded Chrome it still takes --load-extension, so the
// passkey extension loads by itself. Browser tiles try it first once it is there.
//
// Ubuntu 23.10 and later let a program make the user namespaces Chromium's sandbox is built on
// only when an AppArmor profile names it, and a downloaded browser has none. So it runs on the
// setuid sandbox helper of a browser installed from a package (Chrome's chrome-sandbox), or under
// the profile the owner installs once with `sudo nextboard browser-sandbox`. Never without one.

import (
	"archive/zip"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// ManagedBrowserID is the app id of NextBoard Chromium in tiles and the API.
	ManagedBrowserID = "nextboard-chromium"
	managedName      = "NextBoard Chromium"
	cftIndexURL      = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"
	// maxBrowserZip bounds a download: a release is about 170 MB.
	maxBrowserZip     = 600 << 20
	browserCheckEvery = 24 * time.Hour
	browserFirstCheck = 2 * time.Minute
	// SandboxProfilePath is the AppArmor profile `nextboard browser-sandbox` writes.
	SandboxProfilePath = "/etc/apparmor.d/nextboard-browser"
)

// The only host a browser is downloaded from.
var browserHosts = []string{"storage.googleapis.com"}

// managedFlags: Chrome for Testing shows a bar saying it is for automated testing only. That is
// because it does not update itself; NextBoard updates it instead.
var managedFlags = []string{"--disable-infobars"}

// sandboxHelpers are the setuid helpers browsers installed from packages bring, by where they put
// them. Any one runs the sandbox of a Chromium of another version.
var sandboxHelpers = []string{
	"/opt/google/chrome/chrome-sandbox",
	"/opt/google/chrome-beta/chrome-sandbox",
	"/opt/google/chrome-unstable/chrome-sandbox",
	"/usr/lib/chromium/chrome-sandbox",
	"/usr/lib/chromium-browser/chrome-sandbox",
	"/opt/chromium.org/chromium/chrome-sandbox",
	"/opt/brave.com/brave/chrome-sandbox",
	"/opt/microsoft/msedge/msedge-sandbox",
	"/opt/vivaldi/vivaldi-sandbox",
}

const userNSRestriction = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"

// Sandbox modes of BrowserStatus.
const (
	SandboxNamespaces = "namespaces" // nothing stops the browser's own namespace sandbox
	SandboxHelper     = "helper"     // a setuid helper of an installed browser runs it
	SandboxSetup      = "setup"      // neither: the owner installs the AppArmor profile once
)

// BrowserStatus is GET /api/gui/browser.
type BrowserStatus struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Supported  bool       `json:"supported"`
	Installed  string     `json:"installed,omitempty"`
	Latest     string     `json:"latest,omitempty"`
	CheckedAt  *time.Time `json:"checkedAt,omitempty"`
	AutoUpdate bool       `json:"autoUpdate"`
	// State: idle, checking, downloading, installing or error.
	State string `json:"state"`
	Done  int64  `json:"done,omitempty"`
	Total int64  `json:"total,omitempty"`
	Error string `json:"error,omitempty"`
	// Sandbox: how it is sandboxed here; SetupCommand when the owner must allow it first.
	Sandbox       string `json:"sandbox,omitempty"`
	SandboxHelper string `json:"sandboxHelper,omitempty"`
	SetupCommand  string `json:"setupCommand,omitempty"`
	Dir           string `json:"dir,omitempty"`
}

// browserState is <data>/browsers/state.json.
type browserState struct {
	Installed  string     `json:"installed,omitempty"`
	Latest     string     `json:"latest,omitempty"`
	CheckedAt  *time.Time `json:"checkedAt,omitempty"`
	AutoUpdate *bool      `json:"autoUpdate,omitempty"` // nil = on
}

// managedBrowser is the Manager's part for it.
type managedBrowser struct {
	mu      sync.Mutex
	saved   browserState
	loaded  bool
	state   string
	done    int64
	total   int64
	err     string
	busy    bool
	stop    chan struct{}
	started bool

	// tests
	index        string
	client       *http.Client
	hosts        []string
	helpers      []string
	restrictFile string
	profileFile  string
	platform     string
}

func (m *Manager) browsersDir() string { return m.cfg.Browsers }

func (mb *managedBrowser) indexURL() string { return cmp.Or(mb.index, cftIndexURL) }

// cftPlatform names this machine in the index and the folder its zip unpacks to.
func (mb *managedBrowser) cftPlatform() (platform, folder string, ok bool) {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	if mb.platform != "" {
		goos, goarch, _ = strings.Cut(mb.platform, "/")
	}
	if goos != "linux" {
		return "", "", false
	}
	switch goarch {
	case "amd64":
		return "linux64", "chrome-linux64", true
	case "arm64":
		return "linux-arm64", "chrome-linux-arm64", true
	}
	return "", "", false
}

// loadLocked reads state.json once.
func (m *Manager) loadBrowserLocked() {
	mb := &m.mb
	if mb.loaded {
		return
	}
	mb.loaded = true
	if mb.state == "" {
		mb.state = "idle"
	}
	b, err := os.ReadFile(filepath.Join(m.browsersDir(), "state.json"))
	if err == nil {
		_ = json.Unmarshal(b, &mb.saved)
	}
}

func (m *Manager) saveBrowserLocked() error {
	if err := os.MkdirAll(m.browsersDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m.mb.saved, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(m.browsersDir(), "state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.browsersDir(), "state.json"))
}

// managedBinary is the installed browser's program, "" when there is none.
func (m *Manager) managedBinaryLocked() string {
	_, folder, ok := m.mb.cftPlatform()
	if !ok || m.mb.saved.Installed == "" || m.browsersDir() == "" {
		return ""
	}
	bin := filepath.Join(m.browsersDir(), "chrome-"+m.mb.saved.Installed, folder, "chrome")
	if !isExecutable(bin) {
		return ""
	}
	return bin
}

// managedEntry is the desktop entry tiles see for it, nil when it is not installed.
func (m *Manager) managedEntry() *entry {
	m.mb.mu.Lock()
	defer m.mb.mu.Unlock()
	m.loadBrowserLocked()
	bin := m.managedBinaryLocked()
	if bin == "" {
		return nil
	}
	return &entry{
		ID: ManagedBrowserID, Source: bin, Type: "Application", Name: managedName,
		Comment:    "Chrome for Testing " + m.mb.saved.Installed + ", kept up to date by NextBoard",
		Icon:       filepath.Join(filepath.Dir(bin), "product_logo_48.png"),
		Exec:       quoteExec(bin) + " %U",
		Categories: []string{"Network", "WebBrowser"},
		MimeTypes:  []string{"text/html", "x-scheme-handler/http", "x-scheme-handler/https"},
	}
}

// quoteExec quotes a path for an Exec line.
func quoteExec(p string) string {
	if !strings.ContainsAny(p, " \t\"'\\$`") {
		return p
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`)
	return `"` + r.Replace(p) + `"`
}

// entries are the desktop entries of This PC, with NextBoard Chromium when installed.
func (m *Manager) entries() map[string]*entry {
	all := scanEntries(dataDirs())
	if e := m.managedEntry(); e != nil {
		all[e.ID] = e
	}
	return all
}

// sandbox says how a downloaded Chromium is sandboxed here.
func (mb *managedBrowser) sandbox() (mode, helper string) {
	restrict := cmp.Or(mb.restrictFile, userNSRestriction)
	b, err := os.ReadFile(restrict)
	if err != nil || strings.TrimSpace(string(b)) != "1" {
		return SandboxNamespaces, ""
	}
	if _, err := os.Stat(cmp.Or(mb.profileFile, SandboxProfilePath)); err == nil {
		return SandboxNamespaces, ""
	}
	helpers := sandboxHelpers
	if mb.helpers != nil {
		helpers = mb.helpers
	}
	for _, h := range helpers {
		if setuidRoot(h) {
			return SandboxHelper, h
		}
	}
	return SandboxSetup, ""
}

// SandboxProfile is the AppArmor profile that lets the browsers in a hub's data folder make user
// namespaces, as Ubuntu's own profiles do for browsers installed from packages.
func SandboxProfile(dataDir string) (string, error) {
	if !filepath.IsAbs(dataDir) || strings.ContainsAny(dataDir, "\"\n\r\x00*?[]{}^") {
		return "", fmt.Errorf("data folder %q cannot go in an AppArmor profile", dataDir)
	}
	glob := filepath.Join(filepath.Clean(dataDir), "browsers", "chrome-*", "chrome-linux*", "chrome")
	return `# NextBoard Chromium, the browser of NextBoard's web tiles (written by nextboard browser-sandbox).
# Like Ubuntu's profiles of browsers installed from packages: it only lets the program make the
# user namespaces Chromium's sandbox is built on.
abi <abi/4.0>,
include <tunables/global>

profile nextboard-browser "` + glob + `" flags=(unconfined) {
  userns,

  include if exists <local/nextboard-browser>
}
`, nil
}

func (m *Manager) setupCommand() string {
	self, err := os.Executable()
	if err != nil {
		self = "nextboard"
	}
	return "sudo " + shellQuote(self) + " browser-sandbox --data-dir " + shellQuote(filepath.Dir(m.browsersDir()))
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// managedEnv is what the browser's process needs to be sandboxed; an error when it cannot be.
func (m *Manager) managedEnv() ([]string, error) {
	mode, helper := m.mb.sandbox()
	switch mode {
	case SandboxHelper:
		return []string{"CHROME_DEVEL_SANDBOX=" + helper}, nil
	case SandboxSetup:
		return nil, newErr(http.StatusConflict, "browser_sandbox", "%s needs to be allowed its sandbox once. Run: %s", managedName, m.setupCommand())
	}
	return nil, nil
}

// BrowserStatus reports NextBoard Chromium.
func (m *Manager) BrowserStatus() BrowserStatus {
	mb := &m.mb
	mb.mu.Lock()
	defer mb.mu.Unlock()
	m.loadBrowserLocked()
	_, _, ok := mb.cftPlatform()
	st := BrowserStatus{
		ID: ManagedBrowserID, Name: managedName, Supported: ok && m.browsersDir() != "",
		Latest: mb.saved.Latest, CheckedAt: mb.saved.CheckedAt, AutoUpdate: mb.saved.AutoUpdate == nil || *mb.saved.AutoUpdate,
		State: mb.state, Done: mb.done, Total: mb.total, Error: mb.err, Dir: m.browsersDir(),
	}
	if m.managedBinaryLocked() != "" {
		st.Installed = mb.saved.Installed
	}
	if st.Supported {
		st.Sandbox, st.SandboxHelper = mb.sandbox()
		if st.Sandbox == SandboxSetup {
			st.SetupCommand = m.setupCommand()
		}
	}
	return st
}

func (m *Manager) publishBrowser() {
	m.publish(map[string]any{"type": "guiBrowser", "browser": m.BrowserStatus()})
}

// InstallBrowser downloads the latest stable release, or updates to it. It runs on its own; the
// status says how far it is.
func (m *Manager) InstallBrowser() error { return m.startBrowserJob(true) }

// CheckBrowser asks which release is the latest, and updates to it when updates are on.
func (m *Manager) CheckBrowser() error { return m.startBrowserJob(false) }

func (m *Manager) startBrowserJob(install bool) error {
	mb := &m.mb
	mb.mu.Lock()
	m.loadBrowserLocked()
	if _, _, ok := mb.cftPlatform(); !ok || m.browsersDir() == "" {
		mb.mu.Unlock()
		return newErr(http.StatusNotImplemented, "browser_unsupported", "%s is for Linux on x86-64 or ARM64", managedName)
	}
	if mb.busy {
		mb.mu.Unlock()
		return newErr(http.StatusConflict, "browser_busy", "%s is already being checked or downloaded", managedName)
	}
	mb.busy, mb.state, mb.err, mb.done, mb.total = true, "checking", "", 0, 0
	mb.mu.Unlock()
	m.publishBrowser()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err := m.browserJob(ctx, install)
		mb.mu.Lock()
		mb.busy, mb.done, mb.total = false, 0, 0
		if err != nil {
			mb.state, mb.err = "error", err.Error()
		} else {
			mb.state, mb.err = "idle", ""
		}
		mb.mu.Unlock()
		m.publishBrowser()
	}()
	return nil
}

func (m *Manager) setBrowserState(state string, done, total int64) {
	m.mb.mu.Lock()
	m.mb.state, m.mb.done, m.mb.total = state, done, total
	m.mb.mu.Unlock()
}

// browserJob checks the index, and installs when asked to or when an update is due.
func (m *Manager) browserJob(ctx context.Context, install bool) error {
	mb := &m.mb
	version, zipURL, err := m.latestBrowser(ctx)
	now := time.Now().UTC()
	mb.mu.Lock()
	if err == nil {
		mb.saved.Latest = version
	}
	mb.saved.CheckedAt = &now
	installed := ""
	if m.managedBinaryLocked() != "" {
		installed = mb.saved.Installed
	}
	auto := mb.saved.AutoUpdate == nil || *mb.saved.AutoUpdate
	_ = m.saveBrowserLocked()
	mb.mu.Unlock()
	if err != nil {
		return err
	}
	switch {
	case installed == version:
		return nil
	case installed == "" && !install:
		return nil
	case installed != "" && !install && (!auto || !newerVersion(version, installed)):
		return nil
	}
	return m.installRelease(ctx, version, zipURL)
}

// latestBrowser reads the index of Chrome for Testing: the stable version and this machine's zip.
func (m *Manager) latestBrowser(ctx context.Context) (version, zipURL string, err error) {
	platform, _, _ := m.mb.cftPlatform()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.mb.indexURL(), nil)
	if err != nil {
		return "", "", err
	}
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("could not reach the list of releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("the list of releases answered %s", resp.Status)
	}
	var idx struct {
		Channels map[string]struct {
			Version   string `json:"version"`
			Downloads map[string][]struct {
				Platform string `json:"platform"`
				URL      string `json:"url"`
			} `json:"downloads"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&idx); err != nil {
		return "", "", fmt.Errorf("the list of releases is not readable: %w", err)
	}
	st, ok := idx.Channels["Stable"]
	if !ok || !validBrowserVersion(st.Version) {
		return "", "", errors.New("the list of releases has no stable version")
	}
	for _, d := range st.Downloads["chrome"] {
		if d.Platform == platform {
			if err := m.allowedURL(d.URL); err != nil {
				return "", "", err
			}
			return st.Version, d.URL, nil
		}
	}
	return "", "", fmt.Errorf("the stable release has no build for %s", platform)
}

func (m *Manager) httpClient() *http.Client {
	if m.mb.client != nil {
		return m.mb.client
	}
	return &http.Client{Timeout: 25 * time.Minute}
}

func (m *Manager) allowedURL(raw string) error {
	u, err := url.Parse(raw)
	if err == nil {
		if m.mb.hosts != nil {
			// tests: a local server, by host and port
			if slices.Contains(m.mb.hosts, u.Host) {
				return nil
			}
		} else if u.Scheme == "https" && u.Port() == "" && slices.Contains(browserHosts, u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("refusing to download a browser from %q", raw)
}

// validBrowserVersion is four dotted numbers, which also makes it safe in a folder name.
func validBrowserVersion(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err != nil || n < 0 || len(p) > 6 {
			return false
		}
	}
	return true
}

// newerVersion compares dotted versions number by number.
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// installRelease downloads one release, unpacks it beside the others, checks that it runs, and
// makes it the one tiles start. Browsers already open keep theirs until they are closed.
func (m *Manager) installRelease(ctx context.Context, version, zipURL string) error {
	dir := m.browsersDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	zipPath := filepath.Join(dir, ".download.zip")
	defer os.Remove(zipPath)
	if err := m.download(ctx, zipURL, zipPath); err != nil {
		return err
	}
	m.setBrowserState("installing", 0, 0)
	tmp := filepath.Join(dir, ".unpack-"+version)
	_ = os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)
	_, folder, _ := m.mb.cftPlatform()
	if err := unpackBrowser(zipPath, tmp, folder); err != nil {
		return err
	}
	bin := filepath.Join(tmp, folder, "chrome")
	if err := checkBrowserRuns(ctx, bin, version); err != nil {
		return err
	}
	final := filepath.Join(dir, "chrome-"+version)
	_ = os.RemoveAll(final)
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	m.mb.mu.Lock()
	m.mb.saved.Installed = version
	err := m.saveBrowserLocked()
	m.mb.mu.Unlock()
	if err != nil {
		return err
	}
	m.removeOldBrowsers(version)
	return nil
}

func (m *Manager) download(ctx context.Context, rawURL, to string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	// a redirect to another host is not followed
	client := *m.httpClient()
	client.CheckRedirect = func(r *http.Request, _ []*http.Request) error { return m.allowedURL(r.URL.String()) }
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("the download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the download answered %s", resp.Status)
	}
	if resp.ContentLength > maxBrowserZip {
		return fmt.Errorf("the download is %d MB, more than a browser should be", resp.ContentLength>>20)
	}
	f, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	total := resp.ContentLength
	var done int64
	last := time.Time{}
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			if done > maxBrowserZip {
				return errors.New("the download is larger than a browser should be")
			}
			if time.Since(last) > 500*time.Millisecond {
				last = time.Now()
				m.setBrowserState("downloading", done, total)
				m.publishBrowser()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("the download broke off: %w", rerr)
		}
	}
	if total > 0 && done != total {
		return fmt.Errorf("the download stopped at %d of %d bytes", done, total)
	}
	m.setBrowserState("downloading", done, done)
	return f.Close()
}

// unpackBrowser extracts the zip into dir. Only regular files and folders under the platform's
// folder are taken; a path that leaves dir, a link or a device fails the whole release.
func unpackBrowser(zipPath, dir, folder string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("the download is not a zip: %w", err)
	}
	defer r.Close()
	var size uint64
	for _, f := range r.File {
		name := filepath.Clean(filepath.FromSlash(f.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) ||
			(name != folder && !strings.HasPrefix(name, folder+string(filepath.Separator))) {
			return fmt.Errorf("unexpected file in the release: %q", f.Name)
		}
		size += f.UncompressedSize64
		if size > 3*maxBrowserZip {
			return errors.New("the release unpacks to more than a browser should")
		}
		target := filepath.Join(dir, name)
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			return fmt.Errorf("unexpected kind of file in the release: %q", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if mode.Perm()&0o111 != 0 {
			perm = 0o755
		}
		if err := extractFile(f, target, perm); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, target string, perm os.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(rc, int64(f.UncompressedSize64)+1)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// checkBrowserRuns asks the unpacked program its version, which needs no display or sandbox.
func checkBrowserRuns(ctx context.Context, bin, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("the downloaded browser does not run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), version) {
		return fmt.Errorf("the downloaded browser says it is %q, not %s", strings.TrimSpace(string(out)), version)
	}
	return nil
}

// removeOldBrowsers deletes other releases that no open tile runs.
func (m *Manager) removeOldBrowsers(keep string) {
	inUse := map[string]bool{}
	unknown := false
	m.mu.Lock()
	for _, s := range m.sessions {
		if s.pub.AppID == ManagedBrowserID {
			// one adopted after a restart does not know its release: keep them all for now
			unknown = unknown || s.managedVersion == ""
			inUse[s.managedVersion] = true
		}
	}
	m.mu.Unlock()
	if unknown {
		return
	}
	des, _ := os.ReadDir(m.browsersDir())
	for _, d := range des {
		v, ok := strings.CutPrefix(d.Name(), "chrome-")
		if !ok || !d.IsDir() || v == keep || inUse[v] {
			continue
		}
		_ = os.RemoveAll(filepath.Join(m.browsersDir(), d.Name()))
	}
}

// SetBrowserAutoUpdate turns the daily update on or off.
func (m *Manager) SetBrowserAutoUpdate(on bool) (BrowserStatus, error) {
	m.mb.mu.Lock()
	m.loadBrowserLocked()
	m.mb.saved.AutoUpdate = &on
	err := m.saveBrowserLocked()
	m.mb.mu.Unlock()
	if err != nil {
		return BrowserStatus{}, err
	}
	m.publishBrowser()
	return m.BrowserStatus(), nil
}

// RemoveBrowser deletes NextBoard Chromium, not while a tile runs it. Its tiles' profiles stay.
func (m *Manager) RemoveBrowser() error {
	m.mu.Lock()
	for _, s := range m.sessions {
		if s.pub.AppID == ManagedBrowserID && !s.stopping {
			m.mu.Unlock()
			return newErr(http.StatusConflict, "browser_in_use", "close the tiles that run %s first", managedName)
		}
	}
	m.mu.Unlock()
	mb := &m.mb
	mb.mu.Lock()
	if mb.busy {
		mb.mu.Unlock()
		return newErr(http.StatusConflict, "browser_busy", "%s is being checked or downloaded", managedName)
	}
	m.loadBrowserLocked()
	mb.saved.Installed = ""
	err := m.saveBrowserLocked()
	mb.mu.Unlock()
	if err != nil {
		return err
	}
	des, _ := os.ReadDir(m.browsersDir())
	for _, d := range des {
		if strings.HasPrefix(d.Name(), "chrome-") || strings.HasPrefix(d.Name(), ".unpack-") || d.Name() == ".download.zip" {
			_ = os.RemoveAll(filepath.Join(m.browsersDir(), d.Name()))
		}
	}
	m.publishBrowser()
	return nil
}

// startBrowserUpdates checks for a new release a while after start and then daily, while
// NextBoard Chromium is installed.
func (m *Manager) startBrowserUpdates() {
	mb := &m.mb
	mb.mu.Lock()
	if mb.started || m.browsersDir() == "" {
		mb.mu.Unlock()
		return
	}
	mb.started, mb.stop = true, make(chan struct{})
	stop := mb.stop
	mb.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTimer(browserFirstCheck)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			mb.mu.Lock()
			m.loadBrowserLocked()
			installed := m.managedBinaryLocked() != ""
			due := mb.saved.CheckedAt == nil || time.Since(*mb.saved.CheckedAt) >= browserCheckEvery-time.Minute
			mb.mu.Unlock()
			if installed && due {
				_ = m.CheckBrowser()
			}
			t.Reset(time.Hour)
		}
	}()
}

func (m *Manager) stopBrowserUpdates() {
	m.mb.mu.Lock()
	if m.mb.stop != nil {
		close(m.mb.stop)
		m.mb.stop = nil
	}
	m.mb.mu.Unlock()
}

// setuidRoot reports whether a file is a setuid program owned by root.
func setuidRoot(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode()&os.ModeSetuid == 0 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}
