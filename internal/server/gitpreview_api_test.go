package server

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newP7Hub is newTestHub with the preview listeners on a free scratch port
// range (never the default 12320–12339) and git isolated from the user's
// global config.
func newP7Hub(t *testing.T) *testHub {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	os.WriteFile(cfg, []byte("[user]\n\tname = Tester\n\temail = tester@example.invalid\n[init]\n\tdefaultBranch = main\n"), 0o600)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_ASKPASS", "SSH_ASKPASS", "GIT_DIR", "GIT_WORK_TREE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	lo, hi := freePreviewRange(t, 3)
	dir := hubDataDir(t)
	body := fmt.Sprintf(`{"schemaVersion":1,"commands":{"planner":"off"},"previews":{"auto":true,"portRange":[%d,%d]}}`, lo, hi)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.review.Close) // a snapshot still running would write the folder back
	ts := httptest.NewServer(srv)
	srv.sessions.Start()
	srv.startOrchestration()
	t.Cleanup(func() {
		srv.stopOrchestration()
		srv.closeGitPreview()
		srv.jobs.Close()
		srv.sessions.Shutdown()
		srv.boards.Close()
		ts.Close()
	})
	jar, _ := cookiejar.New(nil)
	h := &testHub{t: t, srv: srv, ts: ts, c: &http.Client{Transport: ts.Client().Transport, Jar: jar}}
	h.setup()
	return h
}

func freePreviewRange(t *testing.T, n int) (int, int) {
	t.Helper()
	for i := 0; i < 50; i++ {
		base := 30000 + rand.IntN(25000)
		ok := true
		for p := base; p < base+n; p++ {
			l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
			if err != nil {
				ok = false
				break
			}
			l.Close()
		}
		if ok {
			return base, base + n - 1
		}
	}
	t.Fatal("no free range")
	return 0, 0
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (h *testHub) gitProject(dir string) (string, string) {
	h.t.Helper()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "gitp"}, nil)
	if st != 201 {
		h.t.Fatalf("project %d %v", st, p)
	}
	return p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
}

func (h *testHub) waitJob(id string) map[string]any {
	h.t.Helper()
	var j map[string]any
	waitTrue(h.t, "job "+id, 30*time.Second, func() bool {
		_, j = h.do("GET", "/api/jobs/"+id, nil, nil)
		return j["state"] != "queued" && j["state"] != "running"
	})
	return j
}

