package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/machine"
)

// The hidden runner (docs/API-studio.md §2) drives a CLI in headless mode
// (`claude -p`, `codex exec`) on the project's machine. It never runs with
// yolo flags, NextBoard hooks or --bare, and only ever gets read-only tools
// or no tools at all.

const (
	defaultRunnerTimeout = 10 * time.Minute
	defaultRunnerIdle    = 200 * time.Second
	// missingMarker is printed by the runner script when the CLI is not on
	// the login PATH.
	missingMarker = "__NB_CLI_MISSING__"
	// SchemaFileArg stands for the codex --output-schema file in RunnerArgv;
	// the script replaces it with the file in its mktemp folder.
	SchemaFileArg = "__NB_SCHEMA_FILE__"
	errTail       = 400
)

// readOnlyTools are the only tools a read-only purpose may use.
var readOnlyTools = []string{"Read", "Glob", "Grep"}

// purposePolicy is the fixed tool policy and default limits of a purpose.
type purposePolicy struct {
	readOnly bool          // Read/Glob/Grep only; otherwise no tools at all
	partial  bool          // cancel/timeout keeps the text streamed so far
	timeout  time.Duration // default total timeout (0 = runner default)
}

var purposes = map[string]purposePolicy{
	"document":        {readOnly: true, partial: true},
	"diagram":         {readOnly: true},
	"memory-scan":     {readOnly: true},
	"memory-init":     {readOnly: true},
	"memory-refresh":  {readOnly: true},
	"loop-goal":       {},
	"planner":         {timeout: 2 * time.Minute},
	"handoff-summary": {timeout: 4 * time.Minute},
	"commit-subject":  {timeout: 2 * time.Minute},
	"thread-summary":  {timeout: 30 * time.Second},
	"code-edit":       {readOnly: true, timeout: 3 * time.Minute},
	"code-complete":   {timeout: 30 * time.Second},
	"code-ask":        {readOnly: true, partial: true, timeout: 5 * time.Minute},
}

// RunnerRequest describes one hidden runner job.
type RunnerRequest struct {
	Purpose   string
	ProjectID string
	MachineID string // "" = This PC; planner always runs on This PC
	Cwd       string // planner: <data>/runner/planner
	Agent     string // "claude" | "codex"
	Command   string // binary to run (settings path override); "" = Agent
	Env       []string
	Prompt    string // sent on stdin
	Schema    json.RawMessage
	Model     string
	Stream    bool // claude: stream-json (+ jobOutput) instead of json
	Timeout   time.Duration
	// IdleTimeout kills a run without any output for that long.
	IdleTimeout time.Duration
	OnFinish    func(Job)
	// Remote, when set, gives the CLI on This PC the hub's read-only tools on
	// a server instead of its local ones.
	Remote *RemoteRunner
	// Transform, when set, turns the runner output into the purpose's
	// result shape (e.g. memory-scan → {"facts":[…]}); an error fails the
	// job with code bad_output.
	Transform func(text string, structured json.RawMessage) (json.RawMessage, error)
}

// RemoteRunner is what a claude run needs to read a project on a server.
type RemoteRunner struct {
	MCPConfig string
	Tools     []string // allowed, e.g. mcp__nextboard__read_file
	Prompt    string   // appended system prompt: where the project is
}

// ReadsProject reports whether a purpose needs to read the project (the
// others get all they need in the prompt).
func ReadsProject(purpose string) bool { return purposes[purpose].readOnly }

// runnerResult is Job.Result of a runner job.
type runnerResult struct {
	Text           string          `json:"text"`
	Structured     json.RawMessage `json:"structured,omitempty"`
	AgentSessionID string          `json:"agentSessionId,omitempty"`
	Partial        bool            `json:"partial,omitempty"`
}

