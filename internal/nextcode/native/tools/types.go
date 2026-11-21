// Package tools are the built-in tools of NextCode's own engine (internal/nextcode/native): Read,
// Write, Edit, Bash, Glob, Grep, WebFetch and TodoWrite, with the input names and shapes of
// Claude Code's tools of the same names, so that the chat shows them the same way for every
// engine. Whether a call may run is the agent's to decide (its permission modes); a tool only runs.
package tools

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
)

// Sandbox limits what commands may do (the project's sandbox, docs/API-agents.md §27): write
// only in the working folder, the temp folder and Write, reach the network only with Network.
type Sandbox struct {
	Network bool     `json:"network"`
	Write   []string `json:"write"`
}

// Env is what the tools run with.
type Env struct {
	Cwd     string   // the working folder, absolute
	AddDirs []string // more folders the agent works in (attachments, a root's parts)
	// Environ is the environment of the commands it runs: the agent's own without
	// NB_PROVIDER_KEY and the variables of the hub's (NB_*, NEXTCODE_*).
	Environ []string
	Sandbox *Sandbox // nil = none
	HTTP    *http.Client
}

// Tool kinds: what a tool can do, for the permission modes.
const (
	KindRead  = "read"  // reads files: Read, Glob, Grep
	KindEdit  = "edit"  // changes files: Write, Edit
	KindExec  = "exec"  // runs a command: Bash
	KindFetch = "fetch" // reaches the network: WebFetch
	KindMeta  = "meta"  // changes nothing outside the agent: TodoWrite
)

// Result is what a tool gives the model.
type Result struct {
	Output  string
	IsError bool
}

// Tool is a built-in tool. Run is called only once the call is allowed; ctx ends with the
// turn (the owner's Stop), and a command still running then is killed.
type Tool interface {
	Def() llm.ToolDef
	Kind() string
	Run(ctx context.Context, env *Env, input json.RawMessage) Result
}
