package server

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// What a project on a server tells its agents besides its top CLAUDE.md (docs/API-agents.md
// §21). The CLI runs on This PC in an empty folder, so it would see neither the project's own
// commands and skills nor the instructions of its subfolders. At launch the hub copies
// .claude/commands and .claude/skills of the server's project into that folder, where Claude
// Code loads them as the project's, and names the subfolders that have instructions of their
// own in the system prompt.

const (
	mirrorFileMax   = 256 << 10
	mirrorFiles     = 300
	mirrorTotal     = 4 << 20
	nestedRulesMax  = 40
	mirrorListLimit = 20 * time.Second
)

// serverExtras is what launch found in the project's folder on the server.
type serverExtras struct {
	nested   []string // subfolder instruction files, relative to the project folder
	mirrored int      // command and skill files copied next to the CLI
}

const extrasMark = "---nb-extras---"

// remoteExtras looks in the project folder on the server once, and copies the commands and
// skills when the CLI is Claude Code. Anything that fails leaves the launch as it was.
func (s *Server) remoteExtras(host machine.Host, cwd, localDir string, names []string, claude bool) serverExtras {
	ctx, cancel := context.WithTimeout(context.Background(), mirrorListLimit)
	defer cancel()
	prune := `\( -name node_modules -o -name vendor -o -name .git -o -name storage -o -name dist -o -name build -o -name .next -o -name target -o -name .venv \) -prune`
	script := `find . -maxdepth 6 ` + prune + ` -o -type f \( -name CLAUDE.md -o -name AGENTS.md \) -print 2>/dev/null | head -400; echo ` + extrasMark
	if claude {
		script += `; find .claude/commands .claude/skills -type f -size -` + strconv.Itoa(mirrorFileMax/1024+1) + `k 2>/dev/null | head -` + strconv.Itoa(mirrorFiles)
	}
	res, err := machine.Exec(ctx, host, machine.ExecSpec{Cwd: cwd, Script: script, Timeout: mirrorListLimit, MaxOutput: 1 << 20})
	if err != nil || (res.ExitCode != 0 && len(res.Stdout) == 0) {
		return serverExtras{}
	}
	found, files, _ := strings.Cut(string(res.Stdout), extrasMark+"\n")
	var x serverExtras
	x.nested = nestedRules(found, names)
	if claude {
		x.mirrored = s.mirrorClaude(ctx, host, cwd, localDir, files)
	}
	return x
}

// nestedRules picks one instruction file per subfolder, the CLI's own name first.
func nestedRules(list string, names []string) []string {
	byDir := map[string]string{}
	var dirs []string
	for _, line := range strings.Split(list, "\n") {
		p := strings.TrimPrefix(strings.TrimSpace(line), "./")
		if p == "" || !strings.Contains(p, "/") || strings.Contains(p, "..") {
			continue // the project's own top file is in the prompt already
		}
		d, base := path.Split(p)
		if cur, ok := byDir[d]; ok {
			// the CLI's own name wins: CLAUDE.md for Claude Code, AGENTS.md for the others
			if rank(base, names) < rank(path.Base(cur), names) {
				byDir[d] = p
			}
			continue
		}
		byDir[d] = p
		dirs = append(dirs, d)
	}
	out := make([]string, 0, min(len(dirs), nestedRulesMax))
	for _, d := range dirs {
		if len(out) == nestedRulesMax {
			break
		}
		out = append(out, byDir[d])
	}
	return out
}

func rank(name string, names []string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return len(names)
}

// mirrorClaude copies the server's .claude/commands and .claude/skills into the CLI's folder,
// in place of what an earlier launch copied. Only plain text files inside those two folders.
func (s *Server) mirrorClaude(ctx context.Context, host machine.Host, cwd, localDir, list string) int {
	if localDir == "" {
		return 0
	}
	for _, sub := range []string{"commands", "skills"} {
		_ = os.RemoveAll(filepath.Join(localDir, ".claude", sub))
	}
	n, total := 0, 0
	for _, line := range strings.Split(list, "\n") {
		p := path.Clean(strings.TrimPrefix(strings.TrimSpace(line), "./"))
		if !strings.HasPrefix(p, ".claude/commands/") && !strings.HasPrefix(p, ".claude/skills/") {
			continue
		}
		if strings.Contains(p, "..") || n >= mirrorFiles || total >= mirrorTotal {
			continue
		}
		c, err := host.ReadFile(ctx, remotePath(cwd, p), mirrorFileMax)
		if err != nil || c.Binary || c.Truncated {
			continue
		}
		dst := filepath.Join(localDir, filepath.FromSlash(p))
		if !strings.HasPrefix(dst, filepath.Join(localDir, ".claude")+string(filepath.Separator)) {
			continue
		}
		if os.MkdirAll(filepath.Dir(dst), 0o700) != nil || os.WriteFile(dst, []byte(c.Content), 0o600) != nil {
			continue
		}
		n++
		total += len(c.Content)
	}
	return n
}
