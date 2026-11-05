package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestProjectGroupsAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()

	st, top := h.do("POST", "/api/project-groups", map[string]any{"name": "Clients"}, nil)
	if st != http.StatusCreated || top["id"] == "" {
		t.Fatalf("%d %v", st, top)
	}
	st, sub := h.do("POST", "/api/project-groups", map[string]any{"name": "Globex", "parentId": top["id"]}, nil)
	if st != http.StatusCreated || sub["parentId"] != top["id"] {
		t.Fatalf("%d %v", st, sub)
	}
	if st, b := h.do("POST", "/api/project-groups", map[string]any{"name": "globex", "parentId": top["id"]}, nil); st != http.StatusConflict || b["code"] != "name_conflict" {
		t.Fatalf("%d %v", st, b)
	}
	if st, b := h.do("POST", "/api/project-groups", map[string]any{"name": "x", "parentId": "g_none"}, nil); st != http.StatusNotFound || b["code"] != "group_not_found" {
		t.Fatalf("%d %v", st, b)
	}
	if st, b := h.do("PATCH", "/api/project-groups/"+top["id"].(string), map[string]any{"parentId": sub["id"]}, nil); st != http.StatusBadRequest || b["code"] != "group_cycle" {
		t.Fatalf("%d %v", st, b)
	}
	if st, b := h.do("PATCH", "/api/project-groups/"+sub["id"].(string), map[string]any{"name": "Gen"}, nil); st != http.StatusOK || b["name"] != "Gen" {
		t.Fatalf("%d %v", st, b)
	}

	dir := t.TempDir()
	st, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": dir, "name": "Shop"}, nil)
	if st != http.StatusCreated {
		t.Fatalf("%d %v", st, pr)
	}
	id := pr["id"].(string)
	if _, has := pr["groupId"]; has {
		t.Fatalf("a new project has no group: %v", pr)
	}
	if st, b := h.do("PATCH", "/api/projects/"+id, map[string]any{"groupId": "g_none"}, nil); st != http.StatusNotFound || b["code"] != "group_not_found" {
		t.Fatalf("%d %v", st, b)
	}
	if st, b := h.do("PATCH", "/api/projects/"+id, map[string]any{"tags": []string{"a,b"}}, nil); st != http.StatusBadRequest || b["code"] != "bad_tag" {
		t.Fatalf("%d %v", st, b)
	}
	st, pr = h.do("PATCH", "/api/projects/"+id, map[string]any{"groupId": sub["id"], "tags": []string{"go", "Go", "web"}}, nil)
	if st != http.StatusOK || pr["groupId"] != sub["id"] {
		t.Fatalf("%d %v", st, pr)
	}
	if tags := pr["tags"].([]any); len(tags) != 2 || tags[0] != "go" || tags[1] != "web" {
		t.Fatalf("%v", tags)
	}
	// name alone leaves them
	st, pr = h.do("PATCH", "/api/projects/"+id, map[string]any{"name": "Shop 2"}, nil)
	if st != http.StatusOK || pr["groupId"] != sub["id"] || pr["tags"] == nil {
		t.Fatalf("%d %v", st, pr)
	}

	var groups []map[string]any
	if st := h.getJSON("/api/project-groups", &groups); st != http.StatusOK || len(groups) != 2 {
		t.Fatalf("%d %v", st, groups)
	}

	// Removing the group keeps the project: it moves up.
	if st, b := h.do("DELETE", "/api/project-groups/"+sub["id"].(string), nil, nil); st != http.StatusOK {
		t.Fatalf("%d %v", st, b)
	}
	// the hub may hold a project of its own, so this one is looked up
	current := func() map[string]any {
		t.Helper()
		var list []map[string]any
		if st := h.getJSON("/api/projects", &list); st != http.StatusOK {
			t.Fatalf("%d %v", st, list)
		}
		for _, x := range list {
			if x["id"] == id {
				return x
			}
		}
		t.Fatalf("project gone: %v", list)
		return nil
	}
	if got := current(); got["groupId"] != top["id"] {
		t.Fatalf("%v, want group %v", got, top["id"])
	}
	if st, b := h.do("DELETE", "/api/project-groups/"+top["id"].(string), nil, nil); st != http.StatusOK {
		t.Fatalf("%d %v", st, b)
	}
	if got := current(); got["groupId"] != nil {
		t.Fatalf("no group is left: %v", got)
	}
	if st, b := h.do("DELETE", "/api/project-groups/"+top["id"].(string), nil, nil); st != http.StatusNotFound || b["code"] != "group_not_found" {
		t.Fatalf("%d %v", st, b)
	}
}

func TestProjectGroupsEvents(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, g := h.do("POST", "/api/project-groups", map[string]any{"name": "First"}, nil)
	if st != http.StatusCreated {
		t.Fatalf("%d %v", st, g)
	}
	hdr := http.Header{}
	for _, c := range h.c.Jar.Cookies(mustURL(h.ts.URL)) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	ws, _, err := h.dialWS("/api/events", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var snap map[string]any
	if err := ws.ReadJSON(&snap); err != nil {
		t.Fatal(err)
	}
	if list, _ := snap["projectGroups"].([]any); snap["type"] != "snapshot" || len(list) != 1 {
		t.Fatalf("%v", snap)
	}
	if st, b := h.do("POST", "/api/project-groups", map[string]any{"name": "Second"}, nil); st != http.StatusCreated {
		t.Fatalf("%d %v", st, b)
	}
	for {
		var ev map[string]json.RawMessage
		if err := ws.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		if string(ev["type"]) != `"projectGroups"` {
			continue
		}
		var groups []map[string]any
		if err := json.Unmarshal(ev["groups"], &groups); err != nil || len(groups) != 2 {
			t.Fatalf("%s %v", ev["groups"], err)
		}
		return
	}
}
