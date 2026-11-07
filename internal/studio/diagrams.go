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
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/store"
)

// nb.diagram/v1 (docs/API-studio.md §6) is the JSON the runner produces.
// The hub validates it strictly (the output is untrusted) and keeps it
// as-generated in diagrams/<projectId>/<id>.json.

const DiagramSchemaID = "nb.diagram/v1"

// Diagram limits (§6, §15).
const (
	MaxDiagramNodes  = 40
	MaxDiagramEdges  = 80
	MaxDiagramGroups = 8
	MaxDiagramLabel  = 48
	MaxDiagramNote   = 80
	MaxDiagramTitle  = 80
)

// Diagram is nb.diagram/v1.
type Diagram struct {
	Schema    string   `json:"schema"`
	Title     string   `json:"title"`
	Direction string   `json:"direction"`
	Nodes     []DNode  `json:"nodes"`
	Edges     []DEdge  `json:"edges"`
	Groups    []DGroup `json:"groups"`
}

type DNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
	Shape string `json:"shape"`
	Group string `json:"group,omitempty"`
}

type DEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
	Style string `json:"style,omitempty"`
}

type DGroup struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var dIDRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ErrDiagram is a validation failure of runner output.
type ErrDiagram struct{ Msg string }

func (e *ErrDiagram) Error() string { return "invalid diagram: " + e.Msg }

func bad(format string, a ...any) error { return &ErrDiagram{fmt.Sprintf(format, a...)} }

// shapeAlias maps loose shape names to the four shapes.
var shapeAlias = map[string]string{
	"": "box", "box": "box", "rect": "box", "rectangle": "box", "square": "box",
	"round": "round", "rounded": "round", "pill": "round", "stadium": "round",
	"ellipse": "ellipse", "circle": "ellipse", "oval": "ellipse",
	"diamond": "diamond", "decision": "diamond", "rhombus": "diamond",
}

// ValidateDiagram parses and checks raw runner output against the limits.
// Empty optional fields are dropped, shapes and directions normalised. It
// returns warnings (e.g. an unlabeled branch of a decision).
func ValidateDiagram(raw []byte) (*Diagram, []string, error) {
	var d Diagram
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&d); err != nil {
		return nil, nil, bad("not nb.diagram/v1 JSON: %v", err)
	}
	if d.Schema == "" {
		d.Schema = DiagramSchemaID
	}
	if d.Schema != DiagramSchemaID {
		return nil, nil, bad("schema %q", d.Schema)
	}
	d.Title = strings.TrimSpace(d.Title)
	if utf8.RuneCountInString(d.Title) > MaxDiagramTitle {
		return nil, nil, bad("title longer than %d characters", MaxDiagramTitle)
	}
	switch strings.ToUpper(d.Direction) {
	case "", "TB", "TD", "DOWN":
		d.Direction = "TB"
	case "LR", "RIGHT":
		d.Direction = "LR"
	default:
		return nil, nil, bad("direction %q", d.Direction)
	}
	if len(d.Nodes) == 0 {
		return nil, nil, bad("no nodes")
	}
	if len(d.Nodes) > MaxDiagramNodes || len(d.Edges) > MaxDiagramEdges || len(d.Groups) > MaxDiagramGroups {
		return nil, nil, bad("more than %d nodes, %d edges or %d groups", MaxDiagramNodes, MaxDiagramEdges, MaxDiagramGroups)
	}
	groups := map[string]bool{}
	for i, g := range d.Groups {
		g.Label = strings.TrimSpace(g.Label)
		if !dIDRe.MatchString(g.ID) || groups[g.ID] {
			return nil, nil, bad("group id %q", g.ID)
		}
		if g.Label == "" || utf8.RuneCountInString(g.Label) > MaxDiagramLabel {
			return nil, nil, bad("group label %q", g.ID)
		}
		groups[g.ID] = true
		d.Groups[i] = g
	}
	nodes := map[string]string{}
	for i, n := range d.Nodes {
		n.Label, n.Note = strings.TrimSpace(n.Label), strings.TrimSpace(n.Note)
		if !dIDRe.MatchString(n.ID) || nodes[n.ID] != "" {
			return nil, nil, bad("node id %q", n.ID)
		}
		if n.Label == "" || utf8.RuneCountInString(n.Label) > MaxDiagramLabel {
			return nil, nil, bad("node %q label is empty or longer than %d characters", n.ID, MaxDiagramLabel)
		}
		if utf8.RuneCountInString(n.Note) > MaxDiagramNote {
			return nil, nil, bad("node %q note is longer than %d characters", n.ID, MaxDiagramNote)
		}
		sh, ok := shapeAlias[strings.ToLower(strings.TrimSpace(n.Shape))]
		if !ok {
			return nil, nil, bad("shape %q", n.Shape)
		}
		n.Shape = sh
		if n.Group != "" && !groups[n.Group] {
			return nil, nil, bad("node %q is in group %q, which does not exist", n.ID, n.Group)
		}
		nodes[n.ID] = sh
		d.Nodes[i] = n
	}
	var warn []string
	for i, e := range d.Edges {
		e.Label = strings.TrimSpace(e.Label)
		if nodes[e.From] == "" || nodes[e.To] == "" {
			return nil, nil, bad("edge %s→%s points at a node that does not exist", e.From, e.To)
		}
		if utf8.RuneCountInString(e.Label) > MaxDiagramLabel {
			return nil, nil, bad("edge %s→%s label is longer than %d characters", e.From, e.To, MaxDiagramLabel)
		}
		switch e.Style {
		case "", "solid":
			e.Style = "solid"
		case "dashed", "dotted":
			e.Style = "dashed"
		default:
			return nil, nil, bad("edge style %q", e.Style)
		}
		if nodes[e.From] == "diamond" && e.Label == "" {
			warn = append(warn, fmt.Sprintf("decision branch %s→%s has no label", e.From, e.To))
		}
		d.Edges[i] = e
	}
	if d.Groups == nil {
		d.Groups = []DGroup{}
	}
	if d.Edges == nil {
		d.Edges = []DEdge{}
	}
	return &d, warn, nil
}

