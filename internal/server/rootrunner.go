package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

// submitRootRunner runs a hidden job of a project in several parts at its root (roots.go), the
// way a root console works: the parts on This PC through the root's links, the others (on a
// server, or read only) through the hub's read tools with a part argument. The memory runs use it
// (Init, Scan, Refresh) so that what they read is the whole project, not its first part. A
// project in one part runs in that part's folder, as before.
func (s *Server) submitRootRunner(req jobs.RunnerRequest) (jobs.Job, error) {
	req.Root = false
	pr, ok := s.projects.Get(req.ProjectID)
	if !ok {
		return jobs.Job{}, project.ErrNotFound
	}
	if len(pr.Parts) < 2 {
		pt := pr.DefaultPart()
		req.MachineID, req.Cwd = pt.MachineID, pt.Path
		return s.submitRunner(req)
	}
	dir, err := s.roots.Sync(pr.ID)
	if err != nil {
		return jobs.Job{}, err
	}
	req.MachineID, req.Cwd, req.AddDirs = machine.LocalID, dir, rootAddDirs(pr)
	var far []project.Part // the parts the root has no link to
	for _, pt := range pr.Parts {
		if pt.MachineID != machine.LocalID || pt.ReadOnly {
			far = append(far, pt)
		}
	}
	if len(far) == 0 || !jobs.ReadsProject(req.Purpose) {
		return s.submitRunner(req)
	}
	if !s.settings.View().Agents.RemoteTools || s.hookURL == "" {
		if len(req.AddDirs) == 0 {
			// no part can be read from the root: the run goes to its first part's folder, as before
			// root runs, where a server's own CLI reads it
			pt := pr.DefaultPart()
			req.MachineID, req.Cwd, req.AddDirs = pt.MachineID, pt.Path, nil
			req.Prompt += fmt.Sprintf("\n\nThe hub's tools for servers are off, so this run reads only the part %s: the current folder is that part, not the project root. Propose nothing about the other parts.", pt.Name)
			return s.submitRunner(req)
		}
		// the parts on This PC are read; the others cannot be reached: the run says so rather than guess
		names := make([]string, len(far))
		for i, pt := range far {
			names[i] = pt.Name
		}
		req.Prompt += fmt.Sprintf("\n\nThis run cannot read the parts %s (the hub's tools for servers are off). Propose nothing about them.", strings.Join(names, ", "))
		return s.submitRunner(req)
	}
	// the hub's tools need Claude Code, as a job on a server does (submitRemoteRunner)
	if a, ok := s.agents.Get("claude"); ok && s.agentOff(a) {
		return jobs.Job{}, session.ErrAgentDisabled
	}
	req.Agent, req.Command = "claude", s.settings.View().Agents.Paths[machine.LocalID]["claude"]
	req.Env = s.defaultProfileEnv("claude")
	if err := s.withRunnerProvider(&req); err != nil {
		return jobs.Job{}, err
	}
	target := remoteTarget{projectID: pr.ID, readOnly: true}
	s.withParts(&target, session.Spec{Root: true, ProjectID: pr.ID, MachineID: machine.LocalID})
	for i := range target.parts {
		target.parts[i].readOnly = true // a memory run only reads
	}
	if len(target.parts) == 0 {
		return jobs.Job{}, errors.New("the project's parts could not be read")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return jobs.Job{}, err
	}
	token := hex.EncodeToString(b)
	s.remoteJobs.Store(token, target)
	req.Env = append(req.Env, "NB_TOKEN="+token)
	var tools []string
	for _, x := range remoteToolList {
		if n := x["name"].(string); remoteReadTools[n] {
			tools = append(tools, "mcp__nextboard__"+n)
		}
	}
	req.Remote = &jobs.RemoteRunner{
		MCPConfig: agent.ClaudeMCPConfig(strings.TrimSuffix(s.hookURL, "/nb/hook")+"/nb/mcp-job", nil),
		Tools:     tools,
		Local:     true,
		Prompt:    s.rootRunnerPrompt(pr, far),
	}
	done := req.OnFinish
	req.OnFinish = func(j jobs.Job) {
		s.remoteJobs.Delete(token)
		if done != nil {
			done(j)
		}
	}
	j, err := s.jobs.SubmitRunner(req)
	if err != nil {
		s.remoteJobs.Delete(token)
	}
	return j, err
}

// rootRunnerPrompt tells a job at the root where each part is and how to read it.
func (s *Server) rootRunnerPrompt(pr project.Project, far []project.Part) string {
	var b strings.Builder
	b.WriteString("This project has several parts. The current folder is its root: one entry per part, named like the part.\n")
	for _, pt := range pr.Parts {
		farPart := false
		for _, f := range far {
			farPart = farPart || f.ID == pt.ID
		}
		if !farPart {
			fmt.Fprintf(&b, "- %s: the folder ./%s here (This PC, %s); read it with your own tools.\n", pt.Name, rootEntryName(pt), pt.Path)
			continue
		}
		where := "This PC"
		if pt.MachineID != machine.LocalID {
			where = pt.MachineID
			if h, ok := s.machines.Host(pt.MachineID); ok {
				where = "the server " + h.Info().Name
			}
		}
		fmt.Fprintf(&b, "- %s: on %s, in %s; read it only with the mcp__nextboard__ tools read_file, grep and glob, giving part: %q. Paths there start in that folder.\n", pt.Name, where, pt.Path, pt.Name)
	}
	b.WriteString("Read every part, not only the first.")
	return b.String()
}
