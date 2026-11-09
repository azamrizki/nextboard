package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/nodedap"
	"github.com/azamrizki/nextboard/internal/studio"
)

// Attaching the debugger to a node process that already runs (docs/API-studio.md §7.5). The
// adapter stays on This PC, like the agents, and only the inspector's bytes come from the
// project's machine.

func nodeInspectErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, studio.ErrNoNodeProcess):
		writeErrCode(w, http.StatusNotFound, "no_process", err.Error())
	case errors.Is(err, studio.ErrNotNodeProcess):
		writeErrCode(w, http.StatusConflict, "not_node", err.Error())
	case errors.Is(err, studio.ErrNoProcFS):
		writeErrCode(w, http.StatusNotImplemented, "no_procfs", err.Error())
	case errors.Is(err, studio.ErrNotNodeInspector):
		writeErrCode(w, http.StatusConflict, "not_inspector", err.Error())
	case errors.Is(err, studio.ErrInspectorFailed):
		writeErrCode(w, http.StatusBadGateway, "inspector_failed", err.Error())
	case errors.Is(err, machine.ErrForwardingDisabled):
		writeErrCode(w, http.StatusBadGateway, "forwarding_disabled", "this server does not allow SSH port forwarding (AllowTcpForwarding)")
	default:
		fsErr(w, err)
	}
}

func (s *Server) handleNodeProcesses(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesTimeout)
	defer cancel()
	items, err := studio.NodeProcesses(ctx, h, pr.Path)
	if err != nil {
		nodeInspectErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleNodeInspect takes a process, whose inspector it finds or opens, or a port to check.
func (s *Server) handleNodeInspect(w http.ResponseWriter, r *http.Request, p *principal) {
	pr, h, ok := s.projectHost(w, r)
	if !ok {
		return
	}
	var req struct {
		PID  int `json:"pid"`
		Port int `json:"port"`
	}
	if !decode(w, r, &req) {
		return
	}
	if (req.PID == 0) == (req.Port == 0) {
		writeErrCode(w, http.StatusBadRequest, "bad_request", "give a process or a port")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesTimeout)
	defer cancel()
	if req.Port != 0 {
		if err := studio.CheckNodeInspector(ctx, h, req.Port); err != nil {
			nodeInspectErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, studio.NodeInspector{Port: req.Port})
		return
	}
	in, err := studio.OpenNodeInspector(ctx, h, pr.Path, req.PID)
	if err != nil {
		nodeInspectErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// bridgeNodeInspector runs the adapter on This PC, reaching the inspector through a unix socket
// in a private folder. A TCP port here would let every login on this machine run code in the
// process on the server.
func (s *Server) bridgeNodeInspector(w http.ResponseWriter, r *http.Request, p *principal, h machine.Host, q string) {
	port, err := strconv.Atoi(q)
	if err != nil || port < 1 || port > 65535 {
		writeErrCode(w, http.StatusBadRequest, "bad_port", "that port is not valid")
		return
	}
	local, ok := s.machines.Host(machine.LocalID)
	if !ok {
		writeErrCode(w, http.StatusNotFound, "machine_not_found", machine.ErrNotFound.Error())
		return
	}
	dir, err := os.MkdirTemp("", "nb-dap-")
	if err != nil {
		writeErrCode(w, http.StatusBadGateway, "adapter_failed", err.Error())
		return
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "i")
	l, err := net.Listen("unix", sock)
	if err != nil {
		writeErrCode(w, http.StatusBadGateway, "adapter_failed", err.Error())
		return
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go forwardInspector(ctx, h, port, c)
		}
	}()
	proc, err := machine.StartPipe(local, machine.ExecSpec{Argv: studio.NodeAdapterArgv(), Env: []string{nodedap.InspectSocketEnv + "=" + sock}})
	if err != nil {
		fsErr(w, err)
		return
	}
	// The adapter may have an inspector to close, so its input ends first and it gets a moment.
	// Runs before the deferred calls above: it still needs the socket for that.
	defer func() {
		_ = proc.Stdin.Close()
		select {
		case <-proc.Done():
		case <-time.After(3 * time.Second):
		}
		proc.Stop()
	}()
	s.bridgeFrames(w, r, p, "debug adapter", proc.Stdin, proc.Stdout, proc.StderrTail)
}

func forwardInspector(ctx context.Context, h machine.Host, port int, c net.Conn) {
	defer c.Close()
	up, err := machine.DialLoopback(ctx, h, port)
	if err != nil {
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
