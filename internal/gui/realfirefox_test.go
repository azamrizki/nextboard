package gui

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// A real Firefox through the hub's whole path, on the displays this machine has. Only by hand, as
// it needs Firefox and spends memory on a machine other things run on:
//
//	NEXTBOARD_REAL_FIREFOX=/path/to/firefox go test ./internal/gui -run TestRealFirefox -v
//
// Every step shows in a request the browser really made to a server here. The step that matters
// most is the second page: Firefox takes it over a session bus, unlike the Chromium family.
func TestRealFirefox(t *testing.T) {
	bin := os.Getenv("NEXTBOARD_REAL_FIREFOX")
	if bin == "" {
		t.Skip("set NEXTBOARD_REAL_FIREFOX to the firefox program")
	}
	for _, display := range []string{DisplayWayland, DisplayX11} {
		t.Run(display, func(t *testing.T) { realFirefox(t, bin, display) })
	}
}

func realFirefox(t *testing.T, bin, display string) {
	m, ev := newTestManager(t, map[string]string{"notes": ""})
	m.forceX11 = display == DisplayX11
	if display == DisplayWayland && !m.waylandAvailable() {
		t.Skip("no Wayland display on this machine")
	}
	m.windowTimeout = 90 * time.Second
	writeDesktop(t, os.Getenv("XDG_DATA_HOME"), "real-firefox.desktop",
		"[Desktop Entry]\nType=Application\nName=Real Firefox\nExec="+bin+" %u\nCategories=Network;WebBrowser;\n")

	var mu sync.Mutex
	hits := map[string]string{}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.UserAgent(), "Firefox") {
			mu.Lock()
			hits[r.URL.Path] = r.UserAgent()
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<!doctype html><title>page %s</title><p>%s</p>", r.URL.Path, r.URL.Path)
	})}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()
	site := "http://" + l.Addr().String()
	sawPage := func(path string, within time.Duration) {
		t.Helper()
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			mu.Lock()
			_, ok := hits[path]
			mu.Unlock()
			if ok {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("Firefox never asked for %s; seen %v", path, hits)
	}

	first, err := m.OpenURL(OpenURLRequest{URL: site + "/first", Width: 900, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if first.Session.Display != display {
		t.Fatalf("display %q", first.Session.Display)
	}
	stop := first.Session.ID
	defer func() { _ = m.Stop(stop) }()
	waitSession(t, m, ev, "a window", func(s Session) bool { return s.ID == first.Session.ID && s.State == StateRunning })
	sawPage("/first", 45*time.Second)

	// handed to the running instance, in the same session
	tab, err := m.OpenURL(OpenURLRequest{URL: site + "/second"})
	if err != nil {
		t.Fatal(err)
	}
	if !tab.NewTab || tab.Session.ID != first.Session.ID {
		t.Fatalf("second page: %+v", tab)
	}
	sawPage("/second", 30*time.Second)
	if n := len(m.List()); n != 1 {
		t.Fatalf("%d sessions after a tab", n)
	}
	// its window says so: the tab is on top
	waitSession(t, m, ev, "the title of the tab", func(s Session) bool { return s.ID == first.Session.ID && strings.Contains(s.Title, "second") })

	// The hub restarts and the browser does not. The next page still finds it, on the same bus.
	m.Close()
	m2 := New(Config{Dir: m.cfg.Dir, Publish: ev.add})
	m2.forceX11 = m.forceX11
	m2.pollEvery, m2.closeGrace, m2.windowTimeout, m2.exitedWait = m.pollEvery, m.closeGrace, m.windowTimeout, m.exitedWait
	m2.Start()
	t.Cleanup(m2.Close)
	m = m2
	if n := len(m.List()); n != 1 {
		t.Fatalf("%d sessions after the restart", n)
	}
	after, err := m.OpenURL(OpenURLRequest{URL: site + "/after-restart"})
	if err != nil {
		t.Fatal(err)
	}
	if !after.NewTab || after.Session.ID != first.Session.ID {
		t.Fatalf("after the restart: %+v", after)
	}
	sawPage("/after-restart", 30*time.Second)

	// a tile of its own: another profile, another instance
	own, err := m.OpenURL(OpenURLRequest{URL: site + "/third", Here: true, Width: 800, Height: 500})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(own.Session.ID) }()
	if own.Session.Instance != 2 || own.Session.ID == first.Session.ID {
		t.Fatalf("own tile: %+v", own.Session)
	}
	waitSession(t, m, ev, "a second window", func(s Session) bool { return s.ID == own.Session.ID && s.State == StateRunning })
	sawPage("/third", 45*time.Second)

	// and both end, with nothing left
	for _, id := range []string{own.Session.ID, first.Session.ID} {
		if err := m.Stop(id); err != nil {
			t.Fatal(err)
		}
		waitGone(t, m, ev, id)
		if left := sessionProcs(id); len(left) != 0 {
			t.Fatalf("session %s left %d processes", id, len(left))
		}
	}
}
