package server

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
)

// TestReadPlace: a studio run reads at the root only in a project of several parts and only when
// asked; then its CLI is picked on This PC. Otherwise it reads the part the ref names.
func TestReadPlace(t *testing.T) {
	one := project.Project{ID: "p_one000", MachineID: "m_far", Path: "/srv/a", Parts: []project.Part{{ID: "pt_a", Name: "a", MachineID: "m_far", Path: "/srv/a"}}}
	if seen, root, part, names := readPlace(one, true); root || part != "" || names != nil || seen.MachineID != "m_far" || seen.Path != "/srv/a" {
		t.Fatalf("one part: %v %v %q %v", seen.MachineID, root, part, names)
	}
	two := one
	two.Parts = append(slices.Clone(one.Parts), project.Part{ID: "pt_b", Name: "b", MachineID: machine.LocalID, Path: "/src/b"})
	seen, root, part, names := readPlace(two, true)
	if !root || part != "" || !slices.Equal(names, []string{"a", "b"}) || seen.MachineID != machine.LocalID || seen.Path != "" {
		t.Fatalf("root: %v %v %q %v", seen.MachineID, root, part, names)
	}
	// the ref named b (projectRef put its machine and folder on the project)
	two.MachineID, two.Path = machine.LocalID, "/src/b"
	if seen, root, part, names := readPlace(two, false); root || part != "pt_b" || names != nil || seen.Path != "/src/b" {
		t.Fatalf("part: %v %v %q %v", seen.Path, root, part, names)
	}
}

// TestStudioRunsAtTheRoot: in a project of two parts on This PC, a diagram or document asked for
// at the root runs in the root folder with both parts as --add-dir and a prompt that names them;
// one asked for in a part runs in its folder with the prompt it always had. The document's
// version says where it read.
func TestStudioRunsAtTheRoot(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	bin := t.TempDir()
	inner, log := filepath.Join(bin, "studio-cli"), filepath.Join(bin, "runs.log")
	if err := os.WriteFile(inner, []byte(fakeStudioCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	// records where it ran, its arguments and its prompt, then answers as the studio fake does
	spy := "#!/bin/sh\nprompt=$(cat)\n{ printf 'cwd=%s\\n' \"$PWD\"; for a in \"$@\"; do printf 'arg=%s\\n' \"$a\"; done; printf 'prompt=%s\\n<end>\\n' \"$prompt\"; } >> '" + log + "'\n" +
		"printf '%s' \"$prompt\" | exec '" + inner + "' \"$@\"\n"
	fake := filepath.Join(bin, "fake-claude")
	if err := os.WriteFile(fake, []byte(spy), 0o755); err != nil {
		t.Fatal(err)
	}
	if st, b := h.do("PATCH", "/api/settings", map[string]any{"agents": map[string]any{"paths": map[string]any{"local": map[string]any{"claude": fake}}}}, nil); st != 200 {
		t.Fatalf("settings %d %v", st, b)
	}
	api, web := t.TempDir(), t.TempDir()
	st, p := h.do("POST", "/api/projects", map[string]any{"machineId": "local", "path": api, "name": "Lumen", "partName": "api"}, nil)
	if st != 201 {
		t.Fatalf("project %d %v", st, p)
	}
	pid := p["id"].(string)
	st, p = h.do("POST", "/api/projects/"+pid+"/parts", map[string]any{"name": "web", "machineId": "local", "path": web}, nil)
	if st != 201 {
		t.Fatalf("part %d %v", st, p)
	}
	webID := p["parts"].([]any)[1].(map[string]any)["id"].(string)

	var mu sync.Mutex
	var sent []jobs.RunnerRequest
	for _, f := range []*func(jobs.RunnerRequest) (jobs.Job, error){&h.srv.studio.diagrams.Submit, &h.srv.studio.docs.Submit} {
		next := *f
		*f = func(req jobs.RunnerRequest) (jobs.Job, error) {
			mu.Lock()
			sent = append(sent, req)
			mu.Unlock()
			return next(req)
		}
	}
	last := func() jobs.RunnerRequest {
		mu.Lock()
		defer mu.Unlock()
		return sent[len(sent)-1]
	}
	run := func(ref, kind string, body map[string]any) (map[string]any, string) {
		t.Helper()
		_ = os.Remove(log)
		st, r := h.do("POST", "/api/projects/"+ref+"/"+kind, body, nil)
		if st != 202 {
			t.Fatalf("%s %s: %d %v", kind, ref, st, r)
		}
		if j := h.waitStudioJob(r["job"].(map[string]any)["id"].(string)); j["state"] != "done" {
			t.Fatalf("%s job %v", kind, j)
		}
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		return r, string(b)
	}
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	root := real(h.srv.roots.Dir(pid))
	note := "This project has 2 parts, each a folder of its own: api, web. The current folder is the project root"

	_, got := run(pid, "diagrams", map[string]any{"subject": "architecture", "effort": "quick", "agent": "claude", "root": true})
	if req := last(); !req.Root || req.MachineID != "" || req.Cwd != "" || !strings.Contains(req.Prompt, note) {
		t.Fatalf("diagram request: root %v machine %q cwd %q\n%s", req.Root, req.MachineID, req.Cwd, req.Prompt)
	}
	for _, want := range []string{"arg=--add-dir\narg=" + real(api) + "\n", "arg=--add-dir\narg=" + real(web) + "\n", "show which part each node lives in"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagram run without %q:\n%s", want, got)
		}
	}
	if cwd := strings.TrimPrefix(strings.SplitN(got, "\n", 2)[0], "cwd="); real(cwd) != root {
		t.Fatalf("diagram ran in %s, not the root %s", cwd, root)
	}

	// a part: its folder, no links, the prompt as before
	_, got = run(pid+project.PartKeySep+webID, "diagrams", map[string]any{"subject": "architecture", "effort": "quick", "agent": "claude"})
	if req := last(); req.Root || req.Cwd != web || strings.Contains(req.Prompt, "This project has") {
		t.Fatalf("part request: root %v cwd %q\n%s", req.Root, req.Cwd, req.Prompt)
	}
	if strings.Contains(got, "--add-dir") || real(strings.TrimPrefix(strings.SplitN(got, "\n", 2)[0], "cwd=")) != real(web) {
		t.Fatalf("part run:\n%s", got)
	}

	// documents: the same, and the version says where it read
	r, got := run(pid, "documents", map[string]any{"type": "overview", "effort": "quick", "agent": "claude", "root": true})
	if !strings.Contains(got, "say which part each component, file or command lives in") || !strings.Contains(got, "arg=--add-dir\narg="+real(web)+"\n") {
		t.Fatalf("document run:\n%s", got)
	}
	v := r["document"].(map[string]any)["versions"].([]any)[0].(map[string]any)
	if v["root"] != true || v["partId"] != nil || v["machineId"] != "local" {
		t.Fatalf("root version %v", v)
	}
	doc := r["document"].(map[string]any)["id"].(string)
	r, _ = run(pid+project.PartKeySep+webID, "documents", map[string]any{"effort": "quick", "agent": "claude", "documentId": doc})
	if req := last(); req.Root || req.Cwd != web || strings.Contains(req.Prompt, "This project has") {
		t.Fatalf("document part request: root %v cwd %q", req.Root, req.Cwd)
	}
	v = r["document"].(map[string]any)["versions"].([]any)[0].(map[string]any)
	if v["root"] != nil || v["partId"] != webID {
		t.Fatalf("part version %v", v)
	}
}
