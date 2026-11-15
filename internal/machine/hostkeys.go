package machine

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/azamrizki/nextboard/internal/store"
)

// hostKeyStore verifies SSH host keys against the service user's
// ~/.ssh/known_hosts plus the hub's own <data>/known_hosts, where keys
// accepted through the UI (TOFU) are recorded. Nothing on the remote side
// and nothing in ~/.ssh is ever modified.
type hostKeyStore struct {
	mu     sync.Mutex
	nbFile string
}

// errHostKeyUnknown / errHostKeyChanged are produced by the callback.
type errHostKeyUnknown struct {
	info HostKeyInfo
	key  ssh.PublicKey
	addr string
}

func (e *errHostKeyUnknown) Error() string {
	return "unknown host key: " + e.info.Fingerprint
}

type errHostKeyChanged struct {
	info HostKeyInfo
}

func (e *errHostKeyChanged) Error() string {
	return fmt.Sprintf("HOST KEY CHANGED for %s (now %s). Possibly a man-in-the-middle attack; connection refused.", e.info.Host, e.info.Fingerprint)
}

func (h *hostKeyStore) files() []string {
	var out []string
	for _, f := range []string{
		filepath.Join(sshDir(), "known_hosts"),
		filepath.Join(sshDir(), "known_hosts2"),
		h.nbFile,
	} {
		if _, err := os.Stat(f); err == nil {
			out = append(out, f)
		}
	}
	return out
}

func (h *hostKeyStore) base() (ssh.HostKeyCallback, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	files := h.files()
	if len(files) == 0 {
		return func(string, net.Addr, ssh.PublicKey) error {
			return &knownhosts.KeyError{}
		}, nil
	}
	return knownhosts.New(files...)
}

// callback returns a HostKeyCallback that maps knownhosts results onto
// unknown (needs confirmation) / changed (refuse) errors.
func (h *hostKeyStore) callback() (ssh.HostKeyCallback, error) {
	cb, err := h.base()
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		if err == nil {
			return nil
		}
		info := HostKeyInfo{Fingerprint: ssh.FingerprintSHA256(key), KeyType: key.Type(), Host: hostname}
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			if len(ke.Want) == 0 {
				return &errHostKeyUnknown{info: info, key: key, addr: hostname}
			}
			return &errHostKeyChanged{info: info}
		}
		var re *knownhosts.RevokedError
		if errors.As(err, &re) {
			return &errHostKeyChanged{info: info}
		}
		return err
	}, nil
}

// knownAlgorithms lists host key algorithms already recorded for addr, so
// the handshake negotiates a key type we can actually verify (otherwise a
// host known only by its RSA key would look like a key change).
func (h *hostKeyStore) knownAlgorithms(addr string) []string {
	cb, err := h.base()
	if err != nil {
		return nil
	}
	dummy, _, _ := ed25519.GenerateKey(nil)
	pk, _ := ssh.NewPublicKey(dummy)
	err = cb(addr, &net.TCPAddr{IP: net.IPv4zero}, pk)
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) {
		return nil
	}
	seen := map[string]bool{}
	var algos []string
	add := func(a string) {
		if !seen[a] {
			seen[a] = true
			algos = append(algos, a)
		}
	}
	for _, k := range ke.Want {
		switch t := k.Key.Type(); t {
		case ssh.KeyAlgoRSA:
			add(ssh.KeyAlgoRSASHA512)
			add(ssh.KeyAlgoRSASHA256)
			add(ssh.KeyAlgoRSA)
		default:
			add(t)
		}
	}
	return algos
}

// forget removes the hub's own records for these addresses (a machine deleted for good).
// Lines naming other hosts too keep them. ~/.ssh is never changed.
func (h *hostKeyStore) forget(addrs []string) error {
	if len(addrs) == 0 {
		return nil
	}
	drop := map[string]bool{}
	for _, a := range addrs {
		drop[knownhosts.Normalize(a)] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	b, err := os.ReadFile(h.nbFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var out []string
	changed := false
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		f := strings.Fields(line)
		at := 0
		if len(f) > 0 && strings.HasPrefix(f[0], "@") {
			at = 1
		}
		if len(f) <= at || strings.HasPrefix(f[0], "#") {
			out = append(out, line)
			continue
		}
		var keep []string
		for _, host := range strings.Split(f[at], ",") {
			if !drop[host] {
				keep = append(keep, host)
			}
		}
		switch {
		case len(keep) == len(strings.Split(f[at], ",")):
			out = append(out, line)
		case len(keep) == 0:
			changed = true
		default:
			f[at] = strings.Join(keep, ",")
			out = append(out, strings.Join(f, " "))
			changed = true
		}
	}
	if !changed {
		return nil
	}
	text := strings.Join(out, "\n")
	if text != "" {
		text += "\n"
	}
	return store.WriteFileAtomic(h.nbFile, []byte(text))
}

// accept records key for addr in the hub's known_hosts.
func (h *hostKeyStore) accept(addr string, key ssh.PublicKey) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, err := os.OpenFile(h.nbFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(addr)}, key))
	return err
}
