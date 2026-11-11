// Package settings is the hub settings store (docs/API-agents.md §11):
// one JSON document with defaults filled in, changed through JSON Merge
// Patch (RFC 7386) where null resets a key to its default.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/azamrizki/nextboard/internal/store"
)

// File is the settings document (0600, atomic writes).
const File = "settings.json"

// Agents holds agent-related settings.
type Agents struct {
	Defaults map[string]string            `json:"defaults"` // machineId → agentId
	Paths    map[string]map[string]string `json:"paths"`    // machineId → agentId → command path
	Profiles map[string]map[string]string `json:"profiles"` // machineId → agentId → profileId
	// Disabled agents are installed but not wanted: no picker offers them.
	Disabled   map[string]bool   `json:"disabled"` // agentId → true
	RoleAgents map[string]string `json:"roleAgents"`
	// Models is the model and effort a new console of an agent starts with, by
	// agent id. RoleModels is the same by group role, for that role's own agent.
	Models      map[string]ModelChoice `json:"models"`
	RoleModels  map[string]ModelChoice `json:"roleModels"`
	YoloDefault bool                   `json:"yoloDefault"`
	LiveStatus  bool                   `json:"liveStatus"`
	// IDELink lets Claude consoles use the editor card as their IDE.
	IDELink         bool   `json:"ideLink"`
	HookChannel     string `json:"hookChannel"`     // auto | tty | http
	ResumeOnRestart string `json:"resumeOnRestart"` // auto | ask | never
	// RemoteTools lets an agent work on a server through hub tools (§21).
	RemoteTools bool `json:"remoteTools"`
	// CoAuthor lets agents sign their commits and pull requests (API-agents §26).
	CoAuthor bool `json:"coAuthor"`
}

// ModelChoice is a model and an effort ("" = the CLI's own choice).
type ModelChoice struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// ModelFor is what a new console of this agent starts with: the role's choice
// when the role uses this agent, then the agent's.
func (a Agents) ModelFor(role, agentID string) ModelChoice {
	c := a.Models[agentID]
	if role != "" && a.RoleAgents[role] == agentID {
		r := a.RoleModels[role]
		if r.Model != "" {
			c.Model = r.Model
		}
		if r.Effort != "" {
			c.Effort = r.Effort
		}
	}
	return c
}

// Notifications filters attention events at the hub.
type Notifications struct {
	NeedsYou       bool `json:"needsYou"`
	Finished       bool `json:"finished"`
	Error          bool `json:"error"`
	FinishedMinSec int  `json:"finishedMinSec"`
}

// Shortcuts are the one-key board actions.
type Shortcuts struct {
	Select   string `json:"select"`
	Hand     string `json:"hand"`
	Spawn    string `json:"spawn"`
	Pen      string `json:"pen"`
	Eraser   string `json:"eraser"`
	Git      string `json:"git"`
	Memory   string `json:"memory"`
	Skills   string `json:"skills"`
	Loops    string `json:"loops"`
	Markdown string `json:"markdown"`
	NeedsYou string `json:"needsYou"`
}

// Runner configures the hidden CLI runner (API-studio.md §12).
type Runner struct {
	DefaultAgent string `json:"defaultAgent"`
	// Provider runs the hidden runs (lead, memory, summaries, commit messages…) through this API
	// provider instead of the CLI's login (docs/API-agents.md §10.2); "" = the login.
	Provider string `json:"provider,omitempty"`
}

// Delivery configures the delivery engine (docs/API-orchestration.md §2).
// PromptTimeoutSec is a NextBoard addition: how long a delivery waits for
// a screen it positively recognises as the agent's input prompt (a dialog,
// menu or unknown screen) before it fails with prompt_unrecognized.
type Delivery struct {
	WhileWorking        string `json:"whileWorking"` // queue | type
	StartingTimeoutSec  int    `json:"startingTimeoutSec"`
	NeedsYouTimeoutSec  int    `json:"needsYouTimeoutSec"`
	WorkingTimeoutSec   int    `json:"workingTimeoutSec"`
	ReconnectTimeoutSec int    `json:"reconnectTimeoutSec"`
	SubmitDelayMs       int    `json:"submitDelayMs"`
	HumanTypingGuardSec int    `json:"humanTypingGuardSec"`
	PromptTimeoutSec    int    `json:"promptTimeoutSec"`
}

