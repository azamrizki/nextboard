package settings

import (
	"errors"
	"os"
	"testing"
	"time"

	"nextboard/internal/store"
)

func open(t *testing.T, dir string) *Store {
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestDefaultsAndMergePatch(t *testing.T) {
	if d := Defaults().Usage; d.Background || d.Agents["codex"] || len(d.Agents) != 1 {
		t.Fatalf("usage defaults %+v: automatic checks and Codex checks must start off", d)
	}
	dir := t.TempDir()
	s := open(t, dir)
	d := s.Get()
	if d.SchemaVersion != 1 || !d.Agents.LiveStatus || d.Agents.HookChannel != "auto" || d.Notifications.FinishedMinSec != 30 ||
		d.Shortcuts.Spawn != "t" || d.Agents.RoleAgents["backend"] != "codex" || d.Runner.DefaultAgent != "claude" {
		t.Fatalf("defaults %+v", d)
	}
	changed := make(chan Settings, 4)
	s.OnChange = func(v Settings) { changed <- v }
	got, err := s.Patch([]byte(`{"agents":{"defaults":{"local":"codex"},"paths":{"local":{"claude":"/opt/claude"}},"yoloDefault":true,"roleAgents":{"frontend":"codex"}},"notifications":{"finishedMinSec":5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Agents.Defaults["local"] != "codex" || got.Agents.Paths["local"]["claude"] != "/opt/claude" || !got.Agents.YoloDefault ||
		got.Agents.RoleAgents["frontend"] != "codex" || got.Agents.RoleAgents["backend"] != "codex" || got.Notifications.FinishedMinSec != 5 || !got.Notifications.NeedsYou {
		t.Fatalf("patched %+v", got)
	}
	select {
	case v := <-changed:
		if !v.Agents.YoloDefault {
			t.Fatal("event content")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no change event")
	}
	// null resets to the default (also inside maps).
	got, err = s.Patch([]byte(`{"agents":{"yoloDefault":null,"roleAgents":{"frontend":null},"defaults":{"local":null}},"notifications":null}`))
	if err != nil || got.Agents.YoloDefault || got.Agents.RoleAgents["frontend"] != "claude" || len(got.Agents.Defaults) != 0 ||
		got.Notifications.FinishedMinSec != 30 || got.Agents.Paths["local"]["claude"] != "/opt/claude" {
		t.Fatalf("reset %+v %v", got, err)
	}
	// Persisted with 0600 and reloaded.
	fi, err := os.Stat(dir + "/settings.json")
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file %v %v", fi, err)
	}
	if again := open(t, dir).Get(); again.Agents.Paths["local"]["claude"] != "/opt/claude" {
		t.Fatalf("reload %+v", again)
	}
}

func TestValidation(t *testing.T) {
	s := open(t, t.TempDir())
	for patch, want := range map[string]string{
		`{"unknownKey":{}}`:                          "bad_setting",
		`{"agents":{"nope":1}}`:                      "bad_setting",
		`{"agents":{"hookChannel":"pigeon"}}`:        "bad_setting",
		`{"agents":{"resumeOnRestart":"maybe"}}`:     "bad_setting",
		`{"agents":{"defaults":{"local":"Bad ID"}}}`: "bad_setting",
		`{"notifications":{"finishedMinSec":-1}}`:    "bad_setting",
		`{"schemaVersion":2}`:                        "bad_setting",
		`[1,2]`:                                      "bad_setting",
		`{"shortcuts":{"pen":"t"}}`:                  "bad_shortcut", // duplicate of spawn
		`{"shortcuts":{"pen":"x"}}`:                  "bad_shortcut",
		`{"shortcuts":{"pen":" "}}`:                  "bad_shortcut",
		`{"shortcuts":{"pen":"P"}}`:                  "bad_shortcut",
		`{"shortcuts":{"pen":"pp"}}`:                 "bad_shortcut",
		`{"shortcuts":{"zoom":"z"}}`:                 "bad_setting",
	} {
		if _, err := s.Patch([]byte(patch)); code(err) != want {
			t.Errorf("%s: got %v", patch, err)
		}
	}
	if got, err := s.Patch([]byte(`{"shortcuts":{"pen":"9","eraser":";"}}`)); err != nil || got.Shortcuts.Pen != "9" {
		t.Fatalf("valid shortcuts %v", err)
	}
	if got, _ := s.Patch([]byte(`{"shortcuts":{"pen":null}}`)); got.Shortcuts.Pen != "p" {
		t.Fatal("shortcut reset")
	}
}

// A document saved before the Hand tool may have given its key to another action.
func TestHandKeyKeepsOlderBindings(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := map[string]any{"shortcuts": map[string]string{"git": "h"}, "notifications": map[string]int{"finishedMinSec": 99}}
	if err := st.Save(File, old); err != nil {
		t.Fatal(err)
	}
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	if got.Shortcuts.Git != "h" || got.Shortcuts.Hand != "j" || got.Notifications.FinishedMinSec != 99 {
		t.Fatalf("git %q hand %q finishedMinSec %d", got.Shortcuts.Git, got.Shortcuts.Hand, got.Notifications.FinishedMinSec)
	}
	if Defaults().Shortcuts.Hand != "h" {
		t.Fatal("default hand key")
	}
}

func TestOrchestrationSettings(t *testing.T) {
	s := open(t, t.TempDir())
	d := s.Get()
	if d.Delivery.WhileWorking != "queue" || d.Delivery.SubmitDelayMs != 200 || d.Delivery.NeedsYouTimeoutSec != 900 ||
		d.Commands.Planner != "auto" || d.Commands.ConfirmTTLSec != 60 || d.Commands.ReportTimeoutMin != 30 || d.Memory.AutoPush {
		t.Fatalf("defaults %+v %+v %+v", d.Delivery, d.Commands, d.Memory)
	}
	got, err := s.Patch([]byte(`{"delivery":{"whileWorking":"type","submitDelayMs":500},"commands":{"riskyPatterns":["\\bnpm publish\\b"]},"memory":{"autoPush":true}}`))
	if err != nil || got.Delivery.WhileWorking != "type" || got.Delivery.SubmitDelayMs != 500 || got.Delivery.WorkingTimeoutSec != 1800 ||
		len(got.Commands.RiskyPatterns) != 1 || !got.Memory.AutoPush {
		t.Fatalf("patch %v %+v", err, got)
	}
	for _, bad := range []string{`{"delivery":{"submitDelayMs":50}}`, `{"delivery":{"whileWorking":"now"}}`,
		`{"commands":{"confirmTtlSec":10}}`, `{"commands":{"planner":"cloud"}}`, `{"commands":{"riskyPatterns":["(unclosed"]}}`} {
		if _, err := s.Patch([]byte(bad)); code(err) != "bad_setting" {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if got, _ := s.Patch([]byte(`{"delivery":null}`)); got.Delivery.WhileWorking != "queue" || got.Delivery.SubmitDelayMs != 200 {
		t.Fatalf("reset %+v", got.Delivery)
	}
}
