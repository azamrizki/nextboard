package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/profiles"
	"github.com/azamrizki/nextboard/internal/settings"
)

// localEnvVar reads the hub's own environment for This PC (consoles
// inherit it), "" for other machines.
func localEnvVar(h machine.Host, k string) string {
	if h.ID() != machine.LocalID {
		return ""
	}
	return os.Getenv(k)
}

// ---- settings (§11) ----

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.settings.Get())
}

func (s *Server) handleSettingsPatch(w http.ResponseWriter, r *http.Request, p *principal) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErrCode(w, http.StatusRequestEntityTooLarge, "body_too_large", "body too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "cannot read the body")
		return
	}
	doc, err := s.settings.Patch(body)
	var se *settings.Error
	switch {
	case errors.As(err, &se):
		writeErrCode(w, http.StatusBadRequest, se.Code, se.Msg)
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, doc)
	}
}

// ---- profiles (§10) ----

// profileView fills Exists with a short fs/stat on the profile's machine.
func (s *Server) profileViews(ctx context.Context, list []profiles.Profile) []profiles.Profile {
	var wg sync.WaitGroup
	for i := range list {
		h, ok := s.machines.Host(list[i].MachineID)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if st, err := h.Stat(c, list[i].Dir); err == nil {
				list[i].Exists = st.Exists && st.Dir
			}
		}(i)
	}
	wg.Wait()
	return list
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.profileViews(r.Context(), s.profiles.List(r.URL.Query().Get("machineId"))))
}

func (s *Server) handleProfileCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		MachineID string `json:"machineId"`
		Agent     string `json:"agent"`
		Name      string `json:"name"`
		Dir       string `json:"dir"`
		Create    bool   `json:"create"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.MachineID == "" {
		req.MachineID = machine.LocalID
	}
	h, ok := s.machines.Host(req.MachineID)
	if !ok {
		writeErr(w, http.StatusNotFound, machine.ErrNotFound.Error())
		return
	}
	a, ok := s.agents.Get(req.Agent)
	if !ok || a.ProfileEnv == "" {
		writeErrCode(w, http.StatusBadRequest, "profile_unsupported", "this agent does not support login profiles")
		return
	}
	if _, err := profiles.CleanName(req.Name); err != nil {
		writeErrCode(w, http.StatusBadRequest, "bad_profile", err.Error())
		return
	}
	local := req.MachineID == machine.LocalID
	if req.Create && !local {
		writeErrCode(w, http.StatusForbidden, "remote_create_forbidden", "the hub does not make profile folders on a remote machine; pick an existing folder")
		return
	}
	if !req.Create {
		if req.Dir == "" {
			writeErrCode(w, http.StatusBadRequest, "profile_dir_missing", "dir is required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		st, err := h.Stat(ctx, req.Dir)
		if err != nil {
			fsErr(w, err)
			return
		}
		if !st.Exists || !st.Dir {
			writeErrCode(w, http.StatusBadRequest, "profile_dir_missing", "profile folder does not exist: "+st.Path)
			return
		}
		req.Dir = st.Path
	}
	pf, err := s.profiles.Add(req.MachineID, a.ID, req.Name, req.Dir, req.Create)
	if err == nil {
		s.noLSPOffer(pf)
	}
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "bad_profile", err.Error())
		return
	}
	pf.Exists = true
	s.events.Publish(map[string]any{"type": "profile", "profile": pf})
	writeJSON(w, http.StatusCreated, pf)
}

func (s *Server) handleProfileUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	pf, err := s.profiles.Rename(r.PathValue("id"), req.Name)
	switch {
	case errors.Is(err, profiles.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "profile_not_found", err.Error())
	case err != nil:
		writeErrCode(w, http.StatusBadRequest, "bad_profile", err.Error())
	default:
		pf = s.profileViews(r.Context(), []profiles.Profile{pf})[0]
		s.events.Publish(map[string]any{"type": "profile", "profile": pf})
		writeJSON(w, http.StatusOK, pf)
	}
}

// handleProfileDelete removes the reference; ?purge=1 also deletes a
// hub-created local folder. Remote folders are never touched.
func (s *Server) handleProfileDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	purge := r.URL.Query().Get("purge") == "1"
	id := r.PathValue("id")
	if pf, ok := s.profiles.Get(id); ok && pf.MachineID != machine.LocalID {
		purge = false
	}
	if _, err := s.profiles.Delete(id, purge); err != nil {
		if errors.Is(err, profiles.ErrIsHome) {
			writeErrCode(w, http.StatusConflict, "profile_home", err.Error())
			return
		}
		writeErrCode(w, http.StatusNotFound, "profile_not_found", err.Error())
		return
	}
	s.events.Publish(map[string]any{"type": "profileRemoved", "id": id})
	writeJSON(w, http.StatusOK, okBody)
}

// ---- jobs (API-studio.md §1.2) ----

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request, p *principal) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	writeJSON(w, http.StatusOK, map[string]any{"items": s.jobs.List(q.Get("projectId"), q.Get("active") == "1", limit)})
}

func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request, p *principal) {
	j, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeErrCode(w, http.StatusNotFound, "job_not_found", jobs.ErrNotFound.Error())
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request, p *principal) {
	j, err := s.jobs.Cancel(r.PathValue("id"))
	if err != nil {
		writeErrCode(w, http.StatusNotFound, "job_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, j)
}
