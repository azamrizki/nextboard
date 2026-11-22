package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/connlog"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/nextcode/native"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/profiles"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/transcript"
)

// Remote tools (docs/API-agents.md §21): an agent always runs on This PC, and
// when it works on a server the hub gives it read, write, edit and bash on
// that server over MCP. Login and memory stay on This PC; the server needs
// nothing but SSH. The endpoint lives on the loopback hook listener and only
// answers to the console's own token.

const (
	remotePromptMax    = 16 << 10 // CLAUDE.md of the server in the system prompt
	remoteOutputMax    = 30000    // characters of a tool answer
	remoteBashDefault  = 2 * time.Minute
	remoteBashMax      = 10 * time.Minute
	remoteReadLines    = 2000
	remoteRequestLimit = machine.MaxFileBytes + 1<<20
)

// remoteWorkDir is the local folder the CLI runs in for a folder on a server.
// It stays empty and stable, so the agent's memory follows the server folder.
func (s *Server) remoteWorkDir(machineID, cwd string) (string, error) {
	dir := s.remoteWorkPath(machineID, cwd)
	return dir, os.MkdirAll(dir, 0o700)
}

// remoteWorkPath is remoteWorkDir without making it: where the chats of that folder are.
func (s *Server) remoteWorkPath(machineID, cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return s.st.Path("agents", "remote", machineID, hex.EncodeToString(sum[:6]))
}

