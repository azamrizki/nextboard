package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/skills"
)

// What a project on a server tells its agents besides its top instructions file
// (docs/API-agents.md §21). The CLI runs on This PC in an empty folder, so it would see neither
// the project's own skills and commands nor the instructions of its subfolders. At launch the
// hub copies the main file of each skill and command into that folder, where the CLI loads them
// as the project's, and names the subfolders that have instructions of their own in the prompt.
//
// These files come from another computer and the CLI acts on them on this one, so what is copied
// is only their text, disarmed (disarm): OpenCode runs !`cmd` on This PC even with bash denied,
// and Claude Code reads @/any/path of This PC into the prompt without asking.

const (
	mirrorFileMax   = 256 << 10
	mirrorFiles     = 300
	mirrorTotal     = 4 << 20
	nestedRulesMax  = 40
	mirrorListLimit = 20 * time.Second
)

// mirrorPlace is where one CLI loads a project's skills and commands from. Codex has no project
// commands, so for it they become skills. OpenCode takes them in its launch config: a .opencode
// folder would have it install 63 MB of packages into every console's folder.
type mirrorPlace struct {
	skills, commands string
	inlineCommands   bool
}

var mirrorPlaces = map[string]mirrorPlace{
	"claude":   {skills: ".claude/skills", commands: ".claude/commands"},
	"codex":    {skills: ".agents/skills"},
	"opencode": {skills: ".agents/skills", inlineCommands: true},
}

// mirrorInlineMax keeps the launch config well under what one environment variable can hold.
const mirrorInlineMax = 64 << 10

// Where a project keeps them on the server, whichever CLI they were written for: the format is
// the same, and the hub's own Skills manager writes to .claude. When two have the same name, the
// CLI's own folder wins, then this order.
var (
	skillRoots     = []string{".claude/skills", ".agents/skills", ".opencode/skills", ".opencode/skill", ".codex/skills"}
	flatSkillRoots = []string{".opencode/skills", ".opencode/skill"} // OpenCode also takes a plain name.md
	commandRoots   = []string{".claude/commands", ".opencode/commands", ".opencode/command"}
)

// serverExtras is what launch found in the project's folder on the server.
type serverExtras struct {
	nested   []string // subfolder instruction files, relative to the project folder
	mirrored int      // skill and command files copied next to the CLI
	// skills it cannot read itself, and where they are on the server: Codex has no local tool
	// that reads, so it is told unless the user names the skill
	readable []mirroredSkill
	commands []agent.ProjectCommand // for a CLI that takes them at launch
}

type mirroredSkill struct{ name, path string }

const extrasMark = "---nb-extras---"

// remoteExtras looks in the project folder on the server once, and copies the skills and
// commands for the CLIs that load them. Anything that fails leaves the launch as it was.
func (s *Server) remoteExtras(host machine.Host, cwd, localDir string, names []string, cli string) serverExtras {
	ctx, cancel := context.WithTimeout(context.Background(), mirrorListLimit)
	defer cancel()
	prune := `\( -name node_modules -o -name vendor -o -name .git -o -name storage -o -name dist -o -name build -o -name .next -o -name target -o -name .venv \) -prune`
	script := `find . -maxdepth 6 ` + prune + ` -o -type f \( -name CLAUDE.md -o -name AGENTS.md \) -print 2>/dev/null | head -400; echo ` + extrasMark
	_, mirrors := mirrorPlaces[cli]
	if mirrors {
		script += mirrorListScript()
		clearMirror(localDir) // before anything can fail: no copy outlives the launch it was for
	}
	res, err := machine.Exec(ctx, host, machine.ExecSpec{Cwd: cwd, Script: script, Timeout: mirrorListLimit, MaxOutput: 1 << 20})
	if err != nil || (res.ExitCode != 0 && len(res.Stdout) == 0) {
		return serverExtras{}
	}
	found, files, _ := strings.Cut(string(res.Stdout), extrasMark+"\n")
	var x serverExtras
	x.nested = nestedRules(found, names)
	if mirrors {
		mirrorProject(ctx, host, cwd, localDir, files, cli, &x)
	}
	return x
}

