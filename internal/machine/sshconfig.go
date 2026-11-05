package machine

import (
	"bytes"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// SSHConfigHost is one concrete `Host` entry from ~/.ssh/config.
type SSHConfigHost struct {
	Host         string `json:"host"`
	HostName     string `json:"hostName"`
	User         string `json:"user"`
	Port         int    `json:"port"`
	IdentityFile string `json:"identityFile"`
	ProxyJump    string `json:"proxyJump"`
}

func sshDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh")
}

// sshSystemConfig is the machine-wide config, read after the user's.
var sshSystemConfig = "/etc/ssh/ssh_config"

const sshIncludeDepth = 5

// sshConfigText is a config made fit for the parser, which fails on the whole
// file at any Match it does not know and looks for includes in a home of its own:
//   - Include is written out in place. What follows goes back to the block it was in.
//   - `Match all` and `Match host …` become Host blocks.
//   - Other Match blocks are left out. They depend on things only ssh can know,
//     and `Match exec` would run a command, which reading a config must never do.
type sshConfigText struct {
	buf     bytes.Buffer
	aliases []string // concrete names of Host lines, in order
}

func (c *sshConfigText) read(path, base string, depth int) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	block, skipping := "Host *", false
	for _, line := range strings.Split(string(b), "\n") {
		key, val := sshConfigLine(line)
		switch key {
		case "host":
			block, skipping = line, false
			for _, p := range strings.Fields(val) {
				if p = strings.Trim(p, `"`); p != "" && !strings.ContainsAny(p, "*?!") {
					c.aliases = append(c.aliases, p)
				}
			}
		case "match":
			f := strings.Fields(strings.ReplaceAll(val, ",", " "))
			switch {
			case len(f) == 1 && strings.EqualFold(f[0], "all"):
				block, skipping = "Host *", false
			case len(f) >= 2 && strings.EqualFold(f[0], "host") && !sshMatchCriterion(f[1:]):
				block, skipping = "Host "+strings.Join(f[1:], " "), false
			default:
				skipping = true
				continue
			}
			line = block
		case "include":
			if skipping || depth >= sshIncludeDepth {
				continue
			}
			for _, pat := range strings.Fields(val) {
				matches, _ := filepath.Glob(sshIncludePath(strings.Trim(pat, `"`), base))
				sort.Strings(matches)
				for _, m := range matches {
					_ = c.read(m, base, depth+1)
					c.buf.WriteString("\n" + block + "\n")
				}
			}
			continue
		}
		if !skipping {
			c.buf.WriteString(line + "\n")
		}
	}
	return nil
}

// sshConfigLine splits a config line into its lower case keyword and the rest.
func sshConfigLine(line string) (key, val string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", ""
	}
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return strings.ToLower(line), ""
	}
	return strings.ToLower(line[:i]), strings.TrimLeft(line[i:], " \t=")
}

// sshMatchCriterion reports another criterion after `Match host`: the parser
// would take its words for host names.
func sshMatchCriterion(words []string) bool {
	for _, w := range words {
		switch strings.ToLower(strings.TrimPrefix(w, "!")) {
		case "all", "canonical", "exec", "final", "host", "localnetwork", "localuser", "originalhost", "tagged", "user", "version", "command", "sessiontype":
			return true
		}
	}
	return false
}

func sshIncludePath(p, base string) string {
	switch {
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(filepath.Dir(sshDir()), p[2:])
	case filepath.IsAbs(p):
		return p
	}
	return filepath.Join(base, p)
}

// sshSettings answers from the user's config, then the system's. Both are read
// anew on every use, so edits made after the hub started are picked up.
type sshSettings struct {
	files []*ssh_config.Config
}

func freshSettings() *sshSettings {
	s := &sshSettings{}
	for _, f := range []struct{ path, base string }{
		{filepath.Join(sshDir(), "config"), sshDir()},
		{sshSystemConfig, filepath.Dir(sshSystemConfig)},
	} {
		var text sshConfigText
		if text.read(f.path, f.base, 0) != nil {
			continue
		}
		if cfg, err := ssh_config.DecodeBytes(text.buf.Bytes()); err == nil {
			s.files = append(s.files, cfg)
		}
	}
	return s
}

