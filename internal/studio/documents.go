// Package studio holds the hub side of the Phase 7 studios
// (docs/API-studio.md §5–§8): the Documents library and its runner jobs,
// Diagrams (nb.diagram/v1 validation and landing as board ops), Code studio
// helpers on top of the file API, and the Markdown vault index with its
// link graph and a polling file watcher.
package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/store"
)

// DocTypes and their titles (§5).
var DocTypes = map[string]string{
	"overview": "Project overview", "onboarding": "Onboarding guide", "architecture": "Architecture",
	"readme": "README", "api": "API reference", "dataModel": "Data model", "security": "Security overview",
	"testing": "Testing guide", "releaseNotes": "Release notes", "custom": "",
}

// docFocus is what each type covers (prompt detail).
var docFocus = map[string]string{
	"overview":     "what the project is, who it is for, its main features, how the code is organised and how to run it",
	"onboarding":   "everything a new developer needs on day one: setup, running and testing, the code layout, conventions and where to start",
	"architecture": "the main components, how they talk to each other, data and control flow, key design decisions and trade-offs",
	"readme":       "a README: a one-paragraph summary, features, requirements, installation, usage examples, configuration and contributing",
	"api":          "the public API (HTTP endpoints, CLI commands or exported library functions): parameters, responses, errors and examples",
	"dataModel":    "the data model: entities, fields, relationships, storage and lifecycle",
	"security":     "authentication, authorisation, secrets handling, input validation, trust boundaries and known risks",
	"testing":      "how the project is tested: test layout, how to run the tests, fixtures, what is covered and what is missing",
	"releaseNotes": "release notes for the current state of the project, grouped as features, fixes and changes, based on the code and any changelog or history files",
}

// Effort levels: words and total time limit (§5).
type Effort struct {
	MinWords, MaxWords int
	Timeout            time.Duration
}

var Efforts = map[string]Effort{
	"quick":    {400, 700, 6 * time.Minute},
	"balanced": {700, 1100, 12 * time.Minute},
	"thorough": {900, 1500, 25 * time.Minute},
}

// DocIdle is the runner idle limit of a document job.
const DocIdle = 200 * time.Second

const maxAsk = 2000

var (
	ErrDocNotFound     = errors.New("document not found")
	ErrVersionNotFound = errors.New("document version not found")
	ErrBadDocType      = errors.New("unknown document type")
	ErrBadEffort       = errors.New(`effort must be "quick", "balanced" or "thorough"`)
	ErrBadAsk          = fmt.Errorf("ask is required for the custom type (≤ %d characters)", maxAsk)
)

// Version is one generated version of a document.
type Version struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Agent      string    `json:"agent"`
	MachineID  string    `json:"machineId"`
	Effort     string    `json:"effort"`
	State      string    `json:"state"` // running | complete | incomplete
	Words      int       `json:"words"`
	DurationMs int64     `json:"durationMs"`
	JobID      string    `json:"jobId"`
}

// Document is a library entry of a project.
type Document struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Ask       string    `json:"ask,omitempty"`
	Versions  []Version `json:"versions"`
}

// DocRequest is POST /api/projects/{id}/documents (after the hub resolved
// agent, machine, cwd and profile env).
type DocRequest struct {
	Type       string
	Ask        string
	Effort     string
	Agent      string
	DocumentID string
	MachineID  string
	Cwd        string
	Command    string
	Env        []string
	Model      string // optional (real-CLI test uses haiku)
}

// Documents is the document library (documents/<projectId>/…) and starts
// document runner jobs.
type Documents struct {
	st *store.Store
	// Submit queues a runner job (jobs.Manager.SubmitRunner).
	Submit func(jobs.RunnerRequest) (jobs.Job, error)
	// Publish sends {"type":"document",…}.
	Publish func(v any)

	mu sync.Mutex
}

// NewDocuments opens the library.
func NewDocuments(st *store.Store) *Documents { return &Documents{st: st} }

var idRe = regexp.MustCompile(`^[a-z]{1,3}_[a-z0-9]{6,24}$`)

func (d *Documents) indexName(pid string) string {
	return filepath.Join("documents", pid, "index.json")
}

