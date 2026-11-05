package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"nextboard/internal/store"
)

// File holds custom agents (the phase 1–4 presets.json, extended).
const File = "presets.json"

// Errors map to {"error","code"} in the API.
var (
	ErrNotFound = errors.New("agen tidak ditemukan")
	ErrExists   = errors.New("id agen sudah dipakai")
	ErrBuiltin  = errors.New("agen bawaan/katalog tidak bisa diubah atau dihapus")
)

// BadAgentError is a validation failure (400 bad_agent).
type BadAgentError struct{ Msg string }

func (e *BadAgentError) Error() string { return "agen tidak valid: " + e.Msg }

func bad(format string, a ...any) error { return &BadAgentError{fmt.Sprintf(format, a...)} }

// entry is one custom agent as stored. Old Preset entries (id, name,
// command, args, resumeArgs, color, env) stay valid.
type entry struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Command    string    `json:"command"`
	Args       []string  `json:"args,omitempty"`
	Env        []string  `json:"env,omitempty"`
	Color      string    `json:"color,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Base       string    `json:"base,omitempty"`
	YoloArgs   *[]string `json:"yoloArgs,omitempty"`
	YoloEnv    *[]string `json:"yoloEnv,omitempty"`
	Resume     *Resume   `json:"resume,omitempty"`
	ResumeArgs []string  `json:"resumeArgs,omitempty"` // legacy
}

var (
	idRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,23}$`)
	colorRe  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Store merges built-ins, the catalog and presets.json. The file is
// re-read every few seconds so hand edits apply without a restart.
type Store struct {
	st *store.Store

	mu      sync.Mutex
	entries []entry
	at      time.Time
	loaded  bool
}

// NewStore opens the catalog backed by <data>/presets.json.
func NewStore(st *store.Store) *Store { return &Store{st: st} }

func (s *Store) loadLocked() {
	if s.loaded && time.Since(s.at) < 5*time.Second {
		return
	}
	var list []entry
	if _, err := s.st.Load(File, &list); err != nil {
		list = nil
	}
	s.entries, s.at, s.loaded = nil, time.Now(), true
	for _, e := range list {
		if e.ID != "" && e.Command != "" {
			s.entries = append(s.entries, e)
		}
	}
}

// Reload forces the next read of presets.json.
func (s *Store) Reload() {
	s.mu.Lock()
	s.loaded = false
	s.mu.Unlock()
}

