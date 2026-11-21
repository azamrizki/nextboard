// Package native is NextCode's own engine (docs/API-agents.md §30): a code agent that needs no
// vendor CLI. It runs as `nextboard nextcode-agent`, a child of the hub like the CLIs of the other
// engines, and talks to an API provider of the hub's directly (package llm), with tools of its
// own (package tools).
//
// It speaks, on its stdin and stdout, the subset of Claude Code's stream-json protocol that
// internal/nextcode's Claude engine reads (claude.go), so the driver, the chat, the requests and
// the views are the same for every engine.
package native

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
	"github.com/azamrizki/nextboard/internal/nextcode/native/tools"
)

// The environment the hub gives the agent.
const (
	keyEnv   = "NB_PROVIDER_KEY"   // the provider's API key: read once, then unset
	stateEnv = "NEXTCODE_STATE"    // the folder of the conversations
	homeEnv  = "NEXTCODE_HOME"     // the folder of the usage ledger ("" = none)
	guardEnv = "NB_NEXTCODE_GUARD" // the project's blocked commands and sandbox, as JSON
)

// Permission modes, named as Claude Code names them.
const (
	modeDefault = "default"
	modeAccept  = "acceptEdits"
	modePlan    = "plan"
	modeAuto    = "auto"
	modeBypass  = "bypassPermissions"
)

var modes = []string{modeDefault, modeAccept, modePlan, modeAuto, modeBypass}

var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// config is the agent's launch: its command line, its environment, and what it works with.
type config struct {
	provider      llm.ProviderConfig // without the key
	model         string
	effort        string
	sessionID     string
	resume        string
	cont          bool
	mode          string
	appendPrompt  string // the hub's context for this console, from --append-system-prompt-file
	cwd           string
	addDirs       []string
	maxTokens     int
	contextWindow int // 0 = by the model's name
	stateDir      string
	homeDir       string // "" = no ledger
	blocked       []string
	sandbox       *tools.Sandbox
	environ       []string // the environment of the commands the tools run
	secret        string   // the provider's key, kept out of every line the agent writes

	client llm.Client
	tools  []tools.Tool
	// editPath and readOnly are tools.EditPath and tools.ReadOnlyCommand (the tests give their own).
	editPath func(*tools.Env, json.RawMessage) string
	readOnly func(string) bool
}

// factory makes the agent's client and tools; nil fields are the real ones.
type factory struct {
	client func(llm.ProviderConfig, *http.Client) llm.Client
	tools  func() []tools.Tool
}

// Main runs the agent: `nextboard nextcode-agent [flags]`, in the console's working folder. It
// returns the exit code: 0 once stdin closes, 2 on a bad command line.
func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A closed stdout fails a write instead of killing the agent before it saved. Notify, not
	// Ignore: an ignored signal would stay ignored in the commands the tools run.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	return start(ctx, args, os.Stdin, os.Stdout, os.Stderr, factory{})
}

// start reads the key and the command line, makes the client and the tools, and runs the agent.
// The key leaves the environment first, so no command the agent runs can inherit it.
func start(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, f factory) int {
	key := os.Getenv(keyEnv)
	_ = os.Unsetenv(keyEnv)
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "nextcode-agent: no working folder: %v\n", err)
		return 2
	}
	cfg, err := parse(args, os.Getenv, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "nextcode-agent: %s\n", redact(err.Error(), key))
		return 2
	}
	cfg.secret = key
	cfg.environ = toolEnviron(os.Environ(), key)
	newClient, newTools := f.client, f.tools
	if newClient == nil {
		newClient = llm.NewClient
	}
	if newTools == nil {
		newTools = tools.All
	}
	p := cfg.provider
	p.Key = key
	cfg.client = newClient(p, nil)
	cfg.tools = newTools()
	// the MCP servers Settings › NextCode gives it: their tools join its own
	if spec := os.Getenv(mcpEnv); spec != "" {
		more, closeAll := connectMCP(ctx, spec, cfg.environ, stderr)
		defer closeAll()
		cfg.tools = append(cfg.tools, more...)
	}
	return run(ctx, cfg, stdin, stdout, stderr)
}

// dirList is the repeatable --add-dir.
type dirList []string

func (d *dirList) String() string { return strings.Join(*d, ",") }

func (d *dirList) Set(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("an empty folder")
	}
	*d = append(*d, s)
	return nil
}

