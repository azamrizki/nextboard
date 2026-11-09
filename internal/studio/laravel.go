package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/azamrizki/nextboard/internal/machine"
)

// What the editor knows about a Laravel project (docs/API-studio.md §7.14). It is read from the
// project's files and nothing is run: on a live server, booting the application is not something
// to do because someone opened a card. Only a request by a person runs artisan.
//
// .env is never read. Its keys come from .env.example, and its values stay on the machine.

const (
	laravelScanTimeout = 20 * time.Second
	laravelRunTimeout  = 45 * time.Second
	laravelMaxFile     = 512 << 10
	laravelMaxViews    = 5000
	laravelMaxKeys     = 20000
	laravelLogBytes    = 512 << 10
	laravelMaxEntries  = 200
	laravelMaxTrace    = 4 << 10
)

// ErrNoPHP: the machine has no php for artisan.
var ErrNoPHP = errors.New("php was not found on the machine of this project")

// LaravelKey is a dotted key and where it is set.
type LaravelKey struct {
	Key  string `json:"key"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// LaravelRoute is one route, as far as the files say.
type LaravelRoute struct {
	Name   string `json:"name,omitempty"`
	Method string `json:"method"`
	URI    string `json:"uri"`
	Action string `json:"action,omitempty"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
}

// LaravelView is a Blade view by its dotted name.
type LaravelView struct {
	Name string `json:"name"`
	File string `json:"file"`
}

// LaravelIndex is GET /api/projects/{id}/laravel.
type LaravelIndex struct {
	Detected   bool           `json:"detected"`
	Root       string         `json:"root,omitempty"` // below the project folder, "" = the folder itself
	Version    string         `json:"version,omitempty"`
	Views      []LaravelView  `json:"views"`
	Components []LaravelView  `json:"components"`
	Routes     []LaravelRoute `json:"routes"`
	Config     []LaravelKey   `json:"config"`
	Lang       []LaravelKey   `json:"lang"`
	Env        []string       `json:"env"`
}

// laravelFinder prints NUL-framed names and contents. It reads only what is named here, at the
// lowest priority, and stops at a size: the machine may be a production server.
const laravelFinder = `n=; command -v nice >/dev/null 2>&1 && n='nice -n 19'; ` +
	`if [ ! -f artisan ]; then for d in */artisan; do [ -f "$d" ] && { cd -- "${d%/artisan}" && printf '\000@root\000%s' "${d%/artisan}"; break; }; done; fi; ` +
	`[ -f artisan ] && [ -f composer.json ] && grep -q '"laravel/framework"' composer.json 2>/dev/null || exit 0; ` +
	`printf '\000@laravel\0001'; printf '\000composer.json\000'; head -c 65536 composer.json; ` +
	`$n find resources/views -type f -name '*.blade.php' 2>/dev/null | head -n 5000 | while IFS= read -r f; do printf '\000@view\000%s' "$f"; done; ` +
	`$n find app/View/Components -type f -name '*.php' 2>/dev/null | head -n 2000 | while IFS= read -r f; do printf '\000@class\000%s' "$f"; done; ` +
	`for f in routes/*.php config/*.php lang/en/*.php resources/lang/en/*.php .env.example; do [ -f "$f" ] || continue; printf '\000%s\000' "$f"; head -c 524288 "$f" | tr -d '\000'; done`

// ScanLaravel reads the index of the Laravel app in root, or of the one a folder below it.
func ScanLaravel(ctx context.Context, h machine.Host, root string) (*LaravelIndex, error) {
	ctx, cancel := context.WithTimeout(ctx, laravelScanTimeout)
	defer cancel()
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Cwd: root, Script: "exec /bin/sh -c " + machine.Quote(laravelFinder), Timeout: laravelScanTimeout, MaxOutput: 64 << 20})
	if err != nil {
		return nil, err
	}
	if res.TimedOut {
		return nil, errors.New("reading the project took too long")
	}
	return parseLaravel(string(res.Stdout)), nil
}

