package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readScreen parses a fixture: first line "cursor=x,y alt=… [rules=id]".
func readScreen(t *testing.T, f string) (lines []string, cy int, rules string, hasRules bool) {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	head, body, _ := strings.Cut(string(b), "\n")
	var cx int
	fmt.Sscanf(head, "cursor=%d,%d", &cx, &cy)
	if i := strings.Index(head, "rules="); i >= 0 {
		rules, hasRules = strings.TrimSpace(head[i+len("rules="):]), true
	}
	return strings.Split(body, "\n"), cy, rules, hasRules
}

// TestInputPromptRefusesDialogs: every dialog, menu, picker, shell prompt
// or unknown screen in testdata/dialogs must be refused by the delivery
// guard (real dumps of Claude Code 2.1.312 / Codex 0.163.0 plus synthetic
// variants: the Codex update dialog with the cursor on "Update now", with
// its header scrolled away, login/theme/API-key/model pickers, [Y/n]).
func TestInputPromptRefusesDialogs(t *testing.T) {
	files, _ := filepath.Glob("testdata/dialogs/*.txt")
	if len(files) < 20 {
		t.Fatalf("dialog fixtures missing: %d", len(files))
	}
	for _, f := range files {
		lines, cy, rules, ok := readScreen(t, f)
		if !ok {
			t.Fatalf("%s: no rules= in header", f)
		}
		if got, why := InputPrompt(rules, lines, cy); got {
			t.Errorf("%s: recognised as input prompt", filepath.Base(f))
		} else if why == "" {
			t.Errorf("%s: no reason", filepath.Base(f))
		}
	}
}

// TestInputPromptAcceptsReadyScreens: the normal prompt of the installed
// CLIs (ready, ready after a turn, after a denied permission, and Claude's
// working screen which keeps its input box) is recognised.
func TestInputPromptAcceptsReadyScreens(t *testing.T) {
	files, _ := filepath.Glob("testdata/screens/*.txt")
	n := 0
	for _, f := range files {
		name := filepath.Base(f)
		cli := strings.Split(name, "-")[0]
		lines, cy, _, _ := readScreen(t, f)
		got, why := InputPrompt(cli, lines, cy)
		want := strings.Contains(name, "-ready") || (cli == "claude" && strings.Contains(name, "-working"))
		if got != want {
			t.Errorf("%s: got %v (%s), want %v", name, got, why, want)
		}
		if want {
			n++
		}
	}
	if n < 5 {
		t.Fatalf("only %d ready fixtures", n)
	}
	// Generic agents: a prompt character at the cursor, no dialog.
	if ok, why := InputPrompt("", []string{"", " done.", "> "}, 2); !ok {
		t.Fatalf("generic prompt refused: %s", why)
	}
	// Prose above Claude's input box does not block delivery …
	box := []string{"● Do you want me to run the tests? Press Enter to continue reading.", "", "──────────", "❯ ", "──────────", "  ⏸ manual mode on"}
	if ok, why := InputPrompt("claude", box, 3); !ok {
		t.Fatalf("prose above box refused: %s", why)
	}
	// … but a dialog marker inside or below the box does.
	box[5] = "  Enter to confirm · Esc to cancel"
	if ok, _ := InputPrompt("claude", box, 3); ok {
		t.Fatal("dialog footer below the box accepted")
	}
	// Cursor outside the screen.
	if ok, _ := InputPrompt("claude", box, 9); ok {
		t.Fatal("cursor outside accepted")
	}
}

func TestInputPromptPasted(t *testing.T) {
	rule := "──────────────────────"
	wrapped := []string{"● ok", rule, "❯ a long pasted order that wraps", "  onto a second row and a third", "  row here", rule, "  ⏸ manual mode on"}
	if ok, _ := InputPrompt("claude", wrapped, 4); ok {
		t.Fatal("strict check must not accept a continuation row")
	}
	if ok, why := InputPromptPasted("claude", wrapped, 4); !ok {
		t.Fatalf("pasted wrap refused: %s", why)
	}
	if ok, why := InputPromptPasted("claude", []string{rule, "❯ [Pasted text #1 +3 lines]", rule}, 1); !ok {
		t.Fatalf("collapsed paste refused: %s", why)
	}
	// A dialog that popped up between paste and Return is refused.
	dlg := []string{" Update available: 2.1.312 → 2.2.0", "", " ❯ 1. Update now", "   2. Skip"}
	if ok, _ := InputPromptPasted("claude", dlg, 2); ok {
		t.Fatal("dialog accepted before Return")
	}
	// Box without bottom border (screen mid-redraw) is refused.
	if ok, _ := InputPromptPasted("claude", wrapped[:5], 4); ok {
		t.Fatal("incomplete box accepted")
	}
	for _, f := range []string{"testdata/dialogs/codex-update-cursor-on-option.txt", "testdata/dialogs/claude-permission.txt", "testdata/dialogs/codex-update-real.txt"} {
		lines, cy, rules, _ := readScreen(t, f)
		if ok, _ := InputPromptPasted(rules, lines, cy); ok {
			t.Fatalf("%s accepted", f)
		}
	}
}
