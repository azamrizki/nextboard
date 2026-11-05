package agent

import (
	"regexp"
	"strings"
)

// genericWorking is the hint a TUI shows while a turn runs, whatever the CLI.
var genericWorking = compileAll(
	`(?i)\b(esc|escape|ctrl\+c|ctrl-c|\^c)\b\W{0,3}(to\s+)?(interrupt|stop|abort)\b`,
	`(?i)\besc(ape)?\s+(to\s+)?cancel\b`,
)

const genericScanLines = 6

// GenericEvaluate reads the screen of a CLI without rules of its own, with what
// the delivery guard knows about dialogs and input prompts. Its wording is loose:
// "Do you want me to go on?" above the input prompt is only the end of a reply.
func GenericEvaluate(rulesID string, lines []string, cursorY int) ScreenMatch {
	norm := make([]string, len(lines))
	for i, l := range lines {
		norm[i] = strings.ReplaceAll(l, "\u00a0", " ")
	}
	var m ScreenMatch
	prompt := atInputPrompt(rulesID, norm, cursorY)
	var none []*regexp.Regexp
	m.NeedsYou = dialogLineIn(norm, cursorY, false, uiDialog, dialogWording)
	if m.NeedsYou == "" && cursorY >= 0 && cursorY < len(norm) {
		for _, re := range lineQuestion {
			if re.MatchString(norm[cursorY]) {
				m.NeedsYou = strings.TrimSpace(norm[cursorY])
			}
		}
	}
	if m.NeedsYou == "" && !prompt {
		m.NeedsYou = dialogLineIn(norm, cursorY, false, none, proseQuestion)
	}
	n := 0
	for i := len(norm) - 1; i >= 0 && n < genericScanLines; i-- {
		if strings.TrimSpace(norm[i]) == "" {
			continue
		}
		n++
		for _, re := range genericWorking {
			m.Working = m.Working || re.MatchString(norm[i])
		}
	}
	if m.NeedsYou != "" {
		m.Working = false
	}
	m.Ready = prompt && !m.Working && m.NeedsYou == ""
	return m
}

// atInputPrompt reports whether the cursor line has the shape of the CLI's input
// prompt, whatever else is on the screen.
func atInputPrompt(rulesID string, lines []string, cursorY int) bool {
	if cursorY < 0 || cursorY >= len(lines) || selectedOption.MatchString(lines[cursorY]) {
		return false
	}
	shapes, ok := inputShapes[rulesID]
	if !ok {
		shapes = inputShapes[""]
	}
	for _, sh := range shapes {
		if sh.cursor.MatchString(lines[cursorY]) &&
			(sh.above == nil || sh.above.MatchString(neighbour(lines, cursorY, -1))) &&
			(sh.below == nil || sh.below.MatchString(neighbour(lines, cursorY, 1))) {
			return true
		}
	}
	return false
}