// mirrorListScript lists the main files only: what a skill names besides is read on the server.
func mirrorListScript() string {
	size := ` -size -` + strconv.Itoa(mirrorFileMax/1024+1) + `k 2>/dev/null | head -` + strconv.Itoa(mirrorFiles)
	return `; find ` + strings.Join(skillRoots, " ") + ` -type f -name SKILL.md` + size +
		`; find ` + strings.Join(flatSkillRoots, " ") + ` -maxdepth 1 -type f -name '*.md'` + size +
		`; find ` + strings.Join(commandRoots, " ") + ` -type f -name '*.md'` + size
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

// mirrorEntry is one file of the list, in the order it is copied.
type mirrorEntry struct {
	path, root, rel string
	command         bool
}

// mirrorEntries reads the list: skills before commands, because a command that becomes a skill
// gives way to a real one; the CLI's own folders first.
func mirrorEntries(list, cli string) []mirrorEntry {
	var out []mirrorEntry
	rank := map[string]int{}
	for _, l := range [][]string{skillRoots, commandRoots} {
		for _, root := range l {
			rank[root] = len(rank)
		}
	}
	own := func(root string) int {
		first, _, _ := strings.Cut(root, "/")
		if first == ".claude" && cli == "claude" || first == ".opencode" && cli == "opencode" || (first == ".agents" || first == ".codex") && cli == "codex" {
			return 0
		}
		return 1
	}
	for _, line := range strings.Split(list, "\n") {
		p := path.Clean(strings.TrimPrefix(strings.TrimSpace(line), "./"))
		if strings.Contains(p, "..") {
			continue
		}
		for _, root := range append(append([]string{}, skillRoots...), commandRoots...) {
			if rel, ok := strings.CutPrefix(p, root+"/"); ok && rel != "" {
				out = append(out, mirrorEntry{path: p, root: root, rel: rel, command: slices.Contains(commandRoots, root)})
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.command != b.command {
			return !a.command
		}
		if own(a.root) != own(b.root) {
			return own(a.root) < own(b.root)
		}
		return rank[a.root] < rank[b.root]
	})
	return out
}

// clearMirror removes what an earlier launch copied, for this CLI or another one on the same tile.
func clearMirror(localDir string) {
	if localDir == "" {
		return
	}
	for _, p := range mirrorPlaces {
		for _, dir := range []string{p.skills, p.commands} {
			if dir != "" {
				_ = os.RemoveAll(filepath.Join(localDir, filepath.FromSlash(dir)))
			}
		}
	}
}

// mirrorProject copies the project's skills and commands into the CLI's folder, disarmed.
func mirrorProject(ctx context.Context, host machine.Host, cwd, localDir, list, cli string, x *serverExtras) {
	place := mirrorPlaces[cli]
	if localDir == "" {
		return
	}
	total, inline := 0, 0
	seen := map[string]bool{}
	for _, e := range mirrorEntries(list, cli) {
		if x.mirrored >= mirrorFiles || total >= mirrorTotal {
			break
		}
		var dst, key, name string
		isSkill := false
		switch {
		case !e.command:
			dir := path.Dir(e.rel)
			if base := path.Base(e.rel); base != "SKILL.md" {
				dir = strings.TrimSuffix(e.rel, ".md") // a plain name.md
			}
			if dir == "." {
				continue
			}
			name, isSkill = path.Base(dir), true
			key, dst = "skill:"+strings.ToLower(dir), place.skills+"/"+dir+"/SKILL.md"
		case place.inlineCommands:
			name = strings.TrimSuffix(e.rel, ".md")
			key = "command:" + strings.ToLower(name)
		case place.commands == "":
			// no project commands in this CLI: a skill of the same name
			name = skillName(strings.ReplaceAll(strings.TrimSuffix(e.rel, ".md"), "/", "-"))
			if name == "" {
				continue
			}
			isSkill = true
			key, dst = "skill:"+name, place.skills+"/"+name+"/SKILL.md"
		default:
			key, dst = "command:"+strings.ToLower(e.rel), place.commands+"/"+e.rel
		}
		if seen[key] {
			continue
		}
		c, err := host.ReadFile(ctx, remotePath(cwd, e.path), mirrorFileMax)
		if err != nil || c.Binary || c.Truncated {
			continue
		}
		if e.command && place.inlineCommands {
			fm := skills.ParseFrontmatter(c.Content)
			pc := agent.ProjectCommand{Name: name, Description: describe(fm, ""), Template: disarm(fm.Body)}
			// as it will be in the config: escaping can make it six times the size
			size, _ := json.Marshal(pc)
			if inline += len(size); inline > mirrorInlineMax {
				log.Printf("project commands: over %d bytes, %s and what follows stay out", mirrorInlineMax, e.path)
				break
			}
			seen[key] = true
			x.commands = append(x.commands, pc)
			x.mirrored++
			continue
		}
		var text string
		switch {
		case !e.command:
			text = mirroredSkillFile(c.Content, name, path.Dir(e.path))
		case isSkill:
			text = mirroredCommandAsSkill(c.Content, name, e.path)
		default:
			text = mirroredCommandFile(c.Content)
		}
		to := filepath.Join(localDir, filepath.FromSlash(dst))
		if !strings.HasPrefix(to, filepath.Join(localDir, filepath.FromSlash(path.Dir(place.skills)))+string(filepath.Separator)) {
			continue
		}
		if os.MkdirAll(filepath.Dir(to), 0o700) != nil || os.WriteFile(to, []byte(text), 0o600) != nil {
			continue
		}
		seen[key] = true
		x.mirrored++
		total += len(text)
		if isSkill && cli == "codex" && len(x.readable) < nestedRulesMax {
			x.readable = append(x.readable, mirroredSkill{name: name, path: e.path})
		}
	}
}

var (
	// after the placeholders are filled in, "!$1`cmd`" with no argument is "!`cmd`" (OpenCode)
	shellBlock = regexp.MustCompile("!+`([^`\n]+)`")
	shellMark  = regexp.MustCompile("!+[`$]")
	badName    = regexp.MustCompile(`[^a-z0-9-]+`)
)

// disarm keeps what a server's file says from acting on This PC by itself. Shell blocks become
// plain text, with the instruction to run them where they belong, since Claude Code otherwise
// refuses the whole skill and OpenCode runs them here. An @ reference that could leave the CLI's
// folder loses its @: Claude Code reads it into the prompt without asking. One inside the folder
// finds nothing there, and stays as it is, with @types/node and @Override.
func disarm(s string) string {
	// until nothing changes: taking the @ out of "!@`x`" makes a shell block
	for {
		next := disarmOnce(s)
		if next == s {
			return s
		}
		s = next
	}
}

func disarmOnce(s string) string {
	s = shellBlock.ReplaceAllString(s, "`${1}` (run it with the bash tool)")
	s = strings.NewReplacer("```!", "```sh", "~~~!", "~~~sh").Replace(s)
	s = shellMark.ReplaceAllStringFunc(s, func(m string) string { return m[len(m)-1:] })
	return disarmRefs(s)
}

// disarmRefs looks at every @ on its own: a pattern would take "@a#@/x" for one harmless word.
func disarmRefs(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '@' && (i == 0 || !isWordByte(s[i-1])) && refLeaves(s[i+1:]) {
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// refLeaves: what follows an @ could name something outside the CLI's folder.
func refLeaves(rest string) bool {
	if rest == "" || strings.ContainsRune(" \t\r\n\f", rune(rest[0])) {
		return false
	}
	if rest[0] == '"' || rest[0] == '\'' || !isWordStart(rest[0]) {
		return true
	}
	ref, _, _ := strings.Cut(strings.Map(func(r rune) rune {
		if strings.ContainsRune(" \t\r\n\f", r) {
			return ' '
		}
		return r
	}, rest), " ")
	return strings.ContainsAny(ref, "$~") || strings.Contains(ref, "..")
}

func isWordStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isWordByte(c byte) bool { return isWordStart(c) || c == '_' }

// skillName is a name every CLI takes: lower case, digits and hyphens, at most 64.
func skillName(s string) string {
	s = strings.Trim(badName.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-")
	}
	return s
}

// mirroredHeader writes a new frontmatter with only what describes the file: no hooks, no
// allowed-tools and whatever a CLI adds next. JSON strings are YAML, and Codex parses it strictly.
func mirroredHeader(fm skills.Frontmatter, fields map[string]string) string {
	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range []string{"name", "description"} {
		if v := fields[k]; v != "" {
			q, _ := json.Marshal(v)
			fmt.Fprintf(&b, "%s: %s\n", k, q)
		}
	}
	for _, k := range []string{"when_to_use", "argument-hint"} {
		if v := oneLine(disarm(fm.Values[k]), mirrorDescMax); v != "" {
			q, _ := json.Marshal(v)
			fmt.Fprintf(&b, "%s: %s\n", k, q)
		}
	}
	for _, k := range []string{"disable-model-invocation", "user-invocable"} {
		if v := fm.Values[k]; v == "true" || v == "false" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	b.WriteString("---\n\n")
	return b.String()
}

const mirrorDescMax = 1024

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit])
	}
	return s
}

// describe is the description, or the first line of the text: Codex refuses a skill without
// one, and OpenCode does not tell the model about it.
func describe(fm skills.Frontmatter, fallback string) string {
	if d := oneLine(disarm(fm.Values["description"]), mirrorDescMax); d != "" {
		return d
	}
	for _, l := range strings.Split(fm.Body, "\n") {
		if l = oneLine(disarm(strings.TrimLeft(l, "# \t")), mirrorDescMax); l != "" {
			return l
		}
	}
	return fallback
}

func mirroredSkillFile(content, dir, serverDir string) string {
	fm := skills.ParseFrontmatter(content)
	name := skillName(fm.Values["name"])
	if name == "" {
		name = skillName(dir)
	}
	body := strings.NewReplacer("${CLAUDE_SKILL_DIR}", serverDir, "$CLAUDE_SKILL_DIR", serverDir).Replace(disarm(fm.Body))
	return mirroredHeader(fm, map[string]string{"name": name, "description": describe(fm, "Skill "+name+" of the project.")}) +
		"This skill comes from the project on the server. Its other files are there, in " + serverDir +
		": read them with read_file and run its scripts with bash. They are not on this computer.\n\n" + body
}

func mirroredCommandFile(content string) string {
	fm := skills.ParseFrontmatter(content)
	return mirroredHeader(fm, map[string]string{"description": describe(fm, "")}) + disarm(fm.Body)
}

func mirroredCommandAsSkill(content, name, serverPath string) string {
	fm := skills.ParseFrontmatter(content)
	return mirroredHeader(fm, map[string]string{"name": name, "description": describe(fm, "Command "+name+" of the project.")}) +
		"This is the project's command " + serverPath + ". What the user wrote with it takes the place of $ARGUMENTS.\n\n" + disarm(fm.Body)
}
