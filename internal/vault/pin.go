package vault

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"time"
)

// PIN unlock (docs/API-vault.md). Six digits are a million guesses, so the hub must never hold
// what a PIN opens: an agent reading the data folder would try them all. And a browser alone must
// not either, or whoever copies its storage could. So it takes both:
//
//	browser: salt, and the vault key wrapped under the secret
//	hub:     a random key per PIN, and how many tries are left
//	secret = HMAC(hub key, Argon2id(PIN, salt))
//
// The hub hands the secret out only for the right proof, and counts the wrong ones. It never sees
// the PIN, and without the browser's salt it could not try PINs against what it keeps.

const (
	// MaxPinAttempts wrong tries in a row and the PIN is gone: back to the master password.
	MaxPinAttempts = 5
	// maxPins counts browsers. Past it the oldest PIN goes, and that browser asks for the password.
	maxPins = 32

	proofBytes = 32
)

var (
	ErrWrongPin = errors.New("PIN salah")
	ErrPinGone  = errors.New("PIN tidak berlaku lagi")
)

// Pin is the hub's half of one browser's PIN.
type Pin struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Check     string    `json:"check"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"createdAt"`
}

func pinSecret(key, proof []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(proof)
	return m.Sum(nil)
}

func pinCheck(secret []byte) string {
	sum := sha256.Sum256(append([]byte("nextboard-vault/v1/pin-check/"), secret...))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func decodeProof(proof string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(proof)
	if err != nil || len(raw) != proofBytes {
		return nil, fmt.Errorf("%w: proof", ErrInvalid)
	}
	return raw, nil
}

func validatePin(id string, p Pin) error {
	if p.ID != id || !idRe.MatchString(id) {
		return errors.New("pin: id tidak cocok")
	}
	key, err := base64.StdEncoding.DecodeString(p.Key)
	if err != nil || len(key) != 32 {
		return errors.New("pin: key")
	}
	return nil
}

// CreatePin starts a PIN for one browser and returns its id and the secret to wrap the key under.
func (s *Service) CreatePin(proof string) (id, secret string, err error) {
	raw, err := decodeProof(proof)
	if err != nil {
		return "", "", err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", "", err
	}
	id, err = randomID()
	if err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return "", "", err
	}
	if s.d.Header == nil {
		return "", "", ErrAbsent
	}
	sec := pinSecret(key, raw)
	next := s.d
	next.Pins = maps.Clone(s.d.Pins)
	if next.Pins == nil {
		next.Pins = map[string]Pin{}
	}
	for len(next.Pins) >= maxPins {
		oldest := ""
		for k, p := range next.Pins {
			if oldest == "" || p.CreatedAt.Before(next.Pins[oldest].CreatedAt) {
				oldest = k
			}
		}
		delete(next.Pins, oldest)
	}
	next.Pins[id] = Pin{ID: id, Key: base64.StdEncoding.EncodeToString(key), Check: pinCheck(sec), CreatedAt: now()}
	if err := s.save(next); err != nil {
		return "", "", err
	}
	return id, base64.StdEncoding.EncodeToString(sec), nil
}

// OpenPin returns the secret for the right proof. A wrong one gets nothing and costs a try, saved
// before it is answered, so restarting the hub gives none back. left is how many remain.
func (s *Service) OpenPin(id, proof string) (secret string, left int, err error) {
	raw, err := decodeProof(proof)
	if err != nil {
		return "", 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return "", 0, err
	}
	p, ok := s.d.Pins[id]
	if !ok || s.d.Header == nil {
		return "", 0, ErrPinGone
	}
	key, err := base64.StdEncoding.DecodeString(p.Key)
	if err != nil {
		return "", 0, fmt.Errorf("%w: pin", ErrUnreadable)
	}
	sec := pinSecret(key, raw)
	next := s.d
	next.Pins = maps.Clone(s.d.Pins)
	if subtle.ConstantTimeCompare([]byte(pinCheck(sec)), []byte(p.Check)) == 1 {
		if p.Attempts > 0 {
			p.Attempts = 0
			next.Pins[id] = p
			if err := s.save(next); err != nil {
				return "", 0, err
			}
		}
		return base64.StdEncoding.EncodeToString(sec), MaxPinAttempts, nil
	}
	p.Attempts++
	left = MaxPinAttempts - p.Attempts
	if left <= 0 {
		delete(next.Pins, id)
	} else {
		next.Pins[id] = p
	}
	if err := s.save(next); err != nil {
		return "", 0, err
	}
	if left <= 0 {
		return "", 0, ErrPinGone
	}
	return "", left, ErrWrongPin
}

// DeletePin turns one browser's PIN off. Asking for one that is gone is fine.
func (s *Service) DeletePin(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return err
	}
	if _, ok := s.d.Pins[id]; !ok {
		return nil
	}
	next := s.d
	next.Pins = maps.Clone(s.d.Pins)
	delete(next.Pins, id)
	return s.save(next)
}

// ClearPins turns every browser's PIN off, for a browser that was lost.
func (s *Service) ClearPins() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unreadable(); err != nil {
		return err
	}
	if len(s.d.Pins) == 0 {
		return nil
	}
	next := s.d
	next.Pins = nil
	return s.save(next)
}
