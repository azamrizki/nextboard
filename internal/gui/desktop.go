package gui

import (
	"bufio"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// entry is one parsed .desktop file (Desktop Entry Specification).
type entry struct {
	ID, Source                           string
	Type, Name, Comment, Icon            string
	Exec, TryExec, Path                  string
	Terminal, NoDisplay, Hidden, HasOnly bool
	MimeTypes, Categories                []string
}

// validID limits app ids to what is safe in a URL segment and a folder name.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// dataDirs lists the XDG data directories, most specific first.
func dataDirs() []string {
	home, _ := os.UserHomeDir()
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	sys := os.Getenv("XDG_DATA_DIRS")
	if sys == "" {
		sys = "/usr/local/share:/usr/share"
	}
	dirs := append([]string{dataHome}, filepath.SplitList(sys)...)
	dirs = append(dirs, filepath.Join(dataHome, "flatpak", "exports", "share"), "/var/lib/flatpak/exports/share", "/var/lib/snapd/desktop")
	var out []string
	for _, d := range dirs {
		if d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// scanEntries reads every applications folder. The first file of an id wins,
// so a hidden copy in the user's folder masks the system one.
func scanEntries(dirs []string) map[string]*entry {
	out := map[string]*entry{}
	for _, d := range dirs {
		root := filepath.Join(d, "applications")
		_ = filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				if de != nil && de.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if de.IsDir() || !strings.HasSuffix(p, ".desktop") {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			id := strings.ReplaceAll(strings.TrimSuffix(rel, ".desktop"), string(filepath.Separator), "-")
			if _, seen := out[id]; seen || !validID.MatchString(id) {
				return nil
			}
			if e, err := readEntry(p); err == nil {
				e.ID, e.Source = id, p
				out[id] = e
			}
			return nil
		})
	}
	return out
}

func readEntry(path string) (*entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	e := &entry{}
	in := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			// Only the main group: actions like "New Incognito Window" also
			// carry Exec and Name keys.
			in = line == "[Desktop Entry]"
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !in {
			continue
		}
		k, v = strings.TrimSpace(k), unescapeValue(strings.TrimSpace(v))
		switch k {
		case "Type":
			e.Type = v
		case "Name":
			e.Name = v
		case "Comment":
			e.Comment = v
		case "Icon":
			e.Icon = v
		case "Exec":
			e.Exec = v
		case "TryExec":
			e.TryExec = v
		case "Path":
			e.Path = v
		case "Terminal":
			e.Terminal = v == "true"
		case "NoDisplay":
			e.NoDisplay = v == "true"
		case "Hidden":
			e.Hidden = v == "true"
		case "OnlyShowIn":
			e.HasOnly = v != ""
		case "MimeType":
			e.MimeTypes = splitList(v)
		case "Categories":
			e.Categories = splitList(v)
		}
	}
	return e, sc.Err()
}

func splitList(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ';' })
}

// unescapeValue undoes the string-level escapes of a value.
func unescapeValue(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' || i+1 == len(v) {
			b.WriteByte(v[i])
			continue
		}
		i++
		switch v[i] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

// launchable reports whether the entry is a GUI app a person would pick.
// OnlyShowIn entries are desktop plumbing (settings panels of the real
// session), which make no sense on a private display.
func (e *entry) launchable(pathEnv string) bool {
	if e.Type != "Application" || e.Name == "" || e.Exec == "" || e.NoDisplay || e.Hidden || e.Terminal || e.HasOnly {
		return false
	}
	if e.TryExec != "" {
		if _, ok := lookIn(pathEnv, e.TryExec); !ok {
			return false
		}
	}
	argv, err := parseExec(e.Exec)
	if err != nil || len(argv) == 0 {
		return false
	}
	_, ok := lookIn(pathEnv, argv[binaryIndex(argv)])
	return ok
}

// acceptsFolder reports whether a folder can be handed to the app.
func (e *entry) acceptsFolder() bool {
	argv, err := parseExec(e.Exec)
	if err != nil || len(argv) == 0 || !hasFileCode(argv) {
		return false
	}
	return familyOf(argv[binaryIndex(argv)]) == familyVSCode || slices.Contains(e.MimeTypes, "inode/directory")
}

