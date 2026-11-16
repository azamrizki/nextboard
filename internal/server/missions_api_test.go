package server

import (
	"strings"
	"testing"
	"time"
)

// TestMissionAPI: a mission made through the API waits for its approval, sends its first task
// as an order once approved (a dependent one waits), and is paused, resumed and stopped by the
// owner (docs/API-orchestration.md §22).
func TestMissionAPI(t *testing.T) {
	h := newTestHub(t)
	pid, wid, dir := orchSetup(t, h)
	a, b := h.spawnAgent(wid), h.spawnAgent(wid)
	h.waitInfo(a["id"].(string), "ready", state("ready"))
	h.waitInfo(b["id"].(string), "ready", state("ready"))
	ha, hb := a["handle"].(string), b["handle"].(string)

	body := map[string]any{"goal": "Orders page", "summary": "API first, then the page", "doneWhen": "the page lists orders",
		"doc": "GET /api/orders → [{id,total}]",
		"tasks": []map[string]any{{"id": "t1", "handle": ha, "title": "API", "text": "serve the orders list"},
			{"id": "t2", "handle": hb, "title": "Page", "text": "build the orders page", "dependsOn": []string{"t1"}}}}
	for name, bad := range map[string]map[string]any{
		"unknown console": {"goal": "x", "tasks": []map[string]any{{"id": "t1", "handle": "Nobody", "text": "x"}}},
		"no goal":         {"tasks": []map[string]any{{"id": "t1", "handle": ha, "text": "x"}}},
	} {
		if st, r := h.do("POST", "/api/projects/"+pid+"/missions", bad, nil); st != 400 || r["code"] != "bad_mission" {
			t.Fatalf("%s: %d %v", name, st, r)
		}
	}
	if st, r := h.do("POST", "/api/projects/p_nothere00/missions", body, nil); st != 404 {
		t.Fatalf("unknown project %d %v", st, r)
	}
	st, ms := h.do("POST", "/api/projects/"+pid+"/missions", body, nil)
	if st != 201 || ms["state"] != "awaiting_approval" || ms["threadId"] != pid || ms["confirmationId"] == "" || ms["docRev"] != float64(1) {
		t.Fatalf("create %d %v", st, ms)
	}
	id := ms["id"].(string)
	if st, r := h.do("POST", "/api/projects/"+pid+"/missions", body, nil); st != 201 || r["id"] != id {
		t.Fatalf("a new plan while one awaits approval replaces it: %d %v", st, r)
	} else {
		ms = r
	}
	if st, r := h.do("POST", "/api/confirmations/"+ms["confirmationId"].(string), map[string]any{"decision": "approve"}, nil); st != 200 || r["kind"] != "mission" || r["state"] != "confirmed" {
		t.Fatalf("approve %d %v", st, r)
	}
	waitTrue(t, "t1 typed", 10*time.Second, func() bool { return strings.Contains(received(dir), "serve the orders list") })
	if strings.Contains(received(dir), "build the orders page") {
		t.Fatal("t2 went out before t1 was done")
	}
	_, ms = h.do("GET", "/api/missions/"+id, nil, nil)
	t1 := ms["tasks"].([]any)[0].(map[string]any)
	if ms["state"] != "running" || t1["orderId"] == "" || ms["maxRounds"] != float64(30) {
		t.Fatalf("running %v", ms)
	}
	_, o := h.do("GET", "/api/orders/"+t1["orderId"].(string), nil, nil)
	if o["missionId"] != id {
		t.Fatalf("order %v", o)
	}
	_, l := h.do("GET", "/api/projects/"+pid+"/missions?active=1", nil, nil)
	if items := l["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != id {
		t.Fatalf("active %v", l)
	}
	if st, r := h.do("POST", "/api/projects/"+pid+"/missions", body, nil); st != 409 || r["code"] != "mission_active" {
		t.Fatalf("second mission %d %v", st, r)
	}
	for _, step := range []struct{ action, state string }{{"pause", "paused"}, {"resume", "running"}, {"stop", "stopped"}} {
		if st, r := h.do("POST", "/api/missions/"+id+"/"+step.action, nil, nil); st != 200 || r["state"] != step.state {
			t.Fatalf("%s %d %v", step.action, st, r)
		}
	}
	if st, r := h.do("POST", "/api/missions/"+id+"/resume", nil, nil); st != 409 || r["code"] != "mission_state" {
		t.Fatalf("resume a stopped mission %d %v", st, r)
	}
	if st, _ := h.do("GET", "/api/missions/ms_nothere00", nil, nil); st != 404 {
		t.Fatalf("unknown mission %d", st)
	}
	_, l = h.do("GET", "/api/projects/"+pid+"/missions?active=1", nil, nil)
	_, all := h.do("GET", "/api/projects/"+pid+"/missions", nil, nil)
	if len(l["items"].([]any)) != 0 || len(all["items"].([]any)) != 1 {
		t.Fatalf("lists %v %v", l, all)
	}
}
