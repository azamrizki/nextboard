package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelFlagsInTheWrapper(t *testing.T) {
	byID := func(id string) Agent {
		for _, a := range append(Builtins(), Catalog()...) {
			if a.ID == id {
				return a
			}
		}
		t.Fatalf("no agent %s", id)
		return Agent{}
	}
	claude := byID("claude")
	if !claude.Models || !claude.Efforts {
		t.Fatalf("%+v", claude)
	}
	s := WrapperScript(claude, "", nil)
	for _, want := range []string{`--model "$NB_MODEL"`, `--effort "$NB_EFFORT"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("no %s in %s", want, s)
		}
	}
	s = WrapperScript(byID("codex"), "", nil)
	for _, want := range []string{`-m "$NB_MODEL"`, `-c "model_reasoning_effort=$NB_EFFORT"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("no %s in %s", want, s)
		}
	}
	oc := byID("opencode")
	if !oc.Models || oc.Efforts || strings.Contains(WrapperScript(oc, "", nil), "NB_EFFORT") {
		t.Fatalf("opencode has no effort flag")
	}
	if a := byID("goose"); a.Models || strings.Contains(WrapperScript(a, "", nil), "NB_MODEL") {
		t.Fatal("goose has no known flag")
	}
	// The values go per launch, quoted.
	p, err := Build(claude, Options{Mode: ModeFresh, Model: "claude-fable-5-1[1m]", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Shell, `NB_MODEL='claude-fable-5-1[1m]' NB_EFFORT='high' exec `) && !strings.HasPrefix(p.Shell, `NB_MODEL='claude-fable-5-1[1m]' NB_EFFORT=high exec `) {
		t.Fatalf("shell line %q", p.Shell)
	}
	if p, _ = Build(byID("goose"), Options{Mode: ModeFresh, Model: "x", Effort: "high"}); strings.Contains(p.Shell, "NB_MODEL") {
		t.Fatalf("a CLI with no flag: %q", p.Shell)
	}
	for _, bad := range []string{"a b", "$(x)", "a;b", "`x`", "-flag"} {
		if ValidModel(bad) {
			t.Fatalf("%q passes", bad)
		}
	}
	if !ValidModel("claude-fable-5-1[1m]") || !ValidModel("openai/gpt-6.1-sol") || !ValidEffort("xhigh") || ValidEffort("High Effort") {
		t.Fatal("valid ones refused")
	}
}

func TestListModels(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	codex := Agent{ID: "codex", Kind: KindAgent, Source: SourceBuiltin, Command: "no-such-codex"}
	// no cache and no CLI: nothing, and no error
	if l := ListModels(ctx, codex, "", dir); len(l.Models) != 0 || l.Source != "none" {
		t.Fatalf("%+v", l)
	}
	write("models_cache.json", `{"models":[
	  {"slug":"gpt-6-astra","display_name":"GPT-6-Astra","visibility":"list","priority":2,"default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low"},{"effort":"ultra"}]},
	  {"slug":"gpt-reserve","display_name":"GPT-Reserve","visibility":"hide","priority":1},
	  {"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","description":"Latest","visibility":"list","priority":1,"default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"medium"}]}]}`)
	write("config.toml", "model = \"gpt-6-astra\"\nmodel_reasoning_effort = \"high\"\n[profiles.x]\nmodel = \"nope\"\n")
	l := ListModels(ctx, codex, "", dir)
	if l.Source != "cache" || len(l.Models) != 2 || l.Models[0].ID != "gpt-6.1-sol" || l.Models[0].DefaultEffort != "medium" {
		t.Fatalf("%+v", l)
	}
	if strings.Join(l.Models[1].Efforts, ",") != "low,ultra" || l.Current != "gpt-6-astra" || l.CurrentEffort != "high" {
		t.Fatalf("%+v", l)
	}

	claude := Agent{ID: "claude", Kind: KindAgent, Source: SourceBuiltin, Command: "claude"}
	write("settings.json", `{"model":"claude-opus-5-5","effortLevel":"xhigh"}`)
	write(".claude.json", `{"additionalModelOptionsCache":[{"value":"claude-fable-5-1[1m]","label":"Fable","description":"Most capable"}]}`)
	l = ListModels(ctx, claude, "", dir)
	var ids []string
	for _, m := range l.Models {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, " ") != "opus sonnet haiku claude-fable-5-1[1m]" || l.Current != "claude-opus-5-5" || l.CurrentEffort != "xhigh" || !strings.Contains(strings.Join(l.Efforts, ","), "max") {
		t.Fatalf("%+v", l)
	}

	// a custom agent on a base lists like its base; one on nothing lists nothing
	if l := ListModels(ctx, Agent{ID: "mine", Kind: KindAgent, Source: SourceCustom, Base: "claude"}, "", dir); len(l.Models) != 4 {
		t.Fatalf("%+v", l)
	}
	if l := ListModels(ctx, Agent{ID: "goose", Kind: KindAgent, Source: SourceCatalog}, "", dir); len(l.Models) != 0 || l.Source != "none" {
		t.Fatalf("%+v", l)
	}
}