func lookIn(pathEnv, file string) (string, bool) {
	if strings.Contains(file, "/") {
		return file, isExecutable(file)
	}
	for _, d := range filepath.SplitList(pathEnv) {
		if p := filepath.Join(d, file); isExecutable(p) {
			return p, true
		}
	}
	return "", false
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// parseExec splits an Exec value into arguments. It is never handed to a
// shell: quoting rules are the spec's own.
func parseExec(s string) ([]string, error) {
	var (
		args  []string
		cur   strings.Builder
		quote bool
		any   bool
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote && c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case c == '"':
			quote = !quote
			any = true
		case !quote && (c == ' ' || c == '\t'):
			if any {
				args = append(args, cur.String())
				cur.Reset()
				any = false
			}
		default:
			cur.WriteByte(c)
			any = true
		}
	}
	if quote {
		return nil, errBadExec
	}
	if any {
		args = append(args, cur.String())
	}
	return args, nil
}

// hasFileCode reports whether the command line takes files or URLs.
func hasFileCode(argv []string) bool {
	for _, a := range argv {
		if strings.Contains(a, "%f") || strings.Contains(a, "%F") || strings.Contains(a, "%u") || strings.Contains(a, "%U") {
			return true
		}
	}
	return false
}

// expandExec resolves the field codes; target is a local path or empty.
func expandExec(argv []string, e *entry, target string) []string {
	out := make([]string, 0, len(argv)+2)
	targetURL := ""
	if target != "" {
		targetURL = (&url.URL{Scheme: "file", Path: target}).String()
	}
	for _, a := range argv {
		switch a {
		case "%f", "%F":
			if target != "" {
				out = append(out, target)
			}
			continue
		case "%u", "%U":
			if targetURL != "" {
				out = append(out, targetURL)
			}
			continue
		case "%i":
			if e.Icon != "" {
				out = append(out, "--icon", e.Icon)
			}
			continue
		}
		if !strings.Contains(a, "%") {
			out = append(out, a)
			continue
		}
		var b strings.Builder
		for i := 0; i < len(a); i++ {
			if a[i] != '%' || i+1 == len(a) {
				b.WriteByte(a[i])
				continue
			}
			i++
			switch a[i] {
			case '%':
				b.WriteByte('%')
			case 'c':
				b.WriteString(e.Name)
			case 'k':
				b.WriteString(e.Source)
			case 'f':
				b.WriteString(target)
			case 'u':
				b.WriteString(targetURL)
			}
		}
		if b.Len() > 0 {
			out = append(out, b.String())
		}
	}
	return out
}

// binaryIndex finds the real program in `env A=1 prog …` command lines.
func binaryIndex(argv []string) int {
	if len(argv) == 0 || filepath.Base(argv[0]) != "env" {
		return 0
	}
	for i := 1; i < len(argv); i++ {
		if !strings.Contains(argv[i], "=") && !strings.HasPrefix(argv[i], "-") {
			return i
		}
	}
	return 0
}

// App families whose private profile is chosen with flags. Everything else
// gets redirected XDG directories (see profileFor).
const (
	familyChromium = "chromium"
	familyVSCode   = "vscode"
	familyFirefox  = "firefox"
)

func familyOf(bin string) string {
	if strings.HasPrefix(bin, "/snap/") || isFlatpakExport(bin) {
		// A sandboxed package reaches a profile only inside its own folder, which packageOf
		// knows how to find for a browser. Anything else keeps the redirected XDG folders.
		switch f := nameFamily(sandboxedName(bin)); f {
		case familyChromium, familyFirefox:
			return f
		}
		return ""
	}
	return nameFamily(filepath.Base(bin))
}

