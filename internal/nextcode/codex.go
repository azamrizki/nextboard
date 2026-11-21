package nextcode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The Codex engine speaks Codex's app-server protocol (`codex app-server`, checked against Codex
// CLI 0.165.0 and its `codex app-server generate-json-schema`): JSON-RPC 2.0 without the
// "jsonrpc" field, one JSON object per line each way. The hub is the client: it opens a thread
// (or resumes one), starts a turn per message, and answers the server's approval requests and
// questions. A Codex CLI holds one thread here, the console's conversation.

// Codex methods the engine uses or answers.
const (
	codexCommandApproval = "item/commandExecution/requestApproval"
	codexFileApproval    = "item/fileChange/requestApproval"
	codexUserInput       = "item/tool/requestUserInput"
	codexPermissions     = "item/permissions/requestApproval"
	codexElicitation     = "mcpServer/elicitation/request"
)

// codexMode is the approval policy, sandbox mode and approvals reviewer of a permission mode;
// "" for the CLI's own (its config).
func codexMode(mode string) (approval, sandbox, reviewer string) {
	switch mode {
	case "":
		return "", "", ""
	case "plan":
		return "on-request", "read-only", "user"
	case "auto":
		return "on-request", "workspace-write", "auto_review"
	case "bypassPermissions":
		return "never", "danger-full-access", "user"
	default: // default, acceptEdits
		return "on-request", "workspace-write", "user"
	}
}

// codexSandboxType is the SandboxPolicy type (turn/start) of a sandbox mode (thread/start).
var codexSandboxType = map[string]string{"read-only": "readOnly", "workspace-write": "workspaceWrite",
	"danger-full-access": "dangerFullAccess"}

type codexEngine struct {
	// decode runs on the driver's reader, the rest under the driver's lock: they meet here
	mu sync.Mutex

	mode        string
	launchModel string   // the launch's -m: a resumed thread uses it too
	addDirs     []string // the launch's --add-dir: writable roots of the workspace sandbox
	model       string   // setModel's, for the next turns ("" = the thread's)

	n      int            // the last request id
	calls  map[int]string // requests waiting for their response: id → method
	resume string         // the thread to resume once initialized ("" = a new one)

	thread  string          // the conversation's thread, once open
	sandbox json.RawMessage // its sandbox policy as Codex resolved it
	busy    bool            // a turn was started and has not completed
	turnReq int             // the turn/start of it
	turn    string          // its id, once known
	queue   []string        // messages waiting for the thread or for the turn's end
	usage   *Usage          // the latest token usage of the turn

	inputs     map[string]json.RawMessage // tool inputs of the turn by item id (approvals show them)
	summarized map[string]bool            // reasoning items streamed as summaries
	reqs       map[string]*codexReq       // server requests waiting for the owner, by Request.ID
}

// codexReq is a server request as the answer needs it.
type codexReq struct {
	id        json.RawMessage // the JSON-RPC id as given: the answer echoes it, same JSON type
	method    string
	turn      string          // the turn it belongs to ("" = none given)
	always    json.RawMessage // a command's decision for allowAlways ("" = acceptForSession)
	questions []codexQuestion
	perms     json.RawMessage // what a permissions request asks for
}

type codexQuestion struct{ id, text string }

func newCodexEngine() *codexEngine {
	return &codexEngine{calls: map[int]string{}, inputs: map[string]json.RawMessage{}, summarized: map[string]bool{},
		reqs: map[string]*codexReq{}}
}

func (e *codexEngine) requestLocked(method string, params any) []byte {
	e.n++
	e.calls[e.n] = method
	return line(map[string]any{"id": e.n, "method": method, "params": params})
}

