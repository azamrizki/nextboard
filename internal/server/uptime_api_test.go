package server

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUptimeAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	h.srv.uptime.Start()
	t.Cleanup(h.srv.uptime.Close)
	pid, _ := h.homeWorkspace()

	var alive atomic.Bool
	alive.Store(true)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !alive.Load() {
			w.WriteHeader(503)
		}
	}))
	defer target.Close()

	conn, ev := h.eventsCh("uptimeBeat,uptimeMonitor,uptimeRemoved,attention")
	defer conn.Close()
	waitFor := func(what string, match func(map[string]any) bool) map[string]any {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case e := <-ev:
				if match(e) {
					return e
				}
			case <-deadline:
				t.Fatalf("tidak datang: %s", what)
			}
		}
	}
	beat := func(status string) func(map[string]any) bool {
		return func(e map[string]any) bool {
			m, _ := e["monitor"].(map[string]any)
			return e["type"] == "uptimeBeat" && m["status"] == status
		}
	}

	st, m := h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "site", "type": "http", "url": target.URL}, nil)
	if st != 201 || m["active"] != true || m["notify"] != true || m["retries"] != float64(1) {
		t.Fatalf("buat: %d %v", st, m)
	}
	id := m["id"].(string)
	waitFor("beat pertama", beat("up"))

	alive.Store(false)
	check := func() {
		t.Helper()
		if st, r := h.do("POST", "/api/uptime/"+id+"/check", nil, nil); st != 202 {
			t.Fatalf("check: %d %v", st, r)
		}
	}
	check()
	waitFor("pending", beat("pending"))
	check()
	waitFor("down", beat("down"))
	a := waitFor("attention down", func(e map[string]any) bool { return e["type"] == "attention" })["attention"].(map[string]any)
	if a["kind"] != "uptimeDown" || a["monitorId"] != id || a["projectId"] != pid || a["title"] != "site is down" {
		t.Fatalf("attention: %v", a)
	}
	alive.Store(true)
	check()
	waitFor("pulih", beat("up"))
	a = waitFor("attention up", func(e map[string]any) bool { return e["type"] == "attention" })["attention"].(map[string]any)
	if a["kind"] != "uptimeUp" {
		t.Fatalf("attention: %v", a)
	}

	var list struct {
		Items []map[string]any `json:"items"`
	}
	if h.getJSON("/api/projects/"+pid+"/uptime", &list); len(list.Items) != 1 || list.Items[0]["status"] != "up" {
		t.Fatalf("daftar: %v", list.Items)
	}
	if r := list.Items[0]["recent"].([]any); len(r) != 4 {
		t.Fatalf("recent: %v", r)
	}
	var hist map[string]any
	if st := h.getJSON("/api/uptime/"+id+"/history?range=1h", &hist); st != 200 || hist["stepSec"] != float64(60) {
		t.Fatalf("history: %d %v", st, hist)
	}
	if st, _ := h.do("GET", "/api/uptime/"+id+"/history?range=9y", nil, nil); st != 400 {
		t.Fatalf("range salah: %d", st)
	}
	var evs struct {
		Items []map[string]any `json:"items"`
	}
	if h.getJSON("/api/uptime/"+id+"/events", &evs); len(evs.Items) != 3 || evs.Items[0]["status"] != "up" {
		t.Fatalf("event: %v", evs.Items)
	}

	// PATCH merges; what is protected stays
	st, m = h.do("PATCH", "/api/uptime/"+id, map[string]any{"name": "renamed", "active": false, "id": "u_hacked", "projectId": "p_other"}, nil)
	if st != 200 || m["name"] != "renamed" || m["status"] != "paused" || m["id"] != id || m["projectId"] != pid || m["url"] != target.URL {
		t.Fatalf("ubah: %d %v", st, m)
	}
	if st, _ := h.do("POST", "/api/uptime/"+id+"/check", nil, nil); st != 409 {
		t.Fatalf("check saat pause: %d", st)
	}
	if st, _ := h.do("PATCH", "/api/uptime/"+id, map[string]any{"url": "ftp://x"}, nil); st != 400 {
		t.Fatalf("url buruk: %d", st)
	}
	for _, bad := range []map[string]any{{"type": "http"}, {"name": "x", "type": "nope"}, {"name": "x", "type": "ping", "host": "-f"}} {
		if st, _ := h.do("POST", "/api/projects/"+pid+"/uptime", bad, nil); st != 400 {
			t.Fatalf("%v: %d", bad, st)
		}
	}
	if st, _ := h.do("POST", "/api/projects/nope/uptime", map[string]any{"name": "x"}, nil); st != 404 {
		t.Fatalf("project tak ada: %d", st)
	}

	// the data is the hub's own: the file API does not hand it out
	if st, _ := h.do("GET", "/api/machines/local/fs/read?path="+h.srv.st.Path("uptime", "monitors.json"), nil, nil); st != 403 {
		t.Fatalf("baca lewat file API: %d", st)
	}
	if fi, err := os.Stat(h.srv.st.Path("uptime", "monitors.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("izin file: %v %v", fi, err)
	}

	// push needs no login, only the token
	st, m = h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "cron", "type": "push"}, nil)
	if st != 201 {
		t.Fatalf("buat push: %d %v", st, m)
	}
	pushID, token := m["id"].(string), m["pushToken"].(string)
	plain := &http.Client{}
	get := func(path string) int {
		t.Helper()
		resp, err := plain.Get(h.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if st := get("/api/uptime/" + id + "/events"); st != 401 {
		t.Fatalf("API lain tanpa login: %d", st)
	}
	if st := get("/api/push/wrong-token"); st != 404 {
		t.Fatalf("token salah: %d", st)
	}
	if st := get("/api/push/" + token + "?status=up&msg=done&ping=7"); st != 200 {
		t.Fatalf("push: %d", st)
	}
	e := waitFor("beat push", func(e map[string]any) bool { return e["type"] == "uptimeBeat" && e["monitorId"] == pushID })
	if pm := e["monitor"].(map[string]any); pm["msg"] != "done" || pm["ms"] != float64(7) {
		t.Fatalf("isi push: %v", pm)
	}

	// webhook
	var got atomic.Value
	got.Store("")
	hook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.Store(string(b))
	}))
	defer hook.Close()
	if st, _ := h.do("PUT", "/api/projects/"+pid+"/uptime/settings", map[string]any{"webhook": "nope"}, nil); st != 400 {
		t.Fatalf("webhook buruk: %d", st)
	}
	if st, _ := h.do("PUT", "/api/projects/"+pid+"/uptime/settings", map[string]any{"webhook": hook.URL}, nil); st != 200 {
		t.Fatalf("webhook: %d", st)
	}
	if st, _ := h.do("POST", "/api/projects/"+pid+"/uptime/settings/test", nil, nil); st != 200 || !strings.Contains(got.Load().(string), "alerts are on") {
		t.Fatalf("tes webhook: %d %q", st, got.Load())
	}
	hook.Close()
	if st, r := h.do("POST", "/api/projects/"+pid+"/uptime/settings/test", nil, nil); st != 502 || strings.Contains(fmt.Sprint(r), hook.URL) {
		t.Fatalf("tes webhook mati: %d %v", st, r)
	}

	if st, _ := h.do("DELETE", "/api/uptime/"+pushID, nil, nil); st != 200 {
		t.Fatalf("hapus: %d", st)
	}
	waitFor("removed", func(e map[string]any) bool { return e["type"] == "uptimeRemoved" && e["id"] == pushID })
	if st := get("/api/push/" + token); st != 404 {
		t.Fatalf("token monitor terhapus: %d", st)
	}

	// a monitor goes with its project
	st, np := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "other"}, nil)
	if st != 201 {
		t.Fatalf("project: %d %v", st, np)
	}
	npid := np["id"].(string)
	st, m = h.do("POST", "/api/projects/"+npid+"/uptime", map[string]any{"name": "x", "type": "ping", "host": "127.0.0.1", "active": false}, nil)
	if st != 201 {
		t.Fatalf("monitor: %d %v", st, m)
	}
	dir := h.srv.st.Path("uptime", m["id"].(string))
	if st, _ := h.do("DELETE", "/api/projects/"+npid, nil, nil); st != 200 {
		t.Fatalf("hapus project: %d", st)
	}
	if _, err := os.Stat(filepath.Clean(dir)); !os.IsNotExist(err) {
		t.Fatalf("folder monitor masih ada: %v", err)
	}
}