// Commands configures the command pipeline (§3).
type Commands struct {
	Planner          string   `json:"planner"` // auto | off
	ConfirmTTLSec    int      `json:"confirmTtlSec"`
	ReportTimeoutMin int      `json:"reportTimeoutMin"`
	RiskyPatterns    []string `json:"riskyPatterns"`
}

// Lead is the orchestrator as a person: the lead agent who talks with the owner
// and hands the work out (docs/API-orchestration.md §3.9).
type Lead struct {
	Name        string `json:"name"`
	Personality string `json:"personality"`
	TalkFirst   bool   `json:"talkFirst"` // talk and ask back; dangerous work waits for the owner
	// Runner is what the lead thinks with: "" = Claude Code like the other hidden runs (so through
	// runner.provider when one is set), "claude" or "codex" = that CLI on its login, ap_… = an API
	// provider, on the CLI that speaks its API (docs/API-agents.md §10.2).
	Runner string `json:"runner,omitempty"`
	// Model and Effort of the lead's runs; "" = the runner's own default.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Providers is the API provider the consoles the lead starts run on, by agent id; absent = the
	// agent's login. An order that names an account wins.
	Providers map[string]string `json:"providers,omitempty"`
}

// Memory configures project memory (§12).
type Memory struct {
	AutoPush bool `json:"autoPush"`
}

// OnSaveRule runs Command in the project folder when a saved file matches
// Pattern (docs/API-studio.md §7.2).
type OnSaveRule struct {
	Pattern string `json:"pattern"`
	Command string `json:"command"`
}

// Editor configures the Code workbench.
type Editor struct {
	OnSave []OnSaveRule `json:"onSave"`
}

// Settings is the whole document.
type Settings struct {
	SchemaVersion int           `json:"schemaVersion"`
	Agents        Agents        `json:"agents"`
	Notifications Notifications `json:"notifications"`
	Shortcuts     Shortcuts     `json:"shortcuts"`
	Runner        Runner        `json:"runner"`
	Delivery      Delivery      `json:"delivery"`
	Commands      Commands      `json:"commands"`
	Lead          Lead          `json:"lead"`
	Memory        Memory        `json:"memory"`
	Previews      Previews      `json:"previews"`
	Git           Git           `json:"git"`
	Usage         Usage         `json:"usage"`
	Editor        Editor        `json:"editor"`
}

// Defaults returns the document with every default.
// DefaultLeadName is the lead agent's name until the owner gives another (ROADMAP #45).
const DefaultLeadName = "Aruna"

// formerLeadName was the default before 2026-11-08; a hub that saved it moves to the new one.
const formerLeadName = "Nara"

func Defaults() Settings {
	return Settings{
		SchemaVersion: 1,
		Agents: Agents{
			Models: map[string]ModelChoice{}, RoleModels: map[string]ModelChoice{},
			Defaults: map[string]string{}, Paths: map[string]map[string]string{}, Profiles: map[string]map[string]string{}, Disabled: map[string]bool{},
			RoleAgents: map[string]string{"frontend": "claude", "backend": "codex", "testers": "gemini", "reviewers": "claude",
				"design": "claude", "docs": "claude", "security": "codex", "general": ""},
			LiveStatus: true, IDELink: true, HookChannel: "auto", ResumeOnRestart: "auto",
			// out of preview on 2026-11-08 (ROADMAP #44): a hub that saved false keeps it
			RemoteTools: true,
		},
		Notifications: Notifications{NeedsYou: true, Finished: true, Error: true, FinishedMinSec: 30},
		Shortcuts: Shortcuts{Select: "v", Hand: "h", Spawn: "t", Pen: "p", Eraser: "e", Git: "g", Memory: "m",
			Skills: "s", Loops: "l", Markdown: "d", NeedsYou: "n"},
		Runner: Runner{DefaultAgent: "claude"},
		Lead:   Lead{Name: DefaultLeadName, TalkFirst: true},
		Delivery: Delivery{WhileWorking: "queue", StartingTimeoutSec: 60, NeedsYouTimeoutSec: 900, WorkingTimeoutSec: 1800,
			ReconnectTimeoutSec: 300, SubmitDelayMs: 200, HumanTypingGuardSec: 2, PromptTimeoutSec: 120},
		Commands: Commands{Planner: "auto", ConfirmTTLSec: 60, ReportTimeoutMin: 30, RiskyPatterns: []string{}},
		Previews: defaultPreviews(),
		Git:      defaultGit(),
		Usage:    defaultUsage(),
		Editor:   Editor{OnSave: []OnSaveRule{}},
	}
}

