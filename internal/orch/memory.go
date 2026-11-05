package orch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/store"
)

// Project memory (docs/API-orchestration.md §12). One project has one memory:
// the folder memory/<projectId>/ in the hub's data folder, whose MEMORY.md is
// what the Memory panel edits, Claude Code keeps as its auto memory, and every
// other agent gets at launch. Agents and CLIs come and go; it stays. Other
// .md files there are an agent's notes. memory/global.md is for all projects.
// Nothing is written to any machine.

const (
	MaxMemory       = 64 << 10
	memoryPushChars = 4000
	// memoryPromptChars bounds the memory in a launch's system prompt.
	memoryPromptChars = 16000
	memoryFile        = "MEMORY.md"
	MemoryPushHead    = "Project context from NextBoard (background only, not a task). Keep it in mind for later work and reply with just: OK"
)

var (
	ErrMtimeMismatch = errors.New("memory sudah berubah sejak dibaca (mtime berbeda)")
	ErrMemoryTooBig  = errors.New("memory melebihi 64 KB")
	ErrBadScope      = errors.New(`scope harus "project" atau "global"`)
)

// MemorySections are the headings of the memory convention.
var MemorySections = []string{"Architecture", "Structure", "Commands", "Conventions", "Gotchas", "Context"}

// MemoryDoc is one memory file.
type MemoryDoc struct {
	Content string `json:"content"`
	Mtime   string `json:"mtime"` // RFC 3339 (nanoseconds); "" when missing
}

// Fact is a scan suggestion.
type Fact struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// MemoryNote is an agent's note in the project's memory folder.
type MemoryNote struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Memory reads and writes the memory files.
type Memory struct{ st *store.Store }

func newMemory(st *store.Store) *Memory {
	_ = os.MkdirAll(st.Path("memory"), 0o700)
	return &Memory{st: st}
}

func (m *Memory) path(scope, projectID string) (string, error) {
	switch scope {
	case "project":
		dir, err := m.Dir(projectID)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, memoryFile), nil
	case "global":
		return m.st.Path("memory", "global.md"), nil
	}
	return "", ErrBadScope
}

// Dir is the project's memory folder. A memory from before there was a folder
// moves in.
func (m *Memory) Dir(projectID string) (string, error) {
	if !idRe.MatchString(projectID) {
		return "", ErrBadScope
	}
	dir := m.st.Path("memory", projectID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	old, file := m.st.Path("memory", projectID+".md"), filepath.Join(dir, memoryFile)
	if _, err := os.Stat(old); err == nil {
		if _, err := os.Stat(file); os.IsNotExist(err) {
			if err := os.Rename(old, file); err != nil {
				return "", err
			}
		}
	}
	return dir, nil
}

// Notes lists the other files of the project's memory folder.
func (m *Memory) Notes(projectID string) []MemoryNote {
	out := []MemoryNote{}
	dir, err := m.Dir(projectID)
	if err != nil {
		return out
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || e.Name() == memoryFile || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			out = append(out, MemoryNote{Name: e.Name(), Content: stripFrontMatter(string(b))})
		}
	}
	return out
}

func stripFrontMatter(s string) string {
	if rest, ok := strings.CutPrefix(s, "---\n"); ok {
		if _, body, ok := strings.Cut(rest, "\n---\n"); ok {
			return strings.TrimLeft(body, "\n")
		}
	}
	return s
}

// Adopt moves a Claude Code memory folder into the project's memory: lines of
// its MEMORY.md that are new are added, notes that are new are copied. The
// source is renamed, so this happens once.
func (m *Memory) Adopt(src, projectID string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil // nothing to adopt
	}
	dir, err := m.Dir(projectID)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dir, e.Name())
		b, err := os.ReadFile(from)
		if e.IsDir() || err != nil {
			continue
		}
		if e.Name() != memoryFile {
			if _, err := os.Stat(to); os.IsNotExist(err) {
				if err := store.WriteFileAtomic(to, b); err != nil {
					return err
				}
			}
			continue
		}
		cur, _ := os.ReadFile(to)
		have := map[string]bool{}
		for _, l := range strings.Split(string(cur), "\n") {
			have[strings.TrimSpace(l)] = true
		}
		merged := strings.TrimRight(string(cur), "\n")
		for _, l := range strings.Split(string(b), "\n") {
			if t := strings.TrimSpace(l); t != "" && !have[t] {
				if merged != "" {
					merged += "\n"
				}
				merged += l
			}
		}
		if err := store.WriteFileAtomic(to, []byte(merged+"\n")); err != nil {
			return err
		}
	}
	return os.Rename(src, src+".moved-to-nextboard")
}

