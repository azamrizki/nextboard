package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"strings"
)

// codeAlphabet omits ambiguous characters (0/O, 1/I/L).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// NewCode returns a random one-time code formatted XXXX-XXXX.
func NewCode() string {
	var b strings.Builder
	max := big.NewInt(int64(len(codeAlphabet)))
	for i := 0; i < 8; i++ {
		if i == 4 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	return b.String()
}

// NormalizeCode uppercases a user-entered code and strips separators.
func NormalizeCode(s string) string {
	s = strings.ToUpper(s)
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, s)
}

// hashSecret returns hex(sha256(s)). Codes and tokens are stored hashed so a
// leaked state file does not yield usable credentials.
func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// randomToken returns 32 random bytes, base64url without padding.
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// RandomID returns prefix + 10 lowercase base32-ish characters.
func RandomID(prefix string) string {
	const alpha = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return prefix + string(b)
}