// tomlString is a TOML string: a JSON string is one.
func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// argv is `codex app-server` with the launch's config: its -c pairs and --enable/--disable
// features kept, -m as -c model="…", --add-dir kept for the sandbox (app-server has no such
// flag); the rest (a resume subcommand and its id, the TUI's own flags) is not app-server's.
func (e *codexEngine) argv(base []string, mode string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mode = mode
	if len(base) == 0 {
		return nil
	}
	out := []string{base[0], "app-server"}
	for i := 1; i < len(base); i++ {
		a := base[i]
		if i+1 >= len(base) {
			break // every flag kept here has a value
		}
		switch a {
		case "-c", "--config":
			i++
			out = append(out, "-c", base[i])
		case "-m", "--model":
			i++
			e.launchModel = base[i]
			out = append(out, "-c", "model="+tomlString(base[i]))
		case "--enable", "--disable":
			i++
			out = append(out, a, base[i])
		case "--add-dir":
			i++
			e.addDirs = append(e.addDirs, base[i])
		}
	}
	return out
}

// start initializes the connection; the thread opens when Codex answers (decode).
func (e *codexEngine) start(resume bool, conversation string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resume = ""
	if resume {
		e.resume = conversation
	}
	return e.requestLocked("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "nextboard", "title": "NextBoard", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true}})
}

// threadLocked opens the conversation: thread/resume when it has one, else thread/start.
func (e *codexEngine) threadLocked() []byte {
	p := map[string]any{}
	if approval, sandbox, reviewer := codexMode(e.mode); approval != "" {
		p["approvalPolicy"], p["sandbox"], p["approvalsReviewer"] = approval, sandbox, reviewer
	}
	if e.resume == "" {
		return e.requestLocked("thread/start", p)
	}
	p["threadId"] = e.resume
	p["excludeTurns"] = true // the chat is the hub's: no history to send back
	if e.launchModel != "" {
		p["model"] = e.launchModel
	}
	return e.requestLocked("thread/resume", p)
}

func (e *codexEngine) message(text string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.thread == "" || e.busy {
		e.queue = append(e.queue, text) // sent when the thread opens or the turn ends (decode)
		return nil
	}
	return e.turnStartLocked(text)
}

func (e *codexEngine) turnStartLocked(text string) []byte {
	p := e.policyLocked()
	p["threadId"] = e.thread
	p["input"] = []any{map[string]any{"type": "text", "text": text}}
	if e.model != "" {
		p["model"] = e.model
	}
	b := e.requestLocked("turn/start", p)
	e.busy, e.turnReq, e.turn = true, e.n, ""
	return b
}

// nextLocked starts the turn of the next queued message, when the thread is free.
func (e *codexEngine) nextLocked() []ev {
	if e.thread == "" || e.busy || len(e.queue) == 0 {
		return nil
	}
	text := e.queue[0]
	e.queue = e.queue[1:]
	return []ev{{kind: evReply, reply: e.turnStartLocked(text)}}
}

// policyLocked is the mode as turn/start overrides it: the approval policy and reviewer, and the
// sandbox policy, which keeps what Codex resolved for the thread (its writable roots, network)
// when the type is the same, with the launch's --add-dir folders writable.
func (e *codexEngine) policyLocked() map[string]any {
	p := map[string]any{}
	approval, sandbox, reviewer := codexMode(e.mode)
	if approval != "" {
		p["approvalPolicy"], p["approvalsReviewer"] = approval, reviewer
	}
	var pol map[string]any
	_ = json.Unmarshal(e.sandbox, &pol)
	want := codexSandboxType[sandbox]
	if want == "" { // the CLI's own mode: only the folders to add
		if want, _ = pol["type"].(string); want != "workspaceWrite" || len(e.addDirs) == 0 {
			return p
		}
	}
	if pol == nil || pol["type"] != want {
		pol = map[string]any{"type": want}
	}
	if want == "workspaceWrite" && len(e.addDirs) > 0 {
		roots, _ := pol["writableRoots"].([]any)
		for _, d := range e.addDirs {
			if !slices.Contains(roots, any(d)) {
				roots = append(roots, d)
			}
		}
		pol["writableRoots"] = roots
	}
	p["sandboxPolicy"] = pol
	return p
}

func (e *codexEngine) interrupt() []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.interruptLocked()
}

