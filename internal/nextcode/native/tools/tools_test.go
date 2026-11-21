package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// call runs the tool named name with input (marshalled unless it is already raw JSON).
func call(t *testing.T, name string, env *Env, input any) Result {
	t.Helper()
	return callCtx(context.Background(), t, name, env, input)
}

func callCtx(ctx context.Context, t *testing.T, name string, env *Env, input any) Result {
	t.Helper()
	var raw json.RawMessage
	switch v := input.(type) {
	case json.RawMessage:
		raw = v
	case string:
		raw = json.RawMessage(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		raw = b
	}
	return toolNamed(t, name).Run(ctx, env, raw)
}

func toolNamed(t *testing.T, name string) Tool {
	t.Helper()
	for _, tl := range All() {
		if tl.Def().Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %s", name)
	return nil
}

func mustContain(t *testing.T, got Result, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got.Output, w) {
			t.Errorf("output lacks %q:\n%s", w, got.Output)
		}
	}
}

func TestAllOrderKindsAndSchemas(t *testing.T) {
	want := []struct{ name, kind string }{
		{"Read", KindRead}, {"Write", KindEdit}, {"Edit", KindEdit}, {"Bash", KindExec},
		{"Glob", KindRead}, {"Grep", KindRead}, {"WebFetch", KindFetch}, {"TodoWrite", KindMeta},
	}
	all := All()
	if len(all) != len(want) {
		t.Fatalf("All() has %d tools, want %d", len(all), len(want))
	}
	for i, w := range want {
		d := all[i].Def()
		if d.Name != w.name || all[i].Kind() != w.kind {
			t.Errorf("tool %d = %s/%s, want %s/%s", i, d.Name, all[i].Kind(), w.name, w.kind)
		}
		if len(d.Description) < 100 {
			t.Errorf("%s: description too short", d.Name)
		}
		var s struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(d.Schema, &s); err != nil {
			t.Errorf("%s: schema is not JSON: %v", d.Name, err)
			continue
		}
		if s.Type != "object" || len(s.Required) == 0 {
			t.Errorf("%s: schema type %q, required %v", d.Name, s.Type, s.Required)
		}
		for _, r := range s.Required {
			if _, ok := s.Properties[r]; !ok {
				t.Errorf("%s: required %q is not a property", d.Name, r)
			}
		}
	}
}

func TestBadInputIsAnError(t *testing.T) {
	env := &Env{Cwd: t.TempDir()}
	for _, tl := range All() {
		name := tl.Def().Name
		for _, in := range []string{`{not json`, `[1,2]`, `{}`, ``} {
			r := tl.Run(context.Background(), env, json.RawMessage(in))
			if !r.IsError || r.Output == "" {
				t.Errorf("%s(%s) = %+v, want an error", name, in, r)
			}
		}
		if r := tl.Run(context.Background(), nil, json.RawMessage(`{}`)); !r.IsError {
			t.Errorf("%s with no env: %+v", name, r)
		}
	}
	r := call(t, "Read", env, `{"file_path": 12}`)
	mustContain(t, r, "file_path", "wrong type")
}

func TestCancelledBeforeRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := callCtx(ctx, t, "Bash", &Env{Cwd: t.TempDir()}, map[string]any{"command": "echo hi"})
	if !r.IsError || r.Output != "Stopped" {
		t.Errorf("got %+v", r)
	}
}

func TestLenientNumbersAndFlags(t *testing.T) {
	var in struct {
		N *number `json:"n"`
		F flag    `json:"f"`
	}
	for _, s := range []string{`{"n": 20, "f": true}`, `{"n": "20", "f": "true"}`, `{"n": 20.0, "f": true}`} {
		in.N, in.F = nil, false
		if err := decode("X", json.RawMessage(s), &in); err != nil || in.N == nil || *in.N != 20 || !in.F {
			t.Errorf("%s: %v %v %v", s, err, in.N, in.F)
		}
	}
	for _, s := range []string{`{"n": 2.5}`, `{"n": "x"}`, `{"f": "yes"}`} {
		if err := decode("X", json.RawMessage(s), &in); err == nil {
			t.Errorf("%s: no error", s)
		}
	}
}

func TestEditPath(t *testing.T) {
	env := &Env{Cwd: "/work/app", Environ: []string{"HOME=/home/me"}}
	cases := []struct {
		in   string
		want string
	}{
		{`{"file_path": "/etc/hosts", "content": "x"}`, "/etc/hosts"},
		{`{"file_path": "src/main.go", "old_string": "a", "new_string": "b"}`, "/work/app/src/main.go"},
		{`{"file_path": "./a/../b.txt"}`, "/work/app/b.txt"},
		{`{"file_path": "/x//y/./z/"}`, "/x/y/z"},
		{`{"file_path": "~/notes.md"}`, "/home/me/notes.md"},
		{`{"file_path": ""}`, ""},
		{`{"content": "x"}`, ""},
		{`{"file_path": 3}`, ""},
		{`not json`, ""},
		{``, ""},
	}
	for _, c := range cases {
		if got := EditPath(env, json.RawMessage(c.in)); got != c.want {
			t.Errorf("EditPath(%s) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := EditPath(nil, json.RawMessage(`{"file_path": "/a/b"}`)); got != "/a/b" {
		t.Errorf("nil env, absolute: %q", got)
	}
	if got := EditPath(&Env{}, json.RawMessage(`{"file_path": "rel"}`)); got != "" {
		t.Errorf("no cwd, relative: %q", got)
	}
	if got := EditPath(env, json.RawMessage(`{"file_path": "x"}`)); !filepath.IsAbs(got) {
		t.Errorf("not absolute: %q", got)
	}
}
