package server

import (
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/session"
)

// namedHub is a test hub of instance name ("" = the default instance), not set up yet.
func namedHub(t *testing.T, name string) *testHub {
	t.Helper()
	dir := hubDataDir(t)
	if name != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"instance":"`+name+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return newTestHubAt(t, dir)
}

// Two hubs on one host share their cookies: a cookie ignores the port, as Go's cookiejar does
// too (RFC 6265 §8.5). With an instance name in the cookie, logging into one never logs the
// other out, and logging out of one leaves the other signed in. Two default instances still
// share one name: the known limit of two OS users' default hubs on one host, and the guard that
// the test really shares one jar and that the default kept today's names.
func TestInstanceCookiesOnOneHost(t *testing.T) {
	for _, tc := range []struct {
		a, b     string
		separate bool
	}{
		{"", "personal", true},
		{"work", "personal", true},
		{"", "", false},
	} {
		t.Run(tc.a+"+"+tc.b, func(t *testing.T) {
			a, b := namedHub(t, tc.a), namedHub(t, tc.b)
			if !strings.HasPrefix(a.ts.URL, "http://127.0.0.1:") || !strings.HasPrefix(b.ts.URL, "http://127.0.0.1:") || a.ts.URL == b.ts.URL {
				t.Fatalf("hubs %s %s", a.ts.URL, b.ts.URL)
			}
			jar, _ := cookiejar.New(nil)
			a.c.Jar, b.c.Jar = jar, jar
			a.setup()
			b.setup()
			login := func(h *testHub) {
				t.Helper()
				if st, out := h.do("POST", "/api/auth/login", map[string]any{"password": "rahasia123"}, nil); st != 200 {
					t.Fatalf("login %s: %d %v", h.ts.URL, st, out)
				}
			}
			me := func(h *testHub) int {
				t.Helper()
				st, _ := h.do("GET", "/api/auth/me", nil, nil)
				return st
			}
			names := func() []string {
				var out []string
				for _, c := range jar.Cookies(mustURL(a.ts.URL)) {
					out = append(out, c.Name)
				}
				slices.Sort(out)
				return out
			}
			login(a)
			login(b)
			if !tc.separate {
				if st := me(a); st != 401 {
					t.Fatalf("two default instances: A answers %d after logging into B, want 401 (is the jar shared?)", st)
				}
				if got := names(); !slices.Equal(got, []string{"nb_session_http"}) {
					t.Fatalf("cookies %v", got)
				}
				return
			}
			if st := me(a); st != 200 {
				t.Fatalf("A logged out by logging into B: %d", st)
			}
			if st := me(b); st != 200 {
				t.Fatalf("B: %d", st)
			}
			want := []string{sessionCookieFor("http", tc.a), sessionCookieFor("http", tc.b)}
			slices.Sort(want)
			if got := names(); !slices.Equal(got, want) {
				t.Fatalf("cookies on the host %v, want %v", got, want)
			}
			if tc.a == "" && !slices.Equal(want, []string{"nb_session_http", "nb_session_http_personal"}) {
				t.Fatalf("names %v", want)
			}
			// logging out of one leaves the other signed in, either way round
			if st, _ := b.do("POST", "/api/auth/logout", nil, nil); st != 200 {
				t.Fatalf("logout B: %d", st)
			}
			if st := me(b); st != 401 {
				t.Fatalf("B after its logout: %d", st)
			}
			if st := me(a); st != 200 {
				t.Fatalf("A logged out by logging out of B: %d", st)
			}
			login(b)
			if st, _ := a.do("POST", "/api/auth/logout", nil, nil); st != 200 {
				t.Fatalf("logout A: %d", st)
			}
			if st := me(a); st != 401 {
				t.Fatalf("A after its logout: %d", st)
			}
			if st := me(b); st != 200 {
				t.Fatalf("B logged out by logging out of A: %d", st)
			}
		})
	}
}

