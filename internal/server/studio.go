package server

import (
	"context"
	"errors"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/profiles"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/skills"
	"github.com/azamrizki/nextboard/internal/studio"
	"github.com/azamrizki/nextboard/internal/transcript"
	"github.com/azamrizki/nextboard/internal/usage"
)

// Phase 7 part B wiring (docs/API-studio.md §5–§10): Documents, Diagrams,
// Code and Markdown studio helpers, the Skills manager and Usage.

// studioHub holds the services of the studios and usage.
type studioHub struct {
	docs     *studio.Documents
	diagrams *studio.Diagrams
	vaults   *studio.Vaults
	watcher  *studio.Watcher
	skills   *skills.Manager
	usage    *usage.Service
}

// setupStudio creates the services (no goroutines; startStudio starts
// them).
func (s *Server) setupStudio() {
	h := &studioHub{
		docs: studio.NewDocuments(s.st), diagrams: studio.NewDiagrams(s.st), vaults: studio.NewVaults(),
		watcher: studio.NewWatcher(), skills: skills.New(s.st),
	}
	h.docs.Submit, h.docs.Publish = s.submitRunner, s.events.Publish
	h.diagrams.Submit, h.diagrams.Publish = s.submitRunner, s.events.Publish
	h.watcher.Host, h.watcher.Publish = s.machines.Host, s.events.Publish
	h.usage = usage.New(s.st, s.jobs, usageHub{s}, nil)
	h.docs.Recover()
	h.diagrams.Recover()
	s.studio = h
	// Usage folders a crashed hub left behind are removed when their
	// machine connects (only paths recorded in usage/pending-cleanup.json).
	prev := s.machines.OnConnect
	s.machines.OnConnect = func(id string) {
		if prev != nil {
			prev(id)
		}
		h.usage.MachineConnected(id)
	}
}

// startStudio starts timers and pollers (Run).
func (s *Server) startStudio() {
	s.studio.watcher.Start()
	s.studio.usage.Start()
	s.studio.usage.MachineConnected(machine.LocalID)
}

func (s *Server) closeStudio() {
	if s.studio == nil {
		return
	}
	s.studio.watcher.Close()
	s.studio.usage.Close()
}

