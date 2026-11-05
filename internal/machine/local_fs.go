package machine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
)

// statOf converts FileInfo into the API view. Folder sizes are reported
// as 0 (the inode size is meaningless to users).
func statOf(p string, fi fs.FileInfo) *FileStat {
	st := &FileStat{Path: p, Name: path.Base(p), Dir: fi.IsDir(), Mtime: fi.ModTime().UTC(), Exists: true}
	if !st.Dir {
		st.Size = fi.Size()
	}
	return st
}

// checkRegular rejects folders and special files (FIFOs, devices) that
// would block or never end when read.
func checkRegular(fi fs.FileInfo) error {
	if fi.IsDir() {
		return ErrIsDir
	}
	if !fi.Mode().IsRegular() {
		return errors.New("bukan file biasa")
	}
	return nil
}

func (l *Local) resolve(p string) (string, error) { return cleanAbs(p, l.home) }

// Resolve expands ~ and cleans a file API path (absolute paths only).
func (l *Local) Resolve(p string) (string, error) { return l.resolve(p) }

// Stat reports a local path; a missing path is Exists:false, not an error.
func (l *Local) Stat(_ context.Context, p string) (*FileStat, error) {
	abs, err := l.resolve(p)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if notExist(err) {
		return &FileStat{Path: abs, Name: path.Base(abs)}, nil
	}
	if err != nil {
		return nil, err
	}
	return statOf(abs, fi), nil
}

// ReadFile reads at most max bytes of a local file.
func (l *Local) ReadFile(_ context.Context, p string, max int64) (*FileContent, error) {
	f, st, err := l.openRegular(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	return makeContent(st, data, max), nil
}

// OpenRaw opens a local file for streaming.
func (l *Local) OpenRaw(_ context.Context, p string) (io.ReadCloser, *FileStat, error) {
	f, st, err := l.openRegular(p)
	if err != nil {
		return nil, nil, err
	}
	return f, st, nil
}

func (l *Local) openRegular(p string) (*os.File, *FileStat, error) {
	abs, err := l.resolve(p)
	if err != nil {
		return nil, nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, nil, err
	}
	if err := checkRegular(fi); err != nil {
		return nil, nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, nil, err
	}
	return f, statOf(abs, fi), nil
}

// WriteFile writes data in place (explicit user action only). The parent
// folder must exist; nothing else is created.
func (l *Local) WriteFile(_ context.Context, p string, data []byte, opt WriteOptions) (*FileStat, error) {
	abs, err := l.resolve(p)
	if err != nil {
		return nil, err
	}
	if opt.Unique {
		for n := 1; n <= maxUniqueTries; n++ {
			cand := abs
			if n > 1 {
				cand = uniqueName(abs, n)
			}
			f, err := os.OpenFile(cand, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			return l.finishWrite(f, cand, data)
		}
		return nil, fmt.Errorf("tidak ada nama bebas untuk %s", path.Base(abs))
	}
	fi, err := os.Stat(abs)
	if err != nil && !notExist(err) {
		return nil, err
	}
	if err == nil {
		if err := checkRegular(fi); err != nil {
			return nil, err
		}
	}
	if opt.IfMtime != nil && (fi == nil || !mtimeMatches(fi.ModTime(), *opt.IfMtime)) {
		return nil, ErrMtimeMismatch
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return nil, err
	}
	return l.finishWrite(f, abs, data)
}

func (l *Local) finishWrite(f *os.File, p string, data []byte) (*FileStat, error) {
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	return statOf(p, fi), nil
}

// ReadAt reads up to n bytes of a local file starting at off.
func (l *Local) ReadAt(_ context.Context, p string, off, n int64) ([]byte, *FileStat, error) {
	if off < 0 || n < 0 {
		return nil, nil, errors.New("offset tidak valid")
	}
	f, st, err := l.openRegular(p)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return readRange(f, st, off, n)
}

// readRange reads [off, off+n) from r, clamped to the file size.
func readRange(r io.ReaderAt, st *FileStat, off, n int64) ([]byte, *FileStat, error) {
	if off >= st.Size || n == 0 {
		return []byte{}, st, nil
	}
	n = min(n, st.Size-off)
	buf := make([]byte, n)
	k, err := r.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	return buf[:k], st, nil
}

// Mkdir creates one local folder (explicit user action only).
func (l *Local) Mkdir(_ context.Context, p string) error {
	abs, err := l.resolve(p)
	if err != nil {
		return err
	}
	if err := os.Mkdir(abs, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}
