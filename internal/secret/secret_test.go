package secret

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func openBox(t *testing.T, path string) *Box {
	t.Helper()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpenCreatesAPrivateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	openBox(t, path)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 32 {
		t.Errorf("key is %d bytes, want 32", fi.Size())
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode %o, want 600", got)
	}
}

func TestKeySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	blob, err := openBox(t, path).Seal([]byte("private key"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := openBox(t, path).Open(blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "private key" {
		t.Errorf("got %q", got)
	}
}

func TestOpenRefusesABadKeyAndKeepsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	short := []byte("too short")
	if err := os.WriteFile(path, short, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("a key that is not 32 bytes must be refused")
	}
	// Writing a new key here would make every stored credential unreadable.
	if got, _ := os.ReadFile(path); !bytes.Equal(got, short) {
		t.Errorf("key file was changed to %q", got)
	}
}

func TestOpenReportsAnUnreadableKey(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Error("a directory is not a key")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "no", "such", "secret.key")); err == nil {
		t.Error("a key that cannot be written must fail")
	}
}

func TestSealHidesThePlaintextAndUsesAFreshNonce(t *testing.T) {
	b := openBox(t, filepath.Join(t.TempDir(), "k"))
	plain := []byte("private key material")
	first, err := b.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first, plain) {
		t.Error("the plaintext is visible in the blob")
	}
	if bytes.Equal(first, second) {
		t.Error("two seals of the same text are identical: the nonce repeats")
	}
}

func TestSealOpensEmptyPlaintext(t *testing.T) {
	b := openBox(t, filepath.Join(t.TempDir(), "k"))
	blob, err := b.Seal(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Open(blob)
	if err != nil || len(got) != 0 {
		t.Errorf("got %q err=%v", got, err)
	}
}

func TestOpenRefusesAnyChangedByte(t *testing.T) {
	b := openBox(t, filepath.Join(t.TempDir(), "k"))
	blob, err := b.Seal([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range blob {
		bad := bytes.Clone(blob)
		bad[i] ^= 0x01
		if _, err := b.Open(bad); err == nil {
			t.Errorf("byte %d changed and the blob still opened", i)
		}
	}
}

func TestOpenRefusesShortBlobsAndOtherKeys(t *testing.T) {
	dir := t.TempDir()
	a, other := openBox(t, filepath.Join(dir, "a")), openBox(t, filepath.Join(dir, "b"))
	blob, err := a.Seal([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(blob); err == nil {
		t.Error("another key opened the blob")
	}
	for _, n := range []int{0, 1, a.aead.NonceSize() - 1, a.aead.NonceSize()} {
		if _, err := a.Open(blob[:n]); err == nil {
			t.Errorf("a %d byte blob opened", n)
		}
	}
}