// SubmitRunner validates req and queues a runner job.
func (m *Manager) SubmitRunner(req RunnerRequest) (Job, error) {
	pol, ok := purposes[req.Purpose]
	if !ok {
		return Job{}, ErrBadPurpose
	}
	if req.Agent != "claude" && req.Agent != "codex" {
		return Job{}, ErrRunnerUnsupported
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return Job{}, fmt.Errorf("%w: prompt kosong", ErrBadRequest)
	}
	if len(req.Schema) > 0 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, req.Schema); err != nil {
			return Job{}, fmt.Errorf("%w: schema bukan JSON", ErrBadRequest)
		}
		req.Schema = compact.Bytes()
	}
	if strings.ContainsAny(req.Command, "\x00\n") {
		return Job{}, fmt.Errorf("%w: command", ErrBadRequest)
	}
	if req.Purpose == "planner" || req.MachineID == "" {
		req.MachineID = machine.LocalID
	}
	if req.Purpose == "planner" {
		dir := m.st.Path("runner", "planner")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Job{}, err
		}
		req.Cwd = dir
	}
	if req.Timeout <= 0 {
		req.Timeout = pol.timeout
		if req.Timeout <= 0 {
			req.Timeout = defaultRunnerTimeout
		}
	}
	if req.IdleTimeout <= 0 {
		req.IdleTimeout = min(defaultRunnerIdle, req.Timeout)
	}
	j := &job{
		Job: Job{
			ID: auth.RandomID("jb_"), Kind: KindRunner, Purpose: req.Purpose,
			ProjectID: req.ProjectID, MachineID: req.MachineID, Agent: req.Agent,
			State: StateQueued, Progress: Progress{Phase: "queued", Text: "Queued"},
			CreatedAt: time.Now().UTC(),
		},
		req:  req,
		done: make(chan struct{}),
	}
	if err := m.enqueue(j); err != nil {
		return Job{}, err
	}
	v, _ := m.Get(j.ID)
	return v, nil
}

// RunnerArgv is the CLI argv for req (prompt on stdin). For codex with a
// schema the --output-schema value is SchemaFileArg.
func RunnerArgv(req RunnerRequest) []string {
	pol := purposes[req.Purpose]
	cmd := req.Command
	if cmd == "" {
		cmd = req.Agent
	}
	if req.Agent == "codex" {
		argv := []string{cmd, "exec", "--json", "--sandbox", "read-only", "--skip-git-repo-check", "--ephemeral"}
		if strings.HasPrefix(req.Cwd, "/") {
			argv = append(argv, "-C", req.Cwd)
		}
		if req.Model != "" {
			argv = append(argv, "-m", req.Model)
		}
		if len(req.Schema) > 0 {
			argv = append(argv, "--output-schema", SchemaFileArg)
		}
		return append(argv, "-")
	}
	argv := []string{cmd, "-p"}
	if req.Stream {
		argv = append(argv, "--output-format", "stream-json", "--verbose")
	} else {
		argv = append(argv, "--output-format", "json")
	}
	argv = append(argv, "--permission-mode", "dontAsk", "--no-session-persistence")
	if req.Remote != nil {
		argv = append(argv, "--tools", "", "--mcp-config", req.Remote.MCPConfig, "--strict-mcp-config",
			"--allowedTools", strings.Join(req.Remote.Tools, ","), "--append-system-prompt", req.Remote.Prompt)
	} else if pol.readOnly {
		tools := strings.Join(readOnlyTools, ",")
		argv = append(argv, "--tools", tools, "--allowedTools", tools)
	} else {
		argv = append(argv, "--tools", "", "--max-turns", "1")
	}
	if len(req.Schema) > 0 {
		argv = append(argv, "--json-schema", string(req.Schema))
	}
	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}
	return argv
}

// runnerScript wraps the argv: a missing CLI is reported with
// missingMarker; codex with a schema gets a private mktemp folder under
// /tmp (owner decision #2) that is always removed, and only that folder.
func runnerScript(req RunnerRequest) string {
	argv := RunnerArgv(req)
	check := "command -v " + machine.Quote(argv[0]) + " >/dev/null 2>&1 || { echo " + missingMarker + " >&2; exit 127; }; "
	if req.Agent != "codex" || len(req.Schema) == 0 {
		return check + "exec " + machine.Join(argv...)
	}
	run := strings.Replace(machine.Join(argv...), SchemaFileArg, `"$d/schema.json"`, 1)
	return check +
		`d=$(mktemp -d /tmp/nb-run.XXXXXX) || exit 125; ` +
		`case "$d" in /tmp/nb-run.??????) ;; *) echo 'mktemp tidak terduga' >&2; exit 125;; esac; ` +
		`trap 'rm -rf -- "$d"' EXIT; ` +
		`printf '%s' ` + machine.Quote(string(req.Schema)) + ` > "$d/schema.json" || exit 125; ` +
		`cat > "$d/prompt" || exit 125; ` +
		run + ` < "$d/prompt" & c=$!; ` +
		`trap 'kill -TERM "$c" 2>/dev/null; t=1' TERM INT HUP; ` +
		`wait "$c"; rc=$?; if [ -n "$t" ]; then wait "$c" 2>/dev/null; rc=143; fi; exit $rc`
}

