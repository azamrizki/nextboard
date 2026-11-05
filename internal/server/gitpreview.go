package server

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/gitsvc"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/preview"
	"github.com/azamrizki/nextboard/internal/session"
)

// Phase 7 part A wiring (docs/API-studio.md §3–§4): the Git service, the
// preview proxy and auto-preview detection. Handlers live in h_git.go and
// h_preview.go.

type gitPreview struct {
	git      *gitsvc.Service
	previews *preview.Manager
	detect   *preview.Detector
	reaper   sync.Once
}

// setupGitPreview runs after setupOrchestration (it chains the session
// output and removal callbacks and plugs git into the command bar).
func (s *Server) setupGitPreview() {
	g := &gitPreview{git: gitsvc.New()}
	g.git.Host = s.machines.Host
	g.git.Jobs = s.jobs
	g.git.Publish = s.events.Publish
	g.git.Runner = s.submitRunner
	g.git.RunnerAgent = func(machineID string) (string, string) {
		set := s.settings.Get()
		a := set.Runner.DefaultAgent
		if a == "" {
			a = "claude"
		}
		return a, set.Agents.Paths[machineID][a]
	}
	pr := s.settings.Get().Previews.PortRange
	hubPort := listenPort(s.listen)
	g.previews = preview.New(preview.Config{
		BindHost: listenHost(s.listen),
		PortMin:  pr[0], PortMax: pr[1],
		LocalID: machine.LocalID,
		LocalReserved: func(port int) bool {
			return (hubPort != 0 && port == hubPort) || (s.hookLn != nil && port == listenPort(s.hookLn.Addr().String()))
		},
		Dial: func(ctx context.Context, machineID string, port int) (net.Conn, error) {
			h, ok := s.machines.Host(machineID)
			if !ok {
				return nil, machine.ErrNotFound
			}
			return machine.DialLoopback(ctx, h, port)
		},
		HubOrigins: s.hubOrigins,
		Publish:    s.events.Publish,
		Referenced: s.previewReferenced,
	})
	g.detect = &preview.Detector{
		Enabled: func() bool { return s.settings.Get().Previews.Auto },
		Session: func(sid string) (string, string, bool) {
			in, ok := s.sessions.Info(sid)
			if !ok {
				return "", "", false
			}
			return in.MachineID, in.StartedAt.UTC().Format(time.RFC3339Nano), true
		},
		Probe: func(ctx context.Context, machineID string, port int) bool {
			h, ok := s.machines.Host(machineID)
			return ok && machine.ProbePort(ctx, h, port, preview.ProbeTimeout)
		},
		Allowed: func(machineID string, port int) bool { return g.previews.CheckPort(machineID, port) == nil },
		OnFound: s.autoPreview,
	}
	s.gitPrev = g
	if prev := s.sessions.OnOutput; prev != nil {
		s.sessions.OnOutput = func(sid string, clean []byte) {
			prev(sid, clean)
			g.detect.Feed(sid, clean)
		}
	} else {
		s.sessions.OnOutput = g.detect.Feed
	}
	if prev := s.sessions.OnRemove; prev != nil {
		s.sessions.OnRemove = func(sp session.Spec) {
			prev(sp)
			g.detect.Forget(sp.ID)
		}
	}
	if s.orch != nil {
		s.orch.Git = gitActions{s}
		s.orch.Previews = previewOpener{s}
	}
}

// closeGitPreview shuts every preview listener.
func (s *Server) closeGitPreview() {
	if s.gitPrev != nil {
		s.gitPrev.previews.Close()
	}
}

// listenHost / listenPort split the hub's listen address.
func listenHost(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return h
}

func listenPort(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// hubOrigins are the origins of the hub UI that may frame a preview and
// talk to its bridge, for a request on host (hostname:previewPort) arriving
// with scheme. Behind the reverse proxy the UI is on the default port.
func (s *Server) hubOrigins(scheme, host string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(o string) {
		if o != "" && !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	if hn := hostnameOf(host); hn != "" {
		if strings.Contains(hn, ":") {
			hn = "[" + hn + "]"
		}
		add(scheme + "://" + hn)
		if p := listenPort(s.listen); p != 0 {
			add(scheme + "://" + joinHostPort(strings.Trim(hn, "[]"), p))
		}
	}
	for _, u := range s.urls() {
		if strings.HasPrefix(u, scheme+"://") {
			rest := u[len(scheme)+3:]
			if j := strings.IndexByte(rest, '/'); j >= 0 {
				rest = rest[:j]
			}
			add(scheme + "://" + rest)
		}
	}
	if s.dev {
		add("http://" + joinHostPort("localhost", s.devWeb))
		add("http://" + joinHostPort("127.0.0.1", s.devWeb))
	}
	return out
}

// hostnameOf strips the port of a Host header value.
func hostnameOf(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

func joinHostPort(h string, port int) string { return net.JoinHostPort(h, strconv.Itoa(port)) }

// spaCSP is the CSP of the web UI: frames may only load the preview
// listeners of the host the UI is served from (docs/API-studio.md §4.3).
func (s *Server) spaCSP(scheme, hostHeader string) string {
	frames := "'self'"
	if s.gitPrev != nil {
		if hn := hostnameOf(hostHeader); hn != "" && !strings.ContainsAny(hn, " ;,'\"") {
			lo, hi := s.gitPrev.previews.PortRange()
			var b strings.Builder
			b.WriteString("'self'")
			for p := lo; p <= hi; p++ {
				b.WriteString(" " + scheme + "://" + joinHostPort(hn, p))
			}
			frames = b.String()
		}
	}
	return "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-src " +
		frames + "; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
}

// gitRepo resolves a project to the git service's target.
func (s *Server) gitRepo(pid string) (gitsvc.Repo, bool) {
	p, ok := s.projects.Get(pid)
	if !ok {
		return gitsvc.Repo{}, false
	}
	return gitsvc.Repo{ProjectID: p.ID, MachineID: p.MachineID, Path: p.Path}, true
}

var _ = jobs.KindGit