// From a server: what only that server can reach, over the SSH connection the hub
// already has. The target here listens on loopback, on "both" machines.
func TestUptimeFromServer(t *testing.T) {
	port, key := startSSHServer(t)
	h := newTestHub(t)
	h.setup()
	h.srv.uptime.Start()
	t.Cleanup(h.srv.uptime.Close)
	pid, _ := h.homeWorkspace()
	mid := h.connectMachine(port, key)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "from server") })}
	go srv.Serve(ln)
	defer srv.Close()

	conn, ev := h.eventsCh("uptimeBeat")
	defer conn.Close()
	wait := func(id string) map[string]any {
		t.Helper()
		deadline := time.After(15 * time.Second)
		for {
			select {
			case e := <-ev:
				if e["monitorId"] == id {
					return e["monitor"].(map[string]any)
				}
			case <-deadline:
				t.Fatalf("tidak ada beat untuk %s", id)
			}
		}
	}
	addr := ln.Addr().(*net.TCPAddr)
	bodies := []map[string]any{
		{"name": "web", "type": "http", "machineId": mid, "url": "http://" + addr.String() + "/", "keyword": "from server"},
		{"name": "port", "type": "tcp", "machineId": mid, "host": "127.0.0.1", "port": addr.Port},
	}
	if _, err := exec.LookPath("ping"); err == nil { // minimal systems have none
		bodies = append(bodies, map[string]any{"name": "ping", "type": "ping", "machineId": mid, "host": "127.0.0.1"})
	}
	for _, body := range bodies {
		st, m := h.do("POST", "/api/projects/"+pid+"/uptime", body, nil)
		if st != 201 {
			t.Fatalf("%v: %d %v", body, st, m)
		}
		if got := wait(m["id"].(string)); got["status"] != "up" {
			t.Fatalf("%v: %v", body["name"], got)
		}
	}
	if st, _ := h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "x", "type": "tcp", "machineId": "m_nope", "host": "a.b", "port": 80}, nil); st != 400 {
		t.Fatalf("machine tak ada: %d", st)
	}
}