// parse reads the command line and the environment; cwd is the working folder.
func parse(args []string, getenv func(string) string, cwd string) (*config, error) {
	c := &config{cwd: cwd}
	var promptFile string
	fs := flag.NewFlagSet("nextcode-agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.provider.Kind, "provider-kind", "", "anthropic|openai")
	fs.StringVar(&c.provider.BaseURL, "base-url", "", "the provider's base URL")
	fs.StringVar(&c.provider.API, "api", "", "chat|responses (openai)")
	fs.StringVar(&c.provider.Auth, "auth", "", "bearer|x-api-key (anthropic)")
	fs.StringVar(&c.model, "model", "", "the model")
	fs.StringVar(&c.effort, "effort", "", "low|medium|high|xhigh|max")
	fs.StringVar(&c.sessionID, "session-id", "", "a new conversation with this id")
	fs.StringVar(&c.resume, "resume", "", "continue this conversation")
	fs.BoolVar(&c.cont, "continue", false, "continue the latest conversation")
	fs.StringVar(&c.mode, "permission-mode", modeDefault, "the permission mode")
	fs.StringVar(&promptFile, "append-system-prompt-file", "", "text to append to the system prompt")
	fs.Var((*dirList)(&c.addDirs), "add-dir", "another folder to work in (repeatable)")
	fs.IntVar(&c.maxTokens, "max-tokens", 0, "the longest answer (0 = the client's default)")
	fs.IntVar(&c.contextWindow, "context-window", 0, "the model's context window (0 = by its name)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := c.check(); err != nil {
		return nil, err
	}
	for i, d := range c.addDirs {
		if !filepath.IsAbs(d) {
			d = filepath.Join(cwd, d)
		}
		c.addDirs[i] = filepath.Clean(d)
	}
	if promptFile != "" {
		b, err := os.ReadFile(promptFile)
		if err != nil {
			return nil, fmt.Errorf("--append-system-prompt-file: %w", err)
		}
		c.appendPrompt = string(b)
	}
	if err := c.environment(getenv); err != nil {
		return nil, err
	}
	return c, nil
}

// check validates the flags' values.
func (c *config) check() error {
	if err := oneOf("--provider-kind", c.provider.Kind, "anthropic", "openai"); err != nil {
		return err
	}
	if c.provider.BaseURL == "" {
		return errors.New("--base-url is required")
	}
	if u, err := url.Parse(c.provider.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("--base-url must be an http or https URL")
	}
	if c.provider.API != "" {
		if err := oneOf("--api", c.provider.API, "chat", "responses"); err != nil {
			return err
		}
	}
	if c.provider.Auth != "" {
		if err := oneOf("--auth", c.provider.Auth, "bearer", "x-api-key"); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.model) == "" {
		return errors.New("--model is required")
	}
	if c.effort != "" {
		if err := oneOf("--effort", c.effort, efforts...); err != nil {
			return err
		}
	}
	if c.mode == "" {
		c.mode = modeDefault
	}
	if err := oneOf("--permission-mode", c.mode, modes...); err != nil {
		return err
	}
	if c.maxTokens < 0 || c.contextWindow < 0 {
		return errors.New("--max-tokens and --context-window must not be negative")
	}
	n := 0
	for _, set := range []bool{c.sessionID != "", c.resume != "", c.cont} {
		if set {
			n++
		}
	}
	if n > 1 {
		return errors.New("use only one of --session-id, --resume and --continue")
	}
	for _, id := range []string{c.sessionID, c.resume} {
		if id != "" && !validID(id) {
			return fmt.Errorf("%q is not a conversation id", id)
		}
	}
	return nil
}

// environment reads the agent's variables.
func (c *config) environment(getenv func(string) string) error {
	c.stateDir = getenv(stateEnv)
	if c.stateDir == "" {
		return errors.New(stateEnv + " is required")
	}
	c.homeDir = getenv(homeEnv)
	raw := strings.TrimSpace(getenv(guardEnv))
	if raw == "" {
		return nil
	}
	var g struct {
		Blocked []string       `json:"blocked"`
		Sandbox *tools.Sandbox `json:"sandbox"`
	}
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return fmt.Errorf("%s is not valid JSON: %v", guardEnv, err)
	}
	for _, p := range g.Blocked {
		if strings.TrimSpace(p) != "" {
			c.blocked = append(c.blocked, p)
		}
	}
	c.sandbox = g.Sandbox
	return nil
}

func oneOf(name, v string, allowed ...string) error {
	if v == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !slices.Contains(allowed, v) {
		return fmt.Errorf("%s must be one of %s, not %s", name, strings.Join(allowed, ", "), strconv.Quote(v))
	}
	return nil
}

// toolEnviron is the environment of the commands the tools run: the agent's own without the
// key and the hub's variables (its token, hooks, MCP and the agent's own settings). A variable
// whose value holds the key goes too, whatever its name.
func toolEnviron(environ []string, key string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, val, _ := strings.Cut(kv, "=")
		switch {
		case name == keyEnv, name == "NB_TOKEN", name == guardEnv, name == mcpEnv,
			strings.HasPrefix(name, "NB_HOOK"), strings.HasPrefix(name, "NB_MCP"), strings.HasPrefix(name, "NEXTCODE_"):
			continue
		case key != "" && strings.Contains(val, key):
			continue
		}
		out = append(out, kv)
	}
	return out
}
