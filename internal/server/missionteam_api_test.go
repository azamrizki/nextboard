package server

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestMissionTeamTools: the team of a mission talks only through the hub's MCP tools
// (API-orchestration §22). A question reaches the teammate as a delivery of source "team" and
// stands in the thread, the answer goes back, mission_read shows the mission, a stale brief is
// refused, mission_report done marks the task and wakes the lead, and a console outside the
// mission is told it is not in one.
func TestMissionTeamTools(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	bin := spyCodex(t, h)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	sa := newSpyAgents(t, h, bin, wid)
	web, webTok, argv, _ := sa.start(map[string]any{})
	api, apiTok, _, _ := sa.start(map[string]any{})
	ops, opsTok, _, _ := sa.start(map[string]any{})
	handle := func(sid string) string {
		s, ok := h.srv.sessions.Get(sid)
		if !ok {
			t.Fatalf("no console %s", sid)
		}
		return s.Spec().Handle
	}
	webH, apiH := handle(web), handle(api)
	isErr := func(out string) bool { return strings.Contains(out, "isError:true") }

	// every console has the tools, since a CLI keeps the list it got at start; mission_read only
	// reads
	ts := sa.tools(ops, opsTok)
	for _, n := range []string{"mission_read", "team_ask", "team_reply", "team_update", "mission_doc", "mission_report"} {
		if ts[n] == nil {
			t.Fatalf("no %s in %v", n, ts)
		}
	}
	if fmt.Sprint(ts["mission_read"]["annotations"]) != "map[readOnlyHint:true]" || ts["team_ask"]["annotations"] != nil {
		t.Fatalf("annotations %v %v", ts["mission_read"]["annotations"], ts["team_ask"]["annotations"])
	}
	if strings.Contains(argv, "team_ask") {
		t.Fatalf("codex gets no approval rules from the hub:\n%s", argv)
	}
	if out := sa.call(ops, opsTok, "mission_read", map[string]any{}); !isErr(out) || !strings.Contains(out, "You are not in a mission.") {
		t.Fatalf("no mission yet: %s", out)
	}

	st, ms := h.do("POST", "/api/projects/"+pid+"/missions", map[string]any{"goal": "Orders page with its API", "summary": "Web builds the page, Api the endpoint.",
		"doneWhen": "the page lists orders from GET /api/orders", "doc": "GET /api/orders answers a list.",
		"tasks": []map[string]any{{"id": "t1", "handle": webH, "title": "Build the orders page", "text": "Build /orders."},
			{"id": "t2", "handle": apiH, "title": "Build GET /api/orders", "text": "Serve the orders."}}}, nil)
	if st != 201 || ms["state"] != "awaiting_approval" {
		t.Fatalf("mission %d %v", st, ms)
	}
	mid := ms["id"].(string)
	if out := sa.call(web, webTok, "mission_read", map[string]any{}); !strings.Contains(out, "You are not in a mission.") {
		t.Fatalf("before approval: %s", out)
	}
	if st, b := h.do("POST", "/api/confirmations/"+ms["confirmationId"].(string), map[string]any{"decision": "approve"}, nil); st != 200 {
		t.Fatalf("approve %d %v", st, b)
	}
	mission := func() map[string]any {
		t.Helper()
		st, m := h.do("GET", "/api/missions/"+mid, nil, nil)
		if st != 200 {
			t.Fatalf("mission %d %v", st, m)
		}
		return m
	}
	waitTrue(t, "running", 5*time.Second, func() bool { return mission()["state"] == "running" })

	deliveries := func(sid, source string) []map[string]any {
		_, b := h.do("GET", "/api/sessions/"+sid+"/deliveries", nil, nil)
		var out []map[string]any
		for _, x := range b["items"].([]any) {
			if d := x.(map[string]any); d["source"] == source {
				out = append(out, d)
			}
		}
		return out
	}
	team := func() []map[string]any {
		_, b := h.do("GET", "/api/threads/"+pid+"/messages", nil, nil)
		var out []map[string]any
		for _, x := range b["items"].([]any) {
			if m := x.(map[string]any); m["kind"] == "team" {
				out = append(out, m)
			}
		}
		return out
	}

	// a question to a teammate: delivered as talk, not an order, and in the thread
	out := sa.call(web, webTok, "team_ask", map[string]any{"to": "@" + apiH, "question": "Which fields does GET /api/orders answer?"})
	qid := regexp.MustCompile(`q_[A-Za-z0-9_-]+`).FindString(out)
	if isErr(out) || qid == "" || !strings.Contains(out, "Asked @"+apiH) {
		t.Fatalf("ask %s", out)
	}
	ds := deliveries(api, "team")
	if len(ds) != 1 || ds[0]["orderId"] != nil || !strings.Contains(ds[0]["text"].(string), "@"+webH+" asks you (mission \"Orders page with its API\", question "+qid+"): Which fields") {
		t.Fatalf("team delivery %v", ds)
	}
	msgs := team()
	if len(msgs) != 1 || msgs[0]["missionId"] != mid || msgs[0]["handle"] != webH ||
		fmt.Sprint(msgs[0]["team"]) != fmt.Sprintf("map[from:%s kind:ask questionId:%s to:%s]", webH, qid, apiH) {
		t.Fatalf("thread %v", msgs)
	}
	if out := sa.call(api, apiTok, "mission_read", map[string]any{}); !strings.Contains(out, "Mission: Orders page with its API") ||
		!strings.Contains(out, "Shared brief (rev 1; mission_doc changes it):\nGET /api/orders answers a list.") ||
		!strings.Contains(out, qid+" from @"+webH+": Which fields") || !strings.Contains(out, "t2 [") {
		t.Fatalf("mission_read %s", out)
	}

	// the answer goes back to the asker and closes the question
	if out := sa.call(api, apiTok, "team_reply", map[string]any{"question": qid, "answer": "id, total, createdAt"}); isErr(out) || !strings.Contains(out, "gets it") {
		t.Fatalf("reply %s", out)
	}
	if ds := deliveries(web, "team"); len(ds) != 1 || ds[0]["text"] != "@"+apiH+" answers your question "+qid+": id, total, createdAt" {
		t.Fatalf("answer delivery %v", ds)
	}
	if out := sa.call(api, apiTok, "team_reply", map[string]any{"question": qid, "answer": "again"}); !isErr(out) || !strings.Contains(out, "answered already") {
		t.Fatalf("twice %s", out)
	}

	// the brief (rev 1 as the mission was made): a stale rev is refused, an append moves it on
	if out := sa.call(web, webTok, "mission_doc", map[string]any{"text": "mine", "mode": "replace", "rev": 3}); !isErr(out) || !strings.Contains(out, "changed since rev 3") {
		t.Fatalf("stale rev %s", out)
	}
	if out := sa.call(web, webTok, "mission_doc", map[string]any{"text": "Dates are UTC.", "mode": "append"}); isErr(out) || !strings.Contains(out, "rev 2") {
		t.Fatalf("append %s", out)
	}
	if m := mission(); m["docRev"] != float64(2) || m["doc"] != "GET /api/orders answers a list.\n\nDates are UTC." {
		t.Fatalf("doc %v %v", m["docRev"], m["doc"])
	}

	// limits
	if out := sa.call(web, webTok, "team_update", map[string]any{"text": strings.Repeat("x", 4001), "to": apiH}); !isErr(out) || !strings.Contains(out, "at most 4000") {
		t.Fatalf("too long %s", out)
	}
	if out := sa.call(web, webTok, "team_ask", map[string]any{"to": handle(ops), "question": "hi?"}); !isErr(out) || !strings.Contains(out, "not in this mission") {
		t.Fatalf("outside the team %s", out)
	}
	if out := sa.call(ops, opsTok, "team_ask", map[string]any{"to": webH, "question": "hi?"}); !strings.Contains(out, "You are not in a mission.") {
		t.Fatalf("a console outside the mission %s", out)
	}

	// done: the task is done with the agent's own summary, and the lead wakes (its planner is
	// off here, which the wake says in the thread)
	if out := sa.call(api, apiTok, "mission_report", map[string]any{"status": "done", "summary": "GET /api/orders in routes/api.php; 4 tests pass."}); isErr(out) || !strings.Contains(out, "Reported t2 done") {
		t.Fatalf("report %s", out)
	}
	task := mission()["tasks"].([]any)[1].(map[string]any)
	if task["state"] != "done" || task["result"] != "GET /api/orders in routes/api.php; 4 tests pass." || task["resultBy"] != "report" {
		t.Fatalf("task %v", task)
	}
	waitTrue(t, "the lead woken", 15*time.Second, func() bool {
		_, b := h.do("GET", "/api/threads/"+pid+"/messages", nil, nil)
		for _, x := range b["items"].([]any) {
			if m := x.(map[string]any); m["missionId"] == mid && m["role"] == "hub" && strings.Contains(fmt.Sprint(m["text"]), "The lead cannot think right now") {
				return true
			}
		}
		return false
	})
	if msgs := team(); len(msgs) != 4 || fmt.Sprint(msgs[3]["team"]) != fmt.Sprintf("map[from:%s kind:report to:lead]", apiH) {
		t.Fatalf("thread %v", msgs)
	}

	// Stop: the team's messages not typed yet are cancelled with the orders, nothing is left
	// to type into the consoles
	if st, b := h.do("POST", "/api/missions/"+mid+"/stop", nil, nil); st != 200 || b["state"] != "stopped" {
		t.Fatalf("stop %d %v", st, b)
	}
	for _, sid := range []string{web, api} {
		for _, d := range deliveries(sid, "team") {
			if d["state"] != "cancelled" {
				t.Fatalf("a team delivery left after Stop: %v", d)
			}
		}
	}
}
