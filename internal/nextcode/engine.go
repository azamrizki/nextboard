package nextcode

// engine speaks one CLI's headless protocol: Claude Code's stream-json (claude.go), Codex's
// app-server (codex.go), NextCode's own agent (api.go). The driver keeps the chat, the requests,
// the history and the CLI's life; the engine turns its actions into lines for the CLI and the
// CLI's lines into effects.
type engine interface {
	// argv is the CLI's command line: base is the agent's own launch (agent.Headless, with the
	// login, model, effort and context), mode the permission mode to start in.
	argv(base []string, mode string) []string
	// start is what the driver writes first, before any message; resume says whether the CLI
	// continues the conversation (its id, when known).
	start(resume bool, conversation string) []byte
	// message is the owner's message; nil when the engine sends it later (on an effect).
	message(text string) []byte
	answer(r *Request, a Answer) ([]byte, error)
	// interrupt stops the turn; nil when there is nothing to stop yet.
	interrupt() []byte
	setModel(model string) []byte
	setMode(mode string) []byte
	decode(b []byte) []ev
}

// Engines.
const (
	EngineClaude = "claude"
	EngineCodex  = "codex"
	// EngineAPI is NextCode's own agent on one of the hub's API providers (api.go).
	EngineAPI = "api"
)

// newEngine is the engine of a console's agent.
func newEngine(name string) engine {
	switch name {
	case EngineCodex:
		return newCodexEngine()
	case EngineAPI:
		return newAPIEngine()
	default:
		return newClaudeEngine()
	}
}