func nameFamily(name string) string {
	switch name {
	case "google-chrome", "google-chrome-stable", "google-chrome-beta", "google-chrome-unstable",
		"chromium", "chromium-browser", "brave", "brave-browser", "brave-browser-stable",
		"microsoft-edge", "microsoft-edge-stable", "vivaldi", "vivaldi-stable", "opera":
		return familyChromium
	case "code", "code-insiders", "codium", "vscodium", "cursor", "windsurf":
		return familyVSCode
	case "firefox", "firefox-esr":
		return familyFirefox
	}
	return ""
}

// flatpakBrowsers names the browsers by their flatpak id.
var flatpakBrowsers = map[string]string{
	"org.mozilla.firefox":   "firefox",
	"org.chromium.Chromium": "chromium",
	"com.google.Chrome":     "google-chrome",
	"com.brave.Browser":     "brave-browser",
	"com.microsoft.Edge":    "microsoft-edge",
	"com.vivaldi.Vivaldi":   "vivaldi",
	"com.opera.Opera":       "opera",
}

func isFlatpakExport(bin string) bool { return strings.Contains(bin, "/flatpak/exports/bin/") }

// sandboxedName is the program a /snap/bin or flatpak export path stands for.
func sandboxedName(bin string) string {
	base := filepath.Base(bin)
	if isFlatpakExport(bin) {
		return flatpakBrowsers[base]
	}
	name, _, _ := strings.Cut(base, ".") // /snap/bin/<snap>.<app>
	return name
}

// Kinds of package.
const (
	pkgNative  = ""
	pkgSnap    = "snap"
	pkgFlatpak = "flatpak"
)

// appPackage says how a program is installed. A browser in a snap or a flatpak can only write
// to its own folder, and in a snap it can only reach a compositor socket at the fixed place.
type appPackage struct {
	kind   string // pkgNative, pkgSnap or pkgFlatpak
	name   string // the snap's name or the flatpak's id
	family string
	at     int // index in argv of what the app's own flags follow
}

// packageOf looks at the command line, since flatpak's lives past `flatpak run`.
func packageOf(argv []string, bin string) appPackage {
	i := binaryIndex(argv)
	pk := appPackage{family: familyOf(bin), at: i}
	switch {
	case strings.HasPrefix(bin, "/snap/"):
		if pk.family != "" {
			pk.kind, pk.name = pkgSnap, sandboxedName(bin)
		}
	case isFlatpakExport(bin):
		if pk.family != "" {
			pk.kind, pk.name = pkgFlatpak, filepath.Base(bin)
		}
	case filepath.Base(bin) == "flatpak":
		if at := flatpakAppAt(argv, i); at >= 0 {
			if browser, ok := flatpakBrowsers[argv[at]]; ok {
				pk.kind, pk.name, pk.family, pk.at = pkgFlatpak, argv[at], nameFamily(browser), at
			}
		}
	}
	return pk
}

// flatpakAppAt finds the id in `flatpak run [options] <id> [args]`.
func flatpakAppAt(argv []string, i int) int {
	if i+1 >= len(argv) || argv[i+1] != "run" {
		return -1
	}
	for j := i + 2; j < len(argv); j++ {
		if !strings.HasPrefix(argv[j], "-") {
			return j
		}
	}
	return -1
}

// dropFileForwarding removes what `flatpak run` uses to pass files: a page is no file, and the
// markers would reach the browser as arguments.
func dropFileForwarding(argv []string) []string {
	return slices.DeleteFunc(argv, func(a string) bool {
		return a == "--file-forwarding" || a == "@@" || a == "@@u"
	})
}

