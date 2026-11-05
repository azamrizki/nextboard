package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nextboard/internal/board"
	"nextboard/internal/jobs"
	"nextboard/internal/machine"
	"nextboard/internal/store"
)

// fakeRunner imitates `claude -p --output-format stream-json` for the
// document and diagram purposes. FAKE_MODE picks the behaviour; FAKE_COUNT
// counts runs.
const fakeRunner = `#!/bin/sh
prompt=$(cat)
[ -n "$FAKE_COUNT" ] && echo x >> "$FAKE_COUNT"
[ -n "$FAKE_ARGS" ] && printf '%s\n' "$@" > "$FAKE_ARGS"
[ -n "$FAKE_ENV" ] && echo "cfg=${CLAUDE_CONFIG_DIR:-}" > "$FAKE_ENV"
n=$(wc -l < "${FAKE_COUNT:-/dev/null}" 2>/dev/null || echo 1)
case "$FAKE_MODE" in
doc)
  echo '{"type":"system","subtype":"init","session_id":"s1"}'
  printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"# Architecture\n\nThe hub "}]}}'
  sleep 0.1
  printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"talks to machines."}]}}'
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"# Architecture\n\nThe hub talks to machines.","session_id":"s1"}'
  ;;
partial)
  echo '{"type":"system","subtype":"init","session_id":"s2"}'
  printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"# Overview\n\nDraft so far"}]}}'
  sleep 30 ;;
diagram)
  if [ "$n" -le 1 ] && [ -n "$FAKE_BIG_FIRST" ]; then
    printf '{"type":"result","subtype":"success","is_error":false,"result":"x","structured_output":%s}\n' "$(cat "$FAKE_BIG_FIRST")"
  else
    printf '{"type":"result","subtype":"success","is_error":false,"result":"x","structured_output":%s}\n' "$(cat "$FAKE_DIAGRAM")"
  fi ;;
esac
`

type testEnv struct {
	t      *testing.T
	st     *store.Store
	jm     *jobs.Manager
	fake   string
	dir    string
	events *evlog
}

type evlog struct {
	mu   sync.Mutex
	list []map[string]any
}

func (e *evlog) add(v any) {
	e.mu.Lock()
	e.list = append(e.list, v.(map[string]any))
	e.mu.Unlock()
}

func (e *evlog) of(typ string) []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []map[string]any
	for _, x := range e.list {
		if x["type"] == typ {
			out = append(out, x)
		}
	}
	return out
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(fake, []byte(fakeRunner), 0o755); err != nil {
		t.Fatal(err)
	}
	local := machine.NewLocal(machine.Config{})
	jm := jobs.NewManager(st, func(string) (machine.Host, bool) { return local, true })
	ev := &evlog{}
	jm.Publish = ev.add
	t.Cleanup(jm.Close)
	return &testEnv{t: t, st: st, jm: jm, fake: fake, dir: dir, events: ev}
}

func (e *testEnv) wait(id string) jobs.Job {
	e.t.Helper()
	for i := 0; i < 400; i++ {
		if j, _ := e.jm.Get(id); !j.Active() {
			time.Sleep(50 * time.Millisecond) // OnFinish runs after the state change
			j, _ = e.jm.Get(id)
			return j
		}
		time.Sleep(25 * time.Millisecond)
	}
	e.t.Fatal("job did not finish")
	return jobs.Job{}
}

