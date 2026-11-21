package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
)

// All are the built-in tools: Read, Write, Edit, Bash, Glob, Grep, WebFetch, TodoWrite.
func All() []Tool {
	return []Tool{readTool(), writeTool(), editTool(), bashTool(), globTool(), grepTool(), webFetchTool(), todoTool()}
}

// EditPath is the absolute path a Write or an Edit call would change ("" = not an edit, or no path).
func EditPath(env *Env, input json.RawMessage) string {
	var in struct {
		FilePath string `json:"file_path"`
	}
	if json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.FilePath) == "" {
		return ""
	}
	if env == nil {
		env = &Env{}
	}
	return resolve(env, in.FilePath)
}

// tool is a built-in tool: what the model sees of it, its kind, and the function that runs it.
type tool struct {
	def  llm.ToolDef
	kind string
	run  func(ctx context.Context, env *Env, input json.RawMessage) Result
}

func newTool(name, kind, description, schema string, run func(context.Context, *Env, json.RawMessage) Result) *tool {
	return &tool{def: llm.ToolDef{Name: name, Description: description, Schema: json.RawMessage(schema)}, kind: kind, run: run}
}

func (t *tool) Def() llm.ToolDef { return t.def }

func (t *tool) Kind() string { return t.kind }

// Run runs the tool. A bug in a tool becomes an error the model reads, never a crash of the agent.
func (t *tool) Run(ctx context.Context, env *Env, input json.RawMessage) (res Result) {
	defer func() {
		if r := recover(); r != nil {
			res = fail("%s failed: %v", t.def.Name, r)
		}
	}()
	if env == nil {
		return fail("%s has no environment to run in", t.def.Name)
	}
	if ctx.Err() != nil {
		return stopped()
	}
	return t.run(ctx, env, input)
}

// decode reads a tool's input into v. A malformed input is the model's mistake, said plainly
// so that it can call again.
func decode(name string, input json.RawMessage, v any) error {
	if len(bytes.TrimSpace(input)) == 0 || string(bytes.TrimSpace(input)) == "null" {
		input = json.RawMessage("{}")
	}
	err := json.Unmarshal(input, v)
	if err == nil {
		return nil
	}
	var te *json.UnmarshalTypeError
	var se *json.SyntaxError
	switch {
	case errors.As(err, &te) && te.Field != "":
		return fmt.Errorf("invalid input for %s: %s has the wrong type (%s given)", name, te.Field, te.Value)
	case errors.As(err, &te):
		return fmt.Errorf("invalid input for %s: it must be a JSON object", name)
	case errors.As(err, &se):
		return fmt.Errorf("invalid input for %s: not valid JSON (%v)", name, err)
	}
	return fmt.Errorf("invalid input for %s: %v", name, err)
}

// number is a whole-number argument. Models sometimes send one as a string ("20") or as a
// float (20.0); both are taken.
type number int

func (n *number) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		return nil
	}
	s = strings.TrimSpace(strings.Trim(s, `"`))
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > math.MaxInt32 {
		return fmt.Errorf("%s is not a whole number", b)
	}
	*n = number(f)
	return nil
}

// flag is a true/false argument; "true" and "false" as strings are taken too.
type flag bool

func (f *flag) UnmarshalJSON(b []byte) error {
	switch strings.Trim(strings.TrimSpace(string(b)), `"`) {
	case "true":
		*f = true
	case "false", "null", "":
		*f = false
	default:
		return fmt.Errorf("%s is not true or false", b)
	}
	return nil
}

// resolve is the absolute, cleaned form of a path the model gave: "~" is the home folder and
// a relative path is taken from the working folder. "" when it cannot be made absolute.
func resolve(env *Env, p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home := homeDir(env); home != "" {
			p = filepath.Join(home, p[1:])
		}
	}
	if !filepath.IsAbs(p) {
		if env.Cwd == "" {
			return ""
		}
		p = filepath.Join(env.Cwd, p)
	}
	return filepath.Clean(p)
}

// homeDir is the home folder of the commands the agent runs, or the agent's own.
func homeDir(env *Env) string {
	for _, kv := range env.Environ {
		if h, ok := strings.CutPrefix(kv, "HOME="); ok && h != "" {
			return h
		}
	}
	h, _ := os.UserHomeDir()
	return h
}

// pathArg resolves the path argument of a tool, or says why it cannot.
func pathArg(env *Env, name, field, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("%s needs %s", name, field)
	}
	abs := resolve(env, p)
	if abs == "" {
		return "", fmt.Errorf("%s: %q is relative and there is no working folder; give an absolute path", name, p)
	}
	return abs, nil
}

// fileError says what is wrong with a path the model gave, in its terms.
func fileError(env *Env, path string, err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		if env.Cwd != "" {
			return fmt.Sprintf("File does not exist: %s (the working folder is %s)", path, env.Cwd)
		}
		return "File does not exist: " + path
	case errors.Is(err, os.ErrPermission):
		return "Permission denied: " + path
	}
	return err.Error()
}

// irregular is true for what is not a file to read or write: a device, a pipe or a socket.
func irregular(m os.FileMode) bool {
	return m&(os.ModeDevice|os.ModeCharDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeIrregular) != 0
}

func ok(s string) Result { return Result{Output: s} }

func fail(format string, a ...any) Result {
	return Result{Output: fmt.Sprintf(format, a...), IsError: true}
}

// stopped is what a tool gives when the turn ended (the owner's Stop) while it ran.
func stopped() Result { return Result{Output: "Stopped", IsError: true} }

// plural is "1 line" or "3 lines".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
