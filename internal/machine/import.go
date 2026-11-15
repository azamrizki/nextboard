package machine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

// CopyFrom adds the servers of another instance of this machine, whose data folder is dir
// (POST /api/instances/import). Each keeps its id, so settings that name a server by id still
// fit, and its name, address and way to sign in. With box (that instance's master key) the stored
// credentials come too, opened with its key and sealed again with this hub's; without it a server
// that signs in with a key or a password asks for it when it is connected. The host keys that
// instance accepted for these servers come along, so nobody is asked to trust them again. The
// copies are not connected: Machines › Connect, or the next start, does that. An id this
// registry has already is skipped. Nothing in dir is written.
func (r *Registry) CopyFrom(dir string, box *secret.Box) ([]string, []string, error) {
	src := &store.Store{Dir: dir}
	var cfgs []Config
	if _, err := src.Load(machinesFile, &cfgs); err != nil {
		return nil, nil, err
	}
	known, _ := os.ReadFile(filepath.Join(dir, "known_hosts"))
	var added, notes []string
	var addrs []string
	for _, c := range cfgs {
		if c.ID == LocalID || c.Kind == "local" || c.ID == "" {
			continue
		}
		r.mu.Lock()
		_, exists := r.ssh[c.ID]
		r.mu.Unlock()
		if exists {
			notes = append(notes, fmt.Sprintf("SSH machine %q: this instance has it already; not copied", c.Name))
			continue
		}
		ref := c.CredRef
		c.CredRef = ""
		if box != nil && ref != "" {
			creds, err := otherCreds(src, box, c.ID)
			if err != nil {
				notes = append(notes, fmt.Sprintf("SSH machine %q: its credentials cannot be opened; copied without them", c.Name))
			} else if err := r.saveCreds(c.ID, creds); err != nil {
				return added, notes, err
			} else {
				c.CredRef = c.ID
			}
		} else if ref != "" {
			notes = append(notes, fmt.Sprintf("SSH machine %q: copied without its stored credentials; it asks for them when connected", c.Name))
		}
		r.mu.Lock()
		r.addSSHLocked(c)
		r.saveLocked()
		r.mu.Unlock()
		r.emit(c.ID)
		added = append(added, c.ID)
		addrs = append(addrs, hostAddrs(c)...)
	}
	if err := r.hk.adopt(known, addrs); err != nil {
		notes = append(notes, "the accepted host keys could not be copied; each server asks to trust its key again")
	}
	return added, notes, nil
}

func otherCreds(src *store.Store, box *secret.Box, id string) (*Creds, error) {
	blob, err := os.ReadFile(src.Path("creds", id+".enc"))
	if err != nil {
		return nil, err
	}
	b, err := box.Open(blob)
	if err != nil {
		return nil, err
	}
	var c Creds
	return &c, json.Unmarshal(b, &c)
}

// adopt adds the lines of another hub's known_hosts that name one of addrs and that this file
// does not hold yet.
func (h *hostKeyStore) adopt(other []byte, addrs []string) error {
	if len(other) == 0 || len(addrs) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, a := range addrs {
		want[knownhosts.Normalize(a)] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, err := os.ReadFile(h.nbFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(cur), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var add []string
	for _, line := range strings.Split(string(other), "\n") {
		line = strings.TrimSpace(line)
		f := strings.Fields(line)
		at := 0
		if len(f) > 0 && strings.HasPrefix(f[0], "@") {
			at = 1
		}
		if len(f) <= at || strings.HasPrefix(f[0], "#") || have[line] {
			continue
		}
		for _, host := range strings.Split(f[at], ",") {
			if want[host] {
				add = append(add, line)
				have[line] = true
				break
			}
		}
	}
	if len(add) == 0 {
		return nil
	}
	text := string(cur)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return store.WriteFileAtomic(h.nbFile, []byte(text+strings.Join(add, "\n")+"\n"))
}
