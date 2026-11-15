package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/config"
	"github.com/azamrizki/nextboard/internal/store"
)

var hubIDRe = regexp.MustCompile(`^[a-z0-9]{4,32}$`)

// ValidHubID reports whether id can be a hub id: 4 to 32 lowercase letters and digits, the rule
// EnsureIdentity holds config.json to. Names on servers and in /tmp carry it as it is.
func ValidHubID(id string) bool { return hubIDRe.MatchString(id) }

// NewHubID makes a hub id: 10 random lowercase letters and digits. It is not a secret.
func NewHubID() string { return auth.RandomID("") }

// MismatchError: a data folder that belongs to another instance than the one asked for.
type MismatchError struct {
	DataDir string
	Has     string // the instance the folder belongs to
	Want    string // the instance asked for
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("data dir %s belongs to instance %s, not %s", e.DataDir, e.Has, e.Want)
}

// Identity is what config.json says about the instance a data folder holds.
type Identity struct {
	// Name is the instance's name: the one config.json records, else LegacyName.
	Name string
	// Recorded: config.json records Name (a folder a hub of this version has run on).
	Recorded bool
	// HubID is "" until a hub of this version has run on the folder.
	HubID string
	// Exists: the folder holds a config.json.
	Exists bool
}

// readConfig reads config.json as raw fields, so writing two of them back keeps the others
// exactly. A missing file is an empty object.
func readConfig(dataDir string) (map[string]json.RawMessage, bool, error) {
	path := filepath.Join(dataDir, config.File)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	m := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, true, fmt.Errorf("%s: %w", path, err)
		}
		if m == nil { // the file says null
			m = map[string]json.RawMessage{}
		}
	}
	return m, true, nil
}

func stringField(m map[string]json.RawMessage, key, path string) (string, error) {
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s: %s is not a string", path, key)
	}
	return s, nil
}

// ReadIdentity reads which instance a data folder holds, without writing anything.
func ReadIdentity(dataDir string) (Identity, error) {
	m, exists, err := readConfig(dataDir)
	if err != nil {
		return Identity{}, err
	}
	return identityOf(dataDir, m, exists)
}

func identityOf(dataDir string, m map[string]json.RawMessage, exists bool) (Identity, error) {
	path := filepath.Join(dataDir, config.File)
	name, err := stringField(m, "instance", path)
	if err != nil {
		return Identity{}, err
	}
	hubID, err := stringField(m, "hubId", path)
	if err != nil {
		return Identity{}, err
	}
	if name != "" {
		if err := ValidName(name); err != nil {
			return Identity{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	if hubID != "" && !hubIDRe.MatchString(hubID) {
		return Identity{}, fmt.Errorf("%s: hubId %q is not 4 to 32 lowercase letters and digits", path, hubID)
	}
	id := Identity{Name: name, Recorded: name != "", HubID: hubID, Exists: exists}
	if !id.Recorded {
		id.Name = LegacyName(dataDir)
	}
	return id, nil
}

// EnsureIdentity makes config.json record the instance name and a hub id, as the hub does at
// start. want is the instance the caller resolved ("" for none): a folder that records another
// one is a *MismatchError; a folder that records none yet becomes want, or its LegacyName.
// Every other field of config.json stays as it was; a missing config.json gets just these two.
func EnsureIdentity(dataDir, want string) (Identity, error) {
	if want != "" {
		if err := ValidName(want); err != nil {
			return Identity{}, err
		}
	}
	m, exists, err := readConfig(dataDir)
	if err != nil {
		return Identity{}, err
	}
	id, err := identityOf(dataDir, m, exists)
	if err != nil {
		return Identity{}, err
	}
	if id.Recorded && want != "" && want != id.Name {
		return Identity{}, &MismatchError{DataDir: dataDir, Has: id.Name, Want: want}
	}
	changed := false
	if !id.Recorded {
		if want != "" {
			id.Name = want
		}
		m["instance"], _ = json.Marshal(id.Name)
		id.Recorded, changed = true, true
	}
	if id.HubID == "" {
		id.HubID = NewHubID()
		m["hubId"], _ = json.Marshal(id.HubID)
		changed = true
	}
	if !changed {
		return id, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Identity{}, err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Identity{}, err
	}
	if err := store.WriteFileAtomic(filepath.Join(dataDir, config.File), append(b, '\n')); err != nil {
		return Identity{}, err
	}
	id.Exists = true
	return id, nil
}
