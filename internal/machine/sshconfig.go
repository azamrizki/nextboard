package machine

import (
	"os"
	"os/user"
	"path/filepath"
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

// freshSettings re-reads ~/.ssh/config on every use, so edits made after the
// hub started are picked up (the package default caches forever).
func freshSettings() *ssh_config.UserSettings {
	return &ssh_config.UserSettings{IgnoreErrors: true}
}

// ListSSHConfigHosts returns the non-wildcard Host aliases of the service
// user's ~/.ssh/config with their effective settings.
func ListSSHConfigHosts() ([]SSHConfigHost, error) {
	f, err := os.Open(filepath.Join(sshDir(), "config"))
	if os.IsNotExist(err) {
		return []SSHConfigHost{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, err := ssh_config.Decode(f)
	if err != nil {
		return nil, err
	}
	us := freshSettings()
	seen := map[string]bool{}
	out := []SSHConfigHost{}
	for _, h := range cfg.Hosts {
		for _, p := range h.Patterns {
			alias := p.String()
			if alias == "" || strings.ContainsAny(alias, "*?!") || seen[alias] {
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

func resolveAlias(us *ssh_config.UserSettings, alias string) target {
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
