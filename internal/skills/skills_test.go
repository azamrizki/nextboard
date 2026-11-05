package skills_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"nextboard/internal/machine"
	"nextboard/internal/skills"
	"nextboard/internal/store"
)

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const goodSkill = "---\nname: commit-message\ndescription: Write commit messages. Use when committing.\nargument-hint: \"[scope]\"\nallowed-tools: Bash(git:*), Read\nmodel: haiku\n---\n\nLook at the diff and write a message.\n"

// fixtureCfg builds a config folder (personal skills, commands, a plugin)
// and a repo (project skills/commands) in temp folders.
func fixtureCfg(t *testing.T) (cfg, repo string) {
	cfg, repo = t.TempDir(), t.TempDir()
	write(t, filepath.Join(cfg, "skills", "commit-message", "SKILL.md"), goodSkill)
	write(t, filepath.Join(cfg, "skills", "commit-message", "reference.md"), "ref")
	write(t, filepath.Join(cfg, "skills", "Bad_Name", "SKILL.md"), "---\nname: other\ndescription: \"\"\nfoo: <x>\n---\n")
	write(t, filepath.Join(cfg, "commands", "deploy.md"), "---\ndescription: Deploy it\n---\nRun the deploy.\n")
	write(t, filepath.Join(repo, ".claude", "skills", "commit-message", "SKILL.md"), goodSkill)
	write(t, filepath.Join(repo, ".claude", "commands", "deploy.md"), "---\ndescription: Project deploy\n---\nx\n")
	write(t, filepath.Join(repo, ".claude", "commands", "review.md"), "Review the diff.\n")
	plug := filepath.Join(cfg, "plugins", "cache", "mkt", "tools", "1.0.0")
	write(t, filepath.Join(plug, "skills", "lint-all", "SKILL.md"), "---\nname: lint-all\ndescription: Lint everything\n---\nbody\n")
	write(t, filepath.Join(plug, "commands", "fmt.md"), "---\ndescription: Format\n---\nfmt\n")
	inst, _ := json.Marshal(map[string]any{"version": 2, "plugins": map[string]any{"tools@mkt": []any{map[string]any{"installPath": plug}}}})
	write(t, filepath.Join(cfg, "plugins", "installed_plugins.json"), string(inst))
	return cfg, repo
}

func TestListShadowingAndLint(t *testing.T) {
	cfg, repo := fixtureCfg(t)
	st, _ := store.Open(t.TempDir())
	m := skills.New(st)
	h := machine.NewLocal(machine.Config{})
	loc := skills.Loc{MachineID: "local", Cfg: cfg, Repo: repo}
	items, truncated, err := m.List(context.Background(), h, loc)
	if err != nil || truncated {
		t.Fatal(err)
	}
	by := map[string]skills.Skill{}
	for _, s := range items {
		by[s.ID] = s
	}
	if len(items) != 8 {
		ids := []string{}
		for _, s := range items {
			ids = append(ids, s.ID)
		}
		t.Fatalf("items %v", ids)
	}
	cm := by["personal:commit-message"]
	if cm.Invoke != "/commit-message" || cm.ArgumentHint != "[scope]" || cm.Model != "haiku" || !cm.ModelInvocable ||
		strings.Join(cm.AllowedTools, "|") != "Bash(git:*)|Read" || strings.Join(cm.Siblings, ",") != "reference.md" ||
		cm.ShadowedBy != "" || len(cm.Lint) != 0 || cm.TokensEstimate == 0 {
		t.Fatalf("personal %+v", cm)
	}
	if by["project:commit-message"].ShadowedBy != "personal:commit-message" {
		t.Fatal("personal must shadow project")
	}
	if by["personalCommand:deploy"].ShadowedBy != "" || by["projectCommand:deploy"].ShadowedBy != "personalCommand:deploy" {
		t.Fatalf("command shadowing %+v", by["projectCommand:deploy"])
	}
	if p := by["plugin:tools:lint-all"]; p.Invoke != "/tools:lint-all" || p.Scope != "plugin" || p.Plugin != "tools" {
		t.Fatalf("plugin %+v", p)
	}
	if by["plugin:tools:fmt"].Kind != "command" {
		t.Fatal("plugin command")
	}
	rules := map[string]bool{}
	for _, f := range by["personal:Bad_Name"].Lint {
		rules[f.Rule] = true
	}
	for _, r := range []string{"name-format", "name-folder", "description-missing", "unknown-key", "angle-brackets", "body-empty"} {
		if !rules[r] {
			t.Fatalf("lint %v lacks %s", by["personal:Bad_Name"].Lint, r)
		}
	}
	// A skill beats a command of the same name.
	write(t, filepath.Join(cfg, "commands", "commit-message.md"), "x\n")
	items, _, _ = m.List(context.Background(), h, loc)
	for _, s := range items {
		if s.ID == "personalCommand:commit-message" && s.ShadowedBy != "personal:commit-message" {
			t.Fatalf("skill must shadow command: %+v", s)
		}
	}
	// body over 500 lines
	long := "---\nname: x\ndescription: y\n---\n" + strings.Repeat("line\n", 501)
	if l := skills.Lint(long, "skill", "x"); len(l) != 1 || l[0].Rule != "body-length" {
		t.Fatalf("long %v", l)
	}
	if l := skills.Lint(goodSkill, "skill", "commit-message"); len(l) != 0 {
		t.Fatalf("clean skill %v", l)
	}
}

