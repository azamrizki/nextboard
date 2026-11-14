package review

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// A project on a server takes part through a mirror (docs/API-studio.md §7.10): a copy on This
// PC of what would be in a snapshot. The shadow repository then works on that copy as it does on
// a local folder, so nothing is left on the server, which may be a live one.
//
// The copy comes from another computer, and that computer may be taken over. Everything it says
// is checked here: names, sizes, kinds of file. No link is ever created in the mirror, so nothing
// written into it can land outside. The credentials a server keeps never come over, since agents
// on This PC can read the hub's data folder.

const (
	// mirrorMaxBytes bounds the copy of one project.
	mirrorMaxBytes = 1 << 30
	// fetchBatchBytes and fetchBatchFiles bound one tar over SSH.
	// A batch is held whole while it is unpacked: 8 MB keeps that small, for a few more round trips.
	fetchBatchBytes = 8 << 20
	fetchBatchFiles = 1000
	// mirrorRacy: a file touched this recently may change again within the same second.
	mirrorRacy    = 2
	listMarkerEnd = "__NB_LIST_END__"
	exitHomeRoot  = 77
	exitOutside   = 78
)

// Folders that hold no source, for a folder without .gitignore to say so.
var mirrorPrune = []string{".git", "node_modules", "bower_components", "vendor", "build", "dist", ".next", ".nuxt", ".svelte-kit", "venv", ".venv",
	"target", "coverage", "__pycache__", ".cache", ".turbo", ".gradle", ".terraform", ".idea", ".nextboard-trash"}

var (
	errMirrorHome    = &Error{http.StatusConflict, "review_unsupported", "this project is the server's whole home folder, which review does not copy"}
	errMirrorTooBig  = &Error{http.StatusConflict, "review_too_big", "this project is too big to review: over 20000 files or 1 GB"}
	errMirrorTools   = &Error{http.StatusConflict, "review_unsupported", "review needs GNU tar and stat on the server"}
	errMirrorOffline = &Error{http.StatusServiceUnavailable, "machine_offline", "the server is not connected"}
)

// secretName says a file holds credentials by what it is called. It stays on the server.
func secretName(name string) bool {
	l := strings.ToLower(name)
	switch l {
	case ".env.example", ".env.sample", ".env.dist", ".env.template":
		return false
	case ".env", ".npmrc", ".pypirc", ".netrc", ".git-credentials", ".pgpass", ".my.cnf", "auth.json", "credentials.json", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "wp-config.php":
		return true
	}
	if strings.HasPrefix(l, ".env.") {
		return true
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks", ".kdbx"} {
		if strings.HasSuffix(l, ext) {
			return true
		}
	}
	return false
}

// mirrorPathOK takes what a server names, which may be anything.
func mirrorPathOK(rel string) bool {
	if rel == "" || strings.ContainsAny(rel, "\x00\\") || !filepath.IsLocal(rel) || path.Clean(rel) != rel {
		return false
	}
	for _, c := range strings.Split(rel, "/") {
		if c == ".git" {
			return false
		}
	}
	return !secretName(path.Base(rel))
}

// Mirror is the copy of one project folder on a server.
type Mirror struct {
	Host   func() (machine.Host, bool)
	Remote string // the folder on the server
	Dir    string // the copy, on This PC

	mu     sync.Mutex
	nested []string
	index  map[string]mirrorEntry
	loaded bool
}

type mirrorEntry struct {
	Mtime int64  `json:"m"`
	Size  int64  `json:"s"`
	Mode  string `json:"p"`
}

// SetNested says which repositories live inside the folder: each is listed on its own.
func (m *Mirror) SetNested(rels []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nested = append([]string{}, rels...)
}

func (m *Mirror) indexPath() string { return m.Dir + ".json" }

func (m *Mirror) load() {
	if m.loaded {
		return
	}
	m.loaded = true
	m.index = map[string]mirrorEntry{}
	if b, err := os.ReadFile(m.indexPath()); err == nil {
		_ = json.Unmarshal(b, &m.index)
	}
}

func (m *Mirror) save() error {
	b, err := json.Marshal(m.index)
	if err != nil {
		return err
	}
	tmp := m.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.indexPath())
}

