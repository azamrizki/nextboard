package nextcode

// The API engine runs NextCode's own agent (internal/nextcode/native) as `nextboard
// nextcode-agent` on one of the hub's API providers. The agent speaks the part of Claude Code's
// stream-json protocol that the Claude engine reads, so this is the Claude engine with a command
// line of its own: the agent takes none of Claude Code's flags, only the permission mode.
type apiEngine struct{ *claudeEngine }

func newAPIEngine() *apiEngine { return &apiEngine{newClaudeEngine()} }

// argv is the agent's own launch (agent.Headless) and the permission mode it starts in ("" = its own).
func (e *apiEngine) argv(base []string, mode string) []string {
	out := append([]string{}, base...)
	if mode != "" {
		out = append(out, "--permission-mode", mode)
	}
	return out
}