// DiagramJSONSchema is passed to the runner (--json-schema / codex
// --output-schema). Every property is required (strict structured output);
// optional texts are empty strings.
var DiagramJSONSchema = json.RawMessage(`{"type":"object","additionalProperties":false,
"required":["schema","title","direction","nodes","edges","groups"],
"properties":{
 "schema":{"type":"string","enum":["nb.diagram/v1"]},
 "title":{"type":"string","maxLength":80},
 "direction":{"type":"string","enum":["TB","LR"]},
 "nodes":{"type":"array","minItems":1,"maxItems":40,"items":{"type":"object","additionalProperties":false,
   "required":["id","label","note","shape","group"],
   "properties":{"id":{"type":"string","pattern":"^[a-z0-9_-]{1,32}$"},"label":{"type":"string","maxLength":48},
     "note":{"type":"string","maxLength":80},"shape":{"type":"string","enum":["box","round","ellipse","diamond"]},
     "group":{"type":"string"}}}},
 "edges":{"type":"array","maxItems":80,"items":{"type":"object","additionalProperties":false,
   "required":["from","to","label","style"],
   "properties":{"from":{"type":"string"},"to":{"type":"string"},"label":{"type":"string","maxLength":48},
     "style":{"type":"string","enum":["solid","dashed"]}}}},
 "groups":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,
   "required":["id","label"],"properties":{"id":{"type":"string","pattern":"^[a-z0-9_-]{1,32}$"},"label":{"type":"string","maxLength":48}}}}
}}`)

// Diagram subjects and effort ranges (§6).
var DiagramSubjects = map[string]string{
	"architecture":     "the system architecture: main components, services, stores and how they connect",
	"userFlow":         "the main user flow: screens or steps a user goes through, with decisions",
	"dataFlow":         "how data flows through the system from inputs to storage and outputs",
	"components":       "the main code components or modules and their dependencies",
	"requestLifecycle": "the lifecycle of a typical request from the client through the system and back",
	"stateFlow":        "the main states and the transitions between them",
	"custom":           "",
}

var diagramEffort = map[string][2]int{"quick": {6, 10}, "balanced": {8, 14}, "thorough": {10, 16}}

var (
	ErrBadSubject      = errors.New("unknown diagram subject")
	ErrBadStyle        = errors.New(`style must be "ink" or "color"`)
	ErrDiagramNotFound = errors.New("diagram not found")
)

