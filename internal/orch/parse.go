package orch

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The local parser (docs/API-orchestration.md §3.2) turns an English
// command into an Intent. It knows nothing about the board: names in
// <who> positions stay raw strings and are resolved later (§3.3); an
// intent whose names do not resolve falls through to the planner or the
// no-planner fallback.

// Intent kinds.
const (
	IntentNone        = ""
	IntentSend        = "send"        // @Ash <text>
	IntentEveryone    = "everyone"    // @everyone <text> (= no mention)
	IntentMentionOnly = "mentionOnly" // @Ash without text
	IntentSpawn       = "spawn"
	IntentGroupCreate = "group.create"
	IntentGroupAdd    = "group.add"
	IntentDissolve    = "group.dissolve"
	IntentPause       = "pause"
	IntentResume      = "resume"
	IntentRestart     = "restart"
	IntentKill        = "kill"
	IntentTell        = "tell"
	IntentSame        = "same"
	IntentHandoff     = "handoff"
	IntentForward     = "forward"
	IntentCancel      = "cancel"
	IntentAsk         = "ask"
	IntentUI          = "ui"
	IntentNodeAdd     = "node.add"
	IntentWire        = "wire"
	IntentWsGo        = "workspace.go"
	IntentWsCreate    = "workspace.create"
	IntentWsRename    = "workspace.rename"
	IntentWsClose     = "workspace.close"
	IntentLoop        = "loop.create"
	IntentGit         = "git"
	IntentMemPush     = "memory.push"
	IntentMemScan     = "memory.scan"
	IntentDeploy      = "layout.deploy"
	IntentSmalltalk   = "smalltalk"
)

// Roles of groups (§1).
var Roles = []string{"general", "frontend", "backend", "testers", "reviewers", "design", "docs", "security"}

var roleAliases = map[string]string{
	"general": "general", "generalist": "general", "generalists": "general",
	"frontend": "frontend", "frontends": "frontend", "front-end": "frontend",
	"backend": "backend", "backends": "backend", "back-end": "backend",
	"tester": "testers", "testers": "testers", "qa": "testers",
	"reviewer": "reviewers", "reviewers": "reviewers",
	"design": "design", "designer": "design", "designers": "design",
	"docs": "docs", "doc": "docs", "documentation": "docs", "writer": "docs", "writers": "docs",
	"security": "security",
}

// RoleOf maps a word to a role ("" = not a role).
func RoleOf(w string) string { return roleAliases[strings.ToLower(strings.Trim(w, ".,"))] }