// run executes a runner job (one automatic retry when a schema was asked
// for and the output is not valid JSON).
func (m *Manager) run(ctx context.Context, j *job) {
	req := j.req
	h, ok := m.host(req.MachineID)
	if !ok {
		m.finish(j, StateFailed, "machine_not_found", machine.ErrNotFound.Error(), nil, true)
		return
	}
	var out *runOutcome
	var transformed json.RawMessage
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			m.setProgress(j, "launching", "Retrying…")
		}
		out = m.runOnce(ctx, j, h)
		// A result that does not fit the purpose's shape (e.g. a diagram
		// over the limits) counts as bad output: one automatic retry
		// (docs/API-studio.md §2).
		if out.code == "" && req.Transform != nil {
			b, err := req.Transform(out.text, out.structured)
			if err != nil {
				out.code, out.msg = "bad_output", "output runner tidak sesuai bentuk hasil: "+err.Error()
			} else {
				transformed = b
			}
		}
		if out.code != "bad_output" {
			break
		}
	}
	m.mu.Lock()
	userStop := j.userStop
	m.mu.Unlock()
	switch {
	case out.code == "" && req.Transform != nil:
		m.finish(j, StateDone, "", "", transformed, true)
	case out.code == "bad_output" && !userStop:
		m.finish(j, StateFailed, out.code, out.msg, nil, true)
	case out.code == "":
		b, _ := json.Marshal(runnerResult{Text: out.text, Structured: out.structured, AgentSessionID: out.sessionID})
		m.finish(j, StateDone, "", "", b, true)
	case out.code == "cancelled" || userStop:
		m.finish(j, StateCancelled, "", "", out.partialResult(req), true)
	default:
		m.finish(j, StateFailed, out.code, out.msg, out.partialResult(req), true)
	}
}

// runOutcome is the classified result of one CLI run.
type runOutcome struct {
	text, sessionID string
	structured      json.RawMessage
	code, msg       string
	partial         string
}

func (o *runOutcome) partialResult(req RunnerRequest) json.RawMessage {
	if !purposes[req.Purpose].partial || o.partial == "" {
		return nil
	}
	b, _ := json.Marshal(runnerResult{Text: o.partial, AgentSessionID: o.sessionID, Partial: true})
	return b
}

func (m *Manager) runOnce(ctx context.Context, j *job, h machine.Host) *runOutcome {
	req := j.req
	p := &streamParser{agent: req.Agent,
		phase: func(phase, text string) { m.setProgress(j, phase, text) },
		text: func(t string) {
			m.setProgress(j, "writing", "Writing…")
			m.output(j, t)
		},
	}
	res, err := machine.Exec(ctx, h, machine.ExecSpec{
		Cwd: req.Cwd, Script: runnerScript(req), Env: req.Env, Stdin: []byte(req.Prompt),
		Timeout: req.Timeout, IdleTimeout: req.IdleTimeout, OnStdout: p.write,
	})
	p.close()
	m.mu.Lock()
	j.types = p.types
	m.mu.Unlock()
	if errors.Is(err, machine.ErrNotConnected) || errors.Is(err, machine.ErrDisconnected) {
		return &runOutcome{code: "machine_offline", msg: err.Error(), partial: p.partial.String(), sessionID: p.sessionID}
	}
	if err != nil && res == nil {
		return &runOutcome{code: "runner_failed", msg: err.Error()}
	}
	if !p.haveFinal {
		p.fallback(res.Stdout)
	}
	m.setProgress(j, "finishing", "Finishing…")
	return classify(req, res, err, p)
}

