package server

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/orch"
)

// TestPartMemories: a project of two parts has its main memory and a page per part (§12). The
// page is written and listed with its part, accepted facts go where they hold, an agent in the
// part gets its page at launch and saves to it by the part's name, and removing the part keeps
// the page.
func TestPartMemories(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	bin := spyCodex(t, h)
	api, web := t.TempDir(), t.TempDir()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": api, "name": "Shop"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid, wid := p["id"].(string), p["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	_, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Web", "machineId": "local", "path": web}, nil)
	webID := p["parts"].([]any)[1].(map[string]any)["id"].(string)
	slug := orch.PartSlug(webID)
	_, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "Ops", "machineId": "local", "path": t.TempDir()}, nil)
	opsID := p["parts"].([]any)[2].(map[string]any)["id"].(string)

	if st, e := h.do("PUT", "/api/memory/docs/part-zzzzzzzzzz?projectId="+pid, map[string]any{"content": "- x\n"}, nil); st != 404 || e["code"] != "part_not_found" {
		t.Fatalf("unknown part %d %v", st, e)
	}
	st, d := h.do("PUT", "/api/memory/docs/"+slug+"?projectId="+pid, map[string]any{"title": "Mine", "content": "## Commands\n- the web runs on vite\n"}, nil)
	if st != 200 || d["part"] != webID || d["title"] != "Web" || !strings.Contains(fmt.Sprint(d["description"]), "the Web part only (This PC, "+web+")") {
		t.Fatalf("write %d %v", st, d)
	}
	if _, d = h.do("GET", "/api/memory/docs/"+slug+"?projectId="+pid, nil, nil); d["part"] != webID {
		t.Fatalf("get %v", d)
	}
	_, m := h.do("GET", "/api/projects/"+pid+"/memory", nil, nil)
	if !strings.Contains(fmt.Sprint(m["subs"]), "part:"+webID) {
		t.Fatalf("listing %v", m["subs"])
	}
	_, g := h.do("GET", "/api/memory/galaxy", nil, nil)
	if !strings.Contains(fmt.Sprint(g["projects"]), "part:"+webID) {
		t.Fatalf("galaxy %v", g["projects"])
	}
	st, a := h.do("POST", "/api/projects/"+pid+"/memory/accept", map[string]any{"scope": "project", "facts": []any{
		map[string]any{"kind": "commands", "text": "make test runs everything"}, map[string]any{"kind": "gotchas", "text": "the web needs node 22", "part": webID}}}, nil)
	parts, _ := a["parts"].(map[string]any)
	if st != 200 || !strings.Contains(fmt.Sprint(a["content"]), "make test") || strings.Contains(fmt.Sprint(a["content"]), "node 22") ||
		len(parts) != 1 || !strings.Contains(fmt.Sprint(parts[webID]), "node 22") {
		t.Fatalf("accept %d %v", st, a)
	}

	// an agent in the part: its page at launch, after the main memory; it saves there by name
	sa := newSpyAgents(t, h, bin, wid)
	sid, tok, _, _ := sa.start(map[string]any{"partId": webID})
	prompt, err := os.ReadFile(h.srv.st.Path("agents", "prompts", sid+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if i, j := strings.Index(string(prompt), "make test runs everything"), strings.Index(string(prompt), "Memory of your part Web:\n## Commands\n- the web runs on vite"); i < 0 || j < i ||
		!strings.Contains(string(prompt), `memory_save and part: "Web"`) {
		t.Fatalf("prompt:\n%s", prompt)
	}
	ts := sa.tools(sid, tok)
	if props := ts["memory_save"]["inputSchema"].(map[string]any)["properties"].(map[string]any); props["part"] == nil {
		t.Fatalf("memory_save without part %v", props)
	}
	if out := sa.call(sid, tok, "memory_save", map[string]any{"fact": "pnpm build makes dist", "section": "Commands", "part": "web"}); !strings.Contains(out, "Saved to the memory of the part web") {
		t.Fatalf("save %s", out)
	}
	if out := sa.call(sid, tok, "memory_read", map[string]any{"part": "Web"}); !strings.Contains(out, "pnpm build makes dist") {
		t.Fatalf("read %s", out)
	}
	if out := sa.call(sid, tok, "memory_save", map[string]any{"fact": "x", "part": "Docs"}); !strings.Contains(out, "no such part") {
		t.Fatalf("unknown part %s", out)
	}
	if main, _ := h.srv.orch.Memory.Read("project", pid); strings.Contains(main.Content, "pnpm") {
		t.Fatalf("a part's fact in the main memory %q", main.Content)
	}

	// the part goes: its page stays, as an ordinary page
	h.do("DELETE", "/api/sessions/"+sid, nil, nil)
	if st, e := h.do("DELETE", "/api/projects/"+pid+"/parts/"+webID, nil, nil); st != 200 {
		t.Fatalf("remove %d %v", st, e)
	}
	if _, d = h.do("GET", "/api/memory/docs/"+slug+"?projectId="+pid, nil, nil); d["part"] != nil || d["title"] != "Web (removed part)" || !strings.Contains(fmt.Sprint(d["content"]), "pnpm build") {
		t.Fatalf("after removal %v", d)
	}
	// a part split off takes its page along, as the new project's main memory
	h.do("PUT", "/api/memory/docs/"+orch.PartSlug(opsID)+"?projectId="+pid, map[string]any{"content": "- ops deploys with ansible\n"}, nil)
	st, sp := h.do("POST", "/api/projects/"+pid+"/parts/"+opsID+"/split", nil, nil)
	if st != 201 {
		t.Fatalf("split %d %v", st, sp)
	}
	np := sp["project"].(map[string]any)["id"].(string)
	if _, m := h.do("GET", "/api/projects/"+np+"/memory", nil, nil); !strings.Contains(fmt.Sprint(m["project"]), "ops deploys with ansible") {
		t.Fatalf("the split project's memory %v", m["project"])
	}
	if st, _ := h.do("GET", "/api/memory/docs/"+orch.PartSlug(opsID)+"?projectId="+pid, nil, nil); st != 404 {
		t.Fatalf("the page stayed behind %d", st)
	}
}
