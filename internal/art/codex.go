package art

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Codex is the codex CLI's built-in image generation on This PC: `codex exec` in an empty temp
// folder, outside any git repository, in its read-only sandbox, with the login profile the hub
// uses for Codex (Env), and no tools besides image generation that reach the network.
type Codex struct {
	Host machine.Host // This PC
	// Command is the binary: the settings path override, or "codex" on the login PATH.
	Command string
	// Env is the default login profile (CODEX_HOME=…); without CODEX_HOME the CLI uses ~/.codex.
	Env []string
	// Home is $HOME of the hub, where ~/.codex is.
	Home string
	// Detected: the hub's agent catalog finds codex on This PC and it is not turned off.
	Detected bool
	// Off says why it is not detected ("" = not installed).
	Off string
	// Timeout bounds one image (default 3 minutes).
	Timeout time.Duration
	// TempDir is where the empty working folder is made ("" = the system's).
	TempDir string
}

const (
	defaultTimeout = 3 * time.Minute
	checkTimeout   = 20 * time.Second
	replyMax       = 64 << 10
)

// Features that reach the network or the desktop, turned off for the run when the CLI lists
// them as on. Image generation is the one tool the run needs.
var offFeatures = []string{"apps", "browser_use", "browser_use_external", "computer_use", "in_app_browser",
	"plugins", "remote_plugin", "standalone_web_search", "web_search_request", "web_search_cached", "memories"}

func (c Codex) Name() string { return "codex" }

func (c Codex) command() string {
	if c.Command != "" {
		return c.Command
	}
	return "codex"
}

// features runs `codex features list` and returns name → enabled.
func (c Codex) features(ctx context.Context) (map[string]bool, error) {
	res, err := machine.Exec(ctx, c.Host, machine.ExecSpec{Argv: []string{c.command(), "features", "list"}, Env: c.Env,
		Timeout: checkTimeout, MaxOutput: 256 << 10})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fail("cli_failed", "codex features list failed (exit code %d): %s", res.ExitCode, tail(string(res.Stderr), 200))
	}
	out := map[string]bool{}
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			out[f[0]] = f[len(f)-1] == "true"
		}
	}
	return out, nil
}

// Check: the CLI is there and its image_generation feature is on.
func (c Codex) Check(ctx context.Context) Capability {
	cap := Capability{Tool: c.Name()}
	if !c.Detected {
		cap.Reason = "Codex is not installed on This PC"
		if c.Off != "" {
			cap.Reason = c.Off
		}
		return cap
	}
	f, err := c.features(ctx)
	if err != nil {
		cap.Reason = "Codex did not say what it can do: " + err.Error()
		return cap
	}
	on, listed := f["image_generation"]
	switch {
	case !listed:
		cap.Reason = "this Codex version has no image generation"
	case !on:
		cap.Reason = "Codex's image_generation feature is off"
	default:
		cap.Available = true
	}
	return cap
}

// home is the CLI's state folder, as the CLI sees it: CODEX_HOME of the profile, else the one
// the hub runs with (the CLI inherits it), else ~/.codex.
func (c Codex) home() string {
	v := os.Getenv("CODEX_HOME")
	for _, kv := range c.Env {
		if x, ok := strings.CutPrefix(kv, "CODEX_HOME="); ok {
			v = x
		}
	}
	if filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	return filepath.Join(c.Home, ".codex")
}

// optionalFlags are the flags of `codex exec` this version knows, of those the run would like.
func (c Codex) optionalFlags(ctx context.Context) map[string]bool {
	res, err := machine.Exec(ctx, c.Host, machine.ExecSpec{Argv: []string{c.command(), "exec", "--help"}, Env: c.Env,
		Timeout: checkTimeout, MaxOutput: 256 << 10})
	out := map[string]bool{}
	if err != nil || res.ExitCode != 0 {
		return out
	}
	help := string(res.Stdout)
	for _, f := range []string{"--ignore-user-config", "--ignore-rules", "--color"} {
		out[f] = strings.Contains(help, f)
	}
	return out
}

var (
	sessionRe = regexp.MustCompile(`(?i)session id:\s*([0-9a-f]{8}-[0-9a-f-]{27,})`)
	pathRe    = regexp.MustCompile("(/[^\\s\"'`<>()\\[\\]{}|]+\\.(?i:png|jpe?g))")
)

