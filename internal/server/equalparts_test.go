package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/project"
)

// Equal parts (v0.9.0-beta.2, docs/API-board.md §1): no part is special, review and git are keyed
// <project>~<part> for every part, and old data is migrated once at start.

// TestEqualPartsReorder: moving another part to the front moves no review, Local History or git:
// a file's history keeps the versions from before and after the move, the part's git ref still
// reads its repository, gitPart comes for every part, and the old git event goes with the part
// that is first now.
func TestEqualPartsReorder(t *testing.T) {
	h := newP7Hub(t)
	api, web := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(api, "a.txt"), []byte("v0\n"), 0o644)
	os.WriteFile(filepath.Join(web, "index.html"), []byte("<p>shop</p>\n"), 0o644)
	runGit(t, api, "init", "-q")
	runGit(t, web, "init", "-q")
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": api, "name": "acme-shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid := p["id"].(string)
	apiID := p["parts"].([]any)[0].(map[string]any)["id"].(string)
	_, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Web", "machineId": "local", "path": web}, nil)
	webID := p["parts"].([]any)[1].(map[string]any)["id"].(string)
	apiKey, webKey := pid+"~"+apiID, pid+"~"+webID
	if got := h.srv.reviewKeyOf(pid, apiID); got != apiKey {
		t.Fatalf("key %s", got)
	}
	sub := h.srv.events.Subscribe("git", "gitRepo", "gitPart")
	defer h.srv.events.Unsubscribe(sub)
	events := func(want int) []string {
		t.Helper()
		var out []string
		deadline := time.After(10 * time.Second)
		for len(out) < want {
			select {
			case msg := <-sub.C:
				var e map[string]any
				_ = json.Unmarshal(msg, &e)
				out = append(out, fmt.Sprintf("%s:%v", e["type"], e["part"]))
			case <-deadline:
				t.Fatalf("events so far %v", out)
			}
		}
		select { // and nothing more
		case msg := <-sub.C:
			t.Fatalf("one event too many after %v: %s", out, msg)
		case <-time.After(300 * time.Millisecond):
		}
		return out
	}
	gitStatus := func(ref string) map[string]any {
		t.Helper()
		st, s := h.do("GET", "/api/projects/"+ref+"/git/status", nil, nil)
		if st != 200 {
			t.Fatalf("status %s %d %v", ref, st, s)
		}
		return s
	}
	gitStatus(apiKey)
	if got := strings.Join(events(2), " "); got != "gitPart:"+apiID+" git:<nil>" {
		t.Fatalf("first part events %s", got)
	}
	gitStatus(webKey)
	if got := strings.Join(events(1), " "); got != "gitPart:"+webID {
		t.Fatalf("second part events %s", got)
	}

	history := func(ref, rel string) []string {
		t.Helper()
		var hist struct {
			Items []struct {
				Commit string `json:"commit"`
			} `json:"items"`
		}
		h.getJSON("/api/projects/"+ref+"/history?path="+rel, &hist)
		var out []string
		for _, it := range hist.Items {
			var v map[string]any
			h.getJSON("/api/projects/"+ref+"/history/version?path="+rel+"&commit="+it.Commit, &v)
			out = append(out, strings.TrimSpace(fmt.Sprint(v["content"])))
		}
		return out
	}
	save := func(content string) {
		t.Helper()
		if st, b := h.do("PUT", "/api/machines/local/fs/write", map[string]any{"path": filepath.Join(api, "a.txt"), "content": content, "projectId": apiKey}, nil); st != 200 {
			t.Fatalf("write %d %v", st, b)
		}
	}
	if err := h.srv.review.Snapshot(apiKey); err != nil {
		t.Fatal(err)
	}
	save("v1\n")
	waitTrue(t, "the first version", 10*time.Second, func() bool { return strings.Contains(strings.Join(history(apiKey, "a.txt"), ","), "v1") })

	// Web goes to the front: the project's machineId and path follow it, nothing else moves
	if st, b := h.do("PATCH", "/api/projects/"+pid+"/parts/"+webID, map[string]any{"index": 0}, nil); st != 200 || b["path"] != web {
		t.Fatalf("reorder %d %v", st, b)
	}
	save("v2\n")
	waitTrue(t, "the version after the move", 10*time.Second, func() bool {
		got := strings.Join(history(apiKey, "a.txt"), ",")
		return strings.Contains(got, "v2") && strings.Contains(got, "v1")
	})
	if _, err := os.Stat(h.srv.st.Path("review", pid)); !os.IsNotExist(err) {
		t.Fatalf("a review under the bare project id: %v", err)
	}
	if _, err := os.Stat(h.srv.st.Path("review", apiKey)); err != nil {
		t.Fatalf("the part's review folder: %v", err)
	}
	// a ref without a part is now Web, the first in display order (compat); the keys stay
	if got := h.srv.reviewKeyOf(pid, ""); got != webKey {
		t.Fatalf("default key after the move %s", got)
	}
	if got := h.srv.reviewKeyAt(pid, "local", filepath.Join(api, "a.txt")); got != apiKey {
		t.Fatalf("a file of the old first part %s", got)
	}

	// git: the part's ref still reads its folder; the old event now comes with Web
	os.WriteFile(filepath.Join(api, "new.txt"), []byte("x\n"), 0o644)
	if s := gitStatus(apiKey); s["isRepo"] != true || len(s["files"].([]any)) != 2 {
		t.Fatalf("api git after the move %v", s)
	}
	if got := strings.Join(events(1), " "); got != "gitPart:"+apiID {
		t.Fatalf("events of the part that moved back %s", got)
	}
	os.WriteFile(filepath.Join(web, "new.txt"), []byte("x\n"), 0o644)
	gitStatus(pid) // the bare ref: the first part, Web
	if got := strings.Join(events(2), " "); got != "gitPart:"+webID+" git:<nil>" {
		t.Fatalf("events of the new first part %s", got)
	}
}

