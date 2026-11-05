package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// guiApp installs a desktop entry for this test only. No window ever opens:
// the session stays "starting", which is all the HTTP layer cares about.
func guiApp(t *testing.T, id string) {
	t.Helper()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	p := filepath.Join(data, "applications", id+".desktop")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[Desktop Entry]\nType=Application\nName=Test App\nComment=For tests\nExec=/bin/sleep 60\nIcon=" + filepath.Join(data, "icon.png") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "icon.png"), []byte("\x89PNG\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGUIAppsAndIcon(t *testing.T) {
	guiApp(t, "testapp")
	h := newTestHub(t)
	h.setup()

	code, body := h.do("GET", "/api/gui/apps", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("apps: %d %v", code, body)
	}
	apps, _ := body["apps"].([]any)
	found := false
	for _, a := range apps {
		if m, _ := a.(map[string]any); m["id"] == "testapp" && m["name"] == "Test App" {
			found = true
		}
	}
	if !found {
		t.Errorf("testapp missing from %v", apps)
	}

	res, err := h.c.Get(h.ts.URL + "/api/gui/apps/testapp/icon")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || res.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("icon: %d %v", res.StatusCode, res.Header)
	}
	if code, _ := h.do("GET", "/api/gui/apps/nope/icon", nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown icon: %d", code)
	}
	if code, body := h.do("POST", "/api/gui/sessions", map[string]any{"appId": "nope"}, nil); code == http.StatusCreated {
		t.Errorf("unknown app launched: %v", body)
	}
}

// TestGUIBrowserOpen: the HTTP layer of opening a web page. No window ever
// opens: the program under the browser's name only sleeps.
func TestGUIBrowserOpen(t *testing.T) {
	guiApp(t, "testapp")
	h := newTestHub(t)
	h.setup()
	body := map[string]any{"url": "https://github.com/", "width": 800, "height": 600}

	if code, b := h.do("POST", "/api/gui/browser/open", map[string]any{"url": "file:///etc/passwd"}, nil); code != http.StatusBadRequest || b["code"] != "bad_url" {
		t.Fatalf("bad url: %d %v", code, b)
	}
	if st := h.srv.gui.Status(); !st.Available {
		t.Skip("tigervnc-standalone-server / openbox belum terpasang")
	}
	if code, b := h.do("POST", "/api/gui/browser/open", body, nil); code != http.StatusNotFound || b["code"] != "browser_not_found" {
		t.Fatalf("no browser: %d %v", code, b)
	}

	bin := filepath.Join(t.TempDir(), "google-chrome")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := "[Desktop Entry]\nType=Application\nName=Fake Chrome\nExec=" + bin + " %U\nCategories=Network;WebBrowser;\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_DATA_HOME"), "applications", "fake-chrome.desktop"), []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
	code, b := h.do("POST", "/api/gui/browser/open", body, nil)
	if code != http.StatusCreated || b["newTab"] != false || b["url"] != "https://github.com/" {
		t.Fatalf("first: %d %v", code, b)
	}
	id := b["session"].(map[string]any)["id"].(string)
	defer func() {
		_ = h.srv.gui.Stop(id)
		// Teardown runs in the background: leaving now would leave the display behind.
		deadline := time.Now().Add(15 * time.Second)
		for len(h.srv.gui.List()) > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}()
	code, b = h.do("POST", "/api/gui/browser/open", map[string]any{"url": "https://example.com/"}, nil)
	if code != http.StatusOK || b["newTab"] != true || b["session"].(map[string]any)["id"] != id {
		t.Fatalf("second: %d %v", code, b)
	}
}

func TestGUIRequiresLogin(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	res, err := h.ts.Client().Get(h.ts.URL + "/api/gui/apps")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a session: %d", res.StatusCode)
	}
}