// classify turns an exec result and the parsed stream into an outcome.
func classify(req RunnerRequest, res *machine.ExecResult, execErr error, p *streamParser) *runOutcome {
	o := &runOutcome{text: p.final, sessionID: p.sessionID, partial: p.partial.String()}
	stderr := string(res.Stderr)
	cmd := req.Command
	if cmd == "" {
		cmd = req.Agent
	}
	switch {
	case res.Cancelled:
		o.code = "cancelled"
		return o
	case res.TimedOut:
		o.code, o.msg = "timeout", fmt.Sprintf("runner melebihi batas waktu %s", req.Timeout)
		return o
	case res.IdleKilled:
		o.code, o.msg = "idle_timeout", fmt.Sprintf("runner tidak mengeluarkan output selama %s", req.IdleTimeout)
		return o
	case strings.Contains(stderr, missingMarker), res.ExitCode == 127:
		o.code, o.msg = "cli_missing", fmt.Sprintf("CLI %s tidak ditemukan di machine ini", cmd)
		return o
	}
	failed := execErr != nil || res.ExitCode != 0 || p.isError || !p.haveFinal
	if failed {
		diag := stderr + "\n" + p.errMsg
		if p.isError {
			diag += "\n" + p.final
		}
		if !p.haveFinal || p.isError {
			diag += "\n" + string(res.Stdout[max(0, len(res.Stdout)-4096):])
		}
		if notLoggedIn(diag) {
			o.code, o.msg = "cli_not_logged_in", fmt.Sprintf("%s belum login di machine ini", cmd)
			return o
		}
		o.code = "runner_failed"
		tail := strings.TrimSpace(p.errMsg)
		if tail == "" {
			tail = strings.TrimSpace(stderr)
		}
		if tail == "" && p.isError {
			tail = strings.TrimSpace(p.final)
		}
		if tail == "" && execErr != nil {
			tail = execErr.Error()
		}
		o.msg = fmt.Sprintf("runner gagal (kode keluar %d)", res.ExitCode)
		if tail != "" {
			o.msg += ": " + lastRunes(tail, errTail)
		}
		return o
	}
	if len(req.Schema) > 0 {
		s := p.structured
		if len(s) == 0 {
			s = extractJSON(p.final)
		}
		if len(s) == 0 {
			o.code, o.msg = "bad_output", "output runner bukan JSON yang valid"
			return o
		}
		o.structured = s
	}
	return o
}

// loginHints are lower-case fragments of "not logged in" errors of the
// Claude and Codex CLIs (checked only when a run failed).
var loginHints = []string{
	"not logged in", "please run /login", "run /login", "/login", "invalid api key", "invalid_api_key",
	"api key", "401", "unauthorized", "authentication_error", "authentication failed",
	"codex login", "please log in", "log in", "login required", "oauth token",
	"failed to authenticate", "oauth session",
}

func notLoggedIn(s string) bool {
	s = strings.ToLower(s)
	for _, h := range loginHints {
		if strings.Contains(s, h) {
			return true
		}
	}
	return false
}

// extractJSON returns s as compact JSON, also when wrapped in a ``` fence.
func extractJSON(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimPrefix(s, "json")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}
	var b bytes.Buffer
	if s == "" || json.Compact(&b, []byte(s)) != nil {
		return nil
	}
	return b.Bytes()
}

func lastRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-n:])
}

// streamParser reads the CLI's JSON lines as they arrive.
type streamParser struct {
	agent string
	phase func(phase, text string)
	text  func(string)

	buf        []byte
	final      string
	haveFinal  bool
	structured json.RawMessage
	sessionID  string
	isError    bool
	errMsg     string
	partial    strings.Builder
	types      []string // event types in order of first appearance
	seen       map[string]bool
}

func (p *streamParser) write(chunk []byte) {
	p.buf = append(p.buf, chunk...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		p.line(p.buf[:i])
		p.buf = p.buf[i+1:]
	}
	if len(p.buf) > 8<<20 { // a runaway line without newline
		p.buf = p.buf[:0]
	}
}

