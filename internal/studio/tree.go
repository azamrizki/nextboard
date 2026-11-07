package studio

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Code studio helpers (docs/API-studio.md §7) on top of the Phase 4 file
// API: a one-level tree with prune rules, a name search, mkdir/rename
// without overwrite, trash inside the project, and the "outside project"
// check of a file's real path.

// TreeMax is the entry limit of one tree level.
const TreeMax = 5000

// PruneDirs are hidden folders of the tree (shown with hidden=1).
var PruneDirs = map[string]bool{
	".git": true, "node_modules": true, "build": true, "dist": true, ".next": true, ".nuxt": true, ".svelte-kit": true,
	"venv": true, ".venv": true, "target": true, "vendor": true, "coverage": true, "DerivedData": true, "Pods": true,
	"__pycache__": true, ".cache": true, ".turbo": true, ".gradle": true, ".idea": true,
	".nextboard-trash": true,
}

// KeepDotfiles stay visible although they start with a dot.
var KeepDotfiles = map[string]bool{".gitignore": true, ".env.example": true, ".editorconfig": true, ".github": true, ".claude": true}

// binaryExt marks files the editor does not open.
var binaryExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".avif": true, ".bmp": true, ".ico": true,
	".icns": true, ".tif": true, ".tiff": true, ".psd": true, ".pdf": true, ".zip": true, ".gz": true, ".tgz": true,
	".bz2": true, ".xz": true, ".7z": true, ".rar": true, ".tar": true, ".jar": true, ".war": true, ".class": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true, ".o": true, ".obj": true, ".lib": true,
	".bin": true, ".dat": true, ".db": true, ".sqlite": true, ".sqlite3": true, ".wasm": true, ".pyc": true,
	".mp3": true, ".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".wav": true, ".flac": true, ".ogg": true,
	".webm": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true, ".dmg": true, ".iso": true,
	".apk": true, ".aab": true, ".ipa": true, ".deb": true, ".rpm": true, ".pkg": true,
}

// BinaryName reports whether a file name has a binary extension.
func BinaryName(name string) bool { return binaryExt[strings.ToLower(path.Ext(name))] }

// Hidden reports whether an entry is hidden by default.
func Hidden(name string, dir bool) bool {
	if dir && PruneDirs[name] {
		return true
	}
	return strings.HasPrefix(name, ".") && !KeepDotfiles[name]
}

// TreeEntry is one row of fs/tree.
type TreeEntry struct {
	Name   string    `json:"name"`
	Dir    bool      `json:"dir"`
	Size   int64     `json:"size"`
	Mtime  time.Time `json:"mtime,omitzero"`
	Hidden bool      `json:"hidden"`
	Binary bool      `json:"binary"`
}

// TreeListing is GET /api/machines/{id}/fs/tree.
type TreeListing struct {
	Path      string      `json:"path"`
	Entries   []TreeEntry `json:"entries"`
	Pruned    []string    `json:"pruned"`
	Truncated bool        `json:"truncated"`
}

// Tree lists one folder level: folders first, hidden entries left out
// (named in Pruned) unless showHidden.
func Tree(ctx context.Context, h machine.Host, p string, showHidden bool) (*TreeListing, error) {
	l, err := h.ListDir(ctx, p)
	if err != nil {
		return nil, err
	}
	out := &TreeListing{Path: l.Path, Entries: []TreeEntry{}, Pruned: []string{}}
	for _, e := range l.Entries {
		hid := Hidden(e.Name, e.Dir)
		if hid && !showHidden {
			out.Pruned = append(out.Pruned, e.Name)
			continue
		}
		if len(out.Entries) >= TreeMax {
			out.Truncated = true
			break
		}
		out.Entries = append(out.Entries, TreeEntry{Name: e.Name, Dir: e.Dir, Size: e.Size, Mtime: e.Mtime, Hidden: hid,
			Binary: !e.Dir && BinaryName(e.Name)})
	}
	return out, nil
}

