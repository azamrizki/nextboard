package vault

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func proof(b byte) string {
	raw := make([]byte, proofBytes)
	raw[0] = b
	return base64.StdEncoding.EncodeToString(raw)
}

func TestPinOpensOnlyWithTheRightProof(t *testing.T) {
	s, _ := ready(t)
	id, secret, err := s.CreatePin(proof(1))
	if err != nil {
		t.Fatal(err)
	}
	got, left, err := s.OpenPin(id, proof(1))
	if err != nil || got != secret || left != MaxPinAttempts {
		t.Fatalf("right proof: %q %d %v", got, left, err)
	}
	got, left, err = s.OpenPin(id, proof(2))
	if !errors.Is(err, ErrWrongPin) || got != "" || left != MaxPinAttempts-1 {
		t.Fatalf("wrong proof: %q %d %v", got, left, err)
	}
}

func TestWrongPinsUseItUp(t *testing.T) {
	s, _ := ready(t)
	id, _, err := s.CreatePin(proof(1))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxPinAttempts; i++ {
		if _, left, err := s.OpenPin(id, proof(2)); !errors.Is(err, ErrWrongPin) || left != MaxPinAttempts-i {
			t.Fatalf("try %d: %d %v", i, left, err)
		}
	}
	if _, _, err := s.OpenPin(id, proof(2)); !errors.Is(err, ErrPinGone) {
		t.Fatalf("last try: %v", err)
	}
	// Gone even for the right one.
	if _, _, err := s.OpenPin(id, proof(1)); !errors.Is(err, ErrPinGone) {
		t.Fatalf("right proof after that: %v", err)
	}
}

func TestARightPinStartsTheCountOver(t *testing.T) {
	s, _ := ready(t)
	id, _, _ := s.CreatePin(proof(1))
	for range MaxPinAttempts - 1 {
		if _, _, err := s.OpenPin(id, proof(2)); !errors.Is(err, ErrWrongPin) {
			t.Fatal(err)
		}
	}
	if _, _, err := s.OpenPin(id, proof(1)); err != nil {
		t.Fatal(err)
	}
	if _, left, err := s.OpenPin(id, proof(2)); !errors.Is(err, ErrWrongPin) || left != MaxPinAttempts-1 {
		t.Fatalf("after a right one: %d %v", left, err)
	}
}

func TestRestartingTheHubGivesNoTriesBack(t *testing.T) {
	s, st := ready(t)
	id, _, _ := s.CreatePin(proof(1))
	for range MaxPinAttempts - 1 {
		if _, _, err := s.OpenPin(id, proof(2)); !errors.Is(err, ErrWrongPin) {
			t.Fatal(err)
		}
	}
	if _, _, err := New(st).OpenPin(id, proof(2)); !errors.Is(err, ErrPinGone) {
		t.Fatalf("after a restart: %v", err)
	}
}

func TestPinsNeverLeaveTheHub(t *testing.T) {
	s, st := ready(t)
	_, secret, _ := s.CreatePin(proof(1))
	if _, err := s.Put("item-0001", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(st.Path(fileName))
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), proof(1)) {
		t.Fatal("neither the secret nor the proof may be stored: only what checks them")
	}
	v, _ := s.Get()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"pins"`) || strings.Contains(string(out), `"check"`) {
		t.Fatalf("the view carries PINs: %s", out)
	}
}

func TestChangingThePasswordTurnsPinsOff(t *testing.T) {
	s, _ := ready(t)
	id, _, _ := s.CreatePin(proof(1))
	if _, err := s.SetKey(goodKey(), 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OpenPin(id, proof(1)); !errors.Is(err, ErrPinGone) {
		t.Fatalf("after a new password: %v", err)
	}
}

func TestResetTurnsPinsOff(t *testing.T) {
	s, _ := ready(t)
	id, _, _ := s.CreatePin(proof(1))
	if _, err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Init(goodKey()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OpenPin(id, proof(1)); !errors.Is(err, ErrPinGone) {
		t.Fatalf("after a reset: %v", err)
	}
}

func TestDeletingPins(t *testing.T) {
	s, _ := ready(t)
	a, _, _ := s.CreatePin(proof(1))
	b, _, _ := s.CreatePin(proof(2))
	if err := s.DeletePin(a); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePin(a); err != nil {
		t.Fatalf("twice: %v", err)
	}
	if _, _, err := s.OpenPin(a, proof(1)); !errors.Is(err, ErrPinGone) {
		t.Fatalf("deleted: %v", err)
	}
	if _, _, err := s.OpenPin(b, proof(2)); err != nil {
		t.Fatalf("the other one: %v", err)
	}
	if err := s.ClearPins(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OpenPin(b, proof(2)); !errors.Is(err, ErrPinGone) {
		t.Fatalf("cleared: %v", err)
	}
}

func TestPinProofsAreChecked(t *testing.T) {
	s, _ := ready(t)
	for _, bad := range []string{"", "123456", b64(16), "not base64!"} {
		if _, _, err := s.CreatePin(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("create %q: %v", bad, err)
		}
		if _, _, err := s.OpenPin("whatever-id", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("open %q: %v", bad, err)
		}
	}
	absent, _ := open(t)
	if _, _, err := absent.CreatePin(proof(1)); !errors.Is(err, ErrAbsent) {
		t.Fatalf("no vault: %v", err)
	}
}

func TestTooManyPinsDropTheOldest(t *testing.T) {
	s, _ := ready(t)
	first, _, _ := s.CreatePin(proof(1))
	p := s.d.Pins[first]
	p.CreatedAt = p.CreatedAt.Add(-1000000000000)
	s.d.Pins[first] = p
	var last string
	for i := range maxPins {
		last, _, _ = s.CreatePin(proof(byte(i + 2)))
	}
	if len(s.d.Pins) != maxPins {
		t.Fatalf("%d pins", len(s.d.Pins))
	}
	if _, _, err := s.OpenPin(first, proof(1)); !errors.Is(err, ErrPinGone) {
		t.Fatalf("the oldest: %v", err)
	}
	if _, _, err := s.OpenPin(last, proof(byte(maxPins+1))); err != nil {
		t.Fatalf("the newest: %v", err)
	}
}

func TestDamagedPinSendsTheFileToThePreviousCopy(t *testing.T) {
	s, st := ready(t)
	if _, err := s.Put("item-0001", b64(64), 0); err != nil {
		t.Fatal(err)
	}
	id, _, _ := s.CreatePin(proof(1))
	raw, _ := os.ReadFile(st.Path(fileName))
	damaged := strings.Replace(string(raw), `"key":"`, `"key":"!`, 1)
	if damaged == string(raw) || !strings.Contains(string(raw), id) {
		t.Fatalf("test setup: %s", raw)
	}
	if err := os.WriteFile(st.Path(fileName), []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := New(st).Get(); err != nil || len(v.Items) != 1 {
		t.Fatalf("after damage: %+v %v", v, err)
	}
}