// Error is a validation failure with an API code.
type Error struct {
	Code string // bad_setting | bad_shortcut
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func badSetting(format string, a ...any) error {
	return &Error{Code: "bad_setting", Msg: fmt.Sprintf(format, a...)}
}

// Store keeps the document in memory and on disk.
type Store struct {
	st *store.Store
	// OnChange fires after a successful patch with the new document.
	OnChange func(Settings)

	mu  sync.Mutex
	cur Settings
}

// Open loads settings.json (missing or unreadable keys use defaults).
func Open(st *store.Store) (*Store, error) {
	s := &Store{st: st, cur: Defaults()}
	var raw json.RawMessage
	found, err := st.Load(File, &raw)
	if err != nil {
		return nil, err
	}
	if found {
		doc := Defaults()
		if err := json.Unmarshal(raw, &doc); err == nil {
			settleHandKey(&doc.Shortcuts)
			// the first default name, never picked by anyone: the owner chose another (2026-11-08)
			if doc.Lead.Name == formerLeadName {
				doc.Lead.Name = DefaultLeadName
			}
			if validate(&doc) == nil {
				s.cur = doc
			}
		}
	}
	return s, nil
}

// settleHandKey keeps a document saved before the Hand tool valid: a key already
// bound to another action stays there and Hand takes a free one.
func settleHandKey(sc *Shortcuts) {
	used := map[string]bool{"x": true}
	for _, k := range []string{sc.Select, sc.Spawn, sc.Pen, sc.Eraser, sc.Git, sc.Memory, sc.Skills, sc.Loops, sc.Markdown, sc.NeedsYou} {
		used[k] = true
	}
	if !used[sc.Hand] {
		return
	}
	for _, r := range "hjkyuqwrzbcfioa" {
		if k := string(r); !used[k] {
			sc.Hand = k
			return
		}
	}
}

// Get returns a copy of the current document.
func (s *Store) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.cur)
}

func clone(v Settings) Settings {
	b, _ := json.Marshal(v)
	out := Settings{}
	_ = json.Unmarshal(b, &out)
	return out
}

const maxOnSaveRules = 50

var topLevel = map[string]bool{"schemaVersion": true, "lead": true, "agents": true, "notifications": true, "shortcuts": true, "runner": true,
	"delivery": true, "commands": true, "memory": true, "previews": true, "git": true, "usage": true, "editor": true}