func (d *Documents) load(pid string) []Document {
	var list []Document
	if !idRe.MatchString(pid) {
		return nil
	}
	if _, err := d.st.Load(d.indexName(pid), &list); err != nil {
		return nil
	}
	return list
}

func (d *Documents) save(pid string, list []Document) error {
	if err := os.MkdirAll(d.st.Path("documents", pid), 0o700); err != nil {
		return err
	}
	if list == nil {
		list = []Document{}
	}
	return d.st.Save(d.indexName(pid), list)
}

// List returns a project's documents (newest version first in each).
func (d *Documents) List(pid string) []Document {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := d.load(pid)
	if list == nil {
		list = []Document{}
	}
	return list
}

// find returns the project and index of docID. Caller holds mu.
func (d *Documents) find(docID string) (string, []Document, int) {
	if !idRe.MatchString(docID) {
		return "", nil, -1
	}
	ents, _ := os.ReadDir(d.st.Path("documents"))
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		list := d.load(e.Name())
		for i, doc := range list {
			if doc.ID == docID {
				return e.Name(), list, i
			}
		}
	}
	return "", nil, -1
}

// Get returns one document.
func (d *Documents) Get(docID string) (Document, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, list, i := d.find(docID)
	if i < 0 {
		return Document{}, ErrDocNotFound
	}
	return list[i], nil
}

func (d *Documents) versionPath(pid, docID, vid string) string {
	return d.st.Path("documents", pid, docID, vid+".md")
}

// Version returns a version and its Markdown.
func (d *Documents) Version(docID, vid string) (Version, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	pid, list, i := d.find(docID)
	if i < 0 {
		return Version{}, "", ErrDocNotFound
	}
	for _, v := range list[i].Versions {
		if v.ID == vid {
			b, err := os.ReadFile(d.versionPath(pid, docID, vid))
			if err != nil && !os.IsNotExist(err) {
				return Version{}, "", err
			}
			return v, string(b), nil
		}
	}
	return Version{}, "", ErrVersionNotFound
}

// Delete removes a document permanently.
func (d *Documents) Delete(docID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	pid, list, i := d.find(docID)
	if i < 0 {
		return ErrDocNotFound
	}
	list = append(list[:i], list[i+1:]...)
	if err := d.save(pid, list); err != nil {
		return err
	}
	return os.RemoveAll(d.st.Path("documents", pid, docID))
}

// DeleteVersion removes one version permanently (the document stays,
// possibly without versions).
func (d *Documents) DeleteVersion(docID, vid string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	pid, list, i := d.find(docID)
	if i < 0 {
		return ErrDocNotFound
	}
	vs := list[i].Versions
	for k, v := range vs {
		if v.ID == vid {
			list[i].Versions = append(vs[:k], vs[k+1:]...)
			if err := d.save(pid, list); err != nil {
				return err
			}
			_ = os.Remove(d.versionPath(pid, docID, vid))
			d.publish(pid, list[i])
			return nil
		}
	}
	return ErrVersionNotFound
}

func (d *Documents) publish(pid string, doc Document) {
	if d.Publish != nil {
		d.Publish(map[string]any{"type": "document", "projectId": pid, "document": doc})
	}
}

// DocPrompt is the runner prompt of a document.
func DocPrompt(typ, title, ask, effort string) string {
	e := Efforts[effort]
	var b strings.Builder
	fmt.Fprintf(&b, "Write a %q document for the software project in the current folder.\n\n", title)
	if typ == "custom" {
		fmt.Fprintf(&b, "What the user asked for:\n%s\n\n", ask)
	} else {
		fmt.Fprintf(&b, "Cover %s.\n\n", docFocus[typ])
	}
	fmt.Fprintf(&b, "Rules:\n"+
		"- Explore the repository with the read-only tools only (Read, Glob, Grep). Do not modify anything.\n"+
		"- Base every statement on what you find in the files; say so when something is unclear.\n"+
		"- Write in English, in GitHub-flavoured Markdown, without raw HTML.\n"+
		"- Start with the line \"# %s\" and use ## headings for sections.\n"+
		"- Aim for %d–%d words.\n"+
		"- Reply with the document only: no preamble, no closing remarks, no code fence around it.\n", title, e.MinWords, e.MaxWords)
	return b.String()
}