// Read returns one memory file.
func (m *Memory) Read(scope, projectID string) (MemoryDoc, error) {
	p, err := m.path(scope, projectID)
	if err != nil {
		return MemoryDoc{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return MemoryDoc{}, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return MemoryDoc{}, err
	}
	return MemoryDoc{Content: string(b), Mtime: fi.ModTime().UTC().Format(time.RFC3339Nano)}, nil
}

// Write replaces a memory file; ifMtime (when set) must match.
func (m *Memory) Write(scope, projectID, content, ifMtime string) (MemoryDoc, error) {
	if len(content) > MaxMemory {
		return MemoryDoc{}, ErrMemoryTooBig
	}
	p, err := m.path(scope, projectID)
	if err != nil {
		return MemoryDoc{}, err
	}
	if ifMtime != "" {
		cur, _ := m.Read(scope, projectID)
		if !sameMtime(cur.Mtime, ifMtime) {
			return MemoryDoc{}, ErrMtimeMismatch
		}
	}
	if err := store.WriteFileAtomic(p, []byte(content)); err != nil {
		return MemoryDoc{}, err
	}
	return m.Read(scope, projectID)
}

// sameMtime compares with the precision the client sent.
func sameMtime(have, want string) bool {
	if have == want {
		return true
	}
	a, err1 := time.Parse(time.RFC3339Nano, have)
	b, err2 := time.Parse(time.RFC3339Nano, want)
	if err1 != nil || err2 != nil {
		return false
	}
	if b.Nanosecond() == 0 {
		return a.Truncate(time.Second).Equal(b)
	}
	if b.Nanosecond()%int(time.Millisecond) == 0 {
		return a.Truncate(time.Millisecond).Equal(b)
	}
	return a.Equal(b)
}

// sectionOf maps a fact kind to its heading.
func sectionOf(kind string) string {
	for _, s := range MemorySections {
		if strings.EqualFold(s, strings.TrimSpace(kind)) {
			return s
		}
	}
	return "Context"
}

// cleanFact makes one bullet line of a fact text.
func cleanFact(t string) string {
	t = strings.Join(strings.Fields(t), " ")
	t = strings.TrimLeft(t, "-*• ")
	if len([]rune(t)) > 400 {
		t = string([]rune(t)[:400])
	}
	return t
}

// AcceptFacts adds facts as bullets under their section (created when
// missing); identical bullets are not duplicated.
func AcceptFacts(content string, facts []Fact) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	have := map[string]bool{}
	for _, l := range lines {
		if b := bulletText(l); b != "" {
			have[strings.ToLower(b)] = true
		}
	}
	for _, f := range facts {
		text := cleanFact(f.Text)
		if text == "" || have[strings.ToLower(text)] {
			continue
		}
		have[strings.ToLower(text)] = true
		sec := sectionOf(f.Kind)
		head := -1
		for i, l := range lines {
			if strings.EqualFold(strings.TrimSpace(l), "## "+sec) {
				head = i
				break
			}
		}
		bullet := "- " + text
		if head < 0 {
			if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
				lines = append(lines, "")
			}
			lines = append(lines, "## "+sec, bullet)
			continue
		}
		at := head + 1
		for at < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[at]), "## ") {
			at++
		}
		for at > head+1 && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
		lines = append(lines[:at], append([]string{bullet}, lines[at:]...)...)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

func bulletText(l string) string {
	t := strings.TrimSpace(l)
	for _, p := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(t, p) {
			return cleanFact(t[len(p):])
		}
	}
	return ""
}

// ScanPrompt is the runner prompt of a memory scan.
func ScanPrompt(effort string, existing string) (string, int, int) {
	lo, hi := 8, 11
	switch effort {
	case "quick":
		lo, hi = 6, 8
	case "thorough":
		lo, hi = 11, 14
	}
	p := fmt.Sprintf(`You are scanning this repository (read-only) to propose project memory for coding agents.
Propose %d to %d short, durable facts that a new agent would need: architecture, folder structure, build/test commands, conventions, gotchas, context.
Each fact is one sentence. Do not propose facts already in the existing memory below. Do not modify any file.
Answer with JSON matching the schema: {"facts":[{"kind":"architecture|structure|commands|conventions|gotchas|context","text":"…"}]}.

Existing memory:
%s`, lo, hi, existing)
	return p, lo, hi
}

// ScanSchema is the JSON schema of a memory scan.
var ScanSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["facts"],"properties":{"facts":{"type":"array","maxItems":20,"items":{"type":"object","additionalProperties":false,"required":["kind","text"],"properties":{"kind":{"type":"string","enum":["architecture","structure","commands","conventions","gotchas","context"]},"text":{"type":"string","maxLength":400}}}}}}`)

// RefreshSchema is the JSON schema of a memory refresh check.
var RefreshSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["verdict"],"properties":{"verdict":{"type":"string","enum":["valid","outdated","unknown"]},"suggestion":{"type":"string","maxLength":400}}}`)

// ParseFacts validates a scan result.
func ParseFacts(raw json.RawMessage) ([]Fact, error) {
	var v struct {
		Facts []Fact `json:"facts"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	out := []Fact{}
	for _, f := range v.Facts {
		t := cleanFact(f.Text)
		if t == "" {
			continue
		}
		out = append(out, Fact{Kind: strings.ToLower(sectionOf(f.Kind)), Text: t})
		if len(out) == 20 {
			break
		}
	}
	return out, nil
}