func (p *streamParser) close() {
	if len(bytes.TrimSpace(p.buf)) > 0 {
		p.line(p.buf)
	}
	p.buf = nil
}

// fallback parses the whole stdout (pretty-printed json output, or an
// array of messages) when no line carried the final result.
func (p *streamParser) fallback(stdout []byte) {
	stdout = bytes.TrimSpace(stdout)
	if len(stdout) == 0 {
		return
	}
	var arr []json.RawMessage
	if json.Unmarshal(stdout, &arr) == nil {
		for _, e := range arr {
			p.event(e)
		}
		return
	}
	if json.Valid(stdout) {
		p.event(stdout)
	}
}

func (p *streamParser) note(t string) {
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	if !p.seen[t] {
		p.seen[t] = true
		p.types = append(p.types, t)
	}
}

func (p *streamParser) line(b []byte) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return
	}
	p.event(b)
}

// event handles one JSON event of either CLI.
func (p *streamParser) event(b []byte) {
	var ev struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		// claude
		SessionID        string          `json:"session_id"`
		Message          json.RawMessage `json:"message"` // claude: object; codex error: string
		Result           *string         `json:"result"`
		IsError          bool            `json:"is_error"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		// codex
		ThreadID string `json:"thread_id"`
		Item     struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &ev) != nil {
		return
	}
	var msg struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	var msgText string
	if len(ev.Message) > 0 && ev.Message[0] == '{' {
		_ = json.Unmarshal(ev.Message, &msg)
	} else if len(ev.Message) > 0 {
		_ = json.Unmarshal(ev.Message, &msgText)
	}
	name := ev.Type
	if ev.Subtype != "" {
		name += "/" + ev.Subtype
	}
	if ev.Item.Type != "" {
		name += ":" + ev.Item.Type
	}
	p.note(name)
	switch ev.Type {
	// ---- claude stream-json / json ----
	case "system":
		if ev.SessionID != "" {
			p.sessionID = ev.SessionID
		}
		if ev.Subtype == "init" {
			p.phase("launching", "Starting…")
		}
	case "assistant":
		for _, c := range msg.Content {
			switch c.Type {
			case "text":
				if c.Text != "" {
					p.partial.WriteString(c.Text)
					p.text(c.Text)
				}
			case "tool_use", "thinking", "redacted_thinking":
				p.phase("thinking", "Thinking…")
			}
		}
	case "user":
		p.phase("thinking", "Thinking…")
	case "result":
		if ev.SessionID != "" {
			p.sessionID = ev.SessionID
		}
		if ev.Result != nil {
			p.final = *ev.Result
		}
		p.haveFinal = true
		p.isError = ev.IsError || (ev.Subtype != "" && ev.Subtype != "success")
		if len(ev.StructuredOutput) > 0 && string(ev.StructuredOutput) != "null" {
			p.structured = extractJSON(string(ev.StructuredOutput))
		}
		p.phase("finishing", "Finishing…")
	// ---- codex exec --json ----
	case "thread.started":
		if ev.ThreadID != "" {
			p.sessionID = ev.ThreadID
		}
		p.phase("launching", "Starting…")
	case "turn.started":
		p.phase("thinking", "Thinking…")
	case "item.started", "item.updated", "item.completed":
		switch ev.Item.Type {
		case "agent_message":
			if ev.Type == "item.completed" {
				p.final, p.haveFinal = ev.Item.Text, true
				p.partial.WriteString(ev.Item.Text)
				p.text(ev.Item.Text)
			}
		case "error":
			p.errMsg = ev.Item.Text
		default:
			p.phase("thinking", "Thinking…")
		}
	case "turn.completed":
		p.isError, p.errMsg = false, "" // earlier "error" events were retried
		p.phase("finishing", "Finishing…")
	case "turn.failed", "error":
		p.isError = true
		p.errMsg = errorText(ev.Error, msgText)
	}
}

// errorText extracts a message from {"message":…} or a plain string.
func errorText(raw json.RawMessage, msg string) string {
	if msg != "" {
		return msg
	}
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		return e.Message
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}
