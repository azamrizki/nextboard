package server

import (
	"strings"
	"testing"
)

// An agent asked to start a page and to sort the memory into it can, through its MCP tools: the
// page is made by memory_save, and memory_move takes facts out of the main memory into it.
func TestAgentPagesThroughMCP(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	bin := spyCodex(t, h)
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": t.TempDir(), "name": "Sport"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	if st, _ := h.do("PUT", "/api/projects/"+pid+"/memory", map[string]any{"scope": "project", "content": "## Architecture\n- [api] Laravel serves /api\n- the app is Flutter\n"}, nil); st != 200 {
		t.Fatalf("memory %d", st)
	}
	sa := newSpyAgents(t, h, bin, wid)
	sid, tok, _, _ := sa.start(map[string]any{})
	if out := sa.call(sid, tok, "memory_save", map[string]any{"fact": "GET /health answers 200", "section": "Commands", "page": "api", "title": "API", "description": "The API: endpoints and how to run it."}); !strings.Contains(out, "Made the page api") {
		t.Fatalf("save %s", out)
	}
	if out := sa.call(sid, tok, "memory_move", map[string]any{"facts": []string{"[api] Laravel serves /api"}, "to": "API"}); !strings.Contains(out, "Moved 1 fact(s) from main to api") {
		t.Fatalf("move %s", out)
	}
	_, m := h.do("GET", "/api/projects/"+pid+"/memory", nil, nil)
	_, d := h.do("GET", "/api/memory/docs/api?projectId="+pid, nil, nil)
	if c := m["project"].(map[string]any)["content"].(string); strings.Contains(c, "[api]") || !strings.Contains(c, "Flutter") {
		t.Fatalf("main %q", c)
	}
	if c := d["content"].(string); d["title"] != "API" || !strings.Contains(c, "- [api] Laravel serves /api") || !strings.Contains(c, "GET /health") {
		t.Fatalf("page %v", d)
	}
	if out := sa.call(sid, tok, "memory_read", map[string]any{"name": "api"}); !strings.Contains(out, "Laravel serves /api") {
		t.Fatalf("read %s", out)
	}
}
