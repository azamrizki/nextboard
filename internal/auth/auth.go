// Package auth handles the admin password, browser cookie sessions, the
// one-time setup code, device pairing and device challenge-response login.
//
// auth.json may also be written by CLI subcommands (setup-link, pair,
// reset-password) while the service runs, so the in-memory copy is reloaded
// whenever the file changes on disk.
package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"nextboard/internal/store"
)

const (
	authFileName    = "auth.json"
	devicesFileName = "devices.json"

	CookieTTL      = 30 * 24 * time.Hour
	SetupCodeTTL   = 30 * time.Minute
	PairCodeTTL    = 5 * time.Minute
	AccessTokenTTL = time.Hour
	challengeTTL   = 60 * time.Second
	MinPassword    = 8
)

var (
	ErrAlreadySetup  = errors.New("hub sudah di-setup")
	ErrNotSetup      = errors.New("hub belum di-setup")
	ErrBadCode       = errors.New("kode tidak valid, kedaluwarsa, atau sudah dipakai")
	ErrWeakPassword  = errors.New("password minimal 8 karakter")
	ErrBadPassword   = errors.New("password salah")
	ErrUnknownDevice = errors.New("device tidak dikenal")
	ErrBadSignature  = errors.New("signature tidak valid")
	ErrBadInput      = errors.New("input tidak valid")
)

type codeEntry struct {
	Hash    string    `json:"hash"`
	Expires time.Time `json:"expires"`
}

type cookieSession struct {
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	LastUsed time.Time `json:"lastUsed"`
}

type authFile struct {
	PasswordHash string                    `json:"passwordHash"`
	Sessions     map[string]*cookieSession `json:"sessions"` // key: sha256(token)
	SetupCode    *codeEntry                `json:"setupCode,omitempty"`
	PairCodes    []codeEntry               `json:"pairCodes,omitempty"`
}

// Device is a paired client (desktop / mobile app).
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Platform  string    `json:"platform"`
	PublicKey string    `json:"publicKey"` // base64 raw Ed25519 public key
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
}

type challenge struct {
	deviceID string
	expires  time.Time
}

type accessToken struct {
	deviceID string
	expires  time.Time
}

// Store is the auth state for one hub.
type Store struct {
	st *store.Store

	mu        sync.Mutex
	file      authFile
	fileStamp stamp
	dirty     time.Time // last unsaved cookie renewal

	devices   []*Device
	lastSeenW time.Time

	challenges map[string]challenge   // nonce -> challenge
	tokens     map[string]accessToken // sha256(token) -> token

	// OnDevice is called (without locks held) when a device is added,
	// renamed or revoked.
	OnDevice func(d Device, removed bool)
}

type stamp struct {
	mod  time.Time
	size int64
}

func statStamp(path string) stamp {
	fi, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{fi.ModTime(), fi.Size()}
}

// Open loads auth.json and devices.json.
func Open(st *store.Store) (*Store, error) {
	s := &Store{
		st:         st,
		challenges: map[string]challenge{},
		tokens:     map[string]accessToken{},
	}
	if err := s.reloadLocked(true); err != nil {
		return nil, err
	}
	if _, err := st.Load(devicesFileName, &s.devices); err != nil {
		return nil, err
	}
	return s, nil
}

// reloadLocked re-reads auth.json when it changed on disk (or always when
// force is set). Caller holds s.mu.
func (s *Store) reloadLocked(force bool) error {
	path := s.st.Path(authFileName)
	cur := statStamp(path)
	if !force && cur == s.fileStamp {
		return nil
	}
	var f authFile
	if _, err := s.st.Load(authFileName, &f); err != nil {
		return err
	}
	if f.Sessions == nil {
		f.Sessions = map[string]*cookieSession{}
	}
	s.file = f
	s.fileStamp = cur
	return nil
}

func (s *Store) saveLocked() error {
	now := time.Now()
	for k, c := range s.file.Sessions {
		if now.After(c.Expires) {
			delete(s.file.Sessions, k)
		}
	}
	live := s.file.PairCodes[:0]
	for _, c := range s.file.PairCodes {
		if now.Before(c.Expires) {
			live = append(live, c)
		}
	}
	s.file.PairCodes = live
	if err := s.st.Save(authFileName, &s.file); err != nil {
		return err
	}
	s.fileStamp = statStamp(s.st.Path(authFileName))
	s.dirty = time.Time{}
	return nil
}

func (s *Store) refresh() {
	_ = s.reloadLocked(false)
}

// NeedsSetup reports whether no admin password exists yet.
func (s *Store) NeedsSetup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	return s.file.PasswordHash == ""
}

// HasValidSetupCode reports whether an unexpired setup code exists.
func (s *Store) HasValidSetupCode() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	return s.file.SetupCode != nil && time.Now().Before(s.file.SetupCode.Expires)
}