func TestGitAPI(t *testing.T) {
	h := newP7Hub(t)
	dir := t.TempDir()
	pid, _ := h.gitProject(dir)
	sub := h.srv.events.Subscribe("git")
	defer h.srv.events.Unsubscribe(sub)

	// Not a repository yet → isRepo:false; init creates one on main.
	if st, s := h.do("GET", "/api/projects/"+pid+"/git/status", nil, nil); st != 200 || s["isRepo"] != false {
		t.Fatalf("status %d %v", st, s)
	}
	if st, s := h.do("POST", "/api/projects/"+pid+"/git/init", nil, nil); st != 200 || s["isRepo"] != true || s["branch"] != "main" {
		t.Fatalf("init %d %v", st, s)
	}
	if st, s := h.do("POST", "/api/projects/"+pid+"/git/init", map[string]any{}, nil); st != 409 || s["code"] != "exists" {
		t.Fatalf("init again %d %v", st, s)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	if st, s := h.do("POST", "/api/projects/"+pid+"/git/commit", map[string]any{"message": "", "all": true}, nil); st != 400 || s["code"] != "empty_message" {
		t.Fatalf("empty %d %v", st, s)
	}
	if st, s := h.do("GET", "/api/projects/"+pid+"/git/suggest", nil, nil); st != 200 || s["subject"] != "chore: update 2 files" || s["source"] != "heuristic" {
		t.Fatalf("suggest %d %v", st, s)
	}
	st, c := h.do("POST", "/api/projects/"+pid+"/git/commit", map[string]any{"message": "Add a", "paths": []string{"a.txt"}}, nil)
	if st != 200 || c["commit"].(map[string]any)["subject"] != "Add a" || len(c["status"].(map[string]any)["files"].([]any)) != 1 {
		t.Fatalf("commit %d %v", st, c)
	}
	select {
	case msg := <-sub.C:
		if !strings.Contains(string(msg), `"type":"git"`) || !strings.Contains(string(msg), pid) {
			t.Fatalf("event %s", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no git event")
	}
	if _, l := h.do("GET", "/api/projects/"+pid+"/git/log?limit=5", nil, nil); len(l["items"].([]any)) != 1 {
		t.Fatalf("log %v", l)
	}
	if st, d := h.do("GET", "/api/projects/"+pid+"/git/diff?path=b.txt", nil, nil); st != 200 || !strings.Contains(d["diff"].(string), "+b") {
		t.Fatalf("diff %d %v", st, d)
	}
	if st, d := h.do("GET", "/api/projects/"+pid+"/git/diff?path=../x", nil, nil); st != 400 || d["code"] != "bad_path" {
		t.Fatalf("bad diff %d %v", st, d)
	}
	// Discard → trash → restore.
	st, dr := h.do("POST", "/api/projects/"+pid+"/git/discard", map[string]any{"paths": []string{"b.txt"}}, nil)
	if st != 200 || !strings.HasPrefix(dr["trashedTo"].(string), ".git/nextboard-trash/") {
		t.Fatalf("discard %d %v", st, dr)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("b.txt still there")
	}
	_, tr := h.do("GET", "/api/projects/"+pid+"/git/trash", nil, nil)
	items := tr["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("trash %v", tr)
	}
	id := items[0].(map[string]any)["id"].(string)
	if st, rr := h.do("POST", "/api/projects/"+pid+"/git/trash/restore", map[string]any{"id": id}, nil); st != 200 || len(rr["restored"].([]any)) != 1 {
		t.Fatalf("restore %d %v", st, rr)
	}
	// Branches (names with a slash in the URL).
	if st, s := h.do("POST", "/api/projects/"+pid+"/git/branches", map[string]any{"name": "feature/x"}, nil); st != 200 || s["branch"] != "feature/x" {
		t.Fatalf("branch %d %v", st, s)
	}
	if st, s := h.do("POST", "/api/projects/"+pid+"/git/branches", map[string]any{"name": "bad name"}, nil); st != 400 || s["code"] != "bad_ref" {
		t.Fatalf("bad ref %d %v", st, s)
	}
	if st, s := h.do("POST", "/api/projects/"+pid+"/git/switch", map[string]any{"name": "main"}, nil); st != 200 || s["branch"] != "main" {
		t.Fatalf("switch %d %v", st, s)
	}
	if _, b := h.do("GET", "/api/projects/"+pid+"/git/branches", nil, nil); b["current"] != "main" || len(b["items"].([]any)) != 2 {
		t.Fatalf("branches %v", b)
	}
	if st, s := h.do("DELETE", "/api/projects/"+pid+"/git/branches/feature%2Fx", nil, nil); st != 200 || s["ok"] != true {
		t.Fatalf("delete %d %v", st, s)
	}
	if st, s := h.do("DELETE", "/api/projects/"+pid+"/git/branches/nope", nil, nil); st != 404 || s["code"] != "not_found" {
		t.Fatalf("delete missing %d %v", st, s)
	}
	// Push / pull jobs against a local bare remote.
	bare := filepath.Join(t.TempDir(), "r.git")
	runGit(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	runGit(t, dir, "remote", "add", "origin", bare)
	st, j := h.do("POST", "/api/projects/"+pid+"/git/push", nil, nil)
	if st != 202 || j["kind"] != "git" || j["purpose"] != "push" {
		t.Fatalf("push %d %v", st, j)
	}
	if j = h.waitJob(j["id"].(string)); j["state"] != "done" {
		t.Fatalf("push job %v", j)
	}
	if _, s := h.do("GET", "/api/projects/"+pid+"/git/status", nil, nil); s["upstream"] != "origin/main" {
		t.Fatalf("upstream %v", s)
	}
	st, j = h.do("POST", "/api/projects/"+pid+"/git/pull", nil, nil)
	if st != 202 {
		t.Fatalf("pull %d %v", st, j)
	}
	if j = h.waitJob(j["id"].(string)); j["state"] != "done" {
		t.Fatalf("pull job %v", j)
	}
	// Unknown project.
	if st, s := h.do("GET", "/api/projects/p_nope/git/status", nil, nil); st != 404 || s["code"] != "project_not_found" {
		t.Fatalf("missing project %d %v", st, s)
	}
	// Panel selection for the command bar.
	if st, s := h.do("PUT", "/api/projects/"+pid+"/git/selection", map[string]any{"paths": []string{"x/../y"}}, nil); st != 200 || s["paths"].([]any)[0] != "y" {
		t.Fatalf("selection %d %v", st, s)
	}
	if st, s := h.do("PUT", "/api/projects/"+pid+"/git/selection", map[string]any{"paths": []string{"/etc/passwd"}}, nil); st != 400 {
		t.Fatalf("bad selection %d %v", st, s)
	}
}

// TestGitCommandBar: typed git steps run at once through the git service
// (no confirmation) and report in the project thread.
func TestGitCommandBar(t *testing.T) {
	h := newP7Hub(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-qm", "init")
	bare := filepath.Join(t.TempDir(), "r.git")
	runGit(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	runGit(t, dir, "remote", "add", "origin", bare)
	pid, wid := h.gitProject(dir)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a2\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("not selected\n"), 0o644)
	h.do("PUT", "/api/projects/"+pid+"/git/selection", map[string]any{"paths": []string{"a.txt"}}, nil)

	thread := func() string {
		_, m := h.do("GET", "/api/threads/"+pid+"/messages", nil, nil)
		b, _ := json.Marshal(m)
		return string(b)
	}
	st, r := h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": "commit with the message Tweak a", "workspaceId": wid, "planner": "off"}, nil)
	if st != 202 || r["confirmation"] != nil {
		t.Fatalf("command %d %v", st, r)
	}
	if !strings.Contains(fmt.Sprint(r["messages"]), "Tweak a") {
		t.Fatalf("messages %v", r["messages"])
	}
	if got := runGit(t, dir, "log", "-1", "--format=%s"); got != "Tweak a" {
		t.Fatalf("subject %q", got)
	}
	if got := runGit(t, dir, "show", "--name-only", "--format=", "HEAD"); got != "a.txt" {
		t.Fatalf("selection ignored: %q", got)
	}
	// commit and push: the rest (no selection left) with a heuristic
	// subject, then a push job that reports in the thread.
	h.do("PUT", "/api/projects/"+pid+"/git/selection", map[string]any{"paths": []string{}}, nil)
	st, r = h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": "commit and push", "workspaceId": wid, "planner": "off"}, nil)
	if st != 202 || !strings.Contains(fmt.Sprint(r["messages"]), "chore: add keep.txt") || !strings.Contains(fmt.Sprint(r["messages"]), "Pushing") {
		t.Fatalf("commit and push %d %v", st, r["messages"])
	}
	waitTrue(t, "push report", 30*time.Second, func() bool { return strings.Contains(thread(), "Push done.") })
	if runGit(t, bare, "log", "-1", "--format=%s", "main") != "chore: add keep.txt" {
		t.Fatal("not pushed")
	}
	// pull with nothing new.
	h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": "pull", "workspaceId": wid, "planner": "off"}, nil)
	waitTrue(t, "pull report", 30*time.Second, func() bool { return strings.Contains(thread(), "Pull done.") })
}

// TestGitMultiRepo: a project folder that only holds repositories: the scan,
// ?repo= on the git endpoints, and a command bar that wants a name.
func TestGitMultiRepo(t *testing.T) {
	h := newP7Hub(t)
	dir := t.TempDir()
	newRepo := func(rel string) string {
		d := filepath.Join(dir, rel)
		os.MkdirAll(d, 0o755)
		runGit(t, d, "init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(d, "a.txt"), []byte("a\n"), 0o644)
		runGit(t, d, "add", "-A")
		runGit(t, d, "commit", "-qm", "init")
		return d
	}
	a := newRepo("app-a")
	bare := filepath.Join(t.TempDir(), "r.git")
	runGit(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	runGit(t, a, "remote", "add", "origin", bare)
	pid, wid := h.gitProject(dir)
	base := "/api/projects/" + pid + "/git"
	sub := h.srv.events.Subscribe("git", "gitRepo")
	defer h.srv.events.Unsubscribe(sub)

	command := func(text string) string {
		st, r := h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": text, "workspaceId": wid, "planner": "off"}, nil)
		if st != 202 {
			t.Fatalf("%q: %d %v", text, st, r)
		}
		return fmt.Sprint(r["messages"])
	}

	// One repository below the folder: a plain command finds it.
	os.WriteFile(filepath.Join(a, "one.txt"), []byte("1\n"), 0o644)
	if m := command("commit with the message Only one"); !strings.Contains(m, "app-a: Committed") {
		t.Fatalf("single repo %s", m)
	}
	if got := runGit(t, a, "log", "-1", "--format=%s"); got != "Only one" {
		t.Fatalf("subject %q", got)
	}

	b := newRepo("group/app-b")
	st, l := h.do("GET", base+"/repos", nil, nil)
	if st != 200 || l["truncated"] != false || len(l["items"].([]any)) != 2 {
		t.Fatalf("repos %d %v", st, l)
	}
	it := l["items"].([]any)[1].(map[string]any)
	if it["path"] != "group/app-b" || it["name"] != "app-b" || it["branch"] != "main" || it["changed"] != float64(0) {
		t.Fatalf("item %v", it)
	}

	// No own repository, and none may be created around the two.
	if st, s := h.do("GET", base+"/status", nil, nil); st != 200 || s["isRepo"] != false {
		t.Fatalf("root status %d %v", st, s)
	}
	if st, s := h.do("POST", base+"/init", nil, nil); st != 409 || s["code"] != "nested_repos" {
		t.Fatalf("init %d %v", st, s)
	}

	// ?repo= reaches each endpoint family.
	os.WriteFile(filepath.Join(b, "b.txt"), []byte("b\n"), 0o644)
	q := "?repo=group%2Fapp-b"
	if st, s := h.do("GET", base+"/status"+q, nil, nil); st != 200 || s["isRepo"] != true || len(s["files"].([]any)) != 1 {
		t.Fatalf("status %d %v", st, s)
	}
	if st, d := h.do("GET", base+"/diff"+q+"&path=b.txt", nil, nil); st != 200 || !strings.Contains(d["diff"].(string), "+b") {
		t.Fatalf("diff %d %v", st, d)
	}
	st, c := h.do("POST", base+"/commit"+q, map[string]any{"message": "Add b", "all": true}, nil)
	if st != 200 || c["commit"].(map[string]any)["subject"] != "Add b" {
		t.Fatalf("commit %d %v", st, c)
	}
	if got := runGit(t, a, "log", "-1", "--format=%s"); got != "Only one" {
		t.Fatalf("other repository moved: %q", got)
	}
	if _, g := h.do("GET", base+"/log"+q+"&limit=5", nil, nil); len(g["items"].([]any)) != 2 {
		t.Fatalf("log %v", g)
	}
	if st, s := h.do("POST", base+"/branches"+q, map[string]any{"name": "feature/x"}, nil); st != 200 || s["branch"] != "feature/x" {
		t.Fatalf("branch %d %v", st, s)
	}
	if _, g := h.do("GET", "/api/projects/"+pid+"/github"+q, nil, nil); g == nil {
		t.Fatal("github status")
	}
	waitTrue(t, "gitRepo event", 5*time.Second, func() bool {
		select {
		case msg := <-sub.C:
			return strings.Contains(string(msg), `"type":"gitRepo"`) && strings.Contains(string(msg), `"repo":"group/app-b"`)
		default:
			return false
		}
	})

	// Bad selections.
	if st, s := h.do("GET", base+"/status?repo=..%2Fx", nil, nil); st != 400 || s["code"] != "bad_path" {
		t.Fatalf("escape %d %v", st, s)
	}
	if st, s := h.do("GET", base+"/status?repo=nope", nil, nil); st != 200 || s["isRepo"] != false {
		t.Fatalf("missing %d %v", st, s)
	}

	// Two repositories: the command bar wants a name and touches nothing.
	os.WriteFile(filepath.Join(a, "two.txt"), []byte("2\n"), 0o644)
	if m := command("commit and push"); !strings.Contains(m, "this project has 2 repositories (app-a, group/app-b)") {
		t.Fatalf("no name %s", m)
	}
	if got := runGit(t, a, "log", "-1", "--format=%s"); got != "Only one" {
		t.Fatalf("committed anyway: %q", got)
	}
	if m := command("repo nope push"); !strings.Contains(m, "no such repository") {
		t.Fatalf("wrong name %s", m)
	}
	if m := command("repo app-a commit and push"); !strings.Contains(m, "app-a: Committed") || !strings.Contains(m, "Pushing") {
		t.Fatalf("named %s", m)
	}
	waitTrue(t, "push report", 30*time.Second, func() bool {
		_, msgs := h.do("GET", "/api/threads/"+pid+"/messages", nil, nil)
		b, _ := json.Marshal(msgs)
		return strings.Contains(string(b), "app-a: Push done.")
	})
	if runGit(t, bare, "log", "-1", "--format=%s", "main") != "chore: add two.txt" {
		t.Fatal("not pushed")
	}
}

func TestPreviewAPI(t *testing.T) {
	h := newP7Hub(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Frame-Options", "DENY")
		io.WriteString(w, "<html><head></head><body>dev</body></html>")
	}))
	defer up.Close()
	port := up.Listener.Addr().(*net.TCPAddr).Port
	// The SPA CSP allows framing exactly the preview listeners.
	lo, hi := h.srv.gitPrev.previews.PortRange()
	_, _, hdr := h.doRaw("GET", "/", "", nil)
	csp := hdr.Get("Content-Security-Policy")
	host := mustURL(h.ts.URL).Hostname()
	if !strings.Contains(csp, fmt.Sprintf("frame-src 'self' http://%s:%d", host, lo)) || !strings.Contains(csp, fmt.Sprintf("http://%s:%d;", host, hi)) {
		t.Fatalf("csp %q", csp)
	}
	// Port probe.
	var probe []map[string]any
	h.getJSON(fmt.Sprintf("/api/machines/local/ports?probe=%d,1", port), &probe)
	if len(probe) != 2 || probe[1]["open"] != true || probe[1]["http"] != true || probe[0]["open"] != false {
		t.Fatalf("probe %v", probe)
	}
	if st, b := h.do("GET", "/api/machines/local/ports?probe=x", nil, nil); st != 400 || b["code"] != "bad_port" {
		t.Fatalf("bad probe %d %v", st, b)
	}
	// Hub's own port and the preview range are refused on This PC.
	if st, b := h.do("POST", "/api/previews", map[string]any{"machineId": "local", "port": lo}, nil); st != 400 || b["code"] != "bad_port" {
		t.Fatalf("range port %d %v", st, b)
	}
	if st, b := h.do("POST", "/api/previews", map[string]any{"machineId": "nope", "port": port}, nil); st != 404 {
		t.Fatalf("machine %d %v", st, b)
	}
	st, b := h.do("POST", "/api/previews", map[string]any{"machineId": "local", "port": port}, nil)
	if st != 201 {
		t.Fatalf("create %d %v", st, b)
	}
	pv := b["preview"].(map[string]any)
	lp := b["launchPath"].(string)
	listen := int(pv["listenPort"].(float64))
	if st, again := h.do("POST", "/api/previews", map[string]any{"machineId": "local", "port": port}, nil); st != 200 || again["preview"].(map[string]any)["id"] != pv["id"] {
		t.Fatalf("again %d %v", st, again)
	}
	// Browser side: launch → cookie → proxied page without frame blockers.
	jar, _ := cookiejar.New(nil)
	bc := &http.Client{Jar: jar}
	base := "http://127.0.0.1:" + strconv.Itoa(listen)
	resp, err := bc.Get(base + lp)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Frame-Options") != "" || !strings.Contains(string(body), `<script src="/__nb/bridge.js"></script>`) {
		t.Fatalf("proxied %d %v %s", resp.StatusCode, resp.Header, body)
	}
	// The hub API is not served on the preview origin, and the hub
	// session cookie does not reach it.
	resp, _ = bc.Get(base + "/__nb/bridge.js")
	js, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(js), fmt.Sprintf(`"http://127.0.0.1:%d"`, listenPort(h.srv.listen))) && h.srv.listen != "127.0.0.1:0" {
		t.Fatalf("bridge origins %s", js[:200])
	}
	// Logs and launch/delete endpoints.
	if st, l := h.do("GET", "/api/previews/"+pv["id"].(string)+"/logs", nil, nil); st != 200 || l["seq"].(float64) != 0 {
		t.Fatalf("logs %d %v", st, l)
	}
	if st, l := h.do("POST", "/api/previews/"+pv["id"].(string)+"/launch", map[string]any{"path": "/x"}, nil); st != 200 || !strings.Contains(l["launchPath"].(string), "to=%2Fx") {
		t.Fatalf("launch %d %v", st, l)
	}
	if list := h.list("/api/previews"); len(list) != 1 {
		t.Fatalf("list %v", list)
	}
	if st, _ := h.do("DELETE", "/api/previews/"+pv["id"].(string), nil, nil); st != 200 {
		t.Fatal("delete")
	}
	if st, d := h.do("DELETE", "/api/previews/"+pv["id"].(string), nil, nil); st != 404 || d["code"] != "preview_not_found" {
		t.Fatalf("delete twice %d %v", st, d)
	}
}

// TestAutoPreview: a console prints a localhost URL (with ANSI colour);
// the port answers → one preview, one web node right of the terminal and
// a "preview" edge; printing it again adds nothing.
// A site reaches the proxy only as an address on the tailnet or the internet: This PC and its
// network go to the browser. No test here dials out: public addresses have their own test.
func TestPreviewOfSiteAPI(t *testing.T) {
	h := newP7Hub(t)
	for url, code := range map[string]string{
		"http://127.0.0.1:1/":    "private_address",
		"http://192.168.1.1/":    "private_address",
		"http://169.254.169.254": "private_address",
		"ftp://100.64.10.20/":    "bad_url",
		"https://u:p@100.64.0.1": "bad_url",
	} {
		status, body, _ := h.doRaw("POST", "/api/previews", "application/json", []byte(`{"url":"`+url+`"}`))
		if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"`+code+`"`) {
			t.Errorf("%s: %d %s", url, status, body)
		}
	}
	var list []map[string]any
	h.getJSON("/api/previews", &list)
	if len(list) != 0 {
		t.Fatalf("a refused site opened a preview: %v", list)
	}
}

func TestAutoPreview(t *testing.T) {
	h := newP7Hub(t)
	up := httptest.NewServer(http.NotFoundHandler())
	defer up.Close()
	port := up.Listener.Addr().(*net.TCPAddr).Port
	pid, wid := h.gitProject(t.TempDir())
	_ = pid
	sub := h.srv.events.Subscribe("previewSuggested")
	defer h.srv.events.Unsubscribe(sub)
	st, in := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": wid}, nil)
	if st != 201 {
		t.Fatalf("session %d %v", st, in)
	}
	sid := in["id"].(string)
	line := fmt.Sprintf("printf '  \\033[32m➜\\033[0m  Local:   \\033[36mhttp://localhost:%%s/\\033[0m\\n' %d\r", port)
	time.Sleep(500 * time.Millisecond)
	h.do("POST", "/api/sessions/"+sid+"/input", map[string]any{"data": line}, nil)
	select {
	case msg := <-sub.C:
		if !strings.Contains(string(msg), fmt.Sprintf(`"url":"http://localhost:%d/"`, port)) || !strings.Contains(string(msg), sid) {
			t.Fatalf("event %s", msg)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no previewSuggested")
	}
	webNodes := func() (int, int) {
		var b map[string]any
		h.getJSON("/api/workspaces/"+wid+"/board", &b)
		n, e := 0, 0
		var webID, termID string
		for _, x := range b["nodes"].([]any) {
			nd := x.(map[string]any)
			switch nd["kind"] {
			case "web":
				n++
				webID = nd["id"].(string)
				d := nd["data"].(map[string]any)
				if d["auto"] != true || d["port"].(float64) != float64(port) || d["previewId"] == "" {
					t.Fatalf("web data %v", d)
				}
			case "terminal":
				termID = nd["id"].(string)
			}
		}
		for _, x := range b["edges"].([]any) {
			ed := x.(map[string]any)
			if ed["kind"] == "preview" && ed["source"] == termID && ed["target"] == webID {
				e++
			}
		}
		return n, e
	}
	if n, e := webNodes(); n != 1 || e != 1 {
		t.Fatalf("board web %d edges %d", n, e)
	}
	h.do("POST", "/api/sessions/"+sid+"/input", map[string]any{"data": line}, nil)
	time.Sleep(1500 * time.Millisecond)
	if n, _ := webNodes(); n != 1 {
		t.Fatalf("doubled: %d web nodes", n)
	}
	if list := h.list("/api/previews"); len(list) != 1 || list[0].(map[string]any)["auto"] != true {
		t.Fatalf("previews %v", list)
	}
	// A closed port gives no tile.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	h.srv.gitPrev.detect.Tries, h.srv.gitPrev.detect.Every = 2, 50*time.Millisecond
	h.do("POST", "/api/sessions/"+sid+"/input", map[string]any{"data": fmt.Sprintf("echo http://127.0.0.1:%d/\r", closed)}, nil)
	time.Sleep(1500 * time.Millisecond)
	h.srv.gitPrev.detect.Wait()
	if list := h.list("/api/previews"); len(list) != 1 {
		t.Fatalf("closed port made a preview: %v", list)
	}
	_ = url.Values{}
}

// What an agent changes can be looked at, undone or kept through the API, and each save leaves a
// version in the local history.
func TestReviewAPI(t *testing.T) {
	h := newP7Hub(t)
	dir := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one\n")
	write("b.txt", "bee\n")
	var pr struct {
		ID string `json:"id"`
	}
	st, body, _ := h.doRaw("POST", "/api/projects", "application/json", []byte(`{"name":"rv","path":"`+dir+`","machineId":"local"}`))
	if st >= 300 || json.Unmarshal(body, &pr) != nil || pr.ID == "" {
		t.Fatalf("project %d %s", st, body)
	}
	base := "/api/projects/" + pr.ID
	post := func(path, body string) (int, string) {
		st, b, _ := h.doRaw("POST", base+path, "application/json", []byte(body))
		return st, string(b)
	}
	wait := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 500; i++ {
			if ok() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("timed out: " + what)
	}
	if err := h.srv.review.Snapshot(pr.ID); err != nil {
		t.Fatal(err)
	}
	rv := h.srv.review
	rv.SessionChanged(pr.ID, "s_one", "@Ash", "claude", true, true)
	time.Sleep(300 * time.Millisecond)
	write("a.txt", "one\ntwo\n")
	write("b.txt", "bee!\n")
	rv.SessionChanged(pr.ID, "s_one", "", "", false, false)

	var sum struct {
		Supported bool `json:"supported"`
		Agents    []struct {
			SessionID string `json:"sessionId"`
			Name      string `json:"name"`
			Files     []struct {
				Path string `json:"path"`
				Add  int    `json:"add"`
				Del  int    `json:"del"`
				Root string `json:"root"`
				Base string `json:"base"`
			} `json:"files"`
		} `json:"agents"`
	}
	wait("the changes", func() bool {
		h.getJSON(base+"/review", &sum)
		return len(sum.Agents) == 1 && len(sum.Agents[0].Files) == 2
	})
	if !sum.Supported || sum.Agents[0].Name != "@Ash" || sum.Agents[0].Files[0].Path != "a.txt" || sum.Agents[0].Files[0].Add != 1 {
		t.Fatalf("summary %+v", sum)
	}
	if f := sum.Agents[0].Files[0]; f.Root != "" || f.Base != "" {
		t.Fatalf("the API shows internals: %+v", f)
	}
	var view map[string]any
	h.getJSON(base+"/review/file?session=s_one&path=a.txt", &view)
	if view["base"] != "one\n" || view["current"] != "one\ntwo\n" {
		t.Fatalf("view %v", view)
	}
	if st, b := post("/review/reject", `{"session":"s_one","path":"a.txt"}`); st != 200 {
		t.Fatalf("reject %d %s", st, b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "one\n" {
		t.Fatalf("a.txt %q", b)
	}
	if st, b := post("/review/accept", `{"session":"s_one"}`); st != 200 {
		t.Fatalf("accept all %d %s", st, b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.txt")); string(b) != "bee!\n" {
		t.Fatalf("b.txt %q", b)
	}
	if st, b := post("/review/accept", `{"session":"s_one"}`); st != 404 || !strings.Contains(b, "no_change") {
		t.Fatalf("nothing left %d %s", st, b)
	}

	// A save from the editor leaves a version, on its own.
	st, b, _ := h.doRaw("PUT", "/api/machines/local/fs/write", "application/json", []byte(`{"path":"`+filepath.Join(dir, "b.txt")+`","content":"saved\n","projectId":"`+pr.ID+`"}`))
	if st != 200 {
		t.Fatalf("write %d %s", st, b)
	}
	var hist struct {
		Items []struct {
			Commit string `json:"commit"`
			Who    string `json:"who"`
		} `json:"items"`
	}
	wait("the version", func() bool {
		h.getJSON(base+"/history?path=b.txt", &hist)
		return len(hist.Items) >= 3
	})
	var ver map[string]any
	h.getJSON(base+"/history/version?path=b.txt&commit="+hist.Items[1].Commit, &ver)
	if ver["content"] != "bee!\n" {
		t.Fatalf("version %v", ver)
	}
	if st, _, _ := h.doRaw("GET", base+"/history/version?path=b.txt&commit=HEAD", "", nil); st != 404 {
		t.Fatalf("bad commit %d", st)
	}
	if st, _, _ := h.doRaw("GET", base+"/history?path=../x", "", nil); st != 400 {
		t.Fatalf("bad path %d", st)
	}
}

// A task runs only by the name the project gives it, and its output comes back as a job.
func TestTasksAPI(t *testing.T) {
	h := newP7Hub(t)
	dir := t.TempDir()
	mk := "ok:\n\t@echo all good\nbad:\n\t@echo src/a.go:3:5: boom >&2\n\t@exit 3\nslow:\n\t@sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(mk), 0o644); err != nil {
		t.Fatal(err)
	}
	var pr struct {
		ID string `json:"id"`
	}
	st, body, _ := h.doRaw("POST", "/api/projects", "application/json", []byte(`{"name":"tk","path":"`+dir+`","machineId":"local"}`))
	if st >= 300 || json.Unmarshal(body, &pr) != nil {
		t.Fatalf("project %d %s", st, body)
	}
	base := "/api/projects/" + pr.ID + "/tasks"
	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Command string `json:"command"`
		} `json:"items"`
	}
	h.getJSON(base, &list)
	if len(list.Items) != 3 || list.Items[0].Command != "make ok" {
		t.Fatalf("list %+v", list)
	}
	run := func(id string) (int, map[string]any) {
		st, b, _ := h.doRaw("POST", base+"/run", "application/json", []byte(`{"id":"`+id+`"}`))
		var j map[string]any
		_ = json.Unmarshal(b, &j)
		return st, j
	}
	final := func(id string) map[string]any {
		t.Helper()
		var j map[string]any
		for range 400 {
			h.getJSON("/api/jobs/"+id, &j)
			if j["state"] != "queued" && j["state"] != "running" {
				return j
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("job %v", j)
		return nil
	}
	st, j := run("make:ok")
	if st != 202 {
		t.Fatalf("run %d %v", st, j)
	}
	if got := final(j["id"].(string)); got["state"] != "done" {
		t.Fatalf("ok %v", got)
	}
	_, j = run("make:bad")
	if got := final(j["id"].(string)); got["state"] != "failed" || got["code"] != "task_failed" || !strings.Contains(fmt.Sprint(got["error"]), "exit code") {
		t.Fatalf("bad %v", got)
	}
	// nothing but a named task runs
	if st, _ := run("rm -rf /"); st != 404 {
		t.Fatalf("arbitrary %d", st)
	}
	// cancelling stops it
	_, j = run("make:slow")
	id := j["id"].(string)
	time.Sleep(200 * time.Millisecond)
	if st, b, _ := h.doRaw("POST", "/api/jobs/"+id+"/cancel", "application/json", nil); st >= 300 {
		t.Fatalf("cancel %d %s", st, b)
	}
	if got := final(id); got["state"] != "cancelled" {
		t.Fatalf("cancel %v", got)
	}
}
