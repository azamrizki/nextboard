package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
)

// ledgerPurpose is what the hub's provider ledger calls the agent's calls.
const ledgerPurpose = "nextcode"

// ledgerLine is one line of <NEXTCODE_HOME>/usage.jsonl: one model call. Its JSON is the hub's
// ledgerEntry (internal/server/providers.go, which embeds usage.Tokens): it is how the hub
// counts what an API provider spent and holds it to its monthly budget.
type ledgerLine struct {
	At         string `json:"at"` // RFC 3339, UTC
	Model      string `json:"model"`
	Purpose    string `json:"purpose"`
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	CacheRead  int    `json:"cacheRead"`
	CacheWrite int    `json:"cacheWrite"`
}

// ledger appends the usage of every model call to the hub's file.
type ledger struct {
	mu   sync.Mutex
	path string
}

func newLedger(home string) *ledger { return &ledger{path: filepath.Join(home, "usage.jsonl")} }

// add appends one call: model is the model asked.
func (l *ledger) add(model string, u llm.Usage) error {
	b, err := json.Marshal(ledgerLine{At: time.Now().UTC().Format(time.RFC3339), Model: model, Purpose: ledgerPurpose,
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite})
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
