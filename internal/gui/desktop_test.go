package gui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeDesktop(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, "applications", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseExec(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`/usr/bin/google-chrome-stable %U`, []string{"/usr/bin/google-chrome-stable", "%U"}},
		{`"/opt/My App/run" --flag "a b" %f`, []string{"/opt/My App/run", "--flag", "a b", "%f"}},
		{`sh -c "echo \"hi\" \$HOME"`, []string{"sh", "-c", `echo "hi" $HOME`}},
		{`app ""`, []string{"app", ""}},
		{`  app   x  `, []string{"app", "x"}},
	}
	for _, c := range cases {
		got, err := parseExec(c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("parseExec(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := parseExec(`app "open`); err == nil {
		t.Error("an unterminated quote must fail")
	}
}

func TestExpandExec(t *testing.T) {
	e := &entry{Name: "My App", Icon: "myapp", Source: "/x/my.desktop"}
	argv := []string{"app", "%U", "%f", "%i", "--name=%c", "--src=%k", "100%%", "%d"}

	got := expandExec(argv, e, "")
	want := []string{"app", "--icon", "myapp", "--name=My App", "--src=/x/my.desktop", "100%"}
	if !slices.Equal(got, want) {
		t.Errorf("no target: %q, want %q", got, want)
	}

	got = expandExec([]string{"app", "%u", "--open=%f"}, e, "/home/a b")
	want = []string{"app", "file:///home/a%20b", "--open=/home/a b"}
	if !slices.Equal(got, want) {
		t.Errorf("target: %q, want %q", got, want)
	}
}

func TestScanAndFilter(t *testing.T) {
	bin := t.TempDir()
	prog := filepath.Join(bin, "prog")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	user, sys := t.TempDir(), t.TempDir()
	main := "[Desktop Entry]\nType=Application\nName=Prog\nExec=prog %F\nMimeType=inode/directory;\n" +
		"[Desktop Action new]\nName=Other\nExec=other\n"
	writeDesktop(t, sys, "prog.desktop", main)
	writeDesktop(t, sys, "sub/nested.desktop", "[Desktop Entry]\nType=Application\nName=Nested\nExec=prog\n")
	writeDesktop(t, sys, "tty.desktop", "[Desktop Entry]\nType=Application\nName=T\nExec=prog\nTerminal=true\n")
	writeDesktop(t, sys, "nodisp.desktop", "[Desktop Entry]\nType=Application\nName=N\nExec=prog\nNoDisplay=true\n")
	writeDesktop(t, sys, "only.desktop", "[Desktop Entry]\nType=Application\nName=O\nExec=prog\nOnlyShowIn=XFCE;\n")
	writeDesktop(t, sys, "gone.desktop", "[Desktop Entry]\nType=Application\nName=G\nExec=not-installed\n")
	writeDesktop(t, sys, "tryexec.desktop", "[Desktop Entry]\nType=Application\nName=TE\nExec=prog\nTryExec=not-installed\n")
	writeDesktop(t, sys, "link.desktop", "[Desktop Entry]\nType=Link\nName=L\nURL=http://x\n")
	// The user's copy masks the system one.
	writeDesktop(t, sys, "masked.desktop", "[Desktop Entry]\nType=Application\nName=M\nExec=prog\n")
	writeDesktop(t, user, "masked.desktop", "[Desktop Entry]\nType=Application\nName=M\nExec=prog\nHidden=true\n")

	entries := scanEntries([]string{user, sys})
	var got []string
	for id, e := range entries {
		if e.launchable(bin) {
			got = append(got, id)
		}
	}
	slices.Sort(got)
	want := []string{"prog", "sub-nested"}
	if !slices.Equal(got, want) {
		t.Fatalf("launchable = %q, want %q", got, want)
	}
	p := entries["prog"]
	if p.Name != "Prog" || p.Exec != "prog %F" {
		t.Errorf("action group leaked into the entry: %+v", p)
	}
	if !p.acceptsFolder() {
		t.Error("inode/directory with a file code accepts a folder")
	}
	if entries["sub-nested"].acceptsFolder() {
		t.Error("no file code, no folder")
	}
}

func TestAcceptsFolderVSCode(t *testing.T) {
	// VS Code's own entry doesn't declare inode/directory.
	e := &entry{Exec: "/usr/share/code/code %F", MimeTypes: []string{"application/x-code-workspace"}}
	if !e.acceptsFolder() {
		t.Error("VS Code opens folders")
	}
	if (&entry{Exec: "/usr/bin/google-chrome-stable %U"}).acceptsFolder() {
		t.Error("a browser is no folder app")
	}
}

func TestProfileFor(t *testing.T) {
	flags, env := profileFor("/usr/bin/google-chrome-stable", "/p")
	if !slices.Contains(flags, "--user-data-dir=/p/data") || env != nil {
		t.Errorf("chrome: %q %q", flags, env)
	}
	flags, env = profileFor("/usr/share/code/code", "/p")
	if !slices.Contains(flags, "--user-data-dir=/p/data") || !slices.Contains(flags, "--extensions-dir=/p/extensions") || !slices.Contains(flags, "--shared-data-dir=/p/shared") || !slices.Contains(env, "COPILOT_HOME=/p/copilot") {
		t.Errorf("code: %q %q", flags, env)
	}
	flags, env = profileFor("/usr/bin/firefox", "/p")
	if !slices.Contains(flags, "--profile") || env != nil {
		t.Errorf("firefox: %q %q", flags, env)
	}
	flags, env = profileFor("/usr/bin/gimp", "/p")
	if flags != nil || !slices.Contains(env, "XDG_CONFIG_HOME=/p/config") {
		t.Errorf("generic: %q %q", flags, env)
	}
	// A snap can't reach the hub's data folder.
	if flags, _ = profileFor("/snap/bin/chromium", "/p"); flags != nil {
		t.Errorf("snap: %q", flags)
	}
}

func TestBinaryIndex(t *testing.T) {
	if i := binaryIndex([]string{"env", "A=1", "-u", "B", "app"}); i != 3 {
		// `-u B` is unusual in Exec lines; B is taken for the program, which
		// only costs the family lookup.
		t.Errorf("env with flag: %d", i)
	}
	if i := binaryIndex([]string{"env", "A=1", "app", "x"}); i != 2 {
		t.Errorf("env: %d", i)
	}
	if i := binaryIndex([]string{"app", "A=1"}); i != 0 {
		t.Errorf("plain: %d", i)
	}
}

func TestStrippedEnv(t *testing.T) {
	for _, kv := range []string{
		"VSCODE_IPC_HOOK_CLI=/x", "ELECTRON_RUN_AS_NODE=1", "CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=x", "NEXTBOARD_SESSION_ID=s",
		"DISPLAY=:0", "XAUTHORITY=/x", "DBUS_SESSION_BUS_ADDRESS=x", "XDG_CURRENT_DESKTOP=XFCE", "SSH_CONNECTION=x", "TERM_PROGRAM=vscode", "XDG_SESSION_TYPE=tty",
	} {
		if !strippedEnv(kv) {
			t.Errorf("%s must be stripped", kv)
		}
	}
	for _, kv := range []string{"HOME=/h", "USER=u", "LANG=C", "XDG_RUNTIME_DIR=/run/user/1"} {
		if strippedEnv(kv) {
			t.Errorf("%s must stay", kv)
		}
	}
}

func TestResolveIcon(t *testing.T) {
	d := t.TempDir()
	mk := func(rel string) string {
		p := filepath.Join(d, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("HOME", t.TempDir())
	big := mk("icons/hicolor/512x512/apps/one.png")
	mk("icons/hicolor/48x48/apps/one.png")
	pix := mk("pixmaps/two.png")
	themed := mk("icons/Yaru/apps/128/three.svg")
	abs := mk("own/icon.png")
	xpm := mk("own/icon.xpm")

	for name, want := range map[string]string{
		"one": big, "two": pix, "three": themed, abs: abs, xpm: "", "": "",
		"missing": "", "../pixmaps/two": "", "o*": "",
	} {
		if got := resolveIcon(name, []string{d}); got != want {
			t.Errorf("resolveIcon(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestXauthorityFormat(t *testing.T) {
	cookie := []byte(strings.Repeat("c", 16))
	b := xauthority(cookie)
	if b[0] != 0xff || b[1] != 0xff {
		t.Errorf("family: %x", b[:2])
	}
	if !strings.Contains(string(b), "MIT-MAGIC-COOKIE-1") || string(b[len(b)-16:]) != string(cookie) {
		t.Errorf("cookie: %q", b)
	}
}
