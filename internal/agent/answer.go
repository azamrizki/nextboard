package agent

import (
	"regexp"
	"strings"
)

// ScreenAnswer reads the last answer off the rendered screen of a full-screen CLI that gives the
// hub no transcript (OpenCode). Its raw output is cursor moves and redraws, which read as noise,
// while the screen shows the answer whole, as long as it fits. ok is false when the screen shows
// no finished answer.
func ScreenAnswer(rulesID string, lines []string) (string, bool) {
	if rulesID == "opencode" {
		return openCodeAnswer(lines)
	}
	return "", false
}

// openCodeDone is the line OpenCode 1.17.15 puts under a finished answer: "▣  Build · model · 3.0s".
var openCodeDone = regexp.MustCompile(`^\s*▣\s+\S.*·.*·\s*\d`)

// openCodeAnswer is what stands between the last message box (rows with a heavy bar) and the
// last finished-answer line.
func openCodeAnswer(lines []string) (string, bool) {
	end := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if openCodeDone.MatchString(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 {
		return "", false
	}
	start := 0
	for i := end - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "┃") {
			start = i + 1
			break
		}
	}
	body := lines[start:end]
	indent := -1
	for _, l := range body {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " ")); indent < 0 || n < indent {
			indent = n
		}
	}
	var out []string
	for _, l := range body {
		l = strings.TrimRight(l, " ")
		if len(l) >= indent && indent > 0 {
			l = l[indent:]
		}
		out = append(out, l)
	}
	text := strings.TrimSpace(strings.Join(out, "\n"))
	return text, text != ""
}
