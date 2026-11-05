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
// page opened is a tab of it.

// browserOrder ranks the supported browsers by program name, best first.
var browserOrder = []string{
	"google-chrome-stable", "google-chrome", "chromium", "chromium-browser",
	"brave-browser-stable", "brave-browser", "brave",
	"microsoft-edge-stable", "microsoft-edge", "vivaldi-stable", "vivaldi", "opera",
}

// OpenURLRequest asks the browser to show a page. The size applies when this
// starts the browser.
type OpenURLRequest struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
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
	bad := newErr(http.StatusBadRequest, "bad_url", "alamat web tidak valid (harus http atau https)")
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
	if !found || familyOf(bin) != familyChromium {
		return 0, false
	}
	return slices.Index(browserOrder, filepath.Base(bin)), true
}

// browser picks the installed browser to start, nil when there is none. Only
// the Chromium family: a page goes to its running instance through the profile's
// lock, which needs no bus and works for sessions adopted after a hub restart.
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

// runningBrowser finds a browser session that can take a page.
func (m *Manager) runningBrowser() (Session, *entry, bool) {
	for _, pub := range m.List() {
		if pub.State == StateStopping {
			continue
		}
		if e, ok := m.lookup(pub.AppID); ok {
			if _, ok := m.browserRank(e); ok {
				return pub, e, true
			}
		}
	}
	return Session{}, nil, false
}

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
	if pub, e, ok := m.runningBrowser(); ok {
		if err := m.openTab(pub.ID, e, u); err != nil {
			return OpenedURL{}, err
		}
		return OpenedURL{Session: pub, URL: u, NewTab: true}, nil
	}
	e := m.browser()
	if e == nil {
		return OpenedURL{}, newErr(http.StatusNotFound, "browser_not_found", "tidak ada browser yang didukung di This PC. Pasang Google Chrome atau Chromium (bukan versi snap atau flatpak).")
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
	argv, env, cwd, err := m.appCommand(s, e, "", xauth, pageURL)
	if err == nil {
		_, err = m.spawn(s, argv, cwd, env, "app.log", nil)
	}
	if err != nil {
		return newErr(http.StatusInternalServerError, "gui_start_failed", "gagal membuka tab baru: %v", err)
	}
	return nil
}