// routesStudio registers the Phase 7 part B endpoints.
func (s *Server) routesStudio(authed func(string, handler)) {
	authed("GET /api/projects/{id}/documents", s.handleDocuments)
	authed("POST /api/projects/{id}/documents", s.handleDocumentCreate)
	authed("GET /api/documents/stats", s.handleDocumentStats)
	authed("GET /api/documents/{docId}", s.handleDocumentGet)
	authed("DELETE /api/documents/{docId}", s.handleDocumentDelete)
	authed("GET /api/documents/{docId}/versions/{vid}", s.handleDocumentVersion)
	authed("DELETE /api/documents/{docId}/versions/{vid}", s.handleDocumentVersionDelete)

	authed("GET /api/projects/{id}/diagrams", s.handleDiagrams)
	authed("POST /api/projects/{id}/diagrams", s.handleDiagramCreate)
	authed("GET /api/diagrams/{id}", s.handleDiagramGet)
	authed("POST /api/diagrams/{id}/land", s.handleDiagramLand)

	authed("GET /api/machines/{id}/fs/tree", s.handleFSTree)
	authed("GET /api/machines/{id}/fs/find", s.handleFSFind)
	authed("POST /api/machines/{id}/fs/mkdir", s.handleFSMkdir)
	authed("POST /api/machines/{id}/fs/rename", s.handleFSRename)
	authed("POST /api/machines/{id}/fs/copy", s.handleFSCopy)
	authed("POST /api/machines/{id}/fs/trash", s.handleFSTrash)
	authed("POST /api/machines/{id}/fs/stat-batch", s.handleFSStatBatch)
	authed("POST /api/machines/{id}/fs/watch", s.handleFSWatch)
	authed("POST /api/fs/watch/{wid}/renew", s.handleFSWatchRenew)
	authed("DELETE /api/fs/watch/{wid}", s.handleFSWatchDelete)

	authed("GET /api/projects/{id}/markdown", s.handleMarkdown)
	authed("GET /api/projects/{id}/markdown/graph", s.handleMarkdownGraph)
	authed("GET /api/projects/{id}/markdown/backlinks", s.handleMarkdownBacklinks)

	authed("GET /api/machines/{id}/skills", s.handleSkills)
	authed("POST /api/machines/{id}/skills", s.handleSkillCreate)
	authed("GET /api/machines/{id}/skills/item", s.handleSkillItem)
	authed("PUT /api/machines/{id}/skills/item", s.handleSkillWrite)
	authed("DELETE /api/machines/{id}/skills/item", s.handleSkillDelete)
	authed("POST /api/machines/{id}/skills/lint", s.handleSkillLint)
	authed("POST /api/machines/{id}/skills/import", s.handleSkillImport)
	authed("POST /api/machines/{id}/skills/duplicate", s.handleSkillDuplicate)
	authed("GET /api/skills/starters", s.handleSkillStarters)
	authed("POST /api/sessions/{id}/skill", s.handleSessionSkill)

	authed("GET /api/usage", s.handleUsage)
	authed("POST /api/usage/check", s.handleUsageCheck)
	authed("GET /api/usage/tokens", s.handleUsageTokens)

	authed("GET /api/projects/{id}/files", s.handleFilesList)
	authed("POST /api/projects/{id}/files/search", s.handleFilesSearch)
	authed("POST /api/projects/{id}/files/replace", s.handleFilesReplace)
	authed("POST /api/projects/{id}/files/ignored", s.handleFilesIgnored)
	authed("POST /api/projects/{id}/files/on-save", s.handleFilesOnSave)
	authed("POST /api/projects/{id}/code/edit", s.handleCodeEdit)
	authed("GET /api/projects/{id}/lsp", s.handleLSPStatus)
	authed("GET /api/projects/{id}/lsp/{server}/ws", s.handleLSPSocket)
	authed("POST /api/projects/{id}/eslint", s.handleESLint)
	authed("POST /api/projects/{id}/code/complete", s.handleCodeComplete)
	authed("POST /api/projects/{id}/code/ask", s.handleCodeAsk)
	authed("GET /api/projects/{id}/dap", s.handleDAPStatus)
	authed("GET /api/projects/{id}/dap/{adapter}/ws", s.handleDAPSocket)
	authed("GET /api/extensions", s.handleExtensions)
	authed("GET /api/extensions/search", s.handleExtensionSearch)
	authed("POST /api/extensions", s.handleExtensionInstall)
	authed("DELETE /api/extensions/{extId}", s.handleExtensionDelete)
	authed("GET /api/extensions/{extId}/file", s.handleExtensionFile)
}

// ---- shared helpers ----

// runnerTarget is where a studio runner job runs.
type runnerTarget struct {
	agent, command string
	env            []string
	project        project.Project
}

var (
	errRunnerUnsupported = errors.New("the runner supports only claude and codex")
	errRunnerOff         = errors.New("Claude Code and Codex are turned off in Settings › Agents")
	errProfileMismatch   = errors.New("the profile does not match the machine/agent")
)

// runnerFor resolves agent (request → settings.runner.defaultAgent, Codex
// when only Codex is on the machine), profile (request → settings default
// profile → default login) and the path override for a project job.
func (s *Server) runnerFor(p project.Project, agentID, profileID string) (runnerTarget, error) {
	st := s.settings.Get()
	h, ok := s.machines.Host(p.MachineID)
	if !ok {
		return runnerTarget{}, machine.ErrNotFound
	}
	if a, ok := s.agents.Get(agentID); agentID == "" || (ok && s.agentOff(a)) {
		agentID = st.Runner.DefaultAgent
		if a, ok := s.agents.Get(agentID); !ok || !s.available(h, a) {
			if c, ok := s.agents.Get("codex"); ok && s.available(h, c) {
				agentID = "codex"
			}
		}
		if a, ok := s.agents.Get(agentID); ok && s.agentOff(a) {
			return runnerTarget{}, errRunnerOff
		}
	}
	if agentID != "claude" && agentID != "codex" {
		return runnerTarget{}, errRunnerUnsupported
	}
	t := runnerTarget{agent: agentID, command: st.Agents.Paths[p.MachineID][agentID], project: p}
	a, _ := s.agents.Get(agentID)
	explicit := profileID != ""
	if !explicit {
		profileID = st.Agents.Profiles[p.MachineID][agentID]
	}
	if profileID != "" {
		pf, ok := s.profiles.Get(profileID)
		switch {
		case ok && pf.MachineID == p.MachineID && pf.Agent == agentID && a.ProfileEnv != "":
			t.env = []string{a.ProfileEnv + "=" + pf.Dir}
		case explicit:
			return runnerTarget{}, errProfileMismatch
		}
	}
	return t, nil
}

