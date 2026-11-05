package agent

import (
	"regexp"
	"strings"
)

// Pattern is one screen rule. Where "cursorLine" matches only the line the
// cursor is on; otherwise the bottom ScanLines non-empty lines are tried.
type Pattern struct {
	Re    string `json:"re"`
	Where string `json:"where,omitempty"`
	// Label replaces the matching line as the needsYou activity (e.g.
	// "Trust folder?"); Startup marks dialogs a CLI shows before or
	// outside a turn (trust, update, login), which may raise needsYou even
	// while hooks drive the state.
	Label   string `json:"label,omitempty"`
	Startup bool   `json:"startup,omitempty"`
	re      *regexp.Regexp
}

// ScreenRules recognise a CLI's state from its visible screen (§6.4).
type ScreenRules struct {
	ID        string    `json:"id"`
	ScanLines int       `json:"scanLines"`
	NeedsYou  []Pattern `json:"needsYou"`
	Working   []Pattern `json:"working"`
	Ready     []Pattern `json:"ready"`
}

// ScreenMatch is the outcome of evaluating rules on one screen.
type ScreenMatch struct {
	NeedsYou string // the matching line or the pattern's label ("" = none)
	Startup  bool   // NeedsYou is a startup dialog (trust, update, login)
	Working  bool
	Ready    bool
}

// rules are data in the binary, taken from screen dumps of the installed
// CLI versions (internal/agent/testdata/screens/<cli>-<version>-<state>.txt).
// Gemini is not installed on the reference machine: its patterns are from
// the public UI and are marked unverified.
var rules = map[string]*ScreenRules{
	// Claude Code 2.1.312.
	"claude": {ID: "claude", ScanLines: 14,
		NeedsYou: []Pattern{
			// Folder trust (2.1.312: "Quick safety check: Is this a project
			// you created or one you trust? … ❯ No, exit / Yes, I trust this
			// folder"; Enter = "No, exit"). Short fragments, so a narrow
			// console that wraps the question still matches.
			{Re: `(?i)Yes, I trust this folder`, Label: "Trust folder?", Startup: true},
			{Re: `(?i)Quick safety check`, Label: "Trust folder?", Startup: true},
			{Re: `(?i)project you created or one you trust`, Label: "Trust folder?", Startup: true},
			{Re: `(?i)Do you trust the files in this folder`, Label: "Trust folder?", Startup: true},
			{Re: `^\s*❯?\s*No, exit\s*$`, Label: "Trust folder?", Startup: true},
			{Re: `(?i)Select login method|Choose the text style`, Label: "Setup dialog", Startup: true},
			// First run in a new config folder (2.1.316): the theme picker is
			// taller than the scan window, only its preview footer is in it,
			// and "❯ ✔ Dark mode" alone would read as the input prompt.
			{Re: `(?i)^\s*Syntax theme:`, Label: "Setup dialog", Startup: true},
			{Re: `Do you want to (proceed|make this edit|create|run|allow|use)`},
			{Re: `^\s*❯\s*1\.\s*(Yes|Allow)`},
			{Re: `(?i)Enter to confirm\s*·\s*Esc to (cancel|exit)`},
		},
		// "✽ Elucidating…" while working; "✻ Cogitated for 2s · done" after.
		Working: []Pattern{{Re: `(?i)esc to interrupt`}, {Re: `^\s*[·✢✳✶✻✽*]\s*\p{L}[\p{L} ]*…`}},
		Ready:   []Pattern{{Re: `^\s*[>❯](\s|$)`, Where: "cursorLine"}, {Re: `^\s*│\s*>\s`, Where: "cursorLine"}},
	},
	// Codex 0.163.0 (prompt "› Ask Codex to do anything"; "• OK / Worked
	// for 3s" after a turn; the startup update dialog is a needsYou).
	"codex": {ID: "codex", ScanLines: 16,
		NeedsYou: []Pattern{
			{Re: `(?i)Would you like to (run|make|apply|allow)`},
			{Re: `(?i)Allow (command|Codex)`},
			{Re: `(?i)Do you trust the contents of this directory`, Label: "Trust folder?", Startup: true},
			// The update dialog line starts with it; the boxed banner of the
			// ready screen ("│ ✨ Update available! …") does not. Its default
			// answer is "Update now".
			{Re: `(?i)^\s*Update available`, Label: "Update dialog", Startup: true},
			{Re: `(?i)Update now`, Label: "Update dialog", Startup: true},
			{Re: `(?i)enter continue\s*·\s*esc skip`, Label: "Update dialog", Startup: true},
			{Re: `(?i)Sign in with ChatGPT`, Label: "Login dialog", Startup: true},
			{Re: `(?i)^\s*›\s*1\.\s*Yes`},
			{Re: `(?i)Press enter to confirm or esc to cancel`},
			// Approval of an MCP tool (0.165.0).
			{Re: `(?i)Allow the .{1,60} MCP server to run tool`},
			{Re: `(?i)enter to submit \| esc to cancel`},
		},
		Working: []Pattern{{Re: `(?i)esc to interrupt`}, {Re: `(?i)Working \(\d+s`}},
		Ready:   []Pattern{{Re: `^\s*›\s`, Where: "cursorLine"}, {Re: `^\s*▌\s`, Where: "cursorLine"}},
	},
	// Gemini CLI (unverified).
	"gemini": {ID: "gemini", ScanLines: 14,
		NeedsYou: []Pattern{
			{Re: `(?i)Allow execution`},
			{Re: `(?i)Apply this change\?`},
			{Re: `(?i)Do you want to proceed`},
			{Re: `(?i)Waiting for user confirmation`},
		},
		Working: []Pattern{{Re: `(?i)esc to cancel`}},
		Ready:   []Pattern{{Re: `^\s*│?\s*>\s`, Where: "cursorLine"}, {Re: `(?i)Type your message`}},
	},
}

