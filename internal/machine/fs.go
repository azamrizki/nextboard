package machine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// File API limits (docs/API-board.md §4).
const (
	DefaultReadMax = 800000
	MaxFileBytes   = 20 << 20 // fs/raw, fs/write and the read max cap
	maxUniqueTries = 1000
	binarySniffLen = 8000
)

var (
	// ErrRelativePath: file API paths must be absolute (or start with ~).
	ErrRelativePath = errors.New("path harus absolut (atau diawali ~)")
	// ErrIsDir: a file operation targeted a folder.
	ErrIsDir = errors.New("path adalah folder")
	// ErrMtimeMismatch: fs/write ifMtime did not match the file on disk.
	ErrMtimeMismatch = errors.New("file sudah berubah sejak dibuka (mtime tidak cocok)")
	// ErrNoSFTP: the remote has no SFTP subsystem; the exec fallback is
	// read-only.
	ErrNoSFTP = errors.New("SFTP tidak tersedia di machine ini; hanya baca yang didukung")
	// ErrFileTooLarge: the file exceeds MaxFileBytes.
	ErrFileTooLarge = fmt.Errorf("file lebih dari %d MB", MaxFileBytes>>20)
)

// FileStat is the API view of one path (fs/stat, fs/write).
type FileStat struct {
	Path   string    `json:"path"`
	Name   string    `json:"name"`
	Dir    bool      `json:"dir"`
	Size   int64     `json:"size"`
	Mtime  time.Time `json:"mtime,omitzero"`
	Exists bool      `json:"exists"`
}

// FileContent is the API view of fs/read.
type FileContent struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	Mtime     time.Time `json:"mtime,omitzero"`
	Binary    bool      `json:"binary"`
	Truncated bool      `json:"truncated"`
	Content   string    `json:"content"`
}

// WriteOptions modify WriteFile.
type WriteOptions struct {
	// IfMtime, when set, must match the current mtime (else ErrMtimeMismatch).
	IfMtime *time.Time
	// Unique picks name-2.ext, name-3.ext, … when the path already exists.
	Unique bool
}

// cleanAbs validates a file API path for a POSIX machine: "~" and "~/x"
// are expanded against home, anything else must be absolute.
func cleanAbs(p, home string) (string, error) {
	if strings.ContainsRune(p, 0) || len(p) > 4096 {
		return "", ErrRelativePath
	}
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = home + "/" + p[2:]
	}
	if !strings.HasPrefix(p, "/") {
		return "", ErrRelativePath
	}
	return path.Clean(p), nil
}

// uniqueName returns the n-th candidate for p: name-n.ext (n >= 2).
// Dotfiles without another dot (".env") keep the whole name as stem.
func uniqueName(p string, n int) string {
	dir, base := path.Split(p)
	ext := path.Ext(base)
	if ext == base {
		ext = ""
	}
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s%s-%d%s", dir, stem, n, ext)
}

// mtimeMatches compares the client's ifMtime with the file's mtime at the
// precision the client sent (seconds, milliseconds or exact), so a value
// that went through a JS Date still matches.
func mtimeMatches(cur, want time.Time) bool {
	if cur.Equal(want) {
		return true
	}
	switch {
	case want.Nanosecond() == 0:
		return cur.Truncate(time.Second).Equal(want)
	case want.Nanosecond()%int(time.Millisecond) == 0:
		return cur.Truncate(time.Millisecond).Equal(want)
	}
	return false
}

// makeContent turns the first bytes of a file into the fs/read view.
func makeContent(st *FileStat, data []byte, max int64) *FileContent {
	fc := &FileContent{Path: st.Path, Size: st.Size, Mtime: st.Mtime}
	if int64(len(data)) > max {
		data = data[:max]
		fc.Truncated = true
	}
	if st.Size > max {
		fc.Truncated = true
	}
	if IsBinary(data, fc.Truncated) {
		fc.Binary = true
		return fc
	}
	if fc.Truncated {
		data = trimPartialRune(data)
	}
	fc.Content = string(data)
	return fc
}

// IsBinary reports whether data looks like a non-UTF-8 / binary file. A
// truncated buffer may end mid-rune, which is not counted against it.
func IsBinary(data []byte, truncated bool) bool {
	if bytes.IndexByte(data[:min(len(data), binarySniffLen)], 0) >= 0 {
		return true
	}
	if truncated {
		data = trimPartialRune(data)
	}
	return !utf8.Valid(data)
}

func trimPartialRune(b []byte) []byte {
	for i := 1; i <= 3 && i <= len(b); i++ {
		c := b[len(b)-i]
		if c < 0x80 {
			return b // ASCII: complete
		}
		if utf8.RuneStart(c) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			return b
		}
	}
	return b
}

// notExist reports whether err means the path does not exist.
func notExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
