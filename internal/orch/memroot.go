package orch

import (
	"fmt"
	"strings"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
)

// The memory of a project in several parts is the project's, so the runs that read the code for
// it (Init, Scan, Refresh) read every part: they run at the project root, as a root console does
// (docs/API-orchestration.md §12). A project in one part runs in its folder, as before.

// memoryRun places a memory run: at the root of a project in several parts, else in its folder.
// The machine is the one the CLI runs on, for picking it.
func memoryRun(p project.Project, req jobs.RunnerRequest) (jobs.RunnerRequest, string) {
	if len(p.Parts) > 1 {
		req.Root, req.MachineID, req.Cwd = true, "", ""
		return req, machine.LocalID
	}
	req.MachineID, req.Cwd = p.MachineID, p.Path
	return req, p.MachineID
}

// partsNote is what a memory run's prompt adds for a project in several parts: every part is
// read. Scan and Init then ask which part each fact holds for (partFactsNote).
func partsNote(p project.Project) string {
	if len(p.Parts) < 2 {
		return ""
	}
	names := make([]string, len(p.Parts))
	for i, pt := range p.Parts {
		names[i] = pt.Name
	}
	return fmt.Sprintf(`

This project has %d parts, each a folder of its own: %s. The current folder is the project root, with one entry per part; where each part is and how to read it is said above or in CLAUDE.md / AGENTS.md here. Read every part, not only the first.`,
		len(p.Parts), strings.Join(names, ", "))
}
