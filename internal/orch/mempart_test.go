package orch

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/project"
)

// twoParts is a project "shop" with its first part and a part Web.
func twoParts(t *testing.T) (*fakeHub, *Service, project.Project, project.Part, string) {
	t.Helper()
	h, s := newFakeHub(t)
	p, wid := h.project("shop")
	p, web, err := h.projects.AddPart(p.ID, "Web", "local", "/work/shop-web")
	if err != nil {
		t.Fatal(err)
	}
	return h, s, p, web, wid
}

func TestPartSlug(t *testing.T) {
	if got := PartSlug("pt_ab2cd3ef45"); got != "part-ab2cd3ef45" || !subSlugRe.MatchString(got) {
		t.Fatalf("slug %q", got)
	}
	if got := PartSlug(project.MainPart); got != "part-main" || !subSlugRe.MatchString(got) {
		t.Fatalf("an old part id %q", got)
	}
}

// A part's page shows the part's name and place as they are now, whatever a client sends, and is
// only for a part the project has.
func TestPartPageFollowsItsPart(t *testing.T) {
	h0, s0 := newFakeHub(t)
	one, _ := h0.project("solo")
	// a project of one part has no part pages: a page of that name is an ordinary one
	if d, err := s0.WriteDoc(one.ID, PartSlug(one.Parts[0].ID), "Notes", "", "- x\n", ""); err != nil || d.Part != "" || d.Title != "Notes" {
		t.Fatalf("a project of one part has no part pages: %+v %v", d, err)
	}
	h, s, p, web, _ := twoParts(t)
	slug := PartSlug(web.ID)
	if _, err := s.WriteDoc(p.ID, "part-zzzzzzzzzz", "", "", "- x\n", ""); !errors.Is(err, ErrPartNotFound) {
		t.Fatalf("an unknown part: %v", err)
	}
	d, err := s.WriteDoc(p.ID, slug, "My title", "My words", "## Commands\n- npm run dev starts vite\n", "")
	if err != nil || d.Part != web.ID || d.Title != "Web" || d.Description != "What holds for the Web part only (This PC, /work/shop-web)." {
		t.Fatalf("write %+v %v", d, err)
	}
	if ev := h.lastEvent("memory"); ev["doc"] != slug || ev["projectId"] != p.ID {
		t.Fatalf("event %v", ev)
	}
	name := "Frontend"
	if _, err := h.projects.PatchPart(p.ID, web.ID, project.PartPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.ReadDoc(p.ID, slug); d.Title != "Frontend" || d.Part != web.ID || !strings.Contains(d.Description, "the Frontend part only") {
		t.Fatalf("after a rename %+v", d)
	}
	v, _ := s.ReadMemory(p.ID)
	found := false
	for _, sub := range v.Subs {
		if sub.Slug == slug {
			found = sub.Part == web.ID && sub.Title == "Frontend"
		}
	}
	if !found {
		t.Fatalf("listing %+v", v.Subs)
	}
	for _, o := range s.Galaxy().Projects {
		for _, gd := range o.Docs {
			if gd.Name == slug && (gd.Part != web.ID || gd.Title != "Frontend") {
				t.Fatalf("galaxy %+v", gd)
			}
		}
	}
	if idx := s.memoryIndex(p.ID); !strings.Contains(idx, "- "+slug+": Frontend (part memory). What holds for the Frontend part only") {
		t.Fatalf("index %s", idx)
	}
	if hits := s.SearchMemory(p.ID, "vite", 0, 0); len(hits) == 0 || hits[0].Part != web.ID {
		t.Fatalf("search %+v", hits)
	}
}

// Accepted facts go where they hold: the main memory, or a part's page, made when missing. A part
// the project does not have is the whole project's.
func TestAcceptRoutesFactsToParts(t *testing.T) {
	_, s, p, web, _ := twoParts(t)
	res, err := s.AcceptMemoryFacts(p.ID, "project", []Fact{{Kind: "commands", Text: "make test runs everything"},
		{Kind: "commands", Text: "npm run dev starts vite", Part: web.ID}, {Kind: "gotchas", Text: "the gone part fact", Part: "pt_gone000000"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "make test") || !strings.Contains(res.Content, "the gone part fact") || strings.Contains(res.Content, "vite") {
		t.Fatalf("main %q", res.Content)
	}
	if pg := res.Parts[web.ID]; len(res.Parts) != 1 || !strings.Contains(pg.Content, "## Commands\n- npm run dev starts vite") || pg.Mtime == "" {
		t.Fatalf("parts %+v", res.Parts)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"content":`) || !strings.Contains(string(b), `"parts":{"`+web.ID+`":{"content"`) {
		t.Fatalf("json %s", b)
	}
	// only part facts: the main memory is not written
	main, _ := s.Memory.Read("project", p.ID)
	res, err = s.AcceptMemoryFacts(p.ID, "project", []Fact{{Kind: "context", Text: "the web deploys to a bucket", Part: web.ID}})
	if err != nil || res.Mtime != main.Mtime || !strings.Contains(res.Parts[web.ID].Content, "vite") || !strings.Contains(res.Parts[web.ID].Content, "bucket") {
		t.Fatalf("part only %+v %v", res, err)
	}
	// a project of one part: everything is the whole project's
	h, s1 := newFakeHub(t)
	one, _ := h.project("solo")
	res, err = s1.AcceptMemoryFacts(one.ID, "project", []Fact{{Kind: "context", Text: "solo fact", Part: one.Parts[0].ID}})
	if err != nil || res.Parts != nil || !strings.Contains(res.Content, "solo fact") {
		t.Fatalf("one part %+v %v", res, err)
	}
	b, _ = json.Marshal(res)
	if strings.Contains(string(b), "parts") {
		t.Fatalf("json %s", b)
	}
}

// Scan and Init ask each fact's part by name and give it by id; an unknown name is the whole
// project's. A project of one part keeps the schema it had.
func TestScanAndInitNameParts(t *testing.T) {
	h, s, p, web, _ := twoParts(t)
	if _, err := s.WriteDoc(p.ID, PartSlug(web.ID), "", "", "- the web uses pnpm\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScanMemory(p.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	run := h.lastRun(t, "memory-scan")
	if !strings.Contains(run.Prompt, `Give every fact a "part"`) || !strings.Contains(run.Prompt, `"Web"`) || !strings.Contains(run.Prompt, "Existing memory of the part Web:\n- the web uses pnpm") {
		t.Fatalf("prompt %s", run.Prompt)
	}
	var schema map[string]any
	_ = json.Unmarshal(run.Schema, &schema)
	item := schema["properties"].(map[string]any)["facts"].(map[string]any)["items"].(map[string]any)
	part := item["properties"].(map[string]any)["part"].(map[string]any)
	if part["type"] != "string" || len(part["enum"].([]any)) != 3 || !strings.Contains(string(run.Schema), `"required":["kind","text","part"]`) {
		t.Fatalf("schema %s", run.Schema)
	}
	var strict map[string]any
	if err := json.Unmarshal(jobs.StrictSchema(run.Schema), &strict); err != nil {
		t.Fatal(err)
	}
	out, err := run.Transform(`{"facts":[{"kind":"commands","text":"pnpm dev starts it","part":"web"},{"kind":"context","text":"b","part":"Nope"},{"kind":"context","text":"c","part":""}]}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Facts []Fact }
	_ = json.Unmarshal(out, &got)
	if len(got.Facts) != 3 || got.Facts[0].Part != web.ID || got.Facts[1].Part != "" || got.Facts[2].Part != "" {
		t.Fatalf("scan facts %+v", got.Facts)
	}

	if _, err := s.InitMemory(p.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	run = h.lastRun(t, "memory-init")
	if !strings.Contains(run.Prompt, `Give every fact a "part"`) || !strings.Contains(string(run.Schema), `"part"`) {
		t.Fatalf("init %s\n%s", run.Prompt, run.Schema)
	}
	out, err = run.Transform(`{"facts":[{"kind":"commands","text":"pnpm build makes dist","part":"WEB"},{"kind":"context","text":"one shop","part":""}],"subs":[],"critical":[]}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var r InitResult
	_ = json.Unmarshal(out, &r)
	if len(r.Facts) != 2 || r.Facts[0].Part != web.ID || r.Facts[1].Part != "" {
		t.Fatalf("init facts %+v", r.Facts)
	}
	if err := s.ApplyInit(p.ID, r); err != nil {
		t.Fatal(err)
	}
	pg, _ := s.Memory.Sub(p.ID, PartSlug(web.ID))
	main, _ := s.Memory.Read("project", p.ID)
	if !strings.Contains(pg.Content, "pnpm build makes dist") || !strings.Contains(main.Content, "one shop") || strings.Contains(main.Content, "pnpm build") {
		t.Fatalf("applied: page %q main %q", pg.Content, main.Content)
	}

	// one part: as before
	one, _ := h.project("solo")
	if _, err := s.ScanMemory(one.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	run = h.lastRun(t, "memory-scan")
	if string(run.Schema) != string(ScanSchema) || strings.Contains(run.Prompt, `"part"`) {
		t.Fatalf("one part %s\n%s", run.Schema, run.Prompt)
	}
	out, _ = run.Transform(`{"facts":[{"kind":"commands","text":"x","part":"solo"}]}`, nil)
	if strings.Contains(string(out), "part") {
		t.Fatalf("one part facts %s", out)
	}
}

// A console in a part gets its part's page after the main memory, in what the budget leaves; a
// root console finds the part pages in the list.
func TestLaunchMemoryOfAPart(t *testing.T) {
	h, s, p, web, wid := twoParts(t)
	slug := PartSlug(web.ID)
	if _, err := s.WriteMemory(p.ID, "project", "## Commands\n- make test runs everything\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDoc(p.ID, slug, "", "", "## Commands\n- npm run dev starts vite\n", ""); err != nil {
		t.Fatal(err)
	}
	ayu := h.console(p, wid, "Ayu", "codex", "idle")
	text := s.LaunchMemory(ayu.ID, wid, p.ID, web.ID, false)
	i, j := strings.Index(text, "make test runs everything"), strings.Index(text, "Memory of your part Web:\n## Commands\n- npm run dev starts vite")
	if i < 0 || j < i || !strings.Contains(text, `you work in the part Web`) || !strings.Contains(text, `memory_save and part: "Web"`) || strings.Contains(text, "- "+slug+":") {
		t.Fatalf("part console:\n%s", text)
	}
	if seen := s.seenBy(ayu.ID)[memoryKey(p.ID, slug)]; len(seen.Lines) != 1 {
		t.Fatalf("seen %+v", seen)
	}
	// Claude Code keeps the main memory itself: the part page still comes
	if text := s.LaunchMemory(ayu.ID, wid, p.ID, web.ID, true); !strings.Contains(text, "Memory of your part Web:") || strings.Contains(text, "make test") {
		t.Fatalf("native:\n%s", text)
	}
	bea := h.console(p, wid, "Bea", "codex", "idle")
	root := s.LaunchMemory(bea.ID, wid, p.ID, "", false)
	if !strings.Contains(root, "- "+slug+": Web (part memory)") || strings.Contains(root, "npm run dev") || !strings.Contains(root, "part set to the part's name") {
		t.Fatalf("root console:\n%s", root)
	}
	// the main memory goes first: the part page gets what is left, and is listed when nothing is
	if _, err := s.WriteMemory(p.ID, "project", strings.Repeat("- a filler fact for the budget\n", 480), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDoc(p.ID, slug, "", "", strings.Repeat("- a long web fact\n", 200), ""); err != nil {
		t.Fatal(err)
	}
	if text := s.LaunchMemory(ayu.ID, wid, p.ID, web.ID, false); !strings.Contains(text, partCutNote) || s.seenBy(ayu.ID)[memoryKey(p.ID, slug)].Told == 0 {
		t.Fatalf("cut:\n%.300s", text[len(text)-300:])
	}
	if _, err := s.WriteMemory(p.ID, "project", strings.Repeat("- a filler fact for the budget\n", 700), ""); err != nil {
		t.Fatal(err)
	}
	if text := s.LaunchMemory(ayu.ID, wid, p.ID, web.ID, false); strings.Contains(text, "Memory of your part") || !strings.Contains(text, "- "+slug+": Web (part memory)") {
		t.Fatalf("no room:\n%s", text[:2000])
	}
	// one part: nothing changes
	one, owid := h.project("solo")
	solo := h.console(one, owid, "Cas", "codex", "idle")
	if got := s.LaunchMemory(solo.ID, owid, one.ID, one.Parts[0].ID, false); got != s.MemoryPrompt(one.ID, false) || strings.Contains(got, "several parts") {
		t.Fatalf("one part:\n%s", got)
	}
}

// memory_save and memory_read take a part by name.
func TestMemorySaveToAPart(t *testing.T) {
	h, s, p, web, wid := twoParts(t)
	page, err := s.PartPage(p.ID, "web")
	if err != nil || page != PartSlug(web.ID) {
		t.Fatalf("page %q %v", page, err)
	}
	if _, err := s.PartPage(p.ID, "Docs"); !errors.Is(err, ErrPartNotFound) || !strings.Contains(err.Error(), "shop, Web") {
		t.Fatalf("unknown part %v", err)
	}
	if got, err := s.MemoryReadText(p.ID, page, ""); err != nil || !strings.Contains(got, "The part Web has no memory of its own yet") {
		t.Fatalf("read before %q %v", got, err)
	}
	if err := s.SaveMemoryFact(p.ID, "Commands", "npm run build makes dist", page); err != nil {
		t.Fatal(err)
	}
	d, err := s.ReadDoc(p.ID, page)
	main, _ := s.Memory.Read("project", p.ID)
	if err != nil || d.Title != "Web" || !strings.Contains(d.Content, "## Commands\n- npm run build makes dist") || strings.Contains(main.Content, "dist") {
		t.Fatalf("saved %+v %v, main %q", d, err, main.Content)
	}
	ayu := h.console(p, wid, "Ayu", "codex", "idle")
	h.mu.Lock()
	h.sessions[ayu.ID].PartID = web.ID
	h.mu.Unlock()
	if got, _ := s.MemoryReadFor(ayu.ID, wid, p.ID, "", ""); !strings.Contains(got, "Memory of your part Web:\n## Commands\n- npm run build makes dist") {
		t.Fatalf("read %s", got)
	}
	one, _ := h.project("solo")
	if page, err := s.PartPage(one.ID, "solo"); err != nil || page != MainDoc {
		t.Fatalf("one part %q %v", page, err)
	}
}

// A removed part's page stays as an ordinary page; a split part's page becomes its new project's
// main memory; a merged project's part pages become the pages of the parts they are now.
func TestPartMemoryOutlivesItsPart(t *testing.T) {
	h, s, p, web, _ := twoParts(t)
	p, ops, err := h.projects.AddPart(p.ID, "Ops", "local", "/work/shop-ops")
	if err != nil {
		t.Fatal(err)
	}
	first := p.Parts[0]
	for _, pt := range []project.Part{first, web, ops} {
		if _, err := s.WriteDoc(p.ID, PartSlug(pt.ID), "", "", "- what holds for "+pt.Name+"\n", ""); err != nil {
			t.Fatal(err)
		}
	}
	before := p
	if p, _, err = h.projects.DeletePart(p.ID, web.ID); err != nil {
		t.Fatal(err)
	}
	s.PartRemoved(before, web.ID)
	d, err := s.ReadDoc(p.ID, PartSlug(web.ID))
	if err != nil || d.Part != "" || d.Title != "Web (removed part)" || !strings.Contains(d.Content, "what holds for Web") {
		t.Fatalf("removed %+v %v", d, err)
	}
	if _, err := s.WriteDoc(p.ID, PartSlug(web.ID), "", "", d.Content+"- one more\n", ""); err != nil {
		t.Fatalf("the page can still be changed: %v", err)
	}

	before = p
	np, p, err := h.projects.Split(p.ID, ops.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.PartSplit(before, np.ID, ops.ID)
	if m, _ := s.Memory.Read("project", np.ID); !strings.Contains(m.Content, "what holds for Ops") {
		t.Fatalf("split main %q", m.Content)
	}
	if _, err := s.Memory.Sub(p.ID, PartSlug(ops.ID)); !errors.Is(err, ErrSubNotFound) {
		t.Fatalf("the page went with the part: %v", err)
	}
	// one part left: its page is an ordinary one
	if d, err := s.ReadDoc(p.ID, PartSlug(first.ID)); err != nil || d.Part != "" || d.Title != first.Name || !strings.Contains(d.Content, "what holds for "+first.Name) {
		t.Fatalf("the last part's page %+v %v", d, err)
	}

	// merging a project of two parts carries its part pages over
	b, _ := h.project("tools")
	b, cli, err := h.projects.AddPart(b.ID, "Cli", "local", "/work/tools-cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDoc(b.ID, PartSlug(cli.ID), "", "", "- the cli is cobra\n", ""); err != nil {
		t.Fatal(err)
	}
	_, gone, plan, err := h.projects.Merge(p.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	carried := s.MergedPartPages(gone, p.ID, plan.PartIDs)
	d, err = s.ReadDoc(p.ID, PartSlug(plan.PartIDs[cli.ID]))
	if !carried[PartSlug(cli.ID)] || err != nil || d.Part != plan.PartIDs[cli.ID] || !strings.Contains(d.Content, "cobra") {
		t.Fatalf("merged %v %+v %v", carried, d, err)
	}
}

// Only the shape PartSlug gives is kept for parts: another name starting with "part-" is a page.
func TestPartSlugShape(t *testing.T) {
	if !IsPartSlug(PartSlug("pt_ab23cd45ef")) || IsPartSlug("part-time-work") || IsPartSlug("partners") {
		t.Fatal("part slug shape")
	}
}

// Someone who starts a part's memory while an agent makes it gets a conflict, not the agent's
// facts overwritten; two first writers of a page both keep their facts.
func TestPartPageWritesNeverLoseFacts(t *testing.T) {
	_, s, p, web, _ := twoParts(t)
	slug := PartSlug(web.ID)
	if _, err := s.WriteDoc(p.ID, slug, "", "", "- the owner's first line\n", MtimeAbsent); err != nil {
		t.Fatalf("a new page with absent: %v", err)
	}
	if _, err := s.WriteDoc(p.ID, slug, "", "", "- would overwrite\n", MtimeAbsent); !errors.Is(err, ErrMtimeMismatch) {
		t.Fatalf("absent on a page that exists: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.SaveMemoryFact(p.ID, "context", fmt.Sprintf("fact number %d of the web", i), slug); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	d, _ := s.Memory.Sub(p.ID, slug)
	for i := 0; i < 8; i++ {
		if !strings.Contains(d.Content, fmt.Sprintf("fact number %d of the web", i)) {
			t.Fatalf("fact %d lost:\n%s", i, d.Content)
		}
	}
	if !strings.Contains(d.Content, "the owner's first line") {
		t.Fatalf("first line lost:\n%s", d.Content)
	}
}

// A project in one part keeps no names for parts: "part-" and ten letters is an ordinary page.
func TestOnePartProjectKeepsNoPartNames(t *testing.T) {
	h, s := newFakeHub(t)
	one, _ := h.project("solo")
	if _, err := s.WriteDoc(one.ID, "part-onboarding", "Part onboarding", "How to start", "- x\n", ""); err != nil {
		t.Fatalf("ordinary page: %v", err)
	}
}
