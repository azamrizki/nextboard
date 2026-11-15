package instance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/config"
	"github.com/azamrizki/nextboard/internal/store"
)

func TestValidName(t *testing.T) {
	for _, ok := range []string{"work", "a", "dev-2", "default", "personal", "x" + strings.Repeat("a", 23)} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Work", "1abc", "-x", "a_b", "a b", "../x", "x/y", "a.b", "x" + strings.Repeat("a", 24), "wörk"} {
		if err := ValidName(bad); !errors.Is(err, ErrBadName) {
			t.Errorf("%q accepted (%v)", bad, err)
		}
	}
}

func TestNamesPerInstance(t *testing.T) {
	home := "/home/alice"
	for _, c := range []struct{ name, dir, svc, label, unit, plist string }{
		{"default", "/home/alice/.nextboard", "nextboard", "id.nextboard", "/home/alice/.config/systemd/user/nextboard.service", "/home/alice/Library/LaunchAgents/id.nextboard.plist"},
		{"work", "/home/alice/.nextboard-work", "nextboard-work", "id.nextboard.work", "/home/alice/.config/systemd/user/nextboard-work.service", "/home/alice/Library/LaunchAgents/id.nextboard.work.plist"},
	} {
		if got := dirIn(home, c.name); got != c.dir {
			t.Errorf("dir %s: %s", c.name, got)
		}
		if got := ServiceName(c.name); got != c.svc {
			t.Errorf("service %s: %s", c.name, got)
		}
		if got := LaunchLabel(c.name); got != c.label {
			t.Errorf("label %s: %s", c.name, got)
		}
		if got, _ := unitPathIn(home, "linux", c.name); got != c.unit {
			t.Errorf("unit %s: %s", c.name, got)
		}
		if got, _ := unitPathIn(home, "darwin", c.name); got != c.plist {
			t.Errorf("plist %s: %s", c.name, got)
		}
	}
	t.Setenv("HOME", "/home/alice")
	if d, err := DirFor("work"); err != nil || d != "/home/alice/.nextboard-work" {
		t.Errorf("DirFor: %s %v", d, err)
	}
}

func TestLegacyName(t *testing.T) {
	for dir, want := range map[string]string{
		"/home/alice/.nextboard":          "default",
		"/home/alice/.nextboard-work":     "work",
		"/srv/data/.nextboard-personal":   "personal",
		"/srv/nb":                         "default",
		"/home/alice/apps/nb/.devdata":    "default",
		"/tmp/nb-hub-1234":                "default",
		"/home/alice/.nextboard-Old":      "default",
		"/home/alice/.nextboard-":         "default",
		"/home/alice/.nextboard-work/sub": "default",
	} {
		if got := LegacyName(dir); got != want {
			t.Errorf("%s: %s, want %s", dir, got, want)
		}
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEnsureIdentityFreshFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	id, err := EnsureIdentity(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "default" || !id.Recorded || !id.Exists || !hubIDRe.MatchString(id.HubID) || len(id.HubID) != 10 {
		t.Fatalf("%+v", id)
	}
	m := readJSON(t, filepath.Join(dir, config.File))
	if len(m) != 2 || m["instance"] != "default" || m["hubId"] != id.HubID {
		t.Fatalf("config.json %v", m)
	}
	if fi, _ := os.Stat(filepath.Join(dir, config.File)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// the hub's defaults are what they were
	st, _ := store.Open(dir)
	cfg, err := config.Load(st)
	if err != nil || cfg.Listen != config.DefaultListen || cfg.HubName != "" || cfg.Instance != "default" || cfg.HubID != id.HubID {
		t.Fatalf("%+v %v", cfg, err)
	}
}

// A config.json from before instances: everything in it stays, the name comes from the path,
// and a second start changes nothing.
func TestEnsureIdentityLegacyConfig(t *testing.T) {
	for _, c := range []struct{ base, want string }{{".nextboard", "default"}, {".nextboard-work", "work"}, {"nb", "default"}} {
		dir := filepath.Join(t.TempDir(), c.base)
		os.MkdirAll(dir, 0o700)
		legacy := `{"hubName":"devbox","listen":"0.0.0.0:12312","statusListen":"127.0.0.1:12399","extraUrls":["https://board.example.com"],"futureField":{"a":[1,2]}}`
		if err := os.WriteFile(filepath.Join(dir, config.File), []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		id, err := EnsureIdentity(dir, "")
		if err != nil {
			t.Fatal(err)
		}
		if id.Name != c.want || id.HubID == "" {
			t.Fatalf("%s: %+v", c.base, id)
		}
		m := readJSON(t, filepath.Join(dir, config.File))
		var before map[string]any
		json.Unmarshal([]byte(legacy), &before)
		for k, v := range before {
			if got, _ := json.Marshal(m[k]); string(got) != string(must(json.Marshal(v))) {
				t.Errorf("%s: %s changed to %s", c.base, k, got)
			}
		}
		if m["instance"] != c.want || m["hubId"] != id.HubID || len(m) != len(before)+2 {
			t.Errorf("%s: %v", c.base, m)
		}
		st, _ := store.Open(dir)
		cfg, _ := config.Load(st)
		if cfg.HubName != "devbox" || cfg.Listen != "0.0.0.0:12312" || cfg.StatusListen != "127.0.0.1:12399" || len(cfg.ExtraURLs) != 1 {
			t.Errorf("%s: what the hub sees changed: %+v", c.base, cfg)
		}

		raw1, _ := os.ReadFile(filepath.Join(dir, config.File))
		again, err := EnsureIdentity(dir, "")
		raw2, _ := os.ReadFile(filepath.Join(dir, config.File))
		if err != nil || again.HubID != id.HubID || again.Name != id.Name || string(raw1) != string(raw2) {
			t.Errorf("%s: second start %+v %v, changed=%v", c.base, again, err, string(raw1) != string(raw2))
		}
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestEnsureIdentityWanted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".devdata")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, config.File), []byte(`{"hubName":"dev"}`), 0o600)
	// a folder that records no instance yet is claimed by the one asked for
	id, err := EnsureIdentity(dir, "dev")
	if err != nil || id.Name != "dev" {
		t.Fatalf("%+v %v", id, err)
	}
	if id, err := EnsureIdentity(dir, "dev"); err != nil || id.Name != "dev" {
		t.Fatalf("%+v %v", id, err)
	}
	// then it is dev's
	_, err = EnsureIdentity(dir, "work")
	var mm *MismatchError
	if !errors.As(err, &mm) || mm.Has != "dev" || mm.Want != "work" || !strings.Contains(err.Error(), "data dir "+dir+" belongs to instance dev") {
		t.Fatalf("%v", err)
	}
	if id, err := EnsureIdentity(dir, ""); err != nil || id.Name != "dev" {
		t.Fatalf("%+v %v", id, err)
	}
	if _, err := EnsureIdentity(dir, "Bad"); !errors.Is(err, ErrBadName) {
		t.Fatalf("%v", err)
	}
}

func TestEnsureIdentityRefusesBrokenConfig(t *testing.T) {
	for _, body := range []string{`{"instance":"Not Valid"}`, `{"hubId":"x;rm -rf"}`, `{"instance":3}`, `{not json`} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, config.File), []byte(body), 0o600)
		if _, err := EnsureIdentity(dir, ""); err == nil {
			t.Errorf("%s accepted", body)
		}
		if raw, _ := os.ReadFile(filepath.Join(dir, config.File)); string(raw) != body {
			t.Errorf("%s rewritten: %s", body, raw)
		}
	}
}