func (e *codexEngine) interruptLocked() []byte {
	if !e.busy || e.turn == "" || e.thread == "" {
		return nil
	}
	return e.requestLocked("turn/interrupt", map[string]any{"threadId": e.thread, "turnId": e.turn})
}

// setModel applies from the next turn (turn/start's model).
func (e *codexEngine) setModel(model string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.model = model
	return nil
}

// setMode applies from the next turn (turn/start's approval and sandbox policies).
func (e *codexEngine) setMode(mode string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mode = mode
	return nil
}

// answer is the JSON-RPC response to a server request; a denyStop that Codex has no decision for
// also interrupts the turn.
func (e *codexEngine) answer(r *Request, a Answer) ([]byte, error) {
	switch a.Decision {
	case Allow, AllowAlways, Deny, DenyStop:
	default:
		return nil, fmt.Errorf("unknown decision %q", a.Decision)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	q, ok := e.reqs[r.ID]
	if !ok {
		return nil, ErrNoRequest
	}
	allow := a.Decision == Allow || a.Decision == AllowAlways
	stop := a.Decision == DenyStop
	var result any
	switch q.method {
	case codexCommandApproval, codexFileApproval:
		var d any = "accept"
		switch a.Decision {
		case AllowAlways:
			d = "acceptForSession"
			if len(q.always) > 0 {
				d = q.always
			}
		case Deny:
			d = "decline"
		case DenyStop:
			d, stop = "cancel", false // cancel stops the turn
		}
		result = map[string]any{"decision": d}
	case codexUserInput:
		answers := map[string]any{}
		if allow {
			for _, qq := range q.questions {
				if labels := a.Answers[qq.text]; len(labels) > 0 {
					answers[qq.id] = map[string]any{"answers": labels}
				}
			}
			if a.Message != "" && len(q.questions) > 0 {
				if _, ok := answers[q.questions[0].id]; !ok {
					answers[q.questions[0].id] = map[string]any{"answers": []string{a.Message}}
				}
			}
		}
		result = map[string]any{"answers": answers}
	case codexPermissions:
		res := map[string]any{"permissions": map[string]any{}}
		if allow {
			if len(q.perms) > 0 && string(q.perms) != "null" {
				res["permissions"] = q.perms
			}
			res["scope"] = "turn"
			if a.Decision == AllowAlways {
				res["scope"] = "session"
			}
		}
		result = res
	case codexElicitation:
		switch a.Decision {
		case Allow, AllowAlways:
			result = map[string]any{"action": "accept", "content": map[string]any{}}
		case Deny:
			result = map[string]any{"action": "decline"}
		default:
			result = map[string]any{"action": "cancel"}
		}
	}
	delete(e.reqs, r.ID)
	b := line(map[string]any{"id": q.id, "result": result})
	if stop {
		b = append(b, e.interruptLocked()...)
	}
	return b, nil
}

type codexLine struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *codexError     `json:"error"`
}

type codexError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// decode: a line with an id and no method is a response to the hub, with both a request from
// Codex, with a method only a notification.
func (e *codexEngine) decode(b []byte) []ev {
	var l codexLine
	if json.Unmarshal(b, &l) != nil {
		return nil
	}
	hasID := len(l.ID) > 0 && string(l.ID) != "null"
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case l.Method == "" && hasID:
		return e.responseLocked(l.ID, l.Result, l.Error)
	case hasID:
		return e.serverRequestLocked(l.ID, l.Method, l.Params)
	case l.Method != "":
		return e.notificationLocked(l.Method, l.Params)
	}
	return nil
}