// TestRemoveFirstPart: the first part goes like any other, its review with it, and the project's
// machineId and path follow the part that is first now; a session from before part ids, stamped
// at start, keeps its part busy; the last part stays; split works on the first part and keeps its
// id and review.
func TestRemoveFirstPart(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	api, web, docs := t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(api, "a.txt"), []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(web, "b.txt"), []byte("b\n"), 0o644)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": api, "name": "acme-shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	apiID := p["parts"].([]any)[0].(map[string]any)["id"].(string)
	_, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Web", "machineId": "local", "path": web}, nil)
	webID := p["parts"].([]any)[1].(map[string]any)["id"].(string)
	h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Docs", "machineId": "local", "path": docs}, nil)
	for _, k := range []string{pid + "~" + apiID, pid + "~" + webID} {
		if err := h.srv.review.Snapshot(k); err != nil {
			t.Fatal(err)
		}
	}

	_, mon := h.do("POST", "/api/projects/"+pid+"/uptime", map[string]any{"name": "api", "type": "http", "url": "http://127.0.0.1:9/", "partId": apiID, "active": false}, nil)
	if mon["partId"] != apiID {
		t.Fatalf("monitor %v", mon)
	}

	// a console outside every part's folder is in no part, and holds none
	st, sh := h.do("POST", "/api/sessions", map[string]any{"preset": "shell", "workspaceId": wid, "cwd": t.TempDir()}, nil)
	if st != 201 || sh["partId"] != nil {
		t.Fatalf("shell outside the parts %d %v", st, sh)
	}
	if st, b := h.do("DELETE", "/api/projects/"+pid+"/parts/"+apiID, nil, nil); st != 200 || b["path"] != web {
		t.Fatalf("delete the first part %d %v", st, b)
	}
	pr, _ := h.srv.projects.Get(pid)
	if len(pr.Parts) != 2 || pr.Parts[0].ID != webID || pr.MachineID != "local" || pr.Path != web {
		t.Fatalf("after deleting the first part %+v", pr)
	}
	if _, err := os.Stat(h.srv.st.Path("review", pid+"~"+apiID)); !os.IsNotExist(err) {
		t.Fatalf("the removed part's review stays: %v", err)
	}
	if _, err := os.Stat(h.srv.st.Path("review", pid+"~"+webID)); err != nil {
		t.Fatalf("the other part's review went: %v", err)
	}
	if _, ups := h.do("GET", "/api/projects/"+pid+"/uptime", nil, nil); ups["items"].([]any)[0].(map[string]any)["partId"] != nil {
		t.Fatalf("the monitor keeps the removed part %v", ups)
	}
	// as if it were from before part ids: the start-up migration gives it what "" meant then,
	// the first part (Web now), and then it holds that part
	if n := h.srv.sessions.FillParts(h.srv.partOfOldSession); n != 1 {
		t.Fatalf("stamped %d", n)
	}
	if in, _ := h.srv.sessions.Info(sh["id"].(string)); in.PartID != webID {
		t.Fatalf("stamped part %q", in.PartID)
	}
	if st, b := h.do("DELETE", "/api/projects/"+pid+"/parts/"+webID, nil, nil); st != 409 || b["code"] != "part_busy" || b["sessions"] != float64(1) {
		t.Fatalf("a stamped old session %d %v", st, b)
	}
	h.do("DELETE", "/api/sessions/"+sh["id"].(string), nil, nil)

	// split the first part: the new project keeps its id, name and review
	st, sp := h.do("POST", "/api/projects/"+pid+"/parts/"+webID+"/split", nil, nil)
	if st != 201 {
		t.Fatalf("split %d %v", st, sp)
	}
	np, from := sp["project"].(map[string]any), sp["from"].(map[string]any)
	npID := np["id"].(string)
	if pt := np["parts"].([]any)[0].(map[string]any); pt["id"] != webID || pt["name"] != "Web" || np["path"] != web {
		t.Fatalf("split off %v", np)
	}
	if from["path"] != docs || len(from["parts"].([]any)) != 1 {
		t.Fatalf("left %v", from)
	}
	if _, err := os.Stat(h.srv.st.Path("review", npID+"~"+webID)); err != nil {
		t.Fatalf("the split part's review did not move: %v", err)
	}
	if st, b := h.do("DELETE", "/api/projects/"+pid+"/parts/"+from["parts"].([]any)[0].(map[string]any)["id"].(string), nil, nil); st != 409 || b["code"] != "last_part" {
		t.Fatalf("the last part %d %v", st, b)
	}
}

