package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// instancesHome is a scratch HOME with a systemctl and a loginctl first on PATH that only note
// their calls: nothing here reaches this machine's service manager, and no real unit is made
// (a hub under air would make real ones, so the fake always comes first). $FAKE_SHOW is the exit
// code of `systemctl --user show-environment`: 0 is a service manager that answers.
func instancesHome(t *testing.T) (home, calls string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	home = hubDataDir(t)
	t.Setenv("HOME", home)
	t.Setenv("NEXTBOARD_HOME", "")
	t.Setenv("NEXTBOARD_INSTANCE", "")
	tools := t.TempDir()
	calls = filepath.Join(tools, "calls.log")
	script := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> \"" + calls + "\"\n" +
		"case \"$*\" in\n*show-environment*) exit ${FAKE_SHOW:-0} ;;\nesac\nexit 0\n"
	for _, n := range []string{"systemctl", "loginctl", "launchctl"} {
		if err := os.WriteFile(filepath.Join(tools, n), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools+":/usr/bin:/bin")
	t.Setenv("FAKE_SHOW", "1")
	return home, calls
}

func writeInstanceConfig(t *testing.T, home, folder, cfg string) string {
	t.Helper()
	dir := filepath.Join(home, folder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// pairedDevice pairs a device with h and signs it in: a client without h's admin cookie, and its
// Authorization header.
func pairedDevice(t *testing.T, h *testHub) (*testHub, map[string]string) {
	t.Helper()
	_, ps := h.do("POST", "/api/pair/start", nil, nil)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	anon := &testHub{t: t, srv: h.srv, ts: h.ts, c: &http.Client{Transport: h.ts.Client().Transport}}
	st, pc := anon.do("POST", "/api/pair/complete", map[string]any{
		"code": ps["code"], "deviceName": "Tablet", "platform": "android", "publicKey": base64.StdEncoding.EncodeToString(pub),
	}, nil)
	if st != 200 {
		t.Fatalf("pair %d %v", st, pc)
	}
	_, ch := anon.do("POST", "/api/auth/challenge", map[string]any{"deviceId": pc["deviceId"]}, nil)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(ch["nonce"].(string))))
	_, tok := anon.do("POST", "/api/auth/device", map[string]any{"deviceId": pc["deviceId"], "nonce": ch["nonce"], "sig": sig}, nil)
	return anon, map[string]string{"Authorization": "Bearer " + tok["accessToken"].(string)}
}

// The info panel's fields: the instance, its folders and service, and the other instances of
// This PC with their addresses, for admins; a paired device sees only its own hub.
func TestHubInfoInstanceFields(t *testing.T) {
	home, _ := instancesHome(t)
	dir := writeInstanceConfig(t, home, ".nextboard-work", `{"instance":"work","hubId":"workhub123"}`)
	writeInstanceConfig(t, home, ".nextboard-sib", `{"instance":"sib","hubId":"sibhub1234","hubName":"devbox · sib","listen":"127.0.0.1:41412"}`)
	h := newTestHubAt(t, dir)
	h.setup()

	st, hub := h.do("GET", "/api/hub", nil, nil)
	if st != 200 {
		t.Fatalf("hub %d %v", st, hub)
	}
	if in, _ := hub["instance"].(map[string]any); in["name"] != "work" || in["default"] != false {
		t.Fatalf("instance %v", hub["instance"])
	}
	if hub["hubId"] != "workhub123" || hub["dataDir"] != dir || hub["configFile"] != filepath.Join(dir, "config.json") {
		t.Fatalf("ids and paths: %v", hub)
	}
	if svc, _ := hub["service"].(map[string]any); svc["kind"] != "none" || svc["managed"] != false {
		t.Fatalf("a hub without a unit is not a service: %v", hub["service"])
	}
	if hub["replacedSinceStart"] != false || hub["statusListen"] != "" {
		t.Fatalf("binary and status: %v", hub)
	}
	sibs, _ := hub["siblings"].([]any)
	if len(sibs) != 1 {
		t.Fatalf("siblings %v", hub["siblings"])
	}
	sib := sibs[0].(map[string]any)
	if sib["name"] != "sib" || sib["hubName"] != "devbox · sib" || sib["port"] != float64(41412) || sib["state"] != "stopped" || sib["running"] != false {
		t.Fatalf("sibling %v", sib)
	}
	if urls := anyStrings(sib["urls"]); !slices.Equal(urls, []string{"http://127.0.0.1:41412"}) {
		t.Fatalf("sibling urls %v", urls)
	}

	// a unit of this instance that serves this folder: the service, not run by it
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unit := "[Service]\nExecStart=/opt/nb/nextboard serve --instance work --data-dir " + dir + "\n"
	if err := os.WriteFile(filepath.Join(unitDir, "nextboard-work.service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	h.srv.insts.svcAt = time.Time{}
	_, hub = h.do("GET", "/api/hub", nil, nil)
	svc, _ := hub["service"].(map[string]any)
	if svc["kind"] != "systemd" || svc["name"] != "nextboard-work.service" || svc["unitPath"] != filepath.Join(unitDir, "nextboard-work.service") || svc["managed"] != false {
		t.Fatalf("service %v", svc)
	}

	device, bearer := pairedDevice(t, h)
	st, dev := device.do("GET", "/api/hub", nil, bearer)
	if st != 200 {
		t.Fatalf("device hub %d", st)
	}
	for _, k := range []string{"dataDir", "configFile", "siblings", "binary"} {
		if _, ok := dev[k]; ok {
			t.Errorf("a paired device sees %s: %v", k, dev[k])
		}
	}
	if svc, _ := dev["service"].(map[string]any); svc["unitPath"] != nil || svc["kind"] != "systemd" {
		t.Errorf("device service %v", dev["service"])
	}
	raw, _ := json.Marshal(dev)
	if strings.Contains(string(raw), home) {
		t.Errorf("a paired device sees a path: %s", raw)
	}
	if st, _ := device.do("GET", "/api/instances", nil, bearer); st != 403 {
		t.Errorf("device GET /api/instances %d", st)
	}
}

// replacedSinceStart turns true once another program is at the hub's path.
func TestHubBinaryReplacedSinceStart(t *testing.T) {
	instancesHome(t)
	bin := filepath.Join(t.TempDir(), "nextboard")
	if err := os.WriteFile(bin, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	saved := startBinary
	t.Cleanup(func() { startBinary = saved })
	fi, _ := os.Stat(bin)
	startBinary = binaryStamp{path: bin, fi: fi}
	h := newTestHub(t)
	h.setup()
	if _, hub := h.do("GET", "/api/hub", nil, nil); hub["replacedSinceStart"] != false {
		t.Fatalf("before: %v", hub["replacedSinceStart"])
	}
	// what an update does: a new file renamed over the old one
	next := bin + ".new"
	if err := os.WriteFile(next, []byte("new build, longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, bin); err != nil {
		t.Fatal(err)
	}
	_, hub := h.do("GET", "/api/hub", nil, nil)
	if hub["replacedSinceStart"] != true {
		t.Fatalf("after: %v", hub["replacedSinceStart"])
	}
	if b, _ := hub["binary"].(map[string]any); b["path"] != bin || b["replacedSinceStart"] != true {
		t.Fatalf("binary %v", hub["binary"])
	}
	_ = os.Remove(bin)
	if _, hub := h.do("GET", "/api/hub", nil, nil); hub["replacedSinceStart"] != false {
		t.Fatalf("a binary that is gone is no replacement: %v", hub["replacedSinceStart"])
	}
}

// The manifest is named after the hub, or a named instance before the hub has a name; a default
// instance without one keeps the bundle's names. The status listener knows none of these routes,
// so no path of the hub reaches it.
func TestManifestPerInstanceAndNothingOnStatus(t *testing.T) {
	home, _ := instancesHome(t)
	dir := writeInstanceConfig(t, home, ".nextboard-work", `{"instance":"work"}`)
	h := newTestHubAt(t, dir)
	get := func(h *testHub, path string) (int, map[string]any) {
		resp, err := h.c.Get(h.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if ct := resp.Header.Get("Content-Type"); resp.StatusCode == 200 && !strings.Contains(ct, "manifest+json") {
			t.Errorf("content type %q", ct)
		}
		return resp.StatusCode, m
	}
	if st, m := get(h, "/manifest.json"); st != 200 || m["name"] != "NextBoard · work" || m["short_name"] != "work" || m["start_url"] != "/" {
		t.Fatalf("named manifest %d %v", st, m)
	}
	h.setup() // hub name devbox
	if st, m := get(h, "/manifest.json"); st != 200 || m["name"] != "NextBoard · devbox" || m["short_name"] != "devbox" {
		t.Fatalf("named manifest after setup %d %v", st, m)
	}
	def := newTestHub(t)
	if st, m := get(def, "/manifest.json"); st != 200 || m["name"] != "NextBoard" || m["short_name"] != "NextBoard" {
		t.Fatalf("default manifest %d %v", st, m)
	}
	def.setup()
	if st, m := get(def, "/manifest.json"); st != 200 || m["name"] != "NextBoard · devbox" {
		t.Fatalf("default manifest after setup %d %v", st, m)
	}

	marked := writeInstanceConfig(t, home, ".nextboard-mark", `{"instance":"mark"}`)
	if err := os.MkdirAll(filepath.Join(marked, "SECRETMARK"), 0o700); err != nil {
		t.Fatal(err)
	}
	status := httptest.NewServer(http.HandlerFunc(h.srv.handlePublicStatus))
	defer status.Close()
	for _, p := range []string{"/api/hub", "/api/instances", "/manifest.json", "/status/x/api/hub"} {
		resp, err := http.Get(status.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 || strings.Contains(string(raw), home) || strings.Contains(string(raw), "SECRETMARK") {
			t.Errorf("status listener %s: %d %s", p, resp.StatusCode, raw)
		}
	}
}

func originOf(h *testHub) map[string]string { return map[string]string{"Origin": h.ts.URL} }

func withHeaders(a map[string]string, more ...map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for _, m := range more {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// POST /api/instances refuses everything that is not this hub's admin, from this hub's own
// page, with the admin password, asking for a new name on free ports.
func TestInstanceCreateRefusals(t *testing.T) {
	home, calls := instancesHome(t)
	dir := writeInstanceConfig(t, home, ".nextboard-work", `{"instance":"work","hubId":"workhub123"}`)
	writeInstanceConfig(t, home, ".nextboard-sib", `{"instance":"sib","listen":"127.0.0.1:41412"}`)
	h := newTestHubAt(t, dir)
	h.setup()
	origin := originOf(h)
	body := func(m map[string]any) map[string]any {
		out := map[string]any{"name": "side", "password": "rahasia123", "listenHost": "127.0.0.1"}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	device, bearer := pairedDevice(t, h)
	if st, out := device.do("POST", "/api/instances", body(nil), withHeaders(bearer, origin)); st != 403 || out["error"] != "admin only" {
		t.Errorf("device: %d %v", st, out)
	}
	cases := []struct {
		what string
		body map[string]any
		hdr  map[string]string
		st   int
		code string
	}{
		{"no X-NextBoard", body(nil), withHeaders(origin, map[string]string{"X-NextBoard": ""}), 403, ""},
		{"no Origin", body(nil), nil, 403, "origin"},
		{"a sibling's origin", body(nil), map[string]string{"Origin": "http://127.0.0.1:41412"}, 403, "origin"},
		{"bad name", body(map[string]any{"name": "Side_1"}), origin, 400, "bad_instance_name"},
		{"default", body(map[string]any{"name": "default"}), origin, 400, "bad_instance_name"},
		{"another host", body(map[string]any{"listenHost": "10.9.8.7"}), origin, 400, "bad_listen"},
		{"low port", body(map[string]any{"port": 80}), origin, 400, "bad_listen"},
		{"long hub name", body(map[string]any{"hubName": strings.Repeat("x", 65)}), origin, 400, "bad_hub_name"},
		{"a sibling's name", body(map[string]any{"name": "sib"}), origin, 409, "instance_exists"},
		{"this instance's name", body(map[string]any{"name": "work"}), origin, 409, "instance_exists"},
		{"a sibling's port", body(map[string]any{"port": 41412}), origin, 409, "port_in_use"},
		{"a busy port", body(map[string]any{"port": busyPort}), origin, 409, "port_in_use"},
	}
	for _, c := range cases {
		st, out := h.do("POST", "/api/instances", c.body, c.hdr)
		if st != c.st || (c.code != "" && out["code"] != c.code) {
			t.Errorf("%s: %d %v, want %d %s", c.what, st, out, c.st, c.code)
		}
	}

	// beyond loopback only on this hub's own address, and only when asked for
	h.srv.listen = "0.0.0.0:0"
	if st, out := h.do("POST", "/api/instances", body(map[string]any{"listenHost": "0.0.0.0"}), origin); st != 409 || out["code"] != "network_not_allowed" {
		t.Errorf("network without allowNetwork: %d %v", st, out)
	}
	h.srv.listen = "127.0.0.1:0"

	for i := range 5 {
		if st, out := h.do("POST", "/api/instances", body(map[string]any{"password": "salah"}), origin); st != 403 || out["code"] != "bad_password" {
			t.Fatalf("wrong password %d: %d %v", i, st, out)
		}
	}
	if st, out := h.do("POST", "/api/instances", body(nil), origin); st != 429 {
		t.Fatalf("after 5 wrong passwords: %d %v", st, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".nextboard-side")); !os.IsNotExist(err) {
		t.Fatalf("a refused create left a folder: %v", err)
	}
	if c := readFile(t, calls); strings.Contains(c, "enable") || strings.Contains(c, "restart") {
		t.Fatalf("a refused create reached the service manager: %s", c)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// A new instance from the web: its own folder, ports and unit, started by the (fake) service
// manager, and a setup link that opens the new hub once.
func TestInstanceCreateWithService(t *testing.T) {
	home, calls := instancesHome(t)
	t.Setenv("FAKE_SHOW", "0")
	saved := instanceHealthWait
	instanceHealthWait = 200 * time.Millisecond
	t.Cleanup(func() { instanceHealthWait = saved })
	dir := writeInstanceConfig(t, home, ".nextboard-work", `{"instance":"work"}`)
	h := newTestHubAt(t, dir)
	h.setup()

	st, list := h.do("GET", "/api/instances", nil, nil)
	sug, _ := list["suggest"].(map[string]any)
	if st != 200 || list["self"] != "work" || sug["host"] != "127.0.0.1" || sug["loopback"] != true || sug["port"] == nil {
		t.Fatalf("instances %d %v", st, list)
	}
	if items, _ := list["items"].([]any); len(items) != 1 || items[0].(map[string]any)["name"] != "work" {
		t.Fatalf("items %v", list["items"])
	}
	port := int(sug["port"].(float64))

	st, out := h.do("POST", "/api/instances", map[string]any{
		"name": "side", "hubName": "devbox · side", "listenHost": "127.0.0.1", "port": port, "password": "rahasia123",
	}, originOf(h))
	if st != 201 {
		t.Fatalf("create %d %v", st, out)
	}
	side := filepath.Join(home, ".nextboard-side")
	listen := "127.0.0.1:" + strconv.Itoa(port)
	if out["instance"] != "side" || out["dataDir"] != side || out["listen"] != listen || out["started"] != true || out["healthy"] != false {
		t.Fatalf("created %v", out)
	}
	if out["setupCode"] != nil || out["SetupCode"] != nil {
		t.Fatalf("the code goes only inside the setup link: %v", out)
	}
	if r, _ := out["previewRange"].([]any); len(r) != 2 || int(r[0].(float64)) != port+8 {
		t.Fatalf("the suggested port takes its block's previews: %v", out["previewRange"])
	}
	setup := anyStrings(out["setupUrls"])
	if len(setup) != 1 || !strings.HasPrefix(setup[0], "http://"+listen+"/setup?code=") {
		t.Fatalf("setup urls %v", setup)
	}
	if permOf(t, side) != 0o700 || permOf(t, filepath.Join(side, "config.json")) != 0o600 {
		t.Fatalf("modes %v %v", permOf(t, side), permOf(t, filepath.Join(side, "config.json")))
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", "nextboard-side.service")
	unit := readFile(t, unitPath)
	if !strings.Contains(unit, " serve --instance side --data-dir "+side+"\n") || !strings.Contains(unit, `Description=NextBoard hub "side"`) {
		t.Fatalf("unit:\n%s", unit)
	}
	if svc, _ := out["service"].(map[string]any); svc["name"] != "nextboard-side.service" || svc["unitPath"] != unitPath {
		t.Fatalf("service %v", out["service"])
	}
	log := readFile(t, calls)
	for _, want := range []string{"systemctl --user daemon-reload", "systemctl --user enable nextboard-side", "systemctl --user restart nextboard-side", "loginctl enable-linger"} {
		if !strings.Contains(log, want) {
			t.Errorf("calls lack %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "nextboard-work") || strings.Contains(log, "restart nextboard\n") {
		t.Errorf("another unit was touched:\n%s", log)
	}

	// the new instance is listed now, and taken
	if st, out := h.do("POST", "/api/instances", map[string]any{"name": "side", "password": "rahasia123"}, originOf(h)); st != 409 || out["code"] != "instance_exists" {
		t.Fatalf("again: %d %v", st, out)
	}
	_, hub := h.do("GET", "/api/hub", nil, nil)
	if sibs, _ := hub["siblings"].([]any); len(sibs) != 1 || sibs[0].(map[string]any)["name"] != "side" || sibs[0].(map[string]any)["service"] != "nextboard-side.service" {
		t.Fatalf("siblings after create: %v", hub["siblings"])
	}

	// the new hub, as its service would start it: the link opens it once, with the name given
	code := strings.TrimPrefix(setup[0], "http://"+listen+"/setup?code=")
	nh := newTestHubAt(t, side)
	if _, ss := nh.do("GET", "/api/setup/status", nil, nil); ss["suggestedHubName"] != "devbox · side" || ss["instance"] != "side" {
		t.Fatalf("new hub's setup status %v", ss)
	}
	if st, out := nh.do("POST", "/api/setup", map[string]any{"code": code, "password": "rahasia456", "hubName": "devbox · side"}, nil); st != 200 {
		t.Fatalf("setup with the link %d %v", st, out)
	}
	if st, _ := nh.do("POST", "/api/setup", map[string]any{"code": code, "password": "rahasia456", "hubName": "x"}, nil); st == 200 {
		t.Fatal("the setup link works twice")
	}
}

// Without a service manager (a hub run by hand), the instance is made and the answer says how to
// start it; two creates at once of one name make it once.
func TestInstanceCreateWithoutServiceManager(t *testing.T) {
	home, calls := instancesHome(t) // FAKE_SHOW=1: show-environment fails
	dir := writeInstanceConfig(t, home, ".nextboard-work", `{"instance":"work"}`)
	h := newTestHubAt(t, dir)
	h.setup()
	var wg sync.WaitGroup
	sts := make([]int, 2)
	outs := make([]map[string]any, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sts[i], outs[i] = h.do("POST", "/api/instances", map[string]any{"name": "hand", "password": "rahasia123"}, originOf(h))
		}()
	}
	wg.Wait()
	slices.Sort(sts)
	if !slices.Equal(sts, []int{201, 409}) {
		t.Fatalf("two creates: %v %v", sts, outs)
	}
	out := outs[0]
	if out["code"] != nil {
		out = outs[1]
	}
	if out["started"] != false || out["command"] != "nextboard serve --instance hand" || out["service"] != nil {
		t.Fatalf("created %v", out)
	}
	if len(anyStrings(out["setupUrls"])) == 0 {
		t.Fatalf("no setup link: %v", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "nextboard-hand.service")); !os.IsNotExist(err) {
		t.Fatalf("a unit without a service manager: %v", err)
	}
	if c := readFile(t, calls); strings.Contains(c, "enable") || strings.Contains(c, "restart") {
		t.Fatalf("calls %s", c)
	}
}
