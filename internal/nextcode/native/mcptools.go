package native

// The MCP servers Settings › NextCode gives NextCode (docs/API-agents.md §30, Clean NextCode):
// the hub hands them in NB_NEXTCODE_MCP, their secret values in variables of their own. Each
// server's tools become tools of the agent, named mcp__<server>__<tool> as Claude Code names
// them, and every call asks the owner unless the mode is Bypass (Plan refuses them), as for
// Claude Code. A server that cannot be reached is left out, with a line on stderr.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
	"github.com/azamrizki/nextboard/internal/nextcode/native/mcp"
	"github.com/azamrizki/nextboard/internal/nextcode/native/tools"
)

// mcpEnv is the environment variable of the servers, a JSON array of mcp.Server.
const mcpEnv = "NB_NEXTCODE_MCP"

// KindMCP is the kind of an MCP server's tool: it asks, like any tool of unknown effect.
const KindMCP = "mcp"

// mcpOutputMax is how much of a tool's answer the model gets.
const mcpOutputMax = 64 << 10

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]`)

type mcpTool struct {
	def    llm.ToolDef
	client *mcp.Client
	name   string
}

func (t *mcpTool) Def() llm.ToolDef { return t.def }
func (t *mcpTool) Kind() string     { return KindMCP }

func (t *mcpTool) Run(ctx context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	text, isErr, err := t.client.Call(ctx, t.name, input)
	if err != nil {
		return tools.Result{Output: fmt.Sprintf("%s failed: %v", t.def.Name, err), IsError: true}
	}
	if len(text) > mcpOutputMax {
		text = text[:mcpOutputMax] + "\n[… cut]"
	}
	if text == "" {
		text = "(no output)"
	}
	return tools.Result{Output: text, IsError: isErr}
}

// toolName is mcp__<server>__<tool> within the 64 characters a tool name may have.
func toolName(server, tool string) string {
	n := "mcp__" + unsafeName.ReplaceAllString(server, "_") + "__" + unsafeName.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// connectMCP reaches the servers of spec side by side (each within 30 s) and gives their tools,
// and what ends them.
func connectMCP(ctx context.Context, spec string, environ []string, stderr io.Writer) ([]tools.Tool, func()) {
	var servers []mcp.Server
	if err := json.Unmarshal([]byte(spec), &servers); err != nil {
		fmt.Fprintf(stderr, "nextcode-agent: MCP servers: %v\n", err)
		return nil, func() {}
	}
	type got struct {
		client *mcp.Client
		tools  []mcp.Tool
	}
	results := make([]got, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			c, err := mcp.Connect(cctx, srv, environ, os.Getenv)
			if err != nil {
				fmt.Fprintf(stderr, "nextcode-agent: MCP server %s: %v\n", srv.Name, err)
				return
			}
			list, err := c.Tools(cctx)
			if err != nil {
				fmt.Fprintf(stderr, "nextcode-agent: MCP server %s: tools: %v\n", srv.Name, err)
				c.Close()
				return
			}
			results[i] = got{c, list}
		}()
	}
	wg.Wait()
	var out []tools.Tool
	seen := map[string]bool{}
	for i, r := range results {
		if r.client == nil {
			continue
		}
		for _, t := range r.tools {
			name := toolName(servers[i].Name, t.Name)
			if seen[name] {
				continue
			}
			seen[name] = true
			schema := t.InputSchema
			if len(schema) == 0 || string(schema) == "null" {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			out = append(out, &mcpTool{def: llm.ToolDef{Name: name, Description: t.Description, Schema: schema}, client: r.client, name: t.Name})
		}
	}
	return out, func() {
		for _, r := range results {
			if r.client != nil {
				r.client.Close()
			}
		}
	}
}