// TestOldPartsAtStart: a data folder from before equal parts, opened twice. The first start moves
// the first part's review to its part key, stamps the part of a session that had none, makes an
// unsafe part name safe and says so in the project's thread, and names a one-part project's
// "Main" after its folder; the second start changes nothing and says nothing again.
func TestOldPartsAtStart(t *testing.T) {
	dir := hubDataDir(t)
	api, web, solo := t.TempDir(), t.TempDir(), t.TempDir()
	projects := fmt.Sprintf(`[
{"id":"p_acmeshop","name":"acme-shop","machineId":"local","path":%q,"parts":[
 {"id":"main","name":"Main","machineId":"local","path":%q},
 {"id":"pt_web","name":"web/ui","machineId":"local","path":%q}],"workspaces":[{"id":"w_shop","name":"Main"}]},
{"id":"p_solo001","name":"Solo","machineId":"local","path":%q,"parts":[{"id":"main","name":"Main","machineId":"local","path":%q}],"workspaces":[{"id":"w_solo","name":"Main"}]}
]`, api, api, web, solo, solo)
	sessions := fmt.Sprintf(`[
{"id":"s_inweb","projectId":"p_acmeshop","workspaceId":"w_shop","machineId":"local","cwd":%q,"preset":"shell","agent":"shell","handle":"Ayu"},
{"id":"s_home","projectId":"p_acmeshop","workspaceId":"w_shop","machineId":"local","cwd":"/","preset":"shell","agent":"shell","handle":"Bayu"}
]`, filepath.Join(web, "src"))
	for name, body := range map[string]string{project.File: projects, "sessions.json": sessions, "review/p_acmeshop/state.json": `{"first":true}`} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	notes := func(s *Server) int {
		n := 0
		for _, m := range s.orch.Threads.Last("p_acmeshop", 50) {
			if m.Kind == "note" && strings.Contains(m.Text, `"web/ui" is now "web-ui"`) {
				n++
			}
		}
		return n
	}
	check := func(s *Server) {
		t.Helper()
		shop, _ := s.projects.Get("p_acmeshop")
		one, _ := s.projects.Get("p_solo001")
		if shop.Parts[0].ID != "main" || shop.Parts[0].Name != "Main" || shop.Parts[1].ID != "pt_web" || shop.Parts[1].Name != "web-ui" {
			t.Fatalf("parts %+v", shop.Parts)
		}
		if one.Parts[0].ID != "main" || one.Parts[0].Name != filepath.Base(solo) {
			t.Fatalf("one part %+v", one.Parts)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "review", "p_acmeshop~main", "state.json")); err != nil || string(b) != `{"first":true}` {
			t.Fatalf("review moved %q %v", b, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "review", "p_acmeshop")); !os.IsNotExist(err) {
			t.Fatalf("the bare review folder stays: %v", err)
		}
		inWeb, _ := s.sessions.Info("s_inweb")
		home, _ := s.sessions.Info("s_home")
		if inWeb.PartID != "pt_web" || home.PartID != "main" {
			t.Fatalf("stamped %q %q", inWeb.PartID, home.PartID)
		}
		if n := notes(s); n != 1 {
			t.Fatalf("%d notes about the rename", n)
		}
	}
	start := func() *Server {
		t.Helper()
		s, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.review.Close)
		return s
	}
	s1 := start()
	check(s1)
	saved, _ := os.ReadFile(filepath.Join(dir, project.File))
	s1.shutdown()

	s2 := start()
	defer s2.shutdown()
	check(s2)
	if now, _ := os.ReadFile(filepath.Join(dir, project.File)); string(now) != string(saved) {
		t.Fatalf("the second start changed projects.json:\n%s\n%s", saved, now)
	}
}
