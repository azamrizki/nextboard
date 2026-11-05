package usage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"nextboard/internal/machine"
	"nextboard/internal/store"
)

// A usage check runs the CLI in a private empty folder (owner decision #2).
// Lead decision (overrides "mktemp" in docs/API-studio.md §10.1): the folder
// is ONE fixed path per machine user, /tmp/nb-usage-<uid>, created fresh
// with mkdir before each check and removed right after, so Claude Code
// records a single folder-trust entry instead of one per run. The path is
// validated (in Go and again in the shell) before every rm, it must be a
// real folder (not a symlink) owned by the user, and every folder in use is
// recorded in usage/pending-cleanup.json so a crashed hub removes it when
// the machine is next connected. Nothing else under /tmp is ever touched.

const pendingFile = "usage/pending-cleanup.json"

// Root is the parent of the usage folder ("/tmp"; tests point it at a
// scratch folder).
var Root = "/tmp"

const (
	dirMarker    = "__NB_USAGE_DIR__="
	unsafeMarker = "__NB_USAGE_UNSAFE__"
	tempTimeout  = 20 * time.Second
)

// ErrUnsafeTemp: the fixed path exists but is a symlink or not the user's
// own folder; the check refuses to use or remove it.
var ErrUnsafeTemp = errors.New("folder usage sementara tidak aman (symlink atau bukan milik user)")

// dirRe validates a usage folder path for root.
func dirRe(root string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(root) + `/nb-usage-[0-9]{1,10}$`)
}

// ValidTempPath reports whether p is a usage folder path that may be removed.
func ValidTempPath(p string) bool { return dirRe(Root).MatchString(p) }

// shellCase is the shell `case` pattern equivalent of dirRe.
func shellCase(root string) string {
	return machine.Quote(root) + `/nb-usage-[0-9]*`
}

// makeScript removes a leftover folder of a crashed check (only a real
// folder owned by the user), then creates the folder fresh (mkdir fails if
// anything appeared in between) and prints it.
func makeScript(root string) string {
	return `u=$(id -u) || exit 125; d=` + machine.Quote(root) + `/nb-usage-$u; ` +
		`case "$d" in ` + shellCase(root) + `) ;; *) exit 125;; esac; ` +
		`case "$u" in *[!0-9]*|'') exit 125;; esac; ` +
		`if [ -L "$d" ]; then echo ` + unsafeMarker + `; exit 124; fi; ` +
		`if [ -e "$d" ]; then if [ -d "$d" ] && [ -O "$d" ]; then rm -rf -- "$d" || exit 125; else echo ` + unsafeMarker + `; exit 124; fi; fi; ` +
		`mkdir -m 700 -- "$d" || exit 125; echo "` + dirMarker + `$d"`
}

// removeScript removes p only when it is still a real folder of the user.
func removeScript(root, p string) string {
	return `d=` + machine.Quote(p) + `; case "$d" in ` + shellCase(root) + `) ;; *) exit 125;; esac; ` +
		`case "${d##*/nb-usage-}" in *[!0-9]*|'') exit 125;; esac; ` +
		`if [ -L "$d" ]; then echo ` + unsafeMarker + `; exit 124; fi; ` +
		`if [ -d "$d" ] && [ -O "$d" ]; then rm -rf -- "$d" || exit 125; fi; exit 0`
}

// pendingEntry is one folder that may still exist on a machine.
type pendingEntry struct {
	MachineID string    `json:"machineId"`
	Path      string    `json:"path"`
	At        time.Time `json:"at"`
}

// tempDirs creates and removes usage folders and keeps pending-cleanup.json.
type tempDirs struct {
	st *store.Store
	mu sync.Mutex
}

func (t *tempDirs) load() []pendingEntry {
	var list []pendingEntry
	if _, err := t.st.Load(pendingFile, &list); err != nil {
		return nil
	}
	return list
}

func (t *tempDirs) save(list []pendingEntry) {
	if list == nil {
		list = []pendingEntry{}
	}
	if err := ensureDir(t.st, "usage"); err == nil {
		_ = t.st.Save(pendingFile, list)
	}
}

func (t *tempDirs) record(machineID, p string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	list := t.load()
	for _, e := range list {
		if e.MachineID == machineID && e.Path == p {
			return
		}
	}
	t.save(append(list, pendingEntry{MachineID: machineID, Path: p, At: time.Now().UTC()}))
}

func (t *tempDirs) forget(machineID, p string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	list := t.load()
	out := list[:0]
	for _, e := range list {
		if e.MachineID == machineID && e.Path == p {
			continue
		}
		out = append(out, e)
	}
	t.save(out)
}

// Pending lists recorded folders (tests, diagnostics).
func (t *tempDirs) Pending() []pendingEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.load()
}

// make creates the folder on h and records it before any CLI runs there.
func (t *tempDirs) make(ctx context.Context, h machine.Host) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, tempTimeout)
	defer cancel()
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Script: makeScript(Root), Timeout: tempTimeout})
	if err != nil {
		return "", err
	}
	out := string(res.Stdout)
	if strings.Contains(out, unsafeMarker) {
		return "", ErrUnsafeTemp
	}
	i := strings.Index(out, dirMarker)
	if res.ExitCode != 0 || i < 0 {
		return "", fmt.Errorf("folder usage sementara gagal dibuat (kode %d): %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	p := strings.TrimSpace(strings.SplitN(out[i+len(dirMarker):], "\n", 2)[0])
	if !ValidTempPath(p) {
		return "", fmt.Errorf("path folder usage tidak terduga: %q", p)
	}
	t.record(h.ID(), p)
	return p, nil
}

// remove deletes p on h (validated first) and forgets it. An invalid path
// is never passed to the machine; it is only dropped from the record.
func (t *tempDirs) remove(ctx context.Context, h machine.Host, p string) error {
	if !ValidTempPath(p) {
		t.forget(h.ID(), p)
		return fmt.Errorf("path folder usage ditolak: %q", p)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tempTimeout)
	defer cancel()
	res, err := machine.Exec(ctx, h, machine.ExecSpec{Script: removeScript(Root, p), Timeout: tempTimeout})
	if err != nil {
		return err // kept in the record: retried at the next connect
	}
	if strings.Contains(string(res.Stdout), unsafeMarker) {
		t.forget(h.ID(), p)
		return ErrUnsafeTemp
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("folder usage gagal dihapus (kode %d)", res.ExitCode)
	}
	t.forget(h.ID(), p)
	return nil
}

// cleanup removes every recorded folder of h (hub start / machine
// connected). Only recorded paths are touched.
func (t *tempDirs) cleanup(ctx context.Context, h machine.Host) {
	for _, e := range t.Pending() {
		if e.MachineID != h.ID() {
			continue
		}
		if err := t.remove(ctx, h, e.Path); err != nil {
			log.Printf("usage: pembersihan %s di %s: %v", e.Path, e.MachineID, err)
		}
	}
}