func TestGUIViewerMayBeFramedBySameOrigin(t *testing.T) {
	h := newTestHub(t)
	res, err := h.c.Get(h.ts.URL + guiViewerPath + "?session=g_x")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("viewer X-Frame-Options = %q", got)
	}
	if got := res.Header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'self'") {
		t.Errorf("viewer CSP = %q", got)
	}
	// Every other page keeps refusing to be framed.
	res, err = h.c.Get(h.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("index X-Frame-Options = %q", got)
	}
	if got := res.Header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("index CSP = %q", got)
	}
}

func TestGUISessionOverWS(t *testing.T) {
	guiApp(t, "testapp")
	h := newTestHub(t)
	if !h.srv.gui.Status().Available {
		t.Skip("tigervnc-standalone-server / openbox belum terpasang")
	}
	h.setup()

	code, body := h.do("POST", "/api/gui/sessions", map[string]any{"appId": "testapp", "width": 640, "height": 480}, nil)
	if code != http.StatusCreated {
		t.Fatalf("launch: %d %v", code, body)
	}
	id, _ := body["id"].(string)
	stopped := false
	defer func() {
		if !stopped {
			h.do("DELETE", "/api/gui/sessions/"+id, nil, nil)
		}
		deadline := time.Now().Add(15 * time.Second)
		for len(h.srv.gui.List()) > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// One session per app, and the conflict names the running one.
	code, body = h.do("POST", "/api/gui/sessions", map[string]any{"appId": "testapp"}, nil)
	sess, _ := body["session"].(map[string]any)
	if code != http.StatusConflict || body["code"] != "already_running" || sess["id"] != id {
		t.Errorf("second launch: %d %v", code, body)
	}

	// A foreign page may not open the socket with the viewer's cookie.
	hdr := http.Header{"Origin": {"https://evil.example"}}
	if c, res, err := h.dialWS("/api/gui/sessions/"+id+"/ws", withCookies(h, hdr)); err == nil {
		c.Close()
		t.Error("foreign origin connected")
	} else if res == nil || res.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: %v %v", res, err)
	}

	// The bridge carries RFB both ways: banner down, version up, security list down.
	c, _, err := h.dialWS("/api/gui/sessions/"+id+"/ws", withCookies(h, http.Header{}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	typ, banner, err := c.ReadMessage()
	if err != nil || typ != websocket.BinaryMessage || !strings.HasPrefix(string(banner), "RFB ") {
		t.Fatalf("banner: %d %q %v", typ, banner, err)
	}
	if err := c.WriteMessage(websocket.BinaryMessage, banner); err != nil {
		t.Fatal(err)
	}
	if _, sec, err := c.ReadMessage(); err != nil || len(sec) < 2 || sec[0] == 0 {
		t.Fatalf("security types: %q %v", sec, err)
	}

	if code, body := h.do("DELETE", "/api/gui/sessions/"+id, nil, nil); code != http.StatusOK {
		t.Fatalf("stop: %d %v", code, body)
	}
	stopped = true
	// The stream ends with the session.
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			break
		}
	}
	if code, _ := h.do("DELETE", "/api/gui/sessions/"+id, nil, nil); code != http.StatusNotFound && code != http.StatusOK {
		t.Errorf("second stop: %d", code)
	}
}

func withCookies(h *testHub, hdr http.Header) http.Header {
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.String())
	}
	return hdr
}

