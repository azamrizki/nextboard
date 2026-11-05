// Package vault stores the credential vault (docs/API-vault.md). The hub only ever holds
// ciphertext: the browser derives the key from a master password the hub never sees, and every
// name, username and note is inside an item's blob. So this package checks shapes and sizes and
// could not read a single field if it wanted to.
package vault

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/store"
)

const (
	fileName = "vault.json"
	prevName = fileName + ".prev"
	version  = 1

	// MaxItems counts folders too: the hub cannot tell them apart.
	MaxItems = 5000
	// MaxBlobChars is one item as base64, about 48 KB of ciphertext.
	MaxBlobChars = 64 << 10
	// MaxTotalChars keeps the file, written whole on every change, reasonable.
	MaxTotalChars = 64 << 20
	// MaxBatch is what one import request can carry.
	MaxBatch = 1000

	// An item is at least a nonce and a GCM tag.
	minBlobBytes = 12 + 16
	// A wrapped key is a nonce, 32 bytes of key and a GCM tag.
	wrappedKeyBytes = 12 + 32 + 16

	minSaltBytes = 16
	maxSaltBytes = 64
)

var (
	ErrAbsent     = errors.New("vault belum dibuat")
	ErrExists     = errors.New("vault sudah ada")
	ErrNotFound   = errors.New("item tidak ditemukan")
	ErrConflict   = errors.New("item sudah berubah di tempat lain")
	ErrFull       = errors.New("vault penuh")
	ErrInvalid    = errors.New("data vault tidak valid")
	ErrUnreadable = errors.New("vault.json tidak bisa dibaca")
)

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// KDF names how the browser turns the master password into a key. The floor stops a client bug
// from quietly weakening the vault; the ceiling stops a header no browser could ever open.
type KDF struct {
	Name string `json:"name"`
	M    int    `json:"m"` // memory, KiB
	T    int    `json:"t"` // iterations
	P    int    `json:"p"` // parallelism
}

func (k KDF) validate() error {
	if k.Name != "argon2id" {
		return fmt.Errorf("%w: kdf %q", ErrInvalid, k.Name)
	}
	if k.M < 32<<10 || k.M > 1<<20 || k.T < 2 || k.T > 16 || k.P < 1 || k.P > 16 {
		return fmt.Errorf("%w: parameter kdf di luar batas", ErrInvalid)
	}
	return nil
}

// KeyMaterial opens the vault together with the master password.
type KeyMaterial struct {
	KDF        KDF    `json:"kdf"`
	Salt       string `json:"salt"`
	WrappedKey string `json:"wrappedKey"`
}

func (k KeyMaterial) validate() error {
	if err := k.KDF.validate(); err != nil {
		return err
	}
	salt, err := base64.StdEncoding.DecodeString(k.Salt)
	if err != nil || len(salt) < minSaltBytes || len(salt) > maxSaltBytes {
		return fmt.Errorf("%w: salt", ErrInvalid)
	}
	wrapped, err := base64.StdEncoding.DecodeString(k.WrappedKey)
	if err != nil || len(wrapped) != wrappedKeyBytes {
		return fmt.Errorf("%w: wrappedKey", ErrInvalid)
	}
	return nil
}