func (m *Mirror) exec(ctx context.Context, script string, stdin []byte, limit int64) (*machine.ExecResult, error) {
	h, ok := m.Host()
	if !ok {
		return nil, errMirrorOffline
	}
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Cwd: m.Remote, Script: "exec /bin/sh -c " + machine.Quote(script), Stdin: stdin, MaxOutput: limit})
	if err != nil {
		if errors.Is(err, machine.ErrNotConnected) || errors.Is(err, machine.ErrDisconnected) {
			return nil, errMirrorOffline
		}
		return nil, err
	}
	if res.TimedOut || res.Cancelled {
		return nil, context.DeadlineExceeded
	}
	return res, nil
}

// listScript names the files of the folder, as git sees them where it can. The files are only
// looked at, at the lowest priority: this may be a production server.
func (m *Mirror) listScript() string {
	var b strings.Builder
	b.WriteString(`n=; command -v nice >/dev/null 2>&1 && n='nice -n 19'; ` + machine.GitNoRepoCode + `export GIT_OPTIONAL_LOCKS=0; ` +
		`top=$(pwd -P) || exit 5; top=${top%/}; case "${HOME%/}/" in "$top"/*) exit ` + strconv.Itoa(exitHomeRoot) + `;; esac; date +%s; `)
	prune := make([]string, 0, len(mirrorPrune))
	for _, d := range mirrorPrune {
		prune = append(prune, "-name "+machine.Quote(d))
	}
	roots := append([]string{""}, m.nested...)
	for _, rel := range roots {
		dir := "."
		if rel != "" {
			dir = rel
		}
		find := `find . \( ` + strings.Join(prune, " -o ")
		for _, o := range m.nested {
			if rest, ok := strings.CutPrefix(o, joinRel(rel)); ok && rest != "" {
				find += " -o -path " + machine.Quote("./"+rest)
			}
		}
		find += ` \) -prune -o -type f -print0`
		b.WriteString(`printf 'R %s\n' ` + machine.Quote(rel) + `; ` +
			`( cd -- ` + machine.Quote(dir) + ` || exit 5; ` +
			`if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then git ls-files -z -c -o --exclude-standard; else ` + find + `; fi | ` +
			`$n xargs -0 -r stat -c '%Y %s %a %F|%n' 2>/dev/null ); `)
	}
	b.WriteString(`echo ` + listMarkerEnd)
	return b.String()
}

func joinRel(rel string) string {
	if rel == "" {
		return ""
	}
	return rel + "/"
}

// parseMirrorList reads a listing. Without the end marker it is no listing: reading a cut one as
// the truth would look like every other file having been deleted.
func parseMirrorList(out string) (now int64, files map[string]mirrorEntry, err error) {
	if !strings.Contains(out, "\n"+listMarkerEnd) {
		return 0, nil, errors.New("the file list from the server was cut off")
	}
	files = map[string]mirrorEntry{}
	root := ""
	for i, line := range strings.Split(out, "\n") {
		if i == 0 {
			now, _ = strconv.ParseInt(strings.TrimSpace(line), 10, 64)
			continue
		}
		if line == listMarkerEnd {
			break
		}
		if r, ok := strings.CutPrefix(line, "R "); ok {
			root = r
			continue
		}
		left, name, ok := strings.Cut(line, "|")
		if !ok {
			continue // the tail of a name with a newline in it
		}
		f := strings.Fields(left)
		if len(f) < 4 || f[3] != "regular" {
			continue // links stay out, always
		}
		rel := path.Join(root, strings.TrimPrefix(name, "./"))
		if !mirrorPathOK(rel) {
			continue
		}
		mt, err1 := strconv.ParseInt(f[0], 10, 64)
		size, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 != nil || err2 != nil || size < 0 {
			continue
		}
		files[rel] = mirrorEntry{Mtime: mt, Size: size, Mode: strings.Clone(f[2])} // not a piece of the listing: it would keep all of it
	}
	return now, files, nil
}

// excludedFolder says the path is in a folder a snapshot would leave out anyway.
func excludedFolder(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, c := range parts[:len(parts)-1] {
		for _, x := range defaultExcludes {
			if c == strings.TrimSuffix(x, "/") {
				return true
			}
		}
	}
	return false
}

