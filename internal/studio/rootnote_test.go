package studio

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/store"
)

// TestRootRuns: a diagram or document asked for at the root goes to the hub as a root run (no
// machine or folder of its own) with a prompt that names every part; one in a part keeps the
// prompt it always had. The document's version says where it read.
func TestRootRuns(t *testing.T) {
	if DiagramRootNote([]string{"api"}) != "" || DocRootNote(nil) != "" {
		t.Fatal("a note for fewer than two parts")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	var got []jobs.RunnerRequest
	submit := func(req jobs.RunnerRequest) (jobs.Job, error) {
		got = append(got, req)
		return jobs.Job{ID: "j_test0001", State: jobs.StateQueued}, nil
	}
	g, d := NewDiagrams(st), NewDocuments(st)
	g.Submit, d.Submit = submit, submit
	const pid = "p_rootrun1"
	parts := []string{"api", "web"}
	note := "This project has 2 parts, each a folder of its own: api, web."

	if _, _, err := g.Generate(pid, DiagramRequest{Subject: "architecture", Effort: "quick", Agent: "claude", MachineID: "local", Cwd: "/src/api", Root: true, Parts: parts}); err != nil {
		t.Fatal(err)
	}
	if r := got[0]; !r.Root || r.MachineID != "" || r.Cwd != "" || r.Prompt != DiagramPrompt("architecture", "", "quick")+"\n\n"+rootNote(parts, "show which part each node lives in: one group per part, or the part's name in the node's note.") {
		t.Fatalf("root diagram: %+v", r)
	}
	if _, _, err := g.Generate(pid, DiagramRequest{Subject: "architecture", Effort: "quick", Agent: "claude", MachineID: "local", Cwd: "/src/api", Parts: parts}); err != nil {
		t.Fatal(err)
	}
	if r := got[1]; r.Root || r.Cwd != "/src/api" || r.Prompt != DiagramPrompt("architecture", "", "quick") {
		t.Fatalf("part diagram: %+v", r)
	}

	doc, _, _, err := d.Generate(pid, DocRequest{Type: "overview", Effort: "quick", Agent: "claude", MachineID: "m_far", Cwd: "/srv/web", PartID: "pt_x", Root: true, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	if r := got[2]; !r.Root || r.MachineID != "" || r.Cwd != "" || !strings.HasPrefix(r.Prompt, DocPrompt("overview", "Project overview", "", "quick")+"\n"+note) {
		t.Fatalf("root document: %+v", r)
	}
	if v := doc.Versions[0]; !v.Root || v.PartID != "" || v.MachineID != "local" {
		t.Fatalf("root version %+v", v)
	}
	doc, _, _, err = d.Generate(pid, DocRequest{DocumentID: doc.ID, Effort: "quick", Agent: "claude", MachineID: "m_far", Cwd: "/srv/web", PartID: "pt_x"})
	if err != nil {
		t.Fatal(err)
	}
	if r := got[3]; r.Root || r.Cwd != "/srv/web" || r.Prompt != DocPrompt("overview", "Project overview", "", "quick") {
		t.Fatalf("part document: %+v", r)
	}
	if v := doc.Versions[0]; v.Root || v.PartID != "pt_x" || v.MachineID != "m_far" {
		t.Fatalf("part version %+v", v)
	}
}