// Header is the part of the vault that is not an item.
type Header struct {
	KeyMaterial
	// ID changes when the vault is reset, so that a tab still holding the old key locks.
	ID        string    `json:"id"`
	KeyRev    int64     `json:"keyRev"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Item is one encrypted record: a credential or a folder, which the hub cannot tell apart.
type Item struct {
	ID        string    `json:"id"`
	Blob      string    `json:"blob"`
	Rev       int64     `json:"rev"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// NewItem is an item an import brings in.
type NewItem struct {
	ID   string `json:"id"`
	Blob string `json:"blob"`
}

func validateItem(id, blob string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("%w: id", ErrInvalid)
	}
	if len(blob) > MaxBlobChars {
		return fmt.Errorf("%w: item terlalu besar", ErrInvalid)
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(raw) < minBlobBytes {
		return fmt.Errorf("%w: blob", ErrInvalid)
	}
	return nil
}

// View is what a client gets. There is nothing in it the hub could read.
type View struct {
	Exists bool    `json:"exists"`
	Header *Header `json:"header,omitempty"`
	Items  []Item  `json:"items"`
	Rev    int64   `json:"rev"`
}

type doc struct {
	Version int             `json:"version"`
	Header  *Header         `json:"header,omitempty"`
	Items   map[string]Item `json:"items"`
	Rev     int64           `json:"rev"`
	// Pins are the hub's half of each browser's PIN (pin.go). View has no field for them.
	Pins map[string]Pin `json:"pins,omitempty"`
}

func (d *doc) check() error {
	if d.Version != version {
		return fmt.Errorf("versi %d", d.Version)
	}
	if d.Header == nil {
		if len(d.Items) > 0 {
			return errors.New("item tanpa header")
		}
		return nil
	}
	if err := d.Header.validate(); err != nil {
		return err
	}
	for id, it := range d.Items {
		if it.ID != id {
			return fmt.Errorf("item %q: id tidak cocok", id)
		}
		if err := validateItem(id, it.Blob); err != nil {
			return fmt.Errorf("item %q: %w", id, err)
		}
	}
	for id, p := range d.Pins {
		if err := validatePin(id, p); err != nil {
			return err
		}
	}
	return nil
}

func (d *doc) total() int {
	n := 0
	for _, it := range d.Items {
		n += len(it.Blob)
	}
	return n
}

// Service keeps the vault file. Every change is saved before it counts.
type Service struct {
	st *store.Store
	// Publish tells open tabs that something changed, never what.
	Publish func(any)

	mu     sync.Mutex
	d      doc
	broken error
}

// New loads the vault. A damaged file never stops the hub: the previous copy takes over, and if
// that is damaged too the vault answers with ErrUnreadable and refuses to write over the file.
func New(st *store.Store) *Service {
	s := &Service{st: st, d: doc{Version: version, Items: map[string]Item{}}}
	d, err := loadDoc(st, fileName)
	if err == nil {
		if d != nil {
			s.d = *d
		}
		return s
	}
	log.Printf("vault: %v", err)
	prev, perr := loadDoc(st, prevName)
	if perr != nil || prev == nil {
		s.broken = err
		return s
	}
	if ferr := s.fallBack(); ferr != nil {
		log.Printf("vault: %v", ferr)
		s.broken = err
		return s
	}
	log.Printf("vault: memakai salinan sebelumnya, file rusak disimpan terpisah")
	s.d = *prev
	return s
}

// fallBack sets the damaged file aside and puts the previous copy in its place.
func (s *Service) fallBack() error {
	raw, err := os.ReadFile(s.st.Path(prevName))
	if err != nil {
		return err
	}
	if err := os.Rename(s.st.Path(fileName), s.st.Path(asideName("corrupt"))); err != nil {
		return err
	}
	return store.WriteFileAtomic(s.st.Path(fileName), raw)
}

func asideName(why string) string {
	return fmt.Sprintf("vault.%s-%s.json", why, time.Now().UTC().Format("20060102-150405"))
}

func loadDoc(st *store.Store, name string) (*doc, error) {
	var d doc
	ok, err := st.Load(name, &d)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if d.Items == nil {
		d.Items = map[string]Item{}
	}
	if err := d.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &d, nil
}

// save writes next and only then makes it the current state. The file it replaces stays as the
// previous copy: a vault is the one file here nobody can rebuild.
func (s *Service) save(next doc) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	path := s.st.Path(fileName)
	if cur, err := os.ReadFile(path); err == nil && len(cur) > 0 {
		if err := store.WriteFileAtomic(s.st.Path(prevName), cur); err != nil {
			return err
		}
	}
	if err := store.WriteFileAtomic(path, raw); err != nil {
		return err
	}
	s.d = next
	return nil
}

func (s *Service) published() {
	if s.Publish == nil {
		return
	}
	id := ""
	if s.d.Header != nil {
		id = s.d.Header.ID
	}
	s.Publish(map[string]any{"type": "vaultChanged", "rev": s.d.Rev, "vaultId": id})
}

func (s *Service) unreadable() error {
	if s.broken != nil {
		return fmt.Errorf("%w: pulihkan dari cadangan atau reset vault", ErrUnreadable)
	}
	return nil
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// Get returns the whole vault, sorted so that two reads compare equal.
func (s *Service) Get() (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return View{}, err
	}
	v := View{Exists: s.d.Header != nil, Header: s.d.Header, Items: make([]Item, 0, len(s.d.Items)), Rev: s.d.Rev}
	for _, it := range s.d.Items {
		v.Items = append(v.Items, it)
	}
	sort.Slice(v.Items, func(i, j int) bool { return v.Items[i].ID < v.Items[j].ID })
	return v, nil
}

// Init creates the vault. There is only ever one on a hub.
func (s *Service) Init(k KeyMaterial) (*Header, error) {
	if err := k.validate(); err != nil {
		return nil, err
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return nil, err
	}
	if s.d.Header != nil {
		return nil, ErrExists
	}
	t := now()
	next := s.d
	next.Rev++
	next.Header = &Header{KeyMaterial: k, ID: id, KeyRev: 1, CreatedAt: t, UpdatedAt: t}
	next.Items = map[string]Item{}
	if err := s.save(next); err != nil {
		return nil, err
	}
	s.published()
	return next.Header, nil
}

