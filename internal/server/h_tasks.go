package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"nextboard/internal/jobs"
	"nextboard/internal/machine"
	"nextboard/internal/studio"
)

// Tasks of a project (docs/API-studio.md §7.12): only what its files name can run.

const taskTimeout = 30 * time.Minute

func (s *Server) routesTasks(authed func(string, handler)) {
	authed("GET /api/projects/{id}/tasks", s.handleTasks)
	authed("POST /api/projects/{id}/tasks/run", s.handleTaskRun)
}

func (s *Server) taskRoot(w http.ResponseWriter, r *http.Request) (machine.Host, string, string, bool) {
	rp, ok := s.repo(w, r)
	if !ok {
		return nil, "", "", false
	}
	h, ok := s.machines.Host(rp.MachineID)
	if !ok {
		writeErrCode(w, http.StatusNotFound, "machine_not_found", machine.ErrNotFound.Error())
		return nil, "", "", false
	}
	return h, path.Join(rp.Path, rp.Sub), rp.MachineID, true
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request, p *principal) {
	h, root, _, ok := s.taskRoot(w, r)
	if !ok {
		return
	}
	list, err := studio.DetectTasks(r.Context(), h, root)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) handleTaskRun(w http.ResponseWriter, r *http.Request, p *principal) {
	h, root, machineID, ok := s.taskRoot(w, r)
	if !ok {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &req) {
		return
	}
	list, err := studio.DetectTasks(r.Context(), h, root)
	if err != nil {
		fsErr(w, err)
		return
	}
	var task *studio.Task
	for i := range list {
		if list[i].ID == req.ID {
			task = &list[i]
		}
	}
	if task == nil {
		writeErrCode(w, http.StatusNotFound, "task_not_found", "the project does not name this task any more")
		return
	}
	j, err := s.jobs.SubmitFunc(jobs.FuncJob{Kind: "task", Purpose: task.ID, ProjectID: r.PathValue("id"), MachineID: machineID,
		Run: func(ctx context.Context, rep *jobs.Reporter) (json.RawMessage, error) {
			return runTask(ctx, h, path.Join(root, task.Cwd), task.Command, rep)
		}})
	if err != nil {
		writeErrCode(w, http.StatusServiceUnavailable, "task_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func runTask(ctx context.Context, h machine.Host, cwd, command string, rep *jobs.Reporter) (json.RawMessage, error) {
	rep.Output("$ " + command + "\n")
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Cwd: cwd, Script: "exec 2>&1; " + command, Timeout: taskTimeout, MaxOutput: 1 << 20,
		OnStdout: func(b []byte) { rep.Output(strings.ToValidUTF8(string(b), "�")) }})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, &jobs.FuncError{Code: "task_failed", Msg: err.Error()}
	}
	switch {
	case res.Cancelled:
		return nil, context.Canceled
	case res.TimedOut:
		return nil, &jobs.FuncError{Code: "timeout", Msg: "the task ran longer than " + taskTimeout.String()}
	case res.ExitCode != 0:
		return nil, &jobs.FuncError{Code: "task_failed", Msg: fmt.Sprintf("exit code %d", res.ExitCode)}
	}
	return json.RawMessage(`{"exitCode":0}`), nil
}
