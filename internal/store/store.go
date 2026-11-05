// Package store persists hub state as JSON files under the data directory.
// Every write is atomic (temp file + rename) and files are created 0600.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Store is rooted at the hub data directory (default ~/.nextboard).
type Store struct {
	Dir string
}

// DefaultDir returns $NEXTBOARD_HOME or ~/.nextboard.
func DefaultDir() string {
	if d := os.Getenv("NEXTBOARD_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".nextboard"
	}
	return filepath.Join(home, ".nextboard")
}

// Open creates the data directory tree (0700) if needed.
func Open(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{abs, filepath.Join(abs, "creds"), filepath.Join(abs, "scrollback")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		_ = os.Chmod(d, 0o700)
	}
	return &Store{Dir: abs}, nil
}

// Path joins name onto the data directory.
func (s *Store) Path(name ...string) string {
	return filepath.Join(append([]string{s.Dir}, name...)...)
}

// Load decodes the JSON file name into v. It reports false when the file
// does not exist (v is left untouched).
func (s *Store) Load(name string, v any) (bool, error) {
	b, err := os.ReadFile(s.Path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(b) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	return true, nil
}

// Save atomically writes v as indented JSON to name.
func (s *Store) Save(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.Path(name), append(b, '\n'))
}

// WriteFileAtomic writes data to a temp file next to path and renames it
// into place, so readers never observe a partial file.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
