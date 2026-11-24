// Package nextcode drives NextCode consoles (docs/API-agents.md §30): an agent CLI run headless
// over pipes (Claude Code's stream-json today), its conversation kept as chat items, its
// permission requests and questions held for the owner to answer, and everything streamed to
// the views of the console. The CLI is never modified; the hub only speaks its headless
// protocol. Terminal agents never come here.
package nextcode

import (
	"encoding/json"
	"io"
	"time"
)

// Item kinds.
const (
	KindUser     = "user"     // what the owner (or an order) sent
	KindText     = "text"     // the agent's reply text (Markdown)
	KindThinking = "thinking" // the agent's thinking; its text may be empty (hidden by the CLI)
	KindTool     = "tool"     // a tool call with its result
	KindPrompt   = "prompt"   // the task a sub-agent was given (Parent = its tool call)
	KindError    = "error"    // a failed turn, a CLI that ended
	KindNotice   = "notice"   // the conversation was compacted, a turn was stopped, …
	KindAnswer   = "answer"   // the owner's answer to the agent's questions or plan (Answer set)
)

// Tool statuses.
const (
	ToolRunning = "running"
	ToolDone    = "done"
	ToolError   = "error"
	ToolDenied  = "denied"
	// ToolStopped: the turn ended (stopped, or the CLI ended) before the tool's result came.
	ToolStopped = "stopped"
)

// Item is one entry of the chat.
type Item struct {
	ID   string    `json:"id"`
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	Text string    `json:"text,omitempty"`
	Tool *Tool     `json:"tool,omitempty"`
	// Parent is the tool call of the sub-agent this belongs to ("" = the main conversation).
	Parent string `json:"parent,omitempty"`
	// Stream is the id the item had while it was streamed (Frame "block"/"delta"), so a view
	// replaces what it drew from the deltas with the item.
	Stream string `json:"stream,omitempty"`
	// Answer: of a KindAnswer item, what was asked and answered. Text says it in words.
	Answer *Answered `json:"answer,omitempty"`
}

// Answered is the owner's answer to the agent's questions or to its plan, kept in the chat so
// it can be read back: the questions as they were asked and what was chosen, or that nothing
// was; whether the plan was approved, and the owner's note when it was not.
type Answered struct {
	// Kind is the request answered: ReqQuestion ("" in chats kept before plans were) or ReqPlan.
	Kind string `json:"kind,omitempty"`
	// ToolUseID: the question's or plan's tool call (AskUserQuestion, ExitPlanMode), when the
	// CLI shows it as one.
	ToolUseID string `json:"toolUseId,omitempty"`
	// Input: of a question, the questions with their options, in AskUserQuestion's shape
	// ({"questions": […]}). A plan's is in its tool call only.
	Input json.RawMessage `json:"input,omitempty"`
	// Answers: the chosen labels by question text; a label that is not an option is the owner's
	// own words.
	Answers map[string][]string `json:"answers,omitempty"`
	// Message: the owner's own reply, beside or instead of the options; of a plan not approved,
	// what to change in it ("" = no note).
	Message string `json:"message,omitempty"`
	// Skipped: the owner answered nothing and the agent went on without it.
	Skipped bool `json:"skipped,omitempty"`
	// Approved: of a plan, the owner approved it; else the agent keeps planning.
	Approved bool `json:"approved,omitempty"`
	// Mode: of a plan approved, the permission mode it was approved into ("" = as it was).
	Mode string `json:"mode,omitempty"`
}

// Tool is a tool call of the agent.
type Tool struct {
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output string          `json:"output,omitempty"`
	Status string          `json:"status"`
	// Clipped: Input or Output was too long and was cut.
	Clipped bool `json:"clipped,omitempty"`
}

// Request kinds.
const (
	ReqPermission = "permission" // a tool call that needs the owner's yes
	ReqQuestion   = "question"   // the agent asks the owner to choose (AskUserQuestion)
	ReqPlan       = "plan"       // the agent's plan, to approve before it acts (ExitPlanMode)
)

// Request is something the agent waits on the owner for.
type Request struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Tool      string          `json:"tool"`
	ToolUseID string          `json:"toolUseId,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	// What the CLI says about it, shown as given.
	Title       string `json:"title,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	BlockedPath string `json:"blockedPath,omitempty"`
	Reason      string `json:"reason,omitempty"`
	// CanRemember: the CLI offers rules that let calls like this one go ahead next time
	// (Decision allowAlways).
	CanRemember bool      `json:"canRemember,omitempty"`
	Parent      string    `json:"parent,omitempty"`
	At          time.Time `json:"at"`

	suggestions json.RawMessage // the CLI's own rules, echoed back on allowAlways
}