func parseLaravel(out string) *LaravelIndex {
	idx := &LaravelIndex{Views: []LaravelView{}, Components: []LaravelView{}, Routes: []LaravelRoute{}, Config: []LaravelKey{}, Lang: []LaravelKey{}, Env: []string{}}
	parts := strings.Split(out, "\x00")
	files := map[string]string{}
	var views, classes []string
	for i := 1; i+1 < len(parts); i += 2 {
		name, body := parts[i], parts[i+1]
		switch name {
		case "@root":
			idx.Root = strings.TrimSpace(body)
		case "@laravel":
			idx.Detected = true
		case "@view":
			views = append(views, body)
		case "@class":
			classes = append(classes, body)
		default:
			files[name] = body
		}
	}
	if !idx.Detected {
		return idx
	}
	idx.Version = laravelVersion(files["composer.json"])
	for _, f := range views {
		n, ok := viewName(f)
		if !ok {
			continue
		}
		if c, isComp := strings.CutPrefix(n, "components."); isComp {
			idx.Components = append(idx.Components, LaravelView{Name: c, File: f})
		}
		idx.Views = append(idx.Views, LaravelView{Name: n, File: f})
	}
	for _, f := range classes {
		idx.Components = append(idx.Components, LaravelView{Name: classComponentName(f), File: f})
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, f := range names {
		body := files[f]
		switch {
		case path.Dir(f) == "routes" && path.Base(f) != "console.php" && path.Base(f) != "channels.php":
			idx.Routes = append(idx.Routes, parseRoutes(body, f)...)
		case path.Dir(f) == "config":
			idx.Config = append(idx.Config, phpArrayKeys(body, f, strings.TrimSuffix(path.Base(f), ".php"))...)
		case strings.Contains(f, "lang/en/"):
			idx.Lang = append(idx.Lang, phpArrayKeys(body, f, strings.TrimSuffix(path.Base(f), ".php"))...)
		case f == ".env.example":
			idx.Env = envKeys(body)
		}
	}
	if len(idx.Config) > laravelMaxKeys {
		idx.Config = idx.Config[:laravelMaxKeys]
	}
	if len(idx.Lang) > laravelMaxKeys {
		idx.Lang = idx.Lang[:laravelMaxKeys]
	}
	sort.Slice(idx.Views, func(i, j int) bool { return idx.Views[i].Name < idx.Views[j].Name })
	sort.Slice(idx.Components, func(i, j int) bool { return idx.Components[i].Name < idx.Components[j].Name })
	return idx
}

func laravelVersion(composer string) string {
	var c struct {
		Require map[string]string `json:"require"`
	}
	if json.Unmarshal([]byte(composer), &c) != nil {
		return ""
	}
	return c.Require["laravel/framework"]
}

func viewName(file string) (string, bool) {
	rest, ok := strings.CutPrefix(file, "resources/views/")
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, ".blade.php")
	if !ok {
		return "", false
	}
	return strings.ReplaceAll(rest, "/", "."), true
}

// classComponentName turns app/View/Components/Form/TextInput.php into form.text-input.
func classComponentName(file string) string {
	rest := strings.TrimSuffix(strings.TrimPrefix(file, "app/View/Components/"), ".php")
	segs := strings.Split(rest, "/")
	for i, s := range segs {
		var b strings.Builder
		for j, r := range s {
			if unicode.IsUpper(r) {
				if j > 0 {
					b.WriteByte('-')
				}
				r = unicode.ToLower(r)
			}
			b.WriteRune(r)
		}
		segs[i] = b.String()
	}
	return strings.Join(segs, ".")
}

var envLine = regexp.MustCompile(`(?m)^\s*(?:export\s+)?([A-Z_][A-Z0-9_]*)\s*=`)

