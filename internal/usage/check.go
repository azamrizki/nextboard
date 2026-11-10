package usage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/screen"
)

// A check (docs/API-studio.md §10.1) starts the CLI in a hidden PTY (not in
// sessions.json, not on any board) of 120×40 in the usage folder, with the
// profile's environment and without NextBoard hooks or yolo flags. A
// hub-side VT model answers the CLI's terminal queries and is read to find
// the prompt, answer the folder-trust question for the hub's own empty
// folder, skip an update prompt, then type the usage command and parse the
// screen. No model prompt is ever sent.

// Limits (§15).
var (
	readyTimeout  = 30 * time.Second
	screenTimeout = 15 * time.Second
	totalTimeout  = 60 * time.Second
	stableFor     = 1500 * time.Millisecond
	promptStable  = 600 * time.Millisecond
	exitWait      = 3 * time.Second
	pollEvery     = 100 * time.Millisecond
	keyGap        = 25 * time.Millisecond
	// The command is typed again while the CLI waits for its limits.
	pendingRetries = 4
	pendingGap     = 2 * time.Second
)

const (
	cols = 120
	rows = 40
	// missingMarker is printed when the CLI is not on the login PATH.
	missingMarker = "__NB_USAGE_CLI_MISSING__"
)

// cliSpec is how one CLI is checked.
type cliSpec struct {
	command string // usage command typed at the prompt
	exit    string // typed to leave
	escape  bool   // Esc closes the usage panel before exit
	rules   string // screen rules / input prompt id
	env     []string
	args    func(dir string) []string
}

var clis = map[string]cliSpec{
	"claude": {command: "/usage", exit: "/exit", escape: true, rules: "claude", env: []string{"DISABLE_AUTOUPDATER=1"},
		args: func(string) []string { return nil }},
	// Codex 0.165: the update check is switched off and the usage folder
	// is trusted per launch (-c). Note: Codex's one-time screen-reader
	// probe (tui/src/screen_reader.rs) writes "[tui]
	// screen_reader_detection_done = true" (plus "animations = false" when
	// the D-Bus a11y bus reports IsEnabled) into config.toml on its first
	// interactive start whenever the marker is missing from the file;
	// -c overrides do not stop it (verified), so it is reported, not
	// prevented (docs/API-studio.md §10.4).
	"codex": {command: "/status", exit: "/quit", rules: "codex",
		args: func(dir string) []string {
			return []string{"-c", "check_for_update_on_startup=false", "-c", `projects."` + dir + `".trust_level="trusted"`}
		}},
	// Gemini CLI: unverified (not installed on the reference machine).
	"gemini": {command: "/stats", exit: "/quit", rules: "gemini", args: func(string) []string { return nil }},
}

// Supported reports whether agent has a usage check.
func Supported(agentID string) bool { _, ok := clis[agentID]; return ok }

// CheckSpec is one check request.
type CheckSpec struct {
	MachineID  string
	Agent      string
	ProfileID  string
	ProfileEnv string // e.g. CLAUDE_CONFIG_DIR ("" = none)
	ProfileDir string
	Command    string // binary path override ("" = agent id)
	Trigger    string
	// OnScreen (diagnostics, real-CLI capture) sees every distinct screen.
	OnScreen func(step string, sn screen.Snapshot)
}

// outcome is what the driver learned.
type outcome struct {
	state string // ok | needsInteractive | notLoggedIn | unsupported | error
	err   string
	snap  screen.Snapshot
	raw   []string
}

// Login screens (lower-case fragments).
var loginRe = map[string]*regexp.Regexp{
	"claude": regexp.MustCompile(`(?i)select login method|log ?in with your|please run /login|not logged in|invalid api key`),
	"codex":  regexp.MustCompile(`(?i)sign in with chatgpt|provide your own api key|not logged in|run codex login`),
	"gemini": regexp.MustCompile(`(?i)login with google|waiting for auth|gemini_api_key`),
}

var (
	trustYesRe    = regexp.MustCompile(`(?i)Yes, I trust this folder`)
	trustCursorRe = regexp.MustCompile(`(?i)^\s*[❯›>]\s*(\d+[.)]\s*)?Yes, I trust`)
	updateRe      = regexp.MustCompile(`(?i)update available|update now|skip until next version`)
	loadingRe     = regexp.MustCompile(`(?i)loading (usage|stats|status)|fetching|\bloading…`)
)

// hidden is the PTY of one check with its screen model.
type hidden struct {
	proc machine.Proc
	scr  *screen.Screen
	mu   sync.Mutex
	out  strings.Builder // first bytes, to spot the missing-CLI marker
	done chan struct{}
	code int
}

