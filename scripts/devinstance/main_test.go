package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/azamrizki/nextboard/internal/instance"
)

// scratchHome is an empty HOME: no unit and no ~/.nextboard of the person running the test is seen.
func scratchHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("NEXTBOARD_HOME", "")
	t.Setenv("NEXTBOARD_INSTANCE", "")
	return h
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readConfig(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return m
}

// startsAs is the name the hub records at its start (server.New runs EnsureIdentity with the
// name serve resolved; the dev scripts pass only --data-dir).
func startsAs(t *testing.T, dir string) instance.Identity {
	t.Helper()
	r, err := instance.Resolve(instance.Request{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	id, err := instance.EnsureIdentity(r.DataDir, r.Name)
	if err != nil {
		t.Fatalf("the hub would refuse to start: %v", err)
	}
	return id
}

// A new dev folder, one from before instances, and one a build in between labelled "default"
// all start as instance dev, keeping every other field; a second run changes nothing.
func TestDevFolderBecomesDev(t *testing.T) {
	root := scratchHome(t)
	for _, tc := range []struct {
		name, config string
	}{
		{"new", ""},
		{"legacy", `{"hubName":"devbox","listen":"0.0.0.0:12312","extraUrls":["https://dev.example.test"]}`},
		{"labelled default", `{"instance":"default","hubId":"k7qxm2pa9d","hubName":"devbox","listen":"0.0.0.0:12312"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(root, tc.name, ".devdata")
			if tc.config != "" {
				writeConfig(t, dir, tc.config)
			}
			msg, err := label(dir)
			if err != nil || msg == "" {
				t.Fatalf("label: %q %v", msg, err)
			}
			m := readConfig(t, dir)
			if m["instance"] != "dev" {
				t.Fatalf("config %v", m)
			}
			if tc.config != "" && (m["hubName"] != "devbox" || m["listen"] != "0.0.0.0:12312") {
				t.Fatalf("other fields lost: %v", m)
			}
			if tc.name == "labelled default" && m["hubId"] != "k7qxm2pa9d" {
				t.Fatalf("hubId changed: %v", m)
			}
			for p, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "config.json"): 0o600} {
				if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
					t.Fatalf("%s: %v %v", p, fi.Mode(), err)
				}
			}
			if id := startsAs(t, dir); id.Name != "dev" || id.HubID == "" {
				t.Fatalf("starts as %+v", id)
			}
			if msg, err := label(dir); err != nil || msg != "" {
				t.Fatalf("second run: %q %v", msg, err)
			}
		})
	}
}

// What is not the dev folder, or not free to change, keeps its name, and the hub still starts.
func TestOtherFoldersKeepTheirName(t *testing.T) {
	home := scratchHome(t)
	check := func(t *testing.T, dir, want string, created bool) {
		t.Helper()
		if _, err := label(dir); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(filepath.Join(dir, "config.json"))
		if exists := err == nil; exists != created {
			t.Fatalf("config.json exists=%v, want %v", exists, created)
		}
		if id := startsAs(t, dir); id.Name != want {
			t.Fatalf("starts as %q, want %q", id.Name, want)
		}
	}
	t.Run("default elsewhere", func(t *testing.T) {
		dir := filepath.Join(home, "srv-nb")
		writeConfig(t, dir, `{"instance":"default","hubId":"k7qxm2pa9d"}`)
		check(t, dir, "default", true)
	})
	t.Run("another name", func(t *testing.T) {
		dir := filepath.Join(home, "x", ".devdata")
		writeConfig(t, dir, `{"instance":"work","hubId":"k7qxm2pa9d"}`)
		check(t, dir, "work", true)
	})
	t.Run("the default instance's folder", func(t *testing.T) {
		check(t, filepath.Join(home, ".nextboard"), "default", false)
	})
	t.Run("a named instance's folder", func(t *testing.T) {
		check(t, filepath.Join(home, ".nextboard-personal"), "personal", false)
	})
	t.Run("a hub runs on it", func(t *testing.T) {
		dir := filepath.Join(home, "busy", ".devdata")
		writeConfig(t, dir, `{"instance":"default","hubId":"k7qxm2pa9d"}`)
		lock, err := instance.AcquireLock(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := label(dir); err != nil {
			t.Fatal(err)
		}
		lock.Release()
		if m := readConfig(t, dir); m["instance"] != "default" {
			t.Fatalf("relabelled under a running hub: %v", m)
		}
	})
	t.Run("a service runs it", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("systemd units")
		}
		dir := filepath.Join(home, "svc", ".devdata")
		writeConfig(t, dir, `{"instance":"default","hubId":"k7qxm2pa9d"}`)
		units := filepath.Join(home, ".config", "systemd", "user")
		if err := os.MkdirAll(units, 0o700); err != nil {
			t.Fatal(err)
		}
		unit := "[Service]\nExecStart=/opt/nb/nextboard serve --data-dir " + dir + "\n"
		if err := os.WriteFile(filepath.Join(units, "nextboard.service"), []byte(unit), 0o600); err != nil {
			t.Fatal(err)
		}
		check(t, dir, "default", true)
	})
}