// NewSetupCode creates a fresh one-time setup code (replacing any old one).
func (s *Store) NewSetupCode() (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	if s.file.PasswordHash != "" {
		return "", time.Time{}, ErrAlreadySetup
	}
	code := NewCode()
	exp := time.Now().Add(SetupCodeTTL)
	s.file.SetupCode = &codeEntry{Hash: hashSecret(NormalizeCode(code)), Expires: exp}
	return code, exp, s.saveLocked()
}

// CompleteSetup consumes the setup code, sets the admin password and
// returns a new cookie session token.
func (s *Store) CompleteSetup(code, password string) (string, error) {
	if len(password) < MinPassword {
		return "", ErrWeakPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	if s.file.PasswordHash != "" {
		return "", ErrAlreadySetup
	}
	sc := s.file.SetupCode
	if sc == nil || time.Now().After(sc.Expires) ||
		subtle.ConstantTimeCompare([]byte(sc.Hash), []byte(hashSecret(NormalizeCode(code)))) != 1 {
		return "", ErrBadCode
	}
	s.file.SetupCode = nil
	s.file.PasswordHash = hash
	tok := s.newCookieLocked()
	return tok, s.saveLocked()
}

// SetPassword replaces the admin password and logs out every browser.
// Used by `nextboard reset-password`.
func (s *Store) SetPassword(password string) error {
	if len(password) < MinPassword {
		return ErrWeakPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	s.file.PasswordHash = hash
	s.file.SetupCode = nil
	s.file.Sessions = map[string]*cookieSession{}
	return s.saveLocked()
}

// Login verifies the admin password and returns a new cookie token.
func (s *Store) Login(password string) (string, error) {
	s.mu.Lock()
	s.refresh()
	hash := s.file.PasswordHash
	s.mu.Unlock()
	if hash == "" {
		return "", ErrNotSetup
	}
	// argon2 is slow by design; run it without holding the lock.
	if !VerifyPassword(password, hash) {
		return "", ErrBadPassword
	}
	return s.finishLogin(hash)
}

// finishLogin opens a session for a password that matched hash. That check ran without the lock, so
// `nextboard reset-password` may have replaced the password since: saving without a reload would
// write the old hash back, and a reset promises that the old password stops working.
func (s *Store) finishLogin(hash string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	if s.file.PasswordHash != hash {
		return "", ErrBadPassword
	}
	tok := s.newCookieLocked()
	return tok, s.saveLocked()
}

func (s *Store) newCookieLocked() string {
	tok := randomToken()
	now := time.Now()
	s.file.Sessions[hashSecret(tok)] = &cookieSession{Created: now, Expires: now.Add(CookieTTL), LastUsed: now}
	return tok
}

// ValidateCookie checks a cookie token. When renew is true the caller should
// re-send the cookie (its expiry was extended).
func (s *Store) ValidateCookie(tok string) (ok, renew bool) {
	if tok == "" {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	c := s.file.Sessions[hashSecret(tok)]
	now := time.Now()
	if c == nil || now.After(c.Expires) {
		return false, false
	}
	// Sliding expiry, extended at most hourly to limit disk writes.
	if now.Sub(c.LastUsed) > time.Hour {
		c.LastUsed = now
		c.Expires = now.Add(CookieTTL)
		renew = true
		if err := s.saveLocked(); err != nil {
			s.dirty = now
		}
	}
	return true, renew
}

// Logout deletes a cookie session.
func (s *Store) Logout(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	delete(s.file.Sessions, hashSecret(tok))
	_ = s.saveLocked()
}

// NewPairCode creates a one-time pairing code valid for PairCodeTTL.
func (s *Store) NewPairCode() (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	code := NewCode()
	exp := time.Now().Add(PairCodeTTL)
	s.file.PairCodes = append(s.file.PairCodes, codeEntry{Hash: hashSecret(NormalizeCode(code)), Expires: exp})
	return code, exp, s.saveLocked()
}

// CompletePairing consumes a pairing code and registers a device.
func (s *Store) CompletePairing(code, name, platform, publicKey string) (Device, error) {
	name = strings.TrimSpace(name)
	platform = strings.TrimSpace(platform)
	pub, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize || name == "" || len(name) > 64 || len(platform) > 32 {
		return Device{}, ErrBadInput
	}
	if platform == "" {
		platform = "unknown"
	}
	s.mu.Lock()
	s.refresh()
	h := hashSecret(NormalizeCode(code))
	now := time.Now()
	idx := -1
	for i, c := range s.file.PairCodes {
		if now.Before(c.Expires) && subtle.ConstantTimeCompare([]byte(c.Hash), []byte(h)) == 1 {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return Device{}, ErrBadCode
	}
	s.file.PairCodes = append(s.file.PairCodes[:idx], s.file.PairCodes[idx+1:]...)
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return Device{}, err
	}
	d := &Device{
		ID:        RandomID("d_"),
		Name:      name,
		Platform:  platform,
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		CreatedAt: now,
		LastSeen:  now,
	}
	s.devices = append(s.devices, d)
	err = s.saveDevicesLocked()
	out := *d
	s.mu.Unlock()
	if err != nil {
		return Device{}, err
	}
	s.notify(out, false)
	return out, nil
}

func (s *Store) notify(d Device, removed bool) {
	if s.OnDevice != nil {
		s.OnDevice(d, removed)
	}
}

func (s *Store) saveDevicesLocked() error {
	if s.devices == nil {
		s.devices = []*Device{}
	}
	return s.st.Save(devicesFileName, s.devices)
}

func (s *Store) findDeviceLocked(id string) *Device {
	for _, d := range s.devices {
		if d.ID == id {
			return d
		}
	}
	return nil
}

// Devices returns a copy of all paired devices, oldest first.
func (s *Store) Devices() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Device returns one device.
func (s *Store) Device(id string) (Device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.findDeviceLocked(id)
	if d == nil {
		return Device{}, false
	}
	return *d, true
}

// RenameDevice changes a device's display name.
func (s *Store) RenameDevice(id, name string) (Device, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return Device{}, ErrBadInput
	}
	s.mu.Lock()
	d := s.findDeviceLocked(id)
	if d == nil {
		s.mu.Unlock()
		return Device{}, ErrUnknownDevice
	}
	d.Name = name
	err := s.saveDevicesLocked()
	out := *d
	s.mu.Unlock()
	if err == nil {
		s.notify(out, false)
	}
	return out, err
}

// RevokeDevice removes a device and invalidates its access tokens.
func (s *Store) RevokeDevice(id string) error {
	s.mu.Lock()
	idx := -1
	for i, d := range s.devices {
		if d.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return ErrUnknownDevice
	}
	d := *s.devices[idx]
	s.devices = append(s.devices[:idx], s.devices[idx+1:]...)
	for k, t := range s.tokens {
		if t.deviceID == id {
			delete(s.tokens, k)
		}
	}
	for k, c := range s.challenges {
		if c.deviceID == id {
			delete(s.challenges, k)
		}
	}
	err := s.saveDevicesLocked()
	s.mu.Unlock()
	s.notify(d, true)
	return err
}

// TouchDevice updates LastSeen (persisted at most once a minute).
func (s *Store) TouchDevice(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.findDeviceLocked(id)
	if d == nil {
		return
	}
	now := time.Now()
	d.LastSeen = now
	if now.Sub(s.lastSeenW) > time.Minute {
		s.lastSeenW = now
		_ = s.saveDevicesLocked()
	}
}

// Flush persists pending lastSeen / cookie renewals (called on shutdown).
func (s *Store) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.saveDevicesLocked()
	if !s.dirty.IsZero() {
		s.refresh() // a CLI change on disk outranks an unsaved cookie renewal
		_ = s.saveLocked()
	}
}

// Challenge issues a single-use nonce for a paired device.
func (s *Store) Challenge(deviceID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findDeviceLocked(deviceID) == nil {
		return "", ErrUnknownDevice
	}
	now := time.Now()
	for k, c := range s.challenges {
		if now.After(c.expires) {
			delete(s.challenges, k)
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	nonce := base64.StdEncoding.EncodeToString(b)
	s.challenges[nonce] = challenge{deviceID: deviceID, expires: now.Add(challengeTTL)}
	return nonce, nil
}

// DeviceLogin verifies sig = Ed25519(utf8(nonce)) and returns an opaque
// access token valid for AccessTokenTTL.
func (s *Store) DeviceLogin(deviceID, nonce, sigB64 string) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.challenges[nonce]
	delete(s.challenges, nonce)
	now := time.Now()
	if !ok || c.deviceID != deviceID || now.After(c.expires) {
		return "", time.Time{}, ErrBadSignature
	}
	d := s.findDeviceLocked(deviceID)
	if d == nil {
		return "", time.Time{}, ErrUnknownDevice
	}
	pub, err := base64.StdEncoding.DecodeString(d.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", time.Time{}, ErrBadSignature
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), []byte(nonce), sig) {
		return "", time.Time{}, ErrBadSignature
	}
	for k, t := range s.tokens {
		if now.After(t.expires) {
			delete(s.tokens, k)
		}
	}
	tok := randomToken()
	exp := now.Add(AccessTokenTTL)
	s.tokens[hashSecret(tok)] = accessToken{deviceID: deviceID, expires: exp}
	d.LastSeen = now
	return tok, exp, nil
}

// ValidateBearer returns the device owning a live access token.
func (s *Store) ValidateBearer(tok string) (string, bool) {
	if tok == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[hashSecret(tok)]
	if !ok || time.Now().After(t.expires) || s.findDeviceLocked(t.deviceID) == nil {
		return "", false
	}
	return t.deviceID, true
}