func startHidden(h machine.Host, sp CheckSpec, cs cliSpec, dir string) (*hidden, error) {
	cmd := sp.Command
	if cmd == "" {
		cmd = sp.Agent
	}
	if strings.ContainsAny(cmd, "\x00\n") {
		return nil, errors.New("invalid command")
	}
	env := append([]string{}, cs.env...)
	if sp.ProfileEnv != "" && sp.ProfileDir != "" {
		env = append(env, sp.ProfileEnv+"="+sp.ProfileDir)
	}
	line := "command -v " + machine.Quote(cmd) + " >/dev/null 2>&1 || { echo " + missingMarker + "; exit 127; }; exec " +
		machine.Join(append([]string{cmd}, cs.args(dir)...)...)
	hp := &hidden{done: make(chan struct{})}
	var proc machine.Proc
	hp.scr = screen.New(cols, rows, func(b []byte) {
		hp.mu.Lock()
		p := proc
		hp.mu.Unlock()
		if p != nil {
			_, _ = p.Write(b)
		}
	})
	p, err := h.Start(machine.StartSpec{SessionID: "usage", Shell: line, Cwd: dir, Env: env, Cols: cols, Rows: rows})
	if err != nil {
		hp.scr.Close()
		var nf *machine.CommandNotFoundError
		if errors.As(err, &nf) {
			return nil, errMissing
		}
		return nil, err
	}
	hp.mu.Lock()
	hp.proc = p
	proc = p
	hp.mu.Unlock()
	go func() {
		buf := make([]byte, 16<<10)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				hp.mu.Lock()
				if hp.out.Len() < 4096 {
					hp.out.Write(buf[:min(n, 4096)])
				}
				hp.mu.Unlock()
				hp.scr.Feed(buf[:n])
			}
			if err != nil {
				break
			}
		}
		// The screen applies what it is fed on a goroutine of its own: a CLI that quits at once
		// would otherwise be seen as gone before its last words, the reason it quit, are on screen.
		hp.scr.Sync()
		code, _ := p.Wait()
		hp.mu.Lock()
		hp.code = code
		hp.mu.Unlock()
		close(hp.done)
	}()
	return hp, nil
}

var errMissing = errors.New("cli missing")

func (hp *hidden) exited() bool {
	select {
	case <-hp.done:
		return true
	default:
		return false
	}
}

func (hp *hidden) missing() bool {
	hp.mu.Lock()
	defer hp.mu.Unlock()
	return strings.Contains(hp.out.String(), missingMarker)
}

// typeKeys writes s one rune at a time (no paste detection by the TUI).
func (hp *hidden) typeKeys(ctx context.Context, s string) error {
	for _, r := range s {
		if _, err := hp.proc.Write([]byte(string(r))); err != nil {
			return err
		}
		if !sleepCtx(ctx, keyGap) {
			return ctx.Err()
		}
	}
	return nil
}

func (hp *hidden) key(k string) error { _, err := hp.proc.Write([]byte(k)); return err }