// customTitle derives a title from the ask.
func customTitle(ask string) string {
	t := strings.Join(strings.Fields(ask), " ")
	if r := []rune(t); len(r) > 60 {
		t = strings.TrimSpace(string(r[:59])) + "…"
	}
	if t == "" {
		return "Custom document"
	}
	return strings.ToUpper(t[:1]) + t[1:]
}

// Generate validates req, records the document and a running version,
// and queues the runner job. The version is finished by the job's
// OnFinish: complete with the text, incomplete with the text that arrived
// before Stop/timeout, or removed when nothing arrived.
func (d *Documents) Generate(pid string, req DocRequest) (Document, string, jobs.Job, error) {
	if !idRe.MatchString(pid) {
		return Document{}, "", jobs.Job{}, ErrDocNotFound
	}
	if req.DocumentID != "" { // regenerate keeps type and ask
		cur, err := d.Get(req.DocumentID)
		if err != nil || cur.ProjectID != pid {
			return Document{}, "", jobs.Job{}, ErrDocNotFound
		}
		req.Type, req.Ask = cur.Type, cur.Ask
	}
	if _, ok := DocTypes[req.Type]; !ok {
		return Document{}, "", jobs.Job{}, ErrBadDocType
	}
	eff, ok := Efforts[req.Effort]
	if !ok {
		return Document{}, "", jobs.Job{}, ErrBadEffort
	}
	req.Ask = strings.TrimSpace(req.Ask)
	if (req.Type == "custom" && req.Ask == "") || len(req.Ask) > maxAsk {
		return Document{}, "", jobs.Job{}, ErrBadAsk
	}
	d.mu.Lock()
	list := d.load(pid)
	var doc Document
	idx := -1
	if req.DocumentID != "" {
		for i, x := range list {
			if x.ID == req.DocumentID {
				idx, doc = i, x
			}
		}
		if idx < 0 {
			d.mu.Unlock()
			return Document{}, "", jobs.Job{}, ErrDocNotFound
		}
	} else {
		title := DocTypes[req.Type]
		if req.Type == "custom" {
			title = customTitle(req.Ask)
		}
		doc = Document{ID: auth.RandomID("doc_"), ProjectID: pid, Type: req.Type, Title: title, Ask: req.Ask, Versions: []Version{}}
	}
	// The running version is recorded before the job starts, so a job
	// that finishes at once still finds it.
	vid := auth.RandomID("v_")
	mid := req.MachineID
	if mid == "" {
		mid = "local"
	}
	v := Version{ID: vid, At: time.Now().UTC(), Agent: req.Agent, MachineID: mid, Effort: req.Effort, State: "running"}
	isNew := idx < 0
	doc.Versions = append([]Version{v}, doc.Versions...)
	if isNew {
		list = append(list, doc)
	} else {
		list[idx] = doc
	}
	err := d.save(pid, list)
	d.mu.Unlock()
	if err != nil {
		return Document{}, "", jobs.Job{}, err
	}
	started := time.Now()
	j, err := d.Submit(jobs.RunnerRequest{Purpose: "document", ProjectID: pid, MachineID: req.MachineID, Cwd: req.Cwd,
		Agent: req.Agent, Command: req.Command, Env: req.Env, Prompt: DocPrompt(req.Type, doc.Title, req.Ask, req.Effort),
		Stream: true, Timeout: eff.Timeout, IdleTimeout: DocIdle, Model: req.Model,
		OnFinish: func(j jobs.Job) { d.finish(pid, doc.ID, vid, started, j) }})
	d.mu.Lock()
	defer d.mu.Unlock()
	list = d.load(pid)
	for i := range list {
		if list[i].ID != doc.ID {
			continue
		}
		for k := range list[i].Versions {
			if list[i].Versions[k].ID == vid {
				if err != nil {
					list[i].Versions = append(list[i].Versions[:k], list[i].Versions[k+1:]...)
				} else {
					list[i].Versions[k].JobID = j.ID
				}
				break
			}
		}
		doc = list[i]
		if err != nil && isNew && len(list[i].Versions) == 0 {
			list = append(list[:i], list[i+1:]...)
		}
		break
	}
	if serr := d.save(pid, list); err == nil {
		err = serr
	}
	if err != nil {
		return Document{}, "", jobs.Job{}, err
	}
	d.publish(pid, doc)
	return doc, vid, j, nil
}