func envKeys(body string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, m := range envLine.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// ---- a small reader of PHP: enough to find strings, names and brackets ----

type phpToken struct {
	kind byte // 's' string, 'i' name, 'p' punctuation
	text string
	line int
}

func phpTokens(src string) []phpToken {
	var out []phpToken
	line := 1
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '#' && i+1 < len(src) && src[i+1] == '[':
			// an attribute is code to skip, like a comment
			depth := 0
			for i < len(src) {
				if src[i] == '[' {
					depth++
				} else if src[i] == ']' {
					depth--
					if depth == 0 {
						i++
						break
					}
				} else if src[i] == '\n' {
					line++
				}
				i++
			}
		case c == '#' || (c == '/' && i+1 < len(src) && src[i+1] == '/'):
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i < len(src) && !(src[i] == '*' && i+1 < len(src) && src[i+1] == '/') {
				if src[i] == '\n' {
					line++
				}
				i++
			}
			i += 2
		case c == '\'' || c == '"':
			start, at := i+1, line
			j := start
			var b strings.Builder
			for j < len(src) && src[j] != c {
				if src[j] == '\\' && j+1 < len(src) {
					if src[j+1] == c || src[j+1] == '\\' {
						b.WriteByte(src[j+1])
					} else {
						b.WriteByte('\\')
						b.WriteByte(src[j+1])
					}
					j += 2
					continue
				}
				if src[j] == '\n' {
					line++
				}
				b.WriteByte(src[j])
				j++
			}
			out = append(out, phpToken{'s', b.String(), at})
			i = j + 1
		case c == '_' || c == '\\' || c == '$' || unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c)):
			j := i + 1
			for j < len(src) && (src[j] == '_' || src[j] == '\\' || unicode.IsLetter(rune(src[j])) || unicode.IsDigit(rune(src[j]))) {
				j++
			}
			out = append(out, phpToken{'i', src[i:j], line})
			i = j
		case strings.HasPrefix(src[i:], "=>") || strings.HasPrefix(src[i:], "->") || strings.HasPrefix(src[i:], "::"):
			out = append(out, phpToken{'p', src[i : i+2], line})
			i += 2
		default:
			out = append(out, phpToken{'p', string(c), line})
			i++
		}
	}
	return out
}