func TestGUIVideoAndAudioSockets(t *testing.T) {
	// This app has no window. Only the X11 display sends pictures of nothing
	// changing, and the socket's protocol is the same on both.
	t.Setenv("NEXTBOARD_GUI_BACKEND", "x11")
	guiApp(t, "testapp")
	h := newTestHub(t)
	st := h.srv.gui.Status()
	if !st.Available {
		t.Skip("tigervnc-standalone-server / openbox belum terpasang")
	}
	h.setup()

	if c, res, err := h.dialWS("/api/gui/sessions/g_none/video", withCookies(h, http.Header{})); err == nil {
		c.Close()
		t.Error("video of an unknown session connected")
	} else if res == nil || res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown session: %v %v", res, err)
	}

	code, body := h.do("GET", "/api/gui/apps", nil, nil)
	if code != http.StatusOK || body["video"] != st.Video || body["audio"] != st.Audio {
		t.Errorf("status fields: %d %v", code, body)
	}

	code, body = h.do("POST", "/api/gui/sessions", map[string]any{"appId": "testapp", "width": 640, "height": 480}, nil)
	if code != http.StatusCreated {
		t.Fatalf("launch: %d %v", code, body)
	}
	id, _ := body["id"].(string)
	if body["audio"] != st.Audio {
		t.Errorf("session audio %v, machine %v", body["audio"], st.Audio)
	}
	defer func() {
		h.do("DELETE", "/api/gui/sessions/"+id, nil, nil)
		deadline := time.Now().Add(15 * time.Second)
		for len(h.srv.gui.List()) > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}()

	hdr := http.Header{"Origin": {"https://evil.example"}}
	for _, kind := range []string{"video", "audio"} {
		if c, res, err := h.dialWS("/api/gui/sessions/"+id+"/"+kind, withCookies(h, hdr.Clone())); err == nil {
			c.Close()
			t.Errorf("%s: foreign origin connected", kind)
		} else if res == nil || res.StatusCode != http.StatusForbidden {
			t.Errorf("%s: foreign origin: %v %v", kind, res, err)
		}
	}

	if !st.Video {
		t.Log("no hardware encoder here: video not tested")
	} else {
		c, _, err := h.dialWS("/api/gui/sessions/"+id+"/video?w=640&h=480&codec="+st.Codecs[0], withCookies(h, http.Header{}))
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		typ, hello, err := c.ReadMessage()
		if err != nil || typ != websocket.TextMessage || !strings.Contains(string(hello), `"hello"`) {
			t.Fatalf("hello: %d %q %v", typ, hello, err)
		}
		typ, pic, err := c.ReadMessage()
		if err != nil || typ != websocket.BinaryMessage || len(pic) < 10 || pic[0]&1 != 1 {
			t.Fatalf("first picture must be a key frame: %d %d %v", typ, len(pic), err)
		}
		// Unanswered pictures run out soon; answers keep them coming.
		for range 5 {
			if err := c.WriteMessage(websocket.BinaryMessage, []byte{0}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := c.ReadMessage(); err != nil {
				t.Fatal(err)
			}
		}
		c.Close()
	}

	if !st.Audio {
		t.Log("no PipeWire here: audio not tested")
		return
	}
	c, _, err := h.dialWS("/api/gui/sessions/"+id+"/audio", withCookies(h, http.Header{}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Silence stays off the wire.
	_ = c.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, b, err := c.ReadMessage(); err == nil {
		t.Errorf("a silent app sent %d bytes of audio", len(b))
	}
}

func TestIsSilence(t *testing.T) {
	if !isSilence(make([]byte, 16)) {
		t.Error("zeros are silence")
	}
	if isSilence([]byte{0, 0, 1, 0}) {
		t.Error("a sample is not silence")
	}
}

func TestVideoPacerLimitsByTime(t *testing.T) {
	p := newVideoPacer()
	if !p.ready() {
		t.Fatal("nothing pending: ready")
	}
	p.sent()
	time.Sleep(5 * time.Millisecond)
	p.acked()
	if p.minAge <= 0 {
		t.Fatal("an answer sets the link's delay")
	}
	p.minAge = 10 * time.Millisecond

	p.sent()
	if !p.ready() {
		t.Error("a young picture is no reason to wait")
	}
	p.pending[0] = time.Now().Add(-p.minAge - guiVideoQueue - time.Millisecond)
	if p.ready() {
		t.Error("the oldest picture is too old: wait")
	}
	p.acked()
	if !p.ready() {
		t.Error("answered: ready again")
	}

	// Many young pictures have a limit of their own.
	for range guiVideoMaxPending {
		p.sent()
	}
	if p.ready() {
		t.Error("the count limit must hold")
	}

	done := make(chan struct{})
	close(done)
	if p.wait(done) {
		t.Error("a closed viewer asks for nothing")
	}
}