// launchContext is what the hub gives an agent's launch: the project's one
// memory, whichever CLI this is, and for work on a server the hub's tools.
func (s *Server) launchContext(sp session.Spec) (*agent.LaunchContext, error) {
	a, ok := s.agents.Get(sp.Agent)
	if !ok {
		return nil, session.ErrUnknownPreset
	}
	if !a.TakesContext() {
		if s.orch != nil {
			s.orch.MemoryForgotten(sp.ID) // nothing goes in at launch: it starts with nothing
		}
		return nil, nil
	}
	if a.Engine != "" {
		return s.nextcodeContext(sp, a)
	}
	if s.hookURL == "" {
		return nil, errors.New("the hook listener is not running yet")
	}
	claude := a.CapBase() == "claude"
	c := &agent.LaunchContext{MCPURL: strings.TrimSuffix(s.hookURL, "/nb/hook") + "/nb/mcp/" + sp.ID}
	c.MCP, c.MCPEnv = s.mcp.ForLaunch(sp.ProjectID, sp.RemoteMachineID != "")
	if claude && sp.MachineID == machine.LocalID {
		// Must exist: Claude Code refuses a folder that does not.
		c.AttachDir = s.st.Path("attachments")
		if err := os.MkdirAll(c.AttachDir, 0o700); err != nil {
			return nil, err
		}
	}
	var prompt strings.Builder
	pf, hasProfile := s.profiles.Get(sp.ProfileID)
	// hubConfig is the Claude config folder of this console when the hub made it (a profile it
	// made, an API provider's): only there does the hub record folder trust, never in the
	// user's own. trust records the hub's folder and, explicitly, the one the CLI runs in, so no
	// trust dialog shows whatever a CLI version does with a folder below a trusted one.
	hubConfig := ""
	if claude {
		switch {
		case sp.ProviderID != "":
			hubConfig = providerHub{s}.ConfigDir(sp.ProviderID, a)
		case hasProfile && pf.CreatedByHub:
			hubConfig = pf.Dir
		}
	}
	trust := func(what string, folders ...string) {
		if hubConfig == "" {
			return
		}
		for _, f := range folders {
			if err := profiles.TrustFolder(hubConfig, f); err != nil {
				log.Printf("trust %s folder: %v", what, err)
			}
		}
	}
	if sp.RemoteMachineID != "" {
		host, ok := s.machines.Host(sp.RemoteMachineID)
		if !ok {
			return nil, machine.ErrNotFound
		}
		c.Remote, c.WorkDir = true, sp.Cwd
		// The folder the CLI runs in is empty and the hub's: no trust dialog for it.
		trust("agent", s.st.Path("agents", "remote"), sp.Cwd)
		docs := []string{"AGENTS.md", "CLAUDE.md"}
		if claude {
			docs = []string{"CLAUDE.md", "AGENTS.md"}
		}
		extras := s.remoteExtras(host, sp.RemoteCwd, sp.Cwd, docs, a.CapBase())
		c.Commands = extras.commands
		name := host.Info().Name
		if sp.RemoteMachineID == machine.LocalID {
			name = "" // a read-only part on This PC, reached the same way
		}
		prompt.WriteString(remotePrompt(name, sp.RemoteCwd, s.remoteProjectRules(host, sp.RemoteCwd, docs), claude, extras))
	}
	if sp.Root {
		// At the project root (roots.go): the CLI's own tools in the root folder, whose links lead
		// to the parts on This PC; the hub's tools, with part, for the others.
		pr, ok := s.projects.Get(sp.ProjectID)
		if !ok {
			return nil, project.ErrNotFound
		}
		c.WorkDir, c.TrustDir, c.AddDirs = sp.Cwd, sp.Cwd, rootAddDirs(pr)
		// the root folders are the hub's: no trust dialog for them
		trust("root", s.st.Path("roots"), sp.Cwd)
	}
	if parts := s.partsPrompt(sp); parts != "" {
		if prompt.Len() > 0 {
			prompt.WriteString("\n")
		}
		prompt.WriteString(parts)
	}
	if sp.ProjectID != "" && s.orch != nil {
		dir, err := s.orch.Memory.Dir(sp.ProjectID)
		if err != nil {
			return nil, err
		}
		if claude {
			c.MemoryDir = dir
			// What this login learned before memory moved to the project.
			if hasProfile && pf.CreatedByHub {
				src := filepath.Join(pf.Dir, "projects", transcript.ClaudeSlug(sp.Cwd), "memory")
				if err := s.orch.Memory.Adopt(src, sp.ProjectID); err != nil {
					log.Printf("old memory %s: %v", src, err)
				}
			}
		}
		if prompt.Len() > 0 {
			prompt.WriteString("\n")
		}
		prompt.WriteString(s.orch.LaunchMemory(sp.ID, sp.WorkspaceID, sp.ProjectID, sp.PartID, claude))
	}
	if s.orch != nil {
		c.Blocked = s.blockedIn(sp.ProjectID, sp.PartID)
		if sp.Root {
			c.Blocked = s.rootBlocked(sp.ProjectID) // its shell can cd into any part
		}
		// Codex keeps its rules in its config folder: only in one the hub made, never the user's own.
		if a.CapBase() == "codex" && hasProfile && pf.CreatedByHub && !c.Remote {
			if err := agent.WriteCodexRules(pf.Dir, s.allBlocked()); err != nil {
				log.Printf("codex rules: %v", err)
			}
		}
	}
	if !c.Remote && sp.MachineID == machine.LocalID && agent.Sandboxes(a) {
		if pr, ok := s.projects.Get(sp.ProjectID); ok && pr.Sandbox != "" {
			c.Sandbox = &agent.Sandbox{Network: pr.Sandbox != project.SandboxOffline, Write: sandboxWrites()}
			rule := sandboxRule(c.Sandbox.Network)
			if sp.Root {
				// the folders behind the root's links, not the read-only parts
				for _, d := range c.AddDirs {
					if !slices.Contains(c.Sandbox.Write, d) {
						c.Sandbox.Write = append(c.Sandbox.Write, d)
					}
				}
				rule = rootSandboxRule(c.Sandbox.Network)
			}
			if prompt.Len() > 0 {
				prompt.WriteString("\n")
			}
			prompt.WriteString(rule + "\n")
		}
	}
	if !s.settings.View().Agents.CoAuthor {
		if prompt.Len() > 0 {
			prompt.WriteString("\n")
		}
		prompt.WriteString(agent.NoCoAuthorRule + "\n")
	}
	if c.Prompt = prompt.String(); c.Prompt != "" {
		c.PromptFile = s.st.Path("agents", "prompts", sp.ID+".md")
		if err := os.MkdirAll(filepath.Dir(c.PromptFile), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(c.PromptFile, []byte(c.Prompt), 0o600); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// nextcodeContext is the launch context of a NextCode console (docs/API-agents.md §30). NextCode
// is independent of NextBoard's other agents (owner, 2026-11-20): its own login folder and its
// own memory, and none of the hub's memory, launch prompt or MCP tools. It keeps what makes it
// work safely in the project: its folders, the commands that need approval, the project's
// sandbox, and the owner's rule on commit attribution.
func (s *Server) nextcodeContext(sp session.Spec, a agent.Agent) (*agent.LaunchContext, error) {
	if s.orch != nil {
		s.orch.MemoryForgotten(sp.ID) // the project's memory does not go in: NextCode has its own
	}
	// Clean NextCode: nothing of the CLI's own setup or the account's goes in, only NextCode's
	c := &agent.LaunchContext{NextCode: true}
	if err := s.ncKit.Ensure(); err != nil {
		return nil, err
	}
	if sp.MachineID == machine.LocalID {
		// Must exist: Claude Code refuses a folder that does not.
		c.AttachDir = s.st.Path("attachments")
		if err := os.MkdirAll(c.AttachDir, 0o700); err != nil {
			return nil, err
		}
	}
	var prompt strings.Builder
	if sp.RemoteMachineID != "" {
		// on a server (or a read-only part): the CLI runs on This PC in an empty folder of the
		// hub's and works there through the hub's tools; the server's ones only (handleRemoteMCP)
		if s.hookURL == "" {
			return nil, errors.New("the hook listener is not running yet")
		}
		host, ok := s.machines.Host(sp.RemoteMachineID)
		if !ok {
			return nil, machine.ErrNotFound
		}
		c.Remote, c.WorkDir = true, sp.Cwd
		c.MCPURL = strings.TrimSuffix(s.hookURL, "/nb/hook") + "/nb/mcp/" + sp.ID
		docs := []string{"CLAUDE.md", "AGENTS.md"}
		extras := s.remoteExtras(host, sp.RemoteCwd, sp.Cwd, docs, "claude")
		c.Commands = extras.commands
		name := host.Info().Name
		if sp.RemoteMachineID == machine.LocalID {
			name = "" // a read-only part on This PC, reached the same way
		}
		prompt.WriteString(remotePrompt(name, sp.RemoteCwd, s.remoteProjectRules(host, sp.RemoteCwd, docs), false, extras))
		prompt.WriteString("Your local Read, Write and Edit tools see only an empty folder on this computer. Never use them for project files.\n")
	}
	if sp.Root {
		pr, ok := s.projects.Get(sp.ProjectID)
		if !ok {
			return nil, project.ErrNotFound
		}
		c.WorkDir, c.TrustDir, c.AddDirs = sp.Cwd, sp.Cwd, rootAddDirs(pr)
	}
	if s.orch != nil {
		c.Blocked = s.blockedIn(sp.ProjectID, sp.PartID)
		if sp.Root {
			c.Blocked = s.rootBlocked(sp.ProjectID)
		}
	}
	if sp.MachineID == machine.LocalID && !c.Remote {
		if pr, ok := s.projects.Get(sp.ProjectID); ok && pr.Sandbox != "" {
			c.Sandbox = &agent.Sandbox{Network: pr.Sandbox != project.SandboxOffline, Write: sandboxWrites()}
			rule := sandboxRule(c.Sandbox.Network)
			if sp.Root {
				for _, d := range c.AddDirs {
					if !slices.Contains(c.Sandbox.Write, d) {
						c.Sandbox.Write = append(c.Sandbox.Write, d)
					}
				}
				rule = rootSandboxRule(c.Sandbox.Network)
			}
			prompt.WriteString(rule + "\n")
		}
	}
	if !s.settings.View().Agents.CoAuthor {
		prompt.WriteString(agent.NoCoAuthorRule + "\n")
	}
	// the hub's MCP server: NextCode's agent tool (its sub-agents), and a server's tools there
	if c.MCPURL == "" && s.hookURL != "" {
		c.MCPURL = strings.TrimSuffix(s.hookURL, "/nb/hook") + "/nb/mcp/" + sp.ID
	}
	// NextCode's skills and memory: files it reads and keeps with its own tools, on every engine
	prompt.WriteString("\n" + s.ncKit.Prompt(sp.ProjectID))
	c.AddDirs = append(c.AddDirs, s.ncKit.SkillsDir(), s.ncKit.MemoryDir())
	// its MCP servers: those Settings › NextCode gives it, none of the CLI's or the account's
	c.MCP, c.MCPEnv = s.nextcodeMCP(sp)
	c.Prompt = prompt.String()
	if a.CapBase() == "claude" {
		// Claude Code runs with NextCode's whole system prompt instead of its own; the others
		// add the context to theirs (NextCode's own agent has the same prompt already)
		dirs := append([]string{}, c.AddDirs...)
		if c.AttachDir != "" {
			dirs = append(dirs, c.AttachDir)
		}
		c.Prompt = native.SystemPrompt(sp.Cwd, dirs, c.Prompt)
	}
	if c.Prompt != "" {
		c.PromptFile = s.st.Path("agents", "prompts", sp.ID+".md")
		if err := os.MkdirAll(filepath.Dir(c.PromptFile), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(c.PromptFile, []byte(c.Prompt), 0o600); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// remoteProjectRules reads the first project instructions file on the server,
// "" when there is none.
func (s *Server) remoteProjectRules(host machine.Host, cwd string, names []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, name := range names {
		if c, err := host.ReadFile(ctx, remotePath(cwd, name), remotePromptMax); err == nil && !c.Binary {
			return c.Content
		}
	}
	return ""
}

func remotePrompt(machineName, cwd, rules string, localMemory bool, x serverExtras) string {
	var b strings.Builder
	if machineName == "" {
		fmt.Fprintf(&b, "You are working in the folder %s on this computer, which is read only for you. Your own file and shell tools are off for it.\n", cwd)
	} else {
		fmt.Fprintf(&b, "You are working on the server %q, in the folder %s. This CLI runs on another computer, so its local tools cannot see the project.\n", machineName, cwd)
	}
	b.WriteString("For everything in the project use the tools of the nextboard MCP server: read_file, write_file, edit_file, grep, glob and bash. Relative paths start in that folder. Each bash call starts there in a new shell, so `cd` does not carry over. A command that takes more than a minute runs with bash run_in_background, and bash_output shows what it prints as it goes.\n")
	if localMemory {
		b.WriteString("Your local Read, Write and Edit tools are only for your memory folder. Never use them for project files.\n")
	}
	if x.mirrored > 0 {
		b.WriteString("The main files of the project's skills and commands from the server are copied next to you, so they load as usual. Everything they name, their own other files and scripts too, is on the server: read it with read_file and run it with bash there, never on this computer.\n")
	}
	if len(x.readable) > 0 {
		b.WriteString("You cannot open those copies yourself. When one of these skills fits the task, read it on the server first with read_file, and follow it:\n")
		for _, k := range x.readable {
			b.WriteString("- " + k.name + ": " + k.path + "\n")
		}
	}
	if strings.TrimSpace(rules) != "" {
		b.WriteString("\nProject instructions from the server:\n\n" + rules + "\n")
	}
	if len(x.nested) > 0 {
		b.WriteString("\nThese subfolders have instructions of their own. Before you work in one, read its file with read_file and follow it there:\n")
		for _, p := range x.nested {
			b.WriteString("- " + p + "\n")
		}
	}
	return b.String()
}

func remotePath(cwd, p string) string {
	if strings.HasPrefix(p, "/") || p == "~" || strings.HasPrefix(p, "~/") {
		return p
	}
	return path.Join(cwd, p)
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// remoteTarget is what one token may reach: a server's files, the project's
// memory, or both.
type remoteTarget struct {
	// agents: NextCode's agent tool (a NextCode console, not one of its sub-agents)
	agents         bool
	machineID, cwd string
	readOnly       bool
	projectID      string
	memory         bool
	// a console's token also reads what the memory tiles connected to it add
	sessionID, workspaceID string
	// credentials: the owner shares vault items with agents of this project (credrun.go)
	credentials bool
	// homePart names the console's own part, parts are the project's others (remoteparts.go);
	// inPart is the other part one call works in.
	homePart, inPart     string
	homePartID, inPartID string
	parts                []partTarget
	// reviewKey: the review of the part the call works in (h_parts.go)
	reviewKey string
	// root: a console at the project root (roots.go), which names the part of every file call;
	// own: the call's part is one it changes with its own tools (partTarget.own)
	root, own bool
}

func (t remoteTarget) tools() []map[string]any {
	var list []map[string]any
	if t.machineID != "" || len(t.parts) > 0 {
		write := t.writable()
		for _, x := range remoteToolList {
			if !write && !remoteReadTools[x["name"].(string)] {
				continue
			}
			if len(t.parts) > 0 && x["name"] != "bash_output" && x["name"] != "bash_kill" {
				x = t.withPartArg(x)
			}
			list = append(list, x)
		}
	}
	if t.memory && t.projectID != "" {
		// parts are named only where the project has them: a project in one part keeps the
		// tools it had
		if len(t.parts) > 0 {
			list = append(list, memoryToolList...)
		} else {
			list = append(list, memoryToolsOnePart...)
		}
		// listed whether or not a mission runs: CLIs keep the list they got at start
		if t.sessionID != "" {
			list = append(list, missionToolList...)
			list = append(list, boardToolList...)
		}
	}
	if t.credentials && t.sessionID != "" && t.projectID != "" {
		list = append(list, credentialToolList...)
	}
	if t.agents && t.sessionID != "" {
		list = append(list, agentToolDef)
	}
	return list
}

// handleRemoteMCP serves MCP to a console. There is no event stream, so GET
// answers 405 as the protocol allows.
func (s *Server) handleRemoteMCP(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	sess, ok := s.sessions.Get(sid)
	// a NextCode sub-agent comes with a token of its own: the console's tools, no sub-agents
	sub := false
	if ok && !sess.HookTokenOK(token) {
		owner, found := s.subAgentSession(token)
		ok, sub = found && owner == sid, true
	}
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sp := sess.Spec()
	t := remoteTarget{machineID: sp.RemoteMachineID, cwd: sp.RemoteCwd, projectID: sp.ProjectID, memory: true, sessionID: sp.ID, workspaceID: sp.WorkspaceID,
		credentials: sp.ProjectID != "" && s.credVault != nil && s.credVault.HasAgentEntries(sp.ProjectID)}
	if sess.IsNextCode() {
		// independent of NextBoard's agents (docs/API-agents.md §30): the server's tools only,
		// and NextCode's agent tool for its sub-agents (not for a sub-agent itself)
		t.memory, t.credentials = false, false
		t.agents = !sub
	}
	s.withParts(&t, sp)
	if len(t.tools()) == 0 && !sess.IsNextCode() {
		http.Error(w, "this console does not use hub tools", http.StatusNotFound)
		return
	}
	s.serveRemoteRPC(w, r, t)
}

// handleRemoteJobMCP serves MCP to a hidden runner job, read-only, for as long
// as the job runs.
func (s *Server) handleRemoteJobMCP(w http.ResponseWriter, r *http.Request) {
	v, ok := s.remoteJobs.Load(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	t := v.(remoteTarget)
	if t.root {
		// a job at a project root reads the parts the project has now: one removed or split off
		// while it runs is out of its reach at once
		pr, _ := s.projects.Get(t.projectID)
		kept := make([]partTarget, 0, len(t.parts))
		for _, pt := range t.parts {
			if now, ok := pr.PartByID(pt.id); ok && now.MachineID == pt.machineID && now.Path == pt.cwd {
				kept = append(kept, pt)
			}
		}
		t.parts = kept
	}
	s.serveRemoteRPC(w, r, t)
}

func (s *Server) serveRemoteRPC(w http.ResponseWriter, r *http.Request, t remoteTarget) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, remoteRequestLimit))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	var req rpcRequest
	if json.Unmarshal(body, &req) != nil {
		http.Error(w, "not JSON-RPC", http.StatusBadRequest)
		return
	}
	if len(req.ID) == 0 { // a notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rpcErr := s.remoteRPC(r.Context(), t, req)
	out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rpcErr != nil {
		out["error"] = rpcErr
	} else {
		out["result"] = result
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) remoteRPC(ctx context.Context, t remoteTarget, req rpcRequest) (any, map[string]any) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		return map[string]any{"protocolVersion": p.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "nextboard", "version": "1"}}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := t.tools()
		if list == nil {
			list = []map[string]any{} // an array, also with none (a NextCode sub-agent on This PC)
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params"}
		}
		if p.Name == "agent" {
			if !t.agents {
				return remoteAnswer("this console does not have the tool agent: a sub-agent starts no sub-agents of its own", true), nil
			}
			return remoteAnswer(s.runSubAgents(ctx, t.sessionID, p.Arguments)), nil
		}
		if !t.memory && (strings.HasPrefix(p.Name, "memory_") || strings.HasPrefix(p.Name, "credential_") || missionTools[p.Name] || p.Name == "board_diagram") {
			return remoteAnswer("this console does not have the tool "+p.Name, true), nil
		}
		if strings.HasPrefix(p.Name, "memory_") {
			text, err := s.runMemoryTool(t, p.Name, p.Arguments)
			if err != nil {
				return remoteAnswer(err.Error(), true), nil
			}
			return remoteAnswer(text, false), nil
		}
		if strings.HasPrefix(p.Name, "credential_") {
			return remoteAnswer(s.credentialTool(ctx, t, p.Name, p.Arguments)), nil
		}
		if missionTools[p.Name] {
			text, err := s.runMissionTool(t, p.Name, p.Arguments)
			if err != nil {
				return remoteAnswer(err.Error(), true), nil
			}
			return remoteAnswer(text, false), nil
		}
		if p.Name == "board_diagram" {
			return remoteAnswer(s.boardDiagramTool(t, p.Arguments)), nil
		}
		// a command started in the background is the console's, wherever it runs
		if p.Name == "bash_output" || p.Name == "bash_kill" {
			return remoteAnswer(s.bgTool(ctx, nil, t, p.Name, p.Arguments)), nil
		}
		t, wrong := t.forPart(p.Arguments)
		if wrong != "" {
			return remoteAnswer(wrong, true), nil
		}
		if t.readOnly && !remoteReadTools[p.Name] {
			switch {
			case t.inPart != "":
				return remoteAnswer(fmt.Sprintf("%s is read only for you: read_file, grep and glob work there. Ask the agent of that part, or the owner, for the change.", t.inPart), true), nil
			case t.homePart != "" && t.sessionID != "":
				return remoteAnswer("Your part is read only: read_file, grep and glob work here. Say what should change instead.", true), nil
			}
			return remoteAnswer("This job may only read.", true), nil
		}
		if t.own && !remoteReadTools[p.Name] {
			return remoteAnswer(fmt.Sprintf("%s is on This PC: use your own tools in ./%s. The nextboard tools only read there.", t.inPart, rootEntryName(project.Part{ID: t.inPartID, Name: t.inPart})), true), nil
		}
		if t.machineID == "" {
			return remoteAnswer("This console works on This PC: use your own tools for files.", true), nil
		}
		host, ok := s.machines.Host(t.machineID)
		if !ok {
			return remoteAnswer("That server is no longer registered in NextBoard.", true), nil
		}
		if p.Name == "bash" && s.orch != nil {
			var a struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(p.Arguments, &a) == nil {
				if answer := s.blockedAnswer(t, a.Command); answer != "" {
					return remoteAnswer(answer, true), nil
				}
			}
		}
		var bg struct {
			Background bool `json:"run_in_background"`
		}
		_ = json.Unmarshal(p.Arguments, &bg)
		if p.Name == "bash" && bg.Background {
			return remoteAnswer(s.bgTool(ctx, host, t, p.Name, p.Arguments)), nil
		}
		start := time.Now()
		text, err := runRemoteTool(ctx, host, t.cwd, p.Name, p.Arguments)
		s.noteTool(host, p.Name, time.Since(start), err)
		if err != nil {
			return remoteAnswer(err.Error(), true), nil
		}
		s.noteRemoteChange(t, p.Name, p.Arguments)
		return remoteAnswer(text, false), nil
	}
	return nil, map[string]any{"code": -32601, "message": "method not found"}
}

// noteRemoteChange tells review what an agent's tool just did on the server: whose file it is,
// and that a look is due. Never in the way of the call itself: a look can take a while.
func (s *Server) noteRemoteChange(t remoteTarget, tool string, args json.RawMessage) {
	if t.projectID == "" || t.sessionID == "" || s.review == nil || t.review() == "" {
		return
	}
	switch tool {
	case "write_file", "edit_file":
		var a struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(args, &a) == nil && a.Path != "" {
			s.review.Hint(t.review(), t.sessionID, path.Clean(remotePath(t.cwd, a.Path)))
		}
	case "bash":
	default:
		return
	}
	go s.review.Soon(t.review())
}

// remoteSlowCall: a file call this long says something about the link. Commands
// may rightly take long.
const remoteSlowCall = 5 * time.Second

// noteTool records what a tool call says about the link, never its arguments or
// output: a command's own failure is not a link problem and its text stays out.
func (s *Server) noteTool(host machine.Host, tool string, took time.Duration, err error) {
	reason := linkFailure(err)
	if m, ok := host.(*machine.SSH); ok {
		m.NoteCall(reason != "")
	}
	e := connlog.Event{MachineID: host.ID(), Machine: host.Info().Name, Tool: tool, DurationMs: took.Milliseconds()}
	switch {
	case reason != "":
		e.Kind, e.Reason = connlog.ToolFailed, reason
	case tool != "bash" && took >= remoteSlowCall:
		e.Kind = connlog.ToolSlow
	default:
		return
	}
	s.connLog.Write(e)
}

func linkFailure(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, machine.ErrNotConnected):
		return "not_connected"
	case errors.Is(err, machine.ErrNoSFTP):
		return "no_sftp"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case machine.LinkLost(err):
		return "disconnected"
	}
	return ""
}

// submitRunner is the one way hidden jobs start. The CLI runs on This PC with
// the same login as the consoles; a job on a server's project reads it through
// the hub's tools.
func (s *Server) submitRunner(req jobs.RunnerRequest) (jobs.Job, error) {
	// Hidden runs too: turned off means the hub does not start it at all.
	if a, ok := s.agents.Get(req.Agent); ok && s.agentOff(a) {
		return jobs.Job{}, session.ErrAgentDisabled
	}
	if req.Root {
		return s.submitRootRunner(req)
	}
	if req.MachineID != "" && req.MachineID != machine.LocalID && s.settings.View().Agents.RemoteTools {
		return s.submitRemoteRunner(req)
	}
	if len(req.Env) == 0 && (req.MachineID == "" || req.MachineID == machine.LocalID) {
		req.Env = s.defaultProfileEnv(req.Agent)
	}
	if err := s.withRunnerProvider(&req); err != nil {
		return jobs.Job{}, err
	}
	return s.jobs.SubmitRunner(req)
}

func (s *Server) submitRemoteRunner(req jobs.RunnerRequest) (jobs.Job, error) {
	host, ok := s.machines.Host(req.MachineID)
	if !ok {
		return jobs.Job{}, machine.ErrNotFound
	}
	if s.hookURL == "" {
		return jobs.Job{}, errors.New("the hook listener is not running yet")
	}
	target := remoteTarget{machineID: req.MachineID, cwd: req.Cwd, readOnly: true}
	if req.Cwd != "" {
		dir, err := s.remoteWorkDir(req.MachineID, req.Cwd)
		if err != nil {
			return jobs.Job{}, err
		}
		req.Cwd = dir
	}
	req.Agent, req.Command, req.MachineID = "claude", s.settings.View().Agents.Paths[machine.LocalID]["claude"], machine.LocalID
	req.Env = s.defaultProfileEnv("claude")
	if err := s.withRunnerProvider(&req); err != nil {
		return jobs.Job{}, err
	}
	if !jobs.ReadsProject(req.Purpose) {
		return s.jobs.SubmitRunner(req)
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
		Prompt: fmt.Sprintf("The project is on the server %q, in the folder %s. This CLI runs on another computer. Read the project only with the mcp__nextboard__ tools: read_file, grep and glob. Relative paths start in that folder.",
			host.Info().Name, target.cwd),
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

func remoteAnswer(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": clipOutput(text)}}, "isError": isError}
}

// clipOutput keeps the start and the end: errors usually sit at the end.
func clipOutput(text string) string {
	n := utf8.RuneCountInString(text)
	if n <= remoteOutputMax {
		return text
	}
	// cut at character boundaries without a copy of the text in runes, 4 bytes for each
	half := remoteOutputMax / 2
	head, tail := 0, len(text)
	for i := range text {
		if half == 0 {
			head = i
			break
		}
		half--
	}
	for k := remoteOutputMax / 2; k > 0 && tail > 0; k-- {
		_, size := utf8.DecodeLastRuneInString(text[:tail])
		tail -= size
	}
	return text[:head] + fmt.Sprintf("\n[… %d characters left out …]\n", n-remoteOutputMax) + text[tail:]
}

func toolSchema(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

var remoteToolList = []map[string]any{
	// readOnlyHint: Claude Code runs the tools that only read side by side, not one after another
	{"name": "read_file", "description": "Read a text file on the server. Lines come numbered as `number<TAB>text`; the number is not part of the file.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"path"}, map[string]any{
			"path":   map[string]any{"type": "string", "description": "Absolute, or relative to the project folder."},
			"offset": map[string]any{"type": "integer", "description": "First line to return, from 1."},
			"limit":  map[string]any{"type": "integer", "description": "Lines to return, 2000 by default."}})},
	{"name": "write_file", "description": "Create or replace a text file on the server.",
		"inputSchema": toolSchema([]string{"path", "content"}, map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"}})},
	{"name": "edit_file", "description": "Replace exact text in a file on the server. old_string must match once, unless replace_all is true. Copy it without the line numbers of read_file.",
		"inputSchema": toolSchema([]string{"path", "old_string", "new_string"}, map[string]any{
			"path":        map[string]any{"type": "string"},
			"old_string":  map[string]any{"type": "string"},
			"new_string":  map[string]any{"type": "string"},
			"replace_all": map[string]any{"type": "boolean"}})},
	{"name": "grep", "description": "Search file contents on the server with a regular expression. Answers `file:line:text`.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"pattern"}, map[string]any{
			"pattern":     map[string]any{"type": "string"},
			"path":        map[string]any{"type": "string", "description": "Folder or file, the project folder by default."},
			"include":     map[string]any{"type": "string", "description": "Only files matching this glob, e.g. *.go."},
			"ignore_case": map[string]any{"type": "boolean"}})},
	{"name": "glob", "description": "List files on the server whose path matches a glob, e.g. **/*.go. At most 500.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"pattern"}, map[string]any{
			"pattern": map[string]any{"type": "string"},
			"path":    map[string]any{"type": "string", "description": "Folder to look in, the project folder by default."}})},
	{"name": "bash", "description": "Run a shell command on the server, in the project folder, in a new login shell. Use it to build, test and run git. For anything that takes more than a minute (installs, builds, test suites, dev servers), set run_in_background and follow it with bash_output.",
		"inputSchema": toolSchema([]string{"command"}, map[string]any{
			"command":           map[string]any{"type": "string"},
			"timeout_ms":        map[string]any{"type": "integer", "description": "2 minutes by default, 10 at most. Not for run_in_background."},
			"run_in_background": map[string]any{"type": "boolean", "description": "Start it and answer at once with an id; it may run up to an hour. Read its output as it comes with bash_output."}})},
	{"name": "bash_output", "description": "What a background command started with bash run_in_background has printed since the last call (stdout and stderr together), and whether it still runs or how it ended.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"id"}, map[string]any{
			"id":      map[string]any{"type": "string", "description": "The id bash gave, e.g. bg_1a2b3c4d."},
			"wait_ms": map[string]any{"type": "integer", "description": "Wait up to this long for new output or the end, 30000 at most. 0 answers at once."}})},
	{"name": "bash_kill", "description": "Stop a background command started with bash run_in_background.",
		"inputSchema": toolSchema([]string{"id"}, map[string]any{
			"id": map[string]any{"type": "string"}})},
}