// closing finds the bracket that closes the one at i.
func closing(t []phpToken, i int) int {
	depth := 0
	for ; i < len(t); i++ {
		if t[i].kind != 'p' {
			continue
		}
		switch t[i].text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(t) - 1
}

// phpArrayKeys reads the array a config or language file returns, into dotted keys.
func phpArrayKeys(src, file, prefix string) []LaravelKey {
	t := phpTokens(src)
	var out []LaravelKey
	for i := range t {
		if t[i].kind == 'i' && strings.EqualFold(t[i].text, "return") {
			if i+1 < len(t) {
				if open := arrayOpen(t, i+1); open >= 0 {
					walkArray(t, open, closing(t, open), prefix, file, &out)
				}
			}
			break
		}
	}
	return out
}

// arrayOpen says where the bracket of an array starting at i is, or -1.
func arrayOpen(t []phpToken, i int) int {
	if i >= len(t) {
		return -1
	}
	if t[i].kind == 'p' && t[i].text == "[" {
		return i
	}
	if t[i].kind == 'i' && strings.EqualFold(t[i].text, "array") && i+1 < len(t) && t[i+1].text == "(" {
		return i + 1
	}
	return -1
}

func walkArray(t []phpToken, open, end int, prefix, file string, out *[]LaravelKey) {
	for i := open + 1; i < end; i++ {
		if t[i].kind == 's' && i+1 < end && t[i+1].kind == 'p' && t[i+1].text == "=>" {
			key := t[i].text
			full := key
			if prefix != "" {
				full = prefix + "." + key
			}
			*out = append(*out, LaravelKey{Key: full, File: file, Line: t[i].line})
			if inner := arrayOpen(t, i+2); inner >= 0 {
				c := closing(t, inner)
				walkArray(t, inner, c, full, file, out)
				i = c
				continue
			}
		}
		// past a value that is no array: a nested call or list holds no keys of this level
		if t[i].kind == 'p' && (t[i].text == "(" || t[i].text == "[" || t[i].text == "{") {
			i = closing(t, i)
		}
	}
}

// ---- routes ----

var routeVerbs = map[string]string{"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "delete": "DELETE", "options": "OPTIONS", "any": "ANY", "view": "GET", "redirect": "GET", "permanentRedirect": "GET"}

type routeScope struct{ name, uri string }

// parseRoutes reads the routes of a file. Chains are followed, groups give their prefixes to what
// they hold, and resources become what they stand for. Routes built by code stay unknown, which
// is why a person can ask artisan for the whole list.
func parseRoutes(src, file string) []LaravelRoute {
	t := phpTokens(src)
	scope := routeScope{}
	if path.Base(file) == "api.php" {
		scope.uri = "api"
	}
	out := []LaravelRoute{}
	readRouteStatements(t, 0, len(t), scope, file, &out)
	return out
}

func readRouteStatements(t []phpToken, i, end int, scope routeScope, file string, out *[]LaravelRoute) {
	for i < end {
		if t[i].kind == 'i' && (t[i].text == "Route" || t[i].text == `\Route`) && i+1 < end && t[i+1].text == "::" {
			i = readRouteChain(t, i+2, end, scope, file, out)
			continue
		}
		i++
	}
}

// call is one method of a chain.
type routeCall struct {
	name string
	line int
	open int
	end  int
}

func readRouteChain(t []phpToken, i, end int, scope routeScope, file string, out *[]LaravelRoute) int {
	var calls []routeCall
	for i < end && t[i].kind == 'i' && i+1 < end && t[i+1].text == "(" {
		c := closing(t, i+1)
		calls = append(calls, routeCall{t[i].text, t[i].line, i + 1, c})
		i = c + 1
		if i < end && t[i].kind == 'p' && t[i].text == "->" {
			i++
			continue
		}
		break
	}
	inner := scope
	var made []*LaravelRoute
	var only, except map[string]bool
	var resource *routeCall
	resourceKind := ""
	var group *routeCall
	for k := range calls {
		c := &calls[k]
		args := t[c.open+1 : c.end]
		switch {
		case c.name == "name" || c.name == "as":
			if s := firstString(args); s != "" {
				if len(made) > 0 {
					for _, r := range made {
						r.Name = inner.name + s
					}
				} else {
					inner.name += s
				}
			}
		case c.name == "prefix":
			if s := firstString(args); s != "" {
				inner.uri = joinURI(inner.uri, s)
			}
		case c.name == "group":
			group = c
		case c.name == "only":
			only = stringSet(args)
		case c.name == "except":
			except = stringSet(args)
		case c.name == "resource" || c.name == "apiResource":
			resource, resourceKind = c, c.name
		case c.name == "match":
			// match(['get', 'post'], '/uri', action)
			if len(args) > 0 && args[0].text == "[" {
				end := closing(args, 0)
				if methods := allStrings(args[:end]); len(methods) > 0 && end+1 < len(args) {
					rest := args[end+1:]
					if uri := allStrings(rest); len(uri) > 0 {
						made = append(made, &LaravelRoute{Method: strings.ToUpper(strings.Join(methods, "|")), URI: joinURI(inner.uri, uri[0]), File: file, Line: c.line, Action: routeAction(rest[1:])})
					}
				}
			}
		case routeVerbs[c.name] != "" && len(made) == 0 && resource == nil:
			if s := firstString(args); s != "" || len(args) > 0 {
				made = append(made, &LaravelRoute{Method: routeVerbs[c.name], URI: joinURI(inner.uri, s), File: file, Line: c.line, Action: routeAction(args)})
			}
		}
	}
	if resource != nil {
		made = append(made, resourceRoutes(t[resource.open+1:resource.end], resourceKind, inner, only, except, file, resource.line)...)
	}
	for _, r := range made {
		*out = append(*out, *r)
	}
	if group != nil {
		readRouteStatements(t, group.open+1, group.end, inner, file, out)
	}
	return i
}

func firstString(args []phpToken) string {
	for _, a := range args {
		if a.kind == 's' {
			return a.text
		}
		if a.kind == 'p' && a.text == "," {
			return ""
		}
	}
	return ""
}

func allStrings(args []phpToken) []string {
	var out []string
	for _, a := range args {
		if a.kind == 's' {
			out = append(out, a.text)
		}
	}
	return out
}

func stringSet(args []phpToken) map[string]bool {
	out := map[string]bool{}
	for _, s := range allStrings(args) {
		out[s] = true
	}
	return out
}

func joinURI(a, b string) string {
	a, b = strings.Trim(a, "/"), strings.Trim(b, "/")
	switch {
	case a == "" && b == "":
		return "/"
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "/" + b
}

// routeAction says what handles a route: [Controller::class, 'method'], or 'view.name'.
func routeAction(args []phpToken) string {
	var parts []string
	comma := false
	for i, a := range args {
		if !comma {
			if a.kind == 'p' && a.text == "," {
				comma = true
			}
			continue
		}
		if a.kind == 'i' && i+2 < len(args) && args[i+1].text == "::" && args[i+2].text == "class" {
			parts = append(parts, a.text)
		}
		if a.kind == 's' {
			parts = append(parts, a.text)
		}
		if a.kind == 'i' && (a.text == "function" || a.text == "fn") {
			return "Closure"
		}
	}
	return strings.Join(parts, "@")
}

func resourceRoutes(args []phpToken, kind string, scope routeScope, only, except map[string]bool, file string, line int) []*LaravelRoute {
	name := firstString(args)
	if name == "" {
		return nil
	}
	action := routeAction(args)
	dotted := strings.ReplaceAll(strings.Trim(name, "/"), "/", ".")
	base := joinURI(scope.uri, name)
	param := "{" + strings.ReplaceAll(singular(path.Base(strings.Trim(name, "/"))), "-", "_") + "}"
	acts := []struct{ act, method, uri string }{
		{"index", "GET", base},
		{"create", "GET", base + "/create"},
		{"store", "POST", base},
		{"show", "GET", base + "/" + param},
		{"edit", "GET", base + "/" + param + "/edit"},
		{"update", "PUT|PATCH", base + "/" + param},
		{"destroy", "DELETE", base + "/" + param},
	}
	var out []*LaravelRoute
	for _, a := range acts {
		if kind == "apiResource" && (a.act == "create" || a.act == "edit") {
			continue
		}
		if (only != nil && !only[a.act]) || except[a.act] {
			continue
		}
		r := &LaravelRoute{Name: scope.name + dotted + "." + a.act, Method: a.method, URI: a.uri, File: file, Line: line}
		if action != "" {
			r.Action = action + "@" + a.act
		}
		out = append(out, r)
	}
	return out
}

// singular is what Laravel's resource parameters use, for the plurals routes usually have.
func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return strings.TrimSuffix(s, "ies") + "y"
	case strings.HasSuffix(s, "ses"), strings.HasSuffix(s, "xes"), strings.HasSuffix(s, "ches"), strings.HasSuffix(s, "shes"):
		return strings.TrimSuffix(s, "es")
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss"):
		return strings.TrimSuffix(s, "s")
	}
	return s
}

// ---- artisan, only on request ----

// LaravelFullRoute is one row of `artisan route:list --json`.
type LaravelFullRoute struct {
	Method     string `json:"method"`
	URI        string `json:"uri"`
	Name       string `json:"name,omitempty"`
	Action     string `json:"action"`
	Middleware any    `json:"middleware,omitempty"`
}

// ArtisanRoutes asks the application itself. It boots the application, with everything its
// service providers do, so it runs only on a person's request.
func ArtisanRoutes(ctx context.Context, h machine.Host, root string) ([]LaravelFullRoute, error) {
	ctx, cancel := context.WithTimeout(ctx, laravelRunTimeout)
	defer cancel()
	script := lspPath + `command -v php >/dev/null 2>&1 || exit 127; n=; command -v nice >/dev/null 2>&1 && n='nice -n 19'; exec $n php artisan route:list --json --no-ansi --no-interaction`
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Cwd: root, Script: "exec /bin/sh -c " + machine.Quote(script), Timeout: laravelRunTimeout, MaxOutput: 32 << 20})
	if err != nil {
		return nil, err
	}
	if res.TimedOut {
		return nil, errors.New("artisan took too long")
	}
	if res.ExitCode == 127 {
		return nil, ErrNoPHP
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(res.Stdout))
		}
		if len(msg) > 800 {
			msg = strings.ToValidUTF8(msg[:800], "") + "…"
		}
		return nil, fmt.Errorf("artisan failed: %s", msg)
	}
	// A notice printed before the JSON is common on a real app.
	body := string(res.Stdout)
	if i := strings.IndexByte(body, '['); i > 0 {
		body = body[i:]
	}
	var out []LaravelFullRoute
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, errors.New("artisan did not answer with a list of routes")
	}
	return out, nil
}

