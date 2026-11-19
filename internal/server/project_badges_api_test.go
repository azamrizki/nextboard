package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestProjectBadgesAPI(t *testing.T) {
	h := newTestHub(t)
	h.setup()

	var list []map[string]any
	if st := h.getJSON("/api/project-badges", &list); st != http.StatusOK || list == nil || len(list) != 0 {
		t.Fatalf("%d %v: an empty list, not null", st, list)
	}
	st, prod := h.do("POST", "/api/project-badges", map[string]any{"name": " PROD ", "color": "red", "icon": "shield"}, nil)
	if st != http.StatusCreated || prod["name"] != "PROD" || prod["color"] != "red" || prod["icon"] != "shield" || prod["createdAt"] == nil {
		t.Fatalf("%d %v", st, prod)
	}
	st, dev := h.do("POST", "/api/project-badges", map[string]any{"name": "dev", "color": "green"}, nil)
	if _, has := dev["icon"]; st != http.StatusCreated || has {
		t.Fatalf("%d %v", st, dev)
	}
	for _, c := range []struct {
		body map[string]any
		st   int
		code string
	}{
		{map[string]any{"name": "prod", "color": "blue"}, http.StatusConflict, "name_conflict"},
		{map[string]any{"name": "", "color": "blue"}, http.StatusBadRequest, "bad_badge_name"},
		{map[string]any{"name": "much too long!", "color": "blue"}, http.StatusBadRequest, "bad_badge_name"},
		{map[string]any{"name": "x", "color": "black"}, http.StatusBadRequest, "bad_badge_color"},
		{map[string]any{"name": "x", "color": "blue", "icon": "no-dash"}, http.StatusBadRequest, "bad_badge_icon"},
	} {
		if st, b := h.do("POST", "/api/project-badges", c.body, nil); st != c.st || b["code"] != c.code {
			t.Errorf("%v: %d %v", c.body, st, b)
		}
	}
	if st, b := h.do("PATCH", "/api/project-badges/"+dev["id"].(string), map[string]any{"name": "Prod"}, nil); st != http.StatusConflict || b["code"] != "name_conflict" {
		t.Fatalf("%d %v", st, b)
	}
	st, b := h.do("PATCH", "/api/project-badges/"+dev["id"].(string), map[string]any{"name": "Dev", "icon": "wrench"}, nil)
	if st != http.StatusOK || b["name"] != "Dev" || b["color"] != "green" || b["icon"] != "wrench" {
		t.Fatalf("%d %v", st, b)
	}
	if st, b := h.do("PATCH", "/api/project-badges/b_none", map[string]any{"color": "red"}, nil); st != http.StatusNotFound || b["code"] != "badge_not_found" {
		t.Fatalf("%d %v", st, b)
	}
	if st := h.getJSON("/api/project-badges", &list); st != http.StatusOK || len(list) != 2 || list[0]["name"] != "Dev" || list[1]["name"] != "PROD" {
		t.Fatalf("%d %v", st, list)
	}

	st, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "Shop"}, nil)
	if st != http.StatusCreated {
		t.Fatalf("%d %v", st, pr)
	}
	id := pr["id"].(string)
	if _, has := pr["badgeId"]; has {
		t.Fatalf("a new project has no badge: %v", pr)
	}
	if st, b := h.do("PATCH", "/api/projects/"+id, map[string]any{"badgeId": "b_none"}, nil); st != http.StatusNotFound || b["code"] != "badge_not_found" {
		t.Fatalf("%d %v", st, b)
	}
	if st, pr = h.do("PATCH", "/api/projects/"+id, map[string]any{"badgeId": prod["id"]}, nil); st != http.StatusOK || pr["badgeId"] != prod["id"] {
		t.Fatalf("%d %v", st, pr)
	}
	if st, pr = h.do("PATCH", "/api/projects/"+id, map[string]any{"name": "Shop 2"}, nil); st != http.StatusOK || pr["badgeId"] != prod["id"] {
		t.Fatalf("name alone leaves it: %d %v", st, pr)
	}
	if st, pr = h.do("PATCH", "/api/projects/"+id, map[string]any{"badgeId": ""}, nil); st != http.StatusOK || pr["badgeId"] != nil {
		t.Fatalf("%d %v", st, pr)
	}
	h.do("PATCH", "/api/projects/"+id, map[string]any{"badgeId": prod["id"]}, nil)

	// Removing the badge takes it off the project.
	if st, b := h.do("DELETE", "/api/project-badges/"+prod["id"].(string), nil, nil); st != http.StatusOK || b["ok"] != true {
		t.Fatalf("%d %v", st, b)
	}
	var projects []map[string]any
	h.getJSON("/api/projects", &projects)
	found := false
	for _, x := range projects {
		if x["id"] == id {
			found = true
			if x["badgeId"] != nil {
				t.Fatalf("%v", x)
			}
		}
	}
	if !found {
		t.Fatalf("project gone: %v", projects)
	}
	if st, b := h.do("DELETE", "/api/project-badges/"+prod["id"].(string), nil, nil); st != http.StatusNotFound || b["code"] != "badge_not_found" {
		t.Fatalf("%d %v", st, b)
	}
}

func TestProjectBadgesEvents(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, b := h.do("POST", "/api/project-badges", map[string]any{"name": "First", "color": "blue"}, nil)
	if st != http.StatusCreated {
		t.Fatalf("%d %v", st, b)
	}
	st, pr := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "Shop"}, nil)
	if st != http.StatusCreated {
		t.Fatalf("%d %v", st, pr)
	}
	h.do("PATCH", "/api/projects/"+pr["id"].(string), map[string]any{"badgeId": b["id"]}, nil)
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
	if list, _ := snap["projectBadges"].([]any); snap["type"] != "snapshot" || len(list) != 1 {
		t.Fatalf("%v", snap)
	}
	if st, b := h.do("DELETE", "/api/project-badges/"+b["id"].(string), nil, nil); st != http.StatusOK {
		t.Fatalf("%d %v", st, b)
	}
	// the list comes first, then the project that lost its badge
	gotList := false
	for {
		var ev map[string]json.RawMessage
		if err := ws.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		switch string(ev["type"]) {
		case `"projectBadges"`:
			var badges []map[string]any
			if err := json.Unmarshal(ev["badges"], &badges); err != nil || badges == nil || len(badges) != 0 {
				t.Fatalf("%s %v", ev["badges"], err)
			}
			gotList = true
		case `"project"`:
			var p map[string]any
			if err := json.Unmarshal(ev["project"], &p); err != nil {
				t.Fatal(err)
			}
			if p["id"] != pr["id"] {
				continue
			}
			if !gotList || p["badgeId"] != nil {
				t.Fatalf("%v (list first: %v)", p, gotList)
			}
			return
		}
	}
}
