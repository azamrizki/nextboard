// Package llm calls the model of an API provider for NextCode's own engine (internal/nextcode/native):
// one client for each wire format the hub's API providers speak, an Anthropic-compatible Messages
// API, an OpenAI-compatible Chat Completions API and the Responses API, behind one Client, with
// the conversation in one form for all of them.
package llm

import (
	"context"
	"encoding/json"
)

// ---- the provider

// Wire formats.
const (
	WireAnthropic = "anthropic" // POST {base}/v1/messages
	WireChat      = "chat"      // POST {base}/chat/completions
	WireResponses = "responses" // POST {base}/responses
)

// ProviderConfig is the endpoint of the hub's API provider: from the agent's command line, and
// the key from NB_PROVIDER_KEY.
type ProviderConfig struct {
	Kind    string // "anthropic" | "openai"
	BaseURL string // as the provider has it: Anthropic's without /v1, OpenAI's with it
	API     string // openai: "chat" | "responses" ("" = chat)
	Auth    string // anthropic: "bearer" | "x-api-key" ("" = bearer)
	Key     string // "" = none (a server on this machine)
}

// Wire is the wire format the provider speaks.
func (p ProviderConfig) Wire() string {
	switch {
	case p.Kind == "anthropic":
		return WireAnthropic
	case p.API == "responses":
		return WireResponses
	}
	return WireChat
}

// ---- the conversation, the same for every wire

// Roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Block types.
const (
	BlockText       = "text"
	BlockThinking   = "thinking" // what the model showed of its reasoning (may be empty: hidden)
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

// Block is one part of a message. A user message has text and tool_result blocks; an
// assistant message text, thinking and tool_use blocks.
type Block struct {
	Type string `json:"type"`
	// Text: of a text block, a thinking block, or the content of a tool_result.
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"` // a JSON object
	// tool_result
	ToolUseID string `json:"toolUseId,omitempty"`
	IsError   bool   `json:"isError,omitempty"`
	// Raw is the block as the provider gave it, in its wire's own JSON, replayed unchanged to
	// that wire: Anthropic's thinking with its signature and redacted_thinking, a Responses
	// reasoning item with its encrypted content. Wire and Model say whose it is.
	//
	// Replay rules (each client):
	//   - a block with Raw from the same wire is sent as Raw, byte for byte, except a thinking
	//     block from another model, which is left out;
	//   - a thinking block with no Raw of this wire is left out (Chat Completions: see chat.go
	//     for reasoning_content within the current turn);
	//   - text, tool_use and tool_result blocks are built from their fields.
	Raw   json.RawMessage `json:"raw,omitempty"`
	Wire  string          `json:"wire,omitempty"`
	Model string          `json:"model,omitempty"`
}

// Message is one turn of the conversation.
type Message struct {
	Role   string  `json:"role"`
	Blocks []Block `json:"blocks"`
}

// ToolDef is a tool as the model sees it.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"` // JSON Schema of the input (an object)
}

// ---- the client of a wire

// Request is one call of the model.
type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []ToolDef
	// Effort is the reasoning effort the owner chose ("" = the model's own): low, medium,
	// high, xhigh or max. Each wire maps it.
	Effort string
	// MaxTokens is the longest answer (0 = the client's default).
	MaxTokens int
}

// Event kinds, as the answer streams.
const (
	// EvBlockStart: block Index of the answer starts: BlockType text|thinking|tool_use, and
	// for a tool_use ToolID and ToolName. Every block of Response.Blocks has exactly one,
	// with Index = its position there, before Stream returns.
	EvBlockStart = "blockStart"
	// EvDelta: more of block Index: Text (the text, the thinking, or a piece of a tool
	// call's JSON input).
	EvDelta = "delta"
	// EvRetry: the call failed and is tried again: Attempt of MaxAttempts, Text says why.
	EvRetry = "retry"
)

// Event is something of the answer as it streams.
type Event struct {
	Kind        string
	Index       int
	BlockType   string
	ToolID      string
	ToolName    string
	Text        string
	Attempt     int
	MaxAttempts int
}

// Stop reasons.
const (
	StopEnd     = "end_turn"
	StopToolUse = "tool_use"
	StopMax     = "max_tokens"
	StopRefusal = "refusal"
)

// Usage is the tokens of one call. Input does not count the cached tokens: the context the
// call had is Input + CacheRead + CacheWrite.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

// Response is the whole answer of one call.
type Response struct {
	ID         string // the provider's id of the answer, "" when it has none
	Model      string // the model that answered, as the provider names it ("" = the one asked)
	Blocks     []Block
	StopReason string // one of the Stop* above, or the provider's own when none fits
	Usage      Usage
}

// APIError is a call the provider refused or failed, after the retries.
type APIError struct {
	Status  int    // the HTTP status, 0 = it could not be reached
	Message string // what it said, with the key never in it
	// ContextOverflow: the conversation is longer than the model takes (compaction helps).
	ContextOverflow bool
}

func (e *APIError) Error() string { return e.Message }

// Client calls one wire's API. Stream retries what is worth retrying (a 429, a 5xx, an
// overloaded API, a dropped connection before any of the answer came) a few times, telling on
// with EvRetry, and returns an *APIError for the rest. ctx ends the call (the owner's Stop).
type Client interface {
	Stream(ctx context.Context, req Request, on func(Event)) (*Response, error)
}