func TestDocumentsLifecycle(t *testing.T) {
	e := newEnv(t)
	d := NewDocuments(e.st)
	d.Submit, d.Publish = e.jm.SubmitRunner, e.events.add
	t.Setenv("FAKE_MODE", "doc")
	t.Setenv("FAKE_ARGS", filepath.Join(e.dir, "args"))
	t.Setenv("FAKE_ENV", filepath.Join(e.dir, "env"))
	pid := "p_abcdef123"
	doc, vid, j, err := d.Generate(pid, DocRequest{Type: "architecture", Effort: "quick", Agent: "claude", Cwd: e.dir, Command: e.fake,
		Env: []string{"CLAUDE_CONFIG_DIR=/tmp/prof-x"}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Architecture" || len(doc.Versions) != 1 || doc.Versions[0].State != "running" || doc.Versions[0].ID != vid {
		t.Fatalf("doc %+v", doc)
	}
	if j = e.wait(j.ID); j.State != jobs.StateDone {
		t.Fatalf("job %+v", j)
	}
	// streamed in order
	var text string
	for _, ev := range e.events.of("jobOutput") {
		text += ev["text"].(string)
	}
	if text != "# Architecture\n\nThe hub talks to machines." {
		t.Fatalf("stream %q", text)
	}
	v, md, err := d.Version(doc.ID, vid)
	if err != nil || v.State != "complete" || v.Words != 6 || !strings.HasPrefix(md, "# Architecture") || v.JobID != j.ID {
		t.Fatalf("version %+v %q %v", v, md, err)
	}
	args, _ := os.ReadFile(filepath.Join(e.dir, "args"))
	for _, want := range []string{"stream-json", "dontAsk", "Read,Glob,Grep", "--no-session-persistence"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("argv %s lacks %s", args, want)
		}
	}
	for _, bad := range []string{"dangerously", "bypassPermissions", "--settings", "--bare"} {
		if strings.Contains(string(args), bad) {
			t.Fatalf("argv has %s", bad)
		}
	}
	if env, _ := os.ReadFile(filepath.Join(e.dir, "env")); !strings.Contains(string(env), "cfg=/tmp/prof-x") {
		t.Fatalf("profile env %s", env)
	}
	// Stop keeps the text that arrived → incomplete version.
	t.Setenv("FAKE_MODE", "partial")
	doc2, vid2, j2, err := d.Generate(pid, DocRequest{DocumentID: doc.ID, Effort: "balanced", Agent: "claude", Cwd: e.dir, Command: e.fake})
	if err != nil || doc2.ID != doc.ID || len(doc2.Versions) != 2 || doc2.Versions[0].ID != vid2 {
		t.Fatalf("regenerate %+v %v", doc2, err)
	}
	for i := 0; i < 100 && len(e.events.of("jobOutput")) < 3; i++ {
		time.Sleep(30 * time.Millisecond)
	}
	if c, _ := e.jm.Cancel(j2.ID); c.State != jobs.StateCancelled {
		t.Fatalf("cancel %+v", c)
	}
	e.wait(j2.ID)
	v2, md2, err := d.Version(doc.ID, vid2)
	if err != nil || v2.State != "incomplete" || !strings.Contains(md2, "Draft so far") {
		t.Fatalf("partial %+v %q %v", v2, md2, err)
	}
	if len(e.events.of("document")) < 4 {
		t.Fatalf("document events %d", len(e.events.of("document")))
	}
	// Stats: median of complete runs per effort.
	st := d.Stats("local", "claude")
	if st["quick"].Runs != 1 || st["quick"].MedianMs == nil || st["balanced"].Runs != 0 || st["balanced"].MedianMs != nil {
		t.Fatalf("stats %+v", st)
	}
	// validation
	if _, _, _, err := d.Generate(pid, DocRequest{Type: "custom", Effort: "quick", Agent: "claude"}); err != ErrBadAsk {
		t.Fatalf("custom without ask: %v", err)
	}
	if _, _, _, err := d.Generate(pid, DocRequest{Type: "poem", Effort: "quick", Agent: "claude"}); err != ErrBadDocType {
		t.Fatalf("bad type: %v", err)
	}
	// delete version, then document (permanent)
	if err := d.DeleteVersion(doc.ID, vid); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(doc.ID); err != nil || len(d.List(pid)) != 0 {
		t.Fatalf("delete %v %v", err, d.List(pid))
	}
	if _, err := os.Stat(e.st.Path("documents", pid, doc.ID)); !os.IsNotExist(err) {
		t.Fatal("version files left")
	}
}

func TestDocumentsMedianAndRecover(t *testing.T) {
	e := newEnv(t)
	d := NewDocuments(e.st)
	pid := "p_median0001"
	var vs []Version
	for i, ms := range []int64{100, 900, 300, 500, 700, 99999} {
		vs = append(vs, Version{ID: fmt.Sprintf("v_%07d", i), At: time.Now().Add(time.Duration(i) * time.Second), Agent: "claude",
			MachineID: "local", Effort: "balanced", State: "complete", DurationMs: ms})
	}
	vs = append(vs, Version{ID: "v_running01", At: time.Now(), Effort: "balanced", State: "running", MachineID: "local", Agent: "claude"})
	if err := d.save(pid, []Document{{ID: "doc_aaaaaa1", ProjectID: pid, Type: "overview", Versions: vs}}); err != nil {
		t.Fatal(err)
	}
	// last 5 complete runs: 900,300,500,700,99999 → median 700
	if st := d.Stats("local", "claude")["balanced"]; st.Runs != 5 || *st.MedianMs != 700 {
		t.Fatalf("median %+v", st)
	}
	d.Recover()
	doc, _ := d.Get("doc_aaaaaa1")
	for _, v := range doc.Versions {
		if v.State == "running" {
			t.Fatal("running version survived restart")
		}
	}
}

