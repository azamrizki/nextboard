package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/azamrizki/nextboard/internal/orch"
)

// TestLinkedGroupInput: with a group's input linked, keys typed in one console
// reach the others in order, without what the terminal emulator sent itself.
func TestLinkedGroupInput(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	_, wid := h.homeWorkspace()
	dir := t.TempDir()
	shell := func() string {
		st, in := h.do("POST", "/api/sessions", map[string]any{"machineId": "local", "preset": "shell", "workspaceId": wid, "cwd": dir}, nil)
		if st != 201 {
			t.Fatalf("session %d %v", st, in)
		}
		return in["id"].(string)
	}
	a, b := shell(), shell()
	gname, err := h.srv.orch.CreateGroup(wid, []string{a, b}, "Team", "")
	if err != nil || gname != "Team" {
		t.Fatalf("group %q %v", gname, err)
	}
	bd, err := h.srv.boards.Get(wid)
	if err != nil {
		t.Fatal(err)
	}
	gid := ""
	for _, n := range bd.Nodes {
		if n.Kind == orch.KindGroup {
			gid = n.ID
		}
	}
	hdr := http.Header{"Origin": {h.ts.URL}}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	ws, _, err := h.dialWS("/api/sessions/"+a+"/ws", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	typeIn := func(s string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"t": "i", "d": s})
		if err := ws.WriteMessage(websocket.TextMessage, raw); err != nil {
			t.Fatal(err)
		}
	}
	read := func(pattern string) string {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		var all []string
		for _, m := range matches {
			c, _ := os.ReadFile(m)
			all = append(all, string(c))
		}
		return strings.Join(all, "\n")
	}
	count := func(pattern string) int {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		return len(matches)
	}
	waitTrue(t, "shell ready", 20*time.Second, func() bool {
		typeIn("touch solo-$$\r")
		time.Sleep(200 * time.Millisecond)
		return count("solo-*") > 0
	})
	if count("solo-*") != 1 {
		t.Fatalf("input leaked while not linked: %d files", count("solo-*"))
	}
	if st, r := h.ops(wid, map[string]any{"op": "patch", "id": gid, "set": map[string]any{"data": map[string]any{"syncInput": true}}}); st != 200 {
		t.Fatalf("link %d %v", st, r)
	}
	typeIn("cat -v > linked-$$\r")
	waitTrue(t, "both consoles run the command", 20*time.Second, func() bool { return count("linked-*") == 2 })
	time.Sleep(300 * time.Millisecond) // cat is reading in both
	typeIn("\x1b[I")
	typeIn("one\r")
	typeIn("two\r\x04")
	waitTrue(t, "both consoles got the lines", 20*time.Second, func() bool {
		return strings.Count(read("linked-*"), "one\n") == 2 && strings.Count(read("linked-*"), "two\n") == 2
	})
	got := read("linked-*")
	if strings.Count(got, "^[[I") != 1 {
		t.Fatalf("the focus report belongs to the console it came from:\n%s", got)
	}
	for _, f := range []string{"one", "two"} {
		if !strings.Contains(got, f) {
			t.Fatalf("missing %s:\n%s", f, got)
		}
	}
	if i, j := strings.Index(got, "one"), strings.Index(got, "two"); i > j {
		t.Fatalf("order lost:\n%s", got)
	}
	// Unlinked again: back to one console.
	if st, r := h.ops(wid, map[string]any{"op": "patch", "id": gid, "set": map[string]any{"data": map[string]any{"syncInput": false}}}); st != 200 {
		t.Fatalf("unlink %d %v", st, r)
	}
	typeIn("touch after-$$\r")
	waitTrue(t, "typed console runs it", 20*time.Second, func() bool { return count("after-*") > 0 })
	time.Sleep(500 * time.Millisecond)
	if count("after-*") != 1 {
		t.Fatalf("input leaked after unlinking: %d files", count("after-*"))
	}
}