// UIAction is run by the sending client (§3.1).
type UIAction struct {
	Action      string `json:"action"`
	Target      string `json:"target,omitempty"`
	Panel       string `json:"panel,omitempty"`
	Tool        string `json:"tool,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	// openFile: a file for the editor card.
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
}

// Intent is the parser's result.
type Intent struct {
	Kind     string
	Mentions []string // raw @names (without @)
	Who      []string // raw names of a <who> position
	To       []string // forward targets
	Text     string   // text for agents (original case)
	Count    int
	Agent    string
	Role     string
	Name     string
	All      bool // "kill all", "pause" without who
	Select   bool // "group these"
	Query    string
	UI       *UIAction
	Focus    string // ui focus: name to resolve
	NodeKind string
	NodeText string
	EverySec int
	Git      string
	Repo     string // git: one of the project's repositories
	Message  string
	From     string
	Port     int
	Reply    string // smalltalk answer
}

// ParseContext lists the agent ids the spawn grammar accepts.
type ParseContext struct {
	Agents []string // agent ids (claude, codex, shell, custom ids…)
}

var numberWords = map[string]int{"a": 1, "an": 1, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "another": 1}

// Number parses a count word or digits (0 = not a number).
func Number(w string) int {
	w = strings.ToLower(w)
	if n, ok := numberWords[w]; ok {
		return n
	}
	if n, err := strconv.Atoi(w); err == nil && n > 0 && n < 1000 {
		return n
	}
	return 0
}

// normalize trims, collapses whitespace and drops trailing ".!" (the
// original case is kept; matching is case-insensitive).
func normalize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRightFunc(s, func(r rune) bool { return r == '.' || r == '!' })
}

func re(p string) *regexp.Regexp { return regexp.MustCompile(`(?i)^` + p + `$`) }

var (
	reMentions  = regexp.MustCompile(`^((?:@[A-Za-z0-9][\w.-]*[\s,:]*)+)(.*)$`)
	reMentionNm = regexp.MustCompile(`@([A-Za-z0-9][\w.-]*)`)
	reSmalltalk = re(`(thanks|thank you|thx|ty|ok|okay|nice|cool|great|good job|well done)[ !.]*`)
	reCancel    = re(`(cancel that|never ?mind|scratch that)`)
	reUndo      = re(`(undo|redo)`)
	reFit       = re(`(show me everything|fit|fit (to )?(screen|all)|zoom to fit)`)
	reZoomIn    = re(`zoom in`)
	reZoomOut   = re(`zoom out`)
	reZoomReset = re(`(reset (the )?zoom|zoom reset|actual size)`)
	reFocus     = re(`focus on (.+)`)
	reGoTo      = re(`go to (.+)`)
	reOpenPanel = re(`open (?:the )?(loops|memory|mission map|mission|settings|git|usage|documents|docs|code|markdown|skills)(?: panel)?`)
	reClosePnl  = re(`close (that|the panel|panel)`)
	rePen       = re(`(pen|i want to draw|let me draw|draw)`)
	reEraser    = re(`(eraser|erase)`)
	reWsGo      = re(`(?:go to|switch to|open) workspace (.+)`)
	reWsNew     = re(`(?:new|create a|create|add a) workspace(?: called (.+))?`)
	reWsRename  = re(`rename (?:this|the) workspace to (.+)`)
	reWsClose   = re(`close workspace (.+)`)
	reNote      = re(`add a (?:sticky )?note(?: that says:? (.+))?`)
	reTile      = re(`add an? (checklist|kanban(?: board)?|calendar|uptime(?: monitor)?)`)
	reOrchNode  = re(`add an? orchestrator(?: (?:card )?for (.+))?`)
	reLocalhost = re(`(?:open|show) (?:http://)?localhost:? ?(\d{2,5})`)
	rePreview   = re(`preview(?: port)? (\d{2,5})`)
	reWire      = re(`connect (.+?) to (?:the )?orchestrator(?: (.+))?`)
	reLoop      = re(`every (?:(\S+) )?(minutes?|mins?|hours?|hrs?|h|m) (.+)`)
	reRepoGit   = re(`repo ("[^"]+"|\S+) (.+)`)
	reCommitPsh = re(`commit and push`)
	reCommitMsg = re(`commit (?:with (?:the )?message:? )(.+)`)
	reCommit    = re(`commit(?: (.+))?`)
	rePush      = re(`push`)
	rePull      = re(`pull`)
	reMemPush   = re(`push (?:the )?memory(?: to (.+))?`)
	reMemScan   = re(`scan (?:the )?memory`)
	reDeploy    = re(`deploy (?:the )?(?:layout )?(.+)`)
	reSpawnAlt  = re(`(?:new console|new terminal|open a (shell|terminal)|open a new (shell|terminal))`)
	reSpawn     = re(`(?:spawn|spin up|start|launch)(?: (.+))?`)
	reGroupOf   = re(`(?:make|create) a group of (\S+)(?: (.+?))?(?: called (.+))?`)
	reGroupThis = re(`group (these|them|the selection|selected)`)
	reGroupWho  = re(`group (.+)`)
	reAddTo     = re(`add (\S+)(?: (?:more )?(\S+?))?(?: agents?| consoles?)? to (?:the )?(.+)`)
	reDissolve  = re(`(?:ungroup|dissolve) (.+)`)
	rePause     = re(`pause(?: (.+))?`)
	reWait      = re(`(wait|hold on|hold)`)
	reResume    = re(`(?:resume|continue|keep going|unpause)(?: (.+))?`)
	reRestart   = re(`(?:restart|reset) (.+)`)
	reKillAll   = re(`(?:kill|stop|close) (?:all|everything|everyone)`)
	reKill      = re(`(?:kill|close|stop) (.+)`)
	reDelete    = re(`delete (.+)`)
	reSame      = re(`(?:give everyone the same prompt|give everybody the same prompt|tell everyone|tell everybody|tell all)[:,]? (.+)`)
	reHandoff   = re(`(?:hand off|handoff|hand over|pass)(?: (?:this|it|the)(?: task)?)? to (?:@)?(\S+?)(?: from @?(\S+))?`)
	reForward   = re(`when (.+?) (?:finish|finishes|finished|is done|are done|completes?|complete),? (?:pass|send|forward|give|hand) (?:it|everything|that|this|the results?|their results?|the output|results)(?: on| over)? to (.+)`)
	reTell      = re(`(?:tell|ask) (.+?) to (.+)`)
	reAddress   = regexp.MustCompile(`^@?([A-Za-z][A-Za-z0-9-]{0,19}), (.+)$`)
)

var askQueries = []struct {
	re *regexp.Regexp
	q  string
}{
	{re(`(status|what'?s the status|how'?s it going|how is it going|how are we doing)\??`), "status"},
	{re(`(who finished|who'?s done|who is done|what happened)\??`), "finished"},
	{re(`(who needs me|who needs you|who'?s waiting( for me)?)\??`), "needsYou"},
	{re(`(give me a summary|summari[sz]e|summary)\??`), "summary"},
	{re(`(help|what can you do|commands)\??`), "help"},
}

// SplitWho splits a <who> list: "Ash, Bea and the reviewers" → [Ash Bea reviewers].
func SplitWho(s string) []string {
	s = strings.NewReplacer(" and ", ",", " & ", ",", " + ", ",").Replace(" " + s + " ")
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "@")
		for _, pre := range []string{"the ", "all ", "all the ", "my "} {
			if strings.HasPrefix(strings.ToLower(p), pre) {
				p = p[len(pre):]
			}
		}
		for _, suf := range []string{" group", " team", " consoles", " console", " agents"} {
			if strings.HasSuffix(strings.ToLower(p), suf) && len(p) > len(suf) {
				p = p[:len(p)-len(suf)]
			}
		}
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isEveryone reports whether a name addresses everyone in scope.
func isEveryone(n string) bool {
	switch strings.ToLower(n) {
	case "everyone", "all", "everybody", "everything":
		return true
	}
	return false
}

// spawnTokens interprets "3 codex reviewers" / "a shell" / "claude".
func spawnTokens(rest string, ctx ParseContext) (n int, agentID, role string, ok bool) {
	agents := map[string]string{"terminal": "shell", "terminals": "shell", "shells": "shell", "claude": "claude", "codex": "codex"}
	for _, a := range ctx.Agents {
		agents[strings.ToLower(a)] = a
	}
	for _, w := range strings.Fields(strings.ToLower(rest)) {
		w = strings.Trim(w, ",.")
		switch {
		case n == 0 && Number(w) > 0:
			n = Number(w)
		case agents[w] != "" && agentID == "":
			agentID = agents[w]
		case strings.HasSuffix(w, "s") && agents[strings.TrimSuffix(w, "s")] != "" && agentID == "":
			agentID = agents[strings.TrimSuffix(w, "s")]
		case RoleOf(w) != "" && role == "":
			role = RoleOf(w)
		case w == "agents", w == "agent", w == "consoles", w == "console", w == "instances", w == "instance",
			w == "new", w == "more", w == "of", w == "code", w == "cli", w == "sessions", w == "session":
		default:
			return 0, "", "", false
		}
	}
	if n == 0 {
		n = 1
	}
	return n, agentID, role, true
}

// Parse runs the grammar of §3.2 on one command.
func Parse(text string, ctx ParseContext) Intent {
	t := normalize(text)
	if t == "" {
		return Intent{}
	}
	if m := reMentions.FindStringSubmatch(t); m != nil {
		var names []string
		everyone := false
		for _, mm := range reMentionNm.FindAllStringSubmatch(m[1], -1) {
			if isEveryone(mm[1]) {
				everyone = true
				continue
			}
			names = append(names, mm[1])
		}
		body := strings.TrimSpace(strings.TrimLeft(m[2], ",: "))
		switch {
		case everyone && body == "":
			return Intent{Kind: IntentMentionOnly, All: true}
		case everyone:
			return Intent{Kind: IntentEveryone, Text: body, Mentions: names}
		case body == "":
			return Intent{Kind: IntentMentionOnly, Mentions: names}
		}
		return Intent{Kind: IntentSend, Mentions: names, Text: body}
	}
	if reSmalltalk.MatchString(t) {
		return Intent{Kind: IntentSmalltalk, Reply: smalltalkReply(t)}
	}
	if reCancel.MatchString(t) {
		return Intent{Kind: IntentCancel}
	}
	if m := reUndo.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentUI, UI: &UIAction{Action: strings.ToLower(m[1])}}
	}
	switch {
	case reFit.MatchString(t):
		return Intent{Kind: IntentUI, UI: &UIAction{Action: "fit"}}
	case reZoomIn.MatchString(t):
		return Intent{Kind: IntentUI, UI: &UIAction{Action: "zoomIn"}}
	case reZoomOut.MatchString(t):
		return Intent{Kind: IntentUI, UI: &UIAction{Action: "zoomOut"}}
	case reZoomReset.MatchString(t):
		return Intent{Kind: IntentUI, UI: &UIAction{Action: "zoomReset"}}
	}
	if m := reOpenPanel.FindStringSubmatch(t); m != nil {
		p := strings.ToLower(m[1])
		switch p {
		case "mission map":
			p = "mission"
		case "docs":
			p = "documents"
		}
		return Intent{Kind: IntentUI, UI: &UIAction{Action: "openPanel", Panel: p}}
	}
	if reClosePnl.MatchString(t) {
		return Intent{Kind: IntentUI, UI: &UIAction{Action: "closePanel"}}
	}
	if rePen.MatchString(t) {
		return Intent{Kind: IntentUI, UI: &UIAction{Action: "tool", Tool: "pen"}}
	}
	if reEraser.MatchString(t) {
		return Intent{Kind: IntentUI, UI: &UIAction{Action: "tool", Tool: "eraser"}}
	}
	if m := reWsGo.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentWsGo, Name: m[1]}
	}
	if m := reWsNew.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentWsCreate, Name: m[1]}
	}
	if m := reWsRename.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentWsRename, Name: m[1]}
	}
	if m := reWsClose.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentWsClose, Name: m[1]}
	}
	if m := reFocus.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentUI, UI: &UIAction{Action: "focus"}, Focus: strings.TrimPrefix(m[1], "@")}
	}
	if m := reGoTo.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentUI, UI: &UIAction{Action: "focus"}, Focus: strings.TrimPrefix(m[1], "@")}
	}
	if m := reNote.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentNodeAdd, NodeKind: "note", NodeText: m[1]}
	}
	if m := reTile.FindStringSubmatch(t); m != nil {
		k := strings.ToLower(m[1])
		if strings.HasPrefix(k, "kanban") {
			k = "kanban"
		}
		if strings.HasPrefix(k, "uptime") {
			k = "uptime"
		}
		return Intent{Kind: IntentNodeAdd, NodeKind: k}
	}
	if m := reOrchNode.FindStringSubmatch(t); m != nil {
		in := Intent{Kind: IntentNodeAdd, NodeKind: "orchestrator"}
		if m[1] != "" {
			in.Who = SplitWho(m[1])
		}
		return in
	}
	for _, r := range []*regexp.Regexp{reLocalhost, rePreview} {
		if m := r.FindStringSubmatch(t); m != nil {
			p, _ := strconv.Atoi(m[1])
			if p > 0 && p < 65536 {
				return Intent{Kind: IntentNodeAdd, NodeKind: "web", Port: p}
			}
		}
	}
	if m := reWire.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentWire, Who: SplitWho(m[1]), Name: strings.TrimSpace(m[2])}
	}
	if m := reLoop.FindStringSubmatch(t); m != nil {
		n := 1
		if m[1] != "" {
			if n = Number(m[1]); n == 0 {
				return Intent{}
			}
		}
		unit := 60
		if u := strings.ToLower(m[2]); strings.HasPrefix(u, "h") {
			unit = 3600
		}
		return Intent{Kind: IntentLoop, EverySec: n * unit, Text: m[3]}
	}
	if m := reRepoGit.FindStringSubmatch(t); m != nil {
		if in := Parse(m[2], ctx); in.Kind == IntentGit {
			in.Repo = strings.Trim(strings.TrimRight(m[1], ":"), `"`)
			return in
		}
	}
	switch {
	case reCommitPsh.MatchString(t):
		return Intent{Kind: IntentGit, Git: "commitPush"}
	case reCommitMsg.MatchString(t):
		return Intent{Kind: IntentGit, Git: "commit", Message: reCommitMsg.FindStringSubmatch(t)[1]}
	case reCommit.MatchString(t):
		return Intent{Kind: IntentGit, Git: "commit", Message: reCommit.FindStringSubmatch(t)[1]}
	case rePush.MatchString(t):
		return Intent{Kind: IntentGit, Git: "push"}
	case rePull.MatchString(t):
		return Intent{Kind: IntentGit, Git: "pull"}
	}
	if m := reMemPush.FindStringSubmatch(t); m != nil {
		in := Intent{Kind: IntentMemPush}
		if m[1] != "" {
			in.Who = SplitWho(m[1])
		}
		return in
	}
	if reMemScan.MatchString(t) {
		return Intent{Kind: IntentMemScan}
	}
	if m := reDeploy.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentDeploy, Name: m[1]}
	}
	for _, a := range askQueries {
		if a.re.MatchString(t) {
			return Intent{Kind: IntentAsk, Query: a.q}
		}
	}
	if m := reSpawnAlt.FindStringSubmatch(t); m != nil {
		in := Intent{Kind: IntentSpawn, Count: 1}
		if m[1] != "" || m[2] != "" {
			in.Agent = "shell"
		}
		return in
	}
	if m := reSpawn.FindStringSubmatch(t); m != nil {
		if n, a, r, ok := spawnTokens(m[1], ctx); ok {
			return Intent{Kind: IntentSpawn, Count: n, Agent: a, Role: r}
		}
	}
	if m := reGroupOf.FindStringSubmatch(t); m != nil {
		if n := Number(m[1]); n > 0 {
			if c, a, r, ok := spawnTokens(m[2], ctx); ok {
				_ = c
				return Intent{Kind: IntentGroupCreate, Count: n, Agent: a, Role: r, Name: m[3]}
			}
		}
	}
	if reGroupThis.MatchString(t) {
		return Intent{Kind: IntentGroupCreate, Select: true}
	}
	if m := reGroupWho.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentGroupCreate, Who: SplitWho(m[1])}
	}
	if m := reAddTo.FindStringSubmatch(t); m != nil {
		if n := Number(m[1]); n > 0 {
			in := Intent{Kind: IntentGroupAdd, Count: n, Name: strings.TrimSpace(m[3])}
			if w := m[2]; w != "" {
				if _, a, r, ok := spawnTokens(w, ctx); ok {
					in.Agent, in.Role = a, r
				} else {
					return Intent{}
				}
			}
			for _, suf := range []string{" group", " team"} {
				if strings.HasSuffix(strings.ToLower(in.Name), suf) && len(in.Name) > len(suf) {
					in.Name = in.Name[:len(in.Name)-len(suf)]
				}
			}
			return in
		}
	}
	if m := reDissolve.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentDissolve, Who: SplitWho(m[1])}
	}
	if reWait.MatchString(t) {
		return Intent{Kind: IntentPause, All: true}
	}
	if m := rePause.FindStringSubmatch(t); m != nil {
		return whoIntent(IntentPause, m[1])
	}
	if m := reResume.FindStringSubmatch(t); m != nil {
		return whoIntent(IntentResume, m[1])
	}
	if m := reRestart.FindStringSubmatch(t); m != nil {
		return whoIntent(IntentRestart, m[1])
	}
	if reKillAll.MatchString(t) {
		return Intent{Kind: IntentKill, All: true}
	}
	if m := reSame.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentSame, Text: m[1]}
	}
	if m := reKill.FindStringSubmatch(t); m != nil {
		return whoIntent(IntentKill, m[1])
	}
	if m := reDelete.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentDissolve, Who: SplitWho(m[1]), Name: "delete"}
	}
	if m := reHandoff.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentHandoff, Agent: strings.ToLower(m[1]), From: m[2]}
	}
	if m := reForward.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentForward, Who: SplitWho(m[1]), To: SplitWho(m[2])}
	}
	if m := reTell.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentTell, Who: SplitWho(m[1]), Text: m[2]}
	}
	if m := reAddress.FindStringSubmatch(t); m != nil {
		return Intent{Kind: IntentTell, Who: []string{m[1]}, Text: m[2], Name: "address"}
	}
	return Intent{}
}