// ---- the log ----

// LaravelLogEntry is one entry of the application's log.
type LaravelLogEntry struct {
	At      string `json:"at"`
	Env     string `json:"env"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Trace   string `json:"trace,omitempty"`
	File    string `json:"file,omitempty"` // inside the project, where it can be opened
	Line    int    `json:"line,omitempty"`
}

// LaravelLog is GET …/laravel/log, newest entry first.
type LaravelLog struct {
	File    string            `json:"file"`
	Entries []LaravelLogEntry `json:"entries"`
	// Cut says the log is longer than what was read.
	Cut bool `json:"cut"`
}

var (
	logHead = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2}[ T][0-9:.+\-Z]*)\] ([A-Za-z0-9_-]+)\.([A-Z]+): (.*)$`)
	logAt   = regexp.MustCompile(`(?:in | at |^#\d+ )(/[^\s:()]+\.php)[:(](\d+)`)
)

// ReadLaravelLog reads the end of the newest log file. Daily logs are found by their time.
func ReadLaravelLog(ctx context.Context, h machine.Host, root string) (*LaravelLog, error) {
	ctx, cancel := context.WithTimeout(ctx, laravelScanTimeout)
	defer cancel()
	script := fmt.Sprintf(`f=$(ls -t storage/logs/*.log 2>/dev/null | head -n 1); [ -n "$f" ] || exit 0; printf '%%s\n' "$f"; s=$(wc -c < "$f"); printf '%%s\n' "$s"; tail -c %d "$f"`, laravelLogBytes)
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Cwd: root, Script: "exec /bin/sh -c " + machine.Quote(script), Timeout: laravelScanTimeout, MaxOutput: laravelLogBytes + 4096})
	if err != nil {
		return nil, err
	}
	if res.TimedOut {
		return nil, errors.New("reading the log took too long")
	}
	return parseLaravelLog(string(res.Stdout), root), nil
}

