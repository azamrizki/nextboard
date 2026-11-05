package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Which CLIs can be told a model and an effort at launch (docs/API-agents.md §25).
// The flags are per launch: nothing is written to a CLI's own config.
var modelFlags = map[string]struct {
	model  string
	effort string // "" = no such thing; a value with = goes after it, as one word
}{
	"claude":   {model: "--model", effort: "--effort"},
	"codex":    {model: "-m", effort: "-c model_reasoning_effort="},
	"gemini":   {model: "-m"},
	"opencode": {model: "-m"},
}

var (
	modelRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,127}$`)
	effortRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)
)

// ValidModel reports whether m can be passed to a CLI ("" = the CLI's own choice).
func ValidModel(m string) bool { return m == "" || modelRe.MatchString(m) }

// ValidEffort reports whether e can be passed to a CLI ("" = the CLI's own choice).
func ValidEffort(e string) bool { return e == "" || effortRe.MatchString(e) }

// HasModels reports whether a launch of this agent can name a model.
func (a Agent) HasModels() bool {
	return a.Kind == KindAgent && modelFlags[a.CapBase()].model != ""
}

// HasEfforts reports whether a launch of this agent can name an effort.
func (a Agent) HasEfforts() bool {
	return a.Kind == KindAgent && modelFlags[a.CapBase()].effort != ""
}

// modelScript adds the flags to the wrapper's command: the values come per launch
// in $NB_MODEL and $NB_EFFORT, so that one typed into an idle shell can differ.
func modelScript(a Agent) string {
	if !a.HasModels() {
		return ""
	}
	f := modelFlags[a.CapBase()]
	out := `[ -n "$NB_MODEL" ] && set -- "$@" ` + f.model + ` "$NB_MODEL"` + "\n"
	if flag, ok := strings.CutSuffix(f.effort, "="); ok {
		name, value, _ := strings.Cut(flag, " ")
		out += `[ -n "$NB_EFFORT" ] && set -- "$@" ` + name + ` "` + value + `=$NB_EFFORT"` + "\n"
	} else if f.effort != "" {
		out += `[ -n "$NB_EFFORT" ] && set -- "$@" ` + f.effort + ` "$NB_EFFORT"` + "\n"
	}
	return out
}

// Model is one row of an agent's model list.
type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Efforts       []string `json:"efforts,omitempty"`
	DefaultEffort string   `json:"defaultEffort,omitempty"`
}

// ModelList is GET /api/machines/{id}/agents/{agent}/models.
type ModelList struct {
	Models []Model `json:"models"`
	// Efforts applies to a model that lists none of its own.
	Efforts []string `json:"efforts"`
	// Current and CurrentEffort are what the CLI uses when told nothing.
	Current       string `json:"current,omitempty"`
	CurrentEffort string `json:"currentEffort,omitempty"`
	// Source says where the list came from: cache | command | known | none.
	Source string `json:"source"`
}

const modelListMax = 400

// ListModels asks what the CLI itself knows. configDir is its config folder for
// this login, command the program to run. A CLI with no list gets an empty one:
// a model can still be typed.
func ListModels(ctx context.Context, a Agent, command, configDir string) ModelList {
	out := ModelList{Models: []Model{}, Efforts: []string{}, Source: "none"}
	if !a.HasModels() {
		return out
	}
	if command == "" {
		command = a.Command
	}
	switch a.CapBase() {
	case "claude":
		return claudeModels(configDir)
	case "codex":
		return codexModels(ctx, command, configDir)
	case "opencode":
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		b, err := exec.CommandContext(cctx, command, "models").Output()
		if err != nil {
			return out
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() && len(out.Models) < modelListMax {
			if id := strings.TrimSpace(sc.Text()); ValidModel(id) && id != "" {
				out.Models = append(out.Models, Model{ID: id, Name: id})
			}
		}
		out.Source = "command"
	}
	return out
}

// claudeModels: the CLI's aliases follow its latest models and need no list. What
// the account offers on top is in its config, as the /model picker reads it.
func claudeModels(dir string) ModelList {
	out := ModelList{Source: "known", Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Models: []Model{
		{ID: "opus", Name: "Opus"}, {ID: "sonnet", Name: "Sonnet"}, {ID: "haiku", Name: "Haiku"},
	}}
	var st struct {
		Model  string `json:"model"`
		Effort string `json:"effortLevel"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil && json.Unmarshal(b, &st) == nil {
		out.Current, out.CurrentEffort = st.Model, st.Effort
	}
	// the default folder keeps this file next to it, a profile inside it
	file := filepath.Join(dir, ".claude.json")
	if filepath.Base(dir) == ".claude" {
		file = filepath.Join(filepath.Dir(dir), ".claude.json")
	}
	var cfg struct {
		Options []struct {
			Value       string `json:"value"`
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"additionalModelOptionsCache"`
	}
	if b, err := os.ReadFile(file); err == nil && json.Unmarshal(b, &cfg) == nil {
		for _, o := range cfg.Options {
			if o.Value != "" && ValidModel(o.Value) {
				out.Models = append(out.Models, Model{ID: o.Value, Name: o.Label, Description: o.Description})
			}
		}
	}
	return out
}

type codexCatalog struct {
	Models []struct {
		Slug        string `json:"slug"`
		Name        string `json:"display_name"`
		Description string `json:"description"`
		Visibility  string `json:"visibility"`
		Priority    int    `json:"priority"`
		Default     string `json:"default_reasoning_level"`
		Levels      []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	} `json:"models"`
}

// codexModels reads the cache Codex keeps of what this login may use, and asks
// the CLI when it has not made one yet.
func codexModels(ctx context.Context, command, dir string) ModelList {
	out := ModelList{Models: []Model{}, Efforts: []string{}, Source: "cache"}
	if b, err := os.ReadFile(filepath.Join(dir, "config.toml")); err == nil {
		out.Current, out.CurrentEffort = codexConfigModel(b)
	}
	b, err := os.ReadFile(filepath.Join(dir, "models_cache.json"))
	if err != nil {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, command, "debug", "models")
		cmd.Env = append(os.Environ(), "CODEX_HOME="+dir)
		if b, err = cmd.Output(); err != nil {
			out.Source = "none"
			return out
		}
		out.Source = "command"
	}
	var cat codexCatalog
	if json.Unmarshal(b, &cat) != nil {
		out.Source = "none"
		return out
	}
	sort.SliceStable(cat.Models, func(i, j int) bool { return cat.Models[i].Priority < cat.Models[j].Priority })
	for _, m := range cat.Models {
		if m.Visibility != "list" || !ValidModel(m.Slug) || m.Slug == "" {
			continue
		}
		row := Model{ID: m.Slug, Name: m.Name, Description: m.Description, DefaultEffort: m.Default}
		for _, l := range m.Levels {
			row.Efforts = append(row.Efforts, l.Effort)
		}
		out.Models = append(out.Models, row)
	}
	return out
}

var codexConfigRe = regexp.MustCompile(`^(model|model_reasoning_effort)\s*=\s*"([^"]*)"`)

// codexConfigModel reads the top of config.toml: keys below a [table] are not these.
func codexConfigModel(b []byte) (model, effort string) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			break
		}
		if m := codexConfigRe.FindStringSubmatch(line); m != nil {
			if m[1] == "model" {
				model = m[2]
			} else {
				effort = m[2]
			}
		}
	}
	return model, effort
}
