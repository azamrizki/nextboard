package machine

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// What a connection needs that only a person can give (docs/API.md "Password dan
// passphrase"). A connection that needs it stops there and says what it needs, instead
// of trying again: a wrong password tried in a loop ends in a ban from the server.

// AuthNeed kinds.
const (
	AuthPassword   = "password"
	AuthPassphrase = "passphrase"
)

// AuthNeed is what a machine is waiting for.
type AuthNeed struct {
	Kind string `json:"kind"`
	// KeyPath is the key a passphrase is for, as the config says it. "" is the key
	// uploaded for this machine.
	KeyPath string `json:"keyPath,omitempty"`
	// Host is who asks: "user@host", which can be a jump host on the way.
	Host string `json:"host"`
	// Prompt is the server's own question, when it asks through keyboard-interactive.
	Prompt string `json:"prompt,omitempty"`
	// Wrong: the last answer was refused.
	Wrong bool `json:"wrong,omitempty"`
	// CanPassword: asked for a passphrase, but the server would also take a password.
	CanPassword bool `json:"canPassword,omitempty"`

	file string // the key's full path
}

type errAuthNeeded struct{ need AuthNeed }

func (e *errAuthNeeded) Error() string {
	if e.need.Kind == AuthPassphrase {
		return "private key perlu passphrase"
	}
	return "server perlu password"
}

// ErrAuthNotNeeded: nothing is waiting for this answer (any more).
var ErrAuthNotNeeded = errors.New("machine tidak sedang menunggu ini")

// errAskPerson ends a method with nothing to answer.
var errAskPerson = errors.New("menunggu jawaban pengguna")

type keyState int

const (
	keyOpen keyState = iota
	keyLocked
	keyWrong
)

// openKey parses a private key. A key that needs a passphrase is no error, only a state.
func openKey(b []byte, passphrase string) (ssh.Signer, keyState, error) {
	s, err := ssh.ParsePrivateKey(b)
	if err == nil {
		return s, keyOpen, nil
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil, keyOpen, err
	}
	if passphrase == "" {
		return nil, keyLocked, nil
	}
	s, err = ssh.ParsePrivateKeyWithPassphrase(b, []byte(passphrase))
	if errors.Is(err, x509.IncorrectPasswordError) {
		return nil, keyWrong, nil
	}
	if err != nil {
		return nil, keyOpen, err
	}
	return s, keyOpen, nil
}

// with lays what was typed over what is stored.
func (c Creds) with(typed Creds) Creds {
	if typed.Password != "" {
		c.Password = typed.Password
	}
	if typed.Passphrase != "" {
		c.Passphrase = typed.Passphrase
	}
	if len(typed.KeyPassphrases) > 0 {
		merged := make(map[string]string, len(c.KeyPassphrases)+len(typed.KeyPassphrases))
		for k, v := range c.KeyPassphrases {
			merged[k] = v
		}
		for k, v := range typed.KeyPassphrases {
			merged[k] = v
		}
		c.KeyPassphrases = merged
	}
	return c
}

type lockedKey struct {
	path, file string
	wrong      bool
}

// authAttempt is what one handshake learned about the server and about the keys.
type authAttempt struct {
	locked []lockedKey
	// noKeys is what to say if the server only takes keys and there are none.
	noKeys error

	takesKeys     bool
	asksPassword  bool
	triedPassword bool
	prompt        string
}

// password answers a password method, or notes that a person has to.
func (a *authAttempt) password(pw, prompt string) (string, error) {
	if prompt != "" {
		a.prompt = strings.TrimSpace(prompt)
	}
	if pw == "" {
		a.asksPassword = true
		return "", errAskPerson
	}
	a.triedPassword = true
	return pw, nil
}

// authEnded says the handshake got to the end of what could be tried, as opposed to
// a connection that broke on the way.
func authEnded(err error) bool {
	return errors.Is(err, errAskPerson) || strings.Contains(err.Error(), "unable to authenticate")
}

// need is what to ask for after every method failed. A password the server refused comes
// first: that is what the person chose to use. Then a key that could not be opened, since
// the server takes keys and it may be the one. Nil: nothing a person could type would help.
func (a *authAttempt) need(t target) *AuthNeed {
	host := t.hostName
	if t.user != "" {
		host = t.user + "@" + host
	}
	if a.triedPassword {
		return &AuthNeed{Kind: AuthPassword, Host: host, Prompt: a.prompt, Wrong: true}
	}
	if a.takesKeys && len(a.locked) > 0 {
		k := a.locked[0]
		return &AuthNeed{Kind: AuthPassphrase, KeyPath: k.path, Host: host, Wrong: k.wrong, CanPassword: a.asksPassword, file: k.file}
	}
	if a.asksPassword {
		return &AuthNeed{Kind: AuthPassword, Host: host, Prompt: a.prompt}
	}
	return nil
}

// Answer gives the connection what it asked for and connects. A passphrase is tried on
// the key right here, so a typing mistake never reaches the server. With remember the
// answer is stored encrypted once a connection proved it right; without, it stays in
// memory for reconnecting until the hub restarts.
func (m *SSH) Answer(ctx context.Context, kind, value string, remember bool) error {
	m.mu.Lock()
	need := m.needAuth
	switch {
	case need == nil:
		m.mu.Unlock()
		return ErrAuthNotNeeded
	case kind == AuthPassword && (need.Kind == AuthPassword || need.CanPassword):
		m.answers.Password = value
	case kind == AuthPassphrase && need.Kind == AuthPassphrase:
		path, file := need.KeyPath, need.file
		m.mu.Unlock()
		state, err := m.tryPassphrase(file, value)
		m.mu.Lock()
		if err != nil || state != keyOpen {
			if m.needAuth != nil {
				wrong := *m.needAuth
				wrong.Wrong = true
				m.needAuth = &wrong
			}
			m.mu.Unlock()
			m.changed()
			return nil
		}
		if file == "" {
			m.answers.Passphrase = value
		} else {
			if m.answers.KeyPassphrases == nil {
				m.answers.KeyPassphrases = map[string]string{}
			}
			m.answers.KeyPassphrases[path] = value
		}
	default:
		m.mu.Unlock()
		return ErrAuthNotNeeded
	}
	m.needAuth = nil
	m.keepAnswers = remember
	m.status = StatusConnecting
	m.mu.Unlock()
	err := m.Connect(ctx)
	m.EnsureLoop()
	if err != nil {
		return fmt.Errorf("sambung: %w", err)
	}
	return nil
}

// tryPassphrase opens the key file, or the uploaded key when file is "".
func (m *SSH) tryPassphrase(file, passphrase string) (keyState, error) {
	var b []byte
	var err error
	if file != "" {
		b, err = os.ReadFile(file)
	} else {
		var c *Creds
		if c, err = m.loadCreds(); err == nil {
			b = []byte(c.PrivateKey)
		}
	}
	if err != nil {
		return keyOpen, err
	}
	_, state, err := openKey(b, passphrase)
	return state, err
}