func diagramJSON(nodes, groups int) string {
	d := Diagram{Schema: DiagramSchemaID, Title: "Request lifecycle", Direction: "TB"}
	for g := 0; g < groups; g++ {
		d.Groups = append(d.Groups, DGroup{ID: fmt.Sprintf("g%d", g), Label: fmt.Sprintf("Group %d", g)})
	}
	for i := 0; i < nodes; i++ {
		n := DNode{ID: fmt.Sprintf("n%d", i), Label: fmt.Sprintf("Node %d", i), Shape: "box"}
		if groups > 0 {
			n.Group = fmt.Sprintf("g%d", i*groups/nodes)
		}
		if i == 1 {
			n.Shape, n.Note = "diamond", "decide"
		}
		d.Nodes = append(d.Nodes, n)
		if i > 0 {
			d.Edges = append(d.Edges, DEdge{From: fmt.Sprintf("n%d", i-1), To: fmt.Sprintf("n%d", i), Label: "calls"})
		}
	}
	if nodes > 3 {
		d.Edges = append(d.Edges, DEdge{From: "n1", To: "n3", Style: "dashed"}) // unlabeled branch of a decision
	}
	b, _ := json.Marshal(d)
	return string(b)
}

func TestValidateDiagram(t *testing.T) {
	d, warn, err := ValidateDiagram([]byte(diagramJSON(6, 2)))
	if err != nil || len(d.Nodes) != 6 || len(warn) != 1 || !strings.Contains(warn[0], "n1→n3") {
		t.Fatalf("%v %v %v", d, warn, err)
	}
	for name, raw := range map[string]string{
		"41 nodes":     diagramJSON(41, 0),
		"9 groups":     diagramJSON(9, 9),
		"bad id":       `{"nodes":[{"id":"Bad ID","label":"x"}]}`,
		"long label":   `{"nodes":[{"id":"a","label":"` + strings.Repeat("x", 49) + `"}]}`,
		"long note":    `{"nodes":[{"id":"a","label":"x","note":"` + strings.Repeat("y", 81) + `"}]}`,
		"dangling":     `{"nodes":[{"id":"a","label":"x"}],"edges":[{"from":"a","to":"b"}]}`,
		"unknown grp":  `{"nodes":[{"id":"a","label":"x","group":"zz"}]}`,
		"bad shape":    `{"nodes":[{"id":"a","label":"x","shape":"hexagon"}]}`,
		"no nodes":     `{"nodes":[]}`,
		"wrong schema": `{"schema":"other/v2","nodes":[{"id":"a","label":"x"}]}`,
		"duplicate":    `{"nodes":[{"id":"a","label":"x"},{"id":"a","label":"y"}]}`,
		"not json":     `hello`,
	} {
		if _, _, err := ValidateDiagram([]byte(raw)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// lenient normalisation: pill → round, LR kept, empty optional fields
	d, _, err = ValidateDiagram([]byte(`{"title":"t","direction":"lr","nodes":[{"id":"a","label":"A","shape":"pill","note":"","group":""}],"edges":[{"from":"a","to":"a","label":"","style":""}],"groups":[]}`))
	if err != nil || d.Nodes[0].Shape != "round" || d.Direction != "LR" || d.Edges[0].Style != "solid" || d.Schema != DiagramSchemaID {
		t.Fatalf("%+v %v", d, err)
	}
}

// TestDiagramRetryAndLanding: an over-limit answer is rejected and retried
// once; the valid diagram lands on a board as shape/line/text nodes that
// all carry diagramId/diagramKey.
func TestDiagramRetryAndLanding(t *testing.T) {
	e := newEnv(t)
	g := NewDiagrams(e.st)
	g.Submit, g.Publish = e.jm.SubmitRunner, e.events.add
	big := filepath.Join(e.dir, "big.json")
	good := filepath.Join(e.dir, "good.json")
	_ = os.WriteFile(big, []byte(diagramJSON(41, 0)), 0o600)
	_ = os.WriteFile(good, []byte(diagramJSON(8, 3)), 0o600)
	count := filepath.Join(e.dir, "count")
	t.Setenv("FAKE_MODE", "diagram")
	t.Setenv("FAKE_COUNT", count)
	t.Setenv("FAKE_BIG_FIRST", big)
	t.Setenv("FAKE_DIAGRAM", good)
	pid := "p_diagram001"
	rec, j, err := g.Generate(pid, DiagramRequest{Subject: "architecture", Effort: "balanced", Agent: "claude", Cwd: e.dir, Command: e.fake})
	if err != nil || rec.State != "running" {
		t.Fatalf("%+v %v", rec, err)
	}
	if j = e.wait(j.ID); j.State != jobs.StateDone {
		t.Fatalf("job %+v", j)
	}
	if b, _ := os.ReadFile(count); strings.Count(string(b), "x") != 2 {
		t.Fatalf("runs %q (want one retry)", b)
	}
	got, err := g.Get(rec.ID)
	if err != nil || got.State != "done" || got.Diagram == nil || len(got.Diagram.Nodes) != 8 || got.Title != "Request lifecycle" || len(got.Warnings) != 1 {
		t.Fatalf("record %+v %v", got, err)
	}
	if ev := e.events.of("diagram"); len(ev) != 1 || ev[0]["state"] != "done" {
		t.Fatalf("events %v", ev)
	}
	if l := g.List(pid); len(l) != 1 {
		t.Fatalf("list %v", l)
	}
	// Always over the limits → bad_output after the retry.
	_ = os.Remove(count)
	t.Setenv("FAKE_DIAGRAM", big)
	rec2, j2, _ := g.Generate(pid, DiagramRequest{Subject: "custom", Ask: "the auth flow", Effort: "quick", Agent: "claude", Cwd: e.dir, Command: e.fake})
	if j2 = e.wait(j2.ID); j2.State != jobs.StateFailed || j2.Code != "bad_output" {
		t.Fatalf("over limit %+v", j2)
	}
	if r2, _ := g.Get(rec2.ID); r2.State != "failed" {
		t.Fatalf("record %+v", r2)
	}

	// Landing on a board.
	bm := board.NewManager(e.st)
	defer bm.Close()
	ops := Land(got.Diagram, "color", got.ID, 100, 200, Look{})
	if _, err := bm.Apply("w_landing01", "", ops); err != nil {
		t.Fatal(err)
	}
	b, _ := bm.Get("w_landing01")
	kinds := map[string]int{}
	keys := map[string]bool{}
	for _, n := range b.Nodes {
		kinds[n.Kind]++
		var data map[string]any
		_ = json.Unmarshal(n.Data, &data)
		if data["diagramId"] != got.ID || data["diagramKey"] == "" {
			t.Fatalf("node without diagram ids: %s", n.Data)
		}
		keys[data["diagramKey"].(string)] = true
		if n.Kind == "line" && data["arrowEnd"] != true {
			t.Fatalf("line without arrow %s", n.Data)
		}
	}
	// 8 node shapes + 3 zones = 11 shapes; 8 edges → 8 lines; labels:
	// 7 edge labels + 3 zone labels + title = 11 texts
	if kinds["shape"] != 11 || kinds["line"] != 8 || kinds["text"] != 11 {
		t.Fatalf("kinds %v", kinds)
	}
	if !keys["node:n0"] || !keys["title"] || !keys["group:g0"] {
		t.Fatalf("keys %v", keys)
	}
	// dashed edge → dashed line; diamond node → diamond shape; zones dashed
	var dashed, diamond, zones int
	for _, n := range b.Nodes {
		var data map[string]any
		_ = json.Unmarshal(n.Data, &data)
		if n.Kind == "line" && data["strokeStyle"] == "dashed" {
			dashed++
		}
		if data["shape"] == "diamond" {
			diamond++
		}
		if k, _ := data["diagramKey"].(string); strings.HasPrefix(k, "group:") && data["strokeStyle"] == "dashed" {
			zones++
		}
		if n.X < 100 || n.Y < 200 {
			t.Fatalf("node placed before origin: %+v", n)
		}
	}
	if dashed != 1 || diamond != 1 || zones != 3 {
		t.Fatalf("dashed %d diamond %d zones %d", dashed, diamond, zones)
	}
	// layers go top to bottom along a chain
	var y0, y7 float64
	for _, n := range b.Nodes {
		var data map[string]any
		_ = json.Unmarshal(n.Data, &data)
		switch data["diagramKey"] {
		case "node:n0":
			y0 = n.Y
		case "node:n7":
			y7 = n.Y
		}
	}
	if y7 <= y0 {
		t.Fatalf("TB layout: n0 y=%v n7 y=%v", y0, y7)
	}
	// LR and a cycle lay out too.
	cyc, _, _ := ValidateDiagram([]byte(`{"direction":"LR","nodes":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"edges":[{"from":"a","to":"b"},{"from":"b","to":"a"}]}`))
	if _, err := bm.Apply("w_landing02", "", Land(cyc, "ink", "dg_cycle0001", 0, 0, Look{})); err != nil {
		t.Fatal(err)
	}
}

func TestTreeTrashRenameAndOutside(t *testing.T) {
	ctx := context.Background()
	h := machine.NewLocal(machine.Config{})
	root := t.TempDir()
	mk := func(rel, body string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	mk("README.md", "# x")
	mk("src/main.go", "package main")
	mk("src/util/str.go", "package util")
	mk("node_modules/x/index.js", "x")
	mk(".env", "SECRET=1")
	mk(".gitignore", "x")
	mk("logo.png", "png")
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	tr, err := Tree(ctx, h, root, false)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range tr.Entries {
		names = append(names, e.Name)
		if e.Name == "logo.png" && !e.Binary {
			t.Fatal("png not binary")
		}
	}
	if strings.Join(names, ",") != "src,.gitignore,logo.png,README.md" || !strings.Contains(strings.Join(tr.Pruned, ","), "node_modules") ||
		!strings.Contains(strings.Join(tr.Pruned, ","), ".env") {
		t.Fatalf("tree %v pruned %v", names, tr.Pruned)
	}
	all, _ := Tree(ctx, h, root, true)
	if len(all.Entries) != 7 || len(all.Pruned) != 0 {
		t.Fatalf("hidden=1 %+v", all)
	}
	found, _, err := Find(ctx, h, root, "util go", 10)
	if err != nil || len(found) != 1 || found[0].Rel != "src/util/str.go" {
		t.Fatalf("find %+v %v", found, err)
	}
	if f, _, _ := Find(ctx, h, root, "index", 10); len(f) != 0 {
		t.Fatalf("find entered node_modules: %+v", f)
	}
	// rename never overwrites
	if _, err := machine.Rename(ctx, h, filepath.Join(root, "README.md"), filepath.Join(root, ".gitignore")); err != machine.ErrExists {
		t.Fatalf("rename over existing: %v", err)
	}
	if st, err := machine.Rename(ctx, h, filepath.Join(root, "README.md"), filepath.Join(root, "READ.md")); err != nil || st.Name != "READ.md" {
		t.Fatalf("rename %+v %v", st, err)
	}
	// trash: git repo → .git/nextboard-trash/<ts>/<rel>
	now := time.Date(2026, 10, 28, 14, 15, 0, 0, time.UTC)
	to, err := MoveToTrash(ctx, h, root, filepath.Join(root, "src", "util"), now)
	if err != nil || to != filepath.Join(root, ".git", "nextboard-trash", "20261028-141500", "src", "util") {
		t.Fatalf("trash %q %v", to, err)
	}
	if _, err := os.Stat(filepath.Join(to, "str.go")); err != nil {
		t.Fatal("trashed content missing")
	}
	if _, err := MoveToTrash(ctx, h, root, "/etc/hostname", now); err != ErrOutsideProject {
		t.Fatalf("outside: %v", err)
	}
	if _, err := MoveToTrash(ctx, h, root, root, now); err != ErrTrashRoot {
		t.Fatalf("root: %v", err)
	}
	// not a git repo → .nextboard-trash
	plain := t.TempDir()
	_ = os.WriteFile(filepath.Join(plain, "a.txt"), []byte("a"), 0o644)
	if to, err := MoveToTrash(ctx, h, plain, filepath.Join(plain, "a.txt"), now); err != nil || !strings.Contains(to, "/.nextboard-trash/20261028-141500/a.txt") {
		t.Fatalf("plain trash %q %v", to, err)
	}
	// symlink leaving the project is "outside"
	other := t.TempDir()
	_ = os.WriteFile(filepath.Join(other, "x.txt"), []byte("x"), 0o644)
	_ = os.Symlink(filepath.Join(other, "x.txt"), filepath.Join(root, "link.txt"))
	if out, err := OutsideProject(ctx, h, root, filepath.Join(root, "link.txt")); err != nil || !out {
		t.Fatalf("symlink outside %v %v", out, err)
	}
	if out, _ := OutsideProject(ctx, h, root, filepath.Join(root, "src", "main.go")); out {
		t.Fatal("inside reported outside")
	}
	if out, _ := OutsideProject(ctx, h, root, filepath.Join(root, "new", "file.go")); out {
		t.Fatal("new file reported outside")
	}
	// stat-batch keeps order and reports missing files
	sb, err := StatBatch(ctx, h, []string{filepath.Join(root, "READ.md"), filepath.Join(root, "nope"), "relative"})
	if err != nil || !sb[0].Exists || sb[1].Exists || sb[2].Error == "" {
		t.Fatalf("stat-batch %+v %v", sb, err)
	}
	if _, err := StatBatch(ctx, h, make([]string, 201)); err != ErrTooManyPaths {
		t.Fatal("stat-batch limit")
	}
}

func writeVault(t *testing.T) string {
	root := t.TempDir()
	files := map[string]string{
		"README.md":                  "# Home\n\nSee [[Guide]] and [[guide|the guide#Setup]] and [arch](docs/architecture.md#top).\n```\n[[NotALink]]\n```\nAlso `[[inline]]` and [web](https://x.y/z.md).",
		"docs/Guide.md":              "---\ntitle: The Guide\n---\nBack to [[README]]. Missing [[Nowhere]].",
		"docs/architecture.md":       "# Architecture\n\n[guide](./Guide.md) and [up](../README.md)",
		"notes/orphan.markdown":      "no links here",
		"node_modules/pkg/README.md": "# vendored",
		".hidden/secret.md":          "# hidden",
		"docs/big.mdx":               "# MDX\n[[Guide]]",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestMarkdownVaultGraphBacklinks(t *testing.T) {
	ctx := context.Background()
	h := machine.NewLocal(machine.Config{})
	root := writeVault(t)
	mem := filepath.Join(t.TempDir(), "memory")
	_ = os.MkdirAll(mem, 0o700)
	_ = os.WriteFile(filepath.Join(mem, "MEMORY.md"), []byte("# Memory index"), 0o600)
	_ = os.WriteFile(filepath.Join(mem, "a-fact.md"), []byte("---\ntitle: A fact\n---\nbody"), 0o600)
	vs := NewVaults()
	v, err := vs.Index(ctx, h, root, mem, false)
	if err != nil {
		t.Fatal(err)
	}
	rels := []string{}
	for _, f := range v.Files {
		rels = append(rels, f.Rel)
	}
	if strings.Join(rels, ",") != "README.md,docs/Guide.md,docs/architecture.md,docs/big.mdx,notes/orphan.markdown" {
		t.Fatalf("files %v", rels)
	}
	titles := map[string]string{}
	for _, f := range v.Files {
		titles[f.Rel] = f.Title
	}
	if titles["README.md"] != "Home" || titles["docs/Guide.md"] != "The Guide" || titles["notes/orphan.markdown"] != "orphan" {
		t.Fatalf("titles %v", titles)
	}
	type lk struct{ from, to, kind string }
	got := map[lk]bool{}
	for _, l := range v.Links {
		got[lk{l.From, l.To, l.Kind}] = l.Resolved
	}
	want := map[lk]bool{
		{"README.md", "docs/Guide.md", "wiki"}:          true, // [[Guide]] and [[guide|…#Setup]] (case-insensitive)
		{"README.md", "docs/architecture.md", "md"}:     true,
		{"docs/Guide.md", "README.md", "wiki"}:          true,
		{"docs/Guide.md", "Nowhere", "wiki"}:            false,
		{"docs/architecture.md", "docs/Guide.md", "md"}: true,
		{"docs/architecture.md", "README.md", "md"}:     true,
		{"docs/big.mdx", "docs/Guide.md", "wiki"}:       true,
	}
	for k, res := range want {
		if r, ok := got[k]; !ok || r != res {
			t.Fatalf("link %+v: got %v present %v; all %+v", k, r, ok, v.Links)
		}
	}
	for _, l := range v.Links {
		if strings.Contains(l.To, "NotALink") || strings.Contains(l.To, "inline") || strings.Contains(l.To, "https") {
			t.Fatalf("link from code or web: %+v", l)
		}
		if l.Kind == "wiki" && l.From == "README.md" && l.Heading == "Setup" && l.To != "docs/Guide.md" {
			t.Fatal("alias/heading not resolved")
		}
	}
	if v.AgentMemory == nil || len(v.AgentMemory.Files) != 2 || v.AgentMemory.Files[0].Rel != "MEMORY.md" || v.AgentMemory.Files[1].Title != "A fact" {
		t.Fatalf("memory %+v", v.AgentMemory)
	}
	g := BuildGraph(v)
	var orphan GraphNode
	for _, n := range g.Nodes {
		if n.ID == "notes/orphan.markdown" {
			orphan = n
		}
	}
	if len(g.Nodes) != 5 || !orphan.Orphan || g.Unresolved != 1 {
		t.Fatalf("graph %+v", g)
	}
	for _, e := range g.Edges {
		if e.From == "README.md" && e.To == "docs/Guide.md" && e.Kind == "wiki" && e.Count != 2 {
			t.Fatalf("dedupe count %+v", e)
		}
	}
	in, out, err := Backlinks(v, "docs/Guide.md")
	if err != nil || len(in) != 4 || len(out) != 2 {
		t.Fatalf("backlinks in=%+v out=%+v %v", in, out, err)
	}
	// Cache: an unchanged file is not re-read; a changed one is.
	_ = os.WriteFile(filepath.Join(root, "notes/orphan.markdown"), []byte("now [[README]]"), 0o644)
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(filepath.Join(root, "notes/orphan.markdown"), future, future)
	v2, _ := vs.Index(ctx, h, root, "", false)
	if g2 := BuildGraph(v2); g2.Nodes[4].Orphan {
		t.Fatalf("change not picked up %+v", g2.Nodes)
	}
	// File cap → truncated.
	old := MDMaxFiles
	MDMaxFiles = 2
	defer func() { MDMaxFiles = old }()
	if v3, _ := vs.Index(ctx, h, root, "", true); !v3.Truncated || len(v3.Files) != 2 {
		t.Fatalf("cap %+v", v3)
	}
}

func TestWatcherPolling(t *testing.T) {
	h := machine.NewLocal(machine.Config{})
	dir := t.TempDir()
	f := filepath.Join(dir, "note.md")
	_ = os.WriteFile(f, []byte("v1"), 0o644)
	ev := &evlog{}
	w := NewWatcher()
	w.Interval = 100 * time.Millisecond
	w.Host = func(string) (machine.Host, bool) { return h, true }
	w.Publish = ev.add
	w.Start()
	defer w.Close()
	id, stats, err := w.Add(context.Background(), "local", []string{f, filepath.Join(dir, "later.md")}, 2*time.Second)
	if err != nil || len(stats) != 2 || !stats[0].Exists || stats[1].Exists {
		t.Fatalf("add %v %+v", err, stats)
	}
	time.Sleep(300 * time.Millisecond)
	if len(ev.of("fsChanged")) != 0 {
		t.Fatal("event without change")
	}
	// an agent edits the file: the change shows up well within 3 s
	start := time.Now()
	_ = os.WriteFile(f, []byte("version two"), 0o644)
	seen := func(path string, size int64) bool {
		for _, e := range ev.of("fsChanged") {
			if e["watchId"] != id {
				t.Fatalf("foreign watch %v", e)
			}
			for _, c := range e["changes"].([]StatResult) {
				if c.Path == path && c.Exists && c.Size == size {
					return true
				}
			}
		}
		return false
	}
	for !seen(f, 11) && time.Since(start) < 3*time.Second {
		time.Sleep(20 * time.Millisecond)
	}
	if !seen(f, 11) {
		t.Fatalf("edit not reported: %v", ev.of("fsChanged"))
	}
	later := filepath.Join(dir, "later.md")
	_ = os.WriteFile(later, []byte("x"), 0o644)
	for !seen(later, 1) && time.Since(start) < 3*time.Second {
		time.Sleep(20 * time.Millisecond)
	}
	if !seen(later, 1) {
		t.Fatal("created file not reported")
	}
	// expiry: without renew the watch ends
	if err := w.Renew(id, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := w.Renew(id, time.Second); err != ErrWatchUnknown {
		t.Fatalf("expired watch renewed: %v", err)
	}
}
