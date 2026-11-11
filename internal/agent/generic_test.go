package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

// Real dumps of Claude Code and Codex read without their own rules: what a CLI we
// have no rules for shows looks much the same.
func TestGenericEvaluateOnRealScreens(t *testing.T) {
	files, _ := filepath.Glob("testdata/screens/*.txt")
	if len(files) < 9 {
		t.Fatalf("fixtures missing: %v", files)
	}
	for _, f := range files {
		name := filepath.Base(f)
		// Its picker looks like a prompt ("❯ ✔ Dark mode"): only Claude's own rules know it.
		if strings.Contains(name, "first-run-theme") {
			continue
		}
		lines, cy, _, _ := readScreen(t, f)
		m := GenericEvaluate("", lines, cy)
		switch want := strings.Split(strings.TrimSuffix(name, ".txt"), "-")[2]; want {
		case "needsYou":
			if m.NeedsYou == "" {
				t.Errorf("%s: dialog not seen: %+v", name, m)
			}
		case "ready":
			if m.NeedsYou != "" || m.Working || !m.Ready {
				t.Errorf("%s: %+v", name, m)
			}
		case "working":
			// Its input box stays while it works: the spinner's output says working (T18).
			if m.NeedsYou != "" {
				t.Errorf("%s: %+v", name, m)
			}
		}
	}
}

func TestGenericEvaluateOnDialogs(t *testing.T) {
	for _, name := range []string{
		"claude-permission", "claude-trust", "claude-update-dialog", "claude-login-method", "claude-menu-in-box",
		"codex-approval", "codex-trust", "codex-update-real", "codex-mcp-tool-approval", "generic-allow", "generic-update-yn",
	} {
		lines, cy, _, _ := readScreen(t, "testdata/dialogs/"+name+".txt")
		if m := GenericEvaluate("", lines, cy); m.NeedsYou == "" || m.Ready {
			t.Errorf("%s: %+v", name, m)
		}
	}
}

func TestGenericEvaluateCases(t *testing.T) {
	for name, c := range map[string]struct {
		lines                    []string
		cursor                   int
		needsYou, working, ready bool
	}{
		"aider asks": {lines: []string{"Run shell command? (Y)es/(N)o/(D)on't ask again [Yes]: "}, cursor: 0, needsYou: true},
		"y/n prompt": {lines: []string{"Apply this change? [y/N]", "(y/n) "}, cursor: 1, needsYou: true},
		"a question at the end of a reply, at the prompt": {lines: []string{"Do you want me to go on?", "", "> "}, cursor: 2, ready: true},
		"the same question with no prompt":                {lines: []string{"Do you want me to go on?", ""}, cursor: 1, needsYou: true},
		"a question answered, cursor moved on":            {lines: []string{"Apply this change? (y/n) y", ""}, cursor: 1},
		"the next question":                               {lines: []string{"Apply this change? (y/n) y", "Next file? (y/n) "}, cursor: 1, needsYou: true},
		"working hint":                                    {lines: []string{"Thinking...", "", "esc to interrupt"}, cursor: 1, working: true},
		"esc to cancel is no dialog by itself":            {lines: []string{"Generating", "esc to cancel"}, cursor: 1, working: true},
		"ctrl+c hint":                                     {lines: []string{"Running tools  ctrl+c to stop"}, cursor: 0, working: true},
		"hint scrolled far up":                            {lines: []string{"esc to interrupt", "a", "b", "c", "d", "e", "f", "g", "> "}, cursor: 8, ready: true},
		"unknown screen":                                  {lines: []string{"compiling", ""}, cursor: 1},
		"prompt":                                          {lines: []string{"done.", "> "}, cursor: 1, ready: true},
	} {
		m := GenericEvaluate("", c.lines, c.cursor)
		if (m.NeedsYou != "") != c.needsYou || m.Working != c.working || m.Ready != c.ready {
			t.Errorf("%s: %+v", name, m)
		}
	}
}

// A real dump of OpenCode 1.17.15, idle: no prompt character, a box with a heavy
// bar. Orders could never be typed into it.
func TestOpenCodeInputPrompt(t *testing.T) {
	lines, cy, rules, ok := readScreen(t, "testdata/generic/opencode-1.17.15-ready.txt")
	if !ok || rules != "opencode" {
		t.Fatalf("fixture header: %q", rules)
	}
	if got, why := InputPrompt("opencode", lines, cy); !got {
		t.Errorf("idle OpenCode refused: %s", why)
	}
	if m := GenericEvaluate("opencode", lines, cy); !m.Ready || m.NeedsYou != "" || m.Working {
		t.Errorf("%+v", m)
	}
	if got, _ := InputPrompt("", lines, cy); got {
		t.Error("the heavy bar should only count for OpenCode")
	}
	// Other lines of that box, and a permission footer, are no place to type.
	if got, _ := InputPrompt("opencode", lines, 14); got {
		t.Error("the empty row above the input accepted")
	}
	withFooter := append([]string{}, lines...)
	withFooter[19] = "   enter confirm  esc dismiss"
	if got, _ := InputPrompt("opencode", withFooter, cy); got {
		t.Error("a dialog footer accepted")
	}
	// After a turn the placeholder is gone and the input row is a bare bar (a real
	// screen of OpenCode 1.17.15 on an API provider): the second order was refused.
	lines, cy, _, _ = readScreen(t, "testdata/generic/opencode-1.17.15-after-turn.txt")
	if got, why := InputPrompt("opencode", lines, cy); !got {
		t.Errorf("OpenCode after a turn refused: %s", why)
	}
	if m := GenericEvaluate("opencode", lines, cy); !m.Ready || m.NeedsYou != "" {
		t.Errorf("after a turn %+v", m)
	}
	// and the answer is read off that screen, not off the redraws
	if got, ok := ScreenAnswer("opencode", lines); !ok || got != "391" {
		t.Errorf("answer %q %v", got, ok)
	}
	if got, ok := ScreenAnswer("opencode", lines[:7]); ok {
		t.Errorf("an unfinished answer read: %q", got)
	}
	if _, ok := ScreenAnswer("claude", lines); ok {
		t.Error("only OpenCode is read this way")
	}
	for _, y := range []int{1, 3, cy - 1, cy + 1} { // the bars of the message above, the box's padding
		if got, _ := InputPrompt("opencode", lines, y); got {
			t.Errorf("row %d accepted: %q", y, lines[y])
		}
	}
	var found bool
	for _, a := range Catalog() {
		if a.ID == "opencode" {
			found = a.ScreenRules == "opencode" && RulesFor(a.ScreenRules) == nil
		}
	}
	if !found {
		t.Error("the catalog entry must name the input shape and have no state rules")
	}
}