// Patch applies a JSON Merge Patch and returns the full new document.
func (s *Store) Patch(patch []byte) (Settings, error) {
	var p map[string]json.RawMessage
	if err := json.Unmarshal(patch, &p); err != nil || p == nil {
		return Settings{}, badSetting("the patch must be a JSON object")
	}
	for k := range p {
		if !topLevel[k] {
			return Settings{}, badSetting("unknown setting: %s", k)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	curJSON, _ := json.Marshal(s.cur)
	var doc any
	_ = json.Unmarshal(curJSON, &doc)
	var pv any
	_ = json.Unmarshal(patch, &pv)
	merged, _ := json.Marshal(mergePatch(doc, pv))
	next := Defaults()
	dec := json.NewDecoder(bytes.NewReader(merged))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return Settings{}, badSetting("invalid settings: %v", err)
	}
	if err := validate(&next); err != nil {
		return Settings{}, err
	}
	if err := s.st.Save(File, next); err != nil {
		return Settings{}, err
	}
	s.cur = next
	out := clone(next)
	if s.OnChange != nil {
		go s.OnChange(clone(next))
	}
	return out, nil
}

// mergePatch implements RFC 7386.
func mergePatch(target, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	for k, v := range pm {
		if v == nil {
			delete(tm, k)
			continue
		}
		tm[k] = mergePatch(tm[k], v)
	}
	return tm
}

var (
	idRe       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	machineRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	profileRe  = regexp.MustCompile(`^pf_[a-z0-9]{4,24}$`)
	providerRe = regexp.MustCompile(`^ap_[a-z0-9]{4,24}$`)
	modelRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,127}$`)
	effortRe   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)
	shortcutRe = regexp.MustCompile(`^[a-z0-9!-/:-@\[-` + "`" + `{-~]$`)
)

// ErrBadShortcut marks a shortcut validation failure.
var ErrBadShortcut = errors.New("invalid shortcut")

func validate(s *Settings) error {
	s.Lead.Name = strings.TrimSpace(s.Lead.Name)
	if n := len([]rune(s.Lead.Name)); n < 1 || n > 24 || strings.ContainsAny(s.Lead.Name, "\r\n") {
		return badSetting("lead.name: 1 to 24 characters on one line")
	}
	if len([]rune(s.Lead.Personality)) > 800 {
		return badSetting("lead.personality: at most 800 characters")
	}
	if r := s.Lead.Runner; r != "" && r != "claude" && r != "codex" && !providerRe.MatchString(r) {
		return badSetting(`lead.runner must be "", "claude", "codex" or the id of an API provider`)
	}
	if m := s.Lead.Model; m != "" && !modelRe.MatchString(m) {
		return badSetting("lead.model: a model name")
	}
	if e := s.Lead.Effort; e != "" && !effortRe.MatchString(e) {
		return badSetting("lead.effort: an effort level")
	}
	if len(s.Lead.Providers) > 64 {
		return badSetting("lead.providers: at most 64 agents")
	}
	for ag, p := range s.Lead.Providers {
		if p == "" {
			delete(s.Lead.Providers, ag)
			continue
		}
		if !idRe.MatchString(ag) || !providerRe.MatchString(p) {
			return badSetting("lead.providers maps an agent id to the id of an API provider")
		}
	}
	if s.SchemaVersion != 1 {
		return badSetting("schemaVersion must be 1")
	}
	if s.Editor.OnSave == nil {
		s.Editor.OnSave = []OnSaveRule{}
	}
	if len(s.Editor.OnSave) > maxOnSaveRules {
		return badSetting("editor.onSave: at most %d rules", maxOnSaveRules)
	}
	for _, r := range s.Editor.OnSave {
		if strings.TrimSpace(r.Pattern) == "" || strings.TrimSpace(r.Command) == "" {
			return badSetting("editor.onSave: pattern and command are required")
		}
	}
	a := &s.Agents
	for _, m := range []*map[string]string{&a.Defaults, &a.RoleAgents} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	for _, m := range []*map[string]map[string]string{&a.Paths, &a.Profiles} {
		if *m == nil {
			*m = map[string]map[string]string{}
		}
	}
	for mid, ag := range a.Defaults {
		if !machineRe.MatchString(mid) || !idRe.MatchString(ag) {
			return badSetting("invalid agents.defaults: %s", mid)
		}
	}
	for mid, per := range a.Paths {
		if !machineRe.MatchString(mid) {
			return badSetting("agents.paths: machine %q", mid)
		}
		for ag, p := range per {
			if !idRe.MatchString(ag) || p == "" || len(p) > 1024 || bytes.ContainsAny([]byte(p), "\x00\n\r") {
				return badSetting("invalid agents.paths.%s.%s", mid, ag)
			}
		}
	}
	for mid, per := range a.Profiles {
		if !machineRe.MatchString(mid) {
			return badSetting("agents.profiles: machine %q", mid)
		}
		for ag, p := range per {
			if !idRe.MatchString(ag) || (p != "" && !profileRe.MatchString(p)) {
				return badSetting("invalid agents.profiles.%s.%s", mid, ag)
			}
		}
	}
	for ag := range a.Disabled {
		if !idRe.MatchString(ag) {
			return badSetting("invalid agents.disabled.%s", ag)
		}
	}
	for role, ag := range a.RoleAgents {
		if role == "" || len(role) > 32 || (ag != "" && !idRe.MatchString(ag)) {
			return badSetting("invalid agents.roleAgents.%s", role)
		}
	}
	for _, m := range []*map[string]ModelChoice{&a.Models, &a.RoleModels} {
		if *m == nil {
			*m = map[string]ModelChoice{}
		}
		for k, c := range *m {
			if k == "" || len(k) > 32 || (c.Model != "" && !modelRe.MatchString(c.Model)) || (c.Effort != "" && !effortRe.MatchString(c.Effort)) {
				return badSetting("agents: invalid model for %s", k)
			}
		}
	}
	switch a.HookChannel {
	case "auto", "tty", "http":
	default:
		return badSetting(`agents.hookChannel must be "auto", "tty" or "http"`)
	}
	switch a.ResumeOnRestart {
	case "auto", "ask", "never":
	default:
		return badSetting(`agents.resumeOnRestart must be "auto", "ask" or "never"`)
	}
	if n := s.Notifications.FinishedMinSec; n < 0 || n > 86400 {
		return badSetting("notifications.finishedMinSec must be 0–86400")
	}
	if s.Runner.DefaultAgent != "claude" && s.Runner.DefaultAgent != "codex" {
		return badSetting(`runner.defaultAgent must be "claude" or "codex"`)
	}
	if s.Runner.Provider != "" && !providerRe.MatchString(s.Runner.Provider) {
		return badSetting("runner.provider must be the id of an API provider")
	}
	if err := validateOrchestration(s); err != nil {
		return err
	}
	if err := validatePreviews(s); err != nil {
		return err
	}
	if err := validateUsage(s); err != nil {
		return err
	}
	sc := s.Shortcuts
	keys := map[string]string{}
	for name, k := range map[string]string{"select": sc.Select, "hand": sc.Hand, "spawn": sc.Spawn, "pen": sc.Pen, "eraser": sc.Eraser,
		"git": sc.Git, "memory": sc.Memory, "skills": sc.Skills, "loops": sc.Loops, "markdown": sc.Markdown, "needsYou": sc.NeedsYou} {
		if !shortcutRe.MatchString(k) || k == "x" {
			return &Error{Code: "bad_shortcut", Msg: fmt.Sprintf("shortcut %s: one printable character (lowercase letter, digit, punctuation), not x or space", name)}
		}
		if other, dup := keys[k]; dup {
			return &Error{Code: "bad_shortcut", Msg: fmt.Sprintf("shortcut %q is used by %s and %s", k, other, name)}
		}
		keys[k] = name
	}
	return nil
}

// validateOrchestration checks the phase 6 keys (delivery, commands).
func validateOrchestration(s *Settings) error {
	d := s.Delivery
	if d.WhileWorking != "queue" && d.WhileWorking != "type" {
		return badSetting(`delivery.whileWorking must be "queue" or "type"`)
	}
	for _, r := range []struct {
		name      string
		v, lo, hi int
	}{
		{"delivery.startingTimeoutSec", d.StartingTimeoutSec, 5, 3600},
		{"delivery.needsYouTimeoutSec", d.NeedsYouTimeoutSec, 10, 86400},
		{"delivery.workingTimeoutSec", d.WorkingTimeoutSec, 10, 86400},
		{"delivery.reconnectTimeoutSec", d.ReconnectTimeoutSec, 10, 86400},
		{"delivery.submitDelayMs", d.SubmitDelayMs, 100, 1000},
		{"delivery.humanTypingGuardSec", d.HumanTypingGuardSec, 1, 30},
		{"delivery.promptTimeoutSec", d.PromptTimeoutSec, 10, 3600},
		{"commands.confirmTtlSec", s.Commands.ConfirmTTLSec, 30, 300},
		{"commands.reportTimeoutMin", s.Commands.ReportTimeoutMin, 1, 1440},
	} {
		if r.v < r.lo || r.v > r.hi {
			return badSetting("%s must be %d–%d", r.name, r.lo, r.hi)
		}
	}
	if s.Commands.Planner != "auto" && s.Commands.Planner != "off" {
		return badSetting(`commands.planner must be "auto" or "off"`)
	}
	if s.Commands.RiskyPatterns == nil {
		s.Commands.RiskyPatterns = []string{}
	}
	if len(s.Commands.RiskyPatterns) > 50 {
		return badSetting("commands.riskyPatterns: at most 50")
	}
	for _, p := range s.Commands.RiskyPatterns {
		if p == "" || len(p) > 200 {
			return badSetting("commands.riskyPatterns: a pattern is 1–200 characters")
		}
		if _, err := regexp.Compile("(?i)" + p); err != nil {
			return badSetting("commands.riskyPatterns: %q is not valid RE2", p)
		}
	}
	return nil
}
