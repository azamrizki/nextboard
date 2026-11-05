package orch

import "testing"

// The credentials card holds only a title, but it must stay out of what can be sent to an agent
// and out of what the planner can do, whatever it holds later (docs/API-vault.md).
func TestCredentialsCardIsNeverContext(t *testing.T) {
	if contentTiles["credentials"] {
		t.Fatal("a credentials card must not be a content tile")
	}
	if layoutNodeKinds["credentials"] {
		t.Fatal("a credentials card must not go into a saved layout")
	}
	if err := (Step{Op: "node.add", Kind: "credentials"}).Validate(); err == nil {
		t.Fatal("the planner must not be able to add a credentials card")
	}
}