// Decisions of an Answer.
const (
	Allow       = "allow"
	AllowAlways = "allowAlways" // allow, and the CLI's suggested rules for next time
	Deny        = "deny"
	DenyStop    = "denyStop" // deny and stop the turn
)

// Answer is the owner's answer to a Request.
type Answer struct {
	Decision string `json:"decision"`
	// Message tells the agent why (deny), or is the owner's own reply to a question.
	Message string `json:"message,omitempty"`
	// Answers: of a question, the chosen labels by question text (several for multi-select).
	Answers map[string][]string `json:"answers,omitempty"`
	// Mode: of a plan approved (ReqPlan, Allow), the permission mode the agent carries it out in,
	// e.g. "acceptEdits" ("" = as it is). Ignored on other answers.
	Mode string `json:"mode,omitempty"`
}

// Process states of Status.
const (
	ProcStopped  = "stopped"  // no CLI runs; the next message starts one
	ProcStarting = "starting" // started, its first answer not in yet
	ProcRunning  = "running"
)

// Status is the console's live state.
type Status struct {
	Engine  string `json:"engine"`
	Process string `json:"process"`
	Working bool   `json:"working"`
	// TurnSince: when the current turn started (Working).
	TurnSince *time.Time `json:"turnSince,omitempty"`
	// Stopping: Stop was asked and the turn has not ended yet.
	Stopping       bool   `json:"stopping,omitempty"`
	Model          string `json:"model,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	// Conversation is the CLI's conversation id.
	Conversation string `json:"conversation,omitempty"`
	// Commands are the CLI's slash commands (names, without "/").
	Commands []string `json:"commands,omitempty"`
	CostUSD  float64  `json:"costUsd,omitempty"`
	Usage    *Usage   `json:"usage,omitempty"`
	// RateLimit is the CLI's latest rate limit report, as it gives it.
	RateLimit json.RawMessage `json:"rateLimit,omitempty"`
	// Note: a passing state worth showing ("Retrying (attempt 2)…").
	Note string `json:"note,omitempty"`
	// Error: why the last CLI ended unexpectedly, until the next one starts.
	Error string `json:"error,omitempty"`
}

// Usage is the token use of the latest turn.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

// Frame types sent to a console's views.
const (
	FrameSnapshot = "snapshot" // Items (the latest), Requests, Status, More
	FrameItem     = "item"     // Item, new or changed (by id)
	FrameBlock    = "block"    // a streamed block starts: ID, Kind (text|thinking|tool), Name, Parent
	FrameDelta    = "delta"    // ID, Kind, Text: more of a streamed block
	FrameRequest  = "request"  // Request
	FrameResolved = "resolved" // ID: the request is answered or gone
	FrameStatus   = "status"   // Status
)

// Frame is one message to a view.
type Frame struct {
	T        string    `json:"t"`
	Items    []Item    `json:"items,omitempty"`
	More     bool      `json:"more,omitempty"`
	Item     *Item     `json:"item,omitempty"`
	Requests []Request `json:"requests,omitempty"`
	Request  *Request  `json:"request,omitempty"`
	Status   *Status   `json:"status,omitempty"`
	ID       string    `json:"id,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Name     string    `json:"name,omitempty"`
	Parent   string    `json:"parent,omitempty"`
	Text     string    `json:"text,omitempty"`
}

// Proc is a started CLI (machine.PipeProc on a real machine).
type Proc struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Done   <-chan struct{}
	// Stop ends it: stdin closed, SIGTERM, a kill a little later.
	Stop   func()
	Stderr func() string
}

// Listener is how the console follows the agent's state (all optional; called without the
// driver's lock, in order).
type Listener struct {
	// TurnStart: a turn began (a message was sent, or the CLI started one by itself).
	TurnStart func()
	// Tool: the agent is calling a tool; summary is its main argument.
	Tool func(name, summary string)
	// NeedsYou: a request waits for the owner; text says what (e.g. "Bash(npm test)").
	NeedsYou func(kind, text string)
	// Answered: the owner answered the last open request.
	Answered func()
	// TurnEnd: the turn ended; errText is set when it failed.
	TurnEnd func(errText string)
	// Conversation: the CLI's conversation id, once known.
	Conversation func(id string)
	// Exit: the CLI ended by itself (not asked to, not idle): errText says why.
	Exit func(errText string)
}
