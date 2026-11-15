package server

import (
	"strings"
	"testing"
)

// TestOrchPlacementAPI: spawn and deploy place consoles at the project root or in a part through
// the same rule as POST /api/sessions (v0.9.0-beta.2, docs/API-orchestration.md §5, §13).
func TestOrchPlacementAPI(t *testing.T) {
	h := newTestHub(t)
	pid, wid, own := orchSetup(t, h)
	web := t.TempDir()
	st, pr := h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Web", "machineId": "local", "path": web}, nil)
	if st != 201 {
		t.Fatalf("part %d %v", st, pr)
	}
	parts := pr["parts"].([]any)
	firstID, webID := parts[0].(map[string]any)["id"].(string), parts[1].(map[string]any)["id"].(string)
	sessionsOf := func(res map[string]any) []map[string]any {
		var out []map[string]any
		for _, x := range res["sessions"].([]any) {
			out = append(out, x.(map[string]any))
		}
		return out
	}

	// a part, by name in any case: its id and its folder
	st, res := h.do("POST", "/api/workspaces/"+wid+"/spawn", map[string]any{"count": 1, "agent": "shell", "part": "web"}, nil)
	if st != 201 {
		t.Fatalf("spawn in a part %d %v", st, res)
	}
	if in := sessionsOf(res)[0]; in["partId"] != webID || in["cwd"] != web || in["root"] == true {
		t.Fatalf("in a part %v", in)
	}
	// the root: This PC, the hub's root folder, no part
	st, res = h.do("POST", "/api/workspaces/"+wid+"/spawn", map[string]any{"slots": []any{map[string]any{"agent": "shell", "root": true}}}, nil)
	if st != 201 {
		t.Fatalf("spawn at the root %d %v", st, res)
	}
	if in := sessionsOf(res)[0]; in["root"] != true || in["partId"] != "" && in["partId"] != nil || in["machineId"] != "local" || in["cwd"] == own || in["cwd"] == web {
		t.Fatalf("at the root %v", in)
	}
	for _, c := range []struct {
		body map[string]any
		want int
		code string
	}{
		{map[string]any{"count": 1, "agent": "shell", "root": true, "part": "Web"}, 400, "bad_placement"},
		{map[string]any{"count": 1, "agent": "shell", "part": "Gateway"}, 404, "part_not_found"},
	} {
		if st, b := h.do("POST", "/api/workspaces/"+wid+"/spawn", c.body, nil); st != c.want || b["code"] != c.code {
			t.Fatalf("%v: %d %v", c.body, st, b)
		}
	}

	// deploy: places per group; a lane override on This PC with no placement is in the first part
	st, dep := h.do("POST", "/api/workspaces/"+wid+"/deploy", map[string]any{"layoutId": "builtin:duo", "fill": map[string]any{"agent": "shell"},
		"places": map[string]any{"g1": map[string]any{"part": "Web"}}}, nil)
	if st != 201 {
		t.Fatalf("deploy %d %v", st, dep)
	}
	for _, in := range sessionsOf(dep) {
		if in["partId"] != webID {
			t.Fatalf("deployed in Web %v", in)
		}
	}
	st, dep = h.do("POST", "/api/workspaces/"+wid+"/deploy", map[string]any{"layoutId": "builtin:duo", "fill": map[string]any{"agent": "shell"},
		"laneOverrides": map[string]any{"a1": map[string]any{"machineId": "local"}, "a2": map[string]any{"root": true}}}, nil)
	if st != 201 {
		t.Fatalf("deploy %d %v", st, dep)
	}
	if got := sessionsOf(dep); got[0]["partId"] != firstID || got[0]["cwd"] != own || got[1]["root"] != true {
		t.Fatalf("lane overrides %v", got)
	}

	// a layout saved here keeps the part by name; a project without that part is asked
	st, ly := h.do("POST", "/api/layouts", map[string]any{"workspaceId": wid}, nil)
	if st != 201 {
		t.Fatalf("save %d %v", st, ly)
	}
	named := map[string]int{}
	for _, x := range ly["lanes"].([]any) {
		ln := x.(map[string]any)
		if ln["root"] == true {
			named["(root)"]++
		} else if p, _ := ln["part"].(string); p != "" {
			named[p]++
		}
	}
	if named["Web"] != 3 || named["(root)"] != 2 {
		t.Fatalf("saved places %v (%v)", named, ly["lanes"])
	}
	other := t.TempDir()
	st, op := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": other, "name": "acme-other"}, nil)
	if st != 201 {
		t.Fatalf("other %d %v", st, op)
	}
	opid, owid := op["id"].(string), op["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	if st, b := h.do("POST", "/api/projects/"+opid+"/parts", map[string]any{"name": "Docs", "machineId": "local", "path": t.TempDir()}, nil); st != 201 {
		t.Fatalf("other part %d %v", st, b)
	}
	st, b := h.do("POST", "/api/workspaces/"+owid+"/deploy", map[string]any{"layoutId": ly["id"], "fill": map[string]any{"agent": "shell"}}, nil)
	if st != 409 || b["code"] != "place_unmatched" || !strings.Contains(b["error"].(string), "Web") {
		t.Fatalf("unmatched %d %v", st, b)
	}
	if u, _ := b["unmatched"].([]any); len(u) == 0 || u[0].(map[string]any)["part"] != "Web" {
		t.Fatalf("unmatched list %v", b)
	}
}
