package studio

import (
	"fmt"
	"strings"
)

// A Documents or Diagrams run of a project in several parts reads it at the project root by
// default (docs/API-studio.md §5, §6), as a root console and the memory runs do: the parts on
// This PC through the root's links, the others through the hub's read tools. Its prompt says so
// and asks for the whole project; a run in one part's folder keeps the prompt it always had.

// rootNote is what a root run adds to its prompt: the parts, and how to say which part a thing
// belongs to (cover). "" for fewer than two parts.
func rootNote(parts []string, cover string) string {
	if len(parts) < 2 {
		return ""
	}
	return fmt.Sprintf("This project has %d parts, each a folder of its own: %s. The current folder is the project root, with one entry per part; "+
		"where each part is and how to read it is said in your instructions or in AGENTS.md / CLAUDE.md here. "+
		"Cover the whole project, every part, not only the first; %s", len(parts), strings.Join(parts, ", "), cover)
}

// DiagramRootNote is what DiagramPrompt gets at the end for a root run.
func DiagramRootNote(parts []string) string {
	if n := rootNote(parts, "show which part each node lives in: one group per part, or the part's name in the node's note."); n != "" {
		return "\n\n" + n
	}
	return ""
}

// DocRootNote is what DocPrompt gets at the end for a root run (DocPrompt ends with a newline).
func DocRootNote(parts []string) string {
	if n := rootNote(parts, "say which part each component, file or command lives in."); n != "" {
		return "\n" + n + "\n"
	}
	return ""
}
