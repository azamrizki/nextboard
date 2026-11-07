package uptime

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestJSONPath(t *testing.T) {
	for _, ok := range []string{"status", "$.status", "data.items[0].ok", "[2]", `a["b.c"].d`, "a[1][2]"} {
		if _, err := parseJSONPath(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "$", "a..b", "a.", "a[x]", "a[-1]", "a[1", `a["b`, "a b", "a.b c"} {
		if _, err := parseJSONPath(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestJSONQuery(t *testing.T) {
	body := []byte(`{"status":"ok","data":{"items":[{"n":3,"up":true},{"n":10}],"a.b":"dotted"},"none":null}`)
	cases := []struct {
		path, op, want string
		ok             bool
	}{
		{"status", "==", "ok", true},
		{"status", "!=", "ok", false},
		{"status", "contains", "o", true},
		{"data.items[0].n", "==", "3", true},
		{"data.items[0].up", "==", "true", true},
		{"data.items[1].n", ">", "9", true},
		{"data.items[1].n", "<=", "9", false},
		{"data.items[0].n", "<", "x", false},
		{`data["a.b"]`, "==", "dotted", true},
		{"none", "==", "null", true},
		{"data.items[5].n", "==", "3", false},
		{"missing", "!=", "x", false}, // no value is never a pass
		{"data.items", "contains", `"n":10`, true},
	}
	for _, c := range cases {
		ok, msg := jsonQuery(body, c.path, c.op, c.want)
		if ok != c.ok {
			t.Errorf("%s %s %q: %v (%s)", c.path, c.op, c.want, ok, msg)
		}
	}
	if ok, msg := jsonQuery([]byte("<html>"), "status", "==", "ok"); ok || msg != "The answer is no JSON" {
		t.Errorf("not JSON: %v %q", ok, msg)
	}
}

func TestNewTypesNormalize(t *testing.T) {
	good := []Monitor{
		{Name: "ws", Type: TypeWebSocket, URL: "wss://example.com/socket"},
		{Name: "docker", Type: TypeDocker, Container: "web_1"},
		{Name: "steam", Type: TypeSteam, Host: "game.example.com", MachineID: "m_srv"},
		{Name: "json", Type: TypeHTTP, URL: "https://example.com/health", JSONPath: "status", JSONExpect: "ok"},
	}
	for _, m := range good {
		if err := m.normalize(); err != nil {
			t.Errorf("%s: %v", m.Name, err)
		}
		switch m.Type {
		case TypeSteam:
			if m.Port != 27015 || m.MachineID != "" {
				t.Errorf("steam: %+v", m)
			}
		case TypeHTTP:
			if m.JSONOp != "==" {
				t.Errorf("json op: %q", m.JSONOp)
			}
		}
	}
	bad := []Monitor{
		{Name: "ws", Type: TypeWebSocket, URL: "https://example.com/"},
		{Name: "docker", Type: TypeDocker, Container: "-rm"},
		{Name: "docker", Type: TypeDocker, Container: "a b"},
		{Name: "json", Type: TypeHTTP, URL: "https://example.com/", JSONPath: "a..b", JSONExpect: "x"},
		{Name: "json", Type: TypeHTTP, URL: "https://example.com/", JSONPath: "a", JSONOp: "~", JSONExpect: "x"},
		{Name: "json", Type: TypeHTTP, URL: "https://example.com/", Method: "HEAD", JSONPath: "a", JSONExpect: "x"},
		{Name: "tags", Type: TypePush, Tags: []string{"a,b"}},
		{Name: "group", Type: TypePush, Group: strings.Repeat("g", maxGroupLen+1)},
	}
	for _, m := range bad {
		if err := m.normalize(); err == nil {
			t.Errorf("%s %+v: no error", m.Name, m)
		}
	}
	m := Monitor{Name: "labels", Type: TypePush, Group: "  Shop  ", Tags: []string{" prod ", "Prod", "", "api"}}
	if err := m.normalize(); err != nil || m.Group != "Shop" || strings.Join(m.Tags, ",") != "prod,api" {
		t.Fatalf("%v %+v", err, m)
	}
	// a type change leaves nothing of the old one behind
	m = Monitor{Name: "was json", Type: TypeTCP, Host: "db", Port: 5432, JSONPath: "a", JSONExpect: "b", Container: "c", URL: "https://x/"}
	if err := m.normalize(); err != nil || m.JSONPath != "" || m.Container != "" || m.URL != "" {
		t.Fatalf("%v %+v", err, m)
	}
}

func TestCheckWebSocket(t *testing.T) {
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plain" {
			_, _ = w.Write([]byte("hello"))
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err == nil {
			c.Close()
		}
	}))
	defer srv.Close()
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")
	m := Monitor{Name: "ws", Type: TypeWebSocket, URL: ws + "/socket", TimeoutSec: 5}
	if r := check(context.Background(), m, local()); !r.OK || r.Msg != "Upgraded" {
		t.Fatalf("socket: %+v", r)
	}
	m.URL = ws + "/plain"
	if r := check(context.Background(), m, local()); r.OK || !strings.Contains(r.Msg, "no upgrade") {
		t.Fatalf("plain page: %+v", r)
	}
}

func TestCheckHTTPJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"db":{"ok":false,"lag":12}}`))
	}))
	defer srv.Close()
	m := Monitor{Name: "j", Type: TypeHTTP, URL: srv.URL, TimeoutSec: 5, JSONPath: "db.ok", JSONExpect: "true"}
	if err := m.normalize(); err != nil {
		t.Fatal(err)
	}
	if r := check(context.Background(), m, local()); r.OK || !strings.Contains(r.Msg, `db.ok is "false", want == "true"`) {
		t.Fatalf("false: %+v", r)
	}
	m.JSONPath, m.JSONOp, m.JSONExpect = "db.lag", "<", "30"
	if r := check(context.Background(), m, local()); !r.OK {
		t.Fatalf("lag: %+v", r)
	}
	// with a keyword too, both must pass
	m.Keyword = "nowhere"
	if r := check(context.Background(), m, local()); r.OK {
		t.Fatalf("keyword: %+v", r)
	}
}

func TestDockerResult(t *testing.T) {
	cases := []struct {
		exit           int
		stdout, stderr string
		ok             bool
		msg            string
	}{
		{0, "running|\n", "", true, "running"},
		{0, "running|healthy", "", true, "running (healthy)"},
		{0, "running|unhealthy", "", false, "running (unhealthy)"},
		{0, "running|starting", "", false, "running (starting)"},
		{0, "exited|", "", false, "exited"},
		{1, "", "Error: No such container: web", false, "No such container"},
		{1, "", "permission denied while trying to connect to the Docker daemon socket", false, "No access to docker (is the user in the docker group?)"},
		{127, "", "sh: docker: not found", false, "docker is not installed here"},
	}
	for _, c := range cases {
		r := dockerResult(c.exit, c.stdout, c.stderr, 3)
		if r.OK != c.ok || r.Msg != c.msg {
			t.Errorf("%d %q %q: %+v", c.exit, c.stdout, c.stderr, r)
		}
	}
}

// The real docker of this machine, asked about a container nobody has.
func TestCheckDockerAsksDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker")
	}
	m := Monitor{Name: "d", Type: TypeDocker, Container: "nextboard-test-no-such-container", TimeoutSec: 10}
	r := check(context.Background(), m, local())
	if r.OK || (r.Msg != "No such container" && !strings.HasPrefix(r.Msg, "No access")) {
		t.Fatalf("%+v", r)
	}
}

// A game server that first sends a challenge, as current Source servers do.
func TestCheckSteam(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			req := buf[:n]
			if len(req) == len(a2sInfo) {
				_, _ = pc.WriteTo([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'A', 1, 2, 3, 4}, from)
				continue
			}
			if string(req[len(req)-4:]) != "\x01\x02\x03\x04" {
				continue
			}
			ans := []byte{0xFF, 0xFF, 0xFF, 0xFF, 'I', 17}
			for _, s := range []string{"My Server", "de_dust2", "csgo", "Counter-Strike"} {
				ans = append(append(ans, s...), 0)
			}
			ans = binary.LittleEndian.AppendUint16(ans, 730)
			ans = append(ans, 7, 24)
			_, _ = pc.WriteTo(ans, from)
		}
	}()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	m := Monitor{Name: "s", Type: TypeSteam, Host: "127.0.0.1", Port: port, TimeoutSec: 3}
	r := check(context.Background(), m, local())
	if !r.OK || r.Msg != "My Server · de_dust2 · 7/24 players" {
		t.Fatalf("%+v", r)
	}
	// nobody there
	quiet, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer quiet.Close()
	m.Port, m.TimeoutSec = quiet.LocalAddr().(*net.UDPAddr).Port, 1
	if r := check(context.Background(), m, local()); r.OK || r.Msg != "No answer" {
		t.Fatalf("quiet: %+v", r)
	}
}

func TestWindowTimes(t *testing.T) {
	jakarta, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Skip("no tz data")
	}
	once := Window{Mode: MaintOnce, Start: time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)}
	for _, c := range []struct {
		at time.Time
		on bool
	}{
		{time.Date(2026, 11, 7, 0, 59, 0, 0, time.UTC), false},
		{time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC), false},
	} {
		if once.activeAt(c.at) != c.on {
			t.Errorf("once at %v: want %v", c.at, c.on)
		}
	}
	// Saturdays 23:30 in Jakarta, for 2 hours: across midnight into Sunday
	weekly := Window{Mode: MaintWeekly, Days: []int{6}, From: "23:30", Minutes: 120, TZ: "Asia/Jakarta"}
	for _, c := range []struct {
		at time.Time
		on bool
	}{
		{time.Date(2026, 11, 7, 23, 29, 0, 0, jakarta), false}, // a Saturday
		{time.Date(2026, 11, 7, 23, 30, 0, 0, jakarta), true},
		{time.Date(2026, 11, 8, 1, 15, 0, 0, jakarta), true}, // Sunday, still in it
		{time.Date(2026, 11, 8, 1, 30, 0, 0, jakarta), false},
		{time.Date(2026, 11, 14, 16, 45, 0, 0, time.UTC), true}, // 23:45 in Jakarta, a week on
		{time.Date(2026, 11, 10, 23, 45, 0, 0, jakarta), false}, // a Tuesday
	} {
		if weekly.activeAt(c.at) != c.on {
			t.Errorf("weekly at %v: want %v", c.at, c.on)
		}
	}
	manual := Window{Mode: MaintManual, Active: true}
	if !manual.activeAt(time.Now()) {
		t.Error("manual on")
	}
}

func TestNormalizeWindows(t *testing.T) {
	ids := map[string]bool{"u_a": true, "u_b": true}
	at := time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC)
	out, err := normalizeWindows([]Window{
		{Title: " Deploy ", Mode: MaintOnce, Start: at, End: at.Add(time.Hour), MonitorIDs: []string{"u_a", "u_a"}, Days: []int{1}},
		{Title: "Backups", Mode: MaintWeekly, Days: []int{3, 1, 3}, From: "02:00", Minutes: 30},
		{Title: "Now", Mode: MaintManual, Active: true},
	}, ids)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Title != "Deploy" || len(out[0].MonitorIDs) != 1 || out[0].Days != nil || out[0].ID == "" {
		t.Errorf("once: %+v", out[0])
	}
	if fmt.Sprint(out[1].Days) != "[1 3]" {
		t.Errorf("weekly: %+v", out[1])
	}
	for _, bad := range [][]Window{
		{{Title: "", Mode: MaintManual}},
		{{Title: "x", Mode: "sometimes"}},
		{{Title: "x", Mode: MaintOnce, Start: at, End: at}},
		{{Title: "x", Mode: MaintWeekly, Days: []int{7}, From: "02:00", Minutes: 30}},
		{{Title: "x", Mode: MaintWeekly, Days: []int{1}, From: "2:00", Minutes: 30}},
		{{Title: "x", Mode: MaintWeekly, Days: []int{1}, From: "02:00", Minutes: 0}},
		{{Title: "x", Mode: MaintWeekly, Days: []int{1}, From: "02:00", Minutes: 30, TZ: "Mars/Olympus"}},
		{{Title: "x", Mode: MaintManual, MonitorIDs: []string{"u_other"}}},
	} {
		if _, err := normalizeWindows(bad, ids); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

// In maintenance: not checked, not counted, nobody told. After it, a down is news again.
func TestMaintenanceHoldsChecksAndAlerts(t *testing.T) {
	r := newRig(t, t.TempDir())
	m := httpMonitor("http://127.0.0.1:1/")
	m.Retries = 0
	v, err := r.s.Create(m)
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.s.Create(httpMonitor("http://127.0.0.1:2/"))
	if err != nil {
		t.Fatal(err)
	}
	r.beat(v.ID, up())
	if _, err := r.s.SetMaintenance(testProject, []Window{{Title: "MAINT-NOTE-SECRET", Mode: MaintManual, Active: true, MonitorIDs: []string{v.ID}}}); err != nil {
		t.Fatal(err)
	}
	e := r.s.entry(v.ID)
	beats := len(e.hist.beats)
	for i := 0; i < 3; i++ {
		r.clock.add(time.Minute)
		if next := r.s.runOnce(context.Background(), e, e.m); next > maintRecheck {
			t.Fatalf("next look in %v", next)
		}
	}
	got := r.view(v.ID)
	if got.Status != StatusMaintenance || got.Msg != "MAINT-NOTE-SECRET" || len(e.hist.beats) != beats || len(r.seen()) != 0 {
		t.Fatalf("in maintenance: %s %q beats %d→%d alerts %v", got.Status, got.Msg, beats, len(e.hist.beats), r.seen())
	}
	if *got.Uptime24h != 100 {
		t.Fatalf("uptime %v", *got.Uptime24h)
	}
	if r.view(other.ID).Status == StatusMaintenance {
		t.Fatal("a monitor the window does not cover")
	}

	// on the public page: "Maintenance", and nothing of the window
	r.s.Update(v.ID, func(m *Monitor) error { m.Public, m.PublicName = true, "Shop"; return nil })
	pub, _ := r.s.SetPublic(testProject, true, "")
	r.s.dropPublicCache()
	page := openPublic(t, r, pub.Token)
	if !strings.Contains(page, "Maintenance") || !strings.Contains(page, "under maintenance") || strings.Contains(page, "MAINT-NOTE-SECRET") {
		t.Fatalf("public page:\n%s", page)
	}

	// the window ends: the next check runs, and its down is news
	if _, err := r.s.SetMaintenance(testProject, nil); err != nil {
		t.Fatal(err)
	}
	r.beat(v.ID, down())
	if got := strings.Join(r.seen(), ","); got != KindDown {
		t.Fatalf("after maintenance: %s", got)
	}
}

func TestDeletingAMonitorPrunesItsWindows(t *testing.T) {
	r := newRig(t, t.TempDir())
	a, _ := r.s.Create(httpMonitor("http://127.0.0.1:1/"))
	b, _ := r.s.Create(httpMonitor("http://127.0.0.1:2/"))
	if _, err := r.s.SetMaintenance(testProject, []Window{
		{Title: "only a", Mode: MaintManual, Active: true, MonitorIDs: []string{a.ID}},
		{Title: "a and b", Mode: MaintManual, Active: true, MonitorIDs: []string{a.ID, b.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	ws := r.s.Settings(testProject).Maintenance
	// "only a" would otherwise cover every monitor
	if len(ws) != 1 || ws[0].Title != "a and b" || fmt.Sprint(ws[0].MonitorIDs) != "["+b.ID+"]" {
		t.Fatalf("%+v", ws)
	}
	// and survives a restart
	r.s.Close()
	r2 := newRig(t, r.st.Path(""))
	if ws := r2.s.Settings(testProject).Maintenance; len(ws) != 1 {
		t.Fatalf("after restart: %+v", ws)
	}
}
