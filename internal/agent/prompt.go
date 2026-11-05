package agent

import (
	"regexp"
	"strings"
)

// The delivery engine (docs/API-orchestration.md §2) may only type into a
// console whose screen it positively recognises as the CLI's normal input
// prompt. Anything else — a permission prompt, a trust or login dialog, the
// Codex update dialog (whose default answer is "Update now"), a model or
// theme picker, or simply an unknown screen — is refused. Recognition is
// deliberately strict: a false refusal only delays an order, a false
// acceptance can run an installer.

// inputShape is the per-CLI shape of the input prompt around the cursor.
type inputShape struct {
	cursor *regexp.Regexp // the line the cursor is on
	// above/below, when set, must match the nearest non-empty line above /
	// below the cursor line (within 2 rows): the borders of an input box.
	above, below *regexp.Regexp
}

var (
	ruleLine = regexp.MustCompile(`^\s*─{6,}\s*$`)
	// numbered option with a selection marker: "› 1. Update now", "❯ 2. No"
	selectedOption = regexp.MustCompile(`^\s*[❯›>▶➤→»]\s*\d+[.)]\s`)
)

var inputShapes = map[string][]inputShape{
	// Claude Code 2.1.312: "❯ " at column 0 between two ─ rules. Dialog
	// options are indented (" ❯ 1. Yes") and not boxed by rules.
	"claude": {
		{cursor: regexp.MustCompile(`^❯(\s|$)`), above: ruleLine, below: ruleLine},
		// older releases: a rounded box "│ > …│"
		{cursor: regexp.MustCompile(`^\s*│\s*>\s`), above: regexp.MustCompile(`^\s*╭─`), below: regexp.MustCompile(`^\s*╰─`)},
	},
	// Codex 0.163/0.165: "› Ask Codex to do anything" at column 0.
	"codex": {{cursor: regexp.MustCompile(`^›(\s|$)`)}, {cursor: regexp.MustCompile(`^▌(\s|$)`)}},
	// Gemini (unverified): "│ > Type your message…" in a box.
	"gemini": {{cursor: regexp.MustCompile(`^\s*│\s*>\s`)}},
	// Agents without screen rules: a prompt character at the cursor line.
	"": {{cursor: regexp.MustCompile(`^\s*(│\s*)?[>❯›»](\s|$)`)}},
}

// strongDialog patterns are UI markers of dialogs, menus and pickers that
// practically never appear in an agent's prose; weakDialog patterns are
// dialog wording that also occurs in replies ("Do you want me to …?"), so
// they are only checked right next to the cursor of agents without a
// structural input box.
var strongDialog = compileAll(
	`^\s*[❯›>▶➤→»]\s*\d+[.)]\s`, // selected numbered option
	`(?i)\b(enter|return)\s+(to\s+)?(confirm|continue|select|submit|accept|choose)\b`,
	`(?i)\besc(ape)?\s+(to\s+)?(cancel|skip|exit|go back|dismiss|close)\b`,
	`(?i)\bupdate now\b`,
	`(?i)\bskip until next version\b`,
	`(?i)\[\s*y\s*/\s*n\s*\]|\(\s*y\s*/\s*n\s*\)`,
	`(?i)\bpress any key\b`,
	`(?i)\buse arrow keys\b|↑/↓|\(↑↓\)`,
)

var weakDialog = compileAll(
	`(?i)^[^│╭╰|]*\bupdate available\b`,
	`(?i)^\s*\d+[.)]\s+(yes|no|skip|update|allow|deny|cancel|continue|exit|quit|trust|always|never|approve|reject)\b`,
	`(?i)\bdo you (want|trust|wish|agree)\b`,
	`(?i)\b(would you like to|allow (this|command|access|execution))\b`,
	`(?i)\b(select|choose)\s+(a|an|the|your)\s+(login|account|theme|model|method|option|folder|directory|plan|style)\b`,
	`(?i)\b(log|sign)\s?in\s+(with|to|using)\b`,
)

func compileAll(ps ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(ps))
	for i, p := range ps {
		out[i] = regexp.MustCompile(p)
	}
	return out
}

// Scan windows: bottom non-empty lines and rows around the cursor checked
// for strong markers, rows next to the cursor checked for weak wording.
const (
	dialogScanLines = 12
	cursorWindow    = 8
	weakWindow      = 3
)

