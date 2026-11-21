package native

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestMCPToolsOfNextCode(t *testing.T) {
	if got := toolName("my docs", "search.all"); got != "mcp__my_docs__search_all" {
		t.Fatalf("toolName %q", got)
	}
	if got := toolName(strings.Repeat("s", 40), strings.Repeat("t", 40)); len(got) != 64 {
		t.Fatalf("a long name is cut to 64: %d", len(got))
	}
	// a server that cannot start is left out, said on stderr; the agent goes on
	var stderr bytes.Buffer
	list, done := connectMCP(context.Background(), `[{"name":"gone","command":"/nonexistent/mcp-server"}]`, nil, &stderr)
	done()
	if len(list) != 0 || !strings.Contains(stderr.String(), "MCP server gone") {
		t.Fatalf("tools %v stderr %q", list, stderr.String())
	}
	if list, done := connectMCP(context.Background(), `not json`, nil, &stderr); len(list) != 0 {
		t.Fatal("a bad spec gives no tools")
	} else {
		done()
	}
	// the variable never reaches a command the agent runs
	for _, kv := range toolEnviron([]string{"NB_NEXTCODE_MCP=[]", "NB_MCP_0_X_0=secret", "PATH=/bin"}, "") {
		if !strings.HasPrefix(kv, "PATH=") {
			t.Fatalf("leaked %q", kv)
		}
	}
}
