package jobs

import "testing"

// What a hidden run used, as each CLI reports it: the API provider's ledger needs it.
func TestRunnerUsage(t *testing.T) {
	p := &streamParser{agent: "claude", phase: func(string, string) {}, text: func(string) {}}
	p.write([]byte(`{"type":"result","subtype":"success","result":"ok","usage":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":500,"cache_creation_input_tokens":40},"modelUsage":{"glm-4.6":{"inputTokens":120}}}` + "\n"))
	if u := p.usage; u.Model != "glm-4.6" || u.Input != 120 || u.Output != 30 || u.CacheRead != 500 || u.CacheWrite != 40 {
		t.Fatalf("claude %+v", u)
	}
	c := &streamParser{agent: "codex", phase: func(string, string) {}, text: func(string) {}}
	for _, l := range []string{
		`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":50}}`,
		`{"type":"turn.completed","usage":{"input_tokens":300,"cached_input_tokens":0,"output_tokens":20}}`,
	} {
		c.write([]byte(l + "\n"))
	}
	if u := c.usage; u.Input != 500 || u.CacheRead != 800 || u.Output != 70 {
		t.Fatalf("codex %+v", u)
	}
}