// memoryToolList gives every CLI the project's one memory, to look things up in
// by topic and to add to.
var memoryToolList = []map[string]any{
	{"name": "memory_search", "description": "Look something up in the memory of this project by topic: the main memory, its pages such as a design system or testing notes, and notes of earlier agents. Use it before working in an area you have not looked at yet. Returns the matching passages, not whole pages.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"query"}, map[string]any{
			"query": map[string]any{"type": "string", "description": "What you want to know, in a few words."},
			"limit": map[string]any{"type": "integer", "description": "Passages to return, 6 by default."}})},
	{"name": "memory_read", "description": "Read the memory of this project, shared by every agent that works on it. Without a name: the critical rules, the main memory and the list of pages. With a name: that page. With a topic: only the part about that topic.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{}, map[string]any{
			"name":  map[string]any{"type": "string", "description": "A page, as listed by memory_read."},
			"part":  map[string]any{"type": "string", "description": "In a project of several parts: the memory of this part, by its name, instead of a page."},
			"topic": map[string]any{"type": "string", "description": "Only what is about this."}})},
	{"name": "memory_save", "description": "Save one lasting fact about this project for every agent that comes after you, whichever CLI it uses. Not for progress or plans of this task. Critical rules cannot be changed: tell the owner instead.",
		"inputSchema": toolSchema([]string{"fact"}, map[string]any{
			"fact":        map[string]any{"type": "string", "description": "One short fact, at most 400 characters."},
			"section":     map[string]any{"type": "string", "description": "The heading to put it under. In the main memory: " + strings.Join(orch.MemorySections, ", ") + "."},
			"page":        map[string]any{"type": "string", "description": "The page to add it to, by its name or title; a page that does not exist yet is made. Leave out for the main memory."},
			"title":       map[string]any{"type": "string", "description": "For a page that does not exist yet: its title."},
			"description": map[string]any{"type": "string", "description": "For a page that does not exist yet: one sentence saying what is in it and when to read it, since agents choose pages by it."},
			"part":        map[string]any{"type": "string", "description": "In a project of several parts: the name of the one part this holds for, instead of a page. Leave out when it holds for the whole project."}})},
	{"name": "memory_move", "description": "Move facts from one memory of this project to another: out of the main memory into a page (made when it does not exist yet) or a part's memory, or between pages. Each fact is taken out of where it was and added where it goes, under the same heading. Use it when the owner asks to sort the memory, for example to start a page about one area. Critical notes cannot be moved.",
		"inputSchema": toolSchema([]string{"facts"}, map[string]any{
			"facts":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The facts to move, as memory_read shows them (one line each; a unique part of a line of 12 characters or more works too)."},
			"from":        map[string]any{"type": "string", "description": "The page they are in. Leave out for the main memory."},
			"to":          map[string]any{"type": "string", "description": "The page they go to, by its name or title; made when it does not exist yet. Leave out for the main memory."},
			"from_part":   map[string]any{"type": "string", "description": "In a project of several parts: the part whose memory they are in, instead of from."},
			"to_part":     map[string]any{"type": "string", "description": "In a project of several parts: the part whose memory they go to, instead of to."},
			"title":       map[string]any{"type": "string", "description": "For a page that does not exist yet: its title."},
			"description": map[string]any{"type": "string", "description": "For a page that does not exist yet: one sentence saying what is in it and when to read it."}})},
}