func (e *codexEngine) responseLocked(id, result json.RawMessage, rerr *codexError) []ev {
	n, err := strconv.Atoi(string(id))
	if err != nil {
		return nil
	}
	method, ok := e.calls[n]
	if !ok {
		return nil
	}
	delete(e.calls, n)
	switch method {
	case "initialize":
		if rerr != nil {
			return e.failLocked("Codex could not start: " + rerr.Message)
		}
		return []ev{{kind: evReply, reply: append(line(map[string]any{"method": "initialized"}), e.threadLocked()...)}}
	case "thread/start", "thread/resume":
		if rerr != nil {
			if method == "thread/resume" {
				e.resume = ""
				return []ev{{kind: evNotice, text: "Could not continue the conversation; a new one started."},
					{kind: evReply, reply: e.threadLocked()}}
			}
			return e.failLocked("Could not start the conversation: " + rerr.Message)
		}
		var r struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
			Model   string          `json:"model"`
			Sandbox json.RawMessage `json:"sandbox"`
		}
		if json.Unmarshal(result, &r) != nil || r.Thread.ID == "" {
			return e.failLocked("Could not start the conversation: Codex gave no thread.")
		}
		e.thread, e.sandbox = r.Thread.ID, r.Sandbox
		return append([]ev{{kind: evInit, conv: r.Thread.ID, model: r.Model}}, e.nextLocked()...)
	case "turn/start":
		if n != e.turnReq || !e.busy {
			return nil
		}
		if rerr != nil {
			e.busy, e.turn = false, ""
			return append([]ev{{kind: evTurnEnd, failed: true, text: rerr.Message}}, e.nextLocked()...)
		}
		var r struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(result, &r) == nil && e.turn == "" {
			e.turn = r.Turn.ID
		}
	}
	return nil
}

// failLocked: Codex cannot take messages. A message waiting for it fails as its turn would.
func (e *codexEngine) failLocked(msg string) []ev {
	if e.busy || len(e.queue) > 0 {
		e.busy, e.turn, e.queue = false, "", nil
		return []ev{{kind: evTurnEnd, failed: true, text: msg}}
	}
	return []ev{{kind: evNote, text: msg}}
}

// ours: a notification of this conversation's thread (Codex's sub-agents run in threads of their
// own, which the chat does not show).
func (e *codexEngine) ours(thread string) bool {
	return thread == "" || e.thread == "" || thread == e.thread
}

