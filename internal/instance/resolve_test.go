package instance

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/azamrizki/nextboard/internal/config"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeUnit(t *testing.T, home, file, execStart string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".config", "systemd", "user", file), "[Unit]\nDescription=NextBoard hub\n\n[Service]\nType=simple\nExecStart="+execStart+"\nEnvironment=HOME="+home+"\n")
}

func TestResolvePrecedence(t *testing.T) {
	home := t.TempDir()
	srv := t.TempDir()
	srvWork := filepath.Join(srv, "work-data")
	writeUnit(t, home, "nextboard-work.service", "/opt/nb/nextboard serve --instance work --data-dir "+srvWork)
	writeFile(t, filepath.Join(srvWork, config.File), `{"instance":"work","hubId":"k7qxm2pa9d"}`)
	personal := filepath.Join(srv, "personal")
	writeFile(t, filepath.Join(personal, config.File), `{"instance":"personal"}`)
	legacy := filepath.Join(srv, "nb")
	writeFile(t, filepath.Join(legacy, config.File), `{"hubName":"devbox"}`)
	devdata := filepath.Join(srv, ".devdata")
	inHome := func(n string) string { return filepath.Join(home, n) }

	type env map[string]string
	for _, c := range []struct {
		what     string
		req      Request
		env      env
		name     string
		dir      string
		from     Source
		recorded bool
	}{
		{"nothing", Request{}, nil, "default", inHome(".nextboard"), FromDefault, false},
		{"$NEXTBOARD_HOME", Request{}, env{"NEXTBOARD_HOME": legacy}, "default", legacy, FromEnvHome, false},
		{"$NEXTBOARD_INSTANCE", Request{}, env{"NEXTBOARD_INSTANCE": "side"}, "side", inHome(".nextboard-side"), FromEnvInstance, false},
		{"$NEXTBOARD_INSTANCE finds the unit", Request{}, env{"NEXTBOARD_INSTANCE": "work"}, "work", srvWork, FromEnvInstance, true},
		{"both env, agreeing", Request{}, env{"NEXTBOARD_HOME": personal, "NEXTBOARD_INSTANCE": "personal"}, "personal", personal, FromEnvHome, true},
		{"--instance over env", Request{Instance: "side"}, env{"NEXTBOARD_HOME": legacy, "NEXTBOARD_INSTANCE": "work"}, "side", inHome(".nextboard-side"), FromInstance, false},
		{"--instance from its unit", Request{Instance: "work"}, nil, "work", srvWork, FromInstance, true},
		{"--instance default", Request{Instance: "default"}, nil, "default", inHome(".nextboard"), FromInstance, false},
		{"--data-dir over everything", Request{DataDir: personal}, env{"NEXTBOARD_HOME": legacy, "NEXTBOARD_INSTANCE": "work"}, "personal", personal, FromDataDir, true},
		{"--data-dir of a legacy folder", Request{DataDir: legacy}, nil, "default", legacy, FromDataDir, false},
		{"--data-dir with its own --instance", Request{DataDir: personal, Instance: "personal"}, nil, "personal", personal, FromDataDir, true},
		{"--data-dir claimed by --instance", Request{DataDir: devdata, Instance: "dev"}, nil, "dev", devdata, FromDataDir, false},
		{"--data-dir legacy, claimed", Request{DataDir: legacy, Instance: "side"}, nil, "side", legacy, FromDataDir, false},
		{"--data-dir default as default", Request{DataDir: inHome(".nextboard"), Instance: "default"}, nil, "default", inHome(".nextboard"), FromDataDir, false},
	} {
		r, err := resolve(c.req, home, "linux", func(k string) string { return c.env[k] })
		if err != nil {
			t.Errorf("%s: %v", c.what, err)
			continue
		}
		if r.Name != c.name || r.DataDir != c.dir || r.From != c.from || r.Recorded != c.recorded {
			t.Errorf("%s: %+v, want %s %s %s recorded=%v", c.what, r, c.name, c.dir, c.from, c.recorded)
		}
	}
}

