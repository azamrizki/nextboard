package machine

import (
	"path"
	"strings"
	"time"
)

const (
	probeBegin = "__NB_PROBE_BEGIN__"
	probeEnd   = "__NB_PROBE_END__"
	probeTmuxV = "__NB_TMUXV__"
)

// probeScript builds the read-only probe. Markers fence the output so that
// anything a login profile prints is ignored. `curl` is always looked up:
// it carries the HTTP fallback of agent hooks (docs/API-agents.md §4.5).
func probeScript(commands []string) string {
	words := []string{"tmux", "curl"}
	seen := map[string]bool{"tmux": true, "curl": true}
	for _, c := range commands {
		if c == "" || c == "$SHELL" || seen[c] {
			continue
		}
		seen[c] = true
		words = append(words, c)
	}
	return "echo " + probeBegin + `; uname -sm; echo "$SHELL"; command -v ` + Join(words...) +
		` 2>/dev/null; echo "` + probeTmuxV + `$(tmux -V 2>/dev/null)"; echo "$HOME"; echo ` + probeEnd
}

// parseProbe parses probeScript output.
func parseProbe(out string, commands []string) *ProbeResult {
	if i := strings.Index(out, probeBegin); i >= 0 {
		out = out[i+len(probeBegin):]
	}
	if i := strings.Index(out, probeEnd); i >= 0 {
		out = out[:i]
	}
	var lines []string
	tmuxV := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, probeTmuxV); ok {
			tmuxV = strings.TrimSpace(strings.TrimPrefix(v, "tmux"))
			continue
		}
		if l != "" {
			lines = append(lines, l)
		}
	}
	p := &ProbeResult{Commands: map[string]string{}, ProbedAt: time.Now(), TmuxVersion: tmuxV}
	if len(lines) < 3 {
		return p
	}
	if f := strings.Fields(lines[0]); len(f) >= 2 {
		p.OS, p.Arch = f[0], f[1]
	} else if len(f) == 1 {
		p.OS = f[0]
	}
	p.Shell = lines[1]
	p.Home = lines[len(lines)-1]
	want := append([]string{"tmux", "curl"}, commands...)
	for _, l := range lines[2 : len(lines)-1] {
		for _, w := range want {
			if w == "" || w == "$SHELL" {
				continue
			}
			if l == w || path.Base(l) == path.Base(w) {
				if w == "tmux" {
					p.Tmux = true
				} else {
					p.Commands[w] = l
				}
			}
		}
	}
	return p
}