func TestFrontmatterForms(t *testing.T) {
	fm := skills.ParseFrontmatter("---\nname: a\ndescription: >\n  folded\n  text\nallowed-tools:\n  - Read\n  - \"Bash(ls:*)\"\nmetadata:\n  k: v\nlist: [x, 'y']\n---\nbody")
	if fm.Values["description"] != "folded text" || strings.Join(fm.Lists["allowed-tools"], ",") != "Read,Bash(ls:*)" ||
		strings.Join(fm.Lists["list"], ",") != "x,y" || fm.Body != "body" || strings.Join(fm.Keys, ",") != "name,description,allowed-tools,metadata,list" {
		t.Fatalf("%+v", fm)
	}
	if r := skills.Render("skill", "n", "Use it: carefully", "[x]", "Body"); !strings.Contains(r, `description: "Use it: carefully"`) {
		t.Fatal(r)
	}
}

// exerciseWrites runs create/edit/duplicate/import/delete on h. remote
// machines must go through the 428 protocol for the config folder.
func ExerciseWrites(t *testing.T, h machine.Host, loc skills.Loc, st *store.Store) {
	ctx := context.Background()
	m := skills.New(st)
	m.Now = func() time.Time { return time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC) }
	confirm := func(err error, want ...string) []string {
		t.Helper()
		var ce *skills.ConfirmError
		if !loc.Remote {
			if err != nil {
				t.Fatalf("local write: %v", err)
			}
			return nil
		}
		if !errors.As(err, &ce) {
			t.Fatalf("want 428, got %v", err)
		}
		got := append([]string(nil), ce.Paths...)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("confirm paths\n got %v\nwant %v", got, want)
		}
		return ce.Paths
	}
	// create a personal skill: folder + file (skills/ exists already)
	req := skills.CreateRequest{Scope: "personal", Kind: "skill", Name: "new-skill", Description: "Does things", ArgumentHint: "[x]", Body: "Do it."}
	_, err := m.Create(ctx, h, loc, req, nil)
	dir := loc.Cfg + "/skills/new-skill"
	paths := confirm(err, dir, dir+"/SKILL.md")
	if loc.Remote {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("written without confirmation")
		}
		// one path different → still 428
		bad := append([]string{loc.Cfg + "/skills/other"}, paths[1:]...)
		if _, err := m.Create(ctx, h, loc, req, bad); err == nil {
			t.Fatal("wrong confirm accepted")
		}
		// a missing path → 428
		if _, err := m.Create(ctx, h, loc, req, paths[:1]); err == nil {
			t.Fatal("partial confirm accepted")
		}
		if _, err := m.Create(ctx, h, loc, req, paths); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(dir + "/SKILL.md")
	if err != nil || !strings.Contains(string(b), "name: new-skill") || !strings.Contains(string(b), "argument-hint: \"[x]\"") {
		t.Fatalf("created %q %v", b, err)
	}
	// create again → exists
	if _, err := m.Create(ctx, h, loc, req, paths); !errors.Is(err, skills.ErrExists) {
		t.Fatalf("exists: %v", err)
	}
	// bad name
	if _, err := m.Create(ctx, h, loc, skills.CreateRequest{Scope: "personal", Name: "Bad Name", Description: "x", Body: "y"}, nil); !errors.Is(err, skills.ErrBadName) {
		t.Fatalf("bad name %v", err)
	}
	// a personal command where commands/ does not exist yet: folder + file
	creq := skills.CreateRequest{Scope: "personal", Kind: "command", Name: "ship", Description: "Ship", Body: "ship it"}
	_, err = m.Create(ctx, h, loc, creq, nil)
	if p := confirm(err, loc.Cfg+"/commands", loc.Cfg+"/commands/ship.md"); loc.Remote {
		if _, err := m.Create(ctx, h, loc, creq, p); err != nil {
			t.Fatal(err)
		}
	}
	// project writes need no confirmation, also on SSH (the repo is the
	// user's code); .claude/skills is created as needed
	ps, err := m.Create(ctx, h, loc, skills.CreateRequest{Scope: "project", Name: "proj-skill", Description: "P", Body: "b"}, nil)
	if err != nil || ps.Scope != "project" || ps.Path != loc.Repo+"/.claude/skills/proj-skill/SKILL.md" {
		t.Fatalf("project create %+v %v", ps, err)
	}
	// edit with stale mtime → mismatch; then confirmed edit with backup
	file := dir + "/SKILL.md"
	stale := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = m.Write(ctx, h, loc, file, "---\nname: new-skill\ndescription: v2\n---\nbody v2\n", &stale, []string{file})
	if !errors.Is(err, machine.ErrMtimeMismatch) && !loc.Remote {
		t.Fatalf("stale: %v", err)
	}
	_, err = m.Write(ctx, h, loc, file, "---\nname: new-skill\ndescription: v2\n---\nbody v2\n", nil, nil)
	p := confirm(err, file)
	if loc.Remote {
		if _, err := m.Write(ctx, h, loc, file, "---\nname: new-skill\ndescription: v2\n---\nbody v2\n", nil, p); err != nil {
			t.Fatal(err)
		}
	}
	bk := st.Path("backups", "skills", loc.MachineID, "20261028-140000.000", "skills", "new-skill", "SKILL.md")
	if b, err := os.ReadFile(bk); err != nil || !strings.Contains(string(b), "Does things") {
		t.Fatalf("backup before overwrite %q %v", b, err)
	}
	// plugin skills are read-only
	plugFile := loc.Cfg + "/plugins/cache/mkt/tools/1.0.0/skills/lint-all/SKILL.md"
	if _, err := m.Write(ctx, h, loc, plugFile, "x", nil, []string{plugFile}); !errors.Is(err, skills.ErrReadOnly) {
		t.Fatalf("plugin write: %v", err)
	}
	if s, content, err := m.Item(ctx, h, loc, plugFile); err != nil || s.Scope != "plugin" || !strings.Contains(content, "Lint everything") {
		t.Fatalf("plugin item %+v %v", s, err)
	}
	// duplicate personal → project (no confirm: target is the repo)
	if d, err := m.Duplicate(ctx, h, loc, file, "project", nil); err != nil || d.ID != "project:new-skill" {
		t.Fatalf("duplicate %+v %v", d, err)
	}
	// duplicate project → personal needs confirm on SSH
	_, err = m.Duplicate(ctx, h, loc, loc.Repo+"/.claude/skills/proj-skill/SKILL.md", "personal", nil)
	pd := loc.Cfg + "/skills/proj-skill"
	if p := confirm(err, pd, pd+"/SKILL.md"); loc.Remote {
		if _, err := m.Duplicate(ctx, h, loc, loc.Repo+"/.claude/skills/proj-skill/SKILL.md", "personal", p); err != nil {
			t.Fatal(err)
		}
	}
	// import a folder from This PC (any machine) into personal
	src := t.TempDir() + "/imported-skill"
	write(t, src+"/SKILL.md", "---\nname: imported-skill\ndescription: I\n---\nbody\n")
	write(t, src+"/scripts/run.sh", "echo hi\n")
	local := machine.NewLocal(machine.Config{})
	_, err = m.Import(ctx, local, src, h, loc, "personal", nil)
	id := loc.Cfg + "/skills/imported-skill"
	if p := confirm(err, id, id+"/scripts", id+"/SKILL.md", id+"/scripts/run.sh"); loc.Remote {
		if _, err := m.Import(ctx, local, src, h, loc, "personal", p); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := os.ReadFile(id + "/scripts/run.sh"); err != nil || string(b) != "echo hi\n" {
		t.Fatalf("import %q %v", b, err)
	}
	// import refuses a folder without SKILL.md and > 50 files
	empty := t.TempDir()
	if _, err := m.Import(ctx, local, empty, h, loc, "project", nil); !errors.Is(err, skills.ErrNoSkillFile) {
		t.Fatalf("no SKILL.md: %v", err)
	}
	big := t.TempDir() + "/big-skill"
	write(t, big+"/SKILL.md", "---\nname: big-skill\ndescription: b\n---\nx\n")
	for i := 0; i < 50; i++ {
		write(t, big+"/f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt", "x")
	}
	if _, err := m.Import(ctx, local, big, h, loc, "project", nil); !errors.Is(err, skills.ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	// delete: backup on the hub first, then removed
	m.Now = func() time.Time { return time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC) }
	_, err = m.Delete(ctx, h, loc, id+"/SKILL.md", nil)
	p = confirm(err, id, id+"/scripts", id+"/SKILL.md", id+"/scripts/run.sh")
	if loc.Remote {
		if _, err := os.Stat(id); err != nil {
			t.Fatal("deleted without confirmation")
		}
	}
	backup := ""
	if loc.Remote {
		backup, err = m.Delete(ctx, h, loc, id+"/SKILL.md", p)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		backup = st.Path("backups", "skills", loc.MachineID, "20261028-150000.000")
	}
	if _, err := os.Stat(id); !os.IsNotExist(err) {
		t.Fatal("skill folder still there")
	}
	if b, err := os.ReadFile(filepath.Join(backup, "skills", "imported-skill", "scripts", "run.sh")); err != nil || string(b) != "echo hi\n" {
		t.Fatalf("backup %s: %q %v", backup, b, err)
	}
	// delete a command
	cmdFile := loc.Cfg + "/commands/ship.md"
	_, err = m.Delete(ctx, h, loc, cmdFile, nil)
	if p := confirm(err, cmdFile); loc.Remote {
		if _, err := m.Delete(ctx, h, loc, cmdFile, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(cmdFile); !os.IsNotExist(err) {
		t.Fatal("command still there")
	}
	// paths outside the managed folders are refused
	if _, err := m.Write(ctx, h, loc, loc.Cfg+"/settings.json", "{}", nil, []string{loc.Cfg + "/settings.json"}); !errors.Is(err, skills.ErrNotSkill) {
		t.Fatalf("settings.json: %v", err)
	}
	if _, err := m.Delete(ctx, h, loc, loc.Cfg+"/skills", []string{loc.Cfg + "/skills"}); !errors.Is(err, skills.ErrNotSkill) {
		t.Fatalf("skills root: %v", err)
	}
}

func TestWritesLocal(t *testing.T) {
	cfg, repo := fixtureCfg(t)
	st, _ := store.Open(t.TempDir())
	ExerciseWrites(t, machine.NewLocal(machine.Config{}), skills.Loc{MachineID: "local", Cfg: cfg, Repo: repo}, st)
}