// SetKey replaces the wrapped key after a change of master password. The items stay as they are:
// their key did not change, only what protects it. Every PIN is turned off: a new password is
// often about an old one being known, and each PIN would still open the same key.
func (s *Service) SetKey(k KeyMaterial, baseKeyRev int64) (*Header, error) {
	if err := k.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return nil, err
	}
	if s.d.Header == nil {
		return nil, ErrAbsent
	}
	if s.d.Header.KeyRev != baseKeyRev {
		return nil, ErrConflict
	}
	h := *s.d.Header
	h.KeyMaterial = k
	h.KeyRev++
	h.UpdatedAt = now()
	next := s.d
	next.Rev++
	next.Header = &h
	next.Pins = nil
	if err := s.save(next); err != nil {
		return nil, err
	}
	s.published()
	return &h, nil
}

// Put writes one item. baseRev is the revision the client changed, or 0 for a new item; anything
// else means someone got there first, and the client decides what happens to its edit.
func (s *Service) Put(id, blob string, baseRev int64) (*Item, error) {
	if err := validateItem(id, blob); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return nil, err
	}
	if s.d.Header == nil {
		return nil, ErrAbsent
	}
	old, exists := s.d.Items[id]
	if (exists && old.Rev != baseRev) || (!exists && baseRev != 0) {
		return nil, ErrConflict
	}
	if !exists && len(s.d.Items) >= MaxItems {
		return nil, ErrFull
	}
	if s.d.total()-len(old.Blob)+len(blob) > MaxTotalChars {
		return nil, ErrFull
	}
	next := s.d
	next.Rev++
	next.Items = maps.Clone(s.d.Items)
	it := Item{ID: id, Blob: blob, Rev: next.Rev, UpdatedAt: now()}
	next.Items[id] = it
	if err := s.save(next); err != nil {
		return nil, err
	}
	s.published()
	return &it, nil
}

// Delete removes one item for good. The trash is inside the blobs, where the hub cannot see it.
func (s *Service) Delete(id string, baseRev int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return err
	}
	if s.d.Header == nil {
		return ErrAbsent
	}
	old, exists := s.d.Items[id]
	if !exists {
		return ErrNotFound
	}
	if old.Rev != baseRev {
		return ErrConflict
	}
	next := s.d
	next.Rev++
	next.Items = maps.Clone(s.d.Items)
	delete(next.Items, id)
	if err := s.save(next); err != nil {
		return err
	}
	s.published()
	return nil
}

// Batch adds new items in one write: all of them, or none.
func (s *Service) Batch(items []NewItem) ([]Item, error) {
	if len(items) > MaxBatch {
		return nil, fmt.Errorf("%w: lebih dari %d item", ErrInvalid, MaxBatch)
	}
	for _, it := range items {
		if err := validateItem(it.ID, it.Blob); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return nil, err
	}
	if s.d.Header == nil {
		return nil, ErrAbsent
	}
	if len(s.d.Items)+len(items) > MaxItems {
		return nil, ErrFull
	}
	total := s.d.total()
	next := s.d
	next.Rev++
	next.Items = maps.Clone(s.d.Items)
	t := now()
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if _, exists := next.Items[it.ID]; exists {
			return nil, ErrConflict
		}
		total += len(it.Blob)
		saved := Item{ID: it.ID, Blob: it.Blob, Rev: next.Rev, UpdatedAt: t}
		next.Items[it.ID] = saved
		out = append(out, saved)
	}
	if total > MaxTotalChars {
		return nil, ErrFull
	}
	if err := s.save(next); err != nil {
		return nil, err
	}
	s.published()
	return out, nil
}

// Reset starts over, for a forgotten password or a damaged file. The old file is moved aside and
// not deleted: if the password comes back to mind, it still opens. It returns that file's name.
func (s *Service) Reset() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := ""
	if _, err := os.Stat(s.st.Path(fileName)); err == nil {
		kept = asideName("reset")
		if err := os.Rename(s.st.Path(fileName), s.st.Path(kept)); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// After a reset the previous copy must not be what comes back from a damaged file.
	if err := os.Remove(s.st.Path(prevName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("vault: %v", err)
	}
	s.broken = nil
	s.d = doc{Version: version, Items: map[string]Item{}, Rev: s.d.Rev + 1}
	s.published()
	return kept, nil
}
