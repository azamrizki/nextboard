// Package skills is the Skills manager (docs/API-studio.md §9, E6): it
// lists, reads, lints and writes Claude Code skills and slash commands of a
// machine — personal (<cfg>/skills, <cfg>/commands), project
// (<repo>/.claude/…) and read-only plugin ones. Owner decision #3: on SSH
// machines a write or delete under <cfg> happens only on an explicit user
// action that echoes the exact set of paths the hub computed (HTTP 428
// protocol), and the previous version is always copied to the hub first
// (<data>/backups/skills/<machineId>/<ts>/).
package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/store"
)

// Scopes and kinds.
const (
	ScopePersonal        = "personal"
	ScopeProject         = "project"
	ScopePersonalCommand = "personalCommand"
	ScopeProjectCommand  = "projectCommand"
	ScopePlugin          = "plugin"

	KindSkill   = "skill"
	KindCommand = "command"
)

// Limits (§15).
const (
	MaxItems       = 400
	MaxImportFiles = 50
	MaxImportBytes = 5 << 20
	MaxContent     = 512 << 10
	maxSiblings    = 50
	maxDeleteFiles = 200
)

var (
	ErrNotSkill    = errors.New("the path is not a managed skill or command")
	ErrReadOnly    = errors.New("plugin skills are read-only")
	ErrBadName     = errors.New("the name must be lowercase letters, digits and hyphens (1–64)")
	ErrBadScope    = errors.New("unknown scope or no project chosen")
	ErrExists      = errors.New("a skill or command with that name already exists")
	ErrTooLarge    = errors.New("the skill folder is too large (at most 50 files / 5 MB)")
	ErrNoSkillFile = errors.New("the folder has no SKILL.md")
	ErrContent     = errors.New("the content is too large")
)

// ConfirmError is HTTP 428: the request must carry confirm.paths equal to
// Paths.
type ConfirmError struct {
	Machine string
	Paths   []string
}

func (e *ConfirmError) Error() string { return "confirmation required" }

// Skill is the API view (§9.1).
type Skill struct {
	ID             string    `json:"id"`
	Scope          string    `json:"scope"`
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	Plugin         string    `json:"plugin,omitempty"`
	Path           string    `json:"path"`
	Description    string    `json:"description"`
	Invoke         string    `json:"invoke"`
	ArgumentHint   string    `json:"argumentHint,omitempty"`
	ModelInvocable bool      `json:"modelInvocable"`
	Subagent       bool      `json:"subagent"`
	Model          string    `json:"model,omitempty"`
	AllowedTools   []string  `json:"allowedTools,omitempty"`
	TokensEstimate int       `json:"tokensEstimate"`
	Siblings       []string  `json:"siblings"`
	ShadowedBy     string    `json:"shadowedBy,omitempty"`
	Mtime          time.Time `json:"mtime"`
	Lint           []Finding `json:"lint"`
}

// Loc says where a machine keeps skills: Cfg is the profile folder or
// ~/.claude (absolute), Repo the project folder ("" = no project). Remote
// machines need the confirm protocol for Cfg.
type Loc struct {
	MachineID   string
	MachineName string
	Remote      bool
	Cfg         string
	Repo        string
}

func (l Loc) base(scope string) (string, error) {
	switch scope {
	case ScopePersonal, ScopePersonalCommand:
		return l.Cfg, nil
	case ScopeProject, ScopeProjectCommand:
		if l.Repo == "" {
			return "", ErrBadScope
		}
		return path.Join(l.Repo, ".claude"), nil
	}
	return "", ErrBadScope
}

func kindOf(scope string) string {
	if scope == ScopePersonalCommand || scope == ScopeProjectCommand {
		return KindCommand
	}
	return KindSkill
}

// Target is a classified path.
type Target struct {
	Scope, Kind, Name, Plugin string
	File                      string // SKILL.md or the command file
	Dir                       string // skill folder ("" for commands)
}

