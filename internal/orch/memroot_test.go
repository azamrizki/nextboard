package orch

import (
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/project"
)

// The memory runs of a project in several parts read it from its root; one part stays in its folder.
func TestMemoryRunsAtTheRootOfSeveralParts(t *testing.T) {
	one := project.Project{ID: "p_one", MachineID: "local", Path: "/src/app", Parts: []project.Part{{ID: "pt_a", Name: "app", MachineID: "local", Path: "/src/app"}}}
	req, mid := memoryRun(one, jobs.RunnerRequest{Purpose: "memory-scan"})
	if req.Root || req.Cwd != "/src/app" || req.MachineID != "local" || mid != "local" || partsNote(one) != "" {
		t.Fatalf("one part: %+v %s", req, mid)
	}
	two := project.Project{ID: "p_two", MachineID: "local", Path: "/src/api", Parts: []project.Part{
		{ID: "pt_a", Name: "api", MachineID: "local", Path: "/src/api"}, {ID: "pt_b", Name: "web", MachineID: "m_srv", Path: "/srv/web"}}}
	req, mid = memoryRun(two, jobs.RunnerRequest{Purpose: "memory-scan"})
	if !req.Root || req.Cwd != "" || req.MachineID != "" || mid != "local" {
		t.Fatalf("two parts: %+v %s", req, mid)
	}
	if n := partsNote(two); !strings.Contains(n, "api, web") || !strings.Contains(n, "Read every part") {
		t.Fatalf("note: %s", n)
	}
}