// Pull brings the copy up to date. It succeeds only as a whole: a snapshot of half a project
// would give the missing half to whichever agent is at work.
func (m *Mirror) Pull(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	res, err := m.exec(ctx, m.listScript(), nil, 64<<20)
	if err != nil {
		return err
	}
	switch res.ExitCode {
	case exitHomeRoot:
		return errMirrorHome
	case 126:
		return &Error{http.StatusNotFound, "not_found", "the project folder cannot be opened on the server"}
	}
	if res.Truncated {
		return errMirrorTooBig
	}
	now, files, err := parseMirrorList(string(res.Stdout))
	if err != nil {
		return err
	}
	if len(files) > MaxPaths {
		return errMirrorTooBig
	}
	var todo []string
	var total int64
	for rel, e := range files {
		if excludedFolder(rel) {
			delete(files, rel)
			continue
		}
		if e.Size > MaxFile {
			// as on This PC: a file that grew too big keeps its last state in the snapshots
			if old, ok := m.index[rel]; ok {
				files[rel] = old
			} else {
				delete(files, rel)
			}
			continue
		}
		total += e.Size
		if old, ok := m.index[rel]; !ok || old != e || e.Mtime >= now-mirrorRacy {
			todo = append(todo, rel)
		}
	}
	if total > mirrorMaxBytes {
		return errMirrorTooBig
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	gone := false
	for rel := range m.index {
		if _, ok := files[rel]; !ok {
			if err := m.removeLocal(rel); err != nil {
				return err
			}
			delete(m.index, rel)
			gone = true
		}
	}
	sort.Strings(todo)
	for start := 0; start < len(todo); {
		end := start
		var size int64
		for end < len(todo) && end-start < fetchBatchFiles && (end == start || size+files[todo[end]].Size <= fetchBatchBytes) {
			size += files[todo[end]].Size
			end++
		}
		if err := m.fetch(ctx, todo[start:end], files); err != nil {
			return err
		}
		if err := m.save(); err != nil {
			return err
		}
		start = end
	}
	if gone && len(todo) == 0 {
		return m.save()
	}
	return nil
}

func (m *Mirror) fetch(ctx context.Context, rels []string, files map[string]mirrorEntry) error {
	var names bytes.Buffer
	want := make(map[string]bool, len(rels))
	var size int64
	for _, rel := range rels {
		// ./ so that a name starting with a dash stays a name
		names.WriteString("./" + rel + "\x00")
		want[rel] = true
		size += files[rel].Size
	}
	res, err := m.exec(ctx, `n=; command -v nice >/dev/null 2>&1 && n='nice -n 19'; exec $n tar -cf - --null -T -`, names.Bytes(), size+int64(len(rels))*4096+1<<20)
	if err != nil {
		return err
	}
	// exit 1 is a file that changed while it was read, and a vanished one is also no failure:
	// the next listing settles both. Only an archive with nothing readable is one.
	got, err := m.extract(bytes.NewReader(res.Stdout), want, files)
	if err != nil {
		return err
	}
	if got == 0 && res.ExitCode > 1 {
		if strings.Contains(string(res.Stderr), "unrecognized option") || strings.Contains(string(res.Stderr), "unknown option") || strings.Contains(string(res.Stderr), "invalid option") {
			return errMirrorTools
		}
		return fmt.Errorf("copying from the server: %s", firstOf(string(res.Stderr)))
	}
	return nil
}

func firstOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// extract takes only regular files that were asked for, under their asked-for size limit.
func (m *Mirror) extract(r io.Reader, want map[string]bool, files map[string]mirrorEntry) (int, error) {
	tr := tar.NewReader(r)
	got := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return got, nil
		}
		if err != nil {
			return got, nil // cut off: what came is good, the rest comes with the next listing
		}
		rel := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag != tar.TypeReg || !want[rel] || !mirrorPathOK(rel) || hdr.Size > MaxFile {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, MaxFile+1))
		if err != nil || int64(len(b)) > MaxFile || int64(len(b)) != hdr.Size {
			continue
		}
		e := files[rel]
		if err := m.writeLocal(rel, b, e.Mode); err != nil {
			return got, err
		}
		m.index[rel] = e
		got++
	}
}