func init() {
	for _, r := range rules {
		for _, l := range []*[]Pattern{&r.NeedsYou, &r.Working, &r.Ready} {
			for i := range *l {
				(*l)[i].re = regexp.MustCompile((*l)[i].Re)
			}
		}
	}
}

// RulesFor returns the screen rules by id (nil = generic activity only).
func RulesFor(id string) *ScreenRules { return rules[id] }

// Evaluate runs the rules over a screen: lines are the visible rows and
// cursorY the cursor row.
func (r *ScreenRules) Evaluate(lines []string, cursorY int) ScreenMatch {
	var m ScreenMatch
	if r == nil {
		return m
	}
	// TUIs pad with NO-BREAK SPACE (Claude Code draws "❯\u00a0"); RE2's \s
	// is ASCII-only, so normalise first.
	norm := make([]string, len(lines))
	for i, l := range lines {
		norm[i] = strings.ReplaceAll(l, "\u00a0", " ")
	}
	lines = norm
	// Bottom ScanLines non-empty lines, top to bottom.
	var scan []string
	for i := len(lines) - 1; i >= 0 && len(scan) < r.ScanLines; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			scan = append(scan, lines[i])
		}
	}
	for i, j := 0, len(scan)-1; i < j; i, j = i+1, j-1 {
		scan[i], scan[j] = scan[j], scan[i]
	}
	cursor := ""
	if cursorY >= 0 && cursorY < len(lines) {
		cursor = lines[cursorY]
	}
	match := func(ps []Pattern) (string, *Pattern) {
		for i, p := range ps {
			if p.Where == "cursorLine" {
				if p.re.MatchString(cursor) {
					return cursor, &ps[i]
				}
				continue
			}
			for _, l := range scan {
				if p.re.MatchString(l) {
					return l, &ps[i]
				}
			}
		}
		return "", nil
	}
	if l, p := match(r.NeedsYou); p != nil {
		m.NeedsYou, m.Startup = strings.TrimSpace(l), p.Startup
		if p.Label != "" {
			m.NeedsYou = p.Label
		}
	}
	w, _ := match(r.Working)
	m.Working = w != ""
	rd, _ := match(r.Ready)
	m.Ready = !m.Working && m.NeedsYou == "" && rd != ""
	return m
}