// DiagramPrompt is the runner prompt.
func DiagramPrompt(subject, ask, effort string) string {
	r := diagramEffort[effort]
	what := DiagramSubjects[subject]
	if subject == "custom" {
		what = ask
	}
	return fmt.Sprintf("Draw a diagram of %s for the software project in the current folder.\n\n"+
		"Explore the repository with the read-only tools only (Read, Glob, Grep); do not modify anything.\n"+
		"Answer with ONE JSON object in the nb.diagram/v1 format and nothing else:\n"+
		`{"schema":"nb.diagram/v1","title":"…","direction":"TB" or "LR",`+"\n"+
		` "nodes":[{"id":"api","label":"API server","note":"Go net/http","shape":"box|round|ellipse|diamond","group":"backend"}],`+"\n"+
		` "edges":[{"from":"web","to":"api","label":"HTTPS","style":"solid|dashed"}],`+"\n"+
		` "groups":[{"id":"backend","label":"Backend"}]}`+"\n\n"+
		"Rules: %d–%d nodes (at most 40), at most 80 edges and 8 groups; ids match ^[a-z0-9_-]{1,32}$; labels at most 48 characters, "+
		"notes at most 80 (use \"\" when there is none); every edge points at existing node ids; use diamond for decisions and "+
		"label every edge leaving a diamond with its answer; label a few edges with a verb; use group \"\" for nodes without a group; "+
		"write in English.", what, r[0], r[1])
}