// Get returns the first value for alias, or ssh's default.
func (s *sshSettings) Get(alias, key string) string {
	for _, cfg := range s.files {
		if v, err := cfg.Get(alias, key); err == nil && v != "" {
			return v
		}
	}
	return ssh_config.Default(key)
}

// GetAll returns every value for alias from the first file that has any.
func (s *sshSettings) GetAll(alias, key string) []string {
	for _, cfg := range s.files {
		if v, err := cfg.GetAll(alias, key); err == nil && len(v) > 0 {
			return v
		}
	}
	return nil
}

// ListSSHConfigHosts returns the non-wildcard Host aliases of the service
// user's ~/.ssh/config, includes too, with their effective settings.
func ListSSHConfigHosts() ([]SSHConfigHost, error) {
	var text sshConfigText
	dir := sshDir()
	err := text.read(filepath.Join(dir, "config"), dir, 0)
	if os.IsNotExist(err) {
		return []SSHConfigHost{}, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := ssh_config.DecodeBytes(text.buf.Bytes()); err != nil {
		return nil, err
	}
	us := freshSettings()
	seen := map[string]bool{}
	out := []SSHConfigHost{}
	for _, alias := range text.aliases {
		if seen[alias] {
			continue
		}
		seen[alias] = true
		r := resolveAlias(us, alias)
		idf := ""
		if len(r.identityFiles) > 0 {
			idf = r.identityFiles[0]
		}
		out = append(out, SSHConfigHost{
			Host: alias, HostName: r.hostName, User: r.user, Port: r.port,
			IdentityFile: idf, ProxyJump: r.proxyJump,
		})
	}
	return out, nil
}

// target is a fully resolved SSH destination.
type target struct {
	alias         string
	hostName      string
	port          int
	user          string
	identityFiles []string // explicit IdentityFile entries (unexpanded)
	proxyJump     string
}

func (t target) addr() string {
	return joinHostPort(t.hostName, t.port)
}

func joinHostPort(h string, port int) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		h = "[" + h + "]"
	}
	return h + ":" + strconv.Itoa(port)
}

func resolveAlias(us *sshSettings, alias string) target {
	t := target{alias: alias}
	t.hostName = us.Get(alias, "HostName")
	if t.hostName == "" {
		t.hostName = alias
	}
	t.hostName = strings.ReplaceAll(t.hostName, "%h", alias)
	t.port, _ = strconv.Atoi(us.Get(alias, "Port"))
	if t.port == 0 {
		t.port = 22
	}
	t.user = us.Get(alias, "User")
	for _, f := range us.GetAll(alias, "IdentityFile") {
		// The library returns "~/.ssh/identity" as a default; ignore it.
		if f != "" && f != "~/.ssh/identity" {
			t.identityFiles = append(t.identityFiles, f)
		}
	}
	t.proxyJump = us.Get(alias, "ProxyJump")
	if strings.EqualFold(t.proxyJump, "none") {
		t.proxyJump = ""
	}
	return t
}

// resolveConfig turns a machine definition into a dial target.
func resolveConfig(c Config) target {
	var t target
	if c.SSHConfigHost != "" {
		t = resolveAlias(freshSettings(), c.SSHConfigHost)
	} else {
		t = target{alias: c.Host, hostName: c.Host, port: c.Port}
		// Manual entries still honour ~/.ssh/config defaults for that host.
		r := resolveAlias(freshSettings(), c.Host)
		t.identityFiles = r.identityFiles
		t.user = r.user
		if c.Port == 0 {
			t.port = r.port
		}
	}
	if c.User != "" {
		t.user = c.User
	}
	if c.Port != 0 && c.SSHConfigHost == "" {
		t.port = c.Port
	}
	if t.port == 0 {
		t.port = 22
	}
	if t.user == "" {
		if u, err := user.Current(); err == nil {
			t.user = u.Username
		}
	}
	return t
}

// expandPath expands ~ and the common ssh_config tokens in IdentityFile.
func expandPath(p string, t target) string {
	home, _ := os.UserHomeDir()
	if p == "~" {
		p = home
	} else if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, p[2:])
	}
	r := strings.NewReplacer("%d", home, "%h", t.hostName, "%r", t.user, "%n", t.alias, "%p", strconv.Itoa(t.port), "%%", "%")
	if u, err := user.Current(); err == nil {
		p = strings.ReplaceAll(p, "%u", u.Username)
	}
	p = r.Replace(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(sshDir(), p)
	}
	return p
}