// stop leaves the CLI (Esc when needed, exit command), kills it after exitWait and
// releases the screen.
func (hp *hidden) stop(cs cliSpec) {
	if !hp.exited() {
		if cs.escape {
			// A lone Esc; the pause keeps the next key from being read as
			// Alt+key.
			_ = hp.key("\x1b")
			time.Sleep(600 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = hp.typeKeys(ctx, cs.exit)
		cancel()
		time.Sleep(150 * time.Millisecond)
		_ = hp.key("\r")
		select {
		case <-hp.done:
		case <-time.After(exitWait):
			_ = hp.proc.Signal("KILL")
		}
	}
	select {
	case <-hp.done:
	case <-time.After(exitWait):
	}
	_ = hp.proc.Close()
	hp.scr.Close()
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// norm replaces NO-BREAK SPACE (TUI padding) for matching.
func norm(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.ReplaceAll(l, " ", " ")
	}
	return out
}

// drive runs the check on an already prepared folder.
func drive(ctx context.Context, h machine.Host, sp CheckSpec, dir string, progress func(phase, text string)) outcome {
	cs := clis[sp.Agent]
	ctx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()
	hp, err := startHidden(h, sp, cs, dir)
	if errors.Is(err, errMissing) {
		return outcome{state: "error", err: fmt.Sprintf("CLI %s not found on this machine", sp.Agent)}
	}
	if err != nil {
		return outcome{state: "error", err: err.Error()}
	}
	defer hp.stop(cs)
	var lastV uint64
	seen := func(step string) screen.Snapshot {
		sn := hp.scr.Snapshot()
		if sn.Version != lastV && sp.OnScreen != nil {
			sp.OnScreen(step, sn)
		}
		lastV = sn.Version
		return sn
	}
	// ---- 1. wait for the input prompt, answering startup dialogs ----
	progress("launching", "Starting "+sp.Agent+"…")
	readyBy := time.Now().Add(readyTimeout)
	var changedAt time.Time
	var prevV uint64
	trustKeys, updateKeys := 0, 0
	for {
		if !sleepCtx(ctx, pollEvery) {
			return outcome{state: "error", err: "the usage check was cancelled or timed out", snap: hp.scr.Snapshot()}
		}
		sn := seen("start")
		if sn.Version != prevV {
			prevV, changedAt = sn.Version, time.Now()
		}
		if hp.exited() {
			if hp.missing() {
				return outcome{state: "error", err: fmt.Sprintf("CLI %s not found on this machine", sp.Agent)}
			}
			sn = hp.scr.Snapshot()
			return outcome{state: "error", err: "the CLI exited before its prompt was ready" + exitReason(sn.Lines), snap: sn}
		}
		lines := norm(sn.Lines)
		text := strings.Join(lines, "\n")
		if re := loginRe[sp.Agent]; re != nil && re.MatchString(text) {
			return outcome{state: "notLoggedIn", snap: sn}
		}
		if time.Since(changedAt) < 250*time.Millisecond {
			continue // still drawing
		}
		m := agent.RulesFor(cs.rules).Evaluate(lines, sn.CursorY)
		switch {
		case m.NeedsYou == "Trust folder?" && trustYesRe.MatchString(text):
			// The hub's own empty folder: select "Yes, I trust this
			// folder" (cursor checked on screen), then Enter.
			if trustKeys >= 6 {
				return outcome{state: "needsInteractive", err: "the trust folder dialog could not be answered", snap: sn}
			}
			trustKeys++
			if cursorOn(lines, trustCursorRe) {
				progress("launching", "Trusting the usage folder…")
				_ = hp.key("\r")
			} else {
				_ = hp.key("\x1b[B")
			}
			changedAt = time.Now()
			continue
		case m.NeedsYou == "Update dialog" || (m.NeedsYou != "" && updateRe.MatchString(text)):
			// Never install: Esc skips the update prompt.
			if updateKeys >= 2 {
				return outcome{state: "needsInteractive", err: "the update dialog could not be skipped", snap: sn}
			}
			updateKeys++
			_ = hp.key("\x1b")
			changedAt = time.Now()
			continue
		case m.NeedsYou == "Login dialog":
			return outcome{state: "notLoggedIn", snap: sn}
		case m.NeedsYou != "":
			return outcome{state: "needsInteractive", err: "the CLI is waiting for an answer: " + m.NeedsYou, snap: sn}
		}
		if ok, _ := agent.InputPrompt(cs.rules, lines, sn.CursorY); ok && time.Since(changedAt) >= promptStable {
			break
		}
		if time.Now().After(readyBy) {
			return outcome{state: "needsInteractive", err: "the CLI prompt was not ready within 30 seconds", snap: sn}
		}
	}
	// ---- 2. type the usage command and wait for a stable screen ----
	progress("thinking", "Reading "+cs.command+"…")
	var sn screen.Snapshot
	for attempt := 0; ; attempt++ {
		if err := hp.typeKeys(ctx, cs.command); err != nil {
			return outcome{state: "error", err: err.Error()}
		}
		if !sleepCtx(ctx, 250*time.Millisecond) {
			return outcome{state: "error", err: "cancelled"}
		}
		before := hp.scr.Snapshot().Version
		_ = hp.key("\r")
		by := time.Now().Add(screenTimeout)
		changedAt, prevV = time.Now(), before
		for {
			if !sleepCtx(ctx, pollEvery) {
				return outcome{state: "error", err: "the usage check was cancelled or timed out", snap: hp.scr.Snapshot()}
			}
			sn = seen("usage")
			if sn.Version != prevV {
				prevV, changedAt = sn.Version, time.Now()
			}
			loading := loadingRe.MatchString(strings.Join(sn.Lines, "\n"))
			if sn.Version != before && time.Since(changedAt) >= stableFor && !loading {
				break
			}
			if hp.exited() || time.Now().After(by) {
				break
			}
		}
		if attempt >= pendingRetries || hp.exited() || !ParseScreen(sp.Agent, rawLines(sn.Lines), time.Now(), time.Local).Pending {
			break
		}
		progress("thinking", "Waiting for "+sp.Agent+"’s limits…")
		if !sleepCtx(ctx, pendingGap) {
			return outcome{state: "error", err: "the usage check was cancelled or timed out", snap: sn}
		}
	}
	progress("finishing", "Parsing…")
	return outcome{state: "ok", snap: sn, raw: rawLines(sn.Lines)}
}

// exitReason is the end of what a CLI left on screen when it quit, usually why.
func exitReason(lines []string) string {
	var tail []string
	for _, l := range rawLines(lines) {
		if l = strings.TrimSpace(l); l != "" {
			tail = append(tail, l)
		}
	}
	if len(tail) == 0 {
		return ""
	}
	r := []rune(strings.Join(tail[max(0, len(tail)-3):], " "))
	if len(r) > 240 {
		r = append(r[:240], '…')
	}
	return ": " + string(r)
}

// cursorOn reports whether a selected-option line matches re.
func cursorOn(lines []string, re *regexp.Regexp) bool {
	for _, l := range lines {
		if re.MatchString(l) {
			return true
		}
	}
	return false
}

// rawLines trims trailing blank rows and right spaces.
func rawLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimRight(strings.ReplaceAll(l, " ", " "), " "))
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}
