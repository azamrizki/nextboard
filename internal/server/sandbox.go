package server

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Agent sandboxes (docs/API-agents.md §27): a project can keep its agents' commands to the project
// folder, with or without the network. Claude Code builds it with bubblewrap, Codex with Landlock.

// sandboxWrites are the folders a sandboxed agent's commands may write besides the project and the
// temp folder: the package caches builds and installs need. Only those that exist. A command can
// leave something in a cache that a later build outside the sandbox uses; the docs say so.
func sandboxWrites() []string {
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	if d, err := os.UserCacheDir(); err == nil {
		add(d)
	}
	home, _ := os.UserHomeDir()
	for _, rel := range []string{".npm", "go/pkg/mod", ".cargo/registry", ".cargo/git", ".bun/install/cache", ".local/share/pnpm",
		".m2/repository", ".gradle/caches", ".composer/cache", ".pub-cache"} {
		if home != "" {
			add(filepath.Join(home, rel))
		}
	}
	if v := os.Getenv("GOMODCACHE"); v != "" && filepath.IsAbs(v) {
		add(v)
	}
	return out
}

// sandboxRule tells the agent where its commands may write, so it does not keep retrying.
func sandboxRule(network bool) string {
	r := "Your shell commands run in a sandbox: they may write only in the project folder, the temp folder and package caches"
	if !network {
		return r + ", and they have no network. If a task needs more, say so instead of working around it."
	}
	return r + ". If a task needs to write elsewhere, say so instead of working around it."
}

// SandboxStatus is GET /api/agents/sandbox: whether each CLI can build its sandbox on This PC.
type SandboxStatus struct {
	Claude SandboxCLI `json:"claude"`
	Codex  SandboxCLI `json:"codex"`
}

type SandboxCLI struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

var sandboxProbe struct {
	mu   sync.Mutex
	at   time.Time
	last SandboxStatus
}

// probeSandbox checks This PC once a minute. Codex's Landlock needs nothing more. Claude Code runs
// its commands in bubblewrap and makes a user namespace inside it, which Ubuntu's AppArmor refuses
// until bubblewrap is allowed to (measured on Ubuntu with Claude Code 2.1.322: every command then
// fails, so nothing runs outside the sandbox, but nothing runs at all).
func probeSandbox() SandboxStatus {
	sandboxProbe.mu.Lock()
	defer sandboxProbe.mu.Unlock()
	if time.Since(sandboxProbe.at) < time.Minute {
		return sandboxProbe.last
	}
	st := SandboxStatus{Claude: SandboxCLI{Ready: true}, Codex: SandboxCLI{Ready: true}}
	if runtime.GOOS == "linux" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		switch {
		case lookBwrap() == "":
			st.Claude = SandboxCLI{Reason: "bubblewrap is not installed (the bwrap command)"}
		case exec.CommandContext(ctx, lookBwrap(), "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--unshare-user", "--",
			"unshare", "--user", "--map-root-user", "true").Run() != nil:
			st.Claude = SandboxCLI{Reason: "AppArmor does not let bubblewrap make the user namespace Claude Code's sandbox needs"}
		}
	}
	sandboxProbe.at, sandboxProbe.last = time.Now(), st
	return st
}

func lookBwrap() string {
	p, err := exec.LookPath("bwrap")
	if err != nil {
		return ""
	}
	return p
}

func (s *Server) handleSandboxStatus(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, probeSandbox())
}