func builtinByID(id string) (Agent, bool) {
	for _, a := range append(Builtins(), Catalog()...) {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// resolve turns a stored entry into an Agent, inheriting capabilities
// from its base (or, for a legacy override, from the built-in it shadows).
func resolve(e entry) Agent {
	base := e.Base
	if b, ok := builtinByID(e.ID); ok && b.Source == SourceBuiltin {
		base = e.ID // legacy override of a built-in id
	}
	a := Agent{ID: e.ID, Name: e.Name, Source: SourceCustom, Base: base, Command: e.Command,
		Args: e.Args, Env: e.Env, Color: e.Color, Kind: e.Kind,
		LiveStatus: LiveScreen, Transcript: TranscriptNone}
	if b, ok := builtinByID(base); ok && b.Source == SourceBuiltin {
		a.LiveStatus, a.Transcript, a.ProfileEnv, a.ScreenRules = b.LiveStatus, b.Transcript, b.ProfileEnv, b.ScreenRules
		a.YoloArgs, a.YoloEnv, a.Resume = b.YoloArgs, b.YoloEnv, b.Resume
		if a.Kind == "" {
			a.Kind = b.Kind
		}
	}
	if e.YoloArgs != nil {
		a.YoloArgs = *e.YoloArgs
	}
	if e.YoloEnv != nil {
		a.YoloEnv = *e.YoloEnv
	}
	switch {
	case e.Resume != nil:
		a.Resume = *e.Resume
	case len(e.ResumeArgs) > 0:
		a.Resume = Resume{Kind: ResumeContinue, ContinueArgs: e.ResumeArgs}
	case base == "":
		a.Resume = Resume{Kind: ResumeNone}
	}
	if a.Kind == "" {
		a.Kind = KindAgent
		if e.Command == "$SHELL" {
			a.Kind = KindShell
		}
	}
	if a.Kind == KindShell {
		a.LiveStatus, a.Transcript, a.ProfileEnv, a.ScreenRules = LiveNone, TranscriptNone, "", ""
	}
	if a.Name == "" {
		a.Name = a.ID
	}
	if a.Color == "" {
		a.Color = "#a371f7"
	}
	if e.Command != "$SHELL" {
		a.Detect = e.Command
	}
	return a.normalize()
}

// All returns built-ins, catalog, then custom agents. Legacy overrides
// take the place of the built-in they shadow.
func (s *Store) All() []Agent {
	s.mu.Lock()
	s.loadLocked()
	entries := append([]entry(nil), s.entries...)
	s.mu.Unlock()
	out := append(Builtins(), Catalog()...)
	for _, e := range entries {
		a := resolve(e)
		replaced := false
		for i := range out {
			if out[i].ID == a.ID {
				if out[i].Source == SourceCatalog {
					replaced = true // catalog ids cannot be shadowed
					break
				}
				out[i], replaced = a, true
			}
		}
		if !replaced {
			out = append(out, a)
		}
	}
	return out
}

// Get looks an agent up by id.
func (s *Store) Get(id string) (Agent, bool) {
	for _, a := range s.All() {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// Request is the body of POST/PATCH /api/agents, decoded strictly.
type Request map[string]json.RawMessage

var allowedFields = map[string]bool{"id": true, "name": true, "command": true, "args": true, "env": true,
	"color": true, "kind": true, "base": true, "yoloArgs": true, "yoloEnv": true, "resume": true}

// slug makes an id out of a display name.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 24 {
		out = strings.Trim(out[:24], "-")
	}
	if out != "" && (out[0] < 'a' || out[0] > 'z') {
		out = "a-" + out
	}
	return out
}

// apply merges a request into e and validates the result.
func apply(e *entry, req Request, create bool) error {
	for k := range req {
		if !allowedFields[k] {
			return bad("field %q tidak boleh untuk agen custom", k)
		}
		if k == "id" && !create {
			return bad("id tidak bisa diubah")
		}
	}
	dec := func(k string, v any) error {
		raw, ok := req[k]
		if !ok {
			return nil
		}
		if err := json.Unmarshal(raw, v); err != nil {
			return bad("%s: %v", k, err)
		}
		return nil
	}
	var resume Resume
	_, hasResume := req["resume"]
	var yolo, yoloEnv []string
	_, hasYolo := req["yoloArgs"]
	_, hasYoloEnv := req["yoloEnv"]
	for _, f := range []struct {
		k string
		v any
	}{{"id", &e.ID}, {"name", &e.Name}, {"command", &e.Command}, {"args", &e.Args}, {"env", &e.Env},
		{"color", &e.Color}, {"kind", &e.Kind}, {"base", &e.Base}, {"yoloArgs", &yolo}, {"yoloEnv", &yoloEnv}, {"resume", &resume}} {
		if err := dec(f.k, f.v); err != nil {
			return err
		}
	}
	if hasYolo {
		e.YoloArgs = &yolo
	}
	if hasYoloEnv {
		e.YoloEnv = &yoloEnv
	}
	if hasResume {
		e.Resume = &resume
		e.ResumeArgs = nil
	}
	e.Name = strings.TrimSpace(e.Name)
	e.Command = strings.TrimSpace(e.Command)
	switch {
	case e.Name == "" || len(e.Name) > 80:
		return bad("name wajib diisi (≤ 80 karakter)")
	case e.Command == "" || len(e.Command) > 1024 || strings.ContainsAny(e.Command, "\x00\n\r"):
		return bad("command wajib diisi")
	case e.Kind != "" && e.Kind != KindAgent && e.Kind != KindShell:
		return bad(`kind harus "agent" atau "shell"`)
	case e.Base != "" && e.Base != "claude" && e.Base != "codex":
		return bad(`base harus "claude", "codex" atau kosong`)
	case e.Color != "" && !colorRe.MatchString(e.Color):
		return bad("color harus #rrggbb")
	}
	for _, list := range [][]string{e.Args, derefS(e.YoloArgs)} {
		if len(list) > 64 {
			return bad("terlalu banyak argumen")
		}
		for _, a := range list {
			if len(a) > 4096 || strings.ContainsRune(a, 0) {
				return bad("argumen tidak valid")
			}
		}
	}
	for _, list := range [][]string{e.Env, derefS(e.YoloEnv)} {
		for _, kv := range list {
			k, _, ok := strings.Cut(kv, "=")
			if !ok || !envKeyRe.MatchString(k) || strings.ContainsRune(kv, 0) {
				return bad("env harus KEY=VALUE: %q", kv)
			}
		}
	}
	all := append(append([]string{}, e.Args...), derefS(e.YoloArgs)...)
	if e.Resume != nil {
		r := e.Resume
		all = append(append(append(append(all, r.LaunchArgs...), r.ResumeArgs...), r.ContinueArgs...), r.PickerArgs...)
		switch r.Kind {
		case ResumeContinue, ResumeNone:
		case ResumeSessionID, ResumePicker:
			if e.Base == "" {
				return bad(`agen tanpa base hanya boleh resume.kind "continue" atau "none"`)
			}
		default:
			return bad("resume.kind tidak dikenal")
		}
		if r.Kind == ResumeSessionID && !hasPlaceholder(r.ResumeArgs) {
			return bad("resume.resumeArgs harus memakai %s", Placeholder)
		}
		if e.Base == "" && (hasPlaceholder(r.LaunchArgs) || hasPlaceholder(r.ResumeArgs)) {
			return bad("%s hanya untuk agen dengan base", Placeholder)
		}
	}
	if badTemplate(all) {
		return bad("template yang diizinkan hanya %s", Placeholder)
	}
	if hasPlaceholder(append(append([]string{}, e.Args...), derefS(e.YoloArgs)...)) {
		return bad("%s hanya boleh di resume", Placeholder)
	}
	return nil
}

func derefS(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

func (s *Store) saveLocked() error {
	if s.entries == nil {
		s.entries = []entry{}
	}
	if err := s.st.Save(File, s.entries); err != nil {
		return err
	}
	s.at = time.Now()
	return nil
}

// Create adds a custom agent.
func (s *Store) Create(req Request) (Agent, error) {
	var e entry
	if err := apply(&e, req, true); err != nil {
		return Agent{}, err
	}
	if e.ID == "" {
		e.ID = slug(e.Name)
	}
	if !idRe.MatchString(e.ID) {
		return Agent{}, bad("id harus cocok ^[a-z][a-z0-9-]{1,23}$")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = false
	s.loadLocked()
	if _, ok := builtinByID(e.ID); ok {
		return Agent{}, ErrExists
	}
	for _, x := range s.entries {
		if x.ID == e.ID {
			return Agent{}, ErrExists
		}
	}
	s.entries = append(s.entries, e)
	if err := s.saveLocked(); err != nil {
		return Agent{}, err
	}
	return resolve(e), nil
}

// Update patches a custom agent (built-in and catalog: ErrBuiltin).
func (s *Store) Update(id string, req Request) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = false
	s.loadLocked()
	for i := range s.entries {
		if s.entries[i].ID != id {
			continue
		}
		e := s.entries[i]
		if e.YoloArgs != nil {
			c := append([]string{}, *e.YoloArgs...)
			e.YoloArgs = &c
		}
		if err := apply(&e, req, false); err != nil {
			return Agent{}, err
		}
		s.entries[i] = e
		if err := s.saveLocked(); err != nil {
			return Agent{}, err
		}
		return resolve(e), nil
	}
	if _, ok := builtinByID(id); ok {
		return Agent{}, ErrBuiltin
	}
	return Agent{}, ErrNotFound
}

// Delete removes a custom agent. Running consoles keep their own command.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = false
	s.loadLocked()
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return s.saveLocked()
		}
	}
	if _, ok := builtinByID(id); ok {
		return ErrBuiltin
	}
	return ErrNotFound
}
