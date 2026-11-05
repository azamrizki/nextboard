package machine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
)

// File operations of the Phase 7 studios (docs/API-studio.md §7, §9):
// rename without overwriting, removal of one file or empty folder, and the
// real path of a file. Like WriteFile they run only on an explicit user
// action; on SSH they need the SFTP subsystem (ErrNoSFTP otherwise; there
// is no exec fallback for writes). They are package functions (like Exec)
// so the Host interface stays unchanged.

// ErrExists: the target of a rename (or mkdir) already exists.
var ErrExists = errors.New("path tujuan sudah ada")

// ErrCopyIntoItself: the target of a copy lies inside the folder being copied.
var ErrCopyIntoItself = errors.New("folder tidak bisa disalin ke dalam dirinya sendiri")

// copyTaken is the exit code of copyScript when the target already exists.
const copyTaken = 73

// copyScript copies from to to unless to exists. A failed cp leaves nothing:
// half a folder looks like a whole one.
func copyScript(from, to string) string {
	return `s=` + Quote(from) + `; d=` + Quote(to) + `; ` +
		`if [ -e "$d" ] || [ -L "$d" ]; then exit ` + fmt.Sprint(copyTaken) + `; fi; ` +
		`cp -a -- "$s" "$d" || { r=$?; rm -rf -- "$d"; exit $r; }`
}

// Copy copies a file or folder on h with its links, modes and times, by the
// machine's own cp so that nothing travels through the hub. An existing target
// is never replaced: unique takes name-2.ext, name-3.ext, …, otherwise ErrExists.
func Copy(ctx context.Context, h Host, from, to string, unique bool) (*FileStat, error) {
	if !path.IsAbs(to) {
		return nil, ErrRelativePath
	}
	st, err := h.Stat(ctx, from)
	if err != nil {
		return nil, err
	}
	if !st.Exists {
		return nil, fs.ErrNotExist
	}
	src, err := RealPath(ctx, h, st.Path)
	if err != nil {
		return nil, err
	}
	dst, err := RealPath(ctx, h, to)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(dst, src+"/") {
		return nil, ErrCopyIntoItself
	}
	to = path.Clean(to)
	for n := 1; n <= maxUniqueTries; n++ {
		cand := to
		if n > 1 {
			cand = uniqueName(to, n)
		}
		res, err := Exec(ctx, h, ExecSpec{Script: copyScript(st.Path, cand), MaxOutput: 64 << 10})
		if err != nil {
			return nil, err
		}
		switch {
		case res.ExitCode == 0:
			return h.Stat(ctx, cand)
		case res.ExitCode != copyTaken:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("gagal menyalin: %s", strings.TrimSpace(string(res.Stderr)))
		case !unique:
			return nil, ErrExists
		}
	}
	return nil, fmt.Errorf("tidak ada nama bebas untuk %s", path.Base(to))
}

// Rename moves from to to on h; an existing target is never replaced.
func Rename(ctx context.Context, h Host, from, to string) (*FileStat, error) {
	switch m := h.(type) {
	case *Local:
		return m.rename(from, to)
	case *SSH:
		return m.renameSFTP(ctx, from, to)
	}
	return nil, errors.New("machine tidak didukung")
}

// Remove deletes one file, symlink or empty folder on h.
func Remove(ctx context.Context, h Host, p string) error {
	switch m := h.(type) {
	case *Local:
		abs, err := m.resolve(p)
		if err != nil {
			return err
		}
		return os.Remove(abs)
	case *SSH:
		sc, home, done, err := m.sftpClient(ctx)
		if err != nil {
			return err
		}
		defer done()
		abs, err := cleanAbs(p, home)
		if err != nil {
			return err
		}
		fi, err := sc.Lstat(abs)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return sc.RemoveDirectory(abs)
		}
		return sc.Remove(abs)
	}
	return errors.New("machine tidak didukung")
}

