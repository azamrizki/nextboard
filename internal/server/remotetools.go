package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/connlog"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/profiles"
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
	sum := sha256.Sum256([]byte(cwd))
	dir := s.st.Path("agents", "remote", machineID, hex.EncodeToString(sum[:6]))
	return dir, os.MkdirAll(dir, 0o700)
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
	if s.hookURL == "" {
		return nil, errors.New("listener hook belum berjalan")
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
	if sp.RemoteMachineID != "" {
		host, ok := s.machines.Host(sp.RemoteMachineID)
		if !ok {
			return nil, machine.ErrNotFound
		}
		c.Remote, c.WorkDir = true, sp.Cwd
		// The folder the CLI runs in is empty and the hub's: no trust dialog for
		// it. Only in config folders the hub made, never in the user's own.
		if hasProfile && pf.CreatedByHub && claude {
			if err := profiles.TrustFolder(pf.Dir, s.st.Path("agents", "remote")); err != nil {
				log.Printf("trust folder agen: %v", err)
			}
		}
		docs := []string{"AGENTS.md", "CLAUDE.md"}
		if claude {
			docs = []string{"CLAUDE.md", "AGENTS.md"}
		}
		prompt.WriteString(remotePrompt(host.Info().Name, sp.RemoteCwd, s.remoteProjectRules(host, sp.RemoteCwd, docs), claude))
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
					log.Printf("memory lama %s: %v", src, err)
				}
			}
		}
		if prompt.Len() > 0 {
			prompt.WriteString("\n")
		}
		prompt.WriteString(s.orch.LaunchMemory(sp.ID, sp.WorkspaceID, sp.ProjectID, claude))
	}
	if s.orch != nil {
		c.Blocked = s.orch.BlockedCommands(sp.ProjectID)
		// Codex keeps its rules in its config folder: only in one the hub made, never the user's own.
		if a.CapBase() == "codex" && hasProfile && pf.CreatedByHub && !c.Remote {
			if err := agent.WriteCodexRules(pf.Dir, s.orch.AllBlocked()); err != nil {
				log.Printf("rules codex: %v", err)
			}
		}
	}
	if !s.settings.Get().Agents.CoAuthor {
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

func remotePrompt(machineName, cwd, rules string, localMemory bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are working on the server %q, in the folder %s. This CLI runs on another computer, so its local tools cannot see the project.\n", machineName, cwd)
	b.WriteString("For everything in the project use the tools of the nextboard MCP server: read_file, write_file, edit_file, grep, glob and bash. Relative paths start in that folder. Each bash call starts there in a new shell, so `cd` does not carry over.\n")
	if localMemory {
		b.WriteString("Your local Read, Write and Edit tools are only for your memory folder. Never use them for project files.\n")
	}
	if strings.TrimSpace(rules) != "" {
		b.WriteString("\nProject instructions from the server:\n\n" + rules + "\n")
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
	machineID, cwd string
	readOnly       bool
	projectID      string
	memory         bool
	// a console's token also reads what the memory tiles connected to it add
	sessionID, workspaceID string
}

func (t remoteTarget) tools() []map[string]any {
	var list []map[string]any
	if t.machineID != "" {
		for _, x := range remoteToolList {
			if !t.readOnly || remoteReadTools[x["name"].(string)] {
				list = append(list, x)
			}
		}
	}
	if t.memory && t.projectID != "" {
		list = append(list, memoryToolList...)
	}
	return list
}

// handleRemoteMCP serves MCP to a console. There is no event stream, so GET
// answers 405 as the protocol allows.
func (s *Server) handleRemoteMCP(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessions.Get(r.PathValue("sid"))
	if !ok || !sess.HookTokenOK(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sp := sess.Spec()
	t := remoteTarget{machineID: sp.RemoteMachineID, cwd: sp.RemoteCwd, projectID: sp.ProjectID, memory: true, sessionID: sp.ID, workspaceID: sp.WorkspaceID}
	if len(t.tools()) == 0 {
		http.Error(w, "console ini tidak memakai tool hub", http.StatusNotFound)
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
	s.serveRemoteRPC(w, r, v.(remoteTarget))
}

func (s *Server) serveRemoteRPC(w http.ResponseWriter, r *http.Request, t remoteTarget) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, remoteRequestLimit))
	if err != nil {
		http.Error(w, "body terlalu besar", http.StatusRequestEntityTooLarge)
		return
	}
	var req rpcRequest
	if json.Unmarshal(body, &req) != nil {
		http.Error(w, "bukan JSON-RPC", http.StatusBadRequest)
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
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params"}
		}
		if strings.HasPrefix(p.Name, "memory_") {
			text, err := s.runMemoryTool(t, p.Name, p.Arguments)
			if err != nil {
				return remoteAnswer(err.Error(), true), nil
			}
			return remoteAnswer(text, false), nil
		}
		if t.readOnly && !remoteReadTools[p.Name] {
			return remoteAnswer("This job may only read.", true), nil
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
				if answer := s.orch.BlockedAnswer(t.projectID, a.Command); answer != "" {
					return remoteAnswer(answer, true), nil
				}
			}
		}
		start := time.Now()
		text, err := runRemoteTool(ctx, host, t.cwd, p.Name, p.Arguments)
		s.noteTool(host, p.Name, time.Since(start), err)
		if err != nil {
			return remoteAnswer(err.Error(), true), nil
		}
		return remoteAnswer(text, false), nil
	}
	return nil, map[string]any{"code": -32601, "message": "method not found"}
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
	if req.MachineID != "" && req.MachineID != machine.LocalID && s.settings.Get().Agents.RemoteTools {
		return s.submitRemoteRunner(req)
	}
	if len(req.Env) == 0 && (req.MachineID == "" || req.MachineID == machine.LocalID) {
		req.Env = s.defaultProfileEnv(req.Agent)
	}
	return s.jobs.SubmitRunner(req)
}

