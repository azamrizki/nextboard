package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/store"
)

const testPassword = "correct horse"

// testHash is computed once: argon2 is slow and uses 64 MiB each time.
var testHash = sync.OnceValue(func() string {
	h, err := HashPassword(testPassword)
	if err != nil {
		panic(err)
	}
	return h
})

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return openStore(t, t.TempDir())
}

// withPassword is a hub that finished setup, without paying for a new hash.
func withPassword(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.file.PasswordHash = testHash()
	if err := s.saveLocked(); err != nil {
		t.Fatal(err)
	}
	return s
}

func login(t *testing.T, s *Store) string {
	t.Helper()
	tok, err := s.Login(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func onDisk(t *testing.T, s *Store, name string) string {
	t.Helper()
	b, err := os.ReadFile(s.st.Path(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Errorf("err = %v, want %v", got, want)
	}
}

// --- passwords ---

func TestHashPassword(t *testing.T) {
	h := testHash()
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=2,p=2$") {
		t.Errorf("hash = %q", h)
	}
	if !VerifyPassword(testPassword, h) {
		t.Error("the right password was refused")
	}
	if VerifyPassword(testPassword+"x", h) || VerifyPassword("", h) {
		t.Error("a wrong password was accepted")
	}
	other, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if other == h {
		t.Error("two hashes are identical: the salt repeats")
	}
}

func TestVerifyPasswordRefusesDamagedHashes(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef"))
	key := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	const params = "m=64,t=1,p=1"
	cases := map[string]string{
		"empty":         "",
		"plain text":    "hunter2",
		"other algo":    "$2y$10$abcdefghijklmnopqrstuv",
		"argon2i":       "$argon2i$v=19$" + params + "$" + salt + "$" + key,
		"old version":   "$argon2id$v=16$" + params + "$" + salt + "$" + key,
		"bad version":   "$argon2id$v=x$" + params + "$" + salt + "$" + key,
		"bad params":    "$argon2id$v=19$m=x,t=1,p=1$" + salt + "$" + key,
		"p too big":     "$argon2id$v=19$m=64,t=1,p=999$" + salt + "$" + key,
		"bad salt":      "$argon2id$v=19$" + params + "$***$" + key,
		"bad key":       "$argon2id$v=19$" + params + "$" + salt + "$***",
		"too few parts": "$argon2id$v=19$" + params + "$" + salt,
		// argon2 panics on these three.
		"empty key":    "$argon2id$v=19$" + params + "$" + salt + "$",
		"zero rounds":  "$argon2id$v=19$m=64,t=0,p=1$" + salt + "$" + key,
		"zero threads": "$argon2id$v=19$m=64,t=1,p=0$" + salt + "$" + key,
	}
	for name, hash := range cases {
		t.Run(name, func(t *testing.T) {
			if VerifyPassword("anything", hash) {
				t.Errorf("%q accepted a password", hash)
			}
		})
	}
}

// --- codes ---

func TestNewCode(t *testing.T) {
	seen := map[string]bool{}
	for range 500 {
		c := NewCode()
		if len(c) != 9 || c[4] != '-' {
			t.Fatalf("code %q is not XXXX-XXXX", c)
		}
		for _, r := range strings.ReplaceAll(c, "-", "") {
			if !strings.ContainsRune(codeAlphabet, r) {
				t.Fatalf("code %q has %q", c, r)
			}
		}
		seen[c] = true
	}
	if len(seen) < 499 {
		t.Errorf("only %d different codes in 500", len(seen))
	}
	if strings.ContainsAny(codeAlphabet, "01OIL") {
		t.Error("the alphabet has characters people confuse")
	}
}

func TestNormalizeCode(t *testing.T) {
	for in, want := range map[string]string{
		"ABCD-EFGH":   "ABCDEFGH",
		"abcd-efgh":   "ABCDEFGH",
		" ab cd-efgh": "ABCDEFGH",
		"":            "",
	} {
		if got := NormalizeCode(in); got != want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRandomValues(t *testing.T) {
	if RandomID("d_") == RandomID("d_") {
		t.Error("RandomID repeats")
	}
	if id := RandomID("d_"); !strings.HasPrefix(id, "d_") || len(id) != 12 {
		t.Errorf("RandomID = %q", id)
	}
	if tok := randomToken(); len(tok) != 43 || strings.ContainsAny(tok, "+/=") {
		t.Errorf("token %q is not 32 bytes of base64url", tok)
	}
}

// --- setup ---

func TestSetup(t *testing.T) {
	s := newStore(t)
	if !s.NeedsSetup() || s.HasValidSetupCode() {
		t.Fatal("a new hub must need setup and have no code")
	}
	code, exp, err := s.NewSetupCode()
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasValidSetupCode() {
		t.Error("the new code is not valid")
	}
	if d := time.Until(exp); d <= 0 || d > SetupCodeTTL {
		t.Errorf("code lives %v", d)
	}
	if strings.Contains(onDisk(t, s, authFileName), NormalizeCode(code)) {
		t.Error("the code is stored in clear")
	}

	// A failed try must not burn the code.
	_, err = s.CompleteSetup("AAAA-AAAA", testPassword)
	wantErr(t, err, ErrBadCode)
	_, err = s.CompleteSetup(code, "short")
	wantErr(t, err, ErrWeakPassword)

	tok, err := s.CompleteSetup(strings.ToLower(strings.ReplaceAll(code, "-", " ")), testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ValidateCookie(tok); !ok {
		t.Error("the setup session is not valid")
	}
	if s.NeedsSetup() || s.HasValidSetupCode() {
		t.Error("setup did not finish")
	}
	stored := onDisk(t, s, authFileName)
	if strings.Contains(stored, testPassword) || strings.Contains(stored, tok) {
		t.Error("the password or the session token is stored in clear")
	}

	// The hub cannot be taken over by running setup again.
	_, err = s.CompleteSetup(code, "another password")
	wantErr(t, err, ErrAlreadySetup)
	_, _, err = s.NewSetupCode()
	wantErr(t, err, ErrAlreadySetup)
}

func TestSetupCodeExpires(t *testing.T) {
	s := newStore(t)
	code, _, err := s.NewSetupCode()
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.file.SetupCode.Expires = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if s.HasValidSetupCode() {
		t.Error("an expired code counts as valid")
	}
	_, err = s.CompleteSetup(code, testPassword)
	wantErr(t, err, ErrBadCode)
}

func TestNewSetupCodeReplacesTheOldOne(t *testing.T) {
	s := newStore(t)
	old, _, err := s.NewSetupCode()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.NewSetupCode(); err != nil {
		t.Fatal(err)
	}
	_, err = s.CompleteSetup(old, testPassword)
	wantErr(t, err, ErrBadCode)
}

// --- login and cookies ---

func TestLoginAndLogout(t *testing.T) {
	_, err := newStore(t).Login(testPassword)
	wantErr(t, err, ErrNotSetup)

	s := withPassword(t)
	_, err = s.Login("wrong password")
	wantErr(t, err, ErrBadPassword)

	tok := login(t, s)
	if ok, renew := s.ValidateCookie(tok); !ok || renew {
		t.Errorf("fresh cookie: ok=%v renew=%v", ok, renew)
	}
	if strings.Contains(onDisk(t, s, authFileName), tok) {
		t.Error("the session token is stored in clear")
	}
	other := login(t, s)
	s.Logout(tok)
	if ok, _ := s.ValidateCookie(tok); ok {
		t.Error("the cookie works after logout")
	}
	if ok, _ := s.ValidateCookie(other); !ok {
		t.Error("logout ended another browser's session")
	}
	for _, bad := range []string{"", "nope"} {
		if ok, _ := s.ValidateCookie(bad); ok {
			t.Errorf("cookie %q is valid", bad)
		}
	}
}

func TestSetPasswordEndsEverySession(t *testing.T) {
	s := withPassword(t)
	tok := login(t, s)
	wantErr(t, s.SetPassword("short"), ErrWeakPassword)
	if ok, _ := s.ValidateCookie(tok); !ok {
		t.Error("a refused reset ended the session")
	}

	if err := s.SetPassword("a new password"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ValidateCookie(tok); ok {
		t.Error("the old session survived the reset")
	}
	_, err := s.Login(testPassword)
	wantErr(t, err, ErrBadPassword)
	if _, err := s.Login("a new password"); err != nil {
		t.Error(err)
	}
}

func TestCookieSlidesAtMostHourly(t *testing.T) {
	s := withPassword(t)
	tok := login(t, s)
	session := func() *cookieSession { return s.file.Sessions[hashSecret(tok)] }

	before := onDisk(t, s, authFileName)
	if ok, renew := s.ValidateCookie(tok); !ok || renew {
		t.Errorf("used just now: ok=%v renew=%v", ok, renew)
	}
	if onDisk(t, s, authFileName) != before {
		t.Error("a use within the hour wrote to disk")
	}

	s.mu.Lock()
	session().LastUsed = time.Now().Add(-2 * time.Hour)
	session().Expires = time.Now().Add(time.Minute)
	s.mu.Unlock()
	if ok, renew := s.ValidateCookie(tok); !ok || !renew {
		t.Errorf("used 2h ago: ok=%v renew=%v", ok, renew)
	}
	if left := time.Until(session().Expires); left < CookieTTL-time.Minute {
		t.Errorf("only %v left after renewal", left)
	}
	if onDisk(t, s, authFileName) == before {
		t.Error("the renewal was not saved")
	}
}

func TestExpiredCookieIsRefusedAndDropped(t *testing.T) {
	s := withPassword(t)
	tok := login(t, s)
	h := hashSecret(tok)
	s.mu.Lock()
	s.file.Sessions[h].Expires = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if ok, _ := s.ValidateCookie(tok); ok {
		t.Error("an expired cookie is valid")
	}
	login(t, s) // any save prunes
	if strings.Contains(onDisk(t, s, authFileName), h) {
		t.Error("the expired session is still on disk")
	}
}

// --- changes from CLI subcommands while the service runs ---

func TestServiceSeesCLIChanges(t *testing.T) {
	dir := t.TempDir()
	service := openStore(t, dir)
	if !service.NeedsSetup() {
		t.Fatal("new hub")
	}

	cli := openStore(t, dir)
	code, _, err := cli.NewSetupCode()
	if err != nil {
		t.Fatal(err)
	}
	if !service.HasValidSetupCode() {
		t.Fatal("the service did not see `setup-link`")
	}
	tok, err := service.CompleteSetup(code, testPassword)
	if err != nil {
		t.Fatal(err)
	}

	cli = openStore(t, dir)
	pair, _, err := cli.NewPairCode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompletePairing(pair, "Phone", "android", newDeviceKey(t).pub); err != nil {
		t.Errorf("the service did not see `pair`: %v", err)
	}

	cli = openStore(t, dir)
	if err := cli.SetPassword("a new password"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := service.ValidateCookie(tok); ok {
		t.Error("the session survived `reset-password`")
	}
}

// A login checks the password without the lock, for as long as argon2 takes.
// A reset landing in that window must win.
func TestLoginDoesNotUndoAPasswordReset(t *testing.T) {
	dir := t.TempDir()
	service := openStore(t, dir)
	service.mu.Lock()
	service.file.PasswordHash = testHash()
	if err := service.saveLocked(); err != nil {
		service.mu.Unlock()
		t.Fatal(err)
	}
	service.mu.Unlock()

	if err := openStore(t, dir).SetPassword("a new password"); err != nil {
		t.Fatal(err)
	}
	// Same point as Login right after VerifyPassword passed on the old hash.
	_, err := service.finishLogin(testHash())
	wantErr(t, err, ErrBadPassword)

	after := openStore(t, dir)
	_, err = after.Login(testPassword)
	wantErr(t, err, ErrBadPassword)
	if _, err := after.Login("a new password"); err != nil {
		t.Errorf("the reset was undone: %v", err)
	}
}

func TestFlushDoesNotUndoAPasswordReset(t *testing.T) {
	dir := t.TempDir()
	service := openStore(t, dir)
	service.mu.Lock()
	service.file.PasswordHash = testHash()
	if err := service.saveLocked(); err != nil {
		service.mu.Unlock()
		t.Fatal(err)
	}
	service.dirty = time.Now() // a renewal that could not be saved
	service.mu.Unlock()

	if err := openStore(t, dir).SetPassword("a new password"); err != nil {
		t.Fatal(err)
	}
	service.Flush()
	if _, err := openStore(t, dir).Login("a new password"); err != nil {
		t.Errorf("Flush undid the reset: %v", err)
	}
}

func TestDamagedFile(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	s.mu.Lock()
	s.file.PasswordHash = testHash()
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	if err := os.WriteFile(s.st.Path(authFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A running hub must not fall back to "needs setup": that would offer the hub to anyone.
	if s.NeedsSetup() {
		t.Error("a damaged file put the running hub back into setup")
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(st); err == nil {
		t.Error("opening a damaged file must fail, not start empty")
	}
}

// --- pairing ---

type deviceKey struct {
	pub  string
	priv ed25519.PrivateKey
}

func newDeviceKey(t *testing.T) deviceKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return deviceKey{base64.StdEncoding.EncodeToString(pub), priv}
}

func (k deviceKey) sign(nonce string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(k.priv, []byte(nonce)))
}

func pairDevice(t *testing.T, s *Store, name string) (Device, deviceKey) {
	t.Helper()
	code, _, err := s.NewPairCode()
	if err != nil {
		t.Fatal(err)
	}
	k := newDeviceKey(t)
	d, err := s.CompletePairing(code, name, "android", k.pub)
	if err != nil {
		t.Fatal(err)
	}
	return d, k
}

func TestPairing(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	var events []string
	s.OnDevice = func(d Device, removed bool) { events = append(events, fmt.Sprint(d.Name, removed)) }

	code, exp, err := s.NewPairCode()
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d <= 0 || d > PairCodeTTL {
		t.Errorf("code lives %v", d)
	}
	if strings.Contains(onDisk(t, s, authFileName), NormalizeCode(code)) {
		t.Error("the code is stored in clear")
	}
	k := newDeviceKey(t)

	// Input errors come before the code check and must not burn the code.
	for _, bad := range []struct{ name, platform, key string }{
		{"", "android", k.pub},
		{"   ", "android", k.pub},
		{strings.Repeat("n", 65), "android", k.pub},
		{"Phone", strings.Repeat("p", 33), k.pub},
		{"Phone", "android", "not base64!"},
		{"Phone", "android", base64.StdEncoding.EncodeToString([]byte("short"))},
	} {
		_, err := s.CompletePairing(code, bad.name, bad.platform, bad.key)
		wantErr(t, err, ErrBadInput)
	}
	_, err = s.CompletePairing("AAAA-AAAA", "Phone", "android", k.pub)
	wantErr(t, err, ErrBadCode)

	d, err := s.CompletePairing(strings.ToLower(code), "  Phone  ", "", k.pub)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Phone" || d.Platform != "unknown" || !strings.HasPrefix(d.ID, "d_") {
		t.Errorf("device = %+v", d)
	}
	if len(events) != 1 || events[0] != "Phonefalse" {
		t.Errorf("events = %v", events)
	}

	_, err = s.CompletePairing(code, "Second", "android", k.pub)
	wantErr(t, err, ErrBadCode)

	if got, ok := openStore(t, dir).Device(d.ID); !ok || got.Name != "Phone" {
		t.Errorf("the device did not survive a restart: %+v %v", got, ok)
	}
}

func TestPairCodesExpireAndStandAlone(t *testing.T) {
	s := newStore(t)
	first, _, err := s.NewPairCode()
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.NewPairCode()
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.file.PairCodes[0].Expires = time.Now().Add(-time.Second)
	firstHash := s.file.PairCodes[0].Hash
	s.mu.Unlock()

	k := newDeviceKey(t)
	_, err = s.CompletePairing(first, "Phone", "android", k.pub)
	wantErr(t, err, ErrBadCode)
	if _, err := s.CompletePairing(second, "Phone", "android", k.pub); err != nil {
		t.Errorf("a second live code stopped working: %v", err)
	}
	if strings.Contains(onDisk(t, s, authFileName), firstHash) {
		t.Error("the expired code is still on disk")
	}
}

func TestDevicesAreCopiesOldestFirst(t *testing.T) {
	s := newStore(t)
	a, _ := pairDevice(t, s, "A")
	b, _ := pairDevice(t, s, "B")
	s.mu.Lock()
	s.findDeviceLocked(a.ID).CreatedAt = time.Now().Add(time.Hour)
	s.mu.Unlock()

	list := s.Devices()
	if len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Fatalf("list = %+v", list)
	}
	list[0].Name = "changed"
	if got, _ := s.Device(b.ID); got.Name != "B" {
		t.Error("the caller changed a stored device")
	}
	if _, ok := s.Device("missing"); ok {
		t.Error("found a device that does not exist")
	}
}

func TestRenameDevice(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	d, _ := pairDevice(t, s, "Old")
	var renamed []string
	s.OnDevice = func(d Device, removed bool) { renamed = append(renamed, d.Name) }

	for _, bad := range []string{"", "  ", strings.Repeat("n", 65)} {
		_, err := s.RenameDevice(d.ID, bad)
		wantErr(t, err, ErrBadInput)
	}
	_, err := s.RenameDevice("missing", "New")
	wantErr(t, err, ErrUnknownDevice)
	if len(renamed) != 0 {
		t.Errorf("refused renames fired %v", renamed)
	}

	got, err := s.RenameDevice(d.ID, "  New ")
	if err != nil || got.Name != "New" {
		t.Fatalf("got %+v err=%v", got, err)
	}
	if len(renamed) != 1 || renamed[0] != "New" {
		t.Errorf("events = %v", renamed)
	}
	if got, _ := openStore(t, dir).Device(d.ID); got.Name != "New" {
		t.Errorf("name after restart: %q", got.Name)
	}
}

// --- device login ---

func deviceLogin(t *testing.T, s *Store, d Device, k deviceKey) string {
	t.Helper()
	nonce, err := s.Challenge(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.DeviceLogin(d.ID, nonce, k.sign(nonce))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestDeviceLogin(t *testing.T) {
	s := newStore(t)
	d, k := pairDevice(t, s, "Phone")

	_, err := s.Challenge("missing")
	wantErr(t, err, ErrUnknownDevice)

	nonce, err := s.Challenge(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	tok, exp, err := s.DeviceLogin(d.ID, nonce, k.sign(nonce))
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(exp); left <= 0 || left > AccessTokenTTL {
		t.Errorf("token lives %v", left)
	}
	if got, ok := s.ValidateBearer(tok); !ok || got != d.ID {
		t.Errorf("ValidateBearer = %q, %v", got, ok)
	}

	// The nonce works once.
	_, _, err = s.DeviceLogin(d.ID, nonce, k.sign(nonce))
	wantErr(t, err, ErrBadSignature)

	for _, bad := range []string{"", "nope"} {
		if _, ok := s.ValidateBearer(bad); ok {
			t.Errorf("bearer %q is valid", bad)
		}
	}
	// Tokens stay in memory only.
	if strings.Contains(onDisk(t, s, authFileName)+onDisk(t, s, devicesFileName), tok) {
		t.Error("the access token is on disk")
	}
}

func TestDeviceLoginRefusals(t *testing.T) {
	s := newStore(t)
	d, k := pairDevice(t, s, "Phone")
	other, otherKey := pairDevice(t, s, "Tablet")

	challenge := func() string {
		t.Helper()
		n, err := s.Challenge(d.ID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for name, try := range map[string]func() error{
		"nonce never issued": func() error {
			_, _, err := s.DeviceLogin(d.ID, "made-up", k.sign("made-up"))
			return err
		},
		"signed by another device's key": func() error {
			n := challenge()
			_, _, err := s.DeviceLogin(d.ID, n, otherKey.sign(n))
			return err
		},
		"another device's nonce": func() error {
			n := challenge()
			_, _, err := s.DeviceLogin(other.ID, n, otherKey.sign(n))
			return err
		},
		"signature of something else": func() error {
			n := challenge()
			_, _, err := s.DeviceLogin(d.ID, n, k.sign(n+"x"))
			return err
		},
		"signature not base64": func() error {
			n := challenge()
			_, _, err := s.DeviceLogin(d.ID, n, "not base64!")
			return err
		},
		"expired nonce": func() error {
			n := challenge()
			s.mu.Lock()
			c := s.challenges[n]
			c.expires = time.Now().Add(-time.Second)
			s.challenges[n] = c
			s.mu.Unlock()
			_, _, err := s.DeviceLogin(d.ID, n, k.sign(n))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { wantErr(t, try(), ErrBadSignature) })
	}

	// A failed try burns the nonce: no retries against the same one.
	n := challenge()
	_, _, err := s.DeviceLogin(d.ID, n, otherKey.sign(n))
	wantErr(t, err, ErrBadSignature)
	_, _, err = s.DeviceLogin(d.ID, n, k.sign(n))
	wantErr(t, err, ErrBadSignature)
}

func TestExpiredBearerIsRefused(t *testing.T) {
	s := newStore(t)
	d, k := pairDevice(t, s, "Phone")
	tok := deviceLogin(t, s, d, k)
	h := hashSecret(tok)
	s.mu.Lock()
	entry := s.tokens[h]
	entry.expires = time.Now().Add(-time.Second)
	s.tokens[h] = entry
	s.mu.Unlock()
	if _, ok := s.ValidateBearer(tok); ok {
		t.Error("an expired token is valid")
	}
	deviceLogin(t, s, d, k) // the next login prunes
	s.mu.Lock()
	_, kept := s.tokens[h]
	s.mu.Unlock()
	if kept {
		t.Error("the expired token is still kept")
	}
}

func TestRevokeDevice(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	d, k := pairDevice(t, s, "Phone")
	keep, keepKey := pairDevice(t, s, "Tablet")
	tok := deviceLogin(t, s, d, k)
	keepTok := deviceLogin(t, s, keep, keepKey)
	pending, err := s.Challenge(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	var removed []string
	s.OnDevice = func(d Device, gone bool) {
		if gone {
			removed = append(removed, d.ID)
		}
	}

	wantErr(t, s.RevokeDevice("missing"), ErrUnknownDevice)
	if err := s.RevokeDevice(d.ID); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != d.ID {
		t.Errorf("events = %v", removed)
	}
	if _, ok := s.ValidateBearer(tok); ok {
		t.Error("the token survived the revoke")
	}
	if _, _, err := s.DeviceLogin(d.ID, pending, k.sign(pending)); err == nil {
		t.Error("a pending challenge survived the revoke")
	}
	if _, err := s.Challenge(d.ID); !errors.Is(err, ErrUnknownDevice) {
		t.Errorf("challenge after revoke: %v", err)
	}
	if _, ok := s.ValidateBearer(keepTok); !ok {
		t.Error("revoking one device ended another's token")
	}
	if _, ok := openStore(t, dir).Device(d.ID); ok {
		t.Error("the device came back after a restart")
	}
}

func TestTouchDeviceWritesAtMostOncePerMinute(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	d, _ := pairDevice(t, s, "Phone")
	s.TouchDevice("missing") // must not panic

	s.TouchDevice(d.ID) // first touch saves and starts the minute
	first := onDisk(t, s, devicesFileName)
	time.Sleep(2 * time.Millisecond)
	s.TouchDevice(d.ID)
	if onDisk(t, s, devicesFileName) != first {
		t.Error("a second touch within the minute wrote to disk")
	}
	mem, _ := s.Device(d.ID)
	saved, _ := openStore(t, dir).Device(d.ID)
	if !mem.LastSeen.After(saved.LastSeen) {
		t.Error("the second touch did not update the time in memory")
	}

	s.Flush()
	saved, _ = openStore(t, dir).Device(d.ID)
	if !saved.LastSeen.Equal(mem.LastSeen) {
		t.Errorf("Flush saved %v, want %v", saved.LastSeen, mem.LastSeen)
	}
}

// --- rate limiter ---

func TestRateLimiter(t *testing.T) {
	l := NewRateLimiter(2, time.Hour)
	if l.Blocked("a") {
		t.Error("blocked before any failure")
	}
	l.Fail("a")
	if l.Blocked("a") {
		t.Error("blocked below the limit")
	}
	l.Fail("a")
	if !l.Blocked("a") {
		t.Error("not blocked at the limit")
	}
	if l.Blocked("b") {
		t.Error("one key's failures blocked another key")
	}
}

func TestRateLimiterForgetsOldFailures(t *testing.T) {
	l := NewRateLimiter(2, time.Minute)
	l.Fail("a")
	l.Fail("a")
	l.mu.Lock()
	l.fails["a"][0] = time.Now().Add(-2 * time.Minute)
	l.mu.Unlock()
	if l.Blocked("a") {
		t.Error("an old failure still counts")
	}
	l.mu.Lock()
	l.fails["a"][0] = time.Now().Add(-2 * time.Minute)
	l.mu.Unlock()
	if l.Blocked("a") {
		t.Error("all failures are old")
	}
	l.mu.Lock()
	_, kept := l.fails["a"]
	l.mu.Unlock()
	if kept {
		t.Error("a key with no failures stays in memory")
	}
}

// --- concurrency ---

func TestConcurrentUse(t *testing.T) {
	s := withPassword(t)
	tok := login(t, s)
	d, k := pairDevice(t, s, "Phone")
	limiter := NewRateLimiter(3, time.Second)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				s.ValidateCookie(tok)
				s.TouchDevice(d.ID)
				s.Devices()
				s.NeedsSetup()
				limiter.Fail("ip")
				limiter.Blocked("ip")
				if n, err := s.Challenge(d.ID); err == nil {
					if bearer, _, err := s.DeviceLogin(d.ID, n, k.sign(n)); err == nil {
						s.ValidateBearer(bearer)
					}
				}
			}
		}()
	}
	wg.Wait()
	if ok, _ := s.ValidateCookie(tok); !ok {
		t.Error("the session was lost")
	}
}
