package orch

import (
	"reflect"
	"strings"
	"testing"
)

// TestParserGrammar has at least one case per row of the §3.2 table.
func TestParserGrammar(t *testing.T) {
	ctx := ParseContext{Agents: []string{"claude", "codex", "gemini", "shell", "my-bot"}}
	cases := []struct {
		row, text string
		want      Intent
	}{
		// Direct / everyone / mention only.
		{"direct", "@Ash run the tests", Intent{Kind: IntentSend, Mentions: []string{"Ash"}, Text: "run the tests"}},
		{"direct", "@Ash, @Bea: Run The Tests.", Intent{Kind: IntentSend, Mentions: []string{"Ash", "Bea"}, Text: "Run The Tests"}},
		{"everyone", "@everyone fix lint", Intent{Kind: IntentEveryone, Text: "fix lint"}},
		{"everyone", "@all please review", Intent{Kind: IntentEveryone, Text: "please review"}},
		{"everyone", "@everybody stop and summarise", Intent{Kind: IntentEveryone, Text: "stop and summarise"}},
		{"mention only", "@Ash", Intent{Kind: IntentMentionOnly, Mentions: []string{"Ash"}}},
		// Spawn.
		{"spawn", "spawn 3 codex reviewers", Intent{Kind: IntentSpawn, Count: 3, Agent: "codex", Role: "reviewers"}},
		{"spawn", "spin up two agents", Intent{Kind: IntentSpawn, Count: 2}},
		{"spawn", "start a claude", Intent{Kind: IntentSpawn, Count: 1, Agent: "claude"}},
		{"spawn", "launch four testers", Intent{Kind: IntentSpawn, Count: 4, Role: "testers"}},
		{"spawn", "new console", Intent{Kind: IntentSpawn, Count: 1}},
		{"spawn", "open a shell", Intent{Kind: IntentSpawn, Count: 1, Agent: "shell"}},
		{"spawn", "open a terminal", Intent{Kind: IntentSpawn, Count: 1, Agent: "shell"}},
		{"spawn", "spawn twelve", Intent{Kind: IntentSpawn, Count: 12}},
		// Group.
		{"group", "make a group of 3 codex reviewers called Review", Intent{Kind: IntentGroupCreate, Count: 3, Agent: "codex", Role: "reviewers", Name: "Review"}},
		{"group", "create a group of four", Intent{Kind: IntentGroupCreate, Count: 4}},
		{"group", "group Ash and Bea", Intent{Kind: IntentGroupCreate, Who: []string{"Ash", "Bea"}}},
		{"group", "group these", Intent{Kind: IntentGroupCreate, Select: true}},
		// Add / dissolve.
		{"add", "add 2 codex to Backend", Intent{Kind: IntentGroupAdd, Count: 2, Agent: "codex", Name: "Backend"}},
		{"add", "add one reviewer to the backend team", Intent{Kind: IntentGroupAdd, Count: 1, Role: "reviewers", Name: "backend"}},
		{"dissolve", "ungroup Backend", Intent{Kind: IntentDissolve, Who: []string{"Backend"}}},
		{"dissolve", "dissolve the frontend group", Intent{Kind: IntentDissolve, Who: []string{"frontend"}}},
		// Pause / resume.
		{"pause", "pause", Intent{Kind: IntentPause, All: true}},
		{"pause", "pause Ash", Intent{Kind: IntentPause, Who: []string{"Ash"}}},
		{"pause", "wait", Intent{Kind: IntentPause, All: true}},
		{"pause", "hold on", Intent{Kind: IntentPause, All: true}},
		{"resume", "resume", Intent{Kind: IntentResume, All: true}},
		{"resume", "continue Bea", Intent{Kind: IntentResume, Who: []string{"Bea"}}},
		{"resume", "keep going everyone", Intent{Kind: IntentResume, All: true}},
		// Restart / kill.
		{"restart", "restart Ash", Intent{Kind: IntentRestart, Who: []string{"Ash"}}},
		{"restart", "reset @Bea", Intent{Kind: IntentRestart, Who: []string{"Bea"}}},
		{"kill", "kill Ash and Bea", Intent{Kind: IntentKill, Who: []string{"Ash", "Bea"}}},
		{"kill", "stop the reviewers", Intent{Kind: IntentKill, Who: []string{"reviewers"}}},
		{"kill", "close Cass", Intent{Kind: IntentKill, Who: []string{"Cass"}}},
		{"kill", "kill all", Intent{Kind: IntentKill, All: true}},
		{"kill", "delete Backend", Intent{Kind: IntentDissolve, Who: []string{"Backend"}, Name: "delete"}},
		// Tell / same.
		{"tell", "tell Ash to run the tests", Intent{Kind: IntentTell, Who: []string{"Ash"}, Text: "run the tests"}},
		{"tell", "ask Bea and Cass to review the PR", Intent{Kind: IntentTell, Who: []string{"Bea", "Cass"}, Text: "review the PR"}},
		{"tell", "Ash, fix the build", Intent{Kind: IntentTell, Who: []string{"Ash"}, Text: "fix the build", Name: "address"}},
		{"same", "give everyone the same prompt: review main.go", Intent{Kind: IntentSame, Text: "review main.go"}},
		{"same", "tell everyone: commit your work", Intent{Kind: IntentSame, Text: "commit your work"}},
		// Handoff / forward.
		{"handoff", "hand off to codex", Intent{Kind: IntentHandoff, Agent: "codex"}},
		{"handoff", "pass this task to claude from Ash", Intent{Kind: IntentHandoff, Agent: "claude", From: "Ash"}},
		{"forward", "when Ash finishes, pass it to Bea", Intent{Kind: IntentForward, Who: []string{"Ash"}, To: []string{"Bea"}}},
		{"forward", "when the reviewers finish send everything to Cass", Intent{Kind: IntentForward, Who: []string{"reviewers"}, To: []string{"Cass"}}},
		// Cancel / ask.
		{"cancel", "cancel that", Intent{Kind: IntentCancel}},
		{"cancel", "never mind", Intent{Kind: IntentCancel}},
		{"cancel", "scratch that", Intent{Kind: IntentCancel}},
		{"ask", "status", Intent{Kind: IntentAsk, Query: "status"}},
		{"ask", "how's it going?", Intent{Kind: IntentAsk, Query: "status"}},
		{"ask", "who finished", Intent{Kind: IntentAsk, Query: "finished"}},
		{"ask", "what happened", Intent{Kind: IntentAsk, Query: "finished"}},
		{"ask", "who needs me", Intent{Kind: IntentAsk, Query: "needsYou"}},
		{"ask", "give me a summary", Intent{Kind: IntentAsk, Query: "summary"}},
		{"ask", "help", Intent{Kind: IntentAsk, Query: "help"}},
		{"ask", "what can you do", Intent{Kind: IntentAsk, Query: "help"}},
		// Camera.
		{"camera", "show me everything", Intent{Kind: IntentUI, UI: &UIAction{Action: "fit"}}},
		{"camera", "fit", Intent{Kind: IntentUI, UI: &UIAction{Action: "fit"}}},
		{"camera", "zoom in", Intent{Kind: IntentUI, UI: &UIAction{Action: "zoomIn"}}},
		{"camera", "zoom out", Intent{Kind: IntentUI, UI: &UIAction{Action: "zoomOut"}}},
		{"camera", "reset zoom", Intent{Kind: IntentUI, UI: &UIAction{Action: "zoomReset"}}},
		{"camera", "go to Ash", Intent{Kind: IntentUI, UI: &UIAction{Action: "focus"}, Focus: "Ash"}},
		{"camera", "focus on Backend", Intent{Kind: IntentUI, UI: &UIAction{Action: "focus"}, Focus: "Backend"}},
		// Panels.
		{"panel", "open loops", Intent{Kind: IntentUI, UI: &UIAction{Action: "openPanel", Panel: "loops"}}},
		{"panel", "open mission map", Intent{Kind: IntentUI, UI: &UIAction{Action: "openPanel", Panel: "mission"}}},
		{"panel", "open memory", Intent{Kind: IntentUI, UI: &UIAction{Action: "openPanel", Panel: "memory"}}},
		{"panel", "open skills", Intent{Kind: IntentUI, UI: &UIAction{Action: "openPanel", Panel: "skills"}}},
		{"panel", "close that", Intent{Kind: IntentUI, UI: &UIAction{Action: "closePanel"}}},
		// Nodes.
		{"node", "add a note", Intent{Kind: IntentNodeAdd, NodeKind: "note"}},
		{"node", "add a note that says ship on Friday", Intent{Kind: IntentNodeAdd, NodeKind: "note", NodeText: "ship on Friday"}},
		{"node", "add a checklist", Intent{Kind: IntentNodeAdd, NodeKind: "checklist"}},
		{"node", "add a kanban", Intent{Kind: IntentNodeAdd, NodeKind: "kanban"}},
		{"node", "add a calendar", Intent{Kind: IntentNodeAdd, NodeKind: "calendar"}},
		{"node", "add an uptime monitor", Intent{Kind: IntentNodeAdd, NodeKind: "uptime"}},
		{"node", "add an orchestrator", Intent{Kind: IntentNodeAdd, NodeKind: "orchestrator"}},
		{"node", "add an orchestrator for Backend", Intent{Kind: IntentNodeAdd, NodeKind: "orchestrator", Who: []string{"Backend"}}},
		{"node", "open localhost 5173", Intent{Kind: IntentNodeAdd, NodeKind: "web", Port: 5173}},
		{"node", "preview 3000", Intent{Kind: IntentNodeAdd, NodeKind: "web", Port: 3000}},
		// Wire.
		{"wire", "connect Ash to the orchestrator", Intent{Kind: IntentWire, Who: []string{"Ash"}}},
		{"wire", "connect Backend to orchestrator Planning", Intent{Kind: IntentWire, Who: []string{"Backend"}, Name: "Planning"}},
		// Drawing / undo.
		{"draw", "pen", Intent{Kind: IntentUI, UI: &UIAction{Action: "tool", Tool: "pen"}}},
		{"draw", "I want to draw", Intent{Kind: IntentUI, UI: &UIAction{Action: "tool", Tool: "pen"}}},
		{"draw", "eraser", Intent{Kind: IntentUI, UI: &UIAction{Action: "tool", Tool: "eraser"}}},
		{"undo", "undo", Intent{Kind: IntentUI, UI: &UIAction{Action: "undo"}}},
		{"undo", "redo", Intent{Kind: IntentUI, UI: &UIAction{Action: "redo"}}},
		// Workspaces.
		{"workspace", "go to workspace 2", Intent{Kind: IntentWsGo, Name: "2"}},
		{"workspace", "switch to workspace Backend", Intent{Kind: IntentWsGo, Name: "Backend"}},
		{"workspace", "new workspace", Intent{Kind: IntentWsCreate}},
		{"workspace", "new workspace called Infra", Intent{Kind: IntentWsCreate, Name: "Infra"}},
		{"workspace", "rename this workspace to API", Intent{Kind: IntentWsRename, Name: "API"}},
		{"workspace", "close workspace 3", Intent{Kind: IntentWsClose, Name: "3"}},
		// Loops.
		{"loop", "every 15 minutes run the tests", Intent{Kind: IntentLoop, EverySec: 900, Text: "run the tests"}},
		{"loop", "every 2 hours summarise progress", Intent{Kind: IntentLoop, EverySec: 7200, Text: "summarise progress"}},
		{"loop", "every minute check the build", Intent{Kind: IntentLoop, EverySec: 60, Text: "check the build"}},
		// Git.
		{"git", "commit and push", Intent{Kind: IntentGit, Git: "commitPush"}},
		{"git", "commit with the message fix login", Intent{Kind: IntentGit, Git: "commit", Message: "fix login"}},
		{"git", "commit wip parser", Intent{Kind: IntentGit, Git: "commit", Message: "wip parser"}},
		{"git", "push", Intent{Kind: IntentGit, Git: "push"}},
		{"git", "pull", Intent{Kind: IntentGit, Git: "pull"}},
		// Memory / layout / smalltalk.
		{"memory", "push memory", Intent{Kind: IntentMemPush}},
		{"memory", "push memory to Ash", Intent{Kind: IntentMemPush, Who: []string{"Ash"}}},
		{"memory", "scan memory", Intent{Kind: IntentMemScan}},
		{"layout", "deploy Build & review", Intent{Kind: IntentDeploy, Name: "Build & review"}},
		{"smalltalk", "thanks", Intent{Kind: IntentSmalltalk, Reply: "You're welcome."}},
		{"smalltalk", "ok", Intent{Kind: IntentSmalltalk, Reply: "👍"}},
		{"smalltalk", "nice!", Intent{Kind: IntentSmalltalk, Reply: "👍"}},
		{"smalltalk", "cool", Intent{Kind: IntentSmalltalk, Reply: "👍"}},
		// Unmatched text goes to the planner / fallback.
		{"none", "the login page is broken, can someone look", Intent{}},
		{"none", "start the tests", Intent{}},
	}
	rows := map[string]bool{}
	for _, c := range cases {
		got := Parse(c.text, ctx)
		rows[c.row] = true
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %q:\n got  %+v (ui %+v)\n want %+v (ui %+v)", c.row, c.text, got, got.UI, c.want, c.want.UI)
		}
	}
	for _, r := range strings.Fields("direct everyone spawn group add dissolve pause resume restart kill tell same handoff forward cancel ask camera panel node wire draw undo workspace loop git memory layout smalltalk") {
		if !rows[r] {
			t.Errorf("row %s has no test", r)
		}
	}
}