func (s *Server) submitRemoteRunner(req jobs.RunnerRequest) (jobs.Job, error) {
	host, ok := s.machines.Host(req.MachineID)
	if !ok {
		return jobs.Job{}, machine.ErrNotFound
	}
	if s.hookURL == "" {
		return jobs.Job{}, errors.New("listener hook belum berjalan")
	}
	target := remoteTarget{machineID: req.MachineID, cwd: req.Cwd, readOnly: true}
	if req.Cwd != "" {
		dir, err := s.remoteWorkDir(req.MachineID, req.Cwd)
		if err != nil {
			return jobs.Job{}, err
		}
		req.Cwd = dir
	}
	req.Agent, req.Command, req.MachineID = "claude", s.settings.Get().Agents.Paths[machine.LocalID]["claude"], machine.LocalID
	req.Env = s.defaultProfileEnv("claude")
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
	r := []rune(text)
	if len(r) <= remoteOutputMax {
		return text
	}
	half := remoteOutputMax / 2
	return string(r[:half]) + fmt.Sprintf("\n[… %d characters left out …]\n", len(r)-remoteOutputMax) + string(r[len(r)-half:])
}

func toolSchema(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

var remoteToolList = []map[string]any{
	{"name": "read_file", "description": "Read a text file on the server. Lines come numbered as `number<TAB>text`; the number is not part of the file.",
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
		"inputSchema": toolSchema([]string{"pattern"}, map[string]any{
			"pattern":     map[string]any{"type": "string"},
			"path":        map[string]any{"type": "string", "description": "Folder or file, the project folder by default."},
			"include":     map[string]any{"type": "string", "description": "Only files matching this glob, e.g. *.go."},
			"ignore_case": map[string]any{"type": "boolean"}})},
	{"name": "glob", "description": "List files on the server whose path matches a glob, e.g. **/*.go. At most 500.",
		"inputSchema": toolSchema([]string{"pattern"}, map[string]any{
			"pattern": map[string]any{"type": "string"},
			"path":    map[string]any{"type": "string", "description": "Folder to look in, the project folder by default."}})},
	{"name": "bash", "description": "Run a shell command on the server, in the project folder, in a new login shell. Use it to build, test and run git.",
		"inputSchema": toolSchema([]string{"command"}, map[string]any{
			"command":    map[string]any{"type": "string"},
			"timeout_ms": map[string]any{"type": "integer", "description": "2 minutes by default, 10 at most."}})},
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
			"topic": map[string]any{"type": "string", "description": "Only what is about this."}})},
	{"name": "memory_save", "description": "Save one lasting fact about this project for every agent that comes after you, whichever CLI it uses. Not for progress or plans of this task. Critical rules cannot be changed: tell the owner instead.",
		"inputSchema": toolSchema([]string{"fact"}, map[string]any{
			"fact":    map[string]any{"type": "string", "description": "One short fact, at most 400 characters."},
			"section": map[string]any{"type": "string", "description": "The heading to put it under. In the main memory: " + strings.Join(orch.MemorySections, ", ") + "."},
			"page":    map[string]any{"type": "string", "description": "The page to add it to. Leave out for the main memory."}})},
}

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
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %v", err)
		}
	}
	switch name {
	case "memory_search":
		return s.orch.MemorySearchFor(t.sessionID, t.workspaceID, t.projectID, a.Query, a.Limit), nil
	case "memory_read":
		return s.orch.MemoryReadFor(t.sessionID, t.workspaceID, t.projectID, a.Name, a.Topic)
	case "memory_save":
		if err := s.orch.SaveMemoryFact(t.projectID, a.Section, a.Fact, a.Page); err != nil {
			return "", err
		}
		s.orch.MemorySaved(t.sessionID, t.projectID, a.Page, a.Fact)
		return "Saved to the project memory.", nil
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

// remoteSearch runs ripgrep when the server has it, else the POSIX fallback.
func remoteSearch(ctx context.Context, host machine.Host, cwd, rg, fallback string) (*machine.ExecResult, error) {
	script := `if command -v rg >/dev/null 2>&1; then ` + rg + `; else ` + fallback + `; fi`
	return machine.Exec(ctx, host, machine.ExecSpec{Cwd: cwd, Script: `exec "${SHELL:-/bin/sh}" -lc ` + machine.Quote(script),
		Timeout: remoteBashDefault, IdleTimeout: remoteBashDefault})
}

func runRemoteGrep(ctx context.Context, host machine.Host, cwd, pattern, dir, include string, ignoreCase bool) (string, error) {
	if pattern == "" {
		return "", errors.New("pattern is empty")
	}
	if dir == "" {
		dir = "."
	}
	rg := []string{"rg", "-n", "--no-heading", "--color", "never"}
	grep := []string{"grep", "-rnE"}
	if ignoreCase {
		rg, grep = append(rg, "-i"), append(grep, "-i")
	}
	if include != "" {
		rg, grep = append(rg, "-g", include), append(grep, "--include="+include)
	}
	rg, grep = append(rg, "-e", pattern, "--", dir), append(grep, "-e", pattern, "--", dir)
	res, err := remoteSearch(ctx, host, cwd, machine.Join(rg...), machine.Join(grep...))
	if err != nil {
		return "", err
	}
	switch {
	case res.TimedOut, res.IdleKilled:
		return "", fmt.Errorf("the search was stopped after %s", remoteBashDefault)
	case res.ExitCode == 1 && len(res.Stdout) == 0:
		return "No matches.", nil
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
	res, err := remoteSearch(ctx, host, cwd, rg, find)
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
			writeErr(w, http.StatusBadRequest, "since harus RFC 3339")
			return
		}
		q.Since = t
	}
	q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	items := s.connLog.Read(q)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "summary": connlog.Summarize(items)})
}
