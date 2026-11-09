package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Review and Local History of a project on a real sshd: the copy comes over SSH, the undo goes
// back over it, and what the agent's own tools change is attributed without a look at the rest.
func TestReviewOnAServer(t *testing.T) {
	port, key := startSSHServer(t)
	h := newTestHub(t)
	h.setup()
	mid := h.connectMachine(port, key)
	dir := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	write("app/User.php", "<?php // before\n")
	write(".env", "APP_KEY=CANARY-server-secret\n")
	write("-odd name.php", "<?php // odd\n")
	st, pr := h.do("POST", "/api/projects", map[string]any{"name": "srv", "machineId": mid, "path": dir}, nil)
	if st != 201 {
		t.Fatalf("project: %d %v", st, pr)
	}
	pid := pr["id"].(string)
	if st, res := h.do("GET", "/api/projects/"+pid+"/review", nil, nil); st != 200 || res["supported"] != true {
		t.Fatalf("review: %d %v", st, res)
	}

	const sess = "s_agent"
	h.srv.review.SessionChanged(pid, sess, "@agent", "claude", true, false)
	waitTrue(t, "the first copy", 30*time.Second, func() bool {
		m, _ := filepath.Glob(filepath.Join(h.srv.st.Path("review"), pid, "mirror", "*", "app", "User.php"))
		return len(m) == 1
	})
	// give the turn-start snapshot time to record the agent
	time.Sleep(500 * time.Millisecond)

	// the agent's file tool, through the same call the MCP server makes
	h.srv.noteRemoteChange(remoteTarget{machineID: mid, cwd: dir, projectID: pid, sessionID: sess}, "write_file", json.RawMessage(`{"path":"app/User.php"}`))
	write("app/User.php", "<?php // after\n")
	write("-odd name.php", "<?php // changed\n")

	var files []any
	waitTrue(t, "the change to show", 30*time.Second, func() bool {
		_, res := h.do("GET", "/api/projects/"+pid+"/review", nil, nil)
		agents, _ := res["agents"].([]any)
		if len(agents) != 1 {
			return false
		}
		files, _ = agents[0].(map[string]any)["files"].([]any)
		return len(files) == 2
	})
	q := "session=" + sess + "&path=" + url.QueryEscape("app/User.php")
	st, view := h.do("GET", "/api/projects/"+pid+"/review/file?"+q, nil, nil)
	if st != 200 || view["base"] != "<?php // before\n" || view["current"] != "<?php // after\n" {
		t.Fatalf("file: %d %v", st, view)
	}

	if st, res := h.do("POST", "/api/projects/"+pid+"/review/reject", map[string]any{"session": sess, "path": "app/User.php"}, nil); st != http.StatusOK {
		t.Fatalf("reject: %d %v", st, res)
	}
	if got := read("app/User.php"); got != "<?php // before\n" {
		t.Fatalf("server holds %q", got)
	}
	// A dash at the front stays a file name, on the way there and back.
	if st, res := h.do("POST", "/api/projects/"+pid+"/review/reject", map[string]any{"session": sess, "path": "-odd name.php"}, nil); st != http.StatusOK {
		t.Fatalf("reject odd: %d %v", st, res)
	}
	if got := read("-odd name.php"); got != "<?php // odd\n" {
		t.Fatalf("server holds %q", got)
	}
	h.srv.review.SessionChanged(pid, sess, "", "", false, false)

	// Local History over the same copy.
	write("app/User.php", "<?php // third\n")
	st, hist := h.do("GET", "/api/projects/"+pid+"/history?path="+url.QueryEscape("app/User.php"), nil, nil)
	if st != 200 {
		t.Fatalf("history: %d %v", st, hist)
	}
	items := hist["items"].([]any)
	if len(items) < 2 {
		t.Fatalf("history: %v", items)
	}
	commit := items[len(items)-1].(map[string]any)["commit"].(string)
	st, ver := h.do("GET", "/api/projects/"+pid+"/history/version?path="+url.QueryEscape("app/User.php")+"&commit="+commit, nil, nil)
	if st != 200 || !strings.Contains(ver["content"].(string), "before") {
		t.Fatalf("version: %d %v", st, ver)
	}

	// The server's secret never reached This PC.
	_ = filepath.WalkDir(h.srv.st.Path("review"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "CANARY-server-secret") {
				t.Errorf("copied to %s", p)
			}
		}
		return nil
	})
	// And the server got nothing it did not have.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch e.Name() {
		case "app", ".env", "-odd name.php":
		default:
			t.Errorf("left on the server: %s", e.Name())
		}
	}
}