// wordCount counts words (fields with a letter or digit; Markdown
// markers such as "#" or "-" are not words).
func wordCount(s string) int {
	n := 0
	for _, f := range strings.Fields(s) {
		if strings.IndexFunc(f, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			n++
		}
	}
	return n
}

// cleanMarkdown strips a code fence the agent may have wrapped the
// document in.
func cleanMarkdown(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s) + "\n"
}

func (d *Documents) finish(pid, docID, vid string, started time.Time, j jobs.Job) {
	var res struct {
		Text    string `json:"text"`
		Partial bool   `json:"partial"`
	}
	_ = json.Unmarshal(j.Result, &res)
	text := ""
	if strings.TrimSpace(res.Text) != "" {
		text = cleanMarkdown(res.Text)
	}
	state := "complete"
	if j.State != jobs.StateDone || res.Partial {
		state = "incomplete"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	list := d.load(pid)
	for i := range list {
		if list[i].ID != docID {
			continue
		}
		vs := list[i].Versions
		for k := range vs {
			if vs[k].ID != vid {
				continue
			}
			if text == "" {
				list[i].Versions = append(vs[:k], vs[k+1:]...) // nothing arrived
			} else {
				vs[k].State = state
				vs[k].Words = wordCount(text)
				vs[k].DurationMs = j.ElapsedMs
				if vs[k].DurationMs == 0 {
					vs[k].DurationMs = time.Since(started).Milliseconds()
				}
				_ = os.MkdirAll(d.st.Path("documents", pid, docID), 0o700)
				_ = store.WriteFileAtomic(d.versionPath(pid, docID, vid), []byte(text))
			}
			_ = d.save(pid, list)
			d.publish(pid, list[i])
			return
		}
	}
}

// EffortStat is the median duration of the last 5 complete runs.
type EffortStat struct {
	MedianMs *int64 `json:"medianMs"`
	Runs     int    `json:"runs"`
}

// Stats is GET /api/documents/stats over every project's library.
func (d *Documents) Stats(machineID, agentID string) map[string]EffortStat {
	d.mu.Lock()
	var all []Version
	ents, _ := os.ReadDir(d.st.Path("documents"))
	for _, e := range ents {
		if e.IsDir() {
			for _, doc := range d.load(e.Name()) {
				all = append(all, doc.Versions...)
			}
		}
	}
	d.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].At.After(all[j].At) })
	out := map[string]EffortStat{}
	for eff := range Efforts {
		var ms []int64
		for _, v := range all {
			if v.Effort != eff || v.State != "complete" || v.DurationMs <= 0 ||
				(machineID != "" && v.MachineID != machineID) || (agentID != "" && v.Agent != agentID) {
				continue
			}
			ms = append(ms, v.DurationMs)
			if len(ms) == 5 {
				break
			}
		}
		st := EffortStat{Runs: len(ms)}
		if len(ms) > 0 {
			sort.Slice(ms, func(i, j int) bool { return ms[i] < ms[j] })
			m := ms[len(ms)/2]
			if len(ms)%2 == 0 {
				m = (ms[len(ms)/2-1] + ms[len(ms)/2]) / 2
			}
			st.MedianMs = &m
		}
		out[eff] = st
	}
	return out
}

// Recover marks versions that were running when the hub stopped: with
// text on disk incomplete, else removed (jobs come back as hub_restart).
func (d *Documents) Recover() {
	d.mu.Lock()
	defer d.mu.Unlock()
	ents, _ := os.ReadDir(d.st.Path("documents"))
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		pid := e.Name()
		list := d.load(pid)
		changed := false
		for i := range list {
			kept := list[i].Versions[:0]
			for _, v := range list[i].Versions {
				if v.State == "running" {
					changed = true
					if _, err := os.Stat(d.versionPath(pid, list[i].ID, v.ID)); err != nil {
						continue
					}
					v.State = "incomplete"
				}
				kept = append(kept, v)
			}
			list[i].Versions = kept
		}
		if changed {
			_ = d.save(pid, list)
		}
	}
}