// DiagramRecord is the stored diagram.
type DiagramRecord struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Subject   string    `json:"subject"`
	Ask       string    `json:"ask,omitempty"`
	Effort    string    `json:"effort"`
	Style     string    `json:"style"`
	Agent     string    `json:"agent"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	State     string    `json:"state"` // running | done | failed | cancelled
	JobID     string    `json:"jobId"`
	Error     string    `json:"error,omitempty"`
	Warnings  []string  `json:"warnings,omitempty"`
	Diagram   *Diagram  `json:"diagram"`
}

// DiagramRequest is POST /api/projects/{id}/diagrams after resolution.
type DiagramRequest struct {
	Subject, Ask, Effort, Style, Agent string
	MachineID, Cwd, Command            string
	Env                                []string
}

// Diagrams stores diagram records and starts diagram jobs.
type Diagrams struct {
	st      *store.Store
	Submit  func(jobs.RunnerRequest) (jobs.Job, error)
	Publish func(v any)
	mu      sync.Mutex
}

func NewDiagrams(st *store.Store) *Diagrams { return &Diagrams{st: st} }

func (g *Diagrams) file(pid, id string) string { return filepath.Join("diagrams", pid, id+".json") }

func (g *Diagrams) put(r DiagramRecord) error {
	if err := os.MkdirAll(g.st.Path("diagrams", r.ProjectID), 0o700); err != nil {
		return err
	}
	return g.st.Save(g.file(r.ProjectID, r.ID), r)
}

// Get finds a diagram by id.
func (g *Diagrams) Get(id string) (DiagramRecord, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.getLocked(id)
}

func (g *Diagrams) getLocked(id string) (DiagramRecord, error) {
	if !idRe.MatchString(id) {
		return DiagramRecord{}, ErrDiagramNotFound
	}
	ents, _ := os.ReadDir(g.st.Path("diagrams"))
	for _, e := range ents {
		var r DiagramRecord
		if ok, err := g.st.Load(g.file(e.Name(), id), &r); ok && err == nil {
			return r, nil
		}
	}
	return DiagramRecord{}, ErrDiagramNotFound
}

// List returns a project's diagrams, newest first.
func (g *Diagrams) List(pid string) []DiagramRecord {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []DiagramRecord{}
	if !idRe.MatchString(pid) {
		return out
	}
	ents, _ := os.ReadDir(g.st.Path("diagrams", pid))
	for _, e := range ents {
		var r DiagramRecord
		if ok, err := g.st.Load(filepath.Join("diagrams", pid, e.Name()), &r); ok && err == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Generate records a running diagram and queues the runner job. The
// runner retries once when the output is not valid nb.diagram/v1.
func (g *Diagrams) Generate(pid string, req DiagramRequest) (DiagramRecord, jobs.Job, error) {
	if _, ok := DiagramSubjects[req.Subject]; !ok {
		return DiagramRecord{}, jobs.Job{}, ErrBadSubject
	}
	if _, ok := diagramEffort[req.Effort]; !ok {
		return DiagramRecord{}, jobs.Job{}, ErrBadEffort
	}
	if req.Style == "" {
		req.Style = "ink"
	}
	if req.Style != "ink" && req.Style != "color" {
		return DiagramRecord{}, jobs.Job{}, ErrBadStyle
	}
	req.Ask = strings.TrimSpace(req.Ask)
	if (req.Subject == "custom" && req.Ask == "") || len(req.Ask) > maxAsk {
		return DiagramRecord{}, jobs.Job{}, ErrBadAsk
	}
	if !idRe.MatchString(pid) {
		return DiagramRecord{}, jobs.Job{}, ErrDiagramNotFound
	}
	r := DiagramRecord{ID: auth.RandomID("dg_"), ProjectID: pid, Subject: req.Subject, Ask: req.Ask, Effort: req.Effort,
		Style: req.Style, Agent: req.Agent, CreatedAt: time.Now().UTC(), State: "running"}
	g.mu.Lock()
	err := g.put(r)
	g.mu.Unlock()
	if err != nil {
		return DiagramRecord{}, jobs.Job{}, err
	}
	j, err := g.Submit(jobs.RunnerRequest{Purpose: "diagram", ProjectID: pid, MachineID: req.MachineID, Cwd: req.Cwd,
		Agent: req.Agent, Command: req.Command, Env: req.Env, Prompt: DiagramPrompt(req.Subject, req.Ask, req.Effort),
		Schema: DiagramJSONSchema,
		Transform: func(text string, structured json.RawMessage) (json.RawMessage, error) {
			raw := []byte(structured)
			if len(raw) == 0 {
				raw = []byte(extractObject(text))
			}
			d, warn, err := ValidateDiagram(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(struct {
				*Diagram
				Warnings []string `json:"warnings,omitempty"`
			}{d, warn})
		},
		OnFinish: func(j jobs.Job) { g.finish(pid, r.ID, j) }})
	g.mu.Lock()
	defer g.mu.Unlock()
	cur, gerr := g.getLocked(r.ID)
	if gerr != nil {
		cur = r
	}
	if err != nil {
		_ = os.Remove(g.st.Path(g.file(pid, r.ID)))
		return DiagramRecord{}, jobs.Job{}, err
	}
	cur.JobID = j.ID
	_ = g.put(cur)
	return cur, j, nil
}

func (g *Diagrams) finish(pid, id string, j jobs.Job) {
	g.mu.Lock()
	r, err := g.getLocked(id)
	if err != nil {
		g.mu.Unlock()
		return
	}
	r.JobID = j.ID
	switch j.State {
	case jobs.StateDone:
		var out struct {
			Diagram
			Warnings []string `json:"warnings"`
		}
		if json.Unmarshal(j.Result, &out) == nil {
			d := out.Diagram
			r.Diagram, r.Warnings, r.Title, r.State = &d, out.Warnings, d.Title, "done"
		} else {
			r.State, r.Error = "failed", "could not read the job result"
		}
	case jobs.StateCancelled:
		r.State = "cancelled"
	default:
		r.State, r.Error = "failed", j.Error
	}
	_ = g.put(r)
	g.mu.Unlock()
	if g.Publish != nil {
		g.Publish(map[string]any{"type": "diagram", "projectId": pid, "diagramId": id, "state": r.State})
	}
}

// extractObject returns the first {...} block of s (output wrapped in
// prose or a code fence).
func extractObject(s string) string {
	i := strings.IndexByte(s, '{')
	k := strings.LastIndexByte(s, '}')
	if i < 0 || k < i {
		return s
	}
	return s[i : k+1]
}

// Recover marks diagrams that were running when the hub stopped as failed
// (their jobs come back as hub_restart).
func (g *Diagrams) Recover() {
	g.mu.Lock()
	defer g.mu.Unlock()
	ents, _ := os.ReadDir(g.st.Path("diagrams"))
	for _, e := range ents {
		files, _ := os.ReadDir(g.st.Path("diagrams", e.Name()))
		for _, f := range files {
			var r DiagramRecord
			if ok, err := g.st.Load(filepath.Join("diagrams", e.Name(), f.Name()), &r); ok && err == nil && r.State == "running" {
				r.State, r.Error = "failed", "the hub restarted before the diagram was done"
				_ = g.put(r)
			}
		}
	}
}
