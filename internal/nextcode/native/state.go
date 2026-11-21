package native

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
)

// conversation is one conversation of the agent, as its file keeps it. Messages only grow:
// nothing in them is changed once it is there (a provider binds its thinking to the exact
// conversation before it, and the prompt cache needs the same prefix). A compaction starts a
// new list and keeps the old one under Archived.
type conversation struct {
	ID          string          `json:"id"`
	Created     time.Time       `json:"created"`
	System      string          `json:"system"` // built once, reused byte for byte
	Messages    []llm.Message   `json:"messages"`
	Model       string          `json:"model,omitempty"` // the model last used
	Compactions int             `json:"compactions"`
	Archived    [][]llm.Message `json:"archived,omitempty"`
	// Mode and Date are the permission mode and the date the model was last told about, so
	// that it is told again only when they change.
	Mode string `json:"mode,omitempty"`
	Date string `json:"date,omitempty"`
	// Context is the last call's context in tokens (0 = unknown): past 80% of the context
	// window, the conversation is compacted before the next call.
	Context int `json:"context,omitempty"`
}

func newConversation(id, system string) *conversation {
	return &conversation{ID: id, Created: time.Now().UTC(), System: system, Messages: []llm.Message{}}
}

// store is the folder of the conversations: <dir>/<id>.json.
type store struct{ dir string }

func (s store) path(id string) string { return filepath.Join(s.dir, id+".json") }

// load reads a conversation; fs.ErrNotExist when there is none with that id.
func (s store) load(id string) (*conversation, error) {
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, err
	}
	var c conversation
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("conversation %s is damaged: %w", id, err)
	}
	c.ID = id
	if c.Messages == nil {
		c.Messages = []llm.Message{}
	}
	return &c, nil
}

// save writes a conversation atomically: a temporary file in the same folder, then a rename,
// so that a crash leaves the old file or the new one, never half of one.
func (s store) save(c *conversation) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, "."+c.ID+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, s.path(c.ID))
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// latest is the id of the most recently changed conversation; "" when there is none.
func (s store) latest() (string, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	best, bestAt := "", time.Time{}
	for _, e := range entries {
		name := e.Name()
		id, ok := strings.CutSuffix(name, ".json")
		if !ok || e.IsDir() || !validID(id) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestAt) {
			best, bestAt = id, info.ModTime()
		}
	}
	return best, nil
}

// validID reports whether id can name a conversation file: no path, no hidden file.
func validID(id string) bool {
	if id == "" || len(id) > 128 || id[0] == '.' {
		return false
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			return false
		}
	}
	return true
}

// newUUID is a random UUID (version 4): the id of a conversation the hub did not name.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// randomHex is n random bytes as hex.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