// Generate runs codex once and returns the bytes of the image it made.
func (c Codex) Generate(ctx context.Context, kind, prompt string, progress func(phase, text string)) ([]byte, string, error) {
	feats, err := c.features(ctx)
	if err != nil {
		return nil, "", err
	}
	if !feats["image_generation"] {
		return nil, "", fail("no_image_tool", "Codex's image_generation feature is off")
	}
	flags := c.optionalFlags(ctx)
	dir, err := os.MkdirTemp(c.TempDir, "nb-art-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		return nil, "", err
	}
	reply := filepath.Join(dir, "reply.txt")
	argv := []string{c.command(), "exec", "--skip-git-repo-check", "--sandbox", "read-only", "-C", work, "-o", reply,
		"-c", `web_search="disabled"`, "-c", `model_reasoning_effort="low"`}
	for _, f := range []string{"--ignore-user-config", "--ignore-rules"} {
		if flags[f] {
			argv = append(argv, f)
		}
	}
	if flags["--color"] {
		argv = append(argv, "--color", "never")
	}
	for _, f := range offFeatures {
		if feats[f] {
			argv = append(argv, "--disable", f)
		}
	}
	argv = append(argv, "-")
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	images := filepath.Join(c.home(), "generated_images")
	before := imageFiles(images)
	start := time.Now()
	res, err := machine.Exec(ctx, c.Host, machine.ExecSpec{Argv: argv, Cwd: work, Env: c.Env, Stdin: []byte(prompt),
		Timeout: timeout, IdleTimeout: timeout, MaxOutput: replyMax})
	switch {
	case ctx.Err() != nil:
		return nil, "", ctx.Err()
	case err != nil:
		return nil, "", fail("cli_failed", "codex could not be started: %v", err)
	case res.Cancelled:
		return nil, "", context.Canceled
	case res.TimedOut || res.IdleKilled:
		return nil, "", fail("timeout", "Codex made no image within %s", timeout)
	}
	text := readBounded(reply, replyMax) + "\n" + string(res.Stdout) + "\n" + string(res.Stderr)
	if res.ExitCode != 0 {
		if notLoggedIn(text) {
			return nil, "", fail("cli_not_logged_in", "Codex is not logged in on This PC")
		}
		return nil, "", fail("cli_failed", "codex failed (exit code %d): %s", res.ExitCode, tail(string(res.Stderr), 300))
	}
	session := ""
	if m := sessionRe.FindStringSubmatch(text); m != nil {
		session = strings.ToLower(m[1])
	}
	file, ok := findImage(images, session, text, start, before)
	if !ok {
		return nil, "", fail("no_image", "Codex finished without making an image")
	}
	b := readBounded(file, MaxImageBytes+1)
	if len(b) > MaxImageBytes {
		return nil, "", fail("bad_image", "the image file is too large")
	}
	return []byte(b), readBounded(reply, replyMax), nil
}

// imageFiles lists the images in the folder and in its sub-folders (one per session), by their
// real path, so that one made by this run is told from those that were there.
func imageFiles(images string) map[string]bool {
	out := map[string]bool{}
	root, err := filepath.EvalSymlinks(images)
	if err != nil {
		return out
	}
	add := func(dir string) []os.DirEntry {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.Type().IsRegular() {
				out[filepath.Join(dir, e.Name())] = true
			}
		}
		return ents
	}
	for _, e := range add(root) {
		if e.IsDir() {
			add(filepath.Join(root, e.Name()))
		}
	}
	return out
}

// findImage picks the file Codex made: one that was not there before the run (before), in its
// image folder: the path in its reply, else the newest in the folder of its session, else the
// newest anywhere in the folder. Nothing outside the image folder is ever taken, whatever the
// reply says.
func findImage(images, session, reply string, start time.Time, before map[string]bool) (string, bool) {
	root, err := filepath.EvalSymlinks(images)
	if err != nil {
		return "", false
	}
	since := start.Add(-2 * time.Second) // a coarse clock on the file system
	usable := func(p string) bool {
		real, err := filepath.EvalSymlinks(p)
		if err != nil || !strings.HasPrefix(real, root+string(filepath.Separator)) {
			return false
		}
		fi, err := os.Lstat(real)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
			return false
		}
		if before[real] { // there already: only when the run wrote it again
			return !fi.ModTime().Before(start)
		}
		return !fi.ModTime().Before(since)
	}
	for _, m := range pathRe.FindAllString(reply, 20) {
		if p := filepath.Clean(m); usable(p) {
			return p, true
		}
	}
	newest := func(dirs []string) (string, bool) {
		best, at := "", time.Time{}
		for _, d := range dirs {
			ents, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			for _, e := range ents {
				p := filepath.Join(d, e.Name())
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if e.Type().IsRegular() && (ext == ".png" || ext == ".jpg" || ext == ".jpeg") && usable(p) {
					if fi, err := e.Info(); err == nil && fi.ModTime().After(at) {
						best, at = p, fi.ModTime()
					}
				}
			}
		}
		return best, best != ""
	}
	if session != "" && !strings.ContainsAny(session, `/\.`) {
		if p, ok := newest([]string{filepath.Join(root, session)}); ok {
			return p, true
		}
	}
	dirs := []string{root}
	if ents, err := os.ReadDir(root); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				if fi, err := e.Info(); err == nil && !fi.ModTime().Before(since) {
					dirs = append(dirs, filepath.Join(root, e.Name()))
				}
			}
		}
	}
	return newest(dirs)
}

func readBounded(p string, n int64) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, n))
	return string(b)
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return clean(s, n+4)
}

var loginHints = []string{"not logged in", "codex login", "please log in", "login required", "401 unauthorized", "unauthorized", "invalid api key"}

func notLoggedIn(s string) bool {
	s = strings.ToLower(s)
	for _, h := range loginHints {
		if strings.Contains(s, h) {
			return true
		}
	}
	return false
}
