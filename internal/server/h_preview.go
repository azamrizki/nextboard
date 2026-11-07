package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/preview"
)

// Web previews (docs/API-studio.md §4, B17–B19).

func previewErr(w http.ResponseWriter, err error) {
	var pe *preview.Error
	if errors.As(err, &pe) {
		writeErrCode(w, pe.Status, pe.Code, pe.Msg)
		return
	}
	writeErrCode(w, http.StatusInternalServerError, "preview_failed", err.Error())
}

// handlePorts probes loopback ports of a machine (≤ 20, parallel).
func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErrCode(w, http.StatusNotFound, "machine_not_found", machine.ErrNotFound.Error())
		return
	}
	if !h.Connected() {
		writeErrCode(w, http.StatusServiceUnavailable, "machine_offline", machine.ErrNotConnected.Error())
		return
	}
	ports, err := preview.ParsePorts(r.URL.Query().Get("probe"))
	if err != nil {
		previewErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, preview.ProbePorts(ctx, func(ctx context.Context, port int) (net.Conn, error) {
		return machine.DialLoopback(ctx, h, port)
	}, ports))
}

// handleListening says which TCP ports of a machine something listens on.
func (s *Server) handleListening(w http.ResponseWriter, r *http.Request, p *principal) {
	h, ok := s.machines.Host(r.PathValue("id"))
	if !ok {
		writeErrCode(w, http.StatusNotFound, "machine_not_found", machine.ErrNotFound.Error())
		return
	}
	if !h.Connected() {
		writeErrCode(w, http.StatusServiceUnavailable, "machine_offline", machine.ErrNotConnected.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Script: "exec /bin/sh -c " + machine.Quote(preview.ListeningScript), Timeout: 8 * time.Second})
	if err != nil {
		writeErrCode(w, http.StatusBadGateway, "ports_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": preview.ParseListening(string(res.Stdout))})
}

func (s *Server) handlePreviews(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.gitPrev.previews.List())
}

func (s *Server) handlePreviewCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req preview.CreateRequest
	if !decode(w, r, &req) {
		return
	}
	if req.MachineID == "" {
		req.MachineID = machine.LocalID
	}
	if _, ok := s.machines.Host(req.MachineID); !ok {
		writeErrCode(w, http.StatusNotFound, "machine_not_found", machine.ErrNotFound.Error())
		return
	}
	if req.SessionID != "" {
		if _, ok := s.sessions.Info(req.SessionID); !ok {
			req.SessionID = ""
		}
	}
	req.UserAgent = r.UserAgent()
	pv, lp, created, err := s.gitPrev.previews.Create(req)
	if err != nil {
		previewErr(w, err)
		return
	}
	s.gitPrev.startReaper(s)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"preview": pv, "launchPath": lp})
}

// startReaper runs the idle reaper once a preview exists.
func (g *gitPreview) startReaper(*Server) { g.reaper.Do(g.previews.Start) }

func (s *Server) handlePreviewLaunch(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Path string `json:"path"`
	}
	if !optional(w, r, &req) {
		return
	}
	lp, exp, err := s.gitPrev.previews.Launch(r.PathValue("id"), req.Path)
	if err != nil {
		previewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"launchPath": lp, "expiresAt": exp})
}

func (s *Server) handlePreviewDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.gitPrev.previews.Delete(r.PathValue("id")); err != nil {
		previewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handlePreviewLogs(w http.ResponseWriter, r *http.Request, p *principal) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	items, seq, err := s.gitPrev.previews.Logs(r.PathValue("id"), since)
	if err != nil {
		previewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "seq": seq})
}

// webData is the board data of a web node.
type webData struct {
	URL       string `json:"url"`
	PreviewID string `json:"previewId,omitempty"`
	MachineID string `json:"machineId,omitempty"`
	Port      int    `json:"port,omitempty"`
	Device    string `json:"device,omitempty"`
	Zoom      int    `json:"zoom,omitempty"`
	Auto      bool   `json:"auto"`
}

// previewReferenced reports whether any board has a web node pointing at
// the preview (the idle reaper keeps those listeners).
func (s *Server) previewReferenced(id string) bool {
	for _, pr := range s.projects.List() {
		for _, ws := range pr.Workspaces {
			b, err := s.boards.Get(ws.ID)
			if err != nil {
				continue
			}
			for _, n := range b.Nodes {
				if n.Kind != "web" {
					continue
				}
				var d webData
				if json.Unmarshal(n.Data, &d) == nil && d.PreviewID == id {
					return true
				}
			}
		}
	}
	return false
}

// autoPreview handles a detected localhost URL (B18): a preview, a web
// node right of the console's terminal and a cyan "preview" edge, unless
// that workspace already shows the preview.
func (s *Server) autoPreview(f preview.Found) bool {
	in, ok := s.sessions.Info(f.SessionID)
	if !ok || in.WorkspaceID == "" {
		return true // nothing to place; don't retry
	}
	pv, _, _, err := s.gitPrev.previews.Create(preview.CreateRequest{MachineID: f.MachineID, Port: f.Port, Scheme: f.Scheme,
		Path: f.Path, SessionID: f.SessionID, Auto: true})
	if err != nil {
		return false // e.g. bad_path: let the next print of the URL retry
	}
	s.gitPrev.startReaper(s)
	b, err := s.boards.Get(in.WorkspaceID)
	if err != nil {
		return true
	}
	var term *board.Node
	for _, n := range b.Nodes {
		switch {
		case n.Kind == board.KindTerminal && n.SessionID() == in.ID:
			term = n
		case n.Kind == "web":
			var d webData
			if json.Unmarshal(n.Data, &d) == nil && (d.PreviewID == pv.ID || (d.MachineID == f.MachineID && d.Port == f.Port)) {
				return true // already on this board
			}
		}
	}
	x, y, w, h := 0.0, 0.0, 960.0, 640.0
	if term != nil {
		x, y = term.X+term.W+60, term.Y
	}
	x = min(x, board.MaxCoord-w-1)
	data, _ := json.Marshal(webData{URL: f.URL, PreviewID: pv.ID, MachineID: f.MachineID, Port: f.Port, Device: "fit", Zoom: 100, Auto: true})
	nid := auth.RandomID("n_")
	ops := []board.Op{{Op: "put", Node: &board.Node{ID: nid, Kind: "web", X: x, Y: y, W: w, H: h, Data: data}}}
	if term != nil {
		ops = append(ops, board.Op{Op: "putEdge", Edge: &board.Edge{ID: auth.RandomID("e_"), Kind: "preview", Source: term.ID, Target: nid, Data: json.RawMessage(`{}`)}})
	}
	if _, err := s.boards.Apply(in.WorkspaceID, "", ops); err != nil {
		return true
	}
	s.events.Publish(map[string]any{"type": "previewSuggested", "sessionId": in.ID, "previewId": pv.ID, "url": f.URL,
		"machineId": f.MachineID, "port": f.Port, "workspaceId": in.WorkspaceID, "nodeId": nid})
	return true
}

// previewOpener lets the command bar open a preview for "open localhost
// <port>" so the web tile it adds already carries its previewId.
type previewOpener struct{ s *Server }

func (o previewOpener) OpenPreview(machineID string, port int) (string, error) {
	if o.s.gitPrev == nil {
		return "", errors.New("preview not available")
	}
	pv, _, _, err := o.s.gitPrev.previews.Create(preview.CreateRequest{MachineID: machineID, Port: port})
	if err != nil {
		return "", err
	}
	return pv.ID, nil
}