// Classify maps an absolute path to its skill or command.
func (l Loc) Classify(p string) (Target, error) {
	p = path.Clean(p)
	if strings.Contains(p, "\x00") || !strings.HasPrefix(p, "/") {
		return Target{}, ErrNotSkill
	}
	check := func(base, scope string) (Target, bool) {
		if base == "" {
			return Target{}, false
		}
		sk := path.Join(base, "skills") + "/"
		cm := path.Join(base, "commands") + "/"
		if rest, ok := strings.CutPrefix(p, sk); ok {
			parts := strings.Split(rest, "/")
			if len(parts) == 2 && parts[1] == "SKILL.md" && parts[0] != "" {
				return Target{Scope: scope, Kind: KindSkill, Name: parts[0], File: p, Dir: path.Join(base, "skills", parts[0])}, true
			}
			if len(parts) == 1 && parts[0] != "" { // the skill folder itself
				return Target{Scope: scope, Kind: KindSkill, Name: parts[0], File: path.Join(p, "SKILL.md"), Dir: p}, true
			}
		}
		if rest, ok := strings.CutPrefix(p, cm); ok && !strings.Contains(rest, "/") && strings.HasSuffix(rest, ".md") {
			sc := ScopePersonalCommand
			if scope == ScopeProject {
				sc = ScopeProjectCommand
			}
			return Target{Scope: sc, Kind: KindCommand, Name: strings.TrimSuffix(rest, ".md"), File: p}, true
		}
		return Target{}, false
	}
	if t, ok := check(l.Cfg, ScopePersonal); ok {
		return t, nil
	}
	if l.Repo != "" {
		if t, ok := check(path.Join(l.Repo, ".claude"), ScopeProject); ok {
			return t, nil
		}
	}
	if l.Cfg != "" && strings.HasPrefix(p, path.Join(l.Cfg, "plugins")+"/") {
		return Target{Scope: ScopePlugin}, ErrReadOnly
	}
	return Target{}, ErrNotSkill
}

// confirmNeeded reports whether writing under scope needs the protocol.
func (l Loc) confirmNeeded(scope string) bool {
	return l.Remote && (scope == ScopePersonal || scope == ScopePersonalCommand)
}

// Confirm checks the confirm protocol: given must equal want as a set.
func Confirm(l Loc, scope string, want, given []string) error {
	if !l.confirmNeeded(scope) {
		return nil
	}
	w := append([]string(nil), want...)
	sort.Strings(w)
	g := append([]string(nil), given...)
	sort.Strings(g)
	if len(w) != len(g) {
		return &ConfirmError{Machine: l.MachineName, Paths: w}
	}
	for i := range w {
		if w[i] != path.Clean(g[i]) {
			return &ConfirmError{Machine: l.MachineName, Paths: w}
		}
	}
	return nil
}

// Manager keeps backups and talks to machines.
type Manager struct {
	st *store.Store
	// Now is injectable for tests.
	Now func() time.Time
}

func New(st *store.Store) *Manager { return &Manager{st: st, Now: time.Now} }

// ---- listing ----

func exists(ctx context.Context, h machine.Host, p string) (*machine.FileStat, bool) {
	st, err := h.Stat(ctx, p)
	if err != nil || !st.Exists {
		return st, false
	}
	return st, true
}