// InputPrompt reports whether a screen shows the agent's normal input
// prompt with the cursor in it (rulesID = Agent.ScreenRules, "" for agents
// without rules). reason says why not.
func InputPrompt(rulesID string, lines []string, cursorY int) (bool, string) {
	norm := make([]string, len(lines))
	for i, l := range lines {
		norm[i] = strings.ReplaceAll(l, "\u00a0", " ")
	}
	lines = norm
	if r := RulesFor(rulesID); r != nil {
		if m := r.Evaluate(lines, cursorY); m.NeedsYou != "" {
			return false, "dialog: " + m.NeedsYou
		}
	}
	if cursorY < 0 || cursorY >= len(lines) {
		return false, "cursor di luar layar"
	}
	cur := lines[cursorY]
	if selectedOption.MatchString(cur) {
		return false, "menu: " + strings.TrimSpace(cur)
	}
	shapes, ok := inputShapes[rulesID]
	if !ok {
		shapes = inputShapes[""]
	}
	for _, sh := range shapes {
		if !sh.cursor.MatchString(cur) {
			continue
		}
		boxed := sh.above != nil
		if boxed && !sh.above.MatchString(neighbour(lines, cursorY, -1)) {
			continue
		}
		if sh.below != nil && !sh.below.MatchString(neighbour(lines, cursorY, 1)) {
			continue
		}
		if l := dialogLine(lines, cursorY, boxed); l != "" {
			return false, "dialog: " + l
		}
		return true, ""
	}
	if l := dialogLine(lines, cursorY, false); l != "" {
		return false, "dialog: " + l
	}
	return false, "prompt input tidak dikenali di baris kursor: " + strings.TrimSpace(cur)
}

// neighbour returns the nearest non-empty line within 2 rows of y in
// direction dir ("" when none).
func neighbour(lines []string, y, dir int) string {
	for i, k := y+dir, 0; i >= 0 && i < len(lines) && k < 2; i, k = i+dir, k+1 {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

// dialogLine returns the first line that looks like a dialog or menu. With
// a boxed input prompt (Claude) only the box and what is below it are
// checked — reply text above the box is prose, and a dialog would have
// replaced the box. Otherwise strong markers are searched in the bottom
// lines and around the cursor, weak wording right next to the cursor.
func dialogLine(lines []string, cursorY int, boxed bool) string {
	check := func(i int, pats []*regexp.Regexp) string {
		if i < 0 || i >= len(lines) || strings.TrimSpace(lines[i]) == "" {
			return ""
		}
		for _, re := range pats {
			if re.MatchString(lines[i]) {
				return strings.TrimSpace(lines[i])
			}
		}
		return ""
	}
	if boxed {
		for i := cursorY - 1; i < len(lines); i++ {
			if l := check(i, strongDialog); l != "" {
				return l
			}
		}
		return ""
	}
	n := 0
	for i := len(lines) - 1; i >= 0 && n < dialogScanLines; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		n++
		if l := check(i, strongDialog); l != "" {
			return l
		}
	}
	for i := cursorY - cursorWindow; i <= cursorY+cursorWindow; i++ {
		if l := check(i, strongDialog); l != "" {
			return l
		}
	}
	for i := cursorY - weakWindow; i <= cursorY+weakWindow; i++ {
		if l := check(i, weakDialog); l != "" {
			return l
		}
	}
	return ""
}

// promptStart finds the first line of a multi-line input for
// InputPromptPasted: the line starting the input, at most maxInputRows
// above the cursor.
var promptStart = map[string]*regexp.Regexp{
	"claude": regexp.MustCompile(`^❯(\s|$)`),
	"codex":  regexp.MustCompile(`^[›▌](\s|$)`),
}

const maxInputRows = 30

// InputPromptPasted is the re-check right before the Return of a delivery:
// after a paste the input may span several rows, so the cursor may sit on
// a continuation row below the prompt line. The prompt line must still be
// the box's first row (Claude: directly under a ─ rule, with a ─ rule
// below the cursor), and no dialog may be on screen.
func InputPromptPasted(rulesID string, lines []string, cursorY int) (bool, string) {
	if ok, why := InputPrompt(rulesID, lines, cursorY); ok || strings.HasPrefix(why, "dialog") || strings.HasPrefix(why, "menu") {
		return ok, why
	}
	re, ok := promptStart[rulesID]
	if !ok || cursorY < 0 || cursorY >= len(lines) {
		return InputPrompt(rulesID, lines, cursorY)
	}
	norm := make([]string, len(lines))
	for i, l := range lines {
		norm[i] = strings.ReplaceAll(l, " ", " ")
	}
	start := -1
	for i := cursorY; i >= 0 && i >= cursorY-maxInputRows; i-- {
		if re.MatchString(norm[i]) {
			start = i
			break
		}
		if ruleLine.MatchString(norm[i]) || selectedOption.MatchString(norm[i]) {
			break
		}
	}
	if start < 0 {
		return false, "prompt input tidak ditemukan di atas kursor"
	}
	if rulesID == "claude" {
		if !ruleLine.MatchString(neighbour(norm, start, -1)) || !ruleLine.MatchString(neighbour(norm, cursorY, 1)) {
			return false, "kotak input tidak lengkap"
		}
	}
	if l := dialogLine(norm, cursorY, rulesID == "claude"); l != "" {
		return false, "dialog: " + l
	}
	return true, ""
}
