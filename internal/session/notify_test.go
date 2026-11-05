package session

import "testing"

// Payloads as sent by Codex 0.163.0 (thread ids shortened).
func TestCodexSideThread(t *testing.T) {
	title := []byte(`{"type":"agent-turn-complete","thread-id":"01a1-97db","turn-id":"t","cwd":"/w","client":"codex-tui","input-messages":["Generate a concise, single-line task title"],"last-assistant-message":"{\"title\":\"Reply OK\"}"}`)
	main := []byte(`{"type":"agent-turn-complete","thread-id":"01a1-8007","turn-id":"t","cwd":"/w","client":"codex-tui","input-messages":["Reply with the single word OK."],"last-assistant-message":"OK"}`)
	jsonReply := []byte(`{"type":"agent-turn-complete","thread-id":"x","last-assistant-message":"{\"title\":\"a\",\"body\":\"b\"}"}`)
	if !codexSideThread(title) || codexSideThread(main) || codexSideThread(jsonReply) {
		t.Fatal("side thread detection")
	}
}
