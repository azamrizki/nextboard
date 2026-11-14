package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"

	"github.com/azamrizki/nextboard/internal/session"
)

// serveDebug serves the Go profiles and a few numbers of the running hub on a loopback address
// (`serve --debug-listen 127.0.0.1:6060`). It is off unless asked for: a profile shows what the
// hub holds in memory. It answers only on a loopback address and only to a loopback Host header,
// so a web page in a browser on the same PC cannot read it through a name that points here.
//
//	go tool pprof http://127.0.0.1:6060/debug/pprof/heap
//	curl http://127.0.0.1:6060/debug/hub
func (s *Server) serveDebug(addr string) (stop func(), err error) {
	if addr == "" {
		return func() {}, nil
	}
	if !isLoopbackListen(addr) {
		return nil, errors.New("--debug-listen takes a loopback address such as 127.0.0.1:6060: profiles show what the hub holds in memory")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", http.NotFound) // the command line is nobody's business
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("/debug/hub", s.handleDebugHub)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isLoopbackListen(net.JoinHostPort(hostOnly(r.Host), "0")) || r.Header.Get("Origin") != "" {
				http.Error(w, "loopback only", http.StatusForbidden)
				return
			}
			mux.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	log.Printf("profiles at http://%s/debug/pprof/ (loopback only)", ln.Addr())
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}

// hostOnly is a Host header without its port.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// handleDebugHub: memory and counts of the running hub, in MB where it says so.
func (s *Server) handleDebugHub(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("gc") == "1" {
		runtime.GC()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	mb := func(b uint64) float64 { return float64(b>>10) / 1024 }
	consoles, running, asleep := 0, 0, 0
	for _, in := range s.sessions.List() {
		consoles++
		switch in.Status {
		case session.StatusAsleep:
			asleep++
		case session.StatusRunning:
			running++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"goroutines":     runtime.NumGoroutine(),
		"heapAllocMB":    mb(ms.HeapAlloc),
		"heapInuseMB":    mb(ms.HeapInuse),
		"heapIdleMB":     mb(ms.HeapIdle),
		"heapReleasedMB": mb(ms.HeapReleased),
		"stackMB":        mb(ms.StackInuse),
		"sysMB":          mb(ms.Sys),
		"heapObjects":    ms.HeapObjects,
		"gcRuns":         ms.NumGC,
		"nextGCMB":       mb(ms.NextGC),
		"consoles":       consoles,
		"running":        running,
		"asleep":         asleep,
		"eventClients":   s.events.Count(),
	})
}
