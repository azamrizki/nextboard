package machine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sshHome points the hub at a home of the test's own, with no system config.
func sshHome(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := sshSystemConfig
	sshSystemConfig = filepath.Join(home, "no-system-config")
	t.Cleanup(func() { sshSystemConfig = old })
	for name, text := range files {
		p := filepath.Join(home, ".ssh", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func hostsByName(t *testing.T) map[string]SSHConfigHost {
	t.Helper()
	list, err := ListSSHConfigHosts()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]SSHConfigHost{}
	for _, h := range list {
		out[h.Host] = h
	}
	return out
}

func TestSSHConfigHostsAndTheirSettings(t *testing.T) {
	sshHome(t, map[string]string{"config": `
# a comment
Host web web2
  HostName 10.0.0.5
  User deploy
  Port 2222
  IdentityFile ~/.ssh/deploy_key
  ProxyJump bastion

Host bastion
  HostName bastion.example.com
  ProxyJump none

Host *.corp !skip.corp
  User corp

Host *
  User fallback
`})
	hosts := hostsByName(t)
	if len(hosts) != 3 {
		t.Fatalf("wildcards must not be listed: %+v", hosts)
	}
	web := hosts["web"]
	if web.HostName != "10.0.0.5" || web.User != "deploy" || web.Port != 2222 || web.IdentityFile != "~/.ssh/deploy_key" || web.ProxyJump != "bastion" {
		t.Errorf("web = %+v", web)
	}
	if hosts["web2"].HostName != "10.0.0.5" {
		t.Errorf("second name on one line: %+v", hosts["web2"])
	}
	b := hosts["bastion"]
	if b.ProxyJump != "" || b.User != "fallback" || b.Port != 22 {
		t.Errorf("bastion = %+v", b)
	}
}

func TestSSHConfigMissingFileIsNoHosts(t *testing.T) {
	sshHome(t, nil)
	if hosts := hostsByName(t); len(hosts) != 0 {
		t.Errorf("%+v", hosts)
	}
	// An alias then stands for itself.
	r := resolveAlias(freshSettings(), "box.example.com")
	if r.hostName != "box.example.com" || r.port != 22 || r.user != "" {
		t.Errorf("%+v", r)
	}
}

// The parser gives up on the whole file at a Match it does not know, and every
// host then silently became alias:22 with no user and no key.
func TestSSHConfigSurvivesEveryKindOfMatch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	sshHome(t, map[string]string{"config": `
Host web
  HostName 10.0.0.5
  User deploy

Match exec "touch ` + "`" + ` + marker + ` + "`" + `"
  User from-exec

Match user root
  Port 1

Match final all
  Port 2

Match host *.corp exec "true"
  User from-mixed

Host db
  HostName 10.0.0.6

Match host *.lan,*.home
  User lan

Match all
  ServerAliveInterval 30
`})
	hosts := hostsByName(t)
	if len(hosts) != 2 {
		t.Fatalf("only Host lines name hosts: %+v", hosts)
	}
	web := hosts["web"]
	if web.HostName != "10.0.0.5" || web.User != "deploy" || web.Port != 22 {
		t.Errorf("web = %+v", web)
	}
	if hosts["db"].HostName != "10.0.0.6" || hosts["db"].User != "" {
		t.Errorf("a left-out Match leaked into db: %+v", hosts["db"])
	}
	us := freshSettings()
	if got := resolveAlias(us, "nas.lan").user; got != "lan" {
		t.Errorf("Match host: user %q", got)
	}
	if got := resolveAlias(us, "x.home").user; got != "lan" {
		t.Errorf("Match host, comma: user %q", got)
	}
	if got := us.Get("anything", "ServerAliveInterval"); got != "30" {
		t.Errorf("Match all: %q", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("reading a config ran its Match exec")
	}
}

func TestSSHConfigIncludes(t *testing.T) {
	home := sshHome(t, map[string]string{
		"config": `
Include config.d/*.conf
Include ~/.ssh/extra "missing.conf"

Host after
  HostName 10.0.0.9

Host wrapper
  Include inner.conf
  User wrapped
`,
		"config.d/a.conf": "Host from-a\n  HostName 10.0.1.1\n\nMatch exec true\n  User nope\n",
		"config.d/b.conf": "Host from-b\n  HostName 10.0.1.2\n  User b\n",
		"extra":           "Host from-extra\n  HostName 10.0.1.3\n",
		"inner.conf":      "Port 2200\n",
	})
	hosts := hostsByName(t)
	for name, addr := range map[string]string{"from-a": "10.0.1.1", "from-b": "10.0.1.2", "from-extra": "10.0.1.3", "after": "10.0.0.9"} {
		if hosts[name].HostName != addr {
			t.Errorf("%s = %+v", name, hosts[name])
		}
	}
	if u := hosts["from-a"].User; u != "" {
		t.Errorf("a Match inside an include leaked: user %q", u)
	}
	if w := hosts["wrapper"]; w.Port != 2200 || w.User != "wrapped" {
		t.Errorf("include inside a Host block, and what follows it: %+v", w)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "config")); err != nil {
		t.Fatal(err)
	}
}

// Options after a top level Include belong to no host of the included file.
func TestSSHConfigOptionsAfterAnIncludeStayGlobal(t *testing.T) {
	sshHome(t, map[string]string{
		"config":   "Include one.conf\nUser everyone\n",
		"one.conf": "Host one\n  HostName 10.0.0.1\n",
	})
	if got := resolveAlias(freshSettings(), "other").user; got != "everyone" {
		t.Errorf("user %q", got)
	}
}

func TestSSHConfigIncludeLoopEnds(t *testing.T) {
	sshHome(t, map[string]string{"config": "Host a\n  HostName 10.0.0.1\nInclude config\n"})
	if h := hostsByName(t)["a"]; h.HostName != "10.0.0.1" {
		t.Errorf("%+v", h)
	}
}

// Everything comes from $HOME, also where the user database says something else.
func TestSSHConfigFollowsHOME(t *testing.T) {
	sshHome(t, map[string]string{"config": "Host only-here\n  HostName 10.9.9.9\n  User me\n"})
	r := resolveConfig(Config{SSHConfigHost: "only-here"})
	if r.hostName != "10.9.9.9" || r.user != "me" {
		t.Errorf("%+v", r)
	}
}

func TestSSHConfigSystemFileComesSecond(t *testing.T) {
	home := sshHome(t, map[string]string{"config": "Host web\n  User mine\n"})
	sshSystemConfig = filepath.Join(home, "system")
	if err := os.WriteFile(sshSystemConfig, []byte("Host *\n  User theirs\n  Port 2022\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := resolveAlias(freshSettings(), "web")
	if r.user != "mine" || r.port != 2022 {
		t.Errorf("%+v", r)
	}
}

func TestResolveConfigForAManualMachine(t *testing.T) {
	sshHome(t, map[string]string{"config": "Host box\n  User fromconfig\n  Port 2200\n  IdentityFile ~/.ssh/box_key\n"})
	r := resolveConfig(Config{Host: "box"})
	if r.user != "fromconfig" || r.port != 2200 || len(r.identityFiles) != 1 {
		t.Errorf("defaults for that host: %+v", r)
	}
	r = resolveConfig(Config{Host: "box", User: "typed", Port: 22222})
	if r.user != "typed" || r.port != 22222 {
		t.Errorf("what the user typed wins: %+v", r)
	}
}

func TestExpandPathInHOME(t *testing.T) {
	home := sshHome(t, nil)
	tg := target{hostName: "h", user: "u", alias: "a", port: 22}
	for in, want := range map[string]string{
		"~/.ssh/key":  filepath.Join(home, ".ssh", "key"),
		"key":         filepath.Join(home, ".ssh", "key"),
		"/abs/key":    "/abs/key",
		"~/k_%h_%r":   filepath.Join(home, "k_h_u"),
		"/k/%n/%p%%x": "/k/a/22%x",
	} {
		if got := expandPath(in, tg); got != want {
			t.Errorf("expandPath(%q) = %q, want %q", in, got, want)
		}
	}
	if !strings.HasPrefix(expandPath("%d/k", tg), home) {
		t.Errorf("%%d must be the home folder, %s", home)
	}
}