// List scans every scope of loc (≤ 400 items) and computes shadowing.
func (m *Manager) List(ctx context.Context, h machine.Host, l Loc) ([]Skill, bool, error) {
	var out []Skill
	truncated := false
	add := func(s Skill) bool {
		if len(out) >= MaxItems {
			truncated = true
			return false
		}
		out = append(out, s)
		return true
	}
	scan := func(base, skillScope, cmdScope, plugin string) {
		if base == "" {
			return
		}
		if st, ok := exists(ctx, h, path.Join(base, "skills")); ok && st.Dir {
			if ls, err := h.ListDir(ctx, path.Join(base, "skills")); err == nil {
				for _, e := range ls.Entries {
					if !e.Dir || strings.HasPrefix(e.Name, ".") {
						continue
					}
					file := path.Join(ls.Path, e.Name, "SKILL.md")
					s, err := m.read(ctx, h, Target{Scope: skillScope, Kind: KindSkill, Name: e.Name, Plugin: plugin, File: file, Dir: path.Join(ls.Path, e.Name)}, false)
					if err == nil && !add(s) {
						return
					}
				}
			}
		}
		if st, ok := exists(ctx, h, path.Join(base, "commands")); ok && st.Dir {
			if ls, err := h.ListDir(ctx, path.Join(base, "commands")); err == nil {
				for _, e := range ls.Entries {
					if e.Dir || !strings.HasSuffix(e.Name, ".md") || strings.HasPrefix(e.Name, ".") {
						continue
					}
					t := Target{Scope: cmdScope, Kind: KindCommand, Name: strings.TrimSuffix(e.Name, ".md"), Plugin: plugin, File: path.Join(ls.Path, e.Name)}
					s, err := m.read(ctx, h, t, false)
					if err == nil && !add(s) {
						return
					}
				}
			}
		}
	}
	scan(l.Cfg, ScopePersonal, ScopePersonalCommand, "")
	if l.Repo != "" {
		scan(path.Join(l.Repo, ".claude"), ScopeProject, ScopeProjectCommand, "")
	}
	for _, p := range plugins(ctx, h, l.Cfg) {
		scan(p.dir, ScopePlugin, ScopePlugin, p.name)
	}
	shadow(out)
	if out == nil {
		out = []Skill{}
	}
	return out, truncated, nil
}

type pluginDir struct{ name, dir string }

