package studio

import (
	"strings"
	"testing"
)

// TestDiagramPromptKeepsTheAsk: a subject with words of the lead's or an agent's keeps both in the
// runner's prompt; custom draws the words alone; the panel's subjects come without words.
func TestDiagramPromptKeepsTheAsk(t *testing.T) {
	p := DiagramPrompt("dataFlow", "only the orders and their payments", "balanced")
	if !strings.Contains(p, "Draw a diagram of how data flows through the system from inputs to storage and outputs, in particular: only the orders and their payments for the software project") {
		t.Fatalf("subject and ask:\n%s", p)
	}
	if p := DiagramPrompt("custom", "the login", "quick"); !strings.Contains(p, "Draw a diagram of the login for the software project") {
		t.Fatalf("custom:\n%s", p)
	}
	if p := DiagramPrompt("architecture", "", "quick"); !strings.Contains(p, "main components, services, stores and how they connect for the software project") || strings.Contains(p, "in particular") {
		t.Fatalf("subject alone:\n%s", p)
	}
}