func TestResolveRefuses(t *testing.T) {
	home := t.TempDir()
	srv := t.TempDir()
	personal := filepath.Join(srv, "personal")
	writeFile(t, filepath.Join(personal, config.File), `{"instance":"personal"}`)
	legacy := filepath.Join(srv, "nb")
	writeFile(t, filepath.Join(legacy, config.File), `{"hubName":"devbox"}`)
	writeFile(t, filepath.Join(home, ".nextboard-side", config.File), `{"instance":"other"}`)
	broken := filepath.Join(srv, "broken")
	writeFile(t, filepath.Join(broken, config.File), `{"instance":`)

	type env map[string]string
	for _, c := range []struct {
		what string
		req  Request
		env  env
		has  string // MismatchError.Has, or "" for another error
		bad  bool   // ErrBadName
	}{
		{"--data-dir of another instance", Request{DataDir: personal, Instance: "work"}, nil, "personal", false},
		{"~/.nextboard is the default's", Request{DataDir: filepath.Join(home, ".nextboard"), Instance: "work"}, nil, "default", false},
		{".nextboard-personal is personal's", Request{DataDir: filepath.Join(home, ".nextboard-personal"), Instance: "work"}, nil, "personal", false},
		{".nextboard-work is not the default's", Request{DataDir: filepath.Join(home, ".nextboard-work"), Instance: "default"}, nil, "work", false},
		{"--instance whose folder says otherwise", Request{Instance: "side"}, nil, "other", false},
		{"env disagree", Request{}, env{"NEXTBOARD_HOME": legacy, "NEXTBOARD_INSTANCE": "work"}, "default", false},
		{"env disagree, recorded", Request{}, env{"NEXTBOARD_HOME": personal, "NEXTBOARD_INSTANCE": "work"}, "personal", false},
		{"bad --instance", Request{Instance: "Work"}, nil, "", true},
		{"bad --instance with --data-dir", Request{DataDir: personal, Instance: "../x"}, nil, "", true},
		{"bad $NEXTBOARD_INSTANCE", Request{}, env{"NEXTBOARD_INSTANCE": "a_b"}, "", true},
		{"broken config.json", Request{DataDir: broken}, nil, "", false},
	} {
		_, err := resolve(c.req, home, "linux", func(k string) string { return c.env[k] })
		if err == nil {
			t.Errorf("%s: no error", c.what)
			continue
		}
		var mm *MismatchError
		switch {
		case c.bad:
			if !errors.Is(err, ErrBadName) {
				t.Errorf("%s: %v, want a bad name", c.what, err)
			}
		case c.has != "":
			if !errors.As(err, &mm) || mm.Has != c.has {
				t.Errorf("%s: %v, want it to belong to %s", c.what, err, c.has)
			}
		default:
			if errors.As(err, &mm) || errors.Is(err, ErrBadName) {
				t.Errorf("%s: %v", c.what, err)
			}
		}
	}
}

// Resolve itself, as the CLI calls it: HOME and the environment of the process.
func TestResolveFromTheEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NEXTBOARD_HOME", "")
	t.Setenv("NEXTBOARD_INSTANCE", "")
	r, err := Resolve(Request{})
	if err != nil || r.Name != "default" || r.DataDir != filepath.Join(home, ".nextboard") || r.Exists {
		t.Fatalf("%+v %v", r, err)
	}
	t.Setenv("NEXTBOARD_INSTANCE", "personal")
	if r, err := Resolve(Request{}); err != nil || r.DataDir != filepath.Join(home, ".nextboard-personal") || r.Name != "personal" {
		t.Fatalf("%+v %v", r, err)
	}
	rel := filepath.Join("testdata-not-here", "nb")
	if r, err := Resolve(Request{DataDir: rel}); err != nil || !filepath.IsAbs(r.DataDir) || r.Name != "default" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat("testdata-not-here"); err == nil {
		t.Fatal("Resolve made a folder")
	}
}