// RealPath resolves symlinks of p (of its nearest existing parent when p
// does not exist yet).
func RealPath(ctx context.Context, h Host, p string) (string, error) {
	switch m := h.(type) {
	case *Local:
		abs, err := m.resolve(p)
		if err != nil {
			return "", err
		}
		rest := ""
		for {
			if r, err := filepath.EvalSymlinks(abs); err == nil {
				return path.Join(r, rest), nil
			}
			parent := filepath.Dir(abs)
			if parent == abs {
				return path.Join(abs, rest), nil
			}
			rest = path.Join(filepath.Base(abs), rest)
			abs = parent
		}
	case *SSH:
		sc, home, done, err := m.sftpClient(ctx)
		if err != nil {
			return "", err
		}
		defer done()
		abs, err := cleanAbs(p, home)
		if err != nil {
			return "", err
		}
		rest := ""
		for {
			if _, err := sc.Stat(abs); err == nil {
				r, err := sc.RealPath(abs)
				if err != nil {
					return "", err
				}
				return path.Join(r, rest), nil
			}
			parent := path.Dir(abs)
			if parent == abs {
				return path.Join(abs, rest), nil
			}
			rest = path.Join(path.Base(abs), rest)
			abs = parent
		}
	}
	return "", errors.New("machine tidak didukung")
}

// Lstat reports p without following a final symlink (Exists:false when
// missing). Dir is false for a symlink to a folder.
func Lstat(ctx context.Context, h Host, p string) (*FileStat, bool, error) {
	switch m := h.(type) {
	case *Local:
		abs, err := m.resolve(p)
		if err != nil {
			return nil, false, err
		}
		fi, err := os.Lstat(abs)
		if notExist(err) {
			return &FileStat{Path: abs, Name: path.Base(abs)}, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return statOf(abs, fi), fi.Mode()&fs.ModeSymlink != 0, nil
	case *SSH:
		sc, home, done, err := m.sftpClient(ctx)
		if err != nil {
			return nil, false, err
		}
		defer done()
		abs, err := cleanAbs(p, home)
		if err != nil {
			return nil, false, err
		}
		fi, err := sc.Lstat(abs)
		if notExist(err) {
			return &FileStat{Path: abs, Name: path.Base(abs)}, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return statOf(abs, fi), fi.Mode()&os.ModeSymlink != 0, nil
	}
	return nil, false, errors.New("machine tidak didukung")
}

func (l *Local) rename(from, to string) (*FileStat, error) {
	src, err := l.resolve(from)
	if err != nil {
		return nil, err
	}
	dst, err := l.resolve(to)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(dst); err == nil {
		return nil, ErrExists
	} else if !notExist(err) {
		return nil, err
	}
	if fi.Mode().IsRegular() {
		// link + unlink never replaces an existing target (like OpenSSH's
		// sftp-server); fall back to rename where hard links fail.
		if err := os.Link(src, dst); err == nil {
			if err := os.Remove(src); err != nil {
				_ = os.Remove(dst)
				return nil, err
			}
			return l.statAfter(dst)
		} else if errors.Is(err, fs.ErrExist) {
			return nil, ErrExists
		}
	}
	if err := os.Rename(src, dst); err != nil {
		return nil, err
	}
	return l.statAfter(dst)
}

func (l *Local) statAfter(p string) (*FileStat, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	return statOf(p, fi), nil
}

func (m *SSH) renameSFTP(ctx context.Context, from, to string) (*FileStat, error) {
	sc, home, done, err := m.sftpClient(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	src, err := cleanAbs(from, home)
	if err != nil {
		return nil, err
	}
	dst, err := cleanAbs(to, home)
	if err != nil {
		return nil, err
	}
	if _, err := sc.Lstat(src); err != nil {
		return nil, err
	}
	if _, err := sc.Lstat(dst); err == nil {
		return nil, ErrExists
	}
	// SSH_FXP_RENAME (not posix-rename) refuses an existing target.
	if err := sc.Rename(src, dst); err != nil {
		var se *sftp.StatusError
		if errors.As(err, &se) {
			if _, serr := sc.Lstat(dst); serr == nil {
				return nil, ErrExists
			}
		}
		return nil, fmt.Errorf("rename: %w", err)
	}
	fi, err := sc.Stat(dst)
	if err != nil {
		return nil, err
	}
	return statOf(dst, fi), nil
}