// The default instance keeps today's cookie names (TestSessionCookiePerScheme), so nobody logs
// in again after the upgrade; a named one adds its name for both schemes, and only that name
// logs in there.
func TestInstanceCookieNames(t *testing.T) {
	h := namedHub(t, "work")
	h.setup()
	send := func(method, path string, https bool, ck *http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, h.ts.URL+path, strings.NewReader(`{"password":"rahasia123"}`))
		req.Header.Set("X-NextBoard", "1")
		if https {
			req.Header.Set("X-Forwarded-Proto", "https")
		}
		if ck != nil {
			req.AddCookie(ck)
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	for _, https := range []bool{false, true} {
		cs := send("POST", "/api/auth/login", https, nil).Cookies()
		want, legacy := "nb_session_http_work", "nb_session_http"
		if https {
			want, legacy = "nb_session_work", "nb_session"
		}
		if len(cs) != 1 || cs[0].Name != want || cs[0].Secure != https || !cs[0].HttpOnly || cs[0].SameSite != http.SameSiteStrictMode {
			t.Fatalf("https=%v: cookies %+v, want %s", https, cs, want)
		}
		if r := send("GET", "/api/auth/me", https, cs[0]); r.StatusCode != 200 {
			t.Fatalf("%s: %d", want, r.StatusCode)
		}
		if r := send("GET", "/api/auth/me", https, &http.Cookie{Name: legacy, Value: cs[0].Value}); r.StatusCode != 401 {
			t.Fatalf("the default instance's name %s logged into instance work: %d", legacy, r.StatusCode)
		}
		out := send("POST", "/api/auth/logout", https, cs[0]).Cookies()
		if len(out) != 1 || out[0].Name != want || out[0].MaxAge >= 0 {
			t.Fatalf("logout https=%v: %+v", https, out)
		}
	}
}

// The instance and hubId the hub reads from config.json.
func TestInstanceNameFromConfig(t *testing.T) {
	if n := sessionCookieFor("http", ""); n != "nb_session_http" {
		t.Fatal(n)
	}
	if n := sessionCookieFor("https", ""); n != "nb_session" {
		t.Fatal(n)
	}
	for name, want := range map[string]string{"": "", "default": "", "work": "work"} {
		h := namedHub(t, name)
		if got := h.srv.instanceName(); got != want {
			t.Fatalf("instance %q: instanceName %q, want %q", name, got, want)
		}
		if h.srv.hubID() == "" {
			t.Fatalf("instance %q has no hubId", name)
		}
	}
}

// A console of This PC knows its hub's instance (NEXTBOARD_INSTANCE), so that `nextboard …`
// typed there acts on that instance; what the hub itself inherited never reaches it.
func TestConsoleKnowsItsInstance(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("NEXTBOARD_INSTANCE", "inherited")
	if d := namedHub(t, ""); !slices.Equal(d.srv.sessions.LocalEnv, []string{"NEXTBOARD_INSTANCE=default"}) {
		t.Fatalf("default: %v", d.srv.sessions.LocalEnv)
	}
	h := namedHub(t, "personal")
	h.setup()
	st, in := h.do("POST", "/api/sessions", map[string]any{"machineId": "local", "preset": "shell", "cols": 80, "rows": 24, "cwd": t.TempDir()}, nil)
	if st != 201 {
		t.Fatalf("session %d %v", st, in)
	}
	sid := in["id"].(string)
	time.Sleep(300 * time.Millisecond)
	h.do("POST", "/api/sessions/"+sid+"/input", map[string]any{"data": "echo NBI=${NEXTBOARD_INSTANCE}.\r"}, nil)
	waitScrollback(t, h.srv, sid, "NBI=personal.")
}

// Orphans on a shared server: this hub's own names are listed, a sibling hub's are not (nor one
// whose hubId merely starts like this one's), and names without a hub mark come in their own
// group, which the dialog leaves unselected.
func TestClassifyOrphans(t *testing.T) {
	if n := session.TmuxName("hubaaaa111", "s_abc"); n != "nb-hubaaaa111-s_abc" {
		t.Fatal(n)
	}
	if n := session.TmuxName("", "s_abc"); n != "nb-s_abc" {
		t.Fatal(n)
	}
	names := []string{"nb-hubaaaa111-s_gone", "nb-hubaaaa111-s_live", "nb-hubbbbb222-s_other", "nb-hubaaaa1112-s_x",
		"nb-s_legacy1", "nb-s_ownedlegacy", "nb-manual"}
	owned := map[string]bool{"nb-hubaaaa111-s_live": true, "nb-s_ownedlegacy": true}
	own, legacy := classifyOrphans(names, owned, "hubaaaa111")
	if !slices.Equal(own, []string{"nb-hubaaaa111-s_gone"}) || !slices.Equal(legacy, []string{"nb-s_legacy1"}) {
		t.Fatalf("own %v legacy %v", own, legacy)
	}
	// without a hubId nothing is surely its own
	own, legacy = classifyOrphans(names, owned, "")
	if len(own) != 0 || !slices.Equal(legacy, []string{"nb-s_legacy1"}) {
		t.Fatalf("no hubId: own %v legacy %v", own, legacy)
	}
	h := newTestHub(t)
	h.setup()
	st, out := h.do("GET", "/api/machines/local/orphans", nil, nil)
	if st != 200 || out["names"] == nil || out["legacy"] == nil {
		t.Fatalf("orphans of This PC: %d %v", st, out)
	}
}

// A preview of This PC never opens another instance's hub (or its status page) in a tile: their
// ports are refused like this hub's own. This hub's own folder is not a sibling.
func TestPreviewRefusesSiblingHubPorts(t *testing.T) {
	home := hubDataDir(t)
	t.Setenv("HOME", home)
	for name, cfg := range map[string]string{
		".nextboard-sib": `{"instance":"sib","hubId":"sibhub1234","listen":"127.0.0.1:12412","statusListen":"127.0.0.1:12498"}`,
		".nextboard-me":  `{"instance":"me","listen":"127.0.0.1:12612"}`,
	} {
		if err := os.MkdirAll(filepath.Join(home, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, name, "config.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := newTestHubAt(t, filepath.Join(home, ".nextboard-me"))
	pv := h.srv.gitPrev.previews
	for port, refused := range map[int]bool{12412: true, 12498: true, 12413: false, 12612: false} {
		if err := pv.CheckPort(machine.LocalID, port); (err != nil) != refused {
			t.Errorf("port %d: %v, want refused=%v", port, err, refused)
		}
	}
	// a server's own ports are its own business
	if err := pv.CheckPort("m_other", 12412); err != nil {
		t.Errorf("a server's port 12412: %v", err)
	}
}

// A named instance on block k whose settings still name the default instance's preview ports
// gets its block's; one with a range of its own, or not on a block, keeps it.
func TestPreviewRangePerBlock(t *testing.T) {
	cases := []struct {
		name, listen, settings string
		want                   [2]int
	}{
		{"work", "127.0.0.1:12412", "", [2]int{12420, 12439}},
		{"work", "127.0.0.1:12512", `{"previews":{"auto":false,"portRange":[12320,12339]}}`, [2]int{12520, 12539}},
		{"work", "127.0.0.1:12412", `{"previews":{"auto":true,"portRange":[13000,13019]}}`, [2]int{13000, 13019}},
		{"work", "127.0.0.1:12999", "", [2]int{12320, 12339}},
		{"", "127.0.0.1:12412", "", [2]int{12320, 12339}},
	}
	for _, tc := range cases {
		dir := hubDataDir(t)
		cfg := `{"listen":"` + tc.listen + `"`
		if tc.name != "" {
			cfg += `,"instance":"` + tc.name + `"`
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg+"}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if tc.settings != "" {
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(tc.settings), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		srv, err := New(Options{DataDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		got := srv.settings.View().Previews
		lo, hi := srv.gitPrev.previews.PortRange()
		srv.shutdown()
		if got.PortRange != tc.want || [2]int{lo, hi} != tc.want {
			t.Errorf("%+v: settings %v, proxy %d–%d", tc, got.PortRange, lo, hi)
		}
		if strings.Contains(tc.settings, `"auto":false`) && got.Auto {
			t.Errorf("%+v: auto turned back on", tc)
		}
	}
}
