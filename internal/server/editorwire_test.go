package server

import "testing"

// An orchestrator card wired to an editor card directs the agents opened inside it, so its
// @mentions list them (docs/API-studio.md §7.8); the editor card a pane was opened in is kept.
func TestCardMentionsTheAgentsOfAWiredEditor(t *testing.T) {
	h := newTestHub(t)
	pid, wid, _ := orchSetup(t, h)
	if st, b := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "workspaceId": wid, "panel": true, "card": "not a node"}, nil); st != 400 {
		t.Fatalf("bad card %d %v", st, b)
	}
	st, in := h.do("POST", "/api/sessions", map[string]any{"agent": "fake-claude", "workspaceId": wid, "panel": true, "card": "n_edit000001", "handle": "Ash"}, nil)
	if st != 201 || in["card"] != "n_edit000001" {
		t.Fatalf("create %d %v", st, in)
	}
	t.Cleanup(func() { h.do("DELETE", "/api/sessions/"+in["id"].(string), nil, nil) })
	if st, b := h.ops(wid,
		map[string]any{"op": "put", "node": map[string]any{"id": "n_edit000001", "kind": "editor", "x": 0, "y": 0, "w": 1200, "h": 720, "data": map[string]any{}}},
		map[string]any{"op": "put", "node": map[string]any{"id": "n_orch000001", "kind": "orchestrator", "x": 1300, "y": 0, "w": 400, "h": 500, "data": map[string]any{"title": "Lead"}}},
	); st != 200 {
		t.Fatalf("cards %d %v", st, b)
	}
	mentions := func() []any {
		return h.list("/api/projects/" + pid + "/mentions?workspaceId=" + wid + "&cardId=n_orch000001&q=ash")
	}
	if ms := mentions(); len(ms) != 0 {
		t.Fatalf("before the wire %v", ms)
	}
	if st, b := h.ops(wid, map[string]any{"op": "putEdge", "edge": map[string]any{"id": "e_wire000001", "kind": "wire", "source": "n_orch000001", "target": "n_edit000001", "data": map[string]any{}}}); st != 200 {
		t.Fatalf("wire %d %v", st, b)
	}
	if ms := mentions(); len(ms) != 1 || ms[0].(map[string]any)["handle"] != "Ash" {
		t.Fatalf("mentions %v", ms)
	}
}
