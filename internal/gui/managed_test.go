package gui

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// cftServer stands in for the index of Chrome for Testing and its downloads.
type cftServer struct {
	*httptest.Server
	mu      sync.Mutex
	version string
	zips    map[string][]byte // by version
	host    string            // where the index says the zip is
}

func newCFTServer(t *testing.T) *cftServer {
	c := &cftServer{zips: map[string][]byte{}}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		switch {
		case r.URL.Path == "/index.json":
			host := c.host
			if host == "" {
				host = r.Host
			}
			dl := func(p string) map[string]string {
				return map[string]string{"platform": p, "url": "http://" + host + "/" + c.version + "/" + p + "/chrome.zip"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"channels": map[string]any{"Stable": map[string]any{
				"version": c.version, "downloads": map[string]any{"chrome": []any{dl("linux64"), dl("linux-arm64"), dl("mac-arm64")}},
			}}})
		case strings.HasSuffix(r.URL.Path, "/chrome.zip"):
			v := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0]
			if b, ok := c.zips[v]; ok {
				_, _ = w.Write(b)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(c.Close)
	u, _ := url.Parse(c.URL)
	c.host = u.Host
	return c
}

func (c *cftServer) release(t *testing.T, version string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		switch {
		case strings.HasSuffix(name, "/"):
			h.SetMode(os.ModeDir | 0o755)
		case strings.HasPrefix(body, "symlink:"):
			h.SetMode(os.ModeSymlink | 0o777)
			body = strings.TrimPrefix(body, "symlink:")
		case strings.HasPrefix(body, "#!"):
			h.SetMode(0o755)
		default:
			h.SetMode(0o644)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.version, c.zips[version] = version, buf.Bytes()
	c.mu.Unlock()
}

func goodRelease(version string) map[string]string {
	return map[string]string{
		"chrome-linux64/":                    "",
		"chrome-linux64/chrome":              "#!/bin/sh\necho 'Google Chrome for Testing " + version + " '\n",
		"chrome-linux64/product_logo_48.png": "png",
		"chrome-linux64/locales/en-US.pak":   "pak",
	}
}

func managedManager(t *testing.T, c *cftServer) *Manager {
	t.Helper()
	data := t.TempDir()
	m := New(Config{Dir: filepath.Join(data, "gui"), Browsers: filepath.Join(data, "browsers")})
	m.mb.index = c.URL + "/index.json"
	m.mb.hosts = []string{c.host}
	m.mb.client = c.Client()
	m.mb.platform = "linux/amd64"
	// a machine without the restriction, unless a test says otherwise
	m.mb.restrictFile = filepath.Join(data, "restrict")
	m.mb.profileFile = filepath.Join(data, "no-profile")
	return m
}

// settle waits for the running check or download to end.
func settle(t *testing.T, m *Manager) BrowserStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		m.mb.mu.Lock()
		busy := m.mb.busy
		m.mb.mu.Unlock()
		if !busy {
			return m.BrowserStatus()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the browser job did not end")
	return BrowserStatus{}
}

func TestManagedBrowserInstallAndUpdate(t *testing.T) {
	c := newCFTServer(t)
	m := managedManager(t, c)
	c.release(t, "200.0.1.2", goodRelease("200.0.1.2"))

	if st := m.BrowserStatus(); !st.Supported || st.Installed != "" || !st.AutoUpdate || st.Sandbox != SandboxNamespaces {
		t.Fatalf("before %+v", st)
	}
	if m.managedEntry() != nil {
		t.Fatal("an entry before anything is installed")
	}
	// a check alone downloads nothing
	if err := m.CheckBrowser(); err != nil {
		t.Fatal(err)
	}
	if st := settle(t, m); st.Installed != "" || st.Latest != "200.0.1.2" || st.CheckedAt == nil || st.State != "idle" {
		t.Fatalf("after a check %+v", st)
	}

	if err := m.InstallBrowser(); err != nil {
		t.Fatal(err)
	}
	st := settle(t, m)
	if st.Installed != "200.0.1.2" || st.State != "idle" || st.Error != "" {
		t.Fatalf("installed %+v", st)
	}
	e := m.managedEntry()
	if e == nil || e.ID != ManagedBrowserID || !e.launchable("") {
		t.Fatalf("entry %+v", e)
	}
	if rank, ok := m.browserRank(e); !ok || rank != -1 {
		t.Fatalf("NextBoard Chromium does not come first: %d %v", rank, ok)
	}
	if got := familyOf(e.Source); got != familyChromium {
		t.Fatalf("family %q", got)
	}
	if _, ok := m.entries()[ManagedBrowserID]; !ok {
		t.Fatal("tiles do not see it")
	}
	// another manager on the same folder finds it, as the hub does after a restart
	again := New(Config{Dir: m.cfg.Dir, Browsers: m.cfg.Browsers})
	again.mb.platform = "linux/amd64"
	if again.managedEntry() == nil {
		t.Fatal("a restarted hub forgets the browser")
	}

	// a newer stable release replaces it, and the old one goes
	c.release(t, "200.0.2.0", goodRelease("200.0.2.0"))
	if err := m.CheckBrowser(); err != nil {
		t.Fatal(err)
	}
	if st := settle(t, m); st.Installed != "200.0.2.0" {
		t.Fatalf("update %+v", st)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.Browsers, "chrome-200.0.1.2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old release stayed")
	}

	// with updates off, a check only learns of the next one
	if _, err := m.SetBrowserAutoUpdate(false); err != nil {
		t.Fatal(err)
	}
	c.release(t, "201.0.0.1", goodRelease("201.0.0.1"))
	if err := m.CheckBrowser(); err != nil {
		t.Fatal(err)
	}
	if st := settle(t, m); st.Installed != "200.0.2.0" || st.Latest != "201.0.0.1" || st.AutoUpdate {
		t.Fatalf("updates off %+v", st)
	}

	if err := m.RemoveBrowser(); err != nil {
		t.Fatal(err)
	}
	if st := m.BrowserStatus(); st.Installed != "" || m.managedEntry() != nil {
		t.Fatalf("removed %+v", st)
	}
	if left, _ := filepath.Glob(filepath.Join(m.cfg.Browsers, "chrome-*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// A release that is not what it should be installs nothing.
func TestManagedBrowserRefusesBadReleases(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"leaves its folder":  {"chrome-linux64/chrome": "#!/bin/sh\necho 'Google Chrome for Testing 200.0.0.1'\n", "chrome-linux64/../../evil": "x"},
		"outside the folder": {"chrome-linux64/chrome": "#!/bin/sh\necho 'Google Chrome for Testing 200.0.0.1'\n", "other/file": "x"},
		"a link":             {"chrome-linux64/chrome": "#!/bin/sh\necho 'Google Chrome for Testing 200.0.0.1'\n", "chrome-linux64/lib.so": "symlink:/etc/passwd"},
		"another version":    {"chrome-linux64/chrome": "#!/bin/sh\necho 'Google Chrome for Testing 199.0.0.0'\n"},
		"does not run":       {"chrome-linux64/chrome": "not a program"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCFTServer(t)
			m := managedManager(t, c)
			c.release(t, "200.0.0.1", files)
			if err := m.InstallBrowser(); err != nil {
				t.Fatal(err)
			}
			st := settle(t, m)
			if st.Installed != "" || st.State != "error" || st.Error == "" {
				t.Fatalf("%+v", st)
			}
			if left, _ := filepath.Glob(filepath.Join(m.cfg.Browsers, "*chrome-*")); len(left) != 0 {
				t.Fatalf("left behind: %v", left)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(m.cfg.Browsers), "evil")); err == nil {
				t.Fatal("a file landed outside the browsers folder")
			}
		})
	}
}

func TestManagedBrowserDownloadsOnlyFromItsHost(t *testing.T) {
	c := newCFTServer(t)
	m := managedManager(t, c)
	c.release(t, "200.0.0.1", goodRelease("200.0.0.1"))
	c.mu.Lock()
	c.host = "downloads.example.com"
	c.mu.Unlock()
	if err := m.InstallBrowser(); err != nil {
		t.Fatal(err)
	}
	if st := settle(t, m); st.Installed != "" || !strings.Contains(st.Error, "refusing") {
		t.Fatalf("%+v", st)
	}
	// and the real rule: https, Google's storage, no port
	real := New(Config{})
	for raw, ok := range map[string]bool{
		"https://storage.googleapis.com/chrome-for-testing-public/1.2.3.4/linux64/chrome-linux64.zip": true,
		"http://storage.googleapis.com/x.zip":               false,
		"https://storage.googleapis.com:8443/x.zip":         false,
		"https://evil.example.com/x.zip":                    false,
		"https://storage.googleapis.com.evil.example/x.zip": false,
	} {
		if err := real.allowedURL(raw); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestManagedBrowserSandbox(t *testing.T) {
	c := newCFTServer(t)
	m := managedManager(t, c)
	dir := t.TempDir()
	restrict := filepath.Join(dir, "restrict")
	m.mb.restrictFile = restrict
	notSetuid := filepath.Join(dir, "chrome-sandbox")
	if err := os.WriteFile(notSetuid, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.mb.helpers = []string{notSetuid}

	for _, v := range []string{"", "0\n"} {
		if v != "" {
			_ = os.WriteFile(restrict, []byte(v), 0o644)
		}
		if mode, _ := m.mb.sandbox(); mode != SandboxNamespaces {
			t.Fatalf("no restriction (%q): %s", v, mode)
		}
	}
	_ = os.WriteFile(restrict, []byte("1\n"), 0o644)
	if mode, _ := m.mb.sandbox(); mode != SandboxSetup {
		t.Fatalf("restricted, no helper: %s", mode)
	}
	if _, err := m.managedEnv(); err == nil || !strings.Contains(err.Error(), "browser-sandbox --data-dir") {
		t.Fatalf("no sandbox but no refusal: %v", err)
	}
	if st := m.BrowserStatus(); st.Sandbox != SandboxSetup || !strings.HasPrefix(st.SetupCommand, "sudo ") {
		t.Fatalf("status %+v", st)
	}
	// the profile, once installed
	m.mb.profileFile = filepath.Join(dir, "profile")
	_ = os.WriteFile(m.mb.profileFile, []byte("profile"), 0o644)
	if mode, _ := m.mb.sandbox(); mode != SandboxNamespaces {
		t.Fatalf("with the profile: %s", mode)
	}
	// a real helper on this machine, if there is one
	for _, h := range sandboxHelpers {
		if setuidRoot(h) {
			m.mb.profileFile = filepath.Join(dir, "none")
			m.mb.helpers = []string{notSetuid, h}
			mode, helper := m.mb.sandbox()
			env, err := m.managedEnv()
			if mode != SandboxHelper || helper != h || err != nil || len(env) != 1 || env[0] != "CHROME_DEVEL_SANDBOX="+h {
				t.Fatalf("helper %s: %s %s %v %v", h, mode, helper, env, err)
			}
			break
		}
	}
}

func TestSandboxProfile(t *testing.T) {
	p, err := SandboxProfile("/home/someone/.nextboard")
	if err != nil || !strings.Contains(p, `profile nextboard-browser "/home/someone/.nextboard/browsers/chrome-*/chrome-linux*/chrome" flags=(unconfined)`) || !strings.Contains(p, "userns,") {
		t.Fatalf("%v\n%s", err, p)
	}
	for _, bad := range []string{"relative/dir", "/a\"b", "/a\nb", "/a*b"} {
		if _, err := SandboxProfile(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

func TestManagedBrowserUnsupported(t *testing.T) {
	c := newCFTServer(t)
	m := managedManager(t, c)
	m.mb.platform = "darwin/arm64"
	if st := m.BrowserStatus(); st.Supported {
		t.Fatal("supported on macOS")
	}
	var ge *Error
	if err := m.InstallBrowser(); !errors.As(err, &ge) || ge.Code != "browser_unsupported" {
		t.Fatalf("%v", err)
	}
}
