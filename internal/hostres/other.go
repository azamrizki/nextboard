//go:build !linux

package hostres

import (
	"os"
	"runtime"
	"time"
)

// otherReader: a system the hub cannot read yet (macOS, Windows). The views say so.
type otherReader struct{}

func newReader(string, *badFiles) reader { return otherReader{} }

func (otherReader) stuckAt() string { return "" }

func (otherReader) read(bool) Snapshot {
	h, _ := os.Hostname()
	return Snapshot{At: time.Now(), OS: runtime.GOOS, Hostname: h, GPUs: []GPU{}, Temps: []Temp{}, Disks: []Disk{}, Level: "ok"}
}
