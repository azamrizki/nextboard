package native

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/nextcode/native/tools"
)

// testAgent is an agent for the permission tests, not running: an add-dir and a blocked pattern.
func testAgent(t *testing.T) (*agent, string) {
	t.Helper()
	cfg := newConfig(t, newFake(), newKit())
	extra := filepath.Join(t.TempDir(), "extra")
	if err := os.MkdirAll(extra, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.addDirs = []string{extra}
	cfg.blocked = []string{"git push", "*db:reset*"}
	a, err := newAgent(cfg, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return a, extra
}

func TestPermissionModes(t *testing.T) {
	a, extra := testAgent(t)
	in := func(k, v string) json.RawMessage { b, _ := json.Marshal(map[string]string{k: v}); return b }
	const run, ask, deny = "run", "ask", "deny"
	type call struct {
		kind, name string
		input      json.RawMessage
	}
	calls := map[string]call{
		"read":        {tools.KindRead, "Read", in("file_path", "/etc/hosts")},
		"meta":        {tools.KindMeta, "TodoWrite", json.RawMessage(`{"todos":[]}`)},
		"edit inside": {tools.KindEdit, "Write", in("file_path", "src/new/file.go")},
		"edit extra":  {tools.KindEdit, "Edit", in("file_path", filepath.Join(extra, "x.md"))},
		"edit out":    {tools.KindEdit, "Write", in("file_path", "/etc/passwd")},
		"edit none":   {tools.KindEdit, "Write", json.RawMessage(`{}`)},
		"exec ro":     {tools.KindExec, "Bash", in("command", "ls -la")},
		"exec rw":     {tools.KindExec, "Bash", in("command", "npm install")},
		"exec block":  {tools.KindExec, "Bash", in("command", "git push --force origin main")},
		"exec block2": {tools.KindExec, "Bash", in("command", "ls && rake db:reset")},
		"fetch":       {tools.KindFetch, "WebFetch", in("url", "https://Docs.Example.com/a?b")},
		"other":       {"strange", "Other", json.RawMessage(`{}`)},
	}
	want := map[string]map[string]string{
		modeDefault: {"read": run, "meta": run, "edit inside": ask, "edit extra": ask, "edit out": ask, "edit none": ask, "exec ro": ask, "exec rw": ask, "exec block": ask, "exec block2": ask, "fetch": ask, "other": ask},
		modeAccept:  {"read": run, "meta": run, "edit inside": run, "edit extra": run, "edit out": ask, "edit none": ask, "exec ro": ask, "exec rw": ask, "exec block": ask, "exec block2": ask, "fetch": ask, "other": ask},
		modeAuto:    {"read": run, "meta": run, "edit inside": run, "edit extra": run, "edit out": ask, "edit none": ask, "exec ro": run, "exec rw": ask, "exec block": ask, "exec block2": ask, "fetch": run, "other": ask},
		modePlan:    {"read": run, "meta": run, "edit inside": deny, "edit extra": deny, "edit out": deny, "edit none": deny, "exec ro": run, "exec rw": deny, "exec block": ask, "exec block2": ask, "fetch": ask, "other": deny},
		modeBypass:  {"read": run, "meta": run, "edit inside": run, "edit extra": run, "edit out": run, "edit none": run, "exec ro": run, "exec rw": run, "exec block": ask, "exec block2": ask, "fetch": run, "other": run},
	}
	for _, mode := range modes {
		a.setMode(mode)
		for name, c := range calls {
			v := a.decide(c.kind, c.name, c.input)
			got := run
			switch {
			case v.deny != "":
				got = deny
			case v.ask != nil:
				got = ask
			case !v.run:
				got = "nothing"
			}
			if got != want[mode][name] {
				t.Errorf("%s, %s: %s, want %s", mode, name, got, want[mode][name])
			}
			if got == deny && v.deny != planDenied {
				t.Errorf("%s, %s: denied with %q", mode, name, v.deny)
			}
			if got == ask && strings.HasPrefix(name, "exec block") {
				if !strings.Contains(v.ask.reason, "project's list of commands that need the owner's approval") || v.ask.suggestions != nil {
					t.Errorf("%s, %s: %+v", mode, name, v.ask)
				}
			}
		}
	}
	a.setMode(modeDefault)
	v := a.decide(tools.KindEdit, "Write", calls["edit out"].input)
	if v.ask.blockedPath != "/etc/passwd" || v.ask.suggestions != nil {
		t.Errorf("outside edit: %+v", v.ask)
	}
	v = a.decide(tools.KindEdit, "Write", calls["edit inside"].input)
	if v.ask.blockedPath != "" || len(v.ask.suggestions) != 1 {
		t.Errorf("inside edit: %+v", v.ask)
	}
	v = a.decide(tools.KindFetch, "WebFetch", calls["fetch"].input)
	if b, _ := json.Marshal(v.ask.suggestions); !strings.Contains(string(b), `"ruleContent":"domain:docs.example.com"`) {
		t.Errorf("fetch suggestion: %s", b)
	}
	if v := a.decide(tools.KindExec, "Bash", json.RawMessage(`{"command":"  "}`)); v.deny == "" {
		t.Errorf("an empty command: %+v", v)
	}
}

func TestSessionRulesUnit(t *testing.T) {
	a, _ := testAgent(t)
	a.apply([]permUpdate{{Type: "addRules", Behavior: "allow", Rules: []ruleJSON{{ToolName: "Bash", RuleContent: "git:*"}, {ToolName: "Bash", RuleContent: "npm test:*"},
		{ToolName: "WebFetch", RuleContent: "domain:go.dev"}}, Destination: "session"},
		{Type: "addRules", Behavior: "deny", Rules: []ruleJSON{{ToolName: "Bash", RuleContent: "rm:*"}}}})
	cmd := func(s string) json.RawMessage { b, _ := json.Marshal(map[string]string{"command": s}); return b }
	cases := map[string]bool{
		"npm test":            true,
		"npm test -- -run X":  true,
		"npm testing":         false,
		"npm install":         false,
		"git status":          true,
		"git push":            false, // blocked: no rule lets it go
		"git log | head":      false, // compound
		"git log; rm -rf /":   false,
		"git log && rm":       false,
		"git log $(rm x)":     false,
		"go test ./... 2>&1":  false, // no rule for go
		"git diff 2>&1":       true,  // a redirection is not compound
		"git diff > out.txt":  true,
		"git commit -m \"a\"": true,
	}
	for c, want := range cases {
		v := a.decide(tools.KindExec, "Bash", cmd(c))
		if got := v.run; got != want {
			t.Errorf("%q: run %v, want %v (%+v)", c, got, want, v)
		}
	}
	if v := a.decide(tools.KindFetch, "WebFetch", json.RawMessage(`{"url":"https://GO.dev/doc"}`)); !v.run {
		t.Error("the domain rule did not allow go.dev")
	}
	if v := a.decide(tools.KindFetch, "WebFetch", json.RawMessage(`{"url":"https://evil.go.dev.example/x"}`)); v.run {
		t.Error("the domain rule allowed another host")
	}
	// plan mode is not opened by a rule
	a.setMode(modePlan)
	if v := a.decide(tools.KindExec, "Bash", cmd("npm test")); v.deny == "" {
		t.Errorf("plan mode let a rule run: %+v", v)
	}
	if !a.apply([]permUpdate{{Type: "setMode", Mode: modeAccept, Destination: "session"}}) {
		t.Error("setMode did not change the mode")
	}
	if a.apply([]permUpdate{{Type: "setMode", Mode: "nonsense"}}) {
		t.Error("an unknown mode was taken")
	}
	if m, _ := a.permissions(); m != modeAccept {
		t.Errorf("mode %s", m)
	}
}

func TestCommandPrefix(t *testing.T) {
	cases := map[string]string{
		"npm test -- -run X":          "npm test",
		"git status":                  "git status",
		"git -C /x status":            "",
		"go test ./...":               "go test",
		"python -m pytest -q":         "python -m pytest",
		"python3 -m venv .v":          "python3 -m venv",
		"python script.py":            "python",
		"ls -la":                      "ls",
		"make":                        "make",
		"FOO=1 npm test":              "",
		"./scripts/check.sh --strict": "./scripts/check.sh",
		"":                            "",
	}
	for in, want := range cases {
		if got := commandPrefix(in); got != want {
			t.Errorf("commandPrefix(%q) = %q, want %q", in, got, want)
		}
	}
	for c, want := range map[string]bool{"a && b": true, "a || b": true, "a | b": true, "a; b": true, "a &": true, "a `b`": true, "a $(b)": true,
		"a\nb": true, "a <(b)": true, "a 2>&1": false, "a &> f": false, "a > f": false, "a": false} {
		if compound(c) != want {
			t.Errorf("compound(%q) != %v", c, want)
		}
	}
}

func TestRealPath(t *testing.T) {
	root := t.TempDir()
	real, _ := filepath.EvalSymlinks(root)
	work := filepath.Join(real, "work")
	outside := filepath.Join(real, "outside")
	for _, d := range []string{work, outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// a link inside the working folder to a folder outside it
	if err := os.Symlink(outside, filepath.Join(work, "out")); err != nil {
		t.Fatal(err)
	}
	// a link to a file that does not exist yet, outside
	if err := os.Symlink(filepath.Join(outside, "later.txt"), filepath.Join(work, "dangling")); err != nil {
		t.Fatal(err)
	}
	// a relative link that stays inside
	if err := os.Symlink("sub", filepath.Join(work, "alias")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		filepath.Join(work, "a", "b", "new.go"):   filepath.Join(work, "a", "b", "new.go"),
		filepath.Join(work, "out", "x", "new.go"): filepath.Join(outside, "x", "new.go"),
		filepath.Join(work, "dangling"):           filepath.Join(outside, "later.txt"),
		filepath.Join(work, "alias", "f"):         filepath.Join(work, "sub", "f"),
		filepath.Join(work, "..", "outside", "f"): filepath.Join(outside, "f"),
		filepath.Join(work, "x", "..", "..", "y"): filepath.Join(real, "y"),
	}
	for in, want := range cases {
		if got := realPath(in); got != want {
			t.Errorf("realPath(%s) = %s, want %s", in, got, want)
		}
	}
	if !within(work, filepath.Join(work, "a")) || within(work, outside) || within(work, work+"-other") || !within(work, work) {
		t.Error("within")
	}
	// the agent's check, with the working folder through a link
	cfg := newConfig(t, newFake(), newKit())
	cfg.cwd = work
	a, err := newAgent(cfg, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !a.inside(filepath.Join(work, "src", "x.go")) || a.inside(filepath.Join(work, "out", "x.go")) || a.inside(filepath.Join(work, "dangling")) || !a.inside("rel/path.go") {
		t.Error("inside")
	}
}

func TestPlanMode(t *testing.T) {
	fake := newFake(
		callTool("t1", "Write", `{"file_path":"a.txt","content":"x"}`),
		callTool("t2", "Bash", `{"command":"ls src"}`),
		callTool("t3", "ExitPlanMode", `{"plan":"1. Write a.txt"}`),
		callTool("t4", "Write", `{"file_path":"a.txt","content":"x"}`),
		say("done"),
	)
	k := newKit()
	cfg := newConfig(t, fake, k)
	cfg.mode = modePlan
	h := startAgent(t, cfg)
	h.initialize()
	h.say("plan it")
	req, before := h.request()
	rs := toolResults(before)
	if len(rs) != 2 || rs[0]["content"] != planDenied || rs[0]["is_error"] != true || rs[1]["content"] != "Bash ran: ls src" {
		t.Fatalf("results before the plan: %v", rs)
	}
	if str(req, "request", "tool_name") != "ExitPlanMode" || str(req, "request", "input", "plan") != "1. Write a.txt" || field(req, "request", "permission_suggestions") != nil {
		t.Fatalf("plan request: %v", req)
	}
	h.allow(str(req, "request_id"), map[string]any{"plan": "1. Write a.txt"}, nil)
	req2, before := h.request()
	ini, _ := firstOf(before, is("system", "init"))
	if ini == nil || ini["permissionMode"] != "default" {
		t.Fatalf("no init with the new mode in %v", before)
	}
	if rs := toolResults(before); len(rs) != 1 || rs[0]["content"] != "The owner approved the plan. Carry it out now." {
		t.Fatalf("plan result: %v", rs)
	}
	// default mode now: the edit asks
	if str(req2, "request", "tool_name") != "Write" {
		t.Fatalf("after the plan: %v", req2)
	}
	h.allow(str(req2, "request_id"), map[string]any{"file_path": "a.txt", "content": "x"}, nil)
	h.result()
	if len(k.write.ran()) != 1 {
		t.Fatalf("Write ran %d times", len(k.write.ran()))
	}
	first := texts(fake.request(t, 0).Messages[0])
	if !strings.Contains(first[0], "The permission mode is plan:") {
		t.Fatalf("not told about plan mode: %q", first)
	}
	// told by the result: no reminder of the mode after the plan
	for _, s := range texts(lastMessage(fake.request(t, 3))) {
		if strings.Contains(s, "permission mode") {
			t.Fatalf("a reminder after the approval: %q", s)
		}
	}
	if fake.request(t, 0).System != fake.request(t, 4).System || len(fake.request(t, 0).Tools) != len(fake.request(t, 4).Tools) {
		t.Fatal("the system prompt or the tools changed with the mode")
	}
}

func TestPlanDenied(t *testing.T) {
	fake := newFake(callTool("t1", "ExitPlanMode", `{"plan":"x"}`), say("ok"))
	cfg := newConfig(t, fake, newKit())
	cfg.mode = modePlan
	h := startAgent(t, cfg)
	h.initialize()
	h.say("plan")
	req, _ := h.request()
	h.deny(str(req, "request_id"), "add tests", false)
	_, before := h.result()
	if rs := toolResults(before); len(rs) != 1 || rs[0]["content"] != "The owner wants you to keep planning: add tests" || rs[0]["is_error"] != true {
		t.Fatalf("results: %v", rs)
	}
}

func TestExitPlanOutsidePlanMode(t *testing.T) {
	fake := newFake(callTool("t1", "ExitPlanMode", `{"plan":"x"}`), say("ok"))
	h := startAgent(t, newConfig(t, fake, newKit()))
	h.initialize()
	h.say("go")
	_, before := h.result()
	if slices := toolResults(before); len(slices) != 1 || slices[0]["content"] != "You are not in plan mode." {
		t.Fatalf("results: %v", slices)
	}
	if _, ok := firstOf(before, is("control_request")); ok {
		t.Fatal("asked the owner")
	}
}

func TestBlockedInBypass(t *testing.T) {
	fake := newFake(callTool("t1", "Bash", `{"command":"git push --force origin main"}`), say("pushed"))
	k := newKit()
	cfg := newConfig(t, fake, k)
	cfg.mode = modeBypass
	cfg.blocked = []string{"git push"}
	h := startAgent(t, cfg)
	h.initialize()
	h.say("push")
	req, _ := h.request()
	if !strings.Contains(str(req, "request", "decision_reason"), "project's list of commands that need the owner's approval") ||
		field(req, "request", "permission_suggestions") != nil {
		t.Fatalf("request: %v", req)
	}
	h.allow(str(req, "request_id"), map[string]any{"command": "git push --force origin main"}, nil)
	h.result()
	if len(k.bash.ran()) != 1 {
		t.Fatal("did not run once allowed")
	}
}

func TestSessionRules(t *testing.T) {
	fake := newFake(
		callTool("t1", "Bash", `{"command":"npm test"}`),
		callTool("t2", "Bash", `{"command":"npm test -- --watch=false"}`),
		callTool("t3", "Bash", `{"command":"npm install"}`),
		callTool("t4", "Bash", `{"command":"npm test && curl x"}`),
		say("done"),
	)
	k := newKit()
	h := startAgent(t, newConfig(t, fake, k))
	h.initialize()
	h.say("test")
	req, _ := h.request()
	h.allow("nc_1", map[string]any{"command": "npm test"}, field(req, "request", "permission_suggestions"))
	req, before := h.request()
	if rs := toolResults(before); len(rs) != 2 || rs[1]["tool_use_id"] != "t2" || rs[1]["is_error"] != false {
		t.Fatalf("t2 did not run by the rule: %v", rs)
	}
	if str(req, "request", "tool_use_id") != "t3" {
		t.Fatalf("asked %v", req)
	}
	h.allow(str(req, "request_id"), map[string]any{"command": "npm install"}, nil)
	req, _ = h.request()
	if str(req, "request", "tool_use_id") != "t4" || field(req, "request", "permission_suggestions") != nil {
		t.Fatalf("the compound command: %v", req)
	}
	h.deny(str(req, "request_id"), "no", false)
	h.result()
	if n := len(k.bash.ran()); n != 3 {
		t.Fatalf("ran %d", n)
	}
}

func TestSetModeRule(t *testing.T) {
	fake := newFake(
		callTool("t1", "Write", `{"file_path":"a.txt","content":"1"}`),
		callTool("t2", "Edit", `{"file_path":"a.txt","old_string":"1","new_string":"2"}`),
		callTool("t3", "Write", `{"file_path":"/etc/x","content":"1"}`),
		say("done"),
	)
	k := newKit()
	h := startAgent(t, newConfig(t, fake, k))
	h.initialize()
	h.say("write")
	req, _ := h.request()
	sugg := field(req, "request", "permission_suggestions")
	if str(sugg, "0", "type") != "setMode" || str(sugg, "0", "mode") != "acceptEdits" {
		t.Fatalf("suggestions: %v", sugg)
	}
	h.allow("nc_1", map[string]any{"file_path": "a.txt", "content": "1"}, sugg)
	req, before := h.request()
	if ini, _ := firstOf(before, is("system", "init")); ini == nil || ini["permissionMode"] != "acceptEdits" {
		t.Fatalf("no init with acceptEdits in %v", before)
	}
	if len(k.edit.ran()) != 1 {
		t.Fatal("the edit inside did not run without asking")
	}
	if str(req, "request", "tool_use_id") != "t3" || str(req, "request", "blocked_path") != "/etc/x" {
		t.Fatalf("outside: %v", req)
	}
	h.deny(str(req, "request_id"), "", false)
	h.result()
	if got := texts(lastMessage(fake.request(t, 1))); len(got) != 1 || !strings.Contains(got[0], "switched the permission mode to acceptEdits") {
		t.Fatalf("no reminder of the new mode: %q", got)
	}
}

func TestAskUserQuestion(t *testing.T) {
	q := `{"questions":[{"question":"Which database?","header":"DB","options":[{"label":"Postgres","description":"big"},{"label":"SQLite","description":"small"}],"multiSelect":false},` +
		`{"question":"Which parts?","header":"Parts","options":[{"label":"API","description":""},{"label":"UI","description":""}],"multiSelect":true}]}`
	fake := newFake(callTool("t1", "AskUserQuestion", q), callTool("t2", "AskUserQuestion", q), callTool("t3", "AskUserQuestion", `{"questions":[]}`), say("ok"))
	cfg := newConfig(t, fake, newKit())
	cfg.mode = modeBypass // asks in every mode
	h := startAgent(t, cfg)
	h.initialize()
	h.say("ask me")
	req, _ := h.request()
	if str(req, "request", "tool_name") != "AskUserQuestion" || str(req, "request", "input", "questions", "1", "question") != "Which parts?" {
		t.Fatalf("request: %v", req)
	}
	var in map[string]any
	_ = json.Unmarshal([]byte(q), &in)
	in["answers"] = map[string]any{"Which database?": "Postgres", "Which parts?": "API, UI"}
	in["response"] = "Keep it simple"
	h.allow(str(req, "request_id"), in, nil)
	req, before := h.request()
	want := "The owner answered: Which database? → Postgres; Which parts? → API, UI\nThe owner's own words: Keep it simple"
	if rs := toolResults(before); len(rs) != 1 || rs[0]["content"] != want || rs[0]["is_error"] != false {
		t.Fatalf("answers: %v", rs)
	}
	h.deny(str(req, "request_id"), "skip it", false)
	_, before = h.result()
	rs := toolResults(before)
	if len(rs) != 2 || rs[0]["content"] != "The owner did not answer: skip it" || rs[0]["is_error"] != true ||
		!strings.Contains(rs[1]["content"].(string), "at least one question") {
		t.Fatalf("results: %v", rs)
	}
}

func TestAnswersText(t *testing.T) {
	asked := json.RawMessage(`{"questions":[{"question":"A?"},{"question":"B?"}]}`)
	if got := answersText(asked, json.RawMessage(`{"answers":{"B?":"y","A?":"x","C?":["p","q"]}}`)); got != "The owner answered: A? → x; B? → y; C? → p, q" {
		t.Errorf("got %q", got)
	}
	if got := answersText(asked, json.RawMessage(`{"response":"none fit"}`)); got != "The owner's own words: none fit" {
		t.Errorf("got %q", got)
	}
	if got := answersText(asked, nil); got != "The owner answered without choosing an option." {
		t.Errorf("got %q", got)
	}
}

// firstOf is the first line that match takes.
func firstOf(lines []map[string]any, match func(map[string]any) bool) (map[string]any, bool) {
	for _, l := range lines {
		if match(l) {
			return l, true
		}
	}
	return nil, false
}