// memoryToolsOnePart are the memory tools without their part argument.
var memoryToolsOnePart = func() []map[string]any {
	out := make([]map[string]any, len(memoryToolList))
	for i, x := range memoryToolList {
		y := maps.Clone(x)
		if sc, ok := x["inputSchema"].(map[string]any); ok {
			sc = maps.Clone(sc)
			if props, ok := sc["properties"].(map[string]any); ok {
				props = maps.Clone(props)
				delete(props, "part")
				delete(props, "from_part")
				delete(props, "to_part")
				sc["properties"] = props
			}
			y["inputSchema"] = sc
		}
		out[i] = y
	}
	return out
}()

func (s *Server) runMemoryTool(t remoteTarget, name string, raw json.RawMessage) (string, error) {
	if !t.memory || t.projectID == "" || s.orch == nil {
		return "", errors.New("this console has no project memory tools")
	}
	var a struct {
		Query   string `json:"query"`
		Limit   int    `json:"limit"`
		Name    string `json:"name"`
		Topic   string `json:"topic"`
		Fact    string `json:"fact"`
		Section string `json:"section"`
		Page    string `json:"page"`
		Part    string `json:"part"`
		// a page memory_save makes, and memory_move's
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Facts       []string `json:"facts"`
		From        string   `json:"from"`
		To          string   `json:"to"`
		FromPart    string   `json:"from_part"`
		ToPart      string   `json:"to_part"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %v", err)
		}
	}
	// a part's memory is its page; in a project of one part, the main memory
	if a.Part = strings.TrimSpace(a.Part); a.Part != "" && name != "memory_search" {
		if (name == "memory_read" && a.Name != "") || (name == "memory_save" && a.Page != "") {
			return "", errors.New("give a page or a part, not both")
		}
		page, err := s.orch.PartPage(t.projectID, a.Part)
		if err != nil {
			return "", err
		}
		a.Name, a.Page = page, page
	}
	switch name {
	case "memory_search":
		return s.orch.MemorySearchFor(t.sessionID, t.workspaceID, t.projectID, a.Query, a.Limit), nil
	case "memory_read":
		return s.orch.MemoryReadFor(t.sessionID, t.workspaceID, t.projectID, a.Name, a.Topic)
	case "memory_save":
		page, made, err := s.orch.SaveMemoryFactTo(t.projectID, a.Section, a.Fact, a.Page, a.Title, a.Description)
		if err != nil {
			return "", err
		}
		s.orch.MemorySaved(t.sessionID, t.projectID, page, a.Fact)
		switch {
		case a.Part != "" && page != orch.MainDoc:
			return "Saved to the memory of the part " + a.Part + ".", nil
		case made:
			return fmt.Sprintf("Made the page %s and saved to it. Its name for memory_read and memory_save is %q.", page, page), nil
		case page != orch.MainDoc:
			return "Saved to the page " + page + ".", nil
		}
		return "Saved to the project memory.", nil
	case "memory_move":
		from, to := a.From, a.To
		for _, p := range []struct {
			part string
			page *string
		}{{a.FromPart, &from}, {a.ToPart, &to}} {
			if strings.TrimSpace(p.part) == "" {
				continue
			}
			if *p.page != "" {
				return "", errors.New("give a page or a part, not both")
			}
			page, err := s.orch.PartPage(t.projectID, strings.TrimSpace(p.part))
			if err != nil {
				return "", err
			}
			*p.page = page
		}
		if strings.TrimSpace(to) == "" && strings.TrimSpace(from) == "" {
			return "", errors.New("say where the facts go: to (a page) or to_part, or from a page back to the main memory")
		}
		res, err := s.orch.MoveMemoryFacts(t.projectID, from, to, a.Title, a.Description, a.Facts)
		if err != nil {
			return "", err
		}
		for _, f := range res.Moved {
			s.orch.MemorySaved(t.sessionID, t.projectID, res.To, f)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Moved %d fact(s) from %s to %s", len(res.Moved), res.From, res.To)
		if res.Made {
			b.WriteString(" (the page was made for them)")
		}
		b.WriteString(".")
		if len(res.Missing) > 0 {
			fmt.Fprintf(&b, " Not found in %s, so not moved: %s.", res.From, strings.Join(res.Missing, "; "))
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// missionToolList are a team's tools on a mission (docs/API-orchestration.md §22): agents reach
// each other only through the hub, and every message stands in the thread for the owner to see.
var missionToolList = []map[string]any{
	{"name": "mission_read", "description": "Read the mission you are part of: its goal, when it counts as done, your own tasks, your teammates (handle, role, part and state), the shared brief with its rev, and the latest team messages to or from you. Read it before you start, and again when a teammate says the brief changed.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{}, map[string]any{})},
	{"name": "team_ask", "description": "Ask a teammate in your mission, or the lead, something you need from them, for example the exact shape of an endpoint. The hub delivers the question when they are ready and keeps it in the mission's thread. Their answer comes to you later as a message, never as the result of this tool: go on with what does not depend on it, or end your turn.",
		"inputSchema": toolSchema([]string{"to", "question"}, map[string]any{
			"to":       map[string]any{"type": "string", "description": "A teammate's handle as mission_read lists it, or \"lead\"."},
			"question": map[string]any{"type": "string", "description": "The question, complete on its own: they do not see your conversation. At most 4000 characters."}})},
	{"name": "team_reply", "description": "Answer a question a teammate asked you (a message saying \"@Name asks you … question q_…\"). The hub delivers your answer to them and closes the question.",
		"inputSchema": toolSchema([]string{"question", "answer"}, map[string]any{
			"question": map[string]any{"type": "string", "description": "The question's id, q_… as the message gave it."},
			"answer":   map[string]any{"type": "string", "description": "Your answer, complete on its own. At most 4000 characters."}})},
	{"name": "team_update", "description": "Tell the team something without asking anything: the contract changed, you pushed a change, an endpoint is ready. With to, the note is delivered to that teammate; without, it is only kept in the thread, and the lead reads it on its next turn.",
		"inputSchema": toolSchema([]string{"text"}, map[string]any{
			"text": map[string]any{"type": "string", "description": "The note, at most 4000 characters."},
			"to":   map[string]any{"type": "string", "description": "A teammate's handle, to deliver it to them."}})},
	{"name": "mission_doc", "description": "Change the mission's shared brief: the contract the team works to (endpoints, data shapes, who does what). append adds your text at the end. replace sets the whole brief and needs the rev mission_read showed, so a newer change by a teammate is never overwritten; read again and redo yours when it says the brief changed. Tell the teammates a change affects with team_update.",
		"inputSchema": toolSchema([]string{"text", "mode"}, map[string]any{
			"text": map[string]any{"type": "string", "description": "Markdown. With append, only what you add."},
			"mode": map[string]any{"type": "string", "enum": []string{"append", "replace"}},
			"rev":  map[string]any{"type": "integer", "description": "The brief's rev as mission_read showed it. Needed for replace."}})},
	{"name": "mission_report", "description": "Report on your own task in the mission to the lead. done: your task is finished; summary says what you built or changed, the files or parts it touched and the tests you ran. blocked: you cannot go on; summary says why, and the lead is told at once. progress: where you are, only kept in the thread. Add needs when you need something from the lead or the owner; the lead is told at once.",
		"inputSchema": toolSchema([]string{"status", "summary"}, map[string]any{
			"status":  map[string]any{"type": "string", "enum": []string{"done", "blocked", "progress"}},
			"summary": map[string]any{"type": "string", "description": "At most 4000 characters."},
			"needs":   map[string]any{"type": "string", "description": "What you need, and from whom."}})},
}

// missionTools are the names of missionToolList.
var missionTools = func() map[string]bool {
	out := map[string]bool{}
	for _, x := range missionToolList {
		out[x["name"].(string)] = true
	}
	return out
}()

// runMissionTool answers a team tool for the console of t.
func (s *Server) runMissionTool(t remoteTarget, name string, raw json.RawMessage) (string, error) {
	if t.sessionID == "" || s.orch == nil {
		return "", orch.ErrNotInMission
	}
	var a struct {
		To       string `json:"to"`
		Question string `json:"question"`
		Answer   string `json:"answer"`
		Text     string `json:"text"`
		Mode     string `json:"mode"`
		Rev      *int   `json:"rev"`
		Status   string `json:"status"`
		Summary  string `json:"summary"`
		Needs    string `json:"needs"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %v", err)
		}
	}
	switch name {
	case "mission_read":
		return s.orch.MissionReadText(t.sessionID)
	case "team_ask":
		return s.orch.TeamAsk(t.sessionID, a.To, a.Question)
	case "team_reply":
		return s.orch.TeamReply(t.sessionID, a.Question, a.Answer)
	case "team_update":
		return s.orch.TeamUpdate(t.sessionID, a.Text, a.To)
	case "mission_doc":
		return s.orch.MissionDocEdit(t.sessionID, a.Text, a.Mode, a.Rev)
	case "mission_report":
		return s.orch.MissionReport(t.sessionID, a.Status, a.Summary, a.Needs)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// remoteReadTools change nothing on the server.
var remoteReadTools = map[string]bool{"read_file": true, "grep": true, "glob": true}

// runRemoteTool runs one tool on the host; the error is the agent's to read.
func runRemoteTool(ctx context.Context, host machine.Host, cwd, name string, raw json.RawMessage) (string, error) {
	var a struct {
		Path       string `json:"path"`
		Offset     int    `json:"offset"`
		Limit      int    `json:"limit"`
		Content    string `json:"content"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
		Command    string `json:"command"`
		TimeoutMs  int64  `json:"timeout_ms"`
		Pattern    string `json:"pattern"`
		Include    string `json:"include"`
		IgnoreCase bool   `json:"ignore_case"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	p := remotePath(cwd, a.Path)
	if name == "grep" || name == "glob" {
		p = a.Path
	}
	switch name {
	case "read_file":
		c, err := host.ReadFile(ctx, p, machine.DefaultReadMax)
		if err != nil {
			return "", err
		}
		if c.Binary {
			return "", fmt.Errorf("%s is a binary file", p)
		}
		return numberedLines(c, a.Offset, a.Limit), nil
	case "write_file":
		if _, err := host.WriteFile(ctx, p, []byte(a.Content), machine.WriteOptions{}); err != nil {
			if strings.HasPrefix(p, "/") && makeParent(ctx, host, p) == nil {
				if _, err = host.WriteFile(ctx, p, []byte(a.Content), machine.WriteOptions{}); err == nil {
					return "Wrote " + p, nil
				}
			}
			return "", err
		}
		return "Wrote " + p, nil
	case "edit_file":
		return editRemoteFile(ctx, host, p, a.OldString, a.NewString, a.ReplaceAll)
	case "grep":
		return runRemoteGrep(ctx, host, cwd, a.Pattern, a.Path, a.Include, a.IgnoreCase)
	case "glob":
		return runRemoteGlob(ctx, host, cwd, a.Pattern, a.Path)
	case "bash":
		return runRemoteBash(ctx, host, cwd, a.Command, a.TimeoutMs)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func makeParent(ctx context.Context, host machine.Host, p string) error {
	_, err := machine.Exec(ctx, host, machine.ExecSpec{Argv: []string{"mkdir", "-p", path.Dir(p)}, Timeout: 20 * time.Second})
	return err
}

func numberedLines(c *machine.FileContent, offset, limit int) string {
	lines := strings.Split(c.Content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = remoteReadLines
	}
	if offset > len(lines) {
		return fmt.Sprintf("[the file has %d lines]", len(lines))
	}
	end := min(offset-1+limit, len(lines))
	var b strings.Builder
	for i := offset - 1; i < end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) || c.Truncated {
		fmt.Fprintf(&b, "[more of the file follows; continue with offset %d]\n", end+1)
	}
	return b.String()
}

func editRemoteFile(ctx context.Context, host machine.Host, p, oldText, newText string, all bool) (string, error) {
	if oldText == "" || oldText == newText {
		return "", errors.New("old_string must be non-empty and differ from new_string")
	}
	c, err := host.ReadFile(ctx, p, machine.MaxFileBytes)
	if err != nil {
		return "", err
	}
	if c.Binary || c.Truncated {
		return "", fmt.Errorf("%s cannot be edited: it is binary or too large", p)
	}
	n := strings.Count(c.Content, oldText)
	switch {
	case n == 0:
		return "", fmt.Errorf("old_string was not found in %s", p)
	case n > 1 && !all:
		return "", fmt.Errorf("old_string matches %d times in %s; add context or set replace_all", n, p)
	}
	next := strings.Replace(c.Content, oldText, newText, map[bool]int{true: -1, false: 1}[all])
	opt := machine.WriteOptions{}
	if !c.Mtime.IsZero() {
		opt.IfMtime = &c.Mtime
	}
	if _, err := host.WriteFile(ctx, p, []byte(next), opt); err != nil {
		return "", err
	}
	return fmt.Sprintf("Edited %s (%d replacement%s)", p, n, map[bool]string{true: "", false: "s"}[n == 1]), nil
}

// remoteSearch runs ripgrep when the server has it. Else, in a git work tree, git (when not
// ""), which leaves out what .gitignore does as ripgrep does: the logs, builds and
// dependencies a plain grep searches for minutes. Else the POSIX fallback.
func remoteSearch(ctx context.Context, host machine.Host, cwd, rg, git, fallback string) (*machine.ExecResult, error) {
	script := `if command -v rg >/dev/null 2>&1; then ` + rg + `; else `
	if git != "" {
		// 0: found, 1: nothing found; anything else (a path outside the work tree): the fallback
		script += `if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then ` + git + `; s=$?; [ $s -le 1 ] && exit $s; fi; `
	}
	script += fallback + `; fi`
	return machine.Exec(ctx, host, machine.ExecSpec{Cwd: cwd, Script: `exec "${SHELL:-/bin/sh}" -lc ` + machine.Quote(script),
		Timeout: remoteBashDefault, IdleTimeout: remoteBashDefault})
}

// grepSkips are what the POSIX grep leaves out, as ripgrep would through .gitignore: git's
// own folder, dependencies and logs (a search in a folder of logs takes minutes).
var grepSkips = []string{"--exclude-dir=.git", "--exclude-dir=node_modules", "--exclude=*.log"}

func runRemoteGrep(ctx context.Context, host machine.Host, cwd, pattern, dir, include string, ignoreCase bool) (string, error) {
	if pattern == "" {
		return "", errors.New("pattern is empty")
	}
	if dir == "" {
		dir = "."
	}
	rg := []string{"rg", "-n", "--no-heading", "--color", "never"}
	// tracked files and new ones .gitignore does not leave out, without binary ones
	git := []string{"git", "-c", "core.quotePath=false", "grep", "-n", "-I", "-E", "--untracked", "--color=never"}
	// -s: a file it may not read is no failure of the search (ripgrep goes on past it too)
	grep := append([]string{"grep", "-rnEIs"}, grepSkips...)
	if ignoreCase {
		rg, git, grep = append(rg, "-i"), append(git, "-i"), append(grep, "-i")
	}
	spec := dir // git's pathspec: one, since several are or-ed; its * also matches a /
	if include != "" {
		rg, grep = append(rg, "-g", include), append(grep, "--include="+include)
		spec = include
		if dir != "." {
			spec = strings.TrimSuffix(dir, "/") + "/" + include
		}
	}
	rg, git, grep = append(rg, "-e", pattern, "--", dir), append(git, "-e", pattern, "--", spec), append(grep, "-e", pattern, "--", dir)
	res, err := remoteSearch(ctx, host, cwd, machine.Join(rg...), machine.Join(git...), machine.Join(grep...))
	if err != nil {
		return "", err
	}
	switch {
	case res.TimedOut, res.IdleKilled:
		return "", fmt.Errorf("the search was stopped after %s", remoteBashDefault)
	case res.ExitCode == 1 && len(res.Stdout) == 0:
		return "No matches.", nil
	case res.ExitCode > 1 && len(res.Stdout) > 0:
		// what it found, though some files could not be read
		return string(res.Stdout), nil
	case res.ExitCode > 1 && len(bytes.TrimSpace(res.Stderr)) == 0:
		return "No matches.", nil // only files it may not read, which -s keeps quiet
	case res.ExitCode > 1:
		return "", fmt.Errorf("exit code %d\n%s", res.ExitCode, res.Stderr)
	}
	return string(res.Stdout), nil
}

func runRemoteGlob(ctx context.Context, host machine.Host, cwd, pattern, dir string) (string, error) {
	if pattern == "" {
		return "", errors.New("pattern is empty")
	}
	if dir == "" {
		dir = "."
	}
	limit := " | head -n 500"
	rg := machine.Join("rg", "--files", "-g", pattern, "--", dir) + limit
	find := machine.Join("find", dir, "-type", "f", "-name", path.Base(pattern), "-not", "-path", "*/.git/*") + limit
	res, err := remoteSearch(ctx, host, cwd, rg, "", find)
	if err != nil {
		return "", err
	}
	if res.TimedOut || res.IdleKilled {
		return "", fmt.Errorf("the search was stopped after %s", remoteBashDefault)
	}
	if len(res.Stdout) == 0 {
		return "No files.", nil
	}
	return string(res.Stdout), nil
}

func runRemoteBash(ctx context.Context, host machine.Host, cwd, command string, timeoutMs int64) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", errors.New("command is empty")
	}
	timeout := remoteBashDefault
	if timeoutMs > 0 {
		timeout = min(time.Duration(timeoutMs)*time.Millisecond, remoteBashMax)
	}
	res, err := machine.Exec(ctx, host, machine.ExecSpec{Cwd: cwd, Script: `exec "${SHELL:-/bin/sh}" -lc ` + machine.Quote(command),
		Timeout: timeout, IdleTimeout: timeout})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.Write(res.Stdout)
	if len(res.Stderr) > 0 {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
		b.Write(res.Stderr)
	}
	switch {
	case res.TimedOut, res.IdleKilled:
		return "", fmt.Errorf("the command was stopped after %s\n%s", timeout, b.String())
	case res.Truncated:
		b.WriteString("\n[output truncated]")
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("exit code %d\n%s", res.ExitCode, b.String())
	}
	if b.Len() == 0 {
		return "(no output)", nil
	}
	return b.String(), nil
}

// handleConnLog is GET /api/logs/connections?machineId=&since=&limit=: what
// happened to the links, newest first, and what it adds up to.
func (s *Server) handleConnLog(w http.ResponseWriter, r *http.Request, p *principal) {
	q := connlog.Query{Machine: r.URL.Query().Get("machineId")}
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "since must be RFC 3339")
			return
		}
		q.Since = t
	}
	q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	items := s.connLog.Read(q)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "summary": connlog.Summarize(items)})
}

// nextcodeMCP are the MCP servers of a NextCode console: those of the hub's (Settings › MCP
// servers) that Settings › NextCode gives NextCode, as they reach this console's project and
// place; none by default.
func (s *Server) nextcodeMCP(sp session.Spec) ([]agent.MCPServer, []string) {
	want := s.settings.View().NextCode.MCP
	if len(want) == 0 || s.mcp == nil {
		return nil, nil
	}
	all, env := s.mcp.ForLaunch(sp.ProjectID, sp.RemoteMachineID != "")
	var servers []agent.MCPServer
	keep := map[string]bool{}
	for _, m := range all {
		if slices.Contains(want, m.Name) {
			servers = append(servers, m)
			for _, v := range append(append([]agent.MCPVar{}, m.Headers...), m.Env...) {
				keep[v.Var] = true
			}
		}
	}
	var vars []string
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); keep[k] {
			vars = append(vars, kv)
		}
	}
	return servers, vars
}