func TestSplitWhoAndNumbers(t *testing.T) {
	if got := SplitWho("@Ash, Bea and the reviewers"); !reflect.DeepEqual(got, []string{"Ash", "Bea", "reviewers"}) {
		t.Fatal(got)
	}
	for w, n := range map[string]int{"a": 1, "an": 1, "twelve": 12, "7": 7, "x": 0, "0": 0} {
		if Number(w) != n {
			t.Errorf("%s → %d", w, Number(w))
		}
	}
}

func TestNameMatching(t *testing.T) {
	if Norm("@Ash-2!") != "ash2" {
		t.Fatal(Norm("@Ash-2!"))
	}
	for _, c := range []struct {
		typed, name string
		want        bool
	}{
		{"ahs", "ash", true},        // transposition, short name
		{"bae", "bea", true},        // transposition
		{"cas", "cass", true},       // deletion
		{"quil", "quill", true},     // deletion
		{"asb", "ash", true},        // one substitution
		{"zzz", "ash", false},       // nothing alike
		{"backnd", "backend", true}, // ≤ 2 for longer names
		{"bkend", "backend", true},
		{"fronted", "backend", false},
		{"filip", "philip", true}, // same metaphone
		{"ash", "ash", false},     // exact is not "similar"
	} {
		if got := Similar(c.typed, c.name); got != c.want {
			t.Errorf("Similar(%q,%q) = %v", c.typed, c.name, got)
		}
	}
	for w, k := range map[string]string{"Philip": "FLP", "Filip": "FLP", "Thomas": "TMS", "Knight": "NT", "Xavier": "SF", "Ash": "AX", "Schmidt": "XMT"} {
		if got := Metaphone(w); got != k {
			t.Errorf("Metaphone(%s) = %q, want %q", w, got, k)
		}
	}
}
