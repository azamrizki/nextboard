// Package secret encrypts uploaded SSH credentials with AES-256-GCM using a
// master key kept in <data>/secret.key (0600).
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/azamrizki/nextboard/internal/store"
)

// Box seals and opens blobs with the hub master key.
type Box struct {
	aead cipher.AEAD
}

// Open loads (or creates on first use) the master key at path.
func Open(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := store.WriteFileAtomic(path, key); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s: the master key must be 32 bytes", path)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext; the random nonce is prepended to the output.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a blob produced by Seal.
func (b *Box) Open(blob []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(blob) < n {
		return nil, errors.New("ciphertext too short")
	}
	return b.aead.Open(nil, blob[:n], blob[n:], nil)
}