// runnerErr maps job submission errors.
func runnerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRunnerUnsupported), errors.Is(err, errRunnerOff), errors.Is(err, jobs.ErrRunnerUnsupported):
		writeErrCode(w, http.StatusBadRequest, "runner_unsupported", err.Error())
	case errors.Is(err, errProfileMismatch):
		writeErrCode(w, http.StatusBadRequest, "profile_mismatch", err.Error())
	case errors.Is(err, jobs.ErrQueueFull):
		writeErrCode(w, http.StatusTooManyRequests, "queue_full", err.Error())
	case errors.Is(err, machine.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "machine_not_found", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// projectByID resolves {id} as a project.
func (s *Server) projectByID(w http.ResponseWriter, r *http.Request) (project.Project, bool) {
	p, ok := s.projects.Get(r.PathValue("id"))
	if !ok {
		writeErrCode(w, http.StatusNotFound, "project_not_found", project.ErrNotFound.Error())
	}
	return p, ok
}

// claudeCfg is the Claude config folder new consoles of a machine use:
// the default Claude profile of settings, else ~/.claude (absolute).
func (s *Server) claudeCfg(ctx context.Context, h machine.Host, profileID string) (string, error) {
	if profileID == "" {
		profileID = s.settings.Get().Agents.Profiles[h.ID()]["claude"]
	}
	if profileID != "" {
		if pf, ok := s.profiles.Get(profileID); ok && pf.MachineID == h.ID() && pf.Agent == "claude" {
			return path.Clean(pf.Dir), nil
		} else if !ok {
			return "", profiles.ErrNotFound
		}
	}
	st, err := h.Stat(ctx, "~/.claude")
	if err != nil {
		return "", err
	}
	return st.Path, nil
}

// remoteClaudeDirs lists the Claude config folders of an SSH machine
// (~/.claude and every Claude profile): writes there follow the confirm
// protocol (owner decision #3). nil for This PC.
func (s *Server) remoteClaudeDirs(ctx context.Context, h machine.Host) []string {
	if h.ID() == machine.LocalID {
		return nil
	}
	var out []string
	if st, err := h.Stat(ctx, "~/.claude"); err == nil {
		out = append(out, st.Path)
	}
	for _, pf := range s.profiles.List(h.ID()) {
		if pf.Agent == "claude" && strings.HasPrefix(pf.Dir, "/") {
			out = append(out, path.Clean(pf.Dir))
		}
	}
	return out
}

// confirmBody is the "confirm" field of remote config writes.
type confirmBody struct {
	Paths []string `json:"paths"`
}

// remoteConfigConfirm enforces §9.2 for generic file writes (fs/write,
// mkdir, rename, trash) that touch a Claude config folder on an SSH
// machine: the request must echo exactly the touched paths, else 428.
func (s *Server) remoteConfigConfirm(w http.ResponseWriter, r *http.Request, h machine.Host, touched []string, c *confirmBody) bool {
	dirs := s.remoteClaudeDirs(r.Context(), h)
	if len(dirs) == 0 {
		return true
	}
	var want []string
	for _, p := range touched {
		abs := p
		if st, err := h.Stat(r.Context(), p); err == nil {
			abs = st.Path
		}
		for _, d := range dirs {
			if studio.Inside(abs, d) {
				want = append(want, abs)
				break
			}
		}
	}
	if len(want) == 0 {
		return true
	}
	var given []string
	if c != nil {
		given = c.Paths
	}
	loc := skills.Loc{Remote: true, MachineName: h.Info().Name}
	if err := skills.Confirm(loc, skills.ScopePersonal, want, given); err != nil {
		writeConfirm(w, err.(*skills.ConfirmError))
		return false
	}
	return true
}

func writeConfirm(w http.ResponseWriter, e *skills.ConfirmError) {
	paths := e.Paths
	if paths == nil {
		paths = []string{}
	}
	sort.Strings(paths)
	writeJSON(w, http.StatusPreconditionRequired, map[string]any{"error": "confirmation required", "code": "confirm_required",
		"machine": e.Machine, "paths": paths})
}

// ---- usage adapter ----

type usageHub struct{ s *Server }

func (u usageHub) Host(id string) (machine.Host, bool) { return u.s.machines.Host(id) }
func (u usageHub) Publish(v any)                       { u.s.events.Publish(v) }
func (u usageHub) Settings() usage.Settings {
	st := u.s.settings.Get().Usage
	off := map[string]bool{}
	for a, on := range st.Agents {
		if !on {
			off[a] = true
		}
	}
	return usage.Settings{Background: st.Background, Disabled: off, ChimeEnabled: st.Chime.Enabled, ChimePlay: st.Chime.Play, ChimeSound: st.Chime.Sound}
}

// Profile: "" is what new consoles use, the default profile or else the CLI's
// default login (the usage pill has one chip per account); otherwise the
// profile must belong to machine and agent.
func (u usageHub) Profile(machineID, agentID, profileID string) (usage.Profile, error) {
	if profileID == "" {
		profileID = u.s.settings.Get().Agents.Profiles[machineID][agentID]
		if _, ok := u.s.profiles.Get(profileID); !ok {
			return usage.Profile{}, nil
		}
	}
	pf, ok := u.s.profiles.Get(profileID)
	a, aok := u.s.agents.Get(agentID)
	if !ok || !aok || pf.MachineID != machineID || pf.Agent != agentID || a.ProfileEnv == "" {
		return usage.Profile{}, errProfileMismatch
	}
	return usage.Profile{Env: a.ProfileEnv, Dir: pf.Dir}, nil
}

func (u usageHub) Command(machineID, agentID string) string {
	return u.s.settings.Get().Agents.Paths[machineID][agentID]
}

func (u usageHub) ProfileDirs(machineID string) map[string][]string {
	out := map[string][]string{}
	for _, pf := range u.s.profiles.List(machineID) {
		if strings.HasPrefix(pf.Dir, "/") && (pf.Agent == "claude" || pf.Agent == "codex") {
			out[pf.Agent] = append(out[pf.Agent], pf.Dir)
		}
	}
	return out
}

func (u usageHub) MachineIDs() []string {
	var out []string
	for _, in := range u.s.machines.List() {
		out = append(out, in.ID)
	}
	return out
}

// memoryDir is Claude's agent memory folder of a project:
// <cfg>/projects/<slug(project path)>/memory.
func (s *Server) memoryDir(ctx context.Context, p project.Project) string {
	h, ok := s.machines.Host(p.MachineID)
	if !ok {
		return ""
	}
	cfg, err := s.claudeCfg(ctx, h, "")
	if err != nil {
		return ""
	}
	return path.Join(cfg, "projects", transcript.ClaudeSlug(path.Clean(p.Path)), "memory")
}

// writeAllowed applies the Code studio rules to fs/write: with a project,
// a real path outside it needs allowOutside (403 outside_project);
// Claude config folders of SSH machines need the confirm protocol.
func (s *Server) writeAllowed(w http.ResponseWriter, r *http.Request, h machine.Host, p, projectID string, allowOutside bool, c *confirmBody) bool {
	if projectID != "" && !allowOutside {
		pr, ok := s.projects.Get(projectID)
		if !ok || pr.MachineID != h.ID() {
			writeErrCode(w, http.StatusNotFound, "project_not_found", "project not found on this machine")
			return false
		}
		out, err := studio.OutsideProject(r.Context(), h, pr.Path, p)
		if err != nil {
			fsErr(w, err)
			return false
		}
		if out {
			writeErrCode(w, http.StatusForbidden, "outside_project", "the real path of the file is outside the project; save with allowOutside")
			return false
		}
	}
	return s.remoteConfigConfirm(w, r, h, []string{p}, c)
}