// FindResult is one match of the name search.
type FindResult struct {
	Path string `json:"path"`
	Rel  string `json:"rel"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

// FindLimits bound the name search.
var (
	FindMaxVisit = 20000
	FindBudget   = 10 * time.Second
)

// Find walks root (prune rules, hidden folders skipped) and returns files
// whose relative path contains every word of q (case-insensitive).
// truncated: the walk stopped at the visit cap or the time budget.
func Find(ctx context.Context, h machine.Host, root, q string, limit int) ([]FindResult, bool, error) {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return []FindResult{}, false, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(ctx, FindBudget)
	defer cancel()
	first, err := h.ListDir(ctx, root)
	if err != nil {
		return nil, false, err
	}
	base := first.Path
	out := []FindResult{}
	queue := []*machine.DirListing{first}
	visited := 0
	for len(queue) > 0 {
		l := queue[0]
		queue = queue[1:]
		for _, e := range l.Entries {
			visited++
			if visited > FindMaxVisit || ctx.Err() != nil {
				return out, true, nil
			}
			if Hidden(e.Name, e.Dir) || (e.Dir && strings.HasPrefix(e.Name, ".")) {
				continue
			}
			full := path.Join(l.Path, e.Name)
			rel := strings.TrimPrefix(strings.TrimPrefix(full, base), "/")
			if e.Dir {
				sub, err := h.ListDir(ctx, full)
				if err == nil {
					queue = append(queue, sub)
				}
			}
			lr := strings.ToLower(rel)
			ok := true
			for _, w := range words {
				if !strings.Contains(lr, w) {
					ok = false
					break
				}
			}
			if ok && !e.Dir {
				out = append(out, FindResult{Path: full, Rel: rel, Size: e.Size})
				if len(out) >= limit {
					return out, true, nil
				}
			}
		}
	}
	return out, false, nil
}

var (
	ErrOutsideProject = errors.New("path is outside the project folder")
	ErrTrashRoot      = errors.New("the project folder itself cannot be trashed")
)

// Inside reports whether p is root or below it (clean absolute paths).
func Inside(p, root string) bool {
	p, root = path.Clean(p), path.Clean(root)
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// TrashPath computes where p goes: <root>/.git/nextboard-trash/<ts>/<rel>
// in a git repo, <root>/.nextboard-trash/<ts>/<rel> otherwise.
func TrashPath(root, p string, git bool, now time.Time) (string, error) {
	root, p = path.Clean(root), path.Clean(p)
	if !Inside(p, root) {
		return "", ErrOutsideProject
	}
	if p == root {
		return "", ErrTrashRoot
	}
	rel := strings.TrimPrefix(p, root+"/")
	if strings.HasPrefix(rel, ".git/") || rel == ".git" || strings.HasPrefix(rel, ".nextboard-trash") {
		return "", fmt.Errorf("%s cannot be trashed", rel)
	}
	ts := now.Format("20060102-150405")
	if git {
		return path.Join(root, ".git", "nextboard-trash", ts, rel), nil
	}
	return path.Join(root, ".nextboard-trash", ts, rel), nil
}

// MkdirAll creates dir and its missing parents below root (each one with
// Host.Mkdir; never above root).
func MkdirAll(ctx context.Context, h machine.Host, root, dir string) error {
	if !Inside(dir, root) {
		return ErrOutsideProject
	}
	var missing []string
	for d := path.Clean(dir); Inside(d, root); d = path.Dir(d) {
		st, err := h.Stat(ctx, d)
		if err != nil {
			return err
		}
		if st.Exists {
			if !st.Dir {
				return fmt.Errorf("%s is not a folder", d)
			}
			break
		}
		missing = append(missing, d)
		if d == root {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := h.Mkdir(ctx, missing[i]); err != nil {
			return err
		}
	}
	return nil
}

// MoveToTrash moves p (file or folder) of the project into its trash and
// returns the new path. Nothing is deleted.
func MoveToTrash(ctx context.Context, h machine.Host, root, p string, now time.Time) (string, error) {
	rp, err := machine.RealPath(ctx, h, root)
	if err != nil {
		return "", err
	}
	if !Inside(p, root) && !Inside(p, rp) {
		return "", ErrOutsideProject
	}
	if Inside(p, rp) && !Inside(p, root) {
		root = rp
	}
	st, _, err := machine.Lstat(ctx, h, p)
	if err != nil {
		return "", err
	}
	if !st.Exists {
		return "", fmt.Errorf("%s: %w", p, errNotExist)
	}
	gs, err := h.Stat(ctx, path.Join(root, ".git"))
	if err != nil {
		return "", err
	}
	dst, err := TrashPath(root, p, gs.Exists && gs.Dir, now)
	if err != nil {
		return "", err
	}
	if err := MkdirAll(ctx, h, root, path.Dir(dst)); err != nil {
		return "", err
	}
	if _, err := machine.Rename(ctx, h, p, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// OutsideProject reports whether the real path of p (symlinks resolved)
// is outside the real path of root.
func OutsideProject(ctx context.Context, h machine.Host, root, p string) (bool, error) {
	rr, err := machine.RealPath(ctx, h, root)
	if err != nil {
		return false, err
	}
	rp, err := machine.RealPath(ctx, h, p)
	if err != nil {
		return false, err
	}
	return !Inside(rp, rr), nil
}