// plugins reads <cfg>/plugins/installed_plugins.json (Claude Code 2.x).
func plugins(ctx context.Context, h machine.Host, cfg string) []pluginDir {
	if cfg == "" {
		return nil
	}
	fc, err := h.ReadFile(ctx, path.Join(cfg, "plugins", "installed_plugins.json"), 1<<20)
	if err != nil || fc.Binary {
		return nil
	}
	var doc struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if json.Unmarshal([]byte(fc.Content), &doc) != nil {
		return nil
	}
	var out []pluginDir
	for key, list := range doc.Plugins {
		name, _, _ := strings.Cut(key, "@")
		for _, p := range list {
			if strings.HasPrefix(p.InstallPath, "/") {
				out = append(out, pluginDir{name: name, dir: path.Clean(p.InstallPath)})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// rank orders same-named entries: skills beat commands, personal beats
// project (§9.1). Plugin entries are namespaced and never shadow.
func rank(s Skill) int {
	switch s.Scope {
	case ScopePersonal:
		return 0
	case ScopeProject:
		return 1
	case ScopePersonalCommand:
		return 2
	case ScopeProjectCommand:
		return 3
	}
	return 9
}

func shadow(list []Skill) {
	best := map[string]int{}
	for i, s := range list {
		if s.Scope == ScopePlugin {
			continue
		}
		if j, ok := best[s.Name]; !ok || rank(s) < rank(list[j]) {
			best[s.Name] = i
		}
	}
	for i, s := range list {
		if s.Scope == ScopePlugin {
			continue
		}
		if j := best[s.Name]; j != i {
			list[i].ShadowedBy = list[j].ID
		}
	}
}

// read builds the Skill of t (lint is local and cheap, always filled).
func (m *Manager) read(ctx context.Context, h machine.Host, t Target, _ bool) (Skill, error) {
	s, _, err := m.readContent(ctx, h, t)
	return s, err
}

func (m *Manager) readContent(ctx context.Context, h machine.Host, t Target) (Skill, string, error) {
	fc, err := h.ReadFile(ctx, t.File, MaxContent)
	if err != nil {
		return Skill{}, "", err
	}
	if fc.Binary {
		return Skill{}, "", ErrNotSkill
	}
	fm := ParseFrontmatter(fc.Content)
	s := Skill{Scope: t.Scope, Kind: t.Kind, Name: t.Name, Plugin: t.Plugin, Path: fc.Path, Mtime: fc.Mtime,
		Description: fm.Values["description"], ArgumentHint: fm.Values["argument-hint"], Model: fm.Values["model"],
		Siblings: []string{}, TokensEstimate: (utf8.RuneCountInString(fc.Content) + 3) / 4}
	s.ID = t.Scope + ":" + s.Name
	s.Invoke = "/" + s.Name
	if t.Plugin != "" {
		s.ID = "plugin:" + t.Plugin + ":" + s.Name
		s.Invoke = "/" + t.Plugin + ":" + s.Name
	}
	s.ModelInvocable = !strings.EqualFold(fm.Values["disable-model-invocation"], "true")
	s.Subagent = strings.EqualFold(fm.Values["context"], "fork") || fm.Values["agent"] != ""
	if list, ok := fm.Lists["allowed-tools"]; ok {
		s.AllowedTools = list
	} else if v := fm.Values["allowed-tools"]; v != "" {
		s.AllowedTools = splitTools(v)
	}
	s.Lint = Lint(fc.Content, t.Kind, t.Name)
	if t.Dir != "" {
		if ls, err := h.ListDir(ctx, t.Dir); err == nil {
			for _, e := range ls.Entries {
				if e.Name == "SKILL.md" || len(s.Siblings) >= maxSiblings {
					continue
				}
				n := e.Name
				if e.Dir {
					n += "/"
				}
				s.Siblings = append(s.Siblings, n)
			}
		}
	}
	return s, fc.Content, nil
}

// splitTools splits "Bash(git:*), Read" or "Read Grep" (keeping
// parenthesised patterns whole).
func splitTools(v string) []string {
	var out []string
	depth := 0
	cur := strings.Builder{}
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for _, r := range v {
		switch {
		case r == '(':
			depth++
		case r == ')':
			depth--
		case (r == ',' || r == ' ') && depth == 0:
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

// Item returns one skill with its content.
func (m *Manager) Item(ctx context.Context, h machine.Host, l Loc, p string) (Skill, string, error) {
	t, err := l.Classify(p)
	if errors.Is(err, ErrReadOnly) {
		return m.pluginItem(ctx, h, l, p)
	}
	if err != nil {
		return Skill{}, "", err
	}
	return m.readContent(ctx, h, t)
}

// pluginItem reads a plugin skill/command (read-only) by its path.
func (m *Manager) pluginItem(ctx context.Context, h machine.Host, l Loc, p string) (Skill, string, error) {
	for _, pl := range plugins(ctx, h, l.Cfg) {
		rest, ok := strings.CutPrefix(path.Clean(p), pl.dir+"/")
		if !ok {
			continue
		}
		parts := strings.Split(rest, "/")
		switch {
		case len(parts) == 3 && parts[0] == "skills" && parts[2] == "SKILL.md":
			return m.readContent(ctx, h, Target{Scope: ScopePlugin, Kind: KindSkill, Name: parts[1], Plugin: pl.name, File: path.Clean(p), Dir: path.Join(pl.dir, "skills", parts[1])})
		case len(parts) == 2 && parts[0] == "commands" && strings.HasSuffix(parts[1], ".md"):
			return m.readContent(ctx, h, Target{Scope: ScopePlugin, Kind: KindCommand, Name: strings.TrimSuffix(parts[1], ".md"), Plugin: pl.name, File: path.Clean(p)})
		}
	}
	return Skill{}, "", ErrNotSkill
}

// ---- writes ----

// backup copies the given files (absolute, under base) of machine
// machineID into <data>/backups/skills/<machineId>/<ts>/<rel> and returns
// that folder.
func (m *Manager) backup(ctx context.Context, h machine.Host, machineID, base string, files []string) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	ts := m.Now().UTC().Format("20060102-150405.000")
	dir := m.st.Path("backups", "skills", safeID(machineID), ts)
	for _, f := range files {
		rc, st, err := h.OpenRaw(ctx, f)
		if err != nil {
			return "", fmt.Errorf("backup %s: %w", f, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, MaxImportBytes+1))
		rc.Close()
		if err != nil {
			return "", err
		}
		_ = st
		rel := strings.TrimPrefix(f, strings.TrimSuffix(base, "/")+"/")
		if rel == f {
			rel = path.Base(f)
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return "", err
		}
		if err := store.WriteFileAtomic(dst, data); err != nil {
			return "", err
		}
	}
	return dir, nil
}

var safeRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func safeID(id string) string { return safeRe.ReplaceAllString(id, "_") }

// Write replaces the content of an existing skill or command (or creates
// a command file) after the confirm check and a hub backup.
func (m *Manager) Write(ctx context.Context, h machine.Host, l Loc, p, content string, ifMtime *time.Time, confirm []string) (Skill, error) {
	t, err := l.Classify(p)
	if err != nil {
		return Skill{}, err
	}
	if len(content) > MaxContent {
		return Skill{}, ErrContent
	}
	if err := Confirm(l, t.Scope, []string{t.File}, confirm); err != nil {
		return Skill{}, err
	}
	if st, ok := exists(ctx, h, t.File); ok && !st.Dir {
		base, _ := l.base(t.Scope)
		if _, err := m.backup(ctx, h, l.MachineID, base, []string{t.File}); err != nil {
			return Skill{}, err
		}
	} else if ifMtime != nil {
		return Skill{}, machine.ErrMtimeMismatch
	}
	if _, err := h.WriteFile(ctx, t.File, []byte(content), machine.WriteOptions{IfMtime: ifMtime}); err != nil {
		return Skill{}, err
	}
	s, _, err := m.readContent(ctx, h, t)
	return s, err
}

// CreateRequest is POST /api/machines/{id}/skills.
type CreateRequest struct {
	Scope        string `json:"scope"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argumentHint"`
	Body         string `json:"body"`
}

// yamlScalar quotes a frontmatter value when needed.
func yamlScalar(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if s == "" || strings.ContainsAny(s, ":#'\"[]{},&*!|>%@`") || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "?") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return s
}

// Render builds the file content of a new skill or command.
func Render(kind, name, desc, hint, body string) string {
	var b strings.Builder
	b.WriteString("---\n")
	if kind == KindSkill {
		b.WriteString("name: " + name + "\n")
	}
	b.WriteString("description: " + yamlScalar(desc) + "\n")
	if strings.TrimSpace(hint) != "" {
		b.WriteString("argument-hint: " + yamlScalar(hint) + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(body) + "\n")
	return b.String()
}

// plan is the set of folders to create and files to write of a write.
type plan struct {
	dirs  []string          // new folders, parents first
	files map[string][]byte // absolute path → content
	order []string          // files in write order
}

func (p *plan) paths() []string {
	out := append([]string(nil), p.dirs...)
	out = append(out, p.order...)
	return out
}

// missingDirs lists the folders of dir (below stop) that do not exist,
// parents first.
func missingDirs(ctx context.Context, h machine.Host, stop, dir string) ([]string, error) {
	var miss []string
	for d := path.Clean(dir); d != "/" && d != "." && (d == stop || strings.HasPrefix(d, stop+"/")); d = path.Dir(d) {
		st, err := h.Stat(ctx, d)
		if err != nil {
			return nil, err
		}
		if st.Exists {
			if !st.Dir {
				return nil, fmt.Errorf("%s is not a folder", d)
			}
			break
		}
		miss = append([]string{d}, miss...)
	}
	return miss, nil
}

// apply executes a plan.
func apply(ctx context.Context, h machine.Host, p *plan) error {
	for _, d := range p.dirs {
		if err := h.Mkdir(ctx, d); err != nil {
			return err
		}
	}
	for _, f := range p.order {
		if _, err := h.WriteFile(ctx, f, p.files[f], machine.WriteOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// newPlan plans writing files (relative to the new item's root).
func newPlan(ctx context.Context, h machine.Host, l Loc, scope, name string, files map[string][]byte) (*plan, string, error) {
	base, err := l.base(scope)
	if err != nil {
		return nil, "", err
	}
	if st, ok := exists(ctx, h, l.Cfg); (scope == ScopePersonal || scope == ScopePersonalCommand) && (!ok || !st.Dir) {
		return nil, "", fmt.Errorf("config folder %s does not exist", l.Cfg)
	}
	if st, ok := exists(ctx, h, l.Repo); (scope == ScopeProject || scope == ScopeProjectCommand) && (!ok || !st.Dir) {
		return nil, "", fmt.Errorf("project folder %s does not exist", l.Repo)
	}
	var root, mainFile string
	if kindOf(scope) == KindSkill {
		root = path.Join(base, "skills", name)
		mainFile = path.Join(root, "SKILL.md")
		if _, ok := exists(ctx, h, root); ok {
			return nil, "", ErrExists
		}
	} else {
		root = path.Join(base, "commands")
		mainFile = path.Join(root, name+".md")
		if _, ok := exists(ctx, h, mainFile); ok {
			return nil, "", ErrExists
		}
	}
	// Folders may be created below <cfg> (which must exist: profiles are
	// never created remotely) or below the repo (.claude may be new).
	stop := l.Cfg
	if scope == ScopeProject || scope == ScopeProjectCommand {
		stop = l.Repo
	}
	p := &plan{files: map[string][]byte{}}
	seen := map[string]bool{}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, rel := range keys {
		full := path.Join(root, rel)
		if kindOf(scope) == KindCommand {
			full = mainFile
		}
		if !strings.HasPrefix(full, root+"/") && full != mainFile {
			return nil, "", ErrNotSkill
		}
		dirs, err := missingDirs(ctx, h, stop, path.Dir(full))
		if err != nil {
			return nil, "", err
		}
		for _, d := range dirs {
			if d == stop {
				return nil, "", fmt.Errorf("folder %s does not exist", stop)
			}
			if !seen[d] {
				seen[d] = true
				p.dirs = append(p.dirs, d)
			}
		}
		p.files[full] = files[rel]
		p.order = append(p.order, full)
	}
	sort.SliceStable(p.dirs, func(i, j int) bool { return strings.Count(p.dirs[i], "/") < strings.Count(p.dirs[j], "/") })
	return p, mainFile, nil
}

// Create writes a new skill (folder + SKILL.md) or command.
func (m *Manager) Create(ctx context.Context, h machine.Host, l Loc, req CreateRequest, confirm []string) (Skill, error) {
	if req.Kind == "" {
		req.Kind = kindOf(req.Scope)
	}
	switch req.Scope {
	case ScopePersonal, ScopeProject, ScopePersonalCommand, ScopeProjectCommand:
	default:
		return Skill{}, ErrBadScope
	}
	if req.Kind != kindOf(req.Scope) {
		// scope "personal" + kind "command" → personalCommand
		switch {
		case req.Kind == KindCommand && req.Scope == ScopePersonal:
			req.Scope = ScopePersonalCommand
		case req.Kind == KindCommand && req.Scope == ScopeProject:
			req.Scope = ScopeProjectCommand
		default:
			return Skill{}, ErrBadScope
		}
	}
	if !nameRe.MatchString(req.Name) || len(req.Name) > MaxNameLen {
		return Skill{}, ErrBadName
	}
	content := Render(req.Kind, req.Name, req.Description, req.ArgumentHint, req.Body)
	if len(content) > MaxContent {
		return Skill{}, ErrContent
	}
	rel := "SKILL.md"
	if req.Kind == KindCommand {
		rel = req.Name + ".md"
	}
	p, main, err := newPlan(ctx, h, l, req.Scope, req.Name, map[string][]byte{rel: []byte(content)})
	if err != nil {
		return Skill{}, err
	}
	if err := Confirm(l, req.Scope, p.paths(), confirm); err != nil {
		return Skill{}, err
	}
	if err := apply(ctx, h, p); err != nil {
		return Skill{}, err
	}
	t, err := l.Classify(main)
	if err != nil {
		return Skill{}, err
	}
	s, _, err := m.readContent(ctx, h, t)
	return s, err
}

// collect lists the files of a skill folder (recursively, bounded) with
// their relative paths.
func collect(ctx context.Context, h machine.Host, dir string, maxFiles int, maxBytes int64) (map[string]string, []string, error) {
	files := map[string]string{} // rel → absolute
	var dirs []string
	var total int64
	var walk func(d, rel string, depth int) error
	walk = func(d, rel string, depth int) error {
		if depth > 6 {
			return ErrTooLarge
		}
		ls, err := h.ListDir(ctx, d)
		if err != nil {
			return err
		}
		for _, e := range ls.Entries {
			full := path.Join(ls.Path, e.Name)
			r := path.Join(rel, e.Name)
			if e.Dir {
				dirs = append(dirs, full)
				if err := walk(full, r, depth+1); err != nil {
					return err
				}
				continue
			}
			files[r] = full
			total += e.Size
			if len(files) > maxFiles || total > maxBytes {
				return ErrTooLarge
			}
		}
		return nil
	}
	if err := walk(dir, "", 0); err != nil {
		return nil, nil, err
	}
	return files, dirs, nil
}

func readAll(ctx context.Context, h machine.Host, p string) ([]byte, error) {
	rc, _, err := h.OpenRaw(ctx, p)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, MaxImportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxImportBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

// Import copies a folder that contains SKILL.md (≤ 50 files, ≤ 5 MB) from
// any machine into scope on h.
func (m *Manager) Import(ctx context.Context, src machine.Host, fromPath string, h machine.Host, l Loc, scope string, confirm []string) (Skill, error) {
	if scope != ScopePersonal && scope != ScopeProject {
		return Skill{}, ErrBadScope
	}
	st, err := src.Stat(ctx, fromPath)
	if err != nil {
		return Skill{}, err
	}
	dir := st.Path
	if st.Exists && !st.Dir && path.Base(dir) == "SKILL.md" {
		dir = path.Dir(dir)
	} else if !st.Exists || !st.Dir {
		return Skill{}, ErrNoSkillFile
	}
	if s, ok := exists(ctx, src, path.Join(dir, "SKILL.md")); !ok || s.Dir {
		return Skill{}, ErrNoSkillFile
	}
	name := path.Base(dir)
	if !nameRe.MatchString(name) || len(name) > MaxNameLen {
		return Skill{}, ErrBadName
	}
	list, _, err := collect(ctx, src, dir, MaxImportFiles, MaxImportBytes)
	if err != nil {
		return Skill{}, err
	}
	files := map[string][]byte{}
	for rel, full := range list {
		b, err := readAll(ctx, src, full)
		if err != nil {
			return Skill{}, err
		}
		files[rel] = b
	}
	return m.writeNew(ctx, h, l, scope, name, files, confirm)
}

func (m *Manager) writeNew(ctx context.Context, h machine.Host, l Loc, scope, name string, files map[string][]byte, confirm []string) (Skill, error) {
	p, main, err := newPlan(ctx, h, l, scope, name, files)
	if err != nil {
		return Skill{}, err
	}
	if err := Confirm(l, scope, p.paths(), confirm); err != nil {
		return Skill{}, err
	}
	if err := apply(ctx, h, p); err != nil {
		return Skill{}, err
	}
	t, err := l.Classify(main)
	if err != nil {
		return Skill{}, err
	}
	s, _, err := m.readContent(ctx, h, t)
	return s, err
}

// Duplicate copies a skill or command to the other scope (personal ↔
// project) on the same machine.
func (m *Manager) Duplicate(ctx context.Context, h machine.Host, l Loc, p, toScope string, confirm []string) (Skill, error) {
	t, err := l.Classify(p)
	if errors.Is(err, ErrReadOnly) {
		// plugin skills may be copied out (reading is allowed)
		s, _, perr := m.pluginItem(ctx, h, l, p)
		if perr != nil {
			return Skill{}, perr
		}
		t = Target{Scope: ScopePlugin, Kind: s.Kind, Name: s.Name, File: s.Path}
		if s.Kind == KindSkill {
			t.Dir = path.Dir(s.Path)
		}
	} else if err != nil {
		return Skill{}, err
	}
	if t.Kind == KindCommand {
		switch toScope {
		case ScopePersonal:
			toScope = ScopePersonalCommand
		case ScopeProject:
			toScope = ScopeProjectCommand
		}
	}
	if toScope == t.Scope || kindOf(toScope) != t.Kind {
		return Skill{}, ErrBadScope
	}
	files := map[string][]byte{}
	if t.Kind == KindCommand {
		b, err := readAll(ctx, h, t.File)
		if err != nil {
			return Skill{}, err
		}
		files[t.Name+".md"] = b
	} else {
		list, _, err := collect(ctx, h, t.Dir, MaxImportFiles, MaxImportBytes)
		if err != nil {
			return Skill{}, err
		}
		for rel, full := range list {
			b, err := readAll(ctx, h, full)
			if err != nil {
				return Skill{}, err
			}
			files[rel] = b
		}
	}
	return m.writeNew(ctx, h, l, toScope, t.Name, files, confirm)
}

// Delete removes a skill folder (or command file) after copying it to the
// hub backup. A symlinked skill folder only loses the link.
func (m *Manager) Delete(ctx context.Context, h machine.Host, l Loc, p string, confirm []string) (string, error) {
	t, err := l.Classify(p)
	if err != nil {
		return "", err
	}
	base, _ := l.base(t.Scope)
	if t.Kind == KindCommand {
		if _, ok := exists(ctx, h, t.File); !ok {
			return "", os.ErrNotExist
		}
		if err := Confirm(l, t.Scope, []string{t.File}, confirm); err != nil {
			return "", err
		}
		b, err := m.backup(ctx, h, l.MachineID, base, []string{t.File})
		if err != nil {
			return "", err
		}
		return b, machine.Remove(ctx, h, t.File)
	}
	ls, link, err := machine.Lstat(ctx, h, t.Dir)
	if err != nil {
		return "", err
	}
	if !ls.Exists {
		return "", os.ErrNotExist
	}
	if link {
		if err := Confirm(l, t.Scope, []string{t.Dir}, confirm); err != nil {
			return "", err
		}
		var files []string
		if _, ok := exists(ctx, h, t.File); ok {
			files = []string{t.File}
		}
		b, err := m.backup(ctx, h, l.MachineID, base, files)
		if err != nil {
			return "", err
		}
		return b, machine.Remove(ctx, h, t.Dir)
	}
	list, dirs, err := collect(ctx, h, t.Dir, maxDeleteFiles, MaxImportBytes*4)
	if err != nil {
		return "", err
	}
	var files []string
	for _, f := range list {
		files = append(files, f)
	}
	sort.Strings(files)
	want := append(append([]string{t.Dir}, dirs...), files...)
	if err := Confirm(l, t.Scope, want, confirm); err != nil {
		return "", err
	}
	b, err := m.backup(ctx, h, l.MachineID, base, files)
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if err := machine.Remove(ctx, h, f); err != nil {
			return b, err
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range append(dirs, t.Dir) {
		if err := machine.Remove(ctx, h, d); err != nil {
			return b, err
		}
	}
	return b, nil
}
