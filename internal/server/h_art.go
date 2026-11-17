package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/art"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
)

// "Create with AI" for a project's cover and icon (docs/API-board.md §1): an image tool on This
// PC (Codex's built-in image generation, with the default login profile) makes one image from a
// brief of the project's own files. The brief goes to the tool's model; the reply is not trusted.

const (
	// artCapFor: how long the answer of GET /api/art/capability is kept.
	artCapFor = time.Minute
	// KindArt is the job kind of an art job (purpose projectArt).
	jobKindArt    = "art"
	jobPurposeArt = "projectArt"
)

// artState caches the capability and knows which art jobs run.
type artState struct {
	mu      sync.Mutex
	at      time.Time
	cap     art.Capability
	running map[string]string // project + "/" + kind → job id
	// one image at a time: an image found by "newest in the folder" is then the job's own
	slot chan struct{}
	// tools, when set, replaces the hub's list (tests).
	tools func() []art.Tool
	// timeout, when set, bounds one image instead of the tool's own limit (tests).
	timeout time.Duration
}

func newArtState() *artState {
	return &artState{running: map[string]string{}, slot: make(chan struct{}, 1)}
}

func (a *artState) reset() {
	a.mu.Lock()
	a.at = time.Time{}
	a.mu.Unlock()
}

// forget drops what a deleted project had running.
func (a *artState) forget(pid string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	for _, k := range []string{art.KindCover, art.KindIcon} {
		delete(a.running, pid+"/"+k)
	}
	a.mu.Unlock()
}

// artTools is the list of image tools, in order: Codex only for now.
func (s *Server) artTools() []art.Tool {
	if s.art.tools != nil {
		return s.art.tools()
	}
	local := s.machines.Local()
	c := art.Codex{Host: local, Command: s.settings.View().Agents.Paths[machine.LocalID]["codex"], Env: s.defaultProfileEnv("codex"),
		Home: homeDir(), Timeout: s.art.timeout}
	if a, ok := s.agents.Get("codex"); ok {
		c.Detected = s.available(local, a)
		if s.agentOff(a) {
			c.Off = "Codex is turned off in Settings › Agents"
		}
	}
	return []art.Tool{c}
}

// artCapability is the cached answer: whether an image tool is there now.
func (s *Server) artCapability(ctx context.Context) art.Capability {
	s.art.mu.Lock()
	if !s.art.at.IsZero() && time.Since(s.art.at) < artCapFor {
		c := s.art.cap
		s.art.mu.Unlock()
		return c
	}
	s.art.mu.Unlock()
	_, c := art.Pick(ctx, s.artTools())
	if c.Tool == "" {
		c.Tool = "codex"
	}
	s.art.mu.Lock()
	s.art.cap, s.art.at = c, time.Now()
	s.art.mu.Unlock()
	return c
}

func (s *Server) handleArtCapability(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.artCapability(r.Context()))
}

// handleProjectArt starts a job that makes the project's cover or icon (202 Job).
func (s *Server) handleProjectArt(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Kind string `json:"kind"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Kind != art.KindCover && req.Kind != art.KindIcon {
		writeErrCode(w, http.StatusBadRequest, "bad_kind", `kind must be "cover" or "icon"`)
		return
	}
	pr, ok := s.projects.Get(r.PathValue("id"))
	if !ok {
		writeErrCode(w, http.StatusNotFound, "project_not_found", project.ErrNotFound.Error())
		return
	}
	c := s.artCapability(r.Context())
	if !c.Available {
		writeErrCode(w, http.StatusPreconditionFailed, "no_image_tool", c.Reason)
		return
	}
	key := pr.ID + "/" + req.Kind
	s.art.mu.Lock()
	if id, ok := s.art.running[key]; ok {
		if j, found := s.jobs.Get(id); id == "" || found && j.Active() {
			s.art.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{"error": "an image of this kind is already being made for this project",
				"code": "art_running", "jobId": id})
			return
		}
	}
	s.art.running[key] = "" // held while the job is made
	s.art.mu.Unlock()
	kind := req.Kind
	j, err := s.jobs.SubmitFunc(jobs.FuncJob{
		Kind: jobKindArt, Purpose: jobPurposeArt, ProjectID: pr.ID, MachineID: machine.LocalID, Agent: c.Tool,
		Run: func(ctx context.Context, rep *jobs.Reporter) (json.RawMessage, error) {
			return s.runArt(ctx, rep, pr.ID, kind)
		},
		OnFinish: func(j jobs.Job) {
			s.art.mu.Lock()
			if s.art.running[key] == j.ID {
				delete(s.art.running, key)
			}
			s.art.mu.Unlock()
		},
	})
	s.art.mu.Lock()
	if err != nil {
		delete(s.art.running, key)
	} else if cur, ok := s.art.running[key]; ok && cur == "" {
		s.art.running[key] = j.ID
		if fresh, ok := s.jobs.Get(j.ID); ok && !fresh.Active() {
			delete(s.art.running, key) // it ended before it was noted
		}
	}
	s.art.mu.Unlock()
	if err != nil {
		runnerErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

// runArt is the art job: wait for the one slot, read the project, make the image, store it.
func (s *Server) runArt(ctx context.Context, rep *jobs.Reporter, pid, kind string) (json.RawMessage, error) {
	rep.Progress("queued", "Waiting for another image…")
	select {
	case s.art.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.art.slot }()
	pr, ok := s.projects.Get(pid)
	if !ok {
		return nil, &jobs.FuncError{Code: "project_not_found", Msg: project.ErrNotFound.Error()}
	}
	tool, c := art.Pick(ctx, s.artTools())
	if tool == nil {
		return nil, &jobs.FuncError{Code: "no_image_tool", Msg: c.Reason}
	}
	img, err := art.Make(ctx, tool, kind, s.artSource(pr), rep.Progress)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		return nil, &jobs.FuncError{Code: art.Code(err), Msg: err.Error()}
	}
	id, err := s.storeAsset(img.Data)
	if err != nil {
		return nil, err
	}
	if _, err := s.projects.SetImage(pid, kind, &project.Image{AssetID: id, Source: project.ImageAI, At: time.Now().UTC()}); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			return nil, &jobs.FuncError{Code: "project_not_found", Msg: "the project was deleted meanwhile"}
		}
		return nil, err
	}
	s.emitProject(pid)
	return json.Marshal(map[string]any{"kind": kind, "assetId": id, "url": "/api/assets/" + id, "type": img.Type, "w": img.Width, "h": img.Height})
}

// artSource is what the brief reads: the name, the parts' names, every part's folder (on a
// server through its connection; a part whose machine is not connected is left out) and the
// main project memory.
func (s *Server) artSource(pr project.Project) art.Source {
	src := art.Source{Name: pr.Name}
	parts := pr.Parts
	if len(parts) == 0 {
		parts = []project.Part{{MachineID: pr.MachineID, Path: pr.Path}}
	}
	for _, pt := range parts {
		if pt.Name != "" {
			src.Parts = append(src.Parts, pt.Name)
		}
		if h, ok := s.machines.Host(pt.MachineID); ok && h.Connected() {
			src.Folders = append(src.Folders, art.Folder{Part: pt.Name, Host: h, Path: pt.Path})
		}
	}
	if s.orch != nil {
		if doc, err := s.orch.Memory.Read("project", pr.ID); err == nil {
			src.Memory = doc.Content
		}
	}
	return src
}
