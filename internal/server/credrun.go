package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/vault"
)

// Agents using a credential (docs/API-vault.md, "Agent memakai credential"). An agent asks the hub
// to run one command with values from the vault. The owner approves it in a browser, which opens
// the item, checks that the owner marked it for this project, and hands the values over. The hub
// runs the command as a process of its own, not inside the agent, and the agent reads its output
// with the values hidden. Nothing is written anywhere: the values live in this process for one run.

const (
	credWait       = 45 * time.Second // one tool call, under the 60 s Codex gives an MCP call
	credExpiry     = 10 * time.Minute
	credKeep       = 15 * time.Minute
	credRunDefault = 2 * time.Minute
	credRunMax     = 10 * time.Minute
	credOutputMax  = 1 << 20
	credCommandMax = 4000
	credWhyMax     = 300
	credNoteMax    = 300
	credUsesMax    = 8
	credValueMax   = 64 << 10
	// A console asking again and again must not bury the owner in dialogs.
	credWaitingPerConsole = 3
	credWaitingMax        = 20
	credLogFile           = "credential-log.jsonl"
	credLogMax            = 1 << 20
	credLogRead           = 500
)

var credEnvRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// credEnvRefused: names that change how the shell, the loader or the hub's own script behaves.
func credEnvRefused(name string) bool {
	up := strings.ToUpper(name)
	switch up {
	case "PATH", "HOME", "SHELL", "USER", "LOGNAME", "IFS", "ENV", "BASH_ENV", "CDPATH", "PS4", "PROMPT_COMMAND",
		"TERM", "PWD", "OLDPWD", "TMPDIR", "ZDOTDIR":
		return true
	}
	for _, p := range []string{"LD_", "DYLD_", "NB_", "BASH_FUNC_"} {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	return false
}

// credUse is one value a command gets. Env "" means on its standard input.
type credUse struct {
	Item  string `json:"item"`
	Name  string `json:"name"`
	Value string `json:"value"`
	Env   string `json:"env,omitempty"`
}

// credRequest is one command waiting for the owner, running, or done. It never holds a value.
type credRequest struct {
	ID          string     `json:"id"`
	State       string     `json:"state"` // waiting | running | done | denied | expired | failed
	SessionID   string     `json:"sessionId"`
	Handle      string     `json:"handle"`
	Agent       string     `json:"agent"`
	ProjectID   string     `json:"projectId"`
	WorkspaceID string     `json:"workspaceId"`
	MachineID   string     `json:"machineId"`
	Machine     string     `json:"machine"`
	Cwd         string     `json:"cwd"`
	Command     string     `json:"command"`
	Why         string     `json:"why"`
	Uses        []credUse  `json:"uses"`
	TimeoutMs   int64      `json:"timeoutMs"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	ExitCode    *int       `json:"exitCode,omitempty"`
	DurationMs  int64      `json:"durationMs,omitempty"`
	Note        string     `json:"note,omitempty"`
	By          string     `json:"by,omitempty"`

	result  string // what the agent reads, values hidden
	isError bool
	done    chan struct{}
}

func (r *credRequest) final() bool {
	switch r.State {
	case "waiting", "running":
		return false
	}
	return true
}

// credRequests lives in memory only: a hub restart forgets every request, and an agent asks again.
type credRequests struct {
	mu sync.Mutex
	m  map[string]*credRequest
	// wait, expiry and keep are constants but in tests
	wait   time.Duration
	expiry time.Duration
	keep   time.Duration
	logMu  sync.Mutex
}

func newCredRequests() *credRequests {
	return &credRequests{m: map[string]*credRequest{}, wait: credWait, expiry: credExpiry, keep: credKeep}
}

func (c *credRequests) view(id string) (credRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[id]
	if !ok {
		return credRequest{}, false
	}
	return r.copy(), true
}

func (r *credRequest) copy() credRequest {
	v := *r
	v.Uses = slices.Clone(r.Uses)
	v.done = nil
	return v
}

// ---- the tools an agent gets ----

var credentialToolList = []map[string]any{
	{"name": "credential_list", "description": "List the credentials the owner lets agents of this project use, such as API keys and passwords: their names and the names of their values, never the values. Look here when a command needs a secret, instead of asking the owner to paste one.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{}, map[string]any{})},
	{"name": "credential_run", "description": "Run one shell command with secrets from the owner's vault, after the owner approves it in NextBoard. You never see the values: the command gets them as environment variables or on its standard input, and its output comes back to you with them replaced by ••••. It runs in the project folder, in a new login shell, on the machine this console works on. Ask only for what the task needs and say why. Never try to make the command reveal a value.",
		"inputSchema": toolSchema([]string{"command", "why", "secrets"}, map[string]any{
			"command": map[string]any{"type": "string", "description": "The command, e.g. npm run deploy."},
			"why":     map[string]any{"type": "string", "description": "One sentence for the owner: what it is for."},
			"secrets": map[string]any{"type": "array", "description": "Values for environment variables. May be empty when stdin is given.",
				"items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"item", "value", "env"}, "properties": map[string]any{
					"item":  map[string]any{"type": "string", "description": "A credential's name or id, from credential_list."},
					"value": map[string]any{"type": "string", "description": "Which of its values, e.g. password."},
					"env":   map[string]any{"type": "string", "description": "The environment variable it goes into, e.g. STRIPE_KEY."}}}},
			"stdin": map[string]any{"type": "object", "description": "One value for the command's standard input, e.g. for docker login --password-stdin.", "additionalProperties": false,
				"required": []string{"item", "value"}, "properties": map[string]any{
					"item":  map[string]any{"type": "string"},
					"value": map[string]any{"type": "string"}}},
			"timeout_ms": map[string]any{"type": "integer", "description": "2 minutes by default, 10 at most."}})},
	{"name": "credential_result", "description": "Wait for a request of credential_run that was still waiting for the owner or still running, and get its output.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"id"}, map[string]any{
			"id": map[string]any{"type": "string", "description": "The request id credential_run gave."}})},
}

// credentialTool answers one call of a credential tool, and whether that answer is an error.
func (s *Server) credentialTool(ctx context.Context, t remoteTarget, name string, raw json.RawMessage) (string, bool) {
	if t.sessionID == "" || t.projectID == "" || s.credVault == nil {
		return "Credentials are only for a console that works on a project.", true
	}
	switch name {
	case "credential_list":
		return s.credList(t.projectID), false
	case "credential_run":
		return s.credRun(ctx, t, raw)
	case "credential_result":
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &a)
		r, ok := s.credReqs.view(a.ID)
		if !ok || r.SessionID != t.sessionID {
			return fmt.Sprintf("There is no request %q for this console. The hub may have restarted: ask again with credential_run.", a.ID), true
		}
		return s.credAwait(ctx, a.ID)
	}
	return fmt.Sprintf("unknown tool %q", name), true
}

const credNoneShared = "The owner shares no credentials with agents of this project. Tell the owner what you need. Never ask them to paste a secret into this conversation."

func (s *Server) credList(projectID string) string {
	entries := s.credVault.AgentEntriesFor(projectID)
	if len(entries) == 0 {
		return credNoneShared
	}
	var b strings.Builder
	b.WriteString("Credentials the owner lets agents of this project use. You never get a value: credential_run gives values to one command, after the owner approves it.\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- %q (%s, id %s): %s\n", e.Name, e.Type, e.ID, strings.Join(e.Values, ", "))
	}
	return b.String()
}

// credResolve finds the entry an agent named, by id or by name, and the value it asked for.
func credResolve(entries []vault.AgentEntry, item, value string) (vault.AgentEntry, string, error) {
	item = strings.TrimSpace(item)
	var hits []vault.AgentEntry
	for _, e := range entries {
		if e.ID == item {
			hits = []vault.AgentEntry{e}
			break
		}
		if strings.EqualFold(e.Name, item) {
			hits = append(hits, e)
		}
	}
	switch {
	case len(hits) == 0:
		return vault.AgentEntry{}, "", fmt.Errorf("no credential %q is shared with agents of this project; see credential_list", item)
	case len(hits) > 1:
		return vault.AgentEntry{}, "", fmt.Errorf("%d credentials are called %q: give the id instead, from credential_list", len(hits), item)
	}
	e := hits[0]
	for _, v := range e.Values {
		if strings.EqualFold(v, strings.TrimSpace(value)) {
			return e, v, nil
		}
	}
	return vault.AgentEntry{}, "", fmt.Errorf("%q has no value %q; it has %s", e.Name, value, strings.Join(e.Values, ", "))
}

type credRunArgs struct {
	Command string `json:"command"`
	Why     string `json:"why"`
	Secrets []struct {
		Item  string `json:"item"`
		Value string `json:"value"`
		Env   string `json:"env"`
	} `json:"secrets"`
	Stdin *struct {
		Item  string `json:"item"`
		Value string `json:"value"`
	} `json:"stdin"`
	TimeoutMs int64 `json:"timeout_ms"`
}

// credUses checks what an agent asked for against the catalog of its project.
func credUses(entries []vault.AgentEntry, a credRunArgs) ([]credUse, error) {
	n := len(a.Secrets)
	if a.Stdin != nil {
		n++
	}
	if n == 0 {
		return nil, errors.New("name at least one secret, in secrets or stdin")
	}
	if n > credUsesMax {
		return nil, fmt.Errorf("at most %d secrets in one command", credUsesMax)
	}
	var uses []credUse
	envs := map[string]bool{}
	for _, x := range a.Secrets {
		env := strings.TrimSpace(x.Env)
		if !credEnvRe.MatchString(env) {
			return nil, fmt.Errorf("%q is not an environment variable name", x.Env)
		}
		if credEnvRefused(env) {
			return nil, fmt.Errorf("%s changes how the shell runs: use another name", env)
		}
		if envs[env] {
			return nil, fmt.Errorf("%s is named twice", env)
		}
		envs[env] = true
		e, v, err := credResolve(entries, x.Item, x.Value)
		if err != nil {
			return nil, err
		}
		uses = append(uses, credUse{Item: e.ID, Name: e.Name, Value: v, Env: env})
	}
	if a.Stdin != nil {
		e, v, err := credResolve(entries, a.Stdin.Item, a.Stdin.Value)
		if err != nil {
			return nil, err
		}
		uses = append(uses, credUse{Item: e.ID, Name: e.Name, Value: v})
	}
	return uses, nil
}

func (s *Server) credRun(ctx context.Context, t remoteTarget, raw json.RawMessage) (string, bool) {
	var a credRunArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Sprintf("invalid arguments: %v", err), true
	}
	entries := s.credVault.AgentEntriesFor(t.projectID)
	if len(entries) == 0 {
		return credNoneShared, true
	}
	command, why := strings.TrimSpace(a.Command), strings.TrimSpace(a.Why)
	switch {
	case command == "":
		return "command is empty", true
	case len(command) > credCommandMax || strings.ContainsRune(command, 0):
		return fmt.Sprintf("the command must be text of at most %d bytes", credCommandMax), true
	case why == "":
		return "say why in one sentence: the owner reads it before approving", true
	case utf8.RuneCountInString(why) > credWhyMax:
		return fmt.Sprintf("why: at most %d characters", credWhyMax), true
	}
	uses, err := credUses(entries, a)
	if err != nil {
		return err.Error(), true
	}
	if s.orch != nil {
		if answer := s.orch.BlockedAnswer(t.projectID, command); answer != "" {
			return answer, true
		}
	}
	timeout := credRunDefault
	if a.TimeoutMs > 0 {
		timeout = min(time.Duration(a.TimeoutMs)*time.Millisecond, credRunMax)
	}
	info, ok := s.sessions.Info(t.sessionID)
	if !ok {
		return "This console is gone.", true
	}
	machineID, cwd := info.RemoteMachineID, info.RemoteCwd
	if machineID == "" {
		machineID, cwd = info.MachineID, info.Cwd
	}
	host, ok := s.machines.Host(machineID)
	if !ok {
		return "The machine of this console is no longer registered in NextBoard.", true
	}
	agentName := info.Agent
	if ag, ok := s.agents.Get(info.Agent); ok && ag.Name != "" {
		agentName = ag.Name
	}
	now := time.Now().UTC()
	req := &credRequest{ID: auth.RandomID("cr_"), State: "waiting", SessionID: info.ID, Handle: info.Handle, Agent: agentName,
		ProjectID: t.projectID, WorkspaceID: info.WorkspaceID, MachineID: machineID, Machine: host.Info().Name, Cwd: cwd,
		Command: command, Why: why, Uses: uses, TimeoutMs: timeout.Milliseconds(), CreatedAt: now, done: make(chan struct{})}
	if msg := s.credReqs.add(req); msg != "" {
		return msg, true
	}
	time.AfterFunc(s.credReqs.expiry, func() { s.credExpire(req.ID) })
	s.credPublish(req.ID, "waiting")
	s.credAttention(info, req)
	return s.credAwait(ctx, req.ID)
}

func (c *credRequests) add(r *credRequest) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	all, mine := 0, 0
	for _, x := range c.m {
		if x.State == "waiting" {
			all++
			if x.SessionID == r.SessionID {
				mine++
			}
		}
	}
	switch {
	case mine >= credWaitingPerConsole:
		return "This console already has requests waiting for the owner. Wait for them with credential_result."
	case all >= credWaitingMax:
		return "Too many requests are waiting for the owner. Try again later."
	}
	r.ExpiresAt = r.CreatedAt.Add(c.expiry)
	c.m[r.ID] = r
	return ""
}

// credAwait waits a while for the request to end, then says where it stands.
func (s *Server) credAwait(ctx context.Context, id string) (string, bool) {
	s.credReqs.mu.Lock()
	r, ok := s.credReqs.m[id]
	var done chan struct{}
	if ok {
		done = r.done
	}
	s.credReqs.mu.Unlock()
	if !ok {
		return fmt.Sprintf("There is no request %q. The hub may have restarted: ask again with credential_run.", id), true
	}
	t := time.NewTimer(s.credReqs.wait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-ctx.Done():
	}
	s.credReqs.mu.Lock()
	defer s.credReqs.mu.Unlock()
	switch r.State {
	case "waiting":
		return fmt.Sprintf("Request %s is waiting for the owner to approve it in NextBoard. Call credential_result with this id to wait for it. Do not ask for the values in any other way.", id), false
	case "running":
		return fmt.Sprintf("The owner approved request %s and the command is running. Call credential_result with this id for its output.", id), false
	}
	return r.result, r.isError
}

func (s *Server) credPublish(id, state string) {
	s.events.Publish(map[string]any{"type": "credentialRequest", "id": id, "state": state})
}

// credAttention tells the owner, like a console that needs them.
func (s *Server) credAttention(in session.Info, r *credRequest) {
	if !s.settings.View().Notifications.NeedsYou {
		return
	}
	who := in.Handle
	if who == "" {
		who = in.Name
	}
	body := r.Command
	if utf8.RuneCountInString(body) > 200 {
		body = string([]rune(body)[:200]) + "…"
	}
	a := s.attn.add(Attention{Kind: "credential", SessionID: r.SessionID, Handle: in.Handle, Agent: in.Agent, MachineName: r.Machine,
		ProjectID: r.ProjectID, WorkspaceID: r.WorkspaceID, Title: who + " asks to use a credential", Body: body})
	s.events.Publish(map[string]any{"type": "attention", "attention": a})
}

// credEnd puts a request in its last state, once, and lets every waiter go.
func (s *Server) credEnd(id, from, state, result string, isError bool, mutate func(*credRequest)) bool {
	s.credReqs.mu.Lock()
	r, ok := s.credReqs.m[id]
	if !ok || r.State != from {
		s.credReqs.mu.Unlock()
		return false
	}
	at := time.Now().UTC()
	r.State, r.result, r.isError, r.FinishedAt = state, result, isError, &at
	if mutate != nil {
		mutate(r)
	}
	close(r.done)
	entry := r.copy()
	keep := s.credReqs.keep
	s.credReqs.mu.Unlock()
	time.AfterFunc(keep, func() {
		s.credReqs.mu.Lock()
		delete(s.credReqs.m, id)
		s.credReqs.mu.Unlock()
	})
	s.credPublish(id, state)
	s.credLogWrite(entry)
	return true
}

func (s *Server) credExpire(id string) {
	s.credEnd(id, "waiting", "expired", "Nobody approved this request within 10 minutes. Ask the owner whether to try again.", true, nil)
}

// ---- running it ----

// credPrelude reads one value a line from standard input: every byte written as \0ooo, which
// printf %b turns back into exactly that byte in sh, dash, bash, zsh and busybox. The x keeps a
// trailing newline that $( ) would drop. Values never stand in a command line, where ps on the
// server shows them to every user.
const credPrelude = `nb_v() { IFS= read -r nb_l || exit 125; nb_x=$(printf '%bx' "$nb_l"); nb_x=${nb_x%x}; }`

// credScript is the script that runs the command, and what goes on its standard input: the values
// for the environment first, then the one for the command's own input.
func credScript(command string, uses []credUse, values []string) (string, []byte) {
	var b strings.Builder
	var in bytes.Buffer
	b.WriteString(credPrelude)
	stdin := -1
	for i, u := range uses {
		if u.Env == "" {
			stdin = i
			continue
		}
		b.WriteString("; nb_v; export " + u.Env + `="$nb_x"`)
		for _, c := range []byte(values[i]) {
			fmt.Fprintf(&in, `\0%03o`, c)
		}
		in.WriteByte('\n')
	}
	b.WriteString(`; unset nb_l nb_x; unset -f nb_v; case "${SHELL##*/}" in sh|bash|zsh|dash|ksh|mksh|ash) nb_sh=$SHELL;; *) nb_sh=/bin/sh;; esac; exec "$nb_sh" -lc ` + machine.Quote(command))
	if stdin >= 0 {
		in.WriteString(values[stdin])
	}
	return b.String(), in.Bytes()
}

// credApprove takes the values the browser opened and starts the run.
func (s *Server) credApprove(id string, values []string, by string) (credRequest, error) {
	s.credReqs.mu.Lock()
	r, ok := s.credReqs.m[id]
	if !ok {
		s.credReqs.mu.Unlock()
		return credRequest{}, errCredGone
	}
	if r.State != "waiting" {
		s.credReqs.mu.Unlock()
		return credRequest{}, errCredDecided
	}
	if len(values) != len(r.Uses) {
		s.credReqs.mu.Unlock()
		return credRequest{}, fmt.Errorf("%w: needs %d values", errCredBad, len(r.Uses))
	}
	for i, v := range values {
		switch {
		case len(v) < vault.MinCensored:
			s.credReqs.mu.Unlock()
			return credRequest{}, fmt.Errorf("%w: %s is too short to hide in the output (at least %d characters)", errCredBad, r.Uses[i].Value, vault.MinCensored)
		case len(v) > credValueMax:
			s.credReqs.mu.Unlock()
			return credRequest{}, fmt.Errorf("%w: %s is too long", errCredBad, r.Uses[i].Value)
		case r.Uses[i].Env != "" && strings.ContainsRune(v, 0):
			s.credReqs.mu.Unlock()
			return credRequest{}, fmt.Errorf("%w: an environment variable cannot hold a zero byte", errCredBad)
		}
	}
	r.State, r.By = "running", by
	view := r.copy()
	s.credReqs.mu.Unlock()
	s.credPublish(id, "running")
	go s.credExecute(view, slices.Clone(values))
	return view, nil
}

var (
	errCredGone    = errors.New("the request is gone")
	errCredDecided = errors.New("this request is already decided")
	errCredBad     = errors.New("invalid value")
)

func (s *Server) credExecute(r credRequest, values []string) {
	censor := vault.NewCensor(values)
	host, ok := s.machines.Host(r.MachineID)
	if !ok {
		s.credEnd(r.ID, "running", "failed", "The command could not run: its machine is no longer registered.", true, nil)
		return
	}
	timeout := time.Duration(r.TimeoutMs) * time.Millisecond
	script, stdin := credScript(r.Command, r.Uses, values)
	ctx, cancel := context.WithTimeout(context.Background(), timeout+15*time.Second)
	defer cancel()
	start := time.Now()
	res, err := machine.Exec(ctx, host, machine.ExecSpec{Cwd: r.Cwd, Script: script, Stdin: stdin, Timeout: timeout, MaxOutput: credOutputMax})
	clear(stdin)
	took := time.Since(start).Milliseconds()
	if err != nil {
		s.credEnd(r.ID, "running", "failed", "The command could not run: "+censor.Clean(err.Error(), false), true, func(x *credRequest) { x.DurationMs = took })
		return
	}
	text, isError := credOutcome(res, timeout, censor)
	code := res.ExitCode
	s.credEnd(r.ID, "running", "done", text, isError, func(x *credRequest) { x.ExitCode, x.DurationMs = &code, took })
}

// credOutcome is what the agent reads, as the hub's bash tool would put it, with the values hidden.
func credOutcome(res *machine.ExecResult, timeout time.Duration, censor *vault.Censor) (string, bool) {
	out := censor.Clean(string(res.Stdout), res.Truncated)
	if errOut := censor.Clean(string(res.Stderr), res.Truncated); errOut != "" {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += errOut
	}
	switch {
	case res.TimedOut, res.IdleKilled:
		return fmt.Sprintf("the command was stopped after %s\n%s", timeout, out), true
	case res.Truncated:
		out += "\n[output truncated]"
	}
	if res.ExitCode != 0 {
		return fmt.Sprintf("exit code %d\n%s", res.ExitCode, out), true
	}
	if out == "" {
		return "(no output)", false
	}
	return out, false
}

// ---- the log ----

// credLogWrite appends what happened, never a value: the request has none.
func (s *Server) credLogWrite(r credRequest) {
	s.credReqs.logMu.Lock()
	defer s.credReqs.logMu.Unlock()
	path := s.st.Path(credLogFile)
	if fi, err := os.Stat(path); err == nil && fi.Size() > credLogMax {
		if err := os.Rename(path, path+".1"); err != nil {
			log.Printf("log credential: %v", err)
		}
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("log credential: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		log.Printf("log credential: %v", err)
	}
}

// credLogRead returns the last entries, newest first.
func (s *Server) credLogRead(limit int) []credRequest {
	s.credReqs.logMu.Lock()
	defer s.credReqs.logMu.Unlock()
	var all []credRequest
	for _, name := range []string{credLogFile + ".1", credLogFile} {
		f, err := os.Open(s.st.Path(name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var r credRequest
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				all = append(all, r)
			}
		}
		f.Close()
	}
	slices.Reverse(all)
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// ---- the owner's side ----

func (s *Server) handleCredentialRequests(w http.ResponseWriter, r *http.Request, p *principal) {
	s.credReqs.mu.Lock()
	items := make([]credRequest, 0, len(s.credReqs.m))
	for _, x := range s.credReqs.m {
		items = append(items, x.copy())
	}
	s.credReqs.mu.Unlock()
	slices.SortFunc(items, func(a, b credRequest) int { return a.CreatedAt.Compare(b.CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func credBy(p *principal) string {
	if p.Kind == "device" {
		return "device " + p.DeviceID
	}
	return p.Kind
}

func (s *Server) handleCredentialApprove(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Values []string `json:"values"`
	}
	if !decode(w, r, &req) {
		return
	}
	view, err := s.credApprove(r.PathValue("id"), req.Values, credBy(p))
	if err != nil {
		credErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleCredentialDeny(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	note := strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(note) > credNoteMax {
		note = string([]rune(note)[:credNoteMax])
	}
	text := "The owner denied this request."
	if note != "" {
		text += " They said: " + note
	}
	id := r.PathValue("id")
	if !s.credEnd(id, "waiting", "denied", text, true, func(x *credRequest) { x.Note, x.By = note, credBy(p) }) {
		if _, ok := s.credReqs.view(id); !ok {
			credErr(w, errCredGone)
		} else {
			credErr(w, errCredDecided)
		}
		return
	}
	v, _ := s.credReqs.view(id)
	writeJSON(w, http.StatusOK, v)
}

func credErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errCredGone):
		writeErrCode(w, http.StatusNotFound, "request_not_found", err.Error())
	case errors.Is(err, errCredDecided):
		writeErrCode(w, http.StatusConflict, "request_decided", err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

func (s *Server) handleCredentialLog(w http.ResponseWriter, r *http.Request, p *principal) {
	limit := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, credLogRead)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.credLogRead(limit)})
}

func (s *Server) handleVaultAgents(w http.ResponseWriter, r *http.Request, p *principal) {
	items, err := s.credVault.AgentEntries()
	if err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleVaultAgentPut(w http.ResponseWriter, r *http.Request, p *principal) {
	var e vault.AgentEntry
	if !decode(w, r, &e) {
		return
	}
	e.ID = r.PathValue("id")
	for _, pid := range e.Projects {
		if _, ok := s.projects.Get(pid); !ok {
			writeErrCode(w, http.StatusBadRequest, "project_not_found", fmt.Sprintf("project %s does not exist", pid))
			return
		}
	}
	saved, err := s.credVault.SetAgentEntry(e)
	if err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entry": saved})
}