func parseLaravelLog(out, root string) *LaravelLog {
	log := &LaravelLog{Entries: []LaravelLogEntry{}}
	first, rest, ok := strings.Cut(out, "\n")
	if !ok {
		return log
	}
	log.File = first
	size, rest, _ := strings.Cut(rest, "\n")
	if n, err := strconv.Atoi(strings.TrimSpace(size)); err == nil && n > laravelLogBytes {
		log.Cut = true
	}
	var cur *LaravelLogEntry
	var all []LaravelLogEntry
	flush := func() {
		if cur != nil {
			cur.Trace = strings.TrimRight(cur.Trace, "\n")
			all = append(all, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(rest, "\n") {
		if m := logHead.FindStringSubmatch(line); m != nil {
			flush()
			cur = &LaravelLogEntry{At: m[1], Env: m[2], Level: m[3], Message: m[4]}
			locate(cur, line, root)
			continue
		}
		if cur == nil {
			continue // the start of an entry cut off by the read
		}
		if len(cur.Trace) < laravelMaxTrace {
			cur.Trace += line + "\n"
		}
		locate(cur, line, root)
	}
	flush()
	for i := len(all) - 1; i >= 0 && len(log.Entries) < laravelMaxEntries; i-- {
		log.Entries = append(log.Entries, all[i])
	}
	return log
}

// locate keeps the first place in the project a log entry names: that is where to look, rather
// than the vendor code it passed through.
func locate(e *LaravelLogEntry, line, root string) {
	if e.File != "" {
		return
	}
	m := logAt.FindStringSubmatch(line)
	if m == nil {
		return
	}
	rel, ok := strings.CutPrefix(m[1], strings.TrimRight(root, "/")+"/")
	if !ok || strings.HasPrefix(rel, "vendor/") {
		return
	}
	e.File = rel
	e.Line, _ = strconv.Atoi(m[2])
}
