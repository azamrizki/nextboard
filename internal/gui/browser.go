package gui

import (
	"cmp"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
)

// Web tiles show sites outside the project's machine in a real browser of This
// PC (docs/API-gui.md §8). Most sites refuse to be framed, and a proxy that
// strips their headers breaks logins and shares one origin between them. It is
// the browser's normal session: one profile, so logins carry over, and each
// page opened is a tab of it. A second tile of the same browser asks for a
// session of its own, with a profile of its own: a browser's profile has one
// running instance, which would put the window of the second on the display of
// the first.

// browserOrder ranks the supported browsers by program name, best first.
var browserOrder = []string{
	"google-chrome-stable", "google-chrome", "chromium", "chromium-browser",
	"brave-browser-stable", "brave-browser", "brave",
	"microsoft-edge-stable", "microsoft-edge", "vivaldi-stable", "vivaldi", "opera",
	"firefox", "firefox-esr",
}

// packagedRank puts a snap or a flatpak behind anything installed normally.
const packagedRank = 100

// maxBrowserWindows bounds the browsers of one kind open at once. Each holds hundreds of MB.
const maxBrowserWindows = 4

// OpenURLRequest asks the browser to show a page. The size applies when this
// starts the browser.
type OpenURLRequest struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// Here asks for a browser of this tile's own, instead of a tab of the one that is open.
	// Instance is the one the tile had before, whose logins it would like back.
	Here     bool `json:"here,omitempty"`
	Instance int  `json:"instance,omitempty"`
}

// OpenedURL says where a page went.
type OpenedURL struct {
	Session Session `json:"session"`
	URL     string  `json:"url"`    // as handed to the browser
	NewTab  bool    `json:"newTab"` // the browser was open already
}

// webURL returns the canonical form of an http(s) address. It lands on the
// browser's command line, so anything else stays out: flags, file:// and the
// browser's own schemes.
func webURL(raw string) (string, error) {
	bad := newErr(http.StatusBadRequest, "bad_url", "invalid web address (must be http or https)")
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 8<<10 {
		return "", bad
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", bad
	}
	return u.String(), nil
}

// browserRank orders a desktop entry among the browsers; ok is false for
// anything else. The category keeps out web apps installed from a browser,
// whose entries run the same program.
func (m *Manager) browserRank(e *entry) (rank int, ok bool) {
	if !slices.Contains(e.Categories, "WebBrowser") {
		return 0, false
	}
	argv, err := parseExec(e.Exec)
	if err != nil || len(argv) == 0 {
		return 0, false
	}
	bin, found := lookIn(m.pathEnv(), argv[binaryIndex(argv)])
	if !found {
		return 0, false
	}
	pk := packageOf(argv, bin)
	if pk.family != familyChromium && pk.family != familyFirefox {
		return 0, false
	}
	// A page goes to a running Firefox over a session bus, and native ones need their own.
	if pk.family == familyFirefox && pk.kind == pkgNative {
		if _, ok := lookIn(m.pathEnv(), "dbus-daemon"); !ok {
			return 0, false
		}
	}
	name := filepath.Base(bin)
	switch pk.kind {
	case pkgSnap:
		return packagedRank + slices.Index(browserOrder, pk.name), true
	case pkgFlatpak:
		return packagedRank + slices.Index(browserOrder, flatpakBrowsers[pk.name]), true
	}
	return slices.Index(browserOrder, name), true
}

// browser picks the installed browser to start, nil when there is none. The
// Chromium family first: a page goes to its running instance through the profile's
// lock, which needs no bus. Firefox after them, as it does need one.
func (m *Manager) browser() *entry {
	var best *entry
	bestRank := 0
	for _, e := range scanEntries(dataDirs()) {
		if !e.launchable(m.pathEnv()) {
			continue
		}
		rank, ok := m.browserRank(e)
		if !ok {
			continue
		}
		if best == nil || cmp.Or(cmp.Compare(rank, bestRank), cmp.Compare(e.ID, best.ID)) < 0 {
			best, bestRank = e, rank
		}
	}
	return best
}