// writeLocal writes into the copy. Every folder on the way is checked: a link there would carry
// the write out of the mirror, wherever such a link came from.
func (m *Mirror) writeLocal(rel string, b []byte, mode string) error {
	if !mirrorPathOK(rel) {
		return errBadPath
	}
	dir := m.Dir
	parts := strings.Split(rel, "/")
	for _, c := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, c)
		st, err := os.Lstat(dir)
		switch {
		case err == nil && st.IsDir():
		case err == nil:
			if err := os.Remove(dir); err != nil {
				return err
			}
			fallthrough
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(dir, 0o700); err != nil {
				return err
			}
		default:
			return err
		}
	}
	target := filepath.Join(m.Dir, filepath.FromSlash(rel))
	if st, err := os.Lstat(target); err == nil && st.IsDir() {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".nb-mirror-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	perm := os.FileMode(0o600)
	if p, err := strconv.ParseUint(mode, 8, 32); err == nil && p&0o100 != 0 {
		perm = 0o700
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

func (m *Mirror) removeLocal(rel string) error {
	if !filepath.IsLocal(rel) {
		return errBadPath
	}
	target := filepath.Join(m.Dir, filepath.FromSlash(rel))
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for d := filepath.Dir(target); d != m.Dir && strings.HasPrefix(d, m.Dir); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return nil
}

// containScript moves into the parent of rel, and stops if a link on the way leaves the project.
// An agent can make links on the server, and undoing its change must not write through one.
func containScript(rel string) string {
	return `top=$(pwd -P) || exit 5; d=$(dirname -- ` + machine.Quote("./"+rel) + `); e=$d; ` +
		`while [ ! -d "$e" ]; do e=$(dirname -- "$e"); done; real=$(cd -- "$e" && pwd -P) || exit 5; ` +
		`case "$real" in "$top"|"$top"/*) ;; *) exit ` + strconv.Itoa(exitOutside) + `;; esac; `
}

// WriteFile puts content on the server, as an undo does. The link at the target is replaced, not
// followed. The copy follows at the next pull.
func (m *Mirror) WriteFile(ctx context.Context, rel string, b []byte, mode string) error {
	if !filepath.IsLocal(rel) {
		return errBadPath
	}
	t := machine.Quote("./" + rel)
	script := containScript(rel) + `mkdir -p -- "$d" || exit 5; tmp=$(mktemp "$d/.nb-review-XXXXXX") || exit 5; trap 'rm -f "$tmp"' EXIT; cat > "$tmp" || exit 5; ` +
		`if [ -f ` + t + ` ] && p=$(stat -c %a -- ` + t + ` 2>/dev/null); then chmod "$p" "$tmp" || exit 5; else chmod 644 "$tmp" || exit 5; fi; `
	switch mode {
	case "100755":
		script += `chmod a+x "$tmp" || exit 5; `
	case "100644":
		script += `chmod a-x "$tmp" || exit 5; `
	}
	script += `mv -f -- "$tmp" ` + t
	if b == nil {
		b = []byte{}
	}
	return m.runWrite(ctx, script, b)
}

// RemoveFile deletes a file on the server.
func (m *Mirror) RemoveFile(ctx context.Context, rel string) error {
	if !filepath.IsLocal(rel) {
		return errBadPath
	}
	return m.runWrite(ctx, containScript(rel)+`rm -f -- `+machine.Quote("./"+rel), nil)
}

func (m *Mirror) runWrite(ctx context.Context, script string, stdin []byte) error {
	res, err := m.exec(ctx, script, stdin, 1<<16)
	if err != nil {
		return err
	}
	switch res.ExitCode {
	case 0:
		return nil
	case exitOutside:
		return errBadPath
	}
	return fmt.Errorf("writing on the server: %s", firstOf(string(res.Stderr)))
}

// remoteTickEvery spaces the looks at a server while an agent works. Its file tools ask for one
// right after each change, so this only catches what a shell command did on its own.
const (
	remoteTickEvery = 10 * time.Second
	remoteSoonAfter = 1500 * time.Millisecond
)