func whoIntent(kind, who string) Intent {
	who = strings.TrimSpace(who)
	if who == "" || isEveryone(who) || strings.EqualFold(who, "everything") {
		return Intent{Kind: kind, All: true}
	}
	return Intent{Kind: kind, Who: SplitWho(who)}
}

func smalltalkReply(t string) string {
	if strings.HasPrefix(strings.ToLower(t), "th") || strings.HasPrefix(strings.ToLower(t), "ty") {
		return "You're welcome."
	}
	return "👍"
}

// singleLine reports whether s is a short one-line directive (§3.11).
func singleLine(s string) bool {
	return !strings.ContainsAny(s, "\n\r") && len([]rune(s)) <= 40
}

// explicitSame reports wording that asks for the same text to everyone.
func explicitSame(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "same prompt") || strings.Contains(l, "everyone the same") || strings.Contains(l, "independent review")
}

// needsComposing reports requests that ask the hub to write instructions
// for agents ("write a brief for …"), which are never sent without the
// planner (§3.11).
func needsComposing(s string) bool {
	l := strings.ToLower(s)
	for _, p := range []string{"write a brief", "write instructions", "brief them", "brief everyone", "split the work", "divide the work", "assign tasks", "split this", "divide this", "plan the work"} {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

// firstUpper reports whether s has an upper-case letter (smart case).
func firstUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}