// runningBrowser finds a browser session that can take a page, the first one opened.
func (m *Manager) runningBrowser() (Session, *entry, bool) {
	var found Session
	var fe *entry
	for _, pub := range m.List() {
		if pub.State == StateStopping {
			continue
		}
		if e, ok := m.lookup(pub.AppID); ok {
			if _, ok := m.browserRank(e); ok && (fe == nil || instanceOf(pub) < instanceOf(found)) {
				found, fe = pub, e
			}
		}
	}
	return found, fe, fe != nil
}

func instanceOf(pub Session) int { return max(pub.Instance, 1) }

// freeInstance picks the number of a new browser of this kind: the one asked for if nothing
// has it, else the lowest free.
func (m *Manager) freeInstance(appID string, want int) (int, error) {
	taken := map[int]bool{}
	for _, pub := range m.List() {
		if pub.AppID == appID {
			taken[instanceOf(pub)] = true
		}
	}
	if want >= 1 && want <= maxBrowserWindows && !taken[want] {
		return want, nil
	}
	for n := 1; n <= maxBrowserWindows; n++ {
		if !taken[n] {
			return n, nil
		}
	}
	return 0, newErr(http.StatusConflict, "too_many_sessions", "at most %d browsers of one kind open at once", maxBrowserWindows)
}

// openHere starts a browser for one tile, of the same kind as the one already open if any.
func (m *Manager) openHere(req OpenURLRequest, u string) (OpenedURL, error) {
	e := m.browser()
	if _, running, ok := m.runningBrowser(); ok {
		e = running
	}
	if e == nil {
		return OpenedURL{}, newErr(http.StatusNotFound, "browser_not_found", noBrowserMsg)
	}
	n, err := m.freeInstance(e.ID, req.Instance)
	if err != nil {
		return OpenedURL{}, err
	}
	pub, err := m.launch(LaunchRequest{AppID: e.ID, Width: req.Width, Height: req.Height, Instance: n}, u)
	if err != nil {
		return OpenedURL{}, err
	}
	return OpenedURL{Session: pub, URL: u}, nil
}

const noBrowserMsg = "no supported browser on This PC. Install Google Chrome, Chromium or Firefox"

// OpenURL shows a page in the browser: as a new tab when it is open, else by
// starting it. The window of a started browser arrives later, announced by a
// guiSession event.
func (m *Manager) OpenURL(req OpenURLRequest) (OpenedURL, error) {
	u, err := webURL(req.URL)
	if err != nil {
		return OpenedURL{}, err
	}
	if err := m.checkAvailable(); err != nil {
		return OpenedURL{}, err
	}
	if req.Here {
		return m.openHere(req, u)
	}
	if pub, e, ok := m.runningBrowser(); ok {
		if err := m.openTab(pub.ID, e, u); err != nil {
			return OpenedURL{}, err
		}
		return OpenedURL{Session: pub, URL: u, NewTab: true}, nil
	}
	e := m.browser()
	if e == nil {
		return OpenedURL{}, newErr(http.StatusNotFound, "browser_not_found", noBrowserMsg)
	}
	pub, err := m.launch(LaunchRequest{AppID: e.ID, Width: req.Width, Height: req.Height}, u)
	var ge *Error
	if errors.As(err, &ge) && ge.Code == "already_running" && ge.Session != nil {
		// Someone started it in between.
		if err := m.openTab(ge.Session.ID, e, u); err != nil {
			return OpenedURL{}, err
		}
		return OpenedURL{Session: *ge.Session, URL: u, NewTab: true}, nil
	}
	if err != nil {
		return OpenedURL{}, err
	}
	return OpenedURL{Session: pub, URL: u}, nil
}

// openTab runs the browser once more on the session's profile: it hands the
// page to the running instance over the profile's lock and exits.
func (m *Manager) openTab(id string, e *entry, pageURL string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	alive := ok && !s.stopping
	m.mu.Unlock()
	if !alive {
		return notFound()
	}
	xauth := ""
	if s.pub.Display != DisplayWayland {
		xauth = filepath.Join(s.dir, "xauth")
	}
	argv, env, cwd, err := m.appCommandFor(s, e, "", xauth, pageURL, true)
	if err == nil {
		_, err = m.spawn(s, argv, cwd, env, "app.log", nil)
	}
	if err != nil {
		return newErr(http.StatusInternalServerError, "gui_start_failed", "failed to open a new tab: %v", err)
	}
	return nil
}