func (e *codexEngine) notificationLocked(method string, raw json.RawMessage) []ev {
	var p struct {
		ThreadID string          `json:"threadId"`
		ItemID   string          `json:"itemId"`
		Delta    string          `json:"delta"`
		Index    int             `json:"summaryIndex"`
		Item     json.RawMessage `json:"item"`
		Thread   struct {
			ID     string `json:"id"`
			Parent string `json:"parentThreadId"`
			Model  string `json:"model"`
		} `json:"thread"`
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
		Changes json.RawMessage `json:"changes"`
		Usage   struct {
			Last struct {
				Input      int `json:"inputTokens"`
				Cached     int `json:"cachedInputTokens"`
				CacheWrite int `json:"cacheWriteInputTokens"`
				Output     int `json:"outputTokens"`
			} `json:"last"`
		} `json:"tokenUsage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message   string          `json:"message"`
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	if !e.ours(p.ThreadID) {
		return nil
	}
	switch method {
	case "thread/started":
		if e.thread != "" || p.Thread.ID == "" || p.Thread.Parent != "" {
			return nil
		}
		e.thread = p.Thread.ID
		return []ev{{kind: evInit, conv: p.Thread.ID, model: p.Thread.Model}}
	case "turn/started":
		e.busy, e.turn = true, p.Turn.ID
		return []ev{{kind: evActive}}
	case "turn/completed":
		end := ev{kind: evTurnEnd, usage: e.usage}
		if p.Turn.Status == "failed" { // interrupted is a normal end: the driver says "Stopped."
			end.failed = true
			if p.Turn.Error != nil {
				end.text = p.Turn.Error.Message
			}
			if end.text == "" {
				end.text = "The turn failed."
			}
		}
		out := []ev{end}
		for id, q := range e.reqs { // nothing answers a request of a turn that is over
			if q.turn != "" && q.turn == p.Turn.ID {
				delete(e.reqs, id)
				out = append(out, ev{kind: evCancel, id: id})
			}
		}
		e.busy, e.turn, e.usage = false, "", nil
		clear(e.inputs)
		clear(e.summarized)
		return append(out, e.nextLocked()...)
	case "item/started", "item/completed":
		return e.itemLocked(method == "item/started", p.Item)
	case "item/agentMessage/delta":
		return []ev{{kind: evDelta, id: p.ItemID, sub: KindText, text: p.Delta}}
	case "item/reasoning/summaryTextDelta":
		e.summarized[p.ItemID] = true
		return []ev{{kind: evDelta, id: p.ItemID, sub: KindThinking, text: p.Delta}}
	case "item/reasoning/summaryPartAdded":
		if p.Index > 0 {
			return []ev{{kind: evDelta, id: p.ItemID, sub: KindThinking, text: "\n\n"}}
		}
	case "item/reasoning/textDelta":
		if !e.summarized[p.ItemID] { // the raw reasoning, when the model gives no summary
			return []ev{{kind: evDelta, id: p.ItemID, sub: KindThinking, text: p.Delta}}
		}
	case "item/fileChange/patchUpdated":
		if _, ok := e.inputs[p.ItemID]; ok && len(p.Changes) > 0 {
			var ch []codexChange
			if json.Unmarshal(p.Changes, &ch) == nil {
				in := patchInput(ch)
				e.inputs[p.ItemID] = in
				return []ev{{kind: evItem, item: &Item{ID: p.ItemID, Kind: KindTool, Tool: &Tool{Name: "Patch", Input: in, Status: ToolRunning}}}}
			}
		}
	case "thread/tokenUsage/updated":
		u := p.Usage.Last
		e.usage = &Usage{Input: max(u.Input-u.Cached, 0), Output: u.Output, CacheRead: u.Cached, CacheWrite: u.CacheWrite}
	case "account/rateLimits/updated":
		return []ev{{kind: evRate, raw: append(json.RawMessage{}, raw...)}}
	case "error":
		return []ev{{kind: evNote, text: p.Error.Message}}
	case "warning":
		return []ev{{kind: evNote, text: p.Message}}
	case "serverRequest/resolved":
		id := idText(p.RequestID)
		if _, ok := e.reqs[id]; ok { // Codex settled it itself (the turn ended, it timed out)
			delete(e.reqs, id)
			return []ev{{kind: evCancel, id: id}}
		}
	}
	return nil
}

type codexItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Text             string          `json:"text"`
	Summary          []string        `json:"summary"`
	Content          json.RawMessage `json:"content"`
	Command          string          `json:"command"`
	Cwd              string          `json:"cwd"`
	Status           string          `json:"status"`
	AggregatedOutput *string         `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`
	Changes          []codexChange   `json:"changes"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Result           *struct {
		Content json.RawMessage `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Query string `json:"query"`
}

type codexChange struct {
	Path string          `json:"path"`
	Kind json.RawMessage `json:"kind"` // {"type": "add"|"delete"|"update", "move_path"?}
	Diff string          `json:"diff"`
}

// kind is the change's kind ("add", "delete", "update") and where an update moves the file.
func (c codexChange) kind() (string, string) {
	var s string
	if json.Unmarshal(c.Kind, &s) == nil {
		return s, ""
	}
	var k struct {
		Type     string `json:"type"`
		MovePath string `json:"move_path"`
	}
	_ = json.Unmarshal(c.Kind, &k)
	return k.Type, k.MovePath
}

// patchInput is a file change as its tool call's input: {"changes": [{path, kind, diff}]}.
func patchInput(changes []codexChange) json.RawMessage {
	list := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		kind, move := c.kind()
		m := map[string]any{"path": c.Path, "kind": kind, "diff": c.Diff}
		if move != "" {
			m["movePath"] = move
		}
		list = append(list, m)
	}
	b, _ := json.Marshal(map[string]any{"changes": list})
	return b
}

// codexTool is a tool item as a tool call: its name and input.
func codexTool(it codexItem) (string, json.RawMessage) {
	switch it.Type {
	case "commandExecution":
		b, _ := json.Marshal(map[string]any{"command": it.Command, "cwd": it.Cwd})
		return "Bash", b
	case "fileChange":
		return "Patch", patchInput(it.Changes)
	case "mcpToolCall":
		in := it.Arguments
		if len(in) == 0 || string(in) == "null" {
			in = json.RawMessage("{}")
		}
		return "mcp__" + it.Server + "__" + it.Tool, in
	case "webSearch":
		b, _ := json.Marshal(map[string]any{"query": it.Query})
		return "WebSearch", b
	}
	return "", nil
}

// codexResult is a completed tool item's output and whether it failed.
func codexResult(it codexItem) (string, bool) {
	failed := it.Status == "failed" || it.Status == "declined"
	switch it.Type {
	case "commandExecution":
		out := ""
		if it.AggregatedOutput != nil {
			out = *it.AggregatedOutput
		}
		return out, failed || (it.ExitCode != nil && *it.ExitCode != 0)
	case "fileChange":
		var b strings.Builder
		for _, c := range it.Changes {
			kind, move := c.kind()
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			switch {
			case move != "":
				fmt.Fprintf(&b, "Moved %s to %s", c.Path, move)
			case kind == "add":
				b.WriteString("Added " + c.Path)
			case kind == "delete":
				b.WriteString("Deleted " + c.Path)
			default:
				b.WriteString("Updated " + c.Path)
			}
		}
		return b.String(), failed
	case "mcpToolCall":
		if it.Error != nil {
			return it.Error.Message, true
		}
		if it.Result != nil {
			return toolOutput(it.Result.Content), failed
		}
		return "", failed
	}
	return "", failed
}

func (e *codexEngine) itemLocked(started bool, raw json.RawMessage) []ev {
	var it codexItem
	if json.Unmarshal(raw, &it) != nil || it.ID == "" {
		return nil
	}
	switch it.Type {
	case "agentMessage":
		if started {
			return []ev{{kind: evBlock, id: it.ID, sub: KindText}}
		}
		return []ev{{kind: evItem, item: &Item{ID: it.ID, Kind: KindText, Text: it.Text, Stream: it.ID}}}
	case "reasoning":
		if started {
			return []ev{{kind: evBlock, id: it.ID, sub: KindThinking}}
		}
		text := strings.Join(it.Summary, "\n\n")
		if text == "" {
			var content []string
			_ = json.Unmarshal(it.Content, &content)
			text = strings.Join(content, "\n\n")
		}
		delete(e.summarized, it.ID)
		return []ev{{kind: evItem, item: &Item{ID: it.ID, Kind: KindThinking, Text: text, Stream: it.ID}}}
	case "commandExecution", "fileChange", "mcpToolCall", "webSearch":
		name, in := codexTool(it)
		call := func() ev {
			return ev{kind: evItem, item: &Item{ID: it.ID, Kind: KindTool, Tool: &Tool{Name: name, Input: in, Status: ToolRunning}}}
		}
		if started {
			e.inputs[it.ID] = in
			return []ev{call()}
		}
		var out []ev
		if prev, ok := e.inputs[it.ID]; !ok || !bytes.Equal(prev, in) { // unseen, or its input grew (a patch, a query)
			out = append(out, call())
		}
		delete(e.inputs, it.ID)
		text, failed := codexResult(it)
		return append(out, ev{kind: evResult, id: it.ID, text: text, isError: failed})
	case "contextCompaction":
		if !started {
			return []ev{{kind: evNotice, text: "The conversation was compacted."}}
		}
	}
	return nil
}

// idText is a JSON-RPC id as a request id of the chat: a string's text, a number's digits.
func idText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

func (e *codexEngine) serverRequestLocked(id json.RawMessage, method string, raw json.RawMessage) []ev {
	var p struct {
		TurnID      *string           `json:"turnId"`
		ItemID      string            `json:"itemId"`
		Command     string            `json:"command"`
		Cwd         string            `json:"cwd"`
		Reason      string            `json:"reason"`
		Decisions   []json.RawMessage `json:"availableDecisions"`
		Network     json.RawMessage   `json:"networkApprovalContext"`
		GrantRoot   string            `json:"grantRoot"`
		Permissions json.RawMessage   `json:"permissions"`
		ServerName  string            `json:"serverName"`
		Message     string            `json:"message"`
		Questions   []struct {
			ID       string `json:"id"`
			Header   string `json:"header"`
			Question string `json:"question"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	_ = json.Unmarshal(raw, &p)
	q := &codexReq{id: append(json.RawMessage{}, id...), method: method}
	if p.TurnID != nil {
		q.turn = *p.TurnID
	}
	r := &Request{ID: idText(id), Kind: ReqPermission, ToolUseID: p.ItemID, Reason: p.Reason}
	switch method {
	case codexCommandApproval:
		r.Tool = "Bash"
		in := map[string]any{"command": p.Command, "cwd": p.Cwd}
		if p.Command == "" { // a network approval: the command is the item's
			var item map[string]any
			if json.Unmarshal(e.inputs[p.ItemID], &item) == nil {
				in["command"] = item["command"]
			}
		}
		if len(p.Network) > 0 && string(p.Network) != "null" {
			in["network"] = p.Network
		}
		r.Input, _ = json.Marshal(in)
		r.CanRemember, q.always = codexAlways(p.Decisions)
	case codexFileApproval:
		r.Tool, r.BlockedPath, r.CanRemember = "Patch", p.GrantRoot, true // acceptForSession is always one of its decisions
		r.Input = e.inputs[p.ItemID]
		if len(r.Input) == 0 {
			r.Input = json.RawMessage(`{"changes":[]}`)
		}
	case codexUserInput:
		// in AskUserQuestion's shape, which the views draw
		r.Kind, r.Tool = ReqQuestion, "AskUserQuestion"
		qs := make([]any, 0, len(p.Questions))
		for _, x := range p.Questions {
			opts := make([]any, 0, len(x.Options))
			for _, o := range x.Options {
				opts = append(opts, map[string]any{"label": o.Label, "description": o.Description})
			}
			qs = append(qs, map[string]any{"question": x.Question, "header": x.Header, "options": opts, "multiSelect": false})
			q.questions = append(q.questions, codexQuestion{id: x.ID, text: x.Question})
		}
		r.Input, _ = json.Marshal(map[string]any{"questions": qs})
	case codexPermissions:
		r.Tool, r.Input, r.CanRemember = "Permissions", raw, true
		q.perms = p.Permissions
	case codexElicitation:
		r.Tool, r.DisplayName, r.Title, r.Input = "mcp__"+p.ServerName, p.ServerName, p.Message, raw
	default:
		// nothing of NextBoard's answers it (a login refresh, a dynamic tool): say so, or it waits
		return []ev{{kind: evReply, reply: line(map[string]any{"id": q.id,
			"error": map[string]any{"code": -32601, "message": "NextBoard does not handle " + method}})}}
	}
	e.reqs[r.ID] = q
	return []ev{{kind: evRequest, req: r}}
}

// codexAlways says whether a command approval offers a decision for next time, and which one
// allowAlways sends: acceptForSession when offered (or when nothing is listed), else the
// execpolicy rule Codex proposes.
func codexAlways(decisions []json.RawMessage) (bool, json.RawMessage) {
	if len(decisions) == 0 {
		return true, nil
	}
	var rule json.RawMessage
	for _, d := range decisions {
		var s string
		if json.Unmarshal(d, &s) == nil {
			if s == "acceptForSession" {
				return true, nil
			}
			continue
		}
		var o map[string]json.RawMessage
		if json.Unmarshal(d, &o) == nil && rule == nil {
			if _, ok := o["acceptWithExecpolicyAmendment"]; ok {
				rule = append(json.RawMessage{}, d...)
			}
		}
	}
	return rule != nil, rule
}