// sandboxedProfile is the folder a packaged browser may use: its own. Empty for one that can use
// the hub's. The profile of a snap outlives snap refreshes in common, which does not change with
// the revision, and a flatpak keeps its data under ~/.var/app.
func sandboxedProfile(pk appPackage, instance int) (string, error) {
	if pk.kind == pkgNative {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	name := "nextboard"
	if instance > 1 {
		name += "-" + strconv.Itoa(instance)
	}
	if pk.kind == pkgSnap {
		return filepath.Join(home, "snap", pk.name, "common", name), nil
	}
	return filepath.Join(home, ".var", "app", pk.name, name), nil
}

// firefoxPrefs keeps a first start from asking what a private display has nobody to answer.
const firefoxPrefs = `user_pref("browser.shell.checkDefaultBrowser", false);
user_pref("browser.aboutwelcome.enabled", false);
user_pref("datareporting.policy.dataSubmissionPolicyBypassNotification", true);
user_pref("toolkit.telemetry.reportingpolicy.firstRun", false);
user_pref("browser.startup.homepage_override.mstone", "ignore");
`

// chromiumLighter turns off what a browser in a tile has no use for: casting, and the models and
// hints Chrome downloads in the background. About 7 % less memory over six tabs of two sites,
// measured with Chrome for Testing 153; the other candidates saved nothing (process per site) or
// took a feature away (no software rasterizer: no WebGL without a GPU).
const chromiumLighter = "--disable-features=MediaRouter,OptimizationHints,OptimizationGuideModelDownloading,OptimizationHintsFetching,OptimizationTargetPrediction"

// profileFor returns the extra flags and environment that keep the app on
// its NextBoard profile instead of the one used on the physical display.
// The password store is "basic" because a private display has no keyring;
// a real keyring would prompt on the physical screen, where nobody sees it.
func profileFor(bin, dir string) (flags, env []string) { return familyProfile(familyOf(bin), dir) }

func familyProfile(family, dir string) (flags, env []string) {
	switch family {
	case familyChromium:
		return []string{"--user-data-dir=" + filepath.Join(dir, "data"), "--no-first-run", "--no-default-browser-check", "--password-store=basic", chromiumLighter}, nil
	case familyVSCode:
		// The shared folder holds the recent list, which otherwise travels
		// between the profile and the VS Code on the physical display.
		return []string{"--user-data-dir=" + filepath.Join(dir, "data"), "--extensions-dir=" + filepath.Join(dir, "extensions"), "--shared-data-dir=" + filepath.Join(dir, "shared"), "--password-store=basic"},
			[]string{"COPILOT_HOME=" + filepath.Join(dir, "copilot")}
	case familyFirefox:
		return []string{"--new-instance", "--profile", filepath.Join(dir, "data")}, nil
	}
	return nil, []string{
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"XDG_DATA_HOME=" + filepath.Join(dir, "share"),
		"XDG_CACHE_HOME=" + filepath.Join(dir, "cache"),
		"XDG_STATE_HOME=" + filepath.Join(dir, "state"),
	}
}

var iconSizes = []string{"scalable", "512x512", "256x256", "192x192", "128x128", "96x96", "64x64", "48x48", "32x32"}

// resolveIcon finds the file of an Icon value: the hicolor theme first (it
// holds the app's own icon), then pixmaps, then any other installed theme.
func resolveIcon(name string, dirs []string) string {
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		if isIconFile(name) {
			return name
		}
		return ""
	}
	if strings.ContainsAny(name, `/\*?[`) {
		return ""
	}
	exts := []string{".svg", ".png"}
	home, _ := os.UserHomeDir()
	bases := []string{filepath.Join(home, ".icons")}
	for _, d := range dirs {
		bases = append(bases, filepath.Join(d, "icons"))
	}
	first := func(paths ...string) string {
		for _, p := range paths {
			if isIconFile(p) {
				return p
			}
		}
		return ""
	}
	for _, b := range bases {
		for _, size := range iconSizes {
			for _, ext := range exts {
				if p := first(filepath.Join(b, "hicolor", size, "apps", name+ext)); p != "" {
					return p
				}
			}
		}
	}
	for _, d := range dirs {
		for _, ext := range exts {
			if p := first(filepath.Join(d, "pixmaps", name+ext)); p != "" {
				return p
			}
		}
	}
	for _, b := range bases {
		for _, ext := range exts {
			for _, pattern := range []string{filepath.Join(b, "*", "*", "apps", name+ext), filepath.Join(b, "*", "apps", "*", name+ext)} {
				if m, _ := filepath.Glob(pattern); len(m) > 0 {
					slices.Sort(m)
					return m[len(m)-1]
				}
			}
		}
	}
	return ""
}

func isIconFile(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".svg", ".png":
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir()
	}
	return false
}
